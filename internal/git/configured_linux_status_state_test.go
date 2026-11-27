package git

import (
	"context"
	"path"
	"strings"
	"testing"
	"time"
)

// The state read prunes every path with an everything-excluding pathspec,
// and the git that decides whether the operation lines still print is the
// one on the server, not the one on this Mac. These cases drive a merge, a
// rebase and a quiet repository on the configured Linux host and read the
// state back through SSH, so each host's own git version is what answers.
func TestConfiguredLinuxStatusState(t *testing.T) {
	target := resolveConfiguredLinuxSSHTarget(t)
	t.Logf("remote git version: %q (parsed %+v)", target.gitVersionRaw, target.gitCaps.Version)

	sshOut := func(t *testing.T, script string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := target.directEx.RunShell(ctx, "/bin/sh -c "+ShellQuote("set -eu\n"+script))
		if err != nil {
			t.Fatalf("remote script failed: %v\nscript:\n%s", err, script)
		}
		return string(out)
	}

	open := func(dir string) *Repo {
		return New(&SSHExecutor{Host: target.hostAlias, Dir: dir, NoMultiplex: true})
	}

	// stateCase runs a setup script whose last output line is the case
	// directory it created under the disposable root, and registers its
	// removal.
	stateCase := func(t *testing.T, name, script string) string {
		t.Helper()
		out := sshOut(t, script)
		lines := strings.Split(strings.TrimSpace(out), "\n")
		dir := strings.TrimSpace(lines[len(lines)-1])
		if path.Dir(dir) != target.rootRepo || !strings.HasPrefix(path.Base(dir), "state-"+name+".") {
			t.Fatalf("unexpected case directory %q under %q", dir, target.rootRepo)
		}
		t.Cleanup(func() {
			cleanCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			if _, err := target.directEx.RunShell(cleanCtx, "rm -rf -- "+ShellQuote(dir)); err != nil {
				t.Errorf("cleanup %s: %v", dir, err)
			}
		})
		return dir
	}

	baseSetup := `
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
git init -q
git symbolic-ref HEAD refs/heads/main
git config user.name 'State Test'
git config user.email state@example.invalid
git config commit.gpgsign false
printf 'one\n' > a.txt
git add a.txt
git commit -q -m base
`

	t.Run("merge conflict", func(t *testing.T) {
		dir := stateCase(t, "merge", `
dir=$(mktemp -d `+ShellQuote(target.rootRepo+"/state-merge.XXXXXX")+`)
cd "$dir"
`+baseSetup+`
git checkout -qb feature
printf 'theirs\n' > a.txt
git commit -qam feature
git checkout -q main
printf 'ours\n' > a.txt
git commit -qam main
git merge feature >/dev/null 2>&1 || true
printf '%s\n' "$dir"
`)
		st, err := open(dir).Status()
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if st.State != "merge" || !st.Conflicted() {
			t.Fatalf("State = %q conflicted %v, want merge with conflicts (%+v)", st.State, st.Conflicted(), st)
		}
	})

	t.Run("rebase conflict", func(t *testing.T) {
		dir := stateCase(t, "rebase", `
dir=$(mktemp -d `+ShellQuote(target.rootRepo+"/state-rebase.XXXXXX")+`)
cd "$dir"
`+baseSetup+`
git checkout -qb feature
printf 'theirs\n' > a.txt
git commit -qam feature
git checkout -q main
printf 'ours\n' > a.txt
git commit -qam main
git checkout -q feature
printf '%s\n' "$dir"
`)
		r := open(dir)
		plan, err := r.RebasePlan("main")
		must(t, err)
		if len(plan) != 1 {
			t.Fatalf("plan %+v, want one step", plan)
		}
		if err := r.RebaseWith("main", plan); err != nil {
			t.Fatalf("rebase did not stop on the conflict: %v", err)
		}
		st, err := r.Status()
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if st.State != "rebase" || st.Stopped != "conflict" || !st.Conflicted() {
			t.Fatalf("State = %q stopped %q conflicted %v, want rebase stopped on conflict (%+v)", st.State, st.Stopped, st.Conflicted(), st)
		}
	})

	t.Run("no operation", func(t *testing.T) {
		dir := stateCase(t, "clean", `
dir=$(mktemp -d `+ShellQuote(target.rootRepo+"/state-clean.XXXXXX")+`)
cd "$dir"
`+baseSetup+`
printf '%s\n' "$dir"
`)
		st, err := open(dir).Status()
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if st.State != "" {
			t.Fatalf("State = %q, want empty (%+v)", st.State, st)
		}
	})
}
