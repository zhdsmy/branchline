package store

import (
	"os"
	"path/filepath"
	"testing"
)

// Rename persists the trimmed display name: it survives a reopen of the
// store file, while the path, host and the rest of the repository's metadata
// stay as they were.
func TestStoreRenameSuccess(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Add(Repo{Kind: SSH, Host: "box", User: "me", Path: "/srv/app", Name: "old name"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(r.ID, "  new name  "); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Get(r.ID); !ok || got.Name != "new name" {
		t.Fatalf("memory name = %+v (found %v), want trimmed new name", got, ok)
	}

	disk, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := disk.Get(r.ID)
	if !ok {
		t.Fatal("renamed repository missing after reopen")
	}
	if got.Name != "new name" {
		t.Fatalf("disk name = %q, want %q", got.Name, "new name")
	}
	if got.Path != r.Path || got.Host != r.Host || got.User != r.User {
		t.Fatalf("rename changed metadata: %+v, was %+v", got, r)
	}
}

// Rename of an ID the store does not hold is an error and writes nothing:
// the file keeps the entries it had.
func TestStoreRenameUnknownID(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Add(Repo{Kind: Local, Path: "/work/app", Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("no-such-id", "other"); err == nil {
		t.Fatal("renaming an unknown repository succeeded")
	}
	got, ok := s.Get(r.ID)
	if !ok || got.Name != "app" {
		t.Fatalf("existing entry changed by the failed rename: %+v (found %v)", got, ok)
	}
	disk, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := disk.Get(r.ID); !ok || got.Name != "app" {
		t.Fatalf("disk entry changed by the failed rename: %+v (found %v)", got, ok)
	}
}

// When the save fails, Rename reports the failure and the in-memory name
// rolls back to what the file still holds, so the list never shows a name
// the disk does not.
func TestStoreRenameSaveFailureRollsBack(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Add(Repo{Kind: Local, Path: "/work/app", Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	// A directory at the temporary file's path blocks the write, without
	// touching permissions.
	if err := os.Mkdir(file+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(r.ID, "blocked"); err == nil {
		t.Fatal("a blocked rename succeeded")
	}
	if got, ok := s.Get(r.ID); !ok || got.Name != "app" {
		t.Fatalf("memory name after the failed save = %+v (found %v), want the old name", got, ok)
	}
	disk, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := disk.Get(r.ID); !ok || got.Name != "app" {
		t.Fatalf("disk name after the failed save = %+v (found %v), want the old name", got, ok)
	}

	// With the block gone the rename applies, so the test does not pass
	// merely because renaming is broken entirely.
	if err := os.Remove(file + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(r.ID, "unblocked"); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Get(r.ID); !ok || got.Name != "unblocked" {
		t.Fatalf("memory name after the unblocked save = %+v (found %v)", got, ok)
	}
}
