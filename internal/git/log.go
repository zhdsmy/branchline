package git

import (
	"strconv"
	"strings"
)

// LogOptions choose the commits of Log.
type LogOptions struct {
	// Limit is how many commits, 500 when 0.
	Limit int
	// Skip leaves out the first commits, for the next page.
	Skip int
	// Revs are the revisions to start from, every branch, remote branch,
	// tag and HEAD when empty.
	Revs []string
	// Query matches the message, author or hash, as git log --grep and
	// --author do, or a hash prefix.
	Query string
	// All includes commits no branch or tag points at.
	All bool
	// FirstParent follows only the first parent of a merge.
	FirstParent bool
	// NoMerges leaves merge commits out.
	NoMerges bool
	// Reflog walks the HEAD reflog instead of the commit graph.
	Reflog bool
	// DateOrder sorts by committer date instead of topology.
	DateOrder bool
}

// Log returns the history, children before parents.
func (r *Repo) Log(o LogOptions) ([]Commit, error) {
	q := strings.TrimSpace(o.Query)
	o.Query = ""
	if q == "" {
		return r.log(o)
	}
	// git ANDs --author with --grep. A search matches either, so the two
	// walks are joined, keeping the order of the message matches first.
	byMessage, err := r.log(withQuery(o, "--grep="+q))
	if err != nil {
		return nil, err
	}
	byAuthor, err := r.log(withQuery(o, "--author="+q))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Commit
	add := func(cs []Commit) {
		for _, c := range cs {
			if seen[c.Hash] || len(out) >= o.limit() {
				continue
			}
			seen[c.Hash] = true
			out = append(out, c)
		}
	}
	add(byMessage)
	add(byAuthor)
	if isHex(q) && len(out) < o.limit() {
		// The whole history is looked through, past the page shown.
		revs := o.Revs
		switch {
		case len(revs) > 0:
		case o.All || o.Reflog:
			revs = []string{"--all"}
		}
		hashes, err := r.searchHash(strings.ToLower(q), revs, nil, 0, o.limit())
		if err == nil {
			add(hashes)
		}
	}
	return out, nil
}

func (o LogOptions) limit() int {
	if o.Limit <= 0 {
		return 500
	}
	return o.Limit
}

func withQuery(o LogOptions, flag string) LogOptions {
	o.Query = flag
	return o
}

func (r *Repo) log(o LogOptions) ([]Commit, error) {
	if o.Limit <= 0 {
		o.Limit = 500
	}
	order := "--topo-order"
	if o.DateOrder {
		order = "--date-order"
	}
	args := []string{"log", order, "--decorate=short", "--format=" + logFormat, "-n", strconv.Itoa(o.Limit)}
	if o.Skip > 0 {
		args = append(args, "--skip", strconv.Itoa(o.Skip))
	}
	if o.FirstParent {
		args = append(args, "--first-parent")
	}
	if o.NoMerges {
		args = append(args, "--no-merges")
	}
	if strings.HasPrefix(o.Query, "--") {
		args = append(args, "--regexp-ignore-case", "--fixed-strings", o.Query)
	}
	defaultRevs := false
	switch {
	case o.Reflog:
		args = append(args, "-g", "--all")
	case len(o.Revs) > 0:
		// withEndOfOptions puts the revisions behind --end-of-options when
		// one starts with "-": the caller picks or types them, and such a
		// revision must not become an option of git log. The pseudo-options
		// of the other cases (--all, --branches...) are options and stay
		// ahead of the separator.
		args = withEndOfOptions(args, o.Revs...)
	case o.All:
		args = append(args, "--all")
	default:
		defaultRevs = true
		args = append(args, "--branches", "--remotes", "--tags", "HEAD")
	}
	args = append(args, "--")
	out, err := r.run(quick, args...)
	if err != nil {
		// With "--" present git resolves HEAD strictly, so an unborn HEAD
		// (an empty repository or a not yet committed --orphan branch) fails
		// the walk with "fatal: bad revision 'HEAD'" and exit code 128. When
		// a structured probe confirms HEAD is genuinely unborn, the walk is
		// retried without HEAD: the explicit --branches --remotes --tags keep
		// git from falling back to HEAD, so an empty repository returns no
		// commits while the branches, remote branches and tags of an orphan
		// branch's repository still show. A corrupt ref fails the retry and
		// is returned unchanged, and every other failure is returned as is.
		if defaultRevs && ExitCode(err) == 128 && r.isUnbornHEAD() {
			retryArgs := make([]string, 0, len(args)-1)
			for _, a := range args {
				if a != "HEAD" {
					retryArgs = append(retryArgs, a)
				}
			}
			out, err = r.run(quick, retryArgs...)
		}
		// An explicit walk of HEAD alone — the history filtered to the
		// current branch — has no fallback revision to retry with when HEAD
		// is unborn (an empty repository or a not yet committed --orphan
		// branch). The structured probe confirming the unborn HEAD means
		// the walk's answer is no commits, not a failure, so the read
		// succeeds empty. Only exactly ["HEAD"] earns this: any other
		// caller-chosen revision failing is the caller's error to see, as
		// is a corrupt HEAD the probe cannot confirm.
		if err != nil && len(o.Revs) == 1 && o.Revs[0] == "HEAD" && ExitCode(err) == 128 && r.isUnbornHEAD() {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	return ParseLog(out), nil
}

// FileLog returns the commits that changed path.
func (r *Repo) FileLog(path string, limit int) ([]Commit, error) {
	if limit <= 0 {
		limit = 100
	}
	out, err := r.run(slow, "log", "--topo-order", "--decorate=short", "--format="+logFormat, "-n", strconv.Itoa(limit), "HEAD", "--", path)
	if err != nil {
		return nil, err
	}
	return ParseLog(out), nil
}

// Blame annotates path at rev, HEAD when rev is empty.
func (r *Repo) Blame(path, rev string) (string, error) {
	if rev == "" {
		rev = "HEAD"
	}
	out, err := r.run(slow, "blame", "-w", "--date=short", rev, "--", path)
	return string(out), err
}
