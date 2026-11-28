package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"
)

// Repo is a repository and the git commands the app runs in it.
type Repo struct {
	Exec   Executor
	cmdLog *CommandLog
	// ctx is the parent of every command's context, context.Background()
	// until WithContext replaces it. Canceling it stops the commands the
	// copy runs, never those of the Repo it was copied from.
	ctx context.Context
}

// New returns the repository ex runs git in.
func New(ex Executor) *Repo { return &Repo{Exec: ex} }

// WithCommandLog returns a shallow copy of r whose Git invocations are
// recorded in l. It does not mutate r or r.Exec.
func (r *Repo) WithCommandLog(l *CommandLog) *Repo {
	if r == nil {
		return nil
	}
	copy := *r
	copy.cmdLog = l
	return &copy
}

// WithExecutor returns a new Repo backed by ex while preserving r's
// CommandLog and context.
func (r *Repo) WithExecutor(ex Executor) *Repo {
	if r == nil {
		return New(ex)
	}
	copy := *r
	copy.Exec = ex
	return &copy
}

// WithContext returns a shallow copy of r whose commands run under ctx:
// each command's timeout is still added on top, and canceling ctx stops
// the copy's commands in flight. A nil ctx resets the copy to
// context.Background(). r itself is not affected.
func (r *Repo) WithContext(ctx context.Context) *Repo {
	if r == nil {
		return nil
	}
	copy := *r
	copy.ctx = ctx
	return &copy
}

// baseCtx is the parent every command's timeout context derives from.
func (r *Repo) baseCtx() context.Context {
	if r == nil || r.ctx == nil {
		return context.Background()
	}
	return r.ctx
}

// CommandLog returns the CommandLog bound to r, if any.
func (r *Repo) CommandLog() *CommandLog {
	if r == nil {
		return nil
	}
	return r.cmdLog
}

// WithoutPrompts uses an existing SSH connection, keys or agent without
// opening credential dialogs. It does not change the interactive executor.
func (r *Repo) WithoutPrompts() *Repo {
	if r == nil {
		return nil
	}
	switch ex := r.Exec.(type) {
	case *SSHExecutor:
		copy := *ex
		copy.AskPass = nil
		return r.WithExecutor(&copy)
	case *LocalExecutor:
		copy := *ex
		copy.AskPass = nil
		return r.WithExecutor(&copy)
	default:
		return r
	}
}

const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

const (
	quick = 30 * time.Second
	// fetch, pull, push: the network. Also file history and blame in large
	// repositories, where git log -- path and git blame can take minutes.
	slow = 10 * time.Minute
)

// Run runs a git command whose arguments come from a custom command.
func (r *Repo) Run(args ...string) error {
	if len(args) == 0 {
		return errors.New("git needs a command")
	}
	_, err := r.run(slow, args...)
	return err
}

func (r *Repo) run(timeout time.Duration, args ...string) ([]byte, error) {
	return r.runInput(timeout, nil, args...)
}

func (r *Repo) runInput(timeout time.Duration, stdin []byte, args ...string) ([]byte, error) {
	return r.runContext(r.baseCtx(), timeout, stdin, args...)
}

// runPrepared keeps Git's prepared messages and supplies our sequence editor.
// The policy is scoped to this invocation, including on an SSH server.
func (r *Repo) runPrepared(stdin []byte, args ...string) ([]byte, error) {
	return r.runContext(context.WithValue(r.baseCtx(), preparedEditorsKey{}, true), slow, stdin, args...)
}

type preparedEditorsKey struct{}

func (r *Repo) runContext(parent context.Context, timeout time.Duration, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return recordInvocation(ctx, r.cmdLog, args, func(runCtx context.Context) ([]byte, error) {
		out, err := r.Exec.Run(runCtx, stdin, args...)
		if ctx.Err() == context.DeadlineExceeded {
			return out, fmt.Errorf("git %s: timed out after %s", args[0], timeout)
		}
		return out, err
	})
}

// countingWriter counts what it passes through, atomically: a canceled
// command's copy of the output can outlive the wait for it and write after
// the caller has read the count.
type countingWriter struct {
	w io.Writer
	n atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// runStreaming runs like runContext, with the command's standard output
// written to w as it is produced instead of being collected. An executor
// that does not know the streaming context value returns the output whole;
// then it is written to w in a single write after the command, which the
// zero count of streamed bytes tells apart from a streamed command.
func (r *Repo) runStreaming(parent context.Context, timeout time.Duration, stdin []byte, w io.Writer, args ...string) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cw := &countingWriter{w: w}
	_, err := recordInvocation(ctx, r.cmdLog, args, func(runCtx context.Context) ([]byte, error) {
		out, err := r.Exec.Run(streamWriter(runCtx, cw), stdin, args...)
		if len(out) > 0 && cw.n.Load() == 0 {
			_, _ = w.Write(out)
		}
		if ctx.Err() == context.DeadlineExceeded {
			return out, fmt.Errorf("git %s: timed out after %s", args[0], timeout)
		}
		if ctx.Err() == context.Canceled {
			// The cancellation's kill ends the process with a signal, which
			// reads as an exit error; the caller asks for the cancellation.
			return out, context.Canceled
		}
		return out, err
	})
	return err
}

// TopLevel returns the root of the working tree, and checks that the
// directory is in one.
func (r *Repo) TopLevel() (string, error) {
	out, err := r.run(quick, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	// Only the line's newline goes: a name ending in a space is a valid
	// directory of both this machine and a server, and trimming it would
	// point at a directory that is not the repository's.
	return strings.TrimRight(string(out), "\r\n"), nil
}

// Version returns what git --version prints.
func (r *Repo) Version() (string, error) {
	out, err := r.run(quick, "--version")
	return strings.TrimSpace(string(out)), err
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}
