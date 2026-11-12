package engine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/thewei/solco-gitkit/gitworkbench"
)

// writeRepo: main with c1, c2, c3 (each adds its own file, a.txt carries
// lines), a branch "feature" at c3, "other" at c2 holding another a.txt, and
// one stash entry. HEAD is main.
func writeRepo(t *testing.T) (root string, h []string) {
	t.Helper()
	root = newRepo(t)
	for _, n := range []string{"c1", "c2", "c3"} {
		commit(t, root, n)
		h = append(h, git(t, root, "rev-parse", "HEAD"))
	}
	writeFile(t, root, "a.txt", "main\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-q", "-m", "a on main")
	h = append(h, git(t, root, "rev-parse", "HEAD"))
	git(t, root, "branch", "feature")
	git(t, root, "checkout", "-q", "-b", "other", h[1])
	writeFile(t, root, "a.txt", "other\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-q", "-m", "a on other")
	git(t, root, "checkout", "-q", "main")
	writeFile(t, root, "c1", "stashed\n")
	git(t, root, "stash", "push", "-q", "-m", "first")
	return root, h
}

// fingerprint is the state a write may change, independent of commit dates:
// refs with their trees, HEAD, the work tree, and the stash entries.
func fingerprint(t *testing.T, root string) string {
	t.Helper()
	return hashInMessage.ReplaceAllString(git(t, root, "for-each-ref", "--format=%(refname) %(tree)")+"\n--\n"+
		git(t, root, "symbolic-ref", "-q", "--short", "HEAD")+"\n--\n"+
		git(t, root, "status", "--porcelain")+"\n--\n"+
		git(t, root, "stash", "list", "--format=%gs|%T"), "<h>")
}

// a stash message names the short hash of its base commit, which carries the
// run's dates.
var hashInMessage = regexp.MustCompile(`\b[0-9a-f]{7}\b`)

func classOf(err error) string {
	var ge *gitworkbench.Error
	if errors.As(err, &ge) {
		return fmt.Sprint(ge.Class) // Op names the git subcommand: switch on one path, checkout on the other
	}
	if err != nil {
		return "plain:" + err.Error()
	}
	return "ok"
}

// parity runs op once on the argv path and once through the engine, on two
// identical repositories, and requires the same outcome and the same state.
func parity(t *testing.T, setup func(t *testing.T, root string, h []string), op func(r *gitworkbench.Runner, root string, h []string) error) {
	t.Helper()
	run := func(r *gitworkbench.Runner) (string, string) {
		root, h := writeRepo(t)
		if setup != nil {
			setup(t, root, h)
		}
		err := op(r, root, h)
		return classOf(err), fingerprint(t, root)
	}
	cli, lazy := both()
	ca, fa := run(cli)
	cb, fb := run(lazy)
	if ca != cb {
		t.Fatalf("outcome differs: argv %s, engine %s", ca, cb)
	}
	if fa != fb {
		t.Fatalf("state differs:\nargv:\n%s\nengine:\n%s", fa, fb)
	}
}

func TestWriteBranchParity(t *testing.T) {
	ctx := context.Background()
	dirty := func(t *testing.T, root string, h []string) { writeFile(t, root, "c1", "dirty\n") }
	cases := []struct {
		name  string
		setup func(t *testing.T, root string, h []string)
		op    func(r *gitworkbench.Runner, root string, h []string) error
	}{
		{"switch", nil, func(r *gitworkbench.Runner, root string, h []string) error {
			_, err := r.SwitchBranch(ctx, root, "feature")
			return err
		}},
		{"switch carrying a change", dirty, func(r *gitworkbench.Runner, root string, h []string) error {
			_, err := r.SwitchBranch(ctx, root, "other")
			return err
		}},
		{"switch refused by a conflicting change", func(t *testing.T, root string, h []string) {
			writeFile(t, root, "a.txt", "mine\n")
		}, func(r *gitworkbench.Runner, root string, h []string) error {
			_, err := r.SwitchBranch(ctx, root, "other")
			return err
		}},
		{"switch to an unknown branch", nil, func(r *gitworkbench.Runner, root string, h []string) error {
			_, err := r.SwitchBranch(ctx, root, "nope")
			return err
		}},
		{"create", dirty, func(r *gitworkbench.Runner, root string, h []string) error {
			_, err := r.CreateBranch(ctx, root, "topic")
			return err
		}},
		{"create that exists", nil, func(r *gitworkbench.Runner, root string, h []string) error {
			_, err := r.CreateBranch(ctx, root, "feature")
			return err
		}},
		{"create at a commit", nil, func(r *gitworkbench.Runner, root string, h []string) error {
			_, err := r.CreateBranchAt(ctx, root, "old", h[0])
			return err
		}},
		{"rename", nil, func(r *gitworkbench.Runner, root string, h []string) error {
			return r.RenameBranch(ctx, root, "feature", "feature2")
		}},
		{"rename onto an existing one", nil, func(r *gitworkbench.Runner, root string, h []string) error {
			return r.RenameBranch(ctx, root, "feature", "other")
		}},
		{"rename the current branch", nil, func(r *gitworkbench.Runner, root string, h []string) error {
			return r.RenameBranch(ctx, root, "main", "trunk")
		}},
		{"delete merged", nil, func(r *gitworkbench.Runner, root string, h []string) error {
			return r.DeleteBranch(ctx, root, "feature")
		}},
		{"delete unmerged", nil, func(r *gitworkbench.Runner, root string, h []string) error {
			return r.DeleteBranch(ctx, root, "other")
		}},
		{"delete the current branch", nil, func(r *gitworkbench.Runner, root string, h []string) error {
			return r.DeleteBranch(ctx, root, "main")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { parity(t, c.setup, c.op) })
	}
}

func TestWriteStashParity(t *testing.T) {
	ctx := context.Background()
	two := func(t *testing.T, root string, h []string) {
		writeFile(t, root, "c2", "second stash\n")
		git(t, root, "stash", "push", "-q", "-m", "second")
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, root string, h []string)
		op    func(r *gitworkbench.Runner, root string, h []string) error
	}{
		{"push with a message and an untracked file", func(t *testing.T, root string, h []string) {
			writeFile(t, root, "c2", "changed\n")
			writeFile(t, root, "new.txt", "untracked\n")
		}, func(r *gitworkbench.Runner, root string, h []string) error {
			return r.StashPush(ctx, root, "work in progress")
		}},
		{"push without a message", func(t *testing.T, root string, h []string) { writeFile(t, root, "c2", "changed\n") },
			func(r *gitworkbench.Runner, root string, h []string) error { return r.StashPush(ctx, root, "") }},
		{"push with nothing to stash", nil, func(r *gitworkbench.Runner, root string, h []string) error { return r.StashPush(ctx, root, "empty") }},
		{"pop the newest", two, func(r *gitworkbench.Runner, root string, h []string) error { return r.StashPop(ctx, root, "stash@{0}") }},
		{"pop an older one", two, func(r *gitworkbench.Runner, root string, h []string) error { return r.StashPop(ctx, root, "stash@{1}") }},
		{"apply keeps it", two, func(r *gitworkbench.Runner, root string, h []string) error {
			return r.StashApply(ctx, root, "stash@{1}")
		}},
		{"drop an older one", two, func(r *gitworkbench.Runner, root string, h []string) error {
			return r.StashDrop(ctx, root, "stash@{1}")
		}},
		{"pop onto a conflicting change", func(t *testing.T, root string, h []string) { writeFile(t, root, "c1", "in the way\n") },
			func(r *gitworkbench.Runner, root string, h []string) error { return r.StashPop(ctx, root, "stash@{0}") }},
		{"pop an entry that is not there", nil, func(r *gitworkbench.Runner, root string, h []string) error { return r.StashPop(ctx, root, "stash@{5}") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { parity(t, c.setup, c.op) })
	}
}

func TestWriteTagParity(t *testing.T) {
	ctx := context.Background()
	tagged := func(t *testing.T, root string, h []string) { git(t, root, "tag", "v1", h[1]) }
	cases := []struct {
		name  string
		setup func(t *testing.T, root string, h []string)
		op    func(r *gitworkbench.Runner, root string, h []string) error
	}{
		{"create at HEAD", nil, func(r *gitworkbench.Runner, root string, h []string) error { return r.CreateTag(ctx, root, "v1") }},
		{"create at a commit", nil, func(r *gitworkbench.Runner, root string, h []string) error {
			return r.CreateTagAt(ctx, root, "v0", h[0])
		}},
		{"create that exists", tagged, func(r *gitworkbench.Runner, root string, h []string) error { return r.CreateTag(ctx, root, "v1") }},
		{"delete", tagged, func(r *gitworkbench.Runner, root string, h []string) error { return r.DeleteTag(ctx, root, "v1") }},
		{"delete one that is not there", nil, func(r *gitworkbench.Runner, root string, h []string) error { return r.DeleteTag(ctx, root, "nope") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { parity(t, c.setup, c.op) })
	}
}

// A repository with no commit yet still gets its first branch (the engine's
// `checkout -b` has nothing to start from, so the argv path serves it).
func TestWriteCreateBranchOnAnUnbornHead(t *testing.T) {
	root := newRepo(t)
	_, lazy := both()
	if _, err := lazy.CreateBranch(context.Background(), root, "first"); err != nil {
		t.Fatal(err)
	}
	if got := git(t, root, "symbolic-ref", "--short", "HEAD"); got != "first" {
		t.Fatalf("HEAD: %q", got)
	}
}

// The writes really run through LazyGit: with the argv fallback removed the
// result is the same, so prove the engine is asked at all.
func TestWritesReachTheBackend(t *testing.T) {
	root, _ := writeRepo(t)
	spy := &spyWriter{Engine: New()}
	r := &gitworkbench.Runner{Backend: spy}
	if err := r.DeleteBranch(context.Background(), root, "feature"); err != nil {
		t.Fatal(err)
	}
	if spy.calls != 1 {
		t.Fatalf("backend calls: %d", spy.calls)
	}
}

type spyWriter struct {
	*Engine
	calls int
}

func (s *spyWriter) DeleteBranch(ctx context.Context, root, name string) error {
	s.calls++
	return s.Engine.DeleteBranch(ctx, root, name)
}
