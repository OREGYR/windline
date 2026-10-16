package main

import (
	"strings"
	"time"
)

// misc.go holds the small pieces the other files expect but which do not belong
// to any one of them.

// nowUnixMilli is the wall clock in milliseconds, used for the watchers'
// timestamps and for the fall's fading.
func nowUnixMilli() int64 { return time.Now().UnixMilli() }

// rgb is rgba with full alpha, for the many opaque colours in the renderer.
func rgb(r, g, b float64) uint32 { return rgba(r, g, b, 255) }

// Lf translates a template with a single number in it, which is how the
// "Keep %d photos" menu line reaches the other languages: the table holds the
// template, and the number is substituted after translation so the count lands
// where that language puts it.
func Lf(template string, n int) string {
	translated := L(template)
	return strings.ReplaceAll(translated, "%d", itoa(n))
}

// clampInt keeps an integer inside a range, used by the menu slots and the
// card indices.
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// callProc invokes a function at a raw address; COM vtable entries are not
// exported by name, so this is how they are reached.
func callProc(addr uintptr, args ...uintptr) uintptr {
	return callProcAddr(addr, args...)
}

// ---------------------------------------------------------------- line extras

// copiedID returns the photo currently showing its "Copied" pill, or 0.
func (l *line) copiedID() int64 {
	for _, it := range l.Items {
		if it.Copied {
			return it.ID
		}
	}
	return 0
}

// WatchingFolders reports how many folders are being watched, for the log.
func (l *line) WatchingFolders() int { return l.watchCount }

// SetWatchedFolders records how many folders are watched, purely for logging.
func (l *line) SetWatchedFolders(dirs []string) { l.watchCount = len(dirs) }

// Anchor is where a card hangs from: the point on the rope that its clip grips.
// The renderer sets the card's rectangle during composition, and the gesture code
// needs the same anchor to test the corner cross, so it is derived from the card's
// top edge and its centre.
func (it *item) Anchor() (float64, float64) {
	return it.CardRect.X + it.CardRect.W/2, it.CardRect.Y - cardOffsetBelowTop
}

// ---------------------------------------------------------------- app extras

// onClipboardMessage is WM_CLIPBOARDUPDATE, delivered to the panel's window.
func (a *app) onClipboardMessage() {
	if a.clipboard != nil {
		a.clipboard.check(a.adopt)
	}
}

// startFallFor is the renderer's hook name for the flight overlay.
func (o *flightOverlay) stopAndClear() { o.stop() }

// emptyRenderGuard keeps the frame from being composed before a monitor is
// chosen, which would divide by a zero width.
func (a *app) ready() bool {
	return a != nil && a.panel != nil && a.panel.stripRect.W > 1 && a.line != nil
}

// ---------------------------------------------------------------- panel extras

// dragging is true while an OLE drag is in flight.
var _ = 0

// repaintRecording is the recorder's prompt state.
func (p *panel) repaintRecording() {
	if p.recording {
		p.repaintRecordingPrompt()
	}
}

// setRecordingStyle is set while the shortcut recorder owns the window.
func (p *panel) setRecording(on bool) { p.recording = on }

// ---------------------------------------------------------------- renderer extras

// styleFromSystem picks the glass tint from the shell's app theme, which is the
// closest Windows signal to the adaptive material the original uses.
func (r *renderer) styleFromSystem() {
	const personalizeKey = `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`
	if v := readRegDWORD(hkeyCurrentUser, personalizeKey, "AppsUseLightTheme"); v == 0 {
		r.setStyle(glassLight)
	} else {
		r.setStyle(glassDark)
	}
}

// setStyle switches the glass variant, dropping the cached cards since their
// frames are baked in.
func (r *renderer) setStyle(style browserStyle) {
	if r.style == style {
		return
	}
	r.style = style
	r.discardAllCards()
}

// readRegDWORD reads a DWORD out of the registry, defaulting to 1.
func readRegDWORD(root uintptr, subkey, name string) uint32 {
	var hkey uintptr
	if call(pRegOpenKeyEx, root, utf16Ptr(subkey), 0, keyQueryValue, uptr(&hkey)) != 0 {
		return 1
	}
	defer call(pRegCloseKey, hkey)
	var typ, size uint32
	var value uint32
	size = 4
	if call(pRegQueryValue, hkey, utf16Ptr(name), 0, uptr(&typ), uptr(&value), uptr(&size)) != 0 {
		return 1
	}
	return value
}
