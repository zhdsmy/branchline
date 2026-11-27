package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui/editor"

	"github.com/zhdsmy/branchline/internal/git"
)

// linePatchRepo builds a *git.Repo for a fixture's directory, as the app
// opens a local repository.
func linePatchRepo(repo *gitRepoFixture) *git.Repo {
	return git.New(&git.LocalExecutor{Dir: repo.Path()})
}

// parseOnePatch reads the single file a raw diff changes.
func parseOnePatch(t *testing.T, raw string) editor.FilePatch {
	t.Helper()
	files, err := editor.ParsePatch(raw)
	if err != nil {
		t.Fatalf("parse patch: %v\n%s", err, raw)
	}
	if len(files) != 1 {
		t.Fatalf("patch files = %d, want 1\n%s", len(files), raw)
	}
	return files[0]
}

// unstagedPatch is the patch of one file's working tree against the index,
// read and parsed as the diff panel reads it. The status letter picks git's
// diff: M or D a plain diff, ? or ! the no-index diff of an untracked file.
func unstagedPatch(t *testing.T, r *git.Repo, path string, context int) editor.FilePatch {
	t.Helper()
	return unstagedPatchStatus(t, r, path, 'M', context)
}

// unstagedPatchStatus is unstagedPatch with the file's status letter.
func unstagedPatchStatus(t *testing.T, r *git.Repo, path string, status byte, context int) editor.FilePatch {
	t.Helper()
	raw, err := r.FileDiffOpts(git.FileChange{Path: path, Status: status}, git.DiffOptions{Context: context})
	if err != nil {
		t.Fatalf("read unstaged diff of %s: %v", path, err)
	}
	return parseOnePatch(t, raw)
}

// stagedPatch is the patch of one staged file in the index against HEAD.
func stagedPatch(t *testing.T, r *git.Repo, path string, context int) editor.FilePatch {
	t.Helper()
	raw, err := r.StagedDiffOpts([]string{path}, git.DiffOptions{Context: context})
	if err != nil {
		t.Fatalf("read staged diff of %s: %v", path, err)
	}
	return parseOnePatch(t, raw)
}

// chooseLines marks the changed lines of f that match one of wants, the
// whole line with its first character, as "-three" or "+THREE".
func chooseLines(t *testing.T, f editor.FilePatch, wants ...string) [][]bool {
	t.Helper()
	left := map[string]int{}
	for _, w := range wants {
		left[w]++
	}
	chosen := make([][]bool, len(f.Hunks))
	for i, h := range f.Hunks {
		chosen[i] = make([]bool, len(h.Lines))
		for j, l := range h.Lines {
			if l == "" || l[0] == '\\' {
				continue
			}
			if left[l] > 0 {
				chosen[i][j] = true
				left[l]--
			}
		}
	}
	for w, n := range left {
		if n > 0 {
			t.Fatalf("line %q chosen but not in the patch", w)
		}
	}
	return chosen
}

// indexContent is the content of path in the repository's index.
func indexContent(t *testing.T, repo *gitRepoFixture, path string) string {
	t.Helper()
	out, err := tryGitCommand(repo.Path(), "show", ":"+path)
	if err != nil {
		t.Fatalf("git show :%s: %v\n%s", path, err, out)
	}
	return out
}

// readWorktreeFile is the content of path in the fixture's working tree.
func readWorktreeFile(t *testing.T, repo *gitRepoFixture, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo.Path(), path))
	if err != nil {
		t.Fatalf("read worktree %s: %v", path, err)
	}
	return string(data)
}

// stageLinesChosen builds the forward patch of the chosen lines and applies
// it to the index, as staging lines of an unstaged diff does.
func stageLinesChosen(t *testing.T, r *git.Repo, f editor.FilePatch, chosen [][]bool) string {
	t.Helper()
	text, err := buildLinesPatch(f, chosen, false)
	if err != nil {
		t.Fatalf("build staging patch: %v", err)
	}
	if err := r.ApplyLines(text, true, false); err != nil {
		t.Fatalf("apply staging patch: %v\n%s", err, text)
	}
	return text
}

// unstageLinesChosen builds the reverse patch of the chosen lines and
// applies it to the index backwards, as unstaging lines does.
func unstageLinesChosen(t *testing.T, r *git.Repo, f editor.FilePatch, chosen [][]bool) string {
	t.Helper()
	text, err := buildLinesPatch(f, chosen, true)
	if err != nil {
		t.Fatalf("build unstaging patch: %v", err)
	}
	if err := r.ApplyLines(text, true, true); err != nil {
		t.Fatalf("apply unstaging patch: %v\n%s", err, text)
	}
	return text
}

func linesText(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// The patch of one added line of a replacement run stages that addition
// alone: the deletion it answers to becomes context and stays in the index.
func TestDiffLinePatchStageSinglePlusLine(t *testing.T) {
	repo := newGitRepo(t, "lp-plus")
	repo.CommitFile("a.txt", linesText("one", "two", "three", "four", "five", "six", "seven"), "initial")
	repo.WriteFile("a.txt", linesText("one", "two", "THREE", "four-NEW", "five", "six", "seven"))
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 3)
	text := stageLinesChosen(t, r, fp, chooseLines(t, fp, "+THREE"))

	// The addition leaps ahead of the deletions it answers to, as git-gui
	// orders it: the deletions it replaces follow as context.
	if got, want := indexContent(t, repo, "a.txt"), linesText("one", "two", "THREE", "three", "four", "five", "six", "seven"); got != want {
		t.Fatalf("index after staging one addition =\n%q\nwant\n%q", got, want)
	}
	// The additions not chosen are absent from the patch, the deletions not
	// chosen read as context.
	if !strings.Contains(text, "\n three") || strings.Contains(text, "\n-three") {
		t.Fatalf("patch keeps unchosen deletion as context:\n%s", text)
	}
	if strings.Contains(text, "four-NEW") {
		t.Fatalf("patch holds an addition not chosen:\n%s", text)
	}
}

// The patch of one deleted line of a replacement run stages that removal
// alone: the addition it became is left out and stays unstaged.
func TestDiffLinePatchStageSingleMinusLine(t *testing.T) {
	repo := newGitRepo(t, "lp-minus")
	repo.CommitFile("a.txt", linesText("one", "two", "three", "four", "five", "six", "seven"), "initial")
	repo.WriteFile("a.txt", linesText("one", "two", "THREE", "four-NEW", "five", "six", "seven"))
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 3)
	stageLinesChosen(t, r, fp, chooseLines(t, fp, "-three"))

	if got, want := indexContent(t, repo, "a.txt"), linesText("one", "two", "four", "five", "six", "seven"); got != want {
		t.Fatalf("index after staging one deletion =\n%q\nwant\n%q", got, want)
	}
}

// A deletion and the addition replacing it stage together as the change.
func TestDiffLinePatchStageReplacementPair(t *testing.T) {
	repo := newGitRepo(t, "lp-pair")
	repo.CommitFile("a.txt", linesText("one", "two", "three", "four", "five", "six", "seven"), "initial")
	repo.WriteFile("a.txt", linesText("one", "two", "THREE", "four-NEW", "five", "six", "seven"))
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 3)
	stageLinesChosen(t, r, fp, chooseLines(t, fp, "-three", "+THREE"))

	if got, want := indexContent(t, repo, "a.txt"), linesText("one", "two", "THREE", "four", "five", "six", "seven"); got != want {
		t.Fatalf("index after staging a replacement =\n%q\nwant\n%q", got, want)
	}
	// The other change of the run is still unstaged.
	raw, err := r.FileDiffOpts(git.FileChange{Path: "a.txt", Status: 'M'}, git.DiffOptions{Context: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "-four") || !strings.Contains(raw, "+four-NEW") {
		t.Fatalf("remaining unstaged diff =\n%s", raw)
	}
}

// twoHunkFixture commits twenty lettered lines, replaces the third with
// two lines, and replaces the fifteenth, so the diff is two @@ hunks and
// the first grows the file by one line.
func twoHunkFixture(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	var base, edited []string
	for i := 1; i <= 20; i++ {
		base = append(base, "line"+string(rune('a'+i-1)))
	}
	edited = append(edited, base...)
	edited[2] = "lineC1"
	edited = append(edited[:3], append([]string{"lineC2"}, edited[3:]...)...)
	edited[15] = "lineO-NEW"
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", linesText(base...), "initial")
	repo.WriteFile("a.txt", linesText(edited...))
	return repo
}

// Choosing lines of the second hunk alone, its new-side start is the old
// one: the first hunk the patch leaves out shifts nothing.
func TestDiffLinePatchStageSecondOfTwoHunks(t *testing.T) {
	repo := twoHunkFixture(t, "lp-second")
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 3)
	if len(fp.Hunks) != 2 {
		t.Fatalf("hunks = %d, want 2", len(fp.Hunks))
	}
	text := stageLinesChosen(t, r, fp, chooseLines(t, fp, "-lineo", "+lineO-NEW"))

	if !strings.Contains(text, "@@ -12,7 +12,7 @@") {
		t.Fatalf("second hunk header shifted by the hunk left out:\n%s", text)
	}
	if strings.Contains(text, "lineC") {
		t.Fatalf("patch holds lines of the hunk not chosen:\n%s", text)
	}
	got := indexContent(t, repo, "a.txt")
	if !strings.Contains(got, "lineO-NEW\n") || strings.Contains(got, "lineC1") {
		t.Fatalf("index after staging the second hunk =\n%q", got)
	}
	if strings.Count(got, "\n") != 20 {
		t.Fatalf("index line count changed: %q", got)
	}
}

// Choosing the first hunk and the second hunk, the second hunk's new-side
// start shifts by the lines the first adds.
func TestDiffLinePatchStageAcrossTwoHunks(t *testing.T) {
	repo := twoHunkFixture(t, "lp-across")
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 3)
	// The first hunk's replacement, both added lines: net one line more.
	text := stageLinesChosen(t, r, fp, chooseLines(t, fp, "-linec", "+lineC1", "+lineC2", "-lineo", "+lineO-NEW"))

	if !strings.Contains(text, "@@ -12,7 +13,7 @@") {
		t.Fatalf("second hunk header not shifted by the first hunk's growth:\n%s", text)
	}
	got := indexContent(t, repo, "a.txt")
	var want []string
	want = append(want, "linea", "lineb", "lineC1", "lineC2")
	for i := 4; i <= 20; i++ {
		if i == 15 {
			want = append(want, "lineO-NEW")
			continue
		}
		want = append(want, "line"+string(rune('a'+i-1)))
	}
	if got != linesText(want...) {
		t.Fatalf("index after staging across hunks =\n%q\nwant\n%q", got, linesText(want...))
	}
}

// Unstaging part of a staged file takes exactly that part back out of the
// index: the other run stays staged.
func TestDiffLinePatchUnstageOneOfTwoRuns(t *testing.T) {
	repo := twoHunkFixture(t, "lp-unstage-run")
	r := linePatchRepo(repo)
	repo.Git("add", "--", "a.txt")

	fp := stagedPatch(t, r, "a.txt", 3)
	if len(fp.Hunks) != 2 {
		t.Fatalf("hunks = %d, want 2", len(fp.Hunks))
	}
	text := unstageLinesChosen(t, r, fp, chooseLines(t, fp, "-lineo", "+lineO-NEW"))

	got := indexContent(t, repo, "a.txt")
	if strings.Contains(got, "lineO-NEW") {
		t.Fatalf("index still holds the unstaged line:\n%q", got)
	}
	if !strings.Contains(got, "lineC1") || !strings.Contains(got, "lineC2") {
		t.Fatalf("index lost the run that stays staged:\n%q", got)
	}
	if !strings.Contains(got, "linep\n") {
		t.Fatalf("index lost the restored line:\n%q", got)
	}
	if strings.Contains(text, "lineC") {
		t.Fatalf("unstaging patch holds lines of the run not chosen:\n%s", text)
	}
}

// Unstaging some lines of a staged run leaves the rest of it in the index:
// additions not chosen stay as context, deletions not chosen are left out.
func TestDiffLinePatchUnstageLines(t *testing.T) {
	repo := newGitRepo(t, "lp-unstage-lines")
	repo.CommitFile("a.txt", linesText("one", "two", "three", "four", "five", "six", "seven"), "initial")
	repo.WriteFile("a.txt", linesText("one", "two", "THREE", "four-NEW", "five", "six", "seven"))
	repo.Git("add", "--", "a.txt")
	r := linePatchRepo(repo)

	fp := stagedPatch(t, r, "a.txt", 3)
	// Unstage only the first replacement: THREE leaves the index, the
	// four-NEW change stays staged.
	unstageLinesChosen(t, r, fp, chooseLines(t, fp, "-three", "+THREE"))

	got := indexContent(t, repo, "a.txt")
	if got != linesText("one", "two", "three", "four-NEW", "five", "six", "seven") {
		t.Fatalf("index after unstaging one replacement =\n%q", got)
	}
}

// Staging an addition at the end of a file that had no final newline keeps
// the no-newline marker with the line it belongs to and the index gains
// exactly the appended lines.
func TestDiffLinePatchNoNewlineAppendAtEnd(t *testing.T) {
	repo := newGitRepo(t, "lp-noeol-append")
	repo.CommitFile("a.txt", "one\ntwo\nthree", "initial")
	repo.WriteFile("a.txt", "one\ntwo\nthree\nfour\n")
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 3)
	hasMarker := false
	for _, h := range fp.Hunks {
		for _, l := range h.Lines {
			if strings.HasPrefix(l, "\\") {
				hasMarker = true
			}
		}
	}
	if !hasMarker {
		t.Fatalf("the diff has no no-newline marker:\n%+v", fp.Hunks)
	}
	var wants []string
	for _, h := range fp.Hunks {
		for _, l := range h.Lines {
			if l != "" && (l[0] == '-' || l[0] == '+') {
				wants = append(wants, l)
			}
		}
	}
	stageLinesChosen(t, r, fp, chooseLines(t, fp, wants...))

	if got := indexContent(t, repo, "a.txt"); got != "one\ntwo\nthree\nfour\n" {
		t.Fatalf("index after staging the append = %q", got)
	}
}

// Staging a change of the last line that drops the final newline carries
// the marker over: the index copy ends without a newline too.
func TestDiffLinePatchNoNewlineChangeLastLine(t *testing.T) {
	repo := newGitRepo(t, "lp-noeol-change")
	repo.CommitFile("a.txt", "one\ntwo\nthree\n", "initial")
	repo.WriteFile("a.txt", "one\ntwo\nTHREE")
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 3)
	text := stageLinesChosen(t, r, fp, chooseLines(t, fp, "-three", "+THREE"))

	if !strings.Contains(text, "\\ No newline at end of file") {
		t.Fatalf("patch dropped the no-newline marker:\n%s", text)
	}
	if got := indexContent(t, repo, "a.txt"); got != "one\ntwo\nTHREE" {
		t.Fatalf("index after staging the last-line change = %q", got)
	}
	// Unstaging it again restores the newline exactly.
	repo.Git("add", "--", "a.txt")
	sp := stagedPatch(t, r, "a.txt", 3)
	unstageLinesChosen(t, r, sp, chooseLines(t, sp, "-three", "+THREE"))
	if got := indexContent(t, repo, "a.txt"); got != "one\ntwo\nthree\n" {
		t.Fatalf("index after unstaging the last-line change = %q", got)
	}
}

// A diff read in entire-file mode is one hunk of the whole file; staging
// lines of it works the same and the context lines verify the content.
func TestDiffLinePatchEntireFileContext(t *testing.T) {
	repo := twoHunkFixture(t, "lp-entire")
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 1<<20)
	if len(fp.Hunks) != 1 {
		t.Fatalf("entire-file hunks = %d, want 1", len(fp.Hunks))
	}
	text := stageLinesChosen(t, r, fp, chooseLines(t, fp, "-lineo", "+lineO-NEW"))

	if strings.Count(text, "@@") != 2 {
		t.Fatalf("patch is not one hunk:\n%s", text)
	}
	got := indexContent(t, repo, "a.txt")
	if !strings.Contains(got, "lineO-NEW\n") || strings.Contains(got, "lineC1") {
		t.Fatalf("index after staging lines of an entire-file diff =\n%q", got)
	}
}

// A file with CRLF line endings stages and unstages line by line without
// rewriting any line ending: the carriage returns stay in the index copy.
func TestDiffLinePatchCRLFEndings(t *testing.T) {
	repo := newGitRepo(t, "lp-crlf")
	repo.CommitFile("a.txt", "one\r\ntwo\r\nthree\r\nfour\r\nfive\r\n", "initial")
	repo.WriteFile("a.txt", "one\r\ntwo\r\nTHREE\r\nfour\r\nfive\r\n")
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 3)
	keepsCR := false
	for _, h := range fp.Hunks {
		for _, l := range h.Lines {
			if strings.HasSuffix(l, "\r") {
				keepsCR = true
			}
		}
	}
	if !keepsCR {
		t.Fatal("the parsed patch does not keep carriage returns; CRLF files cannot be staged by lines")
	}
	text := stageLinesChosen(t, r, fp, chooseLines(t, fp, "-three\r", "+THREE\r"))

	if got := indexContent(t, repo, "a.txt"); got != "one\r\ntwo\r\nTHREE\r\nfour\r\nfive\r\n" {
		t.Fatalf("index after staging a CRLF change = %q", got)
	}
	if strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\r") {
		t.Fatalf("patch rewrote a line ending:\n%q", text)
	}

	// Unstaging half of a fully staged CRLF change keeps the endings too.
	repo.WriteFile("a.txt", "one\r\ntwo\r\nTHREE\r\nFOUR\r\nfive\r\n")
	repo.Git("add", "--", "a.txt")
	sp := stagedPatch(t, r, "a.txt", 3)
	unstageLinesChosen(t, r, sp, chooseLines(t, sp, "-four\r", "+FOUR\r"))
	if got := indexContent(t, repo, "a.txt"); got != "one\r\ntwo\r\nTHREE\r\nfour\r\nfive\r\n" {
		t.Fatalf("index after unstaging a CRLF change = %q", got)
	}
}

// Paths with spaces and non-ASCII text apply without quoting, as git writes
// them with core.quotepath off.
func TestDiffLinePatchPathWithSpaceAndUnicode(t *testing.T) {
	repo := newGitRepo(t, "lp-paths")
	repo.CommitFile("sp ace.txt", "one\ntwo\n", "initial")
	repo.CommitFile("中文.txt", "alpha\nbeta\n", "unicode")
	repo.WriteFile("sp ace.txt", "one\ntwo-NEW\n")
	repo.WriteFile("中文.txt", "alpha\nbeta-NEW\n")
	r := linePatchRepo(repo)

	for _, path := range []string{"sp ace.txt", "中文.txt"} {
		fp := unstagedPatch(t, r, path, 3)
		var wants []string
		for _, h := range fp.Hunks {
			for _, l := range h.Lines {
				if l != "" && (l[0] == '-' || l[0] == '+') {
					wants = append(wants, l)
				}
			}
		}
		text := stageLinesChosen(t, r, fp, chooseLines(t, fp, wants...))
		if strings.Contains(text, `"`) {
			t.Fatalf("path %q quoted in patch:\n%s", path, text)
		}
		if !strings.Contains(text, "--- a/"+path+"\n") {
			t.Fatalf("patch header missing path %q:\n%s", path, text)
		}
	}
	if got := indexContent(t, repo, "sp ace.txt"); got != "one\ntwo-NEW\n" {
		t.Fatalf("index of the spaced path = %q", got)
	}
	if got := indexContent(t, repo, "中文.txt"); got != "alpha\nbeta-NEW\n" {
		t.Fatalf("index of the unicode path = %q", got)
	}
}

// A patch of no chosen change is an error and never reaches git.
func TestDiffLinePatchNoSelectionErrors(t *testing.T) {
	repo := newGitRepo(t, "lp-none")
	repo.CommitFile("a.txt", linesText("one", "two", "three"), "initial")
	repo.WriteFile("a.txt", linesText("one", "TWO", "three"))
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 3)
	empty := make([][]bool, len(fp.Hunks))
	for i, h := range fp.Hunks {
		empty[i] = make([]bool, len(h.Lines))
	}
	if text, err := buildLinesPatch(fp, empty, false); err == nil {
		t.Fatalf("no forward selection built a patch:\n%s", text)
	}
	if text, err := buildLinesPatch(fp, empty, true); err == nil {
		t.Fatalf("no reverse selection built a patch:\n%s", text)
	}

	// A selection covering context lines alone has no changes either.
	chosen := patchLinesSelected(fp, editor.DiffLineRange{Side: editor.DiffNew, Start: 0, End: 0})
	if text, err := buildLinesPatch(fp, chosen, false); err == nil {
		t.Fatalf("a context-only selection built a patch:\n%s", text)
	}
	if got := indexContent(t, repo, "a.txt"); got != linesText("one", "two", "three") {
		t.Fatalf("the index changed without a patch applied: %q", got)
	}
}

// A diff read with no context lines, as the toolbar's zero-context choice
// asks git for, builds patches with empty sides: staging one addition of a
// replacement run yields a hunk with no old lines beyond the deletion kept.
func TestDiffLinePatchZeroContext(t *testing.T) {
	repo := newGitRepo(t, "lp-uctx")
	repo.CommitFile("a.txt", linesText("one", "two", "three", "four", "five"), "initial")
	// A pure addition after two, and a replacement of four.
	repo.WriteFile("a.txt", linesText("one", "two", "TWO-ADD", "three", "FOUR", "five"))
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", -1)
	if len(fp.Hunks) != 2 {
		t.Fatalf("zero-context hunks = %d, want 2", len(fp.Hunks))
	}
	// Stage only the pure addition: the first hunk has no deletion, and its
	// old side is empty.
	text := stageLinesChosen(t, r, fp, chooseLines(t, fp, "+TWO-ADD"))
	if !strings.Contains(text, "+TWO-ADD") || strings.Contains(text, "FOUR") {
		t.Fatalf("patch of the addition wrong:\n%s", text)
	}
	if got := indexContent(t, repo, "a.txt"); got != linesText("one", "two", "TWO-ADD", "three", "four", "five") {
		t.Fatalf("index after staging the addition =\n%q", got)
	}

	// Stage both from the same diff in one patch: the second hunk's
	// new-side start shifts by the line the first adds. The index goes back
	// first, as the construction the patch came from reads the diff as it
	// was: applying an addition twice would duplicate it.
	repo.Git("reset", "-q")
	text = stageLinesChosen(t, r, fp, chooseLines(t, fp, "+TWO-ADD", "-four", "+FOUR"))
	if got := indexContent(t, repo, "a.txt"); got != linesText("one", "two", "TWO-ADD", "three", "FOUR", "five") {
		t.Fatalf("index after staging both =\n%q\npatch:\n%s", got, text)
	}
}

// Staging every line of an untracked file writes a creation patch: the new
// file mode header and /dev/null let git create the index entry, which
// holds the file whole.
func TestDiffLinePatchStageUntrackedAllLines(t *testing.T) {
	repo := newGitRepo(t, "lp-untracked-all")
	repo.CommitFile("base.txt", "base\n", "initial")
	repo.WriteFile("new.txt", linesText("alpha", "beta", "gamma", "delta"))
	r := linePatchRepo(repo)

	fp := unstagedPatchStatus(t, r, "new.txt", '?', 3)
	text := stageLinesChosen(t, r, fp, chooseLines(t, fp, "+alpha", "+beta", "+gamma", "+delta"))

	if !strings.Contains(text, "new file mode 100644\n") || !strings.Contains(text, "--- /dev/null\n") {
		t.Fatalf("staging a whole untracked file wrote no creation headers:\n%s", text)
	}
	if got := indexContent(t, repo, "new.txt"); got != linesText("alpha", "beta", "gamma", "delta") {
		t.Fatalf("index after staging all lines =\n%q", got)
	}
	// The file is staged whole: nothing is left unstaged, as git status
	// tells added-and-unmodified from added-and-modified.
	if got := repo.Git("status", "--porcelain", "--", "new.txt"); got != "A  new.txt\n" {
		t.Fatalf("status after staging all lines = %q, want fully added", got)
	}
}

// Staging only two lines of an untracked file keeps the creation headers:
// git creates the index entry with exactly the lines chosen, and the rest
// of the file stays untracked work.
func TestDiffLinePatchStageUntrackedTwoLines(t *testing.T) {
	repo := newGitRepo(t, "lp-untracked-two")
	repo.CommitFile("base.txt", "base\n", "initial")
	repo.WriteFile("new.txt", linesText("alpha", "beta", "gamma", "delta"))
	r := linePatchRepo(repo)

	fp := unstagedPatchStatus(t, r, "new.txt", '?', 3)
	text := stageLinesChosen(t, r, fp, chooseLines(t, fp, "+alpha", "+beta"))

	if got := indexContent(t, repo, "new.txt"); got != linesText("alpha", "beta") {
		t.Fatalf("index after staging two lines =\n%q", got)
	}
	if strings.Contains(text, "\n gamma") || strings.Contains(text, "\n delta") {
		t.Fatalf("patch of a partial creation holds unchosen lines as context:\n%s", text)
	}
	// Added in the index, modified in the working tree: gamma and delta are
	// still to stage.
	if got := repo.Git("status", "--porcelain", "--", "new.txt"); got != "AM new.txt\n" {
		t.Fatalf("status after staging two lines = %q, want added-and-modified", got)
	}
	raw, err := r.FileDiffOpts(git.FileChange{Path: "new.txt", Status: 'M'}, git.DiffOptions{Context: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "+gamma") || !strings.Contains(raw, "+delta") {
		t.Fatalf("remaining unstaged diff lost the unchosen lines:\n%s", raw)
	}
}

// Unstaging every line of an added file removes the index entry: the file
// is untracked again.
func TestDiffLinePatchUnstageAddedAllLines(t *testing.T) {
	repo := newGitRepo(t, "lp-added-all")
	repo.CommitFile("base.txt", "base\n", "initial")
	repo.WriteFile("new.txt", linesText("alpha", "beta", "gamma", "delta"))
	repo.Git("add", "--", "new.txt")
	r := linePatchRepo(repo)

	fp := stagedPatch(t, r, "new.txt", 3)
	text := unstageLinesChosen(t, r, fp, chooseLines(t, fp, "+alpha", "+beta", "+gamma", "+delta"))

	if !strings.Contains(text, "new file mode 100644\n") {
		t.Fatalf("unstaging a whole addition wrote no creation header:\n%s", text)
	}
	if out, err := tryGitCommand(repo.Path(), "ls-files", "--error-unmatch", "--", "new.txt"); err == nil {
		t.Fatalf("index still holds the fully unstaged file: %s", out)
	}
	if got := repo.Git("status", "--porcelain", "--", "new.txt"); got != "?? new.txt\n" {
		t.Fatalf("status after unstaging all lines = %q, want untracked", got)
	}
	if got := readWorktreeFile(t, repo, "new.txt"); got != linesText("alpha", "beta", "gamma", "delta") {
		t.Fatalf("unstaging touched the working tree file: %q", got)
	}
}

// Unstaging two lines of an added file keeps the rest in the index: the
// patch is a plain modification, since the file must stay in the index
// with its remaining lines.
func TestDiffLinePatchUnstageAddedTwoLines(t *testing.T) {
	repo := newGitRepo(t, "lp-added-two")
	repo.CommitFile("base.txt", "base\n", "initial")
	repo.WriteFile("new.txt", linesText("alpha", "beta", "gamma", "delta"))
	repo.Git("add", "--", "new.txt")
	r := linePatchRepo(repo)

	fp := stagedPatch(t, r, "new.txt", 3)
	// Unstage gamma and delta: alpha and beta stay added.
	text := unstageLinesChosen(t, r, fp, chooseLines(t, fp, "+gamma", "+delta"))

	if strings.Contains(text, "new file mode") {
		t.Fatalf("partial unstage of an addition kept the creation header:\n%s", text)
	}
	if got := indexContent(t, repo, "new.txt"); got != linesText("alpha", "beta") {
		t.Fatalf("index after unstaging two lines =\n%q", got)
	}
	if got := repo.Git("status", "--porcelain", "--", "new.txt"); got != "AM new.txt\n" {
		t.Fatalf("status after unstaging two lines = %q, want added-and-modified", got)
	}
	if got := readWorktreeFile(t, repo, "new.txt"); got != linesText("alpha", "beta", "gamma", "delta") {
		t.Fatalf("unstaging touched the working tree file: %q", got)
	}
}

// Staging every line of a deleted file writes a deletion patch: the index
// entry is removed and the deletion is staged whole.
func TestDiffLinePatchStageDeletedAllLines(t *testing.T) {
	repo := newGitRepo(t, "lp-deleted-all")
	repo.CommitFile("gone.txt", linesText("one", "two", "three", "four", "five"), "initial")
	if err := os.Remove(filepath.Join(repo.Path(), "gone.txt")); err != nil {
		t.Fatal(err)
	}
	r := linePatchRepo(repo)

	fp := unstagedPatchStatus(t, r, "gone.txt", 'D', 3)
	text := stageLinesChosen(t, r, fp, chooseLines(t, fp, "-one", "-two", "-three", "-four", "-five"))

	if !strings.Contains(text, "deleted file mode 100644\n") || !strings.Contains(text, "+++ /dev/null\n") {
		t.Fatalf("staging a whole deletion wrote no deletion headers:\n%s", text)
	}
	if out, err := tryGitCommand(repo.Path(), "ls-files", "--error-unmatch", "--", "gone.txt"); err == nil {
		t.Fatalf("index still holds the staged deletion: %s", out)
	}
	if got := repo.Git("status", "--porcelain", "--", "gone.txt"); got != "D  gone.txt\n" {
		t.Fatalf("status after staging the deletion = %q, want fully staged", got)
	}
}

// Staging two lines of a deleted file leaves the file in the index with
// exactly the lines not chosen: the patch is a plain modification.
func TestDiffLinePatchStageDeletedTwoLines(t *testing.T) {
	repo := newGitRepo(t, "lp-deleted-two")
	repo.CommitFile("gone.txt", linesText("one", "two", "three", "four", "five"), "initial")
	if err := os.Remove(filepath.Join(repo.Path(), "gone.txt")); err != nil {
		t.Fatal(err)
	}
	r := linePatchRepo(repo)

	fp := unstagedPatchStatus(t, r, "gone.txt", 'D', 3)
	// Stage the deletion of two and three: the index keeps one, four, five.
	text := stageLinesChosen(t, r, fp, chooseLines(t, fp, "-two", "-three"))

	if strings.Contains(text, "deleted file mode") {
		t.Fatalf("partial stage of a deletion kept the deletion header:\n%s", text)
	}
	if got := indexContent(t, repo, "gone.txt"); got != linesText("one", "four", "five") {
		t.Fatalf("index after staging two deletions =\n%q", got)
	}
	if got := repo.Git("status", "--porcelain", "--", "gone.txt"); got != "MD gone.txt\n" {
		t.Fatalf("status after staging two deletions = %q, want modified-and-deleted", got)
	}
}

// Unstaging every line of a staged deletion restores the file in the index
// exactly as HEAD has it, so the deletion unstages whole.
func TestDiffLinePatchUnstageStagedDeletionAllLines(t *testing.T) {
	repo := newGitRepo(t, "lp-undel-all")
	repo.CommitFile("gone.txt", linesText("one", "two", "three", "four", "five"), "initial")
	if err := os.Remove(filepath.Join(repo.Path(), "gone.txt")); err != nil {
		t.Fatal(err)
	}
	repo.Git("add", "--", "gone.txt")
	r := linePatchRepo(repo)

	fp := stagedPatch(t, r, "gone.txt", 3)
	unstageLinesChosen(t, r, fp, chooseLines(t, fp, "-one", "-two", "-three", "-four", "-five"))

	if got := indexContent(t, repo, "gone.txt"); got != linesText("one", "two", "three", "four", "five") {
		t.Fatalf("index after unstaging the deletion =\n%q", got)
	}
	if got := repo.Git("status", "--porcelain", "--", "gone.txt"); got != " D gone.txt\n" {
		t.Fatalf("status after unstaging the deletion = %q, want only unstaged deletion left", got)
	}
}

// Unstaging two lines of a staged deletion restores exactly those lines in
// the index: the file is there with nothing else, since reverse-creating
// writes the lines the patch holds.
func TestDiffLinePatchUnstageStagedDeletionTwoLines(t *testing.T) {
	repo := newGitRepo(t, "lp-undel-two")
	repo.CommitFile("gone.txt", linesText("one", "two", "three", "four", "five"), "initial")
	if err := os.Remove(filepath.Join(repo.Path(), "gone.txt")); err != nil {
		t.Fatal(err)
	}
	repo.Git("add", "--", "gone.txt")
	r := linePatchRepo(repo)

	fp := stagedPatch(t, r, "gone.txt", 3)
	// Restore two and three alone: one, four and five stay deleted.
	text := unstageLinesChosen(t, r, fp, chooseLines(t, fp, "-two", "-three"))

	if !strings.Contains(text, "deleted file mode") {
		t.Fatalf("unstaging lines of a deletion dropped the deletion header:\n%s", text)
	}
	if got := indexContent(t, repo, "gone.txt"); got != linesText("two", "three") {
		t.Fatalf("index after unstaging two deletions =\n%q", got)
	}
	if got := repo.Git("status", "--porcelain", "--", "gone.txt"); got != "MD gone.txt\n" {
		t.Fatalf("status after unstaging two deletions = %q, want modified-and-deleted", got)
	}
}

// An untracked file without a trailing newline stages whole and in part
// with the no-newline marker on the line it belongs to: a partial staging
// of the last line writes a file ending without a newline.
func TestDiffLinePatchUntrackedNoTrailingNewline(t *testing.T) {
	repo := newGitRepo(t, "lp-untracked-noeol")
	repo.CommitFile("base.txt", "base\n", "initial")
	repo.WriteFile("new.txt", "alpha\nbeta")
	r := linePatchRepo(repo)

	fp := unstagedPatchStatus(t, r, "new.txt", '?', 3)
	// Stage only beta, the newline-less last line.
	stageLinesChosen(t, r, fp, chooseLines(t, fp, "+beta"))

	if got := indexContent(t, repo, "new.txt"); got != "beta" {
		t.Fatalf("index after staging the last line = %q, want exactly beta without newline", got)
	}
	repo.Git("reset", "-q")

	// Staging both lines writes the file as it is on disk.
	fp = unstagedPatchStatus(t, r, "new.txt", '?', 3)
	text := stageLinesChosen(t, r, fp, chooseLines(t, fp, "+alpha", "+beta"))
	if strings.Count(text, "\\ No newline at end of file") != 1 {
		t.Fatalf("creation patch does not carry exactly one no-newline marker:\n%s", text)
	}
	if got := indexContent(t, repo, "new.txt"); got != "alpha\nbeta" {
		t.Fatalf("index after staging the whole file = %q", got)
	}
}

// An untracked file with CRLF line endings stages with its carriage
// returns intact, in whole and in part.
func TestDiffLinePatchUntrackedCRLF(t *testing.T) {
	repo := newGitRepo(t, "lp-untracked-crlf")
	repo.CommitFile("base.txt", "base\n", "initial")
	repo.WriteFile("new.txt", "alpha\r\nbeta\r\ngamma\r\n")
	r := linePatchRepo(repo)

	fp := unstagedPatchStatus(t, r, "new.txt", '?', 3)
	stageLinesChosen(t, r, fp, chooseLines(t, fp, "+alpha\r", "+beta\r"))

	if got := indexContent(t, repo, "new.txt"); got != "alpha\r\nbeta\r\n" {
		t.Fatalf("index after staging two CRLF lines = %q", got)
	}
	repo.Git("reset", "-q")

	fp = unstagedPatchStatus(t, r, "new.txt", '?', 3)
	text := stageLinesChosen(t, r, fp, chooseLines(t, fp, "+alpha\r", "+beta\r", "+gamma\r"))
	if strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\r") {
		t.Fatalf("creation patch rewrote a line ending:\n%q", text)
	}
	if got := indexContent(t, repo, "new.txt"); got != "alpha\r\nbeta\r\ngamma\r\n" {
		t.Fatalf("index after staging the CRLF file = %q", got)
	}
}

// The eligibility of a file's diff follows its page and kind: modifications
// on both pages, untracked files to stage, additions and deletions to
// unstage or stage; never renames, mode changes, links, gitlinks, or diffs
// read ignoring whitespace.
func TestDiffLinePatchEligibilityMatrix(t *testing.T) {
	// A modification of ordinary lines, both names the same.
	mod := parseOnePatch(t, "diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n@@ -1 +1 @@\n-old\n+new\n")
	// An addition, as an untracked or staged-new file reads.
	add := parseOnePatch(t, "diff --git a/f.txt b/f.txt\nnew file mode 100644\n--- /dev/null\n+++ b/f.txt\n@@ -0,0 +1 @@\n+new\n")
	// A deletion.
	del := parseOnePatch(t, "diff --git a/f.txt b/f.txt\ndeleted file mode 100644\n--- a/f.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n")
	// A rename keeps both names, different.
	ren := parseOnePatch(t, "diff --git a/f.txt b/g.txt\nsimilarity index 90%\nrename from f.txt\nrename to g.txt\n--- a/f.txt\n+++ b/g.txt\n@@ -1 +1 @@\n-old\n+new\n")
	// A symlink target change: one line replaced, no newline anywhere.
	link := parseOnePatch(t, "diff --git a/l b/l\nindex 8286e95..27fa349 120000\n--- a/l\n+++ b/l\n@@ -1 +1 @@\n-base\n\\ No newline at end of file\n+other\n\\ No newline at end of file\n")
	// A moved submodule: the subproject commit line.
	sub := parseOnePatch(t, "diff --git a/s b/s\nindex 06e7492..28a9ab7 160000\n--- a/s\n+++ b/s\n@@ -1 +1 @@\n-Subproject commit 06e749269a4091b6105ad23eb5fe84b9aac3c375\n+Subproject commit 28a9ab74f0a3d0a12a283fc55d489706cafb22f2\n")
	// A mode change beside content.
	mode := parseOnePatch(t, "diff --git a/f.txt b/f.txt\nold mode 100644\nnew mode 100755\n--- a/f.txt\n+++ b/f.txt\n@@ -1 +1 @@\n-old\n+new\n")

	unstaged := func(status byte) diffRequest {
		return diffRequest{Kind: diffUnstaged, File: git.FileChange{Path: "f.txt", Status: status}}
	}
	staged := func(status byte) diffRequest {
		return diffRequest{Kind: diffStaged, File: git.FileChange{Path: "f.txt", Status: status, Staged: true}}
	}
	cases := []struct {
		name  string
		req   diffRequest
		patch editor.FilePatch
		want  bool
	}{
		{"modified unstaged", unstaged('M'), mod, true},
		{"modified staged", staged('M'), mod, true},
		{"untracked", unstaged('?'), add, true},
		{"ignored", unstaged('!'), add, true},
		{"deleted unstaged", unstaged('D'), del, true},
		{"added staged", staged('A'), add, true},
		{"deleted staged", staged('D'), del, true},
		{"rename staged", diffRequest{Kind: diffStaged, File: git.FileChange{Path: "g.txt", OrigPath: "f.txt", Status: 'R', Staged: true}}, ren, false},
		{"symlink modified", unstaged('M'), link, false},
		{"submodule modified", unstaged('M'), sub, false},
		{"mode change", unstaged('M'), mode, false},
		{"untracked symlink", unstaged('?'), parseOnePatch(t, "diff --git a/l b/l\nnew file mode 120000\n--- /dev/null\n+++ b/l\n@@ -0,0 +1 @@\n+base\n\\ No newline at end of file\n"), false},
		{"conflicted", unstaged('U'), mod, false},
		{"ignore whitespace", diffRequest{Kind: diffUnstaged, File: git.FileChange{Path: "f.txt", Status: 'M'}, IgnoreSpace: true}, mod, false},
		{"history kind", diffRequest{Kind: diffCommitFile, File: git.FileChange{Path: "f.txt", Status: 'M'}}, mod, false},
		{"status disagrees with patch shape", unstaged('M'), add, false},
	}
	for _, tc := range cases {
		if got := diffLineActionsEligible(tc.req, tc.patch); got != tc.want {
			t.Errorf("%s: eligible = %v, want %v", tc.name, got, tc.want)
		}
	}
}
