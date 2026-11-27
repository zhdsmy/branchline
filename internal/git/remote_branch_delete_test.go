package git

import (
	"strings"
	"testing"
)

// leaseFixture makes a repository with feature pushed to a bare origin, so
// refs/remotes/origin/feature tracks the server's branch. It returns the
// bare repository's directory and the commit feature is at.
func leaseFixture(t *testing.T) (dir, bare, oid string, r *Repo) {
	t.Helper()
	dir, bare, r = remoteRepo(t)
	gitRun(t, dir, "branch", "feature")
	gitRun(t, dir, "push", "-q", "origin", "feature")
	oid = gitOut(t, dir, "rev-parse", "refs/remotes/origin/feature")
	return dir, bare, oid, r
}

// moveServerBranch simulates someone else moving the server's feature: a
// second clone commits on top of it and pushes. This clone does not fetch,
// so its tracking ref still points at the old commit.
func moveServerBranch(t *testing.T, bare string) string {
	t.Helper()
	other := initRepo(t)
	gitRun(t, other, "fetch", "-q", bare, "feature:feature")
	gitRun(t, other, "checkout", "-q", "feature")
	writeFile(t, other, "moved.txt", "moved\n")
	gitRun(t, other, "add", "moved.txt")
	gitRun(t, other, "commit", "-q", "-m", "moved by someone else")
	gitRun(t, other, "push", "-q", bare, "feature:feature")
	return gitOut(t, other, "rev-parse", "feature")
}

// Deleting a remote branch with the lease value the server's branch is at
// deletes it on the server and drops the local tracking ref with it.
func TestDeleteRemoteBranchLeaseDeletes(t *testing.T) {
	dir, bare, oid, r := leaseFixture(t)

	if err := r.DeleteRemoteBranchLease("origin", "feature", oid); err != nil {
		t.Fatal(err)
	}
	if hasRef(bare, "refs/heads/feature") {
		t.Fatalf("feature survived on the server: %s", gitOut(t, bare, "branch"))
	}
	if hasRef(dir, "refs/remotes/origin/feature") {
		t.Fatal("the tracking ref survived the delete")
	}
	if !hasRef(bare, "refs/heads/main") {
		t.Fatal("the delete took main with it")
	}
}

// A branch someone else moved since the expectation was read is never
// deleted: git rejects the lease with "stale info" and the server keeps the
// branch at the new commit.
func TestDeleteRemoteBranchLeaseStaleRefused(t *testing.T) {
	_, bare, oid, r := leaseFixture(t)
	moved := moveServerBranch(t, bare)
	if moved == oid {
		t.Fatal("fixture broken: moving the server branch did not change its commit")
	}

	err := r.DeleteRemoteBranchLease("origin", "feature", oid)
	if err == nil {
		t.Fatal("deleting a moved branch unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "stale info") {
		t.Fatalf("error %q does not name the lease refusal", err)
	}
	if got := gitOut(t, bare, "rev-parse", "refs/heads/feature"); got != moved {
		t.Fatalf("server feature = %q after the refused delete, want %q", got, moved)
	}
}

// An empty or malformed expectation is refused before anything is sent: a
// lease whose value git cannot parse protects nothing. The server's branch
// is untouched.
func TestDeleteRemoteBranchLeaseRejectsBadExpect(t *testing.T) {
	_, bare, _, r := leaseFixture(t)

	for _, expect := range []string{"", "xyz", "1234", strings.Repeat("0", 39), strings.Repeat("g", 40)} {
		err := r.DeleteRemoteBranchLease("origin", "feature", expect)
		if err == nil {
			t.Fatalf("expect %q was accepted", expect)
		}
		if !strings.Contains(err.Error(), "expected to be at") {
			t.Fatalf("error for expect %q = %q, want it to explain the expectation", expect, err)
		}
	}
	if !hasRef(bare, "refs/heads/feature") {
		t.Fatal("feature vanished from the server even though every expectation was refused")
	}
}

// Deleting a branch the server no longer has is an error, not a silent
// success.
func TestDeleteRemoteBranchLeaseMissingOnServer(t *testing.T) {
	_, bare, oid, r := leaseFixture(t)

	err := r.DeleteRemoteBranchLease("origin", "nosuch", oid)
	if err == nil {
		t.Fatal("deleting a branch the server does not have unexpectedly succeeded")
	}
	if !hasRef(bare, "refs/heads/feature") {
		t.Fatal("the failed delete took feature with it")
	}
}

// Names that would corrupt the lease spec or be read as options are refused
// before anything is sent.
func TestDeleteRemoteBranchLeaseRejectsBadNames(t *testing.T) {
	_, bare, oid, r := leaseFixture(t)

	for _, tc := range []struct{ remote, branch string }{
		{"", "feature"},
		{"origin", ""},
		{"-o", "feature"},
		{"bad remote", "feature"},
		{"bad\nremote", "feature"},
		{"origin", "-D"},
		{"origin", "feature/"},
		{"origin", "has space"},
		{"origin", "bad:branch"},
		{"origin", "bad..branch"},
	} {
		if err := r.DeleteRemoteBranchLease(tc.remote, tc.branch, oid); err == nil {
			t.Fatalf("remote %q branch %q was accepted", tc.remote, tc.branch)
		}
	}
	if got := gitOut(t, bare, "rev-parse", "refs/heads/feature"); got != oid {
		t.Fatalf("server feature = %q after the refused inputs, want %q", got, oid)
	}
	// A branch holding a newline would split the lease spec; git itself
	// must never see it.
	if err := r.DeleteRemoteBranchLease("origin", "bad\nbranch", oid); err == nil {
		t.Fatal("a newline in the branch name was accepted")
	}
}
