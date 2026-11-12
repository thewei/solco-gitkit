package engine

import (
	"context"

	"github.com/thewei/solco-gitkit/gitworkbench"
)

/**
 * [INPUT]: LazyGit 的 TagLoader / WorktreeLoader（经 session 与 policyRunner）
 * [OUTPUT]: ListTags、ListWorktrees——与 argv 路径逐项一致的 gitworkbench 模型
 * [POS]: engine 里已对拍通过的两个读取组；其余读取组为何不迁见 CLAUDE.md
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

var (
	_ gitworkbench.TagBackend      = (*Engine)(nil)
	_ gitworkbench.WorktreeBackend = (*Engine)(nil)
)

// maxTags bounds a tag list like the argv path does.
const maxTags = 500

func cancelled(ctx context.Context, sub string) error {
	if err := ctx.Err(); err != nil {
		return gitworkbench.NewError(gitworkbench.ErrCancelled, gitworkbench.OpRead, sub, 0, "", err)
	}
	return nil
}

// ListTags reads the tags newest creation first through LazyGit's TagLoader.
func (e *Engine) ListTags(ctx context.Context, root string) ([]gitworkbench.Tag, error) {
	if err := cancelled(ctx, "tag"); err != nil {
		return nil, err
	}
	s, err := e.session(root)
	if err != nil {
		return nil, err
	}
	loaded, err := s.git.Loaders.TagLoader.GetTags()
	if err != nil {
		return nil, err
	}
	var tags []gitworkbench.Tag
	for _, t := range loaded {
		tags = append(tags, gitworkbench.Tag{Name: t.Name})
		if len(tags) >= maxTags {
			break
		}
	}
	return tags, nil
}

// ListWorktrees reads the work trees through LazyGit's WorktreeLoader. The
// loader moves the work tree it is pinned to to the top of the list, so it is
// asked through the main work tree's session: Git's own order (main first,
// then the linked ones as Git lists them) comes out whichever work tree the
// caller is in. A work tree that is mid-rebase has its branch named by LazyGit;
// the argv path calls it detached, so that is put back.
func (e *Engine) ListWorktrees(ctx context.Context, root string) ([]gitworkbench.Worktree, error) {
	if err := cancelled(ctx, "worktree"); err != nil {
		return nil, err
	}
	s, err := e.session(root)
	if err != nil {
		return nil, err
	}
	if s.mainWorktree != "" && s.mainWorktree != root {
		if ms, err := e.session(s.mainWorktree); err == nil {
			s = ms
		}
	}
	loaded, err := s.git.Loaders.Worktrees.GetWorktrees()
	if err != nil {
		return nil, err
	}
	out := make([]gitworkbench.Worktree, 0, len(loaded))
	for _, w := range loaded {
		wt := gitworkbench.Worktree{Path: w.Path, Head: w.Head, Branch: w.Branch}
		if w.IsRebasingOrBisecting || w.Branch == "" {
			wt.Branch, wt.Detached = "", true
		}
		out = append(out, wt)
	}
	return out, nil
}
