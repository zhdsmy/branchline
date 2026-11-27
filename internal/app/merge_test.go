package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// mergeFFFixture builds a fast-forwardable history: base on main, then one
// commit on feature ahead of main, with main checked out. It returns the
// base and feature tip hashes.
func mergeFFFixture(t *testing.T, repo *gitRepoFixture) (base, feature string) {
	t.Helper()
	base = repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CheckoutNewBranch("feature")
	feature = repo.CommitFile("feat.txt", "feat\n", "feature work")
	repo.Checkout("main")
	return base, feature
}

// mergeDivergedFixture builds a divergent history on files that do not
// overlap: base on main, feature adds feat.txt, main adds mainwork.txt.
// Merging either way applies cleanly with a merge commit or a fast-forward.
func mergeDivergedFixture(t *testing.T, repo *gitRepoFixture) (base, feature, mainTip string) {
	t.Helper()
	base = repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CheckoutNewBranch("feature")
	feature = repo.CommitFile("feat.txt", "feat\n", "feature work")
	repo.Checkout("main")
	mainTip = repo.CommitFile("mainwork.txt", "main\n", "main work")
	return base, feature, mainTip
}

// mergeConflictFixture builds a divergent history where both sides rewrite
// the same line of f.txt, so a merge of feature conflicts.
func mergeConflictFixture(t *testing.T, repo *gitRepoFixture) (feature, mainTip string) {
	t.Helper()
	repo.CommitFile("f.txt", "line\n", "base commit")
	repo.CheckoutNewBranch("feature")
	feature = repo.CommitFile("f.txt", "theirs\n", "feature edits")
	repo.Checkout("main")
	mainTip = repo.CommitFile("f.txt", "ours\n", "main edits")
	return feature, mainTip
}

// mergePredictProbe reports whether the git of dir can predict conflicts,
// skipping the test when merge-tree --write-tree is unavailable (git < 2.38).
func mergePredictProbe(t *testing.T, repo *gitRepoFixture) {
	t.Helper()
	r := git.New(repo.StoreRepo().Executor())
	if _, err := r.MergeConflicts("HEAD", "HEAD"); err != nil {
		if errors.Is(err, git.ErrPredictUnavailable) {
			t.Skipf("this git cannot predict conflicts: %v", err)
		}
	}
}

// openMergeDialog chooses the merge menu item of an already open context
// menu, verifies the dialog opened, and drains the conflict prediction.
func chooseMergeItem(t *testing.T, h *testHarness) {
	t.Helper()
	menu := h.ui.Menu()
	var label string
	for _, item := range menu {
		if strings.HasPrefix(item, "Merge into ") {
			label = item
			break
		}
	}
	if label == "" {
		t.Fatalf("menu %q has no merge item", menu)
	}
	if err := h.ui.ChooseMenuItem(label); err != nil {
		t.Fatalf("choose %q: %v", label, err)
	}
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, ""); !ok {
		t.Fatalf("merge dialog did not open; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "Merge") {
		t.Fatalf("merge dialog title missing; texts=%q", h.ui.Texts())
	}
}

// openMergeDialogFromBranch right-clicks the branch row of the sidebar and
// opens the merge dialog for it.
func openMergeDialogFromBranch(t *testing.T, h *testHarness, branch string) {
	t.Helper()
	if err := h.ui.RightClick(branch); err != nil {
		t.Fatalf("right-click branch row %q: %v\ntexts=%q", branch, err, h.ui.Texts())
	}
	chooseMergeItem(t, h)
	h.drain()
}

// openMergeDialogFromCommit right-clicks the commit row of hash on the
// History page and opens the merge dialog for it.
func openMergeDialogFromCommit(t *testing.T, h *testHarness, hash, subject string) {
	t.Helper()
	row := commitRowLabel(hash, subject)
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row %q: %v\ntexts=%q", row, err, h.ui.Texts())
	}
	chooseMergeItem(t, h)
	h.drain()
}

// confirmMerge clicks the dialog's confirm button and drains the write.
func confirmMerge(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Confirm Merge"); err != nil {
		t.Fatalf("click Confirm Merge: %v", err)
	}
	h.drain()
}

// parentsOf returns the parent hashes of rev in dir.
func parentsOf(t *testing.T, dir, rev string) []string {
	t.Helper()
	return strings.Fields(gitOutTrim(t, dir, "rev-list", "--parents", "-n", "1", rev))[1:]
}

// refExists reports whether ref resolves in dir, tolerating the nonzero exit
// git rev-parse --verify returns for a ref that is not there.
func refExists(t *testing.T, dir, ref string) bool {
	t.Helper()
	out, err := tryGitCommand(dir, "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
}

// remoteRefExists reports whether ref resolves in dir on the server,
// tolerating the nonzero exit of a missing ref.
func remoteRefExists(t *testing.T, srv *sshtest.Target, dir, ref string) bool {
	t.Helper()
	out := srv.RunShell(t, "GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -C "+git.ShellQuote(dir)+" rev-parse --verify --quiet "+git.ShellQuote(ref)+" 2>/dev/null || true")
	return strings.TrimSpace(out) != ""
}

// mergeDialogGone verifies the merge dialog is no longer rendered.
func mergeDialogGone(t *testing.T, h *testHarness) {
	t.Helper()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Merge"); ok {
		t.Fatalf("merge dialog still open; texts=%q", h.ui.Texts())
	}
}

// assertMergeBannerButtons verifies the operation banner of a merge sits
// inside the main content area of the default window and shows Continue and
// Abort — never Skip — inside its bounds.
func assertMergeBannerButtons(t *testing.T, h *testHarness) {
	t.Helper()
	assertMergeBannerLayoutIn(t, h, harnessWidth)
}

// assertMergeBannerLayoutIn verifies the merge banner fits inside a window
// winW wide and renders Continue and Abort (never Skip) inside its bounds.
func assertMergeBannerLayoutIn(t *testing.T, h *testHarness, winW float32) {
	t.Helper()
	const tol = 0.5
	bar, ok := h.ui.AccessByRole(ui.RoleStatus, "Operation in progress")
	if !ok {
		t.Fatalf("operation banner missing; texts=%q", h.ui.Texts())
	}
	if bar.Bounds.W <= 0 || bar.Bounds.X < sidebarWidth-tol || bar.Bounds.X+bar.Bounds.W > winW-16+tol {
		t.Fatalf("operation banner bounds %+v outside the content area %v..%v of a %v-wide window", bar.Bounds, float32(sidebarWidth), winW-16, winW)
	}
	for _, label := range []string{"Continue Operation", "Abort Operation"} {
		btn, ok := h.ui.AccessByRole(ui.RoleButton, label)
		if !ok {
			t.Fatalf("banner button %q missing; texts=%q", label, h.ui.Texts())
		}
		assertInside(t, btn.Bounds, bar.Bounds, "banner button "+label)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Skip Commit"); ok {
		t.Fatalf("merge banner must not offer Skip Commit; texts=%q", h.ui.Texts())
	}
}

// assertMergeBannerLayoutNarrow shrinks the window to 820x700, verifies the
// merge banner still fits inside it without offering Skip, and restores the
// default size.
func assertMergeBannerLayoutNarrow(t *testing.T, h *testHarness) {
	t.Helper()
	h.ui.SetSize(820, 700)
	h.ui.Frame()
	assertMergeBannerLayoutIn(t, h, 820)
	h.ui.SetSize(harnessWidth, 800)
	h.ui.Frame()
}

// mergeBannerGone verifies no frame element of the operation banner survives.
func mergeBannerGone(t *testing.T, h *testHarness) {
	t.Helper()
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Operation in progress"); ok {
		t.Fatalf("operation banner still rendered; texts=%q", h.ui.Texts())
	}
}

// startMergeConflict starts a conflicting merge of feature through the real
// UI entry of the branch sidebar and verifies the conflicted state, the
// banner copy and the disabled Continue button.
func startMergeConflict(t *testing.T, h *testHarness) {
	t.Helper()
	openMergeDialogFromBranch(t, h, "feature")
	confirmMerge(t, h)
	st := h.app.session.Snapshot().Status
	if st.State != "merge" {
		t.Fatalf("state after conflicting merge = %q, want merge", st.State)
	}
	if !st.Conflicted() {
		t.Fatalf("fixture: merge of feature did not conflict: %+v", st)
	}
	if st.Operation == "" {
		t.Fatal("operation target missing for the merge in progress")
	}
	if !hasExactText(h.ui, "Merge in progress. Resolve conflicts and stage the files to continue, or abort.") {
		t.Fatalf("merge conflict banner copy missing; texts=%q", h.ui.Texts())
	}
	assertMergeBannerButtons(t, h)
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Continue Operation"); !ok || !btn.Disabled {
		t.Fatalf("Continue Operation = %+v found=%v, want present and disabled during conflict", btn, ok)
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Abort Operation"); !ok || btn.Disabled {
		t.Fatalf("Abort Operation = %+v found=%v, want present and enabled during conflict", btn, ok)
	}
}

// A non-current local branch of the sidebar offers a merge into the checked
// out branch. The default mode fast-forwards when it can: HEAD moves to the
// feature tip as a single-parent commit, the dialog shows the merge type
// explanations and the no-conflicts prediction, and the merge item of the
// current branch is disabled.
func TestMergeFastForwardFromBranchMenu(t *testing.T) {
	repo := newGitRepo(t, "merge-ff")
	mergePredictProbe(t, repo)
	_, feature := mergeFFFixture(t, repo)
	base := gitOutTrim(t, repo.Path(), "rev-parse", "main")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// The merge item of the current branch is disabled: main cannot merge
	// into itself.
	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click current branch row: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Merge into 'main'…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing merge of the current branch = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()

	openMergeDialogFromBranch(t, h, "feature")
	assertDialogLayout(t, h, "Cancel", "Confirm Merge")
	if radio, ok := h.ui.AccessByRole(ui.RoleRadio, "Fast-forward if possible"); !ok || !radio.Checked {
		t.Fatalf("Fast-forward if possible = %+v found=%v, want present and checked", radio, ok)
	}
	if !hasExactText(h.ui, "Fast-forward when possible; otherwise create a merge commit.") {
		t.Fatalf("default merge type explanation missing; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "Merge can be done without conflicts") {
		t.Fatalf("clean prediction missing; texts=%q", h.ui.Texts())
	}

	confirmMerge(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("fast-forward merge failed: %v", err)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != feature {
		t.Fatalf("HEAD after fast-forward merge = %q, want feature tip %q", head, feature)
	}
	if got := len(parentsOf(t, repo.Path(), "HEAD")); got != 1 {
		t.Fatalf("fast-forwarded HEAD has %d parents, want 1", got)
	}
	if cur := parentsOf(t, repo.Path(), "HEAD"); len(cur) > 0 && cur[0] != base {
		t.Fatalf("fast-forwarded HEAD parent = %v, want base %q", cur, base)
	}
	assertFileBytes(t, repo, "feat.txt", "feat\n")
	mergeDialogGone(t, h)
	mergeBannerGone(t, h)
}

// The No Fast-Forward mode records a merge commit even when the merge could
// fast-forward: the new HEAD has two parents and its message names the merged
// branch.
func TestMergeNoFastForwardFromBranchMenu(t *testing.T) {
	repo := newGitRepo(t, "merge-noff")
	base, feature := mergeFFFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openMergeDialogFromBranch(t, h, "feature")

	if err := h.ui.Click("No Fast-Forward"); err != nil {
		t.Fatalf("click No Fast-Forward radio: %v", err)
	}
	h.ui.Frame()
	if radio, ok := h.ui.AccessByRole(ui.RoleRadio, "No Fast-Forward"); !ok || !radio.Checked {
		t.Fatalf("No Fast-Forward after click = %+v found=%v, want checked", radio, ok)
	}
	if !hasExactText(h.ui, "Always create a merge commit (--no-ff).") {
		t.Fatalf("no-ff explanation missing; texts=%q", h.ui.Texts())
	}
	confirmMerge(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("no-ff merge failed: %v", err)
	}
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	if head == feature || head == base {
		t.Fatalf("no-ff merge did not create a merge commit: HEAD %q", head)
	}
	parents := parentsOf(t, repo.Path(), "HEAD")
	if len(parents) != 2 || parents[0] != base || parents[1] != feature {
		t.Fatalf("merge commit parents = %v, want [%q %q]", parents, base, feature)
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); subject != "Merge branch 'feature'" {
		t.Fatalf("merge commit subject = %q, want Merge branch 'feature'", subject)
	}
	assertFileBytes(t, repo, "feat.txt", "feat\n")
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after no-ff merge = %q, want empty", got)
	}
}

// The Squash mode stages the merged changes without a merge commit and
// without MERGE_HEAD: HEAD stays put, state stays empty and no operation
// banner appears. The same squash over a content conflict surfaces Git's
// error, leaves the file unmerged in Local Changes and still shows no
// banner, because a squash never starts a recoverable merge.
func TestMergeSquashFromBranchMenu(t *testing.T) {
	repo := newGitRepo(t, "merge-squash")
	_, _, mainTip := mergeDivergedFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openMergeDialogFromBranch(t, h, "feature")

	if err := h.ui.Click("Squash"); err != nil {
		t.Fatalf("click Squash radio: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Stage squashed changes without creating a merge commit (--squash).") {
		t.Fatalf("squash explanation missing; texts=%q", h.ui.Texts())
	}
	confirmMerge(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("squash merge failed: %v", err)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD moved by a squash merge: %q, want %q", head, mainTip)
	}
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); !strings.Contains(out, "feat.txt") {
		t.Fatalf("staged after squash merge = %q, want feat.txt", out)
	}
	assertFileBytes(t, repo, "feat.txt", "feat\n")
	st := h.app.session.Snapshot().Status
	if st.State != "" {
		t.Fatalf("state after squash merge = %q, want empty", st.State)
	}
	if refExists(t, repo.Path(), "MERGE_HEAD") {
		t.Fatal("MERGE_HEAD written by a squash merge")
	}
	if len(st.Staged) == 0 {
		t.Fatal("snapshot shows nothing staged after the squash merge")
	}
	mergeBannerGone(t, h)
	mergeDialogGone(t, h)
}

// A conflicting squash merge surfaces Git's error in the toolbar, leaves
// f.txt conflicted in the Unstaged list of Local Changes, and shows no
// operation banner: without MERGE_HEAD there is no merge to continue or
// abort.
func TestMergeSquashConflictShowsNoBanner(t *testing.T) {
	repo := newGitRepo(t, "merge-squash-conflict")
	mergeConflictFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openMergeDialogFromBranch(t, h, "feature")
	if err := h.ui.Click("Squash"); err != nil {
		t.Fatalf("click Squash radio: %v", err)
	}
	h.ui.Frame()
	confirmMerge(t, h)

	assertRepoErrorShown(t, h, "")
	st := h.app.session.Snapshot().Status
	if st.State != "" {
		t.Fatalf("state after conflicting squash = %q, want empty", st.State)
	}
	conflicted := false
	for _, f := range st.Unstaged {
		if f.Path == "f.txt" && f.Status == 'U' {
			conflicted = true
		}
	}
	if !conflicted {
		t.Fatalf("f.txt not unmerged in the snapshot: %+v", st.Unstaged)
	}
	mergeBannerGone(t, h)
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, "Conflicted f.txt"); !ok {
		t.Fatalf("conflicted f.txt not listed on Local Changes; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Operation in progress"); ok {
		t.Fatalf("operation banner rendered for a squash conflict; texts=%q", h.ui.Texts())
	}
}

// The Don't Commit mode stops a divergent clean merge before committing:
// MERGE_HEAD exists, the changes are staged, nothing is conflicted, and the
// shared operation banner offers Continue and Abort — never Skip, because a
// merge has nothing to skip. Continue then concludes the merge with a
// two-parent commit.
func TestMergeNoCommitContinueFromBanner(t *testing.T) {
	repo := newGitRepo(t, "merge-nocommit-continue")
	_, feature, mainTip := mergeDivergedFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openMergeDialogFromBranch(t, h, "feature")
	if err := h.ui.Click("Don't Commit"); err != nil {
		t.Fatalf("click Don't Commit radio: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Merge without automatically committing (--no-commit).") {
		t.Fatalf("no-commit explanation missing; texts=%q", h.ui.Texts())
	}
	confirmMerge(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("no-commit merge failed: %v", err)
	}
	st := h.app.session.Snapshot().Status
	if st.State != "merge" {
		t.Fatalf("state after no-commit merge = %q, want merge", st.State)
	}
	if st.Conflicted() {
		t.Fatalf("clean no-commit merge conflicted: %+v", st)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); got != feature {
		t.Fatalf("MERGE_HEAD = %q, want feature tip %q", got, feature)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD moved before concluding the merge: %q, want %q", head, mainTip)
	}
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); !strings.Contains(out, "feat.txt") {
		t.Fatalf("staged before concluding the merge = %q, want feat.txt", out)
	}

	// The banner tells the user the merge is ready to conclude, on both
	// pages, and it never offers Skip.
	if !hasExactText(h.ui, "Merge in progress. All conflicts resolved; continue to commit the merge, or abort.") {
		t.Fatalf("merge banner copy missing; texts=%q", h.ui.Texts())
	}
	assertMergeBannerButtons(t, h)
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Merge in progress. All conflicts resolved; continue to commit the merge, or abort.") {
		t.Fatalf("merge banner missing on Local Changes; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Continue Operation"); !ok || btn.Disabled {
		t.Fatalf("Continue Operation for an unconflicted merge = %+v found=%v, want present and enabled", btn, ok)
	}

	if err := h.ui.Click("Continue Operation"); err != nil {
		t.Fatalf("click Continue Operation: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("continue of the merge failed: %v", err)
	}
	parents := parentsOf(t, repo.Path(), "HEAD")
	if len(parents) != 2 || parents[0] != mainTip || parents[1] != feature {
		t.Fatalf("merge commit parents after continue = %v, want [%q %q]", parents, mainTip, feature)
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); subject != "Merge branch 'feature'" {
		t.Fatalf("merge commit subject = %q, want Merge branch 'feature'", subject)
	}
	if refExists(t, repo.Path(), "MERGE_HEAD") {
		t.Fatal("MERGE_HEAD survived continue")
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after continue = %q, want empty", got)
	}
	mergeBannerGone(t, h)
}

// Aborting a merge that stopped before committing discards the staged merge
// result and restores the pre-merge state: MERGE_HEAD gone, HEAD back, the
// working tree exactly as before.
func TestMergeNoCommitAbortFromBanner(t *testing.T) {
	repo := newGitRepo(t, "merge-nocommit-abort")
	mergeDivergedFixture(t, repo)
	mainTip := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openMergeDialogFromBranch(t, h, "feature")
	if err := h.ui.Click("Don't Commit"); err != nil {
		t.Fatalf("click Don't Commit radio: %v", err)
	}
	h.ui.Frame()
	confirmMerge(t, h)
	if got := h.app.session.Snapshot().Status.State; got != "merge" {
		t.Fatalf("fixture: state = %q, want merge", got)
	}

	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Abort merge?") {
		t.Fatalf("abort confirmation title missing; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "All uncommitted changes from this merge will be discarded.") {
		t.Fatalf("merge abort warning missing; texts=%q", h.ui.Texts())
	}
	assertDialogLayout(t, h, "Cancel", "Confirm Abort")
	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("abort of the merge failed: %v", err)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD after abort = %q, want pre-merge %q", head, mainTip)
	}
	if refExists(t, repo.Path(), "MERGE_HEAD") {
		t.Fatal("MERGE_HEAD survived abort")
	}
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("working tree dirty after abort: %q", out)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "feat.txt")); !os.IsNotExist(err) {
		t.Fatalf("feat.txt of the aborted merge left behind: err=%v", err)
	}
	mergeBannerGone(t, h)
}

// On a fast-forwardable merge the Don't Commit mode follows Git's native
// semantics: the fast-forward still happens, the branch moves, and there is
// no merge in progress afterwards.
func TestMergeNoCommitFastForwardMovesBranch(t *testing.T) {
	repo := newGitRepo(t, "merge-nocommit-ff")
	_, feature := mergeFFFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openMergeDialogFromBranch(t, h, "feature")
	if err := h.ui.Click("Don't Commit"); err != nil {
		t.Fatalf("click Don't Commit radio: %v", err)
	}
	h.ui.Frame()
	confirmMerge(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("no-commit fast-forward merge failed: %v", err)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != feature {
		t.Fatalf("HEAD after no-commit fast-forward = %q, want feature tip %q", head, feature)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after no-commit fast-forward = %q, want empty", got)
	}
	mergeBannerGone(t, h)
}

// A remote-tracking branch of the sidebar offers the same merge, keyed by
// its remote name so the merge commit message names it, and the merge runs
// against the hash the branch pointed at.
func TestMergeFromRemoteBranchMenu(t *testing.T) {
	repo := newGitRepo(t, "merge-remote")
	base, feature := mergeFFFixture(t, repo)
	repo.AddBareRemote("origin")
	repo.Git("push", "-q", "origin", "feature")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "refs/remotes/origin/feature"); got != feature {
		t.Fatalf("fixture: origin/feature = %q, want %q", got, feature)
	}

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	if _, ok := h.ui.Find("origin/feature"); !ok {
		t.Fatalf("origin/feature missing from the sidebar; texts=%q", h.ui.Texts())
	}

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click remote branch row: %v\ntexts=%q", err, h.ui.Texts())
	}
	if !slices.Contains(h.ui.Menu(), "Merge into 'main'…") {
		t.Fatalf("remote branch menu %q missing the merge item", h.ui.Menu())
	}
	chooseMergeItem(t, h)
	h.drain()
	if err := h.ui.Click("No Fast-Forward"); err != nil {
		t.Fatalf("click No Fast-Forward radio: %v", err)
	}
	h.ui.Frame()
	confirmMerge(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("merge of origin/feature failed: %v", err)
	}
	parents := parentsOf(t, repo.Path(), "HEAD")
	if len(parents) != 2 || parents[0] != base || parents[1] != feature {
		t.Fatalf("merge commit parents = %v, want [%q %q]", parents, base, feature)
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); !strings.Contains(subject, "feature") {
		t.Fatalf("merge commit subject = %q, want it to name the merged branch", subject)
	}
	assertFileBytes(t, repo, "feat.txt", "feat\n")
}

// A commit row of the History list offers a merge into the checked out
// branch: the dialog names the commit, and the merge runs against the
// commit's hash — here a commit main does not contain, so the merge is a
// real one. On the HEAD commit row the item is disabled because merging HEAD
// into itself can never change anything.
func TestMergeFromCommitMenu(t *testing.T) {
	repo := newGitRepo(t, "merge-commit")
	_, feature, mainTip := mergeDivergedFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// The merge item of the HEAD commit row is disabled.
	headRow := commitRowLabel(mainTip, "main work")
	if err := h.ui.RightClick(headRow); err != nil {
		t.Fatalf("right-click HEAD commit row: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Merge into 'main'…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing merge of the HEAD commit = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()

	openMergeDialogFromCommit(t, h, feature, "feature work")
	if !hasExactText(h.ui, short7(feature)+" feature work") {
		t.Fatalf("dialog missing source commit line; texts=%q", h.ui.Texts())
	}
	confirmMerge(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("merge of the commit failed: %v", err)
	}
	parents := parentsOf(t, repo.Path(), "HEAD")
	if len(parents) != 2 || parents[0] != mainTip || parents[1] != feature {
		t.Fatalf("merge commit parents = %v, want [%q %q]", parents, mainTip, feature)
	}
	assertFileBytes(t, repo, "feat.txt", "feat\n")
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); !strings.HasPrefix(subject, "Merge commit '") {
		t.Fatalf("merge commit subject = %q, want it to name the commit", subject)
	}
}

// The conflict prediction of the dialog warns before the user commits to the
// merge: a merge of a branch rewriting the same line predicts conflicts in
// warning color, while the fast-forwardable branch predicts a clean merge —
// and neither blocks the merge itself.
func TestMergeDialogConflictPrediction(t *testing.T) {
	repo := newGitRepo(t, "merge-predict")
	mergePredictProbe(t, repo)
	mergeConflictFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// feature conflicts with main: the prediction warns in the dialog.
	openMergeDialogFromBranch(t, h, "feature")
	if !hasExactText(h.ui, "Merge will cause conflicts") {
		t.Fatalf("conflict prediction missing; texts=%q", h.ui.Texts())
	}
	if hasExactText(h.ui, "Merge can be done without conflicts") {
		t.Fatal("clean prediction shown for a conflicting merge")
	}
	// The prediction never blocks the merge itself.
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Merge"); !ok || btn.Disabled {
		t.Fatalf("Confirm Merge = %+v found=%v, want present and enabled despite the prediction", btn, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	// The clean branch predicts a quiet merge.
	repo.CheckoutNewBranch("ahead")
	repo.CommitFile("ahead.txt", "ahead\n", "ahead work")
	repo.Checkout("main")
	h.app.session.Reload()
	h.drain()
	openMergeDialogFromBranch(t, h, "ahead")
	if !hasExactText(h.ui, "Merge can be done without conflicts") {
		t.Fatalf("clean prediction missing; texts=%q", h.ui.Texts())
	}
	if hasExactText(h.ui, "Merge will cause conflicts") {
		t.Fatal("conflict prediction shown for a clean merge")
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel again: %v", err)
	}
	h.ui.Frame()
}

// A git too old for merge-tree --write-tree cannot predict conflicts: the
// prediction line hides entirely and the merge still runs, because a missing
// prediction must never block the operation.
func TestMergeDialogHidesPredictionWhenUnavailable(t *testing.T) {
	repo := newGitRepo(t, "merge-predict-old")
	_, feature := mergeFFFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	isMergeTree := func(args []string) bool { return len(args) > 0 && args[0] == "merge-tree" }
	installCommandFailExecutor(t, h, isMergeTree, &git.Error{Args: []string{"merge-tree"}, ExitCode: 129, Stderr: "error: unknown option `write-tree'"})

	openMergeDialogFromBranch(t, h, "feature")
	if hasExactText(h.ui, "Merge will cause conflicts") || hasExactText(h.ui, "Merge can be done without conflicts") {
		t.Fatalf("prediction shown despite the unavailable predictor; texts=%q", h.ui.Texts())
	}
	confirmMerge(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("merge with an unavailable predictor failed: %v", err)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != feature {
		t.Fatalf("HEAD after merge with an unavailable predictor = %q, want %q", head, feature)
	}
}

// Canceling the dialog — after switching modes — writes nothing and clears
// the draft: HEAD, the index and the working tree are exactly as before, and
// reopening starts again at the default mode.
func TestMergeCancelLeavesRepositoryUntouched(t *testing.T) {
	repo := newGitRepo(t, "merge-cancel")
	mergeFFFixture(t, repo)
	mainTip := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openMergeDialogFromBranch(t, h, "feature")
	if err := h.ui.Click("Squash"); err != nil {
		t.Fatalf("click Squash radio: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	mergeDialogGone(t, h)
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD moved by a canceled dialog: %q, want %q", head, mainTip)
	}
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("canceled dialog dirtied the working tree: %q", out)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Cancel produced an action error: %v", err)
	}

	// Reopening starts from a clean draft at the default mode.
	openMergeDialogFromBranch(t, h, "feature")
	if radio, ok := h.ui.AccessByRole(ui.RoleRadio, "Fast-forward if possible"); !ok || !radio.Checked {
		t.Fatalf("default mode after reopen = %+v found=%v, want the checked default", radio, ok)
	}
	if radio, ok := h.ui.AccessByRole(ui.RoleRadio, "Squash"); !ok || radio.Checked {
		t.Fatalf("Squash after reopen = %+v found=%v, want present and unchecked", radio, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel again: %v", err)
	}
	h.ui.Frame()
}

// Switching the repository closes an open merge dialog and clears its draft:
// the captured source, HEAD and branch belong to the repository that is gone.
func TestMergeDialogClosesOnRepoSwitch(t *testing.T) {
	repoA := newGitRepo(t, "merge-switch-a")
	mergeFFFixture(t, repoA)
	repoB := newGitRepo(t, "merge-switch-b")
	repoB.CommitFile("b.txt", "b\n", "commit in beta")

	h := newTestApp(t, repoB.StoreRepo())
	openHistoryPage(t, h, repoA.StoreRepo())
	openMergeDialogFromBranch(t, h, "feature")

	saved, ok := h.store.Get(h.store.Repos()[0].ID)
	if !ok {
		t.Fatal("seeded repository missing from the store")
	}
	h.app.openRepo(saved)
	h.ui.Frame()
	h.drain()

	st := mergeUI(h.app)
	if st.open {
		t.Fatal("merge dialog still open after the repository switch")
	}
	if st.sourceRev != "" || st.expectedHead != "" || st.expectedBranch != "" {
		t.Fatalf("merge draft survived the switch: %+v", st)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Merge"); ok {
		t.Fatal("merge dialog rendered again after the repository switch")
	}
}

// While a merge is in progress every merge menu item is disabled: starting a
// second merge on top of the first would corrupt the operation.
func TestMergeMenuItemsDisabledDuringOperation(t *testing.T) {
	repo := newGitRepo(t, "merge-disabled-op")
	_, mainTip := mergeConflictFixture(t, repo)
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "feature"); err == nil {
		t.Fatalf("conflicting merge unexpectedly succeeded: %s", out)
	}

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	if got := h.app.session.Snapshot().Status.State; got != "merge" {
		t.Fatalf("fixture: state = %q, want merge", got)
	}

	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click branch row: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Merge into 'main'…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing the branch merge item during a merge = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()

	row := commitRowLabel(mainTip, "main edits")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Merge into 'main'…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing the commit merge item during a merge = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Merge"); ok {
		t.Fatal("merge dialog opened from a disabled menu item")
	}
}

// While a repository write is busy the merge menu items are disabled, so a
// merge cannot start over the running write.
func TestMergeMenuItemsDisabledWhileBusy(t *testing.T) {
	repo := newGitRepo(t, "merge-busy")
	mergeFFFixture(t, repo)
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
		t.Fatalf("right-click branch row while busy: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Merge into 'main'…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing the branch merge item while busy = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage write failed: %v", err)
	}
}

// Uncommitted tracked changes — unstaged or staged — refuse the merge before
// Git runs: the error explains the refusal in the toolbar, and every byte of
// the working tree, the index and HEAD is exactly as before.
func TestMergeGuardDirtyWorkingTree(t *testing.T) {
	repo := newGitRepo(t, "merge-guard-dirty")
	mergeFFFixture(t, repo)
	mainTip := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// Round 1: an unstaged tracked modification refuses the merge.
	repo.WriteFile("base.txt", "base\ndirty\n")
	h.app.session.Reload()
	h.drain()
	openMergeDialogFromBranch(t, h, "feature")
	confirmMerge(t, h)

	assertRepoErrorShown(t, h, "cannot merge with uncommitted local changes")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD moved by the refused merge: %q, want %q", head, mainTip)
	}
	assertFileBytes(t, repo, "base.txt", "base\ndirty\n")
	if _, err := os.Stat(filepath.Join(repo.Path(), "feat.txt")); err == nil {
		t.Fatal("refused merge still created feat.txt")
	}
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); out != "" {
		t.Fatalf("index touched by the refused merge: %q", out)
	}

	// Round 2: a staged change refuses the merge the same way.
	repo.Git("checkout", "--", "base.txt")
	repo.WriteFile("base.txt", "base\nstaged\n")
	repo.Git("add", "--", "base.txt")
	h.app.session.Reload()
	h.drain()
	openMergeDialogFromBranch(t, h, "feature")
	confirmMerge(t, h)

	assertRepoErrorShown(t, h, "cannot merge with uncommitted local changes")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD moved by the refused merge: %q, want %q", head, mainTip)
	}
	assertFileBytes(t, repo, "base.txt", "base\nstaged\n")
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); !strings.Contains(out, "base.txt") {
		t.Fatalf("staged change lost by the refused merge: %q", out)
	}
}

// When the background guard cannot even read the repository state, the merge
// never runs: HEAD, the index and every byte of the working tree are exactly
// as before, and the failure shows in the toolbar.
func TestMergeGuardStatusReadFailure(t *testing.T) {
	repo := newGitRepo(t, "merge-guard-status")
	mergeFFFixture(t, repo)
	mainTip := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openMergeDialogFromBranch(t, h, "feature")

	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	confirmMerge(t, h)

	assertRepoErrorShown(t, h, "reading the repository state failed")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD moved after a failed status read: %q, want %q", head, mainTip)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "feat.txt")); err == nil {
		t.Fatal("merge ran despite the failed status read")
	}
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("working tree touched after a failed status read: %q", out)
	}
	if count := gitOutTrim(t, repo.Path(), "rev-list", "--count", "HEAD"); count != "1" {
		t.Fatalf("commit count after failed status read = %q, want 1", count)
	}
}

// When HEAD moved after the dialog opened — here an external commit — the
// guard refuses the merge the user confirmed against the old state.
func TestMergeGuardHeadMoved(t *testing.T) {
	repo := newGitRepo(t, "merge-guard-head")
	mergeFFFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openMergeDialogFromBranch(t, h, "feature")

	repo.WriteFile("other.txt", "other\n")
	repo.Git("add", "--", "other.txt")
	repo.Git("commit", "-m", "external commit")
	moved := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	confirmMerge(t, h)

	assertRepoErrorShown(t, h, "HEAD or the current branch changed")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != moved {
		t.Fatalf("HEAD moved by the refused merge: %q, want %q", head, moved)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "feat.txt")); err == nil {
		t.Fatal("merge ran despite the moved HEAD")
	}
}

// When the branch itself changed after the dialog opened, the guard refuses
// the merge even though HEAD's hash did not move.
func TestMergeGuardBranchSwitched(t *testing.T) {
	repo := newGitRepo(t, "merge-guard-branch")
	mergeFFFixture(t, repo)
	mainTip := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	repo.CreateBranch("side")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openMergeDialogFromBranch(t, h, "feature")

	repo.Checkout("side")

	confirmMerge(t, h)

	assertRepoErrorShown(t, h, "HEAD or the current branch changed")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD moved by the refused merge: %q, want %q", head, mainTip)
	}
	if got := gitOutTrim(t, repo.Path(), "symbolic-ref", "--short", "HEAD"); got != "side" {
		t.Fatalf("branch after the refused merge = %q, want side", got)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "feat.txt")); err == nil {
		t.Fatal("merge ran despite the branch switch")
	}
}

// When the merge source moved after the dialog opened — here the feature
// branch gained a commit — the guard refuses to merge a commit the user
// never saw, even though HEAD and the branch are exactly as captured.
func TestMergeGuardMergeTargetChanged(t *testing.T) {
	repo := newGitRepo(t, "merge-guard-target")
	mergeFFFixture(t, repo)
	mainTip := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openMergeDialogFromBranch(t, h, "feature")

	// An external actor advances the feature branch without touching main:
	// HEAD and the branch are exactly as captured, only the source moved.
	repo.Checkout("feature")
	repo.WriteFile("more.txt", "more\n")
	repo.Git("add", "--", "more.txt")
	repo.Git("commit", "-m", "more feature work")
	repo.Checkout("main")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != mainTip {
		t.Fatalf("fixture: HEAD moved while advancing feature: %q", got)
	}

	confirmMerge(t, h)

	assertRepoErrorShown(t, h, "merge target changed after dialog opened")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD moved by the refused merge: %q, want %q", head, mainTip)
	}
	if count := gitOutTrim(t, repo.Path(), "rev-list", "--count", "HEAD"); count != "1" {
		t.Fatalf("commit count after the refused merge = %q, want 1", count)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "more.txt")); err == nil {
		t.Fatal("merge ran despite the moved merge target")
	}

	// Round 2: the target branch deleted outright after the dialog opened
	// refuses the merge the same way — the source no longer resolves at all.
	h.app.session.Reload()
	h.drain()
	openMergeDialogFromBranch(t, h, "feature")
	repo.Git("branch", "-D", "feature")
	confirmMerge(t, h)

	assertRepoErrorShown(t, h, "no longer resolves to a commit")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD moved by the refused merge: %q, want %q", head, mainTip)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "more.txt")); err == nil {
		t.Fatal("merge ran despite the deleted merge target")
	}
}

// When an operation started after the dialog opened, the guard refuses the
// merge before Git runs: merging into an operation in progress would corrupt
// it.
func TestMergeGuardOperationInProgress(t *testing.T) {
	repo := newGitRepo(t, "merge-guard-op")
	mergeConflictFixture(t, repo)
	mainTip := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openMergeDialogFromBranch(t, h, "feature")

	// An external actor starts a conflicting merge after the dialog opened:
	// it does not move HEAD and it does not advance feature, so only the
	// in-progress check of the guard can refuse the user's merge.
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "feature"); err == nil {
		t.Fatalf("external merge unexpectedly clean: %s", out)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != mainTip {
		t.Fatalf("fixture: HEAD = %q, want %q", got, mainTip)
	}
	if !refExists(t, repo.Path(), "MERGE_HEAD") {
		t.Fatal("fixture: MERGE_HEAD missing for the external merge")
	}

	confirmMerge(t, h)

	assertRepoErrorShown(t, h, "is in progress")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD moved by the refused merge: %q, want %q", head, mainTip)
	}
	if !refExists(t, repo.Path(), "MERGE_HEAD") {
		t.Fatal("the merge in progress was destroyed by the refused merge")
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("conflict stages of the merge in progress lost by the refused merge")
	}
	repo.Git("merge", "--abort")
}

// An untracked file sharing the name of a file the merge brings in makes Git
// refuse to overwrite it: the error surfaces, and the untracked file's bytes
// are exactly as before. Untracked files are not uncommitted tracked
// changes, so the guard passes the write to Git and Git itself is the second
// line of defense.
func TestMergeGuardUntrackedCollision(t *testing.T) {
	repo := newGitRepo(t, "merge-guard-collision")
	mergeFFFixture(t, repo)
	mainTip := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	repo.WriteFile("feat.txt", "precious untracked\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openMergeDialogFromBranch(t, h, "feature")
	confirmMerge(t, h)

	assertRepoErrorShown(t, h, "")
	assertFileBytes(t, repo, "feat.txt", "precious untracked\n")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD moved by the colliding merge: %q, want %q", head, mainTip)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after collision = %q, want empty", got)
	}
}

// The guard itself refuses malformed inputs before any repository read: an
// empty or option-like source revision, a merge of a branch into itself, an
// unborn repository and an unknown merge type never reach Git.
func TestMergeGuardRejectsInvalidInputs(t *testing.T) {
	repo := newGitRepo(t, "merge-guard-inputs")
	mergeFFFixture(t, repo)
	r := git.New(repo.StoreRepo().Executor())
	st, err := r.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	cases := []struct {
		name             string
		head, branch     string
		source, expected string
		style            git.MergeStyle
		want             string
	}{
		{"empty source", st.Head, st.Branch, "", "x", git.MergeFastForward, "no merge source"},
		{"dash source", st.Head, st.Branch, "--delete", "x", git.MergeFastForward, "invalid source"},
		{"newline source", st.Head, st.Branch, "a\nb", "x", git.MergeFastForward, "invalid source"},
		{"unborn head", "", st.Branch, "feature", "x", git.MergeFastForward, "no commits"},
		{"self merge", st.Head, "feature", "feature", "x", git.MergeFastForward, "into itself"},
		{"unknown type", st.Head, st.Branch, "feature", "x", git.MergeStyle(42), "unknown merge type"},
	}
	for _, tc := range cases {
		err := mergeWithGuard(r, tc.head, tc.branch, tc.source, tc.expected, tc.style)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: mergeWithGuard error = %v, want it to contain %q", tc.name, err, tc.want)
		}
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != st.Head {
		t.Fatalf("HEAD moved by the refused merges: %q, want %q", head, st.Head)
	}
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("working tree touched by the refused merges: %q", out)
	}
}

// A conflicting merge through the branch sidebar shows the banner on both
// the History and the Local Changes pages, never offers Skip, and canceling
// the Abort confirmation preserves the operation exactly as it was.
func TestMergeConflictBannerAndAbortCancel(t *testing.T) {
	repo := newGitRepo(t, "merge-conflict-cancel")
	mergeConflictFixture(t, repo)
	mainTip := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	startMergeConflict(t, h)
	assertMergeBannerLayoutNarrow(t, h)

	// The banner is on the Local Changes page too, where the conflicted file
	// is listed for resolution.
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Merge in progress. Resolve conflicts and stage the files to continue, or abort.") {
		t.Fatalf("merge banner missing on Local Changes; texts=%q", h.ui.Texts())
	}
	assertMergeBannerButtons(t, h)
	assertMergeBannerLayoutNarrow(t, h)
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, "Conflicted f.txt"); !ok {
		t.Fatalf("conflicted f.txt not listed on Local Changes; texts=%q", h.ui.Texts())
	}

	// Open the Abort confirmation here and cancel it: the operation, the
	// conflict and the banner all survive.
	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Abort merge?") {
		t.Fatalf("abort confirmation missing; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "All uncommitted changes from this merge will be discarded.") {
		t.Fatalf("abort warning missing; texts=%q", h.ui.Texts())
	}
	assertDialogLayout(t, h, "Cancel", "Confirm Abort")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	if hasExactText(h.ui, "Abort merge?") {
		t.Fatal("abort confirmation still open after Cancel")
	}
	st := h.app.session.Snapshot().Status
	if st.State != "merge" || !st.Conflicted() {
		t.Fatalf("Cancel disturbed the operation: state=%q conflicted=%v", st.State, st.Conflicted())
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("Cancel resolved the conflict stages")
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt")); err != nil || !strings.Contains(string(content), "<<<<<<<") {
		t.Fatalf("Cancel touched the conflicted file: content=%q err=%v", content, err)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD moved by the canceled abort: %q, want %q", head, mainTip)
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("no action error recorded for the conflicting merge the banner came from")
	}
}

// When the recovery guard cannot read the repository state, the abort never
// runs: the operation, the conflict stages and the conflicted file's bytes
// are exactly as before, and the failure shows in the toolbar.
func TestMergeConflictAbortGuardStatusFailure(t *testing.T) {
	repo := newGitRepo(t, "merge-abort-status")
	mergeConflictFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	startMergeConflict(t, h)

	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "reading the repository state failed")
	st := h.app.session.Snapshot().Status
	if st.State != "merge" {
		t.Fatalf("operation lost after a failed status read: state=%q", st.State)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("conflict stages lost after a failed status read")
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt")); err != nil || !strings.Contains(string(content), "<<<<<<<") {
		t.Fatalf("conflicted file touched after a failed status read: content=%q err=%v", content, err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); strings.TrimSpace(got) == "" {
		t.Fatal("MERGE_HEAD lost after a failed status read")
	}
}

// When the operation changed underneath the open confirmation — here the
// merge was aborted externally and another conflicting merge of the same kind
// on the same HEAD started — the guard refuses the abort: the replacement
// operation is not the one the user confirmed against, so it and its conflict
// survive untouched. Only the merge target differs, so exactly the operation
// identity check of the guard catches it.
func TestMergeConflictAbortGuardOperationChanged(t *testing.T) {
	repo := newGitRepo(t, "merge-abort-changed")
	mergeConflictFixture(t, repo)
	mainTip := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	// A second branch that conflicts the same way, for the replacement merge.
	repo.Git("branch", "other", "feature")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	startMergeConflict(t, h)

	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()

	// An external actor aborts the merge the user saw and starts another one
	// of the same kind on the same HEAD: only the target differs.
	repo.Git("merge", "--abort")
	repo.Checkout("other")
	repo.WriteFile("f.txt", "other-edit\n")
	repo.Git("add", "--", "f.txt")
	repo.Git("commit", "-q", "-m", "other edits")
	otherTip := gitOutTrim(t, repo.Path(), "rev-parse", "other")
	repo.Checkout("main")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "other"); err == nil {
		t.Fatalf("replacement merge unexpectedly clean: %s", out)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != mainTip {
		t.Fatalf("fixture: HEAD = %q, want the captured %q", got, mainTip)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); strings.TrimSpace(got) != otherTip {
		t.Fatalf("fixture: replacement MERGE_HEAD = %q, want %q", got, otherTip)
	}

	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "active operation changed")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); strings.TrimSpace(got) != otherTip {
		t.Fatalf("replacement operation destroyed by the refused abort: MERGE_HEAD=%q want %q", got, otherTip)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("replacement conflict resolved by the refused abort")
	}
}

// Confirming the Abort restores the repository to the state before the
// merge: HEAD back at the pre-merge commit, MERGE_HEAD gone, the working
// tree clean and the banner gone.
func TestMergeConflictAbortConfirmRestores(t *testing.T) {
	repo := newGitRepo(t, "merge-conflict-abort")
	mergeConflictFixture(t, repo)
	mainTip := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	startMergeConflict(t, h)

	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("abort failed: %v", err)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after abort = %q, want empty", got)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD after abort = %q, want pre-merge %q", head, mainTip)
	}
	assertFileBytes(t, repo, "f.txt", "ours\n")
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("working tree dirty after abort: %q", out)
	}
	mergeBannerGone(t, h)
}

// After the user resolves the conflict on disk and stages the file through
// the real Local Changes UI, Continue enables and concludes the merge: a
// two-parent commit lands with the merge message, the resolution is its
// content, and the banner is gone.
func TestMergeConflictContinueAfterResolution(t *testing.T) {
	repo := newGitRepo(t, "merge-conflict-continue")
	feature, mainTip := mergeConflictFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	startMergeConflict(t, h)

	// Resolve on disk, then stage through the real UI entry.
	repo.WriteFile("f.txt", "resolved\n")
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Mark Resolved f.txt"); err != nil {
		t.Fatalf("click Mark Resolved f.txt: %v", err)
	}
	h.drain()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Continue Operation"); !ok || btn.Disabled {
		t.Fatalf("Continue Operation after staging = %+v found=%v, want enabled", btn, ok)
	}

	if err := h.ui.Click("Continue Operation"); err != nil {
		t.Fatalf("click Continue Operation: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("continue failed: %v", err)
	}
	parents := parentsOf(t, repo.Path(), "HEAD")
	if len(parents) != 2 || parents[0] != mainTip || parents[1] != feature {
		t.Fatalf("merge commit parents after continue = %v, want [%q %q]", parents, mainTip, feature)
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); subject != "Merge branch 'feature'" {
		t.Fatalf("merge commit subject = %q, want Merge branch 'feature'", subject)
	}
	assertFileBytes(t, repo, "f.txt", "resolved\n")
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after continue = %q, want empty", got)
	}
	mergeBannerGone(t, h)
}

// On a detached HEAD the merge menu names HEAD rather than a branch, the
// dialog says the merge goes into the detached HEAD, and the merge still
// runs, moving the detached checkout forward.
func TestMergeIntoDetachedHead(t *testing.T) {
	repo := newGitRepo(t, "merge-detached")
	_, feature := mergeFFFixture(t, repo)
	mainTip := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	repo.Git("checkout", "--detach", mainTip)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	if got := h.app.session.Snapshot().Status.Branch; got != "" {
		t.Fatalf("fixture: branch = %q, want detached", got)
	}

	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click branch row: %v", err)
	}
	if !slices.Contains(h.ui.Menu(), "Merge into HEAD…") {
		t.Fatalf("menu %q missing the detached merge item", h.ui.Menu())
	}
	chooseMergeItem(t, h)
	h.drain()
	if !hasExactText(h.ui, "HEAD (detached)") {
		t.Fatalf("dialog missing the detached into line; texts=%q", h.ui.Texts())
	}
	confirmMerge(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("merge into a detached HEAD failed: %v", err)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != feature {
		t.Fatalf("detached HEAD after merge = %q, want %q", head, feature)
	}
	if out, err := tryGitCommand(repo.Path(), "symbolic-ref", "-q", "HEAD"); err == nil {
		t.Fatalf("checkout no longer detached after the merge: %q", out)
	}
}

// --- SSH loopback ---

// remoteMergeFF builds the fast-forwardable fixture on the server: feature
// gains one commit ahead of main.
func remoteMergeFF(t *testing.T, srv *sshtest.Target, dir string) (feature string) {
	t.Helper()
	srv.RunGit(t, dir, "checkout", "-q", "-b", "feature")
	feature = remoteCommitFile(t, srv, dir, "feat.txt", "feat\n", "feature work")
	srv.RunGit(t, dir, "checkout", "-q", "main")
	return feature
}

// remoteMergeConflict builds the conflict fixture on the server: f.txt
// "line" → feature "theirs", main "ours".
func remoteMergeConflict(t *testing.T, srv *sshtest.Target, dir string) (feature string) {
	t.Helper()
	srv.WriteFile(t, dir, "f.txt", []byte("line\n"))
	srv.RunGit(t, dir, "add", "--", "f.txt")
	srv.RunGit(t, dir, "commit", "-q", "-m", "base commit")
	srv.RunGit(t, dir, "checkout", "-q", "-b", "feature")
	feature = remoteCommitFile(t, srv, dir, "f.txt", "theirs\n", "feature edits")
	srv.RunGit(t, dir, "checkout", "-q", "main")
	remoteCommitFile(t, srv, dir, "f.txt", "ours\n", "main edits")
	return feature
}

// Over a real SSH connection the sidebar entries trigger merges on the
// server: a local branch fast-forwards, and the remote-tracking branch of
// the sidebar merges with the same dialog. Every assertion reads the
// server's repository through a second SSH channel.
func TestMergeSidebarEntriesLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	feature := remoteMergeFF(t, srv, repo.Path)
	base := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "main"))

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)

	// A local branch fast-forward merge through the sidebar.
	openMergeDialogFromBranch(t, h, "feature")
	confirmMerge(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH fast-forward merge failed: %v", err)
	}
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); head != feature {
		t.Fatalf("remote HEAD after fast-forward merge = %q, want %q", head, feature)
	}
	if got := len(strings.Fields(srv.RunGit(t, repo.Path, "rev-list", "--parents", "-n", "1", "HEAD"))) - 1; got != 1 {
		t.Fatalf("remote fast-forwarded HEAD has %d parents, want 1", got)
	}

	// Wire origin on the server and publish feature for the remote entry.
	srv.RunGit(t, repo.Path, "reset", "--hard", "-q", base)
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repo.Path, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repo.Path, "push", "-q", "origin", "feature")
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "refs/remotes/origin/feature")); got != feature {
		t.Fatalf("fixture: origin/feature = %q, want %q", got, feature)
	}
	h.app.session.Reload()
	h.drain()
	if _, ok := h.ui.Find("origin/feature"); !ok {
		t.Fatalf("origin/feature missing from the sidebar over SSH; texts=%q", h.ui.Texts())
	}

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click remote branch row over SSH: %v\ntexts=%q", err, h.ui.Texts())
	}
	if !slices.Contains(h.ui.Menu(), "Merge into 'main'…") {
		t.Fatalf("remote branch menu %q missing the merge item over SSH", h.ui.Menu())
	}
	chooseMergeItem(t, h)
	h.drain()
	confirmMerge(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH merge of origin/feature failed: %v", err)
	}
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); head != feature {
		t.Fatalf("remote HEAD after merging origin/feature = %q, want %q", head, feature)
	}
	if got := srv.ReadFile(t, repo.Path, "feat.txt"); string(got) != "feat\n" {
		t.Fatalf("remote feat.txt after merge = %q, want feat", string(got))
	}
}

// Over SSH each of the four merge modes behaves like Git itself: fast-forward
// moves the branch, no-ff records a two-parent commit even when it could
// fast-forward, squash stages without committing or a banner, and don't
// commit stops before committing so the banner can conclude the merge.
func TestMergeModesLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)

	// Fast-forward if possible.
	feature := remoteMergeFF(t, srv, repo.Path)
	base := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "main"))
	h.app.session.Reload()
	h.drain()
	openMergeDialogFromBranch(t, h, "feature")
	confirmMerge(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH fast-forward merge failed: %v", err)
	}
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); head != feature {
		t.Fatalf("remote HEAD after fast-forward = %q, want %q", head, feature)
	}

	// No Fast-Forward on the same shape records a merge commit.
	srv.RunGit(t, repo.Path, "reset", "--hard", "-q", base)
	h.app.session.Reload()
	h.drain()
	openMergeDialogFromBranch(t, h, "feature")
	if err := h.ui.Click("No Fast-Forward"); err != nil {
		t.Fatalf("click No Fast-Forward radio: %v", err)
	}
	h.ui.Frame()
	confirmMerge(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH no-ff merge failed: %v", err)
	}
	fields := strings.Fields(srv.RunGit(t, repo.Path, "rev-list", "--parents", "-n", "1", "HEAD"))
	if len(fields) != 3 || fields[1] != base || fields[2] != feature {
		t.Fatalf("remote no-ff merge commit fields = %v, want parents [%q %q]", fields, base, feature)
	}
	if subject := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "-1", "--format=%s")); subject != "Merge branch 'feature'" {
		t.Fatalf("remote merge commit subject = %q, want Merge branch 'feature'", subject)
	}

	// Squash stages the merged files without moving HEAD or writing
	// MERGE_HEAD, and no operation banner appears.
	srv.RunGit(t, repo.Path, "reset", "--hard", "-q", base)
	h.app.session.Reload()
	h.drain()
	openMergeDialogFromBranch(t, h, "feature")
	if err := h.ui.Click("Squash"); err != nil {
		t.Fatalf("click Squash radio: %v", err)
	}
	h.ui.Frame()
	confirmMerge(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH squash merge failed: %v", err)
	}
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); head != base {
		t.Fatalf("remote HEAD moved by squash: %q, want %q", head, base)
	}
	if out := srv.RunGit(t, repo.Path, "diff", "--cached", "--name-only"); !strings.Contains(out, "feat.txt") {
		t.Fatalf("remote staged after squash = %q, want feat.txt", out)
	}
	if remoteRefExists(t, srv, repo.Path, "MERGE_HEAD") {
		t.Fatal("remote MERGE_HEAD written by squash")
	}
	mergeBannerGone(t, h)
	srv.RunGit(t, repo.Path, "reset", "--hard", "-q", base)
	h.app.session.Reload()
	h.drain()

	// Don't Commit stops a divergent merge before committing; the banner
	// concludes it as a two-parent commit.
	srv.RunGit(t, repo.Path, "checkout", "-q", "-b", "side2", "feature")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	remoteCommitFile(t, srv, repo.Path, "mainwork.txt", "main\n", "main work")
	mainTip := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	h.app.session.Reload()
	h.drain()
	openMergeDialogFromBranch(t, h, "side2")
	if err := h.ui.Click("Don't Commit"); err != nil {
		t.Fatalf("click Don't Commit radio: %v", err)
	}
	h.ui.Frame()
	confirmMerge(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH no-commit merge failed: %v", err)
	}
	if got := h.app.session.Snapshot().Status.State; got != "merge" {
		t.Fatalf("state after SSH no-commit merge = %q, want merge", got)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "--verify", "--quiet", "MERGE_HEAD")); got == "" {
		t.Fatal("remote MERGE_HEAD missing after the no-commit merge")
	}
	if !hasExactText(h.ui, "Merge in progress. All conflicts resolved; continue to commit the merge, or abort.") {
		t.Fatalf("SSH merge banner copy missing; texts=%q", h.ui.Texts())
	}
	assertMergeBannerButtons(t, h)
	if err := h.ui.Click("Continue Operation"); err != nil {
		t.Fatalf("click Continue Operation: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH continue of the merge failed: %v", err)
	}
	fields = strings.Fields(srv.RunGit(t, repo.Path, "rev-list", "--parents", "-n", "1", "HEAD"))
	if len(fields) != 3 || fields[1] != mainTip {
		t.Fatalf("remote merge commit after continue fields = %v, want first parent %q", fields, mainTip)
	}
	mergeBannerGone(t, h)
}

// Over SSH the data-safety guards protect the server repository the same as
// locally: a dirty working tree, a failed status read and a merge target
// that moved after the dialog opened each refuse the merge without touching
// a byte, and the error shows in the toolbar.
func TestMergeGuardsLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	remoteMergeFF(t, srv, repo.Path)
	mainTip := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)

	// A dirty working tree refuses the merge before Git runs.
	srv.WriteFile(t, repo.Path, "a.txt", []byte("dirty\n"))
	h.app.session.Reload()
	h.drain()
	openMergeDialogFromBranch(t, h, "feature")
	confirmMerge(t, h)
	assertRepoErrorShown(t, h, "cannot merge with uncommitted local changes")
	if got := srv.ReadFile(t, repo.Path, "a.txt"); string(got) != "dirty\n" {
		t.Fatalf("remote a.txt overwritten by refused merge: %q", string(got))
	}
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); head != mainTip {
		t.Fatalf("remote HEAD moved by refused merge: %q, want %q", head, mainTip)
	}

	// A failed status read refuses the merge before Git runs.
	srv.RunGit(t, repo.Path, "checkout", "--", "a.txt")
	h.app.session.Reload()
	h.drain()
	openMergeDialogFromBranch(t, h, "feature")
	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	confirmMerge(t, h)
	assertRepoErrorShown(t, h, "reading the repository state failed")
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); head != mainTip {
		t.Fatalf("remote HEAD moved after failed status read: %q, want %q", head, mainTip)
	}
	if out := srv.RunGit(t, repo.Path, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Fatalf("remote working tree touched after failed status read: %q", out)
	}

	// A merge target advanced after the dialog opened refuses the merge.
	openMergeDialogFromBranch(t, h, "feature")
	srv.RunGit(t, repo.Path, "checkout", "-q", "feature")
	remoteCommitFile(t, srv, repo.Path, "more.txt", "more\n", "more feature work")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	confirmMerge(t, h)
	assertRepoErrorShown(t, h, "merge target changed after dialog opened")
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); head != mainTip {
		t.Fatalf("remote HEAD moved by the refused merge: %q, want %q", head, mainTip)
	}
	if out := srv.RunGit(t, repo.Path, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Fatalf("remote working tree touched by the refused merge: %q", out)
	}
}

// Over SSH a conflicting merge shows the banner without Skip, canceling
// Abort keeps the operation, a failed status read and a replaced operation
// both refuse the abort without destroying the conflict, and confirming
// Abort restores the pre-merge state.
func TestMergeConflictAbortLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	remoteMergeConflict(t, srv, repo.Path)
	srv.RunGit(t, repo.Path, "branch", "other", "feature")
	mainTip := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)
	startMergeConflict(t, h)
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Skip Commit"); ok {
		t.Fatalf("SSH merge banner offered Skip Commit; texts=%q", h.ui.Texts())
	}

	// Canceling the Abort confirmation keeps the operation and the conflict.
	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Abort merge?") {
		t.Fatalf("abort confirmation missing over SSH; texts=%q", h.ui.Texts())
	}
	assertDialogLayout(t, h, "Cancel", "Confirm Abort")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "--verify", "--quiet", "MERGE_HEAD")); got == "" {
		t.Fatal("Cancel disturbed the operation: MERGE_HEAD gone")
	}
	if got := srv.ReadFile(t, repo.Path, "f.txt"); !strings.Contains(string(got), "<<<<<<<") {
		t.Fatalf("Cancel touched the conflicted file: %q", string(got))
	}

	// A failed status read refuses the abort without destroying the conflict.
	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()
	assertRepoErrorShown(t, h, "reading the repository state failed")
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "--verify", "--quiet", "MERGE_HEAD")); got == "" {
		t.Fatal("failed status read destroyed the operation: MERGE_HEAD gone")
	}

	// A replaced operation — the merge concluded externally and another
	// conflicting merge started — refuses the abort the user confirmed
	// against the original.
	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	srv.RunGit(t, repo.Path, "merge", "--abort")
	srv.RunGit(t, repo.Path, "checkout", "-q", "other")
	srv.WriteFile(t, repo.Path, "f.txt", []byte("other-edit\n"))
	srv.RunGit(t, repo.Path, "add", "--", "f.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "other edits")
	otherTip := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "other"))
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	// The replacement merge conflicts like the original; run it through the
	// shell because RunGit fails the test on the nonzero exit a conflict
	// produces.
	srv.RunShell(t, "GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -C "+git.ShellQuote(repo.Path)+" merge --no-edit other >/dev/null 2>&1 || true")
	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()
	assertRepoErrorShown(t, h, "active operation changed")
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "--verify", "--quiet", "MERGE_HEAD")); got != otherTip {
		t.Fatalf("replacement operation destroyed by refused abort: MERGE_HEAD=%q want %q", got, otherTip)
	}

	// Abort the replacement for real: HEAD, the working tree and the banner
	// all return to the pre-merge state.
	h.app.session.Reload()
	h.drain()
	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH abort failed: %v", err)
	}
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); head != mainTip {
		t.Fatalf("remote HEAD after abort = %q, want pre-merge %q", head, mainTip)
	}
	if got := srv.ReadFile(t, repo.Path, "f.txt"); string(got) != "ours\n" {
		t.Fatalf("remote f.txt after abort = %q, want ours", string(got))
	}
	if out := srv.RunGit(t, repo.Path, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Fatalf("remote working tree dirty after abort: %q", out)
	}
	mergeBannerGone(t, h)
}

// Over SSH the resolution flow concludes the merge: the conflicted file is
// resolved on the server, staged through the real Local Changes UI, and
// Continue finishes the merge as a two-parent commit. A conflicting squash
// shows its error and no banner.
func TestMergeConflictContinueLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	feature := remoteMergeConflict(t, srv, repo.Path)
	mainTip := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)
	startMergeConflict(t, h)

	srv.WriteFile(t, repo.Path, "f.txt", []byte("resolved\n"))
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Mark Resolved f.txt"); err != nil {
		t.Fatalf("click Mark Resolved f.txt: %v", err)
	}
	h.drain()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Continue Operation"); !ok || btn.Disabled {
		t.Fatalf("SSH Continue Operation after staging = %+v found=%v, want enabled", btn, ok)
	}
	if err := h.ui.Click("Continue Operation"); err != nil {
		t.Fatalf("click Continue Operation: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH continue failed: %v", err)
	}
	fields := strings.Fields(srv.RunGit(t, repo.Path, "rev-list", "--parents", "-n", "1", "HEAD"))
	if len(fields) != 3 || fields[1] != mainTip || fields[2] != feature {
		t.Fatalf("remote merge commit fields = %v, want parents [%q %q]", fields, mainTip, feature)
	}
	if subject := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "-1", "--format=%s")); subject != "Merge branch 'feature'" {
		t.Fatalf("remote merge commit subject = %q, want Merge branch 'feature'", subject)
	}
	if got := srv.ReadFile(t, repo.Path, "f.txt"); string(got) != "resolved\n" {
		t.Fatalf("remote f.txt after continue = %q, want resolved", string(got))
	}
	mergeBannerGone(t, h)
}
