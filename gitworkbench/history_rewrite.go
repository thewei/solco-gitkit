package gitworkbench

import (
	"context"
	"fmt"
	"strings"
)

/**
 * [INPUT]: Runner.Backend 的 HistoryBackend（LazyGit Rebase 命令组）、RequireCleanWorktree、SequencerState
 * [OUTPUT]: RewriteHistory——对 HEAD 分支上一段连续提交做 reword / squash / fixup / drop / move；任何失败（含冲突）都中止变基，仓库回到原样
 * [POS]: 批量历史改写的入口。与 InteractiveRebase（整份 todo 计划，argv 实现）并存：这里是「选中一段、做一件事」的 LazyGit 语义
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// HistoryAction is what RewriteHistory does to the selected run of commits.
type HistoryAction string

const (
	HistoryReword HistoryAction = "reword" // new message for exactly one commit
	HistorySquash HistoryAction = "squash" // into the commit just below the run, messages joined
	HistoryFixup  HistoryAction = "fixup"  // into the commit just below the run, messages dropped
	HistoryDrop   HistoryAction = "drop"   // remove the commits
	HistoryMove   HistoryAction = "move"   // Offset places toward older (>0) or newer (<0) commits
)

// HistoryOp is one rewrite. Hashes are full hashes of commits that sit next to
// each other on the checked-out branch, in any order.
type HistoryOp struct {
	Action  HistoryAction
	Hashes  []string
	Message string // reword only
	Offset  int    // move only
}

// MaxHistoryMove bounds how far one move shifts a run.
const MaxHistoryMove = 100

// RewriteHistory applies op to the checked-out branch. It needs a HistoryBackend
// (the LazyGit engine); without one it refuses and InteractiveRebase remains the
// way. A conflict or any other failure aborts the rebase: nothing changes, and a
// conflict is reported as ErrConflict with the files that clashed.
func (r *Runner) RewriteHistory(ctx context.Context, root string, op HistoryOp) error {
	if err := validateHistoryOp(op); err != nil {
		return err
	}
	hb, ok := r.Backend.(HistoryBackend)
	if !ok {
		return refusal("history", "this build has no history-rewriting backend; use the rebase plan instead")
	}
	if r.CurrentBranch(ctx, root) == "" {
		return refusal("history", "check out a branch first; HEAD is detached")
	}
	if err := r.RequireCleanWorktree(ctx, root); err != nil {
		return err
	}

	return r.guardedRewrite(ctx, root, func() error { return hb.RewriteHistory(ctx, root, op) })
}

// guardedRewrite runs a backend rewrite under the repository's lock and, when
// it fails for whatever reason, rolls a rebase left behind back: nothing
// changes, and a conflict is reported as ErrConflict with the files that
// clashed. The caller has already validated and required a clean tree.
func (r *Runner) guardedRewrite(ctx context.Context, root string, rewrite func() error) error {
	unlock, err := r.LockRepo(root, "rebase")
	if err != nil {
		return err
	}
	err = rewrite()
	unlock()
	if err == nil {
		return nil
	}

	state, _ := r.SequencerState(ctx, root)
	if state.Active() {
		_, _ = r.Mutate(ctx, root, "rebase", "--abort")
	}
	if state.Unmerged > 0 {
		return &Error{Class: ErrConflict, OpClass: OpMutate, Op: "rebase", Paths: state.UnmergedPaths,
			err: fmt.Errorf("the rewrite stopped on conflicts in %d files and was aborted; nothing changed", state.Unmerged)}
	}
	return err
}

func validateHistoryOp(op HistoryOp) error {
	if len(op.Hashes) == 0 || len(op.Hashes) > MaxRebaseCommits {
		return refusal("history", "choose between 1 and %d commits", MaxRebaseCommits)
	}
	seen := map[string]bool{}
	for _, h := range op.Hashes {
		if !validFullCommitID(h) {
			return refusal("history", "a full Git commit hash is required")
		}
		if seen[h] {
			return refusal("history", "a commit is listed twice")
		}
		seen[h] = true
	}
	switch op.Action {
	case HistoryReword:
		if len(op.Hashes) != 1 {
			return refusal("history", "reword changes one commit at a time")
		}
		if strings.TrimSpace(op.Message) == "" {
			return refusal("history", "a reworded commit needs a message")
		}
	case HistorySquash, HistoryFixup, HistoryDrop:
		if op.Message != "" {
			return refusal("history", "%s takes no message", op.Action)
		}
	case HistoryMove:
		if op.Offset == 0 || op.Offset > MaxHistoryMove || op.Offset < -MaxHistoryMove {
			return refusal("history", "a move needs a distance between 1 and %d", MaxHistoryMove)
		}
	default:
		return refusal("history", "unknown action %q", op.Action)
	}
	if op.Action != HistoryMove && op.Offset != 0 {
		return refusal("history", "%s takes no distance", op.Action)
	}
	return nil
}
