package app

// Sidebar reference folders. References whose names hold slashes — feature/ui/menu,
// v1/rc1, the part of a remote-tracking branch after its remote — group into
// folder rows, one row per name segment: feature/ui/menu shows as the folders
// feature and ui with the branch menu inside. A folder with a single child is
// never merged into one row. Folder rows stand where their first member sorts
// in the section's order — the order the Sort Branches and Sort Tags menus
// already give the flat list, folder-grouping choices included — and the
// references inside a folder keep exactly that order, so choosing any of the
// sort orders reorders the tree exactly as it reordered the flat list.
//
// Folders start expanded; collapsing one remembers it in the repository's
// SidebarClosed settings list by the folder's stable ID — branches:feature/ui,
// remotes:origin:feature, tags:v1 — and the collapse of the four sections
// remembers in SidebarSections. Both are per repository: opening or switching
// repositories reads them back, so a folder collapsed in one repository shows
// expanded under the same name in another until it is collapsed there too.
//
// Collapsing a folder — or one of its ancestor folders — deselects the
// references inside it, because the sidebar's selection only ever holds
// rows the user can see; collapsing a whole section hides its rows without
// touching the selection, exactly as it always did. Range clicks select
// along the rows as shown, so a hidden row can never join a range either.

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

// sidebarFolderIndent is how many points one folder level indents the rows
// under it.
const sidebarFolderIndent = 12

// sidebarFolderChild is one entry of a folder: a subfolder or a reference
// leaf. The entries of a folder are in insertion order — the order the first
// member of each sorts into the flat sorted list — so the flattened rows keep
// exactly the order the sort settings chose.
type sidebarFolderChild[T any] struct {
	folder *sidebarFolderNode[T]
	leaf   int // index into the sorted slice; valid when folder is nil
}

// sidebarFolderNode is one folder of the grouping tree: its segment — the
// text its row shows — the slash-joined path from the tree's root that names
// it for its accessible label and stable ID, and its children in insertion
// order.
type sidebarFolderNode[T any] struct {
	seg      string
	path     string
	children []sidebarFolderChild[T]
	// count is the number of references in the subtree, direct leaves and
	// the leaves of every nested folder.
	count int
}

// sidebarFolderItem is one row of the flattened tree: a folder row, or a
// reference leaf with its index into the sorted slice it came from. Depth is
// the nesting level of the row — 0 at the section's top level — and drives
// the row's indentation.
type sidebarFolderItem[T any] struct {
	folder bool
	leaf   int
	depth  int
	seg    string
	path   string
	count  int
}

// sidebarFolderChildOf inserts one reference path segment by segment into
// node, creating folders as the path descends and appending every new entry
// at the end of its parent, which is exactly the first-appearance order of
// the sorted walk.
func sidebarFolderInsert[T any](node *sidebarFolderNode[T], segments []string, leaf int) {
	if len(segments) == 1 {
		node.children = append(node.children, sidebarFolderChild[T]{leaf: leaf})
		return
	}
	seg := segments[0]
	for i := range node.children {
		if child := node.children[i].folder; child != nil && child.seg == seg {
			sidebarFolderInsert(child, segments[1:], leaf)
			return
		}
	}
	child := &sidebarFolderNode[T]{seg: seg}
	if node.path == "" {
		child.path = seg
	} else {
		child.path = node.path + "/" + seg
	}
	node.children = append(node.children, sidebarFolderChild[T]{folder: child})
	sidebarFolderInsert(child, segments[1:], leaf)
}

// sidebarFolderCount fills every folder's count bottom-up: the references
// directly inside it plus the counts of its subfolders. The count of the
// tree's root is never read.
func sidebarFolderCount[T any](node *sidebarFolderNode[T]) int {
	count := 0
	for i := range node.children {
		if child := node.children[i].folder; child != nil {
			count += sidebarFolderCount(child)
		} else {
			count++
		}
	}
	node.count = count
	return count
}

// sidebarFolderFlatten appends the rows of node to out: the node's own row,
// then — only while the folder stays open — the rows of its children, leaf
// references reporting through emit. Items is the flattened form the row
// table builds from; the visible indexes collect the leaves of open folders
// in display order, the list range selections and multi-select counts read.
func sidebarFolderFlatten[T any](node *sidebarFolderNode[T], depth int, prefix string, closed map[string]bool,
	items *[]sidebarFolderItem[T], visible *[]int, emit func(leaf, depth int)) {
	*items = append(*items, sidebarFolderItem[T]{
		folder: true, depth: depth, seg: node.seg, path: node.path, count: node.count,
	})
	if closed[prefix+node.path] {
		return
	}
	for _, child := range node.children {
		if child.folder != nil {
			sidebarFolderFlatten(child.folder, depth+1, prefix, closed, items, visible, emit)
		} else {
			emit(child.leaf, depth+1)
			*visible = append(*visible, child.leaf)
		}
	}
}

// sidebarGroupRefs flattens the sorted references into folder and leaf row
// items. len(items) is the number of references — git.Branch or git.Tag —
// and itemName is the name the tree groups: the short name for branches and
// tags, the name without its remote for remote-tracking branches. The items
// are the rows in display order, folder rows standing where their first
// member sorts, collapsed folders cutting their subtree out of the leaf rows
// but never out of the row list — the folder row itself always shows. The
// second return value is the indexes of the visible references, in display
// order: hidden references of collapsed folders are not in it.
func sidebarGroupRefs[T any](items []T, itemName func(T) string, prefix string, closed map[string]bool) ([]sidebarFolderItem[T], []int) {
	root := &sidebarFolderNode[T]{}
	for i, item := range items {
		segments := strings.Split(itemName(item), "/")
		sidebarFolderInsert(root, segments, i)
	}
	sidebarFolderCount(root)
	var rows []sidebarFolderItem[T]
	var visible []int
	emit := func(leaf, depth int) {
		rows = append(rows, sidebarFolderItem[T]{leaf: leaf, depth: depth})
	}
	for _, child := range root.children {
		if child.folder != nil {
			sidebarFolderFlatten(child.folder, 0, prefix, closed, &rows, &visible, emit)
		} else {
			emit(child.leaf, 0)
			visible = append(visible, child.leaf)
		}
	}
	return rows, visible
}

// sidebarLastSegment is the text a reference row shows: the last slash
// segment of the name. The full name stays the row's accessible label, and
// the rows it shortened carry it as their tooltip, so the shortened text
// never costs the row its identity.
func sidebarLastSegment(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}

// sidebarRefTooltip is the tooltip of a reference row: the full name when
// the row's text is the shortened last segment, empty when the row already
// shows the name whole — a tooltip repeating the visible text is noise.
func sidebarRefTooltip(name string) string {
	if sidebarLastSegment(name) != name {
		return name
	}
	return ""
}

// sidebarFolderIDOf reads the folder path out of a stable folder ID and
// reports the ID's prefix scope: "branches", "tags", or — with the remote
// name split out as well — "remotes". Reference names never hold a colon, so
// the scope prefix and, for remotes, the remote name split unambiguously.
func sidebarFolderIDOf(id string) (scope, remote, path string) {
	scope, rest, ok := strings.Cut(id, ":")
	if !ok {
		return "", "", ""
	}
	if scope != "remotes" {
		return scope, "", rest
	}
	remote, path, ok = strings.Cut(rest, ":")
	if !ok {
		return "", "", ""
	}
	return scope, remote, path
}

// fallbackFolder is the original geometric folder glyph used on systems
// without SF Symbols, stroked like the other fallback icons.
var fallbackFolder = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 4.5v7a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1H8L6.8 3.5H3a1 1 0 0 0-1 1z"/></svg>`)

// sidebarFolderRow is one folder row of the sidebar: a chevron like the
// section headers', a folder icon, the folder's segment as its only text,
// and the count of the references inside it, subfolders included. The
// accessible name is the folder's full path — remotes included for the
// folders of a remote — so it never collides with a same-named folder of
// another scope or remote, and the expanded state reads to assistive
// technology exactly as the section headers' does. Clicking toggles the
// folder and remembers the choice in the repository's settings.
func (a *app) sidebarFolderRow(c *ui.Context, entry sidebarRowEntry) {
	p := a.pal
	open := entry.folderOpen
	row := ui.ButtonBase(c.Key("foldbtn-"+entry.folderID)).
		Height(24).
		Padding(0, 10, 0, entry.padLeft).
		Gap(5).
		Radius(6).
		AlignItems(ui.Center).
		Justify(ui.Start).
		Focusable().
		Label(entry.folderPath).
		Tooltip(entry.folderPath).
		Expanded(open).
		FocusRing(false)
	if row.Hovered() {
		row.Background(p.hover)
	}
	iconName, fb := "chevron.down", fallbackDown
	if !open {
		iconName, fb = "chevron.right", fallbackFwd
	}
	row.Children(func() {
		symbolIcon(c, iconName, fb, 9, p.faint)
		symbolIcon(c, "folder", fallbackFolder, 12, p.faint)
		ui.Text(c, entry.folderSeg).FontSize(12).TextColor(p.muted).SingleLine().Shrink(1).MinWidth(0)
		ui.Spacer(c)
		ui.Text(c, fmt.Sprint(entry.count)).FontSize(11).TextColor(p.faint).SingleLine()
	})
	if row.Clicked() {
		a.toggleSidebarFolder(entry.folderID)
	}
}

// toggleSidebarFolder flips one folder's collapse, remembers the choice in
// the repository's SidebarClosed settings, and drops the references inside
// the folder from the selection when it collapses: a hidden row is never
// selected. The view changes either way — a failed write surfaces as the
// settings toast while the folder stays as the user chose for this opening —
// and only a toggle writes, never a read.
func (a *app) toggleSidebarFolder(id string) {
	st := &a.refsView
	if st.sidebarClosed == nil {
		st.sidebarClosed = map[string]bool{}
	}
	collapsed := !st.sidebarClosed[id]
	if collapsed {
		st.sidebarClosed[id] = true
		a.pruneSidebarFolderPicks(id)
	} else {
		delete(st.sidebarClosed, id)
	}
	st.sidebarClosedRev++
	a.saveSidebarClosed()
}

// pruneSidebarFolderPicks drops the selection keys of the references inside
// the collapsing folder id — the folder's own rows and every nested
// folder's, which share its path prefix — and the anchor when it names one
// of them, so a range click after the collapse starts fresh instead of
// reaching hidden rows. The pick version steps only when something actually
// left the selection.
func (a *app) pruneSidebarFolderPicks(id string) {
	scope, remote, path := sidebarFolderIDOf(id)
	if path == "" {
		return
	}
	base := ""
	switch scope {
	case "branches", "tags":
		base = path + "/"
	case "remotes":
		base = "refs/remotes/" + remote + "/" + path + "/"
	}
	st := &a.refsView
	changed := false
	if strings.HasPrefix(st.pickAnchor.name, base) {
		st.pickAnchor = sidebarPickKey{}
		changed = true
	}
	for key := range st.pick {
		if strings.HasPrefix(key.name, base) {
			delete(st.pick, key)
			changed = true
		}
	}
	if changed {
		st.pickVersion++
	}
}

// sidebarSectionKind identifies one collapsible sidebar section.
type sidebarSectionKind int

const (
	sidebarSectionBranches sidebarSectionKind = iota
	sidebarSectionRemotes
	sidebarSectionTags
	sidebarSectionStashes
)

// sidebarSectionToggled runs after the user flipped a section's collapse:
// the new state saves into the repository's SidebarSections. The selection
// is untouched — collapsing a section hides rows without changing what is
// selected, exactly as it always did; only collapsing a folder or one of
// its ancestors deselects the references inside it.
func (a *app) sidebarSectionToggled(section sidebarSectionKind, collapsed bool) {
	a.saveSidebarSectionCollapsed(section, collapsed)
}

// sidebarCollapsedFromSettings reads one section's collapse out of the
// repository settings: a saved pointer holds the expanded state, so a
// missing one keeps the original default — expanded.
func sidebarCollapsedFromSettings(saved *bool) bool {
	return saved != nil && !*saved
}

// loadSidebarRefSettings reads the repository's sidebar settings into the
// view state once per repository opening: the collapsed folders of
// SidebarClosed and the section collapse of SidebarSections, stashes
// included. It runs on the UI thread from the sidebar row table's input
// gathering, after the repository-generation reset, so the state of one
// repository never leaks into another. A store without the repository — or
// no store at all — reads as the defaults, everything expanded.
func (a *app) loadSidebarRefSettings() {
	st := &a.refsView
	if st.sidebarClosedLoaded {
		return
	}
	st.sidebarClosedLoaded = true
	st.sidebarClosed = map[string]bool{}
	if a.store == nil || !a.session.HasRepo() {
		return
	}
	id := a.session.Meta().ID
	if id == "" {
		return
	}
	meta, ok := a.store.Get(id)
	if !ok {
		return
	}
	for _, folder := range meta.Local.SidebarClosed {
		st.sidebarClosed[folder] = true
	}
	sections := meta.Local.SidebarSections
	a.branchesCollapsed = sidebarCollapsedFromSettings(sections.Branches)
	a.remotesCollapsed = sidebarCollapsedFromSettings(sections.Remotes)
	a.tagsCollapsed = sidebarCollapsedFromSettings(sections.Tags)
	a.stashUI().collapsed = sidebarCollapsedFromSettings(sections.Stashes)
}

// saveSidebarClosed writes the collapsed folder IDs into the repository's
// SidebarClosed settings, preserving every other setting. A failed write
// surfaces as the settings toast while the view keeps the user's choice for
// this opening, exactly as the other sidebar settings do.
func (a *app) saveSidebarClosed() {
	if a.store == nil || !a.session.HasRepo() {
		return
	}
	id := a.session.Meta().ID
	if id == "" {
		return
	}
	meta, ok := a.store.Get(id)
	if !ok {
		return
	}
	closed := make([]string, 0, len(a.refsView.sidebarClosed))
	for folder := range a.refsView.sidebarClosed {
		closed = append(closed, folder)
	}
	slices.Sort(closed)
	local := meta.Local
	local.SidebarClosed = closed
	if err := a.store.UpdateLocal(id, local); err != nil {
		a.toast = "Cannot save settings: " + err.Error()
	}
}

// saveSidebarSectionCollapsed writes one section's collapse into the
// repository's SidebarSections as its expanded state, preserving the other
// sections and settings. A failed write surfaces as the settings toast while
// the section stays as the user chose for this opening.
func (a *app) saveSidebarSectionCollapsed(section sidebarSectionKind, collapsed bool) {
	if a.store == nil || !a.session.HasRepo() {
		return
	}
	id := a.session.Meta().ID
	if id == "" {
		return
	}
	meta, ok := a.store.Get(id)
	if !ok {
		return
	}
	expanded := !collapsed
	local := meta.Local
	switch section {
	case sidebarSectionBranches:
		local.SidebarSections.Branches = &expanded
	case sidebarSectionRemotes:
		local.SidebarSections.Remotes = &expanded
	case sidebarSectionTags:
		local.SidebarSections.Tags = &expanded
	case sidebarSectionStashes:
		local.SidebarSections.Stashes = &expanded
	}
	if err := a.store.UpdateLocal(id, local); err != nil {
		a.toast = "Cannot save settings: " + err.Error()
	}
}
