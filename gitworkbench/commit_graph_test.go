package gitworkbench

import (
	"strings"
	"testing"
)

/**
 * [INPUT]: 真实临时仓库（主线 + 两条分支 + 标签 + stash）
 * [OUTPUT]: 全分支日期序图谱、--exclude 隐藏只去掉独占提交、真实装饰解析、ResolveCommit/CommitByHash 守卫
 * [POS]: commit_graph.go 回归
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

func subjects(commits []CommitInfo) string {
	var out []string
	for _, c := range commits {
		out = append(out, c.Subject)
	}
	return strings.Join(out, ",")
}

// graphRepo: initial ← main-2; feature (off initial) has f1, f2; release (off
// main-2) has r1 and a tag v1; HEAD is main.
func graphRepo(t *testing.T) (string, *Runner) {
	t.Helper()
	root, r := tempRepo(t)
	commit := func(name string) {
		writeRepoFile(t, root, name+".txt", name+"\n")
		mustRunGit(t, root, "add", "-A")
		mustRunGit(t, root, "commit", "-qm", name)
	}
	mustRunGit(t, root, "branch", "-M", "main")
	mustRunGit(t, root, "switch", "-qc", "feature")
	commit("f1")
	commit("f2")
	mustRunGit(t, root, "switch", "-q", "main")
	commit("main-2")
	mustRunGit(t, root, "switch", "-qc", "release")
	commit("r1")
	mustRunGit(t, root, "tag", "-a", "v1", "-m", "v1")
	mustRunGit(t, root, "switch", "-q", "main")
	writeRepoFile(t, root, "stash.txt", "s")
	mustRunGit(t, root, "add", "-A")
	mustRunGit(t, root, "stash", "push", "-qm", "wip")
	return root, r
}

func TestListCommitGraphShowsEveryRefButNotStashes(t *testing.T) {
	root, r := graphRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()

	commits, err := r.ListCommitGraph(ctx, root, GraphQuery{})
	if err != nil {
		t.Fatal(err)
	}
	got := subjects(commits)
	for _, want := range []string{"f1", "f2", "main-2", "r1", "initial"} {
		if !strings.Contains(got, want) {
			t.Fatalf("graph is missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "wip") || strings.Contains(got, "index on") {
		t.Fatalf("stash commits must stay out of the graph: %s", got)
	}
	// A parent never comes before its child.
	pos := map[string]int{}
	for i, c := range commits {
		pos[c.Hash] = i
	}
	for i, c := range commits {
		for _, p := range c.Parents {
			if j, ok := pos[p]; ok && j < i {
				t.Fatalf("parent %s listed before child %s", p, c.Hash)
			}
		}
	}
}

func TestListCommitGraphDecorationsAreReal(t *testing.T) {
	root, r := graphRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	commits, err := r.ListCommitGraph(ctx, root, GraphQuery{})
	if err != nil {
		t.Fatal(err)
	}
	find := func(subject string) CommitInfo {
		for _, c := range commits {
			if c.Subject == subject {
				return c
			}
		}
		t.Fatalf("no commit %q", subject)
		return CommitInfo{}
	}
	main2 := find("main-2")
	if len(main2.Refs) != 1 || main2.Refs[0] != (CommitRef{Kind: RefBranch, Name: "main", Full: "refs/heads/main", Head: true}) {
		t.Fatalf("main-2 refs = %+v", main2.Refs)
	}
	r1 := find("r1")
	var sawRelease, sawTag bool
	for _, ref := range r1.Refs {
		sawRelease = sawRelease || ref == CommitRef{Kind: RefBranch, Name: "release", Full: "refs/heads/release"}
		sawTag = sawTag || ref == CommitRef{Kind: RefTag, Name: "v1", Full: "refs/tags/v1"}
	}
	if !sawRelease || !sawTag {
		t.Fatalf("r1 refs = %+v", r1.Refs)
	}
}

func TestListCommitGraphHidesOnlyWhatARefAloneReaches(t *testing.T) {
	root, r := graphRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()

	commits, err := r.ListCommitGraph(ctx, root, GraphQuery{Hidden: []string{"refs/heads/feature", "refs/heads/release", "refs/tags/v1"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := subjects(commits); got != "main-2,initial" {
		t.Fatalf("hiding feature+release must leave the main line only, got %s", got)
	}
	// A shared ancestor stays when a visible ref still reaches it.
	// (the v1 tag still reaches r1 while it is visible.)
	commits, _ = r.ListCommitGraph(ctx, root, GraphQuery{Hidden: []string{"refs/heads/release"}})
	if !strings.Contains(subjects(commits), "r1") {
		t.Fatalf("a visible tag keeps its commit: %s", subjects(commits))
	}
	commits, _ = r.ListCommitGraph(ctx, root, GraphQuery{Hidden: []string{"refs/heads/release", "refs/tags/v1"}})
	got := subjects(commits)
	if strings.Contains(got, "r1") || !strings.Contains(got, "main-2") || !strings.Contains(got, "f2") {
		t.Fatalf("hiding release: %s", got)
	}
	// Hiding the checked-out branch changes nothing: HEAD is always shown.
	commits, _ = r.ListCommitGraph(ctx, root, GraphQuery{Hidden: []string{"refs/heads/main"}})
	if !strings.Contains(subjects(commits), "main-2") {
		t.Fatalf("HEAD must stay visible: %s", subjects(commits))
	}
	// The limit bounds the window.
	commits, _ = r.ListCommitGraph(ctx, root, GraphQuery{Limit: 2})
	if len(commits) != 2 {
		t.Fatalf("limit 2 gave %d", len(commits))
	}
}

func TestResolveCommitAndCommitByHash(t *testing.T) {
	root, r := graphRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()

	tip, err := r.ResolveCommit(ctx, root, "refs/heads/feature")
	if err != nil {
		t.Fatal(err)
	}
	cm, err := r.CommitByHash(ctx, root, tip)
	if err != nil || cm == nil || cm.Subject != "f2" {
		t.Fatalf("feature tip = %+v, %v", cm, err)
	}
	peeled, err := r.ResolveCommit(ctx, root, "refs/tags/v1")
	if err != nil || peeled == "" {
		t.Fatalf("an annotated tag peels to its commit: %q, %v", peeled, err)
	}
	for _, bad := range []string{"feature", "--output=/tmp/x", "refs/heads/nope"} {
		if _, err := r.ResolveCommit(ctx, root, bad); err == nil {
			t.Fatalf("ResolveCommit(%q) must be refused", bad)
		}
	}
	if _, err := r.CommitByHash(ctx, root, "--output=/tmp/x"); err == nil {
		t.Fatal("a non-commit must be refused")
	}
}

func TestParseDecorationsSkipsSymbolicAndStash(t *testing.T) {
	refs := parseDecorations("HEAD -> refs/heads/main, refs/remotes/origin/HEAD, refs/remotes/origin/main, tag: refs/tags/v1, refs/stash")
	if len(refs) != 3 || refs[0].Name != "main" || !refs[0].Head || refs[1].Name != "origin/main" || refs[2].Kind != RefTag {
		t.Fatalf("refs = %+v", refs)
	}
}

// GraphPosition says which row a commit has in the full graph, so a window can
// be sized to reach it; commits the graph never shows are -1.
func TestGraphPositionMatchesTheGraphRows(t *testing.T) {
	root, r := graphRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()

	commits, err := r.ListCommitGraph(ctx, root, GraphQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range commits {
		got, err := r.GraphPosition(ctx, root, GraphQuery{}, c.Hash)
		if err != nil || got != i {
			t.Fatalf("%s: position %d, %v; want %d", c.Subject, got, err, i)
		}
	}
	if got, _ := r.GraphPosition(ctx, root, GraphQuery{}, strings.Repeat("a", 40)); got != -1 {
		t.Fatalf("an unknown commit is -1, got %d", got)
	}
	if got, _ := r.GraphPosition(ctx, root, GraphQuery{}, "--all"); got != -1 {
		t.Fatalf("a non-hash is refused with -1, got %d", got)
	}
	// A commit only a hidden ref reaches is out of the graph.
	f1 := strings.TrimSpace(runGit(t, root, "rev-parse", "feature"))
	if got, _ := r.GraphPosition(ctx, root, GraphQuery{Hidden: []string{RefHeadsPrefix + "feature"}}, f1); got != -1 {
		t.Fatalf("hidden branch tip must be out of the graph, got %d", got)
	}
}
