package captions

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func incremental(opts Options) *Buffer {
	opts.Policy = PolicyIncremental
	return New(opts)
}

func texts(lines []Line) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return out
}

// --- joining -------------------------------------------------------------

func TestJoin(t *testing.T) {
	cases := []struct{ left, right, want string }{
		{"", "  Hello", "Hello"},
		{"Hello", " world", "Hello world"},
		{"Hello ", "world", "Hello world"},
		{"Hello", "world", "Hello world"},
		{"Hello", ".", "Hello."},
		{"Hello", ", world", "Hello, world"},
		{"(", "x", "(x"},
		// A fragment that split mid-word must not gain a space.
		{"계획", "입니다.", "계획입니다."},
		// Korean followed by Latin still needs one.
		{"영업이익률", "up", "영업이익률 up"},
	}
	for _, c := range cases {
		if got := join(c.left, c.right); got != c.want {
			t.Errorf("join(%q, %q) = %q, want %q", c.left, c.right, got, c.want)
		}
	}
}

// --- duplicate suppression ------------------------------------------------

// An identical fragment inside the window is the API redelivering it.
func TestDuplicateInsideWindowIsDropped(t *testing.T) {
	b := incremental(Options{})
	b.Feed(" the", 0)
	if b.Feed(" the", DuplicateWindowSeconds/2) {
		t.Fatal("redelivered fragment was accepted")
	}
	if b.PendingText() != "the" {
		t.Errorf("pending = %q", b.PendingText())
	}
}

// A second later it is the speaker actually saying the word again. " the the"
// is ordinary English and suppressing it loses what they said.
func TestDuplicateOutsideWindowIsKept(t *testing.T) {
	b := incremental(Options{})
	b.Feed(" the", 0)
	if !b.Feed(" the", 1.0) {
		t.Fatal("genuine repeat was suppressed")
	}
	if b.PendingText() != "the the" {
		t.Errorf("pending = %q, want the repeat kept", b.PendingText())
	}
}

// --- recording is independent of the display ------------------------------

// clear() settles pending text before wiping, so clearing the screen never
// truncates the transcript mid-sentence.
func TestClearDoesNotDiscardWhatWasSaid(t *testing.T) {
	b := incremental(Options{ClearAfterSeconds: 2})
	b.Feed("One.", 0)
	b.Feed(" Two.", 0.4)
	b.Feed(" Three unfinished", 0.8)
	b.Clear()

	got := b.DrainNewCommits()
	want := []string{"One.", "Two.", "Three unfinished"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DrainNewCommits = %q, want %q", got, want)
	}
	if !b.IsEmpty() {
		t.Error("buffer should be empty after Clear")
	}
}

// newCommits is the one unbounded structure and the buffer outlives a session,
// so it must drain whether or not anyone is listening.
func TestEachLineIsHandedOverExactlyOnce(t *testing.T) {
	b := incremental(Options{})
	b.Feed("First sentence.", 0)
	first := b.DrainNewCommits()
	if len(first) != 1 {
		t.Fatalf("first drain = %q", first)
	}
	if again := b.DrainNewCommits(); len(again) != 0 {
		t.Errorf("second drain = %q, want nothing", again)
	}
}

// --- paced release --------------------------------------------------------

// A burst must not flash past inside a single repaint.
func TestBurstIsReleasedOneLineAtATime(t *testing.T) {
	b := incremental(Options{MaxLines: 2, ClearAfterSeconds: 0})
	for _, s := range []string{
		"Sentence one is here.", "Sentence two is here.", "Sentence three is here.",
		"Sentence four is here.", "Sentence five is here.",
	} {
		b.Feed(s, 0.01)
	}
	// Two go straight on screen because there was room; the rest are paced.
	// The queue holds three: past max_lines the hold halves so every line is
	// still drawn, and only past CatchUpFactor*max_lines is the backlog
	// released in one pass. Five sentences into a two-line box sits inside
	// that band, which is the case the reader actually hit.
	if got := len(b.DisplayLines()); got != 2 {
		t.Fatalf("display holds %d lines, want 2", got)
	}
	if got := len(b.QueuedLines()); got != 3 {
		t.Fatalf("queue holds %d lines, want 3", got)
	}

	// Every one of them still reaches the transcript.
	if got := len(b.DrainNewCommits()); got != 5 {
		t.Errorf("transcript got %d lines, want all 5", got)
	}
}

// A line that has had its full hold time may be replaced — that is the pacing
// working, not a line being lost.
func TestLineIsHeldForItsFullHoldTime(t *testing.T) {
	b := incremental(Options{MaxLines: 1, ClearAfterSeconds: 0})
	b.Feed("A sentence long enough to settle.", 0)
	b.Feed(" Another sentence long enough too.", 0.3) // inside the 0.7 s hold

	want := []string{"A sentence long enough to settle."}
	if got := texts(b.DisplayLines()); !reflect.DeepEqual(got, want) {
		t.Errorf("display = %q, want the first line still up", got)
	}
	if got := len(b.QueuedLines()); got != 1 {
		t.Fatalf("queue holds %d lines, want the second waiting out the hold", got)
	}
	b.Tick(1.2, SpeechUnknown)
	// Released: it is the newest line of the history now, and the renderer
	// puts the newest rows at the bottom of the box.
	got := texts(b.DisplayLines())
	if len(got) == 0 || got[len(got)-1] != "Another sentence long enough too." {
		t.Errorf("display = %q, want the second line released after the hold", got)
	}
	if got := len(b.QueuedLines()); got != 0 {
		t.Errorf("queue still holds %d lines", got)
	}
}

// The idle timer must clear a late server beat, and still fire on a real pause.
//
// Fragments arrive on a one-second server clock, not at the pace of speech
// (measured 2026-08-08: 73 of 93 gaps between 900 and 1200 ms). The tail is a
// beat arriving late followed by one back on the clock. At 1.2 s the timer sat
// inside that jitter and split sentences the speaker had not finished — 11 of
// 93 gaps crossed it.
func TestIdleTimerClearsALateBeatButStillFires(t *testing.T) {
	// A late beat: the slowest gap measured that was followed by an early one.
	b := incremental(Options{ClearAfterSeconds: 0})
	b.Feed("a phrase with no full stop", 0)
	b.Tick(1.26, SpeechActive) // p90 of the measured distribution
	if got := b.PendingText(); got == "" {
		t.Error("a late server beat settled the phrase — the timer is inside the jitter")
	}

	// A real pause still settles it, well before the two-second missed beat.
	b.Tick(1.7, SpeechActive)
	if got := b.PendingText(); got != "" {
		t.Errorf("pending is still %q after a genuine pause", got)
	}
	if got := texts(b.DisplayLines()); len(got) != 1 || got[0] != "a phrase with no full stop" {
		t.Errorf("display = %q, want the settled phrase", got)
	}
}

// The timer cannot usefully exceed the clear window: Tick force-commits
// pending text there too, so a larger idle value would never be reached.
func TestIdleTimerFitsInsideTheClearWindow(t *testing.T) {
	if IdleCommitSeconds >= ClearAfterSeconds {
		t.Errorf("IdleCommitSeconds %.1f >= ClearAfterSeconds %.1f, so the idle timer is dead code",
			IdleCommitSeconds, ClearAfterSeconds)
	}
}

// --- complete-phrases mode ------------------------------------------------

func TestCompletePhrasesWithholdsUnsettledText(t *testing.T) {
	b := incremental(Options{CompletePhrasesOnly: true, ClearAfterSeconds: 0})
	// With nothing settled yet the text in progress is shown rather than an
	// empty box — withholding it there protects nothing and tells the reader
	// nothing. See TestPendingShowsRatherThanABlankBox.
	b.Feed("Revenue is up", 0)
	if got := b.DisplayLines(); len(got) != 1 || got[0].Settled {
		t.Errorf("display = %v, want the unsettled text on an empty screen", got)
	}
	b.Feed(" fourteen percent this quarter.", 0.5)
	if got := texts(b.DisplayLines()); len(got) != 1 {
		t.Errorf("display = %q, want the settled sentence", got)
	}
	// Now that a settled line is on screen, the mode withholds again.
	b.Feed(" And the next sentence begins", 1.0)
	for _, l := range b.DisplayLines() {
		if !l.Settled {
			t.Errorf("unsettled text %q shown alongside a settled line", l.Text)
		}
	}
}

// "Dr." must not settle a two-word fragment with no dimmed line left on screen
// to hint that more is coming.
func TestAbbreviationDoesNotSettleInCompletePhrasesMode(t *testing.T) {
	b := incremental(Options{CompletePhrasesOnly: true, ClearAfterSeconds: 0})
	b.Feed("Dr.", 0)
	if len(b.QueuedLines())+len(b.CommittedLines()) != 0 {
		t.Error("an abbreviation settled a fragment as a sentence")
	}
	// This used to assert the opposite outside complete-phrases mode, because
	// the guard was a blanket 20-rune minimum that only that mode consulted.
	// The guard is now abbreviation detection, and an abbreviation is not a
	// sentence in either mode — the transcript was getting "Dr." on a line of
	// its own, which no reader asked for.
	plain := incremental(Options{ClearAfterSeconds: 0})
	plain.Feed("Dr.", 0)
	if len(plain.QueuedLines())+len(plain.CommittedLines()) != 0 {
		t.Error("an abbreviation settled outside complete-phrases mode")
	}
}

// turn_complete is the only end-of-turn signal the API reports reliably.
func TestTurnCompleteSettles(t *testing.T) {
	b := incremental(Options{CompletePhrasesOnly: true, ClearAfterSeconds: 0})
	b.Feed("No full stop here", 0)
	if !b.NoteTurnComplete(0.1) {
		t.Fatal("turn_complete did not settle the pending text")
	}
	if got := texts(b.DisplayLines()); len(got) != 1 || got[0] != "No full stop here" {
		t.Errorf("display = %q", got)
	}
}

// --- clear on silence -----------------------------------------------------

// The countdown only starts once the backlog has drained AND the room is quiet.
func TestClearWaitsForTheBacklogAndTheSilence(t *testing.T) {
	b := incremental(Options{MaxLines: 2, ClearAfterSeconds: 2})
	for i := 0; i < 5; i++ {
		b.Feed("Sentence number "+strings.Repeat("x", 10)+".", 0.01)
	}
	// Still queued: not complete, so no clearing however long we wait.
	b.Tick(3.0, SpeechQuiet)
	if b.IsEmpty() {
		t.Fatal("cleared while the backlog was still draining")
	}
}

// Somebody talking must not have the captions wiped from under them.
func TestSpeechHoldsOffTheClear(t *testing.T) {
	b := incremental(Options{ClearAfterSeconds: 2})
	b.Feed("A settled sentence.", 0)
	b.Tick(5.0, SpeechActive)
	if b.IsEmpty() {
		t.Error("cleared while the input still carried speech")
	}
	b.Tick(6.0, SpeechQuiet)
	b.Tick(8.5, SpeechQuiet)
	if !b.IsEmpty() {
		t.Error("did not clear once the room went quiet")
	}
}

// A noisy room must not pin stale captions on screen for ever.
func TestMaxClearWaitIsACeiling(t *testing.T) {
	b := incremental(Options{ClearAfterSeconds: 2})
	b.Feed("A settled sentence.", 0)
	for now := 1.0; now <= MaxClearWaitSeconds+1; now += 1.0 {
		b.Tick(now, SpeechActive)
	}
	if !b.IsEmpty() {
		t.Error("captions outlived the ceiling in a room that never went quiet")
	}
}

// --- reconnect ------------------------------------------------------------

// The first fragment after a reconnect continues the sentence it interrupted.
func TestReconnectContinuesTheLine(t *testing.T) {
	b := incremental(Options{ClearAfterSeconds: 0})
	b.Feed("Revenue is up", 0)
	b.Tick(2.0, SpeechUnknown) // idle-commits the phrase
	b.NoteReconnect()
	b.Feed(" fourteen percent.", 3.0)

	lines := texts(b.DisplayLines())
	joined := strings.Join(lines, " | ")
	if !strings.Contains(joined, "Revenue is up fourteen percent.") {
		t.Errorf("display = %q, want the halves rejoined", lines)
	}
}

// The transcript must not repeat the half it already recorded.
func TestReconnectDoesNotDoubleRecordThePrefix(t *testing.T) {
	b := incremental(Options{ClearAfterSeconds: 0})
	b.Feed("Revenue is up", 0)
	b.Tick(2.0, SpeechUnknown)
	first := b.DrainNewCommits() // the transcript already has this half
	if len(first) != 1 {
		t.Fatalf("first drain = %q", first)
	}
	b.NoteReconnect()
	b.Feed(" fourteen percent.", 3.0)

	second := b.DrainNewCommits()
	for _, line := range second {
		if strings.HasPrefix(line, "Revenue is up ") {
			t.Errorf("transcript repeated the recorded half: %q", line)
		}
	}
}

// --- fuzz -----------------------------------------------------------------

// Every entry point together, which is how the three bugs of 2026-08-05
// surfaced: each of them needed two operations to meet.
func TestFuzzInvariantsHold(t *testing.T) {
	fragments := []string{
		" Good morning.", " Revenue", " is up", " fourteen percent.", " Dr.",
		" Smith agrees.", " 영업이익률", " 입니다.", " the", " the", "",
	}
	steps := []float64{0.02, 0.1, 0.4, 1.0, 3.0}

	for _, mode := range []bool{false, true} {
		mode := mode
		name := "default"
		if mode {
			name = "complete-phrases"
		}
		t.Run(name, func(t *testing.T) {
			rng := rand.New(rand.NewSource(20260805))
			b := incremental(Options{
				MaxLines:            2,
				ClearAfterSeconds:   2,
				CompletePhrasesOnly: mode,
			})
			now := 0.0
			recorded := 0

			for step := 0; step < 20000; step++ {
				now += steps[rng.Intn(len(steps))]
				switch roll := rng.Float64(); {
				case roll < 0.55:
					b.Feed(fragments[rng.Intn(len(fragments))], now)
				case roll < 0.60:
					b.NoteTurnComplete(now)
				case roll < 0.63:
					b.NoteReconnect()
				case roll < 0.65:
					b.Clear()
				case roll < 0.67:
					b.SetMaxLines(1 + rng.Intn(4))
				default:
					b.Tick(now, Speech(rng.Intn(3)))
				}
				recorded += len(b.DrainNewCommits())

				// The display history is bounded — by DisplayHistoryFactor
				// sentences per row, not by max_lines: the renderer reflows
				// sentences into shared rows and trims to max_lines rows
				// itself, so the buffer keeps enough sentences to fill them.
				if got := len(b.CommittedLines()); got > b.MaxLines()*DisplayHistoryFactor {
					t.Fatalf("step %d: %d committed lines exceed the history bound %d",
						step, got, b.MaxLines()*DisplayHistoryFactor)
				}
				if got := len(b.DisplayLines()); got > b.MaxLines()*DisplayHistoryFactor+1 {
					t.Fatalf("step %d: %d display lines exceed history+pending", step, got)
				}
				if b.IsEmpty() && len(b.DisplayLines()) > 0 {
					t.Fatalf("step %d: IsEmpty but display is %v", step, b.DisplayLines())
				}
				// Complete-phrases mode withholds unsettled text whenever
				// there is a settled line to protect. On an otherwise empty
				// screen it shows it instead of nothing.
				if mode {
					lines := b.DisplayLines()
					for _, l := range lines {
						if !l.Settled && len(lines) != 1 {
							t.Fatalf("step %d: unsettled text shown beside settled lines", step)
						}
					}
				}

				// The queue is bounded by CatchUpFactor*max_lines once the
				// display has caught up. Shrinking max_lines exceeds it until
				// the next tick, which is why this ticks before asserting.
				b.Tick(now, SpeechUnknown)
				if got := len(b.QueuedLines()); got > b.MaxLines()*CatchUpFactor {
					t.Fatalf("step %d: queue still holds %d after a tick", step, got)
				}
				// newCommits is drained every step above, so it must never grow.
				if got := len(b.newCommits); got > 200 {
					t.Fatalf("step %d: newCommits grew to %d", step, got)
				}
			}
			if recorded == 0 {
				t.Error("20000 steps recorded nothing — the fuzz is not exercising anything")
			}
		})
	}
}

// A full stop settles the sentence whatever its length. The old rule needed 20
// runes behind the stop, so a short sentence waited out the idle timer while a
// long one appeared at once — a full stop that worked only sometimes.
func TestShortSentenceSettlesOnItsFullStop(t *testing.T) {
	b := New(Options{MaxLines: 3, Policy: PolicyIncremental, CompletePhrasesOnly: true})
	b.Feed("Yes, I agree.", 1.0)
	if got := b.PendingText(); got != "" {
		t.Errorf("pending = %q — the full stop did not settle it", got)
	}
	if lines := b.DisplayLines(); len(lines) != 1 || !lines[0].Settled {
		t.Errorf("display = %+v, want one settled line", lines)
	}
}

// …but an abbreviation still must not, or "Dr." settles two words as a
// sentence with nothing on screen to hint that more is coming.
func TestAbbreviationDoesNotSettle(t *testing.T) {
	cases := []struct {
		text   string
		settle bool
	}{
		{"Thank you, Dr.", false},
		{"He asked Mr.", false},
		{"Signed, J.", false}, // an initial
		{"Chapter 3 vs.", false},
		// Words that end sentences far more often than they abbreviate must
		// NOT be on the list — "no" was, and "The answer is no." then waited
		// out the idle timer, which is the defect the list exists to remove.
		{"The answer is no.", true},
		{"He works at Acme Inc.", true},
		{"Apples, oranges, etc.", true},
		{"Yes.", true},
		{"It is ready.", true},
		{"Is it ready?", true},
		{"Stop!", true},
		{"The doctor arrived.", true}, // "doctor" spelled out is not "Dr."
	}
	for _, c := range cases {
		b := New(Options{MaxLines: 3, Policy: PolicyIncremental, CompletePhrasesOnly: true})
		b.Feed(c.text, 1.0)
		settled := b.PendingText() == ""
		if settled != c.settle {
			t.Errorf("%q settled=%v, want %v", c.text, settled, c.settle)
		}
	}
}

// A burst must not skip lines. Releasing the whole backlog at once put the
// middle sentences into committed and straight back out inside one repaint —
// they reached the transcript and never reached the reader.
func TestBurstShowsEveryLine(t *testing.T) {
	b := New(Options{MaxLines: 2, Policy: PolicyIncremental,
		LineHoldSeconds: LineHoldSeconds, ClearAfterSeconds: 0})
	seen := map[string]bool{}
	record := func() {
		for _, l := range b.DisplayLines() {
			seen[l.Text] = true
		}
	}
	for i := 1; i <= 5; i++ {
		b.Feed(fmt.Sprintf(" Sentence number %d here.", i), 1.0)
		record()
	}
	for now := 1.0; now < 8.0; now += 0.1 {
		b.Tick(now, SpeechQuiet)
		record()
	}
	for i := 1; i <= 5; i++ {
		if want := fmt.Sprintf("Sentence number %d here.", i); !seen[want] {
			t.Errorf("%q was never displayed", want)
		}
	}
}

// Complete-phrases mode withholds unsettled text — except when withholding it
// would leave the box empty, which tells the reader nothing and was the
// reported "captions disappear while a long sentence is translated".
func TestPendingShowsRatherThanABlankBox(t *testing.T) {
	b := New(Options{MaxLines: 3, Policy: PolicyIncremental, CompletePhrasesOnly: true})

	b.Feed(" A long sentence that is still being", 1.0)
	lines := b.DisplayLines()
	if len(lines) != 1 || lines[0].Settled {
		t.Fatalf("display = %+v, want the unsettled text rather than nothing", lines)
	}

	// Once a settled line exists there is something to protect, so the
	// unsettled text is withheld again.
	b.Feed(" translated right now.", 2.0)
	b.Feed(" And more text arriving", 3.0)
	for _, l := range b.DisplayLines() {
		if !l.Settled {
			t.Errorf("unsettled text %q was shown alongside a settled line", l.Text)
		}
	}
}

// A reconnect resumes a line only if it was cut off mid-sentence.
//
// The server ends a session every nine minutes, so this runs seven times in an
// hour. Resuming a line that had already reached a full stop glued two
// unrelated sentences into one — most visible in complete-phrases mode, where
// every settled line ends with punctuation by definition.
func TestReconnectDoesNotJoinTwoFinishedSentences(t *testing.T) {
	b := incremental(Options{CompletePhrasesOnly: true, ClearAfterSeconds: 0})
	b.Feed("The reading is finished here.", 0)
	b.DrainNewCommits()

	b.NoteReconnect()
	b.Feed(" And then the speaker carried on.", 1.0)

	lines := texts(b.DisplayLines())
	for _, l := range lines {
		if strings.Contains(l, "here. And then") {
			t.Errorf("two finished sentences were joined into %q", l)
		}
	}
	if len(lines) != 2 {
		t.Errorf("display = %q, want the two sentences on their own lines", lines)
	}
	// And the transcript records them separately too.
	got := b.DrainNewCommits()
	if len(got) != 1 || got[0] != "And then the speaker carried on." {
		t.Errorf("transcript got %q, want the new sentence on its own", got)
	}
}

// The half-sentence case is what the resume exists for, and it still works.
func TestReconnectStillJoinsAnInterruptedSentence(t *testing.T) {
	b := incremental(Options{CompletePhrasesOnly: true, ClearAfterSeconds: 0})
	b.Feed("The reading was interrupted while", 0)
	b.Tick(2.0, SpeechUnknown) // the idle timer settles it with no full stop
	b.DrainNewCommits()

	b.NoteReconnect()
	b.Feed(" the speaker was still talking.", 3.0)

	joined := strings.Join(texts(b.DisplayLines()), " | ")
	if !strings.Contains(joined, "interrupted while the speaker was still talking.") {
		t.Errorf("display = %q, want the halves rejoined", joined)
	}
}

// A turn the server declared over must not be resumed either, even with no
// punctuation on the end.
//
// This is what punctuation alone could not see. turn_complete settles whatever
// is buffered, so "Thank you" is a finished turn that reads exactly like a
// phrase cut off mid-sentence — and the first version of this gate resumed it,
// gluing the next session's opening onto it.
func TestReconnectDoesNotResumeATurnTheServerEnded(t *testing.T) {
	b := incremental(Options{CompletePhrasesOnly: true, ClearAfterSeconds: 0})
	b.Feed("Thank you", 0)
	b.NoteTurnComplete(0.5) // no full stop, but the turn is over
	b.DrainNewCommits()

	b.NoteReconnect()
	b.Feed(" So we begin.", 1.5)

	for _, l := range texts(b.DisplayLines()) {
		if strings.Contains(l, "Thank you So we begin") {
			t.Errorf("a completed turn was glued to the next session: %q", l)
		}
	}
	if got := b.DrainNewCommits(); len(got) != 1 || got[0] != "So we begin." {
		t.Errorf("transcript got %q, want the new sentence on its own", got)
	}
}

// The distinction is how the line settled, not how it reads: the same text,
// settled by the idle timer instead, is still rejoined.
func TestTheSameTextIsResumedWhenATimerCutItOff(t *testing.T) {
	b := incremental(Options{CompletePhrasesOnly: true, ClearAfterSeconds: 0})
	b.Feed("Thank you", 0)
	b.Tick(2.0, SpeechUnknown) // the idle timer gave up — it was interrupted
	b.DrainNewCommits()

	b.NoteReconnect()
	b.Feed(" for coming.", 3.0)

	joined := strings.Join(texts(b.DisplayLines()), " | ")
	if !strings.Contains(joined, "Thank you for coming.") {
		t.Errorf("display = %q, want the halves rejoined", joined)
	}
}

// looksFinished decides which of the two cases applies.
func TestLooksFinished(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"A whole sentence.", true},
		{"A question?", true},
		{"An exclamation!", true},
		{"Cut off mid", false},
		{"Thank you, Dr.", false}, // an abbreviation is not the end
		{"", false},
		{"끝났습니다.", true},
		{"아직 말하는 중", false},
	}
	for _, c := range cases {
		if got := looksFinished(c.line); got != c.want {
			t.Errorf("looksFinished(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}
