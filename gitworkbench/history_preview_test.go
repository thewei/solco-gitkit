package gitworkbench

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/**
 * [INPUT]: history_preview.go、合成的提交链与真实临时仓库（带远端）
 * [OUTPUT]: 预览的合同：每种动作改写后的顺序与标记（移走 / 并入 / 改说明 / 挪位）、不相邻 / 不在链上 / 有合并 / 下面没有可并入的 / 全删 / 挪出界一律拒绝、窗口两端不越界；PublishedCommits 区分已在远端与本地独有
 * [POS]: 2026-11-12 决定第 (12) 条批量历史改写预览的防线；每条都做过变异检查
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// chainOf builds n commits c0 (HEAD) … c(n-1) as ListCommits would return them.
func chainOf(n int) []CommitInfo {
	out := make([]CommitInfo, n)
	for i := range out {
		out[i] = CommitInfo{Hash: fmt.Sprintf("%040x", i+1), Short: fmt.Sprintf("c%d", i), Subject: fmt.Sprintf("subject %d", i)}
		if i+1 < n {
			out[i].Parents = []string{fmt.Sprintf("%040x", i+2)}
		}
	}
	return out
}

func hashes(c []CommitInfo, idx ...int) []string {
	var out []string
	for _, i := range idx {
		out = append(out, c[i].Hash)
	}
	return out
}

func shorts(p HistoryPreview) string {
	var parts []string
	for _, r := range p.Rows {
		s := r.Short
		if r.Removed {
			s = "~" + s
		}
		if r.Change != ChangeNone {
			s += ":" + string(r.Change)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func TestPreviewHistoryShowsTheResultOfEachAction(t *testing.T) {
	c := chainOf(8)
	cases := []struct {
		name  string
		op    HistoryOp
		above int
		want  string
	}{
		{"drop", HistoryOp{Action: HistoryDrop, Hashes: hashes(c, 3, 4)}, 2, "c2 ~c3:dropped ~c4:dropped c5"},
		{"squash", HistoryOp{Action: HistorySquash, Hashes: hashes(c, 3, 4)}, 2, "c2 ~c3:squashed ~c4:squashed c5:absorbs c6"},
		{"fixup", HistoryOp{Action: HistoryFixup, Hashes: hashes(c, 2)}, 1, "c1 ~c2:fixed up c3:absorbs c4"},
		{"reword", HistoryOp{Action: HistoryReword, Hashes: hashes(c, 3), Message: "better words\n\nbody"}, 2, "c2 c3:reworded c4"},
		{"move older", HistoryOp{Action: HistoryMove, Hashes: hashes(c, 2, 3), Offset: 2}, 1, "c1 c4 c5 c2:moved c3:moved c6"},
		{"move newer", HistoryOp{Action: HistoryMove, Hashes: hashes(c, 4), Offset: -2}, 1, "c1 c4:moved c2 c3 c5"},
		{"drop the newest", HistoryOp{Action: HistoryDrop, Hashes: hashes(c, 0)}, 0, "~c0:dropped c1"},
	}
	for _, tc := range cases {
		p, err := PreviewHistory(c, tc.op)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := shorts(p); got != tc.want || p.Above != tc.above {
			t.Errorf("%s: rows %q above %d, want %q above %d", tc.name, got, p.Above, tc.want, tc.above)
		}
	}
	p, _ := PreviewHistory(c, HistoryOp{Action: HistoryReword, Hashes: hashes(c, 3), Message: "better words\n\nbody"})
	if p.Rows[1].Subject != "better words" {
		t.Errorf("reworded subject = %q, want the new first line", p.Rows[1].Subject)
	}
	s, _ := PreviewHistory(c, HistoryOp{Action: HistorySquash, Hashes: hashes(c, 3, 4)})
	if s.Rows[1].Into != "c5" || s.Rows[3].Absorbed != 2 {
		t.Errorf("squash bookkeeping: %+v", s.Rows)
	}
}

func TestPreviewHistoryRefusesWhatCannotBeDone(t *testing.T) {
	c := chainOf(6)
	merge := chainOf(6)
	merge[2].Parents = append(merge[2].Parents, fmt.Sprintf("%040x", 99))
	cases := []struct {
		name    string
		commits []CommitInfo
		op      HistoryOp
		want    string
	}{
		{"not adjacent", c, HistoryOp{Action: HistoryDrop, Hashes: hashes(c, 1, 3)}, "next to each other"},
		{"off the branch", c, HistoryOp{Action: HistoryDrop, Hashes: []string{fmt.Sprintf("%040x", 77)}}, "not on this branch"},
		{"nothing below", c, HistoryOp{Action: HistorySquash, Hashes: hashes(c, 5)}, "no commit below"},
		{"drop all", c, HistoryOp{Action: HistoryDrop, Hashes: hashes(c, 0, 1, 2, 3, 4, 5)}, "empty the branch"},
		{"too far", c, HistoryOp{Action: HistoryMove, Hashes: hashes(c, 4), Offset: 2}, "cannot move that far"},
		{"too far up", c, HistoryOp{Action: HistoryMove, Hashes: hashes(c, 1), Offset: -2}, "cannot move that far"},
		{"merge in range", merge, HistoryOp{Action: HistoryDrop, Hashes: hashes(merge, 3)}, "merge commit"},
		{"bad op", c, HistoryOp{Action: HistoryReword, Hashes: hashes(c, 1, 2), Message: "x"}, "one commit at a time"},
	}
	for _, tc := range cases {
		_, err := PreviewHistory(tc.commits, tc.op)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one containing %q", tc.name, err, tc.want)
		}
	}
	// a merge further down than the rewrite reaches does not matter
	if _, err := PreviewHistory(merge, HistoryOp{Action: HistoryDrop, Hashes: hashes(merge, 0)}); err != nil {
		t.Errorf("a merge below the replayed range must not refuse: %v", err)
	}
}

func TestPublishedCommitsSeparatesPushedFromLocal(t *testing.T) {
	root, r := tempRepo(t)
	ctx := context.Background()
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, root, "init", "--bare", "-q", remote)
	pushed := commitFile(t, root, "a.txt", "a\n", "pushed")
	runGit(t, root, "remote", "add", "origin", remote)
	runGit(t, root, "push", "-q", "origin", "HEAD:refs/heads/main")
	runGit(t, root, "fetch", "-q", "origin")
	local := commitFile(t, root, "b.txt", "b\n", "local only")
	if _, err := os.Stat(remote); err != nil {
		t.Fatal(err)
	}

	got, err := r.PublishedCommits(ctx, root, []string{local, pushed})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != pushed {
		t.Fatalf("published = %v, want only %s", got, pushed)
	}
	if _, err := r.PublishedCommits(ctx, root, []string{"nope"}); err == nil {
		t.Error("a non-hash must be refused")
	}
	if got, _ := r.PublishedCommits(ctx, root, nil); len(got) != 0 {
		t.Errorf("nothing asked, nothing published: %v", got)
	}
}
