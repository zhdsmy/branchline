package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFreshSettingsDefaultsSurviveUnrelatedSave(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	check := func(s *Store) {
		t.Helper()
		v := s.Settings()
		if !v.RememberCheckout || !v.MarkDirtyTabs || !v.CompactLabels || !v.SpellCheck || !v.UpdateSubmodules || !v.AutoFetch {
			t.Fatalf("new preferences did not receive defaults: %+v", v)
		}
		if v.LocalBranchSort != "backward" || v.RemoteBranchSort != "alpha-folders" || v.TagSort != "" {
			t.Fatalf("sort defaults: %+v", v)
		}
		if v.FetchMinutes != 10 || v.StatusUpdateSeconds != 60 || v.DisableStatusUpdate || v.SubjectLow != 50 || v.SubjectHigh != 70 || v.PageGuide != 72 || v.ScanDepth != 6 {
			t.Fatalf("numeric defaults: %+v", v)
		}
	}
	check(s)
	if err := s.SetAppearance("light"); err != nil {
		t.Fatal(err)
	}
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	check(s)
}

func TestExplicitAllOffSettingsStayOff(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSettings(Settings{}); err != nil {
		t.Fatal(err)
	}
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	v := s.Settings()
	if v.RememberCheckout || v.MarkDirtyTabs || v.CompactLabels || v.SpellCheck || v.UpdateSubmodules || v.AutoFetch || v.DisableStatusUpdate || v.StatusUpdateSeconds != 60 {
		t.Fatalf("saved false preferences were reset: %+v", v)
	}
}

func TestNegativeStatusIntervalStaysNegative(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSettings(Settings{StatusUpdateSeconds: -5}); err != nil {
		t.Fatal(err)
	}
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Settings().StatusUpdateSeconds; got != -5 {
		t.Fatalf("negative interval changed to %d", got)
	}
}

func TestLegacySavedSettingsArePreserved(t *testing.T) {
	for _, body := range []string{`{"settings":{"fetchMinutes":10}}`, `{"settings":{"localBranchSort":"date"}}`, `{"settings":{"remoteBranchSort":"date"}}`} {
		file := filepath.Join(t.TempDir(), "repos.json")
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := Open(file)
		if err != nil {
			t.Fatal(err)
		}
		v := s.Settings()
		if v.RememberCheckout || v.UpdateSubmodules || v.SpellCheck || v.AutoFetch || v.DisableStatusUpdate || v.StatusUpdateSeconds != 60 || v.ScanDepth != 0 {
			t.Fatalf("legacy saved settings changed: %+v", v)
		}
		switch body {
		case `{"settings":{"localBranchSort":"date"}}`:
			if v.LocalBranchSort != "date" || v.RemoteBranchSort != "" {
				t.Fatalf("local sort changed: %+v", v)
			}
		case `{"settings":{"remoteBranchSort":"date"}}`:
			if v.RemoteBranchSort != "date" || v.LocalBranchSort != "" {
				t.Fatalf("remote sort changed: %+v", v)
			}
		default:
			if v.LocalBranchSort != "" || v.RemoteBranchSort != "" {
				t.Fatalf("sort filled into an old store: %+v", v)
			}
		}
	}
}
