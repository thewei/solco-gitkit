package gitworkbench

import (
	"strings"
	"testing"
)

// Fork-parity actions (device E2E 2026-11-03): check out a remote branch,
// rename, branch/tag at a commit, push a tag, apply a stash and keep it.

func TestCheckoutRemoteBranchTracksIt(t *testing.T) {
	root, r, origin, branch := pushRig(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	clone := t.TempDir()
	mustRunGitAt(t, clone, "clone", "-q", origin, ".")
	mustRunGitAt(t, clone, "checkout", "-q", "-b", "topic")
	writeRepoFile(t, clone, "t.txt", "t\n")
	mustRunGitAt(t, clone, "add", ".")
	mustRunGitAt(t, clone, "commit", "-qm", "topic work")
	mustRunGitAt(t, clone, "push", "-q", "origin", "topic")
	if err := r.Fetch(ctx, root, "origin"); err != nil {
		t.Fatal(err)
	}

	res, err := r.CheckoutRemoteBranch(ctx, root, "origin/topic")
	if err != nil || res.Branch != "topic" || !res.Created {
		t.Fatalf("checkout = %+v, %v", res, err)
	}
	if got := strings.TrimSpace(runGit(t, root, "rev-parse", "--abbrev-ref", "@{upstream}")); got != "origin/topic" {
		t.Fatalf("upstream = %q, want origin/topic", got)
	}
	// An existing local branch of that name is switched to, not recreated.
	runGit(t, root, "switch", "-q", branch)
	res, err = r.CheckoutRemoteBranch(ctx, root, "origin/topic")
	if err != nil || res.Created || res.Branch != "topic" {
		t.Fatalf("second checkout = %+v, %v", res, err)
	}
	if _, err := r.CheckoutRemoteBranch(ctx, root, "origin/nope"); err == nil {
		t.Fatal("an unknown remote ref must be refused")
	}
}

func TestRenameBranchAndBranchTagAtCommit(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	first := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	writeRepoFile(t, root, "b.txt", "b\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "second")
	cur := r.CurrentBranch(ctx, root)

	if err := r.RenameBranch(ctx, root, cur, "renamed"); err != nil {
		t.Fatal(err)
	}
	if got := r.CurrentBranch(ctx, root); got != "renamed" {
		t.Fatalf("branch = %q", got)
	}
	if err := r.RenameBranch(ctx, root, "ghost", "x"); err == nil {
		t.Fatal("renaming a missing branch must be refused")
	}
	if err := r.RenameBranch(ctx, root, "renamed", "bad name"); err == nil {
		t.Fatal("an invalid new name must be refused")
	}

	if err := r.CreateTagAt(ctx, root, "v-first", first); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGit(t, root, "rev-parse", "v-first^{commit}")); got != first {
		t.Fatalf("tag at %s, want %s", got, first)
	}
	if err := r.CreateTagAt(ctx, root, "v-bad", "HEAD"); err == nil {
		t.Fatal("a non-hash start point must be refused")
	}
	res, err := r.CreateBranchAt(ctx, root, "from-first", first)
	if err != nil || res.Branch != "from-first" {
		t.Fatalf("CreateBranchAt = %+v, %v", res, err)
	}
	if got := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD")); got != first {
		t.Fatal("the new branch must sit on the chosen commit")
	}
}

func TestPushTagAndStashApplyKeepsIt(t *testing.T) {
	root, r, origin, _ := pushRig(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	runGit(t, root, "tag", "v9")
	if err := r.PushTag(ctx, root, "origin", "v9"); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGit(t, origin, "tag", "-l", "v9")); got != "v9" {
		t.Fatalf("remote tags = %q", got)
	}
	if err := r.PushTag(ctx, root, "origin", "nope"); err == nil {
		t.Fatal("a missing tag must be refused")
	}

	writeRepoFile(t, root, "base.txt", "stash me\n")
	if err := r.StashPush(ctx, root, "park"); err != nil {
		t.Fatal(err)
	}
	if err := r.StashApply(ctx, root, "stash@{0}"); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, root, "base.txt"); got != "stash me\n" {
		t.Fatalf("apply must restore the change, got %q", got)
	}
	if got := strings.TrimSpace(runGit(t, root, "stash", "list")); got == "" {
		t.Fatal("apply must keep the stash entry")
	}
	if err := r.StashApply(ctx, root, "HEAD"); err == nil {
		t.Fatal("a non-stash ref must be refused")
	}
}
