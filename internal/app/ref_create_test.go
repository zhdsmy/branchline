package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// remoteAheadFixture builds a repository whose remote-tracking branch
// origin/main is one commit ahead of the local main: the remote work was
// pushed and the local branch rewound, exactly as a fetch of upstream work
// the local clone never merged looks. It returns the fixture, the base
// commit HEAD stands on and the commit origin/main points at.
func remoteAheadFixture(t *testing.T, name string) (*gitRepoFixture, string, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")
	base := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	remoteTip := repo.CommitFile("remote.txt", "remote\n", "remote work")
	repo.Git("push", "-q", "origin", "main")
	repo.Git("reset", "--hard", "-q", base)
	if got := strings.TrimSpace(repo.Git("rev-parse", "refs/remotes/origin/main")); got != remoteTip {
		t.Fatalf("fixture: origin/main = %q, want the pushed commit %q", got, remoteTip)
	}
	return repo, base, remoteTip
}

// remoteBehindFixture builds a repository whose origin/main is a strict
// ancestor of the local main: the remote tracks the pushed initial commit
// and local main advanced with one commit per subject. It returns the
// fixture, the remote tip and the local commit hashes, oldest first.
func remoteBehindFixture(t *testing.T, name string, subjects ...string) (*gitRepoFixture, string, []string) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")
	remoteTip := strings.TrimSpace(repo.Git("rev-parse", "refs/remotes/origin/main"))
	hashes := irLinearFixture(t, repo, subjects...)
	return repo, remoteTip, hashes
}

// tagAncestorFixture builds a repository with two commits on main and the
// lightweight tag lw and the annotated tag ann on the first one, so both
// tags name a strict ancestor of HEAD. It returns the fixture and the two
// commit hashes, oldest first.
func tagAncestorFixture(t *testing.T, name string) (*gitRepoFixture, string, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	first := repo.CommitFile("a.txt", "one\n", "first commit")
	second := repo.CommitFile("b.txt", "two\n", "second commit")
	repo.Git("tag", "lw", first)
	repo.Git("tag", "-a", "ann", "-m", "annotated release", first)
	return repo, first, second
}

// chooseMenuItemFromRow right-clicks the sidebar row labeled row and chooses
// item from its context menu, then renders the result.
func chooseMenuItemFromRow(t *testing.T, h *testHarness, row, item string) {
	t.Helper()
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click row %q: %v\ntexts=%q", row, err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem(item); err != nil {
		t.Fatalf("choose %q: %v\nmenu=%q", item, err, h.ui.Menu())
	}
	h.ui.Frame()
}

// openNewBranchFromRow opens the New Branch dialog of the sidebar row
// through its real context menu and verifies it renders.
func openNewBranchFromRow(t *testing.T, h *testHarness, row string) {
	t.Helper()
	chooseMenuItemFromRow(t, h, row, "New Branch…")
	if !hasExactText(h.ui, "Start point:") {
		t.Fatalf("New Branch dialog did not open; texts=%q", h.ui.Texts())
	}
}

// confirmBranchCreate clicks the New Branch dialog's create button and
// drains the write it starts.
func confirmBranchCreate(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Confirm New Branch From"); err != nil {
		t.Fatalf("click Confirm New Branch From: %v", err)
	}
	h.drain()
}

// assertNoUpstream proves the branch carries no upstream configuration: a
// branch created at an exact commit must never track the remote branch it
// started from.
func assertNoUpstream(t *testing.T, repo *gitRepoFixture, name string) {
	t.Helper()
	if out, err := tryGitCommand(repo.Path(), "config", "--get", "branch."+name+".remote"); err == nil {
		t.Fatalf("branch %s carries an upstream remote %q, want none", name, out)
	}
	if out, err := tryGitCommand(repo.Path(), "config", "--get", "branch."+name+".merge"); err == nil {
		t.Fatalf("branch %s carries an upstream merge ref %q, want none", name, out)
	}
}

// holdBusyWrite pauses the workers and stages a new file so the session
// stays busy on a real write until the returned worker is released.
func holdBusyWrite(t *testing.T, h *testHarness, repo *gitRepoFixture) *gatedWorker {
	t.Helper()
	repo.WriteFile("pending.txt", "pending\n")
	h.app.session.Reload()
	h.drain()
	h.queue.PauseWorkers()
	if err := h.ui.Click("Stage pending.txt"); err != nil {
		t.Fatalf("click Stage pending.txt: %v", err)
	}
	write := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the stage worker is held")
	}
	return write
}

// assertRowItemsDisabled right-clicks the sidebar row and verifies every
// item is present in the menu but disabled, then closes the menu.
func assertRowItemsDisabled(t *testing.T, h *testHarness, row string, items []string) {
	t.Helper()
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click row %q: %v\ntexts=%q", row, err, h.ui.Texts())
	}
	for _, item := range items {
		if err := h.ui.ChooseMenuItem(item); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing %q of row %q = %v, want a disabled error", item, row, err)
		}
	}
	h.ui.CloseMenu()
	h.ui.Frame()
}

// --- menu items and order ---

// The context menu of a remote-tracking branch row gains the interactive
// rebase directly behind the plain rebase, then a separator and the New
// Branch and New Tag entries, exactly like the local branch menu.
func TestRefCreateRemoteMenuOrder(t *testing.T) {
	repo, _, _ := remoteAheadFixture(t, "rc-remote-menu")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/main"); err != nil {
		t.Fatalf("right-click origin/main: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	ir := slices.Index(menu, "Interactively Rebase on 'origin/main'…")
	nb := slices.Index(menu, "New Branch…")
	nt := slices.Index(menu, "New Tag…")
	if ir < 0 || nb < 0 || nt < 0 {
		t.Fatalf("menu %q missing the create items", menu)
	}
	sepOffset := slices.Index(menu[ir+1:], "-")
	if sepOffset < 0 {
		t.Fatalf("menu %q missing separator after interactive rebase", menu)
	}
	sep := ir + 1 + sepOffset
	if !(ir < sep && sep < nb && nb < nt) {
		t.Fatalf("menu order %q: want interactive rebase, separator, New Branch, New Tag", menu)
	}
	rebase := slices.Index(menu, "Rebase 'main' on 'origin/main'…")
	if rebase < 0 || rebase+1 != ir {
		t.Fatalf("menu %q: interactive rebase must directly follow the plain rebase item", menu)
	}
	h.ui.CloseMenu()
}

// The context menu of a tag row inserts the interactive rebase and the
// create entries between Push and Delete, each group separated once.
func TestRefCreateTagMenuOrder(t *testing.T) {
	repo, _, _ := tagAncestorFixture(t, "rc-tag-menu")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("lw"); err != nil {
		t.Fatalf("right-click lw: %v\ntexts=%q", err, h.ui.Texts())
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
	if !slices.Equal(menu, want) {
		t.Fatalf("tag menu = %q, want exactly %q", menu, want)
	}
	h.ui.CloseMenu()
}

// --- New Branch… and New Tag… from a remote branch ---

// New Branch on a remote-tracking branch names the branch and its commit as
// the start point and creates the branch at exactly that commit, checking it
// out, without an upstream: tracking the remote branch is a separate action.
func TestRefCreateNewBranchFromRemoteAhead(t *testing.T) {
	repo, base, remoteTip := remoteAheadFixture(t, "rc-remote-nb")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openNewBranchFromRow(t, h, "origin/main")
	if !h.ui.HasText("origin/main (" + short7(remoteTip) + ")") {
		t.Fatalf("start point hint missing; texts=%q", h.ui.Texts())
	}
	typeBranchCreateName(t, h, "release-x")
	if btn, ok := confirmBranchCreateButton(t, h); !ok || btn.Disabled {
		t.Fatalf("Create Branch with valid name = %+v, want enabled", btn)
	}
	confirmBranchCreate(t, h)

	if !branchExists(t, repo, "release-x") {
		t.Fatal("release-x was not created in the repository")
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "release-x")); got != remoteTip {
		t.Fatalf("release-x points at %q, want the remote commit %q", got, remoteTip)
	}
	if got := currentBranch(t, repo); got != "release-x" {
		t.Fatalf("current branch after create = %q, want release-x (checkout was ticked)", got)
	}
	assertNoUpstream(t, repo, "release-x")
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "main")); got != base {
		t.Fatalf("main moved to %q by the create, want its base %q", got, base)
	}
	if hasExactText(h.ui, "Start point:") {
		t.Fatal("New Branch dialog stayed open after successful create")
	}
}

// New Tag on a remote-tracking branch opens the tag dialog with the target
// pre-filled to the branch's commit and the branch hint below it; the tag
// lands on exactly that commit, not on HEAD.
func TestRefCreateNewTagFromRemoteAhead(t *testing.T) {
	repo, base, remoteTip := remoteAheadFixture(t, "rc-remote-nt")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseMenuItemFromRow(t, h, "origin/main", "New Tag…")
	if got := h.app.tagsUI().newTarget; got != remoteTip {
		t.Fatalf("target prefill = %q, want the remote commit %q", got, remoteTip)
	}
	if !h.ui.HasText("origin/main (" + short7(remoteTip) + ")") {
		t.Fatalf("branch hint not shown; texts=%q", h.ui.Texts())
	}
	h.ui.Type("remote-tag")
	h.ui.Frame()
	if err := h.ui.Click("Create Tag"); err != nil {
		t.Fatalf("click Create Tag: %v", err)
	}
	h.drain()

	if !tagRefExists(t, repo, "remote-tag") {
		t.Fatal("remote-tag was not created")
	}
	if got := tagTypeOf(t, repo, "remote-tag"); got != "commit" {
		t.Fatalf("remote-tag object type = %q, want commit (lightweight)", got)
	}
	if got := tagPointsAt(t, repo, "remote-tag"); got != remoteTip {
		t.Fatalf("remote-tag target = %q, want the remote commit %q", got, remoteTip)
	}
	if got := tagPointsAt(t, repo, "remote-tag"); got == base {
		t.Fatal("remote-tag landed on HEAD instead of the remote commit")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected tag error: %v", err)
	}
}

// --- interactive rebase onto a remote branch ---

// Interactively Rebase on a remote-tracking branch whose commit is an
// ancestor of HEAD opens the form with the branch name as its base label,
// and a drop replays on the real repository exactly as the plan showed.
func TestRefCreateInteractiveRebaseOntoRemote(t *testing.T) {
	repo, remoteTip, hashes := remoteBehindFixture(t, "rc-remote-ir", "ir one", "ir two")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseMenuItemFromRow(t, h, "origin/main", "Interactively Rebase on 'origin/main'…")
	if !hasExactText(h.ui, "Interactive Rebase") {
		t.Fatalf("interactive rebase form did not open; texts=%q", h.ui.Texts())
	}
	drainIRPlan(t, h)

	// The base is the remote branch's exact commit, labeled with its name.
	if !h.ui.HasText(short7(remoteTip) + " origin/main") {
		t.Fatalf("onto label missing; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RolePopUpButton, "Action "+irShort(hashes[0])); !ok {
		t.Fatalf("plan rows missing; texts=%q", h.ui.Texts())
	}

	chooseIRAction(t, h, hashes[0], "Pick", "Drop")
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("drop rebase onto the remote branch failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"ir two", "initial commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after drop = %v, want %v", subjects, want)
	}
	if out, err := tryGitCommand(repo.Path(), "cat-file", "-e", "HEAD:ir1.txt"); err == nil {
		t.Fatalf("ir1.txt of the dropped commit survived: %s", out)
	}
	// The new tip sits directly on the remote branch's commit.
	if got := strings.TrimSpace(repo.Git("rev-parse", "main~1")); got != remoteTip {
		t.Fatalf("parent of the replayed tip = %q, want the remote commit %q", got, remoteTip)
	}
	assertIRDialogClosed(t, h)
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after the rebase = %q, want empty", got)
	}
}

// A remote branch ahead of HEAD is no ancestor of it: the form shows the
// honest refusal of the plan read, the Rebase button stays disabled, and
// canceling leaves the repository exactly as before.
func TestRefCreateInteractiveRebaseOntoRemoteDiverged(t *testing.T) {
	repo, base, _ := remoteAheadFixture(t, "rc-remote-ir-div")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseMenuItemFromRow(t, h, "origin/main", "Interactively Rebase on 'origin/main'…")
	if !hasExactText(h.ui, "Interactive Rebase") {
		t.Fatalf("interactive rebase form did not open; texts=%q", h.ui.Texts())
	}
	drainIRPlan(t, h)

	if !h.ui.HasText("not an ancestor") {
		t.Fatalf("non-ancestor refusal missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || !btn.Disabled {
		t.Fatalf("Rebase with a diverged base = %+v found=%v, want present and disabled", btn, ok)
	}

	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	assertIRRepoUnchanged(t, repo, base, "main", "1")
}

// --- New Branch… and New Tag… from tags ---

// New Branch on a lightweight and on an annotated tag both create at the
// commit the tag names — never at the tag object — and the annotated tag's
// start point names the tag with its commit.
func TestRefCreateNewBranchFromTags(t *testing.T) {
	repo, first, second := tagAncestorFixture(t, "rc-tag-nb")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Lightweight tag: the branch is created at the tagged commit.
	openNewBranchFromRow(t, h, "lw")
	if !h.ui.HasText("lw (" + short7(first) + ")") {
		t.Fatalf("lightweight start point hint missing; texts=%q", h.ui.Texts())
	}
	typeBranchCreateName(t, h, "from-lw")
	confirmBranchCreate(t, h)
	if got := strings.TrimSpace(repo.Git("rev-parse", "from-lw")); got != first {
		t.Fatalf("from-lw points at %q, want the tagged commit %q", got, first)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	assertNoUpstream(t, repo, "from-lw")

	// Back on main, the annotated tag names the very same commit: the branch
	// is created there, and never at the tag object itself.
	repo.Checkout("main")
	h.app.session.Reload()
	h.drain()
	tagObject := strings.TrimSpace(repo.Git("rev-parse", "refs/tags/ann"))
	if tagObject == first {
		t.Fatal("fixture: ann is not annotated, ref and commit coincide")
	}
	openNewBranchFromRow(t, h, "ann")
	if !h.ui.HasText("ann (" + short7(first) + ")") {
		t.Fatalf("annotated start point hint missing; texts=%q", h.ui.Texts())
	}
	typeBranchCreateName(t, h, "from-ann")
	confirmBranchCreate(t, h)
	if got := strings.TrimSpace(repo.Git("rev-parse", "from-ann")); got != first {
		t.Fatalf("from-ann points at %q, want the tagged commit %q", got, first)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "from-ann")); got == tagObject {
		t.Fatal("from-ann was created at the tag object instead of the tagged commit")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "main")); got != second {
		t.Fatalf("main moved to %q by the creates, want %q", got, second)
	}
}

// New Tag on a tag pre-fills the target with the commit the tag names — the
// annotated tag's commit, not its tag object — shows the tag hint, and the
// new tag lands on exactly that commit.
func TestRefCreateNewTagFromTags(t *testing.T) {
	repo, first, _ := tagAncestorFixture(t, "rc-tag-nt")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	for _, source := range []string{"lw", "ann"} {
		chooseMenuItemFromRow(t, h, source, "New Tag…")
		if got := h.app.tagsUI().newTarget; got != first {
			t.Fatalf("%s target prefill = %q, want the tagged commit %q", source, got, first)
		}
		if !h.ui.HasText(source + " (" + short7(first) + ")") {
			t.Fatalf("%s hint not shown; texts=%q", source, h.ui.Texts())
		}
		tagObject := strings.TrimSpace(repo.Git("rev-parse", "refs/tags/"+source))
		if got := h.app.tagsUI().newTarget; source == "ann" && got == tagObject {
			t.Fatalf("annotated prefill = %q is the tag object, want the tagged commit", got)
		}
		h.ui.Type("tagged-" + source)
		h.ui.Frame()
		if err := h.ui.Click("Create Tag"); err != nil {
			t.Fatalf("click Create Tag: %v", err)
		}
		h.drain()
		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("unexpected tag error for %s: %v", source, err)
		}
		if got := tagPointsAt(t, repo, "tagged-"+source); got != first {
			t.Fatalf("tagged-%s target = %q, want the tagged commit %q", source, got, first)
		}
	}
}

// Interactively Rebase on the lightweight tag replays onto the tagged
// commit: the form labels the base with the tag name, and the drop lands
// exactly there. On the annotated tag the form opens onto the same commit —
// the peeled object — with the same plan.
func TestRefCreateInteractiveRebaseOntoTags(t *testing.T) {
	repo, first, _ := tagAncestorFixture(t, "rc-tag-ir")
	// Two more commits on main, so the plan over the tagged commit is nonempty.
	hashes := irLinearFixture(t, repo, "ir one", "ir two")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Lightweight: the full flow replays onto the tagged commit.
	chooseMenuItemFromRow(t, h, "lw", "Interactively Rebase on 'lw'…")
	if !hasExactText(h.ui, "Interactive Rebase") {
		t.Fatalf("interactive rebase form did not open; texts=%q", h.ui.Texts())
	}
	drainIRPlan(t, h)
	if !h.ui.HasText(short7(first) + " lw") {
		t.Fatalf("onto label missing; texts=%q", h.ui.Texts())
	}
	chooseIRAction(t, h, hashes[1], "Pick", "Drop")
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("drop rebase onto the tag failed: %v", err)
	}
	// main replays onto the tagged commit: base, ir one — ir two is dropped.
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"ir one", "second commit", "first commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after drop = %v, want %v", subjects, want)
	}
	assertIRDialogClosed(t, h)

	// Annotated: the plan read succeeds onto the peeled commit, with the tag
	// name as the base label — a tag object could never serve as a base.
	chooseMenuItemFromRow(t, h, "ann", "Interactively Rebase on 'ann'…")
	if !hasExactText(h.ui, "Interactive Rebase") {
		t.Fatalf("interactive rebase form did not open for ann; texts=%q", h.ui.Texts())
	}
	drainIRPlan(t, h)
	if !h.ui.HasText(short7(first) + " ann") {
		t.Fatalf("annotated onto label missing; texts=%q", h.ui.Texts())
	}
	if st := interactiveRebaseUI(h.app); st.readErr != nil {
		t.Fatalf("annotated plan read failed: %v", st.readErr)
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || btn.Disabled {
		t.Fatalf("Rebase onto the annotated tag's commit = %+v found=%v, want present and enabled", btn, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// --- disabled conditions ---

// The interactive rebase entry of the remote branch and the tag rows and
// their create entries disable while a write is in flight: a busy session
// must never start a second write.
func TestRefCreateItemsDisabledWhileBusy(t *testing.T) {
	repo, _, _ := remoteAheadFixture(t, "rc-busy")
	repo.Git("tag", "lw", "HEAD")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	write := holdBusyWrite(t, h, repo)

	remoteItems := []string{"Interactively Rebase on 'origin/main'…", "New Branch…", "New Tag…"}
	assertRowItemsDisabled(t, h, "origin/main", remoteItems)
	tagItems := []string{"Interactively Rebase on 'lw'…", "New Branch…", "New Tag…"}
	assertRowItemsDisabled(t, h, "lw", tagItems)

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(write)
	h.drain()
}

// The interactive rebase entry disables while an operation is in progress,
// with a detached HEAD, and when the row's commit is HEAD itself: none of
// those states can host a plan the user could confirm. The rows of the
// remote branch and the tags share every condition.
func TestRefCreateInteractiveRebaseDisabledStates(t *testing.T) {
	repo, base, _ := remoteAheadFixture(t, "rc-ir-states")
	repo.Git("tag", "onhead", "HEAD")
	irRemote := "Interactively Rebase on 'origin/main'…"

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// During an operation — here a cherry-pick stopped on a conflict — the
	// remote branch and the tag entries disable.
	repo.Git("checkout", "-qb", "side", base)
	repo.WriteFile("conflict.txt", "one\n")
	repo.Git("add", "--", "conflict.txt")
	repo.Git("commit", "-qm", "conflict one")
	side := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.Checkout("main")
	repo.WriteFile("conflict.txt", "two\n")
	repo.Git("add", "--", "conflict.txt")
	repo.Git("commit", "-qm", "conflict two")
	if out, err := tryGitCommand(repo.Path(), "cherry-pick", side); err == nil {
		t.Fatalf("conflicting cherry-pick unexpectedly succeeded: %s", out)
	}
	h.app.session.Reload()
	h.drain()
	if got := h.app.session.Snapshot().Status.State; got != "cherry-pick" {
		t.Fatalf("fixture: state = %q, want cherry-pick", got)
	}
	assertRowItemsDisabled(t, h, "origin/main", []string{irRemote})
	assertRowItemsDisabled(t, h, "onhead", []string{"Interactively Rebase on 'onhead'…"})
	repo.Git("cherry-pick", "--abort")
	repo.Git("reset", "--hard", "-q", base)
	repo.Git("checkout", "-q", "main")
	h.app.session.Reload()
	h.drain()

	// A detached HEAD has no branch for the rebase to move.
	repo.Git("checkout", "-q", "--detach", "HEAD")
	h.app.session.Reload()
	h.drain()
	if got := h.app.session.Snapshot().Status.Branch; got != "" {
		t.Fatalf("fixture: branch = %q, want detached", got)
	}
	assertRowItemsDisabled(t, h, "origin/main", []string{irRemote})
	assertRowItemsDisabled(t, h, "onhead", []string{"Interactively Rebase on 'onhead'…"})
	repo.Checkout("main")
	h.app.session.Reload()
	h.drain()

	// A tag pointing at HEAD itself would leave an empty plan: its entry
	// disables, while the create entries stay enabled.
	if err := h.ui.RightClick("onhead"); err != nil {
		t.Fatalf("right-click onhead: %v\ntexts=%q", err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem("Interactively Rebase on 'onhead'…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing the tag-at-HEAD item = %v, want a disabled error", err)
	}
	if err := h.ui.ChooseMenuItem("New Branch…"); err != nil {
		t.Fatalf("New Branch… on the tag at HEAD must stay enabled: %v", err)
	}
	h.ui.CloseMenu()
	h.ui.Frame()
}

// A remote-tracking branch pointing at HEAD itself would leave an empty
// plan: its interactive rebase entry disables while the create entries stay
// enabled.
func TestRefCreateInteractiveRebaseDisabledRemoteAtHead(t *testing.T) {
	repo := newGitRepo(t, "rc-ir-athead")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")
	if got, head := strings.TrimSpace(repo.Git("rev-parse", "refs/remotes/origin/main")), strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != head {
		t.Fatalf("fixture: origin/main = %q, want HEAD %q", got, head)
	}

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/main"); err != nil {
		t.Fatalf("right-click origin/main: %v\ntexts=%q", err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem("Interactively Rebase on 'origin/main'…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing the remote-at-HEAD item = %v, want a disabled error", err)
	}
	if err := h.ui.ChooseMenuItem("New Branch…"); err != nil {
		t.Fatalf("New Branch… on the remote at HEAD must stay enabled: %v", err)
	}
	h.ui.CloseMenu()
	h.ui.Frame()
}

// A repository whose HEAD is unborn — no local commit yet, while the
// remote-tracking branches are already fetched — disables the interactive
// rebase entries: there is nothing to rewrite.
func TestRefCreateInteractiveRebaseDisabledUnbornHead(t *testing.T) {
	// The remote side of the fixture is the bare repository origin points
	// at: the clone-less local repository fetches it without ever committing.
	seed := newGitRepo(t, "rc-unborn-seed")
	seed.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := seed.AddBareRemote("origin")

	repo := newGitRepo(t, "rc-unborn")
	repo.Git("remote", "add", "origin", bareDir)
	repo.Git("fetch", "-q", "origin")
	if out, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "refs/remotes/origin/main"); err != nil {
		t.Fatalf("fixture: origin/main missing after fetch: %v\n%s", err, out)
	}
	if _, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		t.Fatal("fixture: the unborn repository has a HEAD commit")
	}

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if got := h.app.session.Snapshot().Status.Head; got != "" {
		t.Fatalf("fixture: snapshot head = %q, want unborn", got)
	}

	assertRowItemsDisabled(t, h, "origin/main", []string{"Interactively Rebase on 'origin/main'…"})
}

// --- protected execution ---

// A remote-tracking branch moved externally after the dialog opened refuses
// the create: the exact commit it pointed at is the user's start point, and
// a moved reference is a different start point the user never confirmed.
func TestRefCreateGuardRemoteRefMoved(t *testing.T) {
	repo, base, remoteTip := remoteAheadFixture(t, "rc-guard-remote")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openNewBranchFromRow(t, h, "origin/main")
	typeBranchCreateName(t, h, "copy")

	// The remote-tracking reference moves externally after the dialog
	// opened, the way a fetch would move it.
	tree := strings.TrimSpace(repo.Git("rev-parse", remoteTip+"^{tree}"))
	newTip := strings.TrimSpace(repo.Git("commit-tree", tree, "-p", remoteTip, "-m", "upstream work"))
	repo.Git("update-ref", "refs/remotes/origin/main", newTip)
	if newTip == remoteTip {
		t.Fatal("fixture: the external move did not move origin/main")
	}

	confirmBranchCreate(t, h)

	if branchExists(t, repo, "copy") {
		t.Fatal("copy was created from a moved remote-tracking branch")
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("create from a moved remote-tracking branch produced no error")
	} else if !strings.Contains(err.Error(), "moved after the dialog opened") {
		t.Fatalf("refusal = %q, want it to say the source moved", err)
	}
	// The moved reference itself is untouched.
	if got := strings.TrimSpace(repo.Git("rev-parse", "refs/remotes/origin/main")); got != newTip {
		t.Fatalf("origin/main = %q after the refusal, want its external tip %q", got, newTip)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "main")); got != base {
		t.Fatalf("main moved to %q by the refused create, want %q", got, base)
	}
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("the refusal switched the branch: %q, want main", got)
	}
}

// A remote-tracking branch deleted externally after the dialog opened
// refuses the create: the source is re-read on the repository itself and
// found gone, so no branch is created.
func TestRefCreateGuardRemoteRefDeleted(t *testing.T) {
	repo, _, _ := remoteAheadFixture(t, "rc-guard-rdel")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openNewBranchFromRow(t, h, "origin/main")
	typeBranchCreateName(t, h, "copy")

	repo.Git("update-ref", "-d", "refs/remotes/origin/main")

	confirmBranchCreate(t, h)

	if branchExists(t, repo, "copy") {
		t.Fatal("copy was created from a deleted remote-tracking branch")
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("create from a deleted remote-tracking branch produced no error")
	} else if !strings.Contains(err.Error(), "no longer exists") {
		t.Fatalf("refusal = %q, want it to say the source no longer exists", err)
	}
}

// A lightweight tag retargeted externally after the dialog opened refuses
// the create: the guard re-reads refs/tags/<name> peeled to its commit and
// the tag the user confirmed is gone.
func TestRefCreateGuardLightweightTagMoved(t *testing.T) {
	repo, first, second := tagAncestorFixture(t, "rc-guard-lw")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openNewBranchFromRow(t, h, "lw")
	typeBranchCreateName(t, h, "copy")

	// The tag moves onto HEAD externally after the dialog opened.
	repo.Git("tag", "-f", "lw", second)

	confirmBranchCreate(t, h)

	if branchExists(t, repo, "copy") {
		t.Fatal("copy was created from a moved tag")
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("create from a moved tag produced no error")
	} else if !strings.Contains(err.Error(), "moved after the dialog opened") {
		t.Fatalf("refusal = %q, want it to say the source moved", err)
	}
	// The moved tag itself is untouched.
	if got := tagPointsAt(t, repo, "lw"); got != second {
		t.Fatalf("lw = %q after the refusal, want its retargeted %q untouched", got, second)
	}
	if got := tagPointsAt(t, repo, "lw"); got == first {
		t.Fatal("fixture broken: lw was never retargeted away from the first commit")
	}
}

// An annotated tag force-moved externally after the dialog opened refuses
// the create: the guard compares the peeled commit, so a tag object replaced
// on another commit is caught exactly like a retargeted lightweight tag.
func TestRefCreateGuardAnnotatedTagMoved(t *testing.T) {
	repo, first, second := tagAncestorFixture(t, "rc-guard-ann")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openNewBranchFromRow(t, h, "ann")
	typeBranchCreateName(t, h, "copy")

	// Force-moving the annotated tag onto HEAD replaces its object.
	repo.Git("tag", "-f", "-a", "ann", "-m", "moved release", second)

	confirmBranchCreate(t, h)

	if branchExists(t, repo, "copy") {
		t.Fatal("copy was created from a moved annotated tag")
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("create from a moved annotated tag produced no error")
	} else if !strings.Contains(err.Error(), "moved after the dialog opened") {
		t.Fatalf("refusal = %q, want it to say the source moved", err)
	}
	if got := tagPointsAt(t, repo, "ann"); got != second {
		t.Fatalf("ann = %q after the refusal, want its retargeted %q untouched", got, second)
	}
	if got := tagPointsAt(t, repo, "ann"); got == first {
		t.Fatal("fixture broken: ann was never retargeted away from the first commit")
	}
}

// --- Loopback SSH ---

// Over a real SSH connection the remote branch menu's New Branch creates the
// branch on the server at the exact commit the remote-tracking branch
// pointed at and checks it out, without an upstream.
func TestRefCreateNewBranchFromRemoteLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	// The remote work on the server: main pushed to a bare remote, one more
	// commit pushed, then the server's main rewound — origin/main ahead.
	bare := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repo.Path, "remote", "add", "origin", bare)
	srv.RunGit(t, repo.Path, "push", "-q", "-u", "origin", "main")
	base := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")
	srv.WriteFile(t, repo.Path, "remote.txt", []byte("remote\n"))
	srv.RunGit(t, repo.Path, "add", "remote.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "remote work")
	remoteTip := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	srv.RunGit(t, repo.Path, "push", "-q", "origin", "main")
	srv.RunGit(t, repo.Path, "reset", "--hard", "-q", base)

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	if _, ok := h.ui.Find("origin/main"); !ok {
		t.Fatalf("origin/main missing from the SSH sidebar; texts=%q", h.ui.Texts())
	}

	openNewBranchFromRow(t, h, "origin/main")
	if !h.ui.HasText("origin/main (" + short7(remoteTip) + ")") {
		t.Fatalf("SSH start point hint missing; texts=%q", h.ui.Texts())
	}
	typeBranchCreateName(t, h, "ssh-copy")
	confirmBranchCreate(t, h)

	if out := srv.RunGit(t, repo.Path, "branch", "--list", "ssh-copy"); strings.TrimSpace(out) == "" {
		t.Fatal("ssh-copy was not created on the server")
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "ssh-copy")); got != remoteTip {
		t.Fatalf("ssh-copy points at %q, want the remote commit %q", got, remoteTip)
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "ssh-copy" {
		t.Fatalf("SSH current branch after create = %q, want ssh-copy", got)
	}
	upstreamCheck := srv.RunShell(t, "cd "+git.ShellQuote(repo.Path)+"; git config --get branch.ssh-copy.remote >/dev/null 2>&1; echo rc=$?")
	if strings.TrimSpace(upstreamCheck) != "rc=1" {
		t.Fatalf("ssh-copy upstream check = %q, want rc=1 (no upstream configured)", upstreamCheck)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH create error: %v", err)
	}
}
