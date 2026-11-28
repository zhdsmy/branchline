package git

import "strconv"

// TerminalCommand returns the argv of an interactive login to the server
// that lands in the repository's directory and stays in the user's login
// shell, for Open in Terminal. The first element is the ssh program: SSH
// when set, otherwise the ssh of this Mac — or the bare name "ssh" when no
// ssh is installed, which the terminal's own PATH then resolves.
//
// Unlike SSHArgs this carries no BatchMode, StrictHostKeyChecking,
// ConnectTimeout or ServerAliveInterval: the session belongs to the user,
// who answers ssh's own questions in the terminal with their own ssh
// configuration. Connection sharing is reuse-only: ControlMaster=no never
// creates a master — the terminal session must not become the master of a
// connection the app owns — while ControlPath joins one the app already
// holds, so a logged-in user is not asked for the password again.
func (s *SSHExecutor) TerminalCommand() []string {
	prog := s.SSH
	if prog == "" {
		prog = sshProgram()
	}
	args := []string{prog, "-t"}
	if !s.NoMultiplex {
		args = append(args,
			"-o", "ControlMaster=no",
			"-o", "ControlPath="+s.ControlPath(),
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
	// A plain ";" keeps the user in a shell even when the directory is
	// gone: cd's error stays on the screen and the exec still runs. The
	// login shell may be zsh, fish or tcsh, so the script runs through
	// /bin/sh, as every server command of this package does.
	script := "cd " + RemotePath(s.Dir) + "; exec \"${SHELL:-/bin/sh}\" -l"
	return append(args, s.Host, remoteShellCommand(script))
}
