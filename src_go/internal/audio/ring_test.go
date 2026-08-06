package audio

import (
	"bytes"
	"sync"
	"testing"
)

func TestRingReadsBackWhatWasWritten(t *testing.T) {
	r := NewRing(16)
	r.Write([]byte("abcdefgh"))
	if got := r.Available(); got != 8 {
		t.Errorf("Available = %d", got)
	}
	got, err := r.Read(8)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abcdefgh" {
		t.Errorf("Read = %q", got)
	}
	if r.Available() != 0 {
		t.Error("bytes remain after reading everything")
	}
}

// A read larger than what is buffered waits rather than returning a short read.
func TestRingReadWaitsForAWholeChunk(t *testing.T) {
	r := NewRing(16)
	r.Write([]byte("abc"))
	got, err := r.Read(8)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("Read returned %q before enough was buffered", got)
	}
}

// Writes that straddle the end of the buffer must come back in one piece.
func TestRingWrapsCorrectly(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("12345"))
	if _, err := r.Read(5); err != nil {
		t.Fatal(err)
	}
	r.Write([]byte("abcdef")) // starts at 5, wraps at 8
	got, err := r.Read(6)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abcdef" {
		t.Errorf("Read across the wrap = %q", got)
	}
}

// A stalled consumer must never stall the producer: the oldest audio is dropped
// and the reader catches up.
func TestRingOverflowDropsTheOldest(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("11111111"))
	r.Write([]byte("22222222"))
	r.Write([]byte("33333333"))

	got, err := r.Read(8)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "33333333" {
		t.Errorf("Read = %q, want the newest block", got)
	}
	if r.DroppedBytes() != 16 {
		t.Errorf("DroppedBytes = %d, want 16", r.DroppedBytes())
	}
}

// A single write bigger than the ring keeps its tail, not its head.
func TestRingWriteLargerThanCapacity(t *testing.T) {
	r := NewRing(4)
	r.Write([]byte("abcdefgh"))
	got, err := r.Read(4)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "efgh" {
		t.Errorf("Read = %q, want the tail of the write", got)
	}
}

func TestRingClearDiscardsEverything(t *testing.T) {
	r := NewRing(16)
	r.Write([]byte("abcdefgh"))
	r.Clear()
	if r.Available() != 0 {
		t.Errorf("Available = %d after Clear", r.Available())
	}
	got, _ := r.Read(1)
	if got != nil {
		t.Errorf("Read after Clear = %q", got)
	}
}

func TestRingRejectsImpossibleReads(t *testing.T) {
	r := NewRing(8)
	for _, n := range []int{0, -1, 9} {
		if _, err := r.Read(n); err == nil {
			t.Errorf("Read(%d) was accepted", n)
		}
	}
}

// The producer is a device callback and the consumer a sender goroutine, so the
// race detector has to be happy with them running at once.
func TestRingIsSafeWithOneProducerAndOneConsumer(t *testing.T) {
	const chunk = 320
	r := NewRing(chunk * 8)

	var wg sync.WaitGroup
	wg.Add(2)

	block := bytes.Repeat([]byte{7}, chunk)
	go func() { // producer
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			r.Write(block)
		}
	}()

	go func() { // consumer
		defer wg.Done()
		read := 0
		for read < 2000*chunk {
			got, err := r.Read(chunk)
			if err != nil {
				t.Error(err)
				return
			}
			if got == nil {
				continue
			}
			for _, b := range got {
				if b != 7 {
					t.Errorf("torn read: byte %d", b)
					return
				}
			}
			read += chunk
			// Dropped bytes mean the rest will never arrive; stop waiting.
			if r.DroppedBytes() > 0 {
				return
			}
		}
	}()

	wg.Wait()
}

func TestChunkBytesIsOneHundredMilliseconds(t *testing.T) {
	if ChunkBytes != 3200 {
		t.Errorf("ChunkBytes = %d, want 3200 — 16 kHz, 16-bit, mono, 100 ms", ChunkBytes)
	}
}
