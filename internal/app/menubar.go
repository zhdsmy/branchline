package app

import (
	"github.com/egoist/mygo"

	"github.com/zhdsmy/branchline/internal/store"
)

// Menu item IDs of the application menu bar. The dynamic enabled state is
// keyed by these IDs, and tests find items through them with Menu.ItemByID.
const (
	menuIDSettings           = "menu.settings"
	menuIDNewRepository      = "menu.file.new-repository"
	menuIDClone              = "menu.file.clone"
	menuIDAddLocalRepository = "menu.file.add-local-repository"
	menuIDAddSSHRepository   = "menu.file.add-ssh-repository"
	menuIDLocalChanges       = "menu.view.local-changes"
	menuIDHistory            = "menu.view.history"
	menuIDRefresh            = "menu.view.refresh"
	menuIDFetch              = "menu.repository.fetch"
	menuIDQuickFetch         = "menu.repository.quick-fetch"
	menuIDPull               = "menu.repository.pull"
	menuIDQuickPull          = "menu.repository.quick-pull"
	menuIDPush               = "menu.repository.push"
	menuIDQuickPush          = "menu.repository.quick-push"
	menuIDSaveStash          = "menu.repository.save-stash"
	menuIDQuickStash         = "menu.repository.quick-stash"
	menuIDNewBranch          = "menu.repository.new-branch"
	menuIDNewTag             = "menu.repository.new-tag"
	menuIDShowInFinder       = "menu.repository.show-in-finder"
	menuIDOpenInTerminal     = "menu.repository.open-in-terminal"
	menuIDOpenInEditor       = "menu.repository.open-in-editor"
)

// appMenuBar builds the whole application menu bar: the application menu with
// Settings…, File, Edit, View, Repository and Window. The application menus
// Run installs close over the mainApp variable, so getApp resolves the app at
// click time and a click arriving before Run's WhenReady assigned it is a
// no-op; tests pass their own app's getter instead.
//
// A Click runs on the main thread, where the application state lives, so it
// mutates that state directly rather than posting. Menu clicks are native
// menu events, not input of the window's content, so nothing builds a frame
// for them: the redraw after the action is what makes an opened dialog or a
// switched page show.
//
// While the main window shows a dialog the items of the File, View and
// Repository menus do nothing: their click could arrive through an
// accelerator the native menu answers even for a disabled item, or before
// the frame that disables them, and a dialog opened or an operation started
// on top of the open one is exactly what the gate exists to refuse. The
// click gate reads the same source as the enabled state — a dialog counted
// in the last frame of the main window, applied to exactly the items
// menuBarEnabledStates manages — so the two never disagree. Settings… is
// exempt: the Settings window is not part of the main window, and the Edit
// menu stays live for copying out of a dialog's text fields.
//
// The enabled state of the managed items is not decided here: the items
// start enabled, and the per-frame syncMenuBarEnabled applies the real
// state once the menu is attached.
func appMenuBar(getApp func() *app) *mygo.Menu {
	click := func(fn func(a *app)) func(*mygo.MenuItem, *mygo.Window) {
		return func(it *mygo.MenuItem, _ *mygo.Window) {
			a := getApp()
			if a == nil {
				return
			}
			if _, managed := a.menuBarEnabledStates()[it.ID]; managed && a.dialogOpen {
				return
			}
			fn(a)
			a.redraw()
		}
	}
	appName := mygo.App.Name()
	return mygo.NewMenu([]*mygo.MenuItem{
		{Label: appName, Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout},
			mygo.Separator(),
			{ID: menuIDSettings, Label: "Settings…", Accelerator: "CmdOrCtrl+,", Click: click(func(a *app) {
				a.openSettings()
			})},
			mygo.Separator(),
			{Role: mygo.RoleServices},
			mygo.Separator(),
			{Role: mygo.RoleHide},
			{Role: mygo.RoleHideOthers},
			{Role: mygo.RoleUnhide},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		}},
		{Label: "File", Submenu: []*mygo.MenuItem{
			{ID: menuIDNewRepository, Label: "New Repository…", Click: click(func(a *app) {
				a.showNewRepoDialog()
			})},
			{ID: menuIDClone, Label: "Clone…", Accelerator: "Shift+CmdOrCtrl+N", Click: click(func(a *app) {
				a.showCloneDialog()
			})},
			{ID: menuIDAddLocalRepository, Label: "Add Local Repository…", Accelerator: "CmdOrCtrl+O", Click: click(func(a *app) {
				a.addLocalRepo(a.reposUI())
			})},
			{ID: menuIDAddSSHRepository, Label: "Add SSH Repository…", Click: click(func(a *app) {
				a.reposUI().showSSHForm()
			})},
			mygo.Separator(),
			{Role: mygo.RoleClose},
		}},
		{Role: mygo.RoleEditMenu},
		{Label: "View", Submenu: []*mygo.MenuItem{
			{ID: menuIDLocalChanges, Label: "Local Changes", Accelerator: "CmdOrCtrl+1", Click: click(func(a *app) {
				a.menuShowPage(pageChanges)
			})},
			{ID: menuIDHistory, Label: "History", Accelerator: "CmdOrCtrl+2", Click: click(func(a *app) {
				a.menuShowPage(pageHistory)
			})},
			mygo.Separator(),
			{ID: menuIDRefresh, Label: "Refresh", Accelerator: "CmdOrCtrl+R", Click: click(func(a *app) {
				a.session.Reload()
			})},
			mygo.Separator(),
			{Role: mygo.RoleToggleFullScreen},
		}},
		{Label: "Repository", Submenu: []*mygo.MenuItem{
			{ID: menuIDFetch, Label: "Fetch…", Accelerator: "Shift+CmdOrCtrl+F", Click: click(func(a *app) {
				if a.syncActionsEnabled() {
					a.openFetchDialog()
				}
			})},
			{ID: menuIDQuickFetch, Label: "Quick Fetch", Accelerator: "Alt+Shift+CmdOrCtrl+F", Click: click(func(a *app) {
				if a.syncActionsEnabled() {
					a.syncFetch()
				}
			})},
			{ID: menuIDPull, Label: "Pull…", Accelerator: "Shift+CmdOrCtrl+L", Click: click(func(a *app) {
				if a.syncActionsEnabled() {
					a.openPullDialog()
				}
			})},
			{ID: menuIDQuickPull, Label: "Quick Pull", Accelerator: "Alt+Shift+CmdOrCtrl+L", Click: click(func(a *app) {
				if a.syncActionsEnabled() {
					a.syncPull()
				}
			})},
			{ID: menuIDPush, Label: "Push…", Accelerator: "Shift+CmdOrCtrl+P", Click: click(func(a *app) {
				if a.syncActionsEnabled() {
					a.openPushDialog()
				}
			})},
			{ID: menuIDQuickPush, Label: "Quick Push", Accelerator: "Alt+Shift+CmdOrCtrl+P", Click: click(func(a *app) {
				if a.syncActionsEnabled() {
					a.syncPush()
				}
			})},
			mygo.Separator(),
			{ID: menuIDSaveStash, Label: "Save Stash…", Accelerator: "Shift+CmdOrCtrl+H", Click: click(func(a *app) {
				if a.session.HasRepo() {
					a.openSaveStashDialog()
				}
			})},
			{ID: menuIDQuickStash, Label: "Quick Stash", Accelerator: "Alt+Shift+CmdOrCtrl+H", Click: click(func(a *app) {
				a.quickStash()
			})},
			mygo.Separator(),
			{ID: menuIDNewBranch, Label: "New Branch…", Accelerator: "Shift+CmdOrCtrl+B", Click: click(func(a *app) {
				a.menuOpenBranchCreate()
			})},
			{ID: menuIDNewTag, Label: "New Tag…", Accelerator: "Shift+CmdOrCtrl+G", Click: click(func(a *app) {
				if a.menuCreateEnabled() {
					a.openNewTagDialog("")
				}
			})},
			mygo.Separator(),
			{ID: menuIDShowInFinder, Label: "Show in Finder", Accelerator: "Ctrl+CmdOrCtrl+O", Click: click(func(a *app) {
				a.showActiveInFinder()
			})},
			{ID: menuIDOpenInTerminal, Label: "Open in Terminal", Accelerator: "Ctrl+CmdOrCtrl+T", Click: click(func(a *app) {
				a.openActiveInTerminal()
			})},
			{ID: menuIDOpenInEditor, Label: "Open in Editor", Click: click(func(a *app) {
				if a.menuOpenInEditorEnabled() {
					a.openInEditor(a.session.Meta())
				}
			})},
		}},
		{Role: mygo.RoleWindowMenu},
	})
}

// menuShowPage switches the main page from the View menu. The items are
// disabled without an open repository, and the click mirrors that condition,
// so a click arriving before the per-frame update cannot navigate an empty
// window either.
func (a *app) menuShowPage(page pageKind) {
	if !a.session.HasRepo() {
		return
	}
	a.open(route{page: page})
}

// menuCreateEnabled is the enabled condition of the New Branch… and New Tag…
// menu items, the menu spelling of the branch row context menu's condition:
// no write in flight and a commit to start from. The menu always starts at
// the current branch or HEAD, so the row's per-row hash is the snapshot HEAD.
func (a *app) menuCreateEnabled() bool {
	return !a.session.Busy() && a.session.Snapshot().Status.Head != ""
}

// menuOpenBranchCreate opens the New Branch dialog from the Repository menu:
// starting at the current branch's commit, or at the commit HEAD points at
// when HEAD is detached. The source reference the dialog re-reads in its
// write guard is the branch's full reference for a branch, "HEAD" for a
// detached head; both resolve through the same rev-parse the guard runs. A
// click without a determinable start commit — no repository, an unborn
// branch, a write in flight — does nothing, mirroring the disabled item.
func (a *app) menuOpenBranchCreate() {
	if !a.menuCreateEnabled() {
		return
	}
	snap := a.session.Snapshot()
	hash := snap.Status.Head
	sourceRef, label := "HEAD", "HEAD"
	if cur := currentBranchOf(snap.Refs); cur != nil && cur.Hash != "" {
		sourceRef, label, hash = cur.FullName, cur.Name, cur.Hash
	}
	a.openBranchCreateDialog(sourceRef, label, hash)
}

// menuOpenInEditorEnabled is the enabled condition of the Open in Editor
// menu item, the menu spelling of the repository row's context menu: a
// repository on this Mac. A repository on a server has no local folder the
// editor could open, and without a repository there is nothing to open.
func (a *app) menuOpenInEditorEnabled() bool {
	return a.session.HasRepo() && a.session.Meta().Kind != store.SSH
}

// menuBarEnabledStates computes the enabled state of every menu item the
// menu bar manages. The state follows the repository first: the six sync
// items share the toolbar's condition, New Branch… and New Tag… share the
// create condition of the reference context menus, Quick Stash shares its
// own working-tree condition, Open in Editor needs a repository on this
// Mac, and the page, Finder, Terminal and Save Stash… items need only an
// open repository, while the File menu's four entries and Refresh have no
// repository side at all. Over that the dialog gate of the main window
// sits: a frame that drew a dialog disables every managed item, so nothing
// a menu click would do — opening a second dialog over the first, starting
// a git operation, moving the page under the dialog — runs while one is
// open. Settings… is no entry of the table on purpose: the Settings window
// is not part of the main window the gate watches, and opening it never
// conflicts with a dialog.
func (a *app) menuBarEnabledStates() map[string]bool {
	syncOK := a.syncActionsEnabled()
	createOK := a.menuCreateEnabled()
	hasRepo := a.session.HasRepo()
	states := map[string]bool{
		menuIDNewRepository:      true,
		menuIDClone:              true,
		menuIDAddLocalRepository: true,
		menuIDAddSSHRepository:   true,
		menuIDLocalChanges:       hasRepo,
		menuIDHistory:            hasRepo,
		menuIDRefresh:            true,
		menuIDFetch:              syncOK,
		menuIDQuickFetch:         syncOK,
		menuIDPull:               syncOK,
		menuIDQuickPull:          syncOK,
		menuIDPush:               syncOK,
		menuIDQuickPush:          syncOK,
		menuIDSaveStash:          hasRepo,
		menuIDQuickStash:         a.quickStashEnabled(),
		menuIDNewBranch:          createOK,
		menuIDNewTag:             createOK,
		menuIDShowInFinder:       hasRepo,
		menuIDOpenInTerminal:     hasRepo,
		menuIDOpenInEditor:       a.menuOpenInEditorEnabled(),
	}
	if a.dialogOpen {
		for id := range states {
			states[id] = false
		}
	}
	return states
}

// syncMenuBarEnabled applies the enabled state of the managed menu items
// once per frame. MenuItem.SetEnabled posts a native menu update for every
// call, so only a change is applied: the last state applied to an item is
// remembered in a.menubarEnabled, and an item absent from it — the first
// frame after the menu was attached — is applied once, since the template
// starts every item enabled.
func (a *app) syncMenuBarEnabled() {
	if a.menubar == nil {
		return
	}
	if a.menubarEnabled == nil {
		a.menubarEnabled = map[string]bool{}
	}
	apply := a.menuSetEnabledFn
	if apply == nil {
		apply = func(it *mygo.MenuItem, enabled bool) { it.SetEnabled(enabled) }
	}
	for id, want := range a.menuBarEnabledStates() {
		if got, ok := a.menubarEnabled[id]; ok && got == want {
			continue
		}
		a.menubarEnabled[id] = want
		if it := a.menubar.ItemByID(id); it != nil {
			apply(it, want)
		}
	}
}
