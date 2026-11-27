package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// fallbackCloneIcon is the tray-and-arrow fallback of the Clone Repository
// switcher entry, defined here to keep ui_kit.go limited to shared components.
var fallbackCloneIcon = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 9.8v1.7A1.5 1.5 0 0 0 4.5 13h7a1.5 1.5 0 0 0 1.5-1.5V9.8"/><path d="M8 3v6.2M5.4 6.6L8 9.2l2.6-2.6"/></svg>`)

// pickCloneFolderFn shows the folder picker of the Clone Repository dialog's
// Choose button. It is a package-level variable so tests can substitute a
// picker that never shows the system dialog.
var pickCloneFolderFn = func(parent *mygo.Window) (string, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:    parent,
		Title:     "Clone Repository",
		Directory: true,
	})
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", nil
	}
	return paths[0], nil
}

// lastCloneParent is the parent folder of the last successful clone of this
// run, where the next Clone dialog starts. lastCloneSSHFolder and
// lastCloneSSHIdentity are the same memory for clones onto a server. They
// are read and written on the UI thread only.
var (
	lastCloneParent      string
	lastCloneSSHFolder   string
	lastCloneSSHIdentity string
)

// The clone locations the dialog offers: this Mac, or a server reached over
// SSH whose own git runs the clone.
const (
	cloneThisMac = iota
	cloneSSHServer
)

// cloneUIState is the Clone Repository dialog of one app instance: the form
// draft, the busy state with the cancel handle of the running clone, and the
// generation discarding results of a closed or reopened dialog. The URL and
// the folder name are one draft for both locations; the parent folder and
// the server folder with its identity each belong to their own, so switching
// the location never loses either.
type cloneUIState struct {
	open bool

	location       int
	url            string
	parent         string
	serverFolder   string
	serverIdentity string
	name           string
	nameEdited     bool
	err            string

	busy   bool
	cancel context.CancelFunc
	gen    int
}

var cloneUIStates = struct {
	sync.Mutex
	m map[*app]*cloneUIState
}{m: map[*app]*cloneUIState{}}

func (a *app) cloneUI() *cloneUIState {
	cloneUIStates.Lock()
	defer cloneUIStates.Unlock()
	st := cloneUIStates.m[a]
	if st == nil {
		st = &cloneUIState{}
		cloneUIStates.m[a] = st
	}
	return st
}

// showCloneDialog opens the Clone dialog with a fresh draft at this Mac:
// the parent folder starts at the folder of the last successful clone of
// this run, or the user's home folder, and the server draft starts at the
// server folder and identity of the last successful clone onto a server.
// Advancing the generation discards the completion of a clone started
// before the dialog was last closed.
func (a *app) showCloneDialog() {
	st := a.cloneUI()
	parent := lastCloneParent
	if parent == "" {
		if home, err := os.UserHomeDir(); err == nil {
			parent = home
		}
	}
	gen := st.gen + 1
	*st = cloneUIState{
		open:           true,
		parent:         parent,
		serverFolder:   lastCloneSSHFolder,
		serverIdentity: lastCloneSSHIdentity,
		gen:            gen,
	}
}

// cancelClone stops a running clone and invalidates its completion. The
// worker's clone removes the folder it started; nothing else is touched.
func (a *app) cancelClone(st *cloneUIState) {
	if st.cancel != nil {
		st.cancel()
		st.cancel = nil
	}
	st.gen++
}

// cloneModal is the Clone Repository dialog: the location choice between
// this Mac and a server reached over SSH, the URL, the parent folder with
// its folder picker — or the server's folder and identity — the folder name
// the URL fills in until the user edits it, and the running clone the
// Cancel button or Escape interrupts.
func (a *app) cloneModal(c *ui.Context) {
	st := a.cloneUI()
	if !st.open {
		return
	}
	p := a.pal
	ui.Modal(c.Key("clone-modal"), &st.open, func() {
		ui.Column(c).Width(520).Padding(20).Gap(14).Children(func() {
			ui.Text(c, "Clone Repository").FontSize(17).FontWeight(600)
			if ui.Segmented(c.Key("clone-location"), &st.location, "This Mac", "SSH Server").Label("Clone Location").Disabled(st.busy).Changed() {
				// The error of one location's failed clone does not describe
				// the form of the other.
				st.err = ""
			}
			ui.Field(c, "Repository URL", func() {
				urlInput := ui.TextInput(c.Key("clone-url"), &st.url).Placeholder("https://github.com/owner/repository.git").FillWidth().AutoFocus().Disabled(st.busy)
				if urlInput.Changed() && !st.nameEdited {
					st.name = git.RepoNameFromURL(st.url)
				}
			})
			if st.location == cloneSSHServer {
				ui.Text(c, "The server runs git clone, so this URL must be reachable from the server.").FontSize(12).TextColor(p.muted)
				ui.Field(c, "Server Folder", func() {
					ui.TextInput(c.Key("clone-server-folder"), &st.serverFolder).Placeholder("user@server:/srv/repos  or  ssh://user@server:2222/srv/repos").FillWidth().Disabled(st.busy)
				})
				ui.Field(c, "Identity file (optional)", func() {
					ui.TextInput(c.Key("clone-server-identity"), &st.serverIdentity).Placeholder("~/.ssh/id_ed25519").FillWidth().Disabled(st.busy)
				})
			} else {
				ui.Field(c, "Parent Folder", func() {
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
						ui.TextInput(c.Key("clone-parent"), &st.parent).Placeholder("~/code").FillWidth().Disabled(st.busy)
						if ui.Button(c.Key("clone-choose"), "Choose…").Disabled(st.busy).Clicked() {
							a.pickCloneFolder(st)
						}
					})
				})
			}
			ui.Field(c, "Folder Name", func() {
				nameInput := ui.TextInput(c.Key("clone-name"), &st.name).FillWidth().Disabled(st.busy)
				if nameInput.Changed() {
					st.nameEdited = true
				}
			})
			target := cloneTargetDisplay(st.parent, st.name)
			if st.location == cloneSSHServer {
				target = cloneSSHTargetDisplay(st.serverFolder, st.name)
			}
			ui.Text(c, "Clones into "+target).FontSize(12).TextColor(p.muted)
			if st.busy {
				ui.Row(c.Key("clone-status")).
					FillWidth().
					Role(ui.RoleStatus).
					Label("Clone Status").
					Busy(true).
					Gap(8).
					AlignItems(ui.Center).
					Children(func() {
						ui.Spinner(c)
						ui.Text(c, "Cloning…").FontSize(12.5).TextColor(p.muted)
					})
			} else if st.err != "" {
				ui.Text(c, st.err).FontSize(12.5).TextColor(p.danger)
			}
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c.Key("clone-cancel"), "Cancel").Clicked() {
					a.cancelClone(st)
					st.busy = false
					st.open = false
				}
				if ui.PrimaryButton(c.Key("clone-submit"), "Clone").Disabled(a.cloneDisabled(st)).Clicked() {
					a.runClone(st)
				}
			})
		})
	})
	if !st.open {
		// The dialog was dismissed (Escape, the backdrop, or Cancel): stop a
		// running clone like Cancel does.
		a.cancelClone(st)
		st.busy = false
	}
}

// cloneTargetDisplay is the "Clones into" preview line: the parent folder and
// the folder name joined, however partial the draft still is.
func cloneTargetDisplay(parent, name string) string {
	trimmed := strings.TrimRight(parent, "/")
	if trimmed == "" {
		if parent != "" {
			return "/" + name
		}
		return name
	}
	return trimmed + "/" + name
}

// cloneSSHTargetDisplay is the "Clones into" preview line of a server clone:
// the address of the server folder written as store.Repo.Address reads it
// back, with the folder name joined. A folder that does not parse yet shows
// as typed.
func cloneSSHTargetDisplay(folder, name string) string {
	folder = strings.TrimSpace(folder)
	if folder == "" {
		return name
	}
	if r, err := store.ParseAddress(folder); err == nil {
		return strings.TrimSuffix(r.Address(), "/") + "/" + name
	}
	return cloneTargetDisplay(folder, name)
}

// cloneDisabled reports the cases the Clone button stays disabled: no URL,
// no parent or server folder for the chosen location, or a folder name git
// clone should not be given.
func (a *app) cloneDisabled(st *cloneUIState) bool {
	if st.busy ||
		strings.TrimSpace(st.url) == "" ||
		git.CheckCloneName(strings.TrimSpace(st.name)) != nil {
		return true
	}
	if st.location == cloneSSHServer {
		return strings.TrimSpace(st.serverFolder) == ""
	}
	return strings.TrimSpace(st.parent) == ""
}

// pickCloneFolder runs the system folder picker in the background and puts
// the chosen folder into the parent field, as Add Local Repository does.
// Canceling the picker leaves the field unchanged, and a picker finishing
// after the dialog closed or reopened is dropped.
func (a *app) pickCloneFolder(st *cloneUIState) {
	win := a.win
	gen := st.gen
	a.spawn(func() {
		dir, err := pickCloneFolderFn(win)
		if err != nil || dir == "" {
			return
		}
		a.post(func() {
			if !st.open || st.gen != gen {
				return
			}
			st.parent = dir
		})
	})
}

// expandCloneHome expands a leading ~/ of the parent folder input, as the
// executors expand their directory.
func expandCloneHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + p[1:]
		}
	}
	return p
}

// runClone starts the clone of the dialog's draft in a background worker. The
// settings and the askpass handle are captured on the UI thread: the worker
// never reads the store or the dialog. A running clone cannot re-enter. A
// server clone starts in runCloneSSH, which shares this frame.
func (a *app) runClone(st *cloneUIState) {
	if st.busy {
		return
	}
	if st.location == cloneSSHServer {
		a.runCloneSSH(st)
		return
	}
	url := strings.TrimSpace(st.url)
	parentInput := strings.TrimSpace(st.parent)
	name := strings.TrimSpace(st.name)

	var prefs store.Settings
	if a.store != nil {
		prefs = a.store.Settings()
	}
	st2 := syncStateFor(a)
	ask := st2.ask

	ctx, cancel := context.WithCancel(context.Background())
	st.busy, st.cancel, st.err = true, cancel, ""
	st.gen++
	gen := st.gen

	st2.syncWorkerStarted()
	a.spawn(func() {
		defer cancel()
		defer st2.syncWorkerDone()
		err := cloneInto(ctx, url, parentInput, name, prefs, ask)
		a.post(func() {
			if !st.open || st.gen != gen {
				return
			}
			st.busy, st.cancel = false, nil
			if errors.Is(err, context.Canceled) {
				return
			}
			if err != nil {
				st.err = git.RedactText(git.ConnectionMessage(err))
				return
			}
			parent := expandCloneHome(parentInput)
			target := filepath.Join(parent, name)
			lastCloneParent = parent
			st.open = false
			a.addAndOpenRepo(store.Repo{Kind: store.Local, Path: target, Name: store.DefaultName(target)})
		})
	})
}

// cloneInto is the worker half of the clone: validate the parent folder,
// refuse an existing target without touching it, create the parent folder,
// and clone through an executor running in it. The returned error already
// reads for the user; the caller decides how much of it to show.
func cloneInto(ctx context.Context, url, parentInput, name string, prefs store.Settings, ask *git.AskPass) error {
	parent := expandCloneHome(parentInput)
	if !filepath.IsAbs(parent) {
		return fmt.Errorf("the parent folder must be an absolute path: %s", parentInput)
	}
	target := filepath.Join(parent, name)
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("%s already exists", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot look at %s: %w", target, err)
	}
	ex := store.Repo{Kind: store.Local, Path: parent}.ExecutorWith(prefs.GitPath, prefs.Trace(), prefs.EnvPath)
	if err := git.EnsureDir(ex); err != nil {
		return err
	}
	repo := repoWithAskPass(git.New(ex), ask)
	return repo.CloneInto(ctx, git.CloneOptions{URL: url, Name: name, Recurse: prefs.UpdateSubmodules})
}

// runCloneSSH starts the clone of the dialog's server draft in a background
// worker, as runClone does for this Mac. A server folder that does not read
// as an SSH address is an error of the draft itself: the dialog shows it
// without connecting.
func (a *app) runCloneSSH(st *cloneUIState) {
	url := strings.TrimSpace(st.url)
	folderInput := strings.TrimSpace(st.serverFolder)
	identity := strings.TrimSpace(st.serverIdentity)
	name := strings.TrimSpace(st.name)

	remote, err := store.ParseAddress(folderInput)
	if err != nil {
		st.err = git.RedactText(err.Error())
		return
	}
	remote.Identity = identity

	var prefs store.Settings
	if a.store != nil {
		prefs = a.store.Settings()
	}
	st2 := syncStateFor(a)
	ask := st2.ask

	ctx, cancel := context.WithCancel(context.Background())
	st.busy, st.cancel, st.err = true, cancel, ""
	st.gen++
	gen := st.gen

	st2.syncWorkerStarted()
	a.spawn(func() {
		defer cancel()
		defer st2.syncWorkerDone()
		top, err := cloneIntoSSH(ctx, url, remote, name, prefs, ask)
		a.post(func() {
			if !st.open || st.gen != gen {
				return
			}
			st.busy, st.cancel = false, nil
			if errors.Is(err, context.Canceled) {
				return
			}
			if err != nil {
				st.err = explainCloneSSHError(err)
				return
			}
			lastCloneSSHFolder = folderInput
			lastCloneSSHIdentity = identity
			st.open = false
			a.addAndOpenRepo(store.Repo{
				Kind:     store.SSH,
				Host:     remote.Host,
				User:     remote.User,
				Port:     remote.Port,
				Identity: identity,
				Path:     top,
				Name:     store.DefaultName(top),
			})
		})
	})
}

// cloneIntoSSH is the worker half of the server clone: create the parent
// folder on the server, clone into it through the executor of the parsed
// address, and read the clone's working tree root. The root is what the
// repository is saved as, rather than the address as typed: the server
// answers ~/ with the absolute home, and a path through a symlink with the
// canonical one.
func cloneIntoSSH(ctx context.Context, url string, remote store.Repo, name string, prefs store.Settings, ask *git.AskPass) (string, error) {
	ex := remote.ExecutorWith(prefs.GitPath, prefs.Trace(), prefs.EnvPath)
	repo := repoWithAskPass(git.New(ex), ask)
	if err := git.EnsureDir(repo.Exec); err != nil {
		return "", err
	}
	if err := repo.CloneInto(ctx, git.CloneOptions{URL: url, Name: name, Recurse: prefs.UpdateSubmodules}); err != nil {
		return "", err
	}
	child := remote
	child.Path = path.Join(remote.Path, name)
	topRepo := repoWithAskPass(git.New(child.ExecutorWith(prefs.GitPath, prefs.Trace(), prefs.EnvPath)), ask)
	top, err := topRepo.TopLevel()
	if err != nil {
		return "", err
	}
	return top, nil
}

// explainCloneSSHError writes a failed server clone for the dialog. What
// failed at connecting — the transport, the authentication, the host key —
// gets the guidance Add SSH Repository explains it with; everything else,
// such as the target folder git refused, reads as the connection message
// does, with credentials redacted either way.
func explainCloneSSHError(err error) string {
	if git.ExitCode(err) == 255 ||
		isSSHAuthFailure(err) ||
		classifySSHHostKeyFailure(err) != sshHostKeyNone {
		return explainSSHError(err)
	}
	return git.RedactText(git.ConnectionMessage(err))
}
