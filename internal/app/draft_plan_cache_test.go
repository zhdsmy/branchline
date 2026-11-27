package app

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// irCacheFixture builds the base commit plus the four linear commits of the
// cache tests and returns the base hash and the commit hashes, oldest first.
func irCacheFixture(t *testing.T, name string) (*gitRepoFixture, string, []string) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irLinearFixture(t, repo, "ir one", "ir two", "ir three", "ir four")
	return repo, onto, hashes
}

// irRebaseButton proves the start button's disabled state matches want.
func irRebaseButton(t *testing.T, h *testHarness, wantDisabled bool, context string) {
	t.Helper()
	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase")
	if !ok {
		t.Fatalf("%s: Rebase button missing; texts=%q", context, h.ui.Texts())
	}
	if btn.Disabled != wantDisabled {
		t.Fatalf("%s: Rebase disabled = %v, want %v; texts=%q", context, btn.Disabled, wantDisabled, h.ui.Texts())
	}
}

// The derived results of the interactive rebase plan come out of a cache
// keyed by the plan's mutation count, not rescanned every frame: editing a
// row's action in place — the mutation no real control can perform, because
// every one of them counts its change — leaves the previous frame's results
// in effect. The real control path is exercised first as the positive
// control: the same edit through the action choice does update the form.
func TestDraftPlanCacheRebaseInPlaceEditKeepsCachedResults(t *testing.T) {
	repo, onto, hashes := irCacheFixture(t, "dpcache-inplace")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	// Positive control: choosing Squash through the row's action control folds
	// the newest commit into the second newest, and its chain parent gains the
	// message button in the same gesture. Reverting to Pick removes it again.
	chooseIRAction(t, h, hashes[3], "Pick", "Squash")
	drainIRMessages(t, h)
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Edit Message "+irShort(hashes[2])); !ok {
		t.Fatalf("positive control: chain parent button missing after choosing Squash; texts=%q", h.ui.Texts())
	}
	chooseIRActionUp(t, h, hashes[3], "Squash", "Pick")
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Edit Message "+irShort(hashes[2])); ok {
		t.Fatal("positive control: chain parent button survived reverting to Pick")
	}

	// The in-place edit bypasses every mutation counter: the action is Squash
	// for any reader of the row itself, but the cached fold chains still
	// answer for the all-Pick plan, frame after frame — nothing rescans.
	irRowOf(t, h, hashes[3]).action = "Squash"
	for i := 0; i < 4; i++ {
		h.ui.Frame()
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Edit Message "+irShort(hashes[2])); ok {
		t.Fatal("in-place action edit recomputed the fold chains: the cache never held")
	}
	if h.ui.HasText("needs a commit message") {
		t.Fatalf("in-place action edit recomputed the validation; texts=%q", h.ui.Texts())
	}
	irRebaseButton(t, h, false, "in-place edit keeps the cached start state")
}

// Every real mutation of the plan or the message state invalidates the cache
// and the form follows within the same gestures: the drop count and the plan
// validation of action choices, the fold chain of a move, and the validation,
// the start button and the row marker of a message saved in the editor.
func TestDraftPlanCacheRebaseUIEditsInvalidateCache(t *testing.T) {
	repo, onto, hashes := irCacheFixture(t, "dpcache-invalidate")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	// The drop count of an action choice shows and hides with the action.
	chooseIRAction(t, h, hashes[1], "Pick", "Drop")
	if !h.ui.HasText("1 commit(s) will be dropped.") {
		t.Fatalf("drop hint missing after choosing Drop; texts=%q", h.ui.Texts())
	}
	chooseIRActionUp(t, h, hashes[1], "Drop", "Pick")
	h.ui.Frame()
	if h.ui.HasText("1 commit(s) will be dropped.") {
		t.Fatalf("drop hint survived reverting to Pick; texts=%q", h.ui.Texts())
	}

	// Squashing the oldest commit is the one plan defect: the error text and
	// the disabled start button appear with the choice and leave with it.
	chooseIRAction(t, h, hashes[0], "Pick", "Squash")
	problem := "The oldest kept commit cannot be squash: it has no previous commit to fold into."
	if !h.ui.HasText(problem) {
		t.Fatalf("plan problem missing after squashing the oldest commit; texts=%q", h.ui.Texts())
	}
	irRebaseButton(t, h, true, "oldest squash")
	chooseIRActionUp(t, h, hashes[0], "Squash", "Pick")
	h.ui.Frame()
	if h.ui.HasText(problem) {
		t.Fatalf("plan problem survived reverting the oldest commit; texts=%q", h.ui.Texts())
	}
	irRebaseButton(t, h, false, "oldest squash reverted")

	// Squash the newest commit: the chain parent is the second newest.
	chooseIRAction(t, h, hashes[3], "Pick", "Squash")
	drainIRMessages(t, h)
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Edit Message "+irShort(hashes[2])); !ok {
		t.Fatalf("chain parent button missing after choosing Squash; texts=%q", h.ui.Texts())
	}

	// Moving the squash member down twice re-folds it under ir two and then
	// under ir one: the message button walks the chain parents and leaves
	// every row the chain no longer belongs to.
	mustClick(t, h, "Move Down "+irShort(hashes[3]))
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Edit Message "+irShort(hashes[1])); !ok {
		t.Fatalf("chain parent button missing on ir two after the first move; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Edit Message "+irShort(hashes[2])); ok {
		t.Fatal("chain parent button kept on ir three after the first move")
	}
	mustClick(t, h, "Move Down "+irShort(hashes[3]))
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Edit Message "+irShort(hashes[0])); !ok {
		t.Fatalf("chain parent button missing on ir one after the second move; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Edit Message "+irShort(hashes[1])); ok {
		t.Fatal("chain parent button kept on ir two after the second move")
	}
	// ir one joined the chain with the second move, and its original message
	// read lands before the editor below can take input.
	drainIRMessages(t, h)

	// Saving a blank message in the editor is a draft like any other text: the
	// validation names the chain parent and the start button disables.
	if err := h.ui.Click("Edit Message " + irShort(hashes[0])); err != nil {
		t.Fatalf("click Edit Message: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	replaceIRMessage(t, h, "   ")
	validation := "Commit " + irShort(hashes[0]) + " needs a commit message."
	if !h.ui.HasText(validation) {
		t.Fatalf("blank-message validation missing; texts=%q", h.ui.Texts())
	}
	irRebaseButton(t, h, true, "blank message draft")

	// A real message clears the validation, enables the start and marks the
	// chain parent's row button with the custom-draft dot.
	replaceIRMessage(t, h, "custom combined message")
	if h.ui.HasText(validation) {
		t.Fatalf("validation survived a real message; texts=%q", h.ui.Texts())
	}
	irRebaseButton(t, h, false, "real message draft")
	if !hasExactText(h.ui, "Edit Message •") {
		t.Fatalf("custom-draft marker missing on the row button; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Done"); err != nil {
		t.Fatalf("click Done: %v", err)
	}
	h.ui.Frame()
	if got := irMessagesState(t, h).drafts[hashes[0]]; got != "custom combined message" {
		t.Fatalf("draft after Done = %q, want the custom message", got)
	}
}

// The conflict editor's marker scan is cached on the draft text itself:
// deleting a conflict block from the draft recounts the badge, deleting the
// last one resolves the progress and enables the save, and typing a marker
// back blocks it again — every edit recomputes exactly once, on the draft
// that actually changed.
func TestDraftPlanCacheConflictEditorDraftEdits(t *testing.T) {
	repo := newGitRepo(t, "dpcache-conflict")
	twoHunkMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")
	openMergeHunks(t, h)

	if !h.ui.HasText("2 conflicts") {
		t.Fatalf("initial progress badge missing; texts=%q", h.ui.Texts())
	}
	if btn := saveButton(t, h); !btn.Disabled {
		t.Fatal("save enabled for the untouched raw conflict")
	}

	// Delete the first conflict block from the draft by hand: the badge
	// recounts the one block left.
	st := editorState(t, h)
	start := strings.Index(st.draft, "<<<<<<<")
	end := strings.Index(st.draft, ">>>>>>>")
	if start < 0 || end < 0 {
		t.Fatalf("fixture: draft carries no conflict block: %q", st.draft)
	}
	lineEnd := strings.Index(st.draft[end:], "\n")
	if lineEnd < 0 {
		t.Fatalf("fixture: closing marker ends the draft: %q", st.draft)
	}
	withoutFirst := st.draft[:start] + st.draft[end+lineEnd+1:]
	mustClick(t, h, "Conflict result")
	h.ui.Command("selectAll")
	h.ui.Type(withoutFirst)
	h.ui.Frame()
	if !h.ui.HasText("1 conflict") || h.ui.HasText("2 conflicts") {
		t.Fatalf("progress after deleting one block wrong; texts=%q", h.ui.Texts())
	}
	if btn := saveButton(t, h); !btn.Disabled {
		t.Fatal("save enabled with one conflict block left")
	}

	// Deleting the last conflict block — a clean manual merge — resolves the
	// progress and frees the save.
	h.ui.Command("selectAll")
	h.ui.Type("top\nmanual merge\ntail\n")
	h.ui.Frame()
	if !h.ui.HasText("All conflicts resolved") {
		t.Fatalf("all-resolved progress missing after deleting the last block; texts=%q", h.ui.Texts())
	}
	if btn := saveButton(t, h); btn.Disabled {
		t.Fatal("save disabled for the clean manual merge")
	}

	// Typing a marker back blocks the save again.
	h.ui.Command("selectAll")
	h.ui.Type("half resolved\n>>>>>>> leftover\n")
	h.ui.Frame()
	if btn := saveButton(t, h); !btn.Disabled {
		t.Fatal("save enabled with a marker typed back")
	}
	if h.ui.HasText("All conflicts resolved") {
		t.Fatalf("all-resolved progress survived the typed-back marker; texts=%q", h.ui.Texts())
	}
}
