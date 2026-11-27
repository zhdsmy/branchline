//go:build darwin

package app

// Scale-pinned SF Symbols rasterization for the screenshot regression suite.
// Upstream renders each symbol through -[NSImage CGImageForProposedRect:NULL
// context:nil hints:nil], which AppKit rasterizes at the host main display's
// backing scale: the same test build produces different pixels on 1x and 2x
// hosts. Passing that method an explicit NSGraphicsContext whose CTM is
// scaled by the wanted backing scale makes the rasterization independent of
// the main display. Everything else in this file replicates the upstream
// rasterizer step for step so the only difference is that context argument.

import (
	"image"
	"math"
	"runtime"
	"structs"
	"sync"
	"testing"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo/plugins/sfsymbols"
	"github.com/egoist/mygo/ui"
)

// screenshotSFBackingScale is the backing scale the screenshot suite pins SF
// Symbols rasterization to: the suite's own rendering scale.
const screenshotSFBackingScale = float64(screenshotScale)

var (
	sfSelSymbolImage = objc.RegisterName("imageWithSystemSymbolName:accessibilityDescription:")
	sfSelSymbolConf  = objc.RegisterName("configurationWithPointSize:weight:")
	sfSelSymbolApply = objc.RegisterName("imageWithSymbolConfiguration:")
	sfSelCGImage     = objc.RegisterName("CGImageForProposedRect:context:hints:")
	sfSelSize        = objc.RegisterName("size")
	sfSelAlloc       = objc.RegisterName("alloc")
	sfSelInitBytes   = objc.RegisterName("initWithBytes:length:encoding:")
	sfSelRelease     = objc.RegisterName("release")
	sfSelMainScreen  = objc.RegisterName("mainScreen")
	sfSelBacking     = objc.RegisterName("backingScaleFactor")
	sfSelGraphicsCtx = objc.RegisterName("graphicsContextWithCGContext:flipped:")
)

// sfCGRect is CGRect, which CGContextDrawImage takes by value.
type sfCGRect struct {
	_          structs.HostLayout
	X, Y, W, H float64
}

// sfNSSize is NSSize, which -[NSImage size] returns by value.
type sfNSSize struct {
	_    structs.HostLayout
	W, H float64
}

// sfCG holds the CoreGraphics and libobjc entry points the pinned rasterizer
// calls, once resolved.
var sfCG struct {
	once         sync.Once
	ok           bool
	space        func() uintptr
	releaseSpace func(space uintptr)
	bitmap       func(data unsafe.Pointer, width, height, bitsPerComponent, bytesPerRow, space uintptr, info uint32) uintptr
	releaseCtx   func(ctx uintptr)
	draw         func(ctx uintptr, rect sfCGRect, img uintptr)
	scaleCTM     func(ctx uintptr, sx, sy float64)
	width        func(img uintptr) uintptr
	height       func(img uintptr) uintptr

	// poolPush and poolPop are libobjc's autorelease pools; size sends a
	// message that returns an NSSize.
	poolPush, poolPop uintptr
	size              func(obj objc.ID, sel objc.SEL) sfNSSize
}

const (
	sfPremultipliedLast = 1
	sfByteOrder32Big    = 4 << 12
	sfUTF8Encoding      = 4
	sfMaxPx             = 512
)

func sfLoad() bool {
	sfCG.once.Do(func() {
		if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
			return
		}
		lib, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return
		}
		purego.RegisterLibFunc(&sfCG.space, lib, "CGColorSpaceCreateDeviceRGB")
		purego.RegisterLibFunc(&sfCG.releaseSpace, lib, "CGColorSpaceRelease")
		purego.RegisterLibFunc(&sfCG.bitmap, lib, "CGBitmapContextCreate")
		purego.RegisterLibFunc(&sfCG.releaseCtx, lib, "CGContextRelease")
		purego.RegisterLibFunc(&sfCG.draw, lib, "CGContextDrawImage")
		purego.RegisterLibFunc(&sfCG.scaleCTM, lib, "CGContextScaleCTM")
		purego.RegisterLibFunc(&sfCG.width, lib, "CGImageGetWidth")
		purego.RegisterLibFunc(&sfCG.height, lib, "CGImageGetHeight")
		libobjc, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return
		}
		if sfCG.poolPush, err = purego.Dlsym(libobjc, "objc_autoreleasePoolPush"); err != nil {
			return
		}
		if sfCG.poolPop, err = purego.Dlsym(libobjc, "objc_autoreleasePoolPop"); err != nil {
			return
		}
		msgSend, err := purego.Dlsym(libobjc, "objc_msgSend")
		if err != nil {
			return
		}
		purego.RegisterFunc(&sfCG.size, msgSend)
		sfCG.ok = true
	})
	return sfCG.ok
}

// sfNSString makes an owned NSString for s, which the caller releases.
func sfNSString(s string) objc.ID {
	b := append([]byte(s), 0)
	str := objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(objc.GetClass("NSString")), sfSelAlloc), sfSelInitBytes,
		unsafe.Pointer(&b[0]), uintptr(len(s)), uintptr(sfUTF8Encoding))
	runtime.KeepAlive(b)
	return str
}

// sfCanvas makes a bitmap context over pix, w by h pixels, and returns it
// with the function that releases it.
func sfCanvas(pix []byte, w, h int) (uintptr, func()) {
	space := sfCG.space()
	ctx := sfCG.bitmap(unsafe.Pointer(&pix[0]), uintptr(w), uintptr(h), 8, uintptr(w*4), space, sfPremultipliedLast|sfByteOrder32Big)
	sfCG.releaseSpace(space)
	if ctx == 0 {
		return 0, func() {}
	}
	return ctx, func() {
		sfCG.releaseCtx(ctx)
		runtime.KeepAlive(pix)
	}
}

// sfReferenceCanvas returns a small bitmap context whose coordinate system
// is scaled backingScale times, with the function that releases it. Wrapped
// in an NSGraphicsContext, it makes AppKit rasterize an image at
// backingScale pixels per point instead of the main display's.
func sfReferenceCanvas(backingScale float64) (uintptr, func()) {
	refDim := max(int(math.Ceil(backingScale))*4, 4)
	refPix := make([]byte, refDim*refDim*4)
	refCtx, refDone := sfCanvas(refPix, refDim, refDim)
	if refCtx != 0 {
		sfCG.scaleCTM(refCtx, backingScale, backingScale)
	}
	return refCtx, refDone
}

// sfWithPool runs fn in an autorelease pool, as upstream's withPool does. A
// pool belongs to its thread: the goroutine stays on one until it pops.
func sfWithPool(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	p, _, _ := purego.SyscallN(sfCG.poolPush)
	defer purego.SyscallN(sfCG.poolPop, p)
	fn()
}

// sfRasterizeAtScale draws the symbol's outline into a px×px canvas exactly
// as upstream's rasterize does, except the image is rasterized against an
// explicit reference context scaled to backingScale instead of the nil
// context that follows the main display. pixelsPerPoint reports the scale
// AppKit rasterized at, so tests can assert the pin took effect; absent says
// AppKit has no symbol of the name, and other failures leave it false.
func sfRasterizeAtScale(name string, px int, backingScale float64) (coverage *image.RGBA, pixelsPerPoint float64, ok, absent bool) {
	if !sfLoad() {
		return nil, 0, false, false
	}
	// The reference bitmap context outlives the pool: the NSGraphicsContext
	// wrapping it, and whatever AppKit derived from it, go when the pool
	// pops, and only then is the bitmap context released.
	refCtx, refDone := sfReferenceCanvas(backingScale)
	defer refDone()
	if refCtx == 0 {
		return nil, 0, false, false
	}
	sfWithPool(func() { coverage, pixelsPerPoint, ok, absent = sfDrawAtScale(name, px, refCtx) })
	return coverage, pixelsPerPoint, ok, absent
}

// sfDrawAtScale is sfRasterizeAtScale inside the pool, mirroring upstream's
// draw step for step with a graphics context over refCtx in place of nil.
func sfDrawAtScale(name string, px int, refCtx uintptr) (out *image.RGBA, pixelsPerPoint float64, ok, absent bool) {
	str := sfNSString(name)
	defer objc.Send[objc.ID](str, sfSelRelease)
	img := objc.Send[objc.ID](objc.ID(objc.GetClass("NSImage")), sfSelSymbolImage, str, objc.ID(0))
	if img == 0 {
		return nil, 0, false, true
	}
	// A point size of px renders large enough that the canvas below scales
	// the glyph down, never up; 0.23 is NSFontWeightMedium, the weight every
	// application icon uses.
	cfg := objc.Send[objc.ID](objc.ID(objc.GetClass("NSImageSymbolConfiguration")), sfSelSymbolConf, float64(px), 0.23)
	img = objc.Send[objc.ID](img, sfSelSymbolApply, cfg)
	if img == 0 {
		return nil, 0, false, false
	}
	refNS := objc.Send[objc.ID](objc.ID(objc.GetClass("NSGraphicsContext")), sfSelGraphicsCtx, refCtx, false)
	if refNS == 0 {
		return nil, 0, false, false
	}
	points := sfCG.size(img, sfSelSize)
	cgImage := objc.Send[objc.ID](img, sfSelCGImage, uintptr(0), uintptr(refNS), uintptr(0))
	if cgImage == 0 {
		return nil, 0, false, false
	}
	w, h := int(sfCG.width(uintptr(cgImage))), int(sfCG.height(uintptr(cgImage)))
	if w <= 0 || h <= 0 || points.W <= 0 {
		return nil, 0, false, false
	}
	// pixelsPerPoint is the scale AppKit chose for the image.
	pixelsPerPoint = float64(w) / points.W

	// The glyph inside its alignment margins, in a canvas of its own size.
	src := make([]byte, w*h*4)
	ctx, done := sfCanvas(src, w, h)
	if ctx == 0 {
		return nil, 0, false, false
	}
	sfCG.draw(ctx, sfCGRect{W: float64(w), H: float64(h)}, uintptr(cgImage))
	done()
	minX, minY, maxX, maxY := w, h, -1, -1
	for y := range h {
		for x := range w {
			if src[y*w*4+x*4+3] != 0 {
				minX, maxX = min(minX, x), max(maxX, x)
				minY, maxY = min(minY, y), max(maxY, y)
			}
		}
	}
	if maxX < 0 {
		return nil, 0, false, false
	}
	gw, gh := maxX-minX+1, maxY-minY+1

	// The glyph, scaled into the box and centered.
	dst := make([]byte, px*px*4)
	dctx, ddone := sfCanvas(dst, px, px)
	if dctx == 0 {
		return nil, 0, false, false
	}
	const inset = 0.85
	scale := math.Min(inset*float64(px)/float64(gw), inset*float64(px)/float64(gh))
	dw, dh := float64(gw)*scale, float64(gh)*scale
	// The image holds the glyph inside alignment margins: scale the whole
	// image so that the glyph, and not the margins, fills the box.
	sfCG.draw(dctx, sfCGRect{
		X: (float64(px)-dw)/2 - float64(minX)*scale,
		// CoreGraphics counts y up from the bottom, the rows down from the
		// top: the margin below the glyph is the rows after maxY.
		Y: (float64(px)-dh)/2 - float64(h-1-maxY)*scale,
		W: float64(w) * scale,
		H: float64(h) * scale,
	}, uintptr(cgImage))
	ddone()

	out = image.NewRGBA(image.Rect(0, 0, px, px))
	for i := range px * px {
		a := dst[i*4+3]
		out.Pix[i*4+0], out.Pix[i*4+1], out.Pix[i*4+2], out.Pix[i*4+3] = a, a, a, a
	}
	return out, pixelsPerPoint, true, false
}

// sfTint colors coverage, whose alpha holds the shape, in col, as upstream's
// tint does.
func sfTint(coverage *image.RGBA, col ui.Color) *image.RGBA {
	out := image.NewRGBA(coverage.Rect)
	for i := range len(coverage.Pix) / 4 {
		a := uint16(coverage.Pix[i*4+3]) * uint16(col.A) / 255
		out.Pix[i*4+0] = uint8(uint16(col.R) * a / 255)
		out.Pix[i*4+1] = uint8(uint16(col.G) * a / 255)
		out.Pix[i*4+2] = uint8(uint16(col.B) * a / 255)
		out.Pix[i*4+3] = uint8(a)
	}
	return out
}

// sfShapeKey names a scale-pinned symbol shape; sfTintKey adds the color it
// is drawn in. The backing scale is part of every key so cached bitmaps never
// leak across scales.
type sfShapeKey struct {
	name         string
	px           int
	backingScale float64
}

type sfTintKey struct {
	shape      sfShapeKey
	r, g, b, a uint8
}

var sfTestCache = struct {
	sync.Mutex
	shapes  map[sfShapeKey]*image.RGBA
	missing map[string]bool
	tinted  map[sfTintKey]*ui.Bitmap
}{
	shapes:  map[sfShapeKey]*image.RGBA{},
	missing: map[string]bool{},
	tinted:  map[sfTintKey]*ui.Bitmap{},
}

// sfImageAtScale returns the SF Symbol named name drawn px pixels square in
// col, rasterized at backingScale instead of the host display's scale, or
// false where the system has no such symbol. It mirrors upstream's Image,
// including its caching, with the backing scale added to every cache key.
func sfImageAtScale(name string, px int, col ui.Color, backingScale float64) (ui.ImageSource, bool) {
	if !sfLoad() {
		return nil, false
	}
	px = min(max(px, 8), sfMaxPx)
	sk := sfShapeKey{name: name, px: px, backingScale: backingScale}
	tk := sfTintKey{shape: sk, r: col.R, g: col.G, b: col.B, a: col.A}
	sfTestCache.Lock()
	b, ok := sfTestCache.tinted[tk]
	coverage, drawn := sfTestCache.shapes[sk]
	absent := sfTestCache.missing[name]
	sfTestCache.Unlock()
	if ok {
		return b, true
	}
	if absent {
		return nil, false
	}
	if !drawn {
		var got, absent bool
		if coverage, _, got, absent = sfRasterizeAtScale(name, px, backingScale); !got {
			// Only a name AppKit has no symbol for is remembered, as upstream
			// does: another failure may not happen at another size or scale.
			if absent {
				sfTestCache.Lock()
				sfTestCache.missing[name] = true
				sfTestCache.Unlock()
			}
			return nil, false
		}
	}
	b = ui.NewBitmap(sfTint(coverage, col))
	sfTestCache.Lock()
	sfTestCache.shapes[sk] = coverage
	sfTestCache.tinted[tk] = b
	sfTestCache.Unlock()
	return b, true
}

// fixedScaleSymbolIcon builds the same element upstream's Symbol builds —
// same box, role and label — except the paint step rasterizes at backingScale
// through sfImageAtScale. Where the system has no such symbol it draws the
// fallback exactly as upstream does.
func fixedScaleSymbolIcon(backingScale float64) func(c *ui.Context, sfName string, fallback *ui.SVG, size float32, color ui.Color) {
	return func(c *ui.Context, sfName string, fallback *ui.SVG, size float32, color ui.Color) {
		e := ui.Box(c).Size(size, size).Shrink(0).Role(ui.RoleImage).Label(sfName)
		e.Draw(func(p *ui.Painter, r ui.Rect) {
			side := min(r.W, r.H)
			if side <= 0 {
				return
			}
			box := ui.Rect{X: r.X + (r.W-side)/2, Y: r.Y + (r.H-side)/2, W: side, H: side}
			scale := p.Scale()
			if scale <= 0 {
				scale = 1
			}
			px := int(math.Ceil(float64(side * scale)))
			if src, ok := sfImageAtScale(sfName, px, color, backingScale); ok {
				p.Image(src, box, ui.Contain)
				return
			}
			if fallback != nil {
				p.Icon(fallback, box, color)
			}
		})
	}
}

// installScreenshotSFSymbolsScale pins symbolIcon rasterization to
// backingScale for the duration of the test and restores the previous
// override in cleanup.
func installScreenshotSFSymbolsScale(t *testing.T, backingScale float64) {
	t.Helper()
	previous := symbolIconDrawOverride
	symbolIconDrawOverride = fixedScaleSymbolIcon(backingScale)
	t.Cleanup(func() { symbolIconDrawOverride = previous })
}

// hostMainScreenScale reports the backing scale factor of the host's main
// screen, defaulting to 1 where AppKit or a screen is unavailable.
func hostMainScreenScale() float64 {
	if !sfLoad() {
		return 1
	}
	scale := 1.0
	sfWithPool(func() {
		screen := objc.Send[objc.ID](objc.ID(objc.GetClass("NSScreen")), sfSelMainScreen)
		if screen == 0 {
			return
		}
		if s := objc.Send[float64](screen, sfSelBacking); s > 0 {
			scale = s
		}
	})
	return scale
}

// renderOpaque renders paint as the children of an opaque w×h DIP window at
// scale and returns the capture, for comparing rasterizers pixel for pixel.
func renderOpaque(w, h int, scale float32, paint func(c *ui.Context)) *image.RGBA {
	view := func(c *ui.Context) {
		ui.Box(c).Size(float32(w), float32(h)).Shrink(0).Background(ui.Hex("#101010")).Children(func() { paint(c) })
	}
	tt := ui.NewTester(view, w, h)
	tt.SetScale(scale)
	tt.Frame()
	return copyImage(tt.Image())
}

// renderImageSourceOpaque draws src 1:1 into an opaque window: the box is the
// bitmap's pixel size in DIPs and the tester scale is 1, so neither the box
// fit nor the window scale resamples the bitmap before comparison.
func renderImageSourceOpaque(src ui.ImageSource) *image.RGBA {
	w, h := ui.ImageSize(src)
	if w <= 0 || h <= 0 {
		return nil
	}
	return renderOpaque(int(math.Ceil(float64(w))), int(math.Ceil(float64(h))), 1, func(c *ui.Context) {
		e := ui.Box(c).Size(w, h).Shrink(0)
		e.Draw(func(p *ui.Painter, r ui.Rect) {
			p.Image(src, r, ui.Contain)
		})
	})
}

// TestScreenshotSFSymbolScaleIsolation proves the scale-pinned rasterizer
// matches upstream's pixel for pixel, that the pin decides the scale rather
// than the host display, and that installing it through lockScreenshotEnv
// covers the real symbolIcon entry point and restores cleanly.
func TestScreenshotSFSymbolScaleIsolation(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("SF Symbols rendering requires darwin (current GOOS=%s)", runtime.GOOS)
	}
	if !sfLoad() {
		t.Skip("CoreGraphics or AppKit unavailable")
	}

	t.Run("explicit scales match upstream rasterization", func(t *testing.T) {
		symbols := []struct {
			name string
			px   int
		}{
			{"arrow.triangle.branch", 26},
			{"folder", 28},
			{"chevron.left", 30},
			{"arrow.triangle.2.circlepath", 27},
			{"clock", 34},
		}
		hostScale := hostMainScreenScale()
		for _, sym := range symbols {
			cov1, ppp1, ok1, _ := sfRasterizeAtScale(sym.name, sym.px, 1.0)
			cov2, ppp2, ok2, _ := sfRasterizeAtScale(sym.name, sym.px, 2.0)
			if !ok1 || !ok2 {
				t.Fatalf("sfRasterizeAtScale(%q, %d) failed: 1x ok=%v 2x ok=%v", sym.name, sym.px, ok1, ok2)
			}
			if math.Abs(ppp1-1.0) > 0.05 {
				t.Fatalf("%s 1x pixelsPerPoint = %v, want within 0.05 of 1.0", sym.name, ppp1)
			}
			if math.Abs(ppp2-2.0) > 0.05 {
				t.Fatalf("%s 2x pixelsPerPoint = %v, want within 0.05 of 2.0", sym.name, ppp2)
			}
			if stats, _ := compareScreenshotImages(cov1, cov2, screenshotChanTol); stats.DiffPixels == 0 {
				t.Fatalf("%s 1x and 2x rasterizations are pixel-identical, the scale pin does nothing", sym.name)
			}

			col := ui.Hex("#ffffff")
			mine, ok := sfImageAtScale(sym.name, sym.px, col, hostScale)
			if !ok {
				t.Fatalf("sfImageAtScale(%q, %d, host scale %v) failed", sym.name, sym.px, hostScale)
			}
			upstream, ok := sfsymbols.Image(sym.name, sym.px, col, sfsymbols.Options{Weight: sfsymbols.Medium})
			if !ok {
				t.Fatalf("upstream sfsymbols.Image(%q, %d) failed", sym.name, sym.px)
			}
			mineImg := renderImageSourceOpaque(mine)
			upstreamImg := renderImageSourceOpaque(upstream)
			if mineImg == nil || upstreamImg == nil {
				t.Fatalf("%s image source rendered empty", sym.name)
			}
			stats, _ := compareScreenshotImages(upstreamImg, mineImg, 0)
			if stats.DiffPixels != 0 || stats.MaxChannelDiff != 0 {
				t.Fatalf("%s pinned rasterizer at host scale differs from upstream: %d diff pixels, maxChannelDiff %d (host backing scale %v)",
					sym.name, stats.DiffPixels, stats.MaxChannelDiff, hostScale)
			}
		}
	})

	t.Run("lockScreenshotEnv pins symbolIcon to the fixed scale", func(t *testing.T) {
		// Warm the upstream caches at the host display scale first: the pin
		// must hold even when upstream already cached its own rasterization.
		if _, ok := sfsymbols.Image("clock", 34, ui.Hex("#ffffff"), sfsymbols.Options{Weight: sfsymbols.Medium}); !ok {
			t.Fatal("upstream sfsymbols.Image clock unavailable")
		}
		lockScreenshotEnv(t)
		if symbolIconDrawOverride == nil {
			t.Fatal("lockScreenshotEnv did not install the scale-pinned symbolIcon override")
		}

		renderIcon := func(draw func(c *ui.Context)) *image.RGBA {
			return renderOpaque(64, 64, screenshotScale, func(c *ui.Context) {
				ui.Box(c).Size(64, 64).Shrink(0).Center().Children(func() { draw(c) })
			})
		}
		white := ui.Hex("#ffffff")
		icons := []struct {
			name     string
			size     float32
			fallback *ui.SVG
		}{
			{"clock", 17, fallbackHist},
			{"arrow.triangle.branch", 13, fallbackBr},
		}
		for _, icon := range icons {
			throughReal := renderIcon(func(c *ui.Context) { symbolIcon(c, icon.name, icon.fallback, icon.size, white) })
			at2 := renderIcon(func(c *ui.Context) { fixedScaleSymbolIcon(2.0)(c, icon.name, icon.fallback, icon.size, white) })
			at1 := renderIcon(func(c *ui.Context) { fixedScaleSymbolIcon(1.0)(c, icon.name, icon.fallback, icon.size, white) })
			if stats, _ := compareScreenshotImages(at2, throughReal, 0); stats.DiffPixels != 0 {
				t.Fatalf("%s through symbolIcon differs from fixed 2x rasterization: %d diff pixels", icon.name, stats.DiffPixels)
			}
			if stats, _ := compareScreenshotImages(at1, throughReal, screenshotChanTol); stats.DiffPixels == 0 {
				t.Fatalf("%s through symbolIcon is pixel-identical to 1x rasterization, the 2x pin does nothing", icon.name)
			}
		}

		// On a 2x host the checks above would also pass if symbolIcon ignored
		// the override, since upstream then rasterizes at 2x too. Pinning 1x
		// proves symbolIcon dispatches through the override on any host.
		installScreenshotSFSymbolsScale(t, 1.0)
		for _, icon := range icons {
			throughReal := renderIcon(func(c *ui.Context) { symbolIcon(c, icon.name, icon.fallback, icon.size, white) })
			at1 := renderIcon(func(c *ui.Context) { fixedScaleSymbolIcon(1.0)(c, icon.name, icon.fallback, icon.size, white) })
			at2 := renderIcon(func(c *ui.Context) { fixedScaleSymbolIcon(2.0)(c, icon.name, icon.fallback, icon.size, white) })
			if stats, _ := compareScreenshotImages(at1, throughReal, 0); stats.DiffPixels != 0 {
				t.Fatalf("%s through symbolIcon pinned to 1x differs from fixed 1x rasterization: %d diff pixels", icon.name, stats.DiffPixels)
			}
			if stats, _ := compareScreenshotImages(at2, throughReal, screenshotChanTol); stats.DiffPixels == 0 {
				t.Fatalf("%s through symbolIcon pinned to 1x is pixel-identical to 2x rasterization, the override is not used", icon.name)
			}
		}

		// A missing symbol falls back to its SVG exactly as upstream does:
		// identical to upstream's own Symbol element painting the fallback.
		missing := renderIcon(func(c *ui.Context) {
			symbolIcon(c, "branchline.nonexistent.symbol", fallbackHist, 17, ui.Hex("#ffffff"))
		})
		upstreamFallback := renderIcon(func(c *ui.Context) {
			sfsymbols.Symbol(c, "branchline.nonexistent.symbol").
				Size(17).
				Color(ui.Hex("#ffffff")).
				Weight(sfsymbols.Medium).
				Fallback(fallbackHist)
		})
		if stats, _ := compareScreenshotImages(upstreamFallback, missing, 0); stats.DiffPixels != 0 {
			t.Fatalf("missing symbol fallback differs from upstream fallback: %d diff pixels", stats.DiffPixels)
		}
	})
	if symbolIconDrawOverride != nil {
		t.Fatal("symbolIconDrawOverride leaked past the subtest cleanup")
	}
}
