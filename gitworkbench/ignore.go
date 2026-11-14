package gitworkbench

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
)

/**
 * [INPUT]: runner.go 的 Read / LockRepo、真实仓库里的 .gitignore 与 info/exclude
 * [OUTPUT]: IgnorePaths（LazyGit 的 “ignore / exclude file”）：把未跟踪的文件或文件夹以锚定模式（`/path`，文件夹带 `/`）追加到仓库的 .gitignore 或本机的 info/exclude；特殊字符转义、已有的行不重复、已跟踪的路径拒绝（不替用户取消跟踪）、文件末尾没有换行先补一个
 * [POS]: 文件写入而不是 git 命令：只追加、不覆盖，路径先全部校验完才写第一个字节；在 LockRepo 下与别的变更互斥
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// IgnoreScope is where an ignore rule is written.
type IgnoreScope string

const (
	// IgnoreInRepository writes <root>/.gitignore: the rule travels with the
	// repository when it is committed.
	IgnoreInRepository IgnoreScope = "gitignore"
	// IgnoreLocally writes the repository's info/exclude: only this clone sees it.
	IgnoreLocally IgnoreScope = "exclude"
)

// IgnorePaths adds one anchored rule per untracked path (a folder gets a
// trailing slash) to the scope's file and returns how many lines it added;
// a rule already there is not written again. A tracked path is refused:
// ignoring it would not untrack it, and Solco Git never untracks for you.
func (r *Runner) IgnorePaths(ctx context.Context, root string, paths []string, scope IgnoreScope) (int, error) {
	fail := func(format string, a ...any) error { return refusal("ignore", format, a...) }
	if len(paths) == 0 {
		return 0, fail("no paths to ignore")
	}
	if scope != IgnoreInRepository && scope != IgnoreLocally {
		return 0, fail("unknown ignore scope %q", scope)
	}
	rules := make([]string, 0, len(paths))
	clean := make([]string, 0, len(paths))
	for _, p := range paths {
		p = filepath.ToSlash(p)
		if p == "" || strings.ContainsAny(p, "\n\r") || path.IsAbs(p) {
			return 0, fail("a repository-relative path is required")
		}
		p = strings.TrimSuffix(path.Clean(p), "/")
		if p == "." || p == ".." || strings.HasPrefix(p, "../") || p == ".git" || strings.HasPrefix(p, ".git/") {
			return 0, fail("%s cannot be ignored", p)
		}
		rule := "/" + escapeIgnorePattern(p)
		if info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p))); err == nil && info.IsDir() {
			rule += "/"
		}
		clean = append(clean, p)
		rules = append(rules, rule)
	}

	tracked, err := r.Read(ctx, root, append([]string{"--literal-pathspecs", "ls-files", "--cached", "-z", "--"}, clean...)...)
	if err != nil {
		return 0, err
	}
	if len(bytes.TrimRight(tracked, "\x00")) > 0 {
		return 0, fail("a tracked file stays tracked when it is ignored; untrack it first")
	}

	target := filepath.Join(root, ".gitignore")
	if scope == IgnoreLocally {
		out, err := r.Read(ctx, root, "rev-parse", "--git-path", "info/exclude")
		if err != nil {
			return 0, err
		}
		target = strings.TrimSpace(string(out))
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
	}

	unlock, err := r.LockRepo(root, "ignore")
	if err != nil {
		return 0, err
	}
	defer unlock()

	existing, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		have[strings.TrimRight(line, "\r")] = true
	}
	var add strings.Builder
	added := 0
	for _, rule := range rules {
		if have[rule] {
			continue
		}
		have[rule] = true
		add.WriteString(rule + "\n")
		added++
	}
	if added == 0 {
		return 0, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return 0, err
	}
	text := add.String()
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		text = "\n" + text
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return 0, err
	}
	return added, f.Close()
}

// escapeIgnorePattern makes a file name safe as a gitignore pattern: the
// characters the pattern language reads (* ? [ ] \) and a trailing space,
// which gitignore would otherwise strip. The rule's leading slash already
// keeps a name that starts with # or ! from being read as a comment or a
// negation.
func escapeIgnorePattern(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '*' || c == '?' || c == '[' || c == ']' || c == '\\':
			b.WriteByte('\\')
		case c == ' ' && i == len(name)-1:
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}
