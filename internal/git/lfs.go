package git

import (
	"fmt"
	"strings"
)

func (r *Repo) LFSInstall() error {
	_, err := r.run(quick, "lfs", "install", "--local")
	return err
}

// LFSTrack records a Git LFS pattern in .gitattributes and stages that file.
// Git LFS writes the pattern itself; this does not copy file contents.
func (r *Repo) LFSTrack(pattern string) error {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return fmt.Errorf("a Git LFS pattern is empty")
	}
	if _, err := r.run(quick, "lfs", "track", "--", pattern); err != nil {
		return err
	}
	return r.Stage(".gitattributes")
}

// LFSFetch downloads every Git LFS object from remote. An empty remote
// uses the remote Git LFS would choose itself.
func (r *Repo) LFSFetch(remote string) error {
	args := []string{"lfs", "fetch", "--all"}
	if remote = strings.TrimSpace(remote); remote != "" {
		args = append(args, remote)
	}
	_, err := r.run(slow, args...)
	return err
}

// LFSPull downloads Git LFS objects for the checked-out revision from remote
// and updates the working copy. An empty remote lets Git LFS choose.
func (r *Repo) LFSPull(remote string) error {
	args := []string{"lfs", "pull"}
	if remote = strings.TrimSpace(remote); remote != "" {
		args = append(args, remote)
	}
	_, err := r.run(slow, args...)
	return err
}

func (r *Repo) LFSPrune() error {
	_, err := r.run(slow, "lfs", "prune")
	return err
}

// LFSLock locks path on the Git LFS server. LFSUnlock removes that lock.
// force removes a lock held by someone else when the server allows it.
func (r *Repo) LFSLock(path string, unlock, force bool) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("a Git LFS lock needs a file")
	}
	verb := "lock"
	if unlock {
		verb = "unlock"
	}
	args := []string{"lfs", verb}
	if unlock && force {
		args = append(args, "--force")
	}
	args = append(args, "--", path)
	_, err := r.run(slow, args...)
	return err
}

// LFSFile is one path Git LFS tracks. Present is false when the
// pointer is checked out but the object is not in the local cache.
// Owner is who holds the lock, when one is held.
type LFSFile struct {
	Path    string
	ID      string
	Present bool
	Owner   string
}

// LFSFiles lists every path Git LFS tracks in any revision, and whether
// its object is present. Locks are attached when the server answers;
// a repository with no lock server still returns its files.
func (r *Repo) LFSFiles() ([]LFSFile, error) {
	out, err := r.run(slow, "lfs", "ls-files", "--all", "-l")
	if err != nil {
		return nil, err
	}
	files := ParseLFSFiles(out)
	locks, lockErr := r.run(slow, "lfs", "locks", "--json")
	if lockErr == nil {
		files = applyLFSLocks(files, locks)
	}
	return files, nil
}
