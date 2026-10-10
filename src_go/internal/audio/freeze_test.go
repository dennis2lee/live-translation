package audio

import (
	"fmt"
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

// jittered is the frozen fade with cycle-to-cycle period jitter of amt(t),
// which lowers periodicity while leaving pitch and timbre where they were.
func jittered(amt func(t float64) float64) []float32 {
	r := rand.New(rand.NewPCG(5, 1))
	out := make([]float32, int(0.28*fr))
	phase, period := 0.0, 0.0
	for i := range out {
		t := float64(i) / fr
		if phase >= period {
			phase -= period
			period = 1 / (174 * (1 + amt(t)*(r.Float64()*2-1)))
		}
		ph := 2 * math.Pi * phase / period
		v := math.Sin(ph) + 0.5*math.Sin(2*ph) + 0.25*math.Sin(3*ph)
		out[i] = float32(v / 1.75 * math.Pow(10, (-6-7*t/0.04)/20))
		phase += 1.0 / fr
	}
	return out
}

// steadyNoise is the frozen fade with a noise pattern that repeats every
// window, which lowers periodicity by c without moving pitch or timbre.
func steadyNoise(c float64) []float32 {
	r := rand.New(rand.NewPCG(9, 9))
	pat := make([]float64, fr*freezeWindowMS/1000)
	for i := range pat {
		pat[i] = r.Float64()*2 - 1
	}
	h := frozen()
	for i := range h {
		t := float64(i) / fr
		h[i] += float32(c * pat[i%len(pat)] * math.Pow(10, (-6-7*t/0.04)/20))
	}
	return h
}

// frozenLate holds its level for hold seconds before the fade begins, as two
// of the fifteen recorded freezes did for 18–28 ms. Only following the freeze
// back from where the fade is confirmed finds its real start.
func frozenLate(hold float64) []float32 {
	return voiced(hold+0.28, func(float64) float64 { return 174 },
		func(t float64) float64 { return -6 - 7*max(0, t-hold)/0.04 })
}

func frozen() []float32 { return frozenFor(0.28, 7) }

// gate runs in through a fresh gate in 250 ms blobs — the size the model
// sends — as one session that opens at its first sample, followed by enough
// silence for the opening to be decided and the episode to end. Every test
// that uses it therefore also checks that all of the audio came back out:
// a gate that cut the freeze and then held everything after it passed every
// test until this did.
func gate(in []float32) ([]float32, *freezeGate) {
	g := newFreezeGate(fr)
	g.MarkSessionStart()
	in = concat(in, silence(float64(freezeStartMS)/1000+0.5))
	var out []float32
	blob := fr / 4
	for i := 0; i < len(in); i += blob {
		out = append(out, g.Process(in[i:min(i+blob, len(in))])...)
	}
	if len(out) != len(in) || g.active {
		panic(fmt.Sprintf("gate held back %d of %d samples (still watching: %v)",
			len(in)-len(out), len(in), g.active))
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
	// The freeze starts on the grid here, so the cut takes exactly its
	// one-hop lead and the fade before it, and nothing earlier.
	keep := len(speech) - fr*(freezeHopMS+freezeFadeOutMS)/1000
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
	// Each sits just outside its threshold — measured with the gate's own
	// features — so loosening the threshold by less than the gap starts
	// cutting it.
	cases := map[string][]float32{
		// The upper harmonics grow steadily through the fade: found with the
		// detector itself to clear a 10% timbre tolerance everywhere and be
		// caught at 15%.
		"timbre moves": voicedMix(0.28, func(float64) float64 { return 174 },
			func(t float64) float64 { return -6 - 7*t/0.04 },
			func(t float64) (float64, float64) {
				m := math.Exp(2 * t / 0.28)
				return 0.5 * m, 0.25 * m
			}),
		// Pitch moves 2% over the fade; tolerance 1%.
		"pitch drifts": voiced(0.28, func(t float64) float64 { return 174 * (1 + 0.02*t/0.12) },
			func(t float64) float64 { return -6 - 7*t/0.04 }),
		// Periodicity falls as cycle-to-cycle jitter rises, with timbre
		// unmoved; tolerance 0.05. Found with the detector: clear at 0.05,
		// caught at 0.08. Since pitch is interpolated between lags, more
		// jitter than this also moves the pitch past 1%, and the case would
		// be stopped by that instead — 0.08 was, and hid a looser tolerance.
		"periodicity drifts": jittered(func(t float64) float64 { return 0.07 * t / 0.12 }),
		// Periodicity about 0.44 throughout, everything else still; the
		// floor is 0.5.
		"weakly periodic": steadyNoise(0.85),
		// Fading 1.7 dB per 40 ms; a freeze falls at least 2.
		"fades slowly": frozenFor(0.4, 1.7),
		// Only 120 ms frozen: three windows, one short of the four required.
		"too short": frozenFor(0.12, 7),
		// Fading from -62.6 to -69 dBFS, under the -60 floor.
		"below the floor": voiced(0.28, func(float64) float64 { return 174 },
			func(t float64) float64 { return -60.5 - 2.2*t/0.04 }),
		// Periodicity and timbre collapse as noise rises through the fade.
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

// highBand returns the energy of x[a:b] above 1 kHz and in all, Hann-
// windowed. The test signals have three harmonics and nothing of their own
// above about 700 Hz, so what a cut adds up there is its click.
func highBand(x []float32, a, b int) (hi, all float64) {
	n := b - a
	for k := 0; k <= n/2; k++ {
		var re, im float64
		for i := 0; i < n; i++ {
			w := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
			v := float64(x[a+i]) * w
			ang := 2 * math.Pi * float64(k*i) / float64(n)
			re += v * math.Cos(ang)
			im -= v * math.Sin(ang)
		}
		e := re*re + im*im
		all += e
		if float64(k)*fr/float64(n) > 1000 {
			hi += e
		}
	}
	return hi, all
}

// clickAdded is how much energy above 1 kHz the gate put into out[a:b] that
// in did not already have, as a share of in's energy there. Measured against
// the input, so an abrupt sound that follows the freeze is not counted as the
// gate's click.
func clickAdded(in, out []float32, a, b int) float64 {
	hiIn, allIn := highBand(in, a, b)
	hiOut, _ := highBand(out, a, b)
	return max(0, hiOut-hiIn) / allIn
}

// The cut must not click, at either end and at any alignment. Measured above
// 1 kHz, where these signals have nothing of their own: a hard cut adds
// 900–27 500 ppm of the energy there, a 1 ms fade up to 1 350, 3 ms at most
// 4, the 10 ms fades none. The limit sits between 3 ms and 2 ms.
func TestTheCutDoesNotClick(t *testing.T) {
	const limit = 10e-6
	win := fr * 30 / 1000
	for off := 0; off < 40; off += 3 {
		lead := concat(silence(float64(off)/1000), natural(0.6))
		in := concat(lead, frozen(), natural(0.5)) // speech resumes straight after
		out, g := gate(in)
		if len(g.cutLog) != 1 {
			t.Fatalf("offset %d: %d cuts, want 1", off, len(g.cutLog))
		}
		c := g.cutLog[0]
		for name, at := range map[string]int{
			"fade out": int(c.from) - c.fade/2,
			"fade in":  int(c.to) + fr*freezeFadeInMS/2000,
		} {
			if got := clickAdded(concat(in, silence(2)), out, at-win/2, at+win/2); got > limit {
				t.Errorf("offset %d ms, %s: adds %.0f ppm above 1 kHz, limit %.0f",
					off, name, got*1e6, limit*1e6)
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
	g.MarkSessionStart() // without this the gate passes through and holds nothing to test
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
	g.MarkSessionStart()
	g.Process(natural(1.0))
	if len(g.buf) == 0 {
		t.Fatal("setup: nothing held during the opening")
	}
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
	p.MarkSessionStart()
	p.WriteSpeech(toPCM(natural(0.5)))
	if len(p.freeze.buf) == 0 {
		t.Fatal("setup: the gate is holding nothing")
	}
	p.ResetSpeech()
	if len(p.freeze.buf) != 0 {
		t.Error("ResetSpeech left the held audio in place")
	}
}

// --- episodes: only a session's opening is watched ----------------------------

// After the opening the gate passes everything straight through, freezes
// included: there the frozen frame is the end of a real word, and every cut
// tried — whole, or trimmed to its first 50 ms — turned "question" into
// "quest-".
func TestAFreezeAfterTheOpeningIsLeftAlone(t *testing.T) {
	// 1.5 s in, written out rather than derived from freezeStartMS: the
	// latest opening freeze measured began 0.84 s in, and a window that
	// stretched to here would start reaching real words.
	in := concat(natural(1.5), frozen(), natural(0.3))
	out, g := gate(in)
	if len(g.runStarts) != 0 {
		t.Error("a freeze 1.5 s into the session was cut")
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("audio after the opening was altered at sample %d", i)
		}
	}
}

// Without a session start there is no episode: nothing is held, nothing is
// cut, and the audio comes out in the same call it went in.
func TestWithoutASessionStartNothingIsHeld(t *testing.T) {
	g := newFreezeGate(fr)
	in := concat(natural(0.5), frozen(), natural(0.3))
	out := g.Process(in)
	if len(out) != len(in) {
		t.Fatalf("returned %d of %d samples at once", len(out), len(in))
	}
	if len(g.runStarts) != 0 {
		t.Error("a freeze was cut with no session start")
	}
}

// Once the opening has been decided, the episode ends and the delay goes:
// everything held is let out and later audio passes straight through.
func TestTheEpisodeEndsAfterTheOpening(t *testing.T) {
	g := newFreezeGate(fr)
	g.MarkSessionStart()
	g.Process(natural(float64(freezeStartMS)/1000 + 0.4))
	if g.active {
		t.Fatal("still watching after the opening was decided")
	}
	later := natural(0.25)
	if out := g.Process(later); len(out) != len(later) {
		t.Errorf("after the opening, %d of %d samples came straight out", len(out), len(later))
	}
}

// A new session's opening is watched however long the stream has run.
func TestASecondSessionsOpeningIsRemovedWhole(t *testing.T) {
	first := concat(natural(2.0), silence(2.0))
	opening := natural(0.3)
	f := frozen()
	g := newFreezeGate(fr)
	g.MarkSessionStart()
	var out []float32
	out = append(out, g.Process(first)...)
	g.MarkSessionStart()
	out = append(out, g.Process(concat(opening, f, silence(0.5)))...)
	out = append(out, g.Process(silence(1.5))...)
	if len(g.runStarts) != 1 {
		t.Fatalf("%d freezes, want 1", len(g.runStarts))
	}
	at := len(first) + len(opening)
	if left := energy(out[at:at+len(f)]) / energy(f); left > 0.01 {
		t.Errorf("%.1f%% of an opening freeze still plays", 100*left)
	}
}

// A freeze that holds its level before fading has to be followed back to
// where it really starts, or its loudest part plays: two of fifteen recorded
// freezes did, and the cut that waited for the fade left 16–21% of them.
func TestAFreezeIsFollowedBackToItsStart(t *testing.T) {
	for off := 0; off < 40; off += 3 {
		lead := concat(silence(float64(off)/1000), natural(0.6))
		f := frozenLate(0.07) // needs the full 40 ms of following back
		out, g := gate(concat(lead, f))
		if len(g.runStarts) != 1 {
			t.Fatalf("offset %d ms: %d freezes, want 1", off, len(g.runStarts))
		}
		if left := energy(out[len(lead):len(lead)+len(f)]) / energy(f); left > 0.01 {
			t.Errorf("offset %d ms: %.1f%% of a freeze that held before fading still plays", off, 100*left)
		}
	}
}

// The cut reaches back before the point where the freeze is confirmed, so
// those samples must still be held when it is. With 250 ms blobs that
// happened to be true either way; fed a few milliseconds at a time it is not.
func TestTheCutReachesBackWithTinyBlobs(t *testing.T) {
	lead := natural(0.6)
	f := frozenLate(0.06)
	in := concat(lead, f, silence(1.5))
	g := newFreezeGate(fr)
	g.MarkSessionStart()
	var out []float32
	for i := 0; i < len(in); i += fr / 200 { // 5 ms
		out = append(out, g.Process(in[i:min(i+fr/200, len(in))])...)
	}
	if left := energy(out[len(lead):len(lead)+len(f)]) / energy(f); left > 0.01 {
		t.Errorf("fed 5 ms at a time, %.1f%% of the freeze still plays", 100*left)
	}
}

// MarkSessionStart has to reach the gate, or no opening is ever watched and
// the buzz plays every time. Watching shows as audio being held.
func TestMarkSessionStartReachesTheGate(t *testing.T) {
	p, _ := runningPlayer(t)
	p.WriteSpeech(toPCM(natural(0.5)))
	if len(p.freeze.buf) != 0 {
		t.Fatal("setup: audio held with no session start")
	}
	p.MarkSessionStart()
	p.WriteSpeech(toPCM(natural(0.5)))
	if len(p.freeze.buf) == 0 {
		t.Error("after MarkSessionStart the opening was not held for a decision")
	}
}

// The window has to cover the openings that were measured — the latest began
// 0.84 s into its session — with room to spare.
func TestAFreezeLateInTheOpeningIsStillRemoved(t *testing.T) {
	lead := natural(1.0)
	f := frozen()
	out, g := gate(concat(lead, f))
	if len(g.runStarts) != 1 {
		t.Fatalf("a freeze 1.0 s into the session: %d cuts, want 1", len(g.runStarts))
	}
	if left := energy(out[len(lead):len(lead)+len(f)]) / energy(f); left > 0.01 {
		t.Errorf("%.1f%% of it still plays", 100*left)
	}
}

// Following a freeze back stops where the sound stops being the same frame.
// Here a pure tone turns into the freeze's richer timbre over the 60 ms
// before it, with pitch, phase and periodicity unbroken, so only the timbre
// check can stop the walk. Measured: with it the onset lands 40 ms early —
// the last of the blend is within 10% of the freeze's timbre — and without
// it 60 ms, the full walk. (An abrupt change would stop the walk on
// periodicity alone and show nothing.)
func TestFollowingBackStopsAtADifferentSound(t *testing.T) {
	const at, blend = 0.3, 0.06
	tone := voicedMix(at+0.28, func(float64) float64 { return 174 },
		func(t float64) float64 { return -6 - 7*max(0, t-at)/0.04 },
		func(t float64) (float64, float64) {
			m := min(1, max(0, (t-(at-blend))/blend))
			return 0.5 * m, 0.25 * m
		})
	lead := natural(0.4)
	_, g := gate(concat(lead, tone))
	if len(g.runStarts) != 1 {
		t.Fatalf("%d freezes, want 1", len(g.runStarts))
	}
	freezeAt := len(lead) + int(at*fr)
	if early := (freezeAt - int(g.runStarts[0])) * 1000 / fr; early > 50 {
		t.Errorf("onset placed %d ms before the freeze, inside a different timbre", early)
	}
}

// The window is about where the freeze is, not about how the audio arrives:
// handed the whole opening and more in one call, the gate still leaves a
// freeze past the window alone.
func TestTheWindowHoldsWhenAudioArrivesAllAtOnce(t *testing.T) {
	g := newFreezeGate(fr)
	g.MarkSessionStart()
	in := concat(natural(1.5), frozen(), natural(0.5))
	g.Process(in)
	if len(g.runStarts) != 0 {
		t.Error("a freeze 1.5 s in was cut because it arrived in the same call as the opening")
	}
}

// A session that delivers less than an opening's worth, then a new session:
// what the gate was holding is still speech and must come out, ahead of the
// new session's audio. It used to be released into a buffer the next call
// cleared — 210 ms of speech gone at every such handover.
func TestANewSessionMidOpeningLosesNothing(t *testing.T) {
	g := newFreezeGate(fr)
	g.MarkSessionStart()
	first := natural(0.75)
	second := concat(natural(1.0), silence(2.0))
	var out []float32
	out = append(out, g.Process(first)...)
	g.MarkSessionStart()
	// Several calls, not one: what was carried must come out exactly once.
	for i := 0; i < len(second); i += fr / 4 {
		out = append(out, g.Process(second[i:min(i+fr/4, len(second))])...)
	}
	in := concat(first, second)
	if len(out) != len(in) {
		t.Fatalf("%d of %d samples came out", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("sample %d differs: the first session's held tail was lost or reordered", i)
		}
	}
}

// A session start off the 10 ms grid: the window index has to round up, or
// the first window reaches before the samples it may read.
func TestASessionStartOffTheGrid(t *testing.T) {
	g := newFreezeGate(fr)
	g.Process(natural(0.0137)) // 329 samples, not a multiple of a hop
	g.MarkSessionStart()
	in := concat(natural(0.6), frozen(), silence(2.0))
	var out []float32
	for i := 0; i < len(in); i += fr / 4 {
		out = append(out, g.Process(in[i:min(i+fr/4, len(in))])...)
	}
	if len(g.runStarts) != 1 {
		t.Errorf("%d freezes found after an off-grid start, want 1", len(g.runStarts))
	}
	if len(out) != len(in) {
		t.Errorf("%d of %d samples came out", len(out), len(in))
	}
}

// The other side of each threshold: a freeze just inside every one must still
// be cut, so tightening any of them shows. Each was placed with the gate's own
// features: it is found as shipped and lost when that one threshold is
// tightened as named.
func TestNearHitsAreFreezes(t *testing.T) {
	cases := map[string][]float32{
		"fading 2.2 dB per 40 ms (lost at 2.5)": frozenFor(0.28, 2.2),
		"timbre drifting (lost at 7%)": voicedMix(0.28, func(float64) float64 { return 174 },
			func(t float64) float64 { return -6 - 7*t/0.04 },
			func(t float64) (float64, float64) {
				m := math.Exp(0.8 * t / 0.28)
				return 0.5 * m, 0.25 * m
			}),
		"periodicity drifting (lost at 0.03)":       jittered(func(t float64) float64 { return 0.06 * t / 0.12 }),
		"periodicity 0.55 throughout (lost at 0.6)": steadyNoise(0.65),
		"quiet, from -44 dBFS (lost at a -50 floor)": voiced(0.28, func(float64) float64 { return 174 },
			func(t float64) float64 { return -44 - 2.5*t/0.04 }),
	}
	for name, in := range cases {
		_, g := gate(concat(natural(0.5), in, natural(0.3)))
		if len(g.runStarts) != 1 {
			t.Errorf("%s: %d cuts, want 1", name, len(g.runStarts))
		}
	}
}

// Following back stops at a change of pitch too. A glide from 200 Hz runs
// into the freeze at 174 Hz with the timbre unchanged; the walk must stop
// where the pitch stops moving, not run the full 40 ms up the glide.
func TestFollowingBackStopsWherePitchMoves(t *testing.T) {
	const at, glide = 0.3, 0.06
	tone := voiced(at+0.28, func(t float64) float64 {
		if t >= at {
			return 174
		}
		return 174 + 26*min(1, (at-t)/glide)
	}, func(t float64) float64 { return -6 - 7*max(0, t-at)/0.04 })
	lead := natural(0.4)
	_, g := gate(concat(lead, tone))
	if len(g.runStarts) != 1 {
		t.Fatalf("%d freezes, want 1", len(g.runStarts))
	}
	freezeAt := len(lead) + int(at*fr)
	if early := (freezeAt - int(g.runStarts[0])) * 1000 / fr; early > 20 {
		t.Errorf("onset placed %d ms before the freeze, up the glide", early)
	}
}

// After the cut the sound comes back within the fade-in and is untouched
// from there on.
func TestTheSoundAfterTheCutIsBackWithinTheFade(t *testing.T) {
	lead := natural(0.6)
	in := concat(lead, frozen(), natural(0.5))
	out, g := gate(in)
	c := g.cutLog[0]
	back := int(c.to) + fr*10/1000
	for i := back; i < len(in); i++ {
		if out[i] != in[i] {
			t.Fatalf("the sound after the cut is still altered %d ms after it",
				(i-int(c.to))*1000/fr)
		}
	}
}

// audibleIn reports the first sample of x[a:b] louder than -50 dBFS, or -1.
func audibleIn(x []float32, a, b int) int {
	floor := float32(math.Pow(10, -50.0/20))
	for i := a; i < b; i++ {
		if x[i] > floor || x[i] < -floor {
			return i
		}
	}
	return -1
}

// A freeze that starts late in the opening and runs past its end must be cut
// to its end: the episode cannot close while a cut is still open. Energy is
// too blunt a measure here — what is left of a slow fade is a small share of
// it — so nothing of the freeze may be audible.
func TestAFreezeRunningPastTheWindowIsCutToItsEnd(t *testing.T) {
	// Fading 4 dB per 40 ms, inside the 4–12 measured, for half a second:
	// it is still sounding well after the window closes at 1.2 s. (A fade
	// a hair over the 2 dB threshold is a near-hit, tested elsewhere; here
	// the question is only whether the episode waits for the cut to end.)
	lead := natural(1.15)
	f := frozenFor(0.5, 4)
	out, g := gate(concat(lead, f))
	if len(g.runStarts) != 1 {
		t.Fatalf("%d freezes, want 1", len(g.runStarts))
	}
	if i := audibleIn(out, len(lead), len(lead)+len(f)-fr/100); i >= 0 {
		t.Errorf("the freeze is still audible %d ms in", (i-len(lead))*1000/fr)
	}
}

// When speech follows a freeze straight away, the freeze has not faded out
// by its end, so the cut has to reach its last frozen sample — and stop
// there. Swept across the 10 ms grid: at one alignment the old 40 ms steps
// happened to land on the end, and at 32 of 40 they stopped up to 34 ms
// short. Measured against where the freeze really ends, not against the
// gate's own idea of it, so a cut that runs on into the speech shows too.
func TestTheCutEndsWhereTheFreezeEnds(t *testing.T) {
	for i := 0; i < 80; i++ {
		off := i % 40
		// 200 ms, and 170 ms — barely long enough to be confirmed, so the
		// cut's end is the one set at confirmation, never extended.
		length := []float64{0.2, 0.17}[i/40]
		lead := concat(silence(float64(off)/1000), natural(0.6))
		f := frozenFor(length, 7) // ends loud, at -41 dBFS or above
		speech := natural(0.4)
		in := concat(lead, f, speech)
		out, _ := gate(in)
		end := len(lead) + len(f)
		// The last fully frozen window can end up to a hop before the freeze
		// does; the fade back in covers the rest.
		if i := audibleIn(out, len(lead), end-fr*freezeHopMS/1000); i >= 0 {
			t.Errorf("%.0f ms freeze, offset %d ms: still audible %d ms before its end",
				length*1000, off, (end-i)*1000/fr)
		}
		// From the end of the fade back in, the speech after is untouched.
		for i := end + fr*freezeFadeInMS/1000; i < len(in); i++ {
			if out[i] != in[i] {
				t.Errorf("%.0f ms freeze, offset %d ms: the cut ran %d ms into the speech after it",
					length*1000, off, (i-end)*1000/fr)
				break
			}
		}
	}
}

// The fade back in, where the input itself runs on smoothly: the frame
// freezes and fades for 160 ms, then unfreezes — the pitch starts to glide —
// at the level it had reached, with no break in phase or level. Anything the
// output gains above 1 kHz there is the cut's own click. (Where speech starts
// abruptly after a freeze, its own onset hides a short fade.)
func TestTheFadeBackInDoesNotClick(t *testing.T) {
	const frozenFor = 0.16
	for off := 0; off < 40; off += 3 {
		tone := voiced(0.5, func(t float64) float64 {
			return 174 + 300*max(0, t-frozenFor)
		}, func(t float64) float64 {
			return -6 - 7*min(t, frozenFor)/0.04
		})
		lead := concat(silence(float64(off)/1000), natural(0.6))
		in := concat(lead, tone)
		out, g := gate(in)
		if len(g.cutLog) != 1 {
			t.Fatalf("offset %d: %d cuts, want 1", off, len(g.cutLog))
		}
		at := int(g.cutLog[0].to) + fr*freezeFadeInMS/2000
		win := fr * 30 / 1000
		if got := clickAdded(concat(in, silence(2)), out, at-win/2, at+win/2); got > 10e-6 {
			t.Errorf("offset %d ms: the fade back in adds %.0f ppm above 1 kHz", off, got*1e6)
		}
	}
}

// The window ends 1.2 s after the session's first audio, measured from that
// first audio: a freeze at 1.25 s is past it.
func TestTheWindowEndsWhereItShould(t *testing.T) {
	in := concat(natural(1.25), frozen(), natural(0.3))
	_, g := gate(in)
	if len(g.runStarts) != 0 {
		t.Error("a freeze 1.25 s into the session was cut; the window is 1.2 s")
	}
}

// Low and high voices freeze too: the recorded openings froze between 120
// and 370 Hz. A pitch search that started higher would miss the low ones.
func TestLowAndHighVoicesAreFound(t *testing.T) {
	for _, hz := range []float64{100, 120, 370} {
		f := voiced(0.28, func(float64) float64 { return hz },
			func(t float64) float64 { return -6 - 7*t/0.04 })
		_, g := gate(concat(natural(0.5), f, natural(0.3)))
		if len(g.runStarts) != 1 {
			t.Errorf("a freeze at %.0f Hz: %d cuts, want 1", hz, len(g.runStarts))
		}
	}
}

// A DC offset must not hide a freeze: pitch and timbre are measured on the
// signal with its mean taken out.
func TestAnOffsetDoesNotHideAFreeze(t *testing.T) {
	f := frozen()
	for i := range f {
		f[i] += 0.05
	}
	_, g := gate(concat(natural(0.5), f, natural(0.3)))
	if len(g.runStarts) != 1 {
		t.Errorf("a freeze riding on a DC offset: %d cuts, want 1", len(g.runStarts))
	}
}

// Reset is for discarding: a carry waiting to go out goes too.
func TestResetDropsAPendingCarry(t *testing.T) {
	g := newFreezeGate(fr)
	g.MarkSessionStart()
	g.Process(natural(0.75))
	g.MarkSessionStart() // carries what the first session left held
	g.Reset()
	next := natural(0.3)
	if out := g.Process(next); len(out) != len(next) {
		t.Errorf("after Reset, %d samples came out for %d put in — the carry survived",
			len(out), len(next))
	}
}

// ringSamples drains the ring and returns what the device would have played.
func ringSamples(st *playState) []float64 {
	buf := make([]byte, st.ring.Available())
	st.ring.Read(buf)
	out := make([]float64, len(buf)/2)
	for i := range out {
		out[i] = float64(int16(uint16(buf[2*i])|uint16(buf[2*i+1])<<8)) / 32768
	}
	return out
}

func power(x []float64) float64 {
	var s float64
	for _, v := range x {
		s += v * v
	}
	return s / float64(max(len(x), 1))
}

// Every other player test checks that audio reached the gate, never that the
// player plays what came back out of it. Discarding the gate's output left the
// whole suite green with the buzz still audible — the red team's fifth-round
// blocker. This reads the ring, which is what the device plays.
func TestThePlayerPlaysWhatTheGateReleases(t *testing.T) {
	p, st := runningPlayer(t)
	lead, f := natural(0.6), frozen()
	in := concat(lead, f, natural(0.4), silence(1.5))
	p.MarkSessionStart()
	for i := 0; i < len(in); i += fr / 4 {
		p.WriteSpeech(toPCM(in[i:min(i+fr/4, len(in))]))
	}
	out := ringSamples(st) // 48 kHz: twice the input's sample positions
	inset := 2 * fr * 30 / 1000
	speech := power(out[2*fr/10 : 2*len(lead)-inset])
	freeze := power(out[2*len(lead)+inset : 2*(len(lead)+len(f))-inset])
	if speech == 0 {
		t.Fatal("setup: no speech reached the ring")
	}
	if r := freeze / speech; r > 0.01 {
		t.Errorf("the freeze plays at %.1f%% of the speech's power, want under 1%%", 100*r)
	}
}

// The integer-lag pitch estimate moves in steps of more than 1% above about
// 240 Hz, so a frozen voice up there was judged to be changing pitch and
// missed — 66 of 2554 in the red team's sweep, every one between 250 and
// 390 Hz. These pitches sit between integer lags on purpose.
func TestHighVoicesBetweenLagsAreFound(t *testing.T) {
	for hz := 250.0; hz <= 400; hz += 7.3 {
		f := voiced(0.28, func(float64) float64 { return hz },
			func(t float64) float64 { return -6 - 7*t/0.04 })
		lead := natural(0.5)
		out, g := gate(concat(lead, f, natural(0.3)))
		if len(g.runStarts) != 1 {
			t.Errorf("a freeze at %.1f Hz: %d cuts, want 1", hz, len(g.runStarts))
			continue
		}
		if left := energy(out[len(lead):len(lead)+len(f)]) / energy(f); left > 0.01 {
			t.Errorf("a freeze at %.1f Hz: %.1f%% still plays", hz, 100*left)
		}
	}
}

// Holding audio for a decision is the cost of the gate: a listener hears it as
// delay. Part-way through an opening the gate holds what a decision needs and
// no more: a freeze is confirmed by four 40 ms windows (160 ms), and a cut can
// then reach back 40 ms plus a hop and a fade (60 ms). Measured 210 at 10 ms
// input steps.
func TestTheOpeningIsHeldNoLongerThanTheDecisionNeeds(t *testing.T) {
	const limitMS = 220
	g := newFreezeGate(fr)
	g.MarkSessionStart()
	in := natural(1.0)
	step := fr / 100 // 10 ms
	var in_, out_, worst int
	for i := 0; i < len(in); i += step {
		chunk := in[i:min(i+step, len(in))]
		in_ += len(chunk)
		out_ += len(g.Process(chunk))
		if i >= fr/5 {
			worst = max(worst, in_-out_)
		}
	}
	if ms := worst * 1000 / fr; ms > limitMS {
		t.Errorf("the opening is held up to %d ms behind its input, limit %d", ms, limitMS)
	}
}

// The click test, fed the way TestTheCutReachesBackWithTinyBlobs is: 5 ms at a
// time, with a freeze that holds its level before it fades, so the cut is
// placed by following it back rather than where the fade was confirmed.
func TestTheCutDoesNotClickWithTinyBlobs(t *testing.T) {
	const limit = 10e-6
	win := fr * 30 / 1000
	in := concat(natural(0.6), frozenLate(0.07), natural(0.5), silence(1.7))
	g := newFreezeGate(fr)
	g.MarkSessionStart()
	var out []float32
	for i := 0; i < len(in); i += fr / 200 {
		out = append(out, g.Process(in[i:min(i+fr/200, len(in))])...)
	}
	if len(out) != len(in) || len(g.cutLog) != 1 {
		t.Fatalf("setup: %d of %d samples out, %d cuts", len(out), len(in), len(g.cutLog))
	}
	c := g.cutLog[0]
	for name, at := range map[string]int{
		"fade out": int(c.from) - c.fade/2,
		"fade in":  int(c.to) + fr*freezeFadeInMS/2000,
	} {
		if got := clickAdded(in, out, at-win/2, at+win/2); got > limit {
			t.Errorf("%s: adds %.0f ppm above 1 kHz, limit %.0f", name, got*1e6, limit*1e6)
		}
	}
}

// Every sample written must reach the ring. Dropping one per blob is a few
// hundredths of a millisecond at the model's blob size, below what any power
// measurement sees; written ten samples at a time it is a tenth of the audio.
func TestEverySampleWrittenIsPlayed(t *testing.T) {
	p, st := runningPlayer(t)
	in := natural(1.0)
	for i := 0; i < len(in); i += 10 {
		p.Write(toPCM(in[i:min(i+10, len(in))]))
	}
	// 48 kHz mono, back to 24 kHz. The resampler keeps about 32 ms in its
	// filter (measured 96.8% of a second through), hence 95 and not 100.
	got := float64(st.ring.Available()/2) / 2
	if r := got / float64(len(in)); r < 0.95 || r > 1.0 {
		t.Errorf("%.0f of %d samples reached the ring (%.1f%%)", got, len(in), 100*r)
	}
}

// The same through the model's path, which the gate sits on: a whole opening,
// with enough silence after it for the episode to finish and release.
func TestEverySampleOfTheModelsSpeechIsPlayed(t *testing.T) {
	p, st := runningPlayer(t)
	in := concat(natural(1.0), silence(1.5))
	p.MarkSessionStart()
	for i := 0; i < len(in); i += 10 {
		p.WriteSpeech(toPCM(in[i:min(i+10, len(in))]))
	}
	got := float64(st.ring.Available()/2) / 2
	if r := got / float64(len(in)); r < 0.97 || r > 1.0 {
		t.Errorf("%.0f of %d samples reached the ring (%.1f%%)", got, len(in), 100*r)
	}
}

// A session that ends within its own opening hands what the gate holds to the
// next one. TestANewSessionMidOpeningLosesNothing pins that in the gate; this
// pins that nothing between the player's entry point and the gate drops it.
func TestASecondMarkMidOpeningLosesNothingInThePlayer(t *testing.T) {
	p, st := runningPlayer(t)
	first, second := natural(0.5), concat(natural(0.5), silence(1.5))
	p.MarkSessionStart()
	p.WriteSpeech(toPCM(first))
	p.MarkSessionStart()
	p.WriteSpeech(toPCM(second))
	want := len(first) + len(second)
	got := float64(st.ring.Available()/2) / 2
	if r := got / float64(want); r < 0.97 || r > 1.0 {
		t.Errorf("%.0f of %d samples reached the ring (%.1f%%)", got, want, 100*r)
	}
}

// Interpolation moves the pitch by at most half a lag. At the edge of the
// search the peak is not a maximum, and a parabola through it extrapolates:
// a 40 Hz tone read as -585 Hz before the guard.
func TestPitchStaysInsideTheSearch(t *testing.T) {
	g := newFreezeGate(fr)
	lo := float64(fr) / (float64(g.lagHi) + 0.5)
	hi := float64(fr) / (float64(g.lagLo) - 0.5)
	for hz := 30.0; hz <= 700; hz += 3.7 {
		w := voiced(float64(freezeWindowMS)/1000, func(float64) float64 { return hz },
			func(float64) float64 { return -6 })
		if f := g.features(w).f0; !(f >= lo && f <= hi) {
			t.Errorf("a %.1f Hz tone reads %.1f Hz, outside %.1f–%.1f", hz, f, lo, hi)
		}
	}
}

// Flush empties what would otherwise still play: the queued ring and the
// freeze gate's held opening. The gain is applied as audio is written, so
// without this a mute let up to 2 s of queued speech play on.
func TestFlushDropsWhatIsQueued(t *testing.T) {
	p, st := runningPlayer(t)
	p.MarkSessionStart()
	p.WriteSpeech(toPCM(natural(0.6))) // part of it held by the gate
	p.Write(toPCM(natural(0.5)))
	if st.ring.Available() == 0 || len(p.freeze.buf) == 0 {
		t.Fatal("setup: nothing queued or held")
	}
	p.Flush()
	if n := st.ring.Available(); n != 0 {
		t.Errorf("%d bytes still queued after Flush", n)
	}
	p.WriteSpeech(toPCM(silence(2)))
	if rms := ringRMS(st); rms > 1e-4 {
		t.Errorf("held audio came out after Flush (RMS %.4f)", rms)
	}
}

// A flush during a session's opening keeps the opening watched, and the
// watch still ends where the opening does: restarting it at every m toggle
// pushed the cutting out into the middle of a session.
func TestFlushKeepsAnOpeningWatched(t *testing.T) {
	// watchEnd feeds a session's audio in 50 ms blobs, flushing after
	// flushAt seconds (never if negative), and reports how much audio had
	// gone in when the watch ended.
	watchEnd := func(flushAt float64) float64 {
		p, _ := runningPlayer(t)
		p.MarkSessionStart()
		blob := toPCM(natural(0.05))
		fed := 0.0
		for fed < 4 {
			p.WriteSpeech(blob)
			fed += 0.05
			if flushAt >= 0 && fed >= flushAt && fed < flushAt+0.05 {
				p.Flush()
				if !p.freeze.active {
					t.Fatalf("the flush at %.2f s ended the watch", flushAt)
				}
			}
			if !p.freeze.active {
				return fed
			}
		}
		return fed
	}
	base := watchEnd(-1)
	for _, at := range []float64{0.5, 1.1} {
		if got := watchEnd(at); math.Abs(got-base) > 0.06 {
			t.Errorf("flushed at %.1f s, the watch ended at %.2f s instead of %.2f s", at, got, base)
		}
	}
	q, _ := runningPlayer(t)
	q.Flush()
	if q.freeze.active {
		t.Error("a flush outside an opening started a watch")
	}
}
