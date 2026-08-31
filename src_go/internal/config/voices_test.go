package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The chosen voice is stored per target language, because the direction keys
// switch the target while the app runs and an English voice reading Korean is
// not a voice anyone chose either.

func TestVoiceKeysAreNormalised(t *testing.T) {
	s := Settings{}.WithVoiceFor("en-US", "Samantha")
	for _, code := range []string{"en", "EN", "en-US", "en_GB", " en ", "En-x-Whatever"} {
		if got := s.VoiceFor(code); got != "Samantha" {
			t.Errorf("VoiceFor(%q) = %q, want Samantha", code, got)
		}
	}
	if got := s.VoiceFor("ko"); got != "" {
		t.Errorf("VoiceFor(ko) = %q, want the model's voice", got)
	}
}

func TestEachDirectionKeepsItsOwnVoice(t *testing.T) {
	s := Settings{}.WithVoiceFor("en", "Samantha").WithVoiceFor("ko", "Yuna")
	if got := s.VoiceFor("en"); got != "Samantha" {
		t.Errorf("VoiceFor(en) = %q", got)
	}
	if got := s.VoiceFor("ko"); got != "Yuna" {
		t.Errorf("VoiceFor(ko) = %q — setting one direction overwrote the other", got)
	}
}

// Preferences keeps the Settings it opened with so Cancel can put them back.
// If WithVoiceFor wrote through the shared map, Cancel would have nothing to
// restore — the "original" would already hold the new voice.
func TestWithVoiceForDoesNotWriteThroughToTheOriginal(t *testing.T) {
	original := Settings{}.WithVoiceFor("en", "Samantha")
	changed := original.WithVoiceFor("en", "Alex")

	if got := original.VoiceFor("en"); got != "Samantha" {
		t.Errorf("the original now reads %q — Cancel has nothing to restore", got)
	}
	if got := changed.VoiceFor("en"); got != "Alex" {
		t.Errorf("the copy reads %q, want Alex", got)
	}
}

// An empty name means the model's own voice, which is the absence of a
// choice, not a choice of "". Storing a blank would make Equal report a
// change where there is none.
func TestChoosingTheModelVoiceRemovesTheEntry(t *testing.T) {
	s := Settings{}.WithVoiceFor("en", "Samantha").WithVoiceFor("en", "")
	if got := s.VoiceFor("en"); got != "" {
		t.Errorf("VoiceFor(en) = %q after clearing", got)
	}
	if s.SpeechVoices != nil {
		t.Errorf("SpeechVoices = %v, want nil once the last entry goes", s.SpeechVoices)
	}
}

// Equal is reflect.DeepEqual, which separates a nil map from an empty one.
// applySpeechChanges must do nothing when the output settings have not moved,
// or every Apply from any tab punches a hole in the sound — so setting a voice
// and taking it away again has to compare equal to never having set one.
func TestSettingAVoiceAndClearingItComparesEqual(t *testing.T) {
	base := Default()
	round := base.WithVoiceFor("ko", "Yuna").WithVoiceFor("ko", "")
	if !base.Equal(round) {
		t.Errorf("a set-then-cleared voice does not compare equal:\n base  %#v\n round %#v",
			base.SpeechVoices, round.SpeechVoices)
	}
	if base.Equal(base.WithVoiceFor("ko", "Yuna")) {
		t.Error("Equal missed a voice change — Apply would never reach the speaker")
	}
}

func TestClampingDropsUnusableVoiceEntries(t *testing.T) {
	s := Settings{SpeechVoices: map[string]string{
		"EN-US": " Samantha ",
		"ko":    "   ",
		"":      "Yuna",
	}}.Clamped()

	if got := s.VoiceFor("en"); got != "Samantha" {
		t.Errorf("VoiceFor(en) = %q, want the trimmed name", got)
	}
	if _, ok := s.SpeechVoices["ko"]; ok {
		t.Error("a blank voice name survived clamping")
	}
	if _, ok := s.SpeechVoices[""]; ok {
		t.Error("an empty language key survived clamping")
	}
}

// A hand-edited settings file cannot break the application: there are only
// ever a couple of directions in play, so anything past the cap is corruption.
func TestClampingBoundsTheNumberOfVoices(t *testing.T) {
	voices := map[string]string{}
	for i := 0; i < maxSpeechVoices*3; i++ {
		voices[string(rune('a'+i%26))+string(rune('a'+i/26))] = "V"
	}
	got := Settings{SpeechVoices: voices}.Clamped().SpeechVoices
	if len(got) > maxSpeechVoices {
		t.Errorf("kept %d voices, cap is %d", len(got), maxSpeechVoices)
	}
}

func TestClampingLeavesNoEmptyVoiceMap(t *testing.T) {
	got := Settings{SpeechVoices: map[string]string{"": ""}}.Clamped()
	if got.SpeechVoices != nil {
		t.Errorf("SpeechVoices = %v, want nil when nothing survives", got.SpeechVoices)
	}
}

// The chosen voice has to outlive a restart — that is the whole feature. A
// map is the first field in Settings that JSON can bring back in a shape
// Equal treats as different, so the round trip is worth pinning.
func TestVoicesSurviveSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	defer SetSettingsDirForTests(dir)()

	want := Default().WithVoiceFor("en", "Samantha").WithVoiceFor("ko", "Yuna")
	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if got.VoiceFor("en") != "Samantha" || got.VoiceFor("ko") != "Yuna" {
		t.Fatalf("voices came back as %v", got.SpeechVoices)
	}
	if !got.Equal(want) {
		t.Errorf("the round trip changed the settings:\n want %v\n got  %v",
			want.SpeechVoices, got.SpeechVoices)
	}
}

// A 1.3.x settings file has no speech_voices key. It must come back with no
// voice — the model's — and compare equal to a fresh default, so opening and
// closing Preferences on an upgraded install does not reopen the device.
func TestUpgradingSettingsLeavesTheVoiceUnset(t *testing.T) {
	dir := t.TempDir()
	defer SetSettingsDirForTests(dir)()

	old := `{"font_size": 42, "speak_translation": true}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if got.SpeechVoices != nil {
		t.Errorf("SpeechVoices = %v after an upgrade, want nil", got.SpeechVoices)
	}
	if got.VoiceFor("en") != "" {
		t.Errorf("VoiceFor(en) = %q after an upgrade", got.VoiceFor("en"))
	}
}
