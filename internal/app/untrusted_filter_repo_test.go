package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhdsmy/branchline/internal/git"
)

// Opening a repository whose .git/config defines a filter never runs that
// filter — not for the snapshot load, not for the passive poll the
// automatic refresh fires — while the modified file still reaches Local
// Changes. Plain git on the same checkout does run it, proving the setup.
func TestUntrustedRepoFilterNeverRunsOnOpenAndPoll(t *testing.T) {
	repo := newGitRepo(t, "untrusted-filter-open")

	cleanLog := filepath.Join(t.TempDir(), "clean.log")
	cleanSh := filepath.Join(t.TempDir(), "filter-clean.sh")
	script := "#!/bin/sh\necho ran >> " + git.ShellQuote(cleanLog) + "\nexec cat\n"
	if err := os.WriteFile(cleanSh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	repo.Git("config", "filter.x.clean", "sh "+cleanSh)
	repo.WriteFile(".gitattributes", "*.txt filter=x\n")
	repo.WriteFile("f.txt", "hello\n")
	repo.Git("add", ".gitattributes", "f.txt")
	repo.Git("commit", "-q", "-m", "initial")
	// The commits above legitimately ran the filter while staging; the test
	// measures only what the app runs afterwards.
	if err := os.Remove(cleanLog); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// The rewrite keeps the file's size: a length change alone would answer
	// the status without ever converting the content, and the filter would
	// have nothing to do even for plain git.
	repo.WriteFile("f.txt", "world\n")

	h := newTestApp(t)
	rig := installAutoRefresh(t, h)
	openAutoRefreshRepo(t, h, rig, repo.StoreRepo())

	if _, err := os.Stat(cleanLog); err == nil {
		t.Fatal("the repository's own filter ran while the app opened it")
	}
	cache := h.app.changesView.listCache.lookup(h.app.session.Snapshot().Status)
	if _, ok := cache.lookupUnstaged("f.txt"); !ok {
		t.Fatal("the modified file is missing from Local Changes")
	}

	// One passive poll of the automatic refresh must not run it either.
	rig.clock.advance(61 * time.Second)
	tickAutoRefresh(t, h)
	if _, err := os.Stat(cleanLog); err == nil {
		t.Fatal("the repository's own filter ran on the automatic refresh's poll")
	}

	// The control: plain git status on this checkout, run the way a terminal
	// would, does execute the filter.
	repo.Git("status", "--porcelain=v2", "-z")
	if _, err := os.Stat(cleanLog); err != nil {
		t.Fatal("control: plain git status did not run the filter, the fixture is broken")
	}
}
