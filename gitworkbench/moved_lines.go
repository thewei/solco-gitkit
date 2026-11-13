package gitworkbench

import "strings"

/**
 * [INPUT]: 一个文件 diff 的 Hunk 列表
 * [OUTPUT]: MovedLines——哪些删除行与哪些新增行是“同一行搬了家”，以及它在另一侧的行号
 * [POS]: 移动行高亮的判定（2026-11-12 决定第 (14) 条），纯函数；规则取 TanGit 的保守版并更严一步：只认同一文件内唯一且去空白后完全相同、长度至少 3 的行，且两端不在同一段连续改动里（那是改写，不是搬家）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// MovedLine says a changed line was moved rather than rewritten. For a
// deleted line Other is the line number the text has in the new file; for an
// added line, the number it had in the old file.
type MovedLine struct {
	Removed bool
	Other   int
}

// minMovedLen keeps braces and other tiny lines out: they are too common to
// say anything about a move.
const minMovedLen = 3

// MovedLines finds the deleted lines of the hunks whose text, ignoring the
// surrounding whitespace, reappears as an added line elsewhere in the same
// file. A text counts only when it is unique among the deleted lines and among
// the added ones, so a repeated line is never guessed at; and the two ends
// must sit in different blocks of change (a deleted line directly followed by
// the same text added is an edit of the indentation, not a move). The keys are
// {hunk index, line index in Hunk.Lines}, like a pick in the diff.
func MovedLines(hunks []Hunk) map[[2]int]MovedLine {
	type at struct {
		key   [2]int
		line  int // the line number on its own side
		block int
	}
	removed := map[string]at{}
	added := map[string]at{}
	dupRemoved := map[string]bool{}
	dupAdded := map[string]bool{}
	block := 0
	for hi, h := range hunks {
		inChange := false
		for li, l := range h.Lines {
			if l.Kind == KindContext {
				inChange = false
				continue
			}
			if !inChange {
				block++
				inChange = true
			}
			text := strings.TrimSpace(l.Text)
			if len(text) < minMovedLen {
				continue
			}
			side, dup, number := removed, dupRemoved, l.OldLine
			if l.Kind == KindAdd {
				side, dup, number = added, dupAdded, l.NewLine
			}
			if _, seen := side[text]; seen {
				dup[text] = true
				continue
			}
			side[text] = at{key: [2]int{hi, li}, line: number, block: block}
		}
	}
	out := map[[2]int]MovedLine{}
	for text, from := range removed {
		to, ok := added[text]
		if !ok || dupRemoved[text] || dupAdded[text] || from.block == to.block {
			continue
		}
		out[from.key] = MovedLine{Removed: true, Other: to.line}
		out[to.key] = MovedLine{Other: from.line}
	}
	return out
}
