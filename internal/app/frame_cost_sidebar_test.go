//go:build !race

package app

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// frameCostUpdateRefs feeds the create directives of refs to one git
// update-ref --stdin call in the fixture's repository, so thousands of
// references cost one process.
func frameCostUpdateRefs(t *testing.T, repo *gitRepoFixture, refs string) {
	t.Helper()
	cmd := exec.Command("git", "update-ref", "--stdin")
	cmd.Dir = repo.Path()
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	cmd.Stdin = strings.NewReader(refs)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git update-ref: %v\n%s", err, out)
	}
}

// frameCostSidebarRepo builds a repository whose sidebar shows n local
// branches b00000…, n remote-tracking branches rb00000… of each of the
// remotes remote-a, remote-b and remote-c, n tags t00000…, and n stash
// entries, whichever of them n asks for; zero counts leave the section
// empty. References cost one fast-import commit and one update-ref batch
// each kind, stashes two stash commits and one store per entry, so even
// the 5000-ref fixtures build in seconds. Two local branches beyond main
// always exist, so every drive can select two rows and exercise the
// multi-select counts.
func frameCostSidebarRepo(t *testing.T, name string, branches, remoteBranches, tags, stashes int) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	frameCostStream(t, repo, 1, func(i int) [][2]string {
		return [][2]string{{"f.txt", "content\n"}}
	}, func(i int) string {
		return "initial commit"
	})
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	var refs strings.Builder
	for i := 0; i < branches; i++ {
		fmt.Fprintf(&refs, "create refs/heads/b%05d %s\n", i, head)
	}
	for i := 0; i < tags; i++ {
		fmt.Fprintf(&refs, "create refs/tags/t%05d %s\n", i, head)
	}
	for _, remote := range []string{"remote-a", "remote-b", "remote-c"} {
		if remoteBranches > 0 {
			repo.Git("remote", "add", remote, repo.Path())
		}
		for i := 0; i < remoteBranches; i++ {
			fmt.Fprintf(&refs, "create refs/remotes/%s/rb%05d %s\n", remote, i, head)
		}
	}
	if refs.Len() > 0 {
		frameCostUpdateRefs(t, repo, refs.String())
	}

	if stashes > 0 {
		repo.WriteFile("f.txt", "stash one\n")
		repo.Git("add", "f.txt")
		one := strings.TrimSpace(repo.Git("stash", "create"))
		repo.WriteFile("f.txt", "stash two\n")
		repo.Git("add", "f.txt")
		two := strings.TrimSpace(repo.Git("stash", "create"))
		repo.Git("checkout", "--", "f.txt")
		for i := 0; i < stashes; i++ {
			// Alternating the two commits keeps every reflog entry a real
			// change of the stash ref, which git stash store requires.
			hash := one
			if i%2 == 1 {
				hash = two
			}
			repo.Git("stash", "store", "-m", fmt.Sprintf("stash msg %d", i), hash)
		}
	}
	return repo
}

// frameCostSidebarDrive opens the repository with the alphabetical sort
// orders pinned, selects two local branches — a plain click on the first
// and a Shift-click on the second, so the frames paint a live
// multi-selection and its counts — and then collapses the sections named,
// so the measured kind's rows are the ones in view.
func frameCostSidebarDrive(t *testing.T, repo *gitRepoFixture, collapse ...string) *testHarness {
	t.Helper()
	h := newTestApp(t)
	settings := h.store.Settings()
	settings.LocalBranchSort = ""
	settings.RemoteBranchSort = ""
	settings.TagSort = ""
	if err := h.store.SetSettings(settings); err != nil {
		t.Fatalf("pin alphabetical sort orders: %v", err)
	}
	h.openRepo(repo.StoreRepo())
	h.drain()
	clickBranchRow(t, h, "b00000")
	shiftClickBranchRow(t, h, "b00001")
	for _, section := range collapse {
		mustClick(t, h, section)
		h.ui.Frame()
	}
	return h
}

// The sidebar's branch rows are entries of one virtual list: drawing
// frames with two branches selected must cost no more when the repository
// holds 5000 local branches than when it holds 50, and no frame may run a
// Git command.
func TestFrameCostSidebarBranches(t *testing.T) {
	small, large := frameCostPair(t, "SidebarBranches", func(t *testing.T, scale string) *testHarness {
		n := 50
		if scale == "large" {
			n = 5000
		}
		repo := frameCostSidebarRepo(t, "sidebar-branches-"+scale, n, 0, 0, 0)
		return frameCostSidebarDrive(t, repo)
	})
	frameCostAssert(t, "SidebarBranches", small, large)
}

// The remote-tracking branch rows of three remotes in one list, with the
// Branches section collapsed so they are the rows in view.
func TestFrameCostSidebarRemoteBranches(t *testing.T) {
	small, large := frameCostPair(t, "SidebarRemoteBranches", func(t *testing.T, scale string) *testHarness {
		n := 50
		if scale == "large" {
			n = 5000
		}
		repo := frameCostSidebarRepo(t, "sidebar-remotes-"+scale, 2, n, 0, 0)
		return frameCostSidebarDrive(t, repo, "Branches")
	})
	frameCostAssert(t, "SidebarRemoteBranches", small, large)
}

// The tag rows in the same list, with the sections above collapsed so the
// tags are the rows in view.
func TestFrameCostSidebarTags(t *testing.T) {
	small, large := frameCostPair(t, "SidebarTags", func(t *testing.T, scale string) *testHarness {
		n := 50
		if scale == "large" {
			n = 5000
		}
		repo := frameCostSidebarRepo(t, "sidebar-tags-"+scale, 2, 0, n, 0)
		return frameCostSidebarDrive(t, repo, "Branches", "Remotes")
	})
	frameCostAssert(t, "SidebarTags", small, large)
}

// The stash rows in the same list, with the sections above collapsed so
// the stashes are the rows in view.
func TestFrameCostSidebarStashes(t *testing.T) {
	small, large := frameCostPair(t, "SidebarStashes", func(t *testing.T, scale string) *testHarness {
		n := 5
		if scale == "large" {
			n = 200
		}
		repo := frameCostSidebarRepo(t, "sidebar-stashes-"+scale, 2, 0, 0, n)
		return frameCostSidebarDrive(t, repo, "Branches", "Remotes", "Tags")
	})
	frameCostAssert(t, "SidebarStashes", small, large)
}

// Every reference kind large at once: the sections share one list, so a
// frame must still cost what the rows in view cost, not what the
// repository holds.
func TestFrameCostSidebarAllKinds(t *testing.T) {
	small, large := frameCostPair(t, "SidebarAllKinds", func(t *testing.T, scale string) *testHarness {
		n, s := 50, 5
		if scale == "large" {
			n, s = 5000, 200
		}
		repo := frameCostSidebarRepo(t, "sidebar-all-"+scale, n, n, n, s)
		return frameCostSidebarDrive(t, repo)
	})
	frameCostAssert(t, "SidebarAllKinds", small, large)
}
