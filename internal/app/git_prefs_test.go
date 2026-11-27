package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// isolatedPrefsGitConfig points the test process at a throwaway home and
// global git configuration file, so the global identity tests never read or
// write the real user configuration, and returns the configuration file path.
func isolatedPrefsGitConfig(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	return cfg
}

// loggingShim writes an executable that appends "PATH=<its PATH>\tGIT_TRACE=<its
// GIT_TRACE>\t<args>" to log for every invocation and then executes target with
// its arguments. It is both the proof that a custom program was used and the
// record of the environment and arguments the product handed it.
func loggingShim(t *testing.T, dir, name, target, log string) string {
	t.Helper()
	shim := filepath.Join(dir, name)
	script := "#!/bin/sh\nprintf '%s\\t%s\\t%s\\n' \"PATH=$PATH\" \"GIT_TRACE=${GIT_TRACE:-}\" \"$*\" >> " + git.ShellQuote(log) + "\nexec " + git.ShellQuote(target) + " \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatalf("write shim %s: %v", shim, err)
	}
	return shim
}

func readLogLines(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read shim log %s: %v", log, err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// Adding a local repository through the real Repository Switcher flow uses
// the custom git program saved in the settings: the wrapper's log records the
// rev-parse that reduces the picked folder to its repository root, with the
// trace environment the settings turn on.
func TestGitPrefsLocalAddUsesCustomGit(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find real git: %v", err)
	}
	probeDir := t.TempDir()
	gitLog := filepath.Join(probeDir, "git-log.txt")
	if err := os.WriteFile(gitLog, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	wrapper := loggingShim(t, probeDir, "wrapped git", realGit, gitLog)

	h := newTestApp(t)
	trace := git.Trace{Git: true, SSH: true}
	if err := h.app.saveGitToolchainSettings(wrapper, "parent", trace); err != nil {
		t.Fatalf("save toolchain settings: %v", err)
	}

	// Picked folders inside the repository reduce to its root.
	repo := newGitRepo(t, "prefspicked")
	repo.CommitFile("sub/dir/file.txt", "x\n", "sub commit")
	sub := filepath.Join(repo.Path(), "sub", "dir")
	stubPickLocalFolder(t, func() (string, error) { return sub, nil })

	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Add Local Repository…"); err != nil {
		t.Fatalf("click Add Local Repository…: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if !h.app.session.HasRepo() {
		t.Fatalf("no repository open after picking %q", sub)
	}
	// The pick's rev-parse --show-toplevel ran through the custom git program;
	// opening the added repository also runs snapshot commands (including
	// rev-parse probes) through the same custom program with the trace env.
	allLines := readLogLines(t, gitLog)
	var showToplevel []string
	for _, line := range allLines {
		if strings.Contains(line, "rev-parse") && strings.Contains(line, "--show-toplevel") {
			showToplevel = append(showToplevel, line)
		}
		if !strings.Contains(line, "--version") && !strings.Contains(line, "GIT_TRACE=1") {
			t.Fatalf("custom git invocation %q misses the GIT_TRACE=1 environment of the saved trace settings", line)
		}
	}
	if len(showToplevel) == 0 {
		t.Fatalf("no rev-parse --show-toplevel invocation in the custom git log; log=%q", allLines)
	}
	wantRoot, err := filepath.EvalSymlinks(repo.Path())
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if got := h.app.session.Meta().Path; got != wantRoot {
		t.Fatalf("opened path = %q, want repository root %q", got, wantRoot)
	}
}

// Invalid custom git programs and unknown ENV PATH choices are rejected
// without touching the stored settings, and a store that cannot persist
// leaves the previous settings intact — while a valid save applies, clears
// the legacy verbose flag and survives a retry.
func TestGitPrefsValidationRejectsAndFailureKeepsSettings(t *testing.T) {
	h := newTestApp(t)
	before := h.store.Settings()

	// Seed a real binary for the valid cases.
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find real git: %v", err)
	}

	probeDir := t.TempDir()
	notGit := filepath.Join(probeDir, "not git")
	if err := os.WriteFile(notGit, []byte("#!/bin/sh\necho hello\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name           string
		gitPath        string
		envPath        string
		wantErrContain string
	}{
		{"program that is not git", notGit, "", "not a valid Git binary"},
		{"a directory", probeDir, "", "not a valid Git binary"},
		{"a missing path", filepath.Join(probeDir, "missing"), "", "not a valid Git binary"},
		{"unknown ENV PATH", realGit, "bogus", "unknown ENV PATH"},
		{"ENV PATH with whitespace", realGit, " parent ", "unknown ENV PATH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := h.app.saveGitToolchainSettings(tc.gitPath, tc.envPath, git.Trace{})
			if err == nil || !strings.Contains(err.Error(), tc.wantErrContain) {
				t.Fatalf("save err = %v, want it to contain %q", err, tc.wantErrContain)
			}
			if got := h.store.Settings(); !reflect.DeepEqual(got, before) {
				t.Fatalf("settings changed by a rejected save:\nbefore %+v\nafter  %+v", before, got)
			}
		})
	}

	// A valid save persists the program, the PATH choice and the traces.
	trace := git.Trace{Git: true, Curl: true, Perf: true}
	if err := h.app.saveGitToolchainSettings(" "+realGit+" ", "shell", trace); err != nil {
		t.Fatalf("valid save: %v", err)
	}
	saved := h.store.Settings()
	if saved.GitPath != realGit {
		t.Fatalf("saved GitPath = %q, want trimmed %q", saved.GitPath, realGit)
	}
	if saved.EnvPath != "shell" {
		t.Fatalf("saved EnvPath = %q, want shell", saved.EnvPath)
	}
	if saved.Trace() != trace {
		t.Fatalf("saved traces = %+v, want %+v", saved.Trace(), trace)
	}

	// The legacy all-traces flag never survives a save: the file on disk
	// holds the individual switches only.
	seeded := h.store.Settings()
	if seeded.VerboseGit {
		t.Fatal("the migrated verbose flag is set")
	}
	seeded.VerboseGit = true
	if err := h.store.SetSettings(seeded); err != nil {
		t.Fatalf("re-save verbose seed: %v", err)
	}
	if err := h.app.saveGitToolchainSettings(realGit, "shell", trace); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	raw, err := os.ReadFile(h.store.Path())
	if err != nil {
		t.Fatalf("read store file: %v", err)
	}
	if strings.Contains(string(raw), "verboseGit") {
		t.Fatalf("legacy verboseGit key persisted:\n%s", raw)
	}

	// A store that cannot persist refuses the write and keeps the settings.
	storeDir := filepath.Dir(h.store.Path())
	if err := os.Chmod(storeDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(storeDir, 0o700) })
	intact := h.store.Settings()
	err = h.app.saveGitToolchainSettings(realGit, "parent", git.Trace{})
	if err == nil {
		t.Fatal("save into a read-only store directory unexpectedly succeeded")
	}
	if got := h.store.Settings(); !reflect.DeepEqual(got, intact) {
		t.Fatalf("settings changed by a failed save:\nbefore %+v\nafter  %+v", intact, got)
	}
	// With the directory writable again the same save succeeds, proving the
	// failure above was the store and not the input.
	if err := os.Chmod(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.app.saveGitToolchainSettings(realGit, "parent", git.Trace{}); err != nil {
		t.Fatalf("retry save after restoring permissions: %v", err)
	}
}

// The global author identity round-trips through the product's save helper in
// an isolated configuration: a commit in a repository without its own
// user.name/user.email carries the new author at once, a failed or tampered
// read refuses to overwrite, and the saved custom git program is what reads
// and writes the configuration.
func TestGitPrefsGlobalIdentityRoundtrip(t *testing.T) {
	isolatedPrefsGitConfig(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find real git: %v", err)
	}
	probeDir := t.TempDir()
	gitLog := filepath.Join(probeDir, "git-log.txt")
	if err := os.WriteFile(gitLog, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	wrapper := loggingShim(t, probeDir, "wrapped git", realGit, gitLog)

	h := newTestApp(t)
	if err := h.app.saveGitToolchainSettings(wrapper, "", git.Trace{}); err != nil {
		t.Fatalf("save toolchain settings: %v", err)
	}

	// Read the identity the way the settings form would, then write through
	// the product helper.
	live, err := h.app.localGitForSettings().GlobalIdentity()
	if err != nil || live != (git.Identity{}) {
		t.Fatalf("initial identity = %+v, %v; want empty", live, err)
	}
	identity := git.Identity{Name: "Prefs Author", Email: "prefs@example.com"}
	if err := h.app.saveGlobalIdentity(identity, live, nil); err != nil {
		t.Fatalf("save identity: %v", err)
	}

	// The identity write ran through the custom git program.
	var configWrites int
	for _, line := range readLogLines(t, gitLog) {
		if strings.Contains(line, "config") && strings.Contains(line, "--global") {
			configWrites++
		}
	}
	if configWrites == 0 {
		t.Fatalf("the identity write never reached the custom git program; log=%q", readLogLines(t, gitLog))
	}

	// A commit in a repository without a local identity uses it immediately.
	dir := t.TempDir()
	runGitCommand(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, dir, "add", "--", "f.txt")
	// runGitCommand nulls the global configuration for fixture determinism;
	// this commit must see the isolated global file instead, so it goes
	// through a plain git invocation inheriting the test environment.
	commit := exec.Command("git", "commit", "-q", "-m", "authored")
	commit.Dir = dir
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("commit with the global identity: %v\n%s", err, out)
	}
	author := exec.Command("git", "log", "-1", "--format=%an%x00%ae")
	author.Dir = dir
	out, err := author.CombinedOutput()
	if err != nil {
		t.Fatalf("read author: %v\n%s", err, out)
	}
	name, email, ok := strings.Cut(strings.TrimSpace(string(out)), "\x00")
	if !ok || name != identity.Name || email != identity.Email {
		t.Fatalf("commit author = %q <%q>, want %q <%q>", name, email, identity.Name, identity.Email)
	}

	// A read error refuses the write before anything happens.
	readErr := errors.New("simulated read failure")
	if err := h.app.saveGlobalIdentity(git.Identity{Name: "Changed"}, live, readErr); !errors.Is(err, readErr) {
		t.Fatalf("save with read error = %v, want it to wrap the read error", err)
	}
	id, err := h.app.localGitForSettings().GlobalIdentity()
	if err != nil || id != identity {
		t.Fatalf("identity changed despite the read error: %+v, %v", id, err)
	}

	// An external edit after the read refuses the write too, and survives.
	if err := h.app.localGitForSettings().Run("config", "--global", "user.name", "Mallory"); err != nil {
		t.Fatalf("simulate external edit: %v", err)
	}
	err = h.app.saveGlobalIdentity(git.Identity{Name: "Changed", Email: identity.Email}, identity, nil)
	if !errors.Is(err, git.ErrGlobalIdentityChanged) {
		t.Fatalf("save after external edit = %v, want ErrGlobalIdentityChanged", err)
	}
	id, err = h.app.localGitForSettings().GlobalIdentity()
	if err != nil || id.Name != "Mallory" {
		t.Fatalf("external edit overwritten: %+v, %v", id, err)
	}
}

// The SSH connection probe keeps the git of the server: with a custom local
// git program, the parent ENV PATH choice and traces saved, Test Connection
// and Add against a loopback server never invoke the custom git, while the
// ssh of the probe runs with the exact process PATH and a remote command that
// carries the trace environment. The repository then opens with its real
// commits.
func TestGitPrefsSSHPreservesRemoteGitLoopbackSSH(t *testing.T) {
	sshRepo, srv := loopbackSSHRepo(t)
	wantHead := srv.RunGit(t, sshRepo.Path, "rev-parse", "HEAD")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find real git: %v", err)
	}
	probeDir := t.TempDir()
	gitLog := filepath.Join(probeDir, "git-log.txt")
	sshLog := filepath.Join(probeDir, "ssh-log.txt")
	for _, log := range []string{gitLog, sshLog} {
		if err := os.WriteFile(log, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wrapper := loggingShim(t, probeDir, "wrapped git", realGit, gitLog)
	// The ssh shim sits in front of the loopback wrapper on PATH and records
	// the environment and remote command of every ssh the product starts.
	shimDir := filepath.Join(probeDir, "shim-bin")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	loggingShim(t, shimDir, "ssh", srv.SSHProgram(), sshLog)
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	processPATH := os.Getenv("PATH")

	h := newTestApp(t)
	if err := h.app.saveGitToolchainSettings(wrapper, "parent", git.Trace{Git: true}); err != nil {
		t.Fatalf("save toolchain settings: %v", err)
	}

	openSSHForm(t, h)
	address := fmt.Sprintf("ssh://%s@%s:%d%s", srv.User, srv.Host, srv.Port, sshRepo.Path)
	typeSSHForm(t, h, address, srv.Identity, "")
	h.ui.Frame()
	if err := h.ui.Click("Test Connection"); err != nil {
		t.Fatalf("click Test Connection: %v", err)
	}
	h.ui.Frame()
	h.drain()

	st := sshFormState(h)
	if st.sshFailed {
		t.Fatalf("Test Connection failed: %q", st.sshResult)
	}
	if !strings.Contains(st.sshResult, "Connected:") || !strings.Contains(st.sshResult, "git version") {
		t.Fatalf("Test Connection result = %q, want Connected with the server's git version", st.sshResult)
	}

	// The custom local git program validated once at save time and was never
	// invoked by the SSH probe: the repository's git stays on the server.
	for _, line := range readLogLines(t, gitLog) {
		if !strings.Contains(line, "--version") {
			t.Fatalf("custom git program invoked during the SSH probe: %q", line)
		}
	}

	// The probe's ssh ran with the exact process PATH — the saved "parent"
	// ENV PATH choice applied — and the remote command carries the trace.
	sshLines := readLogLines(t, sshLog)
	if len(sshLines) == 0 {
		t.Fatal("the SSH probe never invoked ssh")
	}
	parentSeen, traceSeen := false, false
	for _, line := range sshLines {
		if strings.HasPrefix(line, "PATH="+processPATH+"\t") {
			parentSeen = true
			if strings.Contains(line, "GIT_TRACE=1") {
				traceSeen = true
			}
		}
	}
	if !parentSeen {
		t.Fatalf("no ssh invocation ran with the exact process PATH (parent mode); log=%q", sshLines)
	}
	if !traceSeen {
		t.Fatalf("no ssh remote command carries GIT_TRACE=1 (trace settings); log=%q", sshLines)
	}

	// Add saves and opens the repository with its real commits.
	if err := h.ui.Click("Add"); err != nil {
		t.Fatalf("click Add: %v", err)
	}
	h.ui.Frame()
	h.drain()
	st = sshFormState(h)
	if st.sshOpen {
		t.Fatalf("dialog stayed open after Add, result %q", st.sshResult)
	}
	if !h.app.session.HasRepo() || h.app.session.Meta().Kind != store.SSH {
		t.Fatalf("Add did not open the SSH repository: %+v", h.app.session.Meta())
	}
	if got := h.app.session.Snapshot().Status.Head; got != wantHead {
		t.Fatalf("opened HEAD = %q, want %q", got, wantHead)
	}
}
