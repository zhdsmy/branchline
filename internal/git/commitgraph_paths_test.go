package git

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// changedPathsRepo makes a repository of n linear commits through one
// fast-import stream. filler.txt changes in every commit, firstcommit.txt
// exists only in the first commit, and each file of spread changes about
// every ninth commit, offset so they do not move together. Background
// maintenance is off, so git never reorganizes the objects behind the test.
func changedPathsRepo(t *testing.T, n int, spread []string) string {
	t.Helper()
	dir := t.TempDir()
	commitGraphGit(t, dir, "init", "-q", "-b", "main")
	commitGraphGit(t, dir, "config", "user.name", "Ada")
	commitGraphGit(t, dir, "config", "user.email", "ada@example.com")
	commitGraphGit(t, dir, "config", "core.commitGraph", "true")
	commitGraphGit(t, dir, "config", "gc.writeCommitGraph", "true")
	commitGraphGit(t, dir, "config", "gc.auto", "0")
	commitGraphGit(t, dir, "config", "maintenance.auto", "false")

	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\n", i)
		fmt.Fprintf(&b, "author Ada <ada@example.com> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "committer Ada <ada@example.com> %d +0000\n", 1700000000+i)
		msg := fmt.Sprintf("commit %d", i)
		fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
		if i > 1 {
			fmt.Fprintf(&b, "from :%d\n", i-1)
		}
		content := fmt.Sprintf("filler a %d\nfiller b %d\n", i, i)
		fmt.Fprintf(&b, "M 100644 inline filler.txt\ndata %d\n%s\n", len(content), content)
		if i == 1 {
			fmt.Fprintf(&b, "M 100644 inline firstcommit.txt\ndata 4\none\n")
		}
		for k, path := range spread {
			if (i+k)%9 != 0 {
				continue
			}
			body := fmt.Sprintf("%d line one\n%d line two\n", i, i)
			fmt.Fprintf(&b, "M 100644 inline %s\ndata %d\n%s\n", path, len(body), body)
		}
		b.WriteString("\n")
	}
	cmd := exec.Command("git", "-C", dir, "fast-import", "--quiet")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	cmd.Stdin = strings.NewReader(b.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %v\n%s", err, out)
	}
	return dir
}

// commitGraphGitEnv is commitGraphGit with extra environment variables for
// the one git invocation, such as GIT_TRACE2_PERF.
func commitGraphGitEnv(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// graphPath resolves the commit-graph of a plain write the way git itself
// names it, so linked worktrees and relative answers come out right.
func graphPath(t *testing.T, dir string) string {
	t.Helper()
	out := commitGraphGit(t, dir, "rev-parse", "--git-path", "objects/info/commit-graph")
	if !filepath.IsAbs(out) {
		out = filepath.Join(dir, out)
	}
	return out
}

// graphFilterInfo reads the chunk table of one commit-graph file: whether
// the changed-path index and data chunks are present and, when they are,
// the filter version the data chunk opens with. The format is the
// documented one: an eight-byte header whose seventh byte is the chunk
// count, then twelve-byte table entries of a four-byte identifier and an
// eight-byte big-endian offset, ended by a zero identifier.
func graphFilterInfo(t *testing.T, path string) (hasBIDX, hasBDAT bool, version uint32) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read commit-graph: %v", err)
	}
	if len(data) < 8 || string(data[:4]) != "CGPH" {
		t.Fatalf("%s is not a commit-graph file", path)
	}
	chunks := int(data[6])
	tableEnd := 8 + 12*(chunks+1)
	if len(data) < tableEnd {
		t.Fatalf("commit-graph chunk table truncated: %d bytes for %d chunks", len(data), chunks)
	}
	for i := 0; i < chunks; i++ {
		id := string(data[8+i*12 : 8+i*12+4])
		off := int(binary.BigEndian.Uint64(data[8+i*12+4 : 8+i*12+12]))
		if id == string([]byte{0, 0, 0, 0}) {
			break
		}
		switch id {
		case "BIDX":
			hasBIDX = true
		case "BDAT":
			hasBDAT = true
			if off+4 <= len(data) {
				version = binary.BigEndian.Uint32(data[off : off+4])
			}
		}
	}
	return hasBIDX, hasBDAT, version
}

// assertGraphV2Filters checks that the file at path carries version-2
// changed-path filters.
func assertGraphV2Filters(t *testing.T, path string) {
	t.Helper()
	bidx, bdat, version := graphFilterInfo(t, path)
	if !bidx || !bdat {
		t.Fatalf("%s has no changed-path filter chunks: BIDX %v BDAT %v", path, bidx, bdat)
	}
	if version != 2 {
		t.Fatalf("%s filter version = %d, want 2", path, version)
	}
}

// writeArgs returns the arguments of the commit-graph write in the command
// log, leading -c pairs included, or nil when no write was recorded.
func writeArgs(t *testing.T, log *CommandLog) []string {
	t.Helper()
	records, _ := log.Snapshot()
	for _, rec := range records {
		args := rec.Args
		i := 0
		for i+1 < len(args) && args[i] == "-c" {
			i += 2
		}
		if len(args)-i >= 2 && args[i] == "commit-graph" && args[i+1] == "write" {
			return args
		}
	}
	return nil
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// assertWriteCarriesFilters checks that the recorded write asks for
// changed-path filters in version 2: -c commitGraph.changedPathsVersion=2
// ahead of the subcommand and --changed-paths after it.
func assertWriteCarriesFilters(t *testing.T, log *CommandLog) {
	t.Helper()
	args := writeArgs(t, log)
	if args == nil {
		t.Fatal("no commit-graph write in the command log")
	}
	if len(args) < 4 || args[0] != "-c" || args[1] != "commitGraph.changedPathsVersion=2" ||
		args[2] != "commit-graph" || args[3] != "write" {
		t.Fatalf("write arguments = %v, want -c commitGraph.changedPathsVersion=2 ahead of commit-graph write", args)
	}
	if !hasArg(args, "--changed-paths") {
		t.Fatalf("write arguments = %v, want --changed-paths", args)
	}
}

// assertWriteOmitsFilters checks that the recorded write carries neither
// the version override nor --changed-paths.
func assertWriteOmitsFilters(t *testing.T, log *CommandLog) {
	t.Helper()
	args := writeArgs(t, log)
	if args == nil {
		t.Fatal("no commit-graph write in the command log")
	}
	if hasArg(args, "--changed-paths") || hasArg(args, "commitGraph.changedPathsVersion=2") {
		t.Fatalf("write arguments = %v, want neither --changed-paths nor the version override", args)
	}
}

// bloomStatistics runs git log for path with trace2 performance output and
// reads the totals of the Bloom filter lookups from its statistics record.
func bloomStatistics(t *testing.T, dir, path string) (notPresent, maybe, definitelyNot int) {
	t.Helper()
	trace := filepath.Join(t.TempDir(), "trace2.txt")
	commitGraphGitEnv(t, dir, []string{"GIT_TRACE2_PERF=" + trace},
		"log", "--format=%H", "HEAD", "--", path)
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("read trace2 output: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		idx := strings.Index(line, "statistics:{")
		if idx < 0 {
			continue
		}
		var stats struct {
			FilterNotPresent int `json:"filter_not_present"`
			Maybe            int `json:"maybe"`
			DefinitelyNot    int `json:"definitely_not"`
		}
		if err := json.Unmarshal([]byte(strings.TrimRight(line[idx+len("statistics:"):], "\r")), &stats); err != nil {
			t.Fatalf("trace2 statistics do not parse: %v in %q", err, line)
		}
		return stats.FilterNotPresent, stats.Maybe, stats.DefinitelyNot
	}
	t.Fatalf("no bloom statistics in trace2 output:\n%s", data)
	return 0, 0, 0
}

// appendCommits adds n more linear commits on main to the repository in
// dir, each changing filler.txt, chained on the current tip. The time
// offset keeps the new commits later than the ones changedPathsRepo made.
func appendCommits(t *testing.T, dir string, n int) {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\n", i)
		fmt.Fprintf(&b, "author Ada <ada@example.com> %d +0000\n", 1700000000+1000+i)
		fmt.Fprintf(&b, "committer Ada <ada@example.com> %d +0000\n", 1700000000+1000+i)
		msg := fmt.Sprintf("appended commit %d", i)
		fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
		if i == 1 {
			b.WriteString("from refs/heads/main^0\n")
		} else {
			fmt.Fprintf(&b, "from :%d\n", i-1)
		}
		content := fmt.Sprintf("filler a %d\nfiller b %d\n", 1000+i, 1000+i)
		fmt.Fprintf(&b, "M 100644 inline filler.txt\ndata %d\n%s\n", len(content), content)
		b.WriteString("\n")
	}
	cmd := exec.Command("git", "-C", dir, "fast-import", "--quiet")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	cmd.Stdin = strings.NewReader(b.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fast-import append: %v\n%s", err, out)
	}
}

// filterCheckedRepo builds the fixture the filter tests share: 200 commits,
// firstcommit.txt changed only in the first one, written by one call whose
// arguments the returned command log records.
func filterCheckedRepo(t *testing.T, spread []string) (dir string, r *Repo, log *CommandLog) {
	t.Helper()
	dir = changedPathsRepo(t, 200, spread)
	log = NewCommandLog()
	r = New(&LocalExecutor{Dir: dir, NoGlobalConfig: true}).WithCommandLog(log)
	wrote, err := r.WriteCommitGraphIfLarge(100)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !wrote {
		t.Fatal("wrote = false for a history above the threshold")
	}
	return dir, r, log
}

// TestCommitGraphChangedPathsWritten checks that the automatic write of a
// large history asks for changed-path filters in version 2 and produces
// them: the file carries the filter chunks in version 2, passes git's own
// verification, and the recorded write carries -c
// commitGraph.changedPathsVersion=2 and --changed-paths.
func TestCommitGraphChangedPathsWritten(t *testing.T) {
	dir, r, log := filterCheckedRepo(t, nil)

	assertGraphV2Filters(t, graphPath(t, dir))
	if err := r.Run("commit-graph", "verify"); err != nil {
		t.Fatalf("git commit-graph verify: %v", err)
	}
	assertWriteCarriesFilters(t, log)
}

// TestCommitGraphChangedPathsFilterUsed checks that a path walk uses the
// filters: for a file only the first commit ever changed, the Bloom
// statistics of git log show commits ruled out without parsing.
func TestCommitGraphChangedPathsFilterUsed(t *testing.T) {
	dir, _, _ := filterCheckedRepo(t, nil)

	_, _, definitelyNot := bloomStatistics(t, dir, "firstcommit.txt")
	if definitelyNot == 0 {
		t.Fatal("definitely_not = 0: no commit was ruled out by the changed-path filter")
	}
}

// TestCommitGraphChangedPathsNonASCII checks that paths with non-ASCII and
// even invalid UTF-8 bytes — NFC and NFD accents, a CJK name, an emoji —
// give the same file history and blame before and after the graph is
// written, so the filters never drop a commit these paths changed. The NFD
// file is compared once more with core.precomposeUnicode=false.
func TestCommitGraphChangedPathsNonASCII(t *testing.T) {
	// The same accented path twice: once with precomposed accents
	// (NFC), once with combining ones (NFD) — different bytes,
	// different files.
	nfc := "caf\u00e9/r\u00e9sum\u00e9.md"
	nfd := "cafe\u0301/re\u0301sume\u0301.md"
	paths := []string{
		"文档/说明.txt",
		nfc,
		nfd,
		"🚀/rocket.txt",
		"bad\xffname.txt",
	}
	dir := changedPathsRepo(t, 200, paths)
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

	type result struct {
		log   []Commit
		blame []BlameLine
	}
	capture := func(t *testing.T, repo *Repo) map[string]result {
		t.Helper()
		out := map[string]result{}
		for _, p := range paths {
			commits, err := repo.FileLog(p, 100)
			if err != nil {
				t.Fatalf("FileLog %q: %v", p, err)
			}
			if len(commits) < 20 {
				t.Fatalf("FileLog %q found %d commits, want at least 20 changes", p, len(commits))
			}
			blame, err := repo.BlameLines(p, "")
			if err != nil {
				t.Fatalf("BlameLines %q: %v", p, err)
			}
			out[p] = result{log: commits, blame: blame}
		}
		return out
	}
	compare := func(t *testing.T, before, after map[string]result) {
		t.Helper()
		for p, want := range before {
			got := after[p]
			if !reflect.DeepEqual(want.log, got.log) {
				t.Errorf("FileLog %q changed across the write:\nbefore %v\nafter  %v", p, want.log, got.log)
			}
			if !reflect.DeepEqual(want.blame, got.blame) {
				t.Errorf("BlameLines %q changed across the write:\nbefore %v\nafter  %v", p, want.blame, got.blame)
			}
		}
	}

	precomposeRepo := New(&LocalExecutor{
		Dir:            dir,
		NoGlobalConfig: true,
		Config:         []string{"-c", "core.precomposeUnicode=false"},
	})
	nfdOnly := func(in map[string]result) map[string]result {
		return map[string]result{nfd: in[nfd]}
	}

	before := capture(t, r)
	precomposeBefore := nfdOnly(capture(t, precomposeRepo))

	wrote, err := r.WriteCommitGraphIfLarge(100)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !wrote {
		t.Fatal("wrote = false for a history above the threshold")
	}
	assertGraphV2Filters(t, graphPath(t, dir))

	compare(t, before, capture(t, r))
	compare(t, precomposeBefore, nfdOnly(capture(t, precomposeRepo)))
}

// TestCommitGraphChangedPathsSurviveRewrites checks that the filters stay,
// still in version 2 and covering the new commits, when the user's own git
// rewrites the graph: a plain commit-graph write, a garbage collection and
// an incremental write into a split chain.
func TestCommitGraphChangedPathsSurviveRewrites(t *testing.T) {
	// assertCoverage checks the filters are version 2, git verifies the
	// graph, and the statistics of a path every commit changes show every
	// commit with a filter present, so the appended commits are covered too.
	assertCoverage := func(t *testing.T, dir string, r *Repo, total int) {
		t.Helper()
		assertGraphV2Filters(t, graphPath(t, dir))
		if err := r.Run("commit-graph", "verify"); err != nil {
			t.Fatalf("git commit-graph verify: %v", err)
		}
		notPresent, maybe, _ := bloomStatistics(t, dir, "filler.txt")
		if notPresent != 0 {
			t.Errorf("filter_not_present = %d, want 0: commits of the graph miss their filter", notPresent)
		}
		if maybe != total {
			t.Errorf("maybe = %d, want %d: a walk of a path every commit changes must consult every commit", maybe, total)
		}
	}

	writeFresh := func(t *testing.T) (dir string, r *Repo) {
		t.Helper()
		dir, r, _ = filterCheckedRepo(t, nil)
		appendCommits(t, dir, 50)
		return dir, r
	}

	// A plain user rewrite keeps the filters and covers the new commits.
	t.Run("plain write", func(t *testing.T) {
		dir, r := writeFresh(t)
		commitGraphGit(t, dir, "commit-graph", "write", "--reachable")
		assertCoverage(t, dir, r, 250)
	})

	// A garbage collection rewriting the graph keeps the filters.
	t.Run("gc", func(t *testing.T) {
		dir, r := writeFresh(t)
		commitGraphGit(t, dir, "gc")
		assertCoverage(t, dir, r, 250)
	})

	// A split write covers the new commits in filtered layers too.
	t.Run("split", func(t *testing.T) {
		dir, r := writeFresh(t)
		commitGraphGit(t, dir, "commit-graph", "write", "--reachable", "--split")

		chainFile := filepath.Join(dir, ".git", "objects", "info", "commit-graphs", "commit-graph-chain")
		chainData, err := os.ReadFile(chainFile)
		if err != nil {
			t.Fatalf("no split chain after --split write: %v", err)
		}
		layers := strings.Fields(string(chainData))
		if len(layers) == 0 {
			t.Fatal("split chain names no layers")
		}
		for _, layer := range layers {
			assertGraphV2Filters(t, filepath.Join(dir, ".git", "objects", "info", "commit-graphs", "graph-"+layer+".graph"))
		}
		if err := r.Run("commit-graph", "verify"); err != nil {
			t.Fatalf("git commit-graph verify: %v", err)
		}
		notPresent, maybe, _ := bloomStatistics(t, dir, "filler.txt")
		if notPresent != 0 {
			t.Errorf("filter_not_present = %d, want 0: commits of the chain miss their filter", notPresent)
		}
		if maybe != 250 {
			t.Errorf("maybe = %d, want 250: a walk of a path every commit changes must consult every commit", maybe)
		}
	})
}

// TestCommitGraphChangedPathsUserSettings checks that a user who set any
// of commitGraph.changedPaths, commitGraph.changedPathsVersion or
// commitGraph.readChangedPaths — in the repository's configuration or on
// the executor's command line — gets a write without the filter flags, and
// git then decides by that configuration.
func TestCommitGraphChangedPathsUserSettings(t *testing.T) {
	writeIn := func(t *testing.T, dir string, config []string) (*Repo, *CommandLog) {
		t.Helper()
		log := NewCommandLog()
		r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true, Config: config}).WithCommandLog(log)
		wrote, err := r.WriteCommitGraphIfLarge(100)
		if err != nil {
			t.Fatalf("write: %v", err)
		}
		if !wrote {
			t.Fatal("wrote = false for a history above the threshold")
		}
		return r, log
	}

	// commitGraph.changedPaths=false writes no filters at all.
	t.Run("changedPaths false", func(t *testing.T) {
		dir := changedPathsRepo(t, 200, nil)
		commitGraphGit(t, dir, "config", "commitGraph.changedPaths", "false")
		_, log := writeIn(t, dir, nil)
		assertWriteOmitsFilters(t, log)
		_, bdat, _ := graphFilterInfo(t, graphPath(t, dir))
		if bdat {
			t.Error("filter data written although commitGraph.changedPaths is false")
		}
	})

	// commitGraph.changedPaths=true with changedPathsVersion=1 writes the
	// version-1 filters the user asked for.
	t.Run("changedPaths true version 1", func(t *testing.T) {
		dir := changedPathsRepo(t, 200, nil)
		commitGraphGit(t, dir, "config", "commitGraph.changedPaths", "true")
		commitGraphGit(t, dir, "config", "commitGraph.changedPathsVersion", "1")
		_, log := writeIn(t, dir, nil)
		assertWriteOmitsFilters(t, log)
		_, bdat, version := graphFilterInfo(t, graphPath(t, dir))
		if !bdat {
			t.Fatal("no filter data although commitGraph.changedPaths is true")
		}
		if version != 1 {
			t.Errorf("filter version = %d, want the configured 1", version)
		}
	})

	// commitGraph.readChangedPaths=false keeps the filter flags off the
	// write as well.
	t.Run("readChangedPaths false", func(t *testing.T) {
		dir := changedPathsRepo(t, 200, nil)
		commitGraphGit(t, dir, "config", "commitGraph.readChangedPaths", "false")
		_, log := writeIn(t, dir, nil)
		assertWriteOmitsFilters(t, log)
	})

	// The same setting passed as -c on the executor's command line counts
	// the same as one in a file.
	t.Run("executor -c", func(t *testing.T) {
		dir := changedPathsRepo(t, 200, nil)
		_, log := writeIn(t, dir, []string{"-c", "commitGraph.changedPaths=false"})
		assertWriteOmitsFilters(t, log)
	})

	// With none of the three keys set the write carries the flags.
	t.Run("no keys set", func(t *testing.T) {
		dir := changedPathsRepo(t, 200, nil)
		_, log := writeIn(t, dir, nil)
		assertWriteCarriesFilters(t, log)
		assertGraphV2Filters(t, graphPath(t, dir))
	})
}
