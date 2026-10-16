package main

import (
	"fmt"
	"math"
	"syscall"
	"unsafe"
)

// gdiplus.go is the renderer's FFI layer. Every entry point used here takes INT
// or pointer parameters only, because float parameters travel in XMM registers
// on x64 and syscall cannot set those. Concretely:
//
//   - GdipAddPathLineI / GdipAddPathBezierI build paths from integers, so
//     rounded rectangles are drawn as four integer bezier corners.
//   - GdipCreateLineBrushI takes integer points, giving real gradients.
//   - GdipCreateFontFromLogfontW takes a LOGFONTW (integers), not the REAL
//     em-size of GdipCreateFont.
//   - GdipDrawString takes its layout rectangle by pointer, so floats are fine
//     inside the pointed-to struct.
//   - GdipDrawImageRectI / GdipDrawImagePointsRectI cover scaled and rotated
//     draws. (The non-I GdipDrawImagePointsRect silently fails here: its
//     srcx/srcwidth are REAL.)
//
// Pens are the one thing missing: every GdipDraw* that strokes takes a REAL
// width. Strokes are therefore drawn as filled rings (two figures in one path,
// FillModeAlternate punches the hole).

var (
	dGdiplus = syscall.NewLazyDLL("gdiplus.dll")

	gdiplusStartup            = dGdiplus.NewProc("GdiplusStartup")
	gdiplusShutdown           = dGdiplus.NewProc("GdiplusShutdown")
	gpCreateBitmapFromScan0   = dGdiplus.NewProc("GdipCreateBitmapFromScan0")
	gpCreateBitmapFromFile    = dGdiplus.NewProc("GdipCreateBitmapFromFile")
	gpCreateBitmapFromHBITMAP = dGdiplus.NewProc("GdipCreateBitmapFromHBITMAP")
	gpCreateHBITMAPFromBitmap = dGdiplus.NewProc("GdipCreateHBITMAPFromBitmap")
	gpCloneBitmapAreaI        = dGdiplus.NewProc("GdipCloneBitmapAreaI")
	gpGetImageGraphicsContext = dGdiplus.NewProc("GdipGetImageGraphicsContext")
	gpGetImageWidth           = dGdiplus.NewProc("GdipGetImageWidth")
	gpGetImageHeight          = dGdiplus.NewProc("GdipGetImageHeight")
	gpGetImageDimension       = dGdiplus.NewProc("GdipGetImageDimension")
	gpSaveImageToFile         = dGdiplus.NewProc("GdipSaveImageToFile")
	gpDisposeImage            = dGdiplus.NewProc("GdipDisposeImage")
	gpSetSmoothingMode        = dGdiplus.NewProc("GdipSetSmoothingMode")
	gpSetInterpolationMode    = dGdiplus.NewProc("GdipSetInterpolationMode")
	gpSetPixelOffsetMode      = dGdiplus.NewProc("GdipSetPixelOffsetMode")
	gpSetCompositingQuality   = dGdiplus.NewProc("GdipSetCompositingQuality")
	gpSetCompositingMode      = dGdiplus.NewProc("GdipSetCompositingMode")
	gpSetTextRenderingHint    = dGdiplus.NewProc("GdipSetTextRenderingHint")
	gpCreatePath              = dGdiplus.NewProc("GdipCreatePath")
	gpAddPathLineI            = dGdiplus.NewProc("GdipAddPathLineI")
	gpAddPathBezierI          = dGdiplus.NewProc("GdipAddPathBezierI")
	gpAddPathEllipseI         = dGdiplus.NewProc("GdipAddPathEllipseI")
	gpAddPathRectangleI       = dGdiplus.NewProc("GdipAddPathRectangleI")
	gpClosePathFigure         = dGdiplus.NewProc("GdipClosePathFigure")
	gpStartPathFigure         = dGdiplus.NewProc("GdipStartPathFigure")
	gpDeletePath              = dGdiplus.NewProc("GdipDeletePath")
	gpCreateSolidFill         = dGdiplus.NewProc("GdipCreateSolidFill")
	gpCreateLineBrushI        = dGdiplus.NewProc("GdipCreateLineBrushI")
	gpCreatePathGradient      = dGdiplus.NewProc("GdipCreatePathGradientFromPath")
	gpSetPathGradCenterColor  = dGdiplus.NewProc("GdipSetPathGradientCenterColor")
	gpSetPathGradSurround     = dGdiplus.NewProc("GdipSetPathGradientSurroundColorsWithCount")
	gpSetPathGradCenterPoint  = dGdiplus.NewProc("GdipSetPathGradientCenterPoint")
	gpSetLineWrapMode         = dGdiplus.NewProc("GdipSetLineWrapMode")
	gpSetLineColors           = dGdiplus.NewProc("GdipSetLineColors")
	// GdipSetLinePresetBlend takes pointers to arrays, so unlike the brush's
	// scalar setters it is callable through syscall. It gives the multi-stop
	// gradients the aluminium clip needs.
	gpSetLinePresetBlend   = dGdiplus.NewProc("GdipSetLinePresetBlend")
	gpImageRotateFlip      = dGdiplus.NewProc("GdipImageRotateFlip")
	gpDeleteBrush          = dGdiplus.NewProc("GdipDeleteBrush")
	gpFillPath             = dGdiplus.NewProc("GdipFillPath")
	gpFillRectangleI       = dGdiplus.NewProc("GdipFillRectangleI")
	gpFillEllipseI         = dGdiplus.NewProc("GdipFillEllipseI")
	gpDrawImageRectI       = dGdiplus.NewProc("GdipDrawImageRectI")
	gpDrawImagePointsRectI = dGdiplus.NewProc("GdipDrawImagePointsRectI")
	gpDrawImageRectRectI   = dGdiplus.NewProc("GdipDrawImageRectRectI")
	// ImageAttributes carries a colour matrix, which is the only way to
	// modulate a sprite's alpha: the plain draw calls have no such parameter and
	// GdipDrawImagePointsRectI is the one overload that takes attributes.
	gpCreateImageAttributes    = dGdiplus.NewProc("GdipCreateImageAttributes")
	gpDisposeImageAttributes   = dGdiplus.NewProc("GdipDisposeImageAttributes")
	gpSetImageAttrColorMatrix  = dGdiplus.NewProc("GdipSetImageAttributesColorMatrix")
	gpCreateFontFromLogfont    = dGdiplus.NewProc("GdipCreateFontFromLogfontW")
	gpDeleteFont               = dGdiplus.NewProc("GdipDeleteFont")
	gpDrawString               = dGdiplus.NewProc("GdipDrawString")
	gpMeasureString            = dGdiplus.NewProc("GdipMeasureString")
	gpCreateStringFormat       = dGdiplus.NewProc("GdipCreateStringFormat")
	gpDeleteStringFormat       = dGdiplus.NewProc("GdipDeleteStringFormat")
	gpSetStringFormatAlign     = dGdiplus.NewProc("GdipSetStringFormatAlign")
	gpSetStringFormatLineAlign = dGdiplus.NewProc("GdipSetStringFormatLineAlign")
	gpSetStringFormatTrimming  = dGdiplus.NewProc("GdipSetStringFormatTrimming")
	gpSetClipPath              = dGdiplus.NewProc("GdipSetClipPath")
	gpSetClipRectI             = dGdiplus.NewProc("GdipSetClipRectI")
	gpResetClip                = dGdiplus.NewProc("GdipResetClip")
	gpSetClipRect              = dGdiplus.NewProc("GdipSetClipRect")
	gpGetClip                  = dGdiplus.NewProc("GdipGetClip")
	gpCreateRegionPath         = dGdiplus.NewProc("GdipCreateRegionPath")
	gpDeleteRegion             = dGdiplus.NewProc("GdipDeleteRegion")
	gpFillRegion               = dGdiplus.NewProc("GdipFillRegion")
	gpDeleteGraphics           = dGdiplus.NewProc("GdipDeleteGraphics")
	gpFlush                    = dGdiplus.NewProc("GdipFlush")
	gpBitmapGetPixel           = dGdiplus.NewProc("GdipBitmapGetPixel")
)

// GDI+ enum values.
const (
	pixelFormat32bppPARGB    = 0x000E200B
	pixelFormat32bppARGB     = 0x0026200A
	smoothingAntiAlias       = 4
	interpHighQualityBicubic = 7
	// PixelOffsetMode.HighQuality keeps a scaled draw centred; Half is the value
	// GDI+ wants when an image is scaled and rotated in one call, which is every
	// card.
	pixelOffsetModeHighQuality = 2
	pixelOffsetModeHalf        = 4
	compositingSourceOver      = 0
	compositingHighQuality     = 2
	textRenderingAntiAlias     = 4
	fillModeAlternate          = 0
	wrapModeClamp              = 4
	unitPixel                  = 2
	combineReplace             = 0
	stringAlignNear            = 0
	stringAlignCenter          = 1
	trimmingNone               = 0
	flushIntentionSync         = 1
	colorAdjustTypeBitmap      = 1
	colorMatrixFlagsDefault    = 0
)

// pngEncoderCLSID is the PNG encoder that ships inside gdiplus.dll.
var pngEncoderCLSID = [16]byte{
	0x06, 0xf4, 0x7c, 0x55, 0x04, 0x1a, 0xd3, 0x11,
	0x9a, 0x73, 0x00, 0x00, 0xf8, 0x1e, 0xf3, 0x2e,
}

type gdipStartupInput struct {
	Version               uint32
	DebugEventCallback    uintptr
	SuppressBackground    int32
	SuppressExternalCodec int32
}

type GpPoint struct{ X, Y int32 }
type GpPointF struct{ X, Y float32 }
type GpRect struct{ X, Y, W, H int32 }
type GpRectF struct{ X, Y, W, H float32 }

// LOGFONTW, integers only, which is what makes GdipCreateFontFromLogfontW
// callable through syscall.
type LOGFONTW struct {
	Height         int32
	Width          int32
	Escapement     int32
	Orientation    int32
	Weight         int32
	Italic         byte
	Underline      byte
	StrikeOut      byte
	CharSet        byte
	OutPrecision   byte
	ClipPrecision  byte
	Quality        byte
	PitchAndFamily byte
	FaceName       [32]uint16
}

// Gdiplus token kept for the process lifetime.
var gpToken uintptr

func gdiplusStart() {
	// Starting twice is harmless but pointless: GdiplusStartup is reference
	// counted, and this package keeps one token for the whole process. Guarding
	// here is what lets main start GDI+ for the real app while the test harnesses
	// also ask for it, without either one shutting it down under the other.
	if gpToken != 0 {
		return
	}
	in := gdipStartupInput{Version: 1}
	if st := call(gdiplusStartup, uptr(&gpToken), uptr(&in), 0); st != 0 {
		panic(fmt.Sprintf("GdiplusStartup failed with status %d", st))
	}
}

func gdiplusStop() {
	if gpToken != 0 {
		call(gdiplusShutdown, gpToken)
		gpToken = 0
	}
}

// gpStatus reports a GDI+ failure the first time it happens; GDI+ returns 0 for
// Ok (unlike most Win32 calls), so a non-zero status is a real bug.
func gpStatus(what string, st uintptr) {
	if st != 0 {
		logf("gdiplus %s failed: status %d", what, st)
	}
}

// ------------------------------------------------------------------ surface

// surface is an offscreen 32bpp premultiplied-BGRA bitmap that GDI+ draws into.
// The pixels are reachable both as a Go slice and as an HDC/HBITMAP, so the same
// buffer can be (a) composed with GDI+, (b) blitted with GDI, and (c) handed to
// UpdateLayeredWindow, without a copy.
type surface struct {
	W, H     int
	memDC    uintptr
	hbmp     uintptr
	bits     uintptr
	bitmap   uintptr // GpBitmap over the same pixels
	g        uintptr // GpGraphics for bitmap
	selected uintptr // the DIB previously selected into memDC
}

func newSurface(w, h int) *surface {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	s := &surface{W: w, H: h}
	s.memDC = call(dGdi32.NewProc("CreateCompatibleDC"), 0)
	if s.memDC == 0 {
		panic("CreateCompatibleDC failed")
	}
	bi := bitmapInfo{}
	bi.Header.Size = uint32(unsafe.Sizeof(bitmapInfoHeader{}))
	bi.Header.Width = int32(w)
	bi.Header.Height = -int32(h) // top-down, matching GDI+ row order
	bi.Header.Planes = 1
	bi.Header.BitCount = 32
	hbmp := call(gdi32CreateDIBSection, 0, uptr(&bi), 0, uptr(&s.bits), 0, 0)
	if hbmp == 0 {
		panic("CreateDIBSection failed")
	}
	s.hbmp = hbmp
	s.selected = call(gdi32SelectObject, s.memDC, hbmp)
	gpStatus("CreateBitmapFromScan0", call(gpCreateBitmapFromScan0,
		u(w), u(h), u(w*4), pixelFormat32bppPARGB, s.bits, uptr(&s.bitmap)))
	gpStatus("GetImageGraphicsContext", call(gpGetImageGraphicsContext, s.bitmap, uptr(&s.g)))
	s.setQuality()
	return s
}

func (s *surface) setQuality() {
	call(gpSetSmoothingMode, s.g, smoothingAntiAlias)
	call(gpSetInterpolationMode, s.g, interpHighQualityBicubic)
	call(gpSetPixelOffsetMode, s.g, pixelOffsetModeHighQuality)
	call(gpSetCompositingQuality, s.g, compositingHighQuality)
	call(gpSetCompositingMode, s.g, compositingSourceOver)
	call(gpSetTextRenderingHint, s.g, textRenderingAntiAlias)
}

// pixels exposes the raw buffer (premultiplied BGRA). The memory belongs to the
// DIB section, which lives exactly as long as this surface; the same reasoning is
// written out in ffi.go.
func (s *surface) pixels() []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(s.bits)), s.W*s.H*4)
}

// clear blanks the surface to fully transparent.
func (s *surface) clear() {
	p := s.pixels()
	clear(p)
}

func (s *surface) free() {
	if s.bitmap != 0 {
		call(gpDeleteGraphics, s.g)
		call(gpDisposeImage, s.bitmap)
	}
	if s.hbmp != 0 {
		call(gdi32SelectObject, s.memDC, s.selected)
		call(gdi32DeleteObject, s.hbmp)
	}
	if s.memDC != 0 {
		call(gdi32DeleteDC, s.memDC)
	}
	*s = surface{}
}

// savePNG writes the surface out, which is how the tests inspect rendering.
func (s *surface) savePNG(path string) {
	gpStatus("SaveImageToFile", call(gpSaveImageToFile, s.bitmap, utf16Ptr(path),
		uptr(&pngEncoderCLSID[0]), 0))
}

// ------------------------------------------------------------------ drawing

// solidBrush makes a solid ARGB brush.
func (s *surface) solidBrush(argb uint32) uintptr {
	var br uintptr
	gpStatus("CreateSolidFill", call(gpCreateSolidFill, uintptr(argb), uptr(&br)))
	return br
}

func freeBrush(br uintptr) {
	if br != 0 {
		call(gpDeleteBrush, br)
	}
}

// lineBrush is a clamped linear gradient between two points.
func (s *surface) lineBrush(x1, y1, x2, y2 int, c1, c2 uint32) uintptr {
	p1, p2 := GpPoint{int32(x1), int32(y1)}, GpPoint{int32(x2), int32(y2)}
	var br uintptr
	gpStatus("CreateLineBrushI", call(gpCreateLineBrushI,
		uintptr(unsafe.Pointer(&p1)), uintptr(unsafe.Pointer(&p2)),
		uintptr(c1), uintptr(c2), 0, uptr(&br)))
	call(gpSetLineWrapMode, br, wrapModeClamp)
	return br
}

// lineBrushStops is a multi-stop linear gradient, used for the brushed-metal
// clip. positions are 0..1 along the line.
func (s *surface) lineBrushStops(x1, y1, x2, y2 int, colors []uint32, positions []float32) uintptr {
	if len(colors) == 0 || len(colors) != len(positions) {
		return 0
	}
	p1, p2 := GpPoint{int32(x1), int32(y1)}, GpPoint{int32(x2), int32(y2)}
	var br uintptr
	gpStatus("CreateLineBrushI", call(gpCreateLineBrushI,
		uintptr(unsafe.Pointer(&p1)), uintptr(unsafe.Pointer(&p2)),
		uintptr(colors[0]), uintptr(colors[len(colors)-1]), 0, uptr(&br)))
	call(gpSetLineWrapMode, br, wrapModeClamp)
	gpStatus("SetLinePresetBlend", call(gpSetLinePresetBlend, br,
		uintptr(unsafe.Pointer(&colors[0])), uintptr(unsafe.Pointer(&positions[0])), u(len(colors))))
	return br
}

// vGrad is a vertical linear gradient between two heights: the shape of most
// fills in the app (glass tint, button faces, metal bodies).
func (s *surface) vGrad(x, yTop, yBottom int, top, bottom uint32) uintptr {
	return s.lineBrush(x, yTop, x, yBottom, top, bottom)
}

// hGrad is a horizontal linear gradient between two x positions.
func (s *surface) hGrad(y, xLeft, xRight int, left, right uint32) uintptr {
	return s.lineBrush(xLeft, y, xRight, y, left, right)
}

func (s *surface) fillRect(x, y, w, h int, argb uint32) {
	br := s.solidBrush(argb)
	s.fillRectBrush(br, x, y, w, h)
	freeBrush(br)
}

func (s *surface) fillRectBrush(br uintptr, x, y, w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	call(gpFillRectangleI, s.g, br, u(x), u(y), u(w), u(h))
}

func (s *surface) fillPath(br, path uintptr) {
	call(gpFillPath, s.g, br, path)
}

func (s *surface) fillEllipse(br uintptr, x, y, w, h int) {
	call(gpFillEllipseI, s.g, br, u(x), u(y), u(w), u(h))
}

// ---------------------------------------------------------------- paths

// roundedPath builds a rounded rectangle out of integer beziers.
func roundedPath(x, y, w, h, r int) uintptr {
	b := newPathBuilder()
	addRoundedFigure(b, x, y, w, h, r)
	return b.finish()
}

// addRoundedFigure appends a rounded rectangle to a path builder. The corners are
// cubic beziers whose control points sit kappa of the radius away, which is within
// a thousandth of a pixel of a true circular arc at these radii.
func addRoundedFigure(b *pathBuilder, x, y, w, h, r int) {
	if w <= 0 || h <= 0 {
		return
	}
	if r*2 > w {
		r = w / 2
	}
	if r*2 > h {
		r = h / 2
	}
	if r < 0 {
		r = 0
	}
	k := (r * 5523) / 10000 // circle-to-bezier constant (kappa)
	curve := func(xc1, yc1, xc2, yc2, xe, ye int) {
		call(gpAddPathBezierI, b.p, u(b.px), u(b.py), u(xc1), u(yc1), u(xc2), u(yc2), u(xe), u(ye))
		b.px, b.py = xe, ye
	}
	if r == 0 {
		b.add(x, y)
		b.add(x+w, y)
		b.add(x+w, y+h)
		b.add(x, y+h)
		return
	}
	b.add(x+r, y)
	b.add(x+w-r, y)
	curve(x+w-r+k, y, x+w, y+r-k, x+w, y+r)
	b.add(x+w, y+h-r)
	curve(x+w, y+h-r+k, x+w-r+k, y+h, x+w-r, y+h)
	b.add(x+r, y+h)
	curve(x+r-k, y+h, x, y+h-r+k, x, y+h-r)
	b.add(x, y+r)
	curve(x, y+r-k, x+r-k, y, x+r, y)
}

// ringPath is a stroked rounded rectangle drawn as a filled outline: an outer
// and an inner rounded figure in one path, with FillModeAlternate punching the
// hole. This is how strokes are drawn, because GDI+ pens take REAL widths.
func ringPath(x, y, w, h, r, width int) uintptr {
	if width < 1 {
		width = 1
	}
	b := newPathBuilder()
	addRoundedFigure(b, x, y, w, h, r)
	b.addTo(x+width, y+width)
	addRoundedFigure(b, x+width, y+width, w-2*width, h-2*width, r-width)
	return b.finish()
}

// circleRingPath is ringPath for a full circle.
func circleRingPath(x, y, d, width int) uintptr {
	b := newPathBuilder()
	circleInto(b, x, y, d)
	if width > 0 && d-width*2 > 0 {
		b.addTo(x+width, y+width)
		circleInto(b, x+width, y+width, d-2*width)
	}
	return b.finish()
}

// circleInto appends a circle as four beziers. GdipAddPathEllipseI would do it in
// one call, but a path built from beziers can be combined with others in a single
// fill, which is what every ring here needs.
func circleInto(b *pathBuilder, x, y, d int) {
	if d <= 0 {
		return
	}
	r := d / 2
	k := (r * 5523) / 10000
	cx, cy := x+r, y+r
	curve := func(xc1, yc1, xc2, yc2, xe, ye int) {
		call(gpAddPathBezierI, b.p, u(b.px), u(b.py), u(xc1), u(yc1), u(xc2), u(yc2), u(xe), u(ye))
		b.px, b.py = xe, ye
	}
	b.add(cx, y)
	curve(cx+k, y, x+d, cy-k, x+d, cy)
	curve(x+d, cy+k, cx+k, y+d, cx, y+d)
	curve(cx-k, y+d, x, cy+k, x, cy)
	curve(x, cy-k, cx-k, y, cx, y)
}

// capsulePath is a fully rounded rectangle (radius = half the height, or half
// the width when the capsule is vertical).
func capsulePath(x, y, w, h int) uintptr {
	r := h / 2
	if w < h {
		r = w / 2
	}
	b := newPathBuilder()
	addRoundedFigure(b, x, y, w, h, r)
	return b.finish()
}

func freePath(p uintptr) {
	if p != 0 {
		call(gpDeletePath, p)
	}
}

// pathBuilder accumulates integer points into a single closed figure.
//
// GDI+'s flat API has no "move to": the only line primitive is
// GdipAddPathLineI(path, x1, y1, x2, y2), which always takes two points. Calling
// it with one point leaves the second pair to be read out of whatever registers
// happen to hold, and the path goes somewhere undefined. The first point is
// therefore added as a zero-length line, which opens the figure at the right
// place; every point after that is a real segment.
type pathBuilder struct {
	p      uintptr
	px, py int
	open   bool
}

func newPathBuilder() *pathBuilder {
	var p uintptr
	gpStatus("CreatePath", call(gpCreatePath, fillModeAlternate, uptr(&p)))
	return &pathBuilder{p: p}
}

func (b *pathBuilder) add(x, y int) {
	if !b.open {
		call(gpAddPathLineI, b.p, u(x), u(y), u(x), u(y))
		b.open, b.px, b.py = true, x, y
		return
	}
	if x == b.px && y == b.py {
		return // a repeated point is a degenerate segment, and breaks joins
	}
	call(gpAddPathLineI, b.p, u(b.px), u(b.py), u(x), u(y))
	b.px, b.py = x, y
}

// addRounded opens a second figure in the same path, used to punch a hole.
func (b *pathBuilder) addTo(x, y int) {
	call(gpStartPathFigure, b.p)
	b.open = false
	b.add(x, y)
}

func (b *pathBuilder) closeFigure() { call(gpClosePathFigure, b.p) }

// finish closes the figure and hands the path over; the caller fills it and then
// frees it with freePath.
func (b *pathBuilder) finish() uintptr {
	b.closeFigure()
	return b.p
}

// ---------------------------------------------------------------- text

type fontSpec struct {
	font     uintptr
	format   uintptr
	align    int
	lineAl   int
	trimming int
}

// newFont builds a font from a LOGFONTW. face is a family name such as
// "Segoe UI"; height is negative for a character height, as LOGFONT expects.
func newFont(face string, height, weight int) *fontSpec {
	lf := LOGFONTW{
		Height:  int32(height),
		Weight:  int32(weight),
		CharSet: 1, // DEFAULT_CHARSET, so CJK falls back properly
		Quality: 5, // CLEARTYPE_QUALITY; GDI+ then maps it to its own hinting
	}
	copy(lf.FaceName[:], syscall.StringToUTF16(face))
	hdc := call(pGetDC, 0)
	defer call(pReleaseDC, 0, hdc)
	fs := &fontSpec{}
	gpStatus("CreateFontFromLogfontW", call(gpCreateFontFromLogfont, hdc,
		uintptr(unsafe.Pointer(&lf)), uptr(&fs.font)))
	var fmtp uintptr
	gpStatus("CreateStringFormat", call(gpCreateStringFormat, 0, 0, uptr(&fmtp)))
	fs.format = fmtp
	return fs
}

func (f *fontSpec) setAlign(align, lineAlign int) {
	f.align, f.lineAl = align, lineAlign
	call(gpSetStringFormatAlign, f.format, u(align))
	call(gpSetStringFormatLineAlign, f.format, u(lineAlign))
}

func (f *fontSpec) setTrimming(mode int) {
	f.trimming = mode
	call(gpSetStringFormatTrimming, f.format, u(mode))
}

func (f *fontSpec) free() {
	if f.format != 0 {
		call(gpDeleteStringFormat, f.format)
	}
	if f.font != 0 {
		call(gpDeleteFont, f.font)
	}
}

// drawText draws single-line text into a box, with a solid colour.
func (s *surface) drawText(text string, x, y, w, h float64, argb uint32, f *fontSpec) {
	br := s.solidBrush(argb)
	box := GpRectF{X: float32(x), Y: float32(y), W: float32(w), H: float32(h)}
	str, _ := syscall.UTF16PtrFromString(text)
	gpStatus("DrawString", call(gpDrawString, s.g, uintptr(unsafe.Pointer(str)), ^uintptr(0),
		f.font, uintptr(unsafe.Pointer(&box)), f.format, br))
	freeBrush(br)
}

// measureText returns the width and height the text needs.
func (s *surface) measureText(text string, x, y, w, h float64, f *fontSpec) (float64, float64) {
	box := GpRectF{X: float32(x), Y: float32(y), W: float32(w), H: float32(h)}
	var out GpRectF
	str, _ := syscall.UTF16PtrFromString(text)
	gpStatus("MeasureString", call(gpMeasureString, s.g, uintptr(unsafe.Pointer(str)), ^uintptr(0),
		f.font, uintptr(unsafe.Pointer(&box)), f.format,
		uintptr(unsafe.Pointer(&out)), 0, 0))
	return float64(out.W), float64(out.H)
}

// ---------------------------------------------------------------- images

// gpImage is a decoded image (a file on disk, or another GpBitmap).
type gpImage struct {
	img uintptr
	W   int
	H   int
}

func loadImageFile(path string) (*gpImage, bool) {
	var bmp uintptr
	if st := call(gpCreateBitmapFromFile, utf16Ptr(path), uptr(&bmp)); st != 0 {
		return nil, false
	}
	var w, h uint32
	call(gpGetImageWidth, bmp, uptr(&w))
	call(gpGetImageHeight, bmp, uptr(&h))
	return &gpImage{img: bmp, W: int(w), H: int(h)}, true
}

func imageFromBitmap(bmp uintptr) *gpImage {
	var w, h uint32
	call(gpGetImageWidth, bmp, uptr(&w))
	call(gpGetImageHeight, bmp, uptr(&h))
	return &gpImage{img: bmp, W: int(w), H: int(h)}
}

func (g *gpImage) free() {
	if g != nil && g.img != 0 {
		call(gpDisposeImage, g.img)
	}
}

// drawScaled draws an image stretched into the destination rectangle.
func (s *surface) drawScaled(g *gpImage, x, y, w, h int) {
	if g == nil || w <= 0 || h <= 0 {
		return
	}
	call(gpDrawImageRectI, s.g, g.img, u(x), u(y), u(w), u(h))
}

// drawScaledBitmap draws another surface, stretched into a rectangle. The source
// is identified by its bitmap handle rather than by a wrapper image, because the
// two surfaces each own their pixels and neither may be freed by the other.
//
// This is the workhorse of the renderer: every card is a surface being drawn onto
// the strip's surface.
func (s *surface) drawScaledBitmap(src *surface, x, y, w, h int) {
	if src == nil || src.bitmap == 0 || w <= 0 || h <= 0 {
		return
	}
	call(gpDrawImageRectI, s.g, src.bitmap, u(x), u(y), u(w), u(h))
}

// drawScaledRotated draws a surface scaled to w x h and rotated by angle radians
// about a pivot. The pivot is the source's top centre: (cx, cy) is where that
// point lands, which is exactly how a photo hangs from a clip.
func (s *surface) drawScaledRotated(src *surface, cx, cy, w, h, angle float64) {
	s.drawRotated(src, cx, cy, w, h, angle, w/2, 0)
}

// drawRotated is the general form: the source is scaled into a w x h rectangle
// and rotated by angle radians about the point (pdx, pdy) inside that rectangle,
// which lands at (cx, cy). A sprite with padding — a soft shadow, or a clip that
// pokes above the card — needs a pivot other than the top centre.
func (s *surface) drawRotated(src *surface, cx, cy, w, h, angle, pdx, pdy float64) {
	s.drawRotatedAlpha(src, cx, cy, w, h, angle, pdx, pdy, 1)
}

// drawRotatedAlpha additionally modulates the whole sprite's opacity, which the
// drop-in and slide animations need.
func (s *surface) drawRotatedAlpha(src *surface, cx, cy, w, h, angle, pdx, pdy, alpha float64) {
	if src == nil || src.bitmap == 0 || w <= 0 || h <= 0 || alpha <= 0.002 {
		return
	}
	corner := func(ix, iy float64) GpPoint {
		dx, dy := ix-pdx, iy-pdy
		sin, cos := math.Sin(angle), math.Cos(angle)
		return GpPoint{
			X: int32(math.Round(cx + dx*cos - dy*sin)),
			Y: int32(math.Round(cy + dx*sin + dy*cos)),
		}
	}
	pts := [3]GpPoint{corner(0, 0), corner(w, 0), corner(0, h)}
	attrs := alphaAttributes(alpha)
	gpStatus("DrawImagePointsRectI", call(gpDrawImagePointsRectI, s.g, src.bitmap,
		uintptr(unsafe.Pointer(&pts[0])), 3, 0, 0, u(src.W), u(src.H), unitPixel, attrs, 0, 0))
}

// alphaAttributes returns a cached ImageAttributes scaling everything by alpha.
// Alphas are quantised, so an animation reuses 32 handles instead of allocating
// one per frame.
var alphaAttrCache = map[int]uintptr{}

func alphaAttributes(alpha float64) uintptr {
	if alpha > 0.999 {
		return 0 // no attributes: draw opaque
	}
	if alpha < 0.004 {
		alpha = 0.004
	}
	q := int(alpha*31 + 0.5)
	if ia, ok := alphaAttrCache[q]; ok {
		return ia
	}
	var ia uintptr
	if st := call(gpCreateImageAttributes, uptr(&ia)); st != 0 {
		return 0
	}
	a := float32(float64(q) / 31)
	m := colorMatrix{
		1, 0, 0, 0, 0,
		0, 1, 0, 0, 0,
		0, 0, 1, 0, 0,
		0, 0, 0, a, 0,
		0, 0, 0, 0, 1,
	}
	gpStatus("SetImageAttributesColorMatrix", call(gpSetImageAttrColorMatrix, ia,
		colorAdjustTypeBitmap, 1, uintptr(unsafe.Pointer(&m)), 0, colorMatrixFlagsDefault))
	alphaAttrCache[q] = ia
	return ia
}

// colorMatrix is GDI+'s 5x5 ColorMatrix.
type colorMatrix [25]float32

// drawSubScaled draws part of an image into a destination rectangle, used when
// a card is clipped by the edge of the strip.
func (s *surface) drawSubScaled(g *gpImage, sx, sy, sw, sh, dx, dy, dw, dh int) {
	if g == nil || sw <= 0 || sh <= 0 || dw <= 0 || dh <= 0 {
		return
	}
	call(gpDrawImageRectRectI, s.g, g.img, u(dx), u(dy), u(dw), u(dh),
		u(sx), u(sy), u(sw), u(sh), unitPixel, 0, 0, 0)
}

// drawSubBitmap is drawSubScaled for a surface, whose bitmap is borrowed rather
// than owned. The glass reads its blurred backdrop this way.
func (s *surface) drawSubBitmap(src *surface, sx, sy, sw, sh, dx, dy, dw, dh int) {
	if src == nil || src.bitmap == 0 || sw <= 0 || sh <= 0 || dw <= 0 || dh <= 0 {
		return
	}
	call(gpDrawImageRectRectI, s.g, src.bitmap, u(dx), u(dy), u(dw), u(dh),
		u(sx), u(sy), u(sw), u(sh), unitPixel, 0, 0, 0)
}

// clipRounded applies a rounded-rectangle clip.
func (s *surface) clipRounded(x, y, w, h, r int) {
	path := roundedPath(x, y, w, h, r)
	gpStatus("SetClipPath", call(gpSetClipPath, s.g, path, combineReplace))
	freePath(path)
}

// clipPath applies an existing path as the clip region.
func (s *surface) clipPath(path uintptr) {
	call(gpSetClipPath, s.g, path, combineReplace)
}

func (s *surface) resetClip() { call(gpResetClip, s.g) }

// clipRect restricts drawing to a rectangle.
func (s *surface) clipRectPx(x, y, w, h int) {
	r := GpRect{X: int32(x), Y: int32(y), W: int32(w), H: int32(h)}
	gpStatus("SetClipRect", call(gpSetClipRectI, s.g, uintptr(unsafe.Pointer(&r)), combineReplace))
}

// blit draws one surface onto another at an integer offset.
func (s *surface) blit(src *surface, x, y int) {
	s.drawScaled(imageFromBitmap(src.bitmap), x, y, src.W, src.H)
}

// setOpaque forces every pixel's alpha to 255, in place. A screen capture has no
// alpha channel, so the captured bytes come back with alpha 0 and the surface
// would read as fully transparent — wrong for anything that inspects the desktop
// (a saved PNG, or a comparison against the composed frame).
func (s *surface) setOpaque() {
	p := s.pixels()
	for i := 3; i < len(p); i += 4 {
		p[i] = 255
	}
}

// snapshotSurface copies a surface's pixels into a new surface of its own.
//
// panel.push frees the surface it replaces, and any repaint hands over a new one, so
// a pointer to what was composed stops being valid the moment the app draws again.
// Code that has to inspect a frame later on works from a private copy instead.
func snapshotSurface(src *surface) *surface {
	dst := newSurface(src.W, src.H)
	copy(dst.pixels(), src.pixels())
	return dst
}

// flush forces GDI+ to finish writing before the pixels are read.
func (s *surface) flush() {
	call(gpFlush, s.g, flushIntentionSync)
}

// alphaAt reads one pixel's alpha, for tests.
func (s *surface) alphaAt(x, y int) byte {
	if x < 0 || y < 0 || x >= s.W || y >= s.H {
		return 0
	}
	return s.pixels()[(y*s.W+x)*4+3]
}

// colorAt returns the straight (un-premultiplied) RGBA of a pixel, for tests.
func (s *surface) colorAt(x, y int) (r, g, b, a byte) {
	if x < 0 || y < 0 || x >= s.W || y >= s.H {
		return
	}
	p := s.pixels()
	i := (y*s.W + x) * 4
	b, g, r, a = p[i], p[i+1], p[i+2], p[i+3]
	if a != 0 && a != 255 {
		r = byte(int(r) * 255 / int(a))
		g = byte(int(g) * 255 / int(a))
		b = byte(int(b) * 255 / int(a))
	}
	return
}

// rgbAt returns a pixel's colour ignoring alpha, which is what a screen capture
// needs: the desktop's pixels are opaque whatever the byte says.
func (s *surface) rgbAt(x, y int) (r, g, b byte) {
	if x < 0 || y < 0 || x >= s.W || y >= s.H {
		return
	}
	p := s.pixels()
	i := (y*s.W + x) * 4
	return p[i+2], p[i+1], p[i]
}
