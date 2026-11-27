package sshtest

import (
	"context"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/zhdsmy/branchline/internal/git"
)

const disposableLinuxPrefix = "/tmp/branchline-test-"

// ConfiguredLinux returns a Target bound to the Linux SSH host configured via
// BRANCHLINE_LINUX_SSH_HOST and BRANCHLINE_LINUX_SSH_REPO (typically exported by
// scripts/sshlab/env.sh or scripts/sshlab/test.sh).
//
// When neither variable is set, ConfiguredLinux calls t.Skip by default, or
// t.Fatal when BRANCHLINE_REQUIRE_LINUX=1 is set. When configured, it enforces
// that BRANCHLINE_LINUX_SSH_REPO is under /tmp/branchline-test-* and verifies
// connectivity to a Linux host. All repositories created via Target live in
// disposable subdirectories cleaned up by t.Cleanup without mutating the root
// fixture directory.
func ConfiguredLinux(t testing.TB) *Target {
	t.Helper()
	return configuredLinux(t, true)
}

func configuredLinux(t testing.TB, probe bool) *Target {
	t.Helper()

	host := strings.TrimSpace(os.Getenv(EnvLinuxHost))
	root := strings.TrimSpace(os.Getenv(EnvLinuxRepo))
	requireLinux := isEnvTrue(EnvRequireLinux)

	if host == "" && root == "" {
		if requireLinux {
			t.Fatalf("%s=1: %s and %s must both be set", EnvRequireLinux, EnvLinuxHost, EnvLinuxRepo)
		}
		t.Skipf("%s and %s are not configured", EnvLinuxHost, EnvLinuxRepo)
		return nil
	}
	if host == "" || root == "" {
		t.Fatalf("both %s (%q) and %s (%q) must be set when configured", EnvLinuxHost, host, EnvLinuxRepo, root)
		return nil
	}
	if !strings.HasPrefix(root, disposableLinuxPrefix) ||
		path.Clean(root) != root ||
		root == disposableLinuxPrefix ||
		strings.ContainsAny(root, "\n\r\x00") {
		t.Fatalf("refusing %s outside disposable %s* prefix: %q", EnvLinuxRepo, disposableLinuxPrefix, root)
		return nil
	}

	target := &Target{
		Kind:         KindConfiguredLinux,
		Host:         host,
		KnownHosts:   strings.TrimSpace(os.Getenv("BRANCHLINE_SSHLAB_KNOWN_HOSTS")),
		RootDir:      root,
		sshBin:       findSSHProgram(),
		controlPaths: make(map[string]struct{}),
	}
	target.trackControlPath((&git.SSHExecutor{Host: host, Dir: root}).ControlPath())

	if probe {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		out, err := target.probeExecutor().RunShell(ctx, "uname -s")
		cancel()
		if err != nil {
			t.Fatalf("configured Linux SSH probe to %s failed: %v", host, err)
			return nil
		}
		if got := strings.TrimSpace(string(out)); got != "Linux" {
			t.Fatalf("configured Linux SSH host %s reported uname -s = %q, want Linux", host, got)
			return nil
		}
	}

	t.Cleanup(func() {
		if err := target.close(); err != nil {
			t.Errorf("sshtest configured-linux cleanup: %v", err)
		}
	})
	return target
}
