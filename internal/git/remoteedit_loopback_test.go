package git_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// TestRemoteEditLoopbackSSH runs download, write-back and a conflicting
// write-back against a real sshd on loopback.
func TestRemoteEditLoopbackSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)
	dir := srv.InitRepo(t, "re-loop")
	srv.WriteFile(t, dir, "src/edit me.txt", []byte("before\n"))
	srv.RunShell(t, "chmod 755 "+git.ShellQuote(filepath.Join(dir, "src/edit me.txt")))

	r := srv.Repo(t, dir)
	cache := filepath.Join(t.TempDir(), "cache")
	copy, err := r.DownloadForEdit(context.Background(), "src/edit me.txt", cache)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	data, err := os.ReadFile(copy.Local)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "before\n" {
		t.Errorf("copy content %q", data)
	}
	if copy.Meta.Mode != "755" {
		t.Errorf("remote mode %q", copy.Meta.Mode)
	}

	if err := os.WriteFile(copy.Local, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := r.SaveEdited(context.Background(), copy)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := srv.ReadFile(t, dir, "src/edit me.txt"); string(got) != "after\n" {
		t.Errorf("server content %q", got)
	}
	out := srv.RunShell(t, "stat -c '%s %a %Y' "+git.ShellQuote(filepath.Join(dir, "src/edit me.txt"))+" 2>/dev/null || stat -f '%z %Lp %m' "+git.ShellQuote(filepath.Join(dir, "src/edit me.txt")))
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 3 {
		if fmt.Sprintf("%s %s %s", fields[0], fields[1], fields[2]) != fmt.Sprintf("%d %s %d", meta.Size, meta.Mode, meta.MTime) {
			t.Errorf("returned meta %+v, server stat %q", meta, out)
		}
	} else {
		t.Errorf("server stat output %q", out)
	}

	// A write on the server after the download must block the next save.
	copy.Meta = meta
	srv.WriteFile(t, dir, "src/edit me.txt", []byte("someone else\n"))
	if err := os.WriteFile(copy.Local, []byte("conflicting\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SaveEdited(context.Background(), copy); !errors.Is(err, git.ErrRemoteChanged) {
		t.Fatalf("conflicting save: %v", err)
	}
	if got := srv.ReadFile(t, dir, "src/edit me.txt"); string(got) != "someone else\n" {
		t.Errorf("conflict overwritten: %q", got)
	}
}
