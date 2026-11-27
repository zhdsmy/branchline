package app

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// remoteUIState is the per-app state of the remote-management dialogs: the
// add draft, the edit draft and the delete confirmation, each capturing the
// remote's name and URL on the UI thread when opened, plus the repository
// generation they belong to. It lives outside the shared app struct, keyed
// by the app instance, exactly as the merge dialog state does.
type remoteUIState struct {
	repoGen uint64

	addOpen        bool
	addName        string
	addURL         string
	addFetch       bool
	addNameChanged bool
	addURLChanged  bool

	editOpen        bool
	editOld         string
	editExpectURL   string
	editExpectKnown bool
	editName        string
	editURL         string

	delOpen        bool
	delTarget      string
	delExpectURL   string
	delExpectKnown bool
}

var remoteUIStates = struct {
	sync.Mutex
	m map[*app]*remoteUIState
}{m: map[*app]*remoteUIState{}}

// remoteUI returns the app's remote-dialog state, creating it on first use
// and discarding every open draft when the repository generation moved past
// it: a switch or close makes the captured remote names and URLs meaningless,
// so nothing of the previous repository carries over.
func remoteUI(a *app) *remoteUIState {
	remoteUIStates.Lock()
	defer remoteUIStates.Unlock()
	st := remoteUIStates.m[a]
	if st == nil {
		st = &remoteUIState{}
		remoteUIStates.m[a] = st
	}
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		*st = remoteUIState{repoGen: gen}
	}
	return st
}

// remoteSnapshotURL reads the remote's fetch URL from the snapshot as the
// background refresh verified it. known is false when the URL read failed
// entirely or the remote has no verified entry: the write guards then refuse
// rather than trust an unknown URL.
func remoteSnapshotURL(a *app, name string) (url string, known bool) {
	urls := a.session.Snapshot().RemoteURLs
	if urls == nil {
		return "", false
	}
	url, known = urls[name]
	return url, known
}

// newRemoteRow is the New Remote entry at the head of the Remotes section.
// With no repository open there is nothing to add a remote to, so the entry
// is hidden entirely.
func (a *app) newRemoteRow(c *ui.Context) {
	if !a.session.HasRepo() {
		return
	}
	p := a.pal
	b := ui.ButtonBase(c.Key("remote-new-row")).
		Height(26).
		PaddingX(10).
		Gap(8).
		Radius(6).
		AlignItems(ui.Center).
		Justify(ui.Start).
		Label("New Remote").
		Tooltip("Add a new remote").
		FocusRing(false).
		Disabled(a.session.Busy())
	if b.Hovered() {
		b.Background(p.hover)
	}
	b.Children(func() {
		ui.Box(c).Width(13).Height(13).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Text(c, "+").FontSize(13).FontWeight(500).TextColor(p.muted).SingleLine()
		})
		ui.Text(c, "New Remote").FontSize(12.5).FontWeight(400).TextColor(p.muted).SingleLine().Shrink(1).MinWidth(0)
	})
	if b.Clicked() {
		a.openAddRemoteDialog()
	}
}

// openAddRemoteDialog opens the Add Remote dialog with a fresh draft: no
// name or URL, and fetch ticked as adding without the branches is the
// exception.
func (a *app) openAddRemoteDialog() {
	st := remoteUI(a)
	st.addOpen = true
	st.addName = ""
	st.addURL = ""
	st.addFetch = true
	st.addNameChanged = false
	st.addURLChanged = false
}

// remoteRow is one remote of the Remotes section: the same look as before —
// globe icon, name, same height and indent — now focusable, labeled for
// assistive technology, and carrying the remote's context menu.
func (a *app) remoteRow(c *ui.Context, remote string) {
	p := a.pal
	row := ui.Row(c.Key("rem-" + remote)).
		Height(26).
		PaddingX(10).
		Gap(8).
		Radius(6).
		AlignItems(ui.Center).
		Focusable().
		Role(ui.RoleNone).
		Label(remote)
	if row.Hovered() {
		row.Background(p.hover)
	}
	row.Children(func() {
		symbolIcon(c, "globe", fallbackRem, 13, p.faint)
		ui.Text(c, remote).FontSize(12.5).FontWeight(400).TextColor(p.text).SingleLine().Shrink(1).MinWidth(0)
	})
	row.ContextMenu(func(m *ui.Menu) {
		a.remoteMenuItems(m, remote)
	})
}

// remoteMenuItems is the context menu of one remote row: Edit and Delete,
// then a New Remote entry opening the same dialog the sidebar row does.
// Everything disables while a write is in flight.
func (a *app) remoteMenuItems(m *ui.Menu, remote string) {
	a.remoteFetchMenuItem(m, remote)
	busy := a.session.Busy()
	if m.Item("Edit '" + remote + "'…").Disabled(busy).Chosen() {
		a.openEditRemoteDialog(remote)
	}
	if m.Item("Delete '" + remote + "'…").Disabled(busy).Chosen() {
		a.openDeleteRemoteDialog(remote)
	}
	m.Separator()
	if m.Item("New Remote…").Disabled(busy).Chosen() {
		a.openAddRemoteDialog()
	}
	a.remoteWebMenuItems(m, remote)
}

// remoteNameProblem says why the typed name cannot name a remote, empty when
// it can. The rules mirror the new-branch rules: a remote name leads every
// remote-tracking reference it fetches, so it must be safe as one path
// segment of refs/remotes/<name>/…. RefNameProblem supplies the
// check-ref-format rules; on top of them a name starting with "-" would be
// read as options on git's command line, one holding whitespace would split
// into several arguments, and one ending in "/" is a directory, never a ref.
func remoteNameProblem(name string) string {
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
	return git.RefNameProblem(name)
}

// remoteURLProblem says why the typed URL cannot be added, empty when it can.
// Beyond empty, whitespace would split the URL into several arguments on
// git's command line.
func remoteURLProblem(url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return ""
	}
	if strings.ContainsAny(url, " \t\r\n") {
		return "URL cannot contain whitespace"
	}
	return ""
}

// addRemoteNameProblem is remoteNameProblem plus the collision check against
// the remotes the snapshot knows.
func (a *app) addRemoteNameProblem(name string) string {
	if p := remoteNameProblem(name); p != "" {
		return p
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	for _, remote := range a.session.Snapshot().Refs.Remotes {
		if remote == name {
			return "A remote named " + name + " already exists"
		}
	}
	return ""
}

// addRemoteProblem is the first reason the Add Remote dialog cannot submit:
// the name when it is empty or unusable, the URL when it is.
func (a *app) addRemoteProblem() string {
	st := remoteUI(a)
	name := strings.TrimSpace(st.addName)
	if name == "" {
		return "Enter a remote name"
	}
	if p := a.addRemoteNameProblem(name); p != "" {
		return p
	}
	url := strings.TrimSpace(st.addURL)
	if url == "" {
		return "Enter the remote URL"
	}
	return remoteURLProblem(url)
}

func (a *app) addRemoteCanSubmit() bool {
	return a.addRemoteProblem() == "" && !a.session.Busy()
}

// addRemoteDialog is the modal adding a remote. The inputs validate live as
// the user types and disable Add Remote; fetch stays ticked unless the user
// unticks it, and a failing fetch keeps the remote and reports the error.
func (a *app) addRemoteDialog(c *ui.Context) {
	p := a.pal
	st := remoteUI(a)
	if !st.addOpen {
		return
	}
	ui.Modal(c.Key("remote-add-modal"), &st.addOpen, func() {
		ui.Column(c.Key("remote-add-body")).Width(440).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Add Remote").FontSize(16).FontWeight(600).TextColor(p.text)
			ui.Field(c, "Name:", func() {
				in := ui.TextInput(c.Key("remote-add-name"), &st.addName).FillWidth().AutoFocus().Placeholder("Remote name").Label("Name")
				if in.Changed() {
					st.addNameChanged = true
				}
				if in.Submitted() && a.addRemoteCanSubmit() {
					a.submitAddRemote()
				}
			})
			ui.Field(c, "URL:", func() {
				in := ui.TextInput(c.Key("remote-add-url"), &st.addURL).FillWidth().Placeholder("Remote URL").Label("URL")
				if in.Changed() {
					st.addURLChanged = true
				}
				if in.Submitted() && a.addRemoteCanSubmit() {
					a.submitAddRemote()
				}
			})
			if problem := a.addRemoteProblem(); problem != "" && (st.addNameChanged || st.addURLChanged) {
				ui.Text(c, problem).FontSize(12).TextColor(p.danger)
			}
			ui.Checkbox(c, &st.addFetch, "Fetch after adding")
			ui.Row(c.Key("remote-add-actions")).Gap(8).Justify(ui.End).Children(func() {
				cancel := ui.Button(c.Key("remote-add-cancel"), "Cancel").Label("Cancel")
				if cancel.Clicked() {
					st.addOpen = false
				}
				add := ui.PrimaryButton(c.Key("remote-add-submit"), "Add Remote").Label("Add Remote").Disabled(!a.addRemoteCanSubmit())
				if add.Clicked() {
					a.submitAddRemote()
				}
			})
		})
	})
}

// submitAddRemote captures the dialog values on the UI thread, closes the
// dialog, and adds the remote through the session's write path.
func (a *app) submitAddRemote() {
	st := remoteUI(a)
	name := strings.TrimSpace(st.addName)
	url := strings.TrimSpace(st.addURL)
	fetch := st.addFetch
	st.addOpen = false
	st.addName = ""
	st.addURL = ""
	st.addNameChanged = false
	st.addURLChanged = false
	a.session.do("Add remote "+name, func(r *git.Repo) error {
		if err := guardRemoteAbsent(r, "add remote", name); err != nil {
			return err
		}
		return redactRemoteErr(r.RemoteAdd(name, url, fetch))
	})
}

// openEditRemoteDialog opens the Edit Remote dialog for the remote, capturing
// its verified URL on the UI thread so the write worker can refuse an edit
// the repository no longer matches.
func (a *app) openEditRemoteDialog(remote string) {
	url, known := remoteSnapshotURL(a, remote)
	st := remoteUI(a)
	st.editOpen = true
	st.editOld = remote
	st.editExpectURL = url
	st.editExpectKnown = known
	st.editName = remote
	st.editURL = url
}

// editRemoteProblem is the first reason the Edit Remote dialog cannot save.
// The collision check ignores the remote being edited: keeping its name is
// not a collision.
func (a *app) editRemoteProblem() string {
	st := remoteUI(a)
	name := strings.TrimSpace(st.editName)
	if name == "" {
		return "Enter a remote name"
	}
	if p := remoteNameProblem(name); p != "" {
		return p
	}
	for _, remote := range a.session.Snapshot().Refs.Remotes {
		if remote == name && remote != st.editOld {
			return "A remote named " + name + " already exists"
		}
	}
	url := strings.TrimSpace(st.editURL)
	if url == "" {
		return "Enter the remote URL"
	}
	return remoteURLProblem(url)
}

func (a *app) editRemoteCanSubmit() bool {
	return a.editRemoteProblem() == "" && !a.session.Busy()
}

// editRemoteDialog is the modal editing one remote's name and URL, both
// prefilled with what the remote is now: the user edits the real URL,
// credentials included, because it is the value git uses.
func (a *app) editRemoteDialog(c *ui.Context) {
	p := a.pal
	st := remoteUI(a)
	if !st.editOpen {
		return
	}
	ui.Modal(c.Key("remote-edit-modal"), &st.editOpen, func() {
		ui.Column(c.Key("remote-edit-body")).Width(440).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Edit Remote").FontSize(16).FontWeight(600).TextColor(p.text)
			ui.Field(c, "Name:", func() {
				in := ui.TextInput(c.Key("remote-edit-name"), &st.editName).FillWidth().AutoFocus().Placeholder("Remote name").Label("Name")
				if in.Submitted() && a.editRemoteCanSubmit() {
					a.submitEditRemote()
				}
			})
			ui.Field(c, "URL:", func() {
				in := ui.TextInput(c.Key("remote-edit-url"), &st.editURL).FillWidth().Placeholder("Remote URL").Label("URL")
				if in.Submitted() && a.editRemoteCanSubmit() {
					a.submitEditRemote()
				}
			})
			if problem := a.editRemoteProblem(); problem != "" && strings.TrimSpace(st.editName) != "" && strings.TrimSpace(st.editURL) != "" {
				ui.Text(c, problem).FontSize(12).TextColor(p.danger)
			}
			ui.Row(c.Key("remote-edit-actions")).Gap(8).Justify(ui.End).Children(func() {
				cancel := ui.Button(c.Key("remote-edit-cancel"), "Cancel").Label("Cancel")
				if cancel.Clicked() {
					st.editOpen = false
				}
				save := ui.PrimaryButton(c.Key("remote-edit-save"), "Save").Label("Save").Disabled(!a.editRemoteCanSubmit())
				if save.Clicked() {
					a.submitEditRemote()
				}
			})
		})
	})
}

// submitEditRemote captures the dialog values on the UI thread, closes the
// dialog, and edits the remote through the session's write path. The worker
// re-verifies the remote is still there with the URL the dialog captured:
// anything it changed into or onto after the dialog opened is never
// overwritten.
func (a *app) submitEditRemote() {
	st := remoteUI(a)
	old := st.editOld
	expectURL := st.editExpectURL
	expectKnown := st.editExpectKnown
	name := strings.TrimSpace(st.editName)
	url := strings.TrimSpace(st.editURL)
	st.editOpen = false
	a.session.do("Edit remote "+old, func(r *git.Repo) error {
		if err := guardRemoteUnchanged(r, old, expectURL, expectKnown); err != nil {
			return err
		}
		if name != old {
			if err := guardRemoteAbsent(r, "rename the remote to "+name, name); err != nil {
				return err
			}
		}
		return redactRemoteErr(r.EditRemote(old, name, url))
	})
}

// openDeleteRemoteDialog opens the confirmation dialog for deleting the
// remote, capturing its verified URL on the UI thread for the write guard.
func (a *app) openDeleteRemoteDialog(remote string) {
	url, known := remoteSnapshotURL(a, remote)
	st := remoteUI(a)
	st.delOpen = true
	st.delTarget = remote
	st.delExpectURL = url
	st.delExpectKnown = known
}

// deleteRemoteDialog is the confirmation dialog for deleting a remote. It
// shows the remote's URL with credentials scrubbed: the dialog confirms what
// is deleted without printing secrets. The actual guard runs in the worker.
func (a *app) deleteRemoteDialog(c *ui.Context) {
	p := a.pal
	st := remoteUI(a)
	if !st.delOpen {
		return
	}
	ui.Modal(c.Key("remote-delete-modal"), &st.delOpen, func() {
		ui.Column(c.Key("remote-delete-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Delete Remote").FontSize(16).FontWeight(600).TextColor(p.text)
			ui.Field(c, "Remote:", func() {
				ui.Text(c, st.delTarget).Font("monospace").FontSize(12).TextColor(p.text)
			})
			if st.delExpectKnown {
				ui.Field(c, "URL:", func() {
					ui.Text(c, git.RedactText(st.delExpectURL)).Font("monospace").FontSize(12).TextColor(p.text).Wrap()
				})
			}
			ui.Text(c, "Deleting this remote also deletes its remote-tracking branches. Local branches tracking them lose their upstream. The branches on the server are not touched.").FontSize(12).TextColor(p.muted).Wrap()
			ui.Row(c.Key("remote-delete-actions")).Gap(8).Justify(ui.End).Children(func() {
				cancel := ui.Button(c.Key("remote-delete-cancel"), "Cancel").Label("Cancel")
				if cancel.Clicked() {
					st.delOpen = false
				}
				confirm := ui.Button(c.Key("remote-delete-confirm"), "Delete").Label("Confirm Delete Remote").Disabled(a.session.Busy())
				if confirm.Clicked() {
					a.submitDeleteRemote()
				}
			})
		})
	})
}

// submitDeleteRemote deletes the dialog's target through the session's write
// path. The worker re-verifies on the repository itself that the remote is
// still there with the URL the dialog captured: a remote that changed or
// vanished since is never touched.
func (a *app) submitDeleteRemote() {
	st := remoteUI(a)
	target := st.delTarget
	expectURL := st.delExpectURL
	expectKnown := st.delExpectKnown
	st.delOpen = false
	if target == "" {
		return
	}
	a.session.do("Delete remote "+target, func(r *git.Repo) error {
		if err := guardRemoteUnchanged(r, target, expectURL, expectKnown); err != nil {
			return err
		}
		return redactRemoteErr(r.RemoteRemove(target))
	})
}

// remoteDialogs renders the remote-management dialogs at the end of the
// sidebar, as the other dialogs there.
func (a *app) remoteDialogs(c *ui.Context) {
	a.addRemoteDialog(c)
	a.editRemoteDialog(c)
	a.deleteRemoteDialog(c)
}

// guardRemoteUnchanged is the worker-side check for editing or deleting a
// remote: the remote must still exist with exactly the URL the UI thread
// captured when the dialog opened. A missing remote, an unreadable URL or a
// changed URL refuses before anything is modified; the refusal names the
// expected URL scrubbed of its credentials.
func guardRemoteUnchanged(r *git.Repo, remote, expectURL string, expectKnown bool) error {
	urls, err := r.RemoteURLs()
	if err != nil {
		return redactRemoteErr(fmt.Errorf("cannot modify remote %s: reading its remotes failed: %w", remote, err))
	}
	url, ok := urls[remote]
	if !ok {
		return fmt.Errorf("cannot modify remote %s: the remote no longer exists; review the repository and try again", remote)
	}
	if !expectKnown {
		return fmt.Errorf("cannot modify remote %s: its URL was not verified when the dialog opened; review the repository and try again", remote)
	}
	if url != expectURL {
		return fmt.Errorf("cannot modify remote %s: its URL changed after the dialog opened (was %s); review the repository and try again", remote, git.RedactText(expectURL))
	}
	return nil
}

// guardRemoteAbsent is the worker-side check that the name is still free,
// for adding a remote or renaming one onto it: a remote added elsewhere
// since the dialog opened is never overwritten or errored into. action says
// what was attempted, as in "add remote".
func guardRemoteAbsent(r *git.Repo, action, name string) error {
	urls, err := r.RemoteURLs()
	if err != nil {
		return redactRemoteErr(fmt.Errorf("cannot %s: reading the remotes failed: %w", action, err))
	}
	if _, exists := urls[name]; exists {
		return fmt.Errorf("cannot %s: a remote named %s already exists; review the repository and try again", action, name)
	}
	return nil
}

// redactRemoteErr scrubs credentials out of a git error before the session
// shows it: fetch and remote failures can echo the URL they ran against.
func redactRemoteErr(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(git.RedactText(err.Error()))
}
