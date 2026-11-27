package git

import "strings"

// RecentMessages returns the messages of the latest commits that differ from
// one another, newest first, at most limit of them: the recent commit messages
// offered to start a new message from.
func (r *Repo) RecentMessages(limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	out, err := r.run(quick, "log", "-n", "100", "--format=%B%x1e")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var list []string
	for _, m := range strings.Split(string(out), "\x1e") {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		list = append(list, m)
		if len(list) == limit {
			break
		}
	}
	return list, nil
}
