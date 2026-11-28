package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

// openEditorRecorder substitutes the run of open for Open in Editor: it
// remembers the application and target of every launch and never starts a
// real editor. fail, when non-nil, is what the launch reports.
type openEditorRecorder struct {
	mu    sync.Mutex
	calls []struct {
		app    string
		target string
	}
	fail error
}

func (r *openEditorRecorder) install(h *testHarness) {
	h.app.reposUI().openEditorAppFn = func(openApp, target string) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, struct {
			app    string
			target string
		}{openApp, target})
		return r.fail
	}
}

func (r *openEditorRecorder) recorded(t *testing.T) []struct {
	app    string
	target string
} {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]struct {
		app    string
		target string
	}(nil), r.calls...)
}

// editorScanStub substitutes the scan for installed editors with one that
// counts its runs and scans the test's own folders through the real scan,
// so the file system rules — directory only, first folder wins, candidate
// order — stay exercised wherever the scan is injected.
type editorScanStub struct {
	mu    sync.Mutex
	calls int
	dirs  []string
}

func stubEditorScan(t *testing.T, dirs ...string) *editorScanStub {
	t.Helper()
	stub := &editorScanStub{dirs: dirs}
	prev := scanInstalledEditors
	scanInstalledEditors = func() []editorApp {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		stub.calls++
		return scanEditorApps(stub.dirs)
	}
	t.Cleanup(func() { scanInstalledEditors = prev })
	return stub
}

func (s *editorScanStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// makeAppDir creates the empty <name>.app directory the scan looks for in
// one of the test's search folders.
func makeAppDir(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name+".app")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	return path
}

// chooseOpenInEditor picks Open in Editor from the context menu of the
// repository row named name, through the switcher's real popup.
func chooseOpenInEditor(t *testing.T, h *testHarness, name string) {
	t.Helper()
	if !h.app.reposUI().popupOpen {
		openSwitcherPopup(t, h)
	}
	rightClickRepoRow(t, h, name)
	if err := h.ui.ChooseMenuItem("Open in Editor"); err != nil {
		t.Fatalf("choose Open in Editor on %q: %v; menu=%q", name, err, h.ui.Menu())
	}
	h.ui.Frame()
}

// openSettingsScanned opens the Settings window on the General page and
// waits for the visit's editor scan to land, so the page shows what the
// scan found.
func openSettingsScanned(t *testing.T, h *testHarness) *ui.Tester {
	t.Helper()
	tt := openSettingsTester(t, h)
	h.queue.Drain(t)
	tt.Frame()
	return tt
}

// The scan reports an editor only when its <name>.app is a directory — a
// plain file of that name does not count — takes the bundle from the first
// search folder holding it, and answers in the order of the candidate list
// rather than the order of the folders.
func TestOpenInEditorScanFindsOnlyDirectories(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	cursorPath := makeAppDir(t, first, "Cursor")
	// A regular file named Zed.app in the first folder is no application.
	if err := os.WriteFile(filepath.Join(first, "Zed.app"), []byte("not a bundle"), 0o644); err != nil {
		t.Fatal(err)
	}
	vscodePath := makeAppDir(t, second, "Visual Studio Code")
	// Cursor again in the second folder: the first folder's bundle wins.
	makeAppDir(t, second, "Cursor")

	got := scanEditorApps([]string{first, second})
	want := []editorApp{
		{name: "Cursor", path: cursorPath},
		{name: "Visual Studio Code", path: vscodePath},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("scanEditorApps = %+v, want %+v", got, want)
	}
}

// Open in Editor on a local repository's row opens the repository's folder
// in the editor the settings name: the first one installed while nothing
// was ever chosen, the saved one while it is still installed, and the
// first one again once the saved editor is gone.
func TestOpenInEditorLocalRepoMenu(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	cursorPath := makeAppDir(t, first, "Cursor")
	vscodePath := makeAppDir(t, second, "Visual Studio Code")
	stubEditorScan(t, first, second)

	local := newGitRepo(t, "edlocal")
	local.CommitFile("a.txt", "a\n", "commit")

	h := newTestApp(t, local.StoreRepo())
	rec := &openEditorRecorder{}
	rec.install(h)

	chooseOpenInEditor(t, h, "edlocal")
	h.drain()

	calls := rec.recorded(t)
	if len(calls) != 1 || calls[0].app != cursorPath || calls[0].target != local.Path() {
		t.Fatalf("open calls = %+v, want exactly one open -a %q %q", calls, cursorPath, local.Path())
	}

	cur := h.store.Settings()
	cur.Editor = "Visual Studio Code"
	if err := h.store.SetSettings(cur); err != nil {
		t.Fatalf("save the editor choice: %v", err)
	}
	h.ui.CloseMenu()
	h.ui.Frame()
	chooseOpenInEditor(t, h, "edlocal")
	h.drain()

	calls = rec.recorded(t)
	if len(calls) != 2 || calls[1].app != vscodePath || calls[1].target != local.Path() {
		t.Fatalf("open calls after choosing Visual Studio Code = %+v, want the second open -a %q %q", calls, vscodePath, local.Path())
	}

	// Zed is saved but not installed: the first editor answers instead.
	cur = h.store.Settings()
	cur.Editor = "Zed"
	if err := h.store.SetSettings(cur); err != nil {
		t.Fatalf("save the uninstalled editor choice: %v", err)
	}
	h.ui.CloseMenu()
	h.ui.Frame()
	chooseOpenInEditor(t, h, "edlocal")
	h.drain()

	calls = rec.recorded(t)
	if len(calls) != 3 || calls[2].app != cursorPath || calls[2].target != local.Path() {
		t.Fatalf("open calls with an uninstalled editor saved = %+v, want the third open -a %q %q", calls, cursorPath, local.Path())
	}
}

// Open in Editor disables on the row of an SSH repository — a server has no
// local folder the editor could open — and choosing it is rejected without
// scanning or launching anything.
func TestOpenInEditorSSHRepoDisabled(t *testing.T) {
	first := t.TempDir()
	makeAppDir(t, first, "Cursor")
	stub := stubEditorScan(t, first)

	local := newGitRepo(t, "edsshlocal")
	local.CommitFile("a.txt", "a\n", "commit")

	h := newTestApp(t, local.StoreRepo(), menuSSHRepo())
	rec := &openEditorRecorder{}
	rec.install(h)

	openSwitcherPopup(t, h)
	rightClickRepoRow(t, h, "sshrepo")
	if err := h.ui.ChooseMenuItem("Open in Editor"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing Open in Editor on an SSH row = %v, want a disabled error", err)
	}
	h.drain()
	if calls := rec.recorded(t); len(calls) != 0 {
		t.Fatalf("Open in Editor ran for an SSH repository: %+v", calls)
	}
	if got := stub.count(); got != 0 {
		t.Fatalf("the disabled choice scanned for editors %d time(s)", got)
	}
}

// With no supported editor installed, Open in Editor explains what it
// accepts instead of launching anything.
func TestOpenInEditorNoneInstalled(t *testing.T) {
	empty := t.TempDir()
	stubEditorScan(t, empty)

	local := newGitRepo(t, "ednolocal")
	local.CommitFile("a.txt", "a\n", "commit")

	h := newTestApp(t, local.StoreRepo())
	rec := &openEditorRecorder{}
	rec.install(h)

	chooseOpenInEditor(t, h, "ednolocal")
	h.drain()

	if calls := rec.recorded(t); len(calls) != 0 {
		t.Fatalf("open ran with no editor installed: %+v", calls)
	}
	if !h.ui.HasText("No supported editor found. Install Cursor, Visual Studio Code, Zed, Sublime Text, BBEdit, TextMate or Xcode.") {
		t.Fatalf("the no-editor toast is missing; texts=%q", h.ui.Texts())
	}
}

// A failure of open surfaces as a toast naming the editor and open's error,
// and never turns the session busy or records an action error: the editor
// launch is no git action.
func TestOpenInEditorOpenFailure(t *testing.T) {
	first := t.TempDir()
	makeAppDir(t, first, "Cursor")
	stubEditorScan(t, first)

	local := newGitRepo(t, "edfailopen")
	local.CommitFile("a.txt", "a\n", "commit")

	h := newTestApp(t, local.StoreRepo())
	h.openRepo(local.StoreRepo())
	h.drain()

	rec := &openEditorRecorder{fail: errors.New("open exploded")}
	rec.install(h)

	chooseOpenInEditor(t, h, "edfailopen")
	h.drain()

	if calls := rec.recorded(t); len(calls) != 1 {
		t.Fatalf("open calls = %+v, want exactly one", calls)
	}
	if !h.ui.HasText("Could not open Cursor: open exploded") {
		t.Fatalf("the failure toast is missing; texts=%q", h.ui.Texts())
	}
	if h.app.session.Busy() {
		t.Fatal("the session turned busy for an editor launch")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("an editor launch recorded an action error: %v", err)
	}
}

// The Editor pop-up of the General settings offers exactly the editors
// installed, persists its choice in the store, and a window opened
// afterwards shows the same choice.
func TestOpenInEditorSettingsChoicePersists(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	makeAppDir(t, first, "Cursor")
	makeAppDir(t, second, "Visual Studio Code")
	stubEditorScan(t, first, second)

	h := newTestApp(t)
	tt := openSettingsScanned(t, h)

	popup, ok := tt.AccessByRole(ui.RolePopUpButton, "Editor")
	if !ok {
		t.Fatalf("no Editor pop-up on the General page; texts=%q", tt.Texts())
	}
	if tt.HasText("No supported editor found.") {
		t.Fatalf("the no-editor note shows although editors are installed; texts=%q", tt.Texts())
	}
	// Nothing was ever chosen: the first installed editor shows.
	if !hasExactText(tt, "Cursor") {
		t.Fatalf("the pop-up does not start on Cursor; texts=%q", tt.Texts())
	}
	if hasExactText(tt, "Visual Studio Code") {
		t.Fatalf("Visual Studio Code shows before it is chosen; texts=%q", tt.Texts())
	}

	// The pop-up's options are exactly the installed editors: Visual Studio
	// Code is one of them while Zed, which is not installed, is not.
	tt.ClickAt(popup.Bounds.X+popup.Bounds.W/2, popup.Bounds.Y+popup.Bounds.H/2)
	tt.Frame()
	if !hasExactText(tt, "Visual Studio Code") {
		t.Fatalf("the open pop-up does not offer Visual Studio Code; texts=%q", tt.Texts())
	}
	for _, absent := range []string{"Zed", "Sublime Text", "BBEdit", "TextMate", "Xcode"} {
		if hasExactText(tt, absent) {
			t.Fatalf("the pop-up offers %q, which is not installed; texts=%q", absent, tt.Texts())
		}
	}
	if err := tt.Click("Visual Studio Code"); err != nil {
		t.Fatalf("choose Visual Studio Code in the Editor pop-up: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()

	if got := h.store.Settings().Editor; got != "Visual Studio Code" {
		t.Fatalf("persisted Editor = %q, want Visual Studio Code", got)
	}

	h.app.closeSettingsWindow()
	h.ui.Key(ui.Cmd, ui.KeyComma)
	h.ui.Frame()
	tt2 := newSettingsTester(t, h)
	h.queue.Drain(t)
	tt2.Frame()
	if !tt2.HasText("Visual Studio Code") {
		t.Fatalf("the reopened settings do not show Visual Studio Code; texts=%q", tt2.Texts())
	}
}

// With no supported editor installed the General page explains instead of
// offering an empty pop-up.
func TestOpenInEditorSettingsNoneInstalled(t *testing.T) {
	empty := t.TempDir()
	stubEditorScan(t, empty)

	h := newTestApp(t)
	tt := openSettingsScanned(t, h)

	if !tt.HasText("No supported editor found.") {
		t.Fatalf("the no-editor note is missing; texts=%q", tt.Texts())
	}
	if _, ok := tt.AccessByRole(ui.RolePopUpButton, "Editor"); ok {
		t.Fatalf("the Editor pop-up shows with no editor installed; texts=%q", tt.Texts())
	}
}

// The scan for installed editors runs once when the General page opens —
// never per frame — and again when the window opens fresh, so a machine's
// editors are looked up on every visit, not on every render.
func TestOpenInEditorScanOncePerSettingsOpen(t *testing.T) {
	first := t.TempDir()
	makeAppDir(t, first, "Cursor")
	stub := stubEditorScan(t, first)

	h := newTestApp(t)
	tt := openSettingsScanned(t, h)

	if got := stub.count(); got != 1 {
		t.Fatalf("scan count after opening General = %d, want 1", got)
	}
	for range 20 {
		tt.Frame()
	}
	if got := stub.count(); got != 1 {
		t.Fatalf("scan count after 20 more frames = %d, want 1: the render scanned per frame", got)
	}

	h.app.closeSettingsWindow()
	h.ui.Key(ui.Cmd, ui.KeyComma)
	h.ui.Frame()
	tt2 := newSettingsTester(t, h)
	h.queue.Drain(t)
	tt2.Frame()
	if got := stub.count(); got != 2 {
		t.Fatalf("scan count after reopening the window = %d, want 2", got)
	}
}
