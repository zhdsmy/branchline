package git

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// orphanVictimEnv marks a child copy of this test binary as the victim of the
// orphan test: it starts an sshd through startSSHD, prints the sshd and
// watchdog pids, and then blocks until the parent test kills it.
const orphanVictimEnv = "BRANCHLINE_GITTEST_ORPHAN_VICTIM"

// TestGitSSHDOrphanVictimHelper is not a test: it runs only as a child
// process of the orphan test (re-executed test binary with the victim
// environment variable set) and provides the test process that gets killed.
// In a direct run it skips immediately.
func TestGitSSHDOrphanVictimHelper(t *testing.T) {
	if os.Getenv(orphanVictimEnv) != "1" {
		t.Skip("helper process for the orphan tests, not a standalone test")
	}
	srv := startSSHD(t)

	// The sshd pid comes from the server's own PidFile rather than anything
	// the launcher keeps in memory, so this helper reports the real sshd no
	// matter how startSSHD starts it. The watchdog shell is the process group
	// leader of the sshd it supervises, so the sshd's process group id is the
	// watchdog pid.
	pidFile := filepath.Join(srv.dir, "pid")
	deadline := time.Now().Add(5 * time.Second)
	var sshdPID int
	for {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			if _, scanErr := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &sshdPID); scanErr == nil && sshdPID > 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no usable sshd pid in %s within 5s", pidFile)
		}
		time.Sleep(25 * time.Millisecond)
	}
	watchdogPID, err := syscall.Getpgid(sshdPID)
	if err != nil {
		t.Fatalf("process group of sshd pid %d: %v", sshdPID, err)
	}
	fmt.Printf("GIT_ORPHAN_PIDS sshd=%d watchdog=%d\n", sshdPID, watchdogPID)
	for {
		time.Sleep(time.Hour)
	}
}

// TestGitSSHDOrphanSIGKILLedTestProcess verifies that when the test process
// is killed with SIGKILL (so its cleanups never run), the sshd it started
// through startSSHD and the supervising watchdog process both exit within 5
// seconds.
func TestGitSSHDOrphanSIGKILLedTestProcess(t *testing.T) {
	if _, err := os.Stat("/usr/sbin/sshd"); err != nil {
		skipOrFailSSH(t, "no sshd on this machine")
	}
	victim, sshdPID, watchdogPID := startGitOrphanVictim(t)
	defer func() {
		_ = victim.Process.Kill()
		_ = victim.Wait()
	}()

	if err := victim.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL victim pid %d: %v", victim.Process.Pid, err)
	}
	_ = victim.Wait()

	waitGitProcessGone(t, "sshd", sshdPID, 5*time.Second)
	waitGitProcessGone(t, "watchdog", watchdogPID, 5*time.Second)
	waitGitGroupGone(t, watchdogPID, 5*time.Second)
}

// startGitOrphanVictim re-executes this test binary as a child running only
// the victim helper, waits for it to report the sshd and watchdog pids on
// stdout, and returns the child command and both pids. Only the child process
// itself and the two pids it reported are ever signalled by this test.
func startGitOrphanVictim(t *testing.T) (*exec.Cmd, int, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^TestGitSSHDOrphanVictimHelper$")
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
			if !strings.HasPrefix(line, "GIT_ORPHAN_PIDS ") {
				continue
			}
			var sshdPID, watchdogPID int
			if _, err := fmt.Sscanf(line, "GIT_ORPHAN_PIDS sshd=%d watchdog=%d", &sshdPID, &watchdogPID); err != nil {
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

// waitGitProcessGone polls until signalling pid with signal 0 reports ESRCH
// (the process is gone) or the timeout expires.
func waitGitProcessGone(t *testing.T, what string, pid int, timeout time.Duration) {
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

// waitGitGroupGone polls until no process remains in the process group pgid,
// catching watchdog helper processes that outlived the watchdog itself.
func waitGitGroupGone(t *testing.T, pgid int, timeout time.Duration) {
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

// skipProbeEnv marks a child copy of this test binary as the probe for the
// skip-or-fail policy test: it calls skipOrFailSSH once and exits.
const skipProbeEnv = "BRANCHLINE_GITTEST_SKIP_PROBE"

// TestSkipOrFailSSHProbeHelper is not a test: it runs only as a child process
// of TestSkipOrFailSSHPolicy. In a direct run it skips immediately.
func TestSkipOrFailSSHProbeHelper(t *testing.T) {
	if os.Getenv(skipProbeEnv) != "1" {
		t.Skip("helper process for the skip-or-fail policy test, not a standalone test")
	}
	skipOrFailSSH(t, "probe")
}

// TestSkipOrFailSSHPolicy runs the probe helper in a copy of this test binary
// and checks the outcome both ways: without BRANCHLINE_REQUIRE_SSH the probe
// skips and the run passes; with BRANCHLINE_REQUIRE_SSH=1 the probe fails and
// the test binary exits non-zero.
func TestSkipOrFailSSHPolicy(t *testing.T) {
	probe := func(requireSSH string) (string, error) {
		cmd := exec.Command(os.Args[0], "-test.v", "-test.run", "^TestSkipOrFailSSHProbeHelper$")
		// Drop any inherited BRANCHLINE_REQUIRE_SSH first: duplicate entries in
		// the environment have no portable precedence, so the probe controls
		// the variable by writing exactly one.
		env := []string{}
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "BRANCHLINE_REQUIRE_SSH=") {
				env = append(env, kv)
			}
		}
		cmd.Env = append(env, skipProbeEnv+"=1", "BRANCHLINE_REQUIRE_SSH="+requireSSH)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := probe("")
	if err != nil {
		t.Fatalf("probe without BRANCHLINE_REQUIRE_SSH should pass, got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "SKIP") {
		t.Fatalf("probe without BRANCHLINE_REQUIRE_SSH should skip, output:\n%s", out)
	}

	out, err = probe("1")
	if err == nil {
		t.Fatalf("probe with BRANCHLINE_REQUIRE_SSH=1 should fail, output:\n%s", out)
	}
	if !strings.Contains(out, "FAIL") {
		t.Fatalf("probe with BRANCHLINE_REQUIRE_SSH=1 should report a failure, output:\n%s", out)
	}
}
