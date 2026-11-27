package git

import "strings"

// unpushedScript prints the path of a submodule whose HEAD has commits
// no remote-tracking branch of the submodule has. git submodule foreach
// runs it with $displaypath set, in each submodule checked out.
const unpushedScript = `if [ -n "$(git rev-list -n 1 HEAD --not --remotes 2>/dev/null)" ]; then printf '%s\0' "$displaypath"; fi`

// UnpushedSubmodules lists the submodules, nested ones too, whose
// checked out commit is not on any of their remotes: pushing the
// superproject then records a commit others cannot fetch. Callers can
// check this before pushing. A repository without submodules has none.
func (r *Repo) UnpushedSubmodules() ([]string, error) {
	out, err := r.run(quick, "submodule", "foreach", "--quiet", "--recursive", unpushedScript)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}
