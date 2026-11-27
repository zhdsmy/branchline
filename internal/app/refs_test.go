package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// doubleClick clicks the element labeled s twice within the system
// double-click interval so DoubleClicked fires on the second press.
func doubleClick(t *testing.T, tt *ui.Tester, s string) {
	t.Helper()
	if err := tt.Click(s); err != nil {
		t.Fatalf("double-click %q first click: %v", s, err)
	}
	tt.Frame()
	if err := tt.Click(s); err != nil {
		t.Fatalf("double-click %q second click: %v", s, err)
	}
	tt.Frame()
}

// setupSidebarRepo builds a repository with main checked out, merged at the
// initial commit, feature and unmerged one commit ahead of it, the origin
// remote tracking main, and tag v1.0.
func setupSidebarRepo(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.CreateBranch("merged")
	repo.CreateTag("v1.0")
	// feature gains its own commit.
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("feature.txt", "feature\n", "feature work")
	// unmerged gains a different commit from the same base.
	repo.Git("checkout", "-q", "main")
	repo.CheckoutNewBranch("unmerged")
	repo.CommitFile("unmerged.txt", "unmerged\n", "unmerged work")
	repo.Checkout("main")
	repo.AddBareRemote("origin")
	return repo
}

func (f *gitRepoFixture) CheckoutNewBranch(name string) {
	f.t.Helper()
	f.Git("checkout", "-q", "-b", name)
}

func branchExists(t *testing.T, repo *gitRepoFixture, name string) bool {
	t.Helper()
	_, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

func currentBranch(t *testing.T, repo *gitRepoFixture) string {
	t.Helper()
	return strings.TrimSpace(repo.Git("rev-parse", "--abbrev-ref", "HEAD"))
}

// The Branches/Remotes/Tags sections list the repository's references with
// the current branch emphasized and marked selected for assistive tech, and
// collapsing one section leaves the others visible.
func TestRefsSidebarSectionsAndCurrentMark(t *testing.T) {
	repo := setupSidebarRepo(t, "refs-sections")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	for _, want := range []string{"main", "merged", "feature", "unmerged", "origin", "origin/main", "v1.0"} {
		if _, ok := h.ui.Find(want); !ok {
			t.Fatalf("sidebar missing %q; texts=%q", want, h.ui.Texts())
		}
	}
	// The current branch row exposes a selected state to assistive technology.
	row, ok := findAccessNode(t, h.ui, "main")
	if !ok {
		t.Fatal("no accessible node labeled main")
	}
	if !row.Selected {
		t.Fatalf("current branch main row Selected = false, want true: %+v", row)
	}
	// Non-current branches are not selected.
	if row, ok := findAccessNode(t, h.ui, "feature"); !ok || row.Selected {
		t.Fatalf("feature row Selected = %+v (found=%v), want false", row, ok)
	}

	// Collapsing Branches hides branch rows but not Remotes or Tags.
	if err := h.ui.Click("Branches"); err != nil {
		t.Fatalf("click Branches: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.Find("feature"); ok {
		t.Fatal("feature stayed visible after collapsing Branches")
	}
	if _, ok := h.ui.Find("origin"); !ok {
		t.Fatal("collapsing Branches hid Remotes")
	}
	if _, ok := h.ui.Find("v1.0"); !ok {
		t.Fatal("collapsing Branches hid Tags")
	}
	if err := h.ui.Click("Branches"); err != nil {
		t.Fatalf("re-expand Branches: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.Find("feature"); !ok {
		t.Fatal("feature did not reappear after expanding Branches")
	}
}

// findAccessNode returns the first accessible element labeled s regardless
// of role, preferring exact matches in reading order.
func findAccessNode(t *testing.T, tt *ui.Tester, s string) (accessInfo, bool) {
	t.Helper()
	if info, ok := tt.Access(s); ok {
		return accessInfo{Selected: info.Selected, Disabled: info.Disabled, Busy: info.Busy}, true
	}
	return accessInfo{}, false
}

type accessInfo struct {
	Selected bool
	Disabled bool
	Busy     bool
}

// doubleClickCheckout drives a double click on a non-current branch row and
// drains the checkout write plus the reload it schedules.
func doubleClickCheckout(t *testing.T, h *testHarness, branch string) {
	t.Helper()
	doubleClick(t, h.ui, branch)
	h.drain()
}

// Double-clicking a non-current branch checks it out through the session's
// write path: the working tree switches, the snapshot reloads, and the
// current-branch emphasis moves to it.
func TestBranchDoubleClickCheckout(t *testing.T) {
	repo := setupSidebarRepo(t, "refs-checkout")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("initial branch = %q, want main", got)
	}
	doubleClickCheckout(t, h, "feature")

	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("branch after double click = %q, want feature", got)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected checkout error: %v", err)
	}
	row, ok := findAccessNode(t, h.ui, "feature")
	if !ok || !row.Selected {
		t.Fatalf("feature row after checkout Selected = %+v (found=%v), want selected", row, ok)
	}
	if row, ok := findAccessNode(t, h.ui, "main"); !ok || row.Selected {
		t.Fatalf("main row after checkout Selected = %+v, want unselected", row)
	}
}

// Double-clicking a branch whose checkout would overwrite uncommitted local
// changes opens the dialog offering to stash or discard them instead of an
// error banner. Canceling it leaves the current branch checked out and the
// uncommitted file content untouched.
func TestBranchDoubleClickCheckoutConflict(t *testing.T) {
	repo := setupSidebarRepo(t, "refs-checkout-conflict")
	// conflicting.txt exists on both branches with different content.
	repo.WriteFile("conflict.txt", "main version\n")
	repo.Git("add", "conflict.txt")
	repo.Git("commit", "-q", "-m", "main conflict version")
	repo.Checkout("feature")
	repo.WriteFile("conflict.txt", "feature version\n")
	repo.Git("add", "conflict.txt")
	repo.Git("commit", "-q", "-m", "feature conflict version")
	repo.Checkout("main")
	// Uncommitted local edit that checkout would have to overwrite.
	repo.WriteFile("conflict.txt", "local uncommitted edits\n")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	doubleClickCheckout(t, h, "feature")

	// The dialog opens with the file in the way, instead of an error banner.
	assertCheckoutBlockedPanel(t, h, "feature", []string{"conflict.txt"}, nil)

	cancelCheckoutBlocked(t, h)
	h.drain()

	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("branch after conflicting checkout = %q, want main (unchanged)", got)
	}
	data, err := os.ReadFile(filepath.Join(repo.Path(), "conflict.txt"))
	if err != nil {
		t.Fatalf("read conflict.txt: %v", err)
	}
	if string(data) != "local uncommitted edits\n" {
		t.Fatalf("uncommitted content = %q, want preserved %q", string(data), "local uncommitted edits\n")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); ok {
		t.Fatalf("Repository Error status after the canceled checkout; texts=%q", h.ui.Texts())
	}
	if row, ok := findAccessNode(t, h.ui, "main"); !ok || !row.Selected {
		t.Fatalf("main row after the canceled checkout Selected = %+v, want still selected", row)
	}
}

// The New Branch dialog validates the typed name live — illegal characters
// and collisions with existing branches disable Create Branch — and a valid
// submission creates the branch at HEAD and checks it out when the box is
// ticked.
func TestRefsNewBranchDialogValidatesAndCreates(t *testing.T) {
	repo := setupSidebarRepo(t, "refs-new-branch")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Branch"); err != nil {
		t.Fatalf("click New Branch: %v", err)
	}
	h.ui.Frame()

	// Empty name: submit disabled.
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Branch"); !ok || !btn.Disabled {
		t.Fatalf("Create Branch with empty name = %+v (found=%v), want disabled", btn, ok)
	}

	// Illegal characters disable submit and show the problem.
	h.ui.Type("bad..name")
	h.ui.Frame()
	if !h.ui.HasText("Must not contain '..'.") {
		t.Fatalf("illegal name problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Branch"); !ok || !btn.Disabled {
		t.Fatalf("Create Branch with illegal name = %+v, want disabled", btn)
	}

	// Collision with an existing branch disables submit.
	clearInput(t, h.ui)
	h.ui.Type("feature")
	h.ui.Frame()
	if !h.ui.HasText("already exists") {
		t.Fatalf("duplicate name problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Branch"); !ok || !btn.Disabled {
		t.Fatalf("Create Branch with duplicate name = %+v, want disabled", btn)
	}

	// A name starting with "-" is rejected: git would read it as options
	// (git branch -l HEAD creates nothing and exits 0).
	clearInput(t, h.ui)
	h.ui.Type("-l")
	h.ui.Frame()
	if !h.ui.HasText("cannot begin with '-'") {
		t.Fatalf("dash-name problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Branch"); !ok || !btn.Disabled {
		t.Fatalf("Create Branch with dash name = %+v, want disabled", btn)
	}

	// A name holding whitespace is rejected: it would split into arguments.
	clearInput(t, h.ui)
	h.ui.Type("has space")
	h.ui.Frame()
	if !h.ui.HasText("cannot contain whitespace") {
		t.Fatalf("whitespace problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Branch"); !ok || !btn.Disabled {
		t.Fatalf("Create Branch with whitespace name = %+v, want disabled", btn)
	}

	// A name ending in "/" is rejected, while nested names like "feature/x"
	// stay valid.
	clearInput(t, h.ui)
	h.ui.Type("feature/")
	h.ui.Frame()
	if !h.ui.HasText("cannot end with '/'") {
		t.Fatalf("trailing-slash problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Branch"); !ok || !btn.Disabled {
		t.Fatalf("Create Branch with trailing slash = %+v, want disabled", btn)
	}
	clearInput(t, h.ui)
	h.ui.Type("feature/x")
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Branch"); !ok || btn.Disabled {
		t.Fatalf("Create Branch with nested name feature/x = %+v, want enabled", btn)
	}

	// A valid name enables submit; creating with checkout ticked switches.
	clearInput(t, h.ui)
	h.ui.Type("release-1")
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Branch"); !ok || btn.Disabled {
		t.Fatalf("Create Branch with valid name = %+v, want enabled", btn)
	}
	if err := h.ui.Click("Create Branch"); err != nil {
		t.Fatalf("click Create Branch: %v", err)
	}
	h.drain()

	if !branchExists(t, repo, "release-1") {
		t.Fatal("release-1 was not created in the repository")
	}
	if got := currentBranch(t, repo); got != "release-1" {
		t.Fatalf("current branch after create = %q, want release-1 (checkout was ticked)", got)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	if h.ui.HasText("Branch name") {
		t.Fatal("New Branch dialog stayed open after successful create")
	}
	if _, ok := h.ui.Find("release-1"); !ok {
		t.Fatal("release-1 missing from sidebar after create")
	}
}

// Creating a branch without the checkout box ticked adds the branch but
// keeps the current branch unchanged.
func TestRefsNewBranchWithoutCheckout(t *testing.T) {
	repo := setupSidebarRepo(t, "refs-new-no-checkout")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Branch"); err != nil {
		t.Fatalf("click New Branch: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("topic-only")
	h.ui.Frame()
	if err := h.ui.Click("Checkout after create"); err != nil {
		t.Fatalf("untick Checkout after create: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Create Branch"); err != nil {
		t.Fatalf("click Create Branch: %v", err)
	}
	h.drain()

	if !branchExists(t, repo, "topic-only") {
		t.Fatal("topic-only was not created")
	}
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("current branch = %q, want main unchanged", got)
	}
	if _, ok := h.ui.Find("topic-only"); !ok {
		t.Fatal("topic-only missing from sidebar")
	}
}

// clearInput selects the text of the focused input and deletes it.
func clearInput(t *testing.T, tt *ui.Tester) {
	t.Helper()
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(0, ui.KeyBackspace)
	tt.Frame()
}

// Deleting a merged branch asks once: the dialog reports it fully merged,
// Confirm Delete removes it from the repository and the sidebar.
func TestBranchDeleteMerged(t *testing.T) {
	repo := setupSidebarRepo(t, "refs-delete-merged")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("Delete branch merged"); err != nil {
		t.Fatalf("click Delete branch merged: %v", err)
	}
	h.ui.Frame()

	// The merged check gates the confirm button until it completes.
	drainDeleteCheck(t, h)

	if !h.ui.HasText("fully merged") {
		t.Fatalf("merged note not shown; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Delete"); err != nil {
		t.Fatalf("click Confirm Delete: %v", err)
	}
	h.drain()

	if branchExists(t, repo, "merged") {
		t.Fatal("merged branch survived Confirm Delete")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected delete error: %v", err)
	}
	if _, ok := h.ui.Find("merged"); ok {
		t.Fatal("merged stayed visible in the sidebar after delete")
	}
	if !branchExists(t, repo, "feature") {
		t.Fatal("delete removed the wrong branch: feature is gone")
	}
}

// drainDeleteCheck runs the asynchronous merged check the delete dialog
// started and renders its result.
func drainDeleteCheck(t *testing.T, h *testHarness) {
	t.Helper()
	h.drain()
}

// While the merged check is still running the dialog reports it and offers
// no delete button at all: a branch whose merged state is not yet known is
// never deleted. The check's worker is held back with PauseWorkers, so the
// in-progress state is deterministic, then released so the flow completes.
func TestBranchDeleteCheckingInProgressHasNoDeleteButton(t *testing.T) {
	repo := setupSidebarRepo(t, "refs-delete-checking")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Pause new workers so the merged check spawned below stays in flight.
	h.queue.PauseWorkers()
	defer h.queue.ResumeWorkers()

	if err := h.ui.Click("Delete branch merged"); err != nil {
		t.Fatalf("click Delete branch merged: %v", err)
	}
	h.ui.Frame()

	// The check worker exists and is held before running.
	check := h.queue.WaitWorker(t)

	if !h.ui.HasText("Checking whether the branch is merged") {
		t.Fatalf("checking-in-progress note not shown; texts=%q", h.ui.Texts())
	}
	// Neither delete button exists yet: the answer is unknown.
	if err := h.ui.Click("Confirm Delete"); err == nil {
		t.Fatal("Confirm Delete was clickable while the merged check was still running")
	}
	if err := h.ui.Click("Force Delete"); err == nil {
		t.Fatal("Force Delete was clickable while the merged check was still running")
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete"); ok {
		t.Fatalf("Confirm Delete present during check: %+v", btn)
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Force Delete"); ok {
		t.Fatalf("Force Delete present during check: %+v", btn)
	}

	// Releasing the worker lets the check answer and the flow complete.
	h.queue.ReleaseWorker(check)
	h.queue.ResumeWorkers()
	h.drain()

	if !h.ui.HasText("fully merged") {
		t.Fatalf("merged note not shown after releasing the check; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Delete"); err != nil {
		t.Fatalf("click Confirm Delete after check: %v", err)
	}
	h.drain()
	if branchExists(t, repo, "merged") {
		t.Fatal("merged branch survived Confirm Delete after the check completed")
	}
}

// Deleting an unmerged branch warns about the commits that would be lost
// and requires the explicit force agreement before Force Delete runs.
func TestBranchDeleteUnmergedForceFlow(t *testing.T) {
	repo := setupSidebarRepo(t, "refs-delete-unmerged")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("Delete branch unmerged"); err != nil {
		t.Fatalf("click Delete branch unmerged: %v", err)
	}
	h.ui.Frame()
	drainDeleteCheck(t, h)

	if !h.ui.HasText("not merged") {
		t.Fatalf("unmerged warning not shown; texts=%q", h.ui.Texts())
	}
	// Force Delete stays disabled until the user agrees explicitly.
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Force Delete"); !ok || !btn.Disabled {
		t.Fatalf("Force Delete before agreement = %+v (found=%v), want disabled", btn, ok)
	}
	// Pressing the disabled button anyway must do nothing: the branch
	// survives in the repository.
	_ = h.ui.Click("Force Delete")
	h.drain()
	if !branchExists(t, repo, "unmerged") {
		t.Fatal("unmerged was deleted by pressing the disabled Force Delete")
	}
	if err := h.ui.Click("I understand, force delete this branch"); err != nil {
		t.Fatalf("click force agreement: %v", err)
	}
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Force Delete"); !ok || btn.Disabled {
		t.Fatalf("Force Delete after agreement = %+v, want enabled", btn)
	}
	if err := h.ui.Click("Force Delete"); err != nil {
		t.Fatalf("click Force Delete: %v", err)
	}
	h.drain()

	if branchExists(t, repo, "unmerged") {
		t.Fatal("unmerged branch survived a confirmed Force Delete")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected force delete error: %v", err)
	}
}

// Canceling the delete dialog leaves the branch in the repository.
func TestBranchDeleteCancel(t *testing.T) {
	repo := setupSidebarRepo(t, "refs-delete-cancel")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("Delete branch feature"); err != nil {
		t.Fatalf("click Delete branch feature: %v", err)
	}
	h.ui.Frame()
	drainDeleteCheck(t, h)
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	if !branchExists(t, repo, "feature") {
		t.Fatal("feature was deleted even though the dialog was canceled")
	}
}

// The current branch exposes no delete entry at all.
func TestBranchDeleteCurrentHasNoEntry(t *testing.T) {
	repo := setupSidebarRepo(t, "refs-delete-current")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("Delete branch main"); err == nil {
		t.Fatal("current branch main showed a delete entry")
	}
}

// While the merged check is still running the dialog has no delete button,
// and when the check itself fails the branch is never deleted: no confirm
// button appears, the error shows, and canceling is the only way out. The
// failure is real: the worker of the merged check runs against a repository
// whose object store is corrupted after the sidebar loaded, and the
// protection is proven both by the missing buttons and by the branch
// surviving in the repository.
func TestBranchDeleteCheckFailureProtects(t *testing.T) {
	repo := setupSidebarRepo(t, "refs-delete-checkfail")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Corrupt the object store after the snapshot loaded: every git command
	// the check and a delete would run fails now. Deleting a loose ref is a
	// ref-file operation that needs no objects, which is exactly why the
	// check must gate the delete — and what this test proves.
	objects := filepath.Join(repo.Path(), ".git", "objects")
	backup := filepath.Join(repo.Path(), ".git", "objects-backup")
	if err := os.Rename(objects, backup); err != nil {
		t.Fatalf("move object store away: %v", err)
	}
	// Verify with the Git CLI that the check really fails before declaring
	// the scenario valid.
	if out, err := tryGitCommand(repo.Path(), "merge-base", "--is-ancestor", "refs/heads/feature", "HEAD"); err == nil {
		t.Fatalf("corrupted repo still passes merge-base; scenario invalid: %s", out)
	}

	if err := h.ui.Click("Delete branch feature"); err != nil {
		t.Fatalf("click Delete branch feature: %v", err)
	}
	h.ui.Frame()
	drainDeleteCheck(t, h)

	if !h.ui.HasText("will not be deleted") {
		t.Fatalf("check failure protection note not shown; texts=%q", h.ui.Texts())
	}
	// The dialog shows the check's error verbatim, not only a fixed note:
	// without objects the verify step fails with git's own fatal message.
	if !h.ui.HasText("verify branch feature") {
		t.Fatalf("check failure error text not shown in the dialog; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Delete"); err == nil {
		t.Fatal("Confirm Delete was clickable after the merged check failed")
	}
	if err := h.ui.Click("Force Delete"); err == nil {
		t.Fatal("Force Delete was clickable after the merged check failed")
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	// The repository is corrupted on purpose, so rev-parse cannot answer
	// here; what proves nothing was deleted is the ref file surviving.
	if _, err := os.Stat(filepath.Join(repo.Path(), ".git", "refs", "heads", "feature")); err != nil {
		t.Fatalf("feature ref file is gone even though the merged check failed: %v", err)
	}
}

// A stale merged-state answer cannot delete a branch that changed since the
// dialog opened: the worker re-verifies on the repository, so when the
// branch gained new unmerged commits after the check answered "merged", the
// non-forcing delete fails with an error and the branch survives.
func TestBranchDeleteStaleMergedAnswerRefused(t *testing.T) {
	repo := setupSidebarRepo(t, "refs-delete-stale")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("Delete branch merged"); err != nil {
		t.Fatalf("click Delete branch merged: %v", err)
	}
	h.ui.Frame()
	drainDeleteCheck(t, h)
	if !h.ui.HasText("fully merged") {
		t.Fatalf("merged note not shown; texts=%q", h.ui.Texts())
	}

	// The branch gains a commit HEAD does not have, after the check answered.
	repo.Checkout("merged")
	repo.CommitFile("late.txt", "late work\n", "late unmerged work")
	repo.Checkout("main")

	if err := h.ui.Click("Confirm Delete"); err != nil {
		t.Fatalf("click Confirm Delete: %v", err)
	}
	h.drain()

	if !branchExists(t, repo, "merged") {
		t.Fatal("merged was deleted with a non-forcing delete after gaining unmerged commits")
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("stale non-forcing delete produced no error")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error status after refused stale delete; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("not fully merged") {
		t.Fatalf("refusal reason not visible in the UI; texts=%q", h.ui.Texts())
	}
}

// opening refs dialogs is scoped to the repository: switching repositories
// discards the drafts.
func TestRefsDialogsResetOnRepoSwitch(t *testing.T) {
	repoA := setupSidebarRepo(t, "refs-switch-a")
	repoB := newGitRepo(t, "refs-switch-b")
	repoB.CommitFile("b.txt", "b\n", "b commit")

	h := newTestApp(t, repoB.StoreRepo())
	h.openRepo(repoA.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Branch"); err != nil {
		t.Fatalf("click New Branch: %v", err)
	}
	h.ui.Frame()
	if !h.app.refsView.newOpen {
		t.Fatal("new branch dialog did not open")
	}

	h.openRepo(repoB.StoreRepo())
	h.drain()

	if h.app.refsView.newOpen {
		t.Fatal("new branch dialog survived a repository switch")
	}
	if h.app.refsView.delOpen {
		t.Fatal("delete dialog survived a repository switch")
	}
}

// --- Loopback SSH variants ---

// The loopback SSH sidebar lists branches, the remote and tags of the
// server repository, and marks the current branch. The server repository
// gets a real bare remote it pushes to, so the Remotes section shows the
// remote-tracking branch origin/main.
func TestRefsSidebarLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	// Build the branch graph on the server.
	srv.RunGit(t, repo.Path, "branch", "merged")
	srv.RunGit(t, repo.Path, "tag", "v1.0")
	srv.RunGit(t, repo.Path, "checkout", "-q", "-b", "feature")
	srv.WriteFile(t, repo.Path, "feature.txt", []byte("feature\n"))
	srv.RunGit(t, repo.Path, "add", "feature.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "feature work")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	// A bare remote on the same server, pushed so origin/main exists.
	bare := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repo.Path, "remote", "add", "origin", bare)
	srv.RunGit(t, repo.Path, "push", "-q", "-u", "origin", "main")

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	for _, want := range []string{"main", "merged", "feature", "v1.0", "origin", "origin/main"} {
		if _, ok := h.ui.Find(want); !ok {
			t.Fatalf("SSH sidebar missing %q; texts=%q", want, h.ui.Texts())
		}
	}
	if row, ok := findAccessNode(t, h.ui, "main"); !ok || !row.Selected {
		t.Fatalf("SSH current branch main Selected = %+v (found=%v), want selected", row, ok)
	}
}

// Double-clicking a branch of a loopback SSH repository checks it out on
// the server.
func TestBranchDoubleClickCheckoutLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	srv.RunGit(t, repo.Path, "checkout", "-q", "-b", "feature")
	srv.WriteFile(t, repo.Path, "feature.txt", []byte("feature\n"))
	srv.RunGit(t, repo.Path, "add", "feature.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "feature work")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()

	doubleClickCheckout(t, h, "feature")

	got := srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD")
	if got != "feature" {
		t.Fatalf("SSH branch after double click = %q, want feature", got)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH checkout error: %v", err)
	}
	if row, ok := findAccessNode(t, h.ui, "feature"); !ok || !row.Selected {
		t.Fatalf("feature row after SSH checkout Selected = %+v, want selected", row)
	}
}

// A conflicting double-click checkout on a loopback SSH repository opens
// the dialog offering to stash or discard the changes instead of an error
// banner. Canceling leaves the server on the original branch with the
// uncommitted content untouched. Repeating the checkout with the stash
// choice moves the server to the branch: the file's single line differs on
// all three sides of the reapply, so the restore conflicts as designed —
// the edit survives inside the conflict markers and stays in the stash
// list, which the error banner says.
func TestBranchCheckoutConflictLoopbackSSH(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	repo, srv := loopbackSSHRepo(t)
	// conflicting content on main and feature.
	srv.WriteFile(t, repo.Path, "conflict.txt", []byte("main version\n"))
	srv.RunGit(t, repo.Path, "add", "conflict.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "main conflict version")
	srv.RunGit(t, repo.Path, "checkout", "-q", "-b", "feature")
	srv.WriteFile(t, repo.Path, "conflict.txt", []byte("feature version\n"))
	srv.RunGit(t, repo.Path, "add", "conflict.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "feature conflict version")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	// Uncommitted edit checkout would overwrite.
	srv.WriteFile(t, repo.Path, "conflict.txt", []byte("server uncommitted edits\n"))

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()

	doubleClickCheckout(t, h, "feature")

	// The dialog opens with the file in the way, instead of an error banner.
	assertCheckoutBlockedPanel(t, h, "feature", []string{"conflict.txt"}, nil)

	cancelCheckoutBlocked(t, h)
	h.drain()

	got := srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD")
	if got != "main" {
		t.Fatalf("SSH branch after the canceled checkout = %q, want main unchanged", got)
	}
	content := srv.ReadFile(t, repo.Path, "conflict.txt")
	if string(content) != "server uncommitted edits\n" {
		t.Fatalf("SSH uncommitted content = %q, want preserved", string(content))
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); ok {
		t.Fatalf("Repository Error status after the canceled SSH checkout; texts=%q", h.ui.Texts())
	}

	// Again with the stash choice: the checkout itself succeeds. The edit's
	// line is different on all three sides of the reapply, so the merge of
	// the stash conflicts — the designed outcome keeps the changes in the
	// stash list and inside the file's conflict markers.
	doubleClickCheckout(t, h, "feature")
	assertCheckoutBlockedPanel(t, h, "feature", []string{"conflict.txt"}, nil)
	clickStashAndCheckout(t, h)

	if got := srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature" {
		t.Fatalf("SSH branch after stash and checkout = %q, want feature — the checkout itself succeeded", got)
	}
	content = srv.ReadFile(t, repo.Path, "conflict.txt")
	if !strings.Contains(string(content), "server uncommitted edits") {
		t.Fatalf("SSH conflicted content = %q, want the uncommitted edit surviving inside the markers", string(content))
	}
	if !strings.Contains(string(content), "feature version") {
		t.Fatalf("SSH conflicted content = %q, want the branch's version inside the markers", string(content))
	}
	if out := strings.TrimSpace(srv.RunGit(t, repo.Path, "stash", "list")); out == "" {
		t.Fatal("SSH stash list is empty after the conflicting restore — the changes must stay there")
	}
	if err := h.app.session.ActionErr(); err == nil || !strings.Contains(err.Error(), "stash list") {
		t.Fatalf("ActionErr after the conflicting restore = %v, want it to say the changes remain in the stash list", err)
	}
	srv.RunGit(t, repo.Path, "reset", "-q", "--hard")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	srv.RunGit(t, repo.Path, "stash", "drop", "stash@{0}")
}

// The New Branch dialog creates the branch on the loopback SSH server and
// checks it out when the box is ticked.
func TestRefsNewBranchLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()

	if err := h.ui.Click("New Branch"); err != nil {
		t.Fatalf("click New Branch: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("ssh-branch")
	h.ui.Frame()
	if err := h.ui.Click("Create Branch"); err != nil {
		t.Fatalf("click Create Branch: %v", err)
	}
	h.drain()

	out := srv.RunGit(t, repo.Path, "branch", "--list", "ssh-branch")
	if strings.TrimSpace(out) == "" {
		t.Fatal("ssh-branch was not created on the server")
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "ssh-branch" {
		t.Fatalf("SSH current branch after create = %q, want ssh-branch", got)
	}
}

// The delete flow on a loopback SSH repository: a merged branch deletes
// with one confirmation, an unmerged branch needs the force agreement, and
// a failed merged check never deletes.
func TestBranchDeleteLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	sshBranchExists := func(name string) bool {
		out := srv.RunGit(t, repo.Path, "branch", "--list", name)
		return strings.TrimSpace(out) != ""
	}
	srv.RunGit(t, repo.Path, "branch", "merged")
	srv.RunGit(t, repo.Path, "checkout", "-q", "-b", "unmerged")
	srv.WriteFile(t, repo.Path, "unmerged.txt", []byte("unmerged\n"))
	srv.RunGit(t, repo.Path, "add", "unmerged.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "unmerged work")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()

	// Merged branch: one confirmation.
	if err := h.ui.Click("Delete branch merged"); err != nil {
		t.Fatalf("click Delete branch merged: %v", err)
	}
	h.ui.Frame()
	drainDeleteCheck(t, h)
	if !h.ui.HasText("fully merged") {
		t.Fatalf("SSH merged note not shown; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Delete"); err != nil {
		t.Fatalf("click Confirm Delete: %v", err)
	}
	h.drain()
	if sshBranchExists("merged") {
		t.Fatal("merged branch survived Confirm Delete on SSH")
	}

	// Unmerged branch: warning plus explicit agreement.
	if err := h.ui.Click("Delete branch unmerged"); err != nil {
		t.Fatalf("click Delete branch unmerged: %v", err)
	}
	h.ui.Frame()
	drainDeleteCheck(t, h)
	if !h.ui.HasText("not merged") {
		t.Fatalf("SSH unmerged warning not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Force Delete"); !ok || !btn.Disabled {
		t.Fatalf("SSH Force Delete before agreement = %+v, want disabled", btn)
	}
	// Canceling leaves the branch in place.
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if !sshBranchExists("unmerged") {
		t.Fatal("unmerged was deleted despite canceling the SSH dialog")
	}

	// Agreeing and force deleting removes it.
	if err := h.ui.Click("Delete branch unmerged"); err != nil {
		t.Fatalf("re-open Delete branch unmerged: %v", err)
	}
	h.ui.Frame()
	drainDeleteCheck(t, h)
	if err := h.ui.Click("I understand, force delete this branch"); err != nil {
		t.Fatalf("click force agreement: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Force Delete"); err != nil {
		t.Fatalf("click Force Delete: %v", err)
	}
	h.drain()
	if sshBranchExists("unmerged") {
		t.Fatal("unmerged branch survived confirmed Force Delete on SSH")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH delete error: %v", err)
	}
}

// The merged check of the delete dialog runs on the server: breaking the
// server's object store after the sidebar loaded fails the check there, no
// delete button appears, and the branch survives on the server.
func TestBranchDeleteCheckFailureLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	srv.RunGit(t, repo.Path, "branch", "feature")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()

	if _, ok := h.ui.Find("feature"); !ok {
		t.Fatalf("feature missing from SSH sidebar before corruption; texts=%q", h.ui.Texts())
	}

	// Move the server's object store away: the check's git commands fail on
	// the server now, while the ref file itself stays in place.
	srv.RunShell(t, "mv "+git.ShellQuote(repo.Path+"/.git/objects")+" "+git.ShellQuote(repo.Path+"/.git/objects-away"))
	check := srv.RunShell(t, "cd "+git.ShellQuote(repo.Path)+"; git merge-base --is-ancestor refs/heads/feature HEAD >/dev/null 2>&1; echo rc=$?")
	if strings.TrimSpace(check) == "rc=0" {
		t.Fatalf("corrupted SSH repo still passes merge-base; scenario invalid: %q", check)
	}

	if err := h.ui.Click("Delete branch feature"); err != nil {
		t.Fatalf("click Delete branch feature on SSH: %v", err)
	}
	h.ui.Frame()
	drainDeleteCheck(t, h)

	if !h.ui.HasText("will not be deleted") {
		t.Fatalf("SSH check failure protection not shown; texts=%q", h.ui.Texts())
	}
	// The SSH check failure also shows its error verbatim in the dialog.
	if !h.ui.HasText("verify branch feature") {
		t.Fatalf("SSH check failure error text not shown in the dialog; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Delete"); err == nil {
		t.Fatal("Confirm Delete clickable after SSH check failure")
	}
	if err := h.ui.Click("Force Delete"); err == nil {
		t.Fatal("Force Delete clickable after SSH check failure")
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	out := srv.RunShell(t, "cd "+git.ShellQuote(repo.Path)+"; git branch --list feature 2>/dev/null; ls .git/refs/heads/feature 2>/dev/null; echo done")
	if !strings.Contains(out, "feature") {
		t.Fatalf("feature was deleted on SSH even though the merged check failed: %q", out)
	}
}
