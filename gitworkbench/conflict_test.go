package gitworkbench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/**
 * [INPUT]: conflict.go、真实临时仓库里的真实合并冲突
 * [OUTPUT]: 冲突编辑内核的合同：三方内容与按块切分（含两边都没冲突的改动已并入文本）、Markdown 的 ======= 行不撞标记、删除/二进制/不在冲突中的文件如实说明、BuildConflictResult 的各选择与未决标记、ResolveWith 拒绝残留标记并暂存、恢复点可重开冲突且拒绝文件被改过或合并已推进
 * [POS]: 2026-11-12 决定第 (3) 条冲突三栏编辑的防线；界面一侧在 solco-git；每条都做过变异检查
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

const conflictBase = "line a\nline b\nline c\nline d\nline e\nline f\nline g\nline h\nline i\nline j\n"

// conflictRepo leaves a repository paused in a merge where f.txt has two
// conflicting hunks (a and i) and one change only the incoming side made (e).
func conflictRepo(t *testing.T) (string, *Runner) {
	root, r := tempRepo(t)
	main := strings.TrimSpace(runGit(t, root, "branch", "--show-current"))
	undoCommit(t, root, "f.txt", conflictBase, "add f")
	runGit(t, root, "switch", "-q", "-c", "topic")
	undoCommit(t, root, "f.txt", strings.NewReplacer("line a", "topic a", "line e", "topic e", "line i", "topic i").Replace(conflictBase), "topic edits")
	runGit(t, root, "switch", "-q", main)
	undoCommit(t, root, "f.txt", strings.NewReplacer("line a", "main a", "line i", "main i").Replace(conflictBase), "main edits")
	if out, err := runGitAt(root, "merge", "topic"); err == nil {
		t.Fatalf("expected a conflict: %s", out)
	}
	return root, r
}

func conflictHunksOf(segs []ConflictSegment) []*ConflictHunk {
	var out []*ConflictHunk
	for _, s := range segs {
		if s.Hunk != nil {
			out = append(out, s.Hunk)
		}
	}
	return out
}

func TestReadConflictCutsTheMergeIntoHunks(t *testing.T) {
	root, r := conflictRepo(t)
	sides, err := r.ReadConflict(context.Background(), root, "f.txt")
	if err != nil || !sides.Editable {
		t.Fatalf("sides = %+v err=%v", sides, err)
	}
	hunks := conflictHunksOf(sides.Segments)
	if len(hunks) != 2 {
		t.Fatalf("%d hunks, want 2: %+v", len(hunks), sides.Segments)
	}
	if hunks[0].Ours != "main a\n" || hunks[0].Theirs != "topic a\n" || hunks[0].Base != "line a\n" {
		t.Errorf("first hunk = %+v", hunks[0])
	}
	if hunks[1].Ours != "main i\n" || hunks[1].Theirs != "topic i\n" {
		t.Errorf("second hunk = %+v", hunks[1])
	}
	whole := BuildConflictResult(sides.Segments, []HunkChoice{ChoiceOurs, ChoiceOurs})
	if !strings.Contains(whole, "topic e\n") {
		t.Error("a change only the incoming side made must already be in the text")
	}
	if want := strings.NewReplacer("line a", "main a", "line e", "topic e", "line i", "main i").Replace(conflictBase); whole != want {
		t.Errorf("all-ours result:\n%s\nwant:\n%s", whole, want)
	}
	if sides.Base != conflictBase || !strings.HasPrefix(sides.Ours, "main a") || !strings.HasPrefix(sides.Theirs, "topic a") {
		t.Error("the three versions are not the index stages")
	}
}

func TestBuildConflictResultChoices(t *testing.T) {
	segs := []ConflictSegment{
		{Text: "head\n"},
		{Hunk: &ConflictHunk{Ours: "ours\n", Base: "base\n", Theirs: "theirs"}},
		{Text: "\ntail\n"},
	}
	cases := map[string]struct {
		c    HunkChoice
		want string
	}{
		"ours":   {ChoiceOurs, "head\nours\n\ntail\n"},
		"theirs": {ChoiceTheirs, "head\ntheirs\ntail\n"},
		"both":   {ChoiceBoth, "head\nours\ntheirs\ntail\n"},
		"base":   {ChoiceBase, "head\nbase\n\ntail\n"},
		"none":   {ChoiceNone, "head\n<<<<<<< Current\nours\n=======\ntheirs\n>>>>>>> Incoming\n\ntail\n"},
	}
	// a side whose last line has no newline still joins on its own line
	if got := BuildConflictResult([]ConflictSegment{{Hunk: &ConflictHunk{Ours: "o", Theirs: "t\n"}}}, []HunkChoice{ChoiceBoth}); got != "o\nt\n" {
		t.Errorf("both without a trailing newline: %q", got)
	}
	for name, tc := range cases {
		if got := BuildConflictResult(segs, []HunkChoice{tc.c}); got != tc.want {
			t.Errorf("%s: got %q want %q", name, got, tc.want)
		}
	}
	if got := BuildConflictResult(segs, nil); !hasConflictMarkers([]byte(got)) {
		t.Error("an undecided hunk must leave markers in the result")
	}
}

func TestMarkdownUnderlineIsNotAMarker(t *testing.T) {
	root, r := tempRepo(t)
	main := strings.TrimSpace(runGit(t, root, "branch", "--show-current"))
	undoCommit(t, root, "doc.md", "Title\n=====\nbody\n", "add doc")
	runGit(t, root, "switch", "-q", "-c", "topic")
	undoCommit(t, root, "doc.md", "Topic title\n=====\nbody\n", "topic")
	runGit(t, root, "switch", "-q", main)
	// the current side's own text holds a seven-character "=======" line
	undoCommit(t, root, "doc.md", "Main title\n=======\nbody\n", "main")
	if _, err := runGitAt(root, "merge", "topic"); err == nil {
		t.Fatal("expected a conflict")
	}
	sides, err := r.ReadConflict(context.Background(), root, "doc.md")
	if err != nil || !sides.Editable {
		t.Fatalf("sides = %+v err=%v", sides, err)
	}
	hunks := conflictHunksOf(sides.Segments)
	if len(hunks) != 1 || hunks[0].Ours != "Main title\n=======\n" || hunks[0].Theirs != "Topic title\n=====\n" {
		t.Fatalf("hunks = %+v", hunks)
	}
	if got := BuildConflictResult(sides.Segments, []HunkChoice{ChoiceOurs}); got != "Main title\n=======\nbody\n" {
		t.Errorf("result = %q", got)
	}
}

func TestParseConflictMarkersRejectsAnUnterminatedBlock(t *testing.T) {
	if _, err := ParseConflictMarkers(strings.Repeat("<", conflictMarkerSize) + " Current\nx\n"); err == nil {
		t.Error("an unterminated block was accepted")
	}
}

func TestConflictsThatCannotBeEditedSayWhy(t *testing.T) {
	root, r := tempRepo(t)
	main := strings.TrimSpace(runGit(t, root, "branch", "--show-current"))
	undoCommit(t, root, "gone.txt", "x\n", "add gone")
	undoCommit(t, root, "bin.dat", "a\x00b\n", "add bin")
	runGit(t, root, "switch", "-q", "-c", "topic")
	runGit(t, root, "rm", "-q", "gone.txt")
	writeRepoFile(t, root, "bin.dat", "t\x00topic\n")
	runGit(t, root, "add", "bin.dat")
	runGit(t, root, "commit", "-q", "-m", "topic")
	runGit(t, root, "switch", "-q", main)
	writeRepoFile(t, root, "gone.txt", "changed on main\n")
	writeRepoFile(t, root, "bin.dat", "m\x00main\n")
	runGit(t, root, "add", "gone.txt", "bin.dat")
	runGit(t, root, "commit", "-q", "-m", "main")
	if _, err := runGitAt(root, "merge", "topic"); err == nil {
		t.Fatal("expected a conflict")
	}
	for path, want := range map[string]string{"gone.txt": "deleted", "bin.dat": "binary"} {
		sides, err := r.ReadConflict(context.Background(), root, path)
		if err != nil || sides.Editable || !strings.Contains(sides.Reason, want) {
			t.Errorf("%s: sides = %+v err=%v", path, sides, err)
		}
	}
	if _, err := r.ReadConflict(context.Background(), root, "base.txt"); err == nil {
		t.Error("a file that is not in conflict was read as one")
	} else if ge, ok := GitError(err); !ok || ge.Class != ErrStaleState {
		t.Errorf("err = %v, want stale state", err)
	}
}

func TestResolveWithStagesTheResultAndRefusesMarkers(t *testing.T) {
	root, r := conflictRepo(t)
	ctx := context.Background()
	sides, err := r.ReadConflict(ctx, root, "f.txt")
	if err != nil {
		t.Fatal(err)
	}
	undecided := BuildConflictResult(sides.Segments, []HunkChoice{ChoiceOurs})
	if err := r.ResolveWith(ctx, root, "f.txt", undecided); err == nil {
		t.Fatal("a result with markers was staged")
	}
	if paths, _ := r.UnmergedPaths(ctx, root); len(paths) != 1 {
		t.Fatalf("unmerged = %v after a refusal", paths)
	}

	result := BuildConflictResult(sides.Segments, []HunkChoice{ChoiceOurs, ChoiceBoth})
	if err := r.ResolveWith(ctx, root, "f.txt", result); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, root, "f.txt"); got != result {
		t.Errorf("f.txt = %q", got)
	}
	if paths, _ := r.UnmergedPaths(ctx, root); len(paths) != 0 {
		t.Errorf("still unmerged: %v", paths)
	}
	if got := runGit(t, root, "show", ":f.txt"); strings.TrimSpace(got) != strings.TrimSpace(result) {
		t.Errorf("the index holds %q", got)
	}
	if err := r.ResolveWith(ctx, root, "f.txt", result); err == nil {
		t.Error("resolving a file that is no longer in conflict must be refused")
	}
	if err := r.ResolveWith(ctx, root, "../escape.txt", "x\n"); err == nil {
		t.Error("a path outside the repository was accepted")
	}
}

func TestResolveWithKeepsARecoveryPointThatReopensTheConflict(t *testing.T) {
	root, r := conflictRepo(t)
	r.Journal = true
	ctx := context.Background()
	// edits made elsewhere into the marker file must survive too
	onDisk := readRepoFile(t, root, "f.txt") + "an edit made in another editor\n"
	writeRepoFile(t, root, "f.txt", onDisk)

	sides, err := r.ReadConflict(ctx, root, "f.txt")
	if err != nil {
		t.Fatal(err)
	}
	result := BuildConflictResult(sides.Segments, []HunkChoice{ChoiceTheirs, ChoiceTheirs})
	if err := r.ResolveWith(ctx, root, "f.txt", result); err != nil {
		t.Fatal(err)
	}
	op := onlyOperation(t, r, root)
	if op.Kind != OpKindResolveConflict || op.After == "" || !op.CanUndo() {
		t.Fatalf("record = %+v", op)
	}

	if err := r.UndoOperation(ctx, root, op.ID); err != nil {
		t.Fatal(err)
	}
	if paths, _ := r.UnmergedPaths(ctx, root); len(paths) != 1 {
		t.Errorf("the conflict was not reopened: %v", paths)
	}
	if got := readRepoFile(t, root, "f.txt"); got != onDisk {
		t.Errorf("f.txt = %q, want what was on disk before", got)
	}
}

func TestUndoResolveRefusesWhenTheFileChangedOrTheMergeMovedOn(t *testing.T) {
	ctx := context.Background()
	t.Run("edited since", func(t *testing.T) {
		root, r := conflictRepo(t)
		r.Journal = true
		if err := r.ResolveWith(ctx, root, "f.txt", conflictBase); err != nil {
			t.Fatal(err)
		}
		writeRepoFile(t, root, "f.txt", "edited after resolving\n")
		err := r.UndoOperation(ctx, root, onlyOperation(t, r, root).ID)
		if ge, ok := GitError(err); !ok || ge.Class != ErrStaleState {
			t.Fatalf("err = %v, want stale state", err)
		}
		if got := readRepoFile(t, root, "f.txt"); got != "edited after resolving\n" {
			t.Errorf("a refused recovery changed the file: %q", got)
		}
	})
	t.Run("merge continued", func(t *testing.T) {
		root, r := conflictRepo(t)
		r.Journal = true
		if err := r.ResolveWith(ctx, root, "f.txt", conflictBase); err != nil {
			t.Fatal(err)
		}
		runGit(t, root, "commit", "-q", "--no-edit")
		err := r.UndoOperation(ctx, root, onlyOperation(t, r, root).ID)
		if ge, ok := GitError(err); !ok || ge.Class != ErrStaleState {
			t.Fatalf("err = %v, want stale state", err)
		}
		if got := readRepoFile(t, root, "f.txt"); got != conflictBase {
			t.Errorf("a refused recovery changed the file: %q", got)
		}
	})
}

func TestConflictEditorLeavesNothingWhenTheJournalIsOff(t *testing.T) {
	root, r := conflictRepo(t)
	ctx := context.Background()
	if _, err := r.ReadConflict(ctx, root, "f.txt"); err != nil {
		t.Fatal(err)
	}
	if err := r.ResolveWith(ctx, root, "f.txt", conflictBase); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, root, "for-each-ref", "refs/solcogit"); strings.TrimSpace(got) != "" {
		t.Errorf("refs written while off: %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(root, ".git", "solcogit", "tmp", "*")); len(left) != 0 {
		t.Errorf("scratch files left behind: %v", left)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "solcogit", "journal")); !os.IsNotExist(err) {
		t.Errorf("a journal directory appeared (stat err = %v)", err)
	}
}
