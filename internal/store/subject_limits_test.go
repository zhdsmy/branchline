package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSubjectLimitsFreshAndLegacyDefaultsStay50And70(t *testing.T) {
	mem, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	if v := mem.Settings(); v.SubjectLow != 50 || v.SubjectHigh != 70 {
		t.Fatalf("in-memory fresh store limits = %d/%d, want 50/70", v.SubjectLow, v.SubjectHigh)
	}

	freshFile := filepath.Join(t.TempDir(), "fresh.json")
	fresh, err := Open(freshFile)
	if err != nil {
		t.Fatal(err)
	}
	if v := fresh.Settings(); v.SubjectLow != 50 || v.SubjectHigh != 70 {
		t.Fatalf("fresh file store limits = %d/%d, want 50/70", v.SubjectLow, v.SubjectHigh)
	}

	fixtures := []struct {
		name          string
		raw           string
		wantLow, High int
	}{
		{"empty_object", `{}`, 50, 70},
		{"empty_settings", `{"settings":{}}`, 50, 70},
		{"legacy_saved_without_subject_limits", `{"settingsSaved":true,"settings":{"fetchMinutes":10,"autoFetch":true}}`, 50, 70},
		{"legacy_only_low_set", `{"settingsSaved":true,"settings":{"subjectLow":25}}`, 25, 70},
		{"legacy_only_high_set", `{"settingsSaved":true,"settings":{"subjectHigh":85}}`, 50, 85},
		{"negative_values_fallback", `{"settingsSaved":true,"settings":{"subjectLow":-5,"subjectHigh":-10}}`, 50, 70},
	}

	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "repos.json")
			if err := os.WriteFile(file, []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := Open(file)
			if err != nil {
				t.Fatal(err)
			}
			v := s.Settings()
			if v.SubjectLow != tc.wantLow || v.SubjectHigh != tc.High {
				t.Fatalf("limits = %d/%d, want %d/%d (settings=%+v)", v.SubjectLow, v.SubjectHigh, tc.wantLow, tc.High, v)
			}
			if v.FetchMinutes <= 0 || v.PageGuide != 72 || v.StatusUpdateSeconds != 60 {
				t.Fatalf("other numeric defaults changed: %+v", v)
			}
		})
	}
}

func TestSubjectLimitsExplicitZeroPersistsAcrossReopenAndUnrelatedSaves(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	v := s.Settings()
	v.SubjectLow, v.SubjectLowSaved = 0, true
	v.SubjectHigh, v.SubjectHighSaved = 0, true
	if err := s.SetSettings(v); err != nil {
		t.Fatal(err)
	}

	assertZeroLimits := func(where string, st *Store) {
		t.Helper()
		got := st.Settings()
		if got.SubjectLow != 0 || got.SubjectHigh != 0 || !got.SubjectLowSaved || !got.SubjectHighSaved {
			t.Fatalf("%s: limits = %d/%d (saved=%v/%v), want 0/0 (true/true)",
				where, got.SubjectLow, got.SubjectHigh, got.SubjectLowSaved, got.SubjectHighSaved)
		}
		if got.PageGuide != 72 || got.FetchMinutes != 10 {
			t.Fatalf("%s: unrelated numeric defaults changed: %+v", where, got)
		}
	}

	assertZeroLimits("same store after SetSettings", s)

	reopened, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	assertZeroLimits("reopened after SetSettings", reopened)

	if err := reopened.SetAppearance("light"); err != nil {
		t.Fatal(err)
	}
	if err := reopened.SetDiffFont("Menlo", 13.5); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Add(Repo{Kind: Local, Path: "/work/repo"}); err != nil {
		t.Fatal(err)
	}
	if err := reopened.SetSheet(PushAllTags, false); err != nil {
		t.Fatal(err)
	}
	if err := reopened.SetWelcomed(); err != nil {
		t.Fatal(err)
	}
	prefs := reopened.Settings()
	prefs.IgnoreSpace = true
	if err := reopened.SetSettings(prefs); err != nil {
		t.Fatal(err)
	}
	assertZeroLimits("after unrelated saves in memory", reopened)

	reopened2, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	assertZeroLimits("reopened after unrelated saves", reopened2)
	if !reopened2.Settings().IgnoreSpace || reopened2.Settings().DiffFontSize != 13.5 {
		t.Fatalf("unrelated preferences lost: %+v", reopened2.Settings())
	}

	// Verify independent single-zero combinations and raw JSON explicit 0.
	for _, tc := range []struct {
		name              string
		in                Settings
		wantLow, wantHigh int
	}{
		{
			name:     "low_zero_high_positive",
			in:       Settings{SubjectLow: 0, SubjectLowSaved: true, SubjectHigh: 70, SubjectHighSaved: true},
			wantLow:  0,
			wantHigh: 70,
		},
		{
			name:     "low_positive_high_zero",
			in:       Settings{SubjectLow: 50, SubjectLowSaved: true, SubjectHigh: 0, SubjectHighSaved: true},
			wantLow:  50,
			wantHigh: 0,
		},
		{
			name:     "low_zero_saved_high_unsaved_default",
			in:       Settings{SubjectLow: 0, SubjectLowSaved: true},
			wantLow:  0,
			wantHigh: 70,
		},
		{
			name:     "low_unsaved_default_high_zero_saved",
			in:       Settings{SubjectHigh: 0, SubjectHighSaved: true},
			wantLow:  50,
			wantHigh: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "single.json")
			st, err := Open(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.SetSettings(tc.in); err != nil {
				t.Fatal(err)
			}
			if got := st.Settings(); got.SubjectLow != tc.wantLow || got.SubjectHigh != tc.wantHigh {
				t.Fatalf("same store = %d/%d, want %d/%d", got.SubjectLow, got.SubjectHigh, tc.wantLow, tc.wantHigh)
			}
			disk, err := Open(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := disk.Settings(); got.SubjectLow != tc.wantLow || got.SubjectHigh != tc.wantHigh {
				t.Fatalf("reopened store = %d/%d, want %d/%d", got.SubjectLow, got.SubjectHigh, tc.wantLow, tc.wantHigh)
			}
		})
	}

	rawZeroFile := filepath.Join(t.TempDir(), "raw-zero.json")
	if err := os.WriteFile(rawZeroFile, []byte(`{"settings":{"subjectLow":0,"subjectHigh":0}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rawStore, err := Open(rawZeroFile)
	if err != nil {
		t.Fatal(err)
	}
	assertZeroLimits("raw JSON explicit zero", rawStore)
	if err := rawStore.SetAppearance("light"); err != nil {
		t.Fatal(err)
	}
	rawReopened, err := Open(rawZeroFile)
	if err != nil {
		t.Fatal(err)
	}
	assertZeroLimits("raw JSON explicit zero after unrelated save", rawReopened)
}

func TestSubjectLimitsSaveFailureRollsBackAndRetries(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	initial := s.Settings()
	if initial.SubjectLow != 50 || initial.SubjectHigh != 70 {
		t.Fatalf("initial limits = %d/%d, want 50/70", initial.SubjectLow, initial.SubjectHigh)
	}

	unblock := blockSnapshotSave(t, s)
	zero := initial
	zero.SubjectLow, zero.SubjectLowSaved = 0, true
	zero.SubjectHigh, zero.SubjectHighSaved = 0, true
	if err := s.SetSettings(zero); err == nil {
		t.Fatal("expected blocked SetSettings to fail")
	}
	if s.data.SettingsSaved || !reflect.DeepEqual(s.Settings(), initial) {
		t.Fatalf("failed save mutated in-memory settings: got %+v want %+v", s.Settings(), initial)
	}
	unblock()

	if err := s.SetSettings(zero); err != nil {
		t.Fatal(err)
	}
	if got := s.Settings(); got.SubjectLow != 0 || got.SubjectHigh != 0 {
		t.Fatalf("retry did not save 0/0 in memory: %+v", got)
	}
	disk, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := disk.Settings(); got.SubjectLow != 0 || got.SubjectHigh != 0 {
		t.Fatalf("retry did not persist 0/0 on disk: %+v", got)
	}

	// Now block again and verify a failed overwrite preserves the saved 0/0.
	savedZero := s.Settings()
	unblock = blockSnapshotSave(t, s)
	nonZero := savedZero
	nonZero.SubjectLow, nonZero.SubjectHigh = 40, 60
	if err := s.SetSettings(nonZero); err == nil {
		t.Fatal("expected second blocked SetSettings to fail")
	}
	if !reflect.DeepEqual(s.Settings(), savedZero) {
		t.Fatalf("failed overwrite mutated in-memory 0/0: %+v", s.Settings())
	}
	disk, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(disk.Settings(), savedZero) {
		t.Fatalf("failed overwrite mutated disk 0/0: %+v", disk.Settings())
	}
	unblock()
}
