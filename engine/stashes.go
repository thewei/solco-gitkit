package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/jesseduffield/lazygit/pkg/commands/git_commands"
	"github.com/thewei/solco-gitkit/gitworkbench"
)

/**
 * [INPUT]: LazyGit 的 StashLoader（hash、主题）、一次批量 `rev-parse <hash>^`（LazyGit 的条目没有基底提交）
 * [OUTPUT]: StashList——与 argv 路径逐项一致的 gitworkbench.Stash
 * [POS]: engine 里读取组 A 的 stash：LazyGit 供条目，gitworkbench 的模型缺的基底提交在这里补齐
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

var _ gitworkbench.StashBackend = (*Engine)(nil)

// StashList lists the entries newest first. LazyGit's loader knows the stash
// commit and its message but not the commit the stash was made on, so the
// bases are asked in one batch: `rev-parse <stash>^ …`. The loader reports
// no error of its own (a failing `git stash list` reads as an empty list);
// the session has already proved the directory is a repository.
func (e *Engine) StashList(ctx context.Context, root string) ([]gitworkbench.Stash, error) {
	if err := cancelled(ctx, "stash"); err != nil {
		return nil, err
	}
	s, err := e.session(root)
	if err != nil {
		return nil, err
	}
	entries := s.git.Loaders.StashLoader.GetStashEntries("")
	var withHash []int
	args := git_commands.NewGitCmd("rev-parse")
	for i, en := range entries {
		if en.Hash != "" {
			withHash = append(withHash, i)
			args.Arg(en.Hash + "^")
		}
	}
	bases := map[int]string{}
	if len(withHash) > 0 {
		out, err := s.cmd.New(args.ToArgv()).DontLog().RunWithOutput()
		if err != nil {
			return nil, err
		}
		lines := strings.Fields(out)
		if len(lines) != len(withHash) {
			return nil, fmt.Errorf("stash bases: asked for %d, got %d", len(withHash), len(lines))
		}
		for n, i := range withHash {
			bases[i] = lines[n]
		}
	}
	stashes := make([]gitworkbench.Stash, 0, len(entries))
	for i, en := range entries {
		stashes = append(stashes, gitworkbench.Stash{
			Ref: fmt.Sprintf("stash@{%d}", en.Index), Hash: en.Hash, Base: bases[i], Subject: en.Name,
		})
	}
	return stashes, nil
}
