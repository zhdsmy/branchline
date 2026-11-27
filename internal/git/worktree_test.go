package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeAddAtStartsOnTheChosenRevision(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "first")
	gitRun(t, dir, "branch", "old")
	writeFile(t, dir, "a.txt", "two\n")
	gitRun(t, dir, "commit", "-q", "-am", "second")
	r := New(&LocalExecutor{Dir: dir})

	folder := filepath.Join(t.TempDir(), "topic")
	if err := r.WorktreeAddAt(folder, "topic", "old"); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(gitOut(t, folder, "log", "--format=%s", "-1"))
	if got != "first" {
		t.Fatalf("topic starts at %q, want the commit of old", got)
	}
	if head := strings.TrimSpace(gitOut(t, folder, "branch", "--show-current")); head != "topic" {
		t.Fatalf("worktree has %q checked out", head)
	}
}

func TestWorktreeCreateAtRejectsExistingBranchWhileWorktreeAddAtChecksItOut(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "first")
	firstCommit := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
	writeFile(t, dir, "a.txt", "two\n")
	gitRun(t, dir, "commit", "-q", "-am", "second")
	secondCommit := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
	gitRun(t, dir, "branch", "existing", secondCommit)
	r := New(&LocalExecutor{Dir: dir})

	rejectedDir := filepath.Join(t.TempDir(), "rejected-wt")
	if err := r.WorktreeCreateAt(rejectedDir, "existing", firstCommit); err == nil || !strings.Contains(strings.ToLower(err.Error()), "already exists") {
		t.Fatalf("WorktreeCreateAt on existing branch err=%v, want already exists", err)
	}
	if _, err := os.Stat(rejectedDir); !os.IsNotExist(err) {
		t.Fatalf("rejected worktree dir exists: %v", err)
	}
	if got := strings.TrimSpace(gitOut(t, dir, "rev-parse", "refs/heads/existing")); got != secondCommit {
		t.Fatalf("existing branch HEAD moved to %s, want %s", got, secondCommit)
	}

	createdDir := filepath.Join(t.TempDir(), "created-wt")
	if err := r.WorktreeCreateAt(createdDir, "fresh", firstCommit); err != nil {
		t.Fatalf("WorktreeCreateAt fresh: %v", err)
	}
	if got := strings.TrimSpace(gitOut(t, createdDir, "rev-parse", "HEAD")); got != firstCommit {
		t.Fatalf("fresh worktree HEAD=%s, want %s", got, firstCommit)
	}

	checkoutDir := filepath.Join(t.TempDir(), "checkout-existing-wt")
	if err := r.WorktreeAddAt(checkoutDir, "existing", firstCommit); err != nil {
		t.Fatalf("WorktreeAddAt existing branch: %v", err)
	}
	if got := strings.TrimSpace(gitOut(t, checkoutDir, "rev-parse", "HEAD")); got != secondCommit {
		t.Fatalf("WorktreeAddAt existing branch HEAD=%s, want %s", got, secondCommit)
	}
}
