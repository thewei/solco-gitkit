package gitworkbench

import (
	"context"
	"strconv"
)

/**
 * [INPUT]: Runner.Backend 的 WriteBackend（LazyGit 的 Branch / Stash / Tag 命令组）
 * [OUTPUT]: WriteBackend 接口与 Runner.viaWriter——校验之后的最后一步由后端执行，否则走 argv
 * [POS]: 写入组的接缝：校验、参数形态、错误分类留在 Runner，只有「执行那条 git 命令」换成 LazyGit
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// WriteBackend runs the last step of the simple write operations. Runner has
// validated every argument (branch names by check-ref-format, full hashes,
// stash references) and holds the repository's lock; a backend only executes.
// Each method does what the argv path's one command does, and a failure is
// reported like it (classified Error).
type WriteBackend interface {
	// SwitchBranch checks out an existing local branch; Git's refusal to carry
	// conflicting local changes is returned, never forced.
	SwitchBranch(ctx context.Context, root, name string) error
	// CreateBranch creates a branch at base (a full ref or hash) and checks it out.
	CreateBranch(ctx context.Context, root, name, base string) error
	RenameBranch(ctx context.Context, root, name, to string) error
	// DeleteBranch is the safe delete: an unmerged branch is refused.
	DeleteBranch(ctx context.Context, root, name string) error
	// StashPush stashes with untracked files and a message (never empty).
	StashPush(ctx context.Context, root, message string) error
	StashPop(ctx context.Context, root string, index int) error
	StashApply(ctx context.Context, root string, index int) error
	StashDrop(ctx context.Context, root string, index int) error
	// CreateTag makes a lightweight tag at ref (HEAD when empty).
	CreateTag(ctx context.Context, root, name, ref string) error
	DeleteTag(ctx context.Context, root, name string) error
}

// viaWriter runs op on the WriteBackend, when there is one, under the
// repository's lock; handled says whether it did (otherwise the caller runs
// its argv command).
func (r *Runner) viaWriter(root, op string, run func(WriteBackend) error) (handled bool, err error) {
	w, ok := r.Backend.(WriteBackend)
	if !ok {
		return false, nil
	}
	unlock, err := r.LockRepo(root, op)
	if err != nil {
		return true, err
	}
	defer unlock()
	return true, run(w)
}

// stashIndex is the N of a validStashRef "stash@{N}".
func stashIndex(ref string) int {
	n, _ := strconv.Atoi(ref[len("stash@{") : len(ref)-1])
	return n
}
