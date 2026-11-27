package git

import (
	"os/exec"
	"strings"
	"testing"
)

// A local branch that tracks origin/topic, which is then deleted on the server.
func TestUnsetUpstreamKeepsBranch(t *testing.T) {
	dir, _, r := remoteRepo(t)
	gitRun(t, dir, "push", "-q", "-u", "origin", "main")
	if err := r.UnsetUpstream("main"); err != nil {
		t.Fatal(err)
	}
	if exec.Command("git", "-C", dir, "config", "--get", "branch.main.merge").Run() == nil {
		t.Fatal("upstream still set")
	}
	if err := r.UnsetUpstream(""); err == nil {
		t.Fatal("an empty branch was accepted")
	}
}

func TestStaleBranchesAreGoneAndNotCurrent(t *testing.T) {
	got := StaleBranches([]Branch{
		{Name: "old", FullName: "refs/heads/old", Gone: true},
		{Name: "main", FullName: "refs/heads/main", Gone: true, Current: true},
		{Name: "origin/x", FullName: "refs/remotes/origin/x", Gone: true},
		{Name: "live", FullName: "refs/heads/live"},
	})
	if strings.Join(got, ",") != "old" {
		t.Fatalf("stale branches = %v", got)
	}
}

func TestFastForwardToUpstreams(t *testing.T) {
	dir, bare, r := remoteRepo(t)
	gitRun(t, dir, "push", "-q", "-u", "origin", "main")
	gitRun(t, dir, "checkout", "-q", "-b", "side")
	gitRun(t, dir, "push", "-q", "-u", "origin", "side")
	other := t.TempDir()
	gitRun(t, other, "clone", "-q", bare, "w")
	w := other + "/w"
	gitRun(t, w, "checkout", "-q", "side")
	writeFile(t, w, "s.txt", "s\n")
	gitRun(t, w, "add", "s.txt")
	gitRun(t, w, "commit", "-qm", "side 2")
	gitRun(t, w, "push", "-q", "origin", "side")
	gitRun(t, dir, "fetch", "-q")
	gitRun(t, dir, "checkout", "-q", "main")
	branches, err := r.Refs()
	if err != nil {
		t.Fatal(err)
	}
	moved, err := r.FastForwardToUpstreams(branches.Branches)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || moved[0] != "side" {
		t.Fatalf("moved = %v", moved)
	}
	if gitOut(t, dir, "rev-parse", "side") != gitOut(t, dir, "rev-parse", "origin/side") {
		t.Fatal("side did not catch up")
	}
}

// The diff of a branch for review is what it adds beyond its merge base
// with the base branch, not what the base branch gained meanwhile.
func TestBranchDiffStartsAtMergeBase(t *testing.T) {
	dir, _, r := remoteRepo(t)
	gitRun(t, dir, "checkout", "-q", "-b", "topic")
	writeFile(t, dir, "topic.txt", "topic\n")
	gitRun(t, dir, "add", "topic.txt")
	gitRun(t, dir, "commit", "-q", "-m", "topic work")
	gitRun(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "main.txt", "main\n")
	gitRun(t, dir, "add", "main.txt")
	gitRun(t, dir, "commit", "-q", "-m", "main work")
	out, err := r.BranchDiff("main", "topic")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "topic.txt") || strings.Contains(out, "main.txt") {
		t.Fatalf("diff:\n%s", out)
	}
	if _, err := r.BranchDiff("", "topic"); err == nil {
		t.Fatal("an empty base was accepted")
	}
}
