package app

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// refsState holds the sidebar branches/remotes/tags state on app:
// the new-branch dialog draft, the delete-branch confirmation dialog with
// its asynchronous merged check, the multi-selection of reference rows with
// the batch Delete Branches dialog and the batch clipboard text, the caches
// of the sorted reference lists, and the repository generation they belong
// to so switching repositories discards them.
type refsState struct {
	repoGen uint64

	newOpen        bool
	newName        string
	newCheckout    bool
	newNameChanged bool

	delOpen        bool
	delTarget      string
	delChecking    bool
	delMerged      bool
	delErr         error
	delForceAgreed bool

	// pick is the sidebar's multi-selection, keyed by reference kind and
	// name; pickAnchor is the row range clicks start from. Keys whose rows a
	// refresh dropped stay until they are filtered out at use. pickVersion
	// steps at every mutation of the selection, so the cached sidebar row
	// list and pick counts rebuild exactly when the selection changed.
	pick        map[sidebarPickKey]bool
	pickAnchor  sidebarPickKey
	pickVersion uint64

	batchDelOpen       bool
	batchDelRows       []batchDelRow
	batchDelAlsoRemote bool

	batchClipboard    string
	hasBatchClipboard bool

	// The batch dialogs of the remote branch and tag multi-selections, all
	// captured on the UI thread when they open: the rows every dialog lists
	// with the commit each reference pointed at then, the push dialog's
	// remote, and the delete-tags dialog's remote box. Everything resets with
	// the repository generation: canceling, dismissing or switching
	// repositories can never push or delete.
	rbdOpen  bool
	rbdRows  []refBatchRow
	ptOpen   bool
	ptRemote string
	ptRows   []refBatchRow
	tdOpen   bool
	tdRows   []refBatchRow
	tdRemote bool

	localSortCache  refSortCache[git.Branch]
	remoteSortCache refSortCache[git.Branch]
	tagSortCache    refSortCache[git.Tag]

	// sidebarList is the scroll state of the sidebar's virtual row list, and
	// sidebarRows the cached row table it shows: the rows in display order,
	// the sorted reference slices they were built from, and the multi-select
	// counts, all recomputed only when one of the inputs they were built
	// from changes.
	sidebarList ui.ListState
	sidebarRows sidebarRowCache
}

// resetForRepoGen discards dialog drafts belonging to another repository
// and cancels the merged check of a closing delete dialog.
func (a *app) refsResetForRepoGen() {
	if a.refsView.repoGen == a.session.Generation() {
		return
	}
	if a.refsView.delOpen {
		a.session.cancelRead("refs-delete-check")
	}
	a.refsView = refsState{repoGen: a.session.Generation()}
}

// refsSidebar mounts the dialogs of the collapsible Branches, Remotes, and
// Tags sidebar: the current branch is marked, double-clicking a local branch
// checks it out, branches are created from a dialog and deleted through a
// confirmation dialog with a merged-state check. The rows themselves are
// entries of the sidebar's virtual row list, drawn there; the dialogs mount
// outside it, unchanged and in the order they always mounted.
func (a *app) refsSidebar(c *ui.Context) {
	a.newBranchDialog(c)
	a.deleteBranchDialog(c)
	a.batchBranchDialogs(c)
	a.refBatchDialogs(c)
	a.tagsDialogs(c)
	a.mergeDialog(c)
	a.rebaseDialog(c)
	a.renameBranchDialog(c)
	a.remoteBranchMenuDialogs(c)
	a.remoteDialogs(c)
	a.branchCreateModals(c)
	a.remoteWebFrame(c)
}

// newBranchRow is the New Branch entry at the head of the Branches section.
// With no repository open there is nothing to branch from, so the entry is
// hidden entirely.
func (a *app) newBranchRow(c *ui.Context) {
	if !a.session.HasRepo() {
		return
	}
	p := a.pal
	b := ui.ButtonBase(c.Key("br-new")).
		Height(26).
		PaddingX(10).
		Gap(8).
		Radius(6).
		AlignItems(ui.Center).
		Justify(ui.Start).
		Label("New Branch").
		Tooltip("Create a new branch").
		FocusRing(false)
	if b.Hovered() {
		b.Background(p.hover)
	}
	b.Children(func() {
		ui.Box(c).Width(13).Height(13).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Text(c, "+").FontSize(13).FontWeight(500).TextColor(p.muted).SingleLine()
		})
		ui.Text(c, "New Branch").FontSize(12.5).FontWeight(400).TextColor(p.muted).SingleLine().Shrink(1).MinWidth(0)
	})
	if b.Clicked() {
		a.refsView.newOpen = true
		a.refsView.newName = ""
		a.refsView.newCheckout = true
		a.refsView.newNameChanged = false
	}
}

// branchRow is one local branch: emphasized when current, double-clicked to
// check out, with a delete button on non-current branches. Clicking selects
// the row in the sidebar's multi-selection — Cmd-click toggles it, a Shift-
// click selects the range from the anchor along ordered, the branches in the
// sidebar's display order — and with two or more rows selected every
// selected row carries the selected wash, while the row's right-click menu
// becomes the batch menu of the selection. A right click on a row outside
// the selection narrows the selection to it first, so the batch menu only
// ever shows what the user sees selected. ordered is the cached sorted slice
// of this frame; reading it here never re-sorts or allocates.
func (a *app) branchRow(c *ui.Context, br git.Branch, inflight bool, ordered []git.Branch, pickedCount int) {
	p := a.pal
	iconColor := p.faint
	weight := 400
	if br.Current {
		iconColor = p.accent
		weight = 600
	}
	multi := pickedCount >= 2
	picked := a.refsView.pick[localBranchPickKey(br.Name)]
	row := ui.Row(c.Key("br-" + br.FullName)).
		Height(26).
		PaddingX(10).
		Gap(8).
		Radius(6).
		AlignItems(ui.Center).
		Focusable().
		Role(ui.RoleListItem).
		Label(br.Name).
		AccessSelected(br.Current || (multi && picked))
	switch {
	case multi && picked:
		row.Background(p.selected)
	case row.Hovered():
		row.Background(p.hover)
	}
	row.Children(func() {
		symbolIcon(c, "arrow.triangle.branch", fallbackBr, 13, iconColor)
		ui.Text(c, br.Name).FontSize(12.5).FontWeight(weight).TextColor(p.text).SingleLine().Shrink(1).MinWidth(0)
		ui.Spacer(c)
		if !br.Current {
			delColor := p.faint
			// The delete affordance stays dim until the row or the button is
			// hovered, so the sidebar does not read as a column of icons.
			if !row.Hovered() {
				delColor = delColor.Alpha(0.45)
			}
			del := iconBtn(c.Key("br-del-"+br.FullName), "minus.circle", fallbackMinusCircle, 11, delColor, "Delete branch "+br.Name, p)
			if del.Clicked() && !inflight {
				a.openDeleteBranchDialog(br.Name)
			}
		}
	})
	if row.Clicked() {
		a.clickSidebarBranch(br.Name, row.ClickModifiers(), ordered)
	}
	if row.DoubleClicked() && !br.Current {
		name := br.Name
		a.session.do("Checkout "+name, func(r *git.Repo) error {
			return r.Checkout(name)
		})
	}
	row.ContextMenu(func(m *ui.Menu) {
		pick := a.refsView.pick
		if !pick[localBranchPickKey(br.Name)] {
			// The right click landed outside the selection: the menu opens
			// on this row alone, exactly as if the row had been clicked
			// first. A context menu press never reaches RightClicked — it
			// opens the menu instead — so the narrowing happens here, in the
			// build the press runs. The build also replays once when an item
			// is chosen; narrowing is idempotent, so the replay changes
			// nothing.
			a.selectOnlySidebarBranch(br.Name)
			pick = a.refsView.pick
		}
		if pick[localBranchPickKey(br.Name)] && sidebarPickCount(pick, ordered) >= 2 {
			a.branchBatchMenuItems(m, pickedLocalBranches(pick, ordered))
			return
		}
		a.branchMergeMenuItems(m, br)
		a.branchRebaseMenuItems(m, br)
		a.branchCreateMenuItems(m, br)
		a.branchTrackingMenuItems(m, br)
		a.branchSyncMenuItems(m, br)
		a.branchWebMenuItems(m, br)
		a.branchManageMenuItems(m, br)
	})
}

// remoteBranchRow is one remote-tracking branch row indented under its
// remote, in the order the settings ask for. Sorting a copy of the whole
// list keeps this remote's subsequence sorted, and the snapshot's own slice
// is never reordered. Clicking selects the row in the sidebar's
// multi-selection — Cmd-click toggles it, Shift-click selects the range
// from the anchor along the rows as the sidebar shows them, remote by
// remote — and with two or more rows selected every selected row carries
// the selected wash and the row's right-click menu becomes the batch menu
// of the selection. A right click on a row outside the selection narrows
// the selection to it first.
func (a *app) remoteBranchRow(c *ui.Context, rb git.Branch, refs git.Refs, sorted []git.Branch, pickedCount int) {
	p := a.pal
	multi := pickedCount >= 2
	picked := a.refsView.pick[remoteBranchPickKey(rb.FullName)]
	row := ui.Row(c.Key("rem-br-"+rb.FullName)).
		Height(24).
		Padding(0, 10, 0, 29).
		Gap(8).
		AlignItems(ui.Center).
		Focusable().
		Role(ui.RoleListItem).
		Label(rb.Name).
		AccessSelected(multi && picked)
	switch {
	case multi && picked:
		row.Background(p.selected)
	case row.Hovered():
		row.Background(p.hover)
	}
	row.Children(func() {
		symbolIcon(c, "arrow.triangle.branch", fallbackBr, 12, p.faint)
		ui.Text(c, rb.Name).FontSize(12).TextColor(p.muted).SingleLine().Shrink(1).MinWidth(0)
	})
	if row.Clicked() {
		a.clickSidebarRemoteBranch(rb, row.ClickModifiers(), refs)
	}
	row.ContextMenu(func(m *ui.Menu) {
		pick := a.refsView.pick
		key := remoteBranchPickKey(rb.FullName)
		if !pick[key] {
			// The right click landed outside the selection: the menu opens
			// on this row alone, exactly as if the row had been clicked
			// first. The narrowing happens in the build the press runs,
			// as it does for local branches.
			a.selectOnlySidebarRef(key)
			pick = a.refsView.pick
		}
		if pick[key] && sidebarRemotePickCount(pick, sorted) >= 2 {
			a.remoteBranchBatchMenuItems(m, pickedRemoteBranches(pick, refs, sorted))
			return
		}
		a.remoteBranchCheckoutMenuItems(m, rb)
		a.remoteBranchPullMenuItems(m, rb)
		a.remoteBranchMergeMenuItems(m, rb)
		a.remoteBranchRebaseMenuItems(m, rb)
		a.remoteBranchCreateMenuItems(m, rb)
		a.remoteBranchDeleteMenuItems(m, rb)
		a.remoteBranchWebMenuItems(m, rb)
		a.remoteBranchCopyMenuItem(m, rb)
	})
}

func (a *app) refStubRow(c *ui.Context, key, sfName string, fallback *ui.SVG, name string, current bool) {
	p := a.pal
	iconColor := p.faint
	weight := 400
	if current {
		iconColor = p.accent
		weight = 600
	}
	ui.Row(c.Key(key)).Height(26).PaddingX(10).Gap(8).AlignItems(ui.Center).Children(func() {
		symbolIcon(c, sfName, fallback, 13, iconColor)
		ui.Text(c, name).FontSize(12.5).FontWeight(weight).TextColor(p.text).SingleLine().Shrink(1).MinWidth(0)
	})
}

func (a *app) refPlaceholderRow(c *ui.Context, text string) {
	ui.Row(c).Height(24).PaddingX(18).AlignItems(ui.Center).Children(func() {
		ui.Text(c, text).FontSize(12).TextColor(a.pal.faint).SingleLine()
	})
}

// newBranchProblem says why the typed name cannot be created, empty when it
// can: invalid characters or a collision with an existing local branch.
// Beyond git check-ref-format it rejects names git would not treat as branch
// names at all on its command line: one starting with "-" is read as options
// (git branch -l HEAD creates nothing and exits 0), one holding whitespace
// splits into several arguments, and one ending in "/" is a directory, never
// a ref.
func (a *app) newBranchProblem(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if strings.HasPrefix(name, "-") {
		return "Name cannot begin with '-'"
	}
	if strings.ContainsAny(name, " \t\r\n") {
		return "Name cannot contain whitespace"
	}
	if strings.HasSuffix(name, "/") {
		return "Name cannot end with '/'"
	}
	if p := git.RefNameProblem(name); p != "" {
		return p
	}
	for _, br := range a.session.Snapshot().Refs.Branches {
		if br.Name == name {
			return "A branch named " + name + " already exists"
		}
	}
	return ""
}

// newBranchDialog is the modal creating a branch at HEAD, checking it out
// when the box stays ticked.
func (a *app) newBranchDialog(c *ui.Context) {
	p := a.pal
	st := &a.refsView
	if !st.newOpen {
		return
	}
	ui.Modal(c, &st.newOpen, func() {
		ui.Column(c).Width(440).Padding(4).Gap(10).Children(func() {
			ui.Text(c, "New Branch").FontSize(16).FontWeight(600).TextColor(p.text)
			ui.Field(c, "Name:", func() {
				in := ui.TextInput(c, &st.newName).FillWidth().AutoFocus().Placeholder("Branch name").Label("Branch name")
				if in.Changed() {
					st.newNameChanged = true
				}
				if in.Submitted() && a.newBranchCanSubmit() {
					a.submitNewBranch()
				}
			})
			if problem := a.newBranchProblem(st.newName); problem != "" && (st.newNameChanged || strings.TrimSpace(st.newName) != "") {
				ui.Text(c, problem).FontSize(12).TextColor(p.danger)
			}
			ui.Checkbox(c, &st.newCheckout, "Checkout after create")
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				cancel := ui.Button(c.Key("br-new-cancel"), "Cancel")
				if cancel.Clicked() {
					st.newOpen = false
				}
				create := ui.Button(c.Key("br-new-create"), "Create Branch").Disabled(!a.newBranchCanSubmit())
				if create.Clicked() {
					a.submitNewBranch()
				}
			})
		})
	})
}

func (a *app) newBranchCanSubmit() bool {
	st := &a.refsView
	name := strings.TrimSpace(st.newName)
	return name != "" && a.newBranchProblem(name) == "" && !a.session.Busy()
}

// submitNewBranch captures the dialog values on the UI thread, closes the
// dialog, and creates the branch through the session's write path.
func (a *app) submitNewBranch() {
	st := &a.refsView
	name := strings.TrimSpace(st.newName)
	checkout := st.newCheckout
	st.newOpen = false
	st.newName = ""
	st.newNameChanged = false
	a.session.do("Create branch "+name, func(r *git.Repo) error {
		return r.CreateBranch(name, "HEAD", checkout)
	})
}

// openDeleteBranchDialog opens the confirmation dialog for name and starts
// the asynchronous merged check that gates its confirm button.
func (a *app) openDeleteBranchDialog(name string) {
	st := &a.refsView
	st.delOpen = true
	st.delTarget = name
	st.delChecking = true
	st.delMerged = false
	st.delErr = nil
	st.delForceAgreed = false
	target := name
	sessionRead(a.session, "refs-delete-check", func(r *git.Repo) (bool, error) {
		return r.BranchMerged(target)
	}, func(merged bool, err error) {
		if !st.delOpen || st.delTarget != target {
			return
		}
		st.delChecking = false
		st.delMerged = merged
		st.delErr = err
	})
}

// deleteBranchDialog is the confirmation dialog for deleting a branch. The
// confirm button stays disabled while the merged check runs and is removed
// entirely when the check failed: a branch whose merged state is unknown is
// never deleted. An unmerged branch asks for an explicit second agreement
// before Force Delete can run. Any answer the user acted on — the force
// checkbox, Confirm or Force Delete — is re-verified in the delete worker
// against the repository itself: a branch that changed since the check ran
// (or whose check failed) is never deleted by it.
func (a *app) deleteBranchDialog(c *ui.Context) {
	p := a.pal
	st := &a.refsView
	if !st.delOpen {
		return
	}
	ui.Modal(c, &st.delOpen, func() {
		ui.Column(c).Width(460).Padding(4).Gap(10).Children(func() {
			ui.Text(c, "Delete Branch").FontSize(16).FontWeight(600).TextColor(p.text)
			ui.Field(c, "Branch:", func() {
				ui.Text(c, st.delTarget).Font("monospace").FontSize(12).TextColor(p.text)
			})
			switch {
			case st.delChecking:
				ui.Text(c, "Checking whether the branch is merged…").FontSize(12).TextColor(p.muted)
			case st.delErr != nil:
				ui.Text(c, "Cannot verify whether this branch is merged. The branch will not be deleted.").FontSize(12).TextColor(p.danger)
				ui.Text(c, st.delErr.Error()).FontSize(12).TextColor(p.danger)
			case st.delMerged:
				ui.Text(c, "This branch is fully merged into the current HEAD. Deleting it loses no commits.").FontSize(12).TextColor(p.muted)
			default:
				ui.Text(c, "This branch has commits not merged into the current HEAD. Force deleting it will lose those commits.").FontSize(12).TextColor(p.danger)
				ui.Checkbox(c, &st.delForceAgreed, "I understand, force delete this branch")
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				cancel := ui.Button(c.Key("br-del-cancel"), "Cancel")
				if cancel.Clicked() {
					a.closeDeleteBranchDialog()
				}
				switch {
				case st.delChecking || st.delErr != nil:
					// No confirm button: an unverified branch is never deleted.
				case st.delMerged:
					confirm := ui.Button(c.Key("br-del-confirm"), "Confirm Delete").Disabled(a.session.Busy())
					if confirm.Clicked() {
						a.submitDeleteBranch(false)
					}
				default:
					force := ui.Button(c.Key("br-del-force"), "Force Delete").Disabled(!st.delForceAgreed || a.session.Busy())
					if force.Clicked() && st.delForceAgreed {
						a.submitDeleteBranch(true)
					}
				}
			})
		})
	})
	if !st.delOpen {
		a.closeDeleteBranchDialog()
	}
}

// closeDeleteBranchDialog closes the confirmation dialog and invalidates
// any merged check still in flight so its late result is discarded. The
// answer the dialog established is kept for the delete worker until the
// submission reads it; canceling or switching repositories clears it.
func (a *app) closeDeleteBranchDialog() {
	st := &a.refsView
	if st.delOpen || st.delChecking {
		a.session.cancelRead("refs-delete-check")
	}
	st.delOpen = false
	st.delTarget = ""
	st.delChecking = false
	st.delMerged = false
	st.delErr = nil
	st.delForceAgreed = false
}

// submitDeleteBranch deletes the dialog's target through the session's
// write path. The worker re-verifies on the repository itself that the
// target is not the checked-out branch and, for a non-forcing delete, that
// the branch is still merged: the UI's merged answer can be stale by the
// time the worker runs, so the worker only uses it as the user's intent —
// the repository must agree before anything is deleted.
func (a *app) submitDeleteBranch(force bool) {
	st := &a.refsView
	target := st.delTarget
	a.closeDeleteBranchDialog()
	if target == "" {
		return
	}
	a.session.do("Delete branch "+target, func(r *git.Repo) error {
		// Re-check on the repository: the dialog's answer can be stale by
		// the time the write runs, and a branch is never deleted on a
		// failed or outdated verification.
		merged, err := r.BranchMerged(target)
		if err != nil {
			return err
		}
		return r.SafeDeleteBranch(target, force, merged, nil)
	})
}

// fallbackMinusCircle is the geometric delete icon used on systems without
// SF Symbols, stroked like the other fallback icons.
var fallbackMinusCircle = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="5.5"/><path d="M5.5 8h5"/></svg>`)
