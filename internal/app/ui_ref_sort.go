package app

// Sidebar reference sorting: the order menus of the Branches, Remotes and
// Tags section headers, the pure ordering they select, and the helpers the
// sidebar rows consume. The choices are app-wide display settings, saved
// through the store; sorting itself is a pure transformation of a copy of
// the snapshot's slices, which stay in the order git reported.

import (
	"slices"
	"strings"
	"unsafe"

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

// refSortKey is what one name compares by, computed once per name before
// sorting so the comparison itself never allocates: the name in lowercase,
// the original name for the case-sensitive tie-break, whether it is exactly
// main or master, and whether it holds a slash.
type refSortKey struct {
	lower  string
	name   string
	master bool
	folder bool
}

func refSortKeyOf(name string) refSortKey {
	return refSortKey{
		lower:  strings.ToLower(name),
		name:   name,
		master: name == "main" || name == "master",
		folder: strings.Contains(name, "/"),
	}
}

// compareRefSortKeys orders two precomputed keys: first by the groups the
// spec asks for, then by the lowercase name, falling back to the original
// name so names differing only in case keep a deterministic order. It
// allocates nothing.
func compareRefSortKeys(a, b refSortKey, spec refSortSpec) int {
	if spec.master && a.master != b.master {
		if a.master {
			return -1
		}
		return 1
	}
	if spec.folders && a.folder != b.folder {
		if a.folder {
			return -1
		}
		return 1
	}
	la, lb := a.lower, b.lower
	if la == lb {
		la, lb = a.name, b.name
	}
	cmp := strings.Compare(la, lb)
	if spec.backward {
		return -cmp
	}
	return cmp
}

// keyedRefItem decorates one item with its comparison key for the sort.
type keyedRefItem[T any] struct {
	key  refSortKey
	item T
}

// sortKeyedRef returns items in the order the stored value asks for, keyed
// on the name keyOf gives each item. The key is computed once per item; the
// comparison reads only the keys. The slice passed in is never changed:
// every caller receives a fresh slice.
func sortKeyedRef[T any](items []T, order string, keyOf func(T) string) []T {
	spec := refSortSpecOf(order)
	decorated := make([]keyedRefItem[T], len(items))
	for i, item := range items {
		decorated[i] = keyedRefItem[T]{key: refSortKeyOf(keyOf(item)), item: item}
	}
	slices.SortStableFunc(decorated, func(x, y keyedRefItem[T]) int {
		return compareRefSortKeys(x.key, y.key, spec)
	})
	out := make([]T, len(items))
	for i, d := range decorated {
		out[i] = d.item
	}
	return out
}

// sortRefNames returns names in the order the stored value asks for. The
// slice passed in is never changed: every caller receives a fresh slice.
func sortRefNames(names []string, order string) []string {
	return sortKeyedRef(names, order, func(name string) string { return name })
}

// sortBranches returns a copy of branches ordered by the stored value,
// keyed on the short branch name. The snapshot's own slice is never sorted:
// the worker and the other views read it in the order git reported.
func sortBranches(branches []git.Branch, order string) []git.Branch {
	return sortKeyedRef(branches, order, func(br git.Branch) string { return br.Name })
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
	return sortKeyedRef(branches, order, remoteBranchSortName)
}

// sortTags returns a copy of tags ordered by the stored value. The
// snapshot's own slice is never sorted.
func sortTags(tags []git.Tag, order string) []git.Tag {
	return sortKeyedRef(tags, order, func(tag git.Tag) string { return tag.Name })
}

// sameBackingSlice reports whether x and y share one backing array and
// length — the identity of the immutable snapshot slice, not just equal
// contents. Retaining the source in the cache keeps its backing array
// alive, so a new snapshot can never reuse the address of the cached one.
func sameBackingSlice[T any](x, y []T) bool {
	return len(x) == len(y) && unsafe.SliceData(x) == unsafe.SliceData(y)
}

// refSortCache is the one-entry cache of a sorted reference list: the
// snapshot slice and the order it was sorted from, and the sorted copy.
// A hit returns the very slice the first sort produced, so repeated frames
// of an unchanged snapshot and unchanged settings sort nothing.
type refSortCache[T any] struct {
	src   []T
	order string
	out   []T
	valid bool
}

// lookup returns the cached sorted copy when it was made from this very
// snapshot slice and this order.
func (c *refSortCache[T]) lookup(src []T, order string) ([]T, bool) {
	if c.valid && c.order == order && sameBackingSlice(c.src, src) {
		return c.out, true
	}
	return nil, false
}

// store replaces the cache entry with the copy sorted from src and order.
func (c *refSortCache[T]) store(src []T, order string, out []T) {
	c.src = src
	c.order = order
	c.out = out
	c.valid = true
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
// branches, in the order the settings ask for. The sorted copy is cached on
// the snapshot slice and the order: repeated frames of an unchanged
// snapshot and unchanged settings return the cached copy without sorting.
func (a *app) sortedLocalBranches(branches []git.Branch) []git.Branch {
	order := a.localBranchSort()
	cache := &a.refsView.localSortCache
	if out, ok := cache.lookup(branches, order); ok {
		return out
	}
	out := sortBranches(branches, order)
	cache.store(branches, order, out)
	return out
}

// sortedRemoteBranches is the Remotes section's copy of the snapshot's
// remote-tracking branches, in the order the settings ask for. Sorting the
// whole list once keeps every remote's subsequence sorted: filtering it for
// one remote preserves relative order, so every remote shares the one
// cached copy rather than sorting the list again per remote.
func (a *app) sortedRemoteBranches(branches []git.Branch) []git.Branch {
	order := a.remoteBranchSort()
	cache := &a.refsView.remoteSortCache
	if out, ok := cache.lookup(branches, order); ok {
		return out
	}
	out := sortRemoteBranches(branches, order)
	cache.store(branches, order, out)
	return out
}

// sortedTags is the Tags section's copy of the snapshot's tags, in the
// order the settings ask for, cached like the branches'.
func (a *app) sortedTags(tags []git.Tag) []git.Tag {
	order := a.tagSort()
	cache := &a.refsView.tagSortCache
	if out, ok := cache.lookup(tags, order); ok {
		return out
	}
	out := sortTags(tags, order)
	cache.store(tags, order, out)
	return out
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
