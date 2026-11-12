package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thewei/solco-gitkit/gitworkbench"
)

func TestListTagsParity(t *testing.T) {
	cases := map[string]func(t *testing.T, root string){
		"no commits": func(*testing.T, string) {},
		"no tags":    func(t *testing.T, root string) { commit(t, root, "a") },
		"light and annotated, newest first": func(t *testing.T, root string) {
			commit(t, root, "a")
			git(t, root, "tag", "v1")
			commit(t, root, "b")
			git(t, root, "tag", "-a", "v2", "-m", "second release\n\nbody")
			commit(t, root, "c")
			git(t, root, "tag", "v1.5")
		},
		"slashes and dots": func(t *testing.T, root string) {
			commit(t, root, "a")
			git(t, root, "tag", "rel/1.0.0")
			git(t, root, "tag", "rel/1.0.1-rc.1")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			root := newRepo(t)
			setup(t, root)
			cli, lazy := both()
			want, err := cli.ListTags(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			got, err := lazy.ListTags(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("engine %+v, argv %+v", got, want)
			}
		})
	}
}

func TestListTagsBound(t *testing.T) {
	root := newRepo(t)
	commit(t, root, "a")
	for i := 0; i < 520; i++ {
		git(t, root, "update-ref", "refs/tags/t"+string(rune('a'+i/26/26%26))+string(rune('a'+i/26%26))+string(rune('a'+i%26)), "HEAD")
	}
	cli, lazy := both()
	want, _ := cli.ListTags(context.Background(), root)
	got, err := lazy.ListTags(context.Background(), root)
	if err != nil || len(got) != 500 || !reflect.DeepEqual(got, want) {
		t.Fatalf("len=%d err=%v (argv %d)", len(got), err, len(want))
	}
}

func TestListWorktreesParity(t *testing.T) {
	setup := func(t *testing.T) (main, linked string) {
		main = newRepo(t)
		commit(t, main, "a")
		commit(t, main, "b")
		parent, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		linked = filepath.Join(parent, "feature")
		git(t, main, "worktree", "add", "-q", "-b", "feature", linked)
		detached := filepath.Join(parent, "detached")
		git(t, main, "worktree", "add", "-q", "--detach", detached, "HEAD~1")
		return main, linked
	}
	main, linked := setup(t)
	for name, root := range map[string]string{"asked from the main work tree": main, "asked from a linked work tree": linked} {
		t.Run(name, func(t *testing.T) {
			cli, lazy := both()
			want, err := cli.ListWorktrees(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			got, err := lazy.ListWorktrees(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			if len(want) != 3 || !reflect.DeepEqual(got, want) {
				t.Fatalf("engine %+v\nargv   %+v", got, want)
			}
		})
	}

	t.Run("a work tree stopped in a rebase is detached", func(t *testing.T) {
		main, linked := setup(t)
		for _, n := range []string{"x", "y"} {
			if err := os.WriteFile(filepath.Join(linked, n), []byte(n), 0o644); err != nil {
				t.Fatal(err)
			}
			git(t, linked, "add", n)
			git(t, linked, "commit", "-q", "-m", n)
		}
		stopRebase(t, linked)
		cli, lazy := both()
		want, _ := cli.ListWorktrees(context.Background(), main)
		got, err := lazy.ListWorktrees(context.Background(), main)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("engine %+v\nargv   %+v", got, want)
		}
		var found bool
		for _, w := range got {
			if w.Path == linked {
				found = true
				if !w.Detached || w.Branch != "" {
					t.Fatalf("a rebasing work tree must read as detached: %+v", w)
				}
			}
		}
		if !found {
			t.Fatal("linked work tree missing")
		}
	})
}

// stopRebase leaves dir in the middle of an interactive rebase (a `break`).
func stopRebase(t *testing.T, dir string) {
	t.Helper()
	// the sequence editor puts `break` first and keeps the picks
	script := filepath.Join(t.TempDir(), "editor.sh")
	body := "#!/bin/sh\nprintf 'break\\n' | cat - \"$1\" > \"$1.new\" && mv \"$1.new\" \"$1\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "rebase", "-i", "HEAD~1")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com",
		"GIT_SEQUENCE_EDITOR="+script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rebase: %v\n%s", err, out)
	}
}

// Remotes are the read group that fails the parity bar, so they stay on the
// argv path: LazyGit's loader reads the raw `remote.<name>.url` of the local
// config (no `url.<base>.insteadOf`, no remote that only the global config
// defines) and puts origin first, where Git lists remotes by name.
func TestRemotesDoNotReachParity(t *testing.T) {
	root := newRepo(t)
	commit(t, root, "a")
	git(t, root, "remote", "add", "upstream", "gh:me/repo.git")
	git(t, root, "remote", "add", "origin", "gh:me/fork.git")
	git(t, root, "config", "url.https://github.com/.insteadOf", "gh:")

	cli, _ := both()
	argv, err := cli.ListRemotes(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New().session(root)
	if err != nil {
		t.Fatal(err)
	}
	lazy := s.git.Loaders.RemoteLoader.GetRemotes()

	if len(argv) != 2 || len(lazy) != 2 {
		t.Fatalf("argv %+v, lazygit %+v", argv, lazy)
	}
	if argv[0].FetchURL != "https://github.com/me/fork.git" {
		t.Fatalf("the argv path applies insteadOf: %q", argv[0].FetchURL)
	}
	if got := lazy[0].Urls[0]; got != "gh:me/fork.git" {
		t.Fatalf("LazyGit reads the raw URL: %q", got)
	}
}

// A group is moved to the engine only with its own parity test: nothing else
// may satisfy these interfaces by accident.
func TestUnmigratedGroupsStayOnArgv(t *testing.T) {
	var e any = New()
	if _, ok := e.(interface {
		ListRemotes(context.Context, string) ([]gitworkbench.Remote, error)
	}); ok {
		t.Fatal("ListRemotes must stay on the argv path (see TestRemotesDoNotReachParity)")
	}
	if _, ok := e.(interface {
		ListSubmodules(context.Context, string) ([]gitworkbench.Submodule, error)
	}); ok {
		t.Fatal("ListSubmodules must stay on the argv path: LazyGit reads .gitmodules, not `submodule status`")
	}
}
