// Command capscenario replays realistic caption sessions against the real
// buffer and reports what the reader would actually see.
//
// Written to check seven reported observations from a 67-minute
// complete-phrases session rather than reason about them. It drives
// captions.Buffer on the same 100 ms tick the window uses, feeds fragments at
// the measured ~1/second, and supplies audio peaks at measured speech levels —
// which is the point: the peak decides whether the buffer believes anybody is
// talking, and that decision gates the clear.
//
//	go run ./spikes/capscenario
package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/dennis2lee/live-translation/src_go/internal/captions"
)

// Mirrors internal/ui/window.go. Duplicated rather than imported because that
// package pulls in Fyne, and this has to run headless.
const (
	tickSeconds = 0.1
	speechPeak  = 0.0056 // about -45 dBFS; mirrors ui.SpeechPeak
	silentPeak  = 0.002  // about -54 dBFS
)

// Measured on 2026-08-06 with the level meter: speech played into the room
// peaked -32.7 to -38.4 dBFS, and CLAUDE.md records a built-in mic at -36.
var (
	peakSpeaking = math.Pow(10, -36.0/20) // 0.0158
	peakQuiet    = math.Pow(10, -70.0/20) // 0.0003
)

func speechFor(peak float64) captions.Speech {
	if peak >= speechPeak {
		return captions.SpeechActive
	}
	return captions.SpeechQuiet
}

// sim runs a buffer on the tick loop and records what the display showed.
type sim struct {
	buf   *captions.Buffer
	now   float64
	peak  float64
	log   []string
	seen  map[string]bool // every line the reader ever saw
	blank float64         // when the display went empty, -1 when it is not
	worst float64         // longest unbroken stretch with an empty box
}

func newSim(completeOnly bool, maxLines int) *sim {
	return &sim{
		buf: captions.New(captions.Options{
			MaxLines:            maxLines,
			Policy:              captions.PolicyIncremental,
			ClearAfterSeconds:   captions.ClearAfterSeconds,
			LineHoldSeconds:     captions.LineHoldSeconds,
			CompletePhrasesOnly: completeOnly,
		}),
		peak:  peakQuiet,
		seen:  map[string]bool{},
		blank: -1,
	}
}

// observe records the display at this instant: which lines the reader saw, and
// how long the box has been empty. Called from every place time moves, so a
// line that appears and is replaced between ticks is still counted as seen.
func (s *sim) observe() {
	lines := s.buf.DisplayLines()
	for _, l := range lines {
		s.seen[l.Text] = true
	}
	if len(lines) == 0 {
		if s.blank < 0 {
			s.blank = s.now
		}
		if d := s.now - s.blank; d > s.worst {
			s.worst = d
		}
		return
	}
	s.blank = -1
}

func (s *sim) shown() string {
	var parts []string
	for _, l := range s.buf.DisplayLines() {
		parts = append(parts, l.Text)
	}
	return strings.Join(parts, " | ")
}

// advance ticks for d seconds, tracking how long the display stays empty.
func (s *sim) advance(d float64) {
	for t := 0.0; t < d-1e-9; t += tickSeconds {
		s.now += tickSeconds
		s.buf.Tick(s.now, speechFor(s.peak))
		s.buf.DrainNewCommits()
		s.observe()
	}
}

// say feeds one sentence as ~1/second fragments while the input carries speech.
func (s *sim) say(sentence string) {
	s.peak = peakSpeaking
	for i, frag := range fragments(sentence) {
		if i > 0 {
			s.advance(1.0)
		}
		s.now += 1e-6
		s.buf.Feed(frag, s.now)
		s.buf.DrainNewCommits()
		s.observe()
	}
}

// fragments splits a sentence the way the API delivers it: a few words at a
// time, each carrying its own leading space.
func fragments(sentence string) []string {
	words := strings.Fields(sentence)
	var out []string
	for i := 0; i < len(words); i += 3 {
		end := min(i+3, len(words))
		out = append(out, " "+strings.Join(words[i:end], " "))
	}
	return out
}

func (s *sim) note(format string, a ...any) {
	s.log = append(s.log, fmt.Sprintf("  %6.1fs  "+format, append([]any{s.now}, a...)...))
}

func (s *sim) dump() {
	for _, l := range s.log {
		fmt.Println(l)
	}
}

func rule(title string) { fmt.Printf("\n=== %s\n", title) }

func main() {
	scenario1()
	scenario2()
	scenario3()
	scenario4()
	scenario5()
	scenario6()
}

// 1. Silence clears the box two seconds after the backlog drains.
func scenario1() {
	rule("1. clear after the backlog drains and two seconds of silence")
	s := newSim(true, 3)
	s.say("This is the first complete sentence of the talk.")
	s.say("And here is a second one to put two lines up.")
	s.peak = peakQuiet
	start := s.now
	drained, cleared := -1.0, -1.0
	for i := 0; i < 300; i++ {
		s.advance(tickSeconds)
		if drained < 0 && s.buf.OutputComplete() && len(s.buf.QueuedLines()) == 0 {
			drained = s.now
		}
		if len(s.buf.DisplayLines()) == 0 {
			cleared = s.now
			break
		}
	}
	fmt.Printf("  silence began at %.1fs; backlog drained %.1fs later; box cleared %.1fs later\n",
		start, drained-start, cleared-drained)
	fmt.Printf("  VERDICT: %s\n", verdict(cleared > 0 && cleared-drained >= 1.5 && cleared-drained <= 3.5,
		"clears about 2s after draining", "did NOT clear as specified"))
}

// 2. A full stop should put the sentence up at once, whatever its length.
func scenario2() {
	rule("2. latency from the full stop to the line appearing (complete phrases)")
	for _, sentence := range []string{
		"Yes, I agree.",
		"Now, at this time, let us offer worship to the Lord and move forward.",
	} {
		s := newSim(true, 3)
		s.say(sentence)
		at := s.now
		shownAt := -1.0
		for i := 0; i < 100; i++ {
			s.advance(tickSeconds)
			if len(s.buf.DisplayLines()) > 0 {
				shownAt = s.now
				break
			}
		}
		runes := len([]rune(sentence))
		fmt.Printf("  %-70q %2d runes -> %+.1fs\n", trunc(sentence), runes, shownAt-at)
	}
	fmt.Println("  (both settle on the full stop itself now; the old blanket")
	fmt.Println("   20-rune minimum held the short one for the 1.2s idle timer)")
}

// 3. A burst of settled sentences, and when each reaches the screen.
func scenario3() {
	rule("3. when each of five quick sentences reaches the screen")
	s := newSim(true, 3)
	// A real burst: the model settles several sentences inside one tick.
	for i := 1; i <= 5; i++ {
		s.now += 1e-6
		s.buf.Feed(fmt.Sprintf(" This is complete sentence number %d of the burst.", i), s.now)
		s.buf.DrainNewCommits()
		s.observe()
	}
	s.advance(6.0)
	shown := 0
	for i := 1; i <= 5; i++ {
		if s.seen[fmt.Sprintf("This is complete sentence number %d of the burst.", i)] {
			shown++
		}
	}
	fmt.Printf("  %d of 5 sentences reached the screen at some point\n", shown)
	fmt.Printf("  VERDICT: %s\n", verdict(shown == 5,
		"every sentence was seen", "sentences were dropped without ever being shown"))
}

// 4. Does the box go blank while somebody is still talking?
func scenario4() {
	rule("4. blank display during continuous speech (the reported symptom)")
	s := newSim(true, 3)
	s.say("The first sentence of the passage is read aloud now.")
	// A natural pause between sentences, the input still carrying room tone.
	s.peak = peakSpeaking
	s.worst = 0
	s.advance(3.0)
	blank := s.worst > 0
	fmt.Printf("  after a 3s pause with the speaker still in the room (peak %.1f dBFS):\n",
		20*math.Log10(peakSpeaking))
	fmt.Printf("  buffer saw speech=%v, display=%q\n",
		speechFor(peakSpeaking) == captions.SpeechActive, s.shown())
	fmt.Printf("  VERDICT: %s\n", verdict(!blank,
		"the previous sentence stayed up", "the box went BLANK while the speaker was there"))
}

// 5. After a clear, how long is the box empty while a long sentence translates?
func scenario5() {
	rule("5. blank time while a long sentence is being translated")
	s := newSim(true, 3)
	s.say("A short opening line to fill the box.")
	s.peak = peakQuiet
	s.advance(5.0) // let the clear fire
	if len(s.buf.DisplayLines()) != 0 {
		fmt.Println("  (the box had not cleared; scenario does not apply)")
		return
	}
	s.worst = 0
	s.say("Now truly a revival day has come to us and has come upon this whole " +
		"congregation gathered here this morning in one accord.")
	s.advance(1.0)
	fmt.Printf("  longest blank stretch while the sentence was translated: %.1fs\n", s.worst)
	fmt.Printf("  VERDICT: %s\n", verdict(s.worst < 2.0,
		"acceptable", "the reader stares at an empty box for this long"))
}

func verdict(ok bool, good, bad string) string {
	if ok {
		return "OK — " + good
	}
	return "PROBLEM — " + bad
}

func trunc(s string) string {
	if len([]rune(s)) <= 60 {
		return s
	}
	return string([]rune(s)[:57]) + "..."
}

// 6. What a reconnect does to the display in complete-phrases mode.
//
// go_away arrives every nine minutes, so this happens seven times in a
// sermon. NoteReconnect asks the buffer to treat the next fragment as a
// continuation of the last settled line, which means pulling that line back
// off the screen into pending — and complete-phrases mode withholds pending.
func scenario6() {
	rule("6. a reconnect in complete-phrases mode")
	for _, tail := range []struct{ what, text string }{
		{"a finished sentence", "The reading is finished here."},
		{"cut off mid-sentence", "The reading was interrupted while"},
	} {
		s := newSim(true, 3)
		s.say("An earlier sentence that is already up.")
		s.say(tail.text)
		s.advance(1.5)
		before := s.shown()

		s.buf.NoteReconnect()
		s.now += 0.5 // the measured swap, 373-870 ms
		s.say("and then the speaker carried on.")
		after := s.shown()

		fmt.Printf("  last line %s:\n", tail.what)
		fmt.Printf("    before: %q\n", before)
		fmt.Printf("    after:  %q\n", after)
		fmt.Printf("    transcript: %q\n", s.buf.DrainNewCommits())
		lost := before != "" && after == ""
		fmt.Printf("    VERDICT: %s\n", verdict(!lost,
			"something stayed on screen", "the display went empty across the reconnect"))
	}
}
