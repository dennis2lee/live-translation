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

// A settings file written before the release hold existed must come back with
// the measured default, not a zero that would reopen the gate on the first
// clear frame — the exact defect 1.4.0 fixed.
func TestAnOlderSettingsFileGetsTheReleaseDefault(t *testing.T) {
	got := Settings{MusicGate: true, MusicGateThreshold: 0.5, MusicGateHold: 2.0}.Clamped()
	if got.MusicGateRelease != 1.5 {
		t.Errorf("MusicGateRelease = %v, want the measured 1.5", got.MusicGateRelease)
	}
}

func TestTheReleaseIsClampedToTheSliderRange(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{
		{0.1, 0.5}, {1.5, 1.5}, {5, 5}, {900, 5},
	} {
		if got := (Settings{MusicGateRelease: tc.in}).Clamped().MusicGateRelease; got != tc.want {
			t.Errorf("release %v normalised to %v, want %v", tc.in, got, tc.want)
		}
	}
}
