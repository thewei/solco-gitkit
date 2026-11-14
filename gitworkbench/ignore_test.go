package gitworkbench

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func ignored(t *testing.T, root, rel string) bool {
	t.Helper()
	// check-ignore exits 1 when the path is not ignored.
	cmd := exec.Command("git", "check-ignore", "-q", "--no-index", "--", rel)
	cmd.Dir = root
	return cmd.Run() == nil
}

func TestIgnorePathsWritesAnchoredRulesGitHonours(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	names := []string{"plain.log", "#hash", "!bang", "star*.txt", "q?.txt", "br[ck].txt", "back\\slash", "sp ace "}
	for _, n := range names {
		writeRepoFile(t, root, n, "x\n")
	}
	if err := os.MkdirAll(filepath.Join(root, "build", "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, root, "build/out/a.o", "o\n")
	writeRepoFile(t, root, "keep.txt", "k\n")

	n, err := r.IgnorePaths(ctx, root, append(names, "build"), IgnoreInRepository)
	if err != nil || n != len(names)+1 {
		t.Fatalf("added %d, %v", n, err)
	}
	for _, name := range append(names, "build/out/a.o") {
		if !ignored(t, root, name) {
			t.Errorf("%q is not ignored after IgnorePaths", name)
		}
	}
	if ignored(t, root, "keep.txt") || ignored(t, root, "other/plain.log") {
		t.Error("the rules are anchored: nothing else is ignored")
	}
	data, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if !strings.Contains(string(data), "/build/\n") {
		t.Errorf("a folder gets a trailing slash: %q", data)
	}
}

func TestIgnorePathsDoesNotRepeatARuleAndKeepsTheFileIntact(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	writeRepoFile(t, root, ".gitignore", "*.tmp") // no trailing newline
	runGit(t, root, "add", ".gitignore")
	runGit(t, root, "commit", "-q", "-m", "ignore file")
	writeRepoFile(t, root, "a.txt", "a\n")
	writeRepoFile(t, root, "b.txt", "b\n")
	if n, err := r.IgnorePaths(ctx, root, []string{"a.txt"}, IgnoreInRepository); err != nil || n != 1 {
		t.Fatalf("first: %d, %v", n, err)
	}
	if n, err := r.IgnorePaths(ctx, root, []string{"a.txt", "b.txt"}, IgnoreInRepository); err != nil || n != 1 {
		t.Fatalf("a rule already there is skipped: %d, %v", n, err)
	}
	if n, err := r.IgnorePaths(ctx, root, []string{"b.txt", "b.txt"}, IgnoreInRepository); err != nil || n != 0 {
		t.Fatalf("nothing new, nothing written: %d, %v", n, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if string(data) != "*.tmp\n/a.txt\n/b.txt\n" {
		t.Errorf("the missing newline is supplied and the old line kept: %q", data)
	}
}

func TestIgnorePathsLocallyWritesInfoExcludeNotTheWorktree(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	writeRepoFile(t, root, "secret.env", "s\n")
	if n, err := r.IgnorePaths(ctx, root, []string{"secret.env"}, IgnoreLocally); err != nil || n != 1 {
		t.Fatalf("%d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".gitignore")); err == nil {
		t.Error("a local exclude leaves the worktree alone")
	}
	data, err := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if err != nil || !strings.Contains(string(data), "/secret.env\n") {
		t.Fatalf("info/exclude: %q, %v", data, err)
	}
	if !ignored(t, root, "secret.env") {
		t.Error("Git honours the exclude")
	}
	if st := runGit(t, root, "status", "--porcelain"); strings.Contains(st, "secret.env") {
		t.Errorf("the file no longer shows in status: %q", st)
	}
}

func TestIgnorePathsRefusesBeforeWritingAnything(t *testing.T) {
	ctx, cancel := opCtx(t)
	defer cancel()
	root, r := tempRepo(t)
	writeRepoFile(t, root, "new.txt", "n\n")
	for name, paths := range map[string][]string{
		"tracked":   {"new.txt", "base.txt"},
		"outside":   {"new.txt", "../elsewhere"},
		"absolute":  {"/etc/passwd"},
		"git dir":   {".git/config"},
		"the root":  {"."},
		"empty":     nil,
		"a newline": {"a\nb"},
	} {
		if _, err := r.IgnorePaths(ctx, root, paths, IgnoreInRepository); err == nil {
			t.Errorf("%s: expected a refusal", name)
		}
	}
	if _, err := r.IgnorePaths(ctx, root, []string{"new.txt"}, IgnoreScope("elsewhere")); err == nil {
		t.Error("an unknown scope is refused")
	}
	if _, err := os.Stat(filepath.Join(root, ".gitignore")); err == nil {
		t.Error("a refusal writes nothing, not even the other, valid path")
	}
}
