package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The tests of this file drive the Enter key, the destructive marking and
// the dismissal of the remote-management, batch reference, discard-lines
// and New SSH Key dialogs through the real UI, on real repositories, as
// dialog_enter_test.go does for the sync and branch dialogs. One focus rule
// of the shared frame shapes the expectations here: a destructive dialog
// whose body holds no control opens with the focus on cancel, the first
// focusable control of its footer, so Enter there is the cancel click —
// never the destructive confirm, but not nothing either. A single-line text
// input takes Enter before the frame's window-level shortcut, so Enter in a
// field submits exactly once, through the input.

// assertDestructiveFill fails the test unless the primary button of a
// destructive dialog paints in the palette's danger color: the visible half
// of the destructive marking — the other half, Enter deafness, cannot show
// in a dialog whose focus never leaves the footer.
func assertDestructiveFill(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	p := paletteForTheme(nil)
	fill := buttonFillSample(t, tt, label)
	if !fillWithin(fill, p.danger, 12) {
		t.Errorf("destructive primary fill = %v, want within 12 of the danger color %v", fill, p.danger)
	}
	if fillWithin(fill, p.accent, 12) {
		t.Errorf("destructive primary fill %v is the plain accent %v, not the danger color", fill, p.accent)
	}
}

// Enter in the URL field of Add Remote submits exactly once: the field takes
// the key, so the input's own submission is the only consumer and the frame's
// window-level shortcut never sees it. The remote is added once and no error
// — an add of nothing the key pressed a second time would report — appears.
func TestDialogRemoteRefAddRemoteEnterSubmitsOnce(t *testing.T) {
	repo := setupSidebarRepo(t, "dr-add-enter")
	bareDir := newBareRemote(t, repo, "upstream")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Remote"); err != nil {
		t.Fatalf("click New Remote: %v", err)
	}
	h.ui.Frame()
	// The name field opens focused; typing goes straight in.
	h.ui.Type("upstream")
	typeField(t, h, "URL", bareDir, false)
	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("add by Enter produced an error — the key submitted twice or not at all: %v", err)
	}
	if got := remoteURLOf(t, repo, "upstream"); got != bareDir {
		t.Fatalf("git remote get-url upstream = %q, want %q", got, bareDir)
	}
	if got := remoteList(t, repo); got != "origin\nupstream" && got != "upstream\norigin" {
		t.Fatalf("remotes after the Enter add = %q, want exactly origin and upstream — a second add ran", got)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Add Remote"); ok {
		t.Error("the Add Remote dialog stayed open after the Enter submission")
	}
}

// Enter in the URL field of Edit Remote saves exactly once: the edit lands
// and no error surfaces.
func TestDialogRemoteRefEditRemoteEnterSubmitsOnce(t *testing.T) {
	repo := setupSidebarRepo(t, "dr-edit-enter")
	bareDir := newBareRemote(t, repo, "other")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Edit 'origin'…"); err != nil {
		t.Fatalf("choose Edit 'origin'…: %v", err)
	}
	h.ui.Frame()
	typeField(t, h, "URL", bareDir, true)
	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("edit by Enter produced an error — the key submitted twice or not at all: %v", err)
	}
	if got := remoteURLOf(t, repo, "origin"); got != bareDir {
		t.Fatalf("git remote get-url origin = %q, want %q", got, bareDir)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Edit Remote"); ok {
		t.Error("the Edit Remote dialog stayed open after the Enter submission")
	}
}

// Delete Remote is destructive: its Delete button paints in the danger color
// and never holds the focus at open. The body holds no control, so the focus
// opens on cancel — the first focusable control of the footer — and Enter is
// the cancel click: the remote survives and the dialog closes harmlessly.
// Clicking Delete is what deletes.
func TestDialogRemoteRefDeleteRemoteEnterDoesNotDelete(t *testing.T) {
	repo := setupSidebarRepo(t, "dr-delete-enter")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Delete 'origin'…"); err != nil {
		t.Fatalf("choose Delete 'origin'…: %v", err)
	}
	h.ui.Frame()

	assertDestructiveFill(t, h.ui, "Confirm Delete Remote")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Remote"); !ok {
		t.Fatalf("Confirm Delete Remote missing; texts=%q", h.ui.Texts())
	} else if btn.Focused {
		t.Fatal("the destructive Delete button holds the focus at open")
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	if remoteURLOf(t, repo, "origin") == "" {
		t.Fatal("Enter deleted the remote — a destructive dialog must never confirm by Enter")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Delete Remote"); ok {
		t.Error("the dialog is still open after Enter on the focused cancel")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter ran a write that failed: %v", err)
	}

	// The click on Delete is what deletes.
	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin again: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Delete 'origin'…"); err != nil {
		t.Fatalf("choose Delete 'origin'… again: %v", err)
	}
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Cancel"); !ok || !btn.Focused {
		t.Fatalf("focus is not on cancel at the destructive dialog's open: %+v found=%v", btn, ok)
	}
	clickDialogButton(t, h, "Confirm Delete Remote")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected delete error: %v", err)
	}
	if remoteURLOf(t, repo, "origin") != "" {
		t.Fatal("origin survived the confirmed delete")
	}
}

// Delete Remote Branches is destructive: Enter never confirms it, the rows
// on the server stay, and the dialog stays open — its focus opens in the
// first row's checkbox, which Enter can at most toggle, never submit. The
// click on Delete deletes exactly the checked rows.
func TestDialogRemoteRefDeleteRemoteBranchesEnterDoesNotDelete(t *testing.T) {
	repo, origin, _ := refBatchFixture(t, "dr-rbd-enter")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectRemoteBranchesForBatch(t, h, "origin/alpha", "origin/beta")
	chooseBatchItem(t, h, "origin/alpha", "Delete 2 Remote Branches…")
	if !hasExactText(h.ui, "Delete Remote Branches") {
		t.Fatalf("Delete Remote Branches dialog did not open; texts=%q", h.ui.Texts())
	}
	assertDestructiveFill(t, h.ui, "Confirm Delete Remote Branches")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Remote Branches"); !ok {
		t.Fatalf("Confirm Delete Remote Branches missing; texts=%q", h.ui.Texts())
	} else if btn.Focused {
		t.Fatal("the destructive Delete button holds the focus at open")
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	for _, br := range []string{"alpha", "beta"} {
		if !bareHasBranch(t, origin, br) {
			t.Fatalf("Enter deleted origin/%s — a destructive dialog must never confirm by Enter", br)
		}
	}
	if !hasExactText(h.ui, "Delete Remote Branches") {
		t.Fatal("the dialog closed on Enter, whose focus never reaches the footer here")
	}

	// Enter may have toggled the focused row's box: recheck what it cleared,
	// so the click below measures the Delete of exactly both rows.
	for _, row := range []string{"origin/alpha", "origin/beta"} {
		box, ok := h.ui.AccessByRole(ui.RoleCheckBox, row)
		if !ok {
			t.Fatalf("no checkbox row %q; texts=%q", row, h.ui.Texts())
		}
		if !box.Checked {
			if err := h.ui.Click(row); err != nil {
				t.Fatalf("recheck %q: %v\ntexts=%q", row, err, h.ui.Texts())
			}
			h.ui.Frame()
		}
	}
	if err := h.ui.Click("Confirm Delete Remote Branches"); err != nil {
		t.Fatalf("click Confirm Delete Remote Branches: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected batch delete error: %v", err)
	}
	for _, br := range []string{"alpha", "beta"} {
		if bareHasBranch(t, origin, br) {
			t.Fatalf("origin/%s survived the confirmed batch delete", br)
		}
	}
}

// Push Tags is not destructive and its body holds no text field, so the Push
// button opens focused and Enter there is its click: the checked tags reach
// the remote and the dialog closes behind the push.
func TestDialogRemoteRefPushTagsEnterPushes(t *testing.T) {
	repo, origin, _ := refBatchFixture(t, "dr-pt-enter")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Push 2 Tags to", "origin…")
	if !hasExactText(h.ui, "Push Tags") {
		t.Fatalf("Push Tags dialog did not open; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Push Tags"); !ok || !btn.Focused {
		t.Fatalf("the Push button is not focused at open: %+v found=%v", btn, ok)
	}

	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("push by Enter failed: %v", err)
	}
	for _, tag := range []string{"rel-1", "rel-2"} {
		if !bareHasTag(t, origin, tag) {
			t.Fatalf("%s did not reach origin by Enter", tag)
		}
	}
	if hasExactText(h.ui, "Push Tags") {
		t.Error("the Push Tags dialog stayed open after the Enter push")
	}
}

// Delete Tags is destructive: Enter never confirms it, the tags stay, and
// the dialog stays open — as in Delete Remote Branches the focus opens in a
// row's checkbox. The click on Delete deletes.
func TestDialogRemoteRefDeleteTagsEnterDoesNotDelete(t *testing.T) {
	repo, _, _ := refBatchFixture(t, "dr-td-enter")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Delete 2 Tags…")
	if !hasExactText(h.ui, "Delete Tags") {
		t.Fatalf("Delete Tags dialog did not open; texts=%q", h.ui.Texts())
	}
	assertDestructiveFill(t, h.ui, "Confirm Delete Tags")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Tags"); !ok {
		t.Fatalf("Confirm Delete Tags missing; texts=%q", h.ui.Texts())
	} else if btn.Focused {
		t.Fatal("the destructive Delete button holds the focus at open")
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	for _, tag := range []string{"rel-1", "rel-2"} {
		if !tagRefExists(t, repo, tag) {
			t.Fatalf("Enter deleted %s — a destructive dialog must never confirm by Enter", tag)
		}
	}
	if !hasExactText(h.ui, "Delete Tags") {
		t.Fatal("the dialog closed on Enter, whose focus never reaches the footer here")
	}

	for _, row := range []string{"rel-1", "rel-2"} {
		box, ok := h.ui.AccessByRole(ui.RoleCheckBox, row)
		if !ok {
			t.Fatalf("no checkbox row %q; texts=%q", row, h.ui.Texts())
		}
		if !box.Checked {
			if err := h.ui.Click(row); err != nil {
				t.Fatalf("recheck %q: %v\ntexts=%q", row, err, h.ui.Texts())
			}
			h.ui.Frame()
		}
	}
	if err := h.ui.Click("Confirm Delete Tags"); err != nil {
		t.Fatalf("click Confirm Delete Tags: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected batch tag delete error: %v", err)
	}
	for _, tag := range []string{"rel-1", "rel-2"} {
		if tagRefExists(t, repo, tag) {
			t.Fatalf("%s survived the confirmed batch delete", tag)
		}
	}
}

// Discarding the selected lines is destructive: its Discard button paints in
// the danger color and never holds the focus, which opens on cancel — the
// body holds no control — so Enter is the cancel click: the working tree
// keeps every byte and the dialog closes harmlessly. The click on Discard is
// what discards.
func TestDialogRemoteRefDiscardLinesEnterDoesNotDiscard(t *testing.T) {
	repo := newGitRepo(t, "dr-discard-enter")
	repo.CommitFile("a.txt", linesText("one", "two", "three", "four", "five", "six", "seven"), "initial")
	repo.WriteFile("a.txt", linesText("one", "two", "THREE", "four-NEW", "five", "six", "seven"))

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged a.txt")
	h.drain()

	wantTree := readWorktreeFile(t, repo, "a.txt")

	clickDiffLineGutter(t, h, ui.RoleInsertion, "THREE", false)
	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
	mustClick(t, h, "Discard Lines")
	h.ui.Frame()
	if !h.ui.HasText("Do you want to discard the changes in the selected lines?") {
		t.Fatalf("discard lines confirmation did not open; texts=%q", h.ui.Texts())
	}
	assertDestructiveFill(t, h.ui, "Confirm Discard")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Discard"); !ok {
		t.Fatalf("Confirm Discard missing; texts=%q", h.ui.Texts())
	} else if btn.Focused {
		t.Fatal("the destructive Discard button holds the focus at open")
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	if got := readWorktreeFile(t, repo, "a.txt"); got != wantTree {
		t.Fatalf("Enter discarded the lines:\n%q\nwant unchanged\n%q", got, wantTree)
	}
	if h.ui.HasText("Do you want to discard the changes in the selected lines?") {
		t.Error("the dialog is still open after Enter on the focused cancel")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter ran a write that failed: %v", err)
	}

	// The cancel left the line selection untouched, so the second question
	// opens without clicking the gutter anew — one more plain click there
	// would toggle the selection off instead.
	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
	mustClick(t, h, "Discard Lines")
	h.ui.Frame()
	if !h.ui.HasText("Do you want to discard the changes in the selected lines?") {
		t.Fatalf("discard lines confirmation did not reopen; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Confirm Discard")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("discard lines action error: %v", err)
	}
	want := linesText("one", "two", "four-NEW", "five", "six", "seven")
	if got := readWorktreeFile(t, repo, "a.txt"); got != want {
		t.Fatalf("worktree after the confirmed discard =\n%q\nwant\n%q", got, want)
	}
}

// Enter in either the file name field or the email field of New SSH Key
// submits exactly once: the key pair generates and no error shows. Each
// single-line input takes the key before the frame's window-level shortcut, so
// the input's submission is the only consumer.
func TestDialogRemoteRefNewSSHKeyEnterGeneratesOnce(t *testing.T) {
	t.Run("file name input", func(t *testing.T) {
		home := isolateSSHKeysTest(t)
		dir := filepath.Join(home, ".ssh")

		h := newTestApp(t)
		tt := openSSHKeysPage(t, h)
		if err := tt.Click("New SSH Key"); err != nil {
			t.Fatalf("click New SSH Key…: %v; texts=%q", err, tt.Texts())
		}
		tt.Frame()

		typeInSettingsField(t, tt, "Key file name", "enterkey")
		tt.Key(0, ui.KeyEnter)
		tt.Frame()
		drainSettings(t, h, tt)

		priv := filepath.Join(dir, "enterkey")
		if _, err := os.Stat(priv); err != nil {
			t.Fatalf("Enter in the name field generated no key: %v", err)
		}
		if _, err := os.Stat(priv + ".pub"); err != nil {
			t.Fatalf("the public half of the key pair is missing: %v", err)
		}
		if st := sshKeysStateOf(h.app); st.newErr != "" {
			t.Fatalf("the dialog shows the error %q — the key submitted twice or not at all", st.newErr)
		}
		if _, ok := tt.AccessByRole(ui.RoleDialog, "New SSH Key"); ok {
			t.Error("the New SSH Key dialog stayed open after the Enter generation")
		}
	})

	t.Run("email input", func(t *testing.T) {
		home := isolateSSHKeysTest(t)
		dir := filepath.Join(home, ".ssh")

		h := newTestApp(t)
		tt := openSSHKeysPage(t, h)
		if err := tt.Click("New SSH Key"); err != nil {
			t.Fatalf("click New SSH Key…: %v; texts=%q", err, tt.Texts())
		}
		tt.Frame()

		typeInSettingsField(t, tt, "Key file name", "emailenterkey")
		typeInSettingsField(t, tt, "Key comment email", "dev@example.com")
		tt.Key(0, ui.KeyEnter)
		tt.Frame()
		drainSettings(t, h, tt)

		priv := filepath.Join(dir, "emailenterkey")
		if _, err := os.Stat(priv); err != nil {
			t.Fatalf("Enter in the email field generated no key: %v", err)
		}
		if _, err := os.Stat(priv + ".pub"); err != nil {
			t.Fatalf("the public half of the key pair is missing: %v", err)
		}
		if st := sshKeysStateOf(h.app); st.newErr != "" {
			t.Fatalf("the dialog shows the error %q — the key submitted twice or not at all", st.newErr)
		}
		if _, ok := tt.AccessByRole(ui.RoleDialog, "New SSH Key"); ok {
			t.Error("the New SSH Key dialog stayed open after the Enter generation")
		}
	})
}

// The Open Existing Key picker is captured on the UI thread when the button
// is clicked: exchanging the injected picker before the background worker
// runs changes nothing — the worker still asks the picker the click captured.
func TestDialogRemoteRefOpenKeyPickerCapturedOnUIThread(t *testing.T) {
	isolateSSHKeysTest(t)
	// Both keys live outside the SSH folder, so the one the worker opens
	// joins the stored key files: the list proves which picker answered.
	first := makeSSHKeyPair(t, t.TempDir(), "firstkey")
	second := makeSSHKeyPair(t, t.TempDir(), "secondkey")

	h := newTestApp(t)
	tt := openSSHKeysPage(t, h)

	sshKeysStateOf(h.app).pickKeyFileFn = pickFuncForTest(first.Path)
	h.queue.PauseWorkers()
	defer h.queue.ResumeWorkers()
	if err := tt.Click("Open Existing Key"); err != nil {
		t.Fatalf("click Open Existing Key…: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	worker := h.queue.WaitWorker(t)

	// The worker is spawned and held: the click already captured its picker.
	// Exchanging the injected one on the UI thread must not reach it.
	sshKeysStateOf(h.app).pickKeyFileFn = pickFuncForTest(second.Path)
	h.queue.ReleaseWorker(worker)
	h.queue.ResumeWorkers()
	drainSettings(t, h, tt)

	prefs := h.store.Settings()
	if len(prefs.SSHKeyFiles) == 0 || !containsKeyPath(prefs.SSHKeyFiles, first.Path) {
		t.Fatalf("stored files = %q, want the key the picker captured at the click, %q", prefs.SSHKeyFiles, first.Path)
	}
	if containsKeyPath(prefs.SSHKeyFiles, second.Path) {
		t.Fatalf("stored files = %q hold the key of the picker exchanged after the click — the worker read the state itself", prefs.SSHKeyFiles)
	}
}

// Escape dismisses every one of the eight dialogs of this batch while no
// write is running: each opens through its real entry and Escape closes it
// without touching the repository.
func TestDialogRemoteRefEscapeClosesDialogs(t *testing.T) {
	repo, origin, _ := refBatchFixture(t, "dr-escape")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	pressEscapeClosed := func(what, title string) {
		t.Helper()
		h.ui.Key(0, ui.KeyEscape)
		h.ui.Frame()
		if _, ok := h.ui.AccessByRole(ui.RoleDialog, title); ok {
			t.Fatalf("Escape did not close the %s dialog; texts=%q", what, h.ui.Texts())
		}
	}

	// Add Remote.
	if err := h.ui.Click("New Remote"); err != nil {
		t.Fatalf("click New Remote: %v", err)
	}
	h.ui.Frame()
	pressEscapeClosed("Add Remote", "Add Remote")

	// Edit Remote.
	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Edit 'origin'…"); err != nil {
		t.Fatalf("choose Edit 'origin'…: %v", err)
	}
	h.ui.Frame()
	pressEscapeClosed("Edit Remote", "Edit Remote")

	// Delete Remote.
	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Delete 'origin'…"); err != nil {
		t.Fatalf("choose Delete 'origin'…: %v", err)
	}
	h.ui.Frame()
	pressEscapeClosed("Delete Remote", "Delete Remote")
	if remoteURLOf(t, repo, "origin") == "" {
		t.Fatal("Escape deleted the remote")
	}

	// Delete Remote Branches.
	selectRemoteBranchesForBatch(t, h, "origin/alpha", "origin/beta")
	chooseBatchItem(t, h, "origin/alpha", "Delete 2 Remote Branches…")
	if !hasExactText(h.ui, "Delete Remote Branches") {
		t.Fatalf("Delete Remote Branches dialog did not open; texts=%q", h.ui.Texts())
	}
	pressEscapeClosed("Delete Remote Branches", "Delete Remote Branches")
	if !bareHasBranch(t, origin, "alpha") || !bareHasBranch(t, origin, "beta") {
		t.Fatal("Escape deleted remote branches")
	}

	// Push Tags.
	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Push 2 Tags to", "origin…")
	if !hasExactText(h.ui, "Push Tags") {
		t.Fatalf("Push Tags dialog did not open; texts=%q", h.ui.Texts())
	}
	pressEscapeClosed("Push Tags", "Push Tags")
	if bareHasTag(t, origin, "rel-1") {
		t.Fatal("Escape pushed the tags")
	}

	// Delete Tags.
	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Delete 2 Tags…")
	if !hasExactText(h.ui, "Delete Tags") {
		t.Fatalf("Delete Tags dialog did not open; texts=%q", h.ui.Texts())
	}
	pressEscapeClosed("Delete Tags", "Delete Tags")
	if !tagRefExists(t, repo, "rel-1") || !tagRefExists(t, repo, "rel-2") {
		t.Fatal("Escape deleted the tags")
	}

	// The discard confirmation of the diff lines, in the same harness: the
	// selection moves to the changes page, whose file list the Escape-free
	// dialogs above never touched.
	repo.WriteFile("a.txt", linesText("one", "two", "THREE", "four", "five", "six", "seven"))
	h.app.session.Reload()
	h.drain()
	if err := h.ui.Click("a.txt"); err != nil {
		// The file may already be the selected row; either way the diff must
		// show before the discard opens.
		h.ui.Frame()
	}
	h.drain()
	clickDiffLineGutter(t, h, ui.RoleInsertion, "THREE", false)
	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
	mustClick(t, h, "Discard Lines")
	h.ui.Frame()
	if !h.ui.HasText("Do you want to discard the changes in the selected lines?") {
		t.Fatalf("discard lines confirmation did not open; texts=%q", h.ui.Texts())
	}
	pressEscapeClosed("discard lines", "Do you want to discard the changes in the selected lines?")
	if got := readWorktreeFile(t, repo, "a.txt"); !strings.Contains(got, "THREE") {
		t.Fatal("Escape discarded the lines")
	}

	// New SSH Key, in the Settings window.
	tt := openSSHKeysPage(t, h)
	if err := tt.Click("New SSH Key"); err != nil {
		t.Fatalf("click New SSH Key…: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if _, ok := tt.AccessByRole(ui.RoleDialog, "New SSH Key"); !ok {
		t.Fatalf("New SSH Key dialog did not open; texts=%q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if _, ok := tt.AccessByRole(ui.RoleDialog, "New SSH Key"); ok {
		t.Fatal("Escape did not close the New SSH Key dialog")
	}
}
