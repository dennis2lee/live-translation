package audio

import "math"

// freezeGate removes the model's frozen-and-fading buzz from the translated
// speech before it is played.
//
// # What it removes
//
// The far end's speech synthesis sometimes stops tracking and repeats one
// frame while it fades: pitch, timbre and periodicity hold still — pitch to
// within a hertz — while the level falls by a few decibels every 40 ms. A
// user heard it and called it a "zzzing".
//
// Measured on 12.6 minutes of the model's audio, twelve sessions: 15 such
// stretches. 8 open a session, 0.27–0.84 s into its first audio; 6 are the
// replaced session's last word, held; 1 is mid-session with the same
// signature (282 Hz held to 0.05% for 160 ms).
//
// The test is the shape. It can fire on natural speech — a synthetic vowel
// faded at a flat pitch is taken for a freeze — and relies on such fades
// being rare, which is measured: about 25 minutes of macOS voices, monotone,
// singing and Korean ones among them, produced one hit at ten alignments.
//
// # Two cuts
//
// At a session's opening there is no word in front of the freeze, and it is
// removed whole: the cut reaches a hop before the first frozen window, after
// following the freeze back over any frames that are frozen but not yet
// fading, so it starts before the buzz does. On the recording, every opening
// freeze went from 58–136 ms above -30 dBFS to none, and measured from an
// independently found onset none of its energy was left.
//
// Anywhere else the frozen frame is the last sound of a word, held too long,
// and removing it whole cut words short — "question" to "quest-", "plan" to
// "play". There the first freezeKeepMS stay and the held tail goes: 49–136
// ms of tone above -30 dBFS became 37–56 ms, and no word was lost on two
// transcriptions of every event ("question" came back as "questions").
//
// Which cut applies is decided by where the session began, which the
// translator reports (MarkSessionStart), not by listening for a silence.
// Silence looked like a handover and was not: paragraph pauses run 1.0–2.3
// s, three to five a minute, against 1.9–2.8 s at a handover.
//
// # How
//
// 40 ms windows are measured every 10 ms. A freeze starts at a window when it
// and the three windows 40, 80 and 120 ms after it share pitch (within 1%),
// timbre (10%) and periodicity (0.05), each falling at least 2 dB below the
// one before; it runs on while the next window 40 ms later still does. On a
// 40 ms grid the cut began 0–70 ms into the freeze, at its loudest, and left
// a median 64% of its energy playing.
//
// Deciding needs 160 ms past the start, and the opening cut reaches 60 ms
// before it, so audio leaves here 210 ms after it arrives. End to end, over
// six minutes of recorded arrivals, the delay to the listener went from
// 0.64 s to 0.81 s. The gate holds that much, so a stream that pauses leaves
// it waiting: Reset drops it when the stream is about to change.
//
// Pitch is the autocorrelation peak; timbre is the RMS of the first
// difference over the RMS of the signal, which follows the spectral centroid
// without a transform.
type freezeGate struct {
	rate, hop, win, step int
	lagLo, lagHi         int
	fadeOut, fadeIn      int
	back                 int // freezeBackMS in hops

	// sessionStarts are the input indices where recent sessions' first audio
	// began, as the translator reported them.
	sessionStarts []int64

	buf   []float32 // unreleased samples
	base  int64     // input index of buf[0]
	total int64     // samples received

	feats    []frameFeat // one per hop
	featBase int64       // hop index of feats[0]
	next     int64       // next hop to test as the start of a freeze

	cuts []cut // silenced stretches, in input samples
	open bool  // the last cut is still being extended

	runRef  frameFeat
	runLast int64 // hop index of the last window in the open cut

	out       []float32
	diff      []float64
	muted     int64
	runStarts []int64
	// cutLog keeps every finished cut, for the log and the tests. Freezes
	// are rare, so it does not grow in practice.
	cutLog []cut
}

// cut is a silenced stretch [from, to) with a fade of fade samples ending at
// from.
type cut struct {
	from, to int64
	fade     int
}

type frameFeat struct {
	level  float64 // peak, dBFS
	f0     float64 // Hz
	per    float64 // normalised autocorrelation at the pitch lag
	colour float64 // RMS of the first difference / RMS of the window
}

const (
	freezeWindowMS   = 40
	freezeHopMS      = 10
	freezeMinWindows = 4
	freezePitchTol   = 0.01
	freezeColourTol  = 0.10
	freezePerTol     = 0.05
	freezeMinPer     = 0.5
	freezeFallDB     = 2.0
	freezeFloorDB    = -60.0
	freezeFadeOutMS  = 10
	freezeFadeInMS   = 10
	freezeMinPitchHz = 70
	freezeMaxPitchHz = 500
	// freezeBackMS is how far a confirmed freeze is followed back over windows
	// that are already frozen but not yet fading. Two of fifteen recorded
	// freezes held their level for 18–28 ms before the fade began, and a cut
	// that waited for the fade left 16–21% of them playing.
	freezeBackMS = 40

	// A freeze that begins within freezeStartMS of a session's first audio is
	// that session's opening buzz and is removed whole. Anywhere else it is
	// the last sound of a word held too long, and only what follows the first
	// freezeKeepMS goes, faded over freezeTailFadeMS.
	//
	// Measured: removing session-end freezes whole cut the end off two words
	// in six — "question" became "quest-", "plan" became "play" — because the
	// held frame is the word's final phoneme. Keeping 30, 50 or 70 ms lost no
	// word in any of them, on two transcriptions each; 50 is the margin. The
	// eight session-opening freezes lost no word removed whole: they start
	// 0.27–0.84 s into the new session's audio, with no word in front of
	// them.
	//
	// The session boundary comes from the translator, not from the audio. A
	// silence of 500 ms or more looked like a handover and was not: natural
	// paragraph pauses run 1.0–2.3 s, three to five a minute, against
	// 1.9–2.8 s at a handover, and four of six session-end freezes followed
	// one and were removed whole — "plan" among them.
	freezeStartMS    = 1200
	freezeKeepMS     = 50
	freezeTailFadeMS = 20
)

func newFreezeGate(rate int) *freezeGate {
	hop := rate * freezeHopMS / 1000
	win := rate * freezeWindowMS / 1000
	return &freezeGate{
		rate:    rate,
		hop:     hop,
		win:     win,
		step:    win / hop,
		lagLo:   rate / freezeMaxPitchHz,
		lagHi:   rate / freezeMinPitchHz,
		fadeOut: rate * freezeFadeOutMS / 1000,
		fadeIn:  rate * freezeFadeInMS / 1000,
		back:    freezeBackMS / freezeHopMS,
		diff:    make([]float64, win),
	}
}

// Muted is how many samples have been silenced.
func (g *freezeGate) Muted() int64 { return g.muted }

// Reset drops whatever the gate is holding. Call it when the stream is about
// to change — a new session after a restart, a switch from a local voice
// back to the model — or the held tail of the old stream plays seconds later
// in front of the new one.
func (g *freezeGate) Reset() {
	starts, muted, log := g.runStarts, g.muted, g.cutLog
	*g = *newFreezeGate(g.rate)
	g.runStarts, g.muted, g.cutLog = starts, muted, log
}

// Process takes mono samples in and returns what may be played, 210 ms
// behind. The returned slice is reused by the next call.
func (g *freezeGate) Process(in []float32) []float32 {
	g.out = g.out[:0]
	g.buf = append(g.buf, in...)
	g.total += int64(len(in))

	// Measure every window that is now complete.
	for {
		h := g.featBase + int64(len(g.feats))
		start := h * int64(g.hop)
		if start+int64(g.win) > g.total {
			break
		}
		g.feats = append(g.feats, g.features(g.buf[start-g.base:start-g.base+int64(g.win)]))
	}

	g.extend()
	// Test each hop that now has the three windows after it.
	span := int64((freezeMinWindows - 1) * g.step)
	for g.next+span < g.featBase+int64(len(g.feats)) {
		k := g.next
		g.next++
		if g.open || (len(g.cuts) > 0 && k*int64(g.hop) < g.cuts[len(g.cuts)-1].to) {
			continue
		}
		if g.startsAt(k) {
			// Follow it back over windows that are the same frame but not yet
			// fading, then one hop further: the first fully frozen window
			// starts within a hop after the freeze does, and that hop is its
			// loudest part.
			b := g.backTo(k)
			onset := b * int64(g.hop)
			c := cut{from: onset - int64(g.hop), to: (k+span)*int64(g.hop) + int64(g.win), fade: g.fadeOut}
			if !g.opensSession(onset) {
				tail := g.rate * freezeTailFadeMS / 1000
				c.from, c.fade = onset+int64(g.rate*freezeKeepMS/1000+tail), tail
			}
			g.cuts = append(g.cuts, c)
			g.open, g.runRef, g.runLast = true, g.feat(k), k+span
			g.runStarts = append(g.runStarts, onset)
			g.extend()
		}
	}

	// Release everything that no future decision can touch: up to the fade
	// before the next possible freeze, and never into an open cut's
	// undecided end.
	limit := g.next*int64(g.hop) - int64(g.hop) - int64(g.back*g.hop) - int64(g.fadeOut)
	if g.open {
		limit = min(limit, g.cuts[len(g.cuts)-1].to)
	}
	g.release(limit)
	g.trim()
	return g.out
}

// extend grows the open cut while the window 40 ms on is still the same
// fading frame.
func (g *freezeGate) extend() {
	for g.open {
		n := g.runLast + int64(g.step)
		if n >= g.featBase+int64(len(g.feats)) {
			return
		}
		if !g.continues(g.runRef, g.feat(g.runLast), g.feat(n)) {
			g.open = false
			g.cutLog = append(g.cutLog, g.cuts[len(g.cuts)-1])
			return
		}
		g.runLast = n
		g.cuts[len(g.cuts)-1].to = n*int64(g.hop) + int64(g.win)
	}
}

// backTo walks back from a confirmed freeze start over windows that hold the
// same pitch, timbre and periodicity, whatever their level, and returns the
// earliest.
func (g *freezeGate) backTo(k int64) int64 {
	ref := g.feat(k)
	b := k
	for b-1 >= g.featBase && k-(b-1) <= int64(g.back) && g.same(ref, g.feat(b-1)) {
		b--
	}
	return b
}

// same reports whether f is the frame ref, ignoring level.
func (g *freezeGate) same(ref, f frameFeat) bool {
	return ref.f0 > 0 && ref.colour > 0 && ref.per > freezeMinPer &&
		math.Abs(f.f0-ref.f0)/ref.f0 < freezePitchTol &&
		math.Abs(f.colour-ref.colour)/ref.colour < freezeColourTol &&
		math.Abs(f.per-ref.per) < freezePerTol &&
		f.level > freezeFloorDB
}

// MarkSessionStart records that the next sample received is a new session's
// first audio.
func (g *freezeGate) MarkSessionStart() {
	g.sessionStarts = append(g.sessionStarts, g.total)
	if len(g.sessionStarts) > 8 {
		g.sessionStarts = g.sessionStarts[1:]
	}
}

// opensSession reports whether onset lies within freezeStartMS of a
// session's first audio.
func (g *freezeGate) opensSession(onset int64) bool {
	for _, st := range g.sessionStarts {
		if onset >= st-int64(g.rate*freezeBackMS/1000) && onset-st <= int64(g.rate*freezeStartMS/1000) {
			return true
		}
	}
	return false
}

func (g *freezeGate) startsAt(k int64) bool {
	ref := g.feat(k)
	for w := 1; w < freezeMinWindows; w++ {
		prev, next := g.feat(k+int64((w-1)*g.step)), g.feat(k+int64(w*g.step))
		if !g.continues(ref, prev, next) {
			return false
		}
	}
	return true
}

// continues reports whether next is still the frozen frame ref, fading from
// prev.
func (g *freezeGate) continues(ref, prev, next frameFeat) bool {
	if ref.f0 <= 0 || ref.colour <= 0 || ref.per <= freezeMinPer {
		return false
	}
	return math.Abs(next.f0-ref.f0)/ref.f0 < freezePitchTol &&
		math.Abs(next.colour-ref.colour)/ref.colour < freezeColourTol &&
		math.Abs(next.per-ref.per) < freezePerTol &&
		next.level <= prev.level-freezeFallDB &&
		next.level > freezeFloorDB
}

func (g *freezeGate) feat(h int64) frameFeat { return g.feats[h-g.featBase] }

// release hands samples before limit to out, applying every cut.
func (g *freezeGate) release(limit int64) {
	limit = min(limit, g.total)
	for n := g.base; n < limit; n++ {
		v := g.buf[n-g.base] * float32(g.gain(n))
		if v == 0 && g.buf[n-g.base] != 0 {
			g.muted++
		}
		g.out = append(g.out, v)
	}
	if limit > g.base {
		g.buf = append(g.buf[:0], g.buf[limit-g.base:]...)
		g.base = limit
	}
}

// gain is the envelope at input sample n: raised-cosine fades that end at a
// cut's first sample and start at its last, silence between. Overlapping
// envelopes take the lower.
func (g *freezeGate) gain(n int64) float64 {
	gain := 1.0
	for i, c := range g.cuts {
		switch {
		case n >= c.from && n < c.to:
			return 0
		case n >= c.from-int64(c.fade) && n < c.from:
			x := float64(c.from-n) / float64(c.fade) // 1 → 0 approaching the cut
			gain = min(gain, 0.5-0.5*math.Cos(math.Pi*x))
		case n >= c.to && n < c.to+int64(g.fadeIn) && !(g.open && i == len(g.cuts)-1):
			x := float64(n-c.to+1) / float64(g.fadeIn)
			gain = min(gain, 0.5-0.5*math.Cos(math.Pi*x))
		}
	}
	return gain
}

// trim forgets measurements and cuts that nothing can refer to any more.
func (g *freezeGate) trim() {
	keep := g.next - int64(g.back)
	if g.open {
		keep = min(keep, g.runLast)
	}
	if drop := keep - g.featBase; drop > 0 && drop <= int64(len(g.feats)) {
		g.feats = append(g.feats[:0], g.feats[drop:]...)
		g.featBase = keep
	}
	for len(g.cuts) > 0 && !(g.open && len(g.cuts) == 1) && g.cuts[0].to+int64(g.fadeIn) <= g.base {
		g.cuts = g.cuts[1:]
	}
}

func (g *freezeGate) features(window []float32) frameFeat {
	var peak, mean float64
	for _, v := range window {
		peak = max(peak, math.Abs(float64(v)))
		mean += float64(v)
	}
	level := 20 * math.Log10(max(peak, 1e-9))
	mean /= float64(len(window))
	var e0 float64
	for i, v := range window {
		g.diff[i] = float64(v) - mean
		e0 += g.diff[i] * g.diff[i]
	}
	if e0 <= 0 {
		return frameFeat{level: level}
	}
	x := g.diff
	bestLag, best := 0, math.Inf(-1)
	for k := g.lagLo; k < g.lagHi && k < len(x); k++ {
		var s float64
		for i := 0; i+k < len(x); i++ {
			s += x[i] * x[i+k]
		}
		if s > best {
			best, bestLag = s, k
		}
	}
	var d2 float64
	for i := 1; i < len(x); i++ {
		d := x[i] - x[i-1]
		d2 += d * d
	}
	return frameFeat{
		level:  level,
		f0:     float64(g.rate) / float64(bestLag),
		per:    best / e0,
		colour: math.Sqrt(d2 / e0),
	}
}
