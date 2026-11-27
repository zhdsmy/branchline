package git

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scanProcessesScript prints one line per process of the server: its id,
// the id of its process group, and its command line with the NULs as
// spaces. The process group is the fifth field of /proc/pid/stat, the
// third once everything up to the closing parenthesis is gone.
const scanProcessesScript = `for p in /proc/[0-9]*; do
  stat=$(cat "$p/stat" 2>/dev/null) || continue
  cmd=$(tr '\0' ' ' < "$p/cmdline" 2>/dev/null) || continue
  [ -n "$cmd" ] || continue
  pg=$(printf '%s\n' "$stat" | awk '{sub(/.*\) /,"",$0); print $3}')
  printf '%s %s %s\n' "${p#/proc/}" "$pg" "$cmd"
done`

// scanProcesses is the id, group and command line of every process on the
// server.
func scanProcesses(t *testing.T, ex *SSHExecutor) [][3]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := ex.RunShell(ctx, scanProcessesScript)
	if err != nil {
		t.Fatalf("scanning the server's processes: %v", err)
	}
	var entries [][3]string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			entries = append(entries, [3]string{fields[0], fields[1], strings.Join(fields[2:], " ")})
		}
	}
	return entries
}

// findLinuxGroupLeader is the process group of the session leader whose
// command line holds part, or 0: the shell sshd starts for a session leads
// its own group, so the leader is the process whose id is its group.
func findLinuxGroupLeader(entries [][3]string, part string) int {
	for _, e := range entries {
		if e[0] == e[1] && strings.Contains(e[2], part) {
			pg, err := strconv.Atoi(e[1])
			if err == nil {
				return pg
			}
		}
	}
	return 0
}

// linuxGroupAlive reports whether any process still belongs to the group.
func linuxGroupAlive(entries [][3]string, pg int) bool {
	want := strconv.Itoa(pg)
	for _, e := range entries {
		if e[1] == want {
			return true
		}
	}
	return false
}

// TestConfiguredLinuxCancelClone cancels a clone on the server whose
// upload-pack never answers: the command returns at once, the process
// group the server named is gone, and the folder git had started is gone
// with it. The clone is driven through the executor exactly as the app
// runs it, with the stuck part played by a upload-pack that sleeps.
func TestConfiguredLinuxCancelClone(t *testing.T) {
	target := resolveConfiguredLinuxSSHTarget(t)
	host, root := target.hostAlias, target.rootRepo

	conn := &SSHExecutor{Host: host, Dir: root, NoMultiplex: true}
	sshOut := func(t *testing.T, command string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := conn.RunShell(ctx, command)
		if err != nil {
			t.Fatalf("remote command %q failed: %v", command, err)
		}
		return string(out)
	}

	scratch := strings.TrimSpace(sshOut(t, "mktemp -d "+ShellQuote(root+"/cancel.XXXXXX")))
	if !strings.HasPrefix(scratch, root+"/cancel.") {
		t.Fatalf("unexpected scratch directory %q", scratch)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := conn.RunShell(ctx, "rm -rf -- "+ShellQuote(scratch)); err != nil {
			t.Errorf("cleanup %s: %v", scratch, err)
		}
	})

	// A source repository, and a upload-pack that answers nothing, the
	// way a stuck network leaves the real one.
	hang := scratch + "/hang-upload-pack.sh"
	setup := "set -eu\n" +
		"git init -q -b main " + ShellQuote(scratch+"/src") + "\n" +
		"cd " + ShellQuote(scratch+"/src") + "\n" +
		"git config user.name 'Branchline Test'\n" +
		"git config user.email test@example.invalid\n" +
		"printf 'cancel me\\n' > a.txt\n" +
		"git add a.txt\n" +
		"git commit -q -m init\n" +
		"printf '%s' " + ShellQuote("#!/bin/sh\nsleep 300 &\nwait\n") + " > " + ShellQuote(hang) + "\n" +
		"chmod 755 " + ShellQuote(hang) + "\n"
	sshOut(t, setup)

	parent := scratch + "/parent"
	sshOut(t, "mkdir -p "+ShellQuote(parent))

	ex := &SSHExecutor{Host: host, Dir: parent, NoMultiplex: true}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := ex.Run(ctx, nil, "clone", "-u", hang, "file://"+scratch+"/src", "stuckcl")
		done <- err
	}()

	// The clone is stuck once the session leader is up and the sleeper is
	// in its group.
	token := scratch[strings.LastIndex(scratch, "/")+1:]
	var pg int
	deadline := time.Now().Add(10 * time.Second)
	for {
		entries := scanProcesses(t, conn)
		pg = findLinuxGroupLeader(entries, token)
		if pg > 0 && linuxGroupAlive(entries, pg) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no session leader with %q in its command line: %q", token, entries)
		}
		time.Sleep(100 * time.Millisecond)
	}

	cancel()
	cancelAt := time.Now()
	assert := cancelAt.Add(5 * time.Second)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the clone of a stuck source succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the clone did not return within five seconds of the cancellation")
	}
	t.Logf("cancel elapsed: %s", time.Since(cancelAt))

	for {
		if !linuxGroupAlive(scanProcesses(t, conn), pg) {
			break
		}
		if time.Now().After(assert) {
			t.Fatalf("process group %d still alive past the deadline", pg)
		}
		time.Sleep(100 * time.Millisecond)
	}

	out := sshOut(t, "if [ -e "+ShellQuote(parent+"/stuckcl")+" ]; then echo present; else echo gone; fi")
	if strings.TrimSpace(out) != "gone" {
		t.Fatalf("the canceled clone left its folder on the server")
	}
}
