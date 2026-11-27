package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
)

// Calling Reload or LoadMore while a mutating write (do/doThen) is in flight
// does not start a concurrent repository.Load, leaves Loading() false and the
// existing snapshot untouched, and defers the increased logLimit to the
// automatic Reload scheduled once the write completes.
func TestSessionReloadAndLoadMoreDeferredWhileBusy(t *testing.T) {
	repo := newGitRepo(t, "busy-reload-repo")
	repo.Git("config", "commit.gpgsign", "false")
	var preWriteHead string
	for _, name := range []string{"c1", "c2", "c3", "c4", "c5"} {
		preWriteHead = repo.CommitFile(name+".txt", name+"\n", "commit "+name)
	}

	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)

	var spawns atomic.Int32
	s := newRepoSession(q.post, func(fn func()) {
		spawns.Add(1)
		q.spawn(fn)
	}, defaultOpenGitRepo)

	s.Open(repo.StoreRepo())
	q.Drain(t)

	// Narrow the log limit to 2 so LoadMore growth is observable in the snapshot.
	s.logLimit = 2
	s.Reload()
	q.Drain(t)

	if got := len(s.Snapshot().Commits); got != 2 {
		t.Fatalf("initial limited commits = %d, want 2", got)
	}
	if !s.Snapshot().More {
		t.Fatal("expected Snapshot().More == true before LoadMore")
	}
	if got := s.Snapshot().Status.Head; got != preWriteHead {
		t.Fatalf("pre-write HEAD = %q, want %q", got, preWriteHead)
	}

	// Gate the write worker so it stays busy while we invoke Reload and LoadMore.
	q.PauseWorkers()
	if ok := s.do("Create commit 6", func(r *git.Repo) error {
		if err := os.WriteFile(filepath.Join(repo.Path(), "c6.txt"), []byte("c6\n"), 0o644); err != nil {
			return err
		}
		if err := r.Run("add", "--", "c6.txt"); err != nil {
			return err
		}
		if err := r.Run("commit", "-m", "commit c6"); err != nil {
			return err
		}
		return r.Run("branch", "after-write")
	}); !ok {
		t.Fatal("session.do was rejected unexpectedly")
	}
	writeWorker := q.WaitWorker(t)
	q.ResumeWorkers()

	spawnsWhileBusy := spawns.Load()

	// 1. Reload during a busy write must be a no-op.
	s.Reload()
	if got := spawns.Load(); got != spawnsWhileBusy {
		t.Fatalf("Reload during busy write spawned %d worker(s), want 0", got-spawnsWhileBusy)
	}
	if s.Loading() {
		t.Fatal("Reload during busy write set Loading() = true, want false")
	}
	if got := s.Snapshot().Status.Head; got != preWriteHead {
		t.Fatalf("snapshot HEAD changed during busy Reload: got %q, want %q", got, preWriteHead)
	}
	if got := len(s.Snapshot().Commits); got != 2 {
		t.Fatalf("snapshot commit count changed during busy Reload: got %d, want 2", got)
	}

	// 2. LoadMore during a busy write records the larger logLimit without starting a read.
	s.LoadMore(3)
	if got := spawns.Load(); got != spawnsWhileBusy {
		t.Fatalf("LoadMore during busy write spawned %d worker(s), want 0", got-spawnsWhileBusy)
	}
	if s.Loading() {
		t.Fatal("LoadMore during busy write set Loading() = true, want false")
	}
	if got := s.Snapshot().Status.Head; got != preWriteHead {
		t.Fatalf("snapshot HEAD changed during busy LoadMore: got %q, want %q", got, preWriteHead)
	}
	if got := len(s.Snapshot().Commits); got != 2 {
		t.Fatalf("snapshot commit count changed during busy LoadMore: got %d, want 2", got)
	}

	// Release the write and drain both the write completion and its automatic Reload.
	q.ReleaseWorker(writeWorker)
	q.Drain(t)

	postWriteHead := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	if postWriteHead == preWriteHead {
		t.Fatal("write worker did not advance HEAD")
	}
	if s.Busy() {
		t.Fatal("session stayed busy after write completed")
	}
	if s.Loading() {
		t.Fatal("session stayed loading after post-write Reload completed")
	}
	if s.ActionErr() != nil {
		t.Fatalf("unexpected ActionErr: %v", s.ActionErr())
	}
	if s.LoadErr() != nil {
		t.Fatalf("unexpected LoadErr: %v", s.LoadErr())
	}
	if got := spawns.Load(); got != spawnsWhileBusy+1 {
		t.Fatalf("post-write spawns = %d, want exactly 1 automatic Reload", got-spawnsWhileBusy)
	}
	if got := s.Snapshot().Status.Head; got != postWriteHead {
		t.Fatalf("post-write snapshot HEAD = %q, want %q", got, postWriteHead)
	}
	if got := len(s.Snapshot().Commits); got != 5 {
		t.Fatalf("post-write snapshot commits = %d, want 5 (updated logLimit 2+3)", got)
	}
	if s.Snapshot().Commits[0].Subject != "commit c6" {
		t.Fatalf("newest commit subject = %q, want %q", s.Snapshot().Commits[0].Subject, "commit c6")
	}
	if !s.Snapshot().More {
		t.Fatal("expected Snapshot().More == true with 6 total commits and logLimit 5")
	}
	foundBranch := false
	for _, br := range s.Snapshot().Refs.Branches {
		if br.Name == "after-write" {
			foundBranch = true
			break
		}
	}
	if !foundBranch {
		t.Fatalf("post-write snapshot missing branch after-write: %+v", s.Snapshot().Refs.Branches)
	}
}

// doThen invokes its after callback on the UI thread when the same repository
// remains active, and skips after if the session switches repositories or
// closes before the write completes.
func TestSessionDoThenAfterSkippedOnSwitchOrClose(t *testing.T) {
	t.Run("same repository invokes after", func(t *testing.T) {
		repo := newGitRepo(t, "dothen-same")
		repo.CommitFile("a.txt", "a\n", "init")

		q := newUIQueue()
		t.Cleanup(q.releaseAllAndWait)
		s := newRepoSession(q.post, q.spawn, defaultOpenGitRepo)
		s.Open(repo.StoreRepo())
		q.Drain(t)

		wantErr := errors.New("expected write failure")
		called := 0
		var gotErr error
		if ok := s.doThen("Failing write", func(*git.Repo) error {
			return wantErr
		}, func(err error) {
			called++
			gotErr = err
		}); !ok {
			t.Fatal("doThen was rejected unexpectedly")
		}
		q.Drain(t)

		if called != 1 || !errors.Is(gotErr, wantErr) {
			t.Fatalf("after callback = (called=%d, err=%v), want (1, %v)", called, gotErr, wantErr)
		}
	})

	t.Run("after switching repository", func(t *testing.T) {
		repoA := newGitRepo(t, "dothen-a")
		repoA.CommitFile("a.txt", "a\n", "init a")
		repoB := newGitRepo(t, "dothen-b")
		repoB.CommitFile("b.txt", "b\n", "init b")

		q := newUIQueue()
		t.Cleanup(q.releaseAllAndWait)
		s := newRepoSession(q.post, q.spawn, defaultOpenGitRepo)
		s.Open(repoA.StoreRepo())
		q.Drain(t)

		q.PauseWorkers()
		afterCalled := false
		if ok := s.doThen("Write on A", func(r *git.Repo) error {
			return r.Run("branch", "stale-after-switch")
		}, func(error) {
			afterCalled = true
		}); !ok {
			t.Fatal("doThen on repoA was rejected")
		}
		writeWorker := q.WaitWorker(t)
		q.ResumeWorkers()

		// Switch to repoB and let repoB's initial snapshot load apply first.
		s.Open(repoB.StoreRepo())
		q.RunNext(t)

		q.ReleaseWorker(writeWorker)
		q.Drain(t)

		if afterCalled {
			t.Fatal("doThen after callback ran after switching repositories")
		}
		if s.Meta().Name != "dothen-b" {
			t.Fatalf("active repo = %q, want dothen-b", s.Meta().Name)
		}
		if s.ActionErr() != nil {
			t.Fatalf("unexpected ActionErr after switch: %v", s.ActionErr())
		}
	})

	t.Run("after closing repository", func(t *testing.T) {
		repo := newGitRepo(t, "dothen-close")
		repo.CommitFile("a.txt", "a\n", "init")

		q := newUIQueue()
		t.Cleanup(q.releaseAllAndWait)
		s := newRepoSession(q.post, q.spawn, defaultOpenGitRepo)
		s.Open(repo.StoreRepo())
		q.Drain(t)

		q.PauseWorkers()
		afterCalled := false
		if ok := s.doThen("Write before close", func(*git.Repo) error {
			return errors.New("stale close error")
		}, func(error) {
			afterCalled = true
		}); !ok {
			t.Fatal("doThen before close was rejected")
		}
		writeWorker := q.WaitWorker(t)
		q.ResumeWorkers()

		s.Close()
		q.ReleaseWorker(writeWorker)
		q.Drain(t)

		if afterCalled {
			t.Fatal("doThen after callback ran after closing repository")
		}
		if s.HasRepo() {
			t.Fatal("session unexpectedly has a repo after Close")
		}
		if s.Loading() {
			t.Fatal("stale write completion started a Reload after Close")
		}
		if s.ActionErr() != nil {
			t.Fatalf("stale write error leaked after Close: %v", s.ActionErr())
		}
	})
}
