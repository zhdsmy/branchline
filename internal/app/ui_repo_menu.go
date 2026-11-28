package app

import (
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/store"
)

// repoRowMenuItems builds the context menu of one repository row of the
// switcher popup: Open switches to it exactly as the row's click does,
// Show in Finder reveals a local repository's folder and disables on the
// server paths of SSH repositories, Open in Terminal opens the repository
// in the chosen terminal — a local folder directly, a server through an
// SSH login — for either kind, Rename… opens the display-name dialog,
// Copy Location copies what the row shows as the repository's address, and
// Remove from List is the row-end minus button's removal, which never
// touches a file.
//
// Copy Location cannot write the clipboard itself: the menu build receives
// no *ui.Context. Like the branch management items it records the text, and
// the rename dialog's mount point — every frame passes through it — writes
// it with the frame's own context.
func (a *app) repoRowMenuItems(m *ui.Menu, r store.Repo, st *reposUIState) {
	if m.Item("Open").Chosen() {
		a.switchRepoByID(r.ID)
		st.popupOpen = false
	}
	if m.Item("Show in Finder").Disabled(r.Kind == store.SSH).Chosen() {
		showInFinder(st, r.Path)
	}
	if m.Item("Open in Terminal").Chosen() {
		a.openInTerminal(r)
	}
	if m.Item("Rename…").Chosen() {
		st.renOpen = true
		st.renID = r.ID
		st.renOrig = r.Name
		st.renName = r.Name
		st.renErr = ""
		st.renSelAll = true
	}
	if m.Item("Copy Location").Chosen() {
		st.repoClipboard = r.Location()
		st.hasRepoClipboard = true
	}
	m.Separator()
	if m.Item("Remove from List").Chosen() {
		a.removeRepo(st, r)
	}
}

// showInFinder reveals path in the file manager, through mygo's shell
// unless a test substituted its recorder.
func showInFinder(st *reposUIState, path string) {
	if st.showInFinderFn != nil {
		st.showInFinderFn(path)
		return
	}
	mygo.Shell.ShowItemInFolder(path)
}

// repoRenameProblem says why the typed display name cannot replace the old
// one, empty when it can: a blank name, and the old name again, which is no
// rename at all.
func repoRenameProblem(orig, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Enter a repository name"
	}
	if name == orig {
		return "The new name is the same as the current name"
	}
	return ""
}

func (st *reposUIState) repoRenameCanSubmit() bool {
	return repoRenameProblem(st.renOrig, st.renName) == ""
}

// repoRenameModal mounts the Rename Repository dialog of the switcher and
// flushes the location text a Copy Location menu item recorded, which needs
// a frame's *ui.Context the menu build never receives. The flush precedes
// the dialog so a copy chosen on any frame reaches the clipboard in the
// same frame whether or not the dialog is open.
//
// The input starts from the current name with all of it selected, so
// typing replaces it; the Rename button stays disabled while the name is
// blank or unchanged. A save failure shows inside the dialog, which stays
// open — the in-memory name already rolled back — while success closes the
// dialog and the switcher's list and title show the new name on the next
// frame. Only the display name changes, never the repository's folder.
func (a *app) repoRenameModal(c *ui.Context, st *reposUIState) {
	if st.hasRepoClipboard {
		c.WriteClipboard(st.repoClipboard)
		st.repoClipboard = ""
		st.hasRepoClipboard = false
	}
	if !st.renOpen {
		return
	}
	p := a.pal
	_, cancelled, confirmed := dialogFrame(c, p, "repo-rename-modal", &st.renOpen, "Rename Repository", 440, func() {
		ui.Field(c, "Name:", func() {
			in := ui.TextInput(c.Key("repo-rename-input"), &st.renName).FillWidth().AutoFocus().Label("Repository Name")
			if st.renSelAll {
				st.renSelAll = false
				in.SetTextSelection(0, utf8.RuneCountInString(st.renName))
			}
			// Enter in the input submits here, never twice: a focused
			// single-line input takes the key before every window shortcut,
			// so the frame's Enter cannot fire for the same press.
			if in.Submitted() && st.repoRenameCanSubmit() {
				a.submitRepoRename(st)
			}
		})
		if st.renErr != "" {
			ui.Text(c, st.renErr).FontSize(12).TextColor(p.danger)
		}
	},
		dialogAction{key: "repo-rename-cancel", text: "Cancel", label: "Cancel"},
		dialogAction{key: "repo-rename-confirm", text: "Rename", label: "Confirm Rename Repository", disabled: !st.repoRenameCanSubmit()})
	if cancelled {
		st.renOpen = false
	}
	if confirmed {
		a.submitRepoRename(st)
	}
}

// submitRepoRename saves the dialog's name in the store. A failure shows in
// the dialog, which stays open; the store already rolled its in-memory copy
// back to what the file holds, so the list never shows a name the disk does
// not.
func (a *app) submitRepoRename(st *reposUIState) {
	if a.store == nil {
		return
	}
	if err := a.store.Rename(st.renID, st.renName); err != nil {
		st.renErr = "Cannot save the repository list: " + err.Error()
		return
	}
	st.renErr = ""
	st.renOpen = false
}
