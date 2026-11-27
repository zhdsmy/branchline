package store

import (
	"path/filepath"
	"testing"
)

func TestRenameKeepsRepositoryMetadata(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Add(Repo{Kind: SSH, Host: "box", Path: "/srv/app", Name: "Old", Workspace: "Work", Local: RepoSettings{TrustShared: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(r.ID, "  New  "); err != nil {
		t.Fatal(err)
	}
	again, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := again.Get(r.ID)
	if !ok || got.Name != "New" || got.Path != r.Path || got.Host != r.Host || got.Workspace != r.Workspace || !got.Local.TrustShared || !got.Opened.Equal(r.Opened) {
		t.Fatalf("renamed %+v from %+v", got, r)
	}
	if err := s.Rename(r.ID, "  "); err == nil {
		t.Fatal("accepted an empty name")
	}
	if err := s.Rename("missing", "Name"); err == nil {
		t.Fatal("accepted an unknown repository")
	}
}
