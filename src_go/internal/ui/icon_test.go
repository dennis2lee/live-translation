package ui

import (
	"bytes"
	"image"
	_ "image/png"
	"strings"
	"testing"
)

// The window icon is embedded, so a build with a missing or corrupt file has
// to fail here rather than at the first launch on a user's machine — where
// the symptom is a generic icon and nothing in the log.
func TestTheWindowIconIsRealPNG(t *testing.T) {
	res := AppIcon()
	if res == nil || len(res.Content()) == 0 {
		t.Fatal("the embedded window icon is empty")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(res.Content()))
	if err != nil {
		t.Fatalf("the embedded icon does not decode: %v", err)
	}
	if format != "png" {
		t.Errorf("icon format = %q, want png — Fyne decodes it with image.Decode", format)
	}
	// The art is a 32x32 grid and 128 is 4x it exactly, so the sizes Windows
	// asks for (16, 32, 64) are exact halvings and no scaler can soften a
	// pixel that was placed deliberately. A size that is not a power-of-two
	// multiple of 32 breaks that.
	if cfg.Width != 128 || cfg.Height != 128 {
		t.Errorf("icon is %dx%d, want 128x128", cfg.Width, cfg.Height)
	}
}

// A screenshot of a bug report is often all there is to say which build
// produced it.
func TestTheTitleCarriesTheVersion(t *testing.T) {
	title := WindowTitle()
	if !strings.Contains(title, AppName) {
		t.Errorf("title %q does not name the application", title)
	}
	if !strings.Contains(title, Version) {
		t.Errorf("title %q does not carry version %q", title, Version)
	}
}
