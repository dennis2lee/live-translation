package ui

import (
	"math"
	"strings"
	"time"

	"fyne.io/fyne/v2"
)

// ScrollDuration is how long the rows take to move up when new text pushes
// them — see SCROLL_PLAN.md.
//
// Short on purpose. The new words are in the layout, at full alpha, in the
// repaint they arrive in; only their position eases, and with ease-out half
// the distance is covered in under 45 ms. Fragments arrive on a one-second
// server clock and a row takes about three seconds to fill, so a move is a
// twentieth of the time between moves. It is a judgement about reading, not a
// measurement: long enough for the eye to follow the row it was on, short
// enough not to trail the voice.
const ScrollDuration = 150 * time.Millisecond

// startScroll starts a scroll animation. A variable so the tests can drive the
// frames themselves: Fyne's test driver ticks an animation straight to its end
// inside Start.
var startScroll = func(a *fyne.Animation) { a.Start() }

// shownRow is a row as it rests on screen: y is where it is drawn when no move
// is running, in the view's coordinates. For a leaving row it is where it
// would rest, above the box.
type shownRow struct {
	text    string
	settled bool
	y       float32
}

// trackKind says which row of the last repaint a move is measured by.
type trackKind int

const (
	trackNone trackKind = iota
	// trackSettled follows the last row of the settled paragraph, by its
	// start offset in the anchor's text.
	trackSettled
	// trackDraft follows the last row of the text in progress, by its offset
	// in that text — used only when nothing is settled.
	trackDraft
)

// scrollState is the move in progress and what the last repaint drew.
//
// All of it lives on the UI goroutine.
type scrollState struct {
	anim *fyne.Animation
	// from is the offset at the start of the running move, progress how far
	// it has eased (Fyne applies the curve). The rows are drawn from*(1-progress)
	// below where they rest.
	from     float32
	progress float32
	// ghosts are rows that have left the top of the box and are still on
	// their way out.
	ghosts []shownRow
	// pending is a move set up by this repaint whose animation is started
	// once the repaint is done.
	pending bool

	// What the last repaint drew, at rest.
	prev    []shownRow
	geom    string
	boxLeft float32
	kind    trackKind
	start   int
	draft   string
	trackY  float32
}

// offset is how far below their resting place the rows are drawn now.
func (s *scrollState) offset() float32 {
	if s.from == 0 {
		return 0
	}
	return s.from * (1 - clamp01(s.progress))
}

// cancel ends any move at once: the rows go where they rest, the leaving rows
// go.
func (s *scrollState) cancel() {
	if s.anim != nil {
		s.anim.Stop()
		s.anim = nil
	}
	s.from, s.progress, s.ghosts, s.pending = 0, 0, nil, false
}

// finish is the end of a move that ran its course.
func (s *scrollState) finish() {
	s.anim = nil
	s.from, s.progress, s.ghosts = 0, 0, nil
}

// retarget starts a move of `move` pixels up from wherever the rows are drawn
// now, so a move that interrupts another never jumps: the rows start from
// what was left of the running offset plus the new movement. Not capped — a
// cap made a one-row box jump at every interruption — and self-limiting,
// since 100 ms into a move only a quarter of it is left.
//
// top is where the new top visible row rests, and clipTop the top of the box:
// rows of the last repaint resting above top have left and become ghosts,
// and ghosts that can no longer reach the box are dropped.
func (s *scrollState) retarget(move, top, clipTop, lineH float32) {
	from := s.offset() + move
	var ghosts []shownRow
	keep := func(g shownRow) {
		if g.y+from+lineH > clipTop {
			ghosts = append(ghosts, g)
		}
	}
	for _, g := range s.ghosts {
		g.y -= move
		keep(g)
	}
	for _, p := range s.prev {
		p.y -= move
		if p.y < top-0.5 {
			keep(p)
		}
	}
	s.from, s.progress, s.ghosts, s.pending = from, 0, ghosts, true
}

// ghostFadeSteps is how many alphas a leaving row takes on its way out.
//
// Few, on purpose. Fyne's GL painter caches a text texture by its content —
// text, size, style, font and colour — and keeps it for a minute, so a fade
// in continuous alpha rasterised a fresh full-row texture every frame and
// held them all: measured in the real driver at 1280x720, graphics memory
// 62 MB without the animation and 303 MB with it (red team, implementation
// round one). Three visible steps are three textures per leaving row.
const ghostFadeSteps = 3

// fadeStep rounds a fade to one of ghostFadeSteps visible levels, or zero.
func fadeStep(f float32) float32 {
	return float32(math.Ceil(float64(clamp01(f)*ghostFadeSteps)-0.5)) / ghostFadeSteps
}

// insideFraction is how much of a row drawn at y, lineH tall, lies between
// top and bottom.
func insideFraction(y, lineH, top, bottom float32) float32 {
	if lineH <= 0 {
		return 0
	}
	return clamp01((min(y+lineH, bottom) - max(y, top)) / lineH)
}

// wrapResult is every row of a wrap, before the box trims it to its line
// count.
type wrapResult struct {
	all []row
	// settled is how many of all are the settled paragraph's; they come first.
	settled int
	// draft is the text in progress when exactly one line of it is wrapped on
	// rows of its own (DimDraft), trimmed; draftStarts are its rows' offsets
	// in it and draftFirst the index in all of its first row.
	draft       string
	draftStarts []int
	draftFirst  int
}

// visible is the newest maxLines rows.
func (w wrapResult) visible(maxLines int) []row {
	maxLines = max(1, maxLines)
	if len(w.all) > maxLines {
		return w.all[len(w.all)-maxLines:]
	}
	return w.all
}

// findStart is the index of the row that begins at offset, or -1.
func findStart(starts []int, offset int) int {
	for i, s := range starts {
		if s == offset {
			return i
		}
	}
	return -1
}

// trackedIndex finds, in a fresh wrap, the row the last repaint was tracking,
// and returns its index in w.all — or -1 when it cannot be found exactly,
// which means the rows were rewritten rather than moved and nothing animates.
//
// a is the anchor as the fresh wrap left it: carried and cut describe that
// wrap, starts its settled rows.
func (s *scrollState) trackedIndex(a *wrapAnchor, w wrapResult) int {
	switch s.kind {
	case trackSettled:
		if !a.carried || w.settled == 0 || len(a.starts) != w.settled {
			return -1
		}
		at := s.start - a.cut
		if at < 0 {
			return -1
		}
		return findStart(a.starts, at)
	case trackDraft:
		if w.settled != 0 || w.draft == "" || !strings.HasPrefix(w.draft, s.draft) {
			return -1
		}
		i := findStart(w.draftStarts, s.start)
		if i < 0 {
			return -1
		}
		return w.draftFirst + i
	}
	return -1
}

// track records which row this repaint is measured by, for the next one.
// rest(i) is where row i of w.all rests.
func (s *scrollState) track(a *wrapAnchor, w wrapResult, rest func(i int) float32) {
	s.kind = trackNone
	switch {
	case w.settled > 0 && len(a.starts) == w.settled:
		s.kind, s.start, s.trackY = trackSettled, a.starts[w.settled-1], rest(w.settled-1)
	case w.settled == 0 && w.draft != "" && len(w.draftStarts) > 0:
		last := len(w.draftStarts) - 1
		s.kind, s.start, s.draft = trackDraft, w.draftStarts[last], w.draft
		s.trackY = rest(w.draftFirst + last)
	}
}
