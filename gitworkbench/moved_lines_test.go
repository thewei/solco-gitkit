package gitworkbench

import "testing"

/**
 * [INPUT]: moved_lines.go、真实 git diff 的解析结果
 * [OUTPUT]: 移动行判定的合同：搬到别处的唯一行成对标出并带对侧行号；缩进改写、重复的行、太短的行、同一段改动里的删加不算搬家；多个 hunk 之间也能配对
 * [POS]: 2026-11-12 决定第 (14) 条的内核防线
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

func hunksOfText(t *testing.T, oldText, newText string) []Hunk {
	t.Helper()
	root, _ := tempRepo(t)
	writeRepoFile(t, root, "m.txt", oldText)
	runGit(t, root, "add", "m.txt")
	runGit(t, root, "commit", "-q", "-m", "m")
	writeRepoFile(t, root, "m.txt", newText)
	return hunksOf(t, root, false, "m.txt")
}

func TestMovedLinesPairsAUniqueLineThatChangedPlace(t *testing.T) {
	oldText := numberedLines(30, map[int]string{4: "func moved() int { return 42 }"})
	newText := numberedLines(30, map[int]string{4: "\x00", 26: "func moved() int { return 42 }"})
	hunks := hunksOfText(t, oldText, newText)
	pairs := MovedLines(hunks)
	if len(pairs) != 2 {
		t.Fatalf("pairs = %v, want both ends", pairs)
	}
	for key, p := range pairs {
		l := hunks[key[0]].Lines[key[1]]
		switch {
		case l.Kind == KindDelete && (!p.Removed || p.Other != 25): // it is line 25 in the new file (one line gone above)
			t.Errorf("deleted end = %+v at %+v", p, l)
		case l.Kind == KindAdd && (p.Removed || p.Other != 4):
			t.Errorf("added end = %+v at %+v", p, l)
		}
	}
}

func TestMovedLinesIgnoresWhatIsNotAMove(t *testing.T) {
	cases := []struct {
		name, old, new string
	}{
		{"reindent in place", "a\n    keep going here\nb\n", "a\n        keep going here\nb\n"},
		{"tiny lines", numberedLines(20, map[int]string{3: "}"}), numberedLines(20, map[int]string{3: "\x00", 18: "}"})},
		{"a repeated text", numberedLines(30, map[int]string{3: "same line", 5: "same line"}), numberedLines(30, map[int]string{3: "\x00", 5: "\x00", 28: "same line"})},
		{"an edit", numberedLines(20, map[int]string{3: "value := 1"}), numberedLines(20, map[int]string{3: "value := 2"})},
		{"added twice", numberedLines(30, map[int]string{3: "relocated text"}), numberedLines(30, map[int]string{3: "\x00", 10: "relocated text", 25: "relocated text"})},
	}
	for _, c := range cases {
		if got := MovedLines(hunksOfText(t, c.old, c.new)); len(got) != 0 {
			t.Errorf("%s: %v marked as moved", c.name, got)
		}
	}
}

func TestMovedLinesFindsThePairAcrossHunksButNotWithinOneBlock(t *testing.T) {
	// two hunks far apart: the line leaves one and arrives in the other
	oldText := numberedLines(40, map[int]string{3: "the line that travels"})
	newText := numberedLines(40, map[int]string{3: "\x00", 36: "the line that travels"})
	hunks := hunksOfText(t, oldText, newText)
	if len(hunks) != 2 || len(MovedLines(hunks)) != 2 {
		t.Fatalf("hunks=%d moved=%v, want a pair across two hunks", len(hunks), MovedLines(hunks))
	}
}

func TestMovedLinesIgnoresTheIndentationOfAMovedLine(t *testing.T) {
	oldText := numberedLines(30, map[int]string{4: "    return value + 1"})
	newText := numberedLines(30, map[int]string{4: "\x00", 26: "\t\treturn value + 1"})
	if got := MovedLines(hunksOfText(t, oldText, newText)); len(got) != 2 {
		t.Fatalf("a line moved and re-indented: %v, want both ends", got)
	}
}
