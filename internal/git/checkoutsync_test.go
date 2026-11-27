package git

import (
	"strings"
	"testing"
)

// A branch behind origin, with a local change in the way.
func behindRepo(t *testing.T) (dir string, r *Repo) {
	t.Helper()
	dir, bare, r := remoteRepo(t)
	gitRun(t, dir, "checkout", "-q", "-b", "topic")
	writeFile(t, dir, "t.txt", "1\n")
	gitRun(t, dir, "add", "t.txt")
	gitRun(t, dir, "commit", "-qm", "topic 1")
	gitRun(t, dir, "push", "-q", "-u", "origin", "topic")
	other := t.TempDir()
	gitRun(t, other, "clone", "-q", bare, "w")
	w := other + "/w"
	gitRun(t, w, "checkout", "-q", "topic")
	writeFile(t, w, "t.txt", "1\n2\n")
	gitRun(t, w, "commit", "-qam", "topic 2")
	gitRun(t, w, "push", "-q", "origin", "topic")
	gitRun(t, dir, "fetch", "-q")
	gitRun(t, dir, "checkout", "-q", "main")
	return dir, r
}

func TestCheckoutAndSync(t *testing.T) {
	for _, action := range []SyncAction{SyncFastForward, SyncMerge, SyncRebase, SyncReset} {
		t.Run(string(action), func(t *testing.T) {
			dir, r := behindRepo(t)
			writeFile(t, dir, "a.txt", "one\nlocal\n")
			if err := r.CheckoutAndSync("topic", "origin/topic", action, true, false); err != nil {
				t.Fatal(err)
			}
			if gitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD") != "topic" {
				t.Fatal("not on topic")
			}
			if gitOut(t, dir, "rev-parse", "HEAD") != gitOut(t, dir, "rev-parse", "origin/topic") {
				t.Fatalf("%s did not catch up", action)
			}
			// Stash and reapply puts the local change back.
			if !strings.Contains(gitOut(t, dir, "status", "--porcelain"), "a.txt") {
				t.Fatal("the local change was not reapplied")
			}
		})
	}
}

func TestCheckoutAndSyncWithoutStashStopsOnLocalChanges(t *testing.T) {
	dir, r := behindRepo(t)
	gitRun(t, dir, "checkout", "-q", "topic")
	gitRun(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "t.txt", "mine\n")
	gitRun(t, dir, "checkout", "-q", "topic", "--", "t.txt")
	writeFile(t, dir, "t.txt", "mine\n")
	if err := r.CheckoutAndSync("topic", "origin/topic", SyncFastForward, false, false); err == nil {
		t.Fatal("fast-forward over a local change succeeded")
	}
}

func TestFastForwardRefusesDivergence(t *testing.T) {
	dir, r := behindRepo(t)
	writeFile(t, dir, "m.txt", "m\n")
	gitRun(t, dir, "add", "m.txt")
	gitRun(t, dir, "commit", "-qm", "main only")
	gitRun(t, dir, "checkout", "-q", "-b", "div", "main")
	if err := r.FastForward("origin/topic"); err == nil {
		t.Fatal("diverged branches fast-forwarded")
	}
	if err := r.CheckoutAndSync("", "", SyncCheckout, false, false); err == nil {
		t.Fatal("an empty branch was accepted")
	}
}
