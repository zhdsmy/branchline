package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// enterCompareLocal opens the commit-to-working-tree comparison of the commit
// through the real entry point — the row's context menu — and drains the
// reads it starts.
func enterCompareLocal(t *testing.T, h *testHarness, hash, subject string) {
	t.Helper()
	row := commitRowLabel(hash, subject)
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row %q: %v\ntexts=%q", row, err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem("Compare to Local Changes"); err != nil {
		t.Fatalf("choose Compare to Local Changes: %v; menu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	h.drain()
}

// worktreeNameStatus renders the oracle file list of a commit-to-worktree
// comparison: git diff --name-status <rev> plus every untracked file as '?',
// in the order WorktreeFiles produces.
func worktreeNameStatus(t *testing.T, repo *gitRepoFixture, rev string) []string {
	t.Helper()
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(repo.Git("diff", "--name-status", rev)), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	for _, l := range strings.Split(strings.TrimSpace(repo.Git("ls-files", "--others", "--exclude-standard")), "\n") {
		if l != "" {
			lines = append(lines, "?\t"+l)
		}
	}
	return lines
}

// worktreeDiffContentLines extracts the content lines of one file's patch in
// git diff <rev> -- <path>, as gitDiffContentLines does for a range diff.
func worktreeDiffContentLines(t *testing.T, repo *gitRepoFixture, rev, path string, sign byte) []string {
	t.Helper()
	out := repo.Git("diff", rev, "--", path)
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if len(l) < 2 || l[0] != sign {
			continue
		}
		if strings.HasPrefix(l, "+++") || strings.HasPrefix(l, "---") {
			continue
		}
		lines = append(lines, l[1:])
	}
	return lines
}

// assertDiffMatchesWorktree asserts the diff panel shows exactly the content
// lines of git diff <rev> -- <path>, as assertDiffMatchesRange does for a
// range diff.
func assertDiffMatchesWorktree(t *testing.T, h *testHarness, repo *gitRepoFixture, rev, path string) {
	t.Helper()
	ins := diffAccessLines(h.ui, ui.RoleInsertion)
	del := diffAccessLines(h.ui, ui.RoleDeletion)
	wantIns := worktreeDiffContentLines(t, repo, rev, path, '+')
	wantDel := worktreeDiffContentLines(t, repo, rev, path, '-')
	for _, l := range wantIns {
		if !slices.Contains(ins, l) {
			t.Fatalf("insertions %q miss worktree-diff added line %q", ins, l)
		}
		if slices.Contains(del, l) {
			t.Fatalf("added line %q wrongly shows as a deletion too", l)
		}
	}
	for _, l := range wantDel {
		if !slices.Contains(del, l) {
			t.Fatalf("deletions %q miss worktree-diff removed line %q", del, l)
		}
		if slices.Contains(ins, l) {
			t.Fatalf("removed line %q wrongly shows as an insertion too", l)
		}
	}
	if len(ins) != len(wantIns) {
		t.Fatalf("insertions %q are not exactly the worktree-diff added lines %q", ins, wantIns)
	}
	if len(del) != len(wantDel) {
		t.Fatalf("deletions %q are not exactly the worktree-diff removed lines %q", del, wantDel)
	}
}

// Choosing Compare to Local Changes on a commit row compares the commit with
// the working tree: the header carries the commit and the Local Changes row,
// the file list is the union of git diff --name-status <commit> and the
// untracked files, the first file is selected, and its diff matches git diff
// <commit> -- <file>.
func TestHistoryCompareLocalEntryAndFileList(t *testing.T) {
	repo, f := newCompareRepo(t, "cl-basic")
	repo.WriteFile("alpha.txt", "alpha v1\nworktree line\n")
	repo.WriteFile("fresh.txt", "fresh\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	enterCompareLocal(t, h, f.c3, "third shrink")
	h.drain()

	hv := &h.app.historyView
	if hv.mode != historyCompareLocal {
		t.Fatalf("mode = %d, want historyCompareLocal (%d)", hv.mode, historyCompareLocal)
	}
	if hv.compareLocal.commit.Hash != f.c3 {
		t.Fatalf("compareLocal commit = %q, want %q", short7(hv.compareLocal.commit.Hash), short7(f.c3))
	}
	if hv.sel != f.c3 {
		t.Fatalf("sel = %q, want %q", hv.sel, f.c3)
	}

	// The header carries the commit row above the Local Changes row.
	if _, ok := h.ui.Find("Comparison Commit " + short7(f.c3)); !ok {
		t.Fatalf("comparison commit row missing; texts=%q", h.ui.Texts())
	}
	commitRect, ok := h.ui.Find("Comparison Commit " + short7(f.c3))
	if !ok {
		t.Fatal("unreachable")
	}
	localRect, ok := h.ui.Find("Comparison Local Changes")
	if !ok {
		t.Fatalf("Local Changes row missing; texts=%q", h.ui.Texts())
	}
	if localRect.Y <= commitRect.Y {
		t.Fatalf("Local Changes row (y=%.1f) is not below the commit row (y=%.1f)", localRect.Y, commitRect.Y)
	}
	// There is no direction to swap.
	if h.ui.HasText("Swap") {
		t.Fatalf("swap control rendered for a working-tree comparison; texts=%q", h.ui.Texts())
	}

	// The file list is the worktree comparison exactly: the modified
	// alpha.txt and the untracked fresh.txt.
	want := worktreeNameStatus(t, repo, f.c3)
	if len(want) == 0 {
		t.Fatal("fixture produces no worktree diff")
	}
	got := compareFileLines(hv.compareLocal.files)
	if !slices.Equal(got, want) {
		t.Fatalf("compare-local files = %q, want %q", got, want)
	}
	if got[0] != "M\talpha.txt" || got[len(got)-1] != "?\tfresh.txt" {
		t.Fatalf("compare-local files = %q, want M alpha.txt first and ? fresh.txt last", got)
	}

	// The first file is selected and its diff matches the worktree diff.
	if hv.compareLocal.fileSel != 0 {
		t.Fatalf("fileSel = %d, want 0 (first file auto-selected)", hv.compareLocal.fileSel)
	}
	first := hv.compareLocal.files[0]
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, "File "+first.Path); !ok || !row.Selected {
		t.Fatalf("first file row access = %+v (found=%v), want Selected=true", row, ok)
	}
	if hv.diff.req.Kind != diffWorktreeFile {
		t.Fatalf("diff kind = %v, want diffWorktreeFile", hv.diff.req.Kind)
	}
	assertDiffMatchesWorktree(t, h, repo, f.c3, first.Path)
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "worktree line") {
		t.Fatalf("diff insertions = %q, want the worktree line", ins)
	}
}

// Opening an untracked file of the comparison shows its whole content as
// added: the diff of /dev/null against the file.
func TestHistoryCompareLocalUntrackedFileDiff(t *testing.T) {
	repo, f := newCompareRepo(t, "cl-untracked")
	repo.WriteFile("alpha.txt", "alpha v1\nworktree line\n")
	repo.WriteFile("fresh.txt", "fresh one\nfresh two\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	enterCompareLocal(t, h, f.c3, "third shrink")
	h.drain()

	if err := h.ui.Click("File fresh.txt"); err != nil {
		t.Fatalf("click File fresh.txt: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()

	hv := &h.app.historyView
	if hv.diff.req.Kind != diffWorktreeFile || hv.diff.req.File.Path != "fresh.txt" {
		t.Fatalf("diff request = %+v, want the untracked file's worktree diff", hv.diff.req)
	}
	if hv.diff.err != nil {
		t.Fatalf("diff error: %v", hv.diff.err)
	}
	ins := diffAccessLines(h.ui, ui.RoleInsertion)
	for _, want := range []string{"fresh one", "fresh two"} {
		if !slices.Contains(ins, want) {
			t.Fatalf("untracked diff insertions = %q, want %q", ins, want)
		}
	}
	if len(ins) != 2 {
		t.Fatalf("untracked diff insertions = %q, want exactly the file's two lines", ins)
	}
	if del := diffAccessLines(h.ui, ui.RoleDeletion); len(del) != 0 {
		t.Fatalf("untracked diff deletions = %q, want none", del)
	}
}

// Right-clicking a row that is not the selected one and choosing Compare to
// Local Changes narrows the selection to the right-clicked commit: the
// comparison acts on the row the menu was opened for, not on the row that
// happened to be selected.
func TestHistoryCompareLocalActsOnRightClickedRow(t *testing.T) {
	repo, f := newCompareRepo(t, "cl-row")
	repo.WriteFile("alpha.txt", "alpha v1\nworktree line\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, f.c2, "second grow")
	h.drain()

	hv := &h.app.historyView
	if hv.sel != f.c2 {
		t.Fatalf("setup: sel = %q, want second grow", hv.sel)
	}
	enterCompareLocal(t, h, f.c1, "first seed")
	h.drain()

	if hv.mode != historyCompareLocal {
		t.Fatalf("mode = %d, want historyCompareLocal (%d)", hv.mode, historyCompareLocal)
	}
	if hv.compareLocal.commit.Hash != f.c1 {
		t.Fatalf("compareLocal commit = %q, want the right-clicked %q", short7(hv.compareLocal.commit.Hash), short7(f.c1))
	}
	if hv.sel != f.c1 {
		t.Fatalf("sel = %q, want the right-clicked commit %q", hv.sel, f.c1)
	}
	if hv.selection.Len() != 1 || !hv.selection.Has(f.c1) {
		t.Fatalf("selection = %d keys, want only the right-clicked commit", hv.selection.Len())
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(f.c1, "first seed")); !ok || !row.Selected {
		t.Fatalf("right-clicked row access = %+v (found=%v), want Selected=true", row, ok)
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(f.c2, "second grow")); !ok || row.Selected {
		t.Fatalf("previously selected row access = %+v (found=%v), want Selected=false", row, ok)
	}
}

// The comparison survives frames of an unchanged selection: rendering again
// and again does not fall back to the single-commit detail. Show Commit
// returns to the commit's detail, and clicking another commit row leaves the
// comparison for that commit's detail.
func TestHistoryCompareLocalStaysAndLeaves(t *testing.T) {
	repo, f := newCompareRepo(t, "cl-stay")
	repo.WriteFile("alpha.txt", "alpha v1\nworktree line\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	enterCompareLocal(t, h, f.c3, "third shrink")
	h.drain()

	hv := &h.app.historyView
	for i := 0; i < 3; i++ {
		h.ui.Frame()
		if hv.mode != historyCompareLocal {
			t.Fatalf("mode = %d after frame %d, want historyCompareLocal (%d)", hv.mode, i, historyCompareLocal)
		}
	}
	if _, ok := h.ui.Find("Comparison Local Changes"); !ok {
		t.Fatalf("comparison header missing after repeated frames; texts=%q", h.ui.Texts())
	}

	// Show Commit returns to the commit's own detail.
	if err := h.ui.Click("Show Commit"); err != nil {
		t.Fatalf("click Show Commit: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.drain()
	if hv.mode != historySingleCommit {
		t.Fatalf("mode = %d after Show Commit, want historySingleCommit (%d)", hv.mode, historySingleCommit)
	}
	if hv.sel != f.c3 {
		t.Fatalf("sel = %q after Show Commit, want %q", hv.sel, f.c3)
	}
	if !strings.HasPrefix(hv.detail.Message, "third shrink") {
		t.Fatalf("detail = %q after Show Commit, want the commit's detail", hv.detail.Message)
	}
	if hv.diff.req.Kind != diffCommitFile {
		t.Fatalf("diff kind = %v after Show Commit, want diffCommitFile", hv.diff.req.Kind)
	}

	// Re-enter and click another commit: the comparison is left behind.
	enterCompareLocal(t, h, f.c3, "third shrink")
	h.drain()
	if hv.mode != historyCompareLocal {
		t.Fatalf("mode = %d after re-entering, want historyCompareLocal", hv.mode)
	}
	clickCommitRow(t, h, f.c2, "second grow")
	h.ui.Frame()
	h.drain()
	if hv.mode != historySingleCommit {
		t.Fatalf("mode = %d after clicking another commit, want historySingleCommit (%d)", hv.mode, historySingleCommit)
	}
	if hv.sel != f.c2 {
		t.Fatalf("sel = %q after clicking another commit, want %q", hv.sel, f.c2)
	}
	if h.ui.HasText("Comparison Local Changes") {
		t.Fatalf("comparison header survived the click; texts=%q", h.ui.Texts())
	}
}

// A new snapshot — here the reload after external working-tree edits —
// re-reads the comparison's file list: the newly created untracked file
// appears, the previously selected file keeps its selection, and its diff is
// re-read against the changed working tree.
func TestHistoryCompareLocalWorktreeChangeRefreshes(t *testing.T) {
	repo, f := newCompareRepo(t, "cl-refresh")
	repo.WriteFile("alpha.txt", "alpha v1\nworktree line\n")
	repo.WriteFile("beta.txt", "beta v1\nbeta worktree\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	enterCompareLocal(t, h, f.c3, "third shrink")
	h.drain()

	hv := &h.app.historyView
	if len(hv.compareLocal.files) != 2 {
		t.Fatalf("setup files = %q, want alpha.txt and beta.txt", compareFileLines(hv.compareLocal.files))
	}
	if err := h.ui.Click("File beta.txt"); err != nil {
		t.Fatalf("click File beta.txt: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if hv.diff.req.File.Path != "beta.txt" {
		t.Fatalf("setup: diff file = %q, want beta.txt", hv.diff.req.File.Path)
	}

	// Edit the selected file and create an untracked one, then reload the
	// snapshot as the automatic refresh would after the file events.
	repo.WriteFile("beta.txt", "beta v1\nbeta worktree\nbeta again\n")
	repo.WriteFile("newfile.txt", "brand new\n")
	h.app.session.Reload()
	h.drain()
	h.drain()

	got := compareFileLines(hv.compareLocal.files)
	if !slices.Contains(got, "?\tnewfile.txt") {
		t.Fatalf("files after reload = %q, want the new untracked file listed", got)
	}
	if hv.mode != historyCompareLocal {
		t.Fatalf("mode = %d after reload, want historyCompareLocal", hv.mode)
	}
	if hv.compareLocal.fileSel < 0 || hv.compareLocal.fileSel >= len(hv.compareLocal.files) {
		t.Fatalf("fileSel = %d out of range after reload", hv.compareLocal.fileSel)
	}
	if kept := hv.compareLocal.files[hv.compareLocal.fileSel].Path; kept != "beta.txt" {
		t.Fatalf("selection after reload = %q, want beta.txt still selected", kept)
	}
	if hv.diff.req.File.Path != "beta.txt" {
		t.Fatalf("diff file after reload = %q, want beta.txt", hv.diff.req.File.Path)
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "beta again") {
		t.Fatalf("diff after reload insertions = %q, want the freshly written line", ins)
	}
}

// Switching to another commit while the comparison's file list is still
// being read discards the read: the slow result never surfaces under the
// single commit the selection moved to.
func TestHistoryCompareLocalRapidSwitchDiscardsStaleResult(t *testing.T) {
	repo, f := newCompareRepo(t, "cl-race")
	repo.WriteFile("alpha.txt", "alpha v1\nworktree line\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	h.queue.PauseWorkers()
	row := commitRowLabel(f.c3, "third shrink")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Compare to Local Changes"); err != nil {
		t.Fatalf("choose Compare to Local Changes: %v; menu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	compareWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	// Move to another commit before the comparison read finishes.
	clickCommitRow(t, h, f.c2, "second grow")
	h.ui.Frame()
	h.queue.ReleaseWorker(compareWorker)
	h.drain()
	h.drain()

	hv := &h.app.historyView
	if hv.mode != historySingleCommit {
		t.Fatalf("mode = %d, want single commit", hv.mode)
	}
	if !strings.HasPrefix(hv.detail.Message, "second grow") {
		t.Fatalf("detail = %q, want second grow", hv.detail.Message)
	}
	if len(hv.compareLocal.files) != 0 {
		t.Fatalf("stale compare-local files surfaced under the single selection: %+v", hv.compareLocal.files)
	}
	if hv.diff.req.Kind != diffCommitFile {
		t.Fatalf("diff kind = %v after the stale read finished, want diffCommitFile", hv.diff.req.Kind)
	}
	if len(hv.files) == 0 {
		t.Fatal("single-commit file list missing after the stale read finished")
	}
}

// While a repository write is in flight the Compare to Local Changes item is
// disabled: comparing against a working tree the write is mutating would
// race it.
func TestHistoryCompareLocalDisabledWhileBusy(t *testing.T) {
	repo := newGitRepo(t, "cl-busy")
	c1 := repo.CommitFile("base.txt", "base\n", "base commit")
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
	row := commitRowLabel(c1, "base commit")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row while busy: %v", err)
	}
	err := h.ui.ChooseMenuItem("Compare to Local Changes")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing Compare to Local Changes while busy = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	h.ui.Frame()
	if h.app.historyView.mode == historyCompareLocal {
		t.Fatal("compare-local mode entered while the session was busy")
	}

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage write failed: %v", err)
	}
}

// The new menu item sits between the replay items and the copy tail: the
// menu still opens with Checkout Commit and still ends with a separator and
// the two copy items.
func TestHistoryCompareLocalMenuStructure(t *testing.T) {
	repo := newGitRepo(t, "cl-menu")
	c1 := repo.CommitFile("base.txt", "base\n", "base commit")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	row := commitRowLabel(c1, "base commit")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row: %v", err)
	}
	menu := h.ui.Menu()
	if len(menu) < 5 || menu[0] != "Checkout Commit…" {
		t.Fatalf("menu %q does not start with Checkout Commit…", menu)
	}
	tail := menu[len(menu)-3:]
	if tail[0] != "-" || tail[1] != "Copy Commit SHA" || tail[2] != "Copy Commit Info" {
		t.Fatalf("menu %q does not end with separator + the two copy items", menu)
	}
	idx := slices.Index(menu, "Compare to Local Changes")
	if idx < 0 {
		t.Fatalf("menu %q misses Compare to Local Changes", menu)
	}
	if idx >= len(menu)-3 {
		t.Fatalf("Compare to Local Changes sits in the copy tail of menu %q", menu)
	}
	h.ui.CloseMenu()
	h.ui.Frame()
}

// The compared commit's short hash renders red and the Local Changes row
// green in the comparison header, in both themes.
func TestHistoryCompareLocalHeaderColors(t *testing.T) {
	repo, f := newCompareRepo(t, "cl-colors")
	repo.WriteFile("alpha.txt", "alpha v1\nworktree line\n")

	h := newTestApp(t)
	h.ui.SetScale(2)
	h.ui.SetWindowActive(true)
	openHistoryPage(t, h, repo.StoreRepo())
	enterCompareLocal(t, h, f.c3, "third shrink")
	settleFrame(h.ui)

	// textHueInRow finds the named text inside the labeled comparison row
	// and counts the dominantly red and green pixels of its rendering.
	textHueInRow := func(rowLabel, text string, dark bool) (red, green int) {
		h.ui.SetDark(dark)
		settleFrame(h.ui)
		img := copyImage(h.ui.Image())
		h.ui.SetDark(false)
		h.ui.Frame()

		all := h.ui.AccessAll()
		var rowRect ui.Rect
		found := false
		for _, a := range all {
			if a.Name == rowLabel {
				rowRect, found = a.Bounds, true
			}
		}
		if !found {
			t.Fatalf("comparison row %q not in the access tree", rowLabel)
		}
		var textRect ui.Rect
		for _, a := range all {
			if a.Name != text {
				continue
			}
			cx, cy := a.Bounds.X+a.Bounds.W/2, a.Bounds.Y+a.Bounds.H/2
			if cx >= rowRect.X && cx <= rowRect.X+rowRect.W && cy >= rowRect.Y && cy <= rowRect.Y+rowRect.H {
				textRect = a.Bounds
			}
		}
		if textRect.W == 0 {
			t.Fatalf("text %q not found inside %q", text, rowLabel)
		}
		px := dipRectToPixels(img, textRect, 2)
		for y := px.Min.Y; y < px.Max.Y; y++ {
			for x := px.Min.X; x < px.Max.X; x++ {
				cl := img.RGBAAt(x, y)
				mx := max(max(int(cl.R), int(cl.G)), int(cl.B))
				mn := min(min(int(cl.R), int(cl.G)), int(cl.B))
				if mx-mn < 40 {
					continue // background or unsaturated glyph edge
				}
				if int(cl.R) > int(cl.G)+30 && int(cl.R) > int(cl.B)+30 {
					red++
				}
				if int(cl.G) > int(cl.R)+20 && int(cl.G) > int(cl.B)+20 {
					green++
				}
			}
		}
		return red, green
	}

	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		red, green := textHueInRow("Comparison Commit "+short7(f.c3), short7(f.c3), dark)
		if red == 0 || red <= green {
			t.Fatalf("%s: commit hash hue red=%d green=%d, want dominantly red", theme, red, green)
		}
		red, green = textHueInRow("Comparison Local Changes", "Local Changes", dark)
		if green == 0 || green <= red {
			t.Fatalf("%s: Local Changes hue red=%d green=%d, want dominantly green", theme, red, green)
		}
	}
}

// The comparison works over a real SSH connection: choosing Compare to Local
// Changes on a loopback SSH repository lists the files the server's working
// tree changes relative to the commit — the untracked file among them — and
// shows their diffs, with every git command running on the server.
func TestHistoryCompareLocalLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)

	// The initial commit seeds a.txt; the working tree then modifies it and
	// gains an untracked file.
	root := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	srv.WriteFile(t, repo.Path, "a.txt", []byte("one\nworktree line\n"))
	srv.WriteFile(t, repo.Path, "untracked.txt", []byte("untracked line\n"))

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

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()

	enterCompareLocal(t, h, root, "initial commit")
	h.drain()

	hv := &h.app.historyView
	if hv.mode != historyCompareLocal {
		t.Fatalf("SSH mode = %d, want historyCompareLocal (%d)", hv.mode, historyCompareLocal)
	}
	if hv.compareLocal.err != nil {
		t.Fatalf("SSH compare-local file list error: %v", hv.compareLocal.err)
	}
	paths := compareFileLines(hv.compareLocal.files)
	if !slices.Contains(paths, "M\ta.txt") || !slices.Contains(paths, "?\tuntracked.txt") {
		t.Fatalf("SSH compare-local files = %q, want M a.txt and ? untracked.txt", paths)
	}

	// The untracked file's diff comes from the server as one addition.
	if err := h.ui.Click("File untracked.txt"); err != nil {
		t.Fatalf("click File untracked.txt: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if hv.diff.req.Kind != diffWorktreeFile {
		t.Fatalf("SSH diff kind = %v, want diffWorktreeFile", hv.diff.req.Kind)
	}
	if hv.diff.err != nil {
		t.Fatalf("SSH diff error: %v", hv.diff.err)
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "untracked line") {
		t.Fatalf("SSH diff insertions = %q, want untracked line", ins)
	}
	if del := diffAccessLines(h.ui, ui.RoleDeletion); len(del) != 0 {
		t.Fatalf("SSH diff deletions = %q, want none for an untracked file", del)
	}

	// The tracked file's diff shows the worktree edit.
	if err := h.ui.Click("File a.txt"); err != nil {
		t.Fatalf("click File a.txt: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "worktree line") {
		t.Fatalf("SSH tracked diff insertions = %q, want worktree line", ins)
	}
}
