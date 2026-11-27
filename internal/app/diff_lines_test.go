package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// diffAccessNode returns the accessibility node of one diff line by its role
// and exact text: an insertion, a deletion, or an unchanged line. In the
// split layout an unchanged line's node exists on both sides with the same
// name and role, and a deletion's context counterpart is on the new side;
// newSide picks between them when newOnly is set. Deletions in split view
// exist on the old side only, insertions on the new side only.
func diffAccessNode(t *testing.T, h *testHarness, role ui.Role, text string, newOnly bool) ui.AccessInfo {
	t.Helper()
	// The divider between the split view's sides marks where the new side
	// begins; in a unified view there is none, and every node is unique.
	dividerX := float32(-1)
	for _, a := range h.ui.AccessAll() {
		if a.Role == ui.RoleSplitter {
			dividerX = a.Bounds.X
		}
	}
	var found ui.AccessInfo
	for _, a := range h.ui.AccessAll() {
		if a.Role != role || a.Name != text {
			continue
		}
		if !newOnly || dividerX < 0 || a.Bounds.X > dividerX {
			return a
		}
		found = a
	}
	if !newOnly || found.Name == "" {
		t.Fatalf("no %v node %q in the diff; texts=%q", role, text, h.ui.Texts())
	}
	t.Fatalf("no new-side %v node %q in the diff; texts=%q", role, text, h.ui.Texts())
	return ui.AccessInfo{}
}

// hoverDiffLine moves the pointer over the text of one diff line so the
// change under it shows its action bar.
//
// The pointer goes to the new side (newOnly true) even for a deletion, and
// the tests hover after selecting lines rather than before. The action bar
// floats over the new side's text, and it is the pointer's presence there
// that keeps the change active while the press on the bar lands: a press
// with the pointer resting in the old side's gutter — where a click of a
// line number leaves it — finds no active change, the bar gone before the
// click registers. A real user's pointer crosses the new side's text on
// the way from the gutter to the bar, so the button answers; the tests
// move it there the same way. The empty room a deletion leaves on the new
// side is not a line, so for a deletion the pointer rests on the unchanged
// line at the deletion's place, which counts as the same change.
func hoverDiffLine(t *testing.T, h *testHarness, role ui.Role, text string, newOnly bool) {
	t.Helper()
	node := diffAccessNode(t, h, role, text, newOnly)
	x := node.Bounds.X + node.Bounds.W/2
	y := node.Bounds.Y + node.Bounds.H/2
	h.ui.Move(x, y)
	h.ui.Frame()
}

// clickDiffLineGutter clicks the line number of one diff line, selecting it;
// shift extends the selection from the line clicked before.
func clickDiffLineGutter(t *testing.T, h *testHarness, role ui.Role, text string, shift bool) {
	t.Helper()
	node := diffAccessNode(t, h, role, text, false)
	// The node spans the text right of the gutter, so a press a little left
	// of its edge lands on the line's number.
	x := node.Bounds.X - 3
	y := node.Bounds.Y + node.Bounds.H/2
	if shift {
		h.ui.ClickAtWith(ui.Shift, x, y)
	} else {
		h.ui.ClickAt(x, y)
	}
	h.ui.Frame()
}

// twoChangeFixture commits fourteen lines and rewrites the third and the
// twelfth, so the diff shows two changes apart.
func twoChangeFixture(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	base := []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve", "thirteen", "fourteen"}
	edited := append([]string(nil), base...)
	edited[2] = "THREE"
	edited[11] = "TWELVE"
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", linesText(base...), "initial")
	repo.WriteFile("a.txt", linesText(edited...))
	return repo
}

// The action bar of an unstaged file stages exactly the change under the
// pointer: the other change of the file stays unstaged.
func TestDiffLinesStageHunkSplitLayout(t *testing.T) {
	repo := twoChangeFixture(t, "dl-stage-hunk")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged a.txt")
	h.drain()

	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
	mustClick(t, h, "Stage Hunk")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage hunk action error: %v", err)
	}
	got := indexContent(t, repo, "a.txt")
	if !strings.Contains(got, "THREE\n") {
		t.Fatalf("index does not hold the staged change:\n%s", got)
	}
	if strings.Contains(got, "TWELVE") {
		t.Fatalf("index holds the change not staged:\n%s", got)
	}
	if !strings.Contains(got, "twelve\n") {
		t.Fatalf("index lost the line of the change not staged:\n%s", got)
	}
}

// With lines selected on the new side, the action bar acts on the selection
// instead of the whole change: only the additions selected reach the index.
func TestDiffLinesStageLinesSplitLayout(t *testing.T) {
	repo := newGitRepo(t, "dl-stage-lines")
	repo.CommitFile("a.txt", linesText("one", "two", "three", "four", "five", "six", "seven"), "initial")
	repo.WriteFile("a.txt", linesText("one", "two", "THREE", "four-NEW", "five", "six", "seven"))

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged a.txt")
	h.drain()

	clickDiffLineGutter(t, h, ui.RoleInsertion, "THREE", false)
	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Stage Lines"); !ok {
		t.Fatalf("no Stage Lines button with an overlapping selection; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Stage Lines")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage lines action error: %v", err)
	}
	got := indexContent(t, repo, "a.txt")
	// Only THREE was selected: the addition leaps ahead of the deletions it
	// answers to, which stay as lines of both sides.
	want := linesText("one", "two", "THREE", "three", "four", "five", "six", "seven")
	if got != want {
		t.Fatalf("index after staging one selected line =\n%q\nwant\n%q", got, want)
	}
	// The selection is gone with the diff reloaded: a fresh state holds no
	// selection, and a diff still loading has none to hold it.
	if st := h.app.changesView.diff.state; st != nil {
		if _, ok := st.SelectedLines(); ok {
			t.Fatal("line selection survived the reload after staging")
		}
	}
}

// In the unified layout the action bar and line selection work the same:
// here a deletion is selected on the old side and staged alone.
func TestDiffLinesStageLinesUnifiedLayout(t *testing.T) {
	repo := newGitRepo(t, "dl-stage-unified")
	repo.CommitFile("a.txt", linesText("one", "two", "three", "four", "five", "six", "seven"), "initial")
	repo.WriteFile("a.txt", linesText("one", "two", "THREE", "four-NEW", "five", "six", "seven"))

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged a.txt")
	h.drain()
	mustClick(t, h, "Unified")
	h.ui.Frame()

	// Select both deleted lines on the old side: the first with a click,
	// the second extending the selection with Shift.
	clickDiffLineGutter(t, h, ui.RoleDeletion, "three", false)
	clickDiffLineGutter(t, h, ui.RoleDeletion, "four", true)
	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
	mustClick(t, h, "Stage Lines")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage lines action error: %v", err)
	}
	got := indexContent(t, repo, "a.txt")
	// Both deletions staged, none of the additions: the index lost the two
	// lines and gained nothing.
	want := linesText("one", "two", "five", "six", "seven")
	if got != want {
		t.Fatalf("index after staging selected deletions =\n%q\nwant\n%q", got, want)
	}
}

// A staged file's action bar unstages: the change under the pointer leaves
// the index while the rest of the file stays staged.
func TestDiffLinesUnstageHunk(t *testing.T) {
	repo := twoChangeFixture(t, "dl-unstage-hunk")
	repo.Git("add", "--", "a.txt")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Staged a.txt")
	h.drain()

	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
	mustClick(t, h, "Unstage Hunk")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unstage hunk action error: %v", err)
	}
	got := indexContent(t, repo, "a.txt")
	if strings.Contains(got, "THREE") {
		t.Fatalf("index still holds the unstaged change:\n%s", got)
	}
	if !strings.Contains(got, "three\n") {
		t.Fatalf("index lost the restored line:\n%s", got)
	}
	if !strings.Contains(got, "TWELVE") {
		t.Fatalf("index lost the change that stays staged:\n%s", got)
	}
}

// With lines selected in a staged diff, only the selected lines unstage:
// an added line selected leaves the index while the one beside it stays,
// and a deleted line selected comes back.
func TestDiffLinesUnstageLines(t *testing.T) {
	repo := newGitRepo(t, "dl-unstage-lines")
	repo.CommitFile("a.txt", linesText("one", "two", "three", "four", "five", "six", "seven", "eight"), "initial")
	// The staged change adds TWO-A and TWO-B after two, and removes five.
	repo.WriteFile("a.txt", linesText("one", "two", "TWO-A", "TWO-B", "three", "four", "six", "seven", "eight"))
	repo.Git("add", "--", "a.txt")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Staged a.txt")
	h.drain()

	// Unstage one of the two added lines: TWO-A leaves the index, TWO-B
	// stays staged, and five is not restored.
	clickDiffLineGutter(t, h, ui.RoleInsertion, "TWO-A", false)
	hoverDiffLine(t, h, ui.RoleInsertion, "TWO-A", true)
	mustClick(t, h, "Unstage Lines")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unstage lines action error: %v", err)
	}
	got := indexContent(t, repo, "a.txt")
	want := linesText("one", "two", "TWO-B", "three", "four", "six", "seven", "eight")
	if got != want {
		t.Fatalf("index after unstaging one added line =\n%q\nwant\n%q", got, want)
	}

	// Unstage the deletion of five on the old side: the line comes back,
	// TWO-B stays staged. The deletion is a change of its own. The pointer
	// rests on six, the new side's line where five was: a press on the
	// action bar does not reach its button while the pointer is on the old
	// side of a split view, as hoverDiffLine says.
	repo.WriteFile("a.txt", linesText("one", "two", "TWO-A", "TWO-B", "three", "four", "six", "seven", "eight"))
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Staged a.txt")
	h.drain()
	clickDiffLineGutter(t, h, ui.RoleDeletion, "five", false)
	hoverDiffLine(t, h, ui.RoleText, "six", true)
	mustClick(t, h, "Unstage Lines")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unstage lines action error: %v", err)
	}
	got = indexContent(t, repo, "a.txt")
	want = linesText("one", "two", "TWO-B", "three", "four", "five", "six", "seven", "eight")
	if got != want {
		t.Fatalf("index after unstaging the deletion =\n%q\nwant\n%q", got, want)
	}
}

// No action bar appears where a partial patch cannot act: diffs of the
// History page, diffs ignoring whitespace, renames, and binary files; the
// whole-file buttons stay usable throughout. An untracked file does stage
// by lines since the line actions learned additions, but never discards:
// discarding it whole stays the file list's own button.
func TestDiffLinesIneligibleDiffsShowNoActionBar(t *testing.T) {
	repo := newGitRepo(t, "dl-ineligible")
	repo.CommitFile("a.txt", linesText("one", "two", "three", "four", "five"), "initial")
	repo.CommitFile("a.txt", linesText("one", "two", "THREE", "four", "five"), "edit")

	h := openChangesHarness(t, repo)

	// A diff of the History page never stages by lines.
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, strings.TrimSpace(repo.Git("rev-parse", "HEAD")), "edit")
	h.drain()
	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
	for _, label := range []string{"Stage Hunk", "Stage Lines", "Unstage Hunk", "Unstage Lines", "Discard Hunk", "Discard Lines"} {
		if _, ok := h.ui.AccessByRole(ui.RoleButton, label); ok {
			t.Fatalf("history diff shows a %s button", label)
		}
	}

	// Back in Local Changes with a whitespace-ignoring diff: the lines the
	// panel shows are not a patch git can apply. The file gains a real
	// change beside a whitespace-only one, so ignoring whitespace leaves a
	// change to hover.
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes nav row: %v", err)
	}
	h.ui.Frame()
	repo.WriteFile("a.txt", linesText("one", "TWO", "THREE", "four", "five  ", "six"))
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Unstaged a.txt")
	h.drain()
	// Sanity: without ignoring whitespace the action bar shows.
	hoverDiffLine(t, h, ui.RoleInsertion, "TWO", true)
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Stage Hunk"); !ok {
		t.Fatalf("no Stage Hunk on a plain modification; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Ignore Whitespace")
	h.drain()
	hoverDiffLine(t, h, ui.RoleInsertion, "TWO", true)
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Stage Hunk"); ok {
		t.Fatal("ignore-whitespace diff shows a Stage Hunk button")
	}
	// The whole-file Stage button is unaffected.
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Stage a.txt"); !ok {
		t.Fatal("whole-file Stage button missing on an ignore-whitespace diff")
	}
	mustClick(t, h, "Ignore Whitespace")
	h.drain()

	// Untracked files stage by lines now: the bar shows the Stage buttons
	// but never a Discard one.
	repo.WriteFile("new.txt", "brand\nnew\n")
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Unstaged new.txt")
	h.drain()
	hoverDiffLine(t, h, ui.RoleInsertion, "brand", true)
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Stage Hunk"); !ok {
		t.Fatalf("untracked file shows no Stage Hunk button; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Discard Hunk"); ok {
		t.Fatal("untracked file shows a Discard Hunk button")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Discard Lines"); ok {
		t.Fatal("untracked file shows a Discard Lines button")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Stage new.txt"); !ok {
		t.Fatal("whole-file Stage button missing for an untracked file")
	}

	// Renames: no action bar on either side of the rename.
	repo.Git("mv", "a.txt", "renamed.txt")
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Staged renamed.txt")
	h.drain()
	if h.ui.HasText("File renamed") {
		// A pure rename shows no lines at all; the absence of the bar is
		// what matters, and there is nothing to hover.
	} else {
		hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Unstage Hunk"); ok {
		t.Fatal("renamed file shows an Unstage Hunk button")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Unstage renamed.txt"); !ok {
		t.Fatal("whole-file Unstage button missing for a rename")
	}

	// Binary files: the diff is a placeholder, and no action bar.
	bin := filepath.Join(repo.Path(), "logo.bin")
	if err := os.WriteFile(bin, []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	repo.Git("add", "--", "logo.bin")
	repo.Git("commit", "-m", "add binary")
	if err := os.WriteFile(bin, []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Unstaged logo.bin")
	h.drain()
	if !h.ui.HasText("Binary file") {
		t.Fatalf("binary diff did not show its placeholder; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Stage Hunk"); ok {
		t.Fatal("binary file shows a Stage Hunk button")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Stage logo.bin"); !ok {
		t.Fatal("whole-file Stage button missing for a binary file")
	}
}

// A file edited after its diff showed refuses the write: the error shows,
// the index keeps its content, and the diff reloads to what is there.
func TestDiffLinesStaleDiffRefusesAndReloads(t *testing.T) {
	repo := newGitRepo(t, "dl-stale")
	repo.CommitFile("a.txt", linesText("one", "two", "three", "four", "five"), "initial")
	repo.WriteFile("a.txt", linesText("one", "two", "THREE", "four", "five"))

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged a.txt")
	h.drain()
	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)

	// The file moves after the diff was read.
	repo.WriteFile("a.txt", linesText("one", "two", "THREE-AGAIN", "four", "five"))
	mustClick(t, h, "Stage Hunk")
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil || !strings.Contains(err.Error(), "The diff changed after it was shown") {
		t.Fatalf("stale diff error = %v, want the diff-changed refusal", err)
	}
	if !h.ui.HasText("The diff changed after it was shown") {
		t.Fatalf("refusal not visible in the page; texts=%q", h.ui.Texts())
	}
	if got := indexContent(t, repo, "a.txt"); got != linesText("one", "two", "three", "four", "five") {
		t.Fatalf("index changed despite the refusal:\n%s", got)
	}
	// The diff reloaded to what the file holds now.
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !containsString(ins, "THREE-AGAIN") {
		t.Fatalf("reloaded diff does not show the new content: %q", ins)
	}
}

// While a write runs, the action bar's button is disabled: a second click
// does not start another write.
func TestDiffLinesBusyDisablesHunkButton(t *testing.T) {
	repo := twoChangeFixture(t, "dl-busy")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged a.txt")
	h.drain()
	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)

	h.queue.PauseWorkers()
	mustClick(t, h, "Stage Hunk")
	worker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		h.queue.ReleaseWorker(worker)
		h.drain()
		t.Fatal("session did not enter busy state for the hunk write")
	}
	hoverDiffLine(t, h, ui.RoleInsertion, "TWELVE", true)
	info, ok := h.ui.AccessByRole(ui.RoleButton, "Stage Hunk")
	if !ok {
		t.Fatalf("action bar missing while busy; texts=%q", h.ui.Texts())
	}
	if !info.Disabled {
		t.Fatalf("Stage Hunk not disabled while busy: %+v", info)
	}
	// A click on the disabled button starts nothing.
	if err := h.ui.Click("Stage Hunk"); err == nil {
		h.ui.Frame()
	}
	h.queue.ReleaseWorker(worker)
	h.queue.ResumeWorkers()
	h.drain()

	got := indexContent(t, repo, "a.txt")
	if !strings.Contains(got, "THREE\n") {
		t.Fatalf("first write did not stage its hunk:\n%s", got)
	}
	if strings.Contains(got, "TWELVE") {
		t.Fatalf("the disabled click staged the second hunk too:\n%s", got)
	}
}

// Loopback SSH: staging selected lines and unstaging a hunk work through
// the same real UI against a repository reached over sshd.
func TestDiffLinesLoopbackSSHStageLinesAndUnstageHunk(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH initial load failed: %v", err)
	}

	srv.WriteFile(t, repo.Path, "a.txt", []byte("one\ntwo\nthree\nfour\nfive\nsix\nseven\n"))
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Stage a.txt")
	h.drain()
	srv.WriteFile(t, repo.Path, "a.txt", []byte("one\ntwo\nTHREE\nfour-NEW\nfive\nsix\nseven\n"))
	h.app.session.Reload()
	h.drain()

	// Stage one selected line over SSH.
	mustClick(t, h, "Unstaged a.txt")
	h.drain()
	clickDiffLineGutter(t, h, ui.RoleInsertion, "THREE", false)
	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
	mustClick(t, h, "Stage Lines")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH stage lines action error: %v", err)
	}
	// RunGit trims the trailing newline of what it reads back.
	staged := srv.RunGit(t, repo.Path, "show", ":a.txt")
	if staged != "one\ntwo\nTHREE\nthree\nfour\nfive\nsix\nseven" {
		t.Fatalf("SSH index after staging lines =\n%q", staged)
	}

	// Unstage the hunk of the other change over SSH.
	srv.WriteFile(t, repo.Path, "a.txt", []byte("one\ntwo\nTHREE\nfour-NEW\nfive\nsix\nseven\n"))
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Staged a.txt")
	h.drain()
	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
	mustClick(t, h, "Unstage Hunk")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH unstage hunk action error: %v", err)
	}
	staged = srv.RunGit(t, repo.Path, "show", ":a.txt")
	if strings.Contains(staged, "THREE") {
		t.Fatalf("SSH index still holds the unstaged hunk:\n%q", staged)
	}
}

// An untracked file's action bar stages exactly the lines selected of it:
// the index gains a new file holding those lines alone, and the rest of
// the file stays untracked work.
func TestDiffLinesStageUntrackedFileLines(t *testing.T) {
	repo := newGitRepo(t, "dl-untracked")
	repo.CommitFile("base.txt", "base\n", "initial")
	repo.WriteFile("new.txt", linesText("alpha", "beta", "gamma", "delta"))

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged new.txt")
	h.drain()

	clickDiffLineGutter(t, h, ui.RoleInsertion, "alpha", false)
	clickDiffLineGutter(t, h, ui.RoleInsertion, "beta", true)
	hoverDiffLine(t, h, ui.RoleInsertion, "alpha", true)
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Stage Lines"); !ok {
		t.Fatalf("no Stage Lines button on an untracked file's selection; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Discard Hunk"); ok {
		t.Fatal("untracked file shows a Discard button")
	}
	mustClick(t, h, "Stage Lines")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage lines action error: %v", err)
	}
	if got := indexContent(t, repo, "new.txt"); got != linesText("alpha", "beta") {
		t.Fatalf("index after staging two lines of an untracked file =\n%q", got)
	}
	if got := repo.Git("status", "--porcelain", "--", "new.txt"); got != "AM new.txt\n" {
		t.Fatalf("status after staging two lines = %q, want added-and-modified", got)
	}
}

// The action bar of an untracked file with no selection stages the whole
// change: the index gains the file whole.
func TestDiffLinesStageUntrackedFileHunk(t *testing.T) {
	repo := newGitRepo(t, "dl-untracked-hunk")
	repo.CommitFile("base.txt", "base\n", "initial")
	repo.WriteFile("new.txt", linesText("alpha", "beta"))

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged new.txt")
	h.drain()

	hoverDiffLine(t, h, ui.RoleInsertion, "alpha", true)
	mustClick(t, h, "Stage Hunk")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage hunk action error: %v", err)
	}
	if got := indexContent(t, repo, "new.txt"); got != linesText("alpha", "beta") {
		t.Fatalf("index after staging the hunk =\n%q", got)
	}
	if got := repo.Git("status", "--porcelain", "--", "new.txt"); got != "A  new.txt\n" {
		t.Fatalf("status after staging the hunk = %q, want fully added", got)
	}
}

// A staged addition's action bar unstages the lines selected: they leave
// the index while the rest of the addition stays staged.
func TestDiffLinesUnstageAddedFileLines(t *testing.T) {
	repo := newGitRepo(t, "dl-added")
	repo.CommitFile("base.txt", "base\n", "initial")
	repo.WriteFile("new.txt", linesText("alpha", "beta", "gamma", "delta"))
	repo.Git("add", "--", "new.txt")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Staged new.txt")
	h.drain()

	clickDiffLineGutter(t, h, ui.RoleInsertion, "gamma", false)
	clickDiffLineGutter(t, h, ui.RoleInsertion, "delta", true)
	hoverDiffLine(t, h, ui.RoleInsertion, "gamma", true)
	mustClick(t, h, "Unstage Lines")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unstage lines action error: %v", err)
	}
	if got := indexContent(t, repo, "new.txt"); got != linesText("alpha", "beta") {
		t.Fatalf("index after unstaging two lines of an addition =\n%q", got)
	}
	if got := readWorktreeFile(t, repo, "new.txt"); got != linesText("alpha", "beta", "gamma", "delta") {
		t.Fatalf("unstaging touched the working tree file: %q", got)
	}
}

// Unstaging every line of a staged addition through the hunk button takes
// the file out of the index: it is untracked again.
func TestDiffLinesUnstageAddedFileHunk(t *testing.T) {
	repo := newGitRepo(t, "dl-added-hunk")
	repo.CommitFile("base.txt", "base\n", "initial")
	repo.WriteFile("new.txt", linesText("alpha", "beta"))
	repo.Git("add", "--", "new.txt")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Staged new.txt")
	h.drain()

	hoverDiffLine(t, h, ui.RoleInsertion, "alpha", true)
	mustClick(t, h, "Unstage Hunk")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unstage hunk action error: %v", err)
	}
	if out, err := tryGitCommand(repo.Path(), "ls-files", "--error-unmatch", "--", "new.txt"); err == nil {
		t.Fatalf("index still holds the unstaged addition: %s", out)
	}
	if got := repo.Git("status", "--porcelain", "--", "new.txt"); got != "?? new.txt\n" {
		t.Fatalf("status after unstaging the addition = %q, want untracked", got)
	}
}

// A deleted file's action bar stages the deletion: the hunk button with no
// selection takes the file out of the index whole, and no Discard button
// shows for it.
func TestDiffLinesStageDeletedFileHunk(t *testing.T) {
	repo := newGitRepo(t, "dl-deleted")
	repo.CommitFile("gone.txt", linesText("one", "two", "three"), "initial")
	if err := os.Remove(filepath.Join(repo.Path(), "gone.txt")); err != nil {
		t.Fatal(err)
	}

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged gone.txt")
	h.drain()
	mustClick(t, h, "Unified")
	h.ui.Frame()

	hoverDiffLine(t, h, ui.RoleDeletion, "one", false)
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Discard Hunk"); ok {
		t.Fatal("deleted file shows a Discard button")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Discard Lines"); ok {
		t.Fatal("deleted file shows a Discard Lines button")
	}
	mustClick(t, h, "Stage Hunk")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage hunk action error: %v", err)
	}
	if out, err := tryGitCommand(repo.Path(), "ls-files", "--error-unmatch", "--", "gone.txt"); err == nil {
		t.Fatalf("index still holds the staged deletion: %s", out)
	}
	if got := repo.Git("status", "--porcelain", "--", "gone.txt"); got != "D  gone.txt\n" {
		t.Fatalf("status after staging the deletion = %q, want fully staged", got)
	}
}

// With lines of a deleted file selected, the action bar stages exactly
// them: the index copy keeps the lines not chosen.
func TestDiffLinesStageDeletedFileLines(t *testing.T) {
	repo := newGitRepo(t, "dl-deleted-lines")
	repo.CommitFile("gone.txt", linesText("one", "two", "three", "four", "five"), "initial")
	if err := os.Remove(filepath.Join(repo.Path(), "gone.txt")); err != nil {
		t.Fatal(err)
	}

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged gone.txt")
	h.drain()
	mustClick(t, h, "Unified")
	h.ui.Frame()

	clickDiffLineGutter(t, h, ui.RoleDeletion, "two", false)
	clickDiffLineGutter(t, h, ui.RoleDeletion, "three", true)
	hoverDiffLine(t, h, ui.RoleDeletion, "two", false)
	mustClick(t, h, "Stage Lines")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage lines action error: %v", err)
	}
	if got := indexContent(t, repo, "gone.txt"); got != linesText("one", "four", "five") {
		t.Fatalf("index after staging two lines of a deletion =\n%q", got)
	}
	if got := repo.Git("status", "--porcelain", "--", "gone.txt"); got != "MD gone.txt\n" {
		t.Fatalf("status after staging two deletions = %q, want modified-and-deleted", got)
	}
}

// A staged deletion's action bar unstages the lines selected: exactly
// those lines return to the index.
func TestDiffLinesUnstageStagedDeletionLines(t *testing.T) {
	repo := newGitRepo(t, "dl-undel")
	repo.CommitFile("gone.txt", linesText("one", "two", "three", "four", "five"), "initial")
	if err := os.Remove(filepath.Join(repo.Path(), "gone.txt")); err != nil {
		t.Fatal(err)
	}
	repo.Git("add", "--", "gone.txt")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Staged gone.txt")
	h.drain()
	mustClick(t, h, "Unified")
	h.ui.Frame()

	clickDiffLineGutter(t, h, ui.RoleDeletion, "two", false)
	clickDiffLineGutter(t, h, ui.RoleDeletion, "three", true)
	hoverDiffLine(t, h, ui.RoleDeletion, "two", false)
	mustClick(t, h, "Unstage Lines")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unstage lines action error: %v", err)
	}
	if got := indexContent(t, repo, "gone.txt"); got != linesText("two", "three") {
		t.Fatalf("index after unstaging two lines of a deletion =\n%q", got)
	}
}

// openLoggedChangesHarness is openChangesHarness with a command log
// recording every git command the app runs, so a test can prove a path
// that must not write never reaches git apply.
func openLoggedChangesHarness(t *testing.T, f *gitRepoFixture) (*testHarness, *git.CommandLog) {
	t.Helper()
	cmdLog := git.NewCommandLog()
	h := newTestApp(t)
	h.app.openRepoFn = func(meta store.Repo) *git.Repo {
		return defaultOpenGitRepo(meta).WithCommandLog(cmdLog)
	}
	h.openRepo(f.StoreRepo())
	h.drain()
	if h.app.route.page != pageChanges {
		t.Fatalf("initial page = %v, want pageChanges", h.app.route.page)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("initial snapshot load failed: %v", err)
	}
	return h, cmdLog
}

// countGitApplies is how many git apply commands the log holds.
func countGitApplies(l *git.CommandLog) int {
	records, _ := l.Snapshot()
	n := 0
	for _, r := range records {
		if len(r.Args) > 0 && r.Args[0] == "apply" {
			n++
		}
	}
	return n
}

// The Discard Hunk button asks before it writes: the confirmation names
// the file and says the other changes stay; confirming it reverses exactly
// the hunk in the working tree while the index never moves.
func TestDiffLinesDiscardHunkConfirm(t *testing.T) {
	repo := twoChangeFixture(t, "dl-discard-hunk")

	h, cmdLog := openLoggedChangesHarness(t, repo)
	mustClick(t, h, "Unstaged a.txt")
	h.drain()

	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
	mustClick(t, h, "Discard Hunk")
	h.ui.Frame()
	if !h.ui.HasText("Discard this hunk of a.txt?") {
		t.Fatalf("discard hunk confirmation did not open; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("The other changes in the file stay.") {
		t.Fatalf("discard hunk confirmation body missing; texts=%q", h.ui.Texts())
	}
	if countGitApplies(cmdLog) != 0 {
		t.Fatal("opening the confirmation already ran git apply")
	}

	before := indexContent(t, repo, "a.txt")
	mustClick(t, h, "Confirm Discard")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("discard hunk action error: %v", err)
	}
	got := readWorktreeFile(t, repo, "a.txt")
	if strings.Contains(got, "THREE") || !strings.Contains(got, "three\n") {
		t.Fatalf("worktree after discarding the hunk =\n%q", got)
	}
	if !strings.Contains(got, "TWELVE") {
		t.Fatalf("discarding the hunk took the other change with it:\n%q", got)
	}
	if idx := indexContent(t, repo, "a.txt"); idx != before {
		t.Fatalf("discard moved the index:\nbefore\n%q\nafter\n%q", before, idx)
	}
}

// With lines selected, the Discard Lines button asks its own question and
// confirming it discards exactly the selected lines.
func TestDiffLinesDiscardLinesConfirm(t *testing.T) {
	repo := newGitRepo(t, "dl-discard-lines")
	repo.CommitFile("a.txt", linesText("one", "two", "three", "four", "five", "six", "seven"), "initial")
	repo.WriteFile("a.txt", linesText("one", "two", "THREE", "four-NEW", "five", "six", "seven"))

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged a.txt")
	h.drain()

	clickDiffLineGutter(t, h, ui.RoleInsertion, "THREE", false)
	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Discard Lines"); !ok {
		t.Fatalf("no Discard Lines button with an overlapping selection; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Discard Lines")
	h.ui.Frame()
	if !h.ui.HasText("Do you want to discard the changes in the selected lines?") {
		t.Fatalf("discard lines confirmation did not open; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("You can't undo this action.") {
		t.Fatalf("discard lines confirmation body missing; texts=%q", h.ui.Texts())
	}

	before := indexContent(t, repo, "a.txt")
	mustClick(t, h, "Confirm Discard")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("discard lines action error: %v", err)
	}
	want := linesText("one", "two", "four-NEW", "five", "six", "seven")
	if got := readWorktreeFile(t, repo, "a.txt"); got != want {
		t.Fatalf("worktree after discarding one addition =\n%q\nwant\n%q", got, want)
	}
	if idx := indexContent(t, repo, "a.txt"); idx != before {
		t.Fatalf("discard moved the index:\nbefore\n%q\nafter\n%q", before, idx)
	}
}

// Every way of canceling the discard confirmation — Cancel, Escape, the
// backdrop, and switching the selected file — leaves git untouched: no
// apply command runs, and the working tree keeps every byte.
func TestDiffLinesDiscardCancelPaths(t *testing.T) {
	repo := twoChangeFixture(t, "dl-discard-cancel")

	h, cmdLog := openLoggedChangesHarness(t, repo)
	mustClick(t, h, "Unstaged a.txt")
	h.drain()

	// The cancel paths compare against the file as it was when the question
	// opened, captured fresh each time since the test itself moves it.
	var wantTree, wantIndex string
	openDiscard := func() {
		t.Helper()
		wantTree = readWorktreeFile(t, repo, "a.txt")
		wantIndex = indexContent(t, repo, "a.txt")
		hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
		mustClick(t, h, "Discard Hunk")
		h.ui.Frame()
		if !h.ui.HasText("Discard this hunk of a.txt?") {
			t.Fatalf("discard confirmation did not open; texts=%q", h.ui.Texts())
		}
		if countGitApplies(cmdLog) != 0 {
			t.Fatal("opening the confirmation already ran git apply")
		}
	}
	assertUntouched := func(what string) {
		t.Helper()
		if countGitApplies(cmdLog) != 0 {
			t.Fatalf("%s ran git apply", what)
		}
		if got := readWorktreeFile(t, repo, "a.txt"); got != wantTree {
			t.Fatalf("%s changed the working tree:\n%q", what, got)
		}
		if got := indexContent(t, repo, "a.txt"); got != wantIndex {
			t.Fatalf("%s changed the index:\n%q", what, got)
		}
		if h.ui.HasText("Discard this hunk of a.txt?") {
			t.Fatalf("%s left the confirmation open; texts=%q", what, h.ui.Texts())
		}
	}

	// Cancel.
	openDiscard()
	mustClick(t, h, "Cancel")
	h.ui.Frame()
	h.drain()
	assertUntouched("Cancel")

	// Escape.
	openDiscard()
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	h.drain()
	assertUntouched("Escape")

	// Clicking the dimmed backdrop.
	openDiscard()
	h.ui.ClickAt(20, 20)
	h.ui.Frame()
	h.drain()
	assertUntouched("backdrop click")

	// The selection moving off the file answers the question with cancel.
	// A modal takes every click outside it, so the selection cannot move by
	// a click while the question is open: what moves it is the file leaving
	// the list, here restored by git behind the app's back.
	openDiscard()
	repo.Git("checkout", "--", "a.txt")
	h.app.session.Reload()
	h.drain()
	// The checkout itself rewrote the file, so only the log and the dialog
	// are checked here: the app answered with cancel and never applied.
	if countGitApplies(cmdLog) != 0 {
		t.Fatal("the file leaving the selection ran git apply")
	}
	if h.ui.HasText("Discard this hunk of a.txt?") {
		t.Fatalf("the file leaving the selection left the confirmation open; texts=%q", h.ui.Texts())
	}
	// Restore the changes for the confirmed-discard probe below.
	repo.WriteFile("a.txt", linesText("one", "two", "THREE", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "TWELVE", "thirteen", "fourteen"))
	h.app.session.Reload()
	h.drain()

	// A confirmed discard proves the log watches the real write path.
	mustClick(t, h, "Unstaged a.txt")
	h.drain()
	openDiscard()
	mustClick(t, h, "Confirm Discard")
	h.drain()
	if countGitApplies(cmdLog) != 1 {
		t.Fatalf("confirmed discard ran %d applies, want 1", countGitApplies(cmdLog))
	}
}

// A file edited while the confirmation is open is never written: the
// answer is refused with the diff-changed message and the working tree
// keeps the edit.
func TestDiffLinesDiscardStaleRefused(t *testing.T) {
	repo := newGitRepo(t, "dl-discard-stale")
	repo.CommitFile("a.txt", linesText("one", "two", "three", "four", "five"), "initial")
	repo.WriteFile("a.txt", linesText("one", "two", "THREE", "four", "five"))

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged a.txt")
	h.drain()
	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
	mustClick(t, h, "Discard Hunk")
	h.ui.Frame()
	if !h.ui.HasText("Discard this hunk of a.txt?") {
		t.Fatalf("discard confirmation did not open; texts=%q", h.ui.Texts())
	}

	// The file moves after the question was asked.
	repo.WriteFile("a.txt", linesText("one", "two", "THREE-AGAIN", "four", "five"))
	mustClick(t, h, "Confirm Discard")
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil || !strings.Contains(err.Error(), "The diff changed after it was shown") {
		t.Fatalf("stale discard error = %v, want the diff-changed refusal", err)
	}
	want := linesText("one", "two", "THREE-AGAIN", "four", "five")
	if got := readWorktreeFile(t, repo, "a.txt"); got != want {
		t.Fatalf("refused discard touched the working tree:\n%q", got)
	}
	if got := indexContent(t, repo, "a.txt"); got != linesText("one", "two", "three", "four", "five") {
		t.Fatalf("refused discard touched the index:\n%q", got)
	}
}

// A symlink and a conflicted file show no action bar: their changes are
// one indivisible line or no plain diff at all.
func TestDiffLinesIneligibleSymlinkAndConflict(t *testing.T) {
	repo := newGitRepo(t, "dl-ineligible-link")
	repo.CommitFile("base.txt", "base\n", "initial")
	link := filepath.Join(repo.Path(), "link1")
	if err := os.Symlink("base.txt", link); err != nil {
		t.Fatal(err)
	}
	repo.Git("add", "--", "link1")
	repo.Git("commit", "-m", "add symlink")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other-target", link); err != nil {
		t.Fatal(err)
	}

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged link1")
	h.drain()
	hoverDiffLine(t, h, ui.RoleInsertion, "other-target", true)
	for _, label := range []string{"Stage Hunk", "Stage Lines", "Discard Hunk", "Discard Lines"} {
		if _, ok := h.ui.AccessByRole(ui.RoleButton, label); ok {
			t.Fatalf("symlink target change shows a %s button", label)
		}
	}
	// The whole-file Stage button is unaffected.
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Stage link1"); !ok {
		t.Fatal("whole-file Stage button missing for a symlink")
	}

	// A conflicted file: no action bar either.
	repo2 := newGitRepo(t, "dl-ineligible-conflict")
	mergeConflictFixture(t, repo2)
	if out, err := tryGitCommand(repo2.Path(), "merge", "feature"); err == nil {
		t.Fatalf("fixture: merge did not conflict: %s", out)
	}
	h2 := openChangesHarness(t, repo2)
	if err := h2.ui.Click("Conflicted f.txt"); err != nil {
		t.Fatalf("click conflicted file: %v\ntexts=%q", err, h2.ui.Texts())
	}
	h2.drain()
	for _, label := range []string{"Stage Hunk", "Stage Lines", "Discard Hunk", "Discard Lines", "Unstage Hunk"} {
		if _, ok := h2.ui.AccessByRole(ui.RoleButton, label); ok {
			t.Fatalf("conflicted file shows a %s button", label)
		}
	}
}

// Drawing the diff with the action bars wired runs no git commands: the
// per-frame work only compares ranges, the patch text building when a
// button is clicked.
func TestDiffLinesFramesRunNoGitCommands(t *testing.T) {
	repo := twoChangeFixture(t, "dl-frames")

	h, cmdLog := openLoggedChangesHarness(t, repo)
	mustClick(t, h, "Unstaged a.txt")
	h.drain()
	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
	h.ui.Frame()

	before, _ := cmdLog.Snapshot()
	for i := 0; i < 10; i++ {
		h.ui.Frame()
	}
	after, _ := cmdLog.Snapshot()
	if len(after) != len(before) {
		t.Fatalf("frames ran git commands: before %d after %d records", len(before), len(after))
	}
}

// Loopback SSH: discarding selected lines and staging lines of an
// untracked file work through the same real UI against a repository
// reached over sshd.
func TestDiffLinesLoopbackSSHDiscardAndUntrackedStage(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH initial load failed: %v", err)
	}

	srv.WriteFile(t, repo.Path, "a.txt", []byte("one\ntwo\nthree\nfour\nfive\nsix\nseven\n"))
	srv.RunGit(t, repo.Path, "add", "--", "a.txt")
	srv.RunGit(t, repo.Path, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "lines")
	srv.WriteFile(t, repo.Path, "a.txt", []byte("one\ntwo\nTHREE\nfour-NEW\nfive\nsix\nseven\n"))
	h.app.session.Reload()
	h.drain()

	// Discard one selected line over SSH.
	mustClick(t, h, "Unstaged a.txt")
	h.drain()
	clickDiffLineGutter(t, h, ui.RoleInsertion, "THREE", false)
	hoverDiffLine(t, h, ui.RoleInsertion, "THREE", true)
	mustClick(t, h, "Discard Lines")
	h.ui.Frame()
	mustClick(t, h, "Confirm Discard")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH discard lines action error: %v", err)
	}
	worktree := string(srv.ReadFile(t, repo.Path, "a.txt"))
	if strings.Contains(worktree, "THREE") || !strings.Contains(worktree, "four-NEW") {
		t.Fatalf("SSH worktree after discarding the line =\n%q", worktree)
	}
	staged := srv.RunGit(t, repo.Path, "show", ":a.txt")
	if staged != "one\ntwo\nthree\nfour\nfive\nsix\nseven" {
		t.Fatalf("SSH discard moved the index:\n%q", staged)
	}

	// Stage two lines of an untracked file over SSH.
	srv.WriteFile(t, repo.Path, "new.txt", []byte("alpha\nbeta\ngamma\n"))
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Unstaged new.txt")
	h.drain()
	clickDiffLineGutter(t, h, ui.RoleInsertion, "alpha", false)
	clickDiffLineGutter(t, h, ui.RoleInsertion, "beta", true)
	hoverDiffLine(t, h, ui.RoleInsertion, "alpha", true)
	mustClick(t, h, "Stage Lines")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH stage lines of untracked file action error: %v", err)
	}
	staged = srv.RunGit(t, repo.Path, "show", ":new.txt")
	if staged != "alpha\nbeta" {
		t.Fatalf("SSH index after staging two lines of an untracked file =\n%q", staged)
	}
}
