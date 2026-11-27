package app

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/sshtest"
	"github.com/zhdsmy/branchline/internal/store"
)

// pushUnreachableTagLocal clones bareDir into a peer, records a commit no
// remote branch ever contains, tags it v-unreach and pushes only the tag,
// then advances main with a second commit. It returns the advancing main tip
// and the tagged commit's hash. The tag is unreachable from every remote
// branch, so only a fetch that asks for tags brings it in.
func pushUnreachableTagLocal(t *testing.T, bareDir string) (mainTip, tagged string) {
	t.Helper()
	peerDir := filepath.Join(t.TempDir(), "peer")
	runGitCommand(t, "", "clone", bareDir, peerDir)
	runGitCommand(t, peerDir, "config", "user.name", "Peer")
	runGitCommand(t, peerDir, "config", "user.email", "peer@branchline.local")
	runGitCommand(t, peerDir, "checkout", "-q", "--detach", "HEAD")
	if err := os.WriteFile(filepath.Join(peerDir, "orphan.txt"), []byte("orphan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, peerDir, "add", "--", "orphan.txt")
	runGitCommand(t, peerDir, "commit", "-q", "-m", "orphan tagged commit")
	tagged = strings.TrimSpace(runGitCommand(t, peerDir, "rev-parse", "HEAD"))
	runGitCommand(t, peerDir, "tag", "v-unreach")
	runGitCommand(t, peerDir, "push", "-q", "origin", "refs/tags/v-unreach")
	runGitCommand(t, peerDir, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(peerDir, "advance.txt"), []byte("advance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, peerDir, "add", "--", "advance.txt")
	runGitCommand(t, peerDir, "commit", "-q", "-m", "peer advance")
	runGitCommand(t, peerDir, "push", "-q", "origin", "main")
	mainTip = strings.TrimSpace(runGitCommand(t, peerDir, "rev-parse", "refs/heads/main"))
	if out, err := tryGitCommand(bareDir, "merge-base", "--is-ancestor", tagged, "refs/heads/main"); err == nil {
		t.Fatalf("fixture broken: tagged commit %s is reachable from main (%s)", tagged, out)
	}
	return mainTip, tagged
}

// The toolbar Fetch honors the Fetch Tags preference against a real bare
// remote: off, the unreachable tag of the remote stays away while the branch
// updates arrive; saved on — and still on after the store file is reopened —
// the next Fetch brings the tag into the repository and the sidebar.
func TestDisplayCommitPrefsFetchTagsLocal(t *testing.T) {
	repo := newGitRepo(t, "prefs-fetch-tags")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	mainTip, tagged := pushUnreachableTagLocal(t, bareDir)

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	beforeTip := strings.TrimSpace(repo.Git("rev-parse", "origin/main"))
	if beforeTip == mainTip {
		t.Fatal("fixture broken: origin/main already at the peer tip before fetching")
	}

	// Fetch with the preference off (the default): the remote branch update
	// arrives, but the unreachable tag does not.
	clickFetch(t, h)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("first fetch failed: %v", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "origin/main")); got != mainTip {
		t.Fatalf("after Fetch origin/main = %q, want %q: branch updates must still arrive", got, mainTip)
	}
	if got := strings.TrimSpace(repo.Git("tag", "-l", "v-unreach")); got != "" {
		t.Fatalf("tag v-unreach fetched with the preference off: %q", got)
	}
	if h.ui.HasText("v-unreach") {
		t.Fatalf("tag v-unreach visible in the sidebar with the preference off; texts=%q", h.ui.Texts())
	}

	// Turn the preference on and prove it persisted: a store reopened from the
	// same file still reads FetchTags on.
	if err := h.app.saveFetchTags(true); err != nil {
		t.Fatalf("saveFetchTags(true): %v", err)
	}
	reopened, err := store.Open(h.store.Path())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if !reopened.Settings().FetchTags {
		t.Fatal("FetchTags not persisted: reopened store reads it off")
	}

	clickFetch(t, h)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("second fetch failed: %v", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "refs/tags/v-unreach")); got != tagged {
		t.Fatalf("after Fetch with tags refs/tags/v-unreach = %q, want the tagged commit %q", got, tagged)
	}
	if !h.ui.HasText("v-unreach") {
		t.Fatalf("tag v-unreach missing from the sidebar after fetching tags; texts=%q", h.ui.Texts())
	}
}

// pushUnreachableTagSSH is pushUnreachableTagLocal for a server: every step
// runs on the SSH host through the shell, in a peer clone of the server's
// bare repository.
func pushUnreachableTagSSH(t *testing.T, srv *sshtest.Target, bareDir string) (mainTip, tagged string) {
	t.Helper()
	peerDir := srv.TempDir(t, "peer")
	srv.RunShell(t, "set -eu\nexport GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 LC_ALL=C\n"+
		"git clone -q "+shellQ(bareDir)+" "+shellQ(peerDir)+"\n"+
		"cd "+shellQ(peerDir)+"\n"+
		"git config user.name Peer\ngit config user.email peer@branchline.invalid\n"+
		"git checkout -q --detach HEAD\n"+
		"printf 'orphan\\n' > orphan.txt\ngit add -- orphan.txt\ngit commit -q -m 'orphan tagged commit'\n"+
		"git tag v-unreach\n"+
		"git push -q origin refs/tags/v-unreach\n"+
		"git checkout -q main\n"+
		"printf 'advance\\n' > advance.txt\ngit add -- advance.txt\ngit commit -q -m 'peer advance'\n"+
		"git push -q origin main\n")
	mainTip = srv.RunGit(t, bareDir, "rev-parse", "refs/heads/main")
	tagged = srv.RunGit(t, bareDir, "rev-parse", "refs/tags/v-unreach")
	for _, h := range strings.Fields(srv.RunGit(t, bareDir, "rev-list", "refs/heads/main")) {
		if h == tagged {
			t.Fatalf("fixture broken: tagged commit %s is reachable from main", tagged)
		}
	}
	return mainTip, tagged
}

// The same Fetch Tags behavior over loopback SSH, verified server-side: the
// toolbar Fetch of the SSH repository honors the preference for the server's
// own tag namespace, and the sidebar follows.
func TestDisplayCommitPrefsFetchTagsLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repoDir, "push", "-u", "origin", "main")
	mainTip, tagged := pushUnreachableTagSSH(t, srv, bareDir)

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	beforeTip := srv.RunGit(t, repoDir, "rev-parse", "origin/main")
	if beforeTip == mainTip {
		t.Fatal("fixture broken: server origin/main already at the peer tip before fetching")
	}

	clickFetch(t, h)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("first SSH fetch failed: %v", err)
	}
	if got := srv.RunGit(t, repoDir, "rev-parse", "origin/main"); got != mainTip {
		t.Fatalf("after SSH Fetch origin/main = %q, want %q: branch updates must still arrive", got, mainTip)
	}
	if got := srv.RunGit(t, repoDir, "tag", "-l", "v-unreach"); got != "" {
		t.Fatalf("server tag v-unreach fetched with the preference off: %q", got)
	}
	if h.ui.HasText("v-unreach") {
		t.Fatalf("tag v-unreach visible in the sidebar with the preference off; texts=%q", h.ui.Texts())
	}

	if err := h.app.saveFetchTags(true); err != nil {
		t.Fatalf("saveFetchTags(true): %v", err)
	}
	reopened, err := store.Open(h.store.Path())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if !reopened.Settings().FetchTags {
		t.Fatal("FetchTags not persisted: reopened store reads it off")
	}

	clickFetch(t, h)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("second SSH fetch failed: %v", err)
	}
	if got := srv.RunGit(t, repoDir, "rev-parse", "refs/tags/v-unreach"); got != tagged {
		t.Fatalf("after SSH Fetch with tags refs/tags/v-unreach = %q, want %q", got, tagged)
	}
	if !h.ui.HasText("v-unreach") {
		t.Fatalf("tag v-unreach missing from the sidebar after fetching tags over SSH; texts=%q", h.ui.Texts())
	}
}

// saveFetchTags rolls a failed write back: the in-memory preference keeps
// its previous value, and once the write block is gone the retry persists.
func TestDisplayCommitPrefsSaveFetchTagsRollback(t *testing.T) {
	h := newTestApp(t)

	if err := h.app.saveFetchTags(true); err != nil {
		t.Fatalf("saveFetchTags(true): %v", err)
	}
	tmp := h.store.Path() + ".tmp"
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatalf("block store writes: %v", err)
	}
	if err := h.app.saveFetchTags(false); err == nil {
		t.Fatal("saveFetchTags succeeded with the store write blocked")
	}
	if got := h.store.Settings().FetchTags; !got {
		t.Fatal("failed saveFetchTags changed the in-memory preference")
	}
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("unblock store writes: %v", err)
	}
	if err := h.app.saveFetchTags(false); err != nil {
		t.Fatalf("retry saveFetchTags(false): %v", err)
	}
	reopened, err := store.Open(h.store.Path())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if reopened.Settings().FetchTags {
		t.Fatal("reopened store still reads FetchTags on after the retry")
	}
}

// normalizedDiffFontSize leaves a zero, negative or non-finite stored value
// at the views' built-in default and clamps positive ones into the accepted
// range, so a hand-edited store can never break a view's layout.
func TestDisplayCommitPrefsNormalizedDiffFontSize(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   float64
		want float32
	}{
		{"zero is default", 0, 0},
		{"negative is default", -3, 0},
		{"NaN is default", math.NaN(), 0},
		{"positive infinity is default", math.Inf(1), 0},
		{"negative infinity is default", math.Inf(-1), 0},
		{"below range clamps up", 5, 10},
		{"lower edge kept", 10, 10},
		{"fractional kept", 12.5, 12.5},
		{"upper edge kept", 24, 24},
		{"above range clamps down", 30, 24},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizedDiffFontSize(store.Settings{DiffFontSize: tc.in}); got != tc.want {
				t.Fatalf("normalizedDiffFontSize(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// The Blame view consumes the code font size preference: untouched settings
// keep the row at exactly the built-in 20px, a saved larger size grows the
// real rendered row so the face does not clip, and restoring the default
// shrinks it back.
func TestDisplayCommitPrefsBlameFontSizeRows(t *testing.T) {
	repo := newGitRepo(t, "prefs-blame-font")
	repo.CommitFile("code.txt", "alpha\nbeta\ngamma\n", "create code")
	repo.WriteFile("code.txt", "alpha\nbeta\ngamma\nchanged\n")

	h := openChangesHarness(t, repo)
	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	h.drain()

	blameRowHeight := func() float32 {
		t.Helper()
		row, ok := h.ui.AccessByRole(ui.RoleListItem, "Blame Line 1")
		if !ok {
			t.Fatalf("blame row missing; texts=%q", h.ui.Texts())
		}
		return row.Bounds.H
	}

	if got := blameRowHeight(); got != 20 {
		t.Fatalf("default blame row height = %v, want exactly 20 (zero drift)", got)
	}

	if err := h.app.saveDiffFontSize(20); err != nil {
		t.Fatalf("saveDiffFontSize(20): %v", err)
	}
	h.ui.Frame()
	if got := blameRowHeight(); got <= 20 {
		t.Fatalf("blame row height with size 20 = %v, want growth beyond 20", got)
	} else if got != 28.5 {
		t.Fatalf("blame row height with size 20 = %v, want exactly 28.5 (size + leading)", got)
	}

	if err := h.app.saveDiffFontSize(0); err != nil {
		t.Fatalf("saveDiffFontSize(0): %v", err)
	}
	h.ui.Frame()
	if got := blameRowHeight(); got != 20 {
		t.Fatalf("blame row height after restoring the default = %v, want exactly 20", got)
	}
}

// saveDiffFontSize rejects sizes outside the accepted range and non-finite
// ones without touching the store, persists the edge sizes and the explicit
// zero of restoring the default across a reopen, and rolls a failed write
// back.
func TestDisplayCommitPrefsSaveDiffFontSizeValidationAndRollback(t *testing.T) {
	h := newTestApp(t)

	before := h.store.Settings()
	for _, size := range []float64{9.99, 24.01, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := h.app.saveDiffFontSize(size); err == nil {
			t.Fatalf("size %v accepted", size)
		}
	}
	if got := h.store.Settings(); !reflect.DeepEqual(got, before) {
		t.Fatalf("rejected sizes changed the settings: got %+v want %+v", got, before)
	}

	for _, size := range []float64{10, 24} {
		if err := h.app.saveDiffFontSize(size); err != nil {
			t.Fatalf("saveDiffFontSize(%v): %v", size, err)
		}
		reopened, err := store.Open(h.store.Path())
		if err != nil {
			t.Fatalf("reopen store: %v", err)
		}
		if got := reopened.Settings().DiffFontSize; got != size {
			t.Fatalf("reopened DiffFontSize = %v, want %v", got, size)
		}
	}

	if err := h.app.saveDiffFontSize(0); err != nil {
		t.Fatalf("saveDiffFontSize(0): %v", err)
	}
	reopened, err := store.Open(h.store.Path())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if got := reopened.Settings().DiffFontSize; got != 0 {
		t.Fatalf("restored default not persisted: reopened DiffFontSize = %v, want 0", got)
	}

	tmp := h.store.Path() + ".tmp"
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatalf("block store writes: %v", err)
	}
	if err := h.app.saveDiffFontSize(12); err == nil {
		t.Fatal("saveDiffFontSize succeeded with the store write blocked")
	}
	if got := h.store.Settings().DiffFontSize; got != 0 {
		t.Fatalf("failed saveDiffFontSize changed the size to %v", got)
	}
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("unblock store writes: %v", err)
	}
	if err := h.app.saveDiffFontSize(12); err != nil {
		t.Fatalf("retry saveDiffFontSize(12): %v", err)
	}
	if got := h.store.Settings().DiffFontSize; got != 12 {
		t.Fatalf("retry did not persist: DiffFontSize = %v, want 12", got)
	}
}

// commitMessageWarning measures the subject in Unicode characters against
// the soft and hard limits — the hard one is severe, either can be turned
// off with an explicit zero — and the description lines against the page
// guide; CRLF line endings never count their carriage return.
func TestDisplayCommitPrefsCommitMessageWarning(t *testing.T) {
	prefs := store.Settings{SubjectLow: 50, SubjectHigh: 70, PageGuide: 72}
	runes := func(n int, r string) string { return strings.Repeat(r, n) }

	for _, tc := range []struct {
		name       string
		message    string
		prefs      store.Settings
		wantText   string
		wantSevere bool
	}{
		{"empty draft", "", prefs, "", false},
		{"blank draft", "  \n \n", prefs, "", false},
		{"short subject", "fix the typo", prefs, "", false},
		{"subject at soft limit", runes(50, "x"), prefs, "", false},
		{"subject at hard limit", runes(70, "x"), store.Settings{SubjectLow: 0, SubjectHigh: 70, PageGuide: 72}, "", false},
		{"soft warning", runes(51, "x"), prefs, "Subject is 51 characters, past the soft limit of 50.", false},
		{"hard warning is severe", runes(71, "x"), prefs, "Subject is 71 characters, past the limit of 70.", true},
		{"unicode counted in characters", runes(51, "界"), prefs, "Subject is 51 characters, past the soft limit of 50.", false},
		{"unicode at boundary", runes(50, "界"), prefs, "", false},
		{"crlf carriage return not counted", runes(50, "x") + "\r\nbody", prefs, "", false},
		{"soft limit off allows soft length", runes(60, "x"), store.Settings{SubjectLow: 0, SubjectHigh: 70, PageGuide: 72}, "", false},
		{"hard limit off falls back to soft", runes(71, "x"), store.Settings{SubjectLow: 50, SubjectHigh: 0, PageGuide: 72}, "Subject is 71 characters, past the soft limit of 50.", false},
		{"both limits off", runes(100, "x"), store.Settings{SubjectLow: 0, SubjectHigh: 0, PageGuide: 72}, "", false},
		{"description line at guide", "subject\n\n" + runes(72, "x"), prefs, "", false},
		{"description past guide", "subject\n\n" + runes(73, "x"), prefs, "A description line is past the 72 character guide.", false},
		{"unicode description counted in characters", "subject\n\n" + runes(73, "界"), prefs, "A description line is past the 72 character guide.", false},
		{"description crlf carriage return not counted", "subject\n\n" + runes(72, "x") + "\r", prefs, "", false},
		{"page guide off", "subject\n\n" + runes(100, "x"), store.Settings{SubjectLow: 50, SubjectHigh: 70, PageGuide: 72, NoPageGuide: true}, "", false},
		{"subject warning precedes description", runes(60, "x") + "\n\n" + runes(100, "x"), prefs, "Subject is 60 characters, past the soft limit of 50.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, severe := commitMessageWarning(tc.message, tc.prefs)
			if text != tc.wantText || severe != tc.wantSevere {
				t.Fatalf("commitMessageWarning = (%q, %v), want (%q, %v)", text, severe, tc.wantText, tc.wantSevere)
			}
		})
	}
}

// saveCommitPrefs persists the commit message rules — explicit zeros stay
// zero across a reopen because the saved flags survive — rejects inverted or
// negative limits and a positive-guide requirement without mutating the
// store, and rolls a failed write back.
func TestDisplayCommitPrefsSaveCommitPrefs(t *testing.T) {
	h := newTestApp(t)

	if err := h.app.saveCommitPrefs(40, 60, 80, false, true); err != nil {
		t.Fatalf("saveCommitPrefs(40, 60, 80): %v", err)
	}
	reopened, err := store.Open(h.store.Path())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	got := reopened.Settings()
	if got.SubjectLow != 40 || !got.SubjectLowSaved || got.SubjectHigh != 60 || !got.SubjectHighSaved || got.PageGuide != 80 || got.NoPageGuide || !got.SignedOffBy {
		t.Fatalf("reopened commit prefs = %+v, want 40/60/80 with signed-off-by on", got)
	}

	// Explicit zeros disable the subject limits and survive the reopen:
	// without the saved flags the defaults of 50/70 would come back.
	if err := h.app.saveCommitPrefs(0, 0, 72, false, false); err != nil {
		t.Fatalf("saveCommitPrefs(0, 0, 72): %v", err)
	}
	reopened, err = store.Open(h.store.Path())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	got = reopened.Settings()
	if got.SubjectLow != 0 || got.SubjectHigh != 0 {
		t.Fatalf("explicit zero limits = %d/%d after reopen, want 0/0 (defaults would be 50/70)", got.SubjectLow, got.SubjectHigh)
	}

	// Validation failures never mutate the store.
	before := h.store.Settings()
	for _, tc := range []struct {
		name             string
		low, high, guide int
		noGuide, signoff bool
	}{
		{"negative soft limit", -1, 70, 72, false, false},
		{"negative hard limit", 50, -1, 72, false, false},
		{"soft past hard", 71, 50, 72, false, false},
		{"guide on without a column", 50, 70, 0, false, false},
		{"negative guide while on", 50, 70, -4, false, false},
	} {
		if err := h.app.saveCommitPrefs(tc.low, tc.high, tc.guide, tc.noGuide, tc.signoff); err == nil {
			t.Fatalf("%s accepted", tc.name)
		}
	}
	if got := h.store.Settings(); !reflect.DeepEqual(got, before) {
		t.Fatalf("rejected commit prefs changed the settings: got %+v want %+v", got, before)
	}

	// Edge combinations are legal: only one limit chosen, an inverted pair
	// with one side off, and no guide column while the guide is off.
	for _, tc := range []struct {
		name             string
		low, high, guide int
		noGuide, signoff bool
	}{
		{"soft off hard on", 0, 60, 72, false, false},
		{"inversion ignored with hard off", 60, 0, 72, false, false},
		{"no guide column while off", 50, 70, 0, true, false},
	} {
		if err := h.app.saveCommitPrefs(tc.low, tc.high, tc.guide, tc.noGuide, tc.signoff); err != nil {
			t.Fatalf("%s rejected: %v", tc.name, err)
		}
	}

	// A failed write rolls the in-memory settings back.
	if err := h.app.saveCommitPrefs(40, 60, 80, false, true); err != nil {
		t.Fatalf("re-establish baseline: %v", err)
	}
	before = h.store.Settings()
	tmp := h.store.Path() + ".tmp"
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatalf("block store writes: %v", err)
	}
	if err := h.app.saveCommitPrefs(30, 90, 100, true, false); err == nil {
		t.Fatal("saveCommitPrefs succeeded with the store write blocked")
	}
	if got := h.store.Settings(); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed saveCommitPrefs changed the settings: got %+v want %+v", got, before)
	}
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("unblock store writes: %v", err)
	}
	if err := h.app.saveCommitPrefs(30, 90, 100, true, false); err != nil {
		t.Fatalf("retry saveCommitPrefs: %v", err)
	}
	reopened, err = store.Open(h.store.Path())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	got = reopened.Settings()
	if got.SubjectLow != 30 || got.SubjectHigh != 90 || got.PageGuide != 100 || !got.NoPageGuide || got.SignedOffBy {
		t.Fatalf("retried commit prefs = %+v, want 30/90/100 with the guide off", got)
	}
}
