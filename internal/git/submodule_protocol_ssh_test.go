package git_test

import (
	"path"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// On a configured Linux SSH server (including Git 2.25), a recursive
// SubmoduleAddAt refuses a nested local-path submodule from .gitmodules
// while direct user-typed local-path additions still succeed.
func TestConfiguredLinuxSubmoduleProtocol(t *testing.T) {
	srv := sshtest.ConfiguredLinux(t)

	inner := srv.InitRepo(t, "subproto-inner")
	srv.WriteFile(t, inner, "inner.txt", []byte("inner\n"))
	srv.RunGit(t, inner, "add", "inner.txt")
	srv.RunGit(t, inner, "commit", "-q", "-m", "inner")

	middle := srv.InitRepo(t, "subproto-middle")
	srv.WriteFile(t, middle, "middle.txt", []byte("middle\n"))
	srv.RunGit(t, middle, "add", "middle.txt")
	srv.RunGit(t, middle, "commit", "-q", "-m", "middle")
	srv.RunGit(t, middle, "-c", "protocol.file.allow=always", "submodule", "add", "--", inner, "deep")
	srv.RunGit(t, middle, "commit", "-q", "-m", "add inner")

	target := srv.InitRepo(t, "subproto-target")
	repo := srv.Repo(t, target)
	err := repo.SubmoduleAddAt(middle, "vendor/", true)
	if err == nil {
		t.Fatal("recursive SubmoduleAddAt cloned a nested local-path submodule")
	}
	t.Logf("remote recursive SubmoduleAddAt error verbatim: %v", err)
	if !strings.Contains(err.Error(), "transport 'file' not allowed") {
		t.Fatalf("error is not git's file transport refusal: %v", err)
	}
	outerPath := path.Join(target, "vendor", path.Base(middle))
	if got := strings.TrimSpace(srv.RunShell(t, "test -f "+git.ShellQuote(path.Join(outerPath, "middle.txt"))+" && echo yes || echo no")); got != "yes" {
		t.Fatalf("outer submodule middle.txt present = %q, want yes", got)
	}
	if got := strings.TrimSpace(srv.RunShell(t, "test -f "+git.ShellQuote(path.Join(outerPath, "deep", "inner.txt"))+" && echo yes || echo no")); got != "no" {
		t.Fatalf("nested submodule inner.txt present = %q, want no", got)
	}

	plain := srv.InitRepo(t, "subproto-plain")
	if err := srv.Repo(t, plain).SubmoduleAddAt(middle, "x/", false); err != nil {
		t.Fatalf("non-recursive SubmoduleAddAt failed for user-typed path: %v", err)
	}
	if got := strings.TrimSpace(srv.RunShell(t, "test -f "+git.ShellQuote(path.Join(plain, "x", path.Base(middle), "middle.txt"))+" && echo yes || echo no")); got != "yes" {
		t.Fatalf("non-recursive outer submodule middle.txt present = %q, want yes", got)
	}

	direct := srv.InitRepo(t, "subproto-direct")
	if err := srv.Repo(t, direct).SubmoduleAdd(inner, "vendor/inner"); err != nil {
		t.Fatalf("direct SubmoduleAdd failed for user-typed path: %v", err)
	}
	if got := strings.TrimSpace(srv.RunShell(t, "test -f "+git.ShellQuote(path.Join(direct, "vendor", "inner", "inner.txt"))+" && echo yes || echo no")); got != "yes" {
		t.Fatalf("direct submodule inner.txt present = %q, want yes", got)
	}
}
