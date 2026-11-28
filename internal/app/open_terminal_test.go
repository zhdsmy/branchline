package app

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// openTerminalRecorder substitutes the run of open for Open in Terminal: it
// remembers the application and target of every launch and never starts a
// real terminal. fail, when non-nil, is what the launch reports.
type openTerminalRecorder struct {
	mu    sync.Mutex
	calls []struct {
		app    string
		target string
	}
	fail error
}

func (r *openTerminalRecorder) install(h *testHarness) {
	h.app.reposUI().openTerminalAppFn = func(openApp, target string) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, struct {
			app    string
			target string
		}{openApp, target})
		return r.fail
	}
}

func (r *openTerminalRecorder) recorded(t *testing.T) []struct {
	app    string
	target string
} {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]struct {
		app    string
		target string
	}(nil), r.calls...)
}

// removeOnCleanup registers the removal of the launch script a test
// recorded: a script only deletes itself when it actually runs, and most
// tests never run it.
func removeOnCleanup(t *testing.T, path string) {
	t.Helper()
	t.Cleanup(func() { _ = os.Remove(path) })
}

// chooseOpenInTerminal picks Open in Terminal from the context menu of the
// repository row named name, through the switcher's real popup.
func chooseOpenInTerminal(t *testing.T, h *testHarness, name string) {
	t.Helper()
	if !h.app.reposUI().popupOpen {
		openSwitcherPopup(t, h)
	}
	rightClickRepoRow(t, h, name)
	if err := h.ui.ChooseMenuItem("Open in Terminal"); err != nil {
		t.Fatalf("choose Open in Terminal on %q: %v; menu=%q", name, err, h.ui.Menu())
	}
	h.ui.Frame()
}

// terminalSSHShim writes a fake ssh that appends its arguments — one per
// line, then an end marker — to log and exits successfully without ever
// connecting, placed first on PATH for the test so sshProgram() finds it.
func terminalSSHShim(t *testing.T) (binDir, log string) {
	t.Helper()
	probeDir := t.TempDir()
	binDir = filepath.Join(probeDir, "bin")
	log = filepath.Join(probeDir, "ssh-args.log")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir ssh shim dir: %v", err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + git.ShellQuote(log) + "\nprintf '%s\\n' --end-- >> " + git.ShellQuote(log) + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write ssh shim: %v", err)
	}
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatalf("create ssh shim log: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return binDir, log
}

// Open in Terminal on a local repository's row opens the repository's folder
// in the terminal the settings name: Terminal while nothing was ever chosen,
// iTerm2 after the choice is saved — the open argument being the bundle
// name iTerm.
func TestOpenInTerminalLocalRepoMenu(t *testing.T) {
	local := newGitRepo(t, "termlocal")
	local.CommitFile("a.txt", "a\n", "commit")

	h := newTestApp(t, local.StoreRepo())
	rec := &openTerminalRecorder{}
	rec.install(h)

	chooseOpenInTerminal(t, h, "termlocal")
	h.drain()

	calls := rec.recorded(t)
	if len(calls) != 1 || calls[0].app != "Terminal" || calls[0].target != local.Path() {
		t.Fatalf("open calls = %+v, want exactly one open -a Terminal %q", calls, local.Path())
	}

	cur := h.store.Settings()
	cur.Terminal = "iTerm2"
	if err := h.store.SetSettings(cur); err != nil {
		t.Fatalf("save the terminal choice: %v", err)
	}
	h.ui.CloseMenu()
	h.ui.Frame()
	chooseOpenInTerminal(t, h, "termlocal")
	h.drain()

	calls = rec.recorded(t)
	if len(calls) != 2 || calls[1].app != "iTerm" || calls[1].target != local.Path() {
		t.Fatalf("open calls after choosing iTerm2 = %+v, want the second open -a iTerm %q", calls, local.Path())
	}
}

// ⌃⌘T opens the repository currently open in the terminal, exactly as the
// row menu does, and with no repository open it does nothing at all.
func TestOpenInTerminalShortcutCurrentRepo(t *testing.T) {
	local := newGitRepo(t, "termcurrent")
	local.CommitFile("a.txt", "a\n", "commit")

	h := newTestApp(t)
	rec := &openTerminalRecorder{}
	rec.install(h)

	h.ui.Key(ui.Ctrl|ui.Cmd, ui.KeyT)
	h.ui.Frame()
	h.drain()
	if calls := rec.recorded(t); len(calls) != 0 {
		t.Fatalf("⌃⌘T without a repository opened the terminal: %+v", calls)
	}

	h.openRepo(local.StoreRepo())
	h.drain()
	h.ui.Key(ui.Ctrl|ui.Cmd, ui.KeyT)
	h.ui.Frame()
	h.drain()
	calls := rec.recorded(t)
	if len(calls) != 1 || calls[0].app != "Terminal" || calls[0].target != local.Path() {
		t.Fatalf("open calls after ⌃⌘T = %+v, want exactly one open -a Terminal %q", calls, local.Path())
	}
}

// Open in Terminal on an SSH repository's row does not open a folder: it
// hands the terminal a self-deleting .command script — private, starting
// with #!/bin/sh — whose exec line is the executor's interactive ssh login.
// Running that script with the machine's ssh replaced by a recorder shows
// exactly the login arguments, and the script has removed itself.
func TestOpenInTerminalSSHRepoScript(t *testing.T) {
	binDir, log := terminalSSHShim(t)
	ssh := store.Repo{Kind: store.SSH, Name: "sshrepo", Host: "box.example.test", User: "alice", Port: 2222, Path: "/srv/app"}

	h := newTestApp(t, ssh)
	cur := h.store.Settings()
	cur.SSHKeys = []string{"/keys/one"}
	if err := h.store.SetSettings(cur); err != nil {
		t.Fatalf("save the active keys: %v", err)
	}
	rec := &openTerminalRecorder{}
	rec.install(h)

	chooseOpenInTerminal(t, h, "sshrepo")
	h.drain()

	calls := rec.recorded(t)
	if len(calls) != 1 || calls[0].app != "Terminal" {
		t.Fatalf("open calls = %+v, want exactly one open -a Terminal <script>", calls)
	}
	script := calls[0].target
	removeOnCleanup(t, script)
	if !strings.HasPrefix(filepath.Base(script), "branchline-ssh-") || !strings.HasSuffix(script, ".command") {
		t.Fatalf("script path %q is not a branchline-ssh-*.command", script)
	}
	st, err := os.Stat(script)
	if err != nil {
		t.Fatalf("the script does not exist after a successful open: %v", err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("script permissions %o, want exactly 700", st.Mode().Perm())
	}
	content, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("read the script: %v", err)
	}
	if first, _, _ := strings.Cut(string(content), "\n"); first != "#!/bin/sh" {
		t.Fatalf("script first line %q, want #!/bin/sh; script:\n%s", first, content)
	}
	if !strings.Contains(string(content), "rm -f -- \"$0\"\n") {
		t.Fatalf("script does not remove itself first:\n%s", content)
	}

	// The script's exec line carries exactly the interactive login of the
	// executor the settings build: run it with the shimmed ssh and compare
	// what the login receives.
	ex, ok := ssh.ExecutorFor(h.store.Settings()).(*git.SSHExecutor)
	if !ok {
		t.Fatalf("ExecutorFor of an SSH repository is %T, want *git.SSHExecutor", ssh.ExecutorFor(h.store.Settings()))
	}
	want := ex.TerminalCommand()
	if want[0] != filepath.Join(binDir, "ssh") {
		t.Fatalf("the login program = %q, want the shimmed ssh %q", want[0], filepath.Join(binDir, "ssh"))
	}
	cmd := exec.Command("/bin/sh", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running the script: %v\n%s", err, out)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(strings.TrimSuffix(string(data), "--end--\n"), "\n"), "\n")
	if len(got) != len(want)-1 {
		t.Fatalf("the login received %q, want exactly the arguments %q", got, want[1:])
	}
	for i, a := range want[1:] {
		if got[i] != a {
			t.Fatalf("login argument %d = %q, want %q; received %q want %q", i, got[i], a, got, want[1:])
		}
	}
	if _, err := os.Stat(script); !os.IsNotExist(err) {
		t.Fatalf("the script did not delete itself after running: err=%v", err)
	}
}

// A failure of open surfaces as a toast naming the terminal and open's
// error, the login script is removed at once since nothing else will, and
// the failure never turns the session busy or records an action error: the
// terminal launch is no git action.
func TestOpenInTerminalOpenFailure(t *testing.T) {
	terminalSSHShim(t)
	local := newGitRepo(t, "termfailopen")
	local.CommitFile("a.txt", "a\n", "commit")
	ssh := store.Repo{Kind: store.SSH, Name: "sshrepo", Host: "box.example.test", User: "alice", Path: "/srv/app"}

	h := newTestApp(t, local.StoreRepo(), ssh)
	h.openRepo(local.StoreRepo())
	h.drain()

	rec := &openTerminalRecorder{fail: errors.New("open exploded")}
	rec.install(h)

	chooseOpenInTerminal(t, h, "sshrepo")
	h.drain()

	calls := rec.recorded(t)
	if len(calls) != 1 {
		t.Fatalf("open calls = %+v, want exactly one", calls)
	}
	script := calls[0].target
	removeOnCleanup(t, script)
	if _, err := os.Stat(script); !os.IsNotExist(err) {
		t.Fatalf("the script survived a failed open: err=%v", err)
	}
	if !h.ui.HasText("Could not open Terminal: open exploded") {
		t.Fatalf("the failure toast is missing; texts=%q", h.ui.Texts())
	}
	if h.app.session.Busy() {
		t.Fatal("the session turned busy for a terminal launch")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("a terminal launch recorded an action error: %v", err)
	}
}

// ⌃⌘O reveals the folder of the repository currently open, exactly as the
// row menu does. On a repository of a server there is no folder to reveal,
// and the shortcut explains so instead of calling the file manager.
func TestOpenInTerminalFinderShortcut(t *testing.T) {
	terminalSSHShim(t)
	local := newGitRepo(t, "termfinder")
	local.CommitFile("a.txt", "a\n", "commit")

	h := newTestApp(t)
	var shown []string
	h.app.reposUI().showInFinderFn = func(path string) { shown = append(shown, path) }

	h.ui.Key(ui.Ctrl|ui.Cmd, ui.KeyO)
	h.ui.Frame()
	if len(shown) != 0 {
		t.Fatalf("⌃⌘O without a repository revealed %q", shown)
	}

	h.openRepo(local.StoreRepo())
	h.drain()
	h.ui.Key(ui.Ctrl|ui.Cmd, ui.KeyO)
	h.ui.Frame()
	if len(shown) != 1 || shown[0] != local.Path() {
		t.Fatalf("finder calls after ⌃⌘O = %q, want exactly %q", shown, local.Path())
	}

	ssh := store.Repo{Kind: store.SSH, Name: "sshrepo", Host: "box.example.test", User: "alice", Path: "/srv/app"}
	h.openRepo(ssh)
	h.drain()
	h.ui.Key(ui.Ctrl|ui.Cmd, ui.KeyO)
	h.ui.Frame()
	if len(shown) != 1 {
		t.Fatalf("⌃⌘O on a server repository revealed %q", shown[1:])
	}
	if !h.ui.HasText("Show in Finder is only for repositories on this Mac.") {
		t.Fatalf("the server toast is missing; texts=%q", h.ui.Texts())
	}
}

// The Terminal pop-up of the General settings persists its choice in the
// store, and a window opened afterwards shows the same choice.
func TestOpenInTerminalSettingsChoice(t *testing.T) {
	h := newTestApp(t)
	tt := openSettingsTester(t, h)

	chooseExternalTool(t, tt, "Terminal", "iTerm2")
	if got := h.store.Settings().Terminal; got != "iTerm2" {
		t.Fatalf("persisted Terminal = %q, want iTerm2", got)
	}

	h.app.closeSettingsWindow()
	h.ui.Key(ui.Cmd, ui.KeyComma)
	h.ui.Frame()
	tt2 := newSettingsTester(t, h)
	if !tt2.HasText("iTerm2") {
		t.Fatalf("the reopened settings do not show iTerm2; texts=%q", tt2.Texts())
	}
}
