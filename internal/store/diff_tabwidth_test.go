package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The diff tab width starts at 0 — the framework default of 4 columns — on
// a fresh store, and SetDiffTabWidth persists each of the 2, 4 and 8 choices
// so a reopened store reads them back.
func TestDiffTabWidthDefaultsToZeroAndChoicesRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "store.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Settings().DiffTabWidth; got != 0 {
		t.Fatalf("fresh DiffTabWidth = %d, want 0", got)
	}
	for _, width := range []int{2, 4, 8, 0} {
		if err := s.SetDiffTabWidth(width); err != nil {
			t.Fatalf("SetDiffTabWidth(%d): %v", width, err)
		}
		reopened, err := Open(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := reopened.Settings().DiffTabWidth; got != width {
			t.Fatalf("reopened DiffTabWidth = %d, want %d", got, width)
		}
	}
}

// Widths outside the choices are rejected without touching the store, and a
// hand-edited store carrying one reads the width back as 0.
func TestDiffTabWidthRejectsInvalidWidthsAndNormalizesReads(t *testing.T) {
	file := filepath.Join(t.TempDir(), "store.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDiffTabWidth(2); err != nil {
		t.Fatal(err)
	}
	before := s.Settings()
	for _, width := range []int{-1, 1, 3, 5, 16, 100} {
		if err := s.SetDiffTabWidth(width); err == nil {
			t.Fatalf("invalid width %d accepted", width)
		}
		if got := s.Settings(); !reflect.DeepEqual(got, before) {
			t.Fatalf("invalid width %d changed settings: got %+v want %+v", width, got, before)
		}
	}
	if got := s.Settings().DiffTabWidth; got != 2 {
		t.Fatalf("rejections changed DiffTabWidth to %d, want 2", got)
	}

	file2 := filepath.Join(t.TempDir(), "store.json")
	if err := os.WriteFile(file2, []byte(`{"settings":{"diffTabWidth":3},"settingsSaved":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(file2)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Settings().DiffTabWidth; got != 0 {
		t.Fatalf("hand-edited width 3 read as %d, want 0 (the framework default)", got)
	}
}

// A failed write rolls the in-memory settings and the saved flag back, and
// unblocking the write persists the retry.
func TestDiffTabWidthFailureRollsBackSettingsAndSavedFlag(t *testing.T) {
	file := filepath.Join(t.TempDir(), "store.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	before := s.Settings()
	if err := os.Mkdir(file+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDiffTabWidth(8); err == nil {
		t.Fatal("blocked write succeeded")
	}
	if s.data.SettingsSaved || !settingsUnset(s.data.Settings) || !reflect.DeepEqual(before, s.Settings()) {
		t.Fatal("failed save changed store/default identity")
	}
	if err := os.Remove(file + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDiffTabWidth(8); err != nil {
		t.Fatal(err)
	}
	s, err = Open(file)
	if err != nil || s.Settings().DiffTabWidth != 8 || !s.Settings().RememberCheckout {
		t.Fatalf("retry lost width/defaults: %v %+v", err, s)
	}
}

// Saving a tab width into a fresh store keeps the other default settings
// intact, as the font save does.
func TestDiffTabWidthPreservesDefaultsAndOtherSettings(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "saved-off"}[explicit], func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "store.json")
			s, err := Open(file)
			if err != nil {
				t.Fatal(err)
			}
			if explicit {
				if err := s.SetSettings(Settings{SupportEmail: "saved@example.com"}); err != nil {
					t.Fatal(err)
				}
			}
			want := s.Settings()
			want.DiffTabWidth = 2
			if err := s.SetDiffTabWidth(2); err != nil {
				t.Fatal(err)
			}
			s, err = Open(file)
			if err != nil {
				t.Fatal(err)
			}
			if got := s.Settings(); !reflect.DeepEqual(got, want) {
				t.Fatalf("tab width save changed settings: got %+v want %+v", got, want)
			}
		})
	}
}
