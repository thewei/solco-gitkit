package engine

import (
	"context"
	"slices"
	"strings"

	"github.com/jesseduffield/lazygit/pkg/commands/models"
	"github.com/thewei/solco-gitkit/gitworkbench"
)

var _ gitworkbench.BranchBackend = (*Engine)(nil)

// ListBranches reads the local branches through LazyGit's BranchLoader and
// shapes them like the argv path does: sorted by name, no detached-HEAD
// pseudo-branch and no unborn branch, the upstream as "remote/branch", the head as a 7-character
// short hash.
func (e *Engine) ListBranches(ctx context.Context, root string) ([]gitworkbench.Branch, error) {
	if err := ctx.Err(); err != nil {
		return nil, gitworkbench.NewError(gitworkbench.ErrCancelled, gitworkbench.OpRead, "for-each-ref", 0, "", err)
	}
	s, err := e.session(root)
	if err != nil {
		return nil, err
	}
	loaded, err := s.git.Loaders.BranchLoader.Load(nil, s.main, nil, false,
		func(work func() error) { _ = work() }, func() {})
	if err != nil {
		return nil, err
	}
	var out []gitworkbench.Branch
	for _, b := range loaded {
		// A detached HEAD is a pseudo-branch, and a branch checked out in a
		// repository with no commit yet has no ref: neither is in refs/heads.
		if b.DetachedHead || b.CommitHash == "" {
			continue
		}
		out = append(out, toBranch(b))
	}
	slices.SortFunc(out, func(a, b gitworkbench.Branch) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func toBranch(b *models.Branch) gitworkbench.Branch {
	out := gitworkbench.Branch{Name: b.Name, Current: b.Head, HeadShort: shortHash(b.CommitHash)}
	if b.UpstreamRemote != "" && b.UpstreamBranch != "" {
		out.Upstream = b.UpstreamRemote + "/" + b.UpstreamBranch
	}
	return out
}

func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}
