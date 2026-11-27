package app

import (
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/repository"
)

// fallbackSearchIcon is the geometric magnifying-glass fallback of the
// History search button, kept in this file because ui_kit.go is shared.
var fallbackSearchIcon = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="7" cy="7" r="4.5"/><path d="M10.5 10.5L13.5 13.5"/></svg>`)

// defaultSearchPageSize is how many matches one commit search lists;
// More Results fetches the next page of the same size.
const defaultSearchPageSize = 500

// defaultRevealMaxSteps caps how many history pages a cross-page Show in
// History reveal loads while looking for its commit.
const defaultRevealMaxSteps = 10

// searchFieldDefs are the choices of what a commit search looks at, in the
// order the segmented control shows them.
var searchFieldDefs = []struct {
	field git.SearchField
	hint  string
}{
	{git.SearchMessage, "Words of the commit message"},
	{git.SearchAuthor, "Name or email of the author"},
	{git.SearchHash, "The start of a commit hash"},
	{git.SearchContent, "Text added or removed by the commit"},
	{git.SearchPath, "Part of the name of a file the commit changed"},
}

// searchUIState is the commit-search UI state of one app instance, kept
// outside the shared app struct in the app-keyed searchUIStates table. All
// fields are owned by the UI thread; background reads publish through
// sessionRead with the fixed scopes "commit-search", "commit-search-more",
// "search-detail" and "search-diff".
type searchUIState struct {
	open bool

	// repoGen is the repository generation the state below belongs to. When
	// session.Generation() moves past it the search resets itself.
	repoGen uint64

	query     string
	field     int
	matchCase bool
	regex     bool
	current   bool // only the branch checked out
	pickaxe   bool // content search as git log -S instead of -G
	path      string

	searched    bool
	searching   bool
	err         error
	results     []git.Commit
	more        bool
	loadingMore bool
	moreErr     string

	// selHash is the hash of the chosen result, "" for none. commit is a
	// copy of the chosen git.Commit captured on the UI thread (with cloned
	// Parents), passed to background reads so workers never read live UI
	// state.
	selHash string
	commit  git.Commit

	detail        git.CommitDetail
	files         []git.FileChange
	detailLoading bool
	detailErr     error

	fileSel int

	diff diffPanel

	list    ui.ListState
	listSel int

	splitSize float32

	// pageSize narrows the search page size when positive, zero meaning the
	// default; revealMaxSteps is how many history pages a cross-page reveal
	// may load. Both survive repository switches like layout choices do.
	pageSize       int
	revealMaxSteps int

	// pendingRevealHash is the commit a Show in History click is locating
	// in the commit graph: while it is set, applyPendingHistoryReveal grows
	// the loaded history one page at a time until the commit shows, the cap
	// is reached or the user chooses a row herself.
	pendingRevealHash  string
	pendingRevealGen   uint64
	pendingRevealSteps int
}

func (st *searchUIState) pageLimit() int {
	if st.pageSize > 0 {
		return st.pageSize
	}
	return defaultSearchPageSize
}

// searchOptions assembles the git.SearchOptions of the current controls.
// Regex is meaningless for hash and file searches — the control is disabled
// for them — and is forced off here as well, so the options never carry a
// combination the UI cannot produce.
func (st *searchUIState) searchOptions(branch string) git.SearchOptions {
	o := git.SearchOptions{
		Field:       searchFieldDefs[st.field].field,
		Query:       st.query,
		MatchCase:   st.matchCase,
		Occurrences: st.pickaxe,
		Limit:       st.pageLimit(),
	}
	if o.Field != git.SearchHash && o.Field != git.SearchPath {
		o.Regex = st.regex
	}
	if p := strings.TrimSpace(st.path); p != "" {
		o.Paths = []string{p}
	}
	if st.current && branch != "" {
		o.Revs = []string{"HEAD"}
	}
	return o
}

var searchUIStates = struct {
	sync.Mutex
	m map[*app]*searchUIState
}{m: map[*app]*searchUIState{}}

// searchUI returns the app's commit-search state, creating it on first use.
// It runs on the UI thread. A repository switch or close invalidates every
// in-flight search read of the old repository and drops its state: nothing
// typed, found or selected there may leak into the new repository.
func (a *app) searchUI() *searchUIState {
	searchUIStates.Lock()
	st := searchUIStates.m[a]
	if st == nil {
		st = &searchUIState{listSel: -1, fileSel: -1, revealMaxSteps: defaultRevealMaxSteps}
		searchUIStates.m[a] = st
	}
	searchUIStates.Unlock()

	if gen := a.session.Generation(); st.repoGen != gen {
		a.session.cancelRead("commit-search")
		a.session.cancelRead("commit-search-more")
		a.session.cancelRead("search-detail")
		st.diff.Clear(a.session, "search-diff")
		split := st.splitSize
		pageSize := st.pageSize
		revealMaxSteps := st.revealMaxSteps
		layout := st.diff.Layout()
		*st = searchUIState{
			repoGen:        gen,
			splitSize:      split,
			pageSize:       pageSize,
			revealMaxSteps: revealMaxSteps,
			listSel:        -1,
			fileSel:        -1,
		}
		st.diff.SetLayout(layout)
	}
	return st
}

func (a *app) isHistorySearchOpen() bool {
	return a.searchUI().open
}

func (a *app) openHistorySearch() {
	a.searchUI().open = true
}

func (a *app) closeHistorySearch() {
	a.searchUI().open = false
}

// historySearchHeaderButton is the compact Search entry at the right end of
// the History list header, opening the search view like Cmd-F does.
func (a *app) historySearchHeaderButton(c *ui.Context, p palette) {
	ui.Spacer(c)
	b := ui.ButtonBase(c.Key("history-search-open")).
		Height(22).
		PaddingX(8).
		Gap(5).
		Radius(6).
		AlignItems(ui.Center).
		Label("Search Commits").
		Tooltip("Search Commits (⌘F)").
		FocusRing(false)
	if b.Hovered() {
		b.Background(p.hover)
	}
	b.Children(func() {
		symbolIcon(c, "magnifyingglass", fallbackSearchIcon, 11, p.muted)
		ui.Text(c, "Search").FontSize(11.5).FontWeight(500).TextColor(p.muted).SingleLine()
	})
	if b.Clicked() {
		a.openHistorySearch()
	}
}

// historySearchView renders the commit search inside the History page: the
// search controls on top, the matches in a virtualized list and the chosen
// match's detail, changed files and diff below. Escape closes it, as the
// Close Search button does.
func (a *app) historySearchView(c *ui.Context, p palette) {
	st := a.searchUI()
	if st.splitSize <= 0 {
		st.splitSize = 340
	}

	c.OnWindowShortcut(0, ui.KeyEscape, a.closeHistorySearch)

	ui.Column(c.Key("history-search")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		a.searchControls(c, p, st)
		ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)
		a.searchBody(c, p, st)
	})
}

// searchControls renders the field picker, the query row and the filter
// options. Toggling a filter option with a query typed re-runs the search
// so the results always match the controls.
func (a *app) searchControls(c *ui.Context, p palette, st *searchUIState) {
	ui.Column(c.Key("search-controls")).FillWidth().Padding(10, 12).Gap(8).Shrink(0).Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Search Commits").FontSize(15).FontWeight(700).TextColor(p.text).SingleLine()
			if ui.Segmented(c.Key("search-in"), &st.field, "Message", "Author", "Hash", "Content", "File").Label("Search in").Changed() {
				a.researchIfQuery()
			}
			ui.Spacer(c)
			if ui.Button(c.Key("search-close"), "Close Search").Label("Close Search").Clicked() {
				a.closeHistorySearch()
			}
		})
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			field := ui.SearchField(c.Key("search-query"), &st.query).
				Grow(1).
				AutoFocus().
				Placeholder(searchFieldDefs[st.field].hint).
				Label("Search query")
			if field.Submitted() {
				a.runCommitSearch()
			}
			if ui.PrimaryButton(c.Key("search-run"), "Search").
				Label("Run Search").
				Disabled(st.searching || strings.TrimSpace(st.query) == "").
				Clicked() {
				a.runCommitSearch()
			}
		})
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			if ui.Checkbox(c.Key("search-matchcase"), &st.matchCase, "Match case").Changed() {
				a.researchIfQuery()
			}
			regexOff := searchFieldDefs[st.field].field == git.SearchHash || searchFieldDefs[st.field].field == git.SearchPath
			if ui.Checkbox(c.Key("search-regex"), &st.regex, "Regex").Disabled(regexOff).Changed() {
				a.researchIfQuery()
			}
			detached := a.session.Snapshot().Status.Branch == ""
			if ui.Checkbox(c.Key("search-current"), &st.current, "Current branch").Disabled(detached).Changed() {
				a.researchIfQuery()
			}
			if searchFieldDefs[st.field].field == git.SearchContent {
				if ui.Checkbox(c.Key("search-pickaxe"), &st.pickaxe, "Added or removed").
					Tooltip("Only commits that change how many times the text is there").
					Changed() {
					a.researchIfQuery()
				}
			}
			ui.Spacer(c)
			pathIn := ui.TextInput(c.Key("search-path"), &st.path).
				Width(160).
				Placeholder("In path").
				Label("Filter path").
				Tooltip("Keep the search to commits that changed this path")
			if pathIn.Submitted() {
				a.runCommitSearch()
			}
		})
	})
}

// researchIfQuery re-runs the search after a filter option changed, but only
// when a query is typed: an empty query has nothing to search for.
func (a *app) researchIfQuery() {
	if strings.TrimSpace(a.searchUI().query) != "" {
		a.runCommitSearch()
	}
}

// runCommitSearch starts the asynchronous search of the current controls
// under the fixed "commit-search" scope. Starting a new search discards the
// results and selection of the previous one, cancels any page load still in
// flight for it, and supersedes a slower earlier search through sessionRead:
// Enter and the filter checkboxes can re-search while a slower search is
// still in flight — only the Run Search button is disabled then — and the
// late completion of the superseded search must never overwrite the newer
// results.
func (a *app) runCommitSearch() {
	st := a.searchUI()
	if strings.TrimSpace(st.query) == "" {
		return
	}
	opts := st.searchOptions(a.session.Snapshot().Status.Branch)
	st.searched = true
	st.searching = true
	st.err = nil
	st.more = false
	st.moreErr = ""
	st.results = nil
	a.session.cancelRead("commit-search-more")
	st.loadingMore = false
	a.clearSearchSelection(st)
	if !sessionRead(a.session, "commit-search", func(repo *git.Repo) ([]git.Commit, error) {
		return repo.Search(opts)
	}, func(found []git.Commit, err error) {
		st.searching = false
		if err != nil {
			st.err = err
			return
		}
		st.err = nil
		st.results = found
		st.more = len(found) >= opts.Limit
		if len(found) > 0 {
			st.listSel = 0
			a.selectSearchResult(found[0])
		}
	}) {
		st.searching = false
	}
}

// loadMoreSearchResults appends the next page of matches of the search
// shown, deduplicated by hash: the walk is stable, but a search running
// against a repository being written to can repeat a match across pages.
func (a *app) loadMoreSearchResults() {
	st := a.searchUI()
	if !st.more || st.loadingMore || st.searching || len(st.results) == 0 {
		return
	}
	opts := st.searchOptions(a.session.Snapshot().Status.Branch)
	opts.Skip = len(st.results)
	have := len(st.results)
	st.loadingMore = true
	st.moreErr = ""
	if !sessionRead(a.session, "commit-search-more", func(repo *git.Repo) ([]git.Commit, error) {
		return repo.Search(opts)
	}, func(found []git.Commit, err error) {
		st.loadingMore = false
		if err != nil {
			st.moreErr = err.Error()
			return
		}
		if len(st.results) != have {
			// The result list changed under the load; appending would
			// corrupt it.
			return
		}
		seen := make(map[string]bool, len(st.results))
		for _, cm := range st.results {
			seen[cm.Hash] = true
		}
		for _, cm := range found {
			if !seen[cm.Hash] {
				st.results = append(st.results, cm)
			}
		}
		st.more = len(found) >= opts.Limit
	}) {
		st.loadingMore = false
	}
}

// clearSearchSelection drops the chosen result, its detail, files and diff,
// and invalidates any in-flight detail read of it.
func (a *app) clearSearchSelection(st *searchUIState) {
	st.selHash = ""
	st.commit = git.Commit{}
	st.detail = git.CommitDetail{}
	st.detailErr = nil
	st.detailLoading = false
	st.files = nil
	st.fileSel = -1
	st.listSel = -1
	a.session.cancelRead("search-detail")
	st.diff.Clear(a.session, "search-diff")
}

// selectSearchResult chooses a result commit and starts the asynchronous
// detail read. The commit is copied on the UI thread with its parents cloned
// so the background worker reads no live UI state.
func (a *app) selectSearchResult(cm git.Commit) {
	st := a.searchUI()
	st.selHash = cm.Hash
	cm.Parents = append([]string(nil), cm.Parents...)
	st.commit = cm
	st.detailLoading = true
	st.detail = git.CommitDetail{}
	st.detailErr = nil
	st.files = nil
	st.fileSel = -1
	st.diff.Clear(a.session, "search-diff")
	a.startSearchDetailLoad(cm)
}

// startSearchDetailLoad begins the asynchronous read of the result commit's
// detail and changed-file list under the fixed "search-detail" scope.
// Results of superseded reads are discarded by sessionRead itself; done
// additionally checks the selection still points at the same commit before
// applying.
func (a *app) startSearchDetailLoad(cm git.Commit) {
	st := a.searchUI()
	hash := cm.Hash
	sessionRead(a.session, "search-detail", func(repo *git.Repo) (historyDetailResult, error) {
		d, err := repo.Detail(cm.Hash)
		if err != nil {
			return historyDetailResult{}, err
		}
		files, err := repo.CommitFiles(cm)
		if err != nil {
			return historyDetailResult{}, err
		}
		return historyDetailResult{detail: d, files: files}, nil
	}, func(res historyDetailResult, err error) {
		if st.selHash != hash {
			return
		}
		st.detailLoading = false
		if err != nil {
			st.detailErr = err
			st.files = nil
			st.detail = git.CommitDetail{}
			st.fileSel = -1
			st.diff.Clear(a.session, "search-diff")
			return
		}
		st.detail = res.detail
		st.files = res.files
		st.detailErr = nil
		if len(res.files) == 0 {
			st.fileSel = -1
			st.diff.Clear(a.session, "search-diff")
			return
		}
		st.fileSel = 0
		st.diff.Load(a.session, "search-diff", commitFileDiffRequest(st.commit, res.files[0]))
	})
}

// searchBody renders one of the four search states — never searched,
// searching, no matches, results — or the failure view, below the controls.
func (a *app) searchBody(c *ui.Context, p palette, st *searchUIState) {
	switch {
	case st.searching:
		placeholderCard(c, "Searching…", "", "", p)
	case st.err != nil:
		ui.Column(c.Key("search-error")).Grow(1).FillWidth().Center().Padding(24).Gap(8).Children(func() {
			ui.Text(c, "Failed to search commits").FontSize(16).FontWeight(600).TextColor(p.danger).TextAlign(ui.Center)
			ui.Text(c, git.RedactText(st.err.Error())).FontSize(12.5).TextColor(p.muted).TextAlign(ui.Center).MaxWidth(520)
			if ui.Button(c.Key("search-retry"), "Retry Search").Label("Retry Search").Clicked() {
				a.runCommitSearch()
			}
		})
	case !st.searched:
		placeholderCard(c, "Type a query and press Enter to search commits.", "", "", p)
	case len(st.results) == 0:
		placeholderCard(c, "No commits match", "", "", p)
	default:
		ui.SplitVertical(c, &st.splitSize, func() {
			a.searchResultsPane(c, p, st)
		}, func() {
			a.searchDetailPane(c, p, st)
		}).Grow(1).FillWidth()
	}
}

// searchResultCount is the result count line: the total once the search
// exhausted the history, or a First-N note while more pages may exist.
func searchResultCount(n int, more bool) string {
	if more {
		return fmt.Sprintf("First %d results", n)
	}
	if n == 1 {
		return "1 result"
	}
	return fmt.Sprintf("%d results", n)
}

// searchResultsPane renders the count bar with the Show in History action,
// the virtualized result list and the More Results footer.
func (a *app) searchResultsPane(c *ui.Context, p palette, st *searchUIState) {
	st.list.Key = func(row int) any {
		return st.results[row].Hash
	}
	st.list.Label = func(row int) string {
		cm := st.results[row]
		return "Search Result " + cm.Short() + " " + cm.Subject
	}
	st.list.Selected = &st.listSel

	ui.Column(c.Key("search-results")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Row(c.Key("search-count-row")).Height(28).FillWidth().PaddingX(14).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
			ui.Text(c, searchResultCount(len(st.results), st.more)).FontSize(11).TextColor(p.faint).SingleLine()
			ui.Spacer(c)
			b := ui.ButtonBase(c.Key("search-show-in-history")).
				Height(22).
				PaddingX(8).
				Radius(6).
				Center().
				Label("Show in History").
				Tooltip("Show the selected commit in the history graph").
				Disabled(st.selHash == "").
				FocusRing(false).
				Border(1, p.border)
			switch {
			case st.selHash == "":
				b.Background(p.surface)
			case b.Hovered():
				b.Background(p.hover)
			default:
				b.Background(p.surface)
			}
			fg := p.text
			if st.selHash == "" {
				fg = p.faint
			}
			b.Children(func() {
				ui.Text(c, "Show in History").FontSize(11.5).FontWeight(500).TextColor(fg).SingleLine()
			})
			if b.Clicked() {
				a.showSelectedResultInHistory()
			}
		})
		ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)

		// The list paints the chosen row through the theme accent, narrowed
		// to the macOS-style selection blue like the commit list does.
		listTheme := *c.Theme()
		listTheme.Accent = p.listSelected
		listTheme.AccentText = p.onAccent
		ui.Themed(c.Key("search-list-theme"), &listTheme).
			Grow(1).FillWidth().MinHeight(0).Background(ui.Transparent).
			Children(func() {
				list := ui.List(c.Key("search-scroll"), &st.list, len(st.results)).
					Grow(1).FillWidth().MinHeight(0).Label("Search Results").
					Rows(func(r ui.ListRow) {
						a.searchResultRow(r.Context, p, r.Selected(), st.results[r.Index])
					})
				if list.Changed() {
					if st.listSel >= 0 && st.listSel < len(st.results) {
						a.selectSearchResult(st.results[st.listSel])
					}
				}
			})

		if st.more {
			ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)
			a.searchMoreRow(c, p, st)
		}
	})
}

// searchResultRow renders one match inside the virtual list: ref badges,
// subject, author, short hash and date, with a context menu jumping to the
// commit in the history graph.
func (a *app) searchResultRow(c *ui.Context, p palette, selected bool, cm git.Commit) {
	subjectColor, metaColor := p.text, p.muted
	if selected {
		subjectColor, metaColor = p.onAccent, p.onAccentMuted
	}
	ui.Row(c).
		Height(historyRowHeight).
		FillWidth().
		PaddingX(10).
		Gap(10).
		AlignItems(ui.Center).
		ContextMenu(func(m *ui.Menu) {
			if m.Item("Show in History").Chosen() {
				a.showSearchResultInHistory(cm)
			}
		}).
		Children(func() {
			ui.Row(c).Gap(5).AlignItems(ui.Center).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				historyRefBadges(c, cm.Refs, p, selected)
				ui.Text(c, cm.Subject).FontSize(12.5).TextColor(subjectColor).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			})
			ui.Text(c, cm.Author).FontSize(11.5).TextColor(metaColor).SingleLine().Width(110).Shrink(0).Ellipsis("…")
			ui.Text(c, cm.Short()).Font("monospace").FontSize(11).TextColor(metaColor).SingleLine().Width(64).Shrink(0)
			ui.Text(c, historyRelDate(cm.Date)).FontSize(11.5).TextColor(metaColor).SingleLine().Width(104).Shrink(0)
		})
}

// searchMoreRow renders the bottom-of-list pagination control. It is
// disabled while a page load is in flight, so repeated clicks cannot
// accumulate overlapping page reads.
func (a *app) searchMoreRow(c *ui.Context, p palette, st *searchUIState) {
	ui.Row(c.Key("search-more-row")).FillWidth().Padding(6, 14).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
		if st.moreErr != "" {
			ui.Text(c, git.RedactText(st.moreErr)).FontSize(11).TextColor(p.danger).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
		}
		ui.Spacer(c)
		b := ui.ButtonBase(c.Key("search-more")).
			Height(26).
			PaddingX(12).
			Radius(6).
			Center().
			Label("More Results").
			Disabled(st.loadingMore).
			FocusRing(false).
			Border(1, p.border)
		switch {
		case st.loadingMore:
			b.Background(p.hover)
		case b.Hovered():
			b.Background(p.hover)
		default:
			b.Background(p.surface)
		}
		fg := p.text
		if st.loadingMore {
			fg = p.faint
		}
		b.Children(func() {
			label := "More Results"
			if st.loadingMore {
				label = "Loading…"
			}
			ui.Text(c, label).FontSize(12).FontWeight(500).TextColor(fg).SingleLine()
		})
		if b.Clicked() {
			a.loadMoreSearchResults()
		}
	})
}

// searchDetailPane renders the bottom split of the search view: the chosen
// match's metadata and changed files on the left, the shared diff panel on
// the right.
func (a *app) searchDetailPane(c *ui.Context, p palette, st *searchUIState) {
	ui.Row(c.Key("search-detail-pane")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Column(c.Key("search-detail-left")).Width(320).FillHeight().Shrink(0).Children(func() {
			a.searchCommitInfo(c, p, st)
			ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)
			a.searchFileList(c, p, st)
		})
		ui.Box(c).Width(1).FillHeight().Background(p.hairline).Shrink(0)
		ui.Box(c.Key("search-detail-right")).Grow(1).FillHeight().MinWidth(0).Children(func() {
			st.diff.View(c, a.session, "search-diff", p)
		})
	})
}

func (a *app) searchCommitInfo(c *ui.Context, p palette, st *searchUIState) {
	switch {
	case st.selHash == "":
		ui.Column(c.Key("search-info-empty")).Padding(14).Gap(4).FillWidth().Shrink(0).Children(func() {
			ui.Text(c, "No commit selected").FontSize(12.5).FontWeight(600).TextColor(p.muted)
			ui.Text(c, "Choose a search result above to see its details.").FontSize(11.5).TextColor(p.faint)
		})
	case st.detailLoading:
		ui.Column(c.Key("search-info-loading")).Padding(14).FillWidth().Shrink(0).Children(func() {
			ui.Text(c, "Loading commit details…").FontSize(12.5).TextColor(p.muted)
		})
	case st.detailErr != nil:
		ui.Column(c.Key("search-info-error")).Padding(14).Gap(6).FillWidth().Shrink(0).Children(func() {
			ui.Text(c, "Failed to load commit details").FontSize(12.5).FontWeight(600).TextColor(p.danger)
			ui.Text(c, st.detailErr.Error()).FontSize(11.5).TextColor(p.muted)
			if ui.Button(c.Key("search-detail-retry"), "Retry").Label("Retry Commit Details").Clicked() {
				st.detailErr = nil
				st.detailLoading = true
				a.startSearchDetailLoad(st.commit)
			}
		})
	default:
		d := st.detail
		ui.Scroll(c.Key("search-info-scroll")).FillWidth().MaxHeight(150).Shrink(0).Children(func() {
			ui.Column(c).Padding(12, 14).Gap(7).FillWidth().Children(func() {
				ui.Text(c, firstLineOf(d.Message)).FontSize(12.5).FontWeight(600).TextColor(p.text)
				if body := restOfMessage(d.Message); body != "" {
					ui.Text(c, body).FontSize(11.5).TextColor(p.muted)
				}
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "Commit").FontSize(11).TextColor(p.faint).SingleLine().Width(52).Shrink(0)
					ui.Text(c, d.Hash).Font("monospace").FontSize(11).TextColor(p.muted).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
				})
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "Author").FontSize(11).TextColor(p.faint).SingleLine().Width(52).Shrink(0)
					ui.Text(c, d.Author+" <"+d.AuthorEmail+">").FontSize(11).TextColor(p.muted).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
				})
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "Date").FontSize(11).TextColor(p.faint).SingleLine().Width(52).Shrink(0)
					ui.Text(c, d.Date.Format("Jan 2, 2006 15:04")).FontSize(11).TextColor(p.muted).SingleLine()
				})
				if len(d.Parents) > 0 {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "Parents").FontSize(11).TextColor(p.faint).SingleLine().Width(52).Shrink(0)
						var shorts []string
						for _, par := range d.Parents {
							if len(par) > 7 {
								par = par[:7]
							}
							shorts = append(shorts, par)
						}
						ui.Text(c, strings.Join(shorts, " ")).Font("monospace").FontSize(11).TextColor(p.muted).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
					})
				}
			})
		})
	}
}

// searchFileList renders the files the chosen match changes. Clicking a file
// loads its diff into the shared diff panel.
func (a *app) searchFileList(c *ui.Context, p palette, st *searchUIState) {
	ui.Column(c.Key("search-files")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Row(c.Key("search-files-header")).Height(26).FillWidth().PaddingX(14).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
			ui.Text(c, "Changed files").FontSize(11).FontWeight(600).TextColor(p.muted).SingleLine()
			if len(st.files) > 0 {
				ui.Text(c, fmt.Sprint(len(st.files))).FontSize(11).TextColor(p.faint).SingleLine()
			}
		})
		ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)
		switch {
		case st.selHash == "" || st.detailLoading || st.detailErr != nil:
			// Nothing to list yet; the info section above reports the state.
		case len(st.files) == 0:
			ui.Row(c.Key("search-files-empty")).Padding(8, 14).FillWidth().Shrink(0).Children(func() {
				ui.Text(c, "No files changed").FontSize(11.5).TextColor(p.faint).SingleLine()
			})
		default:
			ui.Scroll(c.Key("search-files-scroll")).Grow(1).FillWidth().MinHeight(0).Children(func() {
				ui.Column(c).FillWidth().Padding(4, 0).Children(func() {
					for i, f := range st.files {
						a.searchFileRow(c, p, st, i, f)
					}
				})
			})
		}
	})
}

func (a *app) searchFileRow(c *ui.Context, p palette, st *searchUIState, i int, f git.FileChange) {
	selected := st.fileSel == i
	b := ui.ButtonBase(c.Key("search-file-" + st.commit.Hash + "-" + f.Path)).
		Height(24).
		FillWidth().
		PaddingX(14).
		Gap(7).
		AlignItems(ui.Center).
		Justify(ui.Start).
		Role(ui.RoleListItem).
		Label("File " + f.Path).
		AccessSelected(selected).
		FocusRing(false)
	switch {
	case selected:
		b.Background(p.selected)
	case b.Hovered():
		b.Background(p.hover)
	}
	b.Children(func() {
		col := historyStatusColor(p, f.Status)
		ui.Box(c).Size(16, 16).Radius(4).Background(col.Alpha(0.18)).Center().Shrink(0).Children(func() {
			ui.Text(c, string(f.Status)).FontSize(9.5).FontWeight(700).TextColor(col).SingleLine()
		})
		name := f.Path
		if f.OrigPath != "" && f.OrigPath != f.Path {
			name = f.OrigPath + " → " + f.Path
		}
		ui.Text(c, name).FontSize(11.5).TextColor(p.text).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
	})
	if b.Clicked() {
		if st.fileSel != i && st.selHash != "" {
			st.fileSel = i
			st.diff.Load(a.session, "search-diff", commitFileDiffRequest(st.commit, f))
		}
	}
}

// showSelectedResultInHistory jumps the currently chosen match back to the
// history graph; it is the action of the result bar's Show in History button.
func (a *app) showSelectedResultInHistory() {
	st := a.searchUI()
	if st.selHash == "" {
		return
	}
	a.showSearchResultInHistory(st.commit)
}

// showSearchResultInHistory closes the search view and records the commit to
// locate in the history graph. applyPendingHistoryReveal picks it up from
// there: the reveal must run where the commit list is built, because the
// commit can be pages past the loaded history.
func (a *app) showSearchResultInHistory(cm git.Commit) {
	st := a.searchUI()
	st.open = false
	st.pendingRevealHash = cm.Hash
	st.pendingRevealGen = a.session.Generation()
	st.pendingRevealSteps = 0
}

// cancelPendingHistoryReveal drops a pending Show in History reveal: the
// user chose a commit row herself, so the reveal must not take the
// selection back.
func (a *app) cancelPendingHistoryReveal() {
	a.searchUI().pendingRevealHash = ""
}

// applyPendingHistoryReveal advances a pending Show in History reveal. It
// runs in historyCommitList before the commit list is built: when the
// commit is among the loaded rows it is chosen, centered and detailed right
// away; while it is not, the loaded history grows one page at a time — never
// two loads in flight, never past the step cap — and when the history is
// exhausted without the commit, the reveal ends with a toast instead of
// silently doing nothing.
func (a *app) applyPendingHistoryReveal(snap repository.Snapshot) {
	st := a.searchUI()
	if st.pendingRevealHash == "" {
		return
	}
	if st.pendingRevealGen != a.session.Generation() {
		st.pendingRevealHash = ""
		return
	}
	hv := &a.historyView
	for idx, cm := range snap.Commits {
		if cm.Hash == st.pendingRevealHash {
			st.pendingRevealHash = ""
			hv.listSel = idx
			hv.list.ScrollTo(idx, ui.Center)
			a.selectHistoryCommit(cm)
			return
		}
	}
	if snap.More && !a.session.Loading() && !a.session.Busy() && st.pendingRevealSteps < st.revealMaxSteps {
		st.pendingRevealSteps++
		a.session.LoadMore(0)
		return
	}
	if (!snap.More || st.pendingRevealSteps >= st.revealMaxSteps) && !a.session.Loading() && !a.session.Busy() {
		st.pendingRevealHash = ""
		a.toast = "Could not find the commit in the loaded history."
	}
}
