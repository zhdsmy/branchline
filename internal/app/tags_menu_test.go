package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// --- shared helpers of this file ---

// chooseTagMenuItem right-clicks the tag row and chooses the menu item, as a
// user does, verifying the chosen dialog opened through its exact title text.
func chooseTagMenuItem(t *testing.T, h *testHarness, tag, item, dialogTitle string) {
	t.Helper()
	if err := h.ui.RightClick(tag); err != nil {
		t.Fatalf("right-click tag row %q: %v\ntexts=%q", tag, err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem(item); err != nil {
		t.Fatalf("choose %q: %v; menu=%q", item, err, h.ui.Menu())
	}
	h.ui.Frame()
	if dialogTitle != "" && !hasExactText(h.ui, dialogTitle) {
		t.Fatalf("dialog %q did not open after choosing %q; texts=%q", dialogTitle, item, h.ui.Texts())
	}
}

// sshRepoDetached reports whether HEAD of the server repository is detached.
func sshRepoDetached(t *testing.T, srv *sshtest.Target, dir string) bool {
	t.Helper()
	script := "if git -C " + git.ShellQuote(dir) + " symbolic-ref -q HEAD >/dev/null 2>&1; then echo attached; else echo detached; fi"
	return strings.Contains(string(srv.RunShell(t, script)), "detached")
}

// assertBranchRefAt verifies the branch ref itself still names the hash,
// regardless of where HEAD is — the surviving half of a detached checkout.
func assertBranchRefAt(t *testing.T, dir, branch, hash string) {
	t.Helper()
	if got := gitOutTrim(t, dir, "rev-parse", "refs/heads/"+branch); got != hash {
		t.Fatalf("branch ref %s = %q, want %q", branch, got, hash)
	}
}

// The context menu of a tag row opens with Checkout Commit and Show Tag
// Details ahead of the existing push and delete entries, and the remote
// delete and copy items behind them: every new entry is reachable through
// the real menu, and the entries of the earlier cards keep their place.
func TestTagMenuEntriesAndOrder(t *testing.T) {
	repo, _, _, _ := tagsPushSidebarRepo(t, "tagmenu-order")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	if err := h.ui.RightClick("lw"); err != nil {
		t.Fatalf("right-click lw row: %v", err)
	}
	menu := h.ui.Menu()
	want := []string{
		"Checkout Commit…",
		"Show Tag Details…",
		"-",
		"Push…",
		"-",
		"Merge into 'main'…",
		"Rebase 'main' on 'lw'…",
		"Interactively Rebase on 'lw'…",
		"-",
		"New Branch…",
		"New Tag…",
		"Delete…",
		"Delete from Remote…",
		"-",
		"Copy Tag Name",
	}
	if len(menu) != len(want) {
		t.Fatalf("menu %q, want exactly %q", menu, want)
	}
	for i, item := range want {
		if menu[i] != item {
			t.Fatalf("menu[%d] = %q, want %q (full menu %q)", i, menu[i], item, menu)
		}
	}
	h.ui.CloseMenu()

	// The existing delete flow still works through the unchanged menu item.
	chooseTagMenuItem(t, h, "lw", "Delete…", "Delete Tag")
	h.drain()
	if !h.ui.HasText("Lightweight tag") {
		t.Fatalf("existing Delete Tag dialog did not open; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// Checkout Commit detaches HEAD at the commit a lightweight tag names: git
// rev-parse reports it and symbolic-ref fails, while the branch ref itself
// does not move.
func TestTagMenuCheckoutLightweightDetaches(t *testing.T) {
	repo, first, second := tagsSidebarRepo(t, "tagmenu-checkout")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// lw names the second commit and HEAD already is there: checking it out
	// detaches in place.
	chooseTagMenuItem(t, h, "lw", "Checkout Commit…", "Checkout Commit")
	if !h.ui.HasText("HEAD will be detached at " + short7(second)) {
		t.Fatalf("detach line missing the peeled commit; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout Tag Commit"); !ok || btn.Disabled {
		t.Fatalf("Confirm Checkout Tag Commit = %+v (found=%v), want enabled", btn, ok)
	}
	if err := h.ui.Click("Confirm Checkout Tag Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Tag Commit: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout of lw failed: %v", err)
	}
	assertDetached(t, repo.Path(), second)
	assertBranchRefAt(t, repo.Path(), "main", second)
	if first == second {
		t.Fatal("fixture broken: the two commits share a hash")
	}
}

// An annotated tag detaches HEAD exactly at the peeled commit, never at the
// tag object: the dialog announces the commit hash, and rev-parse agrees.
func TestTagMenuCheckoutAnnotatedDetachesAtPeeledCommit(t *testing.T) {
	repo, _, second := tagsSidebarRepo(t, "tagmenu-checkout-ann")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseTagMenuItem(t, h, "ann", "Checkout Commit…", "Checkout Commit")
	if !h.ui.HasText("HEAD will be detached at " + short7(second)) {
		t.Fatalf("annotated detach line missing the peeled commit %q; texts=%q", short7(second), h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Checkout Tag Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Tag Commit: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout of ann failed: %v", err)
	}
	assertDetached(t, repo.Path(), second)
	assertBranchRefAt(t, repo.Path(), "main", second)
}

// While HEAD already is detached at the commit a tag names, its Checkout
// Commit item is disabled — the checkout would be a no-op — while the other
// tag's item stays enabled.
func TestTagMenuCheckoutDisabledWhenDetachedThere(t *testing.T) {
	repo, first, second := tagsSidebarRepo(t, "tagmenu-detached-here")
	repo.Git("checkout", "--detach", first)
	repo.Git("update-ref", "refs/tags/lw", first)

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if got := h.app.session.Snapshot().Status.Branch; got != "" {
		t.Fatalf("fixture: branch = %q, want detached", got)
	}

	if err := h.ui.RightClick("lw"); err != nil {
		t.Fatalf("right-click lw row: %v", err)
	}
	err := h.ui.ChooseMenuItem("Checkout Commit…")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing checkout of the detached-at tag = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	if hasExactText(h.ui, "Checkout Commit") {
		t.Fatal("checkout dialog opened from a disabled menu item")
	}

	// ann names the second commit, not where HEAD is: its item stays enabled.
	if got := tagPointsAt(t, repo, "ann"); got != second || got == first {
		t.Fatalf("fixture broken: ann = %q, want the second commit distinct from HEAD", got)
	}
	chooseTagMenuItem(t, h, "ann", "Checkout Commit…", "Checkout Commit")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// While a merge is in progress the Checkout Commit item is disabled, because
// moving HEAD mid-operation would corrupt it.
func TestTagMenuCheckoutDisabledDuringMerge(t *testing.T) {
	repo := newGitRepo(t, "tagmenu-merge")
	repo.CommitFile("conflict.txt", "base\n", "base commit")
	repo.CreateBranch("side")
	repo.WriteFile("conflict.txt", "main\n")
	repo.Git("add", "--", "conflict.txt")
	repo.Git("commit", "-m", "main edits")
	repo.Checkout("side")
	repo.WriteFile("conflict.txt", "side\n")
	repo.Git("add", "--", "conflict.txt")
	repo.Git("commit", "-m", "side edits")
	repo.Checkout("main")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("conflicting merge unexpectedly succeeded: %s", out)
	}
	repo.Git("tag", "mtag", "HEAD")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if got := h.app.session.Snapshot().Status.State; got != "merge" {
		t.Fatalf("fixture: state = %q, want merge", got)
	}

	if err := h.ui.RightClick("mtag"); err != nil {
		t.Fatalf("right-click mtag row: %v", err)
	}
	err := h.ui.ChooseMenuItem("Checkout Commit…")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing checkout during a merge = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	if hasExactText(h.ui, "Checkout Commit") {
		t.Fatal("checkout dialog opened during a merge")
	}
}

// While a repository write is busy the Checkout Commit and Delete from
// Remote items are disabled, so a second write cannot be started over the
// running one; Copy Tag Name stays enabled because it reads nothing of the
// moving repository. The fixture has a remote, so the remote item's disabled
// state is the busy guard's, never the no-remote one.
func TestTagMenuWriteItemsDisabledWhileBusy(t *testing.T) {
	repo, _, _, second := tagsPushSidebarRepo(t, "tagmenu-busy")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	h.queue.PauseWorkers()
	defer h.queue.ResumeWorkers()
	if !h.app.session.do("Hold the session busy", func(*git.Repo) error { return nil }) {
		h.queue.ResumeWorkers()
		t.Fatal("session refused the holding write")
	}
	hold := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		h.queue.ReleaseWorker(hold)
		t.Fatal("session not busy while the write is held")
	}

	if err := h.ui.RightClick("lw"); err != nil {
		h.queue.ReleaseWorker(hold)
		t.Fatalf("right-click lw row while busy: %v", err)
	}
	err := h.ui.ChooseMenuItem("Checkout Commit…")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		h.queue.ReleaseWorker(hold)
		t.Fatalf("choosing checkout while busy = %v, want a disabled error", err)
	}
	if err := h.ui.ChooseMenuItem("Delete from Remote…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		h.queue.ReleaseWorker(hold)
		t.Fatalf("choosing Delete from Remote… while busy = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	if hasExactText(h.ui, "Checkout Commit") {
		h.queue.ReleaseWorker(hold)
		t.Fatal("checkout dialog opened while busy")
	}
	if h.app.tagsMenuUI().rdOpen {
		h.queue.ReleaseWorker(hold)
		t.Fatal("remote delete dialog opened while busy")
	}

	// Copy Tag Name works while the write is busy: it reads the name the menu
	// was opened on, nothing of the moving repository.
	if err := h.ui.RightClick("lw"); err != nil {
		h.queue.ReleaseWorker(hold)
		t.Fatalf("right-click lw row again while busy: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Copy Tag Name"); err != nil {
		h.queue.ReleaseWorker(hold)
		t.Fatalf("Copy Tag Name unavailable while busy: %v", err)
	}
	if got := h.ui.Clipboard(); got != "lw" {
		h.queue.ReleaseWorker(hold)
		t.Fatalf("clipboard while busy = %q, want %q", got, "lw")
	}

	h.queue.ReleaseWorker(hold)
	h.queue.ResumeWorkers()
	h.drain()
	assertStillOnBranch(t, repo.Path(), "main", second)
}

// A tag moved onto another commit after the dialog opened is never checked
// out: the guard refuses, the error explains why, and HEAD stays on the
// branch exactly where it was.
func TestTagMenuCheckoutGuardTagMoved(t *testing.T) {
	repo, first, second := tagsSidebarRepo(t, "tagmenu-guard-moved")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseTagMenuItem(t, h, "lw", "Checkout Commit…", "Checkout Commit")

	// The tag moves onto the first commit behind the dialog's back.
	repo.Git("update-ref", "refs/tags/lw", first)

	if err := h.ui.Click("Confirm Checkout Tag Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Tag Commit: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "moved to another commit")
	assertStillOnBranch(t, repo.Path(), "main", second)
}

// A tag deleted after the dialog opened verifies nothing: the guard refuses
// before any checkout runs, and HEAD stays exactly where it was.
func TestTagMenuCheckoutGuardTagDeleted(t *testing.T) {
	repo, _, second := tagsSidebarRepo(t, "tagmenu-guard-deleted")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseTagMenuItem(t, h, "lw", "Checkout Commit…", "Checkout Commit")

	repo.Git("tag", "-d", "lw")

	if err := h.ui.Click("Confirm Checkout Tag Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Tag Commit: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "verifying tag lw failed")
	assertStillOnBranch(t, repo.Path(), "main", second)
}

// When HEAD moved after the dialog opened — here an external commit — the
// guard refuses the checkout the user confirmed against the old state.
func TestTagMenuCheckoutGuardHeadMoved(t *testing.T) {
	repo, _, second := tagsSidebarRepo(t, "tagmenu-guard-head")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseTagMenuItem(t, h, "lw", "Checkout Commit…", "Checkout Commit")

	repo.WriteFile("other.txt", "other\n")
	repo.Git("add", "--", "other.txt")
	repo.Git("commit", "-m", "external commit")
	moved := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	if err := h.ui.Click("Confirm Checkout Tag Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Tag Commit: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "HEAD or the current branch changed")
	assertStillOnBranch(t, repo.Path(), "main", moved)
	if moved == second {
		t.Fatal("fixture broken: the external commit did not move HEAD")
	}
}

// Checking out a tag whose file version differs from a locally modified file
// opens the dialog offering to stash or discard the changes instead of an
// error banner. Canceling it leaves every byte of the local modification
// intact.
func TestTagMenuCheckoutConflictRefusedWithBytesIntact(t *testing.T) {
	repo := newGitRepo(t, "tagmenu-conflict")
	first := repo.CommitFile("f.txt", "v1\n", "add f v1")
	second := repo.CommitFile("f.txt", "v2\n", "update f to v2")
	repo.Git("tag", "lw", first)
	repo.WriteFile("f.txt", "local edits\n")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseTagMenuItem(t, h, "lw", "Checkout Commit…", "Checkout Commit")
	if err := h.ui.Click("Confirm Checkout Tag Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Tag Commit: %v", err)
	}
	h.drain()

	// The dialog opens with the file in the way, instead of an error banner.
	assertCheckoutBlockedPanel(t, h, "lw", []string{"f.txt"}, nil)

	cancelCheckoutBlocked(t, h)
	h.drain()

	assertStillOnBranch(t, repo.Path(), "main", second)
	assertFileBytes(t, repo, "f.txt", "local edits\n")

	// Once the user discards the conflicting edit themselves, the same
	// checkout detaches at the tagged commit.
	repo.Git("checkout", "--", "f.txt")
	chooseTagMenuItem(t, h, "lw", "Checkout Commit…", "Checkout Commit")
	if err := h.ui.Click("Confirm Checkout Tag Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Tag Commit again: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout after resolving the conflict failed: %v", err)
	}
	assertDetached(t, repo.Path(), first)
	assertFileBytes(t, repo, "f.txt", "v1\n")
}

// Show Tag Details reads the exact state of a lightweight tag: its kind, the
// commit it names with that commit's subject, and no annotated fields.
func TestTagMenuDetailsLightweight(t *testing.T) {
	repo, _, second := tagsSidebarRepo(t, "tagmenu-details-lw")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseTagMenuItem(t, h, "lw", "Show Tag Details…", "Tag Details")
	h.drain()

	if !h.ui.HasText("Lightweight tag") {
		t.Fatalf("lightweight kind not shown; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("Annotated tag") {
		t.Fatal("lightweight tag reported as annotated")
	}
	if !hasExactText(h.ui, short7(second)+" second commit") {
		t.Fatalf("commit line missing the short hash and subject; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText(second) {
		t.Fatalf("full commit hash missing; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("annotated release") {
		t.Fatal("the details of lw show the annotated tag's message")
	}

	if err := h.ui.Click("Close Tag Details"); err != nil {
		t.Fatalf("click Close Tag Details: %v", err)
	}
	h.ui.Frame()
	if hasExactText(h.ui, "Tag Details") {
		t.Fatal("Tag Details dialog survived Close")
	}
}

// An annotated tag shows its full multi-line message — the second line proves
// nothing is truncated to the first — together with the tagger, the date row
// and the target commit.
func TestTagMenuDetailsAnnotatedMultiLine(t *testing.T) {
	repo, _, second := tagsSidebarRepo(t, "tagmenu-details-ann")
	repo.Git("tag", "-a", "ml", "-m", "line one\nline two", "HEAD")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseTagMenuItem(t, h, "ml", "Show Tag Details…", "Tag Details")
	h.drain()

	if !h.ui.HasText("Annotated tag") {
		t.Fatalf("annotated kind not shown; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, short7(second)+" second commit") {
		t.Fatalf("commit line missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText(second) {
		t.Fatalf("full commit hash missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("line one") {
		t.Fatalf("first message line missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("line two") {
		t.Fatalf("second message line missing: the message is truncated to its first line; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Branchline Test") {
		t.Fatalf("tagger missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Date:") {
		t.Fatalf("date row missing; texts=%q", h.ui.Texts())
	}

	if err := h.ui.Click("Close Tag Details"); err != nil {
		t.Fatalf("click Close Tag Details: %v", err)
	}
	h.ui.Frame()
}

// While the details read is held in flight the dialog reports it is loading;
// a tag deleted before the read runs surfaces the read's error instead of
// details. The worker is held back with PauseWorkers for the loading half.
func TestTagMenuDetailsLoadingAndError(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "tagmenu-details-load")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	h.queue.PauseWorkers()
	chooseTagMenuItem(t, h, "lw", "Show Tag Details…", "Tag Details")
	reader := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.ui.HasText("Loading tag details") {
		t.Fatalf("loading note missing while the read is held; texts=%q", h.ui.Texts())
	}
	// The read-only dialog always offers its close button.
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Close Tag Details"); !ok || btn.Disabled {
		t.Fatalf("Close Tag Details while loading = %+v (found=%v), want enabled", btn, ok)
	}
	h.queue.ReleaseWorker(reader)
	h.queue.ResumeWorkers()
	h.drain()
	if !h.ui.HasText("Lightweight tag") {
		t.Fatalf("details did not appear after releasing the read; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Close Tag Details"); err != nil {
		t.Fatalf("click Close Tag Details: %v", err)
	}
	h.ui.Frame()

	// A tag deleted before the read runs cannot describe itself: the error
	// shows verbatim and no fabricated details appear. The deletion happens
	// before the dialog opens, deterministically — deleting it after the
	// choice would race the read instead.
	repo.Git("tag", "-d", "ann")
	chooseTagMenuItem(t, h, "ann", "Show Tag Details…", "Tag Details")
	h.drain()
	if !h.ui.HasText("verify tag ann") {
		t.Fatalf("read error not shown in the dialog; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("annotated release") {
		t.Fatal("details of a deleted tag were fabricated")
	}
	if err := h.ui.Click("Close Tag Details"); err != nil {
		t.Fatalf("click Close Tag Details after the error: %v", err)
	}
	h.ui.Frame()
}

// Switching the repository closes an open Tag Details dialog and clears its
// state: the read belongs to the repository that is gone. The switch is
// driven through the session because the modal swallows every click outside
// itself — including the repository switcher's — by design.
func TestTagMenuDetailsCloseOnRepoSwitch(t *testing.T) {
	repoA, _, _ := tagsSidebarRepo(t, "tagmenu-detswitch-a")
	repoB := newGitRepo(t, "tagmenu-detswitch-b")
	repoB.CommitFile("b.txt", "b\n", "commit in beta")

	h := newTestApp(t, repoB.StoreRepo())
	h.openRepo(repoA.StoreRepo())
	h.drain()

	chooseTagMenuItem(t, h, "lw", "Show Tag Details…", "Tag Details")
	h.drain()
	if !h.app.tagsMenuUI().detOpen {
		t.Fatal("details dialog did not open")
	}

	saved, ok := h.store.Get(h.store.Repos()[0].ID)
	if !ok {
		t.Fatal("seeded repository missing from the store")
	}
	h.app.openRepo(saved)
	h.ui.Frame()
	h.drain()

	st := h.app.tagsMenuUI()
	if st.detOpen {
		t.Fatal("details dialog survived the repository switch")
	}
	if st.detTag != "" || st.detChecking {
		t.Fatalf("details draft survived the switch: %+v", st)
	}
	if hasExactText(h.ui, "Tag Details") {
		t.Fatal("details dialog rendered again after the repository switch")
	}
}

// A confirmed Delete from Remote removes the tag on the server — read back
// from the bare repository — and nowhere else: the local tag survives.
func TestTagMenuRemoteDeleteSuccessKeepsLocal(t *testing.T) {
	repo, bare, _, _ := tagsPushSidebarRepo(t, "tagmenu-rmdel")
	repo.Git("push", "origin", "refs/tags/lw")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	chooseTagMenuItem(t, h, "lw", "Delete from Remote…", "Delete Tag from Remote")
	h.drain()

	if !h.ui.HasText("The local tag is kept") {
		t.Fatalf("remote deletion warning missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Remote Tag"); !ok || btn.Disabled {
		t.Fatalf("Confirm Delete Remote Tag = %+v (found=%v), want enabled after the probe", btn, ok)
	}
	if err := h.ui.Click("Confirm Delete Remote Tag"); err != nil {
		t.Fatalf("click Confirm Delete Remote Tag: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("remote delete failed: %v", err)
	}
	if bareTagExists(t, bare, "lw") {
		t.Fatal("lw survived on the remote after the confirmed deletion")
	}
	if !tagRefExists(t, repo, "lw") {
		t.Fatal("the remote deletion removed the local tag")
	}
	if h.app.tagsMenuUI().rdOpen {
		t.Fatal("remote delete dialog stayed open after the submit")
	}
}

// While the probe is held in flight the dialog reports it is checking and
// renders no confirm button at all; releasing the probe brings the button.
func TestTagMenuRemoteDeleteCheckingHasNoConfirm(t *testing.T) {
	repo, _, _, _ := tagsPushSidebarRepo(t, "tagmenu-rmdel-checking")
	repo.Git("push", "origin", "refs/tags/lw")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	h.queue.PauseWorkers()
	defer h.queue.ResumeWorkers()
	chooseTagMenuItem(t, h, "lw", "Delete from Remote…", "Delete Tag from Remote")
	probe := h.queue.WaitWorker(t)
	h.ui.Frame()

	if !h.ui.HasText("Checking remote tag on origin") {
		t.Fatalf("checking note missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Remote Tag"); ok {
		t.Fatalf("Confirm Delete Remote Tag present while checking: %+v", btn)
	}
	if err := h.ui.Click("Confirm Delete Remote Tag"); err == nil {
		t.Fatal("Confirm Delete Remote Tag was clickable while checking")
	}

	h.queue.ReleaseWorker(probe)
	h.queue.ResumeWorkers()
	h.drain()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Remote Tag"); !ok || btn.Disabled {
		t.Fatalf("Confirm Delete Remote Tag after the probe = %+v (found=%v), want enabled", btn, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// When the probe cannot read the remote it shows the real error and renders
// no confirm button; when the remote has no such tag the dialog says so and
// renders none either. Nothing is deleted in either state.
func TestTagMenuRemoteDeleteFailureAndAbsentHaveNoConfirm(t *testing.T) {
	repo, bare, _, _ := tagsPushSidebarRepo(t, "tagmenu-rmdel-noconfirm")
	repo.Git("push", "origin", "refs/tags/lw")
	repo.Git("remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	chooseTagMenuItem(t, h, "lw", "Delete from Remote…", "Delete Tag from Remote")
	h.drain()
	if !h.ui.HasText("Cannot verify the remote tag") {
		t.Fatalf("probe failure warning missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("does not appear to be a git repository") {
		t.Fatalf("probe failure error text missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Remote Tag"); ok {
		t.Fatalf("Confirm Delete Remote Tag present after a failed probe: %+v", btn)
	}
	if err := h.ui.Click("Confirm Delete Remote Tag"); err == nil {
		t.Fatal("Confirm Delete Remote Tag was clickable after the probe failed")
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	// Restore the remote and probe a tag the server never had: the dialog
	// says so and offers no delete.
	repo.Git("remote", "set-url", "origin", bare)
	h.app.session.Reload()
	h.drain()
	chooseTagMenuItem(t, h, "ann", "Delete from Remote…", "Delete Tag from Remote")
	h.drain()
	if !h.ui.HasText("no tag named ann") {
		t.Fatalf("absent-remote note missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Remote Tag"); ok {
		t.Fatalf("Confirm Delete Remote Tag present for an absent remote tag: %+v", btn)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if !bareTagExists(t, bare, "lw") {
		t.Fatal("lw vanished from the remote even though nothing was confirmed")
	}
}

// A remote tag moved after the probe answered is never deleted: the lease is
// stale, git refuses atomically, the server keeps its new value, and the
// error surfaces. The retargeting force-push of a second clone is how a real
// concurrent client would move it.
func TestTagMenuRemoteDeleteStaleLeaseProtects(t *testing.T) {
	repo, bare, first, second := tagsPushSidebarRepo(t, "tagmenu-rmdel-stale")
	repo.Git("push", "origin", "refs/tags/lw")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	chooseTagMenuItem(t, h, "lw", "Delete from Remote…", "Delete Tag from Remote")
	h.drain()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Remote Tag"); !ok || btn.Disabled {
		t.Fatalf("Confirm Delete Remote Tag missing after the probe; texts=%q", h.ui.Texts())
	}

	// A second clone retargets the remote tag onto the first commit behind
	// the dialog's back.
	other := filepath.Join(t.TempDir(), "other")
	runGitCommand(t, "", "clone", bare, other)
	runGitCommand(t, other, "config", "user.name", "Ada")
	runGitCommand(t, other, "config", "user.email", "ada@example.com")
	runGitCommand(t, other, "tag", "-f", "lw", first)
	runGitCommand(t, other, "push", "--force", "origin", "refs/tags/lw")
	if got := bareTagOID(t, bare, "lw"); got != first {
		t.Fatalf("fixture: retargeted remote lw = %q, want %q", got, first)
	}

	if err := h.ui.Click("Confirm Delete Remote Tag"); err != nil {
		t.Fatalf("click Confirm Delete Remote Tag: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("deleting with a stale lease produced no error")
	}
	if got := bareTagOID(t, bare, "lw"); got != first {
		t.Fatalf("remote lw = %q after the stale lease, want the concurrent %q untouched", got, first)
	}
	if got := bareTagOID(t, bare, "lw"); got == second {
		t.Fatal("the stale lease deleted and moved nothing yet the value is wrong: fixture inverted")
	}
	if !tagRefExists(t, repo, "lw") {
		t.Fatal("the refused remote deletion removed the local tag")
	}
}

// Choosing between two remotes probes the chosen one again instead of
// reusing the first remote's answer: the deletion lands exactly where the
// user picked.
func TestTagMenuRemoteDeleteMultiRemoteSwitch(t *testing.T) {
	repo, originBare, _, _ := tagsPushSidebarRepo(t, "tagmenu-rmdel-remotes")
	backupBare := repo.AddBareRemote("backup")
	repo.Git("push", "origin", "refs/tags/lw")
	repo.Git("push", "backup", "refs/tags/lw")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	chooseTagMenuItem(t, h, "lw", "Delete from Remote…", "Delete Tag from Remote")
	h.drain()
	if !h.ui.HasText("Checking remote tag") && !h.ui.HasText("The remote tag points at") {
		t.Fatalf("probe result missing; texts=%q", h.ui.Texts())
	}

	// Keyboard-drive the real select: open it, move the highlight from origin
	// to backup, and choose with Enter.
	remotes := h.app.session.Snapshot().Refs.Remotes
	from, to := -1, -1
	for i, remote := range remotes {
		if remote == "origin" {
			from = i
		}
		if remote == "backup" {
			to = i
		}
	}
	if from < 0 || to < 0 || from == to {
		t.Fatalf("fixture remotes = %v, want origin and backup at different positions", remotes)
	}
	if err := h.ui.Click("Delete Remote Tag Remote"); err != nil {
		t.Fatalf("open remote select: %v", err)
	}
	h.ui.Frame()
	for range absInt(to - from) {
		if to > from {
			h.ui.Key(0, ui.KeyDown)
		} else {
			h.ui.Key(0, ui.KeyUp)
		}
		h.ui.Frame()
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if got := h.app.tagsMenuUI().rdRemote; got != "backup" {
		t.Fatalf("chosen remote = %q, want backup", got)
	}

	// The switch re-probed backup: its answer must be its own, and it is.
	h.drain()
	if !h.ui.HasText("The local tag is kept") {
		t.Fatalf("probe after the remote switch did not confirm the tag on backup; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Delete Remote Tag"); err != nil {
		t.Fatalf("click Confirm Delete Remote Tag: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("remote delete on backup failed: %v", err)
	}
	if bareTagExists(t, backupBare, "lw") {
		t.Fatal("lw survived on backup after the confirmed deletion")
	}
	if !bareTagExists(t, originBare, "lw") {
		t.Fatal("lw vanished from origin even though backup was selected")
	}
}

// Without a remote the Delete from Remote item is disabled: there is nowhere
// to delete from.
func TestTagMenuRemoteDeleteDisabledWithoutRemote(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "tagmenu-rmdel-noremote")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("lw"); err != nil {
		t.Fatalf("right-click lw row: %v", err)
	}
	err := h.ui.ChooseMenuItem("Delete from Remote…")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing Delete from Remote… without a remote = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	if hasExactText(h.ui, "Delete Tag from Remote") {
		t.Fatal("remote delete dialog opened without a remote")
	}
}

// Copy Tag Name puts exactly the clicked tag's name on the clipboard, even
// for an annotated tag, and it never disables: the same copy works right
// after another one replaced the clipboard.
func TestTagMenuCopyTagName(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "tagmenu-copy")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("lw"); err != nil {
		t.Fatalf("right-click lw row: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Copy Tag Name"); err != nil {
		t.Fatalf("choose Copy Tag Name: %v", err)
	}
	h.ui.Frame()
	if got := h.ui.Clipboard(); got != "lw" {
		t.Fatalf("clipboard = %q, want %q", got, "lw")
	}

	if err := h.ui.RightClick("ann"); err != nil {
		t.Fatalf("right-click ann row: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Copy Tag Name"); err != nil {
		t.Fatalf("choose Copy Tag Name for ann: %v", err)
	}
	h.ui.Frame()
	if got := h.ui.Clipboard(); got != "ann" {
		t.Fatalf("clipboard = %q, want the annotated tag's name %q", got, "ann")
	}
}

// Each of the three new dialogs is horizontally centered in the 1200-wide
// test window with its buttons inside its own bounds.
func TestTagMenuDialogsCenteredLayout(t *testing.T) {
	repo, _, _, _ := tagsPushSidebarRepo(t, "tagmenu-layout")
	repo.Git("push", "origin", "refs/tags/lw")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	chooseTagMenuItem(t, h, "lw", "Checkout Commit…", "Checkout Commit")
	assertDialogLayout(t, h, "Cancel", "Confirm Checkout Tag Commit")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	chooseTagMenuItem(t, h, "lw", "Show Tag Details…", "Tag Details")
	assertDialogLayout(t, h, "Close Tag Details")
	if err := h.ui.Click("Close Tag Details"); err != nil {
		t.Fatalf("click Close Tag Details: %v", err)
	}
	h.ui.Frame()

	chooseTagMenuItem(t, h, "lw", "Delete from Remote…", "Delete Tag from Remote")
	h.drain()
	assertDialogLayout(t, h, "Cancel", "Confirm Delete Remote Tag")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// --- Loopback SSH variants ---

// Over loopback SSH the three new flows act on the server's repository: the
// checkout detaches the server's HEAD at the tagged commit, the details read
// the annotated tag's multi-line message, and the confirmed remote deletion
// removes the tag from the server's remote while the local one survives.
func TestTagMenuCheckoutDetailsAndRemoteDeleteLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	first := srv.RunGit(t, repoDir, "rev-parse", "HEAD")
	srv.WriteFile(t, repoDir, "b.txt", []byte("two\n"))
	srv.RunGit(t, repoDir, "add", "b.txt")
	srv.RunGit(t, repoDir, "commit", "-q", "-m", "second commit")
	second := srv.RunGit(t, repoDir, "rev-parse", "HEAD")
	srv.RunGit(t, repoDir, "tag", "ssh-lw", first)
	srv.RunGit(t, repoDir, "tag", "-a", "ssh-ann", "-m", "line one\nline two", "HEAD")
	srv.RunGit(t, repoDir, "push", "-q", "origin", "main")

	h := newTestApp(t, repo)
	closeSync(t, h)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	for _, want := range []string{"ssh-lw", "ssh-ann"} {
		if _, ok := h.ui.Find(want); !ok {
			t.Fatalf("SSH sidebar missing %q; texts=%q", want, h.ui.Texts())
		}
	}

	// Checkout the lightweight tag: the server's HEAD detaches at the first
	// commit while its branch ref keeps the second.
	chooseTagMenuItem(t, h, "ssh-lw", "Checkout Commit…", "Checkout Commit")
	if err := h.ui.Click("Confirm Checkout Tag Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Tag Commit: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH checkout failed: %v", err)
	}
	if got := srv.RunGit(t, repoDir, "rev-parse", "HEAD"); got != first {
		t.Fatalf("server HEAD = %q, want the tagged commit %q", got, first)
	}
	if !sshRepoDetached(t, srv, repoDir) {
		t.Fatal("server HEAD not detached after the confirmed checkout")
	}
	if got := srv.RunGit(t, repoDir, "rev-parse", "refs/heads/main"); got != second {
		t.Fatalf("server main = %q, want the unmoved %q", got, second)
	}

	// The annotated tag's details carry the full multi-line message.
	chooseTagMenuItem(t, h, "ssh-ann", "Show Tag Details…", "Tag Details")
	h.drain()
	if !h.ui.HasText("Annotated tag") {
		t.Fatalf("SSH annotated kind not shown; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("line one") || !h.ui.HasText("line two") {
		t.Fatalf("SSH multi-line message not shown in full; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Close Tag Details"); err != nil {
		t.Fatalf("click Close Tag Details: %v", err)
	}
	h.ui.Frame()

	// Push the tag to the server's remote, then delete it back off through
	// the probed dialog: the bare loses it, the server keeps its local tag.
	srv.RunGit(t, repoDir, "push", "-q", "origin", "refs/tags/ssh-lw")
	if !sshBareTagExists(t, srv, bareDir, "ssh-lw") {
		t.Fatal("fixture: ssh-lw did not reach the server remote")
	}

	chooseTagMenuItem(t, h, "ssh-lw", "Delete from Remote…", "Delete Tag from Remote")
	h.drain()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Remote Tag"); !ok || btn.Disabled {
		t.Fatalf("Confirm Delete Remote Tag missing after the SSH probe; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Delete Remote Tag"); err != nil {
		t.Fatalf("click Confirm Delete Remote Tag: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH remote delete failed: %v", err)
	}
	if sshBareTagExists(t, srv, bareDir, "ssh-lw") {
		t.Fatal("ssh-lw survived on the server remote after the confirmed deletion")
	}
	if !sshTagRefExists(t, srv, repoDir, "ssh-lw") {
		t.Fatal("the remote deletion removed the server's local tag")
	}
}
