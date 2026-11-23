package ui

import (
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"

	"github.com/dennis2lee/live-translation/src_go/internal/captions"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
)

// manualScroll replaces the animation starter so a test drives the frames
// itself, and returns the animations started.
func manualScroll(t *testing.T) *[]*fyne.Animation {
	t.Helper()
	var started []*fyne.Animation
	old := startScroll
	startScroll = func(a *fyne.Animation) { started = append(started, a) }
	t.Cleanup(func() { startScroll = old })
	return &started
}

// shown is a row on screen: where it is drawn, in the view's coordinates, and
// at what alpha.
type shown struct {
	text  string
	y     float32
	alpha uint8
	ghost bool
}

// onScreen is every caption row drawn, rows and leaving rows alike, in view
// coordinates (the clip's position plus the row's).
func onScreen(r *captionRenderer) []shown {
	var out []shown
	add := func(ts []*canvas.Text, ghost bool) {
		for _, tx := range ts {
			if !tx.Visible() || tx.Text == "" {
				continue
			}
			_, _, _, a := tx.Color.RGBA()
			out = append(out, shown{tx.Text, r.clip.Position().Y + tx.Position().Y, uint8(a >> 8), ghost})
		}
	}
	add(r.texts, false)
	add(r.ghosts, true)
	return out
}

func rowsOnly(all []shown) []shown {
	var out []shown
	for _, s := range all {
		if !s.ghost {
			out = append(out, s)
		}
	}
	return out
}

func settledLine(text string) []captions.Line {
	return []captions.Line{{Text: text, Settled: true}}
}

// growUntilMove appends words to a paragraph one at a time until the rows
// move, and returns the paragraph before and after. It fails the test if no
// move comes within a few hundred words.
func growUntilMove(t *testing.T, v *CaptionView, started *[]*fyne.Animation, text string) (before, after string) {
	t.Helper()
	n := len(*started)
	for i := 0; i < 400; i++ {
		next := strings.TrimSpace(text + " w" + strconv.Itoa(i))
		v.SetLines(settledLine(next))
		if len(*started) > n {
			return text, next
		}
		text = next
	}
	t.Fatal("no move in 400 words")
	return "", ""
}

func tick(a *fyne.Animation, f float32) {
	if a.Curve != nil {
		f = a.Curve(f)
	}
	a.Tick(f)
}

// The request: when the box is full and new text needs a row, the rows on
// screen move up one row, and the move is drawn, not jumped.
func TestOverflowSlidesTheRowsUpOneRow(t *testing.T) {
	started := manualScroll(t)
	v, r := viewOf(t, config.Default())
	lineH := v.face().LineHeight(float32(v.settings.FontSize))

	// Fill the box first: the default is two rows.
	text, _ := growUntilMove(t, v, started, "Opening words.")
	text, _ = growUntilMove(t, v, started, text)
	v.SetLines(settledLine(text))
	finishAll(*started)
	before := rowsOnly(onScreen(r))
	if len(before) != 2 {
		t.Fatalf("box not full: %+v", before)
	}

	_, next := growUntilMove(t, v, started, text)
	_ = next
	a := (*started)[len(*started)-1]
	if got := v.scroll.from; math.Abs(float64(got-lineH)) > 0.01 {
		t.Fatalf("move of %v px, want one row (%v)", got, lineH)
	}

	// At the first frame the row that was at the bottom is still where it was.
	frame0 := onScreen(r)
	lastBefore := before[len(before)-1]
	found := false
	for _, s := range frame0 {
		if strings.HasPrefix(s.text, lastBefore.text) && !s.ghost {
			found = true
			if s.y != lastBefore.y {
				t.Errorf("frame 0: %q drawn at %v, was at %v", s.text, s.y, lastBefore.y)
			}
		}
	}
	if !found {
		t.Fatalf("the old bottom row is not on screen: %+v", frame0)
	}
	// The old top row is leaving, as a ghost, still where it was and opaque.
	var ghost *shown
	for i := range frame0 {
		if frame0[i].ghost {
			ghost = &frame0[i]
		}
	}
	if ghost == nil || ghost.text != before[0].text || ghost.y != before[0].y {
		t.Fatalf("leaving row = %+v, want %+v", ghost, before[0])
	}

	// Half way: the leaving row is fading, the entering row is opaque.
	tick(a, 0.5)
	mid := onScreen(r)
	for _, s := range mid {
		if s.ghost && (s.alpha == 0 || s.alpha == 255) {
			t.Errorf("mid-move the leaving row is at alpha %d, want between", s.alpha)
		}
		if !s.ghost && s.alpha != 255 {
			t.Errorf("mid-move %q is at alpha %d; entering and staying rows are never faded", s.text, s.alpha)
		}
	}

	// At the end everything is where a view without the animation puts it.
	tick(a, 1)
	off := config.Default()
	off.SmoothScroll = false
	ov, or := viewOf(t, off)
	ov.SetLines(v.lines)
	if got, want := onScreen(r), onScreen(or); !sameShown(got, want) {
		t.Errorf("at rest:\n got %+v\nwant %+v", got, want)
	}
	if v.scroll.anim != nil || len(v.scroll.ghosts) != 0 || v.scroll.offset() != 0 {
		t.Errorf("the move did not end clean: %+v", v.scroll)
	}
}

// textWidthOf is the width the view wraps its rows to.
func textWidthOf(v *CaptionView) float32 {
	return v.Size().Width*float32(v.settings.BoxMaxWidthPct)/100 - 2*PadH
}

func finishAll(as []*fyne.Animation) {
	for _, a := range as {
		tick(a, 1)
	}
}

func sameShown(a, b []shown) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A row that grows within itself is not a move.
func TestGrowthWithinARowDoesNotMove(t *testing.T) {
	started := manualScroll(t)
	v, _ := viewOf(t, config.Default())
	v.SetLines(settledLine("Short."))
	v.SetLines(settledLine("Short. And"))
	v.SetLines(settledLine("Short. And more"))
	if len(*started) != 0 {
		t.Errorf("%d moves for text that stayed on one row", len(*started))
	}
}

// Rows rewritten rather than moved are drawn at once.
func TestRewrittenRowsDoNotMove(t *testing.T) {
	started := manualScroll(t)
	v, _ := viewOf(t, config.Default())
	v.SetLines(settledLine(strings.Repeat("alpha beta gamma ", 8)))
	v.SetLines(settledLine(strings.Repeat("delta epsilon zeta ", 9)))
	if len(*started) != 0 {
		t.Errorf("%d moves for unrelated text", len(*started))
	}
}

// The history dropping its oldest sentence, with nothing on screen changing,
// is not a move.
func TestHistoryTrimmedAtTheFrontDoesNotMove(t *testing.T) {
	started := manualScroll(t)
	v, r := viewOf(t, config.Default())
	var lines []captions.Line
	for i := 0; i < 12; i++ {
		lines = append(lines, captions.Line{Text: "Sentence number " + string(rune('A'+i)) + " is here.", Settled: true})
	}
	v.SetLines(lines)
	finishAll(*started)
	n := len(*started)
	before := onScreen(r)
	v.SetLines(lines[1:])
	if len(*started) != n {
		t.Errorf("dropping a sentence nobody could see started a move")
	}
	if after := onScreen(r); !sameShown(before, after) {
		t.Errorf("rows changed: %+v -> %+v", before, after)
	}
}

// A fragment that adds two rows at once moves them two rows.
func TestTwoRowsInOneFragmentMoveTwoRows(t *testing.T) {
	started := manualScroll(t)
	s := config.Default()
	s.MaxLines = 3
	v, _ := viewOf(t, s)
	lineH := v.face().LineHeight(float32(s.FontSize))
	text := "Start."
	for len(v.scroll.prev) < 3 {
		text += " filler words"
		v.SetLines(settledLine(text))
		finishAll(*started)
	}
	n := len(*started)
	// Add text until the paragraph is two rows longer in one go.
	add := ""
	all := func(t string) int { return len(v.face().Wrap(t, float32(s.FontSize), textWidthOf(v))) }
	base := all(text)
	for all(text+add) < base+2 {
		add += " more words"
	}
	v.SetLines(settledLine(text + add))
	if len(*started) != n+1 {
		t.Fatalf("no move for a two-row fragment")
	}
	if got := v.scroll.from; math.Abs(float64(got-2*lineH)) > 0.01 {
		t.Errorf("moved %v px, want two rows (%v)", got, 2*lineH)
	}
}

// Many moves in quick succession keep only the leaving rows that can still
// be seen.
func TestABurstOfMovesKeepsOnlyVisibleLeavingRows(t *testing.T) {
	started := manualScroll(t)
	s := config.Default()
	s.MaxLines = 1
	v, r := viewOf(t, s)
	lineH := v.face().LineHeight(float32(s.FontSize))
	text := "Burst."
	top := r.box.Position().Y
	most := 0
	for i := 0; i < 8; i++ {
		_, text = growUntilMove(t, v, started, text)
		// Right after a move is set up, every leaving row kept is drawn
		// where it can still be seen.
		for _, g := range v.scroll.ghosts {
			if g.y+v.scroll.from+lineH <= top {
				t.Errorf("move %d kept a leaving row that can never be seen: %+v", i, g)
			}
		}
		most = max(most, len(v.scroll.ghosts))
		// Most of the way through before the next one, so the oldest have
		// left the box by the time it comes.
		tick((*started)[len(*started)-1], 0.9)
	}
	if most == 0 {
		t.Fatal("setup: nothing was leaving")
	}
	if most > 2 {
		t.Errorf("%d leaving rows kept at once in a one-row box", most)
	}
}

// A move that interrupts another starts from where the rows are drawn, so
// nothing jumps — checked where a cap would bite, one and two rows.
func TestAnInterruptedMoveDoesNotJump(t *testing.T) {
	for _, rows := range []int{1, 2} {
		t.Run(string(rune('0'+rows)), func(t *testing.T) {
			started := manualScroll(t)
			s := config.Default()
			s.MaxLines = rows
			v, r := viewOf(t, s)
			lineH := v.face().LineHeight(float32(s.FontSize))
			text, _ := growUntilMove(t, v, started, "First words.")
			finishAll(*started)
			text, _ = growUntilMove(t, v, started, strings.TrimSpace(text+" x"))
			finishAll(*started)

			_, text = growUntilMove(t, v, started, text)
			first := (*started)[len(*started)-1]
			tick(first, 0.5)
			remaining := v.scroll.offset()
			if remaining <= 0 {
				t.Fatalf("nothing left of the first move")
			}
			before := onScreen(r)

			_, _ = growUntilMove(t, v, started, text)
			if (*started)[len(*started)-1] == first {
				t.Fatal("no second move")
			}
			if got, want := v.scroll.from, remaining+lineH; math.Abs(float64(got-want)) > 0.01 {
				t.Errorf("second move starts at %v, want what was left (%v) plus a row (%v)", got, remaining, lineH)
			}
			// Every leaving row kept can still reach the box.
			for _, g := range v.scroll.ghosts {
				if g.y+v.scroll.from+lineH <= r.box.Position().Y {
					t.Errorf("a leaving row that can never be seen is kept: %+v", g)
				}
			}
			// A frame of the replaced move changes nothing.
			from, progress := v.scroll.from, v.scroll.progress
			tick(first, 0.9)
			if v.scroll.from != from || v.scroll.progress != progress {
				t.Errorf("a frame of the replaced move moved the rows: progress %v -> %v", progress, v.scroll.progress)
			}
			after := onScreen(r)
			for _, b := range before {
				for _, a := range after {
					if a.text == b.text && math.Abs(float64(a.y-b.y)) > 0.01 {
						t.Errorf("%q jumped from %v to %v", a.text, b.y, a.y)
					}
				}
			}
		})
	}
}

// A sentence dropped from the front of the history in the same repaint as an
// overflow: the rows still move, followed through the cut.
func TestAMoveWithTheHistoryTrimmedAtTheFront(t *testing.T) {
	started := manualScroll(t)
	v, _ := viewOf(t, config.Default())
	lineH := v.face().LineHeight(float32(v.settings.FontSize))
	lines := []captions.Line{
		{Text: "An opening sentence that will be dropped.", Settled: true},
		{Text: "A second sentence that stays.", Settled: true},
	}
	last := "And the one being said"
	for i := 0; i < 400; i++ {
		cur := append(append([]captions.Line{}, lines...), captions.Line{Text: last, Settled: true})
		v.SetLines(cur)
		finishAll(*started)
		n := len(*started)
		next := last + " w" + strconv.Itoa(i)
		// Would the next word alone add a row? Then drop the first sentence in
		// the same repaint: the visible rows still move up one, and the move
		// must be found through the cut.
		trimmed := append(append([]captions.Line{}, lines[1:]...), captions.Line{Text: next, Settled: true})
		full := append(append([]captions.Line{}, lines...), captions.Line{Text: next, Settled: true})
		size, width := float32(v.settings.FontSize), textWidthOf(v)
		if len(Face{}.reflowRows(full, size, width, 100)) > len(Face{}.reflowRows(cur, size, width, 100)) {
			v.SetLines(trimmed)
			if v.anchor.cut == 0 || !v.anchor.carried {
				t.Fatalf("setup: cut %d, carried %v", v.anchor.cut, v.anchor.carried)
			}
			if len(*started) != n+1 {
				t.Fatal("an overflow with the front trimmed was not animated")
			}
			if math.Abs(float64(v.scroll.from-lineH)) > 0.01 {
				t.Errorf("moved %v, want one row (%v)", v.scroll.from, lineH)
			}
			return
		}
		last = next
	}
	t.Fatal("setup: no overflow found")
}

// A non-fixed box hugs its widest row. When the move also shifts the box
// sideways, it is drawn at once rather than as a slide that jumps sideways.
func TestASidewaysBoxDoesNotSlide(t *testing.T) {
	started := manualScroll(t)
	s := config.Default()
	s.FixedBox = false
	v, r := viewOf(t, s)
	text := "Go."
	sideways := 0
	for i := 0; i < 300; i++ {
		x := r.box.Position().X
		n := len(*started)
		text += " w" + strconv.Itoa(i)
		v.SetLines(settledLine(text))
		finishAll(*started)
		if r.box.Position().X != x {
			sideways++
			if len(*started) != n {
				t.Fatalf("the box moved sideways (%v -> %v) and the rows slid", x, r.box.Position().X)
			}
		}
	}
	if sideways == 0 {
		t.Fatal("setup: the box never moved sideways")
	}
	if len(*started) == 0 {
		t.Error("a non-fixed box never slid at all")
	}
}

// A one-row box still moves: its only row leaves the screen entirely, so the
// move is found through the anchor rather than among the rows on screen.
func TestAOneRowBoxMoves(t *testing.T) {
	started := manualScroll(t)
	s := config.Default()
	s.MaxLines = 1
	v, _ := viewOf(t, s)
	lineH := v.face().LineHeight(float32(s.FontSize))
	growUntilMove(t, v, started, "One row only.")
	if got := v.scroll.from; math.Abs(float64(got-lineH)) > 0.01 {
		t.Errorf("moved %v px, want one row (%v)", got, lineH)
	}
	if len(v.scroll.ghosts) != 1 {
		t.Errorf("the row that left is not drawn leaving: %+v", v.scroll.ghosts)
	}
}

// A word finished across the end of a row moves down whole, so the old last
// row gets shorter. Comparing row strings misses that; the anchor does not.
func TestAWordFinishedAcrossTheRowEndStillMoves(t *testing.T) {
	started := manualScroll(t)
	v, _ := viewOf(t, config.Default())
	width := textWidthOf(v)
	size := float32(v.settings.FontSize)
	f := v.face()
	// Find a paragraph whose last row ends in a piece of a word that, once
	// finished, no longer fits that row.
	for pad := 0; pad < 200; pad++ {
		head := "Begin " + strings.Repeat("ab ", pad)
		old := head + "trans"
		now := head + "transportationally"
		ro, rn := f.Wrap(old, size, width), f.Wrap(now, size, width)
		if len(ro) < 2 || len(rn) != len(ro)+1 || len(rn[len(ro)-1]) >= len(ro[len(ro)-1]) {
			continue
		}
		v.SetLines(nil)
		v.SetLines(settledLine(old))
		finishAll(*started)
		n := len(*started)
		v.SetLines(settledLine(now))
		if len(*started) != n+1 {
			t.Fatalf("the row got shorter (%q -> %q) and the move was missed", ro[len(ro)-1], rn[len(ro)-1])
		}
		return
	}
	t.Skip("no width found where the word crosses the row end")
}

// A paragraph shorter than the anchor's probe cannot be aligned, but text
// that only continues it still moves. A narrow window, so one row holds
// fewer bytes than the probe — about ten Hangul at the default size.
func TestAShortParagraphStillMoves(t *testing.T) {
	started := manualScroll(t)
	v := NewCaptionView(config.Default())
	v.boxAlpha = 1
	win := test.NewWindow(v)
	t.Cleanup(win.Close)
	win.SetPadded(false)
	win.Resize(fyne.NewSize(330, 400))
	text := "안녕"
	for i := 0; i < 50 && len(*started) == 0; i++ {
		before := text
		text += " 하나"
		v.SetLines(settledLine(text))
		if len(*started) > 0 && len(before) >= alignProbe {
			t.Fatalf("setup: the paragraph before the move was %d bytes, not short", len(before))
		}
	}
	if len(*started) == 0 {
		t.Fatal("a short paragraph growing onto a second row did not move")
	}
}

// With nothing settled, text in progress drawn on rows of its own still moves.
func TestDimmedDraftAloneStillMoves(t *testing.T) {
	started := manualScroll(t)
	s := config.Default()
	s.DimDraft = true
	v, _ := viewOf(t, s)
	text := "Being said"
	for i := 0; i < 200 && len(*started) == 0; i++ {
		text += " on and on"
		v.SetLines([]captions.Line{{Text: text, Settled: false}})
	}
	if len(*started) == 0 {
		t.Fatal("a draft growing onto a new row did not move")
	}
}

// Rebuilds that are not new lines — the source line, the status, the ♪, a
// hint, the dot's pulse — leave a running move alone.
func TestOtherRepaintsLeaveTheMoveRunning(t *testing.T) {
	started := manualScroll(t)
	v, _ := viewOf(t, config.Default())
	growUntilMove(t, v, started, "Words.")
	a := (*started)[len(*started)-1]
	tick(a, 0.3)
	from, progress := v.scroll.from, v.scroll.progress
	v.SetSourceText("원문")
	v.SetStatus("status")
	v.SetStatus("")
	v.SetMusicGated(true)
	v.ShowHint("KO→EN", 0)
	v.SetIndicator(IndicatorBusy)
	v.Refresh()
	if v.scroll.anim != a || v.scroll.from != from || v.scroll.progress != progress {
		t.Errorf("the move was disturbed: from %v→%v progress %v→%v", from, v.scroll.from, progress, v.scroll.progress)
	}
	v.StopAnimations()
}

// Everything that should end a move at once does.
func TestMovesAreCancelled(t *testing.T) {
	cases := map[string]func(v *CaptionView, w fyne.Window){
		"empty lines": func(v *CaptionView, _ fyne.Window) { v.SetLines(nil) },
		"rewritten": func(v *CaptionView, _ fyne.Window) {
			v.SetLines(settledLine(strings.Repeat("Something else entirely. ", 6)))
		},
		"clear":      func(v *CaptionView, _ fyne.Window) { v.Clear() },
		"resize":     func(_ *CaptionView, w fyne.Window) { w.Resize(fyne.NewSize(700, 400)) },
		"font size":  func(v *CaptionView, _ fyne.Window) { s := v.settings; s.FontSize = 36; v.ApplySettings(s) },
		"dim draft":  func(v *CaptionView, _ fyne.Window) { s := v.settings; s.DimDraft = true; v.ApplySettings(s) },
		"smooth off": func(v *CaptionView, _ fyne.Window) { s := v.settings; s.SmoothScroll = false; v.ApplySettings(s) },
		// StopAnimations is the shutdown path and repaints nothing itself.
		"stop": func(v *CaptionView, _ fyne.Window) { v.StopAnimations(); v.Refresh() },
		"position": func(v *CaptionView, _ fyne.Window) {
			s := v.settings
			s.BoxPosition = string(config.BoxTop)
			v.ApplySettings(s)
		},
		"line count":    func(v *CaptionView, _ fyne.Window) { s := v.settings; s.MaxLines = 3; v.ApplySettings(s) },
		"box not fixed": func(v *CaptionView, _ fyne.Window) { s := v.settings; s.FixedBox = false; v.ApplySettings(s) },
	}
	for name, cancel := range cases {
		t.Run(name, func(t *testing.T) {
			started := manualScroll(t)
			v := NewCaptionView(config.Default())
			v.boxAlpha = 1
			win := test.NewWindow(v)
			t.Cleanup(win.Close)
			win.SetPadded(false)
			win.Resize(fyne.NewSize(900, 400))
			r := test.WidgetRenderer(v).(*captionRenderer)
			moveWithAGhost(t, v, started)
			tick((*started)[len(*started)-1], 0.2)
			if !hasGhost(r) {
				t.Fatal("setup: no leaving row is drawn before the cancel")
			}
			cancel(v, win)
			if v.scroll.offset() != 0 || len(v.scroll.ghosts) != 0 || v.scroll.anim != nil {
				t.Errorf("still moving: offset %v, %d ghosts", v.scroll.offset(), len(v.scroll.ghosts))
			}
			for _, s := range onScreen(r) {
				if s.ghost {
					t.Errorf("a leaving row is still drawn: %+v", s)
				}
			}
		})
	}
}

// Through the window, the two real ways the box empties — m, and clear on
// silence — leave no caption drawn, moving or not. Both arrive as SetLines
// with nothing in it, not as Clear.
func TestMuteAndClearOnSilenceLeaveNothingMoving(t *testing.T) {
	for _, way := range []string{"mute", "silence"} {
		t.Run(way, func(t *testing.T) {
			started := manualScroll(t)
			w := newTestWindow(t)
			w.win.Resize(fyne.NewSize(900, 400))
			r := test.WidgetRenderer(w.view).(*captionRenderer)
			for i := 0; i < 400 && len(*started) == 0; i++ {
				w.onTargetText(" word" + string(rune('a'+i%26)))
				w.buffer.Tick(nowSeconds(), captions.SpeechActive)
				w.pushLines()
			}
			if len(*started) == 0 {
				t.Fatal("no move")
			}
			tick((*started)[len(*started)-1], 0.2)
			switch way {
			case "mute":
				w.ToggleMute()
			case "silence":
				if !w.buffer.Tick(nowSeconds()+60, captions.SpeechQuiet) {
					t.Fatal("the buffer did not clear")
				}
				w.pushLines()
			}
			if got := onScreen(r); len(got) != 0 {
				t.Errorf("drawn after %s: %+v", way, got)
			}
		})
	}
}

// The clip is the box, and every row — staying or leaving — is inside it.
func TestRowsAreDrawnInsideTheBoxClip(t *testing.T) {
	started := manualScroll(t)
	v, r := viewOf(t, config.Default())
	growUntilMove(t, v, started, "Words.")
	if r.clip.Position() != r.box.Position() || r.clip.Size() != r.box.Size() {
		t.Errorf("clip %v %v, box %v %v", r.clip.Position(), r.clip.Size(), r.box.Position(), r.box.Size())
	}
	if r.clip.Content != r.layer {
		t.Fatal("the clip does not hold the row layer")
	}
	in := map[fyne.CanvasObject]bool{}
	for _, o := range r.layer.Objects {
		in[o] = true
	}
	for _, tx := range append(append([]*canvas.Text{}, r.texts...), r.ghosts...) {
		if !in[tx] {
			t.Errorf("row %q is outside the clip", tx.Text)
		}
	}
	for _, o := range r.Objects() {
		if _, isText := o.(*canvas.Text); isText && o != r.sourceLine && o != r.status && o != r.hint && o != r.musicNote {
			t.Errorf("a caption row is drawn outside the clip")
		}
	}
}

// A leaving row takes only a few colours on its way out. Fyne's GL painter
// keys a text texture on its colour and keeps it a minute, so a fade in
// continuous alpha made a new full-row texture every frame (measured: 62 MB
// of graphics memory became 303 MB).
func TestALeavingRowTakesFewColours(t *testing.T) {
	started := manualScroll(t)
	v, r := viewOf(t, config.Default())
	moveWithAGhost(t, v, started)
	a := (*started)[len(*started)-1]
	colours := map[uint8]bool{}
	for i := 0; i <= 100; i++ {
		tick(a, float32(i)/100)
		for _, s := range onScreen(r) {
			if s.ghost {
				colours[s.alpha] = true
			}
		}
	}
	// A row faded to nothing is hidden, not drawn at alpha 0: Fyne would
	// still make a texture for it.
	if colours[0] {
		t.Error("a leaving row was drawn at alpha 0 instead of hidden")
	}
	if len(colours) < 2 {
		t.Fatalf("setup: the leaving row did not fade (%v)", colours)
	}
	// A literal, not ghostFadeSteps+1: a bound built from the constant only
	// checks the constant against itself.
	if len(colours) > 4 {
		t.Errorf("the leaving row was drawn in %d alphas, want at most 4: %v", len(colours), colours)
	}
}

// A leaving row keeps the colour it had: a dimmed draft stays dimmed, and the
// box's own fade applies to it like to every other row.
func TestALeavingRowKeepsItsColour(t *testing.T) {
	manualScroll(t)
	s := config.Default()
	s.DimDraft = true
	v, r := viewOf(t, s)
	text := "Being said"
	for i := 0; i < 400 && !hasGhost(r); i++ {
		text += " w" + strconv.Itoa(i)
		v.SetLines([]captions.Line{{Text: text, Settled: false}})
	}
	if !hasGhost(r) {
		t.Fatal("setup: no draft row left the box")
	}
	for _, g := range onScreen(r) {
		if g.ghost && g.alpha > 141 { // 55% of full
			t.Errorf("a leaving draft row drawn at alpha %d, brighter than a draft", g.alpha)
		}
	}
	v.boxAlpha = 0.5
	v.Refresh()
	for _, g := range onScreen(r) {
		if g.ghost && g.alpha > 71 { // half of 55%
			t.Errorf("a leaving row ignores the box's fade: alpha %d", g.alpha)
		}
	}
}

func hasGhost(r *captionRenderer) bool {
	for _, s := range onScreen(r) {
		if s.ghost && s.alpha > 0 {
			return true
		}
	}
	return false
}

// moveWithAGhost fills the box and then starts a move in which a row leaves,
// so the leaving-row paths are really exercised.
func moveWithAGhost(t *testing.T, v *CaptionView, started *[]*fyne.Animation) {
	t.Helper()
	text := "Words."
	for i := 0; i < 20; i++ {
		_, text = growUntilMove(t, v, started, text)
		if len(v.scroll.ghosts) > 0 {
			return
		}
		finishAll(*started)
	}
	t.Fatal("setup: no row ever left the box")
}

// Rows rewritten so that the tracked row's old offset happens to begin a row
// again — same word lengths, other words — are not a move: the wrap did not
// carry anything over.
func TestARewriteWithTheSameShapeDoesNotMove(t *testing.T) {
	started := manualScroll(t)
	v, _ := viewOf(t, config.Default())
	old := strings.Repeat("abcd efgh ", 10)
	v.SetLines(settledLine(old))
	finishAll(*started)
	n := len(*started)
	// The same letters reversed, so the words measure the same and the rows
	// break at the same offsets; only the text differs.
	v.SetLines(settledLine(strings.Repeat("dcba hgfe ", 14)))
	if v.anchor.carried {
		t.Fatal("setup: the wrap carried rows over")
	}
	if len(*started) != n {
		t.Error("rewritten rows were animated as a move")
	}
}

// A lone draft rewritten so that a row starts at the tracked offset again is
// not a move: the new draft does not continue the old one.
func TestARewrittenDraftDoesNotMove(t *testing.T) {
	started := manualScroll(t)
	s := config.Default()
	s.DimDraft = true
	v, _ := viewOf(t, s)
	v.SetLines([]captions.Line{{Text: strings.Repeat("abcd efgh ", 10), Settled: false}})
	if v.scroll.kind != trackDraft {
		t.Fatal("setup: the draft is not tracked")
	}
	n := len(*started)
	v.SetLines([]captions.Line{{Text: strings.Repeat("dcba hgfe ", 14), Settled: false}})
	if len(*started) != n {
		t.Error("a rewritten draft was animated as a move")
	}
}

// In DimDraft mode a draft can settle — folding into the paragraph, rows
// rewritten or moving down — while a move is running. That ends the move:
// left running, its leaving row was drawn beside the same text back on
// screen. Fed a session with partial ticks between fragments, no row is ever
// drawn twice, in either drawing.
func TestNoRowIsDrawnTwice(t *testing.T) {
	for _, dim := range []bool{true, false} {
		for _, rows := range []int{1, 2, 3} {
			t.Run(strconv.FormatBool(dim)+strconv.Itoa(rows), func(t *testing.T) {
				started := manualScroll(t)
				s := config.Default()
				s.MaxLines, s.DimDraft = rows, dim
				v, r := viewOf(t, s)
				b := captions.New(captions.Options{
					MaxLines: rows, Policy: captions.PolicyIncremental, DraftInPlace: !dim,
				})
				now := 0.0
				moves := 0
				for i := 0; i < 600; i++ {
					frag := " w" + strconv.Itoa(i)
					if i%7 == 6 {
						frag += "."
					}
					b.Feed(frag, now)
					// Every few fragments let the idle timer settle a sentence.
					if i%5 == 4 {
						now += captions.IdleCommitSeconds + 0.1
					} else {
						now += 0.3
					}
					b.Tick(now, captions.SpeechActive)
					n := len(*started)
					v.SetLines(b.DisplayLines())
					if len(*started) > n {
						moves++
						tick((*started)[len(*started)-1], 0.3)
					}
					seen := map[string]bool{}
					for _, x := range onScreen(r) {
						if x.alpha == 0 {
							continue
						}
						for _, w := range strings.Fields(x.text) {
							if seen[w] {
								t.Fatalf("fragment %d: %q drawn twice: %+v", i, w, onScreen(r))
							}
							seen[w] = true
						}
					}
				}
				if moves == 0 {
					t.Fatal("setup: nothing moved")
				}
			})
		}
	}
}

// With the real fyne.Animation: the duration and curve are the ones meant,
// and the move ends clean. Fyne's test driver ticks it to the end in Start.
func TestTheRealAnimationEndsClean(t *testing.T) {
	var got *fyne.Animation
	old := startScroll
	startScroll = func(a *fyne.Animation) { got = a; a.Start() }
	t.Cleanup(func() { startScroll = old })
	v, r := viewOf(t, config.Default())
	text := "Words."
	for i := 0; i < 400 && got == nil; i++ {
		text += " word"
		v.SetLines(settledLine(text))
	}
	if got == nil {
		t.Fatal("no move")
	}
	if got.Duration != ScrollDuration || got.Curve == nil || got.Curve(0.25) != fyne.AnimationEaseOut(0.25) {
		t.Errorf("animation %v, curve(0.25) %v, want %v ease-out", got.Duration, got.Curve(0.25), ScrollDuration)
	}
	if v.scroll.anim != nil || v.scroll.offset() != 0 || len(v.scroll.ghosts) != 0 {
		t.Errorf("not clean after the last frame: %+v", v.scroll)
	}
	for _, s := range onScreen(r) {
		if s.ghost {
			t.Errorf("leaving row drawn at rest: %+v", s)
		}
	}
}

// Smooth scrolling is on by default, and an older settings file, which has no
// smooth_scroll, reads as on.
func TestSmoothScrollIsOnByDefaultAndForOlderFiles(t *testing.T) {
	if !config.Default().SmoothScroll {
		t.Error("off by default")
	}
	dir := t.TempDir()
	t.Cleanup(config.SetSettingsDirForTests(dir))
	if err := os.WriteFile(config.Path(), []byte(`{"max_lines": 3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := config.Load()
	if loaded.MaxLines != 3 {
		t.Fatalf("setup: the file was not read: %+v", loaded)
	}
	if !loaded.SmoothScroll {
		t.Error("an older settings file reads as off")
	}
	loaded.SmoothScroll = false
	if err := config.Save(loaded); err != nil {
		t.Fatal(err)
	}
	if config.Load().SmoothScroll {
		t.Error("turning it off did not survive a save and a load")
	}
}

// The Captions tab carries the check, and it reaches the settings.
func TestPreferencesCarrySmoothScroll(t *testing.T) {
	w := newTestWindow(t)
	p := openLivePrefs(t, w)
	if p.smoothScroll == nil || !p.smoothScroll.Checked {
		t.Fatal("the check is missing or does not show the setting")
	}
	p.smoothScroll.SetChecked(false)
	if p.collect().SmoothScroll {
		t.Error("unchecking it does not reach the settings")
	}
}

// Off, nothing ever moves.
func TestSmoothScrollOffNeverAnimates(t *testing.T) {
	started := manualScroll(t)
	s := config.Default()
	s.SmoothScroll = false
	v, _ := viewOf(t, s)
	text := "Words."
	for i := 0; i < 200; i++ {
		text += " word"
		v.SetLines(settledLine(text))
		if v.scroll.offset() != 0 {
			t.Fatal("an offset with smooth scrolling off")
		}
	}
	if len(*started) != 0 {
		t.Errorf("%d animations started with smooth scrolling off", len(*started))
	}
}

// Across a recorded session fed fragment by fragment, at rest every row is
// where the unanimated drawing puts it, in both drawings — and moves were
// actually detected, as many as an independent count finds, or the equality
// would pass with detection dead.
func TestTheRecordedSessionAtRestMatchesNoAnimation(t *testing.T) {
	for _, dim := range []bool{false, true} {
		for _, rows := range []int{1, 2, 3} {
			name := "inplace"
			if dim {
				name = "dim"
			}
			t.Run(name+string(rune('0'+rows)), func(t *testing.T) {
				started := manualScroll(t)
				s := config.Default()
				s.MaxLines, s.DimDraft = rows, dim
				off := s
				off.SmoothScroll = false
				v, r := viewOf(t, s)
				ov, or := viewOf(t, off)
				b := captions.New(captions.Options{
					MaxLines: rows, Policy: captions.PolicyIncremental, DraftInPlace: !dim,
				})
				var prev []shown
				oracle := 0
				for i, frag := range spoken {
					b.Feed(frag, float64(i))
					b.Tick(float64(i)+0.01, captions.SpeechActive)
					lines := b.DisplayLines()
					v.SetLines(lines)
					finishAll(*started)
					ov.SetLines(lines)
					got, want := onScreen(r), onScreen(or)
					if !sameShown(got, want) {
						t.Fatalf("fragment %d at rest:\n got %+v\nwant %+v", i, got, want)
					}
					if moved(prev, want) {
						oracle++
					}
					prev = want
				}
				if len(*started) == 0 {
					t.Fatal("no move detected in the whole session")
				}
				if len(*started) != oracle {
					t.Errorf("%d moves detected, an independent count finds %d", len(*started), oracle)
				}
			})
		}
	}
}

// moved is the independent count: the rows moved up if the last settled row
// of the previous repaint, or the row that grew out of it, is now drawn
// higher — or, when it has left a one-row box, if the box holds what comes
// after it. Exact for whole words, which the recorded fixture is.
func moved(prev, now []shown) bool {
	var last *shown
	for i := range prev {
		last = &prev[i]
	}
	if last == nil || len(now) == 0 {
		return false
	}
	for i := len(now) - 1; i >= 0; i-- {
		if strings.HasPrefix(now[i].text, last.text) {
			return now[i].y < last.y-0.5
		}
	}
	// It left a one-row box: a move if the row on screen is new text. Not
	// when a dimmed draft (DimDraft) has just settled into the paragraph —
	// that rewrites the row rather than moving it, and is drawn at once.
	folded := last.alpha < 255 && now[0].alpha == 255
	return len(prev) == 1 && len(now) == 1 && now[0].y == last.y && !folded
}

// The CPU cost of one frame of a move in a four-row box, against a repaint
// that re-wraps — what every frame cost before the wrap was cached. Both are
// a microsecond or two on the Mac this was written on. What a frame costs the
// GPU is not in it: that is the leaving row's textures, which
// ghostFadeSteps bounds and TestALeavingRowTakesFewColours pins.
func BenchmarkAScrollFrame(b *testing.B) {
	old := startScroll
	var started []*fyne.Animation
	startScroll = func(a *fyne.Animation) { started = append(started, a) }
	defer func() { startScroll = old }()
	s := config.Default()
	s.MaxLines = 4
	v := NewCaptionView(s)
	v.boxAlpha = 1
	win := test.NewWindow(v)
	defer win.Close()
	win.SetPadded(false)
	win.Resize(fyne.NewSize(1280, 720))
	text := "Benchmark."
	for len(started) < 6 {
		text += " w" + strconv.Itoa(len(text))
		v.SetLines(settledLine(text))
	}
	a := started[len(started)-1]
	b.Run("frame", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			tick(a, float32(i%100)/100)
		}
	})
	b.Run("rewrap", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			v.linesVersion++
			v.Refresh()
		}
	})
}
