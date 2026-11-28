package git

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// TestExecStreamLocalMatchesCollected checks that a command on this machine
// given a stream writer delivers exactly the bytes the same command without
// one returns, and that nothing is collected alongside the stream.
func TestExecStreamLocalMatchesCollected(t *testing.T) {
	dir := testRepo(t)
	ex := &LocalExecutor{Dir: dir, NoGlobalConfig: true}
	args := []string{"log", "--format=%H %s"}
	collected, err := ex.Run(context.Background(), nil, args...)
	if err != nil {
		t.Fatalf("collected run: %v", err)
	}
	var streamed bytes.Buffer
	out, err := ex.Run(streamWriter(context.Background(), &streamed), nil, args...)
	if err != nil {
		t.Fatalf("streamed run: %v", err)
	}
	if !bytes.Equal(streamed.Bytes(), collected) {
		t.Errorf("streamed %q, collected %q", streamed.Bytes(), collected)
	}
	if len(out) != 0 {
		t.Errorf("a streamed command collected %q", out)
	}
}

// TestExecStreamSSHMatchesCollected checks the same byte-for-byte equality
// for a command the server runs, over the loopback sshd.
func TestExecStreamSSHMatchesCollected(t *testing.T) {
	srv := startSSHD(t)
	dir := testRepo(t)
	ex := srv.executor(t, dir)
	args := []string{"log", "--format=%H %s"}
	collected, err := ex.Run(context.Background(), nil, args...)
	if err != nil {
		t.Fatalf("collected run: %v", err)
	}
	var streamed bytes.Buffer
	out, err := ex.Run(streamWriter(context.Background(), &streamed), nil, args...)
	if err != nil {
		t.Fatalf("streamed run: %v", err)
	}
	if !bytes.Equal(streamed.Bytes(), collected) {
		t.Errorf("streamed %q, collected %q", streamed.Bytes(), collected)
	}
	if len(out) != 0 {
		t.Errorf("a streamed command collected %q", out)
	}
}

// TestExecStreamFailureError checks that a failing command reports the same
// exit code and standard error with a stream writer as without one, with an
// empty standard output in the error.
func TestExecStreamFailureError(t *testing.T) {
	dir := testRepo(t)
	ex := &LocalExecutor{Dir: dir, NoGlobalConfig: true}
	args := []string{"log", "--format=%H", "no-such-reference"}
	_, plainErr := ex.Run(context.Background(), nil, args...)
	if plainErr == nil {
		t.Fatal("the probe reference unexpectedly exists")
	}
	var streamed bytes.Buffer
	_, streamErr := ex.Run(streamWriter(context.Background(), &streamed), nil, args...)
	if streamErr == nil {
		t.Fatal("the streamed run of a failing command succeeded")
	}
	var want, got *Error
	if !errors.As(plainErr, &want) || !errors.As(streamErr, &got) {
		t.Fatalf("plain %v (%T), streamed %v (%T)", plainErr, plainErr, streamErr, streamErr)
	}
	if got.ExitCode != want.ExitCode {
		t.Errorf("exit code %d, want %d", got.ExitCode, want.ExitCode)
	}
	if got.Stderr != want.Stderr {
		t.Errorf("stderr %q, want %q", got.Stderr, want.Stderr)
	}
	if len(got.Stdout) != 0 {
		t.Errorf("a streamed failure carries standard output %q", got.Stdout)
	}
}

// TestExecStreamCommandLog checks that a streamed command is recorded like
// any other — arguments, standard error and exit code of a failure — except
// that its standard output, delivered to the stream, is empty in the log.
func TestExecStreamCommandLog(t *testing.T) {
	dir := testRepo(t)
	log := NewCommandLog()
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true}).WithCommandLog(log)

	failArgs := []string{"log", "--format=%H", "no-such-reference"}
	var streamed bytes.Buffer
	err := r.runStreaming(context.Background(), quick, nil, &streamed, failArgs...)
	if err == nil {
		t.Fatal("the failing command succeeded")
	}
	okArgs := []string{"rev-parse", "--verify", "HEAD"}
	if err := r.runStreaming(context.Background(), quick, nil, &streamed, okArgs...); err != nil {
		t.Fatalf("the succeeding command failed: %v", err)
	}

	records, dropped := log.Snapshot()
	if dropped != 0 || len(records) != 2 {
		t.Fatalf("records %d dropped %d", len(records), dropped)
	}
	fail, ok := records[0], records[1]
	if strings.Join(fail.Args, " ") != strings.Join(failArgs, " ") {
		t.Errorf("logged args %v, want %v", fail.Args, failArgs)
	}
	if fail.ExitCode == 0 {
		t.Error("the failure's logged exit code is 0")
	}
	if !strings.Contains(fail.Stderr, "no-such-reference") {
		t.Errorf("the failure's logged stderr %q names no reference", fail.Stderr)
	}
	if fail.Stdout != "" {
		t.Errorf("the failure's logged stdout %q is not empty", fail.Stdout)
	}
	if !strings.HasPrefix(fail.Command, "$ git log ") {
		t.Errorf("logged command line %q", fail.Command)
	}
	if ok.ExitCode != 0 || ok.Stdout != "" {
		t.Errorf("the success: exit %d stdout %q", ok.ExitCode, ok.Stdout)
	}
	// The streamed output itself is whole, whatever the log drops.
	head := strings.TrimSpace(string(mustRun(t, dir, "rev-parse", "--verify", "HEAD")))
	if !strings.Contains(streamed.String(), head) {
		t.Errorf("the stream misses %q: %q", head, streamed.String())
	}
}

// mustRun returns what git prints in dir, for oracles the test asserts against.
func mustRun(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	out, err := (&LocalExecutor{Dir: dir, NoGlobalConfig: true}).Run(context.Background(), nil, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

// writeCountingWriter records every write it passes through, in order.
type writeCountingWriter struct {
	writes [][]byte
}

func (w *writeCountingWriter) Write(p []byte) (int, error) {
	w.writes = append(w.writes, append([]byte(nil), p...))
	return len(p), nil
}

// staticExecutor answers every command with the same output. It does not
// know the streaming context value, as a custom executor would not.
type staticExecutor struct{ out []byte }

func (e staticExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	return e.out, nil
}

func (e staticExecutor) Describe() string { return "static" }

// TestExecStreamFallbackWriter checks that a command whose executor does not
// stream — it answers the collected output like any executor unaware of the
// streaming context value — still delivers its output to the writer, as one
// write of the whole.
func TestExecStreamFallbackWriter(t *testing.T) {
	payload := []byte("line one\nline two\n")
	r := New(staticExecutor{out: payload})
	w := &writeCountingWriter{}
	if err := r.runStreaming(context.Background(), quick, nil, w, "blame", "x"); err != nil {
		t.Fatalf("runStreaming: %v", err)
	}
	if len(w.writes) != 1 {
		t.Fatalf("%d writes, want the whole output in one", len(w.writes))
	}
	if !bytes.Equal(w.writes[0], payload) {
		t.Errorf("wrote %q, want %q", w.writes[0], payload)
	}
}

