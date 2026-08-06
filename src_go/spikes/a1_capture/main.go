// Spike A1 — does malgo capture as well as PortAudio did?
//
// The Python build measured the acceptance numbers this spike has to match
// (spikes/RESULTS.md): a Sound Blaster X3 at 96 kHz/2ch captured 158124 bytes
// in 5 s, 98.8% of nominal, 0 underruns, 0 drops; built-in mic peak -36 dBFS,
// an input with nothing routed into it -88 dBFS.
//
//	go run ./spikes/a1_capture             # list capture devices
//	go run ./spikes/a1_capture -all        # capture 5 s from every device
//	go run ./spikes/a1_capture -device "X3"
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gen2brain/malgo"
)

const (
	captureSeconds = 5.0
	// Nominal bytes must land within this fraction of what the clock says.
	acceptRatio = 0.98
)

type meter struct {
	bytes     atomic.Uint64
	calls     atomic.Uint64
	peak      atomic.Uint64 // float64 bits, |sample| in [0,1]
	firstAt   atomic.Int64
	lastAt    atomic.Int64
	maxGapUS  atomic.Int64
	minFrames atomic.Uint64
	maxFrames atomic.Uint64
}

func (m *meter) observe(samples []byte, frames uint32) {
	now := time.Now().UnixMicro()
	if m.firstAt.CompareAndSwap(0, now) {
		m.minFrames.Store(uint64(frames))
	} else if prev := m.lastAt.Load(); prev != 0 {
		if gap := now - prev; gap > m.maxGapUS.Load() {
			m.maxGapUS.Store(gap)
		}
	}
	m.lastAt.Store(now)
	m.bytes.Add(uint64(len(samples)))
	m.calls.Add(1)
	if f := uint64(frames); f > m.maxFrames.Load() {
		m.maxFrames.Store(f)
	} else if f < m.minFrames.Load() {
		m.minFrames.Store(f)
	}

	// Peak of the block, as 16-bit little-endian.
	var peak int16
	for i := 0; i+1 < len(samples); i += 2 {
		v := int16(binary.LittleEndian.Uint16(samples[i : i+2]))
		if v == math.MinInt16 {
			v = math.MaxInt16
		} else if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	p := float64(peak) / 32768.0
	for {
		old := math.Float64frombits(m.peak.Load())
		if p <= old || m.peak.CompareAndSwap(math.Float64bits(old), math.Float64bits(p)) {
			break
		}
	}
}

func dBFS(peak float64) float64 {
	if peak <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(peak)
}

func describe(d malgo.DeviceInfo) string {
	rates := make([]string, 0, len(d.Formats))
	seen := map[string]bool{}
	for _, f := range d.Formats {
		s := fmt.Sprintf("%d Hz/%dch/%v", f.SampleRate, f.Channels, f.Format)
		if !seen[s] {
			seen[s] = true
			rates = append(rates, s)
		}
	}
	if len(rates) == 0 {
		rates = append(rates, "no native formats reported")
	}
	def := ""
	if d.IsDefault != 0 {
		def = "  [default]"
	}
	return fmt.Sprintf("%s%s\n      native: %s", d.Name(), def, strings.Join(rates, ", "))
}

// capture opens one device at its native rate and records for captureSeconds.
func capture(ctx *malgo.AllocatedContext, info malgo.DeviceInfo) error {
	channels := uint32(1)
	if len(info.Formats) > 0 && info.Formats[0].Channels > 0 {
		channels = info.Formats[0].Channels
	}

	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = channels
	cfg.Capture.DeviceID = info.ID.Pointer()
	// 0 asks miniaudio for the device's own rate — the rule from CLAUDE.md:
	// never make the backend resample, do it with soxr where we control it.
	cfg.SampleRate = 0
	cfg.PeriodSizeInMilliseconds = 100

	m := &meter{}
	device, err := malgo.InitDevice(ctx.Context, cfg, malgo.DeviceCallbacks{
		Data: func(_, in []byte, frames uint32) { m.observe(in, frames) },
	})
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}
	defer device.Uninit()

	if err := device.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	started := time.Now()
	time.Sleep(time.Duration(captureSeconds * float64(time.Second)))
	if err := device.Stop(); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	elapsed := time.Since(started).Seconds()

	rate := device.CaptureInternalSampleRate()
	if rate == 0 {
		rate = device.SampleRate()
	}
	got := m.bytes.Load()
	nominal := uint64(float64(rate) * float64(channels) * 2 * elapsed)
	ratio := float64(got) / float64(nominal)

	fmt.Printf("      opened at %d Hz / %dch / S16 (config asked for native)\n", rate, channels)
	fmt.Printf("      %d bytes in %.2f s — %.1f%% of nominal %d\n",
		got, elapsed, ratio*100, nominal)
	fmt.Printf("      %d callbacks, frames %d..%d, longest gap %.1f ms\n",
		m.calls.Load(), m.minFrames.Load(), m.maxFrames.Load(),
		float64(m.maxGapUS.Load())/1000)
	fmt.Printf("      peak %.1f dBFS\n", dBFS(math.Float64frombits(m.peak.Load())))

	switch {
	case ratio < acceptRatio:
		fmt.Printf("      FAIL: below the %.0f%% acceptance bar\n", acceptRatio*100)
	case rate == 16000 && len(info.Formats) > 0 && info.Formats[0].SampleRate != 16000:
		fmt.Printf("      FAIL: backend resampled to 16 kHz instead of honouring the native rate\n")
	default:
		fmt.Printf("      PASS\n")
	}
	return nil
}

func main() {
	all := flag.Bool("all", false, "capture from every device, not just list them")
	name := flag.String("device", "", "capture from the device whose name contains this")
	flag.Parse()

	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "context: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		_ = ctx.Uninit()
		ctx.Free()
	}()
	fmt.Println()

	infos, err := ctx.Devices(malgo.Capture)
	if err != nil {
		fmt.Fprintf(os.Stderr, "enumerate: %v\n", err)
		os.Exit(1)
	}

	for i, info := range infos {
		// Formats are only filled in by the detailed query on some backends.
		if full, err := ctx.DeviceInfo(malgo.Capture, info.ID, malgo.Shared); err == nil {
			info = full
		}
		fmt.Printf("  [%d] %s\n", i, describe(info))

		wanted := *all
		if *name != "" {
			wanted = strings.Contains(strings.ToLower(info.Name()), strings.ToLower(*name))
		}
		if wanted {
			if err := capture(ctx, info); err != nil {
				fmt.Printf("      ERROR: %v\n", err)
			}
		}
		fmt.Println()
	}
}
