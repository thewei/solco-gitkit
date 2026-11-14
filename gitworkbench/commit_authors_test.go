package gitworkbench

import (
	"context"
	"strings"
	"testing"
)

func commitPaths(t *testing.T, r *Runner, root string, draft CommitDraft) error {
	t.Helper()
	ctx := context.Background()
	snap, err := r.Snapshot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	pre, err := r.CapturePreflight(ctx, root, snap, draft.Paths)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Commit(ctx, root, draft, pre, snap)
	return err
}

func TestCommitCarriesCoAuthorTrailersGitReads(t *testing.T) {
	root, r := tempRepo(t)
	writeRepoFile(t, root, "a.txt", "a\n")
	err := commitPaths(t, r, root, CommitDraft{
		Subject: "pair work", Body: "details", Paths: []string{"a.txt"},
		CoAuthors: []string{"Ada Example <ada@example.com>", " Bob <bob@example.com> ", "Ada Example <ada@example.com>"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Git's own trailer parser must see exactly the two distinct people.
	got := strings.TrimSpace(runGit(t, root, "log", "-1", "--format=%(trailers:key=Co-authored-by,valueonly)"))
	if got != "Ada Example <ada@example.com>\nBob <bob@example.com>" {
		t.Fatalf("trailers = %q", got)
	}
	if body := runGit(t, root, "log", "-1", "--format=%b"); !strings.HasPrefix(body, "details\n") {
		t.Errorf("the body stays above the trailers: %q", body)
	}

	writeRepoFile(t, root, "b.txt", "b\n")
	for _, bad := range []string{"no email", "<x@y.z>", "Name <broken>", "Name <a@b.c>\nInjected: yes", "Name <a@b.c> trailing"} {
		if err := commitPaths(t, r, root, CommitDraft{Subject: "x", Paths: []string{"b.txt"}, CoAuthors: []string{bad}}); err == nil {
			t.Errorf("co-author %q must be refused", bad)
		}
	}
	if n := strings.Count(strings.TrimSpace(runGit(t, root, "log", "--format=%h")), "\n") + 1; n != 2 {
		t.Errorf("a refused co-author commits nothing: %d commits", n)
	}
}

func TestAmendCanResetTheAuthor(t *testing.T) {
	root, r := tempRepo(t)
	runGit(t, root, "commit", "-q", "--amend", "--no-edit", "--author=Old Hand <old@example.com>")
	writeRepoFile(t, root, "a.txt", "a\n")
	runGit(t, root, "add", "a.txt")
	if err := commitPaths(t, r, root, CommitDraft{Subject: "x", Paths: []string{"a.txt"}, ResetAuthor: true}); err == nil {
		t.Fatal("reset author without amend is refused")
	}
	if err := commitPaths(t, r, root, CommitDraft{Subject: "initial", Paths: []string{"a.txt"}, Amend: true, ResetAuthor: true}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGit(t, root, "log", "-1", "--format=%an <%ae>")); got != "Test <test@example.com>" {
		t.Errorf("author after the reset = %q", got)
	}
}
