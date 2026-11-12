package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thewei/solco-gitkit/gitworkbench"
)

func writeFile(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// patchRepo: base (a, b), X (edits a twice, edits b, adds c), then "later"
// (adds d, or edits the line X added when dependent is true).
func patchRepo(t *testing.T, dependent bool) (root string, h map[string]string) {
	t.Helper()
	root = newRepo(t)
	h = map[string]string{}
	step := func(name string, files map[string]string) {
		for f, body := range files {
			writeFile(t, root, f, body)
		}
		git(t, root, "add", ".")
		git(t, root, "commit", "-q", "-m", name)
		h[name] = git(t, root, "rev-parse", "HEAD")
	}
	step("base", map[string]string{"a.txt": "l1\nl2\nl3\n", "b.txt": "b1\n"})
	step("X", map[string]string{"a.txt": "l1\nl2x\nl3\nl4\n", "b.txt": "b1\nb2\n", "c.txt": "c\n"})
	if dependent {
		step("later", map[string]string{"a.txt": "l1\nl2x\nl3\nl4-edited\n"})
	} else {
		step("later", map[string]string{"d.txt": "d\n"})
	}
	return root, h
}

// pick returns the indices of the selectable lines of one file's diff in a
// commit whose text is one of wants.
func pick(t *testing.T, r *gitworkbench.Runner, root, commit, path string, wants ...string) []int {
	t.Helper()
	lines, err := r.PatchSource(context.Background(), root, commit, path, "")
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, l := range lines {
		if l.Selectable && slices.Contains(wants, l.Text) {
			out = append(out, l.Index)
		}
	}
	if len(out) != len(wants) {
		t.Fatalf("wanted %v in %s, found indices %v", wants, path, out)
	}
	return out
}

func show(t *testing.T, root, rev, file string) string {
	t.Helper()
	return git(t, root, "show", rev+":"+file)
}

func TestPatchSourceMarksOnlyChangedLines(t *testing.T) {
	root, h := patchRepo(t, false)
	r := &gitworkbench.Runner{Backend: New()}
	lines, err := r.PatchSource(context.Background(), root, h["X"], "a.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	var selectable []string
	for _, l := range lines {
		if l.Selectable {
			selectable = append(selectable, l.Text)
		}
	}
	if got := strings.Join(selectable, "|"); got != "-l2|+l2x|+l4" {
		t.Fatalf("selectable lines: %q", got)
	}
}

func TestRewriteWithPatchRemove(t *testing.T) {
	root, h := patchRepo(t, false)
	r := &gitworkbench.Runner{Backend: New()}
	err := r.RewriteWithPatch(context.Background(), root, gitworkbench.PatchOp{
		Action: gitworkbench.PatchRemove, Commit: h["X"],
		Files: []gitworkbench.PatchFile{{Path: "a.txt", Lines: pick(t, r, root, h["X"], "a.txt", "+l4")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := show(t, root, "HEAD", "a.txt"); got != "l1\nl2x\nl3" {
		t.Fatalf("a.txt at HEAD: %q", got)
	}
	if got := show(t, root, "HEAD", "b.txt"); got != "b1\nb2" {
		t.Fatalf("the other file must keep its change: %q", got)
	}
	if got := git(t, root, "log", "--format=%s", "--reverse"); got != "base\nX\nlater" {
		t.Fatalf("history: %q", got)
	}
	if got := git(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("not clean: %q", got)
	}
}

func TestRewriteWithPatchSplit(t *testing.T) {
	root, h := patchRepo(t, false)
	headTree := git(t, root, "rev-parse", "HEAD^{tree}")
	r := &gitworkbench.Runner{Backend: New()}
	err := r.RewriteWithPatch(context.Background(), root, gitworkbench.PatchOp{
		Action: gitworkbench.PatchSplit, Commit: h["X"], Message: "split out b\n\nbecause it stands alone",
		Files: []gitworkbench.PatchFile{{Path: "b.txt", Whole: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, root, "log", "--format=%s", "--reverse"); got != "base\nX\nsplit out b\nlater" {
		t.Fatalf("history: %q", got)
	}
	if got := git(t, root, "rev-parse", "HEAD^{tree}"); got != headTree {
		t.Fatal("a split must not change the final tree")
	}
	if got := show(t, root, "HEAD~2", "b.txt"); got != "b1" {
		t.Fatalf("X keeps no b change: %q", got)
	}
	if got := git(t, root, "log", "-1", "--format=%b", "HEAD~1"); got != "because it stands alone" {
		t.Fatalf("body: %q", got)
	}
}

func TestRewriteWithPatchMove(t *testing.T) {
	for name, dest := range map[string]string{"to a later commit": "later", "to an earlier commit": "base"} {
		t.Run(name, func(t *testing.T) {
			root, h := patchRepo(t, false)
			headTree := git(t, root, "rev-parse", "HEAD^{tree}")
			r := &gitworkbench.Runner{Backend: New()}
			err := r.RewriteWithPatch(context.Background(), root, gitworkbench.PatchOp{
				Action: gitworkbench.PatchMove, Commit: h["X"], Dest: h[dest],
				Files: []gitworkbench.PatchFile{{Path: "b.txt", Whole: true}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := git(t, root, "rev-parse", "HEAD^{tree}"); got != headTree {
				t.Fatal("a move must not change the final tree")
			}
			if got := git(t, root, "log", "--format=%s", "--reverse"); got != "base\nX\nlater" {
				t.Fatalf("history: %q", got)
			}
			if got := show(t, root, "HEAD~1", "b.txt"); (dest == "later") != (got == "b1") {
				t.Fatalf("X's b.txt is %q after moving to %s", got, dest)
			}
			if dest == "base" {
				if got := show(t, root, "HEAD~2", "b.txt"); got != "b1\nb2" {
					t.Fatalf("base should hold the change: %q", got)
				}
			}
		})
	}
}

func TestRewriteWithPatchUndo(t *testing.T) {
	root, h := patchRepo(t, false)
	r := &gitworkbench.Runner{Backend: New()}
	err := r.RewriteWithPatch(context.Background(), root, gitworkbench.PatchOp{
		Action: gitworkbench.PatchUndo, Commit: h["X"],
		Files: []gitworkbench.PatchFile{{Path: "a.txt", Lines: pick(t, r, root, h["X"], "a.txt", "+l4")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := show(t, root, "HEAD", "a.txt"); got != "l1\nl2x\nl3" {
		t.Fatalf("a.txt at HEAD: %q", got)
	}
	if got := git(t, root, "diff", "--cached", "--", "a.txt"); !strings.Contains(got, "+l4") {
		t.Fatalf("the changes should wait staged: %q", got)
	}
}

func TestRewriteWithPatchConflictAborts(t *testing.T) {
	root, h := patchRepo(t, true)
	r := &gitworkbench.Runner{Backend: New()}
	sel := pick(t, r, root, h["X"], "a.txt", "+l4")
	before := git(t, root, "rev-parse", "HEAD") + "\n" + git(t, root, "status", "--porcelain")
	err := r.RewriteWithPatch(context.Background(), root, gitworkbench.PatchOp{
		Action: gitworkbench.PatchRemove, Commit: h["X"], Files: []gitworkbench.PatchFile{{Path: "a.txt", Lines: sel}},
	})
	var ge *gitworkbench.Error
	if !errors.As(err, &ge) || ge.Class != gitworkbench.ErrConflict {
		t.Fatalf("want a conflict, got %v", err)
	}
	if rebaseLeftOver(t, root) {
		t.Fatal("rebase left behind")
	}
	if after := git(t, root, "rev-parse", "HEAD") + "\n" + git(t, root, "status", "--porcelain"); after != before {
		t.Fatalf("repository changed: %q -> %q", before, after)
	}
}

func TestRewriteWithPatchLeavesNoTraces(t *testing.T) {
	root, h := patchRepo(t, false)
	e := New()
	r := &gitworkbench.Runner{Backend: e}
	cwd, _ := os.Getwd()
	if err := r.RewriteWithPatch(context.Background(), root, gitworkbench.PatchOp{
		Action: gitworkbench.PatchRemove, Commit: h["X"], Files: []gitworkbench.PatchFile{{Path: "b.txt", Whole: true}},
	}); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	temp := e.sessions[root].tempDir
	e.mu.Unlock()
	if entries, _ := os.ReadDir(temp); len(entries) != 0 {
		t.Fatalf("patch files stayed in %s: %v", temp, entries)
	}
	if _, err := os.Stat(filepath.Join(cwd, filepath.Base(root))); err == nil {
		t.Fatal("a patch directory appeared in the working directory")
	}
	e.Close()
	if _, err := os.Stat(temp); err == nil {
		t.Fatal("Close must remove the temporary directory")
	}
}

func TestRewriteWithPatchRefusals(t *testing.T) {
	root, h := patchRepo(t, false)
	r := &gitworkbench.Runner{Backend: New()}
	ctx := context.Background()
	whole := []gitworkbench.PatchFile{{Path: "b.txt", Whole: true}}
	cases := map[string]gitworkbench.PatchOp{
		"no files":              {Action: gitworkbench.PatchRemove, Commit: h["X"]},
		"whole and lines":       {Action: gitworkbench.PatchRemove, Commit: h["X"], Files: []gitworkbench.PatchFile{{Path: "b.txt", Whole: true, Lines: []int{3}}}},
		"neither":               {Action: gitworkbench.PatchRemove, Commit: h["X"], Files: []gitworkbench.PatchFile{{Path: "b.txt"}}},
		"escaping path":         {Action: gitworkbench.PatchRemove, Commit: h["X"], Files: []gitworkbench.PatchFile{{Path: "../x", Whole: true}}},
		"split no message":      {Action: gitworkbench.PatchSplit, Commit: h["X"], Files: whole},
		"move onto itself":      {Action: gitworkbench.PatchMove, Commit: h["X"], Dest: h["X"], Files: whole},
		"unknown commit":        {Action: gitworkbench.PatchRemove, Commit: strings.Repeat("a", 40), Files: whole},
		"a line with no change": {Action: gitworkbench.PatchRemove, Commit: h["X"], Files: []gitworkbench.PatchFile{{Path: "b.txt", Lines: []int{0}}}},
	}
	before := snapshot(t, root)
	for name, op := range cases {
		t.Run(name, func(t *testing.T) {
			err := r.RewriteWithPatch(ctx, root, op)
			var ge *gitworkbench.Error
			if !errors.As(err, &ge) || ge.Class != gitworkbench.ErrRefusal {
				t.Fatalf("want a refusal, got %v", err)
			}
			if got := snapshot(t, root); got != before {
				t.Fatal("a refused operation changed the repository")
			}
		})
	}
	t.Run("dirty work tree", func(t *testing.T) {
		writeFile(t, root, "dirty", "x")
		defer os.Remove(filepath.Join(root, "dirty"))
		err := r.RewriteWithPatch(ctx, root, gitworkbench.PatchOp{Action: gitworkbench.PatchRemove, Commit: h["X"], Files: whole})
		var ge *gitworkbench.Error
		if !errors.As(err, &ge) || ge.Class != gitworkbench.ErrRefusal {
			t.Fatalf("want a refusal, got %v", err)
		}
	})
	t.Run("no backend", func(t *testing.T) {
		err := (&gitworkbench.Runner{}).RewriteWithPatch(ctx, root, gitworkbench.PatchOp{Action: gitworkbench.PatchRemove, Commit: h["X"], Files: whole})
		var ge *gitworkbench.Error
		if !errors.As(err, &ge) || ge.Class != gitworkbench.ErrRefusal {
			t.Fatalf("want a refusal, got %v", err)
		}
	})
}
