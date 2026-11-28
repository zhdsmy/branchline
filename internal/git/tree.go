package git

import (
	"fmt"
	"strconv"
	"strings"
)

// TreeEntry names a blob or submodule at a particular commit.
type TreeEntry struct {
	Path, Hash, Mode string
	Size             int64
}

// Tree reads NUL-delimited paths, including names containing tabs or newlines.
func (r *Repo) Tree(hash string) ([]TreeEntry, error) {
	out, err := r.run(quick, append(withEndOfOptions([]string{"ls-tree", "-r", "-l", "-z"}, hash), "--")...)
	if err != nil {
		return nil, err
	}
	var entries []TreeEntry
	for _, record := range strings.Split(string(out), "\x00") {
		if record == "" {
			continue
		}
		header, path, ok := strings.Cut(record, "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 4 {
			return nil, fmt.Errorf("invalid tree entry")
		}
		var size int64
		if fields[3] != "-" {
			size, err = strconv.ParseInt(fields[3], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid tree size: %w", err)
			}
		}
		entries = append(entries, TreeEntry{Path: path, Hash: fields[2], Mode: fields[0], Size: size})
	}
	return entries, nil
}

func (r *Repo) Blob(hash string) ([]byte, error) { return r.run(quick, "cat-file", "blob", hash) }

// Submodules lists configured paths without contacting or updating submodules.
func (r *Repo) Submodules() ([]string, error) {
	out, err := r.run(quick, "config", "--file", ".gitmodules", "--null", "--get-regexp", `^submodule\..*\.path$`)
	if ExitCode(err) == 1 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, record := range strings.Split(string(out), "\x00") {
		if _, path, ok := strings.Cut(record, "\n"); ok {
			paths = append(paths, path)
		}
	}
	return paths, nil
}
