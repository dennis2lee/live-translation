package ui

import (
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/dennis2lee/live-translation/src_go/internal/classify"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
)

// gateModel loads the spike copy of the real model, skipping when absent —
// the same rule as the classify package's own golden tests.
func gateModel(t *testing.T) *classify.Model {
	t.Helper()
	const path = "../../spikes/fixtures/yamnet.tflite"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("model not present — run ./spikes/fixtures/fetch_yamnet.sh")
	}
	m, err := classify.LoadModel(path)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// gatedWindow is a window with the feature on and the loader stubbed.
func gatedWindow(t *testing.T) *Window {
	t.Helper()
	model := gateModel(t)
	w := newTestWindow(t)
	w.loadModel = func() (*classify.Model, error) { return model, nil }
	w.settings.MusicGate = true
	w.settings.MusicGateThreshold = 0.5
	w.settings.MusicGateHold = 2.0
	t.Cleanup(w.stopMusicGate)
	return w
}

func TestMusicGateStaysOffByDefault(t *testing.T) {
	w := newTestWindow(t)
	if w.settings.MusicGate {
		t.Error("MusicGate is on by default — it needs a model download nobody asked for")
	}
	if f := w.musicFilter(); f != nil {
		t.Error("a filter was built with the feature off")
	}
	if w.musicGate != nil {
		t.Error("a gate exists with the feature off")
	}
}

func TestMusicFilterBuildsAGate(t *testing.T) {
	w := gatedWindow(t)
	f := w.musicFilter()
	if f == nil {
		t.Fatal("no filter with the feature on and a model available")
	}
	if w.musicGate == nil {
		t.Fatal("no gate behind the filter")
	}
	got := w.musicGate.Tuning()
	if got.Threshold != 0.5 || got.HoldSeconds != 2.0 {
		t.Errorf("gate tuning = %+v, want the settings values", got)
	}
}

// A missing or corrupt model must not stop the session. Captions are the
// product; the gate is a refinement.
func TestAMissingModelIsNotFatal(t *testing.T) {
	w := newTestWindow(t)
	w.loadModel = func() (*classify.Model, error) { return nil, errors.New("no such file") }
	w.settings.MusicGate = true

	f := w.musicFilter()
	if f != nil {
		t.Error("a filter was built with no model")
	}
	if w.notice == "" {
		t.Error("the model failure was silent — the user sees an ungated session with no explanation")
	}
}

func TestStopMusicGateIsSafeTwice(t *testing.T) {
	w := gatedWindow(t)
	w.musicFilter()
	w.stopMusicGate()
	w.stopMusicGate()
	if w.musicGate != nil {
		t.Error("the gate survived stopMusicGate")
	}
}

// Slider changes reach a running gate without a translator restart — the
// whole point of tuning against live music.
func TestTuningMovesTheRunningGateInPlace(t *testing.T) {
	w := gatedWindow(t)
	w.musicFilter()
	fakeSession(t, w)

	next := w.settings
	next.MusicGateThreshold = 0.7
	next.MusicGateHold = 4.0
	w.applySettings(next)

	if w.musicGate == nil {
		t.Fatal("the gate was torn down by a tuning change")
	}
	got := w.musicGate.Tuning()
	if got.Threshold != 0.7 || got.HoldSeconds != 4.0 {
		t.Errorf("gate tuning = %+v after Apply, want 0.7/4.0", got)
	}
}

// The model survives gate teardown: loading dequantizes four million weights,
// and a stop/start must not pay that twice.
func TestTheModelIsLoadedOnce(t *testing.T) {
	w := gatedWindow(t)
	loads := 0
	inner := w.loadModel
	w.loadModel = func() (*classify.Model, error) { loads++; return inner() }

	w.musicFilter()
	w.stopMusicGate()
	w.musicFilter()
	w.stopMusicGate()

	if loads != 1 {
		t.Errorf("the model was loaded %d times across two sessions, want 1", loads)
	}
}

// The ♪ follows the gate: shown while it suppresses, gone when it opens and
// when the session ends.
func TestTheNoteIndicatorFollowsTheGate(t *testing.T) {
	w := gatedWindow(t)
	w.musicFilter()

	w.view.SetMusicGated(true)
	if !w.view.MusicGated() {
		t.Fatal("the view refused the indicator")
	}
	w.stopMusicGate()
	if w.view.MusicGated() {
		t.Error("stopping the gate left the ♪ on screen")
	}
}

// StopTranslation always retires the gate, translator or none.
func TestStopTranslationRetiresTheGate(t *testing.T) {
	w := gatedWindow(t)
	w.musicFilter()
	w.state = translator.StateRunning

	w.StopTranslation()

	if w.musicGate != nil {
		t.Error("StopTranslation left the gate running")
	}
}

// --- adaptive session prep -------------------------------------------------

// With no music gate there is no window to wait for, so the predicate must be
// nil rather than a function that always says no: a translator handed one
// would sit out the whole reserve at every reconnect for nothing. This is what
// makes the feature safe to ship switched on.
func TestWithNoGateThereIsNoQuietPredicate(t *testing.T) {
	w := newTestWindow(t)
	w.musicGate = nil
	if w.gateClosed() != nil {
		t.Error("gateClosed returned a predicate with no gate running")
	}
}

// A running gate gives a predicate that reads it. Whether it tracks the gate
// as the gate changes is Gate.Closed's own contract, tested in classify; what
// is tested here is that the window hands over the live gate rather than a
// snapshot of its state taken when the translator was built.
func TestARunningGateGivesAPredicate(t *testing.T) {
	w := newTestWindow(t)
	gate := &classify.Gate{}
	w.musicGate = gate
	quiet := w.gateClosed()
	if quiet == nil {
		t.Fatal("no predicate with a gate running")
	}
	if quiet() != gate.Closed() {
		t.Error("the predicate does not report what the gate reports")
	}
	// A fresh gate is open, so comparing return values cannot tell the live
	// method from a wrapper that always says no — and that wrapper is the
	// mistake worth catching, because it would sit out the whole reserve at
	// every reconnect and look like the feature simply never fires. The gate
	// cannot be closed from this package without a test-only door in
	// classify, so pin the handover itself: the predicate must *be* the
	// gate's method, not a closure over a value read once.
	want := reflect.ValueOf(gate.Closed).Pointer()
	if got := reflect.ValueOf(quiet).Pointer(); got != want {
		t.Error("the predicate is a wrapper, not the gate's own Closed method")
	}
}

// Every option the session is opened with has to force a rebuild, or the
// setting silently does nothing until the next restart.
func TestTheSettingsThatNeedAFreshSession(t *testing.T) {
	base := config.Default()
	for name, change := range map[string]func(config.Settings) config.Settings{
		"adaptive session prep": func(s config.Settings) config.Settings {
			s.AdaptiveSessionPrep = !s.AdaptiveSessionPrep
			return s
		},
		"music gate":   func(s config.Settings) config.Settings { s.MusicGate = !s.MusicGate; return s },
		"silence gate": func(s config.Settings) config.Settings { s.SilenceGate = !s.SilenceGate; return s },
		"warm handover": func(s config.Settings) config.Settings {
			s.WarmHandover = !s.WarmHandover
			return s
		},
		"provider": func(s config.Settings) config.Settings {
			s.Provider = config.ProviderOpenAI
			return s
		},
		"target language": func(s config.Settings) config.Settings {
			s.TargetLanguageCode = "ko"
			return s
		},
		"source vocabulary": func(s config.Settings) config.Settings {
			s.VocabularySource = "새 단어"
			return s
		},
	} {
		if !sessionNeedsRestart(base, change(base)) {
			t.Errorf("%s changed without asking for a fresh session", name)
		}
	}
}

// And the ones that must not: a rebuild punches a hole in the audio, so
// anything that reaches a running session has to stay out of that list.
func TestTheSettingsThatMustNotRestartTheSession(t *testing.T) {
	base := config.Default()
	for name, change := range map[string]func(config.Settings) config.Settings{
		"speech volume":  func(s config.Settings) config.Settings { s.SpeechVolume = 0.1; return s },
		"font size":      func(s config.Settings) config.Settings { s.FontSize = 99; return s },
		"gate threshold": func(s config.Settings) config.Settings { s.MusicGateThreshold = 0.9; return s },
		"gate release":   func(s config.Settings) config.Settings { s.MusicGateRelease = 4; return s },
		"debug logging":  func(s config.Settings) config.Settings { s.DebugLogging = !s.DebugLogging; return s },
		"clear on silence": func(s config.Settings) config.Settings {
			s.ClearAfterSeconds = 9
			return s
		},
	} {
		if sessionNeedsRestart(base, change(base)) {
			t.Errorf("%s asked for a fresh session — every Apply would cut the audio", name)
		}
	}
}

// Nothing changed, nothing to do.
func TestUnchangedSettingsNeedNoRestart(t *testing.T) {
	s := config.Default()
	if sessionNeedsRestart(s, s) {
		t.Error("identical settings asked for a restart")
	}
}
