package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/store"
)

// --- helpers ---

// openToolbarPullDialog clicks the toolbar Pull button and asserts the Pull
// dialog opened.
func openToolbarPullDialog(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Pull"); err != nil {
		t.Fatalf("click Pull: %v", err)
	}
	h.ui.Frame()
	if !syncStateFor(h.app).pullDlg.open {
		t.Fatalf("Pull dialog did not open; texts=%q", h.ui.Texts())
	}
}

// selectPullRemote switches the dialog's remote to name through the real
// select control.
func selectPullRemote(t *testing.T, h *testHarness, name string) {
	t.Helper()
	st := &syncStateFor(h.app).pullDlg
	from, to := -1, -1
	for i, r := range st.remotes {
		if r == st.remote {
			from = i
		}
		if r == name {
			to = i
		}
	}
	if from < 0 || to < 0 {
		t.Fatalf("cannot switch remote %q -> %q in %v", st.remote, name, st.remotes)
	}
	driveSelect(t, h, "Pull Remote", from, to)
	if got := syncStateFor(h.app).pullDlg.remote; got != name {
		t.Fatalf("dialog remote = %q, want %q", got, name)
	}
}

// selectPullBranch switches the dialog's branch to name through the real
// select control.
func selectPullBranch(t *testing.T, h *testHarness, name string) {
	t.Helper()
	st := &syncStateFor(h.app).pullDlg
	from, to := -1, -1
	for i, b := range st.branches {
		if b == st.branch {
			from = i
		}
		if b == name {
			to = i
		}
	}
	if from < 0 || to < 0 {
		t.Fatalf("cannot switch branch %q -> %q in %v", st.branch, name, st.branches)
	}
	driveSelect(t, h, "Pull Branch", from, to)
	if got := syncStateFor(h.app).pullDlg.branch; got != name {
		t.Fatalf("dialog branch = %q, want %q", got, name)
	}
}

// pullDialogFixture builds a repository whose main is checked out and tracks
// origin/main of a bare remote: the state the dialog's defaults resolve.
func pullDialogFixture(t *testing.T, name string) (*gitRepoFixture, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	return repo, bareDir
}

// divergingPullFixture builds the pull fixture, advances origin/main by one
// peer commit the local repository never fetches, and diverges the local
// main with a commit of its own. It returns the peer commit's hash: the
// second parent of the merge a pull creates, or the remote head a rebase
// replays onto.
func divergingPullFixture(t *testing.T, name string) (repo *gitRepoFixture, bareDir, peerHead string) {
	t.Helper()
	repo, bareDir = pullDialogFixture(t, name)
	peerHead = pushPeerCommit(t, bareDir, "main", "peer.txt", "peer\n", "peer commit")
	repo.CommitFile("main.txt", "main\n", "main work")
	return repo, bareDir, peerHead
}

// --- defaults ---

// With an upstream the dialog defaults to the upstream's remote and branch,
// the branch list holds the branches of that remote only, and the unchecked
// boxes follow the store's defaults.
func TestPullDialogDefaultsUpstreamRemoteAndBranch(t *testing.T) {
	repo := newGitRepo(t, "pulldlg-def-upstream")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	addSecondBareRemote(t, repo, "origin")
	bareUpstream := addSecondBareRemote(t, repo, "upstream")
	repo.Git("push", "-q", "-u", "upstream", "main")
	// upstream carries a branch origin does not, and origin one upstream
	// does not: the branch list must hold the selected remote's branches.
	repo.Git("push", "-q", "upstream", "main:refs/heads/uonly")
	repo.Git("fetch", "-q", "upstream")
	repo.Git("push", "-q", "origin", "main")
	repo.Git("push", "-q", "origin", "main:refs/heads/oonly")
	repo.Git("fetch", "-q", "origin")
	_ = bareUpstream
	if up := strings.TrimSpace(repo.Git("for-each-ref", "--format=%(upstream:short)", "refs/heads/main")); up != "upstream/main" {
		t.Fatalf("main upstream = %q, want upstream/main", up)
	}

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPullDialog(t, h)
	st := &syncStateFor(h.app).pullDlg
	if st.remote != "upstream" {
		t.Fatalf("default remote = %q, want upstream (the upstream's remote)", st.remote)
	}
	if st.branch != "main" {
		t.Fatalf("default branch = %q, want main (the upstream's branch)", st.branch)
	}
	for _, b := range st.branches {
		if b == "oonly" {
			t.Fatalf("branches of upstream = %v, must not list origin's oonly", st.branches)
		}
	}
	foundUonly := false
	for _, b := range st.branches {
		if b == "uonly" {
			foundUonly = true
		}
		if strings.Contains(b, "/") || b == "HEAD" {
			t.Fatalf("branches of upstream = %v, entries must drop the remote prefix and the HEAD symref", st.branches)
		}
	}
	if !foundUonly {
		t.Fatalf("branches of upstream = %v, want uonly listed", st.branches)
	}
	if st.rebase || st.stash {
		t.Fatalf("default boxes = (rebase=%v, stash=%v), want (false, false)", st.rebase, st.stash)
	}
	if !hasExactText(h.ui, "main") {
		t.Fatalf("Into line missing the current branch; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Pull"); !ok || btn.Disabled {
		t.Fatalf("Confirm Pull = %+v (found=%v), want enabled", btn, ok)
	}
}

// Without an upstream the remote defaults to origin and the branch to the
// one sharing the current branch's name, ahead of the first branch of the
// list; without origin the remote is the first one.
func TestPullDialogDefaultsNoUpstream(t *testing.T) {
	t.Run("origin and same-named branch", func(t *testing.T) {
		repo := newGitRepo(t, "pulldlg-def-origin")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.AddBareRemote("origin")
		repo.Git("branch", "--unset-upstream", "main")
		// aaa sorts ahead of main in the reference listing: a default of aaa
		// would mean the first branch won over the same-named one.
		repo.Git("push", "-q", "origin", "main:refs/heads/aaa")
		repo.Git("fetch", "-q", "origin")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		openToolbarPullDialog(t, h)
		st := &syncStateFor(h.app).pullDlg
		if st.remote != "origin" {
			t.Fatalf("default remote = %q, want origin", st.remote)
		}
		if len(st.branches) == 0 || st.branches[0] != "aaa" {
			t.Fatalf("fixture broken: branches = %v, want aaa first", st.branches)
		}
		if st.branch != "main" {
			t.Fatalf("default branch = %q, want main (the same-named branch), not the first of %v", st.branch, st.branches)
		}
	})

	t.Run("first remote without origin", func(t *testing.T) {
		repo := newGitRepo(t, "pulldlg-def-first")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		bareBackup := addSecondBareRemote(t, repo, "backup")
		addSecondBareRemote(t, repo, "mirror")
		repo.Git("push", "-q", "backup", "main")
		repo.Git("fetch", "-q", "backup")
		_ = bareBackup

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		openToolbarPullDialog(t, h)
		st := &syncStateFor(h.app).pullDlg
		if len(st.remotes) == 0 || st.remote != st.remotes[0] {
			t.Fatalf("default remote = %q, want the first remote of %v", st.remote, st.remotes)
		}
		if st.remote == "origin" {
			t.Fatal("default remote is origin although origin is not configured")
		}
	})
}

// Switching the remote rebuilds the branch list from that remote's branches
// and re-selects the default branch.
func TestPullDialogSwitchRemoteRebuildsBranches(t *testing.T) {
	repo := newGitRepo(t, "pulldlg-switch")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")
	bareBackup := addSecondBareRemote(t, repo, "backup")
	repo.Git("push", "-q", "backup", "main")
	repo.Git("push", "-q", "origin", "main:refs/heads/oonly")
	repo.Git("push", "-q", "backup", "main:refs/heads/bonly")
	repo.Git("fetch", "-q", "origin")
	repo.Git("fetch", "-q", "backup")
	_ = bareBackup

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPullDialog(t, h)
	st := &syncStateFor(h.app).pullDlg
	if st.remote != "origin" || st.branch != "main" {
		t.Fatalf("defaults = (%q, %q), want (origin, main)", st.remote, st.branch)
	}

	selectPullRemote(t, h, "backup")
	st = &syncStateFor(h.app).pullDlg
	hasOonly, hasBonly := false, false
	for _, b := range st.branches {
		hasOonly = hasOonly || b == "oonly"
		hasBonly = hasBonly || b == "bonly"
	}
	if hasOonly || !hasBonly {
		t.Fatalf("branches after switching to backup = %v, want backup's branches without oonly", st.branches)
	}
	if st.branch != "main" {
		t.Fatalf("branch after switching to backup = %q, want main (the same-named default)", st.branch)
	}

	selectPullRemote(t, h, "origin")
	st = &syncStateFor(h.app).pullDlg
	hasOonly, hasBonly = false, false
	for _, b := range st.branches {
		hasOonly = hasOonly || b == "oonly"
		hasBonly = hasBonly || b == "bonly"
	}
	if !hasOonly || hasBonly {
		t.Fatalf("branches after switching back to origin = %v, want origin's branches without bonly", st.branches)
	}
}

// Choices remembered in the store fill the two check boxes.
func TestPullDialogDefaultsFromStore(t *testing.T) {
	repo, _ := pullDialogFixture(t, "pulldlg-def-store")

	h := newTestApp(t)
	if err := h.store.SetSheets(map[string]bool{
		store.PullRebase:          true,
		store.PullStashAndReapply: true,
	}); err != nil {
		t.Fatalf("seed sheets: %v", err)
	}
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPullDialog(t, h)
	st := &syncStateFor(h.app).pullDlg
	if !st.rebase || !st.stash {
		t.Fatalf("boxes from store = (rebase=%v, stash=%v), want (true, true)", st.rebase, st.stash)
	}
	for label, want := range map[string]bool{
		"Rebase instead of merge":         true,
		"Stash and reapply local changes": true,
	} {
		box, ok := h.ui.AccessByRole(ui.RoleCheckBox, label)
		if !ok || box.Checked != want {
			t.Fatalf("%s box = %+v (found=%v), want checked=%v", label, box, ok, want)
		}
	}
}

// --- real pulls through the dialog ---

// A diverged pull with both boxes unticked merges: the current branch's tip
// becomes a merge commit whose second parent is the remote branch.
func TestPullDialogMerge(t *testing.T) {
	repo, _, peerHead := divergingPullFixture(t, "pulldlg-merge")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPullDialog(t, h)
	if err := h.ui.Click("Confirm Pull"); err != nil {
		t.Fatalf("click Confirm Pull: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("merge pull failed: %v", err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--merges", "--count", "HEAD"); got != "1" {
		t.Fatalf("merge commits after the pull = %q, want 1", got)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD^2"); got != peerHead {
		t.Fatalf("second parent of the merge = %q, want the remote branch %q", got, peerHead)
	}
	assertFileBytes(t, repo, "peer.txt", "peer\n")
	assertFileBytes(t, repo, "main.txt", "main\n")
	if _, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); err == nil {
		t.Fatal("MERGE_HEAD survived a completed merge pull")
	}
}

// A diverged pull with Rebase instead of merge ticked rebases: the history
// stays linear and the remote branch is an ancestor of the current one.
func TestPullDialogRebase(t *testing.T) {
	repo, _, peerHead := divergingPullFixture(t, "pulldlg-rebase")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPullDialog(t, h)
	tickBox(t, h, "Rebase instead of merge")
	if err := h.ui.Click("Confirm Pull"); err != nil {
		t.Fatalf("click Confirm Pull: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase pull failed: %v", err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--merges", "--count", "HEAD"); got != "0" {
		t.Fatalf("merge commits after rebase pull = %q, want 0 — the history must stay linear", got)
	}
	if _, err := tryGitCommand(repo.Path(), "merge-base", "--is-ancestor", peerHead, "HEAD"); err != nil {
		t.Fatalf("remote branch %q is not an ancestor of HEAD after the rebase pull: %v", peerHead, err)
	}
	subjects := gitOutTrim(t, repo.Path(), "log", "--format=%s", "-3")
	lines := strings.Split(subjects, "\n")
	if len(lines) != 3 || lines[0] != "main work" || lines[1] != "peer commit" || lines[2] != "initial commit" {
		t.Fatalf("log subjects after rebase pull = %q, want main work / peer commit / initial commit", lines)
	}
	assertFileBytes(t, repo, "peer.txt", "peer\n")
	assertFileBytes(t, repo, "main.txt", "main\n")
}

// Pulling a branch other than the upstream pulls that branch: the dialog's
// branch choice reaches the pull, not the upstream the branch tracks.
func TestPullDialogNonUpstreamBranch(t *testing.T) {
	repo, _ := remotePullFixture(t, "pulldlg-nonupstream")
	featureTip := gitOutTrim(t, repo.Path(), "rev-parse", "refs/remotes/origin/feature")
	headBefore := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPullDialog(t, h)
	st := &syncStateFor(h.app).pullDlg
	if st.branch != "main" {
		t.Fatalf("default branch = %q, want main (the upstream)", st.branch)
	}
	selectPullBranch(t, h, "feature")
	if err := h.ui.Click("Confirm Pull"); err != nil {
		t.Fatalf("click Confirm Pull: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("pull of the non-upstream branch failed: %v", err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != featureTip {
		t.Fatalf("HEAD after pulling feature = %q, want the feature tip %q — the upstream pull would have left %q", got, featureTip, headBefore)
	}
	assertFileBytes(t, repo, "feature.txt", "feature\n")
}

// A pull over uncommitted changes to a file the pull updates is refused by
// git without the stash box — the branch stays put and the edit survives —
// and carries the edit across the pull with it ticked, leaving no stash
// entry behind.
func TestPullDialogStash(t *testing.T) {
	setup := func(t *testing.T, name string) (*gitRepoFixture, string) {
		t.Helper()
		// The peer edits line 1 of a.txt and the uncommitted local edit
		// touches line 6: git refuses to merge over any local change to a
		// file the pull updates, however disjoint the lines.
		repo := newGitRepo(t, name)
		repo.CommitFile("a.txt", "1\n2\n3\n4\n5\n6\n", "initial commit")
		bareDir := repo.AddBareRemote("origin")
		peerHead := pushPeerCommit(t, bareDir, "main", "a.txt", "peer-1\n2\n3\n4\n5\n6\n", "peer edits line one")
		repo.WriteFile("a.txt", "1\n2\n3\n4\n5\nlocal-6\n")
		return repo, peerHead
	}

	t.Run("unticked refuses", func(t *testing.T) {
		repo, _ := setup(t, "pulldlg-stash-off")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		headBefore := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
		openToolbarPullDialog(t, h)
		if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Stash and reapply local changes"); !ok || box.Checked {
			t.Fatalf("stash box = %+v (found=%v), want unticked", box, ok)
		}
		if err := h.ui.Click("Confirm Pull"); err != nil {
			t.Fatalf("click Confirm Pull: %v\ntexts=%q", err, h.ui.Texts())
		}
		h.drain()

		if err := h.app.session.ActionErr(); err == nil {
			t.Fatal("expected an action error for the pull over local changes")
		}
		if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != headBefore {
			t.Fatalf("HEAD moved during the refused pull: got %q, want %q", got, headBefore)
		}
		assertFileBytes(t, repo, "a.txt", "1\n2\n3\n4\n5\nlocal-6\n")
	})

	t.Run("ticked carries the changes", func(t *testing.T) {
		repo, peerHead := setup(t, "pulldlg-stash-on")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		openToolbarPullDialog(t, h)
		tickBox(t, h, "Stash and reapply local changes")
		if err := h.ui.Click("Confirm Pull"); err != nil {
			t.Fatalf("click Confirm Pull: %v\ntexts=%q", err, h.ui.Texts())
		}
		h.drain()

		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("stash pull failed: %v", err)
		}
		if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != peerHead {
			t.Fatalf("HEAD after stash pull = %q, want the fast-forwarded peer tip %q", got, peerHead)
		}
		// Line 1 came from the pull, line 6 survived from the stash.
		assertFileBytes(t, repo, "a.txt", "peer-1\n2\n3\n4\n5\nlocal-6\n")
		if out := strings.TrimSpace(repo.Git("stash", "list")); out != "" {
			t.Fatalf("stash list after the pull = %q, want empty — the reapply must not linger", out)
		}
		assertRefGone(t, repo, "refs/worktree/branchline-sync-stash")
	})
}

// --- remembering the check boxes ---

// The two boxes are remembered across submits: a reopened dialog starts from
// the choices of the last confirmed pull, and Cancel saves nothing.
func TestPullDialogRemembersChoices(t *testing.T) {
	repo, _ := pullDialogFixture(t, "pulldlg-remember")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPullDialog(t, h)
	tickBox(t, h, "Rebase instead of merge")
	tickBox(t, h, "Stash and reapply local changes")
	if err := h.ui.Click("Confirm Pull"); err != nil {
		t.Fatalf("click Confirm Pull: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("remembered pull failed: %v", err)
	}

	openToolbarPullDialog(t, h)
	st := &syncStateFor(h.app).pullDlg
	if !st.rebase || !st.stash {
		t.Fatalf("reopened boxes = (rebase=%v, stash=%v), want (true, true)", st.rebase, st.stash)
	}
	for key, want := range map[string]bool{
		store.PullRebase:          true,
		store.PullStashAndReapply: true,
	} {
		if got := h.store.Sheet(key, !want); got != want {
			t.Fatalf("stored %s = %v, want %v", key, got, want)
		}
	}

	// Cancel saves nothing: unticking both and canceling leaves the
	// remembered choices — and the next dialog — untouched.
	tickBox(t, h, "Rebase instead of merge")
	tickBox(t, h, "Stash and reapply local changes")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	openToolbarPullDialog(t, h)
	st = &syncStateFor(h.app).pullDlg
	if !st.rebase || !st.stash {
		t.Fatalf("boxes after cancel = (rebase=%v, stash=%v), want (true, true)", st.rebase, st.stash)
	}
	if got := h.store.Sheet(store.PullRebase, false); !got {
		t.Fatal("a canceled untick of Rebase instead of merge reached the store")
	}
	if got := h.store.Sheet(store.PullStashAndReapply, false); !got {
		t.Fatal("a canceled untick of Stash and reapply local changes reached the store")
	}
}

// When the store write fails the pull still runs and the failure surfaces as
// a toast instead of an error.
func TestPullDialogSaveFailureToast(t *testing.T) {
	repo, bareDir := pullDialogFixture(t, "pulldlg-savefail")
	peerHead := pushPeerCommit(t, bareDir, "main", "p.txt", "p\n", "peer commit")

	// Build the harness by hand so the store's directory can be made
	// read-only after the initial snapshot: SetSheets then fails on write.
	storeDir := t.TempDir()
	t.Cleanup(func() {
		if err := os.Chmod(storeDir, 0o755); err != nil {
			t.Errorf("restore store dir permissions: %v", err)
		}
	})
	s, err := store.Open(filepath.Join(storeDir, "repositories.json"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)
	a := newApp(s)
	a.postFn = q.post
	a.spawnFn = q.spawn
	h := &testHarness{
		t:     t,
		app:   a,
		store: s,
		ui:    ui.NewTester(a.view, 1200, 800),
		queue: q,
	}
	h.ui.Frame()
	openSyncRepo(t, h, repo.StoreRepo())

	if err := os.Chmod(storeDir, 0o555); err != nil {
		t.Fatalf("make store dir read-only: %v", err)
	}

	// Tick Stash so the save has something to write.
	openToolbarPullDialog(t, h)
	tickBox(t, h, "Stash and reapply local changes")
	if err := h.ui.Click("Confirm Pull"); err != nil {
		t.Fatalf("click Confirm Pull: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("pull failed although only the settings save may fail: %v", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != peerHead {
		t.Fatalf("HEAD = %q, want %q: the pull must run although saving failed", got, peerHead)
	}
	if !h.ui.HasText("Cannot save settings") {
		t.Fatalf("save failure toast missing; texts=%q", h.ui.Texts())
	}
}

// --- worker guard ---

// A commit landing on the current branch after the dialog opened refuses the
// pull: the error says the branch changed after the dialog opened and the
// repository stays exactly as the external commit left it. The fixture
// diverges main from origin/main so a pull run without the guard would move
// HEAD, making the refusal observable on the branch itself.
func TestPullDialogGuardHeadMoved(t *testing.T) {
	repo, _, _ := divergingPullFixture(t, "pulldlg-guard-head")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPullDialog(t, h)
	moved := repo.CommitFile("ext.txt", "ext\n", "external commit")
	rec := installRecordingExecutor(t, h)

	if err := h.ui.Click("Confirm Pull"); err != nil {
		t.Fatalf("click Confirm Pull: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()

	assertRepoErrorShown(t, h, "changed after the dialog opened")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != moved {
		t.Fatalf("HEAD after the refused pull = %q, want the external commit %q", got, moved)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--merges", "--count", "HEAD"); got != "0" {
		t.Fatalf("merge commits after the refused pull = %q, want 0 — nothing may have been merged", got)
	}
	if got := rec.count("pull"); got != 0 {
		t.Fatalf("guard let %d pull command(s) through, want 0", got)
	}
	if _, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); err == nil {
		t.Fatal("a merge is in progress even though the pull was refused")
	}
}

// A remote removed after the dialog opened refuses the pull before anything
// is pulled: the error names the missing remote and the branch stays put.
func TestPullDialogGuardRemoteRemoved(t *testing.T) {
	repo, _ := pullDialogFixture(t, "pulldlg-guard-remote")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPullDialog(t, h)
	rec := installRecordingExecutor(t, h)
	repo.Git("remote", "remove", "origin")

	mainBefore := gitOutTrim(t, repo.Path(), "rev-parse", "refs/heads/main")
	if err := h.ui.Click("Confirm Pull"); err != nil {
		t.Fatalf("click Confirm Pull: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()

	assertRepoErrorShown(t, h, "no longer exists")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "refs/heads/main"); got != mainBefore {
		t.Fatalf("main moved to %q despite the refused pull, want %q", got, mainBefore)
	}
	if got := rec.count("pull"); got != 0 {
		t.Fatalf("guard let %d pull command(s) through, want 0", got)
	}
}

// --- pre-flight checks ---

// A repository without remotes or with a detached HEAD opens no dialog: the
// pre-flight error shows in the dismissible toolbar strip and nothing runs.
func TestPullDialogPreflightChecks(t *testing.T) {
	t.Run("no remote", func(t *testing.T) {
		repo := newGitRepo(t, "pulldlg-noremote")
		repo.CommitFile("a.txt", "one\n", "initial commit")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		rec := installRecordingExecutor(t, h)

		if err := h.ui.Click("Pull"); err != nil {
			t.Fatalf("click Pull: %v", err)
		}
		h.ui.Frame()

		if syncStateFor(h.app).pullDlg.open {
			t.Fatal("Pull dialog opened without a remote")
		}
		if !h.ui.HasText("The repository has no remote to pull from.") {
			t.Fatalf("missing no-remote message; texts=%q", h.ui.Texts())
		}
		if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
			t.Fatalf("no Repository Error status after Pull without remote; texts=%q", h.ui.Texts())
		}
		if h.app.session.Busy() {
			t.Fatal("session busy for a pull that should not have started")
		}
		if total := rec.total(); total != 0 {
			t.Fatalf("no-remote validation ran %d git command(s), want 0", total)
		}
	})

	t.Run("detached HEAD", func(t *testing.T) {
		repo, _ := pullDialogFixture(t, "pulldlg-detached")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		repo.Git("checkout", "--detach", "HEAD")
		h.app.session.Reload()
		h.drain()
		rec := installRecordingExecutor(t, h)

		if err := h.ui.Click("Pull"); err != nil {
			t.Fatalf("click Pull: %v", err)
		}
		h.ui.Frame()

		if syncStateFor(h.app).pullDlg.open {
			t.Fatal("Pull dialog opened on a detached HEAD")
		}
		if !h.ui.HasText("HEAD is detached: check out a branch to pull into it.") {
			t.Fatalf("missing detached message; texts=%q", h.ui.Texts())
		}
		if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
			t.Fatalf("no Repository Error status after Pull on a detached HEAD; texts=%q", h.ui.Texts())
		}
		if h.app.session.Busy() {
			t.Fatal("session busy for a pull that should not have started")
		}
		if total := rec.total(); total != 0 {
			t.Fatalf("detached validation ran %d git command(s), want 0", total)
		}
	})
}

// --- close paths ---

// Cancel, Escape, the backdrop and a repository switch all close the dialog
// without running a single git command, proven by the recording executor on
// the repository.
func TestPullDialogClosePathsNeverPull(t *testing.T) {
	setup := func(t *testing.T, name string) *gitRepoFixture {
		repo, _ := pullDialogFixture(t, name)
		return repo
	}
	verify := func(t *testing.T, h *testHarness, rec *recordingRunExecutor) {
		t.Helper()
		h.ui.Frame()
		if syncStateFor(h.app).pullDlg.open {
			t.Fatal("Pull dialog survived the close path")
		}
		h.drain()
		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("closing the dialog produced an action error: %v", err)
		}
		if total := rec.total(); total != 0 {
			t.Fatalf("close path ran %d git command(s), want 0", total)
		}
	}

	t.Run("cancel button", func(t *testing.T) {
		repo := setup(t, "pulldlg-close-cancel")
		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		openToolbarPullDialog(t, h)
		rec := installRecordingExecutor(t, h)
		if err := h.ui.Click("Cancel"); err != nil {
			t.Fatalf("click Cancel: %v", err)
		}
		verify(t, h, rec)
	})

	t.Run("escape", func(t *testing.T) {
		repo := setup(t, "pulldlg-close-escape")
		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		openToolbarPullDialog(t, h)
		rec := installRecordingExecutor(t, h)
		h.ui.Key(0, ui.KeyEscape)
		verify(t, h, rec)
	})

	t.Run("backdrop", func(t *testing.T) {
		repo := setup(t, "pulldlg-close-backdrop")
		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		openToolbarPullDialog(t, h)
		rec := installRecordingExecutor(t, h)
		// The modal is centered in the 1200x800 test window; its top-left
		// corner is the dimmed backdrop outside the dialog.
		h.ui.ClickAt(20, 20)
		verify(t, h, rec)
	})

	t.Run("repository switch", func(t *testing.T) {
		repoA := setup(t, "pulldlg-close-a")
		repoB := newGitRepo(t, "pulldlg-close-b")
		repoB.CommitFile("b.txt", "b\n", "init b")

		h := newTestApp(t, repoA.StoreRepo(), repoB.StoreRepo())
		closeSync(t, h)
		h.openRepo(repoA.StoreRepo())
		h.drain()
		openToolbarPullDialog(t, h)
		rec := installRecordingExecutor(t, h)

		// The modal covers the switcher, so a switch with the dialog open is
		// programmatic — as opening a repository of the recent list is in the
		// product — and reaches the same generation change.
		h.openRepo(repoB.StoreRepo())
		h.ui.Frame()
		h.drain()
		if syncStateFor(h.app).pullDlg.open {
			t.Fatal("Pull dialog survived the repository switch")
		}
		if total := rec.total(); total != 0 {
			t.Fatalf("repository switch ran %d git command(s) against the old repository, want 0", total)
		}
	})
}

// --- toolbar entry ---

// Option+click on the toolbar Pull button never opens the dialog: it pulls
// the upstream directly, with exactly the arguments a plain click ran before
// the dialog existed.
func TestPullDialogOptionClickQuickPull(t *testing.T) {
	repo, bareDir := pullDialogFixture(t, "pulldlg-quickpull")
	peerHead := pushPeerCommit(t, bareDir, "main", "p.txt", "p\n", "peer commit")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())
	rec := installRecordingExecutor(t, h)

	if err := h.ui.ClickWith(ui.Alt, "Pull"); err != nil {
		t.Fatalf("Option+click Pull: %v", err)
	}
	h.ui.Frame()
	if syncStateFor(h.app).pullDlg.open {
		t.Fatal("Pull dialog opened on Option+click")
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("quick pull failed: %v", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != peerHead {
		t.Fatalf("HEAD after quick pull = %q, want %q", got, peerHead)
	}
	// The recorded pull is exactly the quick pull of before the dialog: the
	// upstream by configuration, merging, no rebase.
	rec.mu.Lock()
	var pulls [][]string
	for _, args := range rec.cmds {
		if len(args) > 0 && args[0] == "pull" {
			pulls = append(pulls, args)
		}
	}
	rec.mu.Unlock()
	if len(pulls) != 1 {
		t.Fatalf("recorded %d pull commands, want 1", len(pulls))
	}
	want := []string{"pull", "--no-edit", "--no-rebase"}
	if strings.Join(pulls[0], " ") != strings.Join(want, " ") {
		t.Fatalf("quick pull args = %v, want %v", pulls[0], want)
	}
}

// --- busy guard ---

// While the repository is busy the confirm button is disabled and Enter
// submits nothing; once idle again Enter submits like the button.
func TestPullDialogBusyConfirmDisabled(t *testing.T) {
	repo, bareDir := pullDialogFixture(t, "pulldlg-busy")
	peerHead := pushPeerCommit(t, bareDir, "main", "p.txt", "p\n", "peer commit")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPullDialog(t, h)

	// Start a real fetch behind the worker gate: the session is busy while
	// the dialog stays open.
	h.queue.PauseWorkers()
	h.app.syncFetch()
	if !h.app.session.Busy() {
		t.Fatal("session not busy after starting the gated fetch")
	}
	fetchWorker := h.queue.WaitWorker(t)
	h.ui.Frame()

	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Pull")
	if !ok || !btn.Disabled {
		t.Fatalf("Confirm Pull while busy = %+v (found=%v), want disabled", btn, ok)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if !syncStateFor(h.app).pullDlg.open {
		t.Fatal("Enter submitted the dialog while the repository was busy")
	}

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(fetchWorker)
	h.drain()
	if h.app.session.Busy() {
		t.Fatal("session stayed busy after the gated fetch finished")
	}

	// Idle again: the button is enabled and Enter submits the dialog.
	btn, ok = h.ui.AccessByRole(ui.RoleButton, "Confirm Pull")
	if !ok || btn.Disabled {
		t.Fatalf("Confirm Pull after drain = %+v (found=%v), want enabled", btn, ok)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if syncStateFor(h.app).pullDlg.open {
		t.Fatal("dialog stayed open after Enter submit")
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter-submitted pull failed: %v", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != peerHead {
		t.Fatalf("HEAD = %q, want %q after the Enter-submitted pull", got, peerHead)
	}
}

// --- loopback SSH ---

// Over loopback SSH the dialog pulls the server repository's upstream, and
// behind a password gate the worker asks through the real askpass modal —
// the dialog itself already closed when the question renders.
func TestPullDialogAskPassLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repoDir, "push", "-q", "-u", "origin", "main")

	// A peer advances origin/main so the pull has an observable effect.
	peerDir := srv.TempDir(t, "peer")
	srv.RunShell(t, "set -eu\nexport GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1\n"+
		"git clone -q "+shellQ(bareDir)+" "+shellQ(peerDir)+"\n"+
		"cd "+shellQ(peerDir)+"\n"+
		"git config user.name Peer\ngit config user.email peer@branchline.invalid\n"+
		"printf 'peer\\n' > peer.txt\ngit add -- peer.txt\ngit commit -q -m 'peer commit'\n"+
		"git push -q origin main\n")
	peerHead := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/main")

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	// Gate every new connection behind password authentication: from here on
	// only the askpass-enabled path reaches the server.
	const password = "hunter2t0ps3cret"
	gateDir := passwordGateWrapper(t, srv.SSHProgram(), password)
	t.Setenv("PATH", gateDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// The worker's status, refs and pull cross the gate as separate ssh
	// invocations, so the question comes back a handful of times. The loop
	// answers every question until the worker's completion arrives,
	// consuming the wake-up callbacks one at a time.
	answerPasswords := func() {
		t.Helper()
		for range 16 {
			h.queue.RunNext(t)
			h.ui.Frame()
			if _, ok := h.ui.Find("Cancel"); !ok {
				if !h.app.session.Busy() {
					return
				}
				continue
			}
			if syncStateFor(h.app).pullDlg.open {
				t.Fatal("the Pull dialog is still open under the askpass question")
			}
			h.ui.Type(password)
			h.ui.Frame()
			if err := h.ui.Click("OK"); err != nil {
				t.Fatalf("click OK in password dialog: %v", err)
			}
			h.ui.Frame()
		}
		t.Fatal("password questions did not stop after 16 rounds")
	}

	openToolbarPullDialog(t, h)
	if err := h.ui.Click("Confirm Pull"); err != nil {
		t.Fatalf("click Confirm Pull: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if syncStateFor(h.app).pullDlg.open {
		t.Fatal("the Pull dialog stayed open after the confirm")
	}

	answerPasswords()
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("pull behind the password gate failed after correct passwords: %v", err)
	}
	if got := srv.RunGit(t, repoDir, "rev-parse", "HEAD"); got != peerHead {
		t.Fatalf("server HEAD after dialog pull = %q, want %q", got, peerHead)
	}
	if data := srv.ReadFile(t, repoDir, "peer.txt"); string(data) != "peer\n" {
		t.Fatalf("server working tree peer.txt = %q, want peer\\n", data)
	}
	if got := srv.RunGit(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("server checked-out branch after the pull = %q, want main", got)
	}
}
