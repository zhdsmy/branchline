package git

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// configValue reads a key of the repository's own configuration, reporting
// unset as "".
func configValue(t *testing.T, dir, key string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "config", "--get", key)
	cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// behindFixture builds topic as a non-current branch one commit behind its
// upstream origin/topic: the upstream gained a commit topic does not have.
// It returns the hash topic points at and the one origin/topic points at.
func behindFixture(t *testing.T, dir string) (oldHash, upstreamHash string) {
	t.Helper()
	gitRun(t, dir, "checkout", "-q", "-b", "topic")
	gitRun(t, dir, "push", "-q", "-u", "origin", "topic")
	writeFile(t, dir, "t.txt", "t\n")
	gitRun(t, dir, "add", "t.txt")
	gitRun(t, dir, "commit", "-q", "-m", "topic 2")
	gitRun(t, dir, "push", "-q", "origin", "topic")
	upstreamHash = gitOut(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "reset", "-q", "--hard", "HEAD~1")
	oldHash = gitOut(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "checkout", "-q", "main")
	return oldHash, upstreamHash
}

// The upstream of a branch comes from its tracking configuration: it names
// the remote-tracking branch when one is set, is empty for a branch tracking
// nothing, and still answers when the remote-tracking ref itself is gone,
// because the setting survives until it is unset.
func TestBranchTrackingUpstream(t *testing.T) {
	dir, bare, r := remoteRepo(t)

	if got, err := r.BranchUpstream("main"); err != nil || got != "origin/main" {
		t.Fatalf("upstream of main = %q, %v, want origin/main", got, err)
	}
	gitRun(t, dir, "branch", "loose")
	if got, err := r.BranchUpstream("loose"); err != nil || got != "" {
		t.Fatalf("upstream of an untracked branch = %q, %v, want empty", got, err)
	}
	if got, err := r.BranchUpstream("no-such-branch"); err != nil || got != "" {
		t.Fatalf("upstream of a missing branch = %q, %v, want empty", got, err)
	}
	if _, err := r.BranchUpstream(""); err == nil {
		t.Fatal("an empty branch name was accepted")
	}

	// A mixed-case branch name holding dots reads through the same key the
	// tracking configuration is written to.
	gitRun(t, dir, "checkout", "-q", "-b", "Feature/V1.2")
	gitRun(t, dir, "push", "-q", "-u", "origin", "Feature/V1.2")
	if got, err := r.BranchUpstream("Feature/V1.2"); err != nil || got != "origin/Feature/V1.2" {
		t.Fatalf("upstream of Feature/V1.2 = %q, %v, want origin/Feature/V1.2", got, err)
	}
	gitRun(t, dir, "checkout", "-q", "main")

	// The remote branch gone: the configuration still answers, so a worker
	// guarding on it compares the upstream the user saw, and unsetting it
	// afterwards leaves nothing behind.
	gitRun(t, dir, "branch", "topic", "--track", "origin/main")
	gitRun(t, bare, "update-ref", "-d", "refs/heads/main")
	gitRun(t, dir, "update-ref", "-d", "refs/remotes/origin/main")
	if got, err := r.BranchUpstream("topic"); err != nil || got != "origin/main" {
		t.Fatalf("upstream of topic with a gone upstream = %q, %v, want origin/main", got, err)
	}
	if err := r.UnsetUpstream("topic"); err != nil {
		t.Fatal(err)
	}
	if got, err := r.BranchUpstream("topic"); err != nil || got != "" {
		t.Fatalf("upstream of topic after unsetting = %q, %v, want empty", got, err)
	}
}

// Unsetting the tracking reference drops both configuration keys and keeps
// the branch itself and its commits.
func TestBranchTrackingUnsetUpstream(t *testing.T) {
	dir, _, r := remoteRepo(t)
	gitRun(t, dir, "branch", "topic", "--track", "origin/main")
	if configValue(t, dir, "branch.topic.remote") != "origin" || configValue(t, dir, "branch.topic.merge") != "refs/heads/main" {
		t.Fatalf("fixture: tracking configuration = %q %q", configValue(t, dir, "branch.topic.remote"), configValue(t, dir, "branch.topic.merge"))
	}
	hashBefore := gitOut(t, dir, "rev-parse", "topic")

	if err := r.UnsetUpstream("topic"); err != nil {
		t.Fatal(err)
	}
	if got := configValue(t, dir, "branch.topic.remote"); got != "" {
		t.Fatalf("branch.topic.remote after unsetting = %q, want empty", got)
	}
	if got := configValue(t, dir, "branch.topic.merge"); got != "" {
		t.Fatalf("branch.topic.merge after unsetting = %q, want empty", got)
	}
	if got := gitOut(t, dir, "rev-parse", "topic"); got != hashBefore {
		t.Fatalf("topic moved by unsetting its upstream: %q, want %q", got, hashBefore)
	}
}

// A non-current branch behind its upstream fast-forwards without a checkout:
// the ref moves to the upstream's commit, the working tree stays on the
// current branch.
func TestFastForwardBranchMovesRef(t *testing.T) {
	dir, _, r := remoteRepo(t)
	oldHash, upstreamHash := behindFixture(t, dir)

	if err := r.FastForwardBranch("topic", oldHash, upstreamHash); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "rev-parse", "topic"); got != upstreamHash {
		t.Fatalf("topic after fast-forward = %q, want upstream %q", got, upstreamHash)
	}
	if got := gitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("current branch after fast-forward = %q, want main", got)
	}
}

// Moving a branch whose ref no longer holds the old value it was read with
// is refused by the atomic compare of update-ref: someone else's commit is
// never overwritten.
func TestFastForwardBranchRejectsStaleOldValue(t *testing.T) {
	dir, _, r := remoteRepo(t)
	oldHash, upstreamHash := behindFixture(t, dir)

	// The branch gains its own commit after oldHash was read.
	gitRun(t, dir, "checkout", "-q", "topic")
	writeFile(t, dir, "own.txt", "own\n")
	gitRun(t, dir, "add", "own.txt")
	gitRun(t, dir, "commit", "-q", "-m", "own work")
	gitRun(t, dir, "checkout", "-q", "main")
	moved := gitOut(t, dir, "rev-parse", "topic")
	if moved == oldHash {
		t.Fatalf("fixture: topic did not move: %q", moved)
	}

	if err := r.FastForwardBranch("topic", oldHash, upstreamHash); err == nil {
		t.Fatal("fast-forwarded with a stale old value")
	}
	if got := gitOut(t, dir, "rev-parse", "topic"); got != moved {
		t.Fatalf("topic overwritten by the refused fast-forward: %q, want %q", got, moved)
	}
}

// A target that does not descend from the branch is no fast-forward: the
// ref stays exactly where it was.
func TestFastForwardBranchRejectsNonFastForward(t *testing.T) {
	dir, _, r := remoteRepo(t)
	_, upstreamHash := behindFixture(t, dir)

	// topic gains a commit origin/topic does not have: it diverged.
	gitRun(t, dir, "checkout", "-q", "topic")
	writeFile(t, dir, "own.txt", "own\n")
	gitRun(t, dir, "add", "own.txt")
	gitRun(t, dir, "commit", "-q", "-m", "own work")
	gitRun(t, dir, "checkout", "-q", "main")
	diverged := gitOut(t, dir, "rev-parse", "topic")

	err := r.FastForwardBranch("topic", diverged, upstreamHash)
	if err == nil || !strings.Contains(err.Error(), "not ahead") {
		t.Fatalf("fast-forward of a diverged branch = %v, want a not-ahead error", err)
	}
	if got := gitOut(t, dir, "rev-parse", "topic"); got != diverged {
		t.Fatalf("diverged topic overwritten: %q, want %q", got, diverged)
	}
}

// A branch checked out in another worktree is never fast-forwarded without
// the checkout: moving its ref would corrupt that working tree.
func TestFastForwardBranchRejectsCheckedOutElsewhere(t *testing.T) {
	dir, _, r := remoteRepo(t)
	oldHash, upstreamHash := behindFixture(t, dir)

	wt := filepath.Join(t.TempDir(), "topic-checkout")
	gitRun(t, dir, "worktree", "add", "-q", wt, "topic")

	err := r.FastForwardBranch("topic", oldHash, upstreamHash)
	if err == nil || !strings.Contains(err.Error(), "checked out") {
		t.Fatalf("fast-forward of a checked out branch = %v, want a checked-out error", err)
	}
	if got := gitOut(t, dir, "rev-parse", "topic"); got != oldHash {
		t.Fatalf("checked out topic moved: %q, want %q", got, oldHash)
	}
}

// The branch checked out in the repository's own working tree is refused
// just the same: this write is only for branches nothing has checked out,
// and the current branch fast-forwards through a merge instead.
func TestFastForwardBranchRejectsCurrentBranch(t *testing.T) {
	dir, _, r := remoteRepo(t)
	base := gitOut(t, dir, "rev-parse", "main")
	writeFile(t, dir, "m.txt", "m\n")
	gitRun(t, dir, "add", "m.txt")
	gitRun(t, dir, "commit", "-q", "-m", "main 2")
	tip := gitOut(t, dir, "rev-parse", "main")
	gitRun(t, dir, "reset", "-q", "--hard", base)

	err := r.FastForwardBranch("main", base, tip)
	if err == nil || !strings.Contains(err.Error(), "checked out") {
		t.Fatalf("fast-forward of the current branch = %v, want a checked-out error", err)
	}
	if got := gitOut(t, dir, "rev-parse", "main"); got != base {
		t.Fatalf("current branch moved: %q, want %q", got, base)
	}
}

// Malformed input never reaches Git: an empty branch, or values that are no
// commit hashes, are refused before anything runs.
func TestFastForwardBranchValidatesInput(t *testing.T) {
	dir, _, r := remoteRepo(t)
	oldHash, upstreamHash := behindFixture(t, dir)

	for name, args := range map[string][3]string{
		"empty branch":   {"", oldHash, upstreamHash},
		"empty old":      {"topic", "", upstreamHash},
		"empty target":   {"topic", oldHash, ""},
		"old no hash":    {"topic", "not-a-hash", upstreamHash},
		"target no hash": {"topic", oldHash, upstreamHash + "x"},
	} {
		if err := r.FastForwardBranch(args[0], args[1], args[2]); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if got := gitOut(t, dir, "rev-parse", "topic"); got != oldHash {
		t.Fatalf("topic moved by the refused inputs: %q, want %q", got, oldHash)
	}
}
