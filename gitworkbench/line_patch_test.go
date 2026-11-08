package gitworkbench

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// numbered writes lines "line 1" … "line n" (each with a trailing newline).
func numberedLines(n int, edit map[int]string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if text, ok := edit[i]; ok {
			if text == "\x00" { // deleted
				continue
			}
			b.WriteString(text + "\n")
			continue
		}
		b.WriteString("line " + itoa(i) + "\n")
	}
	return b.String()
}

func itoa(n int) string { return strconv.Itoa(n) }

// hunksOf reads the unstaged (staged=false) or staged side's hunks of path.
func hunksOf(t *testing.T, root string, staged bool, path string) []Hunk {
	t.Helper()
	args := []string{"diff", "--no-color", "--no-ext-diff", "-U1"}
	if staged {
		args = append(args, "--cached")
	}
	out := runGit(t, root, append(args, "--", path)...)
	fp := parsePatch([]byte(out))
	if fp == nil {
		t.Fatalf("no patch for %s:\n%s", path, out)
	}
	return fp.hunks
}

// changeLines returns the indices of a hunk's additions and deletions.
func changeLines(h Hunk) []int {
	var idx []int
	for i, l := range h.Lines {
		if l.Kind != KindContext {
			idx = append(idx, i)
		}
	}
	return idx
}

func selection(idx ...int) map[int]bool {
	m := map[int]bool{}
	for _, i := range idx {
		m[i] = true
	}
	return m
}

// Staging some lines of one hunk puts exactly those into the index and leaves
// every other change in the worktree; unstaging them reverses it.
func TestApplyLinesStagesAndUnstagesTheSelection(t *testing.T) {
	root, r := tempRepo(t)
	writeRepoFile(t, root, "f.txt", numberedLines(20, nil))
	runGit(t, root, "add", "f.txt")
	runGit(t, root, "commit", "-q", "-m", "f")
	// Hunk 1: line 2 edited (a delete + an add), a new line after 3 (add).
	// Hunk 2: line 15 deleted.
	writeRepoFile(t, root, "f.txt", numberedLines(20, map[int]string{2: "line two", 3: "line 3\nextra", 15: "\x00"}))

	hunks := hunksOf(t, root, false, "f.txt")
	if len(hunks) != 2 {
		t.Fatalf("hunks = %d, want 2 (%+v)", len(hunks), hunks)
	}
	ctx := context.Background()

	// Stage only the edit of line 2 (its delete and its add), not the extra line.
	var del2, add2 int
	for i, l := range hunks[0].Lines {
		switch {
		case l.Kind == KindDelete && l.Text == "line 2":
			del2 = i
		case l.Kind == KindAdd && l.Text == "line two":
			add2 = i
		}
	}
	if err := r.ApplyLines(ctx, root, "f.txt", hunks[0], selection(del2, add2), false); err != nil {
		t.Fatal(err)
	}
	staged := runGit(t, root, "diff", "--cached", "-U0", "--", "f.txt")
	if !strings.Contains(staged, "-line 2") || !strings.Contains(staged, "+line two") {
		t.Fatalf("the selected edit is not staged:\n%s", staged)
	}
	if strings.Contains(staged, "extra") || strings.Contains(staged, "line 15") {
		t.Fatalf("unselected changes were staged:\n%s", staged)
	}
	left := runGit(t, root, "diff", "-U0", "--", "f.txt")
	if !strings.Contains(left, "+extra") || !strings.Contains(left, "-line 15") || strings.Contains(left, "line two") {
		t.Fatalf("the worktree diff lost or kept the wrong changes:\n%s", left)
	}
	if got := runGit(t, root, "show", ":f.txt"); strings.Contains(got, "extra") || !strings.Contains(got, "line two") {
		t.Fatalf("index content wrong:\n%s", got)
	}

	// The second hunk (line 15 deleted), staged as a whole after the first moved the offsets.
	hunks = hunksOf(t, root, false, "f.txt")
	var second Hunk
	for _, h := range hunks {
		for _, l := range h.Lines {
			if l.Kind == KindDelete && l.Text == "line 15" {
				second = h
			}
		}
	}
	if err := r.ApplyLines(ctx, root, "f.txt", second, selection(changeLines(second)...), false); err != nil {
		t.Fatal(err)
	}
	if staged = runGit(t, root, "diff", "--cached", "-U0", "--", "f.txt"); !strings.Contains(staged, "-line 15") {
		t.Fatalf("second hunk not staged:\n%s", staged)
	}

	// Unstage just the "line two" edit again; the line-15 deletion stays staged.
	stagedHunks := hunksOf(t, root, true, "f.txt")
	var first Hunk
	for _, h := range stagedHunks {
		for _, l := range h.Lines {
			if l.Kind == KindAdd && l.Text == "line two" {
				first = h
			}
		}
	}
	if err := r.ApplyLines(ctx, root, "f.txt", first, selection(changeLines(first)...), true); err != nil {
		t.Fatal(err)
	}
	staged = runGit(t, root, "diff", "--cached", "-U0", "--", "f.txt")
	if strings.Contains(staged, "line two") || !strings.Contains(staged, "-line 15") {
		t.Fatalf("unstage touched the wrong lines:\n%s", staged)
	}
	// Nothing in the worktree changed through all of it.
	if got := runGit(t, root, "diff", "--stat", "HEAD", "--", "f.txt"); !strings.Contains(got, "f.txt") {
		t.Fatalf("the worktree no longer differs from HEAD:\n%s", got)
	}
}

// Unselected lines of the SAME hunk stay behind: stage the add, keep the delete.
func TestApplyLinesKeepsUnselectedLinesOfTheHunk(t *testing.T) {
	root, r := tempRepo(t)
	writeRepoFile(t, root, "g.txt", numberedLines(6, nil))
	runGit(t, root, "add", "g.txt")
	runGit(t, root, "commit", "-q", "-m", "g")
	writeRepoFile(t, root, "g.txt", numberedLines(6, map[int]string{3: "line three"}))

	h := hunksOf(t, root, false, "g.txt")[0]
	add := -1
	for i, l := range h.Lines {
		if l.Kind == KindAdd {
			add = i
		}
	}
	if err := r.ApplyLines(context.Background(), root, "g.txt", h, selection(add), false); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, root, "show", ":g.txt"); !strings.Contains(got, "line 3\n") || !strings.Contains(got, "line three\n") {
		t.Fatalf("only the addition should be in the index, so both versions show:\n%s", got)
	}
}

// A file without a final newline round-trips its marker.
func TestApplyLinesHandlesNoNewlineAtEndOfFile(t *testing.T) {
	root, r := tempRepo(t)
	writeRepoFile(t, root, "n.txt", "a\nb\nc")
	runGit(t, root, "add", "n.txt")
	runGit(t, root, "commit", "-q", "-m", "n")
	writeRepoFile(t, root, "n.txt", "a\nb\nc\nd")

	h := hunksOf(t, root, false, "n.txt")[0]
	if err := r.ApplyLines(context.Background(), root, "n.txt", h, selection(changeLines(h)...), false); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, root, "show", ":n.txt"); got != "a\nb\nc\nd" {
		t.Fatalf("index = %q, want the whole change staged", got)
	}
	if got := runGit(t, root, "diff", "--", "n.txt"); strings.TrimSpace(got) != "" {
		t.Fatalf("a fully staged file still differs in the worktree:\n%s", got)
	}
}

func TestLinePatchRefusesAnEmptySelection(t *testing.T) {
	h := Hunk{OldStart: 1, OldLines: 1, NewStart: 1, NewLines: 1, Lines: []DiffLine{
		{Kind: KindDelete, Text: "a"}, {Kind: KindAdd, Text: "b"}, {Kind: KindContext, Text: "c"},
	}}
	if _, ok := LinePatch("f", h, selection(2), false); ok {
		t.Fatal("a context line is not a change; the patch must be refused")
	}
	if _, ok := LinePatch("f", h, nil, false); ok {
		t.Fatal("an empty selection must be refused")
	}
	if err := (&Runner{}).ApplyLines(context.Background(), t.TempDir(), "f", h, nil, false); err == nil {
		t.Fatal("ApplyLines must refuse an empty selection before running Git")
	}
}
