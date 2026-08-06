package captions

import (
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
	// The queue holds two rather than three: once it runs more than max_lines
	// behind it releases one immediately, since pacing a backlog that deep
	// would put the display further behind the audio than it can recover from.
	if got := len(b.DisplayLines()); got != 2 {
		t.Fatalf("display holds %d lines, want 2", got)
	}
	if got := len(b.QueuedLines()); got != 2 {
		t.Fatalf("queue holds %d lines, want 2", got)
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
	b.Tick(1.2, SpeechUnknown)
	if got := texts(b.DisplayLines()); len(got) != 1 || got[0] == want[0] {
		t.Errorf("display = %q, want the second line after the hold", got)
	}
}

// --- complete-phrases mode ------------------------------------------------

func TestCompletePhrasesWithholdsUnsettledText(t *testing.T) {
	b := incremental(Options{CompletePhrasesOnly: true, ClearAfterSeconds: 0})
	b.Feed("Revenue is up", 0)
	if got := b.DisplayLines(); len(got) != 0 {
		t.Errorf("display = %v, want nothing while the phrase is unsettled", got)
	}
	b.Feed(" fourteen percent this quarter.", 0.5)
	if got := texts(b.DisplayLines()); len(got) != 1 {
		t.Errorf("display = %q, want the settled sentence", got)
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
	// Outside the mode the same fragment settles, as it always did.
	plain := incremental(Options{ClearAfterSeconds: 0})
	plain.Feed("Dr.", 0)
	if len(plain.CommittedLines()) != 1 {
		t.Error("default mode should still settle on a full stop")
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

				if got := len(b.CommittedLines()); got > b.MaxLines() {
					t.Fatalf("step %d: %d committed lines exceed max_lines %d", step, got, b.MaxLines())
				}
				if got := len(b.DisplayLines()); got > b.MaxLines() {
					t.Fatalf("step %d: %d display lines exceed max_lines %d", step, got, b.MaxLines())
				}
				if b.IsEmpty() && len(b.DisplayLines()) > 0 {
					t.Fatalf("step %d: IsEmpty but display is %v", step, b.DisplayLines())
				}
				if mode {
					for _, l := range b.DisplayLines() {
						if !l.Settled {
							t.Fatalf("step %d: unsettled text shown in complete-phrases mode", step)
						}
					}
				}

				// The queue is bounded by max_lines once the display has caught
				// up. Shrinking max_lines exceeds it until the next tick, which
				// is why this ticks before asserting.
				b.Tick(now, SpeechUnknown)
				if got := len(b.QueuedLines()); got > b.MaxLines() {
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
