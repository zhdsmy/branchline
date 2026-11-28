package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// openSaveStashDialog clicks the sidebar's Save Stash entry and renders the
// dialog.
func openSaveStashDialog(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Save Stash"); err != nil {
		t.Fatalf("click Save Stash: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
}

// switchSaveStashToSelected moves the dialog's scope to Selected files.
func switchSaveStashToSelected(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Selected files"); err != nil {
		t.Fatalf("click Selected files: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
}

// saveStashCheck returns the access info of the checkbox of one file of the
// dialog's Selected files list.
func saveStashCheck(t *testing.T, h *testHarness, path string) (ui.AccessInfo, bool) {
	t.Helper()
	return h.ui.AccessByRole(ui.RoleCheckBox, "Stash "+path)
}

// mustSaveStashCheck returns the checkbox of path, failing when it is
// absent.
func mustSaveStashCheck(t *testing.T, h *testHarness, path string) ui.AccessInfo {
	t.Helper()
	box, ok := saveStashCheck(t, h, path)
	if !ok {
		t.Fatalf("checkbox of %q missing; texts=%q", path, h.ui.Texts())
	}
	return box
}

// stashShowPaths returns the file paths of the newest stash entry of the
// repository at dir, as git stash show lists them.
func stashShowPaths(t *testing.T, dir string) []string {
	t.Helper()
	out := strings.TrimSpace(runGitCommand(t, dir, "stash", "show", "--name-status", "stash@{0}"))
	if out == "" {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(line, "\t")
		paths = append(paths, fields[1:]...)
	}
	return paths
}

// writeSaveStashMessage focuses the dialog's message input and types s.
func writeSaveStashMessage(t *testing.T, h *testHarness, s string) {
	t.Helper()
	if err := h.ui.Click("Stash message"); err != nil {
		t.Fatalf("focus Stash message: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Type(s)
	h.ui.Frame()
}

// confirmSaveStash clicks the dialog's confirm button and drains the write.
func confirmSaveStash(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Confirm Save Stash"); err != nil {
		t.Fatalf("click Confirm Save Stash: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
}

// setupSelectedStashRepo builds a repository with the fixture of the
// selected-files tests: three modified tracked files — one of them staged —
// and one untracked file.
func setupSelectedStashRepo(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	for _, f := range []string{"a.txt", "b.txt", "c.txt"} {
		repo.CommitFile(f, "base\n", "commit "+f)
	}
	repo.WriteFile("a.txt", "base\nchanged a\n")
	repo.WriteFile("b.txt", "base\nchanged b\n")
	repo.Git("add", "b.txt")
	repo.WriteFile("c.txt", "base\nchanged c\n")
	repo.WriteFile("new.txt", "brand new\n")
	return repo
}

// The Selected files scope of the Save Stash dialog lists every changed
// file checked, and stashing a subset takes exactly the checked files —
// tracked or untracked, staged or not — while everything unchecked keeps
// its changes in the working tree.
func TestStashSelectedCheckedFilesOnly(t *testing.T) {
	repo := setupSelectedStashRepo(t, "stashsel-checked")

	h := openStashApp(t, repo)
	openSaveStashDialog(t, h)
	switchSaveStashToSelected(t, h)

	// All four files are listed and start checked.
	for _, f := range []string{"a.txt", "b.txt", "c.txt", "new.txt"} {
		if box := mustSaveStashCheck(t, h, f); !box.Checked {
			t.Fatalf("checkbox of %q starts unchecked", f)
		}
	}

	// Uncheck a.txt and the untracked new.txt, then stash with a message.
	for _, f := range []string{"a.txt", "new.txt"} {
		if err := h.ui.Click("Stash " + f); err != nil {
			t.Fatalf("uncheck %q: %v\ntexts=%q", f, err, h.ui.Texts())
		}
		h.ui.Frame()
	}
	for _, f := range []string{"a.txt", "new.txt"} {
		if box := mustSaveStashCheck(t, h, f); box.Checked {
			t.Fatalf("checkbox of %q stayed checked after the click", f)
		}
	}
	writeSaveStashMessage(t, h, "selected work")
	confirmSaveStash(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected save error: %v", err)
	}
	if n := stashCount(t, repo.Path()); n != 1 {
		t.Fatalf("stash count = %d, want 1", n)
	}
	if out := stashListLines(t, repo.Path())[0]; !strings.Contains(out, "selected work") {
		t.Fatalf("stash entry %q, want the custom message", out)
	}
	stashed := stashShowPaths(t, repo.Path())
	if len(stashed) != 2 || stashed[0] != "b.txt" || stashed[1] != "c.txt" {
		t.Fatalf("stash holds %v, want exactly [b.txt c.txt]", stashed)
	}
	// The unchecked files keep their changes in the working tree.
	if data, err := os.ReadFile(filepath.Join(repo.Path(), "a.txt")); err != nil || string(data) != "base\nchanged a\n" {
		t.Fatalf("a.txt after the stash = %q, %v; want the unchecked change kept", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(repo.Path(), "new.txt")); err != nil || string(data) != "brand new\n" {
		t.Fatalf("new.txt after the stash = %q, %v; want the unchecked untracked file kept", string(data), err)
	}
	// b.txt was staged and is stashed: it is neither staged nor modified.
	if out := repo.Git("--literal-pathspecs", "status", "--porcelain", "--", "b.txt"); strings.TrimSpace(out) != "" {
		t.Fatalf("status of b.txt after the stash = %q, want clean — neither staged nor modified", out)
	}
	if data, err := os.ReadFile(filepath.Join(repo.Path(), "c.txt")); err != nil || string(data) != "base\n" {
		t.Fatalf("c.txt after the stash = %q, %v; want the committed content restored", string(data), err)
	}
}

// Stashing a staged rename by its checkbox takes both halves: neither the
// old nor the new path is left in the index or the working tree afterwards,
// and the stash entry records the rename.
func TestStashSelectedRename(t *testing.T) {
	repo := newGitRepo(t, "stashsel-rename")
	repo.CommitFile("old.txt", "base\n", "initial commit")
	repo.Git("mv", "old.txt", "renamed.txt")

	h := openStashApp(t, repo)
	openSaveStashDialog(t, h)
	switchSaveStashToSelected(t, h)

	if box := mustSaveStashCheck(t, h, "renamed.txt"); !box.Checked {
		t.Fatal("checkbox of the rename starts unchecked")
	}
	writeSaveStashMessage(t, h, "rename work")
	confirmSaveStash(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected save error: %v", err)
	}
	if n := stashCount(t, repo.Path()); n != 1 {
		t.Fatalf("stash count = %d, want 1", n)
	}
	if out := repo.Git("status", "--porcelain"); strings.Contains(out, "old.txt") || strings.Contains(out, "renamed.txt") {
		t.Fatalf("status after the rename stash = %q, want no trace of old.txt or renamed.txt", out)
	}
	shown := strings.TrimSpace(runGitCommand(t, repo.Path(), "stash", "show", "--name-status", "stash@{0}"))
	fields := strings.Split(shown, "\t")
	if len(fields) != 3 || !strings.HasPrefix(fields[0], "R") || fields[1] != "old.txt" || fields[2] != "renamed.txt" {
		t.Fatalf("stash entry = %q, want the rename old.txt → renamed.txt", shown)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "old.txt")); err != nil {
		t.Fatalf("old.txt missing from the worktree after the stash: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("renamed.txt survived the stash in the worktree (err=%v)", err)
	}
}

// Unticking Include untracked files drops the untracked files from the
// list, and ticking it again brings them back unchecked.
func TestStashSelectedUntrackedToggle(t *testing.T) {
	repo := setupSelectedStashRepo(t, "stashsel-untracked")

	h := openStashApp(t, repo)
	openSaveStashDialog(t, h)
	switchSaveStashToSelected(t, h)
	if box := mustSaveStashCheck(t, h, "new.txt"); !box.Checked {
		t.Fatal("checkbox of new.txt starts unchecked")
	}

	if err := h.ui.Click("Include untracked files"); err != nil {
		t.Fatalf("untick Include untracked files: %v", err)
	}
	h.ui.Frame()
	if _, ok := saveStashCheck(t, h, "new.txt"); ok {
		t.Fatal("new.txt stayed in the list with untracked files excluded")
	}
	if _, ok := saveStashCheck(t, h, "a.txt"); !ok {
		t.Fatal("a.txt left the list with untracked files excluded")
	}

	if err := h.ui.Click("Include untracked files"); err != nil {
		t.Fatalf("retick Include untracked files: %v", err)
	}
	h.ui.Frame()
	if box := mustSaveStashCheck(t, h, "new.txt"); box.Checked {
		t.Fatal("new.txt came back checked: a file re-entering the list must start unchecked")
	}
	if box := mustSaveStashCheck(t, h, "a.txt"); !box.Checked {
		t.Fatal("a.txt lost its check mark across the untracked toggle")
	}
}

// With no file checked the confirm button is disabled and the dialog
// explains what it needs.
func TestStashSelectedNoneCheckedRefused(t *testing.T) {
	repo := setupSelectedStashRepo(t, "stashsel-none")

	h := openStashApp(t, repo)
	openSaveStashDialog(t, h)
	switchSaveStashToSelected(t, h)

	for _, f := range []string{"a.txt", "b.txt", "c.txt", "new.txt"} {
		if err := h.ui.Click("Stash " + f); err != nil {
			t.Fatalf("uncheck %q: %v", f, err)
		}
		h.ui.Frame()
	}

	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Save Stash"); !ok || !btn.Disabled {
		t.Fatalf("Confirm Save Stash with nothing checked = %+v (found=%v), want disabled", btn, ok)
	}
	if !h.ui.HasText("Select at least one file to stash.") {
		t.Fatalf("select-a-file explanation missing; texts=%q", h.ui.Texts())
	}
}

// While the dialog is open, files appearing in the snapshot join the list
// unchecked and files whose changes vanish leave it, without touching the
// check marks of the files that stay.
func TestStashSelectedSnapshotRefresh(t *testing.T) {
	repo := setupSelectedStashRepo(t, "stashsel-refresh")

	h := openStashApp(t, repo)
	openSaveStashDialog(t, h)
	switchSaveStashToSelected(t, h)
	if box := mustSaveStashCheck(t, h, "c.txt"); !box.Checked {
		t.Fatal("checkbox of c.txt starts unchecked")
	}

	// A new untracked file appears in the worktree; once the session reads
	// it, it joins the list unchecked.
	repo.WriteFile("d.txt", "late arrival\n")
	h.app.session.Reload()
	h.drain()
	if box := mustSaveStashCheck(t, h, "d.txt"); box.Checked {
		t.Fatal("d.txt joined the list checked")
	}
	if box := mustSaveStashCheck(t, h, "c.txt"); !box.Checked {
		t.Fatal("c.txt lost its check mark across the refresh")
	}

	// Reverting a checked file removes it from the list.
	repo.Git("checkout", "--", "c.txt")
	h.app.session.Reload()
	h.drain()
	if _, ok := saveStashCheck(t, h, "c.txt"); ok {
		t.Fatal("c.txt stayed in the list after its change was reverted")
	}
	if box := mustSaveStashCheck(t, h, "a.txt"); !box.Checked {
		t.Fatal("a.txt lost its check mark across the second refresh")
	}
}

// The write worker re-reads the live working tree: a file reverted behind
// the open dialog's back is no longer stashable, so the push is refused
// with the no-changes error and creates no stash.
func TestStashSelectedLiveGuard(t *testing.T) {
	repo := newGitRepo(t, "stashsel-guard")
	repo.CommitFile("c.txt", "base\n", "initial commit")
	repo.WriteFile("c.txt", "base\nchanged c\n")

	h := openStashApp(t, repo)
	openSaveStashDialog(t, h)
	switchSaveStashToSelected(t, h)
	if box := mustSaveStashCheck(t, h, "c.txt"); !box.Checked {
		t.Fatal("checkbox of c.txt starts unchecked")
	}

	// Revert the file externally after the dialog opened: the snapshot the
	// dialog reads is never reloaded, so the button stays enabled.
	repo.Git("checkout", "--", "c.txt")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Save Stash"); !ok || btn.Disabled {
		t.Fatalf("Confirm Save Stash after the external revert = %+v, want still enabled from the stale snapshot", btn)
	}
	confirmSaveStash(t, h)

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("save of reverted files produced no ActionErr")
	} else if !strings.Contains(err.Error(), "The selected files have no changes to stash.") {
		t.Fatalf("ActionErr = %q, want the live guard's no-changes refusal", err)
	}
	if n := stashCount(t, repo.Path()); n != 0 {
		t.Fatalf("stash count = %d, want 0: the refused push must not create an entry", n)
	}
}

// The Stash Changes item of a file's context menu opens the Save Stash
// dialog in the Selected files scope with exactly that file checked — for
// an untracked file ticking the untracked choice as well — and it opens
// even while the sidebar's Stashes section is collapsed.
func TestStashSelectedContextMenu(t *testing.T) {
	repo := setupSelectedStashRepo(t, "stashsel-menu")

	h := openStashApp(t, repo)

	// A tracked unstaged file: the dialog opens scoped to it alone.
	chooseFileMenuItem(t, h, "Unstaged c.txt", "Stash Changes…")
	h.ui.Frame()
	if !h.ui.HasText("Stash message") {
		t.Fatalf("Save Stash dialog did not open; texts=%q", h.ui.Texts())
	}
	if box := mustSaveStashCheck(t, h, "c.txt"); !box.Checked {
		t.Fatal("checkbox of c.txt is unchecked after opening from its menu")
	}
	for _, f := range []string{"a.txt", "b.txt", "new.txt"} {
		if box := mustSaveStashCheck(t, h, f); box.Checked {
			t.Fatalf("checkbox of %q is checked though only c.txt was stashed from", f)
		}
	}
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Include untracked files"); !ok || !box.Checked {
		t.Fatalf("Include untracked files = %+v (found=%v), want ticked", box, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	// An untracked file: same scope, with the untracked choice ticked.
	chooseFileMenuItem(t, h, "Unstaged new.txt", "Stash Changes…")
	h.ui.Frame()
	if box := mustSaveStashCheck(t, h, "new.txt"); !box.Checked {
		t.Fatal("checkbox of new.txt is unchecked after opening from its menu")
	}
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Include untracked files"); !ok || !box.Checked {
		t.Fatalf("Include untracked files for an untracked file = %+v (found=%v), want ticked", box, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	// Collapse the Stashes section: the menu item still opens the dialog.
	if err := h.ui.Click("Stashes"); err != nil {
		t.Fatalf("collapse Stashes: %v", err)
	}
	h.ui.Frame()
	chooseFileMenuItem(t, h, "Unstaged c.txt", "Stash Changes…")
	h.ui.Frame()
	if !h.ui.HasText("Stash message") {
		t.Fatalf("Save Stash dialog did not open with Stashes collapsed; texts=%q", h.ui.Texts())
	}
	if box := mustSaveStashCheck(t, h, "c.txt"); !box.Checked {
		t.Fatal("checkbox of c.txt is unchecked after opening from its menu with Stashes collapsed")
	}
}

// The Stash Changes item is disabled for a conflicted file and in a
// repository before its first commit: neither can be stashed.
func TestStashSelectedContextMenuDisabled(t *testing.T) {
	// A conflicted file: the item is present but refuses the click.
	t.Run("conflicted file", func(t *testing.T) {
		repo := newGitRepo(t, "stashsel-menu-conflict")
		repo.CommitFile("conf.txt", "base\n", "initial commit")
		repo.Git("checkout", "-q", "-b", "side")
		repo.CommitFile("conf.txt", "side\n", "side change")
		repo.Git("checkout", "-q", "main")
		repo.CommitFile("conf.txt", "main\n", "main change")
		if out, err := tryGitCommand(repo.Path(), "merge", "side"); err == nil {
			t.Fatalf("fixture merge succeeded unexpectedly: %s", out)
		}
		h := openStashApp(t, repo)

		if err := h.ui.RightClick("Conflicted conf.txt"); err != nil {
			t.Fatalf("right-click Conflicted conf.txt: %v\ntexts=%q", err, h.ui.Texts())
		}
		if err := h.ui.ChooseMenuItem("Stash Changes…"); err == nil {
			t.Fatal("Stash Changes… of a conflicted file is not disabled")
		}
		h.ui.CloseMenu()
		h.ui.Frame()
		if h.ui.HasText("Stash message") {
			t.Fatal("the dialog opened from the disabled menu item of a conflicted file")
		}
	})

	// Before the first commit there is nothing to restore a stashed file
	// to, so the item is disabled for every file.
	t.Run("unborn repository", func(t *testing.T) {
		repo := newGitRepo(t, "stashsel-menu-unborn")
		repo.WriteFile("loose.txt", "x\n")
		h := openStashApp(t, repo)

		if err := h.ui.RightClick("Unstaged loose.txt"); err != nil {
			t.Fatalf("right-click Unstaged loose.txt: %v\ntexts=%q", err, h.ui.Texts())
		}
		if err := h.ui.ChooseMenuItem("Stash Changes…"); err == nil {
			t.Fatal("Stash Changes… in an unborn repository is not disabled")
		}
		h.ui.CloseMenu()
		h.ui.Frame()
	})
}

// Canceling the dialog drops its draft: the next open — from the sidebar —
// starts at All changes with nothing of the earlier selection left.
func TestStashSelectedDraftResetOnClose(t *testing.T) {
	repo := setupSelectedStashRepo(t, "stashsel-draft")

	h := openStashApp(t, repo)

	chooseFileMenuItem(t, h, "Unstaged c.txt", "Stash Changes…")
	h.ui.Frame()
	if box := mustSaveStashCheck(t, h, "c.txt"); !box.Checked {
		t.Fatal("checkbox of c.txt is unchecked after opening from its menu")
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	openSaveStashDialog(t, h)
	if _, ok := saveStashCheck(t, h, "c.txt"); ok {
		t.Fatal("the file list is visible in the All changes scope: the scope survived the cancel")
	}
	switchSaveStashToSelected(t, h)
	if box := mustSaveStashCheck(t, h, "c.txt"); !box.Checked {
		t.Fatal("checkbox of c.txt is unchecked after the switch: a check mark survived the cancel")
	}
}

// Enter in the message input submits the dialog exactly once, and with no
// file checked it submits nothing.
func TestStashSelectedEnterSubmitsOnce(t *testing.T) {
	repo := setupSelectedStashRepo(t, "stashsel-enter")

	h := openStashApp(t, repo)
	openSaveStashDialog(t, h)
	switchSaveStashToSelected(t, h)
	writeSaveStashMessage(t, h, "enter work")
	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected save error: %v", err)
	}
	if n := stashCount(t, repo.Path()); n != 1 {
		t.Fatalf("stash count = %d, want exactly 1: Enter must submit once", n)
	}

	// The first stash took every file, so rebuild the changes for the next
	// round before opening the dialog again.
	for _, f := range []string{"a.txt", "b.txt", "c.txt"} {
		repo.WriteFile(f, "base\nround two\n")
	}
	repo.WriteFile("new.txt", "brand new\n")
	h.app.session.Reload()
	h.drain()

	// Uncheck everything and press Enter: nothing submits.
	openSaveStashDialog(t, h)
	switchSaveStashToSelected(t, h)
	for _, f := range []string{"a.txt", "b.txt", "c.txt", "new.txt"} {
		if box, ok := saveStashCheck(t, h, f); ok && box.Checked {
			if err := h.ui.Click("Stash " + f); err != nil {
				t.Fatalf("uncheck %q: %v", f, err)
			}
			h.ui.Frame()
		}
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Save Stash"); !ok || !btn.Disabled {
		t.Fatalf("Confirm Save Stash with nothing checked = %+v, want disabled", btn)
	}
	writeSaveStashMessage(t, h, "nothing")
	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if n := stashCount(t, repo.Path()); n != 1 {
		t.Fatalf("stash count = %d, want 1: Enter with nothing checked must not submit", n)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected error after the refused Enter: %v", err)
	}
}

// The file list is virtual: with thousands of changed files the dialog
// still builds only the rows in view, not one checkbox per file.
func TestStashSelectedVirtualList(t *testing.T) {
	repo := newGitRepo(t, "stashsel-virtual")
	repo.CommitFile("seed.txt", "base\n", "initial commit")
	for i := 0; i < 5000; i++ {
		name := fmt.Sprintf("f%04d.txt", i)
		if err := os.WriteFile(filepath.Join(repo.Path(), name), []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	h := openStashApp(t, repo)
	openSaveStashDialog(t, h)
	switchSaveStashToSelected(t, h)

	count := 0
	for _, info := range h.ui.AccessAll() {
		if info.Role == ui.RoleCheckBox && strings.HasPrefix(info.Name, "Stash f") {
			count++
		}
	}
	if count == 0 {
		t.Fatal("no file checkboxes in the accessibility tree at all")
	}
	if count >= 100 {
		t.Fatalf("the dialog built %d file checkboxes for 5000 files, want fewer than 100: the list must be virtual", count)
	}
}

// On a loopback SSH repository the Selected files scope stashes exactly the
// checked files on the server, leaving the unchecked ones untouched.
func TestStashSelectedLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	dir := repo.Path

	for _, f := range []string{"sa.txt", "sb.txt", "sc.txt"} {
		srv.WriteFile(t, dir, f, []byte("base\n"))
		srv.RunGit(t, dir, "add", f)
	}
	srv.RunGit(t, dir, "commit", "-q", "-m", "seed files")
	srv.WriteFile(t, dir, "sa.txt", []byte("base\nchanged a\n"))
	srv.WriteFile(t, dir, "sb.txt", []byte("base\nchanged b\n"))
	srv.RunGit(t, dir, "add", "sb.txt")
	srv.WriteFile(t, dir, "sc.txt", []byte("base\nchanged c\n"))
	srv.WriteFile(t, dir, "snew.txt", []byte("brand new\n"))

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	openSaveStashDialog(t, h)
	switchSaveStashToSelected(t, h)
	for _, f := range []string{"sa.txt", "snew.txt"} {
		if err := h.ui.Click("Stash " + f); err != nil {
			t.Fatalf("uncheck %q: %v\ntexts=%q", f, err, h.ui.Texts())
		}
		h.ui.Frame()
	}
	writeSaveStashMessage(t, h, "ssh selected work")
	confirmSaveStash(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH save error: %v", err)
	}
	if n := len(sshStashLines(t, srv, dir)); n != 1 {
		t.Fatalf("server stash count = %d, want 1", n)
	}
	shown := strings.TrimSpace(srv.RunGit(t, dir, "stash", "show", "--name-status", "stash@{0}"))
	for _, want := range []string{"sb.txt", "sc.txt"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("server stash entry %q does not hold %s", shown, want)
		}
	}
	if strings.Contains(shown, "sa.txt") || strings.Contains(shown, "snew.txt") {
		t.Fatalf("server stash entry %q swept up unchecked files", shown)
	}
	if content := srv.ReadFile(t, dir, "sa.txt"); string(content) != "base\nchanged a\n" {
		t.Fatalf("server sa.txt = %q, want the unchecked change kept", string(content))
	}
	if !sshPathExists(t, srv, dir+"/snew.txt") {
		t.Fatal("snew.txt vanished from the server though it was unchecked")
	}
	if out := srv.RunGit(t, dir, "--literal-pathspecs", "status", "--porcelain", "--", "sb.txt"); strings.TrimSpace(out) != "" {
		t.Fatalf("server status of sb.txt = %q, want clean — neither staged nor modified", out)
	}
}

// On a configured Linux SSH container (sshlab), the Selected files scope
// stashes the checked modified file, staged deletion and staged rename while
// leaving unchecked tracked and untracked files untouched, and applying the
// stash restores the deletion as unstaged and the rename with intact content.
func TestConfiguredLinuxStashSelected(t *testing.T) {
	repo, srv := configuredLinuxRepo(t)
	dir := repo.Path

	for _, f := range []string{"sa.txt", "sb.txt", "sdel.txt", "sold.txt"} {
		srv.WriteFile(t, dir, f, []byte("base "+f+"\n"))
		srv.RunGit(t, dir, "add", f)
	}
	srv.RunGit(t, dir, "commit", "-q", "-m", "seed files")
	srv.WriteFile(t, dir, "sa.txt", []byte("base sa.txt\nkeep unstaged\n"))
	srv.WriteFile(t, dir, "sb.txt", []byte("base sb.txt\nstaged edit\n"))
	srv.RunGit(t, dir, "add", "sb.txt")
	srv.RunGit(t, dir, "rm", "-q", "sdel.txt")
	srv.RunGit(t, dir, "mv", "sold.txt", "sren.txt")
	srv.WriteFile(t, dir, "snew.txt", []byte("brand new\n"))

	h := newTestApp(t)
	openSSHRepoThroughSwitcher(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	openSaveStashDialog(t, h)
	switchSaveStashToSelected(t, h)
	for _, f := range []string{"sa.txt", "snew.txt"} {
		if err := h.ui.Click("Stash " + f); err != nil {
			t.Fatalf("uncheck %q: %v\ntexts=%q", f, err, h.ui.Texts())
		}
		h.ui.Frame()
	}
	writeSaveStashMessage(t, h, "linux selected work")
	confirmSaveStash(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected Linux SSH save error: %v", err)
	}
	if n := len(sshStashLines(t, srv, dir)); n != 1 {
		t.Fatalf("server stash count = %d, want 1", n)
	}
	if content := string(srv.ReadFile(t, dir, "sa.txt")); content != "base sa.txt\nkeep unstaged\n" {
		t.Fatalf("server sa.txt = %q, want unchecked edit kept", content)
	}
	if !remotePathExists(t, srv, dir, "snew.txt") {
		t.Fatal("snew.txt vanished from the server though it was unchecked")
	}
	if content := string(srv.ReadFile(t, dir, "sdel.txt")); content != "base sdel.txt\n" {
		t.Fatalf("server sdel.txt after stash = %q, want committed content restored", content)
	}
	if content := string(srv.ReadFile(t, dir, "sold.txt")); content != "base sold.txt\n" {
		t.Fatalf("server sold.txt after stash = %q, want committed content restored", content)
	}
	if remotePathExists(t, srv, dir, "sren.txt") {
		t.Fatal("sren.txt survived the stash on the server")
	}
	for _, p := range []string{"sb.txt", "sdel.txt", "sold.txt", "sren.txt"} {
		if out := srv.RunGit(t, dir, "--literal-pathspecs", "status", "--porcelain", "--", p); strings.TrimSpace(out) != "" {
			t.Fatalf("server status of %s after stash = %q, want clean", p, out)
		}
	}

	// Applying the stash back restores the changes: deletions return as
	// unstaged deletions (because staged deletions are unstaged before the
	// partial push) and the renamed file keeps its exact content.
	srv.RunGit(t, dir, "stash", "apply", "--index", "stash@{0}")
	if remotePathExists(t, srv, dir, "sdel.txt") {
		t.Fatal("sdel.txt still exists on the server after stash apply")
	}
	if got := srv.RunGit(t, dir, "status", "--porcelain", "--", "sdel.txt"); got != " D sdel.txt" {
		t.Fatalf("status of sdel.txt after apply = %q, want unstaged deletion", got)
	}
	if remotePathExists(t, srv, dir, "sold.txt") {
		t.Fatal("sold.txt still exists on the server after stash apply")
	}
	if got := srv.RunGit(t, dir, "status", "--porcelain", "--", "sold.txt"); got != " D sold.txt" {
		t.Fatalf("status of sold.txt after apply = %q, want unstaged deletion", got)
	}
	if content := string(srv.ReadFile(t, dir, "sren.txt")); content != "base sold.txt\n" {
		t.Fatalf("server sren.txt after apply = %q, want renamed content restored", content)
	}
	if content := string(srv.ReadFile(t, dir, "sb.txt")); content != "base sb.txt\nstaged edit\n" {
		t.Fatalf("server sb.txt after apply = %q, want staged edit restored", content)
	}
}
