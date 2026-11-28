package sshdwatch

import (
	"errors"
	"io"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestSSHDWatchStopRemovesProcessGroup checks that Stop ends the watchdog,
// the supervised child and every process of their shared process group.
func TestSSHDWatchStopRemovesProcessGroup(t *testing.T) {
	proc, childPID := startSleepWatch(t)
	proc.Stop()

	waitPIDGone(t, "supervised child", childPID, 5*time.Second)
	waitPIDGone(t, "watchdog", proc.PID(), 5*time.Second)
	waitGroupGone(t, proc.PID(), 5*time.Second)
}

// TestSSHDWatchStopTwice checks that a repeated Stop neither panics nor hangs
// and still leaves nothing of the process group behind.
func TestSSHDWatchStopTwice(t *testing.T) {
	proc, _ := startSleepWatch(t)
	proc.Stop()
	proc.Stop()

	waitPIDGone(t, "watchdog", proc.PID(), 5*time.Second)
	waitGroupGone(t, proc.PID(), 5*time.Second)
}

// TestSSHDWatchChildExitFirst checks that when the supervised child exits on
// its own, the watchdog exits too: Exited closes and no process of the group
// is left behind.
func TestSSHDWatchChildExitFirst(t *testing.T) {
	dir := t.TempDir()
	proc, err := Start(dir, filepath.Join(dir, "child.pid"), io.Discard, "sh", "-c", "exit 0")
	if err != nil {
		t.Fatalf("start watchdog: %v", err)
	}
	defer proc.Stop()

	select {
	case <-proc.Exited():
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog did not exit within 5s of its child exiting")
	}

	waitPIDGone(t, "watchdog", proc.PID(), 5*time.Second)
	waitGroupGone(t, proc.PID(), 5*time.Second)
}

// startSleepWatch starts a watchdog supervising a long sleep and returns the
// watchdog Proc and the child pid the watchdog reported.
func startSleepWatch(t *testing.T) (*Proc, int) {
	t.Helper()
	dir := t.TempDir()
	proc, err := Start(dir, filepath.Join(dir, "child.pid"), io.Discard, "sleep", "300")
	if err != nil {
		t.Fatalf("start watchdog: %v", err)
	}
	childPID, err := proc.SSHDPID()
	if err != nil {
		proc.Stop()
		t.Fatalf("read child pid: %v", err)
	}
	return proc, childPID
}

// waitPIDGone polls until signalling pid with signal 0 reports ESRCH (the
// process is gone) or the timeout expires.
func waitPIDGone(t *testing.T, what string, pid int, timeout time.Duration) {
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
// catching helper processes that outlived the watchdog itself.
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
