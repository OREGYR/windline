package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// line.go is the model: what is hanging, and what you can do with each photo.
// It is a port of the original's Line class. The files never move; the line is
// only a view onto them.

// item states.
type itemState int

const (
	stateFlying itemState = iota // still flying in from where it was captured
	stateLanded
	stateFalling // let go: drawing its fall over the whole screen
)

// item is one screenshot hanging on the line.
type item struct {
	ID       int64
	Path     string
	Thumb    *thumb
	Tilt     float64 // every photo hangs a little crooked, like on a real line
	State    itemState
	Live     bool
	Arrived  bool // the drop-in animation has run
	Swing    anim // sway from the breeze, in degrees
	DropIn   anim // 0 -> 1 arrival
	Hover    bool
	Pressed  bool
	Dragging bool
	Copied   bool
	CopyFade anim // the "Copied" pill's opacity
	Row      int
	RowTotal int

	// cached geometry from the last layout, in strip coordinates
	CardRect  Rect
	CardAngle float64
	CardAlpha float64
}

func (it *item) isRecording() bool { return isRecording(it.Path) }

// line is the whole clothesline.
type line struct {
	set  *settings
	logo *logoRenderer

	Items   []*item
	Scroll  float64 // how far the line is scrolled towards older photos
	Gust    anim    // the breeze
	Reveal  anim    // 0 tucked away above the screen, 1 fully down
	Stagger anim    // 0 -> visibleCount-1, the diagonal wave as cards arrive

	VisibleCount int // how many photos fit across the screen
	MaxItems     int // how many the line keeps (Keep on line)
	KeepSet      bool

	nextID int64

	// onFall is called just before a photo starts falling, so the fall can be
	// drawn over the whole screen rather than clipped by the strip.
	onFall func(*item)

	// HitRects is reported after each layout so the panel can decide whether a
	// click belongs to a photo or to whatever is underneath.
	HitRects map[int64]Rect

	// soundOn mirrors the Sounds menu item.
	SoundOn func() bool
	onDrop  func(*item)

	// watchCount is how many folders are being watched, for the log line.
	watchCount int
}

func newLine(set *settings, logos *logoRenderer) *line {
	l := &line{
		set:      set,
		logo:     logos,
		HitRects: map[int64]Rect{},
		SoundOn:  set.soundOn,
	}
	l.Reveal.snap(0)
	l.restore()
	return l
}

// live returns the items that are not falling.
func (l *line) live() []*item {
	out := make([]*item, 0, len(l.Items))
	for _, it := range l.Items {
		if it.State != stateFalling {
			out = append(out, it)
		}
	}
	return out
}

func (l *line) liveCount() int {
	n := 0
	for _, it := range l.Items {
		if it.State != stateFalling {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------- hanging

// hang adds a file to the line. It returns the new item, or nil when the file is
// already hanging or cannot be decoded.
func (l *line) hang(path string, flying bool) *item {
	clean := canonicalPath(path)
	for _, it := range l.Items {
		if it.State != stateFalling && samePath(it.Path, clean) {
			return nil
		}
	}
	maxPixels := int(320 * l.set.lineSize().Scale())
	t := makeThumbnail(clean, maxPixels)
	if t == nil {
		logf("hang: cannot decode %s", clean)
		return nil
	}
	l.nextID++
	it := &item{
		ID:      l.nextID,
		Path:    clean,
		Thumb:   t,
		Tilt:    randRange(-2.5, 2.5),
		State:   stateLanded,
		Live:    true,
		Arrived: false,
	}
	if flying {
		it.State = stateFlying
	}
	it.Swing.snap(0)
	it.DropIn.snap(-1) // -1 = off the line, 1 = in place
	l.Items = append(l.Items, it)
	l.Scroll = 0
	l.save()
	l.play("tink")
	logf("hang: %s (%dx%d, %d items)", clean, t.W, t.H, len(l.Items))

	// A full line lets one photo fall off the far end: only one, so a line hung
	// on a wider screen keeps its length instead of losing several at once.
	if l.liveCount() > l.MaxItems {
		for _, old := range l.Items {
			if old.State != stateFalling && old.ID != it.ID {
				l.letGo(old.ID)
				break
			}
		}
	}
	return it
}

// land finishes a flight: the real card takes over.
func (l *line) land(id int64) {
	if it := l.find(id); it != nil && it.State == stateFlying {
		it.State = stateLanded
	}
}

func (l *line) find(id int64) *item {
	for _, it := range l.Items {
		if it.ID == id {
			return it
		}
	}
	return nil
}

// drop lets a photo fall off the line. The item stays in the list until the
// fall finishes, so the animation has something to draw.
func (l *line) drop(id int64, quietly bool) {
	it := l.find(id)
	if it == nil || it.State == stateFalling {
		return
	}
	if l.onFall != nil {
		l.onFall(it)
	}
	it.State = stateFalling
	delete(l.HitRects, id)
	l.save()
	if !quietly {
		l.play("pop")
	}
	if l.onDrop != nil {
		l.onDrop(it)
	}
}

// remove drops a finished fall from the list.
func (l *line) remove(id int64) {
	for i, it := range l.Items {
		if it.ID == id {
			it.Thumb.free()
			l.Items = append(l.Items[:i], l.Items[i+1:]...)
			return
		}
	}
}

// clear is "Take everything down": every photo goes the way of its corner cross.
func (l *line) clear() {
	live := l.live()
	for n, it := range live {
		id, quiet := it.ID, n > 0
		after(0.06*float64(n), func() { l.discard(id, quiet) })
	}
}

// neighbours returns the photos hung before (left) or after (right) this one.
func (l *line) neighbours(id int64, toTheLeft bool) []*item {
	live := l.live()
	idx := -1
	for i, it := range live {
		if it.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	if toTheLeft {
		return live[:idx]
	}
	return live[idx+1:]
}

// takeDown takes down everything on one side of a photo.
func (l *line) takeDown(id int64, toTheLeft bool) {
	live := l.neighbours(id, toTheLeft)
	for n, it := range live {
		id, quiet := it.ID, n > 0
		after(0.06*float64(n), func() { l.discard(id, quiet) })
	}
}

// prune notices photos whose file was deleted or moved away, and lets them fall.
func (l *line) prune() {
	changed := false
	for _, it := range l.Items {
		if it.State == stateFalling {
			continue
		}
		if !fileExists(it.Path) {
			logf("prune: %s is gone", it.Path)
			l.drop(it.ID, true)
			changed = true
		}
	}
	if changed {
		l.save()
	}
}

// trim drops the oldest photos past the Keep on line limit.
func (l *line) trim() {
	for l.liveCount() > l.MaxItems {
		var oldest *item
		for _, it := range l.Items {
			if it.State != stateFalling {
				oldest = it
				break
			}
		}
		if oldest == nil {
			break
		}
		l.letGo(oldest.ID)
	}
	l.Scroll = 0
}

// letGo is the oldest photo falling off a full line: like the cross, a file in
// Windline's own folder goes to the Recycle Bin, or nothing would ever take it
// out of there. If the Recycle Bin refuses it, it still leaves the line.
func (l *line) letGo(id int64) {
	if l.isInInbox(id) && l.trash(id, true) {
		return
	}
	l.drop(id, true)
}

// ---------------------------------------------------------------- actions

// isInInbox reports whether the file lives in Windline's own folder. Those go to
// the Recycle Bin when discarded, or the folder would fill with forgotten
// screenshots. Files anywhere else stay where they are.
func (l *line) isInInbox(id int64) bool {
	it := l.find(id)
	if it == nil {
		return false
	}
	dir := inboxFolder()
	rel, err := filepath.Rel(dir, it.Path)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

// discard is what the corner cross and "Take down" both end up doing.
func (l *line) discard(id int64, quietly bool) {
	if l.isInInbox(id) {
		l.trash(id, quietly)
		return
	}
	l.drop(id, quietly)
}

// trash sends the file to the Recycle Bin and takes the photo off the line.
func (l *line) trash(id int64, quietly bool) bool {
	it := l.find(id)
	if it == nil {
		return false
	}
	if err := recycle(it.Path); err != nil {
		logf("recycle %s failed: %v", it.Path, err)
		if !quietly {
			l.play("beep")
		}
		return false
	}
	logf("recycled %s", it.Path)
	l.drop(id, true)
	return true
}

// copyImage puts the image on the clipboard, and the file itself alongside it so
// that pasting into Explorer or a chat app still works.
func (l *line) copyImage(id int64) {
	it := l.find(id)
	if it == nil {
		return
	}
	if err := copyToClipboard(it.Path, it.isRecording()); err != nil {
		logf("clipboard copy failed: %v", err)
		return
	}
	it.Copied = true
	it.CopyFade.snap(1)
	after(1.2, func() {
		if it.Copied {
			it.Copied = false
		}
	})
}

// openFile opens the photo in the user's default viewer.
func (l *line) openFile(id int64) {
	it := l.find(id)
	if it == nil {
		return
	}
	shellOpen(it.Path)
}

// markup opens the photo in the system editor: Paint on Windows, which is what
// macOS's Markup is to a screenshot. A recording opens in the default player
// instead, and the item's thumbnail is refreshed when the editor closes.
func (l *line) markup(id int64) {
	it := l.find(id)
	if it == nil {
		return
	}
	path := it.Path
	if it.isRecording() {
		shellOpen(path)
		return
	}
	before := fileModTime(path)
	runEditor(path)
	after(1.0, func() {
		if fileModTime(path) != before {
			logf("editor changed %s, reloading its thumbnail", path)
			l.reloadThumbnail(path)
		}
	})
}

// quickLook opens the photo in the system photo viewer with the rest of the line
// a key press away: Explorer's viewer, driven by the shell.
func (l *line) quickLook(id int64) {
	it := l.find(id)
	if it == nil {
		return
	}
	shellOpen(it.Path)
}

// showInExplorer selects the file in a new Explorer window.
func (l *line) showInExplorer(id int64) {
	it := l.find(id)
	if it == nil {
		return
	}
	call(pShellExecute, 0, utf16Ptr("open"), utf16Ptr("explorer.exe"),
		utf16Ptr("/select,"+it.Path), 0, swShowNA)
}

// saveToDesktop moves the file to the Desktop and takes it off the line: the
// Windows stand-in for the original's "Save to Desktop", and the way to keep a
// screenshot that Inbox mode put in Windline's own folder.
func (l *line) saveToDesktop(id int64) {
	it := l.find(id)
	if it == nil {
		return
	}
	desktop := desktopFolder()
	target := uniquePath(filepath.Join(desktop, filepath.Base(it.Path)))
	if err := os.Rename(it.Path, target); err != nil {
		if err := copyFile(it.Path, target); err != nil {
			logf("save to Desktop failed: %v", err)
			l.play("beep")
			return
		}
		os.Remove(it.Path)
	}
	logf("saved %s to the Desktop as %s", it.Path, target)
	l.drop(id, true)
}

// reloadThumbnail re-decodes one photo after an edit.
func (l *line) reloadThumbnail(path string) {
	for _, it := range l.Items {
		if it.State != stateFalling && samePath(it.Path, path) {
			maxPixels := int(320 * l.set.lineSize().Scale())
			if t := makeThumbnail(path, maxPixels); t != nil {
				it.Thumb.free()
				it.Thumb = t
			}
			return
		}
	}
}

// reloadThumbnails re-decodes every photo, after the Size setting changes.
func (l *line) reloadThumbnails() {
	maxPixels := int(320 * l.set.lineSize().Scale())
	for _, it := range l.Items {
		if it.State == stateFalling {
			continue
		}
		if t := makeThumbnail(it.Path, maxPixels); t != nil {
			it.Thumb.free()
			it.Thumb = t
		}
	}
}

// scrollBy moves along a line that holds more than fits on screen.
func (l *line) scrollBy(delta float64) {
	most := float64(max(0, len(l.Items)-l.VisibleCount)) * layoutOf(l.set).Spacing()
	next := min(most, max(0, l.Scroll+delta))
	if next != l.Scroll {
		l.Scroll = next
	}
}

// ---------------------------------------------------------------- breeze

// gust is the little wind that moves the line every so often. It is the detail
// that makes it feel like an object and not a widget.
func (l *line) startGusts() {
	var schedule func()
	schedule = func() {
		after(randRange(7, 16), func() {
			if len(l.Items) > 0 && !l.anyDragging() {
				l.Gust.X = 1 // any change wakes the cards up; the amount is random per card
			}
			schedule()
		})
	}
	schedule()
}

func (l *line) anyDragging() bool {
	for _, it := range l.Items {
		if it.Dragging {
			return true
		}
	}
	return false
}

// breeze nudges one card, with the per-card random delay the original uses so
// they do not move in lockstep.
func (it *item) breeze(nowMS float64) {
	delay := randRange(0, 0.35)
	degrees := randRange(1.6, 3.4)
	it.Swing.springTo(degrees, swingK, swingC, nowMS+delay*0)
	it.Swing.easeTo(degrees, 300, false, nowMS+delay*1000)
	// The nudge eases out, then springs back to rest.
	it.Swing.easeTo(degrees, 300, false, nowMS+delay*1000)
	after(0.3+delay, func() {
		it.Swing.springTo(0, settleK, settleC, float64(tickCount()))
	})
}

// nudge is the small sway after landing or a copy.
func (it *item) nudge(degrees float64) {
	now := float64(tickCount())
	it.Swing.easeTo(degrees, 300, false, now)
	after(0.3, func() {
		it.Swing.springTo(0, settleK, settleC, float64(tickCount()))
	})
}

// ---------------------------------------------------------------- persistence

// save stores the paths, exactly as the original stores them in UserDefaults.
func (l *line) save() {
	paths := make([]string, 0, len(l.Items))
	for _, it := range l.Items {
		if it.State != stateFalling {
			paths = append(paths, it.Path)
		}
	}
	l.set.setList(keyLineItems, paths)
}

// restore brings back what was hanging last time, skipping files that are gone.
func (l *line) restore() {
	paths := l.set.getList(keyLineItems)
	for _, p := range paths {
		if !fileExists(p) {
			continue
		}
		maxPixels := int(320 * l.set.lineSize().Scale())
		t := makeThumbnail(p, maxPixels)
		if t == nil {
			continue
		}
		l.nextID++
		it := &item{
			ID: l.nextID, Path: canonicalPath(p), Thumb: t,
			Tilt: randRange(-2.5, 2.5), State: stateLanded, Live: true,
		}
		it.Swing.snap(0)
		it.DropIn.snap(1) // restored photos are already in place
		l.Items = append(l.Items, it)
	}
	if len(l.Items) > 0 {
		logf("restored %d photos from last time", len(l.Items))
	}
}

// ---------------------------------------------------------------- sound

// play makes a small sound, when Sounds is on in the menu.
func (l *line) play(kind string) {
	if l.SoundOn != nil && !l.SoundOn() {
		return
	}
	playSound(kind)
}

// ---------------------------------------------------------------- helpers

func layoutOf(set *settings) Layout {
	return Layout{Size: set.lineSize(), Dpi: 1}
}

// canonicalPath normalises a path so two spellings of the same file compare
// equal. Windows paths are case-insensitive, which os.Rename and the file
// watchers both care about.
func canonicalPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	abs = filepath.Clean(abs)
	// filepath.EvalSymlinks would also resolve 8.3 names, but it touches the
	// filesystem and can fail on a path we are about to accept anyway.
	return abs
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// uniquePath returns a path in dir that does not exist yet, adding " 2", " 3"
// and so on before the extension, as Finder and Explorer both do.
func uniquePath(target string) string {
	if !fileExists(target) {
		return target
	}
	dir := filepath.Dir(target)
	base := strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
	ext := filepath.Ext(target)
	for n := 2; n < 1000; n++ {
		candidate := filepath.Join(dir, base+" "+itoa(n)+ext)
		if !fileExists(candidate) {
			return candidate
		}
	}
	return target
}

func fileModTime(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.ModTime().UnixNano()
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// sortByCreation orders paths by creation time, oldest first, the order the
// original adopts files in.
func sortByCreation(paths []string) {
	sort.SliceStable(paths, func(i, j int) bool {
		return creationTime(paths[i]).Before(creationTime(paths[j]))
	})
}

func creationTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	// Windows has a real creation time; fall back to modification time when the
	// filesystem does not report one.
	if t, ok := creationTimeOf(info); ok {
		return t
	}
	return info.ModTime()
}
