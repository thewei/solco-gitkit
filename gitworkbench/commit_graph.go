package gitworkbench

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

/**
 * [INPUT]: 一个仓库根、条数上限与「在图里隐藏」的完整 ref 名集合
 * [OUTPUT]: ListCommitGraph（所有分支/远程/标签 + HEAD 的日期序历史，隐藏的 ref 用 Git 原生 --exclude 排除，每个提交带真实 %D 装饰）、ResolveCommit（ref → 提交哈希）、CommitByHash（单提交元数据）、CommitRef
 * [POS]: Commits 视图图谱的唯一读取入口；只读，所有可见性由 Git 自己判定，不在客户端推断可达性
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// Ref namespaces the graph reads. A hidden ref is always a full name.
const (
	RefHeadsPrefix   = "refs/heads/"
	RefRemotesPrefix = "refs/remotes/"
	RefTagsPrefix    = "refs/tags/"
)

// CommitRefKind says which namespace a decoration came from.
type CommitRefKind string

const (
	RefBranch CommitRefKind = "branch"
	RefRemote CommitRefKind = "remote"
	RefTag    CommitRefKind = "tag"
)

// CommitRef is one ref pointing at a commit. Full is the whole name (the key
// a hide list uses); Name is the short form chips show.
type CommitRef struct {
	Kind CommitRefKind
	Name string
	Full string
	Head bool // the checked-out branch ("HEAD -> name")
}

// GraphQuery bounds a graph read. Hidden holds full ref names left out of the
// graph: a commit only they reach disappears, a commit shared with a visible
// ref stays.
type GraphQuery struct {
	Limit  int
	Hidden []string
}

// MaxGraphLimit is the largest window a graph read may ask for.
const MaxGraphLimit = 20000

// commitFormat: unit-separated, so a subject may hold tabs.
const commitFormat = "--format=%H%x1f%h%x1f%s%x1f%an%x1f%at%x1f%P%x1f%D"

// ListCommitGraph reads every branch, remote-tracking branch and tag, plus
// HEAD, newest first in date order (a parent never precedes its children,
// which the lane projection relies on). Stashes and notes stay out.
func (r *Runner) ListCommitGraph(ctx context.Context, root string, q GraphQuery) ([]CommitInfo, error) {
	return r.readCommits(ctx, root, q.Limit, graphRevs(q)...)
}

// GraphPosition is the 0-based row a commit has in the graph ListCommitGraph
// draws for q (without a limit), or -1 when the graph never reaches it — the
// size the window must grow to for the commit to be a row.
func (r *Runner) GraphPosition(ctx context.Context, root string, q GraphQuery, hash string) (int, error) {
	if !isFullHash(hash) {
		return -1, nil
	}
	out, err := r.Read(ctx, root, append([]string{"rev-list"}, graphRevs(q)...)...)
	if err != nil {
		return -1, err
	}
	for i, line := range strings.Split(string(out), "\n") {
		if line == hash {
			return i, nil
		}
	}
	return -1, nil
}

func isFullHash(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// graphRevs are the revision arguments of the commit graph: every branch,
// remote and tag plus HEAD, in date order, minus the hidden refs.
func graphRevs(q GraphQuery) []string {
	hidden := map[string][]string{}
	for _, full := range q.Hidden {
		for _, ns := range []struct{ prefix, flag string }{
			{RefHeadsPrefix, "--branches"}, {RefRemotesPrefix, "--remotes"}, {RefTagsPrefix, "--tags"},
		} {
			// --exclude patterns are relative to the namespace and reset after
			// each selector, so they are repeated per selector.
			if rest, ok := strings.CutPrefix(full, ns.prefix); ok && rest != "" {
				hidden[ns.flag] = append(hidden[ns.flag], "--exclude="+rest)
			}
		}
	}
	args := []string{"--date-order"}
	for _, flag := range []string{"--branches", "--remotes", "--tags"} {
		args = append(args, hidden[flag]...)
		args = append(args, flag)
	}
	return append(args, "HEAD")
}

// ResolveCommit names the commit a full ref points at (an annotated tag is
// peeled). Anything but a refs/ name is refused.
func (r *Runner) ResolveCommit(ctx context.Context, root, fullRef string) (string, error) {
	if !strings.HasPrefix(fullRef, "refs/") {
		return "", &Error{Class: ErrStaleState, OpClass: OpRead, Op: "rev-parse",
			err: fmt.Errorf("%q is not a full ref name", fullRef)}
	}
	out, err := r.Read(ctx, root, "rev-parse", "--verify", "--quiet", fullRef+"^{commit}")
	if err != nil {
		return "", &Error{Class: ErrStaleState, OpClass: OpRead, Op: "rev-parse",
			err: fmt.Errorf("%q no longer names a commit", fullRef)}
	}
	return strings.TrimSpace(string(out)), nil
}

// CommitByHash reads one commit's list metadata (no decorations).
func (r *Runner) CommitByHash(ctx context.Context, root, hash string) (*CommitInfo, error) {
	if _, err := r.Read(ctx, root, "rev-parse", "--verify", "--quiet", hash+"^{commit}"); err != nil {
		return nil, &Error{Class: ErrStaleState, OpClass: OpRead, Op: "log",
			err: fmt.Errorf("%q is not a commit", hash)}
	}
	out, err := r.Read(ctx, root, "log", "-1", "--decorate=full", commitFormat, hash, "--")
	if err != nil {
		return nil, err
	}
	commits := parseCommits(out)
	if len(commits) == 0 {
		return nil, nil
	}
	return &commits[0], nil
}

// readCommits runs `git log` over the given revision arguments (HEAD when
// none) and parses the shared format.
func (r *Runner) readCommits(ctx context.Context, root string, limit int, revs ...string) ([]CommitInfo, error) {
	if limit <= 0 || limit > MaxGraphLimit {
		limit = 200
	}
	args := []string{"log", "-n", strconv.Itoa(limit), "--decorate=full", commitFormat}
	args = append(args, revs...)
	out, err := r.Read(ctx, root, args...)
	if err != nil {
		return nil, err
	}
	return parseCommits(out), nil
}

func parseCommits(out []byte) []CommitInfo {
	var commits []CommitInfo
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\x1f", 7)
		if len(f) < 5 {
			continue
		}
		sec, _ := strconv.ParseInt(f[4], 10, 64)
		cm := CommitInfo{Hash: f[0], Short: f[1], Subject: f[2], Author: f[3], Time: time.Unix(sec, 0)}
		if len(f) > 5 {
			cm.Parents = strings.Fields(f[5])
		}
		if len(f) > 6 {
			cm.Refs = parseDecorations(f[6])
		}
		commits = append(commits, cm)
	}
	return commits
}

// parseDecorations reads `%D` under --decorate=full: "HEAD -> refs/heads/main,
// refs/remotes/origin/main, tag: refs/tags/v1". Branch names hold no spaces,
// so ", " separates entries. Symbolic remote HEADs, stashes and bare HEAD are
// not chips.
func parseDecorations(raw string) []CommitRef {
	var refs []CommitRef
	for _, entry := range strings.Split(raw, ", ") {
		entry = strings.TrimSpace(entry)
		head := false
		if rest, ok := strings.CutPrefix(entry, "HEAD -> "); ok {
			entry, head = rest, true
		}
		entry = strings.TrimPrefix(entry, "tag: ")
		switch {
		case strings.HasPrefix(entry, RefHeadsPrefix):
			refs = append(refs, CommitRef{Kind: RefBranch, Name: strings.TrimPrefix(entry, RefHeadsPrefix), Full: entry, Head: head})
		case strings.HasPrefix(entry, RefRemotesPrefix):
			name := strings.TrimPrefix(entry, RefRemotesPrefix)
			if strings.HasSuffix(name, "/HEAD") {
				continue
			}
			refs = append(refs, CommitRef{Kind: RefRemote, Name: name, Full: entry})
		case strings.HasPrefix(entry, RefTagsPrefix):
			refs = append(refs, CommitRef{Kind: RefTag, Name: strings.TrimPrefix(entry, RefTagsPrefix), Full: entry})
		}
	}
	return refs
}
