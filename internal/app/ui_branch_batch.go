package app

// Batch operations on the local branches the sidebar multi-selection holds:
// the batch context menu of a multi-selected row, the Delete Branches
// confirmation dialog with its re-verifying worker, and Copy Branch Names.
// Remote branches, tags and batch push are not wired yet: the selection keys
// already carry a reference kind for them.

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// batchDelRow is one branch of the Delete Branches dialog as it was captured
// when the dialog opened, on the UI thread: its name, the commit it pointed
// at, and its upstream when that upstream was live. The worker re-verifies
// the repository against every row before deleting anything.
type batchDelRow struct {
	name     string
	hash     string
	upstream string
	checked  bool
}

// branchBatchMenuItems builds the batch context menu of the local branches
// the selection holds. It runs from the row's ContextMenu build, after the
// right click already narrowed an outside-the-selection click down to its
// row: names is len(branches), every one of them still present in the
// snapshot and ordered as the sidebar shows them.
func (a *app) branchBatchMenuItems(m *ui.Menu, branches []git.Branch) {
	st := &a.refsView
	n := len(branches)
	busy := a.session.Busy()
	state := a.session.Snapshot().Status.State

	hasUpstream := false
	deletable := 0
	for _, br := range branches {
		if br.Upstream != "" && !br.Gone {
			hasUpstream = true
		}
		if !br.Current {
			deletable++
		}
	}

	if hasUpstream {
		if m.Item(fmt.Sprintf("Fast-Forward %d Branches to Their Upstreams", n)).Disabled(busy || state != "").Chosen() {
			a.batchFastForwardBranches(branches, n)
		}
		m.Separator()
	}
	if m.Item(fmt.Sprintf("Delete %d Branches…", deletable)).Disabled(deletable == 0 || busy).Chosen() {
		a.openBatchDeleteDialog(branches)
	}
	m.Separator()
	if m.Item("Copy Branch Names").Chosen() {
		var names []string
		for _, br := range branches {
			names = append(names, br.Name)
		}
		// The menu build has no *ui.Context to write the clipboard with, so
		// the text is recorded and the frame's dialog mount point flushes it,
		// as the single-branch copy does.
		st.batchClipboard = strings.Join(names, "\n")
		st.hasBatchClipboard = true
	}
}

// runBranchBatchAction runs one batch branch write through the same
// lifecycle as the toolbar sync actions: the askpass-enabled repository copy
// of syncRepo, because deleting upstreams may push to a server that asks for
// a password or key passphrase, the worker counting of
// syncWorkerStarted/syncWorkerDone, and the completion dropped when the
// repository moved on. The caller must have closed its dialog first so an
// askpass modal is never stacked on top of it. onSuccess runs on the UI
// thread only when the write finished without an error.
func (a *app) runBranchBatchAction(label string, work func(repo *git.Repo) (string, error), onSuccess func()) bool {
	if a.session.Busy() {
		return false
	}
	st := syncStateFor(a)
	repo, gen := a.syncRepo()
	if repo == nil {
		return false
	}
	st.syncErr = ""
	a.session.ClearActionErr()
	st.syncWorkerStarted()
	if !a.session.doThen(label, func(*git.Repo) error {
		defer st.syncWorkerDone()
		note, err := work(repo)
		return noted(note, err)
	}, func(err error) {
		if dropIfStale(a, gen) {
			return
		}
		if note := syncNote(err); note != "" {
			a.session.ClearActionErr()
			a.toast = note
		}
		if err == nil && onSuccess != nil {
			onSuccess()
		}
	}) {
		st.syncWorkerDone()
		return false
	}
	return true
}

// batchFastForwardBranches fast-forwards the selected branches that are
// behind their upstreams and have nothing of their own, and toasts how many
// of the selected count moved — zero included, since the count answers
// whether anything happened at all. The branches the menu captured only name
// the working set: the worker re-reads the live refs so a branch that moved
// upstream since the snapshot is judged by what is true now, never fast-
// forwarded against a diverged upstream git would refuse anyway.
func (a *app) batchFastForwardBranches(branches []git.Branch, total int) {
	names := map[string]bool{}
	for _, br := range branches {
		names[br.Name] = true
	}
	label := fmt.Sprintf("Fast-forwarding %d branches", total)
	a.runBranchBatchAction(label, func(repo *git.Repo) (string, error) {
		refs, err := repo.Refs()
		if err != nil {
			return "", err
		}
		var live []git.Branch
		for _, br := range refs.Branches {
			if names[br.Name] {
				live = append(live, br)
			}
		}
		moved, err := repo.FastForwardToUpstreams(live)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Fast-forwarded %d of %d branches.", len(moved), total), nil
	}, nil)
}

// openBatchDeleteDialog opens the Delete Branches dialog over the selected
// branches that are not checked out — the current branch can never be
// deleted, so it is not even listed. Every row captures name, commit and
// live upstream now, on the UI thread, and starts checked.
func (a *app) openBatchDeleteDialog(branches []git.Branch) {
	st := &a.refsView
	var rows []batchDelRow
	for _, br := range branches {
		if br.Current {
			continue
		}
		row := batchDelRow{name: br.Name, hash: br.Hash, checked: true}
		if br.Upstream != "" && !br.Gone {
			row.upstream = br.Upstream
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return
	}
	st.batchDelRows = rows
	st.batchDelAlsoRemote = false
	st.batchDelOpen = true
}

// closeBatchDeleteDialog closes the Delete Branches dialog and drops every
// captured row, so a reopened dialog starts from the repository as it is
// then and canceling, dismissing or switching repositories can never delete.
func (a *app) closeBatchDeleteDialog() {
	st := &a.refsView
	st.batchDelOpen = false
	st.batchDelRows = nil
	st.batchDelAlsoRemote = false
}

// batchDeleteBranchCount is how many dialog rows are still checked.
func batchDeleteBranchCount(rows []batchDelRow) int {
	n := 0
	for _, r := range rows {
		if r.checked {
			n++
		}
	}
	return n
}

// batchBranchDialogs mounts the Delete Branches dialog of the batch menu —
// the composition point refsSidebar calls on every frame — and flushes the
// clipboard text Copy Branch Names recorded, which needs a frame's
// *ui.Context the menu build never receives. The flush precedes the dialog
// so a copy chosen on any frame reaches the clipboard in the same frame
// whether or not the dialog is open.
func (a *app) batchBranchDialogs(c *ui.Context) {
	p := a.pal
	st := &a.refsView
	if st.hasBatchClipboard {
		c.WriteClipboard(st.batchClipboard)
		st.batchClipboard = ""
		st.hasBatchClipboard = false
	}
	if !st.batchDelOpen {
		return
	}

	anyUpstream := false
	for _, r := range st.batchDelRows {
		if r.upstream != "" {
			anyUpstream = true
		}
	}

	canSubmit := batchDeleteBranchCount(st.batchDelRows) > 0 && !a.session.Busy() &&
		a.session.Snapshot().Status.State == ""
	dismissed, cancelled, confirmed := dialogFrame(c, p, "batch-delete", &st.batchDelOpen, "Delete Branches", 460, func() {
		ui.Column(c.Key("batch-delete-rows")).FillWidth().Gap(6).Children(func() {
			for i := range st.batchDelRows {
				row := &st.batchDelRows[i]
				ui.Row(c.Key("batch-del-row-" + row.name)).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Checkbox(c.Key("batch-del-"+row.name), &row.checked, row.name)
					if row.upstream != "" {
						ui.Text(c, "→ "+row.upstream).FontSize(12).TextColor(p.muted).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
					}
				})
			}
		})
		if anyUpstream {
			ui.Checkbox(c.Key("batch-del-remote"), &st.batchDelAlsoRemote, "Also delete corresponding remote branches")
		}
		msg := "Commits only these branches have are lost from the log."
		if st.batchDelAlsoRemote {
			msg += " The remote branches are deleted on the server for everyone."
		}
		ui.Text(c, msg).FontSize(12).TextColor(p.muted)
	},
		dialogAction{key: "batch-del-cancel", text: "Cancel", label: "Cancel"},
		dialogAction{key: "batch-del-confirm", text: "Delete", label: "Confirm Delete Branches", disabled: !canSubmit, destructive: true})
	if cancelled {
		a.closeBatchDeleteDialog()
	}
	if confirmed {
		a.submitBatchDeleteBranches()
	}
	if dismissed {
		a.closeBatchDeleteDialog()
	}
}

// batchDelTarget is one branch the Delete Branches dialog submitted: the
// name and the commit it pointed at when the dialog opened, and the upstream
// to delete along when the remote box was ticked — empty otherwise, so an
// unticked box never touches a server.
type batchDelTarget struct {
	name     string
	hash     string
	upstream string
}

// submitBatchDeleteBranches closes the dialog first — an askpass modal of a
// remote deletion must never stack on it — and deletes the checked branches
// through the sync lifecycle. The dialog's rows carry the commits they
// pointed at when the dialog opened; the worker re-verifies all of them
// before touching a single ref.
func (a *app) submitBatchDeleteBranches() {
	st := &a.refsView
	var targets []batchDelTarget
	alsoRemote := st.batchDelAlsoRemote
	for _, r := range st.batchDelRows {
		if !r.checked {
			continue
		}
		up := ""
		if alsoRemote {
			up = r.upstream
		}
		targets = append(targets, batchDelTarget{name: r.name, hash: r.hash, upstream: up})
	}
	a.closeBatchDeleteDialog()
	if len(targets) == 0 {
		return
	}
	a.runBranchBatchAction("Deleting branches", func(repo *git.Repo) (string, error) {
		return "", batchDeleteWithGuard(repo, targets)
	}, func() {
		// The branches the deletion moved past can no longer be acted on,
		// and the ones left unchecked answer for themselves: nothing of the
		// selection survives the delete.
		st.pick = nil
		st.pickAnchor = sidebarPickKey{}
	})
}

// batchDeleteWithGuard re-validates the repository against what the Delete
// Branches dialog captured before it deletes: no Git operation may be in
// progress, and every checked branch must still exist, still point at the
// commit the dialog saw, and still not be the checked-out branch. Any check
// failing refuses the whole batch without touching a single ref — deleting
// some of the branches would lose commits the user never saw listed.
func batchDeleteWithGuard(repo *git.Repo, targets []batchDelTarget) error {
	if len(targets) == 0 {
		return fmt.Errorf("cannot delete branches: no branch is checked")
	}
	st, err := repo.Status()
	if err != nil {
		return fmt.Errorf("cannot delete branches: reading the repository state failed: %w", err)
	}
	if st.State != "" {
		return fmt.Errorf("cannot delete branches: a %s is in progress; finish or abort it first", st.State)
	}
	refs, err := repo.Refs()
	if err != nil {
		return fmt.Errorf("cannot delete branches: reading repository references failed: %w", err)
	}
	list := make([]git.RemoveBranchOptions, 0, len(targets))
	for _, t := range targets {
		cur := findLocalBranch(refs, t.name)
		switch {
		case cur == nil:
			return fmt.Errorf("cannot delete branches: %s changed after the dialog opened; review the repository and try again", t.name)
		case cur.Hash != t.hash:
			return fmt.Errorf("cannot delete branches: %s changed after the dialog opened; review the repository and try again", t.name)
		case cur.Current:
			return fmt.Errorf("cannot delete branches: %s changed after the dialog opened; review the repository and try again", t.name)
		}
		list = append(list, git.RemoveBranchOptions{Name: t.name, Upstream: t.upstream})
	}
	return repo.RemoveBranches(list)
}
