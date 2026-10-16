package main

import "syscall"

// gdi32.go holds the GDI calls. Everything here takes integers or pointers, so
// it is all directly callable. GDI is used for the one job GDI+ is bad at on a
// layered window: grabbing the pixels behind the strip, which is what makes the
// glass frames frosted instead of plain translucent white.

var (
	dGdi32 = syscall.NewLazyDLL("gdi32.dll")

	gdi32CreateCompatibleDC  = dGdi32.NewProc("CreateCompatibleDC")
	gdi32CreateCompatibleBmp = dGdi32.NewProc("CreateCompatibleBitmap")
	gdi32CreateDIBSection    = dGdi32.NewProc("CreateDIBSection")
	gdi32SelectObject        = dGdi32.NewProc("SelectObject")
	gdi32DeleteObject        = dGdi32.NewProc("DeleteObject")
	gdi32DeleteDC            = dGdi32.NewProc("DeleteDC")
	gdi32BitBlt              = dGdi32.NewProc("BitBlt")
	gdi32StretchBlt          = dGdi32.NewProc("StretchBlt")
	gdi32SetStretchBltMode   = dGdi32.NewProc("SetStretchBltMode")
	gdi32GetObject           = dGdi32.NewProc("GetObjectW")
	gdi32CreateRectRgn       = dGdi32.NewProc("CreateRectRgn")
	gdi32DeleteRgn           = dGdi32.NewProc("DeleteRgn")
	gdi32GetDeviceCaps       = dGdi32.NewProc("GetDeviceCaps")
	gdi32CreateSolidBrush    = dGdi32.NewProc("CreateSolidBrush")
	msimg32AlphaBlend        = syscall.NewLazyDLL("msimg32.dll").NewProc("AlphaBlend")
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

const (
	srcCopy      = 0x00CC0020
	captureBlt   = 0x40000000
	halftone     = 4
	colorOnColor = 3
	dibRGBColors = 0
	biRGB        = 0
	logPixelsX   = 88
)

// screenDC returns the desktop's device context. Used only to copy pixels out of
// the screen (for the glass) and never to draw on it.
func screenDC() uintptr {
	hdc := call(pGetDC, 0)
	return hdc
}

func releaseDC(hdc uintptr) { call(pReleaseDC, 0, hdc) }

// captureInto copies a rectangle of the screen into a surface. When the
// rectangle extends past the screen bounds, the missing part is left as it was:
// the caller sizes the surface to the intersection.
//
// SRCCOPY alone can miss layered windows (the line itself included), so
// CAPTUREBLT is included.
func captureInto(s *surface, srcX, srcY int) {
	hdc := screenDC()
	defer releaseDC(hdc)
	call(gdi32SetStretchBltMode, s.memDC, halftone)
	call(gdi32BitBlt, s.memDC, 0, 0, u(s.W), u(s.H), hdc, u(srcX), u(srcY), srcCopy|captureBlt)
}

// blitSurface copies one surface into another at an integer offset.
func blitSurface(dst, src *surface, x, y int) {
	call(gdi32SetStretchBltMode, dst.memDC, halftone)
	call(gdi32BitBlt, dst.memDC, u(x), u(y), u(src.W), u(src.H), src.memDC, 0, 0, srcCopy)
}

// alphaBlit composites a surface onto another with GDI's per-pixel alpha, which
// is faster than GDI+ for whole-surface copies.
func alphaBlit(dst, src *surface, x, y int) {
	bf := BLENDFUNCTION{BlendOp: 0, BlendFlags: 0, SourceConstantAlpha: 255, AlphaFormat: 1}
	call(msimg32AlphaBlend, dst.memDC, u(x), u(y), u(src.W), u(src.H),
		src.memDC, 0, 0, u(src.W), u(src.H), uptr(&bf))
}

func deviceDPI(hdc uintptr) int {
	r := call(gdi32GetDeviceCaps, hdc, logPixelsX)
	if r == 0 {
		return 96
	}
	return int(r)
}
