package gitworkbench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func commitAll(t *testing.T, root, name, content, msg string) {
	t.Helper()
	writeRepoFile(t, root, name, content)
	mustRunGit(t, root, "add", "--", name)
	mustRunGit(t, root, "commit", "-qm", msg)
}

func gitLines(t *testing.T, root string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, root, args...))
}

// divergedRig: origin has "theirs", the local branch has "mine".
func divergedRig(t *testing.T) (root string, r *Runner, branch string) {
	root, r, origin, branch := pushRig(t)
	clone := t.TempDir()
	mustRunGitAt(t, clone, "clone", "-q", origin, ".")
	writeRepoFile(t, clone, "theirs.txt", "x\n")
	mustRunGitAt(t, clone, "add", ".")
	mustRunGitAt(t, clone, "commit", "-qm", "theirs")
	mustRunGitAt(t, clone, "push", "-q", "origin", "HEAD:"+branch)
	commitAll(t, root, "mine.txt", "y\n", "mine")
	return root, r, branch
}

func TestPullModes(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()

	root, r, branch := divergedRig(t)
	if err := r.Pull(ctx, root, "origin", branch, PullFastForward); err == nil {
		t.Fatal("fast-forward pull of a diverged branch must be refused")
	}
	if err := r.Pull(ctx, root, "origin", branch, PullMerge); err != nil {
		t.Fatalf("merge pull: %v", err)
	}
	if got := len(strings.Fields(gitLines(t, root, "rev-list", "--parents", "-n", "1", "HEAD"))); got != 3 {
		t.Fatalf("merge pull should create a merge commit, parents=%d", got-1)
	}

	root, r, branch = divergedRig(t)
	if err := r.Pull(ctx, root, "origin", branch, PullRebase); err != nil {
		t.Fatalf("rebase pull: %v", err)
	}
	if got := len(strings.Fields(gitLines(t, root, "rev-list", "--parents", "-n", "1", "HEAD"))); got != 2 {
		t.Fatal("rebase pull must keep history linear")
	}
	if gitLines(t, root, "log", "-1", "--format=%s") != "mine" {
		t.Fatal("local commit should sit on top after the rebase")
	}

	if err := r.Pull(ctx, root, "origin", branch, PullMode("nope")); err == nil {
		t.Fatal("unknown mode must be refused")
	}
}

func TestPullRebaseConflictAborts(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r, origin, branch := pushRig(t)
	clone := t.TempDir()
	mustRunGitAt(t, clone, "clone", "-q", origin, ".")
	writeRepoFile(t, clone, "base.txt", "theirs\n")
	mustRunGitAt(t, clone, "commit", "-qam", "theirs")
	mustRunGitAt(t, clone, "push", "-q", "origin", "HEAD:"+branch)
	commitAll(t, root, "base.txt", "mine\n", "mine")
	head := gitLines(t, root, "rev-parse", "HEAD")

	err := r.Pull(ctx, root, "origin", branch, PullRebase)
	ge, ok := GitError(err)
	if !ok || ge.Class != ErrConflict || len(ge.Paths) != 1 || ge.Paths[0] != "base.txt" {
		t.Fatalf("want conflict on base.txt, got %v", err)
	}
	if gitLines(t, root, "rev-parse", "HEAD") != head || gitLines(t, root, "status", "--porcelain") != "" {
		t.Fatal("aborted rebase must leave the repo as it was")
	}
	// A dirty tree is refused before anything starts.
	writeRepoFile(t, root, "dirty.txt", "d\n")
	if err := r.Pull(ctx, root, "origin", branch, PullRebase); err == nil {
		t.Fatal("rebase pull needs a clean tree")
	}
}

func TestFetchAllPrune(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r, origin, _ := pushRig(t)
	mustRunGit(t, root, "push", "-q", "origin", "HEAD:refs/heads/gone")
	mustRunGit(t, root, "fetch", "-q", "origin")
	if gitLines(t, root, "branch", "-r", "--list", "origin/gone") == "" {
		t.Fatal("setup: tracking ref missing")
	}
	mustRunGitAt(t, origin, "branch", "-D", "gone")
	if err := r.FetchAll(ctx, root, false); err != nil {
		t.Fatal(err)
	}
	if gitLines(t, root, "branch", "-r", "--list", "origin/gone") == "" {
		t.Fatal("plain fetch must not prune")
	}
	if err := r.FetchAll(ctx, root, true); err != nil {
		t.Fatal(err)
	}
	if gitLines(t, root, "branch", "-r", "--list", "origin/gone") != "" {
		t.Fatal("fetch --prune should drop the vanished branch")
	}
}

func TestMergeSquash(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	main := r.CurrentBranch(ctx, root)
	mustRunGit(t, root, "checkout", "-qb", "topic")
	commitAll(t, root, "one.txt", "1\n", "one")
	commitAll(t, root, "two.txt", "2\n", "two")
	mustRunGit(t, root, "checkout", "-q", main)
	head := gitLines(t, root, "rev-parse", "HEAD")

	writeRepoFile(t, root, "dirty.txt", "d\n")
	if err := r.MergeSquash(ctx, root, "topic"); err == nil {
		t.Fatal("dirty tree must be refused")
	}
	_ = os.Remove(filepath.Join(root, "dirty.txt"))

	if err := r.MergeSquash(ctx, root, "topic"); err != nil {
		t.Fatalf("squash: %v", err)
	}
	if gitLines(t, root, "rev-parse", "HEAD") != head {
		t.Fatal("squash must not commit")
	}
	if st := stagedPaths(t, root); !st["one.txt"] || !st["two.txt"] {
		t.Fatalf("squash result should be staged: %v", st)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "MERGE_HEAD")); err == nil {
		t.Fatal("squash must not leave MERGE_HEAD")
	}
}

func TestMergeSquashConflictRollsBack(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	main := r.CurrentBranch(ctx, root)
	mustRunGit(t, root, "checkout", "-qb", "topic")
	commitAll(t, root, "base.txt", "topic\n", "topic change")
	mustRunGit(t, root, "checkout", "-q", main)
	commitAll(t, root, "base.txt", "main\n", "main change")

	err := r.MergeSquash(ctx, root, "topic")
	ge, ok := GitError(err)
	if !ok || ge.Class != ErrConflict || len(ge.Paths) != 1 {
		t.Fatalf("want conflict error, got %v", err)
	}
	if gitLines(t, root, "status", "--porcelain") != "" {
		t.Fatal("a conflicting squash must roll back to a clean tree")
	}
}

func TestCheckoutDetachedAndRestoreFile(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	old := gitLines(t, root, "rev-parse", "HEAD")
	commitAll(t, root, "base.txt", "changed\n", "change")

	if err := r.RestoreFile(ctx, root, old, "base.txt"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "base.txt"))
	if string(data) != "base line 1\nbase line 2\n" {
		t.Fatalf("file not restored: %q", data)
	}
	if st := stagedPaths(t, root); st["base.txt"] {
		t.Fatal("restore must stay unstaged")
	}
	if err := r.RestoreFile(ctx, root, old, "missing.txt"); err == nil {
		t.Fatal("a path absent from the commit must be refused")
	}
	if err := r.RestoreFile(ctx, root, old, "../etc/passwd"); err == nil {
		t.Fatal("path escape must be refused")
	}
	if err := r.RestoreFile(ctx, root, "HEAD", "base.txt"); err == nil {
		t.Fatal("non-full hash must be refused")
	}
	mustRunGit(t, root, "checkout", "--", "base.txt")

	if err := r.CheckoutDetached(ctx, root, old); err != nil {
		t.Fatal(err)
	}
	if r.CurrentBranch(ctx, root) != "" || gitLines(t, root, "rev-parse", "HEAD") != old {
		t.Fatal("HEAD should be detached at the commit")
	}
	if err := r.CheckoutDetached(ctx, root, "main"); err == nil {
		t.Fatal("non-hash must be refused")
	}
}

func TestApplyPatch(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	commitAll(t, root, "feat.txt", "feature\n", "feat")
	hash := gitLines(t, root, "rev-parse", "HEAD")
	patch, err := r.FormatPatch(ctx, root, hash)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "feat.patch")
	if err := os.WriteFile(file, patch, 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, root, "reset", "-q", "--hard", "HEAD~1")

	if err := r.ApplyPatch(ctx, root, file); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "feat.txt")); err != nil {
		t.Fatal("patched file missing")
	}
	if len(stagedPaths(t, root)) != 0 {
		t.Fatal("apply must not stage")
	}
	// Applying twice does not fit and changes nothing more.
	if err := r.ApplyPatch(ctx, root, file); err == nil {
		t.Fatal("a patch that does not apply must be refused")
	}

	bad := filepath.Join(t.TempDir(), "bad.patch")
	_ = os.WriteFile(bad, []byte("not a patch\n"), 0o644)
	if err := r.ApplyPatch(ctx, root, bad); err == nil {
		t.Fatal("garbage must be refused")
	}
	if err := r.ApplyPatch(ctx, root, "relative.patch"); err == nil {
		t.Fatal("relative path must be refused")
	}
	empty := filepath.Join(t.TempDir(), "empty.patch")
	_ = os.WriteFile(empty, nil, 0o644)
	if err := r.ApplyPatch(ctx, root, empty); err == nil {
		t.Fatal("empty file must be refused")
	}
}

func TestAnnotatedTagAndDeleteRemoteTag(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r, origin, _ := pushRig(t)

	if err := r.CreateTagAnnotated(ctx, root, "v1", "", "Release one\n\nbody"); err != nil {
		t.Fatal(err)
	}
	if gitLines(t, root, "cat-file", "-t", "v1") != "tag" {
		t.Fatal("a message must make an annotated tag")
	}
	if !strings.Contains(gitLines(t, root, "tag", "-n9", "-l", "v1"), "Release one") {
		t.Fatal("annotation missing")
	}
	if err := r.CreateTagAnnotated(ctx, root, "light", "", ""); err != nil {
		t.Fatal(err)
	}
	if gitLines(t, root, "cat-file", "-t", "light") != "commit" {
		t.Fatal("no message must stay lightweight")
	}
	for _, bad := range []string{"", "-x", "a b", "a..b"} {
		if err := r.CreateTagAnnotated(ctx, root, bad, "", "m"); err == nil {
			t.Fatalf("name %q must be refused", bad)
		}
	}
	if err := r.CreateTagAnnotated(ctx, root, "x", "HEAD", ""); err == nil {
		t.Fatal("non-full hash must be refused")
	}

	mustRunGit(t, root, "push", "-q", "origin", "v1")
	if err := r.DeleteRemoteTag(ctx, root, "origin", "v1"); err != nil {
		t.Fatal(err)
	}
	if out, _ := runGitAt(origin, "tag", "-l", "v1"); strings.TrimSpace(out) != "" {
		t.Fatal("tag should be gone from the remote")
	}
	if gitLines(t, root, "tag", "-l", "v1") == "" {
		t.Fatal("local tag must stay")
	}
	if err := r.DeleteRemoteTag(ctx, root, "origin", "main"); err == nil {
		t.Fatal("a branch name is not a tag")
	}
	if err := r.DeleteRemoteTag(ctx, root, "nowhere", "v1"); err == nil {
		t.Fatal("unknown remote must be refused")
	}
}

func TestRemoveWorktree(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	wt := filepath.Join(t.TempDir(), "side")
	if err := r.AddWorktree(ctx, root, wt); err != nil {
		t.Fatal(err)
	}
	list, _ := r.ListWorktrees(ctx, root)
	if len(list) != 2 {
		t.Fatalf("want 2 work trees, got %+v", list)
	}
	wt = list[1].Path // Git reports the resolved path (macOS /private/var)
	if err := r.RemoveWorktree(ctx, root, list[0].Path); err == nil {
		t.Fatal("main work tree must not be removable")
	}
	if err := r.RemoveWorktree(ctx, root, "/tmp/not-a-worktree"); err == nil {
		t.Fatal("unlisted path must be refused")
	}
	// A dirty work tree is Git's refusal, not forced.
	writeRepoFile(t, wt, "dirty.txt", "d\n")
	mustRunGitAt(t, wt, "add", "dirty.txt")
	if err := r.RemoveWorktree(ctx, root, wt); err == nil {
		t.Fatal("dirty work tree must not be removed without force")
	}
	mustRunGitAt(t, wt, "reset", "-q")
	_ = os.Remove(filepath.Join(wt, "dirty.txt"))
	if err := r.RemoveWorktree(ctx, root, wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Fatal("folder should be gone")
	}
}

func conflictedMerge(t *testing.T) (string, *Runner) {
	t.Helper()
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	main := r.CurrentBranch(ctx, root)
	mustRunGit(t, root, "checkout", "-qb", "topic")
	commitAll(t, root, "base.txt", "topic\n", "topic")
	commitAll(t, root, "only-topic.txt", "t\n", "topic add")
	mustRunGit(t, root, "checkout", "-q", main)
	commitAll(t, root, "base.txt", "main\n", "main")
	if err := r.MergeNoFastForward(ctx, root, "topic"); err == nil {
		t.Fatal("setup: merge should conflict")
	}
	return root, r
}

func TestResolveConflictSides(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()

	root, r := conflictedMerge(t)
	if err := r.ResolveConflict(ctx, root, "base.txt", ConflictOurs); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "base.txt"))
	if string(data) != "main\n" {
		t.Fatalf("ours should keep main, got %q", data)
	}
	if left, _ := r.UnmergedPaths(ctx, root); len(left) != 0 {
		t.Fatalf("still unmerged: %v", left)
	}
	if err := r.ResolveConflict(ctx, root, "base.txt", ConflictOurs); err == nil {
		t.Fatal("a resolved file must be refused as stale")
	}

	root, r = conflictedMerge(t)
	if err := r.ResolveConflict(ctx, root, "base.txt", ConflictTheirs); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(root, "base.txt"))
	if string(data) != "topic\n" {
		t.Fatalf("theirs should take topic, got %q", data)
	}
	if err := r.ResolveConflict(ctx, root, "only-topic.txt", ConflictTheirs); err == nil {
		t.Fatal("a file without conflict must be refused")
	}
	if err := r.ResolveConflict(ctx, root, "base.txt", ConflictSide("both")); err == nil {
		t.Fatal("unknown side must be refused")
	}
}

func TestResolveConflictMarked(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := conflictedMerge(t)
	if err := r.ResolveConflict(ctx, root, "base.txt", ConflictMarked); err == nil {
		t.Fatal("markers still in the file must block Mark as Resolved")
	}
	writeRepoFile(t, root, "base.txt", "edited by hand\n")
	if err := r.ResolveConflict(ctx, root, "base.txt", ConflictMarked); err != nil {
		t.Fatal(err)
	}
	if left, _ := r.UnmergedPaths(ctx, root); len(left) != 0 {
		t.Fatalf("still unmerged: %v", left)
	}
}

func TestResolveConflictDeletedSide(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	main := r.CurrentBranch(ctx, root)
	mustRunGit(t, root, "checkout", "-qb", "topic")
	mustRunGit(t, root, "rm", "-q", "base.txt")
	mustRunGit(t, root, "commit", "-qm", "delete")
	mustRunGit(t, root, "checkout", "-q", main)
	commitAll(t, root, "base.txt", "modified\n", "modify")
	_ = r.MergeNoFastForward(ctx, root, "topic")

	if err := r.ResolveConflict(ctx, root, "base.txt", ConflictTheirs); err != nil {
		t.Fatalf("taking the deleting side: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "base.txt")); err == nil {
		t.Fatal("file should be deleted")
	}
	if left, _ := r.UnmergedPaths(ctx, root); len(left) != 0 {
		t.Fatalf("still unmerged: %v", left)
	}
}

func TestReflog(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	commitAll(t, root, "a.txt", "a\n", "second")
	mustRunGit(t, root, "checkout", "-q", "--detach", "HEAD~1")

	entries, err := r.Reflog(ctx, root, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 3 {
		t.Fatalf("want ≥3 entries, got %d", len(entries))
	}
	if entries[0].Selector != "HEAD@{0}" || !strings.HasPrefix(entries[0].Action, "checkout") || !validFullCommitID(entries[0].Hash) {
		t.Fatalf("bad newest entry: %+v", entries[0])
	}
	if entries[1].Subject != "second" || entries[1].Time.IsZero() {
		t.Fatalf("bad second entry: %+v", entries[1])
	}
	if one, _ := r.Reflog(ctx, root, 1); len(one) != 1 {
		t.Fatal("limit not honoured")
	}
}

func TestSubmoduleAddRemove(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()

	// No protocol.file.allow in the environment: a typed local path must
	// still work, which is the point of addLocalSubmodule.
	sub, _ := tempRepo(t)
	root, r := tempRepo(t)

	if err := r.AddSubmodule(ctx, root, "relative/path", "lib"); err == nil {
		t.Fatal("a bare local path address must be refused")
	}
	if err := r.AddSubmodule(ctx, root, sub, "../escape"); err == nil {
		t.Fatal("path escape must be refused")
	}
	if err := r.AddSubmodule(ctx, root, "file://"+sub, "lib"); err != nil {
		t.Fatalf("add: %v", err)
	}
	subs, _ := r.ListSubmodules(ctx, root)
	if len(subs) != 1 || subs[0].Path != "lib" {
		t.Fatalf("submodule not listed: %+v", subs)
	}
	if err := r.AddSubmodule(ctx, root, "file://"+sub, "lib"); err == nil {
		t.Fatal("adding the same path twice must be refused")
	}
	if err := r.AddSubmodule(ctx, root, sub, "lib2"); err != nil {
		t.Fatalf("an absolute path is a local address too: %v", err)
	}
	if err := r.RemoveSubmodule(ctx, root, "lib2"); err != nil {
		t.Fatalf("remove lib2: %v", err)
	}
	if err := r.RemoveSubmodule(ctx, root, "nothing"); err == nil {
		t.Fatal("non-submodule must be refused")
	}
	if err := r.RemoveSubmodule(ctx, root, "lib"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if subs, _ := r.ListSubmodules(ctx, root); len(subs) != 0 {
		t.Fatalf("still listed: %+v", subs)
	}
	if _, err := os.Stat(filepath.Join(root, "lib")); err == nil {
		t.Fatal("working folder should be gone")
	}
}

func TestStashPushPathsAndClear(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	writeRepoFile(t, root, "base.txt", "changed\n")
	writeRepoFile(t, root, "new.txt", "new\n")
	writeRepoFile(t, root, "keep.txt", "keep\n")

	if err := r.StashPushPaths(ctx, root, "only two", nil); err == nil {
		t.Fatal("no files must be refused")
	}
	if err := r.StashPushPaths(ctx, root, "x", []string{"../x"}); err == nil {
		t.Fatal("path escape must be refused")
	}
	if err := r.StashPushPaths(ctx, root, "only two", []string{"base.txt", "new.txt"}); err != nil {
		t.Fatal(err)
	}
	status := gitLines(t, root, "status", "--porcelain")
	if status != "?? keep.txt" {
		t.Fatalf("only keep.txt should remain, got %q", status)
	}
	if !strings.Contains(gitLines(t, root, "stash", "list"), "only two") {
		t.Fatal("message missing in stash list")
	}
	if err := r.StashClear(ctx, root); err != nil {
		t.Fatal(err)
	}
	if gitLines(t, root, "stash", "list") != "" {
		t.Fatal("stash should be empty")
	}
}

func TestParseWebRepoAndCommitURL(t *testing.T) {
	cases := []struct {
		in, base, name, commit string
		ok                     bool
	}{
		{"git@github.com:acme/app.git", "https://github.com/acme/app", "GitHub", "https://github.com/acme/app/commit/H", true},
		{"https://github.com/acme/app.git", "https://github.com/acme/app", "GitHub", "https://github.com/acme/app/commit/H", true},
		{"ssh://git@gitlab.example.com:2222/grp/sub/app.git", "https://gitlab.example.com/grp/sub/app", "GitLab", "https://gitlab.example.com/grp/sub/app/-/commit/H", true},
		{"git@bitbucket.org:t/r.git", "https://bitbucket.org/t/r", "Bitbucket", "https://bitbucket.org/t/r/commits/H", true},
		{"https://codeberg.org/a/b", "https://codeberg.org/a/b", "Codeberg", "https://codeberg.org/a/b/commit/H", true},
		{"https://git.corp.io/a/b/", "https://git.corp.io/a/b", "git.corp.io", "https://git.corp.io/a/b/commit/H", true},
		{"/Users/me/remotes/origin.git", "", "", "", false},
		{"../origin.git", "", "", "", false},
		{"file:///tmp/x.git", "", "", "", false},
		{"https://github.com", "", "", "", false},
		{"", "", "", "", false},
	}
	for _, c := range cases {
		w, ok := ParseWebRepo(c.in)
		if ok != c.ok {
			t.Fatalf("%q: ok=%v want %v", c.in, ok, c.ok)
		}
		if !ok {
			continue
		}
		if w.Base != c.base || w.Name() != c.name || w.CommitURL("H") != c.commit {
			t.Fatalf("%q: got %+v %s %s", c.in, w, w.Name(), w.CommitURL("H"))
		}
	}
}

func TestCommitNoVerifyAndSignOff(t *testing.T) {
	ctx := context.Background()
	root, r := tempRepo(t)
	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit := func(d CommitDraft, name string) error {
		writeRepoFile(t, root, name, "x\n")
		snap, err := r.Snapshot(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		pre, err := r.CapturePreflight(ctx, root, snap, []string{name})
		if err != nil {
			t.Fatal(err)
		}
		d.Paths = []string{name}
		_, err = r.Commit(ctx, root, d, pre, snap)
		return err
	}
	if err := commit(CommitDraft{Subject: "blocked"}, "a.txt"); err == nil {
		t.Fatal("the failing hook should block a normal commit")
	}
	if err := commit(CommitDraft{Subject: "skipped", NoVerify: true, SignOff: true}, "a.txt"); err != nil {
		t.Fatalf("no-verify commit: %v", err)
	}
	if msg := gitLines(t, root, "log", "-1", "--format=%B"); !strings.Contains(msg, "\nSigned-off-by: ") {
		t.Fatalf("sign-off trailer missing: %q", msg)
	}
}
