package engine

import (
	"context"
	"reflect"
	"testing"
)

// The stash list through LazyGit's loader plus the batched bases is the list
// the argv path reads: ref, stash commit, base commit and subject, newest first.
func TestStashListParity(t *testing.T) {
	root := newRepo(t)
	commit(t, root, "c1")
	cli, lazy := both()
	ctx := context.Background()

	empty, err := lazy.StashList(ctx, root)
	if err != nil || len(empty) != 0 {
		t.Fatalf("no stash: %v %v", empty, err)
	}

	writeFile(t, root, "c1", "one\n")
	git(t, root, "stash", "push", "-q", "-m", "plain")
	commit(t, root, "c2") // the next stash has another base
	writeFile(t, root, "c1", "two\n")
	writeFile(t, root, "untracked", "u\n")
	git(t, root, "stash", "push", "-q", "-u", "-m", "with | a bar, and ünïcode")
	writeFile(t, root, "c1", "three\n")
	git(t, root, "stash", "push", "-q") // WIP on main: …

	want, err := cli.StashList(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := lazy.StashList(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 3 {
		t.Fatalf("argv list: %+v", want)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("lists differ:\nargv   %+v\nengine %+v", want, got)
	}
	if got[0].Base == got[2].Base || got[0].Base == "" {
		t.Fatalf("bases must be filled and tell the stashes apart: %+v", got)
	}
}
