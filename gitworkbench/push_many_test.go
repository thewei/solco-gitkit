package gitworkbench

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pushFixture is a clone of a bare "origin" with: main tracking origin/main
// and one commit ahead; feature, a new local branch; synced, equal to its
// remote twin (nothing to send); diverged, one ahead and one behind; other,
// tracking a second remote; and tags v1 and v2.
func pushFixture(t *testing.T) (root, bare string, r *Runner) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	base := t.TempDir()
	bare = filepath.Join(base, "origin.git")
	second := filepath.Join(base, "second.git")
	root = filepath.Join(base, "work")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "LC_ALL=C")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(dir, file, msg string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, file), []byte(msg+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(dir, "add", file)
		git(dir, "commit", "-q", "-m", msg)
	}
	for _, d := range []string{bare, second} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		git(d, "init", "-q", "--bare", "-b", "main")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	git(root, "init", "-q", "-b", "main")
	git(root, "remote", "add", "origin", bare)
	git(root, "remote", "add", "second", second)
	commit(root, "a.txt", "one")
	git(root, "push", "-q", "-u", "origin", "main")
	// synced: pushed and equal.
	git(root, "branch", "synced")
	git(root, "push", "-q", "origin", "synced")
	// diverged: pushed, then one commit locally, one from "someone else".
	git(root, "branch", "diverged")
	git(root, "push", "-q", "-u", "origin", "diverged")
	git(root, "checkout", "-q", "diverged")
	commit(root, "d-local.txt", "local on diverged")
	other := filepath.Join(base, "other")
	git(base, "clone", "-q", bare, other)
	git(other, "checkout", "-q", "diverged")
	commit(other, "d-remote.txt", "remote on diverged")
	git(other, "push", "-q", "origin", "diverged")
	git(root, "fetch", "-q", "origin")
	// other: tracks the second remote.
	git(root, "checkout", "-q", "-b", "other", "main")
	commit(root, "o.txt", "on other")
	git(root, "push", "-q", "-u", "second", "other")
	commit(root, "o2.txt", "more on other")
	// feature: new.
	git(root, "checkout", "-q", "-b", "feature", "main")
	commit(root, "f.txt", "on feature")
	// main: one ahead.
	git(root, "checkout", "-q", "main")
	commit(root, "m.txt", "ahead on main")
	git(root, "tag", "v1")
	git(root, "tag", "-a", "-m", "second", "v2")
	return root, bare, &Runner{}
}

func refNames(refs []PushRef) string {
	var names []string
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	return strings.Join(names, ",")
}

func TestReadPushOfferListsWhatThereIsToSend(t *testing.T) {
	root, _, r := pushFixture(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	offer, err := r.ReadPushOffer(ctx, root, "origin")
	if err != nil {
		t.Fatal(err)
	}
	// main (current, ahead), then diverged (blocked), feature (new): the
	// current branch first, the rest by name. synced has nothing to send and
	// other belongs to the second remote.
	if got := refNames(offer.Branches); got != "main,diverged,feature" {
		t.Fatalf("branches = %s, want main,diverged,feature", got)
	}
	main, diverged, feature := offer.Branches[0], offer.Branches[1], offer.Branches[2]
	if !main.Current || !main.Tracked || main.Ahead != 1 || main.Behind != 0 || main.Dest != "main" || main.Blocked != "" {
		t.Errorf("main = %+v", main)
	}
	if diverged.Ahead != 1 || diverged.Behind != 1 || !strings.Contains(diverged.Blocked, "pull first") {
		t.Errorf("diverged = %+v, want blocked with pull first", diverged)
	}
	if !feature.New || feature.Tracked || feature.Dest != "feature" || feature.Blocked != "" {
		t.Errorf("feature = %+v", feature)
	}
	if got := refNames(offer.Tags); got != "v2,v1" && got != "v1,v2" {
		t.Errorf("tags = %s", got)
	}

	second, err := r.ReadPushOffer(ctx, root, "second")
	if err != nil {
		t.Fatal(err)
	}
	// Branches tracking origin are not offered to second; the ones with no
	// upstream (and no twin there) are new on it.
	if got := refNames(second.Branches); got != "feature,other,synced" {
		t.Errorf("second remote offers %s, want feature,other,synced", got)
	}
	for _, b := range second.Branches {
		if b.Name == "other" && (!b.Tracked || b.Ahead != 1) {
			t.Errorf("other = %+v", b)
		}
	}
	if _, err := r.ReadPushOffer(ctx, root, "nope"); err == nil {
		t.Error("an unconfigured remote is refused")
	}
}

func TestPushManySendsExactlyTheChosenRefs(t *testing.T) {
	root, bare, r := pushFixture(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	offer, err := r.ReadPushOffer(ctx, root, "origin")
	if err != nil {
		t.Fatal(err)
	}
	pick := []PushRef{offer.Branches[0], offer.Branches[2]} // main, feature
	for _, tag := range offer.Tags {
		if tag.Name == "v2" {
			pick = append(pick, tag)
		}
	}
	if err := r.PushMany(ctx, root, "origin", pick, true); err != nil {
		t.Fatal(err)
	}
	refs := runGit(t, bare, "for-each-ref", "--format=%(refname)")
	for _, want := range []string{"refs/heads/main", "refs/heads/feature", "refs/tags/v2"} {
		if !strings.Contains(refs, want) {
			t.Errorf("origin lacks %s after the push:\n%s", want, refs)
		}
	}
	if strings.Contains(refs, "refs/tags/v1") {
		t.Error("v1 was not chosen and must not be sent")
	}
	if got := strings.TrimSpace(runGit(t, root, "rev-parse", "--abbrev-ref", "feature@{upstream}")); got != "origin/feature" {
		t.Errorf("feature's upstream = %q, want origin/feature (-u for a branch that had none)", got)
	}
	// What was sent is current: the next offer has nothing for those.
	again, err := r.ReadPushOffer(ctx, root, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if got := refNames(again.Branches); got != "diverged" {
		t.Errorf("after the push the offer is %s, want only the blocked diverged", got)
	}
}

func TestPushManyNeverForces(t *testing.T) {
	root, bare, r := pushFixture(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	offer, err := r.ReadPushOffer(ctx, root, "origin")
	if err != nil {
		t.Fatal(err)
	}
	// A blocked ref is refused before Git is asked.
	blocked := offer.Branches[1]
	before := runGit(t, bare, "rev-parse", "diverged")
	if err := r.PushMany(ctx, root, "origin", []PushRef{blocked}, false); err == nil {
		t.Fatal("a diverged branch must be refused")
	}
	// Even with the block lifted by a caller, Git itself refuses the
	// non-fast-forward: no refspec carries a "+".
	blocked.Blocked = ""
	if err := r.PushMany(ctx, root, "origin", []PushRef{blocked}, false); err == nil {
		t.Fatal("git must reject a non-fast-forward push")
	}
	if after := runGit(t, bare, "rev-parse", "diverged"); after != before {
		t.Error("the remote's diverged branch moved")
	}
}

func TestPushManyRefusesBadInput(t *testing.T) {
	root, _, r := pushFixture(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	cases := map[string]struct {
		remote string
		refs   []PushRef
	}{
		"none":            {"origin", nil},
		"unknown remote":  {"nope", []PushRef{{Kind: PushBranch, Name: "main", Dest: "main"}}},
		"dash name":       {"origin", []PushRef{{Kind: PushBranch, Name: "-f", Dest: "-f"}}},
		"gone branch":     {"origin", []PushRef{{Kind: PushBranch, Name: "ghost", Dest: "ghost"}}},
		"bad destination": {"origin", []PushRef{{Kind: PushBranch, Name: "main", Dest: "a..b"}}},
		"unknown kind":    {"origin", []PushRef{{Kind: "stash", Name: "main", Dest: "main"}}},
	}
	for name, tc := range cases {
		if err := r.PushMany(ctx, root, tc.remote, tc.refs, false); err == nil {
			t.Errorf("%s: want a refusal", name)
		}
	}
}
