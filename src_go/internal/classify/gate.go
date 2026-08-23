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
//   - It opens instantly, and retroactively: the first non-musical frame
//     reopens it, and the pre-roll — the last PrerollChunks of suppressed
//     audio — is flushed to the session first, because the words that
//     triggered the reopening are up to a patch length in the past. The
//     server has always been fine with a burst; the reconnect path flushes an
//     accumulated ring the same way.

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
	// flush — 1.5 s covers the decision latency (up to one patch, ~1 s) with
	// margin.
	PrerollChunks = 15
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
	holdFrames int
	closed     bool
	musicalRun int
	preroll    [][]byte

	// Counters for the session summary. Chunks, because chunks are the unit
	// everything else in the pipeline counts in.
	closedChunks  int64
	flushedChunks int64
}

// onFrame consumes one verdict.
func (m *gateMachine) onFrame(musical bool) {
	if !musical {
		m.musicalRun = 0
		m.closed = false
		return
	}
	m.musicalRun++
	if m.musicalRun >= m.holdFrames {
		m.closed = true
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
		if len(m.preroll) > PrerollChunks {
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
	return GateTuning{Threshold: 0.5, HoldSeconds: 2.0}
}

// NewGate starts the classification worker.
func NewGate(model *Model, tuning GateTuning) *Gate {
	def := DefaultTuning()
	if tuning.Threshold <= 0 {
		tuning.Threshold = def.Threshold
	}
	if tuning.HoldSeconds <= 0 {
		tuning.HoldSeconds = def.HoldSeconds
	}
	g := &Gate{
		model:  model,
		tuning: tuning,
		window: make([]float32, 0, PatchSamples),
		kick:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	g.machine.holdFrames = holdFramesFor(tuning.HoldSeconds)
	go g.worker()
	return g
}

// holdFramesFor converts seconds to consecutive frames at the 0.48 s cadence,
// rounding up — the hold is a promise of at *least* that much music.
func holdFramesFor(seconds float64) int {
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
	def := DefaultTuning()
	if t.Threshold <= 0 {
		t.Threshold = def.Threshold
	}
	if t.HoldSeconds <= 0 {
		t.HoldSeconds = def.HoldSeconds
	}
	g.mu.Lock()
	g.tuning = t
	g.machine.holdFrames = holdFramesFor(t.HoldSeconds)
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
