package app

import (
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

// unstagedPatch is the patch of one modified file's working tree against
// the index, read and parsed as the diff panel reads it.
func unstagedPatch(t *testing.T, r *git.Repo, path string, context int) editor.FilePatch {
	t.Helper()
	raw, err := r.FileDiffOpts(git.FileChange{Path: path, Status: 'M'}, git.DiffOptions{Context: context})
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

// Staging only the addition of a replacement at the end of a file without a
// final newline must not glue the added line to the deletion that became
// context: the "\ No newline at end of file" marker travels to the line
// that truly ends each side.
func TestDiffLinePatchNoNewlineStagePlusOfReplacement(t *testing.T) {
	repo := newGitRepo(t, "lp-noeol-repl-plus")
	repo.CommitFile("a.txt", "one\ntwo", "initial")
	repo.WriteFile("a.txt", "one\nTWO")
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 3)
	text := stageLinesChosen(t, r, fp, chooseLines(t, fp, "+TWO"))

	if n := strings.Count(text, "\\ No newline"); n != 1 {
		t.Fatalf("patch carries %d no-newline markers, want 1:\n%s", n, text)
	}
	if got, want := indexContent(t, repo, "a.txt"), "one\nTWO\ntwo"; got != want {
		t.Fatalf("index after staging the addition = %q, want %q", got, want)
	}
}

// Staging only the trailing addition after the file's last line gained a
// newline must not glue the addition to the context line of the rewritten
// deletion: the marker of the context line splits into the newline change
// both sides of the file end differ in.
func TestDiffLinePatchNoNewlineStageAppendOnly(t *testing.T) {
	repo := newGitRepo(t, "lp-noeol-append-only")
	repo.CommitFile("a.txt", "one\ntwo", "initial")
	repo.WriteFile("a.txt", "one\ntwo\nthree")
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 3)
	// The diff carries -two and +two for the newline the last line gained;
	// only the real addition is chosen.
	stageLinesChosen(t, r, fp, chooseLines(t, fp, "+three"))

	if got, want := indexContent(t, repo, "a.txt"), "one\ntwo\nthree"; got != want {
		t.Fatalf("index after staging the append = %q, want %q", got, want)
	}
}

// Staging only the addition of a replacement that removes the file's final
// newline keeps the context line of the unchosen deletion newline-terminated:
// it is not the last line of the file once the addition precedes it.
func TestDiffLinePatchNoNewlineStagePlusDropsFinalNewline(t *testing.T) {
	repo := newGitRepo(t, "lp-noeol-drop-plus")
	repo.CommitFile("a.txt", "one\ntwo\n", "initial")
	repo.WriteFile("a.txt", "one\nTWO")
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, "a.txt", 3)
	stageLinesChosen(t, r, fp, chooseLines(t, fp, "+TWO"))

	if got, want := indexContent(t, repo, "a.txt"), "one\nTWO\ntwo\n"; got != want {
		t.Fatalf("index after staging the addition = %q, want %q", got, want)
	}
}

// Unstaging only the deletion of a staged end-of-file replacement without
// final newlines must not glue the restored deletion to the addition that
// became context: the marker stays on the last line of each side.
func TestDiffLinePatchNoNewlineUnstageMinusOfReplacement(t *testing.T) {
	repo := newGitRepo(t, "lp-noeol-unstage-minus")
	repo.CommitFile("a.txt", "one\ntwo", "initial")
	repo.WriteFile("a.txt", "one\nTWO")
	repo.Git("add", "--", "a.txt")
	r := linePatchRepo(repo)

	fp := stagedPatch(t, r, "a.txt", 3)
	text := unstageLinesChosen(t, r, fp, chooseLines(t, fp, "-two"))

	if n := strings.Count(text, "\\ No newline"); n > 2 {
		t.Fatalf("patch carries %d no-newline markers, want at most 2:\n%s", n, text)
	}
	if got, want := indexContent(t, repo, "a.txt"), "one\ntwo\nTWO"; got != want {
		t.Fatalf("index after unstaging the deletion = %q, want %q", got, want)
	}
}

// Unstaging only the deletion of the line appended after the file's last
// line must not glue it to the context line ahead of it: the reverse patch
// consumes the addition of the same text in place of the context.
func TestDiffLinePatchNoNewlineUnstageAppendedLine(t *testing.T) {
	repo := newGitRepo(t, "lp-noeol-unstage-append")
	repo.CommitFile("a.txt", "one\ntwo\nthree", "initial")
	repo.WriteFile("a.txt", "one\ntwo")
	repo.Git("add", "--", "a.txt")
	r := linePatchRepo(repo)

	fp := stagedPatch(t, r, "a.txt", 3)
	unstageLinesChosen(t, r, fp, chooseLines(t, fp, "-three"))

	if got, want := indexContent(t, repo, "a.txt"), "one\ntwo\nthree"; got != want {
		t.Fatalf("index after unstaging the deletion = %q, want %q", got, want)
	}
}

// A path holding a quote or a backslash quotes as one C string with the a/
// and b/ prefixes inside, as git writes it: "a/x\"y", never a/"x\"y".
func TestDiffLinePatchQuotedPath(t *testing.T) {
	repo := newGitRepo(t, "lp-quoted")
	repo.CommitFile(`a"b.txt`, "one\ntwo\nthree\n", "initial")
	repo.WriteFile(`a"b.txt`, "one\nTWO\nthree\n")
	r := linePatchRepo(repo)

	fp := unstagedPatch(t, r, `a"b.txt`, 3)
	text := stageLinesChosen(t, r, fp, chooseLines(t, fp, "-two", "+TWO"))

	if !strings.Contains(text, `diff --git "a/a\"b.txt" "b/a\"b.txt"`) {
		t.Fatalf("diff header not quoted with the prefixes inside:\n%s", text)
	}
	if !strings.Contains(text, `--- "a/a\"b.txt"`) || !strings.Contains(text, `+++ "b/a\"b.txt"`) {
		t.Fatalf("file headers not quoted with the prefixes inside:\n%s", text)
	}
	if strings.Contains(text, `a/"a\"b.txt"`) || strings.Contains(text, `b/"a\"b.txt"`) {
		t.Fatalf("prefix outside the quotes:\n%s", text)
	}
	if got, want := indexContent(t, repo, `a"b.txt`), "one\nTWO\nthree\n"; got != want {
		t.Fatalf("index after staging through the quoted path = %q, want %q", got, want)
	}

	// A backslash quotes the same way, and unstaging reads the header back.
	repo.Git("add", "--", `a"b.txt`)
	repo.CommitFile(`b\c.txt`, "x\n", "backslash")
	repo.WriteFile(`b\c.txt`, "y\n")
	fp2 := unstagedPatch(t, r, `b\c.txt`, 3)
	text2 := stageLinesChosen(t, r, fp2, chooseLines(t, fp2, "-x", "+y"))
	if !strings.Contains(text2, `--- "a/b\\c.txt"`) {
		t.Fatalf("backslash path not quoted with the prefix inside:\n%s", text2)
	}
	if got, want := indexContent(t, repo, `b\c.txt`), "y\n"; got != want {
		t.Fatalf("index after staging through the backslash path = %q, want %q", got, want)
	}
}
