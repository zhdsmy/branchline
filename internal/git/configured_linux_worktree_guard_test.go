package git_test

import (
	"context"
	"path"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

type linuxOthersCounter struct {
	inner   git.Executor
	entries int
	bytes   int
}

func (c *linuxOthersCounter) Describe() string { return c.inner.Describe() }

func (c *linuxOthersCounter) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	out, err := c.inner.Run(ctx, stdin, args...)
	for i, a := range args {
		if a == "ls-files" && i+1 < len(args) && args[i+1] == "--others" {
			c.bytes += len(out)
			for _, entry := range strings.Split(string(out), "\x00") {
				if entry != "" {
					c.entries++
				}
			}
			break
		}
	}
	return out, err
}

func TestConfiguredLinuxWorktreeGuard(t *testing.T) {
	srv := sshtest.ConfiguredLinux(t)

	t.Run("ObstructionsAndCollapsedParentScan", func(t *testing.T) {
		dir := srv.InitRepo(t, "wg-linux-obs")
		srv.RunShell(t, strings.Join([]string{
			"set -eu",
			"cd " + git.ShellQuote(dir),
			"mkdir -p web/src sub/dir emptycase",
			"printf 'a\\n' > web/src/a.txt",
			"printf 'deep\\n' > sub/dir/f.txt",
			"printf 'e\\n' > emptycase/e.txt",
			"printf 'node_modules\\nignored-parent\\n' > .gitignore",
			"git add .",
			"git commit -q -m init",
			"mkdir -p web/node_modules/pkg",
			"i=0; while [ \"$i\" -lt 500 ]; do : > \"web/node_modules/pkg/f${i}.js\"; i=$((i+1)); done",
			"rm web/src/a.txt && mkdir -p web/src/a.txt && printf 'untracked\\n' > web/src/a.txt/inner.txt",
			"rm emptycase/e.txt && mkdir -p emptycase/e.txt",
		}, "\n"))

		counter := &linuxOthersCounter{inner: srv.Executor(t, dir)}
		r := git.New(counter)
		err := r.Discard(git.FileChange{Path: "web/src/a.txt", Status: 'D'})
		if err == nil || !strings.Contains(err.Error(), "web/src/a.txt") {
			t.Fatalf("obstructed discard = %v, want refusal naming web/src/a.txt", err)
		}
		if counter.entries > 8 || counter.bytes > 1000 {
			t.Fatalf("collapsed scan read %d entries (%d bytes), want a handful", counter.entries, counter.bytes)
		}
		if got := strings.TrimSpace(srv.RunShell(t, "cat "+git.ShellQuote(path.Join(dir, "web/src/a.txt/inner.txt")))); got != "untracked" {
			t.Fatalf("untracked inner.txt = %q, want untracked", got)
		}

		if err := r.Discard(git.FileChange{Path: "emptycase/e.txt", Status: 'D'}); err != nil {
			t.Fatalf("empty directory blocked restore: %v", err)
		}
		if got := strings.TrimSpace(srv.RunShell(t, "cat "+git.ShellQuote(path.Join(dir, "emptycase/e.txt")))); got != "e" {
			t.Fatalf("restored e.txt = %q, want e", got)
		}

		for _, tc := range []struct {
			name, setup, verify string
		}{
			{
				name:   "untracked",
				setup:  "rm -rf sub && printf 'blk-u\\n' > sub",
				verify: "test -f sub && cat sub",
			},
			{
				name:   "ignored",
				setup:  "rm -rf sub && printf 'sub\\n' >> .gitignore && printf 'blk-i\\n' > sub",
				verify: "test -f sub && cat sub",
			},
			{
				name:   "symlink",
				setup:  "rm -rf sub && mkdir -p tgt && ln -s tgt sub",
				verify: "test -L sub && echo link-ok",
			},
		} {
			srv.RunShell(t, "set -eu\ncd "+git.ShellQuote(dir)+"\n"+tc.setup)
			err := r.Discard(git.FileChange{Path: "sub/dir/f.txt", Status: 'D'})
			if err == nil || !strings.Contains(err.Error(), "sub") {
				t.Fatalf("blocked parent (%s) discard = %v, want refusal naming sub", tc.name, err)
			}
			out := strings.TrimSpace(srv.RunShell(t, "set -eu\ncd "+git.ShellQuote(dir)+"\n"+tc.verify))
			if out == "" {
				t.Fatalf("blocked parent (%s) was destroyed", tc.name)
			}
		}
	})

	t.Run("SubmoduleDiscard", func(t *testing.T) {
		up := srv.InitRepo(t, "wg-linux-sub-up")
		srv.RunShell(t, strings.Join([]string{
			"set -eu",
			"cd " + git.ShellQuote(up),
			"printf 'f1-v1\\n' > f1.txt",
			"printf 'f2-v1\\n' > f2.txt",
			"git add f1.txt f2.txt",
			"git commit -q -m c1",
		}, "\n"))
		c1 := strings.TrimSpace(srv.RunGit(t, up, "rev-parse", "HEAD"))
		srv.RunShell(t, strings.Join([]string{
			"set -eu",
			"cd " + git.ShellQuote(up),
			"printf 'f1-v2\\n' > f1.txt",
			"git commit -q -am c2",
		}, "\n"))
		c2 := strings.TrimSpace(srv.RunGit(t, up, "rev-parse", "HEAD"))

		parent := srv.InitRepo(t, "wg-linux-sub-parent")
		srv.RunShell(t, strings.Join([]string{
			"set -eu",
			"cd " + git.ShellQuote(parent),
			"git -c protocol.file.allow=always submodule add -q " + git.ShellQuote(up) + " 'vendor/my lib'",
			"git -C 'vendor/my lib' checkout -q " + git.ShellQuote(c1),
			"git add 'vendor/my lib'",
			"git commit -q -m 'pin c1'",
		}, "\n"))

		sub := path.Join(parent, "vendor/my lib")
		r := srv.Repo(t, parent)

		uninit := path.Join(srv.RootDir, "wg-linux-sub-uninit")
		t.Cleanup(func() {
			srv.RunShell(t, "rm -rf -- "+git.ShellQuote(uninit))
		})
		srv.RunShell(t, "rm -rf -- "+git.ShellQuote(uninit)+" && git clone -q "+git.ShellQuote(parent)+" "+git.ShellQuote(uninit))
		err := srv.Repo(t, uninit).Discard(git.FileChange{Path: "vendor/my lib", Status: 'M'})
		if err == nil || !strings.Contains(err.Error(), "not checked out") {
			t.Fatalf("uninitialized submodule discard = %v, want not checked out error", err)
		}

		srv.RunGit(t, sub, "checkout", "-q", c2)
		srv.RunGit(t, parent, "config", "submodule.vendor/my lib.update", "rebase")
		if err := r.Discard(git.FileChange{Path: "vendor/my lib", Status: 'M'}); err != nil {
			t.Fatalf("moved submodule discard failed: %v", err)
		}
		if got := strings.TrimSpace(srv.RunGit(t, sub, "rev-parse", "HEAD")); got != c1 {
			t.Fatalf("submodule HEAD = %s, want %s", got, c1)
		}

		srv.RunGit(t, sub, "checkout", "-q", c2)
		srv.WriteFile(t, sub, "f1.txt", []byte("dirty-conflict\n"))
		if err := r.Discard(git.FileChange{Path: "vendor/my lib", Status: 'M'}); err == nil {
			t.Fatal("discard succeeded despite conflicting dirty submodule file")
		}
		if got := strings.TrimSpace(srv.RunShell(t, "cat "+git.ShellQuote(path.Join(sub, "f1.txt")))); got != "dirty-conflict" {
			t.Fatalf("conflicting dirty file destroyed: %q", got)
		}
		if got := strings.TrimSpace(srv.RunGit(t, sub, "rev-parse", "HEAD")); got != c2 {
			t.Fatalf("submodule HEAD moved to %s on conflicting dirty discard, want %s", got, c2)
		}
		srv.RunGit(t, sub, "checkout", "-q", "--", "f1.txt")

		srv.WriteFile(t, sub, "f2.txt", []byte("dirty-nonconflict\n"))
		srv.WriteFile(t, sub, "loose.txt", []byte("loose-work\n"))
		err = r.Discard(git.FileChange{Path: "vendor/my lib", Status: 'M'})
		if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
			t.Fatalf("dirty submodule discard = %v, want uncommitted changes error", err)
		}
		if got := strings.TrimSpace(srv.RunShell(t, "cat "+git.ShellQuote(path.Join(sub, "f2.txt")))); got != "dirty-nonconflict" {
			t.Fatalf("non-conflicting dirty file destroyed: %q", got)
		}
		if got := strings.TrimSpace(srv.RunShell(t, "cat "+git.ShellQuote(path.Join(sub, "loose.txt")))); got != "loose-work" {
			t.Fatalf("untracked submodule file destroyed: %q", got)
		}
		srv.RunShell(t, "rm -f "+git.ShellQuote(path.Join(sub, "loose.txt")))
		srv.RunGit(t, sub, "checkout", "-q", "--", "f2.txt")

		fake := "1111111111111111111111111111111111111111"
		srv.RunGit(t, parent, "update-index", "--cacheinfo", "160000,"+fake+",vendor/my lib")
		if err := r.Discard(git.FileChange{Path: "vendor/my lib", Status: 'M'}); err == nil {
			t.Fatal("discard succeeded despite missing submodule commit")
		}
	})
}
