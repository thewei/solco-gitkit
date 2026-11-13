package gitworkbench

import (
	"slices"
	"strings"
	"testing"
)

/**
 * [INPUT]: custom_patch.go PatchLinesFor、合成的 PatchSource 行与界面看到的 Hunk
 * [OUTPUT]: 选择映射的合同：界面里 {hunk,行} 精确落到 PatchSource 的行号、两处读法不一致（行数 / 种类 / 文本 / hunk 数）一律拒绝而不是猜、选到上下文行被拒
 * [POS]: 2026-11-12 决定第 (12) 条 Custom Patch 界面的防线；真实仓库上的端到端在 solco-gitkit/engine/patch_test.go
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

func sourceOf(text string) []PatchSourceLine {
	var out []PatchSourceLine
	for i, l := range strings.Split(text, "\n") {
		out = append(out, PatchSourceLine{Index: i, Text: l})
	}
	return out
}

const twoHunkDiff = `diff --git a/f.txt b/f.txt
index 111..222 100644
--- a/f.txt
+++ b/f.txt
@@ -1,3 +1,3 @@
 keep
-old one
+new one
@@ -10,2 +10,3 @@ func
 tail
+added
\ No newline at end of file
`

func twoHunks() []Hunk {
	return []Hunk{
		{Lines: []DiffLine{{Kind: KindContext, Text: "keep"}, {Kind: KindDelete, Text: "old one"}, {Kind: KindAdd, Text: "new one"}}},
		{Lines: []DiffLine{{Kind: KindContext, Text: "tail"}, {Kind: KindAdd, Text: "added", NoNewline: true}}},
	}
}

func TestPatchLinesForMapsThePickedLines(t *testing.T) {
	got, err := PatchLinesFor(sourceOf(twoHunkDiff), twoHunks(), map[[2]int]bool{{0, 2}: true, {1, 1}: true, {0, 1}: true})
	if err != nil {
		t.Fatal(err)
	}
	// source lines: 0 diff, 1 index, 2 ---, 3 +++, 4 @@, 5 keep, 6 -old, 7 +new, 8 @@, 9 tail, 10 +added, 11 \ No newline
	if want := []int{6, 7, 10}; !slices.Equal(got, want) {
		t.Fatalf("mapped %v, want %v", got, want)
	}
}

func TestPatchLinesForRefusesAReadingThatDiffers(t *testing.T) {
	pick := map[[2]int]bool{{0, 2}: true}
	t.Run("text", func(t *testing.T) {
		h := twoHunks()
		h[0].Lines[2].Text = "different"
		if _, err := PatchLinesFor(sourceOf(twoHunkDiff), h, pick); err == nil {
			t.Error("a different line text must be refused")
		}
	})
	t.Run("kind", func(t *testing.T) {
		h := twoHunks()
		h[0].Lines[2].Kind = KindContext
		if _, err := PatchLinesFor(sourceOf(twoHunkDiff), h, pick); err == nil {
			t.Error("a different line kind must be refused")
		}
	})
	t.Run("line count", func(t *testing.T) {
		h := twoHunks()
		h[1].Lines = h[1].Lines[:1]
		if _, err := PatchLinesFor(sourceOf(twoHunkDiff), h, pick); err == nil {
			t.Error("a different number of lines must be refused")
		}
	})
	t.Run("hunk count", func(t *testing.T) {
		if _, err := PatchLinesFor(sourceOf(twoHunkDiff), twoHunks()[:1], pick); err == nil {
			t.Error("a different number of hunks must be refused")
		}
	})
	t.Run("context line", func(t *testing.T) {
		if _, err := PatchLinesFor(sourceOf(twoHunkDiff), twoHunks(), map[[2]int]bool{{0, 0}: true}); err == nil {
			t.Error("picking a context line must be refused")
		}
	})
	t.Run("out of range", func(t *testing.T) {
		if _, err := PatchLinesFor(sourceOf(twoHunkDiff), twoHunks(), map[[2]int]bool{{5, 0}: true}); err == nil {
			t.Error("a hunk that is not there must be refused")
		}
	})
}
