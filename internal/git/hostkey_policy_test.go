package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// AskPass grants access to secrets, never host-key trust. Check the resolved
// OpenSSH policy against an explicitly permissive user config, without logging
// in or reading the user's real configuration.
func TestSSHHostKeyPolicy(t *testing.T) {
	for _, ask := range []bool{false, true} {
		name := "Passive"
		if ask {
			name = "AskPass"
		}
		t.Run(name, func(t *testing.T) {
			cfg := filepath.Join(t.TempDir(), "config")
			must(t, os.WriteFile(cfg, []byte("Host fixture-alias\n HostName example.invalid\n User config-user\n Port 2207\n StrictHostKeyChecking no\n ProxyJump fixture-jump\n UserKnownHostsFile /tmp/fixture-known-hosts\n"), 0600))
			identity := filepath.Join(filepath.Dir(cfg), "key with space")
			must(t, os.WriteFile(identity, nil, 0600))
			s := &SSHExecutor{Host: "fixture-alias", Dir: "/repo", Identity: identity}
			batch := "yes"
			if ask {
				s.AskPass = &AskPass{}
				batch = "no"
			}
			args := append([]string{"-G", "-F", cfg}, s.SSHArgs("true")...)
			out, err := exec.Command("/usr/bin/ssh", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("resolve isolated SSH config: %v %s", err, out)
			}
			values := map[string]string{}
			for _, line := range strings.Split(string(out), "\n") {
				k, v, ok := strings.Cut(line, " ")
				if ok {
					values[k] = strings.TrimSpace(v)
				}
			}
			for k, want := range map[string]string{"stricthostkeychecking": "true", "batchmode": batch, "hostname": "example.invalid", "user": "config-user", "port": "2207", "proxyjump": "fixture-jump", "identityfile": identity, "identitiesonly": "yes", "controlpersist": "600"} {
				got := values[k]
				// OpenSSH spells Boolean true/yes differently across versions.
				if k == "batchmode" || k == "identitiesonly" {
					got = strings.ReplaceAll(got, "true", "yes")
					got = strings.ReplaceAll(got, "false", "no")
				}
				if got != want {
					t.Errorf("resolved %s=%q, want %q", k, got, want)
				}
			}
		})
	}
}
