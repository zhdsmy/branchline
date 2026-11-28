package git_test

// Names starting with = are legal for files, branches, tags and directories.
// A server's login shell can be zsh, which expands =word at the start of a
// word into the path of a command, so these names must reach git quoted.
// The loopback sshd runs commands through this Mac's login shell — zsh on
// macOS — which makes the expansion real rather than simulated.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

func TestNameQuotingShellQuote(t *testing.T) {
	// Words starting with = are quoted; = anywhere else keeps the word bare,
	// so command lines that use -c key=value or a=b arguments do not change.
	wantQuoted := []string{"=ls", "=", "=ls.txt", "=v1", "=x=1"}
	wantBare := []string{"a=b", "-c", "x=1", "main", "refs/heads/a:refs/heads/a", "user@host", "./=ls", "/srv/=ls"}
	for _, in := range wantQuoted {
		got := git.ShellQuote(in)
		if got == in || !strings.HasPrefix(got, "'") {
			t.Errorf("ShellQuote(%q) = %s, want quoting", in, got)
		}
	}
	for _, in := range wantBare {
		if got := git.ShellQuote(in); got != in {
			t.Errorf("ShellQuote(%q) = %s, want unchanged", in, got)
		}
	}

	// zsh must read every quoted word back as the literal word: no =word
	// expansion. zsh is the login shell this bug comes from; without zsh
	// here the shell round-trip of TestShellQuote still covers POSIX sh.
	if zsh, err := exec.LookPath("zsh"); err == nil {
		for _, in := range wantQuoted {
			out, err := exec.Command(zsh, "-c", "printf %s "+git.ShellQuote(in)).Output()
			if err != nil || string(out) != in {
				t.Errorf("zsh read %s as %q (%v), want %q", git.ShellQuote(in), out, err, in)
			}
		}
		for _, in := range wantBare {
			out, err := exec.Command(zsh, "-c", "printf %s "+in).Output()
			if err != nil || string(out) != in {
				t.Errorf("zsh read bare %s as %q (%v), want %q", in, out, err, in)
			}
		}
	}

	// RemotePath quotes through ShellQuote, so a directory whose name starts
	// with = is covered the same way, under the home or absolute.
	if got := git.RemotePath("=repo"); got != "'=repo'" {
		t.Errorf("RemotePath(%q) = %s, want quoting", "=repo", got)
	}
	if got := git.RemotePath("~/=repo"); got != "~/'=repo'" {
		t.Errorf("RemotePath(%q) = %s, want quoting under home", "~/=repo", got)
	}
}

// runEqualsNamesOverSSH stages a file named =ls.txt and creates a branch
// =ls and a tag =v1 through repo, checking the names with the server's own
// git so the operations never prove themselves.
func runEqualsNamesOverSSH(t *testing.T, srv *sshtest.Target) {
	t.Helper()

	dir := srv.InitRepo(t, "eq-names")
	repo := srv.Repo(t, dir)
	srv.WriteFile(t, dir, "=ls.txt", []byte("x\n"))

	if err := repo.Stage("=ls.txt"); err != nil {
		t.Fatalf("Stage(=ls.txt) over SSH: %v", err)
	}
	if got := srv.RunGit(t, dir, "diff", "--cached", "--name-only"); got != "=ls.txt" {
		t.Fatalf("staged paths = %q, want %q", got, "=ls.txt")
	}

	head := srv.RunGit(t, dir, "rev-parse", "HEAD")
	if err := repo.CreateBranch("=ls", "", false); err != nil {
		t.Fatalf("CreateBranch(=ls) over SSH: %v", err)
	}
	if got := srv.RunGit(t, dir, "rev-parse", "refs/heads/=ls"); got != head {
		t.Fatalf("refs/heads/=ls = %q, want %q", got, head)
	}

	if err := repo.CreateTag("=v1", "HEAD", ""); err != nil {
		t.Fatalf("CreateTag(=v1) over SSH: %v", err)
	}
	if got := srv.RunGit(t, dir, "rev-parse", "refs/tags/=v1"); got != head {
		t.Fatalf("refs/tags/=v1 = %q, want %q", got, head)
	}
}

func TestNameQuotingLoopbackSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)

	t.Run("EqualsNames", func(t *testing.T) {
		runEqualsNamesOverSSH(t, srv)
	})

	t.Run("TopLevelTrailingSpace", func(t *testing.T) {
		checkTopLevelTrailingSpaceSSH(t, srv)
	})
}

func checkTopLevelTrailingSpaceSSH(t *testing.T, srv *sshtest.Target) {
	t.Helper()

	// A repository in a directory whose name ends in a space: the space is
	// part of the name and TopLevel must keep it.
	base := srv.TempDir(t, "eqtop")
	spaced := base + "/repo "
	srv.RunShell(t, "mkdir -- "+git.ShellQuote(spaced)+"\n"+
		"export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1\n"+
		"git init -q -b main "+git.ShellQuote(spaced)+"\n"+
		"git -C "+git.ShellQuote(spaced)+" -c user.name=t -c user.email=t@t commit -q --allow-empty -m init")
	t.Cleanup(func() {
		srv.RunShell(t, "rm -rf -- "+git.ShellQuote(spaced))
	})
	top, err := srv.Repo(t, spaced).TopLevel()
	if err != nil {
		t.Fatalf("TopLevel over SSH in %q: %v", spaced, err)
	}
	if top != spaced {
		t.Fatalf("TopLevel over SSH = %q, want %q (trailing space kept)", top, spaced)
	}

	// A plain directory is unaffected.
	plain := srv.InitRepo(t, "eqtop-plain")
	top, err = srv.Repo(t, plain).TopLevel()
	if err != nil || top != plain {
		t.Fatalf("TopLevel over SSH = (%q, %v), want (%q, nil)", top, err, plain)
	}
}

func TestNameQuotingTopLevelTrailingSpaceLocal(t *testing.T) {
	init := func(dir string) {
		t.Helper()
		cmd := exec.Command("git", "init", "-q", "-b", "main", dir)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init %q: %v\n%s", dir, err, out)
		}
	}

	// A directory whose name ends in a space keeps the space in TopLevel.
	spaced := filepath.Join(t.TempDir(), "repo ")
	init(spaced)
	top, err := git.New(&git.LocalExecutor{Dir: spaced, NoGlobalConfig: true}).TopLevel()
	if err != nil {
		t.Fatalf("TopLevel in %q: %v", spaced, err)
	}
	if !strings.HasSuffix(top, "repo ") {
		t.Fatalf("TopLevel = %q, want a path ending in %q", top, "repo ")
	}
	if resolved, err := filepath.EvalSymlinks(spaced); err == nil && top != resolved && top != spaced {
		t.Fatalf("TopLevel = %q, want %q", top, resolved)
	}

	// A plain directory is unchanged: no trailing whitespace either way.
	plain := filepath.Join(t.TempDir(), "repo")
	init(plain)
	top, err = git.New(&git.LocalExecutor{Dir: plain, NoGlobalConfig: true}).TopLevel()
	if err != nil {
		t.Fatalf("TopLevel in %q: %v", plain, err)
	}
	if strings.HasSuffix(top, " ") || strings.HasSuffix(top, "\n") {
		t.Fatalf("TopLevel = %q ends in whitespace", top)
	}
	if resolved, err := filepath.EvalSymlinks(plain); err == nil && top != resolved && top != plain {
		t.Fatalf("TopLevel = %q, want %q", top, resolved)
	}
}
