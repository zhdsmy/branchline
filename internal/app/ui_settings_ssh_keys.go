package app

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/sshkey"
	"github.com/zhdsmy/branchline/internal/store"
)

// sshKeyRow is one row of the SSH Keys settings list: a usable key pair with
// the state of its Use checkbox — the value the checkbox shows, moved ahead
// by a click, and the value last saved successfully, which a failed save
// reverts the checkbox to — or a stored key file that cannot be read, shown
// with its error instead of a checkbox.
type sshKeyRow struct {
	key     sshkey.Key
	broken  bool
	path    string // the stored path of a broken row
	openErr string
	shown   bool
	applied bool
}

func (r sshKeyRow) displayName() string {
	if r.broken {
		return filepath.Base(r.path)
	}
	return r.key.Name
}

// settingsSSHKeysState is the per-app state of the SSH Keys settings page:
// the merged key list with its checkbox states, the selected row, the drafts
// and background bookkeeping of the New SSH Key dialog and the Open Existing
// Key picker, and the clipboard text a copy button asked for. All fields are
// owned by the UI thread; background workers receive captured values only
// and publish through post.
//
// The three injectables replace the system integration points in tests —
// the file picker, the file manager reveal and the ssh-keygen program — so
// no test shows a system dialog, opens Finder or depends on where ssh-keygen
// is installed. Nil means the production implementation.
type settingsSSHKeysState struct {
	rows       []sshKeyRow
	selected   string
	listErr    string
	dirMissing bool

	saveErr string
	openErr string

	// removePending is the stored key file a Remove click asked to take off
	// the list. The removal itself runs at the start of the next frame, as
	// the Use saves do: rows cannot rebuild in the middle of the build that
	// is iterating them.
	removePending string

	newOpen  bool
	newName  string
	newEmail string
	newBusy  bool
	newErr   string
	newGen   uint64

	openBusy bool
	openGen  uint64

	clipboard    string
	hasClipboard bool

	pickKeyFileFn  func(win *mygo.Window) (string, error)
	showInFolderFn func(path string)
	keygenPathFn   func() string
}

// sshKeysStateOf returns the app's SSH Keys page state, creating it on first
// use. It runs on the UI thread.
func sshKeysStateOf(a *app) *settingsSSHKeysState {
	return featureState(a, func() *settingsSSHKeysState { return &settingsSSHKeysState{} })
}

func init() {
	registerSettingsPage(settingsPageDef{
		id:     "ssh-keys",
		title:  "SSH Keys",
		order:  25,
		onOpen: (*app).openSSHKeysSettings,
		view:   (*app).settingsSSHKeysPage,
	})
}

// keygenPath is the ssh-keygen program generation runs, the injected one in
// tests and the system one in production.
func (st *settingsSSHKeysState) keygenPath() string {
	if st.keygenPathFn != nil {
		return st.keygenPathFn()
	}
	if path, err := exec.LookPath("ssh-keygen"); err == nil {
		return path
	}
	return "/usr/bin/ssh-keygen"
}

// systemKeyFilePicker runs the system open dialog for Open Existing Key.
// It blocks until the user answers, so it runs in a background worker; the
// injected picker of a test takes its place, captured on the UI thread.
func systemKeyFilePicker(win *mygo.Window) (string, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: win,
		Title:  "Open SSH Key",
	})
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", nil
	}
	return paths[0], nil
}

// showInFolder reveals a key file in the file manager, through the injected
// function in tests and the system shell in production.
func (st *settingsSSHKeysState) showInFolder(path string) {
	if st.showInFolderFn != nil {
		st.showInFolderFn(path)
		return
	}
	mygo.Shell.ShowItemInFolder(path)
}

// sameKeyPath reports whether two paths name the same key file, as far as
// their spelling tells: the settings store canonical absolute paths, so a
// cleaned comparison is what deduplication needs.
func sameKeyPath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func containsKeyPath(paths []string, path string) bool {
	for _, p := range paths {
		if sameKeyPath(p, path) {
			return true
		}
	}
	return false
}

func dropKeyPath(paths []string, path string) []string {
	out := paths[:0]
	for _, p := range paths {
		if !sameKeyPath(p, path) {
			out = append(out, p)
		}
	}
	return out
}

// openSSHKeysSettings primes the page when the window opens on it or a click
// selects its tab: the key list reads fresh from disk, and whatever a
// previous visit left open or failing closes and clears.
func (a *app) openSSHKeysSettings() {
	st := sshKeysStateOf(a)
	st.newOpen = false
	st.newGen++
	st.openGen++
	st.openBusy = false
	st.saveErr = ""
	st.openErr = ""
	a.reloadSSHKeys()
}

// reloadSSHKeys merges the key pairs of the SSH folder with the stored key
// files opened from elsewhere, sorted by file name. Pairs of the folder win
// duplicates, stored files that no longer read become error rows, and a
// missing SSH folder is an empty list, not an error. The checkbox states
// prime from the saved active keys.
func (a *app) reloadSSHKeys() {
	st := sshKeysStateOf(a)
	prefs := a.store.Settings()
	st.rows = nil
	st.listErr = ""
	st.dirMissing = false

	var keys []sshkey.Key
	dir, err := sshkey.Dir()
	if err != nil {
		st.listErr = err.Error()
	} else {
		keys, err = sshkey.List(dir)
		if err != nil {
			st.listErr = err.Error()
		}
		if _, serr := os.Stat(dir); errors.Is(serr, fs.ErrNotExist) {
			st.dirMissing = true
		}
	}

	seen := map[string]bool{}
	for _, k := range keys {
		seen[filepath.Clean(k.Path)] = true
		st.rows = append(st.rows, sshKeyRow{key: k})
	}
	for _, file := range prefs.SSHKeyFiles {
		k, err := sshkey.Open(file)
		if err != nil {
			if !seen[filepath.Clean(file)] {
				seen[filepath.Clean(file)] = true
				st.rows = append(st.rows, sshKeyRow{broken: true, path: file, openErr: err.Error()})
			}
			continue
		}
		if seen[filepath.Clean(k.Path)] {
			continue
		}
		seen[filepath.Clean(k.Path)] = true
		st.rows = append(st.rows, sshKeyRow{key: k})
	}
	slices.SortFunc(st.rows, func(x, y sshKeyRow) int {
		return cmp.Compare(x.displayName(), y.displayName())
	})

	active := map[string]bool{}
	for _, p := range prefs.SSHKeys {
		active[filepath.Clean(p)] = true
	}
	for i := range st.rows {
		path := st.rows[i].path
		if !st.rows[i].broken {
			path = st.rows[i].key.Path
		}
		on := active[filepath.Clean(path)]
		st.rows[i].shown, st.rows[i].applied = on, on
	}

	// The selection survives a reload while its key still lists; a removed
	// key drops it.
	if st.selected != "" {
		found := false
		for _, r := range st.rows {
			if !r.broken && sameKeyPath(r.key.Path, st.selected) {
				found = true
				break
			}
		}
		if !found {
			st.selected = ""
		}
	}
}

// saveSSHKeySettings persists settings carrying SSH key changes and, on
// success, rebuilds the executor of the open repository at once, so a key
// just activated reaches the next fetch, pull, push or clone without
// reopening the repository.
func (a *app) saveSSHKeySettings(prefs store.Settings) error {
	if err := a.store.SetSettings(prefs); err != nil {
		return err
	}
	a.session.RebuildExecutor()
	return nil
}

// savePendingSSHKeyUse persists the one Use change a click can have made
// since the last frame. The full active list is recomputed from the rows:
// entries the list does not show — keys whose files vanished from the SSH
// folder, or stored files temporarily unreadable — keep their place instead
// of being dropped by an unrelated click. A failed write reverts the
// checkbox to the saved state before the controls of this frame build, so
// the unapplied choice never paints, and keeps the raw error on the page.
func (a *app) savePendingSSHKeyUse(st *settingsSSHKeysState) {
	for i := range st.rows {
		r := &st.rows[i]
		if r.broken || r.shown == r.applied {
			continue
		}
		pending := r.shown
		prefs := a.store.Settings()
		rowPaths := map[string]bool{}
		for _, rr := range st.rows {
			if !rr.broken {
				rowPaths[filepath.Clean(rr.key.Path)] = true
			}
		}
		var keys []string
		for _, p := range prefs.SSHKeys {
			if !rowPaths[filepath.Clean(p)] {
				keys = append(keys, p)
			}
		}
		for _, rr := range st.rows {
			if !rr.broken && rr.shown && !containsKeyPath(keys, rr.key.Path) {
				keys = append(keys, rr.key.Path)
			}
		}
		prefs.SSHKeys = keys
		if err := a.saveSSHKeySettings(prefs); err != nil {
			r.shown = r.applied
			st.saveErr = err.Error()
		} else {
			r.applied = pending
			st.saveErr = ""
		}
		return
	}
}

// settingsSSHKeysPage renders the SSH Keys page: the merged key list with
// its Use checkboxes, the details of the selected key with its copy and
// reveal buttons, and the New SSH Key and Open Existing Key entries.
func (a *app) settingsSSHKeysPage(c *ui.Context, p palette) {
	st := sshKeysStateOf(a)
	// The clipboard write of a copy clicked on the previous frame goes out
	// here, where the frame's context is available, as the copy menu items
	// of the branch list do.
	if st.hasClipboard {
		c.WriteClipboard(st.clipboard)
		st.clipboard = ""
		st.hasClipboard = false
	}
	if st.removePending != "" {
		pending := st.removePending
		st.removePending = ""
		a.removeSSHKeyFile(pending)
	}
	a.savePendingSSHKeyUse(st)

	ui.Scroll(c.Key("settings-ssh-keys-scroll")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Column(c.Key("settings-ssh-keys")).FillWidth().Gap(10).Padding(0, 0, 8, 0).Children(func() {
			if st.listErr != "" {
				ui.Text(c, st.listErr).FontSize(11.5).TextColor(p.danger).MaxWidth(400)
			}
			if len(st.rows) == 0 && st.listErr == "" {
				empty := "No SSH keys found. Create one with New SSH Key… or open an existing one with Open Existing Key…"
				if st.dirMissing {
					empty = "There is no ~/.ssh folder yet. Create a key with New SSH Key… or open an existing one with Open Existing Key…"
				}
				ui.Text(c, empty).FontSize(12).TextColor(p.muted).MaxWidth(400)
			}
			for i := range st.rows {
				a.sshKeyRowView(c, p, st, i)
			}
			a.sshKeyDetailView(c, p, st)

			ui.Row(c.Key("ssh-keys-actions")).FillWidth().Justify(ui.Start).Gap(8).Children(func() {
				if ui.Button(c.Key("ssh-key-new"), "New SSH Key…").Label("New SSH Key").Clicked() {
					a.openNewSSHKeyDialog()
				}
				openBtn := ui.Button(c.Key("ssh-key-open"), "Open Existing Key…").Label("Open Existing Key").Disabled(st.openBusy)
				if openBtn.Clicked() {
					a.openExistingSSHKey()
				}
			})
			if st.openErr != "" {
				ui.Text(c, st.openErr).FontSize(11.5).TextColor(p.danger).MaxWidth(400)
			}
			if st.saveErr != "" {
				ui.Text(c, st.saveErr).FontSize(11.5).TextColor(p.danger).MaxWidth(400)
			}
		})
	})

	a.newSSHKeyDialog(c, p)
}

// sshKeyRowView renders one row of the key list: the Use checkbox and the
// key's name, type and fingerprint as one selectable row, or — for a stored
// file that does not read — the error and the button taking it off the list.
func (a *app) sshKeyRowView(c *ui.Context, p palette, st *settingsSSHKeysState, i int) {
	r := &st.rows[i]
	if r.broken {
		ui.Row(c.Key("ssh-key-broken-" + r.path)).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				ui.Text(c, r.displayName()).FontSize(13).TextColor(p.text).SingleLine()
				ui.Text(c, r.openErr).FontSize(11).TextColor(p.danger).MaxWidth(340)
			})
			if ui.Button(c.Key("ssh-key-remove-"+r.path), "Remove").Label("Remove " + r.displayName() + " from the list").Clicked() {
				st.removePending = r.path
			}
		})
		return
	}

	k := r.key
	ui.Row(c.Key("ssh-key-row-" + k.Path)).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Checkbox(c.Key("ssh-key-use-"+k.Path), &r.shown, "Use").Label("Use " + k.Name)
		b := ui.ButtonBase(c.Key("ssh-key-select-" + k.Path)).
			Height(34).
			PaddingX(6).
			Radius(6).
			Grow(1).
			Shrink(1).
			MinWidth(0).
			AlignItems(ui.Center).
			Justify(ui.Start).
			Label(k.Name)
		if sameKeyPath(st.selected, k.Path) {
			b.Background(p.selected)
		} else if b.Hovered() {
			b.Background(p.hover)
		}
		b.Children(func() {
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, k.Name).FontSize(13).TextColor(p.text).SingleLine().Shrink(1).MinWidth(0)
					ui.Text(c, strings.TrimPrefix(k.Type, "ssh-")).FontSize(10.5).TextColor(p.faint).SingleLine().Shrink(0)
				})
				ui.Text(c, k.Fingerprint).Font("monospace").FontSize(10.5).TextColor(p.faint).SingleLine().Shrink(1).MinWidth(0)
			})
		})
		if b.Clicked() {
			st.selected = k.Path
		}
	})
}

// sshKeyDetailView renders the details of the selected key below the list:
// the comment and the full public key, selectable for copying by hand, and
// the buttons copying it to the clipboard and revealing the private key in
// the file manager.
func (a *app) sshKeyDetailView(c *ui.Context, p palette, st *settingsSSHKeysState) {
	if st.selected == "" {
		return
	}
	var sel *sshKeyRow
	for i := range st.rows {
		if !st.rows[i].broken && sameKeyPath(st.rows[i].key.Path, st.selected) {
			sel = &st.rows[i]
			break
		}
	}
	if sel == nil {
		return
	}
	k := sel.key
	ui.Column(c.Key("ssh-key-detail")).Gap(6).Padding(6, 0, 0, 26).Children(func() {
		if k.Comment != "" {
			ui.Text(c, k.Comment).FontSize(12).TextColor(p.muted).Selectable()
		}
		ui.Text(c, k.Public).Font("monospace").FontSize(11).TextColor(p.muted).Selectable().MaxWidth(440)
		ui.Row(c.Key("ssh-key-detail-actions")).Gap(8).Justify(ui.Start).Children(func() {
			if ui.Button(c.Key("ssh-key-copy"), "Copy Public Key").Label("Copy Public Key").Clicked() {
				st.clipboard = k.Public
				st.hasClipboard = true
			}
			path := k.Path
			if ui.Button(c.Key("ssh-key-reveal"), "Show in Finder").Label("Show in Finder").Clicked() {
				st.showInFolder(path)
			}
		})
	})
}

// removeSSHKeyFile takes a stored key file off the list: out of the stored
// files and out of the active keys. A failed write keeps the row, as nothing
// changed, and shows the raw error on the page.
func (a *app) removeSSHKeyFile(path string) {
	st := sshKeysStateOf(a)
	prefs := a.store.Settings()
	prefs.SSHKeyFiles = dropKeyPath(prefs.SSHKeyFiles, path)
	prefs.SSHKeys = dropKeyPath(prefs.SSHKeys, path)
	if err := a.saveSSHKeySettings(prefs); err != nil {
		st.saveErr = err.Error()
		return
	}
	st.saveErr = ""
	a.reloadSSHKeys()
}

// defaultSSHKeyName is the file name the New SSH Key dialog starts with:
// id_ed25519, numbered up while a file of that name — either half of the
// pair — already sits in the SSH folder. If dir cannot be queried (for
// example due to permissions or a non-directory path), it stops and returns
// the base name so generation surfaces the real filesystem error.
func defaultSSHKeyName(dir string) string {
	for i := 0; ; i++ {
		name := "id_ed25519"
		if i > 0 {
			name = "id_ed25519_" + strconv.Itoa(i)
		}
		priv := filepath.Join(dir, name)
		_, errPriv := os.Lstat(priv)
		_, errPub := os.Lstat(priv + ".pub")
		if errors.Is(errPriv, fs.ErrNotExist) && errors.Is(errPub, fs.ErrNotExist) {
			return name
		}
		if (errPriv != nil && !errors.Is(errPriv, fs.ErrNotExist)) || (errPub != nil && !errors.Is(errPub, fs.ErrNotExist)) {
			return "id_ed25519"
		}
	}
}

// openNewSSHKeyDialog opens the New SSH Key dialog with a fresh draft: the
// first free numbered name and the email the commit settings know, which
// becomes the key's comment.
func (a *app) openNewSSHKeyDialog() {
	st := sshKeysStateOf(a)
	prefs := a.store.Settings()
	dir, err := sshkey.Dir()
	name := "id_ed25519"
	if err == nil {
		name = defaultSSHKeyName(dir)
	}
	st.newOpen = true
	st.newName = name
	st.newEmail = prefs.AuthorEmail
	st.newErr = ""
	st.newBusy = false
	st.newGen++
}

// generateSSHKey runs ssh-keygen in the background with the dialog's draft,
// newSSHKeyCanGenerate reports whether the New SSH Key dialog can start a
// generation now: no generation in flight and a file name passing the key
// name rules. The Generate Key button's disabled state mirrors it exactly,
// and so do the Enter submissions of the dialog's inputs.
func newSSHKeyCanGenerate(st *settingsSSHKeysState) bool {
	return !st.newBusy && sshkey.CheckName(st.newName) == nil
}

// captured on the UI thread. A failure — a name taken since the dialog
// opened among them — shows in the dialog, which stays open; a success
// closes it, activates the new key and selects it.
func (a *app) generateSSHKey() {
	st := sshKeysStateOf(a)
	if !newSSHKeyCanGenerate(st) {
		return
	}
	dir, err := sshkey.Dir()
	if err != nil {
		st.newErr = err.Error()
		return
	}
	keygen := st.keygenPath()
	name := st.newName
	comment := strings.TrimSpace(st.newEmail)
	st.newBusy = true
	st.newErr = ""
	st.newGen++
	gen := st.newGen

	a.spawn(func() {
		key, err := sshkey.Generate(context.Background(), keygen, dir, name, comment)
		a.post(func() {
			if st.newGen != gen {
				return
			}
			st.newBusy = false
			if err != nil {
				st.newErr = err.Error()
				return
			}
			st.newOpen = false
			a.finishNewSSHKey(key)
		})
	})
}

// finishNewSSHKey activates a key just generated and selects it. When the
// settings cannot persist, the key still exists on disk — the dialog is
// already closed — so the raw error shows on the page and the list reloads
// with the key not active.
func (a *app) finishNewSSHKey(key sshkey.Key) {
	st := sshKeysStateOf(a)
	prefs := a.store.Settings()
	if !containsKeyPath(prefs.SSHKeys, key.Path) {
		prefs.SSHKeys = append(prefs.SSHKeys, key.Path)
	}
	if err := a.saveSSHKeySettings(prefs); err != nil {
		st.saveErr = err.Error()
	} else {
		st.saveErr = ""
	}
	a.reloadSSHKeys()
	st.selected = key.Path
}

// openExistingSSHKey runs the file picker in the background and reads the
// chosen pair. The picker itself — the injected one of a test or the system
// open dialog — is captured on the UI thread, as every other field of the
// page state is: the worker holds the function value alone and never reads
// the state. Canceling is silent; a picker failure or a file that is not a
// key pair shows its error on the page.
func (a *app) openExistingSSHKey() {
	st := sshKeysStateOf(a)
	if st.openBusy {
		return
	}
	win := a.settingsWin
	pick := st.pickKeyFileFn
	if pick == nil {
		pick = systemKeyFilePicker
	}
	st.openBusy = true
	st.openErr = ""
	st.openGen++
	gen := st.openGen

	a.spawn(func() {
		path, err := pick(win)
		var key sshkey.Key
		var openErr error
		if err == nil && path != "" {
			key, openErr = sshkey.Open(path)
		}
		a.post(func() {
			if st.openGen != gen {
				return
			}
			st.openBusy = false
			switch {
			case err != nil:
				st.openErr = err.Error()
			case path == "":
				// canceled
			case openErr != nil:
				st.openErr = openErr.Error()
			default:
				a.finishOpenSSHKey(key)
			}
		})
	})
}

// finishOpenSSHKey activates a key chosen in the picker and selects it. A
// pair outside the SSH folder joins the stored key files so it stays on the
// list even when it is deactivated later; one inside the folder lists on its
// own. A failed write shows the raw error on the page and changes nothing.
func (a *app) finishOpenSSHKey(key sshkey.Key) {
	st := sshKeysStateOf(a)
	prefs := a.store.Settings()
	inside := false
	if dir, err := sshkey.Dir(); err == nil {
		inside = filepath.Dir(key.Path) == filepath.Clean(dir)
	}
	if !inside && !containsKeyPath(prefs.SSHKeyFiles, key.Path) {
		prefs.SSHKeyFiles = append(prefs.SSHKeyFiles, key.Path)
	}
	if !containsKeyPath(prefs.SSHKeys, key.Path) {
		prefs.SSHKeys = append(prefs.SSHKeys, key.Path)
	}
	if err := a.saveSSHKeySettings(prefs); err != nil {
		st.saveErr = err.Error()
		return
	}
	st.saveErr = ""
	a.reloadSSHKeys()
	st.selected = key.Path
}

// newSSHKeyDialog mounts the New SSH Key dialog while it is open: the
// title, the file name and email fields, and the footer with Cancel on the
// left of the primary button, in the look of the app's other dialogs. The
// file name must pass the key name rules before generation can start, and
// the reason shows as soon as it does not.
func (a *app) newSSHKeyDialog(c *ui.Context, p palette) {
	st := sshKeysStateOf(a)
	if !st.newOpen {
		return
	}
	_, cancelled, confirmed := dialogFrame(c, p, "ssh-key-new", &st.newOpen, "New SSH Key", 460, func() {
		ui.Field(c, "File Name", func() {
			// Enter in either input submits through the input itself: a
			// single-line field takes the key before the frame's window-level
			// shortcut ever sees it, so the input's submission is the only
			// consumer. Its condition is the button's, exactly.
			in := ui.TextInput(c.Key("ssh-key-new-name"), &st.newName).FillWidth().AutoFocus().Label("Key file name").Disabled(st.newBusy)
			if in.Submitted() && newSSHKeyCanGenerate(st) {
				a.generateSSHKey()
			}
		})
		ui.Field(c, "Email", func() {
			in := ui.TextInput(c.Key("ssh-key-new-email"), &st.newEmail).FillWidth().Label("Key comment email").Disabled(st.newBusy)
			if in.Submitted() && newSSHKeyCanGenerate(st) {
				a.generateSSHKey()
			}
		})
		problem := ""
		if err := sshkey.CheckName(st.newName); err != nil {
			problem = err.Error()
		}
		if problem != "" {
			ui.Text(c, problem).FontSize(12).TextColor(p.danger)
		}
		if st.newBusy {
			ui.Row(c.Key("ssh-key-new-busy")).FillWidth().Justify(ui.Start).Role(ui.RoleStatus).Label("SSH Key Generation").Busy(true).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).AccessHidden(true)
				ui.Text(c, "Generating key…").FontSize(12).TextColor(p.muted).SingleLine()
			})
		} else if st.newErr != "" {
			ui.Text(c, st.newErr).FontSize(12).TextColor(p.danger).MaxWidth(420)
		}
	},
		dialogAction{key: "ssh-key-new-cancel", text: "Cancel", label: "Cancel New SSH Key"},
		dialogAction{key: "ssh-key-new-generate", text: "Generate Key", label: "Generate Key", disabled: !newSSHKeyCanGenerate(st)})
	if cancelled {
		st.newOpen = false
		st.newGen++
	}
	if confirmed {
		a.generateSSHKey()
	}
	if !st.newOpen {
		// The dialog was dismissed or canceled: a generation still running
		// drops its result through the generation counter.
		st.newGen++
	}
}
