package gitworkbench

import (
	"context"
	"os"
	"strings"
	"testing"
)

/**
 * [INPUT]: stash_lines.go、真实临时仓库
 * [OUTPUT]: 行级 stash 的合同：只存选中的行（跨 hunk、只取一个 hunk 里的一行）、工作区只少这些行、真实索引不动、stash 是原生的（带 index 父提交、可 pop 回原样）；未跟踪、已暂存、行已被改动、合并进行中、没选行、越界路径都先拒、什么都没存没动；拿掉这一步走 Safety Journal 可退回
 * [POS]: 2026-11-12 决定第 (14) 条
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

func stashRig(t *testing.T) (root string, r *Runner, src CourierSource, edited string) {
	t.Helper()
	root, r = tempRepo(t)
	commitAll(t, root, "doc.txt", numberedLines(30, nil), "doc")
	hunks := courierTwoHunks(t, root)
	edited = readRepoFile(t, root, "doc.txt")
	picks := pickAll(1, hunks[1])
	picks[[2]int{0, changeLines(hunks[0])[0]}] = true // "inserted a" only
	return root, r, CourierSource{Path: "doc.txt", Hunks: hunks, Picks: picks}, edited
}

func stashCount(t *testing.T, root string) int {
	t.Helper()
	out := strings.TrimSpace(runGit(t, root, "stash", "list"))
	if out == "" {
		return 0
	}
	return len(strings.Split(out, "\n"))
}

func TestStashLinesStoresOnlyThePickedLinesAndTakesThemOut(t *testing.T) {
	ctx := context.Background()
	root, r, src, edited := stashRig(t)
	r.Journal = true

	if err := r.StashLines(ctx, root, src, "just these"); err != nil {
		t.Fatal(err)
	}
	got := readRepoFile(t, root, "doc.txt")
	if strings.Contains(got, "inserted a") || strings.Contains(got, "twenty-five") || !strings.Contains(got, "inserted b") || !strings.Contains(got, "line 25") {
		t.Errorf("working tree after the stash:\n%s", got)
	}
	if n := stashCount(t, root); n != 1 {
		t.Fatalf("stashes = %d, want 1", n)
	}
	if subject := strings.TrimSpace(runGit(t, root, "stash", "list", "--format=%gs")); !strings.Contains(subject, "just these") {
		t.Errorf("stash message = %q", subject)
	}
	shown := runGit(t, root, "stash", "show", "-p", "stash@{0}")
	if !strings.Contains(shown, "+inserted a") || !strings.Contains(shown, "+twenty-five") || strings.Contains(shown, "inserted b") {
		t.Errorf("the stash holds more or less than the picked lines:\n%s", shown)
	}
	if parents := strings.Fields(runGit(t, root, "rev-list", "--parents", "-n", "1", "stash@{0}")); len(parents) != 3 {
		t.Errorf("stash commit parents = %v, want HEAD and an index commit", parents)
	}
	if got := runGit(t, root, "diff", "--cached", "--name-only"); got != "" {
		t.Errorf("the real index changed: %q", got)
	}
	if got := status(t, root); got != " M doc.txt" {
		t.Errorf("status = %q", got)
	}
	// a native stash pops back: onto a clean file (Git's own rule for a pop)
	// it brings back exactly the picked lines
	runGit(t, root, "checkout", "-q", "--", "doc.txt")
	runGit(t, root, "stash", "pop", "-q")
	if got, want := readRepoFile(t, root, "doc.txt"), strings.Replace(edited, "inserted b\n", "", 1); got != want {
		t.Errorf("pop restored:\n%s\nwant:\n%s", got, want)
	}
	if n := stashCount(t, root); n != 0 {
		t.Errorf("stashes after pop = %d", n)
	}
}

func TestStashLinesRemovalIsOnTheJournal(t *testing.T) {
	ctx := context.Background()
	root, r, src, edited := stashRig(t)
	r.Journal = true
	if err := r.StashLines(ctx, root, src, ""); err != nil {
		t.Fatal(err)
	}
	if subject := strings.TrimSpace(runGit(t, root, "stash", "list", "--format=%gs")); !strings.Contains(subject, "Stashed lines of doc.txt") {
		t.Errorf("default message = %q", subject)
	}
	op := onlyOperation(t, r, root)
	if op.Kind != OpKindApplyPatch || !op.CanUndo() {
		t.Fatalf("record = %+v", op)
	}
	if err := r.UndoOperation(ctx, root, op.ID); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, root, "doc.txt"); got != edited {
		t.Errorf("undo left:\n%s", got)
	}
}

func TestStashLinesRefusesBeforeAnythingIsStored(t *testing.T) {
	ctx := context.Background()
	root, r, src, edited := stashRig(t)

	check := func(name string, s CourierSource, want string) {
		t.Helper()
		err := r.StashLines(ctx, root, s, "")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, want)
		}
		if n := stashCount(t, root); n != 0 {
			t.Errorf("%s: %d stashes stored", name, n)
		}
	}
	check("no picks", CourierSource{Path: "doc.txt", Hunks: src.Hunks, Picks: map[[2]int]bool{}}, "no changed lines")
	check("whole file", CourierSource{Path: "doc.txt", Hunks: src.Hunks}, "picked lines")
	check("a commit", CourierSource{Path: "doc.txt", Commit: strings.Repeat("a", 40), Hunks: src.Hunks, Picks: src.Picks}, "picked lines")
	check("outside the repository", CourierSource{Path: "../x.txt", Hunks: src.Hunks, Picks: src.Picks}, "picked lines")
	check("a rename", CourierSource{Path: "b.txt", OldPath: "doc.txt", Hunks: src.Hunks, Picks: src.Picks}, "picked lines")

	untracked := CourierSource{Path: "fresh.txt", Hunks: src.Hunks, Picks: src.Picks}
	writeRepoFile(t, root, "fresh.txt", "x\n")
	check("untracked", untracked, "not tracked")

	// the lines are no longer as they were picked
	writeRepoFile(t, root, "doc.txt", strings.Replace(edited, "twenty-five", "someone else", 1))
	check("changed under the pick", src, "changed since")
	if !strings.Contains(readRepoFile(t, root, "doc.txt"), "someone else") {
		t.Error("a refused stash changed the file")
	}

	// staged changes in the file
	writeRepoFile(t, root, "doc.txt", edited)
	runGit(t, root, "add", "doc.txt")
	check("staged", src, "staged changes")
	if got := readRepoFile(t, root, "doc.txt"); got != edited {
		t.Error("a refused stash changed the file")
	}
	runGit(t, root, "reset", "-q", "doc.txt")

	// a merge in the middle
	marker := strings.TrimSpace(runGit(t, root, "rev-parse", "--absolute-git-dir")) + "/MERGE_HEAD"
	writeRepoFile(t, "/", strings.TrimPrefix(marker, "/"), strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))+"\n")
	check("merge in progress", src, "merge")
}

func TestStashLinesWorksWithoutAnyIdentity(t *testing.T) {
	ctx := context.Background()
	root, r, src, _ := stashRig(t)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_COMMITTER_NAME", "")
	runGit(t, root, "config", "--local", "user.useConfigOnly", "true")
	runGit(t, root, "config", "--local", "--unset-all", "user.name")
	if err := r.StashLines(ctx, root, src, ""); err != nil {
		t.Fatalf("no identity: %v", err)
	}
	if n := stashCount(t, root); n != 1 {
		t.Errorf("stashes = %d", n)
	}
}

// The lines are stored first and taken out second; when taking them out fails
// after all, the stash is dropped again so no duplicate is left behind.
func TestStashLinesDropsItsStashWhenTheRemovalFails(t *testing.T) {
	ctx := context.Background()
	root, r, src, edited := stashRig(t)
	if err := os.Chmod(root, 0o555); err != nil { // git apply cannot replace doc.txt in a read-only folder
		t.Fatal(err)
	}
	defer os.Chmod(root, 0o755)
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if err := r.StashLines(ctx, root, src, ""); err == nil {
		t.Fatal("the stash succeeded although the lines could not be taken out")
	}
	_ = os.Chmod(root, 0o755)
	if n := stashCount(t, root); n != 0 {
		t.Errorf("%d stashes left behind by a failed stash", n)
	}
	if got := readRepoFile(t, root, "doc.txt"); got != edited {
		t.Error("the file changed although the removal failed")
	}
}
