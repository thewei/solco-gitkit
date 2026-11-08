package gitworkbench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A tree read at an old commit is that commit's tree: files added later are
// absent, directories sort first, sizes are blob sizes.
func TestListTreeReadsTheTreeAtACommit(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()

	first := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	writeRepoFile(t, root, "docs/Guide.md", "# guide\n")
	writeRepoFile(t, root, "docs/api/intro.md", "intro\n")
	writeRepoFile(t, root, "alpha.txt", "alpha\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-q", "-m", "more")

	atFirst, err := r.ListTree(ctx, root, first, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(atFirst) != 1 || atFirst[0].Name != "base.txt" || atFirst[0].Dir || atFirst[0].Size != int64(len("base line 1\nbase line 2\n")) {
		t.Fatalf("tree at the first commit = %+v, want just base.txt with its size", atFirst)
	}

	now, err := r.ListTree(ctx, root, "HEAD", "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range now {
		names = append(names, e.Name)
	}
	if got := strings.Join(names, ","); got != "docs,alpha.txt,base.txt" {
		t.Fatalf("root = %s, want directories first then names: docs,alpha.txt,base.txt", got)
	}
	if !now[0].Dir || now[0].Size != -1 || now[0].Path != "docs" {
		t.Fatalf("docs entry = %+v", now[0])
	}

	docs, err := r.ListTree(ctx, root, "HEAD", "docs")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0].Name != "api" || !docs[0].Dir || docs[0].Path != "docs/api" || docs[1].Path != "docs/Guide.md" {
		t.Fatalf("docs = %+v", docs)
	}

	missing, err := r.ListTree(ctx, root, first, "docs")
	if err != nil || len(missing) != 0 {
		t.Fatalf("a directory that did not exist yet = %+v, %v; want an empty list", missing, err)
	}
}

// ListTree refuses what is not a revision or leaves the tree before asking Git.
func TestListTreeRefusesOptionsAndEscapes(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	for _, rev := range []string{"", "--output=/tmp/x", "-1"} {
		if _, err := r.ListTree(ctx, root, rev, ""); err == nil {
			t.Fatalf("revision %q was accepted", rev)
		}
	}
	if _, err := r.ListTree(ctx, root, "HEAD", "../outside"); err == nil {
		t.Fatal("a path climbing out of the tree was accepted")
	}
}

// The file a tree entry names reads back through ReadBlob at that revision.
func TestListTreeEntryReadsBackThroughReadBlob(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	first := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	writeRepoFile(t, root, "base.txt", "rewritten\n")
	runGit(t, root, "commit", "-q", "-am", "rewrite")

	entries, err := r.ListTree(ctx, root, first, "")
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	data, err := r.ReadBlob(ctx, root, first+":"+entries[0].Path, 1<<20)
	if err != nil || string(data) != "base line 1\nbase line 2\n" {
		t.Fatalf("blob at the first commit = %q, %v", data, err)
	}
}

// FetchedAt follows FETCH_HEAD: absent before any fetch, then its mtime.
func TestFetchedAtFollowsFetchHead(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()

	if _, ok := r.FetchedAt(ctx, root); ok {
		t.Fatal("a repository that never fetched reports a fetch time")
	}
	when := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	head := filepath.Join(root, ".git", "FETCH_HEAD")
	if err := os.WriteFile(head, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(head, when, when); err != nil {
		t.Fatal(err)
	}
	got, ok := r.FetchedAt(ctx, root)
	if !ok || !got.Equal(when) {
		t.Fatalf("FetchedAt = %v, %v; want %v", got, ok, when)
	}
}

// A linked worktree writes its own FETCH_HEAD but shares the remote refs, so
// the newest of its own and the main repository's is the answer.
func TestFetchedAtInALinkedWorktree(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	older := time.Now().Add(-5 * time.Hour).Truncate(time.Second)
	newer := time.Now().Add(-90 * time.Minute).Truncate(time.Second)
	touch := func(path string, when time.Time) {
		t.Helper()
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	touch(filepath.Join(root, ".git", "FETCH_HEAD"), older)
	tree := filepath.Join(t.TempDir(), "wt")
	runGit(t, root, "worktree", "add", "-q", "-b", "side", tree)

	if got, ok := r.FetchedAt(ctx, tree); !ok || !got.Equal(older) {
		t.Fatalf("worktree that never fetched itself = %v, %v; want the main repository's %v", got, ok, older)
	}
	gitDir := strings.TrimSpace(runGit(t, tree, "rev-parse", "--git-dir"))
	touch(filepath.Join(gitDir, "FETCH_HEAD"), newer)
	if got, ok := r.FetchedAt(ctx, tree); !ok || !got.Equal(newer) {
		t.Fatalf("worktree that fetched later = %v, %v; want its own %v", got, ok, newer)
	}
	if got, ok := r.FetchedAt(ctx, root); !ok || !got.Equal(older) {
		t.Fatalf("main repository = %v, %v; want %v (the worktree's file is not its own)", got, ok, older)
	}
}
