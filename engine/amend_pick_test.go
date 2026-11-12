package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thewei/solco-gitkit/gitworkbench"
)

func wantClass(t *testing.T, err error, class gitworkbench.ErrClass) {
	t.Helper()
	var ge *gitworkbench.Error
	if !errors.As(err, &ge) || ge.Class != class {
		t.Fatalf("want class %v, got %v", class, err)
	}
}

func TestAmendTo(t *testing.T) {
	for name, target := range map[string]string{"an older commit": "X", "the head": "later", "the oldest": "base"} {
		t.Run(name, func(t *testing.T) {
			root, h := patchRepo(t, false)
			writeFile(t, root, "e.txt", "extra\n") // a new file: replays cleanly everywhere
			git(t, root, "add", "e.txt")
			want := git(t, root, "write-tree")
			r := &gitworkbench.Runner{Backend: New()}
			if err := r.AmendTo(context.Background(), root, h[target]); err != nil {
				t.Fatal(err)
			}
			if got := git(t, root, "log", "--format=%s", "--reverse"); got != "base\nX\nlater" {
				t.Fatalf("history: %q", got)
			}
			if got := git(t, root, "rev-parse", "HEAD^{tree}"); got != want {
				t.Fatal("the final tree must be the old tree plus the staged change")
			}
			if got := git(t, root, "status", "--porcelain"); got != "" {
				t.Fatalf("not clean: %q", got)
			}
			idx := map[string]string{"base": "HEAD~2", "X": "HEAD~1", "later": "HEAD"}[target]
			if got := git(t, root, "show", idx+":e.txt"); got != "extra" {
				t.Fatalf("%s should hold e.txt, got %q", target, got)
			}
			if target != "base" {
				if got := git(t, root, "ls-tree", "-r", "--name-only", "HEAD~2"); strings.Contains(got, "e.txt") {
					t.Fatal("base must not hold e.txt")
				}
			}
		})
	}
}

func TestAmendToConflictRestoresTheStagedChanges(t *testing.T) {
	root, h := patchRepo(t, false)
	writeFile(t, root, "a.txt", "l1\nl2y\nl3\nl4\n") // edits the line X changed
	git(t, root, "add", "a.txt")
	head := git(t, root, "rev-parse", "HEAD")
	staged := git(t, root, "diff", "--cached")
	r := &gitworkbench.Runner{Backend: New()}
	err := r.AmendTo(context.Background(), root, h["base"])
	wantClass(t, err, gitworkbench.ErrConflict)
	if rebaseLeftOver(t, root) {
		t.Fatal("rebase left behind")
	}
	if got := git(t, root, "rev-parse", "HEAD"); got != head {
		t.Fatal("no fixup commit may stay on the branch")
	}
	if got := git(t, root, "diff", "--cached"); got != staged {
		t.Fatalf("the staged changes must wait as before:\n%s", got)
	}
}

func TestAmendToRefusals(t *testing.T) {
	root, h := patchRepo(t, false)
	r := &gitworkbench.Runner{Backend: New()}
	ctx := context.Background()
	t.Run("nothing staged", func(t *testing.T) {
		wantClass(t, r.AmendTo(ctx, root, h["X"]), gitworkbench.ErrRefusal)
	})
	writeFile(t, root, "e.txt", "extra\n")
	git(t, root, "add", "e.txt")
	before := snapshot(t, root)
	t.Run("unknown commit", func(t *testing.T) {
		wantClass(t, r.AmendTo(ctx, root, strings.Repeat("a", 40)), gitworkbench.ErrRefusal)
	})
	t.Run("short hash", func(t *testing.T) {
		wantClass(t, r.AmendTo(ctx, root, h["X"][:8]), gitworkbench.ErrRefusal)
	})
	t.Run("no backend", func(t *testing.T) {
		wantClass(t, (&gitworkbench.Runner{}).AmendTo(ctx, root, h["X"]), gitworkbench.ErrRefusal)
	})
	t.Run("unstaged changes", func(t *testing.T) {
		writeFile(t, root, "b.txt", "b1\nb2\nmore\n")
		defer git(t, root, "checkout", "--", "b.txt")
		wantClass(t, r.AmendTo(ctx, root, h["X"]), gitworkbench.ErrRefusal)
	})
	t.Run("detached head", func(t *testing.T) {
		git(t, root, "checkout", "-q", "--detach")
		defer git(t, root, "checkout", "-q", "main")
		wantClass(t, r.AmendTo(ctx, root, h["X"]), gitworkbench.ErrRefusal)
	})
	if got := snapshot(t, root); got != before {
		t.Fatal("a refused amend changed the repository")
	}
}

// pickRepo: main has base and m1; side branches off base with s1..s3 (each
// its own file). Returns the side hashes oldest first; HEAD is main.
func pickRepo(t *testing.T, conflicting bool) (root string, side []string) {
	t.Helper()
	root = newRepo(t)
	commit(t, root, "base")
	git(t, root, "checkout", "-q", "-b", "side")
	for _, n := range []string{"s1", "s2", "s3"} {
		if conflicting && n == "s2" {
			writeFile(t, root, "m1", "from side")
			git(t, root, "add", "m1")
			git(t, root, "commit", "-q", "-m", n)
		} else {
			commit(t, root, n)
		}
		side = append(side, git(t, root, "rev-parse", "HEAD"))
	}
	git(t, root, "checkout", "-q", "main")
	commit(t, root, "m1")
	return root, side
}

func TestCherryPickCommitsParity(t *testing.T) {
	state := func(root string) string {
		return git(t, root, "log", "--format=%s|%T", "--reverse") + "\n" + git(t, root, "status", "--porcelain")
	}
	// the same hashes in both repositories: dates are fixed by the helper's
	// environment only per run, so compare subjects and trees, not hashes.
	rootA, sideA := pickRepo(t, false)
	rootB, sideB := pickRepo(t, false)
	cli, lazy := both()
	ctx := context.Background()
	if err := cli.CherryPickCommits(ctx, rootA, sideA); err != nil {
		t.Fatal(err)
	}
	if err := lazy.CherryPickCommits(ctx, rootB, sideB); err != nil {
		t.Fatal(err)
	}
	if a, b := state(rootA), state(rootB); a != b {
		t.Fatalf("paths differ:\nargv:\n%s\nengine:\n%s", a, b)
	}
	if got := git(t, rootB, "log", "--format=%s", "--reverse"); got != "base\nm1\ns1\ns2\ns3" {
		t.Fatalf("order: %q", got)
	}
}

func TestCherryPickCommitsConflictStaysInProgress(t *testing.T) {
	for name, r := range map[string]*gitworkbench.Runner{"argv": {}, "engine": {Backend: New()}} {
		t.Run(name, func(t *testing.T) {
			root, side := pickRepo(t, true)
			err := r.CherryPickCommits(context.Background(), root, side)
			wantClass(t, err, gitworkbench.ErrConflict)
			state, serr := r.SequencerState(context.Background(), root)
			if serr != nil || !state.Active() || state.Unmerged == 0 {
				t.Fatalf("the cherry-pick should wait for the sequencer: %+v %v", state, serr)
			}
			if got := git(t, root, "log", "--format=%s", "--reverse"); got != "base\nm1\ns1" {
				t.Fatalf("s1 applied before the stop: %q", got)
			}
			if err := r.FinishSequencer(context.Background(), root, gitworkbench.SequencerCherryPick, true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCherryPickCommitsRefusals(t *testing.T) {
	root, side := pickRepo(t, false)
	git(t, root, "merge", "-q", "--no-ff", "-m", "merge", "side")
	merge := git(t, root, "rev-parse", "HEAD")
	git(t, root, "reset", "-q", "--hard", "HEAD~1")
	before := snapshot(t, root)
	ctx := context.Background()
	for name, r := range map[string]*gitworkbench.Runner{"argv": {}, "engine": {Backend: New()}} {
		t.Run(name, func(t *testing.T) {
			cases := map[string][]string{
				"none":      nil,
				"duplicate": {side[0], side[0]},
				"short":     {side[0][:8]},
				"merge":     {side[0], merge},
				"unknown":   {strings.Repeat("a", 40)},
			}
			for c, hashes := range cases {
				err := r.CherryPickCommits(ctx, root, hashes)
				if c == "unknown" {
					wantClass(t, err, gitworkbench.ErrStaleState)
				} else {
					wantClass(t, err, gitworkbench.ErrRefusal)
				}
			}
			writeFile(t, root, "dirty", "x")
			defer os.Remove(filepath.Join(root, "dirty"))
			wantClass(t, r.CherryPickCommits(ctx, root, side), gitworkbench.ErrRefusal)
			os.Remove(filepath.Join(root, "dirty"))
			if got := snapshot(t, root); got != before {
				t.Fatal("a refused cherry-pick changed the repository")
			}
		})
	}
}
