//go:build !darwin

package app

import "testing"

// screenshotSFBackingScale mirrors the darwin constant so screenshot_test.go
// compiles on every platform; scale pinning is a no-op away from darwin,
// where SF Symbols never rasterize.
const screenshotSFBackingScale = float64(screenshotScale)

func installScreenshotSFSymbolsScale(t *testing.T, backingScale float64) {
	t.Helper()
}
