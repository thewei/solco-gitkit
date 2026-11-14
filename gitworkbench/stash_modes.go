package gitworkbench

import (
	"context"
	"strings"
)

/**
 * [INPUT]: runner.go 的 Read / Mutate
 * [OUTPUT]: StashPushMode（LazyGit 的 stash 变体）：StashStaged（只存已暂存的，`stash push --staged`）、StashKeepIndex（全存但保留已暂存的留在索引与工作区，`stash push --keep-index -u`）
 * [POS]: 与 StashPush / StashPushPaths 并列，直接走 argv；没有可存的改动时先给出一句人话而不是 Git 的原话
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// StashMode picks what a stash takes.
type StashMode string

const (
	// StashStaged stashes only what is staged; the rest stays in the work tree.
	StashStaged StashMode = "staged"
	// StashKeepIndex stashes everything, tracked and untracked, and leaves the
	// staged changes in the index and the work tree as well.
	StashKeepIndex StashMode = "keep-index"
)

// StashPushMode saves the changes StashMode names, with an optional message.
func (r *Runner) StashPushMode(ctx context.Context, root string, mode StashMode, message string) error {
	var args []string
	switch mode {
	case StashStaged:
		staged, err := r.Read(ctx, root, "diff", "--cached", "--name-only")
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(staged)) == "" {
			return refusal("stash", "nothing is staged")
		}
		args = []string{"stash", "push", "--staged"}
	case StashKeepIndex:
		status, err := r.Read(ctx, root, "status", "--porcelain=v1", "--untracked-files=all")
		if err != nil {
			return err
		}
		if len(status) == 0 {
			return refusal("stash", "there are no changes to stash")
		}
		args = []string{"stash", "push", "--keep-index", "-u"}
	default:
		return refusal("stash", "unknown stash mode %q", mode)
	}
	if message = strings.TrimSpace(message); message != "" {
		args = append(args, "-m", message)
	}
	_, err := r.Mutate(ctx, root, args...)
	return err
}
