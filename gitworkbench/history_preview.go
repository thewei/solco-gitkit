package gitworkbench

import (
	"context"
	"slices"
	"strings"
)

/**
 * [INPUT]: 已读出的提交列表（ListCommits，新的在前）与一次 HistoryOp；Runner.Read（只读）
 * [OUTPUT]: PreviewHistory——不动仓库，算出改写之后这一段提交的顺序（哪些被移走、并入、改了说明、挪了位置）；PublishedCommits——这些提交里哪些已在远端跟踪引用上
 * [POS]: 批量历史改写的「预览」（2026-11-12 决定第 (12) 条）：界面先给用户看结果再执行；规则镜像 RewriteHistory / engine（同一条链、连续、无合并），真正的执行与最终拒绝仍在后端
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// HistoryChange says what a rewrite does to one row of the preview.
type HistoryChange string

const (
	ChangeNone     HistoryChange = ""
	ChangeMoved    HistoryChange = "moved"
	ChangeReworded HistoryChange = "reworded"
	ChangeDropped  HistoryChange = "dropped"
	ChangeSquashed HistoryChange = "squashed" // folded into Into, message kept as part of it
	ChangeFixedUp  HistoryChange = "fixed up" // folded into Into, message dropped
	ChangeAbsorbs  HistoryChange = "absorbs"  // the commit that takes the others in; Absorbed counts them
)

// HistoryPreviewRow is one commit of the preview.
type HistoryPreviewRow struct {
	Hash, Short string
	Subject     string // the new subject for a reworded commit
	Change      HistoryChange
	Removed     bool   // gone after the rewrite: shown struck through at its old place
	Into        string // short hash of the commit a squashed or fixed-up commit goes into
	Absorbed    int    // for ChangeAbsorbs
}

// HistoryPreview is the part of the branch a rewrite touches, in the order it
// will have afterwards (removed commits stay in the list, marked).
type HistoryPreview struct {
	// Above is how many newer commits are not listed: they keep their order
	// and are replayed on top, so they get new hashes.
	Above int
	Rows  []HistoryPreviewRow
}

// PreviewHistory computes what op would do to the branch whose commits are
// given (newest first, the first one is HEAD). It changes nothing. It refuses
// what RewriteHistory would refuse for reasons visible in the list: a commit
// off the branch's first-parent line, a run that is not adjacent, a merge in
// the replayed range, nothing below to squash into, an empty branch, a move
// past either end.
func PreviewHistory(commits []CommitInfo, op HistoryOp) (HistoryPreview, error) {
	if err := validateHistoryOp(op); err != nil {
		return HistoryPreview{}, err
	}
	chain := firstParentChain(commits)
	index := map[string]int{}
	for i, c := range chain {
		index[c.Hash] = i
	}
	start, end := len(chain), -1
	for _, h := range op.Hashes {
		i, ok := index[h]
		if !ok {
			return HistoryPreview{}, refusal("history", "a chosen commit is not on this branch")
		}
		start, end = min(start, i), max(end, i)
	}
	if end-start+1 != len(op.Hashes) {
		return HistoryPreview{}, refusal("history", "the chosen commits must sit next to each other")
	}
	reach := end + 1
	switch {
	case op.Action == HistorySquash || op.Action == HistoryFixup:
		reach++
	case op.Action == HistoryMove && op.Offset > 0:
		reach += op.Offset
	}
	for i := 0; i < min(reach, len(chain)); i++ {
		if len(chain[i].Parents) > 1 {
			return HistoryPreview{}, refusal("history", "the range contains a merge commit (%s); history rewriting only handles linear history", chain[i].Short)
		}
	}
	row := func(i int) HistoryPreviewRow {
		return HistoryPreviewRow{Hash: chain[i].Hash, Short: chain[i].Short, Subject: chain[i].Subject}
	}
	run := make([]HistoryPreviewRow, 0, end-start+1)
	for i := start; i <= end; i++ {
		run = append(run, row(i))
	}

	var rows []HistoryPreviewRow
	lo, hi := max(0, start-1), min(len(chain)-1, end+1)
	switch op.Action {
	case HistoryReword:
		subject, _, _ := strings.Cut(strings.TrimSpace(op.Message), "\n")
		run[0].Change, run[0].Subject = ChangeReworded, strings.TrimSpace(subject)
		rows = slices.Concat(rowsOf(row, lo, start), run, rowsOf(row, end+1, hi+1))
	case HistoryDrop:
		if start == 0 && end == len(chain)-1 {
			return HistoryPreview{}, refusal("history", "dropping every commit would empty the branch; reset instead")
		}
		for i := range run {
			run[i].Change, run[i].Removed = ChangeDropped, true
		}
		rows = slices.Concat(rowsOf(row, lo, start), run, rowsOf(row, end+1, hi+1))
	case HistorySquash, HistoryFixup:
		if end+1 >= len(chain) {
			return HistoryPreview{}, refusal("history", "there is no commit below to squash into")
		}
		target := row(end + 1)
		change := ChangeSquashed
		if op.Action == HistoryFixup {
			change = ChangeFixedUp
		}
		for i := range run {
			run[i].Change, run[i].Removed, run[i].Into = change, true, target.Short
		}
		target.Change, target.Absorbed = ChangeAbsorbs, len(run)
		rows = slices.Concat(rowsOf(row, lo, start), run, []HistoryPreviewRow{target}, rowsOf(row, end+2, end+3))
	case HistoryMove:
		if start+op.Offset < 0 || end+op.Offset >= len(chain) {
			return HistoryPreview{}, refusal("history", "the commits cannot move that far")
		}
		for i := range run {
			run[i].Change = ChangeMoved
		}
		if op.Offset > 0 {
			// toward older commits: the commits it passes come up to its place
			lo, hi = max(0, start-1), min(len(chain)-1, end+op.Offset+1)
			rows = slices.Concat(rowsOf(row, lo, start), rowsOf(row, end+1, end+op.Offset+1), run, rowsOf(row, end+op.Offset+1, hi+1))
		} else {
			lo, hi = max(0, start+op.Offset-1), min(len(chain)-1, end+1)
			rows = slices.Concat(rowsOf(row, lo, start+op.Offset), run, rowsOf(row, start+op.Offset, start), rowsOf(row, end+1, hi+1))
		}
	}
	return HistoryPreview{Above: lo, Rows: rows}, nil
}

func rowsOf(row func(int) HistoryPreviewRow, from, to int) []HistoryPreviewRow {
	var out []HistoryPreviewRow
	for i := from; i < to; i++ {
		out = append(out, row(i))
	}
	return out
}

// firstParentChain is the checked-out branch's own line: HEAD, its first
// parent, and so on, as far as the list reaches.
func firstParentChain(commits []CommitInfo) []CommitInfo {
	if len(commits) == 0 {
		return nil
	}
	byHash := make(map[string]CommitInfo, len(commits))
	for _, c := range commits {
		byHash[c.Hash] = c
	}
	var chain []CommitInfo
	for cur, ok := commits[0], true; ok; {
		chain = append(chain, cur)
		if len(cur.Parents) == 0 {
			break
		}
		cur, ok = byHash[cur.Parents[0]]
	}
	return chain
}

// PublishedCommits returns those of hashes that a remote-tracking ref already
// holds: rewriting them means the branch can only be pushed by force. A
// repository without remotes publishes nothing. Read only.
func (r *Runner) PublishedCommits(ctx context.Context, root string, hashes []string) ([]string, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	for _, h := range hashes {
		if !validFullCommitID(h) {
			return nil, refusal("history", "a full Git commit hash is required")
		}
	}
	args := append([]string{"rev-list"}, hashes...)
	out, err := r.Read(ctx, root, append(args, "--not", "--remotes")...)
	if err != nil {
		return nil, err
	}
	unpublished := map[string]bool{}
	for _, h := range strings.Fields(string(out)) {
		unpublished[h] = true
	}
	var published []string
	for _, h := range hashes {
		if !unpublished[h] {
			published = append(published, h)
		}
	}
	return published, nil
}
