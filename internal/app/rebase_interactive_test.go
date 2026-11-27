package app

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// --- fixtures and helpers ---

// irLinearFixture builds a linear main history: the initial commit of the
// fixture setup plus one commit per subject, each adding a file named after
// it, so replaying any subset in any order applies without conflicts. It
// returns the hashes of the subject commits, oldest first.
func irLinearFixture(t *testing.T, repo *gitRepoFixture, subjects ...string) []string {
	t.Helper()
	hashes := make([]string, 0, len(subjects))
	for i, subject := range subjects {
		name := "ir" + string(rune('1'+i)) + ".txt"
		hashes = append(hashes, repo.CommitFile(name, subject+"\n", subject))
	}
	return hashes
}

// irCommitRowLabels returns the log subjects of the branch, newest first, as
// an independent git command reads them.
func irLogSubjects(t *testing.T, dir, rev string) []string {
	t.Helper()
	out := strings.TrimSpace(gitOutTrim(t, dir, "log", "--format=%s", rev))
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// openIRDialog right-clicks the commit row of onto, chooses the interactive
// rebase menu item, and verifies the form opened.
func openIRDialog(t *testing.T, h *testHarness, onto, ontoSubject string) {
	t.Helper()
	row := commitRowLabel(onto, ontoSubject)
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row %q: %v\ntexts=%q", row, err, h.ui.Texts())
	}
	label := "Interactively Rebase '" + h.app.session.Snapshot().Status.Branch + "' to Here…"
	if !slices.Contains(h.ui.Menu(), label) {
		t.Fatalf("menu %q missing %q", h.ui.Menu(), label)
	}
	if err := h.ui.ChooseMenuItem(label); err != nil {
		t.Fatalf("choose %q: %v", label, err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Interactive Rebase") {
		t.Fatalf("interactive rebase form did not open; texts=%q", h.ui.Texts())
	}
}

// drainIRPlan waits for the form's plan read to land and renders it.
func drainIRPlan(t *testing.T, h *testHarness) {
	t.Helper()
	h.drain()
	st := interactiveRebaseUI(h.app)
	if st.loading {
		t.Fatal("interactive rebase plan read still in flight after drain")
	}
}

// irRowY returns the vertical position of one plan row through the bounds of
// its action choice, so a test can compare the display order independently
// of any text.
func irRowY(t *testing.T, h *testHarness, hash string) float32 {
	t.Helper()
	sel, ok := h.ui.AccessByRole(ui.RolePopUpButton, "Action "+irShort(hash))
	if !ok {
		t.Fatalf("action choice of %s missing; texts=%q", irShort(hash), h.ui.Texts())
	}
	return sel.Bounds.Y
}

// chooseIRAction opens the action choice of the row of hash through its
// accessibility label and moves the highlight to action with the arrow keys,
// confirming with Enter: the popup items carry no unique labels, and the
// choice list order is fixed, so the keys reach exactly the action asked for.
func chooseIRAction(t *testing.T, h *testHarness, hash, from, action string) {
	t.Helper()
	label := "Action " + irShort(hash)
	if err := h.ui.Click(label); err != nil {
		t.Fatalf("click %q: %v\ntexts=%q", label, err, h.ui.Texts())
	}
	steps := slices.Index(irActionNames, action) - slices.Index(irActionNames, from)
	if steps < 0 {
		t.Fatalf("chooseIRAction only moves down the list: %q to %q", from, action)
	}
	for i := 0; i < steps; i++ {
		h.ui.Key(0, ui.KeyDown)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	row := irRowOf(t, h, hash)
	if row.action != action {
		t.Fatalf("action of %s = %q after choosing, want %q", irShort(hash), row.action, action)
	}
}

func irRowOf(t *testing.T, h *testHarness, hash string) *interactiveRebaseRow {
	t.Helper()
	for _, row := range interactiveRebaseUI(h.app).rows {
		if row.hash == hash {
			return row
		}
	}
	t.Fatalf("no plan row for %s", irShort(hash))
	return nil
}

// assertIRDialogClosed verifies no part of the interactive rebase form renders.
func assertIRDialogClosed(t *testing.T, h *testHarness) {
	t.Helper()
	if hasExactText(h.ui, "Interactive Rebase") {
		t.Fatalf("interactive rebase form still open; texts=%q", h.ui.Texts())
	}
}

// assertIRRepoUnchanged proves a refused rebase changed nothing: HEAD, the
// main branch, the commit count and the working tree exactly as before.
func assertIRRepoUnchanged(t *testing.T, repo *gitRepoFixture, head, branch string, count string) {
	t.Helper()
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved by the refused rebase: %q, want %q", got, head)
	}
	if got := gitOutTrim(t, repo.Path(), "symbolic-ref", "--short", "HEAD"); got != branch {
		t.Fatalf("checked out branch after the refusal = %q, want %q", got, branch)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", branch); got != head {
		t.Fatalf("%s moved by the refused rebase: %q, want %q", branch, got, head)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--count", branch); got != count {
		t.Fatalf("commit count after the refusal = %q, want %q", got, count)
	}
	if got := gitOutTrim(t, repo.Path(), "status", "--porcelain"); got != "" {
		t.Fatalf("working tree touched by the refused rebase: %q", got)
	}
}

// assertIRRefusal proves the write was refused and the form kept its draft:
// the session carries the error, the form — open over the window, so the
// toolbar behind it is inert — displays its full text, and the plan rows are
// still there for the user to fix.
func assertIRRefusal(t *testing.T, h *testHarness, fragment string) {
	t.Helper()
	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("expected an action error, got nil")
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("action error %q missing %q", err.Error(), fragment)
	}
	if !hasExactText(h.ui, "Interactive Rebase") {
		t.Fatal("form closed on the refusal")
	}
	if !hasExactText(h.ui, err.Error()) {
		t.Fatalf("form does not display the refusal %q; texts=%q", err.Error(), h.ui.Texts())
	}
}

// --- menu entry ---

// The interactive rebase item disables on the HEAD commit row, during an
// operation, while a write is busy and with a detached HEAD: none of those
// states can host a plan the user could confirm, and every other commit row
// offers it.
func TestRebaseInteractiveMenuItemDisabledStates(t *testing.T) {
	repo := newGitRepo(t, "ir-menu")
	repo.CommitFile("base.txt", "base\n", "base commit")
	hashes := irLinearFixture(t, repo, "ir one", "ir two", "ir three")
	tip := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	item := "Interactively Rebase 'main' to Here…"

	// On the HEAD commit row the plan would be empty.
	headRow := commitRowLabel(tip, "ir three")
	if err := h.ui.RightClick(headRow); err != nil {
		t.Fatalf("right-click HEAD row: %v", err)
	}
	if err := h.ui.ChooseMenuItem(item); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing the item on HEAD = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()

	// During an operation — here a cherry-pick stopped on a conflict — the
	// item disables on every row.
	repo.Git("checkout", "-qb", "other", hashes[0])
	repo.WriteFile("conflict.txt", "one\n")
	repo.Git("add", "--", "conflict.txt")
	repo.Git("commit", "-m", "conflict one")
	side := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.Checkout("main")
	repo.WriteFile("conflict.txt", "two\n")
	repo.Git("add", "--", "conflict.txt")
	repo.Git("commit", "-m", "conflict two")
	if out, err := tryGitCommand(repo.Path(), "cherry-pick", side); err == nil {
		t.Fatalf("conflicting cherry-pick unexpectedly succeeded: %s", out)
	}
	h.app.session.Reload()
	h.drain()
	if got := h.app.session.Snapshot().Status.State; got != "cherry-pick" {
		t.Fatalf("fixture: state = %q, want cherry-pick", got)
	}
	row := commitRowLabel(hashes[0], "ir one")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click during operation: %v", err)
	}
	if err := h.ui.ChooseMenuItem(item); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing the item during an operation = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	repo.Git("cherry-pick", "--abort")
	h.app.session.Reload()
	h.drain()

	// While a write is busy the item disables too.
	repo.WriteFile("pending.txt", "pending\n")
	h.app.session.Reload()
	h.drain()
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	h.queue.PauseWorkers()
	if err := h.ui.Click("Stage pending.txt"); err != nil {
		t.Fatalf("click Stage pending.txt: %v", err)
	}
	writeWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the stage worker is held")
	}
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click while busy: %v", err)
	}
	if err := h.ui.ChooseMenuItem(item); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing the item while busy = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()

	// A detached HEAD has no branch for the rebase to move.
	repo.Git("checkout", "-q", "--detach", "HEAD")
	h.app.session.Reload()
	h.drain()
	if got := h.app.session.Snapshot().Status.Branch; got != "" {
		t.Fatalf("fixture: branch = %q, want detached", got)
	}
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click while detached: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Interactively Rebase HEAD to Here…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing the item while detached = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	repo.Checkout("main")
	h.app.session.Reload()
	h.drain()

	// On an ordinary commit row the item is offered.
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click ordinary row: %v", err)
	}
	if !slices.Contains(h.ui.Menu(), item) {
		t.Fatalf("menu %q missing %q", h.ui.Menu(), item)
	}
	h.ui.CloseMenu()
}

// --- plan rows ---

// The form opens from the real context menu, lists the commits of the range
// newest on top with short hash, subject, author and an action choice per
// row, disables moving up on the top row and moving down on the bottom row,
// keeps every row's controls inside its own band, and centers in the window.
func TestRebaseInteractiveRowsNewestFirstAndLayout(t *testing.T) {
	repo := newGitRepo(t, "ir-rows")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irLinearFixture(t, repo, "ir one", "ir two", "ir three", "ir four")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	// Newest on top: the Y positions of the rows increase down the reverse
	// of the execution order.
	exec := append([]string(nil), hashes...)
	for i := 0; i+1 < len(exec); i++ {
		// exec[i] is older than exec[i+1], so exec[i+1] displays higher.
		if ya, yb := irRowY(t, h, exec[i+1]), irRowY(t, h, exec[i]); ya >= yb {
			t.Fatalf("row of %s (newer) at y=%v not above row of %s (older) at y=%v", irShort(exec[i+1]), ya, irShort(exec[i]), yb)
		}
	}

	// Every row carries its commit's subject and author.
	for _, subject := range []string{"ir one", "ir two", "ir three", "ir four"} {
		if !hasExactText(h.ui, subject) {
			t.Fatalf("subject %q missing; texts=%q", subject, h.ui.Texts())
		}
	}
	if !hasExactText(h.ui, "Branchline Test") {
		t.Fatalf("author missing; texts=%q", h.ui.Texts())
	}

	// The boundary rows disable the move that would leave the plan.
	top := hashes[len(hashes)-1]
	bottom := hashes[0]
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Move Up "+irShort(top)); !ok || !btn.Disabled {
		t.Fatalf("Move Up of the top row = %+v found=%v, want present and disabled", btn, ok)
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Move Down "+irShort(bottom)); !ok || !btn.Disabled {
		t.Fatalf("Move Down of the bottom row = %+v found=%v, want present and disabled", btn, ok)
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Move Down "+irShort(top)); !ok || btn.Disabled {
		t.Fatalf("Move Down of the top row = %+v found=%v, want present and enabled", btn, ok)
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Move Up "+irShort(bottom)); !ok || btn.Disabled {
		t.Fatalf("Move Up of the bottom row = %+v found=%v, want present and enabled", btn, ok)
	}

	// The action choice and both move controls of a row sit inside the
	// dialog, sharing the row's band.
	dlg, ok := h.ui.AccessByRole(ui.RoleDialog, "")
	if !ok {
		t.Fatal("no dialog present")
	}
	for _, hash := range hashes {
		sel, ok := h.ui.AccessByRole(ui.RolePopUpButton, "Action "+irShort(hash))
		if !ok {
			t.Fatalf("action choice of %s missing", irShort(hash))
		}
		for _, label := range []string{"Move Up " + irShort(hash), "Move Down " + irShort(hash)} {
			btn, ok := h.ui.AccessByRole(ui.RoleButton, label)
			if !ok {
				t.Fatalf("%s missing", label)
			}
			if btn.Bounds.Y+btn.Bounds.H <= sel.Bounds.Y || btn.Bounds.Y >= sel.Bounds.Y+sel.Bounds.H {
				t.Fatalf("%s at %+v outside the band of its row's action choice %+v", label, btn.Bounds, sel.Bounds)
			}
			assertInside(t, btn.Bounds, dlg.Bounds, label)
		}
		assertInside(t, sel.Bounds, dlg.Bounds, "Action "+irShort(hash))
	}
	assertDialogLayout(t, h, "Cancel", "Rebase")

	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	assertIRDialogClosed(t, h)
}

// Moving rows through their real buttons changes the execution order, and
// the rebase replays the commits in exactly the order the form showed: the
// independent git log of the result matches it commit by commit.
func TestRebaseInteractiveMoveRowsReplaysFormOrder(t *testing.T) {
	repo := newGitRepo(t, "ir-move")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irLinearFixture(t, repo, "ir one", "ir two", "ir three", "ir four")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	// ir three is the second row from the top; moving it up twice makes it
	// the newest commit of the replay: execution order one, two, four, three.
	if err := h.ui.Click("Move Up " + irShort(hashes[2])); err != nil {
		t.Fatalf("move ir three up once: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Move Up " + irShort(hashes[2])); err != nil {
		t.Fatalf("move ir three up twice: %v", err)
	}
	h.ui.Frame()
	// The display follows: ir three is now the top row.
	topY := irRowY(t, h, hashes[2])
	for _, other := range []string{hashes[0], hashes[1], hashes[3]} {
		if y := irRowY(t, h, other); y <= topY {
			t.Fatalf("row of %s at y=%v not below the moved row at y=%v", irShort(other), y, topY)
		}
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Move Up "+irShort(hashes[2])); !ok || !btn.Disabled {
		t.Fatalf("Move Up of the moved-to-top row = %+v found=%v, want disabled", btn, ok)
	}

	// ir two sits just above the bottom row ir one; moving it down once
	// makes it the oldest commit of the replay, swapping places with ir
	// one. Execution order is then two, one, four, three.
	if err := h.ui.Click("Move Down " + irShort(hashes[1])); err != nil {
		t.Fatalf("move ir two down: %v", err)
	}
	h.ui.Frame()
	bottomY := irRowY(t, h, hashes[1])
	for _, other := range []string{hashes[0], hashes[2], hashes[3]} {
		if y := irRowY(t, h, other); y >= bottomY {
			t.Fatalf("row of %s at y=%v not above the moved-down row at y=%v", irShort(other), y, bottomY)
		}
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Move Down "+irShort(hashes[1])); !ok || !btn.Disabled {
		t.Fatalf("Move Down of the moved-to-bottom row = %+v found=%v, want disabled", btn, ok)
	}

	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("reordered rebase failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"ir three", "ir four", "ir one", "ir two", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after reordered rebase = %v, want %v", subjects, want)
	}
	for i, f := range []string{"ir1.txt", "ir2.txt", "ir3.txt", "ir4.txt"} {
		if content := strings.TrimSpace(repo.Git("show", "HEAD:"+f)); content != "ir "+[]string{"one", "two", "three", "four"}[i] {
			t.Fatalf("%s content at HEAD = %q", f, content)
		}
	}
	assertIRDialogClosed(t, h)
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after the reordered rebase = %q, want empty", got)
	}
}

// Dropping a commit through its real action choice removes it from the
// replayed history: the file it added is gone, the commit count shrinks by
// one, and the notice names the drop before the rebase starts.
func TestRebaseInteractiveDropRemovesCommit(t *testing.T) {
	repo := newGitRepo(t, "ir-drop")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irLinearFixture(t, repo, "ir one", "ir two", "ir three", "ir four")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[2], "Pick", "Drop")
	if !hasExactText(h.ui, "1 commit(s) will be dropped.") {
		t.Fatalf("drop notice missing; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("drop rebase failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"ir four", "ir two", "ir one", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after drop = %v, want %v", subjects, want)
	}
	if out, err := tryGitCommand(repo.Path(), "cat-file", "-e", "HEAD:ir3.txt"); err == nil {
		t.Fatalf("ir3.txt of the dropped commit survived: %s", out)
	}
	assertFileBytes(t, repo, "ir4.txt", "ir four\n")
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); got != "4" {
		t.Fatalf("commit count after drop = %q, want 4", got)
	}
	assertIRDialogClosed(t, h)
}

// Squashing a commit folds it into the previous one: the replayed history
// has one commit fewer, both files of the pair are in it, and the commit
// carries the fold target's message and the squashed commit's, joined.
func TestRebaseInteractiveSquashFoldsCommits(t *testing.T) {
	repo := newGitRepo(t, "ir-squash")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irLinearFixture(t, repo, "ir one", "ir two", "ir three", "ir four")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[3], "Pick", "Squash")
	// A squash chain needs the full messages of its members before the start
	// button enables; without them the rebase would collapse to one-line
	// subjects.
	drainIRMessages(t, h)
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("squash rebase failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"ir three", "ir two", "ir one", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after squash = %v, want %v", subjects, want)
	}
	// The folded commit holds both files: ir three's and ir four's.
	if content := strings.TrimSpace(repo.Git("show", "HEAD:ir3.txt")); content != "ir three" {
		t.Fatalf("ir3.txt at the squashed tip = %q", content)
	}
	if content := strings.TrimSpace(repo.Git("show", "HEAD:ir4.txt")); content != "ir four" {
		t.Fatalf("ir4.txt at the squashed tip = %q", content)
	}
	if msg := strings.TrimSpace(repo.Git("log", "-1", "--format=%B", "main")); msg != "ir three\n\nir four" {
		t.Fatalf("squashed commit message = %q, want the parent's and the squashed commit's messages joined", msg)
	}
	if parent := gitOutTrim(t, repo.Path(), "rev-parse", "main"); parent == hashes[3] {
		t.Fatal("main did not move after squash")
	}
	assertIRDialogClosed(t, h)
}

// A fixup folds the commit into the previous one while keeping the previous
// commit's own message: one commit fewer, both files, the message of the
// fold target.
func TestRebaseInteractiveFixupKeepsTargetMessage(t *testing.T) {
	repo := newGitRepo(t, "ir-fixup")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irLinearFixture(t, repo, "ir one", "ir two", "ir three", "ir four")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[3], "Pick", "Fixup")
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("fixup rebase failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"ir three", "ir two", "ir one", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after fixup = %v, want %v", subjects, want)
	}
	if content := strings.TrimSpace(repo.Git("show", "HEAD:ir4.txt")); content != "ir four" {
		t.Fatalf("ir4.txt at the fixup tip = %q", content)
	}
	if msg := strings.TrimSpace(repo.Git("log", "-1", "--format=%B", "main")); msg != "ir three" {
		t.Fatalf("fixup commit message = %q, want the fold target's message", msg)
	}
	assertIRDialogClosed(t, h)
}

// The oldest kept commit cannot fold into nothing: choosing Squash or Fixup
// on it — here made oldest by dropping the commit before it — shows the
// validation error and disables the start button, and dropping it again
// re-enables the rebase.
func TestRebaseInteractiveOldestKeptCannotFold(t *testing.T) {
	repo := newGitRepo(t, "ir-fold-guard")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irLinearFixture(t, repo, "ir one", "ir two", "ir three")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[0], "Pick", "Drop")
	chooseIRAction(t, h, hashes[1], "Pick", "Squash")
	if !hasExactText(h.ui, "The oldest kept commit cannot be squash: it has no previous commit to fold into.") {
		t.Fatalf("fold validation error missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || !btn.Disabled {
		t.Fatalf("Rebase with an invalid fold = %+v found=%v, want present and disabled", btn, ok)
	}

	// Fixup on the oldest kept commit is equally refused.
	chooseIRAction(t, h, hashes[1], "Squash", "Fixup")
	if !hasExactText(h.ui, "The oldest kept commit cannot be fixup: it has no previous commit to fold into.") {
		t.Fatalf("fixup fold validation error missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || !btn.Disabled {
		t.Fatalf("Rebase with an invalid fixup fold = %+v found=%v, want present and disabled", btn, ok)
	}

	// Dropping the folded row too leaves a valid plan again: ir three picks.
	chooseIRAction(t, h, hashes[1], "Fixup", "Drop")
	if hasExactText(h.ui, "The oldest kept commit cannot be squash: it has no previous commit to fold into.") {
		t.Fatal("fold validation error survived the fix")
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || btn.Disabled {
		t.Fatalf("Rebase after fixing the plan = %+v found=%v, want enabled", btn, ok)
	}
	if !hasExactText(h.ui, "2 commit(s) will be dropped.") {
		t.Fatalf("drop count after two drops missing; texts=%q", h.ui.Texts())
	}

	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase after the fixed plan failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"ir three", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after the fixed plan = %v, want %v", subjects, want)
	}
}

// An Edit action stops the rebase on that commit: the shared banner appears
// with Continue available — there is no conflict — and continuing through
// the banner's real button finishes the replay.
func TestRebaseInteractiveEditStopBannerAndContinue(t *testing.T) {
	repo := newGitRepo(t, "ir-edit")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irLinearFixture(t, repo, "ir one", "ir two", "ir three")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[1], "Pick", "Edit")
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase to the edit stop failed: %v", err)
	}
	st := h.app.session.Snapshot().Status
	if st.State != "rebase" {
		t.Fatalf("state at the edit stop = %q, want rebase", st.State)
	}
	if st.Stopped != "edit" {
		t.Fatalf("stop reason = %q, want edit", st.Stopped)
	}
	if st.Conflicted() {
		t.Fatal("an edit stop must not report conflicts")
	}
	assertIRDialogClosed(t, h)
	if !hasExactText(h.ui, "Rebase in progress. Continue, skip, or abort the rebase.") {
		t.Fatalf("edit stop banner copy missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Continue Operation"); !ok || btn.Disabled {
		t.Fatalf("Continue Operation at the edit stop = %+v found=%v, want present and enabled", btn, ok)
	}
	// The rebase stopped on ir two: HEAD is its replay, ir three not yet applied.
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s", "HEAD")); subject != "ir two" {
		t.Fatalf("HEAD subject at the edit stop = %q, want ir two", subject)
	}
	if out, err := tryGitCommand(repo.Path(), "cat-file", "-e", "HEAD:ir3.txt"); err == nil {
		t.Fatalf("ir3.txt applied before the edit stop ended: %s", out)
	}

	if err := h.ui.Click("Continue Operation"); err != nil {
		t.Fatalf("click Continue Operation: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("continue after the edit stop failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"ir three", "ir two", "ir one", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after continuing the edit stop = %v, want %v", subjects, want)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after continue = %q, want empty", got)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Operation in progress"); ok {
		t.Fatalf("banner still rendered after continue; texts=%q", h.ui.Texts())
	}
}

// --- guarded execution ---

// An external commit after the form opened moves HEAD: the guard refuses the
// rebase, the form keeps its draft with the error showing, and every ref and
// byte of the repository is exactly as the external commit left it.
func TestRebaseInteractiveGuardHeadMoved(t *testing.T) {
	repo := newGitRepo(t, "ir-guard-head")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	irLinearFixture(t, repo, "ir one", "ir two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	repo.WriteFile("other.txt", "other\n")
	repo.Git("add", "--", "other.txt")
	repo.Git("commit", "-m", "external commit")
	moved := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	assertIRRefusal(t, h, "HEAD or the current branch changed")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != moved {
		t.Fatalf("HEAD moved by the refused rebase: %q, want the external commit %q", got, moved)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); got != "4" {
		t.Fatalf("commit count after the refusal = %q, want 4", got)
	}
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("working tree touched by the refused rebase: %q", out)
	}
	// The form kept its draft and shows the error.
	if !hasExactText(h.ui, "Interactive Rebase") {
		t.Fatal("form closed on the refusal")
	}
	if rows := interactiveRebaseUI(h.app).rows; len(rows) != 2 {
		t.Fatalf("draft rows after the refusal = %d, want 2", len(rows))
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// Switching to another branch without moving HEAD still refuses the rebase:
// HEAD and the branch must both be exactly what the form captured.
func TestRebaseInteractiveGuardBranchSwitched(t *testing.T) {
	repo := newGitRepo(t, "ir-guard-branch")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	irLinearFixture(t, repo, "ir one", "ir two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	repo.Git("checkout", "-q", "-b", "other")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != head {
		t.Fatal("fixture: checkout -b moved HEAD")
	}

	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	assertIRRefusal(t, h, "HEAD or the current branch changed")
	if got := gitOutTrim(t, repo.Path(), "symbolic-ref", "--short", "HEAD"); got != "other" {
		t.Fatalf("the refused rebase switched back: %q, want other", got)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "other"); got != head {
		t.Fatalf("other moved by the refused rebase: %q, want %q", got, head)
	}
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("working tree touched by the refused rebase: %q", out)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// Uncommitted tracked changes — unstaged or staged — refuse the rebase
// before Git runs: this form offers no autostash choice, so the user commits
// or stashes first, and every byte of the working tree and the index is
// exactly as before.
func TestRebaseInteractiveGuardTrackedChanges(t *testing.T) {
	repo := newGitRepo(t, "ir-guard-dirty")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	irLinearFixture(t, repo, "ir one", "ir two")
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// Round 1: an unstaged tracked modification.
	repo.WriteFile("base.txt", "base\ndirty\n")
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	assertIRRefusal(t, h, "cannot rebase with uncommitted local changes")
	assertIRRepoUnchangedExcept(t, repo, head, "base.txt", "base\ndirty\n")
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); out != "" {
		t.Fatalf("index touched by the refused rebase: %q", out)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	// Round 2: a staged change refuses the same way.
	repo.Git("checkout", "--", "base.txt")
	repo.WriteFile("base.txt", "base\nstaged\n")
	repo.Git("add", "--", "base.txt")
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	assertIRRefusal(t, h, "cannot rebase with uncommitted local changes")
	assertIRRepoUnchangedExcept(t, repo, head, "base.txt", "base\nstaged\n")
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); !strings.Contains(out, "base.txt") {
		t.Fatalf("staged change lost by the refused rebase: %q", out)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// assertIRRepoUnchangedExcept proves the refusal left HEAD, the branch and
// the commit count unmoved and exactly one file's bytes at want.
func assertIRRepoUnchangedExcept(t *testing.T, repo *gitRepoFixture, head, rel, want string) {
	t.Helper()
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved by the refused rebase: %q, want %q", got, head)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != head {
		t.Fatalf("main moved by the refused rebase: %q, want %q", got, head)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); got != "3" {
		t.Fatalf("commit count after the refusal = %q, want 3", got)
	}
	assertFileBytes(t, repo, rel, want)
}

// When the guard cannot even read the repository state, the rebase never
// runs: HEAD, the branch, the index and every byte of the working tree are
// exactly as before.
func TestRebaseInteractiveGuardStatusFailure(t *testing.T) {
	repo := newGitRepo(t, "ir-guard-status")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	irLinearFixture(t, repo, "ir one", "ir two")
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	assertIRRefusal(t, h, "reading the repository state failed")
	assertIRRepoUnchanged(t, repo, head, "main", "3")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// The plan comparison is the guard's last line: when the commits of the
// range no longer match what the form captured, the write refuses. HEAD and
// the base pin the range, so through the real UI a changed plan always
// arrives with a changed HEAD and the earlier check fires first; this test
// exercises the comparison itself the way the guard contract states it —
// captured plan against live repository — with the rewrite done externally.
func TestRebaseInteractiveGuardPlanChanged(t *testing.T) {
	repo := newGitRepo(t, "ir-guard-plan")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irLinearFixture(t, repo, "ir one", "ir two")
	live := git.New(&git.LocalExecutor{Dir: repo.Path()})

	plan, err := live.RebaseRangePlan(onto)
	if err != nil {
		t.Fatalf("fixture plan read: %v", err)
	}
	// An external rewrite of the tip: the range the plan captured is gone.
	repo.WriteFile("ir2.txt", "rewritten\n")
	repo.Git("add", "--", "ir2.txt")
	repo.Git("commit", "-q", "--amend", "--no-edit")
	rewritten := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	if rewritten == hashes[1] {
		t.Fatal("fixture: the amend did not rewrite the tip")
	}
	mainBefore := gitOutTrim(t, repo.Path(), "rev-parse", "main")

	err = interactiveRebaseWithGuard(live, rewritten, "main", onto, plan)
	if err == nil || !strings.Contains(err.Error(), "commits in the rebase range changed") {
		t.Fatalf("guarded rebase with a stale plan = %v, want a range-changed refusal", err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != rewritten {
		t.Fatalf("HEAD moved by the refused rebase: %q, want %q", got, rewritten)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != mainBefore {
		t.Fatalf("main moved by the refused rebase: %q, want %q", got, mainBefore)
	}
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("working tree touched by the refused rebase: %q", out)
	}
}

// --- plan read failure and retry ---

// A failed plan read shows the error and a Retry button, keeps the start
// button disabled, and retrying after the failure reads the plan for real.
func TestRebaseInteractivePlanReadFailureAndRetry(t *testing.T) {
	repo := newGitRepo(t, "ir-retry")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irLinearFixture(t, repo, "ir one", "ir two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	isRevList := func(args []string) bool { return len(args) > 0 && args[0] == "rev-list" }
	installFailFirstExecutor(t, h, isRevList, errors.New("injected rev-list failure"))

	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)
	if !hasExactText(h.ui, "Retry") {
		t.Fatalf("Retry missing after the failed read; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RolePopUpButton, "Action "+irShort(hashes[0])); ok {
		t.Fatal("plan rows rendered despite the failed read")
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || !btn.Disabled {
		t.Fatalf("Rebase after the failed read = %+v found=%v, want disabled", btn, ok)
	}

	// The injected failure fired exactly once: the retry reads for real.
	if err := h.ui.Click("Retry"); err != nil {
		t.Fatalf("click Retry: %v", err)
	}
	h.drain()
	if _, ok := h.ui.AccessByRole(ui.RolePopUpButton, "Action "+irShort(hashes[0])); !ok {
		t.Fatalf("plan rows missing after Retry; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || btn.Disabled {
		t.Fatalf("Rebase after Retry = %+v found=%v, want enabled", btn, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// Switching the repository closes the form and discards its plan read: the
// read's late result — held back until after the switch — never lands in the
// draft, and reopening the form in the other repository starts empty.
func TestRebaseInteractiveLatePlanReadDiscardedOnRepoSwitch(t *testing.T) {
	repoA := newGitRepo(t, "ir-switch-a")
	repoA.CommitFile("base.txt", "base\n", "base commit")
	ontoA := strings.TrimSpace(repoA.Git("rev-parse", "HEAD"))
	irLinearFixture(t, repoA, "ir one", "ir two")
	repoB := newGitRepo(t, "ir-switch-b")
	repoB.CommitFile("b.txt", "b\n", "commit in beta")
	ontoB := strings.TrimSpace(repoB.Git("rev-parse", "HEAD"))
	repoB.CommitFile("b2.txt", "b2\n", "more in beta")

	h := newTestApp(t, repoB.StoreRepo())
	openHistoryPage(t, h, repoA.StoreRepo())

	// Open the form and hold its plan read worker.
	h.queue.PauseWorkers()
	row := commitRowLabel(ontoA, "base commit")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row: %v", err)
	}
	label := "Interactively Rebase 'main' to Here…"
	if err := h.ui.ChooseMenuItem(label); err != nil {
		t.Fatalf("choose %q: %v", label, err)
	}
	h.ui.Frame()
	readWorker := h.queue.WaitWorker(t)

	// Switch the repository while the read is in flight: resume first so the
	// switch's own load workers are never gated, then release the held plan
	// read — its result can only land after the generation moved past it.
	h.queue.ResumeWorkers()
	var savedID string
	for _, r := range h.store.Repos() {
		if r.Name == repoB.StoreRepo().Name {
			savedID = r.ID
		}
	}
	saved, ok := h.store.Get(savedID)
	if !ok {
		t.Fatal("seeded repository B missing from the store")
	}
	h.app.openRepo(saved)
	h.ui.Frame()
	h.queue.ReleaseWorker(readWorker)
	h.drain()

	st := interactiveRebaseUI(h.app)
	if st.open {
		t.Fatal("form still open after the repository switch")
	}
	if len(st.rows) != 0 || st.onto != "" {
		t.Fatalf("the late read landed in the draft: rows=%d onto=%q", len(st.rows), st.onto)
	}

	// Reopening the form in the other repository reads its plan, never a
	// leftover of the first one.
	openIRDialog(t, h, ontoB, "commit in beta")
	drainIRPlan(t, h)
	st = interactiveRebaseUI(h.app)
	if len(st.rows) != 1 || st.rows[0].subject != "more in beta" {
		t.Fatalf("plan after reopening = %+v, want only commit in beta's successor", st.rows)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// --- SSH loopback ---

// irRemoteLinear builds the linear fixture on the server: the initial commit
// plus one per subject, returning the base hash and the subject hashes.
func irRemoteLinear(t *testing.T, srv *sshtest.Target, dir string, subjects ...string) (onto string, hashes []string) {
	t.Helper()
	onto = strings.TrimSpace(srv.RunGit(t, dir, "rev-parse", "HEAD"))
	for i, subject := range subjects {
		name := "ir" + string(rune('1'+i)) + ".txt"
		hashes = append(hashes, remoteCommitFile(t, srv, dir, name, subject+"\n", subject))
	}
	return onto, hashes
}

// Over a real SSH connection the plan reads, the rows render and a drop
// executes exactly as locally: every assertion reads the server's repository
// through a second SSH channel.
func TestRebaseInteractiveDropLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	onto, hashes := irRemoteLinear(t, srv, repo.Path, "ir one", "ir two", "ir three")

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)
	openIRDialog(t, h, onto, "initial commit")
	drainIRPlan(t, h)
	if _, ok := h.ui.AccessByRole(ui.RolePopUpButton, "Action "+irShort(hashes[0])); !ok {
		t.Fatalf("SSH plan rows missing; texts=%q", h.ui.Texts())
	}

	chooseIRAction(t, h, hashes[1], "Pick", "Drop")
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH drop rebase failed: %v", err)
	}
	out := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "--format=%s", "main"))
	subjects := strings.Split(out, "\n")
	want := []string{"ir three", "ir one", "initial commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("remote log after drop = %v, want %v", subjects, want)
	}
	if out := srv.RunShell(t, "git -C "+git.ShellQuote(repo.Path)+" cat-file -e HEAD:ir2.txt 2>/dev/null && echo present || echo absent"); strings.TrimSpace(out) != "absent" {
		t.Fatalf("ir2.txt of the dropped commit survived on the server: %q", out)
	}
	assertIRDialogClosed(t, h)
}

// Over SSH an Edit action stops the rebase with the banner's Continue
// available, and continuing finishes the replay on the server.
func TestRebaseInteractiveEditContinueLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	onto, hashes := irRemoteLinear(t, srv, repo.Path, "ir one", "ir two", "ir three")

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)
	openIRDialog(t, h, onto, "initial commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[0], "Pick", "Edit")
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH rebase to the edit stop failed: %v", err)
	}
	st := h.app.session.Snapshot().Status
	if st.State != "rebase" || st.Stopped != "edit" {
		t.Fatalf("SSH edit stop state = %+v, want rebase stopped at edit", st)
	}
	if !hasExactText(h.ui, "Rebase in progress. Continue, skip, or abort the rebase.") {
		t.Fatalf("SSH edit stop banner copy missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Continue Operation"); !ok || btn.Disabled {
		t.Fatalf("SSH Continue at the edit stop = %+v found=%v, want enabled", btn, ok)
	}
	if subject := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "-1", "--format=%s", "HEAD")); subject != "ir one" {
		t.Fatalf("remote HEAD subject at the edit stop = %q, want ir one", subject)
	}

	if err := h.ui.Click("Continue Operation"); err != nil {
		t.Fatalf("click Continue Operation: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH continue after the edit stop failed: %v", err)
	}
	out := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "--format=%s", "main"))
	subjects := strings.Split(out, "\n")
	want := []string{"ir three", "ir two", "ir one", "initial commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("remote log after continue = %v, want %v", subjects, want)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after SSH continue = %q, want empty", got)
	}
}
