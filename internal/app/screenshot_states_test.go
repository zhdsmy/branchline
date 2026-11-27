package app

import (
	"strings"
	"testing"
	"time"
)

// The setup functions of the screenshot scenes added after the first ten.
// Every repository here is built from fixed committer dates and every dialog
// is opened through its real entry point — a menu item, a row click or a
// button, never a direct state write — so the capture covers the same pixels
// a user sees. Scenes ending in -dialog or -modal are cropped to their modal
// card through screenshotCropDialog; history-compare keeps the full window.

// buildLinearSnapshotRepo builds a deterministic four-commit chain on main
// for the History scenes: distinct subjects, files and fixed committer dates
// so hashes, rows and dialog targets are identical on every run.
func buildLinearSnapshotRepo(t *testing.T, name string) (*gitRepoFixture, []string) {
	t.Helper()
	r := newDetGitRepo(t, name)
	base := screenshotFixtureTime
	hashes := []string{
		detCommitFile(t, r, base, "alpha.txt", "alpha v1\n", "seed alpha"),
		detCommitFile(t, r, base.Add(time.Hour), "alpha.txt", "alpha v1\nalpha v2\n", "grow alpha"),
		detCommitFile(t, r, base.Add(2*time.Hour), "beta.txt", "beta v1\n", "add beta"),
		detCommitFile(t, r, base.Add(3*time.Hour), "main.go", "package main\n\nfunc main() {}\n", "add main program"),
	}
	return r, hashes
}

// setupScreenshotHistoryCompare selects two commits with a click and a
// Cmd-click on the History page and captures the comparison that opens: the
// base and target rows, the changed-file list and the first file's diff.
func setupScreenshotHistoryCompare(t *testing.T) *testHarness {
	t.Helper()
	r := newDetGitRepo(t, "branchline")
	base := screenshotFixtureTime
	c1 := detCommitFile(t, r, base, "alpha.txt", "alpha v1\n", "first seed")
	detCommitFile(t, r, base.Add(time.Hour), "alpha.txt", "alpha v1\nalpha v2\n", "second grow")
	r.WriteFile("alpha.txt", "alpha v1\nalpha v2 revised\n")
	r.WriteFile("beta.txt", "beta v1\n")
	r.Git("add", "--", "alpha.txt", "beta.txt")
	r.gitAt(base.Add(2*time.Hour), "commit", "-q", "-m", "third revise")
	c3 := strings.TrimSpace(r.Git("rev-parse", "HEAD"))

	h := newSnapshotApp(t)
	openHistoryPage(t, h, r.StoreRepo())
	h.drain()
	selectComparePair(t, h, c3, "third revise", c1, "first seed")
	if !h.ui.HasText("Comparing") || !h.ui.HasText("beta.txt") {
		t.Fatalf("history-compare state missing comparison header or changed file; texts=%q", h.ui.Texts())
	}
	return h
}

// setupScreenshotCloneDialog opens the Clone Repository dialog through the
// repository switcher and fills it in: a placeholder HTTPS address, and a
// tilde parent folder so no machine-specific home path enters the baseline.
func setupScreenshotCloneDialog(t *testing.T) *testHarness {
	t.Helper()
	r, _ := buildRichRepo(t, "branchline")
	h := newSnapshotApp(t)
	h.openRepo(r.StoreRepo())
	h.drain()
	openCloneDialog(t, h)
	h.ui.Type("https://example.com/owner/branchline.git")
	h.ui.Frame()
	replaceField(t, h, "Parent Folder", "~/code")
	if !h.ui.HasText("Clones into") {
		t.Fatalf("clone dialog missing target preview; texts=%q", h.ui.Texts())
	}
	return h
}

// setupScreenshotMergeDialog opens the Merge dialog from the sidebar context
// menu of the unmerged experiment branch and captures it after the conflict
// prediction has settled.
func setupScreenshotMergeDialog(t *testing.T) *testHarness {
	t.Helper()
	r, _ := buildRichRepo(t, "branchline")
	h := newSnapshotApp(t)
	h.openRepo(r.StoreRepo())
	h.drain()
	openMergeDialogFromBranch(t, h, "experiment")
	h.drain()
	if !h.ui.HasText("Fast-forward if possible") {
		t.Fatalf("merge dialog missing merge type choices; texts=%q", h.ui.Texts())
	}
	return h
}

// setupScreenshotResetModal opens the Reset dialog from the context menu of
// a commit row of the History list, defaulting to the Mixed mode.
func setupScreenshotResetModal(t *testing.T) *testHarness {
	t.Helper()
	r, hashes := buildLinearSnapshotRepo(t, "branchline")
	h := newSnapshotApp(t)
	openHistoryPage(t, h, r.StoreRepo())
	h.drain()
	openResetModalOnMain(t, h, hashes[1], "grow alpha")
	if !h.ui.HasText("Keep all changes and unstage differences.") {
		t.Fatalf("reset modal missing mixed-mode explanation; texts=%q", h.ui.Texts())
	}
	return h
}

// setupScreenshotCherryPickModal opens the Cherry-pick dialog from the
// context menu of a commit row of the History list.
func setupScreenshotCherryPickModal(t *testing.T) *testHarness {
	t.Helper()
	r, hashes := buildLinearSnapshotRepo(t, "branchline")
	h := newSnapshotApp(t)
	openHistoryPage(t, h, r.StoreRepo())
	h.drain()
	openCherryPickDialog(t, h, hashes[1], "grow alpha")
	if !h.ui.HasText("Append origin commit SHA (-x)") {
		t.Fatalf("cherry-pick modal missing the record-origin option; texts=%q", h.ui.Texts())
	}
	return h
}

// setupScreenshotInteractiveRebaseModal opens the Interactive Rebase form
// from the context menu of the oldest commit, so the plan lists the three
// commits after it, and captures it once the plan and options reads have
// settled.
func setupScreenshotInteractiveRebaseModal(t *testing.T) *testHarness {
	t.Helper()
	r, hashes := buildLinearSnapshotRepo(t, "branchline")
	h := newSnapshotApp(t)
	openHistoryPage(t, h, r.StoreRepo())
	h.drain()
	openIRDialog(t, h, hashes[0], "seed alpha")
	drainIRPlan(t, h)
	if !h.ui.HasText("Choose the action and position of each commit") {
		t.Fatalf("interactive rebase form missing explainer; texts=%q", h.ui.Texts())
	}
	return h
}

// setupScreenshotApplyStashDialog stashes deterministic working-tree changes
// with fixed committer dates — so the stash hash the dialog names is stable —
// and opens the entry's Apply dialog from the sidebar row's inline actions.
func setupScreenshotApplyStashDialog(t *testing.T) *testHarness {
	t.Helper()
	r := newDetGitRepo(t, "branchline")
	detCommitFile(t, r, screenshotFixtureTime, "alpha.txt", "alpha base\n", "seed alpha")
	r.WriteFile("alpha.txt", "alpha base\nalpha staged\n")
	r.Git("add", "--", "alpha.txt")
	r.WriteFile("beta.txt", "beta untracked\n")
	r.gitAt(screenshotFixtureTime.Add(time.Hour), "stash", "push", "--include-untracked", "-m", "indexed snapshot work")

	h := newSnapshotApp(t)
	h.openRepo(r.StoreRepo())
	h.drain()
	clickStashRow(t, h, "stash@{0}")
	mustSnapshotClick(t, h, "Apply…")
	h.ui.Frame()
	if !h.ui.HasText("indexed snapshot work") || !h.ui.HasText("Delete stash after applying") {
		t.Fatalf("apply stash dialog missing entry message or options; texts=%q", h.ui.Texts())
	}
	return h
}

// setupScreenshotNewTagDialog opens the New Tag dialog from the sidebar
// entry and types a name into it, so the baseline shows a filled-in draft
// rather than the empty form.
func setupScreenshotNewTagDialog(t *testing.T) *testHarness {
	t.Helper()
	r, _ := buildLinearSnapshotRepo(t, "branchline")
	h := newSnapshotApp(t)
	h.openRepo(r.StoreRepo())
	h.drain()
	mustSnapshotClick(t, h, "New Tag")
	h.ui.Frame()
	h.ui.Type("v2.0.0")
	h.ui.Frame()
	if !hasExactText(h.ui, "New Tag") || !h.ui.HasText("Tag name:") {
		t.Fatalf("new tag dialog missing title or name field; texts=%q", h.ui.Texts())
	}
	return h
}

// setupScreenshotFileResetModal opens the file reset confirmation from the
// context menu of a file row of a commit's detail on the History page.
func setupScreenshotFileResetModal(t *testing.T) *testHarness {
	t.Helper()
	r, hashes := buildLinearSnapshotRepo(t, "branchline")
	h := newSnapshotApp(t)
	openHistoryPage(t, h, r.StoreRepo())
	h.drain()
	chooseFileReset(t, h, hashes[1], "grow alpha", "alpha.txt", "Reset File to State Before Commit…", "Reset File to State Before Commit")
	if !h.ui.HasText("alpha.txt will be reset to its state in") {
		t.Fatalf("file reset modal missing outcome line; texts=%q", h.ui.Texts())
	}
	return h
}
