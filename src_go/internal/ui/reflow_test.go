package ui

import (
	"strings"
	"testing"

	"github.com/dennis2lee/live-translation/src_go/internal/captions"
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
