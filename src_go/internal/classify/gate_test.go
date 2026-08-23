package classify

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

// The machine is pure, so these tests drive verdicts and chunks directly —
// no model, no clock, no goroutine.

func machine(holdFrames int) *gateMachine {
	return &gateMachine{holdFrames: holdFrames}
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

func TestSpeechReopensImmediately(t *testing.T) {
	m := machine(2)
	m.onFrame(true)
	m.onFrame(true)
	if !m.closed {
		t.Fatal("setup: gate should be closed")
	}
	m.onFrame(false)
	if m.closed {
		t.Fatal("one speech frame did not reopen the gate")
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
	for i := 0; i < PrerollChunks*3; i++ {
		m.onChunk(chunkOf(byte(i)))
	}
	m.onFrame(false)
	out := m.onChunk(chunkOf(200))
	if len(out) != PrerollChunks+1 {
		t.Fatalf("flush produced %d chunks, want %d", len(out), PrerollChunks+1)
	}
	// And it keeps the newest, not the oldest: the tail nearest the speech.
	if first := out[0][0]; first != byte(PrerollChunks*3-PrerollChunks) {
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
	if m.flushedChunks != PrerollChunks {
		t.Errorf("flushedChunks = %d, want %d", m.flushedChunks, PrerollChunks)
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
			if len(m.preroll) > PrerollChunks {
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
		tuning: GateTuning{Threshold: 0.5, HoldSeconds: 1.0},
		window: make([]float32, 0, PatchSamples),
		kick:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	g.machine.holdFrames = holdFramesFor(1.0)

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

	// The TTS fixture is real Korean speech; one frame of it must reopen.
	speech := speechWave(t)
	feed(speech)
	if g.Closed() {
		v, _ := g.LastVerdict()
		t.Fatalf("gate still closed after speech (speech=%.2f music=%.2f top=%v)",
			v.Speech, v.Music, v.Top)
	}
	gated, flushed := g.GatedMS()
	t.Logf("gated %d ms, flushed %d ms back on reopening", gated, flushed)
	if flushed == 0 {
		t.Error("reopening flushed nothing — the first words after the music are being eaten")
	}
}

// speechWave slices one patch of the TTS fixture, skipping the leading
// silence.
func speechWave(t *testing.T) []float32 {
	t.Helper()
	raw, err := readFixtureWAV("../../spikes/fixtures/ko_fixture.wav")
	if err != nil {
		t.Skipf("speech fixture not present — regenerate with say/afconvert (%v)", err)
	}
	const skip = SampleRate // the first second is quiet
	if len(raw) < skip+PatchSamples {
		t.Fatal("fixture too short")
	}
	return raw[skip : skip+PatchSamples]
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
