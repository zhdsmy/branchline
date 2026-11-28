package git

import (
	"fmt"
	"strings"
)

// pushNameProblem validates a remote or tag name before it goes onto a push
// command line. Beyond an empty value it rejects a leading "-" — git would
// read it as options — and whitespace, which is never valid in either kind of
// name and would smuggle extra words into a force-with-lease refspec.
func pushNameProblem(kind, name string) string {
	if strings.TrimSpace(name) == "" {
		return kind + " is required"
	}
	if strings.HasPrefix(name, "-") {
		return kind + " cannot begin with '-'"
	}
	if strings.ContainsAny(name, " \t\r\n") {
		return kind + " cannot contain whitespace"
	}
	return ""
}

func validOID(s string) bool {
	return len(s) >= 40 && isHex(s)
}

// RemoteTagOID reads the object id the tag name points at on remote through
// ls-remote. The exact-ref match — never the peeled ^{} line — is the value a
// force-with-lease push compares against: for an annotated tag it is the tag
// object, for a lightweight one the commit itself.
//
// A failed read is always an error, never an absence: a remote that cannot
// answer — unreachable, an authentication failure, a dropped connection —
// must not look like a remote without the tag, because callers decide whether
// overwriting is safe from the answer. Only a successful listing without the
// exact ref means the remote does not have the tag.
func (r *Repo) RemoteTagOID(remote, name string) (oid string, exists bool, err error) {
	if problem := pushNameProblem("a remote", remote); problem != "" {
		return "", false, fmt.Errorf("checking the remote tag: %s", problem)
	}
	if problem := pushNameProblem("a tag name", name); problem != "" {
		return "", false, fmt.Errorf("checking the remote tag: %s", problem)
	}
	out, err := r.run(slow, "ls-remote", "--tags", "--", remote, "refs/tags/"+name)
	if err != nil {
		return "", false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "refs/tags/"+name {
			continue
		}
		if !validOID(fields[0]) {
			return "", false, fmt.Errorf("the remote printed no object id for tag %s", name)
		}
		return fields[0], true, nil
	}
	return "", false, nil
}

// RemoteTagOIDs reads the object ids the tag names point at on remote
// through a single ls-remote, returning a map of the tags the remote
// actually has: a name missing from the map is a tag the remote does not
// carry, never an error. The values are the exact-ref matches — the exact
// patterns asked for never even print the peeled ^{} lines — because they
// are what a force-with-lease push compares against: for an annotated tag
// the tag object, for a lightweight one the object it names.
//
// A failed read is always an error, never an absence: a remote that cannot
// answer — unreachable, an authentication failure, a dropped connection —
// must not look like a remote without the tags, because callers decide
// whether overwriting is safe from the answer. Only a successful listing
// means the missing tags are really absent. Unlike RemoteTagOID the remote
// name is never rejected for a leading "-": the separator keeps it the
// repository it is, as the batch deletion of a dash-named remote requires.
func (r *Repo) RemoteTagOIDs(remote string, names []string) (map[string]string, error) {
	if strings.TrimSpace(remote) == "" {
		return nil, fmt.Errorf("checking the remote tags: a remote is required")
	}
	positional := []string{"ls-remote", "--tags", "--", remote}
	want := map[string]bool{}
	for _, name := range names {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		positional = append(positional, "refs/tags/"+name)
		want["refs/tags/"+name] = true
	}
	out, err := r.run(slow, positional...)
	if err != nil {
		return nil, err
	}
	oids := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !want[fields[1]] {
			continue
		}
		if !validOID(fields[0]) {
			return nil, fmt.Errorf("the remote printed no object id for %s", fields[1])
		}
		oids[strings.TrimPrefix(fields[1], "refs/tags/")] = fields[0]
	}
	return oids, nil
}

// verifyLocalTagExists proves refs/tags/<name> exists locally before it is
// pushed. A read failure is an error instead of permission: pushing what the
// repository cannot even confirm would push nothing, or worse, surprise.
func (r *Repo) verifyLocalTagExists(name string) error {
	if _, err := r.run(quick, "show-ref", "--verify", "--quiet", "refs/tags/"+name); err != nil {
		if ExitCode(err) == 1 {
			return fmt.Errorf("tag %s does not exist locally", name)
		}
		return fmt.Errorf("cannot verify local tag %s: %w", name, err)
	}
	return nil
}

// SafePushTag pushes the one tag name to remote, never forced: a remote tag
// of the same name pointing anywhere else is refused by git and never
// overwritten. The push is porcelain so an already-present identical tag
// reads as UpToDatePush instead of silence.
func (r *Repo) SafePushTag(remote, name string) (string, error) {
	if problem := pushNameProblem("a remote", remote); problem != "" {
		return "", fmt.Errorf("pushing a tag: %s", problem)
	}
	if problem := pushNameProblem("a tag name", name); problem != "" {
		return "", fmt.Errorf("pushing a tag: %s", problem)
	}
	if err := r.verifyLocalTagExists(name); err != nil {
		return "", err
	}
	out, err := r.run(slow, "push", "--porcelain", "--", remote, "refs/tags/"+name)
	if err == nil && allUpToDate(string(out)) {
		return UpToDatePush, nil
	}
	return "", err
}

// SafeForcePushTag pushes the tag name to remote with an exact force-with-lease
// pinning the remote's current object id: the overwrite lands only while the
// remote still points at what the caller verified. The guards run before any
// push and each one refuses without touching the network:
//
//   - checkErr non-nil means the remote state was never verified, so there is
//     nothing safe to overwrite onto;
//   - remoteExists with an empty or malformed expectedRemoteOID means the
//     verified answer is missing the one thing the lease pins, so the lease
//     cannot be written;
//   - a local tag that cannot be verified absent or present is never pushed.
//
// With remoteExists false the lease is written with an empty expectation
// (refs/tags/<name>:), so a tag someone else creates in the meantime is
// refused atomically instead of overwritten. Git itself rejects a stale lease
// with "stale info" and changes nothing on the remote.
func (r *Repo) SafeForcePushTag(remote, name, expectedRemoteOID string, remoteExists bool, checkErr error) error {
	if checkErr != nil {
		return fmt.Errorf("refusing to force push %s: the remote state could not be verified: %w", name, checkErr)
	}
	expectedRemoteOID = strings.TrimSpace(expectedRemoteOID)
	if remoteExists && !validOID(expectedRemoteOID) {
		return fmt.Errorf("refusing to force push %s: the verified remote object id is unknown", name)
	}
	if problem := pushNameProblem("a remote", remote); problem != "" {
		return fmt.Errorf("force pushing a tag: %s", problem)
	}
	if problem := pushNameProblem("a tag name", name); problem != "" {
		return fmt.Errorf("force pushing a tag: %s", problem)
	}
	if err := r.verifyLocalTagExists(name); err != nil {
		return err
	}
	lease := "refs/tags/" + name + ":"
	if remoteExists {
		lease += expectedRemoteOID
	}
	_, err := r.run(slow, "push", "--force-with-lease="+lease, "--", remote, "refs/tags/"+name)
	return err
}

// SafePushAllTags pushes every local tag to remote, never forced: remote tags
// pointing anywhere else are refused by git and never overwritten. Without a
// single local tag there is nothing to send, which is an error instead of a
// pointless push. Porcelain output makes the all-already-there answer read as
// UpToDatePush.
func (r *Repo) SafePushAllTags(remote string) (string, error) {
	if problem := pushNameProblem("a remote", remote); problem != "" {
		return "", fmt.Errorf("pushing all tags: %s", problem)
	}
	out, err := r.run(quick, "for-each-ref", "--format=%(refname:short)", "refs/tags")
	if err != nil {
		return "", fmt.Errorf("cannot list local tags: %w", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		return "", fmt.Errorf("No local tags to push.")
	}
	out, err = r.run(slow, "push", "--porcelain", "--tags", "--", remote)
	if err == nil && allUpToDate(string(out)) {
		return UpToDatePush, nil
	}
	return "", err
}

// SafeCreateTagAndPush creates the tag through SafeCreateTag and then pushes
// it to remote. A failed push leaves the created tag in place and says so:
// losing the local tag because the network refused it would destroy the very
// thing the user just made.
func (r *Repo) SafeCreateTagAndPush(name, rev, message, remote string) error {
	if err := r.SafeCreateTag(name, rev, message); err != nil {
		return err
	}
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return nil
	}
	if _, err := r.SafePushTag(remote, name); err != nil {
		return fmt.Errorf("tag %s was created, but pushing it to %s failed: %w", name, remote, err)
	}
	return nil
}
