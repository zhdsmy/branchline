package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// newRepoUIState is the New Repository dialog of one app instance: the form
// draft, the busy state of a running create, and the generation discarding
// the completion of a closed or reopened dialog. The parent folder and the
// server folder with its identity each belong to their own location, so
// switching the location never loses either.
type newRepoUIState struct {
	open bool

	location       int
	parent         string
	serverFolder   string
	serverIdentity string
	name           string
	branch         string
	err            string

	busy bool
	gen  int
}

func (a *app) newRepoUI() *newRepoUIState {
	return featureState(a, func() *newRepoUIState { return &newRepoUIState{} })
}

// showNewRepoDialog opens the New Repository dialog with a fresh draft at
// this Mac: the parent folder starts at the user's home folder and the
// initial branch at main. Advancing the generation discards the completion
// of a create started before the dialog was last closed.
func (a *app) showNewRepoDialog() {
	st := a.newRepoUI()
	parent := ""
	if home, err := os.UserHomeDir(); err == nil {
		parent = home
	}
	gen := st.gen + 1
	*st = newRepoUIState{
		open:   true,
		parent: parent,
		branch: "main",
		gen:    gen,
	}
}

// closeNewRepoDialog closes the dialog and invalidates the completion of a
// create still in flight: the repository it built stays where it was made,
// but the dropped result never opens it. The busy flag clears with the
// dialog; a reopened dialog starts a fresh draft anyway.
func (a *app) closeNewRepoDialog(st *newRepoUIState) {
	st.gen++
	st.busy = false
	st.open = false
}

// newRepoModal is the New Repository dialog: the location choice between
// this Mac and a server reached over SSH, the parent folder with its folder
// picker — or the server's folder and identity — the folder name and the
// initial branch of the repository git init creates. Create runs in the
// background; the dialog stays open on failure and closes over the freshly
// opened repository on success.
func (a *app) newRepoModal(c *ui.Context) {
	st := a.newRepoUI()
	if !st.open {
		return
	}
	p := a.pal
	_, cancelled, confirmed := dialogFrame(c, p, "new-repo", &st.open, "New Repository", 520, func() {
		if ui.Segmented(c.Key("new-repo-location"), &st.location, "This Mac", "SSH Server").Label("Repository Location").Disabled(st.busy).Changed() {
			// The error of one location's failed create does not describe the
			// form of the other.
			st.err = ""
		}
		if st.location == cloneSSHServer {
			ui.Field(c, "Server Folder", func() {
				ui.TextInput(c.Key("new-repo-server-folder"), &st.serverFolder).Placeholder("user@server:/srv/repos  or  ssh://user@server:2222/srv/repos").FillWidth().Disabled(st.busy)
			})
			ui.Field(c, "Identity file (optional)", func() {
				ui.TextInput(c.Key("new-repo-server-identity"), &st.serverIdentity).Placeholder("~/.ssh/id_ed25519").FillWidth().Disabled(st.busy)
			})
		} else {
			ui.Field(c, "Parent Folder", func() {
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					ui.TextInput(c.Key("new-repo-parent"), &st.parent).Placeholder("~/code").FillWidth().Disabled(st.busy)
					if ui.Button(c.Key("new-repo-choose"), "Choose…").Disabled(st.busy).Clicked() {
						a.pickNewRepoFolder(st)
					}
				})
			})
		}
		ui.Field(c, "Name", func() {
			ui.TextInput(c.Key("new-repo-name"), &st.name).Placeholder("The repository's folder").FillWidth().AutoFocus().Disabled(st.busy)
		})
		ui.Field(c, "Initial Branch", func() {
			ui.TextInput(c.Key("new-repo-branch"), &st.branch).FillWidth().Disabled(st.busy)
		})
		if st.busy {
			ui.Row(c.Key("new-repo-status")).
				FillWidth().
				Role(ui.RoleStatus).
				Label("New Repository Status").
				Busy(true).
				Gap(8).
				AlignItems(ui.Center).
				Children(func() {
					ui.Spinner(c)
					ui.Text(c, "Creating…").FontSize(12.5).TextColor(p.muted)
				})
		} else if st.err != "" {
			ui.Text(c, st.err).FontSize(12.5).TextColor(p.danger)
		}
	},
		dialogAction{key: "new-repo-cancel", text: "Cancel", label: "Cancel"},
		dialogAction{key: "new-repo-create", text: "Create", label: "Create", disabled: a.newRepoDisabled(st)})
	if cancelled {
		a.closeNewRepoDialog(st)
	}
	if confirmed {
		a.runNewRepo(st)
	}
	if !st.open {
		// The dialog was dismissed (Escape, the backdrop, or Cancel): the
		// completion of a create in flight must not open anything.
		a.closeNewRepoDialog(st)
	}
}

// newRepoDisabled reports the cases the Create button stays disabled: a
// create in flight, no parent or server folder for the chosen location, or
// a folder name git should not be given. The initial branch is not part of
// it: an empty or illegal branch is the error of the click, answered in the
// dialog.
func (a *app) newRepoDisabled(st *newRepoUIState) bool {
	if st.busy || git.CheckCloneName(strings.TrimSpace(st.name)) != nil {
		return true
	}
	if st.location == cloneSSHServer {
		return strings.TrimSpace(st.serverFolder) == ""
	}
	return strings.TrimSpace(st.parent) == ""
}

// pickNewRepoFolder runs the system folder picker in the background and puts
// the chosen folder into the parent field, sharing the picker helper with
// the Clone dialog. Canceling the picker leaves the field unchanged, and a
// picker finishing after the dialog closed or reopened is dropped.
func (a *app) pickNewRepoFolder(st *newRepoUIState) {
	gen := st.gen
	a.pickParentFolder("New Repository", func(dir string) {
		if !st.open || st.gen != gen {
			return
		}
		st.parent = dir
	})
}

// runNewRepo validates the dialog's draft and starts the create in a
// background worker. A folder or name whose edge is whitespace is refused
// rather than trimmed into a different path, and an initial branch of
// whitespace alone is an error of the draft: either way the dialog shows it
// and nothing is created or started. The settings and the askpass handle
// are captured on the UI thread: the worker never reads the store or the
// dialog.
func (a *app) runNewRepo(st *newRepoUIState) {
	if st.busy {
		return
	}
	onServer := st.location == cloneSSHServer
	if onServer {
		if msg := edgeSpaceError(st.serverFolder, "Server Folder"); msg != "" {
			st.err = msg
			return
		}
	} else {
		if msg := edgeSpaceError(st.parent, "Parent Folder"); msg != "" {
			st.err = msg
			return
		}
	}
	if msg := edgeSpaceError(st.name, "Name"); msg != "" {
		st.err = msg
		return
	}
	parentInput := strings.TrimSpace(st.parent)
	folderInput := strings.TrimSpace(st.serverFolder)
	identity := strings.TrimSpace(st.serverIdentity)
	name := strings.TrimSpace(st.name)
	branch := strings.TrimSpace(st.branch)
	if branch == "" {
		st.err = "Initial Branch cannot be empty."
		return
	}

	var remote store.Repo
	if onServer {
		var err error
		remote, err = store.ParseAddress(folderInput)
		if err != nil {
			st.err = git.RedactText(err.Error())
			return
		}
		remote.Identity = identity
	}

	var prefs store.Settings
	if a.store != nil {
		prefs = a.store.Settings()
	}
	st2 := syncStateFor(a)
	ask := st2.ask

	st.busy, st.err = true, ""
	st.gen++
	gen := st.gen

	st2.syncWorkerStarted()
	a.spawn(func() {
		defer st2.syncWorkerDone()
		top, err := initNewInto(onServer, parentInput, remote, name, branch, prefs, ask)
		a.post(func() {
			if !st.open || st.gen != gen {
				return
			}
			st.busy = false
			if err != nil {
				if onServer {
					st.err = explainCloneSSHError(err)
				} else {
					st.err = git.RedactText(git.ConnectionMessage(err))
				}
				return
			}
			st.open = false
			if onServer {
				a.addAndOpenRepo(store.Repo{
					Kind:     store.SSH,
					Host:     remote.Host,
					User:     remote.User,
					Port:     remote.Port,
					Identity: identity,
					Path:     top,
					Name:     store.DefaultName(top),
				})
			} else {
				a.addAndOpenRepo(store.Repo{Kind: store.Local, Path: top, Name: store.DefaultName(top)})
			}
		})
	})
}

// initNewInto is the worker half of the create: build the executor of the
// parent folder — of this Mac, or of the server the parsed address names —
// and let the git layer create the folder and the repository in it. The
// answered working tree root is what the repository is saved as, rather
// than the input path: the server answers ~/ with the absolute home, and a
// path through a symlink with the canonical one. The returned error already
// reads for the user; the caller decides how much of it to show.
func initNewInto(onServer bool, parentInput string, remote store.Repo, name, branch string, prefs store.Settings, ask *git.AskPass) (string, error) {
	var ex git.Executor
	if onServer {
		ex = repoWithAskPass(git.New(remote.ExecutorFor(prefs)), ask).Exec
	} else {
		parent := expandCloneHome(parentInput)
		if !filepath.IsAbs(parent) {
			return "", fmt.Errorf("the parent folder must be an absolute path: %s", parentInput)
		}
		ex = repoWithAskPass(git.New(store.Repo{Kind: store.Local, Path: parent}.ExecutorFor(prefs)), ask).Exec
	}
	return git.InitNew(ex, name, branch)
}

// initNewRepoHere initializes a Git repository on branch main inside the
// existing folder the not-a-git notice names, then adds and opens it. The
// folder's files stay exactly as they are — git init never touches them —
// so the notice asks no further confirmation. A failure is written back
// into the notice, which stays open; the completion of a create the notice
// outlived is dropped with its generation.
func (a *app) initNewRepoHere(st *reposUIState) {
	if st.notGitBusy {
		return
	}
	dir := st.notGitDir
	if dir == "" {
		return
	}
	var prefs store.Settings
	if a.store != nil {
		prefs = a.store.Settings()
	}
	st.notGitBusy, st.notGitErr = true, ""
	st.notGitGen++
	gen := st.notGitGen

	a.spawn(func() {
		repo := git.New(store.Repo{Kind: store.Local, Path: dir}.ExecutorFor(prefs))
		err := repo.Init("main")
		top := ""
		if err == nil {
			top, err = repo.TopLevel()
		}
		a.post(func() {
			if !st.notGitOpen || st.notGitGen != gen {
				return
			}
			st.notGitBusy = false
			if err != nil {
				st.notGitErr = git.RedactText(git.ConnectionMessage(err))
				return
			}
			st.notGitOpen = false
			a.addAndOpenRepo(store.Repo{Kind: store.Local, Path: top, Name: store.DefaultName(top)})
		})
	})
}
