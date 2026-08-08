package ui

import (
	"image/color"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/dennis2lee/live-translation/src_go/internal/config"
)

// openPrefs builds the dialog's state and widgets without showing it, so the
// Apply/OK/Cancel contract can be exercised without a display.
func openPrefs(t *testing.T, w *Window) *prefsDialog {
	t.Helper()
	p := &prefsDialog{
		win:       w,
		original:  w.settings,
		storedKey: config.StoredAPIKey(),
		working:   w.settings,
		levelStop: make(chan struct{}),
	}
	// The tabs are what construct the widgets that collect() reads back.
	p.apiTab()
	p.audioTab()
	p.networkTab()
	p.vocabularyTab()
	p.appearanceTab()
	p.behaviourTab()
	t.Cleanup(p.close)
	return p
}

// Apply applies and leaves the dialog open; the window keeps the new settings.
func TestApplyAppliesAndKeepsWorking(t *testing.T) {
	w := newTestWindow(t)
	p := openPrefs(t, w)

	p.fontSize.SetValue(72)
	p.maxLines.SetSelected("4")
	p.completeOnly.SetChecked(true)
	p.apply()

	if w.settings.FontSize != 72 {
		t.Errorf("FontSize = %d after Apply", w.settings.FontSize)
	}
	if w.settings.MaxLines != 4 {
		t.Errorf("MaxLines = %d after Apply", w.settings.MaxLines)
	}
	if !w.settings.CompletePhrasesOnly {
		t.Error("CompletePhrasesOnly did not apply")
	}
	// The dialog stays usable: a second Apply on top of the first works.
	p.fontSize.SetValue(56)
	p.apply()
	if w.settings.FontSize != 56 {
		t.Errorf("FontSize = %d after a second Apply", w.settings.FontSize)
	}
}

// Cancel after Apply puts back exactly what was in force when the dialog
// opened — that is the whole reason the dialog keeps `original`.
func TestCancelUndoesAnApply(t *testing.T) {
	w := newTestWindow(t)
	before := w.settings
	p := openPrefs(t, w)

	p.fontSize.SetValue(96)
	p.completeOnly.SetChecked(!before.CompletePhrasesOnly)
	p.apply()
	if w.settings == before {
		t.Fatal("Apply changed nothing, so Cancel has nothing to undo")
	}

	p.cancel()
	if w.settings != before {
		t.Errorf("Cancel left the settings at\n %+v\nwant\n %+v", w.settings, before)
	}
}

// Cancel without Apply must not touch anything, including the key store.
func TestCancelWithoutApplyIsANoOp(t *testing.T) {
	w := newTestWindow(t)
	if err := config.SaveAPIKey("original-key", false); err != nil {
		t.Fatal(err)
	}
	before := w.settings
	p := openPrefs(t, w)
	p.fontSize.SetValue(24) // typed, never applied
	p.cancel()

	if w.settings != before {
		t.Error("Cancel changed the settings although Apply was never pressed")
	}
	if got := config.StoredAPIKey(); got != "original-key" {
		t.Errorf("StoredAPIKey = %q", got)
	}
}

// Applying a key writes it to the encrypted store, and Cancel puts the
// previous one back. A Keychain copy is deliberately *not* undone — there is
// no way to tell a fresh copy from one that was already there.
func TestCancelRestoresTheKeyStore(t *testing.T) {
	w := newTestWindow(t)
	if err := config.SaveAPIKey("original-key", false); err != nil {
		t.Fatal(err)
	}
	p := openPrefs(t, w)

	p.apiKey.SetText("typed-by-mistake")
	p.apply()
	if got := config.StoredAPIKey(); got != "typed-by-mistake" {
		t.Fatalf("StoredAPIKey = %q after Apply", got)
	}
	// The field is cleared once applied, so the key is not left on screen.
	if p.apiKey.Text != "" {
		t.Errorf("the key field still holds %q after Apply", p.apiKey.Text)
	}

	p.cancel()
	if got := config.StoredAPIKey(); got != "original-key" {
		t.Errorf("StoredAPIKey = %q after Cancel, want the original back", got)
	}
}

// Cancel must also cope with there having been no key at all: restoring
// "nothing" empties the store rather than writing an empty string.
func TestCancelRestoresAnEmptyStore(t *testing.T) {
	w := newTestWindow(t)
	p := openPrefs(t, w)
	p.apiKey.SetText("AIzaSyExampleNotARealKey")
	p.apply()
	if config.StoredAPIKey() == "" {
		t.Fatal("Apply did not store the key")
	}
	p.cancel()
	if got := config.StoredAPIKey(); got != "" {
		t.Errorf("StoredAPIKey = %q, want nothing", got)
	}
}

// Everything the six tabs collect must survive a round trip through Apply.
func TestApplyCollectsEveryTab(t *testing.T) {
	w := newTestWindow(t)
	p := openPrefs(t, w)

	p.silenceGate.SetChecked(true)
	p.vocabSource.SetText("영업이익률\n분기 실적")
	p.vocabCaption.SetText("Pyeongwoo Lee")
	p.fontSize.SetValue(56)
	p.maxLines.SetSelected("2")
	p.bgColour.set(color.NRGBA{R: 0x10, G: 0x20, B: 0x30, A: 0xFF})
	p.boxColour.set(color.NRGBA{R: 0x2B, G: 0x5E, B: 0x94, A: 0xFF})
	p.textColour.set(color.NRGBA{R: 0xFF, G: 0xE0, B: 0x00, A: 0xFF})
	p.boxOpacity.SetValue(0.8)
	p.cornerRadius.SetValue(12)
	p.boxWidth.SetValue(80)
	p.bottomMargin.SetValue(10)
	p.boxPosition.SetSelected("Middle")
	p.fixedBox.SetChecked(false)
	p.sourceText.SetChecked(true)
	p.directionHint.SetChecked(false)
	p.clearAfter.SetValue(5)
	p.lineHold.SetValue(1.5)
	p.completeOnly.SetChecked(true)
	p.saveScript.SetChecked(false)
	p.transcriptDir.SetText("/tmp/somewhere")
	p.debugLogging.SetChecked(true)
	p.apply()

	s := w.settings
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"SilenceGate", s.SilenceGate, true},
		{"VocabularySource", s.VocabularySource, "영업이익률\n분기 실적"},
		{"VocabularyCaptions", s.VocabularyCaptions, "Pyeongwoo Lee"},
		{"FontSize", s.FontSize, 56},
		{"MaxLines", s.MaxLines, 2},
		{"BgColor", s.BgColor, "#102030"},
		{"BoxColor", s.BoxColor, "#2B5E94"},
		{"TextColor", s.TextColor, "#FFE000"},
		{"BoxOpacity", s.BoxOpacity, 0.8},
		{"CornerRadius", s.CornerRadius, 12},
		{"BoxMaxWidthPct", s.BoxMaxWidthPct, 80},
		{"BottomMarginPct", s.BottomMarginPct, 10},
		{"BoxPosition", s.BoxPosition, string(config.BoxCenter)},
		{"FixedBox", s.FixedBox, false},
		{"ShowSourceText", s.ShowSourceText, true},
		{"ShowDirectionHint", s.ShowDirectionHint, false},
		{"ClearAfterSeconds", s.ClearAfterSeconds, 5.0},
		{"LineHoldSeconds", s.LineHoldSeconds, 1.5},
		{"CompletePhrasesOnly", s.CompletePhrasesOnly, true},
		{"SaveTranscript", s.SaveTranscript, false},
		{"TranscriptDir", s.TranscriptDir, "/tmp/somewhere"},
		{"DebugLogging", s.DebugLogging, true},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	// The two vocabularies must stay separate all the way through.
	if len(s.SourceTerms()) != 2 || s.SourceTerms()[0] != "영업이익률" {
		t.Errorf("SourceTerms = %q", s.SourceTerms())
	}
	if len(s.CaptionTerms()) != 1 || s.CaptionTerms()[0] != "Pyeongwoo Lee" {
		t.Errorf("CaptionTerms = %q", s.CaptionTerms())
	}
}

// Whatever the widgets offer, what is stored is clamped.
func TestApplyClamps(t *testing.T) {
	w := newTestWindow(t)
	p := openPrefs(t, w)
	p.transcriptDir.SetText("   ")
	p.apply()
	if w.settings.TranscriptDir == "   " {
		t.Error("a blank transcript directory was stored as-is")
	}
}

// The dialog opens showing the settings in force, not the defaults.
func TestDialogOpensOnTheCurrentSettings(t *testing.T) {
	w := newTestWindow(t)
	w.update(func(s *config.Settings) {
		s.FontSize = 72
		s.MaxLines = 1
		s.BoxPosition = string(config.BoxTop)
	})
	p := openPrefs(t, w)

	if p.fontSize.Value != 72 {
		t.Errorf("font-size slider = %v", p.fontSize.Value)
	}
	if p.maxLines.Selected != strconv.Itoa(1) {
		t.Errorf("lines selector = %q", p.maxLines.Selected)
	}
	if p.boxPosition.Selected != config.BoxPositionMenuText[config.BoxTop] {
		t.Errorf("position selector = %q", p.boxPosition.Selected)
	}
}

// The key status line says where the key comes from, and never shows the key.
func TestKeyStatusNamesTheSourceNotTheKey(t *testing.T) {
	w := newTestWindow(t)
	p := openPrefs(t, w)
	if p.keyStatus.Text == "" {
		t.Fatal("the key status is blank")
	}

	const secret = "AIzaSyExampleNotARealKey"
	if err := config.SaveAPIKey(secret, false); err != nil {
		t.Fatal(err)
	}
	p.refreshKeyStatus()
	if p.keyStatus.Text != "Key in use, from: encrypted store" {
		t.Errorf("key status = %q", p.keyStatus.Text)
	}
	if strings.Contains(p.keyStatus.Text, secret) {
		t.Error("the key status shows the key itself")
	}
}

// --- the level meter ------------------------------------------------------

// The bar is dBFS, not linear. A linear bar was the bug: a normal speaking peak
// of -36 dBFS is 0.016 of full scale, so it sat at 1.6% and looked identical to
// a dead input, which is exactly the distinction the meter exists to make.
func TestMeterScaleIsDBFS(t *testing.T) {
	cases := []struct {
		db      float64
		wantPct int
		what    string
	}{
		{-88, 0, "nothing routed in"},
		{-60, 0, "the floor"},
		{-54, 10, "SilentInputPeak"},
		{-36, 40, "built-in mic in a quiet room"},
		{-26, 56, "SpeechPeak"},
		{-12, 80, "the target"},
		{0, 100, "clipping"},
	}
	for _, c := range cases {
		peak := math.Pow(10, c.db/20)
		got := int(peakToFraction(peak) * 100)
		if got != c.wantPct {
			t.Errorf("%.0f dBFS (%s): bar at %d%%, want %d%%",
				c.db, c.what, got, c.wantPct)
		}
	}
	if got := peakToFraction(0); got != 0 {
		t.Errorf("silence: bar at %v, want 0", got)
	}
	// Nothing may leave the bar's range, whatever arrives.
	for _, peak := range []float64{-1, 0, 1e-12, 1, 4} {
		if f := peakToFraction(peak); f < 0 || f > 1 {
			t.Errorf("peak %v mapped outside 0..1: %v", peak, f)
		}
	}
}

// The verdict has to be actionable — a number alone does not say turn it up.
func TestMeterVerdictSaysWhatToDo(t *testing.T) {
	cases := []struct {
		db   float64
		want string
	}{
		{-88, "silent"},
		{-40, "quiet"},
		{-20, "good"},
		{-1, "too loud"},
	}
	for _, c := range cases {
		got := peakVerdict(math.Pow(10, c.db/20))
		if !strings.Contains(got, c.want) {
			t.Errorf("%.0f dBFS: %q does not say %q", c.db, got, c.want)
		}
		if !strings.Contains(got, "dBFS") {
			t.Errorf("%.0f dBFS: %q omits the number", c.db, got)
		}
	}
	if got := peakVerdict(0); got != "no signal" {
		t.Errorf("silence: %q, want %q", got, "no signal")
	}
}

// The button is a toggle, and it was not one: the session-observing path left
// meter nil, so pressing Stop started the test again instead of stopping it.
func TestLevelTestButtonTogglesWhenObserving(t *testing.T) {
	w := newTestWindow(t)
	p := openPrefs(t, w)

	if p.meterRunning() {
		t.Fatal("the meter is running before the button is pressed")
	}
	p.observing = true // as startMeter leaves it with a session running
	p.levelButton.OnTapped()
	if p.meterRunning() {
		t.Error("pressing Stop left the meter running")
	}
	if p.levelButton.Text != "Start Level Test" {
		t.Errorf("button says %q after stopping", p.levelButton.Text)
	}
}

// A dismissed dialog must not leave the device open for the life of the process.
func TestCloseReleasesTheMeter(t *testing.T) {
	w := newTestWindow(t)
	p := openPrefs(t, w)
	p.observing = true
	p.close()
	if p.meterRunning() {
		t.Error("close() left the meter running")
	}
}

// The session can start or stop underneath a running level test at any tick,
// and both directions matter: missing the first holds two streams open on one
// device, missing the second leaves the dialog claiming to listen to a session
// that has ended, reading "no signal" off a working microphone.
func TestMeterFollowsTheSessionBothWays(t *testing.T) {
	cases := []struct {
		ownMeter, observing, sessionRunning bool
		want                                meterAction
	}{
		{false, false, false, meterKeep},              // idle, no session
		{false, false, true, meterKeep},               // test not running; session is none of our business
		{true, false, false, meterKeep},               // our own device, quiet session: steady state
		{true, false, true, meterHandToSession},       // session started under us
		{false, true, true, meterKeep},                // observing a live session: steady state
		{false, true, false, meterReclaimFromSession}, // session ended under us
		{true, true, true, meterHandToSession},        // contradictory state resolves toward the session
		{true, true, false, meterKeep},                // own meter wins once the session is gone
	}
	for _, c := range cases {
		got := meterTransition(c.ownMeter, c.observing, c.sessionRunning)
		if got != c.want {
			t.Errorf("meterTransition(own=%v, observing=%v, session=%v) = %v, want %v",
				c.ownMeter, c.observing, c.sessionRunning, got, c.want)
		}
	}
}

// The picker's alpha never reaches the stored colour: transparency belongs to
// the Box opacity slider, exactly as in the Python dialog.
func TestColourPickDropsAlpha(t *testing.T) {
	w := newTestWindow(t)
	p := openPrefs(t, w)
	p.boxColour.set(color.NRGBA{R: 0x80, G: 0x40, B: 0x20, A: 0x30})
	if p.boxColour.value != "#804020" {
		t.Errorf("value = %q, want #804020", p.boxColour.value)
	}
	if p.boxColour.label.Text != "#804020" {
		t.Errorf("label = %q", p.boxColour.label.Text)
	}
}

// Choosing a colour must be one click on a swatch, not a trip through RGB
// sliders — and the swatch in force has to be the one that reads as chosen.
func TestColourPresetsAreOneClick(t *testing.T) {
	w := newTestWindow(t)
	p := openPrefs(t, w)

	if len(p.boxColour.swatches) != len(boxPresets) {
		t.Fatalf("%d swatches for %d presets", len(p.boxColour.swatches), len(boxPresets))
	}
	// The default is the first box preset, so it starts ringed.
	if !p.boxColour.swatches[0].selected {
		t.Errorf("the preset in force is not marked selected (value %q)", p.boxColour.value)
	}

	p.boxColour.swatches[3].Tapped(nil)
	if p.boxColour.value != boxPresets[3].hex {
		t.Errorf("tapping a swatch set %q, want %q", p.boxColour.value, boxPresets[3].hex)
	}
	if p.boxColour.swatches[0].selected || !p.boxColour.swatches[3].selected {
		t.Error("the ring did not move to the swatch that was tapped")
	}

	// A custom value rings nothing rather than lying about which preset is on.
	p.boxColour.setHex("#123456")
	for i, sw := range p.boxColour.swatches {
		if sw.selected {
			t.Errorf("swatch %d still ringed for a custom value", i)
		}
	}

	// And it still survives Apply.
	p.apply()
	if w.settings.BoxColor != "#123456" {
		t.Errorf("BoxColor = %q", w.settings.BoxColor)
	}
}

// Every preset must be a colour the settings layer will accept, or a click
// would silently fall back to the default.
func TestPresetsAreValidHex(t *testing.T) {
	for _, set := range [][]preset{bgPresets, boxPresets, textPresets} {
		for _, pre := range set {
			if got := config.Default().Clamped(); got.BgColor == "" {
				t.Fatal("unexpected")
			}
			s := config.Default()
			s.BgColor, s.BoxColor, s.TextColor = pre.hex, pre.hex, pre.hex
			c := s.Clamped()
			if c.BgColor != pre.hex || c.BoxColor != pre.hex || c.TextColor != pre.hex {
				t.Errorf("preset %q (%s) did not survive Clamped: %q",
					pre.name, pre.hex, c.BgColor)
			}
		}
	}
}
