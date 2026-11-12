package gitworkbench

import (
	"context"
	"errors"
	"strings"
)

/**
 * [INPUT]: Runner.Backend 的 AmendBackend / CherryPickBackend（LazyGit Rebase 命令组）、RequireCleanWorktree、guardedRewrite
 * [OUTPUT]: AmendTo（把暂存的改动并入更早的提交）与 CherryPickCommits（按给定顺序把一串提交摘到 HEAD）
 * [POS]: LazyGit 独有的两项历史能力；AmendTo 与 RewriteHistory 同一套守卫，CherryPickCommits 无后端时回落到 argv
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// AmendBackend implements AmendTo: the staged changes become part of the
// commit, and the commits above it are replayed. Runner has validated the
// operation, required staged changes only and an attached branch and holds
// the repository's lock; the Runner rolls a failure back.
type AmendBackend interface {
	AmendTo(ctx context.Context, root, commit string) error
}

// CherryPickBackend applies the commits to HEAD, oldest first. Runner has
// validated them (full hashes, no merges), required a clean tree and holds
// the lock. A stop on conflicts stays in progress, as with `git cherry-pick`.
type CherryPickBackend interface {
	CherryPickCommits(ctx context.Context, root string, hashes []string) error
}

// AmendTo amends the commit with whatever is staged. Only staged changes may
// exist (untracked files do not count): anything unstaged would have to be
// carried through the rebase. A failure, a conflict included, leaves the
// repository as it was, the changes staged again.
func (r *Runner) AmendTo(ctx context.Context, root, commit string) error {
	ab, ok := r.Backend.(AmendBackend)
	if !ok {
		return refusal("amend", "this build has no history-rewriting backend")
	}
	if !validFullCommitID(commit) {
		return refusal("amend", "a full Git commit hash is required")
	}
	if r.CurrentBranch(ctx, root) == "" {
		return refusal("amend", "check out a branch first; HEAD is detached")
	}
	if status, err := r.SequencerState(ctx, root); err != nil {
		return err
	} else if status.Active() {
		return &Error{Class: ErrMergeInProgess, OpClass: OpMutate, Op: "amend",
			err: errors.New("a merge, revert, cherry-pick or rebase is already in progress")}
	}
	staged, err := r.Read(ctx, root, "diff", "--cached", "--name-only")
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(staged))) == 0 {
		return refusal("amend", "stage the changes to amend first")
	}
	unstaged, err := r.Read(ctx, root, "diff", "--name-only")
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(unstaged))) != 0 {
		return refusal("amend", "unstaged changes exist; stage them or set them aside first")
	}
	head, err := r.Read(ctx, root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return err
	}

	err = r.guardedRewrite(ctx, root, func() error { return ab.AmendTo(ctx, root, commit) })
	if err == nil {
		return nil
	}
	// The aborted rebase returns to the commit that holds the temporary fixup:
	// take that commit back so the changes wait staged as before.
	if now, rerr := r.Read(ctx, root, "rev-parse", "--verify", "HEAD"); rerr == nil && string(now) != string(head) {
		_, _ = r.Mutate(ctx, root, "reset", "--soft", strings.TrimSpace(string(head)))
	}
	return err
}

// CherryPickCommits applies the commits to HEAD in the order given (oldest
// first), as one operation. Merge commits are refused. A conflict leaves the
// cherry-pick in progress for the sequencer's continue / skip / abort.
func (r *Runner) CherryPickCommits(ctx context.Context, root string, hashes []string) error {
	if len(hashes) == 0 || len(hashes) > MaxRebaseCommits {
		return refusal("cherry-pick", "choose between 1 and %d commits", MaxRebaseCommits)
	}
	seen := map[string]bool{}
	for _, h := range hashes {
		if !validFullCommitID(h) {
			return refusal("cherry-pick", "a full Git commit hash is required")
		}
		if seen[h] {
			return refusal("cherry-pick", "a commit is listed twice")
		}
		seen[h] = true
	}
	if r.CurrentBranch(ctx, root) == "" {
		return refusal("cherry-pick", "check out a branch first; HEAD is detached")
	}
	if err := r.RequireCleanWorktree(ctx, root); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := r.Read(ctx, root, "rev-parse", "--verify", "--quiet", h+"^{commit}"); err != nil {
			return &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "cherry-pick", err: errors.New("a commit no longer exists")}
		}
		parents, err := r.Read(ctx, root, "rev-list", "--parents", "-n", "1", h)
		if err != nil {
			return err
		}
		if len(strings.Fields(string(parents))) > 2 {
			return refusal("cherry-pick", "merge commits need an explicit mainline; not supported")
		}
	}

	if cb, ok := r.Backend.(CherryPickBackend); ok {
		unlock, err := r.LockRepo(root, "cherry-pick")
		if err != nil {
			return err
		}
		defer unlock()
		return cb.CherryPickCommits(ctx, root, hashes)
	}
	_, err := r.Mutate(ctx, root, append([]string{"cherry-pick", "--allow-empty"}, hashes...)...)
	return err
}
