package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/store"
)

// rowsFixtureContent builds the file the diff option tests share: rows that
// count themselves, so the lines a diff shows read directly against the rows
// git diff -U prints. rowChange edits one row in the middle, keeping rows of
// unchanged lines before and after it.
func rowsFixtureContent(n, changeAt int, edited bool) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if i == changeAt && edited {
			fmt.Fprintf(&b, "row %02d edited\n", i)
		} else {
			fmt.Fprintf(&b, "row %02d\n", i)
		}
	}
	return b.String()
}

// diffFixtureLines collects the fixture rows the rendered diff shows, as a
// set: the split view shows unchanged rows on both sides, so containment is
// what a row's presence means. Only rows in view have accessibility nodes,
// which is why the fixtures stay small enough to fit the viewport.
func diffFixtureLines(tt *ui.Tester) map[string]bool {
	out := map[string]bool{}
	for _, a := range tt.AccessAll() {
		if strings.HasPrefix(a.Name, "row ") {
			out[a.Name] = true
		}
	}
	return out
}

// udiffLineSet parses the rows a unified diff prints: the content of every
// hunk line, context and changes alike. Comparing against git diff -U<n>
// output is what checks the rendered coverage against git's own.
func udiffLineSet(raw string) map[string]bool {
	out := map[string]bool{}
	inHunk := false
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHunk = false
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case !inHunk || line == "":
		case strings.HasPrefix(line, " "), strings.HasPrefix(line, "+"), strings.HasPrefix(line, "-"):
			if strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") {
				continue
			}
			if strings.HasPrefix(line[1:], "row ") {
				out[line[1:]] = true
			}
		}
	}
	return out
}

// writeRowsFixture commits the rows file at base and rewrites it with the
// middle row edited, leaving an unstaged modification.
func writeRowsFixture(t *testing.T, repo *gitRepoFixture, n int) {
	t.Helper()
	repo.CommitFile("rows.txt", rowsFixtureContent(n, n/2, false), "base")
	repo.WriteFile("rows.txt", rowsFixtureContent(n, n/2, true))
}

// openRowsDiff returns the harness of the rows fixture with its unstaged
// diff open in the Changes page.
func openRowsDiff(t *testing.T, n int) (*gitRepoFixture, *testHarness) {
	t.Helper()
	repo := newGitRepo(t, "diffopts-rows")
	writeRowsFixture(t, repo, n)
	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged rows.txt")
	h.drain()
	return repo, h
}

func diffOptionsButton(t *testing.T, h *testHarness, name string) ui.AccessInfo {
	t.Helper()
	btn, ok := h.ui.AccessByRole(ui.RoleButton, name)
	if !ok {
		t.Fatalf("%s missing from the diff header; texts=%q", name, h.ui.Texts())
	}
	return btn
}

// The entire-file choice is on in a fresh install: the diff of a change in
// the middle of a file shows every row, the first and the last included, and
// the coverage matches git's own output with a context wide enough. Toggling
// the header's Show Entire File button off narrows the diff to the change and
// its context — the first and last rows leave — and back on restores them.
func TestDiffOptionsEntireFileDefaultAndToggle(t *testing.T) {
	repo, h := openRowsDiff(t, 32)

	if btn := diffOptionsButton(t, h, "Show Entire File"); !btn.Checked {
		t.Fatalf("Show Entire File starts %+v, want on for a fresh install", btn)
	}
	got := diffFixtureLines(h.ui)
	want := udiffLineSet(repo.Git("diff", "-U1048576", "--", "rows.txt"))
	if len(want) != 33 {
		t.Fatalf("oracle sanity: git -U1048576 shows %d distinct rows, want all 33", len(want))
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		for row := range want {
			if !got[row] {
				t.Errorf("entire-file diff missing %q that git shows", row)
			}
		}
		for row := range got {
			if !want[row] {
				t.Errorf("entire-file diff shows %q that git does not", row)
			}
		}
		t.FailNow()
	}

	mustClick(t, h, "Show Entire File")
	h.drain()

	if btn := diffOptionsButton(t, h, "Show Entire File"); btn.Checked {
		t.Fatalf("Show Entire File after clicking = %+v, want off", btn)
	}
	got = diffFixtureLines(h.ui)
	for _, row := range []string{"row 01", "row 32"} {
		if got[row] {
			t.Errorf("changes-only diff still shows %q away from the change", row)
		}
	}
	want = udiffLineSet(repo.Git("diff", "-U3", "--", "rows.txt"))
	if fmt.Sprint(got) != fmt.Sprint(want) {
		missing, extra := []string{}, []string{}
		for row := range want {
			if !got[row] {
				missing = append(missing, row)
			}
		}
		for row := range got {
			if !want[row] {
				extra = append(extra, row)
			}
		}
		if len(missing)+len(extra) > 0 {
			t.Fatalf("changes-only coverage differs from git -U3: missing=%v extra=%v", missing, extra)
		}
	}

	mustClick(t, h, "Show Entire File")
	h.drain()
	got = diffFixtureLines(h.ui)
	for _, row := range []string{"row 01", "row 32"} {
		if !got[row] {
			t.Errorf("toggling back on did not restore %q", row)
		}
	}
}

// The context stepper walks its ladder: Show Entire File disables both ends
// of it; off, the coverage tracks git diff -U at every step — fewer rows at
// -U1, none but the change at -U0 — and each end disables once the ladder
// runs out.
func TestDiffOptionsContextStepperMatchesGit(t *testing.T) {
	repo, h := openRowsDiff(t, 32)

	for _, name := range []string{"Decrease Context Lines", "Increase Context Lines"} {
		if btn := diffOptionsButton(t, h, name); !btn.Disabled {
			t.Fatalf("%s = %+v while the entire file shows, want disabled", name, btn)
		}
	}

	mustClick(t, h, "Show Entire File")
	h.drain()
	if btn := diffOptionsButton(t, h, "Increase Context Lines"); btn.Disabled {
		t.Fatalf("Increase Context Lines = %+v with the default of 3, want enabled", btn)
	}

	assertCoverage := func(uarg, label string) {
		t.Helper()
		got := diffFixtureLines(h.ui)
		want := udiffLineSet(repo.Git("diff", uarg, "--", "rows.txt"))
		missing, extra := []string{}, []string{}
		for row := range want {
			if !got[row] {
				missing = append(missing, row)
			}
		}
		for row := range got {
			if !want[row] {
				extra = append(extra, row)
			}
		}
		if len(missing)+len(extra) > 0 {
			t.Fatalf("%s coverage differs from git %s: missing=%v extra=%v", label, uarg, missing, extra)
		}
	}
	assertCoverage("-U3", "default")

	// Up the ladder: 3 → 5 → 10, where the context starts swallowing the
	// rows before the change.
	mustClick(t, h, "Increase Context Lines")
	h.drain()
	assertCoverage("-U5", "one step up")
	mustClick(t, h, "Increase Context Lines")
	h.drain()
	assertCoverage("-U10", "two steps up")

	// Down past the start: 10 → 5 → 3 → 1 → 0, where only the changed rows
	// are left.
	for i := 0; i < 3; i++ {
		mustClick(t, h, "Decrease Context Lines")
		h.drain()
	}
	assertCoverage("-U1", "one line of context")
	if btn := diffOptionsButton(t, h, "Decrease Context Lines"); btn.Disabled {
		t.Fatalf("Decrease Context Lines = %+v at -U1, want enabled", btn)
	}

	mustClick(t, h, "Decrease Context Lines")
	h.drain()
	assertCoverage("-U0", "no context")
	if btn := diffOptionsButton(t, h, "Decrease Context Lines"); !btn.Disabled {
		t.Fatalf("Decrease Context Lines = %+v at -U0, want disabled at the ladder's end", btn)
	}
	if got := diffFixtureLines(h.ui); got["row 15"] || got["row 17"] {
		t.Fatalf("rows beside the change survive -U0: %v", got)
	}

	// Back up to the top of the ladder: 0 → 1 → 3 → 5 → 10 → 25, where the
	// context reaches past the file's start and Increase disables.
	for i := 0; i < 5; i++ {
		mustClick(t, h, "Increase Context Lines")
		h.drain()
	}
	assertCoverage("-U25", "top of the ladder")
	if btn := diffOptionsButton(t, h, "Increase Context Lines"); !btn.Disabled {
		t.Fatalf("Increase Context Lines = %+v at -U25, want disabled at the ladder's end", btn)
	}
}

// Wrap Lines re-renders the open diff without reloading it: the long row's
// lines grow taller where they fold, the loaded request is the same one, and
// toggling back restores the single-line height.
func TestDiffOptionsWrapLinesWithoutReload(t *testing.T) {
	repo := newGitRepo(t, "diffopts-wrap")
	long := strings.Repeat("x", 200)
	repo.CommitFile("rows.txt", rowsFixtureContent(20, 10, false), "base")
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		if i == 10 {
			fmt.Fprintf(&b, "row 10 edited %s\n", long)
		} else {
			fmt.Fprintf(&b, "row %02d\n", i)
		}
	}
	repo.WriteFile("rows.txt", b.String())

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged rows.txt")
	h.drain()

	longHeight := func() float32 {
		t.Helper()
		for _, a := range h.ui.AccessAll() {
			if strings.Contains(a.Name, "xxx") {
				return a.Bounds.H
			}
		}
		t.Fatalf("long row missing from the diff; texts=%q", h.ui.Texts())
		return 0
	}

	before := longHeight()
	loadedKey := h.app.changesView.diff.loadedKey
	stateBefore := h.app.changesView.diff.state
	if stateBefore == nil {
		t.Fatal("open diff missing its rendered state before toggling wrap")
	}
	if btn := diffOptionsButton(t, h, "Wrap Lines"); btn.Checked {
		t.Fatalf("Wrap Lines starts %+v, want off", btn)
	}

	mustClick(t, h, "Wrap Lines")
	if h.app.changesView.diff.loading {
		t.Fatal("wrapping started a diff reload")
	}
	h.drain()

	if btn := diffOptionsButton(t, h, "Wrap Lines"); !btn.Checked {
		t.Fatalf("Wrap Lines after clicking = %+v, want on", btn)
	}
	if !h.app.session.DiffWrap() {
		t.Fatal("session wrap choice off after clicking Wrap Lines")
	}
	if !h.store.Settings().DiffWrap {
		t.Fatal("store did not persist the wrap choice")
	}
	if got := longHeight(); got <= before {
		t.Fatalf("wrapped long row height = %.1f, want growth beyond the single-line %.1f", got, before)
	}
	if h.app.changesView.diff.loadedKey != loadedKey {
		t.Fatal("wrapping reloaded the diff: the loaded request key changed")
	}
	if h.app.changesView.diff.state != stateBefore {
		t.Fatal("wrapping replaced the diff state: the diff was reloaded")
	}

	mustClick(t, h, "Wrap Lines")
	if h.app.changesView.diff.loading {
		t.Fatal("unwrapping started a diff reload")
	}
	h.drain()
	if h.app.changesView.diff.state != stateBefore {
		t.Fatal("unwrapping replaced the diff state: the diff was reloaded")
	}
	if got := longHeight(); got != before {
		t.Fatalf("unwrapped long row height = %.1f, want the original %.1f", got, before)
	}
}

// The three choices are global: toggled in one page's diff header they show
// in the other's, they persist in the store, and an app built on the reopened
// store restores them; the zero values of a fresh store mean the entire
// file, the default context, and no wrapping.
func TestDiffOptionsPersistAcrossPagesAndRestore(t *testing.T) {
	repo := newGitRepo(t, "diffopts-persist")
	writeRowsFixture(t, repo, 16)
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	if h.app.session.DiffChangesOnly() {
		t.Fatal("a fresh install starts with the entire file off")
	}
	if got := h.app.session.DiffContextLines(); got != 3 {
		t.Fatalf("a fresh install starts with %d context lines, want the default of 3", got)
	}
	if h.app.session.DiffWrap() {
		t.Fatal("a fresh install starts with wrapping on")
	}

	// Load the unstaged diff in Local Changes first: the choices are changed
	// on the History page afterwards, and returning must reload this already
	// open diff with them, not just the buttons.
	h.openRepo(repo.StoreRepo())
	h.drain()
	mustClick(t, h, "Unstaged rows.txt")
	h.drain()
	if got := diffFixtureLines(h.ui); !got["row 01"] {
		t.Fatalf("Local Changes diff did not open with the entire file: %v", got)
	}

	mustClick(t, h, "History")
	h.ui.Frame()
	clickCommitRow(t, h, head, "base")
	h.drain()
	mustClick(t, h, fileRowLabel("rows.txt"))
	h.drain()

	mustClick(t, h, "Show Entire File")
	h.drain()
	mustClick(t, h, "Increase Context Lines")
	h.drain()
	mustClick(t, h, "Wrap Lines")
	h.drain()

	if !h.app.session.DiffChangesOnly() {
		t.Fatal("session changes-only off after clicking Show Entire File")
	}
	if got := h.app.session.DiffContextLines(); got != 5 {
		t.Fatalf("session context lines = %d after one step up, want 5", got)
	}
	settings := h.store.Settings()
	if !settings.DiffChangesOnly {
		t.Fatal("store did not persist the changes-only choice")
	}
	if settings.DiffContextLines != 5 || !settings.DiffContextSaved {
		t.Fatalf("store context = (%d, saved %v), want (5, true)", settings.DiffContextLines, settings.DiffContextSaved)
	}
	if !settings.DiffWrap {
		t.Fatal("store did not persist the wrap choice")
	}

	// The Local Changes page shares the same global choices, and its open
	// diff reloads with them: the rows beyond -U5 of the change leave.
	mustClick(t, h, "Changes")
	h.ui.Frame()
	h.drain()
	if btn := diffOptionsButton(t, h, "Show Entire File"); btn.Checked {
		t.Fatalf("Show Entire File on Local Changes = %+v, want off as chosen in History", btn)
	}
	if btn := diffOptionsButton(t, h, "Wrap Lines"); !btn.Checked {
		t.Fatalf("Wrap Lines on Local Changes = %+v, want on as chosen in History", btn)
	}
	if got := diffFixtureLines(h.ui); got["row 01"] || got["row 16"] {
		t.Fatalf("Local Changes diff kept the entire file after the choice changed in History: %v", got)
	}

	// An app built on the reopened store restores the choices.
	again, err := store.Open(h.store.Path())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	a := newApp(again)
	if !a.session.DiffChangesOnly() {
		t.Fatal("restored app does not keep the changes-only choice")
	}
	if got := a.session.DiffContextLines(); got != 5 {
		t.Fatalf("restored app context lines = %d, want 5", got)
	}
	if !a.session.DiffWrap() {
		t.Fatal("restored app does not keep the wrap choice")
	}
}

// When the settings save fails the choices stay unchanged and the diff is
// not reloaded, for the reloading entire-file toggle and for the rendering
// wrap toggle alike, and the failure is shown as a toast.
func TestDiffOptionsSaveFailure(t *testing.T) {
	repo := newGitRepo(t, "diffopts-savefail")
	writeRowsFixture(t, repo, 16)

	// Build the harness by hand so the store's directory can be made
	// read-only after the initial snapshot: SetSettings then fails on write.
	storeDir := t.TempDir()
	t.Cleanup(func() {
		if err := os.Chmod(storeDir, 0o755); err != nil {
			t.Errorf("restore store dir permissions: %v", err)
		}
	})
	s, err := store.Open(filepath.Join(storeDir, "repositories.json"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)
	a := newApp(s)
	a.postFn = q.post
	a.spawnFn = q.spawn
	h := &testHarness{
		t:     t,
		app:   a,
		store: s,
		ui:    ui.NewTester(a.view, 1200, 800),
		queue: q,
	}
	h.ui.Frame()
	h.openRepo(repo.StoreRepo())
	h.drain()

	mustClick(t, h, "Unstaged rows.txt")
	h.drain()
	if got := diffFixtureLines(h.ui); !got["row 01"] {
		t.Fatalf("entire-file diff did not open with the first row: %v", got)
	}

	if err := os.Chmod(storeDir, 0o555); err != nil {
		t.Fatalf("make store dir read-only: %v", err)
	}
	loadedKey := h.app.changesView.diff.loadedKey

	mustClick(t, h, "Show Entire File")
	if h.app.session.DiffChangesOnly() {
		t.Fatal("changes-only turned on although saving failed")
	}
	if h.app.changesView.diff.loading {
		t.Fatal("diff reload started although saving the toggle failed")
	}
	if btn := diffOptionsButton(t, h, "Show Entire File"); !btn.Checked {
		t.Fatalf("Show Entire File = %+v after a failed save, want still on", btn)
	}
	if got := diffFixtureLines(h.ui); !got["row 01"] {
		t.Fatalf("diff content changed after the failed save: %v", got)
	}

	mustClick(t, h, "Wrap Lines")
	if h.app.session.DiffWrap() {
		t.Fatal("wrap turned on although saving failed")
	}
	if btn := diffOptionsButton(t, h, "Wrap Lines"); btn.Checked {
		t.Fatalf("Wrap Lines = %+v after a failed save, want still off", btn)
	}
	if h.app.changesView.diff.loadedKey != loadedKey {
		t.Fatal("loaded request key changed although every save failed")
	}
	if !h.ui.HasText("Cannot save settings") {
		t.Fatalf("save failure toast missing; texts=%q", h.ui.Texts())
	}
}

// At the 880-point minimum window width every diff header control — the
// four toolbar buttons, -w, Split and Unified — lies inside the header,
// and clicking one through it really works there.
func TestDiffOptionsHeaderControlsFitAtMinWidth(t *testing.T) {
	_, h := openRowsDiff(t, 16)

	resize(t, h, 880, 700)

	header := diffHeaderBounds(t, h)
	for _, name := range []string{
		"Show Entire File", "Decrease Context Lines", "Increase Context Lines", "Wrap Lines",
		"Ignore Whitespace", "Split", "Unified",
	} {
		btn, ok := h.ui.AccessByRole(ui.RoleButton, name)
		if !ok {
			t.Fatalf("%s missing at minimum width; texts=%q", name, h.ui.Texts())
		}
		b := btn.Bounds
		if b.X < header.X-0.5 || b.X+b.W > header.X+header.W+0.5 {
			t.Fatalf("%s horizontal bounds [%.2f, %.2f] outside the diff header [%.2f, %.2f]",
				name, b.X, b.X+b.W, header.X, header.X+header.W)
		}
		if b.Y < header.Y-0.5 || b.Y+b.H > header.Y+header.H+0.5 {
			t.Fatalf("%s vertical bounds [%.2f, %.2f] outside the diff header [%.2f, %.2f]",
				name, b.Y, b.Y+b.H, header.Y, header.Y+header.H)
		}
	}

	mustClick(t, h, "Wrap Lines")
	h.drain()
	if !h.app.session.DiffWrap() {
		t.Fatal("clicking Wrap Lines inside the compact header did not turn wrapping on")
	}
}

// The entire-file choice covers a repository hosted behind a real loopback
// SSH server: the diff of a change in the middle of a file there shows every
// row, and toggling the header button narrows it to the change.
func TestDiffOptionsEntireFileLoopbackSSH(t *testing.T) {
	sshRepo, srv := loopbackSSHRepo(t)
	dir := sshRepo.Path

	writeRemote := func(edited bool) {
		t.Helper()
		srv.WriteFile(t, dir, "rows.txt", []byte(rowsFixtureContent(16, 8, edited)))
	}
	writeRemote(false)
	srv.RunGit(t, dir, "add", "rows.txt")
	srv.RunGit(t, dir, "commit", "-m", "base")
	writeRemote(true)
	srv.RunGit(t, dir, "add", "rows.txt")
	srv.RunGit(t, dir, "commit", "-m", "edit middle")
	head := strings.TrimSpace(srv.RunGit(t, dir, "rev-parse", "HEAD"))

	h := newTestApp(t, sshRepo)
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click(sshRepo.Name); err != nil {
		t.Fatalf("select SSH repo %q in Repository Switcher: %v", sshRepo.Name, err)
	}
	h.ui.Frame()
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	mustClick(t, h, "History")
	h.ui.Frame()
	clickCommitRow(t, h, head, "edit middle")
	h.drain()
	mustClick(t, h, fileRowLabel("rows.txt"))
	h.drain()

	got := diffFixtureLines(h.ui)
	for _, row := range []string{"row 01", "row 16"} {
		if !got[row] {
			t.Fatalf("entire-file diff over SSH missing %q: %v", row, got)
		}
	}

	mustClick(t, h, "Show Entire File")
	h.drain()
	got = diffFixtureLines(h.ui)
	for _, row := range []string{"row 01", "row 16"} {
		if got[row] {
			t.Fatalf("changes-only diff over SSH still shows %q away from the change", row)
		}
	}
}

// When saving the context line count fails, stepping up or down keeps the
// previous count, skips reloading the diff, and surfaces the failure toast.
func TestDiffOptionsContextSaveFailure(t *testing.T) {
	repo := newGitRepo(t, "diffopts-ctx-savefail")
	writeRowsFixture(t, repo, 16)

	storeDir := t.TempDir()
	t.Cleanup(func() {
		if err := os.Chmod(storeDir, 0o755); err != nil {
			t.Errorf("restore store dir permissions: %v", err)
		}
	})
	s, err := store.Open(filepath.Join(storeDir, "repositories.json"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)
	a := newApp(s)
	a.postFn = q.post
	a.spawnFn = q.spawn
	h := &testHarness{
		t:     t,
		app:   a,
		store: s,
		ui:    ui.NewTester(a.view, 1200, 800),
		queue: q,
	}
	h.ui.Frame()
	h.openRepo(repo.StoreRepo())
	h.drain()

	mustClick(t, h, "Unstaged rows.txt")
	h.drain()
	mustClick(t, h, "Show Entire File")
	h.drain()

	if err := os.Chmod(storeDir, 0o555); err != nil {
		t.Fatalf("make store dir read-only: %v", err)
	}
	stateBefore := h.app.changesView.diff.state
	loadedKey := h.app.changesView.diff.loadedKey

	for _, name := range []string{"Increase Context Lines", "Decrease Context Lines"} {
		mustClick(t, h, name)
		if got := h.app.session.DiffContextLines(); got != 3 {
			t.Fatalf("context lines after failed %s = %d, want 3", name, got)
		}
		if h.app.changesView.diff.loading {
			t.Fatalf("diff reload started after failed %s", name)
		}
		if h.app.changesView.diff.loadedKey != loadedKey || h.app.changesView.diff.state != stateBefore {
			t.Fatalf("diff state changed after failed %s", name)
		}
	}
	if !h.ui.HasText("Cannot save settings") {
		t.Fatalf("save failure toast missing; texts=%q", h.ui.Texts())
	}
}

// An existing settings file written before the diff toolbar choices existed
// defaults to the entire file, 3 context lines, and no wrapping without
// migration.
func TestDiffOptionsLegacySettingsDefaultEntireFile(t *testing.T) {
	repo := newGitRepo(t, "diffopts-legacy")
	writeRowsFixture(t, repo, 16)

	storePath := filepath.Join(t.TempDir(), "repositories.json")
	legacyJSON := `{"settingsSaved":true,"settings":{"dateOrder":true}}`
	if err := os.WriteFile(storePath, []byte(legacyJSON), 0o600); err != nil {
		t.Fatalf("write legacy settings file: %v", err)
	}
	s, err := store.Open(storePath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)
	a := newApp(s)
	a.postFn = q.post
	a.spawnFn = q.spawn
	h := &testHarness{
		t:     t,
		app:   a,
		store: s,
		ui:    ui.NewTester(a.view, 1200, 800),
		queue: q,
	}
	h.ui.Frame()
	h.openRepo(repo.StoreRepo())
	h.drain()

	mustClick(t, h, "Unstaged rows.txt")
	h.drain()

	if btn := diffOptionsButton(t, h, "Show Entire File"); !btn.Checked {
		t.Fatalf("legacy settings Show Entire File = %+v, want on by default", btn)
	}
	if got := h.app.session.DiffContextLines(); got != 3 {
		t.Fatalf("legacy settings context lines = %d, want 3", got)
	}
	if h.app.session.DiffWrap() {
		t.Fatal("legacy settings started with wrap on")
	}
	got := diffFixtureLines(h.ui)
	for _, row := range []string{"row 01", "row 16"} {
		if !got[row] {
			t.Fatalf("legacy settings entire-file diff missing %q: %v", row, got)
		}
	}
}
