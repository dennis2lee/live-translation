package classify

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

// The machine is pure, so these tests drive verdicts and chunks directly —
// no model, no clock, no goroutine.

// machine builds one with an explicit hold and a one-frame release, which is
// the 1.3.0 behaviour and keeps the tests that predate the release hold
// saying what they always said.
func machine(holdFrames int) *gateMachine {
	return machineWith(holdFrames, 1)
}

func machineWith(holdFrames, releaseFrames int) *gateMachine {
	m := &gateMachine{holdFrames: holdFrames, releaseFrames: releaseFrames}
	m.prerollCap = prerollFor(releaseFrames)
	return m
}

func chunkOf(b byte) []byte { return bytes.Repeat([]byte{b}, 3200) }

func TestSpeechPassesStraightThrough(t *testing.T) {
	m := machine(4)
	m.onFrame(false)
	out := m.onChunk(chunkOf(1))
	if len(out) != 1 || !bytes.Equal(out[0], chunkOf(1)) {
		t.Fatalf("speech chunk did not pass: %d pieces", len(out))
	}
}

// One musical frame is a bar of a jingle or a misread — not a song.
func TestASingleMusicalFrameDoesNotClose(t *testing.T) {
	m := machine(4)
	m.onFrame(true)
	if m.closed {
		t.Fatal("closed after one musical frame — the hold is the whole point")
	}
	if out := m.onChunk(chunkOf(1)); len(out) != 1 {
		t.Fatalf("chunk suppressed while open: %d pieces", len(out))
	}
}

func TestSustainedMusicCloses(t *testing.T) {
	m := machine(4)
	for i := 0; i < 4; i++ {
		m.onFrame(true)
	}
	if !m.closed {
		t.Fatal("still open after the hold expired")
	}
	if out := m.onChunk(chunkOf(1)); out != nil {
		t.Fatalf("music chunk was sent: %d pieces", len(out))
	}
}

// Speech in the middle of the run starts the count over: the hold demands
// *consecutive* music, or a song with talking over it would creep past.
func TestTheHoldDemandsConsecutiveMusic(t *testing.T) {
	m := machine(4)
	m.onFrame(true)
	m.onFrame(true)
	m.onFrame(true)
	m.onFrame(false) // someone speaks
	m.onFrame(true)
	m.onFrame(true)
	m.onFrame(true)
	if m.closed {
		t.Fatal("closed on non-consecutive music")
	}
}

// The bug this whole release hold exists for. Until 1.4.0 a single clear
// frame reopened the gate, and inside real music single sub-threshold frames
// are the commonest event there is: ten pieces measured in y5_flap produced
// isolated one-frame dips in every one of them. Each reopening also flushed
// the pre-roll, so one misread frame sent seconds of song to be translated.
func TestOneClearFrameDoesNotReopenTheGate(t *testing.T) {
	m := machineWith(2, 4)
	m.onFrame(true)
	m.onFrame(true)
	if !m.closed {
		t.Fatal("setup: gate should be closed")
	}
	m.onFrame(false) // the misread
	if !m.closed {
		t.Fatal("one clear frame reopened the gate — the flap is back")
	}
	if out := m.onChunk(chunkOf(1)); out != nil {
		t.Fatalf("music leaked on a single misread: %d pieces", len(out))
	}
}

func TestTheGateReopensAfterTheReleaseHold(t *testing.T) {
	m := machineWith(2, 4)
	m.onFrame(true)
	m.onFrame(true)
	for i := 0; i < 3; i++ {
		m.onFrame(false)
		if !m.closed {
			t.Fatalf("reopened after %d clear frames, the hold is 4", i+1)
		}
	}
	m.onFrame(false)
	if m.closed {
		t.Fatal("still closed after the full release hold")
	}
}

// The release hold demands *consecutive* clear frames, exactly as the close
// hold demands consecutive musical ones. A song that dips for one frame every
// few seconds must not accumulate its way to a reopening.
func TestTheReleaseHoldDemandsConsecutiveClearFrames(t *testing.T) {
	m := machineWith(2, 4)
	m.onFrame(true)
	m.onFrame(true)
	for i := 0; i < 6; i++ {
		m.onFrame(false)
		m.onFrame(false)
		m.onFrame(true) // the song is still playing
	}
	if !m.closed {
		t.Fatal("intermittent clear frames added up to a reopening")
	}
}

// The pre-roll is derived from the release hold rather than fixed, so raising
// "Reopen after" in Preferences cannot start eating the first words after a
// song. This is the invariant that keeps the two numbers honest.
func TestThePrerollAlwaysCoversTheReleaseHold(t *testing.T) {
	for _, seconds := range []float64{0.5, 1.0, 1.5, 2.0, 3.0, 5.0} {
		frames := framesFor(seconds)
		held := prerollFor(frames) * chunkMS
		countdown := frames * hopSamples * 1000 / SampleRate
		if held < countdown {
			t.Errorf("release %.1fs: countdown %d ms but only %d ms held back",
				seconds, countdown, held)
		}
	}
	// And the shipped default is the measured one.
	if got := prerollFor(framesFor(DefaultTuning().ReleaseSeconds)); got != PrerollChunks {
		t.Errorf("default release implies %d pre-roll chunks, PrerollChunks says %d",
			got, PrerollChunks)
	}
}

// The reopening flushes what was suppressed — the words that triggered it are
// up to a patch length in the past, and clipping them is the defect this
// feature must never have.
func TestReopeningFlushesThePreroll(t *testing.T) {
	m := machine(1)
	m.onFrame(true) // closes
	m.onChunk(chunkOf(10))
	m.onChunk(chunkOf(11))
	m.onFrame(false) // reopens

	out := m.onChunk(chunkOf(12))
	if len(out) != 3 {
		t.Fatalf("flush produced %d chunks, want preroll(2)+current(1)", len(out))
	}
	for i, want := range []byte{10, 11, 12} {
		if out[i][0] != want {
			t.Errorf("flush order: piece %d starts with %d, want %d", i, out[i][0], want)
		}
	}
}

func TestThePrerollIsBounded(t *testing.T) {
	m := machine(1)
	m.onFrame(true)
	cap := m.prerollCap
	for i := 0; i < cap*3; i++ {
		m.onChunk(chunkOf(byte(i)))
	}
	m.onFrame(false)
	out := m.onChunk(chunkOf(200))
	if len(out) != cap+1 {
		t.Fatalf("flush produced %d chunks, want %d", len(out), cap+1)
	}
	// And it keeps the newest, not the oldest: the tail nearest the speech.
	if first := out[0][0]; first != byte(cap*3-cap) {
		t.Errorf("preroll kept the oldest chunks: flush starts at %d", first)
	}
}

// The suppressed chunk is copied. The send loop reuses its read buffer, and a
// pre-roll of aliases would flush fifteen copies of the newest chunk.
func TestThePrerollCopiesTheChunk(t *testing.T) {
	m := machine(1)
	m.onFrame(true)
	buf := chunkOf(1)
	m.onChunk(buf)
	for i := range buf {
		buf[i] = 99 // the send loop reads the next chunk into the same buffer
	}
	m.onFrame(false)
	out := m.onChunk(chunkOf(2))
	if out[0][0] != 1 {
		t.Error("the pre-roll aliased the caller's buffer")
	}
}

func TestGatedAccountingSeparatesDroppedFromFlushed(t *testing.T) {
	m := machine(1)
	m.onFrame(true)
	for i := 0; i < 20; i++ {
		m.onChunk(chunkOf(1))
	}
	m.onFrame(false)
	m.onChunk(chunkOf(2))

	if m.closedChunks != 20 {
		t.Errorf("closedChunks = %d, want 20", m.closedChunks)
	}
	if m.flushedChunks != int64(m.prerollCap) {
		t.Errorf("flushedChunks = %d, want %d", m.flushedChunks, m.prerollCap)
	}
}

// Adversarial sequences: whatever order frames and chunks interleave in, the
// invariants hold. This is the fuzz the plan promised, as a deterministic LCG
// sweep so a failure names its seed.
func TestGateMachineInvariantsUnderAdversarialSequences(t *testing.T) {
	for seed := int64(1); seed <= 50; seed++ {
		m := machine(3)
		x := seed
		sent, fed := 0, 0
		for step := 0; step < 400; step++ {
			x = (1103515245*x + 12345) % (1 << 31)
			switch x % 3 {
			case 0:
				m.onFrame(x%5 < 3)
			default:
				fed++
				sent += len(m.onChunk(chunkOf(byte(step))))
			}
			if len(m.preroll) > m.prerollCap {
				t.Fatalf("seed %d: preroll grew to %d", seed, len(m.preroll))
			}
			if !m.closed && len(m.preroll) > 0 {
				// Legal only between reopen and the next chunk.
				continue
			}
		}
		if int64(fed-sent) != m.closedChunks-m.flushedChunks {
			t.Fatalf("seed %d: fed %d, sent %d, but books say %d dropped",
				seed, fed, sent, m.closedChunks-m.flushedChunks)
		}
	}
}

// --- the wrapper, with the real model when present -------------------------

// The wrapper's contract with the send loop: music closes it, speech reopens
// it, end to end through real inference.
func TestGateClosesOnMusicAndReopensOnSpeech(t *testing.T) {
	if testing.Short() {
		t.Skip("real inference")
	}
	m := loadTestModel(t)
	// Built without the worker goroutine: the test calls classifyPending
	// itself, so every verdict lands at a known point.
	g := &Gate{
		model:  m,
		tuning: GateTuning{Threshold: 0.5, HoldSeconds: 1.0, ReleaseSeconds: 1.5},
		window: make([]float32, 0, PatchSamples),
		kick:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	g.machine.retune(g.tuning)

	feed := func(wave []float32) {
		// 100 ms chunks of PCM16, the shape the send loop delivers.
		chunk := make([]byte, 3200)
		for off := 0; off+1600 <= len(wave); off += 1600 {
			for i := 0; i < 1600; i++ {
				v := int16(wave[off+i] * 32767)
				chunk[2*i] = byte(v)
				chunk[2*i+1] = byte(uint16(v) >> 8)
			}
			g.Filter(chunk)
			g.classifyPending()
		}
	}

	// Three windows of synthetic music: enough for the hold's three
	// consecutive musical frames (Music scores 0.801 in the goldens).
	feed(inHarmonics())
	feed(inHarmonics())
	feed(inHarmonics())
	if !g.Closed() {
		v, _ := g.LastVerdict()
		t.Fatalf("gate open after 2 s of music (speech=%.2f music=%.2f)", v.Speech, v.Music)
	}

	// The TTS fixture is real Korean speech. Reopening now takes a confirmed
	// run of clear frames, so feed enough of it to satisfy the release hold —
	// about 2 s at the default — and then check the two things that matter:
	// the gate is open, and the speech held back while it made up its mind
	// came out rather than being eaten.
	feed(speechWave(t, 5*SampleRate))
	if g.Closed() {
		v, _ := g.LastVerdict()
		t.Fatalf("gate still closed after 5 s of speech (speech=%.2f music=%.2f top=%v)",
			v.Speech, v.Music, v.Top)
	}
	gated, flushed := g.GatedMS()
	t.Logf("gated %d ms, flushed %d ms back on reopening", gated, flushed)
	if flushed == 0 {
		t.Error("reopening flushed nothing — the first words after the music are being eaten")
	}
	// The flush has to cover the countdown itself, or the release hold is
	// paid for in lost words.
	countdown := int64(g.machine.releaseFrames) * hopSamples * 1000 / SampleRate
	if flushed < countdown {
		t.Errorf("flushed %d ms but the gate spent %d ms deciding — that gap is speech",
			flushed, countdown)
	}
}

// speechWave slices n samples of the TTS fixture, skipping the leading
// silence.
func speechWave(t *testing.T, n int) []float32 {
	t.Helper()
	raw, err := readFixtureWAV("../../spikes/fixtures/ko_fixture.wav")
	if err != nil {
		t.Skipf("speech fixture not present — regenerate with say/afconvert (%v)", err)
	}
	const skip = SampleRate // the first second is quiet
	if len(raw) < skip+n {
		t.Fatal("fixture too short")
	}
	return raw[skip : skip+n]
}

func readFixtureWAV(path string) ([]float32, error) {
	raw, err := readFileBytes(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < 44 || string(raw[0:4]) != "RIFF" {
		return nil, fmt.Errorf("not a WAV")
	}
	pos := 12
	for pos+8 <= len(raw) {
		id := string(raw[pos : pos+4])
		size := int(uint32(raw[pos+4]) | uint32(raw[pos+5])<<8 | uint32(raw[pos+6])<<16 | uint32(raw[pos+7])<<24)
		body := pos + 8
		if id == "data" {
			end := body + size
			if end > len(raw) {
				end = len(raw)
			}
			out := make([]float32, (end-body)/2)
			for i := range out {
				v := int16(uint16(raw[body+2*i]) | uint16(raw[body+2*i+1])<<8)
				out[i] = float32(v) / 32768
			}
			return out, nil
		}
		pos = body + size + size%2
	}
	return nil, fmt.Errorf("no data chunk")
}

func readFileBytes(path string) ([]byte, error) { return os.ReadFile(path) }

// The end-to-end regression this whole change exists to prevent: a real
// speech → music → speech timeline through the shipped Gate at DefaultTuning,
// counting what leaked and what was eaten.
//
// The music has to *flap*, or the test proves nothing — the first version of
// it used quiet bars, which move the classifier not at all (amplitude is not
// what it reads), and the old one-frame-reopen policy passed it unchanged.
// 900 ms of silence every 3 s is what produces the real failure mode: one
// isolated sub-threshold frame per cycle, scoring 0.41 — within 0.004 of the
// p25 measured on a real piano recording in y5_flap. Under the 1.3.0 policy
// each of those single frames reopened the gate *and* flushed the pre-roll.
func TestTheShippedGateDoesNotLeakOnMisreads(t *testing.T) {
	if testing.Short() {
		t.Skip("real inference over a 30 s timeline")
	}
	m := loadTestModel(t)
	speech := speechWave(t, 16*SampleRate)

	music := make([]float32, 18*SampleRate)
	src := inHarmonics()
	for i := range music {
		if i%(3*SampleRate) >= 3*SampleRate-900*SampleRate/1000 {
			continue // the 900 ms that makes one frame read as not-music
		}
		music[i] = src[i%len(src)]
	}

	const chunkSamples = 1600
	var wave []float32
	var truth []string
	add := func(w []float32, label string) {
		wave = append(wave, w...)
		for i := 0; i+chunkSamples <= len(w); i += chunkSamples {
			truth = append(truth, label)
		}
	}
	add(speech[:8*SampleRate], "speech")
	add(make([]float32, SampleRate), "gap") // the pause between sections
	add(music, "music")
	add(make([]float32, SampleRate), "gap")
	add(speech[8*SampleRate:], "speech")

	g := &Gate{
		model:  m,
		tuning: DefaultTuning(),
		window: make([]float32, 0, PatchSamples),
		kick:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	g.machine.retune(g.tuning)

	sent := make([]bool, len(truth))
	chunk := make([]byte, chunkSamples*2)
	for c := range truth {
		for i := 0; i < chunkSamples; i++ {
			v := int16(wave[c*chunkSamples+i] * 32767)
			chunk[2*i], chunk[2*i+1] = byte(v), byte(uint16(v)>>8)
		}
		// Stamp the index so a chunk that comes back in a pre-roll flush can
		// be traced to where it came from.
		chunk[0], chunk[1] = byte(c), byte(c>>8)
		for _, out := range g.Filter(chunk) {
			sent[int(out[0])|int(out[1])<<8] = true
		}
		g.classifyPending()
	}

	var leaked, clipped, total int
	for c, label := range truth {
		switch label {
		case "music":
			total++
			if sent[c] {
				leaked++
			}
		case "speech":
			if !sent[c] {
				clipped++
			}
		}
	}
	t.Logf("of %d ms of music, %d ms leaked; %d ms of speech eaten",
		total*100, leaked*100, clipped*100)

	// Eating speech is the defect this feature must never have, at any
	// tuning: the pre-roll exists so the release hold is free.
	if clipped != 0 {
		t.Errorf("%d ms of speech was suppressed and never flushed", clipped*100)
	}
	// The leak is the close latency — the 2 s hold plus about a second of
	// patch fill — and must not grow beyond it. Under the old policy this
	// timeline leaks several times as much, because every dip reopens.
	if budget := 5000; leaked*100 > budget {
		t.Errorf("%d ms of music leaked, budget %d ms — the gate is flapping again",
			leaked*100, budget)
	}
}
