package git_test

// Names starting with = reach git quoted for zsh; bash, dash and ash do not
// expand them, but the quoting must still parse to the literal word under
// each shell a server can carry. The product operations run against the
// configured Linux host's login shell, and every shell installed on the
// host then proves it reads the quoted forms back verbatim: the probe hands
// each shell the quoted text as a command, the way ssh does.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

func TestConfiguredLinuxNames(t *testing.T) {
	srv := sshtest.ConfiguredLinux(t)

	t.Run("EqualsNames", func(t *testing.T) {
		runEqualsNamesOverSSH(t, srv)
	})

	t.Run("ShellsReadQuotedNames", func(t *testing.T) {
		words := []string{"=ls", "=", "a=b"}
		var probe strings.Builder
		for _, shell := range []string{"bash", "dash", "ash", "zsh"} {
			fmt.Fprintf(&probe, "if command -v %s >/dev/null 2>&1; then\n", shell)
			for _, w := range words {
				inner := "printf %s " + git.ShellQuote(w)
				fmt.Fprintf(&probe, "out=$(%s -c %s) || exit 1\n", shell, git.ShellQuote(inner))
				fmt.Fprintf(&probe, "if [ \"$out\" != %s ]; then printf 'bad in %s: %%s\\n' \"$out\"; exit 1; fi\n", git.ShellQuote(w), shell)
			}
			fmt.Fprintf(&probe, "echo %s-ok\nelse echo %s-absent\nfi\n", shell, shell)
		}
		out := srv.RunShell(t, probe.String())
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			line = strings.TrimSpace(line)
			switch {
			case strings.HasSuffix(line, "-ok"):
				t.Logf("%s reads the quoted words verbatim", strings.TrimSuffix(line, "-ok"))
			case strings.HasSuffix(line, "-absent"):
				t.Logf("%s is not installed on this host, skipped", strings.TrimSuffix(line, "-absent"))
			default:
				t.Fatalf("unexpected probe output line %q (full: %q)", line, out)
			}
		}
	})
}
