package gitworkbench

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

/**
 * [INPUT]: Runner（Journal 开关、Read / run / LockRepo）、仓库的 git 目录
 * [OUTPUT]: Operation（一条可找回的破坏性操作：种类、涉及的路径、快照提交、状态）、Operations（读日志，新的在前）、UndoOperation（按记录找回）、记录的落盘与修剪
 * [POS]: 2026-11-12 决定第 (3) 条 Safety Journal：丢弃改动 / 删除未跟踪文件 / 删分支 / drop stash 这类 reflog 里没有的操作，执行前先把会丢的东西钉成一个提交（隐藏引用 refs/solcogit/journal/<id>），再写一条记录到 <git 目录>/solcogit/journal/<id>.json；找回前重新校验现状，变了就拒绝。TanGit 的 journal.go（MIT）的取舍版：记录路径名，不记内容；快照限在被操作的路径上，不是整个工作区
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 *
 * 默认关闭：Runner.Journal 为 true 时 Discard / DeleteUntracked / DeleteBranch / StashDrop 才记录（Solco Git 打开；Solco 与手机 Host 不变）。
 * 记录与引用都在仓库自己的 git 目录里，随仓库走、不进版本库；refs/solcogit/ 不在 refs/heads、refs/tags 之下，普通 push 不会带出去。
 * 记录失败即拒绝（fail closed）：捕获不了恢复点，破坏性操作就不执行。
 */

// Operation kinds and states.
const (
	OpKindDiscard         = "discard"
	OpKindDeleteUntracked = "delete-untracked"
	OpKindBranchDelete    = "branch-delete"
	OpKindStashDrop       = "stash-drop"

	OpStatusPending     = "pending"
	OpStatusSucceeded   = "succeeded"
	OpStatusFailed      = "failed"
	OpStatusInterrupted = "interrupted"
)

const (
	journalVersion    = 1
	journalRefPrefix  = "refs/solcogit/journal/"
	journalKeep       = 50
	journalLifetime   = 14 * 24 * time.Hour
	journalPendingTTL = 10 * time.Minute
	// MaxRecoveryBytes bounds what one recovery point keeps: past it the
	// destructive operation is refused instead of running with nothing to
	// come back to.
	MaxRecoveryBytes = 128 << 20
	// maxJournalPaths bounds the names one record keeps.
	maxJournalPaths = 500
)

// Operation is one destructive action and what a later recovery must
// revalidate. It holds names and object ids, never file content.
type Operation struct {
	Version    int      `json:"version"`
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Status     string   `json:"status"`
	CreatedAt  int64    `json:"created_at_ns"` // Unix nanoseconds: two operations in one second still sort
	FinishedAt int64    `json:"finished_at_ns,omitempty"`
	UndoneAt   int64    `json:"undone_at_ns,omitempty"`
	Paths      []string `json:"paths,omitempty"`
	// Removed are tracked paths that were already deleted in the working tree
	// when a discard brought them back: recovering takes them away again.
	Removed []string `json:"removed,omitempty"`
	// Snapshot is the pinned commit that holds what the operation destroyed
	// (a tree of the affected paths; for a branch, its tip; for a stash, the
	// stash commit).
	Snapshot string `json:"snapshot,omitempty"`
	// Name is the branch's or stash's own name, for the kinds that have one.
	Name  string `json:"name,omitempty"`
	Error string `json:"error,omitempty"`
}

// Created is when the operation started.
func (o Operation) Created() time.Time { return time.Unix(0, o.CreatedAt) }

// CanUndo is a hint for the UI: the preflight in UndoOperation is the authority.
func (o Operation) CanUndo() bool {
	// A failed discard may have rewritten some files before it stopped, so it
	// stays recoverable; a failed branch delete or stash drop destroyed nothing.
	work := o.Kind == OpKindDiscard || o.Kind == OpKindDeleteUntracked
	ok := o.Status == OpStatusSucceeded || o.Status == OpStatusInterrupted || (o.Status == OpStatusFailed && work)
	return ok && o.UndoneAt == 0 && o.Snapshot != ""
}

func validOperationID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func newOperationID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// journalDir is where the records live: in the worktree's own git directory.
func (r *Runner) journalDir(ctx context.Context, root string) (string, error) {
	out, err := r.Read(ctx, root, "rev-parse", "--git-path", "solcogit/journal")
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return "", errors.New("Git did not resolve the journal directory")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	return dir, nil
}

func (r *Runner) writeOperation(ctx context.Context, root string, op Operation) error {
	dir, err := r.journalDir(ctx, root)
	if err != nil {
		return err
	}
	if !validOperationID(op.ID) {
		return errors.New("invalid journal operation id")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the safety journal: %w", err)
	}
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, op.ID+".json"))
}

func (r *Runner) readOperation(ctx context.Context, root, id string) (Operation, error) {
	if !validOperationID(id) {
		return Operation{}, refusal("recover", "that is not a journal entry")
	}
	dir, err := r.journalDir(ctx, root)
	if err != nil {
		return Operation{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return Operation{}, &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "recover", err: errors.New("that entry is gone")}
		}
		return Operation{}, err
	}
	var op Operation
	if err := json.Unmarshal(data, &op); err != nil || op.ID != id || op.Version != journalVersion {
		return Operation{}, refusal("recover", "that journal entry is not readable")
	}
	// A record left pending far longer than any operation runs was cut short
	// (the app quit mid-way): the recovery point exists, so it stays usable.
	if op.Status == OpStatusPending && r.now().Sub(op.Created()) > journalPendingTTL {
		op.Status = OpStatusInterrupted
	}
	return op, nil
}

// Operations lists the journal, newest first. A pending record that has been
// pending far longer than any operation runs is shown as interrupted. Records
// past the keep count or lifetime are pruned (with their pinned commits).
func (r *Runner) Operations(ctx context.Context, root string) ([]Operation, error) {
	dir, err := r.journalDir(ctx, root)
	if err != nil {
		return nil, err
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ops []Operation
	for _, f := range files {
		id := strings.TrimSuffix(f.Name(), ".json")
		if f.IsDir() || id == f.Name() || !validOperationID(id) {
			continue
		}
		op, err := r.readOperation(ctx, root, id)
		if err != nil {
			continue
		}
		ops = append(ops, op)
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].CreatedAt > ops[j].CreatedAt })
	return ops, nil
}

// pruneJournal drops the records beyond the keep count or lifetime. Called
// with the repository lock held, after a record is finished.
func (r *Runner) pruneJournal(ctx context.Context, root string) {
	ops, err := r.Operations(ctx, root)
	if err != nil {
		return
	}
	dir, err := r.journalDir(ctx, root)
	if err != nil {
		return
	}
	now := r.now()
	for i, op := range ops {
		old := now.Sub(op.Created()) > journalLifetime
		if (i < journalKeep && !old) || op.Status == OpStatusPending {
			continue
		}
		_, _ = r.runMutating(ctx, root, mutateEnv(), "update-ref", "-d", journalRefPrefix+op.ID)
		_ = os.Remove(filepath.Join(dir, op.ID+".json"))
	}
}

// journalBegin records a pending operation and pins its snapshot. The caller
// holds the repository lock. A failure here means nothing was destroyed yet.
func (r *Runner) journalBegin(ctx context.Context, root string, op Operation) (Operation, error) {
	id, err := newOperationID()
	if err != nil {
		return op, err
	}
	op.Version, op.ID, op.Status, op.CreatedAt = journalVersion, id, OpStatusPending, r.now().UnixNano()
	if len(op.Paths) > maxJournalPaths {
		op.Paths = op.Paths[:maxJournalPaths]
	}
	if op.Snapshot != "" {
		if _, err := r.runMutating(ctx, root, mutateEnv(), "update-ref", "-m", "solco-git journal "+op.Kind, journalRefPrefix+id, op.Snapshot); err != nil {
			return op, fmt.Errorf("keep a recovery point: %w", err)
		}
	}
	if err := r.writeOperation(ctx, root, op); err != nil {
		_, _ = r.runMutating(ctx, root, mutateEnv(), "update-ref", "-d", journalRefPrefix+id)
		return op, fmt.Errorf("write the safety journal: %w", err)
	}
	return op, nil
}

// journalFinish settles a pending record with the outcome of the action. A
// failed action leaves nothing to recover, so its pin goes with it.
func (r *Runner) journalFinish(ctx context.Context, root string, op Operation, actionErr error) {
	op.FinishedAt = r.now().UnixNano()
	if actionErr == nil {
		op.Status = OpStatusSucceeded
	} else {
		op.Status, op.Error = OpStatusFailed, fmt.Sprintf("%v", actionErr)
		if len(op.Error) > 300 {
			op.Error = op.Error[:300]
		}
	}
	_ = r.writeOperation(ctx, root, op)
	if actionErr != nil && op.Kind != OpKindDiscard && op.Kind != OpKindDeleteUntracked {
		// A failed branch delete or stash drop destroyed nothing.
		dir, _ := r.journalDir(ctx, root)
		_, _ = r.runMutating(ctx, root, mutateEnv(), "update-ref", "-d", journalRefPrefix+op.ID)
		_ = os.Remove(filepath.Join(dir, op.ID+".json"))
	}
	r.pruneJournal(ctx, root)
}

func (r *Runner) markUndone(ctx context.Context, root string, op Operation) error {
	op.UndoneAt = r.now().UnixNano()
	return r.writeOperation(ctx, root, op)
}
