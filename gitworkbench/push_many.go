package gitworkbench

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

/**
 * [INPUT]: 依赖 Runner.Read（只读 for-each-ref / rev-list / rev-parse / check-ref-format）、ListRemotes、ListTags 与 Runner.network（显式网络变更，单飞锁）
 * [OUTPUT]: 对外提供 PushKind、PushRef、PushOffer、PushOffer 的读取（某远端上哪些分支有东西可推、本地标签有哪些）与 PushMany（一次 git push 推多个分支与标签）
 * [POS]: 多引用推送（Fork 的 Push 对话框）的只读+变更入口：只推用户勾选的、名字经 check-ref-format 与 rev-parse 校验的引用，永不带 `+`/--force，分叉的分支标记为受阻而不是强推；不碰工作区
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// PushKind says what a PushRef is.
type PushKind string

const (
	PushBranch PushKind = "branch"
	PushTag    PushKind = "tag"
)

// PushRef is one ref the multi-push dialog offers: a local branch with
// something to send (or none of it on the remote yet), or a local tag.
type PushRef struct {
	Kind PushKind
	Name string // the local branch or tag
	Dest string // the branch's name on the remote (== Name for a tag)

	Ahead, Behind int  // against the upstream, or the fetched same-name remote branch
	New           bool // the remote has no such branch (as far as the last fetch knows)
	Tracked       bool // the branch has an upstream on this remote
	Current       bool // the branch checked out here

	// Blocked is why the ref cannot be pushed without force ("" when it can).
	// There is no force push here; a diverged branch is pulled first.
	Blocked string
}

// PushOffer is what the dialog lists for one remote.
type PushOffer struct {
	Remote   string
	Branches []PushRef // the current branch first, then by name
	Tags     []PushRef // newest first, as ListTags orders them
}

// ReadPushOffer lists, for remote, the local branches that have commits to
// send (or are not on the remote yet) and the local tags. It reads only what
// the last fetch recorded; no network. A branch tracking another remote is not
// offered here.
func (r *Runner) ReadPushOffer(ctx context.Context, root, remote string) (PushOffer, error) {
	offer := PushOffer{Remote: remote}
	if err := r.requireRemote(ctx, root, remote); err != nil {
		return offer, err
	}
	const format = "%(refname:short)%09%(HEAD)%09%(upstream:remotename)%09%(upstream:remoteref)%09%(upstream:track,nobracket)"
	out, err := r.Read(ctx, root, "for-each-ref", "--format="+format, "refs/heads")
	if err != nil {
		return offer, err
	}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 5 {
			continue
		}
		ref := PushRef{Kind: PushBranch, Name: f[0], Dest: f[0], Current: f[1] == "*"}
		upRemote, upRef, track := f[2], strings.TrimPrefix(f[3], "refs/heads/"), f[4]
		switch {
		case upRemote != "" && upRemote != remote:
			continue // it belongs to another remote
		case upRemote == remote && track != "gone":
			ref.Tracked, ref.Dest = true, upRef
			ref.Ahead, ref.Behind = parseTrack(track)
		default:
			// No upstream (or one that is gone): is there a same-name branch
			// the last fetch saw?
			if _, err := r.Read(ctx, root, "rev-parse", "--verify", "--quiet", "refs/remotes/"+remote+"/"+f[0]); err != nil {
				ref.New = true
				break
			}
			count, err := r.Read(ctx, root, "rev-list", "--left-right", "--count", "refs/remotes/"+remote+"/"+f[0]+"..."+"refs/heads/"+f[0])
			if err != nil {
				continue
			}
			if fields := strings.Fields(string(count)); len(fields) == 2 {
				ref.Behind, _ = strconv.Atoi(fields[0])
				ref.Ahead, _ = strconv.Atoi(fields[1])
			}
		}
		if !ref.New && ref.Ahead == 0 {
			continue // nothing to send
		}
		if ref.Ahead > 0 && ref.Behind > 0 {
			ref.Blocked = "Diverged from " + remote + "/" + ref.Dest + " — pull first"
		}
		offer.Branches = append(offer.Branches, ref)
	}
	sort.SliceStable(offer.Branches, func(i, j int) bool {
		a, b := offer.Branches[i], offer.Branches[j]
		if a.Current != b.Current {
			return a.Current
		}
		return a.Name < b.Name
	})
	tags, err := r.ListTags(ctx, root)
	if err != nil {
		return offer, err
	}
	for _, tag := range tags {
		offer.Tags = append(offer.Tags, PushRef{Kind: PushTag, Name: tag.Name, Dest: tag.Name})
	}
	return offer, nil
}

// parseTrack reads for-each-ref's "ahead 1", "behind 2" and "ahead 1, behind 2".
func parseTrack(track string) (ahead, behind int) {
	for _, part := range strings.Split(track, ",") {
		fields := strings.Fields(part)
		if len(fields) != 2 {
			continue
		}
		n, _ := strconv.Atoi(fields[1])
		switch fields[0] {
		case "ahead":
			ahead = n
		case "behind":
			behind = n
		}
	}
	return ahead, behind
}

// MaxPushRefs bounds one multi-ref push.
const MaxPushRefs = 200

// PushMany publishes the given refs to remote in one `git push`: each branch
// lands on its Dest, each tag on itself. setUpstream (-u) records tracking for
// the pushed branches that have none. There is never a force: every refspec is
// fully qualified and carries no "+", a ref that is Blocked is refused, and a
// branch or tag that no longer exists locally is refused before Git is asked.
func (r *Runner) PushMany(ctx context.Context, root, remote string, refs []PushRef, setUpstream bool) error {
	if len(refs) == 0 {
		return refusal("push", "nothing selected")
	}
	if len(refs) > MaxPushRefs {
		return refusal("push", "too many refs in one push")
	}
	if err := r.requireRemote(ctx, root, remote); err != nil {
		return err
	}
	args := []string{"push"}
	branches := 0
	var specs []string
	for _, ref := range refs {
		if ref.Blocked != "" {
			return refusal("push", "%s: %s", ref.Name, ref.Blocked)
		}
		var src, dst string
		switch ref.Kind {
		case PushBranch:
			src, dst = "refs/heads/"+ref.Name, "refs/heads/"+ref.Dest
			branches++
		case PushTag:
			src, dst = "refs/tags/"+ref.Name, "refs/tags/"+ref.Name
		default:
			return refusal("push", "unknown ref kind")
		}
		if ref.Name == "" || ref.Dest == "" || strings.HasPrefix(ref.Name, "-") || strings.HasPrefix(ref.Dest, "-") {
			return refusal("push", "not a ref name")
		}
		if _, err := r.Read(ctx, root, "rev-parse", "--verify", "--quiet", src); err != nil {
			return &Error{Class: ErrStaleState, OpClass: OpMutate, Op: "push", err: fmt.Errorf("%q no longer exists", ref.Name)}
		}
		if _, err := r.Read(ctx, root, "check-ref-format", dst); err != nil {
			return refusal("push", "%q is not a valid ref name", dst)
		}
		specs = append(specs, src+":"+dst)
	}
	if setUpstream && branches > 0 {
		args = append(args, "-u")
	}
	args = append(args, remote)
	args = append(args, specs...)
	_, err := r.network(ctx, root, args...)
	return err
}
