package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
	"github.com/zhdsmy/branchline/internal/store"
)

// remoteEditCapture is the test seam recording what would open in the
// default application or reveal in the file manager: tests never launch
// either.
type remoteEditCapture struct {
	mu      sync.Mutex
	opened  []string
	shown   []string
	openErr error
}

func (c *remoteEditCapture) open(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opened = append(c.opened, path)
	return c.openErr
}

func (c *remoteEditCapture) show(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shown = append(c.shown, path)
}

func (c *remoteEditCapture) openedPaths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.opened...)
}

func (c *remoteEditCapture) shownPaths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.shown...)
}

// installRemoteEditSeams captures opens and reveals, redirects the copy
// cache into a temporary directory, and registers the sync teardown the
// askpass machinery needs. The cache injection must land before anything
// creates the feature's state, so the sweep never touches the real cache.
func installRemoteEditSeams(t *testing.T, h *testHarness) (*remoteEditCapture, string) {
	t.Helper()
	closeSync(t, h)
	capture := &remoteEditCapture{}
	cacheDir := filepath.Join(t.TempDir(), "remote-edit-cache")
	setRemoteEditOpener(h.app, capture.open, capture.show)
	setRemoteEditCacheDir(h.app, func() (string, error) { return cacheDir, nil })
	return capture, cacheDir
}

// gatedShellExecutor wraps the session repository's SSH executor counting
// and optionally gating the shell commands of remote editing, recognized by
// their scripts: downloads cat the file, saves write a temporary one. git
// operations of the session never go through RunShellInput, so the gate
// holds remote editing alone. The workers of remote editing run without a
// canceling context, so the gate also watches abandoned, which the cleanup
// closes before draining: a test failing while a command is held must
// release the worker rather than hang the teardown.
type gatedShellExecutor struct {
	real *git.SSHExecutor

	mu         sync.Mutex
	downloads  int
	saves      int
	gateCh     chan struct{}
	gateArrive chan struct{}
	abandoned  chan struct{}
}

func (g *gatedShellExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	return g.real.Run(ctx, stdin, args...)
}

func (g *gatedShellExecutor) Describe() string { return g.real.Describe() }

func (g *gatedShellExecutor) DirPath() string { return g.real.DirPath() }

func (g *gatedShellExecutor) RunShellInput(ctx context.Context, command string, stdin []byte) ([]byte, error) {
	save := strings.Contains(command, "cat > \"$tmp\"")
	g.mu.Lock()
	if save {
		g.saves++
	} else {
		g.downloads++
	}
	gate := g.gateCh
	arrive := g.gateArrive
	g.mu.Unlock()
	if gate != nil {
		select {
		case arrive <- struct{}{}:
		default:
		}
		select {
		case <-gate:
		case <-g.abandoned:
			return nil, errors.New("the test abandoned the gated shell command")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.real.RunShellInput(ctx, command, stdin)
}

func (g *gatedShellExecutor) counts() (downloads, saves int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.downloads, g.saves
}

// gateNext makes the next shell command of remote editing block until
// releaseGated closes the gate; gateArrived reports one arrived.
func (g *gatedShellExecutor) gateNext() {
	g.mu.Lock()
	g.gateCh = make(chan struct{})
	g.gateArrive = make(chan struct{}, 4)
	g.mu.Unlock()
}

func (g *gatedShellExecutor) gateArrived(t *testing.T) {
	t.Helper()
	g.mu.Lock()
	arrive := g.gateArrive
	g.mu.Unlock()
	if arrive == nil {
		t.Fatal("gateArrived without gateNext")
	}
	select {
	case <-arrive:
	case <-time.After(queueTimeout):
		t.Fatal("timed out waiting for the gated shell command")
	}
}

func (g *gatedShellExecutor) releaseGate(t *testing.T) {
	t.Helper()
	g.mu.Lock()
	gate := g.gateCh
	g.gateCh = nil
	g.mu.Unlock()
	if gate == nil {
		t.Fatal("releaseGate without a held command")
	}
	close(gate)
}

// installGatedShellExecutor replaces the session repository's executor with
// a counting one and restores it at cleanup after every worker drained.
func installGatedShellExecutor(t *testing.T, h *testHarness) *gatedShellExecutor {
	t.Helper()
	repo := h.app.session.Repo()
	if repo == nil {
		t.Fatal("cannot install the gated executor without an open repository")
	}
	ssh, ok := repo.Exec.(*git.SSHExecutor)
	if !ok {
		t.Fatalf("session executor = %T, want *git.SSHExecutor", repo.Exec)
	}
	wrapped := &gatedShellExecutor{real: ssh, abandoned: make(chan struct{})}
	repo.Exec = wrapped
	t.Cleanup(func() {
		close(wrapped.abandoned)
		h.queue.releaseAllAndWait()
		repo.Exec = ssh
	})
	return wrapped
}

// openRemoteEditHarness opens the loopback SSH repository with a.txt
// modified in its working tree, the remote editing seams installed, and the
// initial snapshot loaded.
func openRemoteEditHarness(t *testing.T, repo store.Repo, srv *sshtest.Target) (*testHarness, *remoteEditCapture, string) {
	t.Helper()
	srv.WriteFile(t, repo.Path, "a.txt", []byte("base\n"))
	h := newTestApp(t)
	capture, cacheDir := installRemoteEditSeams(t, h)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH initial load failed: %v", err)
	}
	if h.app.session.Meta().Kind != store.SSH {
		t.Fatalf("session kind = %q, want SSH", h.app.session.Meta().Kind)
	}
	return h, capture, cacheDir
}

// chooseRemoteEditItem right-clicks the row of path in the Unstaged section
// and chooses item, as a user would with the secondary button.
func chooseRemoteEditItem(t *testing.T, h *testHarness, path, item string) {
	t.Helper()
	row := "Unstaged " + path
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click %q: %v\ntexts=%q", row, err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem(item); err != nil {
		t.Fatalf("choose %q in menu of %q: %v", item, row, err)
	}
}

// remoteEditEntryOf returns the entry the app keeps for path of the open
// repository, for assertions only.
func remoteEditEntryOf(h *testHarness, path string) *remoteEditEntry {
	st := remoteEditUI(h.app)
	return st.entries[remoteEditKey{repoID: h.app.session.Meta().ID, path: path}]
}

// opening for editing downloads the file into a private copy, opens it
// through the injected opener, and marks the row as edited locally; editing
// the copy and saving writes the bytes back to the server's working tree,
// refreshes Local Changes, and the copy stays saveable because its metadata
// moved with the save.
func TestRemoteEditUIOpenEditSaveAndBadge(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	h, capture, _ := openRemoteEditHarness(t, repo, srv)

	chooseRemoteEditItem(t, h, "a.txt", "Open for Editing")
	h.drain()

	opened := capture.openedPaths()
	if len(opened) != 1 {
		t.Fatalf("opens = %q, want exactly one", opened)
	}
	local := opened[0]
	if !strings.Contains(filepath.Base(filepath.Dir(local)), "edit-") {
		t.Fatalf("copy %q not inside an edit-* directory", local)
	}
	if filepath.Base(local) != "a.txt" {
		t.Fatalf("copy name = %q, want a.txt", filepath.Base(local))
	}
	if got, err := os.ReadFile(local); err != nil || string(got) != "base\n" {
		t.Fatalf("copy content = %q err %v, want base", got, err)
	}
	entry := remoteEditEntryOf(h, "a.txt")
	if entry == nil || !entry.hasCopy || entry.copy.Local != local {
		t.Fatalf("entry after download = %+v, want the copy at %q", entry, local)
	}

	// The row now carries the badge a screen reader names "Edited Locally",
	// and opening the same file again reuses the copy instead of downloading.
	h.ui.Frame()
	if _, ok := h.ui.Access("Edited Locally"); !ok {
		t.Fatalf("no Edited Locally badge on the row; texts=%q", h.ui.Texts())
	}
	chooseRemoteEditItem(t, h, "a.txt", "Open for Editing")
	h.drain()
	if opened := capture.openedPaths(); len(opened) != 2 || opened[1] != local {
		t.Fatalf("re-open opens = %q, want the same copy twice", opened)
	}
	dirs, err := os.ReadDir(filepath.Dir(filepath.Dir(local)))
	if err != nil || len(dirs) != 1 {
		t.Fatalf("edit-* directories = %d err %v, want exactly 1 (no second download)", len(dirs), err)
	}

	// Edit the copy as the default editor would, then save it to the server.
	if err := os.WriteFile(local, []byte("base\nedited locally\n"), 0o600); err != nil {
		t.Fatalf("edit copy: %v", err)
	}
	chooseRemoteEditItem(t, h, "a.txt", "Save to Server")
	h.drain()

	if got := string(srv.ReadFile(t, repo.Path, "a.txt")); got != "base\nedited locally\n" {
		t.Fatalf("server content after save = %q, want the edited copy", got)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("load after save failed: %v", err)
	}
	if got := syncStateFor(h.app).syncErr; got != "" {
		t.Fatalf("operation error after save = %q, want none", got)
	}

	// The copy stays editable and saveable: editing it once more and saving
	// again writes the new bytes, proving the metadata moved with the first
	// save instead of staying at what was downloaded.
	if err := os.WriteFile(local, []byte("base\nedited twice\n"), 0o600); err != nil {
		t.Fatalf("edit copy again: %v", err)
	}
	chooseRemoteEditItem(t, h, "a.txt", "Save to Server")
	h.drain()
	if got := string(srv.ReadFile(t, repo.Path, "a.txt")); got != "base\nedited twice\n" {
		t.Fatalf("server content after second save = %q, want the second edit", got)
	}
	if got := syncStateFor(h.app).syncErr; got != "" {
		t.Fatalf("operation error after second save = %q, want none", got)
	}
	if _, ok := h.ui.Access("Edited Locally"); !ok {
		t.Fatal("badge gone while the copy still exists")
	}
}

// saving after somebody else changed the file on the server writes nothing:
// the notice says the edits are still in the local copy, Show Local Copy
// reveals it through the injected seam, and the copy and the server's
// content both stay exactly as they were.
func TestRemoteEditUIConflictOnSaveKeepsCopyAndServer(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	h, capture, _ := openRemoteEditHarness(t, repo, srv)

	chooseRemoteEditItem(t, h, "a.txt", "Open for Editing")
	h.drain()
	opened := capture.openedPaths()
	if len(opened) != 1 {
		t.Fatalf("opens = %q, want exactly one", opened)
	}
	local := opened[0]
	if err := os.WriteFile(local, []byte("my edit\n"), 0o600); err != nil {
		t.Fatalf("edit copy: %v", err)
	}

	// Somebody else writes the file on the server after it was downloaded.
	srv.WriteFile(t, repo.Path, "a.txt", []byte("somebody else\n"))
	h.drain()

	chooseRemoteEditItem(t, h, "a.txt", "Save to Server")
	h.drain()
	h.ui.Frame()

	if !h.ui.HasText("The file on the server changed after it was opened for editing. Nothing was written; your edits are still in the local copy.") {
		t.Fatalf("server-changed notice missing; texts=%q", h.ui.Texts())
	}
	if got := string(srv.ReadFile(t, repo.Path, "a.txt")); got != "somebody else\n" {
		t.Fatalf("server content = %q, want the other change — nothing may be written", got)
	}
	entry := remoteEditEntryOf(h, "a.txt")
	if entry == nil || !entry.hasCopy {
		t.Fatal("the copy is gone after the refused save, want it kept")
	}
	if got, err := os.ReadFile(local); err != nil || string(got) != "my edit\n" {
		t.Fatalf("copy content = %q err %v, want my edit", got, err)
	}

	// The notice offers no overwrite; revealing the copy is its one action.
	if h.ui.HasText("Overwrite") {
		t.Fatalf("the notice offers an overwrite, which it must not; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Show Local Copy")
	h.ui.Frame()
	shown := capture.shownPaths()
	if len(shown) != 1 || shown[0] != local {
		t.Fatalf("reveals = %q, want exactly the copy %q", shown, local)
	}
	if entry := remoteEditEntryOf(h, "a.txt"); entry == nil || !entry.hasCopy {
		t.Fatal("the copy is gone after Show Local Copy, want it kept")
	}
}

// discarding a copy that was edited asks first: canceling keeps it,
// confirming deletes it and its directory, while a copy never edited is
// deleted without asking.
func TestRemoteEditUIDiscardChangedConfirmsUnchangedDeletes(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	h, capture, _ := openRemoteEditHarness(t, repo, srv)

	chooseRemoteEditItem(t, h, "a.txt", "Open for Editing")
	h.drain()
	opened := capture.openedPaths()
	if len(opened) != 1 {
		t.Fatalf("opens = %q, want exactly one", opened)
	}
	local := opened[0]
	if err := os.WriteFile(local, []byte("changed\n"), 0o600); err != nil {
		t.Fatalf("edit copy: %v", err)
	}

	chooseRemoteEditItem(t, h, "a.txt", "Discard Local Copy")
	h.drain()
	h.ui.Frame()
	if !h.ui.HasText("Discard local copy?") {
		t.Fatalf("no confirmation for an edited copy; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Cancel")
	h.ui.Frame()
	if _, err := os.Stat(local); err != nil {
		t.Fatalf("copy gone after canceling the confirmation: %v", err)
	}
	if entry := remoteEditEntryOf(h, "a.txt"); entry == nil || !entry.hasCopy {
		t.Fatal("entry gone after canceling the confirmation")
	}

	chooseRemoteEditItem(t, h, "a.txt", "Discard Local Copy")
	h.drain()
	h.ui.Frame()
	mustClick(t, h, "Confirm Discard Local Copy")
	h.drain()
	h.ui.Frame()
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatalf("copy still exists after confirming (err %v), want it deleted", err)
	}
	if _, err := os.Stat(filepath.Dir(local)); !os.IsNotExist(err) {
		t.Fatalf("copy directory still exists after confirming (err %v), want it deleted", err)
	}
	if entry := remoteEditEntryOf(h, "a.txt"); entry != nil {
		t.Fatalf("entry still present after the discard: %+v", entry)
	}
	if _, ok := h.ui.Access("Edited Locally"); ok {
		t.Fatal("badge still present after the discard")
	}

	// A copy never edited since the download is removed without asking.
	chooseRemoteEditItem(t, h, "a.txt", "Open for Editing")
	h.drain()
	opened = capture.openedPaths()
	if len(opened) != 2 {
		t.Fatalf("opens = %q, want a second download", opened)
	}
	local2 := opened[1]
	chooseRemoteEditItem(t, h, "a.txt", "Discard Local Copy")
	h.drain()
	h.ui.Frame()
	if h.ui.HasText("Discard local copy?") {
		t.Fatalf("confirmation asked for an unedited copy, want a direct delete; texts=%q", h.ui.Texts())
	}
	if _, err := os.Stat(local2); !os.IsNotExist(err) {
		t.Fatalf("unedited copy still exists (err %v), want it deleted", err)
	}
}

// local repositories offer none of the remote editing entries, and in an
// SSH repository a deleted file offers no Open for Editing.
func TestRemoteEditUIMenuHidesForLocalReposAndDeletedFiles(t *testing.T) {
	local := newGitRepo(t, "re-local")
	local.CommitFile("tracked.txt", "one\n", "initial commit")
	local.WriteFile("tracked.txt", "one\ntwo\n")

	h := newTestApp(t)
	capture, _ := installRemoteEditSeams(t, h)
	h.openRepo(local.StoreRepo())
	h.drain()

	row := "Unstaged tracked.txt"
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click %q: %v", row, err)
	}
	for _, item := range []string{"Open for Editing", "Save to Server", "Discard Local Copy"} {
		for _, label := range h.ui.Menu() {
			if label == item {
				t.Fatalf("local repository offers %q; menu=%q", item, h.ui.Menu())
			}
		}
	}
	h.ui.CloseMenu()
	if len(capture.openedPaths()) != 0 {
		t.Fatalf("opens = %q, want none", capture.openedPaths())
	}

	// A deleted file of an SSH repository has no working-tree file to edit:
	// Open for Editing is absent while Save and Discard stay absent too for
	// want of a copy.
	repo, srv := loopbackSSHRepo(t)
	h2 := newTestApp(t)
	installRemoteEditSeams(t, h2)
	srv.WriteFile(t, repo.Path, "a.txt", []byte("one\nchanged\n"))
	srv.RunShell(t, "rm -- "+git.ShellQuote(repo.Path+"/a.txt"))
	h2.openRepo(repo)
	h2.drain()
	if err := h2.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH initial load failed: %v", err)
	}

	row = "Unstaged a.txt"
	if err := h2.ui.RightClick(row); err != nil {
		t.Fatalf("right-click %q: %v\ntexts=%q", row, err, h2.ui.Texts())
	}
	menu := h2.ui.Menu()
	for _, label := range menu {
		if label == "Open for Editing" {
			t.Fatalf("deleted file offers Open for Editing; menu=%q", menu)
		}
		if label == "Save to Server" || label == "Discard Local Copy" {
			t.Fatalf("file without a copy offers %q; menu=%q", label, menu)
		}
	}
	h2.ui.CloseMenu()

	// A modified file does offer Open for Editing.
	repo2, srv2 := loopbackSSHRepo(t)
	h3, _, _ := openRemoteEditHarness(t, repo2, srv2)
	row = "Unstaged a.txt"
	if err := h3.ui.RightClick(row); err != nil {
		t.Fatalf("right-click %q: %v", row, err)
	}
	found := false
	for _, label := range h3.ui.Menu() {
		if label == "Open for Editing" {
			found = true
		}
	}
	h3.ui.CloseMenu()
	if !found {
		t.Fatalf("modified file of an SSH repository misses Open for Editing; menu=%q", h3.ui.Menu())
	}
}

// choosing Open for Editing while the download is still running starts no
// second download: the item stays disabled for the duration, exactly one
// download command reaches the server, and only one copy directory exists.
func TestRemoteEditUIDownloadingReentryBlocked(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	h, capture, cacheDir := openRemoteEditHarness(t, repo, srv)
	gated := installGatedShellExecutor(t, h)
	gated.gateNext()

	chooseRemoteEditItem(t, h, "a.txt", "Open for Editing")
	gated.gateArrived(t)

	row := "Unstaged a.txt"
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click %q: %v", row, err)
	}
	err := h.ui.ChooseMenuItem("Open for Editing")
	if err == nil {
		t.Fatal("Open for Editing was enabled while the download is in flight")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing while downloading err = %v, want a disabled item", err)
	}
	h.ui.CloseMenu()

	gated.releaseGate(t)
	h.drain()
	if downloads, _ := gated.counts(); downloads != 1 {
		t.Fatalf("download commands = %d, want exactly 1", downloads)
	}
	if len(capture.openedPaths()) != 1 {
		t.Fatalf("opens = %q, want exactly one", capture.openedPaths())
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("edit-* directories = %d err %v, want exactly 1", len(entries), err)
	}
}

// choosing Save to Server while a save is still running starts no second
// upload: the item stays disabled for the duration and exactly one save
// command reaches the server.
func TestRemoteEditUISavingReentryBlocked(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	h, capture, _ := openRemoteEditHarness(t, repo, srv)
	gated := installGatedShellExecutor(t, h)

	chooseRemoteEditItem(t, h, "a.txt", "Open for Editing")
	h.drain()
	opened := capture.openedPaths()
	if len(opened) != 1 {
		t.Fatalf("opens = %q, want exactly one", opened)
	}
	if err := os.WriteFile(opened[0], []byte("edited\n"), 0o600); err != nil {
		t.Fatalf("edit copy: %v", err)
	}

	gated.gateNext()
	chooseRemoteEditItem(t, h, "a.txt", "Save to Server")
	gated.gateArrived(t)

	row := "Unstaged a.txt"
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click %q: %v", row, err)
	}
	err := h.ui.ChooseMenuItem("Save to Server")
	if err == nil {
		t.Fatal("Save to Server was enabled while a save is in flight")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing while saving err = %v, want a disabled item", err)
	}
	h.ui.CloseMenu()

	gated.releaseGate(t)
	h.drain()
	_, saves := gated.counts()
	if saves != 1 {
		t.Fatalf("save commands = %d, want exactly 1", saves)
	}
	if got := string(srv.ReadFile(t, repo.Path, "a.txt")); got != "edited\n" {
		t.Fatalf("server content = %q, want the saved edit", got)
	}
}

// the entries are remembered by repository rather than by generation:
// switching to another repository and back keeps the copy on disk, the
// badge on the row, and the save working without a new download.
func TestRemoteEditUIStateSurvivesRepoSwitch(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	otherFixture := newGitRepo(t, "re-switch-other")
	otherFixture.CommitFile("other.txt", "other\n", "other initial commit")
	h, capture, cacheDir := openRemoteEditHarness(t, repo, srv)

	chooseRemoteEditItem(t, h, "a.txt", "Open for Editing")
	h.drain()
	opened := capture.openedPaths()
	if len(opened) != 1 {
		t.Fatalf("opens = %q, want exactly one", opened)
	}
	local := opened[0]
	if err := os.WriteFile(local, []byte("switched edit\n"), 0o600); err != nil {
		t.Fatalf("edit copy: %v", err)
	}

	// Switch away and back; the copy and its badge must still be there.
	firstID := h.app.session.Meta().ID
	h.openRepo(otherFixture.StoreRepo())
	h.drain()
	if h.app.session.Meta().ID == firstID {
		t.Fatal("still in the first repository after opening the other one")
	}
	first, ok := h.store.Get(firstID)
	if !ok {
		t.Fatalf("first repository %q no longer in the store", firstID)
	}
	h.openRepo(first)
	h.drain()
	if h.app.session.Meta().ID != firstID {
		t.Fatalf("back in %q, want the first repository", h.app.session.Meta().Name)
	}

	if _, err := os.Stat(local); err != nil {
		t.Fatalf("copy gone after the repository switch: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.Access("Edited Locally"); !ok {
		t.Fatalf("badge gone after switching back; texts=%q", h.ui.Texts())
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("edit-* directories = %d err %v, want exactly 1", len(entries), err)
	}

	// Saving still writes the edit made before the switch: the entry
	// survived with its metadata, so no conflict is raised.
	chooseRemoteEditItem(t, h, "a.txt", "Save to Server")
	h.drain()
	if got := string(srv.ReadFile(t, repo.Path, "a.txt")); got != "switched edit\n" {
		t.Fatalf("server content = %q, want the edit made before the switch", got)
	}
}

// creating the remote editing state sweeps edit-* cache directories older
// than seven days and keeps newer ones.
func TestRemoteEditUISweepsOldCacheDirs(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	h := newTestApp(t)
	capture, cacheDir := installRemoteEditSeams(t, h)
	_ = capture

	old := filepath.Join(cacheDir, "edit-old")
	recent := filepath.Join(cacheDir, "edit-new")
	foreign := filepath.Join(cacheDir, "not-an-edit-dir")
	for _, dir := range []string{old, recent, foreign} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	aged := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(old, aged, aged); err != nil {
		t.Fatalf("age edit-old: %v", err)
	}
	if err := os.Chtimes(foreign, aged, aged); err != nil {
		t.Fatalf("age not-an-edit-dir: %v", err)
	}

	// The first use of remote editing — building its state as the menu of an
	// SSH repository's file row opens — starts the sweep in the background.
	srv.WriteFile(t, repo.Path, "a.txt", []byte("base\n"))
	h.openRepo(repo)
	h.drain()
	row := "Unstaged a.txt"
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click %q: %v\ntexts=%q", row, err, h.ui.Texts())
	}
	h.ui.CloseMenu()
	h.drain()

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old edit-* directory still exists (err %v), want it swept", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("recent edit-* directory swept, want it kept: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("directory without the edit- prefix swept, want it kept: %v", err)
	}
}

// the file row's context menu opens from the keyboard too: Shift+F10 on the
// focused row shows the same entries a right-click does.
func TestRemoteEditUIMenuOpensWithKeyboard(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	h, _, _ := openRemoteEditHarness(t, repo, srv)

	row := "Unstaged a.txt"
	if err := h.ui.Click(row); err != nil {
		t.Fatalf("focus %q: %v", row, err)
	}
	h.ui.Key(ui.Shift, ui.KeyF10)
	menu := h.ui.Menu()
	found := false
	for _, label := range menu {
		if label == "Open for Editing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Shift+F10 menu missing Open for Editing; menu=%q", menu)
	}
	if err := h.ui.ChooseMenuItem("Open for Editing"); err != nil {
		t.Fatalf("choose Open for Editing from the keyboard menu: %v", err)
	}
	h.drain()
	if entry := remoteEditEntryOf(h, "a.txt"); entry == nil || !entry.hasCopy {
		t.Fatal("no copy after choosing Open for Editing from the keyboard menu")
	}
}
