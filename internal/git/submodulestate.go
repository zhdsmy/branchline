package git

import (
	"strconv"
	"strings"
)

// SubmoduleState is where a checked out submodule stands against the
// commit the repository records for it: the commits it is behind and ahead
// by, and its uncommitted changes.
type SubmoduleState struct {
	Path string
	// Recorded is the commit the index of the repository holds for it.
	Recorded string
	// Head is the commit the submodule has checked out; empty when the
	// submodule is not checked out.
	Head string
	// Behind counts the commits of Recorded that Head lacks, Ahead those of
	// Head that Recorded lacks.
	Behind, Ahead int
	// Uncommitted counts the files of the submodule with changes of their
	// own, tracked or not.
	Uncommitted int
}

// CheckedOut reports whether the submodule has a working tree here.
func (s SubmoduleState) CheckedOut() bool { return s.Head != "" }

// SubmoduleStateOf reads the state of the submodule at path. Everything
// runs through git of the repository (git -C path), so it is the same for
// a repository on this machine and one over SSH. A submodule that is not
// checked out gives a state without a Head, and no error.
func (r *Repo) SubmoduleStateOf(path string) (SubmoduleState, error) {
	s := SubmoduleState{Path: path}
	out, err := r.run(quick, "ls-files", "-s", "--", path)
	if err != nil {
		return s, err
	}
	// A line is "160000 <hash> 0<TAB><path>".
	for _, line := range strings.Split(string(out), "\n") {
		meta, _, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) >= 2 && f[0] == "160000" {
			s.Recorded = f[1]
			break
		}
	}
	// An empty directory of a submodule not checked out belongs to the
	// repository above, whose top level is then not the submodule's.
	top, err := r.run(quick, "-C", path, "rev-parse", "--show-toplevel")
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(top)), "/"+strings.Trim(path, "/")) {
		return s, nil
	}
	head, err := r.run(quick, "-C", path, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		return s, nil
	}
	s.Head = strings.TrimSpace(string(head))
	if s.Recorded != "" && s.Recorded != s.Head {
		if out, err := r.run(quick, "-C", path, "rev-list", "--left-right", "--count", s.Recorded+"...HEAD"); err == nil {
			f := strings.Fields(string(out))
			if len(f) == 2 {
				s.Behind, _ = strconv.Atoi(f[0])
				s.Ahead, _ = strconv.Atoi(f[1])
			}
		}
	}
	out, err = r.run(quick, "-C", path, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return s, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) != "" {
			s.Uncommitted++
		}
	}
	return s, nil
}
