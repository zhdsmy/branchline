package app

import (
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// repositoryMenu returns the items of the Repository menu of the rig.
func repositoryMenu(t *testing.T, rig *menuBarRig) []*mygo.MenuItem {
	t.Helper()
	for _, it := range rig.menu.Items() {
		if it.Label == "Repository" {
			return it.Submenu
		}
	}
	t.Fatal("no Repository menu in the menu bar")
	return nil
}

// menuItemIndex is the position of the item with id in items, or -1.
func menuItemIndex(items []*mygo.MenuItem, id string) int {
	for i, it := range items {
		if it.ID == id {
			return i
		}
	}
	return -1
}

// isSeparator reports whether the item is a menu separator.
func isSeparator(it *mygo.MenuItem) bool {
	return it.Type == mygo.MenuItemSeparator
}

// The Repository menu holds Save Stash… and Quick Stash in their own group
// right after Quick Push, separated from the New Branch… group, and Open in
// Editor immediately after Open in Terminal — each with its ID, label,
// accelerator and a Click handler.
func TestMenuBarStashEditorLayoutAndAccelerators(t *testing.T) {
	h := newTestApp(t)
	rig := installMenuBar(h)

	items := repositoryMenu(t, rig)
	quickPush := menuItemIndex(items, menuIDQuickPush)
	saveStash := menuItemIndex(items, menuIDSaveStash)
	quickStash := menuItemIndex(items, menuIDQuickStash)
	newBranch := menuItemIndex(items, menuIDNewBranch)
	for name, idx := range map[string]int{
		"Quick Push":  quickPush,
		"Save Stash…": saveStash,
		"Quick Stash": quickStash,
		"New Branch…": newBranch,
	} {
		if idx < 0 {
			t.Fatalf("no %s item in the Repository menu", name)
		}
	}
	if saveStash != quickPush+2 || !isSeparator(items[quickPush+1]) {
		t.Errorf("Save Stash… does not follow Quick Push after a separator: quick-push %d, save-stash %d", quickPush, saveStash)
	}
	if quickStash != saveStash+1 {
		t.Errorf("Quick Stash is not immediately after Save Stash…: save-stash %d, quick-stash %d", saveStash, quickStash)
	}
	if newBranch != quickStash+2 || !isSeparator(items[quickStash+1]) {
		t.Errorf("New Branch… does not follow Quick Stash after a separator: quick-stash %d, new-branch %d", quickStash, newBranch)
	}

	terminal := menuItemIndex(items, menuIDOpenInTerminal)
	editor := menuItemIndex(items, menuIDOpenInEditor)
	if terminal < 0 || editor != terminal+1 {
		t.Errorf("Open in Editor is not immediately after Open in Terminal: terminal %d, editor %d", terminal, editor)
	}

	want := []struct {
		id    string
		label string
		accel string
	}{
		{menuIDSaveStash, "Save Stash…", "Shift+CmdOrCtrl+H"},
		{menuIDQuickStash, "Quick Stash", "Alt+Shift+CmdOrCtrl+H"},
		{menuIDOpenInEditor, "Open in Editor", ""},
	}
	for _, w := range want {
		it := menuItem(t, rig, w.id)
		if it.Label != w.label {
			t.Errorf("item %q label = %q, want %q", w.id, it.Label, w.label)
		}
		if it.Accelerator != w.accel {
			t.Errorf("item %q accelerator = %q, want %q", w.id, it.Accelerator, w.accel)
		}
		if it.Click == nil {
			t.Errorf("item %q has no Click handler", w.id)
		}
	}
}

// Choosing Save Stash… from the menu opens exactly the sidebar's Save Stash
// dialog with its defaults — a blank message, untracked files included, the
// whole working tree — and starts no stash.
func TestMenuBarStashSaveOpensDialogWithDefaults(t *testing.T) {
	repo := newGitRepo(t, "menustash-save")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.WriteFile("a.txt", "one\ntwo\n")

	h := newTestApp(t)
	rig := installMenuBar(h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickMenuItem(t, rig, menuIDSaveStash)
	h.ui.Frame()

	st := h.app.stashUI()
	if !st.saveOpen {
		t.Fatal("Save Stash… did not open the Save Stash dialog")
	}
	if !h.ui.HasText("Stash message") {
		t.Fatalf("the Save Stash dialog is not visible; texts=%q", h.ui.Texts())
	}
	if st.saveMessage != "" {
		t.Errorf("dialog message = %q, want blank", st.saveMessage)
	}
	if !st.saveUntracked {
		t.Error("Include untracked files is unticked after opening from the menu")
	}
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Include untracked files"); !ok || !box.Checked {
		t.Errorf("Include untracked files checkbox = %+v (found=%v), want ticked", box, ok)
	}
	if st.saveScope != stashScopeAll {
		t.Errorf("dialog scope = %d, want All changes (%d)", st.saveScope, stashScopeAll)
	}
	if _, ok := saveStashCheck(t, h, "a.txt"); ok {
		t.Error("a file checkbox of the Selected files scope shows although the dialog opened on All changes")
	}
	if n := stashCount(t, repo.Path()); n != 0 {
		t.Errorf("stash count after opening the dialog = %d, want 0", n)
	}
	if ran := h.queue.Drain(t); ran != 0 {
		t.Errorf("opening the dialog posted %d worker callback(s), want 0", ran)
	}
}

// After a Save Stash dialog opened from a file's Stash Changes… menu — the
// Selected files scope — is closed, Save Stash… opens the dialog back on the
// whole working tree.
func TestMenuBarStashSaveOpensAllChangesAfterSelected(t *testing.T) {
	repo := setupSelectedStashRepo(t, "menustash-scope")

	h := newTestApp(t)
	rig := installMenuBar(h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseFileMenuItem(t, h, "Unstaged c.txt", "Stash Changes…")
	h.ui.Frame()
	st := h.app.stashUI()
	if !st.saveOpen || st.saveScope != stashScopeSelected {
		t.Fatalf("the file menu did not open the dialog on Selected files: open=%v scope=%d", st.saveOpen, st.saveScope)
	}
	if box := mustSaveStashCheck(t, h, "c.txt"); !box.Checked {
		t.Fatal("checkbox of c.txt is unchecked after opening from its menu")
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if st.saveOpen {
		t.Fatal("the dialog stayed open after Cancel")
	}

	clickMenuItem(t, rig, menuIDSaveStash)
	h.ui.Frame()
	if !st.saveOpen {
		t.Fatal("Save Stash… did not reopen the dialog")
	}
	if st.saveScope != stashScopeAll {
		t.Errorf("dialog scope = %d, want All changes (%d)", st.saveScope, stashScopeAll)
	}
	if _, ok := saveStashCheck(t, h, "c.txt"); ok {
		t.Error("a file checkbox of the Selected files scope shows although the dialog opened on All changes")
	}
	if st.saveMessage != "" || !st.saveUntracked {
		t.Errorf("dialog defaults after reopening: message %q, untracked %v, want blank and ticked", st.saveMessage, st.saveUntracked)
	}
}

// Quick Stash stashes the whole working tree without a dialog: the modified
// tracked file and the untracked file both leave the worktree into one entry
// carrying git's default message.
func TestMenuBarStashQuickStashStashesEverything(t *testing.T) {
	repo := newGitRepo(t, "menustash-quick")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.WriteFile("a.txt", "one\ntwo\n")
	repo.WriteFile("new.txt", "brand new\n")

	h := newTestApp(t)
	rig := installMenuBar(h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if it := menuItem(t, rig, menuIDQuickStash); !it.IsEnabled() {
		t.Fatal("Quick Stash is disabled with changes to stash")
	}
	clickMenuItem(t, rig, menuIDQuickStash)
	if h.app.stashUI().saveOpen {
		t.Fatal("Quick Stash opened the Save Stash dialog")
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Quick Stash recorded an error: %v", err)
	}
	lines := stashListLines(t, repo.Path())
	if len(lines) != 1 {
		t.Fatalf("stash count = %d, want 1", len(lines))
	}
	if !strings.HasPrefix(lines[0], "stash@{0}: ") {
		t.Fatalf("stash list line %q does not start with the reflog name", lines[0])
	}
	if message := strings.TrimPrefix(lines[0], "stash@{0}: "); !strings.HasPrefix(message, "WIP on ") {
		t.Errorf("stash message = %q, want git's default starting with %q", message, "WIP on ")
	}
	shown := runGitCommand(t, repo.Path(), "stash", "show", "--include-untracked", "--name-only", "stash@{0}")
	for _, want := range []string{"a.txt", "new.txt"} {
		if !strings.Contains(shown, want) {
			t.Errorf("git stash show --include-untracked does not list %s: %q", want, shown)
		}
	}
	if data, err := os.ReadFile(repo.Path() + "/a.txt"); err != nil || string(data) != "one\n" {
		t.Errorf("working tree a.txt = %q, err=%v; want the committed one\\n", data, err)
	}
	if _, err := os.Stat(repo.Path() + "/new.txt"); !os.IsNotExist(err) {
		t.Errorf("untracked new.txt is still in the working tree: err=%v", err)
	}
	if n := len(h.app.session.Snapshot().Stashes); n != 1 {
		t.Errorf("sidebar snapshot holds %d stash entries, want 1", n)
	}
}

// Quick Stash is enabled with only untracked files to stash and takes them
// into one entry, leaving no trace in the working tree.
func TestMenuBarStashQuickStashUntrackedOnly(t *testing.T) {
	repo := newGitRepo(t, "menustash-quick-untracked")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.WriteFile("new.txt", "brand new\n")

	h := newTestApp(t)
	rig := installMenuBar(h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if it := menuItem(t, rig, menuIDQuickStash); !it.IsEnabled() {
		t.Fatal("Quick Stash is disabled with only untracked files to stash")
	}
	clickMenuItem(t, rig, menuIDQuickStash)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Quick Stash recorded an error: %v", err)
	}
	lines := stashListLines(t, repo.Path())
	if len(lines) != 1 {
		t.Fatalf("stash count = %d, want 1", len(lines))
	}
	if message := strings.TrimPrefix(lines[0], "stash@{0}: "); !strings.HasPrefix(message, "WIP on ") {
		t.Errorf("stash message = %q, want git's default starting with %q", message, "WIP on ")
	}
	shown := runGitCommand(t, repo.Path(), "stash", "show", "--include-untracked", "--name-only", "stash@{0}")
	if !strings.Contains(shown, "new.txt") {
		t.Errorf("git stash show --include-untracked does not list new.txt: %q", shown)
	}
	if _, err := os.Stat(repo.Path() + "/new.txt"); !os.IsNotExist(err) {
		t.Errorf("untracked new.txt is still in the working tree: err=%v", err)
	}
}

// With a clean working tree Quick Stash is disabled, and choosing it changes
// nothing: no stash, no worker, no error.
func TestMenuBarStashQuickStashDisabledOnCleanTree(t *testing.T) {
	repo := newGitRepo(t, "menustash-clean")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	h := newTestApp(t)
	rig := installMenuBar(h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if it := menuItem(t, rig, menuIDQuickStash); it.IsEnabled() {
		t.Fatal("Quick Stash is enabled on a clean working tree")
	}
	clickMenuItem(t, rig, menuIDQuickStash)
	if ran := h.queue.Drain(t); ran != 0 {
		t.Fatalf("a disabled Quick Stash click posted %d worker callback(s), want 0", ran)
	}
	if n := stashCount(t, repo.Path()); n != 0 {
		t.Fatalf("stash count after a disabled click = %d, want 0", n)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("a disabled click recorded an error: %v", err)
	}
	if h.app.stashUI().saveOpen {
		t.Fatal("a disabled click opened the Save Stash dialog")
	}
}

// While an operation is in flight Quick Stash is disabled, and choosing it
// spawns no second worker.
func TestMenuBarStashQuickStashDisabledWhileBusy(t *testing.T) {
	repo := newGitRepo(t, "menustash-busy")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.WriteFile("a.txt", "one\ntwo\n")

	h := newTestApp(t)
	rig := installMenuBar(h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	h.queue.PauseWorkers()
	clickMenuItem(t, rig, menuIDQuickStash)
	worker := h.queue.WaitWorker(t)
	h.ui.Frame()

	if !h.app.session.Busy() {
		t.Fatal("the quick stash did not start while workers were paused")
	}
	if it := menuItem(t, rig, menuIDQuickStash); it.IsEnabled() {
		t.Error("Quick Stash is enabled while busy")
	}
	clickMenuItem(t, rig, menuIDQuickStash)
	if got := gatedLen(h.queue); got != 0 {
		t.Errorf("the disabled click spawned %d extra worker(s), want 0", got)
	}

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(worker)
	h.drain()
}

// Without a repository Quick Stash is disabled, and choosing it does
// nothing.
func TestMenuBarStashQuickStashDisabledWithoutRepo(t *testing.T) {
	h := newTestApp(t)
	rig := installMenuBar(h)
	h.ui.Frame()

	if it := menuItem(t, rig, menuIDQuickStash); it.IsEnabled() {
		t.Fatal("Quick Stash is enabled without a repository")
	}
	clickMenuItem(t, rig, menuIDQuickStash)
	if ran := h.queue.Drain(t); ran != 0 {
		t.Fatalf("a click without a repository posted %d worker callback(s), want 0", ran)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("a click without a repository recorded an error: %v", err)
	}
}

// The enabled state of Quick Stash follows the working tree: disabled on a
// clean tree, enabled once a change appears, disabled again when it is gone.
func TestMenuBarStashQuickStashEnabledTracksChanges(t *testing.T) {
	repo := newGitRepo(t, "menustash-track")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	h := newTestApp(t)
	rig := installMenuBar(h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if it := menuItem(t, rig, menuIDQuickStash); it.IsEnabled() {
		t.Fatal("Quick Stash is enabled on the initial clean tree")
	}

	repo.WriteFile("a.txt", "one\ntwo\n")
	h.app.session.Reload()
	h.drain()
	if it := menuItem(t, rig, menuIDQuickStash); !it.IsEnabled() {
		t.Fatal("Quick Stash is disabled with a change in the working tree")
	}

	repo.WriteFile("a.txt", "one\n")
	h.app.session.Reload()
	h.drain()
	if it := menuItem(t, rig, menuIDQuickStash); it.IsEnabled() {
		t.Fatal("Quick Stash is still enabled after the change is gone")
	}
}

// Open in Editor on a local repository opens the repository's folder in the
// editor, exactly as the repository row's context menu does.
func TestMenuBarEditorOpensLocalRepository(t *testing.T) {
	first := t.TempDir()
	makeAppDir(t, first, "Cursor")
	stubEditorScan(t, first)

	local := newGitRepo(t, "menueditor-local")
	local.CommitFile("a.txt", "a\n", "commit")

	h := newTestApp(t)
	rig := installMenuBar(h)
	rec := &openEditorRecorder{}
	rec.install(h)

	h.ui.Frame()
	if menuItem(t, rig, menuIDOpenInEditor).IsEnabled() {
		t.Fatal("Open in Editor is enabled without a repository")
	}

	h.openRepo(local.StoreRepo())
	h.drain()
	if !menuItem(t, rig, menuIDOpenInEditor).IsEnabled() {
		t.Fatal("Open in Editor is disabled with a local repository open")
	}
	clickMenuItem(t, rig, menuIDOpenInEditor)
	h.drain()

	calls := rec.recorded(t)
	if len(calls) != 1 || calls[0].target != local.Path() {
		t.Fatalf("open calls = %+v, want exactly one open of %q", calls, local.Path())
	}
}

// Open in Editor is disabled on an SSH repository — a server has no local
// folder the editor could open — and choosing it scans and launches nothing.
func TestMenuBarEditorSSHRepoDisabled(t *testing.T) {
	first := t.TempDir()
	makeAppDir(t, first, "Cursor")
	stub := stubEditorScan(t, first)

	repo, _ := loopbackSSHRepo(t)

	h := newTestApp(t)
	rig := installMenuBar(h)
	rec := &openEditorRecorder{}
	rec.install(h)

	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	if menuItem(t, rig, menuIDOpenInEditor).IsEnabled() {
		t.Fatal("Open in Editor is enabled on an SSH repository")
	}
	clickMenuItem(t, rig, menuIDOpenInEditor)
	h.drain()
	if calls := rec.recorded(t); len(calls) != 0 {
		t.Fatalf("Open in Editor ran for an SSH repository: %+v", calls)
	}
	if got := stub.count(); got != 0 {
		t.Fatalf("the disabled choice scanned for editors %d time(s)", got)
	}
}

// Without a repository Open in Editor is disabled, and choosing it scans and
// launches nothing.
func TestMenuBarEditorDisabledWithoutRepo(t *testing.T) {
	first := t.TempDir()
	makeAppDir(t, first, "Cursor")
	stub := stubEditorScan(t, first)

	h := newTestApp(t)
	rig := installMenuBar(h)
	rec := &openEditorRecorder{}
	rec.install(h)
	h.ui.Frame()

	if menuItem(t, rig, menuIDOpenInEditor).IsEnabled() {
		t.Fatal("Open in Editor is enabled without a repository")
	}
	clickMenuItem(t, rig, menuIDOpenInEditor)
	h.drain()
	if calls := rec.recorded(t); len(calls) != 0 {
		t.Fatalf("Open in Editor ran without a repository: %+v", calls)
	}
	if got := stub.count(); got != 0 {
		t.Fatalf("the disabled choice scanned for editors %d time(s)", got)
	}
}
