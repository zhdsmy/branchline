package app

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// fallbackRemoveIcon is the minus-in-circle fallback of the remove buttons,
// defined here to keep ui_kit.go limited to shared components.
var fallbackRemoveIcon = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="5.5"/><path d="M5.5 8h5"/></svg>`)

// pickLocalFolderFn shows the folder picker of "Add Local Repository". It is
// a package-level variable so tests can substitute a picker that never shows
// the system dialog.
var pickLocalFolderFn = func(parent *mygo.Window) (string, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:    parent,
		Title:     "Add Local Repository",
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

// reposUIState is the repository switcher's UI state of one app instance:
// the popup flag, the Add SSH form draft with its connection-check generation,
// the not-a-git-repository notice and the remove failure notice. The popup's
// repository rows are cached between frames: reposCacheRev is the store's
// revision the copy in reposCache was taken at, advanced by one so that the
// zero value can never match, and repoList keeps the virtual list's place.
type reposUIState struct {
	popupOpen bool

	sshOpen     bool
	sshAddress  string
	sshIdentity string
	sshName     string
	sshBusy     bool
	sshResult   string
	sshFailed   bool
	sshGen      int
	sshRequest  *sshFormRequest
	sshHostKey  sshHostKeyFailure

	notGitOpen bool
	notGitDir  string

	removeErr string

	reposCache    []store.Repo
	reposCacheRev uint64
	repoList      ui.ListState
}

// reposUI returns the app's repository switcher state, creating it on first
// use. It runs on the UI thread.
func (a *app) reposUI() *reposUIState {
	return featureState(a, func() *reposUIState { return &reposUIState{} })
}

// repoSwitcher is the sidebar-top repository switcher: a trigger
// showing the current repository, a popup listing every saved repository with
// remove buttons, and entries adding local and SSH repositories. The modals
// of the two add flows render here too, so they sit in the window-level
// overlay rather than the clipped sidebar.
func (a *app) repoSwitcher(c *ui.Context) {
	p := a.pal
	st := a.reposUI()
	// The popup reads the same rows every frame, so copying and sorting
	// them again only matters when the store says the list changed: the
	// revision asks cost nothing, the copy happens once per change.
	rev := a.store.ReposRev()
	if rev+1 != st.reposCacheRev {
		st.reposCache, rev = a.store.ReposWithRev()
		st.reposCacheRev = rev + 1
	}
	repos := st.reposCache
	title := "No Repository"
	iconName, fb := "folder", fallbackRepo
	if a.session.HasRepo() {
		meta := a.session.Meta()
		title = meta.Name
		if meta.Kind == store.SSH {
			iconName, fb = "server.rack", fallbackSSH
		}
	}

	trig := ui.ButtonBase(c.Key("repo-switcher")).
		Height(32).
		PaddingX(8).
		Gap(7).
		Radius(8).
		AlignItems(ui.Center).
		Justify(ui.Start).
		Label("Repository Switcher").
		Expanded(st.popupOpen).
		FocusRing(false)
	if trig.Hovered() || st.popupOpen {
		trig.Background(p.hover)
	}
	trig.Children(func() {
		symbolIcon(c, iconName, fb, 14, p.muted)
		ui.Text(c, title).FontSize(13).FontWeight(600).TextColor(p.text).SingleLine().Grow(1).Shrink(1).MinWidth(0)
		symbolIcon(c, "chevron.up.chevron.down", fallbackChev, 11, p.faint)
	})
	if trig.Clicked() {
		st.popupOpen = !st.popupOpen
		if st.popupOpen {
			// The closed popup forgets its rows, so the list starts from
			// the first row every time it opens, as the scroll container
			// it replaced did.
			st.repoList = ui.ListState{}
		}
	}

	ui.PopoverBase(c, trig, &st.popupOpen, func(panel ui.Element) {
		panel.Margin(4, 0, 0, 0).
			Padding(5).
			Gap(2).
			Width(268).
			Radius(10).
			Background(p.popoverBg).
			Border(0.5, p.popoverEdge).
			Shadow(0, 8, 22, -4, ui.RGBA(0, 0, 0, 0.22))
		panel.Children(func() {
			if len(repos) == 0 {
				ui.Text(c, "No repositories saved").FontSize(12.5).TextColor(p.muted).Padding(6, 8)
			} else {
				// The list builds only the rows in view plus its overscan
				// rather than every saved repository: the click handling
				// stays in each row's buttons, which a list that chooses
				// no rows leaves alone. The repository ID keys its row, so
				// rows coming and going around it keep its state.
				st.repoList.Key = func(row int) any { return repos[row].ID }
				ui.List(c.Key("repo-switcher-scroll"), &st.repoList, len(repos)).
					MaxHeight(300).Gap(2).
					Rows(func(row ui.ListRow) {
						repoRow(row.Context, a, repos[row.Index], st, p)
					})
			}
			ui.Box(c).Height(0.5).FillWidth().Background(p.hairline).Margin(4, 2)
			a.switcherFootButton(c, st, "Add Local Repository…", p)
			a.switcherFootButton(c, st, "Add SSH Repository…", p)
			a.switcherFootButton(c, st, "Clone Repository…", p)
		})
	})

	a.addSSHModal(c, st)
	a.notGitModal(c, st)
	a.removeErrorModal(c, st)
	a.cloneModal(c)
}

// repoRow is one repository of the switcher popup: a switch button labeled
// with the repository's name and a sibling remove button.
func repoRow(c *ui.Context, a *app, r store.Repo, st *reposUIState, p palette) {
	ui.Row(c.Key("repo-row-" + r.ID)).Gap(2).AlignItems(ui.Center).Children(func() {
		rowHeight := 30
		if r.Kind == store.SSH {
			rowHeight = 44
		}
		b := ui.ButtonBase(c.Key("repo-open-" + r.ID)).
			Height(float32(rowHeight)).
			PaddingX(8).
			Gap(8).
			Radius(6).
			AlignItems(ui.Center).
			Justify(ui.Start).
			Grow(1).
			Shrink(1).
			MinWidth(0).
			Clip().
			Label(r.Name).
			FocusRing(false)
		if b.Hovered() {
			b.Background(p.press)
		}
		b.Children(func() {
			check := ui.Box(c).Size(14, 14).Center().Shrink(0)
			if r.ID == a.activeRepoID() {
				check.Children(func() { symbolIcon(c, "checkmark", fallbackChk, 12, p.accent) })
			}
			iconName, fb := "folder", fallbackRepo
			iconColor := p.muted
			if r.Kind == store.SSH {
				iconName, fb = "server.rack", fallbackSSH
			}
			symbolIcon(c, iconName, fb, 13, iconColor)
			if r.Kind == store.SSH {
				// Two lines: the name with its SSH badge over the location, so
				// neither crowds the other nor the sibling remove button.
				ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
					ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, r.Name).FontSize(13).TextColor(p.text).SingleLine().Grow(1).Shrink(1).MinWidth(0)
						ui.Text(c, "SSH").FontSize(10.5).FontWeight(600).TextColor(p.muted).SingleLine().Shrink(0)
					})
					ui.Text(c, r.Location()).FontSize(10.5).TextColor(p.faint).SingleLine().Shrink(1).MinWidth(0)
				})
			} else {
				ui.Text(c, r.Name).FontSize(13).TextColor(p.text).SingleLine().Grow(1).Shrink(1).MinWidth(0)
			}
		})
		if b.Clicked() {
			a.switchRepoByID(r.ID)
			st.popupOpen = false
		}

		rm := ui.ButtonBase(c.Key("repo-remove-"+r.ID)).
			Size(24, 24).
			Center().
			Radius(6).
			Shrink(0).
			Label("Remove " + r.Name).
			Tooltip("Remove " + r.Name + " from the list (files stay)").
			FocusRing(false)
		if rm.Hovered() {
			rm.Background(p.hover)
		}
		rm.Children(func() {
			symbolIcon(c, "minus.circle", fallbackRemoveIcon, 13, p.faint)
		})
		if rm.Clicked() {
			a.removeRepo(st, r)
		}
	})
}

// switcherFootButton is one bottom entry of the switcher popup.
func (a *app) switcherFootButton(c *ui.Context, st *reposUIState, label string, p palette) {
	b := ui.ButtonBase(c.Key("switcher-" + label)).
		Height(30).
		PaddingX(8).
		Gap(8).
		Radius(6).
		AlignItems(ui.Center).
		Justify(ui.Start).
		Label(label).
		FocusRing(false)
	if b.Hovered() {
		b.Background(p.press)
	}
	iconName, fb := "folder.badge.plus", fallbackRepo
	if strings.Contains(label, "SSH") {
		iconName, fb = "server.rack", fallbackSSH
	} else if strings.Contains(label, "Clone") {
		iconName, fb = "tray.and.arrow.down", fallbackCloneIcon
	}
	b.Children(func() {
		symbolIcon(c, iconName, fb, 13, p.accent)
		ui.Text(c, label).FontSize(13).TextColor(p.text).SingleLine()
	})
	if b.Clicked() {
		st.popupOpen = false
		if strings.Contains(label, "SSH") {
			st.showSSHForm()
		} else if strings.Contains(label, "Clone") {
			a.showCloneDialog()
		} else {
			a.addLocalRepo(st)
		}
	}
}

// addLocalRepo picks a folder in the background, reduces it to the root of
// the repository it is in, saves and opens it. Canceling the picker is
// silent; a folder outside any repository opens the not-a-git notice. The
// saved settings are captured on the UI thread: the picker worker never reads
// the store.
func (a *app) addLocalRepo(st *reposUIState) {
	win := a.win
	var prefs store.Settings
	if a.store != nil {
		prefs = a.store.Settings()
	}
	a.spawn(func() {
		dir, err := pickLocalFolderFn(win)
		if err != nil || dir == "" {
			return
		}
		repo := git.New(store.Repo{Kind: store.Local, Path: dir}.ExecutorFor(prefs))
		top, topErr := repo.TopLevel()
		a.post(func() {
			if isNotRepository(topErr) {
				st.notGitOpen, st.notGitDir = true, dir
				return
			}
			if topErr != nil {
				a.toast = fmt.Sprintf("Cannot open %s: %s", dir, git.RedactText(topErr.Error()))
				return
			}
			a.addAndOpenRepo(store.Repo{Kind: store.Local, Path: top, Name: store.DefaultName(top)})
		})
	})
}

// addAndOpenRepo saves meta in the store and opens it, surfacing a
// persistence failure instead of opening an unsaved repository.
func (a *app) addAndOpenRepo(meta store.Repo) {
	if a.store != nil {
		saved, err := a.store.Add(meta)
		if err != nil {
			a.toast = "Cannot save the repository list: " + err.Error()
			return
		}
		meta = saved
	}
	a.openRepo(meta)
}

// removeRepo drops r from the repository list without touching any file. The
// active repository falling out of the list opens the first remaining one, or
// closes the session when the list runs empty.
func (a *app) removeRepo(st *reposUIState, r store.Repo) {
	if a.store == nil {
		return
	}
	wasActive := r.ID == a.activeRepoID()
	if err := a.store.Remove(r.ID); err != nil {
		st.removeErr = fmt.Sprintf("Cannot remove %s from the list: %s\n\nNothing was deleted; %s stays in the list.", r.Name, err.Error(), r.Name)
		return
	}
	if !wasActive {
		return
	}
	if rest := a.store.Repos(); len(rest) > 0 {
		a.openRepo(rest[0])
		return
	}
	a.session.Close()
	a.selectedRepoID = ""
	a.route = route{page: pageChanges}
	// The browsing history belongs to repository sessions; with none left
	// open it keeps nothing to return to, as openRepo clears it on a switch.
	a.history = nil
	a.forward = nil
}

// removeErrorModal shows why removing a repository from the list failed.
func (a *app) removeErrorModal(c *ui.Context, st *reposUIState) {
	if st.removeErr == "" {
		return
	}
	open := true
	ui.Modal(c.Key("remove-error-modal"), &open, func() {
		ui.Column(c).Width(440).Padding(20).Gap(12).Children(func() {
			ui.Text(c, "Cannot Remove Repository").FontSize(16).FontWeight(600)
			ui.Text(c, st.removeErr).FontSize(12.5).TextColor(a.pal.muted)
			ui.Row(c).FillWidth().Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Close").Clicked() {
					st.removeErr = ""
				}
			})
		})
	})
	if !open {
		st.removeErr = ""
	}
}

// notGitModal tells the user the folder chosen for Add Local Repository is
// not inside a Git repository.
func (a *app) notGitModal(c *ui.Context, st *reposUIState) {
	if !st.notGitOpen {
		return
	}
	ui.Modal(c.Key("not-git-modal"), &st.notGitOpen, func() {
		ui.Column(c).Width(440).Padding(20).Gap(12).Children(func() {
			ui.Text(c, "Not a Git Repository").FontSize(16).FontWeight(600)
			ui.Text(c, fmt.Sprintf("The '%s' directory is not a Git repository, and neither is any folder above it. Choose a folder inside a Git repository to add it.", st.notGitDir)).
				FontSize(12.5).TextColor(a.pal.muted)
			ui.Row(c).FillWidth().Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Close").Clicked() {
					st.notGitOpen = false
				}
			})
		})
	})
}

// sshFormRequest captures one Test Connection or Add click: the form values
// it ran with. Editing any field invalidates the in-flight request so a late
// answer cannot publish for inputs the user already changed.
type sshFormRequest struct {
	repo                    store.Repo
	address, identity, name string
	add                     bool
}

func (r *sshFormRequest) matches(st *reposUIState) bool {
	return r.address == strings.TrimSpace(st.sshAddress) &&
		r.identity == strings.TrimSpace(st.sshIdentity) &&
		r.name == strings.TrimSpace(st.sshName)
}

// showSSHForm opens the Add SSH dialog with a fresh draft, advancing the
// generation so answers of earlier checks cannot publish into it. The
// unrelated notices (not-a-git, remove failure) survive the reset.
func (st *reposUIState) showSSHForm() {
	gen := st.sshGen + 1
	notGitOpen, notGitDir := st.notGitOpen, st.notGitDir
	removeErr := st.removeErr
	*st = reposUIState{
		sshOpen:    true,
		sshGen:     gen,
		notGitOpen: notGitOpen,
		notGitDir:  notGitDir,
		removeErr:  removeErr,
	}
}

// reconcileSSHRequest invalidates the published result and the in-flight
// worker when the form's fields no longer match the request that started it.
func (st *reposUIState) reconcileSSHRequest() {
	if st.sshRequest != nil && !st.sshRequest.matches(st) {
		st.sshGen++
		st.sshRequest, st.sshHostKey = nil, sshHostKeyNone
		st.sshBusy, st.sshFailed, st.sshResult = false, false, ""
	}
}

// addSSHModal is the Add SSH Repository dialog with its Test Connection flow.
func (a *app) addSSHModal(c *ui.Context, st *reposUIState) {
	if !st.sshOpen {
		return
	}
	p := a.pal
	ui.Modal(c.Key("add-ssh-modal"), &st.sshOpen, func() {
		ui.Column(c).Width(520).Padding(20).Gap(14).Children(func() {
			ui.Text(c, "Add SSH Repository…").FontSize(17).FontWeight(600)
			ui.Text(c, "Branchline runs git on the server through your ssh, with your ~/.ssh/config, keys and agent. Verify new server keys in Terminal before connecting here.").
				FontSize(12).TextColor(p.muted)
			ui.Field(c, "Address", func() {
				ui.TextInput(c.Key("ssh-address"), &st.sshAddress).Placeholder("user@server:/srv/app  or  ssh://user@server:2222/srv/app").FillWidth().AutoFocus()
			})
			ui.Field(c, "Identity file (optional)", func() {
				ui.TextInput(c.Key("ssh-identity"), &st.sshIdentity).Placeholder("~/.ssh/id_ed25519").FillWidth()
			})
			ui.Field(c, "Name (optional)", func() {
				ui.TextInput(c.Key("ssh-name"), &st.sshName).Placeholder("The repository's folder").FillWidth()
			})
			st.reconcileSSHRequest()
			// The check's outcome is a live region: the label and the role
			// sit on the keyed outer container so the inner text keeps its own
			// accessible node and screen readers hear the result text itself
			// rather than the fixed label. Busy and success share the key so a
			// finished check announces politely; failures are alerts, which
			// speak on first appearance even without a busy phase before them.
			if st.sshBusy {
				ui.Row(c.Key("ssh-test-result")).
					FillWidth().
					Role(ui.RoleStatus).
					Label("Test Connection Result").
					Busy(true).
					Gap(8).
					AlignItems(ui.Center).
					Children(func() {
						ui.Spinner(c)
						ui.Text(c, "Connecting…").FontSize(12.5).TextColor(p.muted)
					})
			} else if st.sshResult != "" {
				color := ui.Hex("#2f9e4f")
				if p.dark {
					color = ui.Hex("#4cc38a")
				}
				role := ui.RoleStatus
				if st.sshFailed {
					color = p.danger
					role = ui.RoleAlert
				}
				ui.Box(c.Key("ssh-test-result")).
					FillWidth().
					Role(role).
					Label("Test Connection Result").
					Children(func() {
						ui.Text(c, st.sshResult).FontSize(12.5).TextColor(color)
					})
			}
			if st.sshFailed && st.sshHostKey != sshHostKeyNone && st.sshRequest != nil {
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, "Copy SSH Command").Disabled(st.sshBusy).Clicked() {
						c.WriteClipboard(sshHostKeyCommand(st.sshRequest.repo))
					}
					if ui.Button(c, "Retry").Disabled(st.sshBusy).Clicked() {
						a.checkSSHForm(st, st.sshRequest.add)
					}
				})
			}
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					st.sshGen++
					st.sshBusy = false
					st.sshOpen = false
				}
				if ui.Button(c, "Test Connection").Disabled(st.sshBusy || strings.TrimSpace(st.sshAddress) == "").Clicked() {
					a.checkSSHForm(st, false)
				}
				if ui.PrimaryButton(c, "Add").Disabled(st.sshBusy || strings.TrimSpace(st.sshAddress) == "").Clicked() {
					a.checkSSHForm(st, true)
				}
			})
		})
	})
	if !st.sshOpen {
		// The dialog was dismissed (Escape or click outside): invalidate any
		// in-flight request like Cancel does.
		st.sshGen++
		st.sshBusy = false
	}
}

// checkSSHForm connects to the server in the background, verifies the path is
// a Git repository with TopLevel and reads the server's git version. With add
// set, a successful check saves and opens the repository and closes the
// dialog; a test-only check only publishes the result in the dialog.
func (a *app) checkSSHForm(st *reposUIState, add bool) {
	st.reconcileSSHRequest()
	if st.sshBusy {
		return
	}
	r, err := store.ParseAddress(st.sshAddress)
	if err != nil {
		st.sshRequest, st.sshHostKey = nil, sshHostKeyNone
		st.sshResult, st.sshFailed = git.RedactText(err.Error()), true
		return
	}
	r.Identity = strings.TrimSpace(st.sshIdentity)
	r.Name = strings.TrimSpace(st.sshName)
	st.sshGen++
	request := &sshFormRequest{
		repo:     r,
		address:  strings.TrimSpace(st.sshAddress),
		identity: r.Identity,
		name:     r.Name,
		add:      add,
	}
	st.sshRequest = request
	// Without a test-injected opener the probe honors the saved settings: the
	// SSH executor keeps the server's git but applies the ENV PATH choice and
	// the trace switches. The settings are read on the UI thread, never by the
	// connection worker.
	var repo *git.Repo
	if a.openRepoFn == nil && a.store != nil {
		prefs := a.store.Settings()
		repo = git.New(r.ExecutorFor(prefs))
	} else {
		repo = a.buildGitRepo(r)
	}
	st.sshBusy, st.sshResult = true, ""
	gen := st.sshGen

	a.spawn(func() {
		top, err := repo.TopLevel()
		var version string
		if err == nil {
			version, _ = repo.Version()
		}
		a.post(func() {
			if !st.sshOpen || gen != st.sshGen || st.sshRequest != request {
				return // the dialog was closed or the form changed since
			}
			if !request.matches(st) {
				st.reconcileSSHRequest()
				return
			}
			st.sshBusy = false
			st.sshHostKey = classifySSHHostKeyFailure(err)
			if err != nil {
				st.sshResult, st.sshFailed = explainSSHError(err), true
				return
			}
			r.Path = top
			if r.Name == "" {
				r.Name = store.DefaultName(top)
			}
			st.sshResult, st.sshFailed = fmt.Sprintf("Connected: %s, repository at %s", version, top), false
			if add {
				if a.store == nil {
					st.sshResult, st.sshFailed = "Cannot save the repository: no store.", true
					return
				}
				saved, addErr := a.store.Add(r)
				if addErr != nil {
					st.sshResult, st.sshFailed = "Cannot save the repository list: "+addErr.Error(), true
					return
				}
				a.openRepo(saved)
				st.sshGen++
				st.sshOpen = false
			}
		})
	})
}

// sshHostKeyFailure is the kind of host key problem a connection error holds.
type sshHostKeyFailure uint8

const (
	sshHostKeyNone sshHostKeyFailure = iota
	sshHostKeyUnverified
	sshHostKeyChanged
	sshHostKeyRevoked
)

func classifySSHHostKeyFailure(err error) sshHostKeyFailure {
	if err == nil {
		return sshHostKeyNone
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "revoked host key"), strings.Contains(s, "host key is revoked"), strings.Contains(s, "host key ") && strings.Contains(s, "revoked by file"):
		return sshHostKeyRevoked
	case strings.Contains(s, "remote host identification has changed"):
		return sshHostKeyChanged
	case strings.Contains(s, "host key verification failed"), strings.Contains(s, "host key is known for"):
		return sshHostKeyUnverified
	}
	return sshHostKeyNone
}

func (k sshHostKeyFailure) guidance() string {
	switch k {
	case sshHostKeyChanged:
		return "The server's host key has changed. Verify the change independently with the server administrator before updating your trusted keys in Terminal. Branchline keeps the existing trust records. Return here and choose Retry after verification."
	case sshHostKeyRevoked:
		return "The server's host key has been revoked. Branchline refuses this connection. Contact the server administrator to verify a valid replacement; keep the revocation records. Return here and choose Retry only after the trust issue is resolved."
	default:
		return "SSH could not verify this server's host key. Copy SSH Command, run it in Terminal, and independently verify the server fingerprint before trusting it. Then return here and choose Retry. Branchline does not accept host keys for you."
	}
}

// sshHostKeyCommand builds the ssh command the user runs in Terminal to
// verify a server's host key. Only parsed connection fields enter it: the
// remote repository path and key contents never do.
func sshHostKeyCommand(r store.Repo) string {
	args := []string{"ssh", "-o", "StrictHostKeyChecking=ask"}
	if r.Port != 0 {
		args = append(args, "-p", strconv.Itoa(r.Port))
	}
	if r.Identity != "" {
		args = append(args, "-i", r.Identity)
	}
	if r.User != "" {
		args = append(args, "-l", r.User)
	}
	args = append(args, "--", r.Host)
	for i := range args {
		args[i] = quoteSSHCommandArg(args[i])
	}
	return strings.Join(args, " ")
}

func quoteSSHCommandArg(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,+@%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// explainSSHError adds what to do next to the errors connecting gives most.
func explainSSHError(err error) string {
	msg := git.RedactText(err.Error())
	lower := strings.ToLower(msg)
	if kind := classifySSHHostKeyFailure(err); kind != sshHostKeyNone {
		return msg + "\n\n" + kind.guidance()
	}
	switch {
	case isSSHAuthFailure(err):
		return msg + "\n\nThe server did not accept a key. Add one to ssh-agent (ssh-add), or fill in the Identity file field."
	case strings.Contains(lower, "not a git repository"):
		return msg + "\n\nNo Git repository is at this path on the server. Check the repository path on the server."
	case strings.Contains(lower, "no such file or directory"), strings.Contains(lower, "can't cd to"):
		// Debian/Ubuntu dash answers a cd into a missing directory with
		// "sh: 1: cd: can't cd to /path".
		return msg + "\n\nThis path does not exist on the server. Check the repository path on the server."
	case isGitNotFoundMessage(lower):
		return msg + "\n\nThe server has no git on its non-interactive PATH. Install git on the server, or make sure the non-interactive ssh PATH finds it."
	}
	return msg
}

// isGitNotFoundMessage recognizes the shells' git-missing lines: Bash and
// Dash print "git: command not found", BusyBox Ash prints "git: not found",
// and zsh prints "command not found: git".
func isGitNotFoundMessage(lower string) bool {
	return strings.Contains(lower, "git: command not found") ||
		strings.Contains(lower, "git: not found") ||
		strings.Contains(lower, "command not found: git")
}

func isSSHAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	var stderr string
	var ge *git.Error
	if errors.As(err, &ge) {
		stderr = git.RedactText(ge.Stderr)
	} else {
		stderr = git.RedactText(err.Error())
	}
	for _, rawLine := range strings.Split(stderr, "\n") {
		line := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(rawLine, "\r")))
		if line == "" {
			continue
		}
		if ge == nil {
			line = trimLeadingGitErrorPrefix(line)
		}
		line = trimCannotConnectPrefix(line)
		if isSSHAuthFailureLine(line) {
			return true
		}
	}
	return false
}

func trimLeadingGitErrorPrefix(line string) string {
	if strings.HasPrefix(line, "git: ") {
		return strings.TrimSpace(line[len("git: "):])
	}
	if !strings.HasPrefix(line, "git ") {
		return line
	}
	colon := strings.Index(line, ": ")
	if colon <= 4 {
		return line
	}
	sub := line[4:colon]
	for _, r := range sub {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return line
		}
	}
	return strings.TrimSpace(line[colon+2:])
}

func trimCannotConnectPrefix(line string) string {
	const prefix = "cannot connect to "
	if !strings.HasPrefix(line, prefix) {
		return line
	}
	rest := line[len(prefix):]
	colon := strings.Index(rest, ": ")
	if colon <= 0 {
		return line
	}
	dest := rest[:colon]
	if strings.ContainsAny(dest, " \t/'\"") {
		return line
	}
	return strings.TrimSpace(rest[colon+2:])
}

func isSSHAuthFailureLine(line string) bool {
	if idx := strings.Index(line, "permission denied ("); idx >= 0 {
		if !isSSHAuthTargetPrefix(line[:idx]) {
			return false
		}
		rest := line[idx+len("permission denied ("):]
		if !strings.HasSuffix(rest, ")") && !strings.HasSuffix(rest, ").") {
			return false
		}
		methods := strings.TrimSuffix(strings.TrimSuffix(rest, "."), ")")
		return isSSHAuthMethodList(methods)
	}
	if idx := strings.Index(line, "too many authentication failures"); idx >= 0 {
		prefix := line[:idx]
		if !isSSHAuthTargetPrefix(prefix) && !isSSHDisconnectPrefix(prefix) {
			return false
		}
		suffix := line[idx+len("too many authentication failures"):]
		switch suffix {
		case "", ".", " [preauth]", ". [preauth]":
			return true
		}
		return strings.HasPrefix(suffix, " for ") && !strings.ContainsAny(suffix, ":/'\"")
	}
	if idx := strings.Index(line, "no more authentication methods to try"); idx >= 0 {
		if !isSSHAuthTargetPrefix(line[:idx]) {
			return false
		}
		suffix := line[idx+len("no more authentication methods to try"):]
		return suffix == "" || suffix == "."
	}
	return false
}

func isSSHAuthTargetPrefix(prefix string) bool {
	if prefix == "" || prefix == "ssh: " {
		return true
	}
	if !strings.HasSuffix(prefix, ": ") {
		return false
	}
	principal := strings.TrimSuffix(prefix, ": ")
	at := strings.IndexByte(principal, '@')
	if at <= 0 || at == len(principal)-1 {
		return false
	}
	user, host := principal[:at], principal[at+1:]
	if strings.IndexByte(host, '@') >= 0 {
		return false
	}
	return !strings.ContainsAny(user, " \t/'\":") && !strings.ContainsAny(host, " \t/'\"")
}

func isSSHDisconnectPrefix(prefix string) bool {
	if !strings.HasSuffix(prefix, ": ") || strings.ContainsAny(prefix, "/'\"") {
		return false
	}
	return strings.HasPrefix(prefix, "received disconnect from ") || strings.HasPrefix(prefix, "disconnected from ")
}

func isSSHAuthMethodList(methods string) bool {
	if methods == "" {
		return false
	}
	for _, m := range strings.Split(methods, ",") {
		switch m {
		case "publickey", "password", "keyboard-interactive", "keyboard-interactive/pam",
			"gssapi-with-mic", "gssapi-keyex", "gssapi", "hostbased", "none":
		default:
			return false
		}
	}
	return true
}

// isNotRepository says the error is git's answer for a folder outside any
// repository, as opposed to a failure to run git.
func isNotRepository(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not a git repository")
}

// isMissingRepoError says a snapshot load failure means the repository's
// directory itself is gone — deleted or replaced by a plain file — rather
// than broken, unreadable or unreachable. It reads only the error text the
// failed load already produced: it never stats the disk or probes the
// server, so it is safe to call while a view draws.
//
// A local repository fails in git itself: "fatal: cannot change to '<dir>':
// No such file or directory" (or "Not a directory"). An SSH repository
// fails earlier, in the login shell's "cd <dir> && git …" prelude, whose
// wording differs per shell: Debian dash prints "sh: 1: cd: can't cd to
// /path", bash and macOS sh print "…: cd: /path: No such file or
// directory", zsh prints "zsh:cd:1: no such file or directory: /path" and
// BusyBox ash prints "sh: cd: line 1: can't cd to '/path': …".
//
// Anything else answers false: a corrupt repository ("not a git
// repository"), a directory without read permission, and the SSH transport
// layer (exit code 255 or "cannot connect to …", which covers missing local
// private keys — ssh's "Identity file … not accessible: No such file or
// directory" must not read as a missing remote path).
func isMissingRepoError(kind store.Kind, err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	var ge *git.Error
	if errors.As(err, &ge) && strings.TrimSpace(ge.Stderr) != "" {
		text = ge.Stderr
	}
	lower := strings.ToLower(text)

	if kind == store.SSH {
		if (ge != nil && ge.ExitCode == 255) || strings.Contains(lower, "cannot connect to ") {
			return false
		}
		for _, rawLine := range strings.Split(lower, "\n") {
			line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
			if line == "" {
				continue
			}
			if strings.Contains(line, ": permission denied") || strings.Contains(line, "permission denied:") {
				continue
			}
			if strings.Contains(line, "can't cd to") {
				return true
			}
			if (strings.Contains(line, "cd:") || strings.Contains(line, ":cd:")) &&
				(strings.Contains(line, "no such file or directory") || strings.Contains(line, "not a directory")) {
				return true
			}
		}
		return false
	}

	for _, rawLine := range strings.Split(lower, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if strings.Contains(line, "cannot change to") &&
			(strings.Contains(line, "no such file or directory") || strings.Contains(line, "not a directory")) {
			return true
		}
	}
	return false
}

// missingRepoPage draws the notice for an open repository whose directory is
// gone, replacing the raw git or shell error the Changes and History pages
// would otherwise print. It offers Retry, which reloads in place, and Remove
// from List, which only edits the repository list — no file, local or
// remote, is ever deleted here. It reports whether the notice applies and
// was drawn, so the caller skips the ordinary page content.
func (a *app) missingRepoPage(c *ui.Context) bool {
	if !a.session.HasRepo() || a.session.Loading() || a.session.LoadErr() == nil {
		return false
	}
	meta := a.session.Meta()
	if !isMissingRepoError(meta.Kind, a.session.LoadErr()) {
		return false
	}
	p := a.pal

	explanation := fmt.Sprintf("The folder for %q could not be found. It may have been moved or deleted.", meta.Name)
	if meta.Kind == store.SSH {
		explanation = fmt.Sprintf("The remote path for %q could not be found on %s. It may have been moved or deleted.", meta.Name, meta.Host)
	}

	top := a.pageTopInset(c)
	ui.Column(c.Key("missing-repo")).Fill().Padding(top, 24, 24, 24).Children(func() {
		ui.Column(c).Grow(1).Center().Padding(24).Gap(8).Children(func() {
			ui.Text(c, "Repository Not Found").FontSize(18).FontWeight(600).TextColor(p.text).TextAlign(ui.Center)
			ui.Text(c, explanation).FontSize(13).TextColor(p.muted).TextAlign(ui.Center).MaxWidth(480)
			ui.Text(c, git.RedactText(meta.Location())).Font("monospace").FontSize(12).TextColor(p.faint).TextAlign(ui.Center).MaxWidth(480)
			ui.Row(c).Gap(8).Center().Children(func() {
				if ui.Button(c.Key("missing-repo-retry"), "Retry").Clicked() {
					a.session.Reload()
				}
				// Remove is offered only for a repository the list still
				// holds; the removal itself reuses the switcher's path,
				// which never touches any file.
				if a.store != nil && meta.ID != "" {
					if saved, ok := a.store.Get(meta.ID); ok {
						if ui.PrimaryButton(c.Key("missing-repo-remove"), "Remove from List").
							Tooltip("Remove " + saved.Name + " from the list (files stay)").
							Clicked() {
							a.removeRepo(a.reposUI(), saved)
						}
					}
				}
			})
			ui.Text(c, "Removing only updates the repository list and does not delete any files.").FontSize(11.5).TextColor(p.faint).TextAlign(ui.Center).MaxWidth(480)
		})
	})
	return true
}
