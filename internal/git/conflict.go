package git

import (
	"fmt"
	"strings"
)

// MergeFile opens an unmerged file in an external merge tool.
func (r *Repo) MergeFile(tool, path string) error {
	if tool == "" {
		return fmt.Errorf("no external merge tool is configured")
	}
	_, err := r.run(slow, "mergetool", "--no-prompt", "--tool="+tool, "--", path)
	return err
}

// ConflictSides are the two versions git recorded for an unmerged path.
type ConflictSides struct {
	Ours, Theirs         string
	HaveOurs, HaveTheirs bool
}

// ConflictSides reads the ours and theirs stages of an unmerged path.
func (r *Repo) ConflictSides(path string) (ConflictSides, error) {
	var s ConflictSides
	// A failed object read must not masquerade as a deleted side.
	entries, err := r.run(quick, "--literal-pathspecs", "ls-files", "--unmerged", "-z", "--", path)
	if err != nil {
		return s, err
	}
	for _, entry := range strings.Split(string(entries), "\x00") {
		if entry == "" {
			continue
		}
		meta, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || !isHex(fields[1]) {
			return s, fmt.Errorf("invalid conflict index entry %q", entry)
		}
		if name != path || (fields[2] != "2" && fields[2] != "3") {
			continue
		}
		body, err := r.run(quick, "cat-file", "blob", fields[1])
		if err != nil {
			return s, err
		}
		if fields[2] == "2" {
			s.Ours, s.HaveOurs = string(body), true
		} else {
			s.Theirs, s.HaveTheirs = string(body), true
		}
	}
	if !s.HaveOurs && !s.HaveTheirs {
		return s, fmt.Errorf("%s has no conflict stages", path)
	}
	return s, nil
}

// DeleteConflictFile stages an explicit deletion of an unmerged path.
func (r *Repo) DeleteConflictFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("missing conflict path")
	}
	_, err := r.run(quick, "--literal-pathspecs", "rm", "-f", "-q", "--", path)
	return err
}

// TakeConflictSide stages one side of an unmerged path: "ours" or "theirs".
func (r *Repo) TakeConflictSide(path, side string) error {
	switch side {
	case "ours", "theirs":
	default:
		return fmt.Errorf("unknown conflict side %q", side)
	}
	_, err := r.run(quick, "checkout", "--"+side, "--", path)
	if err != nil {
		sides, sidesErr := r.ConflictSides(path)
		if sidesErr == nil {
			if (side == "ours" && !sides.HaveOurs && sides.HaveTheirs) ||
				(side == "theirs" && !sides.HaveTheirs && sides.HaveOurs) {
				return r.DeleteConflictFile(path)
			}
		}
		return err
	}
	return r.Stage(path)
}
