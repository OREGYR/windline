package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// watch.go is ScreenshotWatcher: it notices new files in the folders a
// screenshot can land in.
//
// The macOS original watches with FSEvents. The Windows equivalent is
// ReadDirectoryChangesW, which is what this uses: one directory handle per
// folder, a change notification armed with OVERLAPPED I/O, and a dedicated
// goroutine per watcher waiting on either a change or the stop event. That is
// cheaper and more immediate than polling, and it is exactly what Explorer
// itself does to refresh a folder.
//
// There is one Windows-specific wrinkle worth naming: a screenshot is written in
// one shot by the shell, but a large one can still be mid-write when the
// notification arrives. A file that cannot be decoded yet is retried a moment
// later rather than dropped, which is the same protection the original gets for
// free from FSEvents' latency.

type overlapped struct {
	Internal     uintptr
	InternalHigh uintptr
	Offset       uint32
	OffsetHigh   uint32
	HEvent       uintptr
}

// fileNotifyInformation is one record out of ReadDirectoryChangesW's buffer.
type fileNotifyInformation struct {
	NextEntryOffset uint32
	Action          uint32
	FileNameLength  uint32
	FileName        [1]uint16
}

const (
	fileActionAdded          = 1
	fileActionRenamedNewName = 4
	fileListenAll            = 0x00000037
	fileListDirectory        = 0x0001
	fileShareAll             = 0x00000007
	openExisting             = 3
	fileFlagOverlapped       = 0x40000000
	fileFlagBackupSem        = 0x02000000
	infinite                 = 0xFFFFFFFF
	ioPending                = 997 // ERROR_IO_PENDING
)

// folderWatcherSet watches one or more folders and calls onNew for every file
// that appears in one of them.
//
// Stopping is the delicate part. Each watcher goroutine is parked inside
// WaitForMultipleObjects on a pending ReadDirectoryChangesW, so closing a channel
// cannot wake it: the wait has to be broken by an event it is already waiting on.
// One manual-reset event is therefore shared by every goroutine in the set, and
// stop signals it and only then joins the goroutines. Without that, quitting the
// app deadlocks — there is no timeout inside the wait and no way to reach it.
type folderWatcherSet struct {
	dirs    []string
	onNew   func(string)
	stopCh  chan struct{}
	wg      sync.WaitGroup
	started bool
	mu      sync.Mutex

	stopEvent uintptr // manual reset, shared by every watcher goroutine
}

func newFolderWatcherSet(dirs []string, onNew func(string)) *folderWatcherSet {
	return &folderWatcherSet{dirs: dedupePaths(dirs), onNew: onNew, stopCh: make(chan struct{})}
}

func (w *folderWatcherSet) folders() []string { return w.dirs }

func (w *folderWatcherSet) watching(dir string) bool {
	for _, d := range w.dirs {
		if samePath(d, dir) {
			return true
		}
	}
	return false
}

// start adopts anything that appeared while the app was not running, then begins
// watching for changes.
func (w *folderWatcherSet) start() {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.started = true
	// The stop event is created before any goroutine can wait on it, and is
	// manual-reset so every watcher sees it at once.
	w.stopEvent = call(pCreateEvent, 0, 1, 0, 0)
	w.mu.Unlock()

	if w.stopEvent == 0 {
		logf("watch: cannot create the stop event: %d", lastError())
		return
	}

	// The initial scan is what makes a screenshot taken a second before launch
	// still hang: the launch date is remembered, and files newer than it are
	// adopted. This mirrors the original adopting screenshots taken since it
	// last ran.
	for _, dir := range w.dirs {
		for _, p := range recentFilesIn(dir, launchTime()) {
			path := p
			logf("watch: adopting %s (it arrived while windline was not looking)", path)
			runOnUI(func() { w.onNew(path) })
		}
	}

	for _, dir := range w.dirs {
		if !isDir(dir) {
			logf("watch: %s is not a folder, skipping it", dir)
			continue
		}
		w.wg.Add(1)
		go w.watchLoop(dir)
	}
}

func (w *folderWatcherSet) stop() {
	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return
	}
	w.started = false
	ev := w.stopEvent
	w.stopEvent = 0
	close(w.stopCh)
	w.mu.Unlock()

	// Wake every parked wait, then join. Signalling first is what makes this
	// terminate; joining first would block forever.
	if ev != 0 {
		call(pSetEvent, ev)
	}
	w.wg.Wait()
	if ev != 0 {
		call(pCloseHandle, ev)
	}
}

// watchLoop is one folder's goroutine: arm the notification, wait for either a
// change or the stop event, handle the change, repeat.
func (w *folderWatcherSet) watchLoop(dir string) {
	defer w.wg.Done()

	hDir := call(pCreateFile, utf16Ptr(dir), fileListDirectory, fileShareAll, 0,
		openExisting, fileFlagOverlapped|fileFlagBackupSem, 0)
	if hDir == 0 || hDir == ^uintptr(0) {
		logf("watch: cannot open %s for changes: %d", dir, lastError())
		return
	}
	defer call(pCloseHandle, hDir)

	hChange := call(pCreateEvent, 0, 1, 0, 0) // manual reset, initially unsignalled
	if hChange == 0 {
		logf("watch: cannot create the change event for %s: %d", dir, lastError())
		return
	}
	defer call(pCloseHandle, hChange)

	w.mu.Lock()
	hStop := w.stopEvent
	w.mu.Unlock()
	if hStop == 0 {
		return
	}

	buf := make([]byte, 64*1024)

	for {
		// Arm the notification, then wait for a change or for a stop.
		ov := overlapped{HEvent: hChange}
		call(pResetEvent, hChange)
		var returned uint32
		ok := call(pReadDirectoryCh, hDir, uintptr(unsafe.Pointer(&buf[0])), u(len(buf)),
			0, fileListenAll, 0, uptr(&ov), 0)
		if ok == 0 {
			if err := lastError(); err != ioPending {
				logf("watch: ReadDirectoryChangesW on %s failed: %d", dir, err)
				// A folder can vanish (an external drive unplugged). Give up on it
				// rather than spinning on it.
				return
			}
		}

		handles := [2]uintptr{hChange, hStop}
		r := call(pWaitForMultiple, 2, uintptr(unsafe.Pointer(&handles[0])), 0, infinite)
		if r == waitObject0+1 { // the stop event
			call(pCancelIo, hDir)
			return
		}
		select {
		case <-w.stopCh:
			call(pCancelIo, hDir)
			return
		default:
		}
		if r != waitObject0 {
			if r == waitTimeout {
				continue
			}
			return
		}

		// The change is complete: collect the byte count and walk the records.
		call(pGetOverlapped, hDir, uptr(&ov), uptr(&returned), 1)
		if returned == 0 {
			continue
		}
		w.handleRecords(dir, buf[:min(int(returned), len(buf))])
	}
}

// handleRecords walks the FILE_NOTIFY_INFORMATION records in a notification
// buffer and reports each new file.
func (w *folderWatcherSet) handleRecords(dir string, buf []byte) {
	offset := 0
	for {
		if offset+12 > len(buf) {
			return
		}
		rec := (*fileNotifyInformation)(unsafe.Pointer(&buf[offset]))
		nameLen := int(rec.FileNameLength) / 2
		if nameLen > 0 && offset+12+int(rec.FileNameLength) <= len(buf) {
			name := syscall.UTF16ToString(unsafe.Slice(&rec.FileName[0], nameLen))
			if rec.Action == fileActionAdded || rec.Action == fileActionRenamedNewName {
				w.report(filepath.Join(dir, name))
			}
		}
		if rec.NextEntryOffset == 0 {
			return
		}
		offset += int(rec.NextEntryOffset)
	}
}

// report hands a new path on, after waiting for its contents to settle. A
// screenshot of a 5K display is a few megabytes and the shell may still be
// writing it when the notification arrives.
//
// The wait happens here, on the watcher's own goroutine, but onNew lands on the
// window's thread: adopting a photo touches the line, the renderer and the window.
func (w *folderWatcherSet) report(path string) {
	if !isImagePath(path) && !isRecording(path) {
		return
	}
	onNew := w.onNew
	go func() {
		if !waitUntilSettled(path, 8) {
			logf("watch: %s never became readable, ignoring it", path)
			return
		}
		logf("watch: new file %s", path)
		runOnUI(func() { onNew(path) })
	}()
}

// waitUntilSettled waits for a file to stop changing and become decodable.
// up to tries attempts, 60 ms apart.
func waitUntilSettled(path string, tries int) bool {
	lastSize := int64(-1)
	stableFor := 0
	for i := 0; i < tries; i++ {
		if !fileExists(path) {
			return false
		}
		size := fileSize(path)
		if size > 0 && size == lastSize {
			stableFor++
			if stableFor >= 1 && decodable(path) {
				return true
			}
		} else {
			stableFor = 0
		}
		lastSize = size
		sleepMS(60)
	}
	return decodable(path)
}

// decodable asks GDI+ whether the file is a complete image yet. This is the
// cheapest reliable test: a half-written PNG fails to decode, and the file might
// otherwise be replaced by a second, complete write.
func decodable(path string) bool {
	img, ok := loadImageFile(path)
	if !ok {
		return false
	}
	img.free()
	return true
}

// ---------------------------------------------------------------- scanning

// launchMillis is when this process started, used to decide which existing files
// count as "new since Windline last ran".
var launchMillis uint64

func launchTime() uint64 {
	if launchMillis == 0 {
		launchMillis = uint64(nowUnixMilli())
	}
	return launchMillis
}

// recentFilesIn lists the image files in a folder that were created after a
// cutoff, oldest first. createdAfter is a Unix millisecond timestamp.
func recentFilesIn(dir string, createdAfter uint64) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		if !isImagePath(full) && !isRecording(full) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		when, ok := creationTimeOf(info)
		if !ok {
			when = info.ModTime()
		}
		if uint64(when.UnixMilli()) >= createdAfter {
			out = append(out, full)
		}
	}
	sortByCreation(out)
	return out
}

// newestFilesIn lists up to n of the most recent image files in a folder, newest
// last. The Inbox safety watcher uses it to notice a screenshot that ignored the
// redirect.
func newestFilesIn(dir string, n int) []string {
	all := recentFilesIn(dir, 0)
	if len(all) <= n {
		return all
	}
	return all[len(all)-n:]
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func sleepMS(ms int) { call(pSleep, u(ms)) }

// pathIsInside reports whether path is inside dir (or is dir).
func pathIsInside(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// dedupePaths removes duplicate folders, comparing Windows-style so that
// `C:\Shots` and `c:\shots` count as one.
func dedupePaths(in []string) []string {
	var out []string
	for _, p := range in {
		if p == "" {
			continue
		}
		clean := canonicalPath(p)
		dup := false
		for _, q := range out {
			if samePath(q, clean) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, clean)
		}
	}
	return out
}
