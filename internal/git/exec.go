// Package git runs git against a repository on this machine or on a server
// reached over SSH, and parses what it prints into the values the app shows.
package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Executor runs git with args in one repository. stdin, when not nil, is
// given to git on its standard input.
type Executor interface {
	Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error)
	// Describe says where the repository is, for the window and errors.
	Describe() string
}

// Error is a git command that failed: its exit code and what it printed on
// standard error.
type Error struct {
	Args     []string
	ExitCode int
	Stderr   string
	Stdout   []byte
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", e.ExitCode)
	}
	name := "git"
	if len(e.Args) > 0 {
		name += " " + e.Args[0]
	}
	return name + ": " + msg
}

// ExitCode returns the exit code of a failed git command, or -1.
func ExitCode(err error) int {
	var ge *Error
	if errors.As(err, &ge) {
		return ge.ExitCode
	}
	return -1
}

// baseConfig is given to every git command, so that what it prints is the
// same whatever the user's configuration says. The last two entries keep a
// repository's own configuration from running programs: core.fsmonitor
// names a command every worktree scan would execute, which opening a
// repository must never do, and the ext transport runs a command taken
// from the remote's address, which nothing here uses.
var baseConfig = []string{
	"--literal-pathspecs",
	"-c", "core.quotepath=false",
	"-c", "color.ui=false",
	"-c", "log.showSignature=false",
	"-c", "core.pager=cat",
	"-c", "core.fsmonitor=false",
	"-c", "protocol.ext.allow=never",
}

// configFor returns the common Git options. Commands with an explicit glob
// pathspec, or stash commands without a pathspec, must keep Git's default
// pathspec mode: stash invokes a nested clean with the magic root path :/.
func configFor(args []string) []string {
	for _, arg := range args {
		if strings.HasPrefix(arg, ":(glob") {
			return baseConfig[1:]
		}
	}
	if len(args) > 0 && args[0] == "stash" {
		for i, arg := range args[1:] {
			// A message can itself be "--". A pathspec separator always
			// has at least one path after it.
			if arg == "--" && i+2 < len(args) {
				return baseConfig
			}
		}
		return baseConfig[1:]
	}
	return baseConfig
}

// Trace is the set of Git's own traces the Git settings turn on one by one.
// The trace goes to standard error, which a failed command already keeps.
type Trace struct {
	Git  bool // GIT_TRACE
	Curl bool // GIT_TRACE_CURL
	SSH  bool // GIT_SSH_COMMAND=ssh -vvv
	Pack bool // GIT_TRACE_PACKFILE
	Perf bool // GIT_TRACE_PERFORMANCE
}

// AllTraces turns on every trace, as the single verbose option did.
func AllTraces() Trace { return Trace{true, true, true, true, true} }

// Any reports whether a trace is on.
func (t Trace) Any() bool { return t != Trace{} }

// Env is the environment variables of the traces that are on.
func (t Trace) Env() []string {
	var env []string
	if t.Git {
		env = append(env, "GIT_TRACE=1")
	}
	if t.Curl {
		env = append(env, "GIT_TRACE_CURL=1")
	}
	if t.SSH {
		env = append(env, "GIT_SSH_COMMAND=ssh -vvv")
	}
	if t.Pack {
		env = append(env, "GIT_TRACE_PACKFILE=1")
	}
	if t.Perf {
		env = append(env, "GIT_TRACE_PERFORMANCE=1")
	}
	return env
}

func gitEnv(trace Trace) string {
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C"}
	return strings.Join(append(env, trace.Env()...), " ")
}

// PathMode says which PATH a command on this Mac gets.
// "" adds Homebrew to the process PATH, "parent" keeps the process
// PATH, and "shell" uses the login shell's PATH.
type PathMode string

const (
	PathProcessBrew PathMode = ""
	PathProcess     PathMode = "parent"
	PathShell       PathMode = "shell"
)

// LocalExecutor runs git in a directory of this machine.
type LocalExecutor struct {
	Dir string
	// Git is the git program, "git" when empty.
	Git string
	// Trace asks git to trace its own work on standard error.
	Trace Trace
	// Path is the PATH mode for this command. Empty adds Homebrew.
	Path PathMode
	// AskPass, when set, lets git and ssh ask the user for a password or
	// the passphrase of a key.
	AskPass *AskPass
	// Config is more -c settings for every command, after the app's own.
	Config []string
	// Credentials answer for the HTTPS remotes bound to hosting accounts,
	// through AskPass, which must be set.
	Credentials []Credential
	// NoGlobalConfig keeps git from the user's global configuration file:
	// GIT_CONFIG_GLOBAL names an empty file.
	NoGlobalConfig bool
	// SSHKeys are the private keys ssh offers besides the agent's, for
	// the remotes that use SSH.
	SSHKeys []string
}

func (l *LocalExecutor) Describe() string { return l.Dir }

func (l *LocalExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	return recordInvocation(ctx, nil, args, func(ctx context.Context) ([]byte, error) {
		git := l.Git
		if git == "" {
			git = gitProgram()
		}
		full := append([]string{"-C", expandHome(l.Dir)}, configFor(args)...)
		full = append(full, l.Config...)
		creds := l.Credentials
		if l.AskPass == nil || !asksUser(args) {
			creds = nil
		}
		full = append(full, credentialConfig(creds)...)
		full = append(full, args...)
		cmd := exec.CommandContext(ctx, git, full...)
		cmd.Env = commandEnv(l.Path, l.Trace)
		if ctx.Value(preparedEditorsKey{}) == true {
			cmd.Env = setEnv(cmd.Env, "GIT_EDITOR", "true")
			cmd.Env = setEnv(cmd.Env, "GIT_SEQUENCE_EDITOR", "cp /dev/stdin")
		}
		if l.NoGlobalConfig {
			cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL="+os.DevNull)
		}
		if env := SSHKeysEnv(l.SSHKeys, l.Trace); env != "" {
			cmd.Env = append(cmd.Env, env)
		}
		if l.AskPass != nil {
			if env, err := l.AskPass.Env(); err == nil {
				cmd.Env = append(cmd.Env, env...)
				sess := l.AskPass.session(creds)
				out, err := runAskContext(ctx, cmd, stdin, args, func(pipe, prompt string, _ bool) {
					answer, ok := sess.ask(prompt)
					// The callback cannot report an error. When the answer
					// does not reach the pipe, the script keeps waiting on
					// it, and git or ssh keeps waiting on the script until
					// the command's timeout or cancellation kills it.
					_ = writePipe(pipe, replyLine(answer, ok))
				})
				sess.finish(err)
				return out, err
			}
		}
		return runContext(ctx, cmd, stdin, args)
	})
}

// SSHExecutor runs git in a directory of a server, through the system's
// ssh, so that ~/.ssh/config, keys and the agent work as in a terminal.
// Without AskPass it never asks for a password: give the server a key.
type SSHExecutor struct {
	// Host is a host name, an address or a Host of ~/.ssh/config.
	Host string
	// User and Port are those of the login, the configuration's when
	// empty and 0.
	User string
	Port int
	// Identity is a private key file, the configuration's when empty.
	Identity string
	// Dir is the repository's directory on the server; one starting with
	// ~/ is under the login's home.
	Dir string
	// SSH is the ssh program, "ssh" when empty.
	SSH string
	// NoMultiplex turns off sharing one connection between commands.
	NoMultiplex bool
	// Trace asks the server's git to trace its own work.
	Trace Trace
	// Path is the PATH of the ssh program on this Mac. The server keeps its own.
	Path PathMode
	// AskPass, when set, lets ssh ask for the password or passphrase of
	// the login, and git on the server for what its own remotes need.
	AskPass *AskPass
	// Config is more -c settings for every command, after the app's own.
	Config []string
	// Credentials answer for the HTTPS remotes bound to hosting accounts,
	// through AskPass, which must be set.
	Credentials []Credential
	// NoGlobalConfig keeps git from the user's global configuration file:
	// GIT_CONFIG_GLOBAL names an empty file.
	NoGlobalConfig bool
	// SSHKeys are the active private keys the login offers besides the
	// agent's and Identity.
	SSHKeys []string
}

func (s *SSHExecutor) Describe() string {
	return s.Destination() + ":" + s.Dir
}

func (s *SSHExecutor) DirPath() string { return s.Dir }

// Destination returns user@host, or host.
func (s *SSHExecutor) Destination() string {
	if s.User != "" {
		return s.User + "@" + s.Host
	}
	return s.Host
}

// Validate reports a connection that cannot be tried.
func (s *SSHExecutor) Validate() error {
	switch {
	case strings.TrimSpace(s.Host) == "":
		return errors.New("ssh: the host is empty")
	case strings.HasPrefix(s.Host, "-") || strings.HasPrefix(s.User, "-"):
		return errors.New("ssh: the host and the user cannot start with -")
	case strings.ContainsAny(s.Host+s.User, " \t\r\n@"):
		return errors.New("ssh: the host and the user cannot hold spaces or @")
	case s.Port < 0 || s.Port > 65535:
		return errors.New("ssh: the port is out of range")
	case strings.TrimSpace(s.Dir) == "":
		return errors.New("ssh: the repository path is empty")
	}
	return nil
}

// SSHArgs returns the arguments given to ssh to run the remote command.
func (s *SSHExecutor) SSHArgs(remote string) []string {
	batch := "BatchMode=yes"
	if s.AskPass != nil {
		batch = "BatchMode=no"
	}
	args := []string{
		"-o", batch,
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=30",
	}
	if !s.NoMultiplex && runtime.GOOS != "windows" {
		args = append(args,
			"-o", "ControlMaster=auto",
			"-o", "ControlPath="+s.controlPath(),
			"-o", "ControlPersist=600",
		)
	}
	if s.Port > 0 {
		args = append(args, "-p", strconv.Itoa(s.Port))
	}
	if s.Identity != "" {
		args = append(args, "-i", expandHome(s.Identity), "-o", "IdentitiesOnly=yes")
	} else {
		args = append(args, SSHKeyArgs(s.SSHKeys)...)
	}
	if s.User != "" {
		args = append(args, "-l", s.User)
	}
	return append(args, s.Host, remote)
}

// ControlPath is the socket of the shared connection, kept short: a Unix
// socket's path has room for about 100 bytes, and ssh adds to it.
func (s *SSHExecutor) ControlPath() string { return s.controlPath() }

func (s *SSHExecutor) controlPath() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s", s.User, s.Host, s.Port, s.Identity+strings.Join(s.SSHKeys, "\x00"))))
	return "/tmp/branchline-" + hex.EncodeToString(sum[:])[:16]
}

// RemoteCommand returns the shell command the server runs for git args.
// A command that can need credentials gets an askpass script on the
// server while it runs, when AskPass is set.
func (s *SSHExecutor) RemoteCommand(args ...string) string {
	return s.remoteCommand(false, args...)
}

func (s *SSHExecutor) remoteCommand(prepared bool, args ...string) string {
	ask := s.AskPass != nil && asksUser(args)
	var b strings.Builder
	b.WriteString("cd ")
	b.WriteString(RemotePath(s.Dir))
	b.WriteString(" && ")
	if ask {
		b.WriteString(remoteAskSetup)
		b.WriteString(" && ")
	}
	if prepared {
		b.WriteString(`__bl_err=$(mktemp "/tmp/branchline-stderr.XXXXXXXX") && trap 'rm -f "$__bl_err"' EXIT INT TERM HUP && `)
	}
	b.WriteString(gitEnv(s.Trace))
	if prepared {
		b.WriteString(" GIT_EDITOR=true GIT_SEQUENCE_EDITOR='cp /dev/stdin'")
	}
	if s.NoGlobalConfig {
		b.WriteString(" GIT_CONFIG_GLOBAL=/dev/null")
	}
	b.WriteString(" git")
	full := append(append([]string{}, configFor(args)...), s.Config...)
	full = append(full, credentialConfig(s.credentials(args))...)
	for _, a := range full {
		b.WriteByte(' ')
		b.WriteString(ShellQuote(a))
	}
	for i, a := range args {
		b.WriteByte(' ')
		b.WriteString(remoteGitArg(args, i, a))
	}
	if prepared {
		b.WriteString(` 2>"$__bl_err"; __bl_rc=$?; cat "$__bl_err" >&2 || true; rm -f "$__bl_err"`)
		if ask {
			b.WriteString(` "$__bl_ap"`)
		}
		b.WriteString(`; exit $__bl_rc`)
		return b.String()
	}
	if ask {
		b.WriteString(remoteAskCleanup)
	}
	return b.String()
}

func (s *SSHExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	return recordInvocation(ctx, nil, args, func(ctx context.Context) ([]byte, error) {
		if err := s.Validate(); err != nil {
			return nil, err
		}
		prog := s.SSH
		if prog == "" {
			prog = sshProgram()
		}
		cmd := exec.CommandContext(ctx, prog, s.SSHArgs(s.remoteCommand(ctx.Value(preparedEditorsKey{}) == true, args...))...)
		cmd.Env = s.env()
		out, err := s.runContext(ctx, cmd, stdin, args, s.credentials(args))
		var ge *Error
		if errors.As(err, &ge) && ge.ExitCode == 255 {
			// ssh itself failed: say so, as git never exits with 255.
			ge.Stderr = "cannot connect to " + s.Destination() + ": " + strings.TrimSpace(ge.Stderr)
		}
		return out, err
	})
}

// RunShell runs a command of the server's shell in no directory, for
// checking the connection.
func (s *SSHExecutor) RunShell(ctx context.Context, command string) ([]byte, error) {
	return s.RunShellInput(ctx, command, nil)
}

// RunShellInput runs a command of the server's shell and gives it stdin.
// The command is responsible for its own working directory.
func (s *SSHExecutor) RunShellInput(ctx context.Context, command string, stdin []byte) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	prog := s.SSH
	if prog == "" {
		prog = sshProgram()
	}
	cmd := exec.CommandContext(ctx, prog, s.SSHArgs(command)...)
	cmd.Env = s.env()
	configureShellCancel(cmd)
	return s.run(cmd, stdin, []string{"ssh"}, nil)
}

// credentials are those a command gets: only one that can ask, and only
// with AskPass to hand them over.
func (s *SSHExecutor) credentials(args []string) []Credential {
	if s.AskPass == nil || !asksUser(args) {
		return nil
	}
	return s.Credentials
}

// ShellQuote quotes s for a POSIX shell.
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,+@%", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func remoteGitArg(args []string, idx int, a string) string {
	if isWorktreePathArg(args, idx) && (a == "~" || strings.HasPrefix(a, "~/")) {
		return RemotePath(a)
	}
	return ShellQuote(a)
}

func isWorktreePathArg(args []string, idx int) bool {
	if len(args) < 3 || args[0] != "worktree" {
		return false
	}
	dash := -1
	for i, arg := range args {
		if arg == "--" {
			dash = i
			break
		}
	}
	if dash < 0 || idx <= dash {
		return false
	}
	switch args[1] {
	case "add", "remove":
		return idx == dash+1
	case "move":
		return idx == dash+1 || idx == dash+2
	}
	return false
}

// RemotePath quotes a path of the server for its shell, leaving a leading
// ~ or ~/ for the shell to expand to the home directory.
func RemotePath(p string) string {
	switch {
	case p == "~":
		return "~"
	case strings.HasPrefix(p, "~/"):
		return "~/" + ShellQuote(p[2:])
	case strings.HasPrefix(p, "-"):
		return ShellQuote("./" + p)
	}
	return ShellQuote(p)
}

func runContext(ctx context.Context, cmd *exec.Cmd, stdin []byte, args []string) ([]byte, error) {
	var stderr bytes.Buffer
	return runTo(ctx, cmd, stdin, args, &stderr, stderr.String)
}

func runAskContext(ctx context.Context, cmd *exec.Cmd, stdin []byte, args []string, answer func(pipe, prompt string, remote bool)) ([]byte, error) {
	w := &askWriter{answer: answer}
	return runTo(ctx, cmd, stdin, args, w, w.text)
}

func runTo(ctx context.Context, cmd *exec.Cmd, stdin []byte, args []string, stderrW io.Writer, stderrText func() string) ([]byte, error) {
	var stdout bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, stderrW
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	err := cmd.Run()
	if err != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		msg := stderrText()
		if code == -1 && msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), &Error{Args: args, ExitCode: code, Stderr: msg, Stdout: stdout.Bytes()}
	}
	setExecStderr(ctx, stderrText())
	return stdout.Bytes(), nil
}

// SSHKeyArgs are the ssh options that offer keys: each one, and adding it
// to the agent once its passphrase was given.
func SSHKeyArgs(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	var args []string
	for _, k := range keys {
		args = append(args, "-i", expandHome(k))
	}
	return append(args, "-o", "AddKeysToAgent=yes")
}

// SSHKeysEnv is the GIT_SSH_COMMAND that makes the ssh of a local git offer
// keys. It is empty without keys, and the user's own command is kept.
func SSHKeysEnv(keys []string, trace Trace) string {
	if len(keys) == 0 {
		return ""
	}
	cmd := os.Getenv("GIT_SSH_COMMAND")
	if cmd == "" {
		cmd = "ssh"
	}
	if trace.SSH {
		cmd += " -vvv"
	}
	for _, a := range SSHKeyArgs(keys) {
		cmd += " " + ShellQuote(a)
	}
	return "GIT_SSH_COMMAND=" + cmd
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return h + p[1:]
		}
	}
	return p
}

// commandEnv is the process environment with the chosen PATH, and Git's
// own trace variables when verbose is set.
func commandEnv(mode PathMode, trace Trace) []string {
	env := os.Environ()
	if path, ok := resolvePath(mode); ok {
		env = setEnv(env, "PATH", path)
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	return append(env, trace.Env()...)
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := env[:0:0]
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return append(out, prefix+value)
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return env[i][len(prefix):]
		}
	}
	return ""
}

// resolvePath returns the PATH for mode. The bool is false when the
// process PATH should be left as it is and no extra lookup is needed.
func resolvePath(mode PathMode) (string, bool) {
	switch mode {
	case PathProcess:
		return "", false
	case PathShell:
		if path := loginShellPath(); path != "" {
			return path, true
		}
		return "", false
	default:
		return withBrew(os.Getenv("PATH")), true
	}
}

func withBrew(path string) string {
	extra := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	have := map[string]bool{}
	for _, dir := range strings.Split(path, string(os.PathListSeparator)) {
		have[dir] = true
	}
	var add []string
	for _, dir := range extra {
		if !have[dir] {
			add = append(add, dir)
		}
	}
	if len(add) == 0 {
		return path
	}
	if path == "" {
		return strings.Join(add, string(os.PathListSeparator))
	}
	return strings.Join(add, string(os.PathListSeparator)) + string(os.PathListSeparator) + path
}

// loginShellPath asks the user's login shell for its PATH, the way a
// terminal started by the shell sees it.
func loginShellPath() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	if st, err := os.Stat(shell); err != nil || st.IsDir() {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-l", "-c", "printf %s \"$PATH\"")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Apps started from the Finder get a short PATH: look where git and ssh
// are installed too.
func gitProgram() string {
	return findProgram("git", "/opt/homebrew/bin/git", "/usr/local/bin/git", "/usr/bin/git")
}
func sshProgram() string {
	return findProgram("ssh", "/usr/bin/ssh", "/opt/homebrew/bin/ssh", "/usr/local/bin/ssh")
}

func findProgram(name string, candidates ...string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return name
}

// CommandEnv is the environment a program on this Mac gets for the PATH mode.
func CommandEnv(mode PathMode, trace Trace) []string { return commandEnv(mode, trace) }

// ResolvePath returns the PATH for mode. The bool is false when the process PATH stays.
func ResolvePath(mode PathMode) (string, bool) { return resolvePath(mode) }
