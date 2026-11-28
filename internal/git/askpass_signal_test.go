package git

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// askPassScriptForTest writes the askpass script with the given watchdog
// deadline into a file of its own and returns the path.
func askPassScriptForTest(t *testing.T, timeout int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "askpass.sh")
	if err := os.WriteFile(path, []byte(scriptForTimeout("L", timeout)), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// askPassExchange is one running askpass script: the question line it
// printed on standard error is parsed for the pipe, and the test drives
// the rest of the exchange by hand.
type askPassExchange struct {
	cmd    *exec.Cmd
	pipe   string
	stdout *strings.Builder
	errCh  chan error
}

// startAskPassScript runs the script and waits for the question on its
// standard error, the way the plumbing of a real command does.
func startAskPassScript(t *testing.T, timeout int) *askPassExchange {
	t.Helper()
	cmd := exec.Command(askPassScriptForTest(t, timeout), "Password for 'https://example.com': ")
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ex := &askPassExchange{cmd: cmd, stdout: &strings.Builder{}, errCh: make(chan error, 1)}
	pipeCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(errPipe)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, askMarker) {
				rest := strings.TrimPrefix(line, askMarker)
				_, rest, _ = strings.Cut(rest, " ")
				pipe, _, _ := strings.Cut(rest, " ")
				pipeCh <- pipe
				return
			}
		}
		pipeCh <- ""
	}()
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			ex.stdout.WriteString(sc.Text() + "\n")
		}
	}()
	select {
	case ex.pipe = <-pipeCh:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the script never asked its question")
	}
	if ex.pipe == "" {
		t.Fatal("the script ended without asking")
	}
	go func() { ex.errCh <- cmd.Wait() }()
	return ex
}

// openWriter opens the pipe for writing and keeps it open, retrying like
// writePipe until the script's end of the rendezvous exists. Holding the
// descriptor open keeps the script inside its read, so what the test does
// next meets a waiting script rather than a race.
func (x *askPassExchange) openWriter(t *testing.T) *os.File {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		fd, err := syscall.Open(x.pipe, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			return os.NewFile(uintptr(fd), x.pipe)
		}
		if (!errors.Is(err, syscall.ENXIO) && !errors.Is(err, syscall.ENOENT)) || time.Now().After(deadline) {
			t.Fatalf("open the answer pipe: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// finished passes once the script ended, failing after the bound.
func (x *askPassExchange) finished(t *testing.T, bound time.Duration) error {
	t.Helper()
	select {
	case err := <-x.errCh:
		return err
	case <-time.After(bound):
		_ = x.cmd.Process.Kill()
		t.Fatal("the askpass script did not end within the bound")
		return nil
	}
}

// The askpass script waits for its answer inside a blocking open of a
// named pipe, which a signal without a restarting handler interrupts.
// SIGCHLD is such a signal on this machine's /bin/sh, a bash of the 3.2
// line: before the read learned to try again, one stray signal ended the
// script — and with it the login — with "Interrupted system call". The
// answer must still arrive after the interruptions.
func TestAskPassSignalWhileWaiting(t *testing.T) {
	ex := startAskPassScript(t, askScriptTimeout)
	// No writer has connected: the script is inside the blocking open of
	// its pipe, exactly where it waits while the answer is prepared. The
	// burst covers that wait; one interruption used to end the script.
	for i := 0; i < 10; i++ {
		if err := ex.cmd.Process.Signal(syscall.SIGCHLD); err != nil {
			t.Fatalf("signal the waiting script: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	select {
	case err := <-ex.errCh:
		t.Fatalf("the script died of the signals before the answer came: %v", err)
	default:
	}
	w := ex.openWriter(t)
	if _, err := w.Write([]byte("+s3cret pass\n")); err != nil {
		t.Fatalf("write the answer: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close the answer pipe: %v", err)
	}
	if err := ex.finished(t, 5*time.Second); err != nil {
		t.Fatalf("the script failed after the interruptions: %v", err)
	}
	if got := ex.stdout.String(); got != "s3cret pass\n" {
		t.Fatalf("answer %q, want %q", got, "s3cret pass\n")
	}
}

// A line that is no answer still cancels the login, signals or not.
func TestAskPassSignalCancelUnchanged(t *testing.T) {
	ex := startAskPassScript(t, askScriptTimeout)
	for i := 0; i < 6; i++ {
		_ = ex.cmd.Process.Signal(syscall.SIGCHLD)
		time.Sleep(25 * time.Millisecond)
	}
	w := ex.openWriter(t)
	if _, err := w.Write([]byte("-\n")); err != nil {
		t.Fatalf("write the cancel: %v", err)
	}
	_ = w.Close()
	err := ex.finished(t, 5*time.Second)
	if err == nil {
		t.Fatal("the script accepted a cancel line as the answer")
	}
	if got := ex.stdout.String(); got != "" {
		t.Fatalf("a canceled script printed %q", got)
	}
}

// The watchdog still ends a script nobody answers, even while the read
// keeps trying again: TERM is trapped, and the trap wins over the retry.
func TestAskPassSignalWatchdogStillEnds(t *testing.T) {
	ex := startAskPassScript(t, 1)
	err := ex.finished(t, 8*time.Second)
	if err == nil {
		t.Fatal("an unanswered script exited with success")
	}
	if got := ex.stdout.String(); got != "" {
		t.Fatalf("an unanswered script printed %q", got)
	}
}
