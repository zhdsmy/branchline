package git

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitCapabilitiesSSHRealServer(t *testing.T) {
	srv := startSSHD(t)
	dir := testRepo(t)

	log := NewCommandLog()
	sshRepo := New(srv.executor(t, dir)).WithCommandLog(log)
	localRepo := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

	remoteRaw, err := sshRepo.Version()
	must(t, err)
	caps, err := sshRepo.Capabilities()
	must(t, err)

	localCaps, err := localRepo.Capabilities()
	must(t, err)

	if caps.RawVersion != remoteRaw {
		t.Fatalf("SSH RawVersion = %q, want %q", caps.RawVersion, remoteRaw)
	}
	if caps != localCaps {
		t.Fatalf("SSH capabilities on loopback = %+v, want %+v", caps, localCaps)
	}
	entries, _ := log.Snapshot()
	if len(entries) < 2 || entries[len(entries)-1].Command != "$ git --version" {
		t.Fatalf("SSH command log = %+v, want trailing '$ git --version'", entries)
	}
}

func TestGitCapabilitiesSSHUsesRemoteExecutorNotLocal(t *testing.T) {
	srv := startSSHD(t)
	dir := testRepo(t)

	// Place a fake git on the remote side and inject its bin directory into the
	// remote command's PATH through an SSH client wrapper, proving that
	// Repo.Capabilities() reads the remote SSH executor's git rather than the
	// local machine's git executable.
	remoteBin := t.TempDir()
	remoteGit := filepath.Join(remoteBin, "git")
	remoteMarker := filepath.Join(remoteBin, "invoked")
	remoteGitScript := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + ShellQuote(remoteMarker) + "\n" +
		"echo 'git version 2.25.1 (Ubuntu 2.25.1-1ubuntu3.13)'\n"
	must(t, os.WriteFile(remoteGit, []byte(remoteGitScript), 0o755))

	sshWrapper := filepath.Join(t.TempDir(), "ssh-remote-path")
	// Rewrite the last argument (remote command) to prepend remoteBin to PATH on the server.
	wrapperScript := "#!/bin/sh\n" +
		"last=\"\"\n" +
		"for arg in \"$@\"; do last=\"$arg\"; done\n" +
		"set -- \"$@\"\n" +
		"args=\"\"\n" +
		"while [ $# -gt 1 ]; do\n" +
		"  args=\"$args '$1'\"\n" +
		"  shift\n" +
		"done\n" +
		"eval \"set -- $args\"\n" +
		"exec " + ShellQuote(srv.ssh) + " \"$@\" \"PATH=" + remoteBin + ":\\$PATH; $last\"\n"
	must(t, os.WriteFile(sshWrapper, []byte(wrapperScript), 0o755))

	ex := srv.executor(t, dir)
	ex.SSH = sshWrapper
	ex.NoMultiplex = true

	sshRepo := New(ex)
	localRepo := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

	sshCaps, err := sshRepo.Capabilities()
	must(t, err)
	localCaps, err := localRepo.Capabilities()
	must(t, err)

	wantSSH := GitCapabilities{
		Version:           GitVersion{Major: 2, Minor: 25, Patch: 1},
		RawVersion:        "git version 2.25.1 (Ubuntu 2.25.1-1ubuntu3.13)",
		MeetsMinimum:      true,
		RebaseUpdateRefs:  false,
		StashStaged:       false,
		InitInitialBranch: false,
	}
	if sshCaps != wantSSH {
		t.Fatalf("SSH capabilities = %+v, want %+v", sshCaps, wantSSH)
	}
	if localCaps.Version == sshCaps.Version {
		t.Fatalf("expected local Git (%+v) to differ from remote stubbed Git (%+v)", localCaps.Version, sshCaps.Version)
	}
	invoked, err := os.ReadFile(remoteMarker)
	must(t, err)
	if !strings.Contains(string(invoked), "--version") {
		t.Fatalf("remote git marker = %q, want --version invocation", invoked)
	}
}

func TestGitCapabilitiesSSHTransportErrorPreserved(t *testing.T) {
	srv := startSSHD(t)
	dir := testRepo(t)

	// Allocate an unused loopback port and close it immediately so ssh fails with exit 255.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	closedPort := l.Addr().(*net.TCPAddr).Port
	must(t, l.Close())

	ex := srv.executor(t, dir)
	ex.Port = closedPort
	ex.NoMultiplex = true

	caps, err := New(ex).Capabilities()
	if err == nil {
		t.Fatal("Capabilities() succeeded against closed SSH port")
	}
	if caps != (GitCapabilities{}) {
		t.Fatalf("Capabilities() on SSH failure = %+v, want zero value", caps)
	}
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("errors.As(%v, *Error) = false; transport error type lost", err)
	}
	if ge.ExitCode != 255 || ExitCode(err) != 255 {
		t.Fatalf("ExitCode = %d (ge.ExitCode = %d), want 255 for SSH transport error: %v", ExitCode(err), ge.ExitCode, err)
	}
}
