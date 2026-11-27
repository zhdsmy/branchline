package app

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// remoteBranchMenuUIState is the per-app state of the remote branch context
// menu's two dialogs: the Checkout Remote Branch draft with the remote
// branch, HEAD and branch it was opened against, and the Delete Remote
// Branch confirmation with the commit the remote branch pointed at. It
// lives outside the shared app struct, keyed by the app instance, exactly
// as the merge and rebase dialog states do, and every captured value belongs
// to one repository generation.
type remoteBranchMenuUIState struct {
	repoGen uint64

	coOpen         bool
	coRemote       string // the remote branch, such as origin/feature
	coName         string
	coNameChanged  bool
	coExpectOID    string
	coExpectHead   string
	coExpectBranch string

	rdOpen      bool
	rdFull      string // the remote branch, such as origin/feature
	rdRemote    string
	rdBranch    string
	rdExpectOID string
}

var remoteBranchMenuUIStates = struct {
	sync.Mutex
	m map[*app]*remoteBranchMenuUIState
}{m: map[*app]*remoteBranchMenuUIState{}}

// remoteBranchMenuUI returns the app's remote branch menu state, creating it
// on first use and discarding every open dialog draft when the repository
// generation moved past it: a switch or close makes the captured remote
// branch, HEAD and expectations meaningless, so nothing of the previous
// repository carries over.
func remoteBranchMenuUI(a *app) *remoteBranchMenuUIState {
	remoteBranchMenuUIStates.Lock()
	defer remoteBranchMenuUIStates.Unlock()
	st := remoteBranchMenuUIStates.m[a]
	if st == nil {
		st = &remoteBranchMenuUIState{}
		remoteBranchMenuUIStates.m[a] = st
	}
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		st.clearDraft()
	}
	return st
}

// clearDraft closes both dialogs and drops every value captured for them, so
// a reopened dialog starts from the repository as it is then.
func (st *remoteBranchMenuUIState) clearDraft() {
	st.coOpen = false
	st.coRemote = ""
	st.coName = ""
	st.coNameChanged = false
	st.coExpectOID = ""
	st.coExpectHead = ""
	st.coExpectBranch = ""
	st.rdOpen = false
	st.rdFull = ""
	st.rdRemote = ""
	st.rdBranch = ""
	st.rdExpectOID = ""
}

// remoteBranchIsHEADSymref reports a remote's HEAD symbolic reference, such
// as origin/HEAD: it names the remote's default branch, not a branch of its
// own, so neither checking it out nor deleting it is meaningful. The refs
// parser already drops it from the sidebar; the menu double-checks so the
// items can never act on it.
func remoteBranchIsHEADSymref(rb git.Branch) bool {
	_, rest, ok := strings.Cut(rb.Name, "/")
	return ok && rest == "HEAD"
}

// remoteBranchCheckoutMenuItems adds the Checkout item and its separator to
// the context menu of one remote-tracking branch of the sidebar. The item is
// disabled while a write or sync is busy and while a merge, rebase,
// cherry-pick or revert is in progress.
func (a *app) remoteBranchCheckoutMenuItems(m *ui.Menu, rb git.Branch) {
	if remoteBranchIsHEADSymref(rb) {
		return
	}
	snap := a.session.Snapshot()
	disabled := a.session.Busy() || snap.Status.State != ""
	if m.Item("Checkout…").Disabled(disabled).Chosen() {
		a.openRemoteBranchCheckout(rb)
	}
	m.Separator()
}

// remoteBranchDeleteMenuItems adds the Delete item and its separator to the
// context menu of one remote-tracking branch of the sidebar, disabled under
// the same conditions as Checkout.
func (a *app) remoteBranchDeleteMenuItems(m *ui.Menu, rb git.Branch) {
	if remoteBranchIsHEADSymref(rb) {
		return
	}
	snap := a.session.Snapshot()
	disabled := a.session.Busy() || snap.Status.State != ""
	m.Separator()
	if m.Item("Delete '" + rb.Name + "'…").Disabled(disabled).Chosen() {
		a.openRemoteBranchDelete(rb)
	}
}

// openRemoteBranchCheckout opens the Checkout Remote Branch dialog for the
// remote-tracking branch. Everything the dialog or its worker needs later is
// captured now, on the UI thread: the remote branch's full name and the
// commit it pointed at, and the HEAD hash and branch the checkout must still
// find when it runs — the background guard refuses the write when any of
// them has moved since. The local name starts as the branch part after the
// remote, as git checkout --track would name it.
func (a *app) openRemoteBranchCheckout(rb git.Branch) {
	st := remoteBranchMenuUI(a)
	snap := a.session.Snapshot()
	st.coOpen = true
	st.coRemote = rb.Name
	_, after, _ := strings.Cut(rb.Name, "/")
	st.coName = after
	st.coNameChanged = false
	st.coExpectOID = rb.Hash
	st.coExpectHead = snap.Status.Head
	st.coExpectBranch = snap.Status.Branch
}

// remoteCheckoutProblem says why the typed local name cannot be checked out,
// empty when it can. The collision rule is the checkout's own: a same-named
// local branch tracking this very remote branch is not a problem at all —
// the checkout then simply switches to it — while one tracking anything else
// (or nothing) blocks. The remaining rules are those of the New Branch
// dialog: beyond git check-ref-format, a name starting with "-" would be
// read as options, one holding whitespace would split into arguments, and
// one ending in "/" is a directory, never a ref.
func (a *app) remoteCheckoutProblem(full, name string) string {
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
		if br.Name == name && br.Upstream != full {
			return "A branch named " + name + " already exists and does not track " + full
		}
	}
	return ""
}

// remoteCheckoutSwitchesToExisting reports whether the typed local name is
// an existing local branch whose upstream is the remote branch itself: the
// checkout then switches to it instead of creating anything.
func (a *app) remoteCheckoutSwitchesToExisting(full, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, br := range a.session.Snapshot().Refs.Branches {
		if br.Name == name && br.Upstream == full {
			return true
		}
	}
	return false
}

// remoteBranchMenuDialogs mounts both remote branch dialogs of the sidebar:
// Checkout Remote Branch and Delete Remote Branch. Mounting them once here
// keeps the sidebar the single composition root of the refs dialogs.
func (a *app) remoteBranchMenuDialogs(c *ui.Context) {
	a.remoteBranchCheckoutDialog(c)
	a.remoteBranchPullDialog(c)
	a.remoteBranchDeleteDialog(c)
}

// remoteBranchCheckoutDialog is the modal checking out a remote-tracking
// branch as a local branch. The input is validated as the user types: an
// unusable name disables Checkout with the reason shown. A name that already
// exists locally tracking the remote branch is not an error — the dialog
// says the checkout switches to it.
func (a *app) remoteBranchCheckoutDialog(c *ui.Context) {
	p := a.pal
	st := remoteBranchMenuUI(a)
	if !st.coOpen {
		return
	}
	ui.Modal(c.Key("rb-checkout-modal"), &st.coOpen, func() {
		ui.Column(c.Key("rb-checkout-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Checkout Remote Branch").FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Field(c, "Remote Branch:", func() {
				ui.Text(c, st.coRemote).Font("monospace").FontSize(12).TextColor(p.text).SingleLine().Ellipsis("…")
			})
			ui.Field(c, "Local Branch Name:", func() {
				in := ui.TextInput(c, &st.coName).FillWidth().AutoFocus().Label("Local Branch Name")
				if in.Changed() {
					st.coNameChanged = true
				}
				if in.Submitted() && a.remoteCheckoutCanSubmit() {
					a.submitRemoteBranchCheckout()
				}
			})
			name := strings.TrimSpace(st.coName)
			switch {
			case a.remoteCheckoutProblem(st.coRemote, name) != "" && (st.coNameChanged || name != ""):
				ui.Text(c, a.remoteCheckoutProblem(st.coRemote, name)).FontSize(12).TextColor(p.danger).MaxWidth(424)
			case a.remoteCheckoutSwitchesToExisting(st.coRemote, name):
				ui.Text(c, "A local branch named "+name+" already tracks "+st.coRemote+"; checkout switches to it.").FontSize(12).TextColor(p.muted).MaxWidth(424)
			}
			ui.Row(c.Key("rb-checkout-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("rb-checkout-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeRemoteBranchCheckout()
				}
				if ui.PrimaryButton(c.Key("rb-checkout-confirm"), "Checkout").Label("Confirm Checkout Remote Branch").Disabled(!a.remoteCheckoutCanSubmit()).Clicked() {
					a.submitRemoteBranchCheckout()
				}
			})
		})
	})
	if !st.coOpen {
		a.closeRemoteBranchCheckout()
	}
}

func (a *app) remoteCheckoutCanSubmit() bool {
	st := remoteBranchMenuUI(a)
	name := strings.TrimSpace(st.coName)
	return name != "" && a.remoteCheckoutProblem(st.coRemote, name) == "" && !a.session.Busy() && a.session.Snapshot().Status.State == ""
}

// closeRemoteBranchCheckout closes the dialog and drops every value captured
// for it. Canceling, dismissing, submitting and switching repositories all
// end here, so no captured expectation survives the dialog.
func (a *app) closeRemoteBranchCheckout() {
	st := remoteBranchMenuUI(a)
	st.coOpen = false
	st.coRemote = ""
	st.coName = ""
	st.coNameChanged = false
	st.coExpectOID = ""
	st.coExpectHead = ""
	st.coExpectBranch = ""
}

// submitRemoteBranchCheckout runs the checkout the dialog describes through
// the session's write path. The background worker re-validates everything
// the dialog assumed — the HEAD, the branch, the remote branch's commit and
// the local name still being free — because any of it can change between the
// click and the write.
func (a *app) submitRemoteBranchCheckout() {
	st := remoteBranchMenuUI(a)
	if a.session.Busy() {
		return
	}
	remoteBranch := st.coRemote
	local := strings.TrimSpace(st.coName)
	expectOID := st.coExpectOID
	expectHead := st.coExpectHead
	expectBranch := st.coExpectBranch
	a.closeRemoteBranchCheckout()
	a.session.do("Checkout "+local, func(repo *git.Repo) error {
		return remoteCheckoutWithGuard(repo, remoteBranch, local, expectOID, expectHead, expectBranch)
	})
}

// remoteCheckoutWithGuard re-validates the repository against what the
// Checkout Remote Branch dialog captured before it creates or switches
// anything: HEAD and the branch must still be exactly where the dialog saw
// them, the remote branch must still resolve to the commit the user saw, and
// a same-named local branch created since — unless it tracks the remote
// branch itself — refuses the write. Only then does the checkout run: a
// plain switch when the local branch already tracks the remote branch, or a
// new tracking branch otherwise. Any check failing returns the error without
// touching HEAD, the index, the working tree or any ref. Local changes git
// would have to overwrite make git itself refuse; nothing is forced here.
func remoteCheckoutWithGuard(repo *git.Repo, remoteBranch, local, expectOID, expectHead, expectBranch string) error {
	if remoteBranch == "" || strings.HasPrefix(remoteBranch, "-") || strings.ContainsAny(remoteBranch, "\x00\r\n") {
		return fmt.Errorf("cannot checkout: invalid remote branch")
	}
	if local == "" || strings.HasPrefix(local, "-") || strings.HasSuffix(local, "/") || strings.ContainsAny(local, " \t\r\n\x00") {
		return fmt.Errorf("cannot checkout: invalid local branch name")
	}
	if problem := git.RefNameProblem(local); problem != "" {
		return fmt.Errorf("cannot checkout: invalid local branch name")
	}
	if expectOID == "" {
		return fmt.Errorf("cannot checkout: missing expected remote branch commit")
	}
	fullRef := "refs/remotes/" + remoteBranch
	st, err := repo.Status()
	if err != nil {
		return fmt.Errorf("cannot checkout: reading the repository state failed: %w", err)
	}
	if st.State != "" {
		return fmt.Errorf("cannot checkout: a %s is in progress; finish or abort it first", st.State)
	}
	if st.Head != expectHead || st.Branch != expectBranch {
		return fmt.Errorf("cannot checkout: HEAD or the current branch changed after the dialog opened; review the repository and try again")
	}
	gotOID, err := repo.PathHash(fullRef + "^{commit}")
	if err != nil {
		return fmt.Errorf("cannot checkout: the remote branch %s no longer resolves to a commit: %w", remoteBranch, err)
	}
	if gotOID != expectOID {
		return fmt.Errorf("cannot checkout: the remote branch %s changed after the dialog opened; review the repository and try again", remoteBranch)
	}
	refs, err := repo.Refs()
	if err != nil {
		return fmt.Errorf("cannot checkout: reading the branches failed: %w", err)
	}
	for _, br := range refs.Branches {
		if br.Name != local {
			continue
		}
		if br.Upstream != remoteBranch {
			return fmt.Errorf("cannot checkout: a branch named %s already exists and does not track %s", local, remoteBranch)
		}
		return repo.Checkout(local)
	}
	return repo.CheckoutRemoteAs(remoteBranch, local, false)
}

// openRemoteBranchDelete opens the Delete Remote Branch confirmation for the
// remote-tracking branch, capturing on the UI thread the remote and branch
// names and the commit the branch pointed at — the lease the delete is
// confirmed against.
func (a *app) openRemoteBranchDelete(rb git.Branch) {
	st := remoteBranchMenuUI(a)
	st.rdOpen = true
	st.rdFull = rb.Name
	st.rdRemote = rb.Remote
	_, after, _ := strings.Cut(rb.Name, "/")
	st.rdBranch = after
	st.rdExpectOID = rb.Hash
}

// remoteBranchDeleteDialog is the confirmation dialog deleting a branch from
// its server. The confirmation only ever means "delete what I see now": the
// worker re-reads the tracking reference and refuses when it moved, and the
// delete itself goes out with a lease on that commit, so a branch someone
// else moved on the server is never deleted.
func (a *app) remoteBranchDeleteDialog(c *ui.Context) {
	p := a.pal
	st := remoteBranchMenuUI(a)
	if !st.rdOpen {
		return
	}
	ui.Modal(c.Key("rb-delete-modal"), &st.rdOpen, func() {
		ui.Column(c.Key("rb-delete-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Delete Remote Branch").FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Field(c, "Remote Branch:", func() {
				ui.Text(c, st.rdFull).Font("monospace").FontSize(12).TextColor(p.text).SingleLine().Ellipsis("…")
			})
			ui.Text(c, "This deletes '"+st.rdBranch+"' on the server '"+st.rdRemote+"'. Local branches tracking it lose their upstream.").FontSize(12).TextColor(p.muted).MaxWidth(424)
			ui.Row(c.Key("rb-delete-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("rb-delete-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeRemoteBranchDelete()
				}
				if ui.Button(c.Key("rb-delete-confirm"), "Delete").Label("Confirm Delete Remote Branch").Disabled(a.session.Busy() || a.session.Snapshot().Status.State != "").Clicked() {
					a.submitRemoteBranchDelete()
				}
			})
		})
	})
	if !st.rdOpen {
		a.closeRemoteBranchDelete()
	}
}

// closeRemoteBranchDelete closes the confirmation dialog and drops every
// value captured for it, so canceling, dismissing, submitting and switching
// repositories all end with no captured expectation surviving the dialog.
func (a *app) closeRemoteBranchDelete() {
	st := remoteBranchMenuUI(a)
	st.rdOpen = false
	st.rdFull = ""
	st.rdRemote = ""
	st.rdBranch = ""
	st.rdExpectOID = ""
}

// submitRemoteBranchDelete deletes the dialog's remote branch through the
// session's write path, on the sync repository copy whose executor asks
// through the in-memory askpass dialog — a push needs the server's
// credentials exactly like Push. The completion is dropped when it belongs
// to a repository the user has since switched away from.
func (a *app) submitRemoteBranchDelete() {
	st := remoteBranchMenuUI(a)
	if a.session.Busy() {
		return
	}
	remote := st.rdRemote
	branch := st.rdBranch
	expectOID := st.rdExpectOID
	a.closeRemoteBranchDelete()
	syncSt := syncStateFor(a)
	repo, gen := a.syncRepo()
	if repo == nil {
		return
	}
	syncSt.syncErr = ""
	a.session.ClearActionErr()
	syncSt.syncWorkerStarted()
	if !a.session.doThen("Deleting remote branch", func(*git.Repo) error {
		defer syncSt.syncWorkerDone()
		return deleteRemoteBranchWithGuard(repo, remote, branch, expectOID)
	}, func(err error) {
		if dropIfStale(a, gen) {
			return
		}
	}) {
		syncSt.syncWorkerDone()
	}
}

// deleteRemoteBranchWithGuard re-reads the remote branch's tracking
// reference before anything is pushed: when it no longer resolves or no
// longer points at the commit the dialog was opened against, the delete is
// refused and nothing is sent. When it is unchanged, that same commit is the
// lease expectation the delete goes out with, so a branch moved on the
// server after the last fetch is never deleted either — the push fails and
// the server keeps the branch. Push errors are redacted before returning so
// embedded remote URL credentials never reach any error display.
func deleteRemoteBranchWithGuard(repo *git.Repo, remote, branch, expectOID string) error {
	if remote == "" || branch == "" || strings.HasPrefix(remote, "-") || strings.ContainsAny(remote, " \t\r\n\x00") || strings.HasPrefix(branch, "-") || strings.HasSuffix(branch, "/") || strings.ContainsAny(branch, " \t\r\n\x00") {
		return fmt.Errorf("cannot delete the remote branch: invalid branch")
	}
	if expectOID == "" {
		return fmt.Errorf("cannot delete the remote branch: missing expected commit")
	}
	fullRef := "refs/remotes/" + remote + "/" + branch
	gotOID, err := repo.PathHash(fullRef + "^{commit}")
	if err != nil {
		return fmt.Errorf("cannot delete the remote branch: its tracking reference %s no longer resolves: %w", remote+"/"+branch, err)
	}
	if gotOID != expectOID {
		return fmt.Errorf("cannot delete the remote branch: %s/%s changed after the dialog opened; review the repository and try again", remote, branch)
	}
	if err := repo.DeleteRemoteBranchLease(remote, branch, gotOID); err != nil {
		return errors.New(git.RedactText(err.Error()))
	}
	return nil
}
