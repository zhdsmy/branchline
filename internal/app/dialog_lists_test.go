package app

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// dialogIRFixture builds the linear chain of total commits on main, commit i
// adding its own file ir<i>.txt so replaying the commits in any order applies
// without conflicts, and returns the hash of the oldest commit: the base of
// the interactive rebase whose plan is the other total-1 commits.
func dialogIRFixture(t *testing.T, name string, total int) (*gitRepoFixture, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	dialogFastImport(t, repo, total, func(i int) [][2]string {
		return [][2]string{{fmt.Sprintf("ir%d.txt", i), fmt.Sprintf("ir content %d\n", i)}}
	}, func(i int) string {
		return fmt.Sprintf("ir commit %d", i)
	})
	// fast-import writes objects and refs only: check the tree out so the
	// working tree and the index match the tip, as a clone would leave them.
	repo.Git("reset", "--hard", "-q", "main")
	onto := strings.TrimSpace(repo.Git("rev-list", "--max-parents=0", "main"))
	return repo, onto
}

// dialogFastImport feeds git fast-import one stream of n commits on
// refs/heads/main, chaining each commit on the previous one: thousands of
// commits in a fraction of a second, where one git commit per shell-out
// would take minutes. files returns the paths and contents commit i writes,
// subject its message.
func dialogFastImport(t *testing.T, repo *gitRepoFixture, n int, files func(i int) [][2]string, subject func(i int) string) {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		msg := subject(i)
		fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\n", i)
		fmt.Fprintf(&b, "author Branchline Test <test@branchline.local> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "committer Branchline Test <test@branchline.local> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
		if i > 1 {
			fmt.Fprintf(&b, "from :%d\n", i-1)
		}
		for _, f := range files(i) {
			fmt.Fprintf(&b, "M 100644 inline %s\ndata %d\n%s\n", f[0], len(f[1]), f[1])
		}
	}
	cmd := exec.Command("git", "fast-import", "--quiet")
	cmd.Dir = repo.Path()
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	cmd.Stdin = strings.NewReader(b.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fast-import: %v\n%s", err, out)
	}
}

// dialogIRHash returns the full hash of the commit with subject "ir commit n"
// in repo, read independently through git rev-list.
func dialogIRHash(t *testing.T, repo *gitRepoFixture, n int) string {
	t.Helper()
	subject := fmt.Sprintf("ir commit %d", n)
	out := gitOutTrim(t, repo.Path(), "log", "--format=%H %s", "main")
	for _, line := range strings.Split(out, "\n") {
		hash, subj, _ := strings.Cut(line, " ")
		if subj == subject {
			return hash
		}
	}
	t.Fatalf("no commit with subject %q", subject)
	return ""
}

// openDialogIR loads enough history pages for rows+1 commits, reveals the
// oldest commit row through the focused list's End key, and opens the
// interactive rebase form from its real context menu.
func openDialogIR(t *testing.T, repo *gitRepoFixture, onto string, rows int) *testHarness {
	t.Helper()
	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	h.drain()
	for len(h.app.session.Snapshot().Commits) < rows+1 {
		mustClick(t, h, "Load More")
		h.drain()
	}
	head := h.app.session.Snapshot().Status.Head
	if err := h.ui.Click(commitRowLabel(head, fmt.Sprintf("ir commit %d", rows+1))); err != nil {
		t.Fatalf("click newest commit row: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.ui.Key(0, ui.KeyEnd)
	h.ui.Frame()
	row := commitRowLabel(onto, "ir commit 1")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click oldest commit row: %v\ntexts=%q", err, h.ui.Texts())
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
	return h
}

// irScrollToBottom scrolls the form's plan list down until its last row is
// in view. The subject texts cannot tell the list's rows from the commit
// rows of the History page behind the modal, so the loop reads where the
// list itself is; the rows in view still render or not by the virtualization
// the test exercises.
func irScrollToBottom(t *testing.T, h *testHarness) {
	t.Helper()
	cx, cy := irRowsCenter(t, h)
	st := interactiveRebaseUI(h.app)
	n := len(st.rows)
	for i := 0; i < 60; i++ {
		if _, last := st.list.Visible(); last >= n-1 {
			return
		}
		h.ui.Move(cx, cy)
		h.ui.Scroll(cx, cy, 0, 3000)
		h.ui.Frame()
	}
	t.Fatalf("bottom of the plan list never reached; texts=%q", lastTexts(h.ui, 30))
}

// irRowsCenter returns a point inside the form's rows list: the center of
// the newest commit's action choice, which always renders at the top.
func irRowsCenter(t *testing.T, h *testHarness) (float32, float32) {
	t.Helper()
	st := interactiveRebaseUI(h.app)
	top := st.rows[len(st.rows)-1]
	sel, ok := h.ui.AccessByRole(ui.RolePopUpButton, "Action "+irShort(top.hash))
	if !ok {
		t.Fatalf("action choice of the top row missing; texts=%q", lastTexts(h.ui, 30))
	}
	return sel.Bounds.X + sel.Bounds.W/2, sel.Bounds.Y + sel.Bounds.H/2
}

func lastTexts(tt *ui.Tester, n int) []string {
	texts := tt.Texts()
	if len(texts) > n {
		return texts[len(texts)-n:]
	}
	return texts
}

// irExecOrder returns the hashes of the plan rows in the execution order —
// oldest first — through the form state.
func irExecOrder(t *testing.T, h *testHarness) []string {
	t.Helper()
	st := interactiveRebaseUI(h.app)
	out := make([]string, 0, len(st.rows))
	for _, row := range st.rows {
		out = append(out, row.hash)
	}
	return out
}

// A plan of 1000 commits scrolled to the bottom: the oldest commit moves up
// by one position per click even when the clicks land back to back, no row
// ever renders twice or goes missing, and an action changed to Squash folds
// the commit for real — the rebase replayed exactly what the form showed,
// verified against the independent git log of the result.
func TestDialogListRebaseThousandRowsMoveAndSquash(t *testing.T) {
	repo, onto := dialogIRFixture(t, "ir-dialog-1000", 1001)
	h := openDialogIR(t, repo, onto, 1000)
	// The tester clock stands still until advanced: finish the opening
	// animations once so the rapid clicks below settle in a frame or two
	// each rather than re-running them to the settle cap.
	h.ui.Advance(3 * time.Second)
	irScrollToBottom(t, h)

	hash2 := dialogIRHash(t, repo, 2)
	hash3 := dialogIRHash(t, repo, 3)
	hash4 := dialogIRHash(t, repo, 4)

	// ir commit 2 is the bottom row: moving it up is later in the execution
	// order. Two rapid clicks — the second lands in the frame after the
	// first, the fastest succession a double-click can come in — move it up
	// twice, and no pass may draw a commit twice or lose one.
	if err := h.ui.Click("Move Up " + irShort(hash2)); err != nil {
		t.Fatalf("first move up: %v\ntexts=%q", err, lastTexts(h.ui, 30))
	}
	if err := h.ui.Click("Move Up " + irShort(hash2)); err != nil {
		t.Fatalf("second move up: %v\ntexts=%q", err, lastTexts(h.ui, 30))
	}
	h.ui.Frame()

	// ir commit 7 moves down twice the same way: toward the bottom of the
	// form, earlier in the execution order. Its swap partner is the row the
	// list builds after its own, the case whose mid-build corruption the
	// deferred swap exists to prevent.
	hash7 := dialogIRHash(t, repo, 7)
	if err := h.ui.Click("Move Down " + irShort(hash7)); err != nil {
		t.Fatalf("first move down: %v\ntexts=%q", err, lastTexts(h.ui, 30))
	}
	if err := h.ui.Click("Move Down " + irShort(hash7)); err != nil {
		t.Fatalf("second move down: %v\ntexts=%q", err, lastTexts(h.ui, 30))
	}
	h.ui.Frame()

	exec := irExecOrder(t, h)
	if len(exec) != 1000 {
		t.Fatalf("plan rows after the moves = %d, want 1000", len(exec))
	}
	if slices.Index(exec, hash2) != 2 {
		t.Fatalf("ir commit 2 at execution position %d after two moves up, want 2", slices.Index(exec, hash2))
	}
	if exec[0] != hash3 || exec[1] != hash4 {
		t.Fatalf("positions 0 and 1 = %s, %s, want ir commit 3, ir commit 4", irShort(exec[0]), irShort(exec[1]))
	}
	if exec[3] != hash7 {
		t.Fatalf("position 3 = %s after two moves down of ir commit 7, want ir commit 7", irShort(exec[3]))
	}
	seen := map[string]int{}
	for _, hash := range exec {
		seen[hash]++
	}
	for hash, n := range seen {
		if n != 1 {
			t.Fatalf("commit %s in the plan %d times", irShort(hash), n)
		}
	}
	// The rows in view render their commits exactly once each inside the
	// form — the History page behind it lists the same commits, so only
	// the elements within the dialog's bounds count.
	dlg, ok := h.ui.AccessByRole(ui.RoleDialog, "")
	if !ok {
		t.Fatal("no dialog present")
	}
	for _, subject := range []string{"ir commit 2", "ir commit 3", "ir commit 4", "ir commit 5", "ir commit 6", "ir commit 7"} {
		n := 0
		for _, info := range h.ui.AccessAll() {
			if info.Name == subject && insideRect(info.Bounds, dlg.Bounds) {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("subject %q renders %d times inside the dialog, want exactly 1; texts=%q", subject, n, lastTexts(h.ui, 30))
		}
	}

	// Squash folds ir commit 6 into ir commit 5: the squash reads from the
	// row of ir commit 6, whatever position the moves left it at.
	hash6 := dialogIRHash(t, repo, 6)
	chooseIRAction(t, h, hash6, "Pick", "Squash")
	drainIRMessages(t, h)

	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase of the reordered plan failed: %v", err)
	}

	// The independent git log reads back exactly the order the form held:
	// ir commit 2 two positions later than it was, the rest in order, ir
	// commit 6 folded away, newest first.
	subjects := irLogSubjects(t, repo.Path(), "main")
	// Execution order after the moves: 3, 4, 2, 7, 5, 6(squashed), 8, … —
	// the log is its reverse with ir commit 6 folded into ir commit 5.
	want := make([]string, 0, 1000)
	for n := 1001; n >= 8; n-- {
		want = append(want, fmt.Sprintf("ir commit %d", n))
	}
	want = append(want, "ir commit 5", "ir commit 7", "ir commit 2", "ir commit 4", "ir commit 3", "ir commit 1")
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after the reordered squash rebase = %v…%v (len %d), want len %d ending …%v",
			subjects[:3], subjects[len(subjects)-3:], len(subjects), len(want), want[len(want)-5:])
	}
	// The folded commit carries both files: ir commit 5's and ir commit 6's.
	// Its subject is ir commit 5's, unique in the replayed history.
	tip := ""
	logOut := gitOutTrim(t, repo.Path(), "log", "--format=%H %s", "main")
	for _, line := range strings.Split(logOut, "\n") {
		hash, subj, _ := strings.Cut(line, " ")
		if subj == "ir commit 5" {
			tip = hash
		}
	}
	if tip == "" {
		t.Fatal("no commit with subject ir commit 5 after the squash")
	}
	if content := strings.TrimSpace(repo.Git("show", tip+":ir5.txt")); content != "ir content 5" {
		t.Fatalf("ir5.txt in the squashed commit = %q", content)
	}
	if content := strings.TrimSpace(repo.Git("show", tip+":ir6.txt")); content != "ir content 6" {
		t.Fatalf("ir6.txt in the squashed commit = %q", content)
	}
	assertIRDialogClosed(t, h)
}

// dialogConflictFixture merges side into main with hunks conflict blocks in
// f.txt: base carries one editable line per block separated by shared filler
// lines, side and main rewrite it differently, so the merge conflicts in one
// block per edited line.
func dialogConflictFixture(t *testing.T, name string, hunks int) *gitRepoFixture {
	t.Helper()
	write := func(prefix string) string {
		var b strings.Builder
		b.WriteString("top\n")
		for n := 1; n <= hunks; n++ {
			fmt.Fprintf(&b, "%s-h%d-edit\n", prefix, n)
			for f := 1; f <= 8; f++ {
				fmt.Fprintf(&b, "h%d-filler%d\n", n, f)
			}
		}
		b.WriteString("tail\n")
		return b.String()
	}
	repo := newGitRepo(t, name)
	repo.CommitFile("f.txt", write("base"), "base commit")
	repo.Git("checkout", "-q", "-b", "side")
	repo.CommitFile("f.txt", write("side"), "side edits")
	repo.Git("checkout", "-q", "main")
	repo.CommitFile("f.txt", write("main"), "main edits")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: merge unexpectedly succeeded: %s", out)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("fixture: merge produced no unmerged entries")
	}
	return repo
}

// hunkListCenter returns a point inside the merge view's conflict block
// list: the center of whichever Ours checkbox currently renders.
func hunkListCenter(t *testing.T, h *testHarness) (float32, float32) {
	t.Helper()
	st := editorState(t, h)
	for i := len(st.inspection.Hunks); i >= 1; i-- {
		if cb, ok := h.ui.AccessByRole(ui.RoleCheckBox, fmt.Sprintf("Ours %d", i)); ok {
			return cb.Bounds.X + cb.Bounds.W/2, cb.Bounds.Y + cb.Bounds.H/2
		}
	}
	t.Fatalf("no visible hunk checkbox; texts=%q", lastTexts(h.ui, 30))
	return 0, 0
}

// The merge editor of a 500-block conflict: navigating Next to the last
// block scrolls exactly to it, scrolling back up afterwards stays where the
// user put the list — the navigation never locks the scroll — and choosing
// Theirs on the last block composes a resolution the save writes byte for
// byte into the working tree and the index.
func TestDialogListConflictFiveHundredHunks(t *testing.T) {
	repo := dialogConflictFixture(t, "conflict-dialog-500", 500)
	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")
	openMergeHunks(t, h)
	// The tester clock stands still until advanced: the opening animations
	// of the pane never finish otherwise, and every input below would re-run
	// them frame by frame to the settle cap.
	h.ui.Advance(3 * time.Second)
	st := editorState(t, h)
	if len(st.inspection.Hunks) != 500 {
		t.Fatalf("fixture: %d hunks, want 500", len(st.inspection.Hunks))
	}

	// Resolve every block with Ours first so the save at the end is legal
	// once the last block gains Theirs.
	mustClick(t, h, "All Ours")
	h.ui.Frame()

	// Next 499 times walks the focus to the last block. The frame cost of
	// the virtual list keeps this fast.
	for i := 1; i <= 499; i++ {
		if err := h.ui.Click("Next Conflict"); err != nil {
			t.Fatalf("next conflict %d/499: %v\ntexts=%q", i, err, lastTexts(h.ui, 30))
		}
	}
	h.ui.Frame()
	if st.activeHunk != 499 {
		t.Fatalf("focus after 499 nexts = %d, want 499", st.activeHunk)
	}
	if st.scrollHunk {
		t.Fatal("scroll request survived past its frame")
	}
	// The last block is in view: rows outside the view are not built, so
	// its title text proves it.
	for i := 0; i < 20 && !hasExactText(h.ui, "Conflict 500"); i++ {
		h.ui.Frame()
	}
	if !hasExactText(h.ui, "Conflict 500") {
		t.Fatalf("last block title not in view after the navigation; texts=%q", lastTexts(h.ui, 30))
	}

	// Scrolling back up is the user's own: the list must stay there instead
	// of snapping back to the navigation target.
	cx, cy := hunkListCenter(t, h)
	for i := 0; i < 40 && !hasExactText(h.ui, "Conflict 1"); i++ {
		h.ui.Move(cx, cy)
		h.ui.Scroll(cx, cy, 0, -3000)
		h.ui.Frame()
	}
	if !hasExactText(h.ui, "Conflict 1") {
		t.Fatalf("first block never reached scrolling back up; texts=%q", lastTexts(h.ui, 30))
	}
	y1 := irTextY(t, h, "Conflict 1")
	for i := 0; i < 3; i++ {
		h.ui.Frame()
	}
	if y2 := irTextY(t, h, "Conflict 1"); y2 != y1 {
		t.Fatalf("list pulled back after free scrolling: first block at y=%v then %v", y1, y2)
	}

	// The last block takes Theirs on top of Ours: both sides compose into
	// the result, ours first. Scroll back down to it; its checkbox is the
	// real control, not a state write.
	for i := 0; i < 40 && !hasExactText(h.ui, "Conflict 500"); i++ {
		h.ui.Move(cx, cy)
		h.ui.Scroll(cx, cy, 0, 3000)
		h.ui.Frame()
	}
	mustClick(t, h, "Theirs 500")
	h.ui.Frame()
	st = editorState(t, h)
	if draft := st.draft; strings.Count(draft, "main-h500-edit") != 1 || strings.Count(draft, "side-h500-edit") != 1 {
		t.Fatalf("draft of the last block wrong: %q", tail(st.draft, 400))
	}
	mustClick(t, h, "Save and Mark Resolved")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("save and mark resolved failed: %v", err)
	}

	// The saved resolution is byte for byte the composition: every block
	// ours, the last block ours then theirs.
	var want strings.Builder
	want.WriteString("top\n")
	for n := 1; n <= 500; n++ {
		fmt.Fprintf(&want, "main-h%d-edit\n", n)
		if n == 500 {
			want.WriteString("side-h500-edit\n")
		}
		for f := 1; f <= 8; f++ {
			fmt.Fprintf(&want, "h%d-filler%d\n", n, f)
		}
	}
	want.WriteString("tail\n")
	assertFileBytes(t, repo, "f.txt", want.String())
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out != "" {
		t.Fatalf("unmerged entries survived the save: %q", out)
	}
}

// insideRect reports whether r lies entirely within outer.
func insideRect(r, outer ui.Rect) bool {
	return r.X >= outer.X && r.Y >= outer.Y && r.X+r.W <= outer.X+outer.W && r.Y+r.H <= outer.Y+outer.H
}

func irTextY(t *testing.T, h *testHarness, s string) float32 {
	t.Helper()
	r, ok := h.ui.Find(s)
	if !ok {
		t.Fatalf("text %q missing; texts=%q", s, lastTexts(h.ui, 30))
	}
	return r.Y
}

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}
