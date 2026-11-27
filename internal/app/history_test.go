package app

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// openHistoryPage opens repo in the harness, drains the initial snapshot
// load, and navigates to the History page through the sidebar nav row.
func openHistoryPage(t *testing.T, h *testHarness, repo store.Repo) {
	t.Helper()
	h.openRepo(repo)
	h.drain()
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History nav row: %v", err)
	}
	h.ui.Frame()
}

// hasExactText reports whether the last frame shows a text equal to s (not
// merely containing it, so "commit 1" does not match "commit 12").
func hasExactText(tt *ui.Tester, s string) bool {
	return slices.Contains(tt.Texts(), s)
}

// commitRowLabel is the accessibility label of the row of the commit.
func commitRowLabel(hash, subject string) string {
	return "Commit " + short7(hash) + " " + subject
}

func short7(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

// clickCommitRow clicks the row of the commit through its accessibility
// label, as a user would.
func clickCommitRow(t *testing.T, h *testHarness, hash, subject string) {
	t.Helper()
	if err := h.ui.Click(commitRowLabel(hash, subject)); err != nil {
		t.Fatalf("click commit row %q: %v", subject, err)
	}
}

// makeCommits appends n commits modifying file to the fixture, returning the
// final HEAD hash. Commit subjects are "<prefix> <i>" counting upward.
func makeCommits(t *testing.T, repo *gitRepoFixture, file, prefix string, n int) string {
	t.Helper()
	var head string
	for i := 1; i <= n; i++ {
		head = repo.CommitFile(file, fmt.Sprintf("%s %d\n", prefix, i), fmt.Sprintf("%s %d", prefix, i))
	}
	return head
}

// Clicking History after opening a repository lists its commits newest-first
// with ref badges, author, short hash, and the HEAD branch badge.
func TestHistoryCommitListShowsRefsAndMetadata(t *testing.T) {
	repo := newGitRepo(t, "hist-list")
	first := repo.CommitFile("a.txt", "one\n", "first commit")
	second := repo.CommitFile("b.txt", "two\n", "second commit")
	repo.CreateBranch("feature-x")
	repo.CreateTag("v1.0")
	third := repo.CommitFile("c.txt", "three\n", "third commit")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	if !h.ui.HasText("3 commits") {
		t.Fatalf("commit count header missing; texts=%q", h.ui.Texts())
	}
	texts := h.ui.Texts()
	idxThird := slices.Index(texts, commitRowLabel(third, "third commit"))
	idxSecond := slices.Index(texts, commitRowLabel(second, "second commit"))
	idxFirst := slices.Index(texts, commitRowLabel(first, "first commit"))
	if idxThird < 0 || idxSecond < 0 || idxFirst < 0 {
		t.Fatalf("missing commit row labels: third=%d second=%d first=%d; texts=%q", idxThird, idxSecond, idxFirst, texts)
	}
	if !(idxThird < idxSecond && idxSecond < idxFirst) {
		t.Fatalf("commit rows out of order: third=%d second=%d first=%d", idxThird, idxSecond, idxFirst)
	}
	if !h.ui.HasText("HEAD → main") {
		t.Fatalf("missing HEAD branch badge; texts=%q", texts)
	}
	if !h.ui.HasText("v1.0") {
		t.Fatalf("missing tag badge; texts=%q", texts)
	}
	if !h.ui.HasText("feature-x") {
		t.Fatalf("missing branch badge; texts=%q", texts)
	}
	if !h.ui.HasText("Branchline Test") {
		t.Fatalf("missing author column; texts=%q", texts)
	}
}

// A detached HEAD decorates the commit it points at with a plain "HEAD"
// badge instead of the "HEAD → branch" pill of an attached checkout.
func TestHistoryDetachedHeadShowsHeadBadge(t *testing.T) {
	repo := newGitRepo(t, "hist-detached")
	first := repo.CommitFile("a.txt", "one\n", "first commit")
	repo.CommitFile("b.txt", "two\n", "second commit")
	repo.Git("checkout", "--detach", first)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	if !h.ui.HasText("HEAD") {
		t.Fatalf("missing detached HEAD badge; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("HEAD → main") {
		t.Fatal("attached HEAD branch badge rendered while detached")
	}
}

// Remote-tracking branches decorate their commit with a remote-branch badge
// distinct from local branches and tags.
func TestHistoryRemoteBranchBadge(t *testing.T) {
	repo := newGitRepo(t, "hist-remote")
	head := repo.CommitFile("r.txt", "r\n", "remote tip commit")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// origin/main points at the same commit as main after push -u.
	snap := h.app.session.Snapshot()
	var refs []git.Ref
	for _, cm := range snap.Commits {
		if cm.Hash == head {
			refs = cm.Refs
		}
	}
	hasRemote := false
	for _, r := range refs {
		if r.Kind == git.RefRemote && r.Name == "origin/main" {
			hasRemote = true
		}
	}
	if !hasRemote {
		t.Fatalf("commit refs = %+v, want remote ref origin/main", refs)
	}
	if !h.ui.HasText("origin/main") {
		t.Fatalf("missing remote branch badge; texts=%q", h.ui.Texts())
	}
}

// A branched and merged history produces graph rows with multiple lanes and
// merge rows with two bottom segments, which the graph column draws.
func TestHistoryGraphLanesForMergeHistory(t *testing.T) {
	repo := newGitRepo(t, "hist-graph")
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CreateBranch("side")
	repo.CommitFile("main.txt", "main work\n", "main work")
	repo.Checkout("side")
	repo.CommitFile("side.txt", "side work\n", "side work")
	repo.Checkout("main")
	repo.Git("merge", "--no-ff", "-m", "merge side", "side")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	snap := h.app.session.Snapshot()
	if len(snap.Graph) != len(snap.Commits) {
		t.Fatalf("graph rows = %d, commits = %d, want equal", len(snap.Graph), len(snap.Commits))
	}
	maxWidth := 0
	for _, r := range snap.Graph {
		maxWidth = max(maxWidth, r.Width)
	}
	if maxWidth < 2 {
		t.Fatalf("merge history graph max width = %d, want >= 2 lanes", maxWidth)
	}
	mergeRow := -1
	for i, cm := range snap.Commits {
		if len(cm.Parents) == 2 {
			mergeRow = i
		}
		for _, s := range snap.Graph[i].Segments {
			if s.From < 0 || s.To < 0 {
				t.Fatalf("row %d segment %+v has negative lane", i, s)
			}
		}
	}
	if mergeRow < 0 {
		t.Fatal("no merge commit in snapshot")
	}
	bottoms := 0
	for _, s := range snap.Graph[mergeRow].Segments {
		if s.Kind == git.SegmentBottom {
			bottoms++
		}
	}
	if bottoms != 2 {
		t.Fatalf("merge row %d bottom segments = %d, want 2", mergeRow, bottoms)
	}
	if !h.ui.HasText("merge side") {
		t.Fatalf("merge commit row missing; texts=%q", h.ui.Texts())
	}
}

// With more commits than the current log limit, the footer offers Load More;
// clicking it loads older commits, and while the reload is in flight the
// button is disabled so the limit cannot accumulate.
func TestHistoryLoadMoreFetchesOlderCommits(t *testing.T) {
	repo := newGitRepo(t, "hist-more")
	makeCommits(t, repo, "f.txt", "commit", 12)

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Narrow the page size through the session so pagination is observable
	// without hundreds of commits, the same hook session_test.go uses.
	h.app.session.logLimit = 5
	h.app.session.Reload()
	h.drain()

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()

	snap := h.app.session.Snapshot()
	if len(snap.Commits) != 5 || !snap.More {
		t.Fatalf("initial page = %d commits (More=%v), want 5 (More=true)", len(snap.Commits), snap.More)
	}
	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Load More")
	if !ok {
		t.Fatalf("Load More button missing; texts=%q", h.ui.Texts())
	}
	if btn.Disabled {
		t.Fatal("Load More disabled while idle")
	}
	// First page holds commits 12..8; commit 1 is not there.
	if !hasExactText(h.ui, "commit 8") {
		t.Fatalf("expected commit 8 visible in first page; texts=%q", h.ui.Texts())
	}
	if hasExactText(h.ui, "commit 1") {
		t.Fatalf("commit 1 unexpectedly visible before Load More; texts=%q", h.ui.Texts())
	}

	h.queue.PauseWorkers()
	if err := h.ui.Click("Load More"); err != nil {
		t.Fatalf("click Load More: %v", err)
	}
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Load More"); ok && !btn.Disabled {
		t.Fatal("Load More not disabled during reload")
	}
	if !h.ui.HasText("Loading…") {
		t.Fatalf("missing loading hint during Load More reload; texts=%q", h.ui.Texts())
	}
	worker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(worker)
	h.drain()

	// LoadMore(0) steps by the session default of 500, so the narrowed limit
	// of 5 grows to 505 — past the 12 commits the fixture holds. What matters
	// is that more commits than the first page arrived.
	if got := len(h.app.session.Snapshot().Commits); got != 12 {
		t.Fatalf("commits after Load More = %d, want all 12 (limit steps 5 -> 505)", got)
	}
	if h.app.session.Snapshot().More {
		t.Fatal("snapshot.More still true after all 12 commits loaded")
	}
	if !hasExactText(h.ui, "commit 3") {
		t.Fatalf("older commit 3 not visible after Load More; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "commit 12") {
		t.Fatalf("newest commit missing after Load More; texts=%q", h.ui.Texts())
	}
}

// While a real UI write operation (Stage on Local Changes) holds the session
// busy, Load More is disabled and clicking it cannot accumulate logLimit;
// once the write finishes and the snapshot reload completes, the button is
// usable again.
func TestHistoryLoadMoreDisabledWhileWriteBusy(t *testing.T) {
	repo := newGitRepo(t, "hist-busy")
	makeCommits(t, repo, "f.txt", "commit", 12)
	repo.WriteFile("pending.txt", "pending\n")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	h.app.session.logLimit = 5
	h.app.session.Reload()
	h.drain()

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()
	if got := len(h.app.session.Snapshot().Commits); got != 5 || !h.app.session.Snapshot().More {
		t.Fatalf("initial page = %d commits (More=%v), want 5 (More=true)", got, h.app.session.Snapshot().More)
	}
	limitBefore := h.app.session.logLimit

	// Trigger a real UI write from Local Changes while workers are paused so
	// the session stays busy without loading, then navigate back to History.
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

	if !h.app.session.Busy() || h.app.session.Loading() {
		t.Fatalf("session state busy=%v loading=%v, want busy=true loading=false", h.app.session.Busy(), h.app.session.Loading())
	}

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History while busy: %v", err)
	}
	h.ui.Frame()

	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Load More")
	if !ok {
		t.Fatalf("Load More missing while busy; texts=%q", h.ui.Texts())
	}
	if !btn.Disabled {
		t.Fatal("Load More not disabled while a write is busy")
	}
	if err := h.ui.Click("Load More"); err != nil {
		t.Fatalf("click disabled Load More: %v", err)
	}
	h.ui.Frame()
	if h.app.session.logLimit != limitBefore {
		t.Fatalf("logLimit grew while busy: %d -> %d, want unchanged", limitBefore, h.app.session.logLimit)
	}
	if got := len(h.app.session.Snapshot().Commits); got != 5 {
		t.Fatalf("commits changed while busy: %d, want still 5", got)
	}

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()

	if h.app.session.Busy() {
		t.Fatal("session stayed busy after the write finished")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Stage write failed: %v", err)
	}
	staged := h.app.session.Snapshot().Status.Staged
	if len(staged) != 1 || staged[0].Path != "pending.txt" {
		t.Fatalf("staged after write = %+v, want [pending.txt]", staged)
	}
	btn, ok = h.ui.AccessByRole(ui.RoleButton, "Load More")
	if !ok {
		t.Fatalf("Load More missing after write; texts=%q", h.ui.Texts())
	}
	if btn.Disabled {
		t.Fatal("Load More still disabled after the write finished")
	}
}

// Selecting a commit loads its detail and file list; the first file's diff
// loads automatically and renders insertions and deletions.
func TestHistoryCommitDetailAndFileDiff(t *testing.T) {
	repo := newGitRepo(t, "hist-detail")
	root := repo.CommitFile("main.go", "package main\n\nvar v = 1\n", "add main")
	repo.WriteFile("main.go", "package main\n\nvar v = 2\n")
	repo.Git("add", "main.go")
	repo.Git("commit", "-m", "bump version\n\nbody line here")
	second := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	clickCommitRow(t, h, second, "bump version")
	h.drain()

	// The chosen commit row reads as the selected item of the commit list,
	// and the auto-selected first file reads selected in the file list.
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(second, "bump version")); !ok || !row.Selected {
		t.Fatalf("selected commit row access = %+v (found=%v), want Selected=true", row, ok)
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(root, "add main")); !ok || row.Selected {
		t.Fatalf("unselected commit row access = %+v (found=%v), want Selected=false", row, ok)
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, "File main.go"); !ok || !row.Selected {
		t.Fatalf("auto-selected file row access = %+v (found=%v), want Selected=true", row, ok)
	}

	if !h.ui.HasText("body line here") {
		t.Fatalf("detail missing message body; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText(second) {
		t.Fatalf("detail missing full commit hash; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText(short7(root)) {
		t.Fatalf("detail missing parent short hash; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("main.go") {
		t.Fatalf("file list missing main.go; texts=%q", h.ui.Texts())
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "var v = 2") {
		t.Fatalf("diff insertions = %q, want var v = 2", ins)
	}
	if del := diffAccessLines(h.ui, ui.RoleDeletion); !slices.Contains(del, "var v = 1") {
		t.Fatalf("diff deletions = %q, want var v = 1", del)
	}
}

// A root commit compares against the empty tree: every line of its files
// shows as an insertion, none as a deletion.
func TestHistoryRootCommitDiffAgainstEmptyTree(t *testing.T) {
	repo := newGitRepo(t, "hist-root")
	root := repo.CommitFile("seed.txt", "seed line\n", "seed root")
	repo.CommitFile("other.txt", "other\n", "later commit")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, root, "seed root")
	h.drain()

	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "seed line") {
		t.Fatalf("root commit insertions = %q, want seed line", ins)
	}
	if del := diffAccessLines(h.ui, ui.RoleDeletion); len(del) != 0 {
		t.Fatalf("root commit deletions = %q, want none", del)
	}
}

// Clicking another file in the changed-file list replaces the diff with that
// file's diff.
func TestHistoryFileSelectionSwitchesDiff(t *testing.T) {
	repo := newGitRepo(t, "hist-files")
	repo.CommitFile("aaa.txt", "aaa v1\n", "add aaa")
	repo.CommitFile("zzz.txt", "zzz v1\n", "add zzz")
	repo.WriteFile("aaa.txt", "aaa v2\n")
	repo.WriteFile("zzz.txt", "zzz v2\n")
	repo.Git("add", "aaa.txt", "zzz.txt")
	repo.Git("commit", "-m", "update both")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, head, "update both")
	h.drain()

	// First file auto-selected; the other one loads on click.
	ins := diffAccessLines(h.ui, ui.RoleInsertion)
	firstIsAAA := slices.Contains(ins, "aaa v2")
	firstIsZZZ := slices.Contains(ins, "zzz v2")
	if !firstIsAAA && !firstIsZZZ {
		t.Fatalf("auto-selected file diff missing: insertions=%q", ins)
	}
	other := "File zzz.txt"
	wantIns, wantDel := "zzz v2", "zzz v1"
	if firstIsZZZ {
		other = "File aaa.txt"
		wantIns, wantDel = "aaa v2", "aaa v1"
	}
	if err := h.ui.Click(other); err != nil {
		t.Fatalf("click %q: %v", other, err)
	}
	h.drain()

	// The clicked file row reads selected and the previously selected one not.
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, other); !ok || !row.Selected {
		t.Fatalf("clicked file row access = %+v (found=%v), want Selected=true", row, ok)
	}
	firstLabel := "File zzz.txt"
	if firstIsAAA {
		firstLabel = "File aaa.txt"
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, firstLabel); !ok || row.Selected {
		t.Fatalf("previously selected file row access = %+v (found=%v), want Selected=false", row, ok)
	}

	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, wantIns) {
		t.Fatalf("after file switch insertions = %q, want %q", ins, wantIns)
	}
	if del := diffAccessLines(h.ui, ui.RoleDeletion); !slices.Contains(del, wantDel) {
		t.Fatalf("after file switch deletions = %q, want %q", del, wantDel)
	}
}

// fileRowTextRect returns the bounds of the file-name text s inside the
// file row. Find returns the first element carrying the text, which may be
// the diff panel's header rather than the row's own text; in that case the
// access tree is scanned for the match whose center lies inside the row.
func fileRowTextRect(t *testing.T, tt *ui.Tester, row ui.Rect, s string) ui.Rect {
	t.Helper()
	inRow := func(r ui.Rect) bool {
		cy := r.Y + r.H/2
		return cy >= row.Y && cy <= row.Y+row.H
	}
	if r, ok := tt.Find(s); ok && inRow(r) {
		return r
	}
	for _, a := range tt.AccessAll() {
		if a.Name == s && inRow(a.Bounds) {
			return a.Bounds
		}
	}
	t.Fatalf("file row text %q not found inside row %+v; texts=%q", s, row, tt.Texts())
	return ui.Rect{}
}

// The changed-file rows of the History detail read left-aligned: the status
// badge and the file name start beside the row's left padding instead of
// drifting to the row's center. ButtonBase centers its children along both
// axes, so a FillWidth row without Justify(ui.Start) centers a short file
// name visibly — the assertion uses the short name a.go, whose centered
// offset is far beyond the left-aligned one.
func TestHistoryFileRowsLeftAligned(t *testing.T) {
	repo := newGitRepo(t, "hist-align")
	repo.CommitFile("seed.txt", "seed\n", "seed commit")
	head := repo.CommitFile("a.go", "package a\n", "add a")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, head, "add a")
	h.drain()

	rowInfo, ok := h.ui.Access("File a.go")
	if !ok {
		t.Fatalf("file row missing; texts=%q", h.ui.Texts())
	}
	rowRect := rowInfo.Bounds
	textRect := fileRowTextRect(t, h.ui, rowRect, "a.go")
	if off := textRect.X - rowRect.X; off >= 60 {
		t.Fatalf("file name starts %.2f into its row (row X=%.2f, text X=%.2f), want < 60 beside the left padding: the row content is centered instead of left-aligned", off, rowRect.X, textRect.X)
	}
}

// A renamed file shows both its old and its new path in the file list.
func TestHistoryRenameShowsBothPaths(t *testing.T) {
	repo := newGitRepo(t, "hist-rename")
	repo.CommitFile("before.txt", "rename me\n", "add before")
	repo.Git("mv", "before.txt", "after.txt")
	repo.Git("commit", "-m", "rename file")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, head, "rename file")
	h.drain()

	files := h.app.historyView.files
	if len(files) != 1 {
		t.Fatalf("files = %+v, want exactly one", files)
	}
	if files[0].Status != 'R' || files[0].OrigPath != "before.txt" || files[0].Path != "after.txt" {
		t.Fatalf("rename file = %+v, want R before.txt -> after.txt", files[0])
	}
	if !h.ui.HasText("before.txt → after.txt") {
		t.Fatalf("rename row missing both paths; texts=%q", h.ui.Texts())
	}
}

// The Split/Unified toggle in the shared diff panel header switches the
// history diff layout.
func TestHistoryDiffLayoutToggle(t *testing.T) {
	repo := newGitRepo(t, "hist-layout")
	repo.CommitFile("x.go", "package x\n\nvar a = 1\n", "add x")
	repo.WriteFile("x.go", "package x\n\nvar a = 2\n")
	repo.Git("add", "x.go")
	repo.Git("commit", "-m", "bump a")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, head, "bump a")
	h.drain()

	if h.app.historyView.diff.Layout() != editor.DiffSplit {
		t.Fatalf("default history diff layout = %v, want DiffSplit", h.app.historyView.diff.Layout())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleSplitter, "Divider"); !ok {
		t.Fatal("expected Split divider in history diff")
	}
	if err := h.ui.Click("Unified"); err != nil {
		t.Fatalf("click Unified: %v", err)
	}
	h.ui.Frame()
	if h.app.historyView.diff.Layout() != editor.DiffUnified {
		t.Fatalf("layout after Unified click = %v, want DiffUnified", h.app.historyView.diff.Layout())
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "var a = 2") {
		t.Fatalf("unified diff missing insertion: %q", ins)
	}
}

// Rapidly switching the selected commit discards the slower first detail
// read: the UI ends on the second commit even though its worker finished
// first.
func TestHistoryRapidCommitSwitchDiscardsStaleDetail(t *testing.T) {
	repo := newGitRepo(t, "hist-race")
	hashA := repo.CommitFile("a.txt", "alpha\n", "commit alpha")
	hashB := repo.CommitFile("b.txt", "beta\n", "commit beta")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	h.queue.PauseWorkers()
	clickCommitRow(t, h, hashA, "commit alpha")
	h.ui.Frame()
	workerA := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	clickCommitRow(t, h, hashB, "commit beta")
	h.queue.RunNext(t) // beta's detail applies first
	h.ui.Frame()

	if got := h.app.historyView.detail.Message; !strings.HasPrefix(got, "commit beta") {
		t.Fatalf("detail after fast click = %q, want commit beta", got)
	}

	h.queue.ReleaseWorker(workerA)
	h.drain()

	if got := h.app.historyView.detail.Message; !strings.HasPrefix(got, "commit beta") {
		t.Fatalf("stale detail overwrote beta: message = %q", got)
	}
	if len(h.app.historyView.files) != 1 || h.app.historyView.files[0].Path != "b.txt" {
		t.Fatalf("file list = %+v, want exactly [b.txt]", h.app.historyView.files)
	}
}

// Rapidly switching the selected file discards the slower first diff read.
func TestHistoryRapidFileSwitchDiscardsStaleDiff(t *testing.T) {
	repo := newGitRepo(t, "hist-filerace")
	repo.CommitFile("aaa.txt", "aaa v1\n", "add aaa")
	repo.CommitFile("zzz.txt", "zzz v1\n", "add zzz")
	repo.WriteFile("aaa.txt", "aaa v2\n")
	repo.WriteFile("zzz.txt", "zzz v2\n")
	repo.Git("add", "aaa.txt", "zzz.txt")
	repo.Git("commit", "-m", "update both")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, head, "update both")
	h.drain()

	hv := &h.app.historyView
	if len(hv.files) != 2 {
		t.Fatalf("files = %+v, want 2", hv.files)
	}
	// files[0] auto-loaded. Switch to files[1] (gated), then back to files[0]
	// before files[1]'s worker finishes; the late result must be discarded.
	first, second := hv.files[0].Path, hv.files[1].Path
	h.queue.PauseWorkers()
	if err := h.ui.Click("File " + second); err != nil {
		t.Fatalf("click File %s: %v", second, err)
	}
	h.ui.Frame()
	workerSecond := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	if err := h.ui.Click("File " + first); err != nil {
		t.Fatalf("click File %s: %v", first, err)
	}
	h.queue.RunNext(t) // first file's diff applies first
	h.ui.Frame()

	ins := diffAccessLines(h.ui, ui.RoleInsertion)
	if !slices.Contains(ins, first[:3]+" v2") {
		t.Fatalf("first file diff not applied first: insertions=%q", ins)
	}

	h.queue.ReleaseWorker(workerSecond)
	h.drain()

	ins = diffAccessLines(h.ui, ui.RoleInsertion)
	if slices.Contains(ins, second[:3]+" v2") {
		t.Fatalf("stale diff for %s leaked after rapid file switch: insertions=%q", second, ins)
	}
	if hv.diff.req.File.Path != first {
		t.Fatalf("active diff path = %q, want %q", hv.diff.req.File.Path, first)
	}
}

// Keyboard navigation moves the commit selection like clicking does: Down
// selects the next row and its detail loads.
func TestHistoryKeyboardNavigationSelectsCommits(t *testing.T) {
	repo := newGitRepo(t, "hist-kbd")
	hashA := repo.CommitFile("a.txt", "a\n", "commit alpha")
	hashB := repo.CommitFile("b.txt", "b\n", "commit beta")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, hashB, "commit beta")
	h.drain()
	if h.app.historyView.sel != hashB {
		t.Fatalf("setup: sel = %q, want %q", h.app.historyView.sel, hashB)
	}

	h.ui.Key(0, ui.KeyDown)
	h.ui.Frame()
	h.drain()

	if h.app.historyView.sel != hashA {
		t.Fatalf("after Down: sel = %q, want %q (commit alpha)", h.app.historyView.sel, hashA)
	}
	if !strings.HasPrefix(h.app.historyView.detail.Message, "commit alpha") {
		t.Fatalf("detail after Down = %q, want commit alpha", h.app.historyView.detail.Message)
	}
	// Keyboard selection moves the list item's Selected state with it.
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(hashA, "commit alpha")); !ok || !row.Selected {
		t.Fatalf("alpha row after Down = %+v (found=%v), want Selected=true", row, ok)
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(hashB, "commit beta")); !ok || row.Selected {
		t.Fatalf("beta row after Down = %+v (found=%v), want Selected=false", row, ok)
	}

	h.ui.Key(0, ui.KeyUp)
	h.ui.Frame()
	h.drain()
	if h.app.historyView.sel != hashB {
		t.Fatalf("after Up: sel = %q, want %q (commit beta)", h.app.historyView.sel, hashB)
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(hashB, "commit beta")); !ok || !row.Selected {
		t.Fatalf("beta row after Up = %+v (found=%v), want Selected=true", row, ok)
	}
}

// Amending the selected commit replaces it with a new hash: the reload no
// longer lists the old hash, the list drops its selection, and the detail
// pane clears instead of showing a commit the history no longer has.
func TestHistoryAmendSelectedCommitClearsDetail(t *testing.T) {
	repo := newGitRepo(t, "hist-amend")
	head := repo.CommitFile("z.txt", "z1\n", "original commit")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, head, "original commit")
	h.drain()

	if h.app.historyView.sel != head {
		t.Fatalf("setup: sel = %q, want %q", h.app.historyView.sel, head)
	}
	if !h.ui.HasText("z.txt") {
		t.Fatalf("setup: missing file list; texts=%q", h.ui.Texts())
	}

	repo.WriteFile("z.txt", "z2\n")
	repo.Git("add", "z.txt")
	repo.Git("commit", "--amend", "-m", "amended commit")
	h.app.session.Reload()
	h.drain()

	hv := &h.app.historyView
	if len(h.app.session.Snapshot().Commits) != 1 {
		t.Fatalf("commits after amend = %d, want 1", len(h.app.session.Snapshot().Commits))
	}
	if h.app.session.Snapshot().Commits[0].Hash == head {
		t.Fatal("amend did not replace the commit hash")
	}
	if hv.sel != "" {
		t.Fatalf("selection not cleared after amend: %q", hv.sel)
	}
	if hv.listSel != -1 {
		t.Fatalf("list selection = %d, want -1", hv.listSel)
	}
	if len(hv.files) != 0 {
		t.Fatalf("files not cleared after amend: %+v", hv.files)
	}
	if hv.diff.req.Kind != diffNone {
		t.Fatalf("diff not cleared after amend: %+v", hv.diff.req)
	}
	if !h.ui.HasText("No commit selected") {
		t.Fatalf("missing empty-detail hint; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("amended commit") {
		t.Fatalf("amended commit not listed; texts=%q", h.ui.Texts())
	}
}

// A commit with no file changes shows an empty file list and an empty diff
// panel rather than the previous selection's content.
func TestHistoryEmptyCommitClearsFileListAndDiff(t *testing.T) {
	repo := newGitRepo(t, "hist-empty")
	repo.CommitFile("z.txt", "z1\n", "add z")
	repo.WriteFile("z.txt", "z2\n")
	repo.Git("add", "z.txt")
	repo.Git("commit", "-m", "update z")
	withChanges := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.Git("commit", "--allow-empty", "-m", "empty commit")
	emptyHead := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	clickCommitRow(t, h, withChanges, "update z")
	h.drain()
	if len(h.app.historyView.files) == 0 {
		t.Fatal("expected files for update z")
	}

	clickCommitRow(t, h, emptyHead, "empty commit")
	h.drain()

	if len(h.app.historyView.files) != 0 {
		t.Fatalf("files = %+v, want none for empty commit", h.app.historyView.files)
	}
	if !h.ui.HasText("No files changed") {
		t.Fatalf("missing empty file-list hint; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("No File Selected") {
		t.Fatalf("diff panel not cleared for empty commit; texts=%q", h.ui.Texts())
	}
}

// An empty repository (no commits) loads cleanly without error and shows a
// clear empty state instead of the commit list.
func TestHistoryEmptyRepositoryShowsEmptyState(t *testing.T) {
	repo := newGitRepo(t, "hist-bare")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("empty repository snapshot load failed: %v", err)
	}
	if !h.ui.HasText("No commits yet") {
		t.Fatalf("missing empty state; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("Load More") {
		t.Fatal("empty repository unexpectedly offers Load More")
	}
}

// Opening a path that does not exist fails the very first snapshot load with
// an empty zero-value snapshot. That failure must surface as the readable
// missing-repository notice, not read as an empty repository.
func TestHistoryFirstOpenFailureShowsErrorNotEmptyState(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	h := newTestApp(t)
	h.openRepo(store.Repo{Kind: store.Local, Path: missing, Name: "missing-repo"})
	h.drain()

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()

	if h.app.session.LoadErr() == nil {
		t.Fatal("expected snapshot load error for a missing path")
	}
	if !h.ui.HasText("Repository Not Found") {
		t.Fatalf("missing the Repository Not Found notice; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("No commits yet") {
		t.Fatal("first-open failure swallowed into empty repository state")
	}
}

// A repository the snapshot loader cannot read shows a clear failure.
func TestHistoryLoadFailureShowsError(t *testing.T) {
	repo := newGitRepo(t, "hist-fail")
	repo.CommitFile("a.txt", "a\n", "commit a")
	dir := repo.Path()

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Corrupt the repository after it is open, then reload: the snapshot
	// load fails and the History page surfaces the error.
	if err := os.RemoveAll(dir + "/.git/objects"); err != nil {
		t.Fatalf("corrupt repo: %v", err)
	}
	h.app.session.Reload()
	h.drain()

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()
	if h.app.session.LoadErr() == nil {
		t.Fatal("expected snapshot load error after corrupting objects")
	}
	if !h.ui.HasText("Failed to load repository snapshot") {
		t.Fatalf("missing snapshot failure message; texts=%q", h.ui.Texts())
	}
}

// Switching repositories resets the History page selection, file list and
// diff so no trace of the previous repository remains.
func TestHistorySwitchRepoResetsSelection(t *testing.T) {
	repoA := newGitRepo(t, "hist-swap-a")
	headA := repoA.CommitFile("only-a.txt", "a1\n", "commit in alpha")
	repoB := newGitRepo(t, "hist-swap-b")
	repoB.CommitFile("only-b.txt", "b1\n", "commit in beta")

	h := newTestApp(t, repoB.StoreRepo())
	openHistoryPage(t, h, repoA.StoreRepo())

	clickCommitRow(t, h, headA, "commit in alpha")
	h.drain()
	if !h.ui.HasText("only-a.txt") {
		t.Fatalf("setup: missing alpha file list; texts=%q", h.ui.Texts())
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "a1") {
		t.Fatalf("setup: alpha diff missing: %q", ins)
	}

	// Switch to repoB through the Repository Switcher UI.
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("hist-swap-b"); err != nil {
		t.Fatalf("select hist-swap-b: %v", err)
	}
	h.ui.Frame()
	h.drain()

	hv := &h.app.historyView
	if hv.sel != "" {
		t.Fatalf("selection not reset after repo switch: %q", hv.sel)
	}
	if len(hv.files) != 0 {
		t.Fatalf("files not reset after repo switch: %+v", hv.files)
	}
	if hv.diff.req.Kind != diffNone {
		t.Fatalf("diff not reset after repo switch: %+v", hv.diff.req)
	}
	if h.ui.HasText("only-a.txt") {
		t.Fatalf("stale alpha filename still rendered; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("commit in beta") {
		t.Fatalf("beta commits not listed after switch; texts=%q", h.ui.Texts())
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); slices.Contains(ins, "a1") {
		t.Fatalf("stale alpha diff still rendered: %q", ins)
	}
}

// A commit-detail read that fails shows an error with a working retry
// instead of corrupting the page.
func TestHistoryDetailErrorAndRetry(t *testing.T) {
	repo := newGitRepo(t, "hist-err")
	head := repo.CommitFile("e.txt", "e\n", "commit e")
	second := repo.CommitFile("f.txt", "f\n", "commit f")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// Select the newer commit first; its detail must load cleanly.
	clickCommitRow(t, h, second, "commit f")
	h.drain()
	if h.app.historyView.detailErr != nil {
		t.Fatalf("setup: unexpected detail error: %v", h.app.historyView.detailErr)
	}

	// Hide the object store so the next detail read fails, then select the
	// other commit — a fresh selection is what triggers the read, exactly as
	// a user clicking around a repository whose objects went missing.
	objects := repo.Path() + "/.git/objects"
	backup := repo.Path() + "/.git/objects-backup"
	if err := os.Rename(objects, backup); err != nil {
		t.Fatalf("hide objects: %v", err)
	}
	clickCommitRow(t, h, head, "commit e")
	h.drain()

	if h.app.historyView.detailErr == nil {
		t.Fatal("expected detail error with missing objects")
	}
	if !h.ui.HasText("Failed to load commit details") {
		t.Fatalf("missing detail failure message; texts=%q", h.ui.Texts())
	}

	// Restore the object store and retry through the error view's button.
	if err := os.Rename(backup, objects); err != nil {
		t.Fatalf("restore objects: %v", err)
	}
	if err := h.ui.Click("Retry Commit Details"); err != nil {
		t.Fatalf("click Retry Commit Details: %v", err)
	}
	h.drain()
	if h.app.historyView.detailErr != nil {
		t.Fatalf("detail error after retry: %v", h.app.historyView.detailErr)
	}
	if len(h.app.historyView.files) != 1 || h.app.historyView.files[0].Path != "e.txt" {
		t.Fatalf("files after retry = %+v, want [e.txt]", h.app.historyView.files)
	}
}

// --- SSH loopback ---

// Opening a loopback SSH repository loads the first 1000 commits, Load More
// fetches the rest, and selecting a commit shows its detail and file diff —
// all over a real SSH connection.
func TestHistoryLoopbackSSHFirstPageLoadMoreDetailAndDiff(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)

	// Build 1020 commits on the remote in one fast-import stream.
	var b strings.Builder
	for i := 1; i <= 1020; i++ {
		subject := fmt.Sprintf("imported %d", i)
		content := fmt.Sprintf("imported content %d\n", i)
		fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\n", i)
		fmt.Fprintf(&b, "author Branchline Test <test@branchline.invalid> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "committer Branchline Test <test@branchline.invalid> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "data %d\n%s\n", len(subject), subject)
		if i == 1 {
			head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
			fmt.Fprintf(&b, "from %s\n", head)
		} else {
			fmt.Fprintf(&b, "from :%d\n", i-1)
		}
		fmt.Fprintf(&b, "M 100644 inline history.txt\ndata %d\n%s\n", len(content), content)
	}
	srv.RunShellInput(t, "cd "+git.ShellQuote(repo.Path)+" && GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git fast-import --quiet", []byte(b.String()))

	wantHead := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	total := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-list", "--count", "HEAD"))
	if total != "1021" { // 1020 imported + the initial commit
		t.Fatalf("remote commit count = %s, want 1021", total)
	}

	h := newTestApp(t, repo)
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click(repo.Name); err != nil {
		t.Fatalf("select SSH repo: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if got := len(h.app.session.Snapshot().Commits); got != 1000 {
		t.Fatalf("SSH first page commits = %d, want 1000", got)
	}
	if !h.app.session.Snapshot().More {
		t.Fatal("SSH snapshot.More = false, want true with 1021 commits")
	}
	if h.app.session.Snapshot().Status.Head != wantHead {
		t.Fatalf("SSH HEAD = %q, want %q", h.app.session.Snapshot().Status.Head, wantHead)
	}

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Load More"); !ok {
		t.Fatalf("Load More missing on SSH history; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "imported 1020") {
		t.Fatalf("newest commit missing on first page; texts=%q", h.ui.Texts())
	}
	if hasExactText(h.ui, "imported 1") {
		t.Fatal("oldest imported commit unexpectedly on first page")
	}

	if err := h.ui.Click("Load More"); err != nil {
		t.Fatalf("click Load More: %v", err)
	}
	h.drain()

	if got := len(h.app.session.Snapshot().Commits); got != 1021 {
		t.Fatalf("commits after Load More = %d, want 1021", got)
	}
	if h.app.session.Snapshot().More {
		t.Fatal("snapshot.More still true after loading all 1021 commits")
	}
	// The commit list is virtualized: rows outside the window are not built.
	// Focus it by clicking the newest commit's row, then drive it with the
	// keyboard as a user would: End to reach the oldest commit, Home to get
	// back to the top, and Page Down to the middle commit's row.
	mid := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD~500"))
	midSubject := strings.TrimSpace(srv.RunGit(t, repo.Path, "show", "-s", "--format=%s", mid))

	topCommit := h.app.session.Snapshot().Commits[0]
	clickCommitRow(t, h, topCommit.Hash, topCommit.Subject)
	h.drain()

	h.ui.Key(0, ui.KeyEnd)
	h.ui.Frame()
	if !hasExactText(h.ui, "imported 1") {
		t.Fatalf("oldest imported commit missing after Load More and scrolling to end; texts=%q", h.ui.Texts())
	}

	// End moved the selection to the oldest commit, which started its detail
	// read; Home moves it back to the top commit and starts another. Drain
	// both before paging down so the only read in flight when the middle
	// commit is clicked is its own — the same end state a user pausing on
	// each row would produce, with no dependence on worker timing.
	h.ui.Key(0, ui.KeyHome)
	h.ui.Frame()
	h.drain()
	found := false
	for range 60 {
		if _, ok := h.ui.Find(commitRowLabel(mid, midSubject)); ok {
			found = true
			break
		}
		h.ui.Key(0, ui.KeyPageDown)
		h.ui.Frame()
	}
	if !found {
		t.Fatalf("commit row for %q never appeared while paging down; texts=%q", midSubject, h.ui.Texts())
	}
	clickCommitRow(t, h, mid, midSubject)
	h.drain()

	if h.app.historyView.detailErr != nil {
		t.Fatalf("SSH detail error: %v", h.app.historyView.detailErr)
	}
	if !h.ui.HasText(mid) {
		t.Fatalf("SSH detail missing full hash; texts=%q", h.ui.Texts())
	}
	files := h.app.historyView.files
	if len(files) != 1 || files[0].Path != "history.txt" {
		t.Fatalf("SSH files = %+v, want exactly [history.txt]", files)
	}
	wantIns := "imported content " + strings.TrimPrefix(midSubject, "imported ")
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, wantIns) {
		d := &h.app.historyView.diff
		t.Fatalf("SSH diff insertions = %q, want %q (panel: loading=%v err=%v empty=%v binary=%v tooLarge=%v kind=%v reqPath=%q sel=%q detailLoading=%v)",
			ins, wantIns, d.loading, d.err, d.empty, d.binary, d.tooLarge, d.req.Kind, d.req.File.Path,
			h.app.historyView.sel, h.app.historyView.detailLoading)
	}
	if h.app.historyView.diff.req.Kind != diffCommitFile {
		t.Fatalf("SSH diff kind = %v, want diffCommitFile", h.app.historyView.diff.req.Kind)
	}
}

// --- Selected-row contrast ---

// contrastFixture holds the geometry of the selected commit row of one
// rendered frame, in image pixels, for the pixel-contrast assertions.
type contrastFixture struct {
	img     *image.RGBA
	scale   float32
	row     ui.Rect // DIPs
	subject ui.Rect // DIPs
	author  ui.Rect // DIPs
	sha     ui.Rect // DIPs
	badges  map[string]ui.Rect
	rowBg   color.RGBA
	otherBg color.RGBA
}

// wcagLinearChannel linearizes one 8-bit sRGB channel as WCAG relative
// luminance defines it.
func wcagLinearChannel(v uint8) float64 {
	s := float64(v) / 255
	if s <= 0.04045 {
		return s / 12.92
	}
	return math.Pow((s+0.055)/1.055, 2.4)
}

// wcagLuminance is the WCAG relative luminance of an opaque 8-bit color.
func wcagLuminance(c color.RGBA) float64 {
	return 0.2126*wcagLinearChannel(c.R) + 0.7152*wcagLinearChannel(c.G) + 0.0722*wcagLinearChannel(c.B)
}

// wcagContrast is the WCAG contrast ratio between two opaque colors.
func wcagContrast(a, b color.RGBA) float64 {
	la, lb := wcagLuminance(a), wcagLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// maxChannelDiff is the largest per-channel difference between two colors.
func maxChannelDiff(a, b color.RGBA) uint8 {
	diff := func(x, y uint8) uint8 {
		if x > y {
			return x - y
		}
		return y - x
	}
	return max(diff(a.R, b.R), diff(a.G, b.G), diff(a.B, b.B))
}

// dipRectToPixels converts a DIP rectangle to the enclosing pixel rectangle
// of a scale× image, clamped to the image bounds.
func dipRectToPixels(img *image.RGBA, r ui.Rect, scale float32) image.Rectangle {
	x0 := int(math.Floor(float64(r.X * scale)))
	y0 := int(math.Floor(float64(r.Y * scale)))
	x1 := int(math.Ceil(float64((r.X + r.W) * scale)))
	y1 := int(math.Ceil(float64((r.Y + r.H) * scale)))
	b := img.Bounds()
	return image.Rect(max(x0, b.Min.X), max(y0, b.Min.Y), min(x1, b.Max.X), min(y1, b.Max.Y))
}

// dominantColor returns the most frequent color inside px, the one a flat
// fill leaves there.
func dominantColor(img *image.RGBA, px image.Rectangle) color.RGBA {
	counts := map[color.RGBA]int{}
	best := color.RGBA{}
	bestN := 0
	for y := px.Min.Y; y < px.Max.Y; y++ {
		for x := px.Min.X; x < px.Max.X; x++ {
			c := img.RGBAAt(x, y)
			counts[c]++
			if counts[c] > bestN {
				bestN = counts[c]
				best = c
			}
		}
	}
	return best
}

// peakContrastIn is the highest WCAG contrast ratio any pixel inside the DIP
// rectangle reaches against bg — the contrast of the densest glyph stroke,
// because antialiased edge pixels never reach it.
func peakContrastIn(img *image.RGBA, r ui.Rect, scale float32, bg color.RGBA) float64 {
	px := dipRectToPixels(img, r, scale)
	peak := 0.0
	for y := px.Min.Y; y < px.Max.Y; y++ {
		for x := px.Min.X; x < px.Max.X; x++ {
			if c := wcagContrast(img.RGBAAt(x, y), bg); c > peak {
				peak = c
			}
		}
	}
	return peak
}

// accessRectInRow finds the accessibility element exactly named s whose
// center lies inside row, distinguishing the commit-row text from same-named
// entries of the sidebar or the detail pane.
func accessRectInRow(all []ui.AccessInfo, row ui.Rect, s string) (ui.Rect, bool) {
	for _, a := range all {
		if a.Name != s {
			continue
		}
		cx, cy := a.Bounds.X+a.Bounds.W/2, a.Bounds.Y+a.Bounds.H/2
		if cx >= row.X && cx <= row.X+row.W && cy >= row.Y && cy <= row.Y+row.H {
			return a.Bounds, true
		}
	}
	return ui.Rect{}, false
}

// captureContrastFixture renders the current frame at the given theme and
// captures the geometry of the selected commit row together with its
// dominant background color and that of the first unselected row below it.
func captureContrastFixture(t *testing.T, h *testHarness, rowLabel, otherLabel, subject, shortSha string, badgeNames []string, dark bool) contrastFixture {
	t.Helper()
	h.ui.SetDark(dark)
	settleFrame(h.ui)

	f := contrastFixture{img: copyImage(h.ui.Image()), scale: 2, badges: map[string]ui.Rect{}}
	h.ui.SetDark(false)
	h.ui.Frame()

	var ok bool
	f.row, ok = h.ui.Find(rowLabel)
	if !ok {
		t.Fatalf("selected row %q not found; texts=%q", rowLabel, h.ui.Texts())
	}
	otherRow, ok := h.ui.Find(otherLabel)
	if !ok {
		t.Fatalf("unselected row %q not found; texts=%q", otherLabel, h.ui.Texts())
	}
	all := h.ui.AccessAll()
	if f.subject, ok = accessRectInRow(all, f.row, subject); !ok {
		t.Fatalf("subject text %q not found inside selected row", subject)
	}
	if f.author, ok = accessRectInRow(all, f.row, "Branchline Test"); !ok {
		t.Fatalf("author text not found inside selected row")
	}
	if f.sha, ok = accessRectInRow(all, f.row, shortSha); !ok {
		t.Fatalf("short hash %q not found inside selected row", shortSha)
	}
	for _, name := range badgeNames {
		r, ok := accessRectInRow(all, f.row, name)
		if !ok {
			t.Fatalf("badge %q not found inside selected row", name)
		}
		f.badges[name] = r
	}

	// The dominant color of a row's strip is its flat fill: text, badges and
	// graph pixels are a small minority of a full-width row.
	f.rowBg = dominantColor(f.img, dipRectToPixels(f.img, f.row, f.scale))
	f.otherBg = dominantColor(f.img, dipRectToPixels(f.img, otherRow, f.scale))
	return f
}

// pillPixelAt samples the pill color of a badge just inside its horizontal
// padding, left of the text, at the badge's vertical center.
func pillPixelAt(f contrastFixture, badge ui.Rect) color.RGBA {
	x := int((badge.X - 3) * f.scale)
	y := int((badge.Y + badge.H/2) * f.scale)
	return f.img.RGBAAt(x, y)
}

// checkContrastFixture verifies selected-row readability on one rendered
// frame: the selected row fill is clearly distinct from unselected rows, the
// subject and metadata text reach readable peak contrast against it, and
// every badge has a pill distinguishable from the row fill with readable
// text on it.
func checkContrastFixture(t *testing.T, f contrastFixture, theme string) {
	t.Helper()
	if maxChannelDiff(f.rowBg, f.otherBg) < 40 {
		t.Fatalf("%s: selected row fill %+v barely differs from unselected fill %+v", theme, f.rowBg, f.otherBg)
	}
	if peak := peakContrastIn(f.img, f.subject, f.scale, f.rowBg); peak < 4.5 {
		t.Fatalf("%s: subject peak contrast %.2f against row fill %+v, want >= 4.5", theme, peak, f.rowBg)
	}
	for _, meta := range []struct {
		name string
		r    ui.Rect
	}{{"author", f.author}, {"short hash", f.sha}} {
		if peak := peakContrastIn(f.img, meta.r, f.scale, f.rowBg); peak < 3.8 {
			t.Fatalf("%s: %s peak contrast %.2f against row fill %+v, want >= 3.8", theme, meta.name, peak, f.rowBg)
		}
	}
	names := make([]string, 0, len(f.badges))
	for name := range f.badges {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		badge := f.badges[name]
		pill := pillPixelAt(f, badge)
		if d := maxChannelDiff(pill, f.rowBg); d < 25 {
			t.Fatalf("%s: badge %q pill %+v merges into row fill %+v (channel diff %d < 25)", theme, name, pill, f.rowBg, d)
		}
		if peak := peakContrastIn(f.img, badge, f.scale, pill); peak < 4.5 {
			t.Fatalf("%s: badge %q text peak contrast %.2f against pill %+v, want >= 4.5", theme, name, peak, pill)
		}
	}
}

// A selected commit row keeps its subject, author, short hash, date and every
// ref badge readable in both themes: the filled selection background is
// clearly distinct from unselected rows, white text reaches WCAG contrast
// against it, and badges keep a visible pill with readable text instead of
// dissolving into the fill.
func TestHistorySelectedCommitRowContrast(t *testing.T) {
	// Attached HEAD: the newest commit carries HEAD → main, origin/main and
	// v1.0.0 badges; the older base commit stays unselected for comparison.
	repo := newGitRepo(t, "hist-contrast")
	base := repo.CommitFile("base.txt", "base\n", "base commit")
	badge := repo.CommitFile("feat.txt", "feat\n", "contrast fixture subject")
	repo.CreateTag("v1.0.0")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	h.ui.SetScale(2)
	h.ui.SetWindowActive(true)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, badge, "contrast fixture subject")
	h.drain()
	settleFrame(h.ui)

	rowLabel := commitRowLabel(badge, "contrast fixture subject")
	otherLabel := commitRowLabel(base, "base commit")
	shortSha := short7(badge)
	badges := []string{"HEAD → main", "origin/main", "v1.0.0"}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		f := captureContrastFixture(t, h, rowLabel, otherLabel, "contrast fixture subject", shortSha, badges, dark)
		checkContrastFixture(t, f, theme)
	}

	// Detached HEAD on a commit no ref names: the row carries only the plain
	// HEAD badge, which must stay just as visible on the selection fill.
	drepo := newGitRepo(t, "hist-contrast-detached")
	drepo.CommitFile("base.txt", "base\n", "base commit")
	detached := drepo.CommitFile("tip.txt", "tip\n", "detached fixture subject")
	drepo.Git("checkout", "--detach", detached)

	dh := newTestApp(t)
	dh.ui.SetScale(2)
	dh.ui.SetWindowActive(true)
	openHistoryPage(t, dh, drepo.StoreRepo())
	clickCommitRow(t, dh, detached, "detached fixture subject")
	dh.drain()
	settleFrame(dh.ui)

	if row, ok := dh.ui.AccessByRole(ui.RoleListItem, commitRowLabel(detached, "detached fixture subject")); !ok || !row.Selected {
		t.Fatalf("detached row access = %+v (found=%v), want Selected=true", row, ok)
	}
	dRowLabel := commitRowLabel(detached, "detached fixture subject")
	dOtherLabel := commitRowLabel(strings.TrimSpace(drepo.Git("rev-parse", "HEAD~1")), "base commit")
	dBadges := []string{"HEAD"}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		f := captureContrastFixture(t, dh, dRowLabel, dOtherLabel, "detached fixture subject", short7(detached), dBadges, dark)
		checkContrastFixture(t, f, "detached "+theme)
	}
}
