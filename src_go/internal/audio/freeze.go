package audio

import "math"

// freezeGate removes the buzz the far end's speech synthesis makes as a
// session opens.
//
// # What it removes
//
// The synthesis sometimes repeats one frame while it fades: pitch, timbre and
// periodicity held still — pitch to within a hertz — while the level falls a
// few decibels every 40 ms. A user heard it as a "zzzing" and pointed to one.
//
// Measured on 12.6 minutes of the model's audio, twelve sessions: 15 such
// stretches. 8 come 0.27–0.84 s into a session's first audio, mostly where
// the opening syllable stops tracking. 6 are the replaced session's last word,
// and 1 is mid-session.
//
// # Only at a session's opening
//
// At an opening the freeze is removed whole. Everywhere else it is left
// alone, because there it is the end of a real word and every cut tried lost
// the word: removed whole, "question" became "quest-" and "plan" became
// "play"; trimmed to its first 50 ms, still "quest-". The transcriber caught
// those reliably.
//
// What removing an opening costs is not established by measurement. The
// transcriber cannot say: it read gated openings the same as raw ones, but it
// also missed a control cut of the syllable just before each freeze in 7 of
// 8. Two spectral measures of whether the frozen sound had been heard before
// the freeze disagree — an uncalibrated band-envelope measure said not in 6
// of 8, a calibrated MFCC measure found it present in all 8 (10–175 ms) —
// and the first returns "not heard" at half of random points in ordinary
// speech, so it does not discriminate. What the traces do show is a one-frame
// hold at fixed pitch, fading 3–5 dB per 20 ms, with speech back within
// 10–30 ms of the cut. The case for cutting is the user's ear: the opening
// they reported (tl s03, 0.80 s) is heard as the buzz, not as a word, and
// removing the first 500 ms of fresh sessions sounded clean to them.
//
// The freezes left alone are not small. Over two recordings, 5 of 10
// handovers end the replaced session on one, as loud as the openings (peaks
// -4 to -15 dBFS) and up to twice as long (330–450 ms detected, 48–135 ms
// above -30 dBFS). That is an open problem: this gate does not touch it.
//
// So the gate works in episodes. The translator reports each session's first
// audio (MarkSessionStart); for the next freezeStartMS the gate holds audio
// back while it decides, then lets everything go and passes audio straight
// through until the next session. The hold falls in the silence every
// handover already carries, and the play queue's silence trimming takes the
// extra back out: over six minutes of recorded arrivals the delay to the
// listener went from 0.636 s to 0.660 s. Outside the eight cuts not one
// sample of the 18 million was changed.
//
// The mark is ordered with the new session's audio because both come from
// that session's receive goroutine. The replaced session's goroutine is not
// ordered with them: one of its blobs can land between the mark and the new
// session's first audio, counting as part of the opening and shortening the
// window by up to a blob, 250 ms — 950 ms against a latest measured opening
// freeze at 0.84 s. By then the replaced session is sending silence, and the
// recorded timeline never interleaves.
//
// # How
//
// 40 ms windows are measured every 10 ms. A freeze starts at a window when it
// and the three windows 40, 80 and 120 ms after it share pitch (within 1%),
// timbre (10%) and periodicity (0.05), each falling at least 2 dB below the
// one before; it runs on, hop by hop, while each window is still that frame,
// fading from the one 40 ms before it. A
// confirmed freeze is followed back over windows that are already frozen but
// not yet fading, and the cut starts one hop before that, so it starts
// before the buzz does: measured from an onset found independently, by
// period-to-period correlation, nothing of an opening freeze is left. On a
// 40 ms grid the cut began 0–70 ms late and left a median 64%.
//
// Pitch is the autocorrelation peak; timbre is the RMS of the first
// difference over the RMS of the signal, which follows the spectral centroid
// without a transform.
type freezeGate struct {
	rate, hop, win, step int
	lagLo, lagHi         int
	fadeOut, fadeIn      int
	back                 int // freezeBackMS in hops

	// active is true for an episode: from a session's first audio until no
	// freeze can start any more. origin is that first audio's input index.
	active bool
	origin int64

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
	// carry is audio released outside Process — by MarkSessionStart ending an
	// episode early — waiting for the next Process to hand it out first.
	carry []float32
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

	// freezeStartMS is how long after a session's first audio a freeze can
	// start and still be removed: the eight measured openings began
	// 0.27–0.84 s in.
	freezeStartMS = 1200
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
	starts, muted, log, total := g.runStarts, g.muted, g.cutLog, g.total
	*g = *newFreezeGate(g.rate) // also drops carry: Reset is for discarding
	g.runStarts, g.muted, g.cutLog = starts, muted, log
	g.total, g.base = total, total
}

// Process takes mono samples in and returns what may be played: straight
// through between openings, and during one about 210–220 ms behind,
// depending on where the 10 ms grid falls, and longer while a freeze that
// started late in the opening is still running. The returned slice is reused
// by the next call.
func (g *freezeGate) Process(in []float32) []float32 {
	g.out = append(g.out[:0], g.carry...)
	g.carry = g.carry[:0]
	if !g.active {
		// Between openings there is nothing to decide and nothing to hold.
		g.total += int64(len(in))
		g.base = g.total
		g.out = append(g.out, in...)
		return g.out
	}
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
		if (k*int64(g.hop))-g.origin > int64(g.rate*freezeStartMS/1000) {
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
	if g.done() {
		g.finish()
	}
	return g.out
}

// extend grows the open cut one hop at a time while each new window is
// still the frozen frame, fading from the window 40 ms before it. Stepping
// 40 ms at a time stopped up to 40 ms short of the freeze's end, and a freeze
// cut off by the next sound ends loud.
func (g *freezeGate) extend() {
	for g.open {
		n := g.runLast + 1
		if n >= g.featBase+int64(len(g.feats)) {
			return
		}
		if !g.continues(g.runRef, g.feat(n-int64(g.step)), g.feat(n)) {
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

// MarkSessionStart opens an episode: the next sample is a session's first
// audio. An episode still running is finished first, and what it was holding
// is handed out by the next Process, before the new session's audio.
func (g *freezeGate) MarkSessionStart() {
	if g.active {
		// A session that delivered less than an opening's worth. What it
		// left held is still speech, and goes out ahead of the new session.
		g.out = g.out[:0]
		g.finish()
		g.carry = append(g.carry, g.out...)
		g.out = g.out[:0]
	}
	g.active = true
	g.origin = g.total
	g.base = g.total
	h := (g.total + int64(g.hop) - 1) / int64(g.hop)
	g.featBase, g.next = h, h
	g.feats = g.feats[:0]
	g.cuts = g.cuts[:0]
	g.open = false
}

// done reports whether the episode can end: no freeze can start any more
// and none is still running.
func (g *freezeGate) done() bool {
	return !g.open && g.next*int64(g.hop)-g.origin > int64(g.rate*freezeStartMS/1000)
}

// finish releases everything held, with every cut applied, and ends the
// episode. A freeze still running ends where it has been measured to.
func (g *freezeGate) finish() {
	if g.open {
		g.open = false
		g.cutLog = append(g.cutLog, g.cuts[len(g.cuts)-1])
	}
	g.release(g.total)
	g.active = false
	g.cuts = g.cuts[:0]
	g.feats = g.feats[:0]
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
		keep = min(keep, g.runLast-int64(g.step))
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
