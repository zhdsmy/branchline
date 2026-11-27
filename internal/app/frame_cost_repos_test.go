//go:build !race

package app

import (
	"testing"
)

// switcherCostHarness builds an app with n seeded repositories, every
// fourth of them an SSH one so the taller two-line rows are measured too,
// and opens one real repository in the session so measured frames prove no
// git commands run. The seeded paths never exist and are never opened.
func switcherCostHarness(t *testing.T, scale string, openPopup bool) *testHarness {
	t.Helper()
	n := 5
	if scale == "large" {
		n = 200
	}
	repos := switcherSeedRepos(n)
	path := prepareSeededStore(t, repos)
	h := newSeededTestApp(t, path)

	real := frameCostLinearRepo(t, "switcher-cost-"+scale, 2)
	h.app.session.Open(real.StoreRepo())
	h.drain()
	if openPopup {
		openSwitcherPopup(t, h)
	}
	h.drain()
	return h
}

// With the popup closed the switcher only compares the store's revision:
// drawing frames over it must cost the same with 200 repositories saved as
// with 5, and run no git command.
func TestFrameCostRepoSwitcherClosed(t *testing.T) {
	small, large := frameCostPair(t, "RepoSwitcherClosed", func(t *testing.T, scale string) *testHarness {
		return switcherCostHarness(t, scale, false)
	})
	frameCostAssert(t, "RepoSwitcherClosed", small, large)
}

// With the popup open the virtual list builds only the rows in view, so
// the same flat cost holds with 200 repositories as with 5.
func TestFrameCostRepoSwitcherOpen(t *testing.T) {
	small, large := frameCostPair(t, "RepoSwitcherOpen", func(t *testing.T, scale string) *testHarness {
		return switcherCostHarness(t, scale, true)
	})
	frameCostAssert(t, "RepoSwitcherOpen", small, large)
}
