package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
	"github.com/zhdsmy/branchline/internal/store"
)

// stubPickLocalFolder replaces the system folder picker with fn for the test
// and restores the production picker afterwards.
func stubPickLocalFolder(t *testing.T, fn func() (string, error)) {
	t.Helper()
	prev := pickLocalFolderFn
	pickLocalFolderFn = func(_ *mygo.Window) (string, error) { return fn() }
	t.Cleanup(func() { pickLocalFolderFn = prev })
}

// The repository switcher popup distinguishes local and SSH repositories and
// switching between them loads each repository's real commits.
func TestRepoSwitcherLocalAndSSHLoopbackSSH(t *testing.T) {
	sshRepo, srv := loopbackSSHRepo(t)
	wantSSHHead := srv.RunGit(t, sshRepo.Path, "rev-parse", "HEAD")

	local := newGitRepo(t, "localalpha")
	localHash := local.CommitFile("a.txt", "alpha\n", "alpha commit")

	h := newTestApp(t, local.StoreRepo(), sshRepo)

	// Open the popup and check both repositories are listed with SSH marked.
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.Find("localalpha"); !ok {
		t.Fatalf("local repository missing from popup; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.Find(sshRepo.Name); !ok {
		t.Fatalf("SSH repository missing from popup; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.Find("SSH"); !ok {
		t.Fatalf("SSH badge missing from popup; texts=%q", h.ui.Texts())
	}

	// Geometry of the two-line SSH row: the location line stays inside the
	// switch button, left of the sibling Remove button, and the name line
	// gets width — with the name text and its button sharing one label, the
	// badge's position right of the location line's left edge is the given
	// evidence: in the two-line layout the badge sits after the name on the
	// first line, so its X is the location's X plus the name column width
	// plus the gap; a zero-width name (or the old one-line row, where the
	// badge precedes the location) puts it at or left of location.X + 6.
	locR, ok := h.ui.Find(sshRepo.Location())
	if !ok {
		t.Fatalf("SSH location text missing from popup; texts=%q", h.ui.Texts())
	}
	badgeR, ok := h.ui.Find("SSH")
	if !ok {
		t.Fatalf("SSH badge box missing; texts=%q", h.ui.Texts())
	}
	rmR, ok := h.ui.Find("Remove " + sshRepo.Name)
	if !ok {
		t.Fatalf("Remove button box missing; texts=%q", h.ui.Texts())
	}
	if right := locR.X + locR.W; right > rmR.X {
		t.Fatalf("SSH location box %+v runs to X %.1f, over the Remove button's left edge %.1f", locR, right, rmR.X)
	}
	if nameW := badgeR.X - locR.X - 6; nameW < 40 {
		t.Fatalf("SSH badge at X %.1f against the location line's X %.1f leaves name column width %.1f, want >= 40", badgeR.X, locR.X, nameW)
	}
	if badgeR.W <= 0 || badgeR.X+badgeR.W > rmR.X {
		t.Fatalf("SSH badge box %+v is empty or overflows Remove button's left edge %.1f", badgeR, rmR.X)
	}
	if locR.Y < badgeR.Y+badgeR.H {
		t.Fatalf("SSH location box %+v overlaps first-line badge box %+v vertically", locR, badgeR)
	}

	// Switch to the SSH repository: its real commits load.
	if err := h.ui.Click(sshRepo.Name); err != nil {
		t.Fatalf("choose SSH repo: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if h.app.session.Meta().Kind != store.SSH {
		t.Fatalf("active repo kind = %q, want ssh", h.app.session.Meta().Kind)
	}
	if got := h.app.session.Snapshot().Status.Head; got != wantSSHHead {
		t.Fatalf("SSH HEAD = %q, want %q", got, wantSSHHead)
	}

	// Switch back to the local repository: its real commits load.
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("reopen Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("localalpha"); err != nil {
		t.Fatalf("choose local repo: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if got := h.app.session.Snapshot().Status.Head; got != localHash {
		t.Fatalf("local HEAD = %q, want %q", got, localHash)
	}
	if h.app.session.Meta().Kind != store.Local {
		t.Fatalf("active repo kind = %q, want local", h.app.session.Meta().Kind)
	}
}

// Add Local Repository reduces a chosen subdirectory to its repository root
// and opens it; a non-Git folder shows the notice without touching the store
// or the session, and canceling the picker is silent.
func TestRepoAddLocalValidAndNotGit(t *testing.T) {
	// Choosing a subdirectory of a real repository adds and opens its root.
	repo := newGitRepo(t, "pickedrepo")
	hash := repo.CommitFile("sub/dir/file.txt", "x\n", "sub commit")
	sub := filepath.Join(repo.Path(), "sub", "dir")

	var picked func() (string, error)
	stubPickLocalFolder(t, func() (string, error) { return picked() })
	picked = func() (string, error) { return sub, nil }

	h := newTestApp(t)
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Add Local Repository…"); err != nil {
		t.Fatalf("click Add Local Repository: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if !h.app.session.HasRepo() {
		t.Fatalf("no repository open after picking %q", sub)
	}
	// git rev-parse --show-toplevel resolves macOS /var symlinks, so compare
	// against the resolved root.
	wantRoot, err := filepath.EvalSymlinks(repo.Path())
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if got := h.app.session.Meta().Path; got != wantRoot {
		t.Fatalf("opened path = %q, want repository root %q", got, wantRoot)
	}
	if got := h.app.session.Snapshot().Status.Head; got != hash {
		t.Fatalf("HEAD = %q, want %q", got, hash)
	}
	if repos := h.store.Repos(); len(repos) != 1 || repos[0].Path != wantRoot {
		t.Fatalf("store repos = %+v, want one entry at %q", repos, wantRoot)
	}

	// Picking a folder outside any repository opens the notice, adds nothing
	// and keeps the current repository.
	notGit := t.TempDir()
	picked = func() (string, error) { return notGit, nil }
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("reopen Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Add Local Repository…"); err != nil {
		t.Fatalf("click Add Local Repository again: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if !h.ui.HasText("Not a Git Repository") {
		t.Fatalf("not-a-git notice missing; texts=%q", h.ui.Texts())
	}
	if repos := h.store.Repos(); len(repos) != 1 {
		t.Fatalf("store gained an entry for a non-git folder: %+v", repos)
	}
	if got := h.app.session.Meta().Path; got != wantRoot {
		t.Fatalf("active repo changed to %q after non-git pick", got)
	}
	if err := h.ui.Click("Close"); err != nil {
		t.Fatalf("close not-a-git notice: %v", err)
	}
	h.ui.Frame()
	if h.ui.HasText("Not a Git Repository") {
		t.Fatal("not-a-git notice stayed after Close")
	}

	// Canceling the picker is silent: no notice, no store or session change.
	picked = func() (string, error) { return "", nil }
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("reopen Repository Switcher for cancel: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Add Local Repository…"); err != nil {
		t.Fatalf("click Add Local Repository for cancel: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if h.ui.HasText("Not a Git Repository") {
		t.Fatal("canceling the picker opened the not-a-git notice")
	}
	if repos := h.store.Repos(); len(repos) != 1 {
		t.Fatalf("store changed after cancel: %+v", repos)
	}
	if got := h.app.session.Meta().Path; got != wantRoot {
		t.Fatalf("active repo changed to %q after cancel", got)
	}
	if h.app.toast != "" {
		t.Fatalf("canceling the picker left a toast: %q", h.app.toast)
	}
}

// openSSHForm opens the Add SSH Repository dialog through the switcher popup.
func openSSHForm(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Add SSH Repository…"); err != nil {
		t.Fatalf("click Add SSH Repository: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("Add SSH Repository…") {
		t.Fatalf("Add SSH dialog did not open; texts=%q", h.ui.Texts())
	}
}

// typeSSHForm fills the form fields top to bottom as a user does, moving
// between them with Tab. Skipped optional fields are still tabbed through so
// the focus ends in a predictable field.
func typeSSHForm(t *testing.T, h *testHarness, address, identity, name string) {
	t.Helper()
	h.ui.Type(address)
	if identity != "" || name != "" {
		h.ui.Key(0, ui.KeyTab)
		h.ui.Frame()
		if identity != "" {
			h.ui.Type(identity)
		}
	}
	if name != "" {
		h.ui.Key(0, ui.KeyTab)
		h.ui.Frame()
		h.ui.Type(name)
	}
	h.ui.Frame()
}

// sshFormState reads the dialog's published state of one app for assertions.
func sshFormState(h *testHarness) *reposUIState {
	return h.app.reposUI()
}

// focusField clicks the text field named name as a user does, moving the
// keyboard focus into it.
func focusField(t *testing.T, h *testHarness, name string) {
	t.Helper()
	info, ok := h.ui.AccessByRole(ui.RoleTextField, name)
	if !ok {
		t.Fatalf("no text field named %q", name)
	}
	h.ui.ClickAt(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
	h.ui.Frame()
}

// sshAddressOf builds the SSH address string of dir on srv in one of the git
// remote styles the dialog accepts.
func sshAddressVariants(srv *sshtest.Target, dir string) map[string]string {
	userHost := srv.User + "@" + srv.Host
	return map[string]string{
		"scp":    fmt.Sprintf("%s:%s", userHost, dir),
		"sshurl": fmt.Sprintf("ssh://%s:%d%s", userHost, srv.Port, dir),
	}
}

// The Add SSH dialog parses the git remote address styles, Test Connection
// reports the server's git version and repository root without saving or
// opening anything, Add saves and opens the repository, and editing the form
// discards a stale in-flight answer.
func TestRepoAddSSHAddressFormatsAndTestConnectionLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	wantHead := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")
	addrs := sshAddressVariants(srv, repo.Path)
	// The loopback sshd listens on a nonstandard port, which the scp-style
	// address syntax cannot carry; the live connection checks below use the
	// ssh:// form. ParseAddress coverage of user@host:/path, host:rel/path
	// and ssh:// forms comes with the store package, while the relative-path
	// forms are verified through the buildGitRepo construction path in
	// TestRepoAddSSHAddressParsingFormsLoopbackSSH.
	h := newTestApp(t)

	// Test Connection over a ssh://user@host:port/path address reports success
	// without saving or opening.
	openSSHForm(t, h)
	typeSSHForm(t, h, addrs["sshurl"], srv.Identity, "")
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
	if !strings.Contains(st.sshResult, "Connected:") || !strings.Contains(st.sshResult, repo.Path) {
		t.Fatalf("Test Connection result = %q, want Connected with path %q", st.sshResult, repo.Path)
	}
	if !strings.Contains(st.sshResult, "git version") {
		t.Fatalf("Test Connection result = %q, want git version", st.sshResult)
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("Test Connection saved a repository: %+v", h.store.Repos())
	}
	if h.app.session.HasRepo() {
		t.Fatal("Test Connection opened a repository")
	}
	if !st.sshOpen {
		t.Fatal("Test Connection closed the dialog")
	}

	// Editing the address while a check runs discards its late answer.
	h.queue.PauseWorkers()
	if err := h.ui.Click("Test Connection"); err != nil {
		t.Fatalf("click Test Connection for staleness: %v", err)
	}
	h.ui.Frame()
	staleWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()
	// Edit the address: the in-flight answer no longer matches the form.
	focusField(t, h, "Address")
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.ui.Frame()
	h.ui.Type(addrs["sshurl"] + ".edited")
	h.ui.Frame()
	if got := sshFormState(h).sshAddress; !strings.HasSuffix(got, ".edited") {
		t.Fatalf("address field did not take the edit, value %q", got)
	}
	h.queue.ReleaseWorker(staleWorker)
	h.drain()
	st = sshFormState(h)
	if st.sshBusy {
		t.Fatal("edited form left the dialog busy")
	}
	// The stale worker's answer must not publish: editing invalidated it, so
	// after the queue drained the result stays empty rather than showing the
	// success of the pre-edit address. (Editing itself clears the result, so
	// this assertion only means something because the drain already ran the
	// stale worker's completion.)
	if st.sshResult != "" {
		t.Fatalf("stale answer published after edit: %q", st.sshResult)
	}
	// The edited address never started a check either: no repository was
	// saved and nothing opened.
	if len(h.store.Repos()) != 0 {
		t.Fatalf("stale or edited check saved a repository: %+v", h.store.Repos())
	}
	if h.app.session.HasRepo() {
		t.Fatal("stale or edited check opened a repository")
	}

	// Close and reopen the dialog to reset the form, then Add through the
	// scp-style-with-portless-userHost form's ssh url variant.
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	openSSHForm(t, h)
	typeSSHForm(t, h, addrs["sshurl"], srv.Identity, "")
	h.ui.Frame()
	if err := h.ui.Click("Add"); err != nil {
		t.Fatalf("click Add: %v", err)
	}
	h.ui.Frame()
	h.drain()

	st = sshFormState(h)
	if st.sshOpen {
		t.Fatalf("dialog stayed open after successful Add, result %q", st.sshResult)
	}
	if !h.app.session.HasRepo() || h.app.session.Meta().Kind != store.SSH {
		t.Fatalf("Add did not open the SSH repository: %+v", h.app.session.Meta())
	}
	if got := h.app.session.Meta().Path; got != repo.Path {
		t.Fatalf("added path = %q, want %q", got, repo.Path)
	}
	if got := h.app.session.Snapshot().Status.Head; got != wantHead {
		t.Fatalf("added repo HEAD = %q, want %q", got, wantHead)
	}
	if len(h.store.Repos()) != 1 {
		t.Fatalf("store after Add = %+v, want one entry", h.store.Repos())
	}
}

// The scp-style user@host:/path, host:rel/path and ssh config alias forms
// parse into the executor fields git uses, verified at the point the dialog
// hands the repository to the same buildGitRepo construction production uses.
// (They cannot complete a live loopback connection: their syntax carries no
// port, and the loopback sshd does not listen on 22.)
func TestRepoAddSSHAddressParsingFormsLoopbackSSH(t *testing.T) {
	// The loopback server stands up the SSH test environment even though these
	// forms never dial it: parsing must not depend on the connection.
	loopbackSSHRepo(t)

	cases := []struct {
		name     string
		address  string
		wantHost string
		wantPath string
		wantUser string
		wantPort int
	}{
		{"scp user host abs", "alice@example.com:/srv/app", "example.com", "/srv/app", "alice", 0},
		{"host relative path", "example.com:rel/app", "example.com", "~/rel/app", "", 0},
		{"config alias abs", "devbox:/srv/app", "devbox", "/srv/app", "", 0},
		{"config alias relative", "devbox:rel/app", "devbox", "~/rel/app", "", 0},
		{"ssh url alias", "ssh://devbox/srv/app", "devbox", "/srv/app", "", 0},
		{"ssh url user port", "ssh://bob@example.com:2222/srv/app", "example.com", "/srv/app", "bob", 2222},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestApp(t)
			var gotMeta store.Repo
			intercepted := false
			h.app.openRepoFn = func(meta store.Repo) *git.Repo {
				gotMeta = meta
				intercepted = true
				// A failing executor: the parse results are what matters, and
				// the connection failure keeps the dialog open for inspection.
				return git.New(failingExecutor{})
			}

			openSSHForm(t, h)
			typeSSHForm(t, h, tc.address, "", "")
			h.ui.Frame()
			if err := h.ui.Click("Test Connection"); err != nil {
				t.Fatalf("click Test Connection: %v", err)
			}
			h.ui.Frame()
			h.drain()

			if !intercepted {
				t.Fatal("form did not construct a repository through buildGitRepo")
			}
			if gotMeta.Host != tc.wantHost || gotMeta.Path != tc.wantPath || gotMeta.User != tc.wantUser || gotMeta.Port != tc.wantPort {
				t.Fatalf("parsed = host %q path %q user %q port %d, want host %q path %q user %q port %d",
					gotMeta.Host, gotMeta.Path, gotMeta.User, gotMeta.Port, tc.wantHost, tc.wantPath, tc.wantUser, tc.wantPort)
			}
			if gotMeta.Kind != store.SSH {
				t.Fatalf("kind = %q, want ssh", gotMeta.Kind)
			}
			// The connection failed (injected executor), so nothing is saved.
			if len(h.store.Repos()) != 0 {
				t.Fatalf("failed check saved a repository: %+v", h.store.Repos())
			}
		})
	}
}

// failingExecutor is a git.Executor whose every command fails immediately.
type failingExecutor struct{}

func (failingExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	return nil, errors.New("probe executor: no connection")
}
func (failingExecutor) Describe() string { return "probe" }

// scriptExecutor is a git.Executor answering with scripted output, used to
// reproduce server behaviors the loopback cannot (for example a server
// without git on its non-interactive PATH).
type scriptExecutor struct {
	out []byte
	err error
}

func (s scriptExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	return s.out, s.err
}
func (s scriptExecutor) Describe() string { return "scripted" }

// The Test Connection errors for the four common failure classes are
// actionable: each names the problem and what to do, the host key failure
// offers Copy SSH Command and Retry without leaking the remote path, and a
// server without git is told apart from a path that is not a repository.
func TestRepoAddSSHTestConnectionErrorsLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)

	// sshFormRetry opens the dialog, types the address, clicks Test Connection
	// and drains the check.
	runCheck := func(t *testing.T, h *testHarness, address, identity string) {
		t.Helper()
		openSSHForm(t, h)
		typeSSHForm(t, h, address, identity, "")
		h.ui.Frame()
		if err := h.ui.Click("Test Connection"); err != nil {
			t.Fatalf("click Test Connection: %v", err)
		}
		h.ui.Frame()
		h.drain()
	}

	t.Run("path without repository", func(t *testing.T) {
		empty := srv.TempDir(t, "empty")
		h := newTestApp(t)
		runCheck(t, h, fmt.Sprintf("ssh://%s@%s:%d%s", srv.User, srv.Host, srv.Port, empty), srv.Identity)
		st := sshFormState(h)
		if !st.sshFailed {
			t.Fatalf("check unexpectedly succeeded: %q", st.sshResult)
		}
		if !strings.Contains(st.sshResult, "not a git repository") {
			t.Fatalf("result = %q, want the server's not-a-git error", st.sshResult)
		}
		if !strings.Contains(st.sshResult, "Check the repository path") {
			t.Fatalf("result = %q, want path guidance", st.sshResult)
		}
		if len(h.store.Repos()) != 0 {
			t.Fatalf("failed check saved a repository: %+v", h.store.Repos())
		}
		closeDialog(t, h)
	})

	t.Run("path does not exist", func(t *testing.T) {
		missing := srv.TempDir(t, "base") + "/missing-deep"
		h := newTestApp(t)
		runCheck(t, h, fmt.Sprintf("ssh://%s@%s:%d%s", srv.User, srv.Host, srv.Port, missing), srv.Identity)
		st := sshFormState(h)
		if !st.sshFailed {
			t.Fatalf("check unexpectedly succeeded: %q", st.sshResult)
		}
		if !strings.Contains(st.sshResult, "does not exist") {
			t.Fatalf("result = %q, want does-not-exist guidance", st.sshResult)
		}
		closeDialog(t, h)
	})

	t.Run("key rejected", func(t *testing.T) {
		key := filepath.Join(t.TempDir(), "badkey")
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-C", "branchline-test", "-N", "", "-f", key).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v\n%s", err, out)
		}
		h := newTestApp(t)
		runCheck(t, h, fmt.Sprintf("ssh://%s@%s:%d%s", srv.User, srv.Host, srv.Port, repo.Path), key)
		st := sshFormState(h)
		if !st.sshFailed {
			t.Fatalf("check unexpectedly succeeded: %q", st.sshResult)
		}
		if !strings.Contains(st.sshResult, "Permission denied") {
			t.Fatalf("result = %q, want the server's Permission denied line", st.sshResult)
		}
		if !strings.Contains(st.sshResult, "ssh-add") || !strings.Contains(st.sshResult, "Identity file") {
			t.Fatalf("result = %q, want ssh-add / Identity file guidance", st.sshResult)
		}
		closeDialog(t, h)
	})

	t.Run("host key unverified", func(t *testing.T) {
		// A second loopback server the first server's known_hosts has never
		// seen: connecting through the first server's ssh wrapper fails strict
		// host key verification.
		srv2 := sshtest.StartLoopback(t)
		dir2 := srv2.InitRepo(t, "repo")
		h := newTestApp(t)
		// Point the dialog's repository construction at srv2 but through
		// srv1's ssh wrapper by giving no identity override: buildGitRepo uses
		// the executor of the parsed address, so the test supplies srv1's
		// wrapper through PATH (loopbackSSHRepo already prepended srv1's bin
		// dir) — but the executor defaults to the ssh on PATH, which IS srv1's
		// wrapper. The host key of srv2 is unknown to srv1's known_hosts.
		runCheck(t, h, fmt.Sprintf("ssh://%s@%s:%d%s", srv2.User, srv2.Host, srv2.Port, dir2), srv2.Identity)
		st := sshFormState(h)
		if !st.sshFailed {
			t.Fatalf("check unexpectedly succeeded: %q", st.sshResult)
		}
		if st.sshHostKey != sshHostKeyUnverified {
			t.Fatalf("hostKey class = %v, want unverified; result %q", st.sshHostKey, st.sshResult)
		}
		if !strings.Contains(st.sshResult, "does not accept host keys") {
			t.Fatalf("result = %q, want host key guidance", st.sshResult)
		}
		if !h.ui.HasText("Copy SSH Command") || !h.ui.HasText("Retry") {
			t.Fatalf("Copy SSH Command / Retry buttons missing; texts=%q", h.ui.Texts())
		}
		// Copy SSH Command puts a verification command on the clipboard that
		// holds only connection fields: never the remote path or key contents.
		if err := h.ui.Click("Copy SSH Command"); err != nil {
			t.Fatalf("click Copy SSH Command: %v", err)
		}
		clip := h.ui.Clipboard()
		if !strings.HasPrefix(clip, "ssh -o StrictHostKeyChecking=ask ") {
			t.Fatalf("clipboard = %q, want ssh verification command", clip)
		}
		if strings.Contains(clip, dir2) || strings.Contains(clip, srv2.RootDir) {
			t.Fatalf("clipboard leaks the remote path: %q", clip)
		}
		if !strings.Contains(clip, "-- "+srv2.Host) {
			t.Fatalf("clipboard = %q, want -- host", clip)
		}
		if !strings.Contains(clip, fmt.Sprintf("-p %d", srv2.Port)) {
			t.Fatalf("clipboard = %q, want -p port", clip)
		}
		keyBytes, err := os.ReadFile(srv2.Identity)
		if err == nil && len(keyBytes) > 0 {
			// Never copy any private key material.
			for _, line := range strings.Split(string(keyBytes), "\n") {
				line = strings.TrimSpace(line)
				if len(line) > 30 && !strings.HasPrefix(line, "-----") && strings.Contains(clip, line) {
					t.Fatalf("clipboard leaks key material: %q", clip)
				}
			}
		}

		// Retry really starts a new check: the dialog goes busy while it runs,
		// and the new answer — the host key is still unverified — publishes
		// when it finishes.
		retryGen := sshFormState(h).sshGen
		h.queue.PauseWorkers()
		if err := h.ui.Click("Retry"); err != nil {
			t.Fatalf("click Retry: %v", err)
		}
		h.ui.Frame()
		worker := h.queue.WaitWorker(t)
		if st := sshFormState(h); !st.sshBusy {
			t.Fatal("Retry did not start a new check: dialog not busy")
		}
		if got := sshFormState(h).sshGen; got <= retryGen {
			t.Fatalf("Retry did not advance the form generation: %d, was %d", got, retryGen)
		}
		h.queue.ReleaseWorker(worker)
		h.queue.ResumeWorkers()
		h.drain()
		st = sshFormState(h)
		if st.sshBusy {
			t.Fatal("dialog stayed busy after the retried check finished")
		}
		if !st.sshFailed || st.sshHostKey != sshHostKeyUnverified {
			t.Fatalf("retried check = %q (hostKey %v), want the unverified host key failure again", st.sshResult, st.sshHostKey)
		}
		closeDialog(t, h)
	})

	t.Run("server without git", func(t *testing.T) {
		h := newTestApp(t)
		h.app.openRepoFn = func(meta store.Repo) *git.Repo {
			return git.New(scriptExecutor{err: errors.New("git rev-parse: zsh:1: command not found: git")})
		}
		runCheck(t, h, fmt.Sprintf("ssh://%s@%s:%d%s", srv.User, srv.Host, srv.Port, repo.Path), srv.Identity)
		st := sshFormState(h)
		if !st.sshFailed {
			t.Fatalf("check unexpectedly succeeded: %q", st.sshResult)
		}
		if !strings.Contains(st.sshResult, "command not found") {
			t.Fatalf("result = %q, want command not found text", st.sshResult)
		}
		if !strings.Contains(st.sshResult, "Install git on the server") {
			t.Fatalf("result = %q, want install-git guidance", st.sshResult)
		}
		closeDialog(t, h)
	})

	t.Run("busybox server without git", func(t *testing.T) {
		h := newTestApp(t)
		h.app.openRepoFn = func(meta store.Repo) *git.Repo {
			return git.New(scriptExecutor{err: errors.New("git rev-parse: sh: git: not found")})
		}
		runCheck(t, h, fmt.Sprintf("ssh://%s@%s:%d%s", srv.User, srv.Host, srv.Port, repo.Path), srv.Identity)
		st := sshFormState(h)
		if !st.sshFailed || !strings.Contains(st.sshResult, "Install git on the server") {
			t.Fatalf("busybox result = %q (failed %v), want install-git guidance", st.sshResult, st.sshFailed)
		}
		closeDialog(t, h)
	})
}

// closeDialog dismisses the Add SSH dialog as a user does with Cancel.
func closeDialog(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// The host key failure classifier tells unverified, changed and revoked host
// keys apart, using the exact lines OpenSSH prints.
func TestRepoSSHHostKeyClassification(t *testing.T) {
	cases := []struct {
		name string
		err  string
		want sshHostKeyFailure
	}{
		{"unverified strict", "cannot connect to u@h: No ED25519 host key is known for [127.0.0.1]:2222 and you have requested strict checking.\nHost key verification failed.", sshHostKeyUnverified},
		{"unverified plain", "ssh: Host key verification failed.", sshHostKeyUnverified},
		{"changed", "WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!\nHost key for [127.0.0.1]:2222 has changed and you have requested strict checking.\nHost key verification failed.", sshHostKeyChanged},
		{"revoked", "WARNING: REVOKED HOST KEY DETECTED!\nThe ED25519 host key for h is marked as revoked.\nHost key verification failed.", sshHostKeyRevoked},
		{"auth failure is not a host key failure", "u@h: Permission denied (publickey).", sshHostKeyNone},
		{"not a git repo is not a host key failure", "fatal: not a git repository (or any of the parent directories): .git", sshHostKeyNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifySSHHostKeyFailure(errors.New(tc.err)); got != tc.want {
				t.Fatalf("classify = %v, want %v", got, tc.want)
			}
		})
	}

	// A changed host key also reads as a host key failure, not an auth or
	// generic failure, in the dialog guidance.
	if kind := classifySSHHostKeyFailure(errors.New("remote host identification has changed")); kind != sshHostKeyChanged {
		t.Fatalf("lowercase changed classify = %v", kind)
	}
}

// explainSSHError pairs each server error line with its actionable guidance,
// matching case-insensitively: dash's "can't cd to" reads as a missing path
// just as "no such file or directory" does, and git's "not a git repository"
// line is recognized however it is capitalized.
func TestRepoExplainSSHErrorGuidance(t *testing.T) {
	cases := []struct {
		name string
		err  string
		want string
	}{
		{"dash can't cd", "sh: 1: cd: can't cd to /srv/missing", "This path does not exist on the server"},
		{"dash can't cd mixed case", "sh: 1: cd: Can't CD to /Srv/Missing", "This path does not exist on the server"},
		{"no such file or directory", "bash: line 1: cd: /srv/missing: No such file or directory", "This path does not exist on the server"},
		{"not a git repository", "fatal: not a git repository (or any of the parent directories): .git", "No Git repository is at this path on the server"},
		{"not a git repository uppercase", "fatal: NOT A GIT REPOSITORY (or any of the parent directories): .git", "No Git repository is at this path on the server"},
		{"bash command not found", "bash: line 1: git: command not found", "Install git on the server"},
		{"zsh command not found", "zsh:1: command not found: git", "Install git on the server"},
		{"busybox not found", "sh: git: not found", "Install git on the server"},
		{"permission denied", "u@h: Permission denied (publickey).", "ssh-add"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := explainSSHError(errors.New(tc.err))
			if !strings.Contains(got, tc.want) {
				t.Fatalf("explainSSHError(%q) = %q, want guidance %q", tc.err, got, tc.want)
			}
		})
	}
}

// sshHostKeyCommand contains only connection fields: host, port, user and the
// identity file path, never the repository path, and quotes unsafe characters.
func TestRepoSSHHostKeyCommandContents(t *testing.T) {
	r := store.Repo{
		Kind:     store.SSH,
		Host:     "example.com",
		User:     "alice",
		Port:     2222,
		Identity: "~/.ssh/id ed25519",
		Path:     "/srv/secret-path",
	}
	cmd := sshHostKeyCommand(r)
	if strings.Contains(cmd, "/srv/secret-path") {
		t.Fatalf("command leaks remote path: %q", cmd)
	}
	for _, want := range []string{"ssh", "-o StrictHostKeyChecking=ask", "-p 2222", "-l alice", "-- example.com"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("command = %q, missing %q", cmd, want)
		}
	}
	// The identity file with a space is single-quoted.
	if !strings.Contains(cmd, "'~/.ssh/id ed25519'") {
		t.Fatalf("command = %q, want quoted identity", cmd)
	}
}

// Removing repositories from the switcher list never deletes any file: the
// local working tree and .git survive, the SSH server's repository and HEAD
// survive, removing the active repository opens the first remaining one,
// removing the last repository returns to the empty state, and a persistence
// failure keeps the entry and reports the error.
func TestRepoRemoveDoesNotDeleteFilesLoopbackSSH(t *testing.T) {
	sshRepo, srv := loopbackSSHRepo(t)
	sshHead := srv.RunGit(t, sshRepo.Path, "rev-parse", "HEAD")

	local := newGitRepo(t, "keptlocal")
	localHash := local.CommitFile("keep.txt", "keep\n", "keep commit")

	h := newTestApp(t, local.StoreRepo(), sshRepo)
	// Open the SSH repository so that removing it exercises the active switch.
	h.app.openStartupRepo()
	h.drain()
	if !h.app.session.HasRepo() {
		t.Fatal("no startup repository")
	}
	startMeta := h.app.session.Meta()

	// Make the SSH repo active whichever one startup picked.
	if startMeta.Kind != store.SSH {
		if err := h.ui.Click("Repository Switcher"); err != nil {
			t.Fatalf("open switcher: %v", err)
		}
		h.ui.Frame()
		if err := h.ui.Click(sshRepo.Name); err != nil {
			t.Fatalf("pick ssh repo: %v", err)
		}
		h.ui.Frame()
		h.drain()
	}
	if h.app.session.Meta().Kind != store.SSH {
		t.Fatalf("active kind = %q, want ssh", h.app.session.Meta().Kind)
	}

	// Remove the active SSH repository: the first remaining repository opens,
	// and the remote files are untouched.
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open switcher for remove: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Remove " + sshRepo.Name); err != nil {
		t.Fatalf("click Remove ssh: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if got := h.app.session.Meta().Name; got != "keptlocal" {
		t.Fatalf("after removing active SSH repo, active = %q, want keptlocal", got)
	}
	if repos := h.store.Repos(); len(repos) != 1 || repos[0].Name != "keptlocal" {
		t.Fatalf("store after remove = %+v, want only keptlocal", repos)
	}
	// The server files are intact: HEAD resolves, the tree is unchanged.
	if got := srv.RunGit(t, sshRepo.Path, "rev-parse", "HEAD"); got != sshHead {
		t.Fatalf("SSH repo HEAD after remove = %q, want %q", got, sshHead)
	}
	if got := srv.RunGit(t, sshRepo.Path, "log", "--format=%s", "-1"); got != "initial commit" {
		t.Fatalf("SSH repo log after remove = %q", got)
	}
	if content := srv.ReadFile(t, sshRepo.Path, "a.txt"); string(content) != "one\n" {
		t.Fatalf("SSH repo a.txt after remove = %q", content)
	}

	// Remove the last repository: the session closes to the empty state, and
	// the local files are untouched. Removing an entry keeps the popup open
	// for list management, so the remove button is still in reach when the
	// active repository switched away; if the popup closed (because the click
	// sequence dismissed it), reopen it first.
	if _, ok := h.ui.Find("Remove keptlocal"); !ok {
		if err := h.ui.Click("Repository Switcher"); err != nil {
			t.Fatalf("open switcher for last remove: %v", err)
		}
		h.ui.Frame()
	}
	if err := h.ui.Click("Remove keptlocal"); err != nil {
		t.Fatalf("click Remove keptlocal: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if h.app.session.HasRepo() {
		t.Fatalf("session still open after removing last repo: %+v", h.app.session.Meta())
	}
	if !h.ui.HasText("No Repository Open") {
		t.Fatalf("empty state missing after removing last repo; texts=%q", h.ui.Texts())
	}
	if repos := h.store.Repos(); len(repos) != 0 {
		t.Fatalf("store not empty after removing last repo: %+v", repos)
	}
	// The local repository on disk is intact.
	if _, err := os.Stat(filepath.Join(local.Path(), ".git")); err != nil {
		t.Fatalf("local .git missing after remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(local.Path(), "keep.txt")); err != nil {
		t.Fatalf("local keep.txt missing after remove: %v", err)
	}
	out, err := tryGitCommand(local.Path(), "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(out) != localHash {
		t.Fatalf("local HEAD after remove = %q err %v, want %q", out, err, localHash)
	}
}

// When the store cannot persist the removal of the active repository, the
// error is shown, the entry stays in the list, the session is untouched —
// same active repository, same session generation — and no file is deleted.
// Removing the active repository is what exercises the wasActive branch of
// removeRepo, whose persistence-failure return keeps everything as it was.
func TestRepoRemovePersistFailureKeepsEverythingLoopbackSSH(t *testing.T) {
	sshRepo, srv := loopbackSSHRepo(t)
	local := newGitRepo(t, "persistlocal")
	local.CommitFile("p.txt", "p\n", "p commit")

	h := newTestApp(t, local.StoreRepo(), sshRepo)
	h.app.openStartupRepo()
	h.drain()
	if !h.app.session.HasRepo() {
		t.Fatal("no startup repository")
	}

	// Make the store's directory unwritable so save fails.
	storeDir := filepath.Dir(h.store.Path())
	if err := os.Chmod(storeDir, 0o500); err != nil {
		t.Fatalf("chmod store dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(storeDir, 0o700) })

	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open switcher: %v", err)
	}
	h.ui.Frame()
	// Remove the ACTIVE entry: the persistence failure must keep it open and
	// untouched, not switch away or close the session.
	activeID := h.app.activeRepoID()
	activeGen := h.app.session.Generation()
	victim := h.app.session.Meta().Name
	if err := h.ui.Click("Remove " + victim); err != nil {
		t.Fatalf("click Remove %s: %v", victim, err)
	}
	h.ui.Frame()
	h.drain()

	// The error is shown on screen.
	if !h.ui.HasText("Cannot Remove Repository") {
		t.Fatalf("remove error modal missing; texts=%q", h.ui.Texts())
	}
	// The entry is still in the list.
	if repos := h.store.Repos(); len(repos) != 2 {
		t.Fatalf("store lost entries despite failed persist: %+v", repos)
	}
	// The session is untouched: same repository, same generation (no reload,
	// no close, no switch to the remaining entry).
	if !h.app.session.HasRepo() {
		t.Fatal("session closed despite failed removal")
	}
	if got := h.app.activeRepoID(); got != activeID {
		t.Fatalf("active repo changed to %q despite failed removal, want %q", got, activeID)
	}
	if got := h.app.session.Generation(); got != activeGen {
		t.Fatalf("session generation changed to %d despite failed removal, want %d", got, activeGen)
	}
	// Close the modal.
	if err := h.ui.Click("Close"); err != nil {
		t.Fatalf("close remove error modal: %v", err)
	}
	h.ui.Frame()

	// Nothing was deleted.
	if _, err := os.Stat(filepath.Join(local.Path(), ".git")); err != nil {
		t.Fatalf("local .git missing: %v", err)
	}
	if got := srv.RunGit(t, sshRepo.Path, "rev-parse", "--verify", "HEAD"); got == "" {
		t.Fatal("SSH repo HEAD missing after failed removal")
	}

	// Restore writability and verify removal now works, so the test does not
	// pass merely because removal is broken entirely.
	if err := os.Chmod(storeDir, 0o700); err != nil {
		t.Fatalf("restore store dir: %v", err)
	}
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("reopen switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Remove " + victim); err != nil {
		t.Fatalf("re-click Remove %s: %v", victim, err)
	}
	h.ui.Frame()
	h.drain()
	if repos := h.store.Repos(); len(repos) != 1 {
		t.Fatalf("removal with writable store did not apply: %+v", repos)
	}
}

// Adding or switching repositories through the UI persists the active
// repository, so a restart — a new store and app on the same repositories.json
// calling openStartupRepo — restores the repository that was open, local or
// SSH, with its real commits loaded.
func TestRepoRestartRestoresLastOpenedLoopbackSSH(t *testing.T) {
	// openStartupRepo honors BRANCHLINE_OPEN when set: pin it empty so a
	// developer machine's environment cannot redirect the "restart".
	t.Setenv("BRANCHLINE_OPEN", "")
	sshRepo, srv := loopbackSSHRepo(t)
	sshHead := srv.RunGit(t, sshRepo.Path, "rev-parse", "HEAD")

	local := newGitRepo(t, "restartlocal")
	localHash := local.CommitFile("r.txt", "r\n", "restart commit")

	// One shared store path across the two "runs" of the app.
	storePath := filepath.Join(t.TempDir(), "repositories.json")
	newHarness := func(t *testing.T) *testHarness {
		t.Helper()
		s, err := store.Open(storePath)
		if err != nil {
			t.Fatalf("open shared store: %v", err)
		}
		q := newUIQueue()
		t.Cleanup(q.releaseAllAndWait)
		a := newApp(s)
		a.postFn = q.post
		a.spawnFn = q.spawn
		h := &testHarness{
			t:     t,
			app:   a,
			store: s,
			ui:    ui.NewTester(a.view, 1200, 800),
			queue: q,
		}
		h.ui.Frame()
		return h
	}

	// First run: add both repositories through the real UI and leave the SSH
	// one open.
	first := newHarness(t)
	// Add the local repository through the picker.
	wantRoot, err := filepath.EvalSymlinks(local.Path())
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	stubPickLocalFolder(t, func() (string, error) { return wantRoot, nil })
	if err := first.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("first run open switcher: %v", err)
	}
	first.ui.Frame()
	if err := first.ui.Click("Add Local Repository…"); err != nil {
		t.Fatalf("first run add local: %v", err)
	}
	first.ui.Frame()
	first.drain()
	if !first.app.session.HasRepo() {
		t.Fatal("first run did not open the added local repo")
	}

	// Add the SSH repository through the dialog.
	openSSHForm(t, first)
	typeSSHForm(t, first, fmt.Sprintf("ssh://%s@%s:%d%s", srv.User, srv.Host, srv.Port, sshRepo.Path), srv.Identity, "")
	first.ui.Frame()
	if err := first.ui.Click("Add"); err != nil {
		t.Fatalf("first run click Add: %v", err)
	}
	first.ui.Frame()
	first.drain()
	if first.app.session.Meta().Kind != store.SSH {
		t.Fatalf("first run active kind = %q, want ssh", first.app.session.Meta().Kind)
	}
	sshID := first.app.session.Meta().ID

	// Restart: a new app on the same store file restores the SSH repository.
	second := newHarness(t)
	second.app.openStartupRepo()
	second.drain()
	if !second.app.session.HasRepo() {
		t.Fatal("restart did not restore any repository")
	}
	if got := second.app.session.Meta().ID; got != sshID {
		t.Fatalf("restored repo ID = %q, want SSH repo %q", got, sshID)
	}
	if got := second.app.session.Snapshot().Status.Head; got != sshHead {
		t.Fatalf("restored SSH HEAD = %q, want %q", got, sshHead)
	}

	// Switch to the local repository through the switcher, then restart again:
	// the local repository comes back.
	if err := second.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("second run open switcher: %v", err)
	}
	second.ui.Frame()
	if err := second.ui.Click("restartlocal"); err != nil {
		t.Fatalf("second run pick restartlocal: %v", err)
	}
	second.ui.Frame()
	second.drain()
	if got := second.app.session.Meta().Name; got != "restartlocal" {
		t.Fatalf("second run active = %q, want restartlocal", got)
	}
	localID := second.app.session.Meta().ID

	third := newHarness(t)
	third.app.openStartupRepo()
	third.drain()
	if !third.app.session.HasRepo() {
		t.Fatal("second restart did not restore any repository")
	}
	if got := third.app.session.Meta().ID; got != localID {
		t.Fatalf("third run repo ID = %q, want local %q", got, localID)
	}
	if got := third.app.session.Snapshot().Status.Head; got != localHash {
		t.Fatalf("third run HEAD = %q, want %q", got, localHash)
	}
}

// The configured Linux SSH host (sshlab) walks the whole Add SSH flow: the
// dialog connects with a config-alias address, Test Connection reports the
// server, Add saves and opens the repository with its real commits, and
// removing it from the list leaves the remote files intact.
func TestConfiguredLinuxRepoSwitcherAndAddSSH(t *testing.T) {
	repo, srv := configuredLinuxRepo(t)
	wantHead := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")

	h := newTestApp(t)
	openSSHForm(t, h)
	// The configured Linux host is reached through its ssh config alias, with
	// no explicit identity file: sshlab puts both on ssh's configuration.
	typeSSHForm(t, h, srv.Address(repo.Path), "", "")
	h.ui.Frame()

	// Test Connection reports success without saving.
	if err := h.ui.Click("Test Connection"); err != nil {
		t.Fatalf("click Test Connection: %v", err)
	}
	h.ui.Frame()
	h.drain()
	st := sshFormState(h)
	if st.sshFailed {
		t.Fatalf("configured Linux Test Connection failed: %q", st.sshResult)
	}
	if !strings.Contains(st.sshResult, "Connected:") || !strings.Contains(st.sshResult, "git version") {
		t.Fatalf("configured Linux result = %q, want Connected with version", st.sshResult)
	}
	// The result is readable in the accessibility tree on the real Linux
	// server too: the live region container names it, and a static text node
	// inside carries the result text itself.
	if info, ok := h.ui.AccessByRole(ui.RoleStatus, "Test Connection Result"); !ok || info.Busy {
		t.Fatalf("no idle RoleStatus container named Test Connection Result = %+v (found %v)", info, ok)
	}
	if texts := accessTextNames(h); !strings.Contains(texts, "Connected:") || !strings.Contains(texts, "git version") {
		t.Fatalf("no accessible static text holds the success result; access texts=%q", texts)
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("Test Connection saved: %+v", h.store.Repos())
	}

	// Add saves and opens with real commits.
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
		t.Fatalf("Add did not open the configured Linux repo: %+v", h.app.session.Meta())
	}
	if got := h.app.session.Snapshot().Status.Head; got != wantHead {
		t.Fatalf("configured Linux HEAD = %q, want %q", got, wantHead)
	}
	if h.app.session.Snapshot().Commits[0].Subject != "initial commit" {
		t.Fatalf("first commit subject = %q", h.app.session.Snapshot().Commits[0].Subject)
	}

	// Removing it from the list leaves the remote files intact.
	name := h.app.session.Meta().Name
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open switcher for remove: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Remove " + name); err != nil {
		t.Fatalf("click Remove %s: %v", name, err)
	}
	h.ui.Frame()
	h.drain()
	if len(h.store.Repos()) != 0 {
		t.Fatalf("store not empty after remove: %+v", h.store.Repos())
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "HEAD"); got != wantHead {
		t.Fatalf("remote HEAD after remove = %q, want %q", got, wantHead)
	}
	if content := srv.ReadFile(t, repo.Path, "a.txt"); string(content) != "one\n" {
		t.Fatalf("remote a.txt after remove = %q", content)
	}
}

// While a connection check runs, the dialog shows its busy state and both
// Test Connection and Add stay disabled, so a second click cannot start a
// reentrant check.
func TestRepoAddSSHBusyDisablesReentryLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	h := newTestApp(t)
	openSSHForm(t, h)
	typeSSHForm(t, h, fmt.Sprintf("ssh://%s@%s:%d%s", srv.User, srv.Host, srv.Port, repo.Path), srv.Identity, "")
	h.ui.Frame()

	h.queue.PauseWorkers()
	if err := h.ui.Click("Test Connection"); err != nil {
		t.Fatalf("click Test Connection: %v", err)
	}
	h.ui.Frame()
	worker := h.queue.WaitWorker(t)
	defer h.queue.ReleaseWorker(worker)

	if st := sshFormState(h); !st.sshBusy {
		t.Fatal("dialog not busy while the check runs")
	}
	if !h.ui.HasText("Connecting…") {
		t.Fatalf("busy indicator missing; texts=%q", h.ui.Texts())
	}
	if info, ok := h.ui.AccessByRole(ui.RoleButton, "Test Connection"); !ok || !info.Disabled {
		t.Fatalf("Test Connection while busy = %+v (found %v), want disabled", info, ok)
	}
	if info, ok := h.ui.AccessByRole(ui.RoleButton, "Add"); !ok || !info.Disabled {
		t.Fatalf("Add while busy = %+v (found %v), want disabled", info, ok)
	}

	h.queue.ReleaseWorker(worker)
	h.queue.ResumeWorkers()
	h.drain()

	if st := sshFormState(h); st.sshBusy {
		t.Fatal("dialog stayed busy after the check finished")
	}
	if info, ok := h.ui.AccessByRole(ui.RoleButton, "Test Connection"); !ok || info.Disabled {
		t.Fatalf("Test Connection after completion = %+v, want enabled", info)
	}
}

// accessTextNames returns the accessible names of the static text nodes of
// the last frame, joined for substring assertions.
func accessTextNames(h *testHarness) string {
	var texts []string
	for _, info := range h.ui.AccessAll() {
		if info.Role == ui.RoleText {
			texts = append(texts, info.Name)
		}
	}
	return strings.Join(texts, "\n")
}

// Test Connection publishes its result into the accessibility tree: success
// and failure both put the result text on a readable static text node inside
// the "Test Connection Result" live region, success is announced politely
// when it replaces the busy state, and failures alert at once — for a check
// that fails in the background as much as for an address that never parses.
func TestRepoTestConnectionResultAccessibleLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)

	t.Run("success", func(t *testing.T) {
		h := newTestApp(t)
		h.ui.UseScreenReader()
		openSSHForm(t, h)
		typeSSHForm(t, h, fmt.Sprintf("ssh://%s@%s:%d%s", srv.User, srv.Host, srv.Port, repo.Path), srv.Identity, "")
		h.ui.Frame()
		if err := h.ui.Click("Test Connection"); err != nil {
			t.Fatalf("click Test Connection: %v", err)
		}
		h.ui.Frame()
		if busyInfo, ok := h.ui.AccessByRole(ui.RoleStatus, "Test Connection Result"); !ok || !busyInfo.Busy {
			t.Fatalf("busy RoleStatus container Test Connection Result = %+v (found %v), want Busy=true", busyInfo, ok)
		}
		if busyTexts := accessTextNames(h); !strings.Contains(busyTexts, "Connecting…") {
			t.Fatalf("busy static text missing Connecting…; access texts=%q", busyTexts)
		}
		h.drain()

		st := sshFormState(h)
		if st.sshFailed {
			t.Fatalf("Test Connection failed: %q", st.sshResult)
		}
		info, ok := h.ui.AccessByRole(ui.RoleStatus, "Test Connection Result")
		if !ok {
			t.Fatalf("no RoleStatus container named Test Connection Result; texts=%q", h.ui.Texts())
		}
		if info.Busy {
			t.Fatalf("Test Connection Result still busy after completion: %+v", info)
		}
		texts := accessTextNames(h)
		for _, want := range []string{"Connected:", "git version", repo.Path} {
			if !strings.Contains(texts, want) {
				t.Fatalf("no accessible static text contains %q; access texts=%q", want, texts)
			}
		}
		// The success replaced the busy state of the same live region, so a
		// screen reader announces the result politely.
		announced := false
		for _, a := range h.ui.Announcements() {
			if strings.Contains(a, "Connected:") && strings.Contains(a, repo.Path) {
				announced = true
			}
		}
		if !announced {
			t.Fatalf("success result not announced; announcements=%q", h.ui.Announcements())
		}
		closeDialog(t, h)
	})

	// failCase drives one failing Test Connection and asserts the alert role,
	// the readable error text and the immediate alert announcement.
	failCase := func(t *testing.T, address, identity string, want ...string) {
		t.Helper()
		h := newTestApp(t)
		h.ui.UseScreenReader()
		openSSHForm(t, h)
		typeSSHForm(t, h, address, identity, "")
		h.ui.Frame()
		if err := h.ui.Click("Test Connection"); err != nil {
			t.Fatalf("click Test Connection: %v", err)
		}
		h.ui.Frame()
		h.drain()

		st := sshFormState(h)
		if !st.sshFailed {
			t.Fatalf("Test Connection unexpectedly succeeded: %q", st.sshResult)
		}
		if _, ok := h.ui.AccessByRole(ui.RoleAlert, "Test Connection Result"); !ok {
			t.Fatalf("no RoleAlert container named Test Connection Result; texts=%q", h.ui.Texts())
		}
		texts := accessTextNames(h)
		for _, w := range want {
			if !strings.Contains(texts, w) {
				t.Fatalf("no accessible static text contains %q; access texts=%q", w, texts)
			}
		}
		alerts := h.ui.Alerts()
		joined := strings.Join(alerts, "\n")
		for _, w := range want {
			if !strings.Contains(joined, w) {
				t.Fatalf("alerts %q missing %q", alerts, w)
			}
		}
		closeDialog(t, h)
	}

	t.Run("async missing remote path", func(t *testing.T) {
		missing := srv.TempDir(t, "missingbase") + "/gone"
		failCase(t, fmt.Sprintf("ssh://%s@%s:%d%s", srv.User, srv.Host, srv.Port, missing), srv.Identity,
			"does not exist")
	})

	t.Run("async not a git repository", func(t *testing.T) {
		empty := srv.TempDir(t, "empty")
		failCase(t, fmt.Sprintf("ssh://%s@%s:%d%s", srv.User, srv.Host, srv.Port, empty), srv.Identity,
			"not a git repository")
	})

	t.Run("sync address parse error", func(t *testing.T) {
		failCase(t, "no-address-here", "",
			"write the address as user@host:/path or ssh://user@host:port/path")
	})
}

// isMissingRepoError tells a deleted or file-replaced repository directory
// apart from every other load failure, for a local repository from git's
// "cannot change to" line and for an SSH repository from the login shell's
// cd error of each shell dialect, while ssh transport failures, permission
// errors and corrupt repositories never read as a missing path.
func TestIsMissingRepoError(t *testing.T) {
	sshErr := func(code int, stderr string) error {
		return fmt.Errorf("read status: %w", &git.Error{Args: []string{"status"}, ExitCode: code, Stderr: stderr})
	}
	localErr := func(stderr string) error {
		return fmt.Errorf("read status: %w", &git.Error{Args: []string{"status"}, ExitCode: 128, Stderr: stderr})
	}

	cases := []struct {
		name string
		kind store.Kind
		err  error
		want bool
	}{
		// Local positives: the directory deleted or replaced by a plain file.
		{"local dir deleted", store.Local, localErr("fatal: cannot change to '/tmp/x/repo': No such file or directory"), true},
		{"local dir replaced by file", store.Local, localErr("fatal: cannot change to '/tmp/x/repo': Not a directory"), true},
		{"local plain error text", store.Local, errors.New("fatal: cannot change to '/tmp/x/repo': No such file or directory"), true},
		// Local negatives: everything else keeps the ordinary failure page.
		{"local nil error", store.Local, nil, false},
		{"local permission denied", store.Local, localErr("fatal: cannot change to '/tmp/x/repo': Permission denied"), false},
		{"local not a git repository", store.Local, localErr("fatal: not a git repository (or any of the parent directories): .git"), false},
		{"local not a git repository mount point", store.Local, localErr("fatal: not a git repository (or any parent up to mount point /srv)"), false},
		{"local broken ref", store.Local, localErr("fatal: bad object refs/heads/main"), false},
		{"local git missing", store.Local, errors.New(`exec: "git": executable file not found in $PATH`), false},
		{"local empty stderr", store.Local, &git.Error{Args: []string{"status"}, ExitCode: 128, Stderr: ""}, false},

		// SSH positives: the login shell cannot cd into the remote path, in
		// the wording of each shell family.
		{"ssh dash deleted", store.SSH, sshErr(1, "sh: 1: cd: can't cd to /srv/app"), true},
		{"ssh dash deleted exit 2", store.SSH, sshErr(2, "sh: 1: cd: can't cd to /srv/app"), true},
		{"ssh busybox ash deleted", store.SSH, sshErr(1, "sh: cd: line 1: can't cd to '/srv/app': No such file or directory"), true},
		{"ssh busybox ash file", store.SSH, sshErr(1, "sh: cd: line 1: can't cd to /srv/app: Not a directory"), true},
		{"ssh bash deleted", store.SSH, sshErr(1, "bash: line 1: cd: /srv/app: No such file or directory"), true},
		{"ssh bash 5.0 deleted", store.SSH, sshErr(1, "bash: line 0: cd: /srv/app: No such file or directory"), true},
		{"ssh bash file", store.SSH, sshErr(1, "bash: line 1: cd: /srv/app: Not a directory"), true},
		{"ssh macos sh deleted", store.SSH, sshErr(1, "sh: line 0: cd: /srv/app: No such file or directory"), true},
		{"ssh zsh deleted", store.SSH, sshErr(1, "zsh:cd:1: no such file or directory: /srv/app"), true},
		{"ssh zsh file", store.SSH, sshErr(1, "zsh:cd:1: not a directory: /srv/app"), true},
		{"ssh error string fallback", store.SSH, errors.New("git status: sh: 1: cd: can't cd to /srv/app"), true},
		// SSH negatives: transport failures, remote permission errors and
		// corrupt repositories keep the ordinary failure page.
		{"ssh nil error", store.SSH, nil, false},
		{"ssh missing local key", store.SSH, sshErr(255, "cannot connect to user@host: user@host: Permission denied (publickey).\nIdentity file /home/x/.ssh/id_ed25519 not accessible: No such file or directory"), false},
		{"ssh missing local key warning non-255", store.SSH, sshErr(128, "Warning: Identity file /home/x/.ssh/id_ed25519 not accessible: No such file or directory.\nfatal: not a git repository (or any of the parent directories): .git"), false},
		{"ssh auth failure", store.SSH, sshErr(255, "cannot connect to user@host: user@host: Permission denied (publickey)."), false},
		{"ssh host key failure", store.SSH, sshErr(255, "cannot connect to user@host: Host key verification failed."), false},
		{"ssh network timeout", store.SSH, sshErr(255, "cannot connect to user@host: ssh: connect to host example.com port 22: Operation timed out"), false},
		{"ssh transport without 255", store.SSH, sshErr(1, "cannot connect to user@host: something odd"), false},
		{"ssh remote cd permission bash", store.SSH, sshErr(1, "bash: line 1: cd: /srv/app: Permission denied"), false},
		{"ssh remote cd permission zsh", store.SSH, sshErr(1, "zsh:cd:1: permission denied: /srv/app"), false},
		{"ssh remote cd permission busybox ash", store.SSH, sshErr(1, "sh: cd: line 1: can't cd to /srv/app: Permission denied"), false},
		{"ssh not a git repository", store.SSH, sshErr(128, "fatal: not a git repository (or any of the parent directories): .git"), false},
		{"ssh other git error", store.SSH, sshErr(128, "fatal: bad object refs/heads/main"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMissingRepoError(tc.kind, tc.err); got != tc.want {
				t.Fatalf("isMissingRepoError(%q, %v) = %v, want %v", tc.kind, tc.err, got, tc.want)
			}
		})
	}
}

// assertMissingRepoNotice checks the missing-repository notice of the page:
// its title, explanation and redacted location are readable, no raw git or
// shell error leaks into it, and both actions are offered.
func assertMissingRepoNotice(t *testing.T, h *testHarness, name, location string) {
	t.Helper()
	texts := h.ui.Texts()
	if !h.ui.HasText("Repository Not Found") {
		t.Fatalf("Repository Not Found title missing; texts=%q", texts)
	}
	if !h.ui.HasText(fmt.Sprintf("for %q could not be found", name)) {
		t.Fatalf("explanation naming %q missing; texts=%q", name, texts)
	}
	if !h.ui.HasText("It may have been moved or deleted.") {
		t.Fatalf("moved-or-deleted explanation missing; texts=%q", texts)
	}
	if !h.ui.HasText(git.RedactText(location)) {
		t.Fatalf("redacted location %q missing; texts=%q", git.RedactText(location), texts)
	}
	for _, raw := range []string{"fatal:", "read status:", "can't cd to", "no such file or directory", "Not a directory"} {
		if h.ui.HasText(raw) {
			t.Fatalf("raw git or shell error %q visible; texts=%q", raw, texts)
		}
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Retry"); !ok {
		t.Fatalf("Retry button missing; texts=%q", texts)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Remove from List"); !ok {
		t.Fatalf("Remove from List button missing; texts=%q", texts)
	}
}

// initLocalRepoAt initializes a real Git repository at dir on branch main
// with one commit and returns its HEAD hash.
func initLocalRepoAt(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	runGitCommand(t, dir, "init", "-b", "main")
	runGitCommand(t, dir, "config", "user.name", "Branchline Test")
	runGitCommand(t, dir, "config", "user.email", "test@branchline.local")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	runGitCommand(t, dir, "add", "--", "a.txt")
	runGitCommand(t, dir, "commit", "-q", "-m", "initial commit")
	return strings.TrimSpace(runGitCommand(t, dir, "rev-parse", "HEAD"))
}

// newHarnessOnStore builds an app harness on the store file at storePath, as
// a restart of the app on the machine that wrote it.
func newHarnessOnStore(t *testing.T, storePath string) *testHarness {
	t.Helper()
	s, err := store.Open(storePath)
	if err != nil {
		t.Fatalf("open shared store: %v", err)
	}
	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)
	a := newApp(s)
	a.postFn = q.post
	a.spawnFn = q.spawn
	h := &testHarness{
		t:     t,
		app:   a,
		store: s,
		ui:    ui.NewTester(a.view, 1200, 800),
		queue: q,
	}
	h.ui.Frame()
	return h
}

// A local repository deleted after it was added restores into the readable
// missing-repository notice instead of git's raw error, on Changes and
// History alike. Retry reloads it once the directory is back, and Remove
// from List only edits the repository list: a plain file placed at the
// repository's path afterwards, sibling files and other repositories all
// survive it byte for byte. A store that cannot persist the removal keeps
// the entry, the session and every file, and reports the error.
func TestRepoMissingLocalRestoreRetryAndRemoveSafety(t *testing.T) {
	// openStartupRepo honors BRANCHLINE_OPEN when set: pin it empty so a
	// developer machine's environment cannot redirect the "restart".
	t.Setenv("BRANCHLINE_OPEN", "")

	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "sibling.txt"), []byte("sibling sentinel\n"), 0o644); err != nil {
		t.Fatalf("write sibling sentinel: %v", err)
	}
	victimDir := filepath.Join(parent, "victim")
	otherDir := filepath.Join(parent, "other")
	initLocalRepoAt(t, victimDir)
	otherHead := initLocalRepoAt(t, otherDir)
	victimRoot, err := filepath.EvalSymlinks(victimDir)
	if err != nil {
		t.Fatalf("resolve victim root: %v", err)
	}
	otherRoot, err := filepath.EvalSymlinks(otherDir)
	if err != nil {
		t.Fatalf("resolve other root: %v", err)
	}

	storePath := filepath.Join(t.TempDir(), "repositories.json")

	// First run: add both repositories through the real product entry, with
	// victim left active.
	first := newHarnessOnStore(t, storePath)
	addLocalRepoThroughSwitcher(t, first, otherDir)
	if got := first.app.session.Meta().Name; got != "other" {
		t.Fatalf("first run active after adding other = %q, want other", got)
	}
	addLocalRepoThroughSwitcher(t, first, victimDir)
	if got := first.app.session.Meta().Name; got != "victim" {
		t.Fatalf("first run active after adding victim = %q, want victim", got)
	}
	if repos := first.store.Repos(); len(repos) != 2 {
		t.Fatalf("first run store = %+v, want two entries", repos)
	}

	// Delete victim's directory and restart: the readable notice replaces
	// the raw git error on both pages.
	if err := os.RemoveAll(victimDir); err != nil {
		t.Fatalf("delete victim dir: %v", err)
	}
	second := newHarnessOnStore(t, storePath)
	second.app.openStartupRepo()
	second.drain()
	if err := second.app.session.LoadErr(); err == nil {
		t.Fatal("restart into the deleted repository loaded without error")
	}
	assertMissingRepoNotice(t, second, "victim", victimRoot)
	mustClick(t, second, "History")
	second.ui.Frame()
	assertMissingRepoNotice(t, second, "victim", victimRoot)

	// Recreate the repository and Retry through the real button: the notice
	// clears and the restored commits load.
	restoredHead := initLocalRepoAt(t, victimDir)
	mustClick(t, second, "Retry")
	second.drain()
	if err := second.app.session.LoadErr(); err != nil {
		t.Fatalf("load error after Retry with the directory restored: %v", err)
	}
	if got := second.app.session.Snapshot().Status.Head; got != restoredHead {
		t.Fatalf("HEAD after Retry = %q, want %q", got, restoredHead)
	}
	if second.ui.HasText("Repository Not Found") {
		t.Fatalf("missing notice stayed after a successful Retry; texts=%q", second.ui.Texts())
	}

	// Delete victim again and restart into the notice. With the store
	// directory unwritable, Remove from List reports the failure and keeps
	// the entry, the session generation and every file.
	if err := os.RemoveAll(victimDir); err != nil {
		t.Fatalf("re-delete victim dir: %v", err)
	}
	third := newHarnessOnStore(t, storePath)
	third.app.openStartupRepo()
	third.drain()
	assertMissingRepoNotice(t, third, "victim", victimRoot)

	storeDir := filepath.Dir(storePath)
	if err := os.Chmod(storeDir, 0o500); err != nil {
		t.Fatalf("chmod store dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(storeDir, 0o700) })
	activeID := third.app.activeRepoID()
	activeGen := third.app.session.Generation()
	mustClick(t, third, "Remove from List")
	third.ui.Frame()
	if !third.ui.HasText("Cannot Remove Repository") {
		t.Fatalf("remove failure modal missing; texts=%q", third.ui.Texts())
	}
	if repos := third.store.Repos(); len(repos) != 2 {
		t.Fatalf("store lost entries despite failed persist: %+v", repos)
	}
	if got := third.app.activeRepoID(); got != activeID {
		t.Fatalf("active repo changed to %q despite failed removal, want %q", got, activeID)
	}
	if got := third.app.session.Generation(); got != activeGen {
		t.Fatalf("session generation changed to %d despite failed removal, want %d", got, activeGen)
	}
	mustClick(t, third, "Close")
	third.ui.Frame()

	// Restore writability. Replace victim's path with a plain sentinel file:
	// the removal must edit the list and nothing else.
	if err := os.Chmod(storeDir, 0o700); err != nil {
		t.Fatalf("restore store dir: %v", err)
	}
	// victimRoot itself no longer exists; writing at the path itself turns
	// it into a plain file, the replacement case.
	if err := os.WriteFile(victimRoot, []byte("do not delete me\n"), 0o644); err != nil {
		t.Fatalf("write sentinel at victim path: %v", err)
	}
	mustClick(t, third, "Remove from List")
	third.drain()

	if repos := third.store.Repos(); len(repos) != 1 || repos[0].Name != "other" {
		t.Fatalf("store after remove = %+v, want only other", repos)
	}
	if got := third.app.session.Meta().Name; got != "other" {
		t.Fatalf("active after removing victim = %q, want other", got)
	}
	if got := third.app.session.Snapshot().Status.Head; got != otherHead {
		t.Fatalf("other HEAD after switch = %q, want %q", got, otherHead)
	}
	// Nothing on disk was touched: the sentinel file at victim's path, the
	// sibling file and the other repository all survive byte for byte.
	if got, err := os.ReadFile(victimRoot); err != nil || string(got) != "do not delete me\n" {
		t.Fatalf("sentinel file at victim path = %q err %v, want intact", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(parent, "sibling.txt")); err != nil || string(got) != "sibling sentinel\n" {
		t.Fatalf("sibling.txt = %q err %v, want intact", got, err)
	}
	if _, err := os.Stat(filepath.Join(otherRoot, ".git")); err != nil {
		t.Fatalf("other .git missing after remove: %v", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, otherRoot, "rev-parse", "HEAD")); got != otherHead {
		t.Fatalf("other on-disk HEAD = %q, want %q", got, otherHead)
	}

	// Removing the last remaining repository while it is missing too returns
	// to the empty state, again without touching files: replace other's path
	// with a plain sentinel file first.
	if err := os.RemoveAll(otherDir); err != nil {
		t.Fatalf("delete other dir: %v", err)
	}
	if err := os.WriteFile(otherRoot, []byte("other sentinel\n"), 0o644); err != nil {
		t.Fatalf("write sentinel at other path: %v", err)
	}
	fourth := newHarnessOnStore(t, storePath)
	fourth.app.openStartupRepo()
	fourth.drain()
	assertMissingRepoNotice(t, fourth, "other", otherRoot)
	mustClick(t, fourth, "Remove from List")
	fourth.drain()

	if repos := fourth.store.Repos(); len(repos) != 0 {
		t.Fatalf("store not empty after removing last repo: %+v", repos)
	}
	if fourth.app.session.HasRepo() {
		t.Fatalf("session still open after removing last repo: %+v", fourth.app.session.Meta())
	}
	if !fourth.ui.HasText("No Repository Open") {
		t.Fatalf("empty state missing after removing last repo; texts=%q", fourth.ui.Texts())
	}
	if got, err := os.ReadFile(otherRoot); err != nil || string(got) != "other sentinel\n" {
		t.Fatalf("sentinel file at other path = %q err %v, want intact", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(parent, "sibling.txt")); err != nil || string(got) != "sibling sentinel\n" {
		t.Fatalf("sibling.txt after last remove = %q err %v, want intact", got, err)
	}
	if got, err := os.ReadFile(victimRoot); err != nil || string(got) != "do not delete me\n" {
		t.Fatalf("sentinel file at victim path after last remove = %q err %v, want intact", got, err)
	}
}

// An SSH repository whose remote directory is deleted after it was added
// restores into the readable missing-repository notice instead of the login
// shell's raw cd error, on Changes and History alike. Remove from List only
// edits the repository list: a sentinel file placed at the remote path
// afterwards and the sibling files next to it all survive on the server.
func TestRepoMissingSSHRestoreAndRemoveSafetyLoopbackSSH(t *testing.T) {
	// openStartupRepo honors BRANCHLINE_OPEN when set: pin it empty so a
	// developer machine's environment cannot redirect the "restart".
	t.Setenv("BRANCHLINE_OPEN", "")
	repo, srv := loopbackSSHRepo(t)

	// A sibling sentinel file next to the repository proves the removal does
	// not touch what sits beside the missing path.
	srv.RunShell(t, "printf 'keep me\\n' > "+git.ShellQuote(srv.RootDir+"/keep-sibling.txt"))
	t.Cleanup(func() {
		srv.RunShell(t, "rm -f -- "+git.ShellQuote(srv.RootDir+"/keep-sibling.txt"))
	})

	storePath := filepath.Join(t.TempDir(), "repositories.json")

	// First run: add the repository through the real Add SSH dialog and
	// leave it open.
	first := newHarnessOnStore(t, storePath)
	openSSHForm(t, first)
	typeSSHForm(t, first, fmt.Sprintf("ssh://%s@%s:%d%s", srv.User, srv.Host, srv.Port, repo.Path), srv.Identity, "")
	first.ui.Frame()
	if err := first.ui.Click("Add"); err != nil {
		t.Fatalf("click Add: %v", err)
	}
	first.ui.Frame()
	first.drain()
	if !first.app.session.HasRepo() || first.app.session.Meta().Kind != store.SSH {
		t.Fatalf("first run did not open the SSH repository: %+v", first.app.session.Meta())
	}
	meta := first.app.session.Meta()

	// Delete the remote directory and restart: the readable notice replaces
	// the raw shell error on both pages.
	srv.RunShell(t, "rm -rf -- "+git.ShellQuote(meta.Path))
	second := newHarnessOnStore(t, storePath)
	second.app.openStartupRepo()
	second.drain()
	if err := second.app.session.LoadErr(); err == nil {
		t.Fatal("restart into the deleted remote repository loaded without error")
	}
	assertMissingRepoNotice(t, second, meta.Name, meta.Location())
	if !second.ui.HasText(fmt.Sprintf("could not be found on %s", meta.Host)) {
		t.Fatalf("explanation naming host %q missing; texts=%q", meta.Host, second.ui.Texts())
	}
	mustClick(t, second, "History")
	second.ui.Frame()
	assertMissingRepoNotice(t, second, meta.Name, meta.Location())

	// Place a sentinel file at the remote path itself — the plain-file
	// replacement case — then Remove from List through the real button.
	srv.RunShell(t, "printf 'remote sentinel\\n' > "+git.ShellQuote(meta.Path))
	mustClick(t, second, "Remove from List")
	second.drain()

	if repos := second.store.Repos(); len(repos) != 0 {
		t.Fatalf("store not empty after removing the missing SSH repo: %+v", repos)
	}
	if second.app.session.HasRepo() {
		t.Fatalf("session still open after removing the last repo: %+v", second.app.session.Meta())
	}
	if !second.ui.HasText("No Repository Open") {
		t.Fatalf("empty state missing after removing the missing SSH repo; texts=%q", second.ui.Texts())
	}
	// Nothing on the server was touched: the sentinel file at the
	// repository's path and the sibling file both survive byte for byte.
	if got := srv.RunShell(t, "cat -- "+git.ShellQuote(meta.Path)); got != "remote sentinel\n" {
		t.Fatalf("remote sentinel at repo path = %q, want intact", got)
	}
	if got := srv.RunShell(t, "cat -- "+git.ShellQuote(srv.RootDir+"/keep-sibling.txt")); got != "keep me\n" {
		t.Fatalf("remote sibling file = %q, want intact", got)
	}
}

// On a real Linux server (sshlab, dash and bash shells) a deleted remote
// repository directory restores into the readable missing-repository notice
// without the shell's raw cd error, and Remove from List leaves the sentinel
// file at the remote path and the sibling files next to it intact.
func TestConfiguredLinuxMissingRemoteRepo(t *testing.T) {
	t.Setenv("BRANCHLINE_OPEN", "")
	repo, srv := configuredLinuxRepo(t)

	srv.RunShell(t, "printf 'keep me linux\\n' > "+git.ShellQuote(srv.RootDir+"/keep-sibling.txt"))
	t.Cleanup(func() {
		srv.RunShell(t, "rm -f -- "+git.ShellQuote(srv.RootDir+"/keep-sibling.txt"))
	})

	storePath := filepath.Join(t.TempDir(), "repositories.json")

	first := newHarnessOnStore(t, storePath)
	openSSHForm(t, first)
	// The configured Linux host is reached through its ssh config alias, with
	// no explicit identity file: sshlab puts both on ssh's configuration.
	typeSSHForm(t, first, srv.Address(repo.Path), "", "")
	first.ui.Frame()
	if err := first.ui.Click("Add"); err != nil {
		t.Fatalf("click Add: %v", err)
	}
	first.ui.Frame()
	first.drain()
	if !first.app.session.HasRepo() || first.app.session.Meta().Kind != store.SSH {
		t.Fatalf("first run did not open the SSH repository: %+v", first.app.session.Meta())
	}
	meta := first.app.session.Meta()

	srv.RunShell(t, "rm -rf -- "+git.ShellQuote(meta.Path))
	second := newHarnessOnStore(t, storePath)
	second.app.openStartupRepo()
	second.drain()
	if err := second.app.session.LoadErr(); err == nil {
		t.Fatal("restart into the deleted remote repository loaded without error")
	}
	assertMissingRepoNotice(t, second, meta.Name, meta.Location())
	if !second.ui.HasText(fmt.Sprintf("could not be found on %s", meta.Host)) {
		t.Fatalf("explanation naming host %q missing; texts=%q", meta.Host, second.ui.Texts())
	}
	mustClick(t, second, "History")
	second.ui.Frame()
	assertMissingRepoNotice(t, second, meta.Name, meta.Location())

	srv.RunShell(t, "printf 'remote sentinel linux\\n' > "+git.ShellQuote(meta.Path))
	mustClick(t, second, "Remove from List")
	second.drain()

	if repos := second.store.Repos(); len(repos) != 0 {
		t.Fatalf("store not empty after removing the missing SSH repo: %+v", repos)
	}
	if !second.ui.HasText("No Repository Open") {
		t.Fatalf("empty state missing after removing the missing SSH repo; texts=%q", second.ui.Texts())
	}
	if got := srv.RunShell(t, "cat -- "+git.ShellQuote(meta.Path)); got != "remote sentinel linux\n" {
		t.Fatalf("remote sentinel at repo path = %q, want intact", got)
	}
	if got := srv.RunShell(t, "cat -- "+git.ShellQuote(srv.RootDir+"/keep-sibling.txt")); got != "keep me linux\n" {
		t.Fatalf("remote sibling file = %q, want intact", got)
	}
}

// Canceling or dismissing the dialog and reopening it advances the form
// generation, so the answer of a check started before the close never
// publishes into the reopened dialog, and the reopened draft is empty.
func TestRepoAddSSHDismissDiscardsInFlightLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	addr := fmt.Sprintf("ssh://%s@%s:%d%s", srv.User, srv.Host, srv.Port, repo.Path)

	h := newTestApp(t)
	openSSHForm(t, h)
	typeSSHForm(t, h, addr, srv.Identity, "")
	h.ui.Frame()

	// Start a check and dismiss the dialog with Escape while it runs.
	h.queue.PauseWorkers()
	if err := h.ui.Click("Test Connection"); err != nil {
		t.Fatalf("click Test Connection: %v", err)
	}
	h.ui.Frame()
	worker := h.queue.WaitWorker(t)
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	h.queue.ResumeWorkers()
	if sshFormState(h).sshOpen {
		t.Fatal("Escape left the dialog open")
	}

	// Reopen and let the in-flight answer arrive: nothing publishes.
	openSSHForm(t, h)
	if got := sshFormState(h).sshAddress; got != "" {
		t.Fatalf("reopened dialog kept the old address %q", got)
	}
	h.queue.ReleaseWorker(worker)
	h.drain()
	st := sshFormState(h)
	if st.sshResult != "" {
		t.Fatalf("in-flight answer published into reopened dialog: %q", st.sshResult)
	}
	if st.sshBusy {
		t.Fatal("reopened dialog stayed busy")
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("discarded check saved a repository: %+v", h.store.Repos())
	}
	closeDialog(t, h)
}

// An Address ending in a space fails Test Connection before the form even
// constructs the repository's executor: the error names the Address field
// and nothing connects, is saved or opens.
func TestRepoAddSSHRejectsEdgeSpacesTestConnection(t *testing.T) {
	h := newTestApp(t)
	constructed := false
	h.app.openRepoFn = func(meta store.Repo) *git.Repo {
		constructed = true
		return git.New(failingExecutor{})
	}

	openSSHForm(t, h)
	typeSSHForm(t, h, "user@server:/srv/app ", "", "")
	h.ui.Frame()
	if err := h.ui.Click("Test Connection"); err != nil {
		t.Fatalf("click Test Connection: %v", err)
	}
	h.ui.Frame()
	h.drain()

	st := sshFormState(h)
	if !st.sshOpen {
		t.Fatal("dialog closed on an address ending in a space")
	}
	if st.sshBusy {
		t.Fatal("dialog stayed busy after the validation error")
	}
	if !st.sshFailed {
		t.Fatal("the refused address did not report a failure")
	}
	if got, want := st.sshResult, "Address cannot begin or end with a space."; got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
	if constructed {
		t.Fatal("the form built a repository executor for the refused address")
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("store gained an entry: %+v", h.store.Repos())
	}
	if h.app.session.HasRepo() {
		t.Fatal("a repository opened when the check was refused")
	}
	closeDialog(t, h)
}

// An Address ending in a space fails Add the same way as Test Connection:
// the repository is never constructed, saved or opened and the dialog
// stays open with the error.
func TestRepoAddSSHRejectsEdgeSpacesAdd(t *testing.T) {
	h := newTestApp(t)
	constructed := false
	h.app.openRepoFn = func(meta store.Repo) *git.Repo {
		constructed = true
		return git.New(failingExecutor{})
	}

	openSSHForm(t, h)
	typeSSHForm(t, h, "user@server:/srv/app ", "", "")
	h.ui.Frame()
	if err := h.ui.Click("Add"); err != nil {
		t.Fatalf("click Add: %v", err)
	}
	h.ui.Frame()
	h.drain()

	st := sshFormState(h)
	if !st.sshOpen {
		t.Fatal("dialog closed on an address ending in a space")
	}
	if st.sshBusy {
		t.Fatal("dialog stayed busy after the validation error")
	}
	if !st.sshFailed {
		t.Fatal("the refused address did not report a failure")
	}
	if got, want := st.sshResult, "Address cannot begin or end with a space."; got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
	if constructed {
		t.Fatal("the form built a repository executor for the refused address")
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("store gained an entry: %+v", h.store.Repos())
	}
	if h.app.session.HasRepo() {
		t.Fatal("a repository opened when the add was refused")
	}
	closeDialog(t, h)
}

// Appending a trailing space to Address while an Add check is in flight
// invalidates that request instead of matching it through TrimSpace, and
// appending a space after a successful Test Connection clears the stale
// result.
func TestRepoAddSSHRejectsEdgeSpacesEditInvalidatesInFlight(t *testing.T) {
	local := newGitRepo(t, "ssh-edge-edit")
	local.CommitFile("a.txt", "a\n", "init")

	h := newTestApp(t)
	h.app.openRepoFn = func(meta store.Repo) *git.Repo {
		return git.New(&git.LocalExecutor{Dir: local.Path()})
	}

	const addr = "user@server:/srv/app"
	openSSHForm(t, h)
	typeSSHForm(t, h, addr, "", "")
	h.ui.Frame()

	// Start an Add check, then append a trailing space before it finishes:
	// the stale completion must be dropped without saving or opening.
	h.queue.PauseWorkers()
	if err := h.ui.Click("Add"); err != nil {
		t.Fatalf("click Add: %v", err)
	}
	h.ui.Frame()
	worker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	focusField(t, h, "Address")
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.ui.Frame()
	h.ui.Type(addr + " ")
	h.ui.Frame()
	h.queue.ReleaseWorker(worker)
	h.drain()

	st := sshFormState(h)
	if !st.sshOpen {
		t.Fatal("dialog closed after trailing space invalidated in-flight Add")
	}
	if st.sshBusy {
		t.Fatal("dialog stayed busy after trailing space invalidated in-flight Add")
	}
	if st.sshResult != "" {
		t.Fatalf("stale Add result published after adding trailing space: %q", st.sshResult)
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("store saved a repository despite trailing-space edit: %+v", h.store.Repos())
	}
	if h.app.session.HasRepo() {
		t.Fatal("repository opened despite trailing-space edit")
	}

	// Now run a clean Test Connection, confirm it succeeds, then append a
	// trailing space: the green Connected banner must clear rather than
	// claiming the space-suffixed address succeeded.
	focusField(t, h, "Address")
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.ui.Frame()
	h.ui.Type(addr)
	h.ui.Frame()
	if err := h.ui.Click("Test Connection"); err != nil {
		t.Fatalf("click Test Connection: %v", err)
	}
	h.ui.Frame()
	h.drain()

	st = sshFormState(h)
	if st.sshFailed || !strings.HasPrefix(st.sshResult, "Connected:") {
		t.Fatalf("clean Test Connection result = %q (failed=%v), want Connected", st.sshResult, st.sshFailed)
	}

	focusField(t, h, "Address")
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.ui.Frame()
	h.ui.Type(addr + " ")
	h.ui.Frame()

	st = sshFormState(h)
	if st.sshResult != "" {
		t.Fatalf("Connected banner survived trailing-space edit: %q", st.sshResult)
	}
	closeDialog(t, h)
}
