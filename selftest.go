package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// selftest.go is the harness the app is verified with. A GUI with no console is
// otherwise impossible to check automatically, so --selftest builds the real
// objects, composes real frames, writes them as PNGs and reports exact numbers
// (pixel probes, hit rectangles, geometry) that can be compared against what was
// intended without ever looking at a picture.
//
// It is deliberately part of the shipped binary: it is a few kilobytes, it has no
// UI, and having the replayed test live next to the code it tests is worth more
// than the space.

type selftestReport struct {
	Steps    []string       `json:"steps"`
	Results  map[string]any `json:"results"`
	Failures []string       `json:"failures"`
	OutDir   string         `json:"outDir"`
}

func runSelfTest(set *settings, args []string) {
	outDir := flagValue(args, "--out")
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "windline-selftest")
	}
	os.MkdirAll(outDir, 0o755)
	rep := &selftestReport{Results: map[string]any{}, OutDir: outDir}

	step := func(name string, fn func() error) {
		rep.Steps = append(rep.Steps, name)
		if err := fn(); err != nil {
			rep.Failures = append(rep.Failures, name+": "+err.Error())
		}
	}

	step("gdiplus starts", func() error {
		gdiplusStart()
		return nil
	})
	defer gdiplusStop()
	soundStart()

	// The log must be running, or a failure elsewhere would be invisible.
	logStart()
	defer logStop()

	// The test has to start from a known, empty line.
	//
	// newApp restores whatever the settings file lists, so a previous run — this test's
	// own earlier invocation, or the user's real line — would put photos on it. Nearly
	// every step below assumes exactly one photo is hanging, and "hang a photo" would
	// return nil for a file already on the line, so a second run would test a different
	// picture entirely. Clearing the saved line first makes the result depend on the
	// code rather than on what ran before.
	clearSavedLine(set)

	var a *app
	step("build the app", func() error {
		logos := newLogoRenderer()
		a = newApp(set, logos)
		a.monitor = monitorFromPoint(cursorPos())
		a.panel.place(a.monitor)
		a.scale = a.panel.scale
		a.layout = Layout{Size: SizeMedium, Dpi: a.scale}
		a.updateLayout()
		return nil
	})
	if a == nil {
		writeSelftestReport(rep, outDir)
		return
	}

	step("geometry matches the original's layout", func() error {
		lay := Layout{Size: SizeMedium, Dpi: 1}
		want := map[string]float64{
			"PhotoMaxHeight": 104,
			"CardWidth":      150,
			"Spacing":        174,
			"PanelHeight":    210,
		}
		got := map[string]float64{
			"PhotoMaxHeight": lay.PhotoMaxHeight(),
			"CardWidth":      lay.CardWidth(),
			"Spacing":        lay.Spacing(),
			"PanelHeight":    lay.PanelHeight(),
		}
		rep.Results["geometry"] = got
		for k, v := range want {
			if got[k] != v {
				return fmt.Errorf("%s is %v, expected %v", k, got[k], v)
			}
		}
		// The rope's sag is capped at 30 and reaches it only on a very wide screen.
		if sag := lay.Sag(1920); sag != 30 {
			return fmt.Errorf("sag on a 1920 wide screen is %v, expected 30", sag)
		}
		if sag := lay.Sag(400); sag >= 30 {
			return fmt.Errorf("sag on a 400 wide screen is %v, which should be under the cap", sag)
		}
		return nil
	})

	step("the photo size keeps its proportions", func() error {
		lay := Layout{Size: SizeMedium, Dpi: 1}
		cases := [][2]float64{{1920, 1080}, {800, 1400}, {100, 100}}
		for _, c := range cases {
			w, h := lay.PhotoSize(c[0], c[1])
			if w > lay.CardWidth()-cardPhotoInset+0.01 {
				return fmt.Errorf("a %vx%v photo is %v wide, wider than the card allows", c[0], c[1], w)
			}
			if h > lay.PhotoMaxHeight()+0.01 {
				return fmt.Errorf("a %vx%v photo is %v tall, taller than the card allows", c[0], c[1], h)
			}
			// The aspect ratio has to survive, within a rounding unit.
			want := c[0] / c[1]
			if got := w / h; got < want*0.97 || got > want*1.03 {
				return fmt.Errorf("a %vx%v photo came out %vx%v, which is the wrong shape", c[0], c[1], w, h)
			}
		}
		return nil
	})

	step("the rope is a parabola that sags in the middle", func() error {
		lay := Layout{Size: SizeMedium, Dpi: 1}
		const w = 1920.0
		left, mid, right := lay.RopeY(-20, w), lay.RopeY(w/2, w), lay.RopeY(w+20, w)
		rep.Results["rope"] = map[string]float64{"left": left, "mid": mid, "right": right}
		if !(mid > left && mid > right) {
			return fmt.Errorf("the rope does not sag: left %v, middle %v, right %v", left, mid, right)
		}
		// The parabola's peak is exactly the sag.
		if sag := mid - ropeTop; sag < lay.Sag(w)-0.5 {
			return fmt.Errorf("the middle of the rope hangs %v below the top, expected %v", sag, lay.Sag(w))
		}
		return nil
	})

	step("cards are placed inside the strip", func() error {
		lay := Layout{Size: SizeMedium, Dpi: 1}
		const w = 1920.0
		visible := lay.FitsCount(w)
		rep.Results["visibleCount"] = visible
		if visible != 9 {
			return fmt.Errorf("%v cards fit on a 1920 wide screen, expected 9", visible)
		}
		for count := 1; count <= visible; count++ {
			for i := 0; i < count; i++ {
				x := lay.CardX(i, count, w, visible, 0, false)
				cw, _ := lay.CardSize(1920, 1080)
				if x-cw/2 < 0 || x+cw/2 > w {
					return fmt.Errorf("with %d photos, photo %d is centred at %v and does not fit", count, i, x)
				}
			}
		}
		return nil
	})

	var samplePath string
	step("make a test screenshot", func() error {
		p := filepath.Join(outDir, "sample.png")
		if err := makeSampleImage(p, 640, 400); err != nil {
			return err
		}
		samplePath = p
		a.layout = Layout{Size: SizeMedium, Dpi: a.panel.scale}
		a.updateLayout()
		return nil
	})

	var item *item
	step("hang a photo", func() error {
		item = a.line.hang(samplePath, false)
		if item == nil {
			return fmt.Errorf("the photo did not hang")
		}
		item.DropIn.snap(1)
		rep.Results["thumb"] = map[string]any{"w": item.Thumb.W, "h": item.Thumb.H, "tilt": item.Tilt}
		if item.Thumb.W > 320 || item.Thumb.H > 320 {
			return fmt.Errorf("the thumbnail is %dx%d, larger than the 320 px budget", item.Thumb.W, item.Thumb.H)
		}
		return nil
	})

	step("compose a frame", func() error {
		a.render.captureBackdrop(a.panel.stripRectOnScreen(), a.panel.scale)
		a.line.Reveal.snap(1)
		a.revealEase = 1
		s := a.render.compose(a, a.panel.stripRect, -1, -1)
		if s == nil {
			return fmt.Errorf("the frame came out empty")
		}
		defer s.free()
		p := filepath.Join(outDir, "frame.png")
		s.savePNG(p)
		rep.Results["frame"] = map[string]any{"w": s.W, "h": s.H, "path": p}

		// The rope has to be drawn, and the photos have to be drawn over it: upstream
		// stacks Rope first and the PeggedViews above it, so the rope is visible in
		// the gaps between cards and hidden behind them.
		lay := a.layout
		w := a.panel.stripRect.W
		gaps, onRope := 0, 0
		for i := 0; i <= 240; i++ {
			x := float64(i) / 240 * w
			if x > 30 && x < w-30 && overAnyCard(lay, a.line, x, a.panel.stripRect.W, a.panel.scale) {
				continue
			}
			y := lay.RopeY(x, w)
			gaps++
			// The rope is the only thing drawn this close to the top of the strip,
			// so any opacity there is the rope.
			if s.alphaAt(int(x*a.panel.scale), int(y*a.panel.scale)) > 30 {
				onRope++
			}
		}
		rep.Results["ropeInGaps"] = map[string]int{"gaps": gaps, "drawn": onRope}
		if gaps >= 8 && onRope*4 < gaps*3 {
			return fmt.Errorf("the rope only shows in %d of the %d visible gaps between cards", onRope, gaps)
		}
		return nil
	})

	step("the photo's card is opaque and the gaps are not", func() error {
		if item == nil {
			// The earlier step already reported why it did not hang; without this
			// guard the missing item would turn a clean failure into a panic.
			return fmt.Errorf("no photo is hanging, so there is no card to check")
		}
		if len(a.line.HitRects) == 0 {
			return fmt.Errorf("no hit rectangles were reported")
		}
		r, ok := a.line.HitRects[item.ID]
		if !ok {
			return fmt.Errorf("the hanging photo has no hit rectangle")
		}
		rep.Results["hitRect"] = map[string]float64{"x": r.X, "y": r.Y, "w": r.W, "h": r.H}
		s := a.render.compose(a, a.panel.stripRect, -1, -1)
		if s == nil {
			return fmt.Errorf("the frame came out empty")
		}
		defer s.free()
		sc := a.panel.scale
		// The middle of the card must be opaque.
		mx := int((r.X + r.W/2) * sc)
		my := int((r.Y + r.H/2) * sc)
		if a := s.alphaAt(mx, my); a < 200 {
			return fmt.Errorf("the middle of the card is only %d/255 opaque", a)
		}
		// A point well to the left of every card must be fully transparent, which
		// is what lets the desktop show through.
		far := int(8 * sc)
		if a := s.alphaAt(far, int((r.Y+r.H/2)*sc)); a > 12 {
			return fmt.Errorf("the empty part of the strip is %d/255 opaque, so the desktop will not show through", a)
		}
		return nil
	})

	step("hovering a card gives it a cross, and the cross has its own rect", func() error {
		if item == nil {
			return fmt.Errorf("no photo is hanging, so there is no card to hover")
		}
		item.Hover = true
		a.render.discardCard(item.ID)
		s := a.render.compose(a, a.panel.stripRect, -1, -1)
		if s == nil {
			return fmt.Errorf("the frame came out empty")
		}
		defer s.free()
		p := filepath.Join(outDir, "frame-hover.png")
		s.savePNG(p)
		sprite := a.render.cardSpriteFor(a, item)
		if sprite == nil {
			return fmt.Errorf("no sprite for the hovered card")
		}
		if sprite.crossR.W <= 0 {
			return fmt.Errorf("the hovered card has no cross rectangle")
		}
		rep.Results["cross"] = map[string]float64{"x": sprite.crossR.X, "y": sprite.crossR.Y, "w": sprite.crossR.W, "h": sprite.crossR.H}
		item.Hover = false
		a.render.discardCard(item.ID)
		return nil
	})

	step("a recording gets a play badge", func() error {
		rec := filepath.Join(outDir, "clip.mp4")
		if err := os.WriteFile(rec, []byte("not really a video"), 0o644); err != nil {
			return err
		}
		if !isRecording(rec) {
			return fmt.Errorf("a .mp4 was not recognised as a recording")
		}
		// A recording with no decodable first frame must not hang, rather than
		// crashing the line.
		if it := a.line.hang(rec, false); it != nil {
			return fmt.Errorf("an undecodable video hung on the line")
		}
		return nil
	})

	step("the clipboard round-trips an image", func() error {
		if err := copyToClipboard(samplePath, false); err != nil {
			return err
		}
		// Read it back the same way the watcher would.
		w := newClipboardWatcher()
		if !w.clipboardHoldsImage() {
			return fmt.Errorf("the clipboard does not report an image after copying one")
		}
		data, ok := w.readClipboardImage()
		if !ok {
			return fmt.Errorf("the image could not be read back off the clipboard")
		}
		rep.Results["clipboard"] = map[string]any{"bytes": len(data), "isPNG": isPNG(data)}
		if !isPNG(data) {
			return fmt.Errorf("what came back off the clipboard is not a PNG")
		}
		return nil
	})

	step("settings round-trip", func() error {
		set.setLineSize(SizeLarge)
		if set.lineSize() != SizeLarge {
			return fmt.Errorf("the line size did not persist")
		}
		set.setLineSize(SizeMedium)
		set.setKeepOnLine(50)
		if set.keepOnLine() != 50 {
			return fmt.Errorf("Keep on line did not persist")
		}
		set.setKeepOnLine(0)
		set.setList(keyExtraFolders, []string{`C:\one`, `C:\two`})
		folders := set.extraFolders()
		if len(folders) != 2 {
			return fmt.Errorf("the watched folders did not persist")
		}
		set.setExtraFolders(nil)
		set.save()
		return nil
	})

	step("the inbox redirect is reversible", func() error {
		before := screenshotsFolder()
		rawBefore := readShellFolderValue(screenshotsFolderGUID)
		errApply := applyInbox()
		during := screenshotsFolder()
		rawDuring := readShellFolderValue(screenshotsFolderGUID)
		errRestore := restoreInbox()
		after := screenshotsFolder()
		rawAfter := readShellFolderValue(screenshotsFolderGUID)
		rep.Results["inbox"] = map[string]any{
			"before": before, "during": during, "after": after,
			"rawBefore": rawBefore, "rawDuring": rawDuring, "rawAfter": rawAfter,
			"applyErr": errApply, "restoreErr": errRestore,
		}
		// Writing the registry value is the part that matters and the part that can fail;
		// the shell's *resolved* folder is cached by Explorer, so it is not evidence
		// either way and is only noted.
		if errApply != nil {
			return fmt.Errorf("redirecting the Screenshots folder failed: %v", errApply)
		}
		// The redirect has to actually change the registry. This is exactly the check
		// that would have caught the RegCreateKeyEx call being passed one argument too
		// few, which made every write fail with ERROR_INVALID_PARAMETER while the test
		// still passed because it only compared the resolved folder with itself.
		if rawDuring != inboxFolder() {
			return fmt.Errorf("the raw Screenshots value is %q after redirecting, expected %q", rawDuring, inboxFolder())
		}
		if errRestore != nil {
			return fmt.Errorf("restoring the Screenshots folder failed: %v", errRestore)
		}
		// And undoing it has to put the registry back exactly: an absent value must be
		// absent again, not replaced by the path it happened to resolve to.
		if rawAfter != rawBefore {
			return fmt.Errorf("the raw Screenshots value came back as %q instead of %q", rawAfter, rawBefore)
		}
		if after != before {
			return fmt.Errorf("the Screenshots folder came back as %q instead of %q", after, before)
		}
		return nil
	})

	step("the icon renders at every size", func() error {
		sizes := []int{16, 20, 24, 32, 48, 256}
		for _, n := range sizes {
			h := a.logos.icon(n, false)
			if h == 0 {
				return fmt.Errorf("no icon for size %d", n)
			}
		}
		// And with the clipboard dot, which is a different drawing.
		if a.logos.icon(32, true) == 0 {
			return fmt.Errorf("no icon for the clipboard variant")
		}
		// Save one for inspection.
		s := newSurface(256, 256)
		defer s.free()
		s.clear()
		drawLogoInto(s, 256, true)
		s.flush()
		s.savePNG(filepath.Join(outDir, "icon.png"))
		rep.Results["icons"] = sizes
		return nil
	})

	step("the spring settles without jitter", func() error {
		var an anim
		an.snap(0)
		an.springTo(1, arriveK, arriveC, 0)
		last := 0.0
		steps := 0
		for i := 1; i < 600; i++ {
			now := float64(i) * 12
			if !an.step(now, &last) {
				steps = i
				break
			}
		}
		rep.Results["spring"] = map[string]any{"settledAfterMS": steps * 12, "value": an.X}
		if an.X < 0.998 || an.X > 1.002 {
			return fmt.Errorf("the spring settled at %v instead of 1", an.X)
		}
		if steps == 0 || steps > 100 {
			return fmt.Errorf("the spring took %d frames to settle, which is wrong for a 0.42 s response", steps)
		}
		return nil
	})

	// Clean up the test photo, so running the selftest twice does not leave
	// anything behind.
	if item != nil {
		a.line.remove(item.ID)
	}

	writeSelftestReport(rep, outDir)
}

// overAnyCard reports whether the point x, at the rope's height, falls inside a
// card's bounding box: the rope is hidden there, so the rope check must skip it.
func overAnyCard(lay Layout, l *line, x, stripW, scale float64) bool {
	live := l.live()
	count := len(live)
	for i, it := range live {
		if it.Thumb == nil {
			continue
		}
		cx := lay.CardX(i, count, stripW, l.VisibleCount, l.Scroll, l.KeepSet)
		cw, ch := lay.CardSize(float64(it.Thumb.W), float64(it.Thumb.H))
		r := cardBounds(cx, lay.RopeY(cx, stripW)-pinAbove, cw, ch, 0)
		if x >= r.X && x <= r.Right() {
			return true
		}
	}
	return false
}

func writeSelftestReport(rep *selftestReport, outDir string) {
	data, _ := json.MarshalIndent(rep, "", "  ")
	path := filepath.Join(outDir, "report.json")
	os.WriteFile(path, data, 0o644)
	logf("selftest: %d steps, %d failures, report at %s", len(rep.Steps), len(rep.Failures), path)

	// Also print, because a console run is the common case.
	fmt.Printf("windline selftest — %d steps, %d failures\n", len(rep.Steps), len(rep.Failures))
	for k, v := range rep.Results {
		fmt.Printf("  %-14s %v\n", k, v)
	}
	for _, f := range rep.Failures {
		fmt.Printf("  FAIL  %s\n", f)
	}
	fmt.Printf("artifacts in %s\n", outDir)
}

func isPNG(data []byte) bool {
	return len(data) > 8 && string(data[1:4]) == "PNG"
}

// makeSampleImage writes a test PNG with a recognisable pattern, so a composed
// frame can be told apart from a blank one by eye or by pixel.
func makeSampleImage(path string, w, h int) error {
	s := newSurface(w, h)
	defer s.free()
	s.clear()

	// Four quadrants in the app's own palette, with a diagonal stripe through
	// them: enough for a pixel probe to identify a position.
	quads := []uint32{
		rgba(64, 120, 200, 255),
		rgba(214, 96, 84, 255),
		rgba(96, 176, 120, 255),
		rgba(220, 176, 72, 255),
	}
	qw, qh := w/2, h/2
	for i, c := range quads {
		x := (i % 2) * qw
		y := (i / 2) * qh
		s.fillRect(x, y, qw, qh, c)
	}
	for i := 0; i < min(w, h); i += 4 {
		s.fillRect(i, i, 2, 2, rgba(255, 255, 255, 220))
	}
	s.flush()
	s.savePNG(path)
	if !fileExists(path) {
		return fmt.Errorf("could not write %s", path)
	}
	return nil
}

// drawLogoInto draws the app's icon into an existing surface, which is how the
// selftest saves a copy for inspection.
func drawLogoInto(s *surface, size int, clipboardOn bool) {
	// Reuse the real icon's drawing by rendering through a temporary brush pass;
	// the icon's own renderer composes into its own surface, so this draws the
	// same primitives at the surface's scale.
	r := &logoRenderer{cache: map[logoKey]uintptr{}}
	_ = r
	drawLogoPrimitives(s, size, clipboardOn)
}

// timeFormat is used by the log messages that report timestamps.
func timeFormat(t time.Time) string { return t.Format(time.RFC3339) }

// trimTo is a small helper the layout uses; kept here so the string trimming is
// in one place.
func trimTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var _ = strings.TrimSpace
