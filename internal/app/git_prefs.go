package app

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zhdsmy/branchline/internal/git"
)

// validateGitSettings checks a custom git program and an ENV PATH choice
// before either is saved: the program must be a runnable git, and the choice
// one of the PATH modes the executors know. An empty gitPath is the default
// git, always valid.
func validateGitSettings(gitPath, envPath string) error {
	gitPath = strings.TrimSpace(gitPath)
	if gitPath != "" {
		if _, err := git.CheckBinary(gitPath); err != nil {
			return err
		}
	}
	switch envPath {
	case "", string(git.PathProcess), string(git.PathShell):
	default:
		return fmt.Errorf("unknown ENV PATH choice %q", envPath)
	}
	return nil
}

// saveGitToolchainSettings persists the custom git program, the ENV PATH
// choice and the trace switches. Validation failure or a failed write leaves
// the stored settings exactly as they were.
func (a *app) saveGitToolchainSettings(gitPath, envPath string, trace git.Trace) error {
	if a.store == nil {
		return errors.New("cannot save Git settings: no store")
	}
	if err := validateGitSettings(gitPath, envPath); err != nil {
		return err
	}
	s := a.store.Settings()
	s.GitPath = strings.TrimSpace(gitPath)
	s.EnvPath = envPath
	s.TraceGit, s.TraceCurl, s.TraceSSH, s.TracePack, s.TracePerf = trace.Git, trace.Curl, trace.SSH, trace.Pack, trace.Perf
	s.VerboseGit = false
	return a.store.SetSettings(s)
}

// localGitForSettings builds the repository the global author identity is read
// and written through: any directory works for git config --global, and the
// executor honors the saved custom git program and ENV PATH choice so the
// identity is read and written by the same git the repositories use.
func (a *app) localGitForSettings() *git.Repo {
	var prefs struct {
		GitPath string
		EnvPath string
	}
	if a.store != nil {
		s := a.store.Settings()
		prefs.GitPath = s.GitPath
		prefs.EnvPath = s.EnvPath
	}
	return git.New(&git.LocalExecutor{Dir: os.TempDir(), Git: prefs.GitPath, Path: git.PathMode(prefs.EnvPath)})
}

// saveGlobalIdentity writes the global author identity with the overwrite
// protection of SafeSetGlobalIdentity: readErr is the error of the read next
// and expected come from.
func (a *app) saveGlobalIdentity(next, expected git.Identity, readErr error) error {
	return a.localGitForSettings().SafeSetGlobalIdentity(next, expected, readErr)
}

// gitInstanceChoices lists the git programs the Git settings can offer: the
// saved custom path first when it still runs, then the git of PATH and the
// usual locations.
func gitInstanceChoices(savedGitPath string) []git.Instance {
	return git.FindInstances(savedGitPath, git.LookPath())
}
