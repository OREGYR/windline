package main

import (
	"syscall"
	"unsafe"
)

// dialog.go is the two bits of the original that ask the user something: picking
// a folder to watch, and pressing a new shortcut.
//
// Both avoid a dialog resource or a template file: the folder picker uses the
// shell's own browser (SHBrowseForFolder), and the shortcut recorder takes over
// the panel window for a moment, which needs no extra window at all. That keeps
// the app to a single self-contained binary.

// ---------------------------------------------------------------- folder picker

var pSHBrowseForFolder = dShell32.NewProc("SHBrowseForFolderW")
var pSHGetPathFromIDList = dShell32.NewProc("SHGetPathFromIDListW")
var pCoTaskMemFree32 = dOle32.NewProc("CoTaskMemFree")

const (
	bifReturnOnlyFSDirs = 0x0001
	bifNewDialogStyle   = 0x0040
	biReturnOnlyFSDirs  = 0x0001
)

type browseInfoW struct {
	HwndOwner      uintptr
	PidlRoot       uintptr
	PszDisplayName uintptr
	LpszTitle      uintptr
	UlFlags        uint32
	Lpfn           uintptr
	LParam         uintptr
	IImage         int32
}

// pickFolder asks the user for a folder. It returns "" when they cancel.
//
// SHBrowseForFolder is the older of the shell's two folder pickers, and it is
// chosen deliberately: it is a single call with no COM object to set up, and it
// works in every Windows version the rest of the app supports.
func pickFolder(owner uintptr, title string) string {
	bi := browseInfoW{
		HwndOwner: owner,
		LpszTitle: utf16Ptr(title),
		UlFlags:   bifReturnOnlyFSDirs | bifNewDialogStyle,
	}
	pidl := call(pSHBrowseForFolder, uptr(&bi))
	if pidl == 0 {
		return ""
	}
	defer call(pCoTaskMemFree32, pidl)
	buf := make([]uint16, 260*2)
	if call(pSHGetPathFromIDList, pidl, uintptr(unsafe.Pointer(&buf[0]))) == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

// ---------------------------------------------------------------- shortcut recorder

// shortcutRecording is true while the panel is listening for a new shortcut.
var shortcutRecording bool
var shortcutCh chan shortcut

// captureShortcut takes the panel over for a moment and waits for the user to
// press a combination.
//
// The macOS original shows a small window with a key-capture view. The
// equivalent here reuses the panel's own window: it drops WS_EX_TRANSPARENT so
// it accepts keystrokes, drops WS_EX_NOACTIVATE and takes focus, paints a
// "press a shortcut" prompt, and reports what was pressed. Escape cancels,
// Delete goes back to the default, and a modifier-only press is ignored.
func captureShortcut(hwnd uintptr) (shortcut, bool) {
	shortcutCh = make(chan shortcut, 1)
	shortcutRecording = true

	setClickThrough(hwnd, false)
	removeNoActivate(hwnd)
	call(pSetForegroundWindow, hwnd)
	if panelFor != nil {
		panelFor.recording = true
		panelFor.repaintRecordingPrompt()
	}
	logf("waiting for a new shortcut (Escape cancels)")

	sc := <-shortcutCh
	shortcutCh = nil
	shortcutRecording = false
	if panelFor != nil {
		panelFor.recording = false
	}
	addNoActivate(hwnd)
	call(pSetWindowPos, hwnd, 0, 0, 0, 0, 0,
		swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate)
	return sc, sc.Key != 0
}

// onShortcutKey is called from the panel's window procedure while a shortcut is
// being recorded.
func onShortcutKey(vk uint32, isSystem bool) {
	if !shortcutRecording {
		return
	}
	// The modifier keys themselves: keep listening.
	switch vk {
	case 0x10, 0x11, 0x12, 0x5B, 0x5C, 0xA0, 0xA1, 0xA2, 0xA3, 0xA4, 0xA5:
		return
	case 0x1B: // Escape: cancel
		finishShortcutRecording(shortcut{})
		return
	case 0x2E: // Delete: back to the default
		finishShortcutRecording(defaultShortcutKey)
		return
	}
	mods := currentModifiers()
	// A shortcut with no modifier would swallow a plain key press everywhere,
	// which is never what anyone means.
	if mods&(modControl|modAlt|modWin) == 0 {
		return
	}
	_ = isSystem
	finishShortcutRecording(shortcut{Modifiers: mods, Key: vk})
}

func finishShortcutRecording(sc shortcut) {
	if shortcutCh != nil {
		select {
		case shortcutCh <- sc:
		default:
		}
	}
}

// currentModifiers reads the modifier keys' state right now.
func currentModifiers() uint32 {
	var mods uint32
	if keyDown(0x11) || keyDown(0xA2) || keyDown(0xA3) {
		mods |= modControl
	}
	if keyDown(0x12) || keyDown(0xA4) || keyDown(0xA5) {
		mods |= modAlt
	}
	if keyDown(0x10) || keyDown(0xA0) || keyDown(0xA1) {
		mods |= modShift
	}
	if keyDown(0x5B) || keyDown(0x5C) {
		mods |= modWin
	}
	return mods
}

func keyDown(vk int) bool {
	const keyPressed = 0x8000
	return call(pGetAsyncKeyState, u(vk))&keyPressed != 0
}

// buttonDown reports whether either mouse button is down, which is how the top
// edge notices a click without taking focus.
func buttonDown() bool {
	const pressed = 0x8000
	const vkLButton, vkRButton = 0x01, 0x02
	return call(pGetAsyncKeyState, vkLButton)&pressed != 0 ||
		call(pGetAsyncKeyState, vkRButton)&pressed != 0
}

// removeNoActivate / addNoActivate toggle WS_EX_NOACTIVATE. Removing it is the
// only way the panel can receive keyboard focus, which the recorder needs.
func removeNoActivate(hwnd uintptr) {
	const gwlExstyle = -20
	ex := call(pGetWindowLongPtr, hwnd, u(gwlExstyle))
	call(pSetWindowLongPtr, hwnd, u(gwlExstyle), ex&^wsExNoAct)
}

func addNoActivate(hwnd uintptr) {
	const gwlExstyle = -20
	ex := call(pGetWindowLongPtr, hwnd, u(gwlExstyle))
	call(pSetWindowLongPtr, hwnd, u(gwlExstyle), ex|wsExNoAct)
}

// repaintRecordingPrompt paints the "press a shortcut" prompt over the line.
func (p *panel) repaintRecordingPrompt() {
	if p.app == nil {
		return
	}
	s := p.app.render.composeRecordingPrompt(p.stripRect, p.scale)
	if s != nil {
		p.push(s)
	}
}

// composeRecordingPrompt draws the hint used while recording a shortcut.
func (r *renderer) composeRecordingPrompt(stripRect Rect, scale float64) *surface {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := int(scale * 420)
	h := int(scale * 84)
	if w < 1 || h < 1 {
		return nil
	}
	s := newSurface(w, h)
	defer s.free()
	s.clear()
	s.setQuality()

	path := roundedPath(0, 0, w, h, int(18*scale))
	bg := s.vGrad(0, 0, h, rgba(28, 30, 36, 235), rgba(20, 22, 27, 235))
	s.fillPath(bg, path)
	freePath(path)
	freeBrush(bg)

	edge := ringPath(0, 0, w, h, int(18*scale), max(1, int(scale)))
	eb := s.vGrad(0, 0, h, rgba(255, 255, 255, 120), rgba(255, 255, 255, 40))
	s.fillPath(eb, edge)
	freePath(edge)
	freeBrush(eb)

	f := r.fHint
	f.setAlign(stringAlignCenter, stringAlignCenter)
	s.drawText(L("Press the new shortcut"), 0, float64(h)*0.18, float64(w), 30*scale,
		rgba(238, 240, 245, 245), f)
	s.drawText(L("Escape cancels · Delete goes back to the default"), 0, float64(h)*0.52, float64(w), 24*scale,
		rgba(170, 176, 188, 245), r.fSmall)
	f.setAlign(stringAlignNear, stringAlignNear)
	s.flush()
	return s
}
