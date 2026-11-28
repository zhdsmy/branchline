package git

import (
	"fmt"
	"strconv"
	"strings"
)

// BlobSide names one side of a file compared: a commit, "index" for the
// staged file, "worktree" for the file on disk, or "" for a side the file
// is not on.
type BlobSide struct {
	Rev  string
	Path string
}

// BlobInfo is the size of one side of a binary file and its first bytes.
type BlobInfo struct {
	Exists bool
	Size   int64
	Hash   string
	// Head is the start of the content, at most the limit asked for.
	Head []byte
}

// BinaryBlob reads the size and first bytes of one side of a file. git
// reads it, a working tree file too, so a repository on a server is read
// there, over the same connection.
func (r *Repo) BinaryBlob(side BlobSide, limit int) (BlobInfo, error) {
	if side.Rev == "" || side.Path == "" {
		return BlobInfo{}, nil
	}
	hash, err := r.blobHash(side)
	if err != nil {
		return BlobInfo{}, err
	}
	out, err := r.run(quick, "cat-file", "-s", hash)
	if err != nil {
		return BlobInfo{}, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return BlobInfo{}, fmt.Errorf("git cat-file -s returned %q", out)
	}
	info := BlobInfo{Exists: true, Size: size, Hash: hash}
	// A large blob is not read whole just to show its start.
	if limit > 0 && size <= 8<<20 {
		data, err := r.Blob(hash)
		if err == nil {
			if len(data) > limit {
				data = data[:limit]
			}
			info.Head = data
		}
	}
	return info, nil
}

// blobHash is the object of one side of a file, the working tree file
// written to the object store first.
func (r *Repo) blobHash(side BlobSide) (string, error) {
	var hash string
	switch side.Rev {
	case "worktree":
		out, err := r.run(quick, "hash-object", "-w", "--", side.Path)
		if err != nil {
			return "", err
		}
		hash = strings.TrimSpace(string(out))
	case "index":
		h, err := r.PathHash(":" + side.Path)
		if err != nil {
			return "", err
		}
		hash = h
	default:
		h, err := r.PathHash(side.Rev + ":" + side.Path)
		if err != nil {
			return "", err
		}
		hash = h
	}
	if !isHex(hash) {
		return "", fmt.Errorf("no blob for %s", side.Path)
	}
	return hash, nil
}

// BlobContent reads all of one side of a file, for Save As. A file of a
// repository on a server is read there and comes over the connection.
func (r *Repo) BlobContent(side BlobSide) ([]byte, error) {
	if side.Rev == "" || side.Path == "" {
		return nil, fmt.Errorf("the file is not on this side")
	}
	hash, err := r.blobHash(side)
	if err != nil {
		return nil, err
	}
	return r.run(slow, "cat-file", "blob", hash)
}

// HexDump formats data as hexdump -C does: offset, sixteen bytes in hex,
// and the printable ones.
func HexDump(data []byte) string {
	var b strings.Builder
	for off := 0; off < len(data); off += 16 {
		end := min(off+16, len(data))
		fmt.Fprintf(&b, "%08x  ", off)
		for i := off; i < off+16; i++ {
			if i < end {
				fmt.Fprintf(&b, "%02x ", data[i])
			} else {
				b.WriteString("   ")
			}
			if i == off+7 {
				b.WriteByte(' ')
			}
		}
		b.WriteString(" |")
		for _, c := range data[off:end] {
			if c < 0x20 || c > 0x7e {
				c = '.'
			}
			b.WriteByte(c)
		}
		b.WriteString("|\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// SubmoduleChange is a submodule's commit before and after, and the
// commits between them as git diff --submodule=log lists them.
type SubmoduleChange struct {
	Path     string
	Old, New string
	// Commits are the subjects of the commits added (Added true) and
	// removed between Old and New.
	Commits []SubmoduleCommit
	// Dirty is the submodule's working tree having changes of its own.
	Dirty bool
	// Note is what git says instead of a range, as "not checked out".
	Note string
}

// SubmoduleCommit is one commit between a submodule's two commits.
type SubmoduleCommit struct {
	Added   bool
	Subject string
}

// SubmoduleDiff describes how the submodule at path changed from one
// commit to another of the superproject. An empty to is the working tree,
// and an empty from with to the index compares the index with it.
func (r *Repo) SubmoduleDiff(from, to, path string, staged bool) (SubmoduleChange, error) {
	args := []string{"diff", "--no-ext-diff", "--no-color", "--submodule=log"}
	switch {
	case staged:
		args = append(args, "--cached")
	}
	var revs []string
	switch {
	case staged:
	case from != "" && to != "":
		revs = append(revs, from, to)
	case to != "":
		revs = append(revs, emptyTree, to)
	case from != "":
		revs = append(revs, from)
	}
	args = withEndOfOptions(args, revs...)
	out, err := r.run(quick, append(args, "--", path)...)
	if err != nil {
		return SubmoduleChange{Path: path}, err
	}
	return ParseSubmoduleLog(path, string(out)), nil
}

// ParseSubmoduleLog reads what git diff --submodule=log prints for one
// submodule.
func ParseSubmoduleLog(path, out string) SubmoduleChange {
	ch := SubmoduleChange{Path: path}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "Submodule "):
			rest := strings.TrimPrefix(line, "Submodule ")
			if strings.Contains(rest, "contains modified content") || strings.Contains(rest, "contains untracked content") {
				ch.Dirty = true
				continue
			}
			name, tail, ok := strings.Cut(rest, " ")
			if !ok || name != path {
				if ok && strings.HasPrefix(rest, path+" ") {
					tail = strings.TrimPrefix(rest, path+" ")
				} else {
					continue
				}
			}
			tail = strings.TrimSuffix(strings.TrimSpace(tail), ":")
			if i := strings.Index(tail, " ("); i >= 0 {
				ch.Note = strings.Trim(tail[i+1:], "()")
				tail = tail[:i]
			}
			if old, nw, ok := strings.Cut(tail, "..."); ok {
				ch.Old, ch.New = old, nw
			} else if old, nw, ok := strings.Cut(tail, ".."); ok {
				ch.Old, ch.New = old, nw
			} else if isHex(tail) {
				ch.New = tail
			}
		case strings.HasPrefix(line, "  > "):
			ch.Commits = append(ch.Commits, SubmoduleCommit{Added: true, Subject: strings.TrimPrefix(line, "  > ")})
		case strings.HasPrefix(line, "  < "):
			ch.Commits = append(ch.Commits, SubmoduleCommit{Subject: strings.TrimPrefix(line, "  < ")})
		}
	}
	return ch
}
