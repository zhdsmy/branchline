package git

import (
	"fmt"
	"path"
	"strings"
)

// SubmoduleFolder is where a submodule cloned from url goes inside the
// repository: the folder typed, or the repository's name when none is.
// A folder that ends in a slash holds the clone, so it gets that name too.
func SubmoduleFolder(url, folder string) string {
	folder = strings.Trim(strings.TrimSpace(folder), "\\")
	folder = strings.TrimPrefix(folder, "./")
	name := repositoryName(url)
	switch {
	case folder == "":
		return name
	case strings.HasSuffix(folder, "/"):
		return strings.TrimPrefix(folder, "/") + name
	}
	return strings.TrimPrefix(folder, "/")
}

// repositoryName is the last part of a repository URL without .git, such
// as lib for https://host/team/lib.git or git@host:team/lib.git.
func repositoryName(url string) string {
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	url = strings.TrimSuffix(url, ".git")
	if i := strings.LastIndexAny(url, "/:\\"); i >= 0 {
		url = url[i+1:]
	}
	return path.Clean(url)
}

// SubmoduleAddAt adds the repository at url as a submodule in folder.
// With recursive the submodules the new one has are fetched too.
func (r *Repo) SubmoduleAddAt(url, folder string, recursive bool) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return fmt.Errorf("a submodule needs a URL")
	}
	folder = SubmoduleFolder(url, folder)
	if folder == "" || folder == "." {
		return fmt.Errorf("a submodule needs a folder")
	}
	if err := r.SubmoduleAdd(url, folder); err != nil {
		return err
	}
	if recursive {
		// No protocol.file.allow override here: the nested submodules'
		// URLs come from the added repository's .gitmodules, written by
		// its author. Git refuses the file transport for them since
		// 2.38.1 (CVE-2022-39253), and this step keeps that refusal.
		_, err := r.run(slow, "submodule", "update", "--init", "--recursive", "--", folder)
		return err
	}
	return nil
}
