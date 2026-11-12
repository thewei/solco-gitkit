package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/jesseduffield/lazygit/pkg/commands/oscommands"
	"github.com/thewei/solco-gitkit/gitworkbench"
)

/**
 * [INPUT]: LazyGit 命令层构造出的 *oscommands.CmdObj（argv、目录、环境、stdin）
 * [OUTPUT]: 在 gitworkbench 的运行策略下执行：读 5 s / 写 30 s、输出上限、无提示无分页、失败一律 *gitworkbench.Error
 * [POS]: engine 里唯一启动 git 子进程的地方；LazyGit 自带的 runner（无超时、会提示凭据、流式写日志）被它整体替换
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// readSubcommands are the Git subcommands that never change the repository.
// Everything not listed is treated as a mutation, which only means a longer
// deadline and no GIT_OPTIONAL_LOCKS=0: the safe side of a wrong guess.
var readSubcommands = map[string]bool{
	"for-each-ref": true, "log": true, "rev-parse": true, "rev-list": true,
	"status": true, "diff": true, "show": true, "cat-file": true,
	"ls-files": true, "ls-tree": true, "symbolic-ref": true, "merge-base": true,
	"describe": true, "check-ref-format": true, "blame": true, "version": true,
	"name-rev": true, "show-ref": true, "diff-tree": true, "reflog": true,
}

// readForms are the subcommands that both read and write, with the form that
// only reads: `worktree list`, `stash list`, `tag --list`, `config --get…`.
// Anything else of these stays a mutation.
var readForms = map[string][]string{
	"worktree": {"list"},
	"stash":    {"list"},
	"tag":      {"--list", "-l"},
	"config":   {"--get", "--get-all", "--get-regexp", "--list", "-l"},
}

// isRead reports whether argv (executable first) only reads the repository.
func isRead(args []string) bool {
	sub := subcommand(args)
	if readSubcommands[sub] {
		return true
	}
	forms, ok := readForms[sub]
	if !ok {
		return false
	}
	// The read form must be the first word after the subcommand (a config
	// scope may come before it): `stash push -m list` is not `stash list`.
	for _, a := range args[slices.Index(args, sub)+1:] {
		if slices.Contains(configScopes, a) {
			continue
		}
		return slices.Contains(forms, a)
	}
	return false
}

var configScopes = []string{"--local", "--global", "--system", "--worktree"}

// subcommand is the first word of argv that is not an option of git itself.
// args[0] is the executable. `-c key=value` and `-C dir` take the next word.
func subcommand(args []string) string {
	for i := 1; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-c" || a == "-C":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

// The policy bounds; variables only so a test can shorten them.
var (
	readTimeout   = gitworkbench.ReadTimeout
	mutateTimeout = gitworkbench.MutateTimeout
	maxOutput     = int64(gitworkbench.MaxReadOutput)
)

func readEnv() []string {
	return []string{"GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}
}

func mutateEnv() []string {
	return []string{"GIT_EDITOR=true", "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}
}

// editorKeys are the variables Git consults to open an editor.
var editorKeys = []string{"GIT_EDITOR", "GIT_SEQUENCE_EDITOR", "EDITOR", "VISUAL"}

// withoutForeignEditors drops every editor variable that does not name this
// very program (the rebase daemon, RunDaemon): an editor from the user's
// environment must never open, and GIT_EDITOR=true (mutateEnv) must not
// replace the daemon either. A command that did not ask for the daemon
// therefore ends with no editor of its own, only `true`.
func withoutForeignEditors(env []string) []string {
	own := oscommands.GetLazygitPath()
	out := env[:0:0]
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		if slices.Contains(editorKeys, key) && value != own {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// hasEditor reports whether env sets key.
func hasEditor(env []string, key string) bool {
	return slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, key+"=") })
}

// policyRunner is an oscommands.ICmdObjRunner that applies gitworkbench's
// policy. It ignores LazyGit's streaming, pty and credential strategies on
// purpose: a command that would ask for a credential fails at once
// (GIT_TERMINAL_PROMPT=0) instead of waiting for a person.
type policyRunner struct {
	log  *gitworkbench.CommandLog // nil: nothing is recorded
	root string                   // the repository root the log files the commands under
}

var _ oscommands.ICmdObjRunner = policyRunner{}

func (p policyRunner) Run(c *oscommands.CmdObj) error {
	_, stderr, err := p.execute(c)
	return sanitised(stderr, err)
}

func (p policyRunner) RunWithOutput(c *oscommands.CmdObj) (string, error) {
	stdout, stderr, err := p.execute(c)
	// LazyGit's own RunWithOutput returns stdout and stderr together.
	combined := stdout + stderr
	return combined, sanitised(combined, err)
}

func (p policyRunner) RunWithOutputs(c *oscommands.CmdObj) (string, string, error) {
	stdout, stderr, err := p.execute(c)
	return stdout, stderr, sanitised(stderr, err)
}

func (p policyRunner) RunAndProcessLines(c *oscommands.CmdObj, onLine func(string) (bool, error)) error {
	stdout, stderr, err := p.execute(c)
	if err != nil {
		return sanitised(stderr, err)
	}
	for line := range strings.Lines(stdout) {
		stop, err := onLine(strings.TrimRight(line, "\r\n"))
		if err != nil {
			return err
		}
		if stop {
			break
		}
	}
	return nil
}

// sanitised keeps a *gitworkbench.Error intact (callers branch on its class)
// and turns anything else into the usual error.
func sanitised(detail string, err error) error {
	if err == nil {
		return nil
	}
	var ge *gitworkbench.Error
	if errors.As(err, &ge) {
		return err
	}
	if detail == "" {
		return err
	}
	return errors.New(detail)
}

// execute runs the command once, under the policy, with its own deadline.
func (p policyRunner) execute(c *oscommands.CmdObj) (stdout, stderr string, err error) {
	if m := c.Mutex(); m != nil {
		m.Lock()
		defer m.Unlock()
	}
	src := c.GetCmd()
	sub := subcommand(src.Args)

	class, timeout, env := gitworkbench.OpMutate, mutateTimeout, mutateEnv()
	if isRead(src.Args) {
		class, timeout, env = gitworkbench.OpRead, readTimeout, readEnv()
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	defer func() { p.record(src.Args, class, start, err) }()

	cmd := exec.CommandContext(ctx, src.Path, src.Args[1:]...)
	cmd.Dir = src.Dir
	base := src.Env
	if base == nil {
		base = os.Environ()
	}
	base = withoutForeignEditors(base)
	if class == gitworkbench.OpMutate && hasEditor(base, "GIT_EDITOR") {
		// the daemon is the editor: keep it, add only the rest of the policy
		env = slices.DeleteFunc(slices.Clone(env), func(kv string) bool { return strings.HasPrefix(kv, "GIT_EDITOR=") })
	}
	cmd.Env = append(append([]string(nil), base...), env...)
	cmd.Stdin = src.Stdin
	// A killed Git can leave pipe-holding children (hooks, pagers) behind;
	// without WaitDelay the deadline could stall forever.
	cmd.WaitDelay = 500 * time.Millisecond

	var out, errb bytes.Buffer
	outLimit := &limitedWriter{buf: &out, limit: maxOutput}
	cmd.Stdout = outLimit
	cmd.Stderr = &limitedWriter{buf: &errb, limit: 64 << 10}
	runErr := cmd.Run()

	if outLimit.overflow {
		return "", "", gitworkbench.NewError(gitworkbench.ErrTooLarge, class, sub, 0, "",
			errors.New("command output exceeded size limit"))
	}
	if runErr == nil {
		return out.String(), errb.String(), nil
	}
	exit := 0
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		exit = ee.ExitCode()
	}
	var gc gitworkbench.ErrClass
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		gc = gitworkbench.ErrTimeout
	case errors.Is(ctx.Err(), context.Canceled):
		gc = gitworkbench.ErrCancelled
	case errors.Is(runErr, exec.ErrNotFound):
		gc = gitworkbench.ErrGitMissing
	default:
		gc = gitworkbench.ClassifyStderr(errb.String())
	}
	return out.String(), errb.String(), gitworkbench.NewError(gc, class, sub, exit, errb.String(), runErr)
}

// limitedWriter keeps at most limit bytes and reports whether it had to drop
// any, so a runaway command is cut off instead of filling memory.
type limitedWriter struct {
	buf      *bytes.Buffer
	limit    int64
	overflow bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if room := w.limit - int64(w.buf.Len()); int64(len(p)) > room {
		if room > 0 {
			w.buf.Write(p[:room])
		}
		w.overflow = true
		return len(p), nil
	}
	return w.buf.Write(p)
}

// record files one finished command in the log, if there is one.
func (p policyRunner) record(args []string, class gitworkbench.OpClass, start time.Time, err error) {
	if p.log == nil || len(args) == 0 {
		return
	}
	e := gitworkbench.CommandEntry{Root: p.root, Args: args[1:], Class: class, Duration: time.Since(start)}
	if err != nil {
		e.Failure, e.ExitCode = gitworkbench.ErrOther, -1
		if ge, ok := gitworkbench.GitError(err); ok {
			e.Failure, e.ExitCode = ge.Class, ge.ExitCode
		}
	}
	p.log.Record(e)
}
