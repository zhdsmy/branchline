package git_test

import (
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// TestConfiguredLinuxBlameIncremental checks that BlameIncremental over SSH on
// the configured Linux targets (including older Git 2.25 and BusyBox Alpine)
// spreads its hunks to the exact same hash, author, timestamp, and summary on
// every line as BlameLines, including repeated hunks, boundary commits, and
// whitespace-only edits under -w.
func TestConfiguredLinuxBlameIncremental(t *testing.T) {
	srv := sshtest.ConfiguredLinux(t)
	workDir := srv.InitRepo(t, "blame-incremental")
	repo := srv.Repo(t, workDir)

	version, err := repo.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	t.Logf("remote git: %s", version)

	// Commit 1 (root/boundary): four lines from Ada.
	srv.WriteFile(t, workDir, "notes.txt", []byte("alpha\nbeta\ngamma\ndelta\n"))
	srv.RunGit(t, workDir, "add", "notes.txt")
	srv.RunGit(t, workDir, "commit", "-q", "-m", "initial notes")
	firstCommit := srv.RunGit(t, workDir, "rev-parse", "HEAD")

	// Commit 2: different author edits line 2 and adds line 5, splitting
	// Commit 1 into two separate hunks so metadata elision is exercised.
	srv.WriteFile(t, workDir, "notes.txt", []byte("alpha\nBETA\ngamma\ndelta\nepsilon\n"))
	srv.RunGit(t, workDir, "add", "notes.txt")
	srv.RunGit(t, workDir, "-c", "user.name=Bob", "-c", "user.email=bob@example.invalid",
		"commit", "-q", "-m", "update beta and add epsilon")

	// Commit 3: whitespace-only change on line 1; -w must keep attribution on Commit 1.
	srv.WriteFile(t, workDir, "notes.txt", []byte("  alpha  \nBETA\ngamma\ndelta\nepsilon\n"))
	srv.RunGit(t, workDir, "add", "notes.txt")
	srv.RunGit(t, workDir, "commit", "-q", "-m", "reindent alpha")

	// Commit 4: edits line 4 and appends line 6.
	srv.WriteFile(t, workDir, "notes.txt", []byte("  alpha  \nBETA\ngamma\nDELTA\nepsilon\nzeta\n"))
	srv.RunGit(t, workDir, "add", "notes.txt")
	srv.RunGit(t, workDir, "commit", "-q", "-m", "update delta and add zeta")

	want, err := repo.BlameLines("notes.txt", "")
	if err != nil {
		t.Fatalf("BlameLines: %v", err)
	}
	if len(want) != 6 {
		t.Fatalf("BlameLines returned %d lines, want 6", len(want))
	}

	var hunks []git.BlameHunk
	if err := repo.BlameIncremental("notes.txt", "", func(h git.BlameHunk) {
		hunks = append(hunks, h)
	}); err != nil {
		t.Fatalf("BlameIncremental: %v", err)
	}
	if len(hunks) < 4 {
		t.Fatalf("BlameIncremental returned %d hunks, want at least 4: %+v", len(hunks), hunks)
	}

	type lineInfo struct {
		hash, author, subject string
		date                  int64
		seen                  bool
	}
	got := make([]lineInfo, len(want))
	for _, h := range hunks {
		if h.Hash == "" || h.Lines < 1 {
			t.Fatalf("invalid hunk: %+v", h)
		}
		if h.Hash == firstCommit && h.Boundary {
			// Expected for root/boundary commits only when notes.txt's commit
			// is itself the root; here InitRepo created an initial commit first,
			// so firstCommit is not the root commit and must not be marked boundary.
			t.Errorf("non-root commit hunk marked boundary: %+v", h)
		}
		for i := 0; i < h.Lines; i++ {
			idx := h.Line - 1 + i
			if idx < 0 || idx >= len(got) {
				t.Fatalf("hunk line %d out of range [1..%d]: %+v", idx+1, len(got), h)
			}
			got[idx] = lineInfo{
				hash:    h.Hash,
				author:  h.Author,
				subject: h.Subject,
				date:    h.Date.Unix(),
				seen:    true,
			}
		}
	}

	for i, w := range want {
		g := got[i]
		if !g.seen {
			t.Fatalf("line %d missing from BlameIncremental hunks", i+1)
		}
		if g.hash != w.Hash || g.author != w.Author || g.subject != w.Subject || g.date != w.Date.Unix() {
			t.Errorf("line %d: incremental=(hash=%s author=%q subject=%q date=%d) != BlameLines=(hash=%s author=%q subject=%q date=%d)",
				i+1, g.hash, g.author, g.subject, g.date, w.Hash, w.Author, w.Subject, w.Date.Unix())
		}
	}
	if got[0].hash != firstCommit {
		t.Errorf("line 1 attributed to %s, want %s (-w should ignore whitespace-only edit)", got[0].hash, firstCommit)
	}
	if got[2].hash != firstCommit {
		t.Errorf("line 3 attributed to %s, want %s (second hunk of repeated commit)", got[2].hash, firstCommit)
	}
	if got[1].author != "Bob" || got[4].author != "Bob" {
		t.Errorf("Bob's lines had authors %q and %q, want Bob", got[1].author, got[4].author)
	}

	// Also verify the repository's root commit file marks its hunks as boundary.
	rootHash := srv.RunGit(t, workDir, "rev-list", "--max-parents=0", "HEAD")
	var rootHunks []git.BlameHunk
	if err := repo.BlameIncremental("a.txt", "", func(h git.BlameHunk) {
		rootHunks = append(rootHunks, h)
	}); err != nil {
		t.Fatalf("BlameIncremental(a.txt): %v", err)
	}
	if len(rootHunks) == 0 {
		t.Fatal("BlameIncremental(a.txt) returned no hunks")
	}
	for _, h := range rootHunks {
		if h.Hash != rootHash || !h.Boundary {
			t.Errorf("root file hunk = %+v, want hash=%s boundary=true", h, rootHash)
		}
	}
}
