// Package engine is the LazyGit-backed implementation of some of
// gitworkbench.Runner's capabilities (2026-11-12 decision): LazyGit's command
// layer (pkg/commands) with gitworkbench's runner policy laid over it.
//
// Hard rules:
//   - this is the only package of the family that imports lazygit, and no
//     lazygit type leaves it: every result is a gitworkbench model;
//   - every subprocess goes through policyRunner (policy.go), never through
//     LazyGit's own runner;
//   - LazyGit's user configuration is not read: the defaults of
//     config.NewDummyAppConfig apply, so a user's lazygit config cannot
//     change what Solco Git does;
//   - a capability lands here only when the same real-repository test passes
//     on this path and on the argv path.
package engine

import (
	"fmt"
	"sync"

	"github.com/jesseduffield/lazygit/pkg/commands"
	"github.com/jesseduffield/lazygit/pkg/commands/git_commands"
	"github.com/jesseduffield/lazygit/pkg/commands/git_config"
	"github.com/jesseduffield/lazygit/pkg/commands/oscommands"
	"github.com/jesseduffield/lazygit/pkg/common"
	"github.com/jesseduffield/lazygit/pkg/config"
	"github.com/thewei/solco-gitkit/gitworkbench"
)

// Engine serves gitworkbench.Backend capabilities from LazyGit's command
// layer. One Engine holds one session per repository root.
type Engine struct {
	mu       sync.Mutex
	sessions map[string]*session
}

// New returns an Engine. Install it with `runner.Backend = engine.New()`.
func New() *Engine { return &Engine{sessions: map[string]*session{}} }

var _ gitworkbench.Backend = (*Engine)(nil)

// Name implements gitworkbench.Backend.
func (*Engine) Name() string { return "lazygit" }

// session is LazyGit's command layer pinned to one repository.
type session struct {
	git  *commands.GitCommand
	main *git_commands.MainBranches
	// mainWorktree is the main work tree of the repository the session's
	// root belongs to (root itself unless it is a linked work tree).
	mainWorktree string
}

func (e *Engine) session(root string) (*session, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if s, ok := e.sessions[root]; ok {
		return s, nil
	}
	s, err := newSession(root)
	if err != nil {
		return nil, err
	}
	e.sessions[root] = s
	return s, nil
}

// newSession builds the command layer for root without touching the process's
// working directory (the library's own constructor changes it, and several
// repositories are open at once): every command is pinned to root by the
// builder the layer is given.
func newSession(root string) (*session, error) {
	cmn := common.NewDummyCommon()
	appConfig := config.NewDummyAppConfig()
	osCommand := oscommands.NewOSCommand(cmn, appConfig, oscommands.GetPlatform(),
		oscommands.NewNullGuiIO(cmn.Log))
	osCommand.Cmd = osCommand.Cmd.CloneWithNewRunner(func(oscommands.ICmdObjRunner) oscommands.ICmdObjRunner {
		return policyRunner{}
	})
	version, err := git_commands.GetGitVersion(osCommand)
	if err != nil {
		return nil, fmt.Errorf("read Git version: %w", err)
	}
	paths, err := git_commands.GetRepoPathsForDir(root, osCommand.Cmd)
	if err != nil {
		return nil, err
	}
	if paths.IsBareRepo() {
		return nil, fmt.Errorf("%s", cmn.Tr.BareRepoNotSupported)
	}
	gitConfig := git_config.NewStdCachedGitConfig(cmn.Log)
	gitConfig.SetDir(paths.WorktreePath())
	diffConfig := config.NewDiffRendererConfigManager(cmn.UserConfig)
	g := commands.NewGitCommandAux(cmn, version, osCommand, gitConfig, paths, diffConfig)
	return &session{git: g, main: git_commands.NewMainBranches(cmn, osCommand.Cmd), mainWorktree: paths.RepoPath()}, nil
}
