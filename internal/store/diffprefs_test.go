package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The FileListTree choice of one repository starts unset — the flat list —
// survives a round trip through UpdateLocal and the JSON file under the
// fileListTree key, and leaves every other repository setting untouched.
func TestDiffPrefsFileListTreeRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "store.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Add(Repo{Kind: Local, Path: filepath.Join(t.TempDir(), "repo")})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Local.Diff.FileListTree; got {
		t.Fatal("fresh FileListTree = true, want the flat-list zero value")
	}

	local := r.Local
	local.Diff.ContextLines = 5
	local.Diff.ContextSaved = true
	if err := s.UpdateLocal(r.ID, local); err != nil {
		t.Fatal(err)
	}
	current, ok := s.Get(r.ID)
	if !ok {
		t.Fatal("repository missing after UpdateLocal")
	}
	local = current.Local
	local.Diff.FileListTree = true
	if err := s.UpdateLocal(r.ID, local); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get(r.ID)
	if !ok {
		t.Fatal("repository missing after reopening the store")
	}
	if !got.Local.Diff.FileListTree {
		t.Fatal("FileListTree did not survive the round trip")
	}
	if got.Local.Diff.ContextLines != 5 || !got.Local.Diff.ContextSaved {
		t.Fatalf("the FileListTree save clobbered the other diff preferences: %+v", got.Local.Diff)
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"fileListTree": true`) {
		t.Fatalf("store JSON missing the fileListTree key: %s", raw)
	}
}

// A store whose repository never chose the tree view serializes no key, and
// an unknown repository's update fails without touching the saved file.
func TestDiffPrefsFileListTreeZeroOmitsKeyAndUnknownRepoFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "store.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Add(Repo{Kind: Local, Path: filepath.Join(t.TempDir(), "repo")})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateLocal(r.ID, r.Local); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fileListTree") {
		t.Fatalf("the zero choice wrote a fileListTree key: %s", raw)
	}
	if err := s.UpdateLocal("no-such-repo", RepoSettings{}); err == nil {
		t.Fatal("updating an unknown repository succeeded")
	}
}
