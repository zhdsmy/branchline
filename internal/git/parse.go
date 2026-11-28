package git

import (
	"bytes"
	"strconv"
	"strings"
	"time"
)

const (
	fieldSep  = "\x1f"
	recordSep = "\x1e"
)

// logFormat is the --format of the log ParseLog reads.
const logFormat = "%H%x1f%P%x1f%an%x1f%ae%x1f%at%x1f%D%x1f%s%x1e"

// ParseLog parses git log --format=logFormat.
func ParseLog(out []byte) []Commit {
	var commits []Commit
	for _, rec := range strings.Split(string(out), recordSep) {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, fieldSep, 7)
		if len(f) < 7 {
			continue
		}
		commits = append(commits, Commit{
			Hash:        f[0],
			Parents:     strings.Fields(f[1]),
			Author:      f[2],
			AuthorEmail: f[3],
			Date:        unixTime(f[4]),
			Refs:        ParseDecorations(f[5]),
			Subject:     f[6],
		})
	}
	return commits
}

// ParseDecorations parses %D: "HEAD -> main, origin/main, tag: v1".
func ParseDecorations(s string) []Ref {
	var refs []Ref
	for _, d := range strings.Split(s, ", ") {
		d = strings.TrimSpace(d)
		switch {
		case d == "":
		case strings.HasPrefix(d, "HEAD -> "):
			refs = append(refs, Ref{Name: strings.TrimPrefix(d, "HEAD -> "), Kind: RefBranch, Current: true})
		case d == "HEAD":
			refs = append(refs, Ref{Name: "HEAD", Kind: RefHead, Current: true})
		case strings.HasPrefix(d, "tag: "):
			refs = append(refs, Ref{Name: strings.TrimPrefix(d, "tag: "), Kind: RefTag})
		case strings.HasSuffix(d, "/HEAD"):
			// origin/HEAD says which branch the remote's default is.
		case strings.Contains(d, "/") && !strings.HasPrefix(d, "refs/"):
			// Without the full names, a local branch named a/b reads as a
			// remote branch here; refsKinds corrects it.
			refs = append(refs, Ref{Name: d, Kind: RefRemote})
		default:
			refs = append(refs, Ref{Name: d, Kind: RefBranch})
		}
	}
	return refs
}

// FixRefKinds gives the decorations of commits their kinds from refs: a
// local branch with a / in its name, or a remote without one.
func FixRefKinds(commits []Commit, refs Refs) {
	local := map[string]bool{}
	for _, b := range refs.Branches {
		local[b.Name] = true
	}
	for i := range commits {
		for j, r := range commits[i].Refs {
			if r.Kind == RefRemote && local[r.Name] {
				commits[i].Refs[j].Kind = RefBranch
			}
		}
	}
}

const detailFormat = "%H%x1f%P%x1f%an%x1f%ae%x1f%at%x1f%cn%x1f%ce%x1f%ct%x1f%D%x1f%B"

// ParseDetail parses git show -s --format=detailFormat.
func ParseDetail(out []byte) CommitDetail {
	f := strings.SplitN(string(out), fieldSep, 10)
	for len(f) < 10 {
		f = append(f, "")
	}
	return CommitDetail{
		Commit: Commit{
			Hash:        strings.TrimSpace(f[0]),
			Parents:     strings.Fields(f[1]),
			Author:      f[2],
			AuthorEmail: f[3],
			Date:        unixTime(f[4]),
			Refs:        ParseDecorations(f[8]),
			Subject:     firstLine(f[9]),
		},
		Committer:      f[5],
		CommitterEmail: f[6],
		CommitDate:     unixTime(f[7]),
		Message:        strings.TrimRight(f[9], "\n"),
	}
}

// Refs scans refs/heads and refs/remotes with refFormatBranches, then
// refs/tags with refFormatTags. Both print the six fields ParseRefs reads:
// refname, objectname, upstream short, upstream track, HEAD mark and peeled
// object. Only the tags scan peels: a field starting with * makes git read
// the object every reference names to see whether it is a tag object, and
// only a tag can be one. Branches and remote branches never are, so reading
// their objects costs time without changing a field — the trailing %1f of
// refFormatBranches keeps the peeled field empty, as it always was for them.
// Tags never have an upstream either, so refFormatTags leaves those two
// fields empty instead of asking for them.
const refFormatBranches = "%(refname)%1f%(objectname)%1f%(upstream:short)%1f%(upstream:track)%1f%(HEAD)%1f"
const refFormatTags = "%(refname)%1f%(objectname)%1f%1f%1f%(HEAD)%1f%(*objectname)"

// ParseRefs parses git for-each-ref --format output in the six fields of
// refFormatBranches and refFormatTags: the concatenated output of the branch
// scan and the tag scan, or of one scan asking for every reference.
func ParseRefs(out []byte) Refs {
	var r Refs
	remotes := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, fieldSep)
		if len(f) < 6 {
			continue
		}
		name, hash := f[0], f[1]
		switch {
		case strings.HasPrefix(name, "refs/heads/"):
			b := Branch{Name: strings.TrimPrefix(name, "refs/heads/"), FullName: name, Hash: hash, Upstream: f[2], Current: f[4] == "*"}
			b.Ahead, b.Behind, b.Gone = parseTrack(f[3])
			r.Branches = append(r.Branches, b)
		case strings.HasPrefix(name, "refs/remotes/"):
			short := strings.TrimPrefix(name, "refs/remotes/")
			remote, _, _ := strings.Cut(short, "/")
			remotes[remote] = true
			if strings.HasSuffix(short, "/HEAD") {
				continue
			}
			r.Remote = append(r.Remote, Branch{Name: short, FullName: name, Hash: hash, Remote: remote})
		case strings.HasPrefix(name, "refs/tags/"):
			if f[5] != "" {
				hash = f[5] // an annotated tag: the commit it tags
			}
			r.Tags = append(r.Tags, Tag{Name: strings.TrimPrefix(name, "refs/tags/"), Hash: hash})
		}
	}
	for _, b := range r.Remote {
		if remotes[b.Remote] {
			r.Remotes = append(r.Remotes, b.Remote)
			delete(remotes, b.Remote)
		}
	}
	return r
}

// parseTrack parses %(upstream:track): "[ahead 1, behind 2]" or "[gone]".
func parseTrack(s string) (ahead, behind int, gone bool) {
	s = strings.Trim(s, "[]")
	for _, part := range strings.Split(s, ", ") {
		k, v, _ := strings.Cut(part, " ")
		n, _ := strconv.Atoi(v)
		switch k {
		case "ahead":
			ahead = n
		case "behind":
			behind = n
		case "gone":
			gone = true
		}
	}
	return
}

const stashFormat = "%gd%x1f%H%x1f%at%x1f%gs"

// ParseStashes parses git stash list --format=stashFormat.
func ParseStashes(out []byte) []Stash {
	var list []Stash
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.SplitN(line, fieldSep, 4)
		if len(f) < 4 {
			continue
		}
		idx := len(list)
		if i := strings.Index(f[0], "{"); i >= 0 {
			if n, err := strconv.Atoi(strings.TrimSuffix(f[0][i+1:], "}")); err == nil {
				idx = n
			}
		}
		list = append(list, Stash{Index: idx, Name: f[0], Hash: f[1], Date: unixTime(f[2]), Message: f[3]})
	}
	return list
}

// ParseWorktrees parses git worktree list --porcelain. The first record is
// the checkout the command ran in.
func ParseWorktrees(out []byte) []Worktree {
	var list []Worktree
	var cur Worktree
	flush := func() {
		if cur.Path == "" {
			return
		}
		cur.Branch = strings.TrimPrefix(cur.Branch, "refs/heads/")
		list = append(list, cur)
		cur = Worktree{}
	}
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(line, "branch ")
		}
	}
	flush()
	if len(list) > 0 {
		list[0].Current = true
	}
	return list
}

// ParseStatus parses git status --porcelain=v2 --branch -z.
func ParseStatus(out []byte) Status {
	var st Status
	fields := bytes.Split(out, []byte{0})
	for i := 0; i < len(fields); i++ {
		line := string(fields[i])
		if line == "" {
			continue
		}
		switch line[0] {
		case '#':
			k, v, _ := strings.Cut(strings.TrimPrefix(line, "# "), " ")
			switch k {
			case "branch.oid":
				if v != "(initial)" {
					st.Head = v
				}
			case "branch.head":
				if v != "(detached)" {
					st.Branch = v
				}
			case "branch.upstream":
				st.Upstream = v
			case "branch.ab":
				a, b, _ := strings.Cut(v, " ")
				st.Ahead, _ = strconv.Atoi(strings.TrimPrefix(a, "+"))
				st.Behind, _ = strconv.Atoi(strings.TrimPrefix(b, "-"))
			}
		case '1', '2':
			n := 9
			if line[0] == '2' {
				n = 10
			}
			f := strings.SplitN(line, " ", n)
			if len(f) < n {
				continue
			}
			path, orig := f[n-1], ""
			if line[0] == '2' && i+1 < len(fields) {
				i++
				orig = string(fields[i])
			}
			x, y := f[1][0], f[1][1]
			if x != '.' {
				st.Staged = append(st.Staged, FileChange{Path: path, OrigPath: orig, Status: x, Staged: true})
			}
			if y != '.' {
				st.Unstaged = append(st.Unstaged, FileChange{Path: path, Status: y})
			}
		case 'u':
			f := strings.SplitN(line, " ", 11)
			if len(f) == 11 {
				code := f[1]
				st.Unstaged = append(st.Unstaged, FileChange{Path: f[10], Status: 'U', Conflict: code})
				st.Conflicts = append(st.Conflicts, ConflictStage{
					Path:         f[10],
					Code:         code,
					Submodule:    f[2],
					BaseMode:     f[3],
					OursMode:     f[4],
					TheirsMode:   f[5],
					WorktreeMode: f[6],
					BaseOID:      f[7],
					OursOID:      f[8],
					TheirsOID:    f[9],
				})
			}
		case '?':
			st.Unstaged = append(st.Unstaged, FileChange{Path: strings.TrimPrefix(line, "? "), Status: '?'})
		case '!':
			st.Ignored = append(st.Ignored, FileChange{Path: strings.TrimPrefix(line, "! "), Status: '!'})
		}
	}
	return st
}

// ParseLFSFiles reads git lfs ls-files --all -l: an object id, a
// presence mark, and the path. A leading * means the object is in the
// local cache; a leading - means only the pointer is present.
func ParseLFSFiles(out []byte) []LFSFile {
	var files []LFSFile
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// git lfs ls-files -l prints "<oid> * <path>", the mark being - for
		// a pointer without its object; the mark first is read as well.
		first, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		var mark, id, path string
		if first == "*" || first == "-" {
			mark = first
			id, path, ok = strings.Cut(strings.TrimSpace(rest), " ")
		} else {
			id = first
			mark, path, ok = strings.Cut(strings.TrimSpace(rest), " ")
			if mark != "*" && mark != "-" {
				ok = false
			}
		}
		if !ok {
			continue
		}
		files = append(files, LFSFile{Path: path, ID: id, Present: mark == "*"})
	}
	return files
}

// applyLFSLocks copies lock owners from git lfs locks --json onto files.
// A lock the server reports for an unknown path is kept too, so the
// status window shows it.
func applyLFSLocks(files []LFSFile, raw []byte) []LFSFile {
	for _, lock := range parseLFSLockOwners(raw) {
		found := false
		for i := range files {
			if files[i].Path == lock.path {
				files[i].Owner = lock.owner
				found = true
			}
		}
		if !found {
			files = append(files, LFSFile{Path: lock.path, Owner: lock.owner})
		}
	}
	return files
}

type lfsLockOwner struct{ path, owner string }

func parseLFSLockOwners(raw []byte) []lfsLockOwner {
	var locks []lfsLockOwner
	for _, object := range jsonObjects(string(raw)) {
		path := jsonString(object, "path")
		if path == "" {
			continue
		}
		owner := jsonString(object, "ownername")
		if owner == "" {
			owner = jsonString(object, "name")
		}
		locks = append(locks, lfsLockOwner{path: path, owner: owner})
	}
	return locks
}

// jsonObjects returns the {...} values of a JSON array or one object,
// without decoding nested objects. Git LFS locks are flat.
func jsonObjects(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil
	}
	var objects []string
	depth, start := 0, -1
	for i, r := range raw {
		switch r {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				objects = append(objects, raw[start:i+1])
				start = -1
			}
		}
	}
	return objects
}

func jsonString(object, key string) string {
	needle := `"` + key + `"`
	i := strings.Index(object, needle)
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(object[i+len(needle):])
	if !strings.HasPrefix(rest, ":") {
		return ""
	}
	rest = strings.TrimSpace(rest[1:])
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	var b strings.Builder
	escaped := false
	for _, r := range rest[1:] {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			return b.String()
		default:
			b.WriteRune(r)
		}
	}
	return ""
}

// ParseBisectVars reads git rev-list --bisect-vars.
func ParseBisectVars(out []byte) BisectProgress {
	var p BisectProgress
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		n, _ := strconv.Atoi(strings.Trim(value, "'\""))
		switch key {
		case "bisect_nr":
			p.Left = n
		case "bisect_good":
			p.Good = n
		case "bisect_bad":
			p.Bad = n
		case "bisect_steps":
			p.Steps = n
		}
	}
	return p
}

// ParseNameStatus parses git diff --name-status -z: a status, then the
// path, or the old and the new path of a rename or copy.
func ParseNameStatus(out []byte) []FileChange {
	var list []FileChange
	f := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(f); i++ {
		st := f[i]
		if st == "" {
			continue
		}
		fc := FileChange{Status: st[0], Path: f[i+1]}
		i++
		if (st[0] == 'R' || st[0] == 'C') && i+1 < len(f) {
			fc.OrigPath, fc.Path = fc.Path, f[i+1]
			i++
		}
		list = append(list, fc)
	}
	return list
}

// ParseFileStats parses git log --format=format:%x1e%H%x1f%an%x1f%ae%x1f%at%x1f%s --numstat.
// A renamed file is counted at its new path. Binary files count as one change
// with no lines.
func ParseFileStats(out []byte) ([]Commit, [][]FileStat) {
	var commits []Commit
	var stats [][]FileStat
	for _, rec := range strings.Split(string(out), recordSep) {
		rec = strings.Trim(rec, "\n")
		if rec == "" {
			continue
		}
		lines := strings.Split(rec, "\n")
		f := strings.SplitN(lines[0], fieldSep, 5)
		if len(f) < 5 || f[0] == "" {
			continue
		}
		commits = append(commits, Commit{
			Hash: f[0], Author: f[1], AuthorEmail: f[2], Date: unixTime(f[3]), Subject: f[4],
		})
		var files []FileStat
		for _, line := range lines[1:] {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) != 3 || parts[2] == "" {
				continue
			}
			added, _ := strconv.Atoi(parts[0])
			removed, _ := strconv.Atoi(parts[1])
			files = append(files, FileStat{Path: renamePath(parts[2]), Added: added, Removed: removed})
		}
		stats = append(stats, files)
	}
	return commits, stats
}

// renamePath returns the path a file has after a rename numstat writes as
// "old => new", including a brace shared by a directory: "dir/{a => b}".
func renamePath(path string) string {
	const arrow = " => "
	i := strings.Index(path, arrow)
	if i < 0 {
		return path
	}
	left, right := path[:i], path[i+len(arrow):]
	if j := strings.LastIndex(left, "{"); j >= 0 && strings.HasSuffix(right, "}") {
		return left[:j] + right[:len(right)-1]
	}
	if j := strings.Index(right, " => "); j >= 0 {
		return renamePath(right)
	}
	return right
}

func unixTime(s string) time.Time {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// ParseIndexFlags reads git ls-files -v -z. With core.quotepath=false
// each record is a tag, a space, and the path. A lowercase tag means
// assume-unchanged. S means skip-worktree, alone or with assume-unchanged.
// Ordinary tracked files are left out.
func ParseIndexFlags(out []byte) []IndexFlags {
	var list []IndexFlags
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) < 3 || rec[1] != ' ' {
			continue
		}
		tag, path := rec[0], string(rec[2:])
		if path == "" {
			continue
		}
		var flags IndexFlags
		switch tag {
		case 'H', 'S', 'M', 'R', 'C', 'K', '?', 'h', 's', 'm', 'r', 'c', 'k':
			flags.AssumeUnchanged = tag >= 'a' && tag <= 'z'
			flags.SkipWorktree = tag == 'S' || tag == 's'
		default:
			continue
		}
		if !flags.AssumeUnchanged && !flags.SkipWorktree {
			continue
		}
		flags.Path = path
		list = append(list, flags)
	}
	return list
}

func firstLine(s string) string {
	s = strings.TrimLeft(s, "\n")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
