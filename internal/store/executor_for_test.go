package store

import (
	"reflect"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
)

// isolateSSHKeyTestHome keeps every test of this file away from the real
// home directory and the user's ssh-agent: the keys the settings name are
// never real ones, and no ssh an executor built here could start may reach
// the agent.
func isolateSSHKeyTestHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
}

// ExecutorFor of a local repository hands every toolchain setting to the
// LocalExecutor it builds: the custom git program, the trace switches, the
// PATH choice and the active SSH keys.
func TestExecutorForLocalPassesEverySetting(t *testing.T) {
	isolateSSHKeyTestHome(t)
	r := Repo{Kind: Local, Path: "/repo"}
	s := Settings{
		GitPath:  "/custom/git",
		EnvPath:  "parent",
		TraceGit: true,
		TraceSSH: true,
		SSHKeys:  []string{"/keys/work", "/keys/home"},
	}
	ex, ok := r.ExecutorFor(s).(*git.LocalExecutor)
	if !ok {
		t.Fatalf("ExecutorFor of a local repository is %T, want *git.LocalExecutor", r.ExecutorFor(s))
	}
	if ex.Dir != "/repo" {
		t.Errorf("Dir = %q, want /repo", ex.Dir)
	}
	if ex.Git != "/custom/git" {
		t.Errorf("Git = %q, want /custom/git", ex.Git)
	}
	if ex.Trace != (git.Trace{Git: true, SSH: true}) {
		t.Errorf("Trace = %+v, want Git and SSH on", ex.Trace)
	}
	if ex.Path != git.PathMode("parent") {
		t.Errorf("Path = %q, want parent", ex.Path)
	}
	if !reflect.DeepEqual(ex.SSHKeys, []string{"/keys/work", "/keys/home"}) {
		t.Errorf("SSHKeys = %q, want the two active keys", ex.SSHKeys)
	}
}

// ExecutorFor of an SSH repository hands the toolchain settings to the
// SSHExecutor it builds, keeps the repository's own identity key, and offers
// the active SSH keys alongside it — the executor itself prefers the
// identity when it connects.
func TestExecutorForSSHPassesSettingsAndKeepsIdentity(t *testing.T) {
	isolateSSHKeyTestHome(t)
	r := Repo{Kind: SSH, Host: "example.com", User: "me", Port: 2222, Identity: "/keys/server", Path: "/srv/repo"}
	s := Settings{
		GitPath:   "/custom/git",
		EnvPath:   "shell",
		TraceCurl: true,
		SSHKeys:   []string{"/keys/active"},
	}
	ex, ok := r.ExecutorFor(s).(*git.SSHExecutor)
	if !ok {
		t.Fatalf("ExecutorFor of an SSH repository is %T, want *git.SSHExecutor", r.ExecutorFor(s))
	}
	if ex.Host != "example.com" || ex.User != "me" || ex.Port != 2222 || ex.Dir != "/srv/repo" {
		t.Errorf("address fields = %+v, want the repository's own", ex)
	}
	if ex.Identity != "/keys/server" {
		t.Errorf("Identity = %q, want /keys/server", ex.Identity)
	}
	if ex.Trace != (git.Trace{Curl: true}) {
		t.Errorf("Trace = %+v, want Curl on", ex.Trace)
	}
	if ex.Path != git.PathMode("shell") {
		t.Errorf("Path = %q, want shell", ex.Path)
	}
	if !reflect.DeepEqual(ex.SSHKeys, []string{"/keys/active"}) {
		t.Errorf("SSHKeys = %q, want the active key", ex.SSHKeys)
	}
}

// ExecutorFor with the zero settings builds the default executor: the git of
// PATH, no traces, the default PATH choice and no SSH keys — the same shape
// Executor returns.
func TestExecutorForZeroSettingsIsDefault(t *testing.T) {
	isolateSSHKeyTestHome(t)
	r := Repo{Kind: Local, Path: "/repo"}
	ex := r.ExecutorFor(Settings{}).(*git.LocalExecutor)
	if ex.Git != "" || ex.Trace != (git.Trace{}) || ex.Path != git.PathMode("") || len(ex.SSHKeys) != 0 {
		t.Errorf("zero-settings executor = %+v, want the default toolchain", ex)
	}
	if !reflect.DeepEqual(r.Executor(), ex) {
		t.Errorf("Executor() = %#v, differs from ExecutorFor of the zero settings %#v", r.Executor(), ex)
	}
}

// The active keys an executor offers do not follow later edits of the
// settings slice it was built from: the executor offers what was active when
// it was built.
func TestExecutorForCopiesSSHKeys(t *testing.T) {
	isolateSSHKeyTestHome(t)
	r := Repo{Kind: Local, Path: "/repo"}
	s := Settings{SSHKeys: []string{"/keys/before"}}
	ex := r.ExecutorFor(s).(*git.LocalExecutor)
	s.SSHKeys[0] = "/keys/after"
	if !reflect.DeepEqual(ex.SSHKeys, []string{"/keys/before"}) {
		t.Errorf("SSHKeys = %q after editing the settings slice, want the original /keys/before", ex.SSHKeys)
	}
}
