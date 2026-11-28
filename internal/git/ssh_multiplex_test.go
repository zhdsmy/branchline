package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startMuxSSHD runs an sshd like startSSHD, but at LogLevel VERBOSE with
// the log in a file, so a test can count the authenticated sessions: one
// "Accepted publickey" line is one handshake.
func startMuxSSHD(t *testing.T) (*sshServer, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "sshd.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	must(t, err)
	t.Cleanup(func() { _ = logFile.Close() })
	return startWatchSSHD(t, "VERBOSE", logFile), logPath
}

// muxExecutor opens dir on the server through the ssh program prog. At
// cleanup it closes the shared connection and waits until its control
// socket is gone, so a test never leaves a socket in /tmp behind.
func muxExecutor(t *testing.T, srv *sshServer, prog, dir string) *SSHExecutor {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	ex := &SSHExecutor{Host: "127.0.0.1", Port: srv.port, User: u.Username, Identity: srv.identity, Dir: dir, SSH: prog, NoGlobalConfig: true}
	t.Cleanup(func() {
		closeSSHControl(prog, ex.controlPath(), "127.0.0.1")
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, err := os.Stat(ex.controlPath())
			if errors.Is(err, os.ErrNotExist) {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("control socket %s left behind", ex.controlPath())
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	return ex
}

// muxControl runs ssh -O <action> against the executor's shared connection
// and returns what it printed.
func muxControl(t *testing.T, ex *SSHExecutor, action string) (string, error) {
	t.Helper()
	prog := ex.SSH
	if prog == "" {
		prog = "ssh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, prog, "-O", action, "-o", "ControlPath="+ex.ControlPath(), ex.Host)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// countMuxAuths counts the authenticated sessions in the server's log.
func countMuxAuths(t *testing.T, logPath string) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	must(t, err)
	return strings.Count(string(data), "Accepted publickey")
}

// fatalMuxAuths fails with the handshake count and the server's log.
func fatalMuxAuths(t *testing.T, logPath string, got, want int) {
	t.Helper()
	data, _ := os.ReadFile(logPath)
	t.Fatalf("handshakes %d, want %d:\n%s", got, want, data)
}

// waitMuxMasterGone waits until ssh -O check says the shared connection
// has no master, or fails after the timeout. Polling -O check would itself
// reach the master: each control connection resets the persist timer of a
// ControlPersist master, so it would never expire. The wait therefore
// watches the socket file, which the master removes when it exits, and
// asks -O check only once, at the end.
func waitMuxMasterGone(t *testing.T, ex *SSHExecutor, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		_, err := os.Stat(ex.ControlPath())
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("control socket still there after %s", timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
	out, err := muxControl(t, ex, "check")
	if err == nil {
		t.Fatalf("ssh -O check still finds a master: %s", out)
	}
}

var muxMasterPIDRe = regexp.MustCompile(`pid=(\d+)`)

// muxRunSteps runs the first n of twenty git commands, reads and writes,
// through repo.Output, so that every step is exactly one command over ssh.
// A step with a file writes it on this machine first: the executor opens
// the same directory on the loopback server.
func muxRunSteps(t *testing.T, repo *Repo, dir string, n int) {
	t.Helper()
	steps := []struct {
		file    string
		content string
		args    []string
	}{
		{file: "mux1.txt", content: "one\n", args: []string{"add", "mux1.txt"}},
		{args: []string{"commit", "-q", "-m", "mux one"}},
		{args: []string{"status", "--porcelain"}},
		{args: []string{"rev-parse", "HEAD"}},
		{args: []string{"log", "-1", "--format=%s"}},
		{args: []string{"branch", "--show-current"}},
		{file: "mux2.txt", content: "two\n", args: []string{"add", "mux2.txt"}},
		{args: []string{"commit", "-q", "-m", "mux two"}},
		{args: []string{"status", "--porcelain"}},
		{args: []string{"branch", "mux/a"}},
		{args: []string{"rev-parse", "--verify", "refs/heads/mux/a"}},
		{args: []string{"checkout", "-q", "mux/a"}},
		{file: "mux3.txt", content: "three\n", args: []string{"add", "mux3.txt"}},
		{args: []string{"commit", "-q", "-m", "mux three"}},
		{args: []string{"log", "--oneline", "-3"}},
		{args: []string{"rev-parse", "main"}},
		{args: []string{"branch", "--list"}},
		{file: "mux1.txt", content: "one two\n", args: []string{"add", "mux1.txt"}},
		{args: []string{"commit", "-q", "-m", "mux four"}},
		{args: []string{"diff", "HEAD~1", "HEAD", "--stat"}},
	}
	if n > len(steps) {
		t.Fatalf("only %d steps, asked for %d", len(steps), n)
	}
	for i, step := range steps[:n] {
		if step.file != "" {
			must(t, os.WriteFile(filepath.Join(dir, step.file), []byte(step.content), 0o644))
		}
		if _, err := repo.Output(step.args...); err != nil {
			t.Fatalf("command %d (git %v): %v", i+1, step.args, err)
		}
	}
}

// TestSSHMultiplex proves the commands of one SSHExecutor share a single
// SSH connection: twenty commands cost one handshake, and a connection
// whose master was closed, killed or expired is replaced transparently by
// the next command.
func TestSSHMultiplex(t *testing.T) {
	// TwentyOperationsOneHandshake: twenty reads and writes through one
	// executor all succeed over a single authenticated session, and the
	// master of that session is still running at the end.
	t.Run("TwentyOperationsOneHandshake", func(t *testing.T) {
		srv, logPath := startMuxSSHD(t)
		dir := testRepo(t)
		ex := muxExecutor(t, srv, srv.ssh, dir)
		repo := New(ex)

		muxRunSteps(t, repo, dir, 20)

		if got := countMuxAuths(t, logPath); got != 1 {
			fatalMuxAuths(t, logPath, got, 1)
		}
		out, err := muxControl(t, ex, "check")
		if err != nil {
			t.Fatalf("ssh -O check after 20 commands: %v %s", err, out)
		}
	})

	// NoMultiplexHandshakesEveryTime is the control experiment: with
	// NoMultiplex each of the five commands authenticates on its own, so
	// the counting above can tell one handshake from many.
	t.Run("NoMultiplexHandshakesEveryTime", func(t *testing.T) {
		srv, logPath := startMuxSSHD(t)
		dir := testRepo(t)
		ex := muxExecutor(t, srv, srv.ssh, dir)
		ex.NoMultiplex = true
		repo := New(ex)

		muxRunSteps(t, repo, dir, 5)

		if got := countMuxAuths(t, logPath); got != 5 {
			fatalMuxAuths(t, logPath, got, 5)
		}
	})

	// ClosedMasterReconnects: after ssh -O exit closes the master, the
	// next command succeeds by opening a new connection, which costs one
	// more handshake and leaves a live socket again.
	t.Run("ClosedMasterReconnects", func(t *testing.T) {
		srv, logPath := startMuxSSHD(t)
		dir := testRepo(t)
		ex := muxExecutor(t, srv, srv.ssh, dir)
		repo := New(ex)

		for i := 0; i < 3; i++ {
			if _, err := repo.Output("status", "--porcelain"); err != nil {
				t.Fatal(err)
			}
		}
		before := countMuxAuths(t, logPath)
		if _, err := os.Stat(ex.ControlPath()); err != nil {
			t.Fatalf("no control socket before close: %v", err)
		}
		out, err := muxControl(t, ex, "exit")
		if err != nil {
			t.Fatalf("ssh -O exit: %v %s", err, out)
		}
		waitMuxMasterGone(t, ex, 5*time.Second)

		if _, err := repo.Output("status", "--porcelain"); err != nil {
			t.Fatalf("command after closing the master: %v", err)
		}
		if got := countMuxAuths(t, logPath); got != before+1 {
			fatalMuxAuths(t, logPath, got, before+1)
		}
		if _, err := os.Stat(ex.ControlPath()); err != nil {
			t.Fatalf("no control socket after reconnect: %v", err)
		}
	})

	// StaleSocketReconnects: the master is killed with SIGKILL, which
	// leaves its socket file behind. The next command must remove the
	// stale socket, connect anew and succeed, at one more handshake.
	t.Run("StaleSocketReconnects", func(t *testing.T) {
		srv, logPath := startMuxSSHD(t)
		dir := testRepo(t)
		ex := muxExecutor(t, srv, srv.ssh, dir)
		repo := New(ex)

		for i := 0; i < 3; i++ {
			if _, err := repo.Output("status", "--porcelain"); err != nil {
				t.Fatal(err)
			}
		}
		before := countMuxAuths(t, logPath)
		out, err := muxControl(t, ex, "check")
		if err != nil {
			t.Fatalf("ssh -O check: %v %s", err, out)
		}
		m := muxMasterPIDRe.FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("no pid in ssh -O check output: %q", out)
		}
		pid, err := strconv.Atoi(m[1])
		must(t, err)
		psOut, err := exec.Command("ps", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		if err != nil || !strings.Contains(string(psOut), ex.ControlPath()) {
			t.Fatalf("pid %d command %q lacks this test's control path %s; refusing to kill it", pid, psOut, ex.ControlPath())
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatalf("kill master pid %d: %v", pid, err)
		}
		if _, err := os.Stat(ex.ControlPath()); err != nil {
			t.Fatalf("control socket missing after SIGKILL: %v", err)
		}

		if _, err := repo.Output("status", "--porcelain"); err != nil {
			t.Fatalf("command over a stale socket: %v", err)
		}
		if got := countMuxAuths(t, logPath); got != before+1 {
			fatalMuxAuths(t, logPath, got, before+1)
		}
	})

	// PersistExpiresAndReconnects: a wrapper replaces ControlPersist=600
	// with ControlPersist=2, so the master goes away two seconds after the
	// last command. The next command reconnects at one more handshake. The
	// wrapper records having seen ControlPersist=600, so a change to the
	// executor's arguments cannot pass silently.
	t.Run("PersistExpiresAndReconnects", func(t *testing.T) {
		srv, logPath := startMuxSSHD(t)
		dir := testRepo(t)
		scripts := t.TempDir()
		trace := filepath.Join(scripts, "persist-trace")
		prog := filepath.Join(scripts, "ssh-persist")
		script := `#!/bin/bash
args=()
saw=0
for a in "$@"; do
  if [ "$a" = "ControlPersist=600" ]; then
    saw=1
    args+=("ControlPersist=2")
  else
    args+=("$a")
  fi
done
if [ "$saw" = 1 ]; then
  printf 'saw ControlPersist=600\n' >> ` + ShellQuote(trace) + `
fi
exec /usr/bin/ssh -F /dev/null -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR "${args[@]}"
`
		must(t, os.WriteFile(prog, []byte(script), 0o755))

		ex := muxExecutor(t, srv, prog, dir)
		repo := New(ex)
		if _, err := repo.Output("status", "--porcelain"); err != nil {
			t.Fatal(err)
		}
		before := countMuxAuths(t, logPath)
		waitMuxMasterGone(t, ex, 15*time.Second)

		if _, err := repo.Output("status", "--porcelain"); err != nil {
			t.Fatalf("command after the persist timeout: %v", err)
		}
		if got := countMuxAuths(t, logPath); got != before+1 {
			fatalMuxAuths(t, logPath, got, before+1)
		}
		recorded, err := os.ReadFile(trace)
		if err != nil || !strings.Contains(string(recorded), "saw ControlPersist=600") {
			t.Fatalf("wrapper never saw ControlPersist=600 (trace %q, err %v)", recorded, err)
		}
	})
}
