package app

import (
	"os"
	"testing"

	"github.com/zhdsmy/branchline/internal/sshtest"
	"github.com/zhdsmy/branchline/internal/store"
)

// loopbackSSHRepo starts a disposable loopback sshd via sshtest.StartLoopback,
// creates a remote Git repository with an initial commit, and returns the
// corresponding store.Repo and *sshtest.Target.
//
// Because store.Repo has no SSH program field and the default SSHExecutor
// resolves "ssh" via exec.LookPath, this helper prepends srv.BinDir() to PATH
// using t.Setenv. Tests that call loopbackSSHRepo MUST NOT call t.Parallel().
//
// Call loopbackSSHRepo(t) before newTestApp(t) so t.Cleanup runs in LIFO order
// (draining background app workers before tearing down the loopback sshd).
func loopbackSSHRepo(t *testing.T) (store.Repo, *sshtest.Target) {
	t.Helper()
	srv := sshtest.StartLoopback(t)
	remoteDir := srv.InitRepo(t, "repo")
	repo, err := store.ParseAddress(srv.Address(remoteDir))
	if err != nil {
		t.Fatalf("parse loopback SSH address %q: %v", srv.Address(remoteDir), err)
	}
	repo.Name = store.DefaultName(repo.Path)
	repo.Identity = srv.Identity
	if binDir := srv.BinDir(); binDir != "" {
		if pathEnv := os.Getenv("PATH"); pathEnv != "" {
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+pathEnv)
		} else {
			t.Setenv("PATH", binDir)
		}
	}
	return repo, srv
}

// configuredLinuxRepo binds to the configured Linux SSH environment (sshlab)
// via sshtest.ConfiguredLinux, creates a disposable remote Git repository with
// an initial commit, and returns the corresponding store.Repo and *sshtest.Target.
//
// Unlike loopbackSSHRepo, this helper does not modify PATH because sshlab
// already places its isolated ssh wrapper on PATH.
func configuredLinuxRepo(t *testing.T) (store.Repo, *sshtest.Target) {
	t.Helper()
	srv := sshtest.ConfiguredLinux(t)
	remoteDir := srv.InitRepo(t, "repo")
	repo, err := store.ParseAddress(srv.Address(remoteDir))
	if err != nil {
		t.Fatalf("parse configured Linux SSH address %q: %v", srv.Address(remoteDir), err)
	}
	repo.Name = store.DefaultName(repo.Path)
	return repo, srv
}

func verifyAppOpensSSHRepo(t *testing.T, repo store.Repo, srv *sshtest.Target) {
	t.Helper()
	wantHead := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")
	if wantHead == "" {
		t.Fatal("expected non-empty HEAD on remote SSH repo")
	}

	h := newTestApp(t, repo)
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click(repo.Name); err != nil {
		t.Fatalf("select SSH repo %q in Repository Switcher: %v", repo.Name, err)
	}
	h.ui.Frame()
	h.drain()

	if !h.app.session.HasRepo() {
		t.Fatal("session has no open repository after selecting SSH repo")
	}
	if got := h.app.session.Meta().Kind; got != store.SSH {
		t.Fatalf("session repo kind = %q, want %q", got, store.SSH)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	if !h.app.session.Loaded() {
		t.Fatal("expected SSH session snapshot to be marked loaded")
	}
	snap := h.app.session.Snapshot()
	if len(snap.Commits) == 0 {
		t.Fatalf("loaded SSH snapshot has 0 commits, want > 0 (status=%+v)", snap.Status)
	}
	if snap.Status.Head != wantHead {
		t.Fatalf("loaded SSH HEAD = %q, want %q", snap.Status.Head, wantHead)
	}
	if snap.Commits[0].Subject != "initial commit" {
		t.Fatalf("first SSH commit subject = %q, want %q", snap.Commits[0].Subject, "initial commit")
	}
	if _, ok := h.ui.Find("main"); !ok {
		t.Fatalf("expected branch main visible in sidebar after loading SSH repo; texts=%q", h.ui.Texts())
	}
}

// Opening a loopback SSH repository through the Repository Switcher loads its
// commits and refs from the disposable loopback sshd server.
func TestLoopbackSSHRepoOpens(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	verifyAppOpensSSHRepo(t, repo, srv)
}

// Opening a configured Linux SSH repository (sshlab) through the Repository
// Switcher loads its commits and refs from the real Linux SSH host.
func TestConfiguredLinuxAppOpensRepo(t *testing.T) {
	repo, srv := configuredLinuxRepo(t)
	verifyAppOpensSSHRepo(t, repo, srv)
}
