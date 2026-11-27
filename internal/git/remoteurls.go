package git

import "strings"

// RemoteURLs reads the fetch URLs, including url.insteadOf expansion as
// remote get-url does. It is intended for background repository refresh,
// so opening a menu never waits for git or for an SSH connection.
func (r *Repo) RemoteURLs() (map[string]string, error) {
	names, err := r.run(quick, "remote")
	if err != nil {
		return nil, err
	}
	urls := map[string]string{}
	for _, name := range strings.Split(strings.TrimSpace(string(names)), "\n") {
		if name == "" {
			continue
		}
		url, err := r.RemoteURL(name)
		if err != nil {
			return urls, err
		}
		urls[name] = url
	}
	return urls, nil
}
