package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// a11ySettingsTitles are the titles of the Settings window's pages in tab
// order, as the tab list must expose them.
func a11ySettingsTitles() []string {
	pages := settingsPages()
	titles := make([]string, len(pages))
	for i, def := range pages {
		titles[i] = def.title
	}
	return titles
}

// a11yTabNames returns the accessible names of the tabs that follow the tab
// list in the accessibility tree, and their Selected states.
func a11yTabStates(t *testing.T, tt *ui.Tester) (names []string, selected []bool) {
	t.Helper()
	all := tt.AccessAll()
	start := -1
	for i, a := range all {
		if a.Role == ui.RoleTabList {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("no tab list in the accessibility tree; texts=%q", tt.Texts())
	}
	for _, a := range all[start+1:] {
		if a.Role != ui.RoleTab {
			break
		}
		names = append(names, a.Name)
		selected = append(selected, a.Selected)
	}
	return names, selected
}

// a11yFocusTab presses Tab until the element of role and name has the
// keyboard focus, as a keyboard user reaching it does.
func a11yFocusTab(t *testing.T, tt *ui.Tester, role ui.Role, name string) {
	t.Helper()
	for i := 0; i < 40; i++ {
		if info, ok := tt.AccessByRole(role, name); ok && info.Focused {
			return
		}
		tt.Key(0, ui.KeyTab)
		tt.Frame()
	}
	info, ok := tt.AccessByRole(role, name)
	t.Fatalf("Tab never focused %q: found=%v focused=%v; texts=%q", name, ok, info.Focused, tt.Texts())
}

// a11yFocusedTab reports which tab has the keyboard focus, if any.
func a11yFocusedTab(tt *ui.Tester) (name string, focused bool) {
	for _, a := range tt.AccessAll() {
		if a.Role == ui.RoleTab && a.Focused {
			return a.Name, true
		}
	}
	return "", false
}

// The Settings window's page tabs are one tab list to assistive technology:
// the list is there, the tabs inside it carry every page's title in order,
// and the tab of the page showing reads as selected while the others do not.
func TestSettingsA11yTabListExposesAllPages(t *testing.T) {
	h := newTestApp(t)
	tt := openSettingsTester(t, h)

	if _, ok := tt.AccessByRole(ui.RoleTabList, ""); !ok {
		t.Fatalf("no tab list in the Settings window; texts=%q", tt.Texts())
	}
	names, selected := a11yTabStates(t, tt)
	want := a11ySettingsTitles()
	if len(names) != len(want) {
		t.Fatalf("tab names = %q, want the page titles %q", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("tab %d = %q, want %q (all tabs %q)", i, names[i], want[i], names)
		}
	}
	for i := range names {
		if selected[i] != (i == 0) {
			t.Fatalf("tab %q selected = %v on the first page, want %v", names[i], selected[i], i == 0)
		}
	}
}

// The arrows, Home and End choose and focus a tab while one has the keyboard
// focus, and the page panel's name follows the choice: Right from the first
// tab opens the second page, End the last one — whose onOpen priming starts
// its identity load, exactly once per entry, so the load finishes when the
// queue drains — and Home returns to the first.
func TestSettingsA11yArrowKeysSwitchPages(t *testing.T) {
	h := newTestApp(t)
	tt := openSettingsTester(t, h)
	titles := a11ySettingsTitles()
	if len(titles) < 4 {
		t.Fatalf("the test wants at least 4 settings pages, got %q", titles)
	}
	first, second, last := titles[0], titles[1], titles[len(titles)-1]
	if last != "Git" {
		t.Fatalf("the last settings page = %q, want Git (the load probe below is its)", last)
	}

	a11yFocusTab(t, tt, ui.RoleTab, first)

	tt.Key(0, ui.KeyRight)
	tt.Frame()
	if name, on := a11yFocusedTab(tt); !on || name != second {
		t.Fatalf("after Right the focused tab = %q (focused=%v), want %q", name, on, second)
	}
	if _, ok := tt.AccessByRole(ui.RoleTabPanel, second); !ok {
		t.Fatalf("no tab panel named %q after Right; texts=%q", second, tt.Texts())
	}
	if _, ok := tt.AccessByRole(ui.RoleTabPanel, first); ok {
		t.Fatalf("the tab panel still reads %q after switching to %q", first, second)
	}
	if tab, ok := tt.AccessByRole(ui.RoleTab, second); !ok || !tab.Selected {
		t.Fatalf("the %q tab does not read as selected after Right: %+v (found=%v)", second, tab, ok)
	}

	tt.Key(0, ui.KeyEnd)
	tt.Frame()
	if name, on := a11yFocusedTab(tt); !on || name != last {
		t.Fatalf("after End the focused tab = %q (focused=%v), want %q", name, on, last)
	}
	if _, ok := tt.AccessByRole(ui.RoleTabPanel, last); !ok {
		t.Fatalf("no tab panel named %q after End; texts=%q", last, tt.Texts())
	}
	// Entering the Git page runs its priming once: the identity load it
	// starts shows, and it completes with one drain instead of restarting
	// every frame.
	if !tt.HasText("Loading global Git identity…") {
		t.Fatalf("the Git page's priming did not start the identity load after End; texts=%q", tt.Texts())
	}
	drainSettings(t, h, tt)
	if tt.HasText("Loading global Git identity…") {
		t.Fatal("the identity load never finished: the page priming keeps restarting it")
	}

	tt.Key(0, ui.KeyHome)
	tt.Frame()
	if name, on := a11yFocusedTab(tt); !on || name != first {
		t.Fatalf("after Home the focused tab = %q (focused=%v), want %q", name, on, first)
	}
	if _, ok := tt.AccessByRole(ui.RoleTabPanel, first); !ok {
		t.Fatalf("no tab panel named %q after Home; texts=%q", first, tt.Texts())
	}
}

// The tab list is a single stop of Tab: from the focused tab the next Tab
// leaves the list entirely and lands on the page's first control, the
// Appearance choice.
func TestSettingsA11yTabMovesFromTabIntoPage(t *testing.T) {
	h := newTestApp(t)
	tt := openSettingsTester(t, h)

	a11yFocusTab(t, tt, ui.RoleTab, "General")

	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if name, on := a11yFocusedTab(tt); on {
		t.Fatalf("Tab inside the tab list moved the focus to tab %q instead of into the page", name)
	}
	radio, ok := tt.AccessByRole(ui.RoleRadio, "Appearance System")
	if !ok {
		t.Fatalf("no Appearance System radio; texts=%q", tt.Texts())
	}
	if !radio.Focused {
		t.Fatalf("Tab from the tab list did not land on the page's first control; Appearance System focused=%v", radio.Focused)
	}
}

// A failed Git version probe surfaces the banner as an alert: the moment it
// appears a screen reader reads the failure out, instead of the user of one
// never learning that the check failed.
func TestSettingsA11yGitVersionBannerReadOnAppear(t *testing.T) {
	repo := newGitRepo(t, "sa-gv-alert")
	repo.CommitFile("a.txt", "alpha\n", "alpha commit")

	over := newVersionOverrideExecutor()
	over.armVersion("", errors.New("probe exploded"))
	h := newTestApp(t, repo.StoreRepo())
	installVersionOverrides(h, map[string]*versionOverrideExecutor{repo.Path(): over})
	h.ui.UseScreenReader()

	openRepoViaSwitcher(t, h, "sa-gv-alert")

	if _, ok := h.ui.AccessByRole(ui.RoleAlert, "Git Version Warning"); !ok {
		t.Fatalf("the version banner does not render as an alert named Git Version Warning; texts=%q", h.ui.Texts())
	}
	joined := strings.Join(h.ui.Alerts(), "\n")
	if !strings.Contains(joined, "Git version check failed") {
		t.Fatalf("a screen reader did not read the banner when it appeared; alerts=%q", joined)
	}
}

// While the Git page loads the global identity, the spinner inside the
// loading row is hidden from assistive technology: the named, busy status
// row carries the state, and no unnamed progress node is left in the tree.
func TestSettingsA11yIdentityLoadingHasNoUnnamedProgress(t *testing.T) {
	h := newTestApp(t)
	tt := openCommitGitSettingsTester(t, h)
	clickSettingsTab(t, tt, "Git")

	if !tt.HasText("Loading global Git identity…") {
		t.Fatalf("the identity load is not showing; texts=%q", tt.Texts())
	}
	row, ok := tt.AccessByRole(ui.RoleStatus, "Global Identity Loading")
	if !ok {
		t.Fatalf("no status row named Global Identity Loading; texts=%q", tt.Texts())
	}
	if !row.Busy {
		t.Fatal("the Global Identity Loading row does not read as busy")
	}
	for _, a := range tt.AccessAll() {
		if a.Role == ui.RoleProgress && a.Name == "" {
			t.Fatalf("an unnamed progress node is in the accessibility tree at %v", a.Bounds)
		}
	}
}
