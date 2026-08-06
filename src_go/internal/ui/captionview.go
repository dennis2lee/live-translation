package ui

import (
	"image/color"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"

	"github.com/dennis2lee/live-translation/src_go/internal/captions"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
)

// Indicator is the status dot at the bottom-right of the caption box.
//
// A dot rather than words: the spec asked for "connecting" and "reconnecting"
// text in the caption area, and text there competes with the captions. Errors
// additionally print their cause, because a red dot alone is not actionable.
type Indicator string

const (
	// IndicatorIdle is grey and steady — at launch and after stopping.
	IndicatorIdle Indicator = "idle"
	// IndicatorBusy is green and blinking — connecting or reconnecting.
	IndicatorBusy Indicator = "busy"
	// IndicatorLive is green and steady — connected.
	IndicatorLive Indicator = "live"
	// IndicatorError is red and steady.
	IndicatorError Indicator = "error"
)

var (
	colourLive   = color.NRGBA{R: 0x34, G: 0xC7, B: 0x59, A: 0xFF}
	colourIdle   = color.NRGBA{R: 0x6E, G: 0x6E, B: 0x6E, A: 0xFF}
	colourError  = color.NRGBA{R: 0xFF, G: 0x3B, B: 0x30, A: 0xFF}
	colourStatus = color.NRGBA{R: 0x6E, G: 0x6E, B: 0x6E, A: 0xFF}
	colourSource = color.NRGBA{R: 0x9A, G: 0x9A, B: 0x9A, A: 0xFF}
)

func dotColour(i Indicator) color.NRGBA {
	switch i {
	case IndicatorError:
		return colourError
	case IndicatorIdle:
		return colourIdle
	default:
		return colourLive
	}
}

const (
	fadeDuration     = 150 * time.Millisecond
	hintHold         = 2 * time.Second
	hintFadeDuration = 400 * time.Millisecond
	dotPulseDuration = 1200 * time.Millisecond
)

// CaptionView paints a solid background with a rounded caption box.
//
// Everything is drawn by us rather than assembled from widgets, for the same
// reason the Python build paints it: no stock widget can bottom-align text
// inside a rounded box clipped to N lines, with per-line alpha.
//
// All methods are called from the UI goroutine only.
type CaptionView struct {
	widget.BaseWidget

	settings config.Settings
	lines    []captions.Line
	source   string
	status   string

	boxAlpha  float32
	hintText  string
	hintAlpha float32
	indicator Indicator
	dotAlpha  float32

	boxFade  *fyne.Animation
	hintFade *fyne.Animation
	dotPulse *fyne.Animation
	hintWait *time.Timer
}

// NewCaptionView builds the view.
func NewCaptionView(settings config.Settings) *CaptionView {
	v := &CaptionView{
		settings:  settings,
		indicator: IndicatorIdle,
		dotAlpha:  1,
	}
	if settings.FixedBox {
		v.boxAlpha = 1
	}
	v.ExtendBaseWidget(v)
	return v
}

// CreateRenderer is the fyne.Widget contract.
func (v *CaptionView) CreateRenderer() fyne.WidgetRenderer {
	r := &captionRenderer{view: v}
	r.background = canvas.NewRectangle(color.Black)
	r.box = canvas.NewRectangle(color.Transparent)
	r.hint = canvas.NewText("", colourStatus)
	r.status = canvas.NewText("", colourStatus)
	r.sourceLine = canvas.NewText("", colourSource)
	r.dot = canvas.NewCircle(colourIdle)
	r.hint.Alignment = fyne.TextAlignTrailing
	r.status.Alignment = fyne.TextAlignCenter
	r.sourceLine.Alignment = fyne.TextAlignCenter
	return r
}

// --- inputs ---------------------------------------------------------------

// ApplySettings takes a settings change immediately, without a restart.
func (v *CaptionView) ApplySettings(s config.Settings) {
	v.settings = s
	v.refreshVisibility()
}

// SetLines replaces the caption text.
func (v *CaptionView) SetLines(lines []captions.Line) {
	v.lines = lines
	v.refreshVisibility()
}

// SetSourceText sets the small grey line of what was heard.
func (v *CaptionView) SetSourceText(text string) {
	v.source = text
	v.Refresh()
}

// SetStatus shows error text in the caption area rather than in a modal dialog.
// No modal dialogs while translating.
func (v *CaptionView) SetStatus(status string) {
	v.status = status
	v.refreshVisibility()
}

// Clear empties the display.
func (v *CaptionView) Clear() {
	v.lines = nil
	v.source = ""
	v.refreshVisibility()
}

// Indicator is the current dot state.
func (v *CaptionView) Indicator() Indicator { return v.indicator }

// SetIndicator blinks while connecting and holds steady otherwise.
func (v *CaptionView) SetIndicator(state Indicator) {
	if state == v.indicator {
		return
	}
	v.indicator = state
	if v.dotPulse != nil {
		v.dotPulse.Stop()
		v.dotPulse = nil
	}
	if state == IndicatorBusy {
		v.dotPulse = fyne.NewAnimation(dotPulseDuration, func(f float32) {
			// Down to dim and back, so the dot breathes rather than flicking.
			dim := float32(DotDimAlpha)
			if f <= 0.5 {
				v.dotAlpha = 1 - (1-dim)*(f*2)
			} else {
				v.dotAlpha = dim + (1-dim)*((f-0.5)*2)
			}
			v.Refresh()
		})
		v.dotPulse.RepeatCount = fyne.AnimationRepeatForever
		v.dotPulse.Curve = fyne.AnimationEaseInOut
		v.dotPulse.Start()
	} else {
		v.dotAlpha = 1
	}
	v.Refresh()
}

// ShowHint shows grey text below the caption box briefly, then fades it out.
func (v *CaptionView) ShowHint(text string, hold time.Duration) {
	if hold <= 0 {
		hold = hintHold
	}
	v.hintText = text
	v.stopHintAnimations()
	v.hintAlpha = 1
	v.hintWait = time.AfterFunc(hold, func() {
		fyne.Do(v.startHintFade)
	})
	v.Refresh()
}

// HideHint removes the hint at once.
func (v *CaptionView) HideHint() {
	v.stopHintAnimations()
	v.hintAlpha = 0
	v.hintText = ""
	v.Refresh()
}

func (v *CaptionView) stopHintAnimations() {
	if v.hintFade != nil {
		v.hintFade.Stop()
		v.hintFade = nil
	}
	if v.hintWait != nil {
		v.hintWait.Stop()
		v.hintWait = nil
	}
}

func (v *CaptionView) startHintFade() {
	from := v.hintAlpha
	v.hintFade = fyne.NewAnimation(hintFadeDuration, func(f float32) {
		v.hintAlpha = from * (1 - f)
		v.Refresh()
	})
	v.hintFade.Curve = fyne.AnimationEaseIn
	v.hintFade.Start()
}

// refreshVisibility fades the box in and out. In fixed mode it stays even when
// empty; otherwise it disappears as soon as there is nothing to show.
func (v *CaptionView) refreshVisibility() {
	target := float32(0)
	if len(v.lines) > 0 || v.settings.FixedBox {
		target = 1
	}
	if v.boxFade != nil {
		v.boxFade.Stop()
		v.boxFade = nil
	}
	if v.boxAlpha != target {
		from := v.boxAlpha
		v.boxFade = fyne.NewAnimation(fadeDuration, func(f float32) {
			v.boxAlpha = from + (target-from)*f
			v.Refresh()
		})
		v.boxFade.Curve = fyne.AnimationEaseInOut
		v.boxFade.Start()
	}
	v.Refresh()
}

// StopAnimations releases the timers, so shutdown does not leave one running.
func (v *CaptionView) StopAnimations() {
	v.stopHintAnimations()
	if v.boxFade != nil {
		v.boxFade.Stop()
		v.boxFade = nil
	}
	if v.dotPulse != nil {
		v.dotPulse.Stop()
		v.dotPulse = nil
	}
}

// --- renderer -------------------------------------------------------------

type captionRenderer struct {
	view *CaptionView

	background *canvas.Rectangle
	box        *canvas.Rectangle
	texts      []*canvas.Text
	hint       *canvas.Text
	status     *canvas.Text
	sourceLine *canvas.Text
	dot        *canvas.Circle
}

func (r *captionRenderer) Destroy() { r.view.StopAnimations() }

func (r *captionRenderer) MinSize() fyne.Size { return fyne.NewSize(320, 180) }

func (r *captionRenderer) Objects() []fyne.CanvasObject {
	out := []fyne.CanvasObject{r.background, r.box}
	for _, t := range r.texts {
		out = append(out, t)
	}
	return append(out, r.sourceLine, r.status, r.hint, r.dot)
}

func (r *captionRenderer) Layout(size fyne.Size) { r.rebuild(size) }

func (r *captionRenderer) Refresh() { r.rebuild(r.view.Size()) }

// rebuild positions everything. It is the equivalent of the Python paintEvent,
// and follows the same order: background, box, text, source line, hint, dot.
func (r *captionRenderer) rebuild(size fyne.Size) {
	v := r.view
	s := v.settings
	if size.Width <= 0 || size.Height <= 0 {
		return
	}

	r.background.FillColor = parseHex(s.BgColor, color.NRGBA{A: 0xFF})
	r.background.Resize(size)
	r.background.Move(fyne.NewPos(0, 0))
	r.background.Refresh()

	fontSize := float32(s.FontSize)
	lineH := LineHeight(fontSize)
	maxBoxW := size.Width * float32(s.BoxMaxWidthPct) / 100.0
	maxTextW := maxBoxW - 2*PadH
	if maxTextW < 40 {
		maxTextW = 40
	}

	// Wrap to the real width, then drop from the front if it exceeds max_lines.
	type wrappedLine struct {
		text    string
		settled bool
	}
	var wrapped []wrappedLine
	for _, line := range v.lines {
		for _, piece := range Wrap(line.Text, fontSize, maxTextW) {
			wrapped = append(wrapped, wrappedLine{piece, line.Settled})
		}
	}
	maxLines := s.MaxLines
	if maxLines < 1 {
		maxLines = 1
	}
	if len(wrapped) > maxLines {
		wrapped = wrapped[len(wrapped)-maxLines:]
	}

	visible := v.boxAlpha > 0.001 && (len(wrapped) > 0 || s.FixedBox)

	var boxW, boxH float32
	if s.FixedBox {
		// Fixed width and height, so the box never jitters with content.
		boxW = maxBoxW
		boxH = lineH*float32(maxLines) + 2*PadV
	} else {
		var textW float32
		for _, w := range wrapped {
			if got := measure(w.text, fontSize); got > textW {
				textW = got
			}
		}
		boxW = min(maxBoxW, textW+2*PadH)
		boxH = lineH*float32(len(wrapped)) + 2*PadV // sized to the real line count
	}

	bottom := BoxBottom(s, size.Height, boxH)
	boxLeft := (size.Width - boxW) / 2
	boxTop := bottom - boxH

	// the box
	if visible {
		boxColour := parseHex(s.BoxColor, color.NRGBA{R: 0x1B, G: 0x49, B: 0x65, A: 0xFF})
		boxColour.A = uint8(clamp01(float32(s.BoxOpacity)) * v.boxAlpha * 255)
		r.box.FillColor = boxColour
		r.box.CornerRadius = float32(s.CornerRadius)
		r.box.Resize(fyne.NewSize(boxW, boxH))
		r.box.Move(fyne.NewPos(boxLeft, boxTop))
		r.box.Show()
	} else {
		r.box.Hide()
	}
	r.box.Refresh()

	// the text
	r.ensureTexts(len(wrapped))
	textColour := parseHex(s.TextColor, color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF})
	// Bottom-aligned in fixed mode, so the newest line never moves.
	y := boxTop + PadV
	if s.FixedBox {
		y = boxTop + boxH - PadV - lineH*float32(len(wrapped))
	}
	for i, t := range r.texts {
		if i >= len(wrapped) || !visible {
			t.Hide()
			t.Refresh()
			continue
		}
		line := wrapped[i]
		c := textColour
		alpha := v.boxAlpha
		if !line.settled {
			alpha *= 0.55 // unsettled text at 55% of the settled colour
		}
		c.A = uint8(clamp01(alpha) * 255)
		t.Text = line.text
		t.Color = c
		t.TextSize = fontSize
		t.TextStyle = fyne.TextStyle{}
		t.Alignment = fyne.TextAlignCenter
		t.Resize(fyne.NewSize(boxW-2*PadH, lineH))
		t.Move(fyne.NewPos(boxLeft+PadH, y+float32(i)*lineH))
		t.Show()
		t.Refresh()
	}

	// An empty fixed box carries the status text inside it; without a box the
	// status is drawn where the box would have been.
	r.status.Text = ""
	if v.status != "" && len(wrapped) == 0 {
		statusSize := max(18, fontSize*StatusFontRatio)
		r.status.Text = v.status
		r.status.TextSize = statusSize
		c := colourStatus
		if s.FixedBox {
			c.A = uint8(clamp01(v.boxAlpha) * 255)
		}
		r.status.Color = c
		r.status.Resize(fyne.NewSize(boxW, LineHeight(statusSize)))
		r.status.Move(fyne.NewPos(boxLeft, boxTop+(boxH-LineHeight(statusSize))/2))
		r.status.Show()
	} else {
		r.status.Hide()
	}
	r.status.Refresh()

	// Source text — one small grey line outside the box, on the background.
	if s.ShowSourceText && v.source != "" && visible {
		srcSize := max(14, fontSize*SourceFontRatio)
		r.sourceLine.Text = Elide(v.source, srcSize, maxBoxW)
		r.sourceLine.TextSize = srcSize
		c := colourSource
		c.A = uint8(clamp01(v.boxAlpha) * 255)
		r.sourceLine.Color = c
		h := LineHeight(srcSize)
		r.sourceLine.Resize(fyne.NewSize(maxBoxW, h))
		r.sourceLine.Move(fyne.NewPos((size.Width-maxBoxW)/2, boxTop-SourceGap-h))
		r.sourceLine.Show()
	} else {
		r.sourceLine.Hide()
	}
	r.sourceLine.Refresh()

	// The strip below the box carries the hint and the dot, whether or not
	// there are captions.
	radius := DotRadius(s)
	nominalBottom := BoxBottom(s, size.Height, NominalBoxHeight(s))
	stripTop := min(nominalBottom+HintGap, size.Height-radius*2)
	anchorRight := AnchorRight(s, size.Width)

	dotColor := dotColour(v.indicator)
	dotColor.A = uint8(clamp01(v.dotAlpha) * 255)
	r.dot.FillColor = dotColor
	r.dot.Resize(fyne.NewSize(radius*2, radius*2))
	r.dot.Move(fyne.NewPos(anchorRight-radius*2, stripTop))
	r.dot.Refresh()

	if v.hintAlpha > 0.001 && v.hintText != "" {
		hintSize := max(16, fontSize*HintFontRatio)
		h := LineHeight(hintSize)
		c := colourStatus
		c.A = uint8(clamp01(v.hintAlpha) * 255)
		r.hint.Text = v.hintText
		r.hint.TextSize = hintSize
		r.hint.Color = c
		// Right-aligned to the left of the indicator, so the two never overlap.
		right := anchorRight - radius*2 - DotGap
		r.hint.Resize(fyne.NewSize(right, h))
		r.hint.Move(fyne.NewPos(0, stripTop+radius-h/2))
		r.hint.Show()
	} else {
		r.hint.Hide()
	}
	r.hint.Refresh()
}

func (r *captionRenderer) ensureTexts(n int) {
	for len(r.texts) < n {
		t := canvas.NewText("", color.White)
		t.Alignment = fyne.TextAlignCenter
		r.texts = append(r.texts, t)
	}
}

// parseHex reads "#RRGGBB". Anything else falls back, the same contract
// config.Clamped has.
func parseHex(s string, fallback color.NRGBA) color.NRGBA {
	s = strings.TrimSpace(s)
	if len(s) != 7 || !strings.HasPrefix(s, "#") {
		return fallback
	}
	var out color.NRGBA
	out.A = 0xFF
	for i, shift := range []uint{0, 1, 2} {
		hi, ok1 := hexDigit(s[1+i*2])
		lo, ok2 := hexDigit(s[2+i*2])
		if !ok1 || !ok2 {
			return fallback
		}
		v := hi<<4 | lo
		switch shift {
		case 0:
			out.R = v
		case 1:
			out.G = v
		case 2:
			out.B = v
		}
	}
	return out
}

func hexDigit(b byte) (uint8, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	}
	return 0, false
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
