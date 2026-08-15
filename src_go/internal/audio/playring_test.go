package audio

import (
	"bytes"
	"sync"
	"testing"
)

func TestPlayRingReadsBackWhatWasWritten(t *testing.T) {
	r := newPlayRing(16)
	r.Write([]byte("abcdefgh"))
	if got := r.Available(); got != 8 {
		t.Errorf("Available = %d, want 8", got)
	}
	dst := make([]byte, 8)
	if n := r.Read(dst); n != 8 {
		t.Errorf("Read returned %d, want 8", n)
	}
	if string(dst) != "abcdefgh" {
		t.Errorf("Read = %q", dst)
	}
	if r.Available() != 0 {
		t.Error("bytes remain after reading everything")
	}
}

// The whole reason this is not Ring: a device callback must be given a full
// block, so a short read is padded with silence rather than refused.
func TestPlayRingPadsAShortReadWithSilence(t *testing.T) {
	r := newPlayRing(16)
	r.Write([]byte{1, 2, 3})

	dst := []byte{9, 9, 9, 9, 9, 9}
	n := r.Read(dst)
	if n != 3 {
		t.Errorf("Read returned %d bytes of real audio, want 3", n)
	}
	if !bytes.Equal(dst, []byte{1, 2, 3, 0, 0, 0}) {
		t.Errorf("Read = %v, want the audio then silence", dst)
	}

	starved, starvedBytes, _ := r.Stats()
	if starved != 1 {
		t.Errorf("starved = %d, want 1", starved)
	}
	if starvedBytes != 3 {
		t.Errorf("starvedBytes = %d, want 3", starvedBytes)
	}
}

// An empty ring is the common case at the very start and after a session ends.
// It must be silent, not stale.
func TestPlayRingReadOfAnEmptyRingIsSilence(t *testing.T) {
	r := newPlayRing(16)
	dst := []byte{7, 7, 7, 7}
	if n := r.Read(dst); n != 0 {
		t.Errorf("Read returned %d, want 0", n)
	}
	if !bytes.Equal(dst, []byte{0, 0, 0, 0}) {
		t.Errorf("Read = %v, want silence", dst)
	}
}

// Overflow drops the oldest audio. In live translation the newest speech is
// the useful speech, so a listener who falls behind rejoins at the present
// rather than replaying the past.
func TestPlayRingOverflowDropsTheOldest(t *testing.T) {
	r := newPlayRing(8)
	r.Write([]byte("aaaabbbb"))
	r.Write([]byte("cccc"))

	if got := r.Available(); got != 8 {
		t.Errorf("Available = %d, want the capacity 8", got)
	}
	dst := make([]byte, 8)
	r.Read(dst)
	if string(dst) != "bbbbcccc" {
		t.Errorf("Read = %q, want the newest 8 bytes", dst)
	}
	if _, _, dropped := r.Stats(); dropped != 4 {
		t.Errorf("dropped = %d, want 4", dropped)
	}
}

// A single write larger than the buffer keeps its tail, not its head.
func TestPlayRingWriteLargerThanCapacityKeepsTheTail(t *testing.T) {
	r := newPlayRing(4)
	r.Write([]byte("abcdefgh"))

	dst := make([]byte, 4)
	r.Read(dst)
	if string(dst) != "efgh" {
		t.Errorf("Read = %q, want the last 4 bytes", dst)
	}
}

// Reads and writes straddle the wrap point, which is where an off-by-one in
// the split copy would live.
func TestPlayRingWrapsCorrectly(t *testing.T) {
	r := newPlayRing(8)
	dst := make([]byte, 3)

	// Walk the write position around the buffer twice.
	for i := range 6 {
		want := []byte{byte(i), byte(i), byte(i)}
		r.Write(want)
		if n := r.Read(dst); n != 3 {
			t.Fatalf("round %d: Read returned %d", i, n)
		}
		if !bytes.Equal(dst, want) {
			t.Fatalf("round %d: Read = %v, want %v", i, dst, want)
		}
	}
	if starved, _, dropped := r.Stats(); starved != 0 || dropped != 0 {
		t.Errorf("starved = %d, dropped = %d, want neither", starved, dropped)
	}
}

func TestPlayRingClearDiscardsEverything(t *testing.T) {
	r := newPlayRing(16)
	r.Write([]byte("abcdefgh"))
	r.Clear()
	if got := r.Available(); got != 0 {
		t.Errorf("Available = %d after Clear, want 0", got)
	}
	dst := make([]byte, 4)
	r.Read(dst)
	if !bytes.Equal(dst, []byte{0, 0, 0, 0}) {
		t.Errorf("Read = %v after Clear, want silence", dst)
	}
}

// The device callback contract, pinned. Read runs on miniaudio's thread, which
// allocates nothing — this is the same rule the capture callback keeps, and the
// only way to keep it honest is to measure.
func TestPlayRingReadDoesNotAllocate(t *testing.T) {
	r := newPlayRing(4096)
	dst := make([]byte, 512)
	block := make([]byte, 512)

	// Both branches matter: a read that is fully served, and one that has to
	// pad. Padding is the path that would reach for a fresh slice.
	allocs := testing.AllocsPerRun(200, func() {
		r.Write(block)
		r.Read(dst)
		r.Read(dst)
	})
	if allocs > 0 {
		t.Errorf("Read allocated %.1f times per call on the device callback thread", allocs)
	}
}

func TestPlayRingSurvivesConcurrentUse(t *testing.T) {
	r := newPlayRing(1024)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		block := make([]byte, 128)
		for i := range 2000 {
			block[0] = byte(i)
			r.Write(block)
		}
	}()
	go func() {
		defer wg.Done()
		dst := make([]byte, 96)
		for range 2000 {
			r.Read(dst)
		}
	}()
	wg.Wait()

	// Nothing to assert beyond "the race detector stayed quiet"; the counters
	// are read to prove the lock is still coherent afterwards.
	if r.Available() > r.Capacity() {
		t.Errorf("Available = %d exceeds capacity %d", r.Available(), r.Capacity())
	}
}
