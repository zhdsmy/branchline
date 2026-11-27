package git

import "fmt"

// MergeFileWith opens an unmerged file in a merge tool defined by a command.
// Git runs the command with $BASE, $LOCAL, $REMOTE and $MERGED set.
func (r *Repo) MergeFileWith(name, command, path string) error {
	if name == "" || command == "" {
		return fmt.Errorf("no merge tool command is configured")
	}
	_, err := r.run(slow, "-c", "mergetool."+name+".cmd="+command, "-c", "mergetool."+name+".trustExitCode=true",
		"mergetool", "--no-prompt", "--tool="+name, "--", path)
	return err
}
