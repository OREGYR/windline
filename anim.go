package main

import "math"

// anim.go reproduces the original's motion. Every animation there is a SwiftUI
// spring or a timed ease, and the parameters are copied from the Swift source so
// the line feels the same: the same sway, the same overshoot, the same settle.
//
// A spring here is a mass-spring-damper integrated at frame rate:
//
//	x'' = -k*x - c*x'
//
// SwiftUI's `.spring(response:dampingFraction:)` converts with w0 = 2*pi/response,
// k = w0^2, c = 2*dampingFraction*w0, and `.interpolatingSpring(stiffness:damping:)`
// passes k and c straight through. The constants below are those conversions,
// done once at the port so the numbers match the Swift source.
//
// The two motions also need a critical caveat that the Swift version does not
// have: a spring that overshoots a pixel target and is integrated by the frame
// clock will jitter at rest. So a spring snaps when it is within a hair of its
// target, and the app only repaints while something reports movement.

// Spring parameters, named after the motion each belongs to in the original.
const (
	// PeggedView arrive: .spring(response: 0.42, dampingFraction: 0.72)
	arriveK = 223.8
	arriveC = 21.5
	// PeggedView nudge: .interpolatingSpring(stiffness: 46, damping: 2.6)
	swingK = 46.0
	swingC = 2.6
	// PeggedView land: .interpolatingSpring(stiffness: 38, damping: 2.4)
	settleK = 38.0
	settleC = 2.4
	// LineView items rearranging: .spring(response: 0.55, dampingFraction: 0.78)
	layoutK = 130.5
	layoutC = 17.8
	// LineView reveal: .spring(response: 0.42, dampingFraction: 0.82)
	revealK = 223.8
	revealC = 24.5
)

// anim is one animated scalar, with the two motions the app needs.
type anim struct {
	X, V   float64 // value and velocity
	target float64
	k, c   float64 // spring constants

	// ease mode
	from       float64
	easeTarget float64
	startMS    float64
	durMS      float64
	easeIn     bool

	mode animMode
}

type animMode int

const (
	modeSpring animMode = iota
	modeEase
	modeIdle
)

// springTo retargets with a spring.
func (a *anim) springTo(target, k, c, nowMS float64) {
	a.target, a.k, a.c, a.mode = target, k, c, modeSpring
	_ = nowMS
}

// snap sets the value with no animation.
func (a *anim) snap(v float64) {
	a.X, a.V, a.target, a.mode = v, 0, v, modeIdle
}

// easeTo retargets with a timed ease, matching SwiftUI's .easeOut / .easeIn.
func (a *anim) easeTo(target, durMS float64, easeIn bool, nowMS float64) {
	a.from, a.easeTarget, a.startMS, a.durMS, a.easeIn = a.X, target, nowMS, durMS, easeIn
	a.target = target
	a.mode = modeEase
}

// step advances the animation to nowMS and reports whether it is still moving.
// dt is clamped so a stalled frame — a busy machine, a dragged window — cannot
// make a stiff spring explode.
func (a *anim) step(nowMS float64, lastMS *float64) bool {
	dt := (nowMS - *lastMS) / 1000
	if dt <= 0 {
		return a.mode != modeIdle
	}
	if dt > 0.05 {
		dt = 0.05
	}
	switch a.mode {
	case modeSpring:
		// Sub-step at a fixed rate: a stiff spring integrated in one 50 ms step
		// overshoots wildly, so the step is subdivided at 4 ms.
		steps := int(math.Ceil(dt / 0.004))
		if steps < 1 {
			steps = 1
		}
		h := dt / float64(steps)
		moving := false
		for i := 0; i < steps; i++ {
			acc := -a.k*(a.X-a.target) - a.c*a.V
			a.V += acc * h
			a.X += a.V * h
			if math.Abs(a.X-a.target) > 0.002 || math.Abs(a.V) > 0.02 {
				moving = true
			}
		}
		if !moving {
			a.snap(a.target)
			return false
		}
		return true
	case modeEase:
		t := (nowMS - a.startMS) / a.durMS
		if t >= 1 {
			a.X = a.easeTarget
			a.mode = modeIdle
			return false
		}
		if t < 0 {
			t = 0
		}
		e := t * t // easeIn: slow start
		if !a.easeIn {
			e = 1 - (1-t)*(1-t) // easeOut: quick start, gentle stop
		}
		a.X = a.from + (a.easeTarget-a.from)*e
		return true
	}
	return false
}

func (a *anim) moving() bool { return a.mode != modeIdle }

// rest returns the value the animation is heading for, which is what the layout
// should be measured against rather than the value it happens to be passing
// through.
func (a *anim) rest() float64 {
	switch a.mode {
	case modeEase:
		return a.easeTarget
	case modeSpring:
		return a.target
	}
	return a.X
}

// clock is the animation clock, in milliseconds since the app started. Swapping
// it for a wall clock in the tests makes every animation deterministic.
var clock = func() float64 { return float64(tickCount()) }
