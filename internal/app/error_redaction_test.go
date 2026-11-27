package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// The session scrubs credentials out of every write and load error the
// moment it stores them, because the views print error text as-is: a pre-push
// hook echoing the URL it ran against — userinfo and password included — must
// never reach the commit box, the toolbar badge or the accessibility tree,
// while errors.As keeps finding the original *git.Error underneath.
func TestErrorRedactionPushHookCredentials(t *testing.T) {
	repo := newGitRepo(t, "redact-push")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")

	// The hook answers the push with a URL carrying a password, as a hook or
	// a remote helper echoing its arguments would.
	hook := "#!/bin/sh\necho 'rejecting https://user:secret-token@127.0.0.1:1/x.git for you' >&2\nexit 1\n"
	hookPath := filepath.Join(repo.Path(), ".git", "hooks", "pre-push")
	if err := os.WriteFile(hookPath, []byte(hook), 0o755); err != nil {
		t.Fatalf("write pre-push hook: %v", err)
	}

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	repo.CommitFile("b.txt", "two\n", "second commit")
	h.drain()
	clickPush(t, h)
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("the rejected push produced no ActionErr")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("ActionErr text leaks the credential: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "https://127.0.0.1:1/x.git") {
		t.Fatalf("redacted ActionErr missing the scrubbed URL: %q", err.Error())
	}

	// The original error must survive underneath: errors.As reaches the
	// *git.Error, whose stderr still carries the unredacted hook output.
	var ge *git.Error
	if !errors.As(err, &ge) {
		t.Fatalf("errors.As lost the *git.Error under the redaction: %v (%T)", err, err)
	}
	if !strings.Contains(ge.Stderr, "secret-token") {
		t.Fatalf("Stderr = %q, want the unredacted hook output", ge.Stderr)
	}

	// Nothing on screen or in the accessibility tree may carry the
	// credential: not the toolbar badge, not the commit box, not any
	// accessible name or description.
	mustClick(t, h, "Changes")
	h.ui.Frame()
	if h.ui.HasText("secret-token") {
		t.Fatalf("credential visible in the UI; texts=%q", h.ui.Texts())
	}
	for _, a := range h.ui.AccessAll() {
		for _, text := range []string{a.Name, a.Value, a.Description, a.Placeholder} {
			if strings.Contains(text, "secret-token") {
				t.Fatalf("credential in the accessibility tree: role=%v name=%q value=%q description=%q placeholder=%q",
					a.Role, a.Name, a.Value, a.Description, a.Placeholder)
			}
		}
	}
	if !h.ui.HasText("https://127.0.0.1:1/x.git") {
		t.Fatalf("scrubbed URL not visible in the UI; texts=%q", h.ui.Texts())
	}
}

// failReadExecutor wraps an executor and fails git status with err, so the
// first read of a snapshot load fails.
type failReadExecutor struct {
	real git.Executor
	err  error
}

func (e *failReadExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	for _, a := range args {
		if a == "status" {
			return nil, e.err
		}
	}
	return e.real.Run(ctx, stdin, args...)
}

func (e *failReadExecutor) Describe() string { return e.real.Describe() }

// A load failure whose text carries a credential is scrubbed the same way:
// the full-page error of the Changes and History pages reads redacted, and
// errors.Is still reaches the error the reader returned.
func TestErrorRedactionLoadError(t *testing.T) {
	repo := newGitRepo(t, "redact-load")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	readErr := errors.New("cannot read refs from https://user:secret-token@127.0.0.1:1/x.git")

	h := newTestApp(t)
	h.app.openRepoFn = func(meta store.Repo) *git.Repo {
		base := git.New(meta.Executor())
		return base.WithExecutor(&failReadExecutor{real: base.Exec, err: readErr})
	}
	h.openRepo(repo.StoreRepo())
	h.drain()

	err := h.app.session.LoadErr()
	if err == nil {
		t.Fatal("the failing read produced no LoadErr")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("LoadErr text leaks the credential: %q", err.Error())
	}
	if !errors.Is(err, readErr) {
		t.Fatalf("errors.Is lost the reader's error under the redaction: %v", err)
	}

	h.ui.Frame()
	if !h.ui.HasText("Failed to load repository status") {
		t.Fatalf("full-page load failure missing; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("secret-token") {
		t.Fatalf("credential visible in the UI; texts=%q", h.ui.Texts())
	}
	for _, a := range h.ui.AccessAll() {
		for _, text := range []string{a.Name, a.Value, a.Description, a.Placeholder} {
			if strings.Contains(text, "secret-token") {
				t.Fatalf("credential in the accessibility tree: role=%v name=%q", a.Role, a.Name)
			}
		}
	}
}

// The missing-repository notice still appears for a deleted directory: the
// scrubbed error keeps the original *git.Error underneath, so
// isMissingRepoError classifies it exactly as before.
func TestErrorRedactionMissingRepoPage(t *testing.T) {
	repo := newGitRepo(t, "redact-missing")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("fixture: initial load failed: %v", err)
	}

	if err := os.RemoveAll(repo.Path()); err != nil {
		t.Fatalf("delete repository directory: %v", err)
	}
	h.app.session.Reload()
	h.drain()

	err := h.app.session.LoadErr()
	if err == nil {
		t.Fatal("the deleted repository loaded without error")
	}
	var ge *git.Error
	if !errors.As(err, &ge) {
		t.Fatalf("errors.As lost the *git.Error under the redaction: %v (%T)", err, err)
	}
	if !strings.Contains(ge.Stderr, "cannot change to") {
		t.Fatalf("Stderr = %q, want git's missing-directory message", ge.Stderr)
	}

	h.ui.Frame()
	if !h.ui.HasText("Repository Not Found") {
		t.Fatalf("Repository Not Found notice missing; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "History")
	h.ui.Frame()
	if !h.ui.HasText("Repository Not Found") {
		t.Fatalf("History missing the Repository Not Found notice; texts=%q", h.ui.Texts())
	}
}
