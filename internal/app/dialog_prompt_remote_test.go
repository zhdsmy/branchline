package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/sshtest"
	"github.com/zhdsmy/branchline/internal/store"
)

// passAnswerProbe detours the rendered askpass question through a counting
// channel, so a test sees every answer the dialog delivers to the background
// worker — including a second delivery that must never happen. Call it right
// after waitAskPass rendered the question. The worker parked in wait keeps
// reading the original channel; forward passes exactly one answer on, and the
// registered cleanup forwards a cancel when the test never did, so no failure
// can leave the worker parked for teardown.
func passAnswerProbe(t *testing.T, h *testHarness) (replies chan passReply, forward func(passReply)) {
	t.Helper()
	st := syncStateFor(h.app)
	if st.pass.current == nil {
		t.Fatal("passAnswerProbe before the askpass question was rendered")
	}
	orig := st.pass.current
	replies = make(chan passReply, 8)
	st.pass.current = &passRequest{prompt: orig.prompt, reply: replies}
	var once sync.Once
	forward = func(r passReply) {
		once.Do(func() { orig.reply <- r })
	}
	t.Cleanup(func() { forward(passReply{}) })
	return replies, forward
}

// drainPassReplies returns every answer delivered so far without blocking.
func drainPassReplies(replies chan passReply) []passReply {
	var out []passReply
	for {
		select {
		case r := <-replies:
			out = append(out, r)
		default:
			return out
		}
	}
}

// hostKeyGateWrapper builds an ssh stand-in mimicking OpenSSH asking to trust
// a host it has never seen: invocations that cannot ask fail immediately with
// the host key verification error, while invocations with an askpass script
// in their environment are asked the yes/no question once through the real
// askpass pipe protocol and proceed only when the answer is yes.
func hostKeyGateWrapper(t *testing.T, srvSSH string) string {
	t.Helper()
	// The success branch removes the FIFO explicitly before exec: replacing
	// the process image never runs the EXIT trap, so relying on it alone
	// leaked one pipe per invocation.
	script := `#!/bin/sh
case "$SSH_ASKPASS_REQUIRE" in
force) : interactive ;;
*) echo "Host key verification failed." >&2; exit 255 ;;
esac
r=$(mktemp -u "${TMPDIR:-/tmp}/branchline-ap.XXXXXXXX")
mkfifo -m 600 "$r" || exit 1
trap 'rm -f "$r"' EXIT
trap 'exit 1' INT TERM HUP
printf '\036BRANCHLINE-ASKPASS L %s %s\n' "$r" "The authenticity of host 'gatehost' cannot be established. Are you sure you want to continue connecting (yes/no/[fingerprint])?" >&2
IFS= read -r a < "$r" || exit 1
case "$a" in
+yes) rm -f "$r"; exec ` + shellQ(srvSSH) + ` "$@" ;;
*) echo "Host key verification failed." >&2; exit 255 ;;
esac
`
	bin := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write host key gate wrapper: %v", err)
	}
	return filepath.Dir(bin)
}

// openHostKeyGatedRepo builds a loopback repository behind the host key gate
// and opens it in a fresh app. The gate fails promptless ssh invocations
// immediately, so the initial snapshot load ends in an error without ever
// asking — exactly like a server whose host key is not trusted yet.
func openHostKeyGatedRepo(t *testing.T) (store.Repo, *testHarness, *sshtest.Target) {
	t.Helper()
	repo, srv := loopbackSSHRepo(t)
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repo.Path, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repo.Path, "push", "-u", "origin", "main")

	gateDir := hostKeyGateWrapper(t, srv.SSHProgram())
	t.Setenv("PATH", gateDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err == nil {
		t.Fatal("expected initial load to fail behind the host key gate")
	}
	return repo, h, srv
}

// A password question opens with the focus inside the password field: typing
// and pressing Enter there submits the answer, the background worker receives
// exactly that value exactly once, and the fetch proceeds.
func TestDialogPromptRemotePasswordEnterAnswersOnceLoopbackSSH(t *testing.T) {
	const password = "hunter2t0ps3cret"
	_, h, _ := openPasswordGatedRepo(t, password)

	clickFetch(t, h)
	h.ui.Frame()
	waitAskPass(t, h)
	if !h.ui.HasText("The remote host requires password") {
		t.Fatalf("password dialog title missing; texts=%q", h.ui.Texts())
	}
	field, ok := h.ui.AccessByRole(ui.RoleTextField, "Password")
	if !ok {
		t.Fatalf("password field missing; texts=%q", h.ui.Texts())
	}
	if !field.Focused {
		t.Fatalf("password field is not focused when the dialog opens: %+v", field)
	}
	replies, forward := passAnswerProbe(t, h)

	h.ui.Type(password)
	h.ui.Frame()
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()

	got := drainPassReplies(replies)
	if len(got) != 1 {
		t.Fatalf("answers delivered for one Enter = %d (%+v), want exactly 1", len(got), got)
	}
	if !got[0].ok || got[0].answer != password {
		t.Fatalf("delivered answer = %+v, want the typed password with ok=true", got[0])
	}
	forward(got[0])
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("fetch with Enter-submitted password failed: %v", err)
	}
}

// Pressing Enter in the empty password field submits too, exactly once: the
// empty answer goes to ssh as it always did and the authentication fails.
func TestDialogPromptRemoteEmptyPasswordEnterAnswersOnceLoopbackSSH(t *testing.T) {
	_, h, _ := openPasswordGatedRepo(t, "hunter2t0ps3cret")

	clickFetch(t, h)
	h.ui.Frame()
	waitAskPass(t, h)
	replies, forward := passAnswerProbe(t, h)

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()

	got := drainPassReplies(replies)
	if len(got) != 1 {
		t.Fatalf("answers delivered for one Enter in the empty field = %d (%+v), want exactly 1", len(got), got)
	}
	if !got[0].ok || got[0].answer != "" {
		t.Fatalf("delivered answer = %+v, want the empty answer with ok=true", got[0])
	}
	forward(got[0])
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("expected ActionErr: the empty password cannot pass the gate")
	}
}

// testDialogPromptRemoteCancelLoopbackSSH drives a password question and
// lets cancel close it: the background worker receives exactly one cancel
// answer, ok=false, and the fetch ends in an error without hanging.
func testDialogPromptRemoteCancelLoopbackSSH(t *testing.T, cancel func(t *testing.T, h *testHarness)) {
	t.Helper()
	_, h, _ := openPasswordGatedRepo(t, "hunter2t0ps3cret")

	clickFetch(t, h)
	h.ui.Frame()
	waitAskPass(t, h)
	replies, forward := passAnswerProbe(t, h)

	cancel(t, h)
	h.ui.Frame()

	got := drainPassReplies(replies)
	if len(got) != 1 {
		t.Fatalf("answers delivered for one dismissal = %d (%+v), want exactly 1", len(got), got)
	}
	if got[0].ok || got[0].answer != "" {
		t.Fatalf("delivered answer = %+v, want a cancel (\"\", false)", got[0])
	}
	forward(got[0])
	h.drain()

	if h.app.session.Busy() {
		t.Fatal("session stayed busy after the cancelled password question")
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("expected ActionErr after the cancelled password-gated fetch")
	}
}

// Clicking Cancel in the password dialog answers the background worker with
// cancel exactly once.
func TestDialogPromptRemotePasswordCancelButtonLoopbackSSH(t *testing.T) {
	testDialogPromptRemoteCancelLoopbackSSH(t, func(t *testing.T, h *testHarness) {
		if err := h.ui.Click("Cancel"); err != nil {
			t.Fatalf("click Cancel in the password dialog: %v", err)
		}
	})
}

// Pressing Escape in the password dialog answers the background worker with
// cancel exactly once.
func TestDialogPromptRemotePasswordCancelEscapeLoopbackSSH(t *testing.T) {
	testDialogPromptRemoteCancelLoopbackSSH(t, func(t *testing.T, h *testHarness) {
		h.ui.Key(0, ui.KeyEscape)
	})
}

// The host confirmation is destructive: its Connect button paints in the
// danger color, and Enter — landing on the focused cancel — never answers
// yes. Only clicking Connect delivers yes exactly once and connects; Escape
// answers cancel exactly once.
func TestDialogPromptRemoteHostConfirmLoopbackSSH(t *testing.T) {
	_, h, _ := openHostKeyGatedRepo(t)

	// Round one: Enter on the open dialog must not connect.
	clickFetch(t, h)
	h.ui.Frame()
	waitAskPass(t, h)
	if !h.ui.HasText("Connect to this host?") {
		t.Fatalf("host confirmation title missing; texts=%q", h.ui.Texts())
	}
	if fill := buttonFillSample(t, h.ui, "Connect"); !fillWithin(fill, h.app.pal.danger, 12) {
		t.Fatalf("Connect resting fill = %v, want within 12 of the danger color %v", fill, h.app.pal.danger)
	}
	replies, forward := passAnswerProbe(t, h)

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()

	got := drainPassReplies(replies)
	if len(got) != 1 {
		t.Fatalf("answers delivered for one Enter = %d (%+v), want exactly 1 cancel", len(got), got)
	}
	if got[0].ok && got[0].answer == "yes" {
		t.Fatal("Enter on the host confirmation answered yes — the destructive protection is missing")
	}
	if got[0].ok || got[0].answer != "" {
		t.Fatalf("Enter delivered %+v, want the focused cancel's (\"\", false)", got[0])
	}
	forward(got[0])
	h.drain()
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("expected ActionErr: Enter must not connect to the untrusted host")
	}
	if err := h.ui.Click("Dismiss Error"); err != nil {
		t.Fatalf("click Dismiss Error: %v", err)
	}
	h.ui.Frame()

	// Round two: clicking Connect answers yes exactly once and connects.
	clickFetch(t, h)
	h.ui.Frame()
	waitAskPass(t, h)
	replies, forward = passAnswerProbe(t, h)
	if err := h.ui.Click("Connect"); err != nil {
		t.Fatalf("click Connect in the host confirmation: %v", err)
	}
	h.ui.Frame()

	got = drainPassReplies(replies)
	if len(got) != 1 {
		t.Fatalf("answers delivered for one Connect click = %d (%+v), want exactly 1", len(got), got)
	}
	if !got[0].ok || got[0].answer != "yes" {
		t.Fatalf("Connect delivered %+v, want yes with ok=true", got[0])
	}
	forward(got[0])
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("fetch after clicking Connect failed: %v", err)
	}

	// Round three: Escape answers cancel exactly once.
	clickFetch(t, h)
	h.ui.Frame()
	waitAskPass(t, h)
	replies, forward = passAnswerProbe(t, h)
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()

	got = drainPassReplies(replies)
	if len(got) != 1 {
		t.Fatalf("answers delivered for one Escape = %d (%+v), want exactly 1", len(got), got)
	}
	if got[0].ok || got[0].answer != "" {
		t.Fatalf("Escape delivered %+v, want a cancel (\"\", false)", got[0])
	}
	forward(got[0])
	h.drain()
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("expected ActionErr: Escape must not connect to the untrusted host")
	}
}

// Enter in the local branch name field of Checkout Remote Branch submits
// exactly like the Checkout button — the tracking branch is created and
// checked out — while an illegal name submits nothing at all.
func TestDialogPromptRemoteCheckoutEnterSubmits(t *testing.T) {
	repo := remoteMenuFixture(t, "dpr-checkout-enter")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Checkout…"); err != nil {
		t.Fatalf("choose Checkout…: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Checkout Remote Branch") {
		t.Fatalf("checkout dialog title missing; texts=%q", h.ui.Texts())
	}

	// An illegal name disables Checkout, and Enter in the field submits
	// nothing: no branch appears, HEAD stays on main, and the dialog stays.
	clearInput(t, h.ui)
	h.ui.Type("bad..name")
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout Remote Branch"); !ok || !btn.Disabled {
		t.Fatalf("confirm with illegal name = %+v, want disabled", btn)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()
	if branchExists(t, repo, "bad..name") {
		t.Fatal("Enter with an illegal name created the branch")
	}
	if !hasExactText(h.ui, "Checkout Remote Branch") {
		t.Fatalf("checkout dialog closed after Enter with an illegal name; texts=%q", h.ui.Texts())
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter with an illegal name ran a write that failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("current branch after the refused Enter = %q, want main", got)
	}

	// A legal name submits once through the same key: the tracking branch is
	// created and checked out.
	clearInput(t, h.ui)
	h.ui.Type("feat-enter")
	h.ui.Frame()
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout submitted by Enter failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "feat-enter" {
		t.Fatalf("current branch after Enter = %q, want feat-enter", got)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "--abbrev-ref", "feat-enter@{upstream}"); got != "origin/feature" {
		t.Fatalf("feat-enter upstream = %q, want origin/feature", got)
	}
	if hasExactText(h.ui, "Checkout Remote Branch") {
		t.Fatal("checkout dialog stayed open after Enter submitted it")
	}
}

// Delete Remote Branch is destructive: its Delete button paints in the danger
// color, and pressing Enter — landing on the focused cancel — closes the
// dialog without deleting the branch on the server.
func TestDialogPromptRemoteDeleteRemoteBranchEnterKeeps(t *testing.T) {
	repo := remoteMenuFixture(t, "dpr-rb-delete-enter")
	bare := strings.TrimSpace(repo.Git("remote", "get-url", "origin"))

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Delete 'origin/feature'…"); err != nil {
		t.Fatalf("choose Delete item: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Delete Remote Branch") {
		t.Fatalf("delete dialog title missing; texts=%q", h.ui.Texts())
	}
	if fill := buttonFillSample(t, h.ui, "Confirm Delete Remote Branch"); !fillWithin(fill, h.app.pal.danger, 12) {
		t.Fatalf("Delete resting fill = %v, want within 12 of the danger color %v", fill, h.app.pal.danger)
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Remote Branch"); !ok || btn.Disabled {
		t.Fatalf("Confirm Delete Remote Branch = %+v (found=%v), want enabled", btn, ok)
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	if branchListMissing(t, bare, "feature") {
		t.Fatal("Enter deleted the remote branch — the destructive protection is missing")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter ran a write that failed: %v", err)
	}
	if hasExactText(h.ui, "Delete Remote Branch") {
		t.Fatal("delete dialog stayed open after Enter landed on its cancel")
	}
	if branchListMissing(t, bare, "feature") {
		t.Fatal("the remote branch is gone after the canceled delete")
	}
}

// Delete Tag from Remote renders its confirm button disabled while the probe
// runs — present, not hidden — and paints it in the danger color once the
// probe found the tag; pressing Enter then still deletes nothing, because the
// destructive confirmation never answers to Enter.
func TestDialogPromptRemoteDeleteTagEnterKeeps(t *testing.T) {
	repo, bare, _, _ := tagsPushSidebarRepo(t, "dpr-tag-rmdel-enter")
	repo.Git("push", "origin", "refs/tags/lw")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	h.queue.PauseWorkers()
	defer h.queue.ResumeWorkers()
	chooseTagMenuItem(t, h, "lw", "Delete from Remote…", "Delete Tag from Remote")
	probe := h.queue.WaitWorker(t)
	h.ui.Frame()

	if !h.ui.HasText("Checking remote tag on origin") {
		t.Fatalf("checking note missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Remote Tag"); !ok || !btn.Disabled {
		t.Fatalf("Confirm Delete Remote Tag while checking = %+v (found=%v), want present and disabled", btn, ok)
	}

	h.queue.ReleaseWorker(probe)
	h.queue.ResumeWorkers()
	h.drain()

	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Remote Tag"); !ok || btn.Disabled {
		t.Fatalf("Confirm Delete Remote Tag after the probe = %+v (found=%v), want enabled", btn, ok)
	}
	if fill := buttonFillSample(t, h.ui, "Confirm Delete Remote Tag"); !fillWithin(fill, h.app.pal.danger, 12) {
		t.Fatalf("Delete from Remote resting fill = %v, want within 12 of the danger color %v", fill, h.app.pal.danger)
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	if !bareTagExists(t, bare, "lw") {
		t.Fatal("Enter deleted the remote tag — the destructive protection is missing")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter ran a write that failed: %v", err)
	}
	if hasExactText(h.ui, "Delete Tag from Remote") {
		t.Fatal("the dialog stayed open after Enter landed on its cancel")
	}
	if !bareTagExists(t, bare, "lw") {
		t.Fatal("the remote tag is gone after the canceled delete")
	}
}
