package git

import (
	"fmt"
	"strings"
)

// Status returns the state of the working tree.
func (r *Repo) Status() (Status, error) {
	return r.StatusOpts(false)
}

// StatusOpts returns the working tree. showIgnored also lists untracked
// paths excluded by .gitignore, as git status --ignored=matching. The read
// never runs the filter programs the repository defines: a status is the
// app's own bookkeeping, not a user action. When the filters cannot even be
// listed the read fails instead of running them unguarded. Submodules keep
// their own configuration, which no listing of the parent's can see, so the
// dirty check that would descend into them is off; a submodule still shows
// the moment the commit it points at moves.
func (r *Repo) StatusOpts(showIgnored bool) (Status, error) {
	passive, err := r.passiveFilterConfig()
	if err != nil {
		return Status{}, fmt.Errorf("read repository filter configuration: %w", err)
	}
	args := append(append([]string{}, passive...), "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all", "--ignore-submodules=dirty")
	if showIgnored {
		args = append(args, "--ignored=matching")
	}
	out, err := r.run(quick, args...)
	if err != nil {
		return Status{}, err
	}
	st := ParseStatus(out)
	st.State = r.statePassive(passive)
	if st.State == "rebase" {
		st.Stopped = r.rebaseStop(st.Conflicted())
	}
	operation, err := r.operationTarget(st.State)
	if err != nil {
		return Status{}, err
	}
	st.Operation = operation
	return st, nil
}

func (r *Repo) operationTarget(state string) (string, error) {
	if state == "am" {
		return r.amOperation()
	}
	rev := map[string]string{"merge": "MERGE_HEAD", "cherry-pick": "CHERRY_PICK_HEAD", "revert": "REVERT_HEAD", "rebase": "REBASE_HEAD"}[state]
	if rev == "" {
		return "", nil
	}
	out, err := r.run(quick, "rev-parse", "--verify", "--quiet", rev)
	if err != nil {
		if ExitCode(err) == 1 {
			if state == "rebase" {
				return r.rebaseInterruptedTarget()
			}
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (r *Repo) rebaseInterruptedTarget() (string, error) {
	done, doneExists, err := r.ReadGitFileExists("rebase-merge/done")
	if err != nil {
		return "", err
	}
	todo, todoExists, err := r.ReadGitFileExists("rebase-merge/git-rebase-todo")
	if err != nil {
		return "", err
	}
	origHead, origExists, err := r.ReadGitFileExists("rebase-merge/orig-head")
	if err != nil {
		return "", err
	}
	if !doneExists && !todoExists && !origExists {
		return "", nil
	}
	summary := strings.TrimSpace(origHead) + "\n" + done + "\n--\n" + todo
	digest, err := r.hashText(summary)
	if err != nil {
		return "", err
	}
	if _, hash := lastRebaseDoneAction(done); hash != "" {
		return hash + ":" + digest, nil
	}
	return digest, nil
}

func lastRebaseDoneAction(done string) (action, hash string) {
	lines := strings.Split(done, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		act := fields[0]
		switch act {
		case "pick", "p", "reword", "r", "edit", "e", "squash", "s", "fixup", "f", "break", "b", "exec", "x", "drop", "d", "label", "l", "reset", "t", "merge", "m", "update-ref", "u":
			h := ""
			if len(fields) >= 2 && isHex(fields[1]) {
				h = fields[1]
			}
			return act, h
		}
	}
	return "", ""
}

// amOperation distinguishes one stopped mailbox. HEAD stays still while a
// patch conflicts, and an empty patch is a real stopped session. The identity
// uses its position, patch, and author. Missing progress is an error; an empty
// patch file is not.
func (r *Repo) amOperation() (string, error) {
	next, nextExists, err := r.readAMFile("rebase-apply/next")
	if err != nil {
		return "", err
	}
	last, lastExists, err := r.readAMFile("rebase-apply/last")
	if err != nil {
		return "", err
	}
	patch, patchExists, err := r.readAMFile("rebase-apply/patch")
	if err != nil {
		return "", err
	}
	author, authorExists, err := r.readAMFile("rebase-apply/author-script")
	if err != nil {
		return "", err
	}
	next, last = strings.TrimSpace(next), strings.TrimSpace(last)
	if !nextExists || !lastExists || !patchExists || !authorExists || next == "" || last == "" {
		return "", fmt.Errorf("AM session is missing its current patch")
	}
	patchHash, err := r.hashText(patch)
	if err != nil {
		return "", err
	}
	authorHash, err := r.hashText(author)
	if err != nil {
		return "", err
	}
	return next + "/" + last + " " + patchHash + " " + authorHash, nil
}

func (r *Repo) readAMFile(name string) (string, bool, error) {
	return r.ReadGitFileExists(name)
}

func (r *Repo) hashText(text string) (string, error) {
	out, err := r.runInput(quick, []byte(text), "hash-object", "--stdin")
	if err != nil {
		return "", err
	}
	hash := strings.TrimSpace(string(out))
	if hash == "" {
		return "", fmt.Errorf("git hash-object returned no hash")
	}
	return hash, nil
}

// state finds a merge, rebase, cherry-pick, revert or bisect in progress.
// git status says which one. A leftover REBASE_HEAD is not enough: git keeps
// that object after the rebase finishes. The status read is guarded against
// the repository's own filters like every read the app runs on its own; a
// failure to list them only loses the state, never runs them.
func (r *Repo) state() string {
	passive, err := r.passiveFilterConfig()
	if err != nil {
		return ""
	}
	return r.statePassive(passive)
}

// statePassive is state for a caller that already listed the repository's
// own filters, so one status read costs one listing instead of two. The
// read only needs the lines about an operation in progress, which git
// prints regardless of the file list, so an everything-excluding pathspec
// keeps git from scanning the whole working tree a second time. With every
// path pruned a conflicted merge reads as "All conflicts fixed but you are
// still merging" even before resolution, which the keyword match below
// already treats as a merge.
func (r *Repo) statePassive(passive []string) string {
	args := append(append([]string{}, passive...), "status", "--untracked-files=no", "--ignore-submodules=dirty", "--", ":(exclude)*")
	out, err := r.run(quick, args...)
	if err != nil {
		return ""
	}
	text := string(out)
	switch {
	case strings.Contains(text, "in the middle of an am session"):
		return "am"
	case strings.Contains(text, "interactive rebase in progress") || strings.Contains(text, "rebase in progress"):
		return "rebase"
	case strings.Contains(text, "cherry-pick in progress") || strings.Contains(text, "cherry-picking"):
		return "cherry-pick"
	case strings.Contains(text, "revert in progress") || strings.Contains(text, "reverting"):
		return "revert"
	case strings.Contains(text, "merge in progress") || strings.Contains(text, "merging") || strings.Contains(text, "All conflicts fixed but you are still merging") || strings.Contains(text, "fix conflicts and run"):
		return "merge"
	case strings.Contains(text, "bisect"):
		return "bisect"
	}
	return ""
}

// rebasing reports an unfinished rebase. git config reads a file inside
// the rebase directory, which works for a local checkout and over SSH.
func (r *Repo) rebasing() bool {
	out, err := r.run(quick, "rev-parse", "--git-path", "rebase-merge")
	if err != nil {
		return false
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return false
	}
	_, err = r.run(quick, "config", "--file", path+"/interactive", "--list")
	return err == nil
}

// rebaseStop reads why git paused. A message file with content means
// git is waiting for the commit message; stopped-sha means the commit is
// checked out for editing. Both are read with hash-object, which only
// needs the file and works over SSH.
func (r *Repo) rebaseStop(conflict bool) string {
	if conflict {
		return "conflict"
	}
	out, err := r.run(quick, "rev-parse", "--git-path", "rebase-merge")
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return ""
	}
	// An edit stop also leaves a short message file, so the commit
	// hash git recorded decides it before the message does.
	if r.fileNoneEmpty(dir + "/stopped-sha") {
		return "edit"
	}
	if r.fileNoneEmpty(dir + "/message") {
		return "message"
	}
	return ""
}

// fileNoneEmpty reports a file git can read that is not empty. The empty
// blob is what hash-object prints for a zero-length file. --no-filters
// keeps the repository's own filters out of the read: git applies the
// working tree's attributes even to paths inside .git, and the check only
// ever compares a byte count.
func (r *Repo) fileNoneEmpty(path string) bool {
	out, err := r.run(quick, "hash-object", "--no-filters", "--", path)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"
}
