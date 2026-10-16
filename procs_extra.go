package main

// procs_extra.go holds the entry points that only one or two files need, so the
// two main declaration blocks stay readable and about windowing and drawing.

var (
	// ole32: the shell's own data object, which is what makes dragging a photo out
	// of the line look exactly like dragging a file in Explorer.
	pOleGetClipboard = dOle32.NewProc("OleGetClipboard")

	// ole32: an IStream over a block of memory, so a PNG is encoded without
	// touching the disk.
	pCreateStreamOnHGlobal = dOle32.NewProc("CreateStreamOnHGlobal")
	pGetHGlobalFromStream  = dOle32.NewProc("GetHGlobalFromStream")

	// GDI+ writing into a stream, rather than into a file.
	gpSaveImageToStream = dGdiplus.NewProc("GdipSaveImageToStream")

	// shell32: telling the shell that a known folder moved, and opening a folder.
	pSHChangeNotify = dShell32.NewProc("SHChangeNotify")

	// kernel32: named events, for the second-instance hand-off.
	pCreateEvent = dKernel32.NewProc("CreateEventW")
	pOpenEvent   = dKernel32.NewProc("OpenEventW")
	pSetEvent    = dKernel32.NewProc("SetEvent")
	pResetEvent  = dKernel32.NewProc("ResetEvent")

	// kernel32: the overlapped I/O the folder watchers use.
	pWaitForMultiple = dKernel32.NewProc("WaitForMultipleObjects")
	pGetOverlapped   = dKernel32.NewProc("GetOverlappedResult")
	pCancelIo        = dKernel32.NewProc("CancelIoEx")

	// user32: the message the shell broadcasts when it restarts.
	pRegisterWindowMessage = dUser32.NewProc("RegisterWindowMessageW")

	// user32: building the app's icon from a composed bitmap rather than from a
	// resource, which keeps the whole app in one self-contained binary.
	pCreateIconIndirect = dUser32.NewProc("CreateIconIndirect")

	// gdi32: the monochrome mask an HICON needs.
	gdi32CreateBitmap = dGdi32.NewProc("CreateBitmap")
)

const (
	eventModifyState = 0x0002 // EVENT_MODIFY_STATE
	eventSet         = eventModifyState
	waitObject0      = 0
	waitTimeout      = 0x102
	waitFailed       = 0xFFFFFFFF

	// timerTick drives the animation; timerHousekeeping is the once-a-second job.
	timerTick = 1

	// wmNull is the harmless message posted after a popup menu closes, so the
	// menu's owner returns to the foreground.
	wmNull = 0x0000
)
