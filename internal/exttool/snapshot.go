package exttool

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// dirPrefix is the name prefix of every snapshot folder, inside the parent
// directory the app chooses. CleanStale uses it to tell its own folders
// from anything else in the same parent.
const dirPrefix = "branchline-exttool-"

// Snapshot is one private folder holding the versions handed to an external
// tool. Everything inside belongs to this Mac's user only: the folder is
// 0700 and the files are never group- or world-readable, because a file in
// the repository can contain anything.
type Snapshot struct {
	dir string
}

// NewSnapshot creates an empty snapshot folder inside parent.
func NewSnapshot(parent string) (*Snapshot, error) {
	dir, err := os.MkdirTemp(parent, dirPrefix)
	if err != nil {
		return nil, err
	}
	return &Snapshot{dir: dir}, nil
}

// Write stores data as one version of repoPath under role, which is one of
// "local", "remote", "base" or "merged". The file is named after the file
// name of repoPath inside the role's own folder, so a tool shows the user
// the name they know, while a hostile repository path cannot escape the
// snapshot folder. writable makes the file 0600 instead of 0400; only the
// merged version needs it. Write returns the file's absolute path.
func (s *Snapshot) Write(role, repoPath string, data []byte, writable bool) (string, error) {
	switch role {
	case "local", "remote", "base", "merged":
	default:
		return "", fmt.Errorf("unknown role %q", role)
	}
	name := path.Base(repoPath)
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	dir := filepath.Join(s.dir, role)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	perm := os.FileMode(0o400)
	if writable {
		perm = 0o600
	}
	file := filepath.Join(dir, name)
	if err := os.WriteFile(file, data, perm); err != nil {
		return "", err
	}
	// The process umask can only remove permission bits from the mode
	// above, so a writable snapshot file would otherwise come out
	// unwritable.
	if err := os.Chmod(file, perm); err != nil {
		return "", err
	}
	return file, nil
}

// Dir is the snapshot folder's absolute path.
func (s *Snapshot) Dir() string {
	return s.dir
}

// Remove deletes the snapshot folder and everything in it, including the
// read-only files.
func (s *Snapshot) Remove() error {
	return os.RemoveAll(s.dir)
}

// CleanStale deletes snapshot folders inside parent that are older than
// olderThan, measured by the folder's modification time. It is meant to run
// when the app starts, to clean up after a run that ended before its
// snapshot could be removed. Files and folders whose name does not start
// with the snapshot prefix are left alone. A missing parent is not an
// error. The count is how many folders were deleted.
func CleanStale(parent string, olderThan time.Duration) (int, error) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	cutoff := time.Now().Add(-olderThan)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), dirPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return removed, err
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(parent, entry.Name())); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
