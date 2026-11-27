package git

import (
	"fmt"
	"strings"
)

// DiscardSubmodule restores a registered submodule's working tree and
// removes its untracked files, then checks out the commit in the parent's
// index. Every command goes through the parent's executor, also over SSH.
func (r *Repo) DiscardSubmodule(path string) error {
	subs, err := r.Submodules()
	if err != nil {
		return err
	}
	registered := false
	for _, sub := range subs {
		registered = registered || sub == path
	}
	if !registered {
		return fmt.Errorf("not a registered submodule: %s", path)
	}
	// A .gitmodules entry alone is not proof of a submodule. Require an
	// actual gitlink in the index before any destructive command.
	entries, err := r.run(quick, "--literal-pathspecs", "ls-files", "--stage", "-z", "--", path)
	if err != nil {
		return err
	}
	linked := false
	for _, entry := range strings.Split(string(entries), "\x00") {
		meta, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if ok && name == path && len(fields) == 3 && fields[0] == "160000" && fields[2] == "0" {
			linked = true
		}
	}
	if !linked {
		return fmt.Errorf("no checked-in submodule at %s", path)
	}
	// Git searches parent folders for .git. An uninitialized submodule
	// must not make reset/clean act on the parent repository instead.
	prefix, err := r.run(quick, "-C", path, "rev-parse", "--show-prefix")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(prefix)) != "" {
		return fmt.Errorf("submodule is not checked out: %s", path)
	}
	if _, err := r.run(slow, "-C", path, "reset", "--hard"); err != nil {
		return err
	}
	if _, err := r.run(slow, "-C", path, "clean", "-fd"); err != nil {
		return err
	}
	return r.SubmoduleUpdatePath(path)
}
