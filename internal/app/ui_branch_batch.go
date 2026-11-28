package app

// Batch operations on the local branches the sidebar multi-selection holds:
// the batch context menu of a multi-selected row, the Delete Branches
// confirmation dialog with its re-verifying worker, and Copy Branch Names.
// Remote branches, tags and batch push are not wired yet: the selection keys
// already carry a reference kind for them.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// batchDelRow is one branch of the Delete Branches dialog as it was captured
// when the dialog opened, on the UI thread: its name, the commit it pointed
// at, and its upstream when that upstream was live. A live upstream carries
// the remote and branch names the deletion sends and the commit its tracking
// reference pointed at — the lease the remote deletion is confirmed against.
// The worker re-verifies the repository against every row before deleting
// anything.
type batchDelRow struct {
	name     string
	hash     string
	upstream string
	upRemote string
	upBranch string
	upHash   string
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
// live upstream now, on the UI thread, and starts checked. A live upstream
// also captures its remote and branch names and the commit its tracking
// reference points at, read from the snapshot's remote branches: an upstream
// the snapshot has no tracking reference for is treated as gone and records
// nothing.
func (a *app) openBatchDeleteDialog(branches []git.Branch) {
	st := &a.refsView
	snap := a.session.Snapshot()
	var rows []batchDelRow
	for _, br := range branches {
		if br.Current {
			continue
		}
		row := batchDelRow{name: br.Name, hash: br.Hash, checked: true}
		if br.Upstream != "" && !br.Gone {
			if rb := findRemoteBranch(snap.Refs, br.Upstream); rb != nil {
				row.upstream = br.Upstream
				row.upRemote = rb.Remote
				row.upBranch = strings.TrimPrefix(rb.Name, rb.Remote+"/")
				row.upHash = rb.Hash
			}
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

// findRemoteBranch returns the snapshot's remote-tracking branch whose short
// name is upstream, such as origin/feature, or nil when the snapshot has
// none — the tracking reference is then treated as gone.
func findRemoteBranch(refs git.Refs, upstream string) *git.Branch {
	for i := range refs.Remote {
		if refs.Remote[i].Name == upstream {
			return &refs.Remote[i]
		}
	}
	return nil
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
// name and the commit it pointed at when the dialog opened, and — when the
// remote box was ticked — the upstream to delete along, with the remote and
// branch names the deletion sends and the commit its tracking reference
// pointed at. An unticked box leaves every upstream field empty, so it never
// touches a server.
type batchDelTarget struct {
	name     string
	hash     string
	upstream string
	upRemote string
	upBranch string
	upHash   string
}

// submitBatchDeleteBranches closes the dialog first — an askpass modal of a
// remote deletion must never stack on it — and deletes the checked branches
// through the sync lifecycle. The dialog's rows carry the commits they and
// their upstreams' tracking references pointed at when the dialog opened;
// the worker re-verifies all of them before touching a single ref.
func (a *app) submitBatchDeleteBranches() {
	st := &a.refsView
	var targets []batchDelTarget
	alsoRemote := st.batchDelAlsoRemote
	for _, r := range st.batchDelRows {
		if !r.checked {
			continue
		}
		t := batchDelTarget{name: r.name, hash: r.hash}
		if alsoRemote {
			t.upstream = r.upstream
			t.upRemote = r.upRemote
			t.upBranch = r.upBranch
			t.upHash = r.upHash
		}
		targets = append(targets, t)
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
// progress, every checked branch must still exist, still point at the commit
// the dialog saw, and still not be the checked-out branch, and every
// upstream the remote box covers must still have its tracking reference
// pointing at the commit the dialog saw. Any check failing refuses the
// whole batch without touching a single ref — deleting some of the branches
// would lose commits the user never saw listed. Only then does the deletion
// run: the local branches first, then each upstream on its server through
// the same lease-protected delete the remote branches' own dialogs use,
// every step tried and the errors joined — exactly the order the deletion
// had before, so a failing local branch never stops the rest of the batch.
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
		list = append(list, git.RemoveBranchOptions{Name: t.name})
	}
	for _, t := range targets {
		if t.upstream == "" {
			continue
		}
		gotOID, err := repo.PathHash("refs/remotes/" + t.upstream + "^{commit}")
		if err != nil || gotOID != t.upHash {
			return fmt.Errorf("cannot delete branches: the remote branch %s changed after the dialog opened; review the repository and try again", t.upstream)
		}
	}
	errs := []error{repo.RemoveBranches(list)}
	// Two local branches can share one upstream: the server deletion goes
	// out once, or the second push would fail the lease the first consumed.
	deleted := map[string]bool{}
	for _, t := range targets {
		if t.upstream == "" || deleted[t.upstream] {
			continue
		}
		deleted[t.upstream] = true
		if err := deleteRemoteBranchWithGuard(repo, t.upRemote, t.upBranch, t.upHash); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
