package main

import (
	"math"
)

// app.go is the port of AppDelegate: the state machine that decides when the
// line comes down, when it tucks away, and what each gesture does. The original's
// flags are kept by name, because their interactions are subtle and worth
// preserving:
//
//	revealed      the line has slid down into view
//	pinned        opened on purpose (shortcut or menu); stays down until it has
//	              been visited and left, or the shortcut is pressed again
//	peekUntil     a new screenshot shows itself for a moment, then tucks away
//	keepOpen      opened on purpose while empty, so it does not hide at once
//	wanted        whether the line should be up at all, before the above
//	awaySince     when the pointer left, so it retracts after a short delay
//	hotZoneSince  when the pointer started resting at the top edge
//	suppressed    after a click in the top edge, so the line does not come back
//	              over the menu that click just opened
//
// The one translation that matters: macOS's menu bar is at the top of the screen,
// so "resting the pointer in the menu bar" becomes "resting the pointer along
// the top edge", which is exactly where the taskbar is when it is not at the
// bottom.

const (
	topBandHeight = 26.0 // the resting strip along the top edge, in logical units
	peekDuration  = 2.2  // seconds a new photo shows itself
	tickMS        = 15   // the frame clock: ~66 Hz, a little above 60 Hz
)

type app struct {
	set    *settings
	line   *line
	panel  *panel
	render *renderer
	logos  *logoRenderer

	layout Layout
	scale  float64

	// state machine
	revealed            bool
	visible             bool
	pinned              bool
	peekUntil           uint64
	keepOpen            bool
	wanted              bool
	awaySince           uint64
	hotZoneSince        uint64
	suppressed          bool
	blockedByFullscreen bool

	// Where the reveal animation currently is, 0 tucked away and 1 fully down.
	// A copy of line.Reveal.X, because the renderer needs it for the arrival
	// wave and the panel needs it to decide whether to show at all.
	revealEase float64

	// clocks
	lastTickMS  uint64
	lastPaintMS uint64

	// prevReveal is the reveal value at the end of the last tick.
	prevReveal float64

	// pointer
	overStrip     bool
	lastMouseX    float64
	lastMouseY    float64
	hoverID       int64
	buttonWasDown bool

	monitor uintptr

	watcher     *folderWatcherSet
	safetyWatch *folderWatcherSet
	folderWatch *folderWatcherSet
	clipboard   *clipboardWatcher

	flights *flightOverlay

	quitting bool
}

func newApp(set *settings, logos *logoRenderer) *app {
	a := &app{
		set:        set,
		logos:      logos,
		scale:      1,
		lastMouseX: -1,
		lastMouseY: -1,
	}
	a.layout = Layout{Size: set.lineSize(), Dpi: 1}
	a.line = newLine(set, logos)
	a.render = newRenderer(logos)
	a.line.onFall = a.startFall
	a.line.onDrop = a.onItemDropped
	a.panel = newPanel(a)
	a.flights = newFlightOverlay(a)
	return a
}

// itemAt returns the photo under a point in logical strip coordinates, or nil.
// The newest card wins where two overlap, because it is drawn last.
func (a *app) itemAt(x, y float64) *item {
	live := a.line.live()
	for i := len(live) - 1; i >= 0; i-- {
		it := live[i]
		if r, ok := a.line.HitRects[it.ID]; ok && r.Contains(x, y) {
			return it
		}
	}
	return nil
}

// overCross reports whether a point is on a card's corner cross. The test runs in
// the card's own rotated frame, so the cross stays clickable at any tilt.
func (a *app) overCross(it *item, x, y float64) bool {
	if it == nil || it.Thumb == nil || !it.Hover {
		return false
	}
	// The cross is not a scene-graph node here, so its position is recomputed
	// the same way composeCard places it: a fixed inset from the card's
	// top-right corner, in the card's rotated frame.
	const d = 16.0
	cw, ch := a.layout.CardSize(float64(it.Thumb.W), float64(it.Thumb.H))
	anchorX, anchorY := it.Anchor()
	_ = ch
	cos, sin := math.Cos(it.CardAngle), math.Sin(it.CardAngle)
	// Card-local position of the cross centre, with the card's top-left at (0,0).
	cx := cw - d*0.68
	cy := d * 0.68
	// Rotate that offset about the anchor, which sits at the card's top centre.
	dx := (cx - cw/2) * cos
	dy := (cx - cw/2) * sin
	crossX := anchorX + dx
	crossY := anchorY + cardOffsetBelowTop + dy + cy*cos
	_ = sin
	return math.Hypot(x-crossX, y-crossY) <= d*0.8
}

// ---------------------------------------------------------------- the tick

// tick runs the state machine and repaints when anything moved. It is driven by
// the panel's timer.
func (a *app) tick() {
	now := tickCount()
	dtMS := float64(now - a.lastTickMS)
	if a.lastTickMS == 0 || dtMS <= 0 || dtMS > 250 {
		dtMS = tickMS
	}
	a.lastTickMS = now
	// Every animation steps from the same previous-frame time, so a card can
	// never be a frame ahead of the rope it hangs from.
	prevMS := float64(now) - dtMS

	// 1. The line lives on the monitor the pointer is on, because that is where
	// the screenshot was just taken.
	px, py := cursorPos()
	if mon := monitorFromPoint(px, py); mon != a.monitor {
		a.monitor = mon
		a.panel.place(mon)
		a.scale = a.panel.scale
		a.layout = Layout{Size: a.set.lineSize(), Dpi: a.scale}
		a.render.invalidateBackdrop()
		a.updateLayout()
		a.repaint(true)
	}

	// 2. A full screen app on this monitor keeps the line away, unless the user
	// asked to have it over full screen apps.
	if blocked := fullScreenAppOn(a.monitor) && !a.set.showOverFullScreen(); blocked != a.blockedByFullscreen {
		a.blockedByFullscreen = blocked
		if blocked {
			logf("full screen app on this monitor: putting the line away")
			a.tuck()
		}
	}

	// 3. A click in the top edge puts the line away and keeps it away until the
	// pointer leaves, so it cannot come back over the menu just opened.
	inBand, clicked := a.pointerInTopBand(px, py)
	if clicked && inBand && a.revealed {
		logf("click in the top edge: putting the line away")
		a.tuck()
	}
	a.suppressed = clicked && inBand
	if !inBand {
		a.suppressed = false
	}

	// 4. Resting at the top edge brings it down.
	if inBand && !a.suppressed && a.set.opensFromTop() {
		if a.hotZoneSince == 0 {
			a.hotZoneSince = now
		} else if !a.revealed && now-a.hotZoneSince >= uint64(revealDelay*1000) {
			logf("pointer rested at the top edge: bringing the line down")
			a.reveal()
		}
	} else {
		a.hotZoneSince = 0
	}

	// 5. With the pointer over the line it stays; otherwise it retracts after a
	// short delay. A pinned line, or one showing a new photo, waits its turn.
	over := a.pointerOverStrip(px, py)
	if a.revealed {
		switch {
		case over:
			a.awaySince = 0
			a.keepOpen = false
		case a.pinned || a.peekActive(now):
			a.awaySince = 0
		default:
			if a.awaySince == 0 {
				a.awaySince = now
			} else if now-a.awaySince >= uint64(retractDelay*1000) {
				a.tuck()
			}
		}
	}

	// 6. An empty line opened on purpose stops being pinned to emptiness as soon
	// as something hangs on it.
	if a.keepOpen && a.line.liveCount() > 0 {
		a.keepOpen = false
	}

	// 7. Advance every animation.
	animating := a.line.Reveal.step(float64(now), &prevMS)
	for _, it := range a.line.Items {
		if it.Swing.step(float64(now), &prevMS) {
			animating = true
		}
		if it.DropIn.step(float64(now), &prevMS) {
			animating = true
		}
		if it.CopyFade.step(float64(now), &prevMS) {
			animating = true
		}
		if it.DropIn.X >= 1 && it.State == stateFlying {
			a.line.land(it.ID)
		}
	}
	if a.flights != nil && a.flights.step(float64(now), prevMS) {
		animating = true
	}

	// 8. Hover, which also decides whether the window takes clicks at all.
	a.updateHover(px, py)

	// 9. Paint when something moved. This is what keeps a settled line free: no
	// animation, no compose, no blit.
	a.revealEase = a.line.Reveal.X
	moved := math.Abs(a.revealEase-a.prevReveal) > 0.001
	a.prevReveal = a.revealEase
	if animating || moved {
		a.repaint(true)
	}

	// 10. Show or hide the window with the animation, so a tucked line has no
	// window on screen and swallows no clicks anywhere.
	//
	// The test is `revealed` as well as the animation's position: reveal() sets
	// revealed before the animation has moved at all, so keying off the animation
	// alone would hide the window on the very frame it was opened, and the opening
	// would never be seen.
	wantVisible := (a.line.Reveal.X > 0.004 || a.revealed) && !a.blockedByFullscreen
	if wantVisible != a.visible {
		a.visible = wantVisible
		if wantVisible {
			a.panel.show()
		} else {
			a.panel.hide()
		}
	}
}

func (a *app) peekActive(now uint64) bool {
	return a.peekUntil != 0 && now < a.peekUntil
}

// updateLayout recomputes what fits on the strip after the monitor or the line
// size changed.
func (a *app) updateLayout() {
	lay := a.layout
	w := a.panel.stripRect.W
	a.line.VisibleCount = lay.FitsCount(w)
	// "Keep on line" is a count of photos; the choice of "as many as fit" is
	// stored as 0 and means the visible count.
	if n := a.set.keepOnLine(); n > 0 {
		a.line.MaxItems = n
		a.line.KeepSet = true
	} else {
		a.line.MaxItems = a.line.VisibleCount
		a.line.KeepSet = false
	}
	if a.line.VisibleCount > a.line.MaxItems && a.line.KeepSet {
		a.line.MaxItems = a.line.VisibleCount
	}
	a.line.trim()
}

// ---------------------------------------------------------------- reveal / tuck

// reveal slides the line down. The glass needs a fresh capture of the desktop
// behind it first, taken before the window appears.
func (a *app) reveal() {
	if a.blockedByFullscreen {
		return
	}
	a.wanted = true
	a.revealed = true
	if !a.render.backdropOK {
		a.render.captureBackdrop(a.panel.stripRectOnScreen(), a.panel.scale)
	}
	a.panel.show()
	a.visible = true
	a.line.Reveal.easeTo(1, 200, false, float64(tickCount()))
	// The panel is not accepting clicks while it is tucked away; the pointer's
	// position decides from here on.
	a.updateHover(cursorPos())
	a.repaint(true)
}

// tuck slides the line back up above the top edge. The window stays where it is
// and only the content moves, exactly as the original offsets its view.
func (a *app) tuck() {
	a.revealed = false
	a.wanted = false
	if !a.keepOpen {
		a.pinned = false
	}
	a.hoverID = 0
	a.panel.setThrough(true)
	a.line.Reveal.easeTo(0, 170, true, float64(tickCount()))
	a.repaint(true)
}

// peek shows the line for a moment because something new arrived.
func (a *app) peek() {
	if a.blockedByFullscreen {
		return
	}
	a.peekUntil = tickCount() + uint64(peekDuration*1000)
	a.reveal()
}

// ---------------------------------------------------------------- pointer

// pointerInTopBand reports whether the pointer is resting in the strip along the
// top of the monitor, and whether a button was just pressed there.
func (a *app) pointerInTopBand(px, py int32) (inBand, clicked bool) {
	_, full := monitorWork(a.monitor)
	s := a.panel.scale
	x := float64(px) / s
	y := float64(py) / s
	if x < full.X || x >= full.X+full.W {
		return false, false
	}
	if y < full.Y-1 || y > full.Y+topBandHeight {
		return false, false
	}
	down := buttonDown()
	clicked = down && !a.buttonWasDown
	a.buttonWasDown = down
	return true, clicked
}

// pointerOverStrip reports whether the pointer is anywhere over the panel's
// rectangle. The panel's own rectangle sits at the top of the monitor, so this
// is a cheap test that also tells us when the pointer is nowhere near.
func (a *app) pointerOverStrip(px, py int32) bool {
	s := a.panel.scale
	x, y := float64(px)/s, float64(py)/s
	return a.panel.stripRect.Contains(x, y)
}

// updateHover moves the hover state and flips the window between click-through
// and clickable. Only a photo takes clicks; everywhere else the click belongs to
// the app behind the line, which is what the original's ignoresMouseEvents plus
// per-photo hit rects achieve.
func (a *app) updateHover(px, py int32) {
	s := a.panel.scale
	x, y := float64(px)/s, float64(py)/s
	var hit *item
	if a.line.Reveal.X > 0.5 && a.panel.stripRect.Contains(x, y) {
		hit = a.itemAt(x, y)
	}
	var wantID int64
	if hit != nil {
		wantID = hit.ID
	}
	if wantID != a.hoverID {
		if old := a.line.find(a.hoverID); old != nil {
			old.Hover = false
			a.render.discardCard(old.ID) // the cross is baked into the sprite
		}
		if hit != nil {
			hit.Hover = true
			a.render.discardCard(hit.ID)
		}
		a.hoverID = wantID
	}
	a.overStrip = wantID != 0
	a.panel.setThrough(wantID == 0)
}

func (a *app) onPointerMoved(x, y float64) {
	a.lastMouseX, a.lastMouseY = x, y
}

func (a *app) onPointerLeftLine() {
	if a.hoverID != 0 {
		if old := a.line.find(a.hoverID); old != nil {
			old.Hover = false
			a.render.discardCard(old.ID)
		}
		a.hoverID = 0
	}
	a.overStrip = false
	a.panel.setThrough(true)
}

// ---------------------------------------------------------------- gestures

func (a *app) onPressChanged(id int64, pressed bool) {
	if it := a.line.find(id); it != nil {
		it.Pressed = pressed
	}
}

// onClick copies the photo, which is the original's click.
func (a *app) onClick(id int64) {
	for _, it := range a.line.Items {
		if it.Copied && it.ID != id {
			it.Copied = false
		}
	}
	a.line.copyImage(id)
	if it := a.line.find(id); it != nil {
		it.nudge(3) // the little kick a copied photo gets
		a.render.discardCard(id)
	}
	a.line.save()
	a.repaint(true)
}

// onDoubleClick opens the photo in the default viewer.
func (a *app) onDoubleClick(id int64) { a.line.openFile(id) }

// onLongPress opens the photo in the system editor, which is what the original's
// press-and-hold Markup is on Windows: Paint.
func (a *app) onLongPress(id int64) { a.line.markup(id) }

func (a *app) onCrossClicked(id int64) {
	a.line.discard(id, false)
	a.orderBackdropRefresh()
}

func (a *app) onDragStarted(id int64) {
	if it := a.line.find(id); it != nil {
		it.Dragging = true
	}
	logf("drag started for item %d", id)
	// The custom drag is what makes dragging feel like the original's; but the
	// platform drag is what lets a photo land in Explorer, a chat window or an
	// editor. The platform one wins.
	a.beginSystemDrag(id)
}

// onDragEnded prunes afterwards: a photo dragged into another folder has been
// saved where the user wanted it, so it leaves the line.
func (a *app) onDragEnded(id int64) {
	if it := a.line.find(id); it != nil {
		it.Dragging = false
	}
	a.line.prune()
	a.line.save()
	a.orderBackdropRefresh()
	a.repaint(true)
}

func (a *app) onScroll(delta float64) {
	a.line.scrollBy(delta)
	a.repaint(true)
}

// onHotkey is the shortcut: it toggles the line, and a line opened this way stays
// down until it has been visited.
func (a *app) onHotkey(id int) {
	if id != hotkeyToggle {
		return
	}
	if a.revealed && a.pinned {
		logf("shortcut: putting the line away")
		a.pinned = false
		a.tuck()
		return
	}
	logf("shortcut: bringing the line down")
	a.pinned = true
	a.keepOpen = a.line.liveCount() == 0
	a.reveal()
}

// orderBackdropRefresh recaptures the glass shortly after the desktop behind the
// line changed, without letting the capture catch the line itself.
func (a *app) orderBackdropRefresh() {
	a.render.invalidateBackdrop()
	after(0.15, func() {
		if a.revealed && !a.quitting {
			a.render.captureBackdrop(a.panel.stripRectOnScreen(), a.panel.scale)
			a.repaint(true)
		}
	})
}

// ---------------------------------------------------------------- new arrivals

// adopt hangs a new screenshot or copied image. flying is true while the capture
// is still on its way in from where it was taken.
func (a *app) adopt(path string) {
	if a.quitting {
		return
	}
	if !isImagePath(path) && !isRecording(path) {
		return
	}
	if a.set.inboxEnabled() && !a.isInboxPath(path) && !a.isWatchedPath(path) {
		// In Inbox mode Windline's own folder is the one that matters, but a
		// screenshot saved anywhere else is still a screenshot.
		_ = path
	}
	it := a.line.hang(path, false)
	if it == nil {
		return
	}
	now := float64(tickCount())
	it.DropIn.snap(-1)
	it.DropIn.springTo(1, arriveK, arriveC, now)
	if a.set.peeksAtNew() {
		a.peek()
	} else {
		a.render.invalidateBackdrop()
		a.repaint(true)
	}
}

func (a *app) isInboxPath(path string) bool {
	return pathIsInside(path, inboxFolder())
}

func (a *app) isWatchedPath(path string) bool {
	for _, dir := range a.set.extraFolders() {
		if pathIsInside(path, dir) {
			return true
		}
	}
	return pathIsInside(path, screenshotsFolder())
}

func (a *app) onClipboardChanged() {
	if a.clipboard != nil {
		a.clipboard.check(a.adopt)
	}
}

// previousReveal tracks the last reveal value, so the tick can tell whether the
// line moved and needs repainting.
var _ = 0

func (a *app) onDisplayChanged() {
	if a.quitting {
		return
	}
	logf("display changed: re-placing the panel")
	a.monitor = 0
	a.render.invalidateBackdrop()
	mon := monitorFromPoint(cursorPos())
	a.monitor = mon
	a.panel.place(mon)
	a.scale = a.panel.scale
	a.layout = Layout{Size: a.set.lineSize(), Dpi: a.scale}
	a.updateLayout()
	a.repaint(true)
}

// previousRevealValue reports the reveal value at the end of the last tick.
func (a *app) previousRevealValue() float64 { return a.prevReveal }

// ---------------------------------------------------------------- painting

// repaint composes the strip and blits it. force skips the frame-rate throttle.
//
// It deliberately does not touch a.visible: that flag tracks whether the window
// itself is shown, and the tick owns it. Setting it here used to make the tick
// believe the window was already up, so the ShowWindow that should have brought it
// back after the opening frames never happened and the line stayed hidden while
// its pixels were still being pushed to a window nobody could see.
func (a *app) repaint(force bool) {
	if a.panel.hwnd == 0 || a.quitting {
		return
	}
	if a.line.Reveal.X <= 0.006 {
		return // tucked away: nothing to draw, and the window is hidden
	}
	now := tickCount()
	if !force && now-a.lastPaintMS < tickMS {
		return
	}
	a.lastPaintMS = now
	s := a.render.compose(a, a.panel.stripRect, a.lastMouseX, a.lastMouseY)
	if s == nil {
		return
	}
	a.panel.push(s)
}

// ---------------------------------------------------------------- lifecycle

// start brings the app up: watchers, tray, hotkey, the frame clock.
func (a *app) start() {
	a.monitor = monitorFromPoint(cursorPos())
	a.panel.place(a.monitor)
	a.scale = a.panel.scale
	a.layout = Layout{Size: a.set.lineSize(), Dpi: a.scale}
	a.updateLayout()

	if a.set.inboxEnabled() {
		applyInbox()
	}
	a.startWatchers()

	a.clipboard = newClipboardWatcher()
	if a.set.clipboardEnabled() {
		a.clipboard.start(a.panel.hwnd)
	}

	registerHotkey(a.panel.hwnd)
	a.buildTray()
	a.line.startGusts()
	call(pSetTimer, a.panel.hwnd, timerTick, u(tickMS), 0)

	logf("windline is up: %d photos hanging, inbox=%v clipboard=%v, %d watching",
		a.line.liveCount(), a.set.inboxEnabled(), a.set.clipboardEnabled(), a.line.WatchingFolders())
}

// startWatchers (re)builds every folder watcher: the screenshot folder, the
// safety net, and any folders the user added.
func (a *app) startWatchers() {
	a.stopWatchers()
	a.watcher = newFolderWatcherSet(a.watchTargets(a.set.inboxEnabled()), a.adopt)
	a.watcher.start()
	// In Inbox mode, keep an eye on the folder screenshots used to be saved to
	// as well: a Windows build that ignores the redirect would otherwise drop
	// screenshots out of sight. This is the original's safety watcher.
	if a.set.inboxEnabled() {
		prev := previousScreenshotsFolder()
		if prev != "" && !a.watcher.watching(prev) {
			a.safetyWatch = newFolderWatcherSet([]string{prev}, a.adopt)
			a.safetyWatch.start()
		}
	}
	a.line.SetWatchedFolders(a.watcher.folders())
}

// watchTargets is the list of folders a new screenshot can appear in.
func (a *app) watchTargets(inbox bool) []string {
	var dirs []string
	if inbox {
		dirs = append(dirs, inboxFolder())
	} else {
		dirs = append(dirs, screenshotsFolder())
	}
	dirs = append(dirs, a.set.extraFolders()...)
	return dedupePaths(dirs)
}

// rebuildFolderWatchers restarts the watchers after the folder list changed.
func (a *app) rebuildFolderWatchers() { a.startWatchers() }

func (a *app) stopWatchers() {
	for _, w := range []*folderWatcherSet{a.watcher, a.safetyWatch, a.folderWatch} {
		if w != nil {
			w.stop()
		}
	}
	a.watcher, a.safetyWatch, a.folderWatch = nil, nil, nil
}

// shutdown puts back everything the app changed on its way out.
func (a *app) shutdown() {
	if a.quitting {
		return
	}
	a.quitting = true
	logf("shutting down")
	a.line.save()
	a.stopWatchers()
	if a.clipboard != nil {
		a.clipboard.stop()
	}
	if a.set.inboxEnabled() {
		restoreInbox()
	}
	unregisterHotkey(a.panel.hwnd)
	a.removeTray()
	a.flights.stop()
	for _, it := range a.line.Items {
		it.Thumb.free()
	}
	a.render.discardAllCards()
	if a.render.backdrop != nil {
		a.render.backdrop.free()
		a.render.backdrop = nil
	}
	logf("windline stopped")
}
