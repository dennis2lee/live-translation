package audio

import (
	"encoding/binary"
	"errors"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/gen2brain/malgo"

	"github.com/dennis2lee/live-translation/src_go/internal/resample"
)

// Capture opens a device at its native rate and feeds 16 kHz PCM into the ring
// buffer.
//
// The device callback runs on miniaudio's own thread. It touches no UI, takes
// no lock, and allocates nothing after start — everything it needs is
// preallocated in scratch buffers below.
type Capture struct {
	deviceName string
	Ring       *Ring

	mu         sync.Mutex
	device     *malgo.Device
	resampler  resample.Stream
	channels   int
	nativeRate int
	running    bool
	current    InputDevice

	underruns atomic.Int64
	// peak is written by the callback and read by the UI, so it is atomic
	// rather than the plain float the Python build could get away with.
	peak atomic.Uint64

	// Scratch, owned by the callback thread once the device is running.
	mono []float32
	pcm  []byte
}

// NewCapture builds a capture for the named device. An empty name means the
// system default input.
func NewCapture(deviceName string, ringSeconds int) *Capture {
	if ringSeconds <= 0 {
		ringSeconds = RingSeconds
	}
	return &Capture{
		deviceName: deviceName,
		Ring:       NewRing(TargetRate * BytesPerSample * ringSeconds),
	}
}

// NativeRate is the rate the device was opened at.
func (c *Capture) NativeRate() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nativeRate
}

// Underruns counts device callbacks that reported a problem.
func (c *Capture) Underruns() int64 { return c.underruns.Load() }

// IsRunning reports whether the device is open.
func (c *Capture) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// Device is the device in use.
func (c *Capture) Device() InputDevice {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

// TakePeak returns the highest sample seen since the last call, 0.0–1.0, and
// resets it.
//
// This lets the UI tell "nobody is speaking" from "this device is delivering
// nothing at all" — an input routed to silence opens and streams happily, so
// without this the two look identical. Measured in the same room: built-in mic
// −36 dBFS, an input with nothing routed into it −88 dBFS.
func (c *Capture) TakePeak() float64 {
	return math.Float64frombits(c.peak.Swap(0))
}

// Start opens the stream. It returns the device it settled on.
func (c *Capture) Start() (InputDevice, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return InputDevice{}, errors.New("capture already started")
	}

	dev, ok := ResolveDevice(c.deviceName)
	if !ok {
		return InputDevice{}, errors.New("no audio input device found")
	}
	ctx, err := acquireContext()
	if err != nil {
		return InputDevice{}, err
	}

	c.nativeRate = int(dev.DefaultSampleRate)
	if c.nativeRate == 0 {
		c.nativeRate = 48_000
	}
	c.channels = min(dev.Channels, 2)
	c.underruns.Store(0)
	c.peak.Store(0)

	// The streaming resampler carries filter state across chunk boundaries.
	// A one-shot resample per chunk clicks at every seam.
	resampler, err := resample.NewStream(float64(c.nativeRate), TargetRate)
	if err != nil {
		return InputDevice{}, err
	}
	c.resampler = resampler
	c.Ring.Clear()

	frames := c.nativeRate * CallbackMS / 1000
	c.mono = make([]float32, 0, frames*2)
	c.pcm = make([]byte, 0, frames*BytesPerSample*2)

	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = malgo.FormatF32
	cfg.Capture.Channels = uint32(c.channels)
	cfg.Capture.DeviceID = dev.id.Pointer()
	cfg.SampleRate = uint32(c.nativeRate)
	cfg.PeriodSizeInMilliseconds = CallbackMS

	device, err := malgo.InitDevice(ctx.Context, cfg, malgo.DeviceCallbacks{Data: c.onAudio})
	if err != nil {
		resampler.Close()
		c.resampler = nil
		return InputDevice{}, err
	}
	if err := device.Start(); err != nil {
		device.Uninit()
		resampler.Close()
		c.resampler = nil
		return InputDevice{}, err
	}

	c.device = device
	c.running = true
	c.current = dev
	slog.Info("audio capture started",
		"device", dev.Name, "rate", c.nativeRate, "channels", c.channels, "target", TargetRate)
	return dev, nil
}

// Stop closes the stream. Safe to call when nothing is running.
func (c *Capture) Stop() {
	c.mu.Lock()
	device, resampler := c.device, c.resampler
	c.device, c.resampler = nil, nil
	c.running = false
	c.current = InputDevice{}
	underruns, dropped := c.underruns.Load(), c.Ring.DroppedBytes()
	c.mu.Unlock()

	if device == nil {
		return
	}
	// Stop before releasing the resampler: the callback may still be in flight,
	// and Uninit is what waits for it.
	if err := device.Stop(); err != nil {
		slog.Warn("error stopping the audio device", "err", err)
	}
	device.Uninit()
	if resampler != nil {
		resampler.Close()
	}
	slog.Info("audio capture stopped", "underruns", underruns, "dropped_bytes", dropped)
}

// --- device callback thread ----------------------------------------------
// No UI, no locks, no allocation after start.

func (c *Capture) onAudio(_, in []byte, frames uint32) {
	if len(in) == 0 || frames == 0 {
		return
	}
	resampler := c.resampler
	if resampler == nil {
		return
	}

	// The block arrives as interleaved float32.
	samples := unsafe.Slice((*float32)(unsafe.Pointer(&in[0])), len(in)/4)
	channels := c.channels
	if channels < 1 {
		channels = 1
	}

	c.mono = c.mono[:0]
	var blockPeak float32
	for i := 0; i+channels <= len(samples); i += channels {
		var sum float32
		for ch := 0; ch < channels; ch++ {
			sum += samples[i+ch]
		}
		v := sum / float32(channels)
		c.mono = append(c.mono, v)
		if a := absf(v); a > blockPeak {
			blockPeak = a
		}
	}
	if len(c.mono) == 0 {
		return
	}

	// Publish the peak if this block was louder than whatever is unread.
	for {
		old := math.Float64frombits(c.peak.Load())
		next := float64(blockPeak)
		if next <= old || c.peak.CompareAndSwap(math.Float64bits(old), math.Float64bits(next)) {
			break
		}
	}

	out, err := resampler.Process(c.mono)
	if err != nil {
		// Counted rather than raised: the callback has no one to raise to, and
		// a stall here would starve the device.
		c.underruns.Add(1)
		return
	}
	if len(out) == 0 {
		return
	}

	c.pcm = c.pcm[:0]
	for _, v := range out {
		c.pcm = binary.LittleEndian.AppendUint16(c.pcm, uint16(clampToInt16(v)))
	}
	c.Ring.Write(c.pcm)
}

func absf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// clampToInt16 matches float_to_pcm16 in the Python build: scale by 32767, then
// clip to the int16 range rather than wrapping.
func clampToInt16(v float32) int16 {
	scaled := float64(v) * 32767.0
	if scaled > 32767 {
		return 32767
	}
	if scaled < -32768 {
		return -32768
	}
	return int16(scaled)
}
