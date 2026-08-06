package ui

import (
	"strings"
	"unicode"

	"fyne.io/fyne/v2"

	"github.com/dennis2lee/live-translation/src_go/internal/config"
)

// Caption box metrics. These are the numbers the Python build paints with, kept
// as pure functions so they can be tested without a display.
const (
	PadH = 32.0
	PadV = 24.0

	// SourceGap is the space between the box and the small grey source line.
	SourceGap       = 14.0
	SourceFontRatio = 0.42

	// The direction hint sits at the bottom-right of the caption box.
	HintGap       = 12.0
	HintFontRatio = 0.42

	// The status indicator sits at the bottom-right, beside the hint.
	DotRadiusRatio = 0.13
	DotMinRadius   = 5.0
	DotGap         = 14.0
	DotDimAlpha    = 0.15

	StatusFontRatio = 0.55
)

// LineHeight is the height of one caption line at the given font size.
//
// Fyne measures a string rather than exposing font metrics, so a reference
// string with an ascender and a descender stands in for them. Using the caption
// text itself would make the line height jitter as the text changed.
func LineHeight(fontSize float32) float32 {
	return fyne.MeasureText("Ag영", fontSize, fyne.TextStyle{}).Height
}

// NominalBoxHeight is the height of a full box, for callers that place things
// before the text has been wrapped.
func NominalBoxHeight(s config.Settings) float32 {
	lines := s.MaxLines
	if lines < 1 {
		lines = 1
	}
	return LineHeight(float32(s.FontSize))*float32(lines) + 2*PadV
}

// BoxBottom is the bottom edge of the caption box, wherever it is anchored.
//
// Anchoring to the bottom needs no height, which is why the original
// bottom-only layout never passed one. Top and middle do, so callers that place
// the hint and the dot fall back to a nominal full-height box.
func BoxBottom(s config.Settings, viewHeight, boxHeight float32) float32 {
	margin := viewHeight * float32(s.BottomMarginPct) / 100.0
	switch s.Position() {
	case config.BoxBottom:
		return viewHeight - margin
	case config.BoxTop:
		return margin + boxHeight
	default: // middle
		return (viewHeight + boxHeight) / 2.0
	}
}

// AnchorRight is the right edge of the caption box.
//
// Fixed even when the box hugs its content, so the indicator does not drift
// sideways as the text length changes.
func AnchorRight(s config.Settings, viewWidth float32) float32 {
	maxBoxW := viewWidth * float32(s.BoxMaxWidthPct) / 100.0
	return (viewWidth + maxBoxW) / 2.0
}

// DotRadius scales the status dot with the caption text.
func DotRadius(s config.Settings) float32 {
	r := float32(s.FontSize) * DotRadiusRatio
	if r < DotMinRadius {
		return DotMinRadius
	}
	return r
}

// Wrap breaks text to maxWidth, returning the pieces.
//
// Latin breaks at spaces. Korean has none, so a run that does not fit is broken
// between runes — without that, one long Korean sentence would be a single
// unbreakable line wider than the box.
func Wrap(text string, fontSize, maxWidth float32) []string {
	text = strings.TrimSpace(text)
	if text == "" || maxWidth <= 0 {
		return nil
	}
	if measure(text, fontSize) <= maxWidth {
		return []string{text}
	}

	var out []string
	var line strings.Builder

	flush := func() {
		if piece := strings.TrimSpace(line.String()); piece != "" {
			out = append(out, piece)
		}
		line.Reset()
	}

	for _, word := range splitKeepingSpaces(text) {
		candidate := line.String() + word
		if measure(strings.TrimSpace(candidate), fontSize) <= maxWidth {
			line.WriteString(word)
			continue
		}
		// The word does not fit on this line.
		if strings.TrimSpace(line.String()) != "" {
			flush()
		}
		if measure(strings.TrimSpace(word), fontSize) <= maxWidth {
			line.WriteString(strings.TrimLeft(word, " "))
			continue
		}
		// Nor on a line of its own — break it between runes.
		for _, r := range word {
			next := line.String() + string(r)
			if measure(strings.TrimSpace(next), fontSize) > maxWidth &&
				strings.TrimSpace(line.String()) != "" {
				flush()
			}
			line.WriteRune(r)
		}
	}
	flush()
	return out
}

// splitKeepingSpaces splits into words, keeping each word's leading space so a
// rejoined line reads correctly.
func splitKeepingSpaces(text string) []string {
	var out []string
	var current strings.Builder
	for _, r := range text {
		if unicode.IsSpace(r) && current.Len() > 0 {
			out = append(out, current.String())
			current.Reset()
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}

func measure(text string, fontSize float32) float32 {
	if text == "" {
		return 0
	}
	return fyne.MeasureText(text, fontSize, fyne.TextStyle{}).Width
}

// Elide shortens text from the left until it fits, prefixing an ellipsis. The
// source line is elided from the left because its end is the newest speech.
func Elide(text string, fontSize, maxWidth float32) string {
	if measure(text, fontSize) <= maxWidth {
		return text
	}
	runes := []rune(text)
	for i := 1; i < len(runes); i++ {
		candidate := "…" + string(runes[i:])
		if measure(candidate, fontSize) <= maxWidth {
			return candidate
		}
	}
	return "…"
}
