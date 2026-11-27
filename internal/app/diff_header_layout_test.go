package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// diffHeaderLayoutPath is deep enough that the diff header's title, even
// fully shrunk, could never free enough room for the statistics at the
// minimum window width: the layout must compact instead of overflowing.
const diffHeaderLayoutPath = "avery/deeply/nested/directory/structure/headerlayout.go"

// diffHeaderLayoutLines keeps the statistics wide (four digits) so they
// take real room beside the controls.
const diffHeaderLayoutLines = 1300

func numberedLines(first, n int, fn func(i int) string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(fn(first+i) + "\n")
	}
	return b.String()
}

// writeBigDiffFixture commits a large file at path and then rewrites every
// line of it, leaving an unstaged modification whose statistics read
// +1300/-1300 and whose path nests five directories deep.
func writeBigDiffFixture(t *testing.T, repo *gitRepoFixture, path string, n int) {
	t.Helper()
	repo.CommitFile(path, numberedLines(0, n, func(i int) string {
		return fmt.Sprintf("line %d", i)
	}), "base")
	repo.WriteFile(path, numberedLines(0, n, func(i int) string {
		return fmt.Sprintf("changed line %d", i)
	}))
}

// diffHeaderBounds reads the diff header row's bounds from the
// accessibility tree of the last frame, where the row is the group named
// "Diff Header".
func diffHeaderBounds(t *testing.T, h *testHarness) ui.Rect {
	t.Helper()
	header, ok := h.ui.AccessByRole(ui.RoleGroup, "Diff Header")
	if !ok {
		t.Fatalf("diff header group missing from the accessibility tree; texts=%q", h.ui.Texts())
	}
	return header.Bounds
}

// assertDiffHeaderControlsFit checks the one rule the compact header
// layout exists for: the Ignore Whitespace, Split and Unified controls lie
// inside the diff header row, and the header row ends inside the window.
func assertDiffHeaderControlsFit(t *testing.T, h *testHarness, windowWidth float32) {
	t.Helper()
	header := diffHeaderBounds(t, h)
	if right := header.X + header.W; right > windowWidth+0.5 {
		t.Fatalf("diff header right edge %.2f beyond the %.0f-point window", right, windowWidth)
	}
	for _, name := range []string{"Ignore Whitespace", "Split", "Unified"} {
		btn, ok := h.ui.AccessByRole(ui.RoleButton, name)
		if !ok {
			t.Fatalf("%s control missing; texts=%q", name, h.ui.Texts())
		}
		b := btn.Bounds
		if b.X < header.X-0.5 || b.X+b.W > header.X+header.W+0.5 {
			t.Fatalf("%s horizontal bounds [%.2f, %.2f] outside the diff header [%.2f, %.2f]",
				name, b.X, b.X+b.W, header.X, header.X+header.W)
		}
		if b.Y < header.Y-0.5 || b.Y+b.H > header.Y+header.H+0.5 {
			t.Fatalf("%s vertical bounds [%.2f, %.2f] outside the diff header [%.2f, %.2f]",
				name, b.Y, b.Y+b.H, header.Y, header.Y+header.H)
		}
	}
}

// resize settles the compact layout at a new window size: the first frame
// lays the header out at the new width, the second reads those bounds, so
// assertions see the width's own layout rather than a transition frame.
func resize(t *testing.T, h *testHarness, width, height int) {
	t.Helper()
	h.ui.SetSize(width, height)
	h.ui.Frame()
	h.ui.Frame()
}

func addedText() string   { return fmt.Sprintf("+%d", diffHeaderLayoutLines) }
func deletedText() string { return fmt.Sprintf("-%d", diffHeaderLayoutLines) }

// At the minimum window width the Local Changes diff header compacts for a
// long path with wide statistics: the statistics leave the tree and the
// layout, the badge stays, and the three controls fit inside the header.
func TestDiffHeaderLayoutChangesPageMinWidth(t *testing.T) {
	repo := newGitRepo(t, "hl-changes")
	writeBigDiffFixture(t, repo, diffHeaderLayoutPath, diffHeaderLayoutLines)

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged "+diffHeaderLayoutPath)
	h.drain()

	resize(t, h, 880, 700)

	assertDiffHeaderControlsFit(t, h, 880)
	if hasExactText(h.ui, addedText()) || hasExactText(h.ui, deletedText()) {
		t.Fatalf("statistics visible in the compact header at minimum width; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "Unstaged") {
		t.Fatalf("badge missing although only the statistics should hide; texts=%q", h.ui.Texts())
	}
	for _, a := range h.ui.AccessAll() {
		if a.Name == addedText() || a.Name == deletedText() {
			t.Fatalf("hidden statistics stay in the accessibility tree: %+v", a)
		}
	}
}

// At the minimum window width the History page's diff header keeps its
// three controls inside the header for the same long path and wide
// statistics.
func TestDiffHeaderLayoutHistoryPageMinWidth(t *testing.T) {
	repo := newGitRepo(t, "hl-history")
	repo.CommitFile(diffHeaderLayoutPath, numberedLines(0, diffHeaderLayoutLines, func(i int) string {
		return fmt.Sprintf("line %d", i)
	}), "base")
	head := repo.CommitFile(diffHeaderLayoutPath, numberedLines(0, diffHeaderLayoutLines, func(i int) string {
		return fmt.Sprintf("changed line %d", i)
	}), "rewrite")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, head, "rewrite")
	h.drain()
	mustClick(t, h, fileRowLabel(diffHeaderLayoutPath))
	h.drain()

	resize(t, h, 880, 700)

	assertDiffHeaderControlsFit(t, h, 880)
}

// At the minimum window width the File History inspector's diff header
// keeps its three controls inside the header for the same long path and
// wide statistics.
func TestDiffHeaderLayoutFileHistoryPageMinWidth(t *testing.T) {
	repo := newGitRepo(t, "hl-file-history")
	repo.CommitFile(diffHeaderLayoutPath, numberedLines(0, diffHeaderLayoutLines, func(i int) string {
		return fmt.Sprintf("line %d", i)
	}), "base")
	repo.CommitFile(diffHeaderLayoutPath, numberedLines(0, diffHeaderLayoutLines, func(i int) string {
		return fmt.Sprintf("changed line %d", i)
	}), "rewrite")
	// The inspector opens from an unstaged file row of the Local Changes
	// page; its own diff reads the committed rewrite.
	repo.WriteFile(diffHeaderLayoutPath, numberedLines(0, diffHeaderLayoutLines, func(i int) string {
		return fmt.Sprintf("uncommitted line %d", i)
	}))

	h := openChangesHarness(t, repo)
	chooseFileMenuItem(t, h, "Unstaged "+diffHeaderLayoutPath, "File History…")
	h.drain()

	resize(t, h, 880, 700)

	assertDiffHeaderControlsFit(t, h, 880)
}

// The compact layout follows the window width without flicker: statistics
// show at 1200 points and hide at 880, and resizing back and forth gives
// the same layout for the same width, stable across three consecutive
// frames.
func TestDiffHeaderLayoutStableAcrossResizes(t *testing.T) {
	repo := newGitRepo(t, "hl-stable")
	writeBigDiffFixture(t, repo, diffHeaderLayoutPath, diffHeaderLayoutLines)

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged "+diffHeaderLayoutPath)
	h.drain()

	type signature struct {
		statsVisible bool
		badgeVisible bool
		unifiedRight float32
	}
	sig := func() signature {
		unified, ok := h.ui.AccessByRole(ui.RoleButton, "Unified")
		if !ok {
			t.Fatalf("Unified control missing; texts=%q", h.ui.Texts())
		}
		return signature{
			statsVisible: hasExactText(h.ui, addedText()) || hasExactText(h.ui, deletedText()),
			badgeVisible: hasExactText(h.ui, "Unstaged"),
			unifiedRight: unified.Bounds.X + unified.Bounds.W,
		}
	}
	stable := func(wantFrames int) signature {
		first := sig()
		for i := 2; i <= wantFrames; i++ {
			h.ui.Frame()
			if got := sig(); got != first {
				t.Fatalf("layout flickers at the same width: frame 1 = %+v, frame %d = %+v", first, i, got)
			}
		}
		return first
	}

	resize(t, h, 1200, 700)
	wide := stable(3)
	if !wide.statsVisible || !wide.badgeVisible {
		t.Fatalf("1200-point layout must show statistics and badge as before: %+v", wide)
	}

	resize(t, h, 880, 700)
	narrow := stable(3)
	if narrow.statsVisible {
		t.Fatalf("880-point layout keeps wide statistics: %+v", narrow)
	}
	if narrow == wide {
		t.Fatalf("resizing changed nothing in the header layout: %+v", narrow)
	}

	resize(t, h, 1200, 700)
	if got := stable(3); got != wide {
		t.Fatalf("back at 1200 the layout = %+v, want the earlier %+v", got, wide)
	}

	resize(t, h, 880, 700)
	if got := stable(3); got != narrow {
		t.Fatalf("back at 880 the layout = %+v, want the earlier %+v", got, narrow)
	}
}

// The shrink order is fixed: statistics leave before the badge, and the
// badge leaves before the level stops fitting anything else, so the
// controls never compete with optional content.
func TestDiffHeaderLayoutShrinkOrder(t *testing.T) {
	const (
		controls = 160
		stats    = 60
		badge    = 50
	)
	levelAt := func(avail float32) int {
		return diffHeaderLevelFor(avail, controls, stats, badge)
	}
	if got := levelAt(500); got != diffHeaderFull {
		t.Fatalf("ample width level = %d, want full", got)
	}
	if got := levelAt(controls + stats + badge + 2); got != diffHeaderFull {
		t.Fatalf("exact fit level = %d, want full", got)
	}
	// The slack keeps a borderline fit honest: exactly the measured width
	// already drops a level rather than trusting a zero-margin fit.
	if got := levelAt(controls + stats + badge); got != diffHeaderNoStats {
		t.Fatalf("borderline fit level = %d, want noStats", got)
	}
	if got := levelAt(controls + badge + 20); got != diffHeaderNoStats {
		t.Fatalf("width without room for statistics level = %d, want noStats", got)
	}
	if got := levelAt(controls + 10); got != diffHeaderCompact {
		t.Fatalf("width without room for the badge level = %d, want compact", got)
	}
	// Shrinking through the levels never shows statistics without the badge.
	for avail := float32(500); avail >= controls+stats+badge+2; avail-- {
		if levelAt(avail) != diffHeaderFull {
			t.Fatalf("level below full at avail %.0f that fits everything", avail)
		}
	}
	seenNoStats, seenCompact := false, false
	for avail := float32(500); avail >= 0; avail-- {
		switch levelAt(avail) {
		case diffHeaderNoStats:
			seenNoStats = true
			if seenCompact {
				t.Fatalf("noStats after compact while shrinking at avail %.0f", avail)
			}
		case diffHeaderCompact:
			seenCompact = true
		}
	}
	if !seenNoStats || !seenCompact {
		t.Fatalf("shrink sweep skipped levels: noStats=%v compact=%v", seenNoStats, seenCompact)
	}
	// An empty avail, as the first frame before the header has bounds,
	// keeps the full layout.
	if got := levelAt(0); got != diffHeaderFull {
		t.Fatalf("empty avail level = %d, want full", got)
	}
	// Content without statistics can still drop the badge: the levels do
	// not depend on content that is absent anyway.
	if got := diffHeaderLevelFor(controls+20, controls, 0, badge); got != diffHeaderCompact {
		t.Fatalf("badge-only width without statistics level = %d, want compact", got)
	}
}
