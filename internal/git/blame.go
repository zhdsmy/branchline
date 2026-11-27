package git

import (
	"strconv"
	"strings"
	"time"
)

// BlameLine is one line of a file with the commit that last changed it,
// as the Blame view lists them.
type BlameLine struct {
	Number  int
	Hash    string
	Author  string
	Date    time.Time
	Subject string
	Text    string
	// First is set on the first line of a run of lines of one commit, where
	// the list shows the commit; the lines after it leave those cells empty.
	First bool
}

// BlameLines annotates path at rev, HEAD when rev is empty, line by line.
func (r *Repo) BlameLines(path, rev string) ([]BlameLine, error) {
	if rev == "" {
		rev = "HEAD"
	}
	out, err := r.run(quick, "blame", "--porcelain", "-w", rev, "--", path)
	if err != nil {
		return nil, err
	}
	return ParseBlame(string(out)), nil
}

// ParseBlame reads the output of git blame --porcelain. The details of a
// commit come once, with its first line, and later lines of it name only
// its hash.
func ParseBlame(out string) []BlameLine {
	type meta struct {
		author, subject string
		when            time.Time
	}
	seen := map[string]*meta{}
	var lines []BlameLine
	var cur *meta
	var line BlameLine
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "\t"):
			line.Text = l[1:]
			if cur != nil {
				line.Author, line.Date, line.Subject = cur.author, cur.when, cur.subject
			}
			line.First = len(lines) == 0 || lines[len(lines)-1].Hash != line.Hash
			lines = append(lines, line)
			line = BlameLine{}
		case cur != nil && strings.HasPrefix(l, "author "):
			cur.author = l[len("author "):]
		case cur != nil && strings.HasPrefix(l, "author-time "):
			if n, err := strconv.ParseInt(l[len("author-time "):], 10, 64); err == nil {
				cur.when = time.Unix(n, 0)
			}
		case cur != nil && strings.HasPrefix(l, "summary "):
			cur.subject = l[len("summary "):]
		default:
			// A header is "<hash> <orig line> <final line> [<group size>]".
			f := strings.Fields(l)
			if len(f) >= 3 && len(f[0]) >= 40 && isHex(f[0]) {
				n, err := strconv.Atoi(f[2])
				if err != nil {
					continue
				}
				line = BlameLine{Number: n, Hash: f[0]}
				if seen[f[0]] == nil {
					seen[f[0]] = &meta{}
				}
				cur = seen[f[0]]
			}
		}
	}
	return lines
}
