package filesview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

/**
 * [INPUT]: 真实临时 Git 仓库 + 非 Git 目录；ripgrep 与 git grep 两种引擎
 * [OUTPUT]: 文件清单（Git 忽略语义、固定重目录、包含忽略开关）、文件名排序、全文搜索的选项（大小写/全词/正则）、逐文件与总量上限、无匹配与坏正则、两种引擎结果一致、无引擎时的真实错误
 * [POS]: search.go 回归
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func searchRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	write(t, root, ".gitignore", "ignored.txt\n")
	write(t, root, "main.go", "package main\n\n\tfunc Needle() {}\nfunc other() { needle() }\n")
	write(t, root, "docs/guide.md", "# Guide\nthe NEEDLE is here\nneedles plural\n")
	write(t, root, "ignored.txt", "needle in an ignored file\n")
	write(t, root, "node_modules/dep/index.js", "needle in a dependency\n")
	write(t, root, "bin.dat", "needle\x00binary\n")
	run("add", ".gitignore", "main.go", "docs")
	run("commit", "-qm", "init")
	write(t, root, "scratch.txt", "untracked needle\n") // untracked, not ignored
	return root
}

func engines(t *testing.T) map[string]func() string {
	t.Helper()
	out := map[string]func() string{"git grep": func() string { return "" }}
	if ResolveRipgrep() != "" {
		out["ripgrep"] = ResolveRipgrep
	}
	return out
}

func withEngine(t *testing.T, pick func() string) {
	t.Helper()
	old := ripgrepPath
	ripgrepPath = pick
	t.Cleanup(func() { ripgrepPath = old })
}

func paths(res ContentResult) []string {
	var out []string
	for _, f := range res.Files {
		out = append(out, f.Path)
	}
	slices.Sort(out)
	return out
}

func TestListFilesFollowsGitIgnoreAndTheFixedNames(t *testing.T) {
	root := searchRepo(t)
	idx, err := ListFiles(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".gitignore", "bin.dat", "docs/guide.md", "main.go", "scratch.txt"}
	if !slices.Equal(idx.Paths, want) {
		t.Fatalf("files = %v, want %v (no ignored file, no node_modules)", idx.Paths, want)
	}
	all, err := ListFiles(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(all.Paths, "ignored.txt") || !slices.Contains(all.Paths, "node_modules/dep/index.js") {
		t.Fatalf("including ignored must list what the rules hide: %v", all.Paths)
	}
}

func TestListFilesOutsideARepositoryNeedsRipgrep(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "x")
	write(t, root, "sub/b.txt", "x")
	withEngine(t, func() string { return "" })
	if _, err := ListFiles(context.Background(), root, false); !errors.Is(err, ErrNoContentEngine) {
		t.Fatalf("no git and no ripgrep: %v", err)
	}
	if ResolveRipgrep() == "" {
		return
	}
	withEngine(t, ResolveRipgrep)
	idx, err := ListFiles(context.Background(), root, false)
	if err != nil || !slices.Equal(idx.Paths, []string{"a.txt", "sub/b.txt"}) {
		t.Fatalf("ripgrep lists a plain folder: %v %v", idx.Paths, err)
	}
}

func TestMatchFilesRanksNameOverFolderAndShortOverLong(t *testing.T) {
	all := []string{"internal/nativeui/git_commit_graph.go", "docs/graph.md", "graph.go", "internal/graphics/other.go", "README.md"}
	hits, total := MatchFiles(all, "graph", 10)
	if total != 4 || hits[0] != "graph.go" || hits[1] != "docs/graph.md" {
		t.Fatalf("name matches first, shorter first: %v (%d)", hits, total)
	}
	if hits, _ := MatchFiles(all, "gcgraph", 10); len(hits) != 1 || hits[0] != "internal/nativeui/git_commit_graph.go" {
		t.Fatalf("a scattered subsequence still finds the file: %v", hits)
	}
	if hits, _ := MatchFiles(all, "docs md", 10); len(hits) != 1 || hits[0] != "docs/graph.md" {
		t.Fatalf("every word must match: %v", hits)
	}
	if hits, total := MatchFiles(all, "  ", 10); hits != nil || total != 0 {
		t.Fatalf("a blank query matches nothing: %v", hits)
	}
	if hits, total := MatchFiles(all, "go", 2); len(hits) != 2 || total < 3 {
		t.Fatalf("limit cuts the list, not the count: %v (%d)", hits, total)
	}
}

func TestSearchContentOptionsOnBothEngines(t *testing.T) {
	root := searchRepo(t)
	for name, pick := range engines(t) {
		t.Run(name, func(t *testing.T) {
			withEngine(t, pick)
			ctx := context.Background()

			res, err := SearchContent(ctx, root, ContentQuery{Pattern: "needle"})
			if err != nil {
				t.Fatal(err)
			}
			// Insensitive by default; ignored files, node_modules and binaries stay out;
			// untracked-but-not-ignored files are in.
			if got := paths(res); !slices.Equal(got, []string{"docs/guide.md", "main.go", "scratch.txt"}) {
				t.Fatalf("files = %v", got)
			}
			if res.Engine != name {
				t.Fatalf("engine = %q, want %q", res.Engine, name)
			}
			var main ContentFile
			for _, f := range res.Files {
				if f.Path == "main.go" {
					main = f
				}
			}
			if len(main.Matches) != 2 || main.Matches[0].Line != 3 || main.Matches[0].Text != "func Needle() {}" {
				t.Fatalf("main.go matches = %+v (leading whitespace trimmed, 1-based lines)", main.Matches)
			}
			if m := main.Matches[0]; m.Start != 5 || m.End != 11 || m.Col != 6 {
				t.Fatalf("highlight %d..%d col %d, want 5..11 col 6 (the tab is one column)", m.Start, m.End, m.Col)
			}

			res, _ = SearchContent(ctx, root, ContentQuery{Pattern: "Needle", CaseSensitive: true})
			if got := paths(res); !slices.Equal(got, []string{"main.go"}) {
				t.Fatalf("case sensitive = %v", got)
			}
			res, _ = SearchContent(ctx, root, ContentQuery{Pattern: "needle", WholeWord: true})
			for _, f := range res.Files {
				for _, m := range f.Matches {
					if strings.Contains(m.Text, "needles") {
						t.Fatalf("whole word matched a longer word: %q", m.Text)
					}
				}
			}
			res, _ = SearchContent(ctx, root, ContentQuery{Pattern: `func.+Needle`, Regex: true, CaseSensitive: true})
			if got := paths(res); !slices.Equal(got, []string{"main.go"}) {
				t.Fatalf("regex = %v", got)
			}
			res, _ = SearchContent(ctx, root, ContentQuery{Pattern: "needle", IncludeIgnored: true})
			if got := paths(res); !slices.Contains(got, "ignored.txt") || !slices.Contains(got, "node_modules/dep/index.js") {
				t.Fatalf("including ignored files: %v", got)
			}
			if slices.Contains(paths(res), "bin.dat") {
				t.Fatal("binary files are never searched")
			}
		})
	}
}

func TestSearchContentEmptyAndInvalid(t *testing.T) {
	root := searchRepo(t)
	for name, pick := range engines(t) {
		t.Run(name, func(t *testing.T) {
			withEngine(t, pick)
			ctx := context.Background()
			if res, err := SearchContent(ctx, root, ContentQuery{Pattern: "zzzz-nothing"}); err != nil || res.Matches != 0 {
				t.Fatalf("no match is a result, not an error: %+v %v", res, err)
			}
			if res, err := SearchContent(ctx, root, ContentQuery{Pattern: "n"}); err != nil || res.Matches != 0 {
				t.Fatalf("a one-character pattern is not searched: %+v %v", res, err)
			}
			if _, err := SearchContent(ctx, root, ContentQuery{Pattern: "(unclosed", Regex: true}); err == nil {
				t.Fatal("a broken regex must say so")
			}
		})
	}
}

func TestSearchContentBoundsMatches(t *testing.T) {
	root := searchRepo(t)
	line := strings.Repeat("filler needle\n", 25)
	for i := 0; i < 30; i++ {
		write(t, root, fmt.Sprintf("many/f%02d.txt", i), line)
	}
	write(t, root, "long.txt", "needle "+strings.Repeat("x", 600)+"\n")
	for name, pick := range engines(t) {
		t.Run(name, func(t *testing.T) {
			withEngine(t, pick)
			res, err := SearchContent(context.Background(), root, ContentQuery{Pattern: "needle"})
			if err != nil {
				t.Fatal(err)
			}
			if res.Matches > MaxContentMatches || !res.Truncated {
				t.Fatalf("matches %d truncated %v: the total is capped and says so", res.Matches, res.Truncated)
			}
			for _, f := range res.Files {
				if len(f.Matches) > MaxMatchesPerFile {
					t.Fatalf("%s kept %d matches", f.Path, len(f.Matches))
				}
				for _, m := range f.Matches {
					if n := len([]rune(m.Text)); n > MaxLineChars+1 {
						t.Fatalf("a long line must be cut, got %d chars", n)
					}
				}
			}
		})
	}
}

func TestSearchContentWithoutAnyEngine(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "needle\n")
	withEngine(t, func() string { return "" })
	if _, err := SearchContent(context.Background(), root, ContentQuery{Pattern: "needle"}); !errors.Is(err, ErrNoContentEngine) {
		t.Fatalf("a plain folder without ripgrep: %v", err)
	}
	if ResolveRipgrep() == "" {
		return
	}
	withEngine(t, ResolveRipgrep)
	res, err := SearchContent(context.Background(), root, ContentQuery{Pattern: "needle"})
	if err != nil || !slices.Equal(paths(res), []string{"a.txt"}) {
		t.Fatalf("ripgrep searches a plain folder: %+v %v", res, err)
	}
}
