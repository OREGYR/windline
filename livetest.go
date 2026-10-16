package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// livetest.go is the end-to-end check: it starts the real application — the real
// window, the real layered blit, the real timer-driven animation — reveals the
// line, and then reads the actual pixels off the screen and compares them against
// what the app believes it drew.
//
// --selftest proves the renderer produces the right images. This proves the images
// reach the screen correctly composited over the desktop, which is the part an
// offscreen test cannot see: whether UpdateLayeredWindow is wired up, whether the
// window is actually visible, and whether per-pixel alpha lets the desktop through
// where nothing is hanging.
//
// The comparison needs no assumptions about the wallpaper. Two properties are
// checked, and together they pin the result exactly:
//
//   - Where the composed frame is fully opaque, the screen must show the composed
//     pixels: the window covers the desktop completely there.
//   - Where the composed frame is fully transparent, the screen must show exactly
//     what was there before the line came down: the desktop must come through
//     untouched.
//
// usage: windline --livetest --out=DIR

type liveReport struct {
	OutDir    string
	Failures  []string
	Notes     map[string]any
	OpaqueOK  int
	OpaqueN   int
	ClearOK   int
	ClearN    int
	ScreenW   int
	ScreenH   int
	Visible   bool
	MaxDelta  int
	WorstAt   string
	ChangedOK int
}

func runLiveTest(set *settings, args []string) {
	outDir := flagValue(args, "--out")
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "windline-livetest")
	}
	os.MkdirAll(outDir, 0o755)
	rep := &liveReport{OutDir: outDir, Notes: map[string]any{}}
	fail := func(f string) { rep.Failures = append(rep.Failures, f) }

	// The log is the only channel this test has once it is inside a window
	// procedure, so every stage announces itself.
	logStart()
	defer logStop()

	// A watchdog, because a GUI test that deadlocks would otherwise just sit
	// there: after 25 seconds it writes every goroutine's stack to the log, which
	// names the blocking call exactly.
	go func() {
		time.Sleep(25 * time.Second)
		buf := make([]byte, 1<<18)
		n := runtime.Stack(buf, true)
		logf("livetest watchdog: still running after 25 s\n%s", string(buf[:n]))
	}()

	// Move the console we were started from out of the way.
	//
	// The strip under test is the top of the screen, and a console window sitting
	// there is a program on top of the comparison: the captures would show the
	// console, not the line, and every check would fail with colours that have
	// nothing to do with Windline. The returned function puts it back.
	restoreConsole, consoleMoved := moveConsoleOutOfTheWay()
	defer restoreConsole()

	gdiplusStart()
	defer gdiplusStop()
	soundStart()

	// A photo has to be hanging for there to be anything to compare.
	sample := filepath.Join(outDir, "sample.png")
	if err := makeSampleImage(sample, 640, 400); err != nil {
		fmt.Println("livetest:", err)
		return
	}

	// The test has to start from a known line and known settings. A previous run
	// leaves photos in the settings file, and a real user's line would be full of
	// them; either way the comparison below would be measuring someone else's tray.
	//
	// Opening from the top edge is switched off for the duration, and restored
	// afterwards. It is a real feature and it works — which is exactly the problem:
	// on a machine whose pointer happens to rest against the top of the screen, the
	// hot zone brings the line back down by itself, and the tuck check at the end
	// would be measuring the feature rather than the compositor.
	restoreOpenFromTop := configureForLiveTest(set)
	defer restoreOpenFromTop()
	rep.Notes["consoleMovedOutOfTheStrip"] = consoleMoved
	clearSavedLine(set)
	logf("livetest: starting the app")

	logos := newLogoRenderer()
	a := newApp(set, logos)
	uiThreadID = currentThreadID()

	// The real startup path: window, watchers, tray, hotkey, frame clock.
	a.start()
	defer a.shutdown()
	if n := len(a.line.Items); n != 0 {
		fail(fmt.Sprintf("the line started with %d photos on it, so the screen comparison would be measuring a different line", n))
	}

	logf("livetest: hanging the sample photo")
	item := a.line.hang(sample, false)
	if item == nil {
		reportLive(rep)
		return
	}
	item.DropIn.snap(1)

	// The panel exists and is placed on this monitor.
	strip := a.panel.stripRectOnScreen()
	rep.ScreenW = int(math.Round(strip.W))
	rep.ScreenH = int(math.Round(strip.H))
	rep.Notes["stripOnScreen"] = map[string]float64{"x": strip.X, "y": strip.Y, "w": strip.W, "h": strip.H}
	rep.Notes["scale"] = a.panel.scale

	// 1. Learn which pixels of this desktop are too restless to testify, by watching
	//    the desktop alone for at least as long as the app is about to be on screen.
	//
	//    Two different kinds of movement have to be dealt with, and they need
	//    different answers. A *slow* change — a wallpaper fade, an overlay sliding in
	//    — is handled by the interval test below, which brackets the app's visit with
	//    a capture on each side and accepts any value in between. A *transient* — a
	//    notification, a tooltip, a preview popup, a cursor — appears and is gone
	//    between the two bracket captures, so neither endpoint reveals it, and a
	//    pixel showing one cannot testify about the window. Repeated sampling of the
	//    untouched desktop is what finds transients: anything that ever changed is
	//    marked, so it is judged by what it does over the window rather than by how it
	//    happened to look at two moments.
	//
	//    The sampling window is deliberately as long as the app interval that follows,
	//    because a shorter one simply misses slow events. Doing this with the app
	//    absent keeps it honest: the exclusion can only ever excuse the desktop. The
	//    number left usable is asserted afterwards, so a desktop noisy enough to
	//    exclude the whole strip cannot turn the checks into a formality.
	const preWindowMS = 3000
	const preSamples = 8
	// The capture geometry, known before any capture is taken: every capture of the
	// strip is the same size, so the mask can be sized from it directly.
	capW := int(math.Round(strip.W))
	capH := int(math.Round(strip.H))
	logf("livetest: watching the untouched desktop for %d ms in %d samples to find what moves on its own", preWindowMS, preSamples)
	var baseline *surface
	flicker := make([]bool, capW*capH)
	var noiseGrid [gxGrid * gyGrid]int
	noise, noiseTotal := 0, 0
	for i := 0; i < preSamples; i++ {
		waitPumping(preWindowMS / preSamples)
		s := captureScreen(strip)
		if baseline == nil {
			baseline = s // the reference the whole test compares against
			continue
		}
		for y := 0; y < capH; y++ {
			for x := 0; x < capW; x++ {
				r1, g1, b1 := baseline.rgbAt(x, y)
				r2, g2, b2 := s.rgbAt(x, y)
				if max3(abs8(r1, r2), abs8(g1, g2), abs8(b1, b2)) > matchTol {
					flicker[y*capW+x] = true
				}
			}
		}
		s.free()
	}
	// The stickiest pixel in the window decides: any variation at all marks a pixel
	// as furniture. Counted on a sample grid purely so the report says where.
	for y := margin; y < capH-margin; y += step {
		for x := margin; x < capW-margin; x += step {
			noiseTotal++
			if flicker[y*capW+x] {
				noise++
				noiseGrid[y*gyGrid/capH*gxGrid+x*gxGrid/capW]++
			}
		}
	}
	flickerCount := 0
	for _, f := range flicker {
		if f {
			flickerCount++
		}
	}
	rep.Notes["desktopNoise"] = fmt.Sprintf("%d of %d sampled pixels moved at all while watching an untouched desktop for %d ms (%d of %d pixels overall)",
		noise, noiseTotal, preWindowMS, flickerCount, len(flicker))
	if noise > 0 {
		rep.Notes["desktopNoiseGrid"] = gridString(noiseGrid, gxGrid, gyGrid, capW, capH)
	}

	// 2. The screen as it stands, with the line still away, and the moment the
	//    shortcut is pressed.
	//
	//    This is one end of a bracket. The other end is the capture taken after the
	//    line has been tucked away again, and because both are pictures of the desktop
	//    with no window of ours on screen, the pair bounds how much this desktop
	//    moved over exactly the interval the app was on screen. That matters: an
	//    earlier version of this test measured the desktop's restlessness over its own
	//    fixed window and then applied the answer to a longer one, which
	//    under-estimated a slow fade badly enough to blame the app for tens of
	//    thousands of pixels it never touched.
	logf("livetest: capturing the screen just before the line comes down")
	before := baseline
	before.savePNG(filepath.Join(outDir, "screen-before.png"))
	baseline.savePNG(filepath.Join(outDir, "screen-churn.png"))

	// 2. Bring the line down the way a user does — through the shortcut — and let the
	//    animation finish. The shortcut pins the line, which is what keeps it on
	//    screen for the capture; an unpinned line is designed to retract by itself.
	//    pumpFor runs the real message loop, so the real WM_TIMER handler drives the
	//    real animation.
	logf("livetest: revealing the line")
	a.onHotkey(hotkeyToggle)
	pumpFor(900)
	rep.Notes["revealX"] = a.line.Reveal.X
	rep.Notes["pinned"] = a.pinned
	rep.Notes["panelVisibleFlag"] = a.panel.visible
	rep.Notes["isWindowVisible"] = call(pIsWindowVisible, a.panel.hwnd) != 0
	rep.Visible = a.panel.visible && call(pIsWindowVisible, a.panel.hwnd) != 0
	if !rep.Visible {
		fail("the panel is not visible after reveal")
	}
	if a.blockedByFullscreen {
		fail("the line considers itself blocked by a full screen app")
	}
	if a.line.Reveal.X < 0.99 {
		fail(fmt.Sprintf("the reveal animation did not finish: %.3f", a.line.Reveal.X))
	}

	composed := a.panel.lastSurface
	if composed == nil {
		fail("nothing was pushed to the window")
		reportLive(rep)
		return
	}
	// A private copy, because the surface panel.push handed over is freed by the
	// app's next repaint and the tuck below causes one.
	composed = snapshotSurface(composed)
	defer composed.free()
	composed.savePNG(filepath.Join(outDir, "composed.png"))

	// 3. The screen as it is now, with the line down.
	logf("livetest: capturing the screen with the line down")
	after := captureScreen(strip)
	after.savePNG(filepath.Join(outDir, "screen-after.png"))

	// The opaque pixels of the composition, copied out now.
	//
	// This has to happen before the line is tucked away: panel.push frees the
	// surface it replaces, and every repaint hands over a new one, so the pointer in
	// `composed` stops being valid the moment the app draws again. The leftover check
	// below runs after the tuck, so it works from this copy instead.
	type opaqueRef struct {
		x, y    int
		r, g, b byte
	}
	var opaqueRefs []opaqueRef
	for y := 0; y < composed.H; y++ {
		for x := 0; x < composed.W; x++ {
			if composed.alphaAt(x, y) < opaqueAlpha {
				continue
			}
			r, g, b, _ := composed.colorAt(x, y) // un-premultiplied
			opaqueRefs = append(opaqueRefs, opaqueRef{x: x, y: y, r: r, g: g, b: b})
		}
	}
	rep.Notes["opaquePixelsInComposition"] = len(opaqueRefs)

	// 3. Put the line away again and photograph the desktop once more.
	//
	//    This is the other half of the bracket around the app interval, and it is
	//    taken before the comparison because the comparison needs it: the desktop's
	//    value at a partly-covered pixel is only known to lie somewhere between what
	//    `before` shows and what this shows. Two captures of the desktop with nothing
	//    of ours on screen, one on each side of the app's visit, is what turns "the
	//    desktop drifted" from an excuse into something the test can work with.
	logf("livetest: putting the line away and capturing the desktop after it")
	a.onHotkey(hotkeyToggle)
	pumpFor(500)
	tucked := captureScreen(strip)
	tucked.savePNG(filepath.Join(outDir, "screen-tucked.png"))
	if a.line.Reveal.X > 0.05 {
		fail(fmt.Sprintf("the line did not tuck away: %.3f", a.line.Reveal.X))
	}

	// 4. Compare, point by point, away from the edges where a one-pixel offset
	//    between the capture and the composition would show up as a mismatch.
	//
	//    Screen captures have no alpha channel, so both sides are read as plain
	//    colour: `before` and `after` are the desktop, and `composed` is what the
	//    app believes it put on top of it.
	//
	//    The opaque branch never consults the desktop, so it needs no allowance and
	//    keeps its full strength: whatever the desktop was doing, a covered pixel
	//    must show exactly what the app composed. The blended branch does consult the
	//    desktop, so it uses the interval test described below instead of a tolerance.
	worst := 0
	worstAt := ""
	// Mismatches are reported as a coarse grid, so a failure says where on the
	// strip it happened instead of only how bad the worst pixel was.
	var gridO, gridC [gxGrid * gyGrid]int
	for y := margin; y < after.H-margin; y += step {
		for x := margin; x < after.W-margin; x += step {
			if x >= composed.W || y >= composed.H {
				continue
			}
			ca := composed.alphaAt(x, y)

			if ca >= opaqueAlpha {
				// Fully covered: the screen must be the composed pixels exactly. This
				// needs no desktop at all.
				ar, ag, ab := after.rgbAt(x, y)
				cr, cg, cb := composed.rgbAt(x, y)
				d := max3(abs8(ar, cr), abs8(ag, cg), abs8(ab, cb))
				rep.OpaqueN++
				if d <= opaqueTol {
					rep.OpaqueOK++
				} else {
					gridO[y*gyGrid/after.H*gxGrid+x*gxGrid/after.W]++
					if d > worst {
						worst, worstAt = d, fmt.Sprintf("opaque (%d,%d) screen=%d,%d,%d composed=%d,%d,%d", x, y, ar, ag, ab, cr, cg, cb)
					}
				}
				continue
			}

			// Partly covered: the composed pixel is blended with whatever the desktop
			// had there. The desktop value is not known exactly — it is read at
			// whatever moment the capture happened — so the prediction is checked
			// against an interval rather than a number.
			//
			// composed.colorAt un-premultiplies, and that matters: the buffer is
			// premultiplied BGRA, so a soft shadow at alpha 8 reads back as colour 8,
			// not 247. Blending that raw value over the desktop would predict a
			// near-black edge and blame the compositor for arithmetic that is really
			// the test's own.
			cr, cg, cb, _ := composed.colorAt(x, y)
			ar, ag, ab := after.rgbAt(x, y)
			// The two desktop readings that bracket the app's visit: how it looked
			// just before the line came down, and how it looks now the line is gone.
			br, bg, bb := before.rgbAt(x, y)
			tr, tg, tb := tucked.rgbAt(x, y)
			// The screen must equal the composition blended over *some* desktop value
			// in the range the desktop was seen to occupy. Solving the blend for the
			// desktop value that would explain this pixel, then asking whether that
			// value lies within the observed range, tests the alpha channel without
			// needing to know exactly what the desktop was showing.
			lo, hi := blendedBounds(cr, cg, cb, int(ca), br, bg, bb, tr, tg, tb)
			ok := int(ar) >= int(lo.r) && int(ar) <= int(hi.r) &&
				int(ag) >= int(lo.g) && int(ag) <= int(hi.g) &&
				int(ab) >= int(lo.b) && int(ab) <= int(hi.b)
			// A pixel whose own desktop flickered between two captures taken moments
			// apart, with the app absent, is furniture that is moving. Whatever it
			// shows during the app interval may be that furniture rather than the
			// desk, so it is not evidence about the window and is left out. The
			// usable total is asserted below, so a strip that is mostly furniture
			// cannot satisfy this check by having nothing left to test.
			if flicker[y*before.W+x] {
				continue
			}
			rep.ClearN++
			if ok {
				rep.ClearOK++
			} else {
				gridC[y*gyGrid/after.H*gxGrid+x*gxGrid/after.W]++
				// How far outside the interval the screen fell, which is the honest
				// measure of how wrong this sample is.
				d := 0
				for _, p := range [][3]int{{int(ar), int(lo.r), int(hi.r)}, {int(ag), int(lo.g), int(hi.g)}, {int(ab), int(lo.b), int(hi.b)}} {
					v, l, h := p[0], p[1], p[2]
					if v < l {
						if l-v > d {
							d = l - v
						}
					} else if v > h {
						if v-h > d {
							d = v - h
						}
					}
				}
				if d > worst {
					worst, worstAt = d, fmt.Sprintf("blend (%d,%d) alpha=%d screen=%d,%d,%d outside [%d..%d,%d..%d,%d..%d] (composed=%d,%d,%d)",
						x, y, ca, ar, ag, ab, lo.r, hi.r, lo.g, hi.g, lo.b, hi.b, cr, cg, cb)
				}
			}
		}
	}
	rep.MaxDelta, rep.WorstAt = worst, worstAt
	rep.Notes["opaque"] = fmt.Sprintf("%d/%d exact", rep.OpaqueOK, rep.OpaqueN)
	rep.Notes["blended"] = fmt.Sprintf("%d/%d inside the desktop's observed range", rep.ClearOK, rep.ClearN)
	if rep.OpaqueOK != rep.OpaqueN {
		rep.Notes["opaqueMismatchGrid"] = gridString(gridO, gxGrid, gyGrid, after.W, after.H)
	}
	if rep.ClearOK != rep.ClearN {
		rep.Notes["blendMismatchGrid"] = gridString(gridC, gxGrid, gyGrid, after.W, after.H)
	}

	// Every opaque sample must match exactly, and every blended sample must be
	// explained by some desktop value the desktop was actually seen to take. Both are
	// all-or-nothing: there is no residual slack to spend, so any mismatch means a
	// pixel reached the screen by a route this test cannot account for.
	if rep.OpaqueN < 40 {
		fail(fmt.Sprintf("only %d opaque samples were found; the card did not reach the screen", rep.OpaqueN))
	} else if rep.OpaqueOK != rep.OpaqueN {
		fail(fmt.Sprintf("only %d of %d opaque samples reached the screen unchanged (worst delta %d: %s)",
			rep.OpaqueOK, rep.OpaqueN, worst, worstAt))
	}
	if rep.ClearN < 200 {
		fail(fmt.Sprintf("only %d blended samples were found; the strip is not mostly empty", rep.ClearN))
	} else if rep.ClearOK != rep.ClearN {
		fail(fmt.Sprintf("only %d of %d blended samples match the composition (worst delta %d: %s)",
			rep.ClearOK, rep.ClearN, worst, worstAt))
	}
	// A desktop restless enough to exclude the great majority of the strip would
	// make the blended check vacuous, so the fraction left usable is itself checked.
	if rep.ClearN*4 < (after.W/step)*(after.H/step)*3 {
		fail(fmt.Sprintf("only %d blended samples remained usable out of about %d; this desktop moved too much to draw a conclusion",
			rep.ClearN, (after.W/step)*(after.H/step)))
	}

	// 5. The screen after the line has gone must show nothing of it.
	//
	//    Comparing against the desktop is fragile here in a way the checks above are
	//    not: the desktop fades and repaints on its own over exactly the seconds the
	//    line was up, so "the screen equals the desktop as it was before" is a claim
	//    the desktop itself can violate. This test found as much — a slow fade moved
	//    tens of thousands of pixels on a desktop with the app absent.
	//
	//    So the leftover check does not rely on the desktop at all. It asks a
	//    question whose answer is independent of what the desktop was doing: is the
	//    screen still showing the app's last frame? Every pixel the composition
	//    covered is examined, and if the screen there matches the *composition* far
	//    better than it can match a desktop that has since moved on, the window is
	//    still up. A pixel the composition left transparent is not evidence either
	//    way and is skipped.
	//
	//    A leftover frame is by definition large, so this is a whole-strip comparison
	//    rather than a sampled one: a thin edge could be stepped over. No pixel is
	//    excluded here. The comparison never consults the desktop as a *reference*,
	//    only as a rival explanation, so a restless desktop cannot invalidate it — it
	//    can only make the rival explanation stronger, which is the safe direction.
	leftover := 0
	leftoverTotal := 0
	loMinX, loMinY, loMaxX, loMaxY := after.W, after.H, -1, -1
	var loGrid [gxGrid * gyGrid]int
	for _, ref := range opaqueRefs {
		x, y := ref.x, ref.y
		tr, tg, tb := tucked.rgbAt(x, y)
		br, bg, bb := before.rgbAt(x, y)
		// Distance from the app's last frame, and from the desktop as it was.
		toComposed := max3(abs8(tr, ref.r), abs8(tg, ref.g), abs8(tb, ref.b))
		toBefore := max3(abs8(tr, br), abs8(tg, bg), abs8(tb, bb))
		leftoverTotal++
		// Still showing the app: much closer to its last frame than to the
		// desktop, and not merely within tolerance of both.
		if toComposed <= matchTol+2 && toBefore > toComposed+matchTol+2 {
			leftover++
			loGrid[y*gyGrid/after.H*gxGrid+x*gxGrid/after.W]++
			if x < loMinX {
				loMinX = x
			}
			if y < loMinY {
				loMinY = y
			}
			if x > loMaxX {
				loMaxX = x
			}
			if y > loMaxY {
				loMaxY = y
			}
		}
	}
	rep.Notes["leftBehind"] = fmt.Sprintf("%d of %d opaque pixels still show the line's last frame after tucking it away", leftover, leftoverTotal)
	if leftoverTotal < 1000 {
		fail(fmt.Sprintf("only %d opaque pixels were found to test the tuck with; the card did not reach the screen", leftoverTotal))
	}
	if leftover > 0 {
		rep.Notes["leftBehindBox"] = fmt.Sprintf("x %d..%d, y %d..%d", loMinX, loMaxX, loMinY, loMaxY)
		rep.Notes["leftBehindGrid"] = gridString(loGrid, gxGrid, gyGrid, after.W, after.H)
	}
	rep.ChangedOK = leftoverTotal - leftover
	rep.Notes["restoredAfterTuck"] = fmt.Sprintf("%d of %d opaque pixels gave the desk back", leftoverTotal-leftover, leftoverTotal)

	// What the operating system says is on screen, which is the only authority on
	// whether the app has really gone away. A pixel diff cannot settle it: a window
	// left up that happens to draw background-coloured pixels is invisible to the
	// diff while still intercepting clicks across the whole strip.
	wins := ownWindows()
	rep.Notes["ownWindows"] = strings.Join(wins, " | ")
	for _, w := range wins {
		if strings.HasPrefix(w, "VISIBLE") {
			fail("a window of this process is still visible after tucking the line away: " + w)
		}
	}
	// And the point the line occupied must now belong to somebody else: whatever was
	// underneath before, or the desktop itself.
	if hwnd, _ := windowAtPoint(int(a.panel.stripRect.X)+60, int(a.panel.stripRect.Y)+60); windowPid(hwnd) == getCurrentProcessID() {
		fail(fmt.Sprintf("the strip still belongs to this process when asked what is under it: %#x", hwnd))
	}

	// The threshold is zero: this comparison never consults the desktop, so there is
	// no desktop noise for it to tolerate. A pixel counts only when it matches the
	// app's last frame closely and the desktop poorly, which a hidden window cannot
	// produce. The OS's own answer about visible windows is checked separately and
	// independently just below.
	if leftover > 0 {
		fail(fmt.Sprintf("%d opaque pixels still show the line's last frame after tucking it away (%s); the window did not give the screen back",
			leftover, rep.Notes["leftBehindBox"]))
	}

	reportLive(rep)
}

// channelRange is a per-channel lower and upper bound.
type channelRange struct{ r, g, b int }

// blendedBounds answers: if the app composited colour c at alpha a over some
// desktop colour, what range of desktop colours could produce each channel of the
// screen? Inverting the blend gives the desktop value that would explain an
// observed channel exactly:
//
//	v = (screen - a*c) / (1 - a)
//
// At alpha 0 the app contributed nothing, so the screen must simply be the desktop
// itself and the bound is the desktop's own observed range. At full alpha this
// branch is never reached, because the caller compares those pixels exactly.
//
// The desktop's observed range comes from the two captures that bracket the app
// interval — one before the line appeared, one after it left. Using the observed
// range rather than a rate of change means no assumption about how the desktop
// animates is needed, which is the whole point: the desktop here fades on its own
// over the very seconds the app is on screen.
//
// The tolerance is carried through the inversion: a one-level error in the screen
// becomes a larger error in the recovered desktop value as alpha approaches 1, so
// the bound is widened by the screen tolerance divided by (1 - a).
func blendedBounds(cr, cg, cb byte, ca int, br, bg, bb byte, tr, tg, tb byte) (lo, hi channelRange) {
	dl, dh := br, br
	if tr < dl {
		dl = tr
	}
	if tr > dh {
		dh = tr
	}
	gl, gh := bg, bg
	if tg < gl {
		gl = tg
	}
	if tg > gh {
		gh = tg
	}
	bl, bh := bb, bb
	if tb < bl {
		bl = tb
	}
	if tb > bh {
		bh = tb
	}
	if ca == 0 {
		return channelRange{int(dl), int(gl), int(bl)}, channelRange{int(dh), int(gh), int(bh)}
	}
	a := float64(ca) / 255
	if a >= 1 {
		return channelRange{0, 0, 0}, channelRange{255, 255, 255}
	}
	// A screen value s is consistent with desktop value v where
	//   s = round(a*c + (1-a)*v)
	// so for each channel the screen must lie between the two endpoints of the
	// desktop's range. Slack is the tolerance widened by the inversion.
	slack := float64(matchTol+2) / (1 - a)
	bnd := func(c byte, low, high byte) (int, int) {
		v1 := a*float64(c) + (1-a)*float64(low)
		v2 := a*float64(c) + (1-a)*float64(high)
		if v1 > v2 {
			v1, v2 = v2, v1
		}
		return int(v1 - slack - 0.5), int(v2 + slack + 0.5)
	}
	lr, hr := bnd(cr, dl, dh)
	lg, hg := bnd(cg, gl, gh)
	lb, hb := bnd(cb, bl, bh)
	return channelRange{lr, lg, lb}, channelRange{hr, hg, hb}
}

// waitPumping waits for a duration while still running the message loop, so the
// app keeps drawing. Sleeping outright would freeze the very animation the wait is
// meant to span, and the desktop underneath would be sampled mid-change.
func waitPumping(ms int) {
	const slice = 40
	for waited := 0; waited < ms; waited += slice {
		n := slice
		if ms-waited < n {
			n = ms - waited
		}
		pumpFor(n)
	}
}

// reportLive prints the report as a failure count, the notes it gathered, the
// worst sample found and every failure message. The notes are printed even when
// everything passes, because they are the only record of what the screen actually
// looked like.
func reportLive(rep *liveReport) {
	fmt.Printf("windline livetest — %d failures\n", len(rep.Failures))
	for k, v := range rep.Notes {
		fmt.Printf("  %-20s %v\n", k, v)
	}
	if rep.WorstAt != "" {
		fmt.Printf("  %-20s %s\n", "worst sample", rep.WorstAt)
	}
	for _, f := range rep.Failures {
		fmt.Printf("  FAIL  %s\n", f)
	}
	fmt.Printf("artifacts in %s\n", rep.OutDir)
}

// pumpFor runs the message loop for a while, so the app's own timer drives its own
// animation. This is what makes the test exercise the shipping code path rather
// than a reimplementation of it.
//
// It never blocks: PeekMessage is asked for a message that is already there, and
// the wait between passes is owned by this loop rather than by GetMessage. A modal
// dialog or a stuck window procedure would still be caught by the watchdog.
func pumpFor(ms int) {
	end := tickCount() + uint64(ms)
	var msg MSG
	for tickCount() < end {
		for {
			if call(pPeekMessage, uptr(&msg), 0, 0, 0, pmRemove) == 0 {
				break
			}
			if msg.Message == wmQuit {
				return
			}
			if msg.Message == wmRunOnUI {
				drainUIFuncs()
				continue
			}
			if msg.Message == wmNotifyIcon {
				if panelFor != nil && panelFor.app != nil {
					panelFor.app.onTrayMessage(msg.LParam)
				}
				continue
			}
			call(pTranslateMessage, uptr(&msg))
			call(pDispatchMessage, uptr(&msg))
		}
		sleepMS(4)
	}
}

const (
	pmRemove = 1

	// The sampling spacing and edge margin for the screen comparisons: coarse
	// enough to cover the strip quickly, fine enough to catch a wrong card. The
	// margin keeps a one-pixel offset between the capture and the composition from
	// reading as a mismatch.
	step      = 6
	margin    = 3
	matchTol  = 2
	opaqueTol = 4

	// A pixel counts as fully covered only at alpha 255. Anything less is blended
	// with the desktop by design, and treating alpha 250 as "opaque" would compare
	// the screen against an unblended colour that was never supposed to appear —
	// producing a uniform four-to-five level error on every channel that looks like
	// a compositing fault and is really the test's own arithmetic.
	opaqueAlpha = 255

	// The mismatch grid the comparison reports against: coarse enough that one cell
	// covers a meaningful part of the strip, fine enough to point at a region.
	gxGrid = 12
	gyGrid = 3
)

func abs8(a, b byte) int {
	if a > b {
		return int(a) - int(b)
	}
	return int(b) - int(a)
}

func max3(a, b, c int) int { return max(a, max(b, c)) }

// captureScreen reads a rectangle of the desktop into a new surface. It is the
// only way to see what actually reached the screen, and the alpha bytes come back
// as zero because the desktop has no alpha channel, so they are forced opaque for
// anything that inspects the result.
func captureScreen(r Rect) *surface {
	w := max(1, int(math.Round(r.W)))
	h := max(1, int(math.Round(r.H)))
	s := newSurface(w, h)
	captureInto(s, int(math.Round(r.X)), int(math.Round(r.Y)))
	s.flush()
	s.setOpaque()
	return s
}

// gridString renders a mismatch histogram as rows of digits, so a failure report
// says where on the screen the problem is. Digits, because a count above nine in
// one cell is itself the signal.
func gridString(counts [gxGrid * gyGrid]int, gx, gy, w, h int) string {
	var b []byte
	b = append(b, fmt.Sprintf("grid %dx%d over %dx%d, cell ~%dx%d px\n", gx, gy, w, h, w/gx, h/gy)...)
	for r := 0; r < gy; r++ {
		b = append(b, "  "...)
		for c := 0; c < gx; c++ {
			n := counts[r*gx+c]
			switch {
			case n == 0:
				b = append(b, '.')
			case n < 10:
				b = append(b, byte('0'+n))
			default:
				b = append(b, '#')
			}
		}
		b = append(b, '\n')
	}
	return string(b)
}

// moveConsoleOutOfTheWay hides the console window this test was launched from for
// the duration, and returns a function that brings it back.
//
// The test measures the top of the screen, and a console sitting there would be
// photographed instead of the line. Minimising rather than moving it is deliberate:
// a maximised console keeps its maximised size through a move, so repositioning one
// leaves it covering the whole screen — which is worse than doing nothing, because
// a maximised window over the monitor is indistinguishable from a full-screen app
// and the app would then correctly refuse to show its line at all.
//
// It is found through the process's own console attachment rather than by guessing
// a class name, because the enclosing window differs per host (conhost, Windows
// Terminal, an editor's panel) and a window that is not ours to move is left alone.
func moveConsoleOutOfTheWay() (restore func(), hidden bool) {
	hwnd := getConsoleWindow()
	if hwnd == 0 {
		return func() {}, false
	}
	const swMinimize, swRestore = 6, 9
	call(pShowWindow, hwnd, swMinimize)
	return func() { call(pShowWindow, hwnd, swRestore) }, true
}
func clearSavedLine(set *settings) {
	set.setList(keyLineItems, nil)
	set.save()
}

// configureForLiveTest turns off the two features that would otherwise change the
// line while the test is watching it, and returns a function that puts the user's
// real preferences back.
func configureForLiveTest(set *settings) func() {
	prevOpen, prevPeek := set.opensFromTop(), set.peeksAtNew()
	set.setOpensFromTop(false)
	set.setPeeksAtNew(false)
	set.save()
	return func() {
		set.setOpensFromTop(prevOpen)
		set.setPeeksAtNew(prevPeek)
		set.save()
	}
}
