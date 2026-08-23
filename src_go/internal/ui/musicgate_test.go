package ui

import (
	"errors"
	"os"
	"testing"

	"github.com/dennis2lee/live-translation/src_go/internal/classify"
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
