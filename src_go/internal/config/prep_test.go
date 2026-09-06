package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAdaptiveSessionPrepIsOnByDefault(t *testing.T) {
	if !Default().AdaptiveSessionPrep {
		t.Error("AdaptiveSessionPrep defaults to off — it is meant to ship on")
	}
	if !Default().Clamped().AdaptiveSessionPrep {
		t.Error("Clamped turned it off")
	}
}

// The trap this guards: a bool that defaults to true reads back as false from
// a settings file written before the key existed, because absent and false are
// the same zero. Load unmarshals over the defaults, which is what saves it —
// and this is the test that keeps that true.
func TestAnOlderSettingsFileKeepsAdaptivePrepOn(t *testing.T) {
	dir := t.TempDir()
	defer SetSettingsDirForTests(dir)()

	old := `{"font_size": 42, "music_gate": true}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Load(); !got.AdaptiveSessionPrep {
		t.Error("an upgrade switched adaptive session prep off")
	}
}

// Off has to survive a round trip, or the setting is not a setting.
func TestAdaptivePrepSurvivesSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	defer SetSettingsDirForTests(dir)()

	s := Default()
	s.AdaptiveSessionPrep = false
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if Load().AdaptiveSessionPrep {
		t.Error("switching it off did not survive a save and load")
	}
}
