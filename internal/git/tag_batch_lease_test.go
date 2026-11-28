package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// tagLeaseFixture builds a repository with two commits on main — both
// pushed to a bare origin — and returns the repository, the bare remote's
// directory, the executor-backed Repo, and the two commit hashes. Tags are
// created on the first commit per test, so the second commit is always
// available on the server as a move target.
func tagLeaseFixture(t *testing.T) (dir, bare string, r *Repo, c1, c2 string) {
	t.Helper()
	dir, bare, r = remoteRepo(t)
	writeFile(t, dir, "b.txt", "two\n")
	gitRun(t, dir, "add", "b.txt")
	gitRun(t, dir, "commit", "-q", "-m", "second")
	gitRun(t, dir, "push", "-q", "origin", "main")
	c1 = gitOut(t, dir, "rev-parse", "HEAD~1")
	c2 = gitOut(t, dir, "rev-parse", "HEAD")
	return dir, bare, r, c1, c2
}

// pushTagLeaseFixture tags the first commit as name — annotated when
// annotated, lightweight otherwise — and pushes the tag to origin.
func pushTagLeaseFixture(t *testing.T, dir, name string, annotated bool) {
	t.Helper()
	if annotated {
		gitRun(t, dir, "tag", "-a", "-m", "release "+name, name, "HEAD~1")
	} else {
		gitRun(t, dir, "tag", name, "HEAD~1")
	}
	gitRun(t, dir, "push", "-q", "origin", "refs/tags/"+name)
}

// A tag somebody moved on the server — annotated or lightweight — refuses
// the whole batch delete: nothing is deleted, locally or on the remote, not
// even the tags that still match, and the error names the moved tags and
// the remote.
func TestTagBatchLeaseServerMovedTagsRefuseBatch(t *testing.T) {
	dir, bare, r, _, c2 := tagLeaseFixture(t)
	pushTagLeaseFixture(t, dir, "v1-ann", true)
	pushTagLeaseFixture(t, dir, "v2-light", false)
	pushTagLeaseFixture(t, dir, "v3-keep", false)

	// On the server, behind the local repository's back: v1-ann is recreated
	// onto the newer commit and v2-light is moved there outright.
	gitRun(t, bare, "tag", "-f", "-a", "-m", "moved", "v1-ann", c2)
	gitRun(t, bare, "tag", "-f", "v2-light", c2)
	serverV1 := gitOut(t, bare, "rev-parse", "refs/tags/v1-ann")
	serverV2 := gitOut(t, bare, "rev-parse", "refs/tags/v2-light")

	err := r.DeleteTags([]string{"v1-ann", "v2-light", "v3-keep"}, []string{"origin"})
	if err == nil {
		t.Fatal("deleting server-moved tags produced no error")
	}
	for _, want := range []string{"v1-ann", "v2-light", "origin"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "v3-keep") {
		t.Fatalf("error %q names v3-keep, which still matches", err)
	}
	for _, tag := range []string{"v1-ann", "v2-light", "v3-keep"} {
		if !hasRef(dir, "refs/tags/"+tag) {
			t.Fatalf("local %s was deleted even though the batch was refused", tag)
		}
		if !hasRef(bare, "refs/tags/"+tag) {
			t.Fatalf("server %s was deleted even though the batch was refused", tag)
		}
	}
	if got := gitOut(t, bare, "rev-parse", "refs/tags/v1-ann"); got != serverV1 {
		t.Fatalf("server v1-ann = %q, want the moved %q untouched", got, serverV1)
	}
	if got := gitOut(t, bare, "rev-parse", "refs/tags/v2-light"); got != serverV2 {
		t.Fatalf("server v2-light = %q, want the moved %q untouched", got, serverV2)
	}
}

// An annotated tag deleted on the server and recreated onto the very same
// commit still refuses the batch: its tag object is new, so the object id
// the deletion would pin no longer matches, and the error says so.
func TestTagBatchLeaseRecreatedTagObjectRefusesBatch(t *testing.T) {
	dir, bare, r, c1, _ := tagLeaseFixture(t)
	pushTagLeaseFixture(t, dir, "v1-ann", true)
	pushTagLeaseFixture(t, dir, "v2-light", false)

	gitRun(t, bare, "tag", "-d", "v1-ann")
	gitRun(t, bare, "tag", "-a", "-m", "recreated", "v1-ann", c1)
	if gitOut(t, bare, "rev-parse", "refs/tags/v1-ann") == gitOut(t, dir, "rev-parse", "refs/tags/v1-ann") {
		t.Fatal("fixture broken: the recreated annotated tag kept its object id")
	}

	err := r.DeleteTags([]string{"v1-ann", "v2-light"}, []string{"origin"})
	if err == nil {
		t.Fatal("deleting over a recreated tag object produced no error")
	}
	if !strings.Contains(err.Error(), "v1-ann") || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("error %q does not name v1-ann and origin", err)
	}
	for _, tag := range []string{"v1-ann", "v2-light"} {
		if !hasRef(dir, "refs/tags/"+tag) {
			t.Fatalf("local %s was deleted even though the batch was refused", tag)
		}
	}
}

// A tag the remote never had is skipped without an error: the copies the
// remote does have, and the local tags, are deleted as confirmed.
func TestTagBatchLeaseMissingRemoteTagSkipped(t *testing.T) {
	dir, bare, r, _, _ := tagLeaseFixture(t)
	pushTagLeaseFixture(t, dir, "v1-ann", true)
	gitRun(t, dir, "tag", "v2-local", "HEAD~1") // never pushed

	if err := r.DeleteTags([]string{"v1-ann", "v2-local"}, []string{"origin"}); err != nil {
		t.Fatalf("DeleteTags: %v", err)
	}
	for _, tag := range []string{"v1-ann", "v2-local"} {
		if hasRef(dir, "refs/tags/"+tag) {
			t.Fatalf("local %s survived the confirmed delete", tag)
		}
	}
	if hasRef(bare, "refs/tags/v1-ann") {
		t.Fatal("v1-ann survived on the server")
	}
}

// A remote that cannot be read at all refuses the whole batch — a failed
// read is never a remote without the tags — and the error names the remote
// while every tag stays in place.
func TestTagBatchLeaseUnreadableRemoteRefusesBatch(t *testing.T) {
	dir, _, r, _, _ := tagLeaseFixture(t)
	gitRun(t, dir, "tag", "v1", "HEAD~1")
	gitRun(t, dir, "remote", "add", "gone", t.TempDir()+"/no-such.git")

	err := r.DeleteTags([]string{"v1"}, []string{"gone"})
	if err == nil {
		t.Fatal("deleting over an unreadable remote produced no error")
	}
	if !strings.Contains(err.Error(), "gone") {
		t.Fatalf("error %q does not name the remote", err)
	}
	if !hasRef(dir, "refs/tags/v1") {
		t.Fatal("local v1 was deleted even though the batch was refused")
	}
}

// tamperExecutor wraps an executor and runs tamper exactly once, just before
// the first push it forwards: the window between the verification ls-remote
// and the deletion push, where a concurrent actor can still move a tag.
type tamperExecutor struct {
	inner  *LocalExecutor
	once   sync.Once
	tamper func()
}

func (e *tamperExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "push" {
		e.once.Do(e.tamper)
	}
	return e.inner.Run(ctx, stdin, args...)
}

func (e *tamperExecutor) Describe() string { return e.inner.Describe() }

// A tag moved on the server after the verification but before the push
// fails its lease: the push deletes the tags that still match, the moved
// one keeps its new value, and the error names it and the remote with a
// hint to fetch. The local tags are deleted either way and stay deleted.
func TestTagBatchLeaseLeaseCatchesConcurrentMove(t *testing.T) {
	dir, bare, _, _, c2 := tagLeaseFixture(t)
	pushTagLeaseFixture(t, dir, "v1", false)
	pushTagLeaseFixture(t, dir, "v2", false)

	ex := &tamperExecutor{
		inner: &LocalExecutor{Dir: dir},
		tamper: func() {
			gitRun(t, bare, "tag", "-f", "v1", c2)
		},
	}
	r := New(ex)
	movedOID := gitOut(t, bare, "rev-parse", c2)

	err := r.DeleteTags([]string{"v1", "v2"}, []string{"origin"})
	if err == nil {
		t.Fatal("the lease let a concurrently moved tag be deleted")
	}
	for _, want := range []string{"v1", "origin", "Fetch"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "v2") {
		t.Fatalf("error %q names v2, whose lease held and which was deleted", err)
	}
	if got := gitOut(t, bare, "rev-parse", "refs/tags/v1"); got != movedOID {
		t.Fatalf("server v1 = %q, want the concurrent move %q kept by the lease", got, movedOID)
	}
	if hasRef(bare, "refs/tags/v2") {
		t.Fatal("v2 survived on the server even though nothing moved it")
	}
	for _, tag := range []string{"v1", "v2"} {
		if hasRef(dir, "refs/tags/"+tag) {
			t.Fatalf("local %s survived: the confirmed local deletion is never rolled back", tag)
		}
	}
}

// With two remotes, one matching and one where a tag was moved, the batch
// is refused as a whole: the matching remote keeps its copies too, nothing
// is deleted anywhere, and the error names the mismatching remote and tag.
func TestTagBatchLeaseRefusesWhenOneRemoteMismatch(t *testing.T) {
	dir, bare, r, _, c2 := tagLeaseFixture(t)

	// A second bare remote holding the same commits and tags.
	second := t.TempDir() + "/backup2.git"
	gitRun(t, dir, "init", "-q", "--bare", second)
	gitRun(t, dir, "remote", "add", "backup", second)
	pushTagLeaseFixture(t, dir, "v1", true)
	pushTagLeaseFixture(t, dir, "v2", false)
	gitRun(t, dir, "push", "-q", "backup", "main", "refs/tags/v1", "refs/tags/v2")

	// Only backup's v1 moves; origin still matches exactly.
	gitRun(t, second, "tag", "-f", "v1", c2)

	err := r.DeleteTags([]string{"v1", "v2"}, []string{"origin", "backup"})
	if err == nil {
		t.Fatal("a mismatch on one remote did not refuse the batch")
	}
	if !strings.Contains(err.Error(), "backup") || !strings.Contains(err.Error(), "v1") {
		t.Fatalf("error %q does not name backup and v1", err)
	}
	for _, tag := range []string{"v1", "v2"} {
		if !hasRef(dir, "refs/tags/"+tag) {
			t.Fatalf("local %s was deleted even though the batch was refused", tag)
		}
		if !hasRef(bare, "refs/tags/"+tag) {
			t.Fatalf("origin lost %s even though it matched the local tags", tag)
		}
		if !hasRef(second, "refs/tags/"+tag) {
			t.Fatalf("backup lost %s even though the batch was refused", tag)
		}
	}
}

// The commands of a remote deletion: one for-each-ref reads every local
// tag — over SSH every command is a round trip, so a per-tag rev-parse
// would cost one trip per tag — then one ls-remote and one push per
// remote, the push carrying one force-with-lease per tag pinned to the
// object id the check read, the remote behind the end-of-options
// separator. Without remotes the deletion is the single git tag -d it has
// always been.
func TestTagBatchLeaseCommandShape(t *testing.T) {
	dir, bare, r, _, _ := tagLeaseFixture(t)
	pushTagLeaseFixture(t, dir, "v1-ann", true)
	pushTagLeaseFixture(t, dir, "v2-light", false)
	oidAnn := gitOut(t, bare, "rev-parse", "refs/tags/v1-ann")
	oidLight := gitOut(t, bare, "rev-parse", "refs/tags/v2-light")

	log := NewCommandLog()
	if err := r.WithCommandLog(log).DeleteTags([]string{"v1-ann", "v2-light"}, []string{"origin"}); err != nil {
		t.Fatalf("DeleteTags: %v", err)
	}
	records, _ := log.Snapshot()
	var lsRemote, push, tagDel, forEachRef, revParse []CommandRecord
	for _, rec := range records {
		if len(rec.Args) == 0 {
			continue
		}
		switch rec.Args[0] {
		case "ls-remote":
			lsRemote = append(lsRemote, rec)
		case "push":
			push = append(push, rec)
		case "tag":
			tagDel = append(tagDel, rec)
		case "for-each-ref":
			forEachRef = append(forEachRef, rec)
		case "rev-parse":
			revParse = append(revParse, rec)
		}
	}
	if len(forEachRef) != 1 {
		t.Fatalf("%d for-each-ref invocations, want exactly 1 reading every local tag: %v", len(forEachRef), commandLines(records))
	}
	if len(revParse) != 0 {
		t.Fatalf("%d rev-parse invocations, want none: the local tags read in one command: %v", len(revParse), commandLines(records))
	}
	fea := forEachRef[0].Args
	if !containsArg(fea, "refs/tags/v1-ann") || !containsArg(fea, "refs/tags/v2-light") {
		t.Fatalf("for-each-ref does not ask for both tags: %v", fea)
	}
	if len(lsRemote) != 1 {
		t.Fatalf("%d ls-remote invocations, want exactly 1: %v", len(lsRemote), commandLines(records))
	}
	if len(push) != 1 {
		t.Fatalf("%d push invocations, want exactly 1: %v", len(push), commandLines(records))
	}
	if len(tagDel) != 1 {
		t.Fatalf("%d git tag invocations, want exactly 1: %v", len(tagDel), commandLines(records))
	}
	if !remoteBehindSeparator(lsRemote[0].Args, "origin") {
		t.Fatalf("ls-remote does not put the remote behind --: %v", lsRemote[0].Args)
	}
	args := push[0].Args
	if !remoteBehindSeparator(args, "origin") {
		t.Fatalf("push does not put the remote behind --: %v", args)
	}
	if !containsArg(args, "--delete") {
		t.Fatalf("push lacks --delete: %v", args)
	}
	for _, lease := range []string{
		"--force-with-lease=refs/tags/v1-ann:" + oidAnn,
		"--force-with-lease=refs/tags/v2-light:" + oidLight,
	} {
		if !containsArg(args, lease) {
			t.Fatalf("push lacks lease %q: %v", lease, args)
		}
	}

	// Without remotes: the one git tag -d of before, nothing else.
	gitRun(t, dir, "tag", "v3", "HEAD~1")
	log2 := NewCommandLog()
	if err := r.WithCommandLog(log2).DeleteTags([]string{"v3"}, nil); err != nil {
		t.Fatalf("DeleteTags without remotes: %v", err)
	}
	records2, _ := log2.Snapshot()
	if len(records2) != 1 {
		t.Fatalf("%d commands without remotes, want exactly the one git tag -d: %v", len(records2), commandLines(records2))
	}
	got := records2[0].Args
	want := []string{"tag", "-d", "v3"}
	if len(got) != len(want) {
		t.Fatalf("git tag -d invocation = %v, want exactly %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("git tag -d invocation = %v, want exactly %v", got, want)
		}
	}
}

func commandLines(records []CommandRecord) []string {
	var out []string
	for _, rec := range records {
		out = append(out, strings.Join(rec.Args, " "))
	}
	return out
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// remoteBehindSeparator reports whether args hold "--" immediately before
// the remote name.
func remoteBehindSeparator(args []string, remote string) bool {
	for i, a := range args {
		if a == "--" && i+1 < len(args) && args[i+1] == remote {
			return true
		}
	}
	return false
}

// TagObjectIDs and RemoteTagOIDs only accept exact refname matches: because
// git for-each-ref matches slash-delimited prefixes, asking for v1 when only
// v1/x exists must report v1 absent instead of substituting v1/x.
func TestTagBatchLeaseExactRefMatchIgnoresPrefixChild(t *testing.T) {
	dir, bare, r, _, _ := tagLeaseFixture(t)
	gitRun(t, dir, "tag", "v1/x", "HEAD~1")
	gitRun(t, dir, "tag", "v2", "HEAD~1")
	gitRun(t, dir, "push", "-q", "origin", "refs/tags/v1/x", "refs/tags/v2")

	// Prove git for-each-ref itself prints refs/tags/v1/x for pattern refs/tags/v1.
	if raw := gitOut(t, dir, "for-each-ref", "--format=%(refname)", "refs/tags/v1"); raw != "refs/tags/v1/x" {
		t.Fatalf("fixture broken: for-each-ref refs/tags/v1 = %q, want refs/tags/v1/x", raw)
	}

	gotLocal, err := r.TagObjectIDs([]string{"v1", "v2"})
	if err != nil {
		t.Fatalf("TagObjectIDs: %v", err)
	}
	if _, ok := gotLocal["v1"]; ok {
		t.Fatalf("TagObjectIDs mistook v1/x for v1: %+v", gotLocal)
	}
	if _, ok := gotLocal["v1/x"]; ok {
		t.Fatalf("TagObjectIDs returned unrequested child v1/x: %+v", gotLocal)
	}
	if _, ok := gotLocal["v2"]; !ok {
		t.Fatalf("TagObjectIDs missed exact match v2: %+v", gotLocal)
	}

	gotRemote, err := r.RemoteTagOIDs("origin", []string{"v1", "v2"})
	if err != nil {
		t.Fatalf("RemoteTagOIDs: %v", err)
	}
	if _, ok := gotRemote["v1"]; ok {
		t.Fatalf("RemoteTagOIDs mistook v1/x for v1: %+v", gotRemote)
	}
	if _, ok := gotRemote["v1/x"]; ok {
		t.Fatalf("RemoteTagOIDs returned unrequested child v1/x: %+v", gotRemote)
	}
	if gotRemote["v2"] != gitOut(t, bare, "rev-parse", "refs/tags/v2") {
		t.Fatalf("RemoteTagOIDs v2 = %q, want remote OID", gotRemote["v2"])
	}
}

// A server-side hook declining a tag delete keeps the tag name and porcelain
// rejection reason in the error even when the hook writes nothing to stderr,
// and a mixed batch where one tag fails its lease while another is declined
// by a hook reports both failures without hiding either tag.
func TestTagBatchLeasePorcelainHookAndMixedRejections(t *testing.T) {
	t.Run("silent pre-receive hook keeps tag name and reason", func(t *testing.T) {
		dir, bare, r, _, _ := tagLeaseFixture(t)
		pushTagLeaseFixture(t, dir, "v1", false)

		hook := filepath.Join(bare, "hooks", "pre-receive")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := r.DeleteTags([]string{"v1"}, []string{"origin"})
		if err == nil {
			t.Fatal("delete declined by pre-receive hook produced no error")
		}
		for _, want := range []string{"v1", "origin", "pre-receive hook declined"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not contain %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "Fetch first") {
			t.Fatalf("hook rejection %q was mislabeled as a stale lease", err)
		}
		if !hasRef(bare, "refs/tags/v1") {
			t.Fatal("v1 was deleted on the server despite pre-receive hook declining")
		}
	})

	t.Run("mixed stale lease and hook rejection names both tags", func(t *testing.T) {
		dir, bare, _, _, c2 := tagLeaseFixture(t)
		pushTagLeaseFixture(t, dir, "v1", false)
		pushTagLeaseFixture(t, dir, "v2", false)
		pushTagLeaseFixture(t, dir, "v3", false)

		hook := filepath.Join(bare, "hooks", "update")
		script := "#!/bin/sh\nif [ \"$1\" = \"refs/tags/v2\" ]; then\n  echo \"protected tag v2\" >&2\n  exit 1\nfi\nexit 0\n"
		if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		ex := &tamperExecutor{
			inner: &LocalExecutor{Dir: dir},
			tamper: func() {
				gitRun(t, bare, "tag", "-f", "v1", c2)
			},
		}
		err := New(ex).DeleteTags([]string{"v1", "v2", "v3"}, []string{"origin"})
		if err == nil {
			t.Fatal("mixed stale lease and hook rejection produced no error")
		}
		for _, want := range []string{"v1", "Fetch", "v2", "hook declined", "protected tag v2", "origin"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not contain %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "v3") {
			t.Fatalf("error %q names v3, which was deleted cleanly", err)
		}
		if !hasRef(bare, "refs/tags/v1") || !hasRef(bare, "refs/tags/v2") {
			t.Fatal("rejected tags v1 or v2 were unexpectedly removed from server")
		}
		if hasRef(bare, "refs/tags/v3") {
			t.Fatal("v3 survived on the server even though its lease held and the hook allowed it")
		}
	})
}
