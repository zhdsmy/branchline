package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// obstructFileWithDirectory deletes the file at path in the repository and
// puts a directory holding one untracked file in its place.
func obstructFileWithDirectory(t *testing.T, repo *gitRepoFixture, path string) {
	t.Helper()
	if err := os.Remove(filepath.Join(repo.Path(), path)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo.Path(), path), 0o755); err != nil {
		t.Fatal(err)
	}
	repo.WriteFile(filepath.Join(path, "inner.txt"), "untracked work\n")
}

func readGuardFileOr(t *testing.T, repo *gitRepoFixture, path string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo.Path(), path))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// Clicking Discard on a deleted file whose place a directory of untracked
// files took, then confirming, must surface the refusal in the page and
// leave the directory exactly as it was: the confirmation speaks only of
// the file's tracked content, never of the untracked work inside.
func TestWorktreeGuardDiscardShowsErrorAndKeepsUntrackedDirectory(t *testing.T) {
	repo := newGitRepo(t, "wg-ui-discard")
	repo.CommitFile("victim.txt", "tracked content\n", "initial")
	obstructFileWithDirectory(t, repo, "victim.txt")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Discard victim.txt")
	h.ui.Frame()
	mustClick(t, h, "Confirm Discard")
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil || !strings.Contains(err.Error(), "victim.txt") {
		t.Fatalf("discard action error = %v, want a refusal naming victim.txt", err)
	}
	if !h.ui.HasText("victim.txt") {
		t.Fatalf("the refusal is not visible in the page; texts=%q", h.ui.Texts())
	}
	got, ok := readGuardFileOr(t, repo, filepath.Join("victim.txt", "inner.txt"))
	if !ok || got != "untracked work\n" {
		t.Fatalf("the untracked content was destroyed: content %q, read ok %v", got, ok)
	}
}

// Resetting a file from History to its state at a commit, when the file's
// place a directory of untracked files took, must surface the refusal in
// the page and leave the directory exactly as it was.
func TestWorktreeGuardResetFileShowsErrorAndKeepsUntrackedDirectory(t *testing.T) {
	repo := newGitRepo(t, "wg-ui-reset")
	repo.CommitFile("a.txt", "v1\n", "first")
	second := repo.CommitFile("a.txt", "v2\n", "second")
	obstructFileWithDirectory(t, repo, "a.txt")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	chooseFileReset(t, h, second, "second", "a.txt",
		"Reset File to State at Commit…", "Reset File to State at Commit")
	confirmFileReset(t, h)

	err := h.app.session.ActionErr()
	if err == nil || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("file reset action error = %v, want a refusal naming a.txt", err)
	}
	got, ok := readGuardFileOr(t, repo, filepath.Join("a.txt", "inner.txt"))
	if !ok || got != "untracked work\n" {
		t.Fatalf("the untracked content was destroyed: content %q, read ok %v", got, ok)
	}
}
