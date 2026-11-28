package app

import (
	"strings"
	"testing"
)

// batchLeaseFixture builds a repository whose branches rb1 and rb2 track
// their copies on a bare origin, returning the fixture and the bare
// directory the upstreams live on.
func batchLeaseFixture(t *testing.T, name string) (repo *gitRepoFixture, bareDir string) {
	t.Helper()
	repo = newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	for _, br := range []string{"rb1", "rb2"} {
		repo.CreateBranch(br)
	}
	bareDir = repo.AddBareRemote("origin")
	repo.Git("push", "-q", "origin", "rb1:refs/heads/rb1", "rb2:refs/heads/rb2")
	for _, br := range []string{"rb1", "rb2"} {
		repo.Git("branch", "--set-upstream-to", "origin/"+br, br)
	}
	return repo, bareDir
}

// openLeaseDeleteDialog selects rb1 and rb2, opens their Delete Branches
// dialog and ticks the remote deletion box, leaving the dialog open for the
// test to disturb the repository before confirming.
func openLeaseDeleteDialog(t *testing.T, h *testHarness) {
	t.Helper()
	selectBranchesForBatch(t, h, "rb1", "rb2")
	chooseBatchItem(t, h, "rb1", "Delete 2 Branches…")
	if !hasExactText(h.ui, "Delete Branches") {
		t.Fatalf("Delete Branches dialog did not open; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Also delete corresponding remote branches"); err != nil {
		t.Fatalf("tick remote deletion: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
}

// confirmLeaseDelete clicks the dialog's Delete button and waits for the
// worker.
func confirmLeaseDelete(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Confirm Delete Branches"); err != nil {
		t.Fatalf("click Confirm Delete Branches: %v", err)
	}
	h.drain()
}

// peerMovesServerBranch clones the bare repository, adds a commit on top of
// the branch and pushes it back, moving the branch on the server without the
// local repository fetching it. The pushed commit is returned.
func peerMovesServerBranch(t *testing.T, bareDir, branch string) string {
	t.Helper()
	peer := t.TempDir() + "/peer"
	runGitCommand(t, "", "clone", "-q", bareDir, peer)
	runGitCommand(t, peer, "-c", "user.name=Peer", "-c", "user.email=peer@test.local", "checkout", "-q", "-b", "peerwork", "origin/"+branch)
	writeTestFile(t, peer, "peer.txt", "server move\n")
	runGitCommand(t, peer, "add", "peer.txt")
	runGitCommand(t, peer, "-c", "user.name=Peer", "-c", "user.email=peer@test.local", "commit", "-qm", "server move")
	runGitCommand(t, peer, "push", "-q", "origin", "peerwork:"+branch)
	return gitOutTrim(t, peer, "rev-parse", "HEAD")
}

// recordedPushes copies the recorded git invocations that were pushes.
func recordedPushes(e *recordingRunExecutor) [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out [][]string
	for _, args := range e.cmds {
		if len(args) > 0 && args[0] == "push" {
			out = append(out, append([]string(nil), args...))
		}
	}
	return out
}

// hasArg reports whether args holds want as one exact argument.
func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// A branch someone else moved on the server after the dialog opened is never
// deleted: its lease fails, the server keeps the branch at the moved commit,
// the local branches are still deleted, and the error names the branch.
func TestBranchBatchDeleteLeaseKeepsServerMovedBranch(t *testing.T) {
	repo, bareDir := batchLeaseFixture(t, "batch-lease-moved")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openLeaseDeleteDialog(t, h)
	moved := peerMovesServerBranch(t, bareDir, "rb1")
	confirmLeaseDelete(t, h)

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("deleting over a server-moved branch produced no error")
	}
	if !strings.Contains(err.Error(), "rb1") {
		t.Fatalf("ActionErr = %q, want it to name rb1", err)
	}
	if !bareHasBranch(t, bareDir, "rb1") {
		t.Fatal("rb1 was deleted even though the server moved it past the lease")
	}
	if got := gitOutTrim(t, bareDir, "rev-parse", "refs/heads/rb1"); got != moved {
		t.Fatalf("server rb1 = %q, want the moved commit %q", got, moved)
	}
	if bareHasBranch(t, bareDir, "rb2") {
		t.Fatal("rb2 survived even though nothing moved it: one failure never stops the batch")
	}
	for _, br := range []string{"rb1", "rb2"} {
		if branchExists(t, repo, br) {
			t.Fatalf("local %s survived the confirmed batch delete", br)
		}
	}
}

// A tracking reference moved by a local fetch after the dialog opened
// refuses the whole batch: nothing is deleted, not even the branches that
// never moved, and the error says which remote branch changed.
func TestBranchBatchDeleteLeaseMovedTrackingRefRefusesBatch(t *testing.T) {
	repo, bareDir := batchLeaseFixture(t, "batch-lease-fetch")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openLeaseDeleteDialog(t, h)
	peerMovesServerBranch(t, bareDir, "rb1")
	// The local repository fetches: the tracking reference of origin/rb1 now
	// points at a commit the dialog never saw.
	repo.Git("fetch", "-q", "origin")
	confirmLeaseDelete(t, h)

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("deleting over a moved tracking reference produced no error")
	}
	if !strings.Contains(err.Error(), "origin/rb1 changed after the dialog opened") {
		t.Fatalf("ActionErr = %q, want the changed-after-the-dialog-opened refusal naming origin/rb1", err)
	}
	for _, br := range []string{"rb1", "rb2"} {
		if !branchExists(t, repo, br) {
			t.Fatalf("local %s was deleted even though the batch was refused", br)
		}
		if !bareHasBranch(t, bareDir, br) {
			t.Fatalf("server %s was deleted even though the batch was refused", br)
		}
	}
}

// Deleting with the remote box ticked removes the local branches and their
// upstreams, and every server deletion goes out with a force-with-lease
// pinning the exact commit the tracking reference pointed at.
func TestBranchBatchDeleteLeaseDeletesLocalsAndUpstreams(t *testing.T) {
	repo, bareDir := batchLeaseFixture(t, "batch-lease-clean")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	wantHash := map[string]string{}
	for _, br := range []string{"rb1", "rb2"} {
		wantHash[br] = gitOutTrim(t, repo.Path(), "rev-parse", "refs/remotes/origin/"+br)
	}
	recording := installRecordingExecutor(t, h)

	openLeaseDeleteDialog(t, h)
	confirmLeaseDelete(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected batch delete error: %v", err)
	}
	for _, br := range []string{"rb1", "rb2"} {
		if branchExists(t, repo, br) {
			t.Fatalf("local %s survived the confirmed batch delete", br)
		}
		if bareHasBranch(t, bareDir, br) {
			t.Fatalf("origin/%s survived on the server even though the remote box was ticked", br)
		}
	}

	// Every push that deletes is leased on the commit the dialog captured.
	pushes := recordedPushes(recording)
	if len(pushes) != 2 {
		t.Fatalf("recorded %d pushes, want exactly the two deletions: %v", len(pushes), pushes)
	}
	for _, br := range []string{"rb1", "rb2"} {
		lease := "--force-with-lease=refs/heads/" + br + ":" + wantHash[br]
		found := false
		for _, args := range pushes {
			if hasArg(args, lease) && hasArg(args, "--delete") && hasArg(args, "refs/heads/"+br) {
				found = true
			}
		}
		if !found {
			t.Fatalf("no push carried lease %q with the deletion of %s: %v", lease, br, pushes)
		}
	}
}

// A branch the server no longer has is reported, not silently skipped: the
// lease push is rejected, the error carries git's lease rejection, and the
// local branches are still deleted.
func TestBranchBatchDeleteLeaseServerMissingBranchErrors(t *testing.T) {
	repo, bareDir := batchLeaseFixture(t, "batch-lease-missing")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openLeaseDeleteDialog(t, h)
	// rb1 leaves the server behind the repository's back: the tracking
	// reference still points at the commit the dialog captured.
	runGitCommand(t, bareDir, "update-ref", "-d", "refs/heads/rb1")
	confirmLeaseDelete(t, h)

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("deleting a branch the server lost produced no error")
	}
	if !strings.Contains(err.Error(), "stale info") {
		t.Fatalf("ActionErr = %q, want the lease rejection git printed", err)
	}
	if bareHasBranch(t, bareDir, "rb1") {
		t.Fatal("rb1 reappeared on the server")
	}
	if bareHasBranch(t, bareDir, "rb2") {
		t.Fatal("rb2 survived even though it was present on the server")
	}
	for _, br := range []string{"rb1", "rb2"} {
		if branchExists(t, repo, br) {
			t.Fatalf("local %s survived the confirmed batch delete", br)
		}
	}
}

// Deleting with the remote box unticked never touches the server: not one
// push runs, and the upstreams stay exactly where they are.
func TestBranchBatchDeleteLeaseUntickedRunsNoPush(t *testing.T) {
	repo, bareDir := batchLeaseFixture(t, "batch-lease-unticked")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	recording := installRecordingExecutor(t, h)

	selectBranchesForBatch(t, h, "rb1", "rb2")
	chooseBatchItem(t, h, "rb1", "Delete 2 Branches…")
	if !hasExactText(h.ui, "Delete Branches") {
		t.Fatalf("Delete Branches dialog did not open; texts=%q", h.ui.Texts())
	}
	confirmLeaseDelete(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected batch delete error: %v", err)
	}
	if pushes := recordedPushes(recording); len(pushes) != 0 {
		t.Fatalf("the unticked box still ran %d pushes: %v", len(pushes), pushes)
	}
	for _, br := range []string{"rb1", "rb2"} {
		if branchExists(t, repo, br) {
			t.Fatalf("local %s survived the confirmed batch delete", br)
		}
		if !bareHasBranch(t, bareDir, br) {
			t.Fatalf("origin/%s is gone even though the remote box was unticked", br)
		}
	}
}
