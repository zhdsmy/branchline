package app

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// tryGitCommandStdin runs git in dir with stdin fed, for the index surgery of
// the recreated-conflict test. It mirrors tryGitCommand's isolation.
func tryGitCommandStdin(dir, stdin string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	cmd.Stdin = strings.NewReader(stdin)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// openReplayDialog right-clicks the commit row of hash, chooses the replay
// menu item, and verifies the dialog opened with the expected title.
func openReplayDialog(t *testing.T, h *testHarness, hash, subject, menuLabel, title string) {
	t.Helper()
	row := commitRowLabel(hash, subject)
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row %q: %v\ntexts=%q", row, err, h.ui.Texts())
	}
	if !slices.Contains(h.ui.Menu(), menuLabel) {
		t.Fatalf("menu %q missing %q", h.ui.Menu(), menuLabel)
	}
	if err := h.ui.ChooseMenuItem(menuLabel); err != nil {
		t.Fatalf("choose %q: %v", menuLabel, err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, title) {
		t.Fatalf("replay dialog %q did not open; texts=%q", title, h.ui.Texts())
	}
}

func openCherryPickDialog(t *testing.T, h *testHarness, hash, subject string) {
	t.Helper()
	openReplayDialog(t, h, hash, subject, "Cherry-pick Commit…", "Cherry-pick Commit")
}

func openRevertDialog(t *testing.T, h *testHarness, hash, subject string) {
	t.Helper()
	openReplayDialog(t, h, hash, subject, "Revert Commit…", "Revert Commit")
}

// replayAddsFixture builds a divergent history whose commits only add files:
// base.txt on main, then the side branch adds side.txt at p while main adds
// tip.txt at c2. Cherry-picking p onto main applies cleanly — unlike picking
// a commit of main's own linear history, whose diff is already there — and
// reverting c2 removes tip.txt.
func replayAddsFixture(t *testing.T, repo *gitRepoFixture) (p, c2 string) {
	t.Helper()
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CreateBranch("side")
	repo.Checkout("side")
	p = repo.CommitFile("side.txt", "side\n", "add side file")
	repo.Checkout("main")
	c2 = repo.CommitFile("tip.txt", "tip\n", "add tip file")
	return p, c2
}

// replayConflictFixture builds base → c1 (f.txt "picked") → c2 (f.txt
// "diverged"): cherry-picking c1 onto HEAD conflicts in f.txt because both
// sides rewrote the same line.
func replayConflictFixture(t *testing.T, repo *gitRepoFixture) (c1, c2 string) {
	t.Helper()
	repo.CommitFile("f.txt", "line\n", "base commit")
	c1 = repo.CommitFile("f.txt", "picked\n", "change f to picked")
	c2 = repo.CommitFile("f.txt", "diverged\n", "change f to diverged")
	return c1, c2
}

// assertInside verifies the element bounds fall within the container bounds,
// so neither centered drift nor overflow can pass unnoticed.
func assertInside(t *testing.T, inner, outer ui.Rect, what string) {
	t.Helper()
	const tol = 0.5
	if inner.W <= 0 || inner.H <= 0 {
		t.Fatalf("%s has empty bounds %+v", what, inner)
	}
	if inner.X < outer.X-tol || inner.Y < outer.Y-tol ||
		inner.X+inner.W > outer.X+outer.W+tol || inner.Y+inner.H > outer.Y+outer.H+tol {
		t.Fatalf("%s bounds %+v outside container %+v", what, inner, outer)
	}
}

// harnessWidth is the window width newTestApp renders at.
const harnessWidth = 1200

// assertBannerLayout verifies the operation banner at the default window
// width; see assertBannerLayoutIn.
func assertBannerLayout(t *testing.T, h *testHarness) {
	t.Helper()
	assertBannerLayoutIn(t, h, harnessWidth)
}

// assertBannerLayoutIn verifies the operation banner sits inside the main
// content area of a window winW wide and renders its three buttons inside
// its own bounds: no overflow past the window, no missing button.
func assertBannerLayoutIn(t *testing.T, h *testHarness, winW float32) {
	t.Helper()
	const tol = 0.5
	bar, ok := h.ui.AccessByRole(ui.RoleStatus, "Operation in progress")
	if !ok {
		t.Fatalf("operation banner missing; texts=%q", h.ui.Texts())
	}
	if bar.Bounds.W <= 0 || bar.Bounds.X < sidebarWidth-tol || bar.Bounds.X+bar.Bounds.W > winW-16+tol {
		t.Fatalf("operation banner bounds %+v outside the content area %v..%v of a %v-wide window", bar.Bounds, float32(sidebarWidth), winW-16, winW)
	}
	for _, label := range []string{"Continue Operation", "Skip Commit", "Abort Operation"} {
		btn, ok := h.ui.AccessByRole(ui.RoleButton, label)
		if !ok {
			t.Fatalf("banner button %q missing; texts=%q", label, h.ui.Texts())
		}
		assertInside(t, btn.Bounds, bar.Bounds, "banner button "+label)
	}
}

// assertBannerLayoutNarrow shrinks the window to a narrow 820x700, verifies
// the banner still fits inside it with every button visible, and restores
// the default size.
func assertBannerLayoutNarrow(t *testing.T, h *testHarness) {
	t.Helper()
	h.ui.SetSize(820, 700)
	h.ui.Frame()
	assertBannerLayoutIn(t, h, 820)
	h.ui.SetSize(harnessWidth, 800)
	h.ui.Frame()
}

// assertDialogLayout verifies the single open dialog is horizontally centered
// in the window and holds every given button inside its own bounds.
func assertDialogLayout(t *testing.T, h *testHarness, buttons ...string) {
	t.Helper()
	dlg, ok := h.ui.AccessByRole(ui.RoleDialog, "")
	if !ok {
		t.Fatalf("no dialog present; texts=%q", h.ui.Texts())
	}
	if dlg.Bounds.W <= 0 || dlg.Bounds.H <= 0 {
		t.Fatalf("dialog has empty bounds %+v", dlg.Bounds)
	}
	// The tester window is 1200 DIPs wide and the dialog centers horizontally.
	if center := dlg.Bounds.X + dlg.Bounds.W/2; center < 598 || center > 602 {
		t.Fatalf("dialog not horizontally centered: center=%v bounds=%+v", center, dlg.Bounds)
	}
	for _, label := range buttons {
		btn, ok := h.ui.AccessByRole(ui.RoleButton, label)
		if !ok {
			t.Fatalf("dialog button %q missing; texts=%q", label, h.ui.Texts())
		}
		assertInside(t, btn.Bounds, dlg.Bounds, "dialog button "+label)
	}
}

// triggerPickConflict cherry-picks c1 through the real UI entry, producing a
// content conflict in f.txt, and verifies the operation banner appears on the
// History page with Continue disabled and Skip and Abort enabled.
func triggerPickConflict(t *testing.T, h *testHarness, c1, subject string) {
	t.Helper()
	openCherryPickDialog(t, h, c1, subject)
	if err := h.ui.Click("Confirm Cherry-pick"); err != nil {
		t.Fatalf("click Confirm Cherry-pick: %v", err)
	}
	h.drain()
	st := h.app.session.Snapshot().Status
	if st.State != "cherry-pick" {
		t.Fatalf("state after conflicting cherry-pick = %q, want cherry-pick", st.State)
	}
	if !st.Conflicted() {
		t.Fatalf("fixture: cherry-pick of %s did not conflict: %+v", c1, st)
	}
	if st.Operation != c1 {
		t.Fatalf("operation target = %q, want %q", st.Operation, c1)
	}
	if !hasExactText(h.ui, "Cherry-pick in progress. Resolve conflicts and stage the files to continue, or abort.") {
		t.Fatalf("conflict banner copy missing; texts=%q", h.ui.Texts())
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

// bannerGone verifies no frame element of the operation banner survives.
func bannerGone(t *testing.T, h *testHarness) {
	t.Helper()
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Operation in progress"); ok {
		t.Fatalf("operation banner still rendered; texts=%q", h.ui.Texts())
	}
	if hasExactText(h.ui, "Cherry-pick in progress.") || hasExactText(h.ui, "Revert in progress.") {
		t.Fatalf("operation banner text still rendered; texts=%q", h.ui.Texts())
	}
}

// A commit row's context menu offers Cherry-pick on any commit but HEAD, and
// confirming the default options applies the commit as a new commit on top of
// HEAD. The dialog has no mainline parent group for an ordinary commit, lays
// its buttons out inside the dialog, and Cherry-pick on the HEAD commit row
// itself is disabled because picking HEAD onto itself can never apply.
func TestCommitReplayCherryPickThroughContextMenu(t *testing.T) {
	repo := newGitRepo(t, "replay-pick")
	c1, c2 := replayAddsFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// Cherry-pick on the HEAD commit row is disabled, Revert stays enabled.
	row := commitRowLabel(c2, "add tip file")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click HEAD commit row: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Cherry-pick Commit…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing Cherry-pick on HEAD = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click HEAD commit row again: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Revert Commit…"); err != nil {
		t.Fatalf("Revert on HEAD should be enabled: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Revert Commit") {
		t.Fatalf("Revert on HEAD did not open its dialog; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("cancel the revert dialog: %v", err)
	}
	h.ui.Frame()

	openCherryPickDialog(t, h, c1, "add side file")
	if !hasExactText(h.ui, short7(c1)+" add side file") {
		t.Fatalf("dialog missing target line; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleRadio, "Parent 1"); ok {
		t.Fatal("mainline parent group rendered for a non-merge commit")
	}
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Commit the changes"); !ok || !box.Checked {
		t.Fatalf("Commit the changes = %+v found=%v, want present and checked", box, ok)
	}
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Append origin commit SHA (-x)"); !ok || box.Checked {
		t.Fatalf("Append origin commit SHA (-x) = %+v found=%v, want present and unchecked", box, ok)
	}
	assertDialogLayout(t, h, "Cancel", "Confirm Cherry-pick")

	if err := h.ui.Click("Confirm Cherry-pick"); err != nil {
		t.Fatalf("click Confirm Cherry-pick: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("cherry-pick failed: %v", err)
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); subject != "add side file" {
		t.Fatalf("new commit subject = %q, want add side file", subject)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head == c2 {
		t.Fatalf("HEAD did not move after cherry-pick: %q", head)
	}
	assertFileBytes(t, repo, "side.txt", "side\n")
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after clean cherry-pick = %q, want empty", got)
	}
	if hasExactText(h.ui, "Cherry-pick Commit") {
		t.Fatal("cherry-pick dialog still open after a successful pick")
	}
	bannerGone(t, h)
}

// The -x checkbox appends the origin commit line to the cherry-picked commit
// message, and unchecking Commit the changes applies the diff to the index
// and working tree without committing.
func TestCommitReplayCherryPickRecordOrigin(t *testing.T) {
	repo := newGitRepo(t, "replay-pick-x")
	c1, _ := replayAddsFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openCherryPickDialog(t, h, c1, "add side file")

	if err := h.ui.Click("Append origin commit SHA (-x)"); err != nil {
		t.Fatalf("tick -x checkbox: %v", err)
	}
	h.ui.Frame()
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Append origin commit SHA (-x)"); !ok || !box.Checked {
		t.Fatalf("-x checkbox after click = %+v found=%v, want checked", box, ok)
	}
	if err := h.ui.Click("Confirm Cherry-pick"); err != nil {
		t.Fatalf("click Confirm Cherry-pick: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("cherry-pick -x failed: %v", err)
	}
	body := repo.Git("log", "-1", "--format=%B")
	if !strings.Contains(body, "(cherry picked from commit "+c1+")") {
		t.Fatalf("commit body missing origin line: %q", body)
	}
}

// Unchecking Commit the changes leaves the cherry-picked diff staged without
// a new commit: HEAD stays put and the file is in the index and on disk.
func TestCommitReplayCherryPickNoCommit(t *testing.T) {
	repo := newGitRepo(t, "replay-pick-no-commit")
	c1, c2 := replayAddsFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openCherryPickDialog(t, h, c1, "add side file")

	if err := h.ui.Click("Commit the changes"); err != nil {
		t.Fatalf("untick Commit the changes: %v", err)
	}
	h.ui.Frame()
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Commit the changes"); !ok || box.Checked {
		t.Fatalf("Commit the changes after click = %+v found=%v, want unchecked", box, ok)
	}
	if err := h.ui.Click("Confirm Cherry-pick"); err != nil {
		t.Fatalf("click Confirm Cherry-pick: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("cherry-pick --no-commit failed: %v", err)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != c2 {
		t.Fatalf("HEAD moved by --no-commit pick: %q, want %q", head, c2)
	}
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); !strings.Contains(out, "side.txt") {
		t.Fatalf("staged after --no-commit pick = %q, want side.txt", out)
	}
	assertFileBytes(t, repo, "side.txt", "side\n")
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after --no-commit pick = %q, want empty", got)
	}
}

// Reverting a commit through the context menu creates a commit backing its
// changes out. The revert dialog offers no -x checkbox, and the mainline
// group is absent for an ordinary commit.
func TestCommitReplayRevertThroughContextMenu(t *testing.T) {
	repo := newGitRepo(t, "replay-revert")
	_, c2 := replayAddsFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openRevertDialog(t, h, c2, "add tip file")

	if !hasExactText(h.ui, short7(c2)+" add tip file") {
		t.Fatalf("revert dialog missing target line; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Append origin commit SHA (-x)"); ok {
		t.Fatal("revert dialog shows the cherry-pick-only -x checkbox")
	}
	assertDialogLayout(t, h, "Cancel", "Confirm Revert")
	if err := h.ui.Click("Confirm Revert"); err != nil {
		t.Fatalf("click Confirm Revert: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("revert failed: %v", err)
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); subject != `Revert "add tip file"` {
		t.Fatalf("revert commit subject = %q, want Revert \"add tip file\"", subject)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "tip.txt")); !os.IsNotExist(err) {
		t.Fatalf("tip.txt still exists after revert: err=%v", err)
	}
	assertFileBytes(t, repo, "base.txt", "base\n")
}

// Unchecking Commit the changes on a revert stages the backout without
// committing: the deletion is in the index and HEAD stays put.
func TestCommitReplayRevertNoCommit(t *testing.T) {
	repo := newGitRepo(t, "replay-revert-no-commit")
	_, c2 := replayAddsFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openRevertDialog(t, h, c2, "add tip file")
	if err := h.ui.Click("Commit the changes"); err != nil {
		t.Fatalf("untick Commit the changes: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Revert"); err != nil {
		t.Fatalf("click Confirm Revert: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("revert --no-commit failed: %v", err)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != c2 {
		t.Fatalf("HEAD moved by --no-commit revert: %q, want %q", head, c2)
	}
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); !strings.Contains(out, "tip.txt") {
		t.Fatalf("staged after --no-commit revert = %q, want tip.txt", out)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "tip.txt")); !os.IsNotExist(err) {
		t.Fatalf("tip.txt still in working tree after --no-commit revert: err=%v", err)
	}
}

// For a merge commit the dialog offers the Mainline parent radio group with
// Parent 1 preselected: the default cherry-picks the merge relative to its
// first parent, and choosing Parent 2 replays it relative to the second. The
// fixture first backs the merge out with two reverts so both re-picks apply
// cleanly.
func TestCommitReplayMergeMainlineParents(t *testing.T) {
	repo := newGitRepo(t, "replay-merge")
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CreateBranch("side")
	repo.Checkout("side")
	repo.CommitFile("side.txt", "side\n", "side work")
	repo.Checkout("main")
	repo.CommitFile("main.txt", "main\n", "main work")
	repo.Git("merge", "--no-edit", "side")
	merge := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	if got := len(strings.Fields(repo.Git("rev-list", "--parents", "-n", "1", "HEAD"))); got != 3 {
		t.Fatalf("fixture: merge commit has %d parents+self fields, want 3", got)
	}
	// Back the merge out completely: first the side branch's changes relative
	// to parent 1, then the mainline changes relative to parent 2.
	repo.Git("revert", "--no-edit", "-m", "1", merge)
	repo.Git("revert", "--no-edit", "-m", "2", merge)
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("fixture: working tree dirty after reverts: %q", out)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "side.txt")); !os.IsNotExist(err) {
		t.Fatalf("fixture: side.txt survived the reverts: err=%v", err)
	}
	reverted := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openCherryPickDialog(t, h, merge, "Merge branch 'side'")

	if radio, ok := h.ui.AccessByRole(ui.RoleRadio, "Parent 1"); !ok || !radio.Checked {
		t.Fatalf("Parent 1 = %+v found=%v, want present and preselected", radio, ok)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleRadio, "Parent 2"); !ok {
		t.Fatalf("Parent 2 missing; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "Mainline parent:") {
		t.Fatalf("Mainline parent heading not shown; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Cherry-pick"); err != nil {
		t.Fatalf("click Confirm Cherry-pick: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("cherry-pick of merge with parent 1 failed: %v", err)
	}
	assertFileBytes(t, repo, "side.txt", "side\n")
	if _, err := os.Stat(filepath.Join(repo.Path(), "main.txt")); !os.IsNotExist(err) {
		t.Fatalf("main.txt restored by a parent-1 pick: err=%v", err)
	}

	// Reset back to the backed-out state and re-pick relative to parent 2.
	repo.Git("reset", "--hard", "-q", reverted)
	h.app.session.Reload()
	h.drain()
	openCherryPickDialog(t, h, merge, "Merge branch 'side'")
	if err := h.ui.Click("Parent 2"); err != nil {
		t.Fatalf("click Parent 2 radio: %v", err)
	}
	h.ui.Frame()
	if radio, ok := h.ui.AccessByRole(ui.RoleRadio, "Parent 2"); !ok || !radio.Checked {
		t.Fatalf("Parent 2 after click = %+v found=%v, want checked", radio, ok)
	}
	if err := h.ui.Click("Confirm Cherry-pick"); err != nil {
		t.Fatalf("click Confirm Cherry-pick: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("cherry-pick of merge with parent 2 failed: %v", err)
	}
	assertFileBytes(t, repo, "main.txt", "main\n")
	if _, err := os.Stat(filepath.Join(repo.Path(), "side.txt")); !os.IsNotExist(err) {
		t.Fatalf("side.txt restored by a parent-2 pick: err=%v", err)
	}
}

// Canceling the dialog — however far the draft was driven — writes nothing
// and clears the draft, so reopening starts again at the defaults.
func TestCommitReplayCancelLeavesRepositoryUntouched(t *testing.T) {
	repo := newGitRepo(t, "replay-cancel")
	c1, c2 := replayAddsFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openCherryPickDialog(t, h, c1, "add side file")

	if err := h.ui.Click("Append origin commit SHA (-x)"); err != nil {
		t.Fatalf("tick -x checkbox: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Commit the changes"); err != nil {
		t.Fatalf("untick Commit the changes: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	if hasExactText(h.ui, "Cherry-pick Commit") {
		t.Fatal("dialog still open after Cancel")
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != c2 {
		t.Fatalf("HEAD moved by a canceled dialog: %q, want %q", head, c2)
	}
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("canceled dialog dirtied the working tree: %q", out)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Cancel produced an action error: %v", err)
	}

	// Reopening starts from a clean draft: committed and not recording origin.
	openCherryPickDialog(t, h, c1, "add side file")
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Commit the changes"); !ok || !box.Checked {
		t.Fatalf("Commit the changes after reopen = %+v found=%v, want the checked default", box, ok)
	}
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Append origin commit SHA (-x)"); !ok || box.Checked {
		t.Fatalf("-x checkbox after reopen = %+v found=%v, want the unchecked default", box, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel again: %v", err)
	}
	h.ui.Frame()
}

// While a merge is in progress both replay menu items are disabled and cannot
// be chosen, because replaying on top of an operation would corrupt it.
func TestCommitReplayMenuItemsDisabledDuringOperation(t *testing.T) {
	repo := newGitRepo(t, "replay-merge-busy")
	repo.CommitFile("conflict.txt", "base\n", "base commit")
	repo.CreateBranch("side")
	repo.WriteFile("conflict.txt", "main\n")
	repo.Git("add", "--", "conflict.txt")
	repo.Git("commit", "-m", "main edits")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.Checkout("side")
	repo.WriteFile("conflict.txt", "side\n")
	repo.Git("add", "--", "conflict.txt")
	repo.Git("commit", "-m", "side edits")
	repo.Checkout("main")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("conflicting merge unexpectedly succeeded: %s", out)
	}

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	if got := h.app.session.Snapshot().Status.State; got != "merge" {
		t.Fatalf("fixture: state = %q, want merge", got)
	}

	row := commitRowLabel(head, "main edits")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row: %v", err)
	}
	for _, item := range []string{"Cherry-pick Commit…", "Revert Commit…"} {
		if !slices.Contains(h.ui.Menu(), item) {
			t.Fatalf("menu %q missing the disabled item %q", h.ui.Menu(), item)
		}
		if err := h.ui.ChooseMenuItem(item); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing %q during a merge = %v, want a disabled error", item, err)
		}
	}
	h.ui.CloseMenu()
	if hasExactText(h.ui, "Cherry-pick Commit") || hasExactText(h.ui, "Revert Commit") {
		t.Fatal("replay dialog opened from a disabled menu item")
	}
}

// While a repository write is busy both replay menu items are disabled, so a
// replay cannot start over the running write.
func TestCommitReplayMenuItemsDisabledWhileBusy(t *testing.T) {
	repo := newGitRepo(t, "replay-busy")
	c1, _ := replayAddsFixture(t, repo)
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

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History while busy: %v", err)
	}
	h.ui.Frame()
	row := commitRowLabel(c1, "add side file")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row while busy: %v", err)
	}
	for _, item := range []string{"Cherry-pick Commit…", "Revert Commit…"} {
		if err := h.ui.ChooseMenuItem(item); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing %q while busy = %v, want a disabled error", item, err)
		}
	}
	h.ui.CloseMenu()

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage write failed: %v", err)
	}
}

// Uncommitted tracked changes — unstaged or staged — refuse the replay before
// Git runs: the error explains the refusal in the toolbar, and every byte of
// the working tree, the index and HEAD is exactly as before.
func TestCommitReplayDirtyGuardProtectsWorkingTree(t *testing.T) {
	repo := newGitRepo(t, "replay-dirty")
	c1, c2 := replayAddsFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// Round 1: an unstaged tracked modification refuses the cherry-pick.
	repo.WriteFile("base.txt", "base\ndirty\n")
	openCherryPickDialog(t, h, c1, "add side file")
	if err := h.ui.Click("Confirm Cherry-pick"); err != nil {
		t.Fatalf("click Confirm Cherry-pick: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "cannot cherry-pick with uncommitted local changes")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != c2 {
		t.Fatalf("HEAD moved by the refused cherry-pick: %q, want %q", head, c2)
	}
	assertFileBytes(t, repo, "base.txt", "base\ndirty\n")
	if _, err := os.Stat(filepath.Join(repo.Path(), "side.txt")); err == nil {
		t.Fatal("refused cherry-pick still created side.txt")
	}
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); out != "" {
		t.Fatalf("index touched by the refused cherry-pick: %q", out)
	}

	// Round 2: a staged change refuses the revert the same way.
	repo.Git("checkout", "--", "base.txt")
	repo.WriteFile("base.txt", "base\nstaged\n")
	repo.Git("add", "--", "base.txt")
	openRevertDialog(t, h, c2, "add tip file")
	if err := h.ui.Click("Confirm Revert"); err != nil {
		t.Fatalf("click Confirm Revert: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "cannot revert with uncommitted local changes")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != c2 {
		t.Fatalf("HEAD moved by the refused revert: %q, want %q", head, c2)
	}
	assertFileBytes(t, repo, "base.txt", "base\nstaged\n")
	assertFileBytes(t, repo, "tip.txt", "tip\n")
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); !strings.Contains(out, "base.txt") {
		t.Fatalf("staged change lost by the refused revert: %q", out)
	}
}

// When the background guard cannot even read the repository state, the replay
// never runs: HEAD, the index and every byte of the working tree are exactly
// as before, and the failure shows in the toolbar.
func TestCommitReplayGuardStatusFailure(t *testing.T) {
	repo := newGitRepo(t, "replay-guard-status")
	c1, c2 := replayAddsFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openCherryPickDialog(t, h, c1, "add side file")

	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	if err := h.ui.Click("Confirm Cherry-pick"); err != nil {
		t.Fatalf("click Confirm Cherry-pick: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "reading the repository state failed")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != c2 {
		t.Fatalf("HEAD moved after a failed status read: %q, want %q", head, c2)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "side.txt")); err == nil {
		t.Fatal("cherry-pick ran despite the failed status read")
	}
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("working tree touched after a failed status read: %q", out)
	}
	if count := gitOutTrim(t, repo.Path(), "rev-list", "--count", "HEAD"); count != "2" {
		t.Fatalf("commit count after failed status read = %q, want 2", count)
	}
}

// When HEAD moved after the dialog opened — here an external commit — the
// guard refuses the replay the user confirmed against the old state.
func TestCommitReplayGuardHeadMoved(t *testing.T) {
	repo := newGitRepo(t, "replay-guard-head")
	c1, _ := replayAddsFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openCherryPickDialog(t, h, c1, "add side file")

	repo.WriteFile("other.txt", "other\n")
	repo.Git("add", "--", "other.txt")
	repo.Git("commit", "-m", "external commit")
	moved := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	if err := h.ui.Click("Confirm Cherry-pick"); err != nil {
		t.Fatalf("click Confirm Cherry-pick: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "HEAD or the current branch changed")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != moved {
		t.Fatalf("HEAD moved by the refused cherry-pick: %q, want %q", head, moved)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "side.txt")); err == nil {
		t.Fatal("cherry-pick ran despite the moved HEAD")
	}
}

// When the branch itself changed after the dialog opened, the guard refuses
// the replay even though HEAD's hash did not move.
func TestCommitReplayGuardBranchSwitched(t *testing.T) {
	repo := newGitRepo(t, "replay-guard-branch")
	c1, c2 := replayAddsFixture(t, repo)
	repo.CreateBranch("feature")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openCherryPickDialog(t, h, c1, "add side file")

	repo.Checkout("feature")

	if err := h.ui.Click("Confirm Cherry-pick"); err != nil {
		t.Fatalf("click Confirm Cherry-pick: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "HEAD or the current branch changed")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != c2 {
		t.Fatalf("HEAD moved by the refused cherry-pick: %q, want %q", head, c2)
	}
	if got := gitOutTrim(t, repo.Path(), "symbolic-ref", "--short", "HEAD"); got != "feature" {
		t.Fatalf("branch after the refused cherry-pick = %q, want feature", got)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "side.txt")); err == nil {
		t.Fatal("cherry-pick ran despite the branch switch")
	}
}

// An untracked file sharing the name of a file the replayed commit adds makes
// Git refuse to overwrite it: the error surfaces, and the untracked file's
// bytes are exactly as before. Untracked files are not uncommitted tracked
// changes, so the guard passes the write to Git and Git itself is the second
// line of defense.
func TestCommitReplayUntrackedCollisionPreservesFile(t *testing.T) {
	repo := newGitRepo(t, "replay-collision")
	c1, c2 := replayAddsFixture(t, repo)
	// Picking the side commit adds side.txt to main, which an untracked
	// side.txt collides with.
	repo.WriteFile("side.txt", "precious untracked\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openCherryPickDialog(t, h, c1, "add side file")
	if err := h.ui.Click("Confirm Cherry-pick"); err != nil {
		t.Fatalf("click Confirm Cherry-pick: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "")
	assertFileBytes(t, repo, "side.txt", "precious untracked\n")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != c2 {
		t.Fatalf("HEAD moved by the colliding cherry-pick: %q, want %q", head, c2)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after collision = %q, want empty", got)
	}
}

// A conflicting cherry-pick shows the banner on both the History and the
// Local Changes pages, lays its buttons inside the banner, and canceling the
// Abort confirmation preserves the operation exactly as it was.
func TestCommitReplayConflictBannerAndAbortCancel(t *testing.T) {
	repo := newGitRepo(t, "replay-conflict-cancel")
	c1, c2 := replayConflictFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	triggerPickConflict(t, h, c1, "change f to picked")
	assertBannerLayoutNarrow(t, h)

	// The banner is on the Local Changes page too, where the conflicted file
	// is listed for resolution.
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Cherry-pick in progress. Resolve conflicts and stage the files to continue, or abort.") {
		t.Fatalf("conflict banner missing on Local Changes; texts=%q", h.ui.Texts())
	}
	assertBannerLayout(t, h)
	assertBannerLayoutNarrow(t, h)
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, "Conflicted f.txt"); !ok {
		t.Fatalf("conflicted f.txt not listed on Local Changes; texts=%q", h.ui.Texts())
	}

	// Open the Abort confirmation here and cancel it: the operation, the
	// conflict and the banner all survive.
	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Abort cherry-pick?") {
		t.Fatalf("abort confirmation missing; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "All uncommitted changes from this operation will be discarded.") {
		t.Fatalf("abort warning missing; texts=%q", h.ui.Texts())
	}
	assertDialogLayout(t, h, "Cancel", "Confirm Abort")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	if hasExactText(h.ui, "Abort cherry-pick?") {
		t.Fatal("abort confirmation still open after Cancel")
	}
	st := h.app.session.Snapshot().Status
	if st.State != "cherry-pick" || !st.Conflicted() {
		t.Fatalf("Cancel disturbed the operation: state=%q conflicted=%v", st.State, st.Conflicted())
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("Cancel resolved the conflict stages")
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt")); err != nil || !strings.Contains(string(content), "<<<<<<<") {
		t.Fatalf("Cancel touched the conflicted file: content=%q err=%v", content, err)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != c2 {
		t.Fatalf("HEAD moved by the canceled abort: %q, want %q", head, c2)
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("no action error recorded for the conflicting pick the banner came from")
	}
}

// Confirming the Abort restores the repository to the state before the
// cherry-pick: HEAD back at the pre-pick commit, the working tree clean, the
// conflicted file back to its bytes and the banner gone.
func TestCommitReplayConflictAbortConfirmRestores(t *testing.T) {
	repo := newGitRepo(t, "replay-conflict-abort")
	c1, c2 := replayConflictFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	triggerPickConflict(t, h, c1, "change f to picked")

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
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != c2 {
		t.Fatalf("HEAD after abort = %q, want pre-pick %q", head, c2)
	}
	assertFileBytes(t, repo, "f.txt", "diverged\n")
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("working tree dirty after abort: %q", out)
	}
	bannerGone(t, h)
}

// When the recovery guard cannot read the repository state, the abort never
// runs: the operation, the conflict stages and the conflicted file's bytes
// are exactly as before, and the failure shows in the toolbar.
func TestCommitReplayConflictAbortGuardStatusFailure(t *testing.T) {
	repo := newGitRepo(t, "replay-abort-status")
	c1, _ := replayConflictFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	triggerPickConflict(t, h, c1, "change f to picked")

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
	if st.State != "cherry-pick" {
		t.Fatalf("operation lost after a failed status read: state=%q", st.State)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("conflict stages lost after a failed status read")
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt")); err != nil || !strings.Contains(string(content), "<<<<<<<") {
		t.Fatalf("conflicted file touched after a failed status read: content=%q err=%v", content, err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "--verify", "--quiet", "CHERRY_PICK_HEAD"); strings.TrimSpace(got) == "" {
		t.Fatal("CHERRY_PICK_HEAD lost after a failed status read")
	}
}

// When the operation changed underneath the open confirmation — here aborted
// externally and replaced by a different conflicting cherry-pick of the same
// kind on the same HEAD — the guard refuses the abort: the replacement
// operation is not the one the user confirmed against, so it and its conflict
// survive untouched.
func TestCommitReplayConflictAbortGuardOperationChanged(t *testing.T) {
	repo := newGitRepo(t, "replay-abort-changed")
	repo.CommitFile("f.txt", "line\n", "base commit")
	repo.CreateBranch("side")
	repo.Checkout("side")
	c1a := repo.CommitFile("f.txt", "picked-a\n", "side pick a")
	c1b := repo.CommitFile("f.txt", "picked-b\n", "side pick b")
	repo.Checkout("main")
	repo.CommitFile("f.txt", "diverged\n", "main diverges")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	triggerPickConflict(t, h, c1a, "side pick a")

	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()

	// An external actor aborts the operation the user saw and starts another
	// one of the same kind on the same HEAD: only the target differs.
	repo.Git("cherry-pick", "--abort")
	if out, err := tryGitCommand(repo.Path(), "cherry-pick", c1b); err == nil {
		t.Fatalf("replacement cherry-pick unexpectedly clean: %s", out)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "CHERRY_PICK_HEAD"); got != c1b {
		t.Fatalf("fixture: replacement operation target = %q, want %q", got, c1b)
	}

	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "active operation changed")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "--verify", "--quiet", "CHERRY_PICK_HEAD"); strings.TrimSpace(got) != c1b {
		t.Fatalf("replacement operation destroyed by the refused abort: CHERRY_PICK_HEAD=%q want %q", got, c1b)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("replacement conflict resolved by the refused abort")
	}
}

// After the user resolves the conflict on disk and stages the file through
// the real Local Changes UI, Continue enables and finishes the cherry-pick:
// the commit lands with the original message, the resolution is its content,
// and the banner is gone.
func TestCommitReplayConflictContinueAfterResolution(t *testing.T) {
	repo := newGitRepo(t, "replay-conflict-continue")
	c1, c2 := replayConflictFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	triggerPickConflict(t, h, c1, "change f to picked")

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
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); subject != "change f to picked" {
		t.Fatalf("continued commit subject = %q, want change f to picked", subject)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head == c2 {
		t.Fatal("HEAD did not move after continue")
	}
	assertFileBytes(t, repo, "f.txt", "resolved\n")
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after continue = %q, want empty", got)
	}
	bannerGone(t, h)
}

// Skipping the conflicted commit drops it and ends the operation: no new
// commit, the working tree back to the pre-pick bytes and the banner gone.
func TestCommitReplayConflictSkipDropsPick(t *testing.T) {
	repo := newGitRepo(t, "replay-conflict-skip")
	c1, c2 := replayConflictFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	triggerPickConflict(t, h, c1, "change f to picked")

	if err := h.ui.Click("Skip Commit"); err != nil {
		t.Fatalf("click Skip Commit: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("skip failed: %v", err)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != c2 {
		t.Fatalf("HEAD moved by skip: %q, want %q", head, c2)
	}
	if count := gitOutTrim(t, repo.Path(), "rev-list", "--count", "HEAD"); count != "3" {
		t.Fatalf("skip created a commit: count %q, want 3", count)
	}
	assertFileBytes(t, repo, "f.txt", "diverged\n")
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after skip = %q, want empty", got)
	}
	bannerGone(t, h)
}

// If conflicts reappear between the click on Continue and the worker's live
// read — here the conflict stages are recreated in the index after the user
// staged the resolution — the guard refuses to continue: the operation keeps
// waiting instead of committing over unresolved conflicts.
func TestCommitReplayContinueGuardRefusesRecreatedConflict(t *testing.T) {
	repo := newGitRepo(t, "replay-continue-conflicted")
	c1, _ := replayConflictFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	triggerPickConflict(t, h, c1, "change f to picked")

	// Capture the conflict stages, resolve and stage through the real UI.
	stages := repo.Git("ls-files", "-u")
	repo.WriteFile("f.txt", "resolved\n")
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Mark Resolved f.txt"); err != nil {
		t.Fatalf("click Mark Resolved f.txt: %v", err)
	}
	h.drain()

	// Recreate the conflict in the index before the click's worker reads:
	// remove the merged entry and feed the captured stages back.
	repo.Git("update-index", "-q", "--force-remove", "f.txt")
	if out, err := tryGitCommandStdin(repo.Path(), stages, "update-index", "--index-info"); err != nil {
		t.Fatalf("recreate conflict stages: %v\n%s", err, out)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("fixture: failed to recreate conflict stages")
	}

	if err := h.ui.Click("Continue Operation"); err != nil {
		t.Fatalf("click Continue Operation: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "resolve conflicts and stage the files first")
	if got := h.app.session.Snapshot().Status.State; got != "cherry-pick" {
		t.Fatalf("operation lost after refused continue: state=%q", got)
	}
	if count := gitOutTrim(t, repo.Path(), "rev-list", "--count", "HEAD"); count != "3" {
		t.Fatalf("refused continue created a commit: count %q, want 3", count)
	}
}

// Switching the repository closes an open replay dialog or abort
// confirmation and clears their drafts: the captured target, HEAD, branch
// and operation belong to the repository that is gone.
func TestCommitReplayModalsCloseOnRepoSwitch(t *testing.T) {
	repoA := newGitRepo(t, "replay-switch-a")
	c1, _ := replayAddsFixture(t, repoA)
	repoB := newGitRepo(t, "replay-switch-b")
	repoB.CommitFile("b.txt", "b\n", "commit in beta")

	h := newTestApp(t, repoB.StoreRepo())
	openHistoryPage(t, h, repoA.StoreRepo())
	openCherryPickDialog(t, h, c1, "add side file")

	saved, ok := h.store.Get(h.store.Repos()[0].ID)
	if !ok {
		t.Fatal("seeded repository missing from the store")
	}
	h.app.openRepo(saved)
	h.ui.Frame()
	h.drain()

	st := commitReplayUI(h.app)
	if st.replayOpen {
		t.Fatal("replay dialog still open after the repository switch")
	}
	if st.replayTarget.Hash != "" || st.replayExpectedHead != "" || st.replayExpectedBranch != "" {
		t.Fatalf("replay draft survived the switch: %+v", st)
	}
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History after switch: %v", err)
	}
	h.ui.Frame()
	if hasExactText(h.ui, "Cherry-pick Commit") {
		t.Fatal("replay dialog rendered again after the repository switch")
	}
}

// Switching the repository with an open Abort confirmation closes it and
// drops the captured operation, exactly as it does for the replay dialog.
func TestCommitReplayAbortModalClosesOnRepoSwitch(t *testing.T) {
	repoA := newGitRepo(t, "replay-abort-switch-a")
	c1, _ := replayConflictFixture(t, repoA)
	repoB := newGitRepo(t, "replay-abort-switch-b")
	repoB.CommitFile("b.txt", "b\n", "commit in beta")

	h := newTestApp(t, repoB.StoreRepo())
	openHistoryPage(t, h, repoA.StoreRepo())
	triggerPickConflict(t, h, c1, "change f to picked")

	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Abort cherry-pick?") {
		t.Fatalf("abort confirmation did not open; texts=%q", h.ui.Texts())
	}

	saved, ok := h.store.Get(h.store.Repos()[0].ID)
	if !ok {
		t.Fatal("seeded repository missing from the store")
	}
	h.app.openRepo(saved)
	h.ui.Frame()
	h.drain()

	st := commitReplayUI(h.app)
	if st.abortOpen {
		t.Fatal("abort confirmation still open after the repository switch")
	}
	if st.abortTarget != (operationIdentity{}) {
		t.Fatalf("abort target survived the switch: %+v", st.abortTarget)
	}
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History after switch: %v", err)
	}
	h.ui.Frame()
	if hasExactText(h.ui, "Abort cherry-pick?") {
		t.Fatal("abort confirmation rendered again after the repository switch")
	}
}

// A conflicting revert shows the revert variant of the banner — its own copy
// and its own abort title — and confirming Abort restores the repository to
// the state before the revert.
func TestCommitReplayRevertConflictBannerAndAbort(t *testing.T) {
	repo := newGitRepo(t, "replay-revert-conflict")
	c1, c2 := replayConflictFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openRevertDialog(t, h, c1, "change f to picked")
	if err := h.ui.Click("Confirm Revert"); err != nil {
		t.Fatalf("click Confirm Revert: %v", err)
	}
	h.drain()

	st := h.app.session.Snapshot().Status
	if st.State != "revert" {
		t.Fatalf("state after conflicting revert = %q, want revert", st.State)
	}
	if !st.Conflicted() {
		t.Fatalf("fixture: revert of %s did not conflict: %+v", c1, st)
	}
	if st.Operation != c1 {
		t.Fatalf("operation target = %q, want %q", st.Operation, c1)
	}
	if !hasExactText(h.ui, "Revert in progress. Resolve conflicts and stage the files to continue, or abort.") {
		t.Fatalf("revert conflict banner copy missing; texts=%q", h.ui.Texts())
	}
	assertBannerLayout(t, h)
	assertBannerLayoutNarrow(t, h)

	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Revert in progress. Resolve conflicts and stage the files to continue, or abort.") {
		t.Fatalf("revert conflict banner missing on Local Changes; texts=%q", h.ui.Texts())
	}
	assertBannerLayout(t, h)
	assertBannerLayoutNarrow(t, h)

	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Abort revert?") {
		t.Fatalf("revert abort title missing; texts=%q", h.ui.Texts())
	}
	assertDialogLayout(t, h, "Cancel", "Confirm Abort")
	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("revert abort failed: %v", err)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after revert abort = %q, want empty", got)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != c2 {
		t.Fatalf("HEAD after revert abort = %q, want pre-revert %q", head, c2)
	}
	assertFileBytes(t, repo, "f.txt", "diverged\n")
	bannerGone(t, h)
}

// --- SSH loopback ---

// remoteReplayAdds builds the divergent adds fixture on the server: the side
// branch adds side.txt at p, main adds tip.txt at c2, so picking p onto main
// applies cleanly.
func remoteReplayAdds(t *testing.T, srv *sshtest.Target, dir string) (p, c2 string) {
	t.Helper()
	srv.RunGit(t, dir, "checkout", "-q", "-b", "side")
	p = remoteCommitFile(t, srv, dir, "side.txt", "side\n", "add side file")
	srv.RunGit(t, dir, "checkout", "-q", "main")
	c2 = remoteCommitFile(t, srv, dir, "tip.txt", "tip\n", "add tip file")
	return p, c2
}

// remoteReplayConflict builds the conflict fixture on the server: f.txt
// "line" → c1 "picked" → c2 "diverged".
func remoteReplayConflict(t *testing.T, srv *sshtest.Target, dir string) (c1, c2 string) {
	t.Helper()
	srv.WriteFile(t, dir, "f.txt", []byte("line\n"))
	srv.RunGit(t, dir, "add", "--", "f.txt")
	srv.RunGit(t, dir, "commit", "-q", "-m", "f base")
	c1 = remoteCommitFile(t, srv, dir, "f.txt", "picked\n", "change f to picked")
	c2 = remoteCommitFile(t, srv, dir, "f.txt", "diverged\n", "change f to diverged")
	return c1, c2
}

// Over a real SSH connection the full cherry-pick chain works through the
// context menu: a plain pick lands as a new commit, -x appends the origin
// line, and --no-commit stages the diff without committing. Every assertion
// reads the server's repository through a second SSH channel.
func TestCommitReplayCherryPickLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	c1, c2 := remoteReplayAdds(t, srv, repo.Path)

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)

	// Plain cherry-pick of c1.
	openCherryPickDialog(t, h, c1, "add side file")
	if err := h.ui.Click("Confirm Cherry-pick"); err != nil {
		t.Fatalf("click Confirm Cherry-pick: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH cherry-pick failed: %v", err)
	}
	if subject := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "-1", "--format=%s")); subject != "add side file" {
		t.Fatalf("remote new commit subject = %q, want add side file", subject)
	}
	if got := srv.ReadFile(t, repo.Path, "side.txt"); string(got) != "side\n" {
		t.Fatalf("remote side.txt after pick = %q, want side", string(got))
	}

	// Undo the pick externally, then pick again with -x: the origin line
	// names the full hash of c1.
	srv.RunGit(t, repo.Path, "reset", "--hard", "-q", c2)
	h.app.session.Reload()
	h.drain()
	openCherryPickDialog(t, h, c1, "add side file")
	if err := h.ui.Click("Append origin commit SHA (-x)"); err != nil {
		t.Fatalf("tick -x checkbox: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Cherry-pick"); err != nil {
		t.Fatalf("click Confirm Cherry-pick: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH cherry-pick -x failed: %v", err)
	}
	if body := srv.RunGit(t, repo.Path, "log", "-1", "--format=%B"); !strings.Contains(body, "(cherry picked from commit "+c1+")") {
		t.Fatalf("remote commit body missing origin line: %q", body)
	}

	// Undo once more, then pick with Commit the changes unchecked: the diff
	// lands staged, HEAD stays at c2.
	srv.RunGit(t, repo.Path, "reset", "--hard", "-q", c2)
	h.app.session.Reload()
	h.drain()
	openCherryPickDialog(t, h, c1, "add side file")
	if err := h.ui.Click("Commit the changes"); err != nil {
		t.Fatalf("untick Commit the changes: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Cherry-pick"); err != nil {
		t.Fatalf("click Confirm Cherry-pick: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH cherry-pick --no-commit failed: %v", err)
	}
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); head != c2 {
		t.Fatalf("remote HEAD moved by --no-commit pick: %q, want %q", head, c2)
	}
	if out := srv.RunGit(t, repo.Path, "diff", "--cached", "--name-only"); !strings.Contains(out, "side.txt") {
		t.Fatalf("remote staged after --no-commit pick = %q, want side.txt", out)
	}
	if got := srv.ReadFile(t, repo.Path, "side.txt"); string(got) != "side\n" {
		t.Fatalf("remote side.txt after --no-commit pick = %q, want side", string(got))
	}
}

// Over SSH a revert through the context menu backs the commit out as a new
// commit, and uncommitted tracked changes refuse both replay kinds before Git
// runs — the dirty bytes and the staged entry survive — as does a failed
// status read.
func TestCommitReplayRevertAndGuardsLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	c1, c2 := remoteReplayAdds(t, srv, repo.Path)

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)

	// Revert c2: tip.txt is gone, everything else intact.
	openRevertDialog(t, h, c2, "add tip file")
	if err := h.ui.Click("Confirm Revert"); err != nil {
		t.Fatalf("click Confirm Revert: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH revert failed: %v", err)
	}
	if subject := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "-1", "--format=%s")); subject != `Revert "add tip file"` {
		t.Fatalf("remote revert subject = %q, want Revert \"add tip file\"", subject)
	}
	if out := srv.RunGit(t, repo.Path, "status", "--porcelain", "--", "tip.txt"); strings.TrimSpace(out) != "" {
		t.Fatalf("remote tip.txt still present after revert: %q", out)
	}
	if got := srv.ReadFile(t, repo.Path, "a.txt"); string(got) != "one\n" {
		t.Fatalf("remote a.txt after revert = %q, want one", string(got))
	}

	// Dirty working tree refuses the cherry-pick: bytes, index and HEAD are
	// exactly as before, and the error shows.
	headBefore := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	srv.WriteFile(t, repo.Path, "a.txt", []byte("dirty\n"))
	h.app.session.Reload()
	h.drain()
	openCherryPickDialog(t, h, c1, "add side file")
	if err := h.ui.Click("Confirm Cherry-pick"); err != nil {
		t.Fatalf("click Confirm Cherry-pick: %v", err)
	}
	h.drain()
	assertRepoErrorShown(t, h, "cannot cherry-pick with uncommitted local changes")
	if got := srv.ReadFile(t, repo.Path, "a.txt"); string(got) != "dirty\n" {
		t.Fatalf("remote a.txt overwritten by refused pick: %q", string(got))
	}
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); head != headBefore {
		t.Fatalf("remote HEAD moved by refused pick: %q, want %q", head, headBefore)
	}

	// A failed status read refuses the pick before Git runs.
	srv.RunGit(t, repo.Path, "checkout", "--", "a.txt")
	h.app.session.Reload()
	h.drain()
	openCherryPickDialog(t, h, c1, "add side file")
	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	if err := h.ui.Click("Confirm Cherry-pick"); err != nil {
		t.Fatalf("click Confirm Cherry-pick: %v", err)
	}
	h.drain()
	assertRepoErrorShown(t, h, "reading the repository state failed")
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); head != headBefore {
		t.Fatalf("remote HEAD moved after failed status read: %q, want %q", head, headBefore)
	}
	if out := srv.RunGit(t, repo.Path, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Fatalf("remote working tree touched after failed status read: %q", out)
	}
}

// Over SSH the conflict banner appears on both pages, canceling Abort keeps
// the operation, a failed status read and a replaced operation both refuse
// the abort without destroying the conflict, and confirming Abort restores
// the pre-pick state.
func TestCommitReplayConflictAbortLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	srv.WriteFile(t, repo.Path, "f.txt", []byte("line\n"))
	srv.RunGit(t, repo.Path, "add", "--", "f.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "f base")
	srv.RunGit(t, repo.Path, "checkout", "-q", "-b", "side")
	c1a := remoteCommitFile(t, srv, repo.Path, "f.txt", "picked-a\n", "side pick a")
	c1b := remoteCommitFile(t, srv, repo.Path, "f.txt", "picked-b\n", "side pick b")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	c2 := remoteCommitFile(t, srv, repo.Path, "f.txt", "diverged\n", "main diverges")

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)
	triggerPickConflict(t, h, c1a, "side pick a")

	// Banner on the Local Changes page too.
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Cherry-pick in progress. Resolve conflicts and stage the files to continue, or abort.") {
		t.Fatalf("SSH conflict banner missing on Local Changes; texts=%q", h.ui.Texts())
	}
	assertBannerLayout(t, h)
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()

	// Canceling the Abort confirmation keeps the operation and the conflict.
	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	assertDialogLayout(t, h, "Cancel", "Confirm Abort")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "--verify", "--quiet", "CHERRY_PICK_HEAD")); got != c1a {
		t.Fatalf("Cancel disturbed the operation: CHERRY_PICK_HEAD=%q want %q", got, c1a)
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
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "--verify", "--quiet", "CHERRY_PICK_HEAD")); got != c1a {
		t.Fatalf("failed status read destroyed the operation: CHERRY_PICK_HEAD=%q want %q", got, c1a)
	}

	// A replaced operation — same kind, same HEAD, different target — refuses
	// the abort the user confirmed against the original.
	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	srv.RunGit(t, repo.Path, "cherry-pick", "--abort")
	// The replacement pick conflicts like the original; run it through the
	// shell because RunGit fails the test on the nonzero exit a conflict
	// produces.
	srv.RunShell(t, "GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -C "+git.ShellQuote(repo.Path)+" cherry-pick "+c1b+" >/dev/null 2>&1 || true")
	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()
	assertRepoErrorShown(t, h, "active operation changed")
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "--verify", "--quiet", "CHERRY_PICK_HEAD")); got != c1b {
		t.Fatalf("replacement operation destroyed by refused abort: CHERRY_PICK_HEAD=%q want %q", got, c1b)
	}

	// Abort the replacement for real: HEAD, the working tree and the banner
	// all return to the pre-pick state.
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
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); head != c2 {
		t.Fatalf("remote HEAD after abort = %q, want pre-pick %q", head, c2)
	}
	if got := srv.ReadFile(t, repo.Path, "f.txt"); string(got) != "diverged\n" {
		t.Fatalf("remote f.txt after abort = %q, want diverged", string(got))
	}
	if out := srv.RunGit(t, repo.Path, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Fatalf("remote working tree dirty after abort: %q", out)
	}
	bannerGone(t, h)
}

// Over SSH the resolution flow completes the cherry-pick: the conflicted file
// is resolved on the server, staged through the real Local Changes UI, and
// Continue finishes the operation with the original commit message.
func TestCommitReplayConflictContinueLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	c1, c2 := remoteReplayConflict(t, srv, repo.Path)

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)
	triggerPickConflict(t, h, c1, "change f to picked")

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
	if subject := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "-1", "--format=%s")); subject != "change f to picked" {
		t.Fatalf("remote continued commit subject = %q, want change f to picked", subject)
	}
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); head == c2 {
		t.Fatal("remote HEAD did not move after continue")
	}
	if got := srv.ReadFile(t, repo.Path, "f.txt"); string(got) != "resolved\n" {
		t.Fatalf("remote f.txt after continue = %q, want resolved", string(got))
	}
	bannerGone(t, h)
}
