package git

import (
	"fmt"
	pathpkg "path"
	"sort"
	"strings"
)

// This file guards the operations that write the working tree against one
// data-loss shape: the place of the file they restore or write is a
// directory of files git does not track, or a parent directory's place is
// taken by such a file. `git checkout` and `git checkout-index` remove
// whatever stands in the way, untracked and ignored files included, without
// a word — measured on git checkout -- and checkout-index -f over a
// directory of untracked files and over an untracked file blocking a parent
// directory: exit 0, the content gone. The confirmation dialogs speak only
// of the file's tracked content, so nothing the user read covers those
// files. Every check runs through git itself, so it answers for a
// repository on an SSH server exactly as for a local one.

// checkWorktreeObstructions refuses the batch when writing any of the paths
// would destroy unprotected content: the path's own place held by a
// directory containing untracked or ignored files, or a parent directory's
// place held by an untracked or ignored file, which git deletes to make
// room. An empty directory holds nothing and does not obstruct. The check
// is a precondition of the whole batch: one obstructed path means nothing
// runs at all.
func (r *Repo) checkWorktreeObstructions(paths []string) error {
	return r.checkWorktreeObstructionsFor(paths, nil, nil)
}

func (r *Repo) checkWorktreeObstructionsFor(checkoutPaths, cleanPaths, ancestorOnlyPaths []string) error {
	if len(checkoutPaths) == 0 && len(cleanPaths) == 0 && len(ancestorOnlyPaths) == 0 {
		return nil
	}
	// The scans are split in two because the two questions need different
	// listings. The targets are scanned file by file: a directory at a
	// target's place must show the files inside it, and --directory never
	// descends there at all (measured: with the target's place a directory
	// of untracked files, the collapsed listing is empty). The parent
	// directories are scanned with --directory --no-empty-directory: an
	// untracked or ignored file standing at a parent's place still comes
	// back under its exact name, but an unrelated directory of thousands
	// of ignored files next to the target collapses to a single line
	// instead of flooding the output — and the SSH connection it travels
	// over — with content that cannot block anything.
	targetSpecs := []string{}
	ancSpecs := []string{}
	ancTargets := []string{}
	specs := map[string]bool{}
	addAnc := func(s string) {
		if s != "" && !specs[s] {
			specs[s] = true
			ancSpecs = append(ancSpecs, s)
		}
	}
	targets := []string{}
	seenTarget := map[string]bool{}
	seenAncTarget := map[string]bool{}
	addTarget := func(p string) {
		if p != "" && !seenTarget[p] {
			seenTarget[p] = true
			targets = append(targets, p)
			targetSpecs = append(targetSpecs, p)
		}
	}
	addAncTarget := func(p string) {
		if p == "" || seenAncTarget[p] {
			return
		}
		seenAncTarget[p] = true
		ancTargets = append(ancTargets, p)
		for anc := parentDir(p); anc != ""; anc = parentDir(anc) {
			addAnc(anc)
		}
	}
	for _, p := range checkoutPaths {
		addTarget(p)
		addAncTarget(p)
	}
	for _, p := range cleanPaths {
		if strings.HasSuffix(p, "/") {
			continue
		}
		addTarget(p)
	}
	for _, p := range ancestorOnlyPaths {
		addAncTarget(p)
	}
	under, err := r.worktreeOthers(targetSpecs, false)
	if err != nil {
		return err
	}
	blockers, err := r.worktreeOthers(ancSpecs, true)
	if err != nil {
		return err
	}
	loose := map[string]bool{}
	for _, o := range blockers {
		if !strings.HasSuffix(o, "/") {
			loose[o] = true
		}
	}
	var problems []string
	obstructed := map[string]bool{}
	if len(under) > 0 {
		sort.Strings(under)
		for _, p := range targets {
			if hasPathBelow(under, p+"/") {
				problems = append(problems, "the place of "+p+" in the working tree is a directory holding untracked or ignored files")
				obstructed[p] = true
			}
		}
	}
	for _, p := range ancTargets {
		if obstructed[p] {
			// Already reported at its own place.
			continue
		}
		for anc := parentDir(p); anc != ""; anc = parentDir(anc) {
			if loose[anc] {
				problems = append(problems, "the untracked or ignored file "+anc+" blocks a parent directory of "+p)
				break
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("refusing to write the working tree, nothing was changed: %s", strings.Join(problems, "; "))
	}
	return nil
}

// parentDir is the directory above a slash-separated repository path, or ""
// for a top-level path.
func parentDir(p string) string {
	d := pathpkg.Dir(p)
	if d == "." || d == "/" {
		return ""
	}
	return d
}

// hasPathBelow reports whether the sorted list holds a path inside dir,
// dir given with its trailing slash. Only the first candidate position
// needs checking: the list is sorted, so anything below dir sorts there.
func hasPathBelow(sorted []string, dir string) bool {
	i := sort.SearchStrings(sorted, dir)
	return i < len(sorted) && strings.HasPrefix(sorted[i], dir)
}

// worktreeOthers lists the untracked paths matched by the pathspecs, in as
// few commands as the command-line limit allows. ls-files --others without
// exclusion options lists every file the index does not hold, the files
// the ignore rules exclude included — exactly the content a working-tree
// write must never destroy. A pathspec matching a file names the file
// itself; one matching a directory names the files inside it. collapse
// adds --directory --no-empty-directory, folding a directory of nothing
// but untracked files into one line: right for scanning parent
// directories, where a file's exact name still surfaces but unrelated
// trees of ignored files stay out of the output.
func (r *Repo) worktreeOthers(pathspecs []string, collapse bool) ([]string, error) {
	var all []string
	var batch []string
	size := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		args := []string{"--literal-pathspecs", "ls-files", "--others"}
		if collapse {
			args = append(args, "--directory", "--no-empty-directory")
		}
		args = append(args, "-z", "--")
		args = append(args, batch...)
		out, err := r.run(quick, args...)
		if err != nil {
			return err
		}
		for _, entry := range strings.Split(string(out), "\x00") {
			if entry != "" {
				all = append(all, entry)
			}
		}
		batch = nil
		size = 0
		return nil
	}
	for _, p := range pathspecs {
		pl := len(ShellQuote(p)) + 1
		if len(batch) > 0 && size+pl > pathBatchLimit {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		batch = append(batch, p)
		size += pl
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return all, nil
}

// lsFilesStageEntries reads the index entries of the paths, keyed by path:
// the mode and the object id of each. A path the index does not hold is
// absent from the map. Mode 160000 is a gitlink: the path is a submodule,
// never a file a checkout could write, and its object id is the commit the
// submodule belongs at.
func (r *Repo) lsFilesStageEntries(paths []string) (map[string][2]string, error) {
	entries := map[string][2]string{}
	var batch []string
	size := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		args := append([]string{"--literal-pathspecs", "ls-files", "--stage", "-z", "--"}, batch...)
		out, err := r.run(quick, args...)
		if err != nil {
			return err
		}
		for _, entry := range strings.Split(string(out), "\x00") {
			if entry == "" {
				continue
			}
			meta, name, ok := strings.Cut(entry, "\t")
			fields := strings.Fields(meta)
			if ok && len(fields) == 3 {
				entries[name] = [2]string{fields[0], fields[1]}
			}
		}
		batch = nil
		size = 0
		return nil
	}
	for _, p := range paths {
		pl := len(ShellQuote(p)) + 1
		if len(batch) > 0 && size+pl > pathBatchLimit {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		batch = append(batch, p)
		size += pl
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return entries, nil
}

// restoreSubmodule checks the submodule at path out to the commit the
// parent's index records, never contacting the network: --no-fetch means a
// commit the submodule does not have is an error, not a fetch. Changes in
// the submodule are never deleted — git itself refuses a checkout that
// would overwrite them, and a submodule still dirty after the restore is
// reported for the user to handle inside the submodule. Restoring by
// submodule update instead of reset --hard also keeps an obstruction in
// the submodule's own tree in the user's hands rather than destroying it.
func (r *Repo) restoreSubmodule(path, oid string) error {
	if _, err := r.run(slow, "submodule", "update", "--no-fetch", "--checkout", "--", path); err != nil {
		return err
	}
	// submodule update exits 0 without touching anything when the submodule
	// was never initialized, so the outcome is verified instead of trusted:
	// in a real checkout the submodule is the repository root, where
	// --show-prefix prints an empty line before the HEAD hash. A directory
	// without the submodule's own repository resolves in the parent and
	// prints the path's prefix instead.
	out, err := r.run(quick, "-C", path, "rev-parse", "--show-prefix", "HEAD")
	if err != nil {
		return fmt.Errorf("cannot inspect the submodule %s after restoring it: %w", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 2 || lines[0] != "" {
		return fmt.Errorf("the submodule %s is not checked out: initialize it and try again", path)
	}
	if lines[1] != oid {
		return fmt.Errorf("the submodule %s did not return to the commit the index records: open the submodule and inspect it", path)
	}
	out, err = r.run(quick, "-C", path, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("cannot read the submodule %s after restoring it: %w", path, err)
	}
	if strings.TrimSpace(string(out)) != "" {
		return fmt.Errorf("the submodule %s still has uncommitted changes: open the submodule and commit or discard them there, nothing was deleted", path)
	}
	return nil
}
