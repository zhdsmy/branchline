package git

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// pgidMarker starts the line the server's shell writes on standard error
// before anything else, naming its own process group. The record separator
// keeps it out of any real output, and nothing on the server can print it
// first, because the shell writes it before the command starts.
const pgidMarker = "\x1ebranchline-pgid "

// pgidPrefix makes the server name its process group before anything else.
// It is POSIX for the /bin/sh remoteShellCommand execs over the login
// shell: exec replaces the shell instead of forking, so the sh runs with
// the login shell's own process id, and $$ names it. sshd made that shell
// the leader of the session's process group, so the number is the group of
// everything the command starts.
const pgidPrefix = "printf \"" + pgidMarker + "%s\\n\" \"$$\" >&2; "

// remoteGroupKillScript is the script that ends the process group the
// command on the server named, in the form POSIX writes: the dash that is
// /bin/sh on Debian and Ubuntu takes neither -- nor a negative pid after
// the signal's other spellings, but takes both here.
func remoteGroupKillScript(pg int) string {
	return "kill -s TERM -- -" + strconv.Itoa(pg)
}

// remoteGroupKill is the kill command as it reaches the server. RunShell
// wraps the script for /bin/sh, so the command on the wire is the wrapping
// of the bare script: the kill of a login shell that is not POSIX needs
// not understand process groups or the -- before one.
func remoteGroupKill(pg int) string {
	return remoteShellCommand(remoteGroupKillScript(pg))
}

// pgidScanCap bounds how many bytes of a possible marker the scanner holds
// back: the marker itself, the digits of a process group number, and the
// newline. Anything longer turns out not to be the marker and passes on.
const pgidScanCap = len(pgidMarker) + 21

// pgidWriter is the standard error of a command the server runs: it lifts
// the first complete marker line out of the stream, remembers the process
// group it names, and hands everything else to out unchanged, even when a
// write ends in the middle of the marker. Only the first marker line is
// lifted; any later one goes through like ordinary text.
type pgidWriter struct {
	out io.Writer

	mu     sync.Mutex
	hold   []byte // bytes not passed on yet: a possible start of the marker
	closed bool   // the marker line was lifted; the rest passes through
	pg     int
	ready  chan struct{} // closed the first time pg is set
}

func newPgidWriter() *pgidWriter {
	return &pgidWriter{ready: make(chan struct{})}
}

// chain aims the writer at out. The plumbing calls it once before the
// command starts: canceling needs the writer before its place in the
// plumbing is known, so the two halves come together here.
func (w *pgidWriter) chain(out io.Writer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.out = out
}

func (w *pgidWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.mu.Lock()
	defer w.mu.Unlock()
	for len(p) > 0 {
		if w.closed {
			_, _ = w.out.Write(p)
			break
		}
		if len(w.hold) == 0 {
			i := 0
			for i < len(p) && p[i] != pgidMarker[0] {
				i++
			}
			if i > 0 {
				_, _ = w.out.Write(p[:i])
				p = p[i:]
			}
			if len(p) == 0 {
				break
			}
		}
		// One byte at a time: a write that holds both the end of one
		// false start and the beginning of the marker must not swallow
		// the marker with what the false start passes through.
		w.hold = append(w.hold, p[0])
		p = p[1:]
		w.scanLocked()
	}
	return n, nil
}

// scanLocked consumes what hold holds: a complete marker line lifts the
// process group it names, and anything that stops matching passes through
// unchanged.
func (w *pgidWriter) scanLocked() {
	for len(w.hold) > 0 && !w.closed {
		h := w.hold
		if len(h) < len(pgidMarker) {
			if string(h) == pgidMarker[:len(h)] {
				return // still a possible start: wait for more
			}
			w.flushLocked()
			continue
		}
		if string(h[:len(pgidMarker)]) != pgidMarker {
			w.flushLocked()
			continue
		}
		rest := h[len(pgidMarker):]
		i := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		switch {
		case i > 0 && i < len(rest) && rest[i] == '\n':
			if pg, err := strconv.Atoi(string(rest[:i])); err == nil && pg > 0 {
				w.pg = pg
				if w.ready != nil {
					close(w.ready)
					w.ready = nil
				}
			}
			w.closed = true
			w.hold = h[len(pgidMarker)+i+1:]
			if len(w.hold) > 0 {
				_, _ = w.out.Write(w.hold)
				w.hold = nil
			}
		case len(h) == pgidScanCap:
			// Too many digits, or no newline within the bound: not the marker.
			w.flushLocked()
		case i < len(rest):
			// A byte that is neither a digit nor the newline: not the marker.
			w.flushLocked()
		default:
			return // digits so far: wait for more
		}
	}
}

// flushLocked passes the held bytes through unchanged; they turned out not
// to be the marker.
func (w *pgidWriter) flushLocked() {
	_, _ = w.out.Write(w.hold)
	w.hold = nil
}

// finish passes through whatever the end of the stream left held, so that
// nothing is lost when the connection breaks in the middle of a write.
func (w *pgidWriter) finish() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.hold) > 0 {
		w.flushLocked()
	}
}

// pgid is the process group the command on the server named, or 0 when it
// has not been heard from yet.
func (w *pgidWriter) pgid() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pg
}

// waitPgid blocks until the server named its process group or the bound
// passes, and returns what is known then. The naming is written before the
// command itself starts, so the wait is the difference between a fast
// cancellation reaching the server's processes and one that only ends the
// connection.
func (w *pgidWriter) waitPgid(bound time.Duration) int {
	w.mu.Lock()
	if w.pg > 0 || w.ready == nil {
		pg := w.pg
		w.mu.Unlock()
		return pg
	}
	ready := w.ready
	w.mu.Unlock()
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case <-ready:
	case <-timer.C:
	}
	return w.pgid()
}

// cancelWait is how long a canceled command gets for its pipes before the
// wait for them ends. While the connection is shared, the ssh client hands
// its pipes to the master process, which keeps them open after the client
// dies, and the wait for them would never end; git on this Mac can leave
// its children holding them the same way.
const cancelWait = 3 * time.Second

// pgidWait is how long a cancellation gives the server's naming of its
// process group to arrive before it ends the connection anyway. The naming
// is the first thing the server's shell writes, ahead of the command
// itself, so anything but a connection that never got going answers well
// inside the bound; ending the client before the naming arrived would drop
// it with the connection and leave the command running on the server with
// nobody left to end it.
const pgidWait = cancelWait

// remoteKillTimeout bounds one round trip of the command that stops the
// server's processes.
const remoteKillTimeout = 5 * time.Second

// remoteKillAttempts is how often the stop is sent when the round trip
// itself fails: one lost connection must not leave the server's processes
// running with nobody to end them. A round trip that reaches the server
// ends the retries whatever the kill answers — an answer of no such process
// means the group is already gone.
const remoteKillAttempts = 3

// remoteKillSpacing is the pause between the attempts.
const remoteKillSpacing = time.Second

// stopRemoteGroup sends TERM to the process group pg on the server, the
// one the command itself named in its marker line. The stop rides the same
// connection as every other shell command, on its own goroutine: canceling
// must return at once whether or not the server answers.
func (s *SSHExecutor) stopRemoteGroup(pg int) {
	if pg <= 0 {
		return
	}
	go func() {
		for attempt := 0; attempt < remoteKillAttempts; attempt++ {
			if attempt > 0 {
				time.Sleep(remoteKillSpacing)
			}
			ctx, cancel := context.WithTimeout(context.Background(), remoteKillTimeout)
			_, err := s.RunShell(ctx, remoteGroupKillScript(pg))
			cancel()
			// Only a round trip that never reached the server is tried
			// again: a kill the server answered either ended the group or
			// found it gone.
			if err == nil {
				return
			}
			var ge *Error
			if errors.As(err, &ge) && ge.ExitCode != 255 && ge.ExitCode != -1 {
				return
			}
		}
	}()
}

// configureCancel says how canceling a command stops it. An executor hands
// it to the plumbing, which applies it after the default shell handling:
// exec.CommandContext installs its own Cancel on every command, so setting
// one early cannot be told apart from the default, and the shell handling
// would either overwrite it or never run.
type configureCancel func(cmd *exec.Cmd)

// configureSSHCancel makes canceling the ssh command stop the server's
// process group and the local ssh (or its local process group when askpass
// configured one); the wait for the pipes is bounded by cancelWait. The
// cancellation first gives the server's naming of its process group a
// moment to arrive: the naming leads the command itself, so a cancellation
// landing in between would otherwise end the connection while the command
// keeps running on the server, its process group unknown and unreachable.
func configureSSHCancel(cmd *exec.Cmd, ssh *SSHExecutor, pw *pgidWriter) {
	prev := cmd.Cancel
	cmd.WaitDelay = cancelWait
	cmd.Cancel = func() error {
		pg := pw.waitPgid(pgidWait)
		if pg > 0 {
			ssh.stopRemoteGroup(pg)
		}
		var err error
		if prev != nil {
			err = prev()
		} else if cmd.Process != nil {
			err = cmd.Process.Kill()
		}
		return err
	}
}
