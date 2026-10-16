package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

// clipboard.go is ClipboardWatcher.swift: when "Hang copied images" is on, a
// copied image hangs on the line too.
//
// The macOS original polls NSPasteboard's change count twice a second. Windows
// has a proper notification for this — AddClipboardFormatListener delivers
// WM_CLIPBOARDUPDATE — which is both immediate and free when the clipboard is
// idle, so that is what is used here.

type clipboardWatcher struct {
	hwnd    uintptr
	started bool
	lastSeq uint32
	// formatIDs are the clipboard formats that the original ignores on macOS:
	// plain text, file lists and concealed content are not images.
	skipFormats map[uint32]bool
}

func newClipboardWatcher() *clipboardWatcher {
	w := &clipboardWatcher{skipFormats: map[uint32]bool{}}
	// CF_TEXT, CF_BITMAP is *not* skipped (it is an image), CF_DIB is not either.
	for _, f := range []uint32{cfText, cfHDROP, clipFormatID("text/plain")} {
		if f != 0 {
			w.skipFormats[f] = true
		}
	}
	// "Concealed" is how a password manager marks the clipboard: Apple and the
	// Windows ecosystem agree on the name, and it means "do not look at this".
	if f := clipFormatID("ExcludeClipboardContentFromMonitorProcessing"); f != 0 {
		w.skipFormats[f] = true
	}
	if f := clipFormatID("ClipboardViewerIgnore"); f != 0 {
		w.skipFormats[f] = true
	}
	return w
}

func (c *clipboardWatcher) start(hwnd uintptr) {
	if c.started {
		return
	}
	c.hwnd = hwnd
	if call(pAddClipboardFormatL, hwnd) == 0 {
		logf("could not listen to the clipboard: %d", lastError())
		return
	}
	c.started = true
	c.lastSeq = uint32(call(pGetClipboardSequenceNumber))
	logf("listening to the clipboard")
}

func (c *clipboardWatcher) stop() {
	if !c.started {
		return
	}
	c.started = false
	call(pRemoveClipboardFmtL, c.hwnd)
	logf("stopped listening to the clipboard")
}

// check looks at the clipboard and, if it holds an image, saves it next to the
// other screenshots and hands it to onImage. Called from the message loop when
// WM_CLIPBOARDUPDATE arrives.
func (c *clipboardWatcher) check(onImage func(string)) {
	// A duplicate notification is possible (several formats set in a row), so the
	// sequence number gates the work.
	seq := uint32(call(pGetClipboardSequenceNumber))
	if seq == c.lastSeq {
		return
	}
	c.lastSeq = seq

	// A file list whose files are images is the one case worth following: copying
	// a photo in Explorer should hang it on the line.
	if path := c.clipboardImageFile(); path != "" {
		onImage(path)
		return
	}
	if !c.clipboardHoldsImage() {
		return
	}
	data, ok := c.readClipboardImage()
	if !ok || len(data) == 0 {
		return
	}
	name := "Clipboard " + time.Now().Format("2006-01-02 at 15.04.05") + ".png"
	target := uniquePath(filepath.Join(inboxFolder(), name))
	if err := os.WriteFile(target, data, 0o644); err != nil {
		logf("could not save the copied image: %v", err)
		return
	}
	logf("a copied image was saved as %s", target)
	onImage(target)
}

// clipboardHoldsImage reports whether the clipboard carries an image, and not
// only text or files.
func (c *clipboardWatcher) clipboardHoldsImage() bool {
	if !openClipboard() {
		return false
	}
	defer call(pCloseClipboard)

	hasImage := false
	onlySkipped := true
	for f := uint32(call(pEnumClipboardFormats, 0)); f != 0; f = uint32(call(pEnumClipboardFormats, u(int(f)))) {
		if !c.skipFormats[f] {
			onlySkipped = false
		}
		if f == cfDIB || f == cfDIBv5 || f == cfBitmap {
			hasImage = true
		}
	}
	// A pure text copy must not be treated as an image.
	return hasImage && !onlySkipped
}

// clipboardImageFile returns the first image file in a copied file list, which is
// what copying a photo in Explorer puts on the clipboard.
func (c *clipboardWatcher) clipboardImageFile() string {
	if call(pIsClipboardFormatAv, cfHDROP) == 0 {
		return ""
	}
	if !openClipboard() {
		return ""
	}
	defer call(pCloseClipboard)
	h := call(pGetClipboardData, cfHDROP)
	if h == 0 {
		return ""
	}
	count := call(pDragQueryFile, h, ^uintptr(0), 0, 0)
	for i := uintptr(0); i < count; i++ {
		buf := make([]uint16, 1024)
		if call(pDragQueryFile, h, i, uintptr(unsafe.Pointer(&buf[0])), u(len(buf))) == 0 {
			continue
		}
		p := syscall.UTF16ToString(buf)
		if isImagePath(p) || isRecording(p) {
			return p
		}
	}
	return ""
}

// readClipboardImage encodes the clipboard's image as PNG, which is the format
// the rest of the app expects and the one that keeps alpha.
func (c *clipboardWatcher) readClipboardImage() ([]byte, bool) {
	if !openClipboard() {
		return nil, false
	}
	defer call(pCloseClipboard)

	for _, format := range []uint32{cfDIBv5, cfDIB} {
		h := call(pGetClipboardData, u(int(format)))
		if h == 0 {
			continue
		}
		bits := call(pGlobalLock, h)
		if bits == 0 {
			continue
		}
		size := call(pGlobalSize, h)
		// The block is locked, so its address is stable for this window.
		data := lockedBytes(bits, int(size))
		png, ok := dibToPNG(data, format == cfDIBv5)
		call(pGlobalUnlock, h)
		if ok {
			return png, true
		}
	}
	return nil, false
}

// dibToPNG converts a clipboard DIB into PNG bytes by way of a GDI+ bitmap.
//
// A DIB on the clipboard is a BITMAPINFOHEADER followed by the pixels. The
// header's height is positive for a bottom-up bitmap, which is the opposite of
// what GDI+ expects, so a negative-height copy of the header is built for the
// scan0 bitmap; the pixels themselves are copied verbatim.
func dibToPNG(data []byte, v5 bool) ([]byte, bool) {
	if len(data) < 40 {
		return nil, false
	}
	headerSize := int(binary.LittleEndian.Uint32(data[0:4]))
	width := int(int32(binary.LittleEndian.Uint32(data[4:8])))
	height := int(int32(binary.LittleEndian.Uint32(data[8:12])))
	bitCount := int(binary.LittleEndian.Uint16(data[14:16]))
	compression := binary.LittleEndian.Uint32(data[16:20])
	if width <= 0 || height == 0 || headerSize < 40 || headerSize > len(data) {
		return nil, false
	}
	// BI_BITFIELDS (3) is what a 32-bit clipboard DIB with alpha uses, and
	// BI_RGB (0) is the plain case. Anything else (JPEG, PNG-in-DIB) is skipped.
	if compression != 0 && compression != 3 {
		return nil, false
	}
	if bitCount != 24 && bitCount != 32 {
		return nil, false
	}
	topDown := height < 0
	if topDown {
		height = -height
	}

	pixelOffset := headerSize
	if compression == 3 {
		// Three or four color masks follow the header.
		masks := 12
		if v5 {
			masks = 16
		}
		pixelOffset += masks
	}
	stride := ((width*bitCount + 31) / 32) * 4
	if pixelOffset+stride*height > len(data) {
		return nil, false
	}

	// Build a 32bpp top-down BGRA buffer, converting 24-bit rows if needed.
	pixels := make([]byte, width*height*4)
	for row := 0; row < height; row++ {
		srcRow := row
		if !topDown {
			srcRow = height - 1 - row
		}
		src := data[pixelOffset+srcRow*stride:]
		dst := pixels[row*width*4:]
		switch bitCount {
		case 32:
			copy(dst, src[:width*4])
		case 24:
			for x := 0; x < width; x++ {
				dst[x*4] = src[x*3]
				dst[x*4+1] = src[x*3+1]
				dst[x*4+2] = src[x*3+2]
				dst[x*4+3] = 255
			}
		}
	}
	// A 32-bit DIB with no alpha channel uses the top byte as padding; forcing
	// every pixel opaque in that case is what the clipboard's own consumers do.
	if bitCount == 32 && !v5 {
		hasAlpha := false
		for i := 3; i < len(pixels); i += 4 {
			if pixels[i] != 0 {
				hasAlpha = true
				break
			}
		}
		if !hasAlpha {
			for i := 3; i < len(pixels); i += 4 {
				pixels[i] = 255
			}
		}
	}

	// A GDI+ bitmap over the copied pixels, then a PNG in memory.
	var bmp uintptr
	if st := call(gpCreateBitmapFromScan0, u(width), u(height), u(width*4),
		pixelFormat32bppPARGB, uintptr(unsafe.Pointer(&pixels[0])), uptr(&bmp)); st != 0 {
		return nil, false
	}
	defer call(gpDisposeImage, bmp)

	out, ok := encodePNGToMemory(bmp)
	if !ok {
		return nil, false
	}
	return out, true
}

// encodePNGToMemory writes a GDI+ bitmap to a PNG held in memory, using an
// IStream over a HGLOBAL.
func encodePNGToMemory(bmp uintptr) ([]byte, bool) {
	const gmemMoveable = 0x0002
	const stgmCreate = 0x00001000
	const stgmRead = 0x00000000

	h := call(pGlobalAlloc, gmemMoveable, 0)
	var stream uintptr
	if r := call(pCreateStreamOnHGlobal, h, 1, uptr(&stream)); r != 0 || stream == 0 {
		if h != 0 {
			call(pGlobalFree, h)
		}
		return nil, false
	}
	defer releaseStream(stream)

	if st := call(gpSaveImageToStream, bmp, stream, uptr(&pngEncoderCLSID[0]), 0); st != 0 {
		return nil, false
	}

	// Read the stream back out.
	var hglobal uintptr
	if r := call(pGetHGlobalFromStream, stream, uptr(&hglobal)); r != 0 || hglobal == 0 {
		return nil, false
	}
	size := call(pGlobalSize, hglobal)
	bits := call(pGlobalLock, hglobal)
	if bits == 0 || size == 0 {
		return nil, false
	}
	defer call(pGlobalUnlock, hglobal)
	out := make([]byte, int(size))
	copy(out, lockedBytes(bits, int(size)))
	return out, true
}

// releaseStream releases an IStream, which this code only borrows.
func releaseStream(stream uintptr) { releaseStreamNow(stream) }

// ---------------------------------------------------------------- clipboard out

// copyToClipboard puts an image on the clipboard, and the file list alongside it
// so pasting into Explorer or a chat window still works.
//
// The original copies the image itself with NSPasteboard. On Windows the richest
// form is a file-list plus the decoded image, which is what Explorer itself
// produces when you copy a picture — so pasting into Word gives the picture,
// pasting into Explorer gives the file, and pasting into a chat window gives
// either, whichever it prefers.
func copyToClipboard(path string, recording bool) error {
	if !openClipboard() {
		return fmt.Errorf("could not open the clipboard")
	}
	defer call(pCloseClipboard)
	if call(pEmptyClipboard) == 0 {
		return fmt.Errorf("could not empty the clipboard")
	}

	payload := map[uint32][]byte{}
	// The file list: a DROPFILES struct followed by a double-NUL-terminated list
	// of absolute paths.
	payload[cfHDROP] = dropFilesFor([]string{path})
	// The path as text too, so pasting into a terminal or a text field works.
	payload[cfText] = utf16Bytes(path)

	if !recording {
		if data, ok := fileToDIB(path); ok {
			payload[cfDIB] = data
		}
	}
	return setClipboardPayload(payload)
}

// dropFilesFor builds the DROPFILES block the shell expects for a file list.
func dropFilesFor(paths []string) []byte {
	const dropFilesSize = 20
	const fDropFilesWide = 1
	var names []uint16
	for _, p := range paths {
		names = append(names, syscall.StringToUTF16(p)...)
	}
	names = append(names, 0, 0)
	out := make([]byte, dropFilesSize+len(names)*2)
	binary.LittleEndian.PutUint32(out[0:], dropFilesSize) // pFiles offset
	out[16] = fDropFilesWide                              // fWide = TRUE
	for i, c := range names {
		binary.LittleEndian.PutUint16(out[dropFilesSize+i*2:], c)
	}
	return out
}

// fileToDIB decodes a file and hands back a clipboard DIB (a BITMAPINFOHEADER
// with the pixels below it, bottom-up as the clipboard expects).
func fileToDIB(path string) ([]byte, bool) {
	img, ok := loadImageFile(path)
	if !ok {
		return nil, false
	}
	defer img.free()
	w, h := img.W, img.H
	if w <= 0 || h <= 0 || w > 20000 || h > 20000 {
		return nil, false
	}
	s := newSurface(w, h)
	defer s.free()
	s.clear()
	s.drawScaled(img, 0, 0, w, h)
	s.flush()

	src := s.pixels() // premultiplied BGRA, top-down
	stride := w * 4
	out := make([]byte, 40+stride*h)
	binary.LittleEndian.PutUint32(out[0:], 40)               // biSize
	binary.LittleEndian.PutUint32(out[4:], uint32(int32(w))) // biWidth
	binary.LittleEndian.PutUint32(out[8:], uint32(int32(h))) // biHeight, bottom-up
	binary.LittleEndian.PutUint16(out[12:], 1)               // biPlanes
	binary.LittleEndian.PutUint16(out[14:], 32)              // biBitCount
	binary.LittleEndian.PutUint32(out[16:], 0)               // BI_RGB
	binary.LittleEndian.PutUint32(out[20:], uint32(stride*h))

	// Flip to bottom-up and un-premultiply: a CF_DIB has no premultiplied form.
	for row := 0; row < h; row++ {
		srcRow := src[row*stride:]
		dstRow := out[40+(h-1-row)*stride:]
		for x := 0; x < w; x++ {
			b, g, r, a := srcRow[x*4], srcRow[x*4+1], srcRow[x*4+2], srcRow[x*4+3]
			if a != 0 && a != 255 {
				r = byte(int(r) * 255 / int(a))
				g = byte(int(g) * 255 / int(a))
				b = byte(int(b) * 255 / int(a))
			}
			dstRow[x*4], dstRow[x*4+1], dstRow[x*4+2], dstRow[x*4+3] = b, g, r, a
		}
	}
	return out, true
}

// setClipboardPayload puts a set of formats on an already-open, emptied
// clipboard. Ownership of each block passes to the clipboard on success.
func setClipboardPayload(payload map[uint32][]byte) error {
	const gmemMoveable = 0x0002
	type entry struct {
		format uint32
		handle uintptr
	}
	var placed []entry
	fail := func() error {
		for _, e := range placed {
			call(pGlobalFree, e.handle)
		}
		return fmt.Errorf("could not set the clipboard data")
	}
	for format, data := range payload {
		if len(data) == 0 {
			continue
		}
		h := call(pGlobalAlloc, gmemMoveable, u(len(data)))
		if h == 0 {
			continue
		}
		ptr := call(pGlobalLock, h)
		if ptr == 0 {
			call(pGlobalFree, h)
			continue
		}
		copy(unsafe.Slice((*byte)(unsafe.Pointer(ptr)), len(data)), data)
		call(pGlobalUnlock, h)
		if call(pSetClipboardData, u(int(format)), h) == 0 {
			call(pGlobalFree, h)
			continue
		}
		placed = append(placed, entry{format, h})
	}
	if len(placed) == 0 {
		return fail()
	}
	// One retry loop, because SetClipboardData can be refused while another
	// process still holds the clipboard open.
	for attempt := 0; attempt < 8; attempt++ {
		if openClipboard() {
			return nil
		}
		sleepMS(25)
	}
	return nil
}

// openClipboard retries, because a clipboard that another app is using is a
// normal, transient condition rather than an error.
func openClipboard() bool {
	for attempt := 0; attempt < 8; attempt++ {
		if call(pOpenClipboard, 0) != 0 {
			return true
		}
		sleepMS(20)
	}
	return false
}

// cfBitmap is CF_BITMAP.
const cfBitmap = 2
