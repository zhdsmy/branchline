package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"

	"github.com/zhdsmy/branchline/internal/store"
)

// envDataDir names the environment variable that redirects the whole Branchline
// user data directory (repositories.json, window-state.json, and every other
// file mygo places under PathUserData) to an isolated location. Smoke and
// manual tests set it so they never touch the user's real data directory.
const envDataDir = "BRANCHLINE_DATA_DIR"

// prepareDataDirOverride inspects BRANCHLINE_DATA_DIR via lookupEnv.
// When unset, it returns ("", false, nil).
// When set, it validates that the trimmed value is a non-empty absolute path,
// creates the directory if missing, verifies write access with a temporary
// probe file, and returns (cleanDir, true, nil).
func prepareDataDirOverride(lookupEnv func(string) (string, bool)) (dir string, overridden bool, err error) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	raw, set := lookupEnv(envDataDir)
	if !set {
		return "", false, nil
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false, fmt.Errorf("%s is set but empty: an isolated data directory must be a non-empty absolute path", envDataDir)
	}
	if !filepath.IsAbs(trimmed) {
		return "", false, fmt.Errorf("%s must be an absolute path, got %q", envDataDir, trimmed)
	}
	clean := filepath.Clean(trimmed)
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return "", false, fmt.Errorf("%s: cannot create data directory %s: %w", envDataDir, clean, err)
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", false, fmt.Errorf("%s: cannot stat data directory %s: %w", envDataDir, clean, err)
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("%s: %s is not a directory", envDataDir, clean)
	}
	// Permission bits alone do not prove writability (ACLs, read-only mounts,
	// root), so probe with a real file and make sure none of it is left behind.
	probe, err := os.CreateTemp(clean, ".branchline-probe-*")
	if err != nil {
		return "", false, fmt.Errorf("%s: data directory %s is not writable: %w", envDataDir, clean, err)
	}
	probePath := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(probePath)
	if closeErr != nil {
		return "", false, fmt.Errorf("%s: write probe in %s failed: %w", envDataDir, clean, closeErr)
	}
	if removeErr != nil {
		return "", false, fmt.Errorf("%s: cannot remove write probe in %s: %w", envDataDir, clean, removeErr)
	}
	return clean, true, nil
}

// resolveUserDataDir returns the effective user data directory.
// When BRANCHLINE_DATA_DIR is set, defaultDir is never called.
// When unset, defaultDir is called and its result is returned unchanged.
func resolveUserDataDir(lookupEnv func(string) (string, bool), defaultDir func() (string, error)) (dir string, overridden bool, err error) {
	dir, overridden, err = prepareDataDirOverride(lookupEnv)
	if err != nil || overridden {
		return dir, overridden, err
	}
	dir, err = defaultDir()
	if err != nil {
		return "", false, err
	}
	return dir, false, nil
}

// configureDataDir validates BRANCHLINE_DATA_DIR and applies it through setPath
// (in production: mygo.App.SetPath) when set. When unset or invalid, setPath
// is not called.
func configureDataDir(lookupEnv func(string) (string, bool), setPath func(mygo.PathName, string)) (dir string, overridden bool, err error) {
	dir, overridden, err = prepareDataDirOverride(lookupEnv)
	if err != nil || !overridden {
		return dir, overridden, err
	}
	setPath(mygo.PathUserData, dir)
	return dir, true, nil
}

// openUserStoreWith resolves the user data directory and opens
// repositories.json inside it.
func openUserStoreWith(lookupEnv func(string) (string, bool), defaultDir func() (string, error)) (*store.Store, error) {
	dir, _, err := resolveUserDataDir(lookupEnv, defaultDir)
	if err != nil {
		return nil, err
	}
	return store.Open(filepath.Join(dir, "repositories.json"))
}

// errDataDirSingleInstance formats the startup error when BRANCHLINE_DATA_DIR
// is set while another Branchline instance already holds the single-instance lock.
func errDataDirSingleInstance(dir string) error {
	return fmt.Errorf("%s is set to %s, but another Branchline instance is already running: quit the running Branchline instance first so the isolated data directory is actually used", envDataDir, dir)
}
