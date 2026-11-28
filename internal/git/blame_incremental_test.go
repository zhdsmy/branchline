package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// blameTestGit runs one git command in dir with the configuration files out
// of the way, as the other tests of this package do, and fails the test on
// any error.
func blameTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// blameTestRepo makes an empty repository on main and returns its directory.
func blameTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	blameTestGit(t, dir, "init", "-q", "-b", "main")
	blameTestGit(t, dir, "config", "user.name", "Ada")
	blameTestGit(t, dir, "config", "user.email", "ada@example.com")
	return dir
}

// blameTestWrite writes content to a file of the repository, creating parent
// directories.
func blameTestWrite(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", relPath, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}

// blameTestCommit stages everything and commits with the message.
func blameTestCommit(t *testing.T, dir, message string) string {
	t.Helper()
	blameTestGit(t, dir, "add", "-A")
	blameTestGit(t, dir, "commit", "-q", "-m", message)
	return blameTestGit(t, dir, "rev-parse", "HEAD")
}

// blameRow is one line of the result, spread from the incremental hunks or
// read off BlameLines, for comparing the two line by line.
type blameRow struct {
	hash, author, subject string
	date                  int64
}

// spreadBlame lays the hunks over the lines of the final file: each hunk
// owns the Lines lines starting at its Line, and the rows come out in line
// order.
func spreadBlame(hunks []BlameHunk) []blameRow {
	end := 0
	for _, h := range hunks {
		end = max(end, h.Line+h.Lines-1)
	}
	rows := make([]blameRow, end)
	for _, h := range hunks {
		for i := 0; i < h.Lines; i++ {
			n := h.Line + i
			if n < 1 || n > len(rows) {
				continue
			}
			rows[n-1] = blameRow{hash: h.Hash, author: h.Author, subject: h.Subject, date: h.Date.Unix()}
		}
	}
	return rows
}

// blameRows collects the incremental blame of path and spreads it over the
// lines, returning the hunks as well for assertions about them.
func blameRows(t *testing.T, r *Repo, path, rev string) ([]blameRow, []BlameHunk) {
	t.Helper()
	var hunks []BlameHunk
	if err := r.BlameIncremental(path, rev, func(h BlameHunk) { hunks = append(hunks, h) }); err != nil {
		t.Fatalf("BlameIncremental %s: %v", path, err)
	}
	return spreadBlame(hunks), hunks
}

// blameRowsEqual compares the spread of the incremental blame with what
// BlameLines reports, line by line: the commit of every line and its author,
// time and summary must be the same.
func blameRowsEqual(t *testing.T, r *Repo, path, rev string) []BlameHunk {
	t.Helper()
	want, err := r.BlameLines(path, rev)
	if err != nil {
		t.Fatalf("BlameLines %s: %v", path, err)
	}
	rows, hunks := blameRows(t, r, path, rev)
	if len(rows) != len(want) {
		t.Fatalf("spread covers %d lines, BlameLines reports %d", len(rows), len(want))
	}
	for i, w := range want {
		g := rows[i]
		if g.hash != w.Hash || g.author != w.Author || g.subject != w.Subject || g.date != w.Date.Unix() {
			t.Errorf("line %d: spread %+v, BlameLines hash %s author %q subject %q date %d",
				i+1, g, w.Hash, w.Author, w.Subject, w.Date.Unix())
		}
	}
	return hunks
}

// TestBlameIncrementalLinearHistory checks that a file built commit by
// commit spreads over its lines exactly as BlameLines reads it.
func TestBlameIncrementalLinearHistory(t *testing.T) {
	dir := blameTestRepo(t)
	blameTestWrite(t, dir, "f.txt", "one\ntwo\n")
	blameTestCommit(t, dir, "first")
	blameTestWrite(t, dir, "f.txt", "one\nTWO\ntwo\n")
	blameTestCommit(t, dir, "second")
	blameTestWrite(t, dir, "f.txt", "zero\none\nTWO\ntwo\nthree\n")
	blameTestCommit(t, dir, "third")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	blameRowsEqual(t, r, "f.txt", "")
}

// TestBlameIncrementalMergeHistory checks the spread of a file whose lines
// come through a merge: the two branches change its two ends, so the merge
// applies cleanly and the lines stay attributed to the branch commits.
func TestBlameIncrementalMergeHistory(t *testing.T) {
	dir := blameTestRepo(t)
	blameTestWrite(t, dir, "f.txt", "l1\nl2\nl3\nl4\nl5\nl6\n")
	blameTestCommit(t, dir, "base")
	blameTestGit(t, dir, "checkout", "-q", "-b", "side")
	blameTestWrite(t, dir, "f.txt", "l1\nl2\nl3\nl4\nl5\nSIDE END\n")
	blameTestCommit(t, dir, "side change")
	blameTestGit(t, dir, "checkout", "-q", "main")
	blameTestWrite(t, dir, "f.txt", "MAIN START\nl2\nl3\nl4\nl5\nl6\n")
	blameTestCommit(t, dir, "main change")
	blameTestGit(t, dir, "merge", "-q", "--no-ff", "-m", "merge side", "side")
	blameTestWrite(t, dir, "f.txt", "MAIN START\nl2\nl3\nl4\nl5\nSIDE END\nafter merge\n")
	blameTestCommit(t, dir, "after the merge")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	blameRowsEqual(t, r, "f.txt", "")
}

// TestBlameIncrementalRootBoundary checks that the lines of the root commit
// spread as BlameLines reads them, and that their hunks — and only theirs —
// are marked as boundary hunks.
func TestBlameIncrementalRootBoundary(t *testing.T) {
	dir := blameTestRepo(t)
	blameTestWrite(t, dir, "f.txt", "root one\nroot two\n")
	root := blameTestCommit(t, dir, "root commit")
	blameTestWrite(t, dir, "f.txt", "root one\nchanged\nroot two\n")
	blameTestCommit(t, dir, "later change")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	hunks := blameRowsEqual(t, r, "f.txt", "")
	var rootHunks, otherHunks int
	for _, h := range hunks {
		if h.Hash == root {
			rootHunks++
			if !h.Boundary {
				t.Errorf("hunk of the root commit %+v is not a boundary", h)
			}
		} else {
			otherHunks++
			if h.Boundary {
				t.Errorf("hunk of a later commit %+v is a boundary", h)
			}
		}
	}
	if rootHunks == 0 || otherHunks == 0 {
		t.Fatalf("root hunks %d, other hunks %d: both kinds expected", rootHunks, otherHunks)
	}
}

// TestBlameIncrementalWhitespaceOnly checks that a commit changing only the
// whitespace of a line leaves it attributed to the earlier commit, the -w
// the command lines up with, while the spread matches BlameLines.
func TestBlameIncrementalWhitespaceOnly(t *testing.T) {
	dir := blameTestRepo(t)
	blameTestWrite(t, dir, "ws.txt", "one\ntwo\nthree\n")
	base := blameTestCommit(t, dir, "base")
	blameTestWrite(t, dir, "ws.txt", "one\n  two  \nthree\n")
	blameTestCommit(t, dir, "whitespace only")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	rows, hunks := blameRows(t, r, "ws.txt", "")
	want, err := r.BlameLines("ws.txt", "")
	if err != nil {
		t.Fatalf("BlameLines: %v", err)
	}
	if len(rows) != len(want) {
		t.Fatalf("spread covers %d lines, BlameLines reports %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i].hash != w.Hash {
			t.Errorf("line %d: spread hash %s, BlameLines hash %s", i+1, rows[i].hash, w.Hash)
		}
	}
	// Without -w the reindented line would belong to the whitespace commit;
	// with it, line 2 stays with the base commit.
	if rows[1].hash != base {
		t.Errorf("line 2 attributed to %s, want the base commit %s: -w missing?", rows[1].hash, base)
	}
	for _, h := range hunks {
		if h.Hash == base && h.Line <= 2 && 2 < h.Line+h.Lines {
			return
		}
	}
	t.Errorf("no hunk of the base commit covers line 2: %+v", hunks)
}

// TestBlameIncrementalNonASCII checks the spread of a file whose path and
// content are not ASCII.
func TestBlameIncrementalNonASCII(t *testing.T) {
	dir := blameTestRepo(t)
	blameTestWrite(t, dir, "dïr/文件 é.txt", "émoji\n中文\n")
	blameTestCommit(t, dir, "non-ascii")
	blameTestWrite(t, dir, "dïr/文件 é.txt", "émoji\n中文\nzürich\n")
	blameTestCommit(t, dir, "more non-ascii")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	blameRowsEqual(t, r, "dïr/文件 é.txt", "")
}

// TestBlameIncrementalCRLF checks the spread of a file committed with CRLF
// line endings.
func TestBlameIncrementalCRLF(t *testing.T) {
	dir := blameTestRepo(t)
	blameTestWrite(t, dir, "crlf.txt", "one\r\ntwo\r\n")
	blameTestCommit(t, dir, "crlf")
	blameTestWrite(t, dir, "crlf.txt", "one\r\nTWO\r\nthree\r\n")
	blameTestCommit(t, dir, "more crlf")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	blameRowsEqual(t, r, "crlf.txt", "")
}

// TestBlameIncrementalNoFinalNewline checks the spread of a file whose last
// line has no newline.
func TestBlameIncrementalNoFinalNewline(t *testing.T) {
	dir := blameTestRepo(t)
	blameTestWrite(t, dir, "nf.txt", "one\ntwo")
	blameTestCommit(t, dir, "no final newline")
	blameTestWrite(t, dir, "nf.txt", "zero\none\ntwo")
	blameTestCommit(t, dir, "prepended")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	blameRowsEqual(t, r, "nf.txt", "")
}

// TestBlameIncrementalEmptyFile checks that blaming a file without lines
// reports no hunk and no error, as BlameLines reports no line.
func TestBlameIncrementalEmptyFile(t *testing.T) {
	dir := blameTestRepo(t)
	blameTestWrite(t, dir, "empty.txt", "")
	blameTestCommit(t, dir, "empty file")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	rows, hunks := blameRows(t, r, "empty.txt", "")
	if len(rows) != 0 || len(hunks) != 0 {
		t.Errorf("rows %v hunks %+v, want nothing", rows, hunks)
	}
}

// TestBlameIncrementalRename checks the spread of a file whose whole name
// changed: blame follows the lines into the older name, and the attribution
// still matches BlameLines.
func TestBlameIncrementalRename(t *testing.T) {
	dir := blameTestRepo(t)
	blameTestWrite(t, dir, "old-name.txt", "first\nsecond\nthird\n")
	blameTestCommit(t, dir, "under the old name")
	blameTestWrite(t, dir, "old-name.txt", "first\nSECOND\nthird\n")
	blameTestCommit(t, dir, "edited")
	blameTestGit(t, dir, "mv", "old-name.txt", "new-name.txt")
	blameTestCommit(t, dir, "renamed")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	blameRowsEqual(t, r, "new-name.txt", "")
}

// parseBlameIncremental feeds data to a fresh parser in pieces of n bytes
// and returns the hunks it reports.
func parseBlameIncremental(data []byte, n int) []BlameHunk {
	var hunks []BlameHunk
	p := &blameIncrementalParser{fn: func(h BlameHunk) { hunks = append(hunks, h) }, commits: map[string]*blameCommitMeta{}}
	for len(data) > 0 {
		m := min(n, len(data))
		p.write(data[:m])
		data = data[m:]
	}
	p.flush()
	return hunks
}

func blameHunksEqual(a, b []BlameHunk) string {
	if len(a) != len(b) {
		return fmt.Sprintf("%d hunks vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			return fmt.Sprintf("hunk %d: %+v vs %+v", i, a[i], b[i])
		}
	}
	return ""
}

// TestBlameIncrementalSplitBytes checks that the parser reports the same
// hunks however the output is cut: one byte at a time, random lengths, or
// all of it in one write. The input is real output of a file whose commits
// repeat, so a commit's details arrive only with its first entry.
func TestBlameIncrementalSplitBytes(t *testing.T) {
	dir := blameTestRepo(t)
	blameTestWrite(t, dir, "rep.txt", "l1\nl2\nl3\n")
	blameTestCommit(t, dir, "base")
	blameTestWrite(t, dir, "rep.txt", "l1\nCHANGED\nl3\n")
	blameTestCommit(t, dir, "middle changed")
	out, err := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true}).run(slow, "blame", "--incremental", "-w", "HEAD", "--", "rep.txt")
	if err != nil {
		t.Fatalf("blame for the parser input: %v", err)
	}
	// The blame must hold a commit with several entries, or the elision of
	// its later details goes untested.
	if len(parseBlameIncremental(out, 1<<20)) < 3 {
		t.Fatalf("the input blame has fewer than three entries: %q", out)
	}
	whole := parseBlameIncremental(out, 1<<20)

	t.Run("ByteAtATime", func(t *testing.T) {
		if diff := blameHunksEqual(parseBlameIncremental(out, 1), whole); diff != "" {
			t.Errorf("one byte at a time differs: %s", diff)
		}
	})
	t.Run("RandomLengths", func(t *testing.T) {
		// A simple generator, seeded once: the cuts are arbitrary without a
		// new dependency for randomness, and a failure reproduces.
		state := uint32(88172645)
		var hunks []BlameHunk
		p := &blameIncrementalParser{fn: func(h BlameHunk) { hunks = append(hunks, h) }, commits: map[string]*blameCommitMeta{}}
		rest := out
		for len(rest) > 0 {
			state = state*1664525 + 1013904223
			n := int(state%37) + 1
			m := min(n, len(rest))
			p.write(rest[:m])
			rest = rest[m:]
		}
		p.flush()
		if diff := blameHunksEqual(hunks, whole); diff != "" {
			t.Errorf("random cuts differ: %s", diff)
		}
	})
}

// writeSlowBlameGit writes a fake git that prints one complete blame entry,
// then waits for the file releaseDir/release to exist — polling twice a
// second — prints a second entry and exits. Every other invocation, such as
// the ones no test here makes, would print the same; nothing delegates to a
// real git.
func writeSlowBlameGit(t *testing.T, releaseDir string) string {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "git")
	entry1 := strings.Join([]string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 1 1 1",
		"author Ada",
		"author-mail <ada@example.com>",
		"author-time 1700000001",
		"author-tz +0000",
		"committer Ada",
		"committer-mail <ada@example.com>",
		"committer-time 1700000001",
		"committer-tz +0000",
		"summary first",
		"boundary",
		"filename probe.txt",
	}, "\n")
	entry2 := strings.Join([]string{
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb 2 2 1",
		"author Bob",
		"author-mail <bob@example.com>",
		"author-time 1700000002",
		"author-tz +0000",
		"committer Bob",
		"committer-mail <bob@example.com>",
		"committer-time 1700000002",
		"committer-tz +0000",
		"summary second",
		"filename probe.txt",
	}, "\n")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' " + ShellQuote(entry1) + "\n" +
		// The watch ends with the release file, but also with the directory
		// itself: the test's cleanup removes it whether or not the release
		// was written, and a loop polling a deleted path would never end.
		"while [ ! -f " + ShellQuote(filepath.Join(releaseDir, "release")) + " ]; do [ -d " + ShellQuote(releaseDir) + " ] || exit 0; sleep 0.2; done\n" +
		"printf '%s\\n' " + ShellQuote(entry2) + "\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write slow fake git: %v", err)
	}
	return fake
}

// TestBlameIncrementalFirstHunkEarly checks that the first hunk reaches the
// callback while git is still running: the fake git prints one entry and
// then waits for a release file, and the callback must arrive before that
// file exists.
func TestBlameIncrementalFirstHunkEarly(t *testing.T) {
	releaseDir := t.TempDir()
	fake := writeSlowBlameGit(t, releaseDir)
	r := New(&LocalExecutor{Dir: t.TempDir(), Git: fake})

	firstRelease := make(chan bool, 1)
	done := make(chan error, 1)
	var hunks []BlameHunk
	go func() {
		done <- r.BlameIncremental("probe.txt", "", func(h BlameHunk) {
			hunks = append(hunks, h)
			if len(hunks) == 1 {
				_, err := os.Stat(filepath.Join(releaseDir, "release"))
				firstRelease <- err == nil
			}
		})
	}()

	select {
	case existed := <-firstRelease:
		if existed {
			t.Fatal("the first hunk arrived only after git could have exited: no streaming")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the first hunk never arrived")
	}

	// Let the fake git finish: the second hunk follows and the command ends.
	if err := os.WriteFile(filepath.Join(releaseDir, "release"), nil, 0o644); err != nil {
		t.Fatalf("release the fake git: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("BlameIncremental: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("BlameIncremental did not return after the fake git ended")
	}
	if len(hunks) != 2 {
		t.Errorf("%d hunks, want the two the fake git prints", len(hunks))
	}
}

// countBlameProbeProcesses counts processes whose command line carries the
// probe token, as read_context_test.go counts its own.
func countBlameProbeProcesses(t *testing.T, token string) int {
	t.Helper()
	out, err := exec.Command("ps", "-axww", "-o", "command=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, token) {
			n++
		}
	}
	return n
}

// blameCancelProbeToken returns a token unique enough not to match the
// command lines of other tests running at the same time.
func blameCancelProbeToken(t *testing.T) string {
	return fmt.Sprintf("blamestreamprobe-%d-%d", os.Getpid(), time.Now().UnixNano())
}

// TestBlameIncrementalCancelLocal checks that canceling the Repo's context
// stops a blame in flight on this machine: the git process disappears, the
// callback is not called again, and the error reports the cancellation.
func TestBlameIncrementalCancelLocal(t *testing.T) {
	releaseDir := t.TempDir()
	fake := writeSlowBlameGit(t, releaseDir)
	token := blameCancelProbeToken(t)
	// Whatever happens, releasing the fake git lets it exit instead of
	// lingering past the test.
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(releaseDir, "release"), nil, 0o644)
	})

	r := New(&LocalExecutor{Dir: t.TempDir(), Git: fake})
	parent, cancel := context.WithCancel(context.Background())
	rc := r.WithContext(parent)

	var count atomic.Int32
	done := make(chan error, 1)
	first := make(chan struct{}, 1)
	go func() {
		done <- rc.BlameIncremental(token, "", func(h BlameHunk) {
			if count.Add(1) == 1 {
				first <- struct{}{}
			}
		})
	}()

	select {
	case <-first:
	case <-time.After(10 * time.Second):
		t.Fatal("the first hunk never arrived")
	}
	deadline := time.Now().Add(5 * time.Second)
	for countBlameProbeProcesses(t, token) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the fake git process never appeared in ps")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()

	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("BlameIncremental did not return after the cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want canceled", err)
	}

	deadline = time.Now().Add(5 * time.Second)
	for countBlameProbeProcesses(t, token) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d probe processes linger after the cancellation", countBlameProbeProcesses(t, token))
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Even the output the fake git prints as it is let go must not reach
	// the callback: git was already stopped.
	_ = os.WriteFile(filepath.Join(releaseDir, "release"), nil, 0o644)
	time.Sleep(300 * time.Millisecond)
	if got := count.Load(); got != 1 {
		t.Errorf("%d callbacks after the cancellation, want the one before it", got)
	}
}

// writeBlameRewriteSSH writes an ssh that passes everything to the real ssh
// wrapper, except that the first " git " of the remote command becomes an
// invocation of the fake git through sh: the server resolves git from its
// own PATH, which a test cannot add to, so the command is rewritten instead.
func writeBlameRewriteSSH(t *testing.T, realSSH, fakeGit string) string {
	t.Helper()
	if strings.ContainsAny(fakeGit, " \t'\"\\") {
		t.Fatalf("the fake git path %q cannot be embedded in the rewrite", fakeGit)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "ssh")
	script := "#!/usr/bin/env python3\n" +
		"import os, sys\n" +
		"args = sys.argv[1:]\n" +
		"rewritten = [a.replace(' git ', ' sh " + fakeGit + " ', 1) if ' git ' in a else a for a in args]\n" +
		"os.execv(" + pyQuote(realSSH) + ", [" + pyQuote(realSSH) + "] + rewritten)\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatalf("write rewriting ssh: %v", err)
	}
	return wrapper
}

// pyQuote quotes a string for embedding in a Python source file.
func pyQuote(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "'", "\\'") + "'"
}

// TestBlameIncrementalCancelSSH checks the same cancellation over SSH: the
// server's git — a fake that waits after its first entry — is stopped with
// its process group, and the error reports the cancellation.
func TestBlameIncrementalCancelSSH(t *testing.T) {
	srv := startSSHD(t)
	releaseDir := t.TempDir()
	fake := writeSlowBlameGit(t, releaseDir)
	token := blameCancelProbeToken(t)
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(releaseDir, "release"), nil, 0o644)
	})

	dir := testRepo(t)
	ex := srv.executor(t, dir)
	ex.NoMultiplex = true
	ex.SSH = writeBlameRewriteSSH(t, srv.ssh, fake)
	r := New(ex)
	parent, cancel := context.WithCancel(context.Background())
	rc := r.WithContext(parent)

	var count atomic.Int32
	done := make(chan error, 1)
	first := make(chan struct{}, 1)
	go func() {
		done <- rc.BlameIncremental(token, "", func(h BlameHunk) {
			if count.Add(1) == 1 {
				first <- struct{}{}
			}
		})
	}()

	select {
	case <-first:
	case <-time.After(15 * time.Second):
		t.Fatal("the first hunk never arrived")
	}

	cancel()

	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("BlameIncremental did not return after the cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want canceled", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for countBlameProbeProcesses(t, token) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d probe processes linger after the cancellation", countBlameProbeProcesses(t, token))
		}
		time.Sleep(25 * time.Millisecond)
	}

	_ = os.WriteFile(filepath.Join(releaseDir, "release"), nil, 0o644)
	time.Sleep(300 * time.Millisecond)
	if got := count.Load(); got != 1 {
		t.Errorf("%d callbacks after the cancellation, want the one before it", got)
	}
}

// TestBlameIncrementalSSHConsistency checks that the incremental blame over
// SSH, spread over the lines, matches what BlameLines reads of the same
// repository on this machine.
func TestBlameIncrementalSSHConsistency(t *testing.T) {
	srv := startSSHD(t)
	dir := testRepo(t)
	// More history than the fixture's: commits whose entries repeat, so the
	// stream over SSH exercises the same elision as a local one.
	blameTestWrite(t, dir, "rep.txt", "l1\nl2\nl3\n")
	blameTestGit(t, dir, "add", "rep.txt")
	blameTestGit(t, dir, "commit", "-q", "-m", "rep base")
	blameTestWrite(t, dir, "rep.txt", "l1\nCHANGED\nl3\n")
	blameTestGit(t, dir, "commit", "-q", "-am", "rep middle")

	remote := New(srv.executor(t, dir))
	var hunks []BlameHunk
	if err := remote.BlameIncremental("rep.txt", "", func(h BlameHunk) { hunks = append(hunks, h) }); err != nil {
		t.Fatalf("BlameIncremental over SSH: %v", err)
	}
	if len(hunks) == 0 {
		t.Fatal("no hunks over SSH")
	}
	rows := spreadBlame(hunks)

	local := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	want, err := local.BlameLines("rep.txt", "")
	if err != nil {
		t.Fatalf("BlameLines: %v", err)
	}
	if len(rows) != len(want) {
		t.Fatalf("spread covers %d lines, BlameLines reports %d", len(rows), len(want))
	}
	for i, w := range want {
		g := rows[i]
		if g.hash != w.Hash || g.author != w.Author || g.subject != w.Subject || g.date != w.Date.Unix() {
			t.Errorf("line %d: spread %+v, BlameLines hash %s author %q subject %q date %d",
				i+1, g, w.Hash, w.Author, w.Subject, w.Date.Unix())
		}
	}
}
