package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type stubVersionExecutor struct {
	out   []byte
	err   error
	calls int
	args  [][]string
}

func (s *stubVersionExecutor) Describe() string { return "stub" }

func (s *stubVersionExecutor) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	s.calls++
	s.args = append(s.args, append([]string(nil), args...))
	return s.out, s.err
}

func TestGitVersionParse(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    GitVersion
		wantErr bool
	}{
		{
			name: "standard three part",
			raw:  "git version 2.25.1",
			want: GitVersion{Major: 2, Minor: 25, Patch: 1},
		},
		{
			name: "standard with newline",
			raw:  "git version 2.43.0\n",
			want: GitVersion{Major: 2, Minor: 43, Patch: 0},
		},
		{
			name: "missing patch major minor",
			raw:  "git version 2.25",
			want: GitVersion{Major: 2, Minor: 25, Patch: 0},
		},
		{
			name: "missing patch future major",
			raw:  "git version 3.0",
			want: GitVersion{Major: 3, Minor: 0, Patch: 0},
		},
		{
			name: "apple vendor suffix",
			raw:  "git version 2.39.5 (Apple Git-154)",
			want: GitVersion{Major: 2, Minor: 39, Patch: 5},
		},
		{
			name: "apple vendor suffix without patch",
			raw:  "git version 2.25 (Apple Git-128)",
			want: GitVersion{Major: 2, Minor: 25, Patch: 0},
		},
		{
			name: "ubuntu parenthesized suffix",
			raw:  "git version 2.25.1 (Ubuntu 2.25.1-1ubuntu3.13)",
			want: GitVersion{Major: 2, Minor: 25, Patch: 1},
		},
		{
			name: "ubuntu hyphen suffix",
			raw:  "git version 2.25.1-1ubuntu3.13",
			want: GitVersion{Major: 2, Minor: 25, Patch: 1},
		},
		{
			name: "ubuntu dot suffix",
			raw:  "git version 2.34.1.ubuntu.1",
			want: GitVersion{Major: 2, Minor: 34, Patch: 1},
		},
		{
			name: "ubuntu plus suffix",
			raw:  "git version 2.34.1+ubuntu1",
			want: GitVersion{Major: 2, Minor: 34, Patch: 1},
		},
		{
			name: "windows vendor suffix",
			raw:  "git version 2.43.0.windows.1",
			want: GitVersion{Major: 2, Minor: 43, Patch: 0},
		},
		{
			name: "windows vfs suffix",
			raw:  "git version 2.42.0.vfs.0.3",
			want: GitVersion{Major: 2, Minor: 42, Patch: 0},
		},
		{
			name: "windows suffix without patch",
			raw:  "git version 2.25.windows.1",
			want: GitVersion{Major: 2, Minor: 25, Patch: 0},
		},
		{
			name: "release candidate hyphen",
			raw:  "git version 2.40.0-rc1",
			want: GitVersion{Major: 2, Minor: 40, Patch: 0},
		},
		{
			name: "release candidate dot",
			raw:  "git version 2.40.0.rc1",
			want: GitVersion{Major: 2, Minor: 40, Patch: 0},
		},
		{
			name: "bare version without prefix",
			raw:  "2.38.2 (Apple Git-144)",
			want: GitVersion{Major: 2, Minor: 38, Patch: 2},
		},
		{
			name:    "empty",
			raw:     "",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			raw:     "   \t\n  ",
			wantErr: true,
		},
		{
			name:    "prefix only",
			raw:     "git version",
			wantErr: true,
		},
		{
			name:    "prefix and spaces only",
			raw:     "git version    ",
			wantErr: true,
		},
		{
			name:    "zero major minor",
			raw:     "git version 0.0",
			wantErr: true,
		},
		{
			name:    "zero major minor patch",
			raw:     "git version 0.0.0",
			wantErr: true,
		},
		{
			name:    "zero major with nonzero minor",
			raw:     "git version 0.25.1",
			wantErr: true,
		},
		{
			name:    "major only",
			raw:     "git version 2",
			wantErr: true,
		},
		{
			name:    "trailing dot on major",
			raw:     "git version 2.",
			wantErr: true,
		},
		{
			name:    "trailing dot on minor",
			raw:     "git version 2.25.",
			wantErr: true,
		},
		{
			name:    "leading dot",
			raw:     "git version .2.25.0",
			wantErr: true,
		},
		{
			name:    "double dot",
			raw:     "git version 2..25.0",
			wantErr: true,
		},
		{
			name:    "negative major",
			raw:     "git version -2.25.0",
			wantErr: true,
		},
		{
			name:    "negative minor",
			raw:     "git version 2.-25.0",
			wantErr: true,
		},
		{
			name:    "negative patch",
			raw:     "git version 2.25.-1",
			wantErr: true,
		},
		{
			name:    "non numeric minor",
			raw:     "git version 2.x.0",
			wantErr: true,
		},
		{
			name:    "non numeric patch",
			raw:     "git version 2.25.x",
			wantErr: true,
		},
		{
			name:    "unrelated program output",
			raw:     "OpenSSH_9.0p1, LibreSSL 3.3.6",
			wantErr: true,
		},
		{
			name:    "wrong prefix",
			raw:     "svn, version 1.14.0 (r1876290)",
			wantErr: true,
		},
		{
			name:    "multiline output",
			raw:     "git version 2.25.1\nextra line",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseGitVersion(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseGitVersion(%q) = %+v, nil; want error", tc.raw, got)
				}
				if got != (GitVersion{}) {
					t.Fatalf("ParseGitVersion(%q) returned non-zero version %+v on error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseGitVersion(%q) unexpected error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("ParseGitVersion(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestGitVersionAtLeast(t *testing.T) {
	tests := []struct {
		name  string
		v     GitVersion
		other GitVersion
		want  bool
	}{
		{
			name:  "2.9 is below 2.25 numerically",
			v:     GitVersion{Major: 2, Minor: 9, Patch: 5},
			other: GitVersion{Major: 2, Minor: 25, Patch: 0},
			want:  false,
		},
		{
			name:  "2.25 is at least 2.9 numerically",
			v:     GitVersion{Major: 2, Minor: 25, Patch: 0},
			other: GitVersion{Major: 2, Minor: 9, Patch: 5},
			want:  true,
		},
		{
			name:  "equal versions",
			v:     GitVersion{Major: 2, Minor: 25, Patch: 0},
			other: GitVersion{Major: 2, Minor: 25, Patch: 0},
			want:  true,
		},
		{
			name:  "patch higher",
			v:     GitVersion{Major: 2, Minor: 25, Patch: 1},
			other: GitVersion{Major: 2, Minor: 25, Patch: 0},
			want:  true,
		},
		{
			name:  "patch lower",
			v:     GitVersion{Major: 2, Minor: 25, Patch: 0},
			other: GitVersion{Major: 2, Minor: 25, Patch: 1},
			want:  false,
		},
		{
			name:  "minor lower with large patch",
			v:     GitVersion{Major: 2, Minor: 24, Patch: 99},
			other: GitVersion{Major: 2, Minor: 25, Patch: 0},
			want:  false,
		},
		{
			name:  "future major 3.0 beats 2.99.99",
			v:     GitVersion{Major: 3, Minor: 0, Patch: 0},
			other: GitVersion{Major: 2, Minor: 99, Patch: 99},
			want:  true,
		},
		{
			name:  "older major 1.99.99 is below 2.0.0",
			v:     GitVersion{Major: 1, Minor: 99, Patch: 99},
			other: GitVersion{Major: 2, Minor: 0, Patch: 0},
			want:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.AtLeast(tc.other); got != tc.want {
				t.Fatalf("(%+v).AtLeast(%+v) = %v, want %v", tc.v, tc.other, got, tc.want)
			}
		})
	}
}

func TestGitCapabilitiesThresholds(t *testing.T) {
	tests := []struct {
		raw               string
		wantVersion       GitVersion
		meetsMinimum      bool
		initInitialBranch bool
		stashStaged       bool
		rebaseUpdateRefs  bool
	}{
		{
			raw:         "git version 2.9.0",
			wantVersion: GitVersion{Major: 2, Minor: 9, Patch: 0},
		},
		{
			raw:         "git version 2.24.9 (Apple Git-128)",
			wantVersion: GitVersion{Major: 2, Minor: 24, Patch: 9},
		},
		{
			raw:          "git version 2.25",
			wantVersion:  GitVersion{Major: 2, Minor: 25, Patch: 0},
			meetsMinimum: true,
		},
		{
			raw:          "git version 2.25.0",
			wantVersion:  GitVersion{Major: 2, Minor: 25, Patch: 0},
			meetsMinimum: true,
		},
		{
			raw:          "git version 2.27.9",
			wantVersion:  GitVersion{Major: 2, Minor: 27, Patch: 9},
			meetsMinimum: true,
		},
		{
			raw:               "git version 2.28",
			wantVersion:       GitVersion{Major: 2, Minor: 28, Patch: 0},
			meetsMinimum:      true,
			initInitialBranch: true,
		},
		{
			raw:               "git version 2.28.0",
			wantVersion:       GitVersion{Major: 2, Minor: 28, Patch: 0},
			meetsMinimum:      true,
			initInitialBranch: true,
		},
		{
			raw:               "git version 2.34.9",
			wantVersion:       GitVersion{Major: 2, Minor: 34, Patch: 9},
			meetsMinimum:      true,
			initInitialBranch: true,
		},
		{
			raw:               "git version 2.35",
			wantVersion:       GitVersion{Major: 2, Minor: 35, Patch: 0},
			meetsMinimum:      true,
			initInitialBranch: true,
			stashStaged:       true,
		},
		{
			raw:               "git version 2.35.0",
			wantVersion:       GitVersion{Major: 2, Minor: 35, Patch: 0},
			meetsMinimum:      true,
			initInitialBranch: true,
			stashStaged:       true,
		},
		{
			raw:               "git version 2.37.9",
			wantVersion:       GitVersion{Major: 2, Minor: 37, Patch: 9},
			meetsMinimum:      true,
			initInitialBranch: true,
			stashStaged:       true,
		},
		{
			raw:               "git version 2.38",
			wantVersion:       GitVersion{Major: 2, Minor: 38, Patch: 0},
			meetsMinimum:      true,
			initInitialBranch: true,
			stashStaged:       true,
			rebaseUpdateRefs:  true,
		},
		{
			raw:               "git version 2.38.0.windows.1",
			wantVersion:       GitVersion{Major: 2, Minor: 38, Patch: 0},
			meetsMinimum:      true,
			initInitialBranch: true,
			stashStaged:       true,
			rebaseUpdateRefs:  true,
		},
		{
			raw:               "git version 3.0.0",
			wantVersion:       GitVersion{Major: 3, Minor: 0, Patch: 0},
			meetsMinimum:      true,
			initInitialBranch: true,
			stashStaged:       true,
			rebaseUpdateRefs:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			ex := &stubVersionExecutor{out: []byte(tc.raw + "\n")}
			var reader GitCapabilityReader = New(ex)
			got, err := reader.Capabilities()
			if err != nil {
				t.Fatalf("Capabilities() unexpected error: %v", err)
			}
			want := GitCapabilities{
				Version:           tc.wantVersion,
				RawVersion:        tc.raw,
				MeetsMinimum:      tc.meetsMinimum,
				RebaseUpdateRefs:  tc.rebaseUpdateRefs,
				StashStaged:       tc.stashStaged,
				InitInitialBranch: tc.initInitialBranch,
			}
			if got != want {
				t.Fatalf("Capabilities() = %+v, want %+v", got, want)
			}
			if ex.calls != 1 || len(ex.args) != 1 || len(ex.args[0]) != 1 || ex.args[0][0] != "--version" {
				t.Fatalf("executor calls = %d args = %v, want single [--version]", ex.calls, ex.args)
			}
		})
	}
}

func TestGitCapabilitiesNoGlobalCacheAndErrorPreservation(t *testing.T) {
	t.Run("nil repo or executor fails cleanly", func(t *testing.T) {
		var nilRepo *Repo
		if caps, err := nilRepo.Capabilities(); err == nil || caps != (GitCapabilities{}) {
			t.Fatalf("nil repo Capabilities() = %+v, %v; want zero and error", caps, err)
		}
		if caps, err := (&Repo{}).Capabilities(); err == nil || caps != (GitCapabilities{}) {
			t.Fatalf("nil executor Capabilities() = %+v, %v; want zero and error", caps, err)
		}
	})

	t.Run("transport error is preserved without masking as old or new version", func(t *testing.T) {
		sentinel := &Error{
			Args:     []string{"--version"},
			ExitCode: 255,
			Stderr:   "cannot connect to dev@127.0.0.1: Connection refused",
		}
		ex := &stubVersionExecutor{err: sentinel}
		caps, err := New(ex).Capabilities()
		if err == nil {
			t.Fatal("Capabilities() succeeded on transport error")
		}
		if !errors.Is(err, sentinel) {
			t.Fatalf("errors.Is(%v, sentinel) = false; transport error not preserved", err)
		}
		var ge *Error
		if !errors.As(err, &ge) || ge.ExitCode != 255 {
			t.Fatalf("errors.As(%v) = %+v; want *Error with ExitCode 255", err, ge)
		}
		if caps != (GitCapabilities{}) {
			t.Fatalf("Capabilities() on error returned non-zero %+v", caps)
		}
	})

	t.Run("unparseable version fails and does not cache across calls", func(t *testing.T) {
		ex := &stubVersionExecutor{out: []byte("git version unknown\n")}
		r := New(ex)
		if caps, err := r.Capabilities(); err == nil || caps != (GitCapabilities{}) {
			t.Fatalf("unparseable Capabilities() = %+v, %v; want zero and error", caps, err)
		}
		ex.out = []byte("git version 2.25.1\n")
		caps, err := r.Capabilities()
		if err != nil {
			t.Fatalf("second Capabilities() error: %v", err)
		}
		if !caps.MeetsMinimum || caps.InitInitialBranch || caps.StashStaged || caps.RebaseUpdateRefs {
			t.Fatalf("second Capabilities() = %+v, want 2.25.1 capabilities", caps)
		}
		if ex.calls != 2 {
			t.Fatalf("executor calls = %d, want 2 (no global cache)", ex.calls)
		}
	})
}

func TestGitCapabilitiesLocalRealGit(t *testing.T) {
	dir := testRepo(t)
	log := NewCommandLog()
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true}).WithCommandLog(log)

	beforeConfig, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}

	raw, err := r.Version()
	if err != nil {
		t.Fatalf("Version(): %v", err)
	}
	caps, err := r.Capabilities()
	if err != nil {
		t.Fatalf("Capabilities(): %v", err)
	}
	if caps.RawVersion != raw {
		t.Fatalf("RawVersion = %q, want %q", caps.RawVersion, raw)
	}
	wantVer, err := ParseGitVersion(raw)
	if err != nil {
		t.Fatalf("ParseGitVersion(%q): %v", raw, err)
	}
	if caps.Version != wantVer {
		t.Fatalf("Version = %+v, want %+v", caps.Version, wantVer)
	}
	if !caps.MeetsMinimum {
		t.Fatalf("local Git %q (%+v) reported MeetsMinimum=false", raw, caps.Version)
	}

	afterConfig, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterConfig) != string(beforeConfig) {
		t.Fatalf("Capabilities() mutated .git/config:\nbefore:\n%s\nafter:\n%s", beforeConfig, afterConfig)
	}

	entries, _ := log.Snapshot()
	if len(entries) < 2 || entries[len(entries)-1].Command != "$ git --version" {
		t.Fatalf("command log entries = %+v, want trailing '$ git --version'", entries)
	}

	// Verify LocalExecutor with a real script binary reporting an older Git and a failing exit code.
	fakeGit := filepath.Join(t.TempDir(), "fake-git")
	script := "#!/bin/sh\necho 'git version 2.24.3 (Apple Git-128)'\n"
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldCaps, err := New(&LocalExecutor{Dir: dir, Git: fakeGit, NoGlobalConfig: true}).Capabilities()
	if err != nil {
		t.Fatalf("old fake git Capabilities(): %v", err)
	}
	if oldCaps.MeetsMinimum || oldCaps.InitInitialBranch || oldCaps.StashStaged || oldCaps.RebaseUpdateRefs {
		t.Fatalf("2.24.3 capabilities = %+v, want all false", oldCaps)
	}
	if !strings.Contains(oldCaps.RawVersion, "Apple Git-128") {
		t.Fatalf("RawVersion = %q, want vendor suffix preserved", oldCaps.RawVersion)
	}

	failGit := filepath.Join(t.TempDir(), "fail-git")
	failScript := "#!/bin/sh\necho 'fatal: broken git installation' >&2\nexit 128\n"
	if err := os.WriteFile(failGit, []byte(failScript), 0o755); err != nil {
		t.Fatal(err)
	}
	failCaps, err := New(&LocalExecutor{Dir: dir, Git: failGit, NoGlobalConfig: true}).Capabilities()
	if err == nil {
		t.Fatal("failing git binary succeeded")
	}
	if ExitCode(err) != 128 || failCaps != (GitCapabilities{}) {
		t.Fatalf("failing git binary = %+v, %v (exit %d); want zero caps and exit 128", failCaps, err, ExitCode(err))
	}
}
