package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSheetChoicesSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Sheet(PushAllTags, true) || s.Sheet(PullStashAndReapply, false) {
		t.Fatal("defaults")
	}
	if err := s.SetSheet(PushAllTags, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSheet(PullStashAndReapply, true); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Sheet(PushAllTags, true) || !again.Sheet(PullStashAndReapply, false) {
		t.Fatal("the choices were not kept")
	}
}

func TestSheetSaveFailureCanBeRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSheet(PushAllTags, true); err != nil {
		t.Fatal(err)
	}
	// A directory at the destination reliably refuses replacement, even as root.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	values := map[string]bool{PushAllTags: false, PullRebase: true}
	if err := s.SetSheets(values); err == nil {
		t.Fatal("expected save failure")
	}
	if !s.Sheet(PushAllTags, false) || s.Sheet(PullRebase, false) {
		t.Fatal("failed save leaked into memory")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSheets(values); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Sheet(PushAllTags, true) || !reopened.Sheet(PullRebase, false) {
		t.Fatal("retry skipped persistence")
	}
}
