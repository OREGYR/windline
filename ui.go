package main

import (
	"time"
)

// ui.go is the thread bridge. It exists because of one hard Win32 rule: a window
// belongs to the thread that created it, its messages are delivered to that
// thread's queue, and a call like ShowWindow or SetWindowPos that is made from any
// other thread can block waiting on a thread that is not pumping.
//
// Go schedules goroutines across OS threads and moves them without warning, and
// several parts of this app genuinely live in the background: the folder watchers
// block on ReadDirectoryChangesW, the clipboard listener runs off a window message
// but its work is asynchronous, and time.AfterFunc runs on a timer thread. Any of
// them touching window state directly would be a latent hang — rare, load
// dependent, and almost impossible to reproduce on demand.
//
// So there is exactly one rule in this program: only the window's own thread
// mutates the UI. Background work arrives through runOnUI, which posts a message
// to the panel's window; the message loop picks it up and calls the function
// there. This mirrors what the macOS original gets for free from DispatchQueue.main,
// and it is why `after` is implemented in terms of it rather than next to it.

const wmRunOnUI = wmApp + 7

var (
	uiFuncs   []func()
	uiFuncsMu chan struct{} // a one-token semaphore guarding uiFuncs
	uiWoken   chan struct{}
)

func init() {
	uiFuncsMu = make(chan struct{}, 1)
	uiFuncsMu <- struct{}{}
	uiWoken = make(chan struct{}, 1)
}

// uiThreadID is the id of the thread that created the panel. Anything else is a
// background thread and has to hand its work over.
var uiThreadID uint32

// uiQuit stops the UI worker goroutine.
var uiQuit = make(chan struct{})

// runOnUI runs fn on the window's thread. It never blocks: the caller hands the
// function over and returns. When the panel does not exist yet (during startup,
// or in a headless test) fn runs on a dedicated worker that is started on demand,
// which keeps the same function usable from both paths.
func runOnUI(fn func()) {
	if fn == nil {
		return
	}
	if panelFor == nil || panelFor.hwnd == 0 {
		go fn()
		return
	}
	if uiThreadID == 0 || currentThreadID() == uiThreadID {
		// Already on the right thread — the message-loop path and startup code
		// take this branch, and running inline avoids a pointless round trip.
		fn()
		return
	}
	<-uiFuncsMu
	uiFuncs = append(uiFuncs, fn)
	uiFuncsMu <- struct{}{}

	// Wake the loop so it processes the queue promptly rather than on the next
	// timer tick. WM_NULL is enough: any message will do.
	call(pPostMessage, panelFor.hwnd, wmRunOnUI, 0, 0)
}

// drainUIFuncs runs everything queued for the window's thread. The message loop
// calls it; it is the only reader of uiFuncs.
func drainUIFuncs() {
	for {
		<-uiFuncsMu
		if len(uiFuncs) == 0 {
			uiFuncsMu <- struct{}{}
			return
		}
		fns := uiFuncs
		uiFuncs = nil
		uiFuncsMu <- struct{}{}

		for _, fn := range fns {
			safeUIFunc(fn)
		}
	}
}

// safeUIFunc isolates a queued job: one bad function should not take the message
// loop down with it and leave a window that no longer responds.
func safeUIFunc(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			logf("ui job panicked: %v", r)
		}
	}()
	fn()
}

// after runs fn on the window's thread after d seconds. The macOS original uses
// DispatchQueue.main.asyncAfter for the same jobs, so the function must arrive on
// the UI thread to touch the same state safely.
func after(d float64, fn func()) {
	if d < 0 {
		d = 0
	}
	time.AfterFunc(time.Duration(d*float64(time.Second)), func() { runOnUI(fn) })
}
