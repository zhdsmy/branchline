package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// tagsPushSidebarRepo builds the two-tag sidebar fixture with a bare origin
// remote, so every push dialog starts from the real product state.
func tagsPushSidebarRepo(t *testing.T, name string) (*gitRepoFixture, string, string, string) {
	t.Helper()
	repo, first, second := tagsSidebarRepo(t, name)
	bare := repo.AddBareRemote("origin")
	return repo, bare, first, second
}

func bareTagExists(t *testing.T, bare, name string) bool {
	t.Helper()
	_, err := tryGitCommand(bare, "rev-parse", "--verify", "--quiet", "refs/tags/"+name)
	return err == nil
}

func bareTagOID(t *testing.T, bare, name string) string {
	t.Helper()
	return strings.TrimSpace(runGitCommand(t, bare, "rev-parse", "refs/tags/"+name))
}

func setBareTag(t *testing.T, bare, name, oid string) {
	t.Helper()
	runGitCommand(t, bare, "update-ref", "refs/tags/"+name, oid)
}

// openTagPushDialog selects the tag row and opens its Push Tag dialog through
// the inline action bar, as a user does.
func openTagPushDialog(t *testing.T, h *testHarness, name string) {
	t.Helper()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Push tag "+name); !ok {
		selectTagRow(t, h, name)
	}
	if err := h.ui.Click("Push tag " + name); err != nil {
		t.Fatalf("click Push tag %s: %v", name, err)
	}
	h.ui.Frame()
	if !h.app.tagsPushUI().pushOpen {
		t.Fatalf("Push Tag dialog did not open for %s; texts=%q", name, h.ui.Texts())
	}
}

func clickForcePushBox(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Force push"); err != nil {
		t.Fatalf("click Force push: %v", err)
	}
	h.ui.Frame()
}

func clickForceAgreement(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Agree to overwrite remote tag"); err != nil {
		t.Fatalf("click overwrite agreement: %v", err)
	}
	h.ui.Frame()
}

func assertButtonInsideWindow(t *testing.T, h *testHarness, label string) {
	t.Helper()
	btn, ok := h.ui.AccessByRole(ui.RoleButton, label)
	if !ok {
		t.Fatalf("button %q missing; texts=%q", label, h.ui.Texts())
	}
	if btn.Bounds.W <= 0 || btn.Bounds.H <= 0 {
		t.Fatalf("button %q has empty bounds %+v", label, btn.Bounds)
	}
	if btn.Bounds.X < 0 || btn.Bounds.Y < 0 || btn.Bounds.X+btn.Bounds.W > 1200 || btn.Bounds.Y+btn.Bounds.H > 800 {
		t.Fatalf("button %q bounds %+v leave the 1200x800 test window", label, btn.Bounds)
	}
}

// The push entries are real sidebar rows: they stay left aligned and inside
// the tag row instead of drifting centered or overflowing the narrow sidebar.
func TestTagsPushSidebarLayoutStaysInsideRows(t *testing.T) {
	repo, _, _, _ := tagsPushSidebarRepo(t, "tags-push-layout")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	pushAll, ok := h.ui.Access("Push All Tags")
	if !ok {
		t.Fatalf("Push All Tags entry missing; texts=%q", h.ui.Texts())
	}
	newTag, ok := h.ui.Access("New Tag")
	if !ok {
		t.Fatal("New Tag entry missing")
	}
	if pushAll.Bounds.X < newTag.Bounds.X-1 || pushAll.Bounds.X > newTag.Bounds.X+1 {
		t.Fatalf("Push All Tags left edge %v, want aligned with New Tag left edge %v", pushAll.Bounds.X, newTag.Bounds.X)
	}

	selectTagRow(t, h, "lw")
	row, ok := h.ui.Find("lw")
	if !ok {
		t.Fatal("lw row rect missing")
	}
	push, ok := h.ui.AccessByRole(ui.RoleButton, "Push tag lw")
	if !ok {
		t.Fatalf("Push tag lw button missing; texts=%q", h.ui.Texts())
	}
	deleteBtn, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag lw")
	if !ok {
		t.Fatal("Delete tag lw button missing")
	}
	rowRight := row.X + row.W
	for label, btn := range map[string]ui.AccessInfo{"Push tag lw": push, "Delete tag lw": deleteBtn} {
		if btn.Bounds.X < row.X-1 || btn.Bounds.X+btn.Bounds.W > rowRight+1 {
			t.Fatalf("%s bounds %+v leave tag row bounds %+v", label, btn.Bounds, row)
		}
	}
	assertButtonInsideWindow(t, h, "Push tag lw")
	assertButtonInsideWindow(t, h, "Delete tag lw")
}

// Clicking Push in the selected row opens the dialog with the tag, the default
// remote, an unticked force box, and a Push Tag button inside the window.
func TestTagsPushDialogOpensAndLayout(t *testing.T) {
	repo, _, _, _ := tagsPushSidebarRepo(t, "tags-push-dialog")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openTagPushDialog(t, h, "lw")

	if !h.ui.HasText("Push Tag") {
		t.Fatalf("Push Tag title missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("lw") {
		t.Fatalf("tag name missing from dialog; texts=%q", h.ui.Texts())
	}
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Force push"); !ok || box.Checked {
		t.Fatalf("Force push box = %+v (found=%v), want present and unticked", box, ok)
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Push Tag"); !ok || btn.Disabled {
		t.Fatalf("Confirm Push Tag = %+v (found=%v), want enabled", btn, ok)
	}
	assertButtonInsideWindow(t, h, "Confirm Push Tag")
	assertButtonInsideWindow(t, h, "Cancel")

	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if h.app.tagsPushUI().pushOpen {
		t.Fatal("Push Tag dialog survived Cancel")
	}
}

// A normal push publishes exactly the chosen tag. Pushing it again reports the
// shared up-to-date note as a toast, not an error.
func TestTagsPushSingleTagAndUpToDateToast(t *testing.T) {
	repo, bare, _, second := tagsPushSidebarRepo(t, "tags-push-single")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openTagPushDialog(t, h, "lw")
	if h.ui.HasText("Enter Tag Name") {
		t.Fatal("New Tag dialog overlapped the Push Tag dialog")
	}
	if err := h.ui.Click("Confirm Push Tag"); err != nil {
		t.Fatalf("click Confirm Push Tag: %v", err)
	}
	h.drain()

	if h.app.tagsPushUI().pushOpen {
		t.Fatal("Push Tag dialog stayed open after submit")
	}
	if !bareTagExists(t, bare, "lw") {
		t.Fatal("lw did not reach the remote")
	}
	if got := bareTagOID(t, bare, "lw"); got != second {
		t.Fatalf("remote lw = %q, want HEAD %q", got, second)
	}
	if bareTagExists(t, bare, "ann") {
		t.Fatal("the single-tag push sent ann too")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected push error: %v", err)
	}

	openTagPushDialog(t, h, "lw")
	if err := h.ui.Click("Confirm Push Tag"); err != nil {
		t.Fatalf("click Confirm Push Tag again: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("up-to-date tag push reported an error: %v", err)
	}
	if !h.ui.HasText(git.UpToDatePush) {
		t.Fatalf("up-to-date toast missing; texts=%q", h.ui.Texts())
	}
}

// The normal push is never forced: a remote tag that moved elsewhere is
// refused, keeps its object, and the refusal is shown as a repository error.
func TestTagsPushNeverOverwritesDivergedRemoteTag(t *testing.T) {
	repo, bare, first, second := tagsPushSidebarRepo(t, "tags-push-diverged")
	repo.Git("push", "origin", "refs/tags/lw")
	setBareTag(t, bare, "lw", first)

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openTagPushDialog(t, h, "lw")
	if err := h.ui.Click("Confirm Push Tag"); err != nil {
		t.Fatalf("click Confirm Push Tag: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("pushing over a diverged remote tag produced no error")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error after the refused push; texts=%q", h.ui.Texts())
	}
	if got := bareTagOID(t, bare, "lw"); got != first {
		t.Fatalf("remote lw = %q, want the diverged %q untouched", got, first)
	}
	if got := bareTagOID(t, bare, "lw"); got == second {
		t.Fatal("the normal push overwrote the diverged remote tag")
	}
}

// While the remote probe is held in flight the dialog says it is checking and
// has no force-push button at all. Once it answers, the existing remote tag
// needs the explicit overwrite agreement before the button enables.
func TestTagsPushForceCheckingAndAgreementGate(t *testing.T) {
	repo, bare, first, _ := tagsPushSidebarRepo(t, "tags-push-force-gate")
	repo.Git("push", "origin", "refs/tags/lw")
	setBareTag(t, bare, "lw", first)

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	h.queue.PauseWorkers()
	defer h.queue.ResumeWorkers()
	openTagPushDialog(t, h, "lw")
	clickForcePushBox(t, h)
	probe := h.queue.WaitWorker(t)
	h.ui.Frame()

	if !h.ui.HasText("Checking remote tag on origin") {
		t.Fatalf("checking note missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Force Push Tag"); ok {
		t.Fatalf("Confirm Force Push Tag present while checking: %+v", btn)
	}
	if err := h.ui.Click("Confirm Force Push Tag"); err == nil {
		t.Fatal("Confirm Force Push Tag was clickable while checking")
	}

	h.queue.ReleaseWorker(probe)
	h.queue.ResumeWorkers()
	h.drain()

	if !h.ui.HasText("will be overwritten") {
		t.Fatalf("overwrite warning missing after the probe; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText(first[:7]) {
		t.Fatalf("remote object id summary missing; texts=%q", h.ui.Texts())
	}
	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Force Push Tag")
	if !ok || !btn.Disabled {
		t.Fatalf("Confirm Force Push Tag before agreement = %+v (found=%v), want disabled", btn, ok)
	}
	clickForceAgreement(t, h)
	btn, ok = h.ui.AccessByRole(ui.RoleButton, "Confirm Force Push Tag")
	if !ok || btn.Disabled {
		t.Fatalf("Confirm Force Push Tag after agreement = %+v (found=%v), want enabled", btn, ok)
	}
	assertButtonInsideWindow(t, h, "Confirm Force Push Tag")
}

// A confirmed force push overwrites only through the lease of the probe. When
// the remote moves after that answer the lease is stale: git refuses
// atomically, the remote tag survives, and the error surfaces.
func TestTagsPushForceOverwriteAndStaleLeaseProtects(t *testing.T) {
	repo, bare, first, second := tagsPushSidebarRepo(t, "tags-push-force-stale")
	repo.Git("push", "origin", "refs/tags/lw")
	setBareTag(t, bare, "lw", first)

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openTagPushDialog(t, h, "lw")
	clickForcePushBox(t, h)
	h.drain()
	clickForceAgreement(t, h)
	if err := h.ui.Click("Confirm Force Push Tag"); err != nil {
		t.Fatalf("click Confirm Force Push Tag: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("confirmed force push failed: %v", err)
	}
	if got := bareTagOID(t, bare, "lw"); got != second {
		t.Fatalf("remote lw = %q, want the force-pushed HEAD %q", got, second)
	}

	// Probe the overwritten remote, then move it behind the dialog's back. The
	// lease no longer matches and the push would overwrite real data, so git
	// refuses atomically.
	openTagPushDialog(t, h, "lw")
	clickForcePushBox(t, h)
	h.drain()
	clickForceAgreement(t, h)
	setBareTag(t, bare, "lw", first)
	if err := h.ui.Click("Confirm Force Push Tag"); err != nil {
		t.Fatalf("click Confirm Force Push Tag with stale lease: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("stale force push produced no error")
	}
	if got := bareTagOID(t, bare, "lw"); got != first {
		t.Fatalf("remote lw = %q after stale lease, want the concurrent object %q untouched", got, first)
	}
	if got := bareTagOID(t, bare, "lw"); got == second {
		t.Fatal("the stale force push overwrote the concurrent remote tag")
	}
}

// When the probe cannot read the remote it shows the real error and renders no
// force-push button. Nothing is pushed or overwritten in that state.
func TestTagsPushForceCheckFailureProtects(t *testing.T) {
	repo, bare, first, _ := tagsPushSidebarRepo(t, "tags-push-force-checkfail")
	repo.Git("push", "origin", "refs/tags/lw")
	setBareTag(t, bare, "lw", first)
	repo.Git("remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openTagPushDialog(t, h, "lw")
	clickForcePushBox(t, h)
	h.drain()

	if !h.ui.HasText("Cannot verify the remote tag") {
		t.Fatalf("probe failure warning missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("does not appear to be a git repository") {
		t.Fatalf("probe failure error text missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Force Push Tag"); ok {
		t.Fatalf("Confirm Force Push Tag present after failed probe: %+v", btn)
	}
	if err := h.ui.Click("Confirm Force Push Tag"); err == nil {
		t.Fatal("Confirm Force Push Tag was clickable after the probe failed")
	}
	if got := bareTagOID(t, bare, "lw"); got != first {
		t.Fatalf("remote lw = %q, want %q untouched", got, first)
	}
}

// With no remote tag the force push still uses an empty lease: a tag created
// concurrently after the probe is refused instead of overwritten.
func TestTagsPushForceEmptyLeaseRaceProtects(t *testing.T) {
	repo, bare, first, second := tagsPushSidebarRepo(t, "tags-push-force-race")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openTagPushDialog(t, h, "lw")
	clickForcePushBox(t, h)
	h.drain()
	if !h.ui.HasText("has no tag named lw yet") {
		t.Fatalf("empty-remote note missing; texts=%q", h.ui.Texts())
	}
	// The probe saw no remote tag. Someone else creates it before Confirm.
	setBareTag(t, bare, "lw", first)
	if err := h.ui.Click("Confirm Force Push Tag"); err != nil {
		t.Fatalf("click Confirm Force Push Tag: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("force push over a concurrently created tag produced no error")
	}
	if got := bareTagOID(t, bare, "lw"); got != first {
		t.Fatalf("remote lw = %q, want the concurrent %q untouched", got, first)
	}
	if got := bareTagOID(t, bare, "lw"); got == second {
		t.Fatal("the empty lease overwrote the concurrently created tag")
	}
}

// Choosing between two remotes in the dialog uses the chosen one. While force
// is ticked, changing the remote probes that remote again instead of reusing
// the first remote's answer.
func TestTagsPushMultiRemoteSelection(t *testing.T) {
	repo, originBare, _, second := tagsPushSidebarRepo(t, "tags-push-remotes")
	backupBare := repo.AddBareRemote("backup")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openTagPushDialog(t, h, "lw")
	// Keyboard-drive the real select: open it, move the highlight from origin
	// to backup, and choose with Enter.
	remotes := h.app.session.Snapshot().Refs.Remotes
	from, to := -1, -1
	for i, remote := range remotes {
		if remote == "origin" {
			from = i
		}
		if remote == "backup" {
			to = i
		}
	}
	if from < 0 || to < 0 || from == to {
		t.Fatalf("fixture remotes = %v, want origin and backup at different positions", remotes)
	}
	if err := h.ui.Click("Push Tag remote"); err != nil {
		t.Fatalf("open remote select: %v", err)
	}
	h.ui.Frame()
	for range absInt(to - from) {
		if to > from {
			h.ui.Key(0, ui.KeyDown)
		} else {
			h.ui.Key(0, ui.KeyUp)
		}
		h.ui.Frame()
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if got := h.app.tagsPushUI().pushRemote; got != "backup" {
		t.Fatalf("chosen remote = %q, want backup", got)
	}

	if err := h.ui.Click("Confirm Push Tag"); err != nil {
		t.Fatalf("click Confirm Push Tag: %v", err)
	}
	h.drain()

	if !bareTagExists(t, backupBare, "lw") {
		t.Fatal("lw did not reach the selected backup remote")
	}
	if got := bareTagOID(t, backupBare, "lw"); got != second {
		t.Fatalf("backup lw = %q, want HEAD %q", got, second)
	}
	if bareTagExists(t, originBare, "lw") {
		t.Fatal("lw reached origin even though backup was selected")
	}
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Push All Tags sends every local tag through its sidebar entry. A repeat run
// is the up-to-date toast, and a diverged remote tag is never overwritten.
func TestTagsPushAllTagsAndNeverOverwrite(t *testing.T) {
	repo, bare, first, second := tagsPushSidebarRepo(t, "tags-push-all")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	entry, ok := h.ui.AccessByRole(ui.RoleButton, "Push All Tags")
	if !ok || entry.Disabled {
		t.Fatalf("Push All Tags entry = %+v (found=%v), want enabled", entry, ok)
	}
	if err := h.ui.Click("Push All Tags"); err != nil {
		t.Fatalf("click Push All Tags: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("2 local tags are pushed without force.") {
		t.Fatalf("tag count missing from dialog; texts=%q", h.ui.Texts())
	}
	assertButtonInsideWindow(t, h, "Confirm Push All Tags")
	if err := h.ui.Click("Confirm Push All Tags"); err != nil {
		t.Fatalf("click Confirm Push All Tags: %v", err)
	}
	h.drain()

	for _, name := range []string{"lw", "ann"} {
		if !bareTagExists(t, bare, name) {
			t.Fatalf("%s did not reach the remote", name)
		}
	}
	if got := bareTagOID(t, bare, "lw"); got != second {
		t.Fatalf("remote lw = %q, want HEAD %q", got, second)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected push-all error: %v", err)
	}

	if err := h.ui.Click("Push All Tags"); err != nil {
		t.Fatalf("click Push All Tags again: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Push All Tags"); err != nil {
		t.Fatalf("click Confirm Push All Tags again: %v", err)
	}
	h.drain()
	if !h.ui.HasText(git.UpToDatePush) {
		t.Fatalf("push-all up-to-date toast missing; texts=%q", h.ui.Texts())
	}

	setBareTag(t, bare, "lw", first)
	if err := h.ui.Click("Push All Tags"); err != nil {
		t.Fatalf("click Push All Tags after divergence: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Push All Tags"); err != nil {
		t.Fatalf("click Confirm Push All Tags after divergence: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("pushing all tags over a diverged remote tag produced no error")
	}
	if got := bareTagOID(t, bare, "lw"); got != first {
		t.Fatalf("remote lw = %q, want the diverged %q untouched", got, first)
	}
}

// The Push All entry hides when there is nothing to send or nowhere to send
// it, instead of offering an operation that cannot work.
func TestTagsPushAllEntryVisibility(t *testing.T) {
	repo := newGitRepo(t, "tags-pushall-hidden")
	repo.CommitFile("a.txt", "one\n", "first commit")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Push All Tags"); ok {
		t.Fatal("Push All Tags visible without tags or remotes")
	}

	repo.Git("tag", "v1", "HEAD")
	h.app.session.Reload()
	h.drain()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Push All Tags"); ok {
		t.Fatal("Push All Tags visible without a remote")
	}

	repo.AddBareRemote("origin")
	h.app.session.Reload()
	h.drain()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Push All Tags"); !ok {
		t.Fatalf("Push All Tags missing with a tag and a remote; texts=%q", h.ui.Texts())
	}
}

// A ticked Push to remote in New Tag creates the tag and sends it in the same
// action. The checkbox and remote picker stay inside the dialog's window.
func TestTagsNewTagPushToRemote(t *testing.T) {
	repo, bare, _, second := tagsPushSidebarRepo(t, "tags-new-push")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag: %v", err)
	}
	h.ui.Frame()
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Push to remote"); !ok || box.Checked {
		t.Fatalf("Push to remote box = %+v (found=%v), want present and unticked", box, ok)
	}
	h.ui.Type("v9")
	h.ui.Frame()
	if err := h.ui.Click("Push to remote"); err != nil {
		t.Fatalf("click Push to remote: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Create Tag"); err != nil {
		t.Fatalf("click Create Tag: %v", err)
	}
	h.drain()

	if !tagRefExists(t, repo, "v9") {
		t.Fatal("v9 was not created locally")
	}
	if !bareTagExists(t, bare, "v9") {
		t.Fatal("v9 did not reach the remote")
	}
	if got := bareTagOID(t, bare, "v9"); got != second {
		t.Fatalf("remote v9 = %q, want HEAD %q", got, second)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected create-and-push error: %v", err)
	}
}

// When the push half fails, the created local tag is kept and the error is
// shown: a network failure must not destroy the tag the user just made.
func TestTagsNewTagPushFailureKeepsLocalTag(t *testing.T) {
	repo, _, _, _ := tagsPushSidebarRepo(t, "tags-new-push-fail")
	repo.Git("remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("kept")
	h.ui.Frame()
	if err := h.ui.Click("Push to remote"); err != nil {
		t.Fatalf("click Push to remote: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Create Tag"); err != nil {
		t.Fatalf("click Create Tag: %v", err)
	}
	h.drain()

	if !tagRefExists(t, repo, "kept") {
		t.Fatal("the local tag was lost when pushing it failed")
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("failed create-and-push produced no error")
	} else if !strings.Contains(err.Error(), "was created") {
		t.Fatalf("failed create-and-push error = %q, want it to say the tag was created", err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error after failed create-and-push; texts=%q", h.ui.Texts())
	}
}

// --- Loopback SSH variants ---

// closeMuxSockets gracefully exits every tracked ControlMaster connection of
// the target, so the next ssh invocation authenticates from scratch. The
// master removes its own socket file on exit, which keeps the sockets
// reachable for the target's teardown — unlike deleting the file underneath
// the mux.
func closeMuxSockets(t *testing.T, srv *sshtest.Target) {
	t.Helper()
	for _, socket := range srv.ControlPaths() {
		if _, err := os.Stat(socket); err != nil {
			continue
		}
		cmd := exec.Command(srv.SSHProgram(), "-o", "ControlPath="+socket, "-O", "exit", srv.User+"@"+srv.Host)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("close control socket %s: %v: %s", socket, err, out)
		}
		for i := 0; i < 40; i++ {
			if _, err := os.Stat(socket); os.IsNotExist(err) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if _, err := os.Stat(socket); err == nil {
			t.Fatalf("control socket %s still exists after ssh -O exit", socket)
		}
	}
}

func sshBareTagExists(t *testing.T, srv *sshtest.Target, bareDir, name string) bool {
	t.Helper()
	out := srv.RunGit(t, bareDir, "for-each-ref", "--format=%(refname)", "refs/tags/"+name)
	return strings.TrimSpace(out) == "refs/tags/"+name
}

func sshBareTagOID(t *testing.T, srv *sshtest.Target, bareDir, name string) string {
	t.Helper()
	return strings.TrimSpace(srv.RunGit(t, bareDir, "rev-parse", "refs/tags/"+name))
}

// Over loopback SSH the Push Tag and force-push dialogs act on the server's
// repository: the normal push reaches the server's bare remote, and the
// confirmed force push overwrites only after the live probe and agreement.
func TestTagsPushAndForceLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	first := srv.RunGit(t, repoDir, "rev-parse", "HEAD")
	srv.WriteFile(t, repoDir, "b.txt", []byte("two\n"))
	srv.RunGit(t, repoDir, "add", "b.txt")
	srv.RunGit(t, repoDir, "commit", "-q", "-m", "second commit")
	second := srv.RunGit(t, repoDir, "rev-parse", "HEAD")
	srv.RunGit(t, repoDir, "tag", "ssh-push", "HEAD")

	h := newTestApp(t, repo)
	closeSync(t, h)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	if _, ok := h.ui.Find("ssh-push"); !ok {
		t.Fatalf("ssh-push missing from sidebar; texts=%q", h.ui.Texts())
	}

	openTagPushDialog(t, h, "ssh-push")
	if err := h.ui.Click("Confirm Push Tag"); err != nil {
		t.Fatalf("click Confirm Push Tag: %v", err)
	}
	h.drain()
	if !sshBareTagExists(t, srv, bareDir, "ssh-push") {
		t.Fatal("ssh-push did not reach the server remote")
	}
	if got := sshBareTagOID(t, srv, bareDir, "ssh-push"); got != second {
		t.Fatalf("server remote ssh-push = %q, want HEAD %q", got, second)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH push error: %v", err)
	}

	// Diverge the remote tag on the server, then overwrite it through the
	// probed and confirmed force push.
	srv.RunGit(t, bareDir, "update-ref", "refs/tags/ssh-push", first)
	openTagPushDialog(t, h, "ssh-push")
	clickForcePushBox(t, h)
	h.drain()
	if !h.ui.HasText("will be overwritten") {
		t.Fatalf("SSH overwrite warning missing; texts=%q", h.ui.Texts())
	}
	clickForceAgreement(t, h)
	if err := h.ui.Click("Confirm Force Push Tag"); err != nil {
		t.Fatalf("click Confirm Force Push Tag: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("confirmed SSH force push failed: %v", err)
	}
	if got := sshBareTagOID(t, srv, bareDir, "ssh-push"); got != second {
		t.Fatalf("server remote ssh-push = %q, want force-pushed HEAD %q", got, second)
	}
}

// Push All Tags over loopback SSH sends the server's local tags to its remote;
// a diverged remote tag is refused and survives unchanged on the server.
func TestTagsPushAllLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repoDir, "tag", "one", "HEAD")
	srv.RunGit(t, repoDir, "tag", "-a", "two", "-m", "server notes", "HEAD")
	head := srv.RunGit(t, repoDir, "rev-parse", "HEAD")

	h := newTestApp(t, repo)
	closeSync(t, h)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	if err := h.ui.Click("Push All Tags"); err != nil {
		t.Fatalf("click Push All Tags: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Push All Tags"); err != nil {
		t.Fatalf("click Confirm Push All Tags: %v", err)
	}
	h.drain()
	for _, name := range []string{"one", "two"} {
		if !sshBareTagExists(t, srv, bareDir, name) {
			t.Fatalf("%s did not reach the server remote", name)
		}
	}
	if got := sshBareTagOID(t, srv, bareDir, "one"); got != head {
		t.Fatalf("server remote one = %q, want HEAD %q", got, head)
	}

	// Diverge the remote tag onto the object of the other tag: it exists in
	// the bare after the first push, and it is genuinely different from the
	// local one, so Push All must refuse instead of overwriting it.
	diverged := sshBareTagOID(t, srv, bareDir, "two")
	srv.RunGit(t, bareDir, "update-ref", "refs/tags/one", diverged)
	if err := h.ui.Click("Push All Tags"); err != nil {
		t.Fatalf("click Push All Tags after divergence: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Push All Tags"); err != nil {
		t.Fatalf("click Confirm Push All Tags after divergence: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("SSH push-all over a diverged remote tag produced no error")
	}
	if got := sshBareTagOID(t, srv, bareDir, "one"); got != diverged {
		t.Fatalf("server remote one = %q, want the diverged %q untouched", got, diverged)
	}
}

// A cancelled askpass answer cancels the push before anything reaches the
// remote. Retrying with the right key passphrase pushes the tag, and teardown
// leaves no branchline-askpass temporary directory behind.
func TestTagsPushAskPassLoopbackSSH(t *testing.T) {
	before := snapshotDirs(t, "branchline-askpass*")

	t.Run("cancel does not push then correct passphrase pushes", func(t *testing.T) {
		repo, srv := loopbackSSHRepo(t)
		repo.Identity = srv.GenerateKey(t, "tag-push-enc", "s3cret")
		repoDir := repo.Path
		bareDir := srv.InitBareRepo(t, "origin")
		srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
		srv.RunGit(t, repoDir, "tag", "askpush", "HEAD")

		h := newTestApp(t, repo)
		closeSync(t, h)
		selectRepo(t, h, repo)
		if err := h.app.session.LoadErr(); err == nil {
			t.Fatal("expected initial load to fail with the encrypted key locked")
		}

		// Unlock the connection once through a real toolbar Fetch so the server
		// sidebar can load; then close the master connection gracefully so the
		// tag push must authenticate again and ask itself. The master unlinks
		// its own socket on exit — deleting the file instead would leave a mux
		// the test teardown can no longer reach.
		clickFetch(t, h)
		h.ui.Frame()
		waitAskPass(t, h)
		h.ui.Type("s3cret")
		h.ui.Frame()
		if err := h.ui.Click("OK"); err != nil {
			t.Fatalf("click OK in passphrase dialog: %v", err)
		}
		h.ui.Frame()
		h.drain()
		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("unlocking fetch failed: %v", err)
		}
		if err := h.app.session.LoadErr(); err != nil {
			t.Fatalf("snapshot did not load after unlocking: %v", err)
		}
		if _, ok := h.ui.Find("askpush"); !ok {
			t.Fatalf("askpush missing from sidebar after unlocking; texts=%q", h.ui.Texts())
		}
		closeMuxSockets(t, srv)

		openTagPushDialog(t, h, "askpush")
		if err := h.ui.Click("Confirm Push Tag"); err != nil {
			t.Fatalf("click Confirm Push Tag: %v", err)
		}
		h.ui.Frame()
		waitAskPass(t, h)
		if err := h.ui.Click("Cancel"); err != nil {
			t.Fatalf("click Cancel in passphrase dialog: %v", err)
		}
		h.ui.Frame()
		h.drain()

		if err := h.app.session.ActionErr(); err == nil {
			t.Fatal("cancelled askpass push produced no error")
		}
		if sshBareTagExists(t, srv, bareDir, "askpush") {
			t.Fatal("askpush reached the remote even though askpass was cancelled")
		}

		openTagPushDialog(t, h, "askpush")
		if err := h.ui.Click("Confirm Push Tag"); err != nil {
			t.Fatalf("click Confirm Push Tag retry: %v", err)
		}
		h.ui.Frame()
		waitAskPass(t, h)
		h.ui.Type("s3cret")
		h.ui.Frame()
		if err := h.ui.Click("OK"); err != nil {
			t.Fatalf("click OK in retry passphrase dialog: %v", err)
		}
		h.ui.Frame()
		h.drain()

		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("push with answered passphrase failed: %v", err)
		}
		if !sshBareTagExists(t, srv, bareDir, "askpush") {
			t.Fatal("askpush did not reach the remote after the correct passphrase")
		}
	})

	if fresh := diffDirs(before, snapshotDirs(t, "branchline-askpass*")); len(fresh) > 0 {
		t.Fatalf("tag push askpass test leaked branchline-askpass* directories: %v", fresh)
	}
}
