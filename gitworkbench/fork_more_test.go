package gitworkbench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func commitFile(t *testing.T, root, name, content, msg string) string {
	t.Helper()
	writeRepoFile(t, root, name, content)
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", msg)
	return strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
}

func TestResetToModes(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	first := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	second := commitFile(t, root, "b.txt", "b\n", "second")

	prev, err := r.ResetTo(ctx, root, first, ResetSoft)
	if err != nil || prev != second {
		t.Fatalf("soft: prev %q err %v", prev, err)
	}
	if !stagedPaths(t, root)["b.txt"] {
		t.Fatal("soft reset keeps the change staged")
	}
	runGit(t, root, "reset", "-q", "--hard", second)
	if _, err := r.ResetTo(ctx, root, first, ResetMixed); err != nil {
		t.Fatal(err)
	}
	if stagedPaths(t, root)["b.txt"] {
		t.Fatal("mixed reset unstages")
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); err != nil {
		t.Fatal("mixed reset leaves the file")
	}

	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "again")
	again := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	writeRepoFile(t, root, "base.txt", "dirty\n")
	if _, err := r.ResetTo(ctx, root, first, ResetHard); err == nil {
		t.Fatal("hard reset on a dirty tree must be refused")
	}
	if got, _ := os.ReadFile(filepath.Join(root, "base.txt")); string(got) != "dirty\n" {
		t.Fatal("refused hard reset touched the file")
	}
	runGit(t, root, "checkout", "--", "base.txt")
	if prev, err := r.ResetTo(ctx, root, first, ResetHard); err != nil || prev != again {
		t.Fatalf("hard: prev %q err %v", prev, err)
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); err == nil {
		t.Fatal("hard reset removes the file")
	}
	if _, err := r.ResetTo(ctx, root, "HEAD", ResetSoft); err == nil {
		t.Fatal("a non-full hash is refused")
	}
}

func TestDeleteRemoteBranchAndTracking(t *testing.T) {
	root, r, origin, branch := pushRig(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	runGit(t, root, "branch", "topic")
	runGit(t, root, "push", "-q", "origin", "topic")
	if err := r.Fetch(ctx, root, "origin"); err != nil {
		t.Fatal(err)
	}

	if err := r.SetUpstream(ctx, root, "topic", "origin/topic"); err != nil {
		t.Fatal(err)
	}
	if got := r.BranchUpstream(ctx, root, "topic"); got != "origin/topic" {
		t.Fatalf("upstream = %q", got)
	}
	branches, _ := r.ListBranches(ctx, root)
	for _, b := range branches {
		if b.Name == "topic" && b.Upstream != "origin/topic" {
			t.Fatalf("ListBranches upstream = %q", b.Upstream)
		}
	}
	if err := r.SetUpstream(ctx, root, "topic", "origin/ghost"); err == nil {
		t.Fatal("an unlisted upstream is refused")
	}
	if err := r.UnsetUpstream(ctx, root, "topic"); err != nil || r.BranchUpstream(ctx, root, "topic") != "" {
		t.Fatalf("unset: %v", err)
	}

	if err := r.DeleteRemoteBranch(ctx, root, "origin", branch); err != nil {
		// the bare repo's HEAD branch may refuse deletion; topic is the real subject
		t.Logf("deleting %s: %v", branch, err)
	}
	if err := r.DeleteRemoteBranch(ctx, root, "origin", "topic"); err != nil {
		t.Fatal(err)
	}
	if out, _ := runGitAt(origin, "branch", "--list", "topic"); strings.TrimSpace(out) != "" {
		t.Fatalf("remote still has topic: %q", out)
	}
	if err := r.DeleteRemoteBranch(ctx, root, "origin", "never"); err == nil {
		t.Fatal("an unlisted remote branch is refused")
	}
}

func TestFormatPatchCompareBlameAndHistory(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	second := commitFile(t, root, "base.txt", "base line 1\nchanged line 2\nthird\n", "edit base")

	patch, err := r.FormatPatch(ctx, root, second)
	if err != nil || !strings.Contains(string(patch), "Subject: [PATCH] edit base") || !strings.Contains(string(patch), "+changed line 2") {
		t.Fatalf("patch = %q, %v", patch, err)
	}
	if _, err := r.FormatPatch(ctx, root, "HEAD"); err == nil {
		t.Fatal("only full hashes")
	}

	writeRepoFile(t, root, "base.txt", "base line 1\nchanged line 2\nthird\nlocal edit\n")
	snap, err := r.CompareToWorktree(ctx, root, second)
	if err != nil || len(snap.Files) != 1 || snap.Files[0].Path != "base.txt" || snap.TotalAdditions != 1 {
		t.Fatalf("compare = %+v, %v", snap, err)
	}

	runGit(t, root, "checkout", "--", "base.txt")
	lines, err := r.Blame(ctx, root, "", "base.txt")
	if err != nil || len(lines) != 3 {
		t.Fatalf("blame = %+v, %v", lines, err)
	}
	if lines[1].Hash != second || lines[1].Summary != "edit base" || lines[1].Text != "changed line 2" || lines[1].Line != 2 || lines[1].Author != "Test" {
		t.Fatalf("blame line 2 = %+v", lines[1])
	}
	if lines[0].Hash == second {
		t.Fatalf("line 1 belongs to the first commit: %+v", lines[0])
	}
	if _, err := r.Blame(ctx, root, "", "../x"); err == nil {
		t.Fatal("paths outside the repo are refused")
	}

	hist, err := r.FileHistory(ctx, root, "base.txt", 10)
	if err != nil || len(hist) != 2 || hist[0].Hash != second {
		t.Fatalf("history = %+v, %v", hist, err)
	}
	if _, err := r.FileHistory(ctx, root, "-x", 10); err == nil {
		t.Fatal("option-like paths are refused")
	}
}

func TestRemoteManagement(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	if err := r.AddRemote(ctx, root, "up", "https://example.com/a.git"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct{ name, url string }{
		{"-x", "https://e.com/a"}, {"a b", "https://e.com/a"}, {"ok", "-oProxy=1"}, {"ok", "ext::sh -c id"}, {"ok", ""},
	} {
		if err := r.AddRemote(ctx, root, bad.name, bad.url); err == nil {
			t.Fatalf("AddRemote(%q,%q) must be refused", bad.name, bad.url)
		}
	}
	if err := r.SetRemoteURL(ctx, root, "up", "https://example.com/b.git"); err != nil {
		t.Fatal(err)
	}
	if err := r.RenameRemote(ctx, root, "up", "upstream"); err != nil {
		t.Fatal(err)
	}
	remotes, err := r.ListRemotes(ctx, root)
	if err != nil || len(remotes) != 1 || remotes[0].Name != "upstream" || remotes[0].FetchURL != "https://example.com/b.git" || remotes[0].PushURL != "" {
		t.Fatalf("remotes = %+v, %v", remotes, err)
	}
	if err := r.SetRemoteURL(ctx, root, "ghost", "https://e.com/x"); err == nil {
		t.Fatal("unknown remote refused")
	}
	if err := r.RemoveRemote(ctx, root, "upstream"); err != nil {
		t.Fatal(err)
	}
	if remotes, _ := r.ListRemotes(ctx, root); len(remotes) != 0 {
		t.Fatalf("remotes after remove = %+v", remotes)
	}
}

func TestSubmoduleListAndUpdate(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	sub := t.TempDir()
	mustRunGitAt(t, sub, "init", "-q")
	writeRepoFile(t, sub, "s.txt", "s\n")
	mustRunGitAt(t, sub, "add", ".")
	mustRunGitAt(t, sub, "-c", "user.email=a@b.c", "-c", "user.name=t", "commit", "-qm", "sub")
	runGit(t, root, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "vendor/sub")
	runGit(t, root, "commit", "-qm", "add submodule")

	subs, err := r.ListSubmodules(ctx, root)
	if err != nil || len(subs) != 1 || subs[0].Path != "vendor/sub" || subs[0].State != SubmoduleClean {
		t.Fatalf("subs = %+v, %v", subs, err)
	}
	runGit(t, root, "submodule", "deinit", "-f", "vendor/sub")
	subs, _ = r.ListSubmodules(ctx, root)
	if len(subs) != 1 || subs[0].State != SubmoduleUninitialized {
		t.Fatalf("after deinit = %+v", subs)
	}
	if err := r.UpdateSubmodule(ctx, root, "nope"); err == nil {
		t.Fatal("only listed submodules")
	}
}

func rebaseRepo(t *testing.T) (root string, r *Runner, base string, hashes []string) {
	root, r = tempRepo(t)
	base = strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	for _, n := range []string{"one", "two", "three"} {
		hashes = append(hashes, commitFile(t, root, n+".txt", n+"\n", "add "+n))
	}
	return
}

func logSubjects(t *testing.T, root string) string {
	return strings.TrimSpace(runGit(t, root, "log", "--format=%s", "--reverse", "-n", "6"))
}

func TestInteractiveRebaseSquashRewordDropReorder(t *testing.T) {
	root, r, base, h := rebaseRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()

	plan, err := r.RebasePlan(ctx, root, base)
	if err != nil || len(plan) != 3 || plan[0].Hash != h[0] || plan[2].Hash != h[2] {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	if plan[0].Message != "add one" {
		t.Fatalf("message = %q", plan[0].Message)
	}
	if plan[0].Published {
		t.Fatal("without remote refs nothing is published")
	}

	// reorder three before two, squash two into three, reword one, drop nothing
	err = r.InteractiveRebase(ctx, root, base, []RebaseStep{
		{Hash: h[0], Action: RebaseReword, Message: "first, reworded"},
		{Hash: h[2], Action: RebasePick},
		{Hash: h[1], Action: RebaseFixup},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := logSubjects(t, root)
	if got != "initial\nfirst, reworded\nadd three" {
		t.Fatalf("history = %q", got)
	}
	for _, f := range []string{"one.txt", "two.txt", "three.txt"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Fatalf("%s lost: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "rebase-merge")); err == nil {
		t.Fatal("rebase left unfinished")
	}

	// drop the last commit
	head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	plan, _ = r.RebasePlan(ctx, root, base)
	if len(plan) != 2 {
		t.Fatalf("plan 2 = %+v", plan)
	}
	if err := r.InteractiveRebase(ctx, root, base, []RebaseStep{
		{Hash: plan[0].Hash, Action: RebasePick}, {Hash: plan[1].Hash, Action: RebaseDrop}}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD")) == head || logSubjects(t, root) != "initial\nfirst, reworded" {
		t.Fatalf("drop failed: %q", logSubjects(t, root))
	}
}

func TestInteractiveRebaseRefusals(t *testing.T) {
	root, r, base, h := rebaseRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	pick := func(hs ...string) []RebaseStep {
		var s []RebaseStep
		for _, x := range hs {
			s = append(s, RebaseStep{Hash: x, Action: RebasePick})
		}
		return s
	}
	if err := r.InteractiveRebase(ctx, root, base, pick(h[0], h[1])); err == nil {
		t.Fatal("a plan missing a commit is refused")
	}
	if err := r.InteractiveRebase(ctx, root, base, pick(h[0], h[0], h[1])); err == nil {
		t.Fatal("a duplicated commit is refused")
	}
	steps := pick(h[0], h[1], h[2])
	steps[0].Action = RebaseSquash
	if err := r.InteractiveRebase(ctx, root, base, steps); err == nil {
		t.Fatal("squash first is refused")
	}
	steps = pick(h[0], h[1], h[2])
	steps[1].Action = RebaseReword
	if err := r.InteractiveRebase(ctx, root, base, steps); err == nil {
		t.Fatal("reword without a message is refused")
	}
	writeRepoFile(t, root, "dirty.txt", "x\n")
	runGit(t, root, "add", ".")
	if err := r.InteractiveRebase(ctx, root, base, pick(h[0], h[1], h[2])); err == nil {
		t.Fatal("a dirty tree is refused")
	}
	if _, err := r.RebasePlan(ctx, root, h[2]); err == nil {
		t.Fatal("an empty range is refused")
	}
}

func TestInteractiveRebaseConflictAborts(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	base := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	a := commitFile(t, root, "f.txt", "a\n", "make f")
	b := commitFile(t, root, "f.txt", "a\nb\n", "extend f")
	before := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	// dropping the commit f.txt is created in makes the second one conflict
	err := r.InteractiveRebase(ctx, root, base, []RebaseStep{{Hash: a, Action: RebaseDrop}, {Hash: b, Action: RebasePick}})
	ge, ok := GitError(err)
	if !ok || ge.Class != ErrConflict || len(ge.Paths) == 0 {
		t.Fatalf("err = %v (%+v)", err, ge)
	}
	if strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD")) != before {
		t.Fatal("HEAD moved")
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "rebase-merge")); err == nil {
		t.Fatal("rebase left behind")
	}
}

func TestWorkingPatchCoversStagedUnstagedAndUntrackedWithoutTouchingTheIndex(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	if _, err := r.WorkingPatch(ctx, root); err == nil {
		t.Fatal("a clean tree has nothing to export")
	}
	commitFile(t, root, "kept.txt", "one\n", "add kept")
	writeRepoFile(t, root, "kept.txt", "one\ntwo\n") // unstaged edit
	writeRepoFile(t, root, "staged.txt", "staged\n") // staged new file
	runGit(t, root, "add", "staged.txt")
	writeRepoFile(t, root, "loose.txt", "loose\n") // untracked
	writeRepoFile(t, root, ".gitignore", "ignored.txt\n")
	writeRepoFile(t, root, "ignored.txt", "nope\n")
	before := runGit(t, root, "status", "--porcelain=v1")

	patch, err := r.WorkingPatch(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	text := string(patch)
	for _, want := range []string{"+two", "+staged", "+loose", "b/staged.txt", "b/loose.txt"} {
		if !strings.Contains(text, want) {
			t.Fatalf("patch misses %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "nope") {
		t.Fatal("ignored files stay out of the patch")
	}
	if after := runGit(t, root, "status", "--porcelain=v1"); after != before {
		t.Fatalf("the repository's own index moved:\n%s\n---\n%s", before, after)
	}
	// The patch applies to a clean checkout of HEAD.
	runGit(t, root, "stash", "push", "-u", "-q")
	applyFile := filepath.Join(t.TempDir(), "w.patch")
	_ = os.WriteFile(applyFile, patch, 0o644)
	runGit(t, root, "apply", applyFile)
	if got, _ := os.ReadFile(filepath.Join(root, "loose.txt")); string(got) != "loose\n" {
		t.Fatalf("applied patch lost the untracked file: %q", got)
	}
}

func TestStashPatchIncludesUntrackedAndRefusesBadRefs(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	writeRepoFile(t, root, "base.txt", "base line 1\nstashed edit\n")
	writeRepoFile(t, root, "fresh.txt", "fresh\n")
	if err := r.StashPush(ctx, root, "parked"); err != nil {
		t.Fatal(err)
	}
	patch, err := r.StashPatch(ctx, root, "stash@{0}")
	if err != nil || !strings.Contains(string(patch), "+stashed edit") || !strings.Contains(string(patch), "+fresh") {
		t.Fatalf("stash patch = %q, %v", patch, err)
	}
	for _, bad := range []string{"HEAD", "stash@{x}", "--help", "stash@{0}; rm"} {
		if _, err := r.StashPatch(ctx, root, bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
	if _, err := r.StashPatch(ctx, root, "stash@{7}"); err == nil {
		t.Fatal("a missing stash is an error")
	}
}
