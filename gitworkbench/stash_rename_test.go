package gitworkbench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStashRenameKeepsTheContentAndReplacesTheMessage(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "first\n")
	if err := r.StashPush(ctx, root, "older"); err != nil {
		t.Fatal(err)
	}
	write("b.txt", "second\n")
	if err := r.StashPush(ctx, root, "newer"); err != nil {
		t.Fatal(err)
	}
	list, err := r.StashList(ctx, root)
	if err != nil || len(list) != 2 {
		t.Fatalf("stashes = %v, %v", list, err)
	}
	older := list[1] // stash@{1}
	if err := r.StashRename(ctx, root, older.Hash, "renamed older"); err != nil {
		t.Fatal(err)
	}
	after, err := r.StashList(ctx, root)
	if err != nil || len(after) != 2 {
		t.Fatalf("a rename must neither add nor lose a stash: %v, %v", after, err)
	}
	if !strings.Contains(after[0].Subject, "renamed older") || after[0].Hash == older.Hash {
		t.Fatalf("the renamed stash should lead the list as a new commit: %+v", after[0])
	}
	if !strings.Contains(after[1].Subject, "newer") {
		t.Fatalf("the other stash is untouched: %+v", after[1])
	}
	// The newest entry renames too (storing it unchanged would be a no-op).
	if err := r.StashRename(ctx, root, after[0].Hash, "again"); err != nil {
		t.Fatal(err)
	}
	if l, _ := r.StashList(ctx, root); len(l) != 2 || !strings.Contains(l[0].Subject, "again") {
		t.Fatalf("top rename: %+v", l)
	}
	if err := r.StashRename(ctx, root, older.Hash, "   "); err == nil {
		t.Fatal("an empty message must be refused")
	}
	if err := r.StashRename(ctx, root, strings.Repeat("0", 40), "x"); err == nil {
		t.Fatal("an unknown stash must be refused")
	}
	if err := r.StashRename(ctx, root, "stash@{0}", "x"); err == nil {
		t.Fatal("only a full hash identifies a stash")
	}
}
