//go:build !race

package app

import (
	"testing"
)

// The merge editor of an unresolved conflict: every scan of the draft text —
// the progress badge, the marker hint, the save button — must read a cached
// result, so frames over 500 conflict blocks cost no more than over 5 however
// many markers the draft still carries, and no frame may run a Git command.
func TestFrameCostConflictEditorUnresolved(t *testing.T) {
	small, large := frameCostPair(t, "ConflictEditorUnresolved", driveConflictHunks)
	frameCostAssert(t, "ConflictEditorUnresolved", small, large)
}

// driveIRPlanSquashReword opens the interactive rebase form over a plan of
// rows commits, sets the newest row to Squash and the second newest to
// Reword through their real action choices, waits for the original messages
// of the fold chain to arrive, and closes the message editor the Reword
// opened: the steady state the measurement starts from is a plan whose fold
// chains, message targets and validation the form derives.
func driveIRPlanSquashReword(t *testing.T, scale string) *testHarness {
	t.Helper()
	rows := 20
	if scale == "large" {
		rows = 2000
	}
	repo, onto := frameCostIRRepo(t, "ir-plan-"+scale, rows+1)
	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	h.drain()
	for len(h.app.session.Snapshot().Commits) < rows+1 {
		mustClick(t, h, "Load More")
		h.drain()
	}
	revealOldestHistoryRow(t, h, onto)
	row := commitRowLabel(onto, "ir commit 1")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row: %v\ntexts=%q", err, h.ui.Texts())
	}
	menuItem := "Interactively Rebase 'main' to Here…"
	if err := h.ui.ChooseMenuItem(menuItem); err != nil {
		t.Fatalf("choose %q: %v", menuItem, err)
	}
	h.ui.Frame()
	h.drain()
	st := interactiveRebaseUI(h.app)
	if len(st.rows) != rows {
		t.Fatalf("fixture: plan rows = %d, want %d", len(st.rows), rows)
	}
	newest := st.rows[len(st.rows)-1]
	second := st.rows[len(st.rows)-2]
	chooseIRAction(t, h, newest.hash, "Pick", "Squash")
	chooseIRAction(t, h, second.hash, "Pick", "Reword")
	h.drain()
	ms := interactiveRebaseMessages(h.app)
	if ms.reading {
		t.Fatal("fixture: original message read still in flight after drain")
	}
	if len(ms.originals) == 0 {
		t.Fatal("fixture: no original messages loaded for the fold chain")
	}
	if err := h.ui.Click("Done"); err != nil {
		t.Fatalf("close the message editor: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if ms.editingHash != "" {
		t.Fatal("fixture: message editor still open after Done")
	}
	return h
}

// The interactive rebase form of a plan carrying a squash chain and a reword:
// the fold chains, the message targets, the plan validation and the drop
// count are the same computation for an unchanged plan, so frames over 2000
// commits must cost no more per frame than over 20, and no frame may run a
// Git command.
func TestFrameCostInteractiveRebasePlan(t *testing.T) {
	small, large := frameCostPair(t, "InteractiveRebasePlan", driveIRPlanSquashReword)
	frameCostAssert(t, "InteractiveRebasePlan", small, large)
}
