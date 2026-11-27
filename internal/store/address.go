package store

import "strings"

// IsAddress reports whether s names a folder of a server, written as
// ParseAddress reads it (user@host:/path or ssh://user@host:port/path),
// rather than a folder of this Mac.
func IsAddress(s string) bool {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "ssh://") {
		return true
	}
	host, _, ok := strings.Cut(s, ":")
	return ok && len(host) > 1 && !strings.ContainsAny(host, "/\\~ ")
}
