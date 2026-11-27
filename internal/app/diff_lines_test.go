package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
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
// History page, diffs ignoring whitespace, untracked files, renames, and
// binary files; the whole-file buttons stay usable throughout.
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
	for _, label := range []string{"Stage Hunk", "Stage Lines", "Unstage Hunk", "Unstage Lines"} {
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

	// Untracked files: no action bar.
	repo.WriteFile("new.txt", "brand\nnew\n")
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Unstaged new.txt")
	h.drain()
	hoverDiffLine(t, h, ui.RoleInsertion, "brand", true)
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Stage Hunk"); ok {
		t.Fatal("untracked file shows a Stage Hunk button")
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
