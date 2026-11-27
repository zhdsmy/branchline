package app

import (
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/sshtest"
)

// Cloning onto a configured Linux SSH container (sshlab): the Clone dialog's
// SSH Server form clones a repository of the server into another folder of
// the server, which then opens as an SSH repository whose History shows the
// clone's commit.
func TestConfiguredLinuxCloneToServer(t *testing.T) {
	srv := sshtest.ConfiguredLinux(t)
	src := srv.InitRepo(t, "clone-linux-src")
	wantHead := srv.RunGit(t, src, "rev-parse", "HEAD")
	parent := srv.TempDir(t, "clone-linux-parent")

	h := newTestApp(t)
	closeSync(t, h)
	openCloneDialogAtSSH(t, h)

	if !h.ui.HasText("The server runs git clone, so this URL must be reachable from the server.") {
		t.Fatalf("server URL note missing; texts=%q", h.ui.Texts())
	}
	cloneSSHInto(t, h, src, srv.Address(parent), "", "linuxclone")

	st := h.app.cloneUI()
	if st.open || st.err != "" {
		t.Fatalf("clone failed or dialog stayed open: err %q", st.err)
	}
	target := parent + "/linuxclone"
	if !remotePathExists(t, srv, parent, "linuxclone/a.txt") {
		t.Fatalf("the server has no clone at %s", target)
	}
	if got := srv.RunGit(t, target, "rev-parse", "HEAD"); got != wantHead {
		t.Fatalf("clone HEAD = %q, want %q", got, wantHead)
	}
	if got := string(srv.ReadFile(t, target, "a.txt")); got != "one\n" {
		t.Fatalf("clone file content = %q, want one\\n", got)
	}

	if !h.app.session.HasRepo() {
		t.Fatal("no repository open after cloning onto the server")
	}
	meta := h.app.session.Meta()
	if meta.Path != target {
		t.Fatalf("session path = %q, want the server's absolute root %q", meta.Path, target)
	}
	if strings.HasPrefix(meta.Path, "~") || !strings.HasPrefix(meta.Path, "/") {
		t.Fatalf("stored path %q is not absolute", meta.Path)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("snapshot of the server clone failed to load: %v", err)
	}
	if got := h.app.session.Snapshot().Status.Head; got != wantHead {
		t.Fatalf("session HEAD = %q, want %q", got, wantHead)
	}
	repos := h.store.Repos()
	if len(repos) != 1 || repos[0].Path != target {
		t.Fatalf("store repos = %+v, want one entry at %q", repos, target)
	}

	mustClick(t, h, "History")
	h.ui.Frame()
	if !hasExactText(h.ui, "initial commit") {
		t.Fatalf("History missing the clone's commit; texts=%q", h.ui.Texts())
	}
}
