package main

import "syscall"

// drag.go lets a photo be dragged out of the line and into another application:
// Explorer, a chat window, an image editor.
//
// The macOS original begins an NSDraggingSession with the file on the pasteboard.
// The Windows equivalent is OLE drag and drop, and the subtlety is that OLE wants
// an IDataObject — an 8-method COM interface — which is a lot of machinery to
// hand-build through syscall.
//
// There is a much better way: the shell already implements exactly this object.
// Put the file list on the clipboard, ask the shell for the clipboard's data
// object with OleGetClipboard, and hand that to DoDragDrop. The result is
// identical to what Explorer does when you drag a file, including the drag image
// and the copy/move decision, and it needs no COM implementation of our own.

// doDragDrop is the shell's drag loop, which blocks until the drag ends and runs
// its own message loop while it does.
//
// It must be called from the thread that owns the source window, which is the
// message-loop thread, and only while the mouse button is still down. The panel
// does exactly that: it starts the drag from WM_MOUSEMOVE with the button held.
func (a *app) beginSystemDrag(id int64) {
	it := a.line.find(id)
	if it == nil {
		return
	}
	// OLE takes its own capture, so release ours first or the drag will not track.
	call(pReleaseCapture)

	// While a drag is in flight every part of the line is click-through, so the
	// line is never itself considered as a drop target as the pointer crosses it.
	a.panel.dragging = true
	a.panel.setThrough(true)
	defer func() {
		a.panel.dragging = false
		a.panel.setThrough(a.hoverID == 0)
	}()

	if err := putFilesOnClipboard([]string{it.Path}); err != nil {
		logf("drag: could not put %s on the clipboard: %v", it.Path, err)
		return
	}
	var dataObj uintptr
	if hr := call(pOleGetClipboard, uptr(&dataObj)); hr != 0 || dataObj == 0 {
		logf("drag: OleGetClipboard failed with 0x%08X", uint32(hr))
		return
	}
	defer comRelease(dataObj)

	var effect uint32
	hr := call(pDoDragDrop, a.panel.hwnd, dataObj, dropeffectCopy|dropeffectMove, uptr(&effect))
	logf("drag finished: hr=0x%08X effect=%d", uint32(hr), effect)

	switch {
	case effect == 0 || effect == dropeffectNone:
		// Cancelled: the card springs back where it was.
		a.line.prune()
	case effect&dropeffectMove != 0:
		// Moved: if the file really went somewhere else it is gone from here, and
		// prune notices. If it was moved onto the line itself, nothing changed.
		a.line.prune()
	default:
		// Copied: the original stays hanging, which is what a copy means.
	}
	a.line.save()
}

// putFilesOnClipboard puts a file list on the clipboard, which is what the shell
// needs in order to build a data object for a drag.
func putFilesOnClipboard(paths []string) error {
	if !openClipboard() {
		return syscall.EINVAL
	}
	defer call(pCloseClipboard)
	if call(pEmptyClipboard) == 0 {
		return syscall.EINVAL
	}
	return setClipboardPayload(map[uint32][]byte{cfHDROP: dropFilesFor(paths)})
}

// comRelease calls IUnknown::Release on an interface pointer the shell handed
// back. See ffi.go for why that conversion is contained there.
func comRelease(obj uintptr) { comReleaseNow(obj) }

// callProcAddr invokes a function at a raw address, needed for COM vtable
// entries, which are not exported by name.
func callProcAddr(addr uintptr, args ...uintptr) uintptr {
	if addr == 0 {
		return 0
	}
	r, _, _ := syscall.SyscallN(addr, args...)
	return r
}

// dropeffectNone is the effect returned when the user cancels a drag.
const dropeffectNone = 0x00000000
