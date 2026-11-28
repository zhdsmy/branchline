package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/store"
)

// menuSSHRepo is an SSH repository of the list only: nothing ever connects
// to its server, which need not exist.
func menuSSHRepo() store.Repo {
	return store.Repo{
		Kind: store.SSH,
		Name: "sshrepo",
		Host: "box.example.test",
		User: "alice",
		Path: "/srv/app",
	}
}

// assertSwitcherTitle requires the switcher trigger — the button labeled
// Repository Switcher — to show the title text new and no text old, with
// the popup closed. Box comparison rather than text presence: the
// repository's folder keeps its name, which the pages may still show.
func assertSwitcherTitle(t *testing.T, h *testHarness, new, old string) {
	t.Helper()
	trig, ok := h.ui.AccessByRole(ui.RoleButton, "Repository Switcher")
	if !ok {
		t.Fatalf("no Repository Switcher trigger; texts=%q", h.ui.Texts())
	}
	inside := func(text string) (ui.Rect, bool) {
		r, ok := h.ui.Find(text)
		if !ok {
			return ui.Rect{}, false
		}
		within := r.X >= trig.Bounds.X-1 && r.Y >= trig.Bounds.Y-1 &&
			r.X+r.W <= trig.Bounds.X+trig.Bounds.W+1 && r.Y+r.H <= trig.Bounds.Y+trig.Bounds.H+1
		return r, within
	}
	r, within := inside(new)
	if !within {
		t.Fatalf("switcher title does not show %q inside the trigger %+v: box %+v; texts=%q", new, trig.Bounds, r, h.ui.Texts())
	}
	if r, within := inside(old); within {
		t.Fatalf("switcher title still shows the old name %q at %+v inside the trigger %+v; texts=%q", old, r, trig.Bounds, h.ui.Texts())
	}
}

// rightClickRepoRow opens the context menu of the repository row named name
// in the open switcher popup. It aims at the row's open button rather than
// at the name text: the active repository's name also shows in the closed
// trigger's title, which a text lookup could hit first.
func rightClickRepoRow(t *testing.T, h *testHarness, name string) {
	t.Helper()
	info, ok := h.ui.AccessByRole(ui.RoleButton, name)
	if !ok {
		t.Fatalf("no repository row button named %q; texts=%q", name, h.ui.Texts())
	}
	h.ui.RightClickAt(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
	if h.ui.Menu() == nil {
		t.Fatalf("right-clicking the row of %q opened no menu; texts=%q", name, h.ui.Texts())
	}
}

// The repository row's context menu is Open, Show in Finder, Open in
// Terminal, Rename…, Copy Location, a separator and Remove from List, for
// local and SSH repositories alike — except Show in Finder, which only a
// local folder can serve and so disables on the SSH row.
func TestRepoMenuItemsAndEnabledState(t *testing.T) {
	local := newGitRepo(t, "menulocal")
	local.CommitFile("a.txt", "a\n", "commit")

	h := newTestApp(t, local.StoreRepo(), menuSSHRepo())

	want := []string{"Open", "Show in Finder", "Open in Terminal", "Rename…", "Copy Location", "-", "Remove from List"}

	openSwitcherPopup(t, h)
	rightClickRepoRow(t, h, "menulocal")
	if menu := h.ui.Menu(); !slices.Equal(menu, want) {
		t.Fatalf("local row menu = %q, want %q", menu, want)
	}
	h.ui.CloseMenu()
	h.ui.Frame()

	rightClickRepoRow(t, h, "sshrepo")
	if menu := h.ui.Menu(); !slices.Equal(menu, want) {
		t.Fatalf("SSH row menu = %q, want %q", menu, want)
	}
	// The recorder is injected before any choice, so an enabled Show in
	// Finder can never reach the real file manager — and a choice that
	// runs despite the SSH row's disabled state is visible in its calls.
	var shown []string
	h.app.reposUI().showInFinderFn = func(path string) { shown = append(shown, path) }
	if err := h.ui.ChooseMenuItem("Show in Finder"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing Show in Finder on an SSH row = %v, want a disabled error", err)
	}
	if len(shown) != 0 {
		t.Fatalf("Show in Finder ran for an SSH repository: %q", shown)
	}
	// The same item answers on a local row, so the disabled state is a
	// property of the repository kind, not of the item.
	h.ui.CloseMenu()
	h.ui.Frame()
	rightClickRepoRow(t, h, "menulocal")
	if err := h.ui.ChooseMenuItem("Show in Finder"); err != nil {
		t.Fatalf("Show in Finder on a local row = %v, want it enabled", err)
	}
	if len(shown) != 1 || shown[0] != local.Path() {
		t.Fatalf("Show in Finder calls = %q, want one call with %q", shown, local.Path())
	}
}

// Open of the menu switches to the repository exactly as clicking the row
// does, and closes the popup. On the repository already open it changes
// nothing — same session generation — and still closes the popup.
func TestRepoMenuOpenSwitchesRepository(t *testing.T) {
	alpha := newGitRepo(t, "openalpha")
	alphaHash := alpha.CommitFile("a.txt", "a\n", "alpha commit")
	beta := newGitRepo(t, "openbeta")
	betaHash := beta.CommitFile("b.txt", "b\n", "beta commit")

	h := newTestApp(t)
	h.openRepo(alpha.StoreRepo())
	h.openRepo(beta.StoreRepo())
	h.drain()
	if got := h.app.session.Meta().Name; got != "openbeta" {
		t.Fatalf("active after opening = %q, want openbeta", got)
	}

	openSwitcherPopup(t, h)
	rightClickRepoRow(t, h, "openalpha")
	if err := h.ui.ChooseMenuItem("Open"); err != nil {
		t.Fatalf("choose Open: %v; menu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	h.drain()
	if got := h.app.session.Meta().Name; got != "openalpha" {
		t.Fatalf("active after Open = %q, want openalpha", got)
	}
	if got := h.app.session.Snapshot().Status.Head; got != alphaHash {
		t.Fatalf("HEAD after Open = %q, want %q", got, alphaHash)
	}
	if h.app.reposUI().popupOpen {
		t.Fatal("the popup stayed open after Open")
	}

	// Open on the repository already open is a no-op beyond closing the
	// popup: the session does not reload.
	openSwitcherPopup(t, h)
	gen := h.app.session.Generation()
	rightClickRepoRow(t, h, "openalpha")
	if err := h.ui.ChooseMenuItem("Open"); err != nil {
		t.Fatalf("choose Open on the active row: %v; menu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	if h.app.reposUI().popupOpen {
		t.Fatal("the popup stayed open after Open on the active row")
	}
	if got := h.app.session.Generation(); got != gen {
		t.Fatalf("session generation moved to %d on a no-op Open, was %d", got, gen)
	}
	if got := h.app.session.Meta().Name; got != "openalpha" {
		t.Fatalf("active after the no-op Open = %q, want openalpha", got)
	}
	h.drain()
	// beta is untouched on disk.
	if got := strings.TrimSpace(runGitCommand(t, beta.Path(), "rev-parse", "HEAD")); got != betaHash {
		t.Fatalf("beta HEAD = %q, want %q", got, betaHash)
	}
}

// Copy Location puts exactly what the row shows as the repository's address
// on the clipboard: the folder path of a local repository, the SSH address
// of one on a server.
func TestRepoMenuCopyLocation(t *testing.T) {
	local := newGitRepo(t, "copylocal")
	local.CommitFile("a.txt", "a\n", "commit")
	ssh := menuSSHRepo()

	h := newTestApp(t, local.StoreRepo(), ssh)

	openSwitcherPopup(t, h)
	rightClickRepoRow(t, h, "copylocal")
	if err := h.ui.ChooseMenuItem("Copy Location"); err != nil {
		t.Fatalf("choose Copy Location: %v; menu=%q", err, h.ui.Menu())
	}
	if got := h.ui.Clipboard(); got != local.Path() {
		t.Fatalf("clipboard = %q, want exactly %q", got, local.Path())
	}

	rightClickRepoRow(t, h, "sshrepo")
	if err := h.ui.ChooseMenuItem("Copy Location"); err != nil {
		t.Fatalf("choose Copy Location on the SSH row: %v; menu=%q", err, h.ui.Menu())
	}
	if want := ssh.Location(); h.ui.Clipboard() != want {
		t.Fatalf("clipboard = %q, want exactly %q", h.ui.Clipboard(), want)
	}
}

// Rename… opens the Rename Repository dialog with the current name selected,
// so typing replaces it; confirming renames the repository in the store, the
// switcher's list and — for the repository open — the switcher's title show
// the new name at once, and a restart of the app still shows it. The folder
// itself never moves.
func TestRepoMenuRenameSuccess(t *testing.T) {
	// openStartupRepo honors BRANCHLINE_OPEN when set: pin it empty so a
	// developer machine's environment cannot redirect the "restart".
	t.Setenv("BRANCHLINE_OPEN", "")
	repo := newGitRepo(t, "renamerepo")
	repo.CommitFile("a.txt", "a\n", "commit")

	storePath := filepath.Join(t.TempDir(), "repositories.json")
	first := newHarnessOnStore(t, storePath)
	saved := first.openRepo(repo.StoreRepo())
	first.drain()

	openSwitcherPopup(t, first)
	rightClickRepoRow(t, first, "renamerepo")
	if err := first.ui.ChooseMenuItem("Rename…"); err != nil {
		t.Fatalf("choose Rename…: %v; menu=%q", err, first.ui.Menu())
	}
	first.ui.Frame()
	if !hasExactText(first.ui, "Rename Repository") {
		t.Fatalf("rename dialog did not open; texts=%q", first.ui.Texts())
	}
	field, ok := first.ui.AccessByRole(ui.RoleTextField, "Repository Name")
	if !ok {
		t.Fatalf("Repository Name input missing; texts=%q", first.ui.Texts())
	}
	if field.Value != "renamerepo" {
		t.Fatalf("input starts as %q, want the current name", field.Value)
	}
	// The untouched name is no rename, so confirm starts disabled.
	if btn, ok := first.ui.AccessByRole(ui.RoleButton, "Confirm Rename Repository"); !ok || !btn.Disabled {
		t.Fatalf("Confirm Rename Repository with the untouched name = %+v (found %v), want disabled", btn, ok)
	}

	// The current name is fully selected: typing replaces it instead of
	// inserting into it.
	first.ui.Type("renamed")
	first.ui.Frame()
	if field, ok := first.ui.AccessByRole(ui.RoleTextField, "Repository Name"); !ok || field.Value != "renamed" {
		t.Fatalf("input after typing = %+v (found %v), want exactly %q — the initial selection was not replaced", field, ok, "renamed")
	}
	if btn, ok := first.ui.AccessByRole(ui.RoleButton, "Confirm Rename Repository"); !ok || btn.Disabled {
		t.Fatalf("Confirm Rename Repository with a valid name = %+v, want enabled", btn)
	}
	if err := first.ui.Click("Confirm Rename Repository"); err != nil {
		t.Fatalf("click Confirm Rename Repository: %v", err)
	}
	first.ui.Frame()

	if hasExactText(first.ui, "Rename Repository") {
		t.Fatal("the rename dialog stayed open after a successful rename")
	}
	if got, ok := first.store.Get(saved.ID); !ok || got.Name != "renamed" {
		t.Fatalf("store name = %+v (found %v), want renamed", got, ok)
	}

	// The confirm click landed outside the popup, which closed it. The
	// switcher's title must read the renamed store entry, not the session's
	// copy taken when the repository opened. The folder keeps its old name,
	// which the Local Changes heading shows, so compare boxes: the trigger
	// must hold the new name's text and no old name text.
	if first.app.reposUI().popupOpen {
		t.Fatal("the popup stayed open after the rename; the title check needs it closed")
	}
	assertSwitcherTitle(t, first, "renamed", "renamerepo")

	// A restart — a new store and app on the same file — keeps the name.
	second := newHarnessOnStore(t, storePath)
	second.app.openStartupRepo()
	second.drain()
	if !second.app.session.HasRepo() {
		t.Fatal("restart did not restore the repository")
	}
	if got := second.app.session.Meta().Name; got != "renamed" {
		t.Fatalf("restored name = %q, want renamed", got)
	}
	assertSwitcherTitle(t, second, "renamed", "renamerepo")

	// The folder never moved.
	if _, err := os.Stat(filepath.Join(repo.Path(), ".git")); err != nil {
		t.Fatalf("repository folder moved or deleted: %v", err)
	}
}

// The Rename button stays disabled while the trimmed name is blank or the
// same as the current one, and enables again for a real change.
func TestRepoMenuRenameDisabledStates(t *testing.T) {
	repo := newGitRepo(t, "disabledrepo")
	repo.CommitFile("a.txt", "a\n", "commit")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openSwitcherPopup(t, h)
	rightClickRepoRow(t, h, "disabledrepo")
	if err := h.ui.ChooseMenuItem("Rename…"); err != nil {
		t.Fatalf("choose Rename…: %v; menu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()

	confirm := func() bool {
		t.Helper()
		btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Rename Repository")
		if !ok {
			t.Fatalf("Confirm Rename Repository missing; texts=%q", h.ui.Texts())
		}
		return !btn.Disabled
	}
	if confirm() {
		t.Fatal("confirm enabled for the unchanged name")
	}

	focusField(t, h, "Repository Name")
	clearInput(t, h.ui)
	if confirm() {
		t.Fatal("confirm enabled for an empty name")
	}
	h.ui.Type("   ")
	h.ui.Frame()
	if confirm() {
		t.Fatal("confirm enabled for a blank name")
	}

	clearInput(t, h.ui)
	h.ui.Type("disabledrepo")
	h.ui.Frame()
	if confirm() {
		t.Fatal("confirm enabled for the same name again")
	}

	clearInput(t, h.ui)
	h.ui.Type("other name")
	h.ui.Frame()
	if !confirm() {
		t.Fatal("confirm disabled for a real change")
	}

	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if hasExactText(h.ui, "Rename Repository") {
		t.Fatal("Cancel left the rename dialog open")
	}
	if got := h.store.Repos()[0].Name; got != "disabledrepo" {
		t.Fatalf("canceling renamed the repository to %q", got)
	}
}

// When the store cannot persist the rename, the error shows inside the
// dialog, which stays open, and the name in the list and the store is the
// old one — never a name the disk does not hold.
func TestRepoMenuRenameSaveFailure(t *testing.T) {
	repo := newGitRepo(t, "failrepo")
	repo.CommitFile("a.txt", "a\n", "commit")

	storePath := filepath.Join(t.TempDir(), "repositories.json")
	h := newHarnessOnStore(t, storePath)
	saved := h.openRepo(repo.StoreRepo())
	h.drain()

	// A directory at the temporary file's path blocks every save, without
	// touching permissions.
	if err := os.Mkdir(storePath+".tmp", 0o700); err != nil {
		t.Fatalf("block the store save: %v", err)
	}

	openSwitcherPopup(t, h)
	rightClickRepoRow(t, h, "failrepo")
	if err := h.ui.ChooseMenuItem("Rename…"); err != nil {
		t.Fatalf("choose Rename…: %v; menu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	h.ui.Type("newname")
	h.ui.Frame()
	if err := h.ui.Click("Confirm Rename Repository"); err != nil {
		t.Fatalf("click Confirm Rename Repository: %v", err)
	}
	h.ui.Frame()

	if !hasExactText(h.ui, "Rename Repository") {
		t.Fatal("the rename dialog closed although the save failed")
	}
	if !h.ui.HasText("Cannot save the repository list") {
		t.Fatalf("save failure missing inside the dialog; texts=%q", h.ui.Texts())
	}
	if got, ok := h.store.Get(saved.ID); !ok || got.Name != "failrepo" {
		t.Fatalf("store name after the failed save = %+v (found %v), want the old name", got, ok)
	}

	// With the block gone the same dialog renames, so the test does not
	// pass merely because renaming is broken entirely.
	if err := os.Remove(storePath + ".tmp"); err != nil {
		t.Fatalf("unblock the store save: %v", err)
	}
	if err := h.ui.Click("Confirm Rename Repository"); err != nil {
		t.Fatalf("re-click Confirm Rename Repository: %v", err)
	}
	h.ui.Frame()
	if hasExactText(h.ui, "Rename Repository") {
		t.Fatal("the rename dialog stayed open after the unblocked save")
	}
	if got, ok := h.store.Get(saved.ID); !ok || got.Name != "newname" {
		t.Fatalf("store name after the unblocked save = %+v (found %v), want newname", got, ok)
	}
}

// Remove from List is the row-end minus button's removal: the entry leaves
// the repository list and the files on disk stay byte for byte.
func TestRepoMenuRemoveFromListKeepsFiles(t *testing.T) {
	kept := newGitRepo(t, "keptrepo")
	kept.CommitFile("k.txt", "k\n", "kept commit")
	victim := newGitRepo(t, "victimrepo")
	victim.CommitFile("v.txt", "v\n", "victim commit")

	h := newTestApp(t)
	h.openRepo(kept.StoreRepo())
	h.openRepo(victim.StoreRepo())
	h.drain()

	openSwitcherPopup(t, h)
	rightClickRepoRow(t, h, "victimrepo")
	if err := h.ui.ChooseMenuItem("Remove from List"); err != nil {
		t.Fatalf("choose Remove from List: %v; menu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	h.drain()

	if repos := h.store.Repos(); len(repos) != 1 || repos[0].Name != "keptrepo" {
		t.Fatalf("store after Remove from List = %+v, want only keptrepo", repos)
	}
	// victim was active: removing it opens the first remaining repository,
	// exactly as the minus button does.
	if got := h.app.session.Meta().Name; got != "keptrepo" {
		t.Fatalf("active after removing the active repository = %q, want keptrepo", got)
	}
	// Nothing on disk was touched.
	if _, err := os.Stat(filepath.Join(victim.Path(), ".git")); err != nil {
		t.Fatalf("victim .git missing after Remove from List: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(victim.Path(), "v.txt")); err != nil || string(got) != "v\n" {
		t.Fatalf("victim v.txt = %q err %v, want intact", got, err)
	}
}

// Shift+F10 on a focused repository row opens the same menu as the
// right-click, and choosing Rename… from it opens the rename dialog.
func TestRepoMenuShiftF10OpensMenu(t *testing.T) {
	repo := newGitRepo(t, "keyrepo")
	repo.CommitFile("a.txt", "a\n", "commit")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Opening the popup focuses its trigger. The popup's content follows
	// its anchor in the focus order, so the next Tab lands on the first
	// row's open button — the only focusable element below with a context
	// menu — and the menu key opens its menu.
	openSwitcherPopup(t, h)
	want := []string{"Open", "Show in Finder", "Open in Terminal", "Rename…", "Copy Location", "-", "Remove from List"}
	menu := []string(nil)
	for range 12 {
		h.ui.Key(0, ui.KeyTab)
		h.ui.Key(ui.Shift, ui.KeyF10)
		menu = h.ui.Menu()
		if slices.Equal(menu, want) {
			break
		}
		if menu != nil {
			h.ui.CloseMenu()
		}
		h.ui.Frame()
	}
	if !slices.Equal(menu, want) {
		t.Fatalf("Shift+F10 menu = %q after tabbing through the popup, want %q; texts=%q", menu, want, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem("Rename…"); err != nil {
		t.Fatalf("choose Rename… from the keyboard menu: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Rename Repository") {
		t.Fatalf("rename dialog did not open from the keyboard menu; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}
