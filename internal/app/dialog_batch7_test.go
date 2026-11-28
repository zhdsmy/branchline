package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/store"
)

// The tests of this file pin the batch of dialogs moved onto the shared
// frame: the two single-button notices (Cannot Remove Repository, Tag
// Details), the not-a-git notice whose create locks every way out, and the
// two forms whose text fields submit with Enter (Add SSH Repository, Clone
// Repository). Each drives the real entry — the switcher popup, the folder
// picker or the tag's context menu — on real repositories.

// dialogButtonsIn returns the accessible names of the buttons drawn inside
// the dialog's panel, ordered left to right: the footer's composition and
// order, which a modal alone in the accessibility tree makes exact.
func dialogButtonsIn(t *testing.T, h *testHarness, dlg ui.AccessInfo) []string {
	t.Helper()
	type button struct {
		x    float32
		name string
	}
	var inside []button
	for _, a := range h.ui.AccessAll() {
		if a.Role != ui.RoleButton {
			continue
		}
		const tol = 0.5
		b, d := a.Bounds, dlg.Bounds
		if b.W <= 0 || b.H <= 0 {
			continue
		}
		if b.X < d.X-tol || b.Y < d.Y-tol || b.X+b.W > d.X+d.W+tol || b.Y+b.H > d.Y+d.H+tol {
			continue
		}
		inside = append(inside, button{x: b.X, name: a.Name})
	}
	slices.SortFunc(inside, func(a, b button) int {
		switch {
		case a.x < b.x:
			return -1
		case a.x > b.x:
			return 1
		}
		return 0
	})
	names := make([]string, 0, len(inside))
	for _, b := range inside {
		names = append(names, b.name)
	}
	return names
}

// requireDialog fetches the one dialog panel named title or fails.
func requireDialog(t *testing.T, h *testHarness, title string) ui.AccessInfo {
	t.Helper()
	dlg, ok := h.ui.AccessByRole(ui.RoleDialog, title)
	if !ok {
		t.Fatalf("no dialog panel named %q; texts=%q", title, h.ui.Texts())
	}
	return dlg
}

// The Cannot Remove Repository notice carries a single Close button which
// opens with the focus on it: Enter presses it and the notice clears without
// losing the list entry, and the notice reopened clears with Escape too.
func TestDialogBatch7RemoveErrorSingleButtonClose(t *testing.T) {
	repo := newGitRepo(t, "batch7remove")
	repo.CommitFile("a.txt", "a\n", "commit")

	h := newTestApp(t, repo.StoreRepo())
	closeSync(t, h)

	// The store cannot persist the removal, so every removal attempt fails
	// into the notice.
	storeDir := filepath.Dir(h.store.Path())
	if err := os.Chmod(storeDir, 0o500); err != nil {
		t.Fatalf("chmod store dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(storeDir, 0o700) })

	// openRemoveError clicks the entry's remove button through the switcher
	// popup, reopening the popup first when the notice's comings and goings
	// closed it.
	openRemoveError := func() {
		t.Helper()
		if _, ok := h.ui.Find("Remove batch7remove"); !ok {
			if err := h.ui.Click("Repository Switcher"); err != nil {
				t.Fatalf("open Repository Switcher: %v", err)
			}
			h.ui.Frame()
		}
		if err := h.ui.Click("Remove batch7remove"); err != nil {
			t.Fatalf("click Remove batch7remove: %v\ntexts=%q", err, h.ui.Texts())
		}
		h.ui.Frame()
		requireDialog(t, h, "Cannot Remove Repository")
	}

	openRemoveError()
	dlg := requireDialog(t, h, "Cannot Remove Repository")
	if names := dialogButtonsIn(t, h, dlg); len(names) != 1 || names[0] != "Close" {
		t.Fatalf("buttons inside the notice = %v, want exactly [Close]", names)
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Close"); !ok || !btn.Focused {
		t.Fatalf("Close at open = %+v found=%v, want present and focused", btn, ok)
	}

	// Enter presses the focused Close: the notice clears and stays cleared —
	// the close runs once — and the entry the failed removal belongs to
	// survives.
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if got := h.app.reposUI().removeErr; got != "" {
		t.Fatalf("remove error survived Enter: %q", got)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Cannot Remove Repository"); ok {
		t.Fatal("the notice is still open after Enter")
	}
	h.ui.Frame()
	if got := h.app.reposUI().removeErr; got != "" {
		t.Fatalf("the notice reopened a frame after Enter cleared it: %q", got)
	}
	if repos := h.store.Repos(); len(repos) != 1 {
		t.Fatalf("store after the closed notice = %+v, want the entry kept", repos)
	}

	// Reopened, Escape clears the notice the same way.
	openRemoveError()
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	if got := h.app.reposUI().removeErr; got != "" {
		t.Fatalf("remove error survived Escape: %q", got)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Cannot Remove Repository"); ok {
		t.Fatal("the notice is still open after Escape")
	}
	if repos := h.store.Repos(); len(repos) != 1 {
		t.Fatalf("store after Escape = %+v, want the entry kept", repos)
	}
}

// Enter presses the focused Create Repository Here of the not-a-git notice
// exactly as its click does. While the create is held in flight both buttons
// are disabled, Enter starts nothing further, and Escape cannot leave the
// notice; releasing the worker finishes the create, closes the notice and
// opens the initialized repository.
func TestDialogBatch7NotGitEnterCreatesAndEscapeWaits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plainfolder")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir folder: %v", err)
	}
	stubPickLocalFolder(t, func() (string, error) { return dir, nil })

	h := newTestApp(t)
	closeSync(t, h)
	mustClick(t, h, "Repository Switcher")
	h.ui.Frame()
	if err := h.ui.Click("Add Local Repository…"); err != nil {
		t.Fatalf("click Add Local Repository: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.drain()

	requireDialog(t, h, "Not a Git Repository")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Repository Here"); !ok || !btn.Focused {
		t.Fatalf("Create Repository Here at open = %+v found=%v, want present and focused", btn, ok)
	}

	// Enter starts the create exactly once: the one worker the queue holds.
	h.queue.PauseWorkers()
	defer h.queue.ResumeWorkers()
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	worker := h.queue.WaitWorker(t)
	if !h.ui.HasText("Creating…") {
		t.Fatalf("busy indicator missing mid-create; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Close"); !ok || !btn.Disabled {
		t.Fatalf("Close mid-create = %+v found=%v, want present and disabled", btn, ok)
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Repository Here"); !ok || !btn.Disabled {
		t.Fatalf("Create Repository Here mid-create = %+v found=%v, want disabled", btn, ok)
	}

	// Enter mid-create starts nothing more: the queue holds no second worker.
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.queue.mu.Lock()
	extra := len(h.queue.gated)
	h.queue.mu.Unlock()
	if extra != 0 {
		t.Fatalf("Enter mid-create spawned %d further worker(s), want none", extra)
	}

	// Escape mid-create cannot leave the notice: the disabled Close locks
	// every way out for as long as the create runs.
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Not a Git Repository"); !ok {
		t.Fatal("Escape closed the notice while the create was in flight")
	}
	if !h.app.reposUI().notGitOpen {
		t.Fatal("the notice state closed while the create was in flight")
	}

	// Released, the create finishes as usual: the notice closes and the
	// initialized folder opens.
	h.queue.ReleaseWorker(worker)
	h.queue.ResumeWorkers()
	h.drain()

	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Not a Git Repository"); ok {
		t.Fatal("the notice stayed open after the finished create")
	}
	want := resolvedPath(t, dir)
	if got := initBranchInApp(t, want); got != "refs/heads/main" {
		t.Fatalf("HEAD = %q, want refs/heads/main", got)
	}
	if !h.app.session.HasRepo() {
		t.Fatal("no repository open after the Enter create")
	}
	if meta := h.app.session.Meta(); meta.Path != want || meta.Kind != store.Local {
		t.Fatalf("session meta = %+v, want local repo at %q", meta, want)
	}
	if repos := h.store.Repos(); len(repos) != 1 || repos[0].Path != want {
		t.Fatalf("store repos = %+v, want the one created entry at %q", repos, want)
	}
}

// The Tag Details panel carries a single Close button which opens with the
// focus on it and works while the read is still in flight — canceling the
// read so its late answer never repopulates a closed panel — and Enter and
// Escape close the panel as well.
func TestDialogBatch7TagDetailsSingleButtonClose(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "batch7tagdet")

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// The read held in flight: Close is there, enabled and focused, and
	// clicking it closes the panel and cancels the read.
	h.queue.PauseWorkers()
	defer h.queue.ResumeWorkers()
	chooseTagMenuItem(t, h, "lw", "Show Tag Details…", "Tag Details")
	reader := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.ui.HasText("Loading tag details") {
		t.Fatalf("loading note missing while the read is held; texts=%q", h.ui.Texts())
	}
	dlg := requireDialog(t, h, "Tag Details")
	if names := dialogButtonsIn(t, h, dlg); len(names) != 1 || names[0] != "Close Tag Details" {
		t.Fatalf("buttons inside Tag Details = %v, want exactly [Close Tag Details]", names)
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Close Tag Details"); !ok || !btn.Focused || btn.Disabled {
		t.Fatalf("Close Tag Details while loading = %+v found=%v, want focused and enabled", btn, ok)
	}
	if err := h.ui.Click("Close Tag Details"); err != nil {
		t.Fatalf("click Close Tag Details while loading: %v", err)
	}
	h.ui.Frame()
	if h.app.tagsMenuUI().detOpen {
		t.Fatal("Close while loading left the panel open")
	}
	h.queue.ReleaseWorker(reader)
	h.queue.ResumeWorkers()
	h.drain()
	if st := h.app.tagsMenuUI(); st.detOpen || st.detTag != "" || st.detChecking {
		t.Fatalf("the canceled read repopulated the closed panel: %+v", st)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Tag Details"); ok {
		t.Fatal("the panel reappeared with the late answer of the canceled read")
	}

	// Enter presses the focused Close of the loaded panel: it closes and
	// stays closed — the close runs once.
	chooseTagMenuItem(t, h, "lw", "Show Tag Details…", "Tag Details")
	h.drain()
	if !h.ui.HasText("Lightweight tag") {
		t.Fatalf("details did not load; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Close Tag Details"); !ok || !btn.Focused {
		t.Fatalf("Close Tag Details of the loaded panel = %+v found=%v, want focused", btn, ok)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if h.app.tagsMenuUI().detOpen {
		t.Fatal("Enter did not close the Tag Details panel")
	}
	h.ui.Frame()
	if h.app.tagsMenuUI().detOpen {
		t.Fatal("the Tag Details panel reopened a frame after Enter closed it")
	}

	// Escape closes it the same way.
	chooseTagMenuItem(t, h, "lw", "Show Tag Details…", "Tag Details")
	h.drain()
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	if h.app.tagsMenuUI().detOpen {
		t.Fatal("Escape did not close the Tag Details panel")
	}
}

// The Add SSH dialog's footer is Cancel, Test Connection and Add left to
// right inside the panel, the Address field opens with the focus, Enter in
// it with an empty address does nothing, Test Connection only tests — it
// saves and opens nothing — and Enter with a valid address adds exactly like
// the Add click, exactly once.
func TestDialogBatch7AddSSHLayoutFocusAndEnterLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	wantHead := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")
	addr := fmt.Sprintf("ssh://%s@%s:%d%s", srv.User, srv.Host, srv.Port, repo.Path)

	h := newTestApp(t)
	closeSync(t, h)
	openSSHForm(t, h)

	dlg := requireDialog(t, h, "Add SSH Repository…")
	names := dialogButtonsIn(t, h, dlg)
	want := []string{"Cancel", "Test Connection", "Add"}
	if !slices.Equal(names, want) {
		t.Fatalf("footer buttons left to right = %v, want %v", names, want)
	}
	if field, ok := h.ui.AccessByRole(ui.RoleTextField, "Address"); !ok || !field.Focused {
		t.Fatalf("Address field at open = %+v found=%v, want present and focused", field, ok)
	}

	// Enter with an empty address: nothing happens — no check, no error, no
	// store entry.
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()
	if st := sshFormState(h); st.sshBusy || st.sshResult != "" || st.sshFailed {
		t.Fatalf("Enter on the empty address started something: busy %v result %q failed %v", st.sshBusy, st.sshResult, st.sshFailed)
	}
	if !sshFormState(h).sshOpen {
		t.Fatal("Enter on the empty address closed the dialog")
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("Enter on the empty address saved a repository: %+v", h.store.Repos())
	}

	// Test Connection reports success without saving or opening anything.
	typeSSHForm(t, h, addr, srv.Identity, "")
	h.ui.Frame()
	if err := h.ui.Click("Test Connection"); err != nil {
		t.Fatalf("click Test Connection: %v", err)
	}
	h.ui.Frame()
	h.drain()
	st := sshFormState(h)
	if st.sshFailed {
		t.Fatalf("Test Connection failed: %q", st.sshResult)
	}
	if !strings.Contains(st.sshResult, "Connected:") || !strings.Contains(st.sshResult, repo.Path) {
		t.Fatalf("Test Connection result = %q, want Connected with path %q", st.sshResult, repo.Path)
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("Test Connection saved a repository: %+v", h.store.Repos())
	}
	if h.app.session.HasRepo() {
		t.Fatal("Test Connection opened a repository")
	}
	if !st.sshOpen {
		t.Fatal("Test Connection closed the dialog")
	}

	// Enter in the Address field is the Add click: the repository is saved
	// and opened exactly once.
	focusField(t, h, "Address")
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	st = sshFormState(h)
	if st.sshOpen {
		t.Fatalf("dialog stayed open after the Enter add, result %q", st.sshResult)
	}
	if !h.app.session.HasRepo() || h.app.session.Meta().Kind != store.SSH {
		t.Fatalf("Enter did not open the SSH repository: %+v", h.app.session.Meta())
	}
	if got := h.app.session.Meta().Path; got != repo.Path {
		t.Fatalf("added path = %q, want %q", got, repo.Path)
	}
	if got := h.app.session.Snapshot().Status.Head; got != wantHead {
		t.Fatalf("added repo HEAD = %q, want %q", got, wantHead)
	}
	if repos := h.store.Repos(); len(repos) != 1 {
		t.Fatalf("store after the Enter add = %+v, want exactly one entry", repos)
	}
}

// Escape and Cancel of the Add SSH dialog both invalidate the connection
// check in flight: the answer of the discarded generation never publishes
// into the reopened dialog, and nothing is saved or opened.
func TestDialogBatch7AddSSHEscapeAndCancelDiscardInFlightLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	addr := fmt.Sprintf("ssh://%s@%s:%d%s", srv.User, srv.Host, srv.Port, repo.Path)

	h := newTestApp(t)
	closeSync(t, h)

	// discard drives one pass: open the dialog, start a check behind the
	// worker gate, close the dialog with closeFn, then let the answer arrive
	// in the reopened dialog — where nothing of it may appear.
	discard := func(closeFn func()) {
		t.Helper()
		openSSHForm(t, h)
		typeSSHForm(t, h, addr, srv.Identity, "")
		h.ui.Frame()

		h.queue.PauseWorkers()
		if err := h.ui.Click("Test Connection"); err != nil {
			t.Fatalf("click Test Connection: %v", err)
		}
		h.ui.Frame()
		worker := h.queue.WaitWorker(t)
		closeFn()
		h.ui.Frame()
		h.queue.ResumeWorkers()
		if sshFormState(h).sshOpen {
			t.Fatal("the close left the dialog open")
		}

		openSSHForm(t, h)
		if got := sshFormState(h).sshAddress; got != "" {
			t.Fatalf("reopened dialog kept the old address %q", got)
		}
		h.queue.ReleaseWorker(worker)
		h.drain()
		st := sshFormState(h)
		if st.sshResult != "" {
			t.Fatalf("in-flight answer published into the reopened dialog: %q", st.sshResult)
		}
		if st.sshBusy {
			t.Fatal("reopened dialog stayed busy")
		}
		if len(h.store.Repos()) != 0 {
			t.Fatalf("discarded check saved a repository: %+v", h.store.Repos())
		}
		if h.app.session.HasRepo() {
			t.Fatal("discarded check opened a repository")
		}
		closeDialog(t, h)
	}

	discard(func() { h.ui.Key(0, ui.KeyEscape) })
	discard(func() {
		if err := h.ui.Click("Cancel"); err != nil {
			t.Fatalf("click Cancel: %v", err)
		}
	})
}

// Enter in the Clone dialog's URL field is the Clone click exactly once —
// one git clone invocation, never two — and with the URL still empty it does
// nothing at all.
func TestDialogBatch7CloneEnterSubmitsOnce(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "up.git")
	wantHead := newBareCloneSource(t, "up", bare)
	parent := filepath.Join(root, "code")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	// git runs through a logging wrapper so the clone count is exact.
	probeDir := t.TempDir()
	gitLog := filepath.Join(probeDir, "git-log.txt")
	if err := os.WriteFile(gitLog, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git on PATH: %v", err)
	}

	h := newTestApp(t)
	closeSync(t, h)
	useFakeGitPath(t, h, loggingShim(t, probeDir, "wrapped git", realGit, gitLog))

	openCloneDialog(t, h)
	requireDialog(t, h, "Clone Repository")
	if field, ok := h.ui.AccessByRole(ui.RoleTextField, "Repository URL"); !ok || !field.Focused {
		t.Fatalf("Repository URL field at open = %+v found=%v, want present and focused", field, ok)
	}

	// Enter with the URL empty: no clone starts.
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()
	if got := gitCloneInvocations(t, gitLog); got != 0 {
		t.Fatalf("%d git clone invocations after Enter on the empty URL, want 0", got)
	}
	if st := h.app.cloneUI(); !st.open || st.busy {
		t.Fatalf("Enter on the empty URL closed or busied the dialog: %+v", st)
	}

	// A valid draft, Enter in the URL field: exactly one clone runs and the
	// result opens.
	h.ui.Type(bare)
	h.ui.Frame()
	replaceField(t, h, "Parent Folder", parent)
	focusField(t, h, "Repository URL")
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	if st := h.app.cloneUI(); st.open || st.err != "" {
		t.Fatalf("clone by Enter failed or the dialog stayed open: err %q", st.err)
	}
	if got := gitCloneInvocations(t, gitLog); got != 1 {
		t.Fatalf("%d git clone invocations after one Enter, want exactly 1", got)
	}
	target := filepath.Join(parent, "up")
	if got := strings.TrimSpace(runGitCommand(t, target, "rev-parse", "HEAD")); got != wantHead {
		t.Fatalf("clone HEAD = %q, want %q", got, wantHead)
	}
	if meta := h.app.session.Meta(); meta.Path != target || meta.Kind != store.Local {
		t.Fatalf("session meta = %+v, want local repo at %q", meta, target)
	}
	if repos := h.store.Repos(); len(repos) != 1 {
		t.Fatalf("store repos = %+v, want exactly one entry", repos)
	}
}

// Escape mid-clone stops the running clone exactly as Cancel does: the
// dialog closes, the fake git is reaped and the folder the clone started is
// removed, with nothing saved or opened.
func TestDialogBatch7CloneEscapeCancelsRunningClone(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "code")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	pidFile := filepath.Join(parent, "clone.pid")
	fake := writeBlockingFakeGit(t, root, pidFile)

	h := newTestApp(t)
	closeSync(t, h)
	useFakeGitPath(t, h, fake)

	openCloneDialog(t, h)
	h.ui.Type("https://example.com/slow/repo.git")
	h.ui.Frame()
	replaceField(t, h, "Parent Folder", parent)
	if err := h.ui.Click("Clone"); err != nil {
		t.Fatalf("click Clone: %v", err)
	}
	h.ui.Frame()
	target := filepath.Join(parent, "repo")
	waitForFile(t, pidFile)
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the mid-flight clone has no target folder %s: %v", target, err)
	}
	if !h.ui.HasText("Cloning…") {
		t.Fatalf("busy indicator missing mid-clone; texts=%q", h.ui.Texts())
	}

	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	h.drain()

	if h.app.cloneUI().open {
		t.Fatal("Escape left the dialog open mid-clone")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("the canceled clone left its folder: err %v", err)
	}
	assertProcessGone(t, readNumericPID(t, pidFile))
	if len(h.store.Repos()) != 0 {
		t.Fatalf("store gained an entry: %+v", h.store.Repos())
	}
	if h.app.session.HasRepo() {
		t.Fatal("a repository opened from the canceled clone")
	}
}
