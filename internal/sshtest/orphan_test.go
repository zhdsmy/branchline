package sshtest

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// orphanVictimEnv marks a child copy of this test binary as the victim of the
// orphan tests: it starts a loopback sshd, prints the sshd and watchdog pids,
// and then blocks until the parent test kills it.
const orphanVictimEnv = "BRANCHLINE_SSHTEST_ORPHAN_VICTIM"

// TestLoopbackOrphanVictimHelper is not a test: it runs only as a child
// process of the orphan tests (re-executed test binary with the victim
// environment variable set) and provides the test process that gets killed.
// In a direct run it skips immediately.
func TestLoopbackOrphanVictimHelper(t *testing.T) {
	if os.Getenv(orphanVictimEnv) != "1" {
		t.Skip("helper process for the orphan tests, not a standalone test")
	}
	srv := StartLoopback(t)
	fmt.Printf("ORPHAN_PIDS sshd=%d watchdog=%d\n", srv.PID, srv.cmd.Process.Pid)
	for {
		time.Sleep(time.Hour)
	}
}

// TestLoopbackOrphanSIGKILLedTestProcess verifies that when the test process
// is killed with SIGKILL (so its cleanups never run), the loopback sshd it
// started and the supervising watchdog process both exit within 5 seconds.
func TestLoopbackOrphanSIGKILLedTestProcess(t *testing.T) {
	requireLoopbackSSHD(t)
	victim, sshdPID, watchdogPID := startOrphanVictim(t)
	defer func() {
		_ = victim.Process.Kill()
		_ = victim.Wait()
	}()

	if err := victim.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL victim pid %d: %v", victim.Process.Pid, err)
	}
	_ = victim.Wait()

	waitProcessGone(t, "sshd", sshdPID, 5*time.Second)
	waitProcessGone(t, "watchdog", watchdogPID, 5*time.Second)
	waitGroupGone(t, watchdogPID, 5*time.Second)
}

// TestLoopbackOrphanSSHDExitsFirst verifies that when sshd exits while the
// test process is still alive, the watchdog process exits too and leaves no
// process of its group behind.
func TestLoopbackOrphanSSHDExitsFirst(t *testing.T) {
	requireLoopbackSSHD(t)
	victim, sshdPID, watchdogPID := startOrphanVictim(t)
	defer func() {
		_ = victim.Process.Kill()
		_ = victim.Wait()
	}()

	if err := syscall.Kill(sshdPID, syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL sshd pid %d: %v", sshdPID, err)
	}

	waitProcessGone(t, "watchdog", watchdogPID, 5*time.Second)
	waitGroupGone(t, watchdogPID, 5*time.Second)
}

// requireLoopbackSSHD skips (or fails under BRANCHLINE_REQUIRE_SSH=1) when no
// sshd binary is available, mirroring StartLoopback's policy so the victim
// helper can always start its server.
func requireLoopbackSSHD(t *testing.T) {
	t.Helper()
	if _, err := defaultFindSSHD(); err != nil {
		if isEnvTrue(EnvRequireSSH) {
			t.Fatalf("%s=1: cannot start loopback sshd: %v", EnvRequireSSH, err)
		}
		t.Skipf("cannot start loopback sshd: %v", err)
	}
}

// startOrphanVictim re-executes this test binary as a child running only the
// victim helper, waits for it to report the sshd and watchdog pids on stdout,
// and returns the child command and both pids. Only the child process itself
// and the two pids it reported are ever signalled by these tests.
func startOrphanVictim(t *testing.T) (*exec.Cmd, int, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^TestLoopbackOrphanVictimHelper$")
	cmd.Env = append(os.Environ(), orphanVictimEnv+"=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("victim stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start victim process: %v", err)
	}

	type scanResult struct {
		sshdPID     int
		watchdogPID int
		err         error
	}
	resultCh := make(chan scanResult, 1)
	go func() {
		var res scanResult
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "ORPHAN_PIDS ") {
				continue
			}
			var sshdPID, watchdogPID int
			if _, err := fmt.Sscanf(line, "ORPHAN_PIDS sshd=%d watchdog=%d", &sshdPID, &watchdogPID); err != nil {
				res.err = fmt.Errorf("malformed pid line %q: %v", line, err)
				break
			}
			res.sshdPID = sshdPID
			res.watchdogPID = watchdogPID
			break
		}
		if res.err == nil && res.sshdPID == 0 {
			if err := scanner.Err(); err != nil {
				res.err = fmt.Errorf("scan victim output: %v", err)
			} else {
				res.err = errors.New("victim exited without reporting pids")
			}
		}
		resultCh <- res
	}()

	var res scanResult
	select {
	case res = <-resultCh:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("victim did not report pids within 30s")
	}
	if res.err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("victim pid report: %v", res.err)
	}
	if res.sshdPID <= 0 || res.watchdogPID <= 0 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("victim reported invalid pids sshd=%d watchdog=%d", res.sshdPID, res.watchdogPID)
	}
	return cmd, res.sshdPID, res.watchdogPID
}

// waitProcessGone polls until signalling pid with signal 0 reports ESRCH
// (the process is gone) or the timeout expires.
func waitProcessGone(t *testing.T, what string, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s pid %d still alive after %s (kill err=%v)", what, pid, timeout, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// waitGroupGone polls until no process remains in the process group pgid,
// catching watchdog helper processes that outlived the watchdog itself.
func waitGroupGone(t *testing.T, pgid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still has live processes after %s (kill err=%v)", pgid, timeout, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
