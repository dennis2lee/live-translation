package ui

import (
	"testing"

	"github.com/dennis2lee/live-translation/src_go/internal/config"
)

// With no voice chosen the model's audio is what plays. This is the 1.2.0
// behaviour and it has to survive the local voice being added.
func TestTheModelsAudioPlaysWhenNoVoiceIsChosen(t *testing.T) {
	w, factory := speakingWindow(t)
	w.startPlayer()
	player := factory.last()
	if player == nil {
		t.Fatal("no player was built")
	}

	w.speakAudio([]byte{1, 2, 3, 4})
	player.mu.Lock()
	got := len(player.written)
	player.mu.Unlock()
	if got != 1 {
		t.Errorf("player received %d blobs, want the model's audio", got)
	}
}

// With a voice chosen the model's audio must not reach the player — the
// chosen voice is speaking instead, and playing both would be two voices at
// once. It is still *received*: failing to read it stalls the stream, which is
// why this drops at the player rather than at the request.
func TestTheModelsAudioIsDroppedWhenAVoiceIsChosen(t *testing.T) {
	w, factory := speakingWindow(t)
	w.settings.TargetLanguageCode = "en"
	w.settings = w.settings.WithVoiceFor("en", "Samantha")
	w.startPlayer()
	player := factory.last()
	if player == nil {
		t.Fatal("no player was built")
	}
	t.Cleanup(w.stopVoice)

	if !w.voiceActive.Load() {
		t.Fatal("a voice was chosen but the local speaker did not start")
	}
	w.speakAudio([]byte{1, 2, 3, 4})
	player.mu.Lock()
	got := len(player.written)
	player.mu.Unlock()
	if got != 0 {
		t.Errorf("player received %d blobs of model audio while a voice was chosen", got)
	}
}

// The voice is stored per target language, because the direction keys switch
// the target while the app runs. An English voice reading Korean captions is
// not a voice anyone chose.
func TestTheVoiceIsRememberedPerTargetLanguage(t *testing.T) {
	s := config.Default()
	s = s.WithVoiceFor("en", "Samantha")
	s = s.WithVoiceFor("ko", "Yuna")

	if got := s.VoiceFor("en"); got != "Samantha" {
		t.Errorf("VoiceFor(en) = %q, want Samantha", got)
	}
	if got := s.VoiceFor("ko"); got != "Yuna" {
		t.Errorf("VoiceFor(ko) = %q, want Yuna", got)
	}
	// The target arrives as a bare code, but a region-qualified one must find
	// the same voice rather than silently falling back to the model's.
	if got := s.VoiceFor("en-US"); got != "Samantha" {
		t.Errorf("VoiceFor(en-US) = %q, want Samantha", got)
	}
	if got := s.VoiceFor("fr"); got != "" {
		t.Errorf("VoiceFor(fr) = %q, want the model's voice", got)
	}
}

// WithVoiceFor must not write through to the settings it was called on: the
// dialog keeps the original Settings across exec() so Cancel can put them
// back, and a shared map would revert to the value it was meant to undo.
func TestSettingAVoiceDoesNotMutateTheOriginal(t *testing.T) {
	original := config.Default().WithVoiceFor("en", "Samantha")
	changed := original.WithVoiceFor("en", "Daniel")

	if got := original.VoiceFor("en"); got != "Samantha" {
		t.Errorf("the original moved to %q — Cancel could not put it back", got)
	}
	if got := changed.VoiceFor("en"); got != "Daniel" {
		t.Errorf("the copy has %q, want Daniel", got)
	}
	// And clearing a voice removes it rather than storing a blank.
	cleared := changed.WithVoiceFor("en", "")
	if cleared.VoiceFor("en") != "" {
		t.Error("clearing a voice left something behind")
	}
	if !original.Equal(config.Default().WithVoiceFor("en", "Samantha")) {
		t.Error("the original is no longer what it was built as")
	}
}

// Turning speech off has to take the local voice with it, or a stopped
// session keeps talking.
func TestTurningSpeechOffStopsTheLocalVoice(t *testing.T) {
	w, _ := speakingWindow(t)
	w.settings.TargetLanguageCode = "en"
	w.settings = w.settings.WithVoiceFor("en", "Samantha")
	w.startPlayer()
	if !w.voiceActive.Load() {
		t.Fatal("setup: the local voice should be running")
	}

	before := w.settings
	w.settings.SpeakTranslation = false
	w.applySpeechChanges(before)

	if w.voiceActive.Load() {
		t.Error("the local voice is still attached after speech was turned off")
	}
	if w.voice != nil {
		t.Error("the speaker was not released")
	}
}

// Switching direction switches the voice with it, without the session having
// to be restarted for the sound's sake.
func TestChangingDirectionSwitchesToThatLanguagesVoice(t *testing.T) {
	w, _ := speakingWindow(t)
	w.settings.TargetLanguageCode = "en"
	w.settings = w.settings.WithVoiceFor("en", "Samantha").WithVoiceFor("ko", "Yuna")
	w.startPlayer()
	t.Cleanup(w.stopVoice)
	if w.voice == nil {
		t.Fatal("setup: the local voice should be running")
	}

	before := w.settings
	w.settings.TargetLanguageCode = "ko"
	w.applySpeechChanges(before)

	if w.voice == nil {
		t.Fatal("the local voice was dropped by a direction change")
	}
	if got := w.voice.Voice(); got != "Yuna" {
		t.Errorf("after switching to Korean the voice is %q, want Yuna", got)
	}
	if !w.voiceActive.Load() {
		t.Error("the voice went inactive across a direction change")
	}
}

// A language with no voice chosen falls back to the model's audio rather than
// to silence.
func TestADirectionWithNoVoiceFallsBackToTheModel(t *testing.T) {
	w, factory := speakingWindow(t)
	w.settings.TargetLanguageCode = "en"
	w.settings = w.settings.WithVoiceFor("en", "Samantha")
	w.startPlayer()
	player := factory.last()

	before := w.settings
	w.settings.TargetLanguageCode = "ko" // no Korean voice chosen
	w.applySpeechChanges(before)

	if w.voiceActive.Load() {
		t.Error("the local voice stayed attached for a language with no voice")
	}
	w.speakAudio([]byte{1, 2, 3, 4})
	player.mu.Lock()
	got := len(player.written)
	player.mu.Unlock()
	if got == 0 {
		t.Error("neither the local voice nor the model's audio is playing — that is silence")
	}
}
