package engine

import (
	"context"

	"github.com/jesseduffield/lazygit/pkg/commands/git_commands"
	"github.com/thewei/solco-gitkit/gitworkbench"
)

/**
 * [INPUT]: gitworkbench.WriteBackend 的合约（Runner 已校验参数、已加锁）、LazyGit 的 BranchCommands / StashCommands / TagCommands
 * [OUTPUT]: 分支切换/创建/改名/删除、stash 推入/弹出/应用/丢弃、标签创建/删除
 * [POS]: engine 里写入组的适配；失败经 policyRunner 变成已分类的 *gitworkbench.Error，与 argv 路径同类
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

var _ gitworkbench.WriteBackend = (*Engine)(nil)

// write is the common frame of one write: not cancelled, a session, then run.
func (e *Engine) write(ctx context.Context, root, op string, run func(*session) error) error {
	if err := ctx.Err(); err != nil {
		return gitworkbench.NewError(gitworkbench.ErrCancelled, gitworkbench.OpMutate, op, 0, "", err)
	}
	s, err := e.session(root)
	if err != nil {
		return err
	}
	return run(s)
}

func (e *Engine) SwitchBranch(ctx context.Context, root, name string) error {
	return e.write(ctx, root, "switch", func(s *session) error {
		return s.git.Branch.Checkout(name, git_commands.CheckoutOptions{})
	})
}

func (e *Engine) CreateBranch(ctx context.Context, root, name, base string) error {
	return e.write(ctx, root, "switch", func(s *session) error { return s.git.Branch.New(name, base) })
}

func (e *Engine) RenameBranch(ctx context.Context, root, name, to string) error {
	return e.write(ctx, root, "branch", func(s *session) error { return s.git.Branch.Rename(name, to) })
}

func (e *Engine) DeleteBranch(ctx context.Context, root, name string) error {
	return e.write(ctx, root, "branch", func(s *session) error {
		return s.git.Branch.LocalDelete([]string{name}, false)
	})
}

func (e *Engine) StashPush(ctx context.Context, root, message string) error {
	return e.write(ctx, root, "stash", func(s *session) error {
		return s.git.Stash.StashIncludeUntrackedChanges(message)
	})
}

func (e *Engine) StashPop(ctx context.Context, root string, index int) error {
	return e.write(ctx, root, "stash", func(s *session) error { return s.git.Stash.Pop(index) })
}

func (e *Engine) StashApply(ctx context.Context, root string, index int) error {
	return e.write(ctx, root, "stash", func(s *session) error { return s.git.Stash.Apply(index) })
}

func (e *Engine) StashDrop(ctx context.Context, root string, index int) error {
	return e.write(ctx, root, "stash", func(s *session) error { return s.git.Stash.Drop(index) })
}

func (e *Engine) CreateTag(ctx context.Context, root, name, ref string) error {
	return e.write(ctx, root, "tag", func(s *session) error {
		return s.git.Tag.CreateLightweightObj(name, ref, false).Run()
	})
}

func (e *Engine) DeleteTag(ctx context.Context, root, name string) error {
	return e.write(ctx, root, "tag", func(s *session) error { return s.git.Tag.LocalDelete([]string{name}) })
}
