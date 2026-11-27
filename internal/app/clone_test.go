package app

import (
	"crypto/subtle"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"net/http"
	"net/http/cgi"
	"net/http/httptest"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/store"
)

// stubPickCloneFolder replaces the Clone dialog's folder picker with fn for
// the test and restores the production picker afterwards.
func stubPickCloneFolder(t *testing.T, fn func() (string, error)) {
	t.Helper()
	prev := pickCloneFolderFn
	pickCloneFolderFn = func(_ *mygo.Window) (string, error) { return fn() }
	t.Cleanup(func() { pickCloneFolderFn = prev })
}

// blockCredentialHelpers keeps git's credential machinery away from the
// machine's real configuration: no system or global gitconfig, and an empty
// credential helper list, so nothing reads or writes the user's keychain.
// The starting assertion proves the guard itself works: credential.helper
// reads empty in exactly the environment the clone workers inherit. The two
// acceptable answers of git config --get-all are an empty line with exit
// code 0, or no output with exit code 1; anything else means the guard did
// not apply.
func blockCredentialHelpers(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
	t.Setenv("GIT_CONFIG_VALUE_0", "")

	cmd := exec.Command("git", "config", "--get-all", "credential.helper")
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("credential helper guard failed: credential.helper = %q", out)
	}
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			t.Fatalf("git config --get-all credential.helper: unexpected error %v (output %q)", err, out)
		}
	}
}

// openCloneDialog opens the Clone Repository dialog through the repository
// switcher popup, as a user does.
func openCloneDialog(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Clone Repository…"); err != nil {
		t.Fatalf("click Clone Repository: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("Repository URL") {
		t.Fatalf("Clone dialog did not open; texts=%q", h.ui.Texts())
	}
}

// replaceField replaces the content of the text field named name with text:
// click the field, select all, type over the selection.
func replaceField(t *testing.T, h *testHarness, name, text string) {
	t.Helper()
	focusField(t, h, name)
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.ui.Frame()
	h.ui.Type(text)
	h.ui.Frame()
}

// waitForFile polls for path to exist, bounded to the same 10 seconds the UI
// queue allows. The clone worker's blocking-git test needs the clone
// mid-flight before canceling it; there is no worker callback to wait on
// because the fake git never finishes.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

// waitQueueIdle waits until the UI queue has no active worker: a worker whose
// completion is already queued is done, so the test can deterministically
// close the dialog before the completion runs.
func waitQueueIdle(t *testing.T, h *testHarness) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		h.queue.mu.Lock()
		active := h.queue.active
		h.queue.mu.Unlock()
		if active == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the UI queue to go idle")
}

// newBareCloneSource builds a bare remote repository of the working repo f
// at path, with one initial commit, and returns the source HEAD.
func newBareCloneSource(t *testing.T, name, path string) string {
	t.Helper()
	f := newGitRepo(t, name)
	head := f.CommitFile("a.txt", "one\n", "initial commit")
	runGitCommand(t, "", "init", "--bare", "-b", "main", path)
	f.Git("push", path, "HEAD:main")
	return head
}

// cloningInto drives the dialog: type the URL, replace the parent folder,
// click Clone, and drain the worker.
func cloningInto(t *testing.T, h *testHarness, url, parent string) {
	t.Helper()
	h.ui.Type(url)
	h.ui.Frame()
	replaceField(t, h, "Parent Folder", parent)
	if err := h.ui.Click("Clone"); err != nil {
		t.Fatalf("click Clone: %v", err)
	}
	h.ui.Frame()
	h.drain()
}

// The Clone Repository sheet clones a local bare repository into the chosen
// parent folder and opens the clone: its HEAD matches the source, the
// session and the store point at the target path, the dialog closes, and
// cloning worked from the empty state with no repository open.
func TestCloneSuccessOpensRepository(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "up.git")
	wantHead := newBareCloneSource(t, "up", bare)
	parent := filepath.Join(root, "code")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	h := newTestApp(t)
	closeSync(t, h)
	if !h.ui.HasText("No Repository Open") {
		t.Fatalf("test did not start in the empty state; texts=%q", h.ui.Texts())
	}

	openCloneDialog(t, h)
	target := filepath.Join(parent, "up")
	cloningInto(t, h, bare, parent)

	if h.app.cloneUI().open {
		t.Fatalf("dialog stayed open after a successful clone; err %q", h.app.cloneUI().err)
	}
	got := strings.TrimSpace(runGitCommand(t, target, "rev-parse", "HEAD"))
	if got != wantHead {
		t.Fatalf("clone HEAD = %q, want %q", got, wantHead)
	}
	if !h.app.session.HasRepo() {
		t.Fatal("no repository open after cloning")
	}
	if meta := h.app.session.Meta(); meta.Path != target || meta.Kind != store.Local {
		t.Fatalf("session meta = %+v, want local repo at %q", meta, target)
	}
	if got := h.app.session.Snapshot().Status.Head; got != wantHead {
		t.Fatalf("session HEAD = %q, want %q", got, wantHead)
	}
	repos := h.store.Repos()
	if len(repos) != 1 || repos[0].Path != target || repos[0].Kind != store.Local {
		t.Fatalf("store repos = %+v, want one local entry at %q", repos, target)
	}
	if repos[0].Name != "up" {
		t.Fatalf("stored name = %q, want up", repos[0].Name)
	}
}

// The URL fills the Folder Name field until the user edits it by hand; a
// hand-edited name survives later URL edits, and reopening the dialog starts
// the autofill over.
func TestCloneNameAutofillUntilEdited(t *testing.T) {
	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialog(t, h)

	h.ui.Type("https://example.com/owner/alpha.git")
	h.ui.Frame()
	if got := h.app.cloneUI().name; got != "alpha" {
		t.Fatalf("name after typing URL = %q, want alpha", got)
	}
	if !h.ui.HasText("Clones into ") || !h.ui.HasText("/alpha") {
		t.Fatalf("Clones into preview missing the auto-filled name; texts=%q", h.ui.Texts())
	}

	// Typing a different URL re-fills the still untouched name.
	replaceField(t, h, "Repository URL", "https://example.com/owner/beta.git")
	if got := h.app.cloneUI().name; got != "beta" {
		t.Fatalf("name after re-typing URL = %q, want beta", got)
	}

	// A hand-edited name is no longer overwritten.
	replaceField(t, h, "Folder Name", "myname")
	if !h.app.cloneUI().nameEdited {
		t.Fatal("editing the name field did not mark it edited")
	}
	replaceField(t, h, "Repository URL", "https://example.com/owner/gamma.git")
	if got := h.app.cloneUI().name; got != "myname" {
		t.Fatalf("hand-edited name overwritten by URL edit: %q", got)
	}

	// Reopening the dialog resets the autofill.
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	openCloneDialog(t, h)
	h.ui.Type("https://example.com/owner/delta.git")
	h.ui.Frame()
	if got := h.app.cloneUI().name; got != "delta" {
		t.Fatalf("name after reopening = %q, want delta", got)
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}

// Clone stays disabled while the URL is empty, the parent folder is empty,
// or the folder name fails git's check; it enables only when all three are
// usable.
func TestCloneButtonDisabledStates(t *testing.T) {
	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialog(t, h)

	cloneEnabled := func() bool {
		info, ok := h.ui.AccessByRole(ui.RoleButton, "Clone")
		if !ok {
			t.Fatalf("no Clone button; texts=%q", h.ui.Texts())
		}
		return !info.Disabled
	}

	// Empty URL disables even with parent and a valid name.
	replaceField(t, h, "Folder Name", "somename")
	if cloneEnabled() {
		t.Fatal("Clone enabled with an empty URL")
	}

	// A valid URL and parent with a broken name disables.
	replaceField(t, h, "Repository URL", "https://example.com/owner/repo.git")
	parent := t.TempDir()
	replaceField(t, h, "Parent Folder", parent)
	for _, bad := range []string{"..", "a/b", "-x"} {
		replaceField(t, h, "Folder Name", bad)
		if cloneEnabled() {
			t.Fatalf("Clone enabled with folder name %q", bad)
		}
	}

	// A valid name but an empty parent disables.
	replaceField(t, h, "Folder Name", "goodname")
	replaceField(t, h, "Parent Folder", "")
	if cloneEnabled() {
		t.Fatal("Clone enabled with an empty parent folder")
	}

	// Everything valid enables.
	replaceField(t, h, "Parent Folder", parent)
	if !cloneEnabled() {
		t.Fatal("Clone disabled with a valid URL, parent and name")
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}

// A parent folder that is not absolute after expanding ~ fails before git
// runs: the dialog stays open with the error and nothing is created.
func TestCloneParentNotAbsolute(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "up.git")
	newBareCloneSource(t, "up", bare)

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialog(t, h)
	cloningInto(t, h, bare, "relative/code")

	st := h.app.cloneUI()
	if !st.open {
		t.Fatal("dialog closed on a non-absolute parent folder")
	}
	if st.busy {
		t.Fatal("dialog stayed busy after the validation error")
	}
	if !strings.Contains(st.err, "absolute path") {
		t.Fatalf("error = %q, want the absolute path requirement", st.err)
	}
	if h.ui.HasText("Cloning…") {
		t.Fatal("busy indicator showing after the failed validation")
	}
	if _, err := os.Stat("relative"); err == nil {
		t.Fatal("a relative folder was created in the working directory")
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("store gained an entry: %+v", h.store.Repos())
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}

// An existing target is never cloned into and never touched: the dialog
// reports "<path> already exists" and the directory keeps its content.
func TestCloneTargetExistsUntouched(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "up.git")
	newBareCloneSource(t, "up", bare)
	parent := filepath.Join(root, "code")
	target := filepath.Join(parent, "up")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "sentinel.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialog(t, h)
	cloningInto(t, h, bare, parent)

	st := h.app.cloneUI()
	if !st.open {
		t.Fatal("dialog closed when the target existed")
	}
	if !strings.Contains(st.err, target+" already exists") {
		t.Fatalf("error = %q, want %q already exists", st.err, target)
	}
	// The directory and its content are byte for byte what they were.
	got, err := os.ReadFile(filepath.Join(target, "sentinel.txt"))
	if err != nil || string(got) != "keep me\n" {
		t.Fatalf("sentinel after refused clone = %q err %v, want intact", got, err)
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); err == nil {
		t.Fatal("the clone wrote into the existing target")
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("store gained an entry: %+v", h.store.Repos())
	}
	if h.app.session.HasRepo() {
		t.Fatal("a repository opened when the clone was refused")
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}

// A parent folder that does not exist yet is created with the folders above
// it before the clone runs.
func TestCloneCreatesMissingParentFolder(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "up.git")
	wantHead := newBareCloneSource(t, "up", bare)
	parent := filepath.Join(root, "deep", "nested", "code")

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialog(t, h)
	target := filepath.Join(parent, "up")
	cloningInto(t, h, bare, parent)

	if h.app.cloneUI().err != "" || h.app.cloneUI().open {
		t.Fatalf("clone failed or dialog open: err %q", h.app.cloneUI().err)
	}
	got := strings.TrimSpace(runGitCommand(t, target, "rev-parse", "HEAD"))
	if got != wantHead {
		t.Fatalf("clone HEAD = %q, want %q", got, wantHead)
	}
	if got := h.app.session.Meta().Path; got != target {
		t.Fatalf("opened path = %q, want %q", got, target)
	}
}

// Choose fills the parent field with the picker's folder and canceling the
// picker leaves the field unchanged.
func TestCloneChooseFolderPicker(t *testing.T) {
	var picked func() (string, error)
	stubPickCloneFolder(t, func() (string, error) { return picked() })

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialog(t, h)

	// Choose fills the field.
	chosen := filepath.Join(t.TempDir(), "chosenparent")
	picked = func() (string, error) { return chosen, nil }
	if err := h.ui.Click("Choose…"); err != nil {
		t.Fatalf("click Choose: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if got := h.app.cloneUI().parent; got != chosen {
		t.Fatalf("parent after Choose = %q, want %q", got, chosen)
	}
	if !h.ui.HasText("Clones into " + chosen) {
		t.Fatalf("Clones into preview missing the chosen parent; texts=%q", h.ui.Texts())
	}

	// Canceling the picker leaves the field unchanged.
	picked = func() (string, error) { return "", nil }
	if err := h.ui.Click("Choose…"); err != nil {
		t.Fatalf("click Choose again: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if got := h.app.cloneUI().parent; got != chosen {
		t.Fatalf("canceling the picker changed the parent to %q, want %q", got, chosen)
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}

// A URL git cannot clone keeps the dialog open with the connection error,
// re-enables the fields, and leaves no target directory behind.
func TestCloneInvalidURLShowsError(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "no-such-repository")
	parent := filepath.Join(root, "code")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialog(t, h)
	cloningInto(t, h, missing, parent)

	st := h.app.cloneUI()
	if !st.open {
		t.Fatal("dialog closed on a failed clone")
	}
	if st.busy {
		t.Fatal("dialog stayed busy after the failure")
	}
	if st.err == "" {
		t.Fatal("no error shown for the failed clone")
	}
	if h.ui.HasText("Cloning…") {
		t.Fatal("busy indicator showing after the failure")
	}
	// The fields are editable again: the tester can click into them.
	focusField(t, h, "Repository URL")
	if _, err := os.Stat(filepath.Join(parent, "no-such-repository")); err == nil {
		t.Fatal("the failed clone left a target directory")
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("store gained an entry: %+v", h.store.Repos())
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}

// writeBlockingFakeGit writes a fake git program standing in for a slow
// network clone: for clone invocations it makes the clone's target folder,
// writes its pid to pidFile through a temporary file and an atomic rename —
// so the pid file never reads empty or partial — and execs a sleep, which
// canceling kills without leaving a shell's child behind. Anything but a
// clone hands through to the real git. It returns the program's path.
func writeBlockingFakeGit(t *testing.T, root, pidFile string) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git on PATH: %v", err)
	}
	fake := filepath.Join(root, "fakegit")
	script := "#!/bin/sh\n" +
		"isclone=\"\"\nfor a do [ \"$a\" = clone ] && isclone=yes; done\n" +
		"if [ -z \"$isclone\" ]; then exec " + shellQ(realGit) + " \"$@\"; fi\n" +
		"# -C is the executor's directory; the clone's folder is the last argument.\n" +
		"dir=\"\"\nprev=\"\"\nlast=\"\"\nfor a do\n  case $prev in -C) dir=$a ;; esac\n  prev=$a\n  last=$a\ndone\n" +
		"cd \"$dir\" || exit 1\n" +
		"mkdir -p \"$last\"\n" +
		"printf '%s\\n' \"$$\" > " + shellQ(pidFile+".tmp") + " && mv -f " + shellQ(pidFile+".tmp") + " " + shellQ(pidFile) + "\n" +
		"exec sleep 47\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	return fake
}

// readNumericPID reads a pid file written by the fake git and validates it
// holds digits only, so it can safely interpolate into the probe command.
func readNumericPID(t *testing.T, pidFile string) string {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read clone pid: %v", err)
	}
	pid := strings.TrimSpace(string(b))
	for _, r := range pid {
		if r < '0' || r > '9' {
			t.Fatalf("clone pid %q is not numeric", pid)
		}
	}
	if pid == "" {
		t.Fatalf("clone pid file %s is empty", pidFile)
	}
	return pid
}

// assertProcessGone reports the process is reaped: no sleep of the canceled
// clone survives the worker.
func assertProcessGone(t *testing.T, pid string) {
	t.Helper()
	out, err := exec.Command("sh", "-c", "kill -0 "+pid+" 2>/dev/null && echo alive || echo gone").Output()
	if err != nil || strings.TrimSpace(string(out)) != "gone" {
		t.Fatalf("clone process %s = %q err %v, want gone", pid, out, err)
	}
}

// useFakeGitPath points the app's git program setting at fake.
func useFakeGitPath(t *testing.T, h *testHarness, fake string) {
	t.Helper()
	prefs := h.store.Settings()
	prefs.GitPath = fake
	if err := h.store.SetSettings(prefs); err != nil {
		t.Fatalf("save settings: %v", err)
	}
}

// Canceling mid-clone — with Cancel or with Escape — stops git and removes
// the folder the clone had started: the dialog closes, no process of the
// fake git survives, and the dropped completion neither errors a reopened
// dialog nor opens anything.
func TestCloneCancelRemovesPartialClone(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "code")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	pidFile := filepath.Join(parent, "clone.pid")
	fake := writeBlockingFakeGit(t, root, pidFile)

	h := newTestApp(t)
	closeSync(t, h)
	useFakeGitPath(t, h, fake)

	// startBlockingClone drives the dialog into a mid-flight clone and waits
	// until the fake git reported itself: the pid file appears only after the
	// target folder exists, and its content is complete because the script
	// publishes it with an atomic rename.
	startBlockingClone := func(url, target string) {
		t.Helper()
		openCloneDialog(t, h)
		h.ui.Type(url)
		h.ui.Frame()
		replaceField(t, h, "Parent Folder", parent)
		if err := h.ui.Click("Clone"); err != nil {
			t.Fatalf("click Clone: %v", err)
		}
		h.ui.Frame()
		waitForFile(t, pidFile)
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("the mid-flight clone has no target folder %s: %v", target, err)
		}
		if !h.ui.HasText("Cloning…") {
			t.Fatalf("busy indicator missing mid-clone; texts=%q", h.ui.Texts())
		}
		if info, ok := h.ui.AccessByRole(ui.RoleButton, "Clone"); !ok || !info.Disabled {
			t.Fatalf("Clone mid-clone = %+v (found %v), want disabled", info, ok)
		}
	}

	// assertCanceledClone asserts the dialog closed, the partial folder is
	// gone, the fake git is reaped and nothing was saved or opened.
	assertCanceledClone := func(target string) {
		t.Helper()
		if h.app.cloneUI().open {
			t.Fatal("dialog stayed open after canceling the clone")
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("the canceled clone left its folder: err %v", err)
		}
		assertProcessGone(t, readNumericPID(t, pidFile))
		if len(h.store.Repos()) != 0 {
			t.Fatalf("store gained an entry: %+v", h.store.Repos())
		}
		if h.app.session.HasRepo() {
			t.Fatal("a repository opened from the canceled clone")
		}
	}

	target := filepath.Join(parent, "repo")
	startBlockingClone("https://example.com/slow/repo.git", target)
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel mid-clone: %v", err)
	}
	h.ui.Frame()
	h.drain()
	assertCanceledClone(target)

	// Escape cancels the same way.
	if err := os.Remove(pidFile); err != nil {
		t.Fatalf("remove first clone pid file: %v", err)
	}
	target2 := filepath.Join(parent, "repo2")
	startBlockingClone("https://example.com/slow/repo2.git", target2)
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	h.drain()
	assertCanceledClone(target2)
}

// With UpdateSubmodules on, the clone initializes the repository's
// submodules: the submodule's file is in the clone's working tree. The
// submodule URL is a local path, which git refuses without
// protocol.file.allow=always; the test process's git configuration carries
// it through GIT_CONFIG_COUNT, never a config file.
func TestCloneSubmodulesInitialized(t *testing.T) {
	blockCredentialHelpers(t)
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_1", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_1", "always")
	root := t.TempDir()

	// The submodule source.
	sub := newGitRepo(t, "subsrc")
	sub.CommitFile("subfile.txt", "sub content\n", "sub commit")
	subBare := filepath.Join(root, "subsrc.git")
	runGitCommand(t, "", "init", "--bare", "-b", "main", subBare)
	sub.Git("push", subBare, "HEAD:main")

	// The superproject refers to the submodule by its local path.
	super := newGitRepo(t, "supersrc")
	super.CommitFile("main.txt", "main\n", "main commit")
	super.Git("-c", "protocol.file.allow=always", "submodule", "add", "--", subBare, "deps/subsrc")
	super.Git("commit", "-m", "add submodule")
	superBare := filepath.Join(root, "supersrc.git")
	runGitCommand(t, "", "init", "--bare", "-b", "main", superBare)
	super.Git("push", superBare, "HEAD:main")

	// Cloning the bare superproject must check the submodule out.
	parent := filepath.Join(root, "code")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	h := newTestApp(t)
	closeSync(t, h)
	p := h.store.Settings()
	p.UpdateSubmodules = true
	if err := h.store.SetSettings(p); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	openCloneDialog(t, h)
	target := filepath.Join(parent, "supersrc")
	cloningInto(t, h, superBare, parent)

	if h.app.cloneUI().open || h.app.cloneUI().err != "" {
		t.Fatalf("clone failed or dialog open: err %q", h.app.cloneUI().err)
	}
	got, err := os.ReadFile(filepath.Join(target, "deps", "subsrc", "subfile.txt"))
	if err != nil || string(got) != "sub content\n" {
		t.Fatalf("submodule file = %q err %v, want checked out content", got, err)
	}
	if out := strings.TrimSpace(runGitCommand(t, target, "submodule", "status")); !strings.Contains(out, "deps/subsrc") || strings.HasPrefix(out, "-") {
		t.Fatalf("submodule status = %q, want initialized", out)
	}
}

// A parent folder written with ~/ clones into the expanded home folder.
// The test home is a temporary directory so nothing touches the real one.
func TestCloneParentTildeExpansion(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "up.git")
	wantHead := newBareCloneSource(t, "up", bare)

	home := filepath.Join(root, "home")
	parent := filepath.Join(home, "clones")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir fake home parent: %v", err)
	}
	t.Setenv("HOME", home)
	prev := lastCloneParent
	lastCloneParent = ""
	t.Cleanup(func() { lastCloneParent = prev })

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialog(t, h)
	target := filepath.Join(parent, "up")
	cloningInto(t, h, bare, "~/clones")

	if h.app.cloneUI().open || h.app.cloneUI().err != "" {
		t.Fatalf("clone with ~/ failed or dialog open: err %q", h.app.cloneUI().err)
	}
	got := strings.TrimSpace(runGitCommand(t, target, "rev-parse", "HEAD"))
	if got != wantHead {
		t.Fatalf("clone HEAD = %q, want %q", got, wantHead)
	}
	if meta := h.app.session.Meta(); meta.Path != target {
		t.Fatalf("session path = %q, want %q", meta.Path, target)
	}
}

// The clone entry is available while the open repository is busy: with a
// fetch running behind the worker gate, the dialog opens from the switcher
// and the clone completes independently of the repository session.
func TestCloneWorksWhileRepositoryBusy(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "up.git")
	wantHead := newBareCloneSource(t, "up", bare)
	parent := filepath.Join(root, "code")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	// A busy repository: a local checkout with a bare remote mid-fetch.
	repo := newGitRepo(t, "busyrepo")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	h.queue.PauseWorkers()
	if err := h.ui.ClickWith(ui.Alt, "Fetch"); err != nil {
		t.Fatalf("click Fetch: %v", err)
	}
	h.ui.Frame()
	fetchWorker := h.queue.WaitWorker(t)
	if !h.app.session.Busy() {
		t.Fatal("repository not busy with the gated fetch")
	}

	// The switcher entry opens the clone dialog regardless.
	openCloneDialog(t, h)
	h.ui.Type(bare)
	h.ui.Frame()
	replaceField(t, h, "Parent Folder", parent)
	if err := h.ui.Click("Clone"); err != nil {
		t.Fatalf("click Clone while the repository is busy: %v", err)
	}
	h.ui.Frame()
	cloneWorker := h.queue.WaitWorker(t)
	h.queue.ReleaseWorker(cloneWorker)
	h.queue.ReleaseWorker(fetchWorker)
	h.queue.ResumeWorkers()
	h.drain()

	if h.app.cloneUI().open || h.app.cloneUI().err != "" {
		t.Fatalf("clone failed or dialog open: err %q", h.app.cloneUI().err)
	}
	target := filepath.Join(parent, "up")
	got := strings.TrimSpace(runGitCommand(t, target, "rev-parse", "HEAD"))
	if got != wantHead {
		t.Fatalf("clone HEAD = %q, want %q", got, wantHead)
	}
	if meta := h.app.session.Meta(); meta.Path != target {
		t.Fatalf("session path = %q, want %q", meta.Path, target)
	}
	if repos := h.store.Repos(); len(repos) != 2 {
		t.Fatalf("store repos = %+v, want the clone plus the busy repository", repos)
	}
}

// A parent folder of exactly "/" keeps its leading slash in the "Clones
// into" preview instead of collapsing into the bare folder name. The input
// never clones there: the assertion covers the preview line only.
func TestCloneTargetDisplayRootParent(t *testing.T) {
	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialog(t, h)
	h.ui.Type("https://example.com/owner/repo.git")
	h.ui.Frame()
	replaceField(t, h, "Parent Folder", "/")
	if !h.ui.HasText("Clones into /repo") {
		t.Fatalf("preview missing the root parent; texts=%q", h.ui.Texts())
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}

// A failed clone of a URL carrying a plaintext password never shows the
// password: the dialog error is the redacted form, and no text on screen
// contains it. The fake git fails every clone with git's own wording, the
// URL quoted into stderr exactly as real git prints it.
func TestCloneErrorRedactsURLPassword(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "code")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	const password = "clonesecretpw"
	url := "https://cloneuser:" + password + "@example.com/repo.git"

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git on PATH: %v", err)
	}
	fake := filepath.Join(root, "fakegit")
	script := "#!/bin/sh\n" +
		"isclone=\"\"\nfor a do [ \"$a\" = clone ] && isclone=yes; done\n" +
		"if [ -z \"$isclone\" ]; then exec " + shellQ(realGit) + " \"$@\"; fi\n" +
		"url=\"\"\nprev=\"\"\nfor a do\n  case $prev in --) [ -z \"$url\" ] && url=$a ;; esac\n  prev=$a\ndone\n" +
		"# git prints the failing URL — userinfo and all — into the fatal line.\n" +
		"echo \"fatal: unable to access '$url/': The requested URL returned error: 403\" >&2\n" +
		"exit 128\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}

	h := newTestApp(t)
	closeSync(t, h)
	useFakeGitPath(t, h, fake)
	openCloneDialog(t, h)
	cloningInto(t, h, url, parent)

	st := h.app.cloneUI()
	if !st.open {
		t.Fatal("dialog closed on the failed clone")
	}
	if st.err == "" {
		t.Fatal("no error shown for the failed clone")
	}
	if strings.Contains(st.err, password) {
		t.Fatalf("dialog error leaks the URL password: %q", st.err)
	}
	for _, text := range h.ui.Texts() {
		if strings.Contains(text, password) {
			t.Fatalf("on-screen text leaks the URL password: %q", text)
		}
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}

// The parent folder of a successful clone is the next dialog's default; a
// clone also works while another repository is open.
func TestCloneRemembersLastParent(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "up.git")
	newBareCloneSource(t, "up", bare)
	parentA := filepath.Join(root, "first")
	parentB := filepath.Join(root, "second")
	if err := os.Mkdir(parentA, 0o755); err != nil {
		t.Fatalf("mkdir first: %v", err)
	}
	if err := os.Mkdir(parentB, 0o755); err != nil {
		t.Fatalf("mkdir second: %v", err)
	}

	// Pin the remembered parent so the test does not depend on test order.
	prev := lastCloneParent
	lastCloneParent = ""
	t.Cleanup(func() { lastCloneParent = prev })

	// Another repository is already open: cloning still works.
	other := newGitRepo(t, "otherrepo")
	other.CommitFile("o.txt", "o\n", "other commit")
	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(other.StoreRepo())
	h.drain()
	if !h.app.session.HasRepo() {
		t.Fatal("the pre-opened repository is not open")
	}

	// Without a remembered parent the dialog starts at the home folder.
	openCloneDialog(t, h)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("user home: %v", err)
	}
	if got := h.app.cloneUI().parent; got != home {
		t.Fatalf("initial parent = %q, want home %q", got, home)
	}

	// Clone into the first parent: it is remembered.
	cloningInto(t, h, bare, parentA)
	if h.app.cloneUI().open {
		t.Fatalf("first clone failed: err %q", h.app.cloneUI().err)
	}
	if got := h.app.session.Meta().Path; got != filepath.Join(parentA, "up") {
		t.Fatalf("opened path = %q, want %q", got, filepath.Join(parentA, "up"))
	}
	if lastCloneParent != parentA {
		t.Fatalf("remembered parent = %q, want %q", lastCloneParent, parentA)
	}

	// The next dialog starts at the remembered parent.
	openCloneDialog(t, h)
	if got := h.app.cloneUI().parent; got != parentA {
		t.Fatalf("reopened parent = %q, want %q", got, parentA)
	}
	// ...until the user clones elsewhere.
	h.ui.Type(bare)
	h.ui.Frame()
	replaceField(t, h, "Parent Folder", parentB)
	replaceField(t, h, "Folder Name", "secondcopy")
	if err := h.ui.Click("Clone"); err != nil {
		t.Fatalf("click Clone second time: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if h.app.cloneUI().open {
		t.Fatalf("second clone failed: err %q", h.app.cloneUI().err)
	}
	if lastCloneParent != parentB {
		t.Fatalf("remembered parent after second clone = %q, want %q", lastCloneParent, parentB)
	}
	if got := h.app.session.Meta().Path; got != filepath.Join(parentB, "secondcopy") {
		t.Fatalf("opened path after second clone = %q", got)
	}
}

// Closing the dialog and reopening it discards the old worker's completion —
// even when the old clone finished before the close, so its result is a
// genuine error or a success rather than the cancellation the close itself
// causes. A stale error completion neither publishes into the reopened dialog
// nor clears the busy state of the clone running there, and a stale success
// completion never closes the dialog or opens the clone.
func TestCloneDiscardStaleResultAfterReopen(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "up.git")
	newBareCloneSource(t, "up", bare)
	parent := filepath.Join(root, "code")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	t.Run("stale error does not disturb a busy reopened clone", func(t *testing.T) {
		// A subdirectory per phase keeps the fake git's pid files apart.
		phase := t.TempDir()
		if err := os.MkdirAll(filepath.Join(phase, "code"), 0o755); err != nil {
			t.Fatalf("mkdir phase parent: %v", err)
		}
		phaseParent := filepath.Join(phase, "code")
		pidFile := filepath.Join(phaseParent, "clone.pid")
		fake := writeBlockingFakeGit(t, phase, pidFile)

		h := newTestApp(t)
		closeSync(t, h)

		// Start a clone that fails fast — a missing source path — and let the
		// worker finish. The completion is queued but not run: the dialog is
		// still open while git's error outcome is already decided. This clone
		// uses the real git; the blocking fake git is installed only for the
		// second clone below.
		openCloneDialog(t, h)
		h.ui.Type(filepath.Join(phase, "no-such-source"))
		h.ui.Frame()
		replaceField(t, h, "Parent Folder", phaseParent)
		if err := h.ui.Click("Clone"); err != nil {
			t.Fatalf("click Clone: %v", err)
		}
		h.ui.Frame()
		waitQueueIdle(t, h)

		// Close the dialog and reopen it. The close cancels the finished
		// worker's context — harmlessly, its clone already returned a genuine
		// error before that — and the reopened dialog starts a fresh draft.
		h.ui.Key(0, ui.KeyEscape)
		h.ui.Frame()
		if h.app.cloneUI().open {
			t.Fatal("Escape left the dialog open")
		}
		openCloneDialog(t, h)
		if got := h.app.cloneUI().url; got != "" {
			t.Fatalf("reopened dialog kept the old URL %q", got)
		}

		// Start a second clone in the reopened dialog, this one blocking
		// mid-flight behind the fake git.
		useFakeGitPath(t, h, fake)
		h.ui.Type("https://example.com/slow/repo.git")
		h.ui.Frame()
		replaceField(t, h, "Parent Folder", phaseParent)
		if err := h.ui.Click("Clone"); err != nil {
			t.Fatalf("click Clone in reopened dialog: %v", err)
		}
		h.ui.Frame()
		waitForFile(t, pidFile)
		st := h.app.cloneUI()
		if !st.busy || st.cancel == nil {
			t.Fatal("reopened dialog is not busy with the blocking clone")
		}

		// Run the stale error completion now: it must neither publish its
		// error nor clear the busy state and cancel handle of the new clone.
		h.queue.RunNext(t)
		h.ui.Frame()
		st = h.app.cloneUI()
		if !st.busy || st.cancel == nil {
			t.Fatalf("stale completion cleared the new clone's busy state: busy %v cancel %v", st.busy, st.cancel != nil)
		}
		if st.err != "" {
			t.Fatalf("stale completion published its error into the reopened dialog: %q", st.err)
		}
		if !st.open {
			t.Fatal("stale completion closed the reopened dialog")
		}

		// The new clone still really cancels: the busy state was genuinely
		// its own.
		if err := h.ui.Click("Cancel"); err != nil {
			t.Fatalf("click Cancel in reopened dialog: %v", err)
		}
		h.ui.Frame()
		h.drain()
		if _, err := os.Stat(filepath.Join(phaseParent, "repo")); !os.IsNotExist(err) {
			t.Fatalf("the canceled clone left its folder: err %v", err)
		}
		if len(h.store.Repos()) != 0 {
			t.Fatalf("store gained an entry: %+v", h.store.Repos())
		}
	})

	t.Run("stale success neither opens nor closes", func(t *testing.T) {
		h := newTestApp(t)
		closeSync(t, h)

		// A successful clone finishing before the dialog closes. waitQueueIdle
		// guarantees the worker completed — the clone on disk is real — while
		// its completion callback is still queued.
		openCloneDialog(t, h)
		h.ui.Type(bare)
		h.ui.Frame()
		replaceField(t, h, "Parent Folder", parent)
		if err := h.ui.Click("Clone"); err != nil {
			t.Fatalf("click Clone: %v", err)
		}
		h.ui.Frame()
		waitQueueIdle(t, h)
		if _, err := os.Stat(filepath.Join(parent, "up", ".git")); err != nil {
			t.Fatalf("the finished clone is not on disk: %v", err)
		}

		// Close and reopen before the completion runs.
		h.ui.Key(0, ui.KeyEscape)
		h.ui.Frame()
		openCloneDialog(t, h)
		if got := h.app.cloneUI().url; got != "" {
			t.Fatalf("reopened dialog kept the old URL %q", got)
		}

		// The stale success completion must not open the clone, close the
		// reopened dialog, or touch the store.
		h.drain()
		st := h.app.cloneUI()
		if !st.open {
			t.Fatal("stale success completion closed the reopened dialog")
		}
		if st.err != "" {
			t.Fatalf("stale success completion published an error: %q", st.err)
		}
		if h.app.session.HasRepo() {
			t.Fatal("stale success completion opened the clone")
		}
		if len(h.store.Repos()) != 0 {
			t.Fatalf("store gained an entry: %+v", h.store.Repos())
		}
		h.ui.Key(0, ui.KeyEscape)
		h.ui.Frame()
	})
}

// A folder picker finishing after the dialog closed and reopened never
// publishes its folder into the new draft: the completion carries the
// generation of the dialog it was started in.
func TestCloneFolderPickerDiscardsStaleResult(t *testing.T) {
	var picked func() (string, error)
	stubPickCloneFolder(t, func() (string, error) { return picked() })
	chosen := filepath.Join(t.TempDir(), "latepick")
	picked = func() (string, error) { return chosen, nil }

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialog(t, h)

	// Start the picker behind the worker gate, close the dialog before it
	// runs, and reopen a fresh draft.
	h.queue.PauseWorkers()
	if err := h.ui.Click("Choose…"); err != nil {
		t.Fatalf("click Choose: %v", err)
	}
	h.ui.Frame()
	worker := h.queue.WaitWorker(t)
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	h.queue.ResumeWorkers()

	openCloneDialog(t, h)
	draftParent := h.app.cloneUI().parent
	// The fresh draft starts at the home or remembered folder, never the
	// folder the picker has not even returned yet.
	if draftParent == chosen {
		t.Fatalf("reopened dialog already holds the picker folder %q", chosen)
	}
	h.queue.ReleaseWorker(worker)
	h.drain()

	if got := h.app.cloneUI().parent; got != draftParent {
		t.Fatalf("stale picker completion changed the parent to %q, want %q", got, draftParent)
	}
	if h.app.cloneUI().parent == chosen {
		t.Fatalf("stale picker completion published %q into the reopened dialog", chosen)
	}

	// The picker still works in the new dialog: the same folder arrives when
	// the dialog is the one the picker was started from.
	if err := h.ui.Click("Choose…"); err != nil {
		t.Fatalf("click Choose again: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if got := h.app.cloneUI().parent; got != chosen {
		t.Fatalf("parent after a live Choose = %q, want %q", got, chosen)
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}

// httpGitServer serves bareRoot over the smart HTTP protocol behind Basic
// authentication, as a minimal hosting service: git http-backend through the
// Go CGI runner. It returns the server; URLs are server.URL + "/" + name.
func httpGitServer(t *testing.T, bareRoot, wantUser, wantPassword string) *httptest.Server {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git on PATH: %v", err)
	}
	handler := &cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + bareRoot,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), []byte(wantUser)) != 1 ||
			subtle.ConstantTimeCompare([]byte(password), []byte(wantPassword)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="clone-test"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// answerAskPass waits for the askpass dialog, types the answer into it and
// confirms with OK.
func answerAskPass(t *testing.T, h *testHarness, answer string) {
	t.Helper()
	waitAskPass(t, h)
	h.ui.Type(answer)
	h.ui.Frame()
	if err := h.ui.Click("OK"); err != nil {
		t.Fatalf("click OK in askpass dialog: %v", err)
	}
	h.ui.Frame()
}

// Cloning an HTTPS URL that requires Basic authentication asks the user and
// password through the askpass dialog — shown over the clone dialog — and
// the answered credentials complete the clone.
func TestCloneHTTPSAskPassAnswers(t *testing.T) {
	blockCredentialHelpers(t)
	root := t.TempDir()
	bare := filepath.Join(root, "asked.git")
	wantHead := newBareCloneSource(t, "asked", bare)
	srv := httpGitServer(t, root, "cloneuser", "clonepass")
	parent := filepath.Join(root, "code")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialog(t, h)
	h.ui.Type(srv.URL + "/asked.git")
	h.ui.Frame()
	replaceField(t, h, "Parent Folder", parent)
	if err := h.ui.Click("Clone"); err != nil {
		t.Fatalf("click Clone: %v", err)
	}
	h.ui.Frame()

	// git asks the user name first, then the password. While the question
	// waits, the clone dialog stays up behind the askpass dialog, still
	// showing its busy state — the askpass modal covers it without closing it.
	waitAskPass(t, h)
	if !h.ui.HasText("The remote host requires user name") {
		t.Fatalf("user name dialog missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Cloning…") {
		t.Fatalf("clone dialog not showing behind the askpass dialog; texts=%q", h.ui.Texts())
	}
	h.ui.Type("cloneuser")
	h.ui.Frame()
	if err := h.ui.Click("OK"); err != nil {
		t.Fatalf("click OK in user name dialog: %v", err)
	}
	h.ui.Frame()
	answerAskPass(t, h, "clonepass")
	h.drain()

	if h.app.cloneUI().open {
		t.Fatalf("dialog stayed open after the authenticated clone; err %q", h.app.cloneUI().err)
	}
	target := filepath.Join(parent, "asked")
	got := strings.TrimSpace(runGitCommand(t, target, "rev-parse", "HEAD"))
	if got != wantHead {
		t.Fatalf("clone HEAD = %q, want %q", got, wantHead)
	}
	if meta := h.app.session.Meta(); meta.Path != target {
		t.Fatalf("session path = %q, want %q", meta.Path, target)
	}
	if repos := h.store.Repos(); len(repos) != 1 || repos[0].Path != target {
		t.Fatalf("store repos = %+v, want one entry at %q", repos, target)
	}
}

// Canceling the askpass question fails the clone: the dialog shows the
// error, no repository opens, and the target directory does not exist —
// git removes the folder of a clone that never finished.
func TestCloneHTTPSAskPassCancelFails(t *testing.T) {
	blockCredentialHelpers(t)
	root := t.TempDir()
	bare := filepath.Join(root, "denied.git")
	newBareCloneSource(t, "denied", bare)
	srv := httpGitServer(t, root, "cloneuser", "clonepass")
	parent := filepath.Join(root, "code")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialog(t, h)
	h.ui.Type(srv.URL + "/denied.git")
	h.ui.Frame()
	replaceField(t, h, "Parent Folder", parent)
	if err := h.ui.Click("Clone"); err != nil {
		t.Fatalf("click Clone: %v", err)
	}
	h.ui.Frame()

	// The user name question arrives first; Escape dismisses the askpass
	// dialog on top, answering cancel.
	waitAskPass(t, h)
	if !h.ui.HasText("The remote host requires user name") {
		t.Fatalf("user name dialog missing; texts=%q", h.ui.Texts())
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	h.drain()

	st := h.app.cloneUI()
	if !st.open {
		t.Fatal("clone dialog closed when the clone failed")
	}
	if st.err == "" {
		t.Fatal("no error shown after canceling the question")
	}
	if st.busy {
		t.Fatal("dialog stayed busy after the failure")
	}
	if h.ui.HasText("Cloning…") {
		t.Fatal("busy indicator showing after the failure")
	}
	target := filepath.Join(parent, "denied")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("the failed clone left %q: err %v", target, err)
	}
	if h.app.session.HasRepo() {
		t.Fatal("a repository opened when the clone failed")
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("store gained an entry: %+v", h.store.Repos())
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}
