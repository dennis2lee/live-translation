// Package audio is the capture pipeline.
//
// It opens the interface at its native rate, then downmixes to mono, resamples
// to 16 kHz with the streaming soxr converter, converts to int16 LE, and writes
// into a ring buffer.
//
// The ring buffer keeps filling regardless of connection state, so audio
// survives a reconnect: whatever accumulated during the gap is pushed to the
// new session.
package audio

import (
	"log/slog"
	"strings"

	"github.com/gen2brain/malgo"
)

const (
	// TargetRate is what the Live API wants.
	TargetRate = 16_000
	// BytesPerSample is 16-bit.
	BytesPerSample = 2
	// ChunkMS is the send granularity.
	ChunkMS = 100
	// ChunkBytes is one 100 ms chunk of 16 kHz mono PCM16 — 3200 bytes.
	ChunkBytes = TargetRate * BytesPerSample * ChunkMS / 1000
	// RingSeconds is how much audio survives a reconnect.
	RingSeconds = 30
	// CallbackMS is the device callback period asked for.
	CallbackMS = 20
)

// InputDevice is one capture device.
//
// Identity is the name, never the index or the ID bytes: those change when a
// USB device is reconnected, which is the whole reason the settings store a
// name.
type InputDevice struct {
	Name              string
	Channels          int
	DefaultSampleRate float64
	IsDefault         bool

	id malgo.DeviceID
}

// context holds the one miniaudio context the process needs. malgo contexts are
// expensive to build, and enumeration happens every time a menu opens.
var (
	sharedContext *malgo.AllocatedContext
)

func acquireContext() (*malgo.AllocatedContext, error) {
	if sharedContext != nil {
		return sharedContext, nil
	}
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, err
	}
	sharedContext = ctx
	return ctx, nil
}

// ReleaseContext tears the audio backend down. Called once, from shutdown.
func ReleaseContext() {
	if sharedContext == nil {
		return
	}
	_ = sharedContext.Uninit()
	sharedContext.Free()
	sharedContext = nil
}

// ListInputDevices enumerates devices that have input channels. Call it every
// time the menu opens — devices come and go.
func ListInputDevices() []InputDevice {
	ctx, err := acquireContext()
	if err != nil {
		slog.Error("could not initialise the audio backend", "err", err)
		return nil
	}
	infos, err := ctx.Devices(malgo.Capture)
	if err != nil {
		slog.Error("could not query audio devices", "err", err)
		return nil
	}

	out := make([]InputDevice, 0, len(infos))
	for _, info := range infos {
		// The formats are only filled in by the detailed query on some
		// backends, and that is where the native rate comes from.
		if full, err := ctx.DeviceInfo(malgo.Capture, info.ID, malgo.Shared); err == nil {
			info = full
		}
		device := InputDevice{
			Name:      info.Name(),
			Channels:  1,
			IsDefault: info.IsDefault != 0,
			id:        info.ID,
		}
		if len(info.Formats) > 0 {
			device.Channels = int(info.Formats[0].Channels)
			device.DefaultSampleRate = float64(info.Formats[0].SampleRate)
		}
		if device.DefaultSampleRate == 0 {
			device.DefaultSampleRate = 48_000
		}
		if device.Channels < 1 {
			device.Channels = 1
		}
		out = append(out, device)
	}
	return out
}

// ResolveDevice looks a device up by name, falling back to the system default.
// It returns false when there is no input device at all.
func ResolveDevice(name string) (InputDevice, bool) {
	devices := ListInputDevices()
	if len(devices) == 0 {
		return InputDevice{}, false
	}
	if strings.TrimSpace(name) != "" {
		for _, d := range devices {
			if d.Name == name {
				return d, true
			}
		}
		slog.Warn("audio device not found — falling back to the default", "name", name)
	}
	for _, d := range devices {
		if d.IsDefault {
			return d, true
		}
	}
	return devices[0], true
}
