package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
	"github.com/zhdsmy/branchline/internal/store"
)

// closeSync registers cleanup of the per-app sync/askpass state so tests leak
// neither parked askpass goroutines nor branchline-askpass* temp directories.
// One t.Cleanup performs the three teardown steps in a fixed order, immune
// to the LIFO interleaving with the cleanup newTestApp registered first:
//  1. close the dialog — releases every worker parked on a question and turns
//     questions arriving mid-teardown down at once, so the drain cannot hang;
//  2. drain every worker — releaseAllAndWait is idempotent, so the call
//     newTestApp's own cleanup repeats afterwards is a no-op;
//  3. remove the askpass script directory — only once no worker can still
//     race its lazy creation (a worker inside AskPass.Env writes the very
//     a.dir field AskPass.Close reads without a lock).
func closeSync(t *testing.T, h *testHarness) {
	t.Helper()
	t.Cleanup(func() {
		teardownSync(h)
	})
}

// teardownSync performs the three sync-state teardown steps in the only safe
// order: close the dialog first so no worker can stay parked on a question,
// drain every worker next, and remove the askpass script directory last —
// only once no worker can still race its lazy creation (a worker inside
// AskPass.Env writes the very a.dir field AskPass.Close reads without a
// lock). releaseAllAndWait is idempotent, so the repeat in newTestApp's own
// cleanup afterwards is a no-op.
func teardownSync(h *testHarness) {
	st := h.app.closeSyncUIDialog()
	h.queue.releaseAllAndWait()
	closeSyncUIAskPass(st)
}

// gatedLen reports how many paused workers the queue holds, under the queue
// lock that guards the slice.
func gatedLen(q *uiQueue) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.gated)
}

// openSyncRepo opens a local repository in the app, drains the initial load,
// and registers sync-state cleanup.
func openSyncRepo(t *testing.T, h *testHarness, r store.Repo) {
	t.Helper()
	closeSync(t, h)
	h.openRepo(r)
	h.drain()
}

// The toolbar Fetch/Pull/Push buttons run the real git operations against a
// bare remote: Push publishes a branch without upstream and sets upstream,
// Fetch updates the remote-tracking ref without moving HEAD, and Pull
// fast-forwards HEAD and the working tree.
func TestSyncLocalFetchPullPushAndSetUpstream(t *testing.T) {
	repo := newGitRepo(t, "sync-local")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	// Create a local branch without an upstream and publish it with Push.
	repo.Git("checkout", "-b", "feature")
	featureHead := repo.CommitFile("feature.txt", "feature\n", "feature commit")
	// The external checkout and commit happened behind the snapshot's back:
	// reload so the Push dialog sees the new branch.
	h.app.session.Reload()
	h.drain()

	clickPushConfirm(t, h)
	h.drain()

	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/feature")); got != featureHead {
		t.Fatalf("bare refs/heads/feature = %q, want %q", got, featureHead)
	}
	if got := strings.TrimSpace(repo.Git("config", "branch.feature.remote")); got != "origin" {
		t.Fatalf("branch.feature.remote = %q, want origin", got)
	}
	if got := strings.TrimSpace(repo.Git("config", "branch.feature.merge")); got != "refs/heads/feature" {
		t.Fatalf("branch.feature.merge = %q, want refs/heads/feature", got)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected action error after push: %v", err)
	}

	// A peer advances the remote; Fetch updates origin/feature without moving HEAD.
	peerDir := filepath.Join(t.TempDir(), "peer")
	runGitCommand(t, "", "clone", bareDir, peerDir)
	runGitCommand(t, peerDir, "config", "user.name", "Peer")
	runGitCommand(t, peerDir, "config", "user.email", "peer@branchline.local")
	runGitCommand(t, peerDir, "checkout", "feature")
	writeErr := os.WriteFile(filepath.Join(peerDir, "peer.txt"), []byte("peer\n"), 0o644)
	if writeErr != nil {
		t.Fatalf("write peer file: %v", writeErr)
	}
	runGitCommand(t, peerDir, "add", "--", "peer.txt")
	runGitCommand(t, peerDir, "commit", "-m", "peer commit")
	runGitCommand(t, peerDir, "push", "origin", "feature")
	peerHead := strings.TrimSpace(runGitCommand(t, peerDir, "rev-parse", "HEAD"))

	headBefore := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	if err := h.ui.ClickWith(ui.Alt, "Fetch"); err != nil {
		t.Fatalf("click Fetch: %v", err)
	}
	h.drain()

	if got := strings.TrimSpace(repo.Git("rev-parse", "origin/feature")); got != peerHead {
		t.Fatalf("after Fetch origin/feature = %q, want %q", got, peerHead)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != headBefore {
		t.Fatalf("Fetch moved HEAD: got %q, want unchanged %q", got, headBefore)
	}

	// Pull fast-forwards HEAD and updates the working tree.
	if err := h.ui.Click("Pull"); err != nil {
		t.Fatalf("click Pull: %v", err)
	}
	h.drain()

	if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != peerHead {
		t.Fatalf("after Pull HEAD = %q, want %q", got, peerHead)
	}
	if data, err := os.ReadFile(filepath.Join(repo.Path(), "peer.txt")); err != nil || string(data) != "peer\n" {
		t.Fatalf("working tree peer.txt = %q, err=%v; want peer\\n", data, err)
	}
	if snap := h.app.session.Snapshot(); snap.Status.Head != peerHead {
		t.Fatalf("snapshot HEAD after pull = %q, want %q", snap.Status.Head, peerHead)
	}

	// Pulling again toasts the up-to-date note instead of an error.
	if err := h.ui.Click("Pull"); err != nil {
		t.Fatalf("click Pull again: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("up-to-date pull reported an error: %v", err)
	}
	if !h.ui.HasText(git.UpToDatePull) {
		t.Fatalf("up-to-date pull toast missing; texts=%q", h.ui.Texts())
	}

	// Pushing with nothing to send toasts the up-to-date note.
	clickPushConfirm(t, h)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("up-to-date push reported an error: %v", err)
	}
	if !h.ui.HasText(git.UpToDatePush) {
		t.Fatalf("up-to-date push toast missing; texts=%q", h.ui.Texts())
	}
}

// Switching repositories clears the validation error of the previous one so
// it cannot leak into the new repository's toolbar.
func TestSyncErrorClearedOnRepoSwitch(t *testing.T) {
	repoA := newGitRepo(t, "sync-switch-a")
	repoA.CommitFile("a.txt", "a\n", "init a")
	repoB := newGitRepo(t, "sync-switch-b")
	repoB.CommitFile("b.txt", "b\n", "init b")
	repoB.AddBareRemote("origin")

	h := newTestApp(t, repoA.StoreRepo(), repoB.StoreRepo())
	closeSync(t, h)
	h.openRepo(repoA.StoreRepo())
	h.drain()

	// repoA has no remote: Push shows the validation error.
	if err := h.ui.Click("Push"); err != nil {
		t.Fatalf("click Push: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("The repository has no remote to push to.") {
		t.Fatalf("missing no-remote message before switch; texts=%q", h.ui.Texts())
	}

	// Switch to repoB through the Repository Switcher: the error must be gone.
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click(repoB.StoreRepo().Name); err != nil {
		t.Fatalf("select repoB: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if h.ui.HasText("The repository has no remote to push to.") {
		t.Fatal("validation error of repoA leaked into repoB")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); ok {
		t.Fatal("Repository Error still visible after switching repositories")
	}
}

// A Push click on a repository without any remote, and one with detached HEAD,
// shows a dismissible pre-flight error and never runs git push.
func TestSyncPushValidationErrorsDismiss(t *testing.T) {
	t.Run("no remote", func(t *testing.T) {
		repo := newGitRepo(t, "sync-noremote")
		repo.CommitFile("a.txt", "one\n", "initial commit")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		if err := h.ui.Click("Push"); err != nil {
			t.Fatalf("click Push: %v", err)
		}
		h.ui.Frame()

		info, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error")
		if !ok {
			t.Fatalf("no Repository Error status after Push without remote; texts=%q", h.ui.Texts())
		}
		_ = info
		if !h.ui.HasText("The repository has no remote to push to.") {
			t.Fatalf("missing no-remote message; texts=%q", h.ui.Texts())
		}
		if h.app.session.Busy() {
			t.Fatal("session busy for a push that should not have started")
		}

		if err := h.ui.Click("Dismiss Error"); err != nil {
			t.Fatalf("click Dismiss Error: %v", err)
		}
		h.ui.Frame()
		if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); ok {
			t.Fatal("Repository Error still visible after dismiss")
		}
	})

	t.Run("detached HEAD", func(t *testing.T) {
		repo := newGitRepo(t, "sync-detached")
		head := repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.AddBareRemote("origin")
		repo.Git("checkout", head) // detach

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		if err := h.ui.Click("Push"); err != nil {
			t.Fatalf("click Push: %v", err)
		}
		h.ui.Frame()

		if !h.ui.HasText("HEAD is detached: check out a branch to push it.") {
			t.Fatalf("missing detached message; texts=%q", h.ui.Texts())
		}
		if err := h.ui.Click("Dismiss Error"); err != nil {
			t.Fatalf("click Dismiss Error: %v", err)
		}
		h.ui.Frame()
		if h.ui.HasText("HEAD is detached") {
			t.Fatal("detached error still visible after dismiss")
		}
	})
}

// A pre-flight validation error replaces any stale action error of an earlier
// operation: the toolbar shows the new validation error, not the old failure.
func TestSyncPushValidationErrorReplacesStaleActionErr(t *testing.T) {
	t.Run("detached HEAD", func(t *testing.T) {
		repo := newGitRepo(t, "sync-stale-detached")
		head := repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.AddBareRemote("origin")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		// A failing Fetch leaves its error in the session.
		repo.Git("remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
		clickFetch(t, h)
		h.drain()
		if err := h.app.session.ActionErr(); err == nil {
			t.Fatal("expected ActionErr after failed Fetch")
		}
		h.ui.Frame()
		if !h.ui.HasText("git fetch") {
			t.Fatalf("stale fetch error not visible before Push; texts=%q", h.ui.Texts())
		}

		// Detach HEAD through the CLI, reload the snapshot, then Push: the
		// validation error must replace the stale fetch error in the toolbar.
		repo.Git("checkout", head)
		h.app.session.Reload()
		h.drain()
		clickPush(t, h)
		h.ui.Frame()

		if !h.ui.HasText("HEAD is detached: check out a branch to push it.") {
			t.Fatalf("detached validation error missing; texts=%q", h.ui.Texts())
		}
		if h.ui.HasText("git fetch") {
			t.Fatalf("stale fetch error still shown over the validation error; texts=%q", h.ui.Texts())
		}
		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("stale ActionErr not cleared: %v", err)
		}
	})

	t.Run("no remote", func(t *testing.T) {
		repo := newGitRepo(t, "sync-stale-noremote")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.AddBareRemote("origin")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		// A failing Fetch leaves its error in the session.
		repo.Git("remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
		clickFetch(t, h)
		h.drain()
		if err := h.app.session.ActionErr(); err == nil {
			t.Fatal("expected ActionErr after failed Fetch")
		}

		// Remove the remote entirely and reload: Push must show the no-remote
		// validation error instead of the stale fetch failure.
		repo.Git("remote", "remove", "origin")
		h.app.session.Reload()
		h.drain()
		clickPush(t, h)
		h.ui.Frame()

		if !h.ui.HasText("The repository has no remote to push to.") {
			t.Fatalf("no-remote validation error missing; texts=%q", h.ui.Texts())
		}
		if h.ui.HasText("git fetch") {
			t.Fatalf("stale fetch error still shown over the validation error; texts=%q", h.ui.Texts())
		}
	})
}

// A call reaching a sync entry point while the session is busy changes no
// error state: without the busy guard, a busy-time Push on detached HEAD would
// write a validation error for an operation that never ran, and it would
// surface in the toolbar once the running operation completes.
func TestSyncClickWhileBusyLeavesErrorStateUntouched(t *testing.T) {
	repo := newGitRepo(t, "sync-busy-errors")
	head := repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")
	repo.Git("checkout", head) // detach

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	// Start a real Fetch held behind the worker gate; while it is busy, direct
	// entry-point calls must not write a validation error or start anything.
	h.queue.PauseWorkers()
	clickFetch(t, h)
	fetchWorker := h.queue.WaitWorker(t)

	st := syncStateFor(h.app)
	h.app.syncPush()
	if st.syncErr != "" {
		t.Fatalf("syncPush while busy wrote syncErr %q", st.syncErr)
	}
	h.app.syncPull()
	if st.syncErr != "" {
		t.Fatalf("syncPull while busy wrote syncErr %q", st.syncErr)
	}
	if got := gatedLen(h.queue); got != 0 {
		t.Fatalf("busy-time entry-point calls spawned %d extra worker(s), want 0", got)
	}

	// When the fetch completes successfully, no phantom validation error may
	// appear in the toolbar.
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(fetchWorker)
	h.drain()
	if h.app.session.Busy() {
		t.Fatal("session stayed busy after gated Fetch finished")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("gated fetch failed: %v", err)
	}
	h.ui.Frame()
	if h.ui.HasText("HEAD is detached") {
		t.Fatalf("phantom validation error surfaced after busy operation; texts=%q", h.ui.Texts())
	}
}

// While a sync operation runs, the toolbar exposes the busy status, all three
// sync buttons are disabled, and additional clicks cannot re-enter; when the
// worker finishes the toolbar returns to idle.
func TestSyncBusyDisablesButtonsAndRejectsReentry(t *testing.T) {
	repo := newGitRepo(t, "sync-busy")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	h.queue.PauseWorkers()
	if err := h.ui.ClickWith(ui.Alt, "Fetch"); err != nil {
		t.Fatalf("click Fetch: %v", err)
	}
	fetchWorker := h.queue.WaitWorker(t)
	h.ui.Frame()

	status, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Status")
	if !ok || !status.Busy {
		t.Fatalf("Repository Status = %+v (found=%v), want Busy=true", status, ok)
	}
	if !h.ui.HasText("Fetching…") {
		t.Fatalf("busy label not shown; texts=%q", h.ui.Texts())
	}
	for _, label := range []string{"Fetch", "Pull", "Push"} {
		btn, ok := h.ui.AccessByRole(ui.RoleButton, label)
		if !ok || !btn.Disabled {
			t.Fatalf("%s button while busy = %+v (found=%v), want Disabled=true", label, btn, ok)
		}
	}
	// A click reaching a sync action while busy must not re-enter: session.do
	// rejects it, so no second worker is spawned and the busy label is untouched.
	if err := h.ui.Click("Push"); err != nil {
		t.Logf("Push click while busy did not reach the control (%v); rejection is then trivially guaranteed", err)
	}
	h.ui.Frame()
	if h.app.session.BusyLabel() != "Fetching" {
		t.Fatalf("busy label = %q, want Fetching (re-entry would have replaced or duplicated it)", h.app.session.BusyLabel())
	}
	if got := gatedLen(h.queue); got != 0 {
		t.Fatalf("re-entry spawned %d extra worker(s), want 0", got)
	}

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(fetchWorker)
	h.drain()

	if h.app.session.Busy() {
		t.Fatal("session stayed busy after Fetch completed")
	}
	for _, label := range []string{"Fetch", "Pull", "Push"} {
		btn, ok := h.ui.AccessByRole(ui.RoleButton, label)
		if !ok || btn.Disabled {
			t.Fatalf("%s button after completion = %+v (found=%v), want enabled", label, btn, ok)
		}
	}
}

// A failing sync operation shows its redacted error in a dismissible toolbar
// error status, and starting the next operation clears the previous error.
func TestSyncErrorDisplayAndDismiss(t *testing.T) {
	repo := newGitRepo(t, "sync-error")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	// Break the remote URL so Fetch fails for real.
	repo.Git("remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	if err := h.ui.ClickWith(ui.Alt, "Fetch"); err != nil {
		t.Fatalf("click Fetch: %v", err)
	}
	h.drain()

	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error after failed Fetch; texts=%q", h.ui.Texts())
	}
	if h.app.session.Busy() {
		t.Fatal("session stayed busy after failed Fetch")
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("expected ActionErr after failed Fetch")
	}

	if err := h.ui.Click("Dismiss Error"); err != nil {
		t.Fatalf("click Dismiss Error: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); ok {
		t.Fatal("Repository Error still visible after dismiss")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("ActionErr not cleared: %v", err)
	}

	// Non-fast-forward Push rejection is also a visible, dismissible error.
	repo.Git("remote", "set-url", "origin", bareDir)
	peerDir := filepath.Join(t.TempDir(), "peer")
	runGitCommand(t, "", "clone", bareDir, peerDir)
	runGitCommand(t, peerDir, "config", "user.name", "Peer")
	runGitCommand(t, peerDir, "config", "user.email", "peer@branchline.local")
	if err := os.WriteFile(filepath.Join(peerDir, "p.txt"), []byte("p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, peerDir, "add", "--", "p.txt")
	runGitCommand(t, peerDir, "commit", "-m", "peer ahead")
	runGitCommand(t, peerDir, "push", "origin", "main")
	repo.CommitFile("local.txt", "l\n", "local ahead")

	if err := h.ui.ClickWith(ui.Alt, "Fetch"); err != nil {
		t.Fatalf("click Fetch to learn remote state: %v", err)
	}
	h.drain()
	clickPushConfirm(t, h)
	h.drain()
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("expected ActionErr after non-fast-forward Push")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatal("no Repository Error after rejected Push")
	}
	if err := h.ui.Click("Dismiss Error"); err != nil {
		t.Fatalf("click Dismiss Error: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); ok {
		t.Fatal("Repository Error still visible after dismiss of rejected Push")
	}
}

// Pull with conflicting uncommitted working-tree changes is refused by git:
// the toolbar shows a dismissible error, HEAD does not move, and the local
// modifications survive byte for byte.
func TestSyncPullConflictPreservesWorkingTree(t *testing.T) {
	repo := newGitRepo(t, "sync-conflict")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")

	// The peer advances a.txt on the remote.
	peerDir := filepath.Join(t.TempDir(), "peer")
	runGitCommand(t, "", "clone", bareDir, peerDir)
	runGitCommand(t, peerDir, "config", "user.name", "Peer")
	runGitCommand(t, peerDir, "config", "user.email", "peer@branchline.local")
	if err := os.WriteFile(filepath.Join(peerDir, "a.txt"), []byte("peer change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, peerDir, "add", "--", "a.txt")
	runGitCommand(t, peerDir, "commit", "-m", "peer edits a.txt")
	runGitCommand(t, peerDir, "push", "origin", "main")
	peerHead := strings.TrimSpace(runGitCommand(t, peerDir, "rev-parse", "HEAD"))

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	// Uncommitted local edit to the same file.
	localEdit := "local uncommitted work\x00with unique bytes\n"
	if err := os.WriteFile(filepath.Join(repo.Path(), "a.txt"), []byte(localEdit), 0o644); err != nil {
		t.Fatal(err)
	}
	headBefore := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	if err := h.ui.Click("Pull"); err != nil {
		t.Fatalf("click Pull: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("expected ActionErr after conflicting Pull")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatal("no Repository Error after conflicting Pull")
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != headBefore {
		t.Fatalf("HEAD moved during refused pull: got %q, want %q (peer head %q)", got, headBefore, peerHead)
	}
	data, err := os.ReadFile(filepath.Join(repo.Path(), "a.txt"))
	if err != nil {
		t.Fatalf("read working tree a.txt: %v", err)
	}
	if string(data) != localEdit {
		t.Fatalf("working tree a.txt = %q, want unmodified local edit %q", data, localEdit)
	}

	if err := h.ui.Click("Dismiss Error"); err != nil {
		t.Fatalf("click Dismiss Error: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); ok {
		t.Fatal("Repository Error still visible after dismiss")
	}
}

// Closing the askpass dialog turns every later question down immediately —
// both for workers that already queued one and for callers arriving after the
// close — so teardown can never leak a goroutine parked on a question nobody
// renders. The enqueued case drives a real worker through a gated Fetch over
// a password-gated loopback connection.
func TestSyncAskPassCloseReleasesAndRejectsWaitLoopbackSSH(t *testing.T) {
	// A question enqueued before the close is answered with cancel.
	_, h, _ := openPasswordGatedRepo(t, "hunter2t0ps3cret")
	clickFetch(t, h)
	h.ui.Frame()

	// Wait for the worker to park inside the dialog before closing it, so the
	// test deterministically exercises the wake-the-parked-worker path
	// instead of racing the question's arrival.
	waitAskPass(t, h)

	st := syncStateFor(h.app)
	st.pass.close()
	h.ui.Frame()

	// The worker parked in wait wakes through the queue drain without anyone
	// answering its dialog, and the fetch ends in an error without hanging.
	h.drain()
	if h.app.session.Busy() {
		t.Fatal("session stayed busy after closing the dialog under a parked worker")
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("expected ActionErr for the fetch whose question was closed")
	}

	// A wait call arriving after the close returns cancel at once.
	type result struct {
		answer string
		ok     bool
	}
	done := make(chan result, 1)
	go func() {
		answer, ok := st.pass.wait(h.app, "password for latehost:")
		done <- result{answer, ok}
	}()
	select {
	case r := <-done:
		if r.ok || r.answer != "" {
			t.Fatalf("wait after close = (%q, %v), want (\"\", false)", r.answer, r.ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait after close blocked instead of returning cancel immediately")
	}
}

// A test that fails early with a worker still queued behind the gate must not
// leak the askpass script directory. The worker's first git command is a
// non-asking one (git prefixes fetch with -c flags), so it creates the script
// directory lazily only once the drain releases it — exactly when a teardown
// that removes the directory before draining has already finished. Verified
// against the real failure mode: with the directory removed first, every
// iteration leaks one fresh branchline-askpass* directory.
func TestSyncTeardownPausedWorkerLeavesNoAskPassDir(t *testing.T) {
	before := snapshotDirs(t, "branchline-askpass*")
	for i := 0; i < 3; i++ {
		t.Run("iteration", func(t *testing.T) {
			repo := newGitRepo(t, "sync-teardown-paused")
			repo.CommitFile("a.txt", "one\n", "initial commit")
			repo.AddBareRemote("origin")

			h := newTestApp(t)
			closeSync(t, h)
			h.openRepo(repo.StoreRepo())
			h.drain()

			// Queue the fetch behind the worker gate and end the test body
			// without releasing it — the early-exit shape of a test failing
			// right after starting a sync. The worker only runs (and lazily
			// creates the askpass script directory) once the teardown drain
			// releases it.
			h.queue.PauseWorkers()
			clickFetch(t, h)
			h.queue.WaitWorker(t)
		})
	}

	// Each subtest's teardownSync drained its worker and removed the askpass
	// script directory synchronously before t.Run returned.
	if fresh := diffDirs(before, snapshotDirs(t, "branchline-askpass*")); len(fresh) > 0 {
		t.Fatalf("teardown leaked %d fresh branchline-askpass* directories: %v", len(fresh), fresh)
	}
}

// snapshotDirs returns the set of paths in the system temp directory matching
// the glob pattern, for leak comparison before and after a test.
func snapshotDirs(t *testing.T, pattern string) map[string]bool {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), pattern))
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	out := map[string]bool{}
	for _, m := range matches {
		out[m] = true
	}
	return out
}

// diffDirs returns the paths present in after but not in before.
func diffDirs(before, after map[string]bool) []string {
	var out []string
	for d := range after {
		if !before[d] {
			out = append(out, d)
		}
	}
	return out
}

// A test that fails early with a worker in flight inside the askpass question
// must not race the script directory removal: the worker's commands can still
// write the lazily created a.dir while Close reads it without a lock, so the
// removal has to wait for the drain. Under -race this test is the regression
// net for that ordering; correctness shows in the teardown not hanging.
func TestSyncTeardownInFlightWorkerNoRaceLoopbackSSH(t *testing.T) {
	var dir string
	t.Run("early exit", func(t *testing.T) {
		_, h, _ := openPasswordGatedRepo(t, "hunter2t0ps3cret")
		st := syncStateFor(h.app)

		// Drive a real worker into the askpass question of a gated fetch and
		// leave it parked there: the test body ends without answering, without
		// cancelling, and without draining — exactly what a failure halfway
		// through an askpass test looks like.
		clickFetch(t, h)
		h.ui.Frame()
		waitAskPass(t, h)

		st.mu.Lock()
		inflight := st.inflight
		st.mu.Unlock()
		if inflight != 1 {
			t.Fatalf("in-flight sync workers = %d, want 1", inflight)
		}
		dir = st.askDirForTest()
	})

	// The cleanups woke the parked worker with cancel, drained it, and removed
	// the script directory it was still using when the close started.
	if dir == "" {
		t.Fatal("in-flight worker never created the askpass script directory")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("askpass script directory %s leaked by teardown: stat err = %v", dir, err)
	}
}

// A worker still inside AskPass.Env when the test ends early must not race
// the teardown's script directory removal: Env writes the lazily created
// a.dir while Close reads it without a lock. This is the reviewer's race
// replay — with the removal running before the drain, -race reports the
// pairing on every round; with the drain first, it stays clean. The worker
// is spawned bare (not through a sync entry point) because the race window
// lives inside Env itself, independent of what command would use it.
func TestSyncTeardownEnvWorkerNoRace(t *testing.T) {
	for i := 0; i < 20; i++ {
		t.Run("round", func(t *testing.T) {
			repo := newGitRepo(t, "sync-teardown-race")
			repo.CommitFile("a.txt", "one\n", "initial commit")
			repo.AddBareRemote("origin")

			h := newTestApp(t)
			openSyncRepo(t, h, repo.StoreRepo())

			st := syncStateFor(h.app)
			h.app.spawn(func() {
				_, _ = st.ask.Env()
			})
			// The test body ends here without waiting — the early-exit shape of
			// a failing test. Only the cleanups run after this point, and the
			// drain inside closeSync's single cleanup is what keeps the worker's
			// Env write ordered before the removal.
		})
	}
}

// closeSyncUI with no worker in flight removes the askpass script directory
// synchronously, stays inert on repetition, and leaves syncStateFor returning
// the closed sentinel instead of building an orphaned replacement state.
func TestSyncCloseUINoInflightRemovesAskPass(t *testing.T) {
	repo := newGitRepo(t, "sync-close-idle")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	st := syncStateFor(h.app)
	if _, err := st.ask.Env(); err != nil {
		t.Fatalf("create askpass script: %v", err)
	}
	dir := st.askDirForTest()
	if dir == "" {
		t.Fatal("askpass script directory was not created")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("askpass script directory missing before close: %v", err)
	}

	h.app.closeSyncUI()

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("askpass script directory after closeSyncUI err = %v, want not-exist", err)
	}
	// A post-close lookup must not rebuild a live orphaned state: what it
	// returns carries no live askpass and no fresh dialog.
	post := syncStateFor(h.app)
	if post != nil && post.ask != nil {
		t.Fatalf("syncStateFor after close revived a live state: ask = %p, want nil", post.ask)
	}

	// Repetition is a no-op: closing again neither errors nor resurrects the
	// state, and the directory stays gone.
	h.app.closeSyncUI()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("askpass script directory after repeated closeSyncUI err = %v, want not-exist", err)
	}
	if post := syncStateFor(h.app); post != nil && post.ask != nil {
		t.Fatalf("syncStateFor after repeated close revived a live state: ask = %p, want nil", post.ask)
	}

	// Closing an app before its first syncStateFor lookup still writes the nil
	// sentinel so a subsequent lookup never allocates an orphaned live AskPass.
	fresh := newApp(h.store)
	fresh.closeSyncUI()
	if post := syncStateFor(fresh); post != nil && post.ask != nil {
		t.Fatalf("syncStateFor after pre-init closeSyncUI revived a live state: ask = %p, want nil", post.ask)
	}
}

// closeSyncUI with an in-flight worker defers removing the askpass script
// directory until the worker finishes (deterministically verified behind
// PauseWorkers), wakes a worker parked on a real loopback askpass question
// with cancel, and leaves the directory gone once the worker drains — all
// without a data race under -race.
func TestSyncCloseUIInflightWorkerRemovesAskPassAfterDoneLoopbackSSH(t *testing.T) {
	t.Run("gated worker keeps askpass dir until completion", func(t *testing.T) {
		repo := newGitRepo(t, "sync-close-gated")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.AddBareRemote("origin")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		st := syncStateFor(h.app)
		if _, err := st.ask.Env(); err != nil {
			t.Fatalf("create askpass script: %v", err)
		}
		dir := st.askDirForTest()
		if dir == "" {
			t.Fatal("askpass script directory was not created")
		}

		// Hold the sync worker behind the gate so it stays deterministically
		// in flight across closeSyncUI.
		h.queue.PauseWorkers()
		clickFetch(t, h)
		fetchWorker := h.queue.WaitWorker(t)

		st.mu.Lock()
		inflight := st.inflight
		st.mu.Unlock()
		if inflight != 1 {
			t.Fatalf("in-flight sync workers before close = %d, want 1", inflight)
		}

		h.app.closeSyncUI()

		// Because the worker is paused in flight, closeSyncUI must leave the
		// script directory intact and defer removal to syncWorkerDone.
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("askpass script directory removed while a worker was in flight: %v", err)
		}
		st.mu.Lock()
		closing, askClosed, inflight := st.closing, st.askClosed, st.inflight
		st.mu.Unlock()
		if !closing || askClosed || inflight != 1 {
			t.Fatalf("state while worker paused = (closing=%v, askClosed=%v, inflight=%d), want (true, false, 1)", closing, askClosed, inflight)
		}

		h.queue.ResumeWorkers()
		h.queue.ReleaseWorker(fetchWorker)
		h.drain()

		// By the time h.drain returns, the worker's defer syncWorkerDone has
		// already completed synchronously and removed the directory.
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("askpass script directory after gated worker finished err = %v, want not-exist", err)
		}
		st.mu.Lock()
		askClosed, inflight = st.askClosed, st.inflight
		st.mu.Unlock()
		if !askClosed || inflight != 0 {
			t.Fatalf("state after drain = (askClosed=%v, inflight=%d), want (true, 0)", askClosed, inflight)
		}

		// Repetition stays a no-op and no live state is rebuilt.
		h.app.closeSyncUI()
		if post := syncStateFor(h.app); post != nil && post.ask != nil {
			t.Fatalf("syncStateFor after close revived a live state: ask = %p, want nil", post.ask)
		}
	})

	t.Run("parked askpass worker wakes and removes askpass dir", func(t *testing.T) {
		_, h, _ := openPasswordGatedRepo(t, "hunter2t0ps3cret")
		st := syncStateFor(h.app)

		clickFetch(t, h)
		h.ui.Frame()
		waitAskPass(t, h)

		// The worker is parked on the password question; its ssh already created
		// the askpass script directory lazily.
		st.mu.Lock()
		inflight := st.inflight
		st.mu.Unlock()
		if inflight != 1 {
			t.Fatalf("in-flight sync workers before close = %d, want 1", inflight)
		}
		dir := st.askDirForTest()
		if dir == "" {
			t.Fatal("askpass script directory was not created by the in-flight worker")
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("askpass script directory missing before close: %v", err)
		}

		// closeSyncUI wakes the parked worker with cancel through the dialog
		// close, lets the fetch end in an error, and removes the directory
		// synchronously inside the worker's defer syncWorkerDone.
		h.app.closeSyncUI()
		h.drain()

		if h.app.session.Busy() {
			t.Fatal("session stayed busy after closeSyncUI under a parked worker")
		}
		if err := h.app.session.ActionErr(); err == nil {
			t.Fatal("expected ActionErr for the fetch whose question was closed")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("askpass script directory still present after worker finished: %s (err=%v)", dir, err)
		}

		st.mu.Lock()
		closed := st.askClosed
		inflight = st.inflight
		st.mu.Unlock()
		if !closed {
			t.Fatal("askClosed not set after the in-flight worker finished")
		}
		if inflight != 0 {
			t.Fatalf("in-flight sync workers after drain = %d, want 0", inflight)
		}

		// Repetition stays a no-op and no live state is rebuilt.
		h.app.closeSyncUI()
		if post := syncStateFor(h.app); post != nil && post.ask != nil {
			t.Fatalf("syncStateFor after close revived a live state: ask = %p, want nil", post.ask)
		}
	})
}

// A branch with a Gone upstream publishes to the default remote with fresh
// upstream tracking, even when the default remote is not named origin.
func TestSyncPushGoneUpstreamNonOriginRemote(t *testing.T) {
	repo := newGitRepo(t, "sync-gone-upstream")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("upstream-remote")

	// Track a ref of the remote, then delete that ref in the bare repository
	// and prune the stale tracking ref so the upstream reads as gone.
	repo.Git("push", "-u", "upstream-remote", "main")
	runGitCommand(t, bareDir, "update-ref", "-d", "refs/heads/main")
	repo.Git("fetch", "--prune", "upstream-remote")
	if out := repo.Git("for-each-ref", "--format=%(upstream:track)", "refs/heads/main"); !strings.Contains(out, "gone") {
		t.Fatalf("upstream not reported gone, upstream:track = %q", out)
	}

	repo.Git("checkout", "-b", "republish")
	head := repo.CommitFile("r.txt", "r\n", "republish commit")
	// Carry the gone upstream onto the new branch. git branch
	// --set-upstream-to refuses a tracking ref that no longer exists, so the
	// configuration is written the way a pre-deletion tracking setup leaves
	// it behind.
	repo.Git("config", "branch.republish.remote", "upstream-remote")
	repo.Git("config", "branch.republish.merge", "refs/heads/main")
	if out := repo.Git("for-each-ref", "--format=%(upstream) %(upstream:track)", "refs/heads/republish"); !strings.Contains(out, "gone") {
		t.Fatalf("republish upstream not reported gone, upstream:track = %q", out)
	}

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	clickPushConfirm(t, h)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("push of branch with gone upstream failed: %v", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/republish")); got != head {
		t.Fatalf("bare refs/heads/republish = %q, want %q", got, head)
	}
	if got := strings.TrimSpace(repo.Git("config", "branch.republish.remote")); got != "upstream-remote" {
		t.Fatalf("branch.republish.remote = %q, want upstream-remote", got)
	}
	if got := strings.TrimSpace(repo.Git("config", "branch.republish.merge")); got != "refs/heads/republish" {
		t.Fatalf("branch.republish.merge = %q, want refs/heads/republish", got)
	}
}

// Toolbar errors pass through git.RedactText: credentials embedded in a
// remote URL never reach the error strip.
func TestSyncErrorRedactsURLCredentials(t *testing.T) {
	repo := newGitRepo(t, "sync-redact")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.Git("remote", "add", "origin", "https://syncuser:syncsecretpw@127.0.0.1:9/repo.git")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	clickFetch(t, h)
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("expected ActionErr after fetch against unreachable host")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatal("no Repository Error after failed fetch")
	}
	for _, text := range h.ui.Texts() {
		if strings.Contains(text, "syncsecretpw") {
			t.Fatalf("toolbar error leaks URL credential: %q", text)
		}
	}
}

// Without an open repository the sync buttons are disabled and no Repository
// Error or busy status shows.
func TestSyncButtonsDisabledWithoutRepo(t *testing.T) {
	h := newTestApp(t)
	closeSync(t, h)
	h.ui.Frame()

	for _, label := range []string{"Fetch", "Pull", "Push"} {
		btn, ok := h.ui.AccessByRole(ui.RoleButton, label)
		if !ok {
			t.Fatalf("%s button missing without repository", label)
		}
		if !btn.Disabled {
			t.Fatalf("%s button without repository = %+v, want Disabled=true", label, btn)
		}
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Status"); ok {
		t.Fatal("Repository Status visible without repository")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); ok {
		t.Fatal("Repository Error visible without repository")
	}
}

// clickPush clicks the toolbar Push button and fails the test on error.
func clickPush(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Push"); err != nil {
		t.Fatalf("click Push: %v", err)
	}
}

// clickFetch clicks the toolbar Fetch button with Option held — the direct
// fetch a plain click ran before the Fetch dialog — and fails the test on
// error.
func clickFetch(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.ClickWith(ui.Alt, "Fetch"); err != nil {
		t.Fatalf("click Fetch: %v", err)
	}
}

// clickPull clicks the toolbar Pull button and fails the test on error.
func clickPull(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Pull"); err != nil {
		t.Fatalf("click Pull: %v", err)
	}
}

// Over a loopback SSH repository, toolbar Push publishes a server-side branch
// without upstream and sets upstream, Fetch updates the remote-tracking ref
// without moving the server's HEAD, and Pull fast-forwards the server's
// working tree — all verified on the server through the disposable sshd.
func TestSyncFetchPullPushLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	// Wire the bare repository as origin of the server's working repo.
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repoDir, "push", "-u", "origin", "main")

	h := newTestApp(t, repo)
	closeSync(t, h)
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click(repo.Name); err != nil {
		t.Fatalf("select SSH repo %q: %v", repo.Name, err)
	}
	h.ui.Frame()
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	if !h.app.session.Loaded() {
		t.Fatal("SSH snapshot did not load")
	}

	// Create a branch without upstream on the server, then publish it with Push.
	srv.RunGit(t, repoDir, "checkout", "-b", "feature")
	srv.WriteFile(t, repoDir, "feature.txt", []byte("feature\n"))
	srv.RunGit(t, repoDir, "add", "--", "feature.txt")
	srv.RunGit(t, repoDir, "commit", "-m", "feature commit")
	featureHead := srv.RunGit(t, repoDir, "rev-parse", "HEAD")

	// The server-side checkout and commit happened behind the snapshot's
	// back: reload so the Push dialog sees the new branch.
	h.app.session.Reload()
	h.drain()
	clickPushConfirm(t, h)
	h.drain()

	if got := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/feature"); got != featureHead {
		t.Fatalf("bare refs/heads/feature = %q, want %q", got, featureHead)
	}
	if got := srv.RunGit(t, repoDir, "config", "branch.feature.remote"); got != "origin" {
		t.Fatalf("server branch.feature.remote = %q, want origin", got)
	}
	if got := srv.RunGit(t, repoDir, "config", "branch.feature.merge"); got != "refs/heads/feature" {
		t.Fatalf("server branch.feature.merge = %q, want refs/heads/feature", got)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected action error after SSH push: %v", err)
	}

	// A peer advances origin/feature; Fetch moves the remote-tracking ref only.
	peerDir := srv.TempDir(t, "peer")
	srv.RunShell(t, "set -eu\nexport GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1\n"+
		"git clone -q "+shellQ(bareDir)+" "+shellQ(peerDir)+"\n"+
		"cd "+shellQ(peerDir)+"\n"+
		"git config user.name Peer\ngit config user.email peer@branchline.invalid\n"+
		"git checkout -q feature\n"+
		"printf 'peer\\n' > peer.txt\ngit add -- peer.txt\ngit commit -q -m 'peer commit'\n"+
		"git push -q origin feature\n")
	peerHead := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/feature")

	headBefore := srv.RunGit(t, repoDir, "rev-parse", "HEAD")
	clickFetch(t, h)
	h.drain()

	if got := srv.RunGit(t, repoDir, "rev-parse", "origin/feature"); got != peerHead {
		t.Fatalf("after SSH Fetch origin/feature = %q, want %q", got, peerHead)
	}
	if got := srv.RunGit(t, repoDir, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("SSH Fetch moved server HEAD: got %q, want unchanged %q", got, headBefore)
	}

	// Pull fast-forwards the server's HEAD and working tree.
	clickPull(t, h)
	h.drain()

	if got := srv.RunGit(t, repoDir, "rev-parse", "HEAD"); got != peerHead {
		t.Fatalf("after SSH Pull server HEAD = %q, want %q", got, peerHead)
	}
	if data := srv.ReadFile(t, repoDir, "peer.txt"); string(data) != "peer\n" {
		t.Fatalf("server working tree peer.txt = %q, want peer\\n", data)
	}
	if snap := h.app.session.Snapshot(); snap.Status.Head != peerHead {
		t.Fatalf("snapshot HEAD after SSH pull = %q, want %q", snap.Status.Head, peerHead)
	}
}

// selectRepo opens repo through the Repository Switcher and drains the
// initial load attempt (which may itself fail, e.g. for an encrypted key).
func selectRepo(t *testing.T, h *testHarness, repo store.Repo) {
	t.Helper()
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click(repo.Name); err != nil {
		t.Fatalf("select repo %q: %v", repo.Name, err)
	}
	h.ui.Frame()
	h.drain()
}

// waitAskPass consumes the wake-up callback posted by passDialog.wait and
// renders the modal, asserting it is visible.
func waitAskPass(t *testing.T, h *testHarness) {
	t.Helper()
	h.queue.RunNext(t)
	h.ui.Frame()
	if _, ok := h.ui.Find("Cancel"); !ok {
		t.Fatalf("askpass dialog did not render; texts=%q", h.ui.Texts())
	}
}

// An encrypted SSH identity key makes the initial promptless snapshot load
// fail; clicking Fetch then raises the real OpenSSH askpass flow, the
// "Passphrase for SSH key" modal accepts the passphrase, fetch succeeds, and
// the following snapshot reload reuses the established ControlMaster without
// asking again.
func TestSyncAskPassKeyPassphraseLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repo.Identity = srv.GenerateKey(t, "askpass-enc", "s3cret")
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repoDir, "push", "-u", "origin", "main")

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err == nil {
		t.Fatal("expected initial load to fail with the encrypted key locked")
	}

	// A peer advances origin/main so the fetch has an observable effect.
	peerDir := srv.TempDir(t, "peer")
	srv.RunShell(t, "set -eu\nexport GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1\n"+
		"git clone -q "+shellQ(bareDir)+" "+shellQ(peerDir)+"\n"+
		"cd "+shellQ(peerDir)+"\n"+
		"git config user.name Peer\ngit config user.email peer@branchline.invalid\n"+
		"printf 'peer\\n' > peer.txt\ngit add -- peer.txt\ngit commit -q -m 'peer commit'\n"+
		"git push -q origin main\n")
	peerHead := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/main")

	clickFetch(t, h)
	h.ui.Frame()

	// The worker blocks inside ssh waiting for the passphrase; the wake-up
	// callback renders the modal.
	waitAskPass(t, h)
	if !h.ui.HasText("Passphrase for SSH key") {
		t.Fatalf("passphrase dialog title missing; texts=%q", h.ui.Texts())
	}
	// The note names the key ssh asked about; long paths render truncated in
	// the fixed-width dialog, so assert the legible prefix.
	if !h.ui.HasText("Enter passphrase for SSH key '") {
		t.Fatalf("passphrase dialog key note missing; texts=%q", h.ui.Texts())
	}
	h.ui.Type("s3cret")
	h.ui.Frame()
	if err := h.ui.Click("OK"); err != nil {
		t.Fatalf("click OK in passphrase dialog: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("fetch with answered passphrase failed: %v", err)
	}
	if got := srv.RunGit(t, repoDir, "rev-parse", "origin/main"); got != peerHead {
		t.Fatalf("after askpass fetch origin/main = %q, want %q", got, peerHead)
	}
	// The automatic post-write Reload reuses the ControlMaster: the snapshot
	// loads without another question.
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("post-fetch reload failed: %v", err)
	}
	if !h.app.session.Loaded() {
		t.Fatal("snapshot did not load after askpass-unlocked fetch")
	}

	// A second fetch over the established master connection asks nothing.
	clickFetch(t, h)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("second fetch failed: %v", err)
	}
	if h.ui.HasText("Passphrase for SSH key") {
		t.Fatal("passphrase dialog reappeared over established ControlMaster")
	}
}

// Cancelling the passphrase dialog — by the Cancel button or by Escape — ends
// the operation with an error immediately: the worker never hangs, busy state
// clears, and the dismissible error appears. The two cancel paths run as
// independent top-level tests so that a failing one cannot masquerade as the
// other and loopback sshd environments stay small.
func testSyncAskPassCancelLoopbackSSH(t *testing.T, cancel func(t *testing.T, h *testHarness)) {
	t.Helper()
	repo, srv := loopbackSSHRepo(t)
	repo.Identity = srv.GenerateKey(t, "askpass-cancel", "s3cret")

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)

	clickFetch(t, h)
	h.ui.Frame()
	cancel(t, h)
	h.drain()

	if h.app.session.Busy() {
		t.Fatal("session stayed busy after cancelling the passphrase dialog")
	}
	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("expected ActionErr after cancelled fetch")
	}
	if !strings.Contains(err.Error(), "Permission denied") && !strings.Contains(err.Error(), "cannot connect") {
		t.Fatalf("cancelled fetch error = %q, want a connection failure", err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error after cancel; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Dismiss Error"); err != nil {
		t.Fatalf("click Dismiss Error: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); ok {
		t.Fatal("Repository Error still visible after dismiss")
	}

	// A cancel must not poison the connection: retrying with the correct
	// passphrase succeeds. This healthy connection also lets the loopback
	// sshd reap the cancelled session in the ordinary way before teardown.
	clickFetch(t, h)
	h.ui.Frame()
	waitAskPass(t, h)
	h.ui.Type("s3cret")
	h.ui.Frame()
	if err := h.ui.Click("OK"); err != nil {
		t.Fatalf("click OK in retry passphrase dialog: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("retry fetch after cancel failed: %v", err)
	}
}

// The Cancel button answers the askpass question with cancel and the fetch
// ends in an error without hanging.
func TestSyncAskPassCancelButtonLoopbackSSH(t *testing.T) {
	testSyncAskPassCancelLoopbackSSH(t, func(t *testing.T, h *testHarness) {
		waitAskPass(t, h)
		if err := h.ui.Click("Cancel"); err != nil {
			t.Fatalf("click Cancel in askpass dialog: %v", err)
		}
		h.ui.Frame()
	})
}

// Pressing Escape dismisses the askpass modal, which answers the question
// with cancel and ends the fetch in an error without hanging.
func TestSyncAskPassCancelEscapeLoopbackSSH(t *testing.T) {
	testSyncAskPassCancelLoopbackSSH(t, func(t *testing.T, h *testHarness) {
		waitAskPass(t, h)
		h.ui.Key(0, ui.KeyEscape)
		h.ui.Frame()
	})
}

// Pressing Enter in the askpass input submits the answer like the OK button:
// the password reaches the gate through the pipe and the fetch proceeds.
func TestSyncAskPassEnterSubmitsPasswordLoopbackSSH(t *testing.T) {
	const password = "hunter2t0ps3cret"
	repo, h, srv := openPasswordGatedRepo(t, password)

	// A peer advances origin/main so the fetch has an observable effect.
	peerDir := srv.TempDir(t, "peer")
	bareDir := srv.RunGit(t, repo.Path, "remote", "get-url", "origin")
	srv.RunShell(t, "set -eu\nexport GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1\n"+
		"git clone -q "+shellQ(bareDir)+" "+shellQ(peerDir)+"\n"+
		"cd "+shellQ(peerDir)+"\n"+
		"git config user.name Peer\ngit config user.email peer@branchline.invalid\n"+
		"printf 'peer\\n' > peer.txt\ngit add -- peer.txt\ngit commit -q -m 'peer commit'\n"+
		"git push -q origin main\n")
	peerHead := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/main")

	clickFetch(t, h)
	h.ui.Frame()
	waitAskPass(t, h)
	h.ui.Type(password)
	h.ui.Frame()
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("fetch with Enter-submitted password failed: %v", err)
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "origin/main"); got != peerHead {
		t.Fatalf("server origin/main after Enter-submitted fetch = %q, want %q", got, peerHead)
	}
}

// Clicking the modal backdrop dismisses the dialog like Escape: the question
// is answered with cancel and the fetch ends in an error without hanging.
func TestSyncAskPassBackdropClickCancelsLoopbackSSH(t *testing.T) {
	_, h, _ := openPasswordGatedRepo(t, "hunter2t0ps3cret")

	clickFetch(t, h)
	h.ui.Frame()
	waitAskPass(t, h)
	// The modal is centered in the 1200x800 test window; its top-left corner
	// is well inside the dimmed backdrop, outside the 440-wide dialog.
	h.ui.ClickAt(20, 20)
	h.ui.Frame()
	h.drain()

	if h.app.session.Busy() {
		t.Fatal("session stayed busy after backdrop-dismissed password dialog")
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("expected ActionErr after backdrop-cancelled password-gated fetch")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error after backdrop cancel; texts=%q", h.ui.Texts())
	}
}

// shellQ single-quotes s for the remote POSIX shell.
func shellQ(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// passwordGateWrapper builds an ssh wrapper script standing in for a
// password-authenticating server, which the loopback sshd (publickey-only)
// cannot be. It mimics OpenSSH password authentication semantics: invocations
// that cannot ask (BatchMode, no askpass script in the environment) fail
// immediately with Permission denied, while invocations with an askpass
// script in their environment are asked the password once through the real
// askpass pipe protocol and proceed only when it matches want.
func passwordGateWrapper(t *testing.T, srvSSH, want string) string {
	t.Helper()
	// The success branch removes the FIFO explicitly before exec: replacing
	// the process image never runs the EXIT trap, so relying on it alone
	// leaked one branchline-ap.* pipe per invocation.
	script := `#!/bin/sh
case "$SSH_ASKPASS_REQUIRE" in
force) : interactive ;;
*) echo "Permission denied (publickey,password)." >&2; exit 255 ;;
esac
r=$(mktemp -u "${TMPDIR:-/tmp}/branchline-ap.XXXXXXXX")
mkfifo -m 600 "$r" || exit 1
trap 'rm -f "$r"' EXIT
trap 'exit 1' INT TERM HUP
printf '\036BRANCHLINE-ASKPASS L %s %s\n' "$r" "password for gatehost:" >&2
IFS= read -r a < "$r" || exit 1
case "$a" in
+` + want + `) rm -f "$r"; exec ` + shellQ(srvSSH) + ` "$@" ;;
*) echo "Permission denied, please try again." >&2; exit 255 ;;
esac
`
	bin := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write password gate wrapper: %v", err)
	}
	return filepath.Dir(bin)
}

// openPasswordGatedRepo builds a loopback repository behind the password gate
// and opens it in a fresh app. The gate fails promptless ssh invocations
// immediately, so the initial snapshot load ends in an error without ever
// asking — exactly like a password-only server. It returns the target as well
// so tests can verify server-side effects through the probe executor, which
// bypasses the gate.
func openPasswordGatedRepo(t *testing.T, password string) (store.Repo, *testHarness, *sshtest.Target) {
	t.Helper()
	repo, srv := loopbackSSHRepo(t)
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repo.Path, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repo.Path, "push", "-u", "origin", "main")

	gateDir := passwordGateWrapper(t, srv.SSHProgram(), password)
	t.Setenv("PATH", gateDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err == nil {
		t.Fatal("expected initial load to fail behind the password gate")
	}
	return repo, h, srv
}

// A password prompt — the "The remote host requires password" dialog — accepts
// the typed password through the same askpass pipe and lets the fetch proceed.
// The prompt is triggered by a wrapper ssh that gates the loopback target
// behind password authentication.
func TestSyncAskPassPasswordPromptLoopbackSSH(t *testing.T) {
	const password = "hunter2t0ps3cret"
	repo, h, srv := openPasswordGatedRepo(t, password)

	// A peer advances origin/main on the server so the fetch has an observable
	// server-side effect. Peer operations run server-side through RunShell and
	// never cross the gate.
	peerDir := srv.TempDir(t, "peer")
	bareDir := srv.RunGit(t, repo.Path, "remote", "get-url", "origin")
	srv.RunShell(t, "set -eu\nexport GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1\n"+
		"git clone -q "+shellQ(bareDir)+" "+shellQ(peerDir)+"\n"+
		"cd "+shellQ(peerDir)+"\n"+
		"git config user.name Peer\ngit config user.email peer@branchline.invalid\n"+
		"printf 'peer\\n' > peer.txt\ngit add -- peer.txt\ngit commit -q -m 'peer commit'\n"+
		"git push -q origin main\n")
	peerHead := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/main")

	clickFetch(t, h)
	h.ui.Frame()
	waitAskPass(t, h)
	if !h.ui.HasText("The remote host requires password") {
		t.Fatalf("password dialog title missing for fetch; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("password for gatehost:") {
		t.Fatalf("password prompt text missing; texts=%q", h.ui.Texts())
	}
	h.ui.Type(password)
	h.ui.Frame()
	if err := h.ui.Click("OK"); err != nil {
		t.Fatalf("click OK in password dialog: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("fetch behind password gate failed after correct password: %v", err)
	}
	if h.app.session.Busy() {
		t.Fatal("session stayed busy after password-unlocked fetch")
	}
	// Server-side proof: the fetch updated the server repository's
	// remote-tracking ref, read through the gate-independent probe executor.
	if got := srv.RunGit(t, repo.Path, "rev-parse", "origin/main"); got != peerHead {
		t.Fatalf("server origin/main after password-unlocked fetch = %q, want %q", got, peerHead)
	}
}

// Cancelling the password dialog ends the fetch in an error without hanging.
func TestSyncAskPassPasswordCancelLoopbackSSH(t *testing.T) {
	_, h, _ := openPasswordGatedRepo(t, "hunter2t0ps3cret")

	clickFetch(t, h)
	h.ui.Frame()
	waitAskPass(t, h)
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel in password dialog: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if h.app.session.Busy() {
		t.Fatal("session stayed busy after cancelling the password dialog")
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("expected ActionErr after cancelled password-gated fetch")
	}
}

// Pull over loopback SSH with conflicting uncommitted changes in the server's
// working tree is refused by git: the error is dismissible, and neither the
// server's HEAD nor the uncommitted bytes are lost.
func TestSyncPullConflictPreservesWorkingTreeLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repoDir, "push", "-u", "origin", "main")

	// The peer advances a.txt on the remote.
	peerDir := srv.TempDir(t, "peer")
	srv.RunShell(t, "set -eu\nexport GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1\n"+
		"git clone -q "+shellQ(bareDir)+" "+shellQ(peerDir)+"\n"+
		"cd "+shellQ(peerDir)+"\n"+
		"git config user.name Peer\ngit config user.email peer@branchline.invalid\n"+
		"printf 'peer change\\n' > a.txt\ngit add -- a.txt\ngit commit -q -m 'peer edits a.txt'\n"+
		"git push -q origin main\n")

	h := newTestApp(t, repo)
	closeSync(t, h)
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click(repo.Name); err != nil {
		t.Fatalf("select SSH repo %q: %v", repo.Name, err)
	}
	h.ui.Frame()
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	// Uncommitted edit to the same file in the server's working tree.
	localEdit := []byte("server uncommitted work\x00unique bytes\n")
	srv.WriteFile(t, repoDir, "a.txt", localEdit)
	headBefore := srv.RunGit(t, repoDir, "rev-parse", "HEAD")

	clickPull(t, h)
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("expected ActionErr after conflicting SSH Pull")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatal("no Repository Error after conflicting SSH Pull")
	}
	if got := srv.RunGit(t, repoDir, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("server HEAD moved during refused pull: got %q, want %q", got, headBefore)
	}
	if data := srv.ReadFile(t, repoDir, "a.txt"); string(data) != string(localEdit) {
		t.Fatalf("server working tree a.txt = %q, want unmodified local edit %q", data, localEdit)
	}

	if err := h.ui.Click("Dismiss Error"); err != nil {
		t.Fatalf("click Dismiss Error: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); ok {
		t.Fatal("Repository Error still visible after dismiss")
	}
}

// On a configured Linux SSH host (sshlab), toolbar Push publishes a branch
// without upstream and sets upstream, Fetch updates the remote-tracking ref
// without moving the server's HEAD, and Pull fast-forwards the server's
// working tree — all verified on the Linux server itself.
func TestConfiguredLinuxSyncFetchPullPush(t *testing.T) {
	repo, srv := configuredLinuxRepo(t)
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repoDir, "push", "-u", "origin", "main")

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("Linux SSH snapshot load failed: %v", err)
	}
	if !h.app.session.Loaded() {
		t.Fatal("Linux SSH snapshot did not load")
	}

	// Publish a branch without upstream through Push.
	srv.RunGit(t, repoDir, "checkout", "-b", "feature")
	srv.WriteFile(t, repoDir, "feature.txt", []byte("feature\n"))
	srv.RunGit(t, repoDir, "add", "--", "feature.txt")
	srv.RunGit(t, repoDir, "commit", "-m", "feature commit")
	featureHead := srv.RunGit(t, repoDir, "rev-parse", "HEAD")

	// The server-side checkout and commit happened behind the snapshot's
	// back: reload so the Push dialog sees the new branch.
	h.app.session.Reload()
	h.drain()
	clickPushConfirm(t, h)
	h.drain()

	if got := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/feature"); got != featureHead {
		t.Fatalf("bare refs/heads/feature = %q, want %q", got, featureHead)
	}
	if got := srv.RunGit(t, repoDir, "config", "branch.feature.remote"); got != "origin" {
		t.Fatalf("server branch.feature.remote = %q, want origin", got)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected action error after Linux SSH push: %v", err)
	}

	// A peer advances origin/feature; Fetch moves only the remote-tracking ref.
	peerDir := srv.TempDir(t, "peer")
	srv.RunShell(t, "set -eu\nexport GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1\n"+
		"git clone -q "+shellQ(bareDir)+" "+shellQ(peerDir)+"\n"+
		"cd "+shellQ(peerDir)+"\n"+
		"git config user.name Peer\ngit config user.email peer@branchline.invalid\n"+
		"git checkout -q feature\n"+
		"printf 'peer\\n' > peer.txt\ngit add -- peer.txt\ngit commit -q -m 'peer commit'\n"+
		"git push -q origin feature\n")
	peerHead := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/feature")

	headBefore := srv.RunGit(t, repoDir, "rev-parse", "HEAD")
	clickFetch(t, h)
	h.drain()

	if got := srv.RunGit(t, repoDir, "rev-parse", "origin/feature"); got != peerHead {
		t.Fatalf("after Linux Fetch origin/feature = %q, want %q", got, peerHead)
	}
	if got := srv.RunGit(t, repoDir, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("Linux Fetch moved server HEAD: got %q, want unchanged %q", got, headBefore)
	}

	// Pull fast-forwards the server's HEAD and working tree.
	clickPull(t, h)
	h.drain()

	if got := srv.RunGit(t, repoDir, "rev-parse", "HEAD"); got != peerHead {
		t.Fatalf("after Linux Pull server HEAD = %q, want %q", got, peerHead)
	}
	if data := srv.ReadFile(t, repoDir, "peer.txt"); string(data) != "peer\n" {
		t.Fatalf("server working tree peer.txt = %q, want peer\\n", data)
	}
	if snap := h.app.session.Snapshot(); snap.Status.Head != peerHead {
		t.Fatalf("snapshot HEAD after Linux pull = %q, want %q", snap.Status.Head, peerHead)
	}
}
