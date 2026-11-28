package app

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/zhdsmy/branchline/internal/store"
)

// editorCandidates are the editors the General settings offer, in the order
// the pop-up shows them. The display name is the bundle name minus its .app
// suffix.
var editorCandidates = []string{
	"Cursor",
	"Visual Studio Code",
	"Zed",
	"Sublime Text",
	"BBEdit",
	"TextMate",
	"Xcode",
}

// noSupportedEditorToast formats the toast Open in Editor shows when none of
// the candidate editors are installed, building the list from editorCandidates
// so the message always tracks the candidate table.
func noSupportedEditorToast() string {
	switch len(editorCandidates) {
	case 0:
		return "No supported editor found."
	case 1:
		return "No supported editor found. Install " + editorCandidates[0] + "."
	default:
		last := len(editorCandidates) - 1
		return "No supported editor found. Install " +
			strings.Join(editorCandidates[:last], ", ") +
			" or " + editorCandidates[last] + "."
	}
}

// editorApp is one installed editor: its display name and the full path of
// its application bundle, which is what open -a launches.
type editorApp struct {
	name string
	path string
}

// editorSearchDirs are the folders scanInstalledEditors looks through:
// the system-wide Applications, the system image's own, and the user's.
func editorSearchDirs() []string {
	dirs := []string{"/Applications", "/System/Applications"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, "Applications"))
	}
	return dirs
}

// scanInstalledEditors finds the editors installed on this Mac. It is a
// package-level variable so tests can substitute a scan of their own folders
// — the real one only reads the file system, but a test must count its runs.
var scanInstalledEditors = func() []editorApp {
	return scanEditorApps(editorSearchDirs())
}

// scanEditorApps returns the candidates installed under dirs, in candidate
// order. An editor counts when its <name>.app under one of the folders is a
// directory — a plain file of that name is no application — and the first
// folder holding it wins: the scan stops looking deeper once an editor is
// found. It only reads the file system; nothing is ever launched.
func scanEditorApps(dirs []string) []editorApp {
	var out []editorApp
	for _, name := range editorCandidates {
		for _, dir := range dirs {
			path := filepath.Join(dir, name+".app")
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				out = append(out, editorApp{name: name, path: path})
				break
			}
		}
	}
	return out
}

// editorChoice resolves the editor to use: the saved one when the scan still
// finds it — a choice outlives the installation it was made on — or the
// first the scan found, which is also the answer when none was ever chosen.
// The zero value answers when no editor is installed at all.
func editorChoice(saved string, editors []editorApp) editorApp {
	for _, ed := range editors {
		if ed.name == saved {
			return ed
		}
	}
	if len(editors) > 0 {
		return editors[0]
	}
	return editorApp{}
}

// scanGeneralEditors looks for installed editors in the background for the
// General settings page, which a per-frame render must never do itself. The
// result posts back to the UI thread and is cached on the settings state;
// the generation discards a late answer a newer scan already superseded —
// for example one posted after the window closed and opened again, where the
// fresh scan's own answer is the one the page should show.
func (a *app) scanGeneralEditors() {
	s := &a.settings
	s.editorScanGen++
	gen := s.editorScanGen
	s.editorsReady = false
	s.editorApps = nil
	scan := scanInstalledEditors
	a.spawn(func() {
		editors := scan()
		a.post(func() {
			if gen != s.editorScanGen {
				return
			}
			s.editorApps = editors
			s.editorsReady = true
			if a.settingsWin != nil {
				a.settingsWin.Invalidate()
			}
		})
	})
}

// openInEditor opens a local repository's folder in the editor the settings
// choose: the saved editor when it is still installed, otherwise the first
// one found. The settings are read on the UI thread, never by the worker,
// as every background task of the switcher does; the scan and the launch
// run in the background since both touch the file system or start a process.
// Like Open in Terminal this only ever surfaces a toast: it is no git
// action, so the session never turns busy and no action error records it.
func (a *app) openInEditor(r store.Repo) {
	if a.store == nil {
		return
	}
	saved := a.store.Settings().Editor
	runOpen := runTerminalOpen
	if st := a.reposUI(); st.openEditorAppFn != nil {
		runOpen = st.openEditorAppFn
	}
	scan := scanInstalledEditors
	a.spawn(func() {
		choice := editorChoice(saved, scan())
		if choice.name == "" {
			a.post(func() {
				a.toast = noSupportedEditorToast()
			})
			return
		}
		if err := runOpen(choice.path, r.Path); err != nil {
			a.post(func() {
				a.toast = "Could not open " + choice.name + ": " + err.Error()
			})
		}
	})
}
