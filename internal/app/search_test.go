package app

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// openSearchView opens the commit search from the History list header, as a
// user clicking the Search button does.
func openSearchView(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Search Commits"); err != nil {
		t.Fatalf("click Search Commits: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if !h.app.searchUI().open {
		t.Fatal("search view did not open from the header button")
	}
}

// typeSearchQuery focuses the query field and types q into it, replacing
// whatever was typed before.
func typeSearchQuery(t *testing.T, h *testHarness, q string) {
	t.Helper()
	if err := h.ui.Click("Search query"); err != nil {
		t.Fatalf("click Search query field: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Command("selectAll")
	h.ui.Type(q)
	h.ui.Frame()
	if got := h.app.searchUI().query; got != q {
		t.Fatalf("typed query = %q, want %q", got, q)
	}
}

// submitSearchQuery types q and submits it with Enter, then drains the
// search, detail and diff reads it chains.
func submitSearchQuery(t *testing.T, h *testHarness, q string) {
	t.Helper()
	typeSearchQuery(t, h, q)
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()
}

func searchRowLabel(hash, subject string) string {
	return "Search Result " + short7(hash) + " " + subject
}

func searchState(h *testHarness) *searchUIState {
	return h.app.searchUI()
}

func searchResultSubjects(st *searchUIState) []string {
	var out []string
	for _, cm := range st.results {
		out = append(out, cm.Subject)
	}
	return out
}

// uniqueResultHashes counts the distinct hashes among the search results.
func uniqueResultHashes(st *searchUIState) int {
	seen := map[string]bool{}
	for _, cm := range st.results {
		seen[cm.Hash] = true
	}
	return len(seen)
}

// The Search entry in the History list header opens the commit search, the
// Run Search button stays disabled while the query is empty or whitespace,
// Close Search and Escape return to the commit graph, and Cmd-F opens the
// search again.
func TestCommitSearchOpenCloseAndShortcuts(t *testing.T) {
	repo := newGitRepo(t, "search-openclose")
	repo.CommitFile("a.txt", "one\n", "first commit")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Search Commits")
	if !ok {
		t.Fatalf("Search button missing from the history list header; texts=%q", h.ui.Texts())
	}
	if btn.Description != "Search Commits (⌘F)" {
		t.Fatalf("Search button description = %q, want %q", btn.Description, "Search Commits (⌘F)")
	}

	openSearchView(t, h)
	if !h.ui.HasText("Type a query and press Enter to search commits.") {
		t.Fatalf("missing the not-yet-searched hint; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Close Search") {
		t.Fatalf("missing Close Search; texts=%q", h.ui.Texts())
	}
	if run, ok := h.ui.AccessByRole(ui.RoleButton, "Run Search"); !ok || !run.Disabled {
		t.Fatalf("Run Search access = %+v (found=%v), want disabled with an empty query", run, ok)
	}
	// A whitespace-only query keeps the button disabled.
	typeSearchQuery(t, h, "   ")
	if run, ok := h.ui.AccessByRole(ui.RoleButton, "Run Search"); !ok || !run.Disabled {
		t.Fatalf("Run Search access = %+v (found=%v), want disabled for a whitespace query", run, ok)
	}

	// Close through the button: the commit graph is back.
	if err := h.ui.Click("Close Search"); err != nil {
		t.Fatalf("click Close Search: %v", err)
	}
	h.ui.Frame()
	if h.app.searchUI().open {
		t.Fatal("search view still open after Close Search")
	}
	if !h.ui.HasText("first commit") {
		t.Fatalf("commit graph not back after Close Search; texts=%q", h.ui.Texts())
	}

	// Cmd-F opens the search again. The whitespace query is still in the
	// field, and there Escape is the field's own clear gesture; once the
	// field is empty, Escape closes the search.
	h.ui.Key(ui.Cmd, ui.KeyF)
	h.ui.Frame()
	if !h.app.searchUI().open {
		t.Fatal("search view did not open from Cmd-F")
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	if !h.app.searchUI().open {
		t.Fatal("search view closed while the query field still held text")
	}
	if got := h.app.searchUI().query; got != "" {
		t.Fatalf("query after first Escape = %q, want cleared", got)
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	if h.app.searchUI().open {
		t.Fatal("search view still open after Escape on the empty field")
	}
	if !h.ui.HasText("first commit") {
		t.Fatalf("commit graph not back after Escape; texts=%q", h.ui.Texts())
	}

	// Cmd-F belongs to the History page: on Local Changes it does not open
	// the commit search, and back on History it does again.
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.drain()
	h.ui.Key(ui.Cmd, ui.KeyF)
	h.ui.Frame()
	if h.app.searchUI().open {
		t.Fatal("Cmd-F on Local Changes opened the commit search")
	}
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.drain()
	h.ui.Key(ui.Cmd, ui.KeyF)
	h.ui.Frame()
	if !h.app.searchUI().open {
		t.Fatal("search view did not open from Cmd-F back on History")
	}
}

// A message search finds the matching commits, auto-selects the first
// result, and the detail pane shows the commit, its changed files and the
// first file's diff; clicking the other file switches the diff, and the
// Split/Unified toggle changes the diff layout.
func TestCommitSearchDetailFilesAndDiff(t *testing.T) {
	repo := newGitRepo(t, "search-detail")
	root := repo.CommitFile("x.go", "package x\n\nvar a = 1\n", "base add")
	repo.WriteFile("x.go", "package x\n\nvar a = 2\n")
	repo.WriteFile("y.go", "package y\n\nvar y = true\n")
	repo.Git("add", "x.go", "y.go")
	repo.Git("commit", "-m", "bump version\n\nbody text here")
	second := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openSearchView(t, h)
	submitSearchQuery(t, h, "bump")

	st := searchState(h)
	if st.err != nil {
		t.Fatalf("search error: %v", st.err)
	}
	if len(st.results) != 1 || st.results[0].Hash != second {
		t.Fatalf("results = %q, want exactly [bump version]", searchResultSubjects(st))
	}
	if !h.ui.HasText("1 result") {
		t.Fatalf("missing result count; texts=%q", h.ui.Texts())
	}
	// The first result is auto-selected, reads selected in the list, and its
	// detail and first file diff load without another click.
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, searchRowLabel(second, "bump version")); !ok || !row.Selected {
		t.Fatalf("selected result row access = %+v (found=%v), want Selected=true", row, ok)
	}
	if st.selHash != second {
		t.Fatalf("auto-selected result = %q, want %q", st.selHash, second)
	}
	if !h.ui.HasText("body text here") {
		t.Fatalf("detail missing message body; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText(second) {
		t.Fatalf("detail missing full commit hash; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText(short7(root)) {
		t.Fatalf("detail missing parent short hash; texts=%q", h.ui.Texts())
	}
	if len(st.files) != 2 {
		t.Fatalf("files = %+v, want [x.go y.go]", st.files)
	}
	firstFile := st.files[0].Path
	otherFile := st.files[1].Path
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, "File "+firstFile); !ok || !row.Selected {
		t.Fatalf("auto-selected file row access = %+v (found=%v), want Selected=true", row, ok)
	}
	// File rows are left-aligned: the badge and the short file name hug the
	// row's left edge instead of floating in the middle of the wide row.
	for _, path := range []string{firstFile, otherFile} {
		rowInfo, ok := h.ui.Access("File " + path)
		if !ok {
			t.Fatalf("file row %q missing; texts=%q", path, h.ui.Texts())
		}
		rowRect := rowInfo.Bounds
		textRect, ok := h.ui.Find(path)
		if !ok {
			t.Fatalf("file name %q missing; texts=%q", path, h.ui.Texts())
		}
		if textRect.Y < rowRect.Y || textRect.Y+textRect.H > rowRect.Y+rowRect.H+0.5 {
			t.Fatalf("first %q text at %+v is not inside its row %+v", path, textRect, rowRect)
		}
		if off := textRect.X - rowRect.X; off >= 60 {
			t.Fatalf("file name %q starts %.2f into its row (row X=%.2f W=%.2f, text X=%.2f), want < 60: the row content is centered instead of left-aligned", path, off, rowRect.X, rowRect.W, textRect.X)
		}
	}

	wantIns := "var a = 2"
	wantOther := "var y = true"
	if firstFile == "y.go" {
		wantIns, wantOther = wantOther, wantIns
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, wantIns) {
		t.Fatalf("first file diff insertions = %q, want %q", ins, wantIns)
	}

	// Clicking the other file switches the diff.
	if err := h.ui.Click("File " + otherFile); err != nil {
		t.Fatalf("click File %s: %v", otherFile, err)
	}
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, wantOther) {
		t.Fatalf("after file switch insertions = %q, want %q", ins, wantOther)
	}

	// The Split/Unified toggle of the shared diff panel applies to the
	// search diff.
	if st.diff.Layout() != editor.DiffSplit {
		t.Fatalf("default search diff layout = %v, want DiffSplit", st.diff.Layout())
	}
	if err := h.ui.Click("Unified"); err != nil {
		t.Fatalf("click Unified: %v", err)
	}
	h.ui.Frame()
	if st.diff.Layout() != editor.DiffUnified {
		t.Fatalf("layout after Unified click = %v, want DiffUnified", st.diff.Layout())
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, wantOther) {
		t.Fatalf("unified diff missing insertion %q: %q", wantOther, ins)
	}
}

// The segmented control switches the search dimension: author, hash, content
// and file searches each find their commit, regex is disabled for hash and
// file dimensions, and a dimension change with a query typed re-runs the
// search.
func TestCommitSearchDimensionsAndRegexDisabled(t *testing.T) {
	repo := newGitRepo(t, "search-dims")
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.WriteFile("src/alpha.go", "package src\n\n// searchable-token\n")
	repo.Git("add", "src/alpha.go")
	repo.Git("commit", "--author", "Grace Hopper <grace@example.com>", "-m", "needle commit")
	needle := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openSearchView(t, h)

	// Author dimension: switching the segment with a query typed re-runs
	// the search.
	typeSearchQuery(t, h, "grace@example.com")
	if err := h.ui.Click("Author"); err != nil {
		t.Fatalf("click Author segment: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.drain()
	st := searchState(h)
	if len(st.results) != 1 || st.results[0].Subject != "needle commit" {
		t.Fatalf("author results = %q, want [needle commit]", searchResultSubjects(st))
	}

	// Hash dimension: the regex checkbox is disabled, and the prefix finds
	// the commit.
	typeSearchQuery(t, h, needle[:12])
	if err := h.ui.Click("Hash"); err != nil {
		t.Fatalf("click Hash segment: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if len(st.results) != 1 || st.results[0].Hash != needle {
		t.Fatalf("hash results = %q, want [needle commit]", searchResultSubjects(st))
	}
	if cb, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Regex"); !ok || !cb.Disabled {
		t.Fatalf("Regex checkbox on Hash = %+v (found=%v), want disabled", cb, ok)
	}

	// Content dimension: regex is enabled again and the added line matches.
	typeSearchQuery(t, h, "searchable-token")
	if err := h.ui.Click("Content"); err != nil {
		t.Fatalf("click Content segment: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if len(st.results) != 1 || st.results[0].Hash != needle {
		t.Fatalf("content results = %q, want [needle commit]", searchResultSubjects(st))
	}
	if cb, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Regex"); !ok || cb.Disabled {
		t.Fatalf("Regex checkbox on Content = %+v (found=%v), want enabled", cb, ok)
	}

	// File dimension: regex is disabled, and the file path part matches.
	typeSearchQuery(t, h, "alpha.go")
	if err := h.ui.Click("File"); err != nil {
		t.Fatalf("click File segment: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if len(st.results) != 1 || st.results[0].Hash != needle {
		t.Fatalf("file results = %q, want [needle commit]", searchResultSubjects(st))
	}
	if cb, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Regex"); !ok || !cb.Disabled {
		t.Fatalf("Regex checkbox on File = %+v (found=%v), want disabled", cb, ok)
	}
}

// Match case and Regex change what the search matches, and toggling them
// with a query typed re-runs the search: a case-sensitive needle search
// skips the uppercase commit, and a regex dot matches what the fixed-string
// default does not.
func TestCommitSearchMatchCaseAndRegex(t *testing.T) {
	repo := newGitRepo(t, "search-case")
	repo.CommitFile("a.txt", "a\n", "Add NEEDLE uppercase")
	repo.CommitFile("b.txt", "b\n", "add needle lowercase")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openSearchView(t, h)

	// Case-insensitive by default: both commits match.
	submitSearchQuery(t, h, "needle")
	st := searchState(h)
	if len(st.results) != 2 {
		t.Fatalf("case-insensitive results = %q, want 2 commits", searchResultSubjects(st))
	}

	// Toggling Match case re-runs the search: only the lowercase commit
	// matches now.
	if err := h.ui.Click("Match case"); err != nil {
		t.Fatalf("click Match case: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if len(st.results) != 1 || st.results[0].Subject != "add needle lowercase" {
		t.Fatalf("match-case results = %q, want [add needle lowercase]", searchResultSubjects(st))
	}

	// A fixed-string search treats the dot literally: nothing matches.
	submitSearchQuery(t, h, "need.e")
	if len(st.results) != 0 {
		t.Fatalf("fixed-string results = %q, want none", searchResultSubjects(st))
	}
	if !h.ui.HasText("No commits match") {
		t.Fatalf("missing the no-match state; texts=%q", h.ui.Texts())
	}

	// With Regex on, the dot matches any character.
	if err := h.ui.Click("Regex"); err != nil {
		t.Fatalf("click Regex: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if len(st.results) != 1 || st.results[0].Subject != "add needle lowercase" {
		t.Fatalf("regex results = %q, want [add needle lowercase]", searchResultSubjects(st))
	}
}

// The Current branch checkbox limits the search to HEAD: a commit only on
// another branch drops out of the results, and on a detached HEAD the
// checkbox is disabled.
func TestCommitSearchCurrentBranchScope(t *testing.T) {
	repo := newGitRepo(t, "search-branch")
	repo.CommitFile("a.txt", "a\n", "needle on main")
	repo.CreateBranch("side")
	repo.Checkout("side")
	repo.CommitFile("s.txt", "s\n", "needle on side")
	repo.Checkout("main")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openSearchView(t, h)

	// Without the checkbox the search spans every branch: both commits.
	submitSearchQuery(t, h, "needle")
	st := searchState(h)
	if len(st.results) != 2 {
		t.Fatalf("all-branch results = %q, want 2 commits", searchResultSubjects(st))
	}

	// Current branch keeps only the commit reachable from HEAD (main).
	if err := h.ui.Click("Current branch"); err != nil {
		t.Fatalf("click Current branch: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if len(st.results) != 1 || st.results[0].Subject != "needle on main" {
		t.Fatalf("current-branch results = %q, want [needle on main]", searchResultSubjects(st))
	}

	// Detached HEAD: the checkbox is disabled.
	detach := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.Git("checkout", "--detach", detach)
	h.app.session.Reload()
	h.drain()
	if cb, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Current branch"); !ok || !cb.Disabled {
		t.Fatalf("Current branch checkbox on detached HEAD = %+v (found=%v), want disabled", cb, ok)
	}
}

// The path filter keeps the search to commits that changed it, and Added or
// removed switches the content search from -G to -S: a modification that
// keeps the line count of the needle unchanged matches -G but not -S.
func TestCommitSearchPathFilterAndPickaxe(t *testing.T) {
	repo := newGitRepo(t, "search-path")
	repo.CommitFile("src/deep/keep.txt", "keep\nalpha beta\n", "introduce alpha")
	repo.WriteFile("src/deep/keep.txt", "keep\nalpha gamma\n")
	repo.Git("add", "src/deep/keep.txt")
	repo.Git("commit", "-m", "alpha count unchanged")
	repo.CommitFile("other/unrelated.txt", "alpha beta\nalpha gamma\n", "alpha elsewhere")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openSearchView(t, h)

	// Content -G over the whole tree: all three commits touch alpha lines.
	typeSearchQuery(t, h, "alpha")
	if err := h.ui.Click("Content"); err != nil {
		t.Fatalf("click Content segment: %v", err)
	}
	h.ui.Frame()
	h.drain()
	st := searchState(h)
	if len(st.results) != 3 {
		t.Fatalf("-G results = %q, want 3 commits", searchResultSubjects(st))
	}

	// Added or removed (-S): the count of alpha lines changes only in the
	// first and the third commit; the modification keeping one alpha line
	// drops out.
	if err := h.ui.Click("Added or removed"); err != nil {
		t.Fatalf("click Added or removed: %v", err)
	}
	h.ui.Frame()
	h.drain()
	got := searchResultSubjects(st)
	if len(got) != 2 || !slices.Contains(got, "introduce alpha") || !slices.Contains(got, "alpha elsewhere") {
		t.Fatalf("-S results = %q, want [introduce alpha alpha elsewhere]", got)
	}

	// The path filter keeps the search to commits that changed src/deep:
	// alpha elsewhere drops out.
	if err := h.ui.Click("Filter path"); err != nil {
		t.Fatalf("click Filter path: %v", err)
	}
	h.ui.Type("src/deep")
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()
	if got := searchResultSubjects(st); len(got) != 1 || got[0] != "introduce alpha" {
		t.Fatalf("path-filtered results = %q, want [introduce alpha]", got)
	}

	// Clearing the path filter brings the unfiltered results back.
	if err := h.ui.Click("Filter path"); err != nil {
		t.Fatalf("click Filter path: %v", err)
	}
	h.ui.Command("selectAll")
	h.ui.Key(0, ui.KeyBackspace)
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()
	if len(st.results) != 2 {
		t.Fatalf("unfiltered results = %q, want 2 commits", searchResultSubjects(st))
	}
}

// More Results pages through the matches of the same search: each click
// appends the next page without duplicates, until a short page ends the
// pagination and the button disappears.
func TestCommitSearchPaginationMoreResults(t *testing.T) {
	repo := newGitRepo(t, "search-pages")
	makeCommits(t, repo, "f.txt", "matchme", 8)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openSearchView(t, h)

	st := searchState(h)
	st.pageSize = 3
	submitSearchQuery(t, h, "matchme")

	if len(st.results) != 3 {
		t.Fatalf("first page results = %d, want 3", len(st.results))
	}
	if !st.more {
		t.Fatal("more = false after the first full page, want true")
	}
	if !h.ui.HasText("First 3 results") {
		t.Fatalf("missing First-N count; texts=%q", h.ui.Texts())
	}

	if err := h.ui.Click("More Results"); err != nil {
		t.Fatalf("click More Results: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if len(st.results) != 6 {
		t.Fatalf("second page results = %d, want 6", len(st.results))
	}
	if !st.more {
		t.Fatal("more = false after the second full page, want true")
	}

	if err := h.ui.Click("More Results"); err != nil {
		t.Fatalf("click More Results again: %v", err)
	}
	h.drain()
	if len(st.results) != 8 {
		t.Fatalf("final results = %d, want all 8", len(st.results))
	}
	if st.more {
		t.Fatal("more still true after the short final page")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "More Results"); ok {
		t.Fatal("More Results still offered after the final page")
	}
	if uniqueResultHashes(st) != 8 {
		t.Fatalf("results contain duplicates: %q", searchResultSubjects(st))
	}
	if !h.ui.HasText("8 results") {
		t.Fatalf("missing final result count; texts=%q", h.ui.Texts())
	}
}

// A search that fails shows the failure with the error text and a Retry
// Search button; once the repository is readable again, Retry searches anew.
func TestCommitSearchFailureAndRetry(t *testing.T) {
	repo := newGitRepo(t, "search-retry")
	repo.CommitFile("z.txt", "z\n", "retry needle")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openSearchView(t, h)

	objects := repo.Path() + "/.git/objects"
	backup := repo.Path() + "/.git/objects-backup"
	if err := os.Rename(objects, backup); err != nil {
		t.Fatalf("hide objects: %v", err)
	}
	submitSearchQuery(t, h, "needle")

	st := searchState(h)
	if st.err == nil {
		t.Fatal("expected a search error with missing objects")
	}
	if !h.ui.HasText("Failed to search commits") {
		t.Fatalf("missing the search failure state; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Retry Search") {
		t.Fatalf("missing Retry Search; texts=%q", h.ui.Texts())
	}

	if err := os.Rename(backup, objects); err != nil {
		t.Fatalf("restore objects: %v", err)
	}
	if err := h.ui.Click("Retry Search"); err != nil {
		t.Fatalf("click Retry Search: %v", err)
	}
	h.drain()
	if st.err != nil {
		t.Fatalf("search error after retry: %v", st.err)
	}
	if len(st.results) != 1 || st.results[0].Subject != "retry needle" {
		t.Fatalf("results after retry = %q, want [retry needle]", searchResultSubjects(st))
	}
}

// A slower search started first must never overwrite the results of the
// faster search started after it: the superseded search's late completion
// is discarded.
func TestCommitSearchRapidSearchesDiscardStaleResults(t *testing.T) {
	repo := newGitRepo(t, "search-race")
	repo.CommitFile("a.txt", "a\n", "add needle lowercase")
	repo.CommitFile("b.txt", "b\n", "Add NEEDLE uppercase")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openSearchView(t, h)

	// Search A (case-insensitive, matches both commits) is started and held
	// back; search B (match case, matches only the lowercase commit) starts
	// after it and completes first.
	h.queue.PauseWorkers()
	typeSearchQuery(t, h, "needle")
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	workerA := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	if err := h.ui.Click("Match case"); err != nil {
		t.Fatalf("click Match case: %v", err)
	}
	h.ui.Frame()
	h.queue.RunNext(t) // B's completion applies first
	h.ui.Frame()

	st := searchState(h)
	if len(st.results) != 1 || st.results[0].Subject != "add needle lowercase" {
		t.Fatalf("results after fast search = %q, want [add needle lowercase]", searchResultSubjects(st))
	}

	h.queue.ReleaseWorker(workerA)
	h.drain()

	if len(st.results) != 1 || st.results[0].Subject != "add needle lowercase" {
		t.Fatalf("stale search overwrote the newer results: %q", searchResultSubjects(st))
	}
}

// Rapidly switching the selected search result discards the slower first
// detail read: the detail of the result chosen last stays.
func TestCommitSearchRapidResultSwitchDiscardsStaleDetail(t *testing.T) {
	repo := newGitRepo(t, "search-detail-race")
	hashA := repo.CommitFile("a.txt", "alpha\n", "needle alpha")
	hashB := repo.CommitFile("b.txt", "beta\n", "needle beta")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openSearchView(t, h)
	submitSearchQuery(t, h, "needle")
	if got := len(searchState(h).results); got != 2 {
		t.Fatalf("results = %d, want 2", got)
	}

	h.queue.PauseWorkers()
	if err := h.ui.Click(searchRowLabel(hashA, "needle alpha")); err != nil {
		t.Fatalf("click alpha result: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	workerA := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	if err := h.ui.Click(searchRowLabel(hashB, "needle beta")); err != nil {
		t.Fatalf("click beta result: %v", err)
	}
	h.queue.RunNext(t) // beta's detail applies first
	h.ui.Frame()

	st := searchState(h)
	if !strings.HasPrefix(st.detail.Message, "needle beta") {
		t.Fatalf("detail after fast click = %q, want needle beta", st.detail.Message)
	}

	h.queue.ReleaseWorker(workerA)
	h.drain()

	if !strings.HasPrefix(st.detail.Message, "needle beta") {
		t.Fatalf("stale detail overwrote beta: message = %q", st.detail.Message)
	}
	if len(st.files) != 1 || st.files[0].Path != "b.txt" {
		t.Fatalf("file list = %+v, want exactly [b.txt]", st.files)
	}
}

// Show in History closes the search, grows the loaded history page by page
// until the commit shows, and selects, centers and details it there.
func TestCommitSearchShowInHistoryAcrossPages(t *testing.T) {
	repo := newGitRepo(t, "search-reveal")
	target := repo.CommitFile("old.txt", "old needle\n", "needle ancient commit")
	makeCommits(t, repo, "f.txt", "commit", 8)

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Narrow the first history page so the ancient commit is past it, the
	// same hook the Load More tests use.
	h.app.session.logLimit = 3
	h.app.session.Reload()
	h.drain()

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()
	if got := len(h.app.session.Snapshot().Commits); got != 3 || !h.app.session.Snapshot().More {
		t.Fatalf("first page = %d commits (More=%v), want 3 (More=true)", got, h.app.session.Snapshot().More)
	}
	loaded := func() bool {
		for _, cm := range h.app.session.Snapshot().Commits {
			if cm.Hash == target {
				return true
			}
		}
		return false
	}
	if loaded() {
		t.Fatal("target commit unexpectedly in the first page: the test proves nothing")
	}

	openSearchView(t, h)
	submitSearchQuery(t, h, "ancient")
	st := searchState(h)
	if len(st.results) != 1 || st.results[0].Hash != target {
		t.Fatalf("results = %q, want the ancient commit", searchResultSubjects(st))
	}

	if err := h.ui.Click("Show in History"); err != nil {
		t.Fatalf("click Show in History: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.drain()
	h.drain() // the reveal selects the commit inside a frame; drain its detail chain too

	if st.open {
		t.Fatal("search view still open after Show in History")
	}
	if !loaded() {
		t.Fatal("history did not grow to the page holding the target commit")
	}
	if st.pendingRevealHash != "" {
		t.Fatalf("pending reveal not cleared: %q", st.pendingRevealHash)
	}
	hv := &h.app.historyView
	if hv.sel != target {
		t.Fatalf("history selection = %q, want %q", hv.sel, target)
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(target, "needle ancient commit")); !ok || !row.Selected {
		t.Fatalf("revealed commit row access = %+v (found=%v), want Selected=true", row, ok)
	}
	if !h.ui.HasText("old.txt") {
		t.Fatalf("revealed commit files not loaded; texts=%q", h.ui.Texts())
	}
}

// A reveal that cannot locate its commit within the page cap ends with a
// clear toast instead of loading forever or failing silently.
func TestCommitSearchRevealNotFoundShowsToast(t *testing.T) {
	repo := newGitRepo(t, "search-reveal-miss")
	target := repo.CommitFile("old.txt", "old needle\n", "needle ancient commit")
	makeCommits(t, repo, "f.txt", "commit", 6)

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	h.app.session.logLimit = 3
	h.app.session.Reload()
	h.drain()

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()

	openSearchView(t, h)
	submitSearchQuery(t, h, "ancient")
	st := searchState(h)
	if len(st.results) != 1 || st.results[0].Hash != target {
		t.Fatalf("results = %q, want the ancient commit", searchResultSubjects(st))
	}

	// Cap the reveal at zero extra pages: the target — past the narrowed
	// first page — stays out of reach, as it does in a history longer than
	// the reveal is allowed to load.
	st.revealMaxSteps = 0
	if err := h.ui.Click("Show in History"); err != nil {
		t.Fatalf("click Show in History: %v", err)
	}
	h.ui.Frame()
	h.drain()
	h.ui.Frame()
	h.drain()

	hv := &h.app.historyView
	if hv.sel == target {
		t.Fatal("the unrevealable commit was selected after all")
	}
	if st.pendingRevealHash != "" {
		t.Fatalf("pending reveal not cleared after the cap: %q", st.pendingRevealHash)
	}
	if !h.ui.HasText("Could not find the commit in the loaded history.") {
		t.Fatalf("missing the not-found toast; texts=%q", h.ui.Texts())
	}
}

// Show in History is also the context menu of a result row; when the commit
// is already in the loaded history, it is selected without loading more.
func TestCommitSearchContextMenuShowInHistory(t *testing.T) {
	repo := newGitRepo(t, "search-menu")
	needle := repo.CommitFile("n.txt", "needle\n", "needle menu commit")
	repo.CommitFile("f.txt", "f\n", "other commit")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openSearchView(t, h)
	submitSearchQuery(t, h, "menu")

	st := searchState(h)
	if len(st.results) != 1 || st.results[0].Hash != needle {
		t.Fatalf("results = %q, want [needle menu commit]", searchResultSubjects(st))
	}
	limitBefore := h.app.session.logLimit

	if err := h.ui.RightClick(searchRowLabel(needle, "needle menu commit")); err != nil {
		t.Fatalf("right-click result row: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if menu := h.ui.Menu(); !slices.Contains(menu, "Show in History") {
		t.Fatalf("context menu = %q, want Show in History", menu)
	}
	if err := h.ui.ChooseMenuItem("Show in History"); err != nil {
		t.Fatalf("choose Show in History: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if st.open {
		t.Fatal("search view still open after the context menu jump")
	}
	if h.app.historyView.sel != needle {
		t.Fatalf("history selection = %q, want %q", h.app.historyView.sel, needle)
	}
	if h.app.session.logLimit != limitBefore {
		t.Fatalf("history grew although the commit was loaded: limit %d -> %d", limitBefore, h.app.session.logLimit)
	}
}

// Choosing a commit row in the history itself cancels a pending reveal, so
// it cannot take the selection back afterwards.
func TestCommitSearchManualSelectionCancelsReveal(t *testing.T) {
	repo := newGitRepo(t, "search-cancel")
	target := repo.CommitFile("old.txt", "old needle\n", "needle ancient commit")
	top := repo.CommitFile("f.txt", "f\n", "commit newer")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	h.app.session.logLimit = 1
	h.app.session.Reload()
	h.drain()

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()

	openSearchView(t, h)
	submitSearchQuery(t, h, "ancient")
	st := searchState(h)
	if len(st.results) != 1 || st.results[0].Hash != target {
		t.Fatalf("results = %q, want the ancient commit", searchResultSubjects(st))
	}

	if err := h.ui.Click("Show in History"); err != nil {
		t.Fatalf("click Show in History: %v", err)
	}
	h.ui.Frame()
	if st.pendingRevealHash == "" {
		t.Fatal("no pending reveal after Show in History")
	}

	// The user picks the top row before the reveal resolves; the reveal
	// must not steal the selection back.
	clickCommitRow(t, h, top, "commit newer")
	h.drain()
	h.ui.Frame()
	h.drain()

	if st.pendingRevealHash != "" {
		t.Fatalf("pending reveal not cancelled by the manual selection: %q", st.pendingRevealHash)
	}
	if h.app.historyView.sel != top {
		t.Fatalf("history selection = %q, want the manually chosen %q", h.app.historyView.sel, top)
	}
}

// Switching repositories resets the whole search: query, results, selection,
// detail and diff carry nothing over from the previous repository.
func TestCommitSearchSwitchRepoResetsState(t *testing.T) {
	repoA := newGitRepo(t, "search-swap-a")
	repoA.CommitFile("only-a.txt", "a needle\n", "needle in alpha")
	repoB := newGitRepo(t, "search-swap-b")
	repoB.CommitFile("only-b.txt", "b\n", "commit in beta")

	h := newTestApp(t, repoB.StoreRepo())
	openHistoryPage(t, h, repoA.StoreRepo())
	openSearchView(t, h)
	submitSearchQuery(t, h, "needle")

	st := searchState(h)
	if len(st.results) != 1 {
		t.Fatalf("setup: results = %q, want 1", searchResultSubjects(st))
	}
	if st.selHash == "" {
		t.Fatal("setup: no result selected")
	}
	genBefore := h.app.session.Generation()

	// Switch to the other repository through the Repository Switcher UI.
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("search-swap-b"); err != nil {
		t.Fatalf("select search-swap-b: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if h.app.session.Generation() == genBefore {
		t.Fatal("session generation did not advance on the repository switch")
	}
	st = searchState(h)
	switch {
	case st.open:
		t.Error("search view still open after the repository switch")
	case st.query != "":
		t.Errorf("query not reset: %q", st.query)
	case len(st.results) != 0:
		t.Errorf("results not reset: %q", searchResultSubjects(st))
	case st.searched:
		t.Error("searched flag not reset")
	case st.selHash != "":
		t.Errorf("selection not reset: %q", st.selHash)
	case len(st.files) != 0:
		t.Errorf("files not reset: %+v", st.files)
	case st.diff.req.Kind != diffNone:
		t.Errorf("diff not reset: %+v", st.diff.req)
	case st.pendingRevealHash != "":
		t.Errorf("pending reveal not reset: %q", st.pendingRevealHash)
	}
	if h.ui.HasText("needle in alpha") {
		t.Fatalf("stale result still rendered; texts=%q", h.ui.Texts())
	}
}

// --- SSH loopback ---

// importSSHHistory appends n fast-imported commits on top of the remote
// repository's initial commit: the oldest imported one ("needle-old" by
// Grace Hopper, touching needle/secret.txt) and n-1 filler commits by Ada
// Lovelace rewriting filler.txt. It returns the needle commit's hash.
func importSSHHistory(t *testing.T, srv *sshtest.Target, remoteDir string, n int) string {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		var subject, path, content, author string
		if i == 1 {
			subject = "needle-old"
			path = "needle/secret.txt"
			content = "needle haystack secret\n"
			author = "Grace Hopper <grace@example.com>"
		} else {
			subject = fmt.Sprintf("filler %d", i)
			path = "filler.txt"
			content = fmt.Sprintf("filler content %d\n", i)
			author = "Ada Lovelace <ada@example.com>"
		}
		fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\n", i)
		fmt.Fprintf(&b, "author %s %d +0000\n", author, 1700000000+i)
		fmt.Fprintf(&b, "committer %s %d +0000\n", author, 1700000000+i)
		fmt.Fprintf(&b, "data %d\n%s\n", len(subject), subject)
		if i == 1 {
			head := strings.TrimSpace(srv.RunGit(t, remoteDir, "rev-parse", "HEAD"))
			fmt.Fprintf(&b, "from %s\n", head)
		} else {
			fmt.Fprintf(&b, "from :%d\n", i-1)
		}
		fmt.Fprintf(&b, "M 100644 inline %s\ndata %d\n%s\n", path, len(content), content)
	}
	srv.RunShellInput(t, "cd "+git.ShellQuote(remoteDir)+" && GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git fast-import --quiet", []byte(b.String()))
	srv.RunGit(t, remoteDir, "reset", "-q", "--hard", "main")
	return strings.TrimSpace(srv.RunGit(t, remoteDir, "rev-parse", "HEAD~"+fmt.Sprint(n-1)))
}

// Over a real loopback SSH connection, the commit search covers all five
// dimensions, the path filter, pagination, the no-match state, the detail
// diff and a cross-page Show in History reveal, without ever changing the
// remote worktree or its refs. The history is longer than the search page
// size, so pagination and the reveal walk real pages.
func TestCommitSearchLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)

	// 520 imported commits + the initial one: longer than the 500-commit
	// search page and, with the history page narrowed to 500, longer than
	// the first history page, so the needle commit sits beyond both.
	needleHash := importSSHHistory(t, srv, repo.Path, 520)
	total := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-list", "--count", "HEAD"))
	if total != "521" {
		t.Fatalf("remote commit count = %s, want 521", total)
	}
	statusBefore := srv.RunGit(t, repo.Path, "status", "--porcelain=v1")
	if statusBefore != "" {
		t.Fatalf("remote worktree dirty before the test: %q", statusBefore)
	}
	refsBefore := srv.RunGit(t, repo.Path, "for-each-ref", "--format=%(refname) %(objectname)")

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

	// Narrow the history page to the same 500 commits the search pages in,
	// so the needle commit is past the first page of both.
	h.app.session.logLimit = 500
	h.app.session.Reload()
	h.drain()

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()
	if got := len(h.app.session.Snapshot().Commits); got != 500 || !h.app.session.Snapshot().More {
		t.Fatalf("SSH first page = %d commits (More=%v), want 500 (More=true)", got, h.app.session.Snapshot().More)
	}
	loadedInHistory := func() bool {
		for _, cm := range h.app.session.Snapshot().Commits {
			if cm.Hash == needleHash {
				return true
			}
		}
		return false
	}
	if loadedInHistory() {
		t.Fatal("needle commit unexpectedly in the first history page: the test proves nothing")
	}

	openSearchView(t, h)
	st := searchState(h)

	// Message dimension: the needle commit is past the first history page
	// but the search walks all of it.
	submitSearchQuery(t, h, "needle")
	if st.err != nil {
		t.Fatalf("SSH search error: %v", st.err)
	}
	if len(st.results) != 1 || st.results[0].Hash != needleHash {
		t.Fatalf("SSH message results = %q, want [needle-old]", searchResultSubjects(st))
	}
	// The auto-selected result's detail, file list and diff load over SSH.
	if st.detailErr != nil {
		t.Fatalf("SSH detail error: %v", st.detailErr)
	}
	if !h.ui.HasText(needleHash) {
		t.Fatalf("SSH detail missing full hash; texts=%q", h.ui.Texts())
	}
	if len(st.files) != 1 || st.files[0].Path != "needle/secret.txt" {
		t.Fatalf("SSH files = %+v, want exactly [needle/secret.txt]", st.files)
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "needle haystack secret") {
		t.Fatalf("SSH diff insertions = %q, want needle haystack secret", ins)
	}

	// Author dimension: Ada wrote every filler commit, more than one page.
	typeSearchQuery(t, h, "ada@example.com")
	if err := h.ui.Click("Author"); err != nil {
		t.Fatalf("click Author segment: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if len(st.results) != 500 {
		t.Fatalf("SSH author first page = %d results, want 500", len(st.results))
	}
	if !st.more {
		t.Fatal("SSH author search more = false, want true with 519 matches")
	}
	if !h.ui.HasText("First 500 results") {
		t.Fatalf("missing First-N count over SSH; texts=%q", h.ui.Texts())
	}

	// Pagination: More Results appends the rest without duplicates.
	if err := h.ui.Click("More Results"); err != nil {
		t.Fatalf("click More Results: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if len(st.results) != 519 {
		t.Fatalf("SSH results after More Results = %d, want 519", len(st.results))
	}
	if uniqueResultHashes(st) != 519 {
		t.Fatalf("SSH results contain duplicates: %d unique of %d", uniqueResultHashes(st), len(st.results))
	}
	if st.more {
		t.Fatal("more still true after the final page over SSH")
	}

	// The path filter keeps the author search to commits changing needle/:
	// Ada never touched it, Grace wrote only it.
	if err := h.ui.Click("Filter path"); err != nil {
		t.Fatalf("click Filter path: %v", err)
	}
	h.ui.Type("needle")
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()
	if len(st.results) != 0 {
		t.Fatalf("path-filtered Ada results = %q, want none", searchResultSubjects(st))
	}
	if !h.ui.HasText("No commits match") {
		t.Fatalf("missing the no-match state over SSH; texts=%q", h.ui.Texts())
	}
	typeSearchQuery(t, h, "grace@example.com")
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()
	if len(st.results) != 1 || st.results[0].Hash != needleHash {
		t.Fatalf("path-filtered Grace results = %q, want [needle-old]", searchResultSubjects(st))
	}
	// Clear the filter for the next sections.
	if err := h.ui.Click("Filter path"); err != nil {
		t.Fatalf("click Filter path: %v", err)
	}
	h.ui.Command("selectAll")
	h.ui.Key(0, ui.KeyBackspace)
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()
	if len(st.results) != 1 || st.results[0].Hash != needleHash {
		t.Fatalf("unfiltered Grace results = %q, want [needle-old]", searchResultSubjects(st))
	}

	// Hash dimension: a prefix of the needle hash finds it past the page.
	typeSearchQuery(t, h, needleHash[:12])
	if err := h.ui.Click("Hash"); err != nil {
		t.Fatalf("click Hash segment: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if len(st.results) != 1 || st.results[0].Hash != needleHash {
		t.Fatalf("SSH hash results = %q, want [needle-old]", searchResultSubjects(st))
	}

	// Content dimension, -G and -S: the haystack line was added once by the
	// needle commit, so both pickaxe modes find exactly it.
	typeSearchQuery(t, h, "haystack secret")
	if err := h.ui.Click("Content"); err != nil {
		t.Fatalf("click Content segment: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if len(st.results) != 1 || st.results[0].Hash != needleHash {
		t.Fatalf("SSH -G results = %q, want [needle-old]", searchResultSubjects(st))
	}
	if err := h.ui.Click("Added or removed"); err != nil {
		t.Fatalf("click Added or removed: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if len(st.results) != 1 || st.results[0].Hash != needleHash {
		t.Fatalf("SSH -S results = %q, want [needle-old]", searchResultSubjects(st))
	}
	// -G and -S differ on the filler lines: every filler commit rewrites a
	// line matching -G, but only the commit introducing filler.txt changes
	// how many times the text occurs.
	typeSearchQuery(t, h, "filler content")
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()
	if got := searchResultSubjects(st); len(got) != 1 || got[0] != "filler 2" {
		t.Fatalf("SSH -S filler results = %q, want exactly [filler 2]", got)
	}
	if err := h.ui.Click("Added or removed"); err != nil {
		t.Fatalf("uncheck Added or removed: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if len(st.results) != 500 {
		t.Fatalf("SSH -G filler first page = %d results, want 500", len(st.results))
	}

	// File dimension: the needle file's path part matches.
	typeSearchQuery(t, h, "secret.txt")
	if err := h.ui.Click("File"); err != nil {
		t.Fatalf("click File segment: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if len(st.results) != 1 || st.results[0].Hash != needleHash {
		t.Fatalf("SSH file results = %q, want [needle-old]", searchResultSubjects(st))
	}

	// No-match search.
	submitSearchQuery(t, h, "zz-no-such-commit")
	if len(st.results) != 0 {
		t.Fatalf("SSH no-match results = %q, want none", searchResultSubjects(st))
	}
	if !h.ui.HasText("No commits match") {
		t.Fatalf("missing the no-match state over SSH; texts=%q", h.ui.Texts())
	}

	// Show in History across the first page: the needle commit sits at
	// position 519 of 521, past the 500-commit first page; the reveal loads
	// the rest of the history and selects it there.
	typeSearchQuery(t, h, "needle")
	if err := h.ui.Click("Message"); err != nil {
		t.Fatalf("click Message segment: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if len(st.results) != 1 || st.results[0].Hash != needleHash {
		t.Fatalf("SSH reveal search results = %q, want [needle-old]", searchResultSubjects(st))
	}
	if err := h.ui.Click("Show in History"); err != nil {
		t.Fatalf("click Show in History: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.drain()
	h.drain() // the reveal selects the commit inside a frame; drain its detail chain too

	if st.open {
		t.Fatal("search view still open after Show in History over SSH")
	}
	if !loadedInHistory() {
		t.Fatal("SSH history did not grow to the page holding the needle commit")
	}
	if st.pendingRevealHash != "" {
		t.Fatalf("pending reveal not cleared over SSH: %q", st.pendingRevealHash)
	}
	hv := &h.app.historyView
	if hv.sel != needleHash {
		t.Fatalf("SSH history selection = %q, want %q", hv.sel, needleHash)
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(needleHash, "needle-old")); !ok || !row.Selected {
		t.Fatalf("revealed SSH commit row access = %+v (found=%v), want Selected=true", row, ok)
	}
	if hv.detailErr != nil {
		t.Fatalf("SSH revealed detail error: %v", hv.detailErr)
	}
	if len(hv.files) != 1 || hv.files[0].Path != "needle/secret.txt" {
		t.Fatalf("SSH revealed files = %+v, want exactly [needle/secret.txt]", hv.files)
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "needle haystack secret") {
		t.Fatalf("SSH revealed diff insertions = %q, want needle haystack secret", ins)
	}

	// The whole search is read-only: the remote worktree and refs are
	// byte-identical to before.
	if got := srv.RunGit(t, repo.Path, "status", "--porcelain=v1"); got != statusBefore {
		t.Fatalf("remote worktree changed by the search: %q -> %q", statusBefore, got)
	}
	if got := srv.RunGit(t, repo.Path, "for-each-ref", "--format=%(refname) %(objectname)"); got != refsBefore {
		t.Fatalf("remote refs changed by the search: %q -> %q", refsBefore, got)
	}
}
