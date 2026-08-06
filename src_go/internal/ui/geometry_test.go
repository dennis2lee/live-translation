package ui

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/dennis2lee/live-translation/src_go/internal/config"
)

func TestMain(m *testing.M) {
	// Text measurement needs a driver, and the test driver needs no display.
	test.NewApp()
	m.Run()
}

func TestBoxBottomPerPosition(t *testing.T) {
	s := config.Default()
	s.BottomMarginPct = 10
	const viewH, boxH = 1000, 200

	s.BoxPosition = string(config.BoxBottom)
	if got := BoxBottom(s, viewH, boxH); got != 900 {
		t.Errorf("bottom anchor = %v, want 900", got)
	}
	s.BoxPosition = string(config.BoxTop)
	if got := BoxBottom(s, viewH, boxH); got != 300 {
		t.Errorf("top anchor = %v, want margin+height", got)
	}
	s.BoxPosition = string(config.BoxCenter)
	if got := BoxBottom(s, viewH, boxH); got != 600 {
		t.Errorf("centre anchor = %v, want 600", got)
	}
}

// Anchoring to the bottom needs no height, which is what lets callers place the
// hint and the dot before the text is wrapped.
func TestBottomAnchorIgnoresBoxHeight(t *testing.T) {
	s := config.Default()
	s.BoxPosition = string(config.BoxBottom)
	if BoxBottom(s, 1000, 0) != BoxBottom(s, 1000, 500) {
		t.Error("the bottom anchor moved with the box height")
	}
}

// The dot must not drift sideways as the caption text changes length.
func TestAnchorRightIsFixed(t *testing.T) {
	s := config.Default()
	s.BoxMaxWidthPct = 90
	if got := AnchorRight(s, 1000); got != 950 {
		t.Errorf("AnchorRight = %v, want 950", got)
	}
}

func TestDotRadiusHasAFloor(t *testing.T) {
	s := config.Default()
	s.FontSize = 24
	if got := DotRadius(s); got < DotMinRadius {
		t.Errorf("DotRadius = %v, below the floor", got)
	}
	s.FontSize = 96
	if got := DotRadius(s); got <= DotMinRadius {
		t.Errorf("DotRadius = %v, should scale with the font", got)
	}
}

func TestNominalBoxHeightGrowsWithLines(t *testing.T) {
	s := config.Default()
	s.MaxLines = 1
	one := NominalBoxHeight(s)
	s.MaxLines = 4
	four := NominalBoxHeight(s)
	if four <= one {
		t.Errorf("four lines (%v) is not taller than one (%v)", four, one)
	}
	// The padding is counted once, not per line.
	if four >= one*4 {
		t.Errorf("height %v scaled the padding with the lines", four)
	}
}

func TestWrapKeepsShortTextWhole(t *testing.T) {
	got := Wrap("Revenue is up.", 44, 10000)
	if len(got) != 1 || got[0] != "Revenue is up." {
		t.Errorf("Wrap = %q", got)
	}
}

func TestWrapBreaksAtSpaces(t *testing.T) {
	text := "Revenue is up fourteen percent this quarter and the margin improved."
	got := Wrap(text, 44, 300)
	if len(got) < 2 {
		t.Fatalf("Wrap produced %d line(s), want several", len(got))
	}
	for _, line := range got {
		if measure(line, 44) > 300 {
			t.Errorf("line %q is wider than the box", line)
		}
	}
	// Nothing may be dropped.
	joined := strings.Join(got, " ")
	if strings.ReplaceAll(joined, "  ", " ") != text {
		t.Errorf("Wrap changed the text:\n got %q\nwant %q", joined, text)
	}
}

// Korean has no spaces, so a long sentence must break between runes rather than
// becoming one unbreakable line wider than the box.
func TestWrapBreaksKoreanWithoutSpaces(t *testing.T) {
	text := strings.Repeat("영업이익률이올랐습니다", 6)
	got := Wrap(text, 44, 300)
	if len(got) < 2 {
		t.Fatalf("Korean did not wrap: %d line(s)", len(got))
	}
	for _, line := range got {
		if measure(line, 44) > 300 {
			t.Errorf("line %q is wider than the box", line)
		}
	}
	if strings.Join(got, "") != text {
		t.Error("wrapping Korean lost or duplicated characters")
	}
}

func TestWrapHandlesNothing(t *testing.T) {
	if got := Wrap("", 44, 300); got != nil {
		t.Errorf("Wrap(\"\") = %q", got)
	}
	if got := Wrap("   ", 44, 300); got != nil {
		t.Errorf("Wrap(spaces) = %q", got)
	}
	if got := Wrap("text", 44, 0); got != nil {
		t.Errorf("Wrap with no width = %q", got)
	}
}

// The source line is elided from the left, because its end is the newest
// speech.
func TestElideKeepsTheEnd(t *testing.T) {
	text := "이것은 아주 긴 한국어 문장입니다 그리고 계속 이어집니다"
	got := Elide(text, 18, 100)
	if got == text {
		t.Fatal("nothing was elided")
	}
	if !strings.HasPrefix(got, "…") {
		t.Errorf("Elide = %q, want it shortened from the left", got)
	}
	if !strings.HasSuffix(text, strings.TrimPrefix(got, "…")) {
		t.Errorf("Elide = %q, want it to keep the end of the text", got)
	}
	if measure(got, 18) > 100 {
		t.Errorf("Elide = %q, still too wide", got)
	}
}

func TestElideLeavesShortTextAlone(t *testing.T) {
	if got := Elide("short", 18, 10000); got != "short" {
		t.Errorf("Elide = %q", got)
	}
}

func TestParseHex(t *testing.T) {
	fallback := colourIdle
	if got := parseHex("#1B4965", fallback); got.R != 0x1B || got.G != 0x49 || got.B != 0x65 {
		t.Errorf("parseHex = %v", got)
	}
	if got := parseHex("#ffffff", fallback); got.R != 0xFF {
		t.Errorf("lower case was rejected: %v", got)
	}
	for _, bad := range []string{"", "1B4965", "#12345", "#GGGGGG", "#1B49655"} {
		if got := parseHex(bad, fallback); got != fallback {
			t.Errorf("parseHex(%q) = %v, want the fallback", bad, got)
		}
	}
}
