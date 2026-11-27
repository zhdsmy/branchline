package app

import (
	"testing"
)

// TestHarnessSettlesAfterInput drives the shared test harness through the
// everyday inputs of the suite — opening a repository, clicking buttons,
// selecting rows, switching pages and typing — and requires the window to
// ask for no further frame after each one. Without the harness showing
// animations at their targets at once, an animation any of these inputs
// starts never finishes on the Tester's frozen clock, so every input makes
// the Tester render its maximum of twenty frames.
func TestHarnessSettlesAfterInput(t *testing.T) {
	repo := newGitRepo(t, "harness-settle")
	repo.CommitFile("base.txt", "base\n", "initial commit")
	repo.WriteFile("one.txt", "one\n")

	h := openChangesHarness(t, repo)

	settled := func(what string) {
		t.Helper()
		if h.ui.FramePending() {
			t.Fatalf("%s: the window still asks for frames; animations an input starts never finish on the frozen clock", what)
		}
	}
	settled("opening the repository")

	mustClick(t, h, "Stage All")
	settled("clicking Stage All")
	h.drain()
	settled("draining after Stage All")

	mustClick(t, h, "Unstage All")
	settled("clicking Unstage All")
	h.drain()
	settled("draining after Unstage All")

	if err := h.ui.Click("one.txt"); err != nil {
		t.Fatalf("click change row one.txt: %v\ntexts=%q", err, h.ui.Texts())
	}
	settled("selecting a change row")
	h.drain()
	settled("draining after selecting a change row")

	typeCommitMessage(t, h, "add one")
	settled("typing into the commit message input")

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History nav row: %v", err)
	}
	settled("switching to the History page")
	h.drain()
	settled("draining on the History page")

	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes nav row: %v", err)
	}
	settled("switching back to the Changes page")
	h.drain()
	settled("draining back on the Changes page")
}
