package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshkey"
	"github.com/zhdsmy/branchline/internal/sshtest"
	"github.com/zhdsmy/branchline/internal/store"
)

// isolateSSHKeysTest points the test at a throwaway home directory, so the
// SSH folder the page lists is always a temporary one, and cuts every ssh
// the tests could start off the user's agent: the executors offer keys with
// -o AddKeysToAgent=yes, and a test key reaching the real agent would outlive
// the test. No test here runs ssh or ssh-add for real.
func isolateSSHKeysTest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	return home
}

// sshKeygenPath finds the real ssh-keygen the tests generate keys with.
func sshKeygenPath(t *testing.T) string {
	t.Helper()
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Fatalf("ssh-keygen is required for these tests: %v", err)
	}
	return keygen
}

// makeSSHKeyPair generates a real ed25519 key pair named name in dir and
// returns it.
func makeSSHKeyPair(t *testing.T, dir, name string) sshkey.Key {
	t.Helper()
	key, err := sshkey.Generate(context.Background(), sshKeygenPath(t), dir, name, name+"@example.com")
	if err != nil {
		t.Fatalf("generate key pair %q in %s: %v", name, dir, err)
	}
	return key
}

// openSSHKeysPage drives the Settings window from its real entry — ⌘, in the
// main window — and selects the SSH Keys tab, as a user would.
func openSSHKeysPage(t *testing.T, h *testHarness) *ui.Tester {
	t.Helper()
	tt := openSettingsTester(t, h)
	clickSettingsTab(t, tt, "SSH Keys")
	return tt
}

// seedSSHKeyFiles stores extra key files and active keys in the test store,
// as a previous run of the app would have saved them.
func seedSSHKeySettings(t *testing.T, h *testHarness, files, active []string) {
	t.Helper()
	prefs := h.store.Settings()
	prefs.SSHKeyFiles = files
	prefs.SSHKeys = active
	if err := h.store.SetSettings(prefs); err != nil {
		t.Fatalf("seed SSH key settings: %v", err)
	}
}

// recordingGitWrapper writes an executable that appends one line with its
// GIT_SSH_COMMAND environment and its arguments to log for every invocation
// and then runs the real git. It is the proof of what the executor of the
// open repository hands git, without a single ssh ever starting: the keys
// are only recorded in the environment.
func recordingGitWrapper(t *testing.T, dir, log string) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find real git: %v", err)
	}
	wrapper := filepath.Join(dir, "wrapped git")
	script := "#!/bin/sh\n" +
		"lock=" + git.ShellQuote(log+".lock") + "\n" +
		"while ! mkdir \"$lock\" 2>/dev/null; do :; done\n" +
		"trap 'rmdir \"$lock\" 2>/dev/null' EXIT\n" +
		"printf 'GIT_SSH_COMMAND=%s\\t%s\\n' \"${GIT_SSH_COMMAND-}\" \"$*\" >> " + git.ShellQuote(log) + "\n" +
		"rmdir \"$lock\"\n" +
		"trap - EXIT\n" +
		"exec " + git.ShellQuote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatalf("create git wrapper log: %v", err)
	}
	return wrapper
}

// recordingSSHShim writes a fake ssh that appends its arguments to log and
// exits with a failure. It never delegates to a real ssh: the tests checking
// the arguments of an SSH executor observe them here, and no connection is
// ever attempted.
func recordingSSHShim(t *testing.T, binDir, log string) {
	t.Helper()
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir ssh shim dir: %v", err)
	}
	script := "#!/bin/sh\n" +
		"lock=" + git.ShellQuote(log+".lock") + "\n" +
		"while ! mkdir \"$lock\" 2>/dev/null; do :; done\n" +
		"trap 'rmdir \"$lock\" 2>/dev/null' EXIT\n" +
		"printf '%s\\n' \"$*\" >> " + git.ShellQuote(log) + "\n" +
		"rmdir \"$lock\"\n" +
		"trap - EXIT\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write ssh shim: %v", err)
	}
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatalf("create ssh shim log: %v", err)
	}
}

func logContains(t *testing.T, log, needle string) bool {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read log %s: %v", log, err)
	}
	return strings.Contains(string(data), needle)
}

// The SSH Keys page lists the pairs of the SSH folder merged with the stored
// files opened from elsewhere, sorted by file name and independently of where
// they live: every row carries its Use checkbox, its type and its
// fingerprint.
func TestSettingsSSHKeysListSortedMergedRows(t *testing.T) {
	home := isolateSSHKeysTest(t)
	dir := filepath.Join(home, ".ssh")
	zeta := makeSSHKeyPair(t, dir, "zeta")
	alpha := makeSSHKeyPair(t, dir, "alpha")
	outside := t.TempDir()
	beta := makeSSHKeyPair(t, outside, "beta")
	mid := makeSSHKeyPair(t, dir, "mid")

	h := newTestApp(t)
	seedSSHKeySettings(t, h, []string{beta.Path}, nil)
	tt := openSSHKeysPage(t, h)

	var useBoxes []string
	for _, a := range tt.AccessAll() {
		if a.Role == ui.RoleCheckBox && strings.HasPrefix(a.Name, "Use ") {
			useBoxes = append(useBoxes, strings.TrimPrefix(a.Name, "Use "))
		}
	}
	want := []string{"alpha", "beta", "mid", "zeta"}
	if strings.Join(useBoxes, ",") != strings.Join(want, ",") {
		t.Fatalf("Use checkboxes in order %q, want %q", useBoxes, want)
	}
	if !tt.HasText(alpha.Fingerprint) || !tt.HasText(zeta.Fingerprint) {
		t.Fatalf("the rows miss their fingerprints; texts=%q", tt.Texts())
	}
	if !tt.HasText("ed25519") {
		t.Fatalf("the rows miss the key type; texts=%q", tt.Texts())
	}
	// Nothing is active yet: every checkbox reads unchecked.
	for _, name := range want {
		box, ok := tt.AccessByRole(ui.RoleCheckBox, "Use "+name)
		if !ok {
			t.Fatalf("no Use checkbox for %q; texts=%q", name, tt.Texts())
		}
		if box.Checked {
			t.Fatalf("Use %s reads checked although no key is active", name)
		}
	}
	_ = mid
}

// A stored key file that no longer reads shows as an error row naming the
// problem, and its Remove button takes it off the stored files and the active
// keys without touching anything else.
func TestSettingsSSHKeysBrokenEntryShowsErrorAndRemoves(t *testing.T) {
	home := isolateSSHKeysTest(t)
	dir := filepath.Join(home, ".ssh")
	good := makeSSHKeyPair(t, dir, "goodkey")

	junk := filepath.Join(t.TempDir(), "brokenkey")
	if err := os.WriteFile(junk, []byte("just some text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(junk+".pub", []byte("ssh-ed25519 AAAA junk\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := newTestApp(t)
	seedSSHKeySettings(t, h, []string{junk, good.Path}, []string{junk, good.Path})
	tt := openSSHKeysPage(t, h)

	if !tt.HasText("not a private key header") {
		t.Fatalf("the broken row does not show the read error; texts=%q", tt.Texts())
	}
	if _, ok := tt.AccessByRole(ui.RoleCheckBox, "Use brokenkey"); ok {
		t.Fatal("the broken row has a Use checkbox although the key cannot be read")
	}
	if err := tt.Click("Remove brokenkey from the list"); err != nil {
		t.Fatalf("click Remove brokenkey: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()

	prefs := h.store.Settings()
	if len(prefs.SSHKeyFiles) != 1 || prefs.SSHKeyFiles[0] != good.Path {
		t.Fatalf("stored files after Remove = %q, want only the good key", prefs.SSHKeyFiles)
	}
	if len(prefs.SSHKeys) != 1 || prefs.SSHKeys[0] != good.Path {
		t.Fatalf("active keys after Remove = %q, want only the good key", prefs.SSHKeys)
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "Remove brokenkey from the list"); ok {
		t.Fatal("the broken row survived its Remove")
	}
}

// Without an SSH folder the page lists nothing and explains itself instead
// of reporting an error; an existing but empty folder reads the same.
func TestSettingsSSHKeysMissingAndEmptyFolder(t *testing.T) {
	home := isolateSSHKeysTest(t)

	h := newTestApp(t)
	tt := openSSHKeysPage(t, h)
	if !tt.HasText("There is no ~/.ssh folder yet") {
		t.Fatalf("no missing-folder explanation; texts=%q", tt.Texts())
	}
	for _, a := range tt.AccessAll() {
		if a.Role == ui.RoleCheckBox && strings.HasPrefix(a.Name, "Use ") {
			t.Fatalf("the empty page shows a Use checkbox %q", a.Name)
		}
	}

	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	h2 := newTestApp(t)
	tt2 := openSSHKeysPage(t, h2)
	if !tt2.HasText("No SSH keys found") {
		t.Fatalf("no empty-list explanation; texts=%q", tt2.Texts())
	}
}

// Checking Use writes the key into the active keys of the settings, and the
// executor of the open local repository — rebuilt the moment the choice
// saves — hands it to git through GIT_SSH_COMMAND: a Fetch clicked in the
// main window runs with -i <key> in it, while no ssh ever starts.
func TestSettingsSSHKeysUseCheckboxReachesLocalFetch(t *testing.T) {
	home := isolateSSHKeysTest(t)
	dir := filepath.Join(home, ".ssh")
	key := makeSSHKeyPair(t, dir, "id_ed25519")

	repo := newGitRepo(t, "sk-localfetch")
	repo.CommitFile("base.txt", "base\n", "initial commit")
	repo.AddBareRemote("origin")

	probeDir := t.TempDir()
	gitLog := filepath.Join(probeDir, "git-log.txt")
	wrapper := recordingGitWrapper(t, probeDir, gitLog)

	h := newTestApp(t)
	if err := h.app.saveGitToolchainSettings(wrapper, "parent", git.Trace{}); err != nil {
		t.Fatalf("save toolchain settings: %v", err)
	}
	h.openRepo(repo.StoreRepo())
	h.drain()
	if logContains(t, gitLog, key.Path) {
		t.Fatal("git ran with the key before it was activated")
	}

	tt := openSSHKeysPage(t, h)
	if err := tt.Click("Use id_ed25519"); err != nil {
		t.Fatalf("check Use id_ed25519: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()

	prefs := h.store.Settings()
	if len(prefs.SSHKeys) != 1 || prefs.SSHKeys[0] != key.Path {
		t.Fatalf("active keys = %q, want exactly %q", prefs.SSHKeys, key.Path)
	}

	if err := os.Truncate(gitLog, 0); err != nil {
		t.Fatal(err)
	}
	if err := h.ui.Click("Fetch"); err != nil {
		t.Fatalf("click Fetch: %v; texts=%q", err, h.ui.Texts())
	}
	h.drain()

	data, err := os.ReadFile(gitLog)
	if err != nil {
		t.Fatal(err)
	}
	fetchWithKey := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "fetch") && strings.Contains(line, "-i "+key.Path) {
			fetchWithKey = true
		}
	}
	if !fetchWithKey {
		t.Fatalf("no fetch ran with -i %s in GIT_SSH_COMMAND; log:\n%s", key.Path, data)
	}
}

// On an SSH repository the active key reaches the ssh of the rebuilt
// executor as -i arguments: the fake ssh recording its arguments — and never
// connecting — shows them after the choice saves, where the load before it
// ran without them.
func TestSettingsSSHKeysUseCheckboxReachesSSHExecutor(t *testing.T) {
	home := isolateSSHKeysTest(t)
	dir := filepath.Join(home, ".ssh")
	key := makeSSHKeyPair(t, dir, "id_ed25519")

	probeDir := t.TempDir()
	sshLog := filepath.Join(probeDir, "ssh-log.txt")
	recordingSSHShim(t, filepath.Join(probeDir, "bin"), sshLog)
	t.Setenv("PATH", filepath.Join(probeDir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))

	h := newTestApp(t)
	if err := h.app.saveGitToolchainSettings("", "parent", git.Trace{}); err != nil {
		t.Fatalf("save toolchain settings: %v", err)
	}
	repo := store.Repo{Kind: store.SSH, Host: "127.0.0.1", User: "tester", Port: 22, Path: "/srv/repo", Name: "sshkeys-probe"}
	h.openRepo(repo)
	h.drain()

	initial, err := os.ReadFile(sshLog)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(initial))) == 0 {
		t.Fatal("the initial load never ran ssh: the later comparison would be vacuous")
	}
	if strings.Contains(string(initial), "-i "+key.Path) {
		t.Fatal("ssh ran with the key before it was activated")
	}

	tt := openSSHKeysPage(t, h)
	if err := tt.Click("Use id_ed25519"); err != nil {
		t.Fatalf("check Use id_ed25519: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	h.drain()

	data, err := os.ReadFile(sshLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "-i "+key.Path) {
		t.Fatalf("no ssh ran with -i %s after the key was activated; log:\n%s", key.Path, data)
	}
}

// The New SSH Key dialog starts with id_ed25519, numbered past every pair of
// the name already sitting in the SSH folder, and a generation through its
// real flow — the dialog's button — writes a key ssh-keygen made, activates
// it and selects it.
func TestSettingsSSHKeysNewKeyGeneratesActivatesSelects(t *testing.T) {
	home := isolateSSHKeysTest(t)
	dir := filepath.Join(home, ".ssh")

	h := newTestApp(t)
	tt := openSSHKeysPage(t, h)
	if err := tt.Click("New SSH Key"); err != nil {
		t.Fatalf("click New SSH Key…: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()

	nameField, ok := tt.AccessByRole(ui.RoleTextField, "Key file name")
	if !ok {
		t.Fatalf("no key file name field; texts=%q", tt.Texts())
	}
	if nameField.Value != "id_ed25519" {
		t.Fatalf("default name = %q, want id_ed25519", nameField.Value)
	}
	typeInSettingsField(t, tt, "Key comment email", "worker@example.com")
	if err := tt.Click("Generate Key"); err != nil {
		t.Fatalf("click Generate Key: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	drainSettings(t, h, tt)

	priv := filepath.Join(dir, "id_ed25519")
	st, err := os.Stat(priv)
	if err != nil {
		t.Fatalf("the generated private key is missing: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("private key permissions %o, want 600", st.Mode().Perm())
	}
	if _, err := os.Stat(priv + ".pub"); err != nil {
		t.Fatalf("the generated public key is missing: %v", err)
	}
	key, err := sshkey.Open(priv)
	if err != nil {
		t.Fatalf("read the generated key: %v", err)
	}
	if key.Comment != "worker@example.com" {
		t.Fatalf("key comment = %q, want worker@example.com", key.Comment)
	}

	prefs := h.store.Settings()
	if len(prefs.SSHKeys) != 1 || prefs.SSHKeys[0] != key.Path {
		t.Fatalf("active keys = %q, want exactly the generated %q", prefs.SSHKeys, key.Path)
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "Generate Key"); ok {
		t.Fatal("the dialog stayed open after a successful generation")
	}
	box, ok := tt.AccessByRole(ui.RoleCheckBox, "Use id_ed25519")
	if !ok {
		t.Fatalf("the new key does not list; texts=%q", tt.Texts())
	}
	if !box.Checked {
		t.Fatal("the new key's Use checkbox does not read checked: it was not activated")
	}
	// The key is selected: its full public key shows in the details.
	if !tt.HasText(key.Public) {
		t.Fatalf("the selected key's public key does not show; texts=%q", tt.Texts())
	}

	// A second dialog numbers past the pairs now sitting in the folder.
	makeSSHKeyPair(t, dir, "id_ed25519_1")
	if err := tt.Click("New SSH Key"); err != nil {
		t.Fatalf("reopen New SSH Key…: %v", err)
	}
	tt.Frame()
	nameField, ok = tt.AccessByRole(ui.RoleTextField, "Key file name")
	if !ok {
		t.Fatalf("no key file name field on reopen; texts=%q", tt.Texts())
	}
	if nameField.Value != "id_ed25519_2" {
		t.Fatalf("default name with id_ed25519 and id_ed25519_1 taken = %q, want id_ed25519_2", nameField.Value)
	}
}

// A file name failing the key name rules disables the Generate button and
// says why, and a name taken since the dialog opened keeps the dialog open
// with the error, ready for a corrected retry.
func TestSettingsSSHKeysNewKeyNameRulesAndExistsError(t *testing.T) {
	home := isolateSSHKeysTest(t)
	dir := filepath.Join(home, ".ssh")

	h := newTestApp(t)
	tt := openSSHKeysPage(t, h)
	if err := tt.Click("New SSH Key"); err != nil {
		t.Fatalf("click New SSH Key…: %v", err)
	}
	tt.Frame()

	typeInSettingsField(t, tt, "Key file name", "bad/name")
	if !tt.HasText("the file name cannot contain /") {
		t.Fatalf("no slash explanation; texts=%q", tt.Texts())
	}
	if btn, ok := tt.AccessByRole(ui.RoleButton, "Generate Key"); !ok || !btn.Disabled {
		t.Fatalf("Generate Key is not disabled for a name with /: %+v", btn)
	}
	typeInSettingsField(t, tt, "Key file name", "")
	if !tt.HasText("the key needs a file name") {
		t.Fatalf("no empty-name explanation; texts=%q", tt.Texts())
	}

	// A valid name can generate, but the pair appearing on disk first — as
	// another client writing it at the same time would — fails with the
	// exists error inside the still-open dialog.
	typeInSettingsField(t, tt, "Key file name", "id_ed25519")
	if btn, ok := tt.AccessByRole(ui.RoleButton, "Generate Key"); !ok || btn.Disabled {
		t.Fatalf("Generate Key is disabled for a valid name: %+v", btn)
	}
	makeSSHKeyPair(t, dir, "id_ed25519")
	if err := tt.Click("Generate Key"); err != nil {
		t.Fatalf("click Generate Key over a taken name: %v", err)
	}
	tt.Frame()
	drainSettings(t, h, tt)
	if !tt.HasText("already exists") {
		t.Fatalf("no exists error in the dialog; texts=%q", tt.Texts())
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "Generate Key"); !ok {
		t.Fatal("the dialog closed on the exists error: the draft cannot be fixed")
	}

	// Fixing the name retries through the same dialog.
	typeInSettingsField(t, tt, "Key file name", "id_ed25519_9")
	if err := tt.Click("Generate Key"); err != nil {
		t.Fatalf("click Generate Key with a free name: %v", err)
	}
	tt.Frame()
	drainSettings(t, h, tt)
	if _, err := os.Stat(filepath.Join(dir, "id_ed25519_9")); err != nil {
		t.Fatalf("the retry did not generate id_ed25519_9: %v", err)
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "Generate Key"); ok {
		t.Fatal("the dialog stayed open after the successful retry")
	}
}

// Open Existing Key activates what the picker returns: a pair outside the
// SSH folder joins the stored files so it stays listed, a pair already
// listing from the folder only activates, and a file that is not a key pair
// shows the read error without changing the settings. Canceling is silent.
func TestSettingsSSHKeysOpenExistingKey(t *testing.T) {
	home := isolateSSHKeysTest(t)
	dir := filepath.Join(home, ".ssh")
	listed := makeSSHKeyPair(t, dir, "homekey")
	outside := t.TempDir()
	external := makeSSHKeyPair(t, outside, "workkey")
	junk := filepath.Join(t.TempDir(), "notakey")
	if err := os.WriteFile(junk, []byte("just some text\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := newTestApp(t)
	tt := openSSHKeysPage(t, h)

	// The picker is stubbed through the injectable before any click.
	sshKeysStateOf(h.app).pickKeyFileFn = pickFuncForTest(external.Path)
	if err := tt.Click("Open Existing Key"); err != nil {
		t.Fatalf("click Open Existing Key…: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	drainSettings(t, h, tt)

	prefs := h.store.Settings()
	if len(prefs.SSHKeyFiles) != 1 || prefs.SSHKeyFiles[0] != external.Path {
		t.Fatalf("stored files = %q, want exactly the external key %q", prefs.SSHKeyFiles, external.Path)
	}
	if len(prefs.SSHKeys) != 1 || prefs.SSHKeys[0] != external.Path {
		t.Fatalf("active keys = %q, want exactly the external key", prefs.SSHKeys)
	}
	if box, ok := tt.AccessByRole(ui.RoleCheckBox, "Use workkey"); !ok || !box.Checked {
		t.Fatalf("the opened external key does not list as active: %+v", box)
	}

	// A pair of the SSH folder activates without joining the stored files.
	sshKeysStateOf(h.app).pickKeyFileFn = pickFuncForTest(listed.Path)
	if err := tt.Click("Open Existing Key"); err != nil {
		t.Fatalf("click Open Existing Key… for the listed key: %v", err)
	}
	tt.Frame()
	drainSettings(t, h, tt)
	prefs = h.store.Settings()
	if len(prefs.SSHKeyFiles) != 1 {
		t.Fatalf("the listed key joined the stored files: %q", prefs.SSHKeyFiles)
	}
	if len(prefs.SSHKeys) != 2 || !containsKeyPath(prefs.SSHKeys, listed.Path) {
		t.Fatalf("active keys = %q, want the listed key added", prefs.SSHKeys)
	}
	if box, ok := tt.AccessByRole(ui.RoleCheckBox, "Use homekey"); !ok || !box.Checked {
		t.Fatalf("the listed key does not read active: %+v", box)
	}

	// A file that is not a key pair shows the read error and changes nothing.
	sshKeysStateOf(h.app).pickKeyFileFn = pickFuncForTest(junk)
	if err := tt.Click("Open Existing Key"); err != nil {
		t.Fatalf("click Open Existing Key… for a non-key: %v", err)
	}
	tt.Frame()
	drainSettings(t, h, tt)
	if !tt.HasText("not a private key header") && !tt.HasText("is missing") {
		t.Fatalf("no read error on the page; texts=%q", tt.Texts())
	}
	if got := h.store.Settings(); len(got.SSHKeyFiles) != 1 || len(got.SSHKeys) != 2 {
		t.Fatalf("the failed open changed the settings: files %q keys %q", got.SSHKeyFiles, got.SSHKeys)
	}

	// Canceling the picker changes nothing and shows nothing.
	sshKeysStateOf(h.app).pickKeyFileFn = pickFuncForTest("")
	if err := tt.Click("Open Existing Key"); err != nil {
		t.Fatalf("click Open Existing Key… canceled: %v", err)
	}
	tt.Frame()
	drainSettings(t, h, tt)
	if sshKeysStateOf(h.app).openErr != "" {
		t.Fatalf("a canceled picker left the error %q", sshKeysStateOf(h.app).openErr)
	}
	if got := h.store.Settings(); len(got.SSHKeyFiles) != 1 || len(got.SSHKeys) != 2 {
		t.Fatalf("a canceled picker changed the settings: files %q keys %q", got.SSHKeyFiles, got.SSHKeys)
	}
}

// pickFuncForTest is the injected file picker answering with one fixed
// path, as a user choosing that file in the system dialog would.
func pickFuncForTest(path string) func(win *mygo.Window) (string, error) {
	return func(win *mygo.Window) (string, error) { return path, nil }
}

// Copy Public Key puts exactly the public key line on the clipboard, and
// Show in Finder reveals the private key through the injected shell hook.
func TestSettingsSSHKeysCopyPublicKeyAndShowInFinder(t *testing.T) {
	home := isolateSSHKeysTest(t)
	dir := filepath.Join(home, ".ssh")
	key := makeSSHKeyPair(t, dir, "id_ed25519")

	h := newTestApp(t)
	var revealed []string
	sshKeysStateOf(h.app).showInFolderFn = func(path string) { revealed = append(revealed, path) }

	tt := openSSHKeysPage(t, h)
	if err := tt.Click("id_ed25519"); err != nil {
		t.Fatalf("select the key row: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click("Copy Public Key"); err != nil {
		t.Fatalf("click Copy Public Key: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if got := tt.Clipboard(); got != key.Public {
		t.Fatalf("clipboard = %q, want exactly the public key %q", got, key.Public)
	}

	if err := tt.Click("Show in Finder"); err != nil {
		t.Fatalf("click Show in Finder: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if len(revealed) != 1 || revealed[0] != key.Path {
		t.Fatalf("revealed paths = %q, want exactly the private key %q", revealed, key.Path)
	}
}

// When the store cannot persist, a Use click reverts to the saved state —
// the checkbox reads as before, the raw error shows, and the settings keep
// their value — and restoring writability lets the same click save.
func TestSettingsSSHKeysSaveFailureRevertsCheckbox(t *testing.T) {
	home := isolateSSHKeysTest(t)
	dir := filepath.Join(home, ".ssh")
	key := makeSSHKeyPair(t, dir, "id_ed25519")

	h := newTestApp(t)
	tt := openSSHKeysPage(t, h)

	storeDir := filepath.Dir(h.store.Path())
	if err := os.Chmod(storeDir, 0o500); err != nil {
		t.Fatalf("chmod store dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(storeDir, 0o700) })
	probe := h.store.Settings()
	probe.SSHKeys = []string{key.Path}
	probeErr := h.store.SetSettings(probe)
	if probeErr == nil {
		t.Fatal("SetSettings succeeded into a read-only directory; the failure assertions below would be vacuous")
	}

	if err := tt.Click("Use id_ed25519"); err != nil {
		t.Fatalf("check Use id_ed25519: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()

	if !tt.HasText(probeErr.Error()) {
		t.Fatalf("the page does not show the raw save error %q; texts=%q", probeErr, tt.Texts())
	}
	if len(h.store.Settings().SSHKeys) != 0 {
		t.Fatalf("the failed save changed the active keys: %q", h.store.Settings().SSHKeys)
	}
	box, ok := tt.AccessByRole(ui.RoleCheckBox, "Use id_ed25519")
	if !ok {
		t.Fatalf("no Use id_ed25519 checkbox; texts=%q", tt.Texts())
	}
	if box.Checked {
		t.Fatal("after the failed save the Use checkbox still reads checked: the choice did not revert")
	}

	if err := os.Chmod(storeDir, 0o700); err != nil {
		t.Fatalf("restore store dir: %v", err)
	}
	if err := tt.Click("Use id_ed25519"); err != nil {
		t.Fatalf("check Use id_ed25519 again: %v", err)
	}
	tt.Frame()
	if len(h.store.Settings().SSHKeys) != 1 {
		t.Fatalf("after restoring writability the active keys = %q, want the key", h.store.Settings().SSHKeys)
	}
	box, ok = tt.AccessByRole(ui.RoleCheckBox, "Use id_ed25519")
	if !ok || !box.Checked {
		t.Fatal("after the successful retry the Use checkbox does not read checked")
	}
	if tt.HasText(probeErr.Error()) {
		t.Fatal("the error text survived a successful save")
	}
}

// Every control of the SSH Keys page is reachable and named for keyboard
// users and assistive technology: the Use checkboxes, the key rows, the
// detail buttons and the New and Open entries each carry a name, and Tab
// walks them in order.
func TestSettingsSSHKeysAccessibility(t *testing.T) {
	home := isolateSSHKeysTest(t)
	dir := filepath.Join(home, ".ssh")
	makeSSHKeyPair(t, dir, "id_ed25519")

	h := newTestApp(t)
	tt := openSSHKeysPage(t, h)

	for _, want := range []struct {
		role ui.Role
		name string
	}{
		{ui.RoleCheckBox, "Use id_ed25519"},
		{ui.RoleButton, "id_ed25519"},
		{ui.RoleButton, "New SSH Key"},
		{ui.RoleButton, "Open Existing Key"},
	} {
		if _, ok := tt.AccessByRole(want.role, want.name); !ok {
			t.Fatalf("no %v named %q; texts=%q", want.role, want.name, tt.Texts())
		}
	}

	// Tab reaches the page's controls in order: the Use checkbox first, then
	// the key row.
	a11yFocusTab(t, tt, ui.RoleCheckBox, "Use id_ed25519")
	a11yFocusTab(t, tt, ui.RoleButton, "id_ed25519")

	// Selecting the row reveals its detail buttons, which Tab reaches too.
	if err := tt.Click("id_ed25519"); err != nil {
		t.Fatalf("select the key row: %v", err)
	}
	tt.Frame()
	a11yFocusTab(t, tt, ui.RoleButton, "Copy Public Key")
	a11yFocusTab(t, tt, ui.RoleButton, "Show in Finder")
	a11yFocusTab(t, tt, ui.RoleButton, "New SSH Key")
	a11yFocusTab(t, tt, ui.RoleButton, "Open Existing Key")
}

// defaultSSHKeyName returns the base name instead of looping forever when
// the SSH folder cannot be queried (for example when a regular file sits at
// the folder path).
func TestSettingsSSHKeysDefaultNameOnUnreadableDir(t *testing.T) {
	home := isolateSSHKeysTest(t)
	notADir := filepath.Join(home, ".ssh")
	if err := os.WriteFile(notADir, []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := defaultSSHKeyName(notADir); got != "id_ed25519" {
		t.Fatalf("defaultSSHKeyName on non-directory = %q, want id_ed25519", got)
	}
}

// recordingSSHArgvShim writes a fake ssh that records each argv element on
// its own line (prefixed with "ARG:") and exits with status 1 without ever
// attempting a network connection.
func recordingSSHArgvShim(t *testing.T, binDir, log string) {
	t.Helper()
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir ssh argv shim dir: %v", err)
	}
	script := "#!/bin/sh\n" +
		"lock=" + git.ShellQuote(log+".lock") + "\n" +
		"while ! mkdir \"$lock\" 2>/dev/null; do :; done\n" +
		"trap 'rmdir \"$lock\" 2>/dev/null' EXIT\n" +
		"printf 'CALL\\n' >> " + git.ShellQuote(log) + "\n" +
		"for a in \"$@\"; do printf 'ARG:%s\\n' \"$a\" >> " + git.ShellQuote(log) + "; done\n" +
		"rmdir \"$lock\"\n" +
		"trap - EXIT\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write ssh argv shim: %v", err)
	}
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatalf("create ssh argv shim log: %v", err)
	}
}

func assertSSHArgvHasIdentity(t *testing.T, logPath, wantKey, forbidKey string) {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read argv log %s: %v", logPath, err)
	}
	lines := strings.Split(string(data), "\n")
	foundWant := false
	for i := 0; i+1 < len(lines); i++ {
		if lines[i] == "ARG:-i" && lines[i+1] == "ARG:"+wantKey {
			foundWant = true
		}
	}
	if !foundWant {
		t.Fatalf("fake ssh argv never received intact pair [-i, %q]; log:\n%s", wantKey, data)
	}
	if strings.Contains(string(data), forbidKey) {
		t.Fatalf("fake ssh argv received unchecked key %q; log:\n%s", forbidKey, data)
	}
}

// Active key paths containing spaces, single quotes, and double quotes
// reach both local git's GIT_SSH_COMMAND (evaluated by /bin/sh) and
// SSHExecutor's argv as one intact -i argument, while unchecked keys in
// ~/.ssh are not offered.
func TestSettingsSSHKeysQuotedPathAndOnlyCheckedActive(t *testing.T) {
	home := isolateSSHKeysTest(t)
	t.Setenv("GIT_SSH_COMMAND", "")

	quotedDir := filepath.Join(t.TempDir(), "my 'quoted' dir")
	checked := makeSSHKeyPair(t, quotedDir, `work "key" 1`)
	unchecked := makeSSHKeyPair(t, filepath.Join(home, ".ssh"), "unchecked_key")

	probeDir := t.TempDir()
	sshLog := filepath.Join(probeDir, "ssh-argv.log")
	recordingSSHArgvShim(t, filepath.Join(probeDir, "bin"), sshLog)
	t.Setenv("PATH", filepath.Join(probeDir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))

	h := newTestApp(t)
	if err := h.app.saveGitToolchainSettings("", "parent", git.Trace{}); err != nil {
		t.Fatalf("save toolchain settings: %v", err)
	}
	seedSSHKeySettings(t, h, []string{checked.Path}, nil)

	tt := openSSHKeysPage(t, h)
	if err := tt.Click("Use " + checked.Name); err != nil {
		t.Fatalf("check Use %q: %v; texts=%q", checked.Name, err, tt.Texts())
	}
	tt.Frame()

	// Local git fetch over an SSH URL invokes /bin/sh on GIT_SSH_COMMAND,
	// which executes the fake ssh shim (never connecting).
	localRepo := newGitRepo(t, "sk-quoted-local")
	localRepo.CommitFile("a.txt", "alpha\n", "init")
	localRepo.Git("remote", "add", "origin", "ssh://tester@example.invalid/repo.git")
	h.openRepo(localRepo.StoreRepo())
	h.drain()
	if err := os.Truncate(sshLog, 0); err != nil {
		t.Fatal(err)
	}
	if err := h.ui.Click("Fetch"); err != nil {
		t.Fatalf("click Fetch: %v; texts=%q", err, h.ui.Texts())
	}
	h.drain()
	assertSSHArgvHasIdentity(t, sshLog, checked.Path, unchecked.Path)

	// SSHExecutor passes the key path directly through argv to the fake ssh.
	if err := os.Truncate(sshLog, 0); err != nil {
		t.Fatal(err)
	}
	sshRepo := store.Repo{Kind: store.SSH, Host: "example.invalid", User: "tester", Port: 22, Path: "/srv/repo", Name: "sk-quoted-ssh"}
	h.openRepo(sshRepo)
	h.drain()
	assertSSHArgvHasIdentity(t, sshLog, checked.Path, unchecked.Path)
}

// Checking an SSH key in Settings lets both SSH repositories and local git
// fetches over SSH authenticate against the real Linux SSH server using
// only that test-generated key (no ssh-agent and no default sshlab key),
// even when the key file name contains spaces and single quotes.
func TestConfiguredLinuxSettingsSSHKeysAuthenticate(t *testing.T) {
	srv := sshtest.ConfiguredLinux(t)
	origSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatalf("find sshlab ssh wrapper: %v", err)
	}

	// Read port, user, and known_hosts from sshlab's ssh config via ssh -G.
	cfgOut, err := exec.Command(origSSH, "-G", srv.Host).CombinedOutput()
	if err != nil {
		t.Fatalf("ssh -G %s: %v\n%s", srv.Host, err, cfgOut)
	}
	var remoteUser, knownHosts string
	var port int
	for _, line := range strings.Split(string(cfgOut), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "user":
			if remoteUser == "" {
				remoteUser = fields[1]
			}
		case "port":
			if port == 0 {
				port, _ = strconv.Atoi(fields[1])
			}
		case "userknownhostsfile":
			if knownHosts == "" {
				knownHosts = fields[1]
			}
		}
	}
	if remoteUser == "" || port <= 0 || knownHosts == "" {
		t.Fatalf("incomplete ssh -G %s output: user=%q port=%d knownHosts=%q", srv.Host, remoteUser, port, knownHosts)
	}

	home := isolateSSHKeysTest(t)
	t.Setenv("GIT_SSH_COMMAND", "")
	sshDir := filepath.Join(home, ".ssh")
	checkedKey := makeSSHKeyPair(t, sshDir, "my 'checked' key")
	uncheckedKey := makeSSHKeyPair(t, sshDir, "unchecked_key")

	remoteDir := srv.InitRepo(t, "sshkeys-live")
	wantHead := srv.RunGit(t, remoteDir, "rev-parse", "HEAD")

	// Authorize only checkedKey on the disposable Linux container, keeping
	// sshlab's existing key so oracle helpers keep working, and restore on exit.
	origAuth := srv.RunShell(t, "cat /root/.ssh/authorized_keys")
	t.Cleanup(func() {
		srv.RunShellInput(t, "cat > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys", []byte(origAuth))
	})
	updatedAuth := strings.TrimRight(origAuth, "\n") + "\n" + checkedKey.Public + "\n"
	srv.RunShellInput(t, "cat > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys", []byte(updatedAuth))

	// Wrap ssh so connections to 127.0.0.1 use known_hosts only — no default
	// sshlab IdentityFile and no agent — while srv.Host delegates to origSSH.
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper := "#!/bin/sh\n" +
		"for _arg in \"$@\"; do\n" +
		"  case \"$_arg\" in\n" +
		"    127.0.0.1|*@127.0.0.1)\n" +
		"      exec /usr/bin/ssh -F /dev/null" +
		" -o UserKnownHostsFile=" + git.ShellQuote(knownHosts) +
		" -o HostKeyAlias=" + git.ShellQuote(srv.Host) +
		" -o CheckHostIP=no" +
		" -o IdentityAgent=none" +
		" -o IdentitiesOnly=yes" +
		" -o PasswordAuthentication=no" +
		" -o KbdInteractiveAuthentication=no \"$@\"\n" +
		"      ;;\n" +
		"  esac\n" +
		"done\n" +
		"exec " + git.ShellQuote(origSSH) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Ensure every ControlMaster socket our test opens is shut down cleanly.
	closeMaster := func(keys []string) {
		ex := &git.SSHExecutor{Host: "127.0.0.1", User: remoteUser, Port: port, Dir: remoteDir, SSHKeys: keys}
		cp := ex.ControlPath()
		if _, err := os.Stat(cp); err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "/usr/bin/ssh", "-F", "/dev/null", "-O", "exit", "-o", "ControlPath="+cp, remoteUser+"@127.0.0.1").Run()
		_ = os.Remove(cp)
	}
	t.Cleanup(func() {
		closeMaster(nil)
		closeMaster([]string{uncheckedKey.Path})
		closeMaster([]string{checkedKey.Path})
		closeMaster([]string{uncheckedKey.Path, checkedKey.Path})
		closeMaster([]string{checkedKey.Path, uncheckedKey.Path})
	})

	h := newTestApp(t)
	if err := h.app.saveGitToolchainSettings("", "parent", git.Trace{}); err != nil {
		t.Fatalf("save toolchain settings: %v", err)
	}

	sshRepo := store.Repo{
		Kind: store.SSH,
		Host: "127.0.0.1",
		User: remoteUser,
		Port: port,
		Path: remoteDir,
		Name: "sshkeys-live",
	}

	// Without any active key, authentication to the container must fail.
	h.openRepo(sshRepo)
	h.drain()
	if h.app.session.Loaded() || h.app.session.LoadErr() == nil {
		t.Fatalf("SSH repo loaded without any active key (loaded=%v err=%v)", h.app.session.Loaded(), h.app.session.LoadErr())
	}

	// Activating only the unauthorized key still fails.
	tt := openSSHKeysPage(t, h)
	if err := tt.Click("Use " + uncheckedKey.Name); err != nil {
		t.Fatalf("check Use %s: %v; texts=%q", uncheckedKey.Name, err, tt.Texts())
	}
	tt.Frame()
	h.drain()
	if h.app.session.Loaded() || h.app.session.LoadErr() == nil {
		t.Fatalf("SSH repo loaded with only an unauthorized key (loaded=%v err=%v)", h.app.session.Loaded(), h.app.session.LoadErr())
	}

	// Switching to the authorized key rebuilds the executor and loads the repo.
	if err := tt.Click("Use " + uncheckedKey.Name); err != nil {
		t.Fatalf("uncheck Use %s: %v", uncheckedKey.Name, err)
	}
	tt.Frame()
	h.drain()
	if err := tt.Click("Use " + checkedKey.Name); err != nil {
		t.Fatalf("check Use %s: %v; texts=%q", checkedKey.Name, err, tt.Texts())
	}
	tt.Frame()
	h.drain()

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH repo load failed with checked key: %v", err)
	}
	if !h.app.session.Loaded() {
		t.Fatal("SSH repo not marked loaded after activating authorized key")
	}
	if got := h.app.session.Snapshot().Status.Head; got != wantHead {
		t.Fatalf("loaded HEAD = %q, want %q", got, wantHead)
	}

	// Local git fetch over SSH also authenticates using the checked quoted key.
	localRepo := newGitRepo(t, "sshkeys-local-live")
	localRepo.CommitFile("local.txt", "local\n", "local init")
	remoteURL := "ssh://" + remoteUser + "@127.0.0.1:" + strconv.Itoa(port) + remoteDir
	localRepo.Git("remote", "add", "origin", remoteURL)
	localExec := store.Repo{Kind: store.Local, Path: localRepo.Path()}.ExecutorFor(h.store.Settings())
	if err := git.New(localExec).Fetch(); err != nil {
		t.Fatalf("local git fetch over SSH with checked key failed: %v", err)
	}
	if got := strings.TrimSpace(localRepo.Git("rev-parse", "refs/remotes/origin/main")); got != wantHead {
		t.Fatalf("fetched origin/main = %q, want %q", got, wantHead)
	}
}
