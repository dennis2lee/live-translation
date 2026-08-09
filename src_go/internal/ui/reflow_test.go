package ui

import (
	"image"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/dennis2lee/live-translation/src_go/internal/captions"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
)

// defaultRowWidth is the text width of the default layout — a 1280 px window,
// the box at 90%, minus the padding — which is what the symptom was reported
// against.
const defaultRowWidth = 1280*0.9 - 2*PadH

// A three-row box must fill its rows when the session has said enough to fill
// them.
//
// Reported from live use: with three caption lines configured, the bottom row
// often carried the only text and the two rows above it were blank. The
// renderer reflows settled sentences into one paragraph (so rows pack full),
// but the buffer still capped its settled history at MaxLines *sentences* —
// the Python build's rule, where one sentence was one row. Three short
// sentences joined into a single row, and there was nothing left to put in
// the other two.
func TestShortSentencesFillAThreeRowBox(t *testing.T) {
	b := captions.New(captions.Options{
		MaxLines:            3,
		Policy:              captions.PolicyIncremental,
		CompletePhrasesOnly: true,
	})

	// Short settled lines, the shape the 67-minute transcript measured: 56
	// lines of six characters or fewer. Enough of them to fill three rows —
	// the speaker has been going for half a minute.
	short := []string{
		"Love.", "Lord.", "My soul.", "Arise.", "Sing.", "Praise.",
		"Amen.", "He is good.", "Rejoice.", "Give thanks.", "Hallelujah.", "Glory.",
		"Holy.", "Mercy.", "Grace.", "Peace.", "Joy.", "Hope.",
		"Faith.", "Light.", "Truth.", "Life.", "The King.", "Forever.",
	}
	now := 0.0
	for _, line := range short {
		b.Feed(line, now)
		now += 1.0
		b.Tick(now, captions.SpeechUnknown)
	}
	// Let the pacing release everything.
	for i := 0; i < 20; i++ {
		now += 1.0
		b.Tick(now, captions.SpeechActive) // active input, so nothing clears
	}
	b.DrainNewCommits()

	rows := reflowRows(b.DisplayLines(), 44, defaultRowWidth, 3)
	if len(rows) != 3 {
		t.Fatalf("the box shows %d of its 3 rows: %q", len(rows), rowTexts(rows))
	}
	// And the newest text is still the bottom row.
	if !strings.Contains(rows[len(rows)-1].text, "Forever.") {
		t.Errorf("the newest sentence is not on the bottom row: %q", rowTexts(rows))
	}
}

// One long sentence still fills from the bottom and never overflows the cap.
func TestReflowKeepsTheNewestRows(t *testing.T) {
	lines := []captions.Line{
		{Text: strings.Repeat("A long settled sentence that wraps and wraps. ", 6), Settled: true},
		{Text: "The newest short one.", Settled: true},
	}
	rows := reflowRows(lines, 44, defaultRowWidth, 3)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want exactly the cap", len(rows))
	}
	if !strings.Contains(rows[2].text, "short one.") {
		t.Errorf("the newest text fell off instead of the oldest: %q", rowTexts(rows))
	}
}

// Unsettled text keeps its own dimmed rows, after the settled paragraph.
func TestReflowKeepsPendingSeparateAndLast(t *testing.T) {
	lines := []captions.Line{
		{Text: "A settled sentence.", Settled: true},
		{Text: "still being revised", Settled: false},
	}
	rows := reflowRows(lines, 44, defaultRowWidth, 3)
	if len(rows) != 2 {
		t.Fatalf("rows = %q", rowTexts(rows))
	}
	if rows[0].settled != true || rows[1].settled != false {
		t.Errorf("settled/pending order lost: %+v", rows)
	}
	if rows[0].text == rows[1].text || strings.Contains(rows[0].text, "revised") {
		t.Errorf("pending text was merged into the settled paragraph: %q", rowTexts(rows))
	}
}

func rowTexts(rows []row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.text
	}
	return out
}

// Captions are left-aligned, and the proof is where the ink lands.
//
// Centring came from the Python build, where one sentence was one row. Now
// that settled sentences reflow into shared rows the box carries a paragraph,
// and a centred paragraph moves its left edge on every row — the reader has to
// hunt for the start of each line.
//
// Rendered rather than asserted on the field: a short row and a long row are
// put in the same box, and their first ink column has to match.
func TestCaptionRowsShareALeftEdge(t *testing.T) {
	s := config.Default()
	s.FixedBox = true
	s.MaxLines = 2
	s.BgColor = "#000000"
	s.BoxColor = "#000000" // one flat background, so any ink is text
	s.TextColor = "#FFFFFF"
	s.FontSize = 24

	v := NewCaptionView(s)
	v.boxAlpha = 1
	v.SetLines([]captions.Line{
		{Text: "A much longer settled row that runs most of the way across.", Settled: true},
		{Text: "Short.", Settled: true},
	})

	win := test.NewWindow(v)
	t.Cleanup(win.Close)
	win.SetPadded(false)
	win.Resize(fyne.NewSize(900, 400))

	img := win.Canvas().Capture()
	rows := inkRows(img)
	if len(rows) < 2 {
		t.Fatalf("expected two rows of text, found %d", len(rows))
	}
	first, last := rows[0], rows[len(rows)-1]
	if first < 0 || last < 0 {
		t.Fatal("a row rendered no text")
	}
	// Same left edge, within a glyph's side bearing.
	if diff := first - last; diff > 4 || diff < -4 {
		t.Errorf("rows start at x=%d and x=%d — they are not sharing a left edge", first, last)
	}
}

// inkRows returns the first inked column of each text row found in the image,
// top to bottom. Rows are separated by blank scanlines.
func inkRows(img image.Image) []int {
	b := img.Bounds()
	var out []int
	rowStart := -1
	inRow := false
	for y := b.Min.Y; y < b.Max.Y; y++ {
		firstX := -1
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r > 0x8000 && g > 0x8000 && bl > 0x8000 {
				firstX = x
				break
			}
		}
		switch {
		case firstX >= 0 && !inRow:
			inRow, rowStart = true, firstX
		case firstX >= 0 && firstX < rowStart:
			rowStart = firstX
		case firstX < 0 && inRow:
			out = append(out, rowStart)
			inRow, rowStart = false, -1
		}
	}
	if inRow {
		out = append(out, rowStart)
	}
	return out
}
