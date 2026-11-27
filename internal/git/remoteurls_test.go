package git

import "testing"

func TestRemoteURLsUsesGitURLRewriting(t *testing.T) {
	dir := initRepo(t)
	gitRun(t, dir, "config", "url.https://github.com/example/.insteadOf", "gh:")
	gitRun(t, dir, "remote", "add", "origin", "gh:project.git")
	gitRun(t, dir, "remote", "add", "mirror", "ssh://git@mirror/project")
	urls, err := New(&LocalExecutor{Dir: dir}).RemoteURLs()
	must(t, err)
	if urls["origin"] != "https://github.com/example/project.git" || urls["mirror"] != "ssh://git@mirror/project" {
		t.Fatalf("urls %v", urls)
	}
}
