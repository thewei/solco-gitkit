package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jesseduffield/lazygit/pkg/commands/git_commands"
	"github.com/jesseduffield/lazygit/pkg/commands/models"
	"github.com/jesseduffield/lazygit/pkg/utils"
	"github.com/stefanhaller/git-todo-parser/todo"
	"github.com/thewei/solco-gitkit/gitworkbench"
)

/**
 * [INPUT]: gitworkbench.HistoryOp（Runner.RewriteHistory 已校验、已加锁）、LazyGit 的 CommitLoader 与 RebaseCommands
 * [OUTPUT]: RewriteHistory——定位选中的连续提交，交给 LazyGit 的 reword / squash / fixup / drop / move；停下的变基留给 Runner 中止
 * [POS]: engine 里批量历史改写的适配；git 编辑器是本进程自己（见 daemon.go），所以宿主必须在 main 里先调 RunDaemon
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

var _ gitworkbench.HistoryBackend = (*Engine)(nil)

// historyWindow is how many commits of the branch a rewrite can see. LazyGit's
// rebase commands read "the list ends at the root commit"; a branch longer
// than the window is only rewritten where that cannot matter.
const historyWindow = 1000

func historyRefusal(format string, args ...any) error {
	return gitworkbench.NewError(gitworkbench.ErrRefusal, gitworkbench.OpMutate, "history", 0, "",
		fmt.Errorf(format, args...))
}

// RewriteHistory applies op through LazyGit's rebase commands.
func (e *Engine) RewriteHistory(ctx context.Context, root string, op gitworkbench.HistoryOp) error {
	if err := ctx.Err(); err != nil {
		return gitworkbench.NewError(gitworkbench.ErrCancelled, gitworkbench.OpMutate, "rebase", 0, "", err)
	}
	s, err := e.session(root)
	if err != nil {
		return err
	}
	commits, truncated, err := s.loadCommits()
	if err != nil {
		return err
	}
	start, end, err := locateRun(commits, op.Hashes)
	if err != nil {
		return err
	}
	// The commits from the newest to the one the rebase starts after must be
	// linear: the rebase replays them, and a merge among them would be
	// recreated rather than kept.
	reach := end + 1
	if op.Action == gitworkbench.HistorySquash || op.Action == gitworkbench.HistoryFixup {
		reach++
	}
	if op.Action == gitworkbench.HistoryMove && op.Offset > 0 {
		reach += op.Offset
	}
	if truncated && reach >= len(commits) {
		return historyRefusal("those commits are too far down the branch for one rewrite; choose newer ones")
	}
	if err := refuseMerges(commits, reach); err != nil {
		return err
	}

	rebase := s.git.Rebase
	switch op.Action {
	case gitworkbench.HistoryReword:
		summary, description, _ := strings.Cut(strings.TrimSpace(op.Message), "\n")
		return rebase.RewordCommit(commits, start, strings.TrimSpace(summary), strings.TrimSpace(description))
	case gitworkbench.HistorySquash, gitworkbench.HistoryFixup:
		if end+1 >= len(commits) {
			return historyRefusal("there is no commit below to squash into")
		}
		action := todo.Squash
		if op.Action == gitworkbench.HistoryFixup {
			action = todo.Fixup
		}
		return rebase.InteractiveRebase(commits, start, end, action, "")
	case gitworkbench.HistoryDrop:
		if start == 0 && end == len(commits)-1 {
			return historyRefusal("dropping every commit would empty the branch; reset instead")
		}
		return rebase.InteractiveRebase(commits, start, end, todo.Drop, "")
	case gitworkbench.HistoryMove:
		// Offset > 0 is toward older commits, which is a higher index.
		if start+op.Offset < 0 || end+op.Offset >= len(commits) {
			return historyRefusal("the commits cannot move that far")
		}
		return rebase.MoveCommits(commits, start, end, op.Offset)
	}
	return errors.New("unknown history action")
}

// locateRun finds the hashes in commits (newest first) and returns the first
// and last index of the run, which must be contiguous.
func locateRun(commits []*models.Commit, hashes []string) (start, end int, err error) {
	want := map[string]bool{}
	for _, h := range hashes {
		want[h] = true
	}
	start, end = -1, -1
	found := 0
	for i, c := range commits {
		if !want[c.Hash()] {
			continue
		}
		if start < 0 {
			start = i
		}
		end = i
		found++
	}
	switch {
	case found != len(want):
		return 0, 0, historyRefusal("a chosen commit is not on this branch (or not among its latest %d)", historyWindow)
	case end-start+1 != found:
		return 0, 0, historyRefusal("the chosen commits must sit next to each other")
	}
	return start, end, nil
}

// loadCommits reads the latest commits of the checked-out branch, newest
// first, and says whether the branch is longer than the window.
func (s *session) loadCommits() (commits []*models.Commit, truncated bool, err error) {
	commits, err = s.git.Loaders.CommitLoader.GetCommits(git_commands.GetCommitsOptions{
		Limit: historyWindow, RefName: "HEAD", MainBranches: s.main, HashPool: &utils.StringPool{},
	})
	return commits, len(commits) >= historyWindow, err
}

// refuseMerges refuses a rewrite when one of the first reach commits is a
// merge: the rebase replays them, and a merge would be recreated, not kept.
func refuseMerges(commits []*models.Commit, reach int) error {
	for i := 0; i < min(reach, len(commits)); i++ {
		if commits[i].IsMerge() {
			return historyRefusal("the range contains a merge commit (%s); history rewriting only handles linear history", shortHash(commits[i].Hash()))
		}
	}
	return nil
}
