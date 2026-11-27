package app

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// pushDialogUIState is the per-app draft of the toolbar Push dialog: the
// fields the user edits, the branch list, remote list, current branch and
// HEAD hash captured from the snapshot when the dialog opened, and the
// repository generation they belong to. It lives outside the shared app
// struct, keyed by the app instance, exactly as the other sync dialog states
// do; a repository switch or close discards every captured value, so nothing
// of the previous repository carries over.
type pushDialogUIState struct {
	repoGen uint64

	open        bool
	branch      string
	branches    []git.Branch
	remote      string
	remotes     []string
	dest        string
	destEdited  bool
	setUpstream bool
	force       bool
	forceAgree  bool
	tags        bool

	expectHeadBranch string
	expectHead       string
}

var pushDialogUIStates = struct {
	sync.Mutex
	m map[*app]*pushDialogUIState
}{m: map[*app]*pushDialogUIState{}}

// pushDialogUI returns the app's Push dialog state, creating it on first use
// and discarding every open draft when the repository generation moved past
// it: a switch or close makes the captured branches, remotes and HEAD
// meaningless, so the dialog of the previous repository never carries over.
// It runs on the UI thread.
func pushDialogUI(a *app) *pushDialogUIState {
	pushDialogUIStates.Lock()
	defer pushDialogUIStates.Unlock()
	st := pushDialogUIStates.m[a]
	if st == nil {
		st = &pushDialogUIState{}
		pushDialogUIStates.m[a] = st
	}
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		st.clearPushDraft()
	}
	return st
}

// clearPushDraft closes the dialog and drops every value captured for it, so
// a reopened dialog starts from the repository as it is then.
func (st *pushDialogUIState) clearPushDraft() {
	st.open = false
	st.branch = ""
	st.branches = nil
	st.remote = ""
	st.remotes = nil
	st.dest = ""
	st.destEdited = false
	st.setUpstream = false
	st.force = false
	st.forceAgree = false
	st.tags = false
	st.expectHeadBranch = ""
	st.expectHead = ""
}

// pushDialogBranch is the captured local branch the dialog's choice names,
// nil when the branch list no longer holds it — impossible while the dialog
// constrains the choice to its own options.
func pushDialogBranch(st *pushDialogUIState) *git.Branch {
	for i := range st.branches {
		if st.branches[i].Name == st.branch {
			return &st.branches[i]
		}
	}
	return nil
}

// liveUpstreamOf splits the branch's upstream into its remote and the branch
// name there when the upstream exists, is not gone, and its remote is still
// configured; the third value is false in every other case, including an
// upstream whose remote was deleted since the tracking was set up.
func liveUpstreamOf(b git.Branch, remotes []string) (remote, remoteBranch string, ok bool) {
	if b.Upstream == "" || b.Gone {
		return "", "", false
	}
	return splitBranchUpstream(b.Upstream, remotes)
}

// pushDialogDefaults derives the dialog's initial fields for one local
// branch: the remote and remote branch name of a live upstream, or the
// repository's default remote and the branch's own name when there is none.
// Upstream tracking is offered exactly when the branch has no live upstream
// yet; force and tags are never offered by default.
func pushDialogDefaults(b git.Branch, remotes []string) (remote, dest string, setUpstream bool) {
	if upRemote, upBranch, ok := liveUpstreamOf(b, remotes); ok {
		return upRemote, upBranch, false
	}
	return defaultRemoteOf(git.Refs{Remotes: remotes}), b.Name, true
}

// openPushDialog opens the toolbar Push dialog. Every validation mirrors the
// quick push: with no usable snapshot the real state is probed by the sync
// worker instead of opening an empty dialog, and an unborn branch, a
// detached HEAD, or a remoteless repository is a pre-flight error in the
// dismissible toolbar strip rather than a dialog. Everything the dialog and
// its worker need later is captured here, on the UI thread: the branch list
// with hashes and upstreams, the remote list, and the current branch name
// and HEAD hash the push must still find when it runs — the background guard
// refuses the write when any of them has moved since.
func (a *app) openPushDialog() {
	if a.session.Busy() || !a.session.HasRepo() {
		return
	}
	if !a.session.Loaded() || a.session.LoadErr() != nil {
		// The snapshot never loaded — an SSH repository whose encrypted key
		// had no askpass yet, or a repository that broke after opening: the
		// quick push probes the real state through the askpass channel and
		// reports what it finds in the toolbar.
		a.syncPush()
		return
	}
	snap := a.session.Snapshot()
	headKind, ok := snapshotHeadKind(snap.Refs, snap.Status)
	if !ok {
		// A checkout landing between the snapshot's two reads tore it: the
		// quick push re-reads refs and the HEAD state live instead of
		// trusting a torn classification.
		a.syncPush()
		return
	}
	st := syncStateFor(a)
	st.syncErr = ""
	a.session.ClearActionErr()
	if _, err := resolvePushTarget(snap.Refs, headKind); err != nil {
		st.syncErr = err.Error()
		return
	}
	if len(snap.Refs.Remotes) == 0 {
		st.syncErr = errMsgPushNoRemote
		return
	}
	cur := currentBranchOf(snap.Refs)
	if cur == nil {
		st.syncErr = errMsgPushNoBranch
		return
	}
	dlg := pushDialogUI(a)
	dlg.clearPushDraft()
	dlg.open = true
	dlg.branch = cur.Name
	dlg.branches = slices.Clone(snap.Refs.Branches)
	dlg.remotes = slices.Clone(snap.Refs.Remotes)
	dlg.expectHeadBranch = snap.Status.Branch
	dlg.expectHead = snap.Status.Head
	dlg.remote, dlg.dest, dlg.setUpstream = pushDialogDefaults(*cur, dlg.remotes)
}

// closePushDialog closes the Push dialog and clears its draft. Canceling,
// dismissing, submitting and switching repositories all end here so no
// captured branch, remote or HEAD survives the dialog.
func (a *app) closePushDialog() {
	pushDialogUI(a).clearPushDraft()
}

// normalizePushDest reduces a typed remote branch name to the branch it
// means: surrounding whitespace is never part of it, and the refs/heads/
// prefix of a full ref is the spelling, not the name.
func normalizePushDest(raw string) string {
	return strings.TrimPrefix(strings.TrimSpace(raw), "refs/heads/")
}

// pushDestProblem says why the normalized remote branch name cannot be
// pushed to, empty when it can. It shares the character rules of the branch
// dialogs.
func pushDestProblem(raw string) string {
	if normalizePushDest(raw) == "" {
		return "Enter a remote branch name"
	}
	return validateBranchName(normalizePushDest(raw))
}

// pushDialogCanSubmit reports whether the dialog's confirm button can run: a
// valid remote branch name, no repository write running, and for a force
// push the explicit overwrite agreement.
func pushDialogCanSubmit(st *pushDialogUIState, busy bool) bool {
	if busy || pushDestProblem(st.dest) != "" {
		return false
	}
	if st.force && !st.forceAgree {
		return false
	}
	return true
}

// pushDialog is the modal of the toolbar Push: the local branch to push, the
// remote and the branch name there, upstream tracking, and the two options.
// A ticked force push unfolds its warning and the explicit agreement the
// confirm button is gated on; the button itself switches between Push and
// Force Push with the choice.
func (a *app) pushDialog(c *ui.Context) {
	p := a.pal
	st := pushDialogUI(a)
	if !st.open {
		return
	}
	branchNames := make([]string, len(st.branches))
	for i, b := range st.branches {
		branchNames[i] = b.Name
	}
	if ui.Modal(c.Key("push-dialog-modal"), &st.open, func() {
		ui.Column(c.Key("push-dialog-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Push").FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Field(c, "Branch:", func() {
				if ui.Select(c, &st.branch, branchNames).FillWidth().Label("Push Local Branch").Changed() {
					// A different branch starts from its own upstream state:
					// the remote, the destination and the tracking default are
					// recomputed, the typed destination is forgotten, and no
					// force push of the previous branch can follow the choice.
					if b := pushDialogBranch(st); b != nil {
						st.remote, st.dest, st.setUpstream = pushDialogDefaults(*b, st.remotes)
					}
					st.destEdited = false
					st.force = false
					st.forceAgree = false
				}
			})
			ui.Field(c, "Remote:", func() {
				if ui.Select(c, &st.remote, st.remotes).FillWidth().Label("Push Remote").Changed() {
					// A destination the user typed stays; a default one follows
					// the new remote: the upstream's branch name when this is
					// the upstream's remote, the branch's own name otherwise.
					if !st.destEdited {
						if b := pushDialogBranch(st); b != nil {
							if upRemote, upBranch, ok := liveUpstreamOf(*b, st.remotes); ok && upRemote == st.remote {
								st.dest = upBranch
							} else {
								st.dest = b.Name
							}
						}
					}
					st.forceAgree = false
				}
			})
			ui.Field(c, "Remote branch:", func() {
				in := ui.TextInput(c.Key("push-dialog-dest"), &st.dest).FillWidth().Label("Push Remote Branch")
				if in.Changed() {
					st.destEdited = true
					st.forceAgree = false
				}
				if in.Submitted() && pushDialogCanSubmit(st, a.session.Busy()) {
					a.submitPushDialog()
				}
			})
			if problem := pushDestProblem(st.dest); problem != "" {
				ui.Text(c, problem).FontSize(12).TextColor(p.danger)
			}
			ui.Checkbox(c.Key("push-dialog-upstream"), &st.setUpstream, "Set upstream tracking").Label("Set upstream tracking").Tooltip("--set-upstream")
			if ui.Checkbox(c.Key("push-dialog-force"), &st.force, "Force push (with lease)").Label("Force push (with lease)").Tooltip("--force-with-lease").Changed() {
				// The agreement never survives the box flipping in either
				// direction: unticking retires it, and ticking again starts
				// from disagreement.
				st.forceAgree = false
			}
			if st.force {
				target := st.remote + "/" + strings.TrimSpace(st.dest)
				ui.Text(c, "Force pushing may overwrite commits on "+target+" for everyone.").FontSize(12).TextColor(p.danger).Wrap()
				ui.Text(c, "--force-with-lease protects you: if the remote branch was updated since your last fetch, the push is rejected instead of overwriting it.").FontSize(12).TextColor(p.muted).Wrap()
				ui.Checkbox(c.Key("push-dialog-agree"), &st.forceAgree, "I understand, force push with lease").Label("Confirm force push with lease")
			}
			ui.Checkbox(c.Key("push-dialog-tags"), &st.tags, "Push all tags").Label("Push all tags").Tooltip("--tags")
			ui.Row(c.Key("push-dialog-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("push-dialog-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closePushDialog()
				}
				canSubmit := pushDialogCanSubmit(st, a.session.Busy())
				if st.force {
					if ui.PrimaryButton(c.Key("push-dialog-confirm"), "Force Push").
						Label("Confirm Force Push").
						Disabled(!canSubmit).
						Clicked() {
						a.submitPushDialog()
					}
				} else {
					if ui.PrimaryButton(c.Key("push-dialog-confirm"), "Push").
						Label("Confirm Push").
						Disabled(!canSubmit).
						Clicked() {
						a.submitPushDialog()
					}
				}
			})
		})
	}).Dismissed() {
		a.closePushDialog()
	}
}

// pushDialogRequest is the immutable description of one push the dialog
// confirmed: the fields as the user set them and the repository state the
// push was agreed against. The worker receives it by value and never reads
// the dialog draft, the app, or the store.
type pushDialogRequest struct {
	branch             string
	dest               string
	remote             string
	setUpstream        bool
	force              bool
	tags               bool
	expectedBranchHash string
	expectedHeadBranch string
	expectedHead       string
}

// submitPushDialog captures every field and expectation on the UI thread,
// closes the dialog first — an askpass modal of the push must never stack on
// it — and pushes through the session's sync lifecycle: the askpass-enabled
// repository copy, the worker counting, and the up-to-date note surfacing as
// a toast instead of an error.
func (a *app) submitPushDialog() {
	st := pushDialogUI(a)
	if !st.open || a.session.Busy() {
		return
	}
	req := pushDialogRequest{
		branch:             st.branch,
		dest:               normalizePushDest(st.dest),
		remote:             st.remote,
		setUpstream:        st.setUpstream,
		force:              st.force,
		tags:               st.tags,
		expectedHeadBranch: st.expectHeadBranch,
		expectedHead:       st.expectHead,
	}
	if b := pushDialogBranch(st); b != nil {
		req.expectedBranchHash = b.Hash
	}
	a.closePushDialog()
	if req.branch == "" || req.remote == "" || req.dest == "" {
		return
	}
	syncSt := syncStateFor(a)
	repo, gen := a.syncRepo()
	if repo == nil {
		return
	}
	syncSt.syncErr = ""
	a.session.ClearActionErr()
	syncSt.syncWorkerStarted()
	if !a.session.doThen("Pushing", func(*git.Repo) error {
		defer syncSt.syncWorkerDone()
		note, err := pushDialogWithGuard(repo, req)
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
		syncSt.syncWorkerDone()
	}
}

// pushDialogWithGuard re-validates the repository against what the Push
// dialog captured before it pushes anything: the arguments themselves must
// be safe names, status and refs must both be readable, HEAD and the
// checked-out branch must still be exactly where the dialog saw them, the
// target remote must still exist, and the pushed branch must still point at
// the commit the dialog captured. Any check failing returns the error
// without running git push, so a repository that moved behind the dialog's
// back is never written. Only then does the push run — forced exclusively
// through --force-with-lease. Every error is redacted before returning so
// credentials embedded in a remote URL never reach any error display.
func pushDialogWithGuard(repo *git.Repo, req pushDialogRequest) (note string, err error) {
	defer func() {
		if err != nil {
			err = errors.New(git.RedactText(err.Error()))
		}
	}()
	if req.branch == "" || req.dest == "" || req.remote == "" || req.expectedHeadBranch == "" || req.expectedHead == "" || req.expectedBranchHash == "" {
		return "", fmt.Errorf("cannot push: the push request is incomplete")
	}
	if strings.HasPrefix(req.remote, "-") || strings.ContainsAny(req.remote, " \t\r\n\x00") {
		return "", fmt.Errorf("cannot push: invalid remote")
	}
	if problem := validateBranchName(req.branch); problem != "" {
		return "", fmt.Errorf("cannot push %s: %s", req.branch, problem)
	}
	if problem := validateBranchName(req.dest); problem != "" {
		return "", fmt.Errorf("cannot push to %s: %s", req.dest, problem)
	}
	status, err2 := repo.Status()
	if err2 != nil {
		return "", fmt.Errorf("cannot push %s: reading the repository state failed: %w", req.branch, err2)
	}
	if status.Head != req.expectedHead || status.Branch != req.expectedHeadBranch {
		return "", fmt.Errorf("cannot push %s: HEAD or the current branch changed after the dialog opened; review the repository and try again", req.branch)
	}
	refs, err2 := repo.Refs()
	if err2 != nil {
		return "", fmt.Errorf("cannot push %s: reading repository references failed: %w", req.branch, err2)
	}
	if !slices.Contains(refs.Remotes, req.remote) {
		return "", fmt.Errorf("cannot push %s: remote %q no longer exists", req.branch, req.remote)
	}
	local := findLocalBranch(refs, req.branch)
	if local == nil {
		return "", fmt.Errorf("cannot push %s: the branch no longer exists", req.branch)
	}
	if local.Hash != req.expectedBranchHash {
		return "", fmt.Errorf("cannot push %s: the branch moved after the dialog opened; review the repository and try again", req.branch)
	}
	if cur := currentBranchOf(refs); cur == nil || cur.Name != req.expectedHeadBranch || cur.Hash != req.expectedHead {
		return "", fmt.Errorf("cannot push %s: HEAD or the current branch changed after the dialog opened; review the repository and try again", req.branch)
	}
	note, err2 = repo.PushNote(req.remote, req.branch, req.dest, req.setUpstream, req.force, req.tags)
	if err2 != nil {
		return "", formatPushError(err2, req.force)
	}
	return note, nil
}

// pushRejection is one reference a git push --porcelain run refused: the
// source and destination of the update and git's one-line reason.
type pushRejection struct {
	src     string
	dst     string
	summary string
}

// pushRejections reads the rejected lines of a push --porcelain output: the
// ones led by "!". Git writes them to standard output while standard error
// carries only the generic failure, so lease rejections would otherwise lose
// their reason on the way to the toolbar.
func pushRejections(stdout []byte) []pushRejection {
	var out []pushRejection
	for line := range strings.Lines(string(stdout)) {
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "!\t") {
			continue
		}
		rest := strings.TrimPrefix(line, "!\t")
		spec, summary, _ := strings.Cut(rest, "\t")
		src, dst, _ := strings.Cut(spec, ":")
		out = append(out, pushRejection{src: src, dst: dst, summary: strings.TrimSpace(summary)})
	}
	return out
}

// formatPushError turns a failed push into the error the user reads. A lease
// rejection — the remote moved since the last fetch, so --force-with-lease
// refused — answers with the reason first, because both the session error
// and the 240-pixel toolbar strip cut off anything trailing. Other rejected
// references merge their porcelain reason into the error when the error does
// not already carry it: git prints those to standard output only.
func formatPushError(err error, force bool) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	var rejects []pushRejection
	var ge *git.Error
	if errors.As(err, &ge) {
		rejects = pushRejections(ge.Stdout)
	}
	stale := strings.Contains(strings.ToLower(text), "stale info")
	if !stale {
		for _, r := range rejects {
			if strings.Contains(strings.ToLower(r.summary), "stale info") {
				stale = true
				break
			}
		}
	}
	if stale {
		prefix := "Push rejected (stale info)"
		if force {
			prefix = "Force push rejected (stale info)"
		}
		msg := prefix + ": remote was updated since your last fetch; fetch and review before retrying."
		var refs []string
		for _, r := range rejects {
			if strings.Contains(strings.ToLower(r.summary), "stale info") && r.src != "" {
				refs = append(refs, r.src+":"+r.dst)
			}
		}
		if len(refs) > 0 {
			msg += " Rejected: " + strings.Join(refs, ", ") + "."
		}
		return errors.New(msg)
	}
	var extra []string
	for _, r := range rejects {
		if r.summary != "" && !strings.Contains(text, r.summary) {
			extra = append(extra, r.summary)
		}
	}
	if len(extra) > 0 {
		return errors.New(text + "; " + strings.Join(extra, "; "))
	}
	return err
}
