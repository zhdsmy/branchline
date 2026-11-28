package app

import (
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// tagsUIState is the sidebar Tags state of one app instance: the repository
// generation it belongs to, the New Tag dialog draft, and the Delete Tag
// confirmation dialog with its asynchronous verification. It lives in the
// app-keyed tagsUIStates table because app is a shared file. The tag rows'
// selection is not here: it belongs to the sidebar's multi-selection on
// refsState, the single source of every reference row's selection.
type tagsUIState struct {
	repoGen uint64

	newOpen        bool
	newName        string
	newTarget      string
	newTargetHint  string
	newMessage     string
	newNameChanged bool
	newPush        bool
	newPushRemote  string

	delOpen     bool
	delTarget   string
	delChecking bool
	delInfo     git.TagRefInfo
	delErr      error
}

var tagsUIStates = struct {
	sync.Mutex
	m map[*app]*tagsUIState
}{m: map[*app]*tagsUIState{}}

// tagsUI returns the app's Tags sidebar state, creating it on first use. A
// repository switch discards the previous repository's selection and dialog
// drafts and invalidates a delete verification still in flight, so its late
// answer can never apply to the new repository. It runs on the UI thread.
func (a *app) tagsUI() *tagsUIState {
	tagsUIStates.Lock()
	defer tagsUIStates.Unlock()
	st := tagsUIStates.m[a]
	if st == nil {
		st = &tagsUIState{}
		tagsUIStates.m[a] = st
	}
	if gen := a.session.Generation(); st.repoGen != gen {
		if st.delChecking {
			a.session.cancelRead("tags-delete-check")
		}
		*st = tagsUIState{repoGen: gen}
	}
	return st
}

// tagsSidebarHeaderActions renders the New Tag entry at the head of the
// Tags section. Without a repository open there is nothing to tag, so the
// entry is hidden entirely. The Push All Tags entry behind it is its own
// row of the sidebar's virtual list, drawn where it always was.
func (a *app) tagsSidebarHeaderActions(c *ui.Context) {
	if !a.session.HasRepo() {
		return
	}
	p := a.pal
	b := ui.ButtonBase(c.Key("tag-new")).
		Height(26).
		PaddingX(10).
		Gap(8).
		Radius(6).
		AlignItems(ui.Center).
		Justify(ui.Start).
		Label("New Tag").
		Tooltip("Create a new tag").
		FocusRing(false)
	if b.Hovered() {
		b.Background(p.hover)
	}
	b.Children(func() {
		ui.Box(c).Width(13).Height(13).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Text(c, "+").FontSize(13).FontWeight(500).TextColor(p.muted).SingleLine()
		})
		ui.Text(c, "New Tag").FontSize(12.5).FontWeight(400).TextColor(p.muted).SingleLine().Shrink(1).MinWidth(0)
	})
	if b.Clicked() {
		a.openNewTagDialog("")
	}
}

// tagRow is one tag in the sidebar: the row joins the multi-selection the
// references share — clicking selects it alone, Cmd-click toggles it, and a
// Shift-click selects the range from the anchor along sorted, the visible
// tags in the order the sidebar shows them. The text is the last slash
// segment of the name — the folders ahead of it are rows of their own —
// while the accessible name and the tooltip keep the full name. A lone
// selected tag looks exactly as a selected tag always did: the selected
// wash, the unfolded action bar, and a plain click on its own row takes the
// selection off again. With two or more tags selected every selected row
// carries the wash and reads selected to assistive technology, no action
// bar unfolds, and the row's right-click menu becomes the batch menu of the
// selection. A right click on a row outside the selection narrows the
// selection to it first.
func (a *app) tagRow(c *ui.Context, tag git.Tag, sorted []git.Tag, pickedCount int, padLeft float32) {
	p := a.pal
	key := tagPickKey(tag.Name)
	picked := a.refsView.pick[key]
	row := ui.Row(c.Key("tag-"+tag.Name)).
		Height(26).
		Padding(0, 10, 0, padLeft).
		Gap(8).
		Radius(6).
		AlignItems(ui.Center).
		Focusable().
		Role(ui.RoleListItem).
		Label(tag.Name).
		AccessSelected(picked)
	if tip := sidebarRefTooltip(tag.Name); tip != "" {
		row.Tooltip(tip)
	}
	switch {
	case picked:
		row.Background(p.selected)
	case row.Hovered():
		row.Background(p.hover)
	}
	row.Children(func() {
		symbolIcon(c, "tag", fallbackTag, 13, p.faint)
		ui.Text(c, sidebarLastSegment(tag.Name)).FontSize(12.5).FontWeight(400).TextColor(p.text).SingleLine().Shrink(1).MinWidth(0)
	})
	row.ContextMenu(func(m *ui.Menu) {
		pick := a.refsView.pick
		if !pick[key] {
			// The right click landed outside the selection: the menu opens on
			// this row alone, exactly as if the row had been clicked first.
			a.selectOnlySidebarRef(key)
			pick = a.refsView.pick
		}
		if pick[key] && sidebarTagPickCount(pick, sorted) >= 2 {
			a.tagBatchMenuItems(m, pickedTags(pick, sorted))
			return
		}
		a.tagRowContextMenu(m, tag)
	})
	if row.Clicked() {
		mods := row.ClickModifiers()
		if mods == 0 && pickedCount == 1 && picked {
			// A plain click on the lone selected tag takes the selection off
			// again, exactly as the single-tag selection always behaved. The
			// anchor stays, so a range click after the deselection still
			// starts from this row, as it does after a Cmd-click toggle-out.
			a.clearSidebarPick()
		} else {
			a.clickSidebarTag(tag.Name, mods, sorted)
		}
	}
}

// tagRowActions renders the compact action bar unfolding under a selected tag
// row. The delete action opens the verified confirmation dialog, the push
// action the push dialog; both stay disabled while a write is in flight, and
// pushing needs a remote to push to.
func (a *app) tagRowActions(c *ui.Context, tag git.Tag) {
	pushDisabled := a.session.Busy() || len(a.session.Snapshot().Refs.Remotes) == 0
	ui.Row(c.Key("tag-actions-"+tag.Name)).Height(24).Padding(0, 10, 0, 29).Gap(8).AlignItems(ui.Center).Children(func() {
		if ui.Button(c.Key("tag-push-btn-"+tag.Name), "Push…").
			Label("Push tag "+tag.Name).
			FontSize(11).
			Padding(2, 6).
			Disabled(pushDisabled).
			Clicked() {
			a.openPushTagDialog(tag.Name)
		}
		if ui.Button(c.Key("tag-del-btn-"+tag.Name), "Delete…").
			Label("Delete tag "+tag.Name).
			FontSize(11).
			Padding(2, 6).
			Disabled(a.session.Busy()).
			Clicked() {
			a.openDeleteTagDialog(tag.Name)
		}
		ui.Spacer(c)
	})
}

// tagRowContextMenu is the secondary-click menu of one tag row: the push and
// delete actions, disabled while a write is in flight.
func (a *app) tagRowContextMenu(m *ui.Menu, tag git.Tag) {
	a.tagMenuHeadItems(m, tag)
	pushDisabled := a.session.Busy() || len(a.session.Snapshot().Refs.Remotes) == 0
	if m.Item("Push…").Disabled(pushDisabled).Chosen() {
		a.openPushTagDialog(tag.Name)
	}
	a.tagMergeRebaseMenuItems(m, tag)
	a.tagCreateMenuItems(m, tag)
	if m.Item("Delete…").Disabled(a.session.Busy()).Chosen() {
		a.openDeleteTagDialog(tag.Name)
	}
	a.tagMenuTailItems(m, tag)
}

// openNewTagDialog opens the New Tag dialog. rev pre-fills the target when
// set; without one the commit selected on the History page pre-fills it, and
// without either the target is HEAD. Editing the target afterwards drops the
// hint of what it pointed at.
func (a *app) openNewTagDialog(rev string) {
	st := a.tagsUI()
	target := strings.TrimSpace(rev)
	hint := ""
	if target == "" {
		if a.route.page == pageHistory && a.historyView.sel != "" {
			target = a.historyView.sel
			hint = shortTagHash(target) + " " + a.historyView.commit.Subject
		} else {
			target = "HEAD"
		}
	}
	st.newOpen = true
	st.newName = ""
	st.newTarget = target
	st.newTargetHint = hint
	st.newMessage = ""
	st.newNameChanged = false
	st.newPush = false
	st.newPushRemote = ""
}

func shortTagHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

// newTagProblem says why the typed name cannot be created, empty when it
// can: invalid characters or a collision with an existing tag.
func (a *app) newTagProblem(name string) string {
	if name == "" {
		return ""
	}
	if p := git.ValidateTagName(name); p != "" {
		return p
	}
	for _, tag := range a.session.Snapshot().Refs.Tags {
		if tag.Name == name {
			return "A tag named " + name + " already exists"
		}
	}
	return ""
}

// newTagTargetProblem says why the typed target cannot be tagged, empty when
// the snapshot cannot rule it out. Only the states the snapshot proves are
// reported — an unborn repository tagging HEAD; the live peel inside
// SafeCreateTag is the final arbiter of everything else.
func (a *app) newTagTargetProblem() string {
	st := a.tagsUI()
	target := strings.TrimSpace(st.newTarget)
	if target == "" {
		return "The tag needs a target commit."
	}
	if strings.ContainsAny(target, " \t\r\n") {
		return "The target commit cannot contain whitespace."
	}
	if strings.HasPrefix(target, "-") {
		return "The target commit cannot begin with '-'."
	}
	if a.session.Snapshot().Status.Head == "" && target == "HEAD" {
		return "The repository has no commits yet: make a commit before tagging."
	}
	return ""
}

func (a *app) newTagCanSubmit() bool {
	st := a.tagsUI()
	name := strings.TrimSpace(st.newName)
	return name != "" &&
		a.newTagProblem(name) == "" &&
		a.newTagTargetProblem() == "" &&
		!a.session.Busy()
}

// newTagDialog is the modal creating a lightweight or annotated tag: a name
// validated live against git's rules and the existing tags, the commit the
// tag goes on, and an optional message that makes the tag annotated.
func (a *app) newTagDialog(c *ui.Context) {
	p := a.pal
	st := a.tagsUI()
	if !st.newOpen {
		return
	}
	_, cancelled, confirmed := dialogFrame(c, p, "new-tag", &st.newOpen, "New Tag", 440, func() {
		ui.Field(c, "Target commit:", func() {
			in := ui.TextInput(c, &st.newTarget).FillWidth().Placeholder("HEAD").Label("Target commit")
			if in.Changed() {
				// The hint belongs to the pre-filled target only: once the
				// user edits the target it no longer describes anything.
				st.newTargetHint = ""
			}
		})
		if st.newTargetHint != "" {
			ui.Text(c, st.newTargetHint).FontSize(11.5).TextColor(p.faint).SingleLine()
		}
		ui.Field(c, "Tag name:", func() {
			in := ui.TextInput(c, &st.newName).FillWidth().AutoFocus().Placeholder("Enter Tag Name").Label("Tag name")
			if in.Changed() {
				st.newNameChanged = true
			}
			// Enter inside the name field is the field's own submit: the
			// single-line input takes the key before the frame's shortcut can
			// see it, so this is the one channel confirming from the keyboard
			// while the name is being typed.
			if in.Submitted() && a.newTagCanSubmit() {
				a.submitNewTag()
			}
		})
		name := strings.TrimSpace(st.newName)
		if problem := a.newTagProblem(name); problem != "" && (st.newNameChanged || name != "") {
			ui.Text(c, problem).FontSize(12).TextColor(p.danger)
		}
		ui.Field(c, "Message:", func() {
			ui.TextArea(c.Key("new-tag-message"), &st.newMessage).
				Placeholder("Optional message: makes the tag annotated").
				Label("Tag message").
				Height(72).
				FillWidth()
		})
		if remotes := a.session.Snapshot().Refs.Remotes; len(remotes) > 0 {
			pushBox := ui.Checkbox(c.Key("new-tag-push"), &st.newPush, "Push to remote").Label("Push to remote")
			if pushBox.Changed() && st.newPush && st.newPushRemote == "" {
				st.newPushRemote = defaultRemoteOf(a.session.Snapshot().Refs)
			}
			if st.newPush {
				ui.Field(c, "Remote:", func() {
					ui.Select(c, &st.newPushRemote, remotes).FillWidth().Label("New Tag push remote")
				})
			}
		}
		if problem := a.newTagTargetProblem(); problem != "" {
			ui.Text(c, problem).FontSize(12).TextColor(p.danger)
		}
	},
		dialogAction{key: "tag-new-cancel", text: "Cancel", label: "Cancel"},
		// The name field keeps the focus it autofocuses, so the primary
		// button asks for none of it.
		dialogAction{key: "tag-new-create", text: "Create Tag", label: "Create Tag", disabled: !a.newTagCanSubmit()})
	if cancelled {
		a.closeNewTagDialog()
	}
	if confirmed {
		a.submitNewTag()
	}
	if !st.newOpen {
		a.closeNewTagDialog()
	}
}

func (a *app) closeNewTagDialog() {
	st := a.tagsUI()
	st.newOpen = false
	st.newName = ""
	st.newTarget = ""
	st.newTargetHint = ""
	st.newMessage = ""
	st.newNameChanged = false
	st.newPush = false
	st.newPushRemote = ""
}

// submitNewTag captures the dialog values on the UI thread, closes the
// dialog, and creates the tag through the session's write path: the worker
// re-verifies on the repository itself that the name is free and the target
// is a commit before anything is created.
func (a *app) submitNewTag() {
	if a.tagsUI().newPush {
		a.submitNewTagWithPush()
		return
	}
	st := a.tagsUI()
	name := strings.TrimSpace(st.newName)
	target := strings.TrimSpace(st.newTarget)
	message := st.newMessage
	a.closeNewTagDialog()
	if name == "" || target == "" {
		return
	}
	a.session.do("Create tag "+name, func(r *git.Repo) error {
		return r.SafeCreateTag(name, target, message)
	})
}

// openDeleteTagDialog opens the confirmation dialog for name and starts the
// asynchronous verification that gates its confirm button: the exact object
// the tag points at, read on the repository itself.
func (a *app) openDeleteTagDialog(name string) {
	st := a.tagsUI()
	st.delOpen = true
	st.delTarget = name
	st.delChecking = true
	st.delInfo = git.TagRefInfo{}
	st.delErr = nil
	target := name
	sessionRead(a.session, "tags-delete-check", func(r *git.Repo) (git.TagRefInfo, error) {
		return r.TagRefInfo(target)
	}, func(info git.TagRefInfo, err error) {
		if !st.delOpen || st.delTarget != target {
			return
		}
		st.delChecking = false
		st.delInfo = info
		st.delErr = err
	})
}

// deleteTagDialog is the confirmation dialog for deleting a tag. The confirm
// button stays disabled while the verification runs and when it failed: a
// tag whose state is unknown is never deleted. What the user confirms — the
// exact object the tag pointed at — is re-verified in the delete worker
// against the repository itself: a tag retargeted or recreated since the
// check is a different tag and is never deleted by it.
func (a *app) deleteTagDialog(c *ui.Context) {
	p := a.pal
	st := a.tagsUI()
	if !st.delOpen {
		return
	}
	_, cancelled, confirmed := dialogFrame(c, p, "del-tag", &st.delOpen, "Delete Tag", 460, func() {
		ui.Field(c, "Tag:", func() {
			ui.Text(c, st.delTarget).Font("monospace").FontSize(12).TextColor(p.text)
		})
		switch {
		case st.delChecking:
			ui.Text(c, "Checking tag details…").FontSize(12).TextColor(p.muted)
		case st.delErr != nil:
			ui.Text(c, "Cannot verify this tag. The tag will not be deleted.").FontSize(12).TextColor(p.danger)
			ui.Text(c, st.delErr.Error()).FontSize(12).TextColor(p.danger)
		default:
			info := st.delInfo
			kind := "Lightweight tag"
			if info.Annotated {
				kind = "Annotated tag"
			}
			ui.Text(c, kind+" on commit "+shortTagHash(info.CommitOID)+".").FontSize(12).TextColor(p.muted)
			if info.Annotated && strings.TrimSpace(info.Message) != "" {
				ui.Text(c, firstLineOf(info.Message)).FontSize(12).TextColor(p.muted)
			}
		}
	},
		dialogAction{key: "tag-del-cancel", text: "Cancel", label: "Cancel"},
		dialogAction{key: "tag-del-confirm", text: "Delete Tag", label: "Confirm Delete Tag", disabled: st.delChecking || st.delErr != nil || a.session.Busy(), destructive: true})
	if cancelled {
		a.closeDeleteTagDialog()
	}
	if confirmed {
		a.submitDeleteTag()
	}
	if !st.delOpen {
		a.closeDeleteTagDialog()
	}
}

// closeDeleteTagDialog closes the confirmation dialog and invalidates any
// verification still in flight so its late result is discarded. The object
// id the dialog verified is kept nowhere outside the dialog state, so a
// closed dialog can never submit.
func (a *app) closeDeleteTagDialog() {
	st := a.tagsUI()
	if st.delOpen || st.delChecking {
		a.session.cancelRead("tags-delete-check")
	}
	st.delOpen = false
	st.delTarget = ""
	st.delChecking = false
	st.delInfo = git.TagRefInfo{}
	st.delErr = nil
}

// submitDeleteTag deletes the dialog's target through the session's write
// path. The worker re-verifies on the repository itself that the tag still
// points at the exact object the user confirmed: the dialog's answer can be
// stale by the time the write runs, and a tag is never deleted on a failed
// or outdated verification.
func (a *app) submitDeleteTag() {
	st := a.tagsUI()
	target := st.delTarget
	expectedRefOID := st.delInfo.RefOID
	a.closeDeleteTagDialog()
	if target == "" || expectedRefOID == "" {
		return
	}
	a.session.do("Delete tag "+target, func(r *git.Repo) error {
		return r.SafeDeleteTag(target, expectedRefOID, nil)
	})
}

// tagsDialogs mounts the New Tag, Delete Tag and remote push modals of the
// refs sidebar.
func (a *app) tagsDialogs(c *ui.Context) {
	a.newTagDialog(c)
	a.deleteTagDialog(c)
	a.tagsPushDialogs(c)
	a.tagExtraDialogs(c)
}
