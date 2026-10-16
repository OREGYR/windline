package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// win32.go declares the Win32 surface Windline uses, all through the DLL's entry
// points. win64's calling convention matches syscall's (first four arguments in
// RCX/RDX/R8/R9), so integer and pointer parameters pass through untouched.
//
// Floats are the trap: a function declared with a float parameter wants XMM,
// which syscall cannot set. So every binding here is integer/pointer only. That
// is why image draws go through GDI+'s ...I variants and fonts through
// GdipCreateFontFromLogfontW (see gdiplus.go).

var (
	dUser32   = syscall.NewLazyDLL("user32.dll")
	dKernel32 = syscall.NewLazyDLL("kernel32.dll")
	dShell32  = syscall.NewLazyDLL("shell32.dll")
	dAdvapi32 = syscall.NewLazyDLL("advapi32.dll")
	dOle32    = syscall.NewLazyDLL("ole32.dll")

	// --- user32: windowing
	pRegisterClassEx            = dUser32.NewProc("RegisterClassExW")
	pUnregisterClass            = dUser32.NewProc("UnregisterClassW")
	pCreateWindowEx             = dUser32.NewProc("CreateWindowExW")
	pDestroyWindow              = dUser32.NewProc("DestroyWindow")
	pDefWindowProc              = dUser32.NewProc("DefWindowProcW")
	pShowWindow                 = dUser32.NewProc("ShowWindow")
	pSetWindowPos               = dUser32.NewProc("SetWindowPos")
	pGetMessage                 = dUser32.NewProc("GetMessageW")
	pPeekMessage                = dUser32.NewProc("PeekMessageW")
	pTranslateMessage           = dUser32.NewProc("TranslateMessage")
	pDispatchMessage            = dUser32.NewProc("DispatchMessageW")
	pPostQuitMessage            = dUser32.NewProc("PostQuitMessage")
	pPostMessage                = dUser32.NewProc("PostMessageW")
	pSendMessage                = dUser32.NewProc("SendMessageW")
	pSetTimer                   = dUser32.NewProc("SetTimer")
	pKillTimer                  = dUser32.NewProc("KillTimer")
	pUpdateLayeredWindow        = dUser32.NewProc("UpdateLayeredWindow")
	pGetDC                      = dUser32.NewProc("GetDC")
	pReleaseDC                  = dUser32.NewProc("ReleaseDC")
	pBeginPaint                 = dUser32.NewProc("BeginPaint")
	pEndPaint                   = dUser32.NewProc("EndPaint")
	pFillRect                   = dUser32.NewProc("FillRect")
	pGetClientRect              = dUser32.NewProc("GetClientRect")
	pGetWindowRect              = dUser32.NewProc("GetWindowRect")
	pInvalidateRect             = dUser32.NewProc("InvalidateRect")
	pSetCapture                 = dUser32.NewProc("SetCapture")
	pReleaseCapture             = dUser32.NewProc("ReleaseCapture")
	pGetCursorPos               = dUser32.NewProc("GetCursorPos")
	pSetCursorPos               = dUser32.NewProc("SetCursorPos")
	pSetCursor                  = dUser32.NewProc("SetCursor")
	pLoadCursor                 = dUser32.NewProc("LoadCursorW")
	pGetAsyncKeyState           = dUser32.NewProc("GetAsyncKeyState")
	pGetKeyState                = dUser32.NewProc("GetKeyState")
	pIsWindowVisible            = dUser32.NewProc("IsWindowVisible")
	pEnumWindows                = dUser32.NewProc("EnumWindows")
	pWindowFromPoint            = dUser32.NewProc("WindowFromPoint")
	pGetAncestor                = dUser32.NewProc("GetAncestor")
	pGetWindow                  = dUser32.NewProc("GetWindow")
	pGetWindowThreadProcessID   = dUser32.NewProc("GetWindowThreadProcessId")
	pGetForegroundWindow        = dUser32.NewProc("GetForegroundWindow")
	pGetWindowLongPtr           = dUser32.NewProc("GetWindowLongPtrW")
	pSetWindowLongPtr           = dUser32.NewProc("SetWindowLongPtrW")
	pCreatePopupMenu            = dUser32.NewProc("CreatePopupMenu")
	pAppendMenu                 = dUser32.NewProc("AppendMenuW")
	pInsertMenu                 = dUser32.NewProc("InsertMenuW")
	pTrackPopupMenu             = dUser32.NewProc("TrackPopupMenu")
	pDestroyMenu                = dUser32.NewProc("DestroyMenu")
	pSetForegroundWindow        = dUser32.NewProc("SetForegroundWindow")
	pGetMonitorInfo             = dUser32.NewProc("GetMonitorInfoW")
	pMonitorFromPoint           = dUser32.NewProc("MonitorFromPoint")
	pMonitorFromWindow          = dUser32.NewProc("MonitorFromWindow")
	pEnumDisplayMonitors        = dUser32.NewProc("EnumDisplayMonitors")
	pMessageBox                 = dUser32.NewProc("MessageBoxW")
	pLoadImage                  = dUser32.NewProc("LoadImageW")
	pSetProcessDPIAware         = dUser32.NewProc("SetProcessDPIAware")
	pSetDpiAwarenessCtx         = dUser32.NewProc("SetProcessDpiAwarenessContext")
	pGetDpiForWindow            = dUser32.NewProc("GetDpiForWindow")
	pAdjustWindowRectEx         = dUser32.NewProc("AdjustWindowRectEx")
	pOpenClipboard              = dUser32.NewProc("OpenClipboard")
	pCloseClipboard             = dUser32.NewProc("CloseClipboard")
	pEmptyClipboard             = dUser32.NewProc("EmptyClipboard")
	pSetClipboardData           = dUser32.NewProc("SetClipboardData")
	pGetClipboardData           = dUser32.NewProc("GetClipboardData")
	pIsClipboardFormatAv        = dUser32.NewProc("IsClipboardFormatAvailable")
	pRegisterClipboardF         = dUser32.NewProc("RegisterClipboardFormatW")
	pEnumClipboardFormats       = dUser32.NewProc("EnumClipboardFormats")
	pAddClipboardFormatL        = dUser32.NewProc("AddClipboardFormatListener")
	pRemoveClipboardFmtL        = dUser32.NewProc("RemoveClipboardFormatListener")
	pGetClipboardSequenceNumber = dUser32.NewProc("GetClipboardSequenceNumber")
	pRegisterHotKey             = dUser32.NewProc("RegisterHotKey")
	pUnregisterHotKey           = dUser32.NewProc("UnregisterHotKey")
	pScreenToClient             = dUser32.NewProc("ScreenToClient")
	pClientToScreen             = dUser32.NewProc("ClientToScreen")
	pTrackMouseEvent            = dUser32.NewProc("TrackMouseEvent")
	pSetWindowRgn               = dUser32.NewProc("SetWindowRgn")
	pEndDialog                  = dUser32.NewProc("EndDialog")
	pCreateDialogInd            = dUser32.NewProc("CreateDialogIndirectParamW")

	// --- kernel32
	pGetModuleHandle    = dKernel32.NewProc("GetModuleHandleW")
	pGetCurrentPid      = dKernel32.NewProc("GetCurrentProcessId")
	pGetConsoleWindow   = dKernel32.NewProc("GetConsoleWindow")
	pGlobalAlloc        = dKernel32.NewProc("GlobalAlloc")
	pGlobalFree         = dKernel32.NewProc("GlobalFree")
	pGlobalLock         = dKernel32.NewProc("GlobalLock")
	pGlobalUnlock       = dKernel32.NewProc("GlobalUnlock")
	pGlobalSize         = dKernel32.NewProc("GlobalSize")
	pSleep              = dKernel32.NewProc("Sleep")
	pGetCurrentThreadId = dKernel32.NewProc("GetCurrentThreadId")
	pGetTickCount       = dKernel32.NewProc("GetTickCount64")
	pGetLastError       = dKernel32.NewProc("GetLastError")
	pGetFileAttribs     = dKernel32.NewProc("GetFileAttributesW")
	pCreateFile         = dKernel32.NewProc("CreateFileW")
	pCloseHandle        = dKernel32.NewProc("CloseHandle")
	pReadDirectoryCh    = dKernel32.NewProc("ReadDirectoryChangesW")
	pFindFirstChange    = dKernel32.NewProc("FindFirstChangeNotificationW")
	pFindNextChange     = dKernel32.NewProc("FindNextChangeNotification")
	pFindCloseChange    = dKernel32.NewProc("FindCloseChangeNotification")
	pWaitForSingle      = dKernel32.NewProc("WaitForSingleObject")
	pGetShortPath       = dKernel32.NewProc("GetShortPathNameW")
	pGetFullPath        = dKernel32.NewProc("GetFullPathNameW")
	pGetLocaleName      = dKernel32.NewProc("GetUserDefaultLocaleName")
	pGetUserLang        = dKernel32.NewProc("GetUserDefaultUILanguage")
	pCopyFile           = dKernel32.NewProc("CopyFileW")
	pMoveFileEx         = dKernel32.NewProc("MoveFileExW")
	pDeleteFile         = dKernel32.NewProc("DeleteFileW")
	pCreateProcess      = dKernel32.NewProc("CreateProcessW")
	pCreateMutex        = dKernel32.NewProc("CreateMutexW")
	pGetCommandLine     = dKernel32.NewProc("GetCommandLineW")

	// --- shell32
	pShellNotifyIcon    = dShell32.NewProc("Shell_NotifyIconW")
	pShellExecute       = dShell32.NewProc("ShellExecuteW")
	pSHFileOperation    = dShell32.NewProc("SHFileOperationW")
	pSHGetFolderPath    = dShell32.NewProc("SHGetFolderPathW")
	pSHGetKnownFolder   = dShell32.NewProc("SHGetKnownFolderPath")
	pSHSetKnownFolder   = dShell32.NewProc("SHSetKnownFolderPath")
	pDragQueryFile      = dShell32.NewProc("DragQueryFileW")
	pExtractIconEx      = dShell32.NewProc("ExtractIconExW")
	pShellItemImageFrom = dShell32.NewProc("SHCreateItemFromParsingName")

	// --- advapi32: the Run key and the Screenshots known folder
	pRegCreateKeyEx = dAdvapi32.NewProc("RegCreateKeyExW")
	pRegOpenKeyEx   = dAdvapi32.NewProc("RegOpenKeyExW")
	pRegSetValueEx  = dAdvapi32.NewProc("RegSetValueExW")
	pRegQueryValue  = dAdvapi32.NewProc("RegQueryValueExW")
	pRegDeleteValue = dAdvapi32.NewProc("RegDeleteValueW")
	pRegCloseKey    = dAdvapi32.NewProc("RegCloseKey")
	pRegDeleteKey   = dAdvapi32.NewProc("RegDeleteKeyW")

	// --- ole32: drag and drop, and the shell's move machinery
	pOleInitialize = dOle32.NewProc("OleInitialize")
	pOleUninit     = dOle32.NewProc("OleUninitialize")
	pDoDragDrop    = dOle32.NewProc("DoDragDrop")
	pCoTaskMemFree = dOle32.NewProc("CoTaskMemFree")
)

// ------------------------------------------------------------------ constants

const (
	// window styles
	wsPopup      = 0x80000000
	wsExLayered  = 0x00080000
	wsExTranspar = 0x00000020 // mouse events pass through the window
	wsExTopmost  = 0x00000008
	wsExToolwin  = 0x00000080 // no taskbar button
	wsExNoAct    = 0x08000000 // never takes focus
	wsExComposit = 0x00200000

	hwndTopmost = ^uintptr(0) // (HWND)-1
	hwndBottom  = uintptr(1)  // (HWND)1

	swpNoSize        = 0x0001
	swpNoMove        = 0x0002
	swpNoZOrder      = 0x0004
	swpNoActivate    = 0x0010
	swpShowWindow    = 0x0040
	swpNoRedraw      = 0x0008
	swShowNA         = 8
	swHide           = 0
	swShowNoActivate = 4

	ulwAlpha    = 2
	ulwColorKey = 1

	// messages
	wmDestroy        = 0x0002
	wmQuit           = 0x0012
	wmSize           = 0x0005
	wmClose          = 0x0010
	wmPaint          = 0x000F
	wmEraseBkgnd     = 0x0014
	wmSetCursor      = 0x0020
	wmGetMinMaxInfo  = 0x0024
	wmDisplayChange  = 0x007E
	wmNcDestroy      = 0x0082
	wmNcHitTest      = 0x0084
	wmMouseMove      = 0x0200
	wmMouseActivate  = 0x0021
	wmLButtonDown    = 0x0201
	wmLButtonUp      = 0x0202
	wmRButtonDown    = 0x0204
	wmRButtonUp      = 0x0205
	wmMouseWheel     = 0x020A
	wmMouseLeave     = 0x02A3
	wmMouseHover     = 0x02A1
	wmTimer          = 0x0113
	wmHotkey         = 0x0312
	wmApp            = 0x8000
	wmDpiChanged     = 0x02E0
	wmSettingChange  = 0x001A
	wmClipboard      = 0x031D // WM_CLIPBOARDUPDATE
	wmCommand        = 0x0111
	wmInitMenuPopup  = 0x0117
	wmKeyDown        = 0x0100
	wmKeyUp          = 0x0101
	wmChar           = 0x0102
	wmSysKeyDown     = 0x0104
	wmContextMenu    = 0x007B
	wmCaptureChanged = 0x0215
	wmLButtonDblClk  = 0x0203
	wmMouseHWheel    = 0x020E

	// menu flags
	mfString       = 0x0000
	mfSeparator    = 0x0800
	mfEnabled      = 0x0000
	mfGrayed       = 0x0001
	mfChecked      = 0x0008
	mfPopup        = 0x0010
	mfUnchecked    = 0x0000
	mfByPosition   = 0x0400
	mfByCommand    = 0x0000
	tpmLeftAlign   = 0x0000
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
	tpmNonotify    = 0x0080

	// shell notify icon
	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nimSetVersion = 4
	nifMessage    = 0x1
	nifIcon       = 0x2
	nifTip        = 0x4
	notifyIconV4  = 4
	wmNotifyIcon  = wmApp + 1

	// monitors
	monitorDefaultToNearest = 2
	monitorDefaultToPrimary = 1
	monitorDefaultToNull    = 0

	// registry
	hkeyCurrentUser = 0x80000001
	keyQueryValue   = 0x0001
	keySetValue     = 0x0002
	keyCreateSub    = 0x0004
	keyAllAccess    = 0xF003F
	regSZ           = 1
	regExpandSZ     = 2
	regDWORD        = 4
	regNone         = 0
	runKeyPath      = `Software\Microsoft\Windows\CurrentVersion\Run`
	shellFolderKey  = `Software\Microsoft\Windows\CurrentVersion\Explorer\User Shell Folders`

	// clipboard
	cfText  = 1
	cfDIB   = 8
	cfDIBv5 = 17
	cfHDROP = 15

	// drag and drop
	dropeffectCopy   = 0x1
	dropeffectMove   = 0x2
	dropeffectLink   = 0x4
	dropeffectScroll = 0x80000000
	dragdropSOK      = 0x00040100 // DRAGDROP_S_DROP
	dragdropSCancel  = 0x00040101 // DRAGDROP_S_CANCEL
	dragdropSUseDef  = 0x00040102 // DRAGDROP_S_USEDEFAULTCURSORS
	eNotImpl         = 0x80004001
	eNoInterface     = 0x80004002
	ePointer         = 0x80004003
	eFail            = 0x80004005
	eOutOfMemory     = 0x8007000E
	eInvalidArg      = 0x80070057
	ok               = 0
	false_           = 0
	true_            = 1
)

// ------------------------------------------------------------------ structures

type POINT struct{ X, Y int32 }
type RECT struct{ Left, Top, Right, Bottom int32 }
type SIZE struct{ CX, CY int32 }
type MSG struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      POINT
	Private uint32
}
type WNDCLASSEX struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  uintptr
	LpszClassName uintptr
	HIconSm       uintptr
}
type MINMAXINFO struct {
	PtReserved     POINT
	PtMaxSize      POINT
	PtMaxPosition  POINT
	PtMinTrackSize POINT
	PtMaxTrackSize POINT
}
type MONITORINFO struct {
	CbSize    uint32
	RcMonitor RECT
	RcWork    RECT
	DwFlags   uint32
}
type PAINTSTRUCT struct {
	Hdc         uintptr
	FErase      int32
	RcPaint     RECT
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}
type BLENDFUNCTION struct{ BlendOp, BlendFlags, SourceConstantAlpha, AlphaFormat byte }
type NOTIFYICONDATA struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

// Rect is a float rectangle in monitor pixels, top-left origin, y downwards.
type Rect struct{ X, Y, W, H float64 }

func (r Rect) Contains(x, y float64) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

func (r Rect) Right() float64  { return r.X + r.W }
func (r Rect) Bottom() float64 { return r.Y + r.H }

func rectFromRECT(r RECT) Rect {
	return Rect{
		X: float64(r.Left),
		Y: float64(r.Top),
		W: float64(r.Right - r.Left),
		H: float64(r.Bottom - r.Top),
	}
}

// ------------------------------------------------------------------ helpers

// u converts an integer to the uintptr the syscall layer wants.
func u(v int) uintptr { return uintptr(v) }

// u32 converts an unsigned 32-bit value, which is what most enum and DWORD
// parameters are declared as.
func u32(v uint32) uintptr { return uintptr(v) }

// uptr takes the address of a value for a pointer parameter. It must be
// generic: taking the address through an `any` parameter would hand the callee
// the interface header instead of the value.
func uptr[T any](v *T) uintptr { return uintptr(unsafe.Pointer(v)) }

func utf16Ptr(s string) uintptr {
	p, _ := syscall.UTF16PtrFromString(s)
	return uintptr(unsafe.Pointer(p))
}

// utf16ToString reads a NUL-terminated UTF-16 string.
func utf16ToString(p *uint16) string {
	if p == nil {
		return ""
	}
	return syscall.UTF16ToString(unsafe.Slice(p, 1<<15))
}

func call(p *syscall.LazyProc, args ...uintptr) uintptr {
	r, _, _ := p.Call(args...)
	return r
}

// currentThreadID identifies the calling thread. It is what lets runOnUI tell
// "already on the window's thread" from "must hand this over", which is the whole
// basis of the thread bridge in ui.go.
func currentThreadID() uint32 {
	r, _, _ := pGetCurrentThreadId.Call()
	return uint32(r)
}

func lastError() uintptr {
	r, _, _ := pGetLastError.Call()
	return r
}

// hiWord / loWord read the packed coordinates out of a mouse message's lParam.
func loWord(v uintptr) int32 { return int32(int16(uint16(v & 0xFFFF))) }
func hiWord(v uintptr) int32 { return int32(int16(uint16((v >> 16) & 0xFFFF))) }

func makeLParam(x, y int32) uintptr {
	return uintptr(uint32(uint16(x))) | uintptr(uint32(uint16(y)))<<16
}

// tickCount is milliseconds since the system started.
func tickCount() uint64 {
	r, _, _ := pGetTickCount.Call()
	return uint64(r)
}

func cursorPos() (int32, int32) {
	var pt POINT
	call(pGetCursorPos, uptr(&pt))
	return pt.X, pt.Y
}

// monitorFromPoint returns the monitor containing a point, or the nearest one.
func monitorFromPoint(x, y int32) uintptr {
	pt := POINT{X: x, Y: y}
	return call(pMonitorFromPoint, uptr(&pt), monitorDefaultToNearest)
}

// monitorWork returns the monitor's work area (the desktop minus the taskbar)
// and its full bounds.
func monitorWork(hmon uintptr) (work Rect, full Rect) {
	mi := MONITORINFO{CbSize: uint32(unsafe.Sizeof(MONITORINFO{}))}
	if call(pGetMonitorInfo, hmon, uptr(&mi)) == 0 {
		return Rect{0, 0, 1920, 1080}, Rect{0, 0, 1920, 1080}
	}
	return rectFromRECT(mi.RcWork), rectFromRECT(mi.RcMonitor)
}

// fileExists reports whether a path names an existing file or directory.
func fileExists(path string) bool {
	const invalidFileAttributes = 0xFFFFFFFF
	return call(pGetFileAttribs, utf16Ptr(path)) != invalidFileAttributes
}

func isDir(path string) bool {
	const fileAttributeDirectory = 0x10
	r := call(pGetFileAttribs, utf16Ptr(path))
	return r != 0xFFFFFFFF && r&fileAttributeDirectory != 0
}

// showWindow / hideWindow keep the panel out of the way without disturbing
// z-order or the foreground window.
func showNoActivate(hwnd uintptr) { call(pShowWindow, hwnd, swShowNA) }
func hideWindow(hwnd uintptr)     { call(pShowWindow, hwnd, swHide) }

// windowAtPoint returns the window and the top-level ancestor of it that sits at a
// screen point, which is how a leftover overlay elsewhere in this process is found
// without enumerating every window on the desktop.
func windowAtPoint(x, y int) (hwnd, root uintptr) {
	const gaRoot = 2
	pt := POINT{X: int32(x), Y: int32(y)}
	hwnd = call(pWindowFromPoint, uptr(&pt))
	if hwnd == 0 {
		return 0, 0
	}
	root = hwnd
	for {
		parent := call(pGetAncestor, root, gaRoot)
		if parent == 0 || parent == root {
			break
		}
		root = parent
	}
	return hwnd, root
}

// windowClass returns a window's class name, for naming what was found.
func windowClass(hwnd uintptr) string {
	var buf [256]uint16
	n := call(pGetClassName, hwnd, uptr(&buf[0]), u(len(buf)))
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

// windowPid returns the process that owns a window.
func windowPid(hwnd uintptr) uint32 {
	var pid uint32
	call(pGetWindowThreadProcessID, hwnd, uptr(&pid))
	return pid
}

// windowVisible reports whether Windows considers a window visible. This is the
// real state, not the app's own belief about it.
func windowVisible(hwnd uintptr) bool { return call(pIsWindowVisible, hwnd) != 0 }

// windowRect returns a window's screen rectangle.
func windowRect(hwnd uintptr) (Rect, bool) {
	var r RECT
	if call(pGetWindowRect, hwnd, uptr(&r)) == 0 {
		return Rect{}, false
	}
	return Rect{X: float64(r.Left), Y: float64(r.Top), W: float64(r.Right - r.Left), H: float64(r.Bottom - r.Top)}, true
}

func getCurrentProcessID() uint32 { return uint32(call(pGetCurrentPid)) }

// getConsoleWindow returns the window of the console this process is attached to,
// or 0 when there is none (output redirected, or a terminal host that owns the
// console itself). Tests use it to move the terminal out of the way.
func getConsoleWindow() uintptr { return call(pGetConsoleWindow) }

// ownWindows lists every top-level window owned by this process, with its class,
// rectangle and real visibility. Tests use it to prove nothing of the app is left on
// screen after it has hidden itself — a claim pixel comparison cannot make, because
// an overlay that draws the desktop's own pixels back is invisible to a pixel diff
// while still swallowing every click.
func ownWindows() []string {
	pid := getCurrentProcessID()
	var found []string
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		const gwOwner = 4
		if call(pGetWindow, hwnd, gwOwner) != 0 {
			return 1 // owned windows are not top-level
		}
		if windowPid(hwnd) != pid {
			return 1
		}
		vis := "hidden"
		if windowVisible(hwnd) {
			vis = "VISIBLE"
		}
		r, _ := windowRect(hwnd)
		found = append(found, fmt.Sprintf("%s class=%q rect=(%.0f,%.0f %.0fx%.0f) hwnd=%#x",
			vis, windowClass(hwnd), r.X, r.Y, r.W, r.H, hwnd))
		return 1
	})
	call(pEnumWindows, cb, 0)
	return found
}

func setTopMost(hwnd uintptr, x, y, w, h int32) {
	call(pSetWindowPos, hwnd, hwndTopmost, u(int(x)), u(int(y)), u(int(w)), u(int(h)),
		swpNoActivate|swpShowWindow)
}

// setClickThrough turns WS_EX_TRANSPARENT on or off. While it is on, the window
// is invisible to the mouse and clicks land on whatever is underneath; while it
// is off, the window receives them.
func setClickThrough(hwnd uintptr, through bool) {
	const gwlExstyle = -20
	ex := call(pGetWindowLongPtr, hwnd, u(gwlExstyle))
	if through {
		ex |= wsExTranspar
	} else {
		ex &^= wsExTranspar
	}
	call(pSetWindowLongPtr, hwnd, u(gwlExstyle), ex)
}

// dpiScale returns the monitor's scale factor (1.0 at 96 dpi).
func dpiScale(hwnd uintptr) float64 {
	if r := call(pGetDpiForWindow, hwnd); r > 0 {
		return float64(r) / 96.0
	}
	return 1
}

// makeDPIAware switches the process to per-monitor DPI awareness, so an overlay
// that spans a screen works in real pixels on scaled displays.
func makeDPIAware() {
	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 == (HANDLE)-4
	if call(pSetDpiAwarenessCtx, ^uintptr(3)) == 0 {
		call(pSetProcessDPIAware)
	}
}

// ------------------------------------------------------------------ clipboard

// clipFormatID registers (and returns the id of) a clipboard format by name.
func clipFormatID(name string) uint32 {
	return uint32(call(pRegisterClipboardF, utf16Ptr(name)))
}
