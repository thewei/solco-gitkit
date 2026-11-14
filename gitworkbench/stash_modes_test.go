package gitworkbench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStashStagedTakesOnlyTheIndex(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	commitFile(t, root, "keep.txt", "k\n", "keep")
	writeRepoFile(t, root, "base.txt", "base line 1\nbase line 2\nstaged\n")
	runGit(t, root, "add", "base.txt")
	writeRepoFile(t, root, "other.txt", "loose\n")
	writeRepoFile(t, root, "keep.txt", "k\nunstaged edit\n")

	if err := r.StashPushMode(ctx, root, StashStaged, "just the index"); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, root, "diff", "--cached", "--name-only"); strings.TrimSpace(got) != "" {
		t.Errorf("the index is empty after a staged stash: %q", got)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "keep.txt")); string(data) != "k\nunstaged edit\n" {
		t.Errorf("an unstaged edit stays in the work tree: %q", data)
	}
	if _, err := os.Stat(filepath.Join(root, "other.txt")); err != nil {
		t.Error("an untracked file stays")
	}
	if show := runGit(t, root, "stash", "show", "--name-only", "stash@{0}"); strings.TrimSpace(show) != "base.txt" {
		t.Errorf("the stash holds the staged file only: %q", show)
	}
	if err := r.StashPushMode(ctx, root, StashStaged, ""); err == nil {
		t.Error("with nothing staged the stash is refused")
	}
}

func TestStashKeepIndexLeavesTheStagedChangesInPlace(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	writeRepoFile(t, root, "base.txt", "base line 1\nbase line 2\nstaged\n")
	runGit(t, root, "add", "base.txt")
	writeRepoFile(t, root, "new.txt", "untracked\n")

	if err := r.StashPushMode(ctx, root, StashKeepIndex, ""); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGit(t, root, "diff", "--cached", "--name-only")); got != "base.txt" {
		t.Errorf("the staged change stays in the index: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); err == nil {
		t.Error("the untracked file went into the stash")
	}
	if n := strings.Count(strings.TrimSpace(runGit(t, root, "stash", "list")), "\n") + 1; n != 1 {
		t.Errorf("one stash expected, list has %d", n)
	}
	for _, bad := range []StashMode{"", "everything"} {
		if err := r.StashPushMode(ctx, root, bad, ""); err == nil {
			t.Errorf("mode %q must be refused", bad)
		}
	}
	runGit(t, root, "reset", "-q", "--hard")
	runGit(t, root, "clean", "-fdq")
	if err := r.StashPushMode(ctx, root, StashKeepIndex, ""); err == nil {
		t.Error("a clean tree has nothing to stash")
	}
}
