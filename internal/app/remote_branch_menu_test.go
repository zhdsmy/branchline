package app

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// remoteMenuFixture builds a repository with main checked out, feature one
// commit ahead of it, both pushed to a bare origin, and the local feature
// removed again: the remote-tracking branch origin/feature exists while no
// local branch named feature does.
func remoteMenuFixture(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("feature.txt", "feature\n", "feature work")
	repo.Checkout("main")
	repo.AddBareRemote("origin")
	repo.Git("push", "-q", "origin", "feature")
	repo.Git("branch", "-D", "feature")
	return repo
}

// chooseRemoteMenuItem right-clicks the remote branch row of the sidebar and
// verifies the menu shows the item in its expected position.
func openRemoteBranchMenu(t *testing.T, h *testHarness, rb string) {
	t.Helper()
	if err := h.ui.RightClick(rb); err != nil {
		t.Fatalf("right-click remote branch row %q: %v\ntexts=%q", rb, err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	want := []string{
		"Checkout…",
		"-",
		"Pull into 'main'…",
		"-",
		"Merge into 'main'…",
		"Rebase 'main' on '" + rb + "'…",
		"Interactively Rebase on '" + rb + "'…",
		"-",
		"New Branch…",
		"New Tag…",
		"-",
		"Delete '" + rb + "'…",
		"-",
		"Copy Branch Name",
	}
	if !slices.Equal(menu, want) {
		t.Fatalf("remote branch menu = %q, want %q", menu, want)
	}
}

// The remote branch row's Checkout menu item creates a local branch tracking
// the remote branch and switches to it: its upstream configuration points at
// the remote branch, HEAD is on it, and the checkout runs from the menu in
// the dialog's expected layout.
func TestRemoteBranchCheckoutCreatesTrackingBranch(t *testing.T) {
	repo := remoteMenuFixture(t, "rb-checkout-create")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if _, ok := h.ui.Find("origin/feature"); !ok {
		t.Fatalf("origin/feature missing from the sidebar; texts=%q", h.ui.Texts())
	}
	openRemoteBranchMenu(t, h, "origin/feature")
	if err := h.ui.ChooseMenuItem("Checkout…"); err != nil {
		t.Fatalf("choose Checkout…: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Checkout Remote Branch") {
		t.Fatalf("checkout dialog title missing; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "origin/feature") {
		t.Fatalf("dialog missing the remote branch line; texts=%q", h.ui.Texts())
	}
	assertDialogLayout(t, h, "Cancel", "Confirm Checkout Remote Branch")

	// The prefilled name is the branch part after the remote: submitting it
	// as-is creates feature tracking origin/feature.
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout Remote Branch"); !ok || btn.Disabled {
		t.Fatalf("Confirm Checkout Remote Branch = %+v (found=%v), want enabled", btn, ok)
	}
	if err := h.ui.Click("Confirm Checkout Remote Branch"); err != nil {
		t.Fatalf("click Confirm Checkout Remote Branch: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("remote branch checkout failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("current branch after checkout = %q, want feature", got)
	}
	if got := gitOutTrim(t, repo.Path(), "config", "--get", "branch.feature.remote"); got != "origin" {
		t.Fatalf("branch.feature.remote = %q, want origin", got)
	}
	if got := gitOutTrim(t, repo.Path(), "config", "--get", "branch.feature.merge"); got != "refs/heads/feature" {
		t.Fatalf("branch.feature.merge = %q, want refs/heads/feature", got)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "--abbrev-ref", "feature@{upstream}"); got != "origin/feature" {
		t.Fatalf("feature upstream = %q, want origin/feature", got)
	}
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	remoteTip := gitOutTrim(t, repo.Path(), "rev-parse", "refs/remotes/origin/feature")
	if head != remoteTip {
		t.Fatalf("HEAD = %q, want the remote branch tip %q", head, remoteTip)
	}
	assertFileBytes(t, repo, "feature.txt", "feature\n")
	if h.ui.HasText("Checkout Remote Branch") {
		t.Fatal("checkout dialog stayed open after a successful checkout")
	}
}

// When a local branch already exists tracking this very remote branch, the
// dialog says the checkout switches to it instead of reporting a collision,
// and confirming checks that branch out.
func TestRemoteBranchCheckoutSwitchesToExistingTrackingBranch(t *testing.T) {
	repo := remoteMenuFixture(t, "rb-checkout-existing")
	repo.Git("checkout", "-q", "-b", "feature", "--track", "origin/feature")
	repo.Checkout("main")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Checkout…"); err != nil {
		t.Fatalf("choose Checkout…: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("checkout switches to it") {
		t.Fatalf("existing-tracking note missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout Remote Branch"); !ok || btn.Disabled {
		t.Fatalf("Confirm Checkout Remote Branch with existing tracking branch = %+v, want enabled", btn)
	}
	if err := h.ui.Click("Confirm Checkout Remote Branch"); err != nil {
		t.Fatalf("click Confirm Checkout Remote Branch: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout of the existing tracking branch failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("current branch after checkout = %q, want feature", got)
	}
	if count := gitOutTrim(t, repo.Path(), "branch", "--list", "feature"); strings.Count(count, "feature") != 1 {
		t.Fatalf("feature branch list = %q, want exactly one entry — no duplicate branch was created", count)
	}
}

// A typed name different from the remote branch's own is created as the
// tracking branch instead.
func TestRemoteBranchCheckoutWithRenamedLocalBranch(t *testing.T) {
	repo := remoteMenuFixture(t, "rb-checkout-renamed")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Checkout…"); err != nil {
		t.Fatalf("choose Checkout…: %v", err)
	}
	h.ui.Frame()
	clearInput(t, h.ui)
	h.ui.Type("feat-local")
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout Remote Branch"); !ok || btn.Disabled {
		t.Fatalf("Confirm Checkout Remote Branch with renamed input = %+v, want enabled", btn)
	}
	if err := h.ui.Click("Confirm Checkout Remote Branch"); err != nil {
		t.Fatalf("click Confirm Checkout Remote Branch: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("renamed remote branch checkout failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "feat-local" {
		t.Fatalf("current branch after checkout = %q, want feat-local", got)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "--abbrev-ref", "feat-local@{upstream}"); got != "origin/feature" {
		t.Fatalf("feat-local upstream = %q, want origin/feature", got)
	}
	if branchExists(t, repo, "feature") {
		t.Fatal("a branch named feature was created even though the name was changed")
	}
}

// The input is validated as it is typed: an empty name, an illegal name and
// a collision with a local branch that does not track the remote branch each
// show their reason and disable Checkout, and surrounding whitespace is
// stripped before the checks.
func TestRemoteBranchCheckoutInputValidation(t *testing.T) {
	repo := remoteMenuFixture(t, "rb-checkout-invalid")
	// taken exists locally without tracking origin/feature.
	repo.Git("branch", "taken")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Checkout…"); err != nil {
		t.Fatalf("choose Checkout…: %v", err)
	}
	h.ui.Frame()

	// Illegal characters.
	clearInput(t, h.ui)
	h.ui.Type("bad..name")
	h.ui.Frame()
	if !h.ui.HasText("Must not contain '..'.") {
		t.Fatalf("illegal name problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout Remote Branch"); !ok || !btn.Disabled {
		t.Fatalf("confirm with illegal name = %+v, want disabled", btn)
	}

	// Collision with a local branch that does not track the remote branch.
	clearInput(t, h.ui)
	h.ui.Type("taken")
	h.ui.Frame()
	if !h.ui.HasText("does not track origin/feature") {
		t.Fatalf("collision problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout Remote Branch"); !ok || !btn.Disabled {
		t.Fatalf("confirm with colliding name = %+v, want disabled", btn)
	}

	// Pressing the disabled button anyway changes nothing.
	_ = h.ui.Click("Confirm Checkout Remote Branch")
	h.drain()
	if branchExists(t, repo, "feature") {
		t.Fatal("pressing the disabled confirm created the branch")
	}

	// Empty input disables too.
	clearInput(t, h.ui)
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout Remote Branch"); !ok || !btn.Disabled {
		t.Fatalf("confirm with empty name = %+v, want disabled", btn)
	}

	// Surrounding whitespace is stripped before the checks: a valid name
	// padded with spaces checks out under its trimmed name.
	clearInput(t, h.ui)
	h.ui.Type("  padded  ")
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout Remote Branch"); !ok || btn.Disabled {
		t.Fatalf("confirm with whitespace-padded name = %+v, want enabled", btn)
	}
	if err := h.ui.Click("Confirm Checkout Remote Branch"); err != nil {
		t.Fatalf("click Confirm Checkout Remote Branch: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("whitespace-padded checkout failed: %v", err)
	}
	if !branchExists(t, repo, "padded") {
		t.Fatal("the trimmed branch padded was not created")
	}
}

// A checkout whose local changes git would have to overwrite is refused by
// git itself: the error is shown verbatim, no branch is created, HEAD stays
// on main, and the uncommitted bytes of the working tree are untouched.
func TestRemoteBranchCheckoutConflictRefused(t *testing.T) {
	repo := remoteMenuFixture(t, "rb-checkout-conflict")
	// conflict.txt exists on feature but not on main: the checkout would
	// create it over the uncommitted file of the same name.
	repo.WriteFile("feature.txt", "local uncommitted edits\n")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Checkout…"); err != nil {
		t.Fatalf("choose Checkout…: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Checkout Remote Branch"); err != nil {
		t.Fatalf("click Confirm Checkout Remote Branch: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("the conflicting checkout produced no ActionErr")
	} else if !strings.Contains(err.Error(), "feature.txt") && !strings.Contains(strings.ToLower(err.Error()), "local changes") {
		t.Fatalf("ActionErr = %q, want git checkout conflict wording", err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error status after the refused checkout; texts=%q", h.ui.Texts())
	}
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("current branch after the refused checkout = %q, want main unchanged", got)
	}
	if branchExists(t, repo, "feature") {
		t.Fatal("a feature branch was created even though the checkout was refused")
	}
	assertFileBytes(t, repo, "feature.txt", "local uncommitted edits\n")
}

// pushFeatureAhead moves the server's feature one commit ahead through a
// scratch clone, so the repository's own tracking ref is untouched until it
// fetches.
func pushFeatureAhead(t *testing.T, repo *gitRepoFixture) {
	t.Helper()
	bare := strings.TrimSpace(repo.Git("remote", "get-url", "origin"))
	scratch := newGitRepo(t, repo.name+"-scratch")
	scratch.Git("fetch", "-q", bare, "feature:feature")
	scratch.Git("checkout", "-q", "feature")
	scratch.CommitFile("ahead.txt", "ahead\n", "moved ahead")
	scratch.Git("push", "-q", bare, "feature:feature")
}

// When the remote-tracking reference moved after the dialog opened — here a
// fetch picking up someone else's push — the guard refuses the checkout:
// HEAD stays on main, no branch is created, and the error explains why.
func TestRemoteBranchCheckoutGuardRemoteMoved(t *testing.T) {
	repo := remoteMenuFixture(t, "rb-checkout-guard-moved")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Checkout…"); err != nil {
		t.Fatalf("choose Checkout…: %v", err)
	}
	h.ui.Frame()

	// The remote branch moves after the dialog opened; a fetch moves the
	// tracking ref with it.
	pushFeatureAhead(t, repo)
	repo.Git("fetch", "-q", "origin")

	if err := h.ui.Click("Confirm Checkout Remote Branch"); err != nil {
		t.Fatalf("click Confirm Checkout Remote Branch: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "changed after the dialog opened")
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("current branch after the refused checkout = %q, want main", got)
	}
	if branchExists(t, repo, "feature") {
		t.Fatal("feature was created even though the remote branch had moved")
	}
}

// When HEAD moved after the dialog opened — here an external commit on main —
// the guard refuses the checkout the user confirmed against the old state.
func TestRemoteBranchCheckoutGuardHeadMoved(t *testing.T) {
	repo := remoteMenuFixture(t, "rb-checkout-guard-head")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Checkout…"); err != nil {
		t.Fatalf("choose Checkout…: %v", err)
	}
	h.ui.Frame()

	headBefore := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	repo.CommitFile("other.txt", "other\n", "external commit")

	if err := h.ui.Click("Confirm Checkout Remote Branch"); err != nil {
		t.Fatalf("click Confirm Checkout Remote Branch: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "HEAD or the current branch changed")
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("current branch after the refused checkout = %q, want main", got)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "refs/heads/main"); head == headBefore {
		t.Fatal("fixture broken: the external commit did not move HEAD")
	}
	if branchExists(t, repo, "feature") {
		t.Fatal("feature was created even though HEAD had moved")
	}
}

// When a local branch of the typed name appears after the dialog opened and
// it does not track the remote branch, the guard refuses the checkout
// instead of failing inside git or switching to the wrong branch.
func TestRemoteBranchCheckoutGuardNameTaken(t *testing.T) {
	repo := remoteMenuFixture(t, "rb-checkout-guard-taken")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Checkout…"); err != nil {
		t.Fatalf("choose Checkout…: %v", err)
	}
	h.ui.Frame()

	// Someone creates a plain local branch feature after the dialog opened.
	repo.Git("branch", "feature")

	if err := h.ui.Click("Confirm Checkout Remote Branch"); err != nil {
		t.Fatalf("click Confirm Checkout Remote Branch: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "does not track origin/feature")
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("current branch after the refused checkout = %q, want main", got)
	}
	// The foreign branch is untouched: it still has no upstream and points
	// at the initial commit, not at the remote tip.
	if out, err := tryGitCommand(repo.Path(), "rev-parse", "--abbrev-ref", "feature@{upstream}"); err == nil {
		t.Fatalf("the foreign feature gained an upstream %q", strings.TrimSpace(out))
	}
	if got, want := gitOutTrim(t, repo.Path(), "rev-parse", "feature"), gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != want {
		t.Fatalf("the foreign feature moved to %q, want it at the initial commit %q", got, want)
	}
}

// Deleting a remote branch through the menu deletes it on the server: the
// confirmation dialog names it, the delete leaves the server's other
// branches alone, and the refreshed sidebar no longer lists the remote
// branch.
func TestRemoteBranchDeleteDeletesOnServer(t *testing.T) {
	repo := remoteMenuFixture(t, "rb-delete-basic")
	bare := strings.TrimSpace(repo.Git("remote", "get-url", "origin"))

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Delete 'origin/feature'…"); err != nil {
		t.Fatalf("choose Delete item: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Delete Remote Branch") {
		t.Fatalf("delete dialog title missing; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "origin/feature") {
		t.Fatalf("delete dialog missing the remote branch line; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("lose their upstream") {
		t.Fatalf("upstream warning missing; texts=%q", h.ui.Texts())
	}
	assertDialogLayout(t, h, "Cancel", "Confirm Delete Remote Branch")

	if err := h.ui.Click("Confirm Delete Remote Branch"); err != nil {
		t.Fatalf("click Confirm Delete Remote Branch: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("remote branch delete failed: %v", err)
	}
	if !branchListMissing(t, bare, "feature") {
		t.Fatalf("feature survived on the server: %s", gitOutTrim(t, bare, "branch"))
	}
	if got := gitOutTrim(t, bare, "branch"); !strings.Contains(got, "main") {
		t.Fatalf("the delete took main with it: server branches %q", got)
	}
	if _, ok := h.ui.Find("origin/feature"); ok {
		t.Fatal("origin/feature stayed visible in the sidebar after the delete")
	}
	if _, ok := h.ui.Find("origin/main"); !ok {
		t.Fatal("origin/main vanished from the sidebar even though only feature was deleted")
	}
	if h.ui.HasText("Delete Remote Branch") {
		t.Fatal("delete dialog stayed open after a successful delete")
	}
}

// branchListMissing reports whether branch is absent from the bare server's
// branch list.
func branchListMissing(t *testing.T, bare, branch string) bool {
	t.Helper()
	out, err := tryGitCommand(bare, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err != nil || strings.TrimSpace(out) == ""
}

// Canceling the delete confirmation leaves the server's branch in place.
func TestRemoteBranchDeleteCancel(t *testing.T) {
	repo := remoteMenuFixture(t, "rb-delete-cancel")
	bare := strings.TrimSpace(repo.Git("remote", "get-url", "origin"))

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Delete 'origin/feature'…"); err != nil {
		t.Fatalf("choose Delete item: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	if branchListMissing(t, bare, "feature") {
		t.Fatal("feature was deleted even though the dialog was canceled")
	}
}

// When the server's branch moved after the last fetch — pushed ahead by
// someone else — the lease refuses the delete: the error shows the refusal
// and the server keeps the branch at the new commit.
func TestRemoteBranchDeleteLeaseRefusesMovedServerBranch(t *testing.T) {
	repo := remoteMenuFixture(t, "rb-delete-lease")
	bare := strings.TrimSpace(repo.Git("remote", "get-url", "origin"))

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Delete 'origin/feature'…"); err != nil {
		t.Fatalf("choose Delete item: %v", err)
	}
	h.ui.Frame()

	// Someone else moves the server's branch; this repository never fetches,
	// so only the lease can protect it.
	pushFeatureAhead(t, repo)
	moved := gitOutTrim(t, bare, "rev-parse", "refs/heads/feature")

	if err := h.ui.Click("Confirm Delete Remote Branch"); err != nil {
		t.Fatalf("click Confirm Delete Remote Branch: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "stale info")
	if got := gitOutTrim(t, bare, "rev-parse", "refs/heads/feature"); got != moved {
		t.Fatalf("server feature = %q after the refused delete, want %q", got, moved)
	}
}

// When the remote-tracking reference moved after the dialog opened — here a
// fetch picking up someone else's push — the guard refuses the delete before
// anything is pushed, and the server keeps the branch.
func TestRemoteBranchDeleteGuardTrackingRefMoved(t *testing.T) {
	repo := remoteMenuFixture(t, "rb-delete-guard")
	bare := strings.TrimSpace(repo.Git("remote", "get-url", "origin"))

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Delete 'origin/feature'…"); err != nil {
		t.Fatalf("choose Delete item: %v", err)
	}
	h.ui.Frame()

	pushFeatureAhead(t, repo)
	repo.Git("fetch", "-q", "origin")
	moved := gitOutTrim(t, bare, "rev-parse", "refs/heads/feature")

	if err := h.ui.Click("Confirm Delete Remote Branch"); err != nil {
		t.Fatalf("click Confirm Delete Remote Branch: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "changed after the dialog opened")
	if got := gitOutTrim(t, bare, "rev-parse", "refs/heads/feature"); got != moved {
		t.Fatalf("server feature = %q after the refused delete, want %q", got, moved)
	}
}

// The remote's HEAD symbolic reference — origin/HEAD, naming the remote's
// default branch — is not a branch row at all in the sidebar, so neither
// Checkout nor Delete can act on it.
func TestRemoteBranchCheckoutHEADSymrefNotShown(t *testing.T) {
	repo := remoteMenuFixture(t, "rb-head-symref")
	repo.Git("remote", "set-head", "origin", "main")
	if got := gitOutTrim(t, repo.Path(), "symbolic-ref", "refs/remotes/origin/HEAD"); got != "refs/remotes/origin/main" {
		t.Fatalf("fixture: origin/HEAD = %q, want refs/remotes/origin/main", got)
	}

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("snapshot load failed: %v", err)
	}
	if _, ok := h.ui.Find("origin/main"); !ok {
		t.Fatalf("origin/main missing from the sidebar; texts=%q", h.ui.Texts())
	}
	for _, text := range h.ui.Texts() {
		if text == "origin/HEAD" {
			t.Fatalf("origin/HEAD is shown as a branch row; texts=%q", h.ui.Texts())
		}
	}
	if err := h.ui.RightClick("origin/HEAD"); err == nil {
		t.Fatal("origin/HEAD was right-clickable as a branch row")
	}
}

// While a repository write is busy, both remote branch menu items are
// disabled, so neither a checkout nor a delete can start over the running
// write.
func TestRemoteBranchCheckoutMenuDisabledWhileBusy(t *testing.T) {
	repo := remoteMenuFixture(t, "rb-menu-busy")
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

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature while busy: %v", err)
	}
	menu := h.ui.Menu()
	if !slices.Contains(menu, "Checkout…") || !slices.Contains(menu, "Delete 'origin/feature'…") {
		t.Fatalf("menu %q missing the checkout or delete item", menu)
	}
	if err := h.ui.ChooseMenuItem("Checkout…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing Checkout… while busy = %v, want a disabled error", err)
	}
	if err := h.ui.ChooseMenuItem("Delete 'origin/feature'…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing Delete while busy = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage write failed: %v", err)
	}
}

// While a merge is in progress, both remote branch menu items are disabled:
// no checkout or delete starts on top of an unfinished operation.
func TestRemoteBranchCheckoutMenuDisabledDuringOperation(t *testing.T) {
	repo := newGitRepo(t, "rb-menu-operation")
	repo.CommitFile("f.txt", "line\n", "base commit")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("f.txt", "theirs\n", "feature edits")
	repo.Checkout("main")
	repo.CommitFile("f.txt", "ours\n", "main edits")
	repo.AddBareRemote("origin")
	repo.Git("push", "-q", "origin", "feature")
	repo.Git("branch", "-D", "feature")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "origin/feature"); err == nil {
		t.Fatalf("conflicting merge unexpectedly succeeded: %s", out)
	}

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if got := h.app.session.Snapshot().Status.State; got != "merge" {
		t.Fatalf("fixture: state = %q, want merge", got)
	}

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature during a merge: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Checkout…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing Checkout… during a merge = %v, want a disabled error", err)
	}
	if err := h.ui.ChooseMenuItem("Delete 'origin/feature'…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing Delete during a merge = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout Remote Branch"); ok {
		t.Fatal("checkout dialog opened from a disabled menu item")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Remote Branch"); ok {
		t.Fatal("delete dialog opened from a disabled menu item")
	}
}

// When a merge conflict starts externally after the checkout dialog opened,
// HEAD has not moved yet, but the guard still refuses the checkout because
// an operation is in progress.
func TestRemoteBranchCheckoutGuardOperationStarted(t *testing.T) {
	repo := newGitRepo(t, "rb-checkout-guard-op")
	repo.CommitFile("f.txt", "line\n", "base commit")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("f.txt", "theirs\n", "feature edits")
	repo.Checkout("main")
	repo.CommitFile("f.txt", "ours\n", "main edits")
	repo.AddBareRemote("origin")
	repo.Git("push", "-q", "origin", "feature")
	repo.Git("branch", "-D", "feature")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Checkout…"); err != nil {
		t.Fatalf("choose Checkout…: %v", err)
	}
	h.ui.Frame()

	// Start a conflicting merge externally while the dialog is open: HEAD
	// stays on the same main commit, only Status.State changes to "merge".
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "origin/feature"); err == nil {
		t.Fatalf("conflicting merge unexpectedly succeeded: %s", out)
	}

	if err := h.ui.Click("Confirm Checkout Remote Branch"); err != nil {
		t.Fatalf("click Confirm Checkout Remote Branch: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "merge is in progress")
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("current branch after the refused checkout = %q, want main", got)
	}
	if branchExists(t, repo, "feature") {
		t.Fatal("feature was created even though a merge was in progress")
	}
}

// A failed remote branch delete redacts credentials embedded in the remote
// URL everywhere the error is stored or rendered — both in the toolbar error
// strip and on the Changes view.
func TestRemoteBranchDeleteErrorRedactsCredentials(t *testing.T) {
	repo := remoteMenuFixture(t, "rb-delete-redact")
	// Re-point origin to an unreachable HTTPS URL with embedded credentials
	// while keeping the existing refs/remotes/origin/feature tracking ref.
	repo.Git("remote", "set-url", "origin", "https://deluser:delsecretpw@127.0.0.1:9/repo.git")

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()
	// Also open Changes view so both the toolbar and Changes error displays
	// render ActionErr.
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Delete 'origin/feature'…"); err != nil {
		t.Fatalf("choose Delete item: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Delete Remote Branch"); err != nil {
		t.Fatalf("click Confirm Delete Remote Branch: %v", err)
	}
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("expected ActionErr after delete against unreachable remote")
	}
	if strings.Contains(err.Error(), "delsecretpw") {
		t.Fatalf("ActionErr leaks URL credential: %q", err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error after failed delete; texts=%q", h.ui.Texts())
	}
	for _, text := range h.ui.Texts() {
		if strings.Contains(text, "delsecretpw") {
			t.Fatalf("UI error leaks URL credential: %q", text)
		}
	}
}

// Switching repositories closes an open Delete Remote Branch dialog and
// discards the completion of an in-flight delete belonging to the previous
// repository so its error never appears on the newly opened repository.
func TestRemoteBranchDeleteDiscardsStaleResultOnRepoSwitch(t *testing.T) {
	repoA := remoteMenuFixture(t, "rb-delete-switch-a")
	// Point repoA's origin at a missing bare path so its delete fails fast
	// once released.
	repoA.Git("remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing-bare.git"))

	repoB := newGitRepo(t, "rb-delete-switch-b")
	repoB.CommitFile("b.txt", "b\n", "repo b initial")

	h := newTestApp(t, repoA.StoreRepo(), repoB.StoreRepo())
	closeSync(t, h)
	selectRepo(t, h, repoA.StoreRepo())

	// First check that switching repositories drops an open delete dialog.
	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Delete 'origin/feature'…"); err != nil {
		t.Fatalf("choose Delete item: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Delete Remote Branch") {
		t.Fatalf("delete dialog did not open; texts=%q", h.ui.Texts())
	}
	h.openRepo(repoB.StoreRepo())
	h.drain()
	if h.ui.HasText("Delete Remote Branch") {
		t.Fatal("delete dialog survived a repository switch")
	}

	// Now start a real delete on repoA, hold its worker, switch to repoB via
	// Repository Switcher, then release repoA's failing delete worker.
	selectRepo(t, h, repoA.StoreRepo())
	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature on repoA: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Delete 'origin/feature'…"); err != nil {
		t.Fatalf("choose Delete item on repoA: %v", err)
	}
	h.ui.Frame()

	h.queue.PauseWorkers()
	if err := h.ui.Click("Confirm Delete Remote Branch"); err != nil {
		t.Fatalf("click Confirm Delete Remote Branch: %v", err)
	}
	delWorker := h.queue.WaitWorker(t)
	h.ui.Frame()

	// Allow repoB's load to run while repoA's delete worker remains held.
	h.queue.ResumeWorkers()
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click(repoB.StoreRepo().Name); err != nil {
		t.Fatalf("select repoB: %v", err)
	}
	h.ui.Frame()

	// Release repoA's stale delete worker and drain everything.
	h.queue.ReleaseWorker(delWorker)
	h.drain()

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("repoB load failed: %v", err)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stale repoA delete leaked ActionErr onto repoB: %v", err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); ok {
		t.Fatalf("stale repoA delete error shown on repoB; texts=%q", h.ui.Texts())
	}
	if got := h.app.session.Meta().Name; got != repoB.StoreRepo().Name {
		t.Fatalf("active repo = %q, want %q", got, repoB.StoreRepo().Name)
	}
}

// --- Loopback SSH variants ---

// The checkout and delete of a remote branch run on the server of a loopback
// SSH repository: the checkout creates the tracking branch there, and the
// delete removes the branch from the server's own remote.
func TestRemoteBranchCheckoutAndDeleteLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	// feature on the server, pushed to a bare repo of the same server, and
	// removed locally: only the remote-tracking branch remains.
	srv.RunGit(t, repo.Path, "checkout", "-q", "-b", "feature")
	srv.WriteFile(t, repo.Path, "feature.txt", []byte("feature\n"))
	srv.RunGit(t, repo.Path, "add", "feature.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "feature work")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	bare := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repo.Path, "remote", "add", "origin", bare)
	srv.RunGit(t, repo.Path, "push", "-q", "-u", "origin", "main")
	srv.RunGit(t, repo.Path, "push", "-q", "origin", "feature")
	srv.RunGit(t, repo.Path, "branch", "-D", "feature")

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo)
	h.drain()

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	if _, ok := h.ui.Find("origin/feature"); !ok {
		t.Fatalf("origin/feature missing from the SSH sidebar; texts=%q", h.ui.Texts())
	}

	// Checkout on the server.
	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature over SSH: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Checkout…"); err != nil {
		t.Fatalf("choose Checkout… over SSH: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Checkout Remote Branch"); err != nil {
		t.Fatalf("click Confirm Checkout Remote Branch over SSH: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH remote branch checkout failed: %v", err)
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature" {
		t.Fatalf("SSH current branch after checkout = %q, want feature", got)
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "feature@{upstream}"); got != "origin/feature" {
		t.Fatalf("SSH feature upstream = %q, want origin/feature", got)
	}

	// Delete on the server: back on main first, since the checked out branch
	// tracks the remote branch being deleted.
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	h.app.session.Reload()
	h.drain()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature over SSH: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Delete 'origin/feature'…"); err != nil {
		t.Fatalf("choose Delete item over SSH: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Delete Remote Branch"); err != nil {
		t.Fatalf("click Confirm Delete Remote Branch over SSH: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH remote branch delete failed: %v", err)
	}
	out := srv.RunShell(t, "cd "+git.ShellQuote(bare)+"; git rev-parse --verify --quiet refs/heads/feature 2>/dev/null || echo gone")
	if strings.TrimSpace(out) != "gone" {
		t.Fatalf("feature survived on the SSH server's remote: %q", out)
	}
	if _, ok := h.ui.Find("origin/feature"); ok {
		t.Fatal("origin/feature stayed visible in the SSH sidebar after the delete")
	}
}
