package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStashEntrySelectsDuplicateCommitByMessageAndOccurrence(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "f1.txt", "base\n")
	gitRun(t, dir, "add", "f1.txt")
	gitRun(t, dir, "commit", "-qm", "base")
	writeFile(t, dir, "f1.txt", "selected\n")
	gitRun(t, dir, "stash", "push", "-q", "-m", "seed")
	hash := stashHashAt(t, dir, 0)
	gitRun(t, dir, "stash", "drop", "-q")
	bridge := gitOut(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "update-ref", "-m", "older", "--create-reflog", "refs/stash", hash)
	gitRun(t, dir, "update-ref", "-m", "bridge", "refs/stash", bridge, hash)
	gitRun(t, dir, "update-ref", "-m", "newer", "refs/stash", hash, bridge)

	repo := New(&LocalExecutor{Dir: dir})
	stashes, err := repo.Stashes()
	if err != nil {
		t.Fatal(err)
	}
	newer, older := findStashMessage(t, stashes, hash, "newer"), findStashMessage(t, stashes, hash, "older")
	if newer.Name != "stash@{0}" || older.Name != "stash@{2}" || newer.Hash != older.Hash {
		t.Fatalf("duplicate entries %+v", stashes)
	}
	if err := repo.StashApplyEntryWith(selectionFrom(stashes, older), StashApplyOptions{Index: true}); err != nil {
		t.Fatal(err)
	}
	after, err := repo.Stashes()
	if err != nil || len(after) != 3 || findStashMessage(t, after, hash, "older").Name == "" || findStashMessage(t, after, hash, "newer").Name == "" {
		t.Fatalf("apply removed an entry %+v %v", after, err)
	}
	gitRun(t, dir, "checkout", "-q", "--", "f1.txt")

	if err := repo.StashDropEntry(selectionFrom(stashes, older)); err != nil {
		t.Fatal(err)
	}
	after, err = repo.Stashes()
	if err != nil || findStashMessage(t, after, hash, "older").Name != "" || findStashMessage(t, after, hash, "newer").Name == "" || findStashMessage(t, after, bridge, "bridge").Name == "" {
		t.Fatalf("drop selected the newer duplicate %+v %v", after, err)
	}
	if readStashFile(t, dir, "f1.txt") != "base\n" {
		t.Fatal("drop changed the working tree")
	}

	if err := repo.StashDropEntry(selectionFrom(stashes, older)); err == nil || !strings.Contains(err.Error(), "no longer in the stash") {
		t.Fatalf("missing entry error %v", err)
	}
	after, err = repo.Stashes()
	if err != nil || findStashMessage(t, after, hash, "newer").Name == "" || findStashMessage(t, after, bridge, "bridge").Name == "" {
		t.Fatalf("missing entry removed another stash %+v %v", after, err)
	}
}

func TestStashEntryFollowsHashAfterAnotherStashIsAdded(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "f1.txt", "base\n")
	gitRun(t, dir, "add", "f1.txt")
	gitRun(t, dir, "commit", "-qm", "base")
	repo := New(&LocalExecutor{Dir: dir})
	writeFile(t, dir, "f1.txt", "selected\n")
	gitRun(t, dir, "stash", "push", "-q", "-m", "selected")
	selected := stashAt(t, dir, 0)
	writeFile(t, dir, "f1.txt", "original\n")
	gitRun(t, dir, "stash", "push", "-q", "-m", "original")
	original := stashAt(t, dir, 0)

	if err := repo.StashApplyEntryWith(selectionFrom([]Stash{selected, original}, selected), StashApplyOptions{Index: true}); err != nil {
		t.Fatal(err)
	}
	if readStashFile(t, dir, "f1.txt") != "selected\n" || !stashHashPresent(t, dir, selected.Hash) || !stashHashPresent(t, dir, original.Hash) {
		t.Fatal("apply changed another entry")
	}
	gitRun(t, dir, "checkout", "-q", "--", "f1.txt")
	writeFile(t, dir, "f1.txt", "inserted\n")
	gitRun(t, dir, "stash", "push", "-q", "-m", "inserted")
	inserted := stashAt(t, dir, 0)
	if stashAt(t, dir, 2).Hash != selected.Hash {
		t.Fatal("selected stash did not move")
	}
	if err := repo.StashDropEntry(selectionFrom([]Stash{selected, original, inserted}, selected)); err != nil {
		t.Fatal(err)
	}
	if stashHashPresent(t, dir, selected.Hash) || !stashHashPresent(t, dir, original.Hash) || !stashHashPresent(t, dir, inserted.Hash) || readStashFile(t, dir, "f1.txt") != "base\n" {
		t.Fatal("drop followed the old stash index")
	}
}

func selectionFrom(stashes []Stash, selected Stash) StashSelection {
	seen := 0
	for _, stash := range stashes {
		if stash.Hash == selected.Hash && stash.Message == selected.Message {
			if stash.Name == selected.Name {
				return StashSelection{Hash: selected.Hash, Message: selected.Message, Occurrence: seen}
			}
			seen++
		}
	}
	return StashSelection{Hash: selected.Hash, Message: selected.Message}
}
func stashAt(t *testing.T, dir string, index int) Stash {
	t.Helper()
	stashes, err := New(&LocalExecutor{Dir: dir}).Stashes()
	if err != nil {
		t.Fatal(err)
	}
	for _, stash := range stashes {
		if stash.Index == index {
			return stash
		}
	}
	t.Fatalf("stash index %d missing from %+v", index, stashes)
	return Stash{}
}

func stashHashAt(t *testing.T, dir string, index int) string {
	t.Helper()
	return stashAt(t, dir, index).Hash
}

func findStashMessage(t *testing.T, stashes []Stash, hash, message string) Stash {
	t.Helper()
	for _, stash := range stashes {
		if stash.Hash == hash && stash.Message == message {
			return stash
		}
	}
	return Stash{}
}

func stashHashPresent(t *testing.T, dir, hash string) bool {
	t.Helper()
	stashes, err := New(&LocalExecutor{Dir: dir}).Stashes()
	if err != nil {
		t.Fatal(err)
	}
	for _, stash := range stashes {
		if stash.Hash == hash {
			return true
		}
	}
	return false
}

func readStashFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestStashDropEntriesResolveSameMessageBeforeDeletion(t *testing.T) {
	dir := stashBase(t)
	hash := makeStashCommit(t, dir, "same\n")
	bridge := gitOut(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "update-ref", "-m", "same", "--create-reflog", "refs/stash", hash)
	gitRun(t, dir, "update-ref", "-m", "bridge", "refs/stash", bridge, hash)
	gitRun(t, dir, "update-ref", "-m", "same", "refs/stash", hash, bridge)
	gitRun(t, dir, "update-ref", "-m", "bridge2", "refs/stash", bridge, hash)
	gitRun(t, dir, "update-ref", "-m", "same", "refs/stash", hash, bridge)
	repo := New(&LocalExecutor{Dir: dir})
	before, err := repo.Stashes()
	if err != nil {
		t.Fatal(err)
	}
	first := selectionFrom(before, findStashMessage(t, before, hash, "same"))
	var second Stash
	seen := 0
	for _, stash := range before {
		if stash.Hash == hash && stash.Message == "same" {
			if seen == 1 {
				second = stash
			}
			seen++
		}
	}
	if seen != 3 || first.Occurrence != 0 || second.Name == "" {
		t.Fatalf("same-message entries %+v", before)
	}
	secondSelection := selectionFrom(before, second)
	if secondSelection.Occurrence != 1 {
		t.Fatalf("older occurrence %d", secondSelection.Occurrence)
	}
	if err := repo.StashDropEntries([]StashSelection{first, secondSelection}); err != nil {
		t.Fatal(err)
	}
	after, err := repo.Stashes()
	if err != nil {
		t.Fatal(err)
	}
	if countMessage(after, hash, "same") != 1 || countMessage(after, bridge, "bridge") != 1 || countMessage(after, bridge, "bridge2") != 1 {
		t.Fatalf("batch drop shifted occurrence %+v", after)
	}

	before = after
	missing := selectionFrom(before, findStashMessage(t, before, hash, "same"))
	missing.Occurrence = 1
	kept := selectionFrom(before, findStashMessage(t, before, bridge, "bridge"))
	if err := repo.StashDropEntries([]StashSelection{missing, kept}); err == nil || !strings.Contains(err.Error(), "no longer") {
		t.Fatalf("missing selection error %v", err)
	}
	after, err = repo.Stashes()
	if err != nil || countMessage(after, hash, "same") != 1 || countMessage(after, bridge, "bridge") != 1 || countMessage(after, bridge, "bridge2") != 1 {
		t.Fatalf("missing selection deleted part of the batch %+v %v", after, err)
	}
}

func stashBase(t *testing.T) string {
	t.Helper()
	dir := initRepo(t)
	writeFile(t, dir, "f1.txt", "base\n")
	gitRun(t, dir, "add", "f1.txt")
	gitRun(t, dir, "commit", "-qm", "base")
	return dir
}

func makeStashCommit(t *testing.T, dir, body string) string {
	t.Helper()
	writeFile(t, dir, "f1.txt", body)
	gitRun(t, dir, "stash", "push", "-q", "-m", "seed")
	hash := stashHashAt(t, dir, 0)
	gitRun(t, dir, "stash", "drop", "-q")
	return hash
}

func countMessage(stashes []Stash, hash, message string) int {
	count := 0
	for _, stash := range stashes {
		if stash.Hash == hash && stash.Message == message {
			count++
		}
	}
	return count
}
