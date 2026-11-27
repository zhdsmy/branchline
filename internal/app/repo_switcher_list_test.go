package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/store"
)

// newSeededTestApp mirrors newTestApp but opens the JSON prepareSeededStore
// wrote, so the repositories keep the exact ordering their preset Opened
// timestamps give them instead of the wall-clock ordering store.Add writes.
func newSeededTestApp(t *testing.T, path string) *testHarness {
	t.Helper()
	if path == "" {
		t.Fatal("prepareSeededStore returned an empty path")
	}
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("open seeded store: %v", err)
	}
	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)
	a := newApp(s)
	a.postFn = q.post
	a.spawnFn = q.spawn
	h := &testHarness{t: t, app: a, store: s, ui: ui.NewTester(a.view, 1200, 800), queue: q}
	h.ui.Frame()
	return h
}

// switcherSeedRepos returns n repositories for the store file, newest
// opened first: local repositories named repo-000 on, with every fourth an
// SSH one so the taller two-line rows are among them. Their paths need not
// exist: only switching to one would run git in it. Each carries the local
// settings a repository gathers in real use, so that copying the list —
// which the store deep-copies — costs what it costs outside tests.
func switcherSeedRepos(n int) []store.Repo {
	repos := make([]store.Repo, n)
	for i := 0; i < n; i++ {
		r := store.Repo{
			ID:     fmt.Sprintf("seed-id-%03d", i),
			Name:   fmt.Sprintf("repo-%03d", i),
			Kind:   store.Local,
			Path:   fmt.Sprintf("/nonexistent/repo-%03d", i),
			Opened: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Hour),
			Local: store.RepoSettings{
				NoAutoFetch:   []string{"origin"},
				SidebarClosed: []string{"branches/feature", "tags/releases"},
			},
		}
		if i%4 == 1 {
			r.Kind = store.SSH
			r.Host = "git.example.test"
			r.User = "alice"
		}
		repos[i] = r
	}
	return repos
}

// openSwitcherPopup opens the repository switcher and renders the frame
// that shows it.
func openSwitcherPopup(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v; texts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if !h.app.reposUI().popupOpen {
		t.Fatal("the switcher popup did not open")
	}
}

// switcherRowButtons returns the open buttons of the repository rows of
// the open popup, in reading order. Matching by button role and the row's
// name keeps the trigger's title text — which shows the same name as the
// repository it is open on — out of the comparison.
func switcherRowButtons(t *testing.T, h *testHarness) []ui.AccessInfo {
	t.Helper()
	var rows []ui.AccessInfo
	for _, a := range h.ui.AccessAll() {
		if a.Role == ui.RoleButton && strings.HasPrefix(a.Name, "repo-") {
			rows = append(rows, a)
		}
	}
	if len(rows) == 0 {
		t.Fatalf("no repository row buttons in the open popup; texts=%q", h.ui.Texts())
	}
	return rows
}

// scrollSwitcherToBottom scrolls inside the open popup until row name is
// rendered, as the rows beyond the viewport only exist once they scroll
// into view.
func scrollSwitcherToBottom(t *testing.T, h *testHarness, name string) {
	t.Helper()
	first, ok := h.ui.Find("repo-000")
	if !ok {
		t.Fatalf("first repository row missing in open popup; texts=%q", h.ui.Texts())
	}
	x := first.X + first.W/2
	y := first.Y + first.H/2
	for step := 0; step < 30; step++ {
		if _, ok := h.ui.Find(name); ok {
			return
		}
		h.ui.Scroll(x, y, 0, 300)
		h.ui.Frame()
	}
	t.Fatalf("row %q never rendered while scrolling down; texts=%q", name, h.ui.Texts())
}

// Clicking the last repository of a long list switches to it: the popup
// closes, and opened again it shows that repository first, scrolled to the
// top as every fresh opening of it.
func TestRepoSwitcherListSwitchFromBottom(t *testing.T) {
	const n = 200
	path := prepareSeededStore(t, switcherSeedRepos(n))
	h := newSeededTestApp(t, path)

	oldest := fmt.Sprintf("repo-%03d", n-1)
	openSwitcherPopup(t, h)
	if _, ok := h.ui.Find(oldest); ok {
		t.Fatalf("row %q rendered before scrolling, the list is expected to virtualize", oldest)
	}
	scrollSwitcherToBottom(t, h, oldest)
	if err := h.ui.Click(oldest); err != nil {
		t.Fatalf("click %q: %v; texts=%q", oldest, err, h.ui.Texts())
	}
	h.ui.Frame()
	h.drain()

	if h.app.reposUI().popupOpen {
		t.Fatal("the popup stayed open after switching repositories")
	}
	if got := h.app.session.Meta().ID; got != fmt.Sprintf("seed-id-%03d", n-1) {
		t.Fatalf("active repository ID = %q, want the one of %q", got, oldest)
	}

	openSwitcherPopup(t, h)
	rows := switcherRowButtons(t, h)
	if rows[0].Name != oldest {
		names := make([]string, 0, len(rows))
		for _, r := range rows {
			names = append(names, r.Name)
		}
		t.Fatalf("the first row of the reopened popup is %q, want %q first after switching to it; rows=%v", rows[0].Name, oldest, names)
	}
	if rows[1].Name != "repo-000" {
		t.Fatalf("the second row of the reopened popup is %q, want repo-000", rows[1].Name)
	}
	// A fresh opening starts at the first row: the top of it sits inside
	// the popup's top area, just below the trigger.
	trigR, ok := h.ui.Find("Repository Switcher")
	if !ok {
		t.Fatal("Repository Switcher trigger missing")
	}
	if first := rows[0].Bounds; first.Y < trigR.Y || first.Y > trigR.Y+trigR.H+30 {
		t.Fatalf("first row of the reopened popup at Y %.1f against the trigger's %+v: the list did not start at its top", first.Y, trigR)
	}
}

// The popup follows the store without reopening: a row's remove button
// drops it from the list, and a repository another part of the app saves
// appears in it, each on the next frame.
func TestRepoSwitcherListRemoveAndAddLive(t *testing.T) {
	path := prepareSeededStore(t, switcherSeedRepos(8))
	h := newSeededTestApp(t, path)

	openSwitcherPopup(t, h)
	if err := h.ui.Click("Remove repo-002"); err != nil {
		t.Fatalf("click Remove repo-002: %v; texts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if _, ok := h.ui.Find("repo-002"); ok {
		t.Fatal("repo-002 still renders in the popup after its remove button was clicked")
	}
	if _, ok := h.store.Get("seed-id-002"); ok {
		t.Fatal("repo-002 survived in the store after its remove button was clicked")
	}

	// A repository saved outside the popup — the start page rescanning,
	// a clone finishing — shows on the next frame too.
	if _, err := h.store.Add(store.Repo{Kind: store.Local, Path: "/nonexistent/fresh", Name: "fresh-repo"}); err != nil {
		t.Fatalf("add fresh-repo: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.Find("fresh-repo"); !ok {
		t.Fatalf("fresh-repo missing from the popup a frame after store.Add; texts=%q", h.ui.Texts())
	}
	// It was just opened, so it sorts first.
	freshR, _ := h.ui.Find("fresh-repo")
	zeroR, ok := h.ui.Find("repo-000")
	if !ok {
		t.Fatalf("repo-000 missing from the popup; texts=%q", h.ui.Texts())
	}
	if freshR.Y >= zeroR.Y {
		t.Fatalf("fresh-repo at Y %.1f is not above repo-000's %.1f: the just-added repository did not sort first", freshR.Y, zeroR.Y)
	}
}

// The rows a scroll reveals are only those near the viewport: with 200
// repositories in the list, a freshly opened popup renders far fewer rows
// than it holds, and rows further down come into existence as it scrolls.
func TestRepoSwitcherListVirtualizesRows(t *testing.T) {
	const n = 200
	path := prepareSeededStore(t, switcherSeedRepos(n))
	h := newSeededTestApp(t, path)

	openSwitcherPopup(t, h)
	rendered := 0
	for _, text := range h.ui.Texts() {
		if strings.HasPrefix(text, "repo-") && len(text) == len("repo-000") {
			rendered++
		}
	}
	if rendered == 0 {
		t.Fatalf("no repository row rendered in the open popup; texts=%q", h.ui.Texts())
	}
	if rendered >= n/2 {
		t.Fatalf("%d of %d repository rows rendered in a 300pt viewport: the list builds rows far beyond it", rendered, n)
	}
}
