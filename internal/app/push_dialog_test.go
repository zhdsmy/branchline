package app

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// --- helpers ---

// clickPushConfirm publishes through the real product path a plain toolbar
// Push click now drives: the click opens the Push dialog, and the dialog's
// confirm button starts the push. The caller drains.
func clickPushConfirm(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Push"); err != nil {
		t.Fatalf("click Push: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Push"); err != nil {
		t.Fatalf("click Confirm Push: %v\ntexts=%q", err, h.ui.Texts())
	}
}

// openToolbarPushDialog clicks the toolbar Push button and asserts the Push
// dialog opened.
func openToolbarPushDialog(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Push"); err != nil {
		t.Fatalf("click Push: %v", err)
	}
	h.ui.Frame()
	st := pushDialogUI(h.app)
	if !st.open {
		t.Fatalf("Push dialog did not open; texts=%q", h.ui.Texts())
	}
}

// driveSelect keyboard-drives the select whose trigger carries label: the
// popup opens, the highlight walks from the option at fromIdx to the one at
// toIdx, and Enter chooses it — the same drive the tag push select tests use.
func driveSelect(t *testing.T, h *testHarness, label string, fromIdx, toIdx int) {
	t.Helper()
	if err := h.ui.Click(label); err != nil {
		t.Fatalf("open select %q: %v", label, err)
	}
	h.ui.Frame()
	steps := toIdx - fromIdx
	if steps < 0 {
		steps = -steps
	}
	for range steps {
		if toIdx > fromIdx {
			h.ui.Key(0, ui.KeyDown)
		} else {
			h.ui.Key(0, ui.KeyUp)
		}
		h.ui.Frame()
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
}

// selectPushBranch switches the dialog's local branch to name through the
// real select control.
func selectPushBranch(t *testing.T, h *testHarness, name string) {
	t.Helper()
	st := pushDialogUI(h.app)
	from, to := -1, -1
	for i, b := range st.branches {
		if b.Name == st.branch {
			from = i
		}
		if b.Name == name {
			to = i
		}
	}
	if from < 0 || to < 0 {
		t.Fatalf("cannot switch branch %q -> %q in %v", st.branch, name, st.branches)
	}
	driveSelect(t, h, "Push Local Branch", from, to)
	if got := pushDialogUI(h.app).branch; got != name {
		t.Fatalf("dialog branch = %q, want %q", got, name)
	}
}

// selectPushRemote switches the dialog's remote to name through the real
// select control.
func selectPushRemote(t *testing.T, h *testHarness, name string) {
	t.Helper()
	st := pushDialogUI(h.app)
	from, to := -1, -1
	for i, r := range st.remotes {
		if r == st.remote {
			from = i
		}
		if r == name {
			to = i
		}
	}
	if from < 0 || to < 0 {
		t.Fatalf("cannot switch remote %q -> %q in %v", st.remote, name, st.remotes)
	}
	driveSelect(t, h, "Push Remote", from, to)
	if got := pushDialogUI(h.app).remote; got != name {
		t.Fatalf("dialog remote = %q, want %q", got, name)
	}
}

// focusPushDest clicks the remote branch input, selects its text and deletes
// it, so the next Type starts from an empty field.
func focusPushDest(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Push Remote Branch"); err != nil {
		t.Fatalf("focus remote branch input: %v", err)
	}
	clearInput(t, h.ui)
}

// typePushDest replaces the remote branch name with text.
func typePushDest(t *testing.T, h *testHarness, text string) {
	t.Helper()
	focusPushDest(t, h)
	if text != "" {
		h.ui.Type(text)
	}
	h.ui.Frame()
}

// bareHasRef reports whether ref resolves in the bare repository.
func bareHasRef(t *testing.T, bareDir, ref string) bool {
	t.Helper()
	_, err := tryGitCommand(bareDir, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

// aheadFixture builds a repository whose main tracks origin/main and is one
// commit ahead of it, so a push always has something to send.
func aheadFixture(t *testing.T, name string) (*gitRepoFixture, string, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	head := repo.CommitFile("ahead.txt", "ahead\n", "unpushed commit")
	return repo, bareDir, head
}

// tickBox clicks a checkbox by its accessibility label and renders a frame.
func tickBox(t *testing.T, h *testHarness, label string) {
	t.Helper()
	if err := h.ui.Click(label); err != nil {
		t.Fatalf("click %s: %v\ntexts=%q", label, err, h.ui.Texts())
	}
	h.ui.Frame()
}

// --- pure functions ---

// normalizePushDest strips surrounding whitespace and the refs/heads/ prefix
// of a full ref spelling.
func TestPushDialogNormalizeDest(t *testing.T) {
	cases := []struct{ in, want string }{
		{"feature", "feature"},
		{"  feature  ", "feature"},
		{"refs/heads/feature", "feature"},
		{" refs/heads/feature ", "feature"},
		{"refs/heads/", ""},
		{"", ""},
		{"   ", ""},
		{"release/x", "release/x"},
		{"refs/heads/release/x", "release/x"},
	}
	for _, tc := range cases {
		if got := normalizePushDest(tc.in); got != tc.want {
			t.Errorf("normalizePushDest(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// pushDestProblem answers an empty name first and then shares the branch
// name rules, including the leading dash and the trailing slash Git would
// misread.
func TestPushDialogDestProblem(t *testing.T) {
	cases := []struct{ in, want string }{
		{"feature", ""},
		{"release/x", ""},
		{"", "Enter a remote branch name"},
		{"   ", "Enter a remote branch name"},
		{"refs/heads/", "Enter a remote branch name"},
		{"-mine", "name cannot begin with '-'"},
		{"two words", "name cannot contain whitespace"},
		{"trailing/", "name cannot end with '/'"},
		{"bad..name", "Must not contain '..'."},
	}
	for _, tc := range cases {
		if got := pushDestProblem(tc.in); got != tc.want {
			t.Errorf("pushDestProblem(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// pushDialogDefaults follows the upstream when it is live, and the default
// remote with the branch's own name and tracking otherwise.
func TestPushDialogDefaultsPure(t *testing.T) {
	remotes := []string{"origin", "backup"}

	tracked := git.Branch{Name: "main", Upstream: "origin/main"}
	if remote, dest, setUpstream := pushDialogDefaults(tracked, remotes); remote != "origin" || dest != "main" || setUpstream {
		t.Errorf("defaults of tracked main = (%q, %q, %v), want (origin, main, false)", remote, dest, setUpstream)
	}

	renamed := git.Branch{Name: "work", Upstream: "origin/release"}
	if remote, dest, setUpstream := pushDialogDefaults(renamed, remotes); remote != "origin" || dest != "release" || setUpstream {
		t.Errorf("defaults of renamed upstream = (%q, %q, %v), want (origin, release, false)", remote, dest, setUpstream)
	}

	untracked := git.Branch{Name: "feature"}
	if remote, dest, setUpstream := pushDialogDefaults(untracked, remotes); remote != "origin" || dest != "feature" || !setUpstream {
		t.Errorf("defaults of untracked feature = (%q, %q, %v), want (origin, feature, true)", remote, dest, setUpstream)
	}

	gone := git.Branch{Name: "stale", Upstream: "origin/stale", Gone: true}
	if remote, dest, setUpstream := pushDialogDefaults(gone, remotes); remote != "origin" || dest != "stale" || !setUpstream {
		t.Errorf("defaults of gone upstream = (%q, %q, %v), want (origin, stale, true)", remote, dest, setUpstream)
	}

	// An upstream whose remote is no longer configured is not live, and the
	// default is the first remote when there is no origin.
	dangling := git.Branch{Name: "orphaned", Upstream: "removed/orphaned"}
	if remote, dest, setUpstream := pushDialogDefaults(dangling, remotes); remote != "origin" || dest != "orphaned" || !setUpstream {
		t.Errorf("defaults of dangling upstream = (%q, %q, %v), want (origin, orphaned, true)", remote, dest, setUpstream)
	}
	if remote, _, _ := pushDialogDefaults(untracked, []string{"mirror", "upstream"}); remote != "mirror" {
		t.Errorf("default remote without origin = %q, want mirror (the first remote)", remote)
	}
}

// formatPushError leads with the lease reason when the porcelain output says
// the remote moved since the last fetch, merges rejection reasons stderr
// never printed, and leaves an unrelated error alone.
func TestPushDialogFormatPushError(t *testing.T) {
	staleErr := &git.Error{
		Args:     []string{"push"},
		ExitCode: 1,
		Stderr:   "error: failed to push some refs to '/tmp/bare.git'",
		Stdout:   []byte("!\trefs/heads/main:refs/heads/main\t[rejected] (stale info)\n"),
	}
	err := formatPushError(staleErr, true)
	if err == nil {
		t.Fatal("formatPushError returned nil for a stale lease rejection")
	}
	if !strings.HasPrefix(err.Error(), "Force push rejected (stale info)") {
		t.Fatalf("stale force push error = %q, want it to lead with the stale info reason", err)
	}
	if !strings.Contains(err.Error(), "refs/heads/main:refs/heads/main") {
		t.Fatalf("stale error %q missing the rejected ref", err)
	}
	if err := formatPushError(staleErr, false); err == nil || !strings.HasPrefix(err.Error(), "Push rejected (stale info)") {
		t.Fatalf("non-force stale error = %v, want the Push rejected prefix", err)
	}

	nonFF := &git.Error{
		Args:     []string{"push"},
		ExitCode: 1,
		Stderr:   "error: failed to push some refs to '/tmp/bare.git'",
		Stdout:   []byte("!\trefs/heads/main:refs/heads/main\t[rejected] (non-fast-forward)\n"),
	}
	err = formatPushError(nonFF, false)
	if !strings.Contains(err.Error(), "(non-fast-forward)") {
		t.Fatalf("merged error = %q, want the porcelain reason merged in", err)
	}

	already := &git.Error{
		Args:     []string{"push"},
		ExitCode: 1,
		Stderr:   "error: failed to push some refs\n! [rejected] (hook declined)\n",
		Stdout:   []byte("!\trefs/heads/main:refs/heads/main\t[rejected] (hook declined)\n"),
	}
	if got := formatPushError(already, false); got.Error() != already.Error() {
		t.Fatalf("already-reported reason got merged again: %q", got)
	}

	plain := errors.New("plain failure")
	if got := formatPushError(plain, false); got.Error() != "plain failure" {
		t.Fatalf("unrelated error changed: %q", got)
	}
	if got := formatPushError(nil, false); got != nil {
		t.Fatalf("nil error became %v", got)
	}
}

// --- dialog defaults against real repositories ---

// The dialog of a branch tracking origin/main defaults to the upstream's
// remote and branch name, without tracking, force or tags ticked.
func TestPushDialogDefaultsTrackedBranch(t *testing.T) {
	repo := newGitRepo(t, "pushdlg-def-tracked")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPushDialog(t, h)
	st := pushDialogUI(h.app)
	if st.branch != "main" || st.remote != "origin" || st.dest != "main" {
		t.Fatalf("defaults = (%q, %q, %q), want (main, origin, main)", st.branch, st.remote, st.dest)
	}
	if st.setUpstream || st.force || st.forceAgree || st.tags {
		t.Fatalf("options = (upstream=%v, force=%v, agree=%v, tags=%v), want all false", st.setUpstream, st.force, st.forceAgree, st.tags)
	}
	for label, want := range map[string]bool{
		"Set upstream tracking":   false,
		"Force push (with lease)": false,
		"Push all tags":           false,
	} {
		box, ok := h.ui.AccessByRole(ui.RoleCheckBox, label)
		if !ok || box.Checked != want {
			t.Fatalf("%s box = %+v (found=%v), want unticked", label, box, ok)
		}
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Push"); !ok || btn.Disabled {
		t.Fatalf("Confirm Push = %+v (found=%v), want enabled", btn, ok)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Force Push"); ok {
		t.Fatal("Confirm Force Push visible without force")
	}
}

// A branch whose upstream has a different name — local work tracking
// origin/release — fills the remote branch name with the upstream's.
func TestPushDialogDefaultsRenamedUpstream(t *testing.T) {
	repo := newGitRepo(t, "pushdlg-def-renamed")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.Git("checkout", "-b", "work")
	repo.CommitFile("w.txt", "w\n", "work commit")
	bareDir := repo.AddBareRemote("origin")
	repo.Git("push", "-u", "origin", "work:refs/heads/release")
	if up := strings.TrimSpace(repo.Git("for-each-ref", "--format=%(upstream:short)", "refs/heads/work")); up != "origin/release" {
		t.Fatalf("work upstream = %q, want origin/release", up)
	}
	_ = bareDir

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPushDialog(t, h)
	st := pushDialogUI(h.app)
	if st.branch != "work" || st.remote != "origin" || st.dest != "release" {
		t.Fatalf("defaults = (%q, %q, %q), want (work, origin, release)", st.branch, st.remote, st.dest)
	}
	if st.setUpstream {
		t.Fatal("Set upstream tracking ticked for a live upstream")
	}
}

// A branch without upstream publishes with tracking ticked by default, to
// origin when there is one, otherwise to the repository's first remote.
func TestPushDialogDefaultsUntrackedAndNonOrigin(t *testing.T) {
	t.Run("untracked with origin", func(t *testing.T) {
		repo := newGitRepo(t, "pushdlg-def-untracked")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		addSecondBareRemote(t, repo, "origin")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		openToolbarPushDialog(t, h)
		st := pushDialogUI(h.app)
		if st.remote != "origin" || st.dest != "main" || !st.setUpstream {
			t.Fatalf("defaults = (%q, %q, upstream=%v), want (origin, main, true)", st.remote, st.dest, st.setUpstream)
		}
	})

	t.Run("untracked without origin", func(t *testing.T) {
		repo := newGitRepo(t, "pushdlg-def-noorigin")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		addSecondBareRemote(t, repo, "upstream")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		openToolbarPushDialog(t, h)
		st := pushDialogUI(h.app)
		if st.remote != "upstream" || st.dest != "main" || !st.setUpstream {
			t.Fatalf("defaults = (%q, %q, upstream=%v), want (upstream, main, true)", st.remote, st.dest, st.setUpstream)
		}
	})
}

// A branch whose upstream is gone publishes like an untracked one: the
// remote defaults fresh and tracking is ticked.
func TestPushDialogDefaultsGoneUpstream(t *testing.T) {
	repo := newGitRepo(t, "pushdlg-def-gone")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	runGitCommand(t, bareDir, "update-ref", "-d", "refs/heads/main")
	repo.Git("fetch", "--prune", "origin")
	if out := repo.Git("for-each-ref", "--format=%(upstream:track)", "refs/heads/main"); !strings.Contains(out, "gone") {
		t.Fatalf("upstream not reported gone, upstream:track = %q", out)
	}

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPushDialog(t, h)
	st := pushDialogUI(h.app)
	if st.remote != "origin" || st.dest != "main" || !st.setUpstream {
		t.Fatalf("defaults = (%q, %q, upstream=%v), want (origin, main, true)", st.remote, st.dest, st.setUpstream)
	}
}

// Every reopen starts unticked: a force push and a tag push ticked in one
// dialog never carry over to the next.
func TestPushDialogReopenResetsForceAndTags(t *testing.T) {
	repo, _, _ := aheadFixture(t, "pushdlg-def-reopen")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPushDialog(t, h)
	tickBox(t, h, "Force push (with lease)")
	tickBox(t, h, "Confirm force push with lease")
	tickBox(t, h, "Push all tags")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	openToolbarPushDialog(t, h)
	st := pushDialogUI(h.app)
	if st.force || st.forceAgree || st.tags {
		t.Fatalf("reopened options = (force=%v, agree=%v, tags=%v), want all false", st.force, st.forceAgree, st.tags)
	}
}

// --- real pushes through the dialog ---

// The default dialog pushes the current branch to its upstream and leaves
// tracking untouched.
func TestPushDialogPushCurrentBranchToUpstream(t *testing.T) {
	repo, bareDir, head := aheadFixture(t, "pushdlg-push-upstream")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	clickPushConfirm(t, h)
	h.drain()

	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != head {
		t.Fatalf("bare refs/heads/main = %q, want %q", got, head)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("push to upstream failed: %v", err)
	}
}

// Publishing a branch without upstream sends it to the remote under its own
// name and sets upstream tracking.
func TestPushDialogPublishBranchSetsUpstream(t *testing.T) {
	repo := newGitRepo(t, "pushdlg-publish")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := addSecondBareRemote(t, repo, "origin")
	head := repo.CommitFile("b.txt", "two\n", "publish commit")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	clickPushConfirm(t, h)
	h.drain()

	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != head {
		t.Fatalf("bare refs/heads/main = %q, want %q", got, head)
	}
	if got := strings.TrimSpace(repo.Git("config", "branch.main.remote")); got != "origin" {
		t.Fatalf("branch.main.remote = %q, want origin", got)
	}
	if got := strings.TrimSpace(repo.Git("config", "branch.main.merge")); got != "refs/heads/main" {
		t.Fatalf("branch.main.merge = %q, want refs/heads/main", got)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("publish push failed: %v", err)
	}
}

// Switching the dialog's local branch pushes that branch without touching
// the checked-out one: HEAD stays put throughout.
func TestPushDialogPushNonCurrentBranchKeepsHEAD(t *testing.T) {
	repo, bareDir, _ := aheadFixture(t, "pushdlg-noncurrent")
	repo.Git("checkout", "-b", "feature")
	featureHead := repo.CommitFile("f.txt", "f\n", "feature commit")
	repo.Git("checkout", "main")
	mainHead := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPushDialog(t, h)
	selectPushBranch(t, h, "feature")
	st := pushDialogUI(h.app)
	if st.dest != "feature" || !st.setUpstream {
		t.Fatalf("feature defaults = (dest %q, upstream=%v), want (feature, true)", st.dest, st.setUpstream)
	}
	if err := h.ui.Click("Confirm Push"); err != nil {
		t.Fatalf("click Confirm Push: %v", err)
	}
	h.drain()

	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/feature")); got != featureHead {
		t.Fatalf("bare refs/heads/feature = %q, want %q", got, featureHead)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != mainHead {
		t.Fatalf("HEAD = %q, want %q: pushing a non-current branch must not move it", got, mainHead)
	}
	if got := strings.TrimSpace(repo.Git("config", "branch.feature.merge")); got != "refs/heads/feature" {
		t.Fatalf("branch.feature.merge = %q, want refs/heads/feature", got)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("push of non-current branch failed: %v", err)
	}
}

// A typed remote branch name publishes under that name, normalized of its
// whitespace and refs/heads/ spelling, and the tracking points at it.
func TestPushDialogCustomDestNormalized(t *testing.T) {
	repo := newGitRepo(t, "pushdlg-custom-dest")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := addSecondBareRemote(t, repo, "origin")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPushDialog(t, h)
	typePushDest(t, h, " refs/heads/renamed ")
	if err := h.ui.Click("Confirm Push"); err != nil {
		t.Fatalf("click Confirm Push: %v", err)
	}
	h.drain()

	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/renamed")); got != head {
		t.Fatalf("bare refs/heads/renamed = %q, want %q", got, head)
	}
	if bareHasRef(t, bareDir, "refs/heads/main") {
		t.Fatal("refs/heads/main appeared on the remote although the push targeted renamed")
	}
	if got := strings.TrimSpace(repo.Git("config", "branch.main.merge")); got != "refs/heads/renamed" {
		t.Fatalf("branch.main.merge = %q, want refs/heads/renamed", got)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("custom destination push failed: %v", err)
	}
}

// The remote branch input validates live: empty and illegal names disable
// the confirm button with a red explanation, a fixed name enables it again,
// and Enter in the input submits exactly when the confirm button would be
// enabled.
func TestPushDialogValidationAndEnterSubmit(t *testing.T) {
	repo := newGitRepo(t, "pushdlg-validation")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := addSecondBareRemote(t, repo, "origin")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPushDialog(t, h)

	// Empty name.
	typePushDest(t, h, "")
	if !h.ui.HasText("Enter a remote branch name") {
		t.Fatalf("empty-name problem missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Push"); !ok || !btn.Disabled {
		t.Fatalf("Confirm Push with empty dest = %+v, want disabled", btn)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if !pushDialogUI(h.app).open {
		t.Fatal("Enter with an invalid dest closed the dialog")
	}

	// Illegal ref characters.
	typePushDest(t, h, "bad..name")
	if !h.ui.HasText("Must not contain '..'.") {
		t.Fatalf("refname problem missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Push"); !ok || !btn.Disabled {
		t.Fatalf("Confirm Push with illegal dest = %+v, want disabled", btn)
	}

	// Leading dash, whitespace inside and trailing slash all refuse.
	for _, tc := range []struct{ name, problem string }{
		{"-mine", "name cannot begin with '-'"},
		{"two words", "name cannot contain whitespace"},
		{"trailing/", "name cannot end with '/'"},
	} {
		typePushDest(t, h, tc.name)
		if !h.ui.HasText(tc.problem) {
			t.Fatalf("problem %q missing for %q; texts=%q", tc.problem, tc.name, h.ui.Texts())
		}
		if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Push"); !ok || !btn.Disabled {
			t.Fatalf("Confirm Push with %q = %+v, want disabled", tc.name, btn)
		}
	}

	// A valid name submits through Enter.
	typePushDest(t, h, "entered")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Push"); !ok || btn.Disabled {
		t.Fatalf("Confirm Push with valid dest = %+v, want enabled", btn)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if pushDialogUI(h.app).open {
		t.Fatal("dialog stayed open after Enter submit")
	}
	h.drain()

	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/entered")); got != head {
		t.Fatalf("bare refs/heads/entered = %q, want %q", got, head)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter-submitted push failed: %v", err)
	}
}

// Push all tags sends the repository's tags together with the branch.
func TestPushDialogPushAllTags(t *testing.T) {
	repo, bareDir, head := aheadFixture(t, "pushdlg-tags")
	repo.Git("tag", "v1")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPushDialog(t, h)
	tickBox(t, h, "Push all tags")
	if err := h.ui.Click("Confirm Push"); err != nil {
		t.Fatalf("click Confirm Push: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("push with tags failed: %v", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/tags/v1")); got != head {
		t.Fatalf("bare refs/tags/v1 = %q, want %q", got, head)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != head {
		t.Fatalf("bare refs/heads/main = %q, want %q", got, head)
	}
}

// A push with nothing to send surfaces the up-to-date note as a toast, not
// an error.
func TestPushDialogUpToDateToast(t *testing.T) {
	repo := newGitRepo(t, "pushdlg-uptodate")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	clickPushConfirm(t, h)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("up-to-date push reported an error: %v", err)
	}
	if !h.ui.HasText(git.UpToDatePush) {
		t.Fatalf("up-to-date toast missing; texts=%q", h.ui.Texts())
	}
}

// Option+click on the toolbar Push button never opens the dialog: it
// publishes the current branch directly, without force and without tags.
func TestPushDialogOptionClickQuickPush(t *testing.T) {
	repo := newGitRepo(t, "pushdlg-quickpush")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := addSecondBareRemote(t, repo, "origin")
	head := repo.CommitFile("q.txt", "q\n", "quick commit")
	repo.Git("tag", "never")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	if err := h.ui.ClickWith(ui.Alt, "Push"); err != nil {
		t.Fatalf("Option+click Push: %v", err)
	}
	h.ui.Frame()
	if pushDialogUI(h.app).open {
		t.Fatal("Push dialog opened on Option+click")
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("quick push failed: %v", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != head {
		t.Fatalf("bare refs/heads/main = %q, want %q", got, head)
	}
	if bareHasRef(t, bareDir, "refs/tags/never") {
		t.Fatal("quick push sent tags")
	}
	if got := strings.TrimSpace(repo.Git("config", "branch.main.remote")); got != "origin" {
		t.Fatalf("branch.main.remote = %q, want origin", got)
	}
}

// --- close paths ---

// Cancel, Escape, the backdrop and a repository switch all close the dialog
// without running git push.
func TestPushDialogClosePathsNeverPush(t *testing.T) {
	setup := func(t *testing.T) (*gitRepoFixture, string, string) {
		return aheadFixture(t, "pushdlg-close")
	}
	verify := func(t *testing.T, h *testHarness, bareDir, before string) {
		t.Helper()
		h.ui.Frame()
		if pushDialogUI(h.app).open {
			t.Fatal("Push dialog survived the close path")
		}
		h.drain()
		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("closing the dialog produced an action error: %v", err)
		}
		if got := runGitCommand(t, bareDir, "for-each-ref"); got != before {
			t.Fatalf("bare remote changed by a push that must not run:\nbefore=%q\nafter=%q", before, got)
		}
	}

	t.Run("cancel button", func(t *testing.T) {
		repo, bareDir, _ := setup(t)
		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		openToolbarPushDialog(t, h)
		before := runGitCommand(t, bareDir, "for-each-ref")
		if err := h.ui.Click("Cancel"); err != nil {
			t.Fatalf("click Cancel: %v", err)
		}
		verify(t, h, bareDir, before)
	})

	t.Run("escape", func(t *testing.T) {
		repo, bareDir, _ := setup(t)
		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		openToolbarPushDialog(t, h)
		before := runGitCommand(t, bareDir, "for-each-ref")
		h.ui.Key(0, ui.KeyEscape)
		verify(t, h, bareDir, before)
	})

	t.Run("backdrop", func(t *testing.T) {
		repo, bareDir, _ := setup(t)
		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		openToolbarPushDialog(t, h)
		before := runGitCommand(t, bareDir, "for-each-ref")
		// The modal is centered in the 1200x800 test window; its top-left
		// corner is the dimmed backdrop outside the dialog.
		h.ui.ClickAt(20, 20)
		verify(t, h, bareDir, before)
	})

	t.Run("repository switch", func(t *testing.T) {
		repoA, bareDir, _ := setup(t)
		repoB := newGitRepo(t, "pushdlg-close-other")
		repoB.CommitFile("b.txt", "b\n", "init b")

		h := newTestApp(t, repoA.StoreRepo(), repoB.StoreRepo())
		closeSync(t, h)
		h.openRepo(repoA.StoreRepo())
		h.drain()
		openToolbarPushDialog(t, h)
		before := runGitCommand(t, bareDir, "for-each-ref")

		// The modal covers the switcher, so a switch with the dialog open is
		// programmatic — as opening a repository of the recent list is in the
		// product — and reaches the same generation change.
		h.openRepo(repoB.StoreRepo())
		h.ui.Frame()
		h.drain()
		verify(t, h, bareDir, before)
		if pushDialogUI(h.app).repoGen != h.app.session.Generation() {
			t.Fatal("dialog state not reset to the new repository generation")
		}
	})
}

// --- field linkage ---

// Switching the local branch recomputes the remote, the destination and the
// tracking default from that branch's upstream, forgets the typed
// destination, and retires the force push of the previous branch.
func TestPushDialogBranchSwitchLinkage(t *testing.T) {
	repo := newGitRepo(t, "pushdlg-link-branch")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	addSecondBareRemote(t, repo, "origin")
	repo.Git("checkout", "-b", "feature")
	repo.CommitFile("f.txt", "f\n", "feature commit")
	repo.Git("push", "-u", "origin", "feature:refs/heads/release")
	repo.Git("checkout", "main")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPushDialog(t, h)
	// main is untracked: tick force, agree, and type a custom destination.
	tickBox(t, h, "Force push (with lease)")
	tickBox(t, h, "Confirm force push with lease")
	typePushDest(t, h, "typed-custom")

	selectPushBranch(t, h, "feature")
	st := pushDialogUI(h.app)
	if st.remote != "origin" || st.dest != "release" {
		t.Fatalf("after branch switch = (%q, %q), want (origin, release) of feature's upstream", st.remote, st.dest)
	}
	if st.setUpstream {
		t.Fatal("Set upstream tracking ticked for feature's live upstream")
	}
	if st.force || st.forceAgree {
		t.Fatalf("force state survived the branch switch (force=%v, agree=%v)", st.force, st.forceAgree)
	}
	if st.destEdited {
		t.Fatal("typed destination still marked edited after the branch switch")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Force Push"); ok {
		t.Fatal("Confirm Force Push still shown after the branch switch reset force")
	}
}

// Switching the remote re-derives the destination for an unedited field —
// the upstream's branch name when the choice is the upstream's remote, the
// branch's own name otherwise — keeps both the tracking choice and a typed
// destination, and resets the force agreement.
func TestPushDialogRemoteSwitchLinkage(t *testing.T) {
	repo := newGitRepo(t, "pushdlg-link-remote")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")
	addSecondBareRemote(t, repo, "backup")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPushDialog(t, h)
	st := pushDialogUI(h.app)
	if st.remote != "origin" || st.dest != "main" || st.setUpstream {
		t.Fatalf("defaults = (%q, %q, upstream=%v), want (origin, main, false)", st.remote, st.dest, st.setUpstream)
	}
	tickBox(t, h, "Force push (with lease)")
	tickBox(t, h, "Confirm force push with lease")

	// Away from the upstream's remote: the unedited destination falls back
	// to the branch's own name, the tracking choice survives, and the force
	// agreement resets.
	selectPushRemote(t, h, "backup")
	st = pushDialogUI(h.app)
	if st.dest != "main" || st.setUpstream {
		t.Fatalf("after backup = (dest %q, upstream=%v), want (main, false)", st.dest, st.setUpstream)
	}
	if !st.force || st.forceAgree {
		t.Fatalf("force state after remote switch = (force=%v, agree=%v), want (true, false)", st.force, st.forceAgree)
	}

	// A typed destination is the user's and never follows the remote.
	typePushDest(t, h, "mine")
	selectPushRemote(t, h, "origin")
	if got := pushDialogUI(h.app).dest; got != "mine" {
		t.Fatalf("typed dest after remote switch = %q, want mine", got)
	}
	selectPushRemote(t, h, "backup")
	if got := pushDialogUI(h.app).dest; got != "mine" {
		t.Fatalf("typed dest after second remote switch = %q, want mine", got)
	}
}

// Editing the destination or flipping the force box resets the force
// agreement, so an agreement never outlives the target it was given for.
func TestPushDialogForceAgreementResets(t *testing.T) {
	repo, _, _ := aheadFixture(t, "pushdlg-agree-reset")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPushDialog(t, h)
	tickBox(t, h, "Force push (with lease)")
	tickBox(t, h, "Confirm force push with lease")
	if !pushDialogUI(h.app).forceAgree {
		t.Fatal("agreement not ticked")
	}

	// Typing in the destination resets it.
	focusPushDest(t, h)
	h.ui.Type("x")
	h.ui.Frame()
	if pushDialogUI(h.app).forceAgree {
		t.Fatal("agreement survived editing the destination")
	}

	// Unticking the force box clears it, and ticking again starts empty.
	tickBox(t, h, "Confirm force push with lease")
	tickBox(t, h, "Force push (with lease)")
	if pushDialogUI(h.app).forceAgree {
		t.Fatal("agreement survived unticking force")
	}
	tickBox(t, h, "Force push (with lease)")
	if pushDialogUI(h.app).forceAgree {
		t.Fatal("agreement already ticked after re-ticking force")
	}
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Confirm force push with lease"); !ok || box.Checked {
		t.Fatalf("agreement box after re-ticking force = %+v (found=%v), want unticked", box, ok)
	}
}

// --- force push safety ---

// A ticked force push unfolds the warning and gates its button on the
// explicit agreement: disabled before it, enabled after, and Enter in the
// destination input cannot bypass the gate.
func TestPushDialogForceGateAndWarning(t *testing.T) {
	repo, _, _ := aheadFixture(t, "pushdlg-force-gate")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPushDialog(t, h)
	tickBox(t, h, "Force push (with lease)")

	if !h.ui.HasText("Force pushing may overwrite commits on origin/main for everyone.") {
		t.Fatalf("force warning missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("--force-with-lease") {
		t.Fatalf("lease explanation missing; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Confirm force push with lease"); !ok {
		t.Fatal("agreement checkbox missing")
	}
	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Force Push")
	if !ok || !btn.Disabled {
		t.Fatalf("Confirm Force Push before agreement = %+v (found=%v), want disabled", btn, ok)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Push"); ok {
		t.Fatal("Confirm Push shown while force is ticked")
	}

	// Enter in the destination input submits nothing while unagreed. The
	// input is focused without disturbing the valid default destination.
	if err := h.ui.Click("Push Remote Branch"); err != nil {
		t.Fatalf("focus remote branch input: %v", err)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if !pushDialogUI(h.app).open {
		t.Fatal("Enter bypassed the force agreement gate")
	}

	tickBox(t, h, "Confirm force push with lease")
	btn, ok = h.ui.AccessByRole(ui.RoleButton, "Confirm Force Push")
	if !ok || btn.Disabled {
		t.Fatalf("Confirm Force Push after agreement = %+v (found=%v), want enabled", btn, ok)
	}
}

// Without force a diverged push is rejected, the remote stays byte-identical,
// and the refusal merges the porcelain reason into the visible error.
func TestPushDialogNonFastForwardRejected(t *testing.T) {
	repo, bareDir, _ := aheadFixture(t, "pushdlg-nonff")
	// Publish main, then diverge the remote with a peer commit.
	repo.Git("push", "origin", "main")
	peerHead := pushPeerCommit(t, bareDir, "main", "peer.txt", "peer\n", "peer commit")
	// The local branch keeps its own, older ahead commit.
	repo.CommitFile("local.txt", "l\n", "local diverging commit")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	clickPushConfirm(t, h)
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("non-fast-forward push produced no error")
	}
	// Git's porcelain reason for a remote that moved ahead is (fetch first);
	// stderr only hints at it, so the merged summary proves the porcelain
	// extraction works.
	if !strings.Contains(err.Error(), "(fetch first)") {
		t.Fatalf("error %q missing the merged porcelain rejection reason", err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error after the rejected push; texts=%q", h.ui.Texts())
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != peerHead {
		t.Fatalf("bare refs/heads/main = %q, want the peer's %q untouched", got, peerHead)
	}
}

// A confirmed force push overwrites a diverged remote branch while the lease
// is fresh.
func TestPushDialogForcePushOverwritesWithFreshLease(t *testing.T) {
	repo := newGitRepo(t, "pushdlg-force-success")
	head1 := repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	// Amend the initial commit: the branch diverges from the pushed head but
	// origin/main still reads as the pushed one, so the lease is fresh.
	repo.Git("commit", "--amend", "-m", "amended initial commit")
	head2 := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	if head2 == head1 {
		t.Fatal("amend did not change the commit hash")
	}

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	// Without force the push is rejected.
	clickPushConfirm(t, h)
	h.drain()
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("diverged push without force produced no error")
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != head1 {
		t.Fatalf("bare refs/heads/main = %q, want the original %q untouched", got, head1)
	}
	if err := h.ui.Click("Dismiss Error"); err != nil {
		t.Fatalf("click Dismiss Error: %v", err)
	}
	h.ui.Frame()

	// With force and the agreement the push overwrites.
	openToolbarPushDialog(t, h)
	tickBox(t, h, "Force push (with lease)")
	tickBox(t, h, "Confirm force push with lease")
	if err := h.ui.Click("Confirm Force Push"); err != nil {
		t.Fatalf("click Confirm Force Push: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("confirmed force push failed: %v", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != head2 {
		t.Fatalf("bare refs/heads/main = %q, want the force-pushed %q", got, head2)
	}
}

// A remote updated since the last fetch makes the lease stale: the force
// push is refused atomically, the remote keeps the concurrent commit, and
// the error leads with the stale-info reason in the session and the toolbar.
func TestPushDialogStaleLeaseError(t *testing.T) {
	repo, bareDir, _ := aheadFixture(t, "pushdlg-stale")
	// Publish the ahead commit so local and remote agree, then move the
	// remote behind the repository's back: the lease of the next force push
	// no longer matches.
	repo.Git("push", "origin", "main")
	peerHead := pushPeerCommit(t, bareDir, "main", "peer.txt", "peer\n", "peer moved the remote")
	// Diverge locally as well so the overwrite would be real.
	repo.CommitFile("local.txt", "l\n", "local diverging commit")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPushDialog(t, h)
	tickBox(t, h, "Force push (with lease)")
	tickBox(t, h, "Confirm force push with lease")
	if err := h.ui.Click("Confirm Force Push"); err != nil {
		t.Fatalf("click Confirm Force Push: %v", err)
	}
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("stale force push produced no error")
	}
	if !strings.Contains(err.Error(), "stale info") {
		t.Fatalf("stale lease error %q missing the stale info reason", err)
	}
	if !strings.HasPrefix(err.Error(), "Force push rejected (stale info)") {
		t.Fatalf("stale lease error = %q, want it to lead with the reason", err)
	}
	if !h.ui.HasText("stale info") {
		t.Fatalf("toolbar error strip missing the stale info reason; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error after the stale lease refusal; texts=%q", h.ui.Texts())
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != peerHead {
		t.Fatalf("bare refs/heads/main = %q, want the concurrent %q untouched", got, peerHead)
	}
}

// --- busy and pre-flight guards ---

// Reaching the dialog while a write is in flight or without a repository
// changes nothing: no dialog, no error rewrite, no extra worker.
func TestPushDialogBusyAndNoRepoGuard(t *testing.T) {
	repo, _, _ := aheadFixture(t, "pushdlg-busy")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	h.queue.PauseWorkers()
	clickFetch(t, h)
	fetchWorker := h.queue.WaitWorker(t)

	st := syncStateFor(h.app)
	h.app.openPushDialog()
	if pushDialogUI(h.app).open {
		t.Fatal("Push dialog opened while the repository was busy")
	}
	if st.syncErr != "" {
		t.Fatalf("openPushDialog while busy wrote syncErr %q", st.syncErr)
	}
	if got := gatedLen(h.queue); got != 0 {
		t.Fatalf("busy-time openPushDialog spawned %d extra worker(s), want 0", got)
	}

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(fetchWorker)
	h.drain()

	// Without a repository the direct call is inert as well.
	h2 := newTestApp(t)
	closeSync(t, h2)
	h2.app.openPushDialog()
	if pushDialogUI(h2.app).open {
		t.Fatal("Push dialog opened without a repository")
	}
}

// --- worker guards ---

// The worker re-reads the repository before pushing: a repository that
// moved, renamed or lost what the dialog captured is never pushed, and every
// refusal surfaces in the toolbar.
func TestPushDialogWorkerGuards(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T) (*gitRepoFixture, string)
		mutate  func(t *testing.T, repo *gitRepoFixture)
		wantErr string
	}{
		{
			name: "HEAD moved",
			setup: func(t *testing.T) (*gitRepoFixture, string) {
				repo, bare, _ := aheadFixture(t, "pushdlg-guard")
				return repo, bare
			},
			mutate: func(t *testing.T, repo *gitRepoFixture) {
				repo.CommitFile("moved.txt", "moved\n", "concurrent commit")
			},
			wantErr: "changed after the dialog opened",
		},
		{
			name: "branch switched",
			setup: func(t *testing.T) (*gitRepoFixture, string) {
				repo, bare, _ := aheadFixture(t, "pushdlg-guard")
				repo.Git("branch", "other")
				return repo, bare
			},
			mutate: func(t *testing.T, repo *gitRepoFixture) {
				repo.Git("checkout", "other")
			},
			wantErr: "changed after the dialog opened",
		},
		{
			name: "HEAD detached",
			setup: func(t *testing.T) (*gitRepoFixture, string) {
				repo, bare, _ := aheadFixture(t, "pushdlg-guard")
				return repo, bare
			},
			mutate: func(t *testing.T, repo *gitRepoFixture) {
				repo.Git("checkout", strings.TrimSpace(repo.Git("rev-parse", "HEAD")))
			},
			wantErr: "changed after the dialog opened",
		},
		{
			name: "orphan checkout",
			setup: func(t *testing.T) (*gitRepoFixture, string) {
				repo, bare, _ := aheadFixture(t, "pushdlg-guard")
				return repo, bare
			},
			mutate: func(t *testing.T, repo *gitRepoFixture) {
				repo.Git("checkout", "-q", "--orphan", "fresh")
			},
			wantErr: "changed after the dialog opened",
		},
		{
			name: "remote removed",
			setup: func(t *testing.T) (*gitRepoFixture, string) {
				repo, bare, _ := aheadFixture(t, "pushdlg-guard")
				return repo, bare
			},
			mutate: func(t *testing.T, repo *gitRepoFixture) {
				repo.Git("remote", "remove", "origin")
			},
			wantErr: "no longer exists",
		},
		{
			name: "pushed branch moved",
			setup: func(t *testing.T) (*gitRepoFixture, string) {
				repo, bare, _ := aheadFixture(t, "pushdlg-guard")
				repo.Git("checkout", "-b", "feature")
				repo.CommitFile("f.txt", "f\n", "feature commit")
				repo.Git("checkout", "main")
				return repo, bare
			},
			mutate: func(t *testing.T, repo *gitRepoFixture) {
				repo.Git("update-ref", "refs/heads/feature", strings.TrimSpace(repo.Git("rev-parse", "main")))
			},
			wantErr: "the branch moved after the dialog opened",
		},
		{
			name: "pushed branch deleted",
			setup: func(t *testing.T) (*gitRepoFixture, string) {
				repo, bare, _ := aheadFixture(t, "pushdlg-guard")
				repo.Git("checkout", "-b", "feature")
				repo.CommitFile("f.txt", "f\n", "feature commit")
				repo.Git("checkout", "main")
				return repo, bare
			},
			mutate: func(t *testing.T, repo *gitRepoFixture) {
				repo.Git("branch", "-D", "feature")
			},
			wantErr: "the branch no longer exists",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, bareDir := tc.setup(t)
			before := runGitCommand(t, bareDir, "for-each-ref")

			h := newTestApp(t)
			openSyncRepo(t, h, repo.StoreRepo())

			openToolbarPushDialog(t, h)
			if tc.name == "pushed branch moved" || tc.name == "pushed branch deleted" {
				selectPushBranch(t, h, "feature")
			}
			tc.mutate(t, repo)

			if err := h.ui.Click("Confirm Push"); err != nil {
				t.Fatalf("click Confirm Push: %v\ntexts=%q", err, h.ui.Texts())
			}
			h.drain()

			err := h.app.session.ActionErr()
			if err == nil {
				t.Fatalf("guard produced no error, want one containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("guard error = %q, want it to contain %q", err, tc.wantErr)
			}
			if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
				t.Fatalf("no Repository Error after the guard refusal; texts=%q", h.ui.Texts())
			}
			if got := runGitCommand(t, bareDir, "for-each-ref"); got != before {
				t.Fatalf("bare remote changed by a push the guard must stop:\nbefore=%q\nafter=%q", before, got)
			}
		})
	}
}

// When the worker cannot read the repository state or its references at all,
// the push never runs and the read failure surfaces.
func TestPushDialogGuardReadFailures(t *testing.T) {
	t.Run("status fails", func(t *testing.T) {
		repo, bareDir, _ := aheadFixture(t, "pushdlg-guard-status")
		before := runGitCommand(t, bareDir, "for-each-ref")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		openToolbarPushDialog(t, h)
		installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))

		if err := h.ui.Click("Confirm Push"); err != nil {
			t.Fatalf("click Confirm Push: %v", err)
		}
		h.drain()

		err := h.app.session.ActionErr()
		if err == nil || !strings.Contains(err.Error(), "reading the repository state failed") {
			t.Fatalf("error = %v, want the status read failure", err)
		}
		if got := runGitCommand(t, bareDir, "for-each-ref"); got != before {
			t.Fatalf("bare remote changed by a push the guard must stop:\nbefore=%q\nafter=%q", before, got)
		}
	})

	t.Run("refs fails", func(t *testing.T) {
		repo, bareDir, _ := aheadFixture(t, "pushdlg-guard-refs")
		before := runGitCommand(t, bareDir, "for-each-ref")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		openToolbarPushDialog(t, h)
		installFailFirstExecutor(t, h, isRefsCommand, errors.New("injected refs failure"))

		if err := h.ui.Click("Confirm Push"); err != nil {
			t.Fatalf("click Confirm Push: %v", err)
		}
		h.drain()

		err := h.app.session.ActionErr()
		if err == nil || !strings.Contains(err.Error(), "reading repository references failed") {
			t.Fatalf("error = %v, want the refs read failure", err)
		}
		if got := runGitCommand(t, bareDir, "for-each-ref"); got != before {
			t.Fatalf("bare remote changed by a push the guard must stop:\nbefore=%q\nafter=%q", before, got)
		}
	})
}

// Credentials embedded in the remote URL never reach the session error or
// the toolbar text of a failed push.
func TestPushDialogRedactsCredentials(t *testing.T) {
	repo := newGitRepo(t, "pushdlg-redact")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.Git("remote", "add", "origin", "https://pushuser:pushsecretpw@127.0.0.1:9/repo.git")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	clickPushConfirm(t, h)
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("expected an action error for the unreachable remote")
	}
	if strings.Contains(err.Error(), "pushsecretpw") {
		t.Fatalf("session error leaks the URL credential: %q", err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error after the failed push; texts=%q", h.ui.Texts())
	}
	for _, text := range h.ui.Texts() {
		if strings.Contains(text, "pushsecretpw") {
			t.Fatalf("toolbar text leaks the URL credential: %q", text)
		}
	}
}

// --- loopback SSH ---

// Over loopback SSH the dialog publishes the server repository's untracked
// branch to its bare remote, sets upstream, and pushes the tags with it.
func TestPushDialogPublishWithTagsLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repoDir, "tag", "sshv1", "HEAD")
	head := srv.RunGit(t, repoDir, "rev-parse", "HEAD")

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	openToolbarPushDialog(t, h)
	tickBox(t, h, "Push all tags")
	if err := h.ui.Click("Confirm Push"); err != nil {
		t.Fatalf("click Confirm Push: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH publish push failed: %v", err)
	}
	if got := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/main"); got != head {
		t.Fatalf("server bare refs/heads/main = %q, want %q", got, head)
	}
	if got := srv.RunGit(t, bareDir, "rev-parse", "refs/tags/sshv1"); got != head {
		t.Fatalf("server bare refs/tags/sshv1 = %q, want %q", got, head)
	}
	if got := srv.RunGit(t, repoDir, "config", "branch.main.merge"); got != "refs/heads/main" {
		t.Fatalf("server branch.main.merge = %q, want refs/heads/main", got)
	}
}

// Over loopback SSH a remote moved since the server's last fetch refuses the
// dialog's force push atomically, with the stale-info reason in the error.
func TestPushDialogStaleLeaseLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repoDir, "push", "-q", "-u", "origin", "main")

	// A peer moves the remote behind the server repository's back, and the
	// server diverges locally so the overwrite would be real.
	peerDir := srv.TempDir(t, "peer")
	srv.RunShell(t, "set -eu\nexport GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1\n"+
		"git clone -q "+shellQ(bareDir)+" "+shellQ(peerDir)+"\n"+
		"cd "+shellQ(peerDir)+"\n"+
		"git config user.name Peer\ngit config user.email peer@branchline.invalid\n"+
		"printf 'peer\\n' > peer.txt\ngit add -- peer.txt\ngit commit -q -m 'peer moved the remote'\n"+
		"git push -q origin main\n")
	peerHead := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/main")
	srv.WriteFile(t, repoDir, "local.txt", []byte("local\n"))
	srv.RunGit(t, repoDir, "add", "--", "local.txt")
	srv.RunGit(t, repoDir, "commit", "-q", "-m", "server diverging commit")

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	openToolbarPushDialog(t, h)
	tickBox(t, h, "Force push (with lease)")
	tickBox(t, h, "Confirm force push with lease")
	if err := h.ui.Click("Confirm Force Push"); err != nil {
		t.Fatalf("click Confirm Force Push: %v", err)
	}
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("SSH stale force push produced no error")
	}
	if !strings.Contains(err.Error(), "stale info") {
		t.Fatalf("SSH stale lease error %q missing the stale info reason", err)
	}
	if got := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/main"); got != peerHead {
		t.Fatalf("server bare refs/heads/main = %q, want the concurrent %q untouched", got, peerHead)
	}
}

// Behind a password gate the dialog's push asks through the real askpass
// modal: the answered password lets the push through, and the dialog itself
// is already closed when the question renders.
func TestPushDialogAskPassAnswerLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repoDir, "push", "-q", "-u", "origin", "main")
	// A commit ahead of the remote, so the push has something to send.
	srv.WriteFile(t, repoDir, "ahead.txt", []byte("ahead\n"))
	srv.RunGit(t, repoDir, "add", "--", "ahead.txt")
	srv.RunGit(t, repoDir, "commit", "-q", "-m", "ahead commit")
	head := srv.RunGit(t, repoDir, "rev-parse", "HEAD")

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	// Gate every new connection behind password authentication: from here on
	// only the askpass-enabled path reaches the server.
	const password = "hunter2t0ps3cret"
	gateDir := passwordGateWrapper(t, srv.SSHProgram(), password)
	t.Setenv("PATH", gateDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// The worker's guard reads — status and the several commands of refs —
	// and the push itself each cross the gate as a separate ssh invocation,
	// so the question comes back a handful of times. A real server asks only
	// once here; the gate stands in front of every invocation, connection
	// reuse included. The loop answers every question until the worker's
	// completion arrives, consuming the wake-up callbacks one at a time.
	answerPasswords := func() {
		t.Helper()
		for range 16 {
			h.queue.RunNext(t)
			h.ui.Frame()
			if _, ok := h.ui.Find("Cancel"); !ok {
				if !h.app.session.Busy() {
					return
				}
				continue
			}
			if pushDialogUI(h.app).open {
				t.Fatal("the Push dialog is still open under the askpass question")
			}
			h.ui.Type(password)
			h.ui.Frame()
			if err := h.ui.Click("OK"); err != nil {
				t.Fatalf("click OK in password dialog: %v", err)
			}
			h.ui.Frame()
		}
		t.Fatal("password questions did not stop after 16 rounds")
	}

	openToolbarPushDialog(t, h)
	if err := h.ui.Click("Confirm Push"); err != nil {
		t.Fatalf("click Confirm Push: %v", err)
	}
	h.ui.Frame()
	if pushDialogUI(h.app).open {
		t.Fatal("the Push dialog stayed open after the confirm")
	}

	answerPasswords()
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("push behind the password gate failed after correct passwords: %v", err)
	}
	if got := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/main"); got != head {
		t.Fatalf("server bare refs/heads/main = %q, want %q", got, head)
	}
}

// Cancelling the askpass question — by the Cancel button or by Escape —
// interrupts the push without hanging: the worker ends in an error, the busy
// state clears, and the remote stays untouched.
func TestPushDialogAskPassCancelLoopbackSSH(t *testing.T) {
	run := func(t *testing.T, cancel func(t *testing.T, h *testHarness)) {
		t.Helper()
		repo, srv := loopbackSSHRepo(t)
		repoDir := repo.Path
		bareDir := srv.InitBareRepo(t, "origin")
		srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
		srv.RunGit(t, repoDir, "push", "-q", "-u", "origin", "main")
		srv.WriteFile(t, repoDir, "ahead.txt", []byte("ahead\n"))
		srv.RunGit(t, repoDir, "add", "--", "ahead.txt")
		srv.RunGit(t, repoDir, "commit", "-q", "-m", "ahead commit")

		h := newTestApp(t, repo)
		closeSync(t, h)
		selectRepo(t, h, repo)
		if err := h.app.session.LoadErr(); err != nil {
			t.Fatalf("SSH snapshot load failed: %v", err)
		}
		before := srv.RunGit(t, bareDir, "for-each-ref")

		const password = "hunter2t0ps3cret"
		gateDir := passwordGateWrapper(t, srv.SSHProgram(), password)
		t.Setenv("PATH", gateDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		openToolbarPushDialog(t, h)
		if err := h.ui.Click("Confirm Push"); err != nil {
			t.Fatalf("click Confirm Push: %v", err)
		}
		h.ui.Frame()
		cancel(t, h)
		h.drain()

		if h.app.session.Busy() {
			t.Fatal("session stayed busy after cancelling the password dialog")
		}
		if err := h.app.session.ActionErr(); err == nil {
			t.Fatal("expected an action error after the cancelled push")
		}
		if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
			t.Fatalf("no Repository Error after the cancelled push; texts=%q", h.ui.Texts())
		}
		if got := srv.RunGit(t, bareDir, "for-each-ref"); got != before {
			t.Fatalf("server bare remote changed by the cancelled push:\nbefore=%q\nafter=%q", before, got)
		}
	}

	t.Run("cancel button", func(t *testing.T) {
		run(t, func(t *testing.T, h *testHarness) {
			waitAskPass(t, h)
			if err := h.ui.Click("Cancel"); err != nil {
				t.Fatalf("click Cancel in password dialog: %v", err)
			}
			h.ui.Frame()
		})
	})

	t.Run("escape", func(t *testing.T) {
		run(t, func(t *testing.T, h *testHarness) {
			waitAskPass(t, h)
			h.ui.Key(0, ui.KeyEscape)
			h.ui.Frame()
		})
	})
}

// Server-side verification in the loopback tests goes through srv.RunGit
// directly: the probe executor never crosses the password gate.
