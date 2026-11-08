package gitworkbench

// [INPUT]: Runner 的 Read/Mutate/network/Stdin；Git 的 pull / merge --squash / checkout --detach / restore / apply / tag -a / worktree remove / checkout --ours|--theirs / log -g / submodule / stash push -- paths 语义。
// [OUTPUT]: 对齐 Fork 的第三批能力——Pull 三模式与 Fetch All(+Prune)、Squash Merge、分离 HEAD 检出、从提交还原文件、应用补丁、带说明的 Tag 与删除远端 Tag、移除 Worktree、逐文件冲突解决、Reflog、子模块新增/移除、按文件 Stash 与清空、托管站点链接。
// [POS]: 与 fork_more.go 同层；每个动作先在此校验（完整哈希、列得出的 ref/路径、干净工作区、未解决冲突的路径），冲突型集成动作失败即自行回滚并带冲突文件返回，UI 只编排与确认。
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ---- pull and fetch --------------------------------------------------------

// PullMode is how a pull integrates what it fetched.
type PullMode string

const (
	PullFastForward PullMode = "ff"
	PullMerge       PullMode = "merge"
	PullRebase      PullMode = "rebase"
)

// Pull fetches branch from remote and integrates it into the current branch.
// Fast-forward refuses a diverged history; merge may pause on conflicts (the
// merge banner then offers Continue/Abort); a rebase that stops on conflicts
// is aborted at once and reported with the paths, like RebaseOnto.
func (r *Runner) Pull(ctx context.Context, root, remote, branch string, mode PullMode) error {
	switch mode {
	case PullFastForward:
		return r.PullFFOnly(ctx, root, remote, branch)
	case PullMerge:
		_, err := r.network(ctx, root, "pull", "--no-rebase", "--no-edit", remote, branch)
		return err
	case PullRebase:
		if err := r.RequireCleanWorktree(ctx, root); err != nil {
			return err
		}
		_, err := r.network(ctx, root, "pull", "--rebase", remote, branch)
		if err == nil {
			return nil
		}
		state, _ := r.SequencerState(ctx, root)
		_, _ = r.Mutate(ctx, root, "rebase", "--abort") // no-op error when none started
		if state.Unmerged > 0 {
			return &Error{Class: ErrConflict, OpClass: OpMutate, Op: "rebase", Paths: state.UnmergedPaths,
				err: fmt.Errorf("rebase stopped on conflicts in %d files and was aborted; nothing changed", state.Unmerged)}
		}
		return err
	}
	return refusal("pull", "unknown pull mode %q", mode)
}

// FetchAll fetches every configured remote, optionally dropping remote-tracking
// branches the servers no longer have.
func (r *Runner) FetchAll(ctx context.Context, root string, prune bool) error {
	args := []string{"fetch", "--all"}
	if prune {
		args = append(args, "--prune")
	}
	_, err := r.network(ctx, root, args...)
	return err
}

// ---- squash merge ----------------------------------------------------------

// MergeSquash folds every change of target into the index without committing,
// so the user reviews the result and commits it as one. It needs a clean
// working tree; if it stops on conflicts it is rolled back (`reset --merge`)
// because a squash leaves no MERGE_HEAD for a Continue/Abort banner to hold.
func (r *Runner) MergeSquash(ctx context.Context, root, target string) error {
	ref, err := r.resolveBranchRef(ctx, root, target)
	if err != nil {
		return err
	}
	current := r.CurrentBranch(ctx, root)
	if current == "" || current == target {
		return refusal("merge", "select a different branch while on a checked-out branch")
	}
	if err := r.RequireCleanWorktree(ctx, root); err != nil {
		return err
	}
	_, err = r.Mutate(ctx, root, "merge", "--squash", ref)
	if err == nil {
		return nil
	}
	state, _ := r.SequencerState(ctx, root)
	_, _ = r.Mutate(ctx, root, "reset", "--merge")
	if state.Unmerged > 0 {
		return &Error{Class: ErrConflict, OpClass: OpMutate, Op: "merge", Paths: state.UnmergedPaths,
			err: fmt.Errorf("squash merge stopped on conflicts in %d files and was cancelled; nothing changed", state.Unmerged)}
	}
	return err
}

// ---- checkout, restore, apply ----------------------------------------------

// CheckoutDetached moves HEAD to one exact commit (detached). Git itself
// refuses when local changes would be overwritten.
func (r *Runner) CheckoutDetached(ctx context.Context, root, hash string) error {
	if !validFullCommitID(hash) {
		return refusal("checkout", "a full Git commit hash is required")
	}
	if _, err := r.Read(ctx, root, "rev-parse", "--verify", "--quiet", hash+"^{commit}"); err != nil {
		return &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "checkout", err: errors.New("that commit no longer exists")}
	}
	_, err := r.Mutate(ctx, root, "checkout", "--detach", hash)
	return err
}

// RestoreFile writes one file as it was in a commit into the working tree
// (unstaged, so it shows up as a change to review or discard).
func (r *Runner) RestoreFile(ctx context.Context, root, hash, rel string) error {
	if !validFullCommitID(hash) {
		return refusal("restore", "a full Git commit hash is required")
	}
	rel, ok := cleanRelPath(rel)
	if !ok {
		return refusal("restore", "not a path inside the repository")
	}
	if _, err := r.Read(ctx, root, "cat-file", "-e", hash+":"+rel); err != nil {
		return refusal("restore", "%s does not exist in that commit", rel)
	}
	_, err := r.Mutate(ctx, root, "restore", "--source="+hash, "--worktree", "--", rel)
	return err
}

// MaxPatchFileBytes bounds a patch file Solco will apply.
const MaxPatchFileBytes = 8 << 20

// ApplyPatch applies a patch file (a diff, or a format-patch mailbox) to the
// working tree only — never the index, never a commit. The patch is checked
// first, so a patch that does not fit changes nothing.
func (r *Runner) ApplyPatch(ctx context.Context, root, file string) error {
	if !filepath.IsAbs(file) {
		return refusal("apply", "choose a patch file")
	}
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() {
		return refusal("apply", "that is not a readable file")
	}
	if info.Size() == 0 {
		return refusal("apply", "the patch file is empty")
	}
	if info.Size() > MaxPatchFileBytes {
		return refusal("apply", "the patch file is larger than %d MiB", MaxPatchFileBytes>>20)
	}
	if _, err := r.Read(ctx, root, "apply", "--check", "--", file); err != nil {
		return err
	}
	_, err = r.Mutate(ctx, root, "apply", "--whitespace=nowarn", "--", file)
	return err
}

// ---- tags ------------------------------------------------------------------

// CreateTagAnnotated creates a tag at hash (HEAD when empty). A message makes
// it an annotated tag (travels by stdin, never argv); without one it is the
// lightweight tag CreateTag makes.
func (r *Runner) CreateTagAnnotated(ctx context.Context, root, name, hash, message string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("tag name is required")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid tag name %q", name)
	}
	if _, err := r.Read(ctx, root, "check-ref-format", "--allow-onelevel", name); err != nil {
		return fmt.Errorf("invalid tag name %q", name)
	}
	if hash != "" && !validFullCommitID(hash) {
		return refusal("tag", "a full Git commit hash is required")
	}
	message = strings.TrimSpace(message)
	args := []string{"tag"}
	if message != "" {
		args = append(args, "-a", "-F", "-")
	}
	args = append(args, "--", name)
	if hash != "" {
		args = append(args, hash)
	}
	if message == "" {
		_, err := r.Mutate(ctx, root, args...)
		return err
	}
	_, err := r.Stdin(ctx, root, message+"\n", args...)
	return err
}

// DeleteRemoteTag removes one tag from a configured remote's server.
func (r *Runner) DeleteRemoteTag(ctx context.Context, root, remote, name string) error {
	if err := r.requireRemote(ctx, root, remote); err != nil {
		return err
	}
	if _, err := r.Read(ctx, root, "rev-parse", "--verify", "--quiet", "refs/tags/"+name); err != nil {
		return &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "push", err: fmt.Errorf("%q is not a tag", name)}
	}
	_, err := r.network(ctx, root, "push", remote, "--delete", "refs/tags/"+name)
	return err
}

// ---- worktrees -------------------------------------------------------------

// RemoveWorktree deletes one linked work tree. The main work tree is never a
// target, and Git itself refuses one with changes or a lock.
func (r *Runner) RemoveWorktree(ctx context.Context, root, path string) error {
	list, err := r.ListWorktrees(ctx, root)
	if err != nil {
		return err
	}
	for i, wt := range list {
		if wt.Path != path {
			continue
		}
		if i == 0 {
			return refusal("worktree", "the main work tree cannot be removed")
		}
		_, err := r.Mutate(ctx, root, "worktree", "remove", "--", path)
		return err
	}
	return refusal("worktree", "%q is not a work tree of this repository", path)
}

// ---- conflict resolution ---------------------------------------------------

// ConflictSide is which version of a conflicted file to keep.
type ConflictSide string

const (
	// ConflictOurs keeps the current branch's version.
	ConflictOurs ConflictSide = "ours"
	// ConflictTheirs takes the incoming version.
	ConflictTheirs ConflictSide = "theirs"
	// ConflictMarked accepts the file as it is on disk (the user edited it).
	ConflictMarked ConflictSide = "marked"
)

// UnmergedPaths lists the files that still have unresolved conflicts.
func (r *Runner) UnmergedPaths(ctx context.Context, root string) ([]string, error) {
	out, err := r.Read(ctx, root, "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// ResolveConflict resolves one unmerged file: keep a side, or accept the file
// as the user edited it (refused while conflict markers remain).
func (r *Runner) ResolveConflict(ctx context.Context, root, rel string, side ConflictSide) error {
	unmerged, err := r.UnmergedPaths(ctx, root)
	if err != nil {
		return err
	}
	found := false
	for _, p := range unmerged {
		if p == rel {
			found = true
		}
	}
	if !found {
		return &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "resolve", err: fmt.Errorf("%s has no unresolved conflict (anymore)", rel)}
	}
	switch side {
	case ConflictOurs, ConflictTheirs:
		if _, err := r.Mutate(ctx, root, "checkout", "--"+string(side), "--", rel); err != nil {
			// A side that deleted the file has no version to check out: keeping
			// that side means keeping it deleted.
			ge, ok := GitError(err)
			if !ok || !strings.Contains(ge.Stderr, "does not have") {
				return err
			}
			_, err := r.Mutate(ctx, root, "rm", "-q", "--", rel)
			return err
		}
		_, err := r.Mutate(ctx, root, "add", "--", rel)
		return err
	case ConflictMarked:
		if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))); err == nil && hasConflictMarkers(data) {
			return refusal("resolve", "%s still contains conflict markers — edit them out first", rel)
		}
		_, err := r.Mutate(ctx, root, "add", "--", rel)
		return err
	}
	return refusal("resolve", "unknown resolution %q", side)
}

// hasConflictMarkers reports a line-start <<<<<<< ... >>>>>>> pair.
func hasConflictMarkers(data []byte) bool {
	return bytes.Contains(data, []byte("\n<<<<<<< ")) || bytes.HasPrefix(data, []byte("<<<<<<< "))
}

// ---- reflog ----------------------------------------------------------------

// ReflogEntry is one move of HEAD.
type ReflogEntry struct {
	Hash     string
	Selector string // HEAD@{n}
	Action   string // "commit: fix typo", "checkout: moving from a to b"
	Subject  string // the commit's own subject
	Time     time.Time
}

// MaxReflogEntries bounds the reflog list.
const MaxReflogEntries = 300

// Reflog lists where HEAD has been, newest first.
func (r *Runner) Reflog(ctx context.Context, root string, limit int) ([]ReflogEntry, error) {
	if limit <= 0 || limit > MaxReflogEntries {
		limit = MaxReflogEntries
	}
	out, err := r.Read(ctx, root, "log", "-g", "--no-color", "-n", strconv.Itoa(limit),
		"--format=%H%x1f%gd%x1f%gs%x1f%ct%x1f%s", "HEAD")
	if err != nil {
		if ge, ok := GitError(err); ok && strings.Contains(ge.Stderr, "does not have any commits") {
			return nil, nil
		}
		return nil, err
	}
	var entries []ReflogEntry
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		parts := strings.Split(line, "\x1f")
		if len(parts) < 5 || !validFullCommitID(parts[0]) {
			continue
		}
		secs, _ := strconv.ParseInt(parts[3], 10, 64)
		entries = append(entries, ReflogEntry{Hash: parts[0], Selector: parts[1], Action: parts[2], Subject: parts[4], Time: time.Unix(secs, 0)})
	}
	return entries, nil
}

// ---- submodules ------------------------------------------------------------

// AddSubmodule clones url into rel and records it (staged: .gitmodules and the
// gitlink). It contacts the network.
func (r *Runner) AddSubmodule(ctx context.Context, root, address, rel string) error {
	if err := validRemoteURL(address); err != nil {
		return refusal("submodule", "%v", err)
	}
	rel, ok := cleanRelPath(rel)
	if !ok {
		return refusal("submodule", "the folder must be a path inside the repository")
	}
	subs, err := r.ListSubmodules(ctx, root)
	if err != nil {
		return err
	}
	for _, s := range subs {
		if s.Path == rel {
			return refusal("submodule", "%s is already a submodule", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
		return refusal("submodule", "%s already exists", rel)
	}
	if isLocalAddress(address) {
		return r.addLocalSubmodule(ctx, root, address, rel)
	}
	_, err = r.network(ctx, root, "submodule", "add", "--", address, rel)
	return err
}

// isLocalAddress reports an absolute path or file:// address.
func isLocalAddress(address string) bool {
	return filepath.IsAbs(address) || strings.HasPrefix(strings.ToLower(address), "file://")
}

// addLocalSubmodule adds a submodule cloned from a local repository. Git
// refuses the file transport for submodules by default (it guards against a
// hostile .gitmodules), but here the user typed the path themselves and
// nothing is cloned recursively, so the transport is allowed for this one
// command only.
func (r *Runner) addLocalSubmodule(ctx context.Context, root, address, rel string) error {
	unlock, err := r.LockRepo(root, "submodule")
	if err != nil {
		return err
	}
	defer unlock()
	env := append(mutateEnv(), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=protocol.file.allow", "GIT_CONFIG_VALUE_0=always")
	_, err = r.run(ctx, OpMutate, NetworkTimeout, MaxReadOutput, env, root, []string{"submodule", "add", "--", address, rel})
	return err
}

// RemoveSubmodule unregisters one listed submodule and stages its removal
// (`submodule deinit` then `rm`); Git's own modules folder stays for a later
// re-add.
func (r *Runner) RemoveSubmodule(ctx context.Context, root, rel string) error {
	subs, err := r.ListSubmodules(ctx, root)
	if err != nil {
		return err
	}
	for _, s := range subs {
		if s.Path != rel {
			continue
		}
		if _, err := r.Mutate(ctx, root, "submodule", "deinit", "-f", "--", rel); err != nil {
			return err
		}
		_, err := r.Mutate(ctx, root, "rm", "-f", "--", rel)
		return err
	}
	return refusal("submodule", "%q is not a submodule", rel)
}

// ---- stash -----------------------------------------------------------------

// StashPushPaths stashes only the listed files (untracked ones included).
func (r *Runner) StashPushPaths(ctx context.Context, root, message string, paths []string) error {
	if len(paths) == 0 {
		return refusal("stash", "choose at least one file")
	}
	clean := make([]string, 0, len(paths))
	for _, p := range paths {
		rel, ok := cleanRelPath(p)
		if !ok {
			return refusal("stash", "%q is not a path inside the repository", p)
		}
		clean = append(clean, rel)
	}
	args := []string{"stash", "push", "-u"}
	if message = strings.TrimSpace(message); message != "" {
		args = append(args, "-m", message)
	}
	args = append(args, "--")
	args = append(args, clean...)
	_, err := r.Mutate(ctx, root, args...)
	return err
}

// StashClear drops every stash entry.
func (r *Runner) StashClear(ctx context.Context, root string) error {
	_, err := r.Mutate(ctx, root, "stash", "clear")
	return err
}

// ---- hosting links ---------------------------------------------------------

// WebRepo is a remote read as a web repository page.
type WebRepo struct {
	Host string // "github.com"
	Base string // "https://github.com/owner/repo"
}

// ParseWebRepo reads a remote address (https, ssh://, git://, scp-like) as the
// repository's web page. Local paths and anything unparsable are refused.
func ParseWebRepo(address string) (WebRepo, bool) {
	address = strings.TrimSpace(address)
	if address == "" || strings.ContainsAny(address, " \t\r\n\x00?#") {
		return WebRepo{}, false
	}
	var host, path string
	switch {
	case strings.Contains(address, "://"):
		u, err := url.Parse(address)
		if err != nil || u.Hostname() == "" {
			return WebRepo{}, false
		}
		switch u.Scheme {
		case "https", "http", "ssh", "git":
		default:
			return WebRepo{}, false
		}
		host, path = u.Hostname(), strings.TrimPrefix(u.Path, "/")
	case strings.Contains(address, ":") && !strings.HasPrefix(address, "/"):
		// scp-like: [user@]host:owner/repo
		left, right, _ := strings.Cut(address, ":")
		if i := strings.LastIndex(left, "@"); i >= 0 {
			left = left[i+1:]
		}
		host, path = left, strings.TrimPrefix(right, "/")
	default:
		return WebRepo{}, false
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	if host == "" || path == "" || !strings.Contains(host, ".") || strings.Contains(path, "..") {
		return WebRepo{}, false
	}
	return WebRepo{Host: strings.ToLower(host), Base: "https://" + strings.ToLower(host) + "/" + path}, true
}

// Name is the site's friendly name for menus ("GitHub"), else the host.
func (w WebRepo) Name() string {
	switch {
	case w.Host == "github.com":
		return "GitHub"
	case w.Host == "bitbucket.org":
		return "Bitbucket"
	case w.Host == "codeberg.org":
		return "Codeberg"
	case strings.Contains(w.Host, "gitlab"):
		return "GitLab"
	}
	return w.Host
}

// CommitURL is the commit's page on the hosting site.
func (w WebRepo) CommitURL(hash string) string {
	switch {
	case strings.Contains(w.Host, "gitlab"):
		return w.Base + "/-/commit/" + hash
	case w.Host == "bitbucket.org":
		return w.Base + "/commits/" + hash
	}
	return w.Base + "/commit/" + hash
}
