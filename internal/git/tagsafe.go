package git

import (
	"fmt"
	"strings"
)

// ValidateTagName says why name cannot be created as a tag, empty when it
// can. Beyond the ref-format rules of RefNameProblem it rejects names git
// would not treat as tag names on its command line: one starting with "-" is
// read as options (git tag -l lists instead of creating), one holding
// whitespace splits into several arguments, and one ending in "/" is a
// directory, never a ref.
func ValidateTagName(name string) string {
	if name == "" {
		return ""
	}
	if strings.HasPrefix(name, "-") {
		return "Name cannot begin with '-'"
	}
	if strings.ContainsAny(name, " \t\r\n") {
		return "Name cannot contain whitespace"
	}
	if strings.HasSuffix(name, "/") {
		return "Name cannot end with '/'"
	}
	return RefNameProblem(name)
}

// SafeCreateTag tags the commit rev resolves to, annotated when message is
// not all whitespace. A tag of that name is never overwritten: the ref is
// verified absent through show-ref's exact match before anything is created,
// and any failure of that check is an error instead of a license to create.
//
// rev must resolve to a commit, proven with rev-parse's ^{commit} peel before
// the tag is made: a missing revision, an unborn HEAD, or a non-commit object
// is an error. The exact object id the peel printed is what gets tagged, so
// the tag lands on the commit the verification saw. Both name and commit go
// behind "--" in git tag's argument list, though only names passing
// ValidateTagName and revs without whitespace or a leading "-" arrive here.
func (r *Repo) SafeCreateTag(name, rev, message string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("a tag needs a name")
	}
	if problem := ValidateTagName(name); problem != "" {
		return fmt.Errorf("invalid tag name %s: %s", name, problem)
	}
	rev = strings.TrimSpace(rev)
	if rev == "" {
		return fmt.Errorf("a tag needs a target commit")
	}
	if strings.HasPrefix(rev, "-") {
		return fmt.Errorf("target revision cannot begin with '-'")
	}
	if strings.ContainsAny(rev, " \t\r\n") {
		return fmt.Errorf("target revision cannot contain whitespace")
	}
	out, err := r.run(quick, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return fmt.Errorf("target %s does not resolve to a commit: %w", rev, err)
	}
	commitOID := strings.TrimSpace(string(out))
	if len(commitOID) < 40 || !isHex(commitOID) {
		return fmt.Errorf("verifying target %s printed no object id", rev)
	}
	if _, err := r.run(quick, "show-ref", "--verify", "--quiet", "refs/tags/"+name); err == nil {
		return fmt.Errorf("tag %s already exists", name)
	} else if ExitCode(err) != 1 {
		// Exit 1 is show-ref's "the ref does not exist". Anything else — a
		// corrupt repository, a dropped connection — means the answer is
		// unknown, and an unknown answer never creates over what may be there.
		return fmt.Errorf("cannot check whether tag %s exists: %w", name, err)
	}
	if strings.TrimSpace(message) == "" {
		_, err = r.run(quick, "tag", "--", name, commitOID)
		return err
	}
	_, err = r.runInput(quick, []byte(message), "tag", "-a", "-F", "-", "--", name, commitOID)
	return err
}

// TagRefInfo is the verified state of one tag: the exact object its ref
// points at, the commit it names, and what kind of tag it is.
type TagRefInfo struct {
	Name string
	// RefOID is the object refs/tags/<name> exactly points at: the commit for
	// a lightweight tag, the tag object for an annotated one. It changes when
	// the tag is deleted and recreated, even onto the same commit, so
	// comparing it proves the tag is still the one a check read.
	RefOID    string
	CommitOID string // the commit the tag names
	Annotated bool
	Tagger    string
	Date      string
	Message   string
}

// tagRefOID returns the exact object id refs/tags/<name> points at, read
// through show-ref's exact match. rev-parse is never used here: it resolves
// revision suffixes too, so refs/tags/v1~0 would answer as a revspec even
// though no tag of that name exists.
func (r *Repo) tagRefOID(name string) (string, error) {
	out, err := r.run(quick, "show-ref", "--verify", "refs/tags/"+name)
	if err != nil {
		return "", fmt.Errorf("verify tag %s: %w", name, err)
	}
	first, _, _ := strings.Cut(string(out), "\n")
	oid, ref, ok := strings.Cut(strings.TrimSpace(first), " ")
	if !ok || ref != "refs/tags/"+name {
		return "", fmt.Errorf("verifying tag %s printed no exact ref match", name)
	}
	if len(oid) < 40 || !isHex(oid) {
		return "", fmt.Errorf("verifying tag %s printed no object id", name)
	}
	return oid, nil
}

// TagRefInfo reads the verified state of the tag name. Every part of the
// read must succeed: a repository that cannot answer about the tag — or one
// where the ref moved between the two reads — is an error, never a partial
// answer a caller might act on.
func (r *Repo) TagRefInfo(name string) (TagRefInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return TagRefInfo{}, fmt.Errorf("a tag name is required")
	}
	refOID, err := r.tagRefOID(name)
	if err != nil {
		return TagRefInfo{}, err
	}
	detail, err := r.TagInfo(name)
	if err != nil {
		return TagRefInfo{}, err
	}
	info := TagRefInfo{
		Name:      name,
		RefOID:    refOID,
		Annotated: detail.Annotated,
		Tagger:    detail.Tagger,
		Message:   detail.Message,
	}
	if !detail.Date.IsZero() {
		info.Date = detail.Date.Format("Jan 2, 2006 15:04")
	}
	if detail.Annotated {
		// The annotated ref names the tag object; TagInfo's Target is the
		// commit it peels to.
		info.CommitOID = detail.Target
	} else {
		// The lightweight ref names its target directly.
		info.CommitOID = refOID
	}
	return info, nil
}

// SafeDeleteTag deletes the tag name only after proving on the same
// repository that the deletion is the one the caller asked for:
//
//   - checkErr must be nil and both name and expectedRefOID non-empty, so a
//     failed or never-run verification never deletes anything;
//   - refs/tags/<name> must still exist and point exactly at expectedRefOID,
//     re-read through show-ref at delete time. A tag retargeted or recreated
//     since the caller's check — even onto the same commit, which changes an
//     annotated tag's object id — is a different tag than the one the user
//     confirmed, and is never deleted.
//
// Any read failure refuses the deletion: an answer the repository cannot
// give is never treated as permission. The delete itself is git tag -d with
// the name behind "--", so a tag whose name starts with "-" — creatable
// through the ref store, as any ref is — is still just a tag name and not
// read as command-line options (git tag -d -v is otherwise a conflict of
// -d with --verbose, not the deletion of a tag named -v).
func (r *Repo) SafeDeleteTag(name, expectedRefOID string, checkErr error) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("deleting a tag needs its name")
	}
	if expectedRefOID == "" {
		return fmt.Errorf("refusing to delete %s: the verified object it points at is unknown", name)
	}
	if checkErr != nil {
		return fmt.Errorf("refusing to delete %s: its state could not be verified: %w", name, checkErr)
	}
	liveRefOID, err := r.tagRefOID(name)
	if err != nil {
		return fmt.Errorf("refusing to delete %s: %w", name, err)
	}
	if liveRefOID != expectedRefOID {
		return fmt.Errorf("refusing to delete %s: it changed since it was checked", name)
	}
	_, err = r.run(quick, "tag", "-d", "--", name)
	return err
}
