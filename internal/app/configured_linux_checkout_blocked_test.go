package app

import (
	"strings"
	"testing"
)

// A blocked double-click checkout on a configured Linux SSH container
// (sshlab) opens the dialog listing the file in the way. Confirming with the
// stash choice checks the branch out on the server and reapplies the edit
// there, leaving the stash list empty.
func TestConfiguredLinuxCheckoutBlocked(t *testing.T) {
	repo, srv := configuredLinuxRepo(t)

	// shared.txt differs on feature in line six, so the stashed edit of line
	// one reapplies cleanly after the checkout.
	srv.WriteFile(t, repo.Path, "shared.txt", []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n"))
	srv.RunGit(t, repo.Path, "add", "--", "shared.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "shared base")
	srv.RunGit(t, repo.Path, "checkout", "-q", "-b", "feature")
	srv.WriteFile(t, repo.Path, "shared.txt", []byte("l1\nl2\nl3\nl4\nl5\nfeature-l6\nl7\nl8\n"))
	srv.RunGit(t, repo.Path, "commit", "-q", "-a", "-m", "feature edits line six")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	srv.WriteFile(t, repo.Path, "shared.txt", []byte("dirty-l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n"))

	h := newTestApp(t)
	openSSHRepoThroughSwitcher(t, h, repo)

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	doubleClickCheckout(t, h, "feature")

	// The dialog opens with the file in the way, instead of an error banner.
	assertCheckoutBlockedPanel(t, h, "feature", []string{"shared.txt"}, nil)
	if got := srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("server branch after the blocked checkout = %q, want main — nothing moved yet", got)
	}

	clickStashAndCheckout(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH stash and checkout failed: %v", err)
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature" {
		t.Fatalf("server branch after stash and checkout = %q, want feature", got)
	}
	if got := string(srv.ReadFile(t, repo.Path, "shared.txt")); got != "dirty-l1\nl2\nl3\nl4\nl5\nfeature-l6\nl7\nl8\n" {
		t.Fatalf("server shared.txt = %q, want the stashed line one over feature's line six", got)
	}
	if out := strings.TrimSpace(srv.RunGit(t, repo.Path, "stash", "list")); out != "" {
		t.Fatalf("server stash list = %q, want empty after the clean reapply", out)
	}
	if row, ok := findAccessNode(t, h.ui, "feature"); !ok || !row.Selected {
		t.Fatalf("feature row after the checkout = %+v (found=%v), want selected", row, ok)
	}
}
