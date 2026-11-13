package gitworkbench

import (
	"context"
	"slices"
	"strings"
)

/**
 * [INPUT]: Runner.Backend 的 PatchBackend（LazyGit 的 PatchBuilder 与 PatchCommands）、RequireCleanWorktree、guardedRewrite
 * [OUTPUT]: PatchSource（某提交里某文件的可选行）与 RewriteWithPatch——把一个提交里选中的改动移除 / 拆成新提交 / 并入另一个提交 / 退回工作区
 * [POS]: Custom Patch：以「行」为粒度改写历史；与 RewriteHistory 同一套守卫（干净工作区、附着分支、失败即中止变基）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// PatchAction is what RewriteWithPatch does with the selected changes.
type PatchAction string

const (
	PatchRemove PatchAction = "remove" // delete the changes from the commit; they are gone
	PatchSplit  PatchAction = "split"  // pull them into a new commit right after the commit
	PatchMove   PatchAction = "move"   // move them into another commit of the branch (Dest)
	PatchUndo   PatchAction = "undo"   // take them out of the commit and back into the work tree, staged
)

// PatchSourceLine is one line of the diff a selection is made from. Index is
// what PatchFile.Lines refers to; Selectable lines are the added and removed
// ones (headers, hunk markers and context cannot be picked).
type PatchSourceLine struct {
	Index      int
	Text       string
	Selectable bool
}

// PatchFile is the part of one file's diff in the commit that is selected:
// all of it (Whole, also a rename, a deletion or an added file) or the lines
// whose PatchSourceLine.Index is listed.
type PatchFile struct {
	Path         string
	PreviousPath string // the old path of a renamed file
	Whole        bool
	Lines        []int
}

// PatchOp is one Custom Patch operation on the checked-out branch. Commit is
// the full hash the changes come from; Dest the one they move into (move
// only); Message the new commit's message (split only).
type PatchOp struct {
	Action  PatchAction
	Commit  string
	Dest    string
	Message string
	Files   []PatchFile
}

// PatchBackend implements Custom Patch. Runner has validated the operation,
// required a clean tree and an attached branch and holds the repository's
// lock; a rebase it leaves stopped is aborted by the Runner.
type PatchBackend interface {
	PatchSource(ctx context.Context, root, commit, path, previousPath string) ([]PatchSourceLine, error)
	RewriteWithPatch(ctx context.Context, root string, op PatchOp) error
}

// MaxPatchFiles bounds how many files one operation selects from.
const MaxPatchFiles = 500

// PatchSource lists the lines of a file's diff in a commit that a selection
// can name. Reading only: no state changes.
func (r *Runner) PatchSource(ctx context.Context, root, commit, path, previousPath string) ([]PatchSourceLine, error) {
	pb, ok := r.Backend.(PatchBackend)
	if !ok {
		return nil, refusal("patch", "this build has no custom-patch backend")
	}
	if !validFullCommitID(commit) {
		return nil, refusal("patch", "a full Git commit hash is required")
	}
	if err := validPatchPath(path); err != nil {
		return nil, err
	}
	if previousPath != "" {
		if err := validPatchPath(previousPath); err != nil {
			return nil, err
		}
	}
	return pb.PatchSource(ctx, root, commit, path, previousPath)
}

// RewriteWithPatch applies op to the checked-out branch. Any failure,
// including a conflict, aborts the rebase and leaves the repository as it was.
func (r *Runner) RewriteWithPatch(ctx context.Context, root string, op PatchOp) error {
	if err := validatePatchOp(op); err != nil {
		return err
	}
	pb, ok := r.Backend.(PatchBackend)
	if !ok {
		return refusal("patch", "this build has no custom-patch backend")
	}
	if r.CurrentBranch(ctx, root) == "" {
		return refusal("patch", "check out a branch first; HEAD is detached")
	}
	if err := r.RequireCleanWorktree(ctx, root); err != nil {
		return err
	}
	return r.guardedRewrite(ctx, root, func() error { return pb.RewriteWithPatch(ctx, root, op) })
}

func validPatchPath(p string) error {
	switch {
	case p == "" || strings.ContainsAny(p, "\x00\n\r"):
		return refusal("patch", "a file path is required")
	case strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../"):
		return refusal("patch", "a file path inside the repository is required")
	}
	return nil
}

func validatePatchOp(op PatchOp) error {
	if !validFullCommitID(op.Commit) {
		return refusal("patch", "a full Git commit hash is required")
	}
	switch op.Action {
	case PatchSplit:
		if strings.TrimSpace(op.Message) == "" {
			return refusal("patch", "the new commit needs a message")
		}
	case PatchRemove, PatchUndo:
		if op.Message != "" {
			return refusal("patch", "%s takes no message", op.Action)
		}
	case PatchMove:
		if !validFullCommitID(op.Dest) || op.Dest == op.Commit {
			return refusal("patch", "move needs the full hash of a different commit")
		}
		if op.Message != "" {
			return refusal("patch", "move takes no message")
		}
	default:
		return refusal("patch", "unknown action %q", op.Action)
	}
	if op.Action != PatchMove && op.Dest != "" {
		return refusal("patch", "%s takes no destination", op.Action)
	}
	if len(op.Files) == 0 || len(op.Files) > MaxPatchFiles {
		return refusal("patch", "select changes in between 1 and %d files", MaxPatchFiles)
	}
	seen := map[string]bool{}
	for _, f := range op.Files {
		if err := validPatchPath(f.Path); err != nil {
			return err
		}
		if f.PreviousPath != "" {
			if err := validPatchPath(f.PreviousPath); err != nil {
				return err
			}
		}
		if seen[f.Path] {
			return refusal("patch", "a file is listed twice")
		}
		seen[f.Path] = true
		if f.Whole == (len(f.Lines) > 0) {
			return refusal("patch", "choose either the whole file or some of its lines")
		}
		for _, n := range f.Lines {
			if n < 0 {
				return refusal("patch", "a line index cannot be negative")
			}
		}
	}
	return nil
}

// PatchLinesFor turns a selection made on the diff the user sees (picks are
// {hunk, line index in Hunk.Lines}) into the PatchSourceLine indices
// RewriteWithPatch takes. The two diffs are read by different code, so every
// line is matched by kind and text first: a file whose two readings differ is
// refused (select it whole instead) rather than patched on a guess.
func PatchLinesFor(src []PatchSourceLine, hunks []Hunk, picks map[[2]int]bool) ([]int, error) {
	var perHunk [][]int
	inHunk := false
	for _, l := range src {
		switch {
		case strings.HasPrefix(l.Text, "@@"):
			inHunk = true
			perHunk = append(perHunk, nil)
		case strings.HasPrefix(l.Text, "diff --git"):
			inHunk = false
		case inHunk && l.Text != "" && strings.ContainsRune(" +-", rune(l.Text[0])):
			perHunk[len(perHunk)-1] = append(perHunk[len(perHunk)-1], l.Index)
		}
	}
	mismatch := refusal("patch", "the diff shown here does not line up with the one Git rewrites; select the whole file instead")
	if len(perHunk) != len(hunks) {
		return nil, mismatch
	}
	text := map[int]string{}
	for _, l := range src {
		text[l.Index] = l.Text
	}
	for h, idx := range perHunk {
		if len(idx) != len(hunks[h].Lines) {
			return nil, mismatch
		}
		for i, at := range idx {
			want, got := hunks[h].Lines[i], text[at]
			kind := KindContext
			switch got[0] {
			case '+':
				kind = KindAdd
			case '-':
				kind = KindDelete
			}
			if kind != want.Kind || got[1:] != want.Text {
				return nil, mismatch
			}
		}
	}
	var out []int
	for key := range picks {
		h, i := key[0], key[1]
		if h < 0 || h >= len(hunks) || i < 0 || i >= len(hunks[h].Lines) || hunks[h].Lines[i].Kind == KindContext {
			return nil, refusal("patch", "a picked line is not a changed line")
		}
		out = append(out, perHunk[h][i])
	}
	slices.Sort(out)
	return out, nil
}
