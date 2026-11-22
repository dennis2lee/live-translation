package captions

import (
	"strings"
	"testing"
)

// recorded is the opening minute of a real session, as the server sent it:
// the arrival time of each caption fragment, in seconds from the first audio
// chunk going out (spikes/s3_sync, 2026-11-22, Korean speech to English).
//
// The fragments ride a one-second clock, and each arrives ahead of the speech
// it belongs to — voiced, below, is when the audio of each sentence arrived,
// read off the same recording by where the translated speech has its pauses.
var recorded = []struct {
	at   float64
	text string
}{
	{2.97, "Hello."},
	{3.93, " Today's quarterly"},
	{4.95, " earnings meeting"},
	{5.95, " will now begin."},
	{6.96, " First,"},
	{7.95, " let's review"},
	{8.93, " the third-quarter"},
	{9.95, " sales."},
	{11.95, " Total"},
	{12.95, " revenue is"},
	{13.96, " higher than the same"},
	{14.93, " period last year"},
	{15.96, " by 14%."},
	{17.94, " However,"},
	{18.96, " the domestic"},
	{19.95, " market alone"},
	{20.93, " shows a slight"},
	{21.95, " slowdown in growth."},
	{23.95, " Overseas"},
	{24.95, " sales accounted"},
	{25.95, " for more than half"},
	{26.93, " for the first time."},
	{27.95, " This is"},
	{28.95, " particularly significant."},
	{30.95, " In terms of"},
	{31.95, " costs,"},
	{32.96, " personnel expenses"},
	{33.95, " were slightly"},
	{34.95, " higher than expected."},
	{35.93, " On the other"},
	{36.96, " hand, marketing"},
	{37.96, " costs were"},
	{38.95, " 20%"},
	{39.95, " lower than planned."},
}

// voiced is when the translated speech of each recorded sentence arrived, and
// the words that open and close it. The listener hears it a player's buffer
// later still (audio.PlayPrebufferMS), so a caption on screen by `from` is
// ahead of the voice with room to spare.
var voiced = []struct {
	opens, closes string
	from, to      float64
}{
	{"Hello.", "Hello.", 3.18, 3.94},
	{"Today's quarterly", "will now begin.", 4.18, 7.18},
	{"First,", "third-quarter sales.", 7.70, 10.92},
	{"Total", "by 14%.", 12.69, 17.96},
	{"However,", "slowdown in growth.", 18.69, 23.44},
	{"Overseas", "for the first time.", 24.67, 28.43},
	{"This is", "particularly significant.", 28.97, 30.97},
	{"In terms of", "higher than expected.", 31.67, 36.97},
	{"On the other", "lower than planned.", 37.17, 41.92},
}

// shown is the caption text on screen, as one string.
func shown(b *Buffer) string {
	var parts []string
	for _, l := range b.DisplayLines() {
		if t := strings.TrimSpace(l.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// replay runs the recording through b on the display's 100 ms tick, calling
// visit after every change of the clock. The input is live throughout, so
// nothing clears.
func replay(b *Buffer, visit func(now float64)) {
	next := 0
	end := recorded[len(recorded)-1].at + 3
	for step := 0; float64(step)*0.1 <= end; step++ {
		now := float64(step) * 0.1
		for next < len(recorded) && recorded[next].at <= now {
			b.Feed(recorded[next].text, recorded[next].at)
			visit(recorded[next].at)
			next++
		}
		b.Tick(now, SpeechActive)
		visit(now)
	}
}

// firstSeen replays the recording and reports when each phrase first reached
// the screen.
func firstSeen(b *Buffer, phrases []string) []float64 {
	seen := make([]float64, len(phrases))
	for i := range seen {
		seen[i] = -1
	}
	replay(b, func(now float64) {
		text := shown(b)
		for i, p := range phrases {
			if seen[i] < 0 && strings.Contains(text, p) {
				seen[i] = now
			}
		}
	})
	return seen
}

// Drawn as they arrive, the captions are on screen before the voice speaks
// them — the opening words of every sentence, and its closing ones.
//
// Reported from live use as "the captions are always behind the voice". They
// were not arriving late: the fragments lead the audio by about a second.
// They were being withheld, a whole sentence at a time.
func TestCaptionsKeepUpWithTheVoice(t *testing.T) {
	// One row and the longest hold Preferences allows: the layout in which
	// a settled line held back for its turn would show up as a late caption.
	follow := func() *Buffer {
		return New(Options{
			MaxLines: 1, Policy: PolicyIncremental,
			LineHoldSeconds: 3, DraftInPlace: true,
		})
	}
	var opens, closes []string
	for _, v := range voiced {
		opens = append(opens, v.opens)
		closes = append(closes, v.closes)
	}
	began := firstSeen(follow(), opens)
	ended := firstSeen(follow(), closes)

	for i, v := range voiced {
		if began[i] < 0 || began[i] > v.from {
			t.Errorf("%q reached the screen at %.2f, the voice began it at %.2f",
				v.opens, began[i], v.from)
		}
		if ended[i] < 0 || ended[i] > v.to {
			t.Errorf("%q reached the screen at %.2f, the voice finished it at %.2f",
				v.closes, ended[i], v.to)
		}
	}
}

// The same recording a sentence at a time: each caption lands seconds after
// the voice began the sentence, and the longer the sentence the later. This
// is the lag that was reported, pinned so the cause stays on record — the
// mode trades it for captions that appear whole.
func TestWholeSentencesTrailTheVoice(t *testing.T) {
	b := New(Options{
		MaxLines: 4, Policy: PolicyIncremental, LineHoldSeconds: LineHoldSeconds,
		CompletePhrasesOnly: true, HideDraftWhenEmpty: true, DraftInPlace: true,
	})
	var opens []string
	for _, v := range voiced {
		opens = append(opens, v.opens)
	}
	began := firstSeen(b, opens)

	worst, late := 0.0, 0
	for i, v := range voiced {
		if began[i] < 0 {
			t.Fatalf("%q never reached the screen", v.opens)
		}
		lag := began[i] - v.from
		if lag > 1 {
			late++
		}
		worst = max(worst, lag)
	}
	if late < 6 || worst < 3 {
		t.Errorf("%d of %d sentences trailed the voice by over a second, worst %.2f s — "+
			"the recording had 7 and 3.27 s", late, len(voiced), worst)
	}
}

// A sentence the reader has watched arrive is not taken off the screen when
// it settles.
//
// A settled line waits its turn behind the hold, and text in progress is
// withheld while anything waits. Drawn dimmed and apart that was a draft
// giving way to the finished line. Drawn in place it is the sentence being
// read disappearing, and coming back up to a hold later.
func TestASentenceBeingReadStaysOnScreen(t *testing.T) {
	follow := func(inPlace bool) *Buffer {
		b := New(Options{
			MaxLines: 1, Policy: PolicyIncremental,
			LineHoldSeconds: 0.7, DraftInPlace: inPlace,
		})
		b.Feed("One.", 0)     // fills the one row
		b.Feed("Two", 0.2)    // in progress, on screen
		b.Feed(" more.", 0.4) // settles inside the first line's hold
		b.Feed(" Three", 0.5) // the next sentence begins
		return b
	}

	b := follow(true)
	if got := shown(b); got != "One. Two more. Three" {
		t.Errorf("screen = %q, want every word that arrived", got)
	}
	if q := b.QueuedLines(); len(q) != 0 {
		t.Errorf("held back: %q", q)
	}

	// Dimmed and apart, the hold is unchanged.
	b = follow(false)
	if got := shown(b); got != "One." {
		t.Errorf("screen = %q, want the first line held", got)
	}
	if q := b.QueuedLines(); len(q) != 1 {
		t.Errorf("queue = %q, want the settled line waiting", q)
	}
}

// The caption only ever grows: at no tick does text that was on screen leave
// it, with the hold as long as Preferences allows.
func TestTheCaptionOnlyGrows(t *testing.T) {
	// Two rows keep sixteen sentences, more than the recording has, so
	// nothing leaves by the far end either.
	b := New(Options{
		MaxLines: 2, Policy: PolicyIncremental,
		LineHoldSeconds: 3, DraftInPlace: true,
	})
	prev := ""
	replay(b, func(now float64) {
		text := shown(b)
		if !strings.HasPrefix(text, prev) {
			t.Fatalf("at %.2f the screen went from\n  %q\nto\n  %q", now, prev, text)
		}
		prev = text
	})
	var all string
	for _, f := range recorded {
		all = join(all, f.text)
	}
	if prev != all {
		t.Errorf("the screen ended on\n  %q\nwant every fragment:\n  %q", prev, all)
	}
}

// Pacing is still there where it does its job: sentences that arrive whole,
// which the reader has not seen a word of.
func TestWholeSentencesAreStillPaced(t *testing.T) {
	for name, opts := range map[string]Options{
		"complete phrases": {Policy: PolicyIncremental, CompletePhrasesOnly: true},
		"whole utterances": {Policy: PolicyUtterance},
	} {
		opts.MaxLines, opts.LineHoldSeconds, opts.DraftInPlace = 1, 0.7, true
		b := New(opts)
		b.Feed("One.", 0)
		b.Feed("Two.", 0.2)
		if got := shown(b); got != "One." {
			t.Errorf("%s: screen = %q, want the second line held behind the first", name, got)
		}
		b.Tick(0.7, SpeechActive)
		if got := shown(b); got != "One. Two." {
			t.Errorf("%s: after the hold the screen = %q", name, got)
		}
	}
}

// The setting can change mid-session, and takes effect on the next line.
func TestDrawingInPlaceCanBeSwitched(t *testing.T) {
	b := New(Options{MaxLines: 1, Policy: PolicyIncremental, LineHoldSeconds: 0.7})
	if b.DraftInPlace() {
		t.Fatal("on without being asked for")
	}
	b.Feed("One.", 0)
	b.Feed("Two.", 0.2)
	if q := b.QueuedLines(); len(q) != 1 {
		t.Fatalf("queue = %q, want the second line held", q)
	}
	b.SetDraftInPlace(true)
	if !b.DraftInPlace() {
		t.Fatal("not switched on")
	}
	// What was waiting is shown, not stranded behind a hold that no longer
	// applies.
	b.Tick(0.3, SpeechActive)
	if got := shown(b); got != "One. Two." {
		t.Errorf("screen = %q", got)
	}
}

// Before the policy has been worked out from the first few fragments it is
// taken to be incremental, and nothing is held then either.
func TestNothingIsHeldWhileThePolicyIsUndecided(t *testing.T) {
	b := New(Options{MaxLines: 1, Policy: PolicyAuto, LineHoldSeconds: 0.7, DraftInPlace: true})
	b.Feed("One.", 0)
	b.Feed("Two", 0.2)
	b.Feed(" more.", 0.4)
	if got := shown(b); got != "One. Two more." {
		t.Errorf("screen = %q, want both sentences", got)
	}
}

// pieces is the opening of a real OpenAI session (spikes/s3_sync,
// 2026-11-22): the translation arrives as pieces of words, a quarter of a
// second apart, and a piece with no space in front continues the word before
// it — "gre" + "ets", "third" + "-quarter", "14" + "%".
var pieces = []struct {
	at   float64
	text string
}{
	{1.61, "He"}, {1.73, " gre"}, {1.78, "ets"}, {1.81, " me"}, {1.90, "."}, {4.64, " Today"},
	{5.05, ","}, {5.25, " we'll"}, {5.25, " begin"}, {5.61, " the"}, {5.61, " quarterly"},
	{6.24, " earnings"}, {6.64, " meeting"}, {7.07, "."}, {9.10, " First"}, {9.43, ","},
	{9.43, " let's"}, {9.68, " look"}, {9.87, " at"}, {10.04, " third"}, {10.23, "-quarter"},
	{10.69, " sales"}, {11.06, "."}, {15.61, " Total"}, {16.20, " sales"}, {16.46, " increased"},
	{16.86, " by"}, {17.07, " "}, {17.08, "14"}, {17.84, "%"}, {18.06, " compared"},
	{18.25, " with"}, {18.47, " the"}, {18.47, " same"}, {18.86, " period"}, {19.06, " last"},
	{19.46, " year"}, {19.67, "."},
}

const piecesRead = "He greets me. Today, we'll begin the quarterly earnings meeting. " +
	"First, let's look at third-quarter sales. " +
	"Total sales increased by 14% compared with the same period last year."

func ofPieces() *Buffer {
	return New(Options{
		MaxLines: 4, Policy: PolicyIncremental, LineHoldSeconds: LineHoldSeconds,
		DraftInPlace: true, WordPieces: true,
	})
}

// Pieces of words are joined as they come. Joined by the rule for whole
// words they read "He gre ets me." and "third -quarter sales." — on screen
// and in the transcript, since OpenAI was added.
func TestPiecesOfWordsAreJoinedAsTheyCome(t *testing.T) {
	b := ofPieces()
	for _, p := range pieces {
		b.Feed(p.text, p.at)
		b.Tick(p.at, SpeechActive)
	}
	if got := shown(b); got != piecesRead {
		t.Errorf("the screen reads\n  %q\nwant\n  %q", got, piecesRead)
	}
	if got := strings.Join(b.DrainNewCommits(), " "); got != piecesRead {
		t.Errorf("the transcript reads\n  %q\nwant\n  %q", got, piecesRead)
	}

	// The same pieces under the rule for whole words, which is what they got.
	whole := New(Options{MaxLines: 4, Policy: PolicyIncremental})
	for _, p := range pieces {
		whole.Feed(p.text, p.at)
	}
	if got := strings.Join(whole.DrainNewCommits(), " "); !strings.Contains(got, "gre ets") {
		t.Errorf("setup: the whole-word rule no longer splits these pieces: %q", got)
	}
}

// A word is not drawn until it is finished, so no word on screen ever grows:
// at every tick the screen is the last one with whole words added. Replayed
// at the recording's own pace on the display's 100 ms tick, so the hold has
// to outlast the gaps that were really there — "14", then "%" 0.76 s later.
func TestAWordIsDrawnOnlyWhenItIsFinished(t *testing.T) {
	b := ofPieces()
	prev := ""
	check := func(now float64, after string) {
		text := shown(b)
		if !strings.HasPrefix(text, prev) {
			t.Fatalf("at %.2f (%s) the screen went from\n  %q\nto\n  %q", now, after, prev, text)
		}
		if added := text[len(prev):]; prev != "" && added != "" && added[0] != ' ' {
			t.Fatalf("at %.2f (%s) a word already on screen grew: %q then %q", now, after, prev, text)
		}
		prev = text
	}
	next := 0
	for step := 0; next < len(pieces); step++ {
		now := float64(step) * 0.1
		for next < len(pieces) && pieces[next].at <= now {
			b.Feed(pieces[next].text, pieces[next].at)
			check(pieces[next].at, pieces[next].text)
			next++
		}
		b.Tick(now, SpeechActive)
		check(now, "tick")
	}
	if prev != piecesRead {
		t.Errorf("the screen ended on\n  %q\nwant\n  %q", prev, piecesRead)
	}
}

// A word shown because its hold ran out is not taken back when a piece
// arrives that continues it after all. It grows where it is.
func TestAShownWordIsNotTakenBack(t *testing.T) {
	b := ofPieces()
	b.Feed("So", 0)
	b.Feed(" the", 0.2)
	b.Feed(" oper", 0.4)
	b.Tick(1.5, SpeechActive)
	if got := shown(b); got != "So the oper" {
		t.Fatalf("setup: screen = %q, want the held word released", got)
	}
	b.Feed("ation", 1.55)
	if got := shown(b); got != "So the operation" {
		t.Errorf("screen = %q, want the word to grow in place", got)
	}
	// The next word is held in its turn.
	b.Feed(" was", 1.7)
	if got := shown(b); got != "So the operation" {
		t.Errorf("screen = %q, want the new word held", got)
	}
}

// The last word is not held for ever: with nothing after it, it is shown
// once WordHoldSeconds have gone by.
func TestAHeldWordIsShownAfterAPause(t *testing.T) {
	b := ofPieces()
	b.Feed("So", 0)
	b.Feed(" the", 0.2)
	b.Feed(" oper", 0.4)
	if got := shown(b); got != "So the" {
		t.Fatalf("screen = %q, want the unfinished word held back", got)
	}
	if b.Tick(0.4+WordHoldSeconds-0.1, SpeechActive); shown(b) != "So the" {
		t.Errorf("the word was shown before the hold ran out: %q", shown(b))
	}
	if changed := b.Tick(0.4+WordHoldSeconds+0.01, SpeechActive); !changed || shown(b) != "So the oper" {
		t.Errorf("after the hold: changed=%v, screen = %q", changed, shown(b))
	}
}

// The same piece twice running is the speaker repeating a word, not the
// service repeating itself.
func TestARepeatedPieceIsKept(t *testing.T) {
	b := ofPieces()
	for i, p := range []string{"It", " is", " very", " very", " good", "."} {
		b.Feed(p, float64(i)*0.1)
	}
	if got := shown(b); got != "It is very very good." {
		t.Errorf("screen = %q", got)
	}
}

// The first piece of a session never has a space in front, so after a
// reconnect it is joined to the line it continues by the rule for words.
func TestTheFirstPieceAfterAReconnectStartsAWord(t *testing.T) {
	b := ofPieces()
	b.Feed("We", 0)
	b.Feed(" went", 0.2)
	b.Tick(0.2+IdleCommitSeconds, SpeechActive) // settled by the timer, unfinished
	b.NoteReconnect()
	b.Feed("home", 3)
	b.Feed(".", 3.1)
	if got := shown(b); got != "We went home." {
		t.Errorf("screen = %q", got)
	}
}

// Switching the service off the piece rule lets go of a word being held.
func TestLeavingPiecesShowsTheHeldWord(t *testing.T) {
	b := ofPieces()
	b.Feed("So", 0)
	b.Feed(" the", 0.2)
	if !b.WordPieces() || shown(b) != "So" {
		t.Fatalf("setup: screen = %q", shown(b))
	}
	b.SetWordPieces(false)
	if b.WordPieces() || shown(b) != "So the" {
		t.Errorf("screen = %q, want the held word shown", shown(b))
	}
}
