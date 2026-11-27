package git

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Info is what the repository details say of a repository: how much space
// it takes, how many commits the current branch has, and its first and last
// commit.
type Info struct {
	// SizeKB is the size of the object database, loose and packed.
	SizeKB  int64
	Commits int
	// First is the root commit of the history of HEAD, Last its tip.
	First, Last InfoCommit
}

// InfoCommit is a commit as the details name it.
type InfoCommit struct {
	Hash    string
	Subject string
	Author  string
	Date    time.Time
}

// Short is the first characters of the hash.
func (c InfoCommit) Short() string { return short(c.Hash) }

const infoFormat = "--format=%H%x1f%an%x1f%aI%x1f%s"

// Info reads the details of the repository. A repository without a
// commit has zero commits and empty first and last.
func (r *Repo) Info() (Info, error) {
	var in Info
	out, err := r.run(quick, "count-objects", "-v")
	if err != nil {
		return in, err
	}
	in.SizeKB = ParseObjectSize(string(out))
	out, err = r.run(quick, "rev-list", "--count", "HEAD")
	if err != nil {
		// No commit yet.
		return in, nil
	}
	in.Commits, _ = strconv.Atoi(strings.TrimSpace(string(out)))
	if in.Commits == 0 {
		return in, nil
	}
	if out, err = r.run(quick, "log", "-1", infoFormat, "HEAD"); err == nil {
		in.Last = parseInfoCommit(string(out))
	}
	if out, err = r.run(quick, "rev-list", "--max-parents=0", "--reverse", "HEAD"); err == nil {
		if roots := strings.Fields(string(out)); len(roots) > 0 {
			if out, err = r.run(quick, "log", "-1", infoFormat, roots[0]); err == nil {
				in.First = parseInfoCommit(string(out))
			}
		}
	}
	return in, nil
}

// ParseObjectSize adds the size and size-pack lines of git count-objects
// -v, which are in kibibytes.
func ParseObjectSize(out string) int64 {
	var total int64
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "size", "size-pack":
			n, _ := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
			total += n
		}
	}
	return total
}

func parseInfoCommit(out string) InfoCommit {
	f := strings.SplitN(strings.TrimSpace(out), "\x1f", 4)
	if len(f) < 4 {
		return InfoCommit{}
	}
	when, _ := time.Parse(time.RFC3339, f[2])
	return InfoCommit{Hash: f[0], Author: f[1], Date: when, Subject: f[3]}
}

// SizeText writes a size in kibibytes as KB, MB or GB.
func SizeText(kb int64) string {
	switch {
	case kb < 1024:
		return fmt.Sprintf("%d KB", kb)
	case kb < 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(kb)/1024)
	}
	return fmt.Sprintf("%.2f GB", float64(kb)/1024/1024)
}

// readmeNames are the files the details show as the readme, in order.
var readmeNames = []string{"README.md", "README.markdown", "README", "README.txt", "readme.md", "Readme.md"}

// Readme returns the name and text of the repository's readme, or empty
// strings when it has none.
func (r *Repo) Readme() (name, text string, err error) {
	for _, n := range readmeNames {
		body, err := r.ReadFile(n)
		if err != nil {
			return "", "", err
		}
		if body != "" {
			return n, body, nil
		}
	}
	return "", "", nil
}
