package gitworkbench

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

/**
 * [INPUT]: reflog_undo.go、真实临时仓库（真实 git 写 reflog）
 * [OUTPUT]: undo / redo 的合同：提交（撤销留暂存、重做硬重置）、checkout、一次完整 rebase、多次撤销再重做保持平衡、标记写进 reflog（与 LazyGit 互认）、身份串不符时拒绝、进行中的合并/变基拒绝、重做时保住已改的跟踪文件
 * [POS]: 2026-11-12 决定第 (3) 条 undo / redo 的防线；每条都做过变异检查
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

func undoHead(t *testing.T, root string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
}

func undoCommit(t *testing.T, root, rel, text, msg string) string {
	t.Helper()
	writeRepoFile(t, root, rel, text)
	runGit(t, root, "add", rel)
	runGit(t, root, "commit", "-q", "-m", msg)
	return undoHead(t, root)
}

func TestUndoRedoCommit(t *testing.T) {
	root, r := tempRepo(t)
	ctx := context.Background()
	first := undoHead(t, root)
	second := undoCommit(t, root, "a.txt", "a\n", "add a")

	plan, ok, err := r.PlanReplay(ctx, root, false)
	if err != nil || !ok || plan.Kind != ReplayCommit || plan.From != first || plan.To != second {
		t.Fatalf("plan = %+v ok=%v err=%v", plan, ok, err)
	}
	if err := r.Replay(ctx, root, false, plan.Identity); err != nil {
		t.Fatal(err)
	}
	if got := undoHead(t, root); got != first {
		t.Fatalf("after undo HEAD = %s, want %s", got, first)
	}
	if !stagedPaths(t, root)["a.txt"] {
		t.Errorf("undoing a commit must leave its changes staged; staged = %v", stagedPaths(t, root))
	}
	if top := runGit(t, root, "reflog", "-1", "--format=%gs"); !strings.HasPrefix(top, reflogUndoMarker) {
		t.Errorf("the undo left %q at the top of the reflog, want the LazyGit marker", strings.TrimSpace(top))
	}

	redo, ok, err := r.PlanReplay(ctx, root, true)
	if err != nil || !ok || redo.To != second {
		t.Fatalf("redo plan = %+v ok=%v err=%v", redo, ok, err)
	}
	if err := r.Replay(ctx, root, true, redo.Identity); err != nil {
		t.Fatal(err)
	}
	if got := undoHead(t, root); got != second {
		t.Fatalf("after redo HEAD = %s, want %s", got, second)
	}
	if _, ok, _ := r.PlanReplay(ctx, root, true); ok {
		t.Error("nothing is left to redo")
	}
}

func TestUndoRedoStayBalancedAcrossSeveralActions(t *testing.T) {
	root, r := tempRepo(t)
	ctx := context.Background()
	c0 := undoHead(t, root)
	c1 := undoCommit(t, root, "a.txt", "a\n", "one")
	c2 := undoCommit(t, root, "b.txt", "b\n", "two")
	c3 := undoCommit(t, root, "c.txt", "c\n", "three")

	step := func(redo bool, want string) {
		t.Helper()
		if err := r.Replay(ctx, root, redo, ""); err != nil {
			t.Fatal(err)
		}
		if got := undoHead(t, root); got != want {
			t.Fatalf("HEAD = %s, want %s", got, want)
		}
	}
	runGit(t, root, "reset", "-q", "--hard", "HEAD") // the staged leftovers of an undo are in the way of the next one's checks
	step(false, c2)
	runGit(t, root, "reset", "-q", "--hard", "HEAD")
	step(false, c1)
	runGit(t, root, "reset", "-q", "--hard", "HEAD")
	step(true, c2)
	step(true, c3)
	if err := r.Replay(ctx, root, true, ""); err == nil {
		t.Error("a redo past the newest action succeeded")
	}
	step(false, c2)
	_ = c0
}

func TestUndoRedoCheckout(t *testing.T) {
	root, r := tempRepo(t)
	ctx := context.Background()
	runGit(t, root, "branch", "side")
	main := strings.TrimSpace(runGit(t, root, "rev-parse", "--abbrev-ref", "HEAD"))
	runGit(t, root, "checkout", "-q", "side")

	if err := r.Replay(ctx, root, false, ""); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGit(t, root, "rev-parse", "--abbrev-ref", "HEAD")); got != main {
		t.Fatalf("after undo on %q, want %q", got, main)
	}
	if err := r.Replay(ctx, root, true, ""); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGit(t, root, "rev-parse", "--abbrev-ref", "HEAD")); got != "side" {
		t.Fatalf("after redo on %q, want side", got)
	}
}

func TestUndoRebaseAsOneAction(t *testing.T) {
	root, r := tempRepo(t)
	ctx := context.Background()
	main := strings.TrimSpace(runGit(t, root, "rev-parse", "--abbrev-ref", "HEAD"))
	runGit(t, root, "checkout", "-q", "-b", "side")
	undoCommit(t, root, "side1.txt", "1\n", "side one")
	before := undoCommit(t, root, "side2.txt", "2\n", "side two")
	runGit(t, root, "checkout", "-q", main)
	undoCommit(t, root, "m.txt", "m\n", "main moves")
	runGit(t, root, "checkout", "-q", "side")
	runGit(t, root, "rebase", "-q", main)
	after := undoHead(t, root)
	if after == before {
		t.Fatal("the rebase did not move the branch")
	}

	plan, ok, err := r.PlanReplay(ctx, root, false)
	if err != nil || !ok || plan.Kind != ReplayRebase || plan.From != before || plan.To != after {
		t.Fatalf("plan = %+v ok=%v err=%v", plan, ok, err)
	}
	if err := r.Replay(ctx, root, false, plan.Identity); err != nil {
		t.Fatal(err)
	}
	if got := undoHead(t, root); got != before {
		t.Fatalf("one undo left HEAD at %s, want the pre-rebase %s", got, before)
	}
	if err := r.Replay(ctx, root, true, ""); err != nil {
		t.Fatal(err)
	}
	if got := undoHead(t, root); got != after {
		t.Fatalf("redo left HEAD at %s, want %s", got, after)
	}
}

func TestReplayRefusesWhenHistoryMovedSinceThePlan(t *testing.T) {
	root, r := tempRepo(t)
	ctx := context.Background()
	undoCommit(t, root, "a.txt", "a\n", "add a")
	plan, ok, _ := r.PlanReplay(ctx, root, false)
	if !ok {
		t.Fatal("no plan")
	}
	// Another client commits after the confirmation was drawn.
	head := undoCommit(t, root, "b.txt", "b\n", "add b")
	err := r.Replay(ctx, root, false, plan.Identity)
	ge, isGit := err.(*Error)
	if !isGit || ge.Class != ErrStaleState {
		t.Fatalf("err = %v, want a stale-state refusal", err)
	}
	if got := undoHead(t, root); got != head {
		t.Errorf("a refused undo moved HEAD to %s", got)
	}
}

func TestReplayNothingToUndo(t *testing.T) {
	root, r := tempRepo(t) // one root commit: it has no earlier commit to return to
	if _, ok, err := r.PlanReplay(context.Background(), root, false); err != nil || ok {
		t.Fatalf("plan ok=%v err=%v on a one-commit history", ok, err)
	}
	err := r.Replay(context.Background(), root, false, "")
	if ge, ok := err.(*Error); !ok || ge.Class != ErrRefusal {
		t.Fatalf("err = %v, want a refusal", err)
	}
}

func TestReplayRefusesInTheMiddleOfAMerge(t *testing.T) {
	root, r := tempRepo(t)
	ctx := context.Background()
	main := strings.TrimSpace(runGit(t, root, "rev-parse", "--abbrev-ref", "HEAD"))
	runGit(t, root, "checkout", "-q", "-b", "side")
	undoCommit(t, root, "base.txt", "side\n", "side edit")
	runGit(t, root, "checkout", "-q", main)
	undoCommit(t, root, "base.txt", "main\n", "main edit")
	merge := exec.Command("git", "merge", "side") // conflicts on purpose
	merge.Dir = root
	_ = merge.Run()
	err := r.Replay(ctx, root, false, "")
	if ge, ok := err.(*Error); !ok || ge.Class != ErrMergeInProgess {
		t.Fatalf("err = %v, want a merge-in-progress refusal", err)
	}
}

// A redo is a hard reset; the tracked change made in between survives it.
func TestRedoKeepsTrackedWorkInProgress(t *testing.T) {
	root, r := tempRepo(t)
	ctx := context.Background()
	undoCommit(t, root, "a.txt", "a\n", "add a")
	if err := r.Replay(ctx, root, false, ""); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "reset", "-q", "--hard", "HEAD")
	writeRepoFile(t, root, "base.txt", "edited meanwhile\n")
	if err := r.Replay(ctx, root, true, ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "base.txt"))
	if err != nil || string(data) != "edited meanwhile\n" {
		t.Fatalf("base.txt = %q, %v: the redo lost the edit", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Errorf("the redo did not bring the commit back: %v", err)
	}
	if strings.TrimSpace(runGit(t, root, "stash", "list")) != "" {
		t.Error("the protective stash was left behind")
	}
}
