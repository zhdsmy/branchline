package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// --- helpers ---

// recordingRunExecutor wraps an executor and remembers every command it ran,
// so a test can prove a close path never started git.
type recordingRunExecutor struct {
	real git.Executor

	mu   sync.Mutex
	cmds [][]string
}

func (e *recordingRunExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	e.mu.Lock()
	e.cmds = append(e.cmds, append([]string(nil), args...))
	e.mu.Unlock()
	return e.real.Run(ctx, stdin, args...)
}

func (e *recordingRunExecutor) Describe() string { return e.real.Describe() }

// count reports how many recorded commands started with the subcommand.
func (e *recordingRunExecutor) count(sub string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, args := range e.cmds {
		if len(args) > 0 && args[0] == sub {
			n++
		}
	}
	return n
}

// total reports how many commands were recorded at all.
func (e *recordingRunExecutor) total() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.cmds)
}

// installRecordingExecutor wraps the open repository's executor in a
// recording one, restoring it when the test ends.
func installRecordingExecutor(t *testing.T, h *testHarness) *recordingRunExecutor {
	t.Helper()
	repo := h.app.session.Repo()
	if repo == nil {
		t.Fatal("cannot install recording executor without an open repository")
	}
	wrapped := &recordingRunExecutor{real: repo.Exec}
	repo.Exec = wrapped
	t.Cleanup(func() {
		h.queue.releaseAllAndWait()
		repo.Exec = wrapped.real
	})
	return wrapped
}

// openToolbarFetchDialog clicks the toolbar Fetch button and asserts the
// Fetch dialog opened.
func openToolbarFetchDialog(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Fetch"); err != nil {
		t.Fatalf("click Fetch: %v", err)
	}
	h.ui.Frame()
	if !syncStateFor(h.app).fetchDlg.open {
		t.Fatalf("Fetch dialog did not open; texts=%q", h.ui.Texts())
	}
}

// clickFetchConfirm clicks the toolbar Fetch button and the dialog's confirm
// button — the real product path a plain Fetch click drives now. The caller
// drains.
func clickFetchConfirm(t *testing.T, h *testHarness) {
	t.Helper()
	openToolbarFetchDialog(t, h)
	if err := h.ui.Click("Confirm Fetch"); err != nil {
		t.Fatalf("click Confirm Fetch: %v\ntexts=%q", err, h.ui.Texts())
	}
}

// selectFetchRemote switches the dialog's remote to name through the real
// select control.
func selectFetchRemote(t *testing.T, h *testHarness, name string) {
	t.Helper()
	st := &syncStateFor(h.app).fetchDlg
	from, to := -1, -1
	for i, r := range st.remotes {
		if r == st.remote {
			from = i
		}
		if r == name {
			to = i
		}
	}
	if from < 0 || to < 0 {
		t.Fatalf("cannot switch remote %q -> %q in %v", st.remote, name, st.remotes)
	}
	driveSelect(t, h, "Fetch Remote", from, to)
	if got := syncStateFor(h.app).fetchDlg.remote; got != name {
		t.Fatalf("dialog remote = %q, want %q", got, name)
	}
}

// fetchTwoRemoteFixture builds a repository tracking origin/main with both
// origin and backup wired and fetched, then advances both remotes behind its
// back: origin by one commit, backup by another. It returns the peer hashes
// so a test can tell exactly which remote a fetch updated.
func fetchTwoRemoteFixture(t *testing.T, name string) (repo *gitRepoFixture, bareOrigin, bareBackup, originHead, backupHead string) {
	t.Helper()
	repo = newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareOrigin = repo.AddBareRemote("origin")
	bareBackup = addSecondBareRemote(t, repo, "backup")
	repo.Git("push", "-q", "backup", "main")
	repo.Git("fetch", "-q", "backup")
	originHead = pushPeerCommit(t, bareOrigin, "main", "o.txt", "o\n", "origin peer commit")
	backupHead = pushPeerCommit(t, bareBackup, "main", "b.txt", "b\n", "backup peer commit")
	return repo, bareOrigin, bareBackup, originHead, backupHead
}

// --- defaults ---

// The dialog's remote defaults to the remote of the current branch's
// upstream, ahead of origin, and the unchecked boxes follow the store's
// defaults: prune ticked, all remotes and tags unticked with a fresh store.
func TestFetchDialogDefaultsUpstreamRemote(t *testing.T) {
	repo := newGitRepo(t, "fetchdlg-def-upstream")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	addSecondBareRemote(t, repo, "origin")
	bareUpstream := addSecondBareRemote(t, repo, "upstream")
	repo.Git("push", "-q", "-u", "upstream", "main")
	if up := strings.TrimSpace(repo.Git("for-each-ref", "--format=%(upstream:short)", "refs/heads/main")); up != "upstream/main" {
		t.Fatalf("main upstream = %q, want upstream/main", up)
	}
	_ = bareUpstream

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarFetchDialog(t, h)
	st := &syncStateFor(h.app).fetchDlg
	if st.remote != "upstream" {
		t.Fatalf("default remote = %q, want upstream (the upstream's remote)", st.remote)
	}
	if st.all || !st.prune || st.tags {
		t.Fatalf("default boxes = (all=%v, prune=%v, tags=%v), want (false, true, false)", st.all, st.prune, st.tags)
	}
	if !h.ui.HasText("Fetch the latest changes from a remote.") {
		t.Fatalf("dialog description missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Fetch"); !ok || btn.Disabled {
		t.Fatalf("Confirm Fetch = %+v (found=%v), want enabled", btn, ok)
	}
	for label, want := range map[string]bool{
		"Fetch all remotes":              false,
		"Prune remote-tracking branches": true,
		"Fetch tags":                     false,
	} {
		box, ok := h.ui.AccessByRole(ui.RoleCheckBox, label)
		if !ok || box.Checked != want {
			t.Fatalf("%s box = %+v (found=%v), want checked=%v", label, box, ok, want)
		}
	}
}

// Without an upstream the remote defaults to origin when there is one, and
// to the first remote otherwise.
func TestFetchDialogDefaultsOriginAndFirst(t *testing.T) {
	t.Run("origin without upstream", func(t *testing.T) {
		repo := newGitRepo(t, "fetchdlg-def-origin")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		addSecondBareRemote(t, repo, "origin")
		addSecondBareRemote(t, repo, "backup")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		openToolbarFetchDialog(t, h)
		if got := syncStateFor(h.app).fetchDlg.remote; got != "origin" {
			t.Fatalf("default remote = %q, want origin", got)
		}
	})

	t.Run("first remote without origin", func(t *testing.T) {
		repo := newGitRepo(t, "fetchdlg-def-first")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		addSecondBareRemote(t, repo, "backup")
		addSecondBareRemote(t, repo, "mirror")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		openToolbarFetchDialog(t, h)
		st := &syncStateFor(h.app).fetchDlg
		if len(st.remotes) == 0 || st.remote != st.remotes[0] {
			t.Fatalf("default remote = %q, want the first remote of %v", st.remote, st.remotes)
		}
		if st.remote == "origin" {
			t.Fatal("default remote is origin although origin is not configured")
		}
	})
}

// Ticking Fetch all remotes disables the remote choice, and unticking it
// enables the choice again.
func TestFetchDialogAllRemotesDisablesRemoteChoice(t *testing.T) {
	repo := newGitRepo(t, "fetchdlg-all-disables")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarFetchDialog(t, h)
	sel, ok := h.ui.AccessByRole(ui.RolePopUpButton, "Fetch Remote")
	if !ok || sel.Disabled {
		t.Fatalf("Fetch Remote before ticking all = %+v (found=%v), want enabled", sel, ok)
	}
	tickBox(t, h, "Fetch all remotes")
	sel, ok = h.ui.AccessByRole(ui.RolePopUpButton, "Fetch Remote")
	if !ok || !sel.Disabled {
		t.Fatalf("Fetch Remote with all ticked = %+v (found=%v), want disabled", sel, ok)
	}
	tickBox(t, h, "Fetch all remotes")
	sel, ok = h.ui.AccessByRole(ui.RolePopUpButton, "Fetch Remote")
	if !ok || sel.Disabled {
		t.Fatalf("Fetch Remote after unticking all = %+v (found=%v), want enabled", sel, ok)
	}
}

// Choices remembered in the store fill the boxes, and Fetch tags — never
// stored — follows the FetchTags setting.
func TestFetchDialogDefaultsFromStore(t *testing.T) {
	t.Run("remembered choices", func(t *testing.T) {
		repo := newGitRepo(t, "fetchdlg-def-store")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.AddBareRemote("origin")

		h := newTestApp(t)
		if err := h.store.SetSheets(map[string]bool{
			store.FetchAllRemotes: true,
			store.FetchPrune:      false,
			store.FetchAllTags:    true,
		}); err != nil {
			t.Fatalf("seed sheets: %v", err)
		}
		openSyncRepo(t, h, repo.StoreRepo())

		openToolbarFetchDialog(t, h)
		st := &syncStateFor(h.app).fetchDlg
		if !st.all || st.prune || !st.tags {
			t.Fatalf("boxes from store = (all=%v, prune=%v, tags=%v), want (true, false, true)", st.all, st.prune, st.tags)
		}
	})

	t.Run("tags follow setting", func(t *testing.T) {
		repo := newGitRepo(t, "fetchdlg-def-tags")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.AddBareRemote("origin")

		h := newTestApp(t)
		settings := h.store.Settings()
		settings.FetchTags = true
		if err := h.store.SetSettings(settings); err != nil {
			t.Fatalf("set FetchTags setting: %v", err)
		}
		openSyncRepo(t, h, repo.StoreRepo())

		openToolbarFetchDialog(t, h)
		if !syncStateFor(h.app).fetchDlg.tags {
			t.Fatal("Fetch tags unticked although the FetchTags setting is on and nothing was remembered")
		}
	})
}

// --- real fetches through the dialog ---

// With one remote chosen the fetch updates only that remote's tracking
// branches; the other remote's stay behind.
func TestFetchDialogSingleRemoteFetchesOnlyIt(t *testing.T) {
	repo, _, _, originHead, backupHead := fetchTwoRemoteFixture(t, "fetchdlg-single")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarFetchDialog(t, h)
	selectFetchRemote(t, h, "backup")
	if err := h.ui.Click("Confirm Fetch"); err != nil {
		t.Fatalf("click Confirm Fetch: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("single-remote fetch failed: %v", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "refs/remotes/backup/main")); got != backupHead {
		t.Fatalf("backup/main = %q, want %q", got, backupHead)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "refs/remotes/origin/main")); got == originHead {
		t.Fatalf("origin/main moved to %q although only backup was fetched", got)
	}
}

// Fetch all remotes updates every remote's tracking branches.
func TestFetchDialogAllRemotesFetchesBoth(t *testing.T) {
	repo, _, _, originHead, backupHead := fetchTwoRemoteFixture(t, "fetchdlg-all")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarFetchDialog(t, h)
	tickBox(t, h, "Fetch all remotes")
	if err := h.ui.Click("Confirm Fetch"); err != nil {
		t.Fatalf("click Confirm Fetch: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("all-remotes fetch failed: %v", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "refs/remotes/origin/main")); got != originHead {
		t.Fatalf("origin/main = %q, want %q", got, originHead)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "refs/remotes/backup/main")); got != backupHead {
		t.Fatalf("backup/main = %q, want %q", got, backupHead)
	}
}

// fetchPruneFixture builds a repository whose origin carries a doomed branch
// the remote then loses: refs/remotes/origin/doomed exists locally and only
// a pruning fetch removes it.
func fetchPruneFixture(t *testing.T, name string) (*gitRepoFixture, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	repo.Git("push", "-q", "origin", "main:refs/heads/doomed")
	repo.Git("fetch", "-q", "origin")
	if _, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "refs/remotes/origin/doomed"); err != nil {
		t.Fatalf("refs/remotes/origin/doomed missing before the remote deletion: %v", err)
	}
	runGitCommand(t, bareDir, "update-ref", "-d", "refs/heads/doomed")
	return repo, bareDir
}

// A ticked Prune drops the tracking branches the remote lost; unticked it
// keeps them.
func TestFetchDialogPrune(t *testing.T) {
	t.Run("ticked prunes", func(t *testing.T) {
		repo, _ := fetchPruneFixture(t, "fetchdlg-prune-on")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		clickFetchConfirm(t, h)
		h.drain()

		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("pruning fetch failed: %v", err)
		}
		if _, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "refs/remotes/origin/doomed"); err == nil {
			t.Fatal("refs/remotes/origin/doomed survived a pruning fetch")
		}
	})

	t.Run("unticked keeps", func(t *testing.T) {
		repo, _ := fetchPruneFixture(t, "fetchdlg-prune-off")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		openToolbarFetchDialog(t, h)
		tickBox(t, h, "Prune remote-tracking branches")
		if err := h.ui.Click("Confirm Fetch"); err != nil {
			t.Fatalf("click Confirm Fetch: %v", err)
		}
		h.drain()

		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("non-pruning fetch failed: %v", err)
		}
		if _, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "refs/remotes/origin/doomed"); err != nil {
			t.Fatalf("refs/remotes/origin/doomed pruned although Prune was unticked: %v", err)
		}
	})
}

// fetchTagFixture builds a repository whose origin gains a tag the local
// repository cannot auto-follow: the tag points at a peer commit the local
// repository never fetched, pushed to the remote as the tag reference
// alone. A plain fetch — which auto-follows every tag pointing at objects
// already local, even when nothing else moves — leaves it remote; only a
// tag-fetching fetch brings it down.
func fetchTagFixture(t *testing.T, name string) (*gitRepoFixture, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	peerDir := filepath.Join(t.TempDir(), "peer")
	runGitCommand(t, "", "clone", bareDir, peerDir)
	runGitCommand(t, peerDir, "config", "user.name", "Branchline Peer")
	runGitCommand(t, peerDir, "config", "user.email", "peer@branchline.local")
	if err := os.WriteFile(filepath.Join(peerDir, "t.txt"), []byte("t\n"), 0o644); err != nil {
		t.Fatalf("write peer file: %v", err)
	}
	runGitCommand(t, peerDir, "add", "--", "t.txt")
	runGitCommand(t, peerDir, "commit", "-m", "peer tag target")
	tagTarget := strings.TrimSpace(runGitCommand(t, peerDir, "rev-parse", "HEAD"))
	runGitCommand(t, peerDir, "push", "origin", tagTarget+":refs/tags/vlate")
	if _, err := tryGitCommand(repo.Path(), "cat-file", "-e", tagTarget); err == nil {
		t.Fatal("tag target object already local: the fixture cannot discriminate --tags")
	}
	return repo, bareDir
}

// A ticked Fetch tags brings the tag down; unticked it stays remote.
func TestFetchDialogTags(t *testing.T) {
	t.Run("ticked fetches tags", func(t *testing.T) {
		repo, _ := fetchTagFixture(t, "fetchdlg-tags-on")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		openToolbarFetchDialog(t, h)
		tickBox(t, h, "Fetch tags")
		if err := h.ui.Click("Confirm Fetch"); err != nil {
			t.Fatalf("click Confirm Fetch: %v", err)
		}
		h.drain()

		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("tag fetch failed: %v", err)
		}
		if _, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "refs/tags/vlate"); err != nil {
			t.Fatalf("refs/tags/vlate missing after a fetch with Fetch tags ticked: %v", err)
		}
	})

	t.Run("unticked keeps tags away", func(t *testing.T) {
		repo, _ := fetchTagFixture(t, "fetchdlg-tags-off")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		clickFetchConfirm(t, h)
		h.drain()

		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("plain fetch failed: %v", err)
		}
		if _, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "refs/tags/vlate"); err == nil {
			t.Fatal("refs/tags/vlate came down although Fetch tags was unticked")
		}
	})
}

// --- remembering the check boxes ---

// The three boxes are remembered across submits: a reopened dialog starts
// from the choices of the last confirmed fetch, and Cancel saves nothing.
func TestFetchDialogRemembersChoices(t *testing.T) {
	repo := newGitRepo(t, "fetchdlg-remember")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarFetchDialog(t, h)
	tickBox(t, h, "Fetch all remotes")
	tickBox(t, h, "Prune remote-tracking branches")
	tickBox(t, h, "Fetch tags")
	if err := h.ui.Click("Confirm Fetch"); err != nil {
		t.Fatalf("click Confirm Fetch: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("remembered fetch failed: %v", err)
	}

	openToolbarFetchDialog(t, h)
	st := &syncStateFor(h.app).fetchDlg
	if !st.all || st.prune || !st.tags {
		t.Fatalf("reopened boxes = (all=%v, prune=%v, tags=%v), want (true, false, true)", st.all, st.prune, st.tags)
	}
	for key, want := range map[string]bool{
		store.FetchAllRemotes: true,
		store.FetchPrune:      false,
		store.FetchAllTags:    true,
	} {
		if got := h.store.Sheet(key, !want); got != want {
			t.Fatalf("stored %s = %v, want %v", key, got, want)
		}
	}

	// Cancel saves nothing: unticking tags and canceling leaves the
	// remembered choices — and the next dialog — untouched.
	tickBox(t, h, "Fetch tags")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	openToolbarFetchDialog(t, h)
	st = &syncStateFor(h.app).fetchDlg
	if !st.all || st.prune || !st.tags {
		t.Fatalf("boxes after cancel = (all=%v, prune=%v, tags=%v), want (true, false, true)", st.all, st.prune, st.tags)
	}
	if got := h.store.Sheet(store.FetchAllTags, false); !got {
		t.Fatal("a canceled untick of Fetch tags reached the store")
	}
}

// When the store write fails the fetch still runs and the failure surfaces
// as a toast instead of an error.
func TestFetchDialogSaveFailureToast(t *testing.T) {
	repo := newGitRepo(t, "fetchdlg-savefail")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	peerHead := pushPeerCommit(t, bareDir, "main", "p.txt", "p\n", "peer commit")

	// Build the harness by hand so the store's directory can be made
	// read-only after the initial snapshot: SetSheets then fails on write.
	storeDir := t.TempDir()
	t.Cleanup(func() {
		if err := os.Chmod(storeDir, 0o755); err != nil {
			t.Errorf("restore store dir permissions: %v", err)
		}
	})
	s, err := store.Open(filepath.Join(storeDir, "repositories.json"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)
	a := newApp(s)
	a.postFn = q.post
	a.spawnFn = q.spawn
	h := &testHarness{
		t:     t,
		app:   a,
		store: s,
		ui:    ui.NewTester(a.view, 1200, 800),
		queue: q,
	}
	h.ui.Frame()
	openSyncRepo(t, h, repo.StoreRepo())

	if err := os.Chmod(storeDir, 0o555); err != nil {
		t.Fatalf("make store dir read-only: %v", err)
	}

	// Untick Prune so the save has something to write.
	openToolbarFetchDialog(t, h)
	tickBox(t, h, "Prune remote-tracking branches")
	if err := h.ui.Click("Confirm Fetch"); err != nil {
		t.Fatalf("click Confirm Fetch: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("fetch failed although only the settings save may fail: %v", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "refs/remotes/origin/main")); got != peerHead {
		t.Fatalf("origin/main = %q, want %q: the fetch must run although saving failed", got, peerHead)
	}
	if !h.ui.HasText("Cannot save settings") {
		t.Fatalf("save failure toast missing; texts=%q", h.ui.Texts())
	}
}

// --- close paths ---

// Cancel, Escape, the backdrop and a repository switch all close the dialog
// without running a single git command, proven by the recording executor on
// the repository.
func TestFetchDialogClosePathsNeverFetch(t *testing.T) {
	setup := func(t *testing.T) *gitRepoFixture {
		repo := newGitRepo(t, "fetchdlg-close")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.AddBareRemote("origin")
		return repo
	}
	verify := func(t *testing.T, h *testHarness, rec *recordingRunExecutor) {
		t.Helper()
		h.ui.Frame()
		if syncStateFor(h.app).fetchDlg.open {
			t.Fatal("Fetch dialog survived the close path")
		}
		h.drain()
		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("closing the dialog produced an action error: %v", err)
		}
		if total := rec.total(); total != 0 {
			t.Fatalf("close path ran %d git command(s), want 0", total)
		}
	}

	t.Run("cancel button", func(t *testing.T) {
		repo := setup(t)
		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		openToolbarFetchDialog(t, h)
		rec := installRecordingExecutor(t, h)
		if err := h.ui.Click("Cancel"); err != nil {
			t.Fatalf("click Cancel: %v", err)
		}
		verify(t, h, rec)
	})

	t.Run("escape", func(t *testing.T) {
		repo := setup(t)
		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		openToolbarFetchDialog(t, h)
		rec := installRecordingExecutor(t, h)
		h.ui.Key(0, ui.KeyEscape)
		verify(t, h, rec)
	})

	t.Run("backdrop", func(t *testing.T) {
		repo := setup(t)
		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		openToolbarFetchDialog(t, h)
		rec := installRecordingExecutor(t, h)
		// The modal is centered in the 1200x800 test window; its top-left
		// corner is the dimmed backdrop outside the dialog.
		h.ui.ClickAt(20, 20)
		verify(t, h, rec)
	})

	t.Run("repository switch", func(t *testing.T) {
		repoA := setup(t)
		repoB := newGitRepo(t, "fetchdlg-close-other")
		repoB.CommitFile("b.txt", "b\n", "init b")

		h := newTestApp(t, repoA.StoreRepo(), repoB.StoreRepo())
		closeSync(t, h)
		h.openRepo(repoA.StoreRepo())
		h.drain()
		openToolbarFetchDialog(t, h)
		rec := installRecordingExecutor(t, h)

		// The modal covers the switcher, so a switch with the dialog open is
		// programmatic — as opening a repository of the recent list is in the
		// product — and reaches the same generation change.
		h.openRepo(repoB.StoreRepo())
		h.ui.Frame()
		h.drain()
		if syncStateFor(h.app).fetchDlg.open {
			t.Fatal("Fetch dialog survived the repository switch")
		}
		if total := rec.total(); total != 0 {
			t.Fatalf("repository switch ran %d git command(s) against the old repository, want 0", total)
		}
	})
}

// --- toolbar entry ---

// Option+click on the toolbar Fetch button never opens the dialog: it
// fetches all remotes directly, pruning, exactly as a plain click did before
// the dialog existed.
func TestFetchDialogOptionClickQuickFetch(t *testing.T) {
	repo := newGitRepo(t, "fetchdlg-quickfetch")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	peerHead := pushPeerCommit(t, bareDir, "main", "p.txt", "p\n", "peer commit")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())
	rec := installRecordingExecutor(t, h)

	if err := h.ui.ClickWith(ui.Alt, "Fetch"); err != nil {
		t.Fatalf("Option+click Fetch: %v", err)
	}
	h.ui.Frame()
	if syncStateFor(h.app).fetchDlg.open {
		t.Fatal("Fetch dialog opened on Option+click")
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("quick fetch failed: %v", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "refs/remotes/origin/main")); got != peerHead {
		t.Fatalf("origin/main = %q, want %q", got, peerHead)
	}
	// The recorded fetch is exactly the quick fetch of before the dialog:
	// all remotes, pruning, no tags with a fresh store.
	rec.mu.Lock()
	var fetches [][]string
	for _, args := range rec.cmds {
		if len(args) > 0 && args[0] == "fetch" {
			fetches = append(fetches, args)
		}
	}
	rec.mu.Unlock()
	if len(fetches) != 1 {
		t.Fatalf("recorded %d fetch commands, want 1", len(fetches))
	}
	want := []string{"fetch", "--all", "--prune"}
	if strings.Join(fetches[0], " ") != strings.Join(want, " ") {
		t.Fatalf("quick fetch args = %v, want %v", fetches[0], want)
	}
}

// A repository without remotes opens no dialog: the pre-flight error shows
// in the dismissible toolbar strip and nothing runs.
func TestFetchDialogNoRemote(t *testing.T) {
	repo := newGitRepo(t, "fetchdlg-noremote")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())
	rec := installRecordingExecutor(t, h)

	if err := h.ui.Click("Fetch"); err != nil {
		t.Fatalf("click Fetch: %v", err)
	}
	h.ui.Frame()

	if syncStateFor(h.app).fetchDlg.open {
		t.Fatal("Fetch dialog opened without a remote")
	}
	if !h.ui.HasText("The repository has no remote to fetch from.") {
		t.Fatalf("missing no-remote message; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error status after Fetch without remote; texts=%q", h.ui.Texts())
	}
	if h.app.session.Busy() {
		t.Fatal("session busy for a fetch that should not have started")
	}
	if total := rec.total(); total != 0 {
		t.Fatalf("no-remote validation ran %d git command(s), want 0", total)
	}
}

// --- worker guard ---

// A remote deleted after the dialog opened is never fetched: the worker
// confirms the remote first and the refusal names it in the toolbar.
func TestFetchDialogRemoteRemovedBeforeSubmit(t *testing.T) {
	repo := newGitRepo(t, "fetchdlg-remote-gone")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarFetchDialog(t, h)
	rec := installRecordingExecutor(t, h)
	repo.Git("remote", "remove", "origin")

	if err := h.ui.Click("Confirm Fetch"); err != nil {
		t.Fatalf("click Confirm Fetch: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("fetch of a removed remote produced no error")
	}
	if !strings.Contains(err.Error(), "The remote origin no longer exists.") {
		t.Fatalf("guard error = %q, want it to name the removed remote", err)
	}
	if got := rec.count("fetch"); got != 0 {
		t.Fatalf("guard let %d fetch command(s) through, want 0", got)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error after the guard refusal; texts=%q", h.ui.Texts())
	}
}

// --- busy guard ---

// While the repository is busy the confirm button is disabled and Enter
// submits nothing; once idle again Enter submits like the button.
func TestFetchDialogBusyConfirmDisabled(t *testing.T) {
	repo := newGitRepo(t, "fetchdlg-busy")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	peerHead := pushPeerCommit(t, bareDir, "main", "p.txt", "p\n", "peer commit")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarFetchDialog(t, h)

	// Start a real pull behind the worker gate: the session is busy while the
	// dialog stays open.
	h.queue.PauseWorkers()
	h.app.syncPull()
	if !h.app.session.Busy() {
		t.Fatal("session not busy after starting the gated pull")
	}
	pullWorker := h.queue.WaitWorker(t)
	h.ui.Frame()

	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Fetch")
	if !ok || !btn.Disabled {
		t.Fatalf("Confirm Fetch while busy = %+v (found=%v), want disabled", btn, ok)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if !syncStateFor(h.app).fetchDlg.open {
		t.Fatal("Enter submitted the dialog while the repository was busy")
	}

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(pullWorker)
	h.drain()
	if h.app.session.Busy() {
		t.Fatal("session stayed busy after the gated pull finished")
	}

	// Idle again: the button is enabled and Enter submits the dialog.
	btn, ok = h.ui.AccessByRole(ui.RoleButton, "Confirm Fetch")
	if !ok || btn.Disabled {
		t.Fatalf("Confirm Fetch after drain = %+v (found=%v), want enabled", btn, ok)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if syncStateFor(h.app).fetchDlg.open {
		t.Fatal("dialog stayed open after Enter submit")
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter-submitted fetch failed: %v", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "refs/remotes/origin/main")); got != peerHead {
		t.Fatalf("origin/main = %q, want %q after the Enter-submitted fetch", got, peerHead)
	}
}

// --- loopback SSH ---

// Over loopback SSH the dialog fetches the server repository's remote, and
// behind a password gate the worker asks through the real askpass modal —
// the dialog itself already closed when the question renders.
func TestFetchDialogAskPassLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repoDir, "push", "-q", "-u", "origin", "main")

	// A peer advances origin/main so the fetch has an observable effect.
	peerDir := srv.TempDir(t, "peer")
	srv.RunShell(t, "set -eu\nexport GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1\n"+
		"git clone -q "+shellQ(bareDir)+" "+shellQ(peerDir)+"\n"+
		"cd "+shellQ(peerDir)+"\n"+
		"git config user.name Peer\ngit config user.email peer@branchline.invalid\n"+
		"printf 'peer\\n' > peer.txt\ngit add -- peer.txt\ngit commit -q -m 'peer commit'\n"+
		"git push -q origin main\n")
	peerHead := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/main")

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	// Gate every new connection behind password authentication: from here on
	// only the askpass-enabled path reaches the server.
	const password = "hunter2t0ps3cret"
	gateDir := passwordGateWrapper(t, srv.SSHProgram(), password)
	t.Setenv("PATH", gateDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// The worker's remote probe — for-each-ref, remote and the fetch itself —
	// cross the gate as separate ssh invocations, so the question comes back
	// a handful of times. The loop answers every question until the worker's
	// completion arrives, consuming the wake-up callbacks one at a time.
	answerPasswords := func() {
		t.Helper()
		for range 16 {
			h.queue.RunNext(t)
			h.ui.Frame()
			if _, ok := h.ui.Find("Cancel"); !ok {
				if !h.app.session.Busy() {
					return
				}
				continue
			}
			if syncStateFor(h.app).fetchDlg.open {
				t.Fatal("the Fetch dialog is still open under the askpass question")
			}
			h.ui.Type(password)
			h.ui.Frame()
			if err := h.ui.Click("OK"); err != nil {
				t.Fatalf("click OK in password dialog: %v", err)
			}
			h.ui.Frame()
		}
		t.Fatal("password questions did not stop after 16 rounds")
	}

	openToolbarFetchDialog(t, h)
	if err := h.ui.Click("Confirm Fetch"); err != nil {
		t.Fatalf("click Confirm Fetch: %v", err)
	}
	h.ui.Frame()
	if syncStateFor(h.app).fetchDlg.open {
		t.Fatal("the Fetch dialog stayed open after the confirm")
	}

	answerPasswords()
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("fetch behind the password gate failed after correct passwords: %v", err)
	}
	if got := srv.RunGit(t, repoDir, "rev-parse", "origin/main"); got != peerHead {
		t.Fatalf("server origin/main after dialog fetch = %q, want %q", got, peerHead)
	}
}
