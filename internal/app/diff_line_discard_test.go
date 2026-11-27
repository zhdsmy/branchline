package app

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui/editor"

	"github.com/zhdsmy/branchline/internal/git"
)

// discardLinesChosen builds the reverse patch of the chosen lines and
// applies it to the working tree backwards, as discarding lines of an
// unstaged diff does. The index is never part of a discard.
func discardLinesChosen(t *testing.T, r *git.Repo, f editor.FilePatch, chosen [][]bool) string {
	t.Helper()
	text, err := buildLinesPatch(f, chosen, true)
	if err != nil {
		t.Fatalf("build discard patch: %v", err)
	}
	if err := r.ApplyLines(text, false, true); err != nil {
		t.Fatalf("apply discard patch: %v\n%s", err, text)
	}
	return text
}

// Discarding a whole run of a modification restores exactly those lines:
// the index never moves, and the other change of the file stays unstaged.
func TestDiffLinePatchDiscardWholeRun(t *testing.T) {
	repo := twoChangeFixture(t, "ld-whole")
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 3)
	before := indexContent(t, repo, "a.txt")
	discardLinesChosen(t, r, fp, chooseLines(t, fp, "-three", "+THREE"))

	if got := readWorktreeFile(t, repo, "a.txt"); !strings.Contains(got, "three\n") || strings.Contains(got, "THREE") {
		t.Fatalf("worktree after discarding the run =\n%q", got)
	}
	if !strings.Contains(readWorktreeFile(t, repo, "a.txt"), "TWELVE") {
		t.Fatal("discarding one run took the other change with it")
	}
	if got := indexContent(t, repo, "a.txt"); got != before {
		t.Fatalf("discard moved the index:\nbefore\n%q\nafter\n%q", before, got)
	}
	// What remains of the working tree diff is exactly the other change.
	raw, err := r.FileDiffOpts(git.FileChange{Path: "a.txt", Status: 'M'}, git.DiffOptions{Context: 3})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "THREE") || !strings.Contains(raw, "+TWELVE") {
		t.Fatalf("remaining unstaged diff =\n%s", raw)
	}
}

// Discarding one added line of a replacement run removes only it: the
// deletion it answered to stays undone, and the line it replaced does not
// come back.
func TestDiffLinePatchDiscardSinglePlusLine(t *testing.T) {
	repo := newGitRepo(t, "ld-plus")
	repo.CommitFile("a.txt", linesText("one", "two", "three", "four", "five", "six", "seven"), "initial")
	repo.WriteFile("a.txt", linesText("one", "two", "THREE", "four-NEW", "five", "six", "seven"))
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 3)
	before := indexContent(t, repo, "a.txt")
	discardLinesChosen(t, r, fp, chooseLines(t, fp, "+THREE"))

	want := linesText("one", "two", "four-NEW", "five", "six", "seven")
	if got := readWorktreeFile(t, repo, "a.txt"); got != want {
		t.Fatalf("worktree after discarding one addition =\n%q\nwant\n%q", got, want)
	}
	if got := indexContent(t, repo, "a.txt"); got != before {
		t.Fatalf("discard moved the index:\nbefore\n%q\nafter\n%q", before, got)
	}
}

// Discarding one deleted line of a replacement run puts only it back: the
// addition it became stays in the working tree beside it.
func TestDiffLinePatchDiscardSingleMinusLine(t *testing.T) {
	repo := newGitRepo(t, "ld-minus")
	repo.CommitFile("a.txt", linesText("one", "two", "three", "four", "five", "six", "seven"), "initial")
	repo.WriteFile("a.txt", linesText("one", "two", "THREE", "four-NEW", "five", "six", "seven"))
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 3)
	before := indexContent(t, repo, "a.txt")
	discardLinesChosen(t, r, fp, chooseLines(t, fp, "-three"))

	want := linesText("one", "two", "three", "THREE", "four-NEW", "five", "six", "seven")
	if got := readWorktreeFile(t, repo, "a.txt"); got != want {
		t.Fatalf("worktree after discarding one deletion =\n%q\nwant\n%q", got, want)
	}
	if got := indexContent(t, repo, "a.txt"); got != before {
		t.Fatalf("discard moved the index:\nbefore\n%q\nafter\n%q", before, got)
	}
}

// Discarding the lines of the second of two @@ hunks restores them alone:
// the first hunk's changes stay exactly as the working tree had them.
func TestDiffLinePatchDiscardSecondOfTwoHunks(t *testing.T) {
	repo := twoHunkFixture(t, "ld-second")
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 3)
	if len(fp.Hunks) != 2 {
		t.Fatalf("hunks = %d, want 2", len(fp.Hunks))
	}
	before := indexContent(t, repo, "a.txt")
	discardLinesChosen(t, r, fp, chooseLines(t, fp, "-lineo", "+lineO-NEW"))

	got := readWorktreeFile(t, repo, "a.txt")
	if !strings.Contains(got, "lineo\n") || strings.Contains(got, "lineO-NEW") {
		t.Fatalf("worktree after discarding the second hunk =\n%q", got)
	}
	if !strings.Contains(got, "lineC1") || !strings.Contains(got, "lineC2") {
		t.Fatalf("discarding the second hunk took the first with it:\n%q", got)
	}
	if got := indexContent(t, repo, "a.txt"); got != before {
		t.Fatalf("discard moved the index:\nbefore\n%q\nafter\n%q", before, got)
	}
}
