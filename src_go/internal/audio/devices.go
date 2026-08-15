// Package audio is the audio pipeline, in both directions.
//
// Capture opens the interface at its native rate, then downmixes to mono,
// resamples to 16 kHz with the streaming soxr converter, converts to int16 LE,
// and writes into a ring buffer. The ring buffer keeps filling regardless of
// connection state, so audio survives a reconnect: whatever accumulated during
// the gap is pushed to the new session.
//
// Player is the mirror image, for the translated speech the model sends back:
// 24 kHz mono PCM16 in, resampled to whatever rate the output device runs at,
// through a jitter buffer that plays silence rather than stale audio when it
// runs dry.
//
// Both directions share one malgo context, which is why they share a package.
package audio

import (
	"log/slog"
	"strings"
	"sync"

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

// sharedContext is the one miniaudio context the process needs. malgo contexts
// are expensive to build, and enumeration happens every time a menu opens.
//
// Guarded, because "only the UI goroutine touches this" was true but written
// down nowhere. Preferences opens a capture from its own dialog, the menu
// enumerates devices, and shutdown releases it — all on the UI goroutine today,
// and nothing in the types said they had to stay there.
var (
	contextMu     sync.Mutex
	sharedContext *malgo.AllocatedContext
)

func acquireContext() (*malgo.AllocatedContext, error) {
	contextMu.Lock()
	defer contextMu.Unlock()
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
	contextMu.Lock()
	ctx := sharedContext
	sharedContext = nil
	contextMu.Unlock()

	if ctx == nil {
		return
	}
	_ = ctx.Uninit()
	ctx.Free()
}

// OutputDevice is one playback device.
//
// Identity is the name, for the same reason as InputDevice: indexes and ID
// bytes move when a device is reconnected, and the settings store a name.
type OutputDevice struct {
	Name              string
	Channels          int
	DefaultSampleRate float64
	IsDefault         bool

	id malgo.DeviceID
}

// enumerate queries one direction, filling in the detailed device info where a
// backend offers it. That detail is where the native rate lives, and the native
// rate is the whole reason not to let the backend resample for us.
func enumerate(kind malgo.DeviceType) []malgo.DeviceInfo {
	ctx, err := acquireContext()
	if err != nil {
		slog.Error("could not initialise the audio backend", "err", err)
		return nil
	}
	infos, err := ctx.Devices(kind)
	if err != nil {
		slog.Error("could not query audio devices", "err", err, "kind", kind)
		return nil
	}
	for i, info := range infos {
		if full, err := ctx.DeviceInfo(kind, info.ID, malgo.Shared); err == nil {
			infos[i] = full
		}
	}
	return infos
}

// rateAndChannels reads the native format, with fallbacks for a backend that
// reported none.
func rateAndChannels(info malgo.DeviceInfo, defaultChannels int) (float64, int) {
	rate, channels := 0.0, 0
	if len(info.Formats) > 0 {
		channels = int(info.Formats[0].Channels)
		rate = float64(info.Formats[0].SampleRate)
	}
	if rate == 0 {
		rate = 48_000
	}
	if channels < 1 {
		channels = defaultChannels
	}
	return rate, channels
}

// ListInputDevices enumerates devices that have input channels. Call it every
// time the menu opens — devices come and go.
func ListInputDevices() []InputDevice {
	infos := enumerate(malgo.Capture)
	out := make([]InputDevice, 0, len(infos))
	for _, info := range infos {
		rate, channels := rateAndChannels(info, 1)
		out = append(out, InputDevice{
			Name:              info.Name(),
			Channels:          channels,
			DefaultSampleRate: rate,
			IsDefault:         info.IsDefault != 0,
			id:                info.ID,
		})
	}
	return out
}

// ListOutputDevices enumerates playback devices, the same way and for the same
// reason.
//
// Worth knowing before trusting any of it: on the machine this was written on
// the speakers run at 44.1 kHz while the default device runs at 48, and the
// default is a *virtual* device rather than the speakers. Both facts are why
// the rate is read per device and why the user gets to choose.
func ListOutputDevices() []OutputDevice {
	infos := enumerate(malgo.Playback)
	out := make([]OutputDevice, 0, len(infos))
	for _, info := range infos {
		rate, channels := rateAndChannels(info, 2)
		out = append(out, OutputDevice{
			Name:              info.Name(),
			Channels:          channels,
			DefaultSampleRate: rate,
			IsDefault:         info.IsDefault != 0,
			id:                info.ID,
		})
	}
	return out
}

// identified is what device resolution needs from either direction. The two
// device types are separate structs because they are separate things in the
// settings and in the UI, but finding one by name is the same problem twice.
type identified interface {
	InputDevice | OutputDevice
	identity() (name string, isDefault bool)
}

func (d InputDevice) identity() (string, bool)  { return d.Name, d.IsDefault }
func (d OutputDevice) identity() (string, bool) { return d.Name, d.IsDefault }

// resolveByName looks a device up, falling back to the system default and then
// to whatever is first. It returns false only when there are no devices at all.
func resolveByName[T identified](devices []T, name, kind string) (T, bool) {
	var zero T
	if len(devices) == 0 {
		return zero, false
	}
	if strings.TrimSpace(name) != "" {
		for _, d := range devices {
			if got, _ := d.identity(); got == name {
				return d, true
			}
		}
		slog.Warn("audio device not found — falling back to the default",
			"name", name, "kind", kind)
	}
	for _, d := range devices {
		if _, isDefault := d.identity(); isDefault {
			return d, true
		}
	}
	return devices[0], true
}

// ResolveDevice looks an input device up by name, falling back to the system
// default. It returns false when there is no input device at all.
func ResolveDevice(name string) (InputDevice, bool) {
	return resolveByName(ListInputDevices(), name, "input")
}

// ResolveOutputDevice is the same for playback.
func ResolveOutputDevice(name string) (OutputDevice, bool) {
	return resolveByName(ListOutputDevices(), name, "output")
}
