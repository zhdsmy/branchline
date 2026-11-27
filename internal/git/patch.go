package git

import (
	"errors"
	"fmt"
	"strings"
)

// ApplyOptions are the choices of the Apply Patch dialog.
type ApplyOptions struct {
	// ThreeWay falls back to a three-way merge when the patch does not
	// apply as is, leaving conflicts to resolve.
	ThreeWay bool
	// Reverse takes the patch back out.
	Reverse bool
	// Check only tests whether the patch applies, changing nothing.
	Check bool
	// Stage applies the patch to the index as well as the working tree.
	Stage bool
	// Commit applies a mailbox patch with git am, one commit for each
	// message, keeping their authors and messages.
	Commit bool
}

// ApplyResult says what applying a patch did.
type ApplyResult struct {
	// Stat is git's summary of the files the patch touches.
	Stat string
	// Checked is true when nothing changed because only a check ran.
	Checked bool
}

// ApplyPatchWith applies patch, read from this Mac, to the repository. The
// patch goes to git on standard input, so a repository on a server gets
// it over the same connection as every other command.
func (r *Repo) ApplyPatchWith(patch []byte, o ApplyOptions) (ApplyResult, error) {
	if strings.TrimSpace(string(patch)) == "" {
		return ApplyResult{}, errors.New("the patch is empty")
	}
	if o.Commit && o.Reverse {
		return ApplyResult{}, errors.New("a patch applied as commits cannot be reversed: clear Create commit or Reverse")
	}
	var res ApplyResult
	stat, err := r.runInput(quick, patch, applyArgs(o, "--stat")...)
	if err == nil {
		res.Stat = strings.TrimRight(string(stat), "\n")
	}
	if o.Check {
		_, err := r.runInput(quick, patch, applyArgs(o, "--check")...)
		res.Checked = true
		return res, err
	}
	if o.Commit {
		// A mailbox applies through git am. The observed native flow falls
		// back to a three-way merge without showing that switch.
		args := []string{"am", "--3way", "--keep-cr"}
		_, err := r.runInput(slow, patch, args...)
		if err != nil {
			return res, err
		}
		return res, nil
	}
	_, err = r.runInput(quick, patch, applyArgs(o, "")...)
	return res, err
}

func applyArgs(o ApplyOptions, mode string) []string {
	args := []string{"apply"}
	if mode == "--stat" {
		return append(args, "--stat", "-")
	}
	if mode != "" {
		args = append(args, mode)
	}
	if o.ThreeWay {
		args = append(args, "--3way")
	} else if o.Stage || o.Commit {
		args = append(args, "--index")
	}
	if o.Reverse {
		args = append(args, "--reverse")
	}
	return append(args, "-")
}

// PatchPrediction is what git apply --check says about a patch.
type PatchPrediction struct {
	// Clean is true when the patch applies without conflicts.
	Clean bool
	// Conflicts is true when git found changes the patch cannot be
	// matched to, which a three-way merge may still bring in.
	Conflicts bool
	// Message is git's own words when the patch cannot be applied.
	Message string
}

// PredictPatch tests whether patch applies to the working tree, changing
// nothing. Reverse and Stage count as they do when the patch is applied;
// a three-way merge does not, because conflicts are what is asked about.
func (r *Repo) PredictPatch(patch []byte, o ApplyOptions) PatchPrediction {
	if strings.TrimSpace(string(patch)) == "" {
		return PatchPrediction{Message: "the patch is empty"}
	}
	o.ThreeWay, o.Commit = false, false
	_, err := r.runInput(quick, patch, applyArgs(o, "--check")...)
	if err == nil {
		return PatchPrediction{Clean: true}
	}
	msg := strings.TrimSpace(err.Error())
	for _, s := range []string{"patch does not apply", "patch failed", "does not match index", "already exists in working directory"} {
		if strings.Contains(msg, s) {
			return PatchPrediction{Conflicts: true, Message: msg}
		}
	}
	return PatchPrediction{Message: msg}
}

// PatchHasCommitHeader reports whether patch is a mailbox patch as git
// format-patch writes it, with an author and a subject for a commit,
// rather than a bare diff.
func PatchHasCommitHeader(patch []byte) bool {
	for i, line := range strings.Split(string(patch), "\n") {
		if strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "Index: ") {
			return false
		}
		if f := strings.Fields(line); i == 0 && len(f) >= 2 && f[0] == "From" && len(f[1]) >= 40 {
			return true
		}
		if strings.HasPrefix(line, "Subject: ") {
			return true
		}
	}
	return false
}

// AmAbort gives up a git am that stopped, putting the branch back.
func (r *Repo) AmAbort() error {
	_, err := r.run(quick, "am", "--abort")
	return err
}

// PatchFileName is the name git format-patch gives the n-th patch of a
// series: 0001-the-subject.patch.
func PatchFileName(n int, subject string) string {
	var b strings.Builder
	dash := false
	for _, r := range subject {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.'
		if ok {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		} else {
			dash = true
		}
		if b.Len() >= 52 {
			break
		}
	}
	name := strings.Trim(b.String(), ".-")
	if name == "" {
		name = "patch"
	}
	return fmt.Sprintf("%04d-%s.patch", n, name)
}
