package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo"

	"github.com/zhdsmy/branchline/internal/store"
)

// unsetLookupEnv returns a lookupEnv in which BRANCHLINE_DATA_DIR is unset.
func unsetLookupEnv(string) (string, bool) { return "", false }

// fixedLookupEnv returns a lookupEnv in which BRANCHLINE_DATA_DIR is set to value.
func fixedLookupEnv(value string) func(string) (string, bool) {
	return func(string) (string, bool) { return value, true }
}

// spyDefaultDir records whether it was called and returns dir.
func spyDefaultDir(dir string) (func() (string, error), *bool) {
	called := new(bool)
	return func() (string, error) {
		*called = true
		return dir, nil
	}, called
}

// assertEmptyDir fails the test if dir cannot be read or contains any entries.
func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("%s is not empty: %v", dir, names)
	}
}

// makeReadOnlyDir chmods dir to 0o500 for the duration of the test and restores
// 0o700 in cleanup. It skips only when running as root (euid 0), because root
// bypasses POSIX directory permission bits.
func makeReadOnlyDir(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses 0o500 directory permissions")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod %s read-only: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Errorf("restore %s permissions: %v", dir, err)
		}
	})
}

// TestDataDirResolve checks resolveUserDataDir and prepareDataDirOverride for
// the full input matrix: unset, valid existing, valid auto-created, empty or
// whitespace, relative paths, paths that cannot be created, and directories
// that exist but are not writable. Invalid inputs must return a descriptive
// error without ever calling the default directory resolver, and valid inputs
// must leave no probe files behind.
func TestDataDirResolve(t *testing.T) {
	fakeProd := filepath.Join(t.TempDir(), "prod")

	t.Run("unset uses default directory unchanged", func(t *testing.T) {
		defaultDir, called := spyDefaultDir(fakeProd)
		dir, overridden, err := resolveUserDataDir(unsetLookupEnv, defaultDir)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if overridden {
			t.Fatal("overridden = true, want false")
		}
		if !*called {
			t.Fatal("defaultDir was not called for unset BRANCHLINE_DATA_DIR")
		}
		if dir != fakeProd {
			t.Fatalf("dir = %q, want %q", dir, fakeProd)
		}
	})

	t.Run("default error is propagated when unset", func(t *testing.T) {
		want := errors.New("no default directory")
		_, overridden, err := resolveUserDataDir(unsetLookupEnv, func() (string, error) {
			return "", want
		})
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
		if overridden {
			t.Fatal("overridden = true, want false")
		}
	})

	validCases := []struct {
		name  string
		setup func(t *testing.T) (raw, wantDir string)
	}{
		{
			name: "existing directory",
			setup: func(t *testing.T) (string, string) {
				dir := filepath.Join(t.TempDir(), "appdata")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				return dir, dir
			},
		},
		{
			name: "nested path is auto created",
			setup: func(t *testing.T) (string, string) {
				dir := filepath.Join(t.TempDir(), "nested", "a", "b")
				return dir, dir
			},
		},
		{
			name: "surrounding whitespace and unclean segments are normalized",
			setup: func(t *testing.T) (string, string) {
				dir := filepath.Join(t.TempDir(), "trimmed")
				return "  " + dir + string(filepath.Separator) + "." + string(filepath.Separator) + "  ", dir
			},
		},
	}
	for _, tc := range validCases {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			raw, wantDir := tc.setup(t)
			defaultDir, called := spyDefaultDir(fakeProd)
			dir, overridden, err := resolveUserDataDir(fixedLookupEnv(raw), defaultDir)
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if !overridden {
				t.Fatal("overridden = false, want true")
			}
			if dir != wantDir {
				t.Fatalf("dir = %q, want %q", dir, wantDir)
			}
			if *called {
				t.Fatal("defaultDir must not be called when BRANCHLINE_DATA_DIR is valid")
			}
			info, statErr := os.Stat(wantDir)
			if statErr != nil || !info.IsDir() {
				t.Fatalf("target directory missing or not a dir: stat err = %v", statErr)
			}
			// No temporary write-probe file may remain behind.
			assertEmptyDir(t, wantDir)
		})
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	invalidCases := []struct {
		name    string
		setup   func(t *testing.T) string
		wantErr string
		stray   string
	}{
		{
			name:    "empty string",
			setup:   func(*testing.T) string { return "" },
			wantErr: "empty",
		},
		{
			name:    "whitespace only",
			setup:   func(*testing.T) string { return "   \t\n" },
			wantErr: "empty",
		},
		{
			name:    "relative bare path",
			setup:   func(*testing.T) string { return "fp154rel/data" },
			wantErr: "absolute",
			stray:   filepath.Join(cwd, "fp154rel"),
		},
		{
			name:    "relative dot path",
			setup:   func(*testing.T) string { return "./fp154smoke" },
			wantErr: "absolute",
			stray:   filepath.Join(cwd, "fp154smoke"),
		},
		{
			name:    "relative parent path",
			setup:   func(*testing.T) string { return "../fp154smoke" },
			wantErr: "absolute",
			stray:   filepath.Join(filepath.Dir(cwd), "fp154smoke"),
		},
		{
			name:    "tilde path",
			setup:   func(*testing.T) string { return "~/fp154smoke" },
			wantErr: "absolute",
		},
		{
			name: "parent segment is a regular file",
			setup: func(t *testing.T) string {
				blocker := filepath.Join(t.TempDir(), "file-blocker")
				if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
					t.Fatalf("write blocker: %v", err)
				}
				return filepath.Join(blocker, "sub")
			},
			wantErr: "cannot create",
		},
		{
			name: "target itself is a regular file",
			setup: func(t *testing.T) string {
				target := filepath.Join(t.TempDir(), "regular-file")
				if err := os.WriteFile(target, []byte("not a directory"), 0o644); err != nil {
					t.Fatalf("write target: %v", err)
				}
				return target
			},
			wantErr: "cannot create",
		},
		{
			name: "cannot create below read-only parent",
			setup: func(t *testing.T) string {
				parent := filepath.Join(t.TempDir(), "readonly-parent")
				if err := os.MkdirAll(parent, 0o755); err != nil {
					t.Fatalf("mkdir parent: %v", err)
				}
				makeReadOnlyDir(t, parent)
				return filepath.Join(parent, "child")
			},
			wantErr: "cannot create",
		},
		{
			name: "existing directory is not writable",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "sealed")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				makeReadOnlyDir(t, dir)
				return dir
			},
			wantErr: "not writable",
		},
	}
	for _, tc := range invalidCases {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			value := tc.setup(t)
			defaultDir, called := spyDefaultDir(fakeProd)
			dir, overridden, err := resolveUserDataDir(fixedLookupEnv(value), defaultDir)
			if err == nil {
				t.Fatalf("value %q: err = nil, want error", value)
			}
			if !strings.Contains(err.Error(), envDataDir) {
				t.Fatalf("value %q: error %q does not name %s", value, err, envDataDir)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("value %q: error %q does not mention %q", value, err, tc.wantErr)
			}
			if dir != "" || overridden {
				t.Fatalf("value %q: got (%q, %v), want (\"\", false)", value, dir, overridden)
			}
			if *called {
				t.Fatalf("value %q: defaultDir must not be called for invalid input", value)
			}
			if tc.stray != "" {
				if _, statErr := os.Stat(tc.stray); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("relative input %q created stray path %s", value, tc.stray)
				}
			}
		})
	}

	// prepareDataDirOverride shares the validation core; spot-check its contract
	// directly for unset, valid, and invalid values.
	t.Run("prepareDataDirOverride contract", func(t *testing.T) {
		if dir, overridden, err := prepareDataDirOverride(unsetLookupEnv); dir != "" || overridden || err != nil {
			t.Fatalf("unset: got (%q, %v, %v), want (\"\", false, nil)", dir, overridden, err)
		}
		valid := t.TempDir()
		dir, overridden, err := prepareDataDirOverride(fixedLookupEnv(valid))
		if err != nil || !overridden || dir != valid {
			t.Fatalf("valid: got (%q, %v, %v), want (%q, true, nil)", dir, overridden, err, valid)
		}
		if _, overridden, err := prepareDataDirOverride(fixedLookupEnv("")); err == nil || overridden {
			t.Fatalf("empty: got overridden %v err %v, want error and false", overridden, err)
		}
	})
}

// TestDataDirIsolatesStoreFromDefaultDir proves end to end that with
// BRANCHLINE_DATA_DIR set, repositories added through the app persist into
// repositories.json inside the override directory while the fake production
// directory stays completely empty and the default directory resolver is never
// called. It also covers the invalid-override and unset fallbacks.
func TestDataDirIsolatesStoreFromDefaultDir(t *testing.T) {
	repo := newGitRepo(t, "isolated-repo")
	repo.CommitFile("hello.txt", "hello\n", "initial commit")

	setup := func(t *testing.T) (fakeProdDir, overrideDir string) {
		t.Helper()
		root := t.TempDir()
		fakeProdDir = filepath.Join(root, "prod")
		if err := os.MkdirAll(fakeProdDir, 0o755); err != nil {
			t.Fatalf("mkdir fake prod dir: %v", err)
		}
		overrideDir = filepath.Join(root, "smoke", "appdata")
		return fakeProdDir, overrideDir
	}

	t.Run("override persists into isolated directory only", func(t *testing.T) {
		fakeProdDir, overrideDir := setup(t)
		t.Setenv(envDataDir, overrideDir)

		defaultDir, called := spyDefaultDir(fakeProdDir)
		s, err := openUserStoreWith(os.LookupEnv, defaultDir)
		if err != nil {
			t.Fatalf("openUserStoreWith: %v", err)
		}
		if *called {
			t.Fatal("default directory resolver must not be called when BRANCHLINE_DATA_DIR is set")
		}
		wantPath := filepath.Join(overrideDir, "repositories.json")
		if s.Path() != wantPath {
			t.Fatalf("store path = %q, want %q", s.Path(), wantPath)
		}

		a := newApp(s)
		a.spawnFn = func(fn func()) {} // avoid background git loads; persistence is synchronous
		saved, err := s.Add(repo.StoreRepo())
		if err != nil {
			t.Fatalf("add repo: %v", err)
		}
		a.openRepo(saved)

		if _, err := os.Stat(wantPath); err != nil {
			t.Fatalf("repositories.json missing in override dir: %v", err)
		}

		reopened, err := store.Open(wantPath)
		if err != nil {
			t.Fatalf("reopen store: %v", err)
		}
		meta, ok := reopened.Get(saved.ID)
		if !ok {
			t.Fatalf("repo %q not found in reopened store", saved.ID)
		}
		if meta.Name != "isolated-repo" {
			t.Fatalf("repo name = %q, want %q", meta.Name, "isolated-repo")
		}
		activeID, set := reopened.ActiveTab()
		if !set || activeID != saved.ID {
			t.Fatalf("active tab = (%q, %v), want (%q, true)", activeID, set, saved.ID)
		}

		assertEmptyDir(t, fakeProdDir)
	})

	t.Run("invalid override errors without touching default dir", func(t *testing.T) {
		fakeProdDir, _ := setup(t)
		t.Setenv(envDataDir, "relative/bad")

		defaultDir, called := spyDefaultDir(fakeProdDir)
		s, err := openUserStoreWith(os.LookupEnv, defaultDir)
		if err == nil {
			t.Fatal("err = nil, want error for a relative override path")
		}
		if s != nil {
			t.Fatalf("store = %v, want nil on error", s)
		}
		if !strings.Contains(err.Error(), envDataDir) {
			t.Fatalf("error %q does not name %s", err, envDataDir)
		}
		if *called {
			t.Fatal("default directory resolver must not be called for invalid override")
		}
		assertEmptyDir(t, fakeProdDir)
	})

	t.Run("unset falls back to default directory", func(t *testing.T) {
		fakeProdDir, overrideDir := setup(t)
		defaultDir, called := spyDefaultDir(fakeProdDir)
		s, err := openUserStoreWith(unsetLookupEnv, defaultDir)
		if err != nil {
			t.Fatalf("openUserStoreWith: %v", err)
		}
		if !*called {
			t.Fatal("default directory resolver was not called for unset BRANCHLINE_DATA_DIR")
		}
		wantPath := filepath.Join(fakeProdDir, "repositories.json")
		if s.Path() != wantPath {
			t.Fatalf("store path = %q, want %q", s.Path(), wantPath)
		}
		if _, err := s.Add(repo.StoreRepo()); err != nil {
			t.Fatalf("add repo: %v", err)
		}
		if _, err := os.Stat(wantPath); err != nil {
			t.Fatalf("repositories.json missing in fake default dir: %v", err)
		}
		if _, err := os.Stat(overrideDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("override dir %s exists although BRANCHLINE_DATA_DIR was unset", overrideDir)
		}
	})
}

// TestDataDirConfigureMyGoPath proves that configureDataDir applies a valid
// BRANCHLINE_DATA_DIR through mygo.App.SetPath so mygo itself (including
// window-state.json) resolves into the isolated directory, and that unset or
// invalid values never reach SetPath. The process-level override is restored
// in cleanup.
func TestDataDirConfigureMyGoPath(t *testing.T) {
	t.Run("unset does not call setPath", func(t *testing.T) {
		calls := 0
		dir, overridden, err := configureDataDir(unsetLookupEnv, func(mygo.PathName, string) { calls++ })
		if err != nil || overridden || dir != "" {
			t.Fatalf("got (%q, %v, %v), want (\"\", false, nil)", dir, overridden, err)
		}
		if calls != 0 {
			t.Fatalf("setPath called %d times, want 0", calls)
		}
	})

	t.Run("invalid does not call setPath and returns error", func(t *testing.T) {
		calls := 0
		_, overridden, err := configureDataDir(fixedLookupEnv("relative/bad"), func(mygo.PathName, string) { calls++ })
		if err == nil || overridden {
			t.Fatalf("got overridden %v err %v, want error and false", overridden, err)
		}
		if calls != 0 {
			t.Fatalf("setPath called %d times, want 0", calls)
		}
	})

	t.Run("valid applies override through mygo.App.SetPath", func(t *testing.T) {
		overrideDir := filepath.Join(t.TempDir(), "smoke", "appdata")
		t.Setenv(envDataDir, overrideDir)
		// Restore the process-level user data path override before any other
		// test or the real application path resolution can observe it.
		t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })

		dir, overridden, err := configureDataDir(os.LookupEnv, mygo.App.SetPath)
		if err != nil || !overridden || dir != overrideDir {
			t.Fatalf("got (%q, %v, %v), want (%q, true, nil)", dir, overridden, err, overrideDir)
		}
		got, err := mygo.App.Path(mygo.PathUserData)
		if err != nil {
			t.Fatalf("mygo.App.Path: %v", err)
		}
		if got != overrideDir {
			t.Fatalf("mygo.App.Path(PathUserData) = %q, want %q", got, overrideDir)
		}
		assertEmptyDir(t, overrideDir)
	})

	t.Run("nil lookupEnv falls back to the process environment", func(t *testing.T) {
		overrideDir := filepath.Join(t.TempDir(), "appdata")
		t.Setenv(envDataDir, overrideDir)
		calls := 0
		dir, overridden, err := configureDataDir(nil, func(name mygo.PathName, p string) {
			calls++
			if name != mygo.PathUserData {
				t.Errorf("setPath name = %q, want %q", name, mygo.PathUserData)
			}
			if p != overrideDir {
				t.Errorf("setPath path = %q, want %q", p, overrideDir)
			}
		})
		if err != nil || !overridden || dir != overrideDir {
			t.Fatalf("got (%q, %v, %v), want (%q, true, nil)", dir, overridden, err, overrideDir)
		}
		if calls != 1 {
			t.Fatalf("setPath called %d times, want 1", calls)
		}
	})
}

// TestDataDirSingleInstanceError checks that the single-instance conflict
// error names the configured data directory and tells the user to quit the
// running Branchline instance first.
func TestDataDirSingleInstanceError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "appdata")
	err := errDataDirSingleInstance(dir)
	if err == nil {
		t.Fatal("err = nil, want error")
	}
	msg := err.Error()
	for _, want := range []string{dir, "Branchline", "quit", envDataDir} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not contain %q", msg, want)
		}
	}
}
