package app

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/egoist/mygo"

	"github.com/zhdsmy/branchline/internal/git"
)

// menuBarRig attaches a menu bar built for the harness app and records every
// enabled-state application. The test seam updates the item's Disabled flag
// directly: MenuItem.SetEnabled would post a native menu update into an
// event loop a headless test never runs, while the item's own template field
// is exactly what production ends up changing, so IsEnabled stays honest.
type menuBarRig struct {
	menu    *mygo.Menu
	applied map[string]int
	states  map[string]bool
}

func installMenuBar(h *testHarness) *menuBarRig {
	rig := &menuBarRig{
		applied: map[string]int{},
		states:  map[string]bool{},
	}
	rig.menu = appMenuBar(func() *app { return h.app })
	h.app.menubar = rig.menu
	h.app.menuSetEnabledFn = func(it *mygo.MenuItem, enabled bool) {
		it.Disabled = !enabled
		rig.applied[it.ID]++
		rig.states[it.ID] = enabled
	}
	return rig
}

// resetCounters drops the applications of every frame so far, so the test
// counts only what the frames after it did.
func (rig *menuBarRig) resetCounters() {
	rig.applied = map[string]int{}
}

// totalApplications reports how many SetEnabled equivalents ran since the
// counters were last reset.
func (rig *menuBarRig) totalApplications() int {
	n := 0
	for _, c := range rig.applied {
		n += c
	}
	return n
}

func menuItem(t *testing.T, rig *menuBarRig, id string) *mygo.MenuItem {
	t.Helper()
	it := rig.menu.ItemByID(id)
	if it == nil {
		t.Fatalf("no menu item %q in the menu bar", id)
	}
	return it
}

// clickMenuItem chooses the item exactly as the native menu would: through
// its own Click handler. The window argument is nil, as a menu-bar click of
// the application menu may be in production.
func clickMenuItem(t *testing.T, rig *menuBarRig, id string) {
	t.Helper()
	it := menuItem(t, rig, id)
	if it.Click == nil {
		t.Fatalf("menu item %q has no Click handler", id)
	}
	it.Click(it, nil)
}

// allMenuItems flattens the menu tree, submenus included.
func allMenuItems(items []*mygo.MenuItem) []*mygo.MenuItem {
	var out []*mygo.MenuItem
	for _, it := range items {
		out = append(out, it)
		if len(it.Submenu) > 0 {
			out = append(out, allMenuItems(it.Submenu)...)
		}
	}
	return out
}

// normalizeAccelerator reduces an accelerator to a canonical modifier+key
// form with the macOS reading of CmdOrCtrl — the platform the menu bar ships
// on — so two spellings of one shortcut compare equal.
func normalizeAccelerator(acc string) string {
	if acc == "" {
		return ""
	}
	parts := strings.Split(acc, "+")
	key := parts[len(parts)-1]
	var mods []string
	seen := map[string]bool{}
	for _, p := range parts[:len(parts)-1] {
		switch strings.ToLower(p) {
		case "cmdorctrl", "cmd", "super", "meta", "win":
			p = "Cmd"
		case "ctrl":
			p = "Ctrl"
		case "alt", "option":
			p = "Alt"
		case "shift":
			p = "Shift"
		}
		if !seen[p] {
			seen[p] = true
			mods = append(mods, p)
		}
	}
	sort.Strings(mods)
	return strings.Join(append(mods, key), "+")
}

// The menu bar has the six top-level menus in order — application, File,
// Edit, View, Repository, Window — every custom item carries the task's ID,
// label and accelerator, every custom item has a Click handler, and no two
// items of the whole bar share one accelerator.
func TestMenuBarLayoutAndAccelerators(t *testing.T) {
	h := newTestApp(t)
	rig := installMenuBar(h)

	items := rig.menu.Items()
	if len(items) != 6 {
		t.Fatalf("top-level menus = %d, want 6", len(items))
	}
	if items[0].Role != "" {
		t.Fatalf("first top-level menu has role %q, want the plain application menu", items[0].Role)
	}
	if items[1].Label != "File" {
		t.Fatalf("second top-level menu = %q, want File", items[1].Label)
	}
	if items[2].Role != mygo.RoleEditMenu {
		t.Fatalf("third top-level menu role = %q, want editMenu", items[2].Role)
	}
	if items[3].Label != "View" {
		t.Fatalf("fourth top-level menu = %q, want View", items[3].Label)
	}
	if items[4].Label != "Repository" {
		t.Fatalf("fifth top-level menu = %q, want Repository", items[4].Label)
	}
	if items[5].Role != mygo.RoleWindowMenu {
		t.Fatalf("sixth top-level menu role = %q, want windowMenu", items[5].Role)
	}

	want := []struct {
		id    string
		label string
		accel string
	}{
		{menuIDSettings, "Settings…", "CmdOrCtrl+,"},
		{menuIDNewRepository, "New Repository…", ""},
		{menuIDClone, "Clone…", "Shift+CmdOrCtrl+N"},
		{menuIDAddLocalRepository, "Add Local Repository…", "CmdOrCtrl+O"},
		{menuIDAddSSHRepository, "Add SSH Repository…", ""},
		{menuIDLocalChanges, "Local Changes", "CmdOrCtrl+1"},
		{menuIDHistory, "History", "CmdOrCtrl+2"},
		{menuIDRefresh, "Refresh", "CmdOrCtrl+R"},
		{menuIDFetch, "Fetch…", "Shift+CmdOrCtrl+F"},
		{menuIDQuickFetch, "Quick Fetch", "Alt+Shift+CmdOrCtrl+F"},
		{menuIDPull, "Pull…", "Shift+CmdOrCtrl+L"},
		{menuIDQuickPull, "Quick Pull", "Alt+Shift+CmdOrCtrl+L"},
		{menuIDPush, "Push…", "Shift+CmdOrCtrl+P"},
		{menuIDQuickPush, "Quick Push", "Alt+Shift+CmdOrCtrl+P"},
		{menuIDNewBranch, "New Branch…", "Shift+CmdOrCtrl+B"},
		{menuIDNewTag, "New Tag…", "Shift+CmdOrCtrl+G"},
		{menuIDShowInFinder, "Show in Finder", "Ctrl+CmdOrCtrl+O"},
		{menuIDOpenInTerminal, "Open in Terminal", "Ctrl+CmdOrCtrl+T"},
	}
	for _, w := range want {
		it := menuItem(t, rig, w.id)
		if it.Label != w.label {
			t.Errorf("item %q label = %q, want %q", w.id, it.Label, w.label)
		}
		if it.Accelerator != w.accel {
			t.Errorf("item %q accelerator = %q, want %q", w.id, it.Accelerator, w.accel)
		}
		if it.Click == nil {
			t.Errorf("item %q has no Click handler", w.id)
		}
	}
	if menuItem(t, rig, menuIDFetch).Submenu != nil {
		t.Error("Fetch… must be a plain item, not a submenu")
	}

	// The role items carry their platform standard shortcut: Close Window
	// and Enter Full Screen prove the File and View menus kept the pieces
	// the platform fills in.
	byRole := map[mygo.MenuRole]*mygo.MenuItem{}
	for _, it := range allMenuItems(rig.menu.Items()) {
		if it.Role != "" {
			byRole[it.Role] = it
		}
	}
	if it := byRole[mygo.RoleClose]; it == nil {
		t.Error("no Close Window role item in the File menu")
	} else if normalizeAccelerator(it.Accelerator) != normalizeAccelerator("CmdOrCtrl+W") {
		t.Errorf("Close Window accelerator = %q, want CmdOrCtrl+W", it.Accelerator)
	}
	if it := byRole[mygo.RoleToggleFullScreen]; it == nil {
		t.Error("no Enter Full Screen role item in the View menu")
	} else if normalizeAccelerator(it.Accelerator) != normalizeAccelerator("Ctrl+Cmd+F") {
		t.Errorf("Enter Full Screen accelerator = %q, want Ctrl+Cmd+F", it.Accelerator)
	}

	seen := map[string]string{}
	for _, it := range allMenuItems(rig.menu.Items()) {
		if it.Accelerator == "" || len(it.Submenu) > 0 {
			continue
		}
		norm := normalizeAccelerator(it.Accelerator)
		if prev, dup := seen[norm]; dup {
			t.Errorf("accelerator %q shared by %q and %q (%q)", norm, prev, it.ID, it.Label)
		}
		seen[norm] = it.ID + "/" + it.Label + "/" + string(it.Role)
	}
	if len(seen) == 0 {
		t.Error("no accelerators found in the menu bar at all — the scan itself is broken")
	}
}

// Choosing Fetch…, Pull… or Push… from the menu opens exactly the toolbar's
// dialog without starting any git operation.
func TestMenuBarSyncDialogsOpenWithoutSyncing(t *testing.T) {
	repo := newGitRepo(t, "menu-dlg")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	rig := installMenuBar(h)
	openSyncRepo(t, h, repo.StoreRepo())
	rec := installRecordingExecutor(t, h)

	st := syncStateFor(h.app)
	clickMenuItem(t, rig, menuIDFetch)
	if !st.fetchDlg.open {
		t.Fatal("Fetch… did not open the Fetch dialog")
	}
	if rec.count("fetch") != 0 {
		t.Fatalf("opening the Fetch dialog ran %d fetch command(s)", rec.count("fetch"))
	}

	clickMenuItem(t, rig, menuIDPull)
	if !st.pullDlg.open {
		t.Fatal("Pull… did not open the Pull dialog")
	}
	if rec.count("pull") != 0 {
		t.Fatalf("opening the Pull dialog ran %d pull command(s)", rec.count("pull"))
	}

	clickMenuItem(t, rig, menuIDPush)
	if !pushDialogUI(h.app).open {
		t.Fatal("Push… did not open the Push dialog")
	}
	if rec.count("push") != 0 {
		t.Fatalf("opening the Push dialog ran %d push command(s)", rec.count("push"))
	}
	if ran := h.queue.Drain(t); ran != 0 {
		t.Fatalf("opening the dialogs posted %d worker callback(s), want 0", ran)
	}
}

// menuSyncFixture builds a repository with a bare remote and a published
// main branch, opens it in the harness and returns the pieces the Quick
// Fetch/Pull/Push tests share.
func menuSyncFixture(t *testing.T, name string) (*gitRepoFixture, string, *testHarness, *menuBarRig) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	h := newTestApp(t)
	rig := installMenuBar(h)
	openSyncRepo(t, h, repo.StoreRepo())
	return repo, bareDir, h, rig
}

// menuPeerCommit clones the bare repository, adds and pushes one commit as
// a peer would, and returns its hash.
func menuPeerCommit(t *testing.T, bareDir, branch, file, content string) string {
	t.Helper()
	peerDir := filepath.Join(t.TempDir(), "peer")
	runGitCommand(t, "", "clone", bareDir, peerDir)
	runGitCommand(t, peerDir, "config", "user.name", "Peer")
	runGitCommand(t, peerDir, "config", "user.email", "peer@branchline.local")
	runGitCommand(t, peerDir, "checkout", branch)
	if err := os.WriteFile(filepath.Join(peerDir, file), []byte(content), 0o644); err != nil {
		t.Fatalf("write peer file: %v", err)
	}
	runGitCommand(t, peerDir, "add", "--", file)
	runGitCommand(t, peerDir, "commit", "-m", "peer commit")
	runGitCommand(t, peerDir, "push", "origin", branch)
	return strings.TrimSpace(runGitCommand(t, peerDir, "rev-parse", "HEAD"))
}

// Quick Fetch does what holding Option on the toolbar Fetch button does: the
// remote-tracking ref moves to the remote's new commit without a dialog or a
// HEAD move.
func TestMenuBarQuickFetch(t *testing.T) {
	repo, bareDir, h, rig := menuSyncFixture(t, "menu-qfetch")
	peerHead := menuPeerCommit(t, bareDir, "main", "peer.txt", "peer\n")
	headBefore := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	clickMenuItem(t, rig, menuIDQuickFetch)
	h.drain()

	if got := strings.TrimSpace(repo.Git("rev-parse", "origin/main")); got != peerHead {
		t.Fatalf("after Quick Fetch origin/main = %q, want %q", got, peerHead)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != headBefore {
		t.Fatalf("Quick Fetch moved HEAD to %q, want unchanged %q", got, headBefore)
	}
	if syncStateFor(h.app).fetchDlg.open {
		t.Fatal("Quick Fetch opened the Fetch dialog")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Quick Fetch recorded an error: %v", err)
	}
}

// Quick Pull fast-forwards the branch and the working tree to the remote's
// new commit, exactly as holding Option on the toolbar Pull button does.
func TestMenuBarQuickPull(t *testing.T) {
	repo, bareDir, h, rig := menuSyncFixture(t, "menu-qpull")
	peerHead := menuPeerCommit(t, bareDir, "main", "peer.txt", "peer\n")

	clickMenuItem(t, rig, menuIDQuickPull)
	h.drain()

	if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != peerHead {
		t.Fatalf("after Quick Pull HEAD = %q, want %q", got, peerHead)
	}
	if data, err := os.ReadFile(filepath.Join(repo.Path(), "peer.txt")); err != nil || string(data) != "peer\n" {
		t.Fatalf("working tree peer.txt = %q, err=%v; want peer\\n", data, err)
	}
	if syncStateFor(h.app).pullDlg.open {
		t.Fatal("Quick Pull opened the Pull dialog")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Quick Pull recorded an error: %v", err)
	}
}

// Quick Push publishes the local commit to the remote, exactly as holding
// Option on the toolbar Push button does.
func TestMenuBarQuickPush(t *testing.T) {
	repo, bareDir, h, rig := menuSyncFixture(t, "menu-qpush")
	localHead := repo.CommitFile("local.txt", "local\n", "local commit")
	h.app.session.Reload()
	h.drain()

	clickMenuItem(t, rig, menuIDQuickPush)
	h.drain()

	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != localHead {
		t.Fatalf("after Quick Push bare refs/heads/main = %q, want %q", got, localHead)
	}
	if pushDialogUI(h.app).open {
		t.Fatal("Quick Push opened the Push dialog")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Quick Push recorded an error: %v", err)
	}
}

// Without a repository open the six sync items are disabled and choosing
// them does nothing: no dialog, no worker, no error.
func TestMenuBarSyncItemsDisabledWithoutRepo(t *testing.T) {
	h := newTestApp(t)
	rig := installMenuBar(h)
	closeSync(t, h)
	h.ui.Frame()

	for _, id := range []string{menuIDFetch, menuIDQuickFetch, menuIDPull, menuIDQuickPull, menuIDPush, menuIDQuickPush} {
		if it := menuItem(t, rig, id); it.IsEnabled() {
			t.Errorf("item %q enabled without a repository", id)
		}
	}
	for _, id := range []string{menuIDFetch, menuIDQuickFetch, menuIDPull, menuIDQuickPull, menuIDPush, menuIDQuickPush} {
		clickMenuItem(t, rig, id)
	}
	if ran := h.queue.Drain(t); ran != 0 {
		t.Fatalf("disabled menu clicks posted %d worker callback(s), want 0", ran)
	}
	h.ui.Frame()
	if h.app.session.Busy() {
		t.Fatal("a disabled menu click started an operation")
	}
	st := syncStateFor(h.app)
	if st.fetchDlg.open || st.pullDlg.open || pushDialogUI(h.app).open {
		t.Fatal("a disabled menu click opened a dialog")
	}
	if st.syncErr != "" {
		t.Fatalf("a disabled menu click recorded error %q", st.syncErr)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("a disabled menu click recorded action error %v", err)
	}
}

// While an operation is in flight the six sync items are disabled and
// choosing them changes nothing: the running operation keeps its label and
// no second worker starts.
func TestMenuBarSyncItemsDisabledWhileBusy(t *testing.T) {
	repo := newGitRepo(t, "menu-busy")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	rig := installMenuBar(h)
	openSyncRepo(t, h, repo.StoreRepo())

	h.queue.PauseWorkers()
	clickMenuItem(t, rig, menuIDQuickFetch)
	fetchWorker := h.queue.WaitWorker(t)
	h.ui.Frame()

	if !h.app.session.Busy() {
		t.Fatal("the quick fetch did not start while workers were paused")
	}
	for _, id := range []string{menuIDFetch, menuIDQuickFetch, menuIDPull, menuIDQuickPull, menuIDPush, menuIDQuickPush} {
		if it := menuItem(t, rig, id); it.IsEnabled() {
			t.Errorf("item %q enabled while busy", id)
		}
		clickMenuItem(t, rig, id)
	}
	h.ui.Frame()
	if got := h.app.session.BusyLabel(); got != "Fetching" {
		t.Fatalf("busy label = %q after disabled clicks, want Fetching", got)
	}
	if got := gatedLen(h.queue); got != 0 {
		t.Fatalf("disabled clicks spawned %d extra worker(s), want 0", got)
	}

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(fetchWorker)
	h.drain()
}

// The enabled state is applied only when it changes: quiet frames change
// nothing, and the frame the repository turns busy updates exactly the eight
// operation items — the six sync items plus New Branch… and New Tag… — and
// nothing else.
func TestMenuBarEnabledUpdatesOnlyOnChange(t *testing.T) {
	repo := newGitRepo(t, "menu-enable")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	rig := installMenuBar(h)
	openSyncRepo(t, h, repo.StoreRepo())

	managed := []string{
		menuIDFetch, menuIDQuickFetch, menuIDPull, menuIDQuickPull, menuIDPush, menuIDQuickPush,
		menuIDNewBranch, menuIDNewTag, menuIDLocalChanges, menuIDHistory, menuIDShowInFinder, menuIDOpenInTerminal,
	}
	operation := []string{
		menuIDFetch, menuIDQuickFetch, menuIDPull, menuIDQuickPull, menuIDPush, menuIDQuickPush,
		menuIDNewBranch, menuIDNewTag,
	}

	// Opening the repository disabled nothing enabled: the first frame with
	// the menu attached applied each managed item once and no more.
	for _, id := range managed {
		if rig.states[id] != true {
			t.Fatalf("item %q not enabled with an idle repository open; states=%v", id, rig.states)
		}
	}
	rig.resetCounters()
	h.ui.Frame()
	h.ui.Frame()
	if n := rig.totalApplications(); n != 0 {
		t.Fatalf("two unchanged frames applied the enabled state %d time(s), want 0; per item %v", n, rig.applied)
	}

	// The repository turns busy: the operation items flip to disabled, each
	// exactly once, and no other item is touched.
	h.queue.PauseWorkers()
	clickMenuItem(t, rig, menuIDQuickFetch)
	fetchWorker := h.queue.WaitWorker(t)
	rig.resetCounters()
	h.ui.Frame()
	for id := range rig.applied {
		found := false
		for _, op := range operation {
			if id == op {
				found = true
			}
		}
		if !found {
			t.Errorf("busy frame applied item %q, which the busy state does not govern", id)
		}
	}
	for _, id := range operation {
		if rig.applied[id] != 1 {
			t.Errorf("busy frame applied item %q %d time(s), want exactly 1", id, rig.applied[id])
		}
		if rig.states[id] != false {
			t.Errorf("item %q enabled while busy", id)
		}
	}
	if len(rig.applied) != len(operation) {
		t.Errorf("busy frame applied %d distinct item(s), want %d; %v", len(rig.applied), len(operation), rig.applied)
	}

	// More busy frames change nothing more.
	rig.resetCounters()
	h.ui.Frame()
	if n := rig.totalApplications(); n != 0 {
		t.Fatalf("a second busy frame applied the enabled state %d time(s), want 0; %v", n, rig.applied)
	}

	// The operation ends: the same eight items flip back, again once each.
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(fetchWorker)
	rig.resetCounters()
	h.drain()
	for _, id := range operation {
		if rig.applied[id] != 1 {
			t.Errorf("completion frame applied item %q %d time(s), want exactly 1", id, rig.applied[id])
		}
		if rig.states[id] != true {
			t.Errorf("item %q disabled after the operation ended", id)
		}
	}
	if len(rig.applied) != len(operation) {
		t.Errorf("completion frame applied %d distinct item(s), want %d; %v", len(rig.applied), len(operation), rig.applied)
	}
}

// Local Changes and History switch the main page; without a repository they
// are disabled and choosing them navigates nowhere.
func TestMenuBarPageSwitching(t *testing.T) {
	repo := newGitRepo(t, "menu-pages")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	h := newTestApp(t)
	rig := installMenuBar(h)
	h.ui.Frame()

	if menuItem(t, rig, menuIDLocalChanges).IsEnabled() || menuItem(t, rig, menuIDHistory).IsEnabled() {
		t.Fatal("page items enabled without a repository")
	}
	before := h.app.route
	clickMenuItem(t, rig, menuIDHistory)
	clickMenuItem(t, rig, menuIDLocalChanges)
	if h.app.route != before {
		t.Fatalf("clicks without a repository moved the route to %+v", h.app.route)
	}

	h.openRepo(repo.StoreRepo())
	h.drain()

	if !menuItem(t, rig, menuIDLocalChanges).IsEnabled() || !menuItem(t, rig, menuIDHistory).IsEnabled() {
		t.Fatal("page items disabled with a repository open")
	}
	clickMenuItem(t, rig, menuIDHistory)
	if h.app.route.page != pageHistory {
		t.Fatalf("History click left the page at %v, want pageHistory", h.app.route.page)
	}
	clickMenuItem(t, rig, menuIDLocalChanges)
	if h.app.route.page != pageChanges {
		t.Fatalf("Local Changes click left the page at %v, want pageChanges", h.app.route.page)
	}
}

// New Branch… opens the dialog at the current branch's commit when a branch
// is checked out, and at the detached HEAD's commit when it is not.
func TestMenuBarNewBranchStartPoint(t *testing.T) {
	repo := newGitRepo(t, "menu-newbranch")
	first := repo.CommitFile("a.txt", "one\n", "initial commit")

	h := newTestApp(t)
	rig := installMenuBar(h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickMenuItem(t, rig, menuIDNewBranch)
	st := h.app.branchCreateUI()
	if !st.open {
		t.Fatal("New Branch… did not open the dialog")
	}
	if st.sourceRef != "refs/heads/main" {
		t.Fatalf("dialog sourceRef = %q, want refs/heads/main", st.sourceRef)
	}
	if st.expectedOID != first {
		t.Fatalf("dialog start commit = %q, want the main tip %q", st.expectedOID, first)
	}
	h.app.closeBranchCreateDialog()

	// Detached HEAD: the dialog starts at the commit HEAD points at, through
	// the HEAD reference itself.
	repo.Git("checkout", first)
	h.app.session.Reload()
	h.drain()
	if h.app.session.Snapshot().Status.Branch != "" {
		t.Fatalf("HEAD is not detached in the snapshot: %q", h.app.session.Snapshot().Status.Branch)
	}
	clickMenuItem(t, rig, menuIDNewBranch)
	st = h.app.branchCreateUI()
	if !st.open {
		t.Fatal("New Branch… did not open the dialog on a detached HEAD")
	}
	if st.sourceRef != "HEAD" {
		t.Fatalf("detached dialog sourceRef = %q, want HEAD", st.sourceRef)
	}
	if st.expectedOID != first {
		t.Fatalf("detached dialog start commit = %q, want %q", st.expectedOID, first)
	}
	h.app.closeBranchCreateDialog()
}

// New Tag… targets the commit selected on the History page when there is
// one, and HEAD otherwise.
func TestMenuBarNewTagTarget(t *testing.T) {
	repo := newGitRepo(t, "menu-newtag")
	first := repo.CommitFile("a.txt", "one\n", "initial commit")
	second := repo.CommitFile("b.txt", "two\n", "second commit")

	h := newTestApp(t)
	rig := installMenuBar(h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickMenuItem(t, rig, menuIDNewTag)
	st := h.app.tagsUI()
	if !st.newOpen {
		t.Fatal("New Tag… did not open the dialog")
	}
	if st.newTarget != "HEAD" {
		t.Fatalf("tag target without a selection = %q, want HEAD", st.newTarget)
	}

	// A commit selected on the History page becomes the target, with its
	// short hash in the hint.
	h.app.open(route{page: pageHistory})
	h.ui.Frame()
	h.app.historyView.sel = first
	h.app.historyView.commit = git.Commit{Hash: first, Subject: "initial commit"}
	clickMenuItem(t, rig, menuIDNewTag)
	if st.newTarget != first {
		t.Fatalf("tag target with a selection = %q, want the selected commit %q", st.newTarget, first)
	}
	if !strings.Contains(st.newTargetHint, first[:7]) {
		t.Fatalf("tag target hint = %q, want the short hash %q", st.newTargetHint, first[:7])
	}

	// Back on the Changes page — or with no selection — the target is HEAD
	// again, even though the second commit is the tip.
	h.app.open(route{page: pageChanges})
	h.ui.Frame()
	h.app.historyView.sel = ""
	clickMenuItem(t, rig, menuIDNewTag)
	if st.newTarget != "HEAD" {
		t.Fatalf("tag target after leaving the selection = %q, want HEAD", st.newTarget)
	}
	if strings.TrimSpace(repo.Git("rev-parse", "HEAD")) != second {
		t.Fatalf("the repository tip moved during the test; HEAD = %q, want %q", strings.TrimSpace(repo.Git("rev-parse", "HEAD")), second)
	}
}

// The File menu entries reach the same actions as the repository switcher's
// footer, and Settings… opens the Settings window.
func TestMenuBarFileAndSettingsEntries(t *testing.T) {
	h := newTestApp(t)
	rig := installMenuBar(h)
	rec := installSettingsHooks(h)
	closeSync(t, h)

	clickMenuItem(t, rig, menuIDSettings)
	if rec.opened != 1 {
		t.Fatalf("Settings… opened the window %d time(s), want 1", rec.opened)
	}

	clickMenuItem(t, rig, menuIDNewRepository)
	if !h.app.newRepoUI().open {
		t.Fatal("New Repository… did not open its dialog")
	}
	h.app.closeNewRepoDialog(h.app.newRepoUI())

	clickMenuItem(t, rig, menuIDClone)
	if !h.app.cloneUI().open {
		t.Fatal("Clone… did not open its dialog")
	}
	h.app.cloneUI().open = false

	clickMenuItem(t, rig, menuIDAddSSHRepository)
	if !h.app.reposUI().sshOpen {
		t.Fatal("Add SSH Repository… did not open the SSH form")
	}
	h.app.reposUI().sshOpen = false

	// Add Local Repository… runs the same folder picker flow as the switcher:
	// a canceled picker adds nothing, a chosen repository folder opens it.
	picked := ""
	calls := 0
	stubPickLocalFolder(t, func() (string, error) {
		calls++
		return picked, nil
	})
	clickMenuItem(t, rig, menuIDAddLocalRepository)
	if ran := h.queue.Drain(t); ran != 0 {
		t.Fatalf("a canceled picker posted %d callback(s), want 0", ran)
	}
	if calls != 1 {
		t.Fatalf("the picker ran %d time(s), want 1", calls)
	}

	repo := newGitRepo(t, "menu-addlocal")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	picked = repo.Path()
	clickMenuItem(t, rig, menuIDAddLocalRepository)
	h.drain()
	// git resolves the repository to its real path; the temporary
	// directory's /var is a symlink to /private/var on macOS.
	wantPath, err := filepath.EvalSymlinks(repo.Path())
	if err != nil {
		t.Fatalf("resolve repo path: %v", err)
	}
	if !h.app.session.HasRepo() || h.app.session.Meta().Path != wantPath {
		t.Fatalf("Add Local Repository… did not open %q; meta path %q", wantPath, h.app.session.Meta().Path)
	}
}

// Show in Finder and Open in Terminal follow the repository: disabled
// without one, and choosing them with one runs the same actions as the
// shortcuts — Finder only for a local repository, Terminal through the
// terminal of the settings.
func TestMenuBarFinderAndTerminalEntries(t *testing.T) {
	local := newGitRepo(t, "menu-finder")
	local.CommitFile("a.txt", "a\n", "commit")

	h := newTestApp(t)
	rig := installMenuBar(h)
	rec := &openTerminalRecorder{}
	rec.install(h)
	var reveals []string
	h.app.reposUI().showInFinderFn = func(path string) { reveals = append(reveals, path) }

	h.ui.Frame()
	if menuItem(t, rig, menuIDShowInFinder).IsEnabled() || menuItem(t, rig, menuIDOpenInTerminal).IsEnabled() {
		t.Fatal("Finder/Terminal items enabled without a repository")
	}
	clickMenuItem(t, rig, menuIDShowInFinder)
	clickMenuItem(t, rig, menuIDOpenInTerminal)
	if len(reveals) != 0 || len(rec.recorded(t)) != 0 {
		t.Fatalf("clicks without a repository revealed %v and opened %+v, want nothing", reveals, rec.recorded(t))
	}

	h.openRepo(local.StoreRepo())
	h.drain()
	if !menuItem(t, rig, menuIDShowInFinder).IsEnabled() || !menuItem(t, rig, menuIDOpenInTerminal).IsEnabled() {
		t.Fatal("Finder/Terminal items disabled with a repository open")
	}
	clickMenuItem(t, rig, menuIDShowInFinder)
	if len(reveals) != 1 || reveals[0] != local.Path() {
		t.Fatalf("Show in Finder revealed %v, want %q once", reveals, local.Path())
	}
	clickMenuItem(t, rig, menuIDOpenInTerminal)
	h.drain()
	if calls := rec.recorded(t); len(calls) != 1 || calls[0].target != local.Path() {
		t.Fatalf("Open in Terminal ran %+v, want one open of %q", calls, local.Path())
	}
}

// Refresh reloads the open repository: the snapshot picks up a commit made
// behind the application's back, and with no repository the click is silent.
func TestMenuBarRefreshReloadsRepository(t *testing.T) {
	repo := newGitRepo(t, "menu-refresh")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	h := newTestApp(t)
	rig := installMenuBar(h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	second := repo.CommitFile("b.txt", "two\n", "second commit")
	if h.app.session.Snapshot().Status.Head == second {
		t.Fatal("the snapshot already saw the new commit before Refresh — the test proves nothing")
	}
	clickMenuItem(t, rig, menuIDRefresh)
	h.drain()
	if got := h.app.session.Snapshot().Status.Head; got != second {
		t.Fatalf("after Refresh the snapshot HEAD = %q, want %q", got, second)
	}
}
