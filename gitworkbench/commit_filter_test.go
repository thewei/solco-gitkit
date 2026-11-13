package gitworkbench

import (
	"context"
	"strings"
	"testing"
)

/**
 * [INPUT]: commit_filter.go、真实临时仓库（带固定提交日期与一条未合并的分支）
 * [OUTPUT]: 提交过滤的合同：路径（文件、文件夹、结尾斜杠）、日期区间（两端含当天的零点与 23:59）、当前分支 / 所有分支、被隐藏的分支保持隐藏、Limit；格式错误的日期、倒置的区间、仓库外或以 - 开头的路径在 Git 运行前被拒
 * [POS]: 2026-11-12 决定第 (14) 条的内核防线
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

func datedCommit(t *testing.T, root, date, name, content, msg string) {
	t.Helper()
	t.Setenv("GIT_COMMITTER_DATE", date)
	t.Setenv("GIT_AUTHOR_DATE", date)
	commitAll(t, root, name, content, msg)
}

// filterRig: main has c1 (a.txt, Jan 10), c2 (b.txt, Feb 10), c2b (c.txt, Feb
// 28 23:00), c3 (a.txt, Mar 1 00:30), c4 (docs/x.md, Apr 10); "side" has s1
// (side.txt, Feb 20) off c2 and is not merged.
func filterRig(t *testing.T) (string, *Runner) {
	t.Helper()
	t.Setenv("TZ", "UTC")
	root, r := tempRepo(t)
	datedCommit(t, root, "2026-01-10T12:00:00+0000", "a.txt", "1\n", "c1")
	datedCommit(t, root, "2026-02-10T12:00:00+0000", "b.txt", "b\n", "c2")
	runGit(t, root, "checkout", "-q", "-b", "side")
	datedCommit(t, root, "2026-02-20T12:00:00+0000", "side.txt", "s\n", "s1")
	runGit(t, root, "checkout", "-q", "-")
	datedCommit(t, root, "2026-02-28T23:00:00+0000", "c.txt", "c\n", "c2b")
	datedCommit(t, root, "2026-03-01T00:30:00+0000", "a.txt", "2\n", "c3")
	datedCommit(t, root, "2026-04-10T12:00:00+0000", "docs/x.md", "x\n", "c4")
	return root, r
}

func subjectsOf(cs []CommitInfo) string {
	var s []string
	for _, c := range cs {
		s = append(s, c.Subject)
	}
	return strings.Join(s, ",")
}

func TestFilterCommitsByPathDatesAndScope(t *testing.T) {
	ctx := context.Background()
	root, r := filterRig(t)
	for _, c := range []struct {
		name string
		f    CommitFilter
		want string
	}{
		{"nothing asked: every branch", CommitFilter{}, "c4,c3,c2b,s1,c2,c1,initial"},
		{"a file", CommitFilter{Path: "a.txt"}, "c3,c1"},
		{"a folder", CommitFilter{Path: "docs"}, "c4"},
		{"a folder with a slash", CommitFilter{Path: "./docs/"}, "c4"},
		{"a path nobody touched", CommitFilter{Path: "nope.txt"}, ""},
		{"a month, ends included", CommitFilter{Since: "2026-02-10", Until: "2026-02-28"}, "c2b,s1,c2"},
		{"since only", CommitFilter{Since: "2026-03-01"}, "c4,c3"},
		{"until only", CommitFilter{Until: "2026-01-31"}, "c1"},
		{"the current branch only", CommitFilter{CurrentBranchOnly: true, Since: "2026-02-01", Until: "2026-02-28"}, "c2b,c2"},
		{"path and dates", CommitFilter{Path: "a.txt", Since: "2026-02-01"}, "c3"},
		{"a hidden branch stays hidden", CommitFilter{Hidden: []string{"refs/heads/side"}}, "c4,c3,c2b,c2,c1,initial"},
		{"limit", CommitFilter{Limit: 2}, "c4,c3"},
	} {
		got, err := r.FilterCommits(ctx, root, c.f)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if s := subjectsOf(got); s != c.want {
			t.Errorf("%s: %s, want %s", c.name, s, c.want)
		}
	}
}

func TestFilterCommitsRefusesBeforeGitRuns(t *testing.T) {
	ctx := context.Background()
	root, r := filterRig(t)
	for name, f := range map[string]CommitFilter{
		"a date that is not one": {Since: "last week"},
		"a bad until":            {Until: "2026-13-40"},
		"an inverted range":      {Since: "2026-03-01", Until: "2026-02-01"},
		"outside the repository": {Path: "../x"},
		"the root itself":        {Path: "."},
		"an option for a path":   {Path: "--all"},
	} {
		if _, err := r.FilterCommits(ctx, root, f); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if !(CommitFilter{Path: "a"}).Active() || !(CommitFilter{CurrentBranchOnly: true}).Active() || !(CommitFilter{Since: "2026-01-01"}).Active() || !(CommitFilter{Until: "2026-01-01"}).Active() || (CommitFilter{Hidden: []string{"x"}, Limit: 5}).Active() {
		t.Error("Active() misreports what narrows the list")
	}
}

func TestFilterCommitsTakesAPathLiterally(t *testing.T) {
	ctx := context.Background()
	root, r := filterRig(t)
	datedCommit(t, root, "2026-05-01T12:00:00+0000", "star*.txt", "w\n", "wild")
	got, err := r.FilterCommits(ctx, root, CommitFilter{Path: "*.txt"})
	if err != nil || subjectsOf(got) != "" {
		t.Errorf("a glob matched files: %q err=%v", subjectsOf(got), err)
	}
	got, err = r.FilterCommits(ctx, root, CommitFilter{Path: "star*.txt"})
	if err != nil || subjectsOf(got) != "wild" {
		t.Errorf("the file with a star in its name: %q err=%v", subjectsOf(got), err)
	}
}
