package gitworkbench

import (
	"context"
	"os"
	"strings"
)

/**
 * [INPUT]: Runner、CourierSource（选中行的坐标）、courierLines（正反两份补丁）、DiscardCourierLines（日志里的反向应用）
 * [OUTPUT]: StashLines：把一个文件里选中的、尚未暂存的行存成一条原生 stash（HEAD 上只叠这些行，真实索引不碰），随后把这些行从工作区拿掉；存之前先确认拿得掉，存好之后拿失败就撤回这条 stash，不留重复
 * [POS]: 2026-11-12 决定第 (14) 条的内核（行级 stash）；只认已跟踪、索引与 HEAD 一致的文件——有暂存改动的文件先取消暂存，补丁相对的是 HEAD
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// StashLines saves the picked lines of one working-tree file as a stash entry
// and takes them out of the working tree. The stash is the native kind (HEAD
// plus only those lines, with an index parent), so pop, apply, drop and the
// Stashes list treat it like any other. Picks are over src.Hunks, the unstaged
// diff the user picked on.
//
// The file must be tracked and must have nothing staged: the patch is cut
// against HEAD, and a staged change would make the stash and the working tree
// disagree about what the lines were. Everything that can refuse does so
// before the stash is stored, and the removal is `git apply --reverse`, all or
// nothing; if it still fails the stash just stored is dropped again.
func (r *Runner) StashLines(ctx context.Context, root string, src CourierSource, message string) error {
	path, ok := cleanRelPath(src.Path)
	if !ok || src.Commit != "" || src.Picks == nil || src.renamed() {
		return refusal("stash", "only picked lines of one working-tree file can be stashed")
	}
	forward, ok := courierLines(path, src, false)
	if !ok {
		return refusal("stash", "no changed lines are selected in %s", path)
	}
	reverse, _ := courierLines(path, src, true)
	if len(forward) > MaxPatchFileBytes {
		return refusal("stash", "the lines are larger than %d MiB", MaxPatchFileBytes>>20)
	}
	if st, err := r.SequencerState(ctx, root); err != nil {
		return err
	} else if st.Active() {
		return refusal("stash", "finish or abort the merge, rebase or similar first")
	}
	head, err := r.Read(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return refusal("stash", "stashing lines needs a first commit")
	}
	base := strings.TrimSpace(string(head))
	if out, err := r.Read(ctx, root, "--literal-pathspecs", "ls-files", "--", path); err != nil {
		return err
	} else if strings.TrimSpace(string(out)) == "" {
		return refusal("stash", "%s is not tracked; stash the whole file instead", path)
	}
	if out, err := r.Read(ctx, root, "--literal-pathspecs", "diff", "--cached", "--name-only", "--", path); err != nil {
		return err
	} else if strings.TrimSpace(string(out)) != "" {
		return refusal("stash", "%s has staged changes; unstage them first", path)
	}
	// The lines must still be there to take out: check before anything is stored.
	if _, err := r.runStdin(ctx, OpRead, ReadTimeout, root, reverse, []string{"apply", "--check", "--reverse", "-"}); err != nil {
		if ge, ok := GitError(err); ok && strings.TrimSpace(ge.Stderr) != "" {
			return refusal("stash", "%s changed since the lines were picked: %s", path, courierProblem(ge.Stderr))
		}
		return err
	}

	commit, err := r.stashLinesCommit(ctx, root, base, forward, strings.TrimSpace(message), path)
	if err != nil {
		return err
	}
	if _, err := r.Mutate(ctx, root, "stash", "store", "-m", stashLinesMessage(message, path), commit); err != nil {
		return err
	}
	if err := r.DiscardCourierLines(ctx, root, src); err != nil {
		if top, terr := r.Read(ctx, root, "rev-parse", "--verify", "--quiet", "refs/stash"); terr == nil && strings.TrimSpace(string(top)) == commit {
			_, _ = r.Mutate(ctx, root, "stash", "drop", "stash@{0}")
		}
		return err
	}
	return nil
}

func stashLinesMessage(message, path string) string {
	if message = strings.TrimSpace(message); message != "" {
		return message
	}
	return "Stashed lines of " + path
}

// stashLinesCommit builds the stash commit: HEAD's tree with only the forward
// patch applied, in a throwaway index so the real one is never touched,
// parented on HEAD and on an index commit (a stash's second parent).
func (r *Runner) stashLinesCommit(ctx context.Context, root, base, patch, message, path string) (string, error) {
	tmp, err := os.CreateTemp("", "solco-stash-index-*")
	if err != nil {
		return "", err
	}
	index := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(index)
	_ = os.Remove(index) // git wants to create the index itself
	env := append(mutateEnv(), "GIT_INDEX_FILE="+index)
	if _, err := r.mutateEnv(ctx, root, env, "read-tree", base); err != nil {
		return "", err
	}
	if _, err := r.runMutatingStdin(ctx, root, env, patch, "apply", "--cached", "-"); err != nil {
		return "", err
	}
	tree, err := r.mutateEnv(ctx, root, env, "write-tree")
	if err != nil {
		return "", err
	}
	baseTree, err := r.Read(ctx, root, "rev-parse", base+"^{tree}")
	if err != nil {
		return "", err
	}
	who := r.commitIdentity(ctx, root)
	indexCommit, err := r.mutateEnv(ctx, root, who, "commit-tree", strings.TrimSpace(string(baseTree)), "-p", base, "-m", "index on stashed lines of "+path)
	if err != nil {
		return "", err
	}
	out, err := r.mutateEnv(ctx, root, who, "commit-tree", strings.TrimSpace(string(tree)), "-p", base, "-p", strings.TrimSpace(string(indexCommit)), "-m", stashLinesMessage(message, path))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// commitIdentity is the environment a plumbing commit runs under: the user's
// own identity when Git has one, a neutral stash identity when it has none
// (commit-tree refuses without).
func (r *Runner) commitIdentity(ctx context.Context, root string) []string {
	if _, err := r.Read(ctx, root, "var", "GIT_COMMITTER_IDENT"); err == nil {
		return mutateEnv()
	}
	return append(mutateEnv(),
		"GIT_AUTHOR_NAME=Solco Git", "GIT_AUTHOR_EMAIL=stash@localhost",
		"GIT_COMMITTER_NAME=Solco Git", "GIT_COMMITTER_EMAIL=stash@localhost")
}
