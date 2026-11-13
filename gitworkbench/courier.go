package gitworkbench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

/**
 * [INPUT]: Runner（Read / runStdin / ApplyPatch / journaledApplyPatch）、工作区临时索引补丁（workingPatch）、Hunk 与 hunkBody
 * [OUTPUT]: Change Courier：把一个文件、或其中选中的行（来自工作区或一个提交）做成补丁（CourierPatch），在另一个 worktree 上预检（PreviewCourier：干净才可送，否则给出原因）、送达（DeliverCourier：只进目标的工作区、不暂存，走目标仓库的 Safety Journal）、以及 Move 时把行从源工作区丢弃（DiscardCourierLines）
 * [POS]: 2026-11-12 决定第 (13) 条的内核：补丁只在本模块里生成，预检与送达绑同一份补丁文本；严格——`git apply --check` 不过就一个字节都不动
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// CourierSource names what to send: one file's whole change or just the picked
// lines of it, from the working tree (Commit empty) or from one commit.
//
// Picks are {hunk index, line index in Hunk.Lines} over Hunks, the diff the
// user picked on: for the working tree that is the side shown (index→worktree
// or HEAD→index), for a commit its own diff. A nil Picks is the whole file.
type CourierSource struct {
	Path    string
	OldPath string // a rename's source
	Commit  string // full hash; empty = the working tree
	Hunks   []Hunk
	Picks   map[[2]int]bool
}

func (s CourierSource) renamed() bool { return s.OldPath != "" && s.OldPath != s.Path }

// CourierPatch makes the patch of the sources, one after the other. A whole
// file keeps what Git itself writes (binary data, modes, renames); picked
// lines are cut against the side they were picked on, so a destination that
// does not hold that side refuses them in the preview.
func (r *Runner) CourierPatch(ctx context.Context, root string, srcs ...CourierSource) (string, error) {
	var out strings.Builder
	for _, src := range srcs {
		text, err := r.courierOne(ctx, root, src)
		if err != nil {
			return "", err
		}
		out.WriteString(text)
		if out.Len() > MaxPatchFileBytes {
			return "", refusal("courier", "the changes are larger than %d MiB", MaxPatchFileBytes>>20)
		}
	}
	if out.Len() == 0 {
		return "", refusal("courier", "there is nothing to send")
	}
	return out.String(), nil
}

func (r *Runner) courierOne(ctx context.Context, root string, src CourierSource) (string, error) {
	path, ok := cleanRelPath(src.Path)
	if !ok {
		return "", refusal("courier", "not a path inside the repository")
	}
	paths := []string{path}
	if src.renamed() {
		old, ok := cleanRelPath(src.OldPath)
		if !ok {
			return "", refusal("courier", "not a path inside the repository")
		}
		paths = append(paths, old)
	}
	if src.Picks != nil {
		if src.renamed() {
			return "", refusal("courier", "%s was renamed; send the whole file", path)
		}
		text, ok := courierLines(path, src, false)
		if !ok {
			return "", refusal("courier", "no changed lines are selected in %s", path)
		}
		return text, nil
	}
	if src.Commit != "" {
		if !validFullCommitID(src.Commit) {
			return "", refusal("courier", "a full Git commit hash is required")
		}
		parents, err := r.Read(ctx, root, "rev-list", "--parents", "-n", "1", src.Commit)
		if err != nil {
			return "", err
		}
		if len(strings.Fields(string(parents))) > 2 {
			return "", refusal("courier", "a merge commit has no single change to send")
		}
		out, err := r.Read(ctx, root, append([]string{"--literal-pathspecs", "show", "--format=", "--binary", "--no-color", "-M", src.Commit, "--"}, paths...)...)
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
	out, err := r.workingPatch(ctx, root, paths)
	if err != nil {
		if ge, ok := GitError(err); ok && ge.Class == ErrRefusal {
			return "", refusal("courier", "%s has no changes to send", path)
		}
		return "", err
	}
	return string(out), nil
}

// courierLines cuts the picked lines of every hunk into one patch of the file.
// Forward (the default) is applied to the side the hunks start from and makes
// the picked changes; reverse is applied with --reverse to the side the hunks
// end on and takes them out again (Move's discard). A hunk's start is where
// the side it is applied to has it, so a pick in one hunk never shifts
// another's; the other side's start follows from the lines before it.
func courierLines(path string, src CourierSource, reverse bool) (string, bool) {
	var hunks strings.Builder
	delta, picked := 0, false
	for hi, h := range src.Hunks {
		sel := map[int]bool{}
		for k, on := range src.Picks {
			if on && k[0] == hi {
				sel[k[1]] = true
			}
		}
		body, oldN, newN, ok := hunkBody(h, sel, reverse)
		if !ok {
			continue
		}
		picked = true
		oldStart, newStart := h.OldStart, h.OldStart+delta
		if reverse {
			oldStart, newStart = h.NewStart-delta, h.NewStart
		}
		hunks.WriteString("@@ -" + hunkRange(oldStart, oldN) + " +" + hunkRange(newStart, newN) + " @@\n" + body)
		delta += newN - oldN
	}
	if !picked {
		return "", false
	}
	return "diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n" + hunks.String(), true
}

// CourierFile is one file a patch touches, with its line counts.
type CourierFile struct {
	Path    string
	Added   int
	Removed int
}

// CourierPreview is what `git apply --check` says about a patch on a
// destination worktree. Clean is the only state that may be delivered;
// otherwise Problem says why, in Git's own words trimmed to the first lines.
type CourierPreview struct {
	Clean   bool
	Files   []CourierFile
	Problem string
}

// PreviewCourier checks the patch against the destination worktree without
// changing anything. A destination with a merge, rebase, revert or cherry-pick
// in progress is refused: its working tree is not the quiet one a patch fits.
func (r *Runner) PreviewCourier(ctx context.Context, dest, patch string) (CourierPreview, error) {
	if strings.TrimSpace(patch) == "" {
		return CourierPreview{}, refusal("courier", "there is nothing to send")
	}
	if len(patch) > MaxPatchFileBytes {
		return CourierPreview{}, refusal("courier", "the changes are larger than %d MiB", MaxPatchFileBytes>>20)
	}
	st, err := r.SequencerState(ctx, dest)
	if err != nil {
		return CourierPreview{}, err
	}
	if st.Active() {
		return CourierPreview{Problem: "The destination is in the middle of a merge, rebase or similar; finish or abort it there first."}, nil
	}
	if _, err := r.runStdin(ctx, OpRead, ReadTimeout, dest, patch, []string{"apply", "--check", "-"}); err != nil {
		if ge, ok := GitError(err); ok && strings.TrimSpace(ge.Stderr) != "" {
			return CourierPreview{Problem: courierProblem(ge.Stderr)}, nil
		}
		return CourierPreview{}, err
	}
	out, err := r.runStdin(ctx, OpRead, ReadTimeout, dest, patch, []string{"apply", "--numstat", "-z", "-"})
	if err != nil {
		return CourierPreview{}, err
	}
	return CourierPreview{Clean: true, Files: courierFiles(string(out))}, nil
}

// courierProblem keeps the first lines of Git's complaint, without the
// "error: " prefix and without warnings.
func courierProblem(stderr string) string {
	var lines []string
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "error:"))
		if l == "" || strings.HasPrefix(l, "warning:") {
			continue
		}
		lines = append(lines, l)
		if len(lines) == 4 {
			break
		}
	}
	if len(lines) == 0 {
		return "The changes do not apply there."
	}
	return strings.Join(lines, "\n")
}

// courierFiles reads `apply --numstat -z`: "added\tremoved\tpath\0", and for a
// rename "added\tremoved\t\0old\0new\0".
func courierFiles(numstat string) []CourierFile {
	toks := strings.Split(numstat, "\x00")
	var files []CourierFile
	for i := 0; i < len(toks); i++ {
		f := strings.SplitN(toks[i], "\t", 3)
		if len(f) != 3 {
			continue
		}
		path := f[2]
		if path == "" && i+2 < len(toks) {
			path = toks[i+2]
			i += 2
		}
		a, d := 0, 0 // a binary file counts "-"
		if n, ok := atoiLoose(f[0]); ok {
			a = n
		}
		if n, ok := atoiLoose(f[1]); ok {
			d = n
		}
		files = append(files, CourierFile{Path: path, Added: a, Removed: d})
	}
	return files
}

func atoiLoose(s string) (int, bool) {
	n := 0
	if s == "" {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// DeliverCourier applies a patch that PreviewCourier called clean to the
// destination's working tree: unstaged, the index and every ref untouched, and
// under that repository's Safety Journal (Undo Git Action there takes it back).
// The check runs again right before, so a destination that changed since the
// preview is refused.
func (r *Runner) DeliverCourier(ctx context.Context, dest, patch string) error {
	pre, err := r.PreviewCourier(ctx, dest, patch)
	if err != nil {
		return err
	}
	if !pre.Clean {
		return refusal("courier", "%s", pre.Problem)
	}
	return r.applyViaFile(ctx, dest, "Sent changes.patch", patch, false)
}

// DiscardCourierLines takes the picked lines of a working-tree source out of
// its working tree again (Move): the reverse of what CourierPatch cut, under
// the Safety Journal. The lines must still be there as they were read.
func (r *Runner) DiscardCourierLines(ctx context.Context, root string, src CourierSource) error {
	path, ok := cleanRelPath(src.Path)
	if !ok || src.Commit != "" || src.Picks == nil || src.renamed() {
		return refusal("courier", "only picked lines of a working-tree file can be taken out this way")
	}
	text, ok := courierLines(path, src, true)
	if !ok {
		return refusal("courier", "no changed lines are selected in %s", path)
	}
	return r.applyViaFile(ctx, root, "Moved changes.patch", text, true)
}

// applyViaFile writes the patch under a readable name (the Recovery list shows
// it) and applies it to the working tree only, checking first.
func (r *Runner) applyViaFile(ctx context.Context, root, name, patch string, reverse bool) error {
	dir, err := os.MkdirTemp("", "solco-courier-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, name)
	if err := os.WriteFile(file, []byte(patch), 0o600); err != nil {
		return err
	}
	if !reverse {
		return r.ApplyPatch(ctx, root, file)
	}
	if r.Journal { // `git apply` is all or nothing: a source that changed refuses here
		return r.journaledApplyPatch(ctx, root, file, true)
	}
	_, err = r.Mutate(ctx, root, "apply", "--whitespace=nowarn", "--reverse", "--", file)
	return err
}
