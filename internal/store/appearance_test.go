package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A store that has never saved an appearance follows the system rather
// than forcing every existing user onto one polarity.
func TestAppearanceDefaultsToSystem(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "repos.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Appearance(); got != "system" {
		t.Fatalf("Appearance() = %q, want %q", got, "system")
	}
}

// Values written by versions that offered other appearance choices, or a
// hand-edited store, read back as "system" instead of an unknown polarity.
func TestAppearanceInvalidValuesReadAsSystem(t *testing.T) {
	for _, raw := range []string{"blue", "AUTO", "Dark"} {
		file := filepath.Join(t.TempDir(), "repos.json")
		json := fmt.Sprintf(`{"appearance":%q, "repos":[]}`, raw)
		if err := os.WriteFile(file, []byte(json), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := Open(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Appearance(); got != "system" {
			t.Fatalf("Appearance() with stored %q = %q, want %q", raw, got, "system")
		}
	}
}

// A saved appearance survives a save-load round trip through the file.
func TestAppearanceRoundTripsThroughDisk(t *testing.T) {
	for _, want := range []string{"light", "dark", "system"} {
		file := filepath.Join(t.TempDir(), "repos.json")
		s, err := Open(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetAppearance(want); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := reopened.Appearance(); got != want {
			t.Fatalf("Appearance() after save of %q = %q", want, got)
		}
	}
}
