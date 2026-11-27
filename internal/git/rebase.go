package git

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RebaseTodo is one line of an interactive rebase.
type RebaseTodo struct {
	Action  string // pick, reword, edit, squash, fixup, drop
	Hash    string
	Subject string
	// PreparedMessage replaces the complete message after this step succeeds,
	// including after a conflict/edit stop and a new app session. Empty disables it.
	// Unlike Subject this also applies to a pick/fixup at the end of a fold chain.
	PreparedMessage string
	// Author, Email and Date are shown by the interactive rebase list.
	// They are not part of the todo git reads.
	Author string
	Email  string
	Date   time.Time
}

// RebasePlan lists the commits git would replay from onto to HEAD, oldest first.
func (r *Repo) RebasePlan(onto string) ([]RebaseTodo, error) {
	if err := validateRebaseOntoArg(onto); err != nil {
		return nil, err
	}
	rev := onto + "..HEAD"
	if onto == "" || onto == "--root" {
		// A root commit has no parent. HEAD lists the whole branch.
		rev = "HEAD"
	}
	out, err := r.run(quick, "rev-list", "--reverse", "--format=%H%x1f%s%x1f%an%x1f%ae%x1f%at", rev)
	if err != nil {
		return nil, err
	}
	var plan []RebaseTodo
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, "\x1f")
		if len(fields) < 2 || !isHex(fields[0]) {
			continue
		}
		step := RebaseTodo{Action: "pick", Hash: fields[0], Subject: fields[1]}
		if len(fields) >= 5 {
			step.Author, step.Email = fields[2], fields[3]
			if secs, err := strconv.ParseInt(fields[4], 10, 64); err == nil {
				step.Date = time.Unix(secs, 0)
			}
		}
		plan = append(plan, step)
	}
	if len(plan) == 0 {
		return nil, fmt.Errorf("no commits from %s to HEAD", onto)
	}
	return plan, nil
}

// RebaseWith starts git's own interactive rebase of plan onto onto.
// A conflict or an edit stop leaves the rebase in progress, so it can be
// continued, skipped or aborted. Prepared messages survive either stop.
func (r *Repo) RebaseWith(onto string, plan []RebaseTodo) error {
	return r.RebaseWithOptions(onto, plan, RebaseOptions{})
}

// RebaseOptions are the choices of the interactive rebase dialog.
type RebaseOptions struct {
	// Backup records refs/backup/<branch>-<short head> before the rebase.
	Backup bool
	// UpdateRefs moves other local branches whose tips are in the range
	// being replayed, using the update-ref lines of this rebase.
	UpdateRefs bool
}

// RebaseWithOptions starts the rebase and, when asked, backs up the branch
// and updates other local branches that contain the commits being replayed.
// Local changes are stashed first and put back when the rebase finishes.
// Git keeps an autostash across stops and restores it on continue or abort.
func (r *Repo) RebaseWithOptions(onto string, plan []RebaseTodo, o RebaseOptions) error {
	if len(plan) == 0 {
		return errors.New("the rebase plan is empty")
	}
	return r.rebaseWithOptions(onto, plan, o)
}

func (r *Repo) rebaseWithOptions(onto string, plan []RebaseTodo, o RebaseOptions) error {
	if err := validateRebaseOntoArg(onto); err != nil {
		return err
	}
	kept := false
	for _, step := range plan {
		action := step.Action
		if action == "" {
			action = "pick"
		}
		switch action {
		case "pick", "reword", "edit", "squash", "fixup", "drop":
		default:
			return fmt.Errorf("unknown rebase action %q", action)
		}
		if !isHex(step.Hash) || strings.HasPrefix(step.Hash, "-") {
			return fmt.Errorf("rebase step %q is not a commit", step.Hash)
		}
		if (action == "squash" || action == "fixup") && !kept {
			return fmt.Errorf("%s %s has no previous commit to fold into", action, short(step.Hash))
		}
		if action != "drop" {
			kept = true
		}
		subject := strings.TrimSpace(step.Subject)
		prepared := strings.TrimSpace(step.PreparedMessage)
		if action == "drop" && prepared != "" {
			return errors.New("a dropped commit cannot have a prepared message")
		}
		if action == "reword" && subject == "" && prepared == "" {
			return errors.New("a reworded commit needs a subject")
		}
	}
	if o.UpdateRefs {
		if err := r.requireRebaseUpdateRefs(); err != nil {
			return err
		}
	}
	if o.Backup {
		if err := r.backupBranch(); err != nil {
			return err
		}
	}
	var updateAt map[string][]string
	if o.UpdateRefs {
		var err error
		updateAt, err = r.rebaseUpdates(plan)
		if err != nil {
			return err
		}
	}
	// A durable ownership marker prevents --skip from applying a pending
	// message to the preceding unrelated commit. Fold messages belong to the
	// entire chain, so the marker is before its parent (an empty final Fixup
	// can legitimately leave HEAD unchanged). Git removes these files with
	// its rebase state on completion/abort; a reopened app needs no memory.
	before := map[int][]int{}
	for i, step := range plan {
		if strings.TrimSpace(step.PreparedMessage) == "" {
			continue
		}
		owner := rebaseMessageOwner(plan, i)
		before[owner] = append(before[owner], i)
		for j := owner; j < i; j++ {
			if plan[j].Action != "drop" {
				if refs := updateAt[plan[j].Hash]; len(refs) != 0 {
					updateAt[step.Hash] = append(updateAt[step.Hash], refs...)
					delete(updateAt, plan[j].Hash)
				}
			}
		}
	}
	todo := make([]string, 0, len(plan)*2)
	for i, step := range plan {
		action := step.Action
		if action == "" {
			action = "pick"
		}
		subject := strings.TrimSpace(step.Subject)
		prepared := strings.TrimSpace(step.PreparedMessage)
		gitAction := action
		if action == "reword" {
			gitAction = "pick"
		}
		for _, target := range before[i] {
			path := "$(git rev-parse --git-path rebase-merge/branchline-message-before-" + strconv.Itoa(target) + ")"
			todo = append(todo, "exec git rev-parse HEAD > \""+path+"\"")
		}
		todo = append(todo, gitAction+" "+step.Hash+" "+oneLine(step.Subject))
		message := prepared
		if message == "" && (action == "reword" || (action == "squash" && subject != "")) {
			message = subject
		}
		if message != "" {
			// This runs only after its commit succeeds, even after a conflict
			// or app restart. Message text never becomes shell code.
			out, err := r.runInput(quick, []byte(message+"\n"), "hash-object", "-w", "--stdin")
			if err != nil {
				return err
			}
			blob := strings.TrimSpace(string(out))
			if !isHex(blob) {
				return errors.New("git returned an invalid message object")
			}
			amend := "git cat-file blob " + blob + " | git commit --amend --allow-empty --no-verify -q -F -"
			if prepared != "" {
				marker := "$(git rev-parse --git-path rebase-merge/branchline-message-before-" + strconv.Itoa(i) + ")"
				amend = "before=$(cat \"" + marker + "\") && current=$(git rev-parse HEAD) && { test \"$before\" = \"$current\" || { " + amend + "; }; }"
			}
			todo = append(todo, "exec "+amend)
		}
		for _, ref := range updateAt[step.Hash] {
			todo = append(todo, "update-ref "+ref)
		}
	}
	if onto == "--root" {
		onto = ""
	}
	err := r.rebaseSequence(onto, strings.Join(todo, "\n")+"\n")
	if err != nil && (!rebaseStopped(err) || !r.rebasing()) {
		return err
	}
	return nil
}

// backupBranch records the checked-out branch as a local ref,
// refs/backup/<branch>-<short head>. A detached HEAD uses HEAD in the name.
func (r *Repo) backupBranch() error {
	st, err := r.Status()
	if err != nil {
		return err
	}
	if st.Head == "" {
		return fmt.Errorf("a backup needs a commit")
	}
	name := st.Branch
	if name == "" {
		name = "HEAD"
	}
	ref := "refs/backup/" + backupName(name) + "-" + short(st.Head)
	_, err = r.run(quick, "update-ref", ref, st.Head)
	return err
}

func backupName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "-")
	if name == "" {
		return "HEAD"
	}
	return name
}

// rebaseUpdates maps each replayed commit to the other local branches
// whose tip is that commit. The current branch is left to the rebase itself.
// Git applies an update-ref line to the commit rewritten just before it.
func (r *Repo) rebaseUpdates(plan []RebaseTodo) (map[string][]string, error) {
	st, err := r.Status()
	if err != nil {
		return nil, err
	}
	refs, err := r.Refs()
	if err != nil {
		return nil, err
	}
	at := map[string][]string{}
	for _, b := range refs.Branches {
		if b.Name == "" || b.Name == st.Branch || b.Current || b.Hash == "" {
			continue
		}
		for _, step := range plan {
			if step.Hash == b.Hash {
				at[step.Hash] = append(at[step.Hash], "refs/heads/"+b.Name)
				break
			}
		}
	}
	return at, nil
}

// rebaseSequence runs git rebase -i onto with todo already decided.
// git reads the instruction list from the sequence editor's input, which
// works for a local checkout and a repository reached over SSH.
func (r *Repo) rebaseSequence(onto, todo string) error {
	args := []string{"-c", "sequence.editor=cp /dev/stdin", "rebase", "-i", "--autostash"}
	if onto == "" {
		args = append(args, "--root")
	} else {
		args = append(args, onto)
	}
	_, err := r.runPrepared([]byte(todo), args...)
	return err
}

func rebaseStopped(err error) bool {
	code := ExitCode(err)
	return code == 0 || code == 1
}

// oneLine keeps a todo comment on the instruction line.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

// rebaseMessageOwner finds the parent whose files a prepared fold message owns.
// Drops do not break that chain; no application selection or mutable UI is read.
func rebaseMessageOwner(plan []RebaseTodo, i int) int {
	if plan[i].Action != "squash" && plan[i].Action != "fixup" {
		return i
	}
	for j := i - 1; j >= 0; j-- {
		switch plan[j].Action {
		case "drop", "squash", "fixup":
			continue
		default:
			return j
		}
	}
	return i
}

func (r *Repo) RepairInterruptedRebaseTodo() error {
	out, err := r.run(quick, "rev-parse", "--verify", "--quiet", "REBASE_HEAD")
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return nil
	}
	if err != nil && ExitCode(err) != 1 {
		return err
	}
	if !r.rebasing() {
		return nil
	}
	return errors.New("rebase was interrupted in an unknown state without REBASE_HEAD; refuse to continue automatically to avoid dropping or duplicating commits (use Abort to restore original branch and autostash)")
}
