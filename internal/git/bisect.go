package git

import (
	"errors"
	"fmt"
	"strings"
)

func (r *Repo) Bisect(args ...string) (string, error) {
	out, err := r.run(quick, append([]string{"bisect"}, args...)...)
	text := strings.TrimSpace(string(out))
	var ge *Error
	if err != nil && errors.As(err, &ge) && ge.ExitCode > 0 && bisectOutcome(text) {
		return text, nil
	}
	return text, err
}

// bisectOutcome reports output that ends the search. Git uses a nonzero
// status for that output even though the session and its result remain.
func bisectOutcome(output string) bool {
	return strings.Contains(output, "first bad commit") || strings.Contains(output, "no single first bad commit") || strings.Contains(output, "only skipped commits left to test")
}

// BisectPhase is the state read from the bisection. Waiting has not
// established both boundaries. Searching has both and is checking out
// commits. Completed names one first bad commit. Ambiguous has no single
// first bad commit.
type BisectPhase string

const (
	BisectWaiting   BisectPhase = "waiting"
	BisectSearching BisectPhase = "searching"
	BisectCompleted BisectPhase = "completed"
	BisectAmbiguous BisectPhase = "ambiguous"
)

// BisectProgress is a bisection in progress: the commit checked out,
// the branch it started from, and how many commits are still to test.
type BisectProgress struct {
	Head       string
	FullHead   string
	Started    string
	Boundaries int
	Left       int
	Good, Bad  int
	Steps      int
	Phase      BisectPhase
	Result     string
	Marked     bool
	// HeadIsBad reports that HEAD is already the known bad commit. Good stays
	// disabled until another commit is checked out.
	HeadIsBad bool
}

// BisectProgress reads the bisection, or ok false when none is running.
func (r *Repo) BisectProgress() (p BisectProgress, ok bool, err error) {
	_, exists, err := r.ReadGitFileExists("BISECT_START")
	if err != nil || !exists {
		return BisectProgress{}, false, err
	}
	goodTerm, badTerm := "good", "bad"
	if good, err := r.run(quick, "bisect", "terms", "--term-good"); err != nil {
		if code := ExitCode(err); code != 128 && code != 1 {
			return BisectProgress{}, false, err
		}
	} else {
		goodTerm = strings.TrimSpace(string(good))
	}
	if bad, err := r.run(quick, "bisect", "terms", "--term-bad"); err != nil {
		if code := ExitCode(err); code != 128 && code != 1 {
			return BisectProgress{}, false, err
		}
	} else {
		badTerm = strings.TrimSpace(string(bad))
	}
	head, err := r.run(quick, "rev-parse", "--short=7", "HEAD")
	if err != nil {
		return BisectProgress{}, false, err
	}
	start, err := r.run(quick, "status", "--untracked-files=no")
	if err != nil {
		return BisectProgress{}, false, err
	}
	logText, _, err := r.ReadGitFileExists("BISECT_LOG")
	if err != nil {
		return BisectProgress{}, false, err
	}
	badRef := "refs/bisect/" + badTerm
	goodRef := "refs/bisect/" + goodTerm
	hasBad, err := r.refExists(badRef)
	if err != nil {
		return BisectProgress{}, false, err
	}
	hasGood, err := r.matchingRefExists(goodRef + "-*")
	if err != nil {
		return BisectProgress{}, false, err
	}
	headFull, err := r.run(quick, "rev-parse", "HEAD")
	if err != nil {
		return BisectProgress{}, false, err
	}
	boundaries := 0
	if hasBad {
		boundaries++
	}
	if hasGood {
		boundaries++
	}
	var vars []byte
	if hasBad && hasGood && !bisectFinished(logText) {
		vars, err = r.run(quick, "rev-list", "--bisect-vars", badRef, "--not", "--glob="+goodRef+"-*")
		if err != nil {
			return BisectProgress{}, false, fmt.Errorf("read bisect progress: %w", err)
		}
	}
	p = ParseBisectVars(vars)
	p.Head = strings.TrimSpace(string(head))
	p.FullHead = strings.TrimSpace(string(headFull))
	p.Started = bisectStart(string(start))
	p.Phase = bisectPhase(logText, hasBad && hasGood)
	p.Boundaries = boundaries
	if hasBad {
		badHash, hashErr := r.run(quick, "rev-parse", badRef)
		if hashErr != nil {
			return BisectProgress{}, false, hashErr
		}
		p.HeadIsBad = strings.TrimSpace(string(badHash)) == p.FullHead
	}
	p.Marked = p.Phase == BisectSearching
	if p.Phase == BisectCompleted || p.Phase == BisectAmbiguous {
		p.Result = bisectResult(logText)
	}
	return p, true, nil
}

func (r *Repo) refExists(ref string) (bool, error) {
	_, err := r.run(quick, "rev-parse", "-q", "--verify", ref)
	if err == nil {
		return true, nil
	}
	if ExitCode(err) == 1 {
		return false, nil
	}
	return false, err
}

func (r *Repo) matchingRefExists(pattern string) (bool, error) {
	out, err := r.run(quick, "for-each-ref", "--format=%(refname)", pattern)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func bisectFinished(logText string) bool {
	return strings.Contains(logText, "# first bad commit:") || strings.Contains(logText, "There is no single first bad commit") || strings.Contains(logText, "# only skipped commits left to test")
}

func bisectPhase(logText string, bounded bool) BisectPhase {
	switch {
	case strings.Contains(logText, "# first bad commit:"):
		return BisectCompleted
	case strings.Contains(logText, "There is no single first bad commit") || strings.Contains(logText, "# only skipped commits left to test"):
		return BisectAmbiguous
	case bounded:
		return BisectSearching
	default:
		return BisectWaiting
	}
}

func bisectResult(logText string) string {
	var result string
	for _, line := range strings.Split(logText, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if strings.HasPrefix(line, "first bad commit:") || strings.HasPrefix(line, "There is no single first bad commit") || strings.HasPrefix(line, "only skipped commits left to test") {
			result = line
		}
	}
	return result
}

// bisectStart reads "started from branch 'main'" from git status.
// A detached start has no branch name, so the hash in the message is used.
func bisectStart(status string) string {
	const marker = "started from "
	i := strings.Index(status, marker)
	if i < 0 {
		return ""
	}
	rest := status[i+len(marker):]
	if strings.HasPrefix(rest, "branch '") {
		rest = rest[len("branch '"):]
		if end := strings.IndexByte(rest, '\''); end >= 0 {
			return rest[:end]
		}
	}
	rest = strings.TrimPrefix(rest, "branch ")
	if end := strings.IndexAny(rest, ".\n"); end >= 0 {
		return strings.Trim(rest[:end], "'\"")
	}
	return ""
}
