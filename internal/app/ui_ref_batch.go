package app

// Batch operations on the remote branches and tags the sidebar multi-
// selection holds: the batch context menus of a multi-selected row, the
// Delete Remote Branches, Push Tags and Delete Tags confirmation dialogs
// with their re-verifying workers, and Copy Branch Names / Copy Tag Names.
// All three dialogs share the batch lifecycle of the local branches: every
// row's reference and the commit it pointed at is captured on the UI thread
// when the dialog opens, the submit runs through the askpass-enabled sync
// lifecycle, and the worker re-verifies every captured commit against the
// repository before touching a single reference.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// refBatchRow is one row of the batch dialogs as it was captured when the
// dialog opened, on the UI thread: the selection key the row came from, the
// name the dialog lists, the commit the reference pointed at then — the
// peeled commit for tags — and for remote branches the remote and branch
// names the deletion sends. The worker re-verifies the repository against
// every row before pushing or deleting anything.
type refBatchRow struct {
	key     sidebarPickKey
	name    string
	hash    string
	remote  string
	branch  string
	checked bool
}

// refBatchRows captures the dialog rows of refs in the order given, every
// one checked: the name, the commit the reference points at now, and for
// remote branches the remote and branch parts.
func refBatchRows(refs []git.Branch) []refBatchRow {
	rows := make([]refBatchRow, 0, len(refs))
	for _, rb := range refs {
		_, branch, _ := strings.Cut(rb.Name, "/")
		rows = append(rows, refBatchRow{
			key:     remoteBranchPickKey(rb.FullName),
			name:    rb.Name,
			hash:    rb.Hash,
			remote:  rb.Remote,
			branch:  branch,
			checked: true,
		})
	}
	return rows
}

// refBatchTagRows captures the dialog rows of tags in the order given: the
// name and the commit the tag peels to now, which is what the push and
// delete workers re-verify.
func refBatchTagRows(tags []git.Tag) []refBatchRow {
	rows := make([]refBatchRow, 0, len(tags))
	for _, tag := range tags {
		rows = append(rows, refBatchRow{
			key:     tagPickKey(tag.Name),
			name:    tag.Name,
			hash:    tag.Hash,
			checked: true,
		})
	}
	return rows
}

// refBatchCheckedCount is how many dialog rows are still checked.
func refBatchCheckedCount(rows []refBatchRow) int {
	n := 0
	for _, r := range rows {
		if r.checked {
			n++
		}
	}
	return n
}

// refBatchChecked returns the rows still checked, so the submit never reads
// an unchecked one.
func refBatchChecked(rows []refBatchRow) []refBatchRow {
	var out []refBatchRow
	for _, r := range rows {
		if r.checked {
			out = append(out, r)
		}
	}
	return out
}

// batchCopy writes names to the clipboard through the frame's flush: the
// menu build has no *ui.Context to write it with, so the text is recorded
// and the dialog mount point flushes it, as the single-branch copy does.
func (a *app) batchCopy(names []string) {
	st := &a.refsView
	st.batchClipboard = strings.Join(names, "\n")
	st.hasBatchClipboard = true
}

// --- The remote branch batch menu ---

// remoteBranchBatchMenuItems builds the batch context menu of the remote
// branches the selection holds, replacing the row's own menu entirely. It
// runs from the row's ContextMenu build, after the right click already
// narrowed an outside-the-selection click down to its row: branches are the
// selected rows still present in the snapshot, ordered as the sidebar shows
// them.
func (a *app) remoteBranchBatchMenuItems(m *ui.Menu, branches []git.Branch) {
	n := len(branches)
	busy := a.session.Busy()
	state := a.session.Snapshot().Status.State

	if m.Item(fmt.Sprintf("Delete %d Remote Branches…", n)).Disabled(busy || state != "").Chosen() {
		a.openRemoteBatchDeleteDialog(branches)
	}
	m.Separator()
	if m.Item("Copy Branch Names").Chosen() {
		var names []string
		for _, rb := range branches {
			names = append(names, rb.Name)
		}
		a.batchCopy(names)
	}
}

// --- The tag batch menu ---

// tagBatchMenuItems builds the batch context menu of the tags the selection
// holds. Push goes to the one remote directly, or into a submenu of the
// remotes when there are several; without a remote there is nothing to push
// to and the item does not exist. tags are the selected rows still present
// in the snapshot, ordered as the sidebar shows them.
func (a *app) tagBatchMenuItems(m *ui.Menu, tags []git.Tag) {
	n := len(tags)
	busy := a.session.Busy()
	state := a.session.Snapshot().Status.State
	disabled := busy || state != ""
	remotes := a.session.Snapshot().Refs.Remotes

	switch len(remotes) {
	case 0:
	case 1:
		if m.Item(fmt.Sprintf("Push %d Tags to '%s'…", n, remotes[0])).Disabled(disabled).Chosen() {
			a.openTagBatchPushDialog(remotes[0], tags)
		}
	default:
		m.Submenu(fmt.Sprintf("Push %d Tags to", n), func(sub *ui.Menu) {
			for _, remote := range remotes {
				if sub.Item(remote + "…").Disabled(disabled).Chosen() {
					a.openTagBatchPushDialog(remote, tags)
				}
			}
		})
	}
	if m.Item(fmt.Sprintf("Delete %d Tags…", n)).Disabled(disabled).Chosen() {
		a.openTagBatchDeleteDialog(tags)
	}
	m.Separator()
	if m.Item("Copy Tag Names").Chosen() {
		var names []string
		for _, tag := range tags {
			names = append(names, tag.Name)
		}
		a.batchCopy(names)
	}
}

// --- Delete Remote Branches ---

// openRemoteBatchDeleteDialog opens the Delete Remote Branches dialog over
// the selected remote branches, capturing on the UI thread the commit every
// tracking reference points at: the lease the worker re-verifies and deletes
// against.
func (a *app) openRemoteBatchDeleteDialog(branches []git.Branch) {
	rows := refBatchRows(branches)
	if len(rows) == 0 {
		return
	}
	st := &a.refsView
	st.rbdRows = rows
	st.rbdOpen = true
}

// closeRemoteBatchDeleteDialog closes the dialog and drops every captured
// row, so a reopened dialog starts from the repository as it is then and
// canceling, dismissing or switching repositories can never delete.
func (a *app) closeRemoteBatchDeleteDialog() {
	st := &a.refsView
	st.rbdOpen = false
	st.rbdRows = nil
}

// submitRemoteBatchDelete closes the dialog first — an askpass modal of the
// deletion must never stack on it — and deletes the checked remote branches
// through the sync lifecycle.
func (a *app) submitRemoteBatchDelete() {
	st := &a.refsView
	targets := refBatchChecked(st.rbdRows)
	a.closeRemoteBatchDeleteDialog()
	if len(targets) == 0 {
		return
	}
	a.runBranchBatchAction(fmt.Sprintf("Deleting %d remote branches", len(targets)), func(repo *git.Repo) (string, error) {
		return "", batchDeleteRemoteBranches(repo, targets)
	}, nil)
}

// batchDeleteRemoteBranches re-verifies every checked remote branch before
// deleting anything: its tracking reference must still exist and still point
// at the commit the dialog captured. Any check failing refuses the whole
// batch without touching the server — deleting some of the branches would
// lose commits the user never saw listed. The verification is exactly what
// the per-branch delete then re-checks on its own, so a branch moving
// between the two still cannot be deleted: the lease on the verified commit
// keeps every branch someone moved on the server.
func batchDeleteRemoteBranches(repo *git.Repo, rows []refBatchRow) error {
	if len(rows) == 0 {
		return fmt.Errorf("cannot delete remote branches: no branch is checked")
	}
	for _, r := range rows {
		fullRef := "refs/remotes/" + r.remote + "/" + r.branch
		gotOID, err := repo.PathHash(fullRef + "^{commit}")
		if err != nil || gotOID != r.hash {
			return fmt.Errorf("cannot delete remote branches: %s/%s changed after the dialog opened; review the repository and try again", r.remote, r.branch)
		}
	}
	var errs []error
	for _, r := range rows {
		if err := deleteRemoteBranchWithGuard(repo, r.remote, r.branch, r.hash); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// --- Push Tags ---

// openTagBatchPushDialog opens the Push Tags dialog for the menu's remote
// over the selected tags, capturing on the UI thread the commit every tag
// peels to: what the worker re-verifies before pushing anything.
func (a *app) openTagBatchPushDialog(remote string, tags []git.Tag) {
	rows := refBatchTagRows(tags)
	if len(rows) == 0 {
		return
	}
	st := &a.refsView
	st.ptRows = rows
	st.ptRemote = remote
	st.ptOpen = true
}

// closeTagBatchPushDialog closes the dialog and drops every captured row,
// so canceling, dismissing or switching repositories can never push.
func (a *app) closeTagBatchPushDialog() {
	st := &a.refsView
	st.ptOpen = false
	st.ptRows = nil
	st.ptRemote = ""
}

// submitTagBatchPush closes the dialog first and pushes the checked tags to
// the dialog's remote through the sync lifecycle.
func (a *app) submitTagBatchPush() {
	st := &a.refsView
	targets := refBatchChecked(st.ptRows)
	remote := st.ptRemote
	a.closeTagBatchPushDialog()
	if len(targets) == 0 || remote == "" {
		return
	}
	a.runBranchBatchAction(fmt.Sprintf("Pushing %d tags", len(targets)), func(repo *git.Repo) (string, error) {
		if err := batchVerifyTagCommits(repo, "cannot push tags", targets); err != nil {
			return "", err
		}
		var refs []string
		for _, r := range targets {
			// The full reference, never the bare name: a tag sharing its name
			// with a branch — release/1.0 tagging the release/1.0 line — would
			// otherwise push ambiguously or not at all.
			refs = append(refs, "refs/tags/"+r.name)
		}
		if err := repo.PushRefs(remote, refs); err != nil {
			return "", errors.New(git.RedactText(err.Error()))
		}
		return fmt.Sprintf("Pushed %d tags to %s.", len(targets), remote), nil
	}, nil)
}

// --- Delete Tags ---

// openTagBatchDeleteDialog opens the Delete Tags dialog over the selected
// tags, the remote box unticked as it always starts.
func (a *app) openTagBatchDeleteDialog(tags []git.Tag) {
	rows := refBatchTagRows(tags)
	if len(rows) == 0 {
		return
	}
	st := &a.refsView
	st.tdRows = rows
	st.tdRemote = false
	st.tdOpen = true
}

// closeTagBatchDeleteDialog closes the dialog and drops every captured row,
// so canceling, dismissing or switching repositories can never delete.
func (a *app) closeTagBatchDeleteDialog() {
	st := &a.refsView
	st.tdOpen = false
	st.tdRows = nil
	st.tdRemote = false
}

// submitTagBatchDelete closes the dialog first and deletes the checked tags
// through the sync lifecycle: locally, and from every remote when the remote
// box was ticked. The deletion moving past the selected rows empties the
// selection afterwards.
func (a *app) submitTagBatchDelete() {
	st := &a.refsView
	targets := refBatchChecked(st.tdRows)
	var remotes []string
	if st.tdRemote {
		remotes = append([]string(nil), a.session.Snapshot().Refs.Remotes...)
	}
	a.closeTagBatchDeleteDialog()
	if len(targets) == 0 {
		return
	}
	a.runBranchBatchAction(fmt.Sprintf("Deleting %d tags", len(targets)), func(repo *git.Repo) (string, error) {
		if err := batchVerifyTagCommits(repo, "cannot delete tags", targets); err != nil {
			return "", err
		}
		var names []string
		for _, r := range targets {
			names = append(names, r.name)
		}
		if err := repo.DeleteTags(names, remotes); err != nil {
			return "", errors.New(git.RedactText(err.Error()))
		}
		return "", nil
	}, func() {
		// The deleted rows can no longer be acted on, and the ones left
		// unchecked answer for themselves: nothing of the selection survives
		// the delete.
		st.pick = nil
		st.pickAnchor = sidebarPickKey{}
	})
}

// batchVerifyTagCommits re-reads the commit every checked tag peels to and
// refuses the whole batch — without pushing or deleting anything — when one
// no longer matches what the dialog captured. The comparison is the peeled
// commit, matching the capture: a tag recreated onto the very same commit
// still names it, while one moved anywhere else is a tag the user never saw
// listed. prefix is the operation's refusal lead, such as cannot push tags.
func batchVerifyTagCommits(repo *git.Repo, prefix string, rows []refBatchRow) error {
	if len(rows) == 0 {
		return fmt.Errorf("%s: no tag is checked", prefix)
	}
	for _, r := range rows {
		gotOID, err := repo.PathHash("refs/tags/" + r.name + "^{commit}")
		if err != nil || gotOID != r.hash {
			return fmt.Errorf("%s: %s changed after the dialog opened; review the repository and try again", prefix, r.name)
		}
	}
	return nil
}

// --- The dialogs ---

// refBatchDialogs mounts the Delete Remote Branches, Push Tags and Delete
// Tags dialogs of the batch menus — the composition point refsSidebar calls
// on every frame right after the local branches' batch dialog, whose flush
// already serves the clipboard text the copy items of these menus record.
func (a *app) refBatchDialogs(c *ui.Context) {
	a.remoteBatchDeleteDialog(c)
	a.tagBatchPushDialog(c)
	a.tagBatchDeleteDialog(c)
}

// refBatchDialogRows renders the checkbox rows of one batch dialog, keyed by
// the dialog's name so the same reference in two dialogs never collides.
func refBatchDialogRows(c *ui.Context, key string, rows []refBatchRow) {
	ui.Column(c.Key(key + "-rows")).FillWidth().Gap(6).Children(func() {
		for i := range rows {
			row := &rows[i]
			ui.Row(c.Key(key + "-row-" + row.key.name)).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Checkbox(c.Key(key+"-"+row.key.name), &row.checked, row.name)
			})
		}
	})
}

// refBatchSubmitReady reports whether a batch dialog's confirm button can
// submit: at least one row checked, no write or sync in flight, and no
// merge, rebase, cherry-pick or revert in progress.
func (a *app) refBatchSubmitReady(rows []refBatchRow) bool {
	return refBatchCheckedCount(rows) > 0 && !a.session.Busy() && a.session.Snapshot().Status.State == ""
}

// remoteBatchDeleteDialog is the Delete Remote Branches modal: every
// selected remote branch with a checkbox, all checked, and the warning that
// the deletion lands on the server for everyone. Canceling, dismissing or
// switching repositories never runs a git command.
func (a *app) remoteBatchDeleteDialog(c *ui.Context) {
	p := a.pal
	st := &a.refsView
	if !st.rbdOpen {
		return
	}
	if ui.Modal(c.Key("rbd-modal"), &st.rbdOpen, func() {
		ui.Column(c.Key("rbd-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Delete Remote Branches").FontSize(17).FontWeight(650).TextColor(p.text)
			refBatchDialogRows(c, "rbd", st.rbdRows)
			ui.Text(c, "The branches are deleted on the server for everyone.").FontSize(12).TextColor(p.muted)
			ui.Row(c.Key("rbd-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("rbd-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeRemoteBatchDeleteDialog()
				}
				if ui.PrimaryButton(c.Key("rbd-confirm"), "Delete").
					Label("Confirm Delete Remote Branches").
					Disabled(!a.refBatchSubmitReady(st.rbdRows)).
					Clicked() {
					a.submitRemoteBatchDelete()
				}
			})
		})
	}).Dismissed() {
		a.closeRemoteBatchDeleteDialog()
	}
}

// tagBatchPushDialog is the Push Tags modal: the remote the menu chose,
// every selected tag with a checkbox, all checked, and the note that a
// diverged remote tag is rejected rather than overwritten. Canceling,
// dismissing or switching repositories never runs a git command.
func (a *app) tagBatchPushDialog(c *ui.Context) {
	p := a.pal
	st := &a.refsView
	if !st.ptOpen {
		return
	}
	if ui.Modal(c.Key("pt-modal"), &st.ptOpen, func() {
		ui.Column(c.Key("pt-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Push Tags").FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Field(c, "Remote:", func() {
				ui.Text(c, st.ptRemote).Font("monospace").FontSize(12).TextColor(p.text)
			})
			refBatchDialogRows(c, "pt", st.ptRows)
			ui.Text(c, "Tags that already exist on the remote with a different commit are rejected.").FontSize(12).TextColor(p.muted)
			ui.Row(c.Key("pt-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("pt-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeTagBatchPushDialog()
				}
				if ui.PrimaryButton(c.Key("pt-confirm"), "Push").
					Label("Confirm Push Tags").
					Disabled(!a.refBatchSubmitReady(st.ptRows)).
					Clicked() {
					a.submitTagBatchPush()
				}
			})
		})
	}).Dismissed() {
		a.closeTagBatchPushDialog()
	}
}

// tagBatchDeleteDialog is the Delete Tags modal: every selected tag with a
// checkbox, all checked, and — while the repository has remotes — the box
// that extends the deletion to every remote. Canceling, dismissing or
// switching repositories never runs a git command.
func (a *app) tagBatchDeleteDialog(c *ui.Context) {
	p := a.pal
	st := &a.refsView
	if !st.tdOpen {
		return
	}
	remotes := a.session.Snapshot().Refs.Remotes
	if ui.Modal(c.Key("td-modal"), &st.tdOpen, func() {
		ui.Column(c.Key("td-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Delete Tags").FontSize(17).FontWeight(650).TextColor(p.text)
			refBatchDialogRows(c, "td", st.tdRows)
			if len(remotes) > 0 {
				ui.Checkbox(c.Key("td-remote"), &st.tdRemote, "Also delete from remotes")
			}
			if st.tdRemote {
				ui.Text(c, "The tags are deleted on "+strings.Join(remotes, ", ")+" for everyone. Remotes without them are skipped.").FontSize(12).TextColor(p.muted)
			}
			ui.Row(c.Key("td-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("td-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeTagBatchDeleteDialog()
				}
				if ui.PrimaryButton(c.Key("td-confirm"), "Delete").
					Label("Confirm Delete Tags").
					Disabled(!a.refBatchSubmitReady(st.tdRows)).
					Clicked() {
					a.submitTagBatchDelete()
				}
			})
		})
	}).Dismissed() {
		a.closeTagBatchDeleteDialog()
	}
}
