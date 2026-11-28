package git_test

// Refs reads references in two for-each-ref scans — branches and remote
// branches without peeling, then tags peeling each annotated tag — where
// one scan used to peel every reference. These tests pin the split: the
// parsed result matches the one-scan output field for field, locally and
// over loopback SSH, the scan order and the format of each scan, and the
// propagation of a failing scan. The file is package git_test because
// internal/sshtest imports github.com/zhdsmy/branchline/internal/git; a
// same-package test would create an import cycle.

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// refsSplitOneScanFormat is the format of the single for-each-ref scan Refs
// ran before the split, kept here as the oracle every comparison parses.
const refsSplitOneScanFormat = "%(refname)%1f%(objectname)%1f%(upstream:short)%1f%(upstream:track)%1f%(HEAD)%1f%(*objectname)"

// refsSplitReference returns what Refs returned as one scan: ParseRefs over
// a single for-each-ref covering every reference namespace, then the
// remotes git remote names that have no branch yet, then the branch
// refs/remotes/origin/HEAD points at. ex runs git in the repository under
// test, so the oracle reads exactly what Refs reads.
func refsSplitReference(t *testing.T, ex git.Executor) git.Refs {
	t.Helper()
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return ex.Run(ctx, nil, args...)
	}
	out, err := run("for-each-ref", "--format="+refsSplitOneScanFormat, "refs/heads", "refs/remotes", "refs/tags")
	if err != nil {
		t.Fatalf("oracle for-each-ref: %v", err)
	}
	refs := git.ParseRefs(out)
	if out, err := run("remote"); err == nil {
		have := map[string]bool{}
		for _, n := range refs.Remotes {
			have[n] = true
		}
		for _, n := range strings.Fields(string(out)) {
			if !have[n] {
				refs.Remotes = append(refs.Remotes, n)
			}
		}
	}
	if out, err := run("symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		name := strings.TrimSpace(string(out))
		name = strings.TrimPrefix(name, "origin/")
		if name != "" && name != "HEAD" {
			refs.Default = name
		}
	}
	return refs
}

// refsSplitEqual reports the first difference between the Refs the split
// scans produced and the one-scan oracle, section by section so a failure
// names what changed.
func refsSplitEqual(t *testing.T, got, want git.Refs) {
	t.Helper()
	if len(got.Branches) != len(want.Branches) {
		t.Fatalf("branches: got %d %+v, want %d %+v", len(got.Branches), got.Branches, len(want.Branches), want.Branches)
	}
	for i := range want.Branches {
		if got.Branches[i] != want.Branches[i] {
			t.Errorf("branch %d: got %+v, want %+v", i, got.Branches[i], want.Branches[i])
		}
	}
	if len(got.Remote) != len(want.Remote) {
		t.Fatalf("remote branches: got %d %+v, want %d %+v", len(got.Remote), got.Remote, len(want.Remote), want.Remote)
	}
	for i := range want.Remote {
		if got.Remote[i] != want.Remote[i] {
			t.Errorf("remote branch %d: got %+v, want %+v", i, got.Remote[i], want.Remote[i])
		}
	}
	if len(got.Tags) != len(want.Tags) {
		t.Fatalf("tags: got %d %+v, want %d %+v", len(got.Tags), got.Tags, len(want.Tags), want.Tags)
	}
	for i := range want.Tags {
		if got.Tags[i] != want.Tags[i] {
			t.Errorf("tag %d: got %+v, want %+v", i, got.Tags[i], want.Tags[i])
		}
	}
	if !reflect.DeepEqual(got.Remotes, want.Remotes) {
		t.Errorf("remotes: got %v, want %v", got.Remotes, want.Remotes)
	}
	if got.Default != want.Default {
		t.Errorf("default: got %q, want %q", got.Default, want.Default)
	}
}

// refsSplitBuild fills dir with every shape Refs reads: the current branch
// ahead 1 and behind 2 of its upstream, a branch whose upstream was
// deleted, a branch without upstream, lightweight and annotated tags, a
// tag tagging a tag, names with non-ASCII and quoting characters, a remote
// with branches, a remote without any, and origin/HEAD. bare is the empty
// directory the origin remote pushes to. gitRun runs git inside dir and
// write writes a file inside dir, so the same construction serves a local
// repository and one on an SSH server.
func refsSplitBuild(t *testing.T, gitRun func(args ...string) string, write func(relPath, body string), dir, bare string) string {
	t.Helper()
	// Avoid git init -b so the same fixture runs on Git 2.25 servers too.
	gitRun("init", "-q")
	gitRun("symbolic-ref", "HEAD", "refs/heads/main")
	gitRun("config", "user.name", "Ada")
	gitRun("config", "user.email", "ada@example.com")
	gitRun("config", "commit.gpgsign", "false")
	gitRun("config", "advice.nestedTag", "false")
	write("a.txt", "one\n")
	gitRun("add", "a.txt")
	gitRun("commit", "-q", "-m", "base")
	base := gitRun("rev-parse", "HEAD")

	gitRun("init", "-q", "--bare", bare)
	gitRun("remote", "add", "origin", bare)
	gitRun("push", "-q", "-u", "origin", "main")
	gitRun("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	// The upstream gains two commits main never has.
	write("a.txt", "one\nup one\n")
	gitRun("commit", "-q", "-am", "upstream one")
	write("a.txt", "one\nup one\nup two\n")
	gitRun("commit", "-q", "-am", "upstream two")
	gitRun("push", "-q", "origin", "main")
	// main returns to the base and diverges with one commit of its own.
	gitRun("reset", "-q", "--hard", base)
	write("a.txt", "one\nlocal\n")
	gitRun("commit", "-q", "-am", "local work")

	// gone tracks a branch that was deleted from origin after pushing.
	gitRun("branch", "gone")
	gitRun("push", "-q", "-u", "origin", "gone")
	gitRun("push", "-q", "origin", "--delete", "gone")

	gitRun("branch", "plain")
	gitRun("branch", `brænch-中文-quo'te-$dollar-(paren)`)
	gitRun("tag", "light")
	gitRun("tag", "-a", "ann", "-m", "annotated")
	gitRun("tag", "-a", "annmeta", "-m", "tag of a tag", "ann")
	gitRun("tag", `tag-中文-quo'te-(paren)`)

	// spare names a remote no branch was ever fetched from.
	gitRun("remote", "add", "spare", bare)
	return gitRun("rev-parse", "refs/tags/ann")
}

// refsSplitShapes fails when the fixture lost a shape the comparison is
// meant to exercise, so an equal result cannot be vacuous.
func refsSplitShapes(t *testing.T, refs git.Refs, annTagHash string) {
	t.Helper()
	branches := map[string]git.Branch{}
	for _, b := range refs.Branches {
		branches[b.Name] = b
	}
	main, ok := branches["main"]
	if !ok || main.Ahead != 1 || main.Behind != 2 || main.Upstream != "origin/main" {
		t.Errorf("main %+v, want ahead 1 behind 2 of origin/main", main)
	}
	if gone, ok := branches["gone"]; !ok || !gone.Gone || gone.Upstream != "origin/gone" {
		t.Errorf("gone %+v present %v, want a gone upstream", gone, ok)
	}
	if plain, ok := branches["plain"]; !ok || plain.Upstream != "" {
		t.Errorf("plain %+v present %v, want no upstream", plain, ok)
	}
	if special, ok := branches[`brænch-中文-quo'te-$dollar-(paren)`]; !ok {
		t.Errorf("special branch missing, have %v", branches)
	} else if special.Hash == "" {
		t.Errorf("special branch %+v has no hash", special)
	}
	tags := map[string]git.Tag{}
	for _, tag := range refs.Tags {
		tags[tag.Name] = tag
	}
	ann, ok := tags["ann"]
	if !ok || ann.Hash != main.Hash {
		t.Errorf("annotated tag %+v present %v, want the commit main names %s", ann, ok, main.Hash)
	}
	// Older Git peels %(*objectname) one layer to ann's tag object while newer
	// Git peels through to the commit; either way annmeta must peel past its
	// own tag object and match the one-scan oracle exactly.
	if meta, ok := tags["annmeta"]; !ok || (meta.Hash != main.Hash && meta.Hash != annTagHash) {
		t.Errorf("nested tag %+v present %v, want peeled target %s or %s", meta, ok, main.Hash, annTagHash)
	}
	if light, ok := tags["light"]; !ok || light.Hash == "" {
		t.Errorf("lightweight tag %+v present %v", light, ok)
	}
	if _, ok := tags[`tag-中文-quo'te-(paren)`]; !ok {
		t.Errorf("special tag missing, have %v", tags)
	}
	var spare bool
	for _, n := range refs.Remotes {
		if n == "spare" {
			spare = true
		}
	}
	if !spare {
		t.Errorf("remotes %v lack spare", refs.Remotes)
	}
	if refs.Default != "main" {
		t.Errorf("default %q, want main", refs.Default)
	}
}

// TestRefsSplitMatchesOneScanLocally checks Refs against the one-scan
// oracle on a repository covering every reference shape, first with HEAD
// on main and again with HEAD detached.
func TestRefsSplitMatchesOneScanLocally(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(t.TempDir(), "origin.git")
	gitRun := func(args ...string) string { return localGit(t, dir, args...) }
	write := func(relPath, body string) { writeFileLocal(t, dir, relPath, body) }
	annTagHash := refsSplitBuild(t, gitRun, write, dir, bare)

	repo := git.New(&git.LocalExecutor{Dir: dir, NoGlobalConfig: true})
	oracle := &git.LocalExecutor{Dir: dir, NoGlobalConfig: true}

	got, err := repo.Refs()
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	refsSplitShapes(t, got, annTagHash)
	refsSplitEqual(t, got, refsSplitReference(t, oracle))

	localGit(t, dir, "checkout", "-q", "--detach")
	got, err = repo.Refs()
	if err != nil {
		t.Fatalf("Refs detached: %v", err)
	}
	refsSplitEqual(t, got, refsSplitReference(t, oracle))
	for _, b := range got.Branches {
		if b.Current {
			t.Errorf("detached HEAD still marks %+v current", b)
		}
	}
}

// TestRefsSplitEmptyRepository checks an unborn repository scans clean:
// no commit and no tag is not an error, and the result is the oracle's.
func TestRefsSplitEmptyRepository(t *testing.T) {
	dir := t.TempDir()
	localGit(t, dir, "init", "-q")
	localGit(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
	localGit(t, dir, "config", "user.name", "Ada")
	localGit(t, dir, "config", "user.email", "ada@example.com")

	repo := git.New(&git.LocalExecutor{Dir: dir, NoGlobalConfig: true})
	got, err := repo.Refs()
	if err != nil {
		t.Fatalf("Refs on an empty repository: %v", err)
	}
	refsSplitEqual(t, got, refsSplitReference(t, &git.LocalExecutor{Dir: dir, NoGlobalConfig: true}))
	if len(got.Branches)+len(got.Remote)+len(got.Tags) != 0 {
		t.Errorf("empty repository has %+v", got)
	}
}

// TestRefsSplitBoundaryShapes checks the concatenation boundary between the
// branch scan and the tag scan: branches without tags, tags without branches,
// both empty, and a branch scan output missing its trailing newline never
// merges the last branch with the first tag.
func TestRefsSplitBoundaryShapes(t *testing.T) {
	t.Run("BranchesOnly", func(t *testing.T) {
		dir := t.TempDir()
		localGit(t, dir, "init", "-q")
		localGit(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
		localGit(t, dir, "config", "user.name", "Ada")
		localGit(t, dir, "config", "user.email", "ada@example.com")
		localGit(t, dir, "config", "commit.gpgsign", "false")
		writeFileLocal(t, dir, "a.txt", "one\n")
		localGit(t, dir, "add", "a.txt")
		localGit(t, dir, "commit", "-q", "-m", "base")
		localGit(t, dir, "branch", "feature")

		ex := &git.LocalExecutor{Dir: dir, NoGlobalConfig: true}
		got, err := git.New(ex).Refs()
		if err != nil {
			t.Fatalf("Refs with branches only: %v", err)
		}
		if len(got.Branches) != 2 || len(got.Tags) != 0 {
			t.Fatalf("branches=%+v tags=%+v, want 2 branches and 0 tags", got.Branches, got.Tags)
		}
		refsSplitEqual(t, got, refsSplitReference(t, ex))
	})

	t.Run("TagsOnly", func(t *testing.T) {
		dir := t.TempDir()
		localGit(t, dir, "init", "-q")
		localGit(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
		localGit(t, dir, "config", "user.name", "Ada")
		localGit(t, dir, "config", "user.email", "ada@example.com")
		localGit(t, dir, "config", "commit.gpgsign", "false")
		writeFileLocal(t, dir, "a.txt", "one\n")
		localGit(t, dir, "add", "a.txt")
		localGit(t, dir, "commit", "-q", "-m", "base")
		head := localGit(t, dir, "rev-parse", "HEAD")
		localGit(t, dir, "tag", "light")
		localGit(t, dir, "tag", "-a", "ann", "-m", "annotated")
		localGit(t, dir, "checkout", "-q", "--detach")
		localGit(t, dir, "update-ref", "-d", "refs/heads/main")

		ex := &git.LocalExecutor{Dir: dir, NoGlobalConfig: true}
		got, err := git.New(ex).Refs()
		if err != nil {
			t.Fatalf("Refs with tags only: %v", err)
		}
		if len(got.Branches) != 0 || len(got.Remote) != 0 || len(got.Tags) != 2 {
			t.Fatalf("branches=%+v remote=%+v tags=%+v, want 0 branches and 2 tags", got.Branches, got.Remote, got.Tags)
		}
		var annHash string
		for _, tag := range got.Tags {
			if tag.Name == "ann" {
				annHash = tag.Hash
			}
		}
		if annHash != head {
			t.Fatalf("annotated tag hash = %q, want commit %q", annHash, head)
		}
		refsSplitEqual(t, got, refsSplitReference(t, ex))
	})

	t.Run("MissingTrailingNewline", func(t *testing.T) {
		ex := &refsSplitOutputStub{
			branchesOut: []byte("refs/heads/main\x1f1111111\x1f\x1f\x1f*\x1f"),
			tagsOut:     []byte("refs/tags/v1\x1f2222222\x1f\x1f\x1f\x1f3333333\n"),
		}
		got, err := git.New(ex).Refs()
		if err != nil {
			t.Fatalf("Refs with unterminated branch output: %v", err)
		}
		if len(got.Branches) != 1 || got.Branches[0].Name != "main" {
			t.Fatalf("branches = %+v, want [main]", got.Branches)
		}
		if len(got.Tags) != 1 || got.Tags[0].Name != "v1" || got.Tags[0].Hash != "3333333" {
			t.Fatalf("tags = %+v, want [v1 -> 3333333]", got.Tags)
		}
	})
}

type refsSplitOutputStub struct {
	branchesOut []byte
	tagsOut     []byte
}

func (e *refsSplitOutputStub) Describe() string { return "output stub" }

func (e *refsSplitOutputStub) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "for-each-ref" {
		if refsSplitHasArg(args, "refs/tags") {
			return append([]byte(nil), e.tagsOut...), nil
		}
		return append([]byte(nil), e.branchesOut...), nil
	}
	return nil, nil
}

// refsSplitRecorder is an Executor that records its invocations and can
// fail the scan of refs/tags.
type refsSplitRecorder struct {
	calls    [][]string
	failTags bool
}

func (e *refsSplitRecorder) Describe() string { return "recording test" }

func (e *refsSplitRecorder) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	e.calls = append(e.calls, append([]string(nil), args...))
	if e.failTags && len(args) > 0 && args[0] == "for-each-ref" {
		for _, a := range args {
			if a == "refs/tags" {
				return nil, &git.Error{Args: args, ExitCode: 128, Stderr: "cannot read refs/tags"}
			}
		}
	}
	return nil, nil
}

// TestRefsSplitCommandSequence checks Refs runs the branch scan, the tag
// scan, remote and symbolic-ref in order, the branch scan asking for no
// peeled field and the tag scan peeling but reading no upstream.
func TestRefsSplitCommandSequence(t *testing.T) {
	ex := &refsSplitRecorder{}
	if _, err := git.New(ex).Refs(); err != nil {
		t.Fatalf("Refs: %v", err)
	}
	if len(ex.calls) != 4 {
		t.Fatalf("commands %#v, want 4", ex.calls)
	}
	branchScan, tagScan := ex.calls[0], ex.calls[1]
	if len(branchScan) == 0 || branchScan[0] != "for-each-ref" {
		t.Fatalf("first command %#v, want for-each-ref", branchScan)
	}
	for _, want := range []string{"refs/heads", "refs/remotes"} {
		if !refsSplitHasArg(branchScan, want) {
			t.Errorf("branch scan %#v lacks %s", branchScan, want)
		}
	}
	if refsSplitHasArg(branchScan, "refs/tags") {
		t.Errorf("branch scan %#v reads the tags", branchScan)
	}
	if format := refsSplitFormatArg(t, branchScan); strings.Contains(format, "%(*") {
		t.Errorf("branch scan format %q peels", format)
	}
	if len(tagScan) == 0 || tagScan[0] != "for-each-ref" {
		t.Fatalf("second command %#v, want for-each-ref", tagScan)
	}
	if !refsSplitHasArg(tagScan, "refs/tags") {
		t.Errorf("tag scan %#v lacks refs/tags", tagScan)
	}
	if refsSplitHasArg(tagScan, "refs/heads") || refsSplitHasArg(tagScan, "refs/remotes") {
		t.Errorf("tag scan %#v reads the branches", tagScan)
	}
	format := refsSplitFormatArg(t, tagScan)
	if !strings.Contains(format, "%(*objectname)") {
		t.Errorf("tag scan format %q does not peel", format)
	}
	if strings.Contains(format, "upstream") {
		t.Errorf("tag scan format %q reads the upstream", format)
	}
	if remote := ex.calls[2]; len(remote) != 1 || remote[0] != "remote" {
		t.Errorf("third command %#v, want git remote", remote)
	}
	if symref := ex.calls[3]; len(symref) == 0 || symref[0] != "symbolic-ref" {
		t.Errorf("fourth command %#v, want symbolic-ref", symref)
	}
}

func refsSplitHasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func refsSplitFormatArg(t *testing.T, args []string) string {
	t.Helper()
	for _, a := range args {
		if format, ok := strings.CutPrefix(a, "--format="); ok {
			return format
		}
	}
	t.Fatalf("command %#v has no --format", args)
	return ""
}

// TestRefsSplitTagScanFailure checks a failing tag scan fails Refs instead
// of returning the branches alone.
func TestRefsSplitTagScanFailure(t *testing.T) {
	ex := &refsSplitRecorder{failTags: true}
	if _, err := git.New(ex).Refs(); err == nil {
		t.Fatal("a failing tag scan returned no error")
	} else if !strings.Contains(err.Error(), "cannot read refs/tags") {
		t.Fatalf("error %v does not come from the tag scan", err)
	}
}

// runRefsSplitOverSSH checks Refs against the one-scan oracle over an SSH
// target (loopback sshd or a configured Linux server, including Git 2.25),
// covering full branch/tag/upstream shapes (attached and detached HEAD) plus
// branches-only, tags-only, and empty repositories.
func runRefsSplitOverSSH(t *testing.T, srv *sshtest.Target) {
	t.Helper()

	dir := srv.TempDir(t, "refs-split-repo")
	bare := srv.TempDir(t, "refs-split-bare")
	gitRun := func(args ...string) string { return srv.RunGit(t, dir, args...) }
	write := func(relPath, body string) { srv.WriteFile(t, dir, relPath, []byte(body)) }
	annTagHash := refsSplitBuild(t, gitRun, write, dir, bare)

	repo := srv.Repo(t, dir)
	oracle := srv.Executor(t, dir)

	got, err := repo.Refs()
	if err != nil {
		t.Fatalf("Refs over SSH: %v", err)
	}
	refsSplitShapes(t, got, annTagHash)
	refsSplitEqual(t, got, refsSplitReference(t, oracle))

	gitRun("checkout", "-q", "--detach")
	got, err = repo.Refs()
	if err != nil {
		t.Fatalf("Refs detached over SSH: %v", err)
	}
	refsSplitEqual(t, got, refsSplitReference(t, oracle))

	// Empty repository over SSH.
	emptyDir := srv.TempDir(t, "refs-split-empty")
	srv.RunGit(t, emptyDir, "init", "-q")
	srv.RunGit(t, emptyDir, "symbolic-ref", "HEAD", "refs/heads/main")
	got, err = srv.Repo(t, emptyDir).Refs()
	if err != nil {
		t.Fatalf("Refs on empty SSH repo: %v", err)
	}
	refsSplitEqual(t, got, refsSplitReference(t, srv.Executor(t, emptyDir)))

	// Branches-only repository over SSH.
	branchesDir := srv.TempDir(t, "refs-split-branches-only")
	srv.RunGit(t, branchesDir, "init", "-q")
	srv.RunGit(t, branchesDir, "symbolic-ref", "HEAD", "refs/heads/main")
	srv.RunGit(t, branchesDir, "config", "user.name", "Ada")
	srv.RunGit(t, branchesDir, "config", "user.email", "ada@example.com")
	srv.RunGit(t, branchesDir, "config", "commit.gpgsign", "false")
	srv.WriteFile(t, branchesDir, "a.txt", []byte("one\n"))
	srv.RunGit(t, branchesDir, "add", "a.txt")
	srv.RunGit(t, branchesDir, "commit", "-q", "-m", "base")
	got, err = srv.Repo(t, branchesDir).Refs()
	if err != nil {
		t.Fatalf("Refs on branches-only SSH repo: %v", err)
	}
	if len(got.Branches) != 1 || len(got.Tags) != 0 {
		t.Fatalf("branches-only SSH repo: branches=%+v tags=%+v", got.Branches, got.Tags)
	}
	refsSplitEqual(t, got, refsSplitReference(t, srv.Executor(t, branchesDir)))

	// Tags-only repository over SSH (detached HEAD, no branches).
	srv.RunGit(t, branchesDir, "tag", "-a", "only-tag", "-m", "annotated")
	srv.RunGit(t, branchesDir, "checkout", "-q", "--detach")
	srv.RunGit(t, branchesDir, "update-ref", "-d", "refs/heads/main")
	got, err = srv.Repo(t, branchesDir).Refs()
	if err != nil {
		t.Fatalf("Refs on tags-only SSH repo: %v", err)
	}
	if len(got.Branches) != 0 || len(got.Tags) != 1 {
		t.Fatalf("tags-only SSH repo: branches=%+v tags=%+v", got.Branches, got.Tags)
	}
	refsSplitEqual(t, got, refsSplitReference(t, srv.Executor(t, branchesDir)))
}

// TestRefsSplitLoopbackSSH checks Refs against the one-scan oracle over
// loopback SSH, on a repository holding annotated tags and upstream
// branches. BRANCHLINE_REQUIRE_SSH=1 makes a missing loopback sshd fatal.
func TestRefsSplitLoopbackSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)
	runRefsSplitOverSSH(t, srv)
}

// TestConfiguredLinuxRefsSplit checks Refs against the one-scan oracle on a
// configured Linux SSH server, including Git 2.25 environments.
func TestConfiguredLinuxRefsSplit(t *testing.T) {
	srv := sshtest.ConfiguredLinux(t)
	runRefsSplitOverSSH(t, srv)
}
