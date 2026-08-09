package audio

import (
	"sync"
	"testing"
	"unsafe"

	"github.com/dennis2lee/live-translation/src_go/internal/resample"
)

// Retiring the resampler must not race with the device callback reading it.
//
// Stop has to clear the resampler *before* device.Stop(), because that is what
// waits for callbacks to finish — so the write and the callback's read
// genuinely overlap. The callback cannot take a lock (no locks on the audio
// thread), which leaves the field itself to carry the synchronisation.
//
// No device is opened here: onAudio is called directly, which is exactly the
// concurrency the race detector needs and the only way to reach it, since
// `go test -race` never drives a real device callback. Reverting the field to a
// plain resample.Stream makes this test fail under -race.
func TestRetiringTheResamplerDoesNotRaceWithTheCallback(t *testing.T) {
	c := NewCapture("", 1)
	c.channels = 1

	stream, err := resample.NewStream(48_000, TargetRate)
	if err != nil {
		t.Fatalf("could not build a resampler: %v", err)
	}
	c.resampler.Store(&stream)

	// One 20 ms block at 48 kHz mono, as interleaved float32 — the shape
	// miniaudio hands in.
	const frames = 48_000 * CallbackMS / 1000
	samples := make([]float32, frames)
	for i := range samples {
		samples[i] = 0.1
	}
	block := unsafe.Slice((*byte)(unsafe.Pointer(&samples[0])), len(samples)*4)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			c.onAudio(nil, block, frames)
		}
	}()

	// Retire it while the callback is running, the way Stop does.
	retired := c.resampler.Swap(nil)

	// Close only once no callback can still hold it — device.Uninit is what
	// gives production that guarantee, and the callback is a use-after-free
	// without it.
	wg.Wait()
	if retired != nil {
		(*retired).Close()
	}

	// The callback must have stopped writing once the resampler was retired.
	if c.resampler.Load() != nil {
		t.Error("the resampler was not retired")
	}
}

// A meter capture publishes its peak and writes nothing to the ring. Without
// that, the ring fills in a second with no translator draining it and logs an
// overflow on every callback.
func TestMeterCaptureFeedsThePeakButNotTheRing(t *testing.T) {
	c := NewMeterCapture("")
	c.channels = 1
	stream, err := resample.NewStream(48_000, TargetRate)
	if err != nil {
		t.Fatalf("could not build a resampler: %v", err)
	}
	c.resampler.Store(&stream)
	defer stream.Close()

	const frames = 480
	samples := make([]float32, frames)
	for i := range samples {
		samples[i] = 0.25
	}
	block := unsafe.Slice((*byte)(unsafe.Pointer(&samples[0])), len(samples)*4)
	c.onAudio(nil, block, frames)

	if got := c.Peak(); got < 0.24 || got > 0.26 {
		t.Errorf("Peak = %v, want about 0.25", got)
	}
	if got := c.Ring.Available(); got != 0 {
		t.Errorf("a meter capture wrote %d bytes into the ring", got)
	}
}

// Peak reads without consuming; TakePeak consumes. The Preferences meter reads
// a running session through Peak for exactly this reason — sharing TakePeak
// stole the peaks the caption window needs for speech detection.
func TestPeakDoesNotConsumeAndTakePeakDoes(t *testing.T) {
	c := NewCapture("", 1)
	c.channels = 1
	stream, err := resample.NewStream(48_000, TargetRate)
	if err != nil {
		t.Fatalf("could not build a resampler: %v", err)
	}
	c.resampler.Store(&stream)
	defer stream.Close()

	samples := []float32{0.5, -0.2, 0.3}
	block := unsafe.Slice((*byte)(unsafe.Pointer(&samples[0])), len(samples)*4)
	c.onAudio(nil, block, uint32(len(samples)))

	if got := c.Peak(); got < 0.49 || got > 0.51 {
		t.Fatalf("Peak = %v, want about 0.5", got)
	}
	if got := c.Peak(); got < 0.49 || got > 0.51 {
		t.Errorf("Peak consumed the reading: second call = %v", got)
	}
	if got := c.TakePeak(); got < 0.49 || got > 0.51 {
		t.Errorf("TakePeak = %v, want about 0.5", got)
	}
	if got := c.TakePeak(); got != 0 {
		t.Errorf("TakePeak did not reset: %v", got)
	}
}
