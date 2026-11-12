package gitworkbench

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

/**
 * [INPUT]: journal.go / journal_work.go（记录、固定引用、临时索引）、ApplyPatch 与 StashClear
 * [OUTPUT]: 应用补丁与 Drop All Stashes 的恢复点：补丁涉及的路径在应用前的工作区状态钉成提交、应用后的树记进 After（找回时现状不等于它就拒绝）；清空 stash 时把每个 stash 提交作为父提交挂在一个恢复提交上（一条引用钉住全部），找回 = 从最旧到最新逐个 `stash store`
 * [POS]: Safety Journal 第三批（2026-11-12 决定第 (9) 条）：reflog 管不到的最后两类破坏性操作
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// maxClearedStashes bounds the stashes one Drop All keeps a recovery point for
// (each is a parent argument of the pinning commit).
const maxClearedStashes = 200

// patchPaths lists the working-tree paths a patch file would touch (both names
// of a rename), as the repository root sees them.
func (r *Runner) patchPaths(ctx context.Context, root, file string) ([]string, error) {
	out, err := r.Read(ctx, root, "apply", "--numstat", "-z", "--", file)
	if err != nil {
		return nil, err
	}
	toks := strings.Split(string(out), "\x00")
	var paths []string
	add := func(p string) error {
		p = filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
		if p == "" || p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
			return refusal("apply", "the patch names a path outside the repository")
		}
		paths = append(paths, p)
		return nil
	}
	for i := range toks {
		if toks[i] == "" {
			continue
		}
		f := strings.SplitN(toks[i], "\t", 3)
		if len(f) != 3 {
			return nil, refusal("apply", "cannot read the patch's file list")
		}
		if f[2] == "" {
			return nil, refusal("apply", "cannot read the patch's file list")
		}
		if err := add(f[2]); err != nil {
			return nil, err
		}
	}
	// `apply --numstat` names only the new side of a rename, but the old
	// name disappears too: read those from the patch's own header lines.
	// A "rename from" line inside a mail's message only adds a path the
	// snapshot keeps anyway, which recovery treats as unchanged.
	body, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(body), "\n") {
		from, ok := strings.CutPrefix(line, "rename from ")
		if !ok {
			continue
		}
		from = strings.TrimRight(from, "\r")
		if strings.HasPrefix(from, `"`) {
			unquoted, err := strconv.Unquote(from)
			if err != nil {
				return nil, refusal("apply", "cannot read a renamed file's name")
			}
			from = unquoted
		}
		if err := add(from); err != nil {
			return nil, err
		}
	}
	if len(paths) == 0 {
		return nil, refusal("apply", "the patch changes no file")
	}
	return paths, nil
}

// worktreeTree is the tree the given paths have in the working tree right now
// (absent paths simply are not in it), built in a throwaway index.
func (r *Runner) worktreeTree(ctx context.Context, root string, paths []string) (string, error) {
	index, err := r.tempIndexPath(ctx, root)
	if err != nil {
		return "", err
	}
	defer os.Remove(index)
	env := snapshotEnv(index)
	list := strings.Join(paths, "\x00") + "\x00"
	if _, err := r.runMutatingStdin(ctx, root, env, list, "--literal-pathspecs", "update-index", "--add", "--remove", "-z", "--stdin"); err != nil {
		return "", err
	}
	out, err := r.runMutating(ctx, root, env, "write-tree")
	return strings.TrimSpace(string(out)), err
}

// journaledApplyPatch is ApplyPatch's `git apply` step under the journal: the
// touched paths are kept as they are now, and recorded as they become.
func (r *Runner) journaledApplyPatch(ctx context.Context, root, file string) error {
	paths, err := r.patchPaths(ctx, root, file)
	if err != nil {
		return err
	}
	var removed []string
	return r.journaledWork(ctx, root, Operation{Kind: OpKindApplyPatch, Paths: paths, Name: filepath.Base(file)},
		func() (string, error) {
			if err := pathsSize(root, paths); err != nil {
				return "", err
			}
			for _, p := range paths {
				if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p))); err != nil {
					removed = append(removed, p)
				}
			}
			return r.captureTree(ctx, root, "apply patch", func(env []string) error {
				list := strings.Join(paths, "\x00") + "\x00"
				_, err := r.runMutatingStdin(ctx, root, env, list, "--literal-pathspecs", "update-index", "--add", "--remove", "-z", "--stdin")
				return err
			}, "")
		},
		func() error {
			_, err := r.runMutating(ctx, root, mutateEnv(), "apply", "--whitespace=nowarn", "--", file)
			return err
		},
		func(op *Operation) { op.Removed = removed },
		func(op *Operation) {
			// A failure to read the result back leaves After empty, which
			// recovery refuses: no unchecked overwrite.
			if tree, err := r.worktreeTree(ctx, root, paths); err == nil {
				op.After = tree
			}
		})
}

func (r *Runner) undoApplyPatch(ctx context.Context, root string, op Operation) error {
	if err := r.verifySnapshot(ctx, root, op); err != nil {
		return err
	}
	now, err := r.worktreeTree(ctx, root, op.Paths)
	if err != nil {
		return err
	}
	if op.After == "" || now != op.After {
		return &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "recover",
			err: errors.New("files the patch touched changed after it was applied; recovering would overwrite them")}
	}
	in, err := r.Read(ctx, root, append([]string{"--literal-pathspecs", "ls-tree", "-r", "--name-only", "-z", op.Snapshot, "--"}, op.Paths...)...)
	if err != nil {
		return err
	}
	var back []string
	for _, p := range strings.Split(string(in), "\x00") {
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
	return nil
}

// journaledStashClear pins every stash under one commit before they go.
func (r *Runner) journaledStashClear(ctx context.Context, root string) error {
	out, err := r.Read(ctx, root, "stash", "list", "--format=%H")
	if err != nil {
		return err
	}
	hashes := strings.Fields(string(out))
	if len(hashes) == 0 {
		return nil
	}
	if len(hashes) > maxClearedStashes {
		return &Error{Class: ErrTooLarge, OpClass: OpMutate, Op: "stash",
			err: fmt.Errorf("%d stashes are more than a recovery point can keep (%d)", len(hashes), maxClearedStashes)}
	}
	return r.journaledWork(ctx, root, Operation{Kind: OpKindStashClear, Name: itoaN(len(hashes)) + " stashes"},
		func() (string, error) {
			empty, err := r.runMutatingStdin(ctx, root, identityEnv(), "", "mktree")
			if err != nil {
				return "", err
			}
			args := []string{"commit-tree", strings.TrimSpace(string(empty)), "-m", "solco-git recovery point: drop all stashes"}
			for _, h := range hashes {
				args = append(args, "-p", h)
			}
			c, err := r.runMutating(ctx, root, identityEnv(), args...)
			return strings.TrimSpace(string(c)), err
		},
		func() error {
			_, err := r.runMutating(ctx, root, mutateEnv(), "stash", "clear")
			return err
		}, nil, nil)
}

func itoaN(n int) string { return fmt.Sprintf("%d", n) }

// undoStashClear stores the stashes again, oldest first so the order is the
// one they had.
func (r *Runner) undoStashClear(ctx context.Context, root string, op Operation) error {
	if err := r.verifySnapshot(ctx, root, op); err != nil {
		return err
	}
	out, err := r.Read(ctx, root, "rev-list", "--parents", "-n", "1", op.Snapshot)
	if err != nil {
		return err
	}
	f := strings.Fields(string(out))
	if len(f) < 2 {
		return refusal("recover", "the recovery point holds no stash")
	}
	stashes := f[1:] // newest first, as `stash list` printed them
	for i := len(stashes) - 1; i >= 0; i-- {
		subject, err := r.Read(ctx, root, "log", "-1", "--format=%s", stashes[i])
		if err != nil {
			return err
		}
		if _, err := r.runMutating(ctx, root, mutateEnv(), "stash", "store", "-m", strings.TrimSpace(string(subject)), stashes[i]); err != nil {
			return err
		}
	}
	return nil
}
