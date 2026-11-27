package git

import (
	"errors"
	"fmt"
	"strings"
)

// DefaultMaxConflictBytes caps how much of a conflicted file is read for
// display: enough for any hand-written source file, small enough to stay
// fast over SSH.
const DefaultMaxConflictBytes = 2 << 20

// ErrConflictMarkersRemain refuses to stage or write a file that still
// has conflict markers.
var ErrConflictMarkersRemain = errors.New("the file still has conflict markers")

// ErrConflictStateChanged refuses to resolve a conflict whose repository
// state changed since it was read: another process or session touched it.
var ErrConflictStateChanged = errors.New("the conflict state changed since it was read")

// ErrConflictNotUnmerged refuses to touch a path that is no longer
// conflicted, so a resolved file is never overwritten.
var ErrConflictNotUnmerged = errors.New("the path is no longer unmerged")

// ConflictIdentity pins one conflicted path to the exact repository state
// it was read in. The safe write primitives compare it with a live read
// before changing anything.
type ConflictIdentity struct {
	Path      string
	Code      string
	State     string
	Head      string
	Operation string
	BaseOID   string
	OursOID   string
	TheirsOID string
}

// ConflictIdentity returns the identity of the unmerged path, if any.
func (s Status) ConflictIdentity(path string) (ConflictIdentity, bool) {
	stage, ok := s.ConflictStage(path)
	if !ok {
		return ConflictIdentity{}, false
	}
	return ConflictIdentity{
		Path:      stage.Path,
		Code:      stage.Code,
		State:     s.State,
		Head:      s.Head,
		Operation: s.Operation,
		BaseOID:   stage.BaseOID,
		OursOID:   stage.OursOID,
		TheirsOID: stage.TheirsOID,
	}, true
}

// ConflictInspection is everything the conflict view needs for one path:
// which stages exist, what they contain, and the conflict blocks of the
// working tree file. Large or binary stages are flagged, not read out.
type ConflictInspection struct {
	Identity       ConflictIdentity
	HaveBase       bool
	HaveOurs       bool
	HaveTheirs     bool
	BaseMode       string
	OursMode       string
	TheirsMode     string
	WorktreeExists bool
	WorktreeMode   string
	IsSubmodule    bool
	Binary         bool
	TooLarge       bool
	WorktreeText   string
	OursText       string
	TheirsText     string
	BaseText       string
	Hunks          []ConflictHunk
	HasMarkers     bool
}

// InspectConflict reads one unmerged path: its stages, their contents,
// and the working tree file, for the conflict view. Contents bigger than
// maxBytes are flagged TooLarge and left out; binary contents are flagged
// and never decoded as text. maxBytes of 0 or less uses
// DefaultMaxConflictBytes.
func (r *Repo) InspectConflict(path string, maxBytes int) (ConflictInspection, error) {
	var insp ConflictInspection
	if strings.TrimSpace(path) == "" {
		return insp, fmt.Errorf("missing conflict path")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxConflictBytes
	}
	st, err := r.Status()
	if err != nil {
		return insp, err
	}
	identity, ok := st.ConflictIdentity(path)
	if !ok {
		return insp, fmt.Errorf("%s is not unmerged: %w", path, ErrConflictNotUnmerged)
	}
	stage, _ := st.ConflictStage(path)
	live, err := r.unmergedIndexStages(path)
	if err != nil {
		return insp, err
	}
	if err := verifyStageOIDs(stage, live); err != nil {
		return insp, fmt.Errorf("%s: %w", path, err)
	}

	insp.Identity = identity
	insp.HaveBase, insp.HaveOurs, insp.HaveTheirs = stage.HaveBase(), stage.HaveOurs(), stage.HaveTheirs()
	insp.BaseMode, insp.OursMode, insp.TheirsMode = stage.BaseMode, stage.OursMode, stage.TheirsMode
	insp.WorktreeExists, insp.WorktreeMode = stage.WorktreeExists(), stage.WorktreeMode
	insp.IsSubmodule = stage.IsSubmodule()
	if insp.IsSubmodule {
		// Gitlink objects live in the submodule's own object store, so
		// there is nothing to read here.
		return insp, nil
	}

	readSide := func(oid string) string {
		if !oidPresent(oid) || insp.TooLarge {
			return ""
		}
		size, err := r.blobSize(oid)
		if err != nil || size > maxBytes {
			if err == nil {
				insp.TooLarge = true
			}
			return ""
		}
		data, err := r.run(quick, "cat-file", "blob", oid)
		if err != nil {
			return ""
		}
		if IsBinaryContent(data) {
			insp.Binary = true
			return ""
		}
		return string(data)
	}
	insp.BaseText = readSide(stage.BaseOID)
	insp.OursText = readSide(stage.OursOID)
	insp.TheirsText = readSide(stage.TheirsOID)

	if insp.WorktreeExists && !insp.TooLarge {
		oid, err := r.worktreeBlobOID(path)
		if err == nil {
			size, sizeErr := r.blobSize(oid)
			switch {
			case sizeErr != nil:
			case size > maxBytes:
				insp.TooLarge = true
			default:
				data, err := r.run(quick, "cat-file", "blob", oid)
				if err == nil {
					if IsBinaryContent(data) {
						insp.Binary = true
					} else {
						insp.WorktreeText = string(data)
						insp.Hunks = ParseConflictHunks(insp.WorktreeText)
						insp.HasMarkers = HasConflictMarkers(insp.WorktreeText)
					}
				}
			}
		}
	}
	return insp, nil
}

// blobSize is the byte size of an object, or an error when git cannot
// read it.
func (r *Repo) blobSize(oid string) (int, error) {
	out, err := r.run(quick, "cat-file", "-s", oid)
	if err != nil {
		return 0, err
	}
	var size int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &size); err != nil {
		return 0, fmt.Errorf("git cat-file -s: unexpected size %q", strings.TrimSpace(string(out)))
	}
	return size, nil
}

// worktreeBlobOID writes the working tree file of path into the object
// store and returns its id, reading the file the same way on this
// machine and over SSH.
func (r *Repo) worktreeBlobOID(path string) (string, error) {
	out, err := r.run(quick, "hash-object", "-w", "--", path)
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(string(out))
	if !oidPresent(oid) {
		return "", fmt.Errorf("git hash-object returned no object id for %s", path)
	}
	return oid, nil
}

// unmergedIndexStages are the stage entries of one unmerged path, by
// stage number.
func (r *Repo) unmergedIndexStages(path string) (map[string][2]string, error) {
	out, err := r.run(quick, "--literal-pathspecs", "ls-files", "--unmerged", "-z", "--", path)
	if err != nil {
		return nil, err
	}
	stages := map[string][2]string{}
	for _, entry := range strings.Split(string(out), "\x00") {
		if entry == "" {
			continue
		}
		meta, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || !isHex(fields[1]) {
			return nil, fmt.Errorf("invalid conflict index entry %q", entry)
		}
		if name != path {
			continue
		}
		stages[fields[2]] = [2]string{fields[0], fields[1]}
	}
	return stages, nil
}

// verifyStageOIDs cross-checks the stage objects the status recorded
// against the index itself: a mismatch means the conflict changed while
// it was being read.
func verifyStageOIDs(stage ConflictStage, live map[string][2]string) error {
	want := map[string]string{}
	if stage.HaveBase() {
		want["1"] = stage.BaseOID
	}
	if stage.HaveOurs() {
		want["2"] = stage.OursOID
	}
	if stage.HaveTheirs() {
		want["3"] = stage.TheirsOID
	}
	if len(want) != len(live) {
		return fmt.Errorf("conflict index stages do not match the status: %w", ErrConflictStateChanged)
	}
	for key, oid := range want {
		entry, ok := live[key]
		if !ok || entry[1] != oid {
			return fmt.Errorf("conflict index stages do not match the status: %w", ErrConflictStateChanged)
		}
	}
	return nil
}

// verifyConflictIdentity guards every write primitive: the path must
// still be unmerged in exactly the state the caller read, or nothing is
// touched and an error comes back.
func (r *Repo) verifyConflictIdentity(path string, expected ConflictIdentity) (ConflictStage, error) {
	if strings.TrimSpace(path) == "" || expected.Path != path {
		return ConflictStage{}, fmt.Errorf("conflict identity does not name %q", path)
	}
	st, err := r.Status()
	if err != nil {
		return ConflictStage{}, err
	}
	stage, ok := st.ConflictStage(path)
	if !ok {
		return ConflictStage{}, fmt.Errorf("%s: %w", path, ErrConflictNotUnmerged)
	}
	live, ok := st.ConflictIdentity(path)
	if !ok || live != expected {
		return ConflictStage{}, fmt.Errorf("%s: %w", path, ErrConflictStateChanged)
	}
	stages, err := r.unmergedIndexStages(path)
	if err != nil {
		return ConflictStage{}, err
	}
	if err := verifyStageOIDs(stage, stages); err != nil {
		return ConflictStage{}, fmt.Errorf("%s: %w", path, err)
	}
	return stage, nil
}

// SafeTakeConflictSide resolves an unmerged path to one side of the
// merge: "ours" or "theirs". A side that does not exist resolves to a
// deletion. The conflict must still be exactly as expected describes it,
// or nothing is changed.
func (r *Repo) SafeTakeConflictSide(path, side string, expected ConflictIdentity) error {
	switch side {
	case "ours", "theirs":
	default:
		return fmt.Errorf("unknown conflict side %q", side)
	}
	stage, err := r.verifyConflictIdentity(path, expected)
	if err != nil {
		return err
	}
	if stage.IsSubmodule() {
		return fmt.Errorf("%s is a submodule conflict: resolve it inside the submodule", path)
	}
	have := stage.HaveOurs()
	if side == "theirs" {
		have = stage.HaveTheirs()
	}
	other := stage.HaveOurs() || stage.HaveTheirs()
	switch {
	case have:
		if _, err := r.run(quick, "--literal-pathspecs", "checkout", "--"+side, "--", path); err != nil {
			return err
		}
		_, err := r.run(quick, "--literal-pathspecs", "add", "-A", "--", path)
		return err
	case other:
		// The chosen side was deleted: the resolution is the deletion.
		_, err := r.run(quick, "--literal-pathspecs", "rm", "-f", "-q", "--", path)
		return err
	default:
		return fmt.Errorf("%s is deleted on both sides: use SafeDeleteConflictFile", path)
	}
}

// SafeDeleteConflictFile stages the deletion of an unmerged path, for
// conflicts where at least one side deleted the file. The conflict must
// still be exactly as expected describes it, or nothing is changed.
func (r *Repo) SafeDeleteConflictFile(path string, expected ConflictIdentity) error {
	stage, err := r.verifyConflictIdentity(path, expected)
	if err != nil {
		return err
	}
	if stage.HaveOurs() && stage.HaveTheirs() {
		return fmt.Errorf("%s exists on both sides: deleting it would lose a version", path)
	}
	_, err = r.run(quick, "--literal-pathspecs", "rm", "-f", "-q", "--", path)
	return err
}

// SafeMarkConflictResolved stages the working tree file of an unmerged
// path as its resolution. A file that still has conflict markers is
// never staged, and a path gone from the working tree is staged as a
// deletion. The conflict must still be exactly as expected describes it.
func (r *Repo) SafeMarkConflictResolved(path string, expected ConflictIdentity) error {
	stage, err := r.verifyConflictIdentity(path, expected)
	if err != nil {
		return err
	}
	if !stage.WorktreeExists() {
		_, err := r.run(quick, "--literal-pathspecs", "rm", "-f", "-q", "--", path)
		return err
	}
	oid, err := r.worktreeBlobOID(path)
	if err != nil {
		return err
	}
	body, err := r.run(quick, "cat-file", "blob", oid)
	if err != nil {
		return err
	}
	if !IsBinaryContent(body) && HasConflictMarkers(string(body)) {
		return fmt.Errorf("%s: %w", path, ErrConflictMarkersRemain)
	}
	_, err = r.run(quick, "--literal-pathspecs", "add", "-A", "--", path)
	return err
}

// SafeWriteResolvedConflict resolves an unmerged path with content: it
// writes the object, updates the index keeping the file's mode, and
// checks the file out, atomically enough that a failed write never
// touches the working tree. Content that still has conflict markers is
// refused before anything runs.
func (r *Repo) SafeWriteResolvedConflict(path, content string, expected ConflictIdentity) error {
	if HasConflictMarkers(content) {
		return fmt.Errorf("%s: %w", path, ErrConflictMarkersRemain)
	}
	stage, err := r.verifyConflictIdentity(path, expected)
	if err != nil {
		return err
	}
	if stage.IsSubmodule() {
		return fmt.Errorf("%s is a submodule conflict: resolve it inside the submodule", path)
	}
	for _, mode := range []string{stage.OursMode, stage.TheirsMode, stage.WorktreeMode} {
		if mode == "120000" {
			return fmt.Errorf("%s is a symbolic link conflict: edit it outside the conflict view", path)
		}
	}
	mode := "100644"
	for _, m := range []string{stage.OursMode, stage.TheirsMode, stage.WorktreeMode} {
		if m == "100755" {
			mode = "100755"
			break
		}
	}
	out, err := r.runInput(quick, []byte(content), "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	hash := strings.TrimSpace(string(out))
	if !oidPresent(hash) {
		return fmt.Errorf("git hash-object returned no object id")
	}
	// The three-argument cacheinfo form keeps paths with commas intact,
	// and the working tree stays untouched until the index has it.
	if _, err := r.run(quick, "update-index", "--add", "--cacheinfo", mode, hash, path); err != nil {
		return err
	}
	_, err = r.run(quick, "--literal-pathspecs", "checkout-index", "-f", "--", path)
	return err
}
