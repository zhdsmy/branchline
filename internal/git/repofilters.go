package git

import (
	"slices"
	"sort"
	"strings"
)

// passiveFilterConfig returns the -c arguments that keep a read-only
// command the app runs on its own — opening a repository, refreshing its
// state, re-reading after a write — from executing the filter programs the
// repository itself defines. A repository's .git/config is data whoever
// hands over the repository shapes: an unpacked archive or a copied
// checkout can carry filter.x.clean or filter.x.process commands, point
// files at the filter through .gitattributes, and the first status whose
// stat comparison misses the index runs them without the user asking for
// anything. Filters the repository does not define or rewrite — the user
// global and the system configuration's, such as git-lfs — still run, so
// their files keep comparing the way git would compare them.
//
// The empty command assignments override the repository's values for this
// one invocation, and required=false keeps a required filter from failing
// the read once its command is gone.
func (r *Repo) passiveFilterConfig() ([]string, error) {
	names, err := r.repoDefinedFilters()
	if err != nil {
		return nil, err
	}
	var args []string
	for _, name := range names {
		args = append(args,
			"-c", "filter."+name+".clean=",
			"-c", "filter."+name+".process=",
			"-c", "filter."+name+".required=false",
		)
	}
	return args, nil
}

// repoDefinedFilters lists the names of the filters whose clean or process
// command the repository itself defines or rewrites: the keys of the
// repository's own configuration whose values differ from both the global
// and the system value of the same key, or which those scopes do not set.
// A value equal to an inherited one is the user's own configuration running
// as it would anywhere and is left alone. The repository's files are the
// local and the worktree scopes — not the effective configuration, which
// would mistake the user's inherited keys and executor-level overrides for
// the repository's own — and every scope is read with include and includeIf
// directives resolved, so a filter hidden behind [include] is found. The
// worktree scope is the local file unless the repository opts into
// extensions.worktreeConfig, so the union never duplicates it.
func (r *Repo) repoDefinedFilters() ([]string, error) {
	local, err := r.filterConfigValues("--local")
	if err != nil {
		return nil, err
	}
	worktree, err := r.filterConfigValues("--worktree")
	if err != nil {
		return nil, err
	}
	repo := local
	if repo == nil && len(worktree) > 0 {
		repo = map[string]string{}
	}
	for key, value := range worktree {
		repo[key] = value
	}
	if len(repo) == 0 {
		return nil, nil
	}
	global, err := r.filterConfigValues("--global")
	if err != nil {
		return nil, err
	}
	system, err := r.filterConfigValues("--system")
	if err != nil {
		return nil, err
	}
	defined := map[string]bool{}
	for key, value := range repo {
		name, ok := splitFilterKey(key)
		if !ok {
			continue
		}
		if v, ok := global[key]; ok && v == value {
			continue
		}
		if v, ok := system[key]; ok && v == value {
			continue
		}
		defined[name] = true
	}
	names := make([]string, 0, len(defined))
	for name := range defined {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// splitFilterKey cuts filter.<name>.clean and filter.<name>.process into
// the filter's name. The name itself may hold dots; the cut is anchored on
// the filter. prefix and the final .clean or .process suffix.
func splitFilterKey(key string) (string, bool) {
	const prefix = "filter."
	if !strings.HasPrefix(key, prefix) {
		return "", false
	}
	for _, suffix := range []string{".clean", ".process"} {
		if strings.HasSuffix(key, suffix) {
			name := key[len(prefix) : len(key)-len(suffix)]
			if name != "" {
				return name, true
			}
		}
	}
	return "", false
}

// filterConfigValues reads the filter.x.clean and filter.x.process values
// of one scope — the effective configuration when no scope is given — as
// git itself resolves them, includes applied. git config exits 1 when
// nothing matches, which is an empty answer, not a failure. The worktree
// scope refuses to answer at all in a repository with several working
// trees that never opted into extensions.worktreeConfig — but that is the
// case where the scope is only another name for the local file, so the
// refusal is an empty answer too.
func (r *Repo) filterConfigValues(scope ...string) (map[string]string, error) {
	args := []string{"config", "--includes", "--null"}
	args = append(args, scope...)
	args = append(args, "--get-regexp", `^filter\..*\.(clean|process)$`)
	out, err := r.run(quick, args...)
	if ExitCode(err) == 1 {
		return nil, nil
	}
	if err != nil {
		if ExitCode(err) == 128 &&
			slices.Contains(scope, "--worktree") &&
			strings.Contains(err.Error(), "--worktree cannot be used with multiple working trees") {
			return nil, nil
		}
		return nil, err
	}
	return parseNullConfigValues(out), nil
}

// parseNullConfigValues reads git config --null output: records separated
// by NUL, each holding the key, a newline, and the value. Keys without a
// value carry no newline and do not belong to a command-valued filter.
func parseNullConfigValues(out []byte) map[string]string {
	values := map[string]string{}
	for _, record := range strings.Split(string(out), "\x00") {
		key, value, ok := strings.Cut(record, "\n")
		if !ok || key == "" {
			continue
		}
		values[key] = value
	}
	return values
}
