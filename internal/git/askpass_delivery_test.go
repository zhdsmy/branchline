package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// psLines is the command lines of every process.
func psLines(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("ps", "-axww", "-o", "command=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	return strings.Split(string(out), "\n")
}

// processesMatching is the command lines holding part.
func processesMatching(t *testing.T, part string) []string {
	t.Helper()
	var got []string
	for _, line := range psLines(t) {
		if strings.Contains(line, part) {
			got = append(got, line)
		}
	}
	return got
}

// waitNoProcess passes once no process command line holds part, failing
// after about five seconds.
func waitNoProcess(t *testing.T, part string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		left := processesMatching(t, part)
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("processes with %q in their command line are still running: %q", part, left)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitFileGone passes once path does not exist, failing after about five
// seconds.
func waitFileGone(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s is still there", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestAskPassDeliveryLocalFailsFast ends the command at once when the
// answer for a script on this Mac cannot reach its pipe: the script keeps
// waiting on the pipe otherwise, and git keeps waiting on the script.
func TestAskPassDeliveryLocalFailsFast(t *testing.T) {
	tmp := isolateCredentials(t)
	dir := testRepo(t)
	r := &recorder{}
	ask := NewAskPass(r.prompt)
	defer ask.Close()

	real := deliverPipe
	var mu sync.Mutex
	var pipe string
	deliverPipe = func(path string, line []byte) error {
		mu.Lock()
		pipe = path
		mu.Unlock()
		return errors.New("test: the pipe cannot be written")
	}
	t.Cleanup(func() { deliverPipe = real })

	ex := &LocalExecutor{Dir: dir, AskPass: ask}
	start := time.Now()
	_, err := credentialFill(t, ex)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("credential fill went on though the answer never reached the pipe")
	}
	if elapsed > 15*time.Second {
		t.Fatalf("the command took %s; an answer that cannot be delivered must end it at once", elapsed)
	}
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("error %T %v is not a git error", err, err)
	}
	if !strings.Contains(ge.Stderr, "could not deliver the answer") {
		t.Fatalf("stderr %q does not say the answer could not be delivered", ge.Stderr)
	}
	if !strings.Contains(ge.Stderr, "the pipe cannot be written") {
		t.Fatalf("stderr %q does not say why the delivery failed", ge.Stderr)
	}
	t.Logf("ended after %s with %q", elapsed, ge.Stderr)

	mu.Lock()
	p := pipe
	mu.Unlock()
	if p == "" {
		t.Fatal("the pipe never saw a delivery try")
	}
	waitFileGone(t, p)
	if left, _ := filepath.Glob(filepath.Join(tmp, "branchline-ap.*")); len(left) > 0 {
		t.Fatalf("pipes left behind: %q", left)
	}
	// No askpass script of this test is still running, and none of its
	// watchdogs either: they share the script's command line. The git
	// command line names the repository.
	waitNoProcess(t, filepath.Join(filepath.Base(ask.dir), "askpass.sh"))
	waitNoProcess(t, "-C "+dir)
}

// TestAskPassDeliveryRemoteFailsFast ends the command at once when the
// answer cannot reach the pipe of the script on the server: the channel
// the answer travels on is refused here.
func TestAskPassDeliveryRemoteFailsFast(t *testing.T) {
	isolateCredentials(t)
	srv := startSSHD(t)
	dir := testRepo(t)

	// An ssh that refuses the channel the answer travels on: the one whose
	// remote command writes the pipe. Every other command runs as usual.
	trace := filepath.Join(t.TempDir(), "catpipes")
	wrapper := filepath.Join(t.TempDir(), "ssh")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$a\" in\n" +
		"    *\"cat > \"*)\n" +
		"      printf '%s\\n' \"$a\" >> \"$CATPIPE_TRACE\"\n" +
		"      echo 'test: the reply channel is down' >&2\n" +
		"      exit 255 ;;\n" +
		"  esac\n" +
		"done\n" +
		"exec " + ShellQuote(srv.ssh) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CATPIPE_TRACE", trace)

	r := &recorder{}
	ask := NewAskPass(r.prompt)
	defer ask.Close()
	ex := srv.executor(t, dir)
	ex.SSH = wrapper
	ex.AskPass = ask
	// The server reads the user's global configuration here, which can name
	// a credential helper that answers before the askpass script.
	ex.NoGlobalConfig = true

	// A server without a terminal keeps the command's process tree when the
	// client goes away: only the script's own watchdog ends it, and then the
	// rest of the tree unwinds. Give the script a short deadline so that the
	// test can watch the tree leave.
	realTimeout := scriptTimeout
	scriptTimeout = 3
	t.Cleanup(func() { scriptTimeout = realTimeout })

	start := time.Now()
	_, err := credentialFill(t, ex)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("credential fill went on though the answer never reached the pipe on the server")
	}
	if elapsed > 15*time.Second {
		t.Fatalf("the command took %s; an answer that cannot be delivered must end it at once", elapsed)
	}
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("error %T %v is not a git error", err, err)
	}
	if !strings.Contains(ge.Stderr, "could not deliver the answer") {
		t.Fatalf("stderr %q does not say the answer could not be delivered", ge.Stderr)
	}
	t.Logf("ended after %s with %q", elapsed, ge.Stderr)

	// The pipe on the server is gone, and nothing of the command is still
	// running there (the server is this machine).
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("the reply channel was never tried: %v", err)
	}
	line := strings.TrimSpace(strings.Split(string(data), "\n")[0])
	pipe := strings.TrimPrefix(line, "cat > ")
	if !strings.Contains(pipe, "branchline-ap.") {
		t.Fatalf("the reply command %q does not write an askpass pipe", line)
	}
	waitFileGone(t, pipe)
	waitNoProcess(t, filepath.Base(pipe))
	waitNoProcess(t, "branchline-askpass.")
	waitNoProcess(t, dir)
}

var askPipeRe = regexp.MustCompile(askMarker + `\w (\S+) `)

// scriptPipe is the pipe of a script written for the test, or "" when
// nothing matched.
func scriptPipe(t *testing.T, dir string) string {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(dir, "branchline-ap.*"))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// runWatchdogScript runs the askpass script with the given shell and
// timeout, answering nothing, and checks the watchdog ends it.
func runWatchdogScript(t *testing.T, shell []string, timeout int) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "askpass.sh")
	if err := os.WriteFile(script, []byte(scriptForTimeout("L", timeout)), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", dir)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	args := append(append([]string{}, shell...), script, "Password for 'https://example.com':")
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)
	if runErr == nil {
		t.Fatal("the script printed an answer nobody gave")
	}
	if elapsed > time.Duration(timeout+1)*time.Second {
		t.Fatalf("the watchdog took %s for a %ds timeout", elapsed, timeout)
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) && ee.ExitCode() == 0 {
		t.Fatal("the script exited with 0 without an answer")
	}
	marker := stderr.String()
	m := askPipeRe.FindStringSubmatch(marker)
	if m == nil {
		t.Fatalf("the script never asked: stderr %q", marker)
	}
	pipe := m[1]
	waitFileGone(t, pipe)
	// Neither the script nor its watchdog is still running; both share
	// the script's command line.
	waitNoProcess(t, script)
	if stdout.Len() != 0 {
		t.Fatalf("the script printed %q without an answer", stdout.String())
	}
}

// runAnsweredScript runs the script and gives it an answer: it must print
// the answer, and the watchdog must leave without waiting out its timeout.
func runAnsweredScript(t *testing.T, shell []string, timeout int) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "askpass.sh")
	if err := os.WriteFile(script, []byte(scriptForTimeout("L", timeout)), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", dir)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	args := append(append([]string{}, shell...), script, "Password for 'https://example.com':")
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	errReader, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Answer as soon as the script says where its pipe is, and drain its
	// standard error until the end.
	lines := make(chan string, 8)
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		scanner := bufio.NewScanner(errReader)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	answered := make(chan error, 1)
	go func() {
		for line := range lines {
			if m := askPipeRe.FindStringSubmatch(line); m != nil {
				answered <- writePipe(m[1], replyLine("the answer", true))
				return
			}
		}
		answered <- errors.New("no question on standard error")
	}()

	start := time.Now()
	waitErr := cmd.Wait()
	elapsed := time.Since(start)
	<-scanned
	if waitErr != nil {
		t.Fatalf("the script failed though it was answered: %v", waitErr)
	}
	if err := <-answered; err != nil {
		t.Fatal(err)
	}
	if out := stdout.String(); out != "the answer\n" {
		t.Fatalf("the script printed %q, want the answer", out)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("an answered script took %s; the watchdog must not hold it", elapsed)
	}
	waitNoProcess(t, script)
	if got := scriptPipe(t, dir); got != "" {
		t.Fatalf("pipe %s left behind", got)
	}
}

func busyboxSh(t *testing.T) []string {
	t.Helper()
	prog, err := exec.LookPath("busybox")
	if err != nil {
		return nil
	}
	return []string{prog, "sh"}
}

// TestAskPassDeliveryWatchdog checks the script's own deadline: without an
// answer the watchdog ends the script, with an answer it leaves by itself.
func TestAskPassDeliveryWatchdog(t *testing.T) {
	shells := map[string][]string{"sh": {"/bin/sh"}}
	if bb := busyboxSh(t); bb != nil {
		shells["busybox"] = bb
	} else {
		t.Log("no busybox on this machine; the busybox case is unchecked here")
	}
	for name, shell := range shells {
		t.Run(name+"/timeout", func(t *testing.T) {
			runWatchdogScript(t, shell, 2)
		})
		t.Run(name+"/answered", func(t *testing.T) {
			runAnsweredScript(t, shell, 30)
		})
	}
}

// TestAskPassDeliveryCancelUnchanged cancels the question: the command
// fails the way it did, never with a delivery error.
func TestAskPassDeliveryCancelUnchanged(t *testing.T) {
	tmp := isolateCredentials(t)
	dir := testRepo(t)
	r := &recorder{cancel: true}
	ask := NewAskPass(r.prompt)
	defer ask.Close()

	ex := &LocalExecutor{Dir: dir, AskPass: ask}
	start := time.Now()
	_, err := credentialFill(t, ex)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("credential fill went on after the user cancelled")
	}
	if elapsed > 15*time.Second {
		t.Fatalf("cancelling took %s", elapsed)
	}
	if strings.Contains(err.Error(), "could not deliver") {
		t.Fatalf("a cancellation must not look like a delivery failure: %v", err)
	}
	if !strings.Contains(err.Error(), "unable to read askpass response") {
		t.Fatalf("the cancellation error changed: %v", err)
	}
	t.Logf("cancelled after %s with %q", elapsed, err)
	if left, _ := filepath.Glob(filepath.Join(tmp, "branchline-ap.*")); len(left) > 0 {
		t.Fatalf("pipes left behind: %q", left)
	}
	waitNoProcess(t, filepath.Join(filepath.Base(ask.dir), "askpass.sh"))
}
