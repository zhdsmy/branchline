package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func snapshotSettings() Settings {
	return Settings{
		SourceFolders: []string{"sources"}, SSHKeys: []string{"active"}, SSHKeyFiles: []string{"known"},
		MergeTools: []MergeTool{{Name: "merge"}},
		Commands: []CustomCommand{{Name: "command", UI: &CommandUI{
			Title: "dialog", Controls: []CommandControl{{Name: "choice", Options: []string{"first"}}},
			Buttons: []CommandButton{{Title: "Run"}},
		}}},
	}
}

func changeSnapshotSettings(v Settings) {
	v.SourceFolders[0] = "changed"
	v.SSHKeys[0] = "changed"
	v.SSHKeyFiles[0] = "changed"
	v.MergeTools[0].Name = "changed"
	v.Commands[0].Name = "changed"
	v.Commands[0].UI.Title = "changed"
	v.Commands[0].UI.Controls[0].Name = "changed"
	v.Commands[0].UI.Controls[0].Options[0] = "changed"
	v.Commands[0].UI.Buttons[0].Title = "changed"
}

func snapshotLocal() RepoSettings {
	yes := func() *bool { v := true; return &v }
	return RepoSettings{
		NoAutoFetch: []string{"origin"}, SidebarClosed: []string{"folder"},
		SidebarSections: SidebarSections{Branches: yes(), Remotes: yes(), Tags: yes(), Stashes: yes(), Worktrees: yes(), Submodules: yes()},
		IssueRules:      []IssueRule{{Pattern: "pattern", URL: "url"}},
		RemoteAccounts:  []RemoteAccount{{Remote: "origin", Account: "account"}},
	}
}

func changeSnapshotLocal(v RepoSettings) {
	for _, ptr := range []*bool{v.SidebarSections.Branches, v.SidebarSections.Remotes, v.SidebarSections.Tags, v.SidebarSections.Stashes, v.SidebarSections.Worktrees, v.SidebarSections.Submodules} {
		*ptr = false
	}
	v.NoAutoFetch[0] = "changed"
	v.SidebarClosed[0] = "changed"
	v.IssueRules[0].Pattern = "changed"
	v.RemoteAccounts[0].Account = "changed"
}

func TestSettingsOwnCollections(t *testing.T) {
	s, _ := Open("")
	input := snapshotSettings()
	if err := s.SetSettings(input); err != nil {
		t.Fatal(err)
	}
	expected := s.Settings()
	changeSnapshotSettings(input)
	if got := s.Settings(); !reflect.DeepEqual(got, expected) {
		t.Fatalf("setter retained caller collections: %+v", got)
	}
	edited := s.Settings()
	changeSnapshotSettings(edited)
	if got := s.Settings(); !reflect.DeepEqual(got, expected) {
		t.Fatalf("editing returned snapshot changed stored settings: %+v", got)
	}
}

func TestRepositoryOwnsCollections(t *testing.T) {
	s, _ := Open("")
	input := Repo{Kind: Local, Path: "/repo", Local: snapshotLocal()}
	added, err := s.Add(input)
	if err != nil {
		t.Fatal(err)
	}
	changeSnapshotLocal(input.Local)
	changeSnapshotLocal(added.Local)
	again, err := s.Add(Repo{Kind: Local, Path: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	changeSnapshotLocal(again.Local)
	changeSnapshotLocal(s.Repos()[0].Local)
	got, _ := s.Get(added.ID)
	changeSnapshotLocal(got.Local)
	if current, _ := s.Get(added.ID); !reflect.DeepEqual(current.Local, snapshotLocal()) {
		t.Fatalf("repository collections shared: %+v", current.Local)
	}
	replacement := snapshotLocal()
	replacement.NoAutoFetch[0] = "upstream"
	if err := s.UpdateLocal(added.ID, replacement); err != nil {
		t.Fatal(err)
	}
	expected := cloneRepoSettings(replacement)
	changeSnapshotLocal(replacement)
	if current, _ := s.Get(added.ID); !reflect.DeepEqual(current.Local, expected) {
		t.Fatalf("UpdateLocal retained caller collections: %+v", current.Local)
	}
}

// A directory at the temporary file path forces a real write failure without
// relying on Unix permissions, which behave differently under root.
func blockSnapshotSave(t *testing.T, s *Store) func() {
	t.Helper()
	p := s.path + ".tmp"
	if err := os.Mkdir(p, 0700); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSettingsFailedSaveRollsBackAndRetries(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	initial := s.Settings()
	unblock := blockSnapshotSave(t, s)
	if err := s.SetSettings(Settings{}); err == nil {
		t.Fatal("expected failed first save")
	}
	if s.data.SettingsSaved || !reflect.DeepEqual(s.Settings(), initial) {
		t.Fatal("failed first save changed defaults or explicit-saved flag")
	}
	unblock()
	if err := s.SetSettings(snapshotSettings()); err != nil {
		t.Fatal(err)
	}
	previous := s.Settings()
	unblock = blockSnapshotSave(t, s)
	if err := s.SetSettings(Settings{AutoFetch: true}); err == nil {
		t.Fatal("expected failed replacement")
	}
	if !reflect.DeepEqual(s.Settings(), previous) {
		t.Fatal("failed save changed live settings")
	}
	disk, err := Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(disk.Settings(), previous) {
		t.Fatal("failed save changed persisted settings")
	}
	unblock()
	if err := s.SetSettings(Settings{AutoFetch: true}); err != nil {
		t.Fatal(err)
	}
	disk, err = Open(s.path)
	if err != nil || !disk.Settings().AutoFetch {
		t.Fatalf("retry not persisted: %v", err)
	}
}

func TestRepositoryFailedSaveRollsBackAndRetries(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Add(Repo{Kind: Local, Path: "/repo", Local: snapshotLocal()})
	if err != nil {
		t.Fatal(err)
	}
	beforeDisk, err := Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	unblock := blockSnapshotSave(t, s)
	if _, err := s.Add(Repo{Kind: Local, Path: "/another"}); err == nil {
		t.Fatal("expected Add failure")
	}
	if _, err := s.Add(Repo{Kind: Local, Path: "/repo", Name: "replacement"}); err == nil {
		t.Fatal("expected existing Add failure")
	}
	if err := s.UpdateLocal(first.ID, RepoSettings{MainBranch: "changed"}); err == nil {
		t.Fatal("expected UpdateLocal failure")
	}
	if got := s.Repos(); !reflect.DeepEqual(got, []Repo{first}) {
		t.Fatalf("failed writes changed repositories: %+v", got)
	}
	disk, err := Open(s.path)
	if err != nil || !reflect.DeepEqual(disk.Repos(), beforeDisk.Repos()) {
		t.Fatalf("failed writes changed disk: %v", err)
	}
	unblock()
	next := RepoSettings{MainBranch: "changed"}
	if err := s.UpdateLocal(first.ID, next); err != nil {
		t.Fatal(err)
	}
	disk, err = Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := disk.Get(first.ID); !reflect.DeepEqual(got.Local, next) {
		t.Fatal("retry not persisted")
	}
}
