package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// tagsMenuUIState is the per-app state of the tag context menu items this
// file owns: the Checkout Commit confirmation with the repository state it
// was confirmed against, the read-only Tag Details panel with its
// asynchronous read, the Delete from Remote dialog with the probe its confirm
// button is gated on, and the clipboard text a copy menu item asked for. A
// repository switch resets it and invalidates a read still in flight so its
// late answer can never apply to the new repository.
type tagsMenuUIState struct {
	repoGen uint64

	// coOpen is the Checkout Commit confirmation. The worker refuses the
	// write when the tag or HEAD no longer is exactly what these captured
	// values describe, even if a background reload updated the snapshot
	// while the dialog was open.
	coOpen           bool
	coTag            string
	coCommit         string
	coExpectedHead   string
	coExpectedBranch string

	// detOpen is the Tag Details panel. detChecking is set while the
	// background read runs; detInfo and detSubject hold its answer, detErr a
	// failed read's error verbatim.
	detOpen     bool
	detTag      string
	detChecking bool
	detInfo     git.TagRefInfo
	detSubject  string
	detErr      error

	// rdOpen is the Delete from Remote dialog. rdChecking is set while the
	// ls-remote probe runs. The confirm button exists only with the probe
	// answered successfully and the tag present: rdErr holds a failed
	// probe's error verbatim, rdOID and rdExists its answer.
	rdOpen     bool
	rdTag      string
	rdRemote   string
	rdChecking bool
	rdErr      error
	rdOID      string
	rdExists   bool

	// A menu item's Chosen runs inside the menu's build, which receives no
	// *ui.Context — and mygo's clipboard is only writable with one, on the
	// main thread, in the view or an input handler. The copy choice records
	// the text here, and tagExtraDialogs, the one mount point of this file
	// every frame passes, writes it with the frame's own context.
	tagClipboard    string
	hasTagClipboard bool
}

// tagsMenuUI returns the app's tag menu state, creating it on first use and
// resetting it — including canceling a details read or remote probe in
// flight — when the repository generation moves on. It runs on the UI
// thread.
func (a *app) tagsMenuUI() *tagsMenuUIState {
	st := featureState(a, func() *tagsMenuUIState { return &tagsMenuUIState{} })
	if gen := a.session.Generation(); st.repoGen != gen {
		a.session.cancelRead("tags-details")
		a.session.cancelRead("tags-remote-del-check")
		*st = tagsMenuUIState{repoGen: gen}
	}
	return st
}

// tagMenuHeadItems adds the Checkout Commit and Show Tag Details items at the
// head of a tag row's context menu, ahead of the push item. Checking out is
// a write, so it disables while a repository write or another operation is
// in progress — moving HEAD then would corrupt it — and while HEAD already
// is detached at exactly the commit the tag names, where the checkout would
// be a no-op. Showing details is a read and never disables.
func (a *app) tagMenuHeadItems(m *ui.Menu, tag git.Tag) {
	snap := a.session.Snapshot()
	detachedThere := snap.Status.Branch == "" && snap.Status.Head == tag.Hash
	disabled := a.session.Busy() || snap.Status.State != "" || detachedThere
	if m.Item("Checkout Commit…").Disabled(disabled).Chosen() {
		a.openTagCheckoutDialog(tag, snap.Status)
	}
	if m.Item("Show Tag Details…").Chosen() {
		a.openTagDetailsDialog(tag.Name)
	}
	m.Separator()
}

// tagMenuTailItems adds the remote-delete and copy items at the tail of a tag
// row's context menu, behind the local delete item. Deleting from a remote is
// a write needing somewhere to delete from; copying never disables: it reads
// the name the menu was opened on, nothing of the repository's moving state.
func (a *app) tagMenuTailItems(m *ui.Menu, tag git.Tag) {
	st := a.tagsMenuUI()
	remoteDisabled := a.session.Busy() || len(a.session.Snapshot().Refs.Remotes) == 0
	if m.Item("Delete from Remote…").Disabled(remoteDisabled).Chosen() {
		a.openTagRemoteDeleteDialog(tag.Name)
	}
	m.Separator()
	if m.Item("Copy Tag Name").Chosen() {
		st.tagClipboard = tag.Name
		st.hasTagClipboard = true
	}
}

// openTagCheckoutDialog opens the Checkout Commit confirmation for the tag.
// Everything the dialog or its worker needs later is captured now, on the UI
// thread: the tag's name and the commit it names, and the HEAD hash and
// branch the checkout must still find when it runs.
func (a *app) openTagCheckoutDialog(tag git.Tag, status git.Status) {
	st := a.tagsMenuUI()
	st.coOpen = true
	st.coTag = tag.Name
	st.coCommit = tag.Hash
	st.coExpectedHead = status.Head
	st.coExpectedBranch = status.Branch
}

func (a *app) closeTagCheckoutDialog() {
	st := a.tagsMenuUI()
	st.coOpen = false
	st.coTag = ""
	st.coCommit = ""
	st.coExpectedHead = ""
	st.coExpectedBranch = ""
}

// tagCheckoutDialog is the confirmation detaching HEAD at the commit a tag
// names. The checkout never forces: local changes that would be overwritten
// stop it inside git, with the working tree untouched.
func (a *app) tagCheckoutDialog(c *ui.Context) {
	p := a.pal
	st := a.tagsMenuUI()
	if !st.coOpen {
		return
	}
	dismissed, cancelled, confirmed := dialogFrame(c, p, "tag-checkout-modal", &st.coOpen, "Checkout Commit", 460, func() {
		ui.Field(c, "Tag:", func() {
			ui.Text(c, st.coTag).Font("monospace").FontSize(12).TextColor(p.text)
		})
		ui.Text(c, "HEAD will be detached at "+shortTagHash(st.coCommit)).FontSize(12.5).TextColor(p.muted).SingleLine()
	},
		dialogAction{key: "tag-checkout-cancel", text: "Cancel", label: "Cancel"},
		dialogAction{key: "tag-checkout-confirm", text: "Checkout", label: "Confirm Checkout Tag Commit", disabled: a.session.Busy(), autoFocus: true})
	if cancelled {
		a.closeTagCheckoutDialog()
	}
	if confirmed {
		a.submitTagCheckout()
	}
	if dismissed {
		a.closeTagCheckoutDialog()
	}
}

// submitTagCheckout runs the checkout the dialog describes through the
// session's write path. The tag name, the commit it named, HEAD and the
// branch captured when the dialog opened are passed by value to the
// background worker, which re-reads the repository and refuses the write when
// what it finds differs.
func (a *app) submitTagCheckout() {
	st := a.tagsMenuUI()
	if a.session.Busy() {
		return
	}
	tag := st.coTag
	expectedCommit := st.coCommit
	expectedHead := st.coExpectedHead
	expectedBranch := st.coExpectedBranch
	a.closeTagCheckoutDialog()
	if tag == "" || expectedCommit == "" {
		return
	}
	a.checkoutOfferingStash("Checking out tag "+tag, tag, func(repo *git.Repo, force bool) error {
		return checkoutTagWithGuard(repo, tag, expectedCommit, expectedHead, expectedBranch, force)
	})
}

// checkoutTagWithGuard re-validates the repository against what the Checkout
// Commit confirmation captured before it detaches HEAD:
//
//   - the tag must still verify, still exist, and still name the exact commit
//     the user confirmed — a tag deleted or retargeted since the dialog
//     opened is a different target and never checked out;
//   - the status must still be readable, no merge, rebase, cherry-pick or
//     revert may be in progress, and HEAD and the branch must still be
//     exactly where the confirmation saw them.
//
// Only then does the checkout run — plain, so Git itself refuses when local
// changes would be overwritten, without touching the working tree, the index
// or HEAD. The checks run on every attempt, including the retry a blocked
// checkout offers: force is the retry's choice alone and decides whether git
// may discard what is in the way.
func checkoutTagWithGuard(repo *git.Repo, tagName, expectedCommitOID, expectedHead, expectedBranch string, force bool) error {
	info, err := repo.TagRefInfo(tagName)
	if err != nil {
		return fmt.Errorf("cannot check out: verifying tag %s failed: %w", tagName, err)
	}
	if info.CommitOID != expectedCommitOID {
		return fmt.Errorf("cannot check out: tag %s moved to another commit after the dialog opened; review the repository and try again", tagName)
	}
	st, err := repo.Status()
	if err != nil {
		return fmt.Errorf("cannot check out: reading the repository state failed: %w", err)
	}
	if st.State != "" {
		return fmt.Errorf("cannot check out: a %s is in progress; finish or abort it first", st.State)
	}
	if st.Head != expectedHead || st.Branch != expectedBranch {
		return fmt.Errorf("cannot check out: HEAD or the current branch changed after the dialog opened; review the repository and try again")
	}
	if force {
		return repo.CheckoutForce(info.CommitOID)
	}
	return repo.Checkout(info.CommitOID)
}

// openTagDetailsDialog opens the read-only Tag Details panel for the tag and
// starts the asynchronous read of its exact state and target commit subject
// on the repository itself.
func (a *app) openTagDetailsDialog(name string) {
	st := a.tagsMenuUI()
	st.detOpen = true
	st.detTag = name
	st.detChecking = true
	st.detInfo = git.TagRefInfo{}
	st.detSubject = ""
	st.detErr = nil
	target := name
	sessionRead(a.session, "tags-details", func(r *git.Repo) (tagDetails, error) {
		info, err := r.TagRefInfo(target)
		if err != nil {
			return tagDetails{}, err
		}
		subject := ""
		if detail, err := r.Detail(info.CommitOID); err == nil {
			subject = detail.Subject
		}
		return tagDetails{info: info, subject: subject}, nil
	}, func(details tagDetails, err error) {
		if !st.detOpen || st.detTag != target {
			return
		}
		st.detChecking = false
		st.detInfo = details.info
		st.detSubject = details.subject
		st.detErr = err
	})
}

// tagDetails is the answer of the Tag Details read: the verified state of the
// tag and the subject of the commit it names.
type tagDetails struct {
	info    git.TagRefInfo
	subject string
}

func (a *app) closeTagDetailsDialog() {
	st := a.tagsMenuUI()
	if st.detOpen || st.detChecking {
		a.session.cancelRead("tags-details")
	}
	st.detOpen = false
	st.detTag = ""
	st.detChecking = false
	st.detInfo = git.TagRefInfo{}
	st.detSubject = ""
	st.detErr = nil
}

// tagDetailsDialog is the read-only panel of one tag: its kind, the exact
// commit it names with that commit's subject, and for an annotated tag the
// tagger, the date and the full message, lines and all.
func (a *app) tagDetailsDialog(c *ui.Context) {
	p := a.pal
	st := a.tagsMenuUI()
	if !st.detOpen {
		return
	}
	if ui.Modal(c.Key("tag-details-modal"), &st.detOpen, func() {
		ui.Column(c.Key("tag-details-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Tag Details").FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Field(c, "Tag:", func() {
				ui.Text(c, st.detTag).Font("monospace").FontSize(12).TextColor(p.text)
			})
			switch {
			case st.detChecking:
				ui.Text(c, "Loading tag details…").FontSize(12).TextColor(p.muted)
			case st.detErr != nil:
				ui.Text(c, st.detErr.Error()).FontSize(12).TextColor(p.danger)
			default:
				info := st.detInfo
				kind := "Lightweight tag"
				if info.Annotated {
					kind = "Annotated tag"
				}
				ui.Field(c, "Type:", func() {
					ui.Text(c, kind).FontSize(12).TextColor(p.text)
				})
				commitLine := shortTagHash(info.CommitOID)
				if st.detSubject != "" {
					commitLine += " " + st.detSubject
				}
				ui.Field(c, "Commit:", func() {
					ui.Text(c, commitLine).FontSize(12).TextColor(p.text).SingleLine().Ellipsis("…")
				})
				ui.Text(c, info.CommitOID).Font("monospace").FontSize(11).TextColor(p.faint).SingleLine()
				if info.Annotated {
					ui.Field(c, "Tagger:", func() {
						ui.Text(c, info.Tagger).FontSize(12).TextColor(p.text)
					})
					ui.Field(c, "Date:", func() {
						ui.Text(c, info.Date).FontSize(12).TextColor(p.text)
					})
					if strings.TrimSpace(info.Message) != "" {
						ui.Field(c, "Message:", func() {
							ui.Text(c, info.Message).FontSize(12).TextColor(p.text)
						})
					}
				}
			}
			ui.Row(c.Key("tag-details-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("tag-details-close"), "Close").Label("Close Tag Details").Clicked() {
					a.closeTagDetailsDialog()
				}
			})
		})
	}).Dismissed() {
		a.closeTagDetailsDialog()
	}
}

// openTagRemoteDeleteDialog opens the Delete from Remote dialog for the tag,
// defaulting the remote to the repository's default, and starts the
// asynchronous ls-remote probe its confirm button is gated on.
func (a *app) openTagRemoteDeleteDialog(name string) {
	refs := a.session.Snapshot().Refs
	if len(refs.Remotes) == 0 {
		return
	}
	st := a.tagsMenuUI()
	st.rdOpen = true
	st.rdTag = name
	st.rdRemote = defaultRemoteOf(refs)
	a.startTagRemoteDeleteProbe(name, st.rdRemote)
}

func (a *app) closeTagRemoteDeleteDialog() {
	st := a.tagsMenuUI()
	if st.rdOpen || st.rdChecking {
		a.session.cancelRead("tags-remote-del-check")
	}
	st.rdOpen = false
	st.rdTag = ""
	st.rdRemote = ""
	st.rdChecking = false
	st.rdErr = nil
	st.rdOID = ""
	st.rdExists = false
}

// startTagRemoteDeleteProbe begins the asynchronous ls-remote probe the
// remote deletion is gated on, promptless through the session's own
// repository: a remote needing interactive authentication fails the probe
// instead of stacking a second modal onto the open dialog, and a failed
// probe never deletes.
func (a *app) startTagRemoteDeleteProbe(tag, remote string) {
	st := a.tagsMenuUI()
	st.rdChecking = true
	st.rdErr = nil
	st.rdOID = ""
	st.rdExists = false
	if !sessionRead(a.session, "tags-remote-del-check", func(r *git.Repo) (tagOIDProbe, error) {
		oid, exists, err := r.RemoteTagOID(remote, tag)
		return tagOIDProbe{oid: oid, exists: exists}, err
	}, func(probe tagOIDProbe, err error) {
		if !st.rdOpen || st.rdTag != tag || st.rdRemote != remote {
			return
		}
		st.rdChecking = false
		st.rdErr = err
		st.rdOID = probe.oid
		st.rdExists = probe.exists
	}) {
		st.rdChecking = false
		st.rdErr = errors.New("the repository is not open")
	}
}

// tagRemoteDeleteDialog is the confirmation deleting one tag from one remote.
// The confirm button is disabled while the probe runs, when it failed, or
// when the remote has no such tag: a remote state that is unknown — or known
// to hold nothing — never deletes. What the probe answered is pinned into the
// deletion's lease, so a tag moved on the server since the answer is refused
// atomically by git and keeps its new value.
func (a *app) tagRemoteDeleteDialog(c *ui.Context) {
	p := a.pal
	st := a.tagsMenuUI()
	if !st.rdOpen {
		return
	}
	remotes := a.session.Snapshot().Refs.Remotes
	dismissed, cancelled, confirmed := dialogFrame(c, p, "tag-remote-del", &st.rdOpen, "Delete Tag from Remote", 460, func() {
		ui.Field(c, "Tag:", func() {
			ui.Text(c, st.rdTag).Font("monospace").FontSize(12).TextColor(p.text)
		})
		if len(remotes) == 1 {
			ui.Field(c, "Remote:", func() {
				ui.Text(c, st.rdRemote).FontSize(12).TextColor(p.text)
			})
		} else if len(remotes) > 1 {
			ui.Field(c, "Remote:", func() {
				if ui.Select(c, &st.rdRemote, remotes).FillWidth().Label("Delete Remote Tag Remote").Changed() {
					a.startTagRemoteDeleteProbe(st.rdTag, st.rdRemote)
				}
			})
		}
		switch {
		case st.rdChecking:
			ui.Text(c, "Checking remote tag on "+st.rdRemote+"…").FontSize(12).TextColor(p.muted)
		case st.rdErr != nil:
			ui.Text(c, "Cannot verify the remote tag. It will not be deleted.").FontSize(12).TextColor(p.danger)
			ui.Text(c, git.RedactText(st.rdErr.Error())).FontSize(12).TextColor(p.danger)
		case !st.rdExists:
			ui.Text(c, "The remote "+st.rdRemote+" has no tag named "+st.rdTag+".").FontSize(12).TextColor(p.muted)
		default:
			ui.Text(c, "The remote tag points at "+shortTagHash(st.rdOID)+".").FontSize(12).TextColor(p.muted)
			ui.Text(c, "This deletes the tag on the server "+st.rdRemote+". The local tag is kept.").FontSize(12).TextColor(p.danger)
		}
	},
		dialogAction{key: "tag-remote-del-cancel", text: "Cancel", label: "Cancel"},
		// Deleting a tag on the server discards a release others may build
		// on: the destructive confirmation — red, never armed by Enter. It is
		// disabled rather than hidden while the remote state is unknown or
		// known to hold nothing, as the Push Tag dialog does.
		dialogAction{key: "tag-remote-del-confirm", text: "Delete from Remote", label: "Confirm Delete Remote Tag", disabled: st.rdChecking || st.rdErr != nil || !st.rdExists || a.session.Busy(), destructive: true})
	if cancelled {
		a.closeTagRemoteDeleteDialog()
	}
	if confirmed {
		a.submitTagRemoteDelete()
	}
	if dismissed {
		a.closeTagRemoteDeleteDialog()
	}
}

// submitTagRemoteDelete closes the dialog first — an askpass modal of the
// push must never stack on it — and deletes the tag on the remote with the
// exact lease the probe answered. The guards of SafeDeleteRemoteTag
// re-verify everything in the worker: a probe that failed or a verified
// object id gone missing never results in a push at all.
func (a *app) submitTagRemoteDelete() {
	st := a.tagsMenuUI()
	tag := st.rdTag
	remote := strings.TrimSpace(st.rdRemote)
	expectedRemoteOID := st.rdOID
	a.closeTagRemoteDeleteDialog()
	if tag == "" || remote == "" {
		return
	}
	a.runTagSyncAction("Delete remote tag "+tag, func(repo *git.Repo) (string, error) {
		return "", repo.SafeDeleteRemoteTag(remote, tag, expectedRemoteOID, nil)
	})
}

// tagExtraDialogs mounts the modals of the tag context menu items this file
// owns and flushes the clipboard text a copy menu item recorded, which needs
// a frame's *ui.Context the menu build never receives. The flush precedes
// the dialogs so a copy chosen on any frame reaches the clipboard in the
// same frame whether or not a dialog is open.
func (a *app) tagExtraDialogs(c *ui.Context) {
	st := a.tagsMenuUI()
	if st.hasTagClipboard {
		c.WriteClipboard(st.tagClipboard)
		st.tagClipboard = ""
		st.hasTagClipboard = false
	}
	a.tagCheckoutDialog(c)
	a.tagDetailsDialog(c)
	a.tagRemoteDeleteDialog(c)
}
