package git

import (
	"fmt"
	"strings"
)

func validateInitBranch(branch string) error {
	if branch == "" || branch == "HEAD" || strings.HasPrefix(branch, "-") || strings.HasSuffix(branch, "/") || strings.ContainsAny(branch, " \t\r\n\x00") {
		return fmt.Errorf("git: invalid initial branch name %q", branch)
	}
	if problem := RefNameProblem(branch); problem != "" {
		return fmt.Errorf("git: invalid initial branch name %q: %s", branch, problem)
	}
	for _, part := range strings.Split(branch, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("git: invalid initial branch name %q", branch)
		}
	}
	return nil
}

// initCompat225 initializes a repository on Git < 2.28 where git init -b is
// unavailable. It sets the unborn initial branch with symbolic-ref only when
// the repository was newly created, never rewriting HEAD of an existing repo,
// unborn repository, or linked worktree.
func (r *Repo) initCompat225(branch string) error {
	alreadyInitialized := false
	if _, err := r.run(quick, "rev-parse", "--resolve-git-dir", ".git"); err == nil {
		alreadyInitialized = true
	} else if gitDirOut, err := r.run(quick, "rev-parse", "--git-dir"); err == nil {
		if gd := strings.TrimSpace(string(gitDirOut)); gd == "." || gd == ".git" {
			alreadyInitialized = true
		}
	}
	out, err := r.run(quick, "init")
	if err != nil {
		return err
	}
	if alreadyInitialized || strings.Contains(string(out), "Reinitialized existing") {
		return nil
	}
	if _, err := r.run(quick, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		return nil
	}
	_, err = r.run(quick, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	return err
}

// validateRevisionArg rejects empty revisions, option-injecting leading
// dashes, and NUL/newline control characters while preserving valid Git
// revision expressions such as reflog timestamps containing spaces or colons.
func validateRevisionArg(rev string) error {
	trimmed := strings.TrimSpace(rev)
	if trimmed == "" {
		return fmt.Errorf("git: empty revision")
	}
	if strings.HasPrefix(rev, "-") || strings.HasPrefix(trimmed, "-") {
		return fmt.Errorf("git: invalid revision %q", rev)
	}
	if strings.ContainsAny(rev, "\x00\r\n") {
		return fmt.Errorf("git: invalid revision %q", rev)
	}
	return nil
}

func validateRebaseOntoArg(onto string) error {
	if onto == "" || onto == "--root" {
		return nil
	}
	return validateRevisionArg(onto)
}

func (r *Repo) requireRebaseUpdateRefs() error {
	caps, err := r.Capabilities()
	if err != nil {
		return err
	}
	if !caps.RebaseUpdateRefs {
		return fmt.Errorf("git: rebase --update-refs requires Git 2.38 or newer (found %s)", caps.RawVersion)
	}
	return nil
}

func (r *Repo) requireStashStaged() error {
	caps, err := r.Capabilities()
	if err != nil {
		return err
	}
	if !caps.StashStaged {
		return fmt.Errorf("git: stash --staged requires Git 2.35 or newer (found %s)", caps.RawVersion)
	}
	return nil
}
