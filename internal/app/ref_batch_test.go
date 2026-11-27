package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// refBatchFixture builds a repository on refPickFixture with every branch
// pushed to origin and the tags rel-1, rel-2 and rel-3 existing locally
// only, returning the fixture and the two bare remote directories.
func refBatchFixture(t *testing.T, name string) (repo *gitRepoFixture, origin, upstream string) {
	t.Helper()
	repo, origin, upstream = refPickFixture(t, name)
	return repo, origin, upstream
}

// bareHasBranch reports whether the bare repository at dir has the branch.
func bareHasBranch(t *testing.T, dir, branch string) bool {
	t.Helper()
	return gitOutTrim(t, dir, "branch", "--list", branch) != ""
}

// bareHasTag reports whether the bare repository at dir has the tag.
func bareHasTag(t *testing.T, dir, tag string) bool {
	t.Helper()
	return gitOutTrim(t, dir, "tag", "--list", tag) != ""
}

// trackingRefExists reports whether refs/remotes/<full> exists locally.
func trackingRefExists(t *testing.T, repo *gitRepoFixture, full string) bool {
	t.Helper()
	_, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "refs/remotes/"+full)
	return err == nil
}

// selectRemoteBranchesForBatch selects the named remote branch rows through
// real clicks: a plain click on the first, Cmd-clicks on the rest.
func selectRemoteBranchesForBatch(t *testing.T, h *testHarness, names ...string) {
	t.Helper()
	for i, name := range names {
		if i == 0 {
			clickRefRow(t, h, name)
		} else {
			cmdClickRefRow(t, h, name)
		}
	}
	wantPickedRefs(t, h, nil, names, nil)
}

// selectTagsForBatch selects the named tag rows through real clicks.
func selectTagsForBatch(t *testing.T, h *testHarness, names ...string) {
	t.Helper()
	for i, name := range names {
		if i == 0 {
			clickRefRow(t, h, name)
		} else {
			cmdClickRefRow(t, h, name)
		}
	}
	wantPickedRefs(t, h, nil, nil, names)
}

// Right-clicking a remote branch row inside a multi-selection shows the
// batch menu — delete, then copy — while right-clicking a row outside it
// narrows the selection to that row and shows its own menu.
func TestRefBatchRemoteMenuChoosesBatchOrSingle(t *testing.T) {
	repo, _, _ := refBatchFixture(t, "rbatch-menu")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectRemoteBranchesForBatch(t, h, "origin/alpha", "origin/beta")
	if err := h.ui.RightClick("origin/beta"); err != nil {
		t.Fatalf("right-click selected row: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	want := []string{"Delete 2 Remote Branches…", "-", "Copy Branch Names"}
	if !slices.Equal(menu, want) {
		t.Fatalf("batch menu = %q, want exactly %q", menu, want)
	}
	h.ui.CloseMenu()
	h.ui.Frame()

	// Outside the selection: the selection narrows to the clicked row and
	// the menu is the single-branch one, ending in Copy Branch Name.
	if err := h.ui.RightClick("upstream/gamma"); err != nil {
		t.Fatalf("right-click unselected row: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu = h.ui.Menu()
	if len(menu) == 0 || menu[len(menu)-1] != "Copy Branch Name" {
		t.Fatalf("single remote branch menu %q does not end in Copy Branch Name", menu)
	}
	if containsString(menu, "Copy Branch Names") {
		t.Fatalf("batch item leaked into the single menu %q", menu)
	}
	wantPickedRefs(t, h, nil, []string{"upstream/gamma"}, nil)
	h.ui.CloseMenu()
	h.ui.Frame()
}

// Deleting two remote branches through the batch dialog removes them from
// the server repository and drops their local tracking references, while an
// unchecked row and the other remote's branches survive.
func TestRefBatchDeleteRemoteBranches(t *testing.T) {
	repo, origin, upstream := refBatchFixture(t, "rbatch-del-remote")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectRemoteBranchesForBatch(t, h, "origin/alpha", "origin/beta", "upstream/gamma")
	chooseBatchItem(t, h, "origin/alpha", "Delete 3 Remote Branches…")

	if !hasExactText(h.ui, "Delete Remote Branches") {
		t.Fatalf("Delete Remote Branches dialog did not open; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("The branches are deleted on the server for everyone.") {
		t.Fatalf("server warning missing; texts=%q", h.ui.Texts())
	}
	// Uncheck upstream/gamma: it survives the batch.
	if err := h.ui.Click("upstream/gamma"); err != nil {
		t.Fatalf("uncheck upstream/gamma row: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Delete Remote Branches"); err != nil {
		t.Fatalf("click Confirm Delete Remote Branches: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected batch remote delete error: %v", err)
	}
	for _, br := range []string{"alpha", "beta"} {
		if bareHasBranch(t, origin, br) {
			t.Fatalf("origin/%s survived the confirmed batch delete", br)
		}
		if trackingRefExists(t, repo, "origin/"+br) {
			t.Fatalf("the tracking reference origin/%s survived the delete", br)
		}
	}
	if !bareHasBranch(t, upstream, "gamma") {
		t.Fatal("upstream/gamma is gone even though it was unchecked")
	}
	if !trackingRefExists(t, repo, "upstream/gamma") {
		t.Fatal("the tracking reference upstream/gamma vanished with it")
	}
	if !bareHasBranch(t, origin, "main") {
		t.Fatal("origin/main is gone even though it was never selected")
	}
}

// A tracking reference moved with git update-ref after the dialog opened
// refuses the whole batch: nothing is deleted, not even the branches that
// never moved, and the error says the branch changed after the dialog
// opened.
func TestRefBatchDeleteRemoteGuardRefusesMovedRef(t *testing.T) {
	repo, origin, _ := refBatchFixture(t, "rbatch-guard")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectRemoteBranchesForBatch(t, h, "origin/alpha", "origin/beta")
	chooseBatchItem(t, h, "origin/alpha", "Delete 2 Remote Branches…")
	if !hasExactText(h.ui, "Delete Remote Branches") {
		t.Fatalf("Delete Remote Branches dialog did not open; texts=%q", h.ui.Texts())
	}

	// Move the tracking reference of origin/alpha onto a new commit after the
	// dialog captured it.
	before := gitOutTrim(t, repo.Path(), "rev-parse", "refs/remotes/origin/alpha")
	moved := repo.CommitFile("late.txt", "late work\n", "late external work")
	if moved == before {
		t.Fatal("fixture broken: the move target is the commit alpha already points at")
	}
	repo.Git("update-ref", "refs/remotes/origin/alpha", moved)

	if err := h.ui.Click("Confirm Delete Remote Branches"); err != nil {
		t.Fatalf("click Confirm Delete Remote Branches: %v", err)
	}
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("deleting over a moved tracking reference produced no error")
	}
	if !strings.Contains(err.Error(), "cannot delete remote branches: origin/alpha changed after the dialog opened") {
		t.Fatalf("ActionErr = %q, want the changed-after-the-dialog-opened refusal", err)
	}
	for _, br := range []string{"alpha", "beta"} {
		if !bareHasBranch(t, origin, br) {
			t.Fatalf("origin/%s was deleted even though the batch was refused", br)
		}
	}
	if !bareHasBranch(t, origin, "main") {
		t.Fatal("origin/main is gone even though the batch was refused")
	}
}

// A branch someone else moved on the server after the last fetch is never
// deleted: its lease fails while the other branch of the batch deletes, and
// the error surfaces in the interface.
func TestRefBatchDeleteRemoteLeaseKeepsServerMovedBranch(t *testing.T) {
	repo, origin, _ := refBatchFixture(t, "rbatch-lease")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectRemoteBranchesForBatch(t, h, "origin/alpha", "origin/beta")
	chooseBatchItem(t, h, "origin/alpha", "Delete 2 Remote Branches…")

	// From another clone, move alpha ahead on the server without the local
	// repository fetching it: the tracking reference still points at the
	// commit the dialog captured, but the server does not.
	peer := t.TempDir() + "/peer"
	runGitCommand(t, "", "clone", "-q", origin, peer)
	runGitCommand(t, peer, "-c", "user.name=Peer", "-c", "user.email=peer@test.local", "checkout", "-q", "-b", "peerwork", "origin/alpha")
	writeTestFile(t, peer, "peer.txt", "server move\n")
	runGitCommand(t, peer, "add", "peer.txt")
	runGitCommand(t, peer, "-c", "user.name=Peer", "-c", "user.email=peer@test.local", "commit", "-qm", "server move")
	runGitCommand(t, peer, "push", "-q", "origin", "peerwork:alpha")

	if err := h.ui.Click("Confirm Delete Remote Branches"); err != nil {
		t.Fatalf("click Confirm Delete Remote Branches: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("deleting over a server-moved branch produced no error")
	}
	if !bareHasBranch(t, origin, "alpha") {
		t.Fatal("alpha was deleted even though the server moved it past the lease")
	}
	if bareHasBranch(t, origin, "beta") {
		t.Fatal("beta survived even though nothing moved it: one failure never stops the batch")
	}
	if trackingRefExists(t, repo, "origin/beta") {
		t.Fatal("the tracking reference origin/beta survived the successful delete")
	}
}

// Canceling, dismissing and switching repositories away from the three batch
// dialogs runs no git command at all: the counting executor installed before
// the dialogs open records nothing, and every reference survives.
func TestRefBatchDialogCloseRunsNoGitCommands(t *testing.T) {
	repoA, origin, _ := refBatchFixture(t, "rbatch-close-a")
	repoB := newGitRepo(t, "rbatch-close-b")
	repoB.CommitFile("b.txt", "b\n", "b commit")

	h := newTestApp(t, repoB.StoreRepo())
	h.openRepo(repoA.StoreRepo())
	h.drain()

	counting := &countingExecutor{real: h.app.session.Repo().Exec}
	h.app.session.Repo().Exec = counting
	t.Cleanup(func() {
		h.queue.releaseAllAndWait()
		if repo := h.app.session.Repo(); repo != nil && repo.Exec == counting {
			repo.Exec = counting.real
		}
	})

	// Cancel on all three dialogs.
	selectRemoteBranchesForBatch(t, h, "origin/alpha", "origin/beta")
	chooseBatchItem(t, h, "origin/alpha", "Delete 2 Remote Branches…")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if counting.reads != 0 {
		t.Fatalf("canceling Delete Remote Branches ran %d git commands", counting.reads)
	}

	counting.reads = 0
	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Push 2 Tags to", "origin…")
	if !hasExactText(h.ui, "Push Tags") {
		t.Fatalf("Push Tags dialog did not open; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if counting.reads != 0 {
		t.Fatalf("canceling Push Tags ran %d git commands", counting.reads)
	}

	counting.reads = 0
	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Delete 2 Tags…")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if counting.reads != 0 {
		t.Fatalf("canceling Delete Tags ran %d git commands", counting.reads)
	}

	// Escape and the backdrop dismiss the dialogs the same way, each round
	// on a fresh dialog.
	openDel := func() {
		t.Helper()
		selectRemoteBranchesForBatch(t, h, "origin/alpha", "origin/beta")
		chooseBatchItem(t, h, "origin/alpha", "Delete 2 Remote Branches…")
		if !hasExactText(h.ui, "Delete Remote Branches") {
			t.Fatalf("Delete Remote Branches dialog did not reopen; texts=%q", h.ui.Texts())
		}
		counting.reads = 0
	}
	openDel()
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	if hasExactText(h.ui, "Delete Remote Branches") {
		t.Fatal("Escape did not dismiss Delete Remote Branches")
	}
	if counting.reads != 0 {
		t.Fatalf("Escape-dismissing Delete Remote Branches ran %d git commands", counting.reads)
	}

	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Push 2 Tags to", "origin…")
	counting.reads = 0
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	if hasExactText(h.ui, "Push Tags") {
		t.Fatal("Escape did not dismiss Push Tags")
	}
	if counting.reads != 0 {
		t.Fatalf("Escape-dismissing Push Tags ran %d git commands", counting.reads)
	}

	openDel()
	h.ui.ClickAt(20, 20)
	h.ui.Frame()
	if hasExactText(h.ui, "Delete Remote Branches") {
		t.Fatal("clicking the backdrop did not dismiss Delete Remote Branches")
	}
	if counting.reads != 0 {
		t.Fatalf("backdrop-dismissing Delete Remote Branches ran %d git commands", counting.reads)
	}

	// Switching repositories with a dialog open discards it: nothing is
	// pushed or deleted in the repository the dialog belonged to.
	counting.reads = 0
	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Delete 2 Tags…")
	if !hasExactText(h.ui, "Delete Tags") {
		t.Fatalf("Delete Tags dialog did not open; texts=%q", h.ui.Texts())
	}
	counting.reads = 0
	h.openRepo(repoB.StoreRepo())
	h.drain()
	if hasExactText(h.ui, "Delete Tags") {
		t.Fatal("the Delete Tags dialog survived a repository switch")
	}
	if counting.reads != 0 {
		t.Fatalf("switching repositories over the open dialog ran %d git commands on it", counting.reads)
	}

	for _, br := range []string{"alpha", "beta"} {
		if !bareHasBranch(t, origin, br) {
			t.Fatalf("origin/%s is gone even though its dialog only ever closed", br)
		}
	}
	for _, tag := range []string{"rel-1", "rel-2", "rel-3"} {
		if !tagRefExists(t, repoA, tag) {
			t.Fatalf("%s is gone even though its dialog only ever closed", tag)
		}
		if bareHasTag(t, origin, tag) {
			t.Fatalf("%s reached the server even though Push Tags only ever closed", tag)
		}
	}
}

// Pushing two tags through the batch dialog lands exactly them in the bare
// remote — the unchecked tag stays local — and the toast counts the pushed
// tags and names the remote.
func TestRefBatchPushTags(t *testing.T) {
	repo, origin, upstream := refBatchFixture(t, "rbatch-push")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectTagsForBatch(t, h, "rel-1", "rel-2", "rel-3")
	chooseBatchItem(t, h, "rel-1", "Push 3 Tags to", "origin…")

	if !hasExactText(h.ui, "Push Tags") {
		t.Fatalf("Push Tags dialog did not open; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Tags that already exist on the remote with a different commit are rejected.") {
		t.Fatalf("rejection note missing; texts=%q", h.ui.Texts())
	}
	// The Remote line names the remote the menu chose.
	if !hasExactText(h.ui, "origin") {
		t.Fatalf("the Remote line does not name origin; texts=%q", h.ui.Texts())
	}
	// Uncheck rel-3: it stays local.
	if err := h.ui.Click("rel-3"); err != nil {
		t.Fatalf("uncheck rel-3 row: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Push Tags"); err != nil {
		t.Fatalf("click Confirm Push Tags: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected batch push error: %v", err)
	}
	if !h.ui.HasText("Pushed 2 tags to origin.") {
		t.Fatalf("push toast missing; texts=%q", h.ui.Texts())
	}
	for _, tag := range []string{"rel-1", "rel-2"} {
		if !bareHasTag(t, origin, tag) {
			t.Fatalf("%s did not reach origin", tag)
		}
	}
	if bareHasTag(t, origin, "rel-3") {
		t.Fatal("rel-3 reached origin even though it was unchecked")
	}
	if bareHasTag(t, upstream, "rel-1") {
		t.Fatal("rel-1 reached upstream even though the push went to origin")
	}
}

// A tag sharing its name with a branch pushes as the tag: the full
// refs/tags reference never resolves to the branch, so the bare remote gains
// refs/tags/release/1.0 and not a second branch.
func TestRefBatchPushTagSharingBranchName(t *testing.T) {
	repo, origin, _ := refBatchFixture(t, "rbatch-push-samename")
	repo.CreateTag("release/1.0")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Select the tags before the same-named branch exists: the rows stay
	// clickable by their plain names, and the push captured is the tags'.
	selectTagsForBatch(t, h, "rel-1", "release/1.0")

	// The branch appears only now, after the selection.
	repo.Git("branch", "release/1.0")
	wantPickedRefs(t, h, nil, nil, []string{"rel-1", "release/1.0"})

	chooseBatchItem(t, h, "rel-1", "Push 2 Tags to", "origin…")
	if err := h.ui.Click("Confirm Push Tags"); err != nil {
		t.Fatalf("click Confirm Push Tags: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected batch push error: %v", err)
	}
	if got := gitOutTrim(t, origin, "for-each-ref", "--format=%(refname)", "refs/tags/release/1.0"); got != "refs/tags/release/1.0" {
		t.Fatalf("bare remote refs = %q for refs/tags/release/1.0, want exactly the tag", got)
	}
	if got := gitOutTrim(t, origin, "branch", "--list", "release/1.0"); got != "" {
		t.Fatalf("the bare remote gained branch release/1.0 (%q): the push used the short name", got)
	}
	// origin/alpha is a different branch and proves branches list at all.
	if !bareHasBranch(t, origin, "alpha") {
		t.Fatal("fixture broken: origin never had alpha")
	}
}

// With two remotes the push nests in a submenu, and choosing the second
// remote pushes there and nowhere else.
func TestRefBatchPushTagsSubmenuSecondRemote(t *testing.T) {
	repo, origin, upstream := refBatchFixture(t, "rbatch-push-second")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectTagsForBatch(t, h, "rel-1", "rel-2")
	if err := h.ui.RightClick("rel-1"); err != nil {
		t.Fatalf("right-click selected tag: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	if !containsString(menu, "Push 2 Tags to") {
		t.Fatalf("batch tag menu %q misses the Push 2 Tags to submenu", menu)
	}
	if err := h.ui.ChooseMenuItem("Push 2 Tags to", "upstream…"); err != nil {
		t.Fatalf("choose upstream… in the submenu: %v; menu=%q", err, menu)
	}
	h.ui.Frame()

	if !hasExactText(h.ui, "Push Tags") {
		t.Fatalf("Push Tags dialog did not open; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "upstream") {
		t.Fatalf("the Remote line does not name upstream; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Push Tags"); err != nil {
		t.Fatalf("click Confirm Push Tags: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected batch push error: %v", err)
	}
	if !h.ui.HasText("Pushed 2 tags to upstream.") {
		t.Fatalf("push toast missing; texts=%q", h.ui.Texts())
	}
	if !bareHasTag(t, upstream, "rel-1") || !bareHasTag(t, upstream, "rel-2") {
		t.Fatal("the tags did not reach upstream")
	}
	if bareHasTag(t, origin, "rel-1") {
		t.Fatal("rel-1 reached origin even though the push went to upstream")
	}
}

// A tag moved with git tag -f after the dialog opened refuses the whole
// batch: nothing is pushed, not even the tags that never moved, and the
// error says the tag changed after the dialog opened.
func TestRefBatchPushTagsGuardRefusesRetargeted(t *testing.T) {
	repo, origin, _ := refBatchFixture(t, "rbatch-push-guard")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Push 2 Tags to", "origin…")

	// A new commit, then rel-1 retargeted onto it behind the dialog's back.
	repo.CommitFile("move.txt", "moved\n", "retarget commit")
	repo.Git("tag", "-f", "rel-1", "HEAD")

	if err := h.ui.Click("Confirm Push Tags"); err != nil {
		t.Fatalf("click Confirm Push Tags: %v", err)
	}
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("pushing over a retargeted tag produced no error")
	}
	if !strings.Contains(err.Error(), "cannot push tags: rel-1 changed after the dialog opened") {
		t.Fatalf("ActionErr = %q, want the changed-after-the-dialog-opened refusal", err)
	}
	for _, tag := range []string{"rel-1", "rel-2"} {
		if bareHasTag(t, origin, tag) {
			t.Fatalf("%s reached the server even though the batch was refused", tag)
		}
	}
}

// Deleting tags through the batch dialog removes them locally; with the
// remote box unticked the server keeps its copies, and ticked the deletion
// reaches the server too. Either way the selection clears afterwards.
func TestRefBatchDeleteTags(t *testing.T) {
	repo, origin, _ := refBatchFixture(t, "rbatch-del-tags")
	// The server starts with rel-1 and rel-2 so the remote deletion has
	// something to remove.
	repo.Git("push", "-q", "origin", "refs/tags/rel-1", "refs/tags/rel-2")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Round one, unticked: rel-1 leaves the repository but not the server.
	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Delete 2 Tags…")
	if !hasExactText(h.ui, "Delete Tags") {
		t.Fatalf("Delete Tags dialog did not open; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Also delete from remotes"); !ok {
		t.Fatalf("remote deletion checkbox missing; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("for everyone") {
		t.Fatalf("server warning shown before the box is ticked; texts=%q", h.ui.Texts())
	}
	// Uncheck rel-2: it survives the round entirely.
	if err := h.ui.Click("rel-2"); err != nil {
		t.Fatalf("uncheck rel-2 row: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Delete Tags"); err != nil {
		t.Fatalf("click Confirm Delete Tags: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected batch tag delete error: %v", err)
	}
	if tagRefExists(t, repo, "rel-1") {
		t.Fatal("rel-1 survived the confirmed batch delete")
	}
	if !bareHasTag(t, origin, "rel-1") {
		t.Fatal("rel-1 left the server even though the remote box was unticked")
	}
	if !tagRefExists(t, repo, "rel-2") {
		t.Fatal("rel-2 is gone even though it was unchecked")
	}
	// The delete cleared the selection: nothing of it survives.
	if len(h.app.refsView.pick) != 0 {
		t.Fatalf("selection = %v after the delete, want empty", h.app.refsView.pick)
	}
	if got := h.app.refsView.pickAnchor; got.name != "" {
		t.Fatalf("pick anchor survived the delete: %+v", got)
	}

	// Round two, ticked: rel-2 leaves the repository and the server.
	selectTagsForBatch(t, h, "rel-2", "rel-3")
	chooseBatchItem(t, h, "rel-2", "Delete 2 Tags…")
	if err := h.ui.Click("Also delete from remotes"); err != nil {
		t.Fatalf("tick remote deletion: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if !h.ui.HasText("The tags are deleted on origin, upstream for everyone. Remotes without them are skipped.") {
		t.Fatalf("server warning missing after ticking; texts=%q", h.ui.Texts())
	}
	// rel-3 was never pushed: the server skip must not fail the batch.
	if err := h.ui.Click("Confirm Delete Tags"); err != nil {
		t.Fatalf("click Confirm Delete Tags: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected batch tag delete with remote error: %v", err)
	}
	if tagRefExists(t, repo, "rel-2") {
		t.Fatal("rel-2 survived the confirmed batch delete")
	}
	if bareHasTag(t, origin, "rel-2") {
		t.Fatal("rel-2 survived on the server even though the remote box was ticked")
	}
	if tagRefExists(t, repo, "rel-3") {
		t.Fatal("rel-3 survived the confirmed batch delete")
	}
	// The first round's server copy is gone only because round two deleted
	// tags present there; rel-1 was already deleted locally, so its server
	// copy stays: the batch never touches unchecked or absent tags.
	if !bareHasTag(t, origin, "rel-1") {
		t.Fatal("rel-1 left the server even though it was not part of the second batch")
	}
}

// Copy Branch Names of the remote batch menu writes the selected branches
// one per line in the order the sidebar shows them — remote by remote, each
// remote's branches sorted — never in the order the clicks happened.
func TestRefBatchCopyRemoteBranchNamesFollowsDisplayOrder(t *testing.T) {
	repo, _, _ := refBatchFixture(t, "rbatch-copy-remote")

	h := newTestApp(t)
	settings := h.store.Settings()
	settings.RemoteBranchSort = ""
	if err := h.store.SetSettings(settings); err != nil {
		t.Fatalf("save alphabetical remote sort: %v", err)
	}
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Display order: origin/alpha, origin/beta, origin/main, then
	// upstream/delta, upstream/gamma, upstream/master. Select out of order
	// on purpose: the copy order is the display order.
	selectRemoteBranchesForBatch(t, h, "upstream/gamma", "origin/beta", "origin/alpha")
	chooseBatchItem(t, h, "origin/beta", "Copy Branch Names")
	if got := h.ui.Clipboard(); got != "origin/alpha\norigin/beta\nupstream/gamma" {
		t.Fatalf("clipboard = %q, want %q", got, "origin/alpha\\norigin/beta\\nupstream/gamma")
	}
}

// Copy Tag Names of the tag batch menu writes the selected tags one per line
// in the order the sidebar shows them, which follows the sort order.
func TestRefBatchCopyTagNamesFollowsDisplayOrder(t *testing.T) {
	repo, _, _ := refBatchFixture(t, "rbatch-copy-tags")

	h := newTestApp(t)
	settings := h.store.Settings()
	settings.TagSort = ""
	if err := h.store.SetSettings(settings); err != nil {
		t.Fatalf("save alphabetical tag sort: %v", err)
	}
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Alphabetical display: rel-1, rel-2, rel-3. Select out of order on
	// purpose: the copy order is the display order, not the click order.
	selectTagsForBatch(t, h, "rel-3", "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Copy Tag Names")
	if got := h.ui.Clipboard(); got != "rel-1\nrel-2\nrel-3" {
		t.Fatalf("clipboard = %q, want %q", got, "rel-1\\nrel-2\\nrel-3")
	}

	// Backward sorting flips the display, and the copy follows it: the same
	// selection, which survived the reorder, copies in reverse.
	settings.TagSort = refSortBackward
	if err := h.store.SetSettings(settings); err != nil {
		t.Fatalf("save backward tag sort: %v", err)
	}
	h.ui.Frame()
	wantPickedRefs(t, h, nil, nil, []string{"rel-1", "rel-2", "rel-3"})
	chooseBatchItem(t, h, "rel-1", "Copy Tag Names")
	if got := h.ui.Clipboard(); got != "rel-3\nrel-2\nrel-1" {
		t.Fatalf("clipboard in backward order = %q, want %q", got, "rel-3\\nrel-2\\nrel-1")
	}
}

// Copy Branch Names never writes the names of rows a refresh dropped: only
// the survivors of the selection are copied.
func TestRefBatchCopyDropsVanishedRows(t *testing.T) {
	repo, origin, _ := refBatchFixture(t, "rbatch-copy-stale")

	h := newTestApp(t)
	settings := h.store.Settings()
	settings.RemoteBranchSort = ""
	settings.TagSort = ""
	if err := h.store.SetSettings(settings); err != nil {
		t.Fatalf("save alphabetical sorts: %v", err)
	}
	h.openRepo(repo.StoreRepo())
	h.drain()

	// origin/beta vanishes from the server and the local tracking references
	// after the selection: the copy drops it.
	selectRemoteBranchesForBatch(t, h, "origin/alpha", "origin/beta", "origin/main")
	runGitCommand(t, origin, "branch", "-D", "beta")
	repo.Git("update-ref", "-d", "refs/remotes/origin/beta")
	h.app.session.Reload()
	h.drain()

	chooseBatchItem(t, h, "origin/alpha", "Copy Branch Names")
	if got := h.ui.Clipboard(); got != "origin/alpha\norigin/main" {
		t.Fatalf("clipboard = %q, want %q without the deleted origin/beta", got, "origin/alpha\\norigin/main")
	}

	// The same holds for a vanished tag.
	selectTagsForBatch(t, h, "rel-1", "rel-2", "rel-3")
	repo.Git("tag", "-d", "rel-2")
	h.app.session.Reload()
	h.drain()

	chooseBatchItem(t, h, "rel-1", "Copy Tag Names")
	if got := h.ui.Clipboard(); got != "rel-1\nrel-3" {
		t.Fatalf("clipboard = %q, want %q without the deleted rel-2", got, "rel-1\\nrel-3")
	}
}

// A batch remote branch deletion and a batch tag push work on a loopback SSH
// repository through the askpass sync path: the branches leave the server's
// bare remote and the tags reach it.
func TestRefBatchLoopbackSSHDeleteAndPush(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	bare := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repo.Path, "remote", "add", "origin", bare)
	for _, br := range []string{"alpha", "beta", "keepme"} {
		srv.RunGit(t, repo.Path, "branch", br)
	}
	srv.RunGit(t, repo.Path, "push", "-q", "origin", "alpha:refs/heads/alpha", "beta:refs/heads/beta", "keepme:refs/heads/keepme")
	srv.RunGit(t, repo.Path, "fetch", "-q", "origin")
	for _, tag := range []string{"ssh-rel-1", "ssh-rel-2"} {
		srv.RunGit(t, repo.Path, "tag", tag)
	}

	sshBareHasBranch := func(br string) bool {
		return strings.TrimSpace(srv.RunGit(t, bare, "branch", "--list", br)) != ""
	}
	sshBareHasTag := func(tag string) bool {
		return strings.TrimSpace(srv.RunGit(t, bare, "tag", "--list", tag)) != ""
	}

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	for _, want := range []string{"origin/alpha", "origin/beta", "ssh-rel-1", "ssh-rel-2"} {
		if _, ok := h.ui.Find(want); !ok {
			t.Fatalf("%s missing from the SSH sidebar; texts=%q", want, h.ui.Texts())
		}
	}

	// The batch delete over SSH: both branches leave the bare remote and
	// their tracking references drop locally.
	selectRemoteBranchesForBatch(t, h, "origin/alpha", "origin/beta")
	chooseBatchItem(t, h, "origin/alpha", "Delete 2 Remote Branches…")
	if !hasExactText(h.ui, "Delete Remote Branches") {
		t.Fatalf("Delete Remote Branches dialog did not open on SSH; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Delete Remote Branches"); err != nil {
		t.Fatalf("click Confirm Delete Remote Branches: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH batch remote delete error: %v", err)
	}
	for _, br := range []string{"alpha", "beta"} {
		if sshBareHasBranch(br) {
			t.Fatalf("%s survived the confirmed batch delete on the server", br)
		}
		if srv.RunGit(t, repo.Path, "for-each-ref", "--format=%(refname)", "refs/remotes/origin/"+br) != "" {
			t.Fatalf("the tracking reference origin/%s survived the delete on the server", br)
		}
	}
	if !sshBareHasBranch("keepme") {
		t.Fatal("keepme is gone even though it was never selected")
	}

	// The batch push over SSH: both tags reach the bare remote.
	selectTagsForBatch(t, h, "ssh-rel-1", "ssh-rel-2")
	chooseBatchItem(t, h, "ssh-rel-1", "Push 2 Tags to 'origin'…")
	if !hasExactText(h.ui, "Push Tags") {
		t.Fatalf("Push Tags dialog did not open on SSH; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Push Tags"); err != nil {
		t.Fatalf("click Confirm Push Tags: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH batch push error: %v", err)
	}
	if !h.ui.HasText("Pushed 2 tags to origin.") {
		t.Fatalf("push toast missing on SSH; texts=%q", h.ui.Texts())
	}
	for _, tag := range []string{"ssh-rel-1", "ssh-rel-2"} {
		if !sshBareHasTag(tag) {
			t.Fatalf("%s did not reach the server", tag)
		}
	}
}

// A tag moved with git tag -f after the Delete Tags dialog opened refuses
// the whole batch: nothing is deleted, not even the tags that never moved,
// and the error says the tag changed after the dialog opened.
func TestRefBatchDeleteTagsGuardRefusesRetargeted(t *testing.T) {
	repo, origin, _ := refBatchFixture(t, "rbatch-del-guard")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Delete 2 Tags…")
	if !hasExactText(h.ui, "Delete Tags") {
		t.Fatalf("Delete Tags dialog did not open; texts=%q", h.ui.Texts())
	}

	// rel-1 retargets onto a new commit behind the dialog's back.
	repo.CommitFile("move.txt", "moved\n", "retarget commit")
	repo.Git("tag", "-f", "rel-1", "HEAD")

	if err := h.ui.Click("Confirm Delete Tags"); err != nil {
		t.Fatalf("click Confirm Delete Tags: %v", err)
	}
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("deleting over a retargeted tag produced no error")
	}
	if !strings.Contains(err.Error(), "cannot delete tags: rel-1 changed after the dialog opened") {
		t.Fatalf("ActionErr = %q, want the changed-after-the-dialog-opened refusal", err)
	}
	for _, tag := range []string{"rel-1", "rel-2"} {
		if !tagRefExists(t, repo, tag) {
			t.Fatalf("%s was deleted even though the batch was refused", tag)
		}
		if bareHasTag(t, origin, tag) {
			t.Fatalf("%s reached the server even though the batch was refused", tag)
		}
	}
}
