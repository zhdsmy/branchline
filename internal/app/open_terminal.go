package app

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// terminalChoices are the terminals the General settings offer, in the
// order the pop-up shows them. The open argument differs from the display
// name only for iTerm2, whose application bundle is "iTerm".
var terminalChoices = []struct {
	value   string // stored in Settings.Terminal and shown in the pop-up
	openApp string // the -a argument of open
}{
	{"Terminal", "Terminal"},
	{"iTerm2", "iTerm"},
}

// terminalChoice normalizes a stored Terminal setting to one of the choices:
// an empty or unknown value — none was ever chosen — reads as Terminal.
func terminalChoice(saved string) (value, openApp string) {
	for _, choice := range terminalChoices {
		if choice.value == saved {
			return choice.value, choice.openApp
		}
	}
	return terminalChoices[0].value, terminalChoices[0].openApp
}

// terminalOptionValues are the stored values in pop-up order.
func terminalOptionValues() []string {
	out := make([]string, len(terminalChoices))
	for i, choice := range terminalChoices {
		out[i] = choice.value
	}
	return out
}

// runTerminalOpen runs open with the application and the target — a folder
// or a .command script — returning what open printed on standard error when
// it fails, or its own error text when stderr stayed empty.
func runTerminalOpen(openApp, target string) error {
	cmd := exec.Command("open", "-a", openApp, target)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

// openInTerminal opens the repository in the terminal the settings choose:
// a local repository's folder opens in it directly, while an SSH repository
// goes through a self-deleting .command script that logs the terminal into
// the server and leaves the user's login shell in the repository's
// directory. The work runs in the background — open is a process launch —
// and only ever surfaces a toast: it is no git action, so the session never
// turns busy and no action error records it.
func (a *app) openInTerminal(r store.Repo) {
	if a.store == nil {
		return
	}
	// The settings are read on the UI thread, never by the worker, as every
	// background task of the switcher does.
	prefs := a.store.Settings()
	display, openApp := terminalChoice(prefs.Terminal)
	runOpen := runTerminalOpen
	if st := a.reposUI(); st.openTerminalAppFn != nil {
		runOpen = st.openTerminalAppFn
	}
	a.spawn(func() {
		target := r.Path
		script := ""
		if r.Kind == store.SSH {
			ex, ok := r.ExecutorFor(prefs).(*git.SSHExecutor)
			if !ok {
				a.post(func() {
					a.toast = "Could not open " + display + ": the repository's executor is not SSH"
				})
				return
			}
			path, err := writeTerminalScript(ex)
			if err != nil {
				a.post(func() {
					a.toast = "Could not open " + display + ": " + err.Error()
				})
				return
			}
			script, target = path, path
		}
		if err := runOpen(openApp, target); err != nil {
			if script != "" {
				// The terminal never started, so nothing deletes the script;
				// drop it here rather than leave a login command behind.
				_ = os.Remove(script)
			}
			a.post(func() {
				a.toast = "Could not open " + display + ": " + err.Error()
			})
		}
	})
}

// writeTerminalScript writes the login command of ex into a private .command
// script in the temporary directory, which Terminal and iTerm2 open as a new
// shell session. The script removes itself as its first act: it holds host,
// user, port, key file paths and the server's directory — never a password —
// but there is no reason for it to outlive the launch. The exec replaces the
// script's shell with ssh, so the terminal shows exactly the login.
func writeTerminalScript(ex *git.SSHExecutor) (string, error) {
	f, err := os.CreateTemp(os.TempDir(), "branchline-ssh-*.command")
	if err != nil {
		return "", err
	}
	path := f.Name()
	fail := func(err error) (string, error) {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\nrm -f -- \"$0\"\nexec")
	for _, arg := range ex.TerminalCommand() {
		b.WriteByte(' ')
		b.WriteString(git.ShellQuote(arg))
	}
	b.WriteByte('\n')
	if _, err := f.WriteString(b.String()); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// openActiveInTerminal is the ⌃⌘T shortcut: Open in Terminal for the
// repository currently open, a no-op when there is none.
func (a *app) openActiveInTerminal() {
	if !a.session.HasRepo() {
		return
	}
	a.openInTerminal(a.session.Meta())
}

// showActiveInFinder is the ⌃⌘O shortcut: the row menu's Show in Finder for
// the repository currently open. A repository on a server has no folder the
// file manager could show, so the shortcut explains instead.
func (a *app) showActiveInFinder() {
	if !a.session.HasRepo() {
		return
	}
	meta := a.session.Meta()
	if meta.Kind == store.SSH {
		a.toast = "Show in Finder is only for repositories on this Mac."
		return
	}
	showInFinder(a.reposUI(), meta.Path)
}
