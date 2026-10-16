package main

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

// util.go collects the platform chores: known folders, opening and editing
// files, the Recycle Bin, the clipboard, and the app's own icon, which is drawn
// in code rather than shipped as a bitmap so the binary stays self-contained.

// ---------------------------------------------------------------- timing

// after is defined in ui.go: these delays stand in for DispatchQueue.main
// after-delays, so the work they carry has to arrive on the window's thread.

func randRange(lo, hi float64) float64 { return lo + rand.Float64()*(hi-lo) }

func itoa(n int) string { return strconv.Itoa(n) }

// ---------------------------------------------------------------- known folders

// GUID layout, as Windows defines it.
type GUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	// {33E28130-4E1E-4676-835A-98395C3BC3BB}
	folderIDPictures = GUID{0x33E28130, 0x4E1E, 0x4676, [8]byte{0x83, 0x5A, 0x98, 0x39, 0x5C, 0x3B, 0xC3, 0xBB}}
	// {B4BFCC3A-DB2C-424C-B029-7FE99A87C641}
	folderIDDesktop = GUID{0xB4BFCC3A, 0xDB2C, 0x424C, [8]byte{0xB0, 0x29, 0x7F, 0xE9, 0x9A, 0x87, 0xC6, 0x41}}
	// {B7BEDE81-DF94-4682-A7D8-57A71709C8E8} — where Win+PrtScn and Snipping Tool save
	folderIDScreenshots = GUID{0xB7BEDE81, 0xDF94, 0x4682, [8]byte{0xA7, 0xD8, 0x57, 0xA7, 0x17, 0x09, 0xC8, 0xE8}}
)

// knownFolder resolves a shell folder. It creates the folder when the shell
// would, which is what makes SHGetKnownFolderPath useful for Screenshots.
func knownFolder(id *GUID) string {
	var out *uint16
	if hr := call(pSHGetKnownFolder, uintptr(unsafe.Pointer(id)), 0, 0, uptr(&out)); hr != 0 || out == nil {
		return ""
	}
	path := utf16ToString(out)
	call(pCoTaskMemFree, uintptr(unsafe.Pointer(out)))
	return path
}

func desktopFolder() string {
	if p := knownFolder(&folderIDDesktop); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Desktop")
}

// screenshotsFolder is the folder Windows saves screenshots to. Win+PrtScn and
// the Snipping Tool both resolve it through this known folder, so redirecting it
// (see inbox.go) is the honest Windows equivalent of the original changing where
// macOS writes screenshots.
func screenshotsFolder() string {
	if p := knownFolder(&folderIDScreenshots); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Pictures", "Screenshots")
	}
	return filepath.Join(localDataDir(), "Screenshots")
}

// inboxFolder is Windline's own screenshot folder, the counterpart of the
// original's Application Support/Tendedero/Screenshots.
func inboxFolder() string {
	dir := filepath.Join(localDataDir(), "Screenshots")
	os.MkdirAll(dir, 0o755)
	return dir
}

// ---------------------------------------------------------------- shell

// shellOpen opens a file with the user's default application.
func shellOpen(path string) {
	const swShownormal = 1
	r := call(pShellExecute, 0, utf16Ptr("open"), utf16Ptr(path), 0, 0, swShownormal)
	if r <= 32 {
		logf("ShellExecute(open) on %s returned %d", path, r)
	}
}

// runEditor opens a file in its registered editor: for an image that is Paint,
// which is what macOS's Markup editor is to a screenshot. The "edit" verb is
// what Photo Viewer and friends register for exactly this.
func runEditor(path string) {
	const swShownormal = 1
	r := call(pShellExecute, 0, utf16Ptr("edit"), utf16Ptr(path), 0, 0, swShownormal)
	if r <= 32 {
		logf("no editor for %s (ShellExecute edit returned %d), opening it instead", path, r)
		shellOpen(path)
	}
}

// recycle sends a file to the Recycle Bin, the counterpart of macOS's
// trashItem. Using the shell's own operation means the user can get it back, and
// a file on a network drive falls back to a real delete inside the shell.
func recycle(path string) error {
	const (
		foDelete          = 3
		fofAllowUndo      = 0x0040
		fofNoConfirmation = 0x0010
		fofSilent         = 0x0004
		fofNoErrorUI      = 0x0400
	)
	from := utf16DoubleNull(path)
	op := shFileOpStructW{
		WFunc:  foDelete,
		PFrom:  uintptr(unsafe.Pointer(&from[0])),
		FFlags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI,
	}
	r := call(pSHFileOperation, uptr(&op))
	if r != 0 {
		return fmt.Errorf("SHFileOperation returned %d", r)
	}
	if op.FAnyOperationsAborted != 0 {
		return fmt.Errorf("delete was aborted")
	}
	return nil
}

type shFileOpStructW struct {
	Hwnd                  uintptr
	WFunc                 uint32
	PFrom                 uintptr
	PTo                   uintptr
	FFlags                uint16
	FAnyOperationsAborted int32
	HNameMappings         uintptr
	LpszProgressTitle     uintptr
}

// utf16DoubleNull encodes a path list for the shell, which wants a
// double-NUL-terminated block.
func utf16DoubleNull(paths ...string) []uint16 {
	var out []uint16
	for _, p := range paths {
		out = append(out, syscall.StringToUTF16(p)...)
	}
	if len(out) == 0 || out[len(out)-1] != 0 {
		out = append(out, 0)
	}
	out = append(out, 0)
	return out
}

// ---------------------------------------------------------------- clipboard

// clipboardText puts plain text on the clipboard.
func clipboardText(text string) error {
	if !openClipboard() {
		return fmt.Errorf("could not open the clipboard")
	}
	defer call(pCloseClipboard)
	if call(pEmptyClipboard) == 0 {
		return fmt.Errorf("could not empty the clipboard")
	}
	return setClipboardPayload(map[uint32][]byte{cfText: utf16Bytes(text)})
}

func utf16Bytes(s string) []byte {
	u := syscall.StringToUTF16(s)
	out := make([]byte, len(u)*2)
	for i, c := range u {
		out[i*2] = byte(c)
		out[i*2+1] = byte(c >> 8)
	}
	return out
}

// ---------------------------------------------------------------- creation time

func creationTimeOf(info os.FileInfo) (time.Time, bool) {
	if info == nil {
		return time.Time{}, false
	}
	// On Windows os.FileInfo's Sys() is a *syscall.Win32FileAttributeData with a
	// real creation time.
	if d, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return time.Unix(0, d.CreationTime.Nanoseconds()), true
	}
	return time.Time{}, false
}

// ---------------------------------------------------------------- ICO

// logoRenderer draws the app's icon in code, at whatever size the taskbar and
// the tray ask for, and caches one HICON per size and variant.
//
// The icon is deliberately its own drawing, not the upstream app's: the MIT
// licence covers the source but not the name or the artwork, and a fork has to
// bring its own. This one is a rope with a pair of clips and one framed photo,
// which is the same idea told differently.
type logoRenderer struct {
	cache map[logoKey]uintptr
}

type logoKey struct {
	size   int
	copied bool
}

func newLogoRenderer() *logoRenderer {
	return &logoRenderer{cache: map[logoKey]uintptr{}}
}

// icon returns an HICON for the given size, drawing it on first use.
func (l *logoRenderer) icon(size int, clipboardOn bool) uintptr {
	if size < 8 {
		size = 8
	}
	key := logoKey{size: size, copied: clipboardOn}
	if h, ok := l.cache[key]; ok {
		return h
	}
	h := l.create(size, clipboardOn)
	l.cache[key] = h
	return h
}

func (l *logoRenderer) create(size int, clipboardOn bool) uintptr {
	s := newSurface(size, size)
	defer s.free()
	s.clear()
	drawLogoPrimitives(s, size, clipboardOn)
	s.flush()
	return surfaceToIcon(s)
}

// drawLogoPrimitives draws the app's icon into a surface of the given size. It is
// separate from create so the same drawing can also be composed straight into a
// PNG by the selftest, without going through an HICON.
func drawLogoPrimitives(s *surface, size int, clipboardOn bool) {
	s.setQuality()

	// Background plate: a rounded square, slate blue into near-black.
	radius := max(2, size*22/100)
	plate := roundedPath(0, 0, size, size, radius)
	bg := s.vGrad(0, 0, size, rgba(51, 68, 94, 255), rgba(20, 27, 38, 255))
	s.fillPath(bg, plate)
	freePath(plate)
	freeBrush(bg)
	// A thin top-lit rim.
	rim := ringPath(0, 0, size, size, radius, max(1, size/24))
	rimBrush := s.vGrad(0, 0, size, rgba(255, 255, 255, 119), rgba(255, 255, 255, 34))
	s.fillPath(rimBrush, rim)
	freePath(rim)
	freeBrush(rimBrush)

	// The rope, sagging a little, drawn as short quads because a stroke would
	// need a pen whose width is a REAL this FFI layer cannot pass.
	ropeY := float64(size) * 0.30
	thickness := float64(max(1, size/26))
	yAt := func(t float64) float64 {
		x := -20 + t*(float64(size)+40)
		f := x / float64(size)
		return ropeY + 4*float64(size)*0.06*f*(1-f)
	}
	ropeSteps := max(12, size)
	for i := 0; i < ropeSteps; i++ {
		t0 := float64(i) / float64(ropeSteps)
		t1 := float64(i+1) / float64(ropeSteps)
		fillSegment(s, t0*float64(size), yAt(t0), t1*float64(size), yAt(t1),
			thickness, rgba(196, 200, 208, 255))
	}

	// One framed photo hanging from the rope on two clips.
	frameW := size * 46 / 100
	frameH := size * 40 / 100
	frameX := (size - frameW) / 2
	frameY := int(ropeY) + size*14/100
	if frameW < 2 || frameH < 2 {
		return
	}
	photo := s.vGrad(frameX, frameY, frameY+frameH, rgba(255, 255, 255, 255), rgba(220, 227, 236, 255))
	frame := roundedPath(frameX, frameY, frameW, frameH, max(1, size*8/100))
	s.fillPath(photo, frame)
	freePath(frame)
	freeBrush(photo)

	// A small landscape inside the frame: sky over a hill, so the icon is
	// recognisable even at 16 px.
	inset := max(1, size/16)
	innerW := frameW - inset*2
	innerH := frameH - inset*2
	if innerW > 2 && innerH > 2 {
		innerX := frameX + inset
		innerY := frameY + inset
		sky := s.vGrad(innerX, innerY, innerY+innerH, rgba(111, 168, 220, 255), rgba(58, 110, 165, 255))
		skyPath := roundedPath(innerX, innerY, innerW, innerH, max(0, size/12))
		s.fillPath(sky, skyPath)
		freePath(skyPath)
		freeBrush(sky)

		hill := s.solidBrush(rgba(52, 96, 72, 255))
		hillPath := ellipsePath(innerX-innerW/4, innerY+innerH/3, innerW*3/2, innerH*2/3)
		s.fillPath(hill, hillPath)
		freePath(hillPath)
		freeBrush(hill)
	}

	// The two clips gripping the rope.
	clipW := max(2, size*9/100)
	clipH := max(3, size*26/100)
	for _, cx := range []int{frameX + frameW/4, frameX + frameW*3/4} {
		metal := s.lineBrushStops(cx-clipW/2, 0, cx+clipW/2, 0,
			[]uint32{rgba(179, 179, 179, 255), rgba(237, 237, 237, 255), rgba(209, 209, 209, 255), rgba(158, 158, 158, 255)},
			[]float32{0, 0.35, 0.65, 1})
		body := roundedPath(cx-clipW/2, int(ropeY)-clipH/3, clipW, clipH, max(1, clipW/3))
		s.fillPath(metal, body)
		freePath(body)
		freeBrush(metal)
	}

	// When the clipboard watcher is on, a small filled dot in the corner says so
	// at a glance.
	if clipboardOn {
		d := max(3, size*22/100)
		dot := s.solidBrush(rgba(127, 209, 160, 255))
		dotPath := ellipsePath(size-d-max(1, size/12), max(1, size/12), d, d)
		s.fillPath(dot, dotPath)
		freePath(dotPath)
		freeBrush(dot)
	}
}

// surfaceToIcon converts a composed surface into an HICON.
func surfaceToIcon(s *surface) uintptr {
	var hbmp uintptr
	gpStatus("CreateHBITMAPFromBitmap", call(gpCreateHBITMAPFromBitmap, s.bitmap, uptr(&hbmp), 0))
	if hbmp == 0 {
		return 0
	}
	// A colour bitmap with an alpha channel uses a zeroed monochrome mask; the
	// mask is still required, but nothing is read from it when it is all zero and
	// the colour bitmap carries alpha.
	mask := createMonochromeBitmap(s.W, s.H)
	info := iconInfo{FIcon: 1, HbmColor: hbmp, HbmMask: mask}
	icon := call(pCreateIconIndirect, uptr(&info))
	call(gdi32DeleteObject, hbmp)
	if mask != 0 {
		call(gdi32DeleteObject, mask)
	}
	return icon
}

type iconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	HbmMask  uintptr
	HbmColor uintptr
}

func createMonochromeBitmap(w, h int) uintptr {
	// CreateBitmap(w, h, planes=1, bitsPerPixel=1, bits=nil)
	return call(gdi32CreateBitmap, u(w), u(h), 1, 1, 0)
}

// iconForExe returns the icon Windows associates with our own executable, which
// is the one we drew at build time.
func exePath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}
