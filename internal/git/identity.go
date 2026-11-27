package git

import "strings"

// Identity is the name and email git writes into commits.
type Identity struct {
	Name, Email string
}

// GlobalIdentity reads user.name and user.email from the user's global
// git configuration. A value not set is empty.
func (r *Repo) GlobalIdentity() (Identity, error) {
	get := func(key string) (string, error) {
		out, err := r.run(quick, "config", "--global", "--get", key)
		if ExitCode(err) == 1 {
			return "", nil
		}
		return strings.TrimSpace(string(out)), err
	}
	name, err := get("user.name")
	if err != nil {
		return Identity{}, err
	}
	email, err := get("user.email")
	return Identity{Name: name, Email: email}, err
}

// SetGlobalIdentity writes user.name and user.email into the global git
// configuration. An empty value is left as it is.
func (r *Repo) SetGlobalIdentity(id Identity) error {
	for _, kv := range [][2]string{{"user.name", id.Name}, {"user.email", id.Email}} {
		if v := strings.TrimSpace(kv[1]); v != "" {
			if _, err := r.run(quick, "config", "--global", kv[0], v); err != nil {
				return err
			}
		}
	}
	return nil
}
