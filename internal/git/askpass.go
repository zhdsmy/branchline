package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Prompter asks the user for what git or ssh needs to go on: a password,
// the passphrase of a key, a user name, or a yes or no. ok is false when
// the user cancelled.
type Prompter func(prompt string) (answer string, ok bool)

// Question is what git or ssh asks. CanRemember is set when the answer
// can be saved in the keychain.
type Question struct {
	Prompt      string
	CanRemember bool
}

// Reply is the user's answer. Remember asks to save it in the keychain.
type Reply struct {
	Answer   string
	OK       bool
	Remember bool
}

// scriptFor is the askpass script for where it runs: L on this Mac, R on
// a server.
func scriptFor(where string) string { return strings.Replace(askScript, "@WHERE@", where, 1) }

// askMarker starts the line an askpass script writes on standard error to
// ask for an answer. The record separator keeps it from any real output.
const askMarker = "\x1eBRANCHLINE-ASKPASS "

// askScript is the program git and ssh run as GIT_ASKPASS and SSH_ASKPASS.
// It makes a named pipe, says on standard error where it waits and what
// it asks, then reads the answer from the pipe. The answer goes through
// the pipe only and is never written to a file. A line starting with +
// is the answer; anything else cancels.
const askScript = `#!/bin/sh
# Branchline askpass
r=$(mktemp -u "${TMPDIR:-/tmp}/branchline-ap.XXXXXXXX") || exit 1
r=$(printf '%s' "$r" | tr -s /)
mkfifo -m 600 "$r" || exit 1
trap 'rm -f "$r"' EXIT
trap 'exit 1' INT TERM HUP
p=$(printf '%s' "$1" | tr '\n\r\036' '   ')
printf '\036BRANCHLINE-ASKPASS @WHERE@ %s %s\n' "$r" "$p" >&2
IFS= read -r a < "$r" || exit 1
case $a in
+*) printf '%s\n' "${a#+}" ;;
*) exit 1 ;;
esac
`

// AskPass lets git and ssh ask the user through Prompt when they need a
// secret, on this Mac and on a server reached over SSH. Only one question
// is shown at a time.
type AskPass struct {
	Prompt Prompter
	// Ask, when set, is asked instead of Prompt, and can say to remember
	// the answer.
	Ask func(Question) Reply
	// Secrets, when set, keeps the answers to remember, and is looked in
	// before the user is asked.
	Secrets SecretStore

	once   sync.Once
	dir    string
	script string
	err    error
	mu     sync.Mutex
}

// NewAskPass returns an AskPass that asks p.
func NewAskPass(p Prompter) *AskPass { return &AskPass{Prompt: p} }

// scriptPath writes the askpass script, once, in a folder only this user
// can read.
func (a *AskPass) scriptPath() (string, error) {
	a.once.Do(func() {
		dir, err := os.MkdirTemp("", "branchline-askpass")
		if err != nil {
			a.err = err
			return
		}
		path := filepath.Join(dir, "askpass.sh")
		if err := os.WriteFile(path, []byte(scriptFor("L")), 0o700); err != nil {
			a.err = err
			return
		}
		a.dir, a.script = dir, path
	})
	return a.script, a.err
}

// Close removes the askpass script.
func (a *AskPass) Close() {
	if a != nil && a.dir != "" {
		os.RemoveAll(a.dir)
	}
}

// Env is what a program on this Mac needs to ask through the script:
// git reads GIT_ASKPASS, and ssh SSH_ASKPASS when SSH_ASKPASS_REQUIRE
// is force.
func (a *AskPass) Env() ([]string, error) {
	path, err := a.scriptPath()
	if err != nil {
		return nil, err
	}
	return askEnv(path), nil
}

func askEnv(path string) []string {
	display := os.Getenv("DISPLAY")
	if display == "" {
		display = "branchline:0"
	}
	return []string{"GIT_ASKPASS=" + path, "SSH_ASKPASS=" + path, "SSH_ASKPASS_REQUIRE=force", "DISPLAY=" + display}
}

// askUser asks the user, one question at a time.
func (a *AskPass) askUser(q Question) Reply {
	if a == nil || a.Prompt == nil && a.Ask == nil {
		return Reply{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Ask != nil {
		return a.Ask(q)
	}
	answer, ok := a.Prompt(q.Prompt)
	return Reply{Answer: answer, OK: ok}
}

func (a *AskPass) secrets() SecretStore {
	if a == nil {
		return nil
	}
	return a.Secrets
}

// replyLine is what the script reads from its pipe.
func replyLine(answer string, ok bool) []byte {
	if !ok || strings.ContainsAny(answer, "\n\r") {
		return []byte("-\n")
	}
	return []byte("+" + answer + "\n")
}

// validPipe reports a pipe path the script could have made, so that a
// line on standard error cannot make the app write elsewhere.
func validPipe(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return false
	}
	if !strings.HasPrefix(filepath.Base(path), "branchline-ap.") {
		return false
	}
	for _, r := range path {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+", r)) {
			return false
		}
	}
	return true
}

// writePipe gives the answer to a script on this Mac. The script opens
// its end after asking, so opening is tried again for a while.
func writePipe(path string, line []byte) error {
	if !validPipe(path) {
		return errors.New("askpass: unexpected pipe " + path)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			f := os.NewFile(uintptr(fd), path)
			_, err = f.Write(line)
			f.Close()
			return err
		}
		if !errors.Is(err, syscall.ENXIO) && !errors.Is(err, syscall.ENOENT) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// askWriter is the standard error of a command that can ask: it keeps
// what the command prints, and hands each question to answer, which
// runs on its own goroutine.
type askWriter struct {
	mu      sync.Mutex
	out     bytes.Buffer
	line    []byte
	answer  func(pipe, prompt string, remote bool)
	pending sync.WaitGroup
}

func (w *askWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range p {
		w.line = append(w.line, b)
		if b == '\n' {
			w.flushLine()
		}
	}
	return len(p), nil
}

func (w *askWriter) flushLine() {
	line := w.line
	w.line = nil
	i := bytes.Index(line, []byte(askMarker))
	if i < 0 {
		w.out.Write(line)
		return
	}
	w.out.Write(line[:i])
	rest := strings.TrimRight(string(line[i+len(askMarker):]), "\n")
	where, rest, _ := strings.Cut(rest, " ")
	pipe, prompt, _ := strings.Cut(rest, " ")
	if !validPipe(pipe) || where != "L" && where != "R" {
		return
	}
	w.pending.Add(1)
	go func() {
		defer w.pending.Done()
		w.answer(pipe, prompt, where == "R")
	}()
}

// text is what the command printed, without the questions.
func (w *askWriter) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.line) > 0 {
		w.flushLine()
	}
	return w.out.String()
}

// asksUser reports a git command that can need a password: those that
// reach a remote, and scripts of custom commands.
func asksUser(args []string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" || args[i] == "-C" {
			i++
			continue
		}
		switch args[i] {
		case "fetch", "pull", "push", "clone", "ls-remote", "remote", "submodule", "lfs", "credential", "branchlinescript":
			return true
		}
		return false
	}
	return false
}

// remoteAskSetup writes the askpass script on the server for one command,
// in a file only the login can read; remoteAskCleanup removes it. The
// script holds no secret.
var (
	remoteAskSetup = `__bl_ap=$(mktemp "${TMPDIR:-/tmp}/branchline-askpass.XXXXXXXX") && printf '%s' ` + ShellQuote(scriptFor("R")) +
		` > "$__bl_ap" && chmod 700 "$__bl_ap" && GIT_ASKPASS="$__bl_ap" SSH_ASKPASS="$__bl_ap" SSH_ASKPASS_REQUIRE=force DISPLAY="${DISPLAY:-branchline:0}"`
	remoteAskCleanup = `; __bl_s=$?; rm -f "$__bl_ap"; exit $__bl_s`
)

// env is the environment of the ssh program on this Mac.
func (s *SSHExecutor) env() []string {
	env := commandEnv(s.Path, Trace{})
	if s.AskPass != nil {
		if extra, err := s.AskPass.Env(); err == nil {
			env = append(env, extra...)
		}
	}
	return env
}

// run runs the ssh program, answering what ssh asks on this Mac and what
// the askpass script asks on the server.
func (s *SSHExecutor) run(cmd *exec.Cmd, stdin []byte, args []string, creds []Credential) ([]byte, error) {
	return s.runContext(context.Background(), cmd, stdin, args, creds)
}

func (s *SSHExecutor) runContext(ctx context.Context, cmd *exec.Cmd, stdin []byte, args []string, creds []Credential) ([]byte, error) {
	if s.AskPass == nil {
		return runContext(ctx, cmd, stdin, args)
	}
	sess := s.AskPass.session(creds)
	out, err := runAskContext(ctx, cmd, stdin, args, func(pipe, prompt string, remote bool) {
		answer, ok := sess.ask(prompt)
		line := replyLine(answer, ok)
		if !remote {
			writePipe(pipe, line)
			return
		}
		s.replyRemote(pipe, line)
	})
	sess.finish(err)
	return out, err
}

// replyRemote gives the answer to the script on the server, through its
// pipe, over another channel of the connection. The answer travels on
// the standard input of ssh.
func (s *SSHExecutor) replyRemote(pipe string, line []byte) error {
	if !validPipe(pipe) {
		return errors.New("askpass: unexpected pipe " + pipe)
	}
	prog := s.SSH
	if prog == "" {
		prog = sshProgram()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, prog, s.SSHArgs("cat > "+ShellQuote(pipe))...)
	cmd.Env = s.env()
	_, err := s.run(cmd, line, []string{"ssh"}, nil)
	return err
}
