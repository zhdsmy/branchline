package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
	"github.com/zhdsmy/branchline/internal/store"
)

// --- fixtures and helpers ---

// rebaseDivergedFixture builds a divergent history whose rebase applies
// cleanly: base.txt on the shared base commit, main.txt on main's tip and
// feature.txt on the feature branch checked out from the base. Rebasing
// feature onto main replays the feature commit without touching the same
// lines. It returns the feature tip and the main tip.
func rebaseDivergedFixture(t *testing.T, repo *gitRepoFixture) (featureTip, mainTip string) {
	t.Helper()
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CreateBranch("feature")
	mainTip = repo.CommitFile("main.txt", "main work\n", "main work")
	repo.Checkout("feature")
	featureTip = repo.CommitFile("feature.txt", "feature work\n", "feature work")
	return featureTip, mainTip
}

// rebaseConflictFixture builds a divergent history whose rebase conflicts:
// both branches rewrite the same line of f.txt, so replaying the feature
// commit onto main stops with a content conflict. It returns the feature tip
// and the main tip.
func rebaseConflictFixture(t *testing.T, repo *gitRepoFixture) (featureTip, mainTip string) {
	t.Helper()
	repo.CommitFile("f.txt", "line\n", "base commit")
	repo.CreateBranch("feature")
	mainTip = repo.CommitFile("f.txt", "main\n", "main edits")
	repo.Checkout("feature")
	featureTip = repo.CommitFile("f.txt", "feature\n", "feature edits")
	return featureTip, mainTip
}

// rebaseDependentFixture builds a feature branch with two commits where the
// mid branch points at the first one: mid is a dependent branch of a rebase
// onto main, because its tip is among the replayed commits. It returns the
// first feature commit (mid's tip), the feature tip and the main tip.
func rebaseDependentFixture(t *testing.T, repo *gitRepoFixture) (midTip, featureTip, mainTip string) {
	t.Helper()
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CreateBranch("feature")
	mainTip = repo.CommitFile("main.txt", "main work\n", "main work")
	repo.Checkout("feature")
	midTip = repo.CommitFile("f1.txt", "feature one\n", "feature one")
	repo.CreateBranch("mid")
	featureTip = repo.CommitFile("f2.txt", "feature two\n", "feature two")
	return midTip, featureTip, mainTip
}

// openRebaseDialogFromBranch right-clicks the local branch row of the
// sidebar, chooses its Rebase menu item, and verifies the dialog opened.
func openRebaseDialogFromBranch(t *testing.T, h *testHarness, branch string) {
	t.Helper()
	current := h.app.session.Snapshot().Status.Branch
	if err := h.ui.RightClick(branch); err != nil {
		t.Fatalf("right-click branch row %q: %v\ntexts=%q", branch, err, h.ui.Texts())
	}
	label := "Rebase '" + current + "' on '" + branch + "'…"
	if !slices.Contains(h.ui.Menu(), label) {
		t.Fatalf("menu %q missing %q", h.ui.Menu(), label)
	}
	if err := h.ui.ChooseMenuItem(label); err != nil {
		t.Fatalf("choose %q: %v", label, err)
	}
	h.ui.Frame()
	assertRebaseDialogOpen(t, h)
}

// openRebaseDialogFromCommit right-clicks the commit row of hash, chooses
// the Rebase menu item, and verifies the dialog opened.
func openRebaseDialogFromCommit(t *testing.T, h *testHarness, branch, hash, subject string) {
	t.Helper()
	row := commitRowLabel(hash, subject)
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row %q: %v\ntexts=%q", row, err, h.ui.Texts())
	}
	label := "Rebase '" + branch + "' to Here…"
	if !slices.Contains(h.ui.Menu(), label) {
		t.Fatalf("menu %q missing %q", h.ui.Menu(), label)
	}
	if err := h.ui.ChooseMenuItem(label); err != nil {
		t.Fatalf("choose %q: %v", label, err)
	}
	h.ui.Frame()
	assertRebaseDialogOpen(t, h)
}

func assertRebaseDialogOpen(t *testing.T, h *testHarness) {
	t.Helper()
	if !hasExactText(h.ui, "Replay commits from the current branch onto another base.") {
		t.Fatalf("rebase dialog did not open; texts=%q", h.ui.Texts())
	}
}

// assertRebaseDialogClosed verifies no part of the rebase dialog renders.
func assertRebaseDialogClosed(t *testing.T, h *testHarness) {
	t.Helper()
	if hasExactText(h.ui, "Replay commits from the current branch onto another base.") {
		t.Fatalf("rebase dialog still open; texts=%q", h.ui.Texts())
	}
}

// drainRebaseInspection waits for the dialog's pre-flight inspection to land
// and renders it.
func drainRebaseInspection(t *testing.T, h *testHarness) {
	t.Helper()
	h.drain()
	st := rebaseUI(h.app)
	if st.inspecting {
		t.Fatal("rebase inspection still in flight after drain")
	}
}

// assertCleanRebase verifies the replayed feature tip: its parent is the
// target tip, the branch points at a new commit, and the snapshot agrees.
func assertCleanRebase(t *testing.T, repo *gitRepoFixture, featureTip, targetTip, replayedSubject string) {
	t.Helper()
	newTip := gitOutTrim(t, repo.Path(), "rev-parse", "feature")
	if newTip == featureTip {
		t.Fatalf("feature did not move after rebase: %q", newTip)
	}
	if parent := gitOutTrim(t, repo.Path(), "rev-parse", "feature^"); parent != targetTip {
		t.Fatalf("replayed commit parent = %q, want target tip %q", parent, targetTip)
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s", "feature")); subject != replayedSubject {
		t.Fatalf("replayed commit subject = %q, want %q", subject, replayedSubject)
	}
	if branch := gitOutTrim(t, repo.Path(), "symbolic-ref", "--short", "HEAD"); branch != "feature" {
		t.Fatalf("checked out branch after rebase = %q, want feature", branch)
	}
}

// versionFakeExecutor wraps a real executor and fakes the answers of the
// commands that probe what the repository's Git can do: --version reports a
// fixed string or a fixed error, and merge-tree can fail the way a Git
// without --write-tree fails. Every other command passes through to the real
// executor operating on a real repository, so the capabilities the app
// verifies are exactly the faked ones while the rebase itself runs for real.
type versionFakeExecutor struct {
	real          git.Executor
	version       string
	versionErr    error
	failMergeTree bool
}

func (f *versionFakeExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if len(args) == 1 && args[0] == "--version" {
		if f.versionErr != nil {
			return nil, f.versionErr
		}
		if f.version != "" {
			return []byte(f.version), nil
		}
	}
	if f.failMergeTree && len(args) > 0 && args[0] == "merge-tree" {
		return nil, &git.Error{
			Args:     append([]string(nil), args...),
			ExitCode: 129,
			Stderr:   "error: unknown option `write-tree'\nusage: git merge-tree [-h] <branch1> <branch2>",
		}
	}
	return f.real.Run(ctx, stdin, args...)
}

func (f *versionFakeExecutor) Describe() string { return f.real.Describe() }

// installVersionFakeExecutor routes the app's repository construction through
// a versionFakeExecutor wrapping the executor the store would build.
func installVersionFakeExecutor(h *testHarness, fake *versionFakeExecutor) {
	h.app.openRepoFn = func(meta store.Repo) *git.Repo {
		fake.real = meta.Executor()
		return git.New(fake)
	}
}

// rebaseMergeSnapshot reads every regular file of the repository's
// rebase-merge directory, so a test can prove a refused action changed
// nothing of the interrupted rebase's machinery.
func rebaseMergeSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	rmDir := filepath.Join(dir, ".git", "rebase-merge")
	entries, err := os.ReadDir(rmDir)
	if err != nil {
		t.Fatalf("read rebase-merge: %v", err)
	}
	snap := map[string]string{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(rmDir, e.Name()))
		if err != nil {
			t.Fatalf("read rebase-merge/%s: %v", e.Name(), err)
		}
		snap[e.Name()] = string(content)
	}
	return snap
}

func assertRebaseMergeUnchanged(t *testing.T, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("rebase-merge file count changed: before %d after %d (%v vs %v)", len(before), len(after), sortedKeys(before), sortedKeys(after))
	}
	for name, content := range before {
		if after[name] != content {
			t.Fatalf("rebase-merge/%s changed by the refused action:\nbefore %q\nafter  %q", name, content, after[name])
		}
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// --- sidebar and menu entries ---

// A local branch row's context menu rebases the current branch onto that
// branch: the feature commit is replayed on top of the main tip, the branch
// pointer moves forward and the history stays linear. The refreshed snapshot
// shows the new HEAD.
func TestRebaseOntoLocalBranchThroughSidebar(t *testing.T) {
	repo := newGitRepo(t, "rebase-sidebar-local")
	featureTip, mainTip := rebaseDivergedFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)

	// The dialog names the current branch and the target, predicts no
	// conflicts, and — no dependent branches here — hides the update-refs
	// checkbox entirely.
	if !hasExactText(h.ui, "feature") {
		t.Fatalf("dialog missing current branch line; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "main") {
		t.Fatalf("dialog missing target line; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "Rebase can be done without conflicts") {
		t.Fatalf("clean prediction missing; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches"); ok {
		t.Fatal("update-refs checkbox rendered without dependent branches")
	}
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Stash and reapply local changes"); !ok || box.Checked {
		t.Fatalf("autostash checkbox = %+v found=%v, want present and unchecked", box, ok)
	}
	assertDialogLayout(t, h, "Cancel", "Confirm Rebase")

	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase failed: %v", err)
	}
	assertCleanRebase(t, repo, featureTip, mainTip, "feature work")
	assertFileBytes(t, repo, "feature.txt", "feature work\n")
	assertFileBytes(t, repo, "main.txt", "main work\n")
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after clean rebase = %q, want empty", got)
	}
	if head := h.app.session.Snapshot().Status.Head; head != gitOutTrim(t, repo.Path(), "rev-parse", "HEAD") {
		t.Fatalf("snapshot HEAD = %q, want refreshed %q", head, gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"))
	}
	assertRebaseDialogClosed(t, h)
}

// A remote-tracking branch row's context menu rebases the current branch
// onto the remote branch, replaying the feature commit on top of the remote
// tip.
func TestRebaseOntoRemoteBranchThroughSidebar(t *testing.T) {
	repo := newGitRepo(t, "rebase-sidebar-remote")
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CreateBranch("feature")
	mainTip := repo.CommitFile("main.txt", "main work\n", "main work")
	repo.AddBareRemote("origin")
	repo.Checkout("feature")
	featureTip := repo.CommitFile("feature.txt", "feature work\n", "feature work")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "origin/main"); got != mainTip {
		t.Fatalf("fixture: origin/main = %q, want %q", got, mainTip)
	}

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	if err := h.ui.RightClick("origin/main"); err != nil {
		t.Fatalf("right-click remote branch row: %v\ntexts=%q", err, h.ui.Texts())
	}
	label := "Rebase 'feature' on 'origin/main'…"
	if !slices.Contains(h.ui.Menu(), label) {
		t.Fatalf("menu %q missing %q", h.ui.Menu(), label)
	}
	if err := h.ui.ChooseMenuItem(label); err != nil {
		t.Fatalf("choose %q: %v", label, err)
	}
	h.ui.Frame()
	assertRebaseDialogOpen(t, h)
	if !hasExactText(h.ui, "origin/main") {
		t.Fatalf("dialog missing remote target line; texts=%q", h.ui.Texts())
	}
	drainRebaseInspection(t, h)
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase onto remote branch failed: %v", err)
	}
	assertCleanRebase(t, repo, featureTip, mainTip, "feature work")
}

// A commit row's context menu rebases the current branch so it replays on
// top of that commit: the menu item names the current branch, the dialog
// shows the commit, and the rebase replays the feature commit onto it. The
// item is disabled on the HEAD commit row, because rebasing onto HEAD itself
// can never apply.
func TestRebaseOntoCommitThroughHistory(t *testing.T) {
	repo := newGitRepo(t, "rebase-history")
	featureTip, mainTip := rebaseDivergedFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// Rebase to Here on the HEAD commit row is disabled.
	headRow := commitRowLabel(featureTip, "feature work")
	if err := h.ui.RightClick(headRow); err != nil {
		t.Fatalf("right-click HEAD commit row: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Rebase 'feature' to Here…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing Rebase on HEAD = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()

	openRebaseDialogFromCommit(t, h, "feature", mainTip, "main work")
	drainRebaseInspection(t, h)
	if !hasExactText(h.ui, short7(mainTip)+" main work") {
		t.Fatalf("dialog missing commit target line; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase onto commit failed: %v", err)
	}
	assertCleanRebase(t, repo, featureTip, mainTip, "feature work")
}

// The rebase items disable on the current branch itself, while an operation
// is in progress, and while a write is busy, because none of those states
// can host a rebase.
func TestRebaseMenuItemsDisabled(t *testing.T) {
	repo := newGitRepo(t, "rebase-menu-disabled")

	// A divergent conflict history ending on feature: main and feature both
	// rewrite c.txt after their shared base, so merging main into feature
	// conflicts.
	repo.CommitFile("c.txt", "base\n", "base commit")
	repo.CreateBranch("feature")
	repo.WriteFile("c.txt", "main\n")
	repo.Git("add", "--", "c.txt")
	repo.Git("commit", "-m", "main edits")
	mainTip := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.Checkout("feature")
	repo.CommitFile("c.txt", "feature\n", "feature edits")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// The current branch's own row offers no rebase onto itself.
	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click current branch row: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Rebase 'feature' on 'feature'…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing rebase onto self = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()

	// During an operation — here a conflicting merge — every rebase item is
	// disabled, on branch rows and on commit rows alike.
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "main"); err == nil {
		t.Fatalf("conflicting merge unexpectedly succeeded: %s", out)
	}
	h.app.session.Reload()
	h.drain()
	if got := h.app.session.Snapshot().Status.State; got != "merge" {
		t.Fatalf("fixture: state = %q, want merge", got)
	}

	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main row during merge: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Rebase 'feature' on 'main'…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing rebase during merge = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	row := commitRowLabel(mainTip, "main edits")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row during merge: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Rebase 'feature' to Here…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing commit rebase during merge = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	repo.Git("merge", "--abort")
	h.app.session.Reload()
	h.drain()

	// While a write is busy the items disable too.
	repo.WriteFile("pending.txt", "pending\n")
	h.app.session.Reload()
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
	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main row while busy: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Rebase 'feature' on 'main'…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing rebase while busy = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()

	// On a detached HEAD the menu still renders the detached-HEAD rebase
	// labels in disabled state rather than showing empty branch quotes.
	repo.Git("reset", "--hard", "-q")
	repo.Git("clean", "-fdq")
	repo.Git("checkout", "--detach", "-q", "HEAD")
	h.app.session.Reload()
	h.drain()
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History on detached HEAD: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main row on detached HEAD: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Rebase on 'main'…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing rebase on detached HEAD = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row on detached HEAD: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Rebase HEAD to Here…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing commit rebase on detached HEAD = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
}

// Canceling the dialog — after ticking both checkboxes — writes nothing and
// clears the draft, so reopening starts again at the defaults.
func TestRebaseDialogCancelLeavesRepositoryUntouched(t *testing.T) {
	repo := newGitRepo(t, "rebase-cancel")
	midTip, featureTip, _ := rebaseDependentFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)

	if err := h.ui.Click("Update dependent branches"); err != nil {
		t.Fatalf("tick update-refs checkbox: %v", err)
	}
	if err := h.ui.Click("Stash and reapply local changes"); err != nil {
		t.Fatalf("tick autostash checkbox: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "mid") {
		t.Fatalf("dependent branch mid not listed while ticked; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	assertRebaseDialogClosed(t, h)
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); head != featureTip {
		t.Fatalf("feature moved by a canceled dialog: %q, want %q", head, featureTip)
	}
	if mid := gitOutTrim(t, repo.Path(), "rev-parse", "mid"); mid != midTip {
		t.Fatalf("mid moved by a canceled dialog: %q, want %q", mid, midTip)
	}
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("canceled dialog dirtied the working tree: %q", out)
	}

	// Reopening starts from a clean draft: both boxes unchecked.
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches"); !ok || box.Checked {
		t.Fatalf("update-refs after reopen = %+v found=%v, want the unchecked default", box, ok)
	}
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Stash and reapply local changes"); !ok || box.Checked {
		t.Fatalf("autostash after reopen = %+v found=%v, want the unchecked default", box, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel again: %v", err)
	}
	h.ui.Frame()
}

// Switching the repository closes an open rebase dialog and clears its
// draft: the captured target and HEAD belong to the repository that is gone.
func TestRebaseDialogClosesOnRepoSwitch(t *testing.T) {
	repoA := newGitRepo(t, "rebase-switch-a")
	rebaseDivergedFixture(t, repoA)
	repoB := newGitRepo(t, "rebase-switch-b")
	repoB.CommitFile("b.txt", "b\n", "commit in beta")

	h := newTestApp(t, repoB.StoreRepo())
	openHistoryPage(t, h, repoA.StoreRepo())
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)

	saved, ok := h.store.Get(h.store.Repos()[0].ID)
	if !ok {
		t.Fatal("seeded repository missing from the store")
	}
	h.app.openRepo(saved)
	h.ui.Frame()
	h.drain()

	st := rebaseUI(h.app)
	if st.open {
		t.Fatal("rebase dialog still open after the repository switch")
	}
	if st.ontoRev != "" || st.expectedHead != "" || st.branch != "" {
		t.Fatalf("rebase draft survived the switch: %+v", st)
	}
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History after switch: %v", err)
	}
	h.ui.Frame()
	assertRebaseDialogClosed(t, h)
}

// hostGitHasUpdateRefs reports whether the host's Git — which is also the
// Git a loopback server runs — knows rebase --update-refs.
func hostGitHasUpdateRefs(t *testing.T) bool {
	t.Helper()
	caps, err := git.New(&git.LocalExecutor{Dir: t.TempDir()}).Capabilities()
	if err != nil {
		t.Fatalf("probe host git capabilities: %v", err)
	}
	return caps.RebaseUpdateRefs
}

// --- dependent branches and capabilities ---

// With a capable Git the update-refs checkbox is enabled, unticked by
// default, and ticking it lists the dependent branches. Confirming then
// moves mid with the rebase; leaving it unticked keeps mid where it was.
func TestRebaseUpdateRefsMovesDependentBranches(t *testing.T) {
	if !hostGitHasUpdateRefs(t) {
		t.Skip("host git is older than 2.38: rebase --update-refs cannot be exercised here")
	}
	repo := newGitRepo(t, "rebase-update-refs")
	midTip, featureTip, mainTip := rebaseDependentFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)

	box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches")
	if !ok {
		t.Fatalf("update-refs checkbox missing with dependents present; texts=%q", h.ui.Texts())
	}
	if box.Disabled {
		t.Fatalf("update-refs checkbox disabled on a capable Git: %+v", box)
	}
	if box.Checked {
		t.Fatalf("update-refs checkbox checked by default: %+v", box)
	}

	// Round 1: unticked, mid stays at its original commit.
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase without update-refs failed: %v", err)
	}
	if mid := gitOutTrim(t, repo.Path(), "rev-parse", "mid"); mid != midTip {
		t.Fatalf("mid moved without update-refs: %q, want %q", mid, midTip)
	}
	newFeatureTip := gitOutTrim(t, repo.Path(), "rev-parse", "feature")
	if newFeatureTip == featureTip {
		t.Fatal("feature did not move after rebase")
	}
	if base := gitOutTrim(t, repo.Path(), "rev-parse", "feature~2"); base != mainTip {
		t.Fatalf("replayed chain base = %q, want main tip %q", base, mainTip)
	}

	// Round 2: rebuild the fixture shape and tick the box: mid moves onto the
	// replayed counterpart of its original commit.
	repo.Git("update-ref", "refs/heads/feature", featureTip)
	repo.Git("update-ref", "refs/heads/mid", midTip)
	repo.Git("reset", "--hard", "-q", featureTip)
	h.app.session.Reload()
	h.drain()
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)
	if err := h.ui.Click("Update dependent branches"); err != nil {
		t.Fatalf("tick update-refs checkbox: %v", err)
	}
	h.ui.Frame()
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches"); !ok || !box.Checked {
		t.Fatalf("update-refs after click = %+v found=%v, want checked", box, ok)
	}
	if !hasExactText(h.ui, "mid") {
		t.Fatalf("dependent branch mid not listed while ticked; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase with update-refs failed: %v", err)
	}
	midAfter := gitOutTrim(t, repo.Path(), "rev-parse", "mid")
	if midAfter == midTip {
		t.Fatalf("mid did not move with update-refs: %q", midAfter)
	}
	if parent := gitOutTrim(t, repo.Path(), "rev-parse", "mid^"); parent != mainTip {
		t.Fatalf("mid parent after update-refs = %q, want main tip %q: mid is not the replayed first commit", parent, mainTip)
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s", "mid")); subject != "feature one" {
		t.Fatalf("mid subject after update-refs = %q, want feature one", subject)
	}
	if tip := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); tip == featureTip || tip == midAfter {
		t.Fatalf("feature tip after update-refs = %q, want a new replayed commit distinct from mid %q", tip, midAfter)
	}
	if parent := gitOutTrim(t, repo.Path(), "rev-parse", "feature^"); parent != midAfter {
		t.Fatalf("replayed tip parent = %q, want replayed mid %q", parent, midAfter)
	}
}

// When the executor's Git reports a version without --update-refs, the
// checkbox is disabled with the version named, a direct guarded call with
// UpdateRefs is refused without moving anything, and an ordinary rebase —
// no update-refs — still passes on the same fake old Git.
func TestRebaseUpdateRefsCapabilityGateOldGit(t *testing.T) {
	repo := newGitRepo(t, "rebase-compat-old")
	midTip, featureTip, mainTip := rebaseDependentFixture(t, repo)

	fake := &versionFakeExecutor{version: "git version 2.25.1\n"}
	h := newTestApp(t)
	installVersionFakeExecutor(h, fake)
	openHistoryPage(t, h, repo.StoreRepo())
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)

	box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches")
	if !ok {
		t.Fatalf("update-refs checkbox missing with dependents present; texts=%q", h.ui.Texts())
	}
	if !box.Disabled {
		t.Fatalf("update-refs checkbox enabled on fake Git 2.25: %+v", box)
	}
	if box.Checked {
		t.Fatalf("update-refs checkbox checked on fake Git 2.25: %+v", box)
	}
	if !hasExactText(h.ui, "Requires Git 2.38 or newer (found git version 2.25.1).") {
		t.Fatalf("capability reason missing the found version; texts=%q", h.ui.Texts())
	}

	// Direct guarded call with UpdateRefs through the same fake Git: refused,
	// and nothing moves.
	live := guardedFakeRepo(repo, fake)
	err := rebaseWithGuard(live, featureTip, "feature", "main", mainTip, git.RebaseOntoOptions{UpdateRefs: true})
	if err == nil || !strings.Contains(err.Error(), "--update-refs") {
		t.Fatalf("guarded UpdateRefs rebase on fake Git 2.25 = %v, want an --update-refs refusal", err)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); head != featureTip {
		t.Fatalf("feature moved by the refused rebase: %q, want %q", head, featureTip)
	}
	if mid := gitOutTrim(t, repo.Path(), "rev-parse", "mid"); mid != midTip {
		t.Fatalf("mid moved by the refused rebase: %q, want %q", mid, midTip)
	}

	// The ordinary rebase still runs on the same fake old Git: update-refs is
	// the only capability the dialog gates.
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("ordinary rebase on fake Git 2.25 failed: %v", err)
	}
	if tip := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); tip == featureTip {
		t.Fatalf("feature did not move after the ordinary rebase: %q", tip)
	}
	if base := gitOutTrim(t, repo.Path(), "rev-parse", "feature~2"); base != mainTip {
		t.Fatalf("replayed chain base = %q, want main tip %q", base, mainTip)
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s", "feature")); subject != "feature two" {
		t.Fatalf("replayed tip subject = %q, want feature two", subject)
	}
	if mid := gitOutTrim(t, repo.Path(), "rev-parse", "mid"); mid != midTip {
		t.Fatalf("mid moved without update-refs: %q, want %q", mid, midTip)
	}
}

// When the version probe itself fails, the checkbox is disabled with the
// verification reason and the guarded call is refused: a version that cannot
// be verified is never treated as a new one.
func TestRebaseUpdateRefsCapabilityGateProbeFailure(t *testing.T) {
	repo := newGitRepo(t, "rebase-compat-probe")
	_, featureTip, mainTip := rebaseDependentFixture(t, repo)

	fake := &versionFakeExecutor{versionErr: errors.New("version probe failed")}
	h := newTestApp(t)
	installVersionFakeExecutor(h, fake)
	openHistoryPage(t, h, repo.StoreRepo())
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)

	box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches")
	if !ok {
		t.Fatalf("update-refs checkbox missing with dependents present; texts=%q", h.ui.Texts())
	}
	if !box.Disabled || box.Checked {
		t.Fatalf("update-refs after failed probe = %+v, want disabled and unchecked", box)
	}
	if !hasExactText(h.ui, "Git version could not be verified; requires Git 2.38 or newer.") {
		t.Fatalf("verification failure reason missing; texts=%q", h.ui.Texts())
	}

	live := guardedFakeRepo(repo, fake)
	err := rebaseWithGuard(live, featureTip, "feature", "main", mainTip, git.RebaseOntoOptions{UpdateRefs: true})
	if err == nil {
		t.Fatal("guarded UpdateRefs rebase passed despite the failed probe")
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); head != featureTip {
		t.Fatalf("feature moved by the refused rebase: %q, want %q", head, featureTip)
	}
}

// guardedFakeRepo builds a git.Repo on the fixture whose version probing and
// merge-tree behave exactly as the given fake describes: the capabilities a
// guarded rebase verifies are the faked ones while every other command runs
// on the real repository.
func guardedFakeRepo(repo *gitRepoFixture, fake *versionFakeExecutor) *git.Repo {
	clone := &versionFakeExecutor{
		real:          &git.LocalExecutor{Dir: repo.Path()},
		version:       fake.version,
		versionErr:    fake.versionErr,
		failMergeTree: fake.failMergeTree,
	}
	return git.New(clone)
}

// A Git without merge-tree --write-tree cannot predict conflicts: the
// prediction line hides entirely while everything else — capabilities,
// dependent branches, the rebase itself — keeps working.
func TestRebasePredictionUnavailableHidesLine(t *testing.T) {
	repo := newGitRepo(t, "rebase-predict-unavailable")
	_, _, _ = rebaseDependentFixture(t, repo)

	h := newTestApp(t)
	installVersionFakeExecutor(h, &versionFakeExecutor{failMergeTree: true})
	openHistoryPage(t, h, repo.StoreRepo())
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)

	if hasExactText(h.ui, "Rebase will cause conflicts") || hasExactText(h.ui, "Rebase can be done without conflicts") {
		t.Fatalf("prediction line rendered despite merge-tree failure; texts=%q", h.ui.Texts())
	}
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches"); !ok || box.Disabled {
		t.Fatalf("update-refs = %+v found=%v, want present and enabled: merge-tree is not the version probe", box, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// --- guards ---

// Without autostash, uncommitted tracked changes — unstaged or staged —
// refuse the rebase before Git runs: the error explains the refusal in the
// toolbar, and every byte of the working tree, the index and both branches
// is exactly as before.
func TestRebaseDirtyGuardProtectsWorkingTree(t *testing.T) {
	repo := newGitRepo(t, "rebase-dirty")
	featureTip, mainTip := rebaseDivergedFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// Round 1: an unstaged tracked modification refuses the rebase.
	repo.WriteFile("base.txt", "base\ndirty\n")
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "cannot rebase with uncommitted local changes")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); head != featureTip {
		t.Fatalf("feature moved by the refused rebase: %q, want %q", head, featureTip)
	}
	assertFileBytes(t, repo, "base.txt", "base\ndirty\n")
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); out != "" {
		t.Fatalf("index touched by the refused rebase: %q", out)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != mainTip {
		t.Fatalf("main moved by the refused rebase: %q, want %q", got, mainTip)
	}

	// Round 2: a staged change refuses the rebase the same way.
	repo.Git("checkout", "--", "base.txt")
	repo.WriteFile("base.txt", "base\nstaged\n")
	repo.Git("add", "--", "base.txt")
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "cannot rebase with uncommitted local changes")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); head != featureTip {
		t.Fatalf("feature moved by the refused rebase: %q, want %q", head, featureTip)
	}
	assertFileBytes(t, repo, "base.txt", "base\nstaged\n")
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); !strings.Contains(out, "base.txt") {
		t.Fatalf("staged change lost by the refused rebase: %q", out)
	}
}

// When the background guard cannot even read the repository state, the
// rebase never runs: HEAD, the branches, the index and every byte of the
// working tree are exactly as before, and the failure shows in the toolbar.
func TestRebaseGuardStatusFailure(t *testing.T) {
	repo := newGitRepo(t, "rebase-guard-status")
	featureTip, _ := rebaseDivergedFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)

	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "reading the repository state failed")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); head != featureTip {
		t.Fatalf("feature moved after a failed status read: %q, want %q", head, featureTip)
	}
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("working tree touched after a failed status read: %q", out)
	}
	if count := gitOutTrim(t, repo.Path(), "rev-list", "--count", "feature"); count != "2" {
		t.Fatalf("commit count after failed status read = %q, want 2", count)
	}
}

// When HEAD moved after the dialog opened — here an external commit — the
// guard refuses the rebase the user confirmed against the old state.
func TestRebaseGuardHeadMoved(t *testing.T) {
	repo := newGitRepo(t, "rebase-guard-head")
	rebaseDivergedFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)

	repo.WriteFile("other.txt", "other\n")
	repo.Git("add", "--", "other.txt")
	repo.Git("commit", "-m", "external commit")
	moved := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "HEAD or the current branch changed")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != moved {
		t.Fatalf("HEAD moved by the refused rebase: %q, want %q", head, moved)
	}
	if tip := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); tip != moved {
		t.Fatalf("feature after refused rebase = %q, want the external commit %q", tip, moved)
	}
	if count := gitOutTrim(t, repo.Path(), "rev-list", "--count", "feature"); count != "3" {
		t.Fatalf("refused rebase replayed or dropped commits: count %q, want 3", count)
	}
}

// When the target ref itself moved after the dialog opened — here main gains
// a commit — the guard refuses the rebase: replaying onto a different base
// is not what the user confirmed.
func TestRebaseGuardTargetMoved(t *testing.T) {
	repo := newGitRepo(t, "rebase-guard-target")
	featureTip, mainTip := rebaseDivergedFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)

	repo.Git("checkout", "-q", "main")
	repo.WriteFile("moved.txt", "moved\n")
	repo.Git("add", "--", "moved.txt")
	repo.Git("commit", "-m", "main moves on")
	movedMain := gitOutTrim(t, repo.Path(), "rev-parse", "main")
	if movedMain == mainTip {
		t.Fatal("fixture: main did not move")
	}
	repo.Git("checkout", "-q", "feature")

	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "rebase target changed")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); head != featureTip {
		t.Fatalf("feature moved by the refused rebase: %q, want %q", head, featureTip)
	}
	if parent := gitOutTrim(t, repo.Path(), "rev-parse", "feature^"); parent == movedMain {
		t.Fatal("refused rebase still replayed onto the moved target")
	}
}

// An untracked file sharing the name of a file the rebase target adds makes
// Git refuse to overwrite it: the error surfaces, the untracked file's bytes
// are exactly as before, and the rebase never started. Untracked files are
// not uncommitted tracked changes, so the guard passes the write to Git and
// Git itself is the second line of defense.
func TestRebaseUntrackedCollisionPreservesFile(t *testing.T) {
	repo := newGitRepo(t, "rebase-collision")
	featureTip, mainTip := rebaseDivergedFixture(t, repo)
	// Rebasing onto main checks out main.txt first; an untracked main.txt on
	// the feature working tree collides with it.
	repo.WriteFile("main.txt", "precious untracked\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "")
	assertFileBytes(t, repo, "main.txt", "precious untracked\n")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); head != featureTip {
		t.Fatalf("feature moved by the colliding rebase: %q, want %q", head, featureTip)
	}
	if parent := gitOutTrim(t, repo.Path(), "rev-parse", "feature^"); parent == mainTip {
		t.Fatal("colliding rebase still replayed onto main")
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after collision = %q, want empty", got)
	}
}

// --- autostash ---

// rebaseAutostashSetup dirties the checked-out feature branch: a tracked
// modification and an untracked file on top of an existing user stash, so
// the rebase's autostash must not swallow any of the three.
func rebaseAutostashSetup(t *testing.T, repo *gitRepoFixture) (userStashHash string) {
	t.Helper()
	// local.txt is tracked since the base commit in these fixtures.
	repo.WriteFile("local.txt", "stashed bytes\n")
	repo.Git("stash", "push", "-q", "-m", "existing user stash")
	userStashHash = gitOutTrim(t, repo.Path(), "stash", "list", "--format=%H")
	if userStashHash == "" {
		t.Fatal("fixture: user stash missing")
	}
	repo.WriteFile("local.txt", "unsaved local work\n")
	repo.WriteFile("untracked.txt", "untracked work\n")
	return userStashHash
}

// rebaseDivergedLocalFixture is the divergent fixture plus a local.txt
// tracked since the base, so autostash has a tracked file to carry.
func rebaseDivergedLocalFixture(t *testing.T, repo *gitRepoFixture) (featureTip, mainTip string) {
	t.Helper()
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.WriteFile("local.txt", "base local\n")
	repo.Git("add", "--", "local.txt")
	repo.Git("commit", "-m", "track local")
	repo.CreateBranch("feature")
	mainTip = repo.CommitFile("main.txt", "main work\n", "main work")
	repo.Checkout("feature")
	featureTip = repo.CommitFile("feature.txt", "feature work\n", "feature work")
	return featureTip, mainTip
}

func assertAutostashRestored(t *testing.T, repo *gitRepoFixture, userStashHash string) {
	t.Helper()
	assertFileBytes(t, repo, "local.txt", "unsaved local work\n")
	assertFileBytes(t, repo, "untracked.txt", "untracked work\n")
	list := gitOutTrim(t, repo.Path(), "stash", "list", "--format=%H")
	if list != userStashHash {
		t.Fatalf("stash list after rebase = %q, want only the pre-existing user stash %q", list, userStashHash)
	}
}

// With Stash and reapply local changes ticked, a dirty working tree no
// longer blocks the rebase: the changes are put aside, the replay runs, and
// afterwards the tracked modification, the untracked file and the user's own
// stash entry are all exactly as before.
func TestRebaseAutostashCleanRebase(t *testing.T) {
	repo := newGitRepo(t, "rebase-autostash-clean")
	featureTip, mainTip := rebaseDivergedLocalFixture(t, repo)
	userStash := rebaseAutostashSetup(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)
	if err := h.ui.Click("Stash and reapply local changes"); err != nil {
		t.Fatalf("tick autostash checkbox: %v", err)
	}
	h.ui.Frame()
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Stash and reapply local changes"); !ok || !box.Checked {
		t.Fatalf("autostash after click = %+v found=%v, want checked", box, ok)
	}
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("autostash rebase failed: %v", err)
	}
	assertCleanRebase(t, repo, featureTip, mainTip, "feature work")
	assertAutostashRestored(t, repo, userStash)
}

// --- conflict recovery through the shared banner ---

// triggerRebaseConflict rebases onto main through the real UI entry with the
// conflict fixture, producing a content conflict in f.txt, and verifies the
// operation banner appears with the rebase copy, Continue disabled and Skip
// and Abort enabled.
func triggerRebaseConflict(t *testing.T, h *testHarness) {
	t.Helper()
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)
	if !hasExactText(h.ui, "Rebase will cause conflicts") {
		t.Fatalf("conflict prediction missing; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()
	st := h.app.session.Snapshot().Status
	if st.State != "rebase" {
		t.Fatalf("state after conflicting rebase = %q, want rebase", st.State)
	}
	if !st.Conflicted() {
		t.Fatalf("fixture: rebase did not conflict: %+v", st)
	}
	if st.Operation == "" {
		t.Fatalf("operation target empty during rebase conflict: %+v", st)
	}
	if !hasExactText(h.ui, "Rebase in progress. Resolve conflicts and stage the files to continue, or skip/abort.") {
		t.Fatalf("rebase conflict banner copy missing; texts=%q", h.ui.Texts())
	}
	assertBannerLayout(t, h)
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Continue Operation"); !ok || !btn.Disabled {
		t.Fatalf("Continue Operation = %+v found=%v, want present and disabled during conflict", btn, ok)
	}
	for _, label := range []string{"Skip Commit", "Abort Operation"} {
		if btn, ok := h.ui.AccessByRole(ui.RoleButton, label); !ok || btn.Disabled {
			t.Fatalf("%s = %+v found=%v, want present and enabled during conflict", label, btn, ok)
		}
	}
}

// A conflicting rebase shows the shared banner on both pages, canceling the
// Abort confirmation preserves the operation exactly as it was, and
// confirming Abort restores the branch, the working tree and the banner to
// the state before the rebase.
func TestRebaseConflictBannerAbortCancelAndConfirm(t *testing.T) {
	repo := newGitRepo(t, "rebase-conflict-abort")
	featureTip, _ := rebaseConflictFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	triggerRebaseConflict(t, h)
	assertBannerLayoutNarrow(t, h)

	// The banner is on the Local Changes page too, where the conflicted file
	// is listed for resolution.
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Rebase in progress. Resolve conflicts and stage the files to continue, or skip/abort.") {
		t.Fatalf("rebase conflict banner missing on Local Changes; texts=%q", h.ui.Texts())
	}
	assertBannerLayout(t, h)
	assertBannerLayoutNarrow(t, h)
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, "Conflicted f.txt"); !ok {
		t.Fatalf("conflicted f.txt not listed on Local Changes; texts=%q", h.ui.Texts())
	}

	// Open the Abort confirmation here and cancel it: the operation, the
	// conflict and REBASE_HEAD all survive.
	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Abort rebase?") {
		t.Fatalf("abort confirmation title missing; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "Undo the rebase and restore the original branch.") {
		t.Fatalf("abort rebase description missing; texts=%q", h.ui.Texts())
	}
	assertDialogLayout(t, h, "Cancel", "Confirm Abort")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	if hasExactText(h.ui, "Abort rebase?") {
		t.Fatal("abort confirmation still open after Cancel")
	}
	st := h.app.session.Snapshot().Status
	if st.State != "rebase" || !st.Conflicted() {
		t.Fatalf("Cancel disturbed the operation: state=%q conflicted=%v", st.State, st.Conflicted())
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("Cancel resolved the conflict stages")
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "--verify", "--quiet", "REBASE_HEAD"); strings.TrimSpace(got) == "" {
		t.Fatal("REBASE_HEAD lost by the canceled abort")
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt")); err != nil || !strings.Contains(string(content), "<<<<<<<") {
		t.Fatalf("Cancel touched the conflicted file: content=%q err=%v", content, err)
	}

	// Confirming Abort restores the pre-rebase state.
	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation again: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase abort failed: %v", err)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after abort = %q, want empty", got)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); head != featureTip {
		t.Fatalf("feature after abort = %q, want pre-rebase %q", head, featureTip)
	}
	assertFileBytes(t, repo, "f.txt", "feature\n")
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("working tree dirty after abort: %q", out)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Operation in progress"); ok {
		t.Fatalf("operation banner still rendered after abort; texts=%q", h.ui.Texts())
	}
}

// After the user resolves the conflict on disk and stages the file through
// the real Local Changes UI, Continue enables and finishes the rebase: the
// feature commit lands replayed on the main tip with the resolution as its
// content, and the banner is gone.
func TestRebaseConflictContinueAfterResolution(t *testing.T) {
	repo := newGitRepo(t, "rebase-conflict-continue")
	_, mainTip := rebaseConflictFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	triggerRebaseConflict(t, h)

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
		t.Fatalf("rebase continue failed: %v", err)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after continue = %q, want empty", got)
	}
	tip := gitOutTrim(t, repo.Path(), "rev-parse", "feature")
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s", "feature")); subject != "feature edits" {
		t.Fatalf("continued rebase commit subject = %q, want feature edits", subject)
	}
	if parent := gitOutTrim(t, repo.Path(), "rev-parse", "feature^"); parent != mainTip {
		t.Fatalf("replayed commit parent = %q, want main tip %q", parent, mainTip)
	}
	assertFileBytes(t, repo, "f.txt", "resolved\n")
	if tip == gitOutTrim(t, repo.Path(), "rev-parse", "main") {
		t.Fatal("feature did not move after continue")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Operation in progress"); ok {
		t.Fatalf("operation banner still rendered after continue; texts=%q", h.ui.Texts())
	}
}

// Skipping the conflicted commit drops it and replays the rest: the clean
// follow-up commit lands on the main tip, the conflicting change never
// applies, and the banner is gone.
func TestRebaseConflictSkipContinuesReplay(t *testing.T) {
	repo := newGitRepo(t, "rebase-conflict-skip")
	repo.CommitFile("f.txt", "line\n", "base commit")
	repo.CreateBranch("feature")
	mainTip := repo.CommitFile("f.txt", "main\n", "main edits")
	repo.Checkout("feature")
	repo.CommitFile("f.txt", "feature\n", "feature edits")
	repo.CommitFile("extra.txt", "extra\n", "add extra")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	triggerRebaseConflict(t, h)

	if err := h.ui.Click("Skip Commit"); err != nil {
		t.Fatalf("click Skip Commit: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase skip failed: %v", err)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after skip = %q, want empty", got)
	}
	assertFileBytes(t, repo, "f.txt", "main\n")
	assertFileBytes(t, repo, "extra.txt", "extra\n")
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s", "feature")); subject != "add extra" {
		t.Fatalf("tip subject after skip = %q, want add extra", subject)
	}
	if parent := gitOutTrim(t, repo.Path(), "rev-parse", "feature^"); parent != mainTip {
		t.Fatalf("tip parent after skip = %q, want main tip %q: the conflicting commit was not dropped", parent, mainTip)
	}
	if subjects := repo.Git("log", "--format=%s", "feature"); strings.Contains(subjects, "feature edits") {
		t.Fatalf("skipped commit survived the rebase: %q", subjects)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Operation in progress"); ok {
		t.Fatalf("operation banner still rendered after skip; texts=%q", h.ui.Texts())
	}
}

// When the recovery guard cannot read the repository state, the abort never
// runs; when the operation changed underneath the open confirmation, the
// guard refuses the abort the user confirmed against the original. Both
// leave the conflict exactly as it was.
func TestRebaseAbortGuardStatusFailureAndIdentity(t *testing.T) {
	repo := newGitRepo(t, "rebase-abort-guards")
	featureTip, _ := rebaseConflictFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	triggerRebaseConflict(t, h)

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
	if st := h.app.session.Snapshot().Status; st.State != "rebase" {
		t.Fatalf("operation lost after a failed status read: state=%q", st.State)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("conflict stages lost after a failed status read")
	}

	// A changed operation refuses the abort: an external actor aborts the
	// rebase the user saw and starts a replacement whose conflict stops at the
	// same HEAD but on a different commit — the kind, the HEAD and even the
	// conflict look the same, and only the operation identity tells the
	// replacement apart. The replacement conflict is never the user's to
	// discard, so it survives whole.
	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation again: %v", err)
	}
	h.ui.Frame()
	repo.Git("rebase", "--abort")
	repo.WriteFile("f.txt", "feature alt\n")
	repo.Git("add", "--", "f.txt")
	repo.Git("commit", "-q", "--amend", "--no-edit")
	replacement := gitOutTrim(t, repo.Path(), "rev-parse", "feature")
	if replacement == featureTip {
		t.Fatal("fixture: amended feature commit did not change hash")
	}
	if out, err := tryGitCommand(repo.Path(), "rebase", "main"); err == nil {
		t.Fatalf("replacement rebase unexpectedly clean: %s", out)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "REBASE_HEAD"); got != replacement {
		t.Fatalf("fixture: replacement operation target = %q, want %q", got, replacement)
	}

	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()
	assertRepoErrorShown(t, h, "active operation changed")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "--verify", "--quiet", "REBASE_HEAD"); strings.TrimSpace(got) != replacement {
		t.Fatalf("replacement operation destroyed by the refused abort: REBASE_HEAD=%q want %q", got, replacement)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("replacement conflict resolved by the refused abort")
	}
}

// An interrupted rebase — REBASE_HEAD lost, the machinery otherwise intact —
// refuses Continue through the repair guard: the error shows, HEAD and every
// rebase-merge file stay byte-identical, while Abort still restores the
// original branch.
func TestRebaseInterruptedRefusesContinueButAborts(t *testing.T) {
	repo := newGitRepo(t, "rebase-interrupted")
	featureTip, _ := rebaseConflictFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	triggerRebaseConflict(t, h)

	// Interrupt the rebase: REBASE_HEAD disappears, so the state no longer
	// names the commit it stopped on and automatic continue could drop or
	// duplicate commits.
	if err := os.Remove(filepath.Join(repo.Path(), ".git", "REBASE_HEAD")); err != nil {
		t.Fatalf("remove REBASE_HEAD: %v", err)
	}
	h.app.session.Reload()
	h.drain()
	st := h.app.session.Snapshot().Status
	if st.State != "rebase" {
		t.Fatalf("interrupted state = %q, want rebase", st.State)
	}
	if st.Operation == "" || !strings.Contains(st.Operation, ":") {
		t.Fatalf("interrupted operation identity = %q, want <hash>:<digest>", st.Operation)
	}

	// Resolve and stage through the real UI so Continue is clickable.
	repo.WriteFile("f.txt", "resolved\n")
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Mark Resolved f.txt"); err != nil {
		t.Fatalf("click Mark Resolved f.txt: %v", err)
	}
	h.drain()
	headBefore := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	before := rebaseMergeSnapshot(t, repo.Path())

	if err := h.ui.Click("Continue Operation"); err != nil {
		t.Fatalf("click Continue Operation: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "unknown state")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != headBefore {
		t.Fatalf("HEAD moved by the refused continue: %q, want %q", head, headBefore)
	}
	assertRebaseMergeUnchanged(t, before, rebaseMergeSnapshot(t, repo.Path()))
	if count := gitOutTrim(t, repo.Path(), "rev-list", "--count", "HEAD"); count != "2" {
		t.Fatalf("refused continue created a commit: count %q, want 2", count)
	}

	// Abort stays safe: it restores the original branch exactly.
	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("abort of the interrupted rebase failed: %v", err)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); head != featureTip {
		t.Fatalf("feature after abort = %q, want pre-rebase %q", head, featureTip)
	}
	assertFileBytes(t, repo, "f.txt", "feature\n")
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("working tree dirty after abort: %q", out)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), ".git", "rebase-merge")); !os.IsNotExist(err) {
		t.Fatalf("rebase-merge survived the abort: err=%v", err)
	}
}

// Autostash survives the conflict paths: the local changes stay in the stash
// while the rebase is conflicted, and they come back after a continued
// rebase as well as after an aborted one, never touching the user's own
// stash entry.
func TestRebaseAutostashAcrossConflictContinueAndAbort(t *testing.T) {
	for _, abort := range []bool{false, true} {
		name := "continue"
		if abort {
			name = "abort"
		}
		t.Run(name, func(t *testing.T) {
			repo := newGitRepo(t, "rebase-autostash-conflict-"+name)
			// The conflict fixture plus a tracked local.txt since the base.
			repo.CommitFile("f.txt", "line\n", "base commit")
			repo.WriteFile("local.txt", "base local\n")
			repo.Git("add", "--", "local.txt")
			repo.Git("commit", "-m", "track local")
			repo.CreateBranch("feature")
			repo.CommitFile("f.txt", "main\n", "main edits")
			repo.Checkout("feature")
			featureTip := repo.CommitFile("f.txt", "feature\n", "feature edits")
			userStash := rebaseAutostashSetup(t, repo)

			h := newTestApp(t)
			openHistoryPage(t, h, repo.StoreRepo())
			openRebaseDialogFromBranch(t, h, "main")
			drainRebaseInspection(t, h)
			if err := h.ui.Click("Stash and reapply local changes"); err != nil {
				t.Fatalf("tick autostash checkbox: %v", err)
			}
			h.ui.Frame()
			if err := h.ui.Click("Confirm Rebase"); err != nil {
				t.Fatalf("click Confirm Rebase: %v", err)
			}
			h.drain()
			st := h.app.session.Snapshot().Status
			if st.State != "rebase" || !st.Conflicted() {
				t.Fatalf("fixture: no conflict stop: %+v", st)
			}
			// While conflicted the local changes are inside the rebase's
			// autostash: the working tree carries none of them.
			if content, err := os.ReadFile(filepath.Join(repo.Path(), "local.txt")); err != nil || string(content) != "base local\n" {
				t.Fatalf("local.txt during conflict = %q err=%v, want the base bytes", content, err)
			}

			if abort {
				if err := h.ui.Click("Abort Operation"); err != nil {
					t.Fatalf("click Abort Operation: %v", err)
				}
				h.ui.Frame()
				if err := h.ui.Click("Confirm Abort"); err != nil {
					t.Fatalf("click Confirm Abort: %v", err)
				}
				h.drain()
				if err := h.app.session.ActionErr(); err != nil {
					t.Fatalf("abort with autostash failed: %v", err)
				}
				if head := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); head != featureTip {
					t.Fatalf("feature after abort = %q, want pre-rebase %q", head, featureTip)
				}
				assertFileBytes(t, repo, "f.txt", "feature\n")
			} else {
				repo.WriteFile("f.txt", "resolved\n")
				if err := h.ui.Click("Changes"); err != nil {
					t.Fatalf("click Changes: %v", err)
				}
				h.ui.Frame()
				if err := h.ui.Click("Mark Resolved f.txt"); err != nil {
					t.Fatalf("click Mark Resolved f.txt: %v", err)
				}
				h.drain()
				if err := h.ui.Click("Continue Operation"); err != nil {
					t.Fatalf("click Continue Operation: %v", err)
				}
				h.drain()
				if err := h.app.session.ActionErr(); err != nil {
					t.Fatalf("continue with autostash failed: %v", err)
				}
				assertFileBytes(t, repo, "f.txt", "resolved\n")
			}
			if got := h.app.session.Snapshot().Status.State; got != "" {
				t.Fatalf("state after %s = %q, want empty", name, got)
			}
			assertAutostashRestored(t, repo, userStash)
		})
	}
}

// The guard's own input validation and the states the menus can never reach
// — a detached HEAD, an in-progress operation, a switched branch — refuse
// before anything runs, each with a repository that proves nothing moved.
func TestRebaseGuardInputValidationAndStates(t *testing.T) {
	repo := newGitRepo(t, "rebase-guard-inputs")
	featureTip, _ := rebaseDivergedFixture(t, repo)
	live := git.New(&git.LocalExecutor{Dir: repo.Path()})

	assertRefused := func(ontoRev string, opts git.RebaseOntoOptions, head, branch string, fragment string) {
		t.Helper()
		featureBefore := gitOutTrim(t, repo.Path(), "rev-parse", "feature")
		mainBefore := gitOutTrim(t, repo.Path(), "rev-parse", "main")
		err := rebaseWithGuard(live, head, branch, ontoRev, "", opts)
		if err == nil {
			t.Fatalf("rebaseWithGuard(%q, head=%q branch=%q) passed, want a refusal naming %q", ontoRev, head, branch, fragment)
		}
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("rebaseWithGuard error %q missing %q", err.Error(), fragment)
		}
		if got := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); got != featureBefore {
			t.Fatalf("feature moved by a refused rebase: %q, want %q", got, featureBefore)
		}
		if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != mainBefore {
			t.Fatalf("main moved by a refused rebase: %q, want %q", got, mainBefore)
		}
	}
	clean := git.RebaseOntoOptions{}

	// Malformed targets and detached or self targets never reach Git.
	assertRefused("", clean, featureTip, "feature", "invalid target revision")
	assertRefused("-x", clean, featureTip, "feature", "invalid target revision")
	assertRefused("main\x00x", clean, featureTip, "feature", "invalid target revision")
	assertRefused("main\nx", clean, featureTip, "feature", "invalid target revision")
	assertRefused("main\rx", clean, featureTip, "feature", "invalid target revision")
	assertRefused("main", clean, "", "feature", "not on a branch")
	assertRefused("main", clean, featureTip, "", "not on a branch")
	assertRefused("feature", clean, featureTip, "feature", "the target is the current branch")

	// An operation in progress refuses the rebase outright.
	repo.WriteFile("c.txt", "main side\n")
	repo.Git("add", "--", "c.txt")
	repo.Git("commit", "-m", "main conflict base")
	repo.Git("checkout", "-q", "main")
	repo.WriteFile("c.txt", "main edits\n")
	repo.Git("add", "--", "c.txt")
	repo.Git("commit", "-m", "main conflict edits")
	repo.WriteFile("f.txt", "wait\n")
	repo.Git("add", "--", "f.txt")
	repo.Git("commit", "-m", "main more")
	repo.Checkout("feature")
	repo.WriteFile("c.txt", "feature side\n")
	repo.Git("add", "--", "c.txt")
	repo.Git("commit", "-m", "feature conflict base")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "main"); err == nil {
		t.Fatalf("conflicting merge unexpectedly succeeded: %s", out)
	}
	err := rebaseWithGuard(live, featureTip, "feature", "main", "", clean)
	if err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("rebase during a merge = %v, want an in-progress refusal", err)
	}
	repo.Git("merge", "--abort")

	// A switched branch — HEAD unmoved, the branch different — refuses.
	headNow := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	repo.Git("checkout", "-q", "-b", "other")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != headNow {
		t.Fatal("fixture: checkout -b moved HEAD")
	}
	err = rebaseWithGuard(live, headNow, "feature", "main", "", clean)
	if err == nil || !strings.Contains(err.Error(), "HEAD or the current branch changed") {
		t.Fatalf("rebase after a branch switch = %v, want a HEAD-or-branch refusal", err)
	}
	repo.Git("checkout", "-q", "feature")
}

// --- SSH loopback ---

// remoteRebaseDiverged builds the divergent clean fixture on the server:
// feature branched off the base, both sides adding their own file. It
// returns the feature tip and the main tip.
func remoteRebaseDiverged(t *testing.T, srv *sshtest.Target, dir string) (featureTip, mainTip string) {
	t.Helper()
	srv.WriteFile(t, dir, "base.txt", []byte("base\n"))
	srv.RunGit(t, dir, "add", "--", "base.txt")
	srv.RunGit(t, dir, "commit", "-q", "-m", "base commit")
	srv.RunGit(t, dir, "branch", "feature")
	mainTip = remoteCommitFile(t, srv, dir, "main.txt", "main work\n", "main work")
	srv.RunGit(t, dir, "checkout", "-q", "feature")
	featureTip = remoteCommitFile(t, srv, dir, "feature.txt", "feature work\n", "feature work")
	return featureTip, mainTip
}

// remoteRebaseConflict builds the conflict fixture on the server: both
// branches rewrite the same line of f.txt.
func remoteRebaseConflict(t *testing.T, srv *sshtest.Target, dir string) (featureTip, mainTip string) {
	t.Helper()
	srv.WriteFile(t, dir, "f.txt", []byte("line\n"))
	srv.RunGit(t, dir, "add", "--", "f.txt")
	srv.RunGit(t, dir, "commit", "-q", "-m", "base commit")
	srv.RunGit(t, dir, "branch", "feature")
	mainTip = remoteCommitFile(t, srv, dir, "f.txt", "main\n", "main edits")
	srv.RunGit(t, dir, "checkout", "-q", "feature")
	featureTip = remoteCommitFile(t, srv, dir, "f.txt", "feature\n", "feature edits")
	return featureTip, mainTip
}

// remoteRebaseAutostashSetup dirties the server's feature branch: a tracked
// modification and an untracked file on top of an existing user stash.
func remoteRebaseAutostashSetup(t *testing.T, srv *sshtest.Target, dir string) (userStashHash string) {
	t.Helper()
	srv.WriteFile(t, dir, "local.txt", []byte("stashed bytes\n"))
	srv.RunGit(t, dir, "stash", "push", "-q", "-m", "existing user stash")
	userStashHash = strings.TrimSpace(srv.RunGit(t, dir, "stash", "list", "--format=%H"))
	if userStashHash == "" {
		t.Fatal("fixture: user stash missing on server")
	}
	srv.WriteFile(t, dir, "local.txt", []byte("unsaved local work\n"))
	srv.WriteFile(t, dir, "untracked.txt", []byte("untracked work\n"))
	return userStashHash
}

func assertRemoteAutostashRestored(t *testing.T, srv *sshtest.Target, dir, userStashHash string) {
	t.Helper()
	if got := srv.ReadFile(t, dir, "local.txt"); string(got) != "unsaved local work\n" {
		t.Fatalf("remote local.txt after rebase = %q, want unsaved local work", string(got))
	}
	if got := srv.ReadFile(t, dir, "untracked.txt"); string(got) != "untracked work\n" {
		t.Fatalf("remote untracked.txt after rebase = %q, want untracked work", string(got))
	}
	if list := strings.TrimSpace(srv.RunGit(t, dir, "stash", "list", "--format=%H")); list != userStashHash {
		t.Fatalf("remote stash list after rebase = %q, want only the pre-existing user stash %q", list, userStashHash)
	}
}

// Over a real SSH connection the sidebar entries rebase the current branch
// onto a local branch and onto a remote-tracking branch, replaying the
// feature commit on top of each target. Every assertion reads the server's
// repository through a second SSH channel.
func TestRebaseOntoBranchesLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	featureTip, mainTip := remoteRebaseDiverged(t, srv, repo.Path)

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)

	// Round 1: local branch target.
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH rebase onto local branch failed: %v", err)
	}
	newTip := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature"))
	if newTip == featureTip {
		t.Fatalf("remote feature did not move after rebase: %q", newTip)
	}
	if parent := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature^")); parent != mainTip {
		t.Fatalf("remote replayed parent = %q, want main tip %q", parent, mainTip)
	}
	if got := srv.ReadFile(t, repo.Path, "feature.txt"); string(got) != "feature work\n" {
		t.Fatalf("remote feature.txt after rebase = %q, want feature work", string(got))
	}

	// Round 2: rebuild the divergence, give origin/main a commit ahead of
	// Round 1's main tip, and rebase onto the remote-tracking branch
	// origin/main. Using a distinct commit for origin/main guarantees a new
	// replayed commit hash even when both rebases complete within the same
	// second, and proves the rebase targeted origin/main rather than main.
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	originMainTip := remoteCommitFile(t, srv, repo.Path, "origin-main.txt", "origin main work\n", "origin main work")
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repo.Path, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repo.Path, "push", "-q", "origin", "main")
	srv.RunGit(t, repo.Path, "reset", "--hard", "-q", mainTip)
	srv.RunGit(t, repo.Path, "checkout", "-q", "feature")
	srv.RunGit(t, repo.Path, "reset", "--hard", "-q", featureTip)
	h.app.session.Reload()
	h.drain()
	if got := h.app.session.Snapshot().Refs.Remote; len(got) == 0 {
		t.Fatal("fixture: no remote-tracking branches in snapshot")
	}
	if err := h.ui.RightClick("origin/main"); err != nil {
		t.Fatalf("right-click remote branch row: %v\ntexts=%q", err, h.ui.Texts())
	}
	label := "Rebase 'feature' on 'origin/main'…"
	if !slices.Contains(h.ui.Menu(), label) {
		t.Fatalf("menu %q missing %q", h.ui.Menu(), label)
	}
	if err := h.ui.ChooseMenuItem(label); err != nil {
		t.Fatalf("choose %q: %v", label, err)
	}
	h.ui.Frame()
	assertRebaseDialogOpen(t, h)
	drainRebaseInspection(t, h)
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH rebase onto remote branch failed: %v", err)
	}
	newTip2 := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature"))
	if newTip2 == featureTip || newTip2 == newTip {
		t.Fatalf("remote feature did not move onto origin/main: %q", newTip2)
	}
	if parent := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature^")); parent != originMainTip {
		t.Fatalf("remote replayed parent onto origin/main = %q, want %q", parent, originMainTip)
	}
}

// Over SSH the update-refs checkbox moves the dependent branch mid with the
// rebase, exactly as it does locally — the server's Git here is new enough
// for the capability, and the assertions read the server's refs.
func TestRebaseUpdateRefsLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	srv.WriteFile(t, repo.Path, "base.txt", []byte("base\n"))
	srv.RunGit(t, repo.Path, "add", "--", "base.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "base commit")
	srv.RunGit(t, repo.Path, "branch", "feature")
	mainTip := remoteCommitFile(t, srv, repo.Path, "main.txt", "main work\n", "main work")
	srv.RunGit(t, repo.Path, "checkout", "-q", "feature")
	midTip := remoteCommitFile(t, srv, repo.Path, "f1.txt", "feature one\n", "feature one")
	srv.RunGit(t, repo.Path, "branch", "mid")
	featureTip := remoteCommitFile(t, srv, repo.Path, "f2.txt", "feature two\n", "feature two")

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)
	box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches")
	if !hostGitHasUpdateRefs(t) {
		if !ok || !box.Disabled {
			t.Fatalf("SSH update-refs checkbox on old host git = %+v found=%v, want present and disabled", box, ok)
		}
		return
	}
	if !ok || box.Disabled {
		t.Fatalf("SSH update-refs checkbox = %+v found=%v, want present and enabled", box, ok)
	}
	if err := h.ui.Click("Update dependent branches"); err != nil {
		t.Fatalf("tick update-refs checkbox: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "mid") {
		t.Fatalf("dependent branch mid not listed while ticked; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH update-refs rebase failed: %v", err)
	}
	midAfter := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "mid"))
	if midAfter == midTip {
		t.Fatalf("remote mid did not move with update-refs: %q", midAfter)
	}
	if parent := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "mid^")); parent != mainTip {
		t.Fatalf("remote mid parent = %q, want main tip %q", parent, mainTip)
	}
	featureAfter := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature"))
	if featureAfter == featureTip {
		t.Fatalf("remote feature did not move: %q", featureAfter)
	}
	if parent := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature^")); parent != midAfter {
		t.Fatalf("remote feature parent = %q, want replayed mid %q", parent, midAfter)
	}
}

// Over SSH autostash carries the local changes through a clean rebase and
// through both conflict outcomes, and the guards — dirty without autostash,
// failed status read, moved target — refuse without touching the server.
func TestRebaseAutostashAndGuardsLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	// Divergent fixture with a tracked local.txt since the base.
	srv.WriteFile(t, repo.Path, "base.txt", []byte("base\n"))
	srv.WriteFile(t, repo.Path, "local.txt", []byte("base local\n"))
	srv.RunGit(t, repo.Path, "add", "--", "base.txt", "local.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "base commit")
	srv.RunGit(t, repo.Path, "branch", "feature")
	mainTip := remoteCommitFile(t, srv, repo.Path, "main.txt", "main work\n", "main work")
	srv.RunGit(t, repo.Path, "checkout", "-q", "feature")
	featureTip := remoteCommitFile(t, srv, repo.Path, "feature.txt", "feature work\n", "feature work")
	userStash := remoteRebaseAutostashSetup(t, srv, repo.Path)

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)

	// Round 1: without autostash the dirty working tree refuses the rebase.
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()
	assertRepoErrorShown(t, h, "cannot rebase with uncommitted local changes")
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature")); head != featureTip {
		t.Fatalf("remote feature moved by the refused rebase: %q, want %q", head, featureTip)
	}
	if got := srv.ReadFile(t, repo.Path, "local.txt"); string(got) != "unsaved local work\n" {
		t.Fatalf("remote local.txt overwritten by refused rebase: %q", string(got))
	}

	// Round 2: with autostash the same dirty state rebases cleanly and the
	// changes come back, the user's stash untouched.
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)
	if err := h.ui.Click("Stash and reapply local changes"); err != nil {
		t.Fatalf("tick autostash checkbox: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH autostash rebase failed: %v", err)
	}
	if parent := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature^")); parent != mainTip {
		t.Fatalf("remote replayed parent = %q, want main tip %q", parent, mainTip)
	}
	assertRemoteAutostashRestored(t, srv, repo.Path, userStash)

	// Round 3: a failed status read refuses the rebase before Git runs.
	srv.RunGit(t, repo.Path, "update-ref", "refs/heads/feature", featureTip)
	srv.RunGit(t, repo.Path, "reset", "--hard", "-q", featureTip)
	h.app.session.Reload()
	h.drain()
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)
	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()
	assertRepoErrorShown(t, h, "reading the repository state failed")
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature")); head != featureTip {
		t.Fatalf("remote feature moved after failed status read: %q, want %q", head, featureTip)
	}

	// Round 4: the target moving after the dialog opened refuses the rebase.
	openRebaseDialogFromBranch(t, h, "main")
	drainRebaseInspection(t, h)
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	remoteCommitFile(t, srv, repo.Path, "moved.txt", "moved\n", "main moves on")
	srv.RunGit(t, repo.Path, "checkout", "-q", "feature")
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()
	assertRepoErrorShown(t, h, "rebase target changed")
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature")); head != featureTip {
		t.Fatalf("remote feature moved despite the moved target: %q, want %q", head, featureTip)
	}
}

// Over SSH the conflict banner drives the full recovery: Skip drops the
// conflicted commit and finishes the replay, and the abort guards — failed
// status read, changed operation — refuse without destroying the conflict,
// while a confirmed Abort restores the pre-rebase state.
func TestRebaseConflictRecoveryLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	featureTip, mainTip := remoteRebaseConflict(t, srv, repo.Path)

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)
	triggerRebaseConflict(t, h)
	if !hasExactText(h.ui, "Rebase in progress. Resolve conflicts and stage the files to continue, or skip/abort.") {
		t.Fatalf("SSH rebase conflict banner copy missing; texts=%q", h.ui.Texts())
	}
	assertBannerLayout(t, h)

	// Skip drops the only commit of the replay: the rebase ends with nothing
	// replayed, so feature lands on the main tip and the conflicted change
	// never applies.
	if err := h.ui.Click("Skip Commit"); err != nil {
		t.Fatalf("click Skip Commit: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH rebase skip failed: %v", err)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after SSH skip = %q, want empty", got)
	}
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature")); head != mainTip {
		t.Fatalf("feature after skipping the only replayed commit = %q, want main tip %q", head, mainTip)
	}
	if subjects := srv.RunGit(t, repo.Path, "log", "--format=%s", "feature"); strings.Contains(subjects, "feature edits") {
		t.Fatalf("skipped commit survived on the server: %q", subjects)
	}

	// Rebuild the conflict for the abort guards.
	srv.RunGit(t, repo.Path, "update-ref", "refs/heads/feature", featureTip)
	srv.RunGit(t, repo.Path, "reset", "--hard", "-q", featureTip)
	h.app.session.Reload()
	h.drain()
	triggerRebaseConflict(t, h)

	// Failed status read refuses the abort.
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
	if st := h.app.session.Snapshot().Status; st.State != "rebase" {
		t.Fatalf("operation lost after failed status read: state=%q", st.State)
	}

	// A changed operation refuses the abort: externally abort the rebase the
	// user saw and start a replacement whose conflict stops at the same HEAD
	// on a different commit — only the operation identity tells it apart.
	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation again: %v", err)
	}
	h.ui.Frame()
	srv.RunGit(t, repo.Path, "rebase", "--abort")
	srv.WriteFile(t, repo.Path, "f.txt", []byte("feature alt\n"))
	srv.RunGit(t, repo.Path, "add", "--", "f.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "--amend", "--no-edit")
	replacement := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature"))
	if replacement == featureTip {
		t.Fatal("fixture: amended feature commit did not change hash")
	}
	srv.RunShell(t, "GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -C "+git.ShellQuote(repo.Path)+" rebase main >/dev/null 2>&1 || true")
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "REBASE_HEAD")); got != replacement {
		t.Fatalf("fixture: replacement operation target = %q, want %q", got, replacement)
	}

	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()
	assertRepoErrorShown(t, h, "active operation changed")
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "--verify", "--quiet", "REBASE_HEAD")); got != replacement {
		t.Fatalf("replacement operation destroyed by the refused abort: REBASE_HEAD=%q want %q", got, replacement)
	}
	if out := strings.TrimSpace(srv.RunGit(t, repo.Path, "ls-files", "-u")); out == "" {
		t.Fatal("replacement conflict resolved by the refused abort")
	}

	// Rebuild once more and confirm the real Abort: the server's feature, the
	// working tree and the banner return to the pre-rebase state.
	srv.RunGit(t, repo.Path, "rebase", "--abort")
	srv.RunGit(t, repo.Path, "update-ref", "refs/heads/feature", featureTip)
	srv.RunGit(t, repo.Path, "reset", "--hard", "-q", featureTip)
	h.app.session.Reload()
	h.drain()
	triggerRebaseConflict(t, h)
	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation final: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Abort rebase?") {
		t.Fatalf("SSH abort rebase title missing; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH abort failed: %v", err)
	}
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature")); head != featureTip {
		t.Fatalf("remote feature after abort = %q, want pre-rebase %q", head, featureTip)
	}
	if got := srv.ReadFile(t, repo.Path, "f.txt"); string(got) != "feature\n" {
		t.Fatalf("remote f.txt after abort = %q, want feature", string(got))
	}
	if out := strings.TrimSpace(srv.RunGit(t, repo.Path, "status", "--porcelain")); out != "" {
		t.Fatalf("remote working tree dirty after abort: %q", out)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Operation in progress"); ok {
		t.Fatalf("operation banner still rendered after SSH abort; texts=%q", h.ui.Texts())
	}
}

// Over SSH autostash survives a conflicted rebase's Continue and Abort:
// the local changes come back either way, the user's own stash untouched.
func TestRebaseAutostashConflictLoopbackSSH(t *testing.T) {
	for _, abort := range []bool{false, true} {
		name := "continue"
		if abort {
			name = "abort"
		}
		t.Run(name, func(t *testing.T) {
			repo, srv := loopbackSSHRepo(t)
			srv.WriteFile(t, repo.Path, "f.txt", []byte("line\n"))
			srv.WriteFile(t, repo.Path, "local.txt", []byte("base local\n"))
			srv.RunGit(t, repo.Path, "add", "--", "f.txt", "local.txt")
			srv.RunGit(t, repo.Path, "commit", "-q", "-m", "base commit")
			srv.RunGit(t, repo.Path, "branch", "feature")
			remoteCommitFile(t, srv, repo.Path, "f.txt", "main\n", "main edits")
			srv.RunGit(t, repo.Path, "checkout", "-q", "feature")
			featureTip := remoteCommitFile(t, srv, repo.Path, "f.txt", "feature\n", "feature edits")
			userStash := remoteRebaseAutostashSetup(t, srv, repo.Path)

			h := newTestApp(t, repo)
			openSSHHistoryPage(t, h, repo)
			openRebaseDialogFromBranch(t, h, "main")
			drainRebaseInspection(t, h)
			if err := h.ui.Click("Stash and reapply local changes"); err != nil {
				t.Fatalf("tick autostash checkbox: %v", err)
			}
			h.ui.Frame()
			if err := h.ui.Click("Confirm Rebase"); err != nil {
				t.Fatalf("click Confirm Rebase: %v", err)
			}
			h.drain()
			st := h.app.session.Snapshot().Status
			if st.State != "rebase" || !st.Conflicted() {
				t.Fatalf("fixture: no conflict stop on server: %+v", st)
			}

			if abort {
				if err := h.ui.Click("Abort Operation"); err != nil {
					t.Fatalf("click Abort Operation: %v", err)
				}
				h.ui.Frame()
				if err := h.ui.Click("Confirm Abort"); err != nil {
					t.Fatalf("click Confirm Abort: %v", err)
				}
				h.drain()
				if err := h.app.session.ActionErr(); err != nil {
					t.Fatalf("SSH abort with autostash failed: %v", err)
				}
				if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature")); head != featureTip {
					t.Fatalf("remote feature after abort = %q, want pre-rebase %q", head, featureTip)
				}
			} else {
				srv.WriteFile(t, repo.Path, "f.txt", []byte("resolved\n"))
				if err := h.ui.Click("Changes"); err != nil {
					t.Fatalf("click Changes: %v", err)
				}
				h.ui.Frame()
				if err := h.ui.Click("Mark Resolved f.txt"); err != nil {
					t.Fatalf("click Mark Resolved f.txt: %v", err)
				}
				h.drain()
				if err := h.ui.Click("Continue Operation"); err != nil {
					t.Fatalf("click Continue Operation: %v", err)
				}
				h.drain()
				if err := h.app.session.ActionErr(); err != nil {
					t.Fatalf("SSH continue with autostash failed: %v", err)
				}
			}
			if got := h.app.session.Snapshot().Status.State; got != "" {
				t.Fatalf("state after SSH %s = %q, want empty", name, got)
			}
			assertRemoteAutostashRestored(t, srv, repo.Path, userStash)
		})
	}
}
