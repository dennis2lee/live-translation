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
