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

// historyRepo has six commits c1..c6 (oldest first), each adding its own file,
// so any reordering or removal replays without a conflict.
func historyRepo(t *testing.T) (root string, hashes []string) {
	t.Helper()
	root = newRepo(t)
	for _, n := range []string{"c1", "c2", "c3", "c4", "c5", "c6"} {
		commit(t, root, n)
		hashes = append(hashes, git(t, root, "rev-parse", "HEAD"))
	}
	return root, hashes
}

// snapshot is what a rewrite may change: the messages and tree of every commit
// from the newest down, and the state of the work tree.
func snapshot(t *testing.T, root string) string {
	t.Helper()
	log := git(t, root, "log", "--format=%B|%T", "--reverse")
	return log + "\n--\n" + git(t, root, "status", "--porcelain")
}

func rebaseLeftOver(t *testing.T, root string) bool {
	t.Helper()
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(root, ".git", d)); err == nil {
			return true
		}
	}
	return false
}

// A rewrite through the engine produces the same history (messages and trees)
// as the same plan on the argv path.
func TestRewriteHistoryParity(t *testing.T) {
	type step = gitworkbench.RebaseStep
	pick := func(h string) step { return step{Hash: h, Action: gitworkbench.RebasePick} }
	cases := []struct {
		name string
		op   func(h []string) gitworkbench.HistoryOp
		// plan is the same rewrite as a full todo on the argv path, from c1.
		plan func(h []string) []step
	}{
		{"reword the middle", func(h []string) gitworkbench.HistoryOp {
			return gitworkbench.HistoryOp{Action: gitworkbench.HistoryReword, Hashes: []string{h[2]}, Message: "third, better\n\nwith a body"}
		}, func(h []string) []step {
			return []step{pick(h[1]), {Hash: h[2], Action: gitworkbench.RebaseReword, Message: "third, better\n\nwith a body"}, pick(h[3]), pick(h[4]), pick(h[5])}
		}},
		{"reword the head", func(h []string) gitworkbench.HistoryOp {
			return gitworkbench.HistoryOp{Action: gitworkbench.HistoryReword, Hashes: []string{h[5]}, Message: "last one"}
		}, func(h []string) []step {
			return []step{pick(h[1]), pick(h[2]), pick(h[3]), pick(h[4]), {Hash: h[5], Action: gitworkbench.RebaseReword, Message: "last one"}}
		}},
		{"squash two into the one below", func(h []string) gitworkbench.HistoryOp {
			return gitworkbench.HistoryOp{Action: gitworkbench.HistorySquash, Hashes: []string{h[4], h[3]}}
		}, func(h []string) []step {
			return []step{pick(h[1]), pick(h[2]), {Hash: h[3], Action: gitworkbench.RebaseSquash}, {Hash: h[4], Action: gitworkbench.RebaseSquash}, pick(h[5])}
		}},
		{"fixup the head", func(h []string) gitworkbench.HistoryOp {
			return gitworkbench.HistoryOp{Action: gitworkbench.HistoryFixup, Hashes: []string{h[5]}}
		}, func(h []string) []step {
			return []step{pick(h[1]), pick(h[2]), pick(h[3]), pick(h[4]), {Hash: h[5], Action: gitworkbench.RebaseFixup}}
		}},
		{"drop two", func(h []string) gitworkbench.HistoryOp {
			return gitworkbench.HistoryOp{Action: gitworkbench.HistoryDrop, Hashes: []string{h[2], h[3]}}
		}, func(h []string) []step {
			return []step{pick(h[1]), {Hash: h[2], Action: gitworkbench.RebaseDrop}, {Hash: h[3], Action: gitworkbench.RebaseDrop}, pick(h[4]), pick(h[5])}
		}},
		{"drop the oldest (rebase from the root)", func(h []string) gitworkbench.HistoryOp {
			return gitworkbench.HistoryOp{Action: gitworkbench.HistoryDrop, Hashes: []string{h[0]}}
		}, nil},
		{"move one toward older", func(h []string) gitworkbench.HistoryOp {
			return gitworkbench.HistoryOp{Action: gitworkbench.HistoryMove, Hashes: []string{h[4]}, Offset: 2}
		}, func(h []string) []step {
			return []step{pick(h[1]), pick(h[4]), pick(h[2]), pick(h[3]), pick(h[5])}
		}},
		{"move a run toward newer", func(h []string) gitworkbench.HistoryOp {
			return gitworkbench.HistoryOp{Action: gitworkbench.HistoryMove, Hashes: []string{h[2], h[3]}, Offset: -1}
		}, func(h []string) []step {
			return []step{pick(h[1]), pick(h[4]), pick(h[2]), pick(h[3]), pick(h[5])}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lazyRoot, h := historyRepo(t)
			lazy := &gitworkbench.Runner{Backend: New()}
			if err := lazy.RewriteHistory(context.Background(), lazyRoot, tc.op(h)); err != nil {
				t.Fatalf("RewriteHistory: %v", err)
			}
			if rebaseLeftOver(t, lazyRoot) {
				t.Fatal("a rebase was left behind")
			}
			if tc.plan == nil {
				// the root case has no argv equivalent through a base commit:
				// the oldest commit's file must simply be gone
				if _, err := os.Stat(filepath.Join(lazyRoot, "c1")); err == nil {
					t.Fatal("c1 should have been dropped")
				}
				if got := git(t, lazyRoot, "log", "--format=%s", "--reverse"); got != "c2\nc3\nc4\nc5\nc6" {
					t.Fatalf("history after drop: %q", got)
				}
				return
			}
			argvRoot, ah := historyRepo(t)
			argv := &gitworkbench.Runner{}
			if err := argv.InteractiveRebase(context.Background(), argvRoot, ah[0], tc.plan(ah)); err != nil {
				t.Fatalf("argv InteractiveRebase: %v", err)
			}
			if got, want := snapshot(t, lazyRoot), snapshot(t, argvRoot); got != want {
				t.Fatalf("engine and argv disagree.\nengine:\n%s\nargv:\n%s", got, want)
			}
		})
	}
}

// A conflict stops the rewrite; the Runner aborts and the repository is
// exactly as it was.
func TestRewriteHistoryConflictAborts(t *testing.T) {
	root := newRepo(t)
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(root, "shared"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("one\n")
	git(t, root, "add", "shared")
	git(t, root, "commit", "-q", "-m", "base")
	write("two\n")
	git(t, root, "commit", "-q", "-am", "two")
	two := git(t, root, "rev-parse", "HEAD")
	write("three\n")
	git(t, root, "commit", "-q", "-am", "three")
	before, status := git(t, root, "rev-parse", "HEAD"), snapshot(t, root)

	lazy := &gitworkbench.Runner{Backend: New()}
	err := lazy.RewriteHistory(context.Background(), root, gitworkbench.HistoryOp{Action: gitworkbench.HistoryDrop, Hashes: []string{two}})
	var ge *gitworkbench.Error
	if !errors.As(err, &ge) || ge.Class != gitworkbench.ErrConflict {
		t.Fatalf("want a conflict error, got %v", err)
	}
	if len(ge.Paths) != 1 || ge.Paths[0] != "shared" {
		t.Fatalf("the conflicting files should be named: %v", ge.Paths)
	}
	if rebaseLeftOver(t, root) {
		t.Fatal("the rebase was not aborted")
	}
	if got := git(t, root, "rev-parse", "HEAD"); got != before {
		t.Fatalf("HEAD moved: %s -> %s", before, got)
	}
	if got := snapshot(t, root); got != status {
		t.Fatalf("repository changed:\n%s\nvs\n%s", status, got)
	}
}

// The user's own editor must never open during a rewrite, and the rewrite
// must still work: squash would otherwise start it for the joined message.
func TestRewriteHistoryIgnoresUserEditor(t *testing.T) {
	for _, k := range []string{"GIT_EDITOR", "GIT_SEQUENCE_EDITOR", "EDITOR", "VISUAL"} {
		t.Setenv(k, "false")
	}
	root, h := historyRepo(t)
	lazy := &gitworkbench.Runner{Backend: New()}
	if err := lazy.RewriteHistory(context.Background(), root, gitworkbench.HistoryOp{Action: gitworkbench.HistorySquash, Hashes: []string{h[5]}}); err != nil {
		t.Fatalf("a hostile editor setting broke the rewrite: %v", err)
	}
	if got := git(t, root, "log", "--format=%s", "--reverse"); got != "c1\nc2\nc3\nc4\nc5" {
		t.Fatalf("history: %q", got)
	}
}

// The operations that cannot be done are refused before anything changes.
func TestRewriteHistoryRefusals(t *testing.T) {
	root, h := historyRepo(t)
	lazy := &gitworkbench.Runner{Backend: New()}
	before := snapshot(t, root)
	ctx := context.Background()

	cases := map[string]gitworkbench.HistoryOp{
		"not next to each other":   {Action: gitworkbench.HistoryDrop, Hashes: []string{h[1], h[3]}},
		"unknown commit":           {Action: gitworkbench.HistoryDrop, Hashes: []string{strings.Repeat("a", 40)}},
		"nothing below to squash":  {Action: gitworkbench.HistorySquash, Hashes: []string{h[0]}},
		"move off the branch":      {Action: gitworkbench.HistoryMove, Hashes: []string{h[5]}, Offset: -1},
		"reword without a message": {Action: gitworkbench.HistoryReword, Hashes: []string{h[2]}, Message: " "},
		"drop everything":          {Action: gitworkbench.HistoryDrop, Hashes: h},
	}
	for name, op := range cases {
		t.Run(name, func(t *testing.T) {
			err := lazy.RewriteHistory(ctx, root, op)
			var ge *gitworkbench.Error
			if !errors.As(err, &ge) || ge.Class != gitworkbench.ErrRefusal {
				t.Fatalf("want a refusal, got %v", err)
			}
			if got := snapshot(t, root); got != before {
				t.Fatal("a refused rewrite changed the repository")
			}
		})
	}

	t.Run("dirty work tree", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(root, "dirty"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(filepath.Join(root, "dirty"))
		err := lazy.RewriteHistory(ctx, root, gitworkbench.HistoryOp{Action: gitworkbench.HistoryDrop, Hashes: []string{h[5]}})
		var ge *gitworkbench.Error
		if !errors.As(err, &ge) || ge.Class != gitworkbench.ErrRefusal {
			t.Fatalf("want a refusal, got %v", err)
		}
	})
	t.Run("no backend", func(t *testing.T) {
		err := (&gitworkbench.Runner{}).RewriteHistory(ctx, root, gitworkbench.HistoryOp{Action: gitworkbench.HistoryDrop, Hashes: []string{h[5]}})
		var ge *gitworkbench.Error
		if !errors.As(err, &ge) || ge.Class != gitworkbench.ErrRefusal {
			t.Fatalf("want a refusal, got %v", err)
		}
	})
	t.Run("a merge in the way", func(t *testing.T) {
		mroot := newRepo(t)
		commit(t, mroot, "base")
		git(t, mroot, "checkout", "-q", "-b", "side")
		commit(t, mroot, "side")
		git(t, mroot, "checkout", "-q", "main")
		commit(t, mroot, "main1")
		git(t, mroot, "merge", "-q", "--no-ff", "-m", "merge side", "side")
		commit(t, mroot, "after")
		base := git(t, mroot, "rev-parse", "HEAD~3")
		err := lazy.RewriteHistory(ctx, mroot, gitworkbench.HistoryOp{Action: gitworkbench.HistoryDrop, Hashes: []string{base}})
		var ge *gitworkbench.Error
		if !errors.As(err, &ge) || ge.Class != gitworkbench.ErrRefusal {
			t.Fatalf("want a refusal, got %v", err)
		}
	})
}
