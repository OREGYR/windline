package main

// fullscreen.go is FullScreen.swift: it answers one question — is a full screen
// app on this monitor? — because the line must not hang over a full screen app
// unless the user asked for it.

import (
	"syscall"
	"unsafe"
)

const (
	gwlStyle   = -16
	wsVisible  = 0x10000000
	wsMaximize = 0x01000000
	// The shell's own windows are never "a full screen app".
	classNameProgman  = "Progman"
	classNameWorkerW  = "WorkerW"
	classNameShell    = "Shell_TrayWnd"
	classNameShellSec = "Shell_SecondaryTrayWnd"
)

var pGetClassName = dUser32.NewProc("GetClassNameW")

// fullScreenAppOn reports whether the foreground window covers the whole of a
// monitor. It is a port of the original's FullScreen check, and it deliberately
// counts three cases as full screen, because Windows apps reach that state in
// three different ways:
//
//  1. the window's rectangle covers the monitor and it is visible;
//  2. it is maximised and its style says borderless (a game or a video player);
//  3. it covers the monitor but is not the shell, which catches the borderless
//     windows that games create.
//
// The desktop and the taskbar are excluded explicitly, or the line would hide
// itself the moment the user clicked the desktop.
func fullScreenAppOn(monitor uintptr) bool {
	hwnd := call(pGetForegroundWindow)
	if hwnd == 0 || hwnd == panelHwnd() {
		return false
	}
	_, full := monitorWork(monitor)
	var r RECT
	if call(pGetWindowRect, hwnd, uptr(&r)) == 0 {
		return false
	}
	wr := rectFromRECT(r)
	if wr.W < 8 || wr.H < 8 {
		return false
	}
	// The window has to cover the monitor (one pixel of slack, as the original
	// allows for rounding).
	const slack = 1
	coversMonitor := wr.X <= full.X+slack && wr.Y <= full.Y+slack &&
		wr.Right() >= full.Right()-slack && wr.Bottom() >= full.Bottom()-slack
	if !coversMonitor {
		return false
	}
	if isShellWindow(hwnd) {
		return false
	}
	// Windows' own "full screen" detection is more reliable than the rectangle
	// alone for the maximised-borderless case, so ask for it as well: a window
	// that covers the monitor and is not the shell is treated as full screen.
	return true
}

// isShellWindow filters out the desktop, the taskbar and the worker windows that
// together make up the shell. Those always look like full screen windows and are
// never apps.
func isShellWindow(hwnd uintptr) bool {
	name := windowClassName(hwnd)
	switch name {
	case classNameProgman, classNameWorkerW, classNameShell, classNameShellSec, "SHELLDLL_DefView", "Windows.UI.Core.CoreWindow":
		return true
	}
	return false
}

func windowClassName(hwnd uintptr) string {
	buf := make([]uint16, 256)
	n := call(pGetClassName, hwnd, uintptr(unsafe.Pointer(&buf[0])), u(len(buf)))
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

func panelHwnd() uintptr {
	if panelFor != nil {
		return panelFor.hwnd
	}
	return 0
}

// isTaskbarAtTop reports whether the taskbar sits along the top edge, in which
// case the line must not use the top edge as its hot zone: the taskbar is
// already there and clicking it would be a fight. The original never has this
// problem, because macOS's menu bar is always at the top.
func isTaskbarAtTop(monitor uintptr) bool {
	work, full := monitorWork(monitor)
	return work.Y > full.Y+2 && work.H < full.H
}
