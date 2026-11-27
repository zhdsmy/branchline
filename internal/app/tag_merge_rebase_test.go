package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
)

// --- fixture ---

// tagMergeRebaseFixture builds a divergent history tagged on both tips:
// base.txt on the shared base commit, main.txt on main's tip and feature.txt
// on the feature branch checked out from the base. lw-main/ann-main name
// main's tip, lw-feat/ann-feat feature's tip, and the repository is left on
// feature — the position a rebase onto one of the main tags needs, while a
// merge test checks main out before opening the app. It returns the feature
// and main tip hashes.
func tagMergeRebaseFixture(t *testing.T, repo *gitRepoFixture) (featureTip, mainTip string) {
	t.Helper()
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CreateBranch("feature")
	mainTip = repo.CommitFile("main.txt", "main work\n", "main work")
	repo.Git("tag", "lw-main", mainTip)
	repo.Git("tag", "-a", "ann-main", "-m", "main release", mainTip)
	repo.Checkout("feature")
	featureTip = repo.CommitFile("feature.txt", "feature work\n", "feature work")
	repo.Git("tag", "lw-feat", featureTip)
	repo.Git("tag", "-a", "ann-feat", "-m", "feature release", featureTip)
	return featureTip, mainTip
}

// chooseTagRebaseItem chooses the rebase item of the tag row's open context
// menu — its label names the checked-out branch — and verifies the dialog
// opened.
func chooseTagRebaseItem(t *testing.T, h *testHarness, tag string) {
	t.Helper()
	current := h.app.session.Snapshot().Status.Branch
	label := "Rebase '" + current + "' on '" + tag + "'…"
	if !slices.Contains(h.ui.Menu(), label) {
		t.Fatalf("menu %q missing %q", h.ui.Menu(), label)
	}
	if err := h.ui.ChooseMenuItem(label); err != nil {
		t.Fatalf("choose %q: %v", label, err)
	}
	h.ui.Frame()
	assertRebaseDialogOpen(t, h)
}

// openTagRebaseDialog right-clicks the tag row of the sidebar and opens the
// rebase dialog targeting it.
func openTagRebaseDialog(t *testing.T, h *testHarness, tag string) {
	t.Helper()
	if err := h.ui.RightClick(tag); err != nil {
		t.Fatalf("right-click tag row %q: %v\ntexts=%q", tag, err, h.ui.Texts())
	}
	chooseTagRebaseItem(t, h, tag)
}

// --- merge through the tag menu ---

// The Merge item of a lightweight tag row merges the commit the tag names
// into the checked-out branch through the real merge dialog: the merge
// commit carries both parents, its subject names the tag, and the feature
// file lands in the working tree.
func TestTagMergeRebaseMergeLightweightTag(t *testing.T) {
	repo := newGitRepo(t, "tagmr-merge-lw")
	featureTip, mainTip := tagMergeRebaseFixture(t, repo)
	repo.Checkout("main")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("lw-feat"); err != nil {
		t.Fatalf("right-click lw-feat row: %v\ntexts=%q", err, h.ui.Texts())
	}
	// The menu carries the merge and rebase entries between the push and the
	// delete items, each pair fenced by separators.
	menu := h.ui.Menu()
	push := slices.Index(menu, "Push…")
	mergeItem := slices.Index(menu, "Merge into 'main'…")
	rebaseItem := slices.Index(menu, "Rebase 'main' on 'lw-feat'…")
	del := slices.Index(menu, "Delete…")
	if push < 0 || mergeItem < 0 || rebaseItem < 0 || del < 0 {
		t.Fatalf("menu %q missing the merge or rebase entries", menu)
	}
	if !(push < mergeItem && mergeItem < rebaseItem && rebaseItem < del) {
		t.Fatalf("menu %q: merge and rebase not ordered between Push… and Delete…", menu)
	}
	if menu[push+1] != "-" || menu[push+2] != "Merge into 'main'…" {
		t.Fatalf("menu %q: no separator between Push… and the merge item", menu)
	}
	if menu[rebaseItem+1] != "Interactively Rebase on 'lw-feat'…" || menu[rebaseItem+2] != "-" || menu[rebaseItem+3] != "New Branch…" || menu[rebaseItem+4] != "New Tag…" || menu[rebaseItem+5] != "Delete…" {
		t.Fatalf("menu %q: entries between the rebase item and Delete… out of order", menu)
	}

	chooseMergeItem(t, h)
	h.drain()
	if !hasExactText(h.ui, "lw-feat") {
		t.Fatalf("merge dialog missing the tag as its source; texts=%q", h.ui.Texts())
	}
	confirmMerge(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("merge of the lightweight tag failed: %v", err)
	}
	parents := parentsOf(t, repo.Path(), "HEAD")
	if len(parents) != 2 || parents[0] != mainTip || parents[1] != featureTip {
		t.Fatalf("merge commit parents = %v, want [%q %q]", parents, mainTip, featureTip)
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); subject != "Merge tag 'lw-feat'" {
		t.Fatalf("merge commit subject = %q, want Merge tag 'lw-feat'", subject)
	}
	assertFileBytes(t, repo, "feature.txt", "feature work\n")
	mergeDialogGone(t, h)
}

// The Merge item of an annotated tag row merges the commit the tag peels to,
// never the tag object: the merge commit has the tagged commit as its second
// parent and Git's merge message quotes the tag name.
func TestTagMergeRebaseMergeAnnotatedTag(t *testing.T) {
	repo := newGitRepo(t, "tagmr-merge-ann")
	featureTip, mainTip := tagMergeRebaseFixture(t, repo)
	repo.Checkout("main")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("ann-feat"); err != nil {
		t.Fatalf("right-click ann-feat row: %v\ntexts=%q", err, h.ui.Texts())
	}
	chooseMergeItem(t, h)
	h.drain()
	if !hasExactText(h.ui, "ann-feat") {
		t.Fatalf("merge dialog missing the tag as its source; texts=%q", h.ui.Texts())
	}
	confirmMerge(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("merge of the annotated tag failed: %v", err)
	}
	parents := parentsOf(t, repo.Path(), "HEAD")
	if len(parents) != 2 || parents[0] != mainTip || parents[1] != featureTip {
		t.Fatalf("merge commit parents = %v, want [%q %q]", parents, mainTip, featureTip)
	}
	tagObject := strings.TrimSpace(repo.Git("rev-parse", "refs/tags/ann-feat"))
	if parents[1] == tagObject {
		t.Fatal("merge commit parents the tag object instead of the tagged commit")
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); subject != "Merge tag 'ann-feat'" {
		t.Fatalf("merge commit subject = %q, want Merge tag 'ann-feat'", subject)
	}
	assertFileBytes(t, repo, "feature.txt", "feature work\n")
	mergeDialogGone(t, h)
}

// --- rebase through the tag menu ---

// The Rebase item of a lightweight tag row replays the current branch on top
// of the commit the tag names: the feature commit lands on main's tip, the
// branch pointer moves forward and the history stays linear.
func TestTagMergeRebaseRebaseLightweightTag(t *testing.T) {
	repo := newGitRepo(t, "tagmr-rebase-lw")
	featureTip, mainTip := tagMergeRebaseFixture(t, repo)

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openTagRebaseDialog(t, h, "lw-main")
	drainRebaseInspection(t, h)
	if !hasExactText(h.ui, "lw-main") {
		t.Fatalf("rebase dialog missing the tag as its target; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase onto the lightweight tag failed: %v", err)
	}
	assertCleanRebase(t, repo, featureTip, mainTip, "feature work")
	assertFileBytes(t, repo, "feature.txt", "feature work\n")
	assertFileBytes(t, repo, "main.txt", "main work\n")
	assertRebaseDialogClosed(t, h)
}

// The Rebase item of an annotated tag row rebases onto the peeled commit,
// not the tag object: the replayed commit's parent is exactly the commit the
// tag names.
func TestTagMergeRebaseRebaseAnnotatedTag(t *testing.T) {
	repo := newGitRepo(t, "tagmr-rebase-ann")
	featureTip, mainTip := tagMergeRebaseFixture(t, repo)

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openTagRebaseDialog(t, h, "ann-main")
	drainRebaseInspection(t, h)
	if !hasExactText(h.ui, "ann-main") {
		t.Fatalf("rebase dialog missing the tag as its target; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase onto the annotated tag failed: %v", err)
	}
	assertCleanRebase(t, repo, featureTip, mainTip, "feature work")
	assertRebaseDialogClosed(t, h)
}

// --- disabled states ---

// Both items disable on the tag naming the commit HEAD already is — merging
// or rebasing onto it changes nothing — while the tag of another commit
// keeps them enabled. The disabled entries are never actionable and open no
// dialog.
func TestTagMergeRebaseItemsDisabledOnTagAtHead(t *testing.T) {
	repo := newGitRepo(t, "tagmr-at-head")
	featureTip, mainTip := tagMergeRebaseFixture(t, repo)
	if featureTip == mainTip {
		t.Fatal("fixture broken: the tips share a hash")
	}

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if got := h.app.session.Snapshot().Status.Head; got != featureTip {
		t.Fatalf("fixture: HEAD = %q, want the feature tip %q", got, featureTip)
	}

	// lw-feat names the commit HEAD is: both its items disable.
	if err := h.ui.RightClick("lw-feat"); err != nil {
		t.Fatalf("right-click lw-feat row: %v", err)
	}
	for _, label := range []string{"Merge into 'feature'…", "Rebase 'feature' on 'lw-feat'…"} {
		if err := h.ui.ChooseMenuItem(label); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing %q of the tag at HEAD = %v, want a disabled error", label, err)
		}
	}
	h.ui.CloseMenu()
	if hasExactText(h.ui, "Merge") || hasExactText(h.ui, "Replay commits from the current branch onto another base.") {
		t.Fatal("a dialog opened from a menu item disabled on the tag at HEAD")
	}

	// ann-main names another commit: both its items stay enabled.
	if err := h.ui.RightClick("ann-main"); err != nil {
		t.Fatalf("right-click ann-main row: %v", err)
	}
	chooseMergeItem(t, h)
	h.drain()
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	openTagRebaseDialog(t, h, "ann-main")
	drainRebaseInspection(t, h)
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click rebase Cancel: %v", err)
	}
	h.ui.Frame()
}

// During an operation in progress both items disable: a merge or a rebase
// started over it would corrupt the operation.
func TestTagMergeRebaseItemsDisabledDuringOperation(t *testing.T) {
	repo := newGitRepo(t, "tagmr-in-op")
	repo.CommitFile("c.txt", "base\n", "base commit")
	repo.CreateBranch("feature")
	repo.WriteFile("c.txt", "main\n")
	repo.Git("add", "--", "c.txt")
	repo.Git("commit", "-m", "main edits")
	mainTip := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.Git("tag", "lw-main", mainTip)
	repo.Checkout("feature")
	repo.CommitFile("c.txt", "feature\n", "feature edits")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "main"); err == nil {
		t.Fatalf("conflicting merge unexpectedly succeeded: %s", out)
	}

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if got := h.app.session.Snapshot().Status.State; got != "merge" {
		t.Fatalf("fixture: state = %q, want merge", got)
	}

	if err := h.ui.RightClick("lw-main"); err != nil {
		t.Fatalf("right-click lw-main row during merge: %v", err)
	}
	for _, label := range []string{"Merge into 'feature'…", "Rebase 'feature' on 'lw-main'…"} {
		if err := h.ui.ChooseMenuItem(label); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing %q during a merge = %v, want a disabled error", label, err)
		}
	}
	h.ui.CloseMenu()
	if hasExactText(h.ui, "Replay commits from the current branch onto another base.") {
		t.Fatal("rebase dialog opened during a merge")
	}
	repo.Git("merge", "--abort")
}

// While a repository write is held the items disable; on a detached HEAD the
// merge item stays enabled — merging into a detached HEAD is meaningful —
// while the rebase item disables because a rebase needs a branch to move,
// and its label renders the detached form without an empty branch quote.
func TestTagMergeRebaseItemsDisabledBusyAndDetached(t *testing.T) {
	repo := newGitRepo(t, "tagmr-busy-detached")
	tagMergeRebaseFixture(t, repo)
	repo.Checkout("main")

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
	if err := h.ui.RightClick("lw-feat"); err != nil {
		h.queue.ReleaseWorker(hold)
		t.Fatalf("right-click lw-feat row while busy: %v", err)
	}
	for _, label := range []string{"Merge into 'main'…", "Rebase 'main' on 'lw-feat'…"} {
		if err := h.ui.ChooseMenuItem(label); err == nil || !strings.Contains(err.Error(), "disabled") {
			h.queue.ReleaseWorker(hold)
			t.Fatalf("choosing %q while busy = %v, want a disabled error", label, err)
		}
	}
	h.ui.CloseMenu()
	h.queue.ReleaseWorker(hold)
	h.queue.ResumeWorkers()
	h.drain()

	// Detached HEAD: merge enabled, rebase disabled with the detached label.
	repo.Git("checkout", "--detach", "-q", "HEAD")
	h.app.session.Reload()
	h.drain()
	if got := h.app.session.Snapshot().Status.Branch; got != "" {
		t.Fatalf("fixture: branch = %q, want detached", got)
	}
	if err := h.ui.RightClick("lw-feat"); err != nil {
		t.Fatalf("right-click lw-feat row on detached HEAD: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Rebase on 'lw-feat'…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing rebase on detached HEAD = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	if hasExactText(h.ui, "Replay commits from the current branch onto another base.") {
		t.Fatal("rebase dialog opened on a detached HEAD")
	}
	if err := h.ui.RightClick("lw-feat"); err != nil {
		t.Fatalf("right-click lw-feat row on detached HEAD again: %v", err)
	}
	chooseMergeItem(t, h)
	h.drain()
	if !hasExactText(h.ui, "HEAD (detached)") {
		t.Fatalf("merge dialog missing the detached target line; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// --- guard: the tag moved or vanished behind the dialog ---

// A tag moved onto another commit after the merge dialog opened is never
// merged: the guard refuses, the error explains why, and the repository is
// exactly as before. The same holds for a tag deleted outright, and for an
// annotated tag retargeted with git tag -f -a.
func TestTagMergeRebaseMergeGuardTagMoved(t *testing.T) {
	repo := newGitRepo(t, "tagmr-merge-guard")
	_, mainTip := tagMergeRebaseFixture(t, repo)
	repo.Checkout("main")
	base := strings.TrimSpace(repo.Git("rev-parse", "main~1"))

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("lw-feat"); err != nil {
		t.Fatalf("right-click lw-feat row: %v", err)
	}
	chooseMergeItem(t, h)
	h.drain()

	// The tag moves onto the base commit behind the dialog's back.
	repo.Git("tag", "-f", "lw-feat", base)
	confirmMerge(t, h)

	assertRepoErrorShown(t, h, "merge target changed after dialog opened")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD moved by the refused merge: %q, want %q", head, mainTip)
	}
	if count := gitOutTrim(t, repo.Path(), "rev-list", "--count", "HEAD"); count != "2" {
		t.Fatalf("commit count after the refused merge = %q, want 2", count)
	}
	if got := tagPointsAt(t, repo, "lw-feat"); got != base {
		t.Fatalf("the refused merge retargeted lw-feat: %q, want %q", got, base)
	}

	// Round 2: the annotated tag retargeted with git tag -f -a refuses the
	// same way — the guard compares the peeled commits.
	h.app.session.Reload()
	h.drain()
	if err := h.ui.RightClick("ann-feat"); err != nil {
		t.Fatalf("right-click ann-feat row: %v", err)
	}
	chooseMergeItem(t, h)
	h.drain()
	repo.Git("tag", "-f", "-a", "ann-feat", "-m", "retargeted", base)
	confirmMerge(t, h)

	assertRepoErrorShown(t, h, "merge target changed after dialog opened")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD moved by the refused annotated merge: %q, want %q", head, mainTip)
	}

	// Round 3: the tag deleted outright no longer resolves at all.
	h.app.session.Reload()
	h.drain()
	if err := h.ui.RightClick("ann-feat"); err != nil {
		t.Fatalf("right-click ann-feat row again: %v", err)
	}
	chooseMergeItem(t, h)
	h.drain()
	repo.Git("tag", "-d", "ann-feat")
	confirmMerge(t, h)

	assertRepoErrorShown(t, h, "no longer resolves to a commit")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("HEAD moved by the refused merge of a deleted tag: %q, want %q", head, mainTip)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "feature.txt")); err == nil {
		t.Fatal("merge ran despite the deleted merge source")
	}
}

// A tag moved onto another commit after the rebase dialog opened is never
// rebased onto: the guard refuses before any replay starts, and the branch
// keeps its exact tip and parent.
func TestTagMergeRebaseRebaseGuardTagMoved(t *testing.T) {
	repo := newGitRepo(t, "tagmr-rebase-guard")
	featureTip, _ := tagMergeRebaseFixture(t, repo)
	base := strings.TrimSpace(repo.Git("rev-parse", "main~1"))

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openTagRebaseDialog(t, h, "lw-main")
	drainRebaseInspection(t, h)

	repo.Git("tag", "-f", "lw-main", base)
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "rebase target changed")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); head != featureTip {
		t.Fatalf("feature moved by the refused rebase: %q, want %q", head, featureTip)
	}
	if parent := gitOutTrim(t, repo.Path(), "rev-parse", "feature^"); parent != base {
		t.Fatalf("feature parent after the refused rebase = %q, want the untouched %q", parent, base)
	}

	// Round 2: the annotated tag retargeted refuses the same way.
	h.app.session.Reload()
	h.drain()
	openTagRebaseDialog(t, h, "ann-main")
	drainRebaseInspection(t, h)
	repo.Git("tag", "-f", "-a", "ann-main", "-m", "retargeted", base)
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase again: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "rebase target changed")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); head != featureTip {
		t.Fatalf("feature moved by the refused annotated rebase: %q, want %q", head, featureTip)
	}

	// Round 3: a deleted target no longer resolves at all.
	h.app.session.Reload()
	h.drain()
	openTagRebaseDialog(t, h, "ann-main")
	drainRebaseInspection(t, h)
	repo.Git("tag", "-d", "ann-main")
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase once more: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "no longer resolves to a commit")
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); head != featureTip {
		t.Fatalf("feature moved by the refused rebase of a deleted tag: %q, want %q", head, featureTip)
	}
}

// --- loopback SSH ---

// Over loopback SSH both entries act on the server's repository: the merge
// of a lightweight tag records the merge commit there, and the rebase onto
// an annotated tag replays the server's feature commit on the commit the tag
// peels to.
func TestTagMergeRebaseLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	srv.WriteFile(t, repo.Path, "base.txt", []byte("base\n"))
	srv.RunGit(t, repo.Path, "add", "--", "base.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "base commit")
	srv.RunGit(t, repo.Path, "branch", "feature")
	srv.WriteFile(t, repo.Path, "main.txt", []byte("main work\n"))
	srv.RunGit(t, repo.Path, "add", "--", "main.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "main work")
	mainTip := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	srv.RunGit(t, repo.Path, "tag", "lw-main", mainTip)
	srv.RunGit(t, repo.Path, "tag", "-a", "ann-main", "-m", "main release", mainTip)
	srv.RunGit(t, repo.Path, "checkout", "-q", "feature")
	srv.WriteFile(t, repo.Path, "feature.txt", []byte("feature work\n"))
	srv.RunGit(t, repo.Path, "add", "--", "feature.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "feature work")
	featureTip := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	srv.RunGit(t, repo.Path, "tag", "lw-feat", featureTip)
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)

	// Merge the lightweight tag into main on the server.
	if err := h.ui.RightClick("lw-feat"); err != nil {
		t.Fatalf("right-click lw-feat row over SSH: %v\ntexts=%q", err, h.ui.Texts())
	}
	chooseMergeItem(t, h)
	h.drain()
	confirmMerge(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH merge of the tag failed: %v", err)
	}
	parents := strings.Fields(srv.RunGit(t, repo.Path, "rev-list", "--parents", "-n", "1", "HEAD"))[1:]
	if len(parents) != 2 || parents[0] != mainTip || parents[1] != featureTip {
		t.Fatalf("server merge commit parents = %v, want [%q %q]", parents, mainTip, featureTip)
	}
	if subject := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "-1", "--format=%s")); subject != "Merge tag 'lw-feat'" {
		t.Fatalf("server merge commit subject = %q, want Merge tag 'lw-feat'", subject)
	}
	if got := srv.ReadFile(t, repo.Path, "feature.txt"); string(got) != "feature work\n" {
		t.Fatalf("server feature.txt after merge = %q, want feature work", string(got))
	}

	// Rebase the server's feature branch onto the annotated tag of main.
	srv.RunGit(t, repo.Path, "checkout", "-q", "feature")
	h.app.session.Reload()
	h.drain()
	openTagRebaseDialog(t, h, "ann-main")
	drainRebaseInspection(t, h)
	if err := h.ui.Click("Confirm Rebase"); err != nil {
		t.Fatalf("click Confirm Rebase over SSH: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH rebase onto the tag failed: %v", err)
	}
	newTip := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature"))
	if newTip == featureTip {
		t.Fatalf("server feature did not move after rebase: %q", newTip)
	}
	if parent := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature^")); parent != mainTip {
		t.Fatalf("server replayed parent = %q, want the tagged commit %q", parent, mainTip)
	}
}

// Merging a fast-forwardable annotated tag stored in refs/tags/ follows the
// Merge dialog's chosen mode honestly: "Fast-forward if possible" fast-forwards
// the branch pointer, while "No Fast-Forward" creates a merge commit naming
// the tag and preserving the tag message.
func TestTagMergeRebaseFastForwardAnnotatedTag(t *testing.T) {
	repo := newGitRepo(t, "tagmr-ff-ann")
	baseTip := repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CreateBranch("feature")
	repo.Checkout("feature")
	aheadTip := repo.CommitFile("ahead.txt", "ahead\n", "ahead commit")
	repo.Git("tag", "-a", "ann-ff", "-m", "annotated release notes", aheadTip)
	repo.Checkout("main")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Default "Fast-forward if possible" fast-forwards HEAD to the tagged commit.
	if err := h.ui.RightClick("ann-ff"); err != nil {
		t.Fatalf("right-click ann-ff row: %v\ntexts=%q", err, h.ui.Texts())
	}
	chooseMergeItem(t, h)
	h.drain()
	confirmMerge(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("fast-forward merge of annotated tag failed: %v", err)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); head != aheadTip {
		t.Fatalf("HEAD after fast-forward merge = %q, want %q", head, aheadTip)
	}
	if parents := parentsOf(t, repo.Path(), "HEAD"); len(parents) != 1 || parents[0] != baseTip {
		t.Fatalf("fast-forward merge parents = %v, want single parent [%q]", parents, baseTip)
	}

	// Reset back and choose "No Fast-Forward": creates a merge commit carrying
	// both parents, the tag subject and the tag annotation.
	repo.Git("reset", "--hard", "-q", baseTip)
	h.app.session.Reload()
	h.drain()

	if err := h.ui.RightClick("ann-ff"); err != nil {
		t.Fatalf("right-click ann-ff row for --no-ff: %v\ntexts=%q", err, h.ui.Texts())
	}
	chooseMergeItem(t, h)
	h.drain()
	if err := h.ui.Click("No Fast-Forward"); err != nil {
		t.Fatalf("select No Fast-Forward: %v", err)
	}
	h.ui.Frame()
	confirmMerge(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("no-ff merge of annotated tag failed: %v", err)
	}
	parents := parentsOf(t, repo.Path(), "HEAD")
	if len(parents) != 2 || parents[0] != baseTip || parents[1] != aheadTip {
		t.Fatalf("no-ff merge parents = %v, want [%q %q]", parents, baseTip, aheadTip)
	}
	body := repo.Git("log", "-1", "--format=%B")
	if !strings.Contains(body, "Merge tag 'ann-ff'") || !strings.Contains(body, "annotated release notes") {
		t.Fatalf("no-ff merge message missing tag subject or annotation:\n%s", body)
	}
}
