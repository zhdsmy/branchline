package git

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var _ GitCapabilityReader = (*Repo)(nil)

var (
	minSupportedGitVersion     = GitVersion{Major: 2, Minor: 25}
	minInitInitialBranchGitVer = GitVersion{Major: 2, Minor: 28}
	minStashStagedGitVer       = GitVersion{Major: 2, Minor: 35}
	minRebaseUpdateRefsGitVer  = GitVersion{Major: 2, Minor: 38}
)

// AtLeast reports whether v is numerically at least other.
func (v GitVersion) AtLeast(other GitVersion) bool {
	if v.Major != other.Major {
		return v.Major > other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor > other.Minor
	}
	return v.Patch >= other.Patch
}

// ParseGitVersion parses the output of git --version into its numeric
// release components while tolerating vendor suffixes such as Apple,
// Ubuntu, and Git for Windows builds.
func ParseGitVersion(raw string) (GitVersion, error) {
	line := strings.TrimSpace(raw)
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return GitVersion{}, fmt.Errorf("git: invalid version %q", raw)
	}
	rest := line
	switch {
	case line == "git version":
		return GitVersion{}, fmt.Errorf("git: invalid version %q", raw)
	case strings.HasPrefix(line, "git version"):
		if line[len("git version")] != ' ' && line[len("git version")] != '\t' {
			return GitVersion{}, fmt.Errorf("git: invalid version %q", raw)
		}
		rest = strings.TrimSpace(line[len("git version"):])
	case !isASCIIDigit(line[0]):
		return GitVersion{}, fmt.Errorf("git: invalid version %q", raw)
	}
	if rest == "" {
		return GitVersion{}, fmt.Errorf("git: invalid version %q", raw)
	}

	fields := strings.Fields(rest)
	token := fields[0]
	numPart := token
	if idx := strings.IndexAny(token, "-+"); idx >= 0 {
		numPart = token[:idx]
		suffix := token[idx+1:]
		if numPart == "" || suffix == "" || !isValidVendorToken(suffix) {
			return GitVersion{}, fmt.Errorf("git: invalid version %q", raw)
		}
	}

	parts := strings.Split(numPart, ".")
	if len(parts) < 2 {
		return GitVersion{}, fmt.Errorf("git: invalid version %q", raw)
	}
	for _, p := range parts {
		if p == "" {
			return GitVersion{}, fmt.Errorf("git: invalid version %q", raw)
		}
	}

	major, ok := parseVersionInt(parts[0])
	if !ok || major <= 0 {
		return GitVersion{}, fmt.Errorf("git: invalid version %q", raw)
	}
	minor, ok := parseVersionInt(parts[1])
	if !ok || minor < 0 {
		return GitVersion{}, fmt.Errorf("git: invalid version %q", raw)
	}

	patch := 0
	if len(parts) >= 3 {
		if p, ok := parseVersionInt(parts[2]); ok {
			patch = p
			for _, seg := range parts[3:] {
				if !isASCIIAlphaNum(seg) {
					return GitVersion{}, fmt.Errorf("git: invalid version %q", raw)
				}
			}
		} else if isKnownVendorSegment(parts[2]) {
			for _, seg := range parts[3:] {
				if !isASCIIAlphaNum(seg) {
					return GitVersion{}, fmt.Errorf("git: invalid version %q", raw)
				}
			}
		} else {
			return GitVersion{}, fmt.Errorf("git: invalid version %q", raw)
		}
	}

	return GitVersion{
		Major: major,
		Minor: minor,
		Patch: patch,
	}, nil
}

// Capabilities queries the repository executor's Git version and returns the
// supported capability flags without global caching or configuration changes.
func (r *Repo) Capabilities() (GitCapabilities, error) {
	if r == nil || r.Exec == nil {
		return GitCapabilities{}, errors.New("git: repository has no executor")
	}
	raw, err := r.Version()
	if err != nil {
		return GitCapabilities{}, err
	}
	ver, err := ParseGitVersion(raw)
	if err != nil {
		return GitCapabilities{}, err
	}
	return GitCapabilities{
		Version:           ver,
		RawVersion:        raw,
		MeetsMinimum:      ver.AtLeast(minSupportedGitVersion),
		RebaseUpdateRefs:  ver.AtLeast(minRebaseUpdateRefsGitVer),
		StashStaged:       ver.AtLeast(minStashStagedGitVer),
		InitInitialBranch: ver.AtLeast(minInitInitialBranchGitVer),
	}, nil
}

func parseVersionInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if !isASCIIDigit(s[i]) {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func isASCIIAlphaNum(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		if !isASCIIDigit(b) && (b < 'a' || b > 'z') && (b < 'A' || b > 'Z') {
			return false
		}
	}
	return true
}

func isValidVendorToken(s string) bool {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if !isASCIIDigit(b) && (b < 'a' || b > 'z') && (b < 'A' || b > 'Z') && b != '.' && b != '-' && b != '+' && b != '_' && b != '~' {
			return false
		}
	}
	return true
}

func isKnownVendorSegment(s string) bool {
	lower := strings.ToLower(s)
	switch lower {
	case "windows", "ubuntu", "vfs", "apple", "msysgit":
		return true
	}
	if strings.HasPrefix(lower, "rc") && len(lower) > 2 {
		_, ok := parseVersionInt(lower[2:])
		return ok
	}
	return false
}
