package audio

import "sync"

// playRing is the jitter buffer between the network goroutine, which delivers
// translated speech in bursts, and the playback device callback, which asks for
// a fixed block every few milliseconds.
//
// It is Ring read backwards, and it differs in the two ways that matter.
//
// # A starved player emits silence
//
// Ring's consumer can simply not send. A player has to put *something* in the
// device buffer, and the choice is between silence and whatever was there
// before. Silence is inaudible and correct; leaving the previous block in place
// repeats it fifty times a second, which is a buzz. So Read always fills its
// destination, zero-padding whatever it could not supply.
//
// # Read cannot allocate
//
// Ring.Read returns a fresh slice, which is fine on the sender's goroutine.
// This one is called from the device callback, which allocates nothing and
// takes no lock beyond this one, so the caller supplies the destination.
//
// Overflow behaves the same as Ring: the oldest audio goes, never the newest.
// In live translation the newest speech is the useful speech.
type playRing struct {
	cap int

	mu       sync.Mutex
	buf      []byte
	writePos int64 // cumulative bytes written
	readPos  int64 // cumulative bytes handed to the device

	dropped int64
	// starved counts callbacks that found nothing to play, and starvedBytes is
	// how much silence went out in their place. Counted rather than logged: a
	// momentary gap and a permanent deficit are indistinguishable from inside
	// one callback, and slog has no business on this thread anyway.
	starved      int64
	starvedBytes int64
	// trimmed is silence skipped to keep the queue short. See skipSilentHead.
	trimmed int64
}

// newPlayRing builds a ring of the given size in bytes.
func newPlayRing(capacityBytes int) *playRing {
	if capacityBytes <= 0 {
		panic("audio: playback ring capacity must be positive")
	}
	return &playRing{cap: capacityBytes, buf: make([]byte, capacityBytes)}
}

// Capacity is the ring size in bytes.
func (r *playRing) Capacity() int { return r.cap }

// Available is the bytes waiting to be played.
func (r *playRing) Available() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.writePos - r.readPos
	if n > int64(r.cap) {
		return r.cap
	}
	if n < 0 {
		return 0
	}
	return int(n)
}

// Write is producer-only and never blocks on the device.
func (r *playRing) Write(data []byte) {
	n := len(data)
	if n == 0 {
		return
	}
	if n >= r.cap {
		// One write larger than the whole buffer: keep only the newest tail.
		data = data[n-r.cap:]
		n = r.cap
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	start := int(r.writePos % int64(r.cap))
	if start+n <= r.cap {
		copy(r.buf[start:], data)
	} else {
		split := r.cap - start
		copy(r.buf[start:], data[:split])
		copy(r.buf, data[split:])
	}
	r.writePos += int64(n)

	// Anything unplayed that this write has just overwritten is gone.
	if lag := r.writePos - r.readPos; lag > int64(r.cap) {
		skipped := lag - int64(r.cap)
		r.readPos += skipped
		r.dropped += skipped
	}
}

// Read fills dst with buffered audio, padding with silence when there is not
// enough, and returns how many bytes of real audio it supplied.
//
// Consumer-only, and called from the device callback: it allocates nothing.
func (r *playRing) Read(dst []byte) int {
	if len(dst) == 0 {
		return 0
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	n := int(min(r.writePos-r.readPos, int64(len(dst))))
	if n < 0 {
		n = 0
	}
	if n > 0 {
		start := int(r.readPos % int64(r.cap))
		if start+n <= r.cap {
			copy(dst, r.buf[start:start+n])
		} else {
			split := r.cap - start
			copy(dst, r.buf[start:])
			copy(dst[split:], r.buf[:n-split])
		}
		r.readPos += int64(n)
	}
	if n < len(dst) {
		clear(dst[n:])
		r.starved++
		r.starvedBytes += int64(len(dst) - n)
	}
	return n
}

// Clear discards everything buffered. Consumer-side.
func (r *playRing) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readPos = r.writePos
}

// Stats reports what the run cost: callbacks that had to invent silence, the
// silence they emitted, and audio dropped because the device fell behind.
func (r *playRing) Stats() (starved, starvedBytes, dropped int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starved, r.starvedBytes, r.dropped
}

// Trimmed is how much silence skipSilentHead has discarded, in bytes.
func (r *playRing) Trimmed() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trimmed
}

// skipSilentHead discards the oldest queued chunk if it is silence and the
// queue is longer than keep bytes. It reports how many bytes it skipped.
//
// This is what keeps the queue from ever reaching the cap. The model sends
// about 1% more audio than wall-clock time (378 s in 374 s, measured over six
// minutes of continuous speech), so a player running at exactly real time
// falls slowly behind; and a new session's first audio arrives in a burst,
// about a second of it in 0.4 s. Once the slow drift has filled the ring, the
// burst overflows it, and overflow discards the *oldest* audio — the last
// words of the session being replaced, cut out in three or four jumps within
// half a second. Measured: 16 cuts, 1.81 s of speech, in six minutes.
//
// Skipping silence instead loses nothing anyone hears. Only the head is ever
// looked at, so no speech is reordered or skipped past, and it stops at the
// first chunk with anything in it. One chunk per call: the caller plays one
// chunk for each one it may skip, so a pause plays at no more than double
// speed rather than vanishing.
//
// samplePeak is compared against every 16-bit sample in the chunk, whatever
// the channel count — interleaved silence is silent on every channel.
func (r *playRing) skipSilentHead(chunk, keep int, samplePeak int32) int {
	if chunk <= 0 || chunk%2 != 0 {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writePos-r.readPos-int64(chunk) < int64(keep) {
		return 0
	}
	for i := 0; i < chunk; i += 2 {
		lo := r.buf[int((r.readPos+int64(i))%int64(r.cap))]
		hi := r.buf[int((r.readPos+int64(i+1))%int64(r.cap))]
		v := int32(int16(uint16(lo) | uint16(hi)<<8))
		if v > samplePeak || v < -samplePeak {
			return 0
		}
	}
	r.readPos += int64(chunk)
	r.trimmed += int64(chunk)
	return chunk
}
