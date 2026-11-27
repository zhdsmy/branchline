package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWelcomedAndDeleted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Welcomed() {
		t.Fatal("a new store was welcomed")
	}
	if err := s.SetWelcomed(); err != nil {
		t.Fatal(err)
	}
	n, err := s.AddMany([]string{"/src/a", "/src/b/", "/src/a"})
	if err != nil || n != 2 {
		t.Fatalf("added %d %v", n, err)
	}
	if n, _ := s.AddMany([]string{"/src/b"}); n != 0 {
		t.Fatalf("added a known path again: %d", n)
	}
	var b Repo
	for _, r := range s.Repos() {
		if r.Path == "/src/b" {
			b = r
		}
	}
	if b.ID == "" || b.Name != "b" {
		t.Fatalf("repos %+v", s.Repos())
	}
	if err := s.RemoveDeleted(b.ID); err != nil {
		t.Fatal(err)
	}
	again, _ := Open(path)
	if !again.Welcomed() || len(again.Repos()) != 1 || len(again.Deleted()) != 1 || again.Deleted()[0] != "/src/b" {
		t.Fatalf("reopened: welcomed %v repos %+v deleted %q", again.Welcomed(), again.Repos(), again.Deleted())
	}
	if n, _ := again.AddMany([]string{"/src/b"}); n != 1 || len(again.Deleted()) != 0 {
		t.Fatalf("adding a deleted path back: %d, deleted %q", n, again.Deleted())
	}
	_ = again.RemoveDeleted(again.Repos()[0].ID)
	if err := again.ResetDeleted(); err != nil || len(again.Deleted()) != 0 {
		t.Fatalf("reset: %q %v", again.Deleted(), err)
	}
}

func TestRemoveDeletedRollbackAndRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Add(Repo{Kind: Local, Path: "/src/a", Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Add(Repo{Kind: Local, Path: "/src/b", Name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTabSession([]string{a.ID, b.ID}, a.ID, true); err != nil {
		t.Fatal(err)
	}

	// Block saving so RemoveDeleted fails atomically under one lock.
	if err := os.Mkdir(path+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveDeleted(a.ID); err == nil {
		t.Fatal("RemoveDeleted succeeded while save was blocked")
	}
	if got := s.Deleted(); len(got) != 0 {
		t.Fatalf("failed RemoveDeleted left Deleted in memory: %v", got)
	}
	if err := os.Remove(path + ".tmp"); err != nil {
		t.Fatal(err)
	}
	// An unrelated save after the failed RemoveDeleted must not persist "/src/a" in Deleted.
	if err := s.SetAppearance("light"); err != nil {
		t.Fatal(err)
	}
	disk, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(disk.Deleted()) != 0 || len(disk.Repos()) != 2 {
		t.Fatalf("disk after unrelated save: deleted=%v repos=%+v", disk.Deleted(), disk.Repos())
	}
	if active, set := disk.ActiveTab(); active != a.ID || !set || !reflect.DeepEqual(disk.OpenTabs(), []string{a.ID, b.ID}) {
		t.Fatalf("session after failed RemoveDeleted: active=%q set=%v open=%v", active, set, disk.OpenTabs())
	}

	// Retrying RemoveDeleted now removes the repo, updates session, and records Deleted in one save.
	if err := s.RemoveDeleted(a.ID); err != nil {
		t.Fatal(err)
	}
	disk, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(disk.Deleted(), []string{"/src/a"}) || len(disk.Repos()) != 1 || disk.Repos()[0].ID != b.ID {
		t.Fatalf("disk after retry: deleted=%v repos=%+v", disk.Deleted(), disk.Repos())
	}
	if active, set := disk.ActiveTab(); active != "" || !set || !reflect.DeepEqual(disk.OpenTabs(), []string{b.ID}) {
		t.Fatalf("session after retry: active=%q set=%v open=%v", active, set, disk.OpenTabs())
	}
}
