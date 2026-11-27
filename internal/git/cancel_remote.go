package git

import (
	"context"
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
}

func newPgidWriter() *pgidWriter {
	return &pgidWriter{}
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

// cancelWait is how long a canceled command gets for its pipes before the
// wait for them ends. While the connection is shared, the ssh client hands
// its pipes to the master process, which keeps them open after the client
// dies, and the wait for them would never end; git on this Mac can leave
// its children holding them the same way.
const cancelWait = 3 * time.Second

// remoteKillTimeout bounds the command that stops the server's processes.
const remoteKillTimeout = 5 * time.Second

// stopRemoteGroup sends TERM to the process group pg on the server, the
// one the command itself named in its marker line. The stop rides the same
// connection as every other shell command, on its own goroutine: canceling
// must return at once whether or not the server answers.
func (s *SSHExecutor) stopRemoteGroup(pg int) {
	if pg <= 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), remoteKillTimeout)
		defer cancel()
		_, _ = s.RunShell(ctx, "kill -TERM -- -"+strconv.Itoa(pg))
	}()
}

// configureSSHCancel makes canceling the ssh command stop the local ssh at
// once and, when the server already named its process group, that group
// too; the wait for the pipes is bounded by cancelWait.
func configureSSHCancel(cmd *exec.Cmd, ssh *SSHExecutor, pw *pgidWriter) {
	cmd.WaitDelay = cancelWait
	cmd.Cancel = func() error {
		var err error
		if cmd.Process != nil {
			err = cmd.Process.Kill()
		}
		if pg := pw.pgid(); pg > 0 {
			ssh.stopRemoteGroup(pg)
		}
		return err
	}
}
