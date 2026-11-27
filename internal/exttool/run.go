package exttool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// outputLimit is how much of the tool's standard output and standard error
// is kept: the last 64 KiB of each.
const outputLimit = 64 * 1024

// Vars are the absolute paths of the snapshot files handed to the tool.
// They reach the tool through the environment only: diff tools receive
// LOCAL and REMOTE, merge tools also BASE and MERGED. Which of them the
// command quotes is the preset's decision.
type Vars struct {
	Local  string
	Remote string
	Base   string
	Merged string
}

// ExitError is a tool that ran and exited with a failing status: its exit
// code and the end of what it printed on standard error.
type ExitError struct {
	ExitCode int
	Stderr   string
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg != "" {
		return fmt.Sprintf("external tool exited with status %d: %s", e.ExitCode, msg)
	}
	return fmt.Sprintf("external tool exited with status %d", e.ExitCode)
}

// RunDiff runs the tool's diff command through /bin/sh and waits for it.
// Exit codes 0 and 1 both mean success, because many diff tools exit 1 when
// the two sides differ. Any other exit, or a shell that cannot start, is an
// error carrying the end of the tool's standard error. Cancelling ctx ends
// the tool and returns an error.
func RunDiff(ctx context.Context, t Tool, v Vars) error {
	return run(ctx, t, v, true)
}

// RunMerge runs the tool's merge command through /bin/sh and waits for it.
// Only exit code 0 means success; anything else is an *ExitError with the
// exit code and the end of the tool's standard error. Cancelling ctx ends
// the tool and returns an error.
func RunMerge(ctx context.Context, t Tool, v Vars) error {
	return run(ctx, t, v, false)
}

func run(ctx context.Context, t Tool, v Vars, diff bool) error {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", t.Command)
	cmd.Env = toolEnv(v)
	// nil stdin is /dev/null: a tool reading its standard input gets end of
	// file instead of the app's.
	cmd.Stdin = nil
	stdout := &tailWriter{}
	stderr := &tailWriter{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// The command is a shell that can start children of its own — FileMerge
	// behind a pipe, or a stub launching the real application. Killing only
	// the shell on cancellation would leave those children running, so the
	// shell gets its own process group and the cancel function ends the
	// whole group. WaitDelay bounds the wait in case a grandchild keeps the
	// output pipes open after the group is gone.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("external tool %s: %w", t.Name, ctx.Err())
	}
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return fmt.Errorf("external tool %s: %w", t.Name, err)
	}
	code := exitErr.ExitCode()
	if diff && code == 1 {
		return nil
	}
	return &ExitError{ExitCode: code, Stderr: stderr.String()}
}

// toolEnv is the process environment plus the four snapshot paths. Any
// earlier variable of the same name is replaced, never duplicated.
func toolEnv(v Vars) []string {
	vars := map[string]string{
		"LOCAL":  v.Local,
		"REMOTE": v.Remote,
		"BASE":   v.Base,
		"MERGED": v.Merged,
	}
	env := make([]string, 0, len(os.Environ())+len(vars))
	for _, pair := range os.Environ() {
		name, _, _ := strings.Cut(pair, "=")
		if _, overridden := vars[name]; !overridden {
			env = append(env, pair)
		}
	}
	for _, name := range []string{"LOCAL", "REMOTE", "BASE", "MERGED"} {
		env = append(env, name+"="+vars[name])
	}
	return env
}

// tailWriter keeps only the last outputLimit bytes written to it.
type tailWriter struct {
	buf []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	if len(p) >= outputLimit {
		w.buf = append([]byte(nil), p[len(p)-outputLimit:]...)
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	if len(w.buf) > outputLimit {
		w.buf = append([]byte(nil), w.buf[len(w.buf)-outputLimit:]...)
	}
	return len(p), nil
}

func (w *tailWriter) String() string {
	return string(w.buf)
}
