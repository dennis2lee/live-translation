package audio

import (
	"errors"
	"log/slog"
	"sync"
)

// Ring is a fixed-size byte buffer between the device callback (one producer)
// and the sender goroutine (one consumer).
//
// When full, the producer overwrites the oldest audio rather than blocking, and
// the consumer skips what it lost. That is the right trade for live audio: a
// stalled sender must never stall the device callback, and after a stall the
// newest audio is the useful audio.
//
// # Why this holds a mutex, where the Python original is lock-free
//
// caption_buffer's sibling in the Python build relies on the GIL to make the
// cumulative int positions atomic, and validates after the fact that the
// producer did not lap the consumer mid-copy. That pattern is a seqlock, and a
// seqlock *is* a data race by definition — the producer writes bytes the
// consumer may be reading. Python gets away with it; Go's race detector
// correctly reports it, and `go test -race` clean is a requirement here.
//
// So the copies are serialised. The cost is small and bounded: the producer
// runs about 50 times a second and the consumer 10, each holding the lock only
// for a memcpy of a few kilobytes, well inside a 20 ms callback budget. No
// allocation happens under the lock.
type Ring struct {
	cap int

	mu       sync.Mutex
	buf      []byte
	writePos int64 // cumulative bytes written
	readPos  int64 // cumulative bytes handed to the consumer
	dropped  int64
}

// ErrRingSize is returned for a read that can never be satisfied.
var ErrRingSize = errors.New("read size must be in 1..capacity")

// NewRing builds a ring of the given size in bytes.
func NewRing(capacityBytes int) *Ring {
	if capacityBytes <= 0 {
		panic("audio: ring capacity must be positive")
	}
	return &Ring{cap: capacityBytes, buf: make([]byte, capacityBytes)}
}

// Capacity is the ring size in bytes.
func (r *Ring) Capacity() int { return r.cap }

// DroppedBytes is the total discarded to overflow.
func (r *Ring) DroppedBytes() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// Available is the readable bytes.
func (r *Ring) Available() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.availableLocked()
}

func (r *Ring) availableLocked() int {
	n := r.writePos - r.readPos
	if n > int64(r.cap) {
		return r.cap
	}
	if n < 0 {
		return 0
	}
	return int(n)
}

// Write is producer-only and never blocks on the consumer.
func (r *Ring) Write(data []byte) {
	n := len(data)
	if n == 0 {
		return
	}
	if n >= r.cap {
		// A single write larger than the buffer — keep only the last cap bytes.
		data = data[n-r.cap:]
		n = r.cap
	}

	r.mu.Lock()
	start := int(r.writePos % int64(r.cap))
	if start+n <= r.cap {
		copy(r.buf[start:], data)
	} else {
		split := r.cap - start
		copy(r.buf[start:], data[:split])
		copy(r.buf, data[split:])
	}
	r.writePos += int64(n)

	// Anything the consumer has not taken and we have now overwritten is gone.
	if lag := r.writePos - r.readPos; lag > int64(r.cap) {
		skipped := lag - int64(r.cap)
		r.readPos += skipped
		r.dropped += skipped
		r.mu.Unlock()
		slog.Warn("ring buffer overflow", "dropped_bytes", skipped)
		return
	}
	r.mu.Unlock()
}

// Read is consumer-only. It returns nil, nil until n bytes are available.
func (r *Ring) Read(n int) ([]byte, error) {
	if n <= 0 || n > r.cap {
		return nil, ErrRingSize
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.writePos-r.readPos < int64(n) {
		return nil, nil
	}
	out := make([]byte, n)
	start := int(r.readPos % int64(r.cap))
	if start+n <= r.cap {
		copy(out, r.buf[start:start+n])
	} else {
		split := r.cap - start
		copy(out, r.buf[start:])
		copy(out[split:], r.buf[:n-split])
	}
	r.readPos += int64(n)
	return out, nil
}

// Clear is consumer-only. It discards everything buffered.
func (r *Ring) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readPos = r.writePos
}
