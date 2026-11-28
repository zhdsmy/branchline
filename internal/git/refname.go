package git

import "strings"

// RefNameProblem says why name cannot be a branch or tag name. It is empty
// for a usable name. The rules are those of git check-ref-format, checked
// locally so the dialogs can answer as the user types, on this Mac or for a
// repository on a server. A space is not reported: the dialogs replace it
// as the Space character setting says.
func RefNameProblem(name string) string {
	switch {
	case name == "":
		return ""
	case name == "@":
		return "Branch or tag name cannot be '@'."
	case strings.HasPrefix(name, "-"):
		return "Must not start with '-'."
	case strings.HasPrefix(name, "/"):
		return "Must not start with '/'."
	case strings.HasPrefix(name, "."):
		return "Must not start with '.'."
	case strings.Contains(name, ".."):
		return "Must not contain '..'."
	case strings.Contains(name, "//"):
		return "Must not contain consecutive '/'."
	case strings.Contains(name, "/."):
		return "Path segment must not start with '.'."
	case strings.Contains(name, "/") && strings.HasSuffix(name, ".lock"):
		return "Path segment must not end with '.lock'."
	case strings.HasSuffix(name, ".lock"):
		return "Must not end with '.lock'."
	case strings.HasSuffix(name, "."):
		return "Must not end with '.'."
	case strings.Contains(name, "@{"):
		return "Must not contain '@{'."
	case strings.Contains(name, "\\"):
		return "Must not contain '\\'."
	case strings.ContainsAny(name, "?*["):
		return "Must not contain '?', '*', or '['."
	case strings.ContainsAny(name, "~^:") || strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0:
		return "Must not contain '~', '^', ':', or control characters."
	}
	return ""
}
