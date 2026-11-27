package store

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestWorkspaces(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	api, _ := s.Add(Repo{Kind: Local, Path: "/w/api", Workspace: " Work / Backend "})
	notes, _ := s.Add(Repo{Kind: Local, Path: "/h/notes", Workspace: "Home"})

	if err := s.AddWorkspace("Empty"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddWorkspace("Home"); err == nil {
		t.Fatal("a second workspace named Home was added")
	}
	if err := s.AddWorkspace(" / "); err == nil {
		t.Fatal("a workspace with no name was added")
	}
	want := []string{"Empty", "Home", "Work/Backend"}
	if got := s.Workspaces(); !reflect.DeepEqual(got, want) {
		t.Fatalf("workspaces %v, want %v", got, want)
	}

	// Renaming a folder moves what is nested in it, and the repositories.
	if err := s.AddWorkspace("Work/Frontend"); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameWorkspace("Work", "Job"); err != nil {
		t.Fatal(err)
	}
	want = []string{"Empty", "Home", "Job/Backend", "Job/Frontend"}
	if got := s.Workspaces(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after rename %v, want %v", got, want)
	}
	if r, _ := s.Get(api.ID); r.Workspace != "Job/Backend" {
		t.Fatalf("repository workspace %q", r.Workspace)
	}
	if err := s.RenameWorkspace("Empty", "Home"); err == nil {
		t.Fatal("renamed onto an existing workspace")
	}

	// Deleting a workspace leaves its repositories ungrouped.
	if err := s.DeleteWorkspace("Job"); err != nil {
		t.Fatal(err)
	}
	want = []string{"Empty", "Home"}
	if got := s.Workspaces(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after delete %v, want %v", got, want)
	}
	if r, ok := s.Get(api.ID); !ok || r.Workspace != "" {
		t.Fatalf("repository after delete %+v", r)
	}
	if r, _ := s.Get(notes.ID); r.Workspace != "Home" {
		t.Fatalf("another workspace changed: %+v", r)
	}

	// The empty workspace survives a restart.
	again, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Workspaces(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after reopening %v, want %v", got, want)
	}
}

func TestWorkspaceNamesAreCanonicalWhenAssigned(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Add(Repo{Kind: Local, Path: "/app", Workspace: " Team / First "})
	if err != nil {
		t.Fatal(err)
	}
	if r.Workspace != "Team/First" {
		t.Fatalf("added workspace %q", r.Workspace)
	}
	if err := s.SetWorkspace(r.ID, " / Team / Second / "); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(r.ID)
	if got.Workspace != "Team/Second" {
		t.Fatalf("assigned workspace %q", got.Workspace)
	}
	if name := WorkspaceName(" / Team / Second / "); name != got.Workspace {
		t.Fatalf("name %q", name)
	}
}
