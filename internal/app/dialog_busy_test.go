package app

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// While the write a quick rebase dialog itself started is still running —
// here held before its first git command — Escape does not close the Reword
// Commit dialog and its cancel button is disabled, so the message being
// rewritten stays visible instead of leaving the user with only the
// toolbar's outcome. Once the rebase finishes, the dialog closes by the
// rule it always had: started and finished.
func TestDialogBusyRewordStaysOpenWhileItsWriteRuns(t *testing.T) {
	repo := newGitRepo(t, "busy-reword")
	repo.CommitFile("base.txt", "base\n", "base commit")
	tip := quickLinearFixture(t, repo, "q one")[0]

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openQuickRebaseItem(t, h, tip, "q one", "Reword Message…")
	drainQuickRead(t, h)

	edited := "rewritten subject\n\nrewritten body"
	if err := h.ui.Click("Commit message"); err != nil {
		t.Fatalf("click Commit message: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Command("selectAll")
	h.ui.Type(edited)
	h.ui.Frame()

	h.queue.PauseWorkers()
	if err := h.ui.Click("Reword"); err != nil {
		t.Fatalf("click Reword: %v\ntexts=%q", err, h.ui.Texts())
	}
	writeWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the rebase write is held")
	}

	if cancel, ok := h.ui.AccessByRole(ui.RoleButton, "Cancel"); !ok {
		t.Fatalf("cancel button missing while the write runs; texts=%q", h.ui.Texts())
	} else if !cancel.Disabled {
		t.Error("cancel is enabled while the dialog's own write runs")
	}

	h.ui.Key(0, ui.KeyEscape)
	if st := quickRebaseUI(h.app); !st.open {
		t.Error("Escape closed the dialog while its write runs")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Reword Commit"); !ok {
		t.Errorf("the Reword Commit panel is gone after Escape mid-write; texts=%q", h.ui.Texts())
	}
	if box, ok := h.ui.AccessByRole(ui.RoleTextField, "Commit message"); !ok {
		t.Errorf("the message editor is gone after Escape mid-write; texts=%q", h.ui.Texts())
	} else if box.Value != edited {
		t.Errorf("the message after Escape mid-write = %q, want %q", box.Value, edited)
	}

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("reword failed: %v", err)
	}
	assertQuickDialogClosed(t, h)
	if msg := strings.TrimSpace(repo.Git("log", "-1", "--format=%B", "main")); msg != edited {
		t.Fatalf("HEAD message after the reword = %q, want %q", msg, edited)
	}
}

// The confirmation of Drop follows the same rule: mid-write Escape keeps it
// open with cancel disabled, and the finished rebase closes it.
func TestDialogBusyDropStaysOpenWhileItsWriteRuns(t *testing.T) {
	repo := newGitRepo(t, "busy-drop")
	repo.CommitFile("base.txt", "base\n", "base commit")
	tip := quickLinearFixture(t, repo, "q one")[0]

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openQuickRebaseItem(t, h, tip, "q one", "Drop…")
	h.ui.Frame()

	h.queue.PauseWorkers()
	if err := h.ui.Click("Drop"); err != nil {
		t.Fatalf("click Drop: %v\ntexts=%q", err, h.ui.Texts())
	}
	writeWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the drop write is held")
	}

	if cancel, ok := h.ui.AccessByRole(ui.RoleButton, "Cancel"); !ok {
		t.Fatalf("cancel button missing while the write runs; texts=%q", h.ui.Texts())
	} else if !cancel.Disabled {
		t.Error("cancel is enabled while the dialog's own write runs")
	}

	h.ui.Key(0, ui.KeyEscape)
	if st := quickRebaseUI(h.app); !st.open {
		t.Error("Escape closed the dialog while its write runs")
	}
	if !hasExactText(h.ui, "The commit is removed from the current branch. Later commits are replayed.") {
		t.Errorf("the drop description is gone after Escape mid-write; texts=%q", h.ui.Texts())
	}

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("drop failed: %v", err)
	}
	assertQuickDialogClosed(t, h)
	if count := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); count != "1" {
		t.Fatalf("commit count after the drop = %q, want 1", count)
	}
}

// A write the dialog did not start — a fetch running in the background —
// locks nothing of it: cancel stays enabled and Escape dismisses the Reword
// dialog as always. Only the dialog's own rewrite keeps it shut.
func TestDialogBusyForeignWriteKeepsDialogClosable(t *testing.T) {
	repo := newGitRepo(t, "busy-foreign")
	repo.CommitFile("base.txt", "base\n", "base commit")
	tip := quickLinearFixture(t, repo, "q one")[0]
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openQuickRebaseItem(t, h, tip, "q one", "Reword Message…")
	drainQuickRead(t, h)

	// The fetch stands for any write of the repository the open dialog did
	// not submit. It cannot come from the window's own input here — the
	// modal's backdrop swallows it — so the test starts it the way the
	// toolbar's Fetch does, without the click.
	h.queue.PauseWorkers()
	h.app.syncFetch()
	fetchWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the fetch is held")
	}
	if st := quickRebaseUI(h.app); !st.open {
		t.Fatal("the reword dialog is not open behind the fetch")
	}

	if cancel, ok := h.ui.AccessByRole(ui.RoleButton, "Cancel"); !ok {
		t.Fatalf("cancel button missing during the foreign write; texts=%q", h.ui.Texts())
	} else if cancel.Disabled {
		t.Error("cancel is disabled during a write the dialog did not start")
	}

	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	if st := quickRebaseUI(h.app); st.open {
		t.Error("Escape did not close the dialog during a write it did not start")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Reword Commit"); ok {
		t.Error("the Reword Commit panel is still up after Escape during a foreign write")
	}

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(fetchWorker)
	h.drain()
}
