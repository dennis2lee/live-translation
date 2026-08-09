package ui

import (
	"errors"
	"image"
	"math"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

	"github.com/dennis2lee/live-translation/src_go/internal/captions"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/credentials"
	"github.com/dennis2lee/live-translation/src_go/internal/logging"
	"github.com/dennis2lee/live-translation/src_go/internal/transcript"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
)

// newTestWindow builds a window against the test driver, with both persistence
// locations redirected. Without that, opening a window in a test would
// overwrite the preferences and the key actually in use.
func newTestWindow(t *testing.T) *Window {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(config.SetSettingsDirForTests(dir))
	t.Cleanup(credentials.SetStoreDirForTests(dir))
	t.Cleanup(logging.SetDirForTests(dir))
	// applySettings now drives the real log level, so any test that applies
	// settings can leave debug logging on for the ones that follow.
	wasDebug := logging.Debug()
	t.Cleanup(func() { logging.SetDebug(wasDebug) })
	t.Cleanup(config.SetEnvForTests(func(string) string { return "" }))
	t.Cleanup(config.SetKeychainForTests(
		func(string, string) (string, error) { return "", errors.New("no key stored") },
		func(string, string, string) error { return nil },
		func(string, string) error { return nil },
	))

	settings := config.Default()
	settings.TranscriptDir = dir
	w := NewWindow(test.NewApp(), settings)
	t.Cleanup(w.Shutdown)
	return w
}

func TestIndicatorFollowsTheState(t *testing.T) {
	w := newTestWindow(t)
	cases := []struct {
		state translator.State
		want  Indicator
	}{
		{translator.StateIdle, IndicatorIdle},
		{translator.StateConnecting, IndicatorBusy},
		{translator.StateReconnecting, IndicatorBusy},
		{translator.StateRunning, IndicatorLive},
		{translator.StateError, IndicatorError},
	}
	for _, c := range cases {
		w.onStateChanged(c.state, "")
		if got := w.view.Indicator(); got != c.want {
			t.Errorf("state %v gave indicator %v, want %v", c.state, got, c.want)
		}
	}
}

// An error must print its cause: a red dot alone is not actionable.
func TestErrorStatePrintsItsCause(t *testing.T) {
	w := newTestWindow(t)
	w.onStateChanged(translator.StateError, "API key is not valid")
	if w.view.status != "API key is not valid" {
		t.Errorf("status = %q", w.view.status)
	}
	// With no detail there is still something to read.
	w.onStateChanged(translator.StateError, "")
	if w.view.status == "" {
		t.Error("an error with no detail said nothing at all")
	}
}

// Starting with no key anywhere must fail visibly rather than silently.
func TestStartWithoutAKeyReportsIt(t *testing.T) {
	w := newTestWindow(t)
	w.StartTranslation()
	if w.state != translator.StateError {
		t.Fatalf("state = %v, want an error", w.state)
	}
	if !strings.Contains(w.errorDetail, "API key") {
		t.Errorf("errorDetail = %q", w.errorDetail)
	}
	// A failed start leaves nothing running, so Start must stay usable.
	if w.menus.start.Disabled {
		t.Error("Start was disabled after a start that never started anything")
	}
}

// The buffer outlives a session and is the one unbounded structure, so settled
// lines must be taken off it whether or not a transcript is open.
func TestLinesAreDrainedWithNoTranscript(t *testing.T) {
	w := newTestWindow(t)
	w.writer = nil
	for i := 0; i < 50; i++ {
		w.onTargetText("A whole sentence number " + strings.Repeat("x", 3) + ".")
		w.buffer.Feed(" more.", nowSeconds())
	}
	w.recordNewLines()
	if got := len(w.buffer.DrainNewCommits()); got != 0 {
		t.Errorf("%d lines were left on the buffer with no transcript open", got)
	}
}

// Clearing the display must not truncate the transcript.
func TestClearCaptionsKeepsWhatWasSaid(t *testing.T) {
	w := newTestWindow(t)
	writer := transcript.New(transcript.Options{Dir: t.TempDir()})
	writer.Open(time.Now())
	w.writer = writer
	w.startedAt = time.Now()

	w.onTargetText("A sentence that was said.")
	w.ClearCaptions()

	if writer.LinesWritten() == 0 {
		t.Error("clearing the display lost the line from the transcript")
	}
	if len(w.view.lines) != 0 {
		t.Errorf("the display still holds %d line(s)", len(w.view.lines))
	}
}

// The 9 key and the menu item must drive the same setting.
func TestCompletePhrasesToggle(t *testing.T) {
	w := newTestWindow(t)
	before := w.settings.CompletePhrasesOnly
	w.update(func(s *config.Settings) { s.CompletePhrasesOnly = !s.CompletePhrasesOnly })
	if w.settings.CompletePhrasesOnly == before {
		t.Fatal("the setting did not change")
	}
	if !w.menus.completeOnly.Checked == w.settings.CompletePhrasesOnly {
		t.Error("the menu tick did not follow the setting")
	}
	if w.buffer.CompletePhrasesOnly() != w.settings.CompletePhrasesOnly {
		t.Error("the buffer did not follow the setting")
	}
}

// The Debug logging checkbox has to take effect in this process.
//
// It used to save the setting and change nothing: the level was fixed when
// main installed the handler, so the box did nothing until the next launch and
// said nothing about it. The one thing debug logging is currently needed for is
// measuring a live session.
func TestDebugLoggingTakesEffectImmediately(t *testing.T) {
	w := newTestWindow(t)

	logging.SetDebug(false) // a known starting point, whatever ran before
	w.update(func(s *config.Settings) { s.DebugLogging = true })
	if !logging.Debug() {
		t.Error("turning the setting on did not change the running log level")
	}
	w.update(func(s *config.Settings) { s.DebugLogging = false })
	if logging.Debug() {
		t.Error("turning the setting off did not change the running log level")
	}
}

// A settings change must reach the buffer without rebuilding it, or the
// transcript loses whatever it had settled.
func TestSettingsChangeKeepsUndrainedLines(t *testing.T) {
	w := newTestWindow(t)
	// Fed straight into the buffer, not through onTargetText, which drains as
	// it goes. This is the state a settings change has to survive: settled but
	// not yet handed over.
	w.buffer.Feed("A settled sentence.", nowSeconds())
	w.update(func(s *config.Settings) { s.MaxLines = 4 })
	if got := len(w.buffer.DrainNewCommits()); got == 0 {
		t.Error("a settings change discarded a line that never reached the transcript")
	}
	if w.buffer.MaxLines() != 4 {
		t.Errorf("MaxLines = %d", w.buffer.MaxLines())
	}
}

// Direction changes come with their label, and the label must be renderable.
func TestSetDirectionUpdatesTargetAndHint(t *testing.T) {
	w := newTestWindow(t)
	w.SetDirection(config.EnglishToKorean)
	if w.settings.TargetLanguageCode != "ko" {
		t.Errorf("target = %q", w.settings.TargetLanguageCode)
	}
	if w.settings.ShowDirectionHint && w.view.hintText == "" {
		t.Error("no direction hint was shown")
	}
	if strings.Contains(w.view.hintText, " →") || strings.Contains(w.view.hintText, "→ ") {
		t.Errorf("hint %q has a space next to the arrow, which Fyne renders as tofu", w.view.hintText)
	}
}

// Shutdown is the single exit path and has to be safe to call twice.
func TestShutdownIsIdempotent(t *testing.T) {
	w := newTestWindow(t)
	w.Shutdown()
	w.Shutdown()
	if w.state != translator.StateIdle {
		t.Errorf("state after shutdown = %v", w.state)
	}
}

// A notice is wiped by the next overlay refresh unless it is given a lifetime.
func TestNoticeExpires(t *testing.T) {
	w := newTestWindow(t)
	w.showNotice("Device unavailable — using the default")
	if w.view.status == "" {
		t.Fatal("the notice was not shown")
	}
	w.noticeUntil = time.Now().Add(-time.Second)
	w.refreshOverlay()
	if w.view.status != "" {
		t.Errorf("the notice outlived its window: %q", w.view.status)
	}
}

// The tick has to read the peak exactly once: TakePeak resets the meter, so two
// readers would give one of them zero.
func TestTickWithNoCaptureIsHarmless(t *testing.T) {
	w := newTestWindow(t)
	w.onTick()
	if _, known := w.takeInputPeak(); known {
		t.Error("a peak was reported with nothing open")
	}
}

func TestDisplayLinesReachTheView(t *testing.T) {
	w := newTestWindow(t)
	w.onTargetText("Revenue is up.")
	want := w.buffer.DisplayLines()
	got := w.view.lines
	if len(got) != len(want) {
		t.Fatalf("view holds %d lines, buffer offers %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != (captions.Line{Text: want[i].Text, Settled: want[i].Settled}) {
			t.Errorf("line %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The caption background must reach the edge of the window, in every theme.
//
// Fyne insets window content by theme.Padding() on all four sides by default
// and paints the exposed strip with the canvas background, which follows the
// OS light/dark setting. On a Mac in dark mode that strip is black and hides
// inside the caption view's own black background; on Windows in light mode it
// is white, and it drew a frame around the whole display in full screen.
func TestWindowContentReachesTheEdge(t *testing.T) {
	w := newTestWindow(t)

	if w.win.Padded() {
		t.Error("the window is padded, so the canvas background shows as a frame")
	}

	// The pixel that proves it: the very corner of the canvas has to be the
	// caption background, not the theme's.
	test.ApplyTheme(t, theme.LightTheme())
	w.win.Resize(fyne.NewSize(300, 200))
	img := w.win.Canvas().Capture()
	b := img.Bounds()
	for _, p := range []image.Point{
		{X: b.Min.X, Y: b.Min.Y},
		{X: b.Max.X - 1, Y: b.Min.Y},
		{X: b.Min.X, Y: b.Max.Y - 1},
		{X: b.Max.X - 1, Y: b.Max.Y - 1},
	} {
		r, g, bl, _ := img.At(p.X, p.Y).RGBA()
		if r > 0x4000 || g > 0x4000 || bl > 0x4000 {
			t.Errorf("corner %v is (%d,%d,%d) in the light theme — the caption "+
				"background does not reach it", p, r>>8, g>>8, bl>>8)
		}
	}
}

// Every full-screen entry point funnels through applyFullScreen, and the
// setting and the window state must move together whichever way it is reached.
func TestFullScreenFunnel(t *testing.T) {
	w := newTestWindow(t)

	w.setFullScreen(true) // the f key
	if !w.settings.Fullscreen || !w.win.FullScreen() {
		t.Fatalf("after the key: setting=%v window=%v",
			w.settings.Fullscreen, w.win.FullScreen())
	}

	// Preferences Apply flips it through applySettings.
	next := w.settings
	next.Fullscreen = false
	w.applySettings(next)
	if w.settings.Fullscreen || w.win.FullScreen() {
		t.Fatalf("after Apply: setting=%v window=%v",
			w.settings.Fullscreen, w.win.FullScreen())
	}

	// Divergence — the setting already says on but the window is not — must
	// still switch the window, or the key does nothing.
	w.settings.Fullscreen = true
	w.setFullScreen(true)
	if !w.win.FullScreen() {
		t.Error("a diverged window state was not forced back to the setting")
	}
}

// The 9 key's only feedback is the hint under the caption box — the same style
// the direction announcement uses. It must say which way the toggle went, and
// it must show even with the direction hint turned off.
func TestToggleCompletePhrasesShowsTheHint(t *testing.T) {
	w := newTestWindow(t)
	w.settings.ShowDirectionHint = false

	w.ToggleCompletePhrases()
	if !w.settings.CompletePhrasesOnly {
		t.Fatal("the setting did not turn on")
	}
	if !strings.Contains(w.view.hintText, "on") {
		t.Errorf("hint %q does not say the mode turned on", w.view.hintText)
	}

	w.ToggleCompletePhrases()
	if w.settings.CompletePhrasesOnly {
		t.Fatal("the setting did not turn back off")
	}
	if !strings.Contains(w.view.hintText, "off") {
		t.Errorf("hint %q does not say the mode turned off", w.view.hintText)
	}
	if w.buffer.CompletePhrasesOnly() {
		t.Error("the buffer did not follow the toggle")
	}
}

// SpeechPeak decides whether the buffer believes anybody is talking, and that
// decision is what keeps captions on screen between sentences. It was set at
// -26 dBFS, above the level people actually speak at, so the guard never fired
// and the captions cleared under a talking speaker. Pin it against the
// measurements that set it (spikes/RESULTS.md, the level meter, 2026-08-06).
func TestSpeechPeakSitsBetweenSpeechAndAQuietRoom(t *testing.T) {
	dbfs := func(v float64) float64 { return 20 * math.Log10(v) }

	const (
		quietestSpeech = -38.4 // speech into the built-in mic
		loudestQuiet   = -53.0 // the same room with nobody talking
	)
	got := dbfs(SpeechPeak)
	if got >= quietestSpeech {
		t.Errorf("SpeechPeak is %.1f dBFS, at or above the quietest measured "+
			"speech (%.1f) — speech would read as silence", got, quietestSpeech)
	}
	if got <= loudestQuiet {
		t.Errorf("SpeechPeak is %.1f dBFS, at or below a quiet room (%.1f) — "+
			"room tone would read as speech and captions would never clear",
			got, loudestQuiet)
	}
	// And it must stay above the dead-input threshold, or the two checks
	// disagree about what silence is.
	if SpeechPeak <= SilentInputPeak {
		t.Errorf("SpeechPeak %.4f is not above SilentInputPeak %.4f",
			SpeechPeak, SilentInputPeak)
	}
}
