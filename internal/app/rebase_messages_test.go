package app

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/sshtest"
)

// --- fixtures and helpers ---

// irMessageFixture builds a linear history whose commits carry full multi-line
// messages — a subject, a blank line and a body — so the tests can tell a
// complete message apart from its one-line subject everywhere. It returns the
// commit hashes, oldest first.
func irMessageFixture(t *testing.T, repo *gitRepoFixture, subjects ...string) []string {
	t.Helper()
	hashes := make([]string, 0, len(subjects))
	for i, subject := range subjects {
		name := "m" + string(rune('1'+i)) + ".txt"
		message := subject + "\n\n" + subject + " body"
		hashes = append(hashes, repo.CommitFile(name, subject+"\n", message))
	}
	return hashes
}

// chooseIRActionUp moves the action choice of the row of hash upward through
// the fixed choice list, the mirror of chooseIRAction for actions before the
// current one.
func chooseIRActionUp(t *testing.T, h *testHarness, hash, from, action string) {
	t.Helper()
	label := "Action " + irShort(hash)
	if err := h.ui.Click(label); err != nil {
		t.Fatalf("click %q: %v\ntexts=%q", label, err, h.ui.Texts())
	}
	steps := slices.Index(irActionNames, from) - slices.Index(irActionNames, action)
	if steps < 0 {
		t.Fatalf("chooseIRActionUp only moves up the list: %q to %q", from, action)
	}
	for i := 0; i < steps; i++ {
		h.ui.Key(0, ui.KeyUp)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	row := irRowOf(t, h, hash)
	if row.action != action {
		t.Fatalf("action of %s = %q after choosing, want %q", irShort(hash), row.action, action)
	}
}

// drainIRMessages waits for the form's original-message read to land.
func drainIRMessages(t *testing.T, h *testHarness) {
	t.Helper()
	h.drain()
	ms := interactiveRebaseMessages(h.app)
	if ms.reading {
		t.Fatal("interactive rebase message read still in flight after drain")
	}
}

// irMessagesState surfaces the app's message state to assertions.
func irMessagesState(t *testing.T, h *testHarness) *interactiveRebaseMessagesState {
	t.Helper()
	return interactiveRebaseMessages(h.app)
}

// replaceIRMessage selects the whole editor text and types the replacement,
// the way a user rewrites the prepared message.
func replaceIRMessage(t *testing.T, h *testHarness, message string) {
	t.Helper()
	if err := h.ui.Click("Commit message"); err != nil {
		t.Fatalf("click Commit message editor: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Command("selectAll")
	h.ui.Type(message)
	h.ui.Frame()
	ms := irMessagesState(t, h)
	if ms.editText != message {
		t.Fatalf("editor text after typing = %q, want %q", ms.editText, message)
	}
	// The draft lands inside the panel's build, after the modal head of the
	// same frame already computed its validation: one more frame brings the
	// form's copy of the state in line.
	h.ui.Frame()
}

// isMessageLogCommand matches the git log --format=%B reads of the original
// commit messages, and nothing else.
func isMessageLogCommand(args []string) bool {
	return len(args) > 0 && args[0] == "log" && slices.Contains(args, "--format=%B")
}

// irLogMessages returns the full messages of the branch, one per commit,
// newest first. git terminates every %B entry with a newline the message
// itself does not carry; stripping exactly one trailing newline recovers the
// committed bytes, blank-line interior included.
func irLogMessages(t *testing.T, repo *gitRepoFixture) []string {
	t.Helper()
	out := gitOutTrim(t, repo.Path(), "log", "--format=%B%x1e", "main")
	entries := strings.Split(out, "\x1e")
	var messages []string
	for _, entry := range entries {
		entry = strings.TrimPrefix(entry, "\n")
		entry = strings.TrimSuffix(entry, "\n")
		if entry == "" {
			continue
		}
		messages = append(messages, entry)
	}
	return messages
}

// irMessageAt returns the full message of the commit whose subject line is
// the given one, reading the branch history independently of the app.
func irMessageAt(t *testing.T, repo *gitRepoFixture, subject string) string {
	t.Helper()
	for _, message := range irLogMessages(t, repo) {
		first, _, _ := strings.Cut(message, "\n")
		if first == subject {
			return message
		}
	}
	t.Fatalf("no commit with subject %q in main", subject)
	return ""
}

// irSSHMessageAt is irMessageAt for a repository on the SSH server.
func irSSHMessageAt(t *testing.T, srv *sshtest.Target, dir, subject string) string {
	t.Helper()
	out := srv.RunGit(t, dir, "log", "--format=%B%x1e", "main")
	for _, entry := range strings.Split(out, "\x1e") {
		entry = strings.TrimPrefix(entry, "\n")
		entry = strings.TrimSuffix(entry, "\n")
		first, _, _ := strings.Cut(entry, "\n")
		if first == subject {
			return entry
		}
	}
	t.Fatalf("no commit with subject %q in the remote main", subject)
	return ""
}

// --- reword ---

// A row switched to Reword opens the editor in the same form, prefilled with
// the commit's complete multi-line message — never its one-line subject.
// Editing the text and rebasing rewrites the commit byte for byte, blank
// lines and shell-significant characters included.
func TestRebaseInteractiveMessagesRewordPreservesBodyAndEdits(t *testing.T) {
	repo := newGitRepo(t, "irmsg-reword")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one", "ir two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	// The editor opens on its own when the action becomes Reword.
	chooseIRAction(t, h, hashes[0], "Pick", "Reword")
	h.ui.Frame()
	ms := irMessagesState(t, h)
	if ms.editingHash != hashes[0] {
		t.Fatalf("editor target after choosing Reword = %q, want %q", ms.editingHash, hashes[0])
	}
	if !hasExactText(h.ui, "Message for "+irShort(hashes[0])) {
		t.Fatalf("editor title missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || !btn.Disabled {
		t.Fatalf("Rebase while the original message loads = %+v found=%v, want disabled", btn, ok)
	}
	drainIRMessages(t, h)

	// The prefill is the full message: subject, blank line, body.
	wantOriginal := "ir one\n\nir one body"
	if ms.editText != wantOriginal {
		t.Fatalf("editor prefill = %q, want the complete message %q", ms.editText, wantOriginal)
	}
	if len(ms.drafts) != 0 {
		t.Fatalf("drafts before typing = %v, want none", ms.drafts)
	}

	edited := "rewritten subject with 'quotes' and $HOME $(false)\n\nrewritten body line\n\nsecond body paragraph"
	replaceIRMessage(t, h, edited)
	if !hasExactText(h.ui, "Edit Message •") {
		t.Fatalf("edited marker missing on the row button; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Done"); err != nil {
		t.Fatalf("click Done: %v", err)
	}
	h.ui.Frame()
	if ms.editingHash != "" {
		t.Fatal("editor still open after Done")
	}
	if ms.drafts[hashes[0]] != edited {
		t.Fatalf("draft after Done = %q, want %q", ms.drafts[hashes[0]], edited)
	}

	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("reword rebase failed: %v", err)
	}
	if got := irMessageAt(t, repo, "ir two"); got != "ir two\n\nir two body" {
		t.Fatalf("untouched commit message = %q, want its original", got)
	}
	if got := irMessageAt(t, repo, "rewritten subject with 'quotes' and $HOME $(false)"); got != edited {
		t.Fatalf("rewritten commit message = %q, want byte-exact %q", got, edited)
	}
	assertIRDialogClosed(t, h)
}

// A Reword the user never edits rewrites nothing: the commit keeps its
// complete original message byte for byte, body included.
func TestRebaseInteractiveMessagesRewordKeepsOriginalBodyUnedited(t *testing.T) {
	repo := newGitRepo(t, "irmsg-reword-same")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[0], "Pick", "Reword")
	drainIRMessages(t, h)
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unedited reword rebase failed: %v", err)
	}
	if got := strings.TrimRight(gitOutTrim(t, repo.Path(), "log", "-1", "--format=%B", "main"), "\n"); got != "ir one\n\nir one body" {
		t.Fatalf("unedited reword message = %q, want the original complete message", got)
	}
	assertIRDialogClosed(t, h)
}

// --- squash composition ---

// Squashing without editing joins the complete messages of the fold target
// and the squashed commit — bodies included — instead of collapsing to the
// squashed commit's one-line subject.
func TestRebaseInteractiveMessagesSquashDefaultMergesFullBodies(t *testing.T) {
	repo := newGitRepo(t, "irmsg-squash-default")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one", "ir two", "ir three")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[2], "Pick", "Squash")
	drainIRMessages(t, h)
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("squash rebase failed: %v", err)
	}
	want := "ir two\n\nir two body\n\nir three\n\nir three body"
	if got := irMessageAt(t, repo, "ir two"); got != want {
		t.Fatalf("default squash message = %q, want %q", got, want)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	if !slices.Equal(subjects, []string{"ir two", "ir one", "base commit"}) {
		t.Fatalf("log after squash = %v", subjects)
	}
	if content := strings.TrimSpace(repo.Git("show", "HEAD:m3.txt")); content != "ir three" {
		t.Fatalf("m3.txt at the squashed tip = %q", content)
	}
	assertIRDialogClosed(t, h)
}

// An edited squash message replaces the default composition verbatim: the
// panel of the chain parent carries the joined default, and the typed text
// is what the folded commit ends with.
func TestRebaseInteractiveMessagesSquashCustom(t *testing.T) {
	repo := newGitRepo(t, "irmsg-squash-custom")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one", "ir two", "ir three")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[2], "Pick", "Squash")
	drainIRMessages(t, h)

	// The chain parent — ir two, not the squash row itself — offers the editor.
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Edit Message "+irShort(hashes[1])); !ok {
		t.Fatalf("Edit Message button of the chain parent missing; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Edit Message " + irShort(hashes[1])); err != nil {
		t.Fatalf("click Edit Message: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Squash message for "+irShort(hashes[1])) {
		t.Fatalf("squash editor title missing; texts=%q", h.ui.Texts())
	}
	ms := irMessagesState(t, h)
	wantDefault := "ir two\n\nir two body\n\nir three\n\nir three body"
	if ms.editText != wantDefault {
		t.Fatalf("squash editor prefill = %q, want %q", ms.editText, wantDefault)
	}

	custom := "combined subject\n\nrewritten combined body"
	replaceIRMessage(t, h, custom)
	if err := h.ui.Click("Done"); err != nil {
		t.Fatalf("click Done: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("custom squash rebase failed: %v", err)
	}
	if got := irMessageAt(t, repo, "combined subject"); got != custom {
		t.Fatalf("custom squash message = %q, want %q", got, custom)
	}
	assertIRDialogClosed(t, h)
}

// Fixup rows fold files without joining their messages, and a Drop inside
// the chain breaks nothing: the squash after it still folds into the most
// recent kept commit and the composition skips the dropped commit entirely.
func TestRebaseInteractiveMessagesSquashFixupDropChain(t *testing.T) {
	repo := newGitRepo(t, "irmsg-chain")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one", "ir two", "ir three", "ir four", "ir five")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	// Execution order: one(pick) two(drop) three(fixup) four(squash)
	// five(fixup): the chain ends in a fixup after its squash, so the prepared
	// message must land on the commit the whole chain folds into.
	chooseIRAction(t, h, hashes[1], "Pick", "Drop")
	chooseIRAction(t, h, hashes[2], "Pick", "Fixup")
	chooseIRAction(t, h, hashes[3], "Pick", "Squash")
	chooseIRAction(t, h, hashes[4], "Pick", "Fixup")
	drainIRMessages(t, h)
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("chain rebase failed: %v", err)
	}
	// ir one's chain holds both fixups and the squash; two is dropped, so its
	// message joins nothing; the fixups contribute their files but no message.
	want := "ir one\n\nir one body\n\nir four\n\nir four body"
	if got := irMessageAt(t, repo, "ir one"); got != want {
		t.Fatalf("chain fold message = %q, want %q", got, want)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	if !slices.Equal(subjects, []string{"ir one", "base commit"}) {
		t.Fatalf("log after the chain = %v", subjects)
	}
	for _, f := range []string{"m3.txt", "m4.txt", "m5.txt"} {
		if out, err := tryGitCommand(repo.Path(), "cat-file", "-e", "HEAD:"+f); err != nil {
			t.Fatalf("%s of the folded commits missing in ir one's successor: %s", f, out)
		}
	}
	if out, err := tryGitCommand(repo.Path(), "cat-file", "-e", "main:m2.txt"); err == nil {
		t.Fatalf("m2.txt of the dropped commit survived: %s", out)
	}
	assertIRDialogClosed(t, h)
}

// A Reword whose chain ends in fixups lands its message after the last fixup
// folded in: the rewritten commit carries the edited text and the files of
// the whole chain.
func TestRebaseInteractiveMessagesRewordFixupChain(t *testing.T) {
	repo := newGitRepo(t, "irmsg-reword-fixup")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one", "ir two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[0], "Pick", "Reword")
	chooseIRAction(t, h, hashes[1], "Pick", "Fixup")
	drainIRMessages(t, h)
	edited := "reworded parent\n\nreworded parent body"
	replaceIRMessage(t, h, edited)
	if err := h.ui.Click("Done"); err != nil {
		t.Fatalf("click Done: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("reword-fixup rebase failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	if !slices.Equal(subjects, []string{"reworded parent", "base commit"}) {
		t.Fatalf("log after reword-fixup = %v", subjects)
	}
	if got := strings.TrimRight(gitOutTrim(t, repo.Path(), "log", "-1", "--format=%B", "main"), "\n"); got != edited {
		t.Fatalf("reworded chain message = %q, want %q", got, edited)
	}
	if content := strings.TrimSpace(repo.Git("show", "HEAD:m2.txt")); content != "ir two" {
		t.Fatalf("m2.txt at the chain tip = %q", content)
	}
	assertIRDialogClosed(t, h)
}

// An Edit parent's squash chain mounts no prepared message: whatever the
// user amends at the stop becomes the base of the squash composition, and
// the prepared-message machinery never overwrites it.
func TestRebaseInteractiveMessagesEditParentSquashKeepsStopAmend(t *testing.T) {
	repo := newGitRepo(t, "irmsg-edit-chain")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one", "ir two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[0], "Pick", "Edit")
	chooseIRAction(t, h, hashes[1], "Pick", "Squash")
	h.ui.Frame()
	// An Edit parent edits no message through the form, and nothing blocks
	// the rebase: its chain reads nothing.
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Edit Message "+irShort(hashes[0])); ok {
		t.Fatal("Edit Message button present on an Edit parent row")
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || btn.Disabled {
		t.Fatalf("Rebase with an Edit chain = %+v found=%v, want enabled", btn, ok)
	}
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase to the edit stop failed: %v", err)
	}
	if got := h.app.session.Snapshot().Status.Stopped; got != "edit" {
		t.Fatalf("stop reason = %q, want edit", got)
	}

	// The user's own amend at the stop, done externally as the user would in
	// their terminal.
	repo.WriteFile("m1.txt", "ir one amended\n")
	repo.Git("add", "--", "m1.txt")
	repo.Git("commit", "-q", "--amend", "-m", "amended at the stop\n\nstop body")
	h.app.session.Reload()
	h.drain()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Continue Operation"); !ok || btn.Disabled {
		t.Fatalf("Continue Operation at the edit stop = %+v found=%v, want enabled", btn, ok)
	}
	if err := h.ui.Click("Continue Operation"); err != nil {
		t.Fatalf("click Continue Operation: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("continue after the edit stop failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	if !slices.Equal(subjects, []string{"amended at the stop", "base commit"}) {
		t.Fatalf("log after the edit chain = %v", subjects)
	}
	got := strings.TrimRight(gitOutTrim(t, repo.Path(), "log", "-1", "--format=%B", "main"), "\n")
	if !strings.HasPrefix(got, "amended at the stop\n\nstop body") {
		t.Fatalf("squash after the edit stop dropped the amended message: %q", got)
	}
	if !strings.Contains(got, "ir two") {
		t.Fatalf("squashed commit's message missing from the composition: %q", got)
	}
	assertIRDialogClosed(t, h)
}

// --- draft lifecycle ---

// A draft follows the action, not the row: switching an edited row back to
// Pick or to Drop closes its editor and the rebase that then runs carries no
// trace of the abandoned text — the kept commits keep their original
// messages byte for byte.
func TestRebaseInteractiveMessagesStaleDraftNotCompiled(t *testing.T) {
	repo := newGitRepo(t, "irmsg-stale-draft")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one", "ir two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[0], "Pick", "Reword")
	drainIRMessages(t, h)
	replaceIRMessage(t, h, "abandoned words\n\nnever written")
	if err := h.ui.Click("Done"); err != nil {
		t.Fatalf("click Done: %v", err)
	}
	h.ui.Frame()

	// Back to Pick: the squash-free row owns no message anymore.
	chooseIRActionUp(t, h, hashes[0], "Reword", "Pick")
	ms := irMessagesState(t, h)
	if ms.editingHash != "" {
		t.Fatal("editor survived the switch back to Pick")
	}
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase after the stale draft failed: %v", err)
	}
	if got := irMessageAt(t, repo, "ir one"); got != "ir one\n\nir one body" {
		t.Fatalf("message after the abandoned draft = %q, want the original", got)
	}
	if out := gitOutTrim(t, repo.Path(), "log", "--format=%B", "main"); strings.Contains(out, "abandoned words") {
		t.Fatalf("stale draft leaked into the history: %q", out)
	}

	// Round 2: an edited row switched to Drop — its draft must not land on
	// the surviving commits either.
	repo.WriteFile("extra.txt", "extra\n")
	repo.Git("add", "--", "extra.txt")
	repo.Git("commit", "-q", "-m", "ir three\n\nir three body")
	h.app.session.Reload()
	h.drain()
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)
	rows := interactiveRebaseUI(h.app).rows
	var oneHash string
	for _, row := range rows {
		if row.subject == "ir one" {
			oneHash = row.hash
		}
	}
	if oneHash == "" {
		t.Fatal("ir one missing from the reopened plan")
	}
	chooseIRAction(t, h, oneHash, "Pick", "Reword")
	drainIRMessages(t, h)
	replaceIRMessage(t, h, "dropped draft\n\nmust not survive")
	if err := h.ui.Click("Done"); err != nil {
		t.Fatalf("click Done: %v", err)
	}
	h.ui.Frame()
	chooseIRAction(t, h, oneHash, "Reword", "Drop")
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase with the dropped draft failed: %v", err)
	}
	if out := gitOutTrim(t, repo.Path(), "log", "--format=%B", "main"); strings.Contains(out, "dropped draft") {
		t.Fatalf("dropped row's draft leaked into the history: %q", out)
	}
	assertIRDialogClosed(t, h)
}

// The default composition recomputes with the chain's members while the user
// has not typed: un-squashing a row under the open editor shrinks the
// default back to the parent's own message, and the editor follows because
// nothing custom exists yet.
func TestRebaseInteractiveMessagesDefaultRecomputesWithMembership(t *testing.T) {
	repo := newGitRepo(t, "irmsg-recompute")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one", "ir two", "ir three")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[2], "Pick", "Squash")
	drainIRMessages(t, h)
	if err := h.ui.Click("Edit Message " + irShort(hashes[1])); err != nil {
		t.Fatalf("click Edit Message: %v", err)
	}
	h.ui.Frame()
	ms := irMessagesState(t, h)
	if want := "ir two\n\nir two body\n\nir three\n\nir three body"; ms.editText != want {
		t.Fatalf("editor before the membership change = %q, want %q", ms.editText, want)
	}

	// Un-squash ir three: the chain dissolves, the parent's editor closes
	// with it, and reopening shows the parent's own message alone.
	chooseIRActionUp(t, h, hashes[2], "Squash", "Pick")
	if ms.editingHash != "" {
		t.Fatal("editor survived the dissolution of its chain")
	}
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase after un-squashing failed: %v", err)
	}
	if got := irMessageAt(t, repo, "ir two"); got != "ir two\n\nir two body" {
		t.Fatalf("ir two message = %q, want its original alone", got)
	}
	assertIRDialogClosed(t, h)
}

// Reset Message drops the custom draft: the editor shows the default
// composition again and the rebase executes with it, not with what the user
// typed before the reset.
func TestRebaseInteractiveMessagesResetRestoresDefault(t *testing.T) {
	repo := newGitRepo(t, "irmsg-reset")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[0], "Pick", "Reword")
	drainIRMessages(t, h)
	replaceIRMessage(t, h, "temporary rewrite")
	ms := irMessagesState(t, h)
	if ms.drafts[hashes[0]] != "temporary rewrite" {
		t.Fatalf("draft before reset = %q", ms.drafts[hashes[0]])
	}
	if err := h.ui.Click("Reset Message"); err != nil {
		t.Fatalf("click Reset Message: %v", err)
	}
	h.ui.Frame()
	if len(ms.drafts) != 0 {
		t.Fatalf("drafts after reset = %v, want none", ms.drafts)
	}
	if want := "ir one\n\nir one body"; ms.editText != want {
		t.Fatalf("editor after reset = %q, want the default %q", ms.editText, want)
	}
	if err := h.ui.Click("Done"); err != nil {
		t.Fatalf("click Done: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase after reset failed: %v", err)
	}
	if got := irMessageAt(t, repo, "ir one"); got != "ir one\n\nir one body" {
		t.Fatalf("message after reset = %q, want the original", got)
	}
	assertIRDialogClosed(t, h)
}

// --- validation, failure and recovery ---

// A whitespace-only message is no message: the form names the commit, the
// start button disables, the plan compiler refuses the same draft, and the
// repository stays untouched until the user fixes the text.
func TestRebaseInteractiveMessagesEmptyValidation(t *testing.T) {
	repo := newGitRepo(t, "irmsg-empty")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one")
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[0], "Pick", "Reword")
	drainIRMessages(t, h)
	replaceIRMessage(t, h, "   ")
	if !hasExactText(h.ui, "Commit "+irShort(hashes[0])+" needs a commit message.") {
		t.Fatalf("empty-message validation missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || !btn.Disabled {
		t.Fatalf("Rebase with an empty message = %+v found=%v, want disabled", btn, ok)
	}
	// The compiler is the second line of defense behind the disabled button.
	if _, err := h.app.buildInteractiveRebasePlan(interactiveRebaseUI(h.app)); err == nil || !strings.Contains(err.Error(), "needs a commit message") {
		t.Fatalf("buildInteractiveRebasePlan with an empty message = %v, want a refusal", err)
	}

	replaceIRMessage(t, h, "recovered subject")
	if hasExactText(h.ui, "Commit "+irShort(hashes[0])+" needs a commit message.") {
		t.Fatal("validation survived the fix")
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || btn.Disabled {
		t.Fatalf("Rebase after fixing the message = %+v found=%v, want enabled", btn, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved without a rebase: %q, want %q", got, head)
	}
}

// A failing read of the original messages never falls back to the one-line
// subjects: the form shows the failure with a Retry Message button, the
// rebase cannot start, the repository is untouched, and the retry reads for
// real once the failure is gone.
func TestRebaseInteractiveMessagesReadFailureRetryNoSubjectFallback(t *testing.T) {
	repo := newGitRepo(t, "irmsg-read-fail")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one")
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	installFailFirstExecutor(t, h, isMessageLogCommand, errors.New("injected message read failure"))
	chooseIRAction(t, h, hashes[0], "Pick", "Reword")
	drainIRMessages(t, h)

	if !hasExactText(h.ui, "injected message read failure") {
		t.Fatalf("read failure text missing; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Retry Message"); !ok {
		t.Fatalf("Retry Message button missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || !btn.Disabled {
		t.Fatalf("Rebase after the failed read = %+v found=%v, want disabled", btn, ok)
	}
	ms := irMessagesState(t, h)
	if len(ms.originals) != 0 {
		t.Fatalf("originals cached despite the failure: %v", ms.originals)
	}
	// The compiler refuses as well: no silent subject fallback anywhere.
	if _, err := h.app.buildInteractiveRebasePlan(interactiveRebaseUI(h.app)); err == nil {
		t.Fatal("buildInteractiveRebasePlan compiled despite the failed read")
	}

	// The injected failure fired exactly once: retrying reads for real.
	if err := h.ui.Click("Retry Message"); err != nil {
		t.Fatalf("click Retry Message: %v", err)
	}
	drainIRMessages(t, h)
	if hasExactText(h.ui, "injected message read failure") {
		t.Fatal("failure text survived the successful retry")
	}
	if ms.originals[hashes[0]] != "ir one\n\nir one body" {
		t.Fatalf("original after retry = %q, want the complete message", ms.originals[hashes[0]])
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || btn.Disabled {
		t.Fatalf("Rebase after the retry = %+v found=%v, want enabled", btn, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	assertIRRepoUnchanged(t, repo, head, "main", "2")
}

// Switching the repository discards the message cache and every draft, and
// the in-flight read's late result — held back until after the switch —
// never lands in the other repository's state.
func TestRebaseInteractiveMessagesDiscardedOnRepoSwitch(t *testing.T) {
	repoA := newGitRepo(t, "irmsg-switch-a")
	repoA.CommitFile("base.txt", "base\n", "base commit")
	ontoA := strings.TrimSpace(repoA.Git("rev-parse", "HEAD"))
	irMessageFixture(t, repoA, "ir one")
	repoB := newGitRepo(t, "irmsg-switch-b")
	repoB.CommitFile("b.txt", "b\n", "commit in beta")

	h := newTestApp(t, repoB.StoreRepo())
	openHistoryPage(t, h, repoA.StoreRepo())
	openIRDialog(t, h, ontoA, "base commit")
	drainIRPlan(t, h)

	// Hold the workers so the message read the Reword choice triggers cannot
	// even start: its result can only land after the test releases it.
	h.queue.PauseWorkers()
	rows := interactiveRebaseUI(h.app).rows
	chooseIRAction(t, h, rows[0].hash, "Pick", "Reword")
	readWorker := h.queue.WaitWorker(t)

	// Switch the repository while the read is in flight: resume first so the
	// switch's own load workers are never gated, then release the held read —
	// its result can only land after the generation moved past it.
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

	ms := irMessagesState(t, h)
	if len(ms.drafts) != 0 || len(ms.originals) != 0 || ms.editingHash != "" || ms.reading {
		t.Fatalf("message state survived the repository switch: drafts=%v originals=%d editing=%q reading=%v", ms.drafts, len(ms.originals), ms.editingHash, ms.reading)
	}
	if interactiveRebaseUI(h.app).open {
		t.Fatal("form still open after the repository switch")
	}
}

// --- conflict and layout ---

// A conflict on the reworded commit stops the rebase mid-run; resolving it
// and continuing through the banner's button still lands the edited message
// byte for byte — the prepared message survives the stop.
func TestRebaseInteractiveMessagesConflictContinuePreservesMessage(t *testing.T) {
	repo := newGitRepo(t, "irmsg-conflict")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.CommitFile("conflict.txt", "one\n", "ir one\n\nir one body")
	repo.CommitFile("conflict.txt", "two\n", "ir two\n\nir two body")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	rows := interactiveRebaseUI(h.app).rows
	var oneHash, twoHash string
	for _, row := range rows {
		switch row.subject {
		case "ir one":
			oneHash = row.hash
		case "ir two":
			twoHash = row.hash
		}
	}
	if oneHash == "" || twoHash == "" {
		t.Fatal("fixture rows missing")
	}
	// Dropping ir one makes ir two's replay conflict: conflict.txt does not
	// exist yet when two tries to change it.
	chooseIRAction(t, h, oneHash, "Pick", "Drop")
	chooseIRAction(t, h, twoHash, "Pick", "Reword")
	drainIRMessages(t, h)
	edited := "conflicted reword\n\nbody survives the conflict"
	replaceIRMessage(t, h, edited)
	if err := h.ui.Click("Done"); err != nil {
		t.Fatalf("click Done: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase to the conflict failed: %v", err)
	}
	st := h.app.session.Snapshot().Status
	if st.State != "rebase" || !st.Conflicted() {
		t.Fatalf("fixture: no conflict stop: %+v", st)
	}

	// Resolve through the real Local Changes UI and continue.
	repo.WriteFile("conflict.txt", "two\n")
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Mark Resolved conflict.txt"); err != nil {
		t.Fatalf("click Mark Resolved conflict.txt: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if err := h.ui.Click("Continue Operation"); err != nil {
		t.Fatalf("click Continue Operation: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("continue after the conflict failed: %v", err)
	}
	if got := irMessageAt(t, repo, "conflicted reword"); got != edited {
		t.Fatalf("message after conflict continue = %q, want byte-exact %q", got, edited)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after continue = %q, want empty", got)
	}
	assertIRDialogClosed(t, h)
}

// With the editing panel open the form stays horizontally centered in the
// window and the input and both panel buttons render inside the dialog.
func TestRebaseInteractiveMessagesLayoutCentered(t *testing.T) {
	repo := newGitRepo(t, "irmsg-layout")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[0], "Pick", "Reword")
	drainIRMessages(t, h)
	assertDialogLayout(t, h, "Cancel", "Rebase", "Reset Message", "Done")
	dlg, ok := h.ui.AccessByRole(ui.RoleDialog, "")
	if !ok {
		t.Fatal("no dialog present")
	}
	area, ok := h.ui.AccessByRole(ui.RoleTextField, "Commit message")
	if !ok {
		t.Fatalf("Commit message editor missing; texts=%q", h.ui.Texts())
	}
	assertInside(t, area.Bounds, dlg.Bounds, "Commit message editor")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	assertIRDialogClosed(t, h)
}

// --- SSH loopback ---

// irRemoteMessages builds the linear fixture with multi-line messages on the
// server, returning the base hash and the commit hashes oldest first.
func irRemoteMessages(t *testing.T, srv *sshtest.Target, dir string, subjects ...string) (onto string, hashes []string) {
	t.Helper()
	onto = strings.TrimSpace(srv.RunGit(t, dir, "rev-parse", "HEAD"))
	for i, subject := range subjects {
		name := "m" + string(rune('1'+i)) + ".txt"
		hashes = append(hashes, remoteCommitFile(t, srv, dir, name, subject+"\n", subject+"\n\n"+subject+" body"))
	}
	return onto, hashes
}

// Over a real SSH connection a reworded commit keeps its complete edited
// message byte for byte on the server: the original multi-line message loads
// into the editor through SSH, and the rewrite lands through it.
func TestRebaseInteractiveMessagesRewordLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	onto, hashes := irRemoteMessages(t, srv, repo.Path, "ir one", "ir two")

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)
	openIRDialog(t, h, onto, "initial commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[0], "Pick", "Reword")
	drainIRMessages(t, h)
	ms := irMessagesState(t, h)
	if want := "ir one\n\nir one body"; ms.editText != want {
		t.Fatalf("SSH editor prefill = %q, want %q", ms.editText, want)
	}
	edited := "ssh rewritten subject\n\nssh rewritten body\n\nsecond paragraph"
	replaceIRMessage(t, h, edited)
	if err := h.ui.Click("Done"); err != nil {
		t.Fatalf("click Done: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH reword rebase failed: %v", err)
	}
	if got := irSSHMessageAt(t, srv, repo.Path, "ssh rewritten subject"); got != edited {
		t.Fatalf("SSH rewritten message = %q, want byte-exact %q", got, edited)
	}
	if got := irSSHMessageAt(t, srv, repo.Path, "ir two"); got != "ir two\n\nir two body" {
		t.Fatalf("SSH untouched commit message = %q, want its original", got)
	}
	assertIRDialogClosed(t, h)
}

// Over SSH an unedited squash joins the complete messages of the fold target
// and the squashed commit, and an edited one lands the typed text instead —
// both read back from the server.
func TestRebaseInteractiveMessagesSquashLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	onto, hashes := irRemoteMessages(t, srv, repo.Path, "ir one", "ir two", "ir three")

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)
	openIRDialog(t, h, onto, "initial commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[2], "Pick", "Squash")
	drainIRMessages(t, h)
	if err := h.ui.Click("Edit Message " + irShort(hashes[1])); err != nil {
		t.Fatalf("click Edit Message: %v", err)
	}
	h.ui.Frame()
	ms := irMessagesState(t, h)
	wantDefault := "ir two\n\nir two body\n\nir three\n\nir three body"
	if ms.editText != wantDefault {
		t.Fatalf("SSH squash prefill = %q, want %q", ms.editText, wantDefault)
	}
	custom := "ssh combined\n\nssh combined body"
	replaceIRMessage(t, h, custom)
	if err := h.ui.Click("Done"); err != nil {
		t.Fatalf("click Done: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH squash rebase failed: %v", err)
	}
	if got := irSSHMessageAt(t, srv, repo.Path, "ssh combined"); got != custom {
		t.Fatalf("SSH squash message = %q, want %q", got, custom)
	}
	subjects := strings.Split(srv.RunGit(t, repo.Path, "log", "--format=%s", "main"), "\n")
	if !slices.Equal(subjects, []string{"ssh combined", "ir one", "initial commit"}) {
		t.Fatalf("SSH log after squash = %v", subjects)
	}
	assertIRDialogClosed(t, h)
}

// Over SSH a conflict on the reworded commit stops the rebase, and resolving
// it plus continuing still lands the edited multi-line message on the
// server.
func TestRebaseInteractiveMessagesConflictContinueLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	onto, _ := irRemoteMessages(t, srv, repo.Path)
	remoteCommitFile(t, srv, repo.Path, "conflict.txt", "one\n", "ir one\n\nir one body")
	remoteCommitFile(t, srv, repo.Path, "conflict.txt", "two\n", "ir two\n\nir two body")

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)
	openIRDialog(t, h, onto, "initial commit")
	drainIRPlan(t, h)

	rows := interactiveRebaseUI(h.app).rows
	var oneHash, twoHash string
	for _, row := range rows {
		switch row.subject {
		case "ir one":
			oneHash = row.hash
		case "ir two":
			twoHash = row.hash
		}
	}
	if oneHash == "" || twoHash == "" {
		t.Fatal("fixture rows missing")
	}
	chooseIRAction(t, h, oneHash, "Pick", "Drop")
	chooseIRAction(t, h, twoHash, "Pick", "Reword")
	drainIRMessages(t, h)
	edited := "ssh conflicted reword\n\nssh body survives"
	replaceIRMessage(t, h, edited)
	if err := h.ui.Click("Done"); err != nil {
		t.Fatalf("click Done: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH rebase to the conflict failed: %v", err)
	}
	st := h.app.session.Snapshot().Status
	if st.State != "rebase" || !st.Conflicted() {
		t.Fatalf("fixture: no SSH conflict stop: %+v", st)
	}

	srv.WriteFile(t, repo.Path, "conflict.txt", []byte("two\n"))
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Mark Resolved conflict.txt"); err != nil {
		t.Fatalf("click Mark Resolved conflict.txt: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if err := h.ui.Click("Continue Operation"); err != nil {
		t.Fatalf("click Continue Operation: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH continue after the conflict failed: %v", err)
	}
	if got := irSSHMessageAt(t, srv, repo.Path, "ssh conflicted reword"); got != edited {
		t.Fatalf("SSH message after conflict continue = %q, want byte-exact %q", got, edited)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after SSH continue = %q, want empty", got)
	}
	assertIRDialogClosed(t, h)
}

// Retrying a failed message read that fails a second time keeps the form
// blocked cleanly instead of crashing on a nil failure map, and a subsequent
// retry still loads the original message.
func TestRebaseInteractiveMessagesRepeatedRetryFailureDoesNotPanic(t *testing.T) {
	repo := newGitRepo(t, "irmsg-retry-twice")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one")
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	installFailFirstExecutor(t, h, isMessageLogCommand, errors.New("first injected read failure"))
	chooseIRAction(t, h, hashes[0], "Pick", "Reword")
	drainIRMessages(t, h)
	if !hasExactText(h.ui, "first injected read failure") {
		t.Fatalf("first failure text missing; texts=%q", h.ui.Texts())
	}

	// Second consecutive failure after clicking Retry Message.
	installFailFirstExecutor(t, h, isMessageLogCommand, errors.New("second injected read failure"))
	if err := h.ui.Click("Retry Message"); err != nil {
		t.Fatalf("click first Retry Message: %v", err)
	}
	drainIRMessages(t, h)
	if !hasExactText(h.ui, "second injected read failure") {
		t.Fatalf("second failure text missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || !btn.Disabled {
		t.Fatalf("Rebase after the second failed read = %+v found=%v, want disabled", btn, ok)
	}

	// Third attempt succeeds cleanly.
	if err := h.ui.Click("Retry Message"); err != nil {
		t.Fatalf("click second Retry Message: %v", err)
	}
	drainIRMessages(t, h)
	if ms := irMessagesState(t, h); ms.originals[hashes[0]] != "ir one\n\nir one body" {
		t.Fatalf("original after second retry = %q, want the complete message", ms.originals[hashes[0]])
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || btn.Disabled {
		t.Fatalf("Rebase after recovery = %+v found=%v, want enabled", btn, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	assertIRRepoUnchanged(t, repo, head, "main", "2")
}

// Message editing and the rebase options section share one modal with three
// independent asynchronous reads (plan, options, messages): enabling Backup
// and Update dependent branches alongside Reword and Squash completes with
// the backup ref created, the dependent branch moved, and both full messages
// preserved.
func TestRebaseInteractiveMessagesWithOptionsBackupAndUpdateRefs(t *testing.T) {
	repo := newGitRepo(t, "irmsg-with-options")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one", "ir two", "ir three")
	// A dependent branch anchored at ir two moves with the squashed tip when
	// update-refs is enabled.
	repo.Git("branch", "dep", hashes[1])
	headBefore := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[0], "Pick", "Reword")
	chooseIRAction(t, h, hashes[2], "Pick", "Squash")
	drainIRMessages(t, h)
	edited := "reworded with options\n\nreworded body"
	replaceIRMessage(t, h, edited)
	if err := h.ui.Click("Done"); err != nil {
		t.Fatalf("click Done: %v", err)
	}
	h.ui.Frame()

	if err := h.ui.Click("Create backup branch"); err != nil {
		t.Fatalf("click Create backup branch: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	updateChecked := false
	if cb, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Update dependent branches"); ok && !cb.Disabled {
		if err := h.ui.Click("Update dependent branches"); err != nil {
			t.Fatalf("click Update dependent branches: %v", err)
		}
		h.ui.Frame()
		updateChecked = true
	}

	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("combined options + messages rebase failed: %v", err)
	}
	if got := irMessageAt(t, repo, "reworded with options"); got != edited {
		t.Fatalf("reworded message = %q, want %q", got, edited)
	}
	wantSquash := "ir two\n\nir two body\n\nir three\n\nir three body"
	if got := irMessageAt(t, repo, "ir two"); got != wantSquash {
		t.Fatalf("squashed message = %q, want %q", got, wantSquash)
	}
	backupRef := "refs/backup/main-" + irShort(headBefore)
	if got := gitOutTrim(t, repo.Path(), "rev-parse", backupRef); got != headBefore {
		t.Fatalf("backup ref %s = %q, want %q", backupRef, got, headBefore)
	}
	if updateChecked {
		newTip := gitOutTrim(t, repo.Path(), "rev-parse", "main")
		if got := gitOutTrim(t, repo.Path(), "rev-parse", "dep"); got != newTip {
			t.Fatalf("dependent branch dep = %q, want replayed squashed tip %q", got, newTip)
		}
	}
	assertIRDialogClosed(t, h)
}
