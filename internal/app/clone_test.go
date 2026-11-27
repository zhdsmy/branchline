package app

import (
	"crypto/subtle"
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
// reads empty in exactly the environment the clone workers inherit.
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
	if err == nil && strings.TrimSpace(string(out)) != "" {
		t.Fatalf("credential helper guard failed: credential.helper = %q", out)
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

// waitForFile polls for path to exist, with a bound. The clone worker's
// blocking-git test needs the clone mid-flight before canceling it; there is
// no worker callback to wait on because the fake git never finishes.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
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

// Canceling mid-clone stops git and removes the folder the clone had
// started: the dialog closes, no process of the fake git survives, and the
// dropped completion neither errors a reopened dialog nor opens anything.
func TestCloneCancelRemovesPartialClone(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "code")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	// The fake git makes the clone's target folder, writes its pid into the
	// parent folder for the assertion, and execs a sleep so that canceling
	// kills the very process without leaving a shell's child behind. Anything
	// but a clone hands through to the real git.
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
		"mkdir -p \"$last\"\necho $$ > \"" + filepath.Join(parent, "clone.pid") + "\"\nexec sleep 47\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}

	h := newTestApp(t)
	closeSync(t, h)
	prefs := h.store.Settings()
	prefs.GitPath = fake
	if err := h.store.SetSettings(prefs); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	openCloneDialog(t, h)
	h.ui.Type("https://example.com/slow/repo.git")
	h.ui.Frame()
	replaceField(t, h, "Parent Folder", parent)
	if err := h.ui.Click("Clone"); err != nil {
		t.Fatalf("click Clone: %v", err)
	}
	h.ui.Frame()

	// The clone is mid-flight: the busy state shows and Cancel is offered.
	target := filepath.Join(parent, "repo")
	waitForFile(t, target)
	if !h.ui.HasText("Cloning…") {
		t.Fatalf("busy indicator missing mid-clone; texts=%q", h.ui.Texts())
	}
	if info, ok := h.ui.AccessByRole(ui.RoleButton, "Clone"); !ok || !info.Disabled {
		t.Fatalf("Clone mid-clone = %+v (found %v), want disabled", info, ok)
	}
	pidBytes, err := os.ReadFile(filepath.Join(parent, "clone.pid"))
	if err != nil {
		t.Fatalf("read clone pid: %v", err)
	}

	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel mid-clone: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if h.app.cloneUI().open {
		t.Fatal("dialog stayed open after Cancel")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("the canceled clone left its folder: err %v", err)
	}
	// The killed git is reaped with the worker: no sleep survives.
	pidStr := strings.TrimSpace(string(pidBytes))
	if pidStr == "" {
		t.Fatal("empty clone pid")
	}
	if out, err := exec.Command("sh", "-c", "kill -0 "+pidStr+" 2>/dev/null && echo alive || echo gone").Output(); err != nil || strings.TrimSpace(string(out)) != "gone" {
		t.Fatalf("clone process %s = %q err %v, want gone", pidStr, out, err)
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("store gained an entry: %+v", h.store.Repos())
	}
	if h.app.session.HasRepo() {
		t.Fatal("a repository opened from the canceled clone")
	}

	// Escape cancels the same way: no leftover folder, no leftover process.
	if err := os.Remove(filepath.Join(parent, "clone.pid")); err != nil {
		t.Fatalf("remove first clone pid file: %v", err)
	}
	openCloneDialog(t, h)
	h.ui.Type("https://example.com/slow/repo2.git")
	h.ui.Frame()
	replaceField(t, h, "Parent Folder", parent)
	if err := h.ui.Click("Clone"); err != nil {
		t.Fatalf("click Clone for Escape cancel: %v", err)
	}
	h.ui.Frame()
	target2 := filepath.Join(parent, "repo2")
	waitForFile(t, target2)
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	h.drain()

	if h.app.cloneUI().open {
		t.Fatal("dialog stayed open after Escape")
	}
	if _, err := os.Stat(target2); !os.IsNotExist(err) {
		t.Fatalf("the Escape-canceled clone left its folder: err %v", err)
	}
	pidBytes, err = os.ReadFile(filepath.Join(parent, "clone.pid"))
	if err != nil {
		t.Fatalf("read second clone pid: %v", err)
	}
	pidStr = strings.TrimSpace(string(pidBytes))
	if out, err := exec.Command("sh", "-c", "kill -0 "+pidStr+" 2>/dev/null && echo alive || echo gone").Output(); err != nil || strings.TrimSpace(string(out)) != "gone" {
		t.Fatalf("Escape-canceled clone process %s = %q err %v, want gone", pidStr, out, err)
	}
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

// Closing the dialog mid-clone and reopening it discards the old worker's
// completion: it never closes the reopened dialog, never publishes its
// outcome there, and never opens the clone.
func TestCloneDiscardStaleResultAfterReopen(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "up.git")
	newBareCloneSource(t, "up", bare)
	parent := filepath.Join(root, "code")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialog(t, h)
	h.ui.Type(bare)
	h.ui.Frame()
	replaceField(t, h, "Parent Folder", parent)

	// Start the clone behind the paused worker gate, then close the dialog
	// before the worker ever runs.
	h.queue.PauseWorkers()
	if err := h.ui.Click("Clone"); err != nil {
		t.Fatalf("click Clone: %v", err)
	}
	h.ui.Frame()
	worker := h.queue.WaitWorker(t)
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	h.queue.ResumeWorkers()

	if h.app.cloneUI().open {
		t.Fatal("Escape left the dialog open")
	}

	// Reopen: the draft is fresh. Let the old worker finish; canceling its
	// context makes the clone fail fast, and its completion must not publish
	// into the reopened dialog.
	openCloneDialog(t, h)
	if got := h.app.cloneUI().url; got != "" {
		t.Fatalf("reopened dialog kept the old URL %q", got)
	}
	h.queue.ReleaseWorker(worker)
	h.drain()

	st := h.app.cloneUI()
	if !st.open {
		t.Fatal("stale completion closed the reopened dialog")
	}
	if st.err != "" {
		t.Fatalf("stale completion published an error: %q", st.err)
	}
	if st.busy {
		t.Fatal("reopened dialog stayed busy")
	}
	if h.app.session.HasRepo() {
		t.Fatal("the discarded clone opened a repository")
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("store gained an entry: %+v", h.store.Repos())
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
