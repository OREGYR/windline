package main

import (
	"math"
	"syscall"
	"unsafe"
)

// panel.go is the Windows stand-in for the original's LinePanel + AppDelegate's
// mouse handling + GrabView.
//
// The macOS panel is a borderless NSPanel at floating level that ignores mouse
// events except over the photos, on every Space, never taking focus. On Windows
// that is a layered popup window with WS_EX_LAYERED | WS_EX_TOPMOST |
// WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE, drawn with UpdateLayeredWindow, kept out
// of the taskbar and the alt-tab list, and switched between click-through and
// clickable depending on what is under the pointer — which is exactly what
// ignoresMouseEvents does there.
//
// Menus, hover, the corner cross, clicking to copy and dragging a photo out all
// arrive as ordinary messages, so the pointer logic lives here and the decisions
// live in app.go.

type panel struct {
	app       *app
	hwnd      uintptr
	className string
	inst      uintptr

	// stripRect is the panel's rectangle in monitor coordinates, in logical
	// units. scale converts it to the device pixels the window works in.
	stripRect Rect
	scale     float64

	lastSurface *surface
	surfaceW    int
	surfaceH    int

	clickThrough bool
	tracking     bool // TrackMouseEvent is armed, so WM_MOUSELEAVE will arrive

	// pointer state, in logical strip coordinates
	mouseOver bool
	mouseX    float64
	mouseY    float64

	// press/drag state
	pressedID    int64
	pressX       float64
	pressY       float64
	draggingID   int64
	pressOnCross bool
	pressTime    uint64

	// dragging is true while an OLE drag is in flight, so the panel knows not to
	// reclaim the capture or treat the pointer as hovering.
	dragging bool

	// recording is true while the shortcut recorder owns the window's keyboard.
	recording bool

	visible bool
}

// panelFor is the single panel instance, needed by the window procedure, which
// is a plain function and cannot capture.
var panelFor *panel

func newPanel(a *app) *panel {
	p := &panel{app: a, className: "WindlinePanel", scale: 1}
	p.inst = moduleHandle()
	panelFor = p
	p.registerClass()
	p.createWindow()
	return p
}

func moduleHandle() uintptr {
	r, _, _ := pGetModuleHandle.Call(0)
	return r
}

func (p *panel) registerClass() {
	wc := WNDCLASSEX{
		LpfnWndProc:   panelWndProc,
		HInstance:     p.inst,
		LpszClassName: utf16Ptr(p.className),
	}
	wc.CbSize = uint32(unsafe.Sizeof(WNDCLASSEX{}))
	if atom := call(pRegisterClassEx, uptr(&wc)); atom == 0 {
		logf("RegisterClassEx failed: %d", lastError())
	}
}

func (p *panel) createWindow() {
	exStyle := uintptr(wsExLayered | wsExTopmost | wsExToolwin | wsExNoAct)
	p.hwnd = call(pCreateWindowEx, exStyle, utf16Ptr(p.className), 0, wsPopup,
		0, 0, 1, 1, 0, 0, p.inst, 0)
	if p.hwnd == 0 {
		logf("CreateWindowEx failed: %d", lastError())
		panic("windline: could not create the line window")
	}
	// Start click-through: nothing is hanging yet, and a pass-through window
	// cannot swallow a click meant for the app behind it.
	p.setThrough(true)
}

// place moves the panel onto the monitor's work area: the line hangs from the
// top edge of the screen you are using, which is the one with the pointer.
func (p *panel) place(monitor uintptr) {
	work, _ := monitorWork(monitor)
	scale := dpiForMonitor(monitor)
	lay := p.app.layout
	p.scale = scale
	p.stripRect = Rect{X: work.X, Y: work.Y, W: work.W, H: lay.PanelHeight()}
	p.surfaceW = int(math.Ceil(p.stripRect.W * scale))
	p.surfaceH = int(math.Ceil(p.stripRect.H * scale))

	x := int(math.Round(p.stripRect.X * scale))
	y := int(math.Round(p.stripRect.Y * scale))
	call(pSetWindowPos, p.hwnd, hwndTopmost, u(x), u(y), u(p.surfaceW), u(p.surfaceH),
		swpNoActivate|swpNoZOrder)
	logf("panel placed on monitor %d: logical (%.0f,%.0f %.0fx%.0f), scale %.2f, %dx%d px",
		monitor, p.stripRect.X, p.stripRect.Y, p.stripRect.W, p.stripRect.H, scale, p.surfaceW, p.surfaceH)
}

// dpiForMonitor reads the scale factor of the monitor a window is on. A window
// is needed because GetDpiForMonitor lives in shcore and wants a monitor handle;
// moving the panel there first and asking for its DPI is the simplest reliable
// route.
func dpiForMonitor(monitor uintptr) float64 {
	// Create a throwaway window on that monitor? Simpler: ask the panel after
	// moving it there.
	if panelFor != nil && panelFor.hwnd != 0 {
		if s := dpiScale(panelFor.hwnd); s > 0 {
			return s
		}
	}
	if s := dpiScale(0); s > 0 {
		return s
	}
	return 1
}

// show makes the window visible without activating it, and refresh paints it.
func (p *panel) show() {
	if !p.visible {
		showNoActivate(p.hwnd)
		p.visible = true
		logf("panel shown")
	}
}

func (p *panel) hide() {
	if p.visible {
		hideWindow(p.hwnd)
		p.visible = false
		logf("panel hidden")
	}
}

// push blits a composed surface to the screen at the panel's position.
func (p *panel) push(s *surface) {
	if s == nil || p.hwnd == 0 {
		return
	}
	if p.lastSurface != nil && p.lastSurface != s {
		p.lastSurface.free()
	}
	p.lastSurface = s
	x := int(math.Round(p.stripRect.X * p.scale))
	y := int(math.Round(p.stripRect.Y * p.scale))
	dst := POINT{X: int32(x), Y: int32(y)}
	size := SIZE{CX: int32(s.W), CY: int32(s.H)}
	src := POINT{0, 0}
	blend := BLENDFUNCTION{BlendOp: 0, BlendFlags: 0, SourceConstantAlpha: 255, AlphaFormat: 1}
	if r := call(pUpdateLayeredWindow, p.hwnd, 0, uptr(&dst), uptr(&size),
		s.memDC, uptr(&src), 0, uptr(&blend), ulwAlpha); r == 0 {
		logf("UpdateLayeredWindow failed: %d", lastError())
	}
}

// setThrough switches the window between click-through and clickable. Nothing
// else in the app touches WS_EX_TRANSPARENT.
func (p *panel) setThrough(through bool) {
	if p.clickThrough == through {
		return
	}
	p.clickThrough = through
	setClickThrough(p.hwnd, through)
}

// ---------------------------------------------------------------- messages

var panelWndProc = syscall.NewCallback(func(hwnd, message, wparam, lparam uintptr) uintptr {
	p := panelFor
	if p == nil {
		r, _, _ := pDefWindowProc.Call(hwnd, message, wparam, lparam)
		return r
	}
	switch message {
	case wmDestroy:
		call(pPostQuitMessage, 0)
		return 0
	case wmEraseBkgnd:
		return 1 // never paint a background: the window is all alpha
	case wmMouseActivate:
		const maNoActivate = 3
		return maNoActivate
	case wmNcHitTest:
		// Only the client area is part of the window as far as the mouse is
		// concerned, so dragging the border does nothing.
		const htClient = 1
		return htClient
	case wmSetCursor:
		// The arrow everywhere except over a photo, where the hand says it can
		// be picked up.
		if p.app != nil && p.app.itemAt(p.mouseX, p.mouseY) != nil {
			const idcHand = 32649
			if h := call(pLoadCursor, 0, idcHand); h != 0 {
				call(pSetCursor, h)
				return 1
			}
		}
		return 0
	case wmMouseMove:
		p.onMouseMove(loWord(lparam), hiWord(lparam), wparam)
		return 0
	case wmMouseLeave:
		p.tracking = false
		p.mouseOver = false
		if p.app != nil {
			p.app.onPointerLeftLine()
		}
		return 0
	case wmLButtonDown:
		p.onLeftDown(loWord(lparam), hiWord(lparam))
		return 0
	case wmLButtonUp:
		p.onLeftUp(loWord(lparam), hiWord(lparam))
		return 0
	case wmLButtonDblClk:
		p.onLeftDoubleClick(loWord(lparam), hiWord(lparam))
		return 0
	case wmRButtonUp:
		p.onRightUp(loWord(lparam), hiWord(lparam))
		return 0
	case wmMouseWheel:
		delta := float64(int16(uint16(wparam>>16))) / 120
		if p.app != nil {
			p.app.onScroll(delta * p.app.layout.Spacing() * 2)
		}
		return 0
	case wmCaptureChanged:
		p.draggingID = 0
		if p.app != nil {
			p.app.onDragEnded(p.pressedID)
		}
		p.pressedID = 0
		return 0
	case wmTimer:
		if p.app != nil {
			p.app.tick()
		}
		return 0
	case wmHotkey:
		if p.app != nil {
			p.app.onHotkey(int(wparam))
		}
		return 0
	case wmClipboard:
		if p.app != nil {
			p.app.onClipboardChanged()
		}
		return 0
	case wmCommand:
		if p.app != nil {
			p.app.onMenuCommand(int(wparam & 0xFFFF))
		}
		return 0
	case wmDisplayChange, wmSettingChange, wmDpiChanged:
		if p.app != nil {
			p.app.onDisplayChanged()
		}
		return 0
	case wmClose:
		// The line is closed by quitting, never by clicking it away.
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, message, wparam, lparam)
	return r
})

// toStripX / toStripY convert client pixel coordinates to the logical strip
// coordinates the layout and hit boxes use.
func (p *panel) toStripX(px int32) float64 { return float64(px) / p.scale }
func (p *panel) toStripY(py int32) float64 { return float64(py) / p.scale }

func (p *panel) onMouseMove(px, py int32, wparam uintptr) {
	x, y := p.toStripX(px), p.toStripY(py)
	p.mouseX, p.mouseY = x, y
	p.mouseOver = true
	if !p.tracking {
		p.tracking = true
		const tmeLeave = 0x00000002
		tme := trackMouseEventStruct{CbSize: uint32(unsafe.Sizeof(trackMouseEventStruct{})), DwFlags: tmeLeave, HWndTrack: p.hwnd}
		call(pTrackMouseEvent, uptr(&tme))
	}
	// While the button is held, a move past a few points starts a drag.
	if p.pressedID != 0 && p.draggingID == 0 {
		if math.Hypot(x-p.pressX, y-p.pressY) > 4 {
			p.beginDrag()
		}
	}
	if p.app != nil {
		p.app.onPointerMoved(x, y)
	}
}

func (p *panel) onLeftDown(px, py int32) {
	x, y := p.toStripX(px), p.toStripY(py)
	p.pressTime = tickCount()
	it := p.app.itemAt(x, y)
	if it == nil {
		// A press on the rope itself just takes focus away from nothing.
		return
	}
	p.pressedID = it.ID
	p.pressX, p.pressY = x, y
	p.pressOnCross = p.app.overCross(it, x, y)
	call(pSetCapture, p.hwnd)
	p.app.onPressChanged(it.ID, true)
}

func (p *panel) beginDrag() {
	id := p.pressedID
	if id == 0 {
		return
	}
	p.draggingID = id
	if p.app != nil {
		p.app.onDragStarted(id)
	}
}

func (p *panel) onLeftUp(px, py int32) {
	x, y := p.toStripX(px), p.toStripY(py)
	call(pReleaseCapture)
	id := p.pressedID
	wasDrag := p.draggingID
	p.pressedID, p.draggingID = 0, 0
	if id == 0 {
		return
	}
	p.app.onPressChanged(id, false)
	if wasDrag != 0 {
		p.app.onDragEnded(id)
		return
	}
	it := p.app.itemAt(x, y)
	if it == nil || it.ID != id {
		return
	}
	// A short press is a click: copy. A long press opens the editor, which is
	// the original's press-and-hold for Markup.
	if p.app.overCross(it, x, y) {
		p.app.onCrossClicked(id)
		return
	}
	if tickCount()-p.pressTime > 400 {
		p.app.onLongPress(id)
		return
	}
	p.app.onClick(id)
}

func (p *panel) onLeftDoubleClick(px, py int32) {
	x, y := p.toStripX(px), p.toStripY(py)
	if it := p.app.itemAt(x, y); it != nil {
		p.app.onDoubleClick(it.ID)
	}
}

func (p *panel) onRightUp(px, py int32) {
	x, y := p.toStripX(px), p.toStripY(py)
	it := p.app.itemAt(x, y)
	pt, _ := screenPointFromClient(p.hwnd, px, py)
	p.app.showItemMenu(it, pt)
}

type trackMouseEventStruct struct {
	CbSize      uint32
	DwFlags     uint32
	HWndTrack   uintptr
	DwHoverTime uint32
}

// screenPointFromClient converts a client point to screen coordinates.
func screenPointFromClient(hwnd uintptr, x, y int32) (POINT, bool) {
	pt := POINT{X: x, Y: y}
	call(pClientToScreen, hwnd, uptr(&pt))
	return pt, true
}

// stripRectOnScreen returns the panel's rectangle in physical screen pixels,
// which is what the flight overlay and the glass capture need.
func (p *panel) stripRectOnScreen() Rect {
	return Rect{
		X: p.stripRect.X * p.scale,
		Y: p.stripRect.Y * p.scale,
		W: p.stripRect.W * p.scale,
		H: p.stripRect.H * p.scale,
	}
}
