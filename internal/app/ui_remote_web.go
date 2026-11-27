package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// The hosted web entries of the sidebar: View on '<service>' on remote rows
// and remote branch rows, Create Pull Request on '<service>' for a branch
// whose upstream the service hosts, and Push and Create Pull Request for a
// branch it does not have yet. Opening a page never runs a browser command:
// a menu choice or a finished push only records the target URL, and the end
// of the next sidebar frame hands it to ui.Context.OpenURLThen — or to the
// injected opener tests use to capture it instead of ever opening one.

// remoteWebUIState is the per-app state of the hosted web entries: the URL
// waiting to open, the Push and Create Pull Request draft with the branch,
// hash, remote and URL captured when the dialog opened, and the repository
// generation they belong to. The opener is the test seam: it survives
// repository switches so a test injecting it once captures every open
// regardless of which repository is open, while the production nil leaves
// opening to ui.Context.OpenURLThen.
type remoteWebUIState struct {
	repoGen uint64

	pendingURL string
	opener     func(string) error

	pushOpen       bool
	pushBranch     string
	pushExpectHash string
	pushRemotes    []pushPRRemote
	pushSelText    string
}

// pushPRRemote is one remote the Push and Create Pull Request dialog can
// target: its name, the service hosting it and the verified URL the snapshot
// read, captured so the write worker can refuse a remote changed since.
type pushPRRemote struct {
	name    string
	service string
	url     string
}

// remoteWebUI returns the app's hosted web state, creating it on first use
// and discarding the pending URL and the dialog draft when the repository
// generation moved past them: a switch or close must never open a page of
// the repository that was open before, and the draft's captured branch and
// URL are meaningless there.
func remoteWebUI(a *app) *remoteWebUIState {
	st := featureState(a, func() *remoteWebUIState { return &remoteWebUIState{} })
	if gen := a.session.Generation(); st.repoGen != gen {
		opener := st.opener
		*st = remoteWebUIState{repoGen: gen, opener: opener}
	}
	return st
}

// setRemoteWebOpener injects how URLs open for the app, nil restoring the
// production default of ui.Context.OpenURLThen. Tests inject a capture that
// never touches a browser.
func setRemoteWebOpener(a *app, fn func(string) error) {
	featureState(a, func() *remoteWebUIState { return &remoteWebUIState{} }).opener = fn
}

// openRemoteWebURL starts opening target in the browser. With an injected
// opener it runs at once, on the UI thread; without one the URL waits for
// the end of the sidebar frame, which owns a ui.Context. A failure shows as
// the dismissible sync error; nothing else is ever used to open a page.
func (a *app) openRemoteWebURL(target string) {
	if target == "" {
		return
	}
	st := remoteWebUI(a)
	if st.opener != nil {
		if err := st.opener(target); err != nil {
			a.session.ClearActionErr()
			syncStateFor(a).syncErr = git.RedactText(err.Error())
		}
		return
	}
	st.pendingURL = target
}

// remoteWebFrame runs at the end of the sidebar build: it renders the Push
// and Create Pull Request dialog and hands a URL waiting to open to this
// frame's ui.Context, the only place one is available.
func (a *app) remoteWebFrame(c *ui.Context) {
	a.pushPRDialog(c)
	st := remoteWebUI(a)
	target := st.pendingURL
	if target == "" {
		return
	}
	st.pendingURL = ""
	gen := a.session.Generation()
	c.OpenURLThen(target, func(err error) {
		if err != nil && !dropIfStale(a, gen) {
			a.session.ClearActionErr()
			syncStateFor(a).syncErr = git.RedactText(err.Error())
		}
	})
}

// clearPushPRDraft closes the Push and Create Pull Request dialog and drops
// every value captured when it opened.
func clearPushPRDraft(st *remoteWebUIState) {
	st.pushOpen = false
	st.pushBranch = ""
	st.pushExpectHash = ""
	st.pushRemotes = nil
	st.pushSelText = ""
}

// pushPRRemoteDisplay is how the dialog names a target remote: its name and
// the service hosting it.
func pushPRRemoteDisplay(r pushPRRemote) string {
	return r.name + " (" + r.service + ")"
}

// hostedRemotes are the remotes of the snapshot whose verified URL a known
// hosting service answers, in the snapshot's order.
func hostedRemotes(refs git.Refs, urls map[string]string) []pushPRRemote {
	var out []pushPRRemote
	for _, remote := range refs.Remotes {
		rawurl, known := urls[remote]
		if !known {
			continue
		}
		if repo, ok := parseHostedRepo(rawurl); ok {
			out = append(out, pushPRRemote{name: remote, service: repo.service, url: rawurl})
		}
	}
	return out
}

// upstreamRemote splits the branch's upstream into its remote and the branch
// name there by the longest remote prefix, as remote names can contain
// slashes themselves. known is false when no remote of the snapshot names it.
func upstreamRemote(refs git.Refs, upstream string) (remote, branch string, known bool) {
	for _, name := range refs.Remotes {
		prefix := name + "/"
		if strings.HasPrefix(upstream, prefix) && len(name) > len(remote) {
			remote, branch, known = name, upstream[len(prefix):], true
		}
	}
	return remote, branch, known
}

// remoteWebMenuItems appends the remote row's hosted web entries: View on
// '<service>' opens the repository's main page. It hides entirely when the
// remote has no verified URL or no known hosting service, and stays enabled
// during a write because opening a web page touches nothing.
func (a *app) remoteWebMenuItems(m *ui.Menu, remote string) {
	rawurl, known := remoteSnapshotURL(a, remote)
	if !known {
		return
	}
	repo, ok := parseHostedRepo(rawurl)
	if !ok {
		return
	}
	m.Separator()
	if m.Item("View on '" + repo.service + "'").Chosen() {
		a.openRemoteWebURL(repo.home())
	}
}

// remoteBranchWebMenuItems appends the remote branch row's hosted web
// entries: View on '<service>' opens the branch's page on the service, the
// branch name without its remote prefix. The synthetic remote HEAD never
// offers it.
func (a *app) remoteBranchWebMenuItems(m *ui.Menu, rb git.Branch) {
	if rb.Name == rb.Remote+"/HEAD" {
		return
	}
	rawurl, known := remoteSnapshotURL(a, rb.Remote)
	if !known {
		return
	}
	repo, ok := parseHostedRepo(rawurl)
	if !ok {
		return
	}
	m.Separator()
	if m.Item("View on '" + repo.service + "'").Chosen() {
		a.openRemoteWebURL(repo.branchPage(strings.TrimPrefix(rb.Name, rb.Remote+"/")))
	}
}

// branchWebMenuItems appends the local branch row's pull request entries.
// A branch tracking a branch of a hosted remote offers Create Pull Request
// on '<service>', which opens the new pull request page of the branch it
// tracks; a branch without a working upstream offers Push and Create Pull
// Request when any remote of the repository is hosted, opening the dialog
// that pushes the branch first. Neither depends on the write state for the
// opening entries; pushing disables while a write is in flight.
func (a *app) branchWebMenuItems(m *ui.Menu, br git.Branch) {
	refs := a.session.Snapshot().Refs
	if br.Upstream != "" && !br.Gone {
		if remote, branch, ok := upstreamRemote(refs, br.Upstream); ok {
			if rawurl, known := a.session.Snapshot().RemoteURLs[remote]; known {
				if repo, parsed := parseHostedRepo(rawurl); parsed {
					m.Separator()
					if m.Item("Create Pull Request on '" + repo.service + "'").Chosen() {
						a.openRemoteWebURL(repo.pullRequestPage(branch))
					}
					return
				}
			}
		}
	}
	if br.Upstream != "" && !br.Gone {
		return
	}
	if len(hostedRemotes(refs, a.session.Snapshot().RemoteURLs)) == 0 {
		return
	}
	m.Separator()
	if m.Item("Push and Create Pull Request…").Disabled(a.session.Busy()).Chosen() {
		a.openPushPRDialog(br)
	}
}

// openPushPRDialog opens the Push and Create Pull Request dialog for the
// branch, capturing on the UI thread everything the write worker re-verifies
// later: the branch name and hash, the hosted remotes with their verified
// URLs, and the default choice — origin when hosted, the first hosted remote
// otherwise.
func (a *app) openPushPRDialog(br git.Branch) {
	refs := a.session.Snapshot().Refs
	remotes := hostedRemotes(refs, a.session.Snapshot().RemoteURLs)
	if len(remotes) == 0 {
		return
	}
	st := remoteWebUI(a)
	st.pushOpen = true
	st.pushBranch = br.Name
	st.pushExpectHash = br.Hash
	st.pushRemotes = remotes
	st.pushSelText = pushPRRemoteDisplay(remotes[0])
	for _, r := range remotes {
		if r.name == "origin" {
			st.pushSelText = pushPRRemoteDisplay(r)
			break
		}
	}
}

// pushPRSelected is the remote the dialog's choice currently names, false
// when nothing matches — impossible while the dialog enforces its options,
// so the caller refuses rather than pushing anywhere.
func pushPRSelected(st *remoteWebUIState) (pushPRRemote, bool) {
	for _, r := range st.pushRemotes {
		if pushPRRemoteDisplay(r) == st.pushSelText {
			return r, true
		}
	}
	return pushPRRemote{}, false
}

// pushPRDialog is the confirmation dialog of Push and Create Pull Request:
// it names the branch, offers the choice of the hosted remotes when more
// than one is, and explains the push and the page that opens after it. The
// confirm button disables while a write is in flight; every value the write
// relies on was captured when the dialog opened and is re-verified in the
// worker.
func (a *app) pushPRDialog(c *ui.Context) {
	p := a.pal
	st := remoteWebUI(a)
	if !st.pushOpen {
		return
	}
	_, cancelled, confirmed := dialogFrame(c, p, "push-pr-modal", &st.pushOpen, "Push and Create Pull Request", 460, func() {
		ui.Field(c, "Branch:", func() {
			ui.Text(c, st.pushBranch).Font("monospace").FontSize(12).TextColor(p.text)
		})
		ui.Field(c, "Remote:", func() {
			if len(st.pushRemotes) == 1 {
				ui.Text(c, pushPRRemoteDisplay(st.pushRemotes[0])).FontSize(12).TextColor(p.text)
			} else {
				options := make([]string, len(st.pushRemotes))
				for i, r := range st.pushRemotes {
					options[i] = pushPRRemoteDisplay(r)
				}
				if _, ok := pushPRSelected(st); !ok {
					st.pushSelText = options[0]
				}
				ui.Select(c, &st.pushSelText, options).Label("Push PR Remote")
			}
		})
		service := ""
		if r, ok := pushPRSelected(st); ok {
			service = r.service
		}
		ui.Text(c, "This pushes the branch to the remote, sets it as the branch's upstream, and opens the new pull request page on "+service+" when the push succeeds.").FontSize(12).TextColor(p.muted).Wrap()
	},
		dialogAction{key: "push-pr-cancel", text: "Cancel", label: "Cancel"},
		dialogAction{key: "push-pr-confirm", text: "Push and Create Pull Request", label: "Confirm Push and Create Pull Request", disabled: a.session.Busy()})
	if cancelled {
		clearPushPRDraft(st)
	}
	if confirmed {
		a.submitPushPR()
	}
	if !st.pushOpen {
		clearPushPRDraft(st)
	}
}

// submitPushPR captures the dialog's choice on the UI thread, closes the
// dialog, and pushes through the session's write path with the askpass
// repository, exactly as the toolbar push does. Only a successful push of an
// unchanged branch to an unchanged remote opens the pull request page, and
// only when the repository is still the one the dialog belonged to.
func (a *app) submitPushPR() {
	if a.session.Busy() {
		return
	}
	st := remoteWebUI(a)
	remote, ok := pushPRSelected(st)
	if !ok {
		clearPushPRDraft(st)
		return
	}
	branch := st.pushBranch
	expectHash := st.pushExpectHash
	remoteName, expectURL := remote.name, remote.url
	clearPushPRDraft(st)

	syncSt := syncStateFor(a)
	repo, gen := a.syncRepo()
	if repo == nil {
		return
	}
	a.session.ClearActionErr()
	syncSt.syncErr = ""
	// prURL crosses from the worker to the completion through the closure,
	// exactly as the error of doThen does: the worker's return orders it
	// before the completion posts.
	var prURL string
	syncSt.syncWorkerStarted()
	if !a.session.doThen("Pushing "+branch, func(*git.Repo) error {
		defer syncSt.syncWorkerDone()
		url, err := pushPRWithGuard(repo, branch, expectHash, remoteName, expectURL)
		if err != nil {
			return err
		}
		prURL = url
		return nil
	}, func(err error) {
		if err != nil || dropIfStale(a, gen) {
			return
		}
		a.openRemoteWebURL(prURL)
	}) {
		syncSt.syncWorkerDone()
	}
}

// pushPRWithGuard is the worker-side verification of Push and Create Pull
// Request: the branch must still exist at the hash the dialog captured, and
// the remote must still exist with exactly the verified URL the dialog
// captured, a URL that still resolves to a pull request page. Any refusal
// returns before the push runs, so nothing is written and no page opens;
// the pull request URL is handed back only with a successful push.
func pushPRWithGuard(repo *git.Repo, branch, expectHash, remote, expectURL string) (string, error) {
	refs, err := repo.Refs()
	if err != nil {
		return "", fmt.Errorf("cannot push and create a pull request: reading the references failed: %w", err)
	}
	var found *git.Branch
	for i := range refs.Branches {
		if refs.Branches[i].Name == branch {
			found = &refs.Branches[i]
			break
		}
	}
	if found == nil {
		return "", fmt.Errorf("cannot push and create a pull request: the branch %s no longer exists; review the repository and try again", branch)
	}
	if found.Hash != expectHash {
		return "", fmt.Errorf("cannot push and create a pull request: the branch %s moved after the dialog opened; review the repository and try again", branch)
	}
	rawurl, err := repo.RemoteURL(remote)
	if err != nil {
		return "", fmt.Errorf("cannot push and create a pull request: the remote %s was removed or its URL cannot be read; review the repository and try again", remote)
	}
	if rawurl != expectURL {
		return "", fmt.Errorf("cannot push and create a pull request: the URL of remote %s changed after the dialog opened (was %s); review the repository and try again", remote, git.RedactText(expectURL))
	}
	hosted, ok := parseHostedRepo(rawurl)
	if !ok {
		return "", fmt.Errorf("cannot push and create a pull request: the URL of remote %s no longer names a hosted repository; review the repository and try again", remote)
	}
	if err := repo.Push(remote, branch, true, false, false); err != nil {
		return "", errors.New(git.RedactText(err.Error()))
	}
	return hosted.pullRequestPage(branch), nil
}
