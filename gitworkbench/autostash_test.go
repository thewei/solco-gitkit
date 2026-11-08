package gitworkbench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithAutostashReappliesAfterABlockedSwitch(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	ten := strings.Repeat("line\n", 10)
	commitAll(t, root, "doc.txt", ten, "doc")
	mustRunGit(t, root, "branch", "other")
	mustRunGit(t, root, "switch", "-q", "other")
	commitAll(t, root, "doc.txt", "first changed\n"+strings.Repeat("line\n", 9), "other edits the top")
	mustRunGit(t, root, "switch", "-q", "-")
	// A local edit at the bottom: Git refuses the switch outright.
	writeRepoFile(t, root, "doc.txt", strings.Repeat("line\n", 9)+"last changed\n")
	writeRepoFile(t, root, "fresh.txt", "new\n")
	blocked := func() error { _, err := r.SwitchBranch(ctx, root, "other"); return err }
	err := blocked()
	if err == nil || !LocalChangesBlocked(err) {
		t.Fatalf("control: the plain switch must be refused for local changes: %v", err)
	}
	if cur := gitLines(t, root, "branch", "--show-current"); cur == "other" {
		t.Fatal("control: still on the old branch")
	}
	res, err := r.WithAutostash(ctx, root, blocked)
	if err != nil || !res.Stashed || res.Kept {
		t.Fatalf("autostash switch: %+v %v", res, err)
	}
	if gitLines(t, root, "branch", "--show-current") != "other" {
		t.Fatal("should have switched")
	}
	data, _ := os.ReadFile(filepath.Join(root, "doc.txt"))
	if !strings.HasPrefix(string(data), "first changed\n") || !strings.HasSuffix(string(data), "last changed\n") {
		t.Fatalf("both edits must be present after the reapply:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(root, "fresh.txt")); err != nil {
		t.Fatal("the new file must come back too")
	}
	if got := gitLines(t, root, "stash", "list"); got != "" {
		t.Fatalf("a clean reapply leaves no stash: %q", got)
	}
}

func TestWithAutostashKeepsTheStashWhenTheReapplyConflicts(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	mustRunGit(t, root, "branch", "other")
	mustRunGit(t, root, "switch", "-q", "other")
	commitAll(t, root, "base.txt", "other version\n", "other")
	mustRunGit(t, root, "switch", "-q", "-")
	writeRepoFile(t, root, "base.txt", "local version\n")
	res, err := r.WithAutostash(ctx, root, func() error { _, err := r.SwitchBranch(ctx, root, "other"); return err })
	if err != nil || !res.Stashed || !res.Kept {
		t.Fatalf("a conflicting reapply keeps the stash: %+v %v", res, err)
	}
	if gitLines(t, root, "branch", "--show-current") != "other" {
		t.Fatal("the switch itself happened")
	}
	if !strings.Contains(gitLines(t, root, "stash", "list"), AutostashMessage) {
		t.Fatal("the autostash must stay in the list")
	}
}

func TestWithAutostashRestoresWhenTheActionFails(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	writeRepoFile(t, root, "base.txt", "edited\n")
	res, err := r.WithAutostash(ctx, root, func() error { _, err := r.SwitchBranch(ctx, root, "no-such-branch"); return err })
	if err == nil || !res.Stashed || res.Kept {
		t.Fatalf("a failed action: %+v %v", res, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "base.txt"))
	if string(data) != "edited\n" || gitLines(t, root, "stash", "list") != "" {
		t.Fatalf("the changes must be back and the stash gone: %q", data)
	}
}

func TestWithAutostashOnACleanTreeJustRuns(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	mustRunGit(t, root, "branch", "other")
	res, err := r.WithAutostash(ctx, root, func() error { _, err := r.SwitchBranch(ctx, root, "other"); return err })
	if err != nil || res.Stashed || res.Kept {
		t.Fatalf("clean tree: %+v %v", res, err)
	}
}
