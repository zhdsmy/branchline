package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhdsmy/branchline/internal/git"
)

// Opening a repository whose own configuration names a core.fsmonitor
// program must never execute it — neither the snapshot load nor the
// automatic refresh's status poll — while Local Changes still lists the
// worktree's modified and untracked files.
func TestUntrustedRepoOpenAndRefreshNeverRunFSMonitor(t *testing.T) {
	repo := newGitRepo(t, "untrusted-repo")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.WriteFile("a.txt", "one\ntwo\n")
	repo.WriteFile("untracked.txt", "new\n")

	// The hook and its marker live next to the repository inside this
	// test's own temporary directory; the hook is wired last so no setup
	// command can already trigger it.
	marker := filepath.Join(filepath.Dir(repo.Path()), "fsmonitor-ran")
	hook := filepath.Join(filepath.Dir(repo.Path()), "fsmonitor-hook")
	script := "#!/bin/sh\ntouch " + git.ShellQuote(marker) + "\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	repo.Git("config", "core.fsmonitor", hook)

	h := newTestApp(t)
	rig := installAutoRefresh(t, h)
	openAutoRefreshRepo(t, h, rig, repo.StoreRepo())

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("opening the repository executed its core.fsmonitor program")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if !h.ui.HasText("a.txt") {
		t.Errorf("a.txt missing in Local Changes; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("untracked.txt") {
		t.Errorf("untracked.txt missing in Local Changes; texts=%q", h.ui.Texts())
	}

	// The automatic refresh's status check, once its interval passes.
	rig.clock.advance(61 * time.Second)
	tickAutoRefresh(t, h)
	checkStamped(t, rig, "the interval check in a hostile repository")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the automatic refresh check executed the repository's core.fsmonitor program")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
