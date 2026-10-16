package main

import "math"

// Layout is a direct port of the macOS original's Layout enum, in logical
// layout units. Everything is measured in these units and multiplied by the
// screen's DPI scale at draw time, so the line looks the same on a 150% display
// as on a 100% one.

// Size is the Size setting from the tray menu.
type Size int

const (
	SizeSmall Size = iota
	SizeMedium
	SizeLarge
)

func (s Size) Key() string {
	switch s {
	case SizeSmall:
		return "small"
	case SizeLarge:
		return "large"
	default:
		return "medium"
	}
}

func sizeFromKey(k string) Size {
	switch k {
	case "small":
		return SizeSmall
	case "large":
		return SizeLarge
	default:
		return SizeMedium
	}
}

// Scale grows or shrinks the photos and the room they take, as the original
// does. Medium is the original size.
func (s Size) Scale() float64 {
	switch s {
	case SizeSmall:
		return 0.8
	case SizeLarge:
		return 1.35
	default:
		return 1
	}
}

const (
	ropeTop        = 10.0 // where the rope hangs from the top of the strip
	pinAbove       = 9.5  // the clip pokes this far above the rope
	frameRadius    = 16.0 // the glass frame's corner radius
	frameInset     = 4.0  // the glass border around the photo
	panelHeightPad = 106.0
	cardPhotoInset = 14.0 // card width minus this is the photo's max width

	// The macOS original pops the line back up when the pointer has been away
	// for half a second, and brings it down after resting 0.25 s in the menu
	// bar. On Windows the equivalent rest stop is the top edge of the screen.
	retractDelay = 0.5
	revealDelay  = 0.25
	// How far the whole line waits above the top edge when tucked away.
	tuckOffset = panelHeightPad + 12
)

// Layout carries the two settings that change the geometry.
type Layout struct {
	Size Size
	Dpi  float64 // 1.0 at 96 dpi
}

func (l Layout) PhotoMaxHeight() float64 { return math.Round(104 * l.Size.Scale()) }
func (l Layout) CardWidth() float64      { return math.Round(150 * l.Size.Scale()) }
func (l Layout) Spacing() float64        { return l.CardWidth() + 24 }
func (l Layout) PanelHeight() float64    { return l.PhotoMaxHeight() + panelHeightPad }

// Sag is how far the rope droops at the middle, bounded so it stays subtle.
func (l Layout) Sag(width float64) float64 { return math.Min(30, width*0.018) }

// RopeY is the rope's height at x: a parabola from edge to edge of the screen.
func (l Layout) RopeY(x, width float64) float64 {
	if width <= 0 {
		return ropeTop
	}
	f := x / width
	return ropeTop + 4*l.Sag(width)*f*(1-f)
}

// CardX is a port of Layout.x. Photos hang `Spacing` apart, centred. When there
// are more than fit:
//
//   - with Keep on line set they keep their spacing, the newest stays where the
//     last one would hang on a full line, and scroll brings older ones in from
//     the left;
//   - otherwise (a line hung on a wider screen) they move closer together so
//     none is past the edge.
func (l Layout) CardX(index, count int, width float64, visible int, scroll float64, keepSet bool) float64 {
	if keepSet && visible > 0 && count > visible {
		newest := width/2 + float64(visible-1)*l.Spacing()/2
		return newest - float64(count-1-index)*l.Spacing() + scroll
	}
	gaps := float64(max(count-1, 0))
	step := l.Spacing()
	if gaps > 0 {
		step = math.Min(l.Spacing(), math.Max(0, width-200)/gaps)
	}
	return width/2 - gaps*step/2 + float64(index)*step
}

// PhotoSize fits a photo inside the card area keeping its proportions, so the
// glass border hugs it whether the screenshot is wide or tall.
func (l Layout) PhotoSize(w, h float64) (float64, float64) {
	maxW := l.CardWidth() - cardPhotoInset
	maxH := l.PhotoMaxHeight()
	if w <= 0 || h <= 0 {
		return maxW, maxH
	}
	scale := math.Min(maxW/w, maxH/h)
	return w * scale, h * scale
}

// CardSize is the photo plus the glass inset.
func (l Layout) CardSize(w, h float64) (float64, float64) {
	pw, ph := l.PhotoSize(w, h)
	return pw + frameInset*2, ph + frameInset*2
}

// FitsCount is how many cards fit across a screen of this width, clamped to the
// same 3..12 range as the original.
func (l Layout) FitsCount(width float64) int {
	usable := width - 200
	return min(12, max(3, int(usable/l.Spacing())))
}

// CardTop is the distance from the top of the strip to the card's top edge.
const cardOffsetBelowTop = 26 - 12

// CardRectOnScreen computes where a card hangs, in monitor coordinates. The
// panel occupies stripRect; this is the unrotated frame the renderer tilts.
func (l Layout) CardRectOnScreen(stripRect Rect, index, count int, visible int, scroll float64, keepSet bool, photoW, photoH float64) Rect {
	w := float64(stripRect.W)
	x := l.CardX(index, count, w, visible, scroll, keepSet)
	viewTop := l.RopeY(x, w) - pinAbove
	cardTop := viewTop + cardOffsetBelowTop
	cw, ch := l.CardSize(photoW, photoH)
	return Rect{
		X: stripRect.X + x - cw/2,
		Y: stripRect.Y + cardTop,
		W: cw,
		H: ch,
	}
}
