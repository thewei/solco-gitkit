package gitworkbench

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A staged rename travels as two index sides. Committing the rename through
// only its new path used to leave the old path's deletion staged, so one
// rename landed split across two commits (device E2E 2026-11-03).
func TestCommitStagedRenameConsumesBothSides(t *testing.T) {
	root, r := tempRepo(t)
	ctx := context.Background()

	mustRunGit(t, root, "mv", "base.txt", "renamed.txt")

	snap, err := r.Snapshot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	pre, err := r.CapturePreflight(ctx, root, snap, []string{"renamed.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(ctx, root, CommitDraft{Subject: "rename base", Paths: []string{"renamed.txt"}}, pre, snap); err != nil {
		t.Fatalf("commit rename: %v", err)
	}

	if out := runGit(t, root, "status", "--porcelain"); out != "" {
		t.Fatalf("staged rename must commit both sides, index left %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "base.txt")); !os.IsNotExist(err) {
		t.Fatal("old side must be gone from the worktree")
	}
	head := headFiles(t, root)
	if !head["renamed.txt"] {
		t.Fatal("new side must be in HEAD")
	}
	if head["base.txt"] {
		t.Fatal("old side must not remain in HEAD")
	}
}
