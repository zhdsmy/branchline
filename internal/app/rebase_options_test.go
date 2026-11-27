package app

import (
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// --- fixtures and helpers ---

// drainIRForm waits for the form's plan read and its options probe to land
// and renders the result. Both settle inside one drain: the probe starts only
// after the plan read finishes, and the drain keeps running while either a
// worker is active or a UI callback is pending.
func drainIRForm(t *testing.T, h *testHarness) {
	t.Helper()
	h.drain()
	if interactiveRebaseUI(h.app).loading {
		t.Fatal("interactive rebase plan read still in flight after drain")
	}
	ost := interactiveRebaseOptionsUI(h.app)
	if ost.inspecting {
		t.Fatal("interactive rebase options probe still in flight after drain")
	}
}

// irBackupRefs lists the full names of the backup refs of the repository,
// so a test can prove exactly which of them a rebase created or left alone.
func irBackupRefs(t *testing.T, dir string) []string {
	t.Helper()
	out := strings.TrimSpace(gitOutTrim(t, dir, "for-each-ref", "--format=%(refname)", "refs/backup/"))
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// irDeleteBackupRefs removes every backup ref, so a rebuilt fixture starts
// from a clean slate and the next round can prove it creates none.
func irDeleteBackupRefs(t *testing.T, dir string) {
	t.Helper()
	for _, ref := range irBackupRefs(t, dir) {
		gitOutTrim(t, dir, "update-ref", "-d", ref)
	}
}

// irDependentFixture builds a linear history whose mid branch points at the
// middle commit: ir one is mid's tip, ir two and ir three follow it. Every
// commit adds its own file, so a reordered replay applies without conflicts.
// It returns the hashes of the three commits, oldest first.
func irDependentFixture(t *testing.T, repo *gitRepoFixture) []string {
	t.Helper()
	one := repo.CommitFile("ir1.txt", "ir one\n", "ir one")
	repo.CreateBranch("mid")
	two := repo.CommitFile("ir2.txt", "ir two\n", "ir two")
	three := repo.CommitFile("ir3.txt", "ir three\n", "ir three")
	return []string{one, two, three}
}

// --- backup branch option ---

// Create backup branch is present and unticked by default, ticking it
// previews the exact ref the backend would write, and Cancel writes nothing:
// reopening starts from the unchecked default again.
func TestRebaseInteractiveOptionsBackupCheckboxAndCancel(t *testing.T) {
	repo := newGitRepo(t, "iro-backup-cancel")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	irLinearFixture(t, repo, "ir one", "ir two")
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRForm(t, h)

	box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Create backup branch")
	if !ok {
		t.Fatalf("backup checkbox missing; texts=%q", h.ui.Texts())
	}
	if box.Checked {
		t.Fatalf("backup checkbox checked by default: %+v", box)
	}
	preview := "refs/backup/main-" + irShort(head)
	if err := h.ui.Click("Create backup branch"); err != nil {
		t.Fatalf("tick backup checkbox: %v", err)
	}
	h.ui.Frame()
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Create backup branch"); !ok || !box.Checked {
		t.Fatalf("backup checkbox after click = %+v found=%v, want checked", box, ok)
	}
	if !hasExactText(h.ui, preview) {
		t.Fatalf("backup ref preview %q missing; texts=%q", preview, h.ui.Texts())
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	assertIRDialogClosed(t, h)
	if refs := irBackupRefs(t, repo.Path()); len(refs) != 0 {
		t.Fatalf("canceled form created backup refs: %v", refs)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != head {
		t.Fatalf("canceled form moved HEAD: %q, want %q", got, head)
	}

	openIRDialog(t, h, onto, "base commit")
	drainIRForm(t, h)
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Create backup branch"); !ok || box.Checked {
		t.Fatalf("backup checkbox after reopen = %+v found=%v, want the unchecked default", box, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel again: %v", err)
	}
	h.ui.Frame()
}

// With the backup option ticked a drop rebase writes exactly one backup ref,
// pinned at the pre-rebase HEAD; unticked, the same rebase writes none.
func TestRebaseInteractiveOptionsBackupRefCreation(t *testing.T) {
	repo := newGitRepo(t, "iro-backup-create")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irLinearFixture(t, repo, "ir one", "ir two", "ir three")
	preHead := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRForm(t, h)
	if err := h.ui.Click("Create backup branch"); err != nil {
		t.Fatalf("tick backup checkbox: %v", err)
	}
	h.ui.Frame()
	chooseIRAction(t, h, hashes[1], "Pick", "Drop")
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("backup rebase failed: %v", err)
	}

	backupRef := "refs/backup/main-" + irShort(preHead)
	if got := gitOutTrim(t, repo.Path(), "rev-parse", backupRef); got != preHead {
		t.Fatalf("backup ref %s = %q, want the pre-rebase HEAD %q", backupRef, got, preHead)
	}
	if refs := irBackupRefs(t, repo.Path()); !slices.Equal(refs, []string{backupRef}) {
		t.Fatalf("backup refs after the rebase = %v, want only %v", refs, []string{backupRef})
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"ir three", "ir one", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after the backup drop rebase = %v, want %v", subjects, want)
	}
	assertIRDialogClosed(t, h)

	// Round 2: rebuild the fixture shape, clear the refs and rebase unticked:
	// no backup ref appears at all.
	repo.Git("update-ref", "refs/heads/main", preHead)
	repo.Git("reset", "--hard", "-q", preHead)
	irDeleteBackupRefs(t, repo.Path())
	h.app.session.Reload()
	h.drain()
	openIRDialog(t, h, onto, "base commit")
	drainIRForm(t, h)
	chooseIRAction(t, h, hashes[1], "Pick", "Drop")
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase unticked: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unticked rebase failed: %v", err)
	}
	if refs := irBackupRefs(t, repo.Path()); len(refs) != 0 {
		t.Fatalf("unticked rebase created backup refs: %v", refs)
	}
}

// A branch whose name carries a slash previews and creates the backup ref
// with the dashes the backend uses: feat/demo becomes refs/backup/feat-demo-…
func TestRebaseInteractiveOptionsBackupSlashBranch(t *testing.T) {
	repo := newGitRepo(t, "iro-backup-slash")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.CreateBranch("feat/demo")
	repo.Checkout("feat/demo")
	hashes := irLinearFixture(t, repo, "ir one", "ir two", "ir three")
	preHead := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRForm(t, h)
	if err := h.ui.Click("Create backup branch"); err != nil {
		t.Fatalf("tick backup checkbox: %v", err)
	}
	h.ui.Frame()
	preview := "refs/backup/feat-demo-" + irShort(preHead)
	if !hasExactText(h.ui, preview) {
		t.Fatalf("slash-branch backup preview %q missing; texts=%q", preview, h.ui.Texts())
	}
	chooseIRAction(t, h, hashes[2], "Pick", "Drop")
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("slash-branch backup rebase failed: %v", err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", preview); got != preHead {
		t.Fatalf("backup ref %s = %q, want the pre-rebase HEAD %q", preview, got, preHead)
	}
	if refs := irBackupRefs(t, repo.Path()); !slices.Equal(refs, []string{preview}) {
		t.Fatalf("backup refs after the rebase = %v, want only %v", refs, []string{preview})
	}
}

// The backup ref is written before the rebase starts, so an Edit stop followed
// by the banner's Abort restores the branch while the backup still pins the
// pre-rebase HEAD exactly.
func TestRebaseInteractiveOptionsBackupSurvivesAbort(t *testing.T) {
	repo := newGitRepo(t, "iro-backup-abort")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irLinearFixture(t, repo, "ir one", "ir two", "ir three")
	preHead := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRForm(t, h)
	if err := h.ui.Click("Create backup branch"); err != nil {
		t.Fatalf("tick backup checkbox: %v", err)
	}
	h.ui.Frame()
	chooseIRAction(t, h, hashes[0], "Pick", "Edit")
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase to the edit stop failed: %v", err)
	}
	st := h.app.session.Snapshot().Status
	if st.State != "rebase" || st.Stopped != "edit" {
		t.Fatalf("fixture: edit stop state = %+v, want rebase stopped at edit", st)
	}
	backupRef := "refs/backup/main-" + irShort(preHead)
	if got := gitOutTrim(t, repo.Path(), "rev-parse", backupRef); got != preHead {
		t.Fatalf("backup ref at the edit stop = %q, want the pre-rebase HEAD %q", got, preHead)
	}

	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("abort of the edit stop failed: %v", err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != preHead {
		t.Fatalf("main after abort = %q, want the pre-rebase HEAD %q", got, preHead)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", backupRef); got != preHead {
		t.Fatalf("backup ref after abort = %q, want it preserved at %q", got, preHead)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after abort = %q, want empty", got)
	}
}

// --- update dependent branches option ---

// With dependent branches in the range the checkbox appears enabled on a
// capable Git: unticked, mid stays pinned at its original commit; ticked with
// the dependent branches listed, mid moves with the replayed commit.
func TestRebaseInteractiveOptionsUpdateRefsMovesDependents(t *testing.T) {
	if !hostGitHasUpdateRefs(t) {
		t.Skip("host git is older than 2.38: update-ref lines cannot be exercised here")
	}
	repo := newGitRepo(t, "iro-update-refs")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irDependentFixture(t, repo)
	preHead := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRForm(t, h)
	box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches")
	if !ok {
		t.Fatalf("update-refs checkbox missing with dependents present; texts=%q", h.ui.Texts())
	}
	if box.Disabled {
		t.Fatalf("update-refs checkbox disabled on a capable Git: %+v", box)
	}
	if box.Checked {
		t.Fatalf("update-refs checkbox checked by default: %+v", box)
	}

	// The replay moves ir one after ir two: the replayed ir one is a new
	// commit, so a moved mid proves the update-ref line ran.
	if err := h.ui.Click("Move Up " + irShort(hashes[0])); err != nil {
		t.Fatalf("move ir one up: %v", err)
	}
	h.ui.Frame()

	// Round 1: unticked, mid stays at its original commit.
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("reordered rebase failed: %v", err)
	}
	if mid := gitOutTrim(t, repo.Path(), "rev-parse", "mid"); mid != hashes[0] {
		t.Fatalf("mid moved without update-refs: %q, want the original %q", mid, hashes[0])
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	if want := []string{"ir three", "ir one", "ir two", "base commit"}; !slices.Equal(subjects, want) {
		t.Fatalf("log after the reordered rebase = %v, want %v", subjects, want)
	}

	// Round 2: rebuild the fixture shape and tick the box: mid follows the
	// replayed ir one onto its new parent.
	repo.Git("update-ref", "refs/heads/main", preHead)
	repo.Git("update-ref", "refs/heads/mid", hashes[0])
	repo.Git("reset", "--hard", "-q", preHead)
	h.app.session.Reload()
	h.drain()
	openIRDialog(t, h, onto, "base commit")
	drainIRForm(t, h)
	if err := h.ui.Click("Move Up " + irShort(hashes[0])); err != nil {
		t.Fatalf("move ir one up again: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Update dependent branches"); err != nil {
		t.Fatalf("tick update-refs checkbox: %v", err)
	}
	h.ui.Frame()
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches"); !ok || !box.Checked {
		t.Fatalf("update-refs after click = %+v found=%v, want checked", box, ok)
	}
	if !hasExactText(h.ui, "mid") {
		t.Fatalf("dependent branch mid not listed while ticked; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase with update-refs: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("update-refs rebase failed: %v", err)
	}
	midAfter := gitOutTrim(t, repo.Path(), "rev-parse", "mid")
	if midAfter == hashes[0] {
		t.Fatalf("mid did not move with update-refs: %q", midAfter)
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s", "mid")); subject != "ir one" {
		t.Fatalf("mid subject after update-refs = %q, want ir one: mid is not the replayed commit", subject)
	}
	if inChain := gitOutTrim(t, repo.Path(), "rev-parse", "main~1"); midAfter != inChain {
		t.Fatalf("mid after update-refs = %q, want the replayed ir one inside the main chain at main~1 %q", midAfter, inChain)
	}
	if subjects := irLogSubjects(t, repo.Path(), "main"); !slices.Equal(subjects, []string{"ir three", "ir one", "ir two", "base commit"}) {
		t.Fatalf("log after the update-refs rebase = %v, want the reordered shape", subjects)
	}
}

// Without dependent branches the range offers nothing to move: the checkbox
// hides entirely while the backup option stays.
func TestRebaseInteractiveOptionsNoDependentsHidesUpdateRefs(t *testing.T) {
	repo := newGitRepo(t, "iro-no-deps")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	irLinearFixture(t, repo, "ir one", "ir two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRForm(t, h)
	if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches"); ok {
		t.Fatal("update-refs checkbox rendered without dependent branches")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Create backup branch"); !ok {
		t.Fatal("backup checkbox missing")
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// When the executor's Git reports a version without the update-ref
// capability, the checkbox is disabled with the version named, a direct
// guarded call with UpdateRefs — even together with Backup — is refused
// without writing anything, and an ordinary rebase still passes on the same
// fake old Git.
func TestRebaseInteractiveOptionsCapabilityGateOldGit(t *testing.T) {
	repo := newGitRepo(t, "iro-compat-old")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irDependentFixture(t, repo)
	preHead := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	fake := &versionFakeExecutor{version: "git version 2.25.1\n"}
	h := newTestApp(t)
	installVersionFakeExecutor(h, fake)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRForm(t, h)

	box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches")
	if !ok {
		t.Fatalf("update-refs checkbox missing with dependents present; texts=%q", h.ui.Texts())
	}
	if !box.Disabled {
		t.Fatalf("update-refs checkbox enabled on fake Git 2.25: %+v", box)
	}
	if box.Checked {
		t.Fatalf("update-refs checkbox checked on fake Git 2.25: %+v", box)
	}
	if !hasExactText(h.ui, "Requires Git 2.38 or newer (found git version 2.25.1).") {
		t.Fatalf("capability reason missing the found version; texts=%q", h.ui.Texts())
	}

	// A direct guarded call with UpdateRefs and Backup through the same fake
	// Git: refused, and neither a backup ref nor any branch moved.
	live := guardedFakeRepo(repo, fake)
	plan, err := live.RebaseRangePlan(onto)
	if err != nil {
		t.Fatalf("fixture plan read: %v", err)
	}
	err = interactiveRebaseWithOptionsGuard(live, preHead, "main", onto, plan, git.RebaseOptions{UpdateRefs: true, Backup: true})
	if err == nil || !strings.Contains(err.Error(), "--update-refs") {
		t.Fatalf("guarded UpdateRefs rebase on fake Git 2.25 = %v, want an --update-refs refusal", err)
	}
	if refs := irBackupRefs(t, repo.Path()); len(refs) != 0 {
		t.Fatalf("the refused rebase wrote backup refs: %v", refs)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "main"); head != preHead {
		t.Fatalf("main moved by the refused rebase: %q, want %q", head, preHead)
	}
	if mid := gitOutTrim(t, repo.Path(), "rev-parse", "mid"); mid != hashes[0] {
		t.Fatalf("mid moved by the refused rebase: %q, want %q", mid, hashes[0])
	}

	// The ordinary rebase still runs on the same fake old Git: update-refs is
	// the only capability the options section gates.
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("ordinary rebase on fake Git 2.25 failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	if want := []string{"ir three", "ir two", "ir one", "base commit"}; !slices.Equal(subjects, want) {
		t.Fatalf("log after the ordinary rebase = %v, want %v", subjects, want)
	}
	if mid := gitOutTrim(t, repo.Path(), "rev-parse", "mid"); mid != hashes[0] {
		t.Fatalf("mid moved without update-refs: %q, want %q", mid, hashes[0])
	}
}

// When the version probe itself fails, the checkbox is disabled with the
// verification reason and the guarded call is refused: a version that cannot
// be verified is never treated as a new one.
func TestRebaseInteractiveOptionsCapabilityGateProbeFailure(t *testing.T) {
	repo := newGitRepo(t, "iro-compat-probe")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irDependentFixture(t, repo)
	preHead := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	fake := &versionFakeExecutor{versionErr: errors.New("version probe failed")}
	h := newTestApp(t)
	installVersionFakeExecutor(h, fake)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRForm(t, h)

	box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches")
	if !ok {
		t.Fatalf("update-refs checkbox missing with dependents present; texts=%q", h.ui.Texts())
	}
	if !box.Disabled || box.Checked {
		t.Fatalf("update-refs after failed probe = %+v, want disabled and unchecked", box)
	}
	if !hasExactText(h.ui, "Git version could not be verified; requires Git 2.38 or newer.") {
		t.Fatalf("verification failure reason missing; texts=%q", h.ui.Texts())
	}

	live := guardedFakeRepo(repo, fake)
	plan, err := live.RebaseRangePlan(onto)
	if err != nil {
		t.Fatalf("fixture plan read: %v", err)
	}
	err = interactiveRebaseWithOptionsGuard(live, preHead, "main", onto, plan, git.RebaseOptions{UpdateRefs: true, Backup: true})
	if err == nil {
		t.Fatal("guarded UpdateRefs rebase passed despite the failed probe")
	}
	if refs := irBackupRefs(t, repo.Path()); len(refs) != 0 {
		t.Fatalf("the refused rebase wrote backup refs: %v", refs)
	}
	if head := gitOutTrim(t, repo.Path(), "rev-parse", "main"); head != preHead {
		t.Fatalf("main moved by the refused rebase: %q, want %q", head, preHead)
	}
	if mid := gitOutTrim(t, repo.Path(), "rev-parse", "mid"); mid != hashes[0] {
		t.Fatalf("mid moved by the refused rebase: %q, want %q", mid, hashes[0])
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// A refused write never creates the backup ref the user asked for: with
// Create backup branch ticked, uncommitted tracked changes, a moved HEAD and
// a failed status read each refuse the rebase, and every refusal leaves zero
// refs under refs/backup/ with the repository exactly as it was.
func TestRebaseInteractiveOptionsRefusalWritesNoBackup(t *testing.T) {
	repo := newGitRepo(t, "iro-refusal-backup")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	irLinearFixture(t, repo, "ir one", "ir two")
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// Round 1: an unstaged tracked modification refuses the rebase.
	repo.WriteFile("base.txt", "base\ndirty\n")
	openIRDialog(t, h, onto, "base commit")
	drainIRForm(t, h)
	if err := h.ui.Click("Create backup branch"); err != nil {
		t.Fatalf("tick backup checkbox: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	assertIRRefusal(t, h, "cannot rebase with uncommitted local changes")
	if refs := irBackupRefs(t, repo.Path()); len(refs) != 0 {
		t.Fatalf("the dirty refusal wrote backup refs: %v", refs)
	}
	assertIRRepoUnchangedExcept(t, repo, head, "base.txt", "base\ndirty\n")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	repo.Git("checkout", "--", "base.txt")

	// Round 2: an external commit moves HEAD after the form opened.
	openIRDialog(t, h, onto, "base commit")
	drainIRForm(t, h)
	if err := h.ui.Click("Create backup branch"); err != nil {
		t.Fatalf("tick backup checkbox: %v", err)
	}
	h.ui.Frame()
	repo.WriteFile("other.txt", "other\n")
	repo.Git("add", "--", "other.txt")
	repo.Git("commit", "-m", "external commit")
	moved := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	assertIRRefusal(t, h, "HEAD or the current branch changed")
	if refs := irBackupRefs(t, repo.Path()); len(refs) != 0 {
		t.Fatalf("the head-moved refusal wrote backup refs: %v", refs)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != moved {
		t.Fatalf("HEAD moved by the refused rebase: %q, want the external commit %q", got, moved)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	// Round 3: the guard cannot even read the repository state.
	openIRDialog(t, h, onto, "base commit")
	drainIRForm(t, h)
	if err := h.ui.Click("Create backup branch"); err != nil {
		t.Fatalf("tick backup checkbox: %v", err)
	}
	h.ui.Frame()
	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	assertIRRefusal(t, h, "reading the repository state failed")
	if refs := irBackupRefs(t, repo.Path()); len(refs) != 0 {
		t.Fatalf("the status-failure refusal wrote backup refs: %v", refs)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != moved {
		t.Fatalf("HEAD moved after the failed status read: %q, want %q", got, moved)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// Switching the repository clears the options draft and cancels its probe: a
// ticked checkbox and the probed capabilities belong to the repository that
// is gone, so the state returns to zero and a reopened form starts empty.
func TestRebaseInteractiveOptionsClearOnRepoSwitch(t *testing.T) {
	repoA := newGitRepo(t, "iro-switch-a")
	repoA.CommitFile("base.txt", "base\n", "base commit")
	ontoA := strings.TrimSpace(repoA.Git("rev-parse", "HEAD"))
	irLinearFixture(t, repoA, "ir one", "ir two")
	repoB := newGitRepo(t, "iro-switch-b")
	repoB.CommitFile("b.txt", "b\n", "commit in beta")

	h := newTestApp(t, repoB.StoreRepo())
	openHistoryPage(t, h, repoA.StoreRepo())
	openIRDialog(t, h, ontoA, "base commit")
	drainIRForm(t, h)
	if err := h.ui.Click("Create backup branch"); err != nil {
		t.Fatalf("tick backup checkbox: %v", err)
	}
	h.ui.Frame()
	ost := interactiveRebaseOptionsUI(h.app)
	if !ost.backup {
		t.Fatal("fixture: backup draft did not tick")
	}

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
	h.drain()

	ost = interactiveRebaseOptionsUI(h.app)
	if ost.onto != "" || ost.branch != "" || ost.backup || ost.updateRefs || len(ost.dependents) != 0 {
		t.Fatalf("options draft survived the repository switch: %+v", ost)
	}
	if ost.capsErr != nil {
		t.Fatalf("probe error survived the repository switch: %v", ost.capsErr)
	}

	// Reopening the form in the other repository starts from zero: the
	// checkbox renders unticked even though the probe has no answer yet.
	repoB.CommitFile("b2.txt", "b2\n", "more in beta")
	h.app.session.Reload()
	h.drain()
	ontoB := strings.TrimSpace(repoB.Git("rev-parse", "HEAD~1"))
	openIRDialog(t, h, ontoB, "commit in beta")
	drainIRForm(t, h)
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Create backup branch"); !ok || box.Checked {
		t.Fatalf("backup checkbox after reopening elsewhere = %+v found=%v, want the unchecked default", box, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// Settling the form triggers the options probe directly from plan completion
// without busy-reposting on the UI queue, and clicking Retry after a transient
// failure re-probes dependent branches once the repository recovers.
func TestRebaseInteractiveOptionsProbeNoBusySpinAndRetry(t *testing.T) {
	repo := newGitRepo(t, "iro-no-spin-retry")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	irDependentFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// Round 1: opening the form settles plan read + options probe in a tiny
	// bounded number of UI callbacks (never busy-reposting while loading).
	openIRDialog(t, h, onto, "base commit")
	ran := h.queue.Drain(t)
	h.ui.Frame()
	if ran > 16 {
		t.Fatalf("draining interactive rebase form ran %d UI callbacks, want <= 16 (no busy-spin)", ran)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches"); !ok {
		t.Fatalf("update-refs checkbox missing after settled probe; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	// Round 2: a transient rev-list failure breaks both plan read and
	// dependent-branch discovery; once cleared, clicking Retry recovers both.
	var failRevList atomic.Bool
	failRevList.Store(true)
	installCommandFailExecutor(t, h, func(args []string) bool {
		return failRevList.Load() && len(args) > 0 && args[0] == "rev-list"
	}, errors.New("injected transient rev-list failure"))

	openIRDialog(t, h, onto, "base commit")
	drainIRForm(t, h)
	if !hasExactText(h.ui, "Retry") {
		t.Fatalf("Retry missing after transient failure; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches"); ok {
		t.Fatal("update-refs checkbox rendered while dependent branches read failed")
	}

	failRevList.Store(false)
	if err := h.ui.Click("Retry"); err != nil {
		t.Fatalf("click Retry: %v", err)
	}
	drainIRForm(t, h)
	if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches"); !ok {
		t.Fatalf("update-refs checkbox still missing after Retry recovered; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel after Retry: %v", err)
	}
	h.ui.Frame()
}

// Switching the repository while the options probe itself is in flight cancels
// ir-options and discards its late callback so the previous repository's
// dependent branches never land in the next repository.
func TestRebaseInteractiveOptionsLateProbeDiscardedOnRepoSwitch(t *testing.T) {
	repoA := newGitRepo(t, "iro-late-probe-a")
	repoA.CommitFile("base.txt", "base\n", "base commit")
	ontoA := strings.TrimSpace(repoA.Git("rev-parse", "HEAD"))
	irDependentFixture(t, repoA)
	repoB := newGitRepo(t, "iro-late-probe-b")
	repoB.CommitFile("b.txt", "b\n", "commit in beta")

	h := newTestApp(t, repoB.StoreRepo())
	openHistoryPage(t, h, repoA.StoreRepo())

	// Hold the plan worker, release it while PauseWorkers stays armed so the
	// options probe spawned by plan completion is captured cleanly in flight.
	h.queue.PauseWorkers()
	openIRDialog(t, h, ontoA, "base commit")
	planWorker := h.queue.WaitWorker(t)
	h.queue.ReleaseWorker(planWorker)
	h.queue.RunNext(t)
	optionsWorker := h.queue.WaitWorker(t)
	if !interactiveRebaseOptionsUI(h.app).inspecting {
		t.Fatal("fixture: expected ir-options probe to be inspecting while held")
	}

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
	h.queue.ReleaseWorker(optionsWorker)
	h.drain()

	ost := interactiveRebaseOptionsUI(h.app)
	if ost.onto != "" || ost.inspecting || ost.probed || len(ost.dependents) != 0 {
		t.Fatalf("late options probe survived the repository switch: %+v", ost)
	}
}

// With dependents present and both options ticked the form — its preview,
// its checkboxes, its dependent branch list and its buttons — stays
// horizontally centered in the 1200-wide window, every control inside the
// dialog bounds.
func TestRebaseInteractiveOptionsLayoutCentered(t *testing.T) {
	if !hostGitHasUpdateRefs(t) {
		t.Skip("host git is older than 2.38: the enabled options layout cannot be exercised here")
	}
	repo := newGitRepo(t, "iro-layout")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	irDependentFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRForm(t, h)
	if err := h.ui.Click("Create backup branch"); err != nil {
		t.Fatalf("tick backup checkbox: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Update dependent branches"); err != nil {
		t.Fatalf("tick update-refs checkbox: %v", err)
	}
	h.ui.Frame()

	dlg, ok := h.ui.AccessByRole(ui.RoleDialog, "")
	if !ok {
		t.Fatal("no dialog present")
	}
	for _, label := range []string{"Create backup branch", "Update dependent branches"} {
		box, ok := h.ui.AccessByRole(ui.RoleCheckBox, label)
		if !ok {
			t.Fatalf("%s checkbox missing; texts=%q", label, h.ui.Texts())
		}
		assertInside(t, box.Bounds, dlg.Bounds, "checkbox "+label)
	}
	assertDialogLayout(t, h, "Cancel", "Rebase")

	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// --- SSH loopback ---

// Over a real SSH connection the backup option writes exactly one backup ref
// on the server, pinned at the pre-rebase HEAD — both when the drop rebase
// finishes and when an Edit stop is aborted, which restores the remote branch
// while the backup stays. Every assertion reads the server through a second
// SSH channel.
func TestRebaseInteractiveOptionsBackupLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	onto, hashes := irRemoteLinear(t, srv, repo.Path, "ir one", "ir two", "ir three")
	preHead := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	backupRef := "refs/backup/main-" + irShort(preHead)

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)

	// Round 1: a ticked drop rebase writes the backup ref on the server.
	openIRDialog(t, h, onto, "initial commit")
	drainIRForm(t, h)
	if err := h.ui.Click("Create backup branch"); err != nil {
		t.Fatalf("tick backup checkbox: %v", err)
	}
	h.ui.Frame()
	chooseIRAction(t, h, hashes[1], "Pick", "Drop")
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH backup rebase failed: %v", err)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", backupRef)); got != preHead {
		t.Fatalf("remote backup ref %s = %q, want the pre-rebase HEAD %q", backupRef, got, preHead)
	}
	if refs := strings.TrimSpace(srv.RunGit(t, repo.Path, "for-each-ref", "--format=%(refname)", "refs/backup/")); refs != backupRef {
		t.Fatalf("remote backup refs after the rebase = %q, want only %q", refs, backupRef)
	}
	out := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "--format=%s", "main"))
	if want := []string{"ir three", "ir one", "initial commit"}; !slices.Equal(strings.Split(out, "\n"), want) {
		t.Fatalf("remote log after the backup drop rebase = %v, want %v", strings.Split(out, "\n"), want)
	}

	// Round 2: rebuild the fixture, then an Edit stop aborted through the
	// banner restores the branch and keeps the backup.
	srv.RunGit(t, repo.Path, "update-ref", "refs/heads/main", preHead)
	srv.RunGit(t, repo.Path, "reset", "--hard", "-q", preHead)
	srv.RunGit(t, repo.Path, "update-ref", "-d", backupRef)
	h.app.session.Reload()
	h.drain()
	openIRDialog(t, h, onto, "initial commit")
	drainIRForm(t, h)
	if err := h.ui.Click("Create backup branch"); err != nil {
		t.Fatalf("tick backup checkbox again: %v", err)
	}
	h.ui.Frame()
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
		t.Fatalf("fixture: SSH edit stop state = %+v, want rebase stopped at edit", st)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", backupRef)); got != preHead {
		t.Fatalf("remote backup ref at the edit stop = %q, want the pre-rebase HEAD %q", got, preHead)
	}
	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH abort of the edit stop failed: %v", err)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "main")); got != preHead {
		t.Fatalf("remote main after abort = %q, want the pre-rebase HEAD %q", got, preHead)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", backupRef)); got != preHead {
		t.Fatalf("remote backup ref after abort = %q, want it preserved at %q", got, preHead)
	}
}

// Over SSH the update-refs option moves the server's dependent branch mid
// with the replayed commit, and a dirty working tree refuses the write with
// zero backup refs created and the server exactly as it was.
func TestRebaseInteractiveOptionsUpdateRefsLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	onto := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	one := remoteCommitFile(t, srv, repo.Path, "ir1.txt", "ir one\n", "ir one")
	srv.RunGit(t, repo.Path, "branch", "mid")
	remoteCommitFile(t, srv, repo.Path, "ir2.txt", "ir two\n", "ir two")
	remoteCommitFile(t, srv, repo.Path, "ir3.txt", "ir three\n", "ir three")
	preHead := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))

	if !hostGitHasUpdateRefs(t) {
		t.Skip("host git is older than 2.38: update-ref lines cannot be exercised here")
	}
	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)

	// Round 1: ticked, mid follows the replayed ir one after the reorder.
	openIRDialog(t, h, onto, "initial commit")
	drainIRForm(t, h)
	box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches")
	if !ok || box.Disabled {
		t.Fatalf("SSH update-refs checkbox = %+v found=%v, want present and enabled", box, ok)
	}
	if err := h.ui.Click("Move Up " + irShort(one)); err != nil {
		t.Fatalf("move ir one up: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Update dependent branches"); err != nil {
		t.Fatalf("tick update-refs checkbox: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "mid") {
		t.Fatalf("dependent branch mid not listed while ticked; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH update-refs rebase failed: %v", err)
	}
	midAfter := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "mid"))
	if midAfter == one {
		t.Fatalf("remote mid did not move with update-refs: %q", midAfter)
	}
	if subject := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "-1", "--format=%s", "mid")); subject != "ir one" {
		t.Fatalf("remote mid subject after update-refs = %q, want ir one", subject)
	}

	// Round 2: a dirty tracked file refuses the rebase with the backup option
	// ticked: zero backup refs on the server, nothing moved, nothing touched.
	srv.RunGit(t, repo.Path, "update-ref", "refs/heads/main", preHead)
	srv.RunGit(t, repo.Path, "update-ref", "refs/heads/mid", one)
	srv.RunGit(t, repo.Path, "reset", "--hard", "-q", preHead)
	for _, ref := range strings.Fields(srv.RunGit(t, repo.Path, "for-each-ref", "--format=%(refname)", "refs/backup/")) {
		srv.RunGit(t, repo.Path, "update-ref", "-d", ref)
	}
	h.app.session.Reload()
	h.drain()
	srv.WriteFile(t, repo.Path, "ir1.txt", []byte("ir one\ndirty\n"))
	openIRDialog(t, h, onto, "initial commit")
	drainIRForm(t, h)
	if err := h.ui.Click("Create backup branch"); err != nil {
		t.Fatalf("tick backup checkbox: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	assertIRRefusal(t, h, "cannot rebase with uncommitted local changes")
	if refs := strings.TrimSpace(srv.RunGit(t, repo.Path, "for-each-ref", "--format=%(refname)", "refs/backup/")); refs != "" {
		t.Fatalf("the SSH dirty refusal wrote backup refs: %q", refs)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "main")); got != preHead {
		t.Fatalf("remote main moved by the refused rebase: %q, want %q", got, preHead)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "mid")); got != one {
		t.Fatalf("remote mid moved by the refused rebase: %q, want %q", got, one)
	}
	if got := srv.ReadFile(t, repo.Path, "ir1.txt"); string(got) != "ir one\ndirty\n" {
		t.Fatalf("remote ir1.txt touched by the refused rebase: %q", string(got))
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}
