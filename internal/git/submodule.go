package git

import (
	"fmt"
	"strings"
)

func (r *Repo) SubmoduleUpdate() error {
	return r.SubmoduleUpdatePath("")
}

// SubmoduleUpdatePath clones a missing submodule and checks out the
// recorded commit. An empty path updates every submodule.
func (r *Repo) SubmoduleUpdatePath(path string) error {
	args := []string{"submodule", "update", "--init", "--recursive"}
	if path != "" {
		args = append(args, "--", path)
	}
	_, err := r.run(slow, args...)
	return err
}

// SubmoduleInit registers a submodule from .gitmodules without cloning it.
// An empty path registers every submodule.
func (r *Repo) SubmoduleInit(path string) error {
	args := []string{"submodule", "init"}
	if path != "" {
		args = append(args, "--", path)
	}
	_, err := r.run(quick, args...)
	return err
}

// SubmoduleSync copies the URL recorded in .gitmodules into the local
// submodule configuration. Init runs first, because sync has nothing to
// update until the submodule is registered. An empty path covers all of them.
// SubmoduleAdd clones url into path and records it in .gitmodules.
// The change is staged; it is not committed.
func (r *Repo) SubmoduleAdd(url, path string) error {
	url, path = strings.TrimSpace(url), strings.TrimSpace(path)
	if url == "" || path == "" {
		return fmt.Errorf("a submodule needs a URL and a path")
	}
	// A local path is cloned through the file transport. Git 2.38.1
	// rejects it unless the command itself allows the transport.
	_, err := r.run(slow, "-c", "protocol.file.allow=always", "submodule", "add", "--", url, path)
	return err
}

// SubmoduleDeinit unregisters a submodule and removes its working tree.
// The gitlink in the index stays until the change is committed. force is
// required when the submodule has changes that are not committed.
func (r *Repo) SubmoduleDeinit(path string, force bool) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("a submodule path is required")
	}
	args := []string{"submodule", "deinit"}
	if force {
		args = append(args, "--force")
	}
	_, err := r.run(slow, append(args, "--", path)...)
	if err != nil && !force {
		// A submodule that was just added has that addition staged.
		// Git calls it a local modification; removing it is still safe
		// because the recorded commit is what was cloned.
		_, err = r.run(slow, "submodule", "deinit", "--force", "--", path)
	}
	return err
}

func (r *Repo) SubmoduleSync(path string) error {
	if err := r.SubmoduleInit(path); err != nil {
		return err
	}
	args := []string{"submodule", "sync", "--recursive"}
	if path != "" {
		args = append(args, "--", path)
	}
	_, err := r.run(quick, args...)
	return err
}

// Superproject returns the working tree of the repository this one is a
// submodule of, and this one's path in it; empty when it is not one.
func (r *Repo) Superproject() (parent, path string, err error) {
	out, err := r.run(quick, "rev-parse", "--show-superproject-working-tree", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 2 || lines[0] == "" {
		return "", "", nil
	}
	parent, top := lines[0], lines[1]
	if !strings.HasPrefix(top, parent+"/") {
		return "", "", nil
	}
	return parent, strings.TrimPrefix(top, parent+"/"), nil
}

// SyncSubmoduleURL is Edit Remote's Sync submodule URL with parent
// repository: the parent's .gitmodules gets url for this submodule, and
// git submodule sync copies it to the parent's configuration. git runs
// in the parent through the same executor, so a server's works too.
func (r *Repo) SyncSubmoduleURL(url string) error {
	url = strings.TrimSpace(url)
	parent, path, err := r.Superproject()
	if err != nil {
		return err
	}
	if parent == "" {
		return fmt.Errorf("this repository is not a submodule")
	}
	out, err := r.run(quick, "-C", parent, "config", "--file", ".gitmodules", "--null", "--get-regexp", `^submodule\..*\.path$`)
	if err != nil {
		return fmt.Errorf("the parent repository lists no submodules: %w", err)
	}
	name := ""
	for _, entry := range strings.Split(string(out), "\x00") {
		key, value, ok := strings.Cut(entry, "\n")
		if ok && strings.TrimSpace(value) == path {
			name = strings.TrimSuffix(strings.TrimPrefix(key, "submodule."), ".path")
			break
		}
	}
	if name == "" {
		return fmt.Errorf("the parent repository has no submodule at %s", path)
	}
	if _, err := r.run(quick, "-C", parent, "config", "--file", ".gitmodules", "submodule."+name+".url", url); err != nil {
		return err
	}
	_, err = r.run(quick, "-C", parent, "submodule", "sync", "--", path)
	return err
}
