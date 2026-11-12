package gitworkbench

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

/**
 * [INPUT]: Runner（Read / ReadBlob / runMutating / journaledWork）、`git merge-file`、index 里的 stage 1/2/3
 * [OUTPUT]: ReadConflict（一个冲突文件的三方内容与按块切好的合并结果，或不能编辑的原因）、ParseConflictMarkers / BuildConflictResult（块与选择 → 结果文本，纯函数）、ResolveWith（把用户给定的结果写进工作区并 git add；先留恢复点）、undoResolve（Recovery 找回：重新打开冲突并放回原工作区内容）
 * [POS]: 2026-11-12 决定第 (3) 条「冲突三栏编辑」的内核：界面是上两栏 Current | Incoming + 下 Result，Base 按需展开（VS Code 式）；合并块由 Git 自己算（merge-file --diff3，29 个字符的标记，内容里的 ======= 不会撞上），本包不重写 diff3
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// MaxConflictBytes bounds each version read for the conflict editor, and the
// result it writes back.
const MaxConflictBytes = 2 << 20

// conflictMarkerSize makes merge-file's markers long enough that a line in the
// file's own text (a Markdown "=======") is never mistaken for one.
const conflictMarkerSize = 29

// ConflictHunk is one region both sides changed. Each text keeps its line
// endings (a side that has no lines is "").
type ConflictHunk struct {
	Ours, Base, Theirs string
}

// ConflictSegment is either text both sides agree on (Hunk nil) or a hunk.
type ConflictSegment struct {
	Text string
	Hunk *ConflictHunk
}

// ConflictSides is what the editor shows for one unmerged file.
type ConflictSides struct {
	Path               string
	Ours, Base, Theirs string
	Segments           []ConflictSegment
	// Editable is false when the file cannot be merged line by line; Reason
	// then says why, and the whole-file choices stay.
	Editable bool
	Reason   string
}

// HunkChoice is what to put where a hunk was.
type HunkChoice int

const (
	ChoiceNone   HunkChoice = iota // not decided: the markers stay in the result
	ChoiceOurs                     // the current branch's lines
	ChoiceTheirs                   // the incoming lines
	ChoiceBoth                     // current, then incoming
	ChoiceBase                     // the common ancestor's lines
)

func withNewline(s string) string {
	if s != "" && !strings.HasSuffix(s, "\n") {
		return s + "\n"
	}
	return s
}

// BuildConflictResult joins the segments with one choice per hunk (a missing
// choice is ChoiceNone). Undecided hunks keep readable seven-character markers,
// which ResolveWith refuses to stage.
func BuildConflictResult(segs []ConflictSegment, choices []HunkChoice) string {
	var b strings.Builder
	n := 0
	for _, seg := range segs {
		if seg.Hunk == nil {
			b.WriteString(seg.Text)
			continue
		}
		choice := ChoiceNone
		if n < len(choices) {
			choice = choices[n]
		}
		n++
		h := seg.Hunk
		switch choice {
		case ChoiceOurs:
			b.WriteString(h.Ours)
		case ChoiceTheirs:
			b.WriteString(h.Theirs)
		case ChoiceBoth:
			b.WriteString(withNewline(h.Ours))
			b.WriteString(h.Theirs)
		case ChoiceBase:
			b.WriteString(h.Base)
		default:
			b.WriteString("<<<<<<< Current\n" + withNewline(h.Ours) + "=======\n" + withNewline(h.Theirs) + ">>>>>>> Incoming\n")
		}
	}
	return b.String()
}

// ParseConflictMarkers cuts merge-file's --diff3 output (markers of
// conflictMarkerSize characters, labelled Current / Base / Incoming) into
// segments. Text that merely resembles a marker is kept as text.
func ParseConflictMarkers(merged string) ([]ConflictSegment, error) {
	bar := func(c byte) string { return strings.Repeat(string(c), conflictMarkerSize) }
	open, mid, split, closing := bar('<')+" Current", bar('|')+" Base", bar('=')+"\n", bar('>')+" Incoming"
	var segs []ConflictSegment
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			segs = append(segs, ConflictSegment{Text: text.String()})
			text.Reset()
		}
	}
	lines := strings.SplitAfter(merged, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if !strings.HasPrefix(line, open) {
			text.WriteString(line)
			continue
		}
		flush()
		h := &ConflictHunk{}
		var ours, base, theirs strings.Builder
		cur, state := &ours, 0 // 0 ours, 1 base, 2 theirs
		closed := false
		for i++; i < len(lines); i++ {
			l := lines[i]
			switch {
			case state == 0 && strings.HasPrefix(l, mid):
				cur, state = &base, 1
			case state <= 1 && l == split:
				cur, state = &theirs, 2
			case state == 2 && strings.HasPrefix(l, closing):
				closed = true
			default:
				cur.WriteString(l)
			}
			if closed {
				break
			}
		}
		if !closed {
			return nil, errors.New("the merge result has an unterminated conflict block")
		}
		h.Ours, h.Base, h.Theirs = ours.String(), base.String(), theirs.String()
		segs = append(segs, ConflictSegment{Hunk: h})
	}
	flush()
	return segs, nil
}

// HasConflictMarkers reports whether text still holds a conflict block (the
// check ResolveWith applies, for a button that should wait for it).
func HasConflictMarkers(text string) bool { return hasConflictMarkers([]byte(text)) }

// conflictStages reads the stages of one unmerged path: which exist.
func (r *Runner) conflictStages(ctx context.Context, root, rel string) (map[int]string, error) {
	out, err := r.Read(ctx, root, "--literal-pathspecs", "ls-files", "-u", "-z", "--", rel)
	if err != nil {
		return nil, err
	}
	stages := map[int]string{}
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, _, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			continue
		}
		switch f[2] {
		case "1", "2", "3":
			stages[int(f[2][0]-'0')] = f[0] // the mode
		}
	}
	if len(stages) == 0 {
		return nil, &Error{Class: ErrStaleState, OpClass: OpRead, Op: "resolve", err: fmt.Errorf("%s has no unresolved conflict (anymore)", rel)}
	}
	return stages, nil
}

// ReadConflict reads the three versions of an unmerged file and the merge Git
// would make of them, cut into hunks. A file that cannot be edited line by
// line (a side deleted it, binary, too large, a link or submodule) comes back
// with Editable false and the reason.
func (r *Runner) ReadConflict(ctx context.Context, root, rel string) (ConflictSides, error) {
	sides := ConflictSides{Path: rel}
	stages, err := r.conflictStages(ctx, root, rel)
	if err != nil {
		return sides, err
	}
	for _, mode := range stages {
		if mode == "120000" || mode == "160000" {
			sides.Reason = "This is a link or a submodule: keep one version from the menu."
			return sides, nil
		}
	}
	if stages[2] == "" || stages[3] == "" {
		sides.Reason = "One side deleted this file: keep a version from the menu."
		return sides, nil
	}
	read := func(stage int) (string, error) {
		if stages[stage] == "" {
			return "", nil
		}
		data, err := r.ReadBlob(ctx, root, fmt.Sprintf(":%d:%s", stage, rel), MaxConflictBytes)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	var rerr error
	for _, side := range []struct {
		stage int
		dst   *string
	}{{1, &sides.Base}, {2, &sides.Ours}, {3, &sides.Theirs}} {
		if *side.dst, rerr = read(side.stage); rerr != nil {
			if ge, ok := GitError(rerr); ok && ge.Class == ErrTooLarge {
				sides.Reason = "This file is too large to merge here: keep a version from the menu."
				return sides, nil
			}
			return sides, rerr
		}
	}
	for _, text := range []string{sides.Base, sides.Ours, sides.Theirs} {
		if strings.IndexByte(text, 0) >= 0 {
			sides.Reason = "This file is binary: keep a version from the menu."
			return sides, nil
		}
	}
	merged, err := r.mergeFile(ctx, root, sides)
	if err != nil {
		return sides, err
	}
	sides.Segments, err = ParseConflictMarkers(merged)
	if err != nil {
		return sides, err
	}
	sides.Editable = true
	return sides, nil
}

// mergeFile runs `git merge-file -p --diff3` on the three versions in a
// scratch directory inside the repository's git directory. The exit status is
// the number of conflicts, so a small positive one is a result, not a failure.
func (r *Runner) mergeFile(ctx context.Context, root string, sides ConflictSides) (string, error) {
	jdir, err := r.journalDir(ctx, root)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(jdir), "tmp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(dir, "merge-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	files := map[string]string{"ours": sides.Ours, "base": sides.Base, "theirs": sides.Theirs}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte(text), 0o600); err != nil {
			return "", err
		}
	}
	out, err := r.Read(ctx, root, "merge-file", "-p", "--diff3", fmt.Sprintf("--marker-size=%d", conflictMarkerSize),
		"-L", "Current", "-L", "Base", "-L", "Incoming",
		filepath.Join(tmp, "ours"), filepath.Join(tmp, "base"), filepath.Join(tmp, "theirs"))
	if err != nil {
		ge, ok := GitError(err)
		if !ok || ge.ExitCode < 1 || ge.ExitCode > 127 || bytes.IndexByte(ge.out, 0) >= 0 {
			return "", err
		}
		return string(ge.out), nil
	}
	return string(out), nil
}

// ResolveWith settles one unmerged text file with the given result: it is
// written to the working tree and staged. Refused while the result still holds
// conflict markers, is too large, or the file is no longer in conflict. With
// the journal on, the working-tree file it replaces (the markers, or edits
// made elsewhere) is kept first, and Recovery can reopen the conflict.
func (r *Runner) ResolveWith(ctx context.Context, root, rel, content string) error {
	if len(content) > 2*MaxConflictBytes {
		return &Error{Class: ErrTooLarge, OpClass: OpMutate, Op: "resolve", err: errors.New("the result is too large")}
	}
	if strings.IndexByte(content, 0) >= 0 {
		return refusal("resolve", "the result contains a NUL byte")
	}
	if hasConflictMarkers([]byte(content)) {
		return refusal("resolve", "%s still contains conflict markers — decide every hunk first", rel)
	}
	if _, err := r.conflictStages(ctx, root, rel); err != nil {
		return err
	}
	target := filepath.Join(root, filepath.FromSlash(rel))
	if filepath.Clean(target) != target || !strings.HasPrefix(target, filepath.Clean(root)+string(filepath.Separator)) {
		return refusal("resolve", "not a path inside the repository")
	}
	write := func() error {
		mode := os.FileMode(0o644)
		if info, err := os.Stat(target); err == nil {
			mode = info.Mode().Perm()
		}
		tmp, err := os.CreateTemp(filepath.Dir(target), ".solco-resolve-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if _, err := tmp.WriteString(content); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Chmod(mode); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), target)
	}
	add := func() error {
		if err := write(); err != nil {
			return err
		}
		_, err := r.runMutating(ctx, root, mutateEnv(), "--literal-pathspecs", "add", "--", rel)
		return err
	}
	if !r.Journal {
		unlock, err := r.LockRepo(root, "resolve")
		if err != nil {
			return err
		}
		defer unlock()
		return add()
	}
	return r.journaledWork(ctx, root, Operation{Kind: OpKindResolveConflict, Paths: []string{rel}},
		func() (string, error) {
			if err := pathsSize(root, []string{rel}); err != nil {
				return "", err
			}
			return r.captureTree(ctx, root, "resolve conflict", func(env []string) error {
				_, err := r.runMutatingStdin(ctx, root, env, rel+"\x00", "--literal-pathspecs", "update-index", "--add", "--remove", "-z", "--stdin")
				return err
			}, "")
		},
		add, nil,
		func(op *Operation) {
			if tree, err := r.worktreeTree(ctx, root, []string{rel}); err == nil {
				op.After = tree
			}
		})
}

// undoResolve reopens the conflict a ResolveWith closed and puts the file
// back as it was on disk before: refused when the file changed since, when it
// is in conflict again, or when the merge moved on and Git no longer holds the
// conflict's three stages.
func (r *Runner) undoResolve(ctx context.Context, root string, op Operation) error {
	if err := r.verifySnapshot(ctx, root, op); err != nil {
		return err
	}
	if len(op.Paths) != 1 {
		return refusal("recover", "this entry is not readable")
	}
	rel := op.Paths[0]
	// Git keeps the resolve-undo data past the merge commit, so the paused
	// merge itself has to still be there.
	if st, err := r.SequencerState(ctx, root); err != nil {
		return err
	} else if !st.Active() {
		return &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "recover",
			err: fmt.Errorf("the merge has moved on, so %s can no longer be reopened", rel)}
	}
	if unmerged, err := r.UnmergedPaths(ctx, root); err != nil {
		return err
	} else {
		for _, p := range unmerged {
			if p == rel {
				return &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "recover", err: fmt.Errorf("%s is in conflict again", rel)}
			}
		}
	}
	now, err := r.worktreeTree(ctx, root, []string{rel})
	if err != nil {
		return err
	}
	if op.After == "" || now != op.After {
		return &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "recover",
			err: fmt.Errorf("%s changed after it was resolved; recovering would overwrite it", rel)}
	}
	if _, err := r.runMutating(ctx, root, mutateEnv(), "--literal-pathspecs", "update-index", "--unresolve", "--", rel); err != nil {
		return &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "recover",
			err: fmt.Errorf("the merge has moved on, so %s can no longer be reopened", rel)}
	}
	if _, err := r.Read(ctx, root, "--literal-pathspecs", "ls-tree", "--name-only", op.Snapshot, "--", rel); err == nil {
		if _, err := r.runMutating(ctx, root, mutateEnv(), "--literal-pathspecs", "restore", "--source="+op.Snapshot, "--worktree", "--", rel); err != nil {
			return err
		}
	}
	return nil
}
