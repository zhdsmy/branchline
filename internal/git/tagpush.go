package git

import "fmt"

// CreateTagAndPush creates the tag at rev, annotated when message is not
// empty, and sends it to each of remotes. A push that fails leaves the tag
// in place and says which remote refused it.
func (r *Repo) CreateTagAndPush(name, rev, message string, remotes []string) error {
	if err := r.CreateTag(name, rev, message); err != nil {
		return err
	}
	for _, remote := range remotes {
		if err := r.PushRefs(remote, []string{"refs/tags/" + name}); err != nil {
			return fmt.Errorf("tag %s was created, but pushing it to %s failed: %w", name, remote, err)
		}
	}
	return nil
}
