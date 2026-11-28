package git

import (
	"fmt"
	"path"
	"path/filepath"
)

// InitNew creates the folder named name inside the executor's directory and
// initializes a Git repository there whose initial branch is branch, on this
// Mac or on the server of an SSH executor. The parent folder is created with
// the folders above it first, so it may not exist yet. A file or folder
// already named name is never initialized into — that is what Create
// Repository Here does — and never touched: the error names the collision.
// An empty branch means main. The answer is the working tree root the
// repository itself reports, which for a server path written with ~/ is the
// absolute home path the server answers.
func InitNew(ex Executor, name, branch string) (string, error) {
	if err := CheckCloneName(name); err != nil {
		return "", err
	}
	if branch == "" {
		branch = "main"
	}
	// The branch is validated before any folder exists, so a refusal leaves
	// nothing behind.
	if err := validateInitBranch(branch); err != nil {
		return "", err
	}
	if err := EnsureDir(ex); err != nil {
		return "", err
	}
	if pathExists(ex, name) {
		return "", fmt.Errorf("a file or folder named %q already exists", name)
	}
	child := childExecutor(ex, name)
	if child == nil {
		return "", fmt.Errorf("git init: this executor cannot create a folder")
	}
	if err := EnsureDir(child); err != nil {
		return "", err
	}
	repo := New(child)
	if err := repo.Init(branch); err != nil {
		return "", err
	}
	return repo.TopLevel()
}

// childExecutor is the executor of the folder named name inside the
// executor's own directory: a copy of it with only the directory moved,
// which keeps the git program, the login, the keys and every other setting
// of the parent. nil for an executor this package cannot descend into.
func childExecutor(ex Executor, name string) Executor {
	switch e := ex.(type) {
	case *LocalExecutor:
		child := *e
		child.Dir = filepath.Join(e.Dir, name)
		return &child
	case *SSHExecutor:
		child := *e
		child.Dir = path.Join(e.Dir, name)
		return &child
	}
	return nil
}
