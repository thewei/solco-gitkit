package gitworkbench

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

/**
 * [INPUT]: journal.go（记录与固定引用）、Runner.run（临时索引、commit-tree）、已有的 Discard / DeleteUntracked
 * [OUTPUT]: 丢弃改动与删除未跟踪文件的恢复点：captureTracked / captureUntracked（把会丢的内容写成只含这些路径的树提交，不动用户的索引与工作区）、undoWork（按记录把它们放回去，现状变了就拒绝）
 * [POS]: Safety Journal 的第一批（2026-11-12）：这两个操作之前的确认框写着「无法撤销」，现在有恢复点。快照做在临时索引里（GIT_INDEX_FILE），所以捕获本身不会改用户的任何东西
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// snapshotEnv is the identity the hidden snapshot commits carry: they are
// private to the repository's git directory and never reach a branch.
func snapshotEnv(index string) []string {
	return append(identityEnv(), "GIT_INDEX_FILE="+index)
}

func identityEnv() []string {
	return append(mutateEnv(),
		"GIT_AUTHOR_NAME=Solco Git", "GIT_AUTHOR_EMAIL=solco-git@localhost",
		"GIT_COMMITTER_NAME=Solco Git", "GIT_COMMITTER_EMAIL=solco-git@localhost")
}

// tempIndexPath names a throwaway index file next to the journal; the caller
// removes it.
func (r *Runner) tempIndexPath(ctx context.Context, root string) (string, error) {
	dir, err := r.journalDir(ctx, root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".index-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	f.Close()
	os.Remove(name) // git creates it; an empty file is not a valid index
	return name, nil
}

// pathsSize adds up the sizes of the files a snapshot would hold, and refuses
// past MaxRecoveryBytes.
func pathsSize(root string, paths []string) error {
	var total int64
	tooLarge := &Error{Class: ErrTooLarge, OpClass: OpMutate, Op: "journal",
		err: fmt.Errorf("these files are over %d MiB, too large to keep a recovery point for", MaxRecoveryBytes>>20)}
	for _, rel := range paths {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(rel)), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}
			if d.Type().IsRegular() {
				if info, err := d.Info(); err == nil {
					total += info.Size()
				}
			}
			if total > MaxRecoveryBytes {
				return tooLarge
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// captureTree writes the files named by build (run against a throwaway index)
// into a tree, and makes a commit of it on top of HEAD (when there is one).
func (r *Runner) captureTree(ctx context.Context, root, label string, build func(env []string) error, seed string) (string, error) {
	index, err := r.tempIndexPath(ctx, root)
	if err != nil {
		return "", err
	}
	defer os.Remove(index)
	env := snapshotEnv(index)
	if seed != "" {
		if _, err := r.runMutating(ctx, root, env, "read-tree", seed); err != nil {
			return "", err
		}
	}
	if err := build(env); err != nil {
		return "", err
	}
	tree, err := r.runMutating(ctx, root, env, "write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", strings.TrimSpace(string(tree)), "-m", "solco-git recovery point: " + label}
	if head, err := r.Read(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		args = append(args, "-p", strings.TrimSpace(string(head)))
	}
	out, err := r.runMutating(ctx, root, env, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// captureTracked keeps the working-tree state of tracked paths: what a
// discard (`git restore`, worktree against the index) is about to overwrite.
// Paths already deleted in the working tree are listed too, so recovering
// takes the restored file away again.
func (r *Runner) captureTracked(ctx context.Context, root string, paths []string) (snapshot string, removed []string, err error) {
	if err := pathsSize(root, paths); err != nil {
		return "", nil, err
	}
	idx, err := r.Read(ctx, root, "write-tree")
	if err != nil {
		return "", nil, refusal("discard", "resolve the conflicts before discarding")
	}
	deleted, err := r.Read(ctx, root, append([]string{"--literal-pathspecs", "ls-files", "-d", "-z", "--"}, paths...)...)
	if err != nil {
		return "", nil, err
	}
	for _, p := range strings.Split(string(deleted), "\x00") {
		if p != "" {
			removed = append(removed, p)
		}
	}
	snapshot, err = r.captureTree(ctx, root, "discard", func(env []string) error {
		_, err := r.runMutating(ctx, root, env, append([]string{"--literal-pathspecs", "add", "-A", "--"}, paths...)...)
		return err
	}, strings.TrimSpace(string(idx)))
	return snapshot, removed, err
}

// captureUntracked keeps untracked files about to be deleted.
func (r *Runner) captureUntracked(ctx context.Context, root string, paths []string) (string, error) {
	if err := pathsSize(root, paths); err != nil {
		return "", err
	}
	var present []string
	for _, rel := range paths {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			present = append(present, rel)
		}
	}
	if len(present) == 0 {
		return "", nil
	}
	return r.captureTree(ctx, root, "delete untracked", func(env []string) error {
		list := strings.Join(present, "\x00") + "\x00"
		_, err := r.runMutatingStdin(ctx, root, env, list, "update-index", "--add", "-z", "--stdin")
		return err
	}, "")
}

// runMutatingStdin is runMutating with stdin; the caller holds the lock.
func (r *Runner) runMutatingStdin(ctx context.Context, root string, env []string, stdin string, args ...string) ([]byte, error) {
	out, err := r.runStdinEnv(ctx, OpMutate, MutateTimeout, env, root, stdin, args)
	if err != nil {
		if ge, ok := GitError(err); ok && ge.Class == ErrOther {
			ge.Class = classifyErr(ge.Stderr)
		}
	}
	return out, err
}

// journaledWork runs action as a journaled destructive operation: pin a
// recovery point, record it pending, run, settle the record. Nothing runs when
// the recovery point cannot be kept.
func (r *Runner) journaledWork(ctx context.Context, root string, op Operation, capture func() (string, error), action func() error, fill, after func(*Operation)) error {
	unlock, err := r.LockRepo(root, op.Kind)
	if err != nil {
		return err
	}
	defer unlock()
	snapshot, err := capture()
	if err != nil {
		return err
	}
	op.Snapshot = snapshot
	if fill != nil {
		fill(&op)
	}
	rec, err := r.journalBegin(ctx, root, op)
	if err != nil {
		return err
	}
	actionErr := action()
	if actionErr == nil && after != nil {
		// What the operation left behind, so a later recovery can tell
		// whether anything touched it since.
		after(&rec)
	}
	r.journalFinish(ctx, root, rec, actionErr)
	return actionErr
}

// undoWork puts a discarded or deleted set of files back, after checking that
// nothing there changed since: the files the discard restored must still equal
// the index, the files the delete removed must still be absent. Tracked files
// come back from the snapshot into the working tree only (the index is not
// touched).
func (r *Runner) undoWork(ctx context.Context, root string, op Operation) error {
	if err := r.verifySnapshot(ctx, root, op); err != nil {
		return err
	}
	switch op.Kind {
	case OpKindDiscard:
		if _, err := r.Read(ctx, root, append([]string{"--literal-pathspecs", "diff", "--quiet", "--"}, op.Paths...)...); err != nil {
			return &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "recover",
				err: errors.New("these files changed again after the discard; recovering would overwrite them")}
		}
		inSnap, err := r.Read(ctx, root, append([]string{"--literal-pathspecs", "ls-tree", "-r", "--name-only", "-z", op.Snapshot, "--"}, op.Paths...)...)
		if err != nil {
			return err
		}
		var back []string
		for _, p := range strings.Split(string(inSnap), "\x00") {
			if p != "" {
				back = append(back, p)
			}
		}
		if len(back) > 0 {
			if _, err := r.runMutating(ctx, root, mutateEnv(), append([]string{"--literal-pathspecs", "restore", "--source=" + op.Snapshot, "--worktree", "--"}, back...)...); err != nil {
				return err
			}
		}
		for _, rel := range op.Removed {
			if err := safeRemove(root, rel); err != nil {
				return err
			}
		}
	case OpKindDeleteUntracked:
		for _, rel := range op.Paths {
			if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
				return &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "recover",
					err: fmt.Errorf("%s exists again; recovering would overwrite it", rel)}
			}
		}
		if _, err := r.runMutating(ctx, root, mutateEnv(), append([]string{"--literal-pathspecs", "restore", "--source=" + op.Snapshot, "--worktree", "--"}, op.Paths...)...); err != nil {
			return err
		}
	}
	return nil
}

// verifySnapshot makes sure the pinned ref still points at the commit the
// record names, so a record cannot restore something it did not capture.
func (r *Runner) verifySnapshot(ctx context.Context, root string, op Operation) error {
	out, err := r.Read(ctx, root, "rev-parse", "--verify", "--quiet", journalRefPrefix+op.ID)
	if err != nil || strings.TrimSpace(string(out)) != op.Snapshot {
		return &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "recover", err: errors.New("the recovery point is missing")}
	}
	return nil
}

func safeRemove(root, rel string) error {
	if rel == "" || strings.Contains(rel, "\x00") || filepath.IsAbs(rel) {
		return refusal("recover", "refusing to remove %q", rel)
	}
	target := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	if !strings.HasPrefix(target, filepath.Clean(root)+string(filepath.Separator)) {
		return refusal("recover", "refusing to remove %q", rel)
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// UndoOperation recovers what a journaled operation destroyed. It
// revalidates the repository first and refuses when it has changed since.
func (r *Runner) UndoOperation(ctx context.Context, root, id string) error {
	op, err := r.readOperation(ctx, root, id)
	if err != nil {
		return err
	}
	if !op.CanUndo() {
		return refusal("recover", "this entry cannot be recovered")
	}
	unlock, err := r.LockRepo(root, "recover")
	if err != nil {
		return err
	}
	defer unlock()
	switch op.Kind {
	case OpKindDiscard, OpKindDeleteUntracked:
		err = r.undoWork(ctx, root, op)
	case OpKindBranchDelete:
		err = r.undoBranchDelete(ctx, root, op)
	case OpKindStashDrop:
		err = r.undoStashDrop(ctx, root, op)
	case OpKindApplyPatch:
		err = r.undoApplyPatch(ctx, root, op)
	case OpKindStashClear:
		err = r.undoStashClear(ctx, root, op)
	default:
		err = refusal("recover", "this kind of entry cannot be recovered yet")
	}
	if err != nil {
		return err
	}
	return r.markUndone(ctx, root, op)
}

// undoBranchDelete makes the branch again at the tip it had. A branch of that
// name that exists now is never touched. Its upstream setting is not restored:
// Git removed it with the branch and the record does not keep it.
func (r *Runner) undoBranchDelete(ctx context.Context, root string, op Operation) error {
	if err := r.verifySnapshot(ctx, root, op); err != nil {
		return err
	}
	if _, err := r.Read(ctx, root, "rev-parse", "--verify", "--quiet", "refs/heads/"+op.Name); err == nil {
		return &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "recover",
			err: fmt.Errorf("a branch named %s exists again", op.Name)}
	}
	_, err := r.runMutating(ctx, root, mutateEnv(), "update-ref", "-m", "solco-git: recover branch "+op.Name,
		"refs/heads/"+op.Name, op.Snapshot, strings.Repeat("0", len(op.Snapshot)))
	return err
}

// undoStashDrop puts the dropped stash back on top of the stash list.
func (r *Runner) undoStashDrop(ctx context.Context, root string, op Operation) error {
	if err := r.verifySnapshot(ctx, root, op); err != nil {
		return err
	}
	_, err := r.runMutating(ctx, root, mutateEnv(), "stash", "store", "-m", op.Name, op.Snapshot)
	return err
}
