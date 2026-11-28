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

	"github.com/zhdsmy/branchline/internal/git"
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

// Cloning onto a configured Linux SSH container with the per-clone identity
// left blank authenticates every step (EnsureDir, CloneInto, TopLevel and the
// reopened session) with the key checked on the SSH Keys settings page.
func TestConfiguredLinuxCloneToServerUsesActiveSettingsKey(t *testing.T) {
	resetCloneSSHMemory(t)
	srv := sshtest.ConfiguredLinux(t)
	origSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatalf("find sshlab ssh wrapper: %v", err)
	}

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
	checkedKey := makeSSHKeyPair(t, filepath.Join(home, ".ssh"), "clone_checked_key")

	src := srv.InitRepo(t, "clone-key-src")
	wantHead := srv.RunGit(t, src, "rev-parse", "HEAD")
	parent := srv.TempDir(t, "clone-key-parent")

	origAuth := srv.RunShell(t, "cat /root/.ssh/authorized_keys")
	t.Cleanup(func() {
		srv.RunShellInput(t, "cat > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys", []byte(origAuth))
	})
	updatedAuth := strings.TrimRight(origAuth, "\n") + "\n" + checkedKey.Public + "\n"
	srv.RunShellInput(t, "cat > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys", []byte(updatedAuth))

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

	closeMaster := func(keys []string) {
		ex := &git.SSHExecutor{Host: "127.0.0.1", User: remoteUser, Port: port, Dir: parent, SSHKeys: keys}
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
		closeMaster([]string{checkedKey.Path})
	})

	h := newTestApp(t)
	closeSync(t, h)
	if err := h.app.saveGitToolchainSettings("", "parent", git.Trace{}); err != nil {
		t.Fatalf("save toolchain settings: %v", err)
	}

	serverFolder := "ssh://" + remoteUser + "@127.0.0.1:" + strconv.Itoa(port) + parent

	// Without any active key and with the identity field blank, the clone fails authentication.
	openCloneDialogAtSSH(t, h)
	cloneSSHInto(t, h, src, serverFolder, "", "keyclone")
	if st := h.app.cloneUI(); !st.open || !strings.Contains(st.err, "Permission denied") {
		t.Fatalf("clone without active key: open=%v err=%q, want Permission denied", st.open, st.err)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	// Check the authorized key on the SSH Keys settings page, then clone again with identity blank.
	tt := openSSHKeysPage(t, h)
	if err := tt.Click("Use " + checkedKey.Name); err != nil {
		t.Fatalf("check Use %s: %v; texts=%q", checkedKey.Name, err, tt.Texts())
	}
	tt.Frame()
	h.drain()

	openCloneDialogAtSSH(t, h)
	cloneSSHInto(t, h, src, serverFolder, "", "keyclone")
	if st := h.app.cloneUI(); st.open || st.err != "" {
		t.Fatalf("clone with active settings key failed: open=%v err=%q", st.open, st.err)
	}

	target := parent + "/keyclone"
	if got := srv.RunGit(t, target, "rev-parse", "HEAD"); got != wantHead {
		t.Fatalf("clone HEAD = %q, want %q", got, wantHead)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("opened clone failed to load: %v", err)
	}
	if got := h.app.session.Meta().Path; got != target {
		t.Fatalf("opened path = %q, want %q", got, target)
	}
	if got := h.app.session.Snapshot().Status.Head; got != wantHead {
		t.Fatalf("session HEAD = %q, want %q", got, wantHead)
	}
}
