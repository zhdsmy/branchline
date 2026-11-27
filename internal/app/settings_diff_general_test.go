package app

import (
	"bytes"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// openSettingsTester drives the Settings window from its real entry — ⌘, in
// the main window — and renders its view headlessly, as the window hook
// would in a real window.
func openSettingsTester(t *testing.T, h *testHarness) *ui.Tester {
	t.Helper()
	installSettingsHooks(h)
	h.ui.Key(ui.Cmd, ui.KeyComma)
	h.ui.Frame()
	return newSettingsTester(t, h)
}

// clickSettingsTab selects a page of the Settings window through its tab, as
// a user would.
func clickSettingsTab(t *testing.T, tt *ui.Tester, title string) {
	t.Helper()
	if err := tt.Click(title); err != nil {
		t.Fatalf("click %s settings tab: %v", title, err)
	}
	tt.Frame()
}

// imagesDiffer reports whether two rendered frames differ in any pixel. The
// frames share their size in these tests; a size difference counts as a
// difference.
func imagesDiffer(a, b *image.RGBA) bool {
	if !a.Bounds().Eq(b.Bounds()) {
		return true
	}
	return !bytes.Equal(a.Pix, b.Pix)
}

// gitAt runs git in dir with the given environment entries added, fixing the
// author and committer timestamps a test commits with.
func gitAt(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	cmd.Env = append(cmd.Env, env...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, buf.String())
	}
	return buf.String()
}

// commitAt commits every staged and unstaged change of the fixture with
// fixed author and committer timestamps, and returns its hash.
func commitAt(t *testing.T, repo *gitRepoFixture, stamp, message string) string {
	t.Helper()
	env := []string{"GIT_AUTHOR_DATE=" + stamp, "GIT_COMMITTER_DATE=" + stamp}
	gitAt(t, repo.Path(), env, "add", "-A")
	gitAt(t, repo.Path(), env, "commit", "-q", "-m", message)
	return strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
}

// The Commit Order choice of the General page reorders the history of a
// repository whose topological and date orders strictly differ: by date the
// commits of the two branches interleave, topologically each branch's chain
// stays together. The choice persists in the store, and switching it back
// restores the topological order.
func TestSettingsDiffGeneralCommitOrderSwitchesHistory(t *testing.T) {
	repo := newGitRepo(t, "sdg-order")
	repo.WriteFile("base.txt", "base\n")
	root := commitAt(t, repo, "2024-01-01T10:00:00Z", "root")
	repo.Git("checkout", "-q", "-b", "side")
	repo.WriteFile("s1.txt", "s1\n")
	s1 := commitAt(t, repo, "2024-01-01T10:01:00Z", "side one")
	repo.WriteFile("s2.txt", "s2\n")
	s2 := commitAt(t, repo, "2024-01-01T10:04:00Z", "side two")
	repo.Git("checkout", "-q", "main")
	repo.WriteFile("m1.txt", "m1\n")
	m1 := commitAt(t, repo, "2024-01-01T10:02:00Z", "main one")
	repo.WriteFile("m2.txt", "m2\n")
	m2 := commitAt(t, repo, "2024-01-01T10:03:00Z", "main two")

	// The orders must really differ in the fixture, or every assertion below
	// is vacuous: by date the newest-first walk interleaves the branches.
	dateOrderHashes := []string{s2, m2, m1, s1, root}
	cliDate := strings.Fields(gitAt(t, repo.Path(), nil, "log", "--date-order", "--branches", "--format=%H"))
	if !slices.Equal(cliDate, dateOrderHashes) {
		t.Fatalf("fixture broken: git log --date-order = %v, want %v", cliDate, dateOrderHashes)
	}
	cliTopo := strings.Fields(gitAt(t, repo.Path(), nil, "log", "--topo-order", "--branches", "--format=%H"))
	if slices.Equal(cliTopo, dateOrderHashes) {
		t.Fatalf("fixture broken: --topo-order matches --date-order %v: the orders do not differ", cliTopo)
	}

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	snapshotOrder := func() []string {
		var out []string
		for _, c := range h.app.session.Snapshot().Commits {
			out = append(out, c.Hash)
		}
		return out
	}
	if got := snapshotOrder(); slices.Equal(got, dateOrderHashes) {
		t.Fatalf("default history already in date order %v: the starting order must be topological", got)
	}
	if h.store.Settings().DateOrder {
		t.Fatal("fresh store starts with DateOrder on")
	}

	tt := openSettingsTester(t, h)
	if err := tt.Click("Commit Order By date"); err != nil {
		t.Fatalf("click Commit Order By date: %v", err)
	}
	tt.Frame()
	h.drain()

	if !h.store.Settings().DateOrder {
		t.Fatal("DateOrder not persisted after choosing By date")
	}
	reopened, err := store.Open(h.store.Path())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if !reopened.Settings().DateOrder {
		t.Fatal("reopened store reads DateOrder off: the choice did not persist")
	}
	if got := snapshotOrder(); !slices.Equal(got, dateOrderHashes) {
		t.Fatalf("history after choosing By date = %v, want the date order %v", got, dateOrderHashes)
	}
	radio, ok := tt.AccessByRole(ui.RoleRadio, "Commit Order By date")
	if !ok || !radio.Checked {
		t.Fatalf("Commit Order By date radio = %+v (found=%v), want checked", radio, ok)
	}

	// Switching back restores the topological order.
	topoBefore := snapshotOrder()
	if err := tt.Click("Commit Order Topologically"); err != nil {
		t.Fatalf("click Commit Order Topologically: %v", err)
	}
	tt.Frame()
	h.drain()
	if h.store.Settings().DateOrder {
		t.Fatal("DateOrder still persisted after choosing Topologically")
	}
	got := snapshotOrder()
	if slices.Equal(got, dateOrderHashes) {
		t.Fatalf("history after choosing Topologically still in date order %v", got)
	}
	if slices.Equal(got, topoBefore) {
		// topoBefore was the date order; equality would mean nothing changed.
		t.Fatalf("history order did not change back: %v", got)
	}
}

// The Fetch all tags checkbox of the General page persists the preference,
// and the next toolbar Fetch really brings the remote's unreachable tag in.
func TestSettingsDiffGeneralFetchAllTagsFetchesUnreachableTag(t *testing.T) {
	repo := newGitRepo(t, "sdg-fetch-tags")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	_, tagged := pushUnreachableTagLocal(t, bareDir)

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	tt := openSettingsTester(t, h)
	if err := tt.Click("Fetch all tags"); err != nil {
		t.Fatalf("click Fetch all tags: %v", err)
	}
	tt.Frame()
	if !h.store.Settings().FetchTags {
		t.Fatal("FetchTags not persisted after checking Fetch all tags")
	}
	box, ok := tt.AccessByRole(ui.RoleCheckBox, "Fetch all tags")
	if !ok || !box.Checked {
		t.Fatalf("Fetch all tags checkbox = %+v (found=%v), want checked", box, ok)
	}

	clickFetch(t, h)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "refs/tags/v-unreach")); got != tagged {
		t.Fatalf("after Fetch refs/tags/v-unreach = %q, want the tagged commit %q", got, tagged)
	}
	if !h.ui.HasText("v-unreach") {
		t.Fatalf("tag v-unreach missing from the sidebar after fetching tags; texts=%q", h.ui.Texts())
	}
}

// A whitespace-only working-tree diff drives the diff panel of the Changes
// page; openDiffForWhitespaceRepo returns the harness with the diff open.
func openWhitespaceDiffHarness(t *testing.T) (*gitRepoFixture, *testHarness) {
	t.Helper()
	repo := newGitRepo(t, "sdg-ws")
	repo.CommitFile("notes.txt", "first line\n", "base")
	repo.WriteFile("notes.txt", "first  line\n")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged notes.txt")
	h.drain()
	if del := diffAccessLines(h.ui, ui.RoleDeletion); !slices.Contains(del, "first line") {
		t.Fatalf("whitespace diff did not open: deletions=%q", del)
	}
	return repo, h
}

// The Ignore whitespace changes checkbox of the Diff page and the Ignore
// Whitespace toggle of the diff panel are one choice: checking it in the
// Settings window reloads the open diff to the empty state and selects the
// panel's toggle, and clearing the toggle in the panel unchecks the page's
// checkbox.
func TestSettingsDiffGeneralIgnoreWhitespaceSyncsBothWays(t *testing.T) {
	_, h := openWhitespaceDiffHarness(t)

	tt := openSettingsTester(t, h)
	clickSettingsTab(t, tt, "Diff")
	if err := tt.Click("Ignore whitespace changes"); err != nil {
		t.Fatalf("click Ignore whitespace changes: %v", err)
	}
	tt.Frame()
	h.ui.Frame()
	h.drain()

	if !h.store.Settings().IgnoreSpace {
		t.Fatal("IgnoreSpace not persisted after checking the settings checkbox")
	}
	if !h.ui.HasText("No diff to display") {
		t.Fatalf("whitespace-only diff did not reload to the empty state; texts=%q", h.ui.Texts())
	}
	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Ignore Whitespace")
	if !ok || !btn.Checked {
		t.Fatalf("panel Ignore Whitespace toggle = %+v (found=%v), want selected after the settings checkbox", btn, ok)
	}
	box, ok := tt.AccessByRole(ui.RoleCheckBox, "Ignore whitespace changes")
	if !ok || !box.Checked {
		t.Fatalf("settings Ignore whitespace changes = %+v (found=%v), want checked", box, ok)
	}

	// The other direction: clearing the panel's toggle unchecks the checkbox.
	mustClick(t, h, "Ignore Whitespace")
	h.drain()
	if h.app.session.IgnoreSpace() {
		t.Fatal("session ignore-whitespace still on after clearing the panel toggle")
	}
	tt.Frame()
	box, ok = tt.AccessByRole(ui.RoleCheckBox, "Ignore whitespace changes")
	if !ok {
		t.Fatalf("settings Ignore whitespace changes missing; texts=%q", tt.Texts())
	}
	if box.Checked {
		t.Fatalf("settings checkbox still checked after the panel toggle was cleared: %+v", box)
	}
	if del := diffAccessLines(h.ui, ui.RoleDeletion); !slices.Contains(del, "first line") {
		t.Fatalf("whitespace diff did not come back after clearing the toggle: deletions=%q", del)
	}
}

// The Font Size choice of the Diff page sizes the open diff's code font: the
// panel's state takes the size, the lines really grow taller, the rendering
// changes pixel by pixel, and the choice survives reopening the store.
func TestSettingsDiffGeneralFontSizeResizesOpenDiff(t *testing.T) {
	repo := newGitRepo(t, "sdg-font")
	repo.CommitFile("code.go", "package main\n\nvar version = 1\nvar other = 1\n", "base")
	repo.WriteFile("code.go", "package main\n\nvar version = 2\nvar other = 2\n")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged code.go")
	h.drain()

	state := func() *editor.DiffState {
		t.Helper()
		if h.app.changesView.diff.state == nil {
			t.Fatal("no diff state: the diff did not open")
		}
		return h.app.changesView.diff.state
	}
	lineGap := func() float64 {
		t.Helper()
		return state().LineTop(editor.DiffNew, 1) - state().LineTop(editor.DiffNew, 0)
	}

	h.ui.Frame()
	before := copyImage(h.ui.Image())
	gapBefore := lineGap()
	if state().Font.Size != 0 {
		t.Fatalf("default diff font size = %v, want 0 (the built-in default)", state().Font.Size)
	}

	tt := openSettingsTester(t, h)
	clickSettingsTab(t, tt, "Diff")
	if err := tt.Click("Diff Font 18"); err != nil {
		t.Fatalf("click Diff Font 18: %v", err)
	}
	tt.Frame()
	h.ui.Frame()

	if got := state().Font.Size; got != 18 {
		t.Fatalf("diff state font size after choosing 18 = %v, want 18", got)
	}
	if gap := lineGap(); gap <= gapBefore {
		t.Fatalf("diff line gap with size 18 = %v, want growth beyond the default %v", gap, gapBefore)
	}
	h.ui.Frame()
	if !imagesDiffer(before, h.ui.Image()) {
		t.Fatal("the rendered diff did not change by a single pixel after resizing the font")
	}
	reopened, err := store.Open(h.store.Path())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if got := reopened.Settings().DiffFontSize; got != 18 {
		t.Fatalf("reopened DiffFontSize = %v, want 18", got)
	}

	// Restoring the default shrinks the lines back.
	if err := tt.Click("Diff Font Default"); err != nil {
		t.Fatalf("click Diff Font Default: %v", err)
	}
	tt.Frame()
	h.ui.Frame()
	if got := state().Font.Size; got != 0 {
		t.Fatalf("diff state font size after restoring the default = %v, want 0", got)
	}
	if gap := lineGap(); gap != gapBefore {
		t.Fatalf("diff line gap after restoring the default = %v, want the original %v", gap, gapBefore)
	}
}

// The Tab Width choice of the Diff page expands the tabs of the open diff:
// the state takes the width, and the rendering changes pixel by pixel as the
// indentation of the tabbed lines moves.
func TestSettingsDiffGeneralTabWidthExpandsOpenDiff(t *testing.T) {
	repo := newGitRepo(t, "sdg-tab")
	repo.CommitFile("code.go", "package main\n\nfunc f() {\n\tvar indented = 1\n\tvar other = 1\n}\n", "base")
	repo.WriteFile("code.go", "package main\n\nfunc f() {\n\tvar indented = 2\n\tvar other = 2\n}\n")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged code.go")
	h.drain()
	if h.app.changesView.diff.state == nil {
		t.Fatal("no diff state: the diff did not open")
	}
	if got := h.app.changesView.diff.state.TabSize; got != 0 {
		t.Fatalf("default diff tab size = %d, want 0 (the framework default)", got)
	}
	h.ui.Frame()
	before := copyImage(h.ui.Image())

	tt := openSettingsTester(t, h)
	clickSettingsTab(t, tt, "Diff")
	if err := tt.Click("Diff Tab Width 8"); err != nil {
		t.Fatalf("click Diff Tab Width 8: %v", err)
	}
	tt.Frame()
	h.ui.Frame()

	if got := h.app.changesView.diff.state.TabSize; got != 8 {
		t.Fatalf("diff state tab size after choosing 8 = %d, want 8", got)
	}
	h.ui.Frame()
	if !imagesDiffer(before, h.ui.Image()) {
		t.Fatal("the rendered diff did not change by a single pixel after widening the tabs")
	}
	reopened, err := store.Open(h.store.Path())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if got := reopened.Settings().DiffTabWidth; got != 8 {
		t.Fatalf("reopened DiffTabWidth = %d, want 8", got)
	}

	beforeWide := copyImage(h.ui.Image())
	if err := tt.Click("Diff Tab Width 2"); err != nil {
		t.Fatalf("click Diff Tab Width 2: %v", err)
	}
	tt.Frame()
	h.ui.Frame()
	if got := h.app.changesView.diff.state.TabSize; got != 2 {
		t.Fatalf("diff state tab size after choosing 2 = %d, want 2", got)
	}
	h.ui.Frame()
	if !imagesDiffer(beforeWide, h.ui.Image()) {
		t.Fatal("the rendered diff did not change by a single pixel after narrowing the tabs")
	}
}

// The Show +/- change marks checkbox of the Diff page switches the open
// diff's line indicators from bars to the classic + and - signs, and the
// rendering changes pixel by pixel.
func TestSettingsDiffGeneralChangeMarksRedrawsOpenDiff(t *testing.T) {
	repo := newGitRepo(t, "sdg-marks")
	repo.CommitFile("code.go", "package main\n\nvar version = 1\nvar other = 1\n", "base")
	repo.WriteFile("code.go", "package main\n\nvar version = 2\nvar other = 2\n")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged code.go")
	h.drain()
	if h.app.changesView.diff.state == nil {
		t.Fatal("no diff state: the diff did not open")
	}
	if got := h.app.changesView.diff.state.Indicators; got != editor.DiffBars {
		t.Fatalf("default diff indicators = %v, want DiffBars", got)
	}
	h.ui.Frame()
	before := copyImage(h.ui.Image())

	tt := openSettingsTester(t, h)
	clickSettingsTab(t, tt, "Diff")
	if err := tt.Click("Show +/- change marks"); err != nil {
		t.Fatalf("click Show +/- change marks: %v", err)
	}
	tt.Frame()
	h.ui.Frame()

	if got := h.app.changesView.diff.state.Indicators; got != editor.DiffClassic {
		t.Fatalf("diff indicators after checking change marks = %v, want DiffClassic", got)
	}
	h.ui.Frame()
	if !imagesDiffer(before, h.ui.Image()) {
		t.Fatal("the rendered diff did not change by a single pixel after switching the change marks")
	}
	reopened, err := store.Open(h.store.Path())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if !reopened.Settings().ChangeMarks {
		t.Fatal("reopened store reads ChangeMarks off: the choice did not persist")
	}
	box, ok := tt.AccessByRole(ui.RoleCheckBox, "Show +/- change marks")
	if !ok || !box.Checked {
		t.Fatalf("Show +/- change marks = %+v (found=%v), want checked", box, ok)
	}
}

// writeGitWrapperScript writes a shell script that logs each invocation and
// forwards to the real git, returning its path. GitPath points git commands
// of the app at it.
func writeGitWrapperScript(t *testing.T) (script, logFile string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locate real git: %v", err)
	}
	dir := t.TempDir()
	logFile = filepath.Join(dir, "wrapper.log")
	script = filepath.Join(dir, "git-wrapper.sh")
	content := "#!/bin/sh\necho \"$*\" >> " + shellQ(logFile) + "\nexec " + shellQ(real) + " \"$@\"\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	return script, logFile
}

func readWrapperLog(t *testing.T, logFile string) string {
	t.Helper()
	b, err := os.ReadFile(logFile)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read wrapper log: %v", err)
	}
	return string(b)
}

// A saved custom git program reaches the executor of the active repository
// through RebuildExecutor: a write already in progress finishes uninterrupted
// on the executor it started with, the command after it — the write's
// automatic reload — runs through the wrapper, and rebuilding while idle
// reloads through the wrapper at once.
func TestSettingsDiffGeneralRebuildExecutorUsesCustomGit(t *testing.T) {
	repo := newGitRepo(t, "sdg-rebuild")
	repo.CommitFile("a.txt", "a\n", "init")
	script, logFile := writeGitWrapperScript(t)

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if got := readWrapperLog(t, logFile); got != "" {
		t.Fatalf("wrapper already invoked before being configured: %q", got)
	}

	// Busy path: hold the write before it starts, then swap the executor
	// mid-flight. The write runs on the executor it captured — the plain
	// one — and its automatic reload is the first command of the new one.
	h.queue.PauseWorkers()
	if ok := h.app.session.do("Test write", func(r *git.Repo) error {
		return r.Run("branch", "rebuild-probe")
	}); !ok {
		t.Fatal("session.do was rejected unexpectedly")
	}
	writeWorker := h.queue.WaitWorker(t)

	cur := h.store.Settings()
	cur.GitPath = script
	if err := h.store.SetSettings(cur); err != nil {
		t.Fatalf("save GitPath: %v", err)
	}
	h.app.session.RebuildExecutor()
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()

	if h.app.session.ActionErr() != nil {
		t.Fatalf("write interrupted by RebuildExecutor: %v", h.app.session.ActionErr())
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "--verify", "refs/heads/rebuild-probe")); got == "" {
		t.Fatal("the write did not complete: branch rebuild-probe missing")
	}
	got := readWrapperLog(t, logFile)
	if strings.Contains(got, "rebuild-probe") {
		t.Fatalf("the write in progress switched executors mid-flight; log=%q", got)
	}
	if !strings.Contains(got, "--topo-order") {
		t.Fatalf("the command after the write did not run through the wrapper; log=%q", got)
	}

	// Idle path: rebuilding with no write in progress reloads at once, and
	// the reload runs through the wrapper.
	if err := os.Truncate(logFile, 0); err != nil {
		t.Fatalf("truncate wrapper log: %v", err)
	}
	h.app.session.RebuildExecutor()
	h.drain()
	if got := readWrapperLog(t, logFile); !strings.Contains(got, "--topo-order") {
		t.Fatalf("idle reload after RebuildExecutor did not run through the wrapper; log=%q", got)
	}
}

// With the store's directory read-only every control of the General and Diff
// pages reports the raw save error, keeps the settings and the session
// state unchanged, and reverts to the saved value.
func TestSettingsDiffGeneralSaveFailuresRevertAndReport(t *testing.T) {
	repo := newGitRepo(t, "sdg-savefail")
	repo.CommitFile("code.go", "package main\n\nvar version = 1\n", "base")
	repo.WriteFile("code.go", "package main\n\nvar version = 2\n")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	storeDir := filepath.Dir(h.store.Path())
	tt := openSettingsTester(t, h)

	// Prove the save really fails in this state before asserting on it.
	if err := os.Chmod(storeDir, 0o500); err != nil {
		t.Fatalf("chmod store dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(storeDir, 0o700) })
	probe := h.store.SetSettings(h.store.Settings())
	if probe == nil {
		t.Fatal("SetSettings succeeded into a read-only directory: the failure assertions below would be vacuous")
	}
	commitBefore := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	snapshotBefore := len(h.app.session.Snapshot().Commits)

	clickFailing := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatalf("click %s: %v", label, err)
		}
		tt.Frame()
		if !tt.HasText(probe.Error()) {
			t.Fatalf("page after failing %s does not show the raw error %q; texts=%q", label, probe.Error(), tt.Texts())
		}
		// The control reverted to the saved value: it does not read as
		// checked anymore.
		info, ok := tt.AccessByRole(ui.RoleRadio, label)
		if !ok {
			info, ok = tt.AccessByRole(ui.RoleCheckBox, label)
		}
		if !ok {
			t.Fatalf("control %s missing; texts=%q", label, tt.Texts())
		}
		if info.Checked {
			t.Fatalf("%s still reads as checked after its failed save: %+v", label, info)
		}
	}

	// General page: commit order and fetch tags.
	clickFailing("Commit Order By date")
	if h.store.Settings().DateOrder || h.app.session.DateOrder() {
		t.Fatal("failed commit order save changed the order")
	}
	clickFailing("Fetch all tags")
	if h.store.Settings().FetchTags {
		t.Fatal("failed fetch tags save turned the preference on")
	}

	// Diff page: font size, tab width, change marks, ignore whitespace.
	clickSettingsTab(t, tt, "Diff")
	clickFailing("Diff Font 18")
	if got := h.store.Settings().DiffFontSize; got != 0 {
		t.Fatalf("failed font size save changed DiffFontSize to %v", got)
	}
	clickFailing("Diff Tab Width 8")
	if got := h.store.Settings().DiffTabWidth; got != 0 {
		t.Fatalf("failed tab width save changed DiffTabWidth to %v", got)
	}
	clickFailing("Show +/- change marks")
	if h.store.Settings().ChangeMarks {
		t.Fatal("failed change marks save turned the preference on")
	}
	clickFailing("Ignore whitespace changes")
	if h.store.Settings().IgnoreSpace || h.app.session.IgnoreSpace() {
		t.Fatal("failed ignore whitespace save turned the choice on")
	}
	if got := len(h.app.session.Snapshot().Commits); got != snapshotBefore {
		t.Fatalf("snapshot commit count changed during failed saves: %d, want %d", got, snapshotBefore)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != commitBefore {
		t.Fatalf("HEAD moved during failed saves: %q, want %q", got, commitBefore)
	}

	// Restoring writability lets the same choices save: each control's retry
	// persists and clears its own error, until neither page shows one.
	if err := os.Chmod(storeDir, 0o700); err != nil {
		t.Fatalf("restore store dir: %v", err)
	}
	retrySaving := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatalf("click %s again: %v", label, err)
		}
		tt.Frame()
	}

	clickSettingsTab(t, tt, "General")
	retrySaving("Commit Order By date")
	retrySaving("Fetch all tags")
	h.drain()
	if !h.store.Settings().DateOrder {
		t.Fatal("the retry after restoring writability did not persist DateOrder")
	}
	if !h.store.Settings().FetchTags {
		t.Fatal("the retry after restoring writability did not persist FetchTags")
	}
	if tt.HasText(probe.Error()) {
		t.Fatalf("the General page still shows the save error after successful retries; texts=%q", tt.Texts())
	}

	clickSettingsTab(t, tt, "Diff")
	retrySaving("Diff Font 18")
	retrySaving("Diff Tab Width 8")
	retrySaving("Show +/- change marks")
	retrySaving("Ignore whitespace changes")
	if got := h.store.Settings(); got.DiffFontSize != 18 || got.DiffTabWidth != 8 || !got.ChangeMarks || !got.IgnoreSpace {
		t.Fatalf("the Diff page retries did not persist: %+v", got)
	}
	if tt.HasText(probe.Error()) {
		t.Fatalf("the Diff page still shows the save error after successful retries; texts=%q", tt.Texts())
	}
}

// At the smallest window the Settings window allows, the tab row and every
// settings row stay left-aligned and inside the window: no control of the
// General or the Diff page overflows the 480-point width.
func TestSettingsDiffGeneralLayoutMinWidth(t *testing.T) {
	h := newTestApp(t)
	tt := openSettingsTester(t, h)
	tt.SetSize(480, 360)
	tt.Frame()

	assertWithin := func(page string) {
		t.Helper()
		for _, a := range tt.AccessAll() {
			if right := a.Bounds.X + a.Bounds.W; right > 480+0.5 {
				t.Fatalf("%s page: %q (role %v) right edge %.1f beyond the 480-point window", page, a.Name, a.Role, right)
			}
		}
	}

	tab, ok := tt.Find("General")
	if !ok {
		t.Fatalf("no General tab; texts=%q", tt.Texts())
	}
	if tab.X > 60 {
		t.Fatalf("the General tab starts at X %.1f: the tab row is not left-aligned", tab.X)
	}
	radio, ok := tt.AccessByRole(ui.RoleRadio, "Appearance System")
	if !ok {
		t.Fatalf("no Appearance System radio; texts=%q", tt.Texts())
	}
	if radio.Bounds.X > 200 {
		t.Fatalf("the Appearance radios start at X %.1f: the row content is not left-aligned", radio.Bounds.X)
	}
	assertWithin("General")

	clickSettingsTab(t, tt, "Diff")
	tt.SetSize(480, 360)
	tt.Frame()
	fontRadio, ok := tt.AccessByRole(ui.RoleRadio, "Diff Font Default")
	if !ok {
		t.Fatalf("no Diff Font Default radio; texts=%q", tt.Texts())
	}
	if fontRadio.Bounds.X > 200 {
		t.Fatalf("the Diff Font radios start at X %.1f: the row content is not left-aligned", fontRadio.Bounds.X)
	}
	assertWithin("Diff")

	// The tab row itself stays put: the Diff tab sits beside General, not
	// below it or off the window.
	diffTab, ok := tt.Find("Diff")
	if !ok {
		t.Fatalf("no Diff tab; texts=%q", tt.Texts())
	}
	if diffTab.X+diffTab.W > 480+0.5 {
		t.Fatalf("the Diff tab ends at X %.1f: it overflows the 480-point window", diffTab.X+diffTab.W)
	}
	if dy := diffTab.Y - tab.Y; dy < -2 || dy > 2 {
		t.Fatalf("the Diff tab sits %.1f below the General tab: the tabs are not on one row", dy)
	}
}

// Opening the Diff page through its tab selects it for assistive technology
// and primes its controls from the saved values rather than the zero values.
func TestSettingsDiffGeneralDiffTabSelectedAndPrimed(t *testing.T) {
	h := newTestApp(t)
	cur := h.store.Settings()
	cur.DiffFontSize = 12
	cur.DiffTabWidth = 2
	cur.ChangeMarks = true
	if err := h.store.SetSettings(cur); err != nil {
		t.Fatalf("seed diff settings: %v", err)
	}

	tt := openSettingsTester(t, h)
	general, ok := tt.AccessByRole(ui.RoleTab, "General")
	if !ok || !general.Selected {
		t.Fatalf("General tab = %+v (found=%v), want selected on open", general, ok)
	}

	clickSettingsTab(t, tt, "Diff")
	diffTab, ok := tt.AccessByRole(ui.RoleTab, "Diff")
	if !ok || !diffTab.Selected {
		t.Fatalf("Diff tab = %+v (found=%v), want selected after clicking it", diffTab, ok)
	}
	if general, ok := tt.AccessByRole(ui.RoleTab, "General"); ok && general.Selected {
		t.Fatal("General tab still reads as selected on the Diff page")
	}

	// The seeded values show in the controls: font 12 chosen, tab width 2
	// chosen, change marks checked.
	font, ok := tt.AccessByRole(ui.RoleRadio, "Diff Font 12")
	if !ok || !font.Checked {
		t.Fatalf("Diff Font 12 = %+v (found=%v), want checked with the seeded size", font, ok)
	}
	width, ok := tt.AccessByRole(ui.RoleRadio, "Diff Tab Width 2")
	if !ok || !width.Checked {
		t.Fatalf("Diff Tab Width 2 = %+v (found=%v), want checked with the seeded width", width, ok)
	}
	marks, ok := tt.AccessByRole(ui.RoleCheckBox, "Show +/- change marks")
	if !ok || !marks.Checked {
		t.Fatalf("Show +/- change marks = %+v (found=%v), want checked with the seeded choice", marks, ok)
	}
}

// The session's zero-value Settings keeps standalone diff panels at the
// framework defaults, so panels built without a store render as before.
func TestSettingsDiffGeneralSessionlessPanelKeepsDefaults(t *testing.T) {
	repo := newGitRepo(t, "sdg-standalone")
	repo.CommitFile("code.go", "package main\n\nvar version = 1\n", "base")
	repo.WriteFile("code.go", "package main\n\nvar version = 2\n")

	h := newDiffPanelHarness(t, repo.StoreRepo(), "sdg-standalone-diff")
	h.panel.Load(h.session, h.scope, unstagedDiffRequest(git.FileChange{Path: "code.go", Status: 'M'}, "rev-1"))
	h.drain()
	if h.panel.state == nil {
		t.Fatal("no diff state in the standalone panel")
	}
	if got := h.panel.state.Font.Size; got != 0 {
		t.Fatalf("standalone panel font size = %v, want 0 (the built-in default)", got)
	}
	if got := h.panel.state.TabSize; got != 0 {
		t.Fatalf("standalone panel tab size = %d, want 0 (the framework default)", got)
	}
	if got := h.panel.state.Indicators; got != editor.DiffBars {
		t.Fatalf("standalone panel indicators = %v, want DiffBars", got)
	}
}
