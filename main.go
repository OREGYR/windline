package main

import (
	"os"
	"runtime"
	"time"
	"unsafe"
)

// main.go is the entry point: one instance at a time, DPI awareness before any
// window exists, a message loop, and a couple of switches for the test harness.
//
// A GUI app on Windows has no console, so the switches exist to make it
// observable from outside:
//
//	--selftest    build everything, draw a frame, save it as a PNG, and exit
//	--livetest    start the real app, reveal the line, and check the screen
//	--mkicon      draw the application icon and write it as an .ico (build step)
//	--log         print the log's path and exit
//	--startup     passed by the Run key, so a login launch can be told apart
//	--peek=N      take every N-th second's screenshot behaviour: hang the newest
//	              image in the screenshots folder and exit (for the tests)

func main() {
	args := os.Args[1:]

	// Win32 windows belong to the thread that created them: their messages are
	// posted to that thread's queue, and a blocking SendMessage to a window waits
	// for its owning thread to process it. Go schedules goroutines across OS
	// threads freely, so without this the window can end up owned by one thread
	// while the message loop runs on another — the loop then never sees a single
	// WM_TIMER, WM_MOUSEMOVE or hot key, and calls like ShowWindow can block
	// forever waiting on a thread that has nothing to pump. Pinning the main
	// goroutine is what makes the window and its message loop the same thread.
	runtime.LockOSThread()

	// DPI awareness has to be set before any window is created, or the panel gets
	// virtualised into a blurry bitmap on a scaled display.
	makeDPIAware()

	for _, a := range args {
		switch a {
		case "--log":
			// Report where the log is, without starting the app.
			println("log: " + logPathFor())
			return
		case "--version":
			println(appName + " " + appVersion)
			return
		}
	}

	// A build step, not a mode: draw the application icon and exit. It runs before
	// the single-instance lock so it never fights a running copy of the app, and
	// before the log is opened so it writes nothing.
	if hasFlag(args, "--mkicon") {
		gdiplusStart()
		code := runMakeIcon(args)
		gdiplusStop()
		os.Exit(code)
	}

	// One instance only: two lines would fight over the screenshots folder and the
	// same hot key. A named mutex is the Windows way to say "already running".
	if !acquireSingleInstance() {
		// Something is already running: bring its line down and leave. That makes
		// launching the app again behave like clicking its shortcut.
		logf("another Windline is already running, asking it to show the line")
		pingRunningInstance()
		return
	}
	defer releaseSingleInstance()

	set := newSettings()
	appSettings = set
	logStart()
	logStartup()
	soundStart()

	if hasFlag(args, "--selftest") {
		runSelfTest(set, args)
		return
	}
	if hasFlag(args, "--livetest") {
		runLiveTest(set, args)
		return
	}

	// GDI+ has to be running before anything is drawn, and this is the only place the
	// real app starts it; the test harnesses start it themselves, which the guarded
	// gdiplusStart makes harmless. Leaving it out is invisible until you look at the
	// screen, because every drawing call then fails with GdiplusNotInitialized
	// (status 18) and quietly draws nothing at all.
	gdiplusStart()
	defer gdiplusStop()

	logos := newLogoRenderer()
	a := newApp(set, logos)
	// The window exists now, so this is the thread that owns it. Everything in
	// ui.go is measured against this id.
	uiThreadID = currentThreadID()
	a.start()
	defer a.shutdown()

	// The tray icon must be re-added when Explorer restarts, which it announces by
	// registering a window message under this name.
	taskbarCreatedMsg = uint32(call(pRegisterWindowMessage, utf16Ptr("TaskbarCreated")))
	watchForegroundChanges(a)

	runMessageLoop(a)
}

// runMessageLoop is the app's main loop: block on GetMessage, dispatch, and let
// the panel's timer drive the animation.
func runMessageLoop(a *app) {
	var msg MSG
	for {
		r := call(pGetMessage, uptr(&msg), 0, 0, 0)
		if int32(r) <= 0 { // 0 = WM_QUIT, -1 = error
			return
		}
		// The tray icon's messages arrive as WM_APP+1 and carry the mouse event in
		// lParam; the shell's "taskbar created" is a registered message.
		switch msg.Message {
		case wmRunOnUI:
			drainUIFuncs()
			continue
		case wmNotifyIcon:
			a.onTrayMessage(msg.LParam)
			continue
		case taskbarCreatedMsg:
			trayAdded = false
			a.buildTray()
			continue
		}
		// While a shortcut is being recorded, keys are taken out of the normal
		// dispatch so the recorder sees them first.
		if panelFor != nil && panelFor.recording {
			switch msg.Message {
			case wmKeyDown, wmSysKeyDown:
				onShortcutKey(uint32(msg.WParam), msg.Message == wmSysKeyDown)
				continue
			}
		}
		call(pTranslateMessage, uptr(&msg))
		call(pDispatchMessage, uptr(&msg))
	}
}

// ---------------------------------------------------------------- single instance

const (
	mutexName            = "Local\\Windline.SingleInstance"
	eventName            = "Local\\Windline.ShowTheLine"
	showLineEventMessage = 0x8000 + 42
)

var mutexHandle uintptr

func acquireSingleInstance() bool {
	const errorAlreadyExists = 183
	h := call(pCreateMutex, 0, 0, utf16Ptr(mutexName))
	if h == 0 {
		return true // if the mutex cannot be made, assume no one else is running
	}
	mutexHandle = h
	if lastError() == errorAlreadyExists {
		call(pCloseHandle, h)
		mutexHandle = 0
		return false
	}
	return true
}

func releaseSingleInstance() {
	if mutexHandle != 0 {
		call(pCloseHandle, mutexHandle)
		mutexHandle = 0
	}
}

// pingRunningInstance asks the running copy to bring its line down, through a
// named event. The running app's own foreground watcher polls for it.
func pingRunningInstance() {
	h := call(pOpenEvent, eventSet, 0, utf16Ptr(eventName))
	if h == 0 {
		h = call(pCreateEvent, 0, 0, 0, utf16Ptr(eventName))
	}
	if h == 0 {
		return
	}
	call(pSetEvent, h)
	call(pCloseHandle, h)
}

// watchForegroundChanges polls for the "show the line" event and for the
// foreground window changing, which is what decides whether the line may come
// down over a full screen app.
func watchForegroundChanges(a *app) {
	// A second timer on the same window drives the less frequent checks: the
	// running-instance event, and pruning photos whose file has gone.
	call(pSetTimer, a.panel.hwnd, timerHousekeeping, 1000, 0)
	go func() {
		h := call(pCreateEvent, 0, 0, 0, utf16Ptr(eventName))
		if h == 0 {
			return
		}
		for {
			r := call(pWaitForSingle, h, 1000)
			if r != 0 {
				continue
			}
			// The event is signalled: another launch wants the line shown. The wait runs on
			// this goroutine; the work it triggers belongs to the window's thread.
			call(pResetEvent, h)
			runOnUI(func() { a.onHotkey(hotkeyToggle) })
		}
	}()
}

const timerHousekeeping = 2

// onHousekeeping is the once-a-second job: notice deleted files, and keep the
// line's length correct after the monitor changed.
func (a *app) onHousekeeping() {
	a.line.prune()
	if a.set.lineHidden() && a.revealed {
		a.tuck()
	}
}

func logStartup() {
	logf("go %s, %d-bit process, up since %s", runtime.Version(),
		int(unsafe.Sizeof(uintptr(0))*8), time.Now().Format("2006-01-02 15:04:05"))
}

// hasFlag reports whether a flag is present, in either "--flag" or "--flag=value"
// form.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name || len(a) > len(name) && a[:len(name)+1] == name+"=" {
			return true
		}
	}
	return false
}

func flagValue(args []string, name string) string {
	prefix := name + "="
	for _, a := range args {
		if len(a) > len(prefix) && a[:len(prefix)] == prefix {
			return a[len(prefix):]
		}
	}
	return ""
}

// appSettings is the global settings handle, so helpers that are not methods can
// reach it without threading it through every call.
var appSettings *settings

// taskbarCreatedMsg is the shell's "I restarted" broadcast.
var taskbarCreatedMsg uint32

const (
	appName    = "Windline"
	appVersion = "1.0.0"
)
