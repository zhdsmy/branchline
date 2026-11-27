package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckBinaryTellsGitFromAnythingElse(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "git")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'git version 2.99.1'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if v, err := CheckBinary(fake); err != nil || v != "2.99.1" {
		t.Fatalf("version = %q, err = %v", v, err)
	}
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("#!/bin/sh\necho hello\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{other, filepath.Join(dir, "missing"), dir} {
		_, err := CheckBinary(bad)
		if err == nil || !strings.Contains(err.Error(), "is not a valid Git binary.") {
			t.Errorf("CheckBinary(%s) err = %v", bad, err)
		}
	}
	found := FindInstances(fake, fake, other)
	if len(found) == 0 || found[0].Path != fake || found[0].Version != "2.99.1" {
		t.Fatalf("FindInstances = %+v", found)
	}
	for _, in := range found {
		if in.Path == other {
			t.Fatalf("a program that is not git is listed: %+v", found)
		}
	}
}
