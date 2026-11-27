package git

import (
	"strings"
	"testing"
)

// A linear range reads oldest-first, every step a pick carrying the commit's
// subject, author and email, onto excluded and HEAD included.
func TestRebaseRangePlanLinearRangeOldestFirst(t *testing.T) {
	dir, hashes := linearRepo(t, 4)
	r := New(&LocalExecutor{Dir: dir})
	plan, err := r.RebaseRangePlan(hashes[1])
	must(t, err)
	if len(plan) != 2 {
		t.Fatalf("plan length = %d, want 2: %+v", len(plan), plan)
	}
	for i, step := range plan {
		want := hashes[2+i]
		if step.Hash != want {
			t.Fatalf("plan[%d].Hash = %q, want %q (oldest first)", i, step.Hash, want)
		}
		if step.Action != "pick" {
			t.Fatalf("plan[%d].Action = %q, want pick", i, step.Action)
		}
		if step.Subject != "c"+string(rune('3'+i)) {
			t.Fatalf("plan[%d].Subject = %q", i, step.Subject)
		}
		if step.Author == "" || step.Email == "" {
			t.Fatalf("plan[%d] missing author or email: %+v", i, step)
		}
	}
}

// A base outside HEAD's ancestry is refused before any plan is read: the
// rebase can never rewrite a side line. The fixture's HEAD sits on main
// after the upstream branch diverged from the first commit.
func TestRebaseRangePlanRejectsNonAncestorOnto(t *testing.T) {
	dir, hashes := linearRepo(t, 3)
	gitRun(t, dir, "checkout", "-qb", "upstream", hashes[0])
	writeFile(t, dir, "side.txt", "side\n")
	gitRun(t, dir, "add", "side.txt")
	gitRun(t, dir, "commit", "-qm", "side work")
	sideTip := gitOut(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "checkout", "-q", "main")

	r := New(&LocalExecutor{Dir: dir})
	_, err := r.RebaseRangePlan(sideTip)
	if err == nil || !strings.Contains(err.Error(), "not an ancestor") {
		t.Fatalf("plan of a non-ancestor base = %v, want a not-an-ancestor error", err)
	}
	if gitOut(t, dir, "rev-parse", "main") != hashes[2] {
		t.Fatal("the refused read moved main")
	}
}

// A merge commit inside the range is refused with its short hash named:
// interactive rebase would flatten it. A merge before the range does not
// block it.
func TestRebaseRangePlanRejectsMergeInsideRange(t *testing.T) {
	dir, hashes := linearRepo(t, 2)
	gitRun(t, dir, "checkout", "-qb", "side", hashes[0])
	writeFile(t, dir, "side.txt", "side\n")
	gitRun(t, dir, "add", "side.txt")
	gitRun(t, dir, "commit", "-qm", "side work")
	gitRun(t, dir, "checkout", "-q", "main")
	gitRun(t, dir, "merge", "-q", "--no-ff", "-m", "merge side", "side")
	mergeTip := gitOut(t, dir, "rev-parse", "HEAD")
	writeFile(t, dir, "top.txt", "top\n")
	gitRun(t, dir, "add", "top.txt")
	gitRun(t, dir, "commit", "-qm", "after merge")
	topTip := gitOut(t, dir, "rev-parse", "HEAD")

	r := New(&LocalExecutor{Dir: dir})
	_, err := r.RebaseRangePlan(hashes[0])
	if err == nil {
		t.Fatal("plan over a range with a merge commit passed")
	}
	if !strings.Contains(err.Error(), "merge commit") {
		t.Fatalf("merge refusal = %q, want a merge commit error", err)
	}
	if !strings.Contains(err.Error(), mergeTip[:7]) {
		t.Fatalf("merge refusal %q missing the merge commit's short hash %q", err, mergeTip[:7])
	}
	for _, rootOnto := range []string{"", "--root"} {
		_, err := r.RebaseRangePlan(rootOnto)
		if err == nil {
			t.Fatalf("root plan (%q) over a history with a merge commit passed", rootOnto)
		}
		if !strings.Contains(err.Error(), "merge commit") || !strings.Contains(err.Error(), mergeTip[:7]) {
			t.Fatalf("root merge refusal (%q) = %q, want merge commit error with %q", rootOnto, err, mergeTip[:7])
		}
	}

	// Based after the merge the range is linear again and reads fine.
	plan, err := r.RebaseRangePlan(mergeTip)
	must(t, err)
	if len(plan) != 1 || plan[0].Hash != topTip {
		t.Fatalf("plan after the merge = %+v, want only %q", plan, topTip)
	}
}

// Malformed and missing bases are errors, never a silently empty or root
// plan: a leading dash would inject options, control characters corrupt the
// todo file, and a name without a commit is a typo the user must see.
func TestRebaseRangePlanRejectsInvalidOnto(t *testing.T) {
	dir, hashes := linearRepo(t, 2)
	r := New(&LocalExecutor{Dir: dir})
	for _, onto := range []string{"-x", "\x00", "no-such-commit"} {
		if _, err := r.RebaseRangePlan(onto); err == nil {
			t.Fatalf("RebaseRangePlan(%q) passed, want an error", onto)
		}
	}
	if got := gitOut(t, dir, "rev-parse", "main"); got != hashes[1] {
		t.Fatalf("a refused read moved main to %q", got)
	}
}

// The empty and --root bases name the whole history, exactly as RebasePlan
// reads them: every commit of the branch, oldest first.
func TestRebaseRangePlanRootListsWholeHistory(t *testing.T) {
	dir, hashes := linearRepo(t, 3)
	r := New(&LocalExecutor{Dir: dir})
	for _, onto := range []string{"", "--root"} {
		plan, err := r.RebaseRangePlan(onto)
		must(t, err)
		if len(plan) != len(hashes) {
			t.Fatalf("root plan length = %d, want %d", len(plan), len(hashes))
		}
		for i, step := range plan {
			if step.Hash != hashes[i] {
				t.Fatalf("root plan[%d] = %q, want %q", i, step.Hash, hashes[i])
			}
		}
	}
}

// The plan read is read-only: the repository's HEAD, branch and working tree
// are byte-identical afterwards.
func TestRebaseRangePlanLeavesRepositoryUntouched(t *testing.T) {
	dir, hashes := linearRepo(t, 3)
	writeFile(t, dir, "dirty.txt", "untracked\n")
	before := gitOut(t, dir, "status", "--porcelain")
	r := New(&LocalExecutor{Dir: dir})
	if _, err := r.RebaseRangePlan(hashes[0]); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "rev-parse", "HEAD"); got != hashes[2] {
		t.Fatalf("HEAD moved by the read: %q, want %q", got, hashes[2])
	}
	if got := gitOut(t, dir, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Fatalf("branch after the read = %q, want main", got)
	}
	if got := gitOut(t, dir, "status", "--porcelain"); got != before {
		t.Fatalf("working tree changed by the read: %q, want %q", got, before)
	}
}
