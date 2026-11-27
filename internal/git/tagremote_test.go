package git

import (
	"errors"
	"strings"
	"testing"
)

// tagPushRepo builds a repository with two commits on main and a bare origin
// holding main but no tags, returning the repo directory, the bare directory
// and the two commit hashes, oldest first.
func tagPushRepo(t *testing.T) (dir, bare, first, second string) {
	t.Helper()
	dir = initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "first")
	first = gitOut(t, dir, "rev-parse", "HEAD")
	writeFile(t, dir, "a.txt", "one\ntwo\n")
	gitRun(t, dir, "commit", "-q", "-am", "second")
	second = gitOut(t, dir, "rev-parse", "HEAD")
	bare = t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare")
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "-q", "origin", "main")
	return dir, bare, first, second
}

// remoteTagRefOID returns the exact object refs/tags/<name> points at in the
// bare repository — the tag object of an annotated tag, the commit of a
// lightweight one.
func remoteTagRefOID(t *testing.T, bare, name string) string {
	t.Helper()
	return gitOut(t, bare, "rev-parse", "refs/tags/"+name)
}

// RemoteTagOID answers the exact ref object id of lightweight and annotated
// tags on the remote — never the peeled commit of an annotated tag — and
// reports a missing tag without an error.
func TestTagRemoteOIDLightweightAnnotatedAndMissing(t *testing.T) {
	dir, bare, _, second := tagPushRepo(t)
	gitRun(t, dir, "tag", "lw", "HEAD")
	gitRun(t, dir, "tag", "-a", "ann", "-m", "notes", "HEAD")
	gitRun(t, dir, "push", "-q", "origin", "refs/tags/lw", "refs/tags/ann")
	r := New(&LocalExecutor{Dir: dir})

	oid, exists, err := r.RemoteTagOID("origin", "lw")
	if err != nil || !exists {
		t.Fatalf("RemoteTagOID lw = (%q, %v, %v), want the tag present", oid, exists, err)
	}
	if oid != second {
		t.Fatalf("RemoteTagOID lw = %q, want the commit %q the lightweight tag names", oid, second)
	}

	oid, exists, err = r.RemoteTagOID("origin", "ann")
	if err != nil || !exists {
		t.Fatalf("RemoteTagOID ann = (%q, %v, %v), want the tag present", oid, exists, err)
	}
	tagObj := strings.TrimSpace(gitOut(t, bare, "rev-parse", "refs/tags/ann"))
	if oid != tagObj {
		t.Fatalf("RemoteTagOID ann = %q, want the tag object %q, not the peeled commit %q", oid, tagObj, second)
	}
	if oid == second {
		t.Fatalf("RemoteTagOID ann answered the peeled commit %q instead of the tag object", oid)
	}

	oid, exists, err = r.RemoteTagOID("origin", "nothere")
	if err != nil || exists || oid != "" {
		t.Fatalf("RemoteTagOID missing = (%q, %v, %v), want empty, false, nil", oid, exists, err)
	}
}

// A remote that cannot answer is an error, never an absent tag: only a
// successful listing may say the remote does not have the tag.
func TestTagRemoteOIDBrokenRemoteIsAnError(t *testing.T) {
	dir, _, _, _ := tagPushRepo(t)
	gitRun(t, dir, "remote", "set-url", "origin", t.TempDir()+"/gone.git")
	r := New(&LocalExecutor{Dir: dir})

	oid, exists, err := r.RemoteTagOID("origin", "lw")
	if err == nil {
		t.Fatal("RemoteTagOID against a broken remote returned no error")
	}
	if exists || oid != "" {
		t.Fatalf("RemoteTagOID broken remote = (%q, %v), want empty and false besides the error", oid, exists)
	}

	// Invalid names are rejected before any network round-trip.
	for _, tc := range []struct{ remote, name string }{
		{"", "lw"},
		{"-o", "lw"},
		{"has space", "lw"},
		{"origin", ""},
		{"origin", "-v"},
		{"origin", "has space"},
	} {
		if _, _, err := r.RemoteTagOID(tc.remote, tc.name); err == nil {
			t.Errorf("RemoteTagOID(%q, %q) accepted an invalid argument", tc.remote, tc.name)
		}
	}
}

// SafePushTag creates the tag on the remote when it is new, reports
// UpToDatePush when the remote already has the identical object, and never
// overwrites a remote tag pointing anywhere else.
func TestTagRemoteSafePushTag(t *testing.T) {
	dir, bare, first, second := tagPushRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	gitRun(t, dir, "tag", "v1", "HEAD")

	note, err := r.SafePushTag("origin", "v1")
	if err != nil {
		t.Fatalf("SafePushTag: %v", err)
	}
	if note == UpToDatePush {
		t.Fatal("the first push of v1 reported everything up-to-date")
	}
	if !hasRef(bare, "refs/tags/v1") {
		t.Fatal("v1 missing on the remote after SafePushTag")
	}
	if got := remoteTagRefOID(t, bare, "v1"); got != second {
		t.Fatalf("remote v1 = %q, want HEAD %q", got, second)
	}

	// The identical push is up-to-date.
	note, err = r.SafePushTag("origin", "v1")
	if err != nil {
		t.Fatalf("SafePushTag repeat: %v", err)
	}
	if note != UpToDatePush {
		t.Fatalf("repeat push note = %q, want %q", note, UpToDatePush)
	}

	// The remote's v1 moves away externally: pushing again is refused and the
	// remote keeps its object.
	gitRun(t, bare, "update-ref", "refs/tags/v1", first)
	if _, err := r.SafePushTag("origin", "v1"); err == nil {
		t.Fatal("pushing over a diverged remote tag produced no error")
	}
	if got := remoteTagRefOID(t, bare, "v1"); got != first {
		t.Fatalf("remote v1 = %q, want the diverged %q untouched", got, first)
	}
}

// SafePushTag never pushes what is not there: a missing local tag is an error
// and nothing reaches the remote.
func TestTagRemoteSafePushTagMissingLocal(t *testing.T) {
	dir, bare, _, _ := tagPushRepo(t)
	r := New(&LocalExecutor{Dir: dir})

	if _, err := r.SafePushTag("origin", "ghost"); err == nil {
		t.Fatal("pushing a nonexistent local tag produced no error")
	}
	if hasRef(bare, "refs/tags/ghost") {
		t.Fatal("ghost appeared on the remote")
	}
}

// The guards of SafeForcePushTag refuse before any push: a failed check, and
// a missing expected object id for a remote that does have the tag.
func TestTagRemoteForcePushGuardsRefuseWithoutPushing(t *testing.T) {
	dir, bare, first, second := tagPushRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	gitRun(t, dir, "tag", "v1", "HEAD")
	gitRun(t, dir, "push", "-q", "origin", "refs/tags/v1")
	gitRun(t, bare, "update-ref", "refs/tags/v1", first)

	checkErr := errors.New("the probe failed")
	if err := r.SafeForcePushTag("origin", "v1", second, true, checkErr); err == nil {
		t.Fatal("force push with a failed check produced no error")
	} else if !strings.Contains(err.Error(), "could not be verified") {
		t.Fatalf("check error refusal = %q, want it to say the state could not be verified", err)
	}
	if got := remoteTagRefOID(t, bare, "v1"); got != first {
		t.Fatalf("remote v1 moved to %q despite the failed check, want %q", got, first)
	}

	if err := r.SafeForcePushTag("origin", "v1", "", true, nil); err == nil {
		t.Fatal("force push without the verified object id produced no error")
	}
	if got := remoteTagRefOID(t, bare, "v1"); got != first {
		t.Fatalf("remote v1 moved to %q without the verified object id, want %q", got, first)
	}

	if err := r.SafeForcePushTag("origin", "ghost", "", false, nil); err == nil {
		t.Fatal("force push of a nonexistent local tag produced no error")
	}
	if hasRef(bare, "refs/tags/ghost") {
		t.Fatal("ghost appeared on the remote")
	}
}

// A correct lease overwrites lightweight and annotated remote tags; a lease
// pinned to what the remote no longer has is refused atomically and the
// remote keeps its object.
func TestTagRemoteForcePushLeaseSemantics(t *testing.T) {
	t.Run("lightweight overwrite and stale refusal", func(t *testing.T) {
		dir, bare, first, second := tagPushRepo(t)
		r := New(&LocalExecutor{Dir: dir})
		gitRun(t, dir, "tag", "v1", "HEAD")
		gitRun(t, dir, "push", "-q", "origin", "refs/tags/v1")

		// The remote moves onto the first commit behind the client's back
		// while the local v1 stays on HEAD: the lease pinned to the remote's
		// current object id overwrites it back onto HEAD.
		gitRun(t, bare, "update-ref", "refs/tags/v1", first)
		if err := r.SafeForcePushTag("origin", "v1", first, true, nil); err != nil {
			t.Fatalf("force push with the current lease: %v", err)
		}
		if got := remoteTagRefOID(t, bare, "v1"); got != second {
			t.Fatalf("remote v1 = %q, want the force-pushed HEAD %q", got, second)
		}

		// The remote moves again after the probe answered: the lease now pins
		// a stale object id and must be refused, leaving the remote untouched.
		gitRun(t, bare, "update-ref", "refs/tags/v1", first)
		if err := r.SafeForcePushTag("origin", "v1", second, true, nil); err == nil {
			t.Fatal("force push with a stale lease produced no error")
		}
		if got := remoteTagRefOID(t, bare, "v1"); got != first {
			t.Fatalf("remote v1 = %q after the stale lease, want %q untouched", got, first)
		}
		if got := remoteTagRefOID(t, bare, "v1"); got == second {
			t.Fatal("the stale lease overwrote the remote tag")
		}
	})

	t.Run("annotated overwrite", func(t *testing.T) {
		dir, bare, _, _ := tagPushRepo(t)
		r := New(&LocalExecutor{Dir: dir})
		gitRun(t, dir, "tag", "-a", "rel", "-m", "one", "HEAD")
		gitRun(t, dir, "push", "-q", "origin", "refs/tags/rel")
		remoteObj := remoteTagRefOID(t, bare, "rel")

		// The local annotated tag is recreated with a different message: a
		// different tag object even onto the same commit, so overwriting it is
		// observable in both the object id and the message.
		gitRun(t, dir, "tag", "-d", "rel")
		gitRun(t, dir, "tag", "-a", "rel", "-m", "two", "HEAD")
		localObj := strings.TrimSpace(gitOut(t, dir, "rev-parse", "refs/tags/rel"))
		if remoteObj == localObj {
			t.Fatal("fixture: local and remote annotated tags share their object id")
		}

		if err := r.SafeForcePushTag("origin", "rel", remoteObj, true, nil); err != nil {
			t.Fatalf("annotated force push with the tag object lease: %v", err)
		}
		if got := remoteTagRefOID(t, bare, "rel"); got != localObj {
			t.Fatalf("remote rel = %q, want the local tag object %q", got, localObj)
		}
		if got := gitOut(t, bare, "for-each-ref", "--format=%(contents)", "refs/tags/rel"); strings.TrimSpace(got) != "two" {
			t.Fatalf("remote rel message = %q, want %q", got, "two")
		}
	})

	t.Run("empty lease refuses a concurrent create", func(t *testing.T) {
		dir, bare, first, second := tagPushRepo(t)
		r := New(&LocalExecutor{Dir: dir})
		gitRun(t, dir, "tag", "v9", "HEAD")

		// The probe said the remote has no v9, so the lease expectation is
		// empty; someone else creates it in the meantime. The push is refused
		// atomically and the concurrent tag survives.
		gitRun(t, bare, "update-ref", "refs/tags/v9", first)
		if err := r.SafeForcePushTag("origin", "v9", "", false, nil); err == nil {
			t.Fatal("force push over a concurrently created tag produced no error")
		}
		if got := remoteTagRefOID(t, bare, "v9"); got != first {
			t.Fatalf("remote v9 = %q, want the concurrent %q untouched", got, first)
		}
		if got := remoteTagRefOID(t, bare, "v9"); got == second {
			t.Fatal("the empty lease overwrote the concurrently created tag")
		}

		// With the remote still empty the same empty lease creates the tag.
		gitRun(t, bare, "update-ref", "-d", "refs/tags/v9")
		if err := r.SafeForcePushTag("origin", "v9", "", false, nil); err != nil {
			t.Fatalf("force push onto an empty remote: %v", err)
		}
		if got := remoteTagRefOID(t, bare, "v9"); got != second {
			t.Fatalf("remote v9 = %q, want HEAD %q", got, second)
		}
	})
}

// SafePushAllTags sends every local tag, never forced: a diverged remote tag
// is refused and keeps its object, while an identical one reads as up-to-date.
func TestTagRemoteSafePushAllTags(t *testing.T) {
	dir, bare, first, second := tagPushRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	gitRun(t, dir, "tag", "v1", "HEAD")
	gitRun(t, dir, "tag", "-a", "v2", "-m", "notes", first)

	note, err := r.SafePushAllTags("origin")
	if err != nil {
		t.Fatalf("SafePushAllTags: %v", err)
	}
	if note == UpToDatePush {
		t.Fatal("the first push of all tags reported everything up-to-date")
	}
	for _, name := range []string{"v1", "v2"} {
		if !hasRef(bare, "refs/tags/"+name) {
			t.Fatalf("%s missing on the remote after push --tags", name)
		}
	}
	if got := remoteTagRefOID(t, bare, "v1"); got != second {
		t.Fatalf("remote v1 = %q, want HEAD %q", got, second)
	}

	note, err = r.SafePushAllTags("origin")
	if err != nil {
		t.Fatalf("SafePushAllTags repeat: %v", err)
	}
	if note != UpToDatePush {
		t.Fatalf("repeat push note = %q, want %q", note, UpToDatePush)
	}

	// Diverge v1 on the remote: pushing all tags is refused and the diverged
	// tag keeps its object.
	gitRun(t, bare, "update-ref", "refs/tags/v1", first)
	if _, err := r.SafePushAllTags("origin"); err == nil {
		t.Fatal("pushing all tags over a diverged remote tag produced no error")
	}
	if got := remoteTagRefOID(t, bare, "v1"); got != first {
		t.Fatalf("remote v1 = %q, want the diverged %q untouched", got, first)
	}
}

// Without a single local tag there is nothing to send, and invalid remote
// names are rejected.
func TestTagRemoteSafePushAllTagsValidation(t *testing.T) {
	dir, _, _, _ := tagPushRepo(t)
	r := New(&LocalExecutor{Dir: dir})

	if _, err := r.SafePushAllTags("origin"); err == nil {
		t.Fatal("pushing all tags without any local tag produced no error")
	} else if !strings.Contains(err.Error(), "No local tags to push.") {
		t.Fatalf("empty tag push error = %q, want it to say there is nothing to push", err)
	}

	gitRun(t, dir, "tag", "v1", "HEAD")
	for _, remote := range []string{"", "-o", "has space"} {
		if _, err := r.SafePushAllTags(remote); err == nil {
			t.Errorf("SafePushAllTags(%q) accepted an invalid remote", remote)
		}
	}
}

// SafeCreateTagAndPush creates the tag and sends it; a failed push leaves the
// local tag in place and the error says so.
func TestTagRemoteSafeCreateTagAndPush(t *testing.T) {
	dir, bare, _, second := tagPushRepo(t)
	r := New(&LocalExecutor{Dir: dir})

	if err := r.SafeCreateTagAndPush("v1", "HEAD", "release notes", "origin"); err != nil {
		t.Fatalf("SafeCreateTagAndPush: %v", err)
	}
	if got := gitOut(t, dir, "cat-file", "-t", "refs/tags/v1"); got != "tag" {
		t.Fatalf("v1 object type = %q, want annotated", got)
	}
	if got := gitOut(t, bare, "rev-parse", "refs/tags/v1^{}"); got != second {
		t.Fatalf("remote v1 commit = %q, want HEAD %q", got, second)
	}

	// A missing remote fails the push but keeps the local tag.
	if err := r.SafeCreateTagAndPush("v2", "HEAD", "", "missing"); err == nil {
		t.Fatal("pushing to a missing remote produced no error")
	} else if !strings.Contains(err.Error(), "was created") {
		t.Fatalf("failed push error = %q, want it to say the local tag was created", err)
	}
	if !tagExists(t, dir, "v2") {
		t.Fatal("the local v2 was lost when its push failed")
	}

	// A create failure never reaches the push: the conflicting local tag
	// blocks v1's recreation and nothing on the remote changes.
	if err := r.SafeCreateTagAndPush("v1", "HEAD", "", "origin"); err == nil {
		t.Fatal("recreating the existing v1 produced no error")
	}
}

// A broken ls-remote makes the UI's probe fail, and the refused command never
// pushes: demonstrated end to end by probing and submitting the probe's error.
func TestTagRemoteProbeFailureNeverPushes(t *testing.T) {
	dir, bare, _, _ := tagPushRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	gitRun(t, dir, "tag", "v1", "HEAD")
	gitRun(t, dir, "remote", "set-url", "origin", t.TempDir()+"/gone.git")

	oid, exists, checkErr := r.RemoteTagOID("origin", "v1")
	if checkErr == nil {
		t.Fatal("the probe against the broken remote produced no error")
	}
	if err := r.SafeForcePushTag("origin", "v1", oid, exists, checkErr); err == nil {
		t.Fatal("force push after a failed probe produced no error")
	}
	if hasRef(bare, "refs/tags/v1") {
		t.Fatal("v1 reached the remote despite the failed probe")
	}
}
