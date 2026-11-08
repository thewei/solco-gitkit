package gitworkbench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pushRig is a repo wired to a bare origin with the branch published.
func pushRig(t *testing.T) (root string, r *Runner, origin, branch string) {
	t.Helper()
	origin = t.TempDir()
	mustRunGitAt(t, origin, "init", "-q", "--bare")
	root, r = tempRepo(t)
	mustRunGit(t, root, "remote", "add", "origin", origin)
	ctx, cancel := opCtx(t)
	defer cancel()
	branch = r.CurrentBranch(ctx, root)
	mustRunGit(t, root, "push", "-q", "-u", "origin", branch)
	return
}

// A rejected push must say why: "exit status 1" hid the whole reason.
func TestPushRejectedExplainsWhy(t *testing.T) {
	root, r, origin, branch := pushRig(t)
	ctx, cancel := opCtx(t)
	defer cancel()

	clone := t.TempDir()
	mustRunGitAt(t, clone, "clone", "-q", origin, ".")
	writeRepoFile(t, clone, "theirs.txt", "x\n")
	mustRunGitAt(t, clone, "add", ".")
	mustRunGitAt(t, clone, "commit", "-qm", "theirs")
	mustRunGitAt(t, clone, "push", "-q", "origin", "HEAD:"+branch)

	writeRepoFile(t, root, "mine.txt", "y\n")
	mustRunGit(t, root, "add", ".")
	mustRunGit(t, root, "commit", "-qm", "mine")
	err := r.Push(ctx, root, "origin", branch, false)
	if err == nil {
		t.Fatal("pushing a branch the remote has moved must fail")
	}
	if got := Explain(err); !strings.Contains(got, "pull first") {
		t.Fatalf("Explain = %q, want the pull-first reason", got)
	}
}

func TestPushDeclinedByRemoteHookQuotesTheRemote(t *testing.T) {
	root, r, origin, branch := pushRig(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	hook := filepath.Join(origin, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'main is protected, open a PR' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, root, "mine.txt", "y\n")
	mustRunGit(t, root, "add", ".")
	mustRunGit(t, root, "commit", "-qm", "mine")
	err := r.Push(ctx, root, "origin", branch, false)
	if err == nil {
		t.Fatal("a declining hook must fail the push")
	}
	if got := Explain(err); !strings.Contains(got, "declined") || !strings.Contains(got, "protected") {
		t.Fatalf("Explain = %q, want the remote's own words", got)
	}
}

func TestPushToMissingRemoteExplains(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	err := r.Push(ctx, root, "origin", r.CurrentBranch(ctx, root), true)
	if err == nil {
		t.Fatal("no remote must fail")
	}
	if got := Explain(err); !strings.Contains(got, "remote") {
		t.Fatalf("Explain = %q", got)
	}
}

// A branch tracking a differently named branch on the second remote must
// land there, not on a same-named branch of the first remote.
func TestPushToFollowsTheUpstream(t *testing.T) {
	root, r, _, branch := pushRig(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	fork := t.TempDir()
	mustRunGitAt(t, fork, "init", "-q", "--bare")
	mustRunGit(t, root, "remote", "add", "fork", fork)
	mustRunGit(t, root, "push", "-q", "-u", "fork", branch+":topic")

	writeRepoFile(t, root, "mine.txt", "y\n")
	mustRunGit(t, root, "add", ".")
	mustRunGit(t, root, "commit", "-qm", "mine")
	st, _ := r.Upstream(ctx, root)
	if !st.OK || st.Remote != "fork" || st.Branch != "topic" || st.Ahead != 1 {
		t.Fatalf("upstream = %+v", st)
	}
	if err := r.PushTo(ctx, root, st.Remote, branch, st.Branch, false); err != nil {
		t.Fatalf("PushTo: %v", err)
	}
	if got := strings.TrimSpace(runGit(t, fork, "rev-parse", "topic")); got != strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD")) {
		t.Fatal("the commit must land on fork/topic")
	}
}

func TestExplainSamples(t *testing.T) {
	cases := map[string]string{
		"fatal: could not read Username for 'https://x': terminal prompts disabled":       "sign in",
		"ssh: Could not resolve hostname github.com: nodename nor servname":               "reach",
		"fatal: Not possible to fast-forward, aborting.":                                  "diverged",
		"error: Your local changes to the following files would be overwritten by merge:": "overwritten",
		"fatal: something odd": "something odd",
	}
	for stderr, want := range cases {
		got := Explain(&Error{Stderr: stderr, Op: "push"})
		if !strings.Contains(got, want) {
			t.Errorf("Explain(%q) = %q, want it to contain %q", stderr, got, want)
		}
	}
	if Explain(nil) != "" || Explain(&Error{Op: "push"}) != "" {
		t.Error("nothing to read must stay silent")
	}
}

// Remote branches are merge/rebase targets of the current branch.
func TestMergeAndRebaseOntoRemoteBranch(t *testing.T) {
	root, r, origin, branch := pushRig(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	clone := t.TempDir()
	mustRunGitAt(t, clone, "clone", "-q", origin, ".")
	writeRepoFile(t, clone, "theirs.txt", "x\n")
	mustRunGitAt(t, clone, "add", ".")
	mustRunGitAt(t, clone, "commit", "-qm", "theirs")
	mustRunGitAt(t, clone, "push", "-q", "origin", "HEAD:"+branch)
	writeRepoFile(t, root, "mine.txt", "y\n")
	mustRunGit(t, root, "add", ".")
	mustRunGit(t, root, "commit", "-qm", "mine")
	if err := r.Fetch(ctx, root, "origin"); err != nil {
		t.Fatal(err)
	}
	remote := "origin/" + branch

	if err := r.MergeFastForward(ctx, root, remote); err == nil {
		t.Fatal("a diverged branch cannot fast-forward")
	} else if got := Explain(err); !strings.Contains(got, "diverged") {
		t.Fatalf("Explain = %q", got)
	}
	mustRunGit(t, root, "branch", "keep")
	if err := r.RebaseOnto(ctx, root, remote); err != nil {
		t.Fatalf("rebase: %v", err)
	}
	st, _ := r.Upstream(ctx, root)
	if st.Ahead != 1 || st.Behind != 0 {
		t.Fatalf("after the rebase the branch is ahead 1, got %+v", st)
	}
	if err := r.RebaseOnto(ctx, root, "nowhere/branch"); err == nil {
		t.Fatal("an unknown ref must be refused")
	}
	// Merge commit path on the pre-rebase copy.
	mustRunGit(t, root, "checkout", "-q", "keep")
	if err := r.MergeNoFastForward(ctx, root, remote); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got := readRepoFile(t, root, "theirs.txt"); got != "x\n" {
		t.Fatalf("merge must bring the remote file, got %q", got)
	}
}

// A rebase that hits conflicts is cancelled whole and names the files.
func TestRebaseConflictIsAbortedWithPaths(t *testing.T) {
	root, r, origin, branch := pushRig(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	clone := t.TempDir()
	mustRunGitAt(t, clone, "clone", "-q", origin, ".")
	writeRepoFile(t, clone, "base.txt", "theirs\n")
	mustRunGitAt(t, clone, "commit", "-aqm", "theirs")
	mustRunGitAt(t, clone, "push", "-q", "origin", "HEAD:"+branch)
	writeRepoFile(t, root, "base.txt", "mine\n")
	mustRunGit(t, root, "commit", "-aqm", "mine")
	if err := r.Fetch(ctx, root, "origin"); err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

	err := r.RebaseOnto(ctx, root, "origin/"+branch)
	ge, ok := GitError(err)
	if !ok || ge.Class != ErrConflict || len(ge.Paths) != 1 || ge.Paths[0] != "base.txt" {
		t.Fatalf("err = %v (%+v)", err, ge)
	}
	if got := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD")); got != head {
		t.Fatal("an aborted rebase must leave HEAD where it was")
	}
	if state, _ := r.SequencerState(ctx, root); state.Active() {
		t.Fatalf("nothing may be left half-done, got %+v", state)
	}
}
