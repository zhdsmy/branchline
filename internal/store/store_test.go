package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseAddress(t *testing.T) {
	for _, tt := range []struct {
		in               string
		user, host, path string
		port             int
	}{
		{"me@box:/srv/app", "me", "box", "/srv/app", 0},
		{"box:code/app/", "", "box", "~/code/app", 0},
		{"me@box:~/app", "me", "box", "~/app", 0},
		{"box:", "", "box", "~", 0},
		{"box:/", "", "box", "/", 0},
		{"ssh://me@box:2222/srv/app", "me", "box", "/srv/app", 2222},
		{"ssh://box/~/app", "", "box", "~/app", 0},
		{"ssh://box", "", "box", "~", 0},
		{"  my-alias:/repo  ", "", "my-alias", "/repo", 0},
	} {
		r, err := ParseAddress(tt.in)
		if err != nil {
			t.Errorf("%q: %v", tt.in, err)
			continue
		}
		if r.Kind != SSH || r.User != tt.user || r.Host != tt.host || r.Path != tt.path || r.Port != tt.port {
			t.Errorf("%q: got %+v", tt.in, r)
		}
		back, err := ParseAddress(r.Address())
		if err != nil || !back.Same(r) {
			t.Errorf("%q: address %q reads back as %+v, %v", tt.in, r.Address(), back, err)
		}
	}
	for _, bad := range []string{"", "no-colon", ":/path", "-oProxyCommand=x:/p", "ssh://box:port/x"} {
		if r, err := ParseAddress(bad); err == nil {
			t.Errorf("%q: want an error, got %+v", bad, r)
		}
	}
}

func TestStore(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sub", "repos.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	local, err := s.Add(Repo{Kind: Local, Path: "/work/app"})
	if err != nil {
		t.Fatal(err)
	}
	if local.Name != "app" || local.ID == "" {
		t.Fatalf("added %+v", local)
	}
	remote, _ := s.Add(Repo{Kind: SSH, Host: "box", Path: "/work/app"})
	if remote.ID == local.ID {
		t.Fatal("a server's repository is the same as a local one")
	}
	again, _ := s.Add(Repo{Kind: SSH, Host: "box", Path: "/work/app", Name: "server app"})
	if again.ID != remote.ID || len(s.Repos()) != 2 {
		t.Fatalf("adding twice: %+v, %d repos", again, len(s.Repos()))
	}
	if got := s.Repos()[0]; got.ID != remote.ID || got.Name != "server app" {
		t.Fatalf("last opened first: got %+v", got)
	}
	if err := s.SetOpenTabs([]string{local.ID, remote.ID}); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := s2.Get(remote.ID); !ok || r.Name != "server app" || r.Host != "box" {
		t.Fatalf("read back %+v, %v", r, ok)
	}
	if err := s2.Remove(local.ID); err != nil {
		t.Fatal(err)
	}
	if tabs := s2.OpenTabs(); len(tabs) != 1 || tabs[0] != remote.ID {
		t.Fatalf("tabs after remove: %v", tabs)
	}
	if _, ok := s2.Get(local.ID); ok {
		t.Fatal("removed repository is still there")
	}
}

func TestTabSessionPersistsTheShownRepository(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Add(Repo{Kind: Local, Path: "/work/first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Add(Repo{Kind: Local, Path: "/work/second"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTabSession([]string{first.ID, second.ID}, second.ID, true); err != nil {
		t.Fatal(err)
	}
	again, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.OpenTabs(); len(got) != 2 || got[0] != first.ID || got[1] != second.ID {
		t.Fatalf("tabs %v", got)
	}
	if active, set := again.ActiveTab(); active != second.ID || !set {
		t.Fatalf("active %q set %v", active, set)
	}
	if err := again.SetTabSession([]string{first.ID, second.ID}, "", true); err != nil {
		t.Fatal(err)
	}
	start, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if active, set := start.ActiveTab(); active != "" || !set {
		t.Fatalf("start page active %q set %v", active, set)
	}
}

func TestOldOpenTabsStayOnTheFirstRepository(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repos.json")
	if err := os.WriteFile(file, []byte(`{"repos":[{"id":"one","path":"/one"},{"id":"two","path":"/two"}],"open":["one","two"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, set := s.ActiveTab(); set {
		t.Fatal("an old store remembered a shown repository")
	}
	if got := s.OpenTabs(); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("tabs %v", got)
	}
}

func TestTabSessionSaveFailureRestoresThePreviousChoice(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Add(Repo{Kind: Local, Path: "/work/first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Add(Repo{Kind: Local, Path: "/work/second"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTabSession([]string{first.ID}, first.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTabSession([]string{second.ID}, second.ID, true); err == nil {
		t.Fatal("a blocked save succeeded")
	}
	if got := s.OpenTabs(); len(got) != 1 || got[0] != first.ID {
		t.Fatalf("memory tabs %v", got)
	}
	if active, set := s.ActiveTab(); active != first.ID || !set {
		t.Fatalf("memory active %q set %v", active, set)
	}
	disk, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := disk.OpenTabs(); len(got) != 1 || got[0] != first.ID {
		t.Fatalf("disk tabs %v", got)
	}
	if err := os.Remove(file + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTabSession([]string{first.ID, second.ID}, second.ID, true); err != nil {
		t.Fatal(err)
	}
	disk, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if active, _ := disk.ActiveTab(); active != second.ID || len(disk.OpenTabs()) != 2 {
		t.Fatalf("retry active %q tabs %v", active, disk.OpenTabs())
	}
}

func TestRemoveFailureKeepsTheRepositoryAndItsTab(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Add(Repo{Kind: Local, Path: "/work/first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Add(Repo{Kind: Local, Path: "/work/second"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTabSession([]string{first.ID, second.ID}, second.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(second.ID); err == nil {
		t.Fatal("a blocked removal succeeded")
	}
	if _, ok := s.Get(second.ID); !ok || len(s.Repos()) != 2 {
		t.Fatal("failed removal changed the repositories")
	}
	if got := s.OpenTabs(); len(got) != 2 || got[1] != second.ID {
		t.Fatalf("tabs %v", got)
	}
	if active, set := s.ActiveTab(); active != second.ID || !set {
		t.Fatalf("active %q set %v", active, set)
	}
	disk, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := disk.Get(second.ID); !ok || len(disk.OpenTabs()) != 2 {
		t.Fatal("failed removal changed the file")
	}
}

// The diff toolbar choices survive a store round trip: only the changed
// lines, the context line count with its explicit-zero marker, and line
// wrapping. A store that never saved them reads them at their zero values,
// which is the entire-file default without migration.
func TestDiffToolbarSettingsRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Settings(); got.DiffChangesOnly || got.DiffContextLines != 0 || got.DiffContextSaved || got.DiffWrap {
		t.Fatalf("fresh store diff choices = %+v, want all zero", got)
	}
	settings := s.Settings()
	settings.DiffChangesOnly = true
	settings.DiffContextLines = 0
	settings.DiffContextSaved = true
	settings.DiffWrap = true
	if err := s.SetSettings(settings); err != nil {
		t.Fatal(err)
	}

	again, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	got := again.Settings()
	if !got.DiffChangesOnly {
		t.Fatal("DiffChangesOnly did not survive the round trip")
	}
	if got.DiffContextLines != 0 || !got.DiffContextSaved {
		t.Fatalf("explicit zero context lines = (%d, saved %v), want (0, true)", got.DiffContextLines, got.DiffContextSaved)
	}
	if !got.DiffWrap {
		t.Fatal("DiffWrap did not survive the round trip")
	}
}

// A settings struct holding only diff toolbar choices is no longer unset:
// saving one such choice is what separates a configured installation from a
// fresh one, whose defaults would otherwise overwrite the zero reading.
func TestDiffToolbarChoicesCountAsSetSettings(t *testing.T) {
	if !settingsUnset(Settings{}) {
		t.Fatal("zero settings reported as set")
	}
	for name, v := range map[string]Settings{
		"diffChangesOnly":       {DiffChangesOnly: true},
		"diffContextSavedZero":  {DiffContextSaved: true},
		"diffContextSavedCount": {DiffContextLines: 5, DiffContextSaved: true},
		"diffWrap":              {DiffWrap: true},
	} {
		if settingsUnset(v) {
			t.Fatalf("%s settings reported as unset", name)
		}
	}
}

// A settings struct holding only reference sort choices is no longer unset:
// picking one of the sort orders is a configuration the fresh-install
// defaults must not overwrite, the sidebar tag order included.
func TestRefSortChoicesCountAsSetSettings(t *testing.T) {
	if !settingsUnset(Settings{}) {
		t.Fatal("zero settings reported as set")
	}
	for name, v := range map[string]Settings{
		"localBranchSort":  {LocalBranchSort: "alpha-master-folders"},
		"remoteBranchSort": {RemoteBranchSort: "backward-folders"},
		"tagSort":          {TagSort: "backward"},
	} {
		if settingsUnset(v) {
			t.Fatalf("%s settings reported as unset", name)
		}
	}
}
