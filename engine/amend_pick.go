package engine

import (
	"context"

	"github.com/jesseduffield/lazygit/pkg/commands/models"
	"github.com/jesseduffield/lazygit/pkg/utils"
	"github.com/thewei/solco-gitkit/gitworkbench"
)

/**
 * [INPUT]: gitworkbench.AmendBackend / CherryPickBackend 的合约（Runner 已校验、已加锁）、LazyGit 的 RebaseCommands.AmendTo / CherryPickCommits
 * [OUTPUT]: Engine.AmendTo（暂存改动并入更早的提交）与 Engine.CherryPickCommits（按序摘取一串提交到 HEAD）
 * [POS]: engine 里 LazyGit 独有历史能力的适配；AmendTo 经 LazyGit 的 fixup 提交 + 守护进程移动，所以宿主必须先调 RunDaemon
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

var (
	_ gitworkbench.AmendBackend      = (*Engine)(nil)
	_ gitworkbench.CherryPickBackend = (*Engine)(nil)
)

// AmendTo amends the commit with the staged changes through LazyGit.
func (e *Engine) AmendTo(ctx context.Context, root, commit string) error {
	if err := cancelled(ctx, "rebase"); err != nil {
		return err
	}
	s, err := e.session(root)
	if err != nil {
		return err
	}
	commits, truncated, err := s.loadCommits()
	if err != nil {
		return err
	}
	i := indexOf(commits, commit)
	if i < 0 {
		return historyRefusal("the commit is not on this branch (or not among its latest %d)", historyWindow)
	}
	// The rebase replays everything above the commit, and the fixup commit
	// LazyGit makes sits on top: the commit and its parent must be in view.
	reach := i + 2
	if truncated && reach >= len(commits) {
		return historyRefusal("that commit is too far down the branch for one rewrite; choose a newer one")
	}
	if err := refuseMerges(commits, reach); err != nil {
		return err
	}
	return s.git.Rebase.AmendTo(commits, i)
}

// CherryPickCommits applies the commits to HEAD, oldest first. LazyGit takes
// newest first and reverses; the commits are only named, so a stub commit
// with the hash is enough (the Runner has already refused merges).
func (e *Engine) CherryPickCommits(ctx context.Context, root string, hashes []string) error {
	if err := cancelled(ctx, "cherry-pick"); err != nil {
		return err
	}
	s, err := e.session(root)
	if err != nil {
		return err
	}
	pool := &utils.StringPool{}
	commits := make([]*models.Commit, len(hashes))
	for i, h := range hashes {
		commits[len(hashes)-1-i] = models.NewCommit(pool, models.NewCommitOpts{Hash: h})
	}
	return s.git.Rebase.CherryPickCommits(commits)
}
