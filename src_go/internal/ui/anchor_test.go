package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"fyne.io/fyne/v2"

	"github.com/dennis2lee/live-translation/src_go/internal/captions"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
)

// continues reports whether rows is what prev becomes by adding words at the
// end: the same rows, perhaps scrolled up, every one of them unchanged but
// the last, and the last only longer — no word that was on it has gone to
// the row below. At least one of prev's rows must still be on screen, unless
// turnover is allowed: a one-row box has nothing else to keep.
func continues(prev, rows []row, turnover bool) bool {
	if len(prev) == 0 {
		return true
	}
	for shift := 0; shift < len(prev); shift++ {
		n := len(prev) - shift // rows of prev still on screen
		if n > len(rows) {
			continue
		}
		same := true
		for i := 0; i < n-1; i++ {
			if prev[shift+i] != rows[i] {
				same = false
			}
		}
		if same && strings.HasPrefix(rows[n-1].text, prev[len(prev)-1].text) {
			return true
		}
	}
	return turnover
}

// fills is how many times round the recording a talk has to go to fill the
// history of a box with that many rows, and keep dropping from it after.
func fills(maxLines int) int {
	sentences := 0
	for _, f := range spoken {
		if strings.ContainsAny(f, ".?!") {
			sentences++
		}
	}
	return maxLines*captions.DisplayHistoryFactor/sentences + 8
}

// longTalk feeds the recorded fragments round and round — speech that goes
// on for minutes with no pause long enough to clear the box — and hands each
// state of the buffer to visit.
func longTalk(t *testing.T, maxLines, times int, visit func(fragment string, b *captions.Buffer)) {
	t.Helper()
	b := captions.New(captions.Options{
		MaxLines: maxLines, Policy: captions.PolicyIncremental, DraftInPlace: true,
	})
	now := 0.0
	for range times {
		for _, fragment := range spoken {
			now++
			b.Feed(fragment, now)
			b.Tick(now+0.1, captions.SpeechActive) // live input: nothing clears
			visit(fragment, b)
		}
	}
	if got := len(b.CommittedLines()); got < maxLines*captions.DisplayHistoryFactor {
		t.Fatalf("setup: the history never filled (%d sentences), so nothing was dropped", got)
	}
}

// Dropping the oldest sentence from a full history leaves the rows on screen
// exactly as they were.
//
// Reported from live use, before its cause was known, as the caption being
// rewritten when a sentence ends. In a talk that runs for minutes the history
// is always full, so every sentence that settles pushes the oldest out — and
// the paragraph, wrapped afresh from a new first word, used to break
// differently in every row at once.
func TestTheRowsOnScreenSurviveAFullHistory(t *testing.T) {
	for _, c := range []struct {
		lines int
		width float32
	}{{4, defaultRowWidth}, {2, defaultRowWidth}, {3, 700}, {2, 500}} {
		var anchor wrapAnchor
		var prev []row
		steps := 0
		longTalk(t, c.lines, fills(c.lines), func(fragment string, b *captions.Buffer) {
			rows := Face{}.reflowAnchored(&anchor, "k", inPlace(b.DisplayLines()), 44, c.width, c.lines)
			if !continues(prev, rows, false) {
				t.Fatalf("%d rows at %.0f px: after %q the box went from\n  %q\nto\n  %q",
					c.lines, c.width, fragment, rowTexts(prev), rowTexts(rows))
			}
			for _, r := range rows {
				if w := measure(r.text, 44); w > c.width {
					t.Fatalf("a row is %.0f px wide in a %.0f px box: %q", w, c.width, r.text)
				}
			}
			prev = rows
			steps++
		})
		if steps == 0 {
			t.Fatal("setup: nothing was fed")
		}
	}
}

// And through the view itself, which is where the anchor is kept.
func TestTheViewKeepsItsRowsThroughAFullHistory(t *testing.T) {
	s := config.Default()
	s.MaxLines = 4
	v, r := viewOf(t, s)
	var prev []row
	longTalk(t, 4, fills(4), func(fragment string, b *captions.Buffer) {
		v.SetLines(b.DisplayLines())
		var rows []row
		for _, d := range drawnRows(r) {
			rows = append(rows, row{d.text, true})
		}
		if !continues(prev, rows, false) {
			t.Fatalf("after %q the box went from\n  %q\nto\n  %q", fragment, rowTexts(prev), rowTexts(rows))
		}
		prev = rows
	})
}

// Without the anchor the same talk redraws the box, which is what the test
// above is there to catch — pinned so it cannot pass for a reason of its own.
func TestAFreshWrapRedrawsTheBox(t *testing.T) {
	var prev []row
	redraws := 0
	longTalk(t, 4, fills(4), func(_ string, b *captions.Buffer) {
		rows := reflowRows(inPlace(b.DisplayLines()), 44, defaultRowWidth, 4)
		if !continues(prev, rows, false) {
			redraws++
		}
		prev = rows
	})
	if redraws == 0 {
		t.Error("wrapping afresh never redrew the box: this recording no longer shows the fault")
	}
}

// What comes out is always the text, in order, whatever was kept.
func TestAnchoredRowsAreTheText(t *testing.T) {
	var anchor wrapAnchor
	longTalk(t, 2, fills(2), func(fragment string, b *captions.Buffer) {
		var parts []string
		for _, l := range inPlace(b.DisplayLines()) {
			parts = append(parts, l.Text)
		}
		text := strings.Join(parts, " ")
		rows := Face{}.wrapAnchored(&anchor, "k", text, 44, 700, 2)
		if got := strings.Join(rows, " "); got != text {
			t.Fatalf("after %q the rows read\n  %q\nwant\n  %q", fragment, got, text)
		}
	})
}

// The cases one at a time, on text small enough to read.
func TestWhatTheAnchorKeeps(t *testing.T) {
	const width = 300
	wrap := func(a *wrapAnchor, key, text string) []string {
		return Face{}.wrapAnchored(a, key, text, 44, width, 4)
	}
	first := "One two three. Four five six seven. Eight nine ten eleven twelve."
	var a wrapAnchor
	rows := wrap(&a, "k", first)
	if len(rows) < 4 {
		t.Fatalf("setup: %d rows — %q", len(rows), rows)
	}
	fresh := Face{}.Wrap(first, 44, width)
	if strings.Join(rows, "|") != strings.Join(fresh, "|") {
		t.Fatalf("the first wrap is not the plain one: %q vs %q", rows, fresh)
	}

	// The oldest sentence goes, a word arrives: every row from the first
	// surviving boundary on is as it was.
	dropped := strings.TrimPrefix(first, "One two three. ") + " More"
	after := wrap(&a, "k", dropped)
	// "three. Four" lost its first word and is short; the rows under it are
	// the old ones, and the new word takes a row of its own.
	want := append([]string{"Four"}, rows[2:]...)
	want = append(want, "More")
	if strings.Join(after, "|") != strings.Join(want, "|") {
		t.Errorf("the rows moved when the oldest sentence went:\n  %q\nwant\n  %q", after, want)
	}
	plainly := Face{}.Wrap(dropped, 44, width)
	if strings.Join(plainly, "|") == strings.Join(after, "|") {
		t.Errorf("setup: a plain wrap gives the same rows, so nothing was kept: %q", plainly)
	}
	if strings.Join(after, " ") != dropped {
		t.Errorf("text lost: %q", after)
	}

	// The newest sentence is taken back off the end — a reconnect resuming
	// it, drawn dimmed: the rows above stay.
	cut := strings.TrimSuffix(dropped, " More")
	cut = cut[:strings.LastIndex(cut, " ")]
	back := wrap(&a, "k", cut)
	for i := 0; i+1 < len(back); i++ {
		if back[i] != after[i] {
			t.Errorf("row %d moved when the end was taken back: %q -> %q", i, after[i], back[i])
		}
	}

	// Another width, another font: a plain wrap again.
	plain := func(text string) string { return strings.Join(Face{}.Wrap(text, 44, width), "|") }
	b := a
	if got := wrap(&b, "other", cut); strings.Join(got, "|") != plain(cut) {
		t.Errorf("a new key kept old breaks: %q", got)
	}
	// Text that has nothing to do with the old: a plain wrap.
	other := "Something else entirely, said after the box was cleared."
	if got := wrap(&a, "k", other); strings.Join(got, "|") != plain(other) {
		t.Errorf("unrelated text kept old breaks: %q", got)
	}
	// And with no anchor at all it is Wrap.
	if got := wrap(nil, "", first); strings.Join(got, "|") != strings.Join(fresh, "|") {
		t.Errorf("nil anchor: %q vs %q", got, fresh)
	}
}

// A box that empties starts its next paragraph from the top, not from a
// boundary left over from the last one.
func TestAnEmptiedBoxForgetsItsBreaks(t *testing.T) {
	var a wrapAnchor
	lines := func(text string) []captions.Line { return []captions.Line{{Text: text, Settled: true}} }
	Face{}.reflowAnchored(&a, "k", lines("One two three. Four five six seven."), 44, 300, 4)
	if a.text == "" {
		t.Fatal("setup: nothing was remembered")
	}
	Face{}.reflowAnchored(&a, "k", nil, 44, 300, 4)
	if a.text != "" {
		t.Error("the breaks outlived the text they belonged to")
	}
}

// A word wider than the box is broken between characters, and the cut the
// anchor makes must not fall inside one. Narrow enough here that every long
// Korean word is broken.
func TestTheAnchorKeepsWholeCharacters(t *testing.T) {
	const width = 150 // about three Hangul characters at 44 px
	var a wrapAnchor
	sentences := []string{
		"오늘 분기 실적 회의를 시작하겠습니다.", "먼저 삼분기 매출부터 살펴보겠습니다.",
		"전체 매출은 작년 같은 기간보다 십사 퍼센트 증가했습니다.", "다만 국내 시장만 놓고 보면 성장이 다소 둔화되었습니다.",
	}
	broken := 0
	for i := 0; i < 12; i++ {
		window := make([]string, 0, 3)
		for j := 0; j < 3; j++ {
			window = append(window, sentences[(i+j)%len(sentences)])
		}
		text := strings.Join(window, " ")
		rows := Face{}.wrapAnchored(&a, "k", text, 44, width, 4)
		if got := strings.Join(rows, ""); got != strings.ReplaceAll(text, " ", "") &&
			strings.ReplaceAll(got, " ", "") != strings.ReplaceAll(text, " ", "") {
			t.Fatalf("round %d: the rows read %q, want %q", i, got, text)
		}
		at := 0
		for _, r := range rows {
			if !utf8.ValidString(r) {
				t.Fatalf("round %d: a character was cut in two: %q", i, r)
			}
			// A row that starts where the text has no space before it is a
			// word broken between characters.
			j := strings.Index(text[at:], r)
			if j < 0 {
				t.Fatalf("round %d: row %q is not in the text", i, r)
			}
			if at+j > 0 && text[at+j-1] != ' ' {
				broken++
			}
			at += j + len(r)
		}
	}
	if broken == 0 {
		t.Fatal("setup: no word was broken between characters, so the cut was never tested")
	}
}

// A refrain: the words at the top of the paragraph come round again, so the
// new paragraph's opening fits the old one in more than one place. The cut
// is where it fits furthest, and the rows below still hold.
func TestTheAnchorSurvivesARefrain(t *testing.T) {
	const width = 520
	refrain := "Lord have mercy on us and hear our prayer today."
	verse := "We bring before you everyone who is sick or alone or afraid this morning."
	old := strings.Join([]string{refrain, refrain, refrain, verse, "And we give you thanks."}, " ")
	var a wrapAnchor
	before := Face{}.wrapAnchored(&a, "k", old, 44, width, 8)

	now := strings.TrimPrefix(old, refrain+" ") + " Amen."
	after := Face{}.wrapAnchored(&a, "k", now, 44, width, 8)
	if strings.Join(after, " ") != now {
		t.Fatalf("text lost: %q", after)
	}
	// Every old row that began after the cut is still there, in order — all
	// but the last, which the new word may have joined.
	starts := rowStarts(old, before)
	first := 0
	for first < len(starts) && starts[first] < len(refrain)+1 {
		first++
	}
	want := before[first : len(before)-1]
	if len(want) < 4 {
		t.Fatalf("setup: only %d rows to keep — %q", len(want), before)
	}
	got := strings.Join(after, "|")
	if !strings.Contains(got, strings.Join(want, "|")) {
		t.Errorf("the rows below the cut moved:\n  %q\n  %q", before, after)
	}
}

// One row: every word is on screen at some point. Words that finished a row
// which left the box in the same repaint used to go by unseen.
func TestAOneRowBoxShowsEveryWord(t *testing.T) {
	b := captions.New(captions.Options{
		MaxLines: 1, Policy: captions.PolicyIncremental, DraftInPlace: true,
	})
	var anchor wrapAnchor
	var seen []string // each row as it was when it left the screen
	last := ""
	for i, fragment := range spoken {
		now := float64(i)
		b.Feed(fragment, now)
		b.Tick(now+0.1, captions.SpeechActive)
		rows := Face{}.reflowAnchored(&anchor, "k", inPlace(b.DisplayLines()), 44, defaultRowWidth, 1)
		if len(rows) != 1 {
			t.Fatalf("after %q the one-row box has %d rows", fragment, len(rows))
		}
		if !strings.HasPrefix(rows[0].text, last) {
			seen = append(seen, last)
		}
		last = rows[0].text
	}
	seen = append(seen, last)
	want := strings.Join(strings.Fields(strings.Join(spoken, "")), " ")
	if got := strings.Join(seen, " "); got != want {
		t.Errorf("what crossed the screen:\n  %q\nwhat was said:\n  %q", got, want)
	}
	if len(seen) < 4 {
		t.Fatalf("setup: only %d rows went by, the turnover is not exercised", len(seen))
	}
}

// A sentence that arrives whole and fits the box is shown whole: it starts a
// row of its own rather than filling up a row that scrolls out with its
// first words on it.
func TestAWholeSentenceIsNotStartedOnADepartingRow(t *testing.T) {
	const width, box = 760, 2
	var a wrapAnchor
	lines := []captions.Line{{Text: "A first sentence that ends partway along, here.", Settled: true}}
	before := Face{}.reflowAnchored(&a, "k", lines, 44, width, box)
	if n := len(before); n != 2 || measure(before[1].text+" Then a second", 44) > width {
		t.Fatalf("setup: the first sentence should end partway along row two: %q", rowTexts(before))
	}
	sentence := "Then a second one, long enough to take up two rows all by itself."
	if n := len(Face{}.Wrap(sentence, 44, width)); n != 2 {
		t.Fatalf("setup: the sentence takes %d rows, want 2", n)
	}
	lines = append(lines, captions.Line{Text: sentence, Settled: true})
	// Wrapped straight on, its first words share the first sentence's last
	// row, and that row leaves a two-row box in the same repaint.
	if plain := reflowRows(lines, 44, width, 100); len(plain) != 4 {
		t.Fatalf("setup: wrapped straight on it is %d rows, want 4: %q", len(plain), rowTexts(plain))
	}
	after := Face{}.reflowAnchored(&a, "k", lines, 44, width, box)

	if got := strings.Join(rowTexts(after), " "); got != sentence {
		t.Errorf("the box shows\n  %q\nwant the whole sentence\n  %q\n(before: %q)",
			rowTexts(after), sentence, rowTexts(before))
	}
}

// Sentences of a word or two, in a wide window: the whole history is on
// screen, and none of it may be pulled along when the oldest sentence goes.
func TestShortSentencesInAWideWindowStayPut(t *testing.T) {
	const width, box = 1920*0.9 - 2*PadH, 4
	b := captions.New(captions.Options{
		MaxLines: box, Policy: captions.PolicyIncremental, DraftInPlace: true,
	})
	short := []string{"Yes.", "Thank you.", "Good.", "No.", "Sure.", "Amen.", "I see.", "Right."}
	var anchor wrapAnchor
	var prev []row
	for i := 0; i < 200; i++ {
		now := float64(i)
		b.Feed(short[i%len(short)], now)
		b.Tick(now+0.1, captions.SpeechActive)
		rows := Face{}.reflowAnchored(&anchor, "k", inPlace(b.DisplayLines()), 44, width, box)
		if !continues(prev, rows, false) {
			t.Fatalf("sentence %d: the box went from\n  %q\nto\n  %q", i, rowTexts(prev), rowTexts(rows))
		}
		prev = rows
	}
	if len(prev) != box {
		t.Fatalf("setup: the box holds %d rows, want it full", len(prev))
	}
}

// A real OpenAI session, piece by piece, through the buffer and the view:
// no row on screen changes except the last one growing by whole words.
func TestPiecesOfWordsNeverMoveAWord(t *testing.T) {
	pieces := []string{
		"He", " gre", "ets", " me", ".", " Today", ",", " we'll", " begin", " the", " quarterly",
		" earnings", " meeting", ".", " First", ",", " let's", " look", " at", " third", "-quarter",
		" sales", ".", " Total", " sales", " increased", " by", " ", "14", "%", " compared",
		" with", " the", " same", " period", " last", " year", ".", " However", ",", " looking",
		" only", " at", " the", " domestic", " market", ",", " growth", " has", " slowed",
		" somewhat", ".", " On", " the", " other", " hand", ",", " we", " cut", " marketing",
		" expenses", " by", " ", "20", "%", " compared", " with", " the", " plan", ".",
	}
	for _, width := range []float32{defaultRowWidth, 900*0.9 - 2*PadH, 640, 500, 430} {
		b := captions.New(captions.Options{
			MaxLines: 3, Policy: captions.PolicyIncremental, DraftInPlace: true, WordPieces: true,
		})
		var anchor wrapAnchor
		var prev []row
		for round := 0; round < 6; round++ {
			for i, piece := range pieces {
				now := float64(round*len(pieces)+i) * 0.25
				if i == 0 && round > 0 {
					piece = " " + piece // a new sentence mid-session has its space
				}
				b.Feed(piece, now)
				b.Tick(now, captions.SpeechActive)
				rows := Face{}.reflowAnchored(&anchor, "k", inPlace(b.DisplayLines()), 44, width, 3)
				if !continues(prev, rows, false) {
					t.Fatalf("%.0f px, after %q the box went from\n  %q\nto\n  %q",
						width, piece, rowTexts(prev), rowTexts(rows))
				}
				prev = rows
			}
		}
	}
}

// Another width or another size is another wrap: nothing is kept from the
// old one. Through the view, which is where the key is made.
func TestAResizeOrANewSizeWrapsAfresh(t *testing.T) {
	s := config.Default()
	s.MaxLines = 4
	v, r := viewOf(t, s)
	// Enough talk that the history has been cut and the rows are anchored
	// somewhere a plain wrap would not put them.
	var b *captions.Buffer
	longTalk(t, 4, fills(4), func(_ string, talking *captions.Buffer) {
		b = talking
		v.SetLines(b.DisplayLines())
	})

	plain := func(width float32, size float32) []string {
		rows := reflowRows(inPlace(b.DisplayLines()), size, width, 4)
		return rowTexts(rows)
	}
	shown := func() []string {
		var out []string
		for _, d := range drawnRows(r) {
			out = append(out, d.text)
		}
		return out
	}

	v.Resize(fyne.NewSize(700, 400))
	width := float32(700)*float32(s.BoxMaxWidthPct)/100 - 2*PadH
	if got, want := shown(), plain(width, float32(s.FontSize)); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("after a resize the box shows\n  %q\nwant a fresh wrap\n  %q", got, want)
	}

	s.FontSize = 30
	v.ApplySettings(s)
	if got, want := shown(), plain(width, 30); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("after a new size the box shows\n  %q\nwant a fresh wrap\n  %q", got, want)
	}
}

// A piece that makes the last word longer is not a new word, and is not
// given a row of its own: the word would be cut in two. Short enough to fit
// a row, and long enough to push the word off the row it was on — the case
// in which a new word would get a row to itself.
func TestAGrowingWordIsNotSplit(t *testing.T) {
	const width = 520
	var a wrapAnchor
	before := Face{}.wrapAnchored(&a, "k", "Today we begin the quarterly earnings meet", 44, width, 1)
	text := "Today we begin the quarterly earnings meeting now."
	rows := Face{}.wrapAnchored(&a, "k", text, 44, width, 1)
	if len(rows) <= len(before) {
		t.Fatalf("setup: the longer word did not wrap: %q then %q", before, rows)
	}
	if got := strings.Join(rows, " "); got != text {
		t.Errorf("the rows read\n  %q\nwant\n  %q", got, text)
	}
}

// Text added that would not fit the box anyway runs on from the old row:
// starting it on a row of its own shows less of it, not more.
func TestALongAdditionRunsOn(t *testing.T) {
	const width = 760
	old := "A first sentence that ends partway along, here."
	added := "In terms of costs, personnel expenses were slightly higher than expected."
	if n := len(Face{}.Wrap(added, 44, width)); n < 2 {
		t.Fatalf("setup: the addition fits one row (%d)", n)
	}
	var a wrapAnchor
	Face{}.wrapAnchored(&a, "k", old, 44, width, 1)
	rows := Face{}.wrapAnchored(&a, "k", old+" "+added, 44, width, 1)
	plain := Face{}.Wrap(old+" "+added, 44, width)
	if strings.Join(rows, "|") != strings.Join(plain, "|") {
		t.Errorf("the rows are\n  %q\nwant the plain wrap\n  %q", rows, plain)
	}
}

// Text that changed somewhere as well as being added to is not treated as an
// addition: what follows the change is wrapped again, so every row still fits.
func TestAnEditIsNotTakenForAnAddition(t *testing.T) {
	const width = 520
	old := "One two three four five six seven eight nine ten eleven twelve thirteen " +
		"fourteen fifteen sixteen seventeen eighteen nineteen twenty."
	var a wrapAnchor
	before := Face{}.wrapAnchored(&a, "k", old, 44, width, 1)
	if len(before) < 5 {
		t.Fatalf("setup: only %d rows", len(before))
	}
	// A word on the second row changes, and a few words are added at the end.
	at := strings.Index(old, "eight")
	if at < alignProbe || at >= a.starts[2] {
		t.Fatalf("setup: the change at %d is not on the second row (rows start %v)", at, a.starts)
	}
	text := old[:at] + "EIGHT" + old[at+5:] + " And more."
	rows := Face{}.wrapAnchored(&a, "k", text, 44, width, 1)
	if got := strings.Join(rows, " "); got != text {
		t.Fatalf("the rows read\n  %q\nwant\n  %q", got, text)
	}
	for _, r := range rows {
		if w := measure(r, 44); w > width {
			t.Fatalf("a row is %.0f px wide in a %d px box: %q", w, width, r)
		}
	}
}

// Another font is another wrap too.
func TestANewFontWrapsAfresh(t *testing.T) {
	name := anInstalledFont(t, "Courier New", "Menlo", "Monaco", "Consolas")
	s := config.Default()
	s.MaxLines = 4
	v, r := viewOf(t, s)
	var b *captions.Buffer
	longTalk(t, 4, fills(4), func(_ string, talking *captions.Buffer) {
		b = talking
		v.SetLines(b.DisplayLines())
	})
	s.CaptionFont = name
	v.ApplySettings(s)
	if v.Font() == nil {
		t.Skipf("%s could not be loaded here", name)
	}

	width := float32(900)*float32(s.BoxMaxWidthPct)/100 - 2*PadH
	want := rowTexts(v.face().reflowRows(inPlace(b.DisplayLines()), float32(s.FontSize), width, 4))
	var got []string
	for _, d := range drawnRows(r) {
		got = append(got, d.text)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("in %s the box shows\n  %q\nwant a fresh wrap\n  %q", name, got, want)
	}
}
