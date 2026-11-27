package app

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
	"github.com/zhdsmy/branchline/internal/store"
)

// startCloneSSHServer starts a disposable loopback sshd for a Clone dialog
// test and prepends its isolated ssh wrapper to PATH, as loopbackSSHRepo
// does. Call it before newTestApp so cleanup drains the app's workers before
// the sshd stops. Tests using it must not call t.Parallel().
func startCloneSSHServer(t *testing.T) *sshtest.Target {
	t.Helper()
	srv := sshtest.StartLoopback(t)
	if binDir := srv.BinDir(); binDir != "" {
		if pathEnv := os.Getenv("PATH"); pathEnv != "" {
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+pathEnv)
		} else {
			t.Setenv("PATH", binDir)
		}
	}
	return srv
}

// resetCloneSSHMemory pins the run-memory of server clones so the assertions
// of a test do not depend on the order the suite runs in.
func resetCloneSSHMemory(t *testing.T) {
	t.Helper()
	prevFolder, prevIdentity := lastCloneSSHFolder, lastCloneSSHIdentity
	lastCloneSSHFolder, lastCloneSSHIdentity = "", ""
	t.Cleanup(func() { lastCloneSSHFolder, lastCloneSSHIdentity = prevFolder, prevIdentity })
}

// logicalPathForm rewrites a path below /private/var into its /var spelling.
// Both resolve to the same directory on the server — /var is a symlink — but
// git answers --show-toplevel with the physical path, so a clone asked into
// the logical spelling proves the app saves what the server reports rather
// than what it typed.
func logicalPathForm(t *testing.T, physical string) string {
	t.Helper()
	const canonical = "/private/var/"
	if !strings.HasPrefix(physical, canonical) {
		t.Fatalf("test server path %q is not below %s: this probe needs the symlinked /var form", physical, canonical)
	}
	return "/var/" + strings.TrimPrefix(physical, canonical)
}

// selectSSHServerLocation opens the Clone dialog and chooses the SSH Server
// segment, as a user does.
func openCloneDialogAtSSH(t *testing.T, h *testHarness) {
	t.Helper()
	openCloneDialog(t, h)
	if err := h.ui.Click("SSH Server"); err != nil {
		t.Fatalf("click SSH Server segment: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if h.app.cloneUI().location != cloneSSHServer {
		t.Fatal("the SSH Server segment did not select")
	}
}

// cloneSSHInto drives the dialog of an open SSH Server clone: type the URL,
// the server folder, the optional identity and the folder name, click Clone,
// and drain the worker.
func cloneSSHInto(t *testing.T, h *testHarness, url, folder, identity, name string) {
	t.Helper()
	replaceField(t, h, "Repository URL", url)
	replaceField(t, h, "Server Folder", folder)
	if identity != "" {
		replaceField(t, h, "Identity file (optional)", identity)
	}
	replaceField(t, h, "Folder Name", name)
	if err := h.ui.Click("Clone"); err != nil {
		t.Fatalf("click Clone: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.drain()
}

// sshServerFolderAddress is the ssh:// address of dir on srv for the Server
// Folder field, rewritten into the logical /var spelling so the stored path
// can only be canonical when the app reads it back from the server.
func sshServerFolderAddress(t *testing.T, srv *sshtest.Target, dir string) string {
	t.Helper()
	return srv.Address(logicalPathForm(t, dir))
}

// remoteFileExists checks on the server whether path exists, using POSIX sh.
func remoteFileExists(t *testing.T, srv *sshtest.Target, path string) bool {
	t.Helper()
	out := strings.TrimSpace(srv.RunShell(t,
		"if [ -e "+git.ShellQuote(path)+" ]; then printf present; else printf absent; fi"))
	return out == "present"
}

// Cloning onto a server creates the repository there, saves it as an SSH
// repository at the absolute working tree root the server reports, opens it
// with its commits in History, and remembers the server folder and identity
// for the next dialog of this run.
func TestCloneSSHSuccessOpensServerRepository(t *testing.T) {
	resetCloneSSHMemory(t)
	srv := startCloneSSHServer(t)
	src := srv.InitRepo(t, "clone-src")
	wantHead := srv.RunGit(t, src, "rev-parse", "HEAD")
	parent := srv.TempDir(t, "clone-parent")

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialogAtSSH(t, h)

	// The URL note of the server form is visible, and the preview reads the
	// address as the store writes it back.
	if !h.ui.HasText("The server runs git clone, so this URL must be reachable from the server.") {
		t.Fatalf("server URL note missing; texts=%q", h.ui.Texts())
	}
	folderInput := sshServerFolderAddress(t, srv, parent)
	cloneSSHInto(t, h, src, folderInput, srv.Identity, "srvclone")

	st := h.app.cloneUI()
	if st.open || st.err != "" {
		t.Fatalf("clone failed or dialog stayed open: err %q", st.err)
	}
	target := filepath.Join(parent, "srvclone")
	if !remoteFileExists(t, srv, filepath.Join(target, "a.txt")) {
		t.Fatalf("the server has no clone at %s", target)
	}
	if got := srv.RunGit(t, target, "rev-parse", "HEAD"); got != wantHead {
		t.Fatalf("clone HEAD = %q, want %q", got, wantHead)
	}

	if !h.app.session.HasRepo() {
		t.Fatal("no repository open after cloning onto the server")
	}
	meta := h.app.session.Meta()
	if meta.Kind != store.SSH {
		t.Fatalf("session kind = %q, want ssh", meta.Kind)
	}
	if meta.Path != target {
		t.Fatalf("session path = %q, want the server's absolute root %q", meta.Path, target)
	}
	if strings.HasPrefix(meta.Path, "~") || !strings.HasPrefix(meta.Path, "/") {
		t.Fatalf("stored path %q is not absolute", meta.Path)
	}
	if meta.Identity != srv.Identity {
		t.Fatalf("stored identity = %q, want %q", meta.Identity, srv.Identity)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("snapshot of the server clone failed to load: %v", err)
	}
	if got := h.app.session.Snapshot().Status.Head; got != wantHead {
		t.Fatalf("session HEAD = %q, want %q", got, wantHead)
	}
	repos := h.store.Repos()
	if len(repos) != 1 || repos[0].Kind != store.SSH || repos[0].Path != target {
		t.Fatalf("store repos = %+v, want one SSH entry at %q", repos, target)
	}
	if repos[0].Name != "srvclone" {
		t.Fatalf("stored name = %q, want srvclone", repos[0].Name)
	}

	// The clone went over SSH: multiplexing opened the control socket of the
	// executor's identity. An executor that never connects leaves none.
	controlPath := (&git.SSHExecutor{Host: srv.Host, Port: srv.Port, User: srv.User, Identity: srv.Identity}).ControlPath()
	if _, err := os.Stat(controlPath); err != nil {
		t.Fatalf("no SSH control socket after the clone: %v — the clone did not run over SSH", err)
	}

	// History shows the clone's commit.
	mustClick(t, h, "History")
	h.ui.Frame()
	if !hasExactText(h.ui, "initial commit") {
		t.Fatalf("History missing the clone's commit; texts=%q", h.ui.Texts())
	}

	// The server folder and identity of the success greet the next dialog,
	// which still starts at This Mac.
	openCloneDialog(t, h)
	if got := h.app.cloneUI().location; got != cloneThisMac {
		t.Fatalf("reopened location = %d, want This Mac", got)
	}
	if got := h.app.cloneUI().serverFolder; got != folderInput {
		t.Fatalf("remembered server folder = %q, want %q", got, folderInput)
	}
	if got := h.app.cloneUI().serverIdentity; got != srv.Identity {
		t.Fatalf("remembered identity = %q, want %q", got, srv.Identity)
	}
	if err := h.ui.Click("SSH Server"); err != nil {
		t.Fatalf("click SSH Server in reopened dialog: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("Identity file (optional)") {
		t.Fatalf("reopened server draft missing its identity field; texts=%q", h.ui.Texts())
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}

// A parent folder that does not exist on the server yet is created there
// before the clone runs.
func TestCloneSSHCreatesMissingParentFolder(t *testing.T) {
	resetCloneSSHMemory(t)
	srv := startCloneSSHServer(t)
	src := srv.InitRepo(t, "clone-src")
	wantHead := srv.RunGit(t, src, "rev-parse", "HEAD")
	parent := filepath.Join(srv.RootDir, "deep", "nested")
	if remoteFileExists(t, srv, parent) {
		t.Fatalf("test setup broken: %s already exists", parent)
	}

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialogAtSSH(t, h)
	cloneSSHInto(t, h, src, sshServerFolderAddress(t, srv, parent), "", "nestedclone")

	if st := h.app.cloneUI(); st.open || st.err != "" {
		t.Fatalf("clone failed or dialog stayed open: err %q", st.err)
	}
	target := filepath.Join(parent, "nestedclone")
	if got := srv.RunGit(t, target, "rev-parse", "HEAD"); got != wantHead {
		t.Fatalf("clone HEAD = %q, want %q", got, wantHead)
	}
	if got := h.app.session.Meta().Path; got != target {
		t.Fatalf("opened path = %q, want %q", got, target)
	}
}

// A target folder that already exists on the server and is not empty fails
// with git's own words, the folder keeps its content, and nothing is saved.
func TestCloneSSHTargetExistsUntouched(t *testing.T) {
	resetCloneSSHMemory(t)
	srv := startCloneSSHServer(t)
	src := srv.InitRepo(t, "clone-src")
	parent := srv.TempDir(t, "clone-parent")
	target := filepath.Join(parent, "taken")
	srv.WriteFile(t, parent, "taken/sentinel.txt", []byte("keep me\n"))

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialogAtSSH(t, h)
	cloneSSHInto(t, h, src, sshServerFolderAddress(t, srv, parent), "", "taken")

	st := h.app.cloneUI()
	if !st.open {
		t.Fatal("dialog closed when the target existed")
	}
	if !strings.Contains(st.err, "already exists") {
		t.Fatalf("error = %q, want git's already exists", st.err)
	}
	if got := string(srv.ReadFile(t, parent, "taken/sentinel.txt")); got != "keep me\n" {
		t.Fatalf("sentinel after refused clone = %q, want intact", got)
	}
	if remoteFileExists(t, srv, filepath.Join(target, ".git")) {
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

// Cloning into the folder of a repository that is already open fails with
// git's error and the list keeps a single entry of it.
func TestCloneSSHDuplicateOfOpenRepositoryFails(t *testing.T) {
	resetCloneSSHMemory(t)
	srv := startCloneSSHServer(t)
	src := srv.InitRepo(t, "clone-src")
	parent := srv.TempDir(t, "clone-parent")

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialogAtSSH(t, h)
	cloneSSHInto(t, h, src, sshServerFolderAddress(t, srv, parent), "", "onlycopy")
	if st := h.app.cloneUI(); st.open || st.err != "" {
		t.Fatalf("first clone failed: err %q", st.err)
	}
	if len(h.store.Repos()) != 1 {
		t.Fatalf("store after first clone = %+v, want one entry", h.store.Repos())
	}

	// The clone is open; cloning the same source into the same folder again
	// must fail in the dialog, not add a second entry.
	openCloneDialogAtSSH(t, h)
	cloneSSHInto(t, h, src, sshServerFolderAddress(t, srv, parent), "", "onlycopy")

	st := h.app.cloneUI()
	if !st.open {
		t.Fatal("dialog closed on the duplicate clone")
	}
	if !strings.Contains(st.err, "already exists") {
		t.Fatalf("error = %q, want git's already exists", st.err)
	}
	if repos := h.store.Repos(); len(repos) != 1 {
		t.Fatalf("store after duplicate clone = %+v, want still one entry", repos)
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}

// A server folder that does not read as an SSH address fails in the dialog
// without connecting: the error is the parse error, not a connection error,
// and the dialog never goes busy.
func TestCloneSSHBadServerFolderParseError(t *testing.T) {
	resetCloneSSHMemory(t)
	srv := startCloneSSHServer(t)
	src := srv.InitRepo(t, "clone-src")

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialogAtSSH(t, h)
	cloneSSHInto(t, h, src, "just-a-folder", "", "someclone")

	st := h.app.cloneUI()
	if !st.open {
		t.Fatal("dialog closed on a malformed server folder")
	}
	if st.busy {
		t.Fatal("dialog went busy for a folder that never connects")
	}
	if !strings.Contains(st.err, "write the address as") {
		t.Fatalf("error = %q, want the address format explanation", st.err)
	}
	if strings.Contains(st.err, "cannot connect") || strings.Contains(st.err, "Permission denied") {
		t.Fatalf("error = %q reads as a connection failure; nothing may have connected", st.err)
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("store gained an entry: %+v", h.store.Repos())
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}

// A private key the server does not know fails the clone in the dialog with
// the same guidance the Add SSH Repository dialog explains the failure with:
// the wrapped ssh error and the sentence about adding a key.
func TestCloneSSHAuthenticationFailureExplained(t *testing.T) {
	resetCloneSSHMemory(t)
	srv := startCloneSSHServer(t)
	src := srv.InitRepo(t, "clone-src")
	parent := srv.TempDir(t, "clone-parent")

	// A key the loopback sshd does not authorize: generated locally, never
	// added to its authorized_keys.
	keygenBin, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Fatalf("ssh-keygen on PATH: %v", err)
	}
	stranger := filepath.Join(t.TempDir(), "stranger_ed25519")
	if out, err := exec.Command(keygenBin, "-q", "-t", "ed25519", "-N", "", "-f", stranger).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen stranger: %v\n%s", err, out)
	}

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialogAtSSH(t, h)
	cloneSSHInto(t, h, src, sshServerFolderAddress(t, srv, parent), stranger, "deniedclone")

	st := h.app.cloneUI()
	if !st.open {
		t.Fatal("dialog closed when authentication failed")
	}
	if st.busy {
		t.Fatal("dialog stayed busy after the authentication failure")
	}
	if !strings.Contains(st.err, "Permission denied") {
		t.Fatalf("error = %q, want ssh's permission denied as Add SSH shows it", st.err)
	}
	const guidance = "The server did not accept a key."
	if !strings.Contains(st.err, guidance) {
		t.Fatalf("error = %q, want the key guidance of Add SSH Repository", st.err)
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("store gained an entry: %+v", h.store.Repos())
	}
	if h.app.session.HasRepo() {
		t.Fatal("a repository opened when authentication failed")
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}

// The drafts of the two locations survive switching back and forth; the URL
// and the folder name are shared, and the "Clones into" preview reads the
// server folder as the store writes it back. The Clone button of the server
// form stays disabled while the server folder is empty or the name is not a
// legal folder, and on a malformed folder.
func TestCloneSSHDraftsPreviewAndDisabledStates(t *testing.T) {
	resetCloneSSHMemory(t)
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

	// This Mac has no server note and no server fields.
	if h.ui.HasText("reachable from the server") {
		t.Fatalf("server URL note visible at This Mac; texts=%q", h.ui.Texts())
	}
	replaceField(t, h, "Repository URL", "https://example.com/owner/preview.git")
	replaceField(t, h, "Parent Folder", "/tmp/branchline-test-preview-parent")

	// Switch to SSH Server: URL and name carry over, the note appears, and
	// the empty server folder keeps Clone disabled.
	if err := h.ui.Click("SSH Server"); err != nil {
		t.Fatalf("click SSH Server: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if got := h.app.cloneUI().url; got != "https://example.com/owner/preview.git" {
		t.Fatalf("URL not shared across locations: %q", got)
	}
	if got := h.app.cloneUI().name; got != "preview" {
		t.Fatalf("folder name not shared across locations: %q", got)
	}
	if !h.ui.HasText("The server runs git clone, so this URL must be reachable from the server.") {
		t.Fatalf("server URL note missing; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("Parent Folder") {
		t.Fatalf("Parent Folder still visible in the server form; texts=%q", h.ui.Texts())
	}
	if cloneEnabled() {
		t.Fatal("Clone enabled with an empty server folder")
	}

	// The scp-style address previews as the store writes it back, with the
	// folder name joined.
	replaceField(t, h, "Server Folder", "me@example.com:/srv/repos")
	if !h.ui.HasText("Clones into me@example.com:/srv/repos/preview") {
		t.Fatalf("preview missing the scp-style target; texts=%q", h.ui.Texts())
	}
	if !cloneEnabled() {
		t.Fatal("Clone disabled with a valid URL, server folder and name")
	}

	// The ssh:// form keeps its port; a home-relative path keeps its tilde.
	replaceField(t, h, "Server Folder", "ssh://me@example.com:2222/srv/repos")
	if !h.ui.HasText("Clones into ssh://me@example.com:2222/srv/repos/preview") {
		t.Fatalf("preview missing the ssh:// target; texts=%q", h.ui.Texts())
	}
	replaceField(t, h, "Server Folder", "ssh://me@example.com:2222/~/srv")
	if !h.ui.HasText("Clones into ssh://me@example.com:2222/~/srv/preview") {
		t.Fatalf("preview missing the tilde target; texts=%q", h.ui.Texts())
	}

	// An illegal folder name disables again, as at This Mac.
	replaceField(t, h, "Folder Name", "a/b")
	if cloneEnabled() {
		t.Fatal("Clone enabled with folder name a/b")
	}
	replaceField(t, h, "Folder Name", "preview")
	if !cloneEnabled() {
		t.Fatal("Clone disabled with a legal name again")
	}

	// Back to This Mac: the parent draft survived, and the server fields
	// leave the dialog.
	if err := h.ui.Click("This Mac"); err != nil {
		t.Fatalf("click This Mac: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if got := h.app.cloneUI().parent; got != "/tmp/branchline-test-preview-parent" {
		t.Fatalf("parent draft lost in the switch: %q", got)
	}
	if h.ui.HasText("Identity file (optional)") || h.ui.HasText("reachable from the server") {
		t.Fatalf("server fields visible at This Mac; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Clones into /tmp/branchline-test-preview-parent/preview") {
		t.Fatalf("local preview missing after switching back; texts=%q", h.ui.Texts())
	}

	// And forth again: the server draft survived too.
	if err := h.ui.Click("SSH Server"); err != nil {
		t.Fatalf("click SSH Server again: %v", err)
	}
	h.ui.Frame()
	if got := h.app.cloneUI().serverFolder; got != "ssh://me@example.com:2222/~/srv" {
		t.Fatalf("server folder draft lost in the switch: %q", got)
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}

// hungGitHTTPServer serves a git smart-HTTP endpoint whose info/refs handler
// never answers: it hangs until release closes, or until the client drops
// the connection, which it reports on the dropped channel. It listens on
// 127.0.0.1 only.
func hungGitHTTPServer(t *testing.T) (url string, sawConn, dropped <-chan struct{}, release func()) {
	t.Helper()
	droppedCh := make(chan struct{}, 1)
	hang := make(chan struct{})
	sawConnCh := make(chan struct{}, 1)
	httpSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sawConnCh <- struct{}{}:
		default:
		}
		select {
		case <-hang:
		case <-r.Context().Done():
			select {
			case droppedCh <- struct{}{}:
			default:
			}
		}
	}))
	httpSrv.Listener.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen loopback: %v", err)
	}
	httpSrv.Listener = ln
	httpSrv.Start()
	once := make(chan struct{})
	release = func() {
		select {
		case <-once:
		default:
			close(once)
			close(hang)
		}
	}
	t.Cleanup(func() {
		release()
		httpSrv.Close()
	})
	return httpSrv.URL + "/repo.git", sawConnCh, droppedCh, release
}

// waitRemoteFile polls for path to exist on the server, bounded to ten
// seconds: a mid-flight clone has no completion the test could wait on.
func waitRemoteFile(t *testing.T, srv *sshtest.Target, path string) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		if remoteFileExists(t, srv, path) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s on the server", path)
}

// Canceling a clone onto a server mid-flight closes the dialog without
// saving or opening anything: those application-side effects are what this
// test asserts. What the cancel means for the server's git is probed and
// logged instead: the clone runs behind ssh's connection sharing, which the
// killed client does not tear down, so the server keeps cloning — the
// worker waiting on it does not return, and neither its HTTP request nor
// the folder it started goes away. The test exits the control master
// afterwards so the stuck worker unwinds and the cleanup it skipped can run.
func TestCloneSSHCancelClosesWithoutSaving(t *testing.T) {
	resetCloneSSHMemory(t)
	srv := startCloneSSHServer(t)
	parent := srv.TempDir(t, "clone-parent")
	url, sawConn, dropped, _ := hungGitHTTPServer(t)

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialogAtSSH(t, h)
	replaceField(t, h, "Repository URL", url)
	replaceField(t, h, "Server Folder", sshServerFolderAddress(t, srv, parent))
	replaceField(t, h, "Identity file (optional)", srv.Identity)
	replaceField(t, h, "Folder Name", "hungclone")
	if err := h.ui.Click("Clone"); err != nil {
		t.Fatalf("click Clone: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()

	// The clone is mid-flight once its folder is on the server: git clone
	// makes the target before the transfer, and its HTTP request hangs
	// unanswered from then on.
	target := filepath.Join(parent, "hungclone")
	waitRemoteFile(t, srv, target)
	select {
	case <-sawConn:
	case <-time.After(10 * time.Second):
		t.Fatal("the clone's HTTP request never reached the source")
	}
	if !h.ui.HasText("Cloning…") {
		t.Fatalf("busy indicator missing mid-clone; texts=%q", h.ui.Texts())
	}

	start := time.Now()
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel mid-clone: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()

	// The application side: the dialog closed, nothing is busy, saved or
	// open. No drain here: the worker behind the cancel does not return
	// while the server's git keeps the shared connection alive.
	st := h.app.cloneUI()
	if st.open {
		t.Fatal("dialog stayed open after canceling the clone")
	}
	if st.busy {
		t.Fatal("dialog stayed busy after canceling the clone")
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("store gained an entry: %+v", h.store.Repos())
	}
	if h.app.session.HasRepo() {
		t.Fatal("a repository opened from the canceled clone")
	}

	// The probe: does the server's HTTP request drop once the client side
	// is stopped, and does the folder the clone started go away?
	select {
	case <-dropped:
		t.Logf("probe: the server's HTTP request dropped %s after the cancel", time.Since(start))
	case <-time.After(4 * time.Second):
		t.Logf("probe: the server's HTTP request still hangs 4s after the cancel — the server's git clone survives it")
	}
	if remoteFileExists(t, srv, target) {
		t.Logf("probe: the canceled clone's folder %s is still on the server", target)
	} else {
		t.Logf("probe: the canceled clone's folder %s was removed", target)
	}

	// Unwind the stuck worker for the teardown drain: exiting the shared
	// connection's master ends the session the server's git runs in, so the
	// command the cancel killed answers and the cleanup runs.
	controlPath := (&git.SSHExecutor{Host: srv.Host, Port: srv.Port, User: srv.User, Identity: srv.Identity}).ControlPath()
	if out, err := exec.Command(srv.SSHProgram(), "-O", "exit", "-o", "ControlPath="+controlPath, srv.User+"@"+srv.Host).CombinedOutput(); err != nil {
		t.Logf("probe: exiting the control master failed, the worker may hang the drain: %v %s", err, out)
	}
	h.drain()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if !remoteFileExists(t, srv, target) {
			t.Logf("probe: after unwinding, the clone's folder was removed %s after the cancel", time.Since(start))
			break
		}
		if time.Now().After(deadline) {
			t.Logf("probe: after unwinding, the clone's folder %s is still on the server", target)
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
}
