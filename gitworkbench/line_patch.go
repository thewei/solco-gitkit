package gitworkbench

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// LinePatch builds the patch that stages — or, with unstage, takes back out of
// the index — only the selected lines of one hunk of a tracked file's text
// change. It is `git add -p`'s edit: lines outside the selection stay as they
// are on the side the patch is applied to.
//
// Stage: h is a hunk of index→worktree and the patch is applied to the index
// as it is. An unselected addition is left out; an unselected deletion stays
// as context (the index keeps the line).
//
// Unstage: h is a hunk of HEAD→index and the patch is applied in reverse to
// the index. An unselected addition stays as context (the index keeps it); an
// unselected deletion is left out (the index stays without it).
//
// selected holds indices into h.Lines; only additions and deletions count.
// It returns false when the selection holds no change line.
func LinePatch(path string, h Hunk, selected map[int]bool, unstage bool) (string, bool) {
	var body strings.Builder
	oldN, newN := 0, 0
	picked := false
	emit := func(prefix byte, l DiffLine) {
		body.WriteByte(prefix)
		body.WriteString(l.Text)
		body.WriteByte('\n')
		if l.NoNewline {
			body.WriteString("\\ No newline at end of file\n")
		}
	}
	for i, l := range h.Lines {
		switch l.Kind {
		case KindContext:
			emit(' ', l)
			oldN++
			newN++
		case KindAdd:
			switch {
			case selected[i]:
				emit('+', l)
				newN++
				picked = true
			case unstage: // the index keeps it
				emit(' ', l)
				oldN++
				newN++
			}
		case KindDelete:
			switch {
			case selected[i]:
				emit('-', l)
				oldN++
				picked = true
			case !unstage: // the index keeps it
				emit(' ', l)
				oldN++
				newN++
			}
		}
	}
	if !picked {
		return "", false
	}
	start := h.OldStart
	if unstage {
		start = h.NewStart
	}
	var out strings.Builder
	out.WriteString("diff --git a/" + path + " b/" + path + "\n")
	out.WriteString("--- a/" + path + "\n+++ b/" + path + "\n")
	out.WriteString("@@ -" + hunkRange(start, oldN) + " +" + hunkRange(start, newN) + " @@\n")
	out.WriteString(body.String())
	return out.String(), true
}

// hunkRange writes a unified-diff range: "start,count", or "start" for one line.
func hunkRange(start, count int) string {
	if count == 1 {
		return strconv.Itoa(start)
	}
	return strconv.Itoa(start) + "," + strconv.Itoa(count)
}

// ApplyLines stages (or unstages) the selected lines of one hunk of path's
// change in the index; the worktree is never touched. Tracked files only:
// the caller offers it for modified text files shown on one side.
func (r *Runner) ApplyLines(ctx context.Context, root, path string, h Hunk, selected map[int]bool, unstage bool) error {
	patch, ok := LinePatch(path, h, selected, unstage)
	if !ok {
		return errors.New("no changed lines selected")
	}
	args := []string{"apply", "--cached", "--recount", "--whitespace=nowarn"}
	if unstage {
		args = append(args, "--reverse")
	}
	_, err := r.Stdin(ctx, root, patch, append(args, "-")...)
	return err
}
