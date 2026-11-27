package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// selectBranchesForBatch selects the named branch rows through real clicks:
// a plain click on the first, Cmd-clicks on the rest.
func selectBranchesForBatch(t *testing.T, h *testHarness, names ...string) {
	t.Helper()
	for i, name := range names {
		if i == 0 {
			clickBranchRow(t, h, name)
		} else {
			cmdClickBranchRow(t, h, name)
		}
	}
	wantPicked(t, h, names...)
}

// chooseBatchItem right-clicks the selected row and chooses the batch menu
// item, failing the test unless the menu carries it enabled.
func chooseBatchItem(t *testing.T, h *testHarness, row, item string) {
	t.Helper()
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click selected row %q: %v\ntexts=%q", row, err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem(item); err != nil {
		t.Fatalf("choose %q: %v; menu=%q", item, err, h.ui.Menu())
	}
	h.ui.Frame()
}

// batchFFFixture builds a repository whose clone at dir has three fixture
// branches: behind, which tracks an upstream one commit ahead of it;
// diverged, which has one commit of its own beyond its upstream; and noup,
// which tracks nothing. The returned bareDir is the origin the upstreams
// advance on.
func batchFFFixture(t *testing.T, name string) (repo *gitRepoFixture, bareDir string) {
	t.Helper()
	repo = newGitRepo(t, name)
	base := repo.CommitFile("base.txt", "base\n", "base commit")
	for _, br := range []string{"behind", "diverged", "noup"} {
		repo.Git("branch", br)
	}
	bareDir = repo.AddBareRemote("origin")
	repo.Git("push", "-q", "origin", "behind:refs/heads/behind", "diverged:refs/heads/diverged")
	for _, br := range []string{"behind", "diverged"} {
		repo.Git("branch", "--set-upstream-to", "origin/"+br, br)
	}

	// The upstreams of behind and diverged both gain a commit the local
	// branches do not have.
	other := t.TempDir() + "/peer"
	runGitCommand(t, "", "clone", "-q", bareDir, other)
	runGitCommand(t, other, "-c", "user.name=Peer", "-c", "user.email=peer@test.local", "checkout", "-q", "-b", "peerwork", "origin/behind")
	writeTestFile(t, other, "up.txt", "upstream move\n")
	runGitCommand(t, other, "add", "up.txt")
	runGitCommand(t, other, "-c", "user.name=Peer", "-c", "user.email=peer@test.local", "commit", "-qm", "upstream move")
	runGitCommand(t, other, "push", "-q", "origin", "peerwork:behind")
	runGitCommand(t, other, "push", "-q", "origin", "peerwork:diverged")

	// diverged gains its own commit on top: with the upstream moved too, it
	// has both ahead and behind commits — it diverged.
	repo.Git("checkout", "-q", "diverged")
	repo.CommitFile("own.txt", "own work\n", "diverged own work")
	repo.Git("checkout", "-q", "main")

	repo.Git("fetch", "-q", "origin")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "refs/heads/behind"); got != base {
		t.Fatalf("fixture: behind moved during setup: %q, want base %q", got, base)
	}
	return repo, bareDir
}

func writeTestFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// Fast-Forward of the batch menu moves exactly the selected branches that
// are behind their upstreams with nothing of their own: the diverged branch
// and the one without an upstream stay exactly where they are, and the toast
// counts one moved of the three selected.
func TestBranchBatchFastForward(t *testing.T) {
	repo, _ := batchFFFixture(t, "batch-ff")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	for _, br := range []string{"behind", "diverged", "noup"} {
		if _, ok := h.ui.Find(br); !ok {
			t.Fatalf("fixture branch %q missing from the sidebar; texts=%q", br, h.ui.Texts())
		}
	}
	selectBranchesForBatch(t, h, "behind", "diverged", "noup")

	behindBefore := branchHash(t, repo, "behind")
	divergedBefore := branchHash(t, repo, "diverged")
	noupBefore := branchHash(t, repo, "noup")
	originBehind := gitOutTrim(t, repo.Path(), "rev-parse", "refs/remotes/origin/behind")
	if originBehind == behindBefore {
		t.Fatal("fixture broken: origin/behind never moved ahead of behind")
	}

	chooseBatchItem(t, h, "behind", "Fast-Forward 3 Branches to Their Upstreams")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected fast-forward error: %v", err)
	}
	if !h.ui.HasText("Fast-forwarded 1 of 3 branches.") {
		t.Fatalf("toast %q missing; texts=%q", "Fast-forwarded 1 of 3 branches.", h.ui.Texts())
	}
	if got := branchHash(t, repo, "behind"); got != originBehind {
		t.Fatalf("refs/heads/behind = %q, want the upstream commit %q", got, originBehind)
	}
	if got := branchHash(t, repo, "diverged"); got != divergedBefore {
		t.Fatalf("diverged moved to %q, want untouched %q", got, divergedBefore)
	}
	if got := branchHash(t, repo, "noup"); got != noupBefore {
		t.Fatalf("noup moved to %q, want untouched %q", got, noupBefore)
	}
	// main never entered the selection and never moved.
	if got := branchHash(t, repo, "main"); got == originBehind {
		t.Fatal("main moved even though it was not selected")
	}
}

// Selecting only branches without an upstream shows the batch menu without a
// fast-forward entry at all, starting with the delete item.
func TestBranchBatchMenuWithoutUpstreamsOmitsFastForward(t *testing.T) {
	repo := pickFixture(t, "batch-menu-plain")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectBranchesForBatch(t, h, "alpha", "zeta")
	if err := h.ui.RightClick("zeta"); err != nil {
		t.Fatalf("right-click selected row: %v", err)
	}
	menu := h.ui.Menu()
	want := []string{"Delete 2 Branches…", "-", "Copy Branch Names"}
	if !slices.Equal(menu, want) {
		t.Fatalf("batch menu = %q, want exactly %q", menu, want)
	}
	h.ui.CloseMenu()
	h.ui.Frame()
}

// The delete item of the batch menu counts only the selected branches that
// are not checked out: the current branch is neither counted nor listed, and
// the dialog opens with every other branch checked.
func TestBranchBatchDeleteDialogListsNonCurrentBranches(t *testing.T) {
	repo := pickFixture(t, "batch-dlg")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectBranchesForBatch(t, h, "main", "alpha", "zeta")
	if err := h.ui.RightClick("alpha"); err != nil {
		t.Fatalf("right-click selected row alpha: %v", err)
	}
	menu := h.ui.Menu()
	if !slices.Contains(menu, "Delete 2 Branches…") {
		t.Fatalf("batch menu %q misses the count excluding the current branch", menu)
	}
	if slices.Contains(menu, "Delete 3 Branches…") {
		t.Fatalf("batch menu %q counts the current branch", menu)
	}
	if err := h.ui.ChooseMenuItem("Delete 2 Branches…"); err != nil {
		t.Fatalf("choose Delete 2 Branches…: %v; menu=%q", err, menu)
	}
	h.ui.Frame()

	if !hasExactText(h.ui, "Delete Branches") {
		t.Fatalf("Delete Branches dialog did not open; texts=%q", h.ui.Texts())
	}
	for _, name := range []string{"alpha", "zeta"} {
		if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, name); !ok {
			t.Fatalf("branch %q missing from the dialog; texts=%q", name, h.ui.Texts())
		}
	}
	if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, "main"); ok {
		t.Fatalf("current branch main is listed in the dialog; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Commits only these branches have are lost from the log.") {
		t.Fatalf("loss warning missing; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Also delete corresponding remote branches"); ok {
		t.Fatalf("remote deletion offered without any upstream; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Branches"); !ok || btn.Disabled {
		t.Fatalf("Confirm Delete Branches with all rows checked = %+v (found=%v), want enabled", btn, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("cancel dialog: %v", err)
	}
	h.ui.Frame()
}

// Deleting several branches through the batch dialog removes exactly the
// checked ones from the repository: an unchecked row and every unselected
// branch survive, and the selection clears afterwards.
func TestBranchBatchDeleteBranches(t *testing.T) {
	repo := pickFixture(t, "batch-delete")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectBranchesForBatch(t, h, "alpha", "beta", "master")
	chooseBatchItem(t, h, "alpha", "Delete 3 Branches…")

	// Uncheck beta: it survives the batch.
	if err := h.ui.Click("beta"); err != nil {
		t.Fatalf("uncheck beta row: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Delete Branches"); err != nil {
		t.Fatalf("click Confirm Delete Branches: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected batch delete error: %v", err)
	}
	for _, gone := range []string{"alpha", "master"} {
		if branchExists(t, repo, gone) {
			t.Fatalf("%s survived the confirmed batch delete", gone)
		}
	}
	for _, kept := range []string{"beta", "zeta", "release/one", "main"} {
		if !branchExists(t, repo, kept) {
			t.Fatalf("%s is gone even though it was not checked for deletion", kept)
		}
	}
	wantPicked(t, h)
}

// Deleting with the remote box ticked removes the upstream branches from the
// server along with the local ones; unticked, the server keeps them.
func TestBranchBatchDeleteRemoteBare(t *testing.T) {
	repo := newGitRepo(t, "batch-del-remote")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	for _, br := range []string{"rb1", "rb2", "rb3", "rb4"} {
		repo.CreateBranch(br)
	}
	bareDir := repo.AddBareRemote("origin")
	repo.Git("push", "-q", "origin", "rb1:refs/heads/rb1", "rb2:refs/heads/rb2", "rb3:refs/heads/rb3", "rb4:refs/heads/rb4")
	for _, br := range []string{"rb1", "rb2", "rb3", "rb4"} {
		repo.Git("branch", "--set-upstream-to", "origin/"+br, br)
	}
	remoteHas := func(br string) bool {
		return gitOutTrim(t, bareDir, "branch", "--list", br) != ""
	}

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openDialog := func(a, b string) {
		t.Helper()
		selectBranchesForBatch(t, h, a, b)
		chooseBatchItem(t, h, a, "Delete 2 Branches…")
		if !h.ui.HasText("→ origin/"+a) || !h.ui.HasText("→ origin/"+b) {
			t.Fatalf("upstream annotations missing; texts=%q", h.ui.Texts())
		}
		if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Also delete corresponding remote branches"); !ok {
			t.Fatalf("remote deletion checkbox missing; texts=%q", h.ui.Texts())
		}
		if h.ui.HasText("deleted on the server") {
			t.Fatalf("server warning shown before the box is ticked; texts=%q", h.ui.Texts())
		}
	}
	confirm := func() {
		t.Helper()
		if err := h.ui.Click("Confirm Delete Branches"); err != nil {
			t.Fatalf("click Confirm Delete Branches: %v", err)
		}
		h.drain()
		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("unexpected batch delete error: %v", err)
		}
	}

	// Round one, unticked: the server keeps rb1 and rb2.
	openDialog("rb1", "rb2")
	confirm()
	for _, br := range []string{"rb1", "rb2"} {
		if branchExists(t, repo, br) {
			t.Fatalf("%s survived the confirmed batch delete", br)
		}
		if !remoteHas(br) {
			t.Fatalf("origin/%s is gone even though the remote box was unticked", br)
		}
	}

	// Round two, ticked: rb3 and rb4 leave the server too.
	openDialog("rb3", "rb4")
	if err := h.ui.Click("Also delete corresponding remote branches"); err != nil {
		t.Fatalf("tick remote deletion: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if !h.ui.HasText("The remote branches are deleted on the server for everyone.") {
		t.Fatalf("server warning missing after ticking; texts=%q", h.ui.Texts())
	}
	confirm()
	for _, br := range []string{"rb3", "rb4"} {
		if branchExists(t, repo, br) {
			t.Fatalf("%s survived the confirmed batch delete", br)
		}
		if remoteHas(br) {
			t.Fatalf("origin/%s survived on the server even though the remote box was ticked", br)
		}
	}
	// The unticked round's upstreams still sit on the server.
	for _, br := range []string{"rb1", "rb2"} {
		if !remoteHas(br) {
			t.Fatalf("origin/%s vanished during the second round", br)
		}
	}
}

// A branch moved outside the app after the dialog opened refuses the whole
// batch: nothing is deleted, not even the branches that never moved, and the
// error says the branch changed after the dialog opened.
func TestBranchBatchDeleteGuardRefusesMovedBranch(t *testing.T) {
	repo := pickFixture(t, "batch-guard")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectBranchesForBatch(t, h, "alpha", "beta")
	chooseBatchItem(t, h, "alpha", "Delete 2 Branches…")
	if !hasExactText(h.ui, "Delete Branches") {
		t.Fatalf("Delete Branches dialog did not open; texts=%q", h.ui.Texts())
	}

	// alpha gains a commit HEAD does not have, after the dialog captured it.
	repo.Git("checkout", "-q", "alpha")
	repo.CommitFile("late.txt", "late work\n", "late external work")
	repo.Git("checkout", "-q", "main")

	if err := h.ui.Click("Confirm Delete Branches"); err != nil {
		t.Fatalf("click Confirm Delete Branches: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("deleting over an externally moved branch produced no error")
	} else if !strings.Contains(err.Error(), "alpha changed after the dialog opened") {
		t.Fatalf("ActionErr = %q, want it to say alpha changed after the dialog opened", err)
	}
	for _, br := range []string{"alpha", "beta"} {
		if !branchExists(t, repo, br) {
			t.Fatalf("%s was deleted even though the batch was refused", br)
		}
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error status after the refused delete; texts=%q", h.ui.Texts())
	}
}

// Canceling the Delete Branches dialog runs no git command at all: the
// counting executor installed before the dialog opens records nothing, and
// every branch survives.
func TestBranchBatchDeleteCancelRunsNoGitCommands(t *testing.T) {
	repo := pickFixture(t, "batch-cancel")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectBranchesForBatch(t, h, "alpha", "beta")
	if err := h.ui.RightClick("alpha"); err != nil {
		t.Fatalf("right-click alpha: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Delete 2 Branches…"); err != nil {
		t.Fatalf("choose Delete 2 Branches…: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Delete Branches") {
		t.Fatalf("Delete Branches dialog did not open; texts=%q", h.ui.Texts())
	}

	counting := &countingExecutor{real: h.app.session.Repo().Exec}
	h.app.session.Repo().Exec = counting
	t.Cleanup(func() {
		h.queue.releaseAllAndWait()
		h.app.session.Repo().Exec = counting.real
	})

	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	for i := 0; i < 3; i++ {
		h.ui.Frame()
	}
	if counting.reads != 0 {
		t.Fatalf("canceling the dialog ran %d git commands", counting.reads)
	}
	for _, br := range []string{"alpha", "beta"} {
		if !branchExists(t, repo, br) {
			t.Fatalf("%s is gone even though the dialog was canceled", br)
		}
	}
}

// Dismissing the Delete Branches dialog — with Escape or by clicking the
// backdrop — runs no git command, exactly like Cancel.
func TestBranchBatchDeleteEscapeAndBackdropRunNoGitCommands(t *testing.T) {
	repo := pickFixture(t, "batch-dismiss")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	counting := &countingExecutor{real: h.app.session.Repo().Exec}
	h.app.session.Repo().Exec = counting
	t.Cleanup(func() {
		h.queue.releaseAllAndWait()
		h.app.session.Repo().Exec = counting.real
	})

	openDialog := func() {
		t.Helper()
		selectBranchesForBatch(t, h, "alpha", "beta")
		chooseBatchItem(t, h, "alpha", "Delete 2 Branches…")
		if !hasExactText(h.ui, "Delete Branches") {
			t.Fatalf("Delete Branches dialog did not open; texts=%q", h.ui.Texts())
		}
		counting.reads = 0
	}

	// Escape dismisses the dialog without a single git invocation.
	openDialog()
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	if hasExactText(h.ui, "Delete Branches") {
		t.Fatal("Escape did not dismiss the Delete Branches dialog")
	}
	if counting.reads != 0 {
		t.Fatalf("Escape-dismissing the dialog ran %d git commands", counting.reads)
	}

	// Clicking the dimmed backdrop outside the dialog dismisses it the same
	// way: the modal is centered in the 1200x800 window, so (20, 20) is well
	// inside the backdrop.
	openDialog()
	h.ui.ClickAt(20, 20)
	h.ui.Frame()
	if hasExactText(h.ui, "Delete Branches") {
		t.Fatal("clicking the backdrop did not dismiss the Delete Branches dialog")
	}
	if counting.reads != 0 {
		t.Fatalf("backdrop-dismissing the dialog ran %d git commands", counting.reads)
	}

	for _, br := range []string{"alpha", "beta"} {
		if !branchExists(t, repo, br) {
			t.Fatalf("%s is gone even though the dialog was only dismissed", br)
		}
	}
}

// Switching repositories with the Delete Branches dialog open discards it:
// nothing is deleted in the repository the dialog belonged to.
func TestBranchBatchDeleteRepoSwitchRunsNoGitCommands(t *testing.T) {
	repoA := pickFixture(t, "batch-switch-a")
	repoB := newGitRepo(t, "batch-switch-b")
	repoB.CommitFile("b.txt", "b\n", "b commit")

	h := newTestApp(t, repoB.StoreRepo())
	h.openRepo(repoA.StoreRepo())
	h.drain()

	selectBranchesForBatch(t, h, "alpha", "beta")
	chooseBatchItem(t, h, "alpha", "Delete 2 Branches…")
	if !hasExactText(h.ui, "Delete Branches") {
		t.Fatalf("Delete Branches dialog did not open; texts=%q", h.ui.Texts())
	}

	counting := &countingExecutor{real: h.app.session.Repo().Exec}
	h.app.session.Repo().Exec = counting
	t.Cleanup(func() {
		h.queue.releaseAllAndWait()
		if repo := h.app.session.Repo(); repo != nil && repo.Exec == counting {
			repo.Exec = counting.real
		}
	})

	h.openRepo(repoB.StoreRepo())
	h.drain()

	if hasExactText(h.ui, "Delete Branches") {
		t.Fatal("the Delete Branches dialog survived a repository switch")
	}
	if counting.reads != 0 {
		t.Fatalf("switching repositories over the open dialog ran %d git commands on it", counting.reads)
	}
	for _, br := range []string{"alpha", "beta"} {
		if !branchExists(t, repoA, br) {
			t.Fatalf("%s is gone even though its repository was switched away", br)
		}
	}
}

// Copy Branch Names of the batch menu writes the selected branches one per
// line in the order the sidebar shows them, which follows the sort order.
func TestBranchBatchCopyNamesFollowsDisplayOrder(t *testing.T) {
	repo := pickFixture(t, "batch-copy")

	h := newTestApp(t)
	settings := h.store.Settings()
	settings.LocalBranchSort = ""
	if err := h.store.SetSettings(settings); err != nil {
		t.Fatalf("save alphabetical sort: %v", err)
	}
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Alphabetical display: alpha, beta, master. Select out of order on
	// purpose: the copy order is the display order, not the click order.
	selectBranchesForBatch(t, h, "master", "beta", "alpha")
	chooseBatchItem(t, h, "beta", "Copy Branch Names")
	if got := h.ui.Clipboard(); got != "alpha\nbeta\nmaster" {
		t.Fatalf("clipboard = %q, want %q", got, "alpha\\nbeta\\nmaster")
	}

	// Backward sorting flips the display, and the copy follows it: the same
	// selection, which survived the reorder, copies in reverse.
	settings.LocalBranchSort = refSortBackward
	if err := h.store.SetSettings(settings); err != nil {
		t.Fatalf("save backward sort: %v", err)
	}
	h.ui.Frame()
	assertSidebarOrder(t, h, "master", "beta", "alpha")
	wantPicked(t, h, "alpha", "beta", "master")
	chooseBatchItem(t, h, "beta", "Copy Branch Names")
	if got := h.ui.Clipboard(); got != "master\nbeta\nalpha" {
		t.Fatalf("clipboard in backward order = %q, want %q", got, "master\\nbeta\\nalpha")
	}
}

// Copy Branch Names of the batch menu never writes the names of branches a
// refresh dropped: only the survivors of the selection are copied.
func TestBranchBatchCopyNamesDropsVanishedBranches(t *testing.T) {
	repo := pickFixture(t, "batch-copy-stale")

	h := newTestApp(t)
	settings := h.store.Settings()
	settings.LocalBranchSort = ""
	if err := h.store.SetSettings(settings); err != nil {
		t.Fatalf("save alphabetical sort: %v", err)
	}
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectBranchesForBatch(t, h, "alpha", "beta", "master")
	repo.Git("branch", "-D", "beta")
	h.app.session.Reload()
	h.drain()

	chooseBatchItem(t, h, "alpha", "Copy Branch Names")
	if got := h.ui.Clipboard(); got != "alpha\nmaster" {
		t.Fatalf("clipboard = %q, want %q without the deleted beta", got, "alpha\\nmaster")
	}
}

// Deleting branches with the remote box ticked works on a loopback SSH
// repository: the local branches leave the server repository, and the
// upstreams leave the bare remote on the same server through the askpass
// sync path.
func TestBranchBatchLoopbackSSHDeleteWithRemote(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	bare := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repo.Path, "remote", "add", "origin", bare)
	for _, br := range []string{"rb1", "rb2", "keepme"} {
		srv.RunGit(t, repo.Path, "branch", br)
	}
	srv.RunGit(t, repo.Path, "push", "-q", "origin", "rb1:refs/heads/rb1", "rb2:refs/heads/rb2")
	for _, br := range []string{"rb1", "rb2"} {
		srv.RunGit(t, repo.Path, "branch", "--set-upstream-to", "origin/"+br, br)
	}
	srv.RunGit(t, repo.Path, "fetch", "-q", "origin")

	sshBranchExists := func(path, br string) bool {
		return strings.TrimSpace(srv.RunGit(t, path, "branch", "--list", br)) != ""
	}

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()

	for _, br := range []string{"rb1", "rb2"} {
		if _, ok := h.ui.Find(br); !ok {
			t.Fatalf("%s missing from the SSH sidebar; texts=%q", br, h.ui.Texts())
		}
	}
	selectBranchesForBatch(t, h, "rb1", "rb2")
	chooseBatchItem(t, h, "rb1", "Delete 2 Branches…")

	if !h.ui.HasText("→ origin/rb1") {
		t.Fatalf("upstream annotations missing on SSH; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Also delete corresponding remote branches"); err != nil {
		t.Fatalf("tick remote deletion: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Delete Branches"); err != nil {
		t.Fatalf("click Confirm Delete Branches: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH batch delete error: %v", err)
	}
	for _, br := range []string{"rb1", "rb2"} {
		if sshBranchExists(repo.Path, br) {
			t.Fatalf("%s survived the confirmed batch delete on the server", br)
		}
		if sshBranchExists(bare, br) {
			t.Fatalf("%s survived on the bare remote even though the remote box was ticked", br)
		}
	}
	if !sshBranchExists(repo.Path, "keepme") {
		t.Fatal("keepme is gone even though it was never selected")
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("server HEAD = %q, want main still checked out", got)
	}
}
