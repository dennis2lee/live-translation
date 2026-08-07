package ui

import (
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
	"github.com/dennis2lee/live-translation/src_go/internal/captions"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/platform"
	"github.com/dennis2lee/live-translation/src_go/internal/transcript"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
)

const (
	tickInterval  = 100 * time.Millisecond
	noticeSeconds = 6 * time.Second

	// SilentInputSeconds: a device routed to silence opens and streams happily,
	// so it looks exactly like nobody talking. Warn once the input has stayed
	// below the noise floor of a live microphone for this long. Measured: a
	// quiet room reads about −36 dBFS on a built-in mic, while an input with
	// nothing routed to it reads −88.
	SilentInputSeconds = 12 * time.Second
	SilentInputPeak    = 0.002 // about −54 dBFS

	// SpeechPeak: above this the input counts as somebody speaking, which holds
	// the captions on screen even while the model has stopped sending
	// fragments. Well clear of the −36 dBFS a quiet room reads, so room noise
	// does not keep stale captions up for ever.
	SpeechPeak = 0.05 // about −26 dBFS
)

// FontSizeChoices and LineCountChoices are the menu presets.
var (
	FontSizeChoices  = []int{24, 32, 44, 56, 72, 96}
	LineCountChoices = []int{1, 2, 3, 4}
)

var statusText = map[translator.State]string{
	translator.StateIdle:         "Idle",
	translator.StateConnecting:   "Connecting",
	translator.StateRunning:      "Translating",
	translator.StateReconnecting: "Reconnecting",
	translator.StateError:        "Error",
}

// indicatorForState maps a connection state to the dot. Connecting and
// reconnecting are reported by the dot, not by text in the caption area.
var indicatorForState = map[translator.State]Indicator{
	translator.StateIdle:         IndicatorIdle,
	translator.StateConnecting:   IndicatorBusy,
	translator.StateReconnecting: IndicatorBusy,
	translator.StateRunning:      IndicatorLive,
	translator.StateError:        IndicatorError,
}

// Window is the caption window and everything hanging off it.
//
// State is touched from the UI goroutine. Translator callbacks arrive on their own
// goroutines and are marshalled across with fyne.Do — the equivalent of the
// Python build's pyqtSignal bridge, and the only way in.
type Window struct {
	app fyne.App
	win fyne.Window

	settings config.Settings
	buffer   *captions.Buffer
	view     *CaptionView
	menus    *menuBar

	capture    *audio.Capture
	translator *translator.Translator
	writer     *transcript.Writer

	state       translator.State
	errorDetail string
	notice      string
	noticeUntil time.Time
	startedAt   time.Time
	displayLock platform.DisplaySleepBlocker

	loudAt       time.Time
	warnedSilent bool

	shutdownOnce sync.Once
	stopTick     chan struct{}
	// shuttingDown turns onTick into a no-op. Shutdown signals the tick and
	// never waits for it — see Shutdown for why waiting deadlocks.
	shuttingDown atomic.Bool
}

// NewWindow builds the caption window.
func NewWindow(app fyne.App, settings config.Settings) *Window {
	w := &Window{
		app:      app,
		settings: settings,
		state:    translator.StateIdle,
		stopTick: make(chan struct{}),
	}
	w.buffer = newBuffer(settings)
	w.view = NewCaptionView(settings)

	w.win = app.NewWindow(AppName)
	// No inner padding. Fyne insets the content by theme.Padding() on all four
	// sides by default and paints the exposed strip with the canvas background,
	// which follows the OS light/dark setting. On a Mac in dark mode that strip
	// is black and lands invisibly on the caption view's own black background;
	// on Windows in light mode it is white, and it framed the whole screen in
	// full screen. The caption background has to reach the edge of the display.
	w.win.SetPadded(false)
	w.win.SetContent(w.view)
	w.win.Resize(fyne.NewSize(1280, 800))
	w.win.SetCloseIntercept(func() {
		w.Shutdown()
		w.win.Close()
	})

	w.menus = newMenuBar(w)
	w.win.SetMainMenu(w.menus.menu)
	w.bindKeys()
	w.syncActions()
	w.refreshOverlay()

	if settings.Fullscreen {
		w.win.SetFullScreen(true)
		w.syncMenuVisibility(true)
	}

	// The tick starts with the event loop, not before it. A fyne.Do issued
	// before the driver runs executes inline on the calling goroutine, which
	// is exactly the unmarshalled UI access the framework warns about at
	// launch ("not been migrated to the fyne.Do threading model"). The
	// WaitGroup is armed inside the callback: under the test driver OnStarted
	// never fires, and a Wait armed for a loop that never ran would hang
	// Shutdown for ever.
	app.Lifecycle().SetOnStarted(func() { go w.tickLoop() })
	return w
}

// Show puts the window on screen and runs the event loop.
func (w *Window) Show() { w.win.ShowAndRun() }

// FyneWindow exposes the underlying window, for the signal handlers in main.
func (w *Window) FyneWindow() fyne.Window { return w.win }

func newBuffer(s config.Settings) *captions.Buffer {
	return captions.New(captions.Options{
		MaxLines:            s.MaxLines,
		Policy:              s.Policy(),
		ClearAfterSeconds:   s.ClearAfterSeconds,
		LineHoldSeconds:     s.LineHoldSeconds,
		CompletePhrasesOnly: s.CompletePhrasesOnly,
	})
}

// --- keys -----------------------------------------------------------------

// bindKeys wires the plain-digit shortcuts.
//
// Menu items cannot display a bare-digit accelerator, so the rune handler
// reproduces the behaviour and the item text notes the key — the same
// compromise the Python build makes with "(9)".
func (w *Window) bindKeys() {
	w.win.Canvas().SetOnTypedRune(func(r rune) {
		switch r {
		case '0':
			w.ToggleTranslation()
		case '1':
			w.SetDirection(config.KoreanToEnglish)
		case '2':
			w.SetDirection(config.EnglishToKorean)
		case '9':
			w.update(func(s *config.Settings) { s.CompletePhrasesOnly = !s.CompletePhrasesOnly })
		case 'f', 'F':
			// Full screen hides the menu on the platforms that draw it in the
			// window, so this is the way back out — and the only reliable one,
			// since Escape was measured not to arrive in full screen. Never
			// hide the menu without leaving a key that shows it again.
			w.setFullScreen(!w.win.FullScreen())
		}
	})
	w.win.Canvas().SetOnTypedKey(func(ev *fyne.KeyEvent) {
		// Spike F2 measured Escape not reaching this handler while full screen
		// on macOS. It works in a window, which is where it is most often
		// wanted, so it stays — but `f` above is the binding that is relied on.
		if ev.Name == fyne.KeyEscape && w.win.FullScreen() {
			w.setFullScreen(false)
		}
	})
}

// --- settings -------------------------------------------------------------

// update applies and persists a settings change without dropping the session.
func (w *Window) update(change func(*config.Settings)) {
	next := w.settings
	change(&next)
	w.applySettings(next.Clamped())
}

// applySettings takes a full settings replacement, restarting whatever part of
// a live session cannot change in place.
func (w *Window) applySettings(next config.Settings) {
	old := w.settings
	w.settings = next
	if err := config.Save(next); err != nil {
		slog.Error("could not save settings", "err", err)
	}
	w.applyBufferSettings()
	w.view.ApplySettings(next)
	w.pushLines()
	w.syncActions()
	if next.Fullscreen != old.Fullscreen {
		w.win.SetFullScreen(next.Fullscreen)
	}
	w.applySessionChanges(old)
}

// applyBufferSettings changes the buffer in place. Rebuilding it would strand
// whatever it had settled but not yet handed to the transcript.
func (w *Window) applyBufferSettings() {
	w.buffer.SetMaxLines(w.settings.MaxLines)
	w.buffer.SetCompletePhrasesOnly(w.settings.CompletePhrasesOnly)
	w.buffer.SetClearAfterSeconds(w.settings.ClearAfterSeconds)
	w.buffer.SetLineHoldSeconds(w.settings.LineHoldSeconds)
	w.buffer.SetPolicy(w.settings.Policy())
}

// --- translation control --------------------------------------------------

// ToggleTranslation is the 0 key: start when nothing is running, stop otherwise.
func (w *Window) ToggleTranslation() {
	if w.translator == nil && w.capture == nil {
		w.StartTranslation()
	} else {
		w.StopTranslation()
	}
}

// SetDirection changes the direction preset (1 = KO→EN, 2 = EN→KO).
//
// The target language is fixed when the session connects, so a live session
// reconnects the translator only. Audio capture and the ring buffer keep
// running, so nothing is lost in the gap.
func (w *Window) SetDirection(direction config.Direction) {
	target := config.DirectionTarget[direction]
	if target == w.settings.TargetLanguageCode {
		w.announceDirection() // still show it when re-selecting the same direction
		return
	}
	w.update(func(s *config.Settings) { s.TargetLanguageCode = target })
	if w.state == translator.StateIdle {
		w.announceDirection()
	}
}

func (w *Window) announceDirection() {
	if w.settings.ShowDirectionHint {
		w.view.ShowHint(config.DirectionLabelFor(w.settings.TargetLanguageCode), 0)
	}
}

// applySessionChanges restarts the affected part of a running session.
//
// The audio device is fixed when the stream opens, and the target language and
// the vocabulary hints when the session connects, so those changes cannot apply
// silently.
func (w *Window) applySessionChanges(old config.Settings) {
	if w.state == translator.StateIdle {
		return
	}
	next := w.settings
	if old.AudioDeviceName != next.AudioDeviceName {
		slog.Info("audio device changed while running — restarting capture")
		w.StopTranslation()
		w.StartTranslation()
		return
	}
	if old.TargetLanguageCode != next.TargetLanguageCode ||
		old.SilenceGate != next.SilenceGate ||
		old.EchoTargetLanguage != next.EchoTargetLanguage ||
		old.VocabularySource != next.VocabularySource ||
		old.VocabularyCaptions != next.VocabularyCaptions {
		if old.TargetLanguageCode != next.TargetLanguageCode {
			if w.writer != nil {
				w.writer.NoteDirection(config.DirectionLabelFor(next.TargetLanguageCode),
					time.Since(w.startedAt).Seconds())
			}
			w.announceDirection()
		}
		w.restartTranslator()
	}
}

// restartTranslator reopens only the translator session, keeping audio and the
// transcript.
func (w *Window) restartTranslator() {
	if w.capture == nil {
		return
	}
	apiKey := config.LoadAPIKey()
	if apiKey == "" {
		return
	}
	if w.translator != nil {
		w.translator.Stop()
		w.translator = nil
	}
	tr, err := w.newTranslator(w.capture.Ring, apiKey)
	if err != nil {
		w.failToStart(err.Error())
		return
	}
	w.translator = tr
	if err := tr.Start(); err != nil {
		w.failToStart(err.Error())
		return
	}
	w.state = translator.StateConnecting
	w.syncActions()
	w.refreshOverlay()
}

func (w *Window) newTranslator(ring *audio.Ring, apiKey string) (*translator.Translator, error) {
	return translator.New(translator.Options{
		Ring:   ring,
		APIKey: apiKey,
		Session: translator.SessionConfig{
			TargetLanguageCode: w.settings.TargetLanguageCode,
			EchoTargetLanguage: w.settings.EchoTargetLanguage,
			SourceTerms:        w.settings.SourceTerms(),
			CaptionTerms:       w.settings.CaptionTerms(),
		},
		SilenceGate: w.settings.SilenceGate,
		Callbacks: translator.Callbacks{
			// Every one of these arrives on a translator goroutine, so each
			// hops to the UI goroutine before touching anything.
			OnSource: func(text string) { fyne.Do(func() { w.view.SetSourceText(text) }) },
			OnTarget: func(text string) { fyne.Do(func() { w.onTargetText(text) }) },
			OnState: func(s translator.State, detail string) {
				fyne.Do(func() { w.onStateChanged(s, detail) })
			},
			OnReconnect:    func() { fyne.Do(w.onReconnected) },
			OnTurnComplete: func() { fyne.Do(w.onTurnComplete) },
		},
	})
}

// StartTranslation opens the audio device and the session.
func (w *Window) StartTranslation() {
	if w.state != translator.StateIdle {
		return
	}
	apiKey := config.LoadAPIKey()
	if apiKey == "" {
		w.failToStart("API key required — enter it in Preferences")
		return
	}

	capture := audio.NewCapture(w.settings.AudioDeviceName, audio.RingSeconds)
	dev, err := capture.Start()
	if err != nil {
		w.failToStart(err.Error())
		return
	}
	w.capture = capture

	if w.settings.AudioDeviceName != "" && dev.Name != w.settings.AudioDeviceName {
		slog.Warn("requested device unavailable", "using", dev.Name)
		w.showNotice(w.settings.AudioDeviceName + " unavailable — using " + dev.Name)
	}

	tr, err := w.newTranslator(capture.Ring, apiKey)
	if err != nil {
		capture.Stop()
		w.capture = nil
		w.failToStart(err.Error())
		return
	}
	w.translator = tr
	w.startedAt = time.Now()
	w.writer = nil
	if w.settings.SaveTranscript {
		writer := transcript.New(transcript.Options{
			Dir:            w.settings.TranscriptDir,
			TargetLanguage: w.settings.TargetLanguageCode,
			Model:          translator.Model,
		})
		if writer.Open(time.Now()) != "" {
			w.writer = writer
		} else {
			w.showNotice("Cannot write the transcript — check folder permissions")
		}
	}

	if err := tr.Start(); err != nil {
		w.failToStart(err.Error())
		return
	}
	w.state = translator.StateConnecting
	w.errorDetail = ""
	// Captions are for watching, not typing, so the idle timer would blank the
	// screen mid-session — on battery especially.
	w.displayLock.Acquire(platform.DefaultSleepReason)
	w.loudAt = time.Now()
	w.warnedSilent = false
	w.syncActions()
	w.refreshOverlay()
	w.announceDirection() // report the current direction on start
}

// StopTranslation closes the session and finishes the transcript.
//
// Every step is guarded: a failure to stop the translator must still let the
// audio device close and the transcript be finished, or quitting mid-session
// would leave a half-written file and an open input stream.
func (w *Window) StopTranslation() {
	reconnects := 0
	if w.translator != nil {
		reconnects = w.translator.Stats().Reconnects
	}
	tr, capture, writer := w.translator, w.capture, w.writer
	w.translator, w.capture, w.writer = nil, nil, nil

	if tr != nil {
		guard("stopping the translator", tr.Stop)
	}
	if capture != nil {
		guard("stopping audio capture", capture.Stop)
	}
	// Hand the display back to the system's idle timer.
	guard("releasing the display sleep assertion", w.displayLock.Release)

	// Make sure the final unsettled sentence reaches the transcript.
	w.buffer.Flush()
	lines := w.buffer.DrainNewCommits()
	if writer != nil {
		guard("finishing the transcript", func() {
			elapsed := time.Since(w.startedAt).Seconds()
			for _, line := range lines {
				writer.AppendLine(line, elapsed)
			}
			writer.Close(elapsed, reconnects)
		})
	}
	// With no transcript the lines are still taken off the buffer above — it
	// outlives the session, and it is the one unbounded structure.

	w.state = translator.StateIdle
	w.errorDetail = ""
	w.syncActions()
	w.refreshOverlay()
}

// failToStart reports a start-up failure through the error state and the red
// dot.
func (w *Window) failToStart(detail string) {
	w.state = translator.StateError
	w.errorDetail = detail
	w.syncActions()
	w.refreshOverlay()
}

// ClearCaptions wipes the display without losing what was said.
func (w *Window) ClearCaptions() {
	w.buffer.Clear()
	// Clear settles whatever was half-written; hand it to the transcript now
	// rather than leaving it for the next tick to find.
	w.recordNewLines()
	w.view.Clear()
	w.refreshOverlay()
}

// --- callbacks, on the UI goroutine ---------------------------------------

func (w *Window) onTargetText(text string) {
	if w.buffer.Feed(text, nowSeconds()) {
		w.pushLines()
	}
	w.recordNewLines()
}

// recordNewLines feeds settled lines to the transcript, independent of what is
// displayed.
//
// Drained even with no transcript open. The buffer holds settled lines until
// somebody takes them, and it outlives a session, so returning early here would
// let them pile up for as long as the application runs.
func (w *Window) recordNewLines() {
	lines := w.buffer.DrainNewCommits()
	if w.writer == nil {
		return // taken and discarded, so nothing accumulates
	}
	elapsed := time.Since(w.startedAt).Seconds()
	for _, line := range lines {
		w.writer.AppendLine(line, elapsed)
	}
}

func (w *Window) onReconnected() {
	w.buffer.NoteReconnect()
	if w.writer != nil && w.translator != nil {
		gaps := w.translator.Stats().GapsMS
		last := 0.0
		if len(gaps) > 0 {
			last = gaps[len(gaps)-1]
		}
		w.writer.NoteReconnect(time.Since(w.startedAt).Seconds(), last)
	}
}

// onTurnComplete settles whatever is still buffered — the server says the turn
// ended. This is what stops complete-phrases mode holding the last phrase of an
// utterance back until the idle timer catches it.
func (w *Window) onTurnComplete() {
	if w.buffer.NoteTurnComplete(nowSeconds()) {
		w.pushLines()
	}
	w.recordNewLines()
}

func (w *Window) onStateChanged(state translator.State, detail string) {
	w.state = state
	w.errorDetail = detail
	w.syncActions()
	w.refreshOverlay()
}

// --- tick -----------------------------------------------------------------

func (w *Window) tickLoop() {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			fyne.Do(w.onTick)
		case <-w.stopTick:
			return
		}
	}
}

func (w *Window) onTick() {
	if w.shuttingDown.Load() {
		return
	}
	// One reader per tick: TakePeak resets the meter, so the buffer and the
	// dead-input check have to share the same reading.
	peak, known := w.takeInputPeak()
	speech := captions.SpeechUnknown
	if known {
		if peak >= SpeechPeak {
			speech = captions.SpeechActive
		} else {
			speech = captions.SpeechQuiet
		}
	}
	if w.buffer.Tick(nowSeconds(), speech) {
		w.pushLines()
	}
	w.recordNewLines()
	if known {
		w.checkInputLevel(peak)
	}
	// A notice expires on its own, so the overlay has to be refreshed even when
	// nothing else changed.
	if w.notice != "" && time.Now().After(w.noticeUntil) {
		w.refreshOverlay()
	}
}

// takeInputPeak returns the loudest sample since the last tick, and whether
// anything is open to read from.
func (w *Window) takeInputPeak() (float64, bool) {
	if w.capture == nil || w.state == translator.StateIdle {
		return 0, false
	}
	return w.capture.TakePeak(), true
}

// checkInputLevel notes in the log when the chosen device is delivering nothing
// at all.
//
// A misrouted input streams silence rather than failing, so a dead device is
// otherwise indistinguishable from a quiet room. Deliberately *not* shown in
// the caption area: a silent input is often intentional, and a message that
// covers the captions is worse than the ambiguity.
func (w *Window) checkInputLevel(peak float64) {
	if w.capture == nil || w.state == translator.StateIdle {
		return
	}
	now := time.Now()
	if peak >= SilentInputPeak {
		w.loudAt = now
		w.warnedSilent = false
		return
	}
	if w.warnedSilent || now.Sub(w.loudAt) < SilentInputSeconds {
		return
	}
	w.warnedSilent = true
	name := w.capture.Device().Name
	if name == "" {
		name = "the input device"
	}
	slog.Warn("no signal from the input — check the device and its level",
		"device", name, "seconds", SilentInputSeconds.Seconds())
}

// --- display --------------------------------------------------------------

func (w *Window) pushLines() {
	w.view.SetLines(w.buffer.DisplayLines())
	w.refreshOverlay()
}

// showNotice shows a warning that is not an error state, for a few seconds.
//
// Without this any message set at start-up is wiped by the next overlay
// refresh, so the user never sees it.
func (w *Window) showNotice(text string) {
	w.notice = text
	w.noticeUntil = time.Now().Add(noticeSeconds)
	w.refreshOverlay()
}

// refreshOverlay updates the indicator. It never opens a modal dialog.
//
// Connecting and reconnecting are conveyed by the dot alone. Errors also print
// their cause, because a red dot is not actionable on its own.
func (w *Window) refreshOverlay() {
	indicator, ok := indicatorForState[w.state]
	if !ok {
		indicator = IndicatorIdle
	}
	w.view.SetIndicator(indicator)
	if w.notice != "" && time.Now().After(w.noticeUntil) {
		w.notice = ""
	}
	if w.state == translator.StateError {
		detail := w.errorDetail
		if detail == "" {
			detail = "Connection failed"
		}
		w.view.SetStatus(detail)
	} else {
		w.view.SetStatus(w.notice)
	}
}

func (w *Window) setFullScreen(on bool) {
	w.update(func(s *config.Settings) { s.Fullscreen = on })
	w.win.SetFullScreen(on)
	w.syncMenuVisibility(on)
}

// syncMenuVisibility takes the menu off the window in full screen, on the
// platforms that draw it there.
//
// Measured on Windows on 2026-08-06: full screen covered the taskbar and the
// title bar, but Fyne's in-window menu strip stayed on top of the captions.
// On macOS the menu lives in the system bar, which full screen hides by
// itself, and clearing it there would remove the application menu — so this
// does nothing on macOS.
//
// The menu is the mouse route out of full screen, so the `f` key exists to
// replace it. Do not remove one without the other.
func (w *Window) syncMenuVisibility(fullScreen bool) {
	if !platform.MenuIsDrawnInWindow() {
		return
	}
	if fullScreen {
		w.win.SetMainMenu(nil)
		return
	}
	w.win.SetMainMenu(w.menus.menu)
}

// OpenTranscriptDir reveals the transcript folder in the file manager.
func (w *Window) OpenTranscriptDir() {
	path := transcript.ResolveDir(w.settings.TranscriptDir)
	cmd := platform.RevealCommand(path)
	if err := exec.Command(cmd[0], cmd[1:]...).Run(); err != nil {
		slog.Error("could not open the transcript directory", "path", path, "err", err)
		w.view.SetStatus("Cannot open transcript folder: " + path)
	}
}

// --- shutdown -------------------------------------------------------------

// Shutdown quits safely even with a session open. Safe to call more than once.
//
// This is the single exit path, reached from the window closing, Quit, SIGINT
// and SIGTERM, so a live session is always closed, the audio device released
// and the transcript finished with its footer. Do not add another route that
// calls StopTranslation directly.
func (w *Window) Shutdown() {
	w.shutdownOnce.Do(func() {
		// The tick is signalled and never waited for. It may be parked inside
		// fyne.Do, which needs the UI goroutine — and Shutdown itself runs
		// *on* the UI goroutine, because that is the only thread AppKit lets
		// tear the window down. Waiting here deadlocked the first mid-session
		// SIGINT: the transcript footer was never written, which is precisely
		// what this path exists to guarantee. onTick no-ops instead.
		w.shuttingDown.Store(true)
		close(w.stopTick)
		guard("stopping translation during shutdown", w.StopTranslation)
		guard("stopping the caption animations", w.view.StopAnimations)
		guard("saving settings during shutdown", func() {
			if err := config.Save(w.settings); err != nil {
				slog.Error("could not save settings", "err", err)
			}
		})
		guard("releasing the audio backend", audio.ReleaseContext)
		// The goroutine count is the leak check the gate asks for: after a
		// clean shutdown it is the runtime's own housekeeping plus the main
		// goroutine — anything climbing session over session is a leak.
		slog.Info("shutdown complete", "goroutines", runtime.NumGoroutine())
	})
}

// guard runs a shutdown step so that one failure cannot skip the rest.
func guard(what string, step func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("error while "+what, "panic", r)
		}
	}()
	step()
}

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// deviceLabel is the menu text for one input device.
func deviceLabel(d audio.InputDevice) string {
	return fmt.Sprintf("%s  —  %dch / %.0f Hz", strings.TrimSpace(d.Name),
		d.Channels, d.DefaultSampleRate)
}
