// Package sshdwatch starts test sshd processes under a supervising watchdog
// so they cannot outlive the test process, and never leaves them behind when
// the test process is killed outright. It depends only on the standard
// library so both the SSH test harness and the git package tests can use it.
package sshdwatch

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Script supervises a child process (the sshd command line) so it cannot
// outlive the process that started it. macOS has no PR_SET_PDEATHSIG
// equivalent, so instead the watchdog shell starts the child as its own child
// and blocks in wait, while a background reader watches a pipe whose write
// end is held only by the starting process (passed as fd 3 through
// exec.Cmd.ExtraFiles; the write end never enters any child). When the
// starting process dies without running its cleanups (SIGKILL, terminal
// closed), the pipe reaches EOF; the reader then ignores SIGTERM so
// signalling the whole process group does not kill the reader itself before
// its SIGKILL fallback can run. When the child exits first, wait returns and
// the watchdog kills and reaps the reader, leaving nothing behind. The
// watchdog and the child share one process group, so the normal cleanup path
// terminates all of them together while the pipe is still open.
//
// Arguments: $1 is the pid file the watchdog writes the child pid into, the
// rest is the child command line.
const Script = `#!/bin/sh
pidfile=$1
shift
"$@" 3<&- &
sshd_pid=$!
if ! printf '%s\n' "$sshd_pid" > "$pidfile.tmp" || ! mv -f "$pidfile.tmp" "$pidfile"; then
    kill -KILL "$sshd_pid" 2>/dev/null
    exit 1
fi
(
    while read -r _ignored <&3; do :; done
    trap '' TERM
    kill -TERM "$sshd_pid" 2>/dev/null
    kill -TERM -$$ 2>/dev/null
    i=0
    while [ "$i" -lt 10 ]; do
        kill -0 "$sshd_pid" 2>/dev/null || break
        sleep 0.1
        i=$((i + 1))
    done
    if kill -0 "$sshd_pid" 2>/dev/null; then
        kill -KILL "$sshd_pid" 2>/dev/null
    fi
    kill -KILL -$$ 2>/dev/null
) >/dev/null 2>&1 &
reader_pid=$!
exec 3<&-
wait "$sshd_pid"
kill -KILL "$reader_pid" 2>/dev/null
wait "$reader_pid" 2>/dev/null
exit 0
`

// scriptName is the file name Start writes the watchdog script under.
const scriptName = "sshd-watchdog"

// Proc is a running watchdog process: the Script shell supervising one child
// process in its own process group.
type Proc struct {
	// Cmd is the watchdog process itself, started in a new process group whose
	// group id is the watchdog pid.
	Cmd     *exec.Cmd
	pidFile string
	pipeW   *os.File
	exited  chan struct{}
	stop    sync.Once
}

// Start writes the watchdog script into dir and runs it, with pidFile as the
// file the script writes the supervised child's pid into and argv as the
// child command line. The watchdog's stdout and stderr (which the child
// shares) go to out. The caller owns the returned Proc and ends it with
// Stop; if the caller's process dies first, the watchdog ends the child
// itself and exits.
func Start(dir, pidFile string, out io.Writer, argv ...string) (*Proc, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("sshdwatch: empty child command line")
	}
	scriptPath := filepath.Join(dir, scriptName)
	if err := os.WriteFile(scriptPath, []byte(Script), 0o755); err != nil {
		return nil, fmt.Errorf("sshdwatch: write watchdog script: %w", err)
	}
	// A pid file left from a previous attempt on the same directory would be
	// mistaken for this run's child pid.
	_ = os.Remove(pidFile)

	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("sshdwatch: create watchdog pipe: %w", err)
	}
	args := append([]string{scriptPath, pidFile}, argv...)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.ExtraFiles = []*os.File{pipeR}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = pipeR.Close()
		_ = pipeW.Close()
		return nil, fmt.Errorf("sshdwatch: start watchdog: %w", err)
	}
	// The child holds its own copy of the pipe read end (as fd 3); only the
	// write end in this process keeps the watchdog alive.
	_ = pipeR.Close()

	p := &Proc{Cmd: cmd, pidFile: pidFile, pipeW: pipeW, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.exited)
	}()
	return p, nil
}

// PID returns the watchdog process id, which is also the id of the process
// group the supervised child belongs to.
func (p *Proc) PID() int {
	if p.Cmd == nil || p.Cmd.Process == nil {
		return 0
	}
	return p.Cmd.Process.Pid
}

// Exited returns a channel closed once the watchdog process has exited and
// been reaped.
func (p *Proc) Exited() <-chan struct{} {
	return p.exited
}

// SSHDPID waits for the pid file the watchdog writes when its child starts,
// and returns the supervised child's pid. Failing after 2 seconds, or after
// the watchdog has already exited, means the watchdog script or the child
// itself failed to start.
func (p *Proc) SSHDPID() (int, error) {
	return ReadPIDFile(p.pidFile, p.exited)
}

// Stop ends the watchdog and its whole process group: SIGTERM first with a
// 2 second grace period, SIGKILL after that, then the pipe write end is
// closed. It is safe to call more than once.
func (p *Proc) Stop() {
	p.stop.Do(func() {
		TerminateGroup(p.PID(), p.Cmd.Process, p.exited)
		_ = p.pipeW.Close()
		p.pipeW = nil
	})
}

// TerminateGroup signals the whole process group pid first so forked children
// also exit, waits up to 2 seconds for the leader to exit, then escalates to
// SIGKILL and waits for the leader to be reaped.
func TerminateGroup(pid int, proc *os.Process, exited <-chan struct{}) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
	}
	if proc != nil {
		_ = proc.Signal(syscall.SIGTERM)
	}
	if exited != nil {
		select {
		case <-exited:
			return
		case <-time.After(2 * time.Second):
		}
	}
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	if proc != nil {
		_ = proc.Kill()
	}
	if exited != nil {
		<-exited
	}
}

// ReadPIDFile polls for the child pid the watchdog script writes at startup.
// The write happens as the child starts, so a file missing or unparsable
// after 2 seconds (or after the watchdog has already exited) means the
// watchdog script itself failed.
func ReadPIDFile(path string, exited <-chan struct{}) (int, error) {
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for {
		data, err := os.ReadFile(path)
		if err != nil {
			lastErr = err
		} else {
			text := strings.TrimSpace(string(data))
			pid, convErr := strconv.Atoi(text)
			if convErr == nil && pid > 0 {
				return pid, nil
			}
			if convErr != nil {
				lastErr = fmt.Errorf("invalid pid %q: %w", text, convErr)
			} else {
				lastErr = fmt.Errorf("non-positive pid %q", text)
			}
		}
		select {
		case <-exited:
			return 0, fmt.Errorf("watchdog exited before writing valid pid to %s: %v", path, lastErr)
		default:
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("no valid pid in %s within 2s: %v", path, lastErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
