package audio

import "testing"

// Device identity is the name, never the index: indexes move when a USB
// device is reconnected, which is why the settings store a name. resolveByName
// is the whole of that rule, and it is what stands between a renamed or
// unplugged device and a session with no audio at all.

func inputs() []InputDevice {
	return []InputDevice{
		{Name: "Sound Blaster X3"},
		{Name: "MacBook Pro Microphone", IsDefault: true},
		{Name: "Scarlett 2i2"},
	}
}

func TestAStoredNameWinsOverTheDefault(t *testing.T) {
	got, ok := resolveByName(inputs(), "Scarlett 2i2", "input")
	if !ok || got.Name != "Scarlett 2i2" {
		t.Fatalf("resolved %q (ok=%v), want the named device", got.Name, ok)
	}
}

// An unplugged device must not take the session down with it. Falling back to
// the default keeps captions coming from *something*.
func TestAnAbsentDeviceFallsBackToTheDefault(t *testing.T) {
	got, ok := resolveByName(inputs(), "Sound Blaster X3 (unplugged)", "input")
	if !ok || got.Name != "MacBook Pro Microphone" {
		t.Fatalf("resolved %q (ok=%v), want the system default", got.Name, ok)
	}
}

func TestNoStoredNameTakesTheDefault(t *testing.T) {
	for _, name := range []string{"", "   "} {
		got, ok := resolveByName(inputs(), name, "input")
		if !ok || got.Name != "MacBook Pro Microphone" {
			t.Errorf("name %q resolved to %q (ok=%v), want the default", name, got.Name, ok)
		}
	}
}

// Some machines report no default at all. First is better than nothing.
func TestWithNoDefaultTheFirstDeviceIsUsed(t *testing.T) {
	devices := []InputDevice{{Name: "A"}, {Name: "B"}}
	got, ok := resolveByName(devices, "missing", "input")
	if !ok || got.Name != "A" {
		t.Fatalf("resolved %q (ok=%v), want the first device", got.Name, ok)
	}
}

// The one case that is genuinely a failure, and the only one that returns
// false: the caller has to be able to tell "no audio hardware" apart from
// "not the device you asked for".
func TestNoDevicesAtAllIsTheOnlyFailure(t *testing.T) {
	if _, ok := resolveByName([]InputDevice(nil), "anything", "input"); ok {
		t.Error("resolveByName claimed a device on an empty list")
	}
}

// Playback resolves by the same rule; the generic is shared, the types are not.
func TestOutputDevicesResolveByTheSameRule(t *testing.T) {
	devices := []OutputDevice{
		{Name: "External Headphones"},
		{Name: "MacBook Pro Speakers", IsDefault: true},
	}
	if got, _ := resolveByName(devices, "External Headphones", "output"); got.Name != "External Headphones" {
		t.Errorf("resolved %q, want the named output", got.Name)
	}
	if got, _ := resolveByName(devices, "Studio Display Speakers", "output"); got.Name != "MacBook Pro Speakers" {
		t.Errorf("resolved %q, want the default output", got.Name)
	}
}

// Clipping, not wrapping. A wrap turns the loudest moment of a sentence into a
// full-scale sample of the opposite sign, which is a click — audibly worse
// than the flat top it replaces.
func TestLoudSamplesClipRatherThanWrap(t *testing.T) {
	for _, tc := range []struct {
		in   float32
		want int16
	}{
		{0, 0},
		{1, 32767},
		{-1, -32767},
		{1.5, 32767},
		{-1.5, -32768},
		{9, 32767},
		{-9, -32768},
		{0.5, 16383},
	} {
		if got := clampToInt16(tc.in); got != tc.want {
			t.Errorf("clampToInt16(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// msOf turns the starvation and drop counters into the milliseconds the log
// reports. It is the number a user reads to answer "is this my network?", so
// a stereo device must not make it read half.
func TestByteCountsBecomeMilliseconds(t *testing.T) {
	mono := &Player{devRate: 48_000, devChan: 1}
	if got := mono.msOf(48_000 * BytesPerSample); got != 1000 {
		t.Errorf("mono: %d ms for a second of audio", got)
	}
	stereo := &Player{devRate: 48_000, devChan: 2}
	if got := stereo.msOf(48_000 * BytesPerSample * 2); got != 1000 {
		t.Errorf("stereo: %d ms for a second of audio", got)
	}
	// Before Start there is no device rate, and dividing by it would panic.
	if got := (&Player{}).msOf(1234); got != 0 {
		t.Errorf("msOf on a stopped player = %d, want 0", got)
	}
}
