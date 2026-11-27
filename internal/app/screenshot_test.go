package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/store"
)

// This file records the visual regression baselines of the ten key UI
// states. Everything here is test-only: product code is never adjusted to
// make a screenshot pass. The baselines are darwin-only because CoreText and
// SF Symbols are part of the pixels being guarded.

var updateScreenshots = flag.Bool("update", false, "update UI screenshot golden baselines")

const (
	screenshotWidth      = 1200
	screenshotHeight     = 800
	screenshotScale      = 2
	screenshotChanTol    = 4
	screenshotMaxRatio   = 0.0002
	screenshotMaxSizeKiB = 5120

	screenshotGoldenDir = "testdata/screenshots"
	screenshotMetaFile  = "meta.json"
)

var screenshotFixtureTime = time.Date(2025, 5, 15, 10, 30, 0, 0, time.UTC)

// screenshotMeta is the environment identity recorded next to the baselines.
// macOS major-version changes default to a skip because glyph rasterization
// is allowed to change there; mygo changes never skip because framework
// upgrades are exactly what this regression suite watches.
type screenshotMeta struct {
	GOOS         string `json:"goos"`
	GOARCH       string `json:"goarch"`
	MacOSVersion string `json:"macos_version"`
	MacOSMajor   int    `json:"macos_major"`
	MacOSBuild   string `json:"macos_build"`
	MyGoVersion  string `json:"mygo_version"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Scale        int    `json:"scale"`
}

type screenshotHostMeta struct {
	MacOSVersion string
	MacOSMajor   int
	MacOSBuild   string
	MyGoVersion  string
}

type screenshotCompareStats struct {
	TotalPixels    int
	DiffPixels     int
	Ratio          float64
	MaxChannelDiff uint8
	Bounds         image.Rectangle
	HasDiff        bool
}

type screenshotState struct {
	name  string
	setup func(t *testing.T) *testHarness
}

func screenshotStates() []screenshotState {
	return []screenshotState{
		{"changes-diff", setupScreenshotChangesDiff},
		{"history-detail", setupScreenshotHistoryDetail},
		{"refs-sidebar", setupScreenshotRefsSidebar},
		{"repo-switcher", setupScreenshotRepoSwitcher},
		{"add-ssh-modal", setupScreenshotAddSSHModal},
		{"sync-error-banner", setupScreenshotSyncErrorBanner},
		{"askpass-dialog", setupScreenshotAskPassDialog},
		{"empty-repo-unborn", setupScreenshotEmptyRepoUnborn},
		{"no-repo-welcome", setupScreenshotNoRepoWelcome},
		{"delete-branch-modal", setupScreenshotDeleteBranchModal},
	}
}

func TestUIScreenshots(t *testing.T) {
	if runtime.GOOS != "darwin" {
		if *updateScreenshots {
			t.Fatalf("UI screenshot baseline updates require darwin CoreText + SF Symbols rendering (current GOOS=%s)", runtime.GOOS)
		}
		t.Skipf("UI screenshot regression requires darwin CoreText + SF Symbols rendering (current GOOS=%s)", runtime.GOOS)
	}

	lockScreenshotEnv(t)

	if *updateScreenshots {
		runScreenshotUpdate(t)
		return
	}

	baseline := readScreenshotMeta(t)
	host := currentScreenshotHostMeta(t)
	if skip, reason := screenshotCrossVersionSkip(baseline, host, os.Getenv("BRANCHLINE_SCREENSHOT_STRICT") == "1"); skip {
		t.Skip(reason)
	}

	for _, state := range screenshotStates() {
		t.Run(state.name, func(t *testing.T) {
			for _, dark := range []bool{false, true} {
				theme := "light"
				if dark {
					theme = "dark"
				}
				t.Run(theme, func(t *testing.T) {
					h := prepareScreenshotState(t, state.setup)
					compareScreenshotState(t, h, state.name+"-"+theme, dark, baseline, host)
				})
			}
		})
	}
}

// screenshotCrossVersionSkip decides whether a host macOS major version may
// compare against baselines rendered on another one. Framework version
// differences never skip: upgrading mygo is one of the regressions this suite
// exists to catch.
func screenshotCrossVersionSkip(baseline screenshotMeta, host screenshotHostMeta, strict bool) (bool, string) {
	if baseline.MacOSMajor == host.MacOSMajor || strict {
		return false, ""
	}
	return true, fmt.Sprintf("UI screenshot baselines were rendered on macOS %d (%s build %s), host is macOS %d (%s build %s); set BRANCHLINE_SCREENSHOT_STRICT=1 to compare anyway or regenerate with go test -count=1 -run '^TestUIScreenshots$' ./internal/app -update", baseline.MacOSMajor, baseline.MacOSVersion, baseline.MacOSBuild, host.MacOSMajor, host.MacOSVersion, host.MacOSBuild)
}

// lockScreenshotEnv pins every process-wide input that can move pixels:
// locale, subprocess language, startup repository autoload, and the cached
// Go local timezone. It also clears the host's SSH askpass variables so
// promptless commands never reach a host helper; only the app's own AskPass
// env may set them. It deliberately forbids parallel execution because
// time.Local is process-wide.
func lockScreenshotEnv(t *testing.T) {
	t.Helper()
	oldLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = oldLocal })
	t.Setenv("TZ", "UTC")
	t.Setenv("LC_ALL", "C")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("BRANCHLINE_OPEN", "")
	t.Setenv("SSH_ASKPASS", "")
	t.Setenv("SSH_ASKPASS_REQUIRE", "")
	installScreenshotSFSymbolsScale(t, screenshotSFBackingScale)
}

// newSnapshotApp opens the ordinary test harness and fixes every desktop
// property that affects rasterization. Scale is applied immediately because
// text layout is scale-aware.
func newSnapshotApp(t *testing.T, repos ...store.Repo) *testHarness {
	t.Helper()
	h := newTestApp(t, repos...)
	configureSnapshotTester(t, h.ui)
	return h
}

func configureSnapshotTester(t *testing.T, tt *ui.Tester) {
	t.Helper()
	tt.SetSize(screenshotWidth, screenshotHeight)
	tt.SetScale(screenshotScale)
	tt.SetLocale("en-US")
	tt.SetDirection(ui.LeftToRight)
	tt.SetPreferences(ui.Preferences{})
	tt.SetWindowActive(true)
	tt.Frame()
}

// settleFrame settles animations at an exact caret blink phase. 1060ms is
// both far beyond the 160ms interface animations and exactly two 530ms caret
// half-periods, so the light and dark captures share the same caret phase.
func settleFrame(tt *ui.Tester) {
	tt.Frame()
	tt.Advance(1060 * time.Millisecond)
	tt.Frame()
}

// prepareScreenshotState drives one real product path into a screenshot
// state. The setup functions intentionally leave the tester in light mode;
// each leaf subtest then switches exactly once to its own theme.
func prepareScreenshotState(t *testing.T, setup func(*testing.T) *testHarness) *testHarness {
	t.Helper()
	h := setup(t)
	if h == nil {
		t.Fatal("screenshot setup returned no test harness")
	}
	return h
}

func screenshotWithinTolerance(stats screenshotCompareStats, maxRatio float64) bool {
	return stats.TotalPixels > 0 && stats.Ratio <= maxRatio
}

func compareScreenshotState(t *testing.T, h *testHarness, name string, dark bool, baseline screenshotMeta, host screenshotHostMeta) {
	t.Helper()
	tt := h.ui
	tt.SetDark(dark)
	settleFrame(tt)
	img := copyImage(tt.Image())
	tt.SetDark(false)
	tt.Frame()

	if got, want := img.Bounds().Size().X, screenshotWidth*screenshotScale; got != want {
		t.Fatalf("captured image width = %d, want %d", got, want)
	}
	if got, want := img.Bounds().Size().Y, screenshotHeight*screenshotScale; got != want {
		t.Fatalf("captured image height = %d, want %d", got, want)
	}

	goldenPath := filepath.Join(screenshotGoldenDir, name+".png")
	want, err := readPNG(goldenPath)
	if err != nil {
		artifactDir := writeScreenshotArtifacts(t, name, img, nil)
		t.Fatalf("missing or unreadable screenshot baseline %s: %v\nactual: %s\nafter inspecting, remove artifacts with: rm -rf %s", goldenPath, err, filepath.Join(artifactDir, name+"-actual.png"), artifactDir)
	}
	stats, diff := compareScreenshotImages(want, img, screenshotChanTol)
	if want.Bounds().Size() == img.Bounds().Size() && screenshotWithinTolerance(stats, screenshotMaxRatio) {
		return
	}

	artifactDir := writeScreenshotArtifacts(t, name, img, diff)
	expected, _ := filepath.Abs(goldenPath)
	actual := filepath.Join(artifactDir, name+"-actual.png")
	diffPath := filepath.Join(artifactDir, name+"-diff.png")
	if stats.TotalPixels == 0 {
		t.Fatalf("screenshot %s size mismatch: baseline %dx%d, actual %dx%d\nexpected: %s\nactual:   %s\nbaseline macOS %s build %s mygo %s; host macOS %s build %s mygo %s\nafter inspecting, remove artifacts with: rm -rf %s",
			name, want.Bounds().Dx(), want.Bounds().Dy(), img.Bounds().Dx(), img.Bounds().Dy(), expected, actual, baseline.MacOSVersion, baseline.MacOSBuild, baseline.MyGoVersion, host.MacOSVersion, host.MacOSBuild, host.MyGoVersion, artifactDir)
	}
	t.Fatalf("screenshot %s differs: %d/%d pixels (%.4f%% > %.4f%%), maxChannelDiff=%d channelTol=%d diffBounds=(%d,%d)-(%d,%d)\nexpected: %s\nactual:   %s\ndiff:     %s\nbaseline macOS %s build %s mygo %s; host macOS %s build %s mygo %s\nafter inspecting, remove artifacts with: rm -rf %s",
		name, stats.DiffPixels, stats.TotalPixels, 100*stats.Ratio, 100*screenshotMaxRatio, stats.MaxChannelDiff, screenshotChanTol,
		stats.Bounds.Min.X, stats.Bounds.Min.Y, stats.Bounds.Max.X, stats.Bounds.Max.Y,
		expected, actual, diffPath, baseline.MacOSVersion, baseline.MacOSBuild, baseline.MyGoVersion, host.MacOSVersion, host.MacOSBuild, host.MyGoVersion, artifactDir)
}

func readPNG(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	return cloneToRGBA(img), nil
}

func writePNG(path string, img *image.RGBA) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	encodeErr := enc.Encode(f, img)
	closeErr := f.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}

func copyImage(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	return dst
}

func cloneToRGBA(src image.Image) *image.RGBA {
	if rgba, ok := src.(*image.RGBA); ok {
		return copyImage(rgba)
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.SetRGBA(x, y, color.RGBAModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.RGBA))
		}
	}
	return dst
}

// compareScreenshotImages compares RGBA pixels with an L-infinity channel
// tolerance. It returns statistics and a diff image whose unchanged pixels
// are a dimmed grayscale copy of the baseline and whose failures are magenta.
func compareScreenshotImages(want, got *image.RGBA, channelTol uint8) (screenshotCompareStats, *image.RGBA) {
	if want == nil || got == nil || want.Bounds().Size() != got.Bounds().Size() {
		return screenshotCompareStats{}, nil
	}
	b := want.Bounds()
	w, h := b.Dx(), b.Dy()
	diff := image.NewRGBA(image.Rect(0, 0, w, h))
	stats := screenshotCompareStats{TotalPixels: w * h}
	first := true
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			wp := want.RGBAAt(b.Min.X+x, b.Min.Y+y)
			gp := got.RGBAAt(got.Bounds().Min.X+x, got.Bounds().Min.Y+y)
			mx := maxUint8(absDiffUint8(wp.R, gp.R), absDiffUint8(wp.G, gp.G), absDiffUint8(wp.B, gp.B), absDiffUint8(wp.A, gp.A))
			if mx > stats.MaxChannelDiff {
				stats.MaxChannelDiff = mx
			}
			if mx > channelTol {
				stats.DiffPixels++
				stats.HasDiff = true
				if first || x < stats.Bounds.Min.X {
					stats.Bounds.Min.X = x
				}
				if first || y < stats.Bounds.Min.Y {
					stats.Bounds.Min.Y = y
				}
				if first || x+1 > stats.Bounds.Max.X {
					stats.Bounds.Max.X = x + 1
				}
				if first || y+1 > stats.Bounds.Max.Y {
					stats.Bounds.Max.Y = y + 1
				}
				first = false
				diff.SetRGBA(x, y, color.RGBA{R: 255, G: 0, B: 96, A: 255})
			} else {
				luma := uint8((299*int(wp.R) + 587*int(wp.G) + 114*int(wp.B)) / 4000)
				diff.SetRGBA(x, y, color.RGBA{R: luma, G: luma, B: luma, A: 255})
			}
		}
	}
	if stats.TotalPixels > 0 {
		stats.Ratio = float64(stats.DiffPixels) / float64(stats.TotalPixels)
	}
	if first {
		stats.Bounds = image.Rectangle{}
	}
	return stats, diff
}

func absDiffUint8(a, b uint8) uint8 {
	if a > b {
		return a - b
	}
	return b - a
}

func maxUint8(vals ...uint8) uint8 {
	m := vals[0]
	for _, v := range vals[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func screenshotArtifactsEnabled(lookup func(string) *flag.Flag) bool {
	if lookup == nil {
		return false
	}
	artifacts := lookup("test.artifacts")
	return artifacts != nil && artifacts.Value.String() == "true"
}

// writeScreenshotArtifacts writes only failure artifacts. With Go 1.27's
// -artifacts they stay in t.ArtifactDir(); in ordinary go test runs they fall
// back to a visible /tmp/branchline-test-golden-* directory because ArtifactDir
// would be deleted before a developer could inspect it.
func writeScreenshotArtifacts(t *testing.T, name string, actual, diff *image.RGBA) string {
	t.Helper()
	return writeScreenshotArtifactsMode(t, screenshotArtifactsEnabled(flag.Lookup), name, actual, diff)
}

func writeScreenshotArtifactsMode(t *testing.T, useArtifacts bool, name string, actual, diff *image.RGBA) string {
	t.Helper()
	var dir string
	if useArtifacts {
		dir = t.ArtifactDir()
	} else {
		var err error
		dir, err = os.MkdirTemp("/tmp", "branchline-test-golden-")
		if err != nil {
			t.Fatalf("create screenshot artifact directory: %v", err)
		}
	}
	if actual != nil {
		if err := writePNG(filepath.Join(dir, name+"-actual.png"), actual); err != nil {
			t.Fatalf("write actual screenshot artifact: %v", err)
		}
	}
	if diff != nil {
		if err := writePNG(filepath.Join(dir, name+"-diff.png"), diff); err != nil {
			t.Fatalf("write diff screenshot artifact: %v", err)
		}
	}
	return dir
}

func readScreenshotMeta(t *testing.T) screenshotMeta {
	t.Helper()
	path := filepath.Join(screenshotGoldenDir, screenshotMetaFile)
	meta, err := readScreenshotMetaFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return meta
}

func readScreenshotMetaFile(path string) (screenshotMeta, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return screenshotMeta{}, fmt.Errorf("read screenshot metadata %s: %w", path, err)
	}
	var meta screenshotMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return screenshotMeta{}, fmt.Errorf("parse screenshot metadata %s: %w", path, err)
	}
	if err := validateScreenshotMeta(meta); err != nil {
		return screenshotMeta{}, fmt.Errorf("screenshot metadata %s is incomplete or inconsistent: %w", path, err)
	}
	return meta, nil
}

func validateScreenshotMeta(meta screenshotMeta) error {
	if meta.GOOS != "darwin" {
		return fmt.Errorf("goos = %q, want darwin", meta.GOOS)
	}
	if meta.GOARCH == "" {
		return fmt.Errorf("goarch is empty")
	}
	if meta.MacOSMajor <= 0 || meta.MacOSVersion == "" || meta.MacOSBuild == "" {
		return fmt.Errorf("incomplete macOS identity: %+v", meta)
	}
	if major := macOSMajor(meta.MacOSVersion); major != meta.MacOSMajor {
		return fmt.Errorf("macos_major = %d does not match macos_version %q (%d)", meta.MacOSMajor, meta.MacOSVersion, major)
	}
	if meta.Width != screenshotWidth || meta.Height != screenshotHeight || meta.Scale != screenshotScale {
		return fmt.Errorf("geometry = %dx%d@%d, want %dx%d@%d", meta.Width, meta.Height, meta.Scale, screenshotWidth, screenshotHeight, screenshotScale)
	}
	if meta.MyGoVersion == "" {
		return fmt.Errorf("mygo_version is empty")
	}
	return nil
}

func currentScreenshotHostMeta(t *testing.T) screenshotHostMeta {
	t.Helper()
	version, build := swVers(t)
	return screenshotHostMeta{
		MacOSVersion: version,
		MacOSMajor:   macOSMajor(version),
		MacOSBuild:   build,
		MyGoVersion:  currentMyGoVersion(),
	}
}

func swVers(t *testing.T) (version, build string) {
	t.Helper()
	product, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		t.Fatalf("sw_vers -productVersion: %v", err)
	}
	buildOut, err := exec.Command("sw_vers", "-buildVersion").Output()
	if err != nil {
		t.Fatalf("sw_vers -buildVersion: %v", err)
	}
	return strings.TrimSpace(string(product)), strings.TrimSpace(string(buildOut))
}

func macOSMajor(version string) int {
	head, _, _ := strings.Cut(version, ".")
	n, _ := strconv.Atoi(head)
	return n
}

func currentMyGoVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range info.Deps {
		if dep == nil {
			continue
		}
		if dep.Path == "github.com/egoist/mygo" {
			if dep.Replace != nil && dep.Replace.Version != "" {
				return dep.Replace.Version
			}
			return dep.Version
		}
	}
	return "unknown"
}

// runScreenshotUpdate regenerates every baseline through the same scene
// preparation used by comparison, then writes meta.json. It exists for
// intentional visual changes only, never to silence an unexpected failure.
func runScreenshotUpdate(t *testing.T) {
	for _, state := range screenshotStates() {
		t.Run(state.name, func(t *testing.T) {
			for _, dark := range []bool{false, true} {
				theme := "light"
				if dark {
					theme = "dark"
				}
				t.Run(theme, func(t *testing.T) {
					h := prepareScreenshotState(t, state.setup)
					tt := h.ui
					tt.SetDark(dark)
					settleFrame(tt)
					img := copyImage(tt.Image())
					tt.SetDark(false)
					tt.Frame()
					path := filepath.Join(screenshotGoldenDir, state.name+"-"+theme+".png")
					if err := writePNG(path, img); err != nil {
						t.Fatalf("write screenshot baseline %s: %v", path, err)
					}
				})
			}
		})
	}
	if t.Failed() {
		return
	}
	version, build := swVers(t)
	meta := screenshotMeta{
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		MacOSVersion: version,
		MacOSMajor:   macOSMajor(version),
		MacOSBuild:   build,
		MyGoVersion:  currentMyGoVersion(),
		Width:        screenshotWidth,
		Height:       screenshotHeight,
		Scale:        screenshotScale,
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("encode screenshot metadata: %v", err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(filepath.Join(screenshotGoldenDir, screenshotMetaFile), b, 0o644); err != nil {
		t.Fatalf("write screenshot metadata: %v", err)
	}
}

// newDetGitRepo makes a fixture whose authors, dates, hooks and signing
// configuration are frozen, so commit hashes and rendered metadata do not
// depend on the machine or the day the test runs.
func newDetGitRepo(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	r := newGitRepo(t, name)
	r.Git("config", "user.name", "Ada Lovelace")
	r.Git("config", "user.email", "ada@branchline.test")
	r.Git("config", "commit.gpgsign", "false")
	r.Git("config", "tag.gpgsign", "false")
	r.Git("config", "core.autocrlf", "false")
	r.Git("config", "core.hooksPath", "/dev/null")
	return r
}

func (f *gitRepoFixture) gitAt(commitTime time.Time, args ...string) string {
	f.t.Helper()
	stamp := commitTime.UTC().Format(time.RFC3339)
	f.t.Setenv("GIT_AUTHOR_DATE", stamp)
	f.t.Setenv("GIT_COMMITTER_DATE", stamp)
	return f.Git(args...)
}

func detCommitFile(t *testing.T, r *gitRepoFixture, commitTime time.Time, relPath, content, message string) string {
	t.Helper()
	r.WriteFile(relPath, content)
	r.Git("add", "--", relPath)
	r.gitAt(commitTime, "commit", "-q", "-m", message)
	return strings.TrimSpace(r.Git("rev-parse", "HEAD"))
}

// buildRichRepo builds the deterministic multi-lane repository used by the
// screenshots: branches feature, release/v1 and experiment, a remote-tracking
// origin/main, tag v1.0.0 and a no-ff merge with a second parent.
func buildRichRepo(t *testing.T, name string) (*gitRepoFixture, string) {
	t.Helper()
	r := newDetGitRepo(t, name)
	base := screenshotFixtureTime
	root := detCommitFile(t, r, base, "README.md", "# Branchline fixture\n\nA deterministic repository for UI screenshots.\n", "Initial project skeleton")
	detCommitFile(t, r, base.Add(time.Hour), "main.go", "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n", "Add main program")
	r.CreateTag("v1.0.0")

	r.Git("checkout", "-q", "-b", "feature")
	featureHash := detCommitFile(t, r, base.Add(2*time.Hour), "greeter.go", "package main\n\nfunc greet(name string) string {\n\treturn \"hello, \" + name\n}\n", "Add greeting helper")
	detCommitFile(t, r, base.Add(3*time.Hour), "greeter.go", "package main\n\nfunc greet(name string) string {\n\tif name == \"\" {\n\t\treturn \"hello\"\n\t}\n\treturn \"hello, \" + name\n}\n", "Handle empty names in greeting")

	r.Git("checkout", "-q", "main")
	detCommitFile(t, r, base.Add(4*time.Hour), "main.go", "package main\n\nfunc main() {\n\tprintln(greet(\"branchline\"))\n}\n", "Use greeting helper in main")
	r.gitAt(base.Add(5*time.Hour), "merge", "--no-ff", "--no-edit", "feature")
	r.CreateBranch("release/v1")

	r.Git("checkout", "-q", "-b", "experiment", root)
	detCommitFile(t, r, base.Add(6*time.Hour), "experiment.txt", "unmerged experiment\n", "Try experiment")
	r.Git("checkout", "-q", "main")

	bare := r.AddBareRemote("origin")
	_ = bare
	return r, featureHash
}

func mustSnapshotClick(t *testing.T, h *testHarness, label string) {
	t.Helper()
	if err := h.ui.Click(label); err != nil {
		t.Fatalf("click %q: %v\ntexts=%q", label, err, h.ui.Texts())
	}
}

func setupScreenshotChangesDiff(t *testing.T) *testHarness {
	t.Helper()
	r, _ := buildRichRepo(t, "branchline")
	r.WriteFile("README.md", "# Branchline fixture\n\nA deterministic repository for UI screenshots.\n\nStaged documentation refinement.\n")
	r.Git("add", "--", "README.md")
	r.WriteFile("main.go", "package main\n\nfunc main() {\n\tprintln(greet(\"branchline\"))\n\tprintln(\"unstaged edit\")\n}\n")
	r.WriteFile("notes.txt", "notes for the next commit\n")

	h := newSnapshotApp(t)
	h.openRepo(r.StoreRepo())
	h.drain()
	mustSnapshotClick(t, h, "Unstaged main.go")
	h.drain()
	if !h.ui.HasText("Unstaged") || !h.ui.HasText("Staged") {
		t.Fatalf("changes-diff state missing file sections; texts=%q", h.ui.Texts())
	}
	return h
}

func setupScreenshotHistoryDetail(t *testing.T) *testHarness {
	t.Helper()
	r, _ := buildRichRepo(t, "branchline")
	// The HEAD merge commit carries every ref badge the commit list renders —
	// the hollow HEAD dot, HEAD → main, origin/main and release/v1 — except a
	// tag, so this scene alone adds one to it. Selecting that row keeps the
	// badges, the selection fill and the graph dot inside the baseline, where
	// a contrast regression can no longer hide behind an unselected row.
	r.CreateTag("v1.1.0")
	headHash := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	h := newSnapshotApp(t)
	h.openRepo(r.StoreRepo())
	h.drain()
	mustSnapshotClick(t, h, "History")
	h.drain()
	mustSnapshotClick(t, h, commitRowLabel(headHash, "Merge branch 'feature'"))
	h.drain()
	for _, want := range []string{"Merge branch 'feature'", "HEAD → main", "origin/main", "v1.1.0"} {
		if !h.ui.HasText(want) {
			t.Fatalf("history-detail state missing %q; texts=%q", want, h.ui.Texts())
		}
	}
	return h
}

func setupScreenshotRefsSidebar(t *testing.T) *testHarness {
	t.Helper()
	r, _ := buildRichRepo(t, "branchline")
	h := newSnapshotApp(t)
	h.openRepo(r.StoreRepo())
	h.drain()
	rect, ok := h.ui.Find("feature")
	if !ok {
		t.Fatalf("feature branch row missing; texts=%q", h.ui.Texts())
	}
	h.ui.Move(rect.X+rect.W/2, rect.Y+rect.H/2)
	h.ui.Frame()
	return h
}

// prepareSeededStore writes repositories.json before store.Open so Repos()
// ordering is fixed by timestamps rather than wall-clock Add ordering.
func prepareSeededStore(t *testing.T, repos []store.Repo) string {
	t.Helper()
	dir := t.TempDir()
	type seedFile struct {
		Repos []store.Repo `json:"repos"`
	}
	b, err := json.Marshal(seedFile{Repos: repos})
	if err != nil {
		t.Fatalf("encode seeded repositories.json: %v", err)
	}
	path := filepath.Join(dir, "repositories.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write seeded repositories.json: %v", err)
	}
	return path
}

func snapshotSSHRepo(t *testing.T, address, name string) store.Repo {
	t.Helper()
	r, err := store.ParseAddress(address)
	if err != nil {
		t.Fatalf("parse fixed SSH fixture address %q: %v", address, err)
	}
	r.Name = name
	return r
}

func setupScreenshotRepoSwitcher(t *testing.T) *testHarness {
	t.Helper()
	localMain, _ := buildRichRepo(t, "branchline")
	localDocs := newDetGitRepo(t, "docs-site")
	detCommitFile(t, localDocs, screenshotFixtureTime, "index.md", "# Docs\n", "Initial docs")
	infra := snapshotSSHRepo(t, "alice@git.example.test:/srv/infra", "infra")

	mainMeta := localMain.StoreRepo()
	mainMeta.ID = "branchline-local-id"
	mainMeta.Opened = time.Date(2025, 5, 15, 12, 0, 0, 0, time.UTC)
	infra.ID = "infra-ssh-id"
	infra.Opened = time.Date(2025, 5, 15, 11, 0, 0, 0, time.UTC)
	docsMeta := localDocs.StoreRepo()
	docsMeta.ID = "docs-site-local-id"
	docsMeta.Opened = time.Date(2025, 5, 15, 10, 0, 0, 0, time.UTC)
	seededStorePath := prepareSeededStore(t, []store.Repo{mainMeta, infra, docsMeta})

	h := newSnapshotAppSeeded(t, seededStorePath)
	// Open the main repository through the session alone: app.openRepo and
	// store.Add would rewrite mainMeta.Opened to the wall clock, which both
	// reorders the switcher and makes the capture depend on when it runs.
	// activeRepoID reads the session meta, so the switcher's check mark and
	// title render exactly as the store-backed path renders them.
	h.app.session.Open(mainMeta)
	h.ui.Frame()
	h.drain()
	repos := h.store.Repos()
	gotNames := []string{}
	for _, r := range repos {
		gotNames = append(gotNames, r.Name)
	}
	wantNames := []string{"branchline", "infra", "docs-site"}
	if strings.Join(gotNames, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("seeded repository order = %v, want %v", gotNames, wantNames)
	}
	wantOpened := []time.Time{mainMeta.Opened, infra.Opened, docsMeta.Opened}
	for i, r := range repos {
		if !r.Opened.Equal(wantOpened[i]) {
			t.Fatalf("repository %q Opened = %v, want untouched preset %v (wall clock leaked into the store)", r.Name, r.Opened, wantOpened[i])
		}
	}
	mustSnapshotClick(t, h, "Repository Switcher")
	return h
}

// newSnapshotAppSeeded mirrors newSnapshotApp but opens the JSON prepared by
// prepareSeededStore instead of adding repositories through time.Now().
func newSnapshotAppSeeded(t *testing.T, path string) *testHarness {
	t.Helper()
	if path == "" {
		t.Fatal("prepareSeededStore returned an empty path")
	}
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("open seeded store: %v", err)
	}
	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)
	a := newApp(s)
	a.postFn = q.post
	a.spawnFn = q.spawn
	h := &testHarness{t: t, app: a, store: s, ui: ui.NewTester(a.view, screenshotWidth, screenshotHeight), queue: q}
	configureSnapshotTester(t, h.ui)
	return h
}

func setupScreenshotAddSSHModal(t *testing.T) *testHarness {
	t.Helper()
	r, _ := buildRichRepo(t, "branchline")
	h := newSnapshotApp(t)
	h.openRepo(r.StoreRepo())
	h.drain()
	mustSnapshotClick(t, h, "Repository Switcher")
	h.ui.Frame()
	mustSnapshotClick(t, h, "Add SSH Repository…")
	h.ui.Frame()
	h.ui.Type("alice@git.example.test:/srv/branchline")
	h.ui.Frame()
	if !h.ui.HasText("Branchline runs git on the server") {
		t.Fatalf("add-ssh-modal missing Branchline explanatory copy; texts=%q", h.ui.Texts())
	}
	return h
}

func setupScreenshotSyncErrorBanner(t *testing.T) *testHarness {
	t.Helper()
	r := newDetGitRepo(t, "branchline")
	detCommitFile(t, r, screenshotFixtureTime, "README.md", "# Branchline\n", "Initial commit")
	h := newSnapshotApp(t)
	closeSync(t, h)
	h.openRepo(r.StoreRepo())
	h.drain()
	mustSnapshotClick(t, h, "Push")
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("Repository Error missing after no-remote Push; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("The repository has no remote to push to.") {
		t.Fatalf("sync error copy missing; texts=%q", h.ui.Texts())
	}
	return h
}

// askpassFixtureWrapper is an ssh stand-in that fails promptless invocations
// at once and invokes the real production "$SSH_ASKPASS" helper with a fixed
// prompt when SSH_ASKPASS_REQUIRE=force, without dialing any host.
func askpassFixtureWrapper(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
case "$SSH_ASKPASS_REQUIRE" in
force) : interactive ;;
*) echo "Permission denied (publickey,password)." >&2; exit 255 ;;
esac
"$SSH_ASKPASS" "alice@git.example.test's password:" >/dev/null || { echo "Permission denied, please try again." >&2; exit 255; }
echo "Permission denied, please try again." >&2
exit 255
`
	bin := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write askpass ssh fixture wrapper: %v", err)
	}
	return filepath.Dir(bin)
}

func setupScreenshotAskPassDialog(t *testing.T) *testHarness {
	t.Helper()
	gateDir := askpassFixtureWrapper(t)
	t.Setenv("PATH", gateDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	sshRepo := snapshotSSHRepo(t, "alice@git.example.test:/srv/branchline", "branchline")
	h := newSnapshotApp(t)
	closeSync(t, h)
	t.Cleanup(func() {
		h.ui.Frame()
		if _, ok := h.ui.Find("Cancel"); ok {
			if err := h.ui.Click("Cancel"); err != nil {
				t.Logf("cleanup click Cancel on askpass dialog: %v", err)
			}
			h.ui.Frame()
			h.drain()
		}
	})
	h.openRepo(sshRepo)
	// Do not drain the initial promptless load into its terminal error:
	// clicking Fetch while that load is still serialized ahead of it lets the
	// real Fetch worker replace the opening failure with the password request
	// and the busy badge. waitScreenshotAskPass consumes the intermediate UI
	// callbacks until the modal is rendered.
	// Option+click keeps the direct fetch a plain click ran before the Fetch
	// dialog, so the askpass flow this scenario drives is unchanged.
	if err := h.ui.ClickWith(ui.Alt, "Fetch"); err != nil {
		t.Fatalf("Option+click Fetch: %v", err)
	}
	h.ui.Frame()
	waitScreenshotAskPass(t, h)
	if !h.ui.HasText("The remote host requires password") || !h.ui.HasText("alice@git.example.test's password:") {
		t.Fatalf("askpass dialog missing fixed title or prompt; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Fetching…") {
		t.Fatalf("askpass background toolbar missing busy badge; texts=%q", h.ui.Texts())
	}
	return h
}

// waitScreenshotAskPass consumes UI callbacks until the askpass modal is
// visible. Unlike waitAskPass it tolerates the initial snapshot completion
// callback that can be queued ahead of the Fetch worker's wake-up.
func waitScreenshotAskPass(t *testing.T, h *testHarness) {
	t.Helper()
	for i := 0; i < 20; i++ {
		h.ui.Frame()
		if _, ok := h.ui.Find("Cancel"); ok {
			return
		}
		h.queue.RunNext(t)
	}
	h.ui.Frame()
	if _, ok := h.ui.Find("Cancel"); !ok {
		t.Fatalf("askpass dialog did not render; texts=%q", h.ui.Texts())
	}
}

func setupScreenshotEmptyRepoUnborn(t *testing.T) *testHarness {
	t.Helper()
	r := newDetGitRepo(t, "empty-repo")
	r.WriteFile("README.md", "# Empty repo\n")
	h := newSnapshotApp(t)
	h.openRepo(r.StoreRepo())
	h.drain()
	for _, want := range []string{"No branches", "No remotes", "No tags"} {
		if !h.ui.HasText(want) {
			t.Fatalf("empty repository sidebar missing %q; texts=%q", want, h.ui.Texts())
		}
	}
	return h
}

func setupScreenshotNoRepoWelcome(t *testing.T) *testHarness {
	t.Helper()
	h := newSnapshotApp(t)
	h.app.openStartupRepo()
	h.ui.Frame()
	if !h.ui.HasText("No Repository Open") || !h.ui.HasText("No Repository") {
		t.Fatalf("welcome state missing expected empty copy; texts=%q", h.ui.Texts())
	}
	return h
}

func setupScreenshotDeleteBranchModal(t *testing.T) *testHarness {
	t.Helper()
	r, _ := buildRichRepo(t, "branchline")
	h := newSnapshotApp(t)
	h.openRepo(r.StoreRepo())
	h.drain()
	mustSnapshotClick(t, h, "Delete branch experiment")
	h.drain()
	if !h.ui.HasText("This branch has commits not merged") {
		t.Fatalf("delete modal missing unmerged warning; texts=%q", h.ui.Texts())
	}
	return h
}

// TestScreenshotGoldenBudget guards the repository-size budget independent of
// whether the visual comparison is skipped on another macOS major version.
func TestScreenshotGoldenBudget(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("UI screenshot regression requires darwin CoreText + SF Symbols rendering (current GOOS=%s)", runtime.GOOS)
	}
	entries, err := filepath.Glob(filepath.Join(screenshotGoldenDir, "*.png"))
	if err != nil {
		t.Fatalf("glob screenshot baselines: %v", err)
	}
	if len(entries) != 20 {
		t.Fatalf("screenshot baseline PNG count = %d, want 20", len(entries))
	}
	var total int64
	for _, path := range entries {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat screenshot baseline %s: %v", path, err)
		}
		total += info.Size()
	}
	if meta, err := os.Stat(filepath.Join(screenshotGoldenDir, screenshotMetaFile)); err == nil {
		total += meta.Size()
	}
	if total > screenshotMaxSizeKiB*1024 {
		t.Fatalf("screenshot baseline directory size = %d KiB, want <= %d KiB", total/1024, screenshotMaxSizeKiB)
	}
}

func TestScreenshotHostMetaUsesSwVersShape(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("UI screenshot regression requires darwin (current GOOS=%s)", runtime.GOOS)
	}
	host := currentScreenshotHostMeta(t)
	if host.MacOSMajor == 0 || host.MacOSVersion == "" || host.MacOSBuild == "" {
		t.Fatalf("host screenshot metadata incomplete: %+v", host)
	}
	if host.MyGoVersion == "" {
		t.Fatal("host mygo version empty")
	}
}

func TestScreenshotCompareImages(t *testing.T) {
	mk := func(w, h int, fill color.RGBA) *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.SetRGBA(x, y, fill)
			}
		}
		return img
	}
	base := mk(8, 6, color.RGBA{R: 100, G: 120, B: 140, A: 255})

	t.Run("identical", func(t *testing.T) {
		stats, diff := compareScreenshotImages(base, copyImage(base), screenshotChanTol)
		if stats.DiffPixels != 0 || stats.MaxChannelDiff != 0 || stats.HasDiff {
			t.Fatalf("identical stats = %+v, want zero diff", stats)
		}
		if diff == nil {
			t.Fatal("expected a diff image for identical inputs")
		}
	})

	t.Run("within channel tolerance", func(t *testing.T) {
		got := copyImage(base)
		c := got.RGBAAt(2, 2)
		c.R += screenshotChanTol
		got.SetRGBA(2, 2, c)
		stats, _ := compareScreenshotImages(base, got, screenshotChanTol)
		if stats.DiffPixels != 0 {
			t.Fatalf("within-tolerance diff pixels = %d, want 0", stats.DiffPixels)
		}
		if stats.MaxChannelDiff != screenshotChanTol {
			t.Fatalf("within-tolerance maxChannelDiff = %d, want %d", stats.MaxChannelDiff, screenshotChanTol)
		}
	})

	t.Run("beyond channel tolerance marks bounds and magenta", func(t *testing.T) {
		got := copyImage(base)
		c := got.RGBAAt(3, 4)
		c.R += screenshotChanTol + 1
		got.SetRGBA(3, 4, c)
		stats, diff := compareScreenshotImages(base, got, screenshotChanTol)
		if stats.DiffPixels != 1 || stats.Ratio != 1.0/48.0 {
			t.Fatalf("beyond-tolerance stats = %+v, want one differing pixel", stats)
		}
		if stats.Bounds != image.Rect(3, 4, 4, 5) {
			t.Fatalf("diff bounds = %v, want (3,4)-(4,5)", stats.Bounds)
		}
		if got := diff.RGBAAt(3, 4); got != (color.RGBA{R: 255, G: 0, B: 96, A: 255}) {
			t.Fatalf("diff pixel color = %+v, want magenta", got)
		}
		if unchanged := diff.RGBAAt(0, 0); unchanged != (color.RGBA{R: 29, G: 29, B: 29, A: 255}) {
			t.Fatalf("unchanged diff pixel = %+v, want 25%% grayscale {29 29 29 255}", unchanged)
		}
	})

	t.Run("alpha channel uses same L-infinity tolerance", func(t *testing.T) {
		within := copyImage(base)
		c := within.RGBAAt(1, 1)
		c.A -= screenshotChanTol
		within.SetRGBA(1, 1, c)
		stats, _ := compareScreenshotImages(base, within, screenshotChanTol)
		if stats.DiffPixels != 0 || stats.MaxChannelDiff != screenshotChanTol {
			t.Fatalf("within-alpha stats = %+v, want 0 diff pixels and maxChannelDiff %d", stats, screenshotChanTol)
		}

		beyond := copyImage(base)
		c = beyond.RGBAAt(1, 1)
		c.A -= screenshotChanTol + 1
		beyond.SetRGBA(1, 1, c)
		stats, _ = compareScreenshotImages(base, beyond, screenshotChanTol)
		if stats.DiffPixels != 1 || stats.MaxChannelDiff != screenshotChanTol+1 {
			t.Fatalf("beyond-alpha stats = %+v, want 1 diff pixel and maxChannelDiff %d", stats, screenshotChanTol+1)
		}
	})

	t.Run("size mismatch produces no pseudo diff", func(t *testing.T) {
		other := mk(4, 4, color.RGBA{A: 255})
		stats, diff := compareScreenshotImages(base, other, screenshotChanTol)
		if stats != (screenshotCompareStats{}) || diff != nil {
			t.Fatalf("size mismatch stats=%+v diff=%v, want zero stats and nil diff", stats, diff != nil)
		}
		if screenshotWithinTolerance(stats, screenshotMaxRatio) {
			t.Fatal("size mismatch stats should never pass screenshotWithinTolerance")
		}
	})
}

func TestScreenshotArtifactModes(t *testing.T) {
	sample := image.NewRGBA(image.Rect(0, 0, 4, 3))
	sample.SetRGBA(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	diffImg := image.NewRGBA(image.Rect(0, 0, 4, 3))
	diffImg.SetRGBA(0, 0, color.RGBA{R: 255, G: 0, B: 96, A: 255})
	checkArtifactPNG := func(t *testing.T, path string, want *image.RGBA) {
		t.Helper()
		img, err := readPNG(path)
		if err != nil {
			t.Fatalf("read artifact PNG %s: %v", filepath.Base(path), err)
		}
		if got := img.Bounds().Size(); got != image.Pt(4, 3) {
			t.Fatalf("artifact PNG %s size = %v, want (4,3)", filepath.Base(path), got)
		}
		if got, wantPx := img.RGBAAt(0, 0), want.RGBAAt(0, 0); got != wantPx {
			t.Fatalf("artifact PNG %s pixel (0,0) = %v, want %v", filepath.Base(path), got, wantPx)
		}
	}

	t.Run("flag detection", func(t *testing.T) {
		if screenshotArtifactsEnabled(nil) {
			t.Fatal("nil lookup reported artifacts enabled")
		}
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		if screenshotArtifactsEnabled(fs.Lookup) {
			t.Fatal("unregistered test.artifacts flag reported enabled")
		}
		fs.Bool("test.artifacts", false, "")
		if screenshotArtifactsEnabled(fs.Lookup) {
			t.Fatal("false test.artifacts flag reported enabled")
		}
		if err := fs.Set("test.artifacts", "true"); err != nil {
			t.Fatalf("set test.artifacts: %v", err)
		}
		if !screenshotArtifactsEnabled(fs.Lookup) {
			t.Fatal("true test.artifacts flag reported disabled")
		}
	})

	t.Run("default fallback mode writes under /tmp/branchline-test-golden-*", func(t *testing.T) {
		dir := writeScreenshotArtifactsMode(t, false, "sample-case", sample, diffImg)
		t.Cleanup(func() { _ = os.RemoveAll(dir) })

		if filepath.Dir(dir) != "/tmp" || !strings.HasPrefix(filepath.Base(dir), "branchline-test-golden-") {
			t.Fatalf("default artifact dir = %q, want /tmp/branchline-test-golden-*", dir)
		}
		checkArtifactPNG(t, filepath.Join(dir, "sample-case-actual.png"), sample)
		checkArtifactPNG(t, filepath.Join(dir, "sample-case-diff.png"), diffImg)
	})

	t.Run("default fallback omits diff when nil", func(t *testing.T) {
		dir := writeScreenshotArtifactsMode(t, false, "size-mismatch", sample, nil)
		t.Cleanup(func() { _ = os.RemoveAll(dir) })

		if _, err := os.Stat(filepath.Join(dir, "size-mismatch-actual.png")); err != nil {
			t.Fatalf("missing actual PNG: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "size-mismatch-diff.png")); !os.IsNotExist(err) {
			t.Fatalf("unexpected diff PNG when diff is nil: err=%v", err)
		}
	})

	t.Run("artifacts mode writes into t.ArtifactDir", func(t *testing.T) {
		dir := writeScreenshotArtifactsMode(t, true, "artifacts-case", sample, diffImg)
		if dir != t.ArtifactDir() {
			t.Fatalf("artifacts mode dir = %q, want t.ArtifactDir() %q", dir, t.ArtifactDir())
		}
		checkArtifactPNG(t, filepath.Join(dir, "artifacts-case-actual.png"), sample)
		checkArtifactPNG(t, filepath.Join(dir, "artifacts-case-diff.png"), diffImg)
	})
}

func TestScreenshotCrossVersionPolicy(t *testing.T) {
	baseline := screenshotMeta{MacOSMajor: 27, MacOSVersion: "27.2.1", MacOSBuild: "26B131", MyGoVersion: "v0.10.0"}
	host := screenshotHostMeta{MacOSMajor: 27, MacOSVersion: "27.3", MacOSBuild: "26C1", MyGoVersion: "v0.11.0"}
	if skip, reason := screenshotCrossVersionSkip(baseline, host, false); skip || reason != "" {
		t.Fatalf("same-major comparison skipped (%v, %q), want comparison even when mygo differs", skip, reason)
	}

	host.MacOSMajor = 28
	host.MacOSVersion = "28.0"
	skip, reason := screenshotCrossVersionSkip(baseline, host, false)
	if !skip {
		t.Fatal("cross-major comparison did not skip by default")
	}
	for _, part := range []string{"macOS 27", "27.2.1", "26B131", "macOS 28", "28.0", "BRANCHLINE_SCREENSHOT_STRICT=1", "-update"} {
		if !strings.Contains(reason, part) {
			t.Fatalf("cross-major skip reason missing %q: %q", part, reason)
		}
	}
	if skip, _ := screenshotCrossVersionSkip(baseline, host, true); skip {
		t.Fatal("strict mode still skipped a cross-major comparison")
	}
}

func TestScreenshotComparatorRatioBoundary(t *testing.T) {
	const w, h = 100, 100
	want := image.NewRGBA(image.Rect(0, 0, w, h))
	got := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{R: 80, G: 90, B: 100, A: 255}
			want.SetRGBA(x, y, c)
			got.SetRGBA(x, y, c)
		}
	}
	// Two pixels in a 10,000-pixel image are exactly the configured 0.02%
	// boundary and must pass; the third pixel must tip the ratio over it.
	for i := 0; i < 2; i++ {
		c := got.RGBAAt(i, 0)
		c.R += screenshotChanTol + 1
		got.SetRGBA(i, 0, c)
	}
	stats, _ := compareScreenshotImages(want, got, screenshotChanTol)
	if stats.DiffPixels != 2 || stats.Ratio != screenshotMaxRatio || !screenshotWithinTolerance(stats, screenshotMaxRatio) {
		t.Fatalf("boundary stats = %+v, want exactly two pixels passing ratio %v", stats, screenshotMaxRatio)
	}
	c := got.RGBAAt(2, 0)
	c.R += screenshotChanTol + 1
	got.SetRGBA(2, 0, c)
	stats, _ = compareScreenshotImages(want, got, screenshotChanTol)
	if stats.DiffPixels != 3 || stats.Ratio <= screenshotMaxRatio || screenshotWithinTolerance(stats, screenshotMaxRatio) {
		t.Fatalf("over-boundary stats = %+v, want ratio above %v to fail", stats, screenshotMaxRatio)
	}
}

func TestValidateScreenshotMeta(t *testing.T) {
	valid := screenshotMeta{
		GOOS:         "darwin",
		GOARCH:       "arm64",
		MacOSVersion: "27.2.1",
		MacOSMajor:   27,
		MacOSBuild:   "26B131",
		MyGoVersion:  "v0.10.0",
		Width:        screenshotWidth,
		Height:       screenshotHeight,
		Scale:        screenshotScale,
	}
	if err := validateScreenshotMeta(valid); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*screenshotMeta)
	}{
		{"wrong goos", func(m *screenshotMeta) { m.GOOS = "linux" }},
		{"missing goarch", func(m *screenshotMeta) { m.GOARCH = "" }},
		{"missing macos major", func(m *screenshotMeta) { m.MacOSMajor = 0 }},
		{"inconsistent macos major", func(m *screenshotMeta) { m.MacOSMajor = 26 }},
		{"missing macos version", func(m *screenshotMeta) { m.MacOSVersion = "" }},
		{"missing macos build", func(m *screenshotMeta) { m.MacOSBuild = "" }},
		{"wrong width", func(m *screenshotMeta) { m.Width = screenshotWidth + 1 }},
		{"wrong height", func(m *screenshotMeta) { m.Height = screenshotHeight + 1 }},
		{"wrong scale", func(m *screenshotMeta) { m.Scale = 1 }},
		{"missing mygo", func(m *screenshotMeta) { m.MyGoVersion = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := valid
			tc.mutate(&m)
			if err := validateScreenshotMeta(m); err == nil {
				t.Fatalf("invalid metadata accepted: %+v", m)
			}
		})
	}
}

func TestReadScreenshotMetaFileErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := readScreenshotMetaFile(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing metadata file succeeded, want error")
	}
	badPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badPath, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write malformed metadata: %v", err)
	}
	if _, err := readScreenshotMetaFile(badPath); err == nil {
		t.Fatal("malformed metadata JSON succeeded, want error")
	}
	inconsistentPath := filepath.Join(dir, "inconsistent.json")
	inconsistent := `{"goos":"darwin","goarch":"arm64","macos_version":"27.2.1","macos_major":26,"macos_build":"26B131","mygo_version":"v0.10.0","width":1200,"height":800,"scale":2}`
	if err := os.WriteFile(inconsistentPath, []byte(inconsistent), 0o600); err != nil {
		t.Fatalf("write inconsistent metadata: %v", err)
	}
	if _, err := readScreenshotMetaFile(inconsistentPath); err == nil {
		t.Fatal("metadata with macos_major inconsistent with macos_version succeeded, want error")
	}
}
