package git

import (
	"fmt"
	"strings"
)

// SafeDeleteRemoteTag deletes the tag name on remote, pinned by an exact
// force-with-lease on the object id the caller verified the remote tag to
// point at: the deletion lands only while the server still answers with that
// object, so a tag someone else moved or recreated since the check keeps its
// new value and is never removed by this call. The local tag is untouched in
// every outcome.
//
// Every guard runs before any network round-trip and each one refuses
// without pushing at all:
//
//   - checkErr non-nil means the remote state was never verified, so there is
//     nothing safe to pin the lease on;
//   - an empty or malformed expectedRemoteOID means the verified answer is
//     missing the one thing the lease pins — a deletion without it would race
//     whoever moved the tag;
//   - remote and name go through pushNameProblem, so neither can read as
//     command-line options or smuggle extra words into the lease refspec.
//
// The delete itself is git push --delete with the refspec behind "--": the
// lease option precedes the separator where git expects it, and a name
// starting with "-" stays a tag name instead of becoming flags. Git itself
// rejects a stale lease with "stale info" and changes nothing on the remote;
// a remote that never had the tag — or lost it since the check — answers with
// its own error, which passes through verbatim.
func (r *Repo) SafeDeleteRemoteTag(remote, name, expectedRemoteOID string, checkErr error) error {
	if checkErr != nil {
		return fmt.Errorf("refusing to delete %s on %s: the remote state could not be verified: %w", name, remote, checkErr)
	}
	expectedRemoteOID = strings.TrimSpace(expectedRemoteOID)
	if !validOID(expectedRemoteOID) {
		return fmt.Errorf("refusing to delete %s on %s: the verified remote object id is unknown", name, remote)
	}
	if problem := pushNameProblem("a remote", remote); problem != "" {
		return fmt.Errorf("deleting a remote tag: %s", problem)
	}
	if problem := pushNameProblem("a tag name", name); problem != "" {
		return fmt.Errorf("deleting a remote tag: %s", problem)
	}
	lease := "refs/tags/" + name + ":" + expectedRemoteOID
	_, err := r.run(slow, "push", "--force-with-lease="+lease, "--delete", "--", remote, "refs/tags/"+name)
	return err
}
