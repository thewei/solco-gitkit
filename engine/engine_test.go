package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/commands/oscommands"
	"github.com/jesseduffield/lazygit/pkg/common"
	"github.com/jesseduffield/lazygit/pkg/config"
	"github.com/thewei/solco-gitkit/gitworkbench"
)

// git runs one Git command in dir against a hermetic configuration.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	return dir
}

func commit(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", name)
	git(t, dir, "commit", "-q", "-m", name)
}

// both returns the argv Runner and one served by the engine.
func both() (cli, lazy *gitworkbench.Runner) {
	return &gitworkbench.Runner{}, &gitworkbench.Runner{Backend: New()}
}

func TestListBranchesParity(t *testing.T) {
	cases := map[string]func(t *testing.T) string{
		"unborn HEAD": func(t *testing.T) string { return newRepo(t) },
		"one branch": func(t *testing.T) string {
			d := newRepo(t)
			commit(t, d, "a")
			return d
		},
		"several, not on the first": func(t *testing.T) string {
			d := newRepo(t)
			commit(t, d, "a")
			git(t, d, "branch", "zeta")
			git(t, d, "branch", "alpha/feature")
			git(t, d, "switch", "-q", "-c", "mid")
			commit(t, d, "b")
			return d
		},
		"tracking a remote": func(t *testing.T) string {
			d := newRepo(t)
			commit(t, d, "a")
			remote := filepath.Join(t.TempDir(), "remote.git")
			git(t, d, "init", "-q", "--bare", remote)
			git(t, d, "remote", "add", "origin", remote)
			git(t, d, "push", "-q", "-u", "origin", "main")
			git(t, d, "branch", "local-only")
			commit(t, d, "ahead")
			return d
		},
		"upstream gone": func(t *testing.T) string {
			d := newRepo(t)
			commit(t, d, "a")
			remote := filepath.Join(t.TempDir(), "remote.git")
			git(t, d, "init", "-q", "--bare", remote)
			git(t, d, "remote", "add", "origin", remote)
			git(t, d, "push", "-q", "-u", "origin", "main")
			git(t, d, "switch", "-q", "-c", "topic")
			git(t, d, "push", "-q", "-u", "origin", "topic")
			git(t, d, "switch", "-q", "main")
			git(t, d, "push", "-q", "origin", "--delete", "topic")
			git(t, d, "fetch", "-q", "--prune")
			return d
		},
		"detached HEAD": func(t *testing.T) string {
			d := newRepo(t)
			commit(t, d, "a")
			commit(t, d, "b")
			git(t, d, "checkout", "-q", "--detach", "HEAD~1")
			return d
		},
	}
	cli, lazy := both()
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			root := build(t)
			want, err := cli.ListBranches(context.Background(), root)
			if err != nil {
				t.Fatalf("argv path: %v", err)
			}
			got, err := lazy.ListBranches(context.Background(), root)
			if err != nil {
				t.Fatalf("engine path: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("engine and argv paths disagree\n engine: %+v\n   argv: %+v", got, want)
			}
		})
	}
}

func TestEngineIsTheBackendOfTheRunner(t *testing.T) {
	root := newRepo(t)
	commit(t, root, "a")
	e := New()
	if _, err := e.ListBranches(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.sessions) != 1 {
		t.Fatalf("a read must open exactly one session, got %d", len(e.sessions))
	}
	var _ gitworkbench.BranchBackend = e
}

func TestNotARepositoryIsAnError(t *testing.T) {
	_, err := New().ListBranches(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("a folder that is not a repository must fail")
	}
}

func TestCancelledContextStopsBeforeGit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New().ListBranches(ctx, newRepo(t))
	ge, ok := gitworkbench.GitError(err)
	if !ok || ge.Class != gitworkbench.ErrCancelled {
		t.Fatalf("want a cancelled *gitworkbench.Error, got %v", err)
	}
}

// builder is a command builder that runs through the policy runner.
func builder() *oscommands.CmdObjBuilder {
	cmn := common.NewDummyCommon()
	os := oscommands.NewOSCommand(cmn, config.NewDummyAppConfig(), oscommands.GetPlatform(),
		oscommands.NewNullGuiIO(cmn.Log))
	return os.Cmd.CloneWithNewRunner(func(oscommands.ICmdObjRunner) oscommands.ICmdObjRunner { return policyRunner{} })
}

func TestPolicyEnvironment(t *testing.T) {
	// A hostile parent environment: the policy must win over it.
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GIT_PAGER", "less")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	out, err := builder().New([]string{"sh", "-c", "echo $GIT_TERMINAL_PROMPT $GIT_PAGER $LC_ALL"}).RunWithOutput()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out); got != "0 cat C" {
		t.Fatalf("a command must never prompt or page: %q", got)
	}
}

// No editor of the user's can open; the rebase daemon (this very program) can.
func TestPolicyEditors(t *testing.T) {
	for _, k := range []string{"GIT_EDITOR", "GIT_SEQUENCE_EDITOR", "EDITOR", "VISUAL"} {
		t.Setenv(k, "vim")
	}
	const probe = "echo \"$GIT_EDITOR|$GIT_SEQUENCE_EDITOR|$EDITOR|$VISUAL\""
	out, err := builder().New([]string{"sh", "-c", probe}).RunWithOutput()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out); got != "true|||" {
		t.Fatalf("a foreign editor survived: %q", got)
	}

	own := oscommands.GetLazygitPath()
	out, err = builder().New([]string{"sh", "-c", probe}).
		AddEnvVars("GIT_EDITOR="+own, "GIT_SEQUENCE_EDITOR="+own).RunWithOutput()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(out), own+"|"+own+"||"; got != want {
		t.Fatalf("the daemon must stay the editor: %q, want %q", got, want)
	}
}

func TestPolicyDeadline(t *testing.T) {
	old := mutateTimeout
	mutateTimeout = 150 * time.Millisecond
	defer func() { mutateTimeout = old }()
	start := time.Now()
	err := builder().New([]string{"sh", "-c", "sleep 5"}).Run()
	ge, ok := gitworkbench.GitError(err)
	if !ok || ge.Class != gitworkbench.ErrTimeout {
		t.Fatalf("want a timeout *gitworkbench.Error, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("the deadline did not stop the command: %v", time.Since(start))
	}
}

func TestPolicyOutputBound(t *testing.T) {
	old := maxOutput
	maxOutput = 1024
	defer func() { maxOutput = old }()
	err := builder().New([]string{"sh", "-c", "head -c 100000 /dev/zero"}).Run()
	ge, ok := gitworkbench.GitError(err)
	if !ok || ge.Class != gitworkbench.ErrTooLarge {
		t.Fatalf("want a too-large *gitworkbench.Error, got %v", err)
	}
}

func TestPolicyClassifiesGitFailures(t *testing.T) {
	root := newRepo(t)
	s, err := newSession(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.git.Branch.CurrentBranchInfo()
	_ = err // an unborn HEAD may or may not fail; the next command must.
	_, _, err = builder().New([]string{"git", "-C", root, "rev-parse", "--verify", "--quiet", "nothing"}).RunWithOutputs()
	var ge *gitworkbench.Error
	if !errors.As(err, &ge) {
		t.Fatalf("a failing git must come back as *gitworkbench.Error, got %v", err)
	}
	if ge.Op != "rev-parse" || ge.OpClass != gitworkbench.OpRead || ge.ExitCode == 0 {
		t.Fatalf("unexpected error shape: %+v", ge)
	}
}

func TestSubcommand(t *testing.T) {
	for want, args := range map[string][]string{
		"log":          {"git", "log", "--oneline"},
		"for-each-ref": {"git", "--no-pager", "for-each-ref"},
		"status":       {"git", "-c", "core.quotepath=off", "-C", "/x", "--no-optional-locks", "status"},
		"":             {"git"},
	} {
		if got := subcommand(args); got != want {
			t.Errorf("subcommand(%v) = %q, want %q", args, got, want)
		}
	}
}

func TestPolicyRunnerFilesEveryCommandUnderTheSessionRoot(t *testing.T) {
	root := newRepo(t)
	log := gitworkbench.NewCommandLog()
	b := oscommands.NewOSCommand(common.NewDummyCommon(), config.NewDummyAppConfig(), oscommands.GetPlatform(),
		oscommands.NewNullGuiIO(common.NewDummyCommon().Log)).Cmd.
		CloneWithNewRunner(func(oscommands.ICmdObjRunner) oscommands.ICmdObjRunner { return policyRunner{log: log, root: root} })

	if err := b.New([]string{"git", "-C", root, "rev-parse", "--git-dir"}).Run(); err != nil {
		t.Fatal(err)
	}
	if err := b.New([]string{"git", "-C", root, "config", "user.name", "Somebody"}).Run(); err != nil {
		t.Fatal(err)
	}
	_ = b.New([]string{"git", "-C", root, "rev-parse", "--verify", "--quiet", "nothing"}).Run()

	got := log.Entries(root, true)
	if len(got) != 3 {
		t.Fatalf("entries = %+v, want three", got)
	}
	if got[0].Class != gitworkbench.OpRead || !got[0].Failed() || got[0].ExitCode != 1 {
		t.Errorf("newest = %+v, want the failed read", got[0])
	}
	if got[1].Class != gitworkbench.OpMutate || !strings.Contains(got[1].Line(), "config user.name Somebody") || got[1].Failed() {
		t.Errorf("middle = %+v (%s), want the tag write", got[1], got[1].Line())
	}
	if strings.HasPrefix(got[1].Args[0], "git") || got[2].Args[0] != "-C" {
		t.Errorf("args must be argv after the executable, got %v", got[2].Args)
	}
}

func TestReadFormsOfMixedSubcommandsAreReads(t *testing.T) {
	cases := []struct {
		args []string
		read bool
	}{
		{[]string{"git", "worktree", "list", "--porcelain"}, true},
		{[]string{"git", "stash", "list", "-z"}, true},
		{[]string{"git", "tag", "--list", "-n"}, true},
		{[]string{"git", "config", "--local", "--get-regexp", "^branch"}, true},
		{[]string{"git", "-C", "/r", "config", "--get", "user.name"}, true},
		{[]string{"git", "stash", "push", "-m", "list"}, false}, // a message that says "list" is still a write
		{[]string{"git", "worktree", "add", "list"}, false},
		{[]string{"git", "tag", "v1", "--list"}, false},
		{[]string{"git", "config", "--unset", "x"}, false},
		{[]string{"git", "commit", "-m", "x"}, false},
		{[]string{"git", "status"}, true},
		{[]string{"git", "stash"}, false},
	}
	for _, c := range cases {
		if got := isRead(c.args); got != c.read {
			t.Errorf("isRead(%v) = %v, want %v", c.args, got, c.read)
		}
	}
}
