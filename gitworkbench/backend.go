package gitworkbench

import "context"

/**
 * [INPUT]: 一个可选的第二实现（Backend），由应用在启动时装到 Runner.Backend
 * [OUTPUT]: 按能力分组的可选接口；Runner 的对应方法先问 Backend，没有实现的能力照旧走 argv 版 git
 * [POS]: 根模块与「solco-gitkit/engine」（LazyGit 命令层适配，独立 go.mod）之间唯一的接缝；根模块仍只依赖标准库
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// Backend is the marker of a second implementation of some Runner
// capabilities. A capability is a small interface below; the Runner method
// that owns it type-asserts Backend to that interface and, when the backend
// does not implement it, keeps running on the argv `git` path in this
// package. That is how capabilities move to the backend one group at a time
// (2026-11-12 decision) without a signature changing for the callers.
//
// A backend is held to this package's policy, not to its own: argv only,
// GIT_TERMINAL_PROMPT=0, no pager, reads bounded by ReadTimeout and writes by
// MutateTimeout, output bounded by MaxReadOutput, failures returned as *Error
// (build one with NewError), no automatic fetch, no force, no clean.
// Single-flight per repository root stays with the Runner method that calls
// the backend (LockRepo), so a backend must not lock again.
type Backend interface {
	// Name says which implementation this is, for logs and tests.
	Name() string
}

// BranchBackend implements ListBranches. It returns the local branches
// sorted by name, without a detached HEAD pseudo-branch, exactly as the argv
// path does.
type BranchBackend interface {
	ListBranches(ctx context.Context, root string) ([]Branch, error)
}

// TagBackend implements ListTags: tag names, newest creation first, at most
// 500, exactly as the argv path returns them.
type TagBackend interface {
	ListTags(ctx context.Context, root string) ([]Tag, error)
}

// WorktreeBackend implements ListWorktrees: the main work tree first, then
// the linked ones in the order Git lists them; a work tree stopped in a
// rebase counts as detached, as `git worktree list` reports it.
type WorktreeBackend interface {
	ListWorktrees(ctx context.Context, root string) ([]Worktree, error)
}

// NewError builds the structured failure a Backend returns, so a caller
// branches on the same classes whichever path ran. stderr is truncated like
// the argv path's; cause may be nil.
func NewError(class ErrClass, op OpClass, sub string, exitCode int, stderr string, cause error) *Error {
	return &Error{
		Class:    class,
		OpClass:  op,
		Op:       sub,
		ExitCode: exitCode,
		Stderr:   truncate(stderr, 8000),
		err:      cause,
	}
}

// ClassifyStderr is the argv path's own stderr classifier: a backend that has
// Git's words in hand classes the failure the same way.
func ClassifyStderr(stderr string) ErrClass { return classifyErr(stderr) }

// HistoryBackend implements RewriteHistory's rewrite step on one run of
// commits of the checked-out branch. Runner.RewriteHistory has already
// validated the operation, required a clean tree and an attached branch and
// holds the repository's lock; the backend only rewrites. It may leave a
// rebase stopped (conflict or failure): the Runner aborts it, so the
// repository is never left half-done.
type HistoryBackend interface {
	RewriteHistory(ctx context.Context, root string, op HistoryOp) error
}
