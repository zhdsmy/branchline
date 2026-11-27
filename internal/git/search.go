package git

import (
	"errors"
	"strconv"
	"strings"
)

// SearchField is what a commit search looks at.
type SearchField string

const (
	SearchMessage SearchField = "message" // the commit message, git log --grep
	SearchAuthor  SearchField = "author"  // the author's name or email, --author
	SearchHash    SearchField = "hash"    // a prefix of the commit hash
	SearchContent SearchField = "content" // lines added or removed, -G
	SearchPath    SearchField = "path"    // the paths of the files changed
)

// SearchOptions choose the commits of Search.
type SearchOptions struct {
	Field SearchField
	Query string
	// Regex takes the query as a regular expression; otherwise it is
	// matched as written.
	Regex bool
	// MatchCase keeps the search case-sensitive.
	MatchCase bool
	// Revs are where the search starts, every branch, remote branch and
	// tag when empty.
	Revs []string
	// Limit is how many commits, 500 when 0.
	Limit int
	// Skip leaves out the first matches, for the next page.
	Skip int
	// Paths keep the commits that changed one of them, git log -- paths,
	// whatever the field.
	Paths []string
	// Occurrences finds content as git log -S does, the commits that
	// change how many times the text is there, instead of -G.
	Occurrences bool
}

// Search walks the history for the commits that match o. The query goes to
// git as one argument, so it is never seen by a shell, here or on a server.
func (r *Repo) Search(o SearchOptions) ([]Commit, error) {
	q := strings.TrimSpace(o.Query)
	if q == "" {
		return nil, errors.New("type something to search for")
	}
	limit := o.Limit
	if limit <= 0 {
		limit = 500
	}
	args := []string{"log", "--topo-order", "--decorate=short", "--format=" + logFormat}
	var paths []string
	switch o.Field {
	case SearchHash:
		return r.searchHash(strings.ToLower(q), o.Revs, o.Paths, o.Skip, limit)
	case SearchAuthor:
		args = append(args, "--author="+q)
	case SearchContent:
		switch {
		case o.Occurrences:
			args = append(args, "-S"+q)
			if o.Regex {
				args = append(args, "--pickaxe-regex")
			}
		case o.Regex:
			args = append(args, "-G"+q)
		default:
			args = append(args, "-G"+regexQuote(q))
		}
	case SearchPath:
		if o.MatchCase {
			paths = []string{":(glob)**/*" + escapeGlobPath(q) + "*"}
		} else {
			paths = []string{":(glob,icase)**/*" + escapeGlobPath(q) + "*"}
		}
	default:
		args = append(args, "--grep="+q)
	}
	if o.Field != SearchPath && o.Field != SearchContent {
		if !o.Regex {
			args = append(args, "--fixed-strings")
		} else {
			args = append(args, "--extended-regexp")
		}
	}
	if !o.MatchCase && o.Field != SearchPath {
		args = append(args, "--regexp-ignore-case")
	}
	args = append(args, "-n", strconv.Itoa(limit))
	if o.Skip > 0 {
		args = append(args, "--skip", strconv.Itoa(o.Skip))
	}
	args = append(args, searchRevs(o.Revs)...)
	args = append(args, "--")
	queryPaths := paths
	paths = cleanPaths(o.Paths)
	if o.Field == SearchPath {
		for i, path := range paths {
			paths[i] = ":(literal)" + path
		}
	}
	args = append(args, queryPaths...)
	args = append(args, paths...)
	out, err := r.run(slow, args...)
	if err != nil {
		if strings.Contains(err.Error(), "does not have any commits") {
			return nil, nil
		}
		return nil, err
	}
	return ParseLog(out), nil
}

func escapeGlobPath(text string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`, `]`, `\]`).Replace(text)
}

func searchRevs(revs []string) []string {
	if len(revs) > 0 {
		return revs
	}
	return []string{"--branches", "--remotes", "--tags", "HEAD"}
}

// cleanPaths are the paths a search is kept to, without empty ones; a
// path is a pathspec, after --, so one starting with - is still a path.
func cleanPaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// searchHash lists the commits whose hash starts with prefix, in all the
// history from revs. git has no flag for it, so only the hashes are read,
// which is quick for a long history, and the commits found are then read.
func (r *Repo) searchHash(prefix string, revs, paths []string, skip, limit int) ([]Commit, error) {
	if !isHex(prefix) {
		return nil, nil
	}
	args := append([]string{"log", "--topo-order", "--format=%H"}, searchRevs(revs)...)
	args = append(append(args, "--"), cleanPaths(paths)...)
	out, err := r.run(slow, args...)
	if err != nil {
		if strings.Contains(err.Error(), "does not have any commits") {
			return nil, nil
		}
		return nil, err
	}
	var hits []string
	for _, h := range strings.Fields(string(out)) {
		if !strings.HasPrefix(h, prefix) {
			continue
		}
		if skip > 0 {
			skip--
			continue
		}
		hits = append(hits, h)
		if len(hits) >= limit {
			break
		}
	}
	if len(hits) == 0 {
		return nil, nil
	}
	out, err = r.run(quick, append([]string{"log", "--no-walk=unsorted", "--decorate=short", "--format=" + logFormat}, hits...)...)
	if err != nil {
		return nil, err
	}
	return ParseLog(out), nil
}

// regexQuote escapes the characters a POSIX regular expression gives a
// meaning to, so -G matches the text as typed.
func regexQuote(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Authors lists the names and emails of the authors of the history, for
// suggestions while typing a search.
func (r *Repo) Authors() ([]string, error) {
	out, err := r.run(quick, "shortlog", "-sne", "--all", "--no-merges")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if _, who, ok := strings.Cut(line, "\t"); ok && strings.TrimSpace(who) != "" {
			names = append(names, strings.TrimSpace(who))
		}
	}
	return names, nil
}

// TrackedPaths lists the files at HEAD, for suggestions.
func (r *Repo) TrackedPaths() ([]string, error) {
	out, err := r.run(quick, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// CommitFileStatsAt is CommitFileStats from ref, HEAD when empty: the
// history the statistics and treemap of that reference are drawn from.
func (r *Repo) CommitFileStatsAt(ref string) ([]Commit, [][]FileStat, error) {
	if strings.TrimSpace(ref) == "" {
		return r.CommitFileStats()
	}
	if strings.HasPrefix(ref, "-") {
		return nil, nil, errors.New("a reference cannot start with -")
	}
	out, err := r.run(slow, "log", "--date-order", "--no-renames",
		"--format=format:%x1e%H%x1f%an%x1f%ae%x1f%at%x1f%s", "--numstat", ref, "--")
	if err != nil {
		return nil, nil, err
	}
	commits, stats := ParseFileStats(out)
	return commits, stats, nil
}
