package app

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// gitVersionUIState is the per-app UI state of the Git version banner: the
// repository generation whose banner the user dismissed. Capability answers
// live in the session; this state only decides whether the banner is visible.
type gitVersionUIState struct {
	dismissedGen uint64
}

func (a *app) gitVersionUI() *gitVersionUIState {
	return featureState(a, func() *gitVersionUIState { return &gitVersionUIState{} })
}

// hasGitVersionBanner reports whether the page content renders a compatibility
// banner on top. It is false for a verified Git of 2.25 or later, while the
// first probe is still in flight, after the user dismissed the banner of the
// current repository generation, and without an open repository — in all of
// which the page keeps its exact unbannered layout. Dismissal is scoped to the
// repository generation: switching away and back re-evaluates the banner.
func (a *app) hasGitVersionBanner() bool {
	if !a.session.HasRepo() {
		return false
	}
	if a.gitVersionUI().dismissedGen == a.session.Generation() {
		return false
	}
	if a.session.GitCapabilitiesErr() != nil {
		return true
	}
	caps, verified := a.session.GitCapabilities()
	return verified && !caps.MeetsMinimum
}

func formatGitVersion(v git.GitVersion) string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// gitVersionBanner renders the non-blocking compatibility banner for the two
// states that never prevent opening or using the repository: a verified Git
// older than 2.25, and a failed version probe. The probe failure is never
// presented as a low version. The banner's top padding clears the floating
// top edge bar so the Retry and Dismiss buttons stay visible and clickable.
func (a *app) gitVersionBanner(c *ui.Context) {
	if err := a.session.GitCapabilitiesErr(); err != nil {
		a.gitVersionBannerBox(c, err)
		return
	}
	caps, verified := a.session.GitCapabilities()
	if verified && !caps.MeetsMinimum {
		a.gitVersionBannerBox(c, nil)
	}
}

func (a *app) gitVersionBannerBox(c *ui.Context, probeErr error) {
	p := a.pal
	t := c.Theme()
	isErr := probeErr != nil
	color := t.Warning
	if isErr {
		color = t.Danger
	}

	msg := ""
	if isErr {
		msg = "Git version check failed: " + git.RedactText(probeErr.Error()) + ". Options requiring a newer Git are unavailable."
	} else if caps, verified := a.session.GitCapabilities(); verified {
		msg = fmt.Sprintf("Git %s is older than 2.25; some operations may fail. Upgrade to Git 2.25 or later.", formatGitVersion(caps.Version))
	}

	retrying := a.session.GitCapabilitiesRetrying()

	ui.Column(c.Key("git-version-banner")).
		FillWidth().
		Padding(a.pageTopInset(c)+4, 24, 6, 24).
		Role(ui.RoleStatus).
		Label("Git Version Warning").
		Children(func() {
			ui.Row(c.Key("git-version-banner-row")).
				FillWidth().
				Padding(8, 12, 8, 12).
				Gap(8).
				AlignItems(ui.Center).
				Radius(8).
				Background(color.Alpha(0.14)).
				Border(1, color.Alpha(0.5)).
				Children(func() {
					ui.Text(c, msg).FontSize(12.5).TextColor(p.text).Grow(1)

					if isErr {
						retryLabel := "Retry"
						if retrying {
							retryLabel = "Checking…"
						}
						retry := a.gitVersionBannerButton(c, "git-version-retry", retryLabel, "Retry Git Version Check", color, p, retrying)
						if retry.Clicked() {
							a.session.RetryGitCapabilities()
						}
					}

					dismiss := a.gitVersionBannerButton(c, "git-version-dismiss", "Dismiss", "Dismiss Git Version Warning", color, p, false)
					if dismiss.Clicked() {
						a.gitVersionUI().dismissedGen = a.session.Generation()
					}
				})
		})
}

// gitVersionBannerButton builds one small banner action whose accessible
// label differs from its visible text, so tests and assistive technology
// activate it by the full action name while the banner stays compact.
func (a *app) gitVersionBannerButton(c *ui.Context, key, text, accessLabel string, color ui.Color, p palette, disabled bool) ui.Element {
	b := ui.ButtonBase(c.Key(key)).
		Height(24).
		PaddingX(10).
		Radius(6).
		AlignItems(ui.Center).
		Label(accessLabel).
		Disabled(disabled).
		FocusRing(false)
	fg := color
	bg := color.Alpha(0.10)
	if disabled {
		fg = p.muted
	}
	if !disabled {
		if b.Pressed() {
			bg = color.Alpha(0.22)
		} else if b.Hovered() {
			bg = color.Alpha(0.18)
		}
	}
	b.Background(b.AnimateColor(colorAnimKey{"git-banner-btn", p.gen}, bg, ui.Animation{Duration: 110 * time.Millisecond}))
	b.Children(func() {
		ui.Text(c, text).FontSize(12).FontWeight(500).TextColor(fg).SingleLine()
	})
	return b
}
