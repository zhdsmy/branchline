package app

import (
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The tests of this file drive the Enter key, the destructive marking and
// the focus rules of the sixth batch of dialogs on the shared frame through
// the real UI, on real repositories, exactly as dialog_remote_ref_test.go
// does for its own batch. The frame's focus rule shapes the expectations
// here: a destructive dialog whose body holds no control opens with the
// focus on cancel, so Enter there is the cancel click — never the
// destructive confirm. The single-line input of the rename dialog takes
// Enter before the frame's window-level shortcut, so the input's own
// submission is the only consumer.

// Discarding changes is destructive: its Discard button paints in the danger
// color and never holds the focus, which opens on cancel — the body holds no
// control — so Enter is the cancel click: the working tree keeps every byte
// and the dialog closes with its captured targets dropped. The click on
// Discard is what discards.
func TestDialogBatch6DiscardChangesEnterDoesNotDiscard(t *testing.T) {
	repo := newGitRepo(t, "b6-discard-enter")
	repo.CommitFile("tracked.txt", "base\n", "initial commit")
	repo.WriteFile("tracked.txt", "base\nmodified\n")

	h := openChangesHarness(t, repo)
	wantTree := readWorktreeFile(t, repo, "tracked.txt")

	mustClick(t, h, "Discard tracked.txt")
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Discard changes?"); !ok {
		t.Fatalf("discard confirmation did not open; texts=%q", h.ui.Texts())
	}
	assertDestructiveFill(t, h.ui, "Confirm Discard")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Discard"); !ok {
		t.Fatalf("Confirm Discard missing; texts=%q", h.ui.Texts())
	} else if btn.Focused {
		t.Fatal("the destructive Discard button holds the focus at open")
	}
	if cancel, ok := h.ui.AccessByRole(ui.RoleButton, "Cancel"); !ok || !cancel.Focused {
		t.Fatalf("focus is not on cancel at the destructive dialog's open: %+v found=%v", cancel, ok)
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	if got := readWorktreeFile(t, repo, "tracked.txt"); got != wantTree {
		t.Fatalf("Enter discarded the changes:\n%q\nwant unchanged\n%q", got, wantTree)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Discard changes?"); ok {
		t.Error("the dialog is still open after Enter on the focused cancel")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter ran a write that failed: %v", err)
	}
	s := &h.app.changesView
	if s.confirmDiscard || len(s.discardTargets) != 0 {
		t.Fatalf("discard state survived the cancellation: open=%v targets=%v", s.confirmDiscard, s.discardTargets)
	}

	// The click on Discard is what discards: reopening asks afresh, and the
	// confirmed write restores the committed bytes.
	mustClick(t, h, "Discard tracked.txt")
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Discard changes?"); !ok {
		t.Fatalf("discard confirmation did not reopen; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Confirm Discard")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("confirmed discard failed: %v", err)
	}
	if got := readWorktreeFile(t, repo, "tracked.txt"); got != "base\n" {
		t.Fatalf("worktree after the confirmed discard = %q, want base", got)
	}
}

// Escape dismisses the discard confirmation exactly like its cancel does:
// the dialog closes, the captured targets drop in one cancellation, and the
// porcelain status of the working tree does not move a byte.
func TestDialogBatch6DiscardChangesEscapeCancels(t *testing.T) {
	repo := newGitRepo(t, "b6-discard-escape")
	repo.CommitFile("tracked.txt", "base\n", "initial commit")
	repo.WriteFile("tracked.txt", "base\nmodified\n")
	repo.WriteFile("untracked.txt", "new\n")

	h := openChangesHarness(t, repo)
	before := repo.Git("status", "--porcelain")

	mustClick(t, h, "Discard All")
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Discard changes?"); !ok {
		t.Fatalf("discard confirmation did not open; texts=%q", h.ui.Texts())
	}

	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	h.drain()

	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Discard changes?"); ok {
		t.Error("Escape did not close the discard confirmation")
	}
	if after := repo.Git("status", "--porcelain"); after != before {
		t.Fatalf("Escape changed the working tree: before %q after %q", before, after)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Escape ran a write that failed: %v", err)
	}
	s := &h.app.changesView
	if s.confirmDiscard || len(s.discardTargets) != 0 {
		t.Fatalf("discard state survived the Escape dismissal: open=%v targets=%v", s.confirmDiscard, s.discardTargets)
	}
}

// Confirming a conflict resolution is destructive — the other side and every
// manual edit of the working tree file are overwritten — so its Confirm
// Resolve button paints in the danger color, Enter never arms it, and a
// repository write in progress disables it. Enter on the focused cancel
// closes the dialog with the conflict and its stages untouched; the click on
// Confirm Resolve is what resolves.
func TestDialogBatch6ConflictResolveEnterDoesNotResolve(t *testing.T) {
	repo := newGitRepo(t, "b6-conflict-enter")
	uuMergeFixture(t, repo)
	repo.AddBareRemote("origin")

	h := openConflictedMerge(t, repo)

	mustClick(t, h, "Conflicted f.txt")
	h.drain()
	mustClick(t, h, "Use Ours")
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Use our version?"); !ok {
		t.Fatalf("resolve confirmation did not open; texts=%q", h.ui.Texts())
	}
	conflictBytes := readWorktreeFile(t, repo, "f.txt")
	if !strings.Contains(conflictBytes, "<<<<<<<") {
		t.Fatalf("fixture: conflicted f.txt missing markers: %q", conflictBytes)
	}
	stagesBefore := repo.Git("ls-files", "-u")

	assertDestructiveFill(t, h.ui, "Confirm Resolve")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Resolve"); !ok {
		t.Fatalf("Confirm Resolve missing; texts=%q", h.ui.Texts())
	} else if btn.Focused {
		t.Fatal("the destructive Confirm Resolve button holds the focus at open")
	} else if btn.Disabled {
		t.Fatal("Confirm Resolve starts disabled with no write running")
	}
	if cancel, ok := h.ui.AccessByRole(ui.RoleButton, "Cancel"); !ok || !cancel.Focused {
		t.Fatalf("focus is not on cancel at the destructive dialog's open: %+v found=%v", cancel, ok)
	}

	// A repository write in progress disables the confirmation: the fetch
	// stands for any write of the repository the open dialog did not submit.
	h.queue.PauseWorkers()
	h.app.syncFetch()
	fetchWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the fetch is held")
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Resolve"); !ok {
		t.Fatalf("Confirm Resolve missing during the write; texts=%q", h.ui.Texts())
	} else if !btn.Disabled {
		t.Error("Confirm Resolve is enabled while a repository write runs")
	}
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(fetchWorker)
	h.drain()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Resolve"); !ok || btn.Disabled {
		t.Fatalf("Confirm Resolve after the write = %+v found=%v, want enabled again", btn, ok)
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	if content := readWorktreeFile(t, repo, "f.txt"); content != conflictBytes {
		t.Fatalf("Enter overwrote the conflicted file:\n%q\nwant unchanged\n%q", content, conflictBytes)
	}
	if stages := repo.Git("ls-files", "-u"); stages != stagesBefore {
		t.Fatalf("Enter altered the unmerged stages: before %q after %q", stagesBefore, stages)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Use our version?"); ok {
		t.Error("the dialog is still open after Enter on the focused cancel")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter ran a write that failed: %v", err)
	}

	// The click on Confirm Resolve is what resolves: our version lands and
	// the unmerged entries leave the index.
	mustClick(t, h, "Use Ours")
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Use our version?"); !ok {
		t.Fatalf("resolve confirmation did not reopen; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Confirm Resolve")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("confirmed use ours failed: %v", err)
	}
	if content := readWorktreeFile(t, repo, "f.txt"); content != "main\n" {
		t.Fatalf("f.txt after the confirmed use ours = %q, want main", content)
	}
	if out := repo.Git("ls-files", "-u"); strings.TrimSpace(out) != "" {
		t.Fatalf("unmerged entries survived the confirmed resolution: %q", out)
	}
}

// Discarding an edited local copy is destructive: its Discard Local Copy
// button paints in the danger color and never holds the focus, so Enter is
// the cancel click — the copy and its edits survive byte for byte. The click
// on Discard Local Copy is what deletes.
func TestDialogBatch6RemoteEditDiscardEnterDoesNotDelete(t *testing.T) {
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
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Discard local copy?"); !ok {
		t.Fatalf("discard confirmation did not open; texts=%q", h.ui.Texts())
	}
	assertDestructiveFill(t, h.ui, "Confirm Discard Local Copy")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Discard Local Copy"); !ok {
		t.Fatalf("Confirm Discard Local Copy missing; texts=%q", h.ui.Texts())
	} else if btn.Focused {
		t.Fatal("the destructive Discard Local Copy button holds the focus at open")
	}
	if cancel, ok := h.ui.AccessByRole(ui.RoleButton, "Cancel"); !ok || !cancel.Focused {
		t.Fatalf("focus is not on cancel at the destructive dialog's open: %+v found=%v", cancel, ok)
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	if got, err := os.ReadFile(local); err != nil || string(got) != "changed\n" {
		t.Fatalf("Enter deleted or rewrote the copy: content=%q err=%v", got, err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Discard local copy?"); ok {
		t.Error("the dialog is still open after Enter on the focused cancel")
	}
	if entry := remoteEditEntryOf(h, "a.txt"); entry == nil || !entry.hasCopy {
		t.Fatal("the copy entry is gone after the cancellation, want it kept")
	}

	// The click on Discard Local Copy is what deletes: copy, directory and
	// entry all go.
	chooseRemoteEditItem(t, h, "a.txt", "Discard Local Copy")
	h.drain()
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Discard local copy?"); !ok {
		t.Fatalf("discard confirmation did not reopen; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Confirm Discard Local Copy")
	h.drain()
	h.ui.Frame()
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatalf("copy still exists after the confirmed discard (err %v), want it deleted", err)
	}
	if entry := remoteEditEntryOf(h, "a.txt"); entry != nil {
		t.Fatalf("entry still present after the confirmed discard: %+v", entry)
	}
}

// The server-changed notice is not destructive: its Show Local Copy button
// opens focused, and Enter is exactly its click — the dialog closes and the
// copy reveals through the injected seam while the server's bytes never move.
func TestDialogBatch6RemoteEditConflictEnterShowsCopy(t *testing.T) {
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

	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "File changed on the server"); !ok {
		t.Fatalf("server-changed notice did not open; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Show Local Copy"); !ok {
		t.Fatalf("Show Local Copy missing; texts=%q", h.ui.Texts())
	} else if !btn.Focused {
		t.Fatal("Show Local Copy is not focused at the notice's open")
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	shown := capture.shownPaths()
	if len(shown) != 1 || shown[0] != local {
		t.Fatalf("reveals after Enter = %q, want exactly the copy %q", shown, local)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "File changed on the server"); ok {
		t.Error("the notice is still open after Enter on Show Local Copy")
	}
	if got := string(srv.ReadFile(t, repo.Path, "a.txt")); got != "somebody else\n" {
		t.Fatalf("server content = %q, want the other change — nothing may be written", got)
	}
	if got, err := os.ReadFile(local); err != nil || string(got) != "my edit\n" {
		t.Fatalf("copy content = %q err %v, want my edit", got, err)
	}
}

// Renaming a repository is not destructive, but its single-line input holds
// the focus — fully selecting the current name — and takes Enter before the
// frame's shortcut, so the input's own submission is the only consumer. With
// the name unchanged the key does nothing and the Rename button stays
// disabled; with a new name the one press renames and closes the dialog.
func TestDialogBatch6RenameRepositoryEnterInField(t *testing.T) {
	repo := newGitRepo(t, "b6renamerepo")
	repo.CommitFile("a.txt", "a\n", "commit")

	h := newTestApp(t)
	saved := h.openRepo(repo.StoreRepo())
	h.drain()

	openSwitcherPopup(t, h)
	rightClickRepoRow(t, h, "b6renamerepo")
	if err := h.ui.ChooseMenuItem("Rename…"); err != nil {
		t.Fatalf("choose Rename…: %v; menu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Rename Repository"); !ok {
		t.Fatalf("rename dialog did not open; texts=%q", h.ui.Texts())
	}
	field, ok := h.ui.AccessByRole(ui.RoleTextField, "Repository Name")
	if !ok {
		t.Fatalf("Repository Name input missing; texts=%q", h.ui.Texts())
	}
	if !field.Focused {
		t.Fatal("the name field is not focused at the dialog's open")
	}
	if field.Value != "b6renamerepo" {
		t.Fatalf("input starts as %q, want the current name", field.Value)
	}

	// The unchanged name is no rename: Enter in the field does nothing at
	// all — no rename, no close, no error — and the button stays disabled.
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Rename Repository"); !ok {
		t.Error("the rename dialog closed on Enter with the name unchanged")
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Rename Repository"); !ok || !btn.Disabled {
		t.Fatalf("Confirm Rename Repository with the unchanged name = %+v found=%v, want disabled", btn, ok)
	}
	if got, ok := h.store.Get(saved.ID); !ok || got.Name != "b6renamerepo" {
		t.Fatalf("store name after the dead Enter = %+v (found %v), want unchanged", got, ok)
	}

	// The opening selection replaced, the one Enter submits through the
	// input: the store renames exactly once, the dialog closes and no error
	// shows.
	h.ui.Type("b6renamed")
	h.ui.Frame()
	if field, ok := h.ui.AccessByRole(ui.RoleTextField, "Repository Name"); !ok || field.Value != "b6renamed" {
		t.Fatalf("input after typing = %+v (found %v), want exactly b6renamed — the initial selection was not replaced", field, ok)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	if got, ok := h.store.Get(saved.ID); !ok || got.Name != "b6renamed" {
		t.Fatalf("store name after the Enter rename = %+v (found %v), want b6renamed", got, ok)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Rename Repository"); ok {
		t.Error("the rename dialog stayed open after the Enter rename")
	}
	if st := h.app.reposUI(); st.renErr != "" {
		t.Fatalf("the dialog shows the error %q after the Enter rename — the submission ran twice or failed", st.renErr)
	}
}
