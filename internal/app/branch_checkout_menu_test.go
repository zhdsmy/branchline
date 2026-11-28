package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// checkoutMenuFixture builds a repository with main checked out and a
// non-current feature branch, without any remote.
func checkoutMenuFixture(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("feature.txt", "feature\n", "feature work")
	repo.Checkout("main")
	return repo
}

// A non-current local branch shows Checkout in its context menu even without
// any remote or upstream, and choosing it checks the branch out exactly as a
// double-click does: HEAD moves and the sidebar marks the branch current. The
// current branch's own menu never carries Checkout.
func TestBranchCheckoutMenuNoRemoteChecksOutAndSidebarFollows(t *testing.T) {
	repo := checkoutMenuFixture(t, "checkout-menu-noremote")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click feature: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	if !slices.Contains(menu, "Checkout") {
		t.Fatalf("feature menu %q missing Checkout", menu)
	}
	if err := h.ui.ChooseMenuItem("Checkout"); err != nil {
		t.Fatalf("choose Checkout: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout feature failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("current branch after Checkout = %q, want feature", got)
	}
	// The sidebar follows the checkout: feature is now the current branch.
	row, ok := findAccessNode(t, h.ui, "feature")
	if !ok {
		t.Fatalf("no accessible node labeled feature after checkout; texts=%q", h.ui.Texts())
	}
	if !row.Selected {
		t.Fatalf("feature row Selected = false after checkout, want true: %+v", row)
	}

	// Now current, feature no longer shows Checkout in its menu.
	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click current feature: %v\ntexts=%q", err, h.ui.Texts())
	}
	if menu := h.ui.Menu(); slices.Contains(menu, "Checkout") {
		t.Fatalf("current branch feature menu %q must not show Checkout", menu)
	}
	h.ui.CloseMenu()
}

// On a branch tracking its upstream, Checkout sits immediately before the
// Checkout and… submenu.
func TestBranchCheckoutMenuPrecedesCheckoutAndSubmenu(t *testing.T) {
	repo := newGitRepo(t, "checkout-menu-tracked")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("feature.txt", "feature\n", "feature work")
	repo.Git("push", "-q", "-u", "origin", "feature")
	repo.Checkout("main")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click feature: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	h.ui.CloseMenu()
	checkoutAt := slices.Index(menu, "Checkout")
	checkoutAndAt := slices.Index(menu, "Checkout and…")
	if checkoutAt < 0 {
		t.Fatalf("tracked feature menu %q missing Checkout", menu)
	}
	if checkoutAndAt < 0 {
		t.Fatalf("tracked feature menu %q missing Checkout and…", menu)
	}
	if checkoutAt+1 != checkoutAndAt {
		t.Fatalf("tracked feature menu %q does not place Checkout immediately before Checkout and…", menu)
	}
}

// While a merge is in progress, Checkout is disabled: choosing it reports the
// item as disabled and HEAD stays where it is.
func TestBranchCheckoutMenuDisabledDuringMerge(t *testing.T) {
	repo := checkoutMenuFixture(t, "checkout-menu-merge")

	// Start a conflicting merge on main.
	repo.CheckoutNewBranch("conflict-side")
	repo.CommitFile("a.txt", "side\n", "side edit")
	repo.Checkout("main")
	repo.CommitFile("a.txt", "main\n", "main edit")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "conflict-side"); err == nil {
		t.Fatalf("conflicting merge unexpectedly succeeded: %s", out)
	}

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())
	if got := h.app.session.Snapshot().Status.State; got != "merge" {
		t.Fatalf("fixture: status state = %q, want merge", got)
	}

	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click feature during merge: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	if !slices.Contains(menu, "Checkout") {
		t.Fatalf("feature menu %q missing Checkout during merge", menu)
	}
	if err := h.ui.ChooseMenuItem("Checkout"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choose Checkout during merge = %v, want disabled error", err)
	}
	h.ui.CloseMenu()

	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("current branch moved to %q despite disabled Checkout", got)
	}

	// Double-clicking the branch row shares the same guard: it must not start
	// a checkout while the merge is in progress either.
	doubleClickCheckout(t, h, "feature")
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("double-click during merge started a failing checkout: %v", err)
	}
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("current branch moved to %q after double-click during merge", got)
	}
	repo.Git("merge", "--abort")
}

// Opening the context menu from the keyboard with Shift+F10 on the focused
// branch row shows the same Checkout item, and choosing it checks the branch
// out — the path keyboard and screen reader users take.
func TestBranchCheckoutMenuKeyboardShiftF10(t *testing.T) {
	repo := checkoutMenuFixture(t, "checkout-menu-keyboard")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	clickBranchRow(t, h, "feature")
	if info := sidebarAccessInfo(t, h, "feature"); info.Selected {
		t.Fatalf("feature row Selected = true before checkout, want false: %+v", info)
	}
	h.ui.Key(ui.Shift, ui.KeyF10)
	h.ui.Frame()

	menu := h.ui.Menu()
	if !slices.Contains(menu, "Checkout") {
		t.Fatalf("keyboard-opened feature menu %q missing Checkout", menu)
	}
	if err := h.ui.ChooseMenuItem("Checkout"); err != nil {
		t.Fatalf("choose Checkout from keyboard menu: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("keyboard menu checkout feature failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("current branch after keyboard Checkout = %q, want feature", got)
	}
	if info := sidebarAccessInfo(t, h, "feature"); !info.Selected {
		t.Fatalf("feature row Selected = false after keyboard checkout, want true: %+v", info)
	}
}

// The multi-selection batch menu never carries Checkout: checking out is a
// single-branch action.
func TestBranchCheckoutMenuAbsentFromBatchMenu(t *testing.T) {
	repo := checkoutMenuFixture(t, "checkout-menu-batch")
	repo.CreateBranch("topic")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	clickBranchRow(t, h, "feature")
	cmdClickBranchRow(t, h, "topic")

	if err := h.ui.RightClick("topic"); err != nil {
		t.Fatalf("right-click selected row topic: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	if slices.Contains(menu, "Checkout") {
		t.Fatalf("batch menu %q must not show Checkout", menu)
	}
	h.ui.CloseMenu()
}
