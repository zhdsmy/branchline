package app

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// renameFixture builds a repository with main checked out and the named
// branch one commit ahead of it, so renaming never has to touch the
// checked-out branch unless the test wants it to.
func renameFixture(t *testing.T, name, branch string) (*gitRepoFixture, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.Git("checkout", "-q", "-b", branch)
	tip := repo.CommitFile("work.txt", "work\n", "topic work")
	repo.Git("checkout", "-q", "main")
	return repo, tip
}

// openRenameDialog right-clicks the branch row of the sidebar and chooses
// Rename…, then verifies the dialog opened with the input field the spec
// names.
func openRenameDialog(t *testing.T, h *testHarness, branch string) {
	t.Helper()
	if err := h.ui.RightClick(branch); err != nil {
		t.Fatalf("right-click branch row %q: %v\ntexts=%q", branch, err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem("Rename…"); err != nil {
		t.Fatalf("choose Rename…: %v; menu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Rename Branch") {
		t.Fatalf("rename dialog did not open; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleTextField, "New Branch Name"); !ok {
		t.Fatalf("New Branch Name input missing; texts=%q", h.ui.Texts())
	}
}

// branchHash returns the commit refs/heads/name points at in dir.
func branchHash(t *testing.T, repo *gitRepoFixture, name string) string {
	t.Helper()
	return gitOutTrim(t, repo.Path(), "rev-parse", "refs/heads/"+name)
}

// Renaming a branch through the sidebar context menu rewrites the ref in the
// real repository: the new name points at exactly the commit the branch had,
// the old name is gone, and the upstream configuration moves to the new name
// with it. The menu carries Rename… and Delete… behind a separator after the
// merge and rebase items, then Copy Branch Name behind its own separator.
func TestBranchRenameThroughContextMenu(t *testing.T) {
	repo, tip := renameFixture(t, "rename-basic", "topic")
	repo.AddBareRemote("origin")
	repo.Git("push", "-q", "origin", "topic")
	repo.Git("branch", "--set-upstream-to", "origin/topic", "topic")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("topic"); err != nil {
		t.Fatalf("right-click branch row: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	if len(menu) < 5 {
		t.Fatalf("branch menu %q too short for the management tail", menu)
	}
	tail := menu[len(menu)-5:]
	want := []string{"-", "Rename…", "Delete…", "-", "Copy Branch Name"}
	for i, w := range want {
		if tail[i] != w {
			t.Fatalf("menu %q tail = %q, want %q", menu, tail, want)
		}
	}
	if err := h.ui.ChooseMenuItem("Rename…"); err != nil {
		t.Fatalf("choose Rename…: %v; menu=%q", err, menu)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Rename Branch") {
		t.Fatalf("rename dialog did not open; texts=%q", h.ui.Texts())
	}

	// The input starts from the old name, so confirm stays disabled — the
	// same name is no rename — without scolding the user before they type.
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Rename Branch"); !ok || !btn.Disabled {
		t.Fatalf("Confirm Rename Branch with the untouched name = %+v (found=%v), want disabled", btn, ok)
	}
	if h.ui.HasText("same as the current name") {
		t.Fatalf("same-name problem shown before any edit; texts=%q", h.ui.Texts())
	}
	assertDialogLayout(t, h, "Cancel", "Confirm Rename Branch")

	clearInput(t, h.ui)
	h.ui.Type("topic-2")
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Rename Branch"); !ok || btn.Disabled {
		t.Fatalf("Confirm Rename Branch with a valid name = %+v, want enabled", btn)
	}
	if err := h.ui.Click("Confirm Rename Branch"); err != nil {
		t.Fatalf("click Confirm Rename Branch: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected rename error: %v", err)
	}
	if got := branchHash(t, repo, "topic-2"); got != tip {
		t.Fatalf("refs/heads/topic-2 = %q, want the original commit %q", got, tip)
	}
	if branchExists(t, repo, "topic") {
		t.Fatal("old name topic survived the rename")
	}
	// The upstream configuration moves with the branch: the new name keeps
	// the tracking keys and the old name's keys are gone.
	if got := gitOutTrim(t, repo.Path(), "config", "--get", "branch.topic-2.remote"); got != "origin" {
		t.Fatalf("branch.topic-2.remote = %q, want origin", got)
	}
	if got := gitOutTrim(t, repo.Path(), "config", "--get", "branch.topic-2.merge"); got != "refs/heads/topic" {
		t.Fatalf("branch.topic-2.merge = %q, want refs/heads/topic", got)
	}
	if out, err := tryGitCommand(repo.Path(), "config", "--get", "branch.topic.remote"); err == nil {
		t.Fatalf("branch.topic.remote survived the rename: %q", out)
	}
	if row, ok := findAccessNode(t, h.ui, "topic-2"); !ok {
		t.Fatalf("topic-2 missing from the sidebar after the rename; texts=%q", h.ui.Texts())
	} else if row.Selected {
		t.Fatal("renamed non-current branch shows as current")
	}
	// The remote-tracking origin/topic row shows topic as its last name
	// segment, so the old name's absence is checked by the list item's
	// accessible name rather than raw text.
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, "topic"); ok {
		t.Fatal("old name topic stayed visible as a branch row in the sidebar")
	}
	if hasExactText(h.ui, "Rename Branch") {
		t.Fatal("rename dialog stayed open after the rename")
	}
}

// Renaming the checked-out branch is a normal Git operation: HEAD follows
// the branch to its new name and the sidebar's current-branch emphasis moves
// with it.
func TestBranchRenameCurrentBranch(t *testing.T) {
	repo, _ := renameFixture(t, "rename-current", "topic")
	repo.Git("checkout", "-q", "topic")
	tip := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if got := h.app.session.Snapshot().Status.Branch; got != "topic" {
		t.Fatalf("fixture: current branch = %q, want topic", got)
	}

	openRenameDialog(t, h, "topic")
	clearInput(t, h.ui)
	h.ui.Type("renamed-current")
	h.ui.Frame()
	if err := h.ui.Click("Confirm Rename Branch"); err != nil {
		t.Fatalf("click Confirm Rename Branch: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected rename error: %v", err)
	}
	if got := currentBranch(t, repo); got != "renamed-current" {
		t.Fatalf("current branch after rename = %q, want renamed-current", got)
	}
	if got := branchHash(t, repo, "renamed-current"); got != tip {
		t.Fatalf("refs/heads/renamed-current = %q, want %q", got, tip)
	}
	if branchExists(t, repo, "topic") {
		t.Fatal("old name topic survived the rename")
	}
	if got := h.app.session.Snapshot().Status.Branch; got != "renamed-current" {
		t.Fatalf("snapshot branch = %q, want renamed-current", got)
	}
	if row, ok := findAccessNode(t, h.ui, "renamed-current"); !ok || !row.Selected {
		t.Fatalf("renamed-current row Selected = %+v (found=%v), want selected", row, ok)
	}
}

// The Rename Branch dialog validates the typed name live: an empty name, the
// old name again, characters Git forbids and a collision with another local
// branch each show their own message and disable the Rename button, and a
// name padded with whitespace is trimmed before it is judged.
func TestBranchRenameInputValidation(t *testing.T) {
	repo, _ := renameFixture(t, "rename-validation", "topic")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openRenameDialog(t, h, "topic")

	// Empty name.
	clearInput(t, h.ui)
	h.ui.Frame()
	if !h.ui.HasText("Enter a new branch name") {
		t.Fatalf("empty-name problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Rename Branch"); !ok || !btn.Disabled {
		t.Fatalf("Confirm Rename Branch with empty name = %+v, want disabled", btn)
	}

	// The old name again.
	h.ui.Type("topic")
	h.ui.Frame()
	if !h.ui.HasText("same as the current name") {
		t.Fatalf("same-name problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Rename Branch"); !ok || !btn.Disabled {
		t.Fatalf("Confirm Rename Branch with the old name = %+v, want disabled", btn)
	}

	// Characters Git forbids in a ref.
	clearInput(t, h.ui)
	h.ui.Type("bad..name")
	h.ui.Frame()
	if !h.ui.HasText("Must not contain '..'.") {
		t.Fatalf("refname problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Rename Branch"); !ok || !btn.Disabled {
		t.Fatalf("Confirm Rename Branch with illegal name = %+v, want disabled", btn)
	}

	// Collision with another local branch.
	clearInput(t, h.ui)
	h.ui.Type("main")
	h.ui.Frame()
	if !h.ui.HasText("already exists") {
		t.Fatalf("collision problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Rename Branch"); !ok || !btn.Disabled {
		t.Fatalf("Confirm Rename Branch with colliding name = %+v, want disabled", btn)
	}

	// Leading and trailing whitespace is trimmed before the checks, so a
	// padded valid name enables the button again.
	clearInput(t, h.ui)
	h.ui.Type("  topic-2  ")
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Rename Branch"); !ok || btn.Disabled {
		t.Fatalf("Confirm Rename Branch with padded valid name = %+v, want enabled", btn)
	}

	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if branchExists(t, repo, "topic-2") {
		t.Fatal("topic-2 exists even though the dialog was canceled")
	}
	if !branchExists(t, repo, "topic") {
		t.Fatal("topic is gone even though no rename was confirmed")
	}
}

// A branch deleted outside the app after the dialog opened is never
// resurrected: the worker cannot find it where the user left it, refuses the
// rename, and creates nothing under the new name.
func TestBranchRenameGuardBranchDeletedExternally(t *testing.T) {
	repo, _ := renameFixture(t, "rename-guard-deleted", "topic")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openRenameDialog(t, h, "topic")
	clearInput(t, h.ui)
	h.ui.Type("topic-2")
	h.ui.Frame()

	repo.Git("branch", "-D", "topic")

	if err := h.ui.Click("Confirm Rename Branch"); err != nil {
		t.Fatalf("click Confirm Rename Branch: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("rename of an externally deleted branch produced no error")
	} else if !strings.Contains(err.Error(), "no longer exists") {
		t.Fatalf("ActionErr = %q, want it to say the branch no longer exists", err)
	}
	if branchExists(t, repo, "topic") {
		t.Fatal("topic came back after the refused rename")
	}
	if branchExists(t, repo, "topic-2") {
		t.Fatal("topic-2 was created even though the old branch was gone")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error status after the refused rename; texts=%q", h.ui.Texts())
	}
}

// A branch moved outside the app after the dialog opened is never renamed:
// the commits the user saw and the commits the branch has now are different,
// so the worker refuses and both names keep exactly what they had.
func TestBranchRenameGuardBranchMovedExternally(t *testing.T) {
	repo, tip := renameFixture(t, "rename-guard-moved", "topic")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openRenameDialog(t, h, "topic")
	clearInput(t, h.ui)
	h.ui.Type("topic-2")
	h.ui.Frame()

	repo.Git("checkout", "-q", "topic")
	moved := repo.CommitFile("late.txt", "late\n", "late external work")
	repo.Git("checkout", "-q", "main")

	if err := h.ui.Click("Confirm Rename Branch"); err != nil {
		t.Fatalf("click Confirm Rename Branch: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("rename of an externally moved branch produced no error")
	} else if !strings.Contains(err.Error(), "moved") {
		t.Fatalf("ActionErr = %q, want it to say the branch moved", err)
	}
	if got := branchHash(t, repo, "topic"); got != moved {
		t.Fatalf("refs/heads/topic = %q, want the external commit %q untouched", got, moved)
	}
	if branchExists(t, repo, "topic-2") {
		t.Fatal("topic-2 was created even though the branch had moved")
	}
	if branchHash(t, repo, "topic") == tip {
		t.Fatal("fixture broken: the branch never moved")
	}
}

// A new branch created outside the app under the chosen name after the
// dialog opened is never overwritten: the worker finds the name taken and
// refuses, leaving both branches exactly as they are.
func TestBranchRenameGuardNameTakenExternally(t *testing.T) {
	repo, tip := renameFixture(t, "rename-guard-taken", "topic")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openRenameDialog(t, h, "topic")
	clearInput(t, h.ui)
	h.ui.Type("topic-2")
	h.ui.Frame()

	repo.Git("branch", "topic-2")
	taken := branchHash(t, repo, "topic-2")

	if err := h.ui.Click("Confirm Rename Branch"); err != nil {
		t.Fatalf("click Confirm Rename Branch: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("rename onto an externally taken name produced no error")
	} else if !strings.HasPrefix(err.Error(), "cannot rename topic: a branch named topic-2 already exists") {
		// The guard's own refusal, not Git's late "fatal: a branch named
		// 'topic-2' already exists": the write is rejected before any
		// mutation is attempted.
		t.Fatalf("ActionErr = %q, want the guard's own refusal", err)
	}
	if got := branchHash(t, repo, "topic"); got != tip {
		t.Fatalf("refs/heads/topic = %q, want the original commit %q", got, tip)
	}
	if got := branchHash(t, repo, "topic-2"); got != taken {
		t.Fatalf("refs/heads/topic-2 = %q, want the external branch %q untouched", got, taken)
	}
}

// Renaming a branch of a loopback SSH repository rewrites the ref on the
// server: the new name points at the commit the branch had and the old name
// is gone.
func TestBranchRenameLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	srv.RunGit(t, repo.Path, "checkout", "-q", "-b", "topic")
	srv.WriteFile(t, repo.Path, "work.txt", []byte("work\n"))
	srv.RunGit(t, repo.Path, "add", "work.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "topic work")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	tip := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "refs/heads/topic"))

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()
	if _, ok := h.ui.Find("topic"); !ok {
		t.Fatalf("topic missing from the SSH sidebar; texts=%q", h.ui.Texts())
	}

	openRenameDialog(t, h, "topic")
	clearInput(t, h.ui)
	h.ui.Type("ssh-renamed")
	h.ui.Frame()
	if err := h.ui.Click("Confirm Rename Branch"); err != nil {
		t.Fatalf("click Confirm Rename Branch: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH rename error: %v", err)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "refs/heads/ssh-renamed")); got != tip {
		t.Fatalf("SSH refs/heads/ssh-renamed = %q, want the original commit %q", got, tip)
	}
	if out := srv.RunGit(t, repo.Path, "branch", "--list", "topic"); strings.TrimSpace(out) != "" {
		t.Fatal("old name topic survived the rename on the server")
	}
	if row, ok := findAccessNode(t, h.ui, "ssh-renamed"); !ok {
		t.Fatalf("ssh-renamed missing from the sidebar after the SSH rename; texts=%q", h.ui.Texts())
	} else if row.Selected {
		t.Fatal("renamed non-current branch shows as current over SSH")
	}
}

// Copy Branch Name of a local branch puts exactly its short name on the
// clipboard, slash and all, and stays available while a repository write
// runs: copying reads the name the menu was opened on, nothing of the
// repository's moving state.
func TestBranchCopyNameThroughContextMenu(t *testing.T) {
	repo := newGitRepo(t, "copy-branch-name")
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.Git("branch", "feature/x")
	repo.WriteFile("pending.txt", "pending\n")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("feature/x"); err != nil {
		t.Fatalf("right-click branch row: %v\ntexts=%q", err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem("Copy Branch Name"); err != nil {
		t.Fatalf("choose Copy Branch Name: %v; menu=%q", err, h.ui.Menu())
	}
	if got := h.ui.Clipboard(); got != "feature/x" {
		t.Fatalf("clipboard = %q, want exactly %q", got, "feature/x")
	}

	// While a write is busy the copy item still works.
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	h.queue.PauseWorkers()
	if err := h.ui.Click("Stage pending.txt"); err != nil {
		t.Fatalf("click Stage pending.txt: %v", err)
	}
	writeWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the stage worker is held")
	}
	h.ui.SetClipboard("")
	if err := h.ui.RightClick("feature/x"); err != nil {
		t.Fatalf("right-click branch row while busy: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Copy Branch Name"); err != nil {
		t.Fatalf("Copy Branch Name unavailable while busy: %v", err)
	}
	if got := h.ui.Clipboard(); got != "feature/x" {
		t.Fatalf("clipboard while busy = %q, want %q", got, "feature/x")
	}

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage write failed: %v", err)
	}
}

// Copy Branch Name of a remote-tracking branch puts its full short name on
// the clipboard, remote included, such as origin/feature/x.
func TestBranchCopyNameRemoteBranch(t *testing.T) {
	repo := newGitRepo(t, "copy-remote-name")
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.Git("branch", "feature/x")
	repo.AddBareRemote("origin")
	repo.Git("push", "-q", "origin", "feature/x")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if _, ok := h.ui.Find("origin/feature/x"); !ok {
		t.Fatalf("origin/feature/x missing from the sidebar; texts=%q", h.ui.Texts())
	}

	if err := h.ui.RightClick("origin/feature/x"); err != nil {
		t.Fatalf("right-click remote branch row: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	if len(menu) < 2 || menu[len(menu)-2] != "-" || menu[len(menu)-1] != "Copy Branch Name" {
		t.Fatalf("remote branch menu %q does not end with separator + Copy Branch Name", menu)
	}
	if err := h.ui.ChooseMenuItem("Copy Branch Name"); err != nil {
		t.Fatalf("choose Copy Branch Name: %v; menu=%q", err, menu)
	}
	if got := h.ui.Clipboard(); got != "origin/feature/x" {
		t.Fatalf("clipboard = %q, want exactly %q", got, "origin/feature/x")
	}
}

// Delete… of a branch row's context menu opens the same confirmation dialog
// the row-end minus button opens: the merged check runs, Confirm Delete
// removes the merged branch, and canceling leaves it in place.
func TestBranchMenuDeleteThroughContextMenu(t *testing.T) {
	repo := setupSidebarRepo(t, "menu-delete")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("merged"); err != nil {
		t.Fatalf("right-click branch row merged: %v\ntexts=%q", err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem("Delete…"); err != nil {
		t.Fatalf("choose Delete…: %v; menu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	drainDeleteCheck(t, h)
	if !h.ui.HasText("fully merged") {
		t.Fatalf("merged note not shown; texts=%q", h.ui.Texts())
	}
	// Canceling leaves the branch alone.
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if !branchExists(t, repo, "merged") {
		t.Fatal("merged was deleted even though the dialog was canceled")
	}

	if err := h.ui.RightClick("merged"); err != nil {
		t.Fatalf("right-click branch row merged again: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Delete…"); err != nil {
		t.Fatalf("choose Delete… again: %v", err)
	}
	h.ui.Frame()
	drainDeleteCheck(t, h)
	if err := h.ui.Click("Confirm Delete"); err != nil {
		t.Fatalf("click Confirm Delete: %v", err)
	}
	h.drain()

	if branchExists(t, repo, "merged") {
		t.Fatal("merged branch survived Confirm Delete chosen through the menu")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected delete error: %v", err)
	}
	if !branchExists(t, repo, "feature") {
		t.Fatal("the menu delete removed the wrong branch: feature is gone")
	}
}

// Delete… never applies to the checked-out branch — Git refuses to remove
// it — so the item disables on it, while Rename… stays enabled: renaming the
// current branch is a normal operation.
func TestBranchMenuDeleteDisabledOnCurrentBranch(t *testing.T) {
	repo := setupSidebarRepo(t, "menu-delete-current")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click current branch row: %v\ntexts=%q", err, h.ui.Texts())
	}
	if !hasMenuItem(h.ui, "Delete…") {
		t.Fatalf("menu %q missing the disabled Delete… item", h.ui.Menu())
	}
	if err := h.ui.ChooseMenuItem("Delete…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing Delete… on the current branch = %v, want a disabled error", err)
	}
	if err := h.ui.ChooseMenuItem("Rename…"); err != nil {
		t.Fatalf("Rename… on the current branch = %v, want it enabled", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Rename Branch") {
		t.Fatalf("rename dialog did not open for the current branch; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if !branchExists(t, repo, "main") {
		t.Fatal("main is gone even though Delete… was disabled")
	}
}

// While a repository write runs, Rename… and Delete… are disabled so neither
// write starts over the running one; Copy Branch Name stays available
// because it only reads the name the menu was opened on.
func TestBranchMenuDeleteAndRenameDisabledWhileBusy(t *testing.T) {
	repo := setupSidebarRepo(t, "menu-busy")
	repo.WriteFile("pending.txt", "pending\n")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()

	h.queue.PauseWorkers()
	if err := h.ui.Click("Stage pending.txt"); err != nil {
		t.Fatalf("click Stage pending.txt: %v", err)
	}
	writeWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the stage worker is held")
	}

	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click branch row while busy: %v\ntexts=%q", err, h.ui.Texts())
	}
	for _, item := range []string{"Rename…", "Delete…"} {
		if err := h.ui.ChooseMenuItem(item); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing %q while busy = %v, want a disabled error", item, err)
		}
	}
	if err := h.ui.ChooseMenuItem("Copy Branch Name"); err != nil {
		t.Fatalf("Copy Branch Name unavailable while busy: %v", err)
	}
	if got := h.ui.Clipboard(); got != "feature" {
		t.Fatalf("clipboard while busy = %q, want %q", got, "feature")
	}
	if hasExactText(h.ui, "Rename Branch") {
		t.Fatal("rename dialog opened from a disabled menu item")
	}

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage write failed: %v", err)
	}
}

// hasMenuItem reports whether the open context menu lists label at all,
// enabled or not.
func hasMenuItem(tt *ui.Tester, label string) bool {
	for _, item := range tt.Menu() {
		if item == label {
			return true
		}
	}
	return false
}
