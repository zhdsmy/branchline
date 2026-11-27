package app

import (
	"log"
	"os"
	"reflect"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

type pageKind int

const (
	pageChanges pageKind = iota
	pageHistory
)

type route struct {
	page   pageKind
	repoID string
	ref    string
}

// app owns the single-window UI state, routing history, repository store, and
// active repository session. All fields are accessed on the UI thread;
// background work publishes updates exclusively through post.
type app struct {
	win       *mygo.Window
	store     *store.Store
	session   *repoSession
	pal       palette
	darkKnown bool
	lastDark  bool
	themeGen  int

	route          route
	history        []route
	forward        []route
	selectedRepoID string

	navScroll  ui.ScrollState
	pageScroll ui.ScrollState
	scrolled   route
	underTop   bool

	branchesCollapsed bool
	remotesCollapsed  bool
	tagsCollapsed     bool

	// Per-feature UI states defined in their owning ui_*.go files. The fields
	// below are the states mounted directly on app; newer features instead
	// keep their state in featureStates through featureState, so adding a
	// feature does not edit this struct.
	featureStates map[reflect.Type]any
	historyView   historyState
	changesView   changesState
	refsView      refsState
	reposView     reposState
	syncView      syncState
	settings      settingsState

	// settingsWin is the open Settings window, nil while closed.
	// settingsOpen mirrors it for headless tests, whose window hook creates
	// no window: both are set when the window opens and cleared by the
	// window's OnClosed callback.
	settingsWin  *mygo.Window
	settingsOpen bool

	toast string

	// Test hooks: nil in production.
	postFn     func(func())
	spawnFn    func(func())
	openRepoFn func(store.Repo) *git.Repo

	// Settings hooks: production implementations assigned in newApp,
	// replaced by recorders in tests.
	openSettingsWindowFn func()
	applyAppearanceFn    func(mygo.ThemeSource)
}

func newApp(s *store.Store) *app {
	if s == nil {
		s, _ = store.Open("")
	}
	a := &app{
		store: s,
		route: route{page: pageChanges},
	}
	a.session = newRepoSession(a.post, a.spawn, a.buildGitRepo)
	a.openSettingsWindowFn = a.createSettingsWindow
	a.applyAppearanceFn = mygo.Theme.SetSource
	a.session.settingsFn = s.Settings
	a.session.ignoreSpace = s.Settings().IgnoreSpace
	a.session.diffChangesOnly = s.Settings().DiffChangesOnly
	if s.Settings().DiffContextSaved {
		a.session.diffContextLines = s.Settings().DiffContextLines
		a.session.diffContextSaved = true
	}
	a.session.diffWrap = s.Settings().DiffWrap
	a.session.dateOrder = s.Settings().DateOrder
	a.session.saveIgnoreSpace = func(v bool) error {
		return saveIgnoreSpacePref(a, v)
	}
	a.session.saveDiffChangesOnly = func(v bool) error {
		return saveDiffChangesOnlyPref(a, v)
	}
	a.session.saveDiffContextLines = func(v int) error {
		return saveDiffContextLinesPref(a, v)
	}
	a.session.saveDiffWrap = func(v bool) error {
		return saveDiffWrapPref(a, v)
	}
	return a
}

func (a *app) post(fn func()) {
	if a.postFn != nil {
		a.postFn(fn)
		return
	}
	if a.win == nil {
		return
	}
	a.win.Update(func() {
		fn()
		a.redraw()
	})
}

func (a *app) spawn(fn func()) {
	if a.spawnFn != nil {
		a.spawnFn(fn)
		return
	}
	go fn()
}

func (a *app) buildGitRepo(meta store.Repo) *git.Repo {
	if a.openRepoFn != nil {
		return a.openRepoFn(meta)
	}
	if a.store != nil {
		prefs := a.store.Settings()
		return git.New(meta.ExecutorWith(prefs.GitPath, prefs.Trace(), prefs.EnvPath))
	}
	return defaultOpenGitRepo(meta)
}

func (a *app) redraw() {
	if a.win != nil {
		a.win.Invalidate()
	}
}

func (a *app) activeRepoID() string {
	if a.session == nil || !a.session.HasRepo() {
		return ""
	}
	return a.session.Meta().ID
}

// openRepo opens meta in the active session and records it as the active
// repository in the store when it has a persisted ID.
func (a *app) openRepo(meta store.Repo) {
	if a.activeRepoID() != meta.ID {
		a.history = nil
		a.forward = nil
	}
	a.selectedRepoID = meta.ID
	a.route.repoID = meta.ID
	a.session.Open(meta)
	if a.store != nil && meta.ID != "" {
		_ = a.store.Touch(meta.ID)
		_ = a.store.SetTabSession([]string{meta.ID}, meta.ID, true)
	}
}

// switchRepoByID opens the repository identified by id from the store.
func (a *app) switchRepoByID(id string) bool {
	if a.store == nil || id == "" {
		return false
	}
	if id == a.activeRepoID() {
		return true
	}
	meta, ok := a.store.Get(id)
	if !ok {
		return false
	}
	a.openRepo(meta)
	return true
}

// openStartupRepo opens BRANCHLINE_OPEN when set, otherwise the last active or
// most recently opened repository stored in internal/store. When none exist,
// the window stays in the empty repository state.
func (a *app) openStartupRepo() {
	if raw := strings.TrimSpace(os.Getenv("BRANCHLINE_OPEN")); raw != "" {
		target, err := resolveRepoTarget(raw)
		if err != nil {
			a.toast = "Cannot open BRANCHLINE_OPEN: " + err.Error()
			return
		}
		if a.store != nil {
			if saved, addErr := a.store.Add(target); addErr == nil {
				target = saved
			}
		}
		a.openRepo(target)
		return
	}
	if a.store == nil {
		return
	}
	if activeID, set := a.store.ActiveTab(); set && activeID != "" {
		if meta, ok := a.store.Get(activeID); ok {
			a.openRepo(meta)
			return
		}
	}
	if repos := a.store.Repos(); len(repos) > 0 {
		a.openRepo(repos[0])
	}
}

func (a *app) showMainWindow() {
	if a.win == nil {
		return
	}
	if a.win.IsMinimized() {
		a.win.Restore()
	}
	a.win.Show()
	a.win.Focus()
	mygo.App.Focus()
}

func openUserStore() *store.Store {
	s, err := openUserStoreWith(os.LookupEnv, func() (string, error) {
		return mygo.App.Path(mygo.PathUserData)
	})
	if err != nil {
		log.Printf("branchline: open store: %v", err)
		s, _ = store.Open("")
	}
	return s
}

// Run starts the Branchline desktop application and blocks until it quits.
func Run() error {
	dataDir, dataDirOverridden, err := configureDataDir(os.LookupEnv, mygo.App.SetPath)
	if err != nil {
		return err
	}
	if !mygo.App.RequestSingleInstanceLock() {
		if dataDirOverridden {
			return errDataDirSingleInstance(dataDir)
		}
		return nil
	}
	var mainApp *app
	mygo.App.WhenReady(func() {
		a := newApp(openUserStore())
		a.applyStartupAppearance()
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:                mygo.App.Name(),
			Width:                1180,
			Height:               780,
			MinWidth:             880,
			MinHeight:            580,
			StateKey:             "main",
			TitleBarStyle:        mygo.TitleBarHidden,
			TitleBarHeight:       topBarTotalHeight,
			TrafficLightPosition: &mygo.Point{X: 20, Y: 20},
			Vibrancy:             mygo.VibrancySidebar,
			BackgroundColor:      "light-dark(#fbfbfc, #151619)",
			Content:              ui.View(a.view),
		})
		mainApp = a
		mygo.App.OnQuit(a.closeSyncUI)
		startAutoRefresh(a)
		a.openStartupRepo()
	})
	mygo.App.OnActivate(func(bool) {
		if mainApp != nil {
			mainApp.showMainWindow()
		}
	})
	mygo.App.OnSecondInstance(func([]string, string) {
		if mainApp != nil {
			mainApp.showMainWindow()
		}
	})
	mygo.App.OnWindowAllClosed(func() {
		mygo.App.Quit()
	})
	// The first menu keeps the system's application-menu roles and adds
	// Settings… after About; the other menus stay the default role menus.
	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Label: mygo.App.Name(), Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout},
			mygo.Separator(),
			{Label: "Settings…", Accelerator: "CmdOrCtrl+,", Click: func(*mygo.MenuItem, *mygo.Window) {
				if mainApp != nil {
					mainApp.openSettings()
				}
			}},
			mygo.Separator(),
			{Role: mygo.RoleServices},
			mygo.Separator(),
			{Role: mygo.RoleHide},
			{Role: mygo.RoleHideOthers},
			{Role: mygo.RoleUnhide},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		}},
		{Role: mygo.RoleFileMenu},
		{Role: mygo.RoleEditMenu},
		{Role: mygo.RoleViewMenu},
		{Role: mygo.RoleWindowMenu},
	}))
	return mygo.App.Run()
}
