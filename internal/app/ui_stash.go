package app

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// fallbackStash is the original geometric archive box used on systems
// without SF Symbols, stroked like the other fallback icons.
var fallbackStash = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3.4 5.4l1-2.4h7.2l1 2.4"/><rect x="3" y="5.4" width="10" height="7.6" rx="1.4"/><path d="M6.5 8.2h3"/></svg>`)

// Reasons a stash cannot be saved, shared by the dialog's live explanation
// and the write worker's last-moment verification so the user hears one
// wording for one cause.
const (
	stashErrUnborn    = "Cannot stash before the first commit."
	stashErrNoChanges = "No local changes to stash."
	stashErrNoTracked = "No tracked local changes to stash."
)

// stashTarget is the identity of one stash entry captured on the UI thread
// when a dialog opens or a direct action starts: its reflog name at that
// moment, its commit and message, the selection occurrence inside its
// (hash, message) group, and the size of that group. A write worker
// re-verifies the group size against the live reflog before touching
// anything, so an entry added, moved or removed while the dialog waited is
// never mistaken for the target and a different entry shifted into the
// target's old reflog position is never applied or deleted in its place.
type stashTarget struct {
	name       string
	hash       string
	message    string
	selection  git.StashSelection
	groupCount int
}

// stashUIState is the per-app stash sidebar state kept outside the shared
// app struct: the collapsed section flag, the selected row key, and the
// Save, Apply and Drop dialogs with their drafts and captured targets.
type stashUIState struct {
	repoGen uint64

	collapsed   bool
	selectedKey string

	saveOpen      bool
	saveMessage   string
	saveUntracked bool
	saveScope     int
	saveChecked   map[string]bool
	saveList      ui.ListState

	applyOpen   bool
	applyTarget stashTarget
	applyIndex  bool
	applyPop    bool

	dropOpen   bool
	dropTarget stashTarget
}

func (a *app) stashUI() *stashUIState {
	return featureState(a, func() *stashUIState { return &stashUIState{saveUntracked: true} })
}

// stashResetForRepoGen discards the selection and every stash dialog when
// the repository session advances: the entries and reflog positions they
// were captured from belong to the previous repository. The collapsed flag
// survives because it is a view preference, not repository data.
func (a *app) stashResetForRepoGen() {
	st := a.stashUI()
	if st.repoGen == a.session.Generation() {
		return
	}
	collapsed := st.collapsed
	*st = stashUIState{repoGen: a.session.Generation(), collapsed: collapsed}
}

// stashRowKey identifies one rendered stash entry. It includes the reflog
// name so a shifted entry reads as a different row: the selection captured
// against stash@{0} must not silently follow onto whatever now sits there.
func stashRowKey(s git.Stash) string {
	return s.Name + "\x00" + s.Hash
}

// stashGroupCount counts the entries sharing hash and the whitespace-trimmed
// message, the identity group a selection is verified against.
func stashGroupCount(stashes []git.Stash, hash, message string) int {
	hash = strings.TrimSpace(hash)
	message = strings.TrimSpace(message)
	count := 0
	for _, s := range stashes {
		if s.Hash == hash && strings.TrimSpace(s.Message) == message {
			count++
		}
	}
	return count
}

// stashSelectionFor computes the selection of target inside the newest-first
// list: Occurrence counts same-group entries above it, and the second return
// value is the total size of its (hash, message) group.
func stashSelectionFor(stashes []git.Stash, target git.Stash) (git.StashSelection, int) {
	occurrence := 0
	count := 0
	for _, s := range stashes {
		if s.Hash != target.Hash || strings.TrimSpace(s.Message) != strings.TrimSpace(target.Message) {
			continue
		}
		if s.Name == target.Name {
			occurrence = count
		}
		count++
	}
	return git.StashSelection{Hash: target.Hash, Message: target.Message, Occurrence: occurrence}, count
}

// captureStashTarget freezes the identity of s against the stash list of the
// loaded snapshot on the UI thread.
func captureStashTarget(stashes []git.Stash, s git.Stash) stashTarget {
	selection, groupCount := stashSelectionFor(stashes, s)
	return stashTarget{
		name:       s.Name,
		hash:       s.Hash,
		message:    s.Message,
		selection:  selection,
		groupCount: groupCount,
	}
}

// guardStashSelection is the shared last-moment verification of every stash
// write: the live reflog must be readable, and the target's (hash, message)
// group must still hold exactly as many entries as when the user chose the
// operation. A group that grew or shrank means the reflog moved since the
// capture, and positional resolution could hit the wrong entry.
func guardStashSelection(repo *git.Repo, sel git.StashSelection, expectedGroupCount int) error {
	live, err := repo.Stashes()
	if err != nil {
		return fmt.Errorf("read stash list before operation: %w", err)
	}
	if got := stashGroupCount(live, sel.Hash, sel.Message); got != expectedGroupCount {
		return fmt.Errorf("stash list changed before operation (matching entries: was %d, now %d)", expectedGroupCount, got)
	}
	return nil
}

// stashDropWithGuard deletes exactly the captured entry and nothing when the
// verification fails: no read failure, shift or disappearance deletes any
// stash.
func stashDropWithGuard(repo *git.Repo, sel git.StashSelection, expectedGroupCount int) error {
	if err := guardStashSelection(repo, sel, expectedGroupCount); err != nil {
		return err
	}
	return repo.StashDropEntry(sel)
}

// stashApplyWithGuard applies or pops exactly the captured entry after the
// same verification.
func stashApplyWithGuard(repo *git.Repo, sel git.StashSelection, expectedGroupCount int, opts git.StashApplyOptions) error {
	if err := guardStashSelection(repo, sel, expectedGroupCount); err != nil {
		return err
	}
	return repo.StashApplyEntryWith(sel, opts)
}

// stashSaveWithGuard re-reads the live working tree before pushing because
// the dialog's snapshot answer can be stale by the time the write runs, and
// git stash push exits successfully without creating anything when there is
// nothing to save: a silent empty success must become an error instead.
func stashSaveWithGuard(repo *git.Repo, message string, includeUntracked bool) error {
	live, err := repo.Status()
	if err != nil {
		return err
	}
	if live.Head == "" {
		return fmt.Errorf("%s", stashErrUnborn)
	}
	if includeUntracked {
		if len(live.Staged)+len(live.Unstaged) == 0 {
			return fmt.Errorf("%s", stashErrNoChanges)
		}
	} else if !stashHasTrackedChanges(live) {
		return fmt.Errorf("%s", stashErrNoTracked)
	}
	return repo.StashPushWith(git.StashOptions{Message: message, Untracked: includeUntracked})
}

// stashHasTrackedChanges reports whether the status holds any change to a
// tracked file, staged or not. Untracked and ignored entries do not count.
func stashHasTrackedChanges(st git.Status) bool {
	if len(st.Staged) > 0 {
		return true
	}
	for _, f := range st.Unstaged {
		if f.Status != '?' && f.Status != '!' {
			return true
		}
	}
	return false
}

// stashSaveProblem explains why the working tree cannot be stashed right
// now, or "" when it can. It reads the loaded snapshot only; the write
// worker re-checks the live repository before pushing.
func stashSaveProblem(st git.Status, includeUntracked bool) string {
	if st.Head == "" {
		return stashErrUnborn
	}
	if includeUntracked {
		if len(st.Staged)+len(st.Unstaged) == 0 {
			return stashErrNoChanges
		}
		return ""
	}
	if !stashHasTrackedChanges(st) {
		return stashErrNoTracked
	}
	return ""
}

// stashSidebar is the collapsible Stashes section of the sidebar: a Save
// Stash entry, one selectable row per stash newest first, an inline action
// bar on the selected row, a context menu on every row, and the Save,
// Apply and Drop dialogs.
func (a *app) stashSidebar(c *ui.Context) {
	p := a.pal
	st := a.stashUI()
	a.stashResetForRepoGen()
	stashes := a.session.Snapshot().Stashes

	if st.selectedKey != "" && a.session.Loaded() {
		present := false
		for i := range stashes {
			if stashRowKey(stashes[i]) == st.selectedKey {
				present = true
				break
			}
		}
		if !present {
			st.selectedKey = ""
		}
	}

	sidebarSectionHeader(c, "Stashes", len(stashes), &st.collapsed, p)
	if !st.collapsed {
		a.saveStashRow(c)
		if len(stashes) == 0 {
			a.refPlaceholderRow(c, "No stashes")
		} else {
			for i := range stashes {
				a.stashRow(c, stashes[i])
			}
		}
	}

	a.saveStashDialog(c)
	a.applyStashDialog(c)
	a.dropStashDialog(c)
}

// saveStashRow is the Save Stash entry at the head of the Stashes section.
// With no repository open there is nothing to stash, so the entry is hidden
// entirely.
func (a *app) saveStashRow(c *ui.Context) {
	if !a.session.HasRepo() {
		return
	}
	p := a.pal
	b := ui.ButtonBase(c.Key("stash-new")).
		Height(26).
		PaddingX(10).
		Gap(8).
		Radius(6).
		AlignItems(ui.Center).
		Justify(ui.Start).
		Label("Save Stash").
		Tooltip("Stash the working tree changes").
		FocusRing(false)
	if b.Hovered() {
		b.Background(p.hover)
	}
	b.Children(func() {
		ui.Box(c).Width(13).Height(13).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Text(c, "+").FontSize(13).FontWeight(500).TextColor(p.muted).SingleLine()
		})
		ui.Text(c, "Save Stash").FontSize(12.5).FontWeight(400).TextColor(p.muted).SingleLine().Shrink(1).MinWidth(0)
	})
	if b.Clicked() {
		st := a.stashUI()
		st.saveOpen = true
		st.saveMessage = ""
		st.saveUntracked = true
	}
}

// stashRow is one stash entry: icon and message, selectable with a click,
// with a context menu of the same operations the inline action bar of the
// selected row offers. The label carries the reflog name so every row is
// uniquely addressable, and the inner text keeps its own node.
func (a *app) stashRow(c *ui.Context, s git.Stash) {
	p := a.pal
	st := a.stashUI()
	key := stashRowKey(s)
	selected := st.selectedKey == key
	busy := a.session.Busy()

	row := ui.ButtonBase(c.Key("stash-" + s.Name)).
		Height(26).
		PaddingX(10).
		Gap(8).
		Radius(6).
		AlignItems(ui.Center).
		Justify(ui.Start).
		Focusable().
		Role(ui.RoleListItem).
		Label("Stash " + s.Name).
		AccessSelected(selected).
		FocusRing(false)
	switch {
	case selected:
		row.Background(p.selected)
	case row.Hovered():
		row.Background(p.hover)
	}
	row.Children(func() {
		symbolIcon(c, "archivebox", fallbackStash, 13, p.faint)
		ui.Text(c, stashDisplayMessage(s.Message, s.Name)).FontSize(12.5).FontWeight(400).TextColor(p.text).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
	})
	row.ContextMenu(func(m *ui.Menu) {
		if m.Item("Apply…").Disabled(busy).Chosen() {
			a.openApplyStashDialog(s)
		}
		if m.Item("Pop").Disabled(busy).Chosen() {
			a.popStash(s)
		}
		m.Separator()
		if m.Item("Drop…").Disabled(busy).Chosen() {
			a.openDropStashDialog(s)
		}
	})
	if row.Clicked() {
		if st.selectedKey == key {
			st.selectedKey = ""
		} else {
			st.selectedKey = key
		}
	}

	if selected {
		a.stashActionBar(c, s, busy)
	}
}

// stashDisplayMessage is the row text of one entry: its trimmed reflog
// message, or its name when the message is empty.
func stashDisplayMessage(message, name string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return name
	}
	return message
}

// stashActionBar is the compact inline action strip expanded beneath the
// selected row. The buttons disable while a write is in flight so no second
// operation can start re-entrantly. The strip and its buttons are sized tight
// so all three fit inside the sidebar without clipping the last one.
func (a *app) stashActionBar(c *ui.Context, s git.Stash, busy bool) {
	ui.Row(c.Key("stash-actions-"+s.Name)).Height(28).Padding(1, 0, 1, 31).Gap(4).AlignItems(ui.Center).Children(func() {
		if ui.Button(c.Key("stash-apply-"+s.Name), "Apply…").FontSize(11.5).Padding(2, 8).Disabled(busy).Clicked() {
			a.openApplyStashDialog(s)
		}
		if ui.Button(c.Key("stash-pop-"+s.Name), "Pop").FontSize(11.5).Padding(2, 8).Disabled(busy).Clicked() {
			a.popStash(s)
		}
		if ui.Button(c.Key("stash-drop-"+s.Name), "Drop…").FontSize(11.5).Padding(2, 8).Disabled(busy).Clicked() {
			a.openDropStashDialog(s)
		}
	})
}

// openApplyStashDialog captures the identity of s from the stash list of the
// loaded snapshot and opens the Apply Stash dialog. Capturing at the moment
// of the click on the UI thread is what lets the write worker detect a
// reflog that moved while the dialog was open.
func (a *app) openApplyStashDialog(s git.Stash) {
	st := a.stashUI()
	st.applyTarget = captureStashTarget(a.session.Snapshot().Stashes, s)
	st.applyIndex = true
	st.applyPop = false
	st.applyOpen = true
}

// openDropStashDialog captures the identity of s and opens the Drop Stash
// confirmation dialog. Dropping never skips this confirmation.
func (a *app) openDropStashDialog(s git.Stash) {
	st := a.stashUI()
	st.dropTarget = captureStashTarget(a.session.Snapshot().Stashes, s)
	st.dropOpen = true
}

// popStash is the direct Pop of the inline bar and the context menu: no
// dialog, same identity capture and worker verification as the apply path.
func (a *app) popStash(s git.Stash) {
	target := captureStashTarget(a.session.Snapshot().Stashes, s)
	a.session.do("Pop "+s.Name, func(r *git.Repo) error {
		return stashApplyWithGuard(r, target.selection, target.groupCount, git.StashApplyOptions{Pop: true, Index: true})
	})
}

// stashSaveProblemFor is the live explanation of the Save Stash dialog's
// current scope: the whole-tree answer for All changes, or the selected
// files' own answer — an unborn repository, a conflicted working tree, or
// no file checked — for Selected files.
func (a *app) stashSaveProblemFor(files []stashFileEntry) string {
	st := a.stashUI()
	status := a.session.Snapshot().Status
	if st.saveScope == stashScopeSelected {
		return stashSaveSelectedProblem(status, files, st.saveChecked)
	}
	return stashSaveProblem(status, st.saveUntracked)
}

// stashSaveCanSubmit reports whether the Save Stash dialog can submit: not
// busy, and the snapshot shows something the chosen scope would stash.
func (a *app) stashSaveCanSubmit() bool {
	if a.session.Busy() {
		return false
	}
	files := stashSelectedFiles(a.session.Snapshot().Status, a.stashUI().saveUntracked)
	return a.stashSaveProblemFor(files) == ""
}

// saveStashDialog is the modal stashing the working tree: an optional
// message, the untracked-files choice, the scope — the whole working tree
// or only the files checked in the list below — and a live explanation that
// disables the confirm button when there is nothing the chosen options
// would stash. The list is virtual, so a tree of thousands of changed files
// still builds only the rows in view.
func (a *app) saveStashDialog(c *ui.Context) {
	p := a.pal
	st := a.stashUI()
	if !st.saveOpen {
		return
	}
	status := a.session.Snapshot().Status
	files := stashSelectedFiles(status, st.saveUntracked)
	if st.saveChecked == nil {
		st.saveChecked = map[string]bool{}
	}
	stashSaveReconcile(files, st.saveChecked)

	_, cancelled, confirmed := dialogFrame(c, p, "stash-save-modal", &st.saveOpen, "Save Stash", 460, func() {
		ui.Field(c, "Message:", func() {
			in := ui.TextInput(c.Key("stash-save-message"), &st.saveMessage).FillWidth().AutoFocus().Placeholder("Stash message (optional)").Label("Stash message")
			if in.Submitted() && a.stashSaveCanSubmit() {
				a.submitSaveStash()
			}
		})
		ui.Checkbox(c.Key("stash-save-untracked"), &st.saveUntracked, "Include untracked files")
		scope := st.saveScope
		seg := ui.Segmented(c.Key("stash-save-scope"), &scope, "All changes", "Selected files").Label("Stash Scope")
		if seg.Changed() {
			st.saveScope = scope
			if scope == stashScopeSelected {
				// Entering the file choice starts from everything checked,
				// so a Save Stash right away takes the same files All
				// changes would.
				stashSaveAllChecked(stashSelectedFiles(status, st.saveUntracked), st.saveChecked)
			}
		}
		if st.saveScope == stashScopeSelected {
			a.saveStashFileList(c, files)
		}
		if problem := a.stashSaveProblemFor(files); problem != "" {
			ui.Text(c, problem).FontSize(12).TextColor(p.danger).MaxWidth(424)
		}
	},
		dialogAction{key: "stash-save-cancel", text: "Cancel", label: "Cancel"},
		dialogAction{key: "stash-save-confirm", text: "Save Stash", label: "Confirm Save Stash", disabled: !a.stashSaveCanSubmit()})
	if cancelled {
		a.closeSaveStashDialog()
	}
	if confirmed {
		a.submitSaveStash()
	}
	if !st.saveOpen {
		a.closeSaveStashDialog()
	}
}

// saveStashFileList renders the checkable files of the Selected files
// scope. Only the rows in view are built: the list is virtual, so a working
// tree of thousands of changed files costs one frame's rows, not one row
// per file.
func (a *app) saveStashFileList(c *ui.Context, files []stashFileEntry) {
	p := a.pal
	st := a.stashUI()
	st.saveList.Key = func(row int) any { return files[row].path }
	st.saveList.Label = func(row int) string { return "Stash file " + files[row].path }
	ui.List(c.Key("stash-save-files-scroll"), &st.saveList, len(files)).
		MaxHeight(240).FillWidth().
		Rows(func(r ui.ListRow) {
			a.saveStashFileRow(r.Context, p, files[r.Index])
		})
}

// saveStashFileRow renders one file of the Selected files list: the
// checkbox that includes the file in the stash, its status letter and its
// path.
func (a *app) saveStashFileRow(c *ui.Context, p palette, f stashFileEntry) {
	st := a.stashUI()
	checked := st.saveChecked[f.path]
	ui.Row(c.Key("stash-save-row-" + f.path)).FillWidth().Gap(8).AlignItems(ui.Center).PaddingX(2).Children(func() {
		ui.Box(c).Size(16, 16).Radius(4).Background(historyStatusColor(p, f.status).Alpha(0.18)).Center().Shrink(0).Children(func() {
			ui.Text(c, string(f.status)).FontSize(9.5).FontWeight(700).TextColor(historyStatusColor(p, f.status)).SingleLine()
		})
		cb := ui.Checkbox(c.Key("stash-save-check-"+f.path), &checked, f.path).Label("Stash " + f.path).Shrink(1).MinWidth(0)
		if cb.Changed() {
			if st.saveChecked == nil {
				st.saveChecked = map[string]bool{}
			}
			if checked {
				st.saveChecked[f.path] = true
			} else {
				delete(st.saveChecked, f.path)
			}
		}
	})
}

// closeSaveStashDialog closes the Save Stash dialog and resets its draft —
// the message, the untracked choice, the scope and every check mark — so
// the next open starts clean with All changes.
func (a *app) closeSaveStashDialog() {
	st := a.stashUI()
	st.saveOpen = false
	st.saveMessage = ""
	st.saveUntracked = true
	st.saveScope = stashScopeAll
	st.saveChecked = nil
	st.saveList = ui.ListState{}
}

// openSaveStashForFile opens the Save Stash dialog in the Selected files
// scope with exactly one file checked, for the Stash Changes item of the
// file's context menu. An untracked file also ticks the untracked choice,
// without which its checkbox could not even appear.
func (a *app) openSaveStashForFile(f git.FileChange) {
	st := a.stashUI()
	st.saveOpen = true
	st.saveMessage = ""
	st.saveUntracked = true
	st.saveScope = stashScopeSelected
	st.saveChecked = map[string]bool{f.Path: true}
	st.saveList = ui.ListState{}
}

// submitSaveStash captures the dialog values on the UI thread, closes the
// dialog, and stashes through the session's write path. The worker verifies
// the live working tree again before pushing.
func (a *app) submitSaveStash() {
	st := a.stashUI()
	message := strings.TrimSpace(st.saveMessage)
	untracked := st.saveUntracked
	if st.saveScope == stashScopeSelected {
		files := stashSelectedFiles(a.session.Snapshot().Status, untracked)
		paths := stashSaveSubmitPaths(files, st.saveChecked)
		a.closeSaveStashDialog()
		a.session.do("Save Stash", func(r *git.Repo) error {
			return stashSaveSelectedWithGuard(r, message, paths, untracked)
		})
		return
	}
	a.closeSaveStashDialog()
	a.session.do("Save Stash", func(r *git.Repo) error {
		return stashSaveWithGuard(r, message, untracked)
	})
}

// stashTargetTitle is the one-line identity of a dialog's target: its reflog
// name and short commit hash.
func stashTargetTitle(t stashTarget) string {
	hash := strings.TrimSpace(t.hash)
	if len(hash) > 12 {
		hash = hash[:12]
	}
	return t.name + "  " + hash
}

// applyStashDialog is the modal applying one stash entry, with the index
// restore and the delete-after-applying choices. Ticking the deletion choice
// turns the operation into a pop, which still keeps the stash when the apply
// conflicts.
func (a *app) applyStashDialog(c *ui.Context) {
	p := a.pal
	st := a.stashUI()
	if !st.applyOpen {
		return
	}
	tgt := st.applyTarget
	ui.Modal(c.Key("stash-apply-modal"), &st.applyOpen, func() {
		ui.Column(c).Width(440).Padding(4).Gap(10).Children(func() {
			ui.Text(c, "Apply Stash").FontSize(16).FontWeight(600).TextColor(p.text)
			ui.Text(c, stashTargetTitle(tgt)).Font("monospace").FontSize(12).TextColor(p.text)
			if message := strings.TrimSpace(tgt.message); message != "" {
				ui.Text(c, message).FontSize(12).TextColor(p.muted).MaxWidth(410)
			}
			ui.Checkbox(c.Key("stash-apply-index"), &st.applyIndex, "Restore staged changes (--index)")
			if st.applyIndex {
				ui.Text(c, "When the index cannot be restored, the changes are applied unstaged.").FontSize(12).TextColor(p.muted)
			}
			ui.Checkbox(c.Key("stash-apply-pop"), &st.applyPop, "Delete stash after applying")
			if st.applyPop {
				ui.Text(c, "The stash will not be deleted if a conflict occurs.").FontSize(12).TextColor(p.muted)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("stash-apply-cancel"), "Cancel").Clicked() {
					a.closeApplyStashDialog()
				}
				confirm := ui.Button(c.Key("stash-apply-confirm"), "Apply Stash").Label("Confirm Apply Stash").Disabled(a.session.Busy())
				if confirm.Clicked() {
					a.submitApplyStash()
				}
			})
		})
	})
	if !st.applyOpen {
		a.closeApplyStashDialog()
	}
}

// closeApplyStashDialog closes the Apply Stash dialog and drops its captured
// target so a later open captures a fresh one.
func (a *app) closeApplyStashDialog() {
	st := a.stashUI()
	st.applyOpen = false
	st.applyTarget = stashTarget{}
	st.applyIndex = true
	st.applyPop = false
}

// submitApplyStash captures the dialog values on the UI thread, closes the
// dialog, and applies the captured entry through the session's write path.
func (a *app) submitApplyStash() {
	st := a.stashUI()
	target := st.applyTarget
	pop := st.applyPop
	index := st.applyIndex
	a.closeApplyStashDialog()
	label := "Apply " + target.name
	if pop {
		label = "Pop " + target.name
	}
	a.session.do(label, func(r *git.Repo) error {
		return stashApplyWithGuard(r, target.selection, target.groupCount, git.StashApplyOptions{Pop: pop, Index: index})
	})
}

// dropStashDialog is the confirmation modal of the only path deleting a
// stash. It shows exactly which entry will be deleted, warns that the
// deletion is permanent, and its confirm button stays disabled while a
// write is in flight.
func (a *app) dropStashDialog(c *ui.Context) {
	p := a.pal
	st := a.stashUI()
	if !st.dropOpen {
		return
	}
	tgt := st.dropTarget
	ui.Modal(c.Key("stash-drop-modal"), &st.dropOpen, func() {
		ui.Column(c).Width(440).Padding(4).Gap(10).Children(func() {
			ui.Text(c, "Drop Stash").FontSize(16).FontWeight(600).TextColor(p.text)
			ui.Text(c, stashTargetTitle(tgt)).Font("monospace").FontSize(12).TextColor(p.text)
			if message := strings.TrimSpace(tgt.message); message != "" {
				ui.Text(c, message).FontSize(12).TextColor(p.muted).MaxWidth(410)
			}
			ui.Text(c, "Dropping permanently deletes this stash. This cannot be undone.").FontSize(12).TextColor(p.danger)
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("stash-drop-cancel"), "Cancel").Clicked() {
					a.closeDropStashDialog()
				}
				confirm := ui.Button(c.Key("stash-drop-confirm"), "Drop Stash").Label("Confirm Drop Stash").Disabled(a.session.Busy())
				if confirm.Clicked() {
					a.submitDropStash()
				}
			})
		})
	})
	if !st.dropOpen {
		a.closeDropStashDialog()
	}
}

// closeDropStashDialog closes the Drop Stash dialog and drops its captured
// target. Canceling or dismissing the dialog never deletes anything.
func (a *app) closeDropStashDialog() {
	st := a.stashUI()
	st.dropOpen = false
	st.dropTarget = stashTarget{}
}

// submitDropStash captures the dialog target on the UI thread, closes the
// dialog, and deletes exactly the captured entry through the session's write
// path. The worker re-reads the live reflog first: a read failure, a
// vanished target, or a changed group count returns an error and deletes
// nothing; a shifted target is still found by identity and removed while
// newer entries stay untouched.
func (a *app) submitDropStash() {
	st := a.stashUI()
	target := st.dropTarget
	a.closeDropStashDialog()
	a.session.do("Drop "+target.name, func(r *git.Repo) error {
		return stashDropWithGuard(r, target.selection, target.groupCount)
	})
}
