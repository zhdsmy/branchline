package app

// Sidebar reference sorting: the order menus of the Branches, Remotes and
// Tags section headers, the pure ordering they select, and the helpers the
// sidebar rows consume. The choices are app-wide display settings, saved
// through the store; sorting itself is a pure transformation of a copy of
// the snapshot's slices, which stay in the order git reported.

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// The stored values of the reference sort orders. The zero value of a
// stored setting is alphabetical, so it has no name of its own.
const (
	refSortAlphaMasterFolders    = "alpha-master-folders"
	refSortBackward              = "backward"
	refSortBackwardMasterFolders = "backward-master-folders"
	refSortAlphaFolders          = "alpha-folders"
	refSortBackwardFolders       = "backward-folders"
)

// refSortChoice is one order a section header menu offers: the value the
// store keeps, and the label the menu shows.
type refSortChoice struct {
	value string
	label string
}

// localBranchSortChoices are the orders of the Sort Branches menu of the
// Branches section, in the order the menu shows them.
var localBranchSortChoices = []refSortChoice{
	{"", "Alphabetically"},
	{refSortAlphaMasterFolders, "Alphabetically, master and folders on top"},
	{refSortBackward, "Alphabetically backward"},
	{refSortBackwardMasterFolders, "Alphabetically backward, master and folders on top"},
}

// remoteBranchSortChoices are the orders of the Sort Branches menu of the
// Remotes section, in the order the menu shows them.
var remoteBranchSortChoices = []refSortChoice{
	{"", "Alphabetically"},
	{refSortAlphaFolders, "Alphabetically, folders on top"},
	{refSortBackwardFolders, "Alphabetically backward, folders on top"},
}

// tagSortChoices are the orders of the Sort Tags menu of the Tags section,
// in the order the menu shows them.
var tagSortChoices = []refSortChoice{
	{"", "Alphabetically"},
	{refSortAlphaFolders, "Alphabetically, folders on top"},
	{refSortBackward, "Alphabetically backward"},
	{refSortBackwardFolders, "Alphabetically backward, folders on top"},
}

// normalizeRefSort maps a stored value that is not one of the choices — a
// leftover of an older version, or a hand-edited store — onto alphabetical,
// without rewriting the stored value itself.
func normalizeRefSort(order string, choices []refSortChoice) string {
	for _, c := range choices {
		if order == c.value {
			return order
		}
	}
	return ""
}

// refSortSpec is what a stored value means for the comparison: backward
// reverses the direction of the name comparison, master groups the branches
// named exactly main or master ahead of everything else, and folders groups
// names holding a slash ahead of the ones without. Groups never reverse:
// only the names inside them follow the backward direction.
type refSortSpec struct {
	backward bool
	master   bool
	folders  bool
}

func refSortSpecOf(order string) refSortSpec {
	switch order {
	case refSortBackward:
		return refSortSpec{backward: true}
	case refSortAlphaMasterFolders:
		return refSortSpec{master: true, folders: true}
	case refSortBackwardMasterFolders:
		return refSortSpec{backward: true, master: true, folders: true}
	case refSortAlphaFolders:
		return refSortSpec{folders: true}
	case refSortBackwardFolders:
		return refSortSpec{backward: true, folders: true}
	default:
		return refSortSpec{}
	}
}

// compareRefNames orders two names for sortRefNames: first by the groups
// the spec asks for, then by name ignoring case, falling back to a
// case-sensitive comparison so names differing only in case keep a
// deterministic order.
func compareRefNames(a, b string, spec refSortSpec) int {
	if spec.master {
		am, bm := a == "main" || a == "master", b == "main" || b == "master"
		if am != bm {
			if am {
				return -1
			}
			return 1
		}
	}
	if spec.folders {
		af, bf := strings.Contains(a, "/"), strings.Contains(b, "/")
		if af != bf {
			if af {
				return -1
			}
			return 1
		}
	}
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la == lb {
		la, lb = a, b
	}
	cmp := strings.Compare(la, lb)
	if spec.backward {
		return -cmp
	}
	return cmp
}

// sortRefNames returns names in the order the stored value asks for. The
// slice passed in is never changed: every caller receives a fresh slice.
func sortRefNames(names []string, order string) []string {
	out := slices.Clone(names)
	spec := refSortSpecOf(order)
	slices.SortStableFunc(out, func(a, b string) int {
		return compareRefNames(a, b, spec)
	})
	return out
}

// sortBranches returns a copy of branches ordered by the stored value,
// keyed on the short branch name. The snapshot's own slice is never sorted:
// the worker and the other views read it in the order git reported.
func sortBranches(branches []git.Branch, order string) []git.Branch {
	out := slices.Clone(branches)
	spec := refSortSpecOf(order)
	slices.SortStableFunc(out, func(x, y git.Branch) int {
		return compareRefNames(x.Name, y.Name, spec)
	})
	return out
}

// remoteBranchSortName is the name a remote-tracking branch sorts by: the
// short name without its remote prefix, so origin/release/x orders by
// release/x.
func remoteBranchSortName(rb git.Branch) string {
	return strings.TrimPrefix(rb.Name, rb.Remote+"/")
}

// sortRemoteBranches returns a copy of branches ordered by the stored
// value, keyed on the branch name without the remote prefix. The snapshot's
// own slice is never sorted.
func sortRemoteBranches(branches []git.Branch, order string) []git.Branch {
	out := slices.Clone(branches)
	spec := refSortSpecOf(order)
	slices.SortStableFunc(out, func(x, y git.Branch) int {
		return compareRefNames(remoteBranchSortName(x), remoteBranchSortName(y), spec)
	})
	return out
}

// sortTags returns a copy of tags ordered by the stored value. The
// snapshot's own slice is never sorted.
func sortTags(tags []git.Tag, order string) []git.Tag {
	out := slices.Clone(tags)
	spec := refSortSpecOf(order)
	slices.SortStableFunc(out, func(x, y git.Tag) int {
		return compareRefNames(x.Name, y.Name, spec)
	})
	return out
}

// localBranchSort is the order the Branches section currently shows, a
// value not offered by the menu normalized onto alphabetical. The store is
// nil in some test fixtures; that reads as alphabetical.
func (a *app) localBranchSort() string {
	if a.store == nil {
		return ""
	}
	return normalizeRefSort(a.store.Settings().LocalBranchSort, localBranchSortChoices)
}

// remoteBranchSort is the order the Remotes section currently shows,
// normalized the same way.
func (a *app) remoteBranchSort() string {
	if a.store == nil {
		return ""
	}
	return normalizeRefSort(a.store.Settings().RemoteBranchSort, remoteBranchSortChoices)
}

// tagSort is the order the Tags section currently shows, normalized the
// same way.
func (a *app) tagSort() string {
	if a.store == nil {
		return ""
	}
	return normalizeRefSort(a.store.Settings().TagSort, tagSortChoices)
}

// sortedLocalBranches is the Branches section's copy of the snapshot's
// branches, in the order the settings ask for.
func (a *app) sortedLocalBranches(branches []git.Branch) []git.Branch {
	return sortBranches(branches, a.localBranchSort())
}

// sortedRemoteBranches is the Remotes section's copy of the snapshot's
// remote-tracking branches, in the order the settings ask for. Sorting the
// whole list once keeps every remote's subsequence sorted: filtering it for
// one remote preserves relative order.
func (a *app) sortedRemoteBranches(branches []git.Branch) []git.Branch {
	return sortRemoteBranches(branches, a.remoteBranchSort())
}

// sortedTags is the Tags section's copy of the snapshot's tags, in the
// order the settings ask for.
func (a *app) sortedTags(tags []git.Tag) []git.Tag {
	return sortTags(tags, a.tagSort())
}

// saveLocalBranchSort persists the order of the sidebar Branches section.
// The choice changes nothing but the display, so it is accepted while a
// write is in flight. The store rolls its own failed write back; a failure
// leaves the order unchanged and surfaces as a toast.
func (a *app) saveLocalBranchSort(order string) error {
	cur := a.store.Settings()
	cur.LocalBranchSort = order
	if err := a.store.SetSettings(cur); err != nil {
		a.toast = "Cannot save settings: " + err.Error()
		return err
	}
	return nil
}

// saveRemoteBranchSort persists the order of the sidebar Remotes section.
func (a *app) saveRemoteBranchSort(order string) error {
	cur := a.store.Settings()
	cur.RemoteBranchSort = order
	if err := a.store.SetSettings(cur); err != nil {
		a.toast = "Cannot save settings: " + err.Error()
		return err
	}
	return nil
}

// saveTagSort persists the order of the sidebar Tags section.
func (a *app) saveTagSort(order string) error {
	cur := a.store.Settings()
	cur.TagSort = order
	if err := a.store.SetSettings(cur); err != nil {
		a.toast = "Cannot save settings: " + err.Error()
		return err
	}
	return nil
}

// sortRefMenuItems adds the sort order block of a section header menu,
// behind a separator: a disabled heading, then one checkable item per
// order. The current order carries the check mark. Sorting only changes
// the display, so the items stay enabled while a write is in flight; they
// disable only without a store to save the choice into.
func (a *app) sortRefMenuItems(m *ui.Menu, heading string, choices []refSortChoice, current string, save func(string) error) {
	m.Separator()
	m.Item(heading).Disabled(true)
	disabled := a.store == nil
	for _, choice := range choices {
		if m.Item(choice.label).Checked(current == choice.value).Disabled(disabled).Chosen() {
			_ = save(choice.value)
		}
	}
}

// branchesSortMenuItems is the Sort Branches block of the Branches section
// header menu.
func (a *app) branchesSortMenuItems(m *ui.Menu) {
	a.sortRefMenuItems(m, "Sort Branches:", localBranchSortChoices, a.localBranchSort(), a.saveLocalBranchSort)
}

// remotesSortMenuItems is the Sort Branches block of the Remotes section
// header menu.
func (a *app) remotesSortMenuItems(m *ui.Menu) {
	a.sortRefMenuItems(m, "Sort Branches:", remoteBranchSortChoices, a.remoteBranchSort(), a.saveRemoteBranchSort)
}

// tagsSortMenuItems is the Sort Tags block of the Tags section header menu.
func (a *app) tagsSortMenuItems(m *ui.Menu) {
	a.sortRefMenuItems(m, "Sort Tags:", tagSortChoices, a.tagSort(), a.saveTagSort)
}
