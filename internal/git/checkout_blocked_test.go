package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFileDeep writes a file, creating its parent directories first.
func writeFileDeep(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, name, body)
}

// blockedFixture builds a repository whose checkout of other is refused:
// shared.txt and "dir/é 中.txt" are tracked on both branches with different
// content and modified locally on main, and "untracked only.txt" is
// untracked on main but tracked on other.
func blockedFixture(t *testing.T) (dir string, r *Repo) {
	t.Helper()
	dir = initRepo(t)
	writeFile(t, dir, "shared.txt", "base\n")
	writeFileDeep(t, dir, "dir/é 中.txt", "base\n")
	writeFile(t, dir, "spaced file.txt", "base\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	gitRun(t, dir, "checkout", "-q", "-b", "other")
	writeFile(t, dir, "shared.txt", "other\n")
	writeFileDeep(t, dir, "dir/é 中.txt", "other\n")
	writeFile(t, dir, "spaced file.txt", "other\n")
	writeFile(t, dir, "untracked only.txt", "tracked on other\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "other")
	gitRun(t, dir, "checkout", "-q", "main")
	return dir, New(&LocalExecutor{Dir: dir})
}

func TestCheckoutBlockedBy(t *testing.T) {
	t.Run("tracked changes", func(t *testing.T) {
		dir, r := blockedFixture(t)
		writeFile(t, dir, "shared.txt", "local\n")
		blocked, ok := CheckoutBlockedBy(r.Checkout("other"))
		if !ok {
			t.Fatal("a checkout refused over local changes was not recognized as blocked")
		}
		if strings.Join(blocked.Changed, ",") != "shared.txt" {
			t.Fatalf("changed = %q, want shared.txt", blocked.Changed)
		}
		if len(blocked.Untracked) != 0 {
			t.Fatalf("untracked = %q, want none", blocked.Untracked)
		}
	})

	t.Run("untracked in the way", func(t *testing.T) {
		dir, r := blockedFixture(t)
		writeFile(t, dir, "untracked only.txt", "mine\n")
		blocked, ok := CheckoutBlockedBy(r.Checkout("other"))
		if !ok {
			t.Fatal("a checkout refused over an untracked file was not recognized as blocked")
		}
		if len(blocked.Changed) != 0 {
			t.Fatalf("changed = %q, want none", blocked.Changed)
		}
		if strings.Join(blocked.Untracked, ",") != "untracked only.txt" {
			t.Fatalf("untracked = %q, want untracked only.txt", blocked.Untracked)
		}
	})

	t.Run("both sections in git order", func(t *testing.T) {
		dir, r := blockedFixture(t)
		writeFile(t, dir, "shared.txt", "local\n")
		writeFile(t, dir, "dir/é 中.txt", "local\n")
		writeFile(t, dir, "spaced file.txt", "local\n")
		writeFile(t, dir, "untracked only.txt", "mine\n")
		err := r.Checkout("other")
		blocked, ok := CheckoutBlockedBy(err)
		if !ok {
			t.Fatal("a checkout refused over both kinds was not recognized as blocked")
		}
		// Git sorts the paths by byte order: dir/… before shared.txt.
		wantChanged := []string{"dir/é 中.txt", "shared.txt", "spaced file.txt"}
		if strings.Join(blocked.Changed, "\x00") != strings.Join(wantChanged, "\x00") {
			t.Fatalf("changed = %q, want %q in git's order", blocked.Changed, wantChanged)
		}
		if strings.Join(blocked.Untracked, ",") != "untracked only.txt" {
			t.Fatalf("untracked = %q, want untracked only.txt", blocked.Untracked)
		}
		// The paths keep their exact bytes: spaces and non-ASCII pass through.
		for _, p := range append(blocked.Changed, blocked.Untracked...) {
			if strings.ContainsAny(p, "\"") {
				t.Fatalf("path %q still carries quoting", p)
			}
		}
		_ = err
	})

	t.Run("advice lines are not required", func(t *testing.T) {
		dir, r := blockedFixture(t)
		gitRun(t, dir, "config", "advice.commitBeforeMerge", "false")
		writeFile(t, dir, "shared.txt", "local\n")
		err := r.Checkout("other")
		var ge *Error
		if !errors.As(err, &ge) {
			t.Fatalf("checkout error = %v, want a git error", err)
		}
		if strings.Contains(ge.Stderr, "Please commit your changes") {
			t.Fatalf("fixture broken: the advice line printed although it is off:\n%s", ge.Stderr)
		}
		blocked, ok := CheckoutBlockedBy(err)
		if !ok {
			t.Fatalf("blocked checkout without the advice lines was not recognized:\n%s", ge.Stderr)
		}
		if strings.Join(blocked.Changed, ",") != "shared.txt" {
			t.Fatalf("changed = %q, want shared.txt", blocked.Changed)
		}
	})

	t.Run("other git errors are not blocked", func(t *testing.T) {
		_, r := blockedFixture(t)
		if _, ok := CheckoutBlockedBy(r.Checkout("no-such-branch")); ok {
			t.Fatal("a checkout of a missing branch counted as blocked")
		}
	})

	t.Run("non git errors are not blocked", func(t *testing.T) {
		if _, ok := CheckoutBlockedBy(fmt.Errorf("plain failure")); ok {
			t.Fatal("a plain error counted as blocked")
		}
		if _, ok := CheckoutBlockedBy(nil); ok {
			t.Fatal("nil counted as blocked")
		}
		if _, ok := CheckoutBlockedBy(fmt.Errorf("wrapped: %w", errors.New("no"))); ok {
			t.Fatal("a wrapped plain error counted as blocked")
		}
	})

	t.Run("untracked removed wording parses", func(t *testing.T) {
		// Newer git knows a second wording for the untracked section —
		// "would be removed by checkout" — for a file the target only needs
		// out of the way. It parses exactly like the overwritten one.
		err := &Error{ExitCode: 1, Stderr: "error: The following untracked working tree files would be removed by checkout:\n\tin/the/way.txt\nPlease move or remove them before you switch branches.\nAborting\n"}
		blocked, ok := CheckoutBlockedBy(err)
		if !ok {
			t.Fatal("the removed wording of the untracked section was not recognized as blocked")
		}
		if strings.Join(blocked.Untracked, ",") != "in/the/way.txt" {
			t.Fatalf("untracked = %q, want in/the/way.txt", blocked.Untracked)
		}
	})

	t.Run("quoted paths are restored", func(t *testing.T) {
		// Git puts C-style quoting around paths it cannot print literally;
		// the dialog names the file as it is on disk.
		err := &Error{ExitCode: 1, Stderr: "error: Your local changes to the following files would be overwritten by checkout:\n\t\"\\303\\251 quoted.txt\"\n\t\"with\\ttab.txt\"\nAborting\n"}
		blocked, ok := CheckoutBlockedBy(err)
		if !ok {
			t.Fatal("a blocked error with quoted paths was not recognized")
		}
		want := "é quoted.txt,with\ttab.txt"
		if strings.Join(blocked.Changed, ",") != want {
			t.Fatalf("changed = %q, want %q", strings.Join(blocked.Changed, ","), want)
		}
	})

	t.Run("unrelated wording is not blocked", func(t *testing.T) {
		err := &Error{ExitCode: 1, Stderr: "error: Updating the following directories would lose untracked files in them:\n\tsub\n\nAborting\n"}
		if _, ok := CheckoutBlockedBy(err); ok {
			t.Fatal("the directory-loss error counted as a blocked checkout")
		}
	})
}

func TestWithSavedChanges(t *testing.T) {
	// savedChangesFixture builds the conflict shape of the feature checkout:
	// shared.txt differs on other in line six only, so the stashed edit of
	// line one reapplies cleanly after the checkout.
	savedChangesFixture := func(t *testing.T) (string, *Repo) {
		t.Helper()
		dir := initRepo(t)
		writeFile(t, dir, "shared.txt", "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n")
		gitRun(t, dir, "add", "-A")
		gitRun(t, dir, "commit", "-q", "-m", "base")
		gitRun(t, dir, "checkout", "-q", "-b", "other")
		writeFile(t, dir, "shared.txt", "l1\nl2\nl3\nl4\nl5\nother-l6\nl7\nl8\n")
		gitRun(t, dir, "commit", "-q", "-a", "-m", "other edits line six")
		gitRun(t, dir, "checkout", "-q", "main")
		return dir, New(&LocalExecutor{Dir: dir})
	}

	readFile := func(t *testing.T, dir, name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(data)
	}

	t.Run("changes and untracked files reach the new branch", func(t *testing.T) {
		dir, r := savedChangesFixture(t)
		writeFile(t, dir, "shared.txt", "local-l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n")
		writeFile(t, dir, "new work.txt", "untracked\n")
		err := r.WithSavedChanges("test checkout", func() error {
			return r.Checkout("other")
		})
		if err != nil {
			t.Fatalf("stash checkout failed: %v", err)
		}
		if got := gitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "other" {
			t.Fatalf("branch = %q, want other", got)
		}
		if got := readFile(t, dir, "shared.txt"); got != "local-l1\nl2\nl3\nl4\nl5\nother-l6\nl7\nl8\n" {
			t.Fatalf("shared.txt = %q, want the stashed line one over other's line six", got)
		}
		if got := readFile(t, dir, "new work.txt"); got != "untracked\n" {
			t.Fatalf("new work.txt = %q, want the untracked content back", got)
		}
		if out := strings.TrimSpace(gitOut(t, dir, "stash", "list")); out != "" {
			t.Fatalf("stash list = %q, want empty after a clean reapply", out)
		}
		if hasRef(dir, "refs/worktree/branchline-sync-stash") {
			t.Fatal("the sync stash marker survived a clean reapply")
		}
	})

	t.Run("a failing fn still restores the changes", func(t *testing.T) {
		dir, r := savedChangesFixture(t)
		writeFile(t, dir, "shared.txt", "local-l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n")
		fnErr := fmt.Errorf("the checkout stayed out")
		err := r.WithSavedChanges("test checkout", func() error {
			return fnErr
		})
		if !errors.Is(err, fnErr) {
			t.Fatalf("error = %v, want the fn error", err)
		}
		if got := gitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
			t.Fatalf("branch = %q, want main", got)
		}
		if got := readFile(t, dir, "shared.txt"); got != "local-l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n" {
			t.Fatalf("shared.txt = %q, want the local edit restored on main", got)
		}
		if out := strings.TrimSpace(gitOut(t, dir, "stash", "list")); out != "" {
			t.Fatalf("stash list = %q, want empty after the restore", out)
		}
	})

	t.Run("a conflicting restore keeps the changes in the stash list", func(t *testing.T) {
		dir := initRepo(t)
		writeFile(t, dir, "shared.txt", "l1\n")
		gitRun(t, dir, "add", "-A")
		gitRun(t, dir, "commit", "-q", "-m", "base")
		gitRun(t, dir, "checkout", "-q", "-b", "other")
		writeFile(t, dir, "shared.txt", "other-l1\n")
		gitRun(t, dir, "commit", "-q", "-a", "-m", "other edits line one")
		gitRun(t, dir, "checkout", "-q", "main")
		r := New(&LocalExecutor{Dir: dir})

		// Both sides of the reapply touched line one: the merge of the stash
		// conflicts, the changes stay in the stash list, and the error says so.
		writeFile(t, dir, "shared.txt", "local-l1\n")
		err := r.WithSavedChanges("test checkout", func() error {
			return r.Checkout("other")
		})
		if err == nil {
			t.Fatal("a conflicting restore reported success")
		}
		if !strings.Contains(err.Error(), "stash list") {
			t.Fatalf("restore error = %q, want it to say the changes remain in the stash list", err)
		}
		if got := gitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "other" {
			t.Fatalf("branch = %q, want other — the checkout itself succeeded", got)
		}
		if out := strings.TrimSpace(gitOut(t, dir, "stash", "list")); out == "" {
			t.Fatal("stash list is empty after the conflicting restore — the changes must stay there")
		}
	})

	t.Run("without local changes fn runs and nothing is stashed", func(t *testing.T) {
		dir, r := savedChangesFixture(t)
		ran := false
		err := r.WithSavedChanges("test checkout", func() error {
			ran = true
			return r.Checkout("other")
		})
		if err != nil {
			t.Fatalf("clean stash checkout failed: %v", err)
		}
		if !ran {
			t.Fatal("fn never ran on a clean tree")
		}
		if got := gitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "other" {
			t.Fatalf("branch = %q, want other", got)
		}
		if out := strings.TrimSpace(gitOut(t, dir, "stash", "list")); out != "" {
			t.Fatalf("stash list = %q, want empty — nothing was ever saved", out)
		}
	})
}
