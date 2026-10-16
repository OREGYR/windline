package main

import (
	"math"
)

// flight.go is CaptureFlight.swift and the falling animation: cards that travel
// across the screen instead of being clipped to the strip.
//
// The macOS original draws a captured screenshot flying from the middle of the
// screen up to the line, and a discarded photo falling off the bottom. The panel
// is only as tall as the line, so both need a second, full-screen window: this
// overlay. It is created on demand, layered and click-through, and destroyed as
// soon as the last flight lands — an invisible full-screen window that swallowed
// clicks would be exactly the kind of bug that is hard to find later.

type flightKind int

const (
	flightFalling  flightKind = iota // a discarded photo, on its way down
	flightFlyingIn                   // a new capture, on its way to the line
)

type flight struct {
	item    *item
	kind    flightKind
	sprite  *cardSprite
	fromX   float64 // start anchor, monitor coordinates
	fromY   float64
	toX     float64
	toY     float64
	x, y    float64
	angle   float64
	spin    float64
	elapsed float64 // seconds
	dur     float64 // seconds
	alpha   float64
	velY    float64
	done    bool
	onDone  func(*flight)
}

type flightOverlay struct {
	app     *app
	hwnd    uintptr
	scale   float64
	flights []*flight
	visible bool
	surface *surface
}

func newFlightOverlay(a *app) *flightOverlay {
	return &flightOverlay{app: a, scale: 1}
}

// startFall is the line's onFall hook: a discarded photo begins its fall.
func (a *app) startFall(it *item) {
	if a.flights == nil {
		return
	}
	sprite := a.render.spriteFor(a, it)
	if sprite == nil {
		return
	}
	// The fall starts where the card was hanging and accelerates downwards, at
	// whatever tilt it had.
	lay := a.layout
	live := a.line.live()
	index, count := 0, len(live)
	for i, other := range live {
		if other.ID == it.ID {
			index = i
			break
		}
	}
	scale := a.panel.scale
	w := a.panel.stripRect.W
	x := lay.CardX(index, count, w, a.line.VisibleCount, a.line.Scroll, a.line.KeepSet)
	ropeY := lay.RopeY(x, w)
	a.flights.add(&flight{
		item:   it,
		kind:   flightFalling,
		sprite: sprite,
		x:      a.panel.stripRect.X + x,
		y:      a.panel.stripRect.Y + ropeY - pinAbove + cardOffsetBelowTop,
		angle:  (it.Tilt + it.Swing.X) * math.Pi / 180,
		spin:   randRange(-1.2, 1.2),
		alpha:  1,
	})
	_ = scale
}

// onItemDropped runs when a photo leaves the line for good.
func (a *app) onItemDropped(it *item) {
	a.render.discardCard(it.ID)
	// The fall animation owns the sprite for a while, so the item is only
	// removed from the model once it is off the screen.
	for i, f := range a.flights.flights {
		if f.item == it {
			f.onDone = func(f *flight) {
				a.flights.flights = removeFlight(a.flights.flights, f)
				a.line.remove(f.item.ID)
			}
			_ = i
			return
		}
	}
	a.line.remove(it.ID)
}

func removeFlight(list []*flight, target *flight) []*flight {
	for i, f := range list {
		if f == target {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}

// flyIn animates a new capture from a screen point to its place on the line. The
// drop-in spring on the item takes over as soon as it lands.
func (o *flightOverlay) flyIn(it *item, fromX, fromY float64) {
	sprite := o.app.render.spriteFor(o.app, it)
	if sprite == nil {
		return
	}
	lay := o.app.layout
	live := o.app.line.live()
	index, count := 0, len(live)
	for i, other := range live {
		if other.ID == it.ID {
			index = i
			break
		}
	}
	w := o.app.panel.stripRect.W
	x := lay.CardX(index, count, w, o.app.line.VisibleCount, o.app.line.Scroll, o.app.line.KeepSet)
	ropeY := lay.RopeY(x, w)
	o.add(&flight{
		item:   it,
		kind:   flightFlyingIn,
		sprite: sprite,
		fromX:  fromX,
		fromY:  fromY,
		toX:    o.app.panel.stripRect.X + x,
		toY:    o.app.panel.stripRect.Y + ropeY - pinAbove + cardOffsetBelowTop,
		x:      fromX,
		y:      fromY,
		alpha:  1,
		dur:    0.42,
		onDone: func(f *flight) {
			if o.app.line.find(f.item.ID) != nil {
				o.app.line.land(f.item.ID)
			}
		},
	})
}

func (o *flightOverlay) add(f *flight) {
	if f.kind == flightFalling {
		// A fall is a gravity animation, not a timed one: it ends when the card is
		// past the bottom of the screen.
		f.dur = 0
		f.velY = 0
	}
	o.flights = append(o.flights, f)
	o.ensureWindow()
}

// ---------------------------------------------------------------- window

func (o *flightOverlay) ensureWindow() {
	if o.hwnd != 0 {
		return
	}
	o.hwnd = call(pCreateWindowEx, wsExLayered|wsExTopmost|wsExToolwin|wsExNoAct|wsExTranspar,
		utf16Ptr("WindlineFlight"), 0, wsPopup, 0, 0, 1, 1, 0, 0, moduleHandle(), 0)
	if o.hwnd == 0 {
		logf("could not create the flight window: %d", lastError())
		return
	}
	// A full-screen window that ignores the mouse and never takes focus.
	work, _ := monitorWork(o.app.monitor)
	scale := o.app.panel.scale
	o.scale = scale
	x := int(math.Round(work.X * scale))
	y := int(math.Round(work.Y * scale))
	w := int(math.Ceil(work.W * scale))
	h := int(math.Ceil(work.H * scale))
	call(pSetWindowPos, o.hwnd, hwndTopmost, u(x), u(y), u(w), u(h), swpNoActivate|swpShowWindow)
	o.visible = true
	logf("flight window up: %dx%d at (%d,%d)", w, h, x, y)
}

func (o *flightOverlay) destroyWindow() {
	if o.hwnd == 0 {
		return
	}
	call(pDestroyWindow, o.hwnd)
	o.hwnd = 0
	o.visible = false
	if o.surface != nil {
		o.surface.free()
		o.surface = nil
	}
	logf("flight window down")
}

// step advances every flight and repaints the overlay. It returns true while
// anything is still moving, which is what keeps the app's own frame clock alive.
func (o *flightOverlay) step(nowMS, prevMS float64) bool {
	if len(o.flights) == 0 || o.hwnd == 0 {
		if o.hwnd != 0 {
			o.destroyWindow()
		}
		return false
	}
	dtMS := nowMS - prevMS
	if dtMS <= 0 || dtMS > 250 {
		dtMS = tickMS
	}
	dt := dtMS / 1000

	work, _ := monitorWork(o.app.monitor)
	scale := o.scale
	bottom := work.Bottom() + 200 // past the bottom of the screen

	for _, f := range o.flights {
		switch f.kind {
		case flightFalling:
			// Gravity, with a little air resistance so the fall reads as paper
			// rather than a stone. The spin keeps the tilt changing as it goes.
			f.velY += 2600 * dt
			f.velY *= 1 - 0.6*dt
			f.y += f.velY * dt / scale
			f.angle += f.spin * dt * 1.6
			f.alpha = clamp01((bottom - f.y) / 160)
			if f.y > bottom || f.alpha <= 0.01 {
				f.done = true
			}
		case flightFlyingIn:
			f.elapsed += dt
			t := clamp01(f.elapsed / f.dur)
			// Ease out: quick off the mark, settling onto the line.
			e := 1 - (1-t)*(1-t)*(1-t)
			f.x = f.fromX + (f.toX-f.fromX)*e
			f.y = f.fromY + (f.toY-f.fromY)*e
			// A small arc, so it arrives from below rather than in a straight line.
			f.y -= math.Sin(t*math.Pi) * 26
			f.alpha = t
			if t >= 1 {
				f.done = true
			}
		}
		if f.done && f.onDone != nil {
			f.onDone(f)
			f.onDone = nil
		}
	}

	// Drop the finished ones and rebuild the list in place.
	kept := o.flights[:0]
	for _, f := range o.flights {
		if !f.done {
			kept = append(kept, f)
		}
	}
	o.flights = kept

	o.paint(work, scale)
	if len(o.flights) == 0 {
		o.destroyWindow()
	}
	return true
}

// paint composites every flight into one full-screen surface and blits it.
func (o *flightOverlay) paint(work Rect, scale float64) {
	w := int(math.Ceil(work.W * scale))
	h := int(math.Ceil(work.H * scale))
	if w < 1 || h < 1 {
		return
	}
	if o.surface == nil || o.surface.W != w || o.surface.H != h {
		if o.surface != nil {
			o.surface.free()
		}
		o.surface = newSurface(w, h)
	}
	s := o.surface
	s.clear()
	s.setQuality()

	for _, f := range o.flights {
		if f.sprite == nil {
			continue
		}
		// Flights are positioned in monitor coordinates; drawSpriteAt takes the
		// same coordinates and applies the scale itself.
		o.app.render.drawSpriteAt(s, f.sprite, f.x-work.X, f.y-work.Y, f.angle, scale, f.alpha)
	}
	s.flush()

	x := int(math.Round(work.X * scale))
	y := int(math.Round(work.Y * scale))
	dst := POINT{X: int32(x), Y: int32(y)}
	size := SIZE{CX: int32(s.W), CY: int32(s.H)}
	src := POINT{0, 0}
	blend := BLENDFUNCTION{BlendOp: 0, BlendFlags: 0, SourceConstantAlpha: 255, AlphaFormat: 1}
	call(pUpdateLayeredWindow, o.hwnd, 0, uptr(&dst), uptr(&size), s.memDC, uptr(&src), 0, uptr(&blend), ulwAlpha)
}

func (o *flightOverlay) stop() {
	o.flights = nil
	o.destroyWindow()
}

func clamp01(v float64) float64 {
	return math.Min(1, math.Max(0, v))
}
