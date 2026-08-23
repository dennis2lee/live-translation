package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A 1.2.x settings file has no music-gate keys at all. Loading it must yield
// the defaults, not zeros — a threshold of 0 gates on everything.
func TestUpgradingSettingsKeepsTheMusicGateSane(t *testing.T) {
	dir := t.TempDir()
	restore := SetSettingsDirForTests(dir)
	defer restore()

	old := `{"font_size": 42, "silence_gate": true}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if got.MusicGate {
		t.Error("MusicGate switched itself on across an upgrade")
	}
	if got.MusicGateThreshold != 0.5 {
		t.Errorf("MusicGateThreshold = %v after upgrade, want 0.5", got.MusicGateThreshold)
	}
	if got.MusicGateHold != 2.0 {
		t.Errorf("MusicGateHold = %v after upgrade, want 2.0", got.MusicGateHold)
	}
}

func TestMusicGateTuningIsClamped(t *testing.T) {
	s := Default()
	s.MusicGateThreshold = 7.5
	s.MusicGateHold = 400
	got := s.Clamped()
	if got.MusicGateThreshold != 0.9 {
		t.Errorf("MusicGateThreshold = %v, want the 0.9 ceiling", got.MusicGateThreshold)
	}
	if got.MusicGateHold != 10 {
		t.Errorf("MusicGateHold = %v, want the 10 s ceiling", got.MusicGateHold)
	}
}
