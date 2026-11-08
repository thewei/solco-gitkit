package gitworkbench

// [INPUT]: Runner 的 Read/Mutate/network；Git 的 reset / push --delete / branch --set-upstream-to / format-patch / blame / submodule / remote 语义。
// [OUTPUT]: 对齐 Fork 的第二批能力——重置到提交、删除远程分支、跟踪分支、导出补丁、与工作区对比、文件历史/Blame、远程管理、子模块、交互式变基。
// [POS]: Git 域的"重一点"动作；每个都先在此校验（列得出的 ref、完整哈希、干净工作区），UI 只编排与确认。
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func refusal(op string, format string, args ...any) error {
	return &Error{Class: ErrRefusal, OpClass: OpMutate, Op: op, err: fmt.Errorf(format, args...)}
}

// ---- reset -----------------------------------------------------------------

// ResetMode is one of Git's three reset depths.
type ResetMode string

const (
	ResetSoft  ResetMode = "soft"  // move the branch only; changes stay staged
	ResetMixed ResetMode = "mixed" // move the branch and unstage; files untouched
	ResetHard  ResetMode = "hard"  // move the branch and rewrite the files
)

// ResetTo moves the current branch to one commit. Hard additionally demands a
// clean working tree, so no uncommitted work can be lost; the commits it drops
// stay reachable through the reflog and the previous HEAD is returned so the UI
// can say how to get back.
func (r *Runner) ResetTo(ctx context.Context, root, hash string, mode ResetMode) (previous string, err error) {
	if !validFullCommitID(hash) {
		return "", refusal("reset", "a full Git commit hash is required")
	}
	switch mode {
	case ResetSoft, ResetMixed, ResetHard:
	default:
		return "", refusal("reset", "unknown reset mode %q", mode)
	}
	if _, err := r.Read(ctx, root, "rev-parse", "--verify", "--quiet", hash+"^{commit}"); err != nil {
		return "", &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "reset", err: fmt.Errorf("%q is not a commit", hash)}
	}
	if r.CurrentBranch(ctx, root) == "" {
		return "", refusal("reset", "check out a branch first; HEAD is detached")
	}
	if mode == ResetHard {
		if err := r.RequireCleanWorktree(ctx, root); err != nil {
			return "", err
		}
	}
	head, _ := r.Read(ctx, root, "rev-parse", "HEAD")
	previous = strings.TrimSpace(string(head))
	if _, err := r.Mutate(ctx, root, "reset", "--"+string(mode), hash); err != nil {
		return "", err
	}
	return previous, nil
}

// ---- remote branches and tracking ------------------------------------------

func (r *Runner) requireRemoteBranch(ctx context.Context, root, remote, name, op string) error {
	remotes, err := r.ListRemoteBranches(ctx, root)
	if err != nil {
		return err
	}
	for _, b := range remotes {
		if b.Remote == remote && b.Name == name {
			return nil
		}
	}
	return refusal(op, "%s/%s is not a remote branch", remote, name)
}

// DeleteRemoteBranch removes one branch from its remote (`git push <remote>
// :refs/heads/<name>`). The branch must be one Git lists as remote-tracking.
func (r *Runner) DeleteRemoteBranch(ctx context.Context, root, remote, name string) error {
	if err := r.requireRemoteBranch(ctx, root, remote, name, "push"); err != nil {
		return err
	}
	_, err := r.network(ctx, root, "push", remote, ":"+RefHeadsPrefix+name)
	return err
}

// SetUpstream makes a local branch track a listed remote-tracking branch
// ("origin/main").
func (r *Runner) SetUpstream(ctx context.Context, root, branch, upstream string) error {
	if err := r.validateLocalBranch(ctx, root, branch); err != nil {
		return err
	}
	remote, name, ok := strings.Cut(upstream, "/")
	if !ok {
		return refusal("branch", "%q is not a remote branch", upstream)
	}
	if err := r.requireRemoteBranch(ctx, root, remote, name, "branch"); err != nil {
		return err
	}
	_, err := r.Mutate(ctx, root, "branch", "--set-upstream-to="+upstream, branch)
	return err
}

// UnsetUpstream stops a local branch tracking anything.
func (r *Runner) UnsetUpstream(ctx context.Context, root, branch string) error {
	if err := r.validateLocalBranch(ctx, root, branch); err != nil {
		return err
	}
	_, err := r.Mutate(ctx, root, "branch", "--unset-upstream", branch)
	return err
}

// BranchUpstream reads what a local branch tracks ("" when nothing).
func (r *Runner) BranchUpstream(ctx context.Context, root, branch string) string {
	out, err := r.Read(ctx, root, "for-each-ref", "--format=%(upstream:short)", RefHeadsPrefix+branch)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ---- patch and compare -----------------------------------------------------

// FormatPatch is one commit as an mbox patch (`git format-patch -1 --stdout`),
// the text "Save as Patch" writes.
func (r *Runner) FormatPatch(ctx context.Context, root, hash string) ([]byte, error) {
	if !validFullCommitID(hash) {
		return nil, refusal("format-patch", "a full Git commit hash is required")
	}
	out, err := r.Read(ctx, root, "format-patch", "-1", "--stdout", "--no-color", hash)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, refusal("format-patch", "this commit has no changes to export (a merge commit?)")
	}
	return out, nil
}

// StashPatch is one stash entry as a plain diff patch, untracked files
// included (`git stash show -p --include-untracked`), for "Save as Patch".
func (r *Runner) StashPatch(ctx context.Context, root, ref string) ([]byte, error) {
	if !validStashRef(ref) {
		return nil, refusal("stash", "invalid stash ref %q", ref)
	}
	out, err := r.Read(ctx, root, "stash", "show", "-p", "--include-untracked", "--binary", "--no-color", ref)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, refusal("stash", "this stash has no changes to export")
	}
	return out, nil
}

// WorkingPatch is everything that differs from HEAD right now — staged,
// unstaged and untracked (ignored files stay out) — as one diff patch.
// It is built in a throw-away index, so the repository's own index, working
// tree and refs are never touched (only unreferenced blob objects may be
// written, which `git gc` prunes).
func (r *Runner) WorkingPatch(ctx context.Context, root string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "solco-patch-index-*")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(name)
	_ = os.Remove(name) // git wants to create the index itself
	env := append(mutateEnv(), "GIT_INDEX_FILE="+name)

	base := "HEAD"
	if _, herr := r.Read(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); herr != nil {
		// A repository without commits diffs against the empty tree.
		out, terr := r.Read(ctx, root, "hash-object", "-t", "tree", "--stdin")
		if terr != nil {
			return nil, terr
		}
		base = strings.TrimSpace(string(out))
	} else if _, err := r.mutateEnv(ctx, root, env, "read-tree", "HEAD"); err != nil {
		return nil, err
	}
	if _, err := r.mutateEnv(ctx, root, env, "add", "-A", "--", "."); err != nil {
		return nil, err
	}
	out, err := r.run(ctx, OpRead, ReadTimeout, MaxReadOutput, append(readEnv(), "GIT_INDEX_FILE="+name), root,
		[]string{"diff", "--cached", "--binary", "--no-color", base})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, refusal("diff", "there are no changes to export")
	}
	return out, nil
}

// CompareToWorktree is the review of one commit against the working tree
// (staged and unstaged changes included, untracked files not): what changed
// between that commit and what is on disk now.
func (r *Runner) CompareToWorktree(ctx context.Context, root, hash string) (*ChangesSnapshot, error) {
	if _, err := r.Read(ctx, root, "rev-parse", "--verify", "--quiet", hash+"^{commit}"); err != nil {
		return nil, &Error{Class: ErrStaleState, OpClass: OpRead, Op: "diff", err: fmt.Errorf("%q is not a commit", hash)}
	}
	snap := &ChangesSnapshot{Root: root, Head: hash, Signature: "compare:" + hash, GeneratedAt: r.now(), DiffBase: hash}
	budget := &snapshotBudget{limit: MaxSnapshotBytes}
	files, adds, dels, truncated, err := r.rangeFiles(ctx, root, hash, budget)
	if err != nil {
		snap.Err = err.Error()
		return snap, err
	}
	snap.Files, snap.TotalAdditions, snap.TotalDeletions, snap.Truncated = files, adds, dels, truncated
	sortFilesByPath(snap.Files)
	return snap, nil
}

// ---- file history and blame ------------------------------------------------

// cleanRelPath refuses anything but a plain repository-relative path.
func cleanRelPath(rel string) (string, bool) {
	rel = path.Clean(filepath.ToSlash(rel))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "-") {
		return "", false
	}
	return rel, true
}

// FileHistory lists the commits that touched one path, following renames,
// newest first.
func (r *Runner) FileHistory(ctx context.Context, root, rel string, limit int) ([]CommitInfo, error) {
	rel, ok := cleanRelPath(rel)
	if !ok {
		return nil, refusal("log", "not a repository path")
	}
	return r.readCommits(ctx, root, limit, "--follow", "--", rel)
}

// BlameLine is one line of a file with the commit that last wrote it.
type BlameLine struct {
	Line    int
	Hash    string
	Short   string
	Author  string
	Time    time.Time
	Summary string
	Text    string
}

// MaxBlameLines bounds one blame read.
const MaxBlameLines = 5000

// Blame annotates a file at HEAD (or rev) line by line.
func (r *Runner) Blame(ctx context.Context, root, rev, rel string) ([]BlameLine, error) {
	rel, ok := cleanRelPath(rel)
	if !ok {
		return nil, refusal("blame", "not a repository path")
	}
	args := []string{"blame", "--line-porcelain"}
	if rev != "" {
		if !validFullCommitID(rev) {
			return nil, refusal("blame", "a full Git commit hash is required")
		}
		args = append(args, rev)
	}
	args = append(args, "--", rel)
	out, err := r.Read(ctx, root, args...)
	if err != nil {
		return nil, err
	}
	return parseBlame(out), nil
}

func parseBlame(out []byte) []BlameLine {
	var lines []BlameLine
	var cur BlameLine
	for _, raw := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(raw, "\t"):
			cur.Text = raw[1:]
			lines = append(lines, cur)
			if len(lines) >= MaxBlameLines {
				return lines
			}
			cur = BlameLine{}
		case cur.Hash == "" || (len(raw) > 40 && isHex(raw[:40]) && raw[40] == ' '):
			f := strings.Fields(raw)
			if len(f) >= 3 && isHex(f[0]) {
				cur = BlameLine{Hash: f[0], Short: f[0][:min(7, len(f[0]))]}
				cur.Line, _ = strconv.Atoi(f[2])
			}
		case strings.HasPrefix(raw, "author "):
			cur.Author = strings.TrimPrefix(raw, "author ")
		case strings.HasPrefix(raw, "author-time "):
			sec, _ := strconv.ParseInt(strings.TrimPrefix(raw, "author-time "), 10, 64)
			cur.Time = time.Unix(sec, 0)
		case strings.HasPrefix(raw, "summary "):
			cur.Summary = strings.TrimPrefix(raw, "summary ")
		}
	}
	return lines
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' && ch < 'a' || ch > 'f' {
			return false
		}
	}
	return true
}

// ---- remotes ---------------------------------------------------------------

// Remote is one configured remote.
type Remote struct {
	Name     string
	FetchURL string
	PushURL  string // "" when it equals FetchURL
}

var remoteNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ListRemotes reads the configured remotes (no network).
func (r *Runner) ListRemotes(ctx context.Context, root string) ([]Remote, error) {
	out, err := r.Read(ctx, root, "remote")
	if err != nil {
		return nil, err
	}
	var remotes []Remote
	for _, name := range strings.Fields(string(out)) {
		rm := Remote{Name: name}
		if u, err := r.Read(ctx, root, "remote", "get-url", name); err == nil {
			rm.FetchURL = strings.TrimSpace(string(u))
		}
		if u, err := r.Read(ctx, root, "remote", "get-url", "--push", name); err == nil {
			if push := strings.TrimSpace(string(u)); push != rm.FetchURL {
				rm.PushURL = push
			}
		}
		remotes = append(remotes, rm)
	}
	return remotes, nil
}

func validRemoteURL(url string) error {
	switch {
	case strings.TrimSpace(url) == "":
		return errors.New("the address is empty")
	case strings.HasPrefix(url, "-"):
		return errors.New("the address cannot start with a dash")
	case strings.ContainsAny(url, "\n\r\x00"):
		return errors.New("the address has control characters")
	case strings.HasPrefix(strings.ToLower(url), "ext::"):
		return errors.New("ext:: remotes run commands and are not accepted")
	}
	return nil
}

func validRemoteName(name string) error {
	if !remoteNameRe.MatchString(name) {
		return errors.New("a remote name uses letters, digits, '.', '_' and '-', and starts with a letter or digit")
	}
	return nil
}

// AddRemote registers a new remote.
func (r *Runner) AddRemote(ctx context.Context, root, name, url string) error {
	if err := validRemoteName(name); err != nil {
		return refusal("remote", "%v", err)
	}
	if err := validRemoteURL(url); err != nil {
		return refusal("remote", "%v", err)
	}
	_, err := r.Mutate(ctx, root, "remote", "add", "--", name, url)
	return err
}

// SetRemoteURL repoints an existing remote's fetch (and push) address.
func (r *Runner) SetRemoteURL(ctx context.Context, root, name, url string) error {
	if err := validRemoteURL(url); err != nil {
		return refusal("remote", "%v", err)
	}
	if err := r.requireRemote(ctx, root, name); err != nil {
		return err
	}
	_, err := r.Mutate(ctx, root, "remote", "set-url", "--", name, url)
	return err
}

// RenameRemote renames a remote; Git moves its tracking refs along.
func (r *Runner) RenameRemote(ctx context.Context, root, name, to string) error {
	if err := validRemoteName(to); err != nil {
		return refusal("remote", "%v", err)
	}
	if err := r.requireRemote(ctx, root, name); err != nil {
		return err
	}
	_, err := r.Mutate(ctx, root, "remote", "rename", "--", name, to)
	return err
}

// RemoveRemote deletes a remote and its tracking branches.
func (r *Runner) RemoveRemote(ctx context.Context, root, name string) error {
	if err := r.requireRemote(ctx, root, name); err != nil {
		return err
	}
	_, err := r.Mutate(ctx, root, "remote", "remove", "--", name)
	return err
}

func (r *Runner) requireRemote(ctx context.Context, root, name string) error {
	remotes, err := r.ListRemotes(ctx, root)
	if err != nil {
		return err
	}
	for _, rm := range remotes {
		if rm.Name == name {
			return nil
		}
	}
	return refusal("remote", "%q is not a configured remote", name)
}

// ---- submodules ------------------------------------------------------------

// SubmoduleState is `git submodule status`'s one-character state.
type SubmoduleState string

const (
	SubmoduleClean         SubmoduleState = "clean"
	SubmoduleUninitialized SubmoduleState = "uninitialized"
	SubmoduleModified      SubmoduleState = "modified" // checked out at another commit than recorded
	SubmoduleConflict      SubmoduleState = "conflict"
)

// Submodule is one submodule of the repository.
type Submodule struct {
	Path     string
	Commit   string
	State    SubmoduleState
	Describe string
}

// ListSubmodules reads `git submodule status` (no network, not recursive).
func (r *Runner) ListSubmodules(ctx context.Context, root string) ([]Submodule, error) {
	out, err := r.Read(ctx, root, "submodule", "status")
	if err != nil {
		return nil, err
	}
	var subs []Submodule
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(line) < 43 {
			continue
		}
		s := Submodule{State: SubmoduleClean}
		switch line[0] {
		case '-':
			s.State = SubmoduleUninitialized
		case '+':
			s.State = SubmoduleModified
		case 'U':
			s.State = SubmoduleConflict
		}
		rest := strings.TrimSpace(line[1:])
		sha, after, _ := strings.Cut(rest, " ")
		s.Commit = sha
		if i := strings.LastIndex(after, " ("); i >= 0 && strings.HasSuffix(after, ")") {
			s.Describe = after[i+2 : len(after)-1]
			after = after[:i]
		}
		s.Path = strings.TrimSpace(after)
		if s.Path != "" {
			subs = append(subs, s)
		}
	}
	return subs, nil
}

// UpdateSubmodule initialises (when needed) and checks out the commit the
// superproject records for one listed submodule. It may contact the network.
func (r *Runner) UpdateSubmodule(ctx context.Context, root, rel string) error {
	subs, err := r.ListSubmodules(ctx, root)
	if err != nil {
		return err
	}
	for _, s := range subs {
		if s.Path == rel {
			_, err := r.network(ctx, root, "submodule", "update", "--init", "--", rel)
			return err
		}
	}
	return refusal("submodule", "%q is not a submodule", rel)
}

// ---- interactive rebase ----------------------------------------------------

// RebaseAction is one line of an interactive rebase todo.
type RebaseAction string

const (
	RebasePick   RebaseAction = "pick"
	RebaseReword RebaseAction = "reword"
	RebaseSquash RebaseAction = "squash"
	RebaseFixup  RebaseAction = "fixup"
	RebaseDrop   RebaseAction = "drop"
)

// RebaseStep is one planned commit: its action and, for reword, the new message.
type RebaseStep struct {
	Hash    string
	Action  RebaseAction
	Message string
}

// RebaseCommit is a commit the plan can rewrite.
type RebaseCommit struct {
	CommitInfo
	Published bool   // reachable from a remote-tracking ref: rewriting it needs a force push
	Message   string // the full message, what a reword starts from
}

// MaxRebaseCommits bounds a plan.
const MaxRebaseCommits = 100

// RebasePlan lists base..HEAD oldest first. Merge commits in the range are
// refused (a linear rebase would flatten them silently).
func (r *Runner) RebasePlan(ctx context.Context, root, base string) ([]RebaseCommit, error) {
	if !validFullCommitID(base) {
		return nil, refusal("rebase", "a full Git commit hash is required")
	}
	if _, err := r.Read(ctx, root, "merge-base", "--is-ancestor", base, "HEAD"); err != nil {
		return nil, refusal("rebase", "that commit is not an ancestor of HEAD")
	}
	commits, err := r.readCommits(ctx, root, MaxRebaseCommits+1, base+"..HEAD")
	if err != nil {
		return nil, err
	}
	if len(commits) == 0 {
		return nil, refusal("rebase", "there are no commits after that one")
	}
	if len(commits) > MaxRebaseCommits {
		return nil, refusal("rebase", "more than %d commits; choose a closer base", MaxRebaseCommits)
	}
	unpublished := map[string]bool{}
	if out, err := r.Read(ctx, root, "rev-list", base+"..HEAD", "--not", "--remotes"); err == nil {
		for _, h := range strings.Fields(string(out)) {
			unpublished[h] = true
		}
	}
	messages := map[string]string{}
	if out, err := r.Read(ctx, root, "log", "--format=%H%x1f%B%x1e", base+".."+"HEAD"); err == nil {
		for _, rec := range strings.Split(string(out), "\x1e") {
			if hash, msg, ok := strings.Cut(strings.TrimLeft(rec, "\n"), "\x1f"); ok {
				messages[hash] = strings.TrimSpace(msg)
			}
		}
	}
	plan := make([]RebaseCommit, 0, len(commits))
	for i := len(commits) - 1; i >= 0; i-- {
		c := commits[i]
		if len(c.Parents) > 1 {
			return nil, refusal("rebase", "the range contains a merge commit (%s); interactive rebase only handles linear history", c.Short)
		}
		plan = append(plan, RebaseCommit{CommitInfo: c, Published: !unpublished[c.Hash], Message: messages[c.Hash]})
	}
	return plan, nil
}

// InteractiveRebase replays base..HEAD as the steps say. The todo is handed to
// Git through GIT_SEQUENCE_EDITOR (a copy of a file we wrote), rewords become an
// `exec git commit --amend -F` line, and a rebase that stops is aborted at once
// like RebaseOnto: the repository is never left half-done.
func (r *Runner) InteractiveRebase(ctx context.Context, root, base string, steps []RebaseStep) error {
	plan, err := r.RebasePlan(ctx, root, base)
	if err != nil {
		return err
	}
	if err := r.RequireCleanWorktree(ctx, root); err != nil {
		return err
	}
	if r.CurrentBranch(ctx, root) == "" {
		return refusal("rebase", "check out a branch first; HEAD is detached")
	}
	known := map[string]bool{}
	for _, c := range plan {
		known[c.Hash] = true
	}
	if len(steps) != len(plan) {
		return refusal("rebase", "the plan must list each of the %d commits exactly once", len(plan))
	}
	dir, err := os.MkdirTemp("", "solco-rebase-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	seen := map[string]bool{}
	var todo strings.Builder
	kept := 0
	for i, st := range steps {
		if !known[st.Hash] || seen[st.Hash] {
			return refusal("rebase", "the plan must list each of the %d commits exactly once", len(plan))
		}
		seen[st.Hash] = true
		switch st.Action {
		case RebasePick, RebaseDrop:
		case RebaseReword:
			if strings.TrimSpace(st.Message) == "" {
				return refusal("rebase", "a reworded commit needs a message")
			}
		case RebaseSquash, RebaseFixup:
			if kept == 0 {
				return refusal("rebase", "the first kept commit cannot be squashed into anything")
			}
		default:
			return refusal("rebase", "unknown action %q", st.Action)
		}
		if st.Action != RebaseDrop {
			kept++
		}
		line := string(st.Action)
		if st.Action == RebaseReword {
			line = string(RebasePick)
		}
		fmt.Fprintf(&todo, "%s %s\n", line, st.Hash)
		if st.Action == RebaseReword {
			msg := filepath.Join(dir, "msg-"+strconv.Itoa(i))
			if err := os.WriteFile(msg, []byte(strings.TrimSpace(st.Message)+"\n"), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(&todo, "exec git commit --amend --allow-empty -q -F %s\n", msg)
		}
	}
	if kept == 0 {
		return refusal("rebase", "dropping every commit would empty the branch; reset instead")
	}
	todoFile := filepath.Join(dir, "todo")
	if err := os.WriteFile(todoFile, []byte(todo.String()), 0o600); err != nil {
		return err
	}
	env := append(mutateEnv(), "GIT_SEQUENCE_EDITOR=cp "+todoFile)
	_, err = r.mutateEnv(ctx, root, env, "rebase", "-i", base)
	if err == nil {
		return nil
	}
	state, _ := r.SequencerState(ctx, root)
	_, _ = r.Mutate(ctx, root, "rebase", "--abort")
	if state.Unmerged > 0 {
		return &Error{Class: ErrConflict, OpClass: OpMutate, Op: "rebase", Paths: state.UnmergedPaths,
			err: fmt.Errorf("rebase stopped on conflicts in %d files and was aborted; nothing changed", state.Unmerged)}
	}
	return err
}

// mutateEnv runs one mutation with an explicit environment (single-flight).
func (r *Runner) mutateEnv(ctx context.Context, root string, env []string, args ...string) ([]byte, error) {
	unlock, err := r.LockRepo(root, first(args))
	if err != nil {
		return nil, err
	}
	defer unlock()
	out, err := r.run(ctx, OpMutate, MutateTimeout, MaxReadOutput, env, root, args)
	if err != nil {
		if ge, ok := GitError(err); ok && ge.Class == ErrOther {
			ge.Class = classifyErr(ge.Stderr)
		}
	}
	return out, err
}
