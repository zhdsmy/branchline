package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/store"
)

// settingsHooks records what the production window and theme hooks would
// have done, without creating a native window or touching the desktop
// appearance in a headless test.
type settingsHooks struct {
	opened  int
	applied []mygo.ThemeSource
}

// installSettingsHooks replaces the app's settings hooks with recorders.
func installSettingsHooks(h *testHarness) *settingsHooks {
	rec := &settingsHooks{}
	h.app.openSettingsWindowFn = func() { rec.opened++ }
	h.app.applyAppearanceFn = func(src mygo.ThemeSource) { rec.applied = append(rec.applied, src) }
	return rec
}

// newSettingsTester renders the Settings window view headlessly, as the
// window hook's ui.View(a.settingsView) would in a real window.
func newSettingsTester(t *testing.T, h *testHarness) *ui.Tester {
	t.Helper()
	tt := ui.NewTester(h.app.settingsView, 560, 420)
	tt.Frame()
	return tt
}

func (r *settingsHooks) lastApplied(t *testing.T) mygo.ThemeSource {
	t.Helper()
	if len(r.applied) == 0 {
		t.Fatal("the appearance hook was never called")
	}
	return r.applied[len(r.applied)-1]
}

// ⌘, in the main window opens the Settings window once; pressing it again
// focuses the window already open instead of creating a second one, and
// after the window closes the shortcut creates a fresh window.
func TestSettingsShortcutOpensWindowOnceAndFocuses(t *testing.T) {
	h := newTestApp(t)
	rec := installSettingsHooks(h)

	h.ui.Key(ui.Cmd, ui.KeyComma)
	h.ui.Frame()
	if rec.opened != 1 {
		t.Fatalf("first ⌘, created the window %d times, want 1", rec.opened)
	}

	h.ui.Key(ui.Cmd, ui.KeyComma)
	h.ui.Frame()
	if rec.opened != 1 {
		t.Fatalf("second ⌘, created the window again (%d times): reopening must focus, not create", rec.opened)
	}

	// The window closing clears the tracked reference; the next ⌘, opens a
	// fresh window. closeSettingsWindow is what the production window's
	// OnClosed callback runs.
	h.app.closeSettingsWindow()
	h.ui.Key(ui.Cmd, ui.KeyComma)
	h.ui.Frame()
	if rec.opened != 2 {
		t.Fatalf("⌘, after the window closed created it %d times total, want 2", rec.opened)
	}
}

// The Settings window opens on the General page, whose tab reads as the
// selected tab to assistive technology.
func TestSettingsWindowOpensOnGeneralTab(t *testing.T) {
	h := newTestApp(t)
	installSettingsHooks(h)

	h.ui.Key(ui.Cmd, ui.KeyComma)
	h.ui.Frame()
	tt := newSettingsTester(t, h)

	tab, ok := tt.AccessByRole(ui.RoleTab, "General")
	if !ok {
		t.Fatalf("no General tab in the accessibility tree; texts=%q", tt.Texts())
	}
	if !tab.Selected {
		t.Fatal("the General tab does not read as selected on the General page")
	}
	if !tt.HasText("Appearance") {
		t.Fatalf("the General page shows no Appearance row; texts=%q", tt.Texts())
	}
}

// Choosing Light, Dark and System through the radios saves each value in
// the store — read back through a reopened store file — and applies it to
// the appearance hook once it is saved.
func TestSettingsAppearanceChoicesPersistAndApply(t *testing.T) {
	h := newTestApp(t)
	rec := installSettingsHooks(h)
	tt := newSettingsTester(t, h)

	for _, choice := range []struct {
		label string
		value string
	}{
		{"Appearance Light", "light"},
		{"Appearance Dark", "dark"},
		{"Appearance System", "system"},
	} {
		if err := tt.Click(choice.label); err != nil {
			t.Fatalf("click %s: %v", choice.label, err)
		}
		tt.Frame()
		if got := rec.lastApplied(t); got != mygo.ThemeSource(choice.value) {
			t.Fatalf("after %s the appearance hook last got %q, want %q", choice.label, got, choice.value)
		}
		reopened, err := store.Open(h.store.Path())
		if err != nil {
			t.Fatal(err)
		}
		if got := reopened.Appearance(); got != choice.value {
			t.Fatalf("after %s the saved appearance reads back %q, want %q", choice.label, got, choice.value)
		}
		radio, ok := tt.AccessByRole(ui.RoleRadio, choice.label)
		if !ok {
			t.Fatalf("no %s radio after choosing it; texts=%q", choice.label, tt.Texts())
		}
		if !radio.Checked {
			t.Fatalf("the %s radio does not read as checked after being chosen", choice.label)
		}
	}
}

// When the store cannot persist the choice, the page shows the raw error,
// the appearance hook is not called, the stored value is unchanged, and the
// radios revert to the choice that last saved. Restoring writability and
// choosing again saves and clears the error, so the test cannot pass merely
// because saving is broken entirely.
func TestSettingsAppearanceSaveFailureRevertsChoice(t *testing.T) {
	h := newTestApp(t)
	rec := installSettingsHooks(h)
	tt := newSettingsTester(t, h)

	// Save a valid choice first; it becomes the one the failure reverts to.
	if err := tt.Click("Appearance Light"); err != nil {
		t.Fatalf("click Appearance Light: %v", err)
	}
	tt.Frame()
	if got := h.store.Appearance(); got != "light" {
		t.Fatalf("setup: appearance = %q, want light", got)
	}
	callsBeforeFailure := len(rec.applied)

	// Make the store's directory unwritable so the save fails, and confirm
	// the store really reports an error in this state before asserting the
	// interface on it.
	storeDir := filepath.Dir(h.store.Path())
	if err := os.Chmod(storeDir, 0o500); err != nil {
		t.Fatalf("chmod store dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(storeDir, 0o700) })
	probeErr := h.store.SetAppearance("system")
	if probeErr == nil {
		t.Fatal("SetAppearance succeeded into a read-only directory; the failure assertions below would be vacuous")
	}

	if err := tt.Click("Appearance Dark"); err != nil {
		t.Fatalf("click Appearance Dark: %v", err)
	}
	tt.Frame()

	// The raw error text shows on the page.
	if !tt.HasText(probeErr.Error()) {
		t.Fatalf("the page does not show the raw save error %q; texts=%q", probeErr, tt.Texts())
	}
	// The appearance hook was not called for the failed choice.
	if len(rec.applied) != callsBeforeFailure {
		t.Fatalf("the appearance hook ran %d more time(s) for a failed save", len(rec.applied)-callsBeforeFailure)
	}
	// The stored value is unchanged.
	if got := h.store.Appearance(); got != "light" {
		t.Fatalf("the failed save changed the appearance to %q, want light", got)
	}
	// The radios reverted to the last saved choice: Light reads checked,
	// Dark does not.
	light, ok := tt.AccessByRole(ui.RoleRadio, "Appearance Light")
	if !ok {
		t.Fatalf("no Appearance Light radio; texts=%q", tt.Texts())
	}
	if !light.Checked {
		t.Fatal("after the failed save the Appearance Light radio does not read as checked: the choice did not revert")
	}
	dark, ok := tt.AccessByRole(ui.RoleRadio, "Appearance Dark")
	if !ok {
		t.Fatalf("no Appearance Dark radio; texts=%q", tt.Texts())
	}
	if dark.Checked {
		t.Fatal("after the failed save the Appearance Dark radio still reads as checked")
	}

	// Restore writability: the same choice now saves, applies and clears
	// the error.
	if err := os.Chmod(storeDir, 0o700); err != nil {
		t.Fatalf("restore store dir: %v", err)
	}
	if err := tt.Click("Appearance Dark"); err != nil {
		t.Fatalf("click Appearance Dark again: %v", err)
	}
	tt.Frame()
	if got := h.store.Appearance(); got != "dark" {
		t.Fatalf("after restoring writability the appearance = %q, want dark", got)
	}
	if got := rec.lastApplied(t); got != mygo.ThemeDark {
		t.Fatalf("after restoring writability the hook last got %q, want dark", got)
	}
	if tt.HasText(probeErr.Error()) {
		t.Fatal("the error text is still shown after a successful save")
	}
}

// Startup applies the appearance saved in the store through the same hook a
// manual choice uses, and a store without a saved value applies system,
// leaving the app's default follow-the-system behavior unchanged.
func TestSettingsStartupAppliesSavedAppearance(t *testing.T) {
	h := newTestApp(t)
	rec := installSettingsHooks(h)

	h.app.applyStartupAppearance()
	if got := rec.lastApplied(t); got != mygo.ThemeSystem {
		t.Fatalf("startup with no saved appearance applied %q, want system", got)
	}

	if err := h.store.SetAppearance("dark"); err != nil {
		t.Fatal(err)
	}
	h.app.applyStartupAppearance()
	if got := rec.lastApplied(t); got != mygo.ThemeDark {
		t.Fatalf("startup with a saved dark appearance applied %q, want dark", got)
	}
}

// The tab row and the settings rows are left-aligned: their content starts
// near the window's left edge, in the padding of their containers, instead
// of drifting toward the center of the full-width rows.
func TestSettingsLayoutLeftAligned(t *testing.T) {
	h := newTestApp(t)
	installSettingsHooks(h)
	tt := newSettingsTester(t, h)

	tab, ok := tt.Find("General")
	if !ok {
		t.Fatalf("no General tab; texts=%q", tt.Texts())
	}
	if tab.X > 60 {
		t.Fatalf("the General tab starts at X %.1f: the tab row is not left-aligned", tab.X)
	}

	radio, ok := tt.AccessByRole(ui.RoleRadio, "Appearance System")
	if !ok {
		t.Fatalf("no Appearance System radio; texts=%q", tt.Texts())
	}
	if radio.Bounds.X > 160 {
		t.Fatalf("the Appearance radios start at X %.1f: the row content is not left-aligned", radio.Bounds.X)
	}
	group, ok := tt.AccessByRole(ui.RoleRadioGroup, "Appearance")
	if !ok {
		t.Fatalf("no Appearance radio group; texts=%q", tt.Texts())
	}
	if d := radio.Bounds.X - group.Bounds.X; d < -8 || d > 24 {
		t.Fatalf("the first radio starts %.1f into its group: the group's own padding moved", d)
	}

	// The choices sit beside one another in one row: Light starts right of
	// System on the same line, not below it.
	light, ok := tt.AccessByRole(ui.RoleRadio, "Appearance Light")
	if !ok {
		t.Fatalf("no Appearance Light radio; texts=%q", tt.Texts())
	}
	if light.Bounds.X <= radio.Bounds.X+radio.Bounds.W {
		t.Fatalf("Appearance Light starts at X %.1f, not right of Appearance System (ends %.1f): the choices are not in one row", light.Bounds.X, radio.Bounds.X+radio.Bounds.W)
	}
	if dy := light.Bounds.Y - radio.Bounds.Y; dy < -2 || dy > 2 {
		t.Fatalf("Appearance Light sits %.1f below Appearance System: the choices are not on one line", dy)
	}
}
