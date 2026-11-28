package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// menuGateIDs are the twenty menu items the dialog gate of the main window
// manages: the File menu's four entries, the View menu's page items and
// Refresh, and the whole Repository menu. Settings… and the platform's own
// items are not among them.
var menuGateIDs = []string{
	menuIDNewRepository,
	menuIDClone,
	menuIDAddLocalRepository,
	menuIDAddSSHRepository,
	menuIDLocalChanges,
	menuIDHistory,
	menuIDRefresh,
	menuIDFetch,
	menuIDQuickFetch,
	menuIDPull,
	menuIDQuickPull,
	menuIDPush,
	menuIDQuickPush,
	menuIDSaveStash,
	menuIDQuickStash,
	menuIDNewBranch,
	menuIDNewTag,
	menuIDShowInFinder,
	menuIDOpenInTerminal,
	menuIDOpenInEditor,
}

// menuGateStates copies the last applied enabled state of every gated item,
// so a test can compare the states of two moments exactly.
func menuGateStates(rig *menuBarRig) map[string]bool {
	out := map[string]bool{}
	for _, id := range menuGateIDs {
		out[id] = rig.states[id]
	}
	return out
}

// assertMenuGateDisabled requires every gated item disabled in the rig's
// last applied states, and the exempt Settings… item neither disabled nor
// ever synced — it is no entry of the enabled-state table at all.
func assertMenuGateDisabled(t *testing.T, rig *menuBarRig) {
	t.Helper()
	for _, id := range menuGateIDs {
		state, applied := rig.states[id]
		if !applied {
			t.Errorf("item %q was never synced to the menu; the test proves nothing", id)
		} else if state {
			t.Errorf("item %q enabled with a dialog open", id)
		}
	}
	if _, applied := rig.states[menuIDSettings]; applied {
		t.Error("Settings… was synced by the enabled-state table; it must stay out of it")
	}
	if it := menuItem(t, rig, menuIDSettings); !it.IsEnabled() {
		t.Error("Settings… disabled with a dialog open")
	}
}

// openPushDialogFromToolbar clicks the toolbar Push button — a dialog the
// window itself opens, not one a menu click opened — and requires the Push
// dialog open.
func openPushDialogFromToolbar(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Push"); err != nil {
		t.Fatalf("click Push: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if !pushDialogUI(h.app).open {
		t.Fatalf("the toolbar Push button did not open the Push dialog; texts=%q", h.ui.Texts())
	}
}

// escapeCloses presses Escape and requires the checker's dialog closed.
func escapeCloses(t *testing.T, h *testHarness, closed func() bool) {
	t.Helper()
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	if !closed() {
		t.Fatalf("Escape did not close the dialog; texts=%q", h.ui.Texts())
	}
}

// A dialog the window itself opens — here the toolbar's Push dialog —
// disables the twenty managed menu items in the rig's applied states while
// leaving Settings… alone, the clicks of Fetch…, New Branch… and Clone…
// open nothing on top of it, a Quick Fetch starts no git command, and once
// Escape closes the dialog every item returns to exactly the state it had
// before the dialog opened.
func TestMenuBarGateWindowDialogDisablesMenuAndClicks(t *testing.T) {
	repo := newGitRepo(t, "menugate-window")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	rig := installMenuBar(h)
	openSyncRepo(t, h, repo.StoreRepo())
	rec := installRecordingExecutor(t, h)
	h.ui.Frame()
	baseline := menuGateStates(rig)

	openPushDialogFromToolbar(t, h)
	assertMenuGateDisabled(t, rig)

	clickMenuItem(t, rig, menuIDFetch)
	if syncStateFor(h.app).fetchDlg.open {
		t.Fatal("Fetch… opened its dialog on top of the Push dialog")
	}
	clickMenuItem(t, rig, menuIDNewBranch)
	if h.app.branchCreateUI().open {
		t.Fatal("New Branch… opened its dialog on top of the Push dialog")
	}
	clickMenuItem(t, rig, menuIDClone)
	if h.app.cloneUI().open {
		t.Fatal("Clone… opened its dialog on top of the Push dialog")
	}
	fetches := rec.count("fetch")
	clickMenuItem(t, rig, menuIDQuickFetch)
	h.drain()
	if got := rec.count("fetch"); got != fetches {
		t.Fatalf("Quick Fetch ran %d fetch command(s) with a dialog open, want none", got-fetches)
	}
	if h.app.session.Busy() {
		t.Fatal("a menu click started an operation with a dialog open")
	}
	if !pushDialogUI(h.app).open {
		t.Fatal("the Push dialog did not survive the gated clicks")
	}

	escapeCloses(t, h, func() bool { return !pushDialogUI(h.app).open })
	after := menuGateStates(rig)
	for _, id := range menuGateIDs {
		if after[id] != baseline[id] {
			t.Errorf("item %q enabled = %v after the dialog closed, want the pre-dialog state %v", id, after[id], baseline[id])
		}
	}
}

// A dialog opened from the menu itself — New Branch… — disables the menu
// exactly like one the window opened, and closing it restores every item.
func TestMenuBarGateMenuOpenedDialogDisablesMenu(t *testing.T) {
	repo := newGitRepo(t, "menugate-menu")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	rig := installMenuBar(h)
	openSyncRepo(t, h, repo.StoreRepo())
	h.ui.Frame()
	baseline := menuGateStates(rig)

	clickMenuItem(t, rig, menuIDNewBranch)
	h.ui.Frame()
	if !h.app.branchCreateUI().open {
		t.Fatal("New Branch… did not open the dialog")
	}
	assertMenuGateDisabled(t, rig)

	escapeCloses(t, h, func() bool { return !h.app.branchCreateUI().open })
	after := menuGateStates(rig)
	for _, id := range menuGateIDs {
		if after[id] != baseline[id] {
			t.Errorf("item %q enabled = %v after the dialog closed, want the pre-dialog state %v", id, after[id], baseline[id])
		}
	}
}

// The menu disables in the very frame that first draws the dialog: opening
// the Push dialog and rendering exactly one frame already shows every
// managed item disabled in the applied states. Syncing the menu before the
// frame builds — as view once did — would read the count one frame late,
// and this frame would apply nothing.
func TestMenuBarGateDisablesOnFirstDialogFrame(t *testing.T) {
	repo := newGitRepo(t, "menugate-firstframe")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	rig := installMenuBar(h)
	openSyncRepo(t, h, repo.StoreRepo())

	// The measurement tester counts the frames it renders, so the test can
	// require the items disabled after exactly the first frame with the
	// dialog in it rather than after however many frames a settle ran.
	frames := 0
	view := instantView(h.app.view)
	measure := ui.NewTester(func(c *ui.Context) {
		frames++
		view(c)
	}, 1200, 800)

	h.app.openPushDialog()
	if !pushDialogUI(h.app).open {
		t.Fatal("fixture: the Push dialog did not open")
	}
	before := frames
	measure.Frame()
	if ran := frames - before; ran != 1 {
		t.Fatalf("the first dialog frame ran as %d frames, want exactly 1 — the test cannot pin the first frame otherwise", ran)
	}
	assertMenuGateDisabled(t, rig)
}

// A dialog of the Settings window — the New SSH Key dialog — does not count
// against the main window's menu: with it open, a frame of the main window
// leaves every managed item in the state it had without the dialog.
func TestMenuBarGateSettingsWindowDialogDoesNotCount(t *testing.T) {
	isolateSSHKeysTest(t)

	repo := newGitRepo(t, "menugate-settings")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	rig := installMenuBar(h)
	openSyncRepo(t, h, repo.StoreRepo())
	h.ui.Frame()
	baseline := menuGateStates(rig)

	tt := openSSHKeysPage(t, h)
	if err := tt.Click("New SSH Key"); err != nil {
		t.Fatalf("click New SSH Key…: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if !sshKeysStateOf(h.app).newOpen {
		t.Fatalf("the New SSH Key dialog did not open; texts=%q", tt.Texts())
	}

	h.ui.Frame()
	if h.app.dialogOpen {
		t.Fatal("the main window's dialog gate tripped on a Settings window dialog")
	}
	after := menuGateStates(rig)
	for _, id := range menuGateIDs {
		if after[id] != baseline[id] {
			t.Errorf("item %q enabled = %v with a Settings dialog open, want the state without it %v", id, after[id], baseline[id])
		}
	}
}

// Without a repository the File menu's four entries are enabled, the Clone
// dialog opening disables them, and closing it enables them again.
func TestMenuBarGateFileItemsWithoutRepository(t *testing.T) {
	h := newTestApp(t)
	rig := installMenuBar(h)
	h.ui.Frame()

	fileItems := []string{menuIDNewRepository, menuIDClone, menuIDAddLocalRepository, menuIDAddSSHRepository}
	for _, id := range fileItems {
		if !rig.states[id] {
			t.Fatalf("item %q not enabled without a repository before any dialog", id)
		}
	}

	clickMenuItem(t, rig, menuIDClone)
	h.ui.Frame()
	if !h.app.cloneUI().open {
		t.Fatal("Clone… did not open its dialog")
	}
	assertMenuGateDisabled(t, rig)

	escapeCloses(t, h, func() bool { return !h.app.cloneUI().open })
	for _, id := range fileItems {
		if !rig.states[id] {
			t.Errorf("item %q not enabled again after the Clone dialog closed", id)
		}
	}
}

// Opening and closing a dialog three times applies the enabled state only
// to the items that change: with every managed item enabled at rest, each
// open applies each item exactly once (to disabled), each close once more
// (back to enabled), and quiet frames in between apply nothing.
func TestMenuBarGateAppliesOnlyChangedItems(t *testing.T) {
	repo := newGitRepo(t, "menugate-apply")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")
	// A working-tree change keeps Quick Stash enabled, so every managed
	// item is enabled at rest and each cycle flips all twenty.
	repo.WriteFile("a.txt", "one\ntwo\n")

	h := newTestApp(t)
	rig := installMenuBar(h)
	openSyncRepo(t, h, repo.StoreRepo())
	h.ui.Frame()
	for _, id := range menuGateIDs {
		state, applied := rig.states[id]
		if !applied {
			t.Fatalf("item %q was never synced to the menu; the counting below would prove nothing", id)
		}
		if !state {
			t.Fatalf("fixture: item %q not enabled at rest; the cycle counts would not flip it", id)
		}
	}

	for cycle := 1; cycle <= 3; cycle++ {
		rig.resetCounters()
		openPushDialogFromToolbar(t, h)
		for _, id := range menuGateIDs {
			if rig.applied[id] != 1 {
				t.Errorf("cycle %d: opening the dialog applied item %q %d time(s), want exactly 1", cycle, id, rig.applied[id])
			}
		}
		if n := len(rig.applied); n != len(menuGateIDs) {
			t.Errorf("cycle %d: opening the dialog applied %d distinct item(s), want %d; %v", cycle, n, len(menuGateIDs), rig.applied)
		}

		rig.resetCounters()
		h.ui.Frame()
		if n := rig.totalApplications(); n != 0 {
			t.Errorf("cycle %d: a quiet frame with the dialog open applied the enabled state %d time(s), want 0", cycle, n)
		}

		rig.resetCounters()
		escapeCloses(t, h, func() bool { return !pushDialogUI(h.app).open })
		for _, id := range menuGateIDs {
			if rig.applied[id] != 1 {
				t.Errorf("cycle %d: closing the dialog applied item %q %d time(s), want exactly 1", cycle, id, rig.applied[id])
			}
		}
		if n := len(rig.applied); n != len(menuGateIDs) {
			t.Errorf("cycle %d: closing the dialog applied %d distinct item(s), want %d; %v", cycle, n, len(menuGateIDs), rig.applied)
		}
		for _, id := range menuGateIDs {
			if !rig.states[id] {
				t.Errorf("cycle %d: item %q not enabled after the dialog closed", cycle, id)
			}
		}

		rig.resetCounters()
		h.ui.Frame()
		if n := rig.totalApplications(); n != 0 {
			t.Errorf("cycle %d: a quiet frame after the dialog closed applied the enabled state %d time(s), want 0", cycle, n)
		}
	}
}
