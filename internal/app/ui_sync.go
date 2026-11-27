package app

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// fallbackXmark is the geometric fallback of the Dismiss Error close icon,
// kept in this file so ui_kit.go stays limited to shared components.
var fallbackXmark = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 4l8 8M12 4l-8 8"/></svg>`)

// syncState holds the toolbar sync and askpass dialog state on app.
type syncState struct{}

// syncUIState is the per-app sync state kept outside the shared app struct:
// the askpass dialog queue, its in-memory AskPass, and pre-flight validation
// errors that session.actionErr has no room for. Access is serialized through
// mutex-protected app lookups on the UI thread; the dialog itself is the only
// piece background workers touch, through the git.Prompter closure.
type syncUIState struct {
	pass        passDialog
	ask         *git.AskPass
	syncErr     string
	lastRepoGen uint64
	fetchDlg    fetchDialogState

	// mu guards the lifecycle counters background workers and the quit-time
	// teardown share. inflight counts running sync workers; closing is set by
	// closeSyncUI, after which the askpass script directory is removed exactly
	// once — immediately when no worker is in flight, or by the last worker
	// finishing, because a worker can still be writing the script directory
	// lazily (AskPass.scriptPath) while Close would read it without a lock.
	mu        sync.Mutex
	inflight  int
	closing   bool
	askClosed bool
}

// syncWorkerStarted records one more in-flight sync worker.
func (st *syncUIState) syncWorkerStarted() {
	st.mu.Lock()
	st.inflight++
	st.mu.Unlock()
}

// syncWorkerDone records the end of one in-flight sync worker; when teardown
// is waiting for it, the last worker to finish removes the askpass script
// directory — the only moment no worker can still be creating it lazily.
func (st *syncUIState) syncWorkerDone() {
	st.mu.Lock()
	st.inflight--
	closeAsk := st.closing && st.inflight == 0 && !st.askClosed
	if closeAsk {
		st.askClosed = true
	}
	st.mu.Unlock()
	if closeAsk && st.ask != nil {
		st.ask.Close()
	}
}

// finishSyncClose marks the sync state as closing and removes the askpass
// script directory when no worker is in flight, or defers that to the last
// worker finishing. It is idempotent and never runs while a worker can still
// race the script's lazy creation.
func (st *syncUIState) finishSyncClose() {
	st.mu.Lock()
	st.closing = true
	closeAsk := st.inflight == 0 && !st.askClosed
	if closeAsk {
		st.askClosed = true
	}
	st.mu.Unlock()
	if closeAsk && st.ask != nil {
		st.ask.Close()
	}
}

// askDirForTest returns the askpass script directory once it has been
// created, for tests asserting the teardown removes it. Creation is
// serialized by the once inside scriptPath, so after Env has returned the
// directory never changes again and no worker can still be assigning it.
func (st *syncUIState) askDirForTest() string {
	if st.ask == nil {
		return ""
	}
	env, err := st.ask.Env()
	if err != nil {
		return ""
	}
	for _, e := range env {
		if path, ok := strings.CutPrefix(e, "SSH_ASKPASS="); ok {
			return filepath.Dir(path)
		}
	}
	return ""
}

var syncUIStates = struct {
	sync.Mutex
	m map[*app]*syncUIState
}{m: map[*app]*syncUIState{}}

// syncStateFor returns the app's sync state, creating it — and its in-memory
// git.AskPass — on first use. It runs on the UI thread. Once closeSyncUI has
// retired the state the map holds a nil sentinel for the app: every later
// lookup returns that same retired state — whose dialog turns questions down
// and whose syncRepo hands out nothing — instead of building a fresh orphaned
// one behind the teardown.
func syncStateFor(a *app) *syncUIState {
	syncUIStates.Lock()
	defer syncUIStates.Unlock()
	st, ok := syncUIStates.m[a]
	if !ok {
		st = &syncUIState{}
		st.ask = git.NewAskPass(st.pass.prompter(a))
		syncUIStates.m[a] = st
	}
	if st == nil {
		// The state is retired; return a local closed state so the caller
		// sees a nil ask (no new syncs) without ever reviving the map entry.
		return &syncUIState{}
	}
	if gen := a.session.Generation(); gen != st.lastRepoGen {
		// Repository switched or closed: drop validation errors of the old
		// repository and cancel any parked askpass question so its worker is
		// released instead of waiting on a dialog of a repo that is gone.
		st.lastRepoGen = gen
		st.syncErr = ""
		st.pass.cancelAll()
		// The Fetch dialog's captured remotes belong to the old repository:
		// close it without fetching or remembering anything.
		st.fetchDlg = fetchDialogState{}
	}
	return st
}

// closeSyncUIDialog closes the askpass dialog of the app's sync state without
// touching the askpass script directory yet, and returns the state so the
// caller can finish the teardown after its workers drain. Closing the dialog
// first releases every worker parked on a question and turns questions
// arriving later down immediately, so the worker drain in test teardown
// cannot hang on a dialog nobody renders anymore. The map entry is retired
// to a nil sentinel, so syncStateFor never builds an orphaned live state
// behind the teardown.
func (a *app) closeSyncUIDialog() *syncUIState {
	syncUIStates.Lock()
	st := syncUIStates.m[a]
	// Retire the state unconditionally: a nil entry marks the closed sentinel,
	// so syncStateFor never builds an orphaned replacement behind teardown —
	// even if closeSyncUI runs before the first syncStateFor lookup.
	syncUIStates.m[a] = nil
	syncUIStates.Unlock()
	if st != nil {
		st.pass.close()
	}
	return st
}

// closeSyncUIAskPass removes the askpass script's temporary directory. It must
// run only after every background worker has drained: a worker still inside
// git.SSHExecutor.env or git.LocalExecutor.Run can be writing the very a.dir
// field AskPass.Close reads without a lock, and the script file must exist
// for as long as a command can still start with it.
func closeSyncUIAskPass(st *syncUIState) {
	if st != nil && st.ask != nil {
		st.ask.Close()
	}
}

// closeSyncUI is the product quit path, registered on mygo.App.OnQuit: it
// closes the askpass dialog so every worker parked on a question wakes with
// cancel, then removes the askpass script directory — at once when no sync
// worker is in flight, or through the last finishing worker when one is,
// because removing it while a worker can still create it lazily would race
// the lock-free read inside AskPass.Close. It is idempotent: a repeated call
// finds the dialog already closed and the directory removal already claimed.
func (a *app) closeSyncUI() {
	st := a.closeSyncUIDialog()
	if st == nil {
		return
	}
	st.finishSyncClose()
}

// syncNoteError is an operation that finished without an error but had nothing
// to transfer: pull found nothing to fetch or push had nothing to send. The
// note is shown as a toast instead of an error.
type syncNoteError struct{ note string }

func (e *syncNoteError) Error() string { return e.note }

func syncNote(err error) string {
	var n *syncNoteError
	if errors.As(err, &n) {
		return n.note
	}
	return ""
}

func noted(note string, err error) error {
	if err == nil && note != "" {
		return &syncNoteError{note: note}
	}
	return err
}

// syncToolbar builds the top-right toolbar: repository operation
// busy status, dismissible error status, and the Fetch, Pull, Push actions.
func (a *app) syncToolbar(c *ui.Context) {
	p := a.pal
	st := syncStateFor(a)

	if a.session.Busy() {
		label := a.session.BusyLabel()
		if label == "" {
			label = "Working"
		}
		ui.Row(c.Key("busy-status")).
			Height(24).
			PaddingX(8).
			Radius(6).
			Background(p.hover).
			AlignItems(ui.Center).
			Role(ui.RoleStatus).
			Label("Repository Status").
			Busy(true).
			Children(func() {
				ui.Text(c, label+"…").FontSize(12).TextColor(p.muted).SingleLine()
			})
	} else {
		errText := ""
		if err := a.session.ActionErr(); err != nil {
			errText = git.RedactText(err.Error())
		} else if st.syncErr != "" {
			errText = st.syncErr
		}
		if errText != "" {
			ui.Row(c.Key("action-err")).
				Height(24).
				Padding(0, 4, 0, 8).
				Gap(4).
				Radius(6).
				Background(p.danger.Alpha(0.14)).
				AlignItems(ui.Center).
				Role(ui.RoleStatus).
				Label("Repository Error").
				Children(func() {
					ui.Text(c, errText).FontSize(12).TextColor(p.danger).SingleLine().MaxWidth(240)
					dismiss := iconBtn(c.Key("dismiss-err"), "xmark", fallbackXmark, 10, p.danger, "Dismiss Error", p)
					if dismiss.Clicked() {
						a.session.ClearActionErr()
						st.syncErr = ""
					}
				})
		}
	}

	disabled := !a.session.HasRepo() || a.session.Busy()
	// The Fetch button opens the Fetch dialog; holding Option it fetches all
	// remotes as a click always did.
	fetch := toolbarActionBtnTip(c.Key("sync-fetch"), "arrow.triangle.2.circlepath", fallbackSync, "Fetch",
		"Fetch — hold Option to fetch all remotes directly", p, disabled)
	if fetch.Clicked() {
		if fetch.ClickModifiers()&ui.Alt != 0 {
			a.syncFetch()
		} else {
			a.openFetchDialog()
		}
	}
	pull := toolbarActionBtn(c.Key("sync-pull"), "arrow.down", fallbackDownArr, "Pull", p, disabled)
	if pull.Clicked() {
		a.syncPull()
	}
	// The Push button opens the Push dialog; holding Option it pushes the
	// current branch as a click always did.
	push := toolbarActionBtnTip(c.Key("sync-push"), "arrow.up", fallbackUpArr, "Push",
		"Push — hold Option to push the current branch directly", p, disabled)
	if push.Clicked() {
		if push.ClickModifiers()&ui.Alt != 0 {
			a.syncPush()
		} else {
			a.openPushDialog()
		}
	}

	// The askpass modal is built from the toolbar pass: ui.Modal mounts into
	// the window-level overlayRoot, so it is not clipped by the toolbar strip.
	// The Push and Fetch dialogs mount beneath it, so a question of their
	// workers can never hide behind the dialog it belongs to.
	a.pushDialog(c)
	a.fetchDialog(c)
	st.pass.view(c)
}

// syncRepo returns a repository copy whose executor asks through the in-memory
// askpass dialog, and captures the repository generation on the UI thread so
// completions of an older repository can be dropped.
func (a *app) syncRepo() (*git.Repo, uint64) {
	st := syncStateFor(a)
	repo := a.session.Repo()
	if repo == nil || st.ask == nil {
		return nil, a.session.Generation()
	}
	return repoWithAskPass(repo, st.ask), a.session.Generation()
}

// dropIfStale reports whether the completion arriving from a sync operation
// still belongs to the open repository; a stale one is dropped silently.
func dropIfStale(a *app, gen uint64) bool {
	return a.session.Generation() != gen
}

// The sync entry points return early when the session is busy: a click
// reaching them mid-operation must leave both the running operation's state
// and the displayed errors untouched.
func (a *app) syncFetch() {
	// Captured on the UI thread by value: the worker never reads the store.
	fetchTags := a.store.Settings().FetchTags
	a.syncFetchOpts(git.FetchOptions{All: true, Prune: true, Tags: fetchTags})
}

// syncFetchOpts runs one fetch with the given options through the session's
// sync lifecycle. A fetch aimed at one remote first confirms the remote is
// still configured — with git remote through refs — so a remote deleted
// after its fetch was asked for is never fetched, and the refusal is the
// error the toolbar shows.
func (a *app) syncFetchOpts(opts git.FetchOptions) {
	if a.session.Busy() {
		return
	}
	st := syncStateFor(a)
	repo, gen := a.syncRepo()
	if repo == nil {
		return
	}
	st.syncErr = ""
	st.syncWorkerStarted()
	if !a.session.doThen("Fetching", func(*git.Repo) error {
		defer st.syncWorkerDone()
		if opts.Remote != "" && !opts.All {
			refs, err := repo.Refs()
			if err != nil {
				return err
			}
			if !slices.Contains(refs.Remotes, opts.Remote) {
				return errors.New("The remote " + opts.Remote + " no longer exists.")
			}
		}
		return repo.FetchOpts(opts)
	}, func(err error) {
		if dropIfStale(a, gen) {
			return
		}
	}) {
		st.syncWorkerDone()
	}
}

func (a *app) syncPull() {
	if a.session.Busy() {
		return
	}
	st := syncStateFor(a)
	repo, gen := a.syncRepo()
	if repo == nil {
		return
	}
	st.syncErr = ""
	st.syncWorkerStarted()
	if !a.session.doThen("Pulling", func(*git.Repo) error {
		defer st.syncWorkerDone()
		note, err := repo.PullNote(git.PullOptions{Rebase: false})
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
	}
}

// pushTarget is the branch and remote a Push click sends. The worker resolves
// it from live refs so the decision never depends on a stale snapshot.
type pushTarget struct {
	branch      string
	remote      string
	setUpstream bool
}

// currentBranchOf returns the checked-out branch of refs, nil when detached.
func currentBranchOf(refs git.Refs) *git.Branch {
	for i := range refs.Branches {
		if refs.Branches[i].Current {
			return &refs.Branches[i]
		}
	}
	return nil
}

// defaultRemoteOf is v1's defaultRemote: origin when there is one, otherwise
// the first remote.
func defaultRemoteOf(refs git.Refs) string {
	for _, r := range refs.Remotes {
		if r == "origin" {
			return r
		}
	}
	if len(refs.Remotes) > 0 {
		return refs.Remotes[0]
	}
	return ""
}

// Push pre-flight validation messages. The unborn-branch text is checked
// first, so an empty repository or a not yet committed --orphan branch is
// never misread as a detached HEAD.
const (
	errMsgPushDetached = "HEAD is detached: check out a branch to push it."
	errMsgPushUnborn   = "Current branch has no commits yet: make a commit before pushing."
	errMsgPushNoRemote = "The repository has no remote to push to."
	errMsgPushNoBranch = "HEAD does not point to a local branch."
)

// snapshotHeadKind classifies the HEAD state from a loaded snapshot without
// running git: the refs half decides a born branch, and the status half — read
// in the same snapshot — separates an unborn branch (no head yet, but a branch
// name) from a detached HEAD (a head but no branch). The second return value
// is false for every inconsistent combination, so the caller falls through to
// the worker's live probe instead of trusting a snapshot torn by a concurrent
// checkout between its two reads.
func snapshotHeadKind(refs git.Refs, status git.Status) (git.HEADKind, bool) {
	if currentBranchOf(refs) != nil {
		return git.HEADBranch, true
	}
	if status.Head == "" {
		if status.Branch != "" && status.Branch != "(unknown)" {
			return git.HEADUnborn, true
		}
		return 0, false
	}
	if status.Branch == "" {
		return git.HEADDetached, true
	}
	return 0, false
}

// resolvePushTarget applies the v1 push decision to refs and the HEAD state:
// the source side is validated before the destination side, so an unborn
// branch reports before a missing remote exactly like a detached HEAD does. A
// branch with a live upstream pushes to it, a branch without one publishes to
// the default remote with upstream tracking, and an unborn branch, a detached
// HEAD, or a remoteless repository is a pre-flight validation error.
func resolvePushTarget(refs git.Refs, headKind git.HEADKind) (pushTarget, error) {
	switch headKind {
	case git.HEADUnborn:
		return pushTarget{}, errors.New(errMsgPushUnborn)
	case git.HEADDetached:
		return pushTarget{}, errors.New(errMsgPushDetached)
	}
	b := currentBranchOf(refs)
	if b == nil {
		return pushTarget{}, errors.New(errMsgPushNoBranch)
	}
	if b.Upstream != "" && !b.Gone {
		return pushTarget{branch: b.Name}, nil
	}
	remote := defaultRemoteOf(refs)
	if remote == "" {
		return pushTarget{}, errors.New(errMsgPushNoRemote)
	}
	return pushTarget{branch: b.Name, remote: remote, setUpstream: true}, nil
}

func (a *app) syncPush() {
	if a.session.Busy() {
		return
	}
	st := syncStateFor(a)
	repo, gen := a.syncRepo()
	if repo == nil {
		return
	}
	st.syncErr = ""
	a.session.ClearActionErr()

	// Pre-flight validation runs on the UI thread against the loaded snapshot,
	// mirroring v1: an unborn branch, a detached HEAD, or a remoteless
	// repository shows a dismissible error without starting git. The snapshot
	// is trusted only when the last load succeeded; a failed load — an SSH
	// repository whose encrypted key had no askpass yet, or a repository that
	// broke after opening — defers validation to the worker, which probes refs
	// and the HEAD state live through the askpass-enabled repository. An
	// inconsistent snapshot classification also falls through to the worker,
	// so a checkout landing between the snapshot's two reads is never misread.
	// The worker re-reads refs either way, so a branch checked out after the
	// last snapshot still publishes correctly and never to the wrong upstream.
	if a.session.Loaded() && a.session.LoadErr() == nil {
		snap := a.session.Snapshot()
		if headKind, ok := snapshotHeadKind(snap.Refs, snap.Status); ok {
			if _, err := resolvePushTarget(snap.Refs, headKind); err != nil {
				st.syncErr = err.Error()
				return
			}
		}
	}

	st.syncWorkerStarted()
	if !a.session.doThen("Pushing", func(*git.Repo) error {
		defer st.syncWorkerDone()
		refs, err := repo.Refs()
		if err != nil {
			return err
		}
		// A born branch never pays for the probe: only when no current branch
		// is among the refs does the worker classify HEAD live, so the hot
		// path adds no process or SSH round-trip. The probe preserves the
		// error of a corrupt repository or a dropped connection instead of
		// misreading it as unborn or detached.
		headKind := git.HEADBranch
		if currentBranchOf(refs) == nil {
			headKind, _, err = repo.HeadState()
			if err != nil {
				return err
			}
			if headKind == git.HEADBranch {
				// HEAD moved onto a born branch between Refs and HeadState:
				// re-read once so the target names that branch rather than a
				// stale or empty one.
				refs, err = repo.Refs()
				if err != nil {
					return err
				}
			}
		}
		target, err := resolvePushTarget(refs, headKind)
		if err != nil {
			return err
		}
		var note string
		if target.setUpstream {
			note, err = repo.PushNote(target.remote, target.branch, "", true, false, false)
		} else {
			note, err = repo.PushNote("", "", "", false, false, false)
		}
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
	}
}
