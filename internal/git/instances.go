package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Instance is a git program on this Mac with the version it reports.
type Instance struct {
	Path    string
	Version string
}

// KnownInstances are the places git usually is: Homebrew, the Xcode tools
// and the system.
var KnownInstances = []string{
	"/opt/homebrew/bin/git",
	"/usr/local/bin/git",
	"/usr/bin/git",
	"/Library/Developer/CommandLineTools/usr/bin/git",
}

// CheckBinary runs path --version and returns the version git reports,
// such as "2.43.0". A program that is not git is an error.
func CheckBinary(path string) (string, error) {
	path = strings.TrimSpace(path)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s is not a valid Git binary.", path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	line := strings.TrimSpace(string(out))
	if err != nil || !strings.HasPrefix(line, "git version") {
		return "", fmt.Errorf("%s is not a valid Git binary.", path)
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "git version")), nil
}

// FindInstances lists the git programs of KnownInstances that run, and
// the first git on PATH when it is another one.
func FindInstances(extra ...string) []Instance {
	candidates := append([]string{}, extra...)
	candidates = append(candidates, KnownInstances...)
	var out []Instance
	seen := map[string]bool{}
	for _, path := range candidates {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if v, err := CheckBinary(path); err == nil {
			out = append(out, Instance{Path: path, Version: v})
		}
	}
	return out
}

// LookPath is the git a bare "git" starts, empty when there is none.
func LookPath() string {
	p, _ := exec.LookPath("git")
	return p
}
