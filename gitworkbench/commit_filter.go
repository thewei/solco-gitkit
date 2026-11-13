package gitworkbench

import (
	"context"
	"strconv"
	"time"
)

/**
 * [INPUT]: Runner.Read、commitFormat / parseCommits / graphRevs（commit_graph.go）、cleanRelPath
 * [OUTPUT]: CommitFilter 与 FilterCommits：按路径、日期区间、范围（所有分支 / 仅当前分支）由 Git 自己筛提交，返回与图谱同形的 CommitInfo
 * [POS]: 2026-11-12 决定第 (14) 条的内核（提交过滤）；客户端的文字搜索仍只在已读入的窗口里筛，这里是 Git 一侧的查询，所以不受窗口大小限制，只受 Limit 限制
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// CommitFilter is the Git-side query of the Commits list. The zero value
// asks for nothing but the graph's own scope.
type CommitFilter struct {
	// Path keeps the commits that touched this file or folder (relative to the
	// repository root, taken literally).
	Path string
	// Since and Until are whole days, YYYY-MM-DD, both ends included; either may
	// be empty. They compare the commit date.
	Since, Until string
	// CurrentBranchOnly reads HEAD's history instead of every branch, remote and
	// tag the graph shows.
	CurrentBranchOnly bool
	// Hidden are the refs the graph hides; they stay hidden here too.
	Hidden []string
	Limit  int
}

const commitFilterDay = "2006-01-02"

// Active says whether the filter narrows anything.
func (f CommitFilter) Active() bool {
	return f.Path != "" || f.Since != "" || f.Until != "" || f.CurrentBranchOnly
}

// FilterCommits asks Git for the commits that pass the filter, newest first.
// A malformed date, a range that ends before it begins and a path outside the
// repository are refused before Git runs.
func (r *Runner) FilterCommits(ctx context.Context, root string, f CommitFilter) ([]CommitInfo, error) {
	var since, until time.Time
	var err error
	if f.Since != "" {
		if since, err = time.Parse(commitFilterDay, f.Since); err != nil {
			return nil, refusal("log", "%q is not a date; write it as YYYY-MM-DD", f.Since)
		}
	}
	if f.Until != "" {
		if until, err = time.Parse(commitFilterDay, f.Until); err != nil {
			return nil, refusal("log", "%q is not a date; write it as YYYY-MM-DD", f.Until)
		}
	}
	if f.Since != "" && f.Until != "" && until.Before(since) {
		return nil, refusal("log", "the range ends before it begins")
	}
	limit := f.Limit
	if limit <= 0 || limit > MaxGraphLimit {
		limit = 200
	}
	args := []string{"--literal-pathspecs", "log", "-n", strconv.Itoa(limit), "--decorate=full", commitFormat}
	if f.Since != "" {
		args = append(args, "--since="+f.Since+" 00:00:00")
	}
	if f.Until != "" {
		args = append(args, "--until="+f.Until+" 23:59:59")
	}
	if f.CurrentBranchOnly {
		args = append(args, "HEAD")
	} else {
		args = append(args, graphRevs(GraphQuery{Hidden: f.Hidden})...)
	}
	if f.Path != "" {
		path, ok := cleanRelPath(f.Path)
		if !ok {
			return nil, refusal("log", "%q is not a path inside the repository", f.Path)
		}
		args = append(args, "--", path)
	}
	out, err := r.Read(ctx, root, args...)
	if err != nil {
		return nil, err
	}
	return parseCommits(out), nil
}
