package store

import (
	"os"
	"path/filepath"
	"testing"
)

// openRevStore opens an empty store in a temporary directory.
func openRevStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "repositories.json"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return s
}

// Every save of the store advances the revision a caching reader watches,
// and the reads it uses to fill its cache leave it alone.
func TestReposRevMutationsAndReads(t *testing.T) {
	s := openRevStore(t)

	rev := s.ReposRev()
	// Reads do not advance the revision.
	s.Repos()
	s.ReposWithRev()
	if got := s.ReposRev(); got != rev {
		t.Fatalf("Repos/ ReposWithRev advanced the revision from %d to %d", rev, got)
	}

	added, err := s.Add(Repo{Kind: Local, Path: "/nonexistent/alpha", Name: "alpha"})
	if err != nil {
		t.Fatalf("add alpha: %v", err)
	}
	if next := s.ReposRev(); next == rev {
		t.Fatal("Add did not advance the revision")
	} else {
		rev = next
	}

	// Get is a read of one repository.
	if _, ok := s.Get(added.ID); !ok {
		t.Fatal("Get lost the repository Add returned")
	}
	if got := s.ReposRev(); got != rev {
		t.Fatalf("Get advanced the revision from %d to %d", rev, got)
	}

	if err := s.Touch(added.ID); err != nil {
		t.Fatalf("touch alpha: %v", err)
	}
	if next := s.ReposRev(); next == rev {
		t.Fatal("Touch did not advance the revision")
	} else {
		rev = next
	}

	if err := s.SetWorkspace(added.ID, "work"); err != nil {
		t.Fatalf("set workspace: %v", err)
	}
	if next := s.ReposRev(); next == rev {
		t.Fatal("SetWorkspace did not advance the revision")
	} else {
		rev = next
	}

	if err := s.UpdateLocal(added.ID, RepoSettings{MainBranch: "main"}); err != nil {
		t.Fatalf("update local: %v", err)
	}
	if next := s.ReposRev(); next == rev {
		t.Fatal("UpdateLocal did not advance the revision")
	} else {
		rev = next
	}

	if err := s.Remove(added.ID); err != nil {
		t.Fatalf("remove alpha: %v", err)
	}
	if next := s.ReposRev(); next == rev {
		t.Fatal("Remove did not advance the revision")
	} else {
		rev = next
	}

	// The revision is strictly monotonic: it never falls back, so a cache
	// keyed on it cannot mistake an older list for a newer one.
	if rev <= 0 {
		t.Fatalf("revision after the mutations is %d, want it above zero", rev)
	}
}

// ReposWithRev copies the sorted list and the revision in one lock, so the
// pair is consistent: the revision it reports is the one the copy is fresh
// for, and the copy matches what Repos returns.
func TestReposWithRevConsistentPair(t *testing.T) {
	s := openRevStore(t)
	if _, err := s.Add(Repo{Kind: Local, Path: "/nonexistent/alpha", Name: "alpha"}); err != nil {
		t.Fatalf("add alpha: %v", err)
	}
	if _, err := s.Add(Repo{Kind: Local, Path: "/nonexistent/beta", Name: "beta"}); err != nil {
		t.Fatalf("add beta: %v", err)
	}

	list, rev := s.ReposWithRev()
	if rev != s.ReposRev() {
		t.Fatalf("ReposWithRev reported revision %d, ReposRev now reads %d with no write in between", rev, s.ReposRev())
	}
	if len(list) != 2 {
		t.Fatalf("ReposWithRev returned %d repositories, want 2", len(list))
	}
	// beta was added after alpha, so it opened last and sorts first.
	if list[0].Name != "beta" || list[1].Name != "alpha" {
		t.Fatalf("ReposWithRev order = %q, %q, want beta, alpha", list[0].Name, list[1].Name)
	}

	// A mutation invalidates the pair.
	if err := s.Touch(list[1].ID); err != nil {
		t.Fatalf("touch alpha: %v", err)
	}
	if s.ReposRev() == rev {
		t.Fatal("the revision did not move after Touch, a cache on the pair could not tell it stale")
	}
	list, _ = s.ReposWithRev()
	if list[0].Name != "alpha" {
		t.Fatalf("after touching alpha the order is %q first, want alpha", list[0].Name)
	}
}

// The writes outside the five repository operations the revision was made
// for save through the same path, so each of them advances it too.
func TestReposRevOtherMutations(t *testing.T) {
	s := openRevStore(t)

	check := func(name string, mutate func() error) {
		t.Helper()
		rev := s.ReposRev()
		if err := mutate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := s.ReposRev(); got == rev {
			t.Fatalf("%s did not advance the revision", name)
		}
	}

	added, err := s.Add(Repo{Kind: Local, Path: "/nonexistent/alpha", Name: "alpha"})
	if err != nil {
		t.Fatalf("add alpha: %v", err)
	}

	check("Rename", func() error { return s.Rename(added.ID, "renamed") })
	check("AddMany", func() error {
		_, err := s.AddMany([]string{"/nonexistent/beta"})
		return err
	})
	check("RenameWorkspace", func() error {
		if err := s.AddWorkspace("old"); err != nil {
			return err
		}
		return s.RenameWorkspace("old", "new")
	})
	check("DeleteWorkspace", func() error { return s.DeleteWorkspace("new") })
}

// A save that fails advances the revision all the same: ReposWithRev then
// hands a caching reader the rolled-back list, which is the state the store
// stands in, and the revision tells the reader its copy went stale.
func TestReposRevAdvancesOnFailedSave(t *testing.T) {
	s := openRevStore(t)
	added, err := s.Add(Repo{Kind: Local, Path: "/nonexistent/alpha", Name: "alpha"})
	if err != nil {
		t.Fatalf("add alpha: %v", err)
	}

	// A file where the store's parent directory must be fails every write.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	s.path = filepath.Join(blocker, "repositories.json")

	rev := s.ReposRev()
	if err := s.Remove(added.ID); err == nil {
		t.Fatal("remove into an unwritable store succeeded, want an error")
	}
	if got := s.ReposRev(); got == rev {
		t.Fatal("a failed save did not advance the revision")
	}

	list, _ := s.ReposWithRev()
	if len(list) != 1 || list[0].ID != added.ID {
		t.Fatalf("after the rolled-back remove ReposWithRev holds %d repositories, want alpha alone", len(list))
	}
}
