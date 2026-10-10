package ui

import (
	"errors"
	"sync"
	"testing"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
)

// fakeSpeaker stands in for the output device. malgo opens real hardware, and
// a test that needs headphones plugged in is not a test.
type fakeSpeaker struct {
	deviceName string
	startErr   error

	mu      sync.Mutex
	starts  int
	stops   int
	volumes []float64
	written [][]byte

	speechWrites int
	resets       int
	sessionMarks int
	flushes      int
}

func (f *fakeSpeaker) Start() (audio.OutputDevice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return audio.OutputDevice{}, f.startErr
	}
	f.starts++
	name := f.deviceName
	if name == "" {
		name = "Fake Default Output"
	}
	return audio.OutputDevice{Name: name, DefaultSampleRate: 48000, Channels: 2}, nil
}

func (f *fakeSpeaker) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
}

func (f *fakeSpeaker) Write(pcm []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written = append(f.written, append([]byte(nil), pcm...))
}

// WriteSpeech records like Write, and counts separately, so a test can tell
// the model's path (gated) from a local voice's (not).
func (f *fakeSpeaker) WriteSpeech(pcm []byte) {
	f.mu.Lock()
	f.speechWrites++
	f.mu.Unlock()
	f.Write(pcm)
}

// MarkSessionStart counts calls.
func (f *fakeSpeaker) MarkSessionStart() {
	f.mu.Lock()
	f.sessionMarks++
	f.mu.Unlock()
}

// ResetSpeech counts calls.
func (f *fakeSpeaker) ResetSpeech() {
	f.mu.Lock()
	f.resets++
	f.mu.Unlock()
}

func (f *fakeSpeaker) Flush() {
	f.mu.Lock()
	f.flushes++
	f.mu.Unlock()
}

func (f *fakeSpeaker) speechCounts() (speech, resets int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.speechWrites, f.resets
}

func (f *fakeSpeaker) SetVolume(v float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volumes = append(f.volumes, v)
}

func (f *fakeSpeaker) counts() (starts, stops int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops
}

func (f *fakeSpeaker) volumeHistory() []float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]float64(nil), f.volumes...)
}

func (f *fakeSpeaker) writes() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.written...)
}

// speakerFactory records every player the window asks for, so a test can tell
// "reopened" from "left alone".
type speakerFactory struct {
	mu       sync.Mutex
	built    []*fakeSpeaker
	startErr error
}

func (s *speakerFactory) build(deviceName string, _ int) speaker {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := &fakeSpeaker{deviceName: deviceName, startErr: s.startErr}
	s.built = append(s.built, f)
	return f
}

func (s *speakerFactory) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.built)
}

func (s *speakerFactory) last() *fakeSpeaker {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.built) == 0 {
		return nil
	}
	return s.built[len(s.built)-1]
}

// speakingWindow returns a window with speech on and a player running, without
// a session or a device anywhere near it.
//
// startPlayer refuses to run without a capture, which is right — speech with no
// session is nothing to play — so the tests give it one to look at.
func speakingWindow(t *testing.T) (*Window, *speakerFactory) {
	t.Helper()
	w := newTestWindow(t)
	factory := &speakerFactory{}
	w.newSpeaker = factory.build

	w.settings.SpeakTranslation = true
	w.settings.SpeechVolume = 0.8
	fakeSession(t, w)
	return w, factory
}

// fakeSession makes the window look like one that is translating, without
// opening a microphone or a socket.
//
// Both halves are needed and they gate different things: startPlayer asks
// whether audio is being captured, because that is what exists by the time it
// runs inside StartTranslation, while applySessionChanges asks for the state,
// because a settings change during an idle window has no session to disturb.
func fakeSession(t *testing.T, w *Window) {
	t.Helper()
	w.capture = audio.NewCapture("", 1)
	w.state = translator.StateRunning
	t.Cleanup(func() {
		w.capture = nil
		w.state = translator.StateIdle
	})
}

func TestSpeechStaysOffByDefault(t *testing.T) {
	w := newTestWindow(t)
	if w.settings.SpeakTranslation {
		t.Error("SpeakTranslation is on by default — speakers feed the translation back into the microphone")
	}
	if w.settings.SpeechVolume != 1.0 {
		t.Errorf("SpeechVolume = %v by default, want 1.0", w.settings.SpeechVolume)
	}
}

// With speech off, nothing is ever opened.
func TestNoPlayerWhenSpeechIsOff(t *testing.T) {
	w, factory := speakingWindow(t)
	w.settings.SpeakTranslation = false

	w.startPlayer()
	if factory.count() != 0 {
		t.Errorf("built %d players with speech off", factory.count())
	}
	if w.player.Load() != nil {
		t.Error("a player is running with speech off")
	}
}

// Speech with no session is nothing to play.
func TestNoPlayerWithoutASession(t *testing.T) {
	w := newTestWindow(t)
	factory := &speakerFactory{}
	w.newSpeaker = factory.build
	w.settings.SpeakTranslation = true

	w.startPlayer()
	if factory.count() != 0 {
		t.Errorf("built %d players with no session running", factory.count())
	}
}

func TestStartPlayerCarriesTheVolume(t *testing.T) {
	w, factory := speakingWindow(t)
	w.startPlayer()

	if factory.count() != 1 {
		t.Fatalf("built %d players, want 1", factory.count())
	}
	got := factory.last().volumeHistory()
	if len(got) == 0 || got[0] != 0.8 {
		t.Errorf("volume history = %v, want it to open at 0.8", got)
	}
}

// The transition table from the design, row by row. The last row is the one
// that gets broken by accident.
func TestApplyingSettingsMovesThePlayerTheLeastItCan(t *testing.T) {
	t.Run("off to on starts one", func(t *testing.T) {
		w, factory := speakingWindow(t)
		old := w.settings
		old.SpeakTranslation = false

		w.applySpeechChanges(old)
		if factory.count() != 1 {
			t.Fatalf("built %d players, want 1", factory.count())
		}
		if w.player.Load() == nil {
			t.Error("no player is running after switching speech on")
		}
	})

	t.Run("on to off stops it", func(t *testing.T) {
		w, factory := speakingWindow(t)
		w.startPlayer()
		player := factory.last()

		old := w.settings
		w.settings.SpeakTranslation = false
		w.applySpeechChanges(old)

		if _, stops := player.counts(); stops != 1 {
			t.Errorf("Stop called %d times, want 1", stops)
		}
		if w.player.Load() != nil {
			t.Error("the player outlived the setting being switched off")
		}
	})

	t.Run("a new device reopens", func(t *testing.T) {
		w, factory := speakingWindow(t)
		w.startPlayer()
		first := factory.last()

		old := w.settings
		w.settings.OutputDeviceName = "Some Other Speakers"
		w.applySpeechChanges(old)

		if _, stops := first.counts(); stops != 1 {
			t.Errorf("the old player was stopped %d times, want 1", stops)
		}
		if factory.count() != 2 {
			t.Fatalf("built %d players, want 2", factory.count())
		}
		if got := factory.last().deviceName; got != "Some Other Speakers" {
			t.Errorf("reopened on %q", got)
		}
	})

	t.Run("a new volume does not reopen", func(t *testing.T) {
		w, factory := speakingWindow(t)
		w.startPlayer()
		player := factory.last()

		old := w.settings
		w.settings.SpeechVolume = 0.25
		w.applySpeechChanges(old)

		if factory.count() != 1 {
			t.Errorf("built %d players for a volume change — the sound would cut out", factory.count())
		}
		if _, stops := player.counts(); stops != 0 {
			t.Errorf("Stop called %d times for a volume change", stops)
		}
		got := player.volumeHistory()
		if len(got) == 0 || got[len(got)-1] != 0.25 {
			t.Errorf("volume history = %v, want it to end at 0.25", got)
		}
	})

	// Applying an unrelated setting must leave the sound completely alone.
	// Anything else puts a hole in the audio every time any tab is applied.
	t.Run("no change touches nothing", func(t *testing.T) {
		w, factory := speakingWindow(t)
		w.startPlayer()
		player := factory.last()
		volumesBefore := len(player.volumeHistory())

		old := w.settings
		w.settings.FontSize = 72 // something from another tab entirely
		w.applySpeechChanges(old)

		if factory.count() != 1 {
			t.Errorf("built %d players for an unrelated setting", factory.count())
		}
		if _, stops := player.counts(); stops != 0 {
			t.Errorf("Stop called %d times for an unrelated setting", stops)
		}
		if got := len(player.volumeHistory()); got != volumesBefore {
			t.Errorf("volume was set again (%d -> %d) for an unrelated setting", volumesBefore, got)
		}
	})
}

// The blobs have to actually arrive. This is the whole feature in one line.
func TestSpeakAudioReachesThePlayer(t *testing.T) {
	w, factory := speakingWindow(t)
	w.startPlayer()

	w.speakAudio([]byte{1, 2, 3, 4})

	written := factory.last().writes()
	if len(written) != 1 {
		t.Fatalf("the player received %d blobs, want 1", len(written))
	}
	if want := []byte{1, 2, 3, 4}; string(written[0]) != string(want) {
		t.Errorf("blob = %v, want %v", written[0], want)
	}
}

// Audio arrives whether or not anyone is listening — the stream stalls if it is
// not consumed — so a blob with no player must be a no-op, not a crash.
func TestSpeakAudioWithNoPlayerIsHarmless(t *testing.T) {
	w := newTestWindow(t)
	w.speakAudio([]byte{1, 2, 3, 4}) // must not panic
	if w.player.Load() != nil {
		t.Error("speaking created a player out of nothing")
	}
}

// An output device that will not open must not take the session down. Captions
// are the product; sound is an extra.
func TestAFailedOutputDeviceIsNotFatal(t *testing.T) {
	w, factory := speakingWindow(t)
	factory.startErr = errors.New("device is busy")

	w.startPlayer()

	if w.player.Load() != nil {
		t.Error("a player that failed to open was stored anyway")
	}
	if w.notice == "" {
		t.Error("nothing was said about the output device failing")
	}
	if w.errorDetail != "" {
		t.Errorf("a failed output device put the session into an error state: %q", w.errorDetail)
	}
}

// Retrying on Apply is how someone who plugged their headphones back in gets
// sound without restarting the session.
func TestApplyRetriesADeviceThatFailedToOpen(t *testing.T) {
	w, factory := speakingWindow(t)
	factory.startErr = errors.New("device is busy")
	w.startPlayer()
	if w.player.Load() != nil {
		t.Fatal("the first open should have failed")
	}

	factory.startErr = nil
	w.applySpeechChanges(w.settings) // nothing changed, but there is no player

	if w.player.Load() == nil {
		t.Error("Apply did not retry the device that had failed")
	}
}

// Stopping is idempotent, because the shutdown path calls it beside every other
// guarded step.
func TestStopPlayerIsSafeTwice(t *testing.T) {
	w, factory := speakingWindow(t)
	w.startPlayer()
	player := factory.last()

	w.stopPlayer()
	w.stopPlayer()

	if _, stops := player.counts(); stops != 1 {
		t.Errorf("Stop called %d times, want exactly 1", stops)
	}
}

// Cancel reverts through the same path as Apply, so the player has to come back
// to where it was before the dialog opened.
func TestCancellingPreferencesPutsTheSpeechBack(t *testing.T) {
	w := newTestWindow(t)
	factory := &speakerFactory{}
	w.newSpeaker = factory.build
	fakeSession(t, w)

	original := w.settings // speech off, the default

	// Apply turns it on.
	next := original
	next.SpeakTranslation = true
	next.SpeechVolume = 0.5
	w.applySettings(next)
	if w.player.Load() == nil {
		t.Fatal("Apply did not start the player")
	}
	player := factory.last()

	// Cancel puts back what was in force when the dialog opened.
	w.applySettings(original)

	if w.player.Load() != nil {
		t.Error("Cancel left the translation speaking")
	}
	if _, stops := player.counts(); stops != 1 {
		t.Errorf("Stop called %d times on cancel, want 1", stops)
	}
	if w.settings.SpeakTranslation {
		t.Error("SpeakTranslation survived the cancel")
	}
}

// A settings change that does not touch the player must not restart the
// session either — this is the whole-window version of the quiet row above.
func TestApplyingAnUnrelatedSettingKeepsSpeaking(t *testing.T) {
	w, factory := speakingWindow(t)
	w.startPlayer()
	player := factory.last()

	next := w.settings
	next.BoxMaxWidthPct = 70
	w.applySettings(next)

	if factory.count() != 1 {
		t.Errorf("built %d players, want the original left alone", factory.count())
	}
	if _, stops := player.counts(); stops != 0 {
		t.Errorf("Stop called %d times for a layout change", stops)
	}
}

// --- the freeze gate's wiring -------------------------------------------------

// The model's audio goes through the freeze gate; a local voice does not.
// Swap either and the ui tests used to pass regardless, because the fake
// treated both methods as one.
func TestOnlyTheModelsVoiceIsGated(t *testing.T) {
	w, factory := speakingWindow(t)
	w.startPlayer()
	player := factory.last()

	w.speakAudio(make([]byte, 480))
	if speech, _ := player.speechCounts(); speech != 1 {
		t.Errorf("the model's audio went through WriteSpeech %d times, want 1", speech)
	}

	w.playLocalVoice(make([]byte, 480))
	if speech, _ := player.speechCounts(); speech != 1 {
		t.Error("a local voice went through the freeze gate")
	}
	if got := len(player.writes()); got != 2 {
		t.Errorf("%d writes reached the player, want 2", got)
	}
}

// Switching back from a local voice to the model must drop the model audio
// the gate held from before the switch, or it plays first.
func TestReturningToTheModelsVoiceDropsTheHeldTail(t *testing.T) {
	w, factory := speakingWindow(t)
	w.startPlayer()
	player := factory.last()

	w.stopVoice()
	if _, resets := player.speechCounts(); resets != 1 {
		t.Errorf("ResetSpeech called %d times on the switch back, want 1", resets)
	}
}

// A restart keeps the player and replaces the model's stream, so the gate's
// held tail from the old session has to go — otherwise it plays in front of
// the new session's first words, possibly in the other language.
func TestARestartDropsTheHeldTail(t *testing.T) {
	w, factory := speakingWindow(t)
	w.startPlayer()
	player := factory.last()
	defer config.SetEnvForTests(func(name string) string {
		if name == "GEMINI_API_KEY" {
			return "AIza-test-key-not-real"
		}
		return ""
	})()
	w.buildTranslator = func(*audio.Ring, string) (*translator.Translator, error) {
		return nil, errors.New("no network in tests")
	}

	w.restartTranslator()
	if _, resets := player.speechCounts(); resets != 1 {
		t.Errorf("ResetSpeech called %d times on a restart, want 1", resets)
	}
}

// The translator's session-start signal has to reach the player, directly
// and in order with the audio: the player removes a session's opening buzz
// whole only if it knows where the session opened.
func TestASessionStartReachesThePlayer(t *testing.T) {
	w, factory := speakingWindow(t)
	w.startPlayer()
	player := factory.last()

	cb := w.translatorCallbacks()
	if cb.OnSessionStart == nil {
		t.Fatal("the translator is given no way to report a session start")
	}
	cb.OnSessionStart()
	cb.OnAudio(make([]byte, 480))

	player.mu.Lock()
	marks := player.sessionMarks
	player.mu.Unlock()
	if marks != 1 {
		t.Errorf("the player saw %d session starts, want 1", marks)
	}
	if speech, resets := player.speechCounts(); speech != 1 || resets != 0 {
		t.Errorf("WriteSpeech %d times, ResetSpeech %d times, want 1 and 0 — a session start "+
			"must not drop what the last session's opening still holds", speech, resets)
	}
}

// TestASessionStartReachesThePlayer calls translatorCallbacks itself, so it
// would pass with a production translator built from other callbacks — and
// every test that runs a session replaces the builder. This checks what the
// production branch actually hands the translator.
func TestTheProductionTranslatorReportsSessionStarts(t *testing.T) {
	w, factory := speakingWindow(t)
	w.startPlayer()
	player := factory.last()

	opts := w.translatorOptions(audio.NewRing(4096), "AIza-test-key-not-real")
	if opts.Callbacks.OnSessionStart == nil || opts.Callbacks.OnAudio == nil {
		t.Fatal("the production translator is not told where to send session starts and audio")
	}
	opts.Callbacks.OnSessionStart()
	opts.Callbacks.OnAudio(make([]byte, 480))

	player.mu.Lock()
	marks := player.sessionMarks
	player.mu.Unlock()
	if speech, _ := player.speechCounts(); marks != 1 || speech != 1 {
		t.Errorf("session starts %d, speech writes %d reached the player, want 1 and 1", marks, speech)
	}
}
