package gitworkbench

import (
	"context"
	"strings"
)

/**
 * [INPUT]: Runner.Read/Mutate、SequencerState、任意一个会被本地改动挡住的 Git 动作
 * [OUTPUT]: WithAutostash —— Fork 的 "Stash and reapply"：先把本地改动（含新文件）stash，跑动作，再 pop 回来
 * [POS]: 检出/拉取/变基被本地改动拒绝后的重试路径；不引入新 Git 语义——只是 stash push + 动作 + stash pop。动作失败且仓库不在半路状态时立即还原；动作把仓库停在冲突/变基中途时 stash 原样保留，绝不往冲突树里 pop
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// AutostashMessage labels the temporary stash so a kept one is recognisable.
const AutostashMessage = "Solco autostash"

// AutostashResult says what became of the stashed changes.
type AutostashResult struct {
	// Stashed is false when there was nothing to put away.
	Stashed bool
	// Kept is true when the changes could not be reapplied (conflicts with the
	// action's result, or the action left the repository mid-operation) and
	// stay in the stash.
	Kept bool
}

// WithAutostash stashes the local changes, runs action and reapplies them.
// A failing action restores the changes at once unless it left a merge/rebase
// half way; a reapply that conflicts leaves the stash in place (Kept).
func (r *Runner) WithAutostash(ctx context.Context, root string, action func() error) (AutostashResult, error) {
	var res AutostashResult
	before := r.stashTop(ctx, root)
	if _, err := r.Mutate(ctx, root, "stash", "push", "-u", "-m", AutostashMessage); err != nil {
		return res, err
	}
	top := r.stashTop(ctx, root)
	res.Stashed = top != "" && top != before
	if !res.Stashed {
		return res, action()
	}
	actionErr := action()
	if actionErr != nil {
		if st, err := r.SequencerState(ctx, root); err != nil || st.Active() {
			res.Kept = true
			return res, actionErr
		}
	}
	if r.stashTop(ctx, root) != top {
		// Something else pushed a stash meanwhile; never pop a stranger's.
		res.Kept = true
		return res, actionErr
	}
	if _, err := r.Mutate(ctx, root, "stash", "pop"); err != nil {
		res.Kept = true
	}
	return res, actionErr
}

func (r *Runner) stashTop(ctx context.Context, root string) string {
	out, err := r.Read(ctx, root, "rev-parse", "-q", "--verify", "refs/stash")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// LocalChangesBlocked reports whether a Git failure is the refusal to touch
// the working tree because of local changes — the case "Stash and reapply"
// answers.
func LocalChangesBlocked(err error) bool {
	ge, ok := GitError(err)
	if !ok {
		return false
	}
	text := ge.Stderr
	for _, marker := range []string{
		"would be overwritten by checkout",
		"would be overwritten by merge",
		"You have unstaged changes",
		"Please commit your changes or stash them",
		"cannot pull with rebase",
		"cannot rebase: ",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
