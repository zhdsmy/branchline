package app

import (
	"runtime"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

const (
	sidebarWidth      = 232
	topBarHeight      = 40
	topEdgePad        = 6
	topBarTotalHeight = topBarHeight + topEdgePad
	pageTopGap        = 12
)

func (a *app) view(c *ui.Context) {
	dark := c.Theme().Dark
	if !a.darkKnown {
		a.darkKnown = true
		a.lastDark = dark
	} else if a.lastDark != dark {
		a.lastDark = dark
		a.themeGen++
	}
	a.pal = applyPalette(c, a.themeGen)
	p := a.pal

	a.bindShortcuts(c)

	if c.Vibrancy() {
		c.Root().Background(ui.Transparent)
	}
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		side := ui.Column(c.Key("sidebar")).Width(sidebarWidth)
		if !c.Vibrancy() {
			side.Background(p.sidebar)
		}
		side.Children(func() { a.sidebar(c) })

		ui.Box(c.Key("main")).Grow(1).Background(p.bg).Children(func() {
			a.pageContent(c)
			a.topEdgeBar(c)
		})
	})

	if a.toast != "" {
		c.Toast(a.toast)
		a.toast = ""
	}
}

func (a *app) bindShortcuts(c *ui.Context) {
	c.OnShortcut(0, ui.KeyBack, a.back)
	c.OnShortcut(0, ui.KeyForward, a.goForward)
	c.OnShortcut(ui.Cmd, ui.KeyBracketLeft, a.back)
	c.OnShortcut(ui.Cmd, ui.KeyBracketRight, a.goForward)
	c.OnShortcut(ui.Cmd, ui.KeyComma, a.openSettings)
}

func (a *app) open(r route) {
	if r.repoID == "" {
		r.repoID = a.activeRepoID()
	}
	if r == a.route {
		return
	}
	a.history = append(a.history, a.route)
	a.forward = nil
	a.show(r)
}

// show applies r as the active route. Note: cross-repository routing calls
// switchRepoByID -> openRepo, which clears a.history and a.forward, so
// back/forward history is scoped to the currently open repository.
func (a *app) show(r route) {
	if r.repoID == "" {
		r.repoID = a.activeRepoID()
	}
	if r.repoID != "" && r.repoID != a.activeRepoID() {
		a.switchRepoByID(r.repoID)
	}
	a.route = r
}

func (a *app) back() {
	if len(a.history) == 0 {
		return
	}
	a.forward = append(a.forward, a.route)
	prev := a.history[len(a.history)-1]
	a.history = a.history[:len(a.history)-1]
	a.show(prev)
}

func (a *app) goForward() {
	if len(a.forward) == 0 {
		return
	}
	a.history = append(a.history, a.route)
	next := a.forward[len(a.forward)-1]
	a.forward = a.forward[:len(a.forward)-1]
	a.show(next)
}

func (a *app) sidebar(c *ui.Context) {
	top := float32(14)
	if c.TitleBar().Left > 0 {
		top = topBar(c) + 4
	}
	ui.Column(c).Fill().Padding(top, 12, 12, 12).Gap(6).DragWindow().Children(func() {
		a.repoSwitcher(c)

		// The whole scroll area of the sidebar — the navigation rows, the
		// section headers, the entry rows, and every reference and stash
		// row — is one virtual list: it builds only the rows in view, and
		// its row table rebuilds only when the data behind it changed. The
		// rows keep their own buttons, menus and selection painting: the
		// list never chooses anything itself — its selection is the mirror
		// of what the rows mark, rebuilt with the table — so the row
		// element it wraps every row in only repeats what the row inside
		// tells assistive technology, and the theme copy keeps the fill
		// and hover the list would paint for that invisible, the rows
		// painting their own exactly as before. KeepOffset keeps the
		// scrolled place in pixels as rows come and go, exactly as the
		// plain scroll container did.
		rows := a.sidebarRows()
		rc := &a.refsView.sidebarRows
		a.refsView.sidebarList.KeepOffset = true
		a.refsView.sidebarList.Key = func(row int) any { return rows[row].key }
		a.refsView.sidebarList.Label = func(row int) string { return rows[row].accessName }
		a.refsView.sidebarList.Selection = &rc.mirror
		a.refsView.sidebarList.MenuKeepsSelection = true
		rowTheme := *c.Theme()
		listTheme := rowTheme
		listTheme.Accent = ui.Transparent
		listTheme.SurfaceHover = ui.Transparent
		ui.Themed(c.Key("sidebar-list-theme"), &listTheme).
			Grow(1).Margin(0, -4).Background(ui.Transparent).
			Children(func() {
				ui.List(c.Key("sidebar-scroll"), &a.refsView.sidebarList, len(rows)).
					TrackScroll(&a.navScroll).
					Grow(1).FillWidth().
					Padding(0, 4).
					Gap(4).
					Rows(func(r ui.ListRow) {
						a.sidebarRowView(r.Context, rows[r.Index], &rowTheme)
					})
			})

		a.refsSidebar(c)
		a.stashSidebar(c)
	})
}

func (a *app) navRow(c *ui.Context, label, sfName string, fallback *ui.SVG, target route, badge string) {
	p := a.pal
	selected := a.route.page == target.page
	b := ui.ButtonBase(c.Key("nav-" + label)).
		Height(34).
		PaddingX(10).
		Gap(10).
		Radius(8).
		AlignItems(ui.Center).
		Justify(ui.Start).
		Label(label).
		AccessSelected(selected).
		FocusRing(false)
	if selected {
		b.Current(ui.CurrentPage)
	}
	bg := ui.Transparent
	switch {
	case selected:
		bg = p.selected
	case b.Hovered():
		bg = p.hover
	}
	b.Background(b.AnimateColor(colorAnimKey{"bg", p.gen}, bg, ui.Animation{Duration: 110 * time.Millisecond}))
	iconColor := p.muted
	if selected {
		iconColor = p.accent
	}
	b.Children(func() {
		symbolIcon(c, sfName, fallback, 17, iconColor)
		ui.Text(c, label).FontSize(13.5).FontWeight(500).TextColor(p.text).SingleLine()
		ui.Spacer(c)
		if badge != "" {
			ui.Text(c, badge).FontSize(11.5).TextColor(p.muted).SingleLine()
		}
	})
	if b.Clicked() {
		a.open(target)
	}
}

func topBar(c *ui.Context) float32 {
	if runtime.GOOS != "darwin" {
		return topBarTotalHeight
	}
	return max(c.TitleBar().Height, topBarHeight)
}

func (a *app) pageTopInset(c *ui.Context) float32 {
	top := topBar(c)
	if runtime.GOOS == "darwin" {
		top += topEdgePad
	}
	return top
}

// startMark records whether scrolled page content sits beneath the floating
// top glass edge. Pages place this first inside their scroll container.
func (a *app) startMark(c *ui.Context) {
	b := ui.Box(c.Key("start-mark")).Height(0).Bounds()
	if b.W > 0 {
		a.underTop = b.Y < a.pageTopInset(c)+pageTopGap-0.5
	}
}

func (a *app) topEdgeBar(c *ui.Context) {
	p := a.pal
	edgeH := a.pageTopInset(c)
	edge := ui.Box(c.Key("top-edge")).
		Absolute().
		Top(0).
		Left(0).
		Right(0).
		Height(edgeH).
		DragWindow().
		Material(glass.ScrollEdge{Hard: true, Background: p.bg})
	targetOpacity := float32(0)
	if a.underTop || edge.Hovered() {
		targetOpacity = 1
	}
	edge.Opacity(edge.AnimateTo("edge", targetOpacity, ui.Animation{Duration: 160 * time.Millisecond}))

	barH := topBar(c)
	ui.Row(c.Key("nav-history-controls")).
		Absolute().
		Top(0).
		Left(14).
		Height(barH).
		Gap(2).
		AlignItems(ui.Center).
		Children(func() {
			canBack := len(a.history) > 0
			backColor := p.muted
			if !canBack {
				backColor = p.faint
			}
			backBtn := iconBtn(c.Key("back"), "chevron.left", fallbackBack, 15, backColor, "Back", p).Disabled(!canBack)
			if backBtn.Clicked() {
				a.back()
			}

			canFwd := len(a.forward) > 0
			fwdColor := p.muted
			if !canFwd {
				fwdColor = p.faint
			}
			fwdBtn := iconBtn(c.Key("forward"), "chevron.right", fallbackFwd, 15, fwdColor, "Forward", p).Disabled(!canFwd)
			if fwdBtn.Clicked() {
				a.goForward()
			}
		})

	ui.Row(c.Key("toolbar-slot")).
		Absolute().
		Top(0).
		Right(14).
		Height(barH).
		Gap(6).
		AlignItems(ui.Center).
		Children(func() {
			a.syncToolbar(c)
		})
}

func (a *app) pageContent(c *ui.Context) {
	if a.scrolled != a.route {
		a.pageScroll = ui.ScrollState{}
		a.scrolled = a.route
	}
	a.underTop = false

	if !a.session.HasRepo() {
		a.emptyRepoPage(c)
		return
	}
	if a.missingRepoPage(c) {
		return
	}

	if a.hasGitVersionBanner() {
		ui.Column(c.Key("page-with-git-banner")).Fill().Children(func() {
			a.gitVersionBanner(c)
			ui.Box(c.Key("page-below-git-banner")).Grow(1).FillWidth().MinHeight(0).Children(func() {
				a.renderActivePage(c)
			})
		})
		return
	}
	a.renderActivePage(c)
}

func (a *app) renderActivePage(c *ui.Context) {
	switch a.route.page {
	case pageHistory:
		a.historyPage(c)
	default:
		a.changesPage(c)
	}
}

func (a *app) emptyRepoPage(c *ui.Context) {
	top := a.pageTopInset(c)
	ui.Column(c.Key("empty-repo")).Fill().Padding(top, 24, 24, 24).Children(func() {
		placeholderCard(
			c,
			"No Repository Open",
			"Open a local Git repository or connect to an SSH server repository to get started.",
			"Use the Repository Switcher in the sidebar to add a local folder or SSH repository.",
			a.pal,
		)
	})
}
