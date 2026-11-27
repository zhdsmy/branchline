package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// stashListLines returns the entries of `git stash list` of the repository at
// dir, newest first, or nil when the stash is empty.
func stashListLines(t *testing.T, dir string) []string {
	t.Helper()
	out := strings.TrimSpace(runGitCommand(t, dir, "stash", "list"))
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// stashCount returns how many entries `git stash list` shows in dir.
func stashCount(t *testing.T, dir string) int {
	t.Helper()
	return len(stashListLines(t, dir))
}

// stashHashes returns the commit hashes of the stash entries of dir, newest
// first.
func stashHashes(t *testing.T, dir string) []string {
	t.Helper()
	out := strings.TrimSpace(runGitCommand(t, dir, "stash", "list", "--format=%H"))
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// stashHasHash reports whether the stash of dir holds an entry with the
// commit hash.
func stashHasHash(t *testing.T, dir, hash string) bool {
	t.Helper()
	for _, h := range stashHashes(t, dir) {
		if h == hash {
			return true
		}
	}
	return false
}

// pushStashCLI stashes the working tree of repo through the Git CLI with the
// given message, including untracked files, and returns the new stash entry's
// commit hash.
func pushStashCLI(t *testing.T, repo *gitRepoFixture, message string) string {
	t.Helper()
	repo.Git("stash", "push", "--include-untracked", "-m", message)
	return strings.TrimSpace(repo.Git("rev-parse", "stash@{0}"))
}

// setupStashRepo builds a repository on branch main with one commit and one
// stash entry already pushed, so the sidebar has an entry to select.
func setupStashRepo(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.WriteFile("a.txt", "one\nstashed change\n")
	repo.WriteFile("untracked.txt", "stashed untracked\n")
	pushStashCLI(t, repo, "first stash")
	return repo
}

// openStashApp opens repo in a fresh harness with the snapshot loaded.
func openStashApp(t *testing.T, repo *gitRepoFixture) *testHarness {
	t.Helper()
	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	return h
}

// clickSelectedStashRow selects the stash entry named name (for example
// "stash@{0}") in the sidebar through its accessibility label.
func clickStashRow(t *testing.T, h *testHarness, name string) {
	t.Helper()
	if err := h.ui.Click("Stash " + name); err != nil {
		t.Fatalf("click stash row %q: %v", name, err)
	}
	h.ui.Frame()
}

// The Save Stash dialog stashes the working tree with an optional message:
// with untracked files included they leave the worktree and enter the stash,
// without them they stay in place, and the dialog explains instead of
// submitting when nothing matches the chosen options — while the write
// worker re-verifies the live working tree so a tree cleaned behind the
// dialog's back errors instead of silently succeeding.
func TestStashSaveWithMessageAndUntrackedToggle(t *testing.T) {
	// With "Include untracked files" ticked (the default) the untracked file
	// enters the stash and leaves the worktree; the custom message shows in
	// the sidebar.
	t.Run("untracked included with message", func(t *testing.T) {
		repo := newGitRepo(t, "stash-save-inc")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.WriteFile("a.txt", "one\ntwo\n")
		repo.WriteFile("new.txt", "brand new\n")

		h := openStashApp(t, repo)
		if err := h.ui.Click("Save Stash"); err != nil {
			t.Fatalf("click Save Stash: %v", err)
		}
		h.ui.Frame()
		h.ui.Type("early work")
		h.ui.Frame()
		if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Save Stash"); !ok || btn.Disabled {
			t.Fatalf("Confirm Save Stash with changes = %+v (found=%v), want enabled", btn, ok)
		}
		if err := h.ui.Click("Confirm Save Stash"); err != nil {
			t.Fatalf("click Confirm Save Stash: %v", err)
		}
		h.drain()

		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("unexpected save error: %v", err)
		}
		if n := stashCount(t, repo.Path()); n != 1 {
			t.Fatalf("stash count = %d, want 1", n)
		}
		if out := stashListLines(t, repo.Path())[0]; !strings.Contains(out, "early work") {
			t.Fatalf("stash entry %q, want the custom message", out)
		}
		if data, err := os.ReadFile(filepath.Join(repo.Path(), "a.txt")); err != nil || string(data) != "one\n" {
			t.Fatalf("a.txt after stash = %q, %v; want clean checkout content", string(data), err)
		}
		if _, err := os.Stat(filepath.Join(repo.Path(), "new.txt")); !os.IsNotExist(err) {
			t.Fatalf("new.txt still in worktree after untracked stash (err=%v)", err)
		}
		if row, ok := findAccessNode(t, h.ui, "Stash stash@{0}"); !ok {
			t.Fatalf("new stash row missing from sidebar; texts=%q", h.ui.Texts())
		} else if row.Selected {
			t.Fatal("new stash row should not be selected")
		}
		if h.ui.HasText("Stash message") {
			t.Fatal("Save Stash dialog stayed open after a successful save")
		}
	})

	// With the untracked box unticked the untracked file stays in the
	// worktree while the tracked modification is stashed.
	t.Run("untracked excluded stays in worktree", func(t *testing.T) {
		repo := newGitRepo(t, "stash-save-exc")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.WriteFile("a.txt", "one\ntwo\n")
		repo.WriteFile("new.txt", "brand new\n")

		h := openStashApp(t, repo)
		if err := h.ui.Click("Save Stash"); err != nil {
			t.Fatalf("click Save Stash: %v", err)
		}
		h.ui.Frame()
		if err := h.ui.Click("Include untracked files"); err != nil {
			t.Fatalf("untick Include untracked files: %v", err)
		}
		h.ui.Frame()
		h.ui.Type("tracked only")
		h.ui.Frame()
		if err := h.ui.Click("Confirm Save Stash"); err != nil {
			t.Fatalf("click Confirm Save Stash: %v", err)
		}
		h.drain()

		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("unexpected save error: %v", err)
		}
		if n := stashCount(t, repo.Path()); n != 1 {
			t.Fatalf("stash count = %d, want 1", n)
		}
		if data, err := os.ReadFile(filepath.Join(repo.Path(), "new.txt")); err != nil || string(data) != "brand new\n" {
			t.Fatalf("new.txt = %q, %v; want the untracked file untouched in the worktree", string(data), err)
		}
		if data, err := os.ReadFile(filepath.Join(repo.Path(), "a.txt")); err != nil || string(data) != "one\n" {
			t.Fatalf("a.txt after stash = %q, %v; want clean checkout content", string(data), err)
		}
	})

	// With only untracked files present and the box unticked there is nothing
	// the options would stash: the dialog explains and disables the confirm
	// button.
	t.Run("only untracked with box unticked is refused", func(t *testing.T) {
		repo := newGitRepo(t, "stash-save-only-untracked")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.WriteFile("new.txt", "brand new\n")

		h := openStashApp(t, repo)
		if err := h.ui.Click("Save Stash"); err != nil {
			t.Fatalf("click Save Stash: %v", err)
		}
		h.ui.Frame()
		// The default tick would take the untracked file, so the confirm
		// button starts enabled.
		if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Save Stash"); !ok || btn.Disabled {
			t.Fatalf("Confirm Save Stash with untracked included = %+v, want enabled", btn)
		}
		if err := h.ui.Click("Include untracked files"); err != nil {
			t.Fatalf("untick Include untracked files: %v", err)
		}
		h.ui.Frame()
		if !h.ui.HasText("No tracked local changes to stash.") {
			t.Fatalf("no-tracked-changes explanation missing; texts=%q", h.ui.Texts())
		}
		if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Save Stash"); !ok || !btn.Disabled {
			t.Fatalf("Confirm Save Stash with only untracked excluded = %+v, want disabled", btn)
		}
		if err := h.ui.Click("Cancel"); err != nil {
			t.Fatalf("click Cancel: %v", err)
		}
		h.ui.Frame()
		if n := stashCount(t, repo.Path()); n != 0 {
			t.Fatalf("stash count = %d, want 0 after cancel", n)
		}
	})

	// A clean worktree has nothing to stash: the dialog explains and disables
	// the confirm button.
	t.Run("clean worktree is refused", func(t *testing.T) {
		repo := newGitRepo(t, "stash-save-clean")
		repo.CommitFile("a.txt", "one\n", "initial commit")

		h := openStashApp(t, repo)
		if err := h.ui.Click("Save Stash"); err != nil {
			t.Fatalf("click Save Stash: %v", err)
		}
		h.ui.Frame()
		if !h.ui.HasText("No local changes to stash.") {
			t.Fatalf("no-changes explanation missing; texts=%q", h.ui.Texts())
		}
		if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Save Stash"); !ok || !btn.Disabled {
			t.Fatalf("Confirm Save Stash on clean tree = %+v, want disabled", btn)
		}
	})

	// A repository before its first commit cannot stash: the dialog explains
	// and disables the confirm button.
	t.Run("unborn repository is refused", func(t *testing.T) {
		repo := newGitRepo(t, "stash-save-unborn")
		repo.WriteFile("a.txt", "one\n")

		h := openStashApp(t, repo)
		if err := h.ui.Click("Save Stash"); err != nil {
			t.Fatalf("click Save Stash: %v", err)
		}
		h.ui.Frame()
		if !h.ui.HasText("Cannot stash before the first commit.") {
			t.Fatalf("unborn explanation missing; texts=%q", h.ui.Texts())
		}
		if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Save Stash"); !ok || !btn.Disabled {
			t.Fatalf("Confirm Save Stash on unborn repository = %+v, want disabled", btn)
		}
	})

	// The dialog's answer comes from the snapshot and can go stale: when the
	// worktree is cleaned behind the open dialog's back, the write worker's
	// live re-check refuses the push, returns an error shown in the UI, and
	// creates no stash — instead of git stash push exiting 0 with nothing
	// saved.
	t.Run("live guard refuses a cleaned worktree", func(t *testing.T) {
		repo := newGitRepo(t, "stash-save-stale")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.WriteFile("a.txt", "one\ntwo\n")

		h := openStashApp(t, repo)
		if err := h.ui.Click("Save Stash"); err != nil {
			t.Fatalf("click Save Stash: %v", err)
		}
		h.ui.Frame()
		if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Save Stash"); !ok || btn.Disabled {
			t.Fatalf("Confirm Save Stash with changes = %+v, want enabled", btn)
		}
		// Clean the worktree externally after the dialog opened; the snapshot
		// the dialog reads is never reloaded, so the button stays enabled.
		repo.Git("checkout", "--", "a.txt")
		if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Save Stash"); !ok || btn.Disabled {
			t.Fatalf("Confirm Save Stash after external clean = %+v, want still enabled from the stale snapshot", btn)
		}
		if err := h.ui.Click("Confirm Save Stash"); err != nil {
			t.Fatalf("click Confirm Save Stash: %v", err)
		}
		h.drain()

		if err := h.app.session.ActionErr(); err == nil {
			t.Fatal("save on a cleaned worktree produced no ActionErr")
		} else if !strings.Contains(err.Error(), "No local changes to stash") {
			t.Fatalf("ActionErr = %q, want the live guard's no-changes refusal", err)
		}
		if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
			t.Fatalf("no Repository Error status after refused save; texts=%q", h.ui.Texts())
		}
		if n := stashCount(t, repo.Path()); n != 0 {
			t.Fatalf("stash count = %d, want 0: the refused push must not create an entry", n)
		}
	})

	// The same live re-check covers the untracked-excluded path: with the box
	// unticked and the tracked change reverted behind the open dialog's back,
	// only an untracked file remains live, and the worker must refuse rather
	// than silently succeed with nothing stashed — git stash push exits 0
	// either way.
	t.Run("live guard refuses untracked-only with the box unticked", func(t *testing.T) {
		repo := newGitRepo(t, "stash-save-stale-untracked")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.WriteFile("a.txt", "one\ntwo\n")
		repo.WriteFile("new.txt", "brand new\n")

		h := openStashApp(t, repo)
		if err := h.ui.Click("Save Stash"); err != nil {
			t.Fatalf("click Save Stash: %v", err)
		}
		h.ui.Frame()
		if err := h.ui.Click("Include untracked files"); err != nil {
			t.Fatalf("untick Include untracked files: %v", err)
		}
		h.ui.Frame()
		if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Save Stash"); !ok || btn.Disabled {
			t.Fatalf("Confirm Save Stash with tracked changes = %+v, want enabled", btn)
		}
		// Revert the only tracked change externally; the dialog's snapshot is
		// never reloaded, so the button stays enabled while live only the
		// untracked file remains.
		repo.Git("checkout", "--", "a.txt")
		if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Save Stash"); !ok || btn.Disabled {
			t.Fatalf("Confirm Save Stash after external revert = %+v, want still enabled from the stale snapshot", btn)
		}
		if err := h.ui.Click("Confirm Save Stash"); err != nil {
			t.Fatalf("click Confirm Save Stash: %v", err)
		}
		h.drain()

		if err := h.app.session.ActionErr(); err == nil {
			t.Fatal("save with only untracked changes live produced no ActionErr")
		} else if !strings.Contains(err.Error(), "No tracked local changes to stash") {
			t.Fatalf("ActionErr = %q, want the live guard's no-tracked-changes refusal", err)
		}
		if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
			t.Fatalf("no Repository Error status after refused save; texts=%q", h.ui.Texts())
		}
		if n := stashCount(t, repo.Path()); n != 0 {
			t.Fatalf("stash count = %d, want 0: the refused push must not create an entry", n)
		}
		if data, err := os.ReadFile(filepath.Join(repo.Path(), "new.txt")); err != nil || string(data) != "brand new\n" {
			t.Fatalf("new.txt after refused save = %q, %v; want the untracked file untouched", string(data), err)
		}
	})
}

// The Stashes sidebar section collapses independently of the other sections,
// a click selects a row and expands its inline action bar, a second click
// collapses it again, and switching repositories clears the selection and
// closes every open stash dialog.
func TestStashSidebarCollapseSelectionAndRepoSwitch(t *testing.T) {
	repo := setupStashRepo(t, "stash-side-a")
	repo.CreateTag("v1.0")

	h := openStashApp(t, repo)

	// The section lists the stash newest-first with its reflog identity, and
	// starts unselected.
	if row, ok := findAccessNode(t, h.ui, "Stash stash@{0}"); !ok {
		t.Fatalf("stash row missing; texts=%q", h.ui.Texts())
	} else if row.Selected {
		t.Fatal("stash row starts selected")
	}

	// Collapsing Stashes hides its rows without hiding the other sections.
	if err := h.ui.Click("Stashes"); err != nil {
		t.Fatalf("collapse Stashes: %v", err)
	}
	h.ui.Frame()
	if _, ok := findAccessNode(t, h.ui, "Stash stash@{0}"); ok {
		t.Fatal("stash row stayed visible after collapsing Stashes")
	}
	if _, ok := h.ui.Find("main"); !ok {
		t.Fatal("collapsing Stashes hid Branches")
	}
	if _, ok := h.ui.Find("v1.0"); !ok {
		t.Fatal("collapsing Stashes hid Tags")
	}
	if err := h.ui.Click("Stashes"); err != nil {
		t.Fatalf("re-expand Stashes: %v", err)
	}
	h.ui.Frame()
	if _, ok := findAccessNode(t, h.ui, "Stash stash@{0}"); !ok {
		t.Fatal("stash row did not reappear after expanding Stashes")
	}

	// A click selects the row and opens the inline action bar with the three
	// operations; a second click collapses both.
	clickStashRow(t, h, "stash@{0}")
	if row, ok := findAccessNode(t, h.ui, "Stash stash@{0}"); !ok || !row.Selected {
		t.Fatalf("stash row after click Selected = %+v (found=%v), want selected", row, ok)
	}
	for _, label := range []string{"Apply…", "Pop", "Drop…"} {
		if _, ok := h.ui.AccessByRole(ui.RoleButton, label); !ok {
			t.Fatalf("inline action %q missing on the selected row; texts=%q", label, h.ui.Texts())
		}
	}
	clickStashRow(t, h, "stash@{0}")
	if row, ok := findAccessNode(t, h.ui, "Stash stash@{0}"); !ok || row.Selected {
		t.Fatalf("stash row after second click Selected = %+v, want unselected", row)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Pop"); ok {
		t.Fatal("inline action bar stayed open after deselecting the row")
	}

	// Open the Apply dialog and select the row, then switch repositories: the
	// generation change closes the dialog and clears the selection.
	clickStashRow(t, h, "stash@{0}")
	if err := h.ui.Click("Apply…"); err != nil {
		t.Fatalf("click Apply…: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("Delete stash after applying") {
		t.Fatalf("Apply Stash dialog did not open; texts=%q", h.ui.Texts())
	}

	repoB := newGitRepo(t, "stash-side-b")
	repoB.CommitFile("b.txt", "b\n", "initial commit")
	repoB.WriteFile("b.txt", "b\nmore\n")
	pushStashCLI(t, repoB, "other repo stash")
	h.openRepo(repoB.StoreRepo())
	h.drain()

	if h.ui.HasText("Delete stash after applying") {
		t.Fatal("Apply Stash dialog survived the repository switch")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Pop"); ok {
		t.Fatal("inline action bar survived the repository switch")
	}
	if row, ok := findAccessNode(t, h.ui, "Stash stash@{0}"); !ok {
		t.Fatalf("other repository's stash row missing after switch; texts=%q", h.ui.Texts())
	} else if row.Selected {
		t.Fatal("stash selection survived the repository switch")
	}

	// A repository without any stash shows the placeholder instead of rows,
	// and without an open repository the Save Stash entry is hidden.
	repoC := newGitRepo(t, "stash-side-c")
	repoC.CommitFile("c.txt", "c\n", "initial commit")
	h.openRepo(repoC.StoreRepo())
	h.drain()
	if !h.ui.HasText("No stashes") {
		t.Fatalf("No stashes placeholder missing for a repository without stashes; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Save Stash"); !ok {
		t.Fatalf("Save Stash entry missing with a repository open; texts=%q", h.ui.Texts())
	}

	empty := newTestApp(t)
	empty.ui.Frame()
	if !empty.ui.HasText("No stashes") {
		t.Fatalf("No stashes placeholder missing without a repository; texts=%q", empty.ui.Texts())
	}
	if _, ok := empty.ui.AccessByRole(ui.RoleButton, "Save Stash"); ok {
		t.Fatal("Save Stash entry is visible without an open repository")
	}
}

// The stash rows share the sidebar's left-aligned visual language and the
// inline action bar stays inside it: the entry text starts beside its icon
// at the row's left edge instead of floating centered, and the selected
// row's Apply, Pop and Drop buttons all end inside the row's right edge.
func TestStashSidebarRowsAndActionBarStayInsideTheSidebar(t *testing.T) {
	repo := newGitRepo(t, "stash-layout")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.WriteFile("a.txt", "one\ntwo\n")
	pushStashCLI(t, repo, "hi")

	h := openStashApp(t, repo)

	// A short message makes centering obvious: the text must start beside
	// the icon near the row's left edge. The stash row and the Save Stash
	// row share one geometry — same height, padding, icon box and gap — so
	// their left edges coincide and the icon and text of the entry sit on
	// the same vertical lines as the Save Stash row's.
	rowInfo, ok := h.ui.Access("Stash stash@{0}")
	if !ok {
		t.Fatalf("stash row missing; texts=%q", h.ui.Texts())
	}
	rowRect := rowInfo.Bounds
	textRect, ok := h.ui.Find("On main: hi")
	if !ok {
		t.Fatalf("stash row message text missing; texts=%q", h.ui.Texts())
	}
	if off := textRect.X - rowRect.X; off >= 40 {
		t.Fatalf("stash message starts %.2f into its row (row X=%.2f, text X=%.2f), want < 40 beside the left icon: the row content is centered instead of left-aligned", off, rowRect.X, textRect.X)
	}
	saveRect, ok := h.ui.Find("Save Stash")
	if !ok {
		t.Fatalf("Save Stash row missing; texts=%q", h.ui.Texts())
	}
	if d := rowRect.X - saveRect.X; d < -1 || d > 1 {
		t.Fatalf("stash row X = %.2f vs Save Stash row X = %.2f: the entries no longer share one left edge", rowRect.X, saveRect.X)
	}

	// Select the row: the inline action bar opens beneath it, and every
	// button must end inside the row's right edge instead of overflowing
	// out of the sidebar.
	clickStashRow(t, h, "stash@{0}")
	rowInfo, ok = h.ui.Access("Stash stash@{0}")
	if !ok {
		t.Fatalf("selected stash row missing; texts=%q", h.ui.Texts())
	}
	right := rowInfo.Bounds.X + rowInfo.Bounds.W
	for _, label := range []string{"Apply…", "Pop", "Drop…"} {
		btn, ok := h.ui.AccessByRole(ui.RoleButton, label)
		if !ok {
			t.Fatalf("inline action %q missing on the selected row; texts=%q", label, h.ui.Texts())
		}
		if btnRight := btn.Bounds.X + btn.Bounds.W; btnRight > right+0.5 {
			t.Fatalf("inline %q right edge = %.2f, beyond the row's right edge %.2f: the action bar overflows the sidebar", label, btnRight, right)
		}
	}
}

// setupIndexedStashRepo builds a repository whose stash entry carries both a
// staged modification of a.txt and an unstaged modification of b.txt, so an
// apply with --index provably restores the index.
func setupIndexedStashRepo(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "a base\n", "initial commit")
	repo.CommitFile("b.txt", "b base\n", "add b")
	repo.WriteFile("a.txt", "a staged\n")
	repo.Git("add", "a.txt")
	repo.WriteFile("b.txt", "b base\nb worktree\n")
	pushStashCLI(t, repo, "indexed work")
	return repo
}

// stagedPaths returns the paths with staged changes in dir.
func stagedPaths(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(runGitCommand(t, dir, "diff", "--cached", "--name-only"))
}

// unstagedPaths returns the tracked paths with unstaged changes in dir.
func unstagedPaths(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(runGitCommand(t, dir, "diff", "--name-only"))
}

// The Apply Stash dialog shows the entry's identity, restores the index with
// its default options and keeps the stash, ticking "Delete stash after
// applying" turns it into a pop, the inline Pop and the context menu Pop
// apply and remove the entry directly, and a write in flight disables every
// entry point so no operation can start re-entrantly.
func TestStashApplyDialogAndDirectPop(t *testing.T) {
	// Applying with the defaults restores the staged modification as staged
	// and keeps the entry in the stash.
	t.Run("apply keeps stash and restores the index", func(t *testing.T) {
		repo := setupIndexedStashRepo(t, "stash-apply-index")
		h := openStashApp(t, repo)

		clickStashRow(t, h, "stash@{0}")
		if err := h.ui.Click("Apply…"); err != nil {
			t.Fatalf("click Apply…: %v", err)
		}
		h.ui.Frame()

		// The dialog identifies the entry and ticks the index restore by
		// default.
		if !h.ui.HasText("stash@{0}") {
			t.Fatalf("dialog does not name the target entry; texts=%q", h.ui.Texts())
		}
		if !h.ui.HasText("indexed work") {
			t.Fatalf("dialog does not show the entry message; texts=%q", h.ui.Texts())
		}
		if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Restore staged changes (--index)"); !ok || !box.Checked {
			t.Fatalf("index checkbox at open = %+v (found=%v), want ticked", box, ok)
		}
		if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Delete stash after applying"); !ok || box.Checked {
			t.Fatalf("delete checkbox at open = %+v (found=%v), want unticked", box, ok)
		}

		if err := h.ui.Click("Confirm Apply Stash"); err != nil {
			t.Fatalf("click Confirm Apply Stash: %v", err)
		}
		h.drain()

		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("unexpected apply error: %v", err)
		}
		if n := stashCount(t, repo.Path()); n != 1 {
			t.Fatalf("stash count after apply = %d, want 1: apply must keep the entry", n)
		}
		if got := stagedPaths(t, repo.Path()); got != "a.txt" {
			t.Fatalf("staged paths after apply = %q, want a.txt restored as staged", got)
		}
		if got := unstagedPaths(t, repo.Path()); got != "b.txt" {
			t.Fatalf("unstaged paths after apply = %q, want b.txt left unstaged", got)
		}
		if h.ui.HasText("Delete stash after applying") {
			t.Fatal("Apply Stash dialog stayed open after a successful apply")
		}
	})

	// Canceling the dialog applies nothing and drops nothing.
	t.Run("cancel applies nothing", func(t *testing.T) {
		repo := setupIndexedStashRepo(t, "stash-apply-cancel")
		h := openStashApp(t, repo)

		// The context menu Apply… opens the same dialog.
		if err := h.ui.RightClick("Stash stash@{0}"); err != nil {
			t.Fatalf("right-click stash row: %v", err)
		}
		if got, want := h.ui.Menu(), []string{"Apply…", "Pop", "-", "Drop…"}; strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("context menu = %v, want %v", got, want)
		}
		if err := h.ui.ChooseMenuItem("Apply…"); err != nil {
			t.Fatalf("choose Apply…: %v", err)
		}
		h.ui.Frame()
		if !h.ui.HasText("Delete stash after applying") {
			t.Fatalf("Apply Stash dialog did not open from the context menu; texts=%q", h.ui.Texts())
		}
		if err := h.ui.Click("Cancel"); err != nil {
			t.Fatalf("click Cancel: %v", err)
		}
		h.ui.Frame()

		if got := stagedPaths(t, repo.Path()); got != "" {
			t.Fatalf("staged paths after cancel = %q, want nothing applied", got)
		}
		if n := stashCount(t, repo.Path()); n != 1 {
			t.Fatalf("stash count after cancel = %d, want 1", n)
		}
	})

	// Ticking "Delete stash after applying" applies and removes the entry:
	// the dialog's pop.
	t.Run("delete after applying pops", func(t *testing.T) {
		repo := setupIndexedStashRepo(t, "stash-apply-pop")
		h := openStashApp(t, repo)

		clickStashRow(t, h, "stash@{0}")
		if err := h.ui.Click("Apply…"); err != nil {
			t.Fatalf("click Apply…: %v", err)
		}
		h.ui.Frame()
		if err := h.ui.Click("Delete stash after applying"); err != nil {
			t.Fatalf("tick Delete stash after applying: %v", err)
		}
		h.ui.Frame()
		if err := h.ui.Click("Confirm Apply Stash"); err != nil {
			t.Fatalf("click Confirm Apply Stash: %v", err)
		}
		h.drain()

		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("unexpected pop error: %v", err)
		}
		if n := stashCount(t, repo.Path()); n != 0 {
			t.Fatalf("stash count after delete-after-apply = %d, want 0", n)
		}
		if got := stagedPaths(t, repo.Path()); got != "a.txt" {
			t.Fatalf("staged paths after pop = %q, want a.txt restored as staged", got)
		}
	})

	// The inline Pop of the selected row applies and removes the entry
	// without a dialog.
	t.Run("inline pop applies and drops", func(t *testing.T) {
		repo := setupIndexedStashRepo(t, "stash-pop-inline")
		h := openStashApp(t, repo)

		clickStashRow(t, h, "stash@{0}")
		if err := h.ui.Click("Pop"); err != nil {
			t.Fatalf("click inline Pop: %v", err)
		}
		h.drain()

		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("unexpected inline pop error: %v", err)
		}
		if n := stashCount(t, repo.Path()); n != 0 {
			t.Fatalf("stash count after inline pop = %d, want 0", n)
		}
		if got := stagedPaths(t, repo.Path()); got != "a.txt" {
			t.Fatalf("staged paths after inline pop = %q, want a.txt restored as staged", got)
		}
	})

	// The context menu Pop applies and removes the entry without a dialog.
	t.Run("context menu pop applies and drops", func(t *testing.T) {
		repo := setupIndexedStashRepo(t, "stash-pop-menu")
		h := openStashApp(t, repo)

		if err := h.ui.RightClick("Stash stash@{0}"); err != nil {
			t.Fatalf("right-click stash row: %v", err)
		}
		if err := h.ui.ChooseMenuItem("Pop"); err != nil {
			t.Fatalf("choose Pop: %v", err)
		}
		h.drain()

		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("unexpected menu pop error: %v", err)
		}
		if n := stashCount(t, repo.Path()); n != 0 {
			t.Fatalf("stash count after menu pop = %d, want 0", n)
		}
		if got := stagedPaths(t, repo.Path()); got != "a.txt" {
			t.Fatalf("staged paths after menu pop = %q, want a.txt restored as staged", got)
		}
	})

	// While a write runs, the inline buttons and the context menu items are
	// disabled, so a second operation cannot start; releasing the first write
	// completes exactly one pop.
	t.Run("busy disables every entry point", func(t *testing.T) {
		repo := setupIndexedStashRepo(t, "stash-pop-busy")
		h := openStashApp(t, repo)

		clickStashRow(t, h, "stash@{0}")
		h.queue.PauseWorkers()
		if err := h.ui.Click("Pop"); err != nil {
			t.Fatalf("click inline Pop: %v", err)
		}
		worker := h.queue.WaitWorker(t)
		h.queue.ResumeWorkers()
		h.ui.Frame()

		if !h.app.session.Busy() {
			t.Fatal("session not busy with the pop held")
		}
		for _, label := range []string{"Apply…", "Pop", "Drop…"} {
			if btn, ok := h.ui.AccessByRole(ui.RoleButton, label); !ok || !btn.Disabled {
				t.Fatalf("inline %q while busy = %+v (found=%v), want disabled", label, btn, ok)
			}
		}
		if err := h.ui.RightClick("Stash stash@{0}"); err != nil {
			t.Fatalf("right-click stash row while busy: %v", err)
		}
		if err := h.ui.ChooseMenuItem("Pop"); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing Pop while busy = %v, want a disabled refusal", err)
		}
		h.ui.CloseMenu()

		h.queue.ReleaseWorker(worker)
		h.drain()
		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("unexpected held pop error: %v", err)
		}
		if n := stashCount(t, repo.Path()); n != 0 {
			t.Fatalf("stash count after the held pop = %d, want 0: exactly one pop ran", n)
		}
	})
}

// A pop whose merge conflicts keeps the stash entry and reports the error in
// the UI, and a pop colliding with an untracked file of the same name keeps
// both the entry and the original file's bytes: neither failure deletes or
// overwrites user data.
func TestStashPopConflictAndUntrackedCollisionKeepStash(t *testing.T) {
	// The stash modifies a.txt where HEAD has since moved: the pop merge
	// conflicts, git keeps the entry, and the error surfaces in the toolbar
	// status.
	t.Run("merge conflict keeps the stash", func(t *testing.T) {
		repo := newGitRepo(t, "stash-pop-conflict")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.WriteFile("a.txt", "stashed version\n")
		stashHash := pushStashCLI(t, repo, "conflict stash")
		repo.WriteFile("a.txt", "newer version\n")
		repo.Git("add", "a.txt")
		repo.Git("commit", "-q", "-m", "move a.txt forward")

		h := openStashApp(t, repo)
		clickStashRow(t, h, "stash@{0}")
		if err := h.ui.Click("Pop"); err != nil {
			t.Fatalf("click inline Pop: %v", err)
		}
		h.drain()

		if err := h.app.session.ActionErr(); err == nil {
			t.Fatal("conflicting pop produced no ActionErr")
		}
		if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
			t.Fatalf("no Repository Error status after conflicting pop; texts=%q", h.ui.Texts())
		}
		if n := stashCount(t, repo.Path()); n != 1 {
			t.Fatalf("stash count after conflicting pop = %d, want 1: the entry must be kept", n)
		}
		if !stashHasHash(t, repo.Path(), stashHash) {
			t.Fatalf("the conflicted entry %s is gone from the stash", stashHash)
		}
		if row, ok := findAccessNode(t, h.ui, "Stash stash@{0}"); !ok {
			t.Fatalf("conflicted entry missing from the sidebar; texts=%q", h.ui.Texts())
		} else if !row.Selected {
			t.Fatal("conflicted entry lost its sidebar selection")
		}
	})

	// The stash holds an untracked file whose name an untracked worktree file
	// already uses: the pop fails, the entry stays, and the worktree file's
	// bytes are untouched.
	t.Run("untracked collision keeps stash and file bytes", func(t *testing.T) {
		repo := newGitRepo(t, "stash-pop-collision")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.WriteFile("u.txt", "stashed bytes\n")
		stashHash := pushStashCLI(t, repo, "untracked stash")
		repo.WriteFile("u.txt", "local original bytes\n")

		h := openStashApp(t, repo)
		clickStashRow(t, h, "stash@{0}")
		if err := h.ui.Click("Pop"); err != nil {
			t.Fatalf("click inline Pop: %v", err)
		}
		h.drain()

		if err := h.app.session.ActionErr(); err == nil {
			t.Fatal("colliding pop produced no ActionErr")
		}
		if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
			t.Fatalf("no Repository Error status after colliding pop; texts=%q", h.ui.Texts())
		}
		if n := stashCount(t, repo.Path()); n != 1 {
			t.Fatalf("stash count after colliding pop = %d, want 1: the entry must be kept", n)
		}
		if !stashHasHash(t, repo.Path(), stashHash) {
			t.Fatalf("the collided entry %s is gone from the stash", stashHash)
		}
		data, err := os.ReadFile(filepath.Join(repo.Path(), "u.txt"))
		if err != nil {
			t.Fatalf("read u.txt: %v", err)
		}
		if string(data) != "local original bytes\n" {
			t.Fatalf("u.txt = %q, want the original bytes %q untouched", string(data), "local original bytes\n")
		}
	})
}

// openDropDialog selects the named stash row and opens its Drop Stash
// confirmation dialog through the inline action bar.
func openDropDialog(t *testing.T, h *testHarness, name string) {
	t.Helper()
	clickStashRow(t, h, name)
	if err := h.ui.Click("Drop…"); err != nil {
		t.Fatalf("click Drop…: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("This cannot be undone") {
		t.Fatalf("Drop Stash dialog did not open for %s; texts=%q", name, h.ui.Texts())
	}
}

// confirmDropDialog clicks the dialog's confirm button and drains the write.
func confirmDropDialog(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Confirm Drop Stash"); err != nil {
		t.Fatalf("click Confirm Drop Stash: %v", err)
	}
	h.drain()
}

// The Drop Stash confirmation never deletes without the confirm click,
// deletes exactly the captured entry when it moved in the reflog, and
// deletes nothing at all — reporting the error instead — when the target
// vanished, its duplicate group changed size, or the stash list cannot be
// read.
func TestStashDropConfirmCancelShiftMissingDuplicateAndReadFailure(t *testing.T) {
	// Canceling the confirmation keeps every entry, whichever way the dialog
	// was opened.
	t.Run("cancel deletes nothing", func(t *testing.T) {
		repo := setupStashRepo(t, "stash-drop-cancel")
		h := openStashApp(t, repo)
		want := stashHashes(t, repo.Path())

		// Open through the context menu this time.
		if err := h.ui.RightClick("Stash stash@{0}"); err != nil {
			t.Fatalf("right-click stash row: %v", err)
		}
		if err := h.ui.ChooseMenuItem("Drop…"); err != nil {
			t.Fatalf("choose Drop…: %v", err)
		}
		h.ui.Frame()
		if !h.ui.HasText("This cannot be undone") {
			t.Fatalf("Drop Stash dialog did not open from the context menu; texts=%q", h.ui.Texts())
		}
		if err := h.ui.Click("Cancel"); err != nil {
			t.Fatalf("click Cancel: %v", err)
		}
		h.ui.Frame()

		if got := stashHashes(t, repo.Path()); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("stash after cancel = %v, want unchanged %v", got, want)
		}
		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("canceling recorded an error: %v", err)
		}
	})

	// Confirming deletes exactly the chosen entry and keeps the others.
	t.Run("confirm deletes precisely", func(t *testing.T) {
		repo := newGitRepo(t, "stash-drop-precise")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.WriteFile("a.txt", "older\n")
		olderHash := pushStashCLI(t, repo, "older stash")
		repo.WriteFile("a.txt", "newer\n")
		newerHash := pushStashCLI(t, repo, "newer stash")

		h := openStashApp(t, repo)
		openDropDialog(t, h, "stash@{1}")
		if !h.ui.HasText("older stash") {
			t.Fatalf("dialog does not show the target message; texts=%q", h.ui.Texts())
		}
		confirmDropDialog(t, h)

		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("unexpected drop error: %v", err)
		}
		if stashHasHash(t, repo.Path(), olderHash) {
			t.Fatal("the confirmed entry survived the drop")
		}
		if !stashHasHash(t, repo.Path(), newerHash) {
			t.Fatal("the other entry was deleted with the target")
		}
		if n := stashCount(t, repo.Path()); n != 1 {
			t.Fatalf("stash count after precise drop = %d, want 1", n)
		}
	})

	// A different entry pushed on top after the dialog opened shifts the
	// target from stash@{0} to stash@{1}: the confirmation still deletes the
	// captured commit and fully keeps the new entry.
	t.Run("shifted target is still the one deleted", func(t *testing.T) {
		repo := newGitRepo(t, "stash-drop-shift")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.WriteFile("a.txt", "target\n")
		targetHash := pushStashCLI(t, repo, "target stash")

		h := openStashApp(t, repo)
		openDropDialog(t, h, "stash@{0}")

		// Another writer pushes a different stash on top while the dialog
		// waits.
		repo.WriteFile("a.txt", "interloper\n")
		interloperHash := pushStashCLI(t, repo, "interloper stash")
		if interloperHash == targetHash {
			t.Fatal("interloper stash must have its own commit")
		}
		confirmDropDialog(t, h)

		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("unexpected shifted drop error: %v", err)
		}
		if stashHasHash(t, repo.Path(), targetHash) {
			t.Fatal("the shifted target survived the drop")
		}
		if !stashHasHash(t, repo.Path(), interloperHash) {
			t.Fatal("the newly pushed entry was deleted instead of kept")
		}
		if n := stashCount(t, repo.Path()); n != 1 {
			t.Fatalf("stash count after shifted drop = %d, want 1", n)
		}
	})

	// The target dropped externally while the dialog waits: the confirmation
	// errors and the entry that shifted into stash@{0} is never deleted in
	// its place.
	t.Run("missing target deletes nothing", func(t *testing.T) {
		repo := newGitRepo(t, "stash-drop-missing")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.WriteFile("a.txt", "bystander\n")
		bystanderHash := pushStashCLI(t, repo, "bystander stash")
		repo.WriteFile("a.txt", "victim\n")
		victimHash := pushStashCLI(t, repo, "victim stash")

		h := openStashApp(t, repo)
		// The victim is the newest entry at stash@{0}: open its dialog, then
		// drop it externally, so the bystander shifts up into its position.
		openDropDialog(t, h, "stash@{0}")
		repo.Git("stash", "drop", "stash@{0}")
		confirmDropDialog(t, h)

		// The bystander survival comes first: it is what the scenario
		// exists to prove, so a regression deleting it names the data loss
		// directly.
		if !stashHasHash(t, repo.Path(), bystanderHash) {
			t.Fatal("the bystander shifted into the target's position was deleted in its place")
		}
		if stashHasHash(t, repo.Path(), victimHash) {
			t.Fatal("the externally dropped victim reappeared")
		}
		if err := h.app.session.ActionErr(); err == nil {
			t.Fatal("dropping a missing target produced no ActionErr")
		}
		if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
			t.Fatalf("no Repository Error status after missing-target drop; texts=%q", h.ui.Texts())
		}
		if n := stashCount(t, repo.Path()); n != 1 {
			t.Fatalf("stash count after refused drop = %d, want 1", n)
		}
	})

	// A duplicate of the target's (hash, message) group appearing while the
	// dialog waits changes the group size: the confirmation errors and not a
	// single entry is deleted.
	t.Run("changed duplicate group deletes nothing", func(t *testing.T) {
		repo := newGitRepo(t, "stash-drop-dup")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.WriteFile("a.txt", "target\n")
		targetHash := pushStashCLI(t, repo, "duplicated stash")

		h := openStashApp(t, repo)
		openDropDialog(t, h, "stash@{0}")

		// Push a different stash, then store the target's commit a second
		// time with its exact reflog message: the group grows from 1 to 2.
		targetMessage := strings.TrimSpace(repo.Git("stash", "list", "--format=%gs", "-1"))
		repo.WriteFile("a.txt", "in between\n")
		pushStashCLI(t, repo, "in between stash")
		repo.Git("stash", "store", "-m", targetMessage, targetHash)
		if n := stashCount(t, repo.Path()); n != 3 {
			t.Fatalf("stash count after external duplication = %d, want 3", n)
		}
		confirmDropDialog(t, h)

		if err := h.app.session.ActionErr(); err == nil {
			t.Fatal("dropping with a changed duplicate group produced no ActionErr")
		}
		if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
			t.Fatalf("no Repository Error status after duplicate-group refusal; texts=%q", h.ui.Texts())
		}
		if n := stashCount(t, repo.Path()); n != 3 {
			t.Fatalf("stash count after duplicate-group refusal = %d, want 3: nothing may be deleted", n)
		}
		duplicates := 0
		for _, hash := range stashHashes(t, repo.Path()) {
			if hash == targetHash {
				duplicates++
			}
		}
		if duplicates != 2 {
			t.Fatalf("target hash occurrences = %d, want 2: the duplicate entries must survive", duplicates)
		}
	})

	// A stash list that cannot be read — here the object store moved away
	// after the dialog opened, verified with the Git CLI — fails the
	// confirmation with an error and deletes nothing.
	t.Run("read failure deletes nothing", func(t *testing.T) {
		repo := newGitRepo(t, "stash-drop-unreadable")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.WriteFile("a.txt", "target\n")
		pushStashCLI(t, repo, "target stash")

		h := openStashApp(t, repo)
		openDropDialog(t, h, "stash@{0}")

		objects := filepath.Join(repo.Path(), ".git", "objects")
		away := filepath.Join(repo.Path(), ".git", "objects-away")
		if err := os.Rename(objects, away); err != nil {
			t.Fatalf("move object store away: %v", err)
		}
		restored := false
		restore := func() {
			if !restored {
				if err := os.Rename(away, objects); err != nil {
					t.Fatalf("restore object store: %v", err)
				}
				restored = true
			}
		}
		defer restore()
		if _, err := tryGitCommand(repo.Path(), "stash", "list"); err == nil {
			t.Fatal("git stash list still succeeds without the object store; the failure scenario is invalid")
		}
		confirmDropDialog(t, h)
		restore()

		if err := h.app.session.ActionErr(); err == nil {
			t.Fatal("dropping with an unreadable stash list produced no ActionErr")
		} else if !strings.Contains(err.Error(), "read stash list") {
			t.Fatalf("ActionErr = %q, want the guard's read failure wording", err)
		}
		if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
			t.Fatalf("no Repository Error status after unreadable drop; texts=%q", h.ui.Texts())
		}
		if n := stashCount(t, repo.Path()); n != 1 {
			t.Fatalf("stash count after unreadable drop = %d, want 1: nothing may be deleted", n)
		}
	})
}

// sshStashLines returns the entries of `git stash list` of the repository at
// remoteDir on the server, newest first, or nil when the stash is empty.
func sshStashLines(t *testing.T, srv *sshtest.Target, remoteDir string) []string {
	t.Helper()
	out := strings.TrimSpace(srv.RunGit(t, remoteDir, "stash", "list"))
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// sshStashHashes returns the commit hashes of the stash entries of remoteDir
// on the server, newest first.
func sshStashHashes(t *testing.T, srv *sshtest.Target, remoteDir string) []string {
	t.Helper()
	out := strings.TrimSpace(srv.RunGit(t, remoteDir, "stash", "list", "--format=%H"))
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// sshStashHasHash reports whether the stash of remoteDir on the server holds
// an entry with the commit hash.
func sshStashHasHash(t *testing.T, srv *sshtest.Target, remoteDir, hash string) bool {
	t.Helper()
	for _, h := range sshStashHashes(t, srv, remoteDir) {
		if h == hash {
			return true
		}
	}
	return false
}

// sshPathExists reports whether path exists on the server.
func sshPathExists(t *testing.T, srv *sshtest.Target, path string) bool {
	t.Helper()
	out := srv.RunShell(t, "test -e "+git.ShellQuote(path)+" && echo present || echo missing")
	return strings.Contains(out, "present")
}

// Save, Apply, Pop and Drop on a loopback SSH repository run their git on the
// server: the save takes the message and the untracked file, the apply keeps
// the entry while restoring the worktree, the pop applies and removes it, and
// the confirmed drop deletes exactly the entry the dialog named.
func TestStashSaveApplyPopAndDropLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	dir := repo.Path

	// The changes to stash exist before the app opens, so the loaded snapshot
	// the Save Stash dialog reads already knows them.
	srv.WriteFile(t, dir, "a.txt", []byte("one\nssh stashed work\n"))
	srv.WriteFile(t, dir, "untracked-ssh.txt", []byte("ssh untracked bytes\n"))

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	// Save with a message and the untracked file included.
	if err := h.ui.Click("Save Stash"); err != nil {
		t.Fatalf("click Save Stash: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("ssh stash message")
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Save Stash"); !ok || btn.Disabled {
		t.Fatalf("Confirm Save Stash with SSH changes = %+v (found=%v), want enabled", btn, ok)
	}
	if err := h.ui.Click("Confirm Save Stash"); err != nil {
		t.Fatalf("click Confirm Save Stash: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH save error: %v", err)
	}
	lines := sshStashLines(t, srv, dir)
	if len(lines) != 1 || !strings.Contains(lines[0], "ssh stash message") {
		t.Fatalf("server stash list = %v, want one entry with the custom message", lines)
	}
	if out := srv.RunGit(t, dir, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Fatalf("server worktree after save = %q, want clean", out)
	}
	if sshPathExists(t, srv, dir+"/untracked-ssh.txt") {
		t.Fatal("untracked-ssh.txt still on the server after the untracked stash")
	}
	if _, ok := findAccessNode(t, h.ui, "Stash stash@{0}"); !ok {
		t.Fatalf("saved entry missing from the SSH sidebar; texts=%q", h.ui.Texts())
	}

	// Apply keeps the entry and restores the worktree, including the
	// untracked file.
	clickStashRow(t, h, "stash@{0}")
	if err := h.ui.Click("Apply…"); err != nil {
		t.Fatalf("click Apply…: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("ssh stash message") {
		t.Fatalf("SSH Apply dialog does not show the entry message; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Apply Stash"); err != nil {
		t.Fatalf("click Confirm Apply Stash: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH apply error: %v", err)
	}
	if n := len(sshStashLines(t, srv, dir)); n != 1 {
		t.Fatalf("server stash count after apply = %d, want 1: apply must keep the entry", n)
	}
	if content := srv.ReadFile(t, dir, "a.txt"); string(content) != "one\nssh stashed work\n" {
		t.Fatalf("server a.txt after apply = %q, want the stashed content", string(content))
	}
	if !sshPathExists(t, srv, dir+"/untracked-ssh.txt") {
		t.Fatal("untracked-ssh.txt was not restored by the apply")
	}

	// Save the applied worktree again through the UI: the reload after the
	// apply already knows these changes, so the dialog can submit.
	if err := h.ui.Click("Save Stash"); err != nil {
		t.Fatalf("click Save Stash again: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("ssh drop candidate")
	h.ui.Frame()
	if err := h.ui.Click("Confirm Save Stash"); err != nil {
		t.Fatalf("click Confirm Save Stash again: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected second SSH save error: %v", err)
	}
	hashes := sshStashHashes(t, srv, dir)
	if len(hashes) != 2 {
		t.Fatalf("server stash hashes after second save = %v, want two entries", hashes)
	}
	secondHash, firstHash := hashes[0], hashes[1]

	// Pop the older entry through the context menu: it applies and only that
	// entry is removed.
	if err := h.ui.RightClick("Stash stash@{1}"); err != nil {
		t.Fatalf("right-click SSH stash row: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Pop"); err != nil {
		t.Fatalf("choose Pop on SSH: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH pop error: %v", err)
	}
	if sshStashHasHash(t, srv, dir, firstHash) {
		t.Fatal("the popped entry survived the SSH pop")
	}
	if !sshStashHasHash(t, srv, dir, secondHash) {
		t.Fatal("the newer entry was removed by the pop of the older one")
	}
	if content := srv.ReadFile(t, dir, "a.txt"); string(content) != "one\nssh stashed work\n" {
		t.Fatalf("server a.txt after pop = %q, want the stashed content", string(content))
	}

	// Clean the worktree on the server, then drop the remaining entry through
	// the confirmation dialog.
	srv.RunGit(t, dir, "reset", "--hard", "-q", "HEAD")
	srv.RunShell(t, "rm -f -- "+git.ShellQuote(dir+"/untracked-ssh.txt"))
	if out := srv.RunGit(t, dir, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Fatalf("server worktree after external clean = %q, want clean", out)
	}

	openDropDialog(t, h, "stash@{0}")
	if !h.ui.HasText("ssh drop candidate") {
		t.Fatalf("SSH Drop dialog does not show the target message; texts=%q", h.ui.Texts())
	}
	confirmDropDialog(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH drop error: %v", err)
	}
	if sshStashHasHash(t, srv, dir, secondHash) {
		t.Fatal("the confirmed entry survived the SSH drop")
	}
	if n := len(sshStashLines(t, srv, dir)); n != 0 {
		t.Fatalf("server stash count after drop = %d, want 0", n)
	}
}

// On a loopback SSH repository, a conflicting pop keeps the entry and reports
// the error, and the drop confirmation never deletes when the target vanished
// or the stash list cannot be read on the server.
func TestStashPopConflictAndDropReadFailureLoopbackSSH(t *testing.T) {
	// The stash modifies a.txt where the server's HEAD has since moved: the
	// pop merge conflicts, the entry stays on the server, and the error
	// surfaces in the UI.
	t.Run("pop conflict keeps the stash on the server", func(t *testing.T) {
		repo, srv := loopbackSSHRepo(t)
		dir := repo.Path

		// The fixture stash is pushed on the server before the app opens.
		srv.WriteFile(t, dir, "a.txt", []byte("one\nssh conflict stash\n"))
		srv.RunGit(t, dir, "stash", "push", "--include-untracked", "-m", "ssh conflict stash")
		stashHash := strings.TrimSpace(srv.RunGit(t, dir, "rev-parse", "stash@{0}"))
		// Move the server's HEAD past the stashed change.
		srv.WriteFile(t, dir, "a.txt", []byte("one\nserver moved forward\n"))
		srv.RunGit(t, dir, "add", "a.txt")
		srv.RunGit(t, dir, "commit", "-q", "-m", "move a.txt forward")

		h := newTestApp(t)
		h.openRepo(repo)
		h.drain()

		clickStashRow(t, h, "stash@{0}")
		if err := h.ui.Click("Pop"); err != nil {
			t.Fatalf("click inline Pop: %v", err)
		}
		h.drain()

		if err := h.app.session.ActionErr(); err == nil {
			t.Fatal("conflicting SSH pop produced no ActionErr")
		}
		if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
			t.Fatalf("no Repository Error status after conflicting SSH pop; texts=%q", h.ui.Texts())
		}
		if !sshStashHasHash(t, srv, dir, stashHash) {
			t.Fatal("the conflicted entry was dropped on the server despite the merge conflict")
		}
	})

	// The victim entry dropped on the server while its confirmation waits:
	// confirming errors and the entry shifting into the victim's old position
	// is never deleted in its place.
	t.Run("drop with a missing target deletes nothing on the server", func(t *testing.T) {
		repo, srv := loopbackSSHRepo(t)
		dir := repo.Path

		srv.WriteFile(t, dir, "a.txt", []byte("one\nbystander\n"))
		srv.RunGit(t, dir, "stash", "push", "--include-untracked", "-m", "bystander stash")
		srv.WriteFile(t, dir, "a.txt", []byte("one\nvictim\n"))
		srv.RunGit(t, dir, "stash", "push", "--include-untracked", "-m", "victim stash")
		hashes := sshStashHashes(t, srv, dir)
		if len(hashes) != 2 {
			t.Fatalf("server stash hashes = %v, want two entries", hashes)
		}
		victimHash := hashes[0]
		bystanderHash := hashes[1]

		h := newTestApp(t)
		h.openRepo(repo)
		h.drain()

		// The victim is the newest entry at stash@{0}: open its dialog, then
		// drop it externally, so the bystander shifts up into its position.
		openDropDialog(t, h, "stash@{0}")
		srv.RunGit(t, dir, "stash", "drop", "stash@{0}")
		confirmDropDialog(t, h)

		// The bystander survival comes first: it is what the scenario
		// exists to prove, so a regression deleting it names the data loss
		// directly.
		if !sshStashHasHash(t, srv, dir, bystanderHash) {
			t.Fatal("the bystander shifted into the victim's position was deleted on the server in its place")
		}
		if sshStashHasHash(t, srv, dir, victimHash) {
			t.Fatal("the externally dropped victim reappeared on the server")
		}
		if err := h.app.session.ActionErr(); err == nil {
			t.Fatal("SSH drop of a missing target produced no ActionErr")
		}
		if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
			t.Fatalf("no Repository Error status after missing-target SSH drop; texts=%q", h.ui.Texts())
		}
	})

	// The server's object store moved away after the dialog opened — verified
	// to fail `git stash list` on the server — fails the confirmation with an
	// error and deletes nothing.
	t.Run("drop with an unreadable stash list deletes nothing on the server", func(t *testing.T) {
		repo, srv := loopbackSSHRepo(t)
		dir := repo.Path

		srv.WriteFile(t, dir, "a.txt", []byte("one\nssh target\n"))
		srv.RunGit(t, dir, "stash", "push", "--include-untracked", "-m", "ssh target stash")

		h := newTestApp(t)
		h.openRepo(repo)
		h.drain()

		openDropDialog(t, h, "stash@{0}")

		objects := dir + "/.git/objects"
		away := dir + "/.git/objects-away"
		srv.RunShell(t, "mv -- "+git.ShellQuote(objects)+" "+git.ShellQuote(away))
		restored := false
		restore := func() {
			if !restored {
				srv.RunShell(t, "mv -- "+git.ShellQuote(away)+" "+git.ShellQuote(objects))
				restored = true
			}
		}
		defer restore()
		check := srv.RunShell(t, "cd "+git.ShellQuote(dir)+"; git stash list >/dev/null 2>&1; echo rc=$?")
		if strings.TrimSpace(check) == "rc=0" {
			t.Fatalf("server git stash list still succeeds without the object store; scenario invalid: %q", check)
		}
		confirmDropDialog(t, h)
		restore()

		if err := h.app.session.ActionErr(); err == nil {
			t.Fatal("SSH drop with an unreadable stash list produced no ActionErr")
		} else if !strings.Contains(err.Error(), "read stash list") {
			t.Fatalf("ActionErr = %q, want the guard's read failure wording", err)
		}
		if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
			t.Fatalf("no Repository Error status after unreadable SSH drop; texts=%q", h.ui.Texts())
		}
		if n := len(sshStashLines(t, srv, dir)); n != 1 {
			t.Fatalf("server stash count after unreadable drop = %d, want 1: nothing may be deleted", n)
		}
	})
}
