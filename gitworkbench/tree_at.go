package gitworkbench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

/**
 * [INPUT]: 依赖 Runner.Read（受限只读 Git）、一个提交/引用与目录路径
 * [OUTPUT]: 对外提供 TreeEntry、ListTree（某提交下一个目录的直接子项，目录在前、按名字排序）与 FetchedAt（远端上次抓取的时间，读 FETCH_HEAD 的修改时间）
 * [POS]: gitworkbench 的"那一刻的文件树"与"抓取新鲜度"只读入口；配合 ReadBlob 的 `<rev>:<path>` 读文件，Fork 的 File Tree 与侧栏页脚都读这里；不新增命令类别（ls-tree、rev-parse --git-path）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// TreeEntry is one child of a directory as it stood at a commit.
type TreeEntry struct {
	Name string // base name
	Path string // slash path from the repository root
	Mode string // octal mode as Git prints it ("100644", "040000", "160000", …)
	// Dir marks a sub-directory; Submodule a gitlink (a commit of another
	// repository, which has no file content here).
	Dir, Submodule bool
	// Size is the blob's byte size; -1 for directories and submodules.
	Size int64
}

// ListTree lists the direct children of dir ("" is the repository root) in
// rev's tree: directories first, then by name, case-insensitively. rev is
// anything Git resolves to a commit or tree (a hash, a branch, a tag); a
// revision starting with "-" or a dir that climbs out of the tree is refused
// before Git is asked. A directory that did not exist at rev is an empty
// list, not an error.
func (r *Runner) ListTree(ctx context.Context, root, rev, dir string) ([]TreeEntry, error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return nil, &Error{Class: ErrRefusal, OpClass: OpRead, Op: "ls-tree", err: errors.New("not a revision")}
	}
	dir = strings.Trim(filepath.ToSlash(dir), "/")
	for _, part := range strings.Split(dir, "/") {
		if part == ".." {
			return nil, &Error{Class: ErrRefusal, OpClass: OpRead, Op: "ls-tree", err: errors.New("path leaves the tree")}
		}
	}
	args := []string{"ls-tree", "-z", "--long", "--full-name", rev}
	if dir != "" {
		args = append(args, "--", dir+"/")
	}
	out, err := r.Read(ctx, root, args...)
	if err != nil {
		return nil, err
	}
	var entries []TreeEntry
	for _, record := range strings.Split(string(out), "\x00") {
		if record == "" {
			continue
		}
		// "<mode> <type> <object> <size>\t<path>": the size is right-aligned
		// with spaces, and "-" for anything that is not a blob.
		meta, path, ok := strings.Cut(record, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) < 4 {
			continue
		}
		entry := TreeEntry{Mode: fields[0], Path: path, Name: filepath.Base(path), Size: -1}
		switch fields[1] {
		case "tree":
			entry.Dir = true
		case "commit":
			entry.Submodule = true
		default:
			if size, err := strconv.ParseInt(fields[3], 10, 64); err == nil {
				entry.Size = size
			}
		}
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

// FetchedAt reports when the repository's remotes were last fetched: the
// newest modification time of FETCH_HEAD, the file every fetch rewrites, in
// the worktree's own git dir and in the repository's common one (a fetch run
// from any linked worktree updates the shared remote refs, but writes only
// its own FETCH_HEAD). ok is false when nothing was ever fetched — a
// repository with no remote included.
func (r *Runner) FetchedAt(ctx context.Context, root string) (at time.Time, ok bool) {
	for _, args := range [][]string{
		{"rev-parse", "--git-path", "FETCH_HEAD"},
		{"rev-parse", "--git-common-dir"},
	} {
		out, err := r.Read(ctx, root, args...)
		if err != nil {
			continue
		}
		path := strings.TrimSpace(string(out))
		if path == "" {
			continue
		}
		if args[1] == "--git-common-dir" {
			path = filepath.Join(path, "FETCH_HEAD")
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		if info, err := os.Stat(path); err == nil && (!ok || info.ModTime().After(at)) {
			at, ok = info.ModTime(), true
		}
	}
	return at, ok
}
