package git

import (
	"errors"
	"strings"
)

// HEADKind classifies what HEAD points at.
type HEADKind int

const (
	// HEADBranch is a HEAD symbolic-ref whose target branch ref exists.
	HEADBranch HEADKind = iota
	// HEADUnborn is a HEAD symbolic-ref whose target branch ref does not exist
	// yet: an empty repository or a not yet committed --orphan branch.
	HEADUnborn
	// HEADDetached is a HEAD that is not a symbolic reference but resolves to
	// a valid commit object.
	HEADDetached
)

// HeadState reports what HEAD points at, structured so no failure is ever
// mistaken for one of the three legal states. The probe runs entirely through
// r.run so the local and SSH executors agree, and it is compatible with Git
// 2.25+ on Linux and macOS:
//
//  1. git symbolic-ref -q HEAD exit 0 with a non-empty ref means HEAD is a
//     symbolic reference. git show-ref --verify --quiet <ref> then decides:
//     exit 0 is a born branch, exit 1 the quiet "does not exist" answer of an
//     unborn branch, and any other exit code — a corrupt ref, a missing commit
//     object, or an SSH failure — is an error rather than a misread state.
//  2. git symbolic-ref -q HEAD exit 1 means HEAD is not a symbolic reference.
//     git rev-parse --verify HEAD^{commit} (kept without --quiet so the
//     diagnostic stderr survives) then decides: a hex object id is a detached
//     HEAD, anything else an error, because a detached HEAD pointing at an
//     invalid or missing object must not be reported as detached.
//  3. Any other exit code of symbolic-ref — a corrupt .git/HEAD, a corrupt or
//     empty target ref file, a corrupt packed-refs, a non-Git directory, or an
//     SSH connection failure — is an error, never an unborn or detached HEAD.
//
// The second return value is the short branch name (without refs/heads/) for
// HEADBranch and HEADUnborn, empty for HEADDetached or when err is non-nil.
func (r *Repo) HeadState() (HEADKind, string, error) {
	out, err := r.run(quick, "symbolic-ref", "-q", "HEAD")
	if err == nil {
		ref := strings.TrimSpace(string(out))
		if ref == "" {
			return 0, "", errors.New("git symbolic-ref HEAD returned an empty ref")
		}
		_, showErr := r.run(quick, "show-ref", "--verify", "--quiet", ref)
		name := strings.TrimPrefix(ref, "refs/heads/")
		switch {
		case showErr == nil:
			return HEADBranch, name, nil
		case ExitCode(showErr) == 1:
			return HEADUnborn, name, nil
		default:
			return 0, "", showErr
		}
	}
	if ExitCode(err) != 1 {
		return 0, "", err
	}
	revOut, verifyErr := r.run(quick, "rev-parse", "--verify", "HEAD^{commit}")
	if verifyErr != nil {
		return 0, "", verifyErr
	}
	if !isHex(strings.TrimSpace(string(revOut))) {
		return 0, "", errors.New("git rev-parse HEAD^{commit} did not return a hex object id")
	}
	return HEADDetached, "", nil
}

// isUnbornHEAD reports whether HEAD is a symbolic reference whose target ref
// does not exist yet, delegating to HeadState so the structural probe never
// misreads a corrupt repository, a missing directory, or an SSH failure as an
// unborn HEAD.
func (r *Repo) isUnbornHEAD() bool {
	kind, _, err := r.HeadState()
	return err == nil && kind == HEADUnborn
}
