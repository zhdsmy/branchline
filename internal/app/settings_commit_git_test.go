package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// openCommitGitSettingsTester drives the Settings window from its real
// entry — ⌘, in the main window — and renders its view headlessly, as the
// window hook would in a real window. The tester is taller than the
// window's default, as it is after a user drags it taller: the Git page's
// rows — error and retry ones included — stay inside it instead of needing
// a scroll the click coordinates cannot follow.
func openCommitGitSettingsTester(t *testing.T, h *testHarness) *ui.Tester {
	t.Helper()
	installSettingsHooks(h)
	h.ui.Key(ui.Cmd, ui.KeyComma)
	h.ui.Frame()
	tt := ui.NewTester(h.app.settingsView, 560, 700)
	tt.Frame()
	return tt
}

// drainSettings runs every queued worker completion and repaints both the
// main window and the Settings window testers, so assertions after it see
// the state the asynchronous loads and saves published.
func drainSettings(t *testing.T, h *testHarness, tt *ui.Tester) {
	t.Helper()
	h.drain()
	tt.Frame()
}

// focusSettingsField clicks the text field named label inside the Settings
// window tester, moving the keyboard focus into it as a user does.
func focusSettingsField(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	info, ok := tt.AccessByRole(ui.RoleTextField, label)
	if !ok {
		t.Fatalf("no text field labeled %q; texts=%q", label, tt.Texts())
	}
	tt.ClickAt(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
	tt.Frame()
}

// typeInSettingsField replaces the content of the text field named label
// with text, as a user selecting everything and typing over it does.
func typeInSettingsField(t *testing.T, tt *ui.Tester, label, text string) {
	t.Helper()
	focusSettingsField(t, tt, label)
	tt.Command("selectAll")
	tt.Type(text)
	tt.Frame()
}

// chooseGitInstance picks an option of the Git Instance pop-up through its
// real popup: a click on the trigger opens the options and a click on the
// option chooses it.
func chooseGitInstance(t *testing.T, tt *ui.Tester, option string) {
	t.Helper()
	if err := tt.Click("Git Instance"); err != nil {
		t.Fatalf("open Git Instance pop-up: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click(option); err != nil {
		t.Fatalf("choose %q in Git Instance pop-up: %v; texts=%q", option, err, tt.Texts())
	}
	tt.Frame()
}

// chooseEnvPath picks an option of the ENV PATH pop-up through its popup.
func chooseEnvPath(t *testing.T, tt *ui.Tester, option string) {
	t.Helper()
	if err := tt.Click("ENV PATH"); err != nil {
		t.Fatalf("open ENV PATH pop-up: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click(option); err != nil {
		t.Fatalf("choose %q in ENV PATH pop-up: %v; texts=%q", option, err, tt.Texts())
	}
	tt.Frame()
}

// runGitInheritEnv runs git with the test process environment — including
// the isolated global configuration — unlike the fixture helpers, which
// null the global configuration for determinism. Verifying that a commit
// carries the global identity requires the real lookup chain.
func runGitInheritEnv(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func assertNoCommitWarning(t *testing.T, h *testHarness) {
	t.Helper()
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Commit Message Warning"); ok {
		t.Fatalf("the commit box renders a warning node it should not; texts=%q", h.ui.Texts())
	}
	for _, text := range h.ui.Texts() {
		if strings.Contains(text, "past the") {
			t.Fatalf("warning text %q rendered outside a warning state", text)
		}
	}
}

// The Commit page's subject limits and page guide drive the warning of the
// real commit box end to end: an empty draft and a draft inside the limits
// render no warning node at all, a subject past the soft or the hard limit
// and a description line past the guide warn as typed, and saving explicit
// zeros turns the subject warnings off again.
func TestSettingsCommitGitCommitPageRulesDriveCommitBox(t *testing.T) {
	repo := newGitRepo(t, "scg-rules")
	repo.CommitFile("base.txt", "base\n", "initial commit")
	repo.WriteFile("base.txt", "base\nmodified\n")

	h := openChangesHarness(t, repo)

	// The default box renders no warning node: neither for the empty draft
	// nor for a message inside the default limits.
	assertNoCommitWarning(t, h)
	typeCommitMessage(t, h, "a short subject")
	h.ui.Frame()
	assertNoCommitWarning(t, h)

	tt := openCommitGitSettingsTester(t, h)
	clickSettingsTab(t, tt, "Commit")

	// The drafts prime from the normalized defaults.
	if got := commitGitState(h.app).subjectLow; got != "50" {
		t.Fatalf("primed soft limit = %q, want 50", got)
	}

	typeInSettingsField(t, tt, "Subject soft limit", "20")
	typeInSettingsField(t, tt, "Subject hard limit", "30")
	typeInSettingsField(t, tt, "Page guide column", "35")
	if err := tt.Click("Save Commit Settings"); err != nil {
		t.Fatalf("click Save Commit Settings: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()

	prefs := h.store.Settings()
	if prefs.SubjectLow != 20 || prefs.SubjectHigh != 30 || prefs.PageGuide != 35 || prefs.NoPageGuide {
		t.Fatalf("saved rules = soft %d hard %d guide %d off %v, want 20/30/35/on",
			prefs.SubjectLow, prefs.SubjectHigh, prefs.PageGuide, prefs.NoPageGuide)
	}

	// A 25-character subject passes the soft limit of 20.
	typeCommitMessage(t, h, "1234567890123456789012345")
	h.ui.Frame()
	if !h.ui.HasText("Subject is 25 characters, past the soft limit of 20.") {
		t.Fatalf("no soft-limit warning in commit box; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("past the limit of") {
		t.Fatalf("soft warning reads as the severe hard-limit one; texts=%q", h.ui.Texts())
	}

	// A 35-character subject passes the hard limit of 30.
	typeCommitMessage(t, h, "12345678901234567890123456789012345")
	h.ui.Frame()
	if !h.ui.HasText("Subject is 35 characters, past the limit of 30.") {
		t.Fatalf("no hard-limit warning in commit box; texts=%q", h.ui.Texts())
	}

	// A description line past the 35-column guide warns even with a short
	// subject.
	typeCommitMessage(t, h, "short\n\n1234567890123456789012345678901234567890")
	h.ui.Frame()
	if !h.ui.HasText("A description line is past the 35 character guide.") {
		t.Fatalf("no page-guide warning in commit box; texts=%q", h.ui.Texts())
	}

	// Unchecking the guide turns its warning off while the subject limits
	// keep working.
	if err := tt.Click("Page guide at column"); err != nil {
		t.Fatalf("uncheck Page guide at column: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click("Save Commit Settings"); err != nil {
		t.Fatalf("save with the guide off: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if got := h.store.Settings().NoPageGuide; !got {
		t.Fatal("the guide's off switch did not persist")
	}
	h.ui.Frame()
	if h.ui.HasText("character guide") {
		t.Fatalf("guide warning survived its off switch; texts=%q", h.ui.Texts())
	}

	// Saving explicit zeros turns the subject warnings off: the 35-character
	// subject warns about nothing anymore.
	typeInSettingsField(t, tt, "Subject soft limit", "0")
	typeInSettingsField(t, tt, "Subject hard limit", "0")
	if err := tt.Click("Save Commit Settings"); err != nil {
		t.Fatalf("save zeros: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	prefs = h.store.Settings()
	if prefs.SubjectLow != 0 || prefs.SubjectHigh != 0 || !prefs.SubjectLowSaved || !prefs.SubjectHighSaved {
		t.Fatalf("saved zeros = soft %d (saved %v) hard %d (saved %v), want 0/true 0/true",
			prefs.SubjectLow, prefs.SubjectLowSaved, prefs.SubjectHigh, prefs.SubjectHighSaved)
	}
	typeCommitMessage(t, h, "12345678901234567890123456789012345")
	h.ui.Frame()
	assertNoCommitWarning(t, h)
}

// The Signed-off-by choice of the Commit page signs real commits off through
// the commit box, and amending a commit whose message already carries the
// trailer never duplicates it.
func TestSettingsCommitGitSignoffCommitsAndAmendOnce(t *testing.T) {
	repo := newGitRepo(t, "scg-signoff")
	repo.CommitFile("base.txt", "base\n", "initial commit")
	repo.WriteFile("base.txt", "base\nmodified\n")

	h := openChangesHarness(t, repo)
	tt := openCommitGitSettingsTester(t, h)
	clickSettingsTab(t, tt, "Commit")

	if err := tt.Click("Add Signed-off-by to commits"); err != nil {
		t.Fatalf("check Add Signed-off-by to commits: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click("Save Commit Settings"); err != nil {
		t.Fatalf("save sign-off: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if got := h.store.Settings().SignedOffBy; !got {
		t.Fatal("the Signed-off-by choice did not persist")
	}

	// A real commit through the box carries exactly one trailer.
	mustClick(t, h, "Stage base.txt")
	h.drain()
	typeCommitMessage(t, h, "signed work")
	mustClick(t, h, "Commit")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	body := repo.Git("log", "-1", "--format=%B")
	if n := strings.Count(body, "Signed-off-by:"); n != 1 {
		t.Fatalf("committed message has %d Signed-off-by lines, want 1:\n%s", n, body)
	}

	// Amending with the prefilled message — trailer included — adds no
	// second one.
	repo.WriteFile("base.txt", "base\nmodified\nmore\n")
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Stage base.txt")
	h.drain()
	selectAmend(t, h)
	h.drain()
	if msg := h.app.changesView.message; !strings.Contains(msg, "Signed-off-by:") {
		t.Fatalf("amend prefill lost the trailer: %q", msg)
	}
	mustClick(t, h, "Commit")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("amend failed: %v", err)
	}
	body = repo.Git("log", "-1", "--format=%B")
	if n := strings.Count(body, "Signed-off-by:"); n != 1 {
		t.Fatalf("amended message has %d Signed-off-by lines, want 1:\n%s", n, body)
	}

	// Turning the choice off stops signing: the next commit has no trailer.
	if err := tt.Click("Add Signed-off-by to commits"); err != nil {
		t.Fatalf("uncheck sign-off: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Save Commit Settings"); err != nil {
		t.Fatalf("save sign-off off: %v", err)
	}
	tt.Frame()
	repo.WriteFile("unsigned.txt", "unsigned\n")
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Stage unsigned.txt")
	h.drain()
	typeCommitMessage(t, h, "unsigned work")
	mustClick(t, h, "Commit")
	h.drain()
	body = repo.Git("log", "-1", "--format=%B")
	if strings.Contains(body, "Signed-off-by:") {
		t.Fatalf("commit signed off with the choice off:\n%s", body)
	}
}

// Rejected inputs and a store that cannot persist show the raw error on the
// Commit page and change nothing, and the drafts survive for fixing.
func TestSettingsCommitGitSaveFailuresKeepSettings(t *testing.T) {
	h := newTestApp(t)
	tt := openCommitGitSettingsTester(t, h)
	clickSettingsTab(t, tt, "Commit")
	before := h.store.Settings()

	// A negative limit is rejected.
	typeInSettingsField(t, tt, "Subject soft limit", "-5")
	if err := tt.Click("Save Commit Settings"); err != nil {
		t.Fatalf("save negative: %v", err)
	}
	tt.Frame()
	if !tt.HasText("the soft subject limit must not be negative") {
		t.Fatalf("no negative-limit error on the page; texts=%q", tt.Texts())
	}
	if got := h.store.Settings(); got.SubjectLow != before.SubjectLow {
		t.Fatalf("negative input changed the soft limit to %d", got.SubjectLow)
	}

	// A soft limit past the hard one is rejected.
	typeInSettingsField(t, tt, "Subject soft limit", "40")
	typeInSettingsField(t, tt, "Subject hard limit", "30")
	if err := tt.Click("Save Commit Settings"); err != nil {
		t.Fatalf("save inverted: %v", err)
	}
	tt.Frame()
	if !tt.HasText("the soft subject limit must not pass the hard one") {
		t.Fatalf("no inverted-limits error on the page; texts=%q", tt.Texts())
	}
	if got := h.store.Settings(); got.SubjectLow != before.SubjectLow || got.SubjectHigh != before.SubjectHigh {
		t.Fatalf("inverted input changed the limits to %d/%d", got.SubjectLow, got.SubjectHigh)
	}

	// Text that is not a number is rejected.
	typeInSettingsField(t, tt, "Subject soft limit", "abc")
	if err := tt.Click("Save Commit Settings"); err != nil {
		t.Fatalf("save non-numeric: %v", err)
	}
	tt.Frame()
	if !tt.HasText("not a whole number") {
		t.Fatalf("no parse error on the page; texts=%q", tt.Texts())
	}

	// The drafts survive the rejections for fixing, and a valid save works.
	if got := commitGitState(h.app).subjectLow; got != "abc" {
		t.Fatalf("the rejected draft was clobbered: %q", got)
	}
	typeInSettingsField(t, tt, "Subject soft limit", "42")
	typeInSettingsField(t, tt, "Subject hard limit", "60")
	if err := tt.Click("Save Commit Settings"); err != nil {
		t.Fatalf("save valid: %v", err)
	}
	tt.Frame()
	if got := h.store.Settings(); got.SubjectLow != 42 || got.SubjectHigh != 60 {
		t.Fatalf("valid save = %d/%d, want 42/60", got.SubjectLow, got.SubjectHigh)
	}

	// A store that cannot persist reports the raw error and keeps the
	// settings; restored writability saves again.
	storeDir := filepath.Dir(h.store.Path())
	if err := os.Chmod(storeDir, 0o500); err != nil {
		t.Fatalf("chmod store dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(storeDir, 0o700) })
	probe := h.store.Settings()
	probe.SubjectLow = 43
	probeErr := h.store.SetSettings(probe)
	if probeErr == nil {
		t.Fatal("SetSettings succeeded into a read-only directory; the failure assertions below would be vacuous")
	}
	typeInSettingsField(t, tt, "Subject soft limit", "43")
	if err := tt.Click("Save Commit Settings"); err != nil {
		t.Fatalf("save into read-only store: %v", err)
	}
	tt.Frame()
	if !tt.HasText(probeErr.Error()) {
		t.Fatalf("the page does not show the raw save error %q; texts=%q", probeErr, tt.Texts())
	}
	if got := h.store.Settings().SubjectLow; got != 42 {
		t.Fatalf("the failed save changed the soft limit to %d", got)
	}
	if err := os.Chmod(storeDir, 0o700); err != nil {
		t.Fatalf("restore store dir: %v", err)
	}
	if err := tt.Click("Save Commit Settings"); err != nil {
		t.Fatalf("retry save: %v", err)
	}
	tt.Frame()
	if got := h.store.Settings().SubjectLow; got != 43 {
		t.Fatalf("the retried save = %d, want 43", got)
	}
	if tt.HasText(probeErr.Error()) {
		t.Fatal("the error text survived a successful save")
	}
}

// The Git page writes the global author identity through its real save
// flow, and a repository without its own user.name/user.email commits with
// it at once — through the product's commit box.
func TestSettingsCommitGitGlobalIdentityRoundtrip(t *testing.T) {
	cfg := isolatedPrefsGitConfig(t)
	h := newTestApp(t)
	tt := openCommitGitSettingsTester(t, h)
	clickSettingsTab(t, tt, "Git")
	drainSettings(t, h, tt)

	if tt.HasText("Loading global Git identity…") {
		t.Fatal("the identity load did not finish")
	}
	typeInSettingsField(t, tt, "Git user name", "Settings Author")
	typeInSettingsField(t, tt, "Git user email", "settings.author@example.com")
	if err := tt.Click("Save Git Settings"); err != nil {
		t.Fatalf("click Save Git Settings: %v; texts=%q", err, tt.Texts())
	}
	drainSettings(t, h, tt)

	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("read isolated global config: %v", err)
	}
	if !bytes.Contains(raw, []byte("Settings Author")) || !bytes.Contains(raw, []byte("settings.author@example.com")) {
		t.Fatalf("the global config does not hold the new identity:\n%s", raw)
	}

	// A commit in a repository without a local identity carries the global
	// one: unset the fixture's repository-level user.name and user.email, so
	// only the isolated global configuration can name the author.
	repo := newGitRepo(t, "scg-identity")
	repo.CommitFile("base.txt", "base\n", "initial commit")
	repo.Git("config", "--unset", "user.name")
	repo.Git("config", "--unset", "user.email")
	repo.WriteFile("authored.txt", "authored\n")

	h2 := newTestApp(t)
	h2.openRepo(repo.StoreRepo())
	h2.drain()
	mustClick(t, h2, "Stage authored.txt")
	h2.drain()
	typeCommitMessage(t, h2, "authored through the box")
	mustClick(t, h2, "Commit")
	h2.drain()
	if err := h2.app.session.ActionErr(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	out := runGitInheritEnv(t, repo.Path(), "log", "-1", "--format=%an%x00%ae")
	name, email, ok := strings.Cut(strings.TrimSpace(out), "\x00")
	if !ok || name != "Settings Author" || email != "settings.author@example.com" {
		t.Fatalf("commit author = %q <%q>, want Settings Author <settings.author@example.com>", name, email)
	}
}

// A global configuration git cannot parse reads as an error on the Git
// page: the identity inputs stay disabled, saving the other Git settings
// never overwrites the broken file, and Retry after a repair loads the
// identity for editing.
func TestSettingsCommitGitBrokenConfigNeverOverwritten(t *testing.T) {
	cfg := isolatedPrefsGitConfig(t)
	broken := []byte("[user\n name = broken\n")
	if err := os.WriteFile(cfg, broken, 0o644); err != nil {
		t.Fatal(err)
	}

	h := newTestApp(t)
	tt := openCommitGitSettingsTester(t, h)
	clickSettingsTab(t, tt, "Git")
	drainSettings(t, h, tt)

	if !tt.HasText("bad config") {
		t.Fatalf("the page shows no read error for the broken config; texts=%q", tt.Texts())
	}
	nameField, ok := tt.AccessByRole(ui.RoleTextField, "Git user name")
	if !ok {
		t.Fatalf("no Git user name field; texts=%q", tt.Texts())
	}
	if !nameField.Disabled {
		t.Fatal("the name input is editable although the identity never loaded")
	}
	emailField, ok := tt.AccessByRole(ui.RoleTextField, "Git user email")
	if !ok || !emailField.Disabled {
		t.Fatal("the email input is editable although the identity never loaded")
	}

	// Saving the toolchain settings must not touch the broken file.
	if err := tt.Click("Save Git Settings"); err != nil {
		t.Fatalf("click Save Git Settings: %v", err)
	}
	drainSettings(t, h, tt)
	after, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("read back the broken config: %v", err)
	}
	if !bytes.Equal(after, broken) {
		t.Fatalf("the save overwrote the broken configuration:\nbefore %q\nafter  %q", broken, after)
	}

	// After the file is repaired, Retry loads the identity and enables the
	// inputs.
	if err := os.WriteFile(cfg, []byte("[user]\n\tname = Repaired User\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Retry Global Identity"); err != nil {
		t.Fatalf("click Retry Global Identity: %v; texts=%q", err, tt.Texts())
	}
	drainSettings(t, h, tt)
	nameField, ok = tt.AccessByRole(ui.RoleTextField, "Git user name")
	if !ok || nameField.Disabled {
		t.Fatal("the name input is still disabled after a successful retry")
	}
	if nameField.Value != "Repaired User" {
		t.Fatalf("the retry loaded name %q, want Repaired User", nameField.Value)
	}
}

// When the global identity changes underneath the loaded page, saving
// refuses to overwrite it: the externally written configuration survives
// byte for byte, the page shows the conflict error, and the draft survives
// for a retry.
func TestSettingsCommitGitExternalChangeRefusesOverwrite(t *testing.T) {
	cfg := isolatedPrefsGitConfig(t)
	if err := os.WriteFile(cfg, []byte("[user]\n\tname = Original\n\temail = original@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := newTestApp(t)
	tt := openCommitGitSettingsTester(t, h)
	clickSettingsTab(t, tt, "Git")
	drainSettings(t, h, tt)

	// An external edit after the load: what a terminal's git config --global
	// would do while the page is open.
	runGitInheritEnv(t, "", "config", "--global", "user.name", "Mallory")
	beforeBytes, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}

	typeInSettingsField(t, tt, "Git user name", "Overwrite Attempt")
	if err := tt.Click("Save Git Settings"); err != nil {
		t.Fatalf("click Save Git Settings: %v", err)
	}
	drainSettings(t, h, tt)

	if !tt.HasText("the global Git identity changed since it was read") {
		t.Fatalf("no conflict error on the page; texts=%q", tt.Texts())
	}
	afterBytes, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterBytes, beforeBytes) {
		t.Fatalf("the external edit was overwritten:\nbefore %q\nafter  %q", beforeBytes, afterBytes)
	}
	// The draft survives for a retry instead of reverting to the live value.
	if field, ok := tt.AccessByRole(ui.RoleTextField, "Git user name"); !ok || field.Value != "Overwrite Attempt" {
		value := ""
		if ok {
			value = field.Value
		}
		t.Fatalf("the draft name reads %q after the refused save, want Overwrite Attempt", value)
	}
}

// When the global configuration cannot be written, saving reports the
// failure and the file keeps every byte it had.
func TestSettingsCommitGitWriteFailureProtectsConfig(t *testing.T) {
	cfg := isolatedPrefsGitConfig(t)
	original := []byte("[user]\n\tname = Locked\n\temail = locked@example.com\n")
	if err := os.WriteFile(cfg, original, 0o644); err != nil {
		t.Fatal(err)
	}

	h := newTestApp(t)
	tt := openCommitGitSettingsTester(t, h)
	clickSettingsTab(t, tt, "Git")
	drainSettings(t, h, tt)

	// The configuration's directory read-only: git cannot create its lock
	// file, so the write must fail. The read still works, so the page loads.
	cfgDir := filepath.Dir(cfg)
	if err := os.Chmod(cfgDir, 0o500); err != nil {
		t.Fatalf("chmod config dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfgDir, 0o700) })

	typeInSettingsField(t, tt, "Git user name", "Write Attempt")
	if err := tt.Click("Save Git Settings"); err != nil {
		t.Fatalf("click Save Git Settings: %v", err)
	}
	drainSettings(t, h, tt)

	if got := commitGitState(h.app).saveErr; got == "" {
		t.Fatal("no save error after writing into a read-only directory")
	} else if !tt.HasText(got) {
		t.Fatalf("the page does not show the save error %q; texts=%q", got, tt.Texts())
	}
	after, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("the failed write changed the configuration:\nbefore %q\nafter  %q", original, after)
	}

	// With the directory writable again the same draft saves, proving the
	// failure was the permissions and not the input.
	if err := os.Chmod(cfgDir, 0o700); err != nil {
		t.Fatalf("restore config dir: %v", err)
	}
	if err := tt.Click("Save Git Settings"); err != nil {
		t.Fatalf("retry save: %v", err)
	}
	drainSettings(t, h, tt)
	after, err = os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(after, []byte("Write Attempt")) {
		t.Fatalf("the retried save did not write the draft:\n%s", after)
	}
}

// A custom git program saved through the Git page reaches the executor of
// the open repository at once through RebuildExecutor: the next reload runs
// through the wrapper. A program that is not git is rejected, changes
// nothing, and never replaces the executor.
func TestSettingsCommitGitCustomGitHotSwitchesExecutor(t *testing.T) {
	repo := newGitRepo(t, "scg-hotswitch")
	repo.CommitFile("base.txt", "base\n", "initial commit")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find real git: %v", err)
	}
	probeDir := t.TempDir()
	logFile := filepath.Join(probeDir, "git-log.txt")
	if err := os.WriteFile(logFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	script := loggingShim(t, probeDir, "wrapped git", realGit, logFile)

	h := openChangesHarness(t, repo)
	tt := openCommitGitSettingsTester(t, h)
	clickSettingsTab(t, tt, "Git")
	drainSettings(t, h, tt)

	chooseGitInstance(t, tt, gitInstanceCustom)
	if _, ok := tt.AccessByRole(ui.RoleTextField, "Custom Git binary"); !ok {
		t.Fatalf("no custom binary input after choosing Custom…; texts=%q", tt.Texts())
	}
	typeInSettingsField(t, tt, "Custom Git binary", script)
	chooseEnvPath(t, tt, "Process PATH")
	if err := tt.Click("Enable verbose Git output"); err != nil {
		t.Fatalf("check verbose master: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click("GIT_TRACE"); err != nil {
		t.Fatalf("check GIT_TRACE: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	before := h.app.session.Repo()
	if err := tt.Click("Save Git Settings"); err != nil {
		t.Fatalf("click Save Git Settings: %v; texts=%q", err, tt.Texts())
	}
	drainSettings(t, h, tt)

	prefs := h.store.Settings()
	if got := prefs.GitPath; got != script {
		t.Fatalf("saved GitPath = %q, want %q", got, script)
	}
	if got := prefs.EnvPath; got != "parent" {
		t.Fatalf("saved EnvPath = %q, want parent", got)
	}
	if !prefs.TraceGit {
		t.Fatal("the GIT_TRACE switch did not persist")
	}
	if errText := commitGitState(h.app).saveErr; errText != "" {
		t.Fatalf("unexpected save error: %s", errText)
	}
	if h.app.session.Repo() == before {
		t.Fatal("the executor of the open repository was never rebuilt")
	}
	// The rebuild's reload already ran through the wrapper: the log holds
	// repository commands beyond the --version validations, and they carry
	// the GIT_TRACE environment the saved switch turns on.
	wrapperOps := false
	for _, line := range readLogLines(t, logFile) {
		if strings.Contains(line, "--version") {
			continue
		}
		wrapperOps = true
		if !strings.Contains(line, "GIT_TRACE=1") {
			t.Fatalf("the rebuilt executor runs without the saved GIT_TRACE environment: %q", line)
		}
	}
	if !wrapperOps {
		t.Fatalf("no repository command ran through the wrapper after the save; log=%q", readLogLines(t, logFile))
	}

	// A further repository operation — here a stage — runs through the
	// wrapper too.
	if err := os.Truncate(logFile, 0); err != nil {
		t.Fatal(err)
	}
	repo.WriteFile("staged.txt", "staged\n")
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Stage staged.txt")
	h.drain()
	stageRan := false
	for _, line := range readLogLines(t, logFile) {
		if strings.Contains(line, "add") {
			stageRan = true
		}
	}
	if !stageRan {
		t.Fatalf("the stage did not run through the wrapper; log=%q", readLogLines(t, logFile))
	}

	// A program that is not git is rejected: the settings keep the wrapper,
	// the executor stays put, and the page shows the error.
	notGitDir := t.TempDir()
	notGit := filepath.Join(notGitDir, "not git")
	if err := os.WriteFile(notGit, []byte("#!/bin/sh\necho hello\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	typeInSettingsField(t, tt, "Custom Git binary", notGit)
	executorBefore := h.app.session.Repo()
	if err := tt.Click("Save Git Settings"); err != nil {
		t.Fatalf("save invalid program: %v", err)
	}
	drainSettings(t, h, tt)
	if !tt.HasText("not a valid Git binary") {
		t.Fatalf("no validation error on the page; texts=%q", tt.Texts())
	}
	if got := h.store.Settings().GitPath; got != script {
		t.Fatalf("the rejected save changed GitPath to %q", got)
	}
	if h.app.session.Repo() != executorBefore {
		t.Fatal("the rejected save replaced the executor of the open repository")
	}
	// The invalid draft survives for fixing, and saving the valid wrapper
	// again clears the error.
	if got := commitGitState(h.app).customPath; got != notGit {
		t.Fatalf("the invalid draft was clobbered: %q", got)
	}
	typeInSettingsField(t, tt, "Custom Git binary", script)
	if err := tt.Click("Save Git Settings"); err != nil {
		t.Fatalf("retry valid save: %v", err)
	}
	drainSettings(t, h, tt)
	if errText := commitGitState(h.app).saveErr; errText != "" {
		t.Fatalf("the valid retry still reports an error: %s", errText)
	}
}

// On a loopback SSH repository the Signed-off-by choice signs commits off
// through the same box, and saving a custom local git program with traces on
// never disturbs the remote one: the wrapper only validates at save time,
// while the repository keeps answering through the server's git.
func TestSettingsCommitGitLoopbackSSH(t *testing.T) {
	isolatedPrefsGitConfig(t)
	repo, srv := loopbackSSHRepo(t)
	wantHead := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH initial load failed: %v", err)
	}

	// Sign off through the Commit page, then commit through the box.
	tt := openCommitGitSettingsTester(t, h)
	clickSettingsTab(t, tt, "Commit")
	if err := tt.Click("Add Signed-off-by to commits"); err != nil {
		t.Fatalf("check sign-off: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click("Save Commit Settings"); err != nil {
		t.Fatalf("save sign-off: %v", err)
	}
	tt.Frame()

	srv.WriteFile(t, repo.Path, "ssh-signed.txt", []byte("signed\n"))
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Stage ssh-signed.txt")
	h.drain()
	typeCommitMessage(t, h, "ssh signed work")
	mustClick(t, h, "Commit")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH commit failed: %v", err)
	}
	body := srv.RunGit(t, repo.Path, "log", "-1", "--format=%B")
	if n := strings.Count(body, "Signed-off-by:"); n != 1 {
		t.Fatalf("SSH commit has %d Signed-off-by lines, want 1:\n%s", n, body)
	}

	// Save a custom local git program and the GIT_TRACE switch through the
	// Git page. The open repository is an SSH one: the rebuild keeps running
	// the server's git, and the wrapper never sees a repository command.
	script, logFile := writeGitWrapperScript(t)
	clickSettingsTab(t, tt, "Git")
	drainSettings(t, h, tt)
	chooseGitInstance(t, tt, gitInstanceCustom)
	typeInSettingsField(t, tt, "Custom Git binary", script)
	if err := tt.Click("Enable verbose Git output"); err != nil {
		t.Fatalf("check verbose master: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click("GIT_TRACE"); err != nil {
		t.Fatalf("check GIT_TRACE: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click("Save Git Settings"); err != nil {
		t.Fatalf("save Git settings: %v; texts=%q", err, tt.Texts())
	}
	drainSettings(t, h, tt)

	prefs := h.store.Settings()
	if prefs.GitPath != script || !prefs.TraceGit {
		t.Fatalf("saved toolchain = path %q trace %v, want %q with GIT_TRACE", prefs.GitPath, prefs.TraceGit, script)
	}
	if errText := commitGitState(h.app).saveErr; errText != "" {
		t.Fatalf("unexpected save error: %s", errText)
	}

	// Turning the master switch off clears the five traces in place: a save
	// right after it persists none of them.
	if err := tt.Click("Enable verbose Git output"); err != nil {
		t.Fatalf("uncheck verbose master: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if got := commitGitState(h.app).traces; got != [5]bool{} {
		t.Fatalf("the traces survived the master switch turning off: %v", got)
	}
	if err := tt.Click("Save Git Settings"); err != nil {
		t.Fatalf("save with traces off: %v; texts=%q", err, tt.Texts())
	}
	drainSettings(t, h, tt)
	if got := h.store.Settings().Trace(); got != (git.Trace{}) {
		t.Fatalf("the saved traces = %+v after the master switch turned off, want all clear", got)
	}

	// The SSH repository still answers through the server's git after the
	// rebuild: a fresh reload finds the same HEAD and no error.
	h.app.session.Reload()
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH reload after the toolchain save failed: %v", err)
	}
	if got := h.app.session.Snapshot().Status.Head; got == wantHead {
		// wantHead is the initial commit; the signed commit moved HEAD.
		t.Fatalf("the signed commit is missing from the reloaded snapshot: HEAD still %q", got)
	}
	wantNow := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	if got := h.app.session.Snapshot().Status.Head; got != wantNow {
		t.Fatalf("reloaded SSH HEAD = %q, want %q", got, wantNow)
	}
	for _, line := range readLogLines(t, logFile) {
		if !strings.Contains(line, "--version") {
			t.Fatalf("the SSH repository ran through the local wrapper: %q", line)
		}
	}
}

// On a loopback SSH repository the executor hot-swap leaves a busy write
// alone: the save still applies to the commands after it, and the remote
// working tree stays consistent throughout.
func TestSettingsCommitGitToolchainSaveDuringBusyLoopbackSSH(t *testing.T) {
	isolatedPrefsGitConfig(t)
	repo, srv := loopbackSSHRepo(t)

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()

	tt := openCommitGitSettingsTester(t, h)
	clickSettingsTab(t, tt, "Git")
	drainSettings(t, h, tt)
	chooseGitInstance(t, tt, gitInstanceCustom)
	script, _ := writeGitWrapperScript(t)
	typeInSettingsField(t, tt, "Custom Git binary", script)

	// Save while a write of the repository is in flight: the write finishes
	// on the executor it started with, the save is not interrupted either.
	srv.WriteFile(t, repo.Path, "busy.txt", []byte("busy\n"))
	h.app.session.Reload()
	h.drain()
	h.queue.PauseWorkers()
	mustClick(t, h, "Stage busy.txt")
	stageWorker := h.queue.WaitWorker(t)
	if err := tt.Click("Save Git Settings"); err != nil {
		t.Fatalf("save during busy: %v", err)
	}
	// The save spawns its validation worker, which the pause holds too; both
	// workers release together, the write still finishing on the executor it
	// started with.
	saveWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(stageWorker)
	h.queue.ReleaseWorker(saveWorker)
	drainSettings(t, h, tt)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("the in-flight write was interrupted: %v", err)
	}
	if got := h.store.Settings().GitPath; got != script {
		t.Fatalf("the busy-time save did not persist GitPath: %q", got)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("the reloaded snapshot failed after the busy-time save: %v", err)
	}
	head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	if got := h.app.session.Snapshot().Status.Head; got != head {
		t.Fatalf("snapshot HEAD = %q, want %q", got, head)
	}
}

// The Commit and Git pages lay out left-aligned inside the smallest window
// size: no control overflows the width of the window, at both the default
// and the minimum size.
func TestSettingsCommitGitLayoutMinWidth(t *testing.T) {
	isolatedPrefsGitConfig(t)
	h := newTestApp(t)
	tt := openCommitGitSettingsTester(t, h)

	assertWithin := func(page string, width float32) {
		t.Helper()
		for _, a := range tt.AccessAll() {
			if right := a.Bounds.X + a.Bounds.W; right > width+0.5 {
				t.Fatalf("%s page: %q (role %v) right edge %.1f beyond the %.0f-point window", page, a.Name, a.Role, right, width)
			}
		}
	}

	for _, width := range []float32{560, 480} {
		height := float32(420)
		if width == 480 {
			height = 360
		}
		tt.SetSize(int(width), int(height))
		tt.Frame()

		clickSettingsTab(t, tt, "Commit")
		tt.SetSize(int(width), int(height))
		tt.Frame()
		softInput, ok := tt.AccessByRole(ui.RoleTextField, "Subject soft limit")
		if !ok {
			t.Fatalf("no Subject soft limit input; texts=%q", tt.Texts())
		}
		if softInput.Bounds.X > 240 {
			t.Fatalf("Commit page at %.0f: the soft limit input starts at X %.1f, not left-aligned", width, softInput.Bounds.X)
		}
		assertWithin("Commit", width)

		clickSettingsTab(t, tt, "Git")
		tt.SetSize(int(width), int(height))
		drainSettings(t, h, tt)
		nameInput, ok := tt.AccessByRole(ui.RoleTextField, "Git user name")
		if !ok {
			t.Fatalf("no Git user name input; texts=%q", tt.Texts())
		}
		if nameInput.Bounds.X > 240 {
			t.Fatalf("Git page at %.0f: the name input starts at X %.1f, not left-aligned", width, nameInput.Bounds.X)
		}
		assertWithin("Git", width)

		// The tab row itself stays on one line inside the window.
		gitTab, ok := tt.Find("Git")
		if !ok {
			t.Fatalf("no Git tab; texts=%q", tt.Texts())
		}
		if gitTab.X+gitTab.W > width+0.5 {
			t.Fatalf("at %.0f the Git tab ends at X %.1f: it overflows the window", width, gitTab.X+gitTab.W)
		}
		commitTab, ok := tt.Find("Commit")
		if !ok {
			t.Fatalf("no Commit tab; texts=%q", tt.Texts())
		}
		if dy := gitTab.Y - commitTab.Y; dy < -2 || dy > 2 {
			t.Fatalf("at %.0f the Git tab sits %.1f below the Commit tab: the tabs are not on one row", width, dy)
		}
	}
}

// At the real Settings window default (560x420) and minimum (480x360) sizes,
// the Git page scrolls vertically so the Save Git Settings button reaches the
// viewport and clicks through to persist changes.
func TestSettingsCommitGitScrollableAtDefaultWindowSize(t *testing.T) {
	isolatedPrefsGitConfig(t)

	for _, sz := range []struct {
		name string
		w, h int
	}{
		{"default-560x420", 560, 420},
		{"min-480x360", 480, 360},
	} {
		t.Run(sz.name, func(t *testing.T) {
			h := newTestApp(t)
			installSettingsHooks(h)
			h.ui.Key(ui.Cmd, ui.KeyComma)
			h.ui.Frame()
			tt := ui.NewTester(h.app.settingsView, sz.w, sz.h)
			tt.Frame()

			clickSettingsTab(t, tt, "Git")
			drainSettings(t, h, tt)

			// Turn on Verbose Git output to make the Git page taller than
			// the window, verify Save Git Settings starts below the viewport,
			// then scroll down to bring it into view and click it.
			if err := tt.Click("Enable verbose Git output"); err != nil {
				t.Fatalf("check verbose master: %v; texts=%q", err, tt.Texts())
			}
			tt.Frame()
			beforeBtn, ok := tt.AccessByRole(ui.RoleButton, "Save Git Settings")
			if !ok {
				t.Fatalf("no Save Git Settings button before scroll; texts=%q", tt.Texts())
			}
			if beforeBtn.Bounds.Y+beforeBtn.Bounds.H <= float32(sz.h) {
				t.Fatalf("expected Save Git Settings (%+v) to start below %dx%d viewport before scrolling", beforeBtn.Bounds, sz.w, sz.h)
			}
			if err := tt.Click("GIT_TRACE"); err != nil {
				// If GIT_TRACE is below the viewport, scroll down first.
				tt.Scroll(float32(sz.w)/2, float32(sz.h)/2, 0, 400)
				tt.Frame()
				if err := tt.Click("GIT_TRACE"); err != nil {
					t.Fatalf("check GIT_TRACE after scroll: %v; texts=%q", err, tt.Texts())
				}
			}
			tt.Frame()

			tt.Scroll(float32(sz.w)/2, float32(sz.h)/2, 0, 800)
			tt.Frame()

			btn, ok := tt.AccessByRole(ui.RoleButton, "Save Git Settings")
			if !ok {
				t.Fatalf("no Save Git Settings button; texts=%q", tt.Texts())
			}
			if btn.Bounds.Y >= beforeBtn.Bounds.Y {
				t.Fatalf("Save Git Settings did not move upward on scroll: before=%+v after=%+v", beforeBtn.Bounds, btn.Bounds)
			}
			if btn.Bounds.Y < 0 || btn.Bounds.Y+btn.Bounds.H > float32(sz.h)+0.5 {
				t.Fatalf("Save Git Settings button bounds %+v still outside %dx%d viewport after scrolling", btn.Bounds, sz.w, sz.h)
			}
			if err := tt.Click("Save Git Settings"); err != nil {
				t.Fatalf("click Save Git Settings after scroll: %v", err)
			}
			drainSettings(t, h, tt)
			if !h.store.Settings().TraceGit {
				t.Fatal("scrolling to and clicking Save Git Settings did not persist GIT_TRACE")
			}
		})
	}
}

// An unreadable global Git configuration (mode 000) reports a read error on the
// Git page, disables the identity inputs, leaves the file untouched when saving
// other settings, and reloads via Retry once permissions are restored.
func TestSettingsCommitGitUnreadableConfigNeverOverwritten(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores chmod 000")
	}
	cfg := isolatedPrefsGitConfig(t)
	original := []byte("[user]\n\tname = Secret Owner\n\temail = secret@example.com\n")
	if err := os.WriteFile(cfg, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg, 0o000); err != nil {
		t.Fatalf("chmod 000 global config: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfg, 0o644) })

	h := newTestApp(t)
	tt := openCommitGitSettingsTester(t, h)
	clickSettingsTab(t, tt, "Git")
	drainSettings(t, h, tt)

	if commitGitState(h.app).identityReadErr == nil {
		t.Fatalf("unreadable global config produced no identityReadErr; texts=%q", tt.Texts())
	}
	if !tt.HasText("reading the global Git configuration") {
		t.Fatalf("unreadable global config error not shown on Git page; texts=%q", tt.Texts())
	}
	nameField, ok := tt.AccessByRole(ui.RoleTextField, "Git user name")
	if !ok || !nameField.Disabled {
		t.Fatal("Git user name input remained editable on unreadable global config")
	}
	emailField, ok := tt.AccessByRole(ui.RoleTextField, "Git user email")
	if !ok || !emailField.Disabled {
		t.Fatal("Git user email input remained editable on unreadable global config")
	}

	// Saving other Git settings must not overwrite the unreadable config.
	if err := tt.Click("Save Git Settings"); err != nil {
		t.Fatalf("click Save Git Settings: %v", err)
	}
	drainSettings(t, h, tt)

	if err := os.Chmod(cfg, 0o644); err != nil {
		t.Fatalf("restore global config perms: %v", err)
	}
	after, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("saving with unreadable config overwrote bytes:\nbefore %q\nafter  %q", original, after)
	}

	if err := tt.Click("Retry Global Identity"); err != nil {
		t.Fatalf("click Retry Global Identity: %v", err)
	}
	drainSettings(t, h, tt)
	nameField, ok = tt.AccessByRole(ui.RoleTextField, "Git user name")
	if !ok || nameField.Disabled || nameField.Value != "Secret Owner" {
		t.Fatalf("after retry name field = %+v (ok=%v), want enabled Secret Owner", nameField, ok)
	}
}

// Amending an unsigned commit while Signed-off-by is enabled adds exactly one
// Signed-off-by trailer through amendWithGuard.
func TestSettingsCommitGitAmendAddsSignoffToUnsignedCommit(t *testing.T) {
	repo := newGitRepo(t, "scg-amend-unsigned")
	repo.CommitFile("base.txt", "base\n", "unsigned commit")
	repo.WriteFile("base.txt", "base\namended\n")

	h := openChangesHarness(t, repo)
	tt := openCommitGitSettingsTester(t, h)
	clickSettingsTab(t, tt, "Commit")
	if err := tt.Click("Add Signed-off-by to commits"); err != nil {
		t.Fatalf("check Add Signed-off-by to commits: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Save Commit Settings"); err != nil {
		t.Fatalf("save sign-off: %v", err)
	}
	tt.Frame()

	mustClick(t, h, "Stage base.txt")
	h.drain()
	selectAmend(t, h)
	h.drain()
	if strings.Contains(h.app.changesView.message, "Signed-off-by:") {
		t.Fatalf("unsigned commit prefill unexpectedly contained trailer: %q", h.app.changesView.message)
	}
	mustClick(t, h, "Commit")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("amend failed: %v", err)
	}
	body := repo.Git("log", "-1", "--format=%B")
	if n := strings.Count(body, "Signed-off-by:"); n != 1 {
		t.Fatalf("amended unsigned commit has %d Signed-off-by lines, want 1:\n%s", n, body)
	}
}
