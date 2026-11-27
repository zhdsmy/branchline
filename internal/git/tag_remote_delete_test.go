package git

import (
	"errors"
	"strings"
	"testing"
)

// SafeDeleteRemoteTag removes lightweight and annotated tags from the remote
// when the lease pins exactly what the remote points at, and the local tags
// survive every deletion untouched.
func TestSafeDeleteRemoteTagLightweightAndAnnotated(t *testing.T) {
	dir, bare, _, _ := tagPushRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	gitRun(t, dir, "tag", "lw", "HEAD")
	gitRun(t, dir, "tag", "-a", "ann", "-m", "notes", "HEAD")
	gitRun(t, dir, "push", "-q", "origin", "refs/tags/lw", "refs/tags/ann")

	lwOID := remoteTagRefOID(t, bare, "lw")
	if err := r.SafeDeleteRemoteTag("origin", "lw", lwOID, nil); err != nil {
		t.Fatalf("SafeDeleteRemoteTag lightweight: %v", err)
	}
	if hasRef(bare, "refs/tags/lw") {
		t.Fatal("lw survived on the remote after the confirmed deletion")
	}
	if !hasRef(dir, "refs/tags/lw") {
		t.Fatal("the remote deletion removed the local lw tag")
	}

	// The annotated lease pins the tag object, never the peeled commit: the
	// object id ls-remote answers is what remoteTagRefOID reads here.
	annOID := remoteTagRefOID(t, bare, "ann")
	if err := r.SafeDeleteRemoteTag("origin", "ann", annOID, nil); err != nil {
		t.Fatalf("SafeDeleteRemoteTag annotated: %v", err)
	}
	if hasRef(bare, "refs/tags/ann") {
		t.Fatal("ann survived on the remote after the confirmed deletion")
	}
	if !hasRef(dir, "refs/tags/ann") {
		t.Fatal("the remote deletion removed the local ann tag")
	}
}

// A remote tag someone else moved since the check is never deleted: the lease
// pinned to the verified object id is stale, git refuses atomically, and the
// server keeps its new value. The retargeting is done through a second clone
// force-pushing the same tag name, the way a real concurrent client would.
func TestSafeDeleteRemoteTagStaleLeaseProtects(t *testing.T) {
	dir, bare, first, second := tagPushRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	gitRun(t, dir, "tag", "v1", "HEAD")
	gitRun(t, dir, "push", "-q", "origin", "refs/tags/v1")
	checked := remoteTagRefOID(t, bare, "v1")
	if checked != second {
		t.Fatalf("fixture: remote v1 = %q, want HEAD %q before the retarget", checked, second)
	}

	// A second clone force-pushes the same tag onto the first commit.
	other := t.TempDir()
	gitRun(t, t.TempDir(), "clone", "-q", bare, other)
	gitRun(t, other, "config", "user.name", "Ada")
	gitRun(t, other, "config", "user.email", "ada@example.com")
	gitRun(t, other, "tag", "-f", "v1", first)
	gitRun(t, other, "push", "-q", "--force", "origin", "refs/tags/v1")
	if got := remoteTagRefOID(t, bare, "v1"); got != first {
		t.Fatalf("fixture: retargeted remote v1 = %q, want %q", got, first)
	}

	if err := r.SafeDeleteRemoteTag("origin", "v1", checked, nil); err == nil {
		t.Fatal("deleting with a stale lease produced no error")
	}
	if got := remoteTagRefOID(t, bare, "v1"); got != first {
		t.Fatalf("remote v1 = %q after the stale lease, want the concurrent %q untouched", got, first)
	}

	// Re-verifying the moved tag and deleting with the fresh lease succeeds.
	if err := r.SafeDeleteRemoteTag("origin", "v1", first, nil); err != nil {
		t.Fatalf("deleting with the refreshed lease: %v", err)
	}
	if hasRef(bare, "refs/tags/v1") {
		t.Fatal("v1 survived on the remote after the correctly leased deletion")
	}
}

// The offline guards refuse before any network round-trip: a failed check,
// an empty or malformed expected object id, and remote or tag names that
// would read as options or split into arguments never reach the server. Every
// refusal is the guard's own wording, which no git push error contains, and
// the remote tag stays put throughout.
func TestSafeDeleteRemoteTagGuardsRefuseWithoutPushing(t *testing.T) {
	dir, bare, _, second := tagPushRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	gitRun(t, dir, "tag", "v1", "HEAD")
	gitRun(t, dir, "push", "-q", "origin", "refs/tags/v1")
	remoteOID := remoteTagRefOID(t, bare, "v1")

	checkErr := errors.New("the probe failed")
	if err := r.SafeDeleteRemoteTag("origin", "v1", remoteOID, checkErr); err == nil {
		t.Fatal("deleting with a failed check produced no error")
	} else if !strings.Contains(err.Error(), "could not be verified") {
		t.Fatalf("check error refusal = %q, want it to say the state could not be verified", err)
	}

	for _, oid := range []string{"", "  ", "zzzz", "abc123", remoteOID[:39]} {
		if err := r.SafeDeleteRemoteTag("origin", "v1", oid, nil); err == nil {
			t.Errorf("deleting with expected object id %q produced no error", oid)
		} else if !strings.Contains(err.Error(), "object id is unknown") {
			t.Errorf("object id refusal for %q = %q, want it to say the verified object id is unknown", oid, err)
		}
	}

	for _, tc := range []struct{ remote, name string }{
		{"", "v1"},
		{"-o", "v1"},
		{"has space", "v1"},
		{"ori gin", "v1"},
		{"origin", ""},
		{"origin", "-v"},
		{"origin", "has space"},
	} {
		if err := r.SafeDeleteRemoteTag(tc.remote, tc.name, remoteOID, nil); err == nil {
			t.Errorf("SafeDeleteRemoteTag(%q, %q) accepted an invalid argument", tc.remote, tc.name)
		}
	}

	if got := remoteTagRefOID(t, bare, "v1"); got != second {
		t.Fatalf("remote v1 moved to %q despite every guard refusing, want %q untouched", got, second)
	}
	if got := gitOut(t, dir, "rev-parse", "refs/tags/v1"); strings.TrimSpace(got) != second {
		t.Fatalf("local v1 moved despite the remote-only operation")
	}
}

// Deleting a tag the remote does not have is git's own error, not silence:
// the caller must not read it as a successful cleanup.
func TestSafeDeleteRemoteTagMissingOnRemote(t *testing.T) {
	dir, bare, _, _ := tagPushRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	gitRun(t, dir, "tag", "v1", "HEAD")

	if err := r.SafeDeleteRemoteTag("origin", "nothere", strings.Repeat("a", 40), nil); err == nil {
		t.Fatal("deleting a tag the remote never had produced no error")
	}
	if hasRef(bare, "refs/tags/nothere") {
		t.Fatal("fixture broken: nothere exists on the remote")
	}

	// A tag deleted since the check is the same case: the lease cannot match
	// what is no longer there.
	gitRun(t, dir, "push", "-q", "origin", "refs/tags/v1")
	checked := remoteTagRefOID(t, bare, "v1")
	gitRun(t, bare, "update-ref", "-d", "refs/tags/v1")
	if err := r.SafeDeleteRemoteTag("origin", "v1", checked, nil); err == nil {
		t.Fatal("deleting a tag already gone from the remote produced no error")
	}
}
