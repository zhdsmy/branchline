package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// tagsPushUIState is the per-app state of the remote tag pushing: the Push
// Tag dialog with the remote probe its force push is gated on, and the Push
// All Tags dialog. A repository switch resets it and invalidates a probe
// still in flight so its late answer can never apply to the new repository.
type tagsPushUIState struct {
	repoGen uint64

	pushOpen   bool
	pushTag    string
	pushRemote string
	force      bool

	// forceChecking is set while the ls-remote probe of the force push runs.
	// The confirm button of a force push exists only with the probe answered
	// successfully: forceErr holds a failed probe's error verbatim, forceOID
	// and forceExists its answer.
	forceChecking bool
	forceErr      error
	forceOID      string
	forceExists   bool
	forceAgree    bool

	pushAllOpen   bool
	pushAllRemote string
	pushAllCount  int
}

// tagsPushUI returns the app's tag push state, creating it on first use and
// resetting it — including canceling a force push probe in flight — when the
// repository generation moves on. It runs on the UI thread.
func (a *app) tagsPushUI() *tagsPushUIState {
	st := featureState(a, func() *tagsPushUIState { return &tagsPushUIState{} })
	if gen := a.session.Generation(); st.repoGen != gen {
		if st.forceChecking {
			a.session.cancelRead("tags-remote-check")
		}
		*st = tagsPushUIState{repoGen: gen}
	}
	return st
}

// runTagSyncAction runs one remote tag write through the same lifecycle as
// the toolbar sync actions: the askpass-enabled repository copy of syncRepo,
// the worker counting of syncWorkerStarted/syncWorkerDone, and the up-to-date
// note surfacing as a toast instead of an error. The caller must have closed
// its dialog first so an askpass modal is never stacked on top of it.
func (a *app) runTagSyncAction(label string, work func(repo *git.Repo) (string, error)) bool {
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
	}) {
		st.syncWorkerDone()
		return false
	}
	return true
}

// pushAllTagsSidebarEntry is the Push All Tags entry under the New Tag entry
// of the Tags section. It shows only while there is something to push and
// somewhere to push it to, and it is disabled while a write is in flight.
func (a *app) pushAllTagsSidebarEntry(c *ui.Context) {
	refs := a.session.Snapshot().Refs
	if !a.session.HasRepo() || len(refs.Tags) == 0 || len(refs.Remotes) == 0 {
		return
	}
	p := a.pal
	b := ui.ButtonBase(c.Key("tag-push-all")).
		Height(26).
		PaddingX(10).
		Gap(8).
		Radius(6).
		AlignItems(ui.Center).
		Justify(ui.Start).
		Label("Push All Tags").
		Tooltip("Push all tags to a remote").
		Disabled(a.session.Busy()).
		FocusRing(false)
	if b.Hovered() {
		b.Background(p.hover)
	}
	b.Children(func() {
		symbolIcon(c, "arrow.up", fallbackUpArr, 11, p.muted)
		ui.Text(c, "Push All Tags…").FontSize(12.5).FontWeight(400).TextColor(p.muted).SingleLine().Shrink(1).MinWidth(0)
	})
	if b.Clicked() {
		a.openPushAllTagsDialog()
	}
}

// openPushTagDialog opens the Push Tag dialog for the tag, defaulting the
// remote to the repository's default. Every probe field starts empty: no
// answer of an earlier dialog can bless this one's force push.
func (a *app) openPushTagDialog(tag string) {
	refs := a.session.Snapshot().Refs
	if len(refs.Remotes) == 0 {
		return
	}
	st := a.tagsPushUI()
	st.pushOpen = true
	st.pushTag = tag
	st.pushRemote = defaultRemoteOf(refs)
	st.force = false
	st.forceChecking = false
	st.forceErr = nil
	st.forceOID = ""
	st.forceExists = false
	st.forceAgree = false
}

func (a *app) closePushTagDialog() {
	st := a.tagsPushUI()
	if st.pushOpen || st.forceChecking {
		a.session.cancelRead("tags-remote-check")
	}
	st.pushOpen = false
	st.pushTag = ""
	st.pushRemote = ""
	st.force = false
	st.forceChecking = false
	st.forceErr = nil
	st.forceOID = ""
	st.forceExists = false
	st.forceAgree = false
}

// startTagForceProbe begins the asynchronous ls-remote probe the force push
// is gated on, promptless through the session's own repository: a remote
// needing interactive authentication fails the probe instead of stacking a
// second modal onto the open dialog, and a failed probe never force pushes.
func (a *app) startTagForceProbe(tag, remote string) {
	st := a.tagsPushUI()
	st.forceChecking = true
	st.forceErr = nil
	st.forceOID = ""
	st.forceExists = false
	st.forceAgree = false
	if !sessionRead(a.session, "tags-remote-check", func(r *git.Repo) (tagOIDProbe, error) {
		oid, exists, err := r.RemoteTagOID(remote, tag)
		return tagOIDProbe{oid: oid, exists: exists}, err
	}, func(probe tagOIDProbe, err error) {
		if !st.pushOpen || st.pushTag != tag || st.pushRemote != remote || !st.force {
			return
		}
		st.forceChecking = false
		st.forceErr = err
		st.forceOID = probe.oid
		st.forceExists = probe.exists
	}) {
		st.forceChecking = false
		st.forceErr = errors.New("the repository is not open")
	}
}

// tagOIDProbe is the answer of the force push probe: the exact object id the
// remote tag points at and whether the remote has it at all.
type tagOIDProbe struct {
	oid    string
	exists bool
}

// pushTagDialog is the modal pushing one tag to a remote. The default push is
// never forced; ticking the force box starts the remote probe, and only its
// successful answer — plus, while the remote already has the tag, the user's
// explicit overwrite agreement — renders an enabled Force Push Tag button.
func (a *app) pushTagDialog(c *ui.Context) {
	p := a.pal
	st := a.tagsPushUI()
	if !st.pushOpen {
		return
	}
	refs := a.session.Snapshot().Refs
	remotes := refs.Remotes
	if ui.Modal(c.Key("push-tag-modal"), &st.pushOpen, func() {
		ui.Column(c.Key("push-tag-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Push Tag").FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Field(c, "Tag:", func() {
				ui.Text(c, st.pushTag).Font("monospace").FontSize(12).TextColor(p.text)
			})
			switch {
			case len(remotes) == 0:
				ui.Text(c, "The repository has no remote to push to.").FontSize(12).TextColor(p.danger)
			default:
				ui.Field(c, "Remote:", func() {
					if ui.Select(c, &st.pushRemote, remotes).FillWidth().Label("Push Tag remote").Changed() && st.force {
						a.startTagForceProbe(st.pushTag, st.pushRemote)
					}
				})
			}
			forceBox := ui.Checkbox(c.Key("push-tag-force"), &st.force, "Force push (overwrite existing remote tag)").Label("Force push")
			if forceBox.Changed() {
				if st.force {
					a.startTagForceProbe(st.pushTag, st.pushRemote)
				} else {
					a.session.cancelRead("tags-remote-check")
					st.forceChecking = false
					st.forceErr = nil
					st.forceOID = ""
					st.forceExists = false
					st.forceAgree = false
				}
			}
			if st.force {
				switch {
				case st.forceChecking:
					ui.Text(c, "Checking remote tag on "+st.pushRemote+"…").FontSize(12).TextColor(p.muted)
				case st.forceErr != nil:
					ui.Text(c, "Cannot verify the remote tag. It will not be overwritten.").FontSize(12).TextColor(p.danger)
					ui.Text(c, st.forceErr.Error()).FontSize(12).TextColor(p.danger)
				case st.forceExists:
					ui.Text(c, "The remote tag points at "+shortTagHash(st.forceOID)+" and will be overwritten for everyone.").FontSize(12).TextColor(p.danger)
					ui.Checkbox(c.Key("push-tag-agree"), &st.forceAgree, "I understand, overwrite remote tag").Label("Agree to overwrite remote tag")
				default:
					ui.Text(c, "The remote has no tag named "+st.pushTag+" yet. It is still pushed with an empty lease: one created in the meantime is never overwritten.").FontSize(12).TextColor(p.muted)
				}
			}
			ui.Row(c.Key("push-tag-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("push-tag-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closePushTagDialog()
				}
				canSubmit := len(remotes) > 0 && !a.session.Busy()
				if st.force {
					// The confirm button of a force push does not exist while
					// the probe runs and does not exist when it failed: a
					// remote state that is unknown is never overwritten.
					if !st.forceChecking && st.forceErr == nil {
						ready := canSubmit && (!st.forceExists || st.forceAgree)
						if ui.PrimaryButton(c.Key("push-tag-force-confirm"), "Force Push Tag").
							Label("Confirm Force Push Tag").
							Disabled(!ready).
							Clicked() {
							a.submitForcePushTag()
						}
					}
				} else {
					if ui.PrimaryButton(c.Key("push-tag-confirm"), "Push Tag").
						Label("Confirm Push Tag").
						Disabled(!canSubmit).
						Clicked() {
						a.submitPushTag()
					}
				}
			})
		})
	}).Dismissed() {
		a.closePushTagDialog()
	}
}

// submitPushTag closes the dialog first — an askpass modal of the push must
// never stack on it — and pushes the tag without force: a diverged remote tag
// of the same name is refused by git and never overwritten.
func (a *app) submitPushTag() {
	st := a.tagsPushUI()
	tag := st.pushTag
	remote := strings.TrimSpace(st.pushRemote)
	a.closePushTagDialog()
	if tag == "" || remote == "" {
		return
	}
	a.runTagSyncAction("Push tag "+tag, func(repo *git.Repo) (string, error) {
		return repo.SafePushTag(remote, tag)
	})
}

// submitForcePushTag closes the dialog first and pushes with the exact lease
// the probe answered. The guards of SafeForcePushTag re-verify everything in
// the worker: a probe that failed or a verified object id gone missing never
// results in a push at all.
func (a *app) submitForcePushTag() {
	st := a.tagsPushUI()
	tag := st.pushTag
	remote := strings.TrimSpace(st.pushRemote)
	expectedOID := st.forceOID
	exists := st.forceExists
	a.closePushTagDialog()
	if tag == "" || remote == "" {
		return
	}
	a.runTagSyncAction("Force push tag "+tag, func(repo *git.Repo) (string, error) {
		return "", repo.SafeForcePushTag(remote, tag, expectedOID, exists, nil)
	})
}

// openPushAllTagsDialog opens the Push All Tags dialog, defaulting the remote
// to the repository's default.
func (a *app) openPushAllTagsDialog() {
	refs := a.session.Snapshot().Refs
	if len(refs.Remotes) == 0 || len(refs.Tags) == 0 {
		return
	}
	st := a.tagsPushUI()
	st.pushAllOpen = true
	st.pushAllRemote = defaultRemoteOf(refs)
	st.pushAllCount = len(refs.Tags)
}

func (a *app) closePushAllTagsDialog() {
	st := a.tagsPushUI()
	st.pushAllOpen = false
	st.pushAllRemote = ""
	st.pushAllCount = 0
}

// pushAllTagsDialog is the modal pushing every local tag to one remote,
// always without force: remote tags pointing anywhere else are refused by
// git and never overwritten.
func (a *app) pushAllTagsDialog(c *ui.Context) {
	p := a.pal
	st := a.tagsPushUI()
	if !st.pushAllOpen {
		return
	}
	refs := a.session.Snapshot().Refs
	remotes := refs.Remotes
	dismissed, cancelled, confirmed := dialogFrame(c, p, "push-all-tags-modal", &st.pushAllOpen, "Push All Tags", 460, func() {
		countText := fmt.Sprintf("%d local tags are pushed without force.", st.pushAllCount)
		if st.pushAllCount == 1 {
			countText = "1 local tag is pushed without force."
		}
		ui.Text(c, countText).FontSize(12).TextColor(p.muted)
		ui.Text(c, "A remote tag pointing anywhere else is never overwritten.").FontSize(12).TextColor(p.muted)
		switch {
		case len(remotes) == 0:
			ui.Text(c, "The repository has no remote to push to.").FontSize(12).TextColor(p.danger)
		default:
			ui.Field(c, "Remote:", func() {
				ui.Select(c, &st.pushAllRemote, remotes).FillWidth().Label("Push All Tags remote")
			})
		}
	},
		dialogAction{key: "push-all-cancel", text: "Cancel", label: "Cancel"},
		dialogAction{key: "push-all-confirm", text: "Push All Tags", label: "Confirm Push All Tags", disabled: len(remotes) == 0 || a.session.Busy()})
	if cancelled {
		a.closePushAllTagsDialog()
	}
	if confirmed {
		a.submitPushAllTags()
	}
	if dismissed {
		a.closePushAllTagsDialog()
	}
}

// submitPushAllTags closes the dialog first and pushes every local tag
// without force.
func (a *app) submitPushAllTags() {
	st := a.tagsPushUI()
	remote := strings.TrimSpace(st.pushAllRemote)
	a.closePushAllTagsDialog()
	if remote == "" {
		return
	}
	a.runTagSyncAction("Push all tags", func(repo *git.Repo) (string, error) {
		return repo.SafePushAllTags(remote)
	})
}

// submitNewTagWithPush is the New Tag submit for a ticked Push to remote:
// the dialog closes first, then the create and push run through the sync
// lifecycle so an SSH remote can ask for its password or key passphrase. A
// failed push keeps the created local tag and surfaces its error.
func (a *app) submitNewTagWithPush() {
	st := a.tagsUI()
	name := strings.TrimSpace(st.newName)
	target := strings.TrimSpace(st.newTarget)
	message := st.newMessage
	remote := strings.TrimSpace(st.newPushRemote)
	a.closeNewTagDialog()
	if name == "" || target == "" || remote == "" {
		return
	}
	a.runTagSyncAction("Create tag "+name, func(repo *git.Repo) (string, error) {
		return "", repo.SafeCreateTagAndPush(name, target, message, remote)
	})
}

// tagsPushDialogs mounts the Push Tag and Push All Tags modals of the refs
// sidebar.
func (a *app) tagsPushDialogs(c *ui.Context) {
	a.pushTagDialog(c)
	a.pushAllTagsDialog(c)
}
