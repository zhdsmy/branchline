package git

import (
	"bufio"
	"bytes"
	"context"
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
	script string
	stdout *strings.Builder
	errCh  chan error
}

// startAskPassScript runs the script and waits for the question on its
// standard error, the way the plumbing of a real command does.
func startAskPassScript(t *testing.T, timeout int) *askPassExchange {
	t.Helper()
	script := askPassScriptForTest(t, timeout)
	cmd := exec.Command(script, "Password for 'https://example.com': ")
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
	ex := &askPassExchange{cmd: cmd, script: script, stdout: &strings.Builder{}, errCh: make(chan error, 1)}
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

// The read is only tried again while the pipe exists. A pipe removed while
// the script waits — a temporary-folder cleanup is what does this in
// practice — must end the script within a couple of seconds: the keeper's
// round notices the pipe is gone, its exit closes the last write end, and
// the end of file wakes the read, which sees the pipe is gone and leaves.
// Without the check the script spins on a path that no longer exists, and
// nothing ever ends it.
func TestAskPassSignalFifoRemoved(t *testing.T) {
	ex := startAskPassScript(t, askScriptTimeout)
	// The script is waiting inside a blocked open or read of its pipe when
	// the deletion lands; it cannot notice by itself — an unlink does not
	// wake either — so the wait would outlive the bound without the keeper.
	if err := os.Remove(ex.pipe); err != nil {
		t.Fatalf("remove the pipe: %v", err)
	}
	started := time.Now()
	err := ex.finished(t, 5*time.Second)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("the script survived the removal of its pipe")
	}
	t.Logf("script exited %v, %s after the pipe was removed", err, elapsed)
	// Nothing of the script's tree may be left: the keeper, which holds the
	// pipe open, is the one process that could outlive the script. It is a
	// subshell of the script, so it carries the script's path in its
	// command line; the script itself is gone at this point, so any match
	// left is a leak.
	deadline := time.Now().Add(2 * time.Second)
	for {
		leftover := 0
		for _, e := range psEntries(t) {
			if strings.Contains(e[2], ex.script) {
				leftover++
			}
		}
		if leftover == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d process(es) of the askpass script outlived its end", leftover)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Canceling an askpass script by signalling its process group with SIGTERM
// — the way configureGitCancel and configureSSHCancel end canceled commands —
// terminates both the script and its keeper without leaving processes or
// pipes behind.
func TestAskPassSignalCancelKillsProcessGroup(t *testing.T) {
	script := askPassScriptForTest(t, askScriptTimeout)
	cmd := exec.Command(script, "Password for 'https://example.com': ")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
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
	var pipe string
	select {
	case pipe = <-pipeCh:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatal("the script never asked its question")
	}
	if pipe == "" {
		t.Fatal("the script ended without asking")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM process group: %v", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled script exited with 0")
		}
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatal("script did not exit after process-group SIGTERM")
	}
	waitFileGone(t, pipe)
	waitNoProcess(t, script)
}

// Even if only the askpass script itself is ended with SIGKILL (bypassing its
// EXIT trap and without killing its process group), the keeper does not hang
// blocked in open: it finishes at its deadline, removes the pipe, and leaves
// no process behind.
func TestAskPassSignalSigkillNoStuckKeeper(t *testing.T) {
	ex := startAskPassScript(t, 2)
	if err := ex.cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL script: %v", err)
	}
	if err := ex.finished(t, 5*time.Second); err == nil {
		t.Fatal("SIGKILLed script exited with 0")
	}
	waitFileGone(t, ex.pipe)
	waitNoProcess(t, ex.script)
}

// TestConfiguredLinuxAskPassScript checks the remote askpass script on a
// configured Linux SSH host (covering Ubuntu dash and Alpine BusyBox ash):
// a normal answer returns the secret with no leftover processes, removing the
// pipe while waiting ends the script with exit 1 within seconds and leaves no
// processes, and process-group SIGTERM tears down both script and keeper.
func TestConfiguredLinuxAskPassScript(t *testing.T) {
	target := resolveConfiguredLinuxSSHTarget(t)
	conn := &SSHExecutor{Host: target.hostAlias, Dir: target.rootRepo, NoMultiplex: true}

	sshOut := func(t *testing.T, command string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := conn.RunShell(ctx, command)
		if err != nil {
			t.Fatalf("remote command %q failed: %v", command, err)
		}
		return string(out)
	}

	scratch := strings.TrimSpace(sshOut(t, "mktemp -d "+ShellQuote(target.rootRepo+"/askpass-linux.XXXXXX")))
	if !strings.HasPrefix(scratch, target.rootRepo+"/askpass-linux.") {
		t.Fatalf("unexpected remote scratch directory %q", scratch)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := conn.RunShell(ctx, "rm -rf -- "+ShellQuote(scratch)); err != nil {
			t.Errorf("cleanup %s: %v", scratch, err)
		}
	})

	startRemote := func(t *testing.T, name string, timeout int) (scriptPath, pipe string, pg int, stdout *bytes.Buffer, done chan error, cancel context.CancelFunc) {
		t.Helper()
		tmpDir := scratch + "/" + name + "-tmp"
		scriptPath = scratch + "/" + name + ".sh"
		writeCmd := "mkdir -p " + ShellQuote(tmpDir) +
			" && printf " + ShellQuote(printfScriptFormat(scriptForTimeout("R", timeout))) +
			" > " + ShellQuote(scriptPath) +
			" && chmod 700 " + ShellQuote(scriptPath)
		sshOut(t, writeCmd)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		remoteCmd := remoteShellCommand(pgidPrefix + "TMPDIR=" + ShellQuote(tmpDir) + " " + ShellQuote(scriptPath) + " " + ShellQuote("Password for 'https://example.com': "))
		cmd := exec.CommandContext(ctx, sshProgram(), conn.SSHArgs(remoteCmd)...)
		cmd.Env = conn.env()
		stdout = &bytes.Buffer{}
		cmd.Stdout = stdout
		errReader, err := cmd.StderrPipe()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}

		pw := newPgidWriter()
		var stderrBuf bytes.Buffer
		pw.chain(&stderrBuf)
		pipeCh := make(chan string, 1)
		go func() {
			sc := bufio.NewScanner(errReader)
			for sc.Scan() {
				line := sc.Text()
				_, _ = pw.Write([]byte(line + "\n"))
				if i := strings.Index(line, askMarker); i >= 0 {
					rest := line[i+len(askMarker):]
					_, rest, _ = strings.Cut(rest, " ")
					p, _, _ := strings.Cut(rest, " ")
					pipeCh <- p
					// Keep draining stderr so the remote command never blocks.
					for sc.Scan() {
						_, _ = pw.Write([]byte(sc.Text() + "\n"))
					}
					return
				}
			}
			pipeCh <- ""
		}()

		done = make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		select {
		case pipe = <-pipeCh:
		case <-time.After(15 * time.Second):
			cancel()
			t.Fatalf("remote askpass script %s never asked its question", name)
		}
		if pipe == "" {
			cancel()
			t.Fatalf("remote askpass script %s ended without asking: stderr=%q", name, stderrBuf.String())
		}
		pg = pw.waitPgid(5 * time.Second)
		return scriptPath, pipe, pg, stdout, done, cancel
	}

	waitNoRemoteScript := func(t *testing.T, scriptPath string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			left := 0
			for _, e := range scanProcesses(t, conn) {
				if strings.Contains(e[2], scriptPath) {
					left++
				}
			}
			if left == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d remote process(es) still carry %q in cmdline", left, scriptPath)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	t.Run("NormalAnswer", func(t *testing.T) {
		scriptPath, pipe, _, stdout, done, cancel := startRemote(t, "normal", 30)
		defer cancel()

		if err := conn.replyRemote(pipe, replyLine("s3cret pass", true)); err != nil {
			t.Fatalf("replyRemote: %v", err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("remote askpass exited with error on normal answer: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("remote askpass did not exit after receiving answer")
		}
		if got := stdout.String(); got != "s3cret pass\n" {
			t.Fatalf("remote askpass stdout = %q, want %q", got, "s3cret pass\n")
		}
		if got := strings.TrimSpace(sshOut(t, "if [ -e "+ShellQuote(pipe)+" ]; then echo present; else echo gone; fi")); got != "gone" {
			t.Fatalf("remote pipe %s still present after normal answer", pipe)
		}
		waitNoRemoteScript(t, scriptPath)
	})

	t.Run("FifoRemoved", func(t *testing.T) {
		scriptPath, pipe, _, stdout, done, cancel := startRemote(t, "removed", 300)
		defer cancel()

		started := time.Now()
		sshOut(t, "rm -f -- "+ShellQuote(pipe))
		var waitErr error
		select {
		case waitErr = <-done:
		case <-time.After(8 * time.Second):
			t.Fatal("remote askpass did not exit within 8s after its pipe was removed")
		}
		elapsed := time.Since(started)
		if waitErr == nil {
			t.Fatal("remote askpass exited 0 after its pipe was removed")
		}
		var ee *exec.ExitError
		if !errors.As(waitErr, &ee) || ee.ExitCode() != 1 {
			t.Fatalf("remote askpass exit = %v, want exit code 1", waitErr)
		}
		if got := stdout.String(); got != "" {
			t.Fatalf("remote askpass printed %q after pipe removal", got)
		}
		t.Logf("remote askpass exited 1 in %s after pipe removal", elapsed)
		waitNoRemoteScript(t, scriptPath)
	})

	t.Run("GroupCancel", func(t *testing.T) {
		scriptPath, pipe, pg, _, done, cancel := startRemote(t, "cancel", 300)
		defer cancel()
		if pg <= 0 {
			t.Fatal("remote askpass did not report its process group")
		}

		sshOut(t, remoteGroupKillScript(pg)+" || true")
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("remote askpass exited 0 after process-group SIGTERM")
			}
		case <-time.After(8 * time.Second):
			t.Fatal("remote askpass did not exit after process-group SIGTERM")
		}
		if got := strings.TrimSpace(sshOut(t, "if [ -e "+ShellQuote(pipe)+" ]; then echo present; else echo gone; fi")); got != "gone" {
			t.Fatalf("remote pipe %s still present after process-group SIGTERM", pipe)
		}
		waitNoRemoteScript(t, scriptPath)
		deadline := time.Now().Add(5 * time.Second)
		for linuxGroupAlive(scanProcesses(t, conn), pg) {
			if time.Now().After(deadline) {
				t.Fatalf("remote process group %d still alive after SIGTERM", pg)
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
}
