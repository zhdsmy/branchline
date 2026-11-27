package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests run in a per-process temporary directory so that the leak
// checks counting directories there cannot trip over other test processes
// sharing the system default. TestMain redirects TMPDIR before m.Run.
func TestTempDirIsPerProcess(t *testing.T) {
	tempDir := os.TempDir()
	if base := filepath.Base(tempDir); !strings.HasPrefix(base, "branchline-apptest-") {
		t.Fatalf("os.TempDir() base = %q, want prefix %q (full path %q)", base, "branchline-apptest-", tempDir)
	}
	if parent, want := filepath.Dir(tempDir), filepath.Clean(originalTempDir); parent != want {
		t.Fatalf("os.TempDir() parent = %q, want the original temporary directory %q", parent, want)
	}
}
