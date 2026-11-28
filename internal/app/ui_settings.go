package app

import (
	"errors"
	"sort"
	"strconv"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/store"
)

// settingsPageDef describes one page of the Settings window: its identity,
// the title its tab shows, where the page sorts among the tabs, an optional
// priming hook that runs as the window opens on the page or a click selects
// its tab, and the page itself.
type settingsPageDef struct {
	id     string
	title  string
	order  int
	onOpen func(*app)
	view   func(*app, *ui.Context, palette)
}

// registeredSettingsPages are the pages of the Settings window as their files
// register them; settingsPages reads them in tab order.
var registeredSettingsPages []settingsPageDef

// registerSettingsPage adds a page to the Settings window. Pages register in
// their file's init, so a new page joins the window without changes to the
// window itself.
func registerSettingsPage(def settingsPageDef) {
	registeredSettingsPages = append(registeredSettingsPages, def)
}

// settingsPages are the registered pages in tab order: ascending order, ties
// broken by id, so the row is stable however the files initialize.
func settingsPages() []settingsPageDef {
	pages := append([]settingsPageDef(nil), registeredSettingsPages...)
	sort.SliceStable(pages, func(i, j int) bool {
		if pages[i].order != pages[j].order {
			return pages[i].order < pages[j].order
		}
		return pages[i].id < pages[j].id
	})
	return pages
}

func init() {
	registerSettingsPage(settingsPageDef{
		id:     "general",
		title:  "General",
		order:  0,
		onOpen: (*app).openGeneralSettings,
		view:   (*app).settingsGeneralPage,
	})
}

// settingsState holds the Settings window state on app. All fields are owned
// by the UI thread: the window shares the main thread with the main window.
//
// Each control keeps the same triple: the value it shows (a click moves it
// ahead before the view rebuilds), the value last saved successfully — which
// a failed save reverts the control to — and its raw save error in errs,
// shown under the control until one of its saves succeeds.
type settingsState struct {
	// tab is the index of the page the window shows among the registered
	// pages; out of range resolves to the first page. It is the selection
	// the tab list edits, so pointer and keyboard choices land in the same
	// state. opened is the page whose onOpen priming already ran for this
	// activation of the window.
	tab    int
	opened string
	errs   map[string]string

	appearance, appearanceApplied string
	dateOrder, dateOrderApplied   bool
	fetchTags, fetchTagsApplied   bool
	terminal, terminalApplied     string
	editor, editorApplied         string

	// editorApps caches the editors the background scan of
	// scanGeneralEditors found, ready once editorsReady is set; the page
	// hides its Editor control until then. editorScanGen is the generation
	// of the scan in flight, so a late answer a newer scan superseded is
	// discarded.
	editorsReady  bool
	editorApps    []editorApp
	editorScanGen int

	// The automatic refresh controls live with the General choices; the
	// interval keeps both its saved text and the last text validated, so a
	// draft that fails validation survives for further editing instead of
	// reverting on the first keystroke.
	refreshAuto, refreshAutoApplied         bool
	refreshInterval, refreshIntervalApplied string
	refreshIntervalChecked                  string

	fontSize, fontSizeApplied       float64
	tabWidth, tabWidthApplied       int
	changeMarks, changeMarksApplied bool
	ignoreSpace, ignoreSpaceApplied bool
}

// syncSettingsChoice refreshes a control's shown and applied values from the
// live one while no edit is pending: a click moves the shown value ahead of
// the applied one, and the pending choice must survive until the save picks
// it up. Changes made elsewhere — the same setting edited in another view —
// flow into the control through the refresh.
func syncSettingsChoice[T comparable](shown, applied *T, live T) {
	if *shown == *applied {
		*shown = live
		*applied = live
	}
}

// saveSettingsChoice persists a control's pending choice: the frame after a
// click arrives with the shown value ahead of the applied one. A successful
// save moves the applied value, and the control's effect runs inside the save
// closure, after the write. A failure reverts the control to the applied
// value — before the controls of this frame are built, so the unapplied
// choice never paints — and keeps the raw error on the page.
func saveSettingsChoice[T comparable](s *settingsState, key string, shown, applied *T, save func(T) error) {
	if *shown == *applied {
		return
	}
	choice := *shown
	if err := save(choice); err != nil {
		*shown = *applied
		s.errs[key] = err.Error()
		return
	}
	*applied = choice
	s.errs[key] = ""
}

// openSettings opens the Settings window, or focuses the one already open:
// the shortcut and the menu item never create a second window.
func (a *app) openSettings() {
	if a.settingsOpen {
		if w := a.settingsWin; w != nil {
			if w.IsMinimized() {
				w.Restore()
			}
			w.Show()
			w.Focus()
			mygo.App.Focus()
		}
		return
	}
	a.settingsOpen = true
	a.openSettingsWindowFn()
}

// createSettingsWindow is the production window hook: it opens the Settings
// window and wires its close to clear the tracked reference, so the next
// ⌘, opens a fresh window.
func (a *app) createSettingsWindow() {
	w := mygo.NewWindow(mygo.WindowOptions{
		Title:           "Settings",
		Width:           560,
		Height:          420,
		MinWidth:        480,
		MinHeight:       360,
		BackgroundColor: "light-dark(#fbfbfc, #151619)",
		Content:         ui.View(a.settingsView),
	})
	a.settingsWin = w
	w.OnClosed(a.closeSettingsWindow)
}

// closeSettingsWindow records the Settings window closing. The production
// window's OnClosed callback runs it; tests call it to exercise the reopen
// path without a real window. The priming marker clears so the next window
// primes its page from the live values again.
func (a *app) closeSettingsWindow() {
	a.settingsWin = nil
	a.settingsOpen = false
	a.settings.opened = ""
}

// applyStartupAppearance applies the appearance saved in the store, which
// Run calls before creating the main window so the app starts in it. A
// store without a saved value reads back as system, leaving the default
// behavior unchanged.
func (a *app) applyStartupAppearance() {
	a.applyAppearanceFn(mygo.ThemeSource(a.store.Appearance()))
}

// currentSettingsPage resolves the page the window shows: the one the tab
// selection names, or the first page when the selection is out of range.
func (a *app) currentSettingsPage(pages []settingsPageDef) settingsPageDef {
	if a.settings.tab >= 0 && a.settings.tab < len(pages) {
		return pages[a.settings.tab]
	}
	if len(pages) > 0 {
		return pages[0]
	}
	return settingsPageDef{}
}

// settingsView renders the Settings window: a row of page tabs across the
// top and the active page below it.
func (a *app) settingsView(c *ui.Context) {
	p := applyPalette(c, a.themeGen)
	s := &a.settings
	if s.errs == nil {
		s.errs = map[string]string{}
	}

	pages := settingsPages()
	cur := a.currentSettingsPage(pages)
	if cur.id != s.opened {
		s.opened = cur.id
		if cur.onOpen != nil {
			cur.onOpen(a)
		}
	}

	ui.Column(c).Fill().Padding(0, 0, 16, 0).Children(func() {
		a.settingsTabRow(c, p, pages)
		ui.Box(c.Key("settings-page")).Grow(1).Padding(16, 20, 0, 20).Role(ui.RoleTabPanel).Label(cur.title).Children(func() {
			if cur.view != nil {
				cur.view(a, c, p)
			}
		})
	})
}

// settingsTabRow builds the page tabs across the top of the window as one
// tab list: a click chooses a tab, and the arrows, Home and End choose and
// focus one, while Tab reaches the list once and moves on into the page.
func (a *app) settingsTabRow(c *ui.Context, p palette, pages []settingsPageDef) {
	s := &a.settings
	tabs := ui.TabsBase(c.Key("settings-tabs"), &s.tab, len(pages))
	tabs.List.Gap(4).Justify(ui.Start).Padding(12, 20, 0, 20).Children(func() {
		for i, def := range pages {
			selected := s.tab == i
			tab := tabs.Tab(i).
				Height(28).
				PaddingX(12).
				Radius(6).
				AlignItems(ui.Center).
				Justify(ui.Start).
				Label(def.title)
			fg := p.muted
			if selected {
				tab.Background(p.selected)
				fg = p.text
			} else if tab.Hovered() {
				tab.Background(p.hover)
			}
			tab.Children(func() {
				ui.Text(c, def.title).FontSize(13).FontWeight(500).TextColor(fg).SingleLine()
			})
		}
	})
}

// openGeneralSettings primes the General page when the Settings window opens
// or returns to the tab, discarding any unsaved interval draft so the field
// shows the persisted value again, and starts the one background scan for
// installed editors the page needs: scanning is file system work no frame
// render may do, so it happens here — once per visit — and its result posts
// back into the cache the per-frame controls read.
func (a *app) openGeneralSettings() {
	s := &a.settings
	s.refreshInterval = ""
	s.refreshIntervalApplied = ""
	s.refreshIntervalChecked = ""
	if s.errs != nil {
		delete(s.errs, "refreshInterval")
	}
	a.scanGeneralEditors()
	a.syncGeneralSettings()
}

// syncGeneralSettings refreshes the General page's controls from the live
// values: the saved appearance, the history ordering and whether Fetch
// brings every tag. Pending edits are left alone, as syncSettingsChoice
// describes.
func (a *app) syncGeneralSettings() {
	s := &a.settings
	prefs := a.store.Settings()
	syncSettingsChoice(&s.appearance, &s.appearanceApplied, a.store.Appearance())
	syncSettingsChoice(&s.dateOrder, &s.dateOrderApplied, prefs.DateOrder)
	syncSettingsChoice(&s.fetchTags, &s.fetchTagsApplied, prefs.FetchTags)
	syncSettingsChoice(&s.refreshAuto, &s.refreshAutoApplied, !prefs.DisableStatusUpdate)
	terminalLive, _ := terminalChoice(prefs.Terminal)
	syncSettingsChoice(&s.terminal, &s.terminalApplied, terminalLive)
	// The editor sync reads only the cached scan; with none finished yet
	// the choice resolves to empty, which the hidden control never shows.
	editorLive := editorChoice(prefs.Editor, s.editorApps).name
	syncSettingsChoice(&s.editor, &s.editorApplied, editorLive)
	if s.refreshInterval == s.refreshIntervalApplied {
		live := refreshIntervalText(prefs)
		s.refreshInterval = live
		s.refreshIntervalApplied = live
		s.refreshIntervalChecked = live
	}
}

// refreshIntervalText is the interval input's text for a saved value: the
// effective seconds the refresh counts down, where a stored zero — never
// chosen — reads as the default of 60.
func refreshIntervalText(prefs store.Settings) string {
	return strconv.Itoa(refreshIntervalSeconds(prefs))
}

// settingsGeneralPage renders the General page: the appearance, the commit
// ordering of the history, and whether Fetch brings every tag.
func (a *app) settingsGeneralPage(c *ui.Context, p palette) {
	s := &a.settings
	a.syncGeneralSettings()

	// A click mutates a control's value before the view rebuilds, so a fresh
	// choice is pending here. Save it before building the controls: the
	// revert of a failed save then lands in this same frame instead of
	// painting the unapplied choice for one frame.
	saveSettingsChoice(s, "appearance", &s.appearance, &s.appearanceApplied, func(choice string) error {
		if err := a.store.SetAppearance(choice); err != nil {
			return err
		}
		a.applyAppearanceFn(mygo.ThemeSource(choice))
		return nil
	})
	saveSettingsChoice(s, "dateOrder", &s.dateOrder, &s.dateOrderApplied, func(byDate bool) error {
		cur := a.store.Settings()
		cur.DateOrder = byDate
		if err := a.store.SetSettings(cur); err != nil {
			return err
		}
		a.session.SetDateOrder(byDate)
		a.redraw()
		return nil
	})
	saveSettingsChoice(s, "fetchTags", &s.fetchTags, &s.fetchTagsApplied, func(on bool) error {
		if err := a.saveFetchTags(on); err != nil {
			return err
		}
		a.redraw()
		return nil
	})
	saveSettingsChoice(s, "refreshAuto", &s.refreshAuto, &s.refreshAutoApplied, func(on bool) error {
		cur := a.store.Settings()
		cur.DisableStatusUpdate = !on
		if err := a.store.SetSettings(cur); err != nil {
			return err
		}
		// The watch follows the setting at once: off ends the watch of the
		// open repository, on starts one for it; the interval counts from
		// the next tick with whatever value is saved then.
		syncAutoRefreshWatch(a, autoRefresh(a))
		a.redraw()
		return nil
	})
	saveSettingsChoice(s, "terminal", &s.terminal, &s.terminalApplied, func(choice string) error {
		cur := a.store.Settings()
		cur.Terminal = choice
		return a.store.SetSettings(cur)
	})
	saveSettingsChoice(s, "editor", &s.editor, &s.editorApplied, func(choice string) error {
		cur := a.store.Settings()
		cur.Editor = choice
		return a.store.SetSettings(cur)
	})
	if s.refreshInterval != s.refreshIntervalChecked {
		s.refreshIntervalChecked = s.refreshInterval
		n, err := parseSettingsInt(s.refreshInterval)
		if err == nil && (n < 10 || n > 3600) {
			err = errRefreshIntervalRange
		}
		if err != nil {
			s.errs["refreshInterval"] = err.Error()
		} else {
			cur := a.store.Settings()
			if refreshIntervalSeconds(cur) != n {
				cur.StatusUpdateSeconds = n
				err = a.store.SetSettings(cur)
			}
			if err != nil {
				s.errs["refreshInterval"] = err.Error()
			} else {
				s.refreshIntervalApplied = s.refreshInterval
				s.errs["refreshInterval"] = ""
			}
		}
	}

	ui.Form(c, func() {
		ui.Field(c, "Appearance", func() {
			ui.RadioGroup(c, func() {
				for _, choice := range []struct {
					value string
					name  string
				}{
					{"system", "System"},
					{"light", "Light"},
					{"dark", "Dark"},
				} {
					ui.Radio(c, &s.appearance, choice.value, choice.name).Label("Appearance " + choice.name)
				}
			}).Row()
		}).Error(s.errs["appearance"])
		ui.Field(c, "Commit Order", func() {
			ui.RadioGroup(c, func() {
				ui.Radio(c, &s.dateOrder, false, "Topologically").Label("Commit Order Topologically")
				ui.Radio(c, &s.dateOrder, true, "By date").Label("Commit Order By date")
			}).Row()
		}).Error(s.errs["dateOrder"])
		ui.Field(c, "Tags", func() {
			ui.Checkbox(c, &s.fetchTags, "Fetch all tags").Label("Fetch all tags")
		}).Error(s.errs["fetchTags"])
		ui.Field(c, "Automatic Refresh", func() {
			ui.Checkbox(c, &s.refreshAuto, "Refresh repository status automatically").Label("Refresh repository status automatically")
		}).Error(s.errs["refreshAuto"])
		ui.Field(c, "Refresh Interval", func() {
			ui.TextInput(c.Key("refresh-interval"), &s.refreshInterval).Width(72).Label("Refresh interval (seconds)").Disabled(!s.refreshAuto)
		}).Error(s.errs["refreshInterval"])
		ui.Field(c, "Terminal", func() {
			ui.Select(c.Key("terminal"), &s.terminal, terminalOptionValues()).Label("Terminal")
		}).Error(s.errs["terminal"])
		// The Editor pop-up appears once the scan of the visit answered —
		// until then the page cannot know what it offers — and only when it
		// found an editor to choose. One pop-up serves every machine: its
		// options are exactly the editors installed here.
		if s.editorsReady {
			ui.Field(c, "Editor", func() {
				if len(s.editorApps) == 0 {
					ui.Text(c, "No supported editor found.").FontSize(13).TextColor(p.muted)
					return
				}
				options := make([]string, len(s.editorApps))
				for i, ed := range s.editorApps {
					options[i] = ed.name
				}
				ui.Select(c.Key("editor"), &s.editor, options).Label("Editor")
			}).Error(s.errs["editor"])
		}
	})
}

// errRefreshIntervalRange is what the refresh interval input reports for a
// number outside the range it accepts.
var errRefreshIntervalRange = errors.New("enter a number from 10 to 3600 seconds")
