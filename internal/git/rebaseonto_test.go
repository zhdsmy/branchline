package git

import (
	"os/exec"
	"strings"
	"testing"
)

func stackedRepo(t *testing.T) (string, *Repo) {
	t.Helper()
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-qm", "base")
	gitRun(t, dir, "checkout", "-qb", "feature")
	writeFile(t, dir, "f1.txt", "1\n")
	gitRun(t, dir, "add", "f1.txt")
	gitRun(t, dir, "commit", "-qm", "f1")
	gitRun(t, dir, "branch", "mid")
	writeFile(t, dir, "f2.txt", "2\n")
	gitRun(t, dir, "add", "f2.txt")
	gitRun(t, dir, "commit", "-qm", "f2")
	gitRun(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "m.txt", "m\n")
	gitRun(t, dir, "add", "m.txt")
	gitRun(t, dir, "commit", "-qm", "main moves")
	gitRun(t, dir, "checkout", "-q", "feature")
	return dir, New(&LocalExecutor{Dir: dir})
}

func TestRebaseOntoUpdatesDependentBranchesAndReappliesStash(t *testing.T) {
	dir, r := stackedRepo(t)
	names, err := r.DependentBranches("main")
	if err != nil || strings.Join(names, ",") != "mid" {
		t.Fatalf("dependent %v %v", names, err)
	}
	writeFile(t, dir, "a.txt", "one\nlocal\n")
	if err := r.RebaseOnto("main", RebaseOntoOptions{Stash: true, UpdateRefs: true}); err != nil {
		t.Fatal(err)
	}
	if exec.Command("git", "-C", dir, "merge-base", "--is-ancestor", "main", "mid").Run() != nil {
		t.Fatal("mid did not move onto main")
	}
	if !strings.Contains(gitOut(t, dir, "log", "--format=%s", "mid"), "main moves") {
		t.Fatal("mid was not rebased with feature")
	}
	if !strings.Contains(gitOut(t, dir, "status", "--porcelain"), "a.txt") {
		t.Fatal("the stash was not reapplied")
	}
	if gitOut(t, dir, "stash", "list") != "" {
		t.Fatal("a stash is left over")
	}
}

func TestRebaseOntoWithoutUpdateRefsLeavesOtherBranches(t *testing.T) {
	dir, r := stackedRepo(t)
	before := gitOut(t, dir, "rev-parse", "mid")
	if err := r.RebaseOnto("main", RebaseOntoOptions{}); err != nil {
		t.Fatal(err)
	}
	if gitOut(t, dir, "rev-parse", "mid") != before {
		t.Fatal("mid moved without Update dependent branches")
	}
	if err := r.RebaseOnto(" ", RebaseOntoOptions{}); err == nil {
		t.Fatal("empty revision accepted")
	}
}
