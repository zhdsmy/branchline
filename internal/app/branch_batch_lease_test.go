package app

import (
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
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

// assertSidebarMatchesAfterDelete checks that the refreshed snapshot and
// sidebar UI reflect the post-delete repository state: absentLocals have no
// local branch rows, absentRemotes have no remote branch rows, and
// presentRemotes remain listed.
func assertSidebarMatchesAfterDelete(t *testing.T, h *testHarness, absentLocals, absentRemotes, presentRemotes []string) {
	t.Helper()
	refs := h.app.session.Snapshot().Refs
	for _, br := range absentLocals {
		if findLocalBranch(refs, br) != nil {
			t.Fatalf("refreshed snapshot still lists deleted local branch %s", br)
		}
		if _, ok := h.ui.Find("Delete branch " + br); ok {
			t.Fatalf("refreshed sidebar still renders deleted local branch %s; texts=%q", br, h.ui.Texts())
		}
	}
	for _, rb := range absentRemotes {
		if findRemoteBranch(refs, rb) != nil {
			t.Fatalf("refreshed snapshot still lists deleted remote branch %s", rb)
		}
		if _, ok := h.ui.Find(rb); ok {
			t.Fatalf("refreshed sidebar still shows deleted remote branch %s; texts=%q", rb, h.ui.Texts())
		}
	}
	for _, rb := range presentRemotes {
		if findRemoteBranch(refs, rb) == nil {
			t.Fatalf("refreshed snapshot dropped kept remote branch %s", rb)
		}
		if _, ok := h.ui.Find(rb); !ok {
			t.Fatalf("refreshed sidebar dropped kept remote branch %s; texts=%q", rb, h.ui.Texts())
		}
	}
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
	if !strings.Contains(err.Error(), "origin/rb1") {
		t.Fatalf("ActionErr = %q, want it to name origin/rb1", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "fetch") {
		t.Fatalf("ActionErr = %q, want it to advise fetching first", err)
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
	assertSidebarMatchesAfterDelete(t, h, []string{"rb1", "rb2"}, []string{"origin/rb2"}, []string{"origin/rb1"})
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
	if !strings.Contains(err.Error(), "origin/rb1") {
		t.Fatalf("ActionErr = %q, want it to name origin/rb1", err)
	}
	if !strings.Contains(err.Error(), "stale info") {
		t.Fatalf("ActionErr = %q, want the lease rejection git printed", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "fetch") {
		t.Fatalf("ActionErr = %q, want it to advise fetching first", err)
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
	assertSidebarMatchesAfterDelete(t, h, []string{"rb1", "rb2"}, []string{"origin/rb2"}, []string{"origin/rb1"})
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

// Two local branches tracking the same upstream send exactly one leased
// remote deletion so the second branch does not fail against the lease the
// first already consumed.
func TestBranchBatchDeleteLeaseSharedUpstreamDeletesOnce(t *testing.T) {
	repo := newGitRepo(t, "batch-lease-shared")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	for _, br := range []string{"rb1", "rb2"} {
		repo.CreateBranch(br)
	}
	bareDir := repo.AddBareRemote("origin")
	repo.Git("push", "-q", "origin", "rb1:refs/heads/shared")
	for _, br := range []string{"rb1", "rb2"} {
		repo.Git("branch", "--set-upstream-to", "origin/shared", br)
	}
	wantHash := gitOutTrim(t, repo.Path(), "rev-parse", "refs/remotes/origin/shared")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	recording := installRecordingExecutor(t, h)
	openLeaseDeleteDialog(t, h)
	confirmLeaseDelete(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("deleting two branches with a shared upstream failed: %v", err)
	}
	for _, br := range []string{"rb1", "rb2"} {
		if branchExists(t, repo, br) {
			t.Fatalf("local %s survived the confirmed batch delete", br)
		}
	}
	if bareHasBranch(t, bareDir, "shared") {
		t.Fatal("origin/shared survived on the server")
	}
	pushes := recordedPushes(recording)
	if len(pushes) != 1 {
		t.Fatalf("recorded %d pushes for a shared upstream, want 1: %v", len(pushes), pushes)
	}
	lease := "--force-with-lease=refs/heads/shared:" + wantHash
	if !hasArg(pushes[0], lease) || !hasArg(pushes[0], "--delete") || !hasArg(pushes[0], "refs/heads/shared") {
		t.Fatalf("shared upstream push = %v, want lease %q deleting refs/heads/shared", pushes[0], lease)
	}
}

// When multiple remote deletions fail after local branches are deleted, the
// error clearly names every failed remote branch and advises fetching, while
// the refreshed UI matches the repository state.
func TestBranchBatchDeleteLeaseMultipleRemoteFailuresNamed(t *testing.T) {
	repo, bareDir := batchLeaseFixture(t, "batch-lease-multi-fail")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openLeaseDeleteDialog(t, h)
	moved := peerMovesServerBranch(t, bareDir, "rb1")
	runGitCommand(t, bareDir, "update-ref", "-d", "refs/heads/rb2")
	confirmLeaseDelete(t, h)

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("expected errors for both failed remote branches")
	}
	for _, want := range []string{"origin/rb1", "origin/rb2", "stale info"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ActionErr = %q, want it to contain %q", err, want)
		}
	}
	if !strings.Contains(strings.ToLower(err.Error()), "fetch") {
		t.Fatalf("ActionErr = %q, want a fetch hint", err)
	}
	if got := gitOutTrim(t, bareDir, "rev-parse", "refs/heads/rb1"); got != moved {
		t.Fatalf("server rb1 = %q, want moved commit %q", got, moved)
	}
	if bareHasBranch(t, bareDir, "rb2") {
		t.Fatal("rb2 reappeared on the server")
	}
	for _, br := range []string{"rb1", "rb2"} {
		if branchExists(t, repo, br) {
			t.Fatalf("local %s survived the confirmed batch delete", br)
		}
	}
	assertSidebarMatchesAfterDelete(t, h, []string{"rb1", "rb2"}, nil, []string{"origin/rb1", "origin/rb2"})
}

// Deleting a tracking reference locally after the dialog opened refuses the
// whole batch without deleting any local or remote branch.
func TestBranchBatchDeleteLeaseMissingTrackingRefRefusesBatch(t *testing.T) {
	repo, bareDir := batchLeaseFixture(t, "batch-lease-gone-ref")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openLeaseDeleteDialog(t, h)
	repo.Git("update-ref", "-d", "refs/remotes/origin/rb1")
	confirmLeaseDelete(t, h)

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("deleting when a tracking ref disappeared produced no error")
	}
	if !strings.Contains(err.Error(), "origin/rb1 changed after the dialog opened") {
		t.Fatalf("ActionErr = %q, want changed-after-the-dialog-opened refusal naming origin/rb1", err)
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

// Batch branch deletion with leased upstream deletion works end to end over
// SSH on the configured Linux host (including Git 2.25): a server-moved
// upstream is protected by --force-with-lease while clean upstreams and
// shared upstreams delete cleanly and the refreshed sidebar matches server
// state.
func TestConfiguredLinuxBatchDeleteBranchesLease(t *testing.T) {
	repo, srv := configuredLinuxRepo(t)
	bare := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repo.Path, "remote", "add", "origin", bare)
	for _, br := range []string{"rb-clean", "rb-moved", "rb-shared-a", "rb-shared-b"} {
		srv.RunGit(t, repo.Path, "branch", br)
	}
	srv.RunGit(t, repo.Path, "push", "-q", "origin",
		"rb-clean:refs/heads/rb-clean",
		"rb-moved:refs/heads/rb-moved",
		"rb-shared-a:refs/heads/shared")
	srv.RunGit(t, repo.Path, "branch", "--set-upstream-to", "origin/rb-clean", "rb-clean")
	srv.RunGit(t, repo.Path, "branch", "--set-upstream-to", "origin/rb-moved", "rb-moved")
	srv.RunGit(t, repo.Path, "branch", "--set-upstream-to", "origin/shared", "rb-shared-a")
	srv.RunGit(t, repo.Path, "branch", "--set-upstream-to", "origin/shared", "rb-shared-b")
	srv.RunGit(t, repo.Path, "fetch", "-q", "origin")

	h := newTestApp(t)
	openSSHRepoThroughSwitcher(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	// Round 1: rb-clean and rb-moved, with rb-moved advanced on the server
	// bare repo after opening the dialog and without fetching.
	selectBranchesForBatch(t, h, "rb-clean", "rb-moved")
	chooseBatchItem(t, h, "rb-clean", "Delete 2 Branches…")
	if err := h.ui.Click("Also delete corresponding remote branches"); err != nil {
		t.Fatalf("tick remote deletion: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()

	peerParent := srv.TempDir(t, "lease-peer")
	peerDir := peerParent + "/clone"
	moved := strings.TrimSpace(srv.RunShell(t, `set -eu
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 LC_ALL=C
git clone -q `+git.ShellQuote(bare)+` `+git.ShellQuote(peerDir)+`
cd `+git.ShellQuote(peerDir)+`
git config user.name 'Peer User'
git config user.email 'peer@branchline.invalid'
git config commit.gpgsign false
git config core.hooksPath /dev/null
git checkout -q -b peerwork origin/rb-moved
printf 'server move\n' > moved.txt
git add moved.txt
git commit -q -m 'server move on linux'
git push -q origin peerwork:refs/heads/rb-moved
git rev-parse HEAD
`))
	if moved == "" {
		t.Fatal("failed to advance rb-moved on server bare repo")
	}

	confirmLeaseDelete(t, h)
	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("expected lease error for server-moved rb-moved on Linux SSH")
	}
	for _, want := range []string{"origin/rb-moved", "stale info"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("SSH ActionErr = %q, want %q", err, want)
		}
	}
	if !strings.Contains(strings.ToLower(err.Error()), "fetch") {
		t.Fatalf("SSH ActionErr = %q, want a fetch hint", err)
	}
	for _, br := range []string{"rb-clean", "rb-moved"} {
		if remoteBranchExists(t, srv, repo.Path, br) {
			t.Fatalf("local %s survived on server repo", br)
		}
	}
	if remoteBranchExists(t, srv, bare, "rb-clean") {
		t.Fatal("rb-clean survived on server bare remote")
	}
	if !remoteBranchExists(t, srv, bare, "rb-moved") {
		t.Fatal("rb-moved was deleted on server bare remote despite moving past the lease")
	}
	if got := srv.RunGit(t, bare, "rev-parse", "refs/heads/rb-moved"); got != moved {
		t.Fatalf("server bare rb-moved = %q, want moved commit %q", got, moved)
	}
	assertSidebarMatchesAfterDelete(t, h, []string{"rb-clean", "rb-moved"}, []string{"origin/rb-clean"}, []string{"origin/rb-moved"})

	// Round 2: rb-shared-a and rb-shared-b both track origin/shared; deleting
	// both sends one leased push and completes cleanly.
	selectBranchesForBatch(t, h, "rb-shared-a", "rb-shared-b")
	chooseBatchItem(t, h, "rb-shared-a", "Delete 2 Branches…")
	if err := h.ui.Click("Also delete corresponding remote branches"); err != nil {
		t.Fatalf("tick remote deletion for shared upstream: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	confirmLeaseDelete(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH batch delete with shared upstream errored: %v", err)
	}
	for _, br := range []string{"rb-shared-a", "rb-shared-b"} {
		if remoteBranchExists(t, srv, repo.Path, br) {
			t.Fatalf("local %s survived on server repo", br)
		}
	}
	if remoteBranchExists(t, srv, bare, "shared") {
		t.Fatal("shared upstream survived on server bare remote")
	}
	if _, ok := h.ui.Find("origin/shared"); ok {
		t.Fatalf("refreshed sidebar still shows deleted origin/shared; texts=%q", h.ui.Texts())
	}
}
