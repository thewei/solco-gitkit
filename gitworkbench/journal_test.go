package gitworkbench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

/**
 * [INPUT]: journal.go、journal_work.go、真实临时仓库
 * [OUTPUT]: Safety Journal 第一批的合同：丢弃与删除未跟踪文件先留恢复点、找回只放回被丢的内容并保住索引与别的文件、现状变了就拒绝、记录只有名字没有内容、关闭时不留痕、超限拒绝执行、修剪、引用丢失拒绝
 * [POS]: 2026-11-12 决定第 (3) 条 Safety Journal 的防线；每条都做过变异检查
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

func journalRepo(t *testing.T) (string, *Runner) {
	root, r := tempRepo(t)
	r.Journal = true
	return root, r
}

func onlyOperation(t *testing.T, r *Runner, root string) Operation {
	t.Helper()
	ops, err := r.Operations(context.Background(), root)
	if err != nil || len(ops) != 1 {
		t.Fatalf("operations = %+v err=%v, want exactly one", ops, err)
	}
	return ops[0]
}

func TestDiscardKeepsARecoveryPointAndUndoRestoresOnlyTheWorktree(t *testing.T) {
	root, r := journalRepo(t)
	ctx := context.Background()
	undoCommit(t, root, "a.txt", "a\n", "add a")
	undoCommit(t, root, "other.txt", "other\n", "add other")

	// index holds "staged", worktree holds "dirty": a discard must lose only the latter.
	writeRepoFile(t, root, "a.txt", "staged\n")
	runGit(t, root, "add", "a.txt")
	writeRepoFile(t, root, "a.txt", "dirty\n")
	writeRepoFile(t, root, "other.txt", "other edited, not discarded\n")
	indexBefore := runGit(t, root, "ls-files", "-s")

	if err := r.Discard(ctx, root, []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, root, "a.txt"); got != "staged\n" {
		t.Fatalf("after discard a.txt = %q, want the staged content", got)
	}
	op := onlyOperation(t, r, root)
	if op.Kind != OpKindDiscard || op.Status != OpStatusSucceeded || !op.CanUndo() {
		t.Fatalf("record = %+v", op)
	}
	if got := strings.TrimSpace(runGit(t, root, "rev-parse", journalRefPrefix+op.ID)); got != op.Snapshot {
		t.Errorf("hidden ref = %s, record says %s", got, op.Snapshot)
	}

	if err := r.UndoOperation(ctx, root, op.ID); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, root, "a.txt"); got != "dirty\n" {
		t.Errorf("after recovery a.txt = %q, want the discarded edit", got)
	}
	if got := runGit(t, root, "ls-files", "-s"); got != indexBefore {
		t.Errorf("the index changed:\nbefore %s\nafter  %s", indexBefore, got)
	}
	if got := readRepoFile(t, root, "other.txt"); got != "other edited, not discarded\n" {
		t.Errorf("a path outside the discard changed: %q", got)
	}
	if after := onlyOperation(t, r, root); after.UndoneAt == 0 || after.CanUndo() {
		t.Errorf("a recovered entry must not recover twice: %+v", after)
	}
	if err := r.UndoOperation(ctx, root, op.ID); err == nil {
		t.Error("recovering the same entry twice must be refused")
	}
}

func TestDiscardOfADeletedFileIsUndoneByDeletingItAgain(t *testing.T) {
	root, r := journalRepo(t)
	ctx := context.Background()
	undoCommit(t, root, "gone.txt", "gone\n", "add gone")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	if err := r.Discard(ctx, root, []string{"gone.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal("the discard should have brought the file back")
	}
	if err := r.UndoOperation(ctx, root, onlyOperation(t, r, root).ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "gone.txt")); !os.IsNotExist(err) {
		t.Errorf("recovery must delete the file again (stat err = %v)", err)
	}
}

func TestUndoDiscardRefusesWhenTheFileChangedAgain(t *testing.T) {
	root, r := journalRepo(t)
	ctx := context.Background()
	writeRepoFile(t, root, "base.txt", "dirty\n")
	if err := r.Discard(ctx, root, []string{"base.txt"}); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, root, "base.txt", "new work\n")
	err := r.UndoOperation(ctx, root, onlyOperation(t, r, root).ID)
	if ge, ok := GitError(err); !ok || ge.Class != ErrStaleState {
		t.Fatalf("err = %v, want a stale-state refusal", err)
	}
	if got := readRepoFile(t, root, "base.txt"); got != "new work\n" {
		t.Errorf("a refused recovery must leave the file alone: %q", got)
	}
}

func TestDeleteUntrackedKeepsTheFilesAndUndoBringsThemBack(t *testing.T) {
	root, r := journalRepo(t)
	ctx := context.Background()
	writeRepoFile(t, root, "notes/todo item.txt", "one\n")
	writeRepoFile(t, root, "-dash.txt", "two\n")
	paths := []string{"notes/todo item.txt", "-dash.txt"}

	if err := r.DeleteUntracked(root, paths); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if _, err := os.Stat(filepath.Join(root, p)); !os.IsNotExist(err) {
			t.Fatalf("%s should be deleted", p)
		}
	}
	if err := r.UndoOperation(ctx, root, onlyOperation(t, r, root).ID); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, root, "notes/todo item.txt"); got != "one\n" {
		t.Errorf("notes/todo item.txt = %q", got)
	}
	if got := readRepoFile(t, root, "-dash.txt"); got != "two\n" {
		t.Errorf("-dash.txt = %q", got)
	}
	if got := runGit(t, root, "diff", "--cached", "--name-only"); strings.TrimSpace(got) != "" {
		t.Errorf("recovering untracked files must not stage them: %q", got)
	}
}

func TestUndoDeleteUntrackedRefusesToOverwrite(t *testing.T) {
	root, r := journalRepo(t)
	ctx := context.Background()
	writeRepoFile(t, root, "scratch.txt", "old\n")
	if err := r.DeleteUntracked(root, []string{"scratch.txt"}); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, root, "scratch.txt", "written since\n")
	err := r.UndoOperation(ctx, root, onlyOperation(t, r, root).ID)
	if ge, ok := GitError(err); !ok || ge.Class != ErrStaleState {
		t.Fatalf("err = %v, want a stale-state refusal", err)
	}
	if got := readRepoFile(t, root, "scratch.txt"); got != "written since\n" {
		t.Errorf("scratch.txt = %q", got)
	}
}

func TestJournalIsOffByDefault(t *testing.T) {
	root, r := tempRepo(t)
	writeRepoFile(t, root, "base.txt", "dirty\n")
	if err := r.Discard(context.Background(), root, []string{"base.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "solcogit")); !os.IsNotExist(err) {
		t.Errorf("a runner without Journal must leave nothing behind (stat err = %v)", err)
	}
	if got := runGit(t, root, "for-each-ref", "refs/solcogit"); strings.TrimSpace(got) != "" {
		t.Errorf("refs written while off: %q", got)
	}
}

func TestRecordsHoldNamesAndHashesNeverContent(t *testing.T) {
	root, r := journalRepo(t)
	writeRepoFile(t, root, "secret.txt", "TOP-SECRET-CONTENT\n")
	if err := r.DeleteUntracked(root, []string{"secret.txt"}); err != nil {
		t.Fatal(err)
	}
	op := onlyOperation(t, r, root)
	dir := filepath.Join(root, ".git", "solcogit", "journal")
	file := filepath.Join(dir, op.ID+".json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "TOP-SECRET") {
		t.Error("the journal record carries file content")
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o600 {
		t.Errorf("record mode = %v, want 0600", info.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
	// The pinned commit is the only place the content lives, and not on any branch.
	if got := runGit(t, root, "for-each-ref", "refs/heads", "refs/tags"); strings.Contains(got, op.Snapshot) {
		t.Error("the recovery point is reachable from a branch or tag")
	}
	if got := runGit(t, root, "show", op.Snapshot+":secret.txt"); got != "TOP-SECRET-CONTENT\n" {
		t.Errorf("snapshot content = %q", got)
	}
}

func TestCaptureLeavesTheUsersIndexAndWorktreeAlone(t *testing.T) {
	root, r := journalRepo(t)
	undoCommit(t, root, "a.txt", "a\n", "add a")
	writeRepoFile(t, root, "a.txt", "dirty\n")
	writeRepoFile(t, root, "new.txt", "new\n")
	before := runGit(t, root, "status", "--porcelain=v1") + runGit(t, root, "ls-files", "-s")
	if _, _, err := r.captureTracked(context.Background(), root, []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, root, "status", "--porcelain=v1") + runGit(t, root, "ls-files", "-s"); got != before {
		t.Errorf("capturing changed the repository:\nbefore %s\nafter  %s", before, got)
	}
	if got := readRepoFile(t, root, "a.txt"); got != "dirty\n" {
		t.Errorf("a.txt = %q", got)
	}
}

func TestOversizedDeletionIsRefusedAndNothingIsRemoved(t *testing.T) {
	root, r := journalRepo(t)
	big := filepath.Join(root, "big.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxRecoveryBytes + 1); err != nil { // sparse
		t.Fatal(err)
	}
	f.Close()
	err = r.DeleteUntracked(root, []string{"big.bin"})
	if ge, ok := GitError(err); !ok || ge.Class != ErrTooLarge {
		t.Fatalf("err = %v, want too_large", err)
	}
	if _, err := os.Stat(big); err != nil {
		t.Error("a refused deletion must not delete")
	}
	if ops, _ := r.Operations(context.Background(), root); len(ops) != 0 {
		t.Errorf("a refused operation left records: %+v", ops)
	}
}

func TestFailedDiscardLeavesNoOrphanedPendingRecord(t *testing.T) {
	root, r := journalRepo(t)
	ctx := context.Background()
	err := r.Discard(ctx, root, []string{"does-not-exist.txt"})
	if err == nil {
		t.Fatal("discarding an unknown path should fail")
	}
	for _, op := range mustOps(t, r, root) {
		if op.Status == OpStatusPending {
			t.Errorf("a pending record survived a finished operation: %+v", op)
		}
	}
}

func mustOps(t *testing.T, r *Runner, root string) []Operation {
	t.Helper()
	ops, err := r.Operations(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

func TestJournalKeepsTheNewestFiftyAndDropsTheirPins(t *testing.T) {
	root, r := journalRepo(t)
	base := time.Now()
	tick := 0
	r.Now = func() time.Time { tick++; return base.Add(time.Duration(tick) * time.Second) }
	var first Operation
	for i := 0; i < journalKeep+3; i++ {
		name := fmt.Sprintf("f%02d.txt", i)
		writeRepoFile(t, root, name, "x\n")
		if err := r.DeleteUntracked(root, []string{name}); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = mustOps(t, r, root)[0]
		}
	}
	ops := mustOps(t, r, root)
	if len(ops) != journalKeep {
		t.Fatalf("%d records kept, want %d", len(ops), journalKeep)
	}
	if got := strings.TrimSpace(runGit(t, root, "for-each-ref", journalRefPrefix+first.ID)); got != "" {
		t.Errorf("the oldest record's pin survived pruning: %q", got)
	}
	if got := strings.Count(strings.TrimSpace(runGit(t, root, "for-each-ref", "refs/solcogit/journal")), "\n") + 1; got != journalKeep {
		t.Errorf("%d pinned refs remain, want %d", got, journalKeep)
	}
}

func TestUndoRefusesWhenThePinIsGone(t *testing.T) {
	root, r := journalRepo(t)
	writeRepoFile(t, root, "x.txt", "x\n")
	if err := r.DeleteUntracked(root, []string{"x.txt"}); err != nil {
		t.Fatal(err)
	}
	op := onlyOperation(t, r, root)
	runGit(t, root, "update-ref", "-d", journalRefPrefix+op.ID)
	err := r.UndoOperation(context.Background(), root, op.ID)
	if ge, ok := GitError(err); !ok || ge.Class != ErrStaleState {
		t.Fatalf("err = %v, want a stale-state refusal", err)
	}
	if _, err := os.Stat(filepath.Join(root, "x.txt")); !os.IsNotExist(err) {
		t.Error("nothing may be written without the recovery point")
	}
}

func TestUndoRejectsMalformedIDs(t *testing.T) {
	root, r := journalRepo(t)
	for _, id := range []string{"", "../../x", "zz", strings.Repeat("g", 32), strings.Repeat("a", 31)} {
		if err := r.UndoOperation(context.Background(), root, id); err == nil {
			t.Errorf("id %q was accepted", id)
		}
	}
}

func TestAnOldPendingRecordShowsAsInterruptedAndStaysRecoverable(t *testing.T) {
	root, r := journalRepo(t)
	writeRepoFile(t, root, "x.txt", "x\n")
	if err := r.DeleteUntracked(root, []string{"x.txt"}); err != nil {
		t.Fatal(err)
	}
	op := onlyOperation(t, r, root)
	op.Status, op.CreatedAt = OpStatusPending, time.Now().Add(-time.Hour).UnixNano()
	if err := r.writeOperation(context.Background(), root, op); err != nil {
		t.Fatal(err)
	}
	got := onlyOperation(t, r, root)
	if got.Status != OpStatusInterrupted || !got.CanUndo() {
		t.Fatalf("record = %+v", got)
	}
	if err := r.UndoOperation(context.Background(), root, got.ID); err != nil {
		t.Fatal(err)
	}
	if readRepoFile(t, root, "x.txt") != "x\n" {
		t.Error("an interrupted entry should still recover")
	}
}

func TestJournalWorksInALinkedWorktree(t *testing.T) {
	root, r := journalRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, root, "worktree", "add", "-q", "-b", "side", wt)
	writeRepoFile(t, wt, "w.txt", "w\n")
	if err := r.DeleteUntracked(wt, []string{"w.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := r.UndoOperation(context.Background(), wt, onlyOperation(t, r, wt).ID); err != nil {
		t.Fatal(err)
	}
	if readRepoFile(t, wt, "w.txt") != "w\n" {
		t.Error("recovery in a linked worktree failed")
	}
}
