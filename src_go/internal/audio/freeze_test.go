package audio

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/dennis2lee/live-translation/src_go/internal/resample"
)

const fr = TranslatedRate

// --- signals ---------------------------------------------------------------

// voiced makes a harmonic voice-like signal whose pitch and level (dBFS)
// follow the given functions of time. mix sets the second and third harmonic.
func voicedMix(seconds float64, pitchAt, levelAt func(t float64) float64, mix func(t float64) (float64, float64)) []float32 {
	n := int(seconds * fr)
	out := make([]float32, n)
	phase := 0.0
	for i := range out {
		t := float64(i) / fr
		phase += 2 * math.Pi * pitchAt(t) / fr
		h2, h3 := mix(t)
		v := math.Sin(phase) + h2*math.Sin(2*phase) + h3*math.Sin(3*phase)
		out[i] = float32(v / (1 + h2 + h3) * math.Pow(10, levelAt(t)/20))
	}
	return out
}

func voiced(seconds float64, pitchAt, levelAt func(t float64) float64) []float32 {
	return voicedMix(seconds, pitchAt, levelAt, func(float64) (float64, float64) { return 0.5, 0.25 })
}

func silence(seconds float64) []float32 { return make([]float32, int(seconds*fr)) }

func concat(parts ...[]float32) []float32 {
	var out []float32
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// natural is speech-like: the pitch wanders and the level moves.
func natural(seconds float64) []float32 {
	return voiced(seconds,
		func(t float64) float64 { return 180 + 12*math.Sin(2*math.Pi*5*t) + 30*t },
		func(t float64) float64 { return -8 + 4*math.Sin(2*math.Pi*3*t) })
}

// frozen is the measured artifact: one frame held while it fades, 7 dB per
// 40 ms at a fixed 174 Hz — the shape recorded at session 2's start.
func frozenFor(seconds, fallPer40 float64) []float32 {
	return voiced(seconds, func(float64) float64 { return 174 },
		func(t float64) float64 { return -6 - fallPer40*t/0.04 })
}

func frozen() []float32 { return frozenFor(0.28, 7) }

// gate runs in through a fresh gate in 250 ms blobs — the size the model
// sends — as one session that opens at its first sample, with enough silence
// after it that nothing of interest is still held.
func gate(in []float32) ([]float32, *freezeGate) {
	g := newFreezeGate(fr)
	g.MarkSessionStart()
	in = concat(in, silence(0.5))
	var out []float32
	blob := fr / 4
	for i := 0; i < len(in); i += blob {
		out = append(out, g.Process(in[i:min(i+blob, len(in))])...)
	}
	return out, g
}

func energy(x []float32) float64 {
	var s float64
	for _, v := range x {
		s += float64(v) * float64(v)
	}
	return s
}

// --- the freeze is removed, from where it really starts ----------------------

// The red team's finding: on a 40 ms grid the cut started 0–70 ms into the
// freeze, at its loudest, and left a median 64% of it playing. A test that
// lined the freeze up with a frame boundary could not see that. This one puts
// the freeze at every millisecond of a 40 ms span and measures what is left
// from its true first sample.
func TestTheWholeFreezeIsRemovedWhereverItStarts(t *testing.T) {
	for off := 0; off < 40; off++ {
		lead := concat(silence(float64(off)/1000), natural(0.6))
		f := frozen()
		out, g := gate(concat(lead, f))
		if len(g.runStarts) != 1 {
			t.Errorf("offset %d ms: %d freezes found, want 1", off, len(g.runStarts))
			continue
		}
		at := len(lead)
		left := energy(out[at:at+len(f)]) / energy(f)
		if left > 0.01 {
			t.Errorf("offset %d ms: %.1f%% of the freeze's energy still plays", off, 100*left)
		}
	}
}

// Speech has to come through bit-identical: the gate only ever delays it.
func TestNaturalSpeechPassesUntouched(t *testing.T) {
	in := concat(natural(1.2), silence(0.2), natural(0.8))
	out, g := gate(in)
	if len(g.runStarts) != 0 {
		t.Fatalf("natural speech triggered %d freezes", len(g.runStarts))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("natural speech altered at sample %d", i)
		}
	}
}

// Before the cut, everything more than the lead-in and fade ahead of the
// freeze is untouched.
func TestTheSpeechBeforeAFreezeIsKept(t *testing.T) {
	speech := natural(0.6)
	out, _ := gate(concat(speech, frozen()))
	keep := len(speech) - fr*(freezeHopMS+freezeFadeOutMS)/1000 - fr*freezeHopMS/1000
	for i := 0; i < keep; i++ {
		if out[i] != speech[i] {
			t.Fatalf("speech %d ms before the freeze was altered",
				(len(speech)-i)*1000/fr)
		}
	}
}

// --- near misses: each condition is load-bearing -------------------------------

// Each of these has the frozen fade's shape except for one property, and so
// must pass through. Together they pin every threshold: loosen one and its
// case starts being cut.
func TestNearMissesAreNotFreezes(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 9))
	noisy := func(x []float32, amount func(t float64) float64) []float32 {
		for i := range x {
			t := float64(i) / fr
			x[i] += float32(amount(t) * (rng.Float64()*2 - 1) * math.Abs(float64(x[i])+1e-3))
		}
		return x
	}
	cases := map[string][]float32{
		// Pitch held, fading, but the timbre changes 20% across the fade.
		"timbre moves": voicedMix(0.28, func(float64) float64 { return 174 },
			func(t float64) float64 { return -6 - 7*t/0.04 },
			func(t float64) (float64, float64) { return 0.2 + 3*t, 0.1 + 2*t }),
		// Pitch moves 2% over the fade — just outside the 1% tolerance.
		"pitch drifts": voiced(0.28, func(t float64) float64 { return 174 * (1 + 0.02*t/0.12) },
			func(t float64) float64 { return -6 - 7*t/0.04 }),
		// Fading at 1 dB per 40 ms: slower than any freeze recorded.
		"fades slowly": frozenFor(0.4, 1),
		// Only 120 ms frozen: three windows, one short of the four required.
		"too short": frozenFor(0.12, 7),
		// The whole thing below -60 dBFS: nobody hears it, nothing to cut.
		"below the floor": voiced(0.28, func(float64) float64 { return 174 },
			func(t float64) float64 { return -62 - 3*t/0.04 }),
		// Periodicity collapses as noise rises through the fade.
		"turns to noise": noisy(frozen(), func(t float64) float64 { return 6 * t / 0.28 }),
	}
	for name, in := range cases {
		_, g := gate(concat(natural(0.5), in, natural(0.3)))
		if len(g.runStarts) != 0 {
			t.Errorf("%s: taken for a freeze", name)
		}
	}
}

// A sound can hold still without fading, and a sound can fade while its pitch
// moves — a sentence's last syllable usually falls in both. Neither is the
// artifact, which needs both at once.
func TestOnlyFrozenAndFadingTogetherCounts(t *testing.T) {
	steady := voiced(0.4, func(float64) float64 { return 174 }, func(float64) float64 { return -6 })
	gliding := voiced(0.4, func(t float64) float64 { return 220 - 150*t },
		func(t float64) float64 { return -6 - 6*t/0.04 })
	for name, in := range map[string][]float32{"steady tone": steady, "falling glide": gliding} {
		_, g := gate(concat(natural(0.4), in))
		if len(g.runStarts) != 0 {
			t.Errorf("%s was taken for a freeze", name)
		}
	}
}

// --- the cut itself is inaudible --------------------------------------------

// hf is the energy of the second difference around sample at — what a click
// is made of — so it can be compared with a hard cut of the same signal.
func hf(x []float32, at int) float64 {
	var s float64
	for i := max(at-fr/1000*3, 2); i < min(at+fr/1000*3, len(x)); i++ {
		d := float64(x[i]) - 2*float64(x[i-1]) + float64(x[i-2])
		s += d * d
	}
	return s
}

// The fades must leave less click than a hard cut at the same places, at any
// alignment. Against a fixed step limit a hard cut passed, because the test
// signal happened to start at zero phase on a frame boundary.
func TestTheCutDoesNotClick(t *testing.T) {
	for off := 0; off < 40; off += 3 {
		lead := concat(silence(float64(off)/1000), natural(0.6))
		in := concat(lead, frozen(), natural(0.5)) // speech resumes straight after
		out, g := gate(in)
		if len(g.cutLog) != 1 {
			t.Fatalf("offset %d: %d cuts, want 1", off, len(g.cutLog))
		}
		c := g.cutLog[0]
		hard := append([]float32(nil), in...)
		for i := c.from; i < c.to; i++ {
			hard[i] = 0
		}
		for name, at := range map[string]int{
			"fade out": int(c.from) - fr*freezeFadeOutMS/2000,
			"fade in":  int(c.to) + fr*freezeFadeInMS/2000,
		} {
			cut := map[string]int{"fade out": int(c.from), "fade in": int(c.to)}[name]
			if got, worst := hf(out, at), hf(hard, cut); got > 0.25*worst {
				t.Errorf("offset %d ms, %s: %.2g of click energy, a hard cut has %.2g",
					off, name, got, worst)
			}
		}
	}
}

// --- streaming ----------------------------------------------------------------

// Fed in uneven pieces, the gate must neither drop, duplicate nor reorder.
func TestNothingIsLostOrReordered(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	in := concat(natural(0.7), silence(0.1), natural(0.9))
	g := newFreezeGate(fr)
	var out []float32
	for i := 0; i < len(in); {
		n := 1 + rng.IntN(fr/5)
		out = append(out, g.Process(in[i:min(i+n, len(in))])...)
		i += n
	}
	// The most the gate can hold: the windows a decision needs, the hop the
	// cut starts early, the lead-in it may follow back, the fade before it,
	// and one hop for a window not yet complete.
	maxMS := freezeMinWindows*freezeWindowMS + freezeHopMS + freezeBackMS + freezeFadeOutMS + freezeHopMS
	held := len(in) - len(out)
	if held < 0 || held > fr*maxMS/1000 {
		t.Fatalf("holding %d samples (%d ms), want at most %d ms", held, held*1000/fr, maxMS)
	}
	for i := range out {
		if out[i] != in[i] {
			t.Fatalf("sample %d differs", i)
		}
	}
}

// Reset must drop what is held, so the old stream's tail cannot play in front
// of the next one.
func TestResetDropsTheHeldTail(t *testing.T) {
	g := newFreezeGate(fr)
	g.Process(natural(1.0))
	g.Reset()
	next := natural(0.5)
	out := g.Process(concat(next, silence(0.3)))
	if len(out) < len(next) {
		t.Fatalf("setup: only %d of %d samples released", len(out), len(next))
	}
	for i := range next {
		if out[i] != next[i] {
			t.Fatalf("after Reset, sample %d is not the new stream's", i)
		}
	}
}

// --- through the player -------------------------------------------------------

func runningPlayer(t *testing.T) (*Player, *playState) {
	t.Helper()
	p := NewPlayer("", TranslatedRate)
	p.devRate, p.devChan, p.running = TranslatedRate, 1, true
	rs, err := resample.NewStream(TranslatedRate, TranslatedRate*2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rs.Close)
	p.resampler = rs
	p.freeze = newFreezeGate(TranslatedRate)
	st := &playState{ring: newPlayRing(TranslatedRate * 2 * 8), blockBytes: 2}
	p.active.Store(st)
	return p, st
}

func toPCM(x []float32) []byte {
	b := make([]byte, 2*len(x))
	for i, v := range x {
		s := clampToInt16(v)
		b[2*i], b[2*i+1] = byte(uint16(s)), byte(uint16(s)>>8)
	}
	return b
}

func ringRMS(st *playState) float64 {
	buf := make([]byte, st.ring.Available())
	st.ring.Read(buf)
	var s float64
	for i := 0; i+1 < len(buf); i += 2 {
		v := float64(int16(uint16(buf[i])|uint16(buf[i+1])<<8)) / 32768
		s += v * v
	}
	return math.Sqrt(s / float64(max(len(buf)/2, 1)))
}

// The chime and a local voice go through Write; only the model's audio goes
// through WriteSpeech. A chime note is a decaying sine — exactly what the
// gate removes — so the separation is not optional.
func TestOnlyTheModelsAudioIsGated(t *testing.T) {
	p, _ := runningPlayer(t)
	pcm := toPCM(frozen())
	p.Write(pcm)
	if p.freeze.total != 0 {
		t.Fatal("Write went through the freeze gate")
	}
	p.WriteSpeech(pcm)
	if p.freeze.total == 0 {
		t.Fatal("WriteSpeech bypassed the freeze gate")
	}
}

// The volume — and m, which mutes by setting it to zero — must reach the
// audio on both paths. The gate decodes at unity gain, so the gain has to be
// applied after it; drop that line and speech plays at full volume, muted or
// not.
func TestVolumeReachesBothPaths(t *testing.T) {
	speech := toPCM(natural(1.0))
	for _, path := range []string{"Write", "WriteSpeech"} {
		levels := map[float64]float64{}
		for _, vol := range []float64{1, 0.5, 0} {
			p, st := runningPlayer(t)
			p.SetVolume(vol)
			if path == "Write" {
				p.Write(speech)
			} else {
				p.WriteSpeech(speech)
			}
			levels[vol] = ringRMS(st)
		}
		if levels[1] == 0 {
			t.Fatalf("%s: nothing reached the ring", path)
		}
		if levels[0] != 0 {
			t.Errorf("%s at volume 0 (muted) still plays at RMS %.4f", path, levels[0])
		}
		if r := levels[0.5] / levels[1]; math.Abs(r-0.5) > 0.02 {
			t.Errorf("%s at volume 0.5 plays at %.2f of full", path, r)
		}
	}
}

// ResetSpeech drops the gate's held audio.
func TestResetSpeechReachesTheGate(t *testing.T) {
	p, _ := runningPlayer(t)
	p.WriteSpeech(toPCM(natural(0.5)))
	if len(p.freeze.buf) == 0 {
		t.Fatal("setup: the gate is holding nothing")
	}
	p.ResetSpeech()
	if len(p.freeze.buf) != 0 {
		t.Error("ResetSpeech left the held audio in place")
	}
}

// --- session opening or word ending -----------------------------------------

// Mid-speech, a freeze is the last sound of a word held too long: removing it
// whole turned "question" into "quest-" and "plan" into "play". The first
// freezeKeepMS stay exactly as they were and only the held tail goes.
func TestAFreezeInSpeechKeepsTheEndOfTheWord(t *testing.T) {
	lead := natural(2.0) // more than freezeStartMS after any silence
	f := frozen()
	out, g := gate(concat(lead, f))
	if len(g.cutLog) != 1 {
		t.Fatalf("%d cuts, want 1", len(g.cutLog))
	}
	onset := int(g.runStarts[0])
	keep := fr * freezeKeepMS / 1000
	for i := onset; i < onset+keep; i++ {
		if out[i] != concat(lead, f)[i] {
			t.Fatalf("the kept start of the sound was altered at +%d ms", (i-onset)*1000/fr)
		}
	}
	c := g.cutLog[0]
	for i := c.from; i < c.to; i++ {
		if out[i] != 0 {
			t.Fatalf("the held tail still plays at +%d ms", (int(i)-onset)*1000/fr)
		}
	}
	// Energy is the wrong yardstick here — a fading sound keeps most of its
	// energy in the part that is kept. What matters is that the held tail is
	// gone: nothing sounds after the keep and the tail fade.
	end := onset + fr*(freezeKeepMS+freezeTailFadeMS)/1000
	for i := end; i < len(lead)+len(f); i++ {
		if out[i] != 0 {
			t.Fatalf("still sounding %d ms after the onset; the tail should end by %d ms",
				(i-onset)*1000/fr, freezeKeepMS+freezeTailFadeMS)
		}
	}
}

// A freeze just after a second session opens is that session's opening buzz
// and goes whole, however long the stream has run.
func TestASecondSessionsOpeningIsRemovedWhole(t *testing.T) {
	first := concat(natural(2.0), silence(2.0))
	opening := natural(0.3)
	f := frozen()
	g := newFreezeGate(fr)
	g.MarkSessionStart()
	var out []float32
	out = append(out, g.Process(first)...)
	g.MarkSessionStart() // what the translator reports before the new session's first audio
	out = append(out, g.Process(concat(opening, f, silence(0.5)))...)
	if len(g.runStarts) != 1 {
		t.Fatalf("%d freezes, want 1", len(g.runStarts))
	}
	at := len(first) + len(opening)
	if left := energy(out[at:at+len(f)]) / energy(f); left > 0.01 {
		t.Errorf("%.1f%% of an opening freeze still plays", 100*left)
	}
}

// Silence alone is not a session boundary: paragraph pauses run 1.0–2.3 s,
// against 1.9–2.8 s at a handover. Without the translator's mark, a freeze
// after a long pause is a held word ending and keeps its first 50 ms.
func TestALongPauseIsNotASessionStart(t *testing.T) {
	lead := concat(natural(2.0), silence(2.5), natural(0.3))
	out, g := gate(concat(lead, frozen()))
	if len(g.runStarts) != 1 {
		t.Fatalf("%d freezes, want 1", len(g.runStarts))
	}
	onset := int(g.runStarts[0])
	if out[onset+fr*freezeKeepMS/2000] == 0 {
		t.Error("a freeze after a pause was removed whole, as if it opened a session")
	}
}

// The tail fade must not click either.
func TestTheTailCutDoesNotClick(t *testing.T) {
	for off := 0; off < 40; off += 3 {
		lead := concat(natural(2.0), silence(float64(off)/1000), natural(0.4))
		in := concat(lead, frozen(), natural(0.5))
		out, g := gate(in)
		if len(g.cutLog) != 1 {
			t.Fatalf("offset %d: %d cuts", off, len(g.cutLog))
		}
		c := g.cutLog[0]
		hard := append([]float32(nil), in...)
		for i := c.from; i < c.to; i++ {
			hard[i] = 0
		}
		if got, worst := hf(out, int(c.from)-c.fade/2), hf(hard, int(c.from)); got > 0.25*worst {
			t.Errorf("offset %d ms: tail fade has %.2g of click energy, a hard cut %.2g", off, got, worst)
		}
	}
}
