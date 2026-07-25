// Package audio holds the capture pipeline and its lock-free ring buffer.
//
// Port of the Python src/livecap/audio.py. The buffer keeps filling regardless
// of connection state, so audio survives a reconnect: whatever accumulated
// during the gap is pushed to the new session.
package audio

import (
	"errors"
	"log/slog"
	"sync/atomic"
)

const (
	TargetRate     = 16000
	BytesPerSample = 2
	ChunkMS        = 100
	// ChunkBytes is 100 ms at 16 kHz mono 16-bit: 3200 bytes.
	ChunkBytes  = TargetRate * BytesPerSample * ChunkMS / 1000
	RingSeconds = 30
	CallbackMS  = 20
)

// ErrBadSize is returned when a read is outside 1..capacity.
var ErrBadSize = errors.New("n must be in 1..capacity")

// RingBuffer is a single-producer (audio callback) / single-consumer buffer.
//
// Only the producer advances the write position and only the consumer advances
// the read position. Both are monotonic counters accessed atomically, so no
// lock is taken on the audio callback path.
//
// When full, the producer overwrites rather than blocking, and the consumer
// detects on its next read that it was lapped and skips the oldest data.
type RingBuffer struct {
	cap      int
	buf      []byte
	writePos atomic.Int64 // producer only, cumulative bytes
	readPos  atomic.Int64 // consumer only, cumulative bytes
	dropped  atomic.Int64 // consumer only
}

// NewRingBuffer panics on a non-positive capacity, which is always a programming error.
func NewRingBuffer(capacityBytes int) *RingBuffer {
	if capacityBytes <= 0 {
		panic("capacityBytes must be positive")
	}
	return &RingBuffer{cap: capacityBytes, buf: make([]byte, capacityBytes)}
}

// Capacity reports the buffer size in bytes.
func (r *RingBuffer) Capacity() int { return r.cap }

// DroppedBytes reports the total discarded to overflow.
func (r *RingBuffer) DroppedBytes() int64 { return r.dropped.Load() }

// Available reports readable bytes, before accounting for overwritten data.
func (r *RingBuffer) Available() int {
	n := r.writePos.Load() - r.readPos.Load()
	if n > int64(r.cap) {
		return r.cap
	}
	return int(n)
}

// Write is producer-only and never blocks.
func (r *RingBuffer) Write(data []byte) {
	n := len(data)
	if n == 0 {
		return
	}
	if n >= r.cap {
		// A single write larger than the buffer — keep only the last cap bytes.
		data = data[n-r.cap:]
		n = r.cap
	}
	w := r.writePos.Load()
	start := int(w % int64(r.cap))
	if start+n <= r.cap {
		copy(r.buf[start:], data)
	} else {
		split := r.cap - start
		copy(r.buf[start:], data[:split])
		copy(r.buf, data[split:])
	}
	r.writePos.Store(w + int64(n))
}

// Read is consumer-only. It returns nil until n bytes are available.
func (r *RingBuffer) Read(n int) ([]byte, error) {
	if n <= 0 || n > r.cap {
		return nil, ErrBadSize
	}
	for {
		// If we were lapped, drop the oldest data and catch up.
		rp := r.readPos.Load()
		if lag := r.writePos.Load() - rp; lag > int64(r.cap) {
			skipped := lag - int64(r.cap)
			rp += skipped
			r.readPos.Store(rp)
			r.dropped.Add(skipped)
			slog.Warn("ring buffer overflow", "droppedBytes", skipped)
		}
		if r.writePos.Load()-rp < int64(n) {
			return nil, nil
		}

		out := make([]byte, n)
		start := int(rp % int64(r.cap))
		if start+n <= r.cap {
			copy(out, r.buf[start:start+n])
		} else {
			split := r.cap - start
			copy(out, r.buf[start:])
			copy(out[split:], r.buf[:n-split])
		}

		// If the producer lapped us mid-copy, that data is corrupt — retry.
		if r.writePos.Load()-rp > int64(r.cap) {
			continue
		}
		r.readPos.Store(rp + int64(n))
		return out, nil
	}
}

// Clear is consumer-only. It discards everything buffered.
func (r *RingBuffer) Clear() { r.readPos.Store(r.writePos.Load()) }

// FloatToPCM16 converts float32 samples in [-1, 1] to 16-bit signed PCM,
// little-endian, clipping out-of-range input.
func FloatToPCM16(x []float32, out []byte) []byte {
	if cap(out) < len(x)*2 {
		out = make([]byte, len(x)*2)
	}
	out = out[:len(x)*2]
	for i, v := range x {
		s := v * 32767.0
		switch {
		case s > 32767:
			s = 32767
		case s < -32768:
			s = -32768
		}
		n := int16(s)
		out[i*2] = byte(uint16(n))
		out[i*2+1] = byte(uint16(n) >> 8)
	}
	return out
}
