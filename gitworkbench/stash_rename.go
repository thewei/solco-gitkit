package gitworkbench

import (
	"context"
	"fmt"
	"strings"
)

/**
 * [INPUT]: Runner.StashList、validFullCommitID、git stash store/drop
 * [OUTPUT]: StashRename —— 给既有 stash 换一条留言（Fork 的 Rename Stash）
 * [POS]: Git 本身没有 stash 改名；做法与 Fork 一致：同树同父另建一个带新留言的提交，`stash store` 入栈后再丢旧条目（原样重存对栈顶是空操作）。先存后丢，任何一步失败都不会丢数据
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// StashRename gives one stash a new message. The stash is found by its commit
// hash (its stash@{n} index moves whenever another stash is made), rebuilt
// under the new message — it lands on top of the list, as in Fork — and
// the old entry is dropped only once the new one exists.
func (r *Runner) StashRename(ctx context.Context, root, hash, message string) error {
	message = strings.TrimSpace(strings.ReplaceAll(message, "\n", " "))
	if !validFullCommitID(hash) {
		return refusal("stash", "a full Git commit hash is required")
	}
	if message == "" {
		return refusal("stash", "a stash message is required")
	}
	stashes, err := r.StashList(ctx, root)
	if err != nil {
		return err
	}
	old := -1
	for i, st := range stashes {
		if st.Hash == hash {
			old = i
			break
		}
	}
	if old < 0 {
		return &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "stash", err: fmt.Errorf("that stash no longer exists")}
	}
	// The stash is rebuilt as a new commit (same tree, same parents) carrying
	// the new message: storing the unchanged commit again would be a no-op for
	// the top entry, and this way the subject and the reflog text agree.
	out, err := r.Read(ctx, root, "rev-list", "--parents", "-n", "1", hash)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return refusal("stash", "that is not a stash commit")
	}
	args := []string{"commit-tree", hash + "^{tree}"}
	for _, p := range fields[1:] {
		args = append(args, "-p", p)
	}
	args = append(args, "-m", message)
	made, err := r.Mutate(ctx, root, args...)
	if err != nil {
		return err
	}
	renamed := strings.TrimSpace(string(made))
	if !validFullCommitID(renamed) {
		return refusal("stash", "could not rebuild the stash")
	}
	if _, err := r.Mutate(ctx, root, "stash", "store", "-m", message, renamed); err != nil {
		return err
	}
	// The stored copy took index 0, so the original sits one further down.
	_, err = r.Mutate(ctx, root, "stash", "drop", fmt.Sprintf("stash@{%d}", old+1))
	return err
}
