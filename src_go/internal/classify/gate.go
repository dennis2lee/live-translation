package classify

// The music gate: a hysteresis machine and the streaming wrapper around it.
//
// The machine (gateMachine) is pure — it consumes per-frame verdicts and
// per-chunk audio and decides what may be sent — so every rule below is
// testable without a model or a clock. The wrapper (Gate) owns the model, a
// sliding sample window, and the worker goroutine that keeps 52 ms of
// inference (measured, BenchmarkScores) out of the send loop's way.
//
// The asymmetry is the design:
//
//   - The gate closes slowly: only after HoldSeconds of *consecutive* musical
//     frames. A verse's first bars getting translated is a nuisance; a
//     sentence's first words getting eaten is a defect.
//   - It opens deliberately, and retroactively: ReleaseSeconds of consecutive
//     *clear* frames reopen it, and the pre-roll — the last PrerollChunks of
//     suppressed audio — is flushed to the session first, so the delay costs
//     no words. The server has always been fine with a burst; the reconnect
//     path flushes an accumulated ring the same way.
//
// Until 1.4.0 the gate opened on the *first* clear frame. That was wrong, and
// wrong in the expensive direction: the classifier drops below the threshold
// for one frame all the time inside real music — measured on ten real pieces,
// isolated one-frame dips are the single commonest event (spike y5_flap) — and
// each one reopened the gate *and* flushed 1.5 s of music into the session.
// A release hold is affordable because a song and the speech after it are
// never butt-joined; there is always a pause, and the countdown runs during
// it. The pre-roll is sized for the case where there is not.

import (
	"sync"
	"time"
)

// GateTuning is what the Preferences sliders move. Zero values are replaced
// by the defaults; see DefaultTuning.
type GateTuning struct {
	// Threshold is the music-family score at which a frame counts as musical.
	Threshold float64
	// HoldSeconds of consecutive musical frames close the gate.
	HoldSeconds float64
	// ReleaseSeconds of consecutive clear frames reopen it. The pre-roll
	// makes the wait free; see PrerollChunks.
	ReleaseSeconds float64
}

// Margin is how far the music score must sit above the speech score, on top
// of the threshold. Not user-tunable: it exists to keep singing-with-speech
// ambiguity from flapping the gate, and one slider (the threshold) is the
// tuning story. Changing this needs a measurement, not a preference.
const Margin = 0.05

// Chunk and frame geometry, tied to the pipeline's 100 ms chunks and the
// model's 0.48 s hop.
const (
	chunkMS = 100
	// hopSamples is the classification cadence: one verdict per 0.48 s,
	// the model's own patch hop.
	hopSamples = 7680
	// PrerollChunks is how much suppressed audio is kept for the retroactive
	// flush at the default release hold. The rule is prerollFor: hold back
	// exactly the countdown, so nothing suppressed while the gate makes up its
	// mind is lost. Measured against a song cut straight into speech with no
	// pause at all — the adversarial case the user's own description says does
	// not occur — 20 chunks eat 0 ms of speech where 15 eat 400 ms and 10 eat
	// 900 ms (y5_flap). Bigger is not better: every extra chunk is music
	// flushed back into the session, so 40 chunks leak 2.0 s more than 20 do
	// and clip exactly as little.
	PrerollChunks = 20
)

// Verdict is one classification frame, as shown in the settings live test.
type Verdict struct {
	Speech  float32
	Music   float32
	Musical bool
	// Top holds the three strongest class indices, for display.
	Top []int
	At  time.Time
}

// --- the pure machine ------------------------------------------------------

type gateMachine struct {
	holdFrames    int
	releaseFrames int
	prerollCap    int
	closed        bool
	musicalRun    int
	clearRun      int
	preroll       [][]byte

	// Counters for the session summary. Chunks, because chunks are the unit
	// everything else in the pipeline counts in.
	closedChunks  int64
	flushedChunks int64
}

// onFrame consumes one verdict. Both directions need a confirmed run: the
// single-frame misread is the commonest classifier event there is, and acting
// on one in either direction is what made the gate flap.
func (m *gateMachine) onFrame(musical bool) {
	if musical {
		m.clearRun = 0
		m.musicalRun++
		if m.musicalRun >= m.holdFrames {
			m.closed = true
		}
		return
	}
	m.musicalRun = 0
	m.clearRun++
	if m.clearRun >= m.releaseFrames {
		m.closed = false
	}
}

// onChunk decides what the send loop may send in place of this chunk: the
// chunk itself, nothing, or the pre-roll followed by the chunk right after a
// reopening.
func (m *gateMachine) onChunk(chunk []byte) [][]byte {
	if m.closed {
		m.closedChunks++
		// Copied: the send loop reuses its read buffer, and the pre-roll
		// outlives the call.
		kept := append([]byte(nil), chunk...)
		m.preroll = append(m.preroll, kept)
		for len(m.preroll) > m.prerollCap {
			m.preroll = m.preroll[1:]
		}
		return nil
	}
	if len(m.preroll) > 0 {
		out := append(m.preroll, chunk)
		m.flushedChunks += int64(len(m.preroll))
		m.preroll = nil
		return out
	}
	return [][]byte{chunk}
}

// retune sets the three frame counts a tuning implies. Shrinking the pre-roll
// does not discard what is already held: onChunk trims to the new cap on the
// next chunk, so a slider moved mid-song cannot drop audio on the spot.
func (m *gateMachine) retune(t GateTuning) {
	m.holdFrames = framesFor(t.HoldSeconds)
	m.releaseFrames = framesFor(t.ReleaseSeconds)
	m.prerollCap = prerollFor(m.releaseFrames)
}

// --- the streaming wrapper -------------------------------------------------

// Gate wires the machine to a model. Create one per translation session when
// the feature is on; Filter goes into translator.Options.
type Gate struct {
	model *Model

	mu      sync.Mutex
	machine gateMachine
	tuning  GateTuning
	verdict Verdict
	window  []float32 // the last PatchSamples samples
	fresh   int       // samples accumulated since the last classification

	kick chan struct{}
	done chan struct{}
}

// DefaultTuning: measured in spike Y3 against synthesized music, the Korean
// TTS fixture and real recorded loops — see spikes/RESULTS.md for the score
// tables these numbers are read from.
func DefaultTuning() GateTuning {
	return GateTuning{Threshold: 0.5, HoldSeconds: 2.0, ReleaseSeconds: 1.5}
}

// NewGate starts the classification worker.
func NewGate(model *Model, tuning GateTuning) *Gate {
	tuning = withDefaults(tuning)
	g := &Gate{
		model:  model,
		tuning: tuning,
		window: make([]float32, 0, PatchSamples),
		kick:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	g.machine.retune(tuning)
	go g.worker()
	return g
}

// withDefaults replaces unset fields. A zero here means "an older settings
// file", never a choice: the sliders stop well short of zero in both
// directions.
func withDefaults(t GateTuning) GateTuning {
	def := DefaultTuning()
	if t.Threshold <= 0 {
		t.Threshold = def.Threshold
	}
	if t.HoldSeconds <= 0 {
		t.HoldSeconds = def.HoldSeconds
	}
	if t.ReleaseSeconds <= 0 {
		t.ReleaseSeconds = def.ReleaseSeconds
	}
	return t
}

// prerollFor sizes the held-back audio from the release hold, in chunks:
// everything suppressed while the gate is counting down to reopen has to be
// recoverable, or the wait would cost the words it was meant to protect. At
// the default 1.5 s release that is 4 frames x 480 ms = 20 chunks, which is
// exactly where the measured clipping reaches zero — so the derivation and
// the measurement agree, and raising the release in Preferences grows the
// pre-roll with it instead of silently starting to eat speech.
func prerollFor(releaseFrames int) int {
	n := (releaseFrames*hopSamples/(chunkMS*SampleRate/1000) + 1)
	if n < 1 {
		n = 1
	}
	return n
}

// framesFor converts seconds to consecutive frames at the 0.48 s cadence,
// rounding up — a hold is a promise of at *least* that much evidence.
func framesFor(seconds float64) int {
	frameSeconds := float64(hopSamples) / SampleRate
	n := int(seconds/frameSeconds + 0.999)
	if n < 1 {
		n = 1
	}
	return n
}

// SetTuning applies slider changes to the running gate. Takes effect on the
// next frame; no session restart.
func (g *Gate) SetTuning(t GateTuning) {
	t = withDefaults(t)
	g.mu.Lock()
	g.tuning = t
	g.machine.retune(t)
	g.mu.Unlock()
}

// Filter is the send-loop hook: every captured chunk passes through, whether
// or not it will be sent. It never blocks on inference — the worker does that.
func (g *Gate) Filter(chunk []byte) [][]byte {
	g.mu.Lock()
	g.feedLocked(chunk)
	out := g.machine.onChunk(chunk)
	g.mu.Unlock()

	select {
	case g.kick <- struct{}{}:
	default:
	}
	return out
}

// feedLocked appends PCM16 samples to the sliding window.
func (g *Gate) feedLocked(chunk []byte) {
	n := len(chunk) / 2
	for i := 0; i < n; i++ {
		v := int16(uint16(chunk[2*i]) | uint16(chunk[2*i+1])<<8)
		g.window = append(g.window, float32(v)/32768)
	}
	if excess := len(g.window) - PatchSamples; excess > 0 {
		copy(g.window, g.window[excess:])
		g.window = g.window[:PatchSamples]
	}
	g.fresh += n
}

// worker classifies at the frame cadence, outside the send path.
func (g *Gate) worker() {
	for {
		select {
		case <-g.done:
			return
		case <-g.kick:
		}
		g.classifyPending()
	}
}

// classifyPending runs one classification if a full window with a fresh hop
// is waiting, and reports whether it did. Split from the worker loop so the
// tests can drive the gate synchronously — an asynchronous worker makes every
// assertion a race.
func (g *Gate) classifyPending() bool {
	g.mu.Lock()
	ready := len(g.window) == PatchSamples && g.fresh >= hopSamples
	var wave []float32
	if ready {
		wave = append(wave, g.window...)
		g.fresh = 0
	}
	g.mu.Unlock()
	if !ready {
		return false
	}

	scores := g.model.Scores(wave) // ~52 ms, deliberately unlocked

	g.mu.Lock()
	speech, music := SpeechScore(scores), MusicScore(scores)
	musical := float64(music) >= g.tuning.Threshold &&
		float64(music) >= float64(speech)+Margin
	g.verdict = Verdict{
		Speech: speech, Music: music, Musical: musical,
		Top: Top(scores, 3), At: time.Now(),
	}
	g.machine.onFrame(musical)
	g.mu.Unlock()
	return true
}

// Tuning returns what the gate is currently running with.
func (g *Gate) Tuning() GateTuning {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.tuning
}

// Closed reports whether the gate is currently suppressing audio — the ♪
// indicator reads this.
func (g *Gate) Closed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.machine.closed
}

// LastVerdict is the newest classification, for the settings live test.
func (g *Gate) LastVerdict() (Verdict, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.verdict, !g.verdict.At.IsZero()
}

// GatedMS is how much audio the gate withheld from the session, and how much
// of that was later flushed by a reopening. gated − flushed is what the
// listener never paid for.
func (g *Gate) GatedMS() (gated, flushed int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.machine.closedChunks * chunkMS, g.machine.flushedChunks * chunkMS
}

// Close stops the worker. The Gate must not be used afterwards.
func (g *Gate) Close() {
	close(g.done)
}
