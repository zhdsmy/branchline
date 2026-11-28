package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// rkProbe is a shell alias whose process group stands in for any command
// the server runs: a sleep git waits on, with a token in the command line
// so the test can find the group in the process table of this machine,
// which a loopback server shares.
func rkProbe(token string) []string {
	return []string{"-c", "alias.zz=!sleep 30 # " + token, "zz"}
}

// rkServerEntries is the process table lines of the server side: they carry
// the token, and none of the ssh client options only the local command
// lines — client, wrapper and master alike — carry.
func rkServerEntries(t *testing.T, token string) [][3]string {
	t.Helper()
	var out [][3]string
	for _, e := range psEntries(t) {
		if strings.Contains(e[2], token) && !strings.Contains(e[2], "ControlPath") {
			out = append(out, e)
		}
	}
	return out
}

// rkWaitGroupLeader passes once the server-side session leader of the probe
// is in the process table, failing after five seconds. The leader is the
// process whose id is its group, which keeps the helper processes of the
// local side out.
func rkWaitGroupLeader(t *testing.T, token string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, e := range rkServerEntries(t, token) {
			if e[0] == e[1] {
				if pg, err := strconv.Atoi(e[1]); err == nil && pg > 0 {
					return pg
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no server-side session leader with %q in its command line", token)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// rkWaitServerGone passes once no server-side process of the probe is left,
// failing when the deadline passes.
func rkWaitServerGone(t *testing.T, token string, deadline time.Time, what string) {
	t.Helper()
	for {
		if len(rkServerEntries(t, token)) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: server probe processes linger past the deadline: %v", what, rkServerEntries(t, token))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// rkDelayedSSH writes an ssh wrapper that defers everything the real ssh
// writes on standard error by the given number of seconds: the process-group
// marker the server's shell writes first reaches the app that much later
// than the command's own start on the server, which is the ordering a fast
// cancellation meets on a loaded machine. The real stderr still arrives in
// order — it is spooled in a file and replayed — and the replayer is ended
// once ssh exits so nothing is left behind.
func rkDelayedSSH(t *testing.T, innerSSH string, seconds string) string {
	t.Helper()
	dir := t.TempDir()
	logDir := filepath.Join(dir, "spool")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"_f=$(mktemp " + ShellQuote(filepath.Join(logDir, "err.XXXXXXXX")) + ") || exit 1\n" +
		"( sleep " + seconds + "; exec tail -f -n +1 \"$_f\" 1>&2 ) &\n" +
		"_tp=$!\n" +
		"trap 'kill \"$_tp\" 2>/dev/null; rm -f \"$_f\"' EXIT\n" +
		ShellQuote(innerSSH) + " \"$@\" 2>\"$_f\"\n"
	wrapper := filepath.Join(dir, "ssh")
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatalf("write delaying ssh wrapper: %v", err)
	}
	return wrapper
}

// rkFailFirstKill writes an ssh wrapper that fails the first process-group
// kill it is asked to run with the exit status a lost connection produces,
// and passes everything else — including the retries — to the real ssh.
// Losing one kill round trip is what a busy or flapping connection does to
// the single attempt canceling used to make.
func rkFailFirstKill(t *testing.T, innerSSH string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	flag := filepath.Join(dir, "first-kill-failed")
	script := "#!/bin/sh\n" +
		"_last=\n" +
		"for _a in \"$@\"; do _last=$_a; done\n" +
		"case \"$_last\" in\n" +
		"  *\"kill -s TERM\"*)\n" +
		"    if [ ! -e " + ShellQuote(flag) + " ]; then\n" +
		"      : > " + ShellQuote(flag) + "\n" +
		"      echo 'ssh: connection to server lost' >&2\n" +
		"      exit 255\n" +
		"    fi ;;\n" +
		"esac\n" +
		"exec " + ShellQuote(innerSSH) + " \"$@\"\n"
	wrapper := filepath.Join(dir, "ssh")
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatalf("write kill-failing ssh wrapper: %v", err)
	}
	return wrapper, flag
}

// rkExecutor is a loopback executor for the probe of one test, through the
// given ssh wrapper, with the shared connection already warm.
func rkExecutor(t *testing.T, srv *sshServer, wrapper string) (*SSHExecutor, string) {
	t.Helper()
	dir := t.TempDir()
	ex := srv.executor(t, dir)
	ex.SSH = wrapper
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := ex.RunShell(ctx, "true"); err != nil {
		t.Fatalf("warm the shared connection: %v", err)
	}
	return ex, dir
}

// A command canceled while the server's naming of its process group is
// still on its way must not be left running on the server: the cancellation
// waits for the naming instead of dropping it with the connection, and the
// probe's processes are gone within seconds, not when their own half minute
// runs out.
func TestRemoteKillCancelRacingMarker(t *testing.T) {
	srv := startSSHD(t)
	ex, _ := rkExecutor(t, srv, rkDelayedSSH(t, srv.ssh, "1.5"))

	token := fmt.Sprintf("rkmark%d", time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := ex.Run(ctx, nil, rkProbe(token)...)
		done <- err
	}()

	pg := rkWaitGroupLeader(t, token)
	// The probe runs on the server while the delayed stderr guarantees its
	// process group is not named on this side yet: canceling now is the race
	// a fast supersede loses when the machine is busy.
	cancel()
	cancelAt := time.Now()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the canceled probe command succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the canceled command did not return within ten seconds")
	}
	rkWaitServerGone(t, token, cancelAt.Add(6*time.Second), "canceled racing the marker")
	if !groupAlive(pg) {
		t.Logf("process group %d ended after the cancellation that raced the marker", pg)
	}
}

// A canceled command's process group on the server is ended even when the
// first kill rides a connection that drops it: the stop is tried again
// instead of leaving the server's processes running with nobody to end them.
func TestRemoteKillRetriesLostRoundTrip(t *testing.T) {
	srv := startSSHD(t)
	killer, flag := rkFailFirstKill(t, srv.ssh)
	ex, _ := rkExecutor(t, srv, rkDelayedSSH(t, killer, "1.5"))

	token := fmt.Sprintf("rkretry%d", time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := ex.Run(ctx, nil, rkProbe(token)...)
		done <- err
	}()

	rkWaitGroupLeader(t, token)
	cancel()
	cancelAt := time.Now()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the canceled probe command succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the canceled command did not return within ten seconds")
	}
	deadline := cancelAt.Add(8 * time.Second)
	for {
		if _, err := os.Stat(flag); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first process-group kill was never attempted")
		}
		time.Sleep(25 * time.Millisecond)
	}
	rkWaitServerGone(t, token, deadline, "canceled with the first kill lost")
}
