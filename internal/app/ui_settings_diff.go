package app

import (
	"strconv"

	"github.com/egoist/mygo/ui"
)

func init() {
	registerSettingsPage(settingsPageDef{
		id:     "diff",
		title:  "Diff",
		order:  10,
		onOpen: (*app).syncDiffSettings,
		view:   (*app).settingsDiffPage,
	})
}

// diffFontSizeChoices are the code font sizes the Diff page offers: 0 is the
// viewer's built-in default, the rest the explicit sizes.
var diffFontSizeChoices = []struct {
	size float64
	name string
}{
	{0, "Default"},
	{11, "11"},
	{12, "12"},
	{13, "13"},
	{14, "14"},
	{16, "16"},
	{18, "18"},
}

// diffTabWidthChoices are the tab widths the Diff page offers: the columns a
// tab expands to. A stored 0 is the framework default of 4 columns, shown as
// the 4 choice.
var diffTabWidthChoices = []int{2, 4, 8}

// syncDiffSettings refreshes the Diff page's controls from the live values,
// as syncGeneralSettings does the General page's: the ignore-whitespace
// choice lives on the session, where the diff panel's own header toggle
// edits it, and the refresh is what keeps the two in step both ways. The tab
// width reads in its display form, where 0 — the framework default — is 4.
func (a *app) syncDiffSettings() {
	s := &a.settings
	prefs := a.store.Settings()
	syncSettingsChoice(&s.fontSize, &s.fontSizeApplied, prefs.DiffFontSize)
	tabWidth := prefs.DiffTabWidth
	if tabWidth == 0 {
		tabWidth = 4
	}
	syncSettingsChoice(&s.tabWidth, &s.tabWidthApplied, tabWidth)
	syncSettingsChoice(&s.changeMarks, &s.changeMarksApplied, prefs.ChangeMarks)
	syncSettingsChoice(&s.ignoreSpace, &s.ignoreSpaceApplied, a.session.IgnoreSpace())
}

// settingsDiffPage renders the Diff page: the code font size, the tab width,
// whether changed lines carry + and - marks, and the ignore-whitespace choice
// the diff panel's header toggle shares.
func (a *app) settingsDiffPage(c *ui.Context, p palette) {
	s := &a.settings
	a.syncDiffSettings()

	// Pending choices save before the controls build, as the General page's
	// do, and the effects follow the write: the diff panels read the
	// preferences as they render, so redrawing the main window is what puts
	// a saved choice on screen.
	saveSettingsChoice(s, "diffFontSize", &s.fontSize, &s.fontSizeApplied, func(size float64) error {
		if err := a.saveDiffFontSize(size); err != nil {
			return err
		}
		a.redraw()
		return nil
	})
	saveSettingsChoice(s, "diffTabWidth", &s.tabWidth, &s.tabWidthApplied, func(width int) error {
		if err := a.store.SetDiffTabWidth(width); err != nil {
			return err
		}
		a.redraw()
		return nil
	})
	saveSettingsChoice(s, "changeMarks", &s.changeMarks, &s.changeMarksApplied, func(on bool) error {
		cur := a.store.Settings()
		cur.ChangeMarks = on
		if err := a.store.SetSettings(cur); err != nil {
			return err
		}
		a.redraw()
		return nil
	})
	saveSettingsChoice(s, "ignoreSpace", &s.ignoreSpace, &s.ignoreSpaceApplied, func(on bool) error {
		if err := a.session.SetIgnoreSpace(on); err != nil {
			return err
		}
		a.redraw()
		return nil
	})

	ui.Form(c, func() {
		ui.Field(c, "Font Size", func() {
			ui.RadioGroup(c, func() {
				for _, choice := range diffFontSizeChoices {
					ui.Radio(c, &s.fontSize, choice.size, choice.name).Label("Diff Font " + choice.name)
				}
			}).Row()
		}).Error(s.errs["diffFontSize"])
		ui.Field(c, "Tab Width", func() {
			ui.RadioGroup(c, func() {
				for _, width := range diffTabWidthChoices {
					ui.Radio(c, &s.tabWidth, width, strconv.Itoa(width)).Label("Diff Tab Width " + strconv.Itoa(width))
				}
			}).Row()
		}).Error(s.errs["diffTabWidth"])
		ui.Field(c, "Change Marks", func() {
			ui.Checkbox(c, &s.changeMarks, "Show +/- change marks").Label("Show +/- change marks")
		}).Error(s.errs["changeMarks"])
		ui.Field(c, "Whitespace", func() {
			ui.Checkbox(c, &s.ignoreSpace, "Ignore whitespace changes").Label("Ignore whitespace changes")
		}).Error(s.errs["ignoreSpace"])
	})
}
