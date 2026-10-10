package translator

import (
	"encoding/binary"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
)

// voiced is a blob of translated audio loud enough to count as speech.
func voiced() []byte {
	b := make([]byte, 0, 480)
	for i := range 240 {
		v := int16(8000)
		if i%2 == 1 {
			v = -8000
		}
		b = binary.LittleEndian.AppendUint16(b, uint16(v))
	}
	return b
}

// silent is a blob of the digital zeros both models send between utterances.
func silent() []byte { return make([]byte, 480) }

type warmRun struct {
	tr       *Translator
	first    *fakeSession
	second   *fakeSession
	opener   *recordingOpener
	collect  *collector
	starts   atomic.Int32
	quiet    atomic.Bool
	ring     *audio.Ring
	extra    []*fakeSession
	extraIdx atomic.Int32
	// openDelay holds the replacement's open, in nanoseconds.
	openDelay atomic.Int64
}

// startWarm runs a translator with warm handover on, up to the first
// session running. The replacement is second; any session after that comes
// from extra.
func startWarm(t *testing.T, prep bool, extra ...*fakeSession) *warmRun {
	t.Helper()
	return startWarmWith(t, func(o *Options) {
		o.AdaptiveSessionPrep = prep
	}, extra...)
}

func startWarmWith(t *testing.T, configure func(*Options), extra ...*fakeSession) *warmRun {
	t.Helper()
	fastTiming(t)
	q, w, r, p, g := HandoverQuiet, HandoverWarm, HandoverReserve, handoverPoll, HandoverGateQuiet
	HandoverQuiet, HandoverWarm, HandoverReserve, handoverPoll, HandoverGateQuiet =
		30*time.Millisecond, 40*time.Millisecond, 20*time.Millisecond, time.Millisecond, 60*time.Millisecond
	t.Cleanup(func() {
		HandoverQuiet, HandoverWarm, HandoverReserve, handoverPoll, HandoverGateQuiet = q, w, r, p, g
	})

	run := &warmRun{collect: &collector{}, first: newFakeSession(nil), second: newFakeSession(nil), extra: extra}
	run.opener = &recordingOpener{build: func(attempt int) (liveSession, error) {
		switch attempt {
		case 0:
			return run.first, nil
		case 1:
			time.Sleep(time.Duration(run.openDelay.Load()))
			return run.second, nil
		}
		i := int(run.extraIdx.Add(1)) - 1
		if i < len(run.extra) {
			return run.extra[i], nil
		}
		return newFakeSession(nil), nil
	}}
	cb := run.collect.callbacks()
	cb.OnSessionStart = func() { run.starts.Add(1) }
	run.ring = audio.NewRing(audio.ChunkBytes * 64)
	opts := Options{
		Ring:         run.ring,
		open:         run.opener.open,
		Callbacks:    cb,
		WarmHandover: true,
	}
	configure(&opts)
	if opts.AdaptiveSessionPrep {
		opts.QuietMoment = run.quiet.Load
	}
	tr, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	run.tr = tr
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.Stop)
	waitFor(t, "the first session to run", func() bool { return tr.State() == StateRunning })
	// The first session speaks, as a live one would have by now.
	run.first.push(Message{Audio: voiced()})
	waitFor(t, "the first session's audio", func() bool { return len(run.collect.audioBlobs()) == 1 })
	return run
}

func (r *warmRun) warn(left time.Duration) {
	r.first.push(Message{GoAway: true, GoAwayLeft: left})
}

func (r *warmRun) reconnects() int {
	r.collect.mu.Lock()
	defer r.collect.mu.Unlock()
	return r.collect.reconnects
}

func (r *warmRun) active() *sessionHolder {
	r.tr.mu.Lock()
	defer r.tr.mu.Unlock()
	return r.tr.active
}

// keepTalking has a session produce speech every few milliseconds until the
// returned function is called, which ends the sentence with a caption.
func keepTalking(s *fakeSession) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				select {
				case s.next <- Message{Audio: voiced()}:
				case <-s.done:
					return
				default:
				}
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		select {
		case s.next <- Message{TargetText: " and that is all."}:
		case <-s.done:
		}
	}
}

// The replacement opens on the warning, not at the switch — that is the warm
// up — while the old session stays the one being heard.
func TestTheReplacementOpensAtTheWarning(t *testing.T) {
	r := startWarm(t, false)
	stop := keepTalking(r.first)
	defer stop()
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	time.Sleep(30 * time.Millisecond)
	if r.reconnects() != 0 || r.active().live() != liveSession(r.first) {
		t.Error("switched while the old session was still speaking")
	}
}

// Both sessions hear the same audio from the warning on: what warms the
// replacement is the speech the old one is translating.
func TestTheReplacementHearsWhatTheOldSessionHears(t *testing.T) {
	r := startWarm(t, false)
	stop := keepTalking(r.first)
	defer stop()
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	waitFor(t, "the replacement to be fed", func() bool {
		r.tr.mu.Lock()
		defer r.tr.mu.Unlock()
		return r.tr.warming != nil
	})
	before := r.first.chunksSent()
	for range 5 {
		r.ring.Write(make([]byte, audio.ChunkBytes))
	}
	waitFor(t, "both sessions to be fed", func() bool {
		return r.first.chunksSent() >= before+5 && r.second.chunksSent() >= 5
	})
}

// Nothing a warming replacement says is heard: its captions would repeat the
// old session's, and its opening is where the buzz is.
func TestAWarmingReplacementIsNotHeard(t *testing.T) {
	r := startWarm(t, false)
	stop := keepTalking(r.first)
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	stop()
	// Kept from looking like a pause, so the switch cannot happen under the
	// test: the old session goes on speaking through it.
	stop = keepTalking(r.first)
	defer func() { stop() }()
	time.Sleep(10 * time.Millisecond)
	r.collect.mu.Lock()
	audioBefore := len(r.collect.audio)
	r.collect.mu.Unlock()

	r.second.push(Message{TargetText: "duplicate", SourceText: "중복"})
	time.Sleep(30 * time.Millisecond)
	for _, s := range r.collect.targets() {
		if strings.Contains(s, "duplicate") {
			t.Fatal("a warming replacement's caption was shown")
		}
	}
	r.collect.mu.Lock()
	for _, s := range r.collect.source {
		if s == "중복" {
			t.Error("a warming replacement's source caption was shown")
		}
	}
	r.collect.mu.Unlock()

	// Its audio: stop the old session, then let only the replacement speak.
	stop()
	stop = func() {}
	time.Sleep(20 * time.Millisecond) // what the old session had queued
	r.collect.mu.Lock()
	audioBefore = len(r.collect.audio)
	r.collect.mu.Unlock()
	if r.reconnects() == 0 {
		r.second.push(Message{Audio: voiced()})
		time.Sleep(5 * time.Millisecond)
		r.collect.mu.Lock()
		got := len(r.collect.audio)
		r.collect.mu.Unlock()
		if got != audioBefore && r.reconnects() == 0 {
			t.Error("a warming replacement's audio was played")
		}
	}
}

// The switch waits for both sessions to have fallen quiet, and for the
// replacement to be past its opening; then the old one is closed at once and
// the replacement is what is heard.
func TestTheSwitchComesInAPause(t *testing.T) {
	r := startWarm(t, false)
	stopFirst := keepTalking(r.first)
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	stopSecond := keepTalking(r.second)

	time.Sleep(60 * time.Millisecond)
	if r.reconnects() != 0 {
		t.Fatal("switched while both were speaking")
	}
	stopFirst()
	time.Sleep(60 * time.Millisecond)
	if r.reconnects() != 0 {
		t.Fatal("switched while the replacement was still speaking")
	}
	stopSecond()
	waitFor(t, "the switch", func() bool { return r.reconnects() == 1 })
	waitFor(t, "the old session to close", func() bool { return isClosed(r.first) })

	r.second.push(Message{TargetText: " after the switch"})
	waitFor(t, "the replacement to be heard", func() bool {
		for _, s := range r.collect.targets() {
			if s == " after the switch" {
				return true
			}
		}
		return false
	})
	st := r.tr.Stats()
	if st.QuietHandovers != 1 || st.ForcedHandovers != 0 {
		t.Errorf("handovers quiet %d forced %d, want 1 and 0", st.QuietHandovers, st.ForcedHandovers)
	}
}

// A replacement that has never spoken is not warm, however quiet it is: the
// opening it has not had yet would be heard.
func TestAQuietButColdReplacementIsNotSwitchedTo(t *testing.T) {
	r := startWarm(t, false)
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	time.Sleep(80 * time.Millisecond)
	if r.reconnects() != 0 {
		t.Error("switched to a replacement that had never spoken")
	}
	r.second.push(Message{Audio: silent(), TargetText: " The meeting starts now."})
	r.first.push(Message{TargetText: " The meeting starts now."})
	waitFor(t, "the switch once it has spoken", func() bool { return r.reconnects() == 1 })
}

// The switch never outlasts the server: with no pause, it is made anyway
// before the grace runs out, and counted as forced.
func TestWithNoPauseTheSwitchIsForcedInTime(t *testing.T) {
	r := startWarm(t, false)
	stopFirst := keepTalking(r.first)
	defer stopFirst()
	r.warn(150 * time.Millisecond)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	stopSecond := keepTalking(r.second)
	defer stopSecond()
	waitFor(t, "the forced switch", func() bool { return r.reconnects() == 1 })
	if st := r.tr.Stats(); st.ForcedHandovers != 1 {
		t.Errorf("forced handovers = %d", st.ForcedHandovers)
	}
}

// The old session dying mid-warm-up switches at once: there is nothing left
// to wait for.
func TestTheOldSessionDyingSwitchesAtOnce(t *testing.T) {
	r := startWarm(t, false)
	stop := keepTalking(r.second)
	defer stop()
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	_ = r.first.Close()
	waitFor(t, "the switch", func() bool { return r.reconnects() == 1 })
	if r.active().live() != liveSession(r.second) {
		t.Error("did not switch to the warm replacement")
	}
	if r.opener.attempts() != 2 {
		t.Errorf("%d sessions opened, want 2 — the warm one should be used", r.opener.attempts())
	}
}

// A replacement that dies while warming is no worse than today: the swap
// falls back to a cold one.
func TestAReplacementThatDiesWarmingFallsBackToACold(t *testing.T) {
	third := newFakeSession(nil)
	r := startWarm(t, false, third)
	stop := keepTalking(r.first)
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	_ = r.second.Close()
	waitFor(t, "a cold replacement", func() bool { return r.opener.attempts() == 3 })
	stop()
	waitFor(t, "the cold swap", func() bool { return r.reconnects() == 1 })
	if r.active().live() != liveSession(third) {
		t.Error("the cold replacement is not the active session")
	}
}

// A song is a better moment than a pause, and is taken even before the
// replacement is warm — nobody is listening at all.
func TestTheMusicGateIsAMomentToo(t *testing.T) {
	r := startWarm(t, true)
	stop := keepTalking(r.first)
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	time.Sleep(20 * time.Millisecond)
	if r.reconnects() != 0 {
		t.Fatal("switched before the gate closed")
	}
	r.quiet.Store(true)
	// The gate withholds input, not output: the old session is still
	// translating what was said before the song, and must be let finish.
	time.Sleep(40 * time.Millisecond)
	if r.reconnects() != 0 {
		t.Fatal("switched under the gate while the old session was still speaking")
	}
	stop()
	// Then it is switched, though the replacement has never spoken: nothing
	// is being said for it to warm up on, and nobody is listening.
	waitFor(t, "the switch under the gate", func() bool { return r.reconnects() == 1 })
	if st := r.tr.Stats(); st.PreppedSwaps != 1 || st.QuietHandovers != 1 {
		t.Errorf("prepped %d quiet %d", st.PreppedSwaps, st.QuietHandovers)
	}
}

// The red team's reproduction: a sentence the old session is finishing when
// the gate closes reaches the screen whole.
func TestASentenceFinishingAsTheSongStartsIsNotCut(t *testing.T) {
	r := startWarm(t, true)
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	r.first.push(Message{Audio: voiced(), TargetText: " Now let us"})
	r.quiet.Store(true)
	time.Sleep(10 * time.Millisecond)
	r.first.push(Message{Audio: voiced(), TargetText: " sing together."})
	waitFor(t, "the switch", func() bool { return r.reconnects() == 1 })
	got := strings.Join(r.collect.targets(), "")
	if !strings.Contains(got, " sing together.") {
		t.Errorf("captions %q: the old session was cut off mid-sentence", got)
	}
}

// The session the listener hears after a warm switch is not starting: its
// opening was spent unheard. The freeze gate must not be told otherwise, or
// it holds the next 1.2 s of the middle of a conversation for a decision.
func TestAWarmSwitchIsNotASessionStart(t *testing.T) {
	r := startWarm(t, false)
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	r.second.push(Message{Audio: voiced(), TargetText: " The meeting starts now."})
	r.first.push(Message{TargetText: " The meeting starts now."})
	waitFor(t, "the switch", func() bool { return r.reconnects() == 1 })
	r.second.push(Message{Audio: voiced()})
	waitFor(t, "the replacement's audio", func() bool { return len(r.collect.audioBlobs()) >= 2 })
	if n := r.starts.Load(); n != 1 {
		t.Errorf("%d session starts, want 1 — only the first session opened in earshot", n)
	}
}

// Stopping closes the replacement being warmed as well as the active session.
func TestStoppingClosesTheWarmingReplacement(t *testing.T) {
	r := startWarm(t, false)
	stop := keepTalking(r.first)
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	stop()
	r.first.push(Message{Audio: voiced()}) // keep it from looking like a pause
	r.tr.Stop()
	if !isClosed(r.second) {
		t.Error("the warming replacement outlived Stop")
	}
}

// A warning that states no grace leaves no budget to wait in, so the swap is
// the cold one it always was.
func TestAWarningWithNoTimeLeftSwapsCold(t *testing.T) {
	r := startWarm(t, false)
	r.warn(0)
	waitFor(t, "the swap", func() bool { return r.reconnects() == 1 })
	if st := r.tr.Stats(); st.QuietHandovers+st.ForcedHandovers != 0 {
		t.Errorf("counted a handover that never warmed: %+v", st)
	}
	r.tr.mu.Lock()
	retiring := r.tr.retiring
	r.tr.mu.Unlock()
	if retiring == nil && !isClosed(r.first) {
		t.Error("the cold swap neither retired nor closed the old session")
	}
}

func TestTheSummaryReportsHandovers(t *testing.T) {
	got := Stats{QuietHandovers: 2, ForcedHandovers: 1}.Summary()
	if !strings.Contains(got, "handovers=2/3") {
		t.Errorf("summary = %q", got)
	}
}

// A shared pause is a switch point only when it is the same pause: both
// sessions silent, both at a sentence end, and that sentence the same one. A
// fresh OpenAI session runs ahead of the one it replaces, and switching when
// the two were a sentence apart lost the sentence in between.
func TestTheSwitchWantsTheSamePauseInBoth(t *testing.T) {
	q := HandoverQuiet
	HandoverQuiet = 200 * time.Millisecond
	t.Cleanup(func() { HandoverQuiet = q })

	session := func(quiet time.Duration, captions string) *sessionHolder {
		h := newSessionHolder()
		h.noteText(captions)
		h.sentenceEnded.Store(endsSentence(captions))
		h.lastVoice.Store(time.Now().Add(-quiet).UnixNano())
		return h
	}
	const (
		said  = " The average delay was 3.3 seconds. The maximum was 5.2 seconds."
		same  = " average delay of 3.3 seconds. And the maximum was 5.2 seconds."
		ahead = " The maximum was 5.2 seconds. This figure is a bit higher than expected."
		// The red team's case: the replacement a numeric sentence ahead.
		saidFirst = " The average delay was 3.3 seconds."
		aheadNum  = " The average delay was 3.3 seconds. The maximum was 5.2 seconds."
	)
	long, short := 250*time.Millisecond, 150*time.Millisecond
	for _, c := range []struct {
		name      string
		old, next *sessionHolder
		want      bool
	}{
		{"the same sentence, both quiet", session(long, said), session(long, same), true},
		{"the replacement a sentence ahead", session(long, said), session(long, ahead), false},
		{"the replacement a numeric sentence ahead", session(long, saidFirst), session(long, aheadNum), false},
		{"the same shape, different numbers", session(long, " Sales rose 5% in the first quarter."),
			session(long, " Sales rose 7% in the second quarter."), false},
		{"old mid-sentence", session(long, said+" This figure"), session(long, same), false},
		{"new mid-sentence", session(long, said), session(long, same+" This"), false},
		{"old not quiet long enough", session(short, said), session(long, same), false},
		{"new not quiet long enough", session(long, said), session(short, same), false},
		{"nothing said yet", session(long, ""), session(long, ""), false},
		// The old session's unfinished sentence reads just like the
		// replacement's finished one; only the sentence end tells them apart.
		{"old mid-sentence, its words matching", session(long, " One. The maximum was 5.2 seconds and"),
			session(long, " One. The maximum was 5.2 seconds."), false},
	} {
		if got := pauseShared(c.old, c.next); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLastSentenceAndSimilarity(t *testing.T) {
	if got := lastSentence(" One. Two three? Four five."); got != "Four five" {
		t.Errorf("lastSentence = %q", got)
	}
	if got := lastSentence(" 회의를 시작하겠습니다. 먼저 정리해 보겠습니다."); got != "먼저 정리해 보겠습니다" {
		t.Errorf("lastSentence = %q", got)
	}
	if got := lastSentence("No terminator at all"); got != "No terminator at all" {
		t.Errorf("lastSentence = %q", got)
	}
	// A decimal point is not a sentence end.
	if got := lastSentence(" One. The average delay was 3.3 seconds."); got != "The average delay was 3.3 seconds" {
		t.Errorf("lastSentence = %q", got)
	}
	if got := lastSentence(" Costs were 2.5 billion. Revenue was 1.5."); got != "Revenue was 1.5" {
		t.Errorf("lastSentence = %q", got)
	}
	// A full stop after a number, before a word, is a sentence end: the
	// decimal test needs a digit on both sides, not either.
	if got := lastSentence(" The year was 2019. Then it rose."); got != "Then it rose" {
		t.Errorf("lastSentence = %q", got)
	}
	if got := lastSentence(" It rose. 2020 was better."); got != "2020 was better" {
		t.Errorf("lastSentence = %q", got)
	}
	// OpenAI's fragments sometimes drop their leading space ("serverwas",
	// measured), so a sentence end can sit right against a number. A digit
	// after the stop is not enough to make it a decimal point.
	if got := lastSentence(" It rose.2020 was better."); got != "2020 was better" {
		t.Errorf("lastSentence = %q", got)
	}
	// Measured pairs from spike o3_handover: one sentence translated by two
	// sessions, and two different sentences.
	if s := similarity("If you open a new connection in advance at that point, the switch-over takes about 400 ms",
		"At that point, the switch-over for replacement is about 400 milliseconds"); s < HandoverMatch {
		t.Errorf("one sentence, two wordings: %.2f, want at least %.2f", s, HandoverMatch)
	}
	if s := similarity("If you open a new connection in advance at that point, the switch-over takes about 400 ms",
		"There are no sentence breaks"); s >= HandoverMatch {
		t.Errorf("two different sentences: %.2f, want under %.2f", s, HandoverMatch)
	}
	if s := similarity("회의를 시작하겠습니다", "회의를 시작합니다"); s < HandoverMatch {
		t.Errorf("Korean, one sentence: %.2f", s)
	}
	if s := similarity("", "anything"); s != 0 {
		t.Errorf("empty: %.2f", s)
	}
}

// The caption tail is kept short, and cut on a character boundary.
func TestTheCaptionTailStaysShortAndWhole(t *testing.T) {
	h := newSessionHolder()
	for range 200 {
		h.noteText(" 한국어 문장입니다.")
	}
	got := h.recentText()
	if len(got) > recentTextBytes || !utf8.ValidString(got) {
		t.Errorf("tail of %d bytes, valid UTF-8 %v", len(got), utf8.ValidString(got))
	}
}

func TestSentenceEnds(t *testing.T) {
	for text, want := range map[string]bool{
		" done.": true, " really?": true, " stop!": true, " he said \"go.\"": true,
		" 끝났습니다.": true, " おわり。": true, " and so": false, " 3.3": false, "": false,
		" seconds. This": false,
	} {
		if got := endsSentence(text); got != want {
			t.Errorf("endsSentence(%q) = %v, want %v", text, got, want)
		}
	}
}

// Silence is read from the audio, not from when it arrived: the model's
// blobs come every ~250 ms, so arrival alone cannot see a shorter pause.
func TestQuietIsMeasuredInsideTheBlob(t *testing.T) {
	blob := append(voiced()[:240], make([]byte, 2*24000/10)...) // voice, then 100 ms of zeros
	if d, voiced := trailingSilence(blob); !voiced || d < 90*time.Millisecond || d > 110*time.Millisecond {
		t.Errorf("trailing silence %v (voiced %v), want about 100 ms", d, voiced)
	}
	if _, voiced := trailingSilence(silent()); voiced {
		t.Error("a silent blob counted as voice")
	}
	h := newSessionHolder()
	h.noteOutput(Message{Audio: blob})
	if q := h.quietFor(); q < 90*time.Millisecond {
		t.Errorf("quiet for %v right after a blob ending in 100 ms of silence", q)
	}
}

// switchNow brings a warm run to a switch: both sessions finish the same
// sentence and fall quiet, the replacement having spoken.
func (r *warmRun) switchNow(t *testing.T) {
	t.Helper()
	r.second.push(Message{Audio: voiced(), TargetText: " The meeting starts now."})
	r.first.push(Message{TargetText: " The meeting starts now."})
	waitFor(t, "the switch", func() bool { return r.reconnects() == 1 })
}

// A warming replacement's resumption handles are not the run's: until it is
// switched to, a reconnect must resume the session actually being heard.
func TestAWarmingReplacementWritesNoHandle(t *testing.T) {
	r := startWarm(t, false)
	stop := keepTalking(r.first)
	defer func() { stop() }()
	r.first.push(Message{ResumeHandle: "old-handle"})
	waitFor(t, "the old handle", func() bool {
		r.tr.mu.Lock()
		defer r.tr.mu.Unlock()
		return r.tr.handle == "old-handle"
	})
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	r.second.push(Message{ResumeHandle: "warming-handle"})
	time.Sleep(20 * time.Millisecond)
	r.tr.mu.Lock()
	got := r.tr.handle
	r.tr.mu.Unlock()
	if got != "old-handle" {
		t.Errorf("handle = %q while the replacement was warming", got)
	}
}

// Quiet at a sentence end and speaking for less than HandoverWarm is not
// warm: the replacement's opening would be heard.
func TestTheReplacementMustHaveSpokenForAWhile(t *testing.T) {
	r := startWarm(t, false)
	w := HandoverWarm
	HandoverWarm = 150 * time.Millisecond
	t.Cleanup(func() { HandoverWarm = w })
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	start := time.Now()
	r.switchNow(t)
	if waited := time.Since(start); waited < 140*time.Millisecond {
		t.Errorf("switched %v after the replacement first spoke, before HandoverWarm", waited)
	}
}

// At a warm switch the old session is silenced at once and closed at once —
// not left to a retirement window in which it would still be heard.
func TestTheOldSessionIsSilencedAndClosedAtTheSwitch(t *testing.T) {
	r := startWarm(t, false)
	grace := RetireGrace
	RetireGrace = 10 * time.Second
	t.Cleanup(func() { RetireGrace = grace })
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	r.switchNow(t)
	waitFor(t, "the old session to close", func() bool { return isClosed(r.first) })
	select {
	case r.first.next <- Message{TargetText: " late from the old session"}:
	default:
	}
	time.Sleep(20 * time.Millisecond)
	for _, s := range r.collect.targets() {
		if strings.Contains(s, "late from the old session") {
			t.Error("the old session was heard after the switch")
		}
	}
}

// After the switch the new active session gets each chunk once — it is no
// longer also the warming one.
func TestAfterTheSwitchEachChunkIsSentOnce(t *testing.T) {
	r := startWarm(t, false)
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	r.switchNow(t)
	before := r.second.chunksSent()
	for range 10 {
		r.ring.Write(make([]byte, audio.ChunkBytes))
	}
	waitFor(t, "the chunks", func() bool { return r.second.chunksSent() >= before+10 })
	time.Sleep(20 * time.Millisecond)
	if got := r.second.chunksSent() - before; got != 10 {
		t.Errorf("%d sends for 10 chunks", got)
	}
}

// A switch the listener cannot hear is not a reconnect to show: the
// indicator stays on running.
func TestAWarmSwitchDoesNotShowReconnecting(t *testing.T) {
	r := startWarm(t, false)
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	r.switchNow(t)
	if r.collect.sawState(StateReconnecting) {
		t.Error("a warm switch showed the reconnecting state")
	}
}

// The wait spends at most the server's grace less the reserve, and never
// more than HandoverMaxWait however long the grace.
func TestTheWaitIsBounded(t *testing.T) {
	r, m := HandoverReserve, HandoverMaxWait
	HandoverReserve, HandoverMaxWait = 5*time.Second, 45*time.Second
	t.Cleanup(func() { HandoverReserve, HandoverMaxWait = r, m })
	for grace, want := range map[time.Duration]time.Duration{
		50 * time.Second: 45 * time.Second,
		30 * time.Second: 25 * time.Second,
		10 * time.Minute: 45 * time.Second,
		4 * time.Second:  -time.Second,
	} {
		if got := handoverBudget(grace); got != want {
			t.Errorf("grace %v: budget %v, want %v", grace, got, want)
		}
	}
}

// A replacement that dies as the pause comes is not switched to: the
// healthy old session would be closed for nothing.
func TestADeadReplacementIsNeverSwitchedTo(t *testing.T) {
	third := newFakeSession(nil)
	r := startWarm(t, false, third)
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	r.second.push(Message{Audio: voiced(), TargetText: " The meeting starts now."})
	time.Sleep(time.Duration(float64(HandoverWarm) * 1.5))
	_ = r.second.Close()
	r.first.push(Message{TargetText: " The meeting starts now."})
	waitFor(t, "a swap", func() bool { return r.reconnects() == 1 })
	if r.active().live() == liveSession(r.second) {
		t.Error("switched to a replacement that had closed")
	}
}

// The replacement hears what the active session hears after the same gates:
// with the silence gate on, held-back silence goes to neither.
func TestTheSilenceGateAppliesToTheReplacementToo(t *testing.T) {
	fastTiming(t)
	hold := silenceHold
	silenceHold = 5 * time.Millisecond
	t.Cleanup(func() { silenceHold = hold })
	r := startWarmWith(t, func(o *Options) { o.SilenceGate = true })
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	waitFor(t, "the replacement to be fed", func() bool {
		r.tr.mu.Lock()
		defer r.tr.mu.Unlock()
		return r.tr.warming != nil
	})
	for range 20 {
		r.ring.Write(make([]byte, audio.ChunkBytes))
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	a, b := r.first.chunksSent(), r.second.chunksSent()
	if b > a {
		t.Errorf("the replacement was sent %d chunks of silence, the active session %d", b, a)
	}
	if a >= 20 {
		t.Fatalf("setup: the silence gate let %d of 20 silent chunks through", a)
	}
}

// Closing takes a moment, and a closing session can still deliver what it had
// received. Whatever the old session says after the swap the replacement says
// too, so it is muted in the same lock as the swap, before the close begins.
func TestTheOldSessionIsMutedBeforeItFinishesClosing(t *testing.T) {
	r := startWarm(t, false)
	gate := make(chan struct{})
	r.first.mu.Lock()
	r.first.closeGate = gate
	r.first.mu.Unlock()
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	r.switchNow(t)
	// The supervisor is now inside the old session's Close, held at the gate;
	// the old receive loop is still running.
	r.first.push(Message{TargetText: " said while closing", Audio: voiced()})
	time.Sleep(30 * time.Millisecond)
	close(gate)
	for _, s := range r.collect.targets() {
		if strings.Contains(s, "said while closing") {
			t.Error("the old session was heard while it closed")
		}
	}
}

// A replacement that has closed is never handed over, and is dropped — the
// select in handOver can pick a ready pause over a ready death, so this is the
// only place the outcome is certain.
func TestAClosedReplacementIsNotClaimed(t *testing.T) {
	tr, err := New(Options{Ring: audio.NewRing(4096), open: (&recordingOpener{}).open})
	if err != nil {
		t.Fatal(err)
	}
	next := newSessionHolder()
	tr.warming = next
	next.markClosed()
	if got := tr.claim(next, true, 0); got != nil {
		t.Error("claimed a replacement that had closed")
	}
	if tr.warming != nil {
		t.Error("the dead replacement is still being fed")
	}
	if st := tr.Stats(); st.QuietHandovers+st.ForcedHandovers != 0 {
		t.Errorf("counted a handover that never happened: %+v", st)
	}
	alive := newSessionHolder()
	if got := tr.claim(alive, true, time.Second); got != alive || tr.Stats().QuietHandovers != 1 {
		t.Error("a live replacement was not claimed and counted")
	}
}

// Warm means speaking for HandoverWarm since the first blob — not since the
// latest. Measured from the latest, a replacement that talks without pause
// is never warm until it stops, and then only HandoverWarm later.
func TestWarmthCountsFromTheFirstBlob(t *testing.T) {
	r := startWarm(t, false)
	w := HandoverWarm
	HandoverWarm = 150 * time.Millisecond
	t.Cleanup(func() { HandoverWarm = w })
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	stop := keepTalking(r.second)
	time.Sleep(250 * time.Millisecond)
	stop() // ends with " and that is all."
	stopped := time.Now()
	r.first.push(Message{TargetText: " and that is all."})
	waitFor(t, "the switch", func() bool { return r.reconnects() == 1 })
	if d := time.Since(stopped); d > 120*time.Millisecond {
		t.Errorf("switched %v after the replacement stopped talking; warm since its first blob, it was due within the %v pause",
			d, HandoverQuiet)
	}
}

// The wait's deadline is set by the server's grace, which runs from the
// warning — so time spent opening the replacement comes out of it, and the
// switch is not pushed back by a slow connect.
func TestASlowOpenComesOutOfTheBudget(t *testing.T) {
	r := startWarm(t, false)
	stop := keepTalking(r.first) // never a pause: the deadline decides
	defer stop()
	r.openDelay.Store(int64(300 * time.Millisecond))
	warned := time.Now()
	r.warn(HandoverReserve + 350*time.Millisecond)
	waitFor(t, "the forced switch", func() bool { return r.reconnects() == 1 })
	if d := time.Since(warned); d > 520*time.Millisecond {
		t.Errorf("switched %v after the warning; the grace allowed %v", d, 350*time.Millisecond)
	}
}

// voicedFor is a blob of continuous voice ms long, at 24 kHz.
func voicedFor(ms int) []byte {
	n := 24 * ms
	b := make([]byte, 0, 2*n)
	for i := range n {
		v := int16(8000)
		if i%2 == 1 {
			v = -8000
		}
		b = binary.LittleEndian.AppendUint16(b, uint16(v))
	}
	return b
}

// The music-gate switch at the rate audio really arrives: 200 ms of voice
// every 210 ms, the old session mid-sentence. With the real HandoverGateQuiet
// it must hold until the sentence is finished and a second has passed — the
// red team's reproduction switched 0.43 s after the gate closed, 3 of 3.
func TestTheGateHoldsAtTheRealArrivalRate(t *testing.T) {
	r := startWarm(t, true)
	HandoverGateQuiet = time.Second // the shipped value, not startWarm's
	r.warn(10 * time.Second)
	waitFor(t, "the replacement to open", func() bool { return r.opener.attempts() == 2 })
	r.quiet.Store(true)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(210 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				// Not push: a wrong switch closes this session, and a send
				// to it would hang the test instead of failing it.
				select {
				case r.first.next <- Message{Audio: voicedFor(200), TargetText: " and"}:
				case <-r.first.done:
					return
				}
			}
		}
	}()
	time.Sleep(1500 * time.Millisecond)
	close(stop)
	<-done
	if r.reconnects() != 0 {
		t.Fatal("switched under the gate while the old session was mid-sentence")
	}
	r.first.push(Message{Audio: voicedFor(200), TargetText: " that is all."})
	finished := time.Now()
	deadline := time.Now().Add(3 * time.Second)
	for r.reconnects() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if r.reconnects() != 1 {
		t.Fatal("never switched once the sentence was over and a second had passed")
	}
	if d := time.Since(finished); d < 900*time.Millisecond {
		t.Errorf("switched %v after the sentence ended, before HandoverGateQuiet", d)
	}
}

// Under the gate the replacement must be quiet too: if it has been saying
// something, it is the tail of the speech before the song, and the room
// would hear it twice.
func TestTheGateWaitsForTheReplacementToo(t *testing.T) {
	q := HandoverGateQuiet
	HandoverGateQuiet = time.Second
	t.Cleanup(func() { HandoverGateQuiet = q })
	session := func(quiet time.Duration, captions string) *sessionHolder {
		h := newSessionHolder()
		h.noteText(captions)
		h.sentenceEnded.Store(endsSentence(captions))
		h.lastVoice.Store(time.Now().Add(-quiet).UnixNano())
		return h
	}
	for _, c := range []struct {
		name      string
		old, next *sessionHolder
		want      bool
	}{
		{"both long quiet, old at a sentence end", session(1200*time.Millisecond, " done."), session(1200*time.Millisecond, ""), true},
		{"old mid-sentence", session(1200*time.Millisecond, " and"), session(1200*time.Millisecond, ""), false},
		{"old quiet only a blob gap", session(300*time.Millisecond, " done."), session(1200*time.Millisecond, ""), false},
		{"replacement still talking", session(1200*time.Millisecond, " done."), session(300*time.Millisecond, " the tail"), false},
	} {
		if got := gateQuiet(c.old, c.next); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
