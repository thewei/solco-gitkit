package gitworkbench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/**
 * [INPUT]: courier.go、真实临时仓库与它的第二个 worktree
 * [OUTPUT]: Change Courier 内核的合同：整文件（含未跟踪、改名）与按行（多 hunk 偏移、提交来源）的补丁送到另一个 worktree 只进工作区、不暂存、源不变；预检不干净（内容对不上、目标在合并中）就一个字节不动并说明原因；送达与 Move 的丢弃行都有可退回的恢复点；合并提交、改名后按行、没选行、越界路径被拒
 * [POS]: 2026-11-12 决定第 (13) 条；界面一侧在 solco-git internal/nativeui/git_courier_ui_test.go
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// courierRig: a repository with doc.txt (30 lines) committed and a second
// worktree "dest" on its own branch at the same commit.
func courierRig(t *testing.T) (root, dest string, r *Runner) {
	t.Helper()
	root, r = tempRepo(t)
	commitAll(t, root, "doc.txt", numberedLines(30, nil), "doc")
	dest = filepath.Join(t.TempDir(), "dest")
	runGit(t, root, "worktree", "add", "-q", "-b", "dest", dest)
	return root, dest, r
}

func status(t *testing.T, root string) string {
	t.Helper()
	return strings.TrimRight(runGit(t, root, "status", "--porcelain"), "\n")
}

func TestCourierSendsAWholeTrackedFileToAnotherWorktree(t *testing.T) {
	ctx := context.Background()
	root, dest, r := courierRig(t)
	writeRepoFile(t, root, "doc.txt", numberedLines(30, map[int]string{3: "three", 25: "twenty-five"}))
	runGit(t, root, "add", "doc.txt") // staged changes travel too: the file's whole change against HEAD
	writeRepoFile(t, root, "doc.txt", numberedLines(30, map[int]string{3: "three", 25: "twenty-five", 28: "28!"}))
	want := readRepoFile(t, root, "doc.txt")

	patch, err := r.CourierPatch(ctx, root, CourierSource{Path: "doc.txt"})
	if err != nil {
		t.Fatal(err)
	}
	pre, err := r.PreviewCourier(ctx, dest, patch)
	if err != nil || !pre.Clean || len(pre.Files) != 1 || pre.Files[0] != (CourierFile{Path: "doc.txt", Added: 3, Removed: 3}) {
		t.Fatalf("preview = %+v err=%v", pre, err)
	}
	if got := readRepoFile(t, dest, "doc.txt"); got != numberedLines(30, nil) {
		t.Fatal("the preview changed the destination")
	}
	if err := r.DeliverCourier(ctx, dest, patch); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, dest, "doc.txt"); got != want {
		t.Errorf("destination doc.txt differs from the source:\n%s", got)
	}
	if got := status(t, dest); got != " M doc.txt" {
		t.Errorf("destination status = %q, want an unstaged change", got)
	}
	if got := status(t, root); got != "MM doc.txt" {
		t.Errorf("the source changed: %q", got)
	}
}

func TestCourierSendsAnUntrackedAndARenamedFile(t *testing.T) {
	ctx := context.Background()
	root, dest, r := courierRig(t)
	writeRepoFile(t, root, "fresh.txt", "brand new\n")
	patch, err := r.CourierPatch(ctx, root, CourierSource{Path: "fresh.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DeliverCourier(ctx, dest, patch); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, dest, "fresh.txt"); got != "brand new\n" {
		t.Errorf("fresh.txt = %q", got)
	}
	if got := status(t, dest); got != "?? fresh.txt" {
		t.Errorf("destination status = %q, want an untracked file", got)
	}

	runGit(t, root, "mv", "doc.txt", "renamed.txt")
	patch, err = r.CourierPatch(ctx, root, CourierSource{Path: "renamed.txt", OldPath: "doc.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patch, "rename from doc.txt") {
		t.Fatalf("a rename must travel as one:\n%s", patch)
	}
	if err := r.DeliverCourier(ctx, dest, patch); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "doc.txt")); err == nil {
		t.Error("doc.txt is still in the destination")
	}
	if got := readRepoFile(t, dest, "renamed.txt"); got != numberedLines(30, nil) {
		t.Errorf("renamed.txt = %q", got)
	}
}

// courierTwoHunks edits doc.txt in two places far apart: two lines are inserted after
// line 3 and line 25 is changed, so the second hunk sits two lines further
// down in the source than in a destination without the insertion.
func courierTwoHunks(t *testing.T, root string) []Hunk {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= 30; i++ {
		switch i {
		case 25:
			b.WriteString("twenty-five\n")
		default:
			b.WriteString("line " + itoa(i) + "\n")
		}
		if i == 3 {
			b.WriteString("inserted a\ninserted b\n")
		}
	}
	writeRepoFile(t, root, "doc.txt", b.String())
	hunks := hunksOf(t, root, false, "doc.txt")
	if len(hunks) != 2 {
		t.Fatalf("hunks = %d, want 2", len(hunks))
	}
	return hunks
}

func pickAll(hunk int, h Hunk) map[[2]int]bool {
	out := map[[2]int]bool{}
	for _, i := range changeLines(h) {
		out[[2]int{hunk, i}] = true
	}
	return out
}

func TestCourierSendsPickedLinesAcrossHunksWithTheRightOffsets(t *testing.T) {
	ctx := context.Background()
	root, dest, r := courierRig(t)
	hunks := courierTwoHunks(t, root)
	picks := pickAll(0, hunks[0]) // both insertions of the first hunk …
	for k := range pickAll(1, hunks[1]) {
		picks[k] = true // … and the change in the second
	}
	patch, err := r.CourierPatch(ctx, root, CourierSource{Path: "doc.txt", Hunks: hunks, Picks: picks})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DeliverCourier(ctx, dest, patch); err != nil {
		t.Fatalf("%v\n%s", err, patch)
	}
	if got, want := readRepoFile(t, dest, "doc.txt"), readRepoFile(t, root, "doc.txt"); got != want {
		t.Errorf("destination =\n%s\nwant the source's file", got)
	}

	// only some lines: the first insertion and not the second, nor the change
	dest2 := filepath.Join(t.TempDir(), "dest2")
	runGit(t, root, "worktree", "add", "-q", "-b", "dest2", dest2)
	var first int
	for _, i := range changeLines(hunks[0]) {
		first = i
		break
	}
	patch, err = r.CourierPatch(ctx, root, CourierSource{Path: "doc.txt", Hunks: hunks, Picks: map[[2]int]bool{{0, first}: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DeliverCourier(ctx, dest2, patch); err != nil {
		t.Fatal(err)
	}
	got := readRepoFile(t, dest2, "doc.txt")
	if !strings.Contains(got, "inserted a\n") || strings.Contains(got, "inserted b") || strings.Contains(got, "twenty-five") {
		t.Errorf("destination = %q, want just the first inserted line", got)
	}
	// the source is untouched by sending
	if status(t, root) != " M doc.txt" {
		t.Errorf("the source changed: %q", status(t, root))
	}
}

func TestCourierSendsLinesOfACommit(t *testing.T) {
	ctx := context.Background()
	root, dest, r := courierRig(t) // dest is at the commit before the next one
	hunks := courierTwoHunks(t, root)
	runGit(t, root, "commit", "-qam", "edit doc")
	commit := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	// the commit's own diff, the one a review of it shows
	fp := parsePatch([]byte(runGit(t, root, "diff", "--no-color", "-U1", "HEAD~1", "HEAD", "--", "doc.txt")))
	if fp == nil || len(fp.hunks) != len(hunks) {
		t.Fatal("the commit's diff is not the one just made")
	}
	patch, err := r.CourierPatch(ctx, root, CourierSource{Path: "doc.txt", Commit: commit, Hunks: fp.hunks, Picks: pickAll(1, fp.hunks[1])})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DeliverCourier(ctx, dest, patch); err != nil {
		t.Fatalf("%v\n%s", err, patch)
	}
	got := readRepoFile(t, dest, "doc.txt")
	if !strings.Contains(got, "twenty-five") || strings.Contains(got, "inserted") {
		t.Errorf("destination = %q, want only the second hunk of the commit", got)
	}
	if head := strings.TrimSpace(runGit(t, dest, "rev-parse", "HEAD")); head == commit {
		t.Error("a delivery must not move the destination's branch")
	}

	// the whole file of that commit
	whole, err := r.CourierPatch(ctx, root, CourierSource{Path: "doc.txt", Commit: commit})
	if err != nil || !strings.Contains(whole, "inserted a") || !strings.Contains(whole, "twenty-five") {
		t.Errorf("whole-file patch of the commit: err=%v\n%s", err, whole)
	}
}

func TestCourierPreviewRefusesWhatDoesNotFit(t *testing.T) {
	ctx := context.Background()
	root, dest, r := courierRig(t)
	writeRepoFile(t, root, "doc.txt", numberedLines(30, map[int]string{3: "three"}))
	patch, err := r.CourierPatch(ctx, root, CourierSource{Path: "doc.txt"})
	if err != nil {
		t.Fatal(err)
	}
	other := numberedLines(30, map[int]string{3: "something else"})
	writeRepoFile(t, dest, "doc.txt", other)

	pre, err := r.PreviewCourier(ctx, dest, patch)
	if err != nil || pre.Clean || !strings.Contains(pre.Problem, "doc.txt") {
		t.Fatalf("preview = %+v err=%v, want a refusal naming doc.txt", pre, err)
	}
	if err := r.DeliverCourier(ctx, dest, patch); err == nil {
		t.Fatal("a patch that does not fit was delivered")
	}
	if got := readRepoFile(t, dest, "doc.txt"); got != other {
		t.Error("a refused delivery changed the destination")
	}

	// a destination in the middle of a merge is not a quiet working tree
	runGit(t, dest, "checkout", "-q", "--", "doc.txt")
	marker := strings.TrimSpace(runGit(t, dest, "rev-parse", "--git-path", "MERGE_HEAD"))
	if !filepath.IsAbs(marker) {
		marker = filepath.Join(dest, marker)
	}
	if err := os.WriteFile(marker, []byte(strings.TrimSpace(runGit(t, dest, "rev-parse", "HEAD"))+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pre, err = r.PreviewCourier(ctx, dest, patch)
	if err != nil || pre.Clean || !strings.Contains(pre.Problem, "merge") {
		t.Fatalf("preview in a merge = %+v err=%v", pre, err)
	}
	if err := r.DeliverCourier(ctx, dest, patch); err == nil {
		t.Error("a patch was delivered into a worktree in the middle of a merge")
	}
	if got := readRepoFile(t, dest, "doc.txt"); got != numberedLines(30, nil) {
		t.Error("a refused delivery changed the destination")
	}
}

// The start of every hunk is where the side it is applied to has it: the
// second hunk of the forward patch sits two lines lower once the first
// hunk's two insertions are in, and the reverse patch names the second hunk
// where the working tree has it.
func TestCourierHunkStartsFollowTheLinesBeforeThem(t *testing.T) {
	root, _, _ := courierRig(t)
	hunks := courierTwoHunks(t, root)
	picks := pickAll(0, hunks[0])
	for k := range pickAll(1, hunks[1]) {
		picks[k] = true
	}
	src := CourierSource{Path: "doc.txt", Hunks: hunks, Picks: picks}
	heads := func(text string) []string {
		var out []string
		for _, l := range strings.Split(text, "\n") {
			if strings.HasPrefix(l, "@@") {
				out = append(out, l)
			}
		}
		return out
	}
	fwd, _ := courierLines("doc.txt", src, false)
	h := heads(fwd)
	if len(h) != 2 || !strings.HasPrefix(h[1], "@@ -"+itoa(hunks[1].OldStart)+",") || !strings.Contains(h[1], " +"+itoa(hunks[1].OldStart+2)+",") {
		t.Errorf("forward heads = %v, want the second hunk two lines lower on the new side", h)
	}
	// only one of the two insertions is taken out: the other stays in the
	// working tree, so the - side of the second hunk is one line, not two, above
	part := map[[2]int]bool{{0, changeLines(hunks[0])[0]}: true}
	for k := range pickAll(1, hunks[1]) {
		part[k] = true
	}
	rev, _ := courierLines("doc.txt", CourierSource{Path: "doc.txt", Hunks: hunks, Picks: part}, true)
	h = heads(rev)
	if len(h) != 2 || !strings.Contains(h[1], " +"+itoa(hunks[1].NewStart)+",") || !strings.HasPrefix(h[1], "@@ -"+itoa(hunks[1].NewStart-1)+",") {
		t.Errorf("reverse heads = %v, want the working tree's own start on the + side and one line less on the - side", h)
	}
}

func TestCourierRefusesWhatCannotBeSent(t *testing.T) {
	ctx := context.Background()
	root, _, r := courierRig(t)
	runGit(t, root, "checkout", "-q", "-b", "side")
	commitAll(t, root, "s.txt", "s\n", "side")
	runGit(t, root, "checkout", "-q", "-")
	runGit(t, root, "merge", "-q", "--no-ff", "-m", "merge side", "side")
	merge := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	hunks := []Hunk{{OldStart: 1, NewStart: 1, Lines: []DiffLine{{Kind: KindAdd, Text: "x"}}}}

	cases := []struct {
		name string
		src  CourierSource
	}{
		{"merge commit", CourierSource{Path: "s.txt", Commit: merge}},
		{"outside the repository", CourierSource{Path: "../escape.txt"}},
		{"renamed with lines", CourierSource{Path: "b.txt", OldPath: "a.txt", Hunks: hunks, Picks: map[[2]int]bool{{0, 0}: true}}},
		{"no line picked", CourierSource{Path: "doc.txt", Hunks: hunks, Picks: map[[2]int]bool{}}},
		{"a short hash", CourierSource{Path: "doc.txt", Commit: "abc"}},
		{"nothing changed", CourierSource{Path: "doc.txt"}},
	}
	for _, c := range cases {
		if _, err := r.CourierPatch(ctx, root, c.src); err == nil {
			t.Errorf("%s: sent", c.name)
		}
	}
	if _, err := r.PreviewCourier(ctx, root, "  \n"); err == nil {
		t.Error("an empty patch was previewed")
	}
}

func TestCourierDeliveryAndMoveKeepRecoveryPoints(t *testing.T) {
	ctx := context.Background()
	root, dest, r := courierRig(t)
	r.Journal = true
	hunks := courierTwoHunks(t, root)
	picks := pickAll(1, hunks[1])
	src := CourierSource{Path: "doc.txt", Hunks: hunks, Picks: picks}
	patch, err := r.CourierPatch(ctx, root, src)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DeliverCourier(ctx, dest, patch); err != nil {
		t.Fatal(err)
	}
	op := onlyOperation(t, r, dest)
	if op.Kind != OpKindApplyPatch || !op.CanUndo() {
		t.Fatalf("the delivery's record = %+v", op)
	}
	// Move: the same lines leave the source, and only those
	before := readRepoFile(t, root, "doc.txt")
	if err := r.DiscardCourierLines(ctx, root, src); err != nil {
		t.Fatal(err)
	}
	got := readRepoFile(t, root, "doc.txt")
	if strings.Contains(got, "twenty-five") || !strings.Contains(got, "inserted a") || !strings.Contains(got, "line 25") {
		t.Errorf("source after the move = %q", got)
	}
	ops, _ := r.Operations(ctx, root)
	if len(ops) != 1 || !ops[0].CanUndo() {
		t.Fatalf("the move's record = %+v", ops)
	}
	if err := r.UndoOperation(ctx, root, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, root, "doc.txt"); got != before {
		t.Errorf("undoing the move left %q", got)
	}
	// and the delivery is taken back in the destination
	if err := r.UndoOperation(ctx, dest, op.ID); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, dest, "doc.txt"); got != numberedLines(30, nil) {
		t.Errorf("undoing the delivery left %q", got)
	}

	// a source whose lines are no longer as they were read refuses the move
	writeRepoFile(t, root, "doc.txt", numberedLines(30, map[int]string{25: "someone else"}))
	if err := r.DiscardCourierLines(ctx, root, src); err == nil {
		t.Error("the lines moved although the source changed under them")
	}
	if err := r.DiscardCourierLines(ctx, root, CourierSource{Path: "doc.txt", Commit: strings.Repeat("a", 40), Picks: picks}); err == nil {
		t.Error("a commit's lines cannot be discarded from the working tree")
	}
}
