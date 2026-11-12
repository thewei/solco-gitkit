package gitworkbench

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

/**
 * [INPUT]: Runner.Read / run（单仓库锁）、SequencerState、HEAD reflog
 * [OUTPUT]: ReplayPlan（下一步可撤销/重做的那一个动作：种类、两端提交、身份串、说明）、PlanReplay（只读预览）、Replay（按预览的身份串执行撤销或重做）
 * [POS]: 2026-11-12 决定第 (3) 条的 undo / redo：LazyGit 的 z / Z（pkg/gui/controllers/undo_controller.go）与 TanGit 的 history_undo.go（均为 MIT）按 argv 移植——撤销与重做各自往 HEAD reflog 写一条带标记的记录，没有进程内栈，重启后仍平衡，也和 LazyGit 命令行互认；不写任何新引用或文件
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 *
 * 作用范围：HEAD 上的 checkout、commit / reset / pull、一次完整的 rebase；工作区丢弃、删分支、drop stash 不在 reflog 里，归 Safety Journal。
 */

// The reflog markers are LazyGit's own: the reflog is state shared by every
// Git client, so Solco Git and LazyGit read each other's undo and redo entries.
const (
	reflogUndoMarker = "[lazygit undo]"
	reflogRedoMarker = "[lazygit redo]"
)

// maxReplayReflog bounds how far back one undo looks: the marker entries that
// balance an action may be older than the Reflog list's own page, so the walk
// reads far more than the UI shows.
const maxReplayReflog = 5000

// ReplayKind is the sort of reflog action a plan replays.
type ReplayKind string

const (
	ReplayCheckout ReplayKind = "checkout"
	ReplayCommit   ReplayKind = "commit"
	ReplayRebase   ReplayKind = "rebase"
)

// ReplayPlan is the one action an undo (or redo) would take now. Identity
// binds a confirmation to exactly this action: both endpoint hashes and the
// kind must still be the next replay when it is accepted, so another Git
// client moving HEAD in between makes the confirmation refuse rather than undo
// something the user never approved.
type ReplayPlan struct {
	Redo        bool
	Kind        ReplayKind
	From, To    string
	Identity    string
	Description string
}

type reflogEntry struct {
	hash string
	name string
}

type reflogAction struct {
	kind     ReplayKind
	current  bool // an unfinished rebase: nothing can be undone past it
	from, to string
	text     string
}

var (
	checkoutReflogPattern = regexp.MustCompile(`^checkout: moving from (\S+) to (\S+)`)
	rebaseStartPattern    = regexp.MustCompile(`^rebase (?:-i )?\(start\)`)
	rebaseFinishPattern   = regexp.MustCompile(`^rebase (?:-i )?\((?:finish|abort)\)`)
)

func (a reflogAction) identity() string { return fmt.Sprintf("%s:%s:%s", a.kind, a.from, a.to) }

// PlanReplay reads the whole HEAD reflog and says what an undo (or redo) would
// replay; ok is false when there is nothing eligible.
func (r *Runner) PlanReplay(ctx context.Context, root string, redo bool) (plan ReplayPlan, ok bool, err error) {
	entries, err := r.replayEntries(ctx, root)
	if err != nil {
		return ReplayPlan{}, false, err
	}
	action, ok := findReflogAction(entries, redo)
	if !ok {
		return ReplayPlan{}, false, nil
	}
	return action.plan(redo), true, nil
}

func (a reflogAction) plan(redo bool) ReplayPlan {
	return ReplayPlan{Redo: redo, Kind: a.kind, From: a.from, To: a.to, Identity: a.identity(), Description: a.text}
}

// Replay undoes (or redoes) the action PlanReplay reported. expected is that
// plan's Identity, or empty to take whatever is next. A merge, revert,
// cherry-pick or rebase in progress refuses, as does a history that moved
// since the plan was read.
func (r *Runner) Replay(ctx context.Context, root string, redo bool, expected string) error {
	verb := "undo"
	if redo {
		verb = "redo"
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := r.LockRepo(root, verb)
	if err != nil {
		return err
	}
	defer unlock()

	state, err := r.SequencerState(ctx, root)
	if err != nil {
		return err
	}
	if state.Active() {
		return &Error{Class: ErrMergeInProgess, OpClass: OpMutate, Op: verb,
			err: fmt.Errorf("cannot %s while a merge, revert, cherry-pick or rebase is in progress", verb)}
	}
	entries, err := r.replayEntries(ctx, root)
	if err != nil {
		return err
	}
	action, ok := findReflogAction(entries, redo)
	if expected != "" && (!ok || action.identity() != expected) {
		return &Error{Class: ErrStaleState, OpClass: OpMutate, Op: verb, err: errors.New("Git history changed; review the operation and try again")}
	}
	if !ok {
		return refusal(verb, "no Git operation to %s", verb)
	}
	for _, h := range []string{action.from, action.to} {
		if h == "" || strings.HasPrefix(h, "-") {
			return refusal(verb, "the recorded action names no commit")
		}
	}

	marker := reflogUndoMarker
	if redo {
		marker = reflogRedoMarker
	}
	env := append(mutateEnv(), "GIT_REFLOG_ACTION="+marker)
	do := func(args ...string) ([]byte, error) { return r.runMutating(ctx, root, env, args...) }

	switch action.kind {
	case ReplayCheckout:
		target := action.from
		if redo {
			target = action.to
		}
		_, err := do("checkout", target, "--")
		return err
	case ReplayCommit:
		// Undoing a commit, reset or pull is soft: its changes stay staged.
		// Reapplying it is a hard reset to the recorded post-action commit.
		if redo {
			return r.resetProtectingWork(ctx, root, action.to, do)
		}
		_, err := do("reset", "--soft", action.from)
		return err
	case ReplayRebase:
		target := action.from
		if redo {
			target = action.to
		}
		return r.resetProtectingWork(ctx, root, target, do)
	}
	return refusal(verb, "cannot %s the selected action", verb)
}

// replayEntries is the reflog newest first, deep enough to balance markers.
func (r *Runner) replayEntries(ctx context.Context, root string) ([]reflogEntry, error) {
	out, err := r.Read(ctx, root, "log", "-g", "--no-color", "-n", strconv.Itoa(maxReplayReflog), "--format=%H%x1f%gs", "HEAD")
	if err != nil {
		if ge, ok := GitError(err); ok && strings.Contains(ge.Stderr, "does not have any commits") {
			return nil, nil
		}
		return nil, err
	}
	var entries []reflogEntry
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		hash, name, ok := strings.Cut(line, "\x1f")
		if !ok || !validFullCommitID(hash) {
			continue
		}
		entries = append(entries, reflogEntry{hash: hash, name: name})
	}
	return entries, nil
}

// runMutating runs one mutation while the caller already holds the repository
// lock (Mutate would refuse a second lock), classifying a failure the same way.
func (r *Runner) runMutating(ctx context.Context, root string, env []string, args ...string) ([]byte, error) {
	out, err := r.run(ctx, OpMutate, MutateTimeout, MaxReadOutput, env, root, args)
	if err != nil {
		if ge, ok := GitError(err); ok && ge.Class == ErrOther {
			ge.Class = classifyErr(ge.Stderr)
		}
	}
	return out, err
}

// resetProtectingWork resets hard to ref but keeps tracked and staged work:
// it is stashed first and put back after, as LazyGit does, so a redo never
// silently loses what was edited since. A failed reset puts the work back
// before reporting; a failed put-back leaves it in the stash and says so.
// Untracked files are never touched by a reset and are left alone.
func (r *Runner) resetProtectingWork(ctx context.Context, root, ref string, do func(args ...string) ([]byte, error)) error {
	dirty, err := r.Read(ctx, root, "status", "--porcelain=v1", "--untracked-files=no", "--ignore-submodules=all")
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(dirty))) == 0 {
		_, err := do("reset", "--hard", ref)
		return err
	}
	before := r.stashTop(ctx, root)
	if _, err := do("stash", "push", "-m", "Solco Git: work set aside before reset"); err != nil {
		return err
	}
	if top := r.stashTop(ctx, root); top == "" || top == before {
		_, err := do("reset", "--hard", ref) // nothing was stashed (submodule-only changes)
		return err
	}
	if _, err := do("reset", "--hard", ref); err != nil {
		if _, popErr := do("stash", "pop"); popErr != nil {
			return errors.Join(err, errors.New("your work is kept in the stash"))
		}
		return err
	}
	if _, err := do("stash", "pop"); err != nil {
		return &Error{Class: ErrConflict, OpClass: OpMutate, Op: "undo",
			err: errors.New("the reset is done, but your earlier changes conflict with it and stay in the stash")}
	}
	return nil
}

// findReflogAction balances user actions against marked undo / redo entries
// while walking newest to oldest. Unsupported entries are skipped. A rebase
// counts as one action only once its finish (or abort) entry has been paired
// with its start; a start without a finish means one is running now.
func findReflogAction(entries []reflogEntry, redo bool) (reflogAction, bool) {
	counter := 0
	rebaseFinishHash := ""
	for i, entry := range entries {
		previous := ""
		if i+1 < len(entries) {
			previous = entries[i+1].hash
		}
		var action *reflogAction
		if rebaseFinishHash == "" {
			checkout := checkoutReflogPattern.FindStringSubmatch(entry.name)
			switch {
			case strings.HasPrefix(entry.name, reflogUndoMarker):
				counter++
			case strings.HasPrefix(entry.name, reflogRedoMarker):
				counter--
			case rebaseFinishPattern.MatchString(entry.name):
				rebaseFinishHash = entry.hash
			case len(checkout) > 0:
				action = &reflogAction{kind: ReplayCheckout, from: checkout[1], to: checkout[2], text: entry.name}
			case isCommitResetOrPull(entry.name):
				action = &reflogAction{kind: ReplayCommit, from: previous, to: entry.hash, text: entry.name}
			case rebaseStartPattern.MatchString(entry.name):
				action = &reflogAction{kind: ReplayRebase, current: true, from: previous}
			}
		} else if rebaseStartPattern.MatchString(entry.name) {
			action = &reflogAction{kind: ReplayRebase, from: previous, to: rebaseFinishHash, text: "completed rebase"}
			rebaseFinishHash = ""
		}
		if action == nil {
			continue
		}
		if action.current {
			return reflogAction{}, false
		}
		// An action that left HEAD where it was moved nothing, and the root
		// commit has no earlier commit to go back to.
		if action.from == "" || action.to == "" || action.from == action.to {
			continue
		}
		if redo {
			if counter == 0 {
				return reflogAction{}, false
			}
			if counter == 1 {
				return *action, true
			}
		} else if counter == 0 {
			return *action, true
		}
		counter--
	}
	return reflogAction{}, false
}

func isCommitResetOrPull(name string) bool {
	return strings.HasPrefix(name, "commit") ||
		strings.HasPrefix(name, "reset: moving to") ||
		strings.HasPrefix(name, "pull")
}
