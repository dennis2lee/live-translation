package platform

import (
	"strings"
	"testing"
)

// The hint has to name a real screen on the platform the user is on. A macOS
// path shown on Windows sends them looking for System Settings, which Windows
// does not have.
func TestTheVoiceHintNamesThisPlatformsScreen(t *testing.T) {
	for _, tc := range []struct{ goos, want string }{
		{"darwin", "System Settings"},
		{"windows", "Settings→Time & language"},
		{"linux", "Install a speech voice"},
	} {
		got := voiceInstallHintFor(tc.goos)
		if !strings.Contains(got, tc.want) {
			t.Errorf("hint for %s = %q, want it to mention %q", tc.goos, got, tc.want)
		}
	}
}

// A hint that stops at "add a voice" is not a hint. Every platform's has to
// say where.
func TestEveryPlatformGetsSomewhereToGo(t *testing.T) {
	for _, goos := range []string{"darwin", "windows", "linux", "plan9"} {
		if got := voiceInstallHintFor(goos); len(got) < 20 {
			t.Errorf("hint for %s is not actionable: %q", goos, got)
		}
	}
}

func TestVoiceInstallHintUsesTheRunningPlatform(t *testing.T) {
	defer func() { goosOverride = "" }()
	goosOverride = "windows"
	if got := VoiceInstallHint(); got != voiceInstallHintFor("windows") {
		t.Errorf("VoiceInstallHint() = %q, want the windows hint", got)
	}
}

// Fyne draws an arrow with a space after it as the arrow and a replacement
// glyph, so a path in a UI string is written "A→B", never "A → B".
func TestVoiceHintsKeepTheArrowTight(t *testing.T) {
	for _, goos := range []string{"darwin", "windows", "linux"} {
		if got := voiceInstallHintFor(goos); strings.Contains(got, " →") || strings.Contains(got, "→ ") {
			t.Errorf("hint for %s has a spaced arrow: %q", goos, got)
		}
	}
}
