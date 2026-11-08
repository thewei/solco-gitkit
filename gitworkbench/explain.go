package gitworkbench

import "strings"

/**
 * [INPUT]: 依赖 runner.go 的 Error（Stderr 原文，仅供 UI 展示、绝不入日志）
 * [OUTPUT]: 对外提供 Explain(err)——把 git 失败翻成一句人话（空串＝没有可说的）
 * [POS]: Push/Pull/Fetch 失败 toast 的唯一文案来源：此前只显示 "git push: exit status 1"，原因（被拒、鉴权、网络、远端钩子）全藏在 Stderr 里（2026-11-03 设备 E2E：用户反馈 Push 不通过却看不到为什么）。只认 git 协议用语的子串，不解析仓库内容；返回的句子里最多带一行远端 `remote:` 原话（远端钩子/保护分支自己的解释），不带 URL
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// Explain says in one sentence why a Git network or merge command failed. It
// returns "" when err is not a Git failure with output to read.
func Explain(err error) string {
	ge, ok := GitError(err)
	if !ok || strings.TrimSpace(ge.Stderr) == "" {
		return ""
	}
	raw := ge.Stderr
	low := strings.ToLower(raw)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(low, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("non-fast-forward", "fetch first", "updates were rejected because the tip", "updates were rejected because the remote contains work"):
		return "the remote has commits you don't have — pull first, then push"
	case has("not possible to fast-forward", "diverging branches", "cannot fast-forward", "need to specify how to reconcile divergent"):
		return "the branches have diverged, so a fast-forward isn't possible — merge or rebase instead"
	case has("remote rejected", "pre-receive hook declined", "protected branch", "gh006", "denied to"):
		if line := remoteLine(raw); line != "" {
			return "the remote declined the push: " + line
		}
		return "the remote declined the push (protected branch or a server-side hook)"
	case has("could not read username", "terminal prompts disabled", "authentication failed", "invalid username or password", "permission denied (publickey)", "could not read password", "permission denied, please try again"):
		return "can't sign in to the remote — authenticate once in a Terminal (git credential helper or ssh-add), then retry"
	case has("could not resolve host", "connection refused", "connection timed out", "operation timed out", "network is unreachable", "unable to access", "could not connect"):
		return "can't reach the remote — check the network and the remote address"
	case has("repository not found", "does not appear to be a git repository", "no such remote", "no configured push destination", "does not exist"):
		return "the remote repository wasn't found — check the remote's address in Terminal (git remote -v)"
	case has("would be overwritten by merge", "your local changes to the following files would be overwritten"):
		return "local changes would be overwritten — commit or stash them first"
	case has("unrelated histories"):
		return "the histories are unrelated — nothing to merge automatically"
	case has("has no upstream branch", "no upstream configured"):
		return "this branch has no upstream yet — push once to publish it"
	}
	// Anything else: git's own last fatal/error line beats "exit status 1".
	var last string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		for _, p := range []string{"fatal: ", "error: "} {
			if strings.HasPrefix(line, p) {
				last = strings.TrimPrefix(line, p)
			}
		}
	}
	return last
}

// remoteLine is the first meaningful `remote:` line of a rejected push: the
// server's own explanation (a hook's message, a protected-branch rule).
func remoteLine(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "remote:") {
			continue
		}
		text := strings.TrimSpace(strings.TrimPrefix(line, "remote:"))
		if text == "" || strings.Trim(text, "= -") == "" {
			continue
		}
		if len(text) > 160 {
			text = text[:160]
		}
		return text
	}
	return ""
}
