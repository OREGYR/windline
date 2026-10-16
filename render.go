package main

import (
	"math"
	"sync"
)

// render.go draws the line. It is a port of the SwiftUI views in the original —
// LineView's Rope and Hint, PeggedView's photo, glass frame, clip and shadow,
// the play badge, the corner cross, the "Copied" caption — with one structural
// difference: SwiftUI composites a live view tree every frame, while this
// composes each card once into its own sprite and then draws that sprite at the
// card's current angle. A swing therefore costs one rotated blit instead of a
// rebuild of every gradient in the frame.
//
// The glass is real rather than faked: the pixels behind the strip are captured
// once per reveal and box-blurred, so a frame over a photo looks like frosted
// glass. That is the Windows answer to the original's .glassFrame and
// .regularMaterial.
//
// Everything is measured in logical units (the original's points) and multiplied
// by the monitor's scale factor, so a card is the same physical size at 100% and
// at 150%.

// Geometry in logical units, mirroring the original's Frame and Clip enums.
const (
	cardRadius       = 16.0
	photoRadius      = 12.0 // cardRadius - frameInset: the way macOS rounds nested shapes
	clipW            = 9.0
	clipH            = 26.0
	clipOverCard     = 12.0 // how much of the clip sits over the card
	clipGripRadius   = 3.5
	playBadgeD       = 28.0
	cardShadowRadius = 10.0
	cardShadowAlpha  = 0.18
	spritePad        = 6.0  // room in the sprite for the shadow's sides
	spritePadBottom  = 16.0 // and for the shadow below
)

// roundedCornerScale is the circle-to-bezier constant (kappa) the paths use.
const roundedCornerScale = 5523.0 / 10000

// browserStyle decides whether the glass leans light-outlined or dark-outlined.
// The original solves this with a material that adapts to the backdrop; the
// closest Windows signal is the app theme the user has chosen, which the shell
// exposes through the registry.
type browserStyle int

const (
	glassDark  browserStyle = iota // light text and highlights, for a dark backdrop
	glassLight                     // dark text and highlights, for a light backdrop
)

// renderer composes the strip and caches what it can.
type renderer struct {
	mu        sync.Mutex
	logos     *logoRenderer
	cardCache map[int64]*cardSprite
	style     browserStyle

	// The blurred screen behind the strip, captured when the line comes down.
	backdrop      *surface
	backdropX     int // monitor coordinates the backdrop was captured at
	backdropY     int
	backdropScale float64
	backdropOK    bool

	// fonts, built once
	fHint  *fontSpec
	fSmall *fontSpec
}

func newRenderer(logos *logoRenderer) *renderer {
	r := &renderer{cardCache: map[int64]*cardSprite{}}
	// Segoe UI is always present, and GDI+'s font linking brings in the CJK
	// fallback, so the hint and the caption read correctly in every language the
	// original ships.
	r.fHint = newFont("Segoe UI", -12, 600)
	r.fSmall = newFont("Segoe UI", -11, 600)
	return r
}

// cardSprite is one composited card: glass frame, photo, chrome, plus the margins
// the shadow and the clip need. It owns its surface, so freeing the sprite frees
// the pixels.
type cardSprite struct {
	surf    *surface
	W, H    int     // sprite size in device pixels
	padLeft float64 // the card's left edge inside the sprite, logical units
	padTop  float64 // the card's top edge inside the sprite, logical units
	cardW   float64
	cardH   float64
	crossR  Rect // the corner cross's hit area, in sprite pixels
	photoW  float64
	photoH  float64
	version int
}

func (c *cardSprite) free() {
	if c != nil && c.surf != nil {
		c.surf.free()
		c.surf = nil
	}
}

func (r *renderer) discardCard(id int64) {
	if c, ok := r.cardCache[id]; ok {
		c.free()
		delete(r.cardCache, id)
	}
}

func (r *renderer) discardAllCards() {
	for id, c := range r.cardCache {
		c.free()
		delete(r.cardCache, id)
	}
}

// invalidateBackdrop drops the cached blur, so the next frame recaptures the
// screen. Called when the line is about to come down, and when the desktop
// changes underneath it.
func (r *renderer) invalidateBackdrop() { r.backdropOK = false }

// ---------------------------------------------------------------- capture

// captureBackdrop grabs the screen region the strip will cover and blurs it.
// Doing this while the line is still tucked away means the capture contains the
// real desktop and not the line itself.
func (r *renderer) captureBackdrop(stripRect Rect, scale float64) {
	w := int(math.Ceil(stripRect.W * scale))
	h := int(math.Ceil(stripRect.H * scale))
	if w < 1 || h < 1 {
		return
	}
	x := int(math.Round(stripRect.X * scale))
	y := int(math.Round(stripRect.Y * scale))
	s := newSurface(w, h)
	captureInto(s, x, y)
	s.flush()

	// Two box-blur passes give a smooth, cheap Gaussian approximation: enough to
	// read as frosted glass at these radii.
	radius := max(4, int(math.Round(10*scale)))
	boxBlur(s.pixels(), w, h, radius)
	boxBlur(s.pixels(), w, h, radius/2+1)

	if r.backdrop != nil {
		r.backdrop.free()
	}
	r.backdrop = s
	r.backdropX = x
	r.backdropY = y
	r.backdropScale = scale
	r.backdropOK = true
}

// boxBlur blurs a premultiplied BGRA buffer in place with a separable sliding
// window. Blurring premultiplied samples is correct: it cannot pull colour out of
// transparent pixels.
func boxBlur(pix []byte, w, h, radius int) {
	if radius < 1 || w < 2 || h < 2 {
		return
	}
	tmp := make([]byte, len(pix))
	blurPass(pix, tmp, w, h, radius, true)
	blurPass(tmp, pix, w, h, radius, false)
}

// blurPass runs one direction of the box blur. horizontal selects the axis.
func blurPass(src, dst []byte, w, h, radius int, horizontal bool) {
	n := 4
	window := radius*2 + 1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sum [4]int
			count := 0
			for k := -radius; k <= radius; k++ {
				var sx, sy int
				if horizontal {
					sx, sy = x+k, y
				} else {
					sx, sy = x, y+k
				}
				if sx < 0 || sy < 0 || sx >= w || sy >= h {
					continue
				}
				i := (sy*w + sx) * n
				sum[0] += int(src[i])
				sum[1] += int(src[i+1])
				sum[2] += int(src[i+2])
				sum[3] += int(src[i+3])
				count++
			}
			if count == 0 {
				continue
			}
			o := (y*w + x) * n
			for c := 0; c < n; c++ {
				dst[o+c] = byte(sum[c] / count)
			}
		}
	}
	_ = window
}

// ---------------------------------------------------------------- the strip

// compose draws the whole strip into a fresh surface. stripRect is the panel's
// rectangle in monitor coordinates; mouseX/mouseY are the pointer's position in
// the same space, or negative when it is not over the line.
func (r *renderer) compose(app *app, stripRect Rect, mouseX, mouseY float64) *surface {
	r.mu.Lock()
	defer r.mu.Unlock()

	scale := app.scale
	w := int(math.Ceil(stripRect.W * scale))
	h := int(math.Ceil(stripRect.H * scale))
	if w < 1 || h < 1 {
		return nil
	}
	s := newSurface(w, h)
	s.clear()
	s.setQuality()

	l := app.line
	lay := app.layout
	// The layout works in logical units; the surface is in device pixels.
	stripW := float64(w) / scale

	// --- the rope
	r.drawRope(s, lay, stripW, scale)

	// --- the hint, when nothing is hanging
	if l.liveCount() == 0 {
		r.drawHint(s, lay, stripW, scale)
	}

	// --- the photos
	live := l.live()
	count := len(live)
	l.HitRects = make(map[int64]Rect, count)
	for i, it := range live {
		if it.Thumb == nil {
			continue
		}
		x := lay.CardX(i, count, stripW, l.VisibleCount, l.Scroll, l.KeepSet)
		ropeY := lay.RopeY(x, stripW)

		// The drop-in: a card arrives from 46 points above.
		var extraY float64
		if it.DropIn.X >= 0 {
			extraY = (it.DropIn.X - 1) * 46
		}
		// While the line is sliding down, cards further along lag behind, which
		// is what makes the arrival read as a wave.
		if app.revealEase < 1 {
			lag := math.Max(0.25, 1-float64(i)*0.06)
			extraY += (1 - app.revealEase) * 30 * lag
		}
		// The anchor is the view's top centre, which is where the whole hanging
		// view rotates from: the clip's grip on the rope.
		anchorX := x
		anchorY := ropeY - pinAbove + extraY
		angle := (it.Tilt + it.Swing.X) * math.Pi / 180

		// Photos scrolled towards an edge fade out before they reach it.
		alpha := math.Min(1, math.Max(0, math.Min(x, stripW-x)/90))
		if it.DropIn.X < 0 {
			alpha = 0
		}

		// Card geometry, in logical units, relative to the anchor. The card's
		// rectangle and angle are kept on the item as well, because the gesture
		// code needs them to test the corner cross and to place a drag.
		cw, ch := lay.CardSize(float64(it.Thumb.W), float64(it.Thumb.H))
		rect := cardBounds(anchorX, anchorY, cw, ch, angle)
		l.HitRects[it.ID] = rect
		it.CardRect = rect
		it.CardAngle = angle
		it.CardAlpha = alpha

		// A card whose rectangle is entirely off the strip is not drawn.
		if rect.X > stripRect.X+stripRect.W || rect.Right() < stripRect.X {
			continue
		}

		sprite := r.cardSpriteFor(app, it)
		if sprite == nil {
			continue
		}
		// The sprite's pivot is the same anchor: the view's top centre, which in
		// sprite-local logical units is (padLeft + cardW/2, padTop).
		s.drawRotatedAlpha(sprite.surf,
			(anchorX * scale), (anchorY * scale),
			float64(sprite.W), float64(sprite.H), angle,
			(sprite.padLeft+sprite.cardW/2)*scale, sprite.padTop*scale, alpha)
	}

	// --- a card that was just copied says so
	r.drawCopiedPill(s, lay, scale, l)

	s.flush()
	// The caller owns the surface from here; panel.push frees the previous one.
	return s
}

// cardBounds is the axis-aligned bounding box of a card rotated about its
// anchor, in strip coordinates. It is what the pointer must be inside for a
// click to reach the card, so it includes the clip above the card as well.
func cardBounds(anchorX, anchorY, cw, ch, angle float64) Rect {
	// The card's corners in the hanging view's frame, relative to the anchor. The
	// card starts cardOffsetBelowTop below the anchor, which is where the rope
	// grips it.
	o := float64(cardOffsetBelowTop)
	corners := [4][2]float64{
		{-cw / 2, o}, {cw / 2, o}, {cw / 2, o + ch}, {-cw / 2, o + ch},
	}
	sin, cos := math.Sin(angle), math.Cos(angle)
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, c := range corners {
		px := anchorX + c[0]*cos - c[1]*sin
		py := anchorY + c[0]*sin + c[1]*cos
		minX, minY = math.Min(minX, px), math.Min(minY, py)
		maxX, maxY = math.Max(maxX, px), math.Max(maxY, py)
	}
	// Include the clip, which pokes above the card and is part of the grab.
	minX = math.Min(minX, anchorX-clipW/2-2)
	maxX = math.Max(maxX, anchorX+clipW/2+2)
	minY = math.Min(minY, anchorY-2)
	return Rect{X: minX, Y: minY, W: maxX - minX, H: maxY - minY}
}

// drawSpriteAt is used by the flight overlay: it draws a card sprite at an
// arbitrary place on a big surface (the whole monitor) while a capture flies to
// the line, or a discarded card falls off it.
func (r *renderer) drawSpriteAt(dst *surface, sprite *cardSprite, anchorX, anchorY, angle, scale, alpha float64) {
	dst.drawRotatedAlpha(sprite.surf, anchorX*scale, anchorY*scale,
		float64(sprite.W), float64(sprite.H), angle,
		(sprite.padLeft+sprite.cardW/2)*scale, sprite.padTop*scale, alpha)
}

// spriteFor exposes a card's sprite to the flight overlay.
func (r *renderer) spriteFor(app *app, it *item) *cardSprite {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cardSpriteFor(app, it)
}

// cardSpriteFor returns the cached card, compositing it on first use or after
// its photo or the line's size changed.
func (r *renderer) cardSpriteFor(app *app, it *item) *cardSprite {
	if c, ok := r.cardCache[it.ID]; ok && c.version == it.Thumb.Version {
		return c
	}
	if old, ok := r.cardCache[it.ID]; ok {
		old.free()
		delete(r.cardCache, it.ID)
	}
	c := r.composeCard(app, it)
	if c == nil {
		return nil
	}
	r.cardCache[it.ID] = c
	return c
}

// composeCard builds one photo's sprite in device pixels: shadow, glass, photo,
// chrome. Everything that does not depend on the card's angle is baked here.
func (r *renderer) composeCard(app *app, it *item) *cardSprite {
	scale := app.scale
	lay := app.layout
	photoW, photoH := lay.PhotoSize(float64(it.Thumb.W), float64(it.Thumb.H))
	cardW := photoW + frameInset*2
	cardH := photoH + frameInset*2

	padTop := clipH - clipOverCard // room above the card for the part of the clip that shows
	W := int(math.Ceil((cardW + spritePad*2) * scale))
	H := int(math.Ceil((cardH + padTop + spritePadBottom) * scale))
	if W < 1 || H < 1 {
		return nil
	}

	s := newSurface(W, H)
	s.clear()
	s.setQuality()

	ix := func(v float64) int { return int(math.Round(v * scale)) }
	cardX := ix(spritePad)
	cardY := ix(padTop)
	cw := ix(cardW)
	chh := ix(cardH)
	rad := ix(cardRadius)

	// --- shadow: .shadow(color:radius:y:), tighter and darker on hover
	shadowAlpha, shadowRadius, shadowY := cardShadowAlpha, cardShadowRadius, 6.0
	if it.Hover {
		shadowAlpha, shadowRadius = 0.26, 14
	}
	drawSoftShadow(s, float64(cardX), float64(cardY), float64(cw), float64(chh),
		float64(rad), shadowRadius*scale, shadowAlpha, shadowY*scale)

	// --- the glass: the blurred desktop, clipped to the card's rounded shape
	glassPath := roundedPath(cardX, cardY, cw, chh, rad)
	glassReady := r.backdropOK && r.backdrop != nil
	if glassReady {
		// A point (sx, sy) in the sprite maps to the desktop at
		// stripRect + (sx - pivot), so the matching backdrop pixel is offset from
		// the backdrop's own origin by the difference.
		dstX := app.panel.stripRect.X*scale + (float64(cardX) - (spritePad+cardW/2)*scale)
		dstY := app.panel.stripRect.Y*scale + (float64(cardY) - padTop*scale)
		sx := int(math.Round(dstX)) - r.backdropX
		sy := int(math.Round(dstY)) - r.backdropY
		s.clipPath(glassPath)
		if sx >= 0 && sy >= 0 && sx+cw <= r.backdrop.W && sy+chh <= r.backdrop.H {
			s.drawSubBitmap(r.backdrop, sx, sy, cw, chh, cardX, cardY, cw, chh)
		} else {
			// Partly outside the captured region: sample what is there and let the
			// tint cover the rest, rather than skewing the glass.
			ox, oy := max(0, sx), max(0, sy)
			ex := min(r.backdrop.W, sx+cw) - ox
			ey := min(r.backdrop.H, sy+chh) - oy
			if ex > 0 && ey > 0 {
				s.drawSubBitmap(r.backdrop, ox, oy, ex, ey, cardX+(ox-sx), cardY+(oy-sy), ex, ey)
			}
		}
		s.resetClip()
	}

	// --- the glass tint: a vertical white gradient, so the frame reads over a
	// white document as well as over a dark desktop.
	tintTop, tintBottom := rgba(255, 255, 255, 102), rgba(255, 255, 255, 51)
	if r.style == glassLight {
		tintTop, tintBottom = rgba(24, 28, 34, 140), rgba(24, 28, 34, 96)
	}
	tint := s.vGrad(cardX, cardY, cardY+chh, tintTop, tintBottom)
	s.clipPath(glassPath)
	s.fillRectBrush(tint, cardX, cardY, cw, chh)
	s.resetClip()
	freeBrush(tint)

	// --- a hairline inner edge, brighter at the top
	edge := ringPath(cardX, cardY, cw, chh, rad, max(1, ix(0.75)))
	edgeBrush := s.vGrad(cardX, cardY, cardY+chh, rgba(255, 255, 255, 235), rgba(255, 255, 255, 56))
	s.fillPath(edgeBrush, edge)
	freePath(edge)
	freeBrush(edgeBrush)

	// --- the photo, clipped to its own rounded corners
	px := cardX + ix(frameInset)
	py := cardY + ix(frameInset)
	pw := ix(photoW)
	ph := ix(photoH)
	photoPath := roundedPath(px, py, pw, ph, ix(photoRadius))
	s.clipPath(photoPath)
	s.drawScaledBitmap(it.Thumb.surf, px, py, pw, ph)
	s.resetClip()
	freePath(photoPath)

	// A faint border around the photo, as the original strokes it.
	border := ringPath(px, py, pw, ph, ix(photoRadius), max(1, ix(0.5)))
	fb := s.solidBrush(rgba(255, 255, 255, 46))
	s.fillPath(fb, border)
	freePath(border)
	freeBrush(fb)

	// --- the clip, gripping the top edge
	r.drawClip(s, float64(cardX)+float64(cw)/2, float64(cardY), scale)

	// --- a recording says so with a play button over its first frame
	if it.isRecording() {
		r.drawPlayBadge(s, float64(px)+float64(pw)/2, float64(py)+float64(ph)/2, scale)
	}

	// --- the corner cross, which appears under the pointer
	cross := Rect{}
	if it.Hover {
		d := 16 * scale
		cxp := float64(cardX+cw) - d*0.68
		cyp := float64(cardY) + d*0.68
		r.drawCross(s, cxp, cyp, d)
		cross = Rect{X: cxp - d*0.8, Y: cyp - d*0.8, W: d * 1.6, H: d * 1.6}
	}

	freePath(glassPath)
	s.flush()

	// The sprite owns its surface; nothing is copied, because a GDI+ clone over a
	// scan0-backed bitmap is not an independent copy and would outlive the memory
	// it points at.
	return &cardSprite{
		surf:    s,
		W:       W,
		H:       H,
		padLeft: spritePad,
		padTop:  padTop,
		cardW:   cardW,
		cardH:   cardH,
		crossR:  cross,
		photoW:  photoW,
		photoH:  photoH,
		version: it.Thumb.Version,
	}
}

// ---------------------------------------------------------------- pieces

// drawRope is LineView's Rope: a mid gray core with a faint highlight above it
// and a soft shadow below, fading out at both ends so it seems to come from
// beyond the screen.
//
// Each of the three passes is one closed path — the upper edge of the sag, then
// the lower edge back — filled with a horizontal gradient that carries the end
// fade. That is both exact and seamless: drawing the rope as a run of short
// quads instead makes every seam a place where a 1.2-unit thickness rounds to
// zero and the rope breaks into dots.
func (r *renderer) drawRope(s *surface, lay Layout, stripW, scale float64) {
	sag := lay.Sag(stripW)
	steps := max(64, int(stripW/4))
	if steps > 640 {
		steps = 640
	}
	// x is sampled a little past both edges, so the rope leaves the screen rather
	// than stopping inside it.
	xs := make([]float64, steps+1)
	ys := make([]float64, steps+1)
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		x := -24 + t*(stripW+48)
		xs[i] = x
		ys[i] = ropeTop + 4*sag*(x/stripW)*(1-(x/stripW))
	}

	passes := []struct {
		width float64
		offY  float64
		col   uint32
	}{
		{2.0, 1.2, rgba(0, 0, 0, 56)},          // the shadow the rope casts
		{1.2, 0, rgba(140, 140, 140, 255)},     // the rope
		{0.5, -0.55, rgba(255, 255, 255, 115)}, // the light catching its top
	}
	for _, p := range passes {
		path := ropeBandPath(xs, ys, p.width, p.offY, scale)
		if path == 0 {
			continue
		}
		// The fade at both ends, as a horizontal alpha ramp: clear at 0, opaque
		// between 8% and 92%, clear at 1. This is the original's gradient mask.
		left := int(math.Round(xs[0] * scale))
		right := int(math.Round(xs[len(xs)-1] * scale))
		faded := func(a float64) uint32 {
			return (p.col & 0x00FFFFFF) | (uint32(a*float64(byte(p.col>>24))) << 24)
		}
		br := s.lineBrushStops(left, 0, right, 0,
			[]uint32{faded(0), faded(1), faded(1), faded(0)},
			[]float32{0, 0.08, 0.92, 1})
		s.fillPath(br, path)
		freeBrush(br)
		freePath(path)
	}
}

// ropeBandPath builds the closed outline of a thick curve: the sampled points
// displaced up by half the thickness, then the same points back displaced down.
// The displacement is vertical rather than perpendicular, which for a rope that
// sags at most 30 units over the width of a screen differs from perpendicular by
// a fifth of a percent — and it keeps the thickness exactly uniform.
func ropeBandPath(xs, ys []float64, thickness, offY, scale float64) uintptr {
	if len(xs) < 2 {
		return 0
	}
	half := math.Max(0.5, thickness*scale/2)
	b := newPathBuilder()
	for i := range xs {
		b.add(int(math.Round(xs[i]*scale)), int(math.Round((ys[i]+offY-half)*scale)))
	}
	// Back along the underside.
	for i := len(xs) - 1; i >= 0; i-- {
		b.add(int(math.Round(xs[i]*scale)), int(math.Round((ys[i]+offY+half)*scale)))
	}
	return b.finish()
}

// fillSegment fills a short quad between two points: a variable-thickness line
// without a GDI+ pen, whose width would be a REAL this FFI layer cannot pass.
//
// A quad thinner than about two pixels can round away to nothing, so the
// perpendicular offset is floored at half a pixel and the resulting points are
// checked for a real area before the path is filled. For anything longer than a
// short stroke, ropeBandPath is the better tool.
func fillSegment(s *surface, x0, y0, x1, y1, thickness float64, argb uint32) {
	dx, dy := x1-x0, y1-y0
	length := math.Hypot(dx, dy)
	if length < 0.0001 {
		return
	}
	if thickness < 1 {
		thickness = 1
	}
	nx, ny := -dy/length*thickness/2, dx/length*thickness/2
	if math.Hypot(nx, ny) < 0.6 {
		// Widen to half a pixel, or the integer points below would coincide.
		scale := 0.6 / math.Hypot(nx, ny)
		nx, ny = nx*scale, ny*scale
	}
	b := newPathBuilder()
	b.add(int(math.Round(x0+nx)), int(math.Round(y0+ny)))
	b.add(int(math.Round(x1+nx)), int(math.Round(y1+ny)))
	b.add(int(math.Round(x1-nx)), int(math.Round(y1-ny)))
	b.add(int(math.Round(x0-nx)), int(math.Round(y0-ny)))
	path := b.finish()
	br := s.solidBrush(argb)
	s.fillPath(br, path)
	freeBrush(br)
	freePath(path)
}

// drawHint is LineView's Hint: a capsule of regular material with one line of
// secondary text, shown while the line is empty.
func (r *renderer) drawHint(s *surface, lay Layout, stripW, scale float64) {
	text := L("Take a screenshot and it will hang here")
	f := r.fHint
	f.setAlign(stringAlignCenter, stringAlignCenter)

	padX, padY := 12.0, 6.0
	// Measure in device pixels, then lay the capsule out in device pixels.
	tw, th := s.measureText(text, 0, 0, float64(s.W), 60*scale, f)
	bw := tw + padX*2*scale
	bh := th + padY*2*scale

	cx := float64(s.W) / 2
	ropeY := lay.RopeY(stripW/2, stripW) * scale
	cy := ropeY + 34*scale

	x := int(math.Round(cx - bw/2))
	y := int(math.Round(cy - bh/2))
	wi, hi := int(math.Round(bw)), int(math.Round(bh))

	drawSoftShadow(s, float64(x), float64(y), float64(wi), float64(hi),
		float64(hi)/2, 8*scale, 0.22, 1*scale)

	capsule := capsulePath(x, y, wi, hi)
	bg := s.vGrad(x, y, y+hi, rgba(255, 255, 255, 205), rgba(255, 255, 255, 165))
	textColor := rgba(60, 64, 72, 235)
	if r.style == glassLight {
		bg = s.vGrad(x, y, y+hi, rgba(30, 34, 40, 210), rgba(30, 34, 40, 180))
		textColor = rgba(235, 238, 244, 235)
	}
	s.fillPath(bg, capsule)
	freePath(capsule)
	freeBrush(bg)

	s.drawText(text, float64(x), float64(y), float64(wi), float64(hi), textColor, f)
	f.setAlign(stringAlignNear, stringAlignNear)
}

// drawSoftShadow draws a blurred rounded rectangle, the port of SwiftUI's
// .shadow(color:radius:y:). A real blur per frame is far too slow, so the shadow
// is a set of concentric rounded rectangles with decreasing alpha: at these
// sizes that reads the same and costs a handful of fills.
func drawSoftShadow(s *surface, x, y, w, h, radius, blur, alpha, offsetY float64) {
	const steps = 7
	for i := steps; i >= 1; i-- {
		t := float64(i) / float64(steps)
		grow := blur * t
		a := alpha * (1 - t) * 0.5
		if a <= 0.004 {
			continue
		}
		p := roundedPath(
			int(math.Round(x-grow)),
			int(math.Round(y-grow+offsetY)),
			int(math.Round(w+2*grow)),
			int(math.Round(h+2*grow)),
			int(math.Round(radius+grow)))
		br := s.solidBrush(rgba(0, 0, 0, a*255))
		s.fillPath(br, p)
		freeBrush(br)
		freePath(p)
	}
}

// drawClip is PeggedView's Clothespin: a narrow rounded bar with a vertical
// metal gradient, a highlight rim and the slot the rope passes through.
func (r *renderer) drawClip(s *surface, centerX, cardTopY, scale float64) {
	w := clipW * scale
	h := clipH * scale
	// The clip hangs so that clipOverCard of it overlaps the card.
	x := int(math.Round(centerX - w/2))
	y := int(math.Round(cardTopY - clipOverCard*scale))
	wi := int(math.Round(w))
	hi := int(math.Round(h))
	rad := int(math.Round(clipGripRadius * scale))

	body := s.lineBrushStops(x, y, x, y+hi,
		[]uint32{rgba(178, 178, 178, 255), rgba(238, 238, 238, 255), rgba(208, 208, 208, 255), rgba(158, 158, 158, 255)},
		[]float32{0, 0.32, 0.68, 1})
	path := roundedPath(x, y, wi, hi, rad)
	s.fillPath(body, path)
	freePath(path)
	freeBrush(body)

	rim := ringPath(x, y, wi, hi, rad, max(1, int(math.Round(0.6*scale))))
	rimBrush := s.vGrad(x, y, y+hi, rgba(255, 255, 255, 230), rgba(0, 0, 0, 46))
	s.fillPath(rimBrush, rim)
	freePath(rim)
	freeBrush(rimBrush)

	slotW := max(2, int(math.Round(5*scale)))
	slotH := max(1, int(math.Round(1.4*scale)))
	slotX := x + (wi-slotW)/2
	slotY := y + int(math.Round(8.5*scale))
	slot := capsulePath(slotX, slotY, slotW, slotH)
	slotBrush := s.solidBrush(rgba(0, 0, 0, 82))
	s.fillPath(slotBrush, slot)
	freePath(slot)
	freeBrush(slotBrush)
}

// drawPlayBadge is the play button the original puts over a recording's first
// frame.
func (r *renderer) drawPlayBadge(s *surface, cx, cy, scale float64) {
	d := playBadgeD * scale
	x := int(math.Round(cx - d/2))
	y := int(math.Round(cy - d/2))
	di := int(math.Round(d))

	drawSoftShadow(s, float64(x), float64(y), float64(di), float64(di), d/2, 4*scale, 0.25, 1*scale)
	circle := ellipsePath(x, y, di, di)
	bg := s.vGrad(x, y, y+di, rgba(255, 255, 255, 205), rgba(255, 255, 255, 165))
	s.fillPath(bg, circle)
	freePath(circle)
	freeBrush(bg)

	triH := d * 0.44
	triW := triH * 0.86
	tx := cx - triW*0.42
	ty := cy - triH/2
	tri := newPathBuilder()
	tri.add(int(math.Round(tx)), int(math.Round(ty)))
	tri.add(int(math.Round(tx+triW)), int(math.Round(cy)))
	tri.add(int(math.Round(tx)), int(math.Round(ty+triH)))
	path := tri.finish()
	br := s.solidBrush(rgba(20, 22, 26, 235))
	s.fillPath(br, path)
	freeBrush(br)
	freePath(path)
}

// drawCross is the corner cross a photo lets go with.
func (r *renderer) drawCross(s *surface, cx, cy, d float64) {
	rad := d / 2
	x := int(math.Round(cx - rad))
	y := int(math.Round(cy - rad))
	di := int(math.Round(d))

	drawSoftShadow(s, float64(x), float64(y), float64(di), float64(di), rad, 4, 0.30, 1)
	circle := ellipsePath(x, y, di, di)
	bg := s.vGrad(x, y, y+di, rgba(70, 74, 82, 225), rgba(38, 40, 46, 225))
	s.fillPath(bg, circle)
	freePath(circle)
	freeBrush(bg)

	arm := d * 0.21
	col := rgba(245, 246, 248, 245)
	fillSegment(s, cx-arm, cy-arm, cx+arm, cy+arm, math.Max(1, d*0.10), col)
	fillSegment(s, cx+arm, cy-arm, cx-arm, cy+arm, math.Max(1, d*0.10), col)
}

// drawCopiedPill is PeggedView's "Copied" caption: a small capsule in regular
// material that fades in beneath the card and out again.
func (r *renderer) drawCopiedPill(s *surface, lay Layout, scale float64, l *line) {
	var target *item
	for _, it := range l.Items {
		if it.Copied && it.CopyFade.X > 0.01 {
			target = it
			break
		}
	}
	if target == nil {
		return
	}
	rect, ok := l.HitRects[target.ID]
	if !ok {
		return
	}
	alpha := target.CopyFade.X
	text := L("Copied")
	f := r.fSmall
	f.setAlign(stringAlignCenter, stringAlignCenter)

	tw, th := s.measureText(text, 0, 0, 400*scale, 30*scale, f)
	padX, padY := 9.0*scale, 4.0*scale
	bw, bh := tw+padX*2, th+padY*2
	cx := (rect.X + rect.W/2) * scale
	cy := (rect.Y+rect.H)*scale + bh/2 + 6*scale
	x := int(math.Round(cx - bw/2))
	y := int(math.Round(cy - bh/2))
	wi, hi := int(math.Round(bw)), int(math.Round(bh))

	drawSoftShadow(s, float64(x), float64(y), float64(wi), float64(hi), float64(hi)/2, 6*scale, 0.2*alpha, 2*scale)
	cap := capsulePath(x, y, wi, hi)
	bg := s.vGrad(x, y, y+hi, rgba(28, 30, 36, 225*alpha), rgba(28, 30, 36, 195*alpha))
	s.fillPath(bg, cap)
	freePath(cap)
	freeBrush(bg)
	s.drawText(text, float64(x), float64(y), float64(wi), float64(hi), rgba(240, 242, 246, 240*alpha), f)
	f.setAlign(stringAlignNear, stringAlignNear)
}

// rgba packs a colour with an alpha into the ARGB integer GDI+ wants. Alpha is
// given in 0..255 and is clamped.
func rgba(r, g, b, a float64) uint32 {
	cl := func(v float64) uint32 {
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		return uint32(v + 0.5)
	}
	return cl(a)<<24 | cl(r)<<16 | cl(g)<<8 | cl(b)
}

// ellipsePath builds an ellipse as a path, since callers need to clip with it as
// well as fill it. GdipAddPathEllipseI is a single call with integer bounds and is
// safe, but for a circle the bezier form in circleInto is used so the shape can be
// combined with a second figure in one fill.
func ellipsePath(x, y, w, h int) uintptr {
	b := newPathBuilder()
	call(gpAddPathEllipseI, b.p, u(x), u(y), u(w), u(h))
	return b.finish()
}
