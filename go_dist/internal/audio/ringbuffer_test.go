package audio

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Mirrors tests/test_ring_buffer.py.

func mustRead(t *testing.T, r *RingBuffer, n int) []byte {
	t.Helper()
	b, err := r.Read(n)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return b
}

func TestRoundtripPreservesBytes(t *testing.T) {
	r := NewRingBuffer(1000)
	r.Write([]byte("abcdefghij"))
	if got := mustRead(t, r, 4); !bytes.Equal(got, []byte("abcd")) {
		t.Fatalf("got %q", got)
	}
	if got := mustRead(t, r, 6); !bytes.Equal(got, []byte("efghij")) {
		t.Fatalf("got %q", got)
	}
}

func TestReadReturnsNilWhenShort(t *testing.T) {
	r := NewRingBuffer(1000)
	r.Write([]byte("abc"))
	if got := mustRead(t, r, 4); got != nil {
		t.Fatalf("got %q, want nil", got)
	}
	r.Write([]byte("d"))
	if got := mustRead(t, r, 4); !bytes.Equal(got, []byte("abcd")) {
		t.Fatalf("got %q", got)
	}
}

func TestWraparoundIsContiguous(t *testing.T) {
	r := NewRingBuffer(10)
	r.Write([]byte("0123456789"))
	if got := mustRead(t, r, 6); !bytes.Equal(got, []byte("012345")) {
		t.Fatalf("got %q", got)
	}
	r.Write([]byte("ABCDEF")) // wraps past the end of the backing array
	if got := mustRead(t, r, 10); !bytes.Equal(got, []byte("6789ABCDEF")) {
		t.Fatalf("got %q", got)
	}
}

func TestOverflowDropsOldestAndCounts(t *testing.T) {
	r := NewRingBuffer(10)
	r.Write([]byte("0123456789"))
	r.Write([]byte("ABCDE")) // producer laps the consumer by 5 bytes
	if got := mustRead(t, r, 10); !bytes.Equal(got, []byte("56789ABCDE")) {
		t.Fatalf("got %q", got)
	}
	if r.DroppedBytes() != 5 {
		t.Fatalf("dropped = %d", r.DroppedBytes())
	}
}

func TestWriteLargerThanCapacityKeepsTail(t *testing.T) {
	r := NewRingBuffer(8)
	r.Write([]byte("0123456789ABCDEF"))
	if got := mustRead(t, r, 8); !bytes.Equal(got, []byte("89ABCDEF")) {
		t.Fatalf("got %q", got)
	}
}

func TestClearDiscardsPending(t *testing.T) {
	r := NewRingBuffer(100)
	r.Write([]byte("abcdef"))
	r.Clear()
	if got := mustRead(t, r, 1); got != nil {
		t.Fatalf("got %q, want nil", got)
	}
	r.Write([]byte("xy"))
	if got := mustRead(t, r, 2); !bytes.Equal(got, []byte("xy")) {
		t.Fatalf("got %q", got)
	}
}

func TestAvailableIsCappedAtCapacity(t *testing.T) {
	r := NewRingBuffer(10)
	r.Write([]byte("0123456789ABCDE"))
	if r.Available() != 10 {
		t.Fatalf("available = %d", r.Available())
	}
}

func TestRejectsBadSizes(t *testing.T) {
	r := NewRingBuffer(10)
	if _, err := r.Read(0); err == nil {
		t.Fatal("expected an error for 0")
	}
	if _, err := r.Read(11); err == nil {
		t.Fatal("expected an error past capacity")
	}
}

func TestEmptyWriteIsNoop(t *testing.T) {
	r := NewRingBuffer(10)
	r.Write(nil)
	if r.Available() != 0 {
		t.Fatalf("available = %d", r.Available())
	}
}

func TestSustainedProducerConsumerCycle(t *testing.T) {
	// Consuming in 3200-byte (100 ms) units must stay aligned over a long run.
	r := NewRingBuffer(TargetRate * BytesPerSample * RingSeconds)
	payload := bytes.Repeat([]byte("0123456789ABCDEF"), 80) // 1280 bytes
	var produced, consumed []byte
	for i := 0; i < 500; i++ {
		r.Write(payload)
		produced = append(produced, payload...)
		for {
			chunk := mustRead(t, r, ChunkBytes)
			if chunk == nil {
				break
			}
			consumed = append(consumed, chunk...)
		}
	}
	if r.DroppedBytes() != 0 {
		t.Fatalf("dropped = %d", r.DroppedBytes())
	}
	if !bytes.Equal(consumed, produced[:len(consumed)]) {
		t.Fatal("consumed bytes diverged from produced")
	}
	if len(produced)-len(consumed) >= ChunkBytes {
		t.Fatalf("left over %d bytes", len(produced)-len(consumed))
	}
}

func TestChunkConstantMatches100msAt16k(t *testing.T) {
	if ChunkBytes != 3200 {
		t.Fatalf("ChunkBytes = %d", ChunkBytes)
	}
}

func TestPCM16EncodingIsLittleEndianInt16(t *testing.T) {
	out := FloatToPCM16([]float32{0, 1.0, -1.0}, nil)
	if len(out) != 6 {
		t.Fatalf("len = %d", len(out))
	}
	want := []int16{0, 32767, -32767}
	for i, w := range want {
		got := int16(binary.LittleEndian.Uint16(out[i*2:]))
		if got != w {
			t.Fatalf("sample %d = %d, want %d", i, got, w)
		}
	}
}

func TestPCM16ClipsOutOfRangeInput(t *testing.T) {
	out := FloatToPCM16([]float32{2.0, -2.0}, nil)
	if got := int16(binary.LittleEndian.Uint16(out[0:])); got != 32767 {
		t.Fatalf("got %d", got)
	}
	if got := int16(binary.LittleEndian.Uint16(out[2:])); got != -32768 {
		t.Fatalf("got %d", got)
	}
}
