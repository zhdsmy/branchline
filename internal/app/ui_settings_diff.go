package app

import (
	"slices"
	"strconv"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/exttool"
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

// defaultDiffTool is the external diff tool a fresh store uses: the first of
// the preset names, where a saved empty value means none was ever chosen.
const defaultDiffTool = "FileMerge"

// diffToolSettingsState holds the drafts of the Diff page's external tool
// controls. Each control keeps the same triple the rest of the page uses:
// the value it shows, the value last saved successfully — which a failed
// save reverts the control to — and its raw save error in errs, shared with
// the other controls of the page.
type diffToolSettingsState struct {
	diffTool, diffToolApplied   string
	mergeTool, mergeToolApplied string
	customDiffPath              string
	customDiffPathApplied       string
	customDiffArgs              string
	customDiffArgsApplied       string
	customMergePath             string
	customMergePathApplied      string
	customMergeArgs             string
	customMergeArgsApplied      string
	errs                        map[string]string

	// pickToolPathFn shows the file picker of the Choose buttons. Tests
	// replace it with a picker that never shows the system dialog.
	pickToolPathFn func(parent *mygo.Window) (string, error)
}

// diffToolSettings returns the app's external tool settings state, creating
// it on first use.
func diffToolSettings(a *app) *diffToolSettingsState {
	return featureState(a, func() *diffToolSettingsState {
		return &diffToolSettingsState{
			errs:           map[string]string{},
			pickToolPathFn: pickToolPath,
		}
	})
}

// pickToolPath shows the file picker of the Choose buttons of the Custom
// tool rows.
func pickToolPath(parent *mygo.Window) (string, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: parent,
		Title:  "Choose Tool",
	})
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", nil
	}
	return paths[0], nil
}

// diffToolNameOrDefault reads a saved tool name for its pop-up: an empty or
// unknown value — a name no longer offered, or none ever chosen — shows the
// default.
func diffToolNameOrDefault(name string) string {
	if slices.Contains(exttool.Names(), name) {
		return name
	}
	return defaultDiffTool
}

// syncDiffToolSettings refreshes the external tool controls from the live
// values, as syncSettingsChoice does the page's other controls. The custom
// inputs sync even while their tool is not Custom — they are hidden then —
// because the next save carries them, and a stale copy would overwrite what
// the store holds.
func (a *app) syncDiffToolSettings() {
	st := diffToolSettings(a)
	prefs := a.store.Settings()
	syncSettingsChoice(&st.diffTool, &st.diffToolApplied, diffToolNameOrDefault(prefs.DiffTool))
	syncSettingsChoice(&st.mergeTool, &st.mergeToolApplied, diffToolNameOrDefault(prefs.MergeTool))
	syncSettingsChoice(&st.customDiffPath, &st.customDiffPathApplied, prefs.CustomDiff.Path)
	syncSettingsChoice(&st.customDiffArgs, &st.customDiffArgsApplied, prefs.CustomDiff.Args)
	syncSettingsChoice(&st.customMergePath, &st.customMergePathApplied, prefs.CustomMerge.Path)
	syncSettingsChoice(&st.customMergeArgs, &st.customMergeArgsApplied, prefs.CustomMerge.Args)
}

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
	a.syncDiffToolSettings()
}

// saveToolSettings writes the external tool choices into a copy of the live
// settings, so one failing write leaves every other setting untouched.
func (a *app) saveToolSettings(diffTool, mergeTool string, customDiffPath, customDiffArgs, customMergePath, customMergeArgs string) error {
	cur := a.store.Settings()
	cur.DiffTool = diffTool
	cur.MergeTool = mergeTool
	cur.CustomDiff.Path = customDiffPath
	cur.CustomDiff.Args = customDiffArgs
	cur.CustomMerge.Path = customMergePath
	cur.CustomMerge.Args = customMergeArgs
	return a.store.SetSettings(cur)
}

// saveDiffToolChoice persists one pending choice of the external tool
// controls: the write carries every control's draft, as the four custom
// inputs belong to the choices they customize. A failure reverts the control
// to the applied value — before the controls of this frame are built, so the
// unapplied choice never paints — and keeps the raw error under it.
func saveDiffToolChoice[T comparable](st *diffToolSettingsState, key string, shown, applied *T, save func() error) {
	if *shown == *applied {
		return
	}
	choice := *shown
	if err := save(); err != nil {
		*shown = *applied
		st.errs[key] = err.Error()
		return
	}
	*applied = choice
	st.errs[key] = ""
}

// chooseCustomToolPath runs the file picker of one Choose button in the
// background, as the system dialog must never run inside a view, and saves
// the picked program through the same write the input's own edits take. A
// failed write leaves the input showing its saved path again.
func (a *app) chooseCustomToolPath(st *diffToolSettingsState, key string, shown, applied *string) {
	win := a.settingsWin
	a.spawn(func() {
		dir, err := st.pickToolPathFn(win)
		if err != nil || dir == "" {
			return
		}
		a.post(func() {
			*shown = dir
			saveDiffToolChoice(st, key, shown, applied, func() error {
				return a.saveToolSettings(st.diffTool, st.mergeTool, st.customDiffPath, st.customDiffArgs, st.customMergePath, st.customMergeArgs)
			})
		})
	})
}

// settingsDiffToolRows renders the external tool rows of the Diff page: one
// pop-up per tool, revealing the program and arguments inputs of its Custom
// choice.
func (a *app) settingsDiffToolRows(c *ui.Context, key, selectLabel, pathKey, argsKey string, tool, path, pathApplied, args *string, argsPlaceholder string) {
	st := diffToolSettings(a)
	ui.Field(c, selectLabel, func() {
		ui.Select(c.Key(key), tool, exttool.Names()).Label(selectLabel)
	}).Error(st.errs[key])
	if *tool != exttool.Custom {
		return
	}
	ui.Field(c, "Path", func() {
		ui.Row(c.Key(key + "-path-row")).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c.Key(pathKey), path).Grow(1).MinWidth(0).Font("monospace").Placeholder("/usr/local/bin/difftool").Label(selectLabel + " Path")
			if ui.Button(c.Key(key+"-choose"), "Choose…").Label("Choose " + selectLabel + " Path").Clicked() {
				a.chooseCustomToolPath(st, key, path, pathApplied)
			}
		})
	}).Error(st.errs[key])
	ui.Field(c, "Arguments", func() {
		ui.TextInput(c.Key(argsKey), args).FillWidth().Font("monospace").Placeholder(argsPlaceholder).Label(selectLabel + " Arguments")
	}).Error(st.errs[key])
}

// settingsDiffPage renders the Diff page: the code font size, the tab width,
// whether changed lines carry + and - marks, the ignore-whitespace choice
// the diff panel's header toggle shares, and the external diff and merge
// tools.
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

	st := diffToolSettings(a)
	save := func() error {
		return a.saveToolSettings(st.diffTool, st.mergeTool, st.customDiffPath, st.customDiffArgs, st.customMergePath, st.customMergeArgs)
	}
	saveDiffToolChoice(st, "diffTool", &st.diffTool, &st.diffToolApplied, save)
	saveDiffToolChoice(st, "mergeTool", &st.mergeTool, &st.mergeToolApplied, save)
	saveDiffToolChoice(st, "customDiffPath", &st.customDiffPath, &st.customDiffPathApplied, save)
	saveDiffToolChoice(st, "customDiffArgs", &st.customDiffArgs, &st.customDiffArgsApplied, save)
	saveDiffToolChoice(st, "customMergePath", &st.customMergePath, &st.customMergePathApplied, save)
	saveDiffToolChoice(st, "customMergeArgs", &st.customMergeArgs, &st.customMergeArgsApplied, save)

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
		a.settingsDiffToolRows(c, "diffTool", "External Diff Tool", "diff-custom-path", "diff-custom-args",
			&st.diffTool, &st.customDiffPath, &st.customDiffPathApplied, &st.customDiffArgs,
			`"$LOCAL" "$REMOTE"`)
		a.settingsDiffToolRows(c, "mergeTool", "Merge Tool", "merge-custom-path", "merge-custom-args",
			&st.mergeTool, &st.customMergePath, &st.customMergePathApplied, &st.customMergeArgs,
			`"$BASE" "$LOCAL" "$REMOTE" "$MERGED"`)
	})
}
