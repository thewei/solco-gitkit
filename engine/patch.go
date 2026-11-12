package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/jesseduffield/lazygit/pkg/commands/git_commands"
	"github.com/jesseduffield/lazygit/pkg/commands/models"
	"github.com/thewei/solco-gitkit/gitworkbench"
)

/**
 * [INPUT]: gitworkbench.PatchOp（Runner 已校验、已加锁、工作区干净）、LazyGit 的 PatchBuilder 与 PatchCommands
 * [OUTPUT]: PatchSource、RewriteWithPatch——按行选中一个提交里的改动，移除 / 拆成新提交 / 并入另一个提交 / 退回工作区
 * [POS]: Custom Patch 的适配；PatchBuilder 是 session 里唯一的有状态对象，每次操作前后都 Reset，操作之间互不可见
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

var _ gitworkbench.PatchBackend = (*Engine)(nil)

// PatchSource returns the diff of one file in a commit exactly as LazyGit's
// patch builder reads it, so the line indices of a selection mean the same
// thing here and in RewriteWithPatch.
func (e *Engine) PatchSource(ctx context.Context, root, commit, path, previousPath string) ([]gitworkbench.PatchSourceLine, error) {
	if err := cancelled(ctx, "diff"); err != nil {
		return nil, err
	}
	s, err := e.session(root)
	if err != nil {
		return nil, err
	}
	commits, _, err := s.loadCommits()
	if err != nil {
		return nil, err
	}
	i := indexOf(commits, commit)
	if i < 0 {
		return nil, historyRefusal("the commit is not on this branch (or not among its latest %d)", historyWindow)
	}
	text, err := s.git.WorkingTree.ShowFileDiff(commits[i].ParentRefName(), commit, false, path, previousPath, git_commands.DiffModePlain)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(text, "\n")
	out := make([]gitworkbench.PatchSourceLine, 0, len(lines))
	inHunk := false
	for i, l := range lines {
		if strings.HasPrefix(l, "@@") {
			inHunk = true
		} else if strings.HasPrefix(l, "diff --git") {
			inHunk = false
		}
		out = append(out, gitworkbench.PatchSourceLine{
			Index:      i,
			Text:       l,
			Selectable: inHunk && (strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-")),
		})
	}
	return out, nil
}

// RewriteWithPatch applies op through LazyGit's custom patch commands.
func (e *Engine) RewriteWithPatch(ctx context.Context, root string, op gitworkbench.PatchOp) error {
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
	src := indexOf(commits, op.Commit)
	if src < 0 {
		return historyRefusal("the commit is not on this branch (or not among its latest %d)", historyWindow)
	}
	dst := src
	if op.Action == gitworkbench.PatchMove {
		if dst = indexOf(commits, op.Dest); dst < 0 {
			return historyRefusal("the destination commit is not on this branch (or not among its latest %d)", historyWindow)
		}
	}
	reach := max(src, dst) + 2
	if truncated && reach >= len(commits) {
		return historyRefusal("those commits are too far down the branch for one rewrite; choose newer ones")
	}
	if err := refuseMerges(commits, reach); err != nil {
		return err
	}

	from := commits[src].ParentRefName()
	pb := s.git.Patch.PatchBuilder
	pb.Reset()
	defer pb.Reset()
	defer s.clearTemp()
	pb.Start(from, op.Commit, false, true)
	for _, f := range op.Files {
		if f.Whole {
			err = pb.AddFileWhole(f.Path, f.PreviousPath)
		} else {
			err = pb.AddFileLineRange(f.Path, f.PreviousPath, f.Lines)
		}
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(pb.RenderAggregatedPatch(true)) == "" {
		return historyRefusal("the selection contains no change")
	}

	patch := s.git.Patch
	switch op.Action {
	case gitworkbench.PatchRemove:
		return patch.DeletePatchesFromCommit(commits, src)
	case gitworkbench.PatchSplit:
		summary, description, _ := strings.Cut(strings.TrimSpace(op.Message), "\n")
		return patch.PullPatchIntoNewCommit(commits, src, strings.TrimSpace(summary), strings.TrimSpace(description))
	case gitworkbench.PatchMove:
		return patch.MovePatchToSelectedCommit(commits, src, dst)
	case gitworkbench.PatchUndo:
		return patch.MovePatchIntoIndex(commits, src, false)
	}
	return historyRefusal("unknown patch action %q", op.Action)
}

func indexOf(commits []*models.Commit, hash string) int {
	for i, c := range commits {
		if c.Hash() == hash {
			return i
		}
	}
	return -1
}

// clearTemp removes the patch files LazyGit wrote while applying.
func (s *session) clearTemp() {
	entries, err := os.ReadDir(s.tempDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(s.tempDir, e.Name()))
	}
}
