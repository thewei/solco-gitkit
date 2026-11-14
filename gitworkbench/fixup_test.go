package gitworkbench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakePlan(subjects ...string) []RebaseCommit {
	var plan []RebaseCommit
	for i, s := range subjects {
		plan = append(plan, RebaseCommit{CommitInfo: CommitInfo{Hash: string(rune('a' + i)), Subject: s}})
	}
	return plan
}

func stepsText(steps []RebaseStep) string {
	var out []string
	for _, st := range steps {
		out = append(out, string(st.Action)+" "+st.Hash)
	}
	return strings.Join(out, ",")
}

func TestPlanAutosquashPutsEachFixupAfterItsTarget(t *testing.T) {
	// a=one b=two c=three d=fixup! one e=fixup! two f=fixup! fixup! one g=fixup! nowhere h=fixup! thr (prefix of three)
	steps, moved := PlanAutosquash(fakePlan("one", "two", "three", "fixup! one", "fixup! two", "fixup! fixup! one", "fixup! nowhere", "fixup! thr"))
	want := "pick a,fixup d,fixup f,pick b,fixup e,pick c,fixup h,pick g"
	if got := stepsText(steps); got != want || moved != 4 {
		t.Fatalf("steps = %s (moved %d), want %s (moved 4)", got, moved, want)
	}
	if steps, moved := PlanAutosquash(fakePlan("one", "two")); moved != 0 || stepsText(steps) != "pick a,pick b" {
		t.Fatalf("without fixups the plan is unchanged: %s, %d", stepsText(steps), moved)
	}
	// A fixup does not target an older fixup, and a later commit is never a target.
	if steps, moved := PlanAutosquash(fakePlan("fixup! two", "two")); moved != 0 || stepsText(steps) != "pick a,pick b" {
		t.Fatalf("the target must be older: %s, %d", stepsText(steps), moved)
	}
}

func TestCreateFixupAndSquashFixups(t *testing.T) {
	root, r := tempRepo(t)
	ctx, cancel := opCtx(t)
	defer cancel()
	c1 := commitFile(t, root, "one.txt", "1\n", "one")
	c2 := commitFile(t, root, "two.txt", "2\n", "two")
	commitFile(t, root, "three.txt", "3\n", "three")

	if err := r.CreateFixup(ctx, root, c1); err == nil {
		t.Fatal("nothing is staged: refused")
	}
	writeRepoFile(t, root, "one.txt", "1\nfixed\n")
	runGit(t, root, "add", "one.txt")
	if err := r.CreateFixup(ctx, root, "HEAD"); err == nil {
		t.Fatal("a short name is refused")
	}
	if err := r.CreateFixup(ctx, root, c1); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, root, "two.txt", "2\nfixed\n")
	runGit(t, root, "add", "two.txt")
	if err := r.CreateFixup(ctx, root, c2); err != nil {
		t.Fatal(err)
	}
	if got := oneLineLog(t, root); got != "fixup! two,fixup! one,three,two,one,initial" {
		t.Fatalf("log = %s", got)
	}

	// Starting at "two", the fixup of "one" is out of range and stays.
	if n, err := r.SquashFixups(ctx, root, c2); err != nil || n != 1 {
		t.Fatalf("squash from two: %d, %v", n, err)
	}
	if got := oneLineLog(t, root); got != "fixup! one,three,two,one,initial" {
		t.Fatalf("log = %s", got)
	}
	head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	if _, err := r.SquashFixups(ctx, root, c2); err == nil {
		t.Fatal("no fixup left in that range: refused")
	}
	if strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD")) != head {
		t.Fatal("a refusal moved HEAD")
	}
	data, _ := os.ReadFile(filepath.Join(root, "two.txt"))
	if string(data) != "2\nfixed\n" {
		t.Errorf("the fixup's change is part of two now: %q", data)
	}
}
