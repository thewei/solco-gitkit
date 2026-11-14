package gitworkbench

import (
	"context"
	"strings"
)

/**
 * [INPUT]: runner.go 的 Read / Mutate、fork_more.go 的 RebasePlan / InteractiveRebase
 * [OUTPUT]: CreateFixup（`git commit --fixup=<提交>`：把已暂存的改动提交成 “fixup! 主题”）、PlanAutosquash（纯函数：把 “fixup! X” 提交挪到 X 之后并标成 fixup；找不到目标的原位保留）、SquashFixups（以所点提交为起点，对它及其之后的提交做一次 autosquash 变基）
 * [POS]: LazyGit 的 “Create fixup commit / Squash all ‘fixup!’ commits above”；没有“中途”状态——变基由 InteractiveRebase 执行，冲突即中止；只认 `fixup!`，`squash!` / `amend!` 不碰
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// CreateFixup commits the staged changes as "fixup! <subject>" of hash, a
// commit on the current history. Nothing staged, or a merge or rebase in
// progress, is refused.
func (r *Runner) CreateFixup(ctx context.Context, root, hash string) error {
	if !validFullCommitID(hash) {
		return refusal("fixup", "a full Git commit hash is required")
	}
	if _, err := r.Read(ctx, root, "merge-base", "--is-ancestor", hash, "HEAD"); err != nil {
		return refusal("fixup", "that commit is not part of the current branch's history")
	}
	if st, err := r.SequencerState(ctx, root); err != nil {
		return err
	} else if st.Active() {
		return refusal("fixup", "a merge, revert, cherry-pick or rebase is already in progress")
	}
	staged, err := r.Read(ctx, root, "diff", "--cached", "--name-only")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(staged)) == "" {
		return refusal("fixup", "stage the changes the fixup should carry first")
	}
	_, err = r.Mutate(ctx, root, "commit", "--fixup="+hash)
	return err
}

const fixupPrefix = "fixup! "

// fixupTarget is the subject a "fixup! ..." commit points at, with any chain
// of fixup! prefixes removed; ok is false for an ordinary commit.
func fixupTarget(subject string) (string, bool) {
	if !strings.HasPrefix(subject, fixupPrefix) {
		return "", false
	}
	for strings.HasPrefix(subject, fixupPrefix) {
		subject = strings.TrimPrefix(subject, fixupPrefix)
	}
	return subject, true
}

// PlanAutosquash orders a plan (oldest first) the way `rebase --autosquash`
// does: each "fixup! X" commit moves to just after the nearest older commit
// whose subject is X (else starts with X) and becomes a fixup; one with no
// target stays where it was, picked. moved counts the fixups that found one.
func PlanAutosquash(plan []RebaseCommit) (steps []RebaseStep, moved int) {
	owner := make([]int, len(plan)) // index of the commit a fixup lands on, itself when none
	for i := range plan {
		owner[i] = i
		want, ok := fixupTarget(plan[i].Subject)
		if !ok {
			continue
		}
		found := -1
		for j := i - 1; j >= 0 && found < 0; j-- {
			if _, isFix := fixupTarget(plan[j].Subject); !isFix && plan[j].Subject == want {
				found = j
			}
		}
		for j := i - 1; j >= 0 && found < 0; j-- {
			if _, isFix := fixupTarget(plan[j].Subject); !isFix && strings.HasPrefix(plan[j].Subject, want) {
				found = j
			}
		}
		if found >= 0 {
			owner[i] = found // only ordinary commits are targets, so found owns itself
		}
	}
	for i := range plan {
		if owner[i] != i {
			continue
		}
		steps = append(steps, RebaseStep{Hash: plan[i].Hash, Action: RebasePick})
		for k := i + 1; k < len(plan); k++ {
			if owner[k] == i {
				steps = append(steps, RebaseStep{Hash: plan[k].Hash, Action: RebaseFixup})
				moved++
			}
		}
	}
	return steps, moved
}

// SquashFixups folds the "fixup!" commits of hash and everything after it
// into their targets (one rebase onto hash's parent) and returns how many it
// folded. No fixup with a target in that range is a refusal; a conflict
// aborts the rebase like every other history rewrite.
func (r *Runner) SquashFixups(ctx context.Context, root, hash string) (int, error) {
	if !validFullCommitID(hash) {
		return 0, refusal("rebase", "a full Git commit hash is required")
	}
	parent, err := r.Read(ctx, root, "rev-parse", "--verify", "--quiet", hash+"^")
	base := strings.TrimSpace(string(parent))
	if err != nil || base == "" {
		return 0, refusal("rebase", "the first commit of the repository has nothing before it to rebase onto")
	}
	plan, err := r.RebasePlan(ctx, root, base)
	if err != nil {
		return 0, err
	}
	steps, moved := PlanAutosquash(plan)
	if moved == 0 {
		return 0, refusal("rebase", "there is no fixup! commit with a target from that commit up")
	}
	if err := r.InteractiveRebase(ctx, root, base, steps); err != nil {
		return 0, err
	}
	return moved, nil
}
