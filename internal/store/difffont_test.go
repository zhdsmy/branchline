package store

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSetDiffFontRejectsInvalidSizesWithoutMutation(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	before := s.Settings()
	for _, size := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if err := s.SetDiffFont("Menlo-Bold", size); err == nil || !reflect.DeepEqual(before, s.Settings()) {
			t.Fatalf("invalid size %v changed settings", size)
		}
	}
}

func TestSetDiffFontPreservesDefaultsAndOtherSettings(t *testing.T) {
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
			want.DiffFont, want.DiffFontSize = "Menlo-Bold", 12.5
			if err := s.SetDiffFont(" Menlo-Bold ", 12.5); err != nil {
				t.Fatal(err)
			}
			s, err = Open(file)
			if err != nil {
				t.Fatal(err)
			}
			if got := s.Settings(); !reflect.DeepEqual(got, want) {
				t.Fatalf("font save changed settings: got %+v want %+v", got, want)
			}
		})
	}
}

func TestSetDiffFontFailureRollsBackSettingsAndSavedFlag(t *testing.T) {
	file := filepath.Join(t.TempDir(), "store.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	before := s.Settings()
	if err := os.Mkdir(file+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDiffFont("Menlo-Italic", 11.5); err == nil {
		t.Fatal("blocked write succeeded")
	}
	if s.data.SettingsSaved || !settingsUnset(s.data.Settings) || !reflect.DeepEqual(before, s.Settings()) {
		t.Fatal("failed save changed store/default identity")
	}
	if err := os.Remove(file + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDiffFont("Menlo-Italic", 11.5); err != nil {
		t.Fatal(err)
	}
	s, err = Open(file)
	if err != nil || s.Settings().DiffFontSize != 11.5 || !s.Settings().RememberCheckout {
		t.Fatalf("retry lost fraction/defaults: %v %+v", err, s)
	}
}

func TestDiffFontSizeReadsLegacyIntegerAndFractionalJSON(t *testing.T) {
	for _, fixture := range []struct {
		raw  string
		size float64
	}{{"14", 14}, {"12.5", 12.5}} {
		file := filepath.Join(t.TempDir(), "store.json")
		if err := os.WriteFile(file, []byte(`{"settings":{"diffFont":"Menlo-Bold","diffFontSize":`+fixture.raw+`}}`), 0600); err != nil {
			t.Fatal(err)
		}
		s, err := Open(file)
		if err != nil || s.Settings().DiffFontSize != fixture.size {
			t.Fatalf("read %s: %v %+v", fixture.raw, err, s)
		}
		if err := s.SetDiffFont("Menlo-Bold", fixture.size); err != nil {
			t.Fatal(err)
		}
		s, err = Open(file)
		if err != nil || s.Settings().DiffFontSize != fixture.size {
			t.Fatalf("reopen %s: %v %+v", fixture.raw, err, s)
		}
	}
}
