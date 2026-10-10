package audio

import (
	"encoding/binary"
	"math"
	"testing"
)

// samples encodes 16-bit samples as the ring stores them.
func samples(vs ...int16) []byte {
	out := make([]byte, 0, len(vs)*2)
	for _, v := range vs {
		out = binary.LittleEndian.AppendUint16(out, uint16(v))
	}
	return out
}

func fill(n int, v int16) []int16 {
	s := make([]int16, n)
	for i := range s {
		s[i] = v
	}
	return s
}

// A silent head over a long queue is skipped; that is the whole mechanism.
func TestASilentHeadIsSkippedWhenTheQueueIsLong(t *testing.T) {
	r := newPlayRing(64)
	r.Write(samples(fill(4, 0)...))   // 8 bytes of silence at the head
	r.Write(samples(fill(8, 900)...)) // 16 bytes of speech behind it
	if got := r.skipSilentHead(8, 4, 33); got != 8 {
		t.Fatalf("skipped %d bytes, want the 8-byte silent head", got)
	}
	if r.Available() != 16 {
		t.Errorf("Available = %d after trimming, want the 16 bytes of speech", r.Available())
	}
	if r.Trimmed() != 8 {
		t.Errorf("Trimmed = %d, want 8", r.Trimmed())
	}
}

// The defect this exists to remove is lost speech, so trimming must never
// take any. One sample above the threshold keeps the whole chunk.
func TestSpeechIsNeverSkipped(t *testing.T) {
	r := newPlayRing(64)
	chunk := fill(4, 0)
	chunk[2] = 34 // one sample just over the line
	r.Write(samples(chunk...))
	r.Write(samples(fill(8, 0)...))
	if got := r.skipSilentHead(8, 0, 33); got != 0 {
		t.Errorf("skipped %d bytes of a chunk with speech in it", got)
	}
	// Negative excursions count too — silence is about magnitude.
	r2 := newPlayRing(64)
	chunk[2] = -34
	r2.Write(samples(chunk...))
	r2.Write(samples(fill(8, 0)...))
	if got := r2.skipSilentHead(8, 0, 33); got != 0 {
		t.Errorf("skipped %d bytes of a chunk with a negative peak", got)
	}
}

// Only the oldest chunk is ever looked at. Silence queued *behind* speech
// must wait its turn, or the order of what is heard would change.
func TestOnlyTheHeadIsEverTrimmed(t *testing.T) {
	r := newPlayRing(64)
	r.Write(samples(fill(4, 5000)...)) // speech at the head
	r.Write(samples(fill(4, 0)...))    // silence behind it
	r.Write(samples(fill(8, 0)...))
	if got := r.skipSilentHead(8, 0, 33); got != 0 {
		t.Errorf("trimmed %d bytes past speech at the head", got)
	}
}

// A short queue is a cushion against the network pausing; trimming it would
// turn a covered gap into an audible one.
func TestTrimmingNeverLeavesLessThanTheCushion(t *testing.T) {
	r := newPlayRing(64)
	r.Write(samples(fill(8, 0)...)) // 16 bytes, all silent
	if got := r.skipSilentHead(8, 9, 33); got != 0 {
		t.Errorf("trimmed to %d bytes against a cushion of 9", r.Available())
	}
	if got := r.skipSilentHead(8, 8, 33); got != 8 {
		t.Errorf("did not trim when exactly the cushion would remain: skipped %d", got)
	}
}

// The ring wraps; the head chunk can straddle the end of the buffer.
func TestTrimmingReadsAcrossTheWrap(t *testing.T) {
	r := newPlayRing(16)
	r.Write(samples(fill(6, 7000)...)) // 12 bytes
	r.Read(make([]byte, 12))           // head now at 12 of 16
	r.Write(samples(fill(4, 0)...))    // 8 silent bytes: 4 before the wrap, 4 after
	r.Write(samples(fill(2, 7000)...)) // speech behind
	if got := r.skipSilentHead(8, 0, 33); got != 8 {
		t.Fatalf("skipped %d across the wrap, want 8", got)
	}
	dst := make([]byte, 4)
	r.Read(dst)
	if v := int16(binary.LittleEndian.Uint16(dst)); v != 7000 {
		t.Errorf("after trimming, the next sample is %d, want the speech", v)
	}
}

// A chunk that is not a whole number of samples cannot be judged.
func TestAnOddChunkIsRefused(t *testing.T) {
	r := newPlayRing(64)
	r.Write(samples(fill(16, 0)...))
	if got := r.skipSilentHead(3, 0, 33); got != 0 {
		t.Errorf("skipped %d bytes on an odd chunk size", got)
	}
}

// --- through the device callback ------------------------------------------

func trimmingPlayer(trimBytes int) (*Player, *playState) {
	p := NewPlayer("", TranslatedRate)
	p.devRate, p.devChan = TranslatedRate, 1
	st := &playState{
		ring:        newPlayRing(4096),
		prebuffer:   0,
		blockBytes:  BytesPerSample,
		trimAbove:   trimBytes,
		silencePeak: 32768 * math.Pow(10, PlaySilenceDBFS/20),
	}
	p.active.Store(st)
	p.primed.Store(true)
	return p, st
}

// Write applies the volume before the ring, so the same word tail sits lower
// at a lower volume. A fixed threshold would start skipping quiet speech the
// moment the slider moved down; the threshold has to move with it.
func TestTheSilenceThresholdFollowsTheVolume(t *testing.T) {
	// A word tail at -44 dBFS (about 200), stored at volume 0.1 as 20.
	tail := fill(32, 20)
	behind := fill(512, 20)

	p, st := trimmingPlayer(2)
	p.SetVolume(0.1)
	st.ring.Write(samples(tail...))
	st.ring.Write(samples(behind...))
	p.onPlayback(make([]byte, 64), nil, 32)
	if st.ring.Trimmed() != 0 {
		t.Errorf("at volume 0.1 a -44 dBFS word tail was skipped as silence")
	}

	// The same stored value at full volume really is near silence (-64 dBFS).
	p, st = trimmingPlayer(2)
	p.SetVolume(1.0)
	st.ring.Write(samples(tail...))
	st.ring.Write(samples(behind...))
	p.onPlayback(make([]byte, 64), nil, 32)
	if st.ring.Trimmed() == 0 {
		t.Errorf("at full volume a -64 dBFS chunk was not treated as silence")
	}
}

// Zero switches it off: the player behaves exactly as it did before.
func TestAZeroThresholdTrimsNothing(t *testing.T) {
	p, st := trimmingPlayer(0)
	st.ring.Write(samples(fill(1024, 0)...))
	for range 8 {
		p.onPlayback(make([]byte, 64), nil, 32)
	}
	if st.ring.Trimmed() != 0 {
		t.Errorf("trimmed %d bytes with trimming off", st.ring.Trimmed())
	}
}

// The measured failure, synthesized: speech with ordinary pauses, delivered
// 2% faster than real time, with a new session's opening burst every minute.
// Without trimming the ring fills and overflow cuts speech; with it, nothing
// is lost. The second half is what proves the scenario actually stresses the
// player — a test that passes with trimming off would prove nothing.
func TestADriftingStreamWithBurstsLosesNoSpeech(t *testing.T) {
	const bps = TranslatedRate * BytesPerSample
	// 400 ms of speech then 100 ms of exact silence, repeated: a pause share
	// close to the 17% of exact-zero chunks measured in real model audio.
	var pcm []byte
	for len(pcm) < bps*400 {
		pcm = append(pcm, samples(fill(TranslatedRate*4/10, 6000)...)...)
		pcm = append(pcm, samples(fill(TranslatedRate/10, 0)...)...)
	}
	audio := map[int][]byte{1: pcm}

	blob := bps / 4 // 250 ms, the measured blob size
	var arrivals []arrival
	at := 0.0
	for sent := 0; sent+blob <= len(pcm); sent += blob {
		arrivals = append(arrivals, arrival{at, 1, blob})
		gap := 0.25 / 1.02
		// Every 60 s, four blobs in 0.4 s: the opening burst of a new session.
		if int(at)%60 == 0 && int(at) > 0 && math.Mod(at, 60) < 0.4 {
			gap = 0.1
		}
		at += gap
	}

	if r := replay(arrivals, audio, 0); r.cuts == 0 {
		t.Fatal("setup: with trimming off the scenario never overflowed — it tests nothing")
	}
	r := replay(arrivals, audio, PlayTrimAboveMS)
	if r.cuts != 0 || r.lost != 0 {
		t.Errorf("with trimming on: %d cuts, %.2f s of speech lost", r.cuts, r.lost)
	}
	if r.maxQueue > float64(PlayRingMS)/1000 {
		t.Errorf("queue reached %.2f s, past the ring", r.maxQueue)
	}
}
