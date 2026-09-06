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
	"github.com/dennis2lee/live-translation/src_go/internal/classify"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/logging"
	"github.com/dennis2lee/live-translation/src_go/internal/platform"
	"github.com/dennis2lee/live-translation/src_go/internal/transcript"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
	"github.com/dennis2lee/live-translation/src_go/internal/tts"
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
	// fragments.
	//
	// This was 0.05 (−26 dBFS) in both builds and it was **above the level
	// people actually speak at**, so the guard never fired: measured with the
	// Preferences level meter on 2026-08-06, speech into the built-in
	// microphone peaked −32.7 to −38.4 dBFS, and CLAUDE.md records −36. The
	// buffer therefore believed the room was silent throughout a sermon and
	// wiped the captions on the 2 s timer between sentences — the reported
	// "captions go blank while I am still talking".
	//
	// −45 dBFS sits in the measured gap: about 7 dB below the quietest speech
	// and 8 dB above the loudest reading from a quiet room (−53 to −73).
	SpeechPeak = 0.0056 // about −45 dBFS
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
	// prefs is the open Preferences dialog, or nil. One at a time — see
	// OpenPreferences.
	prefs *prefsDialog

	capture    *audio.Capture
	translator *translator.Translator
	writer     *transcript.Writer

	// player speaks the translation, when SpeakTranslation is on. It is atomic
	// where every other field here is plain, because it is the one thing read
	// from a translator goroutine: OnAudio hands blobs straight to it rather
	// than through fyne.Do, which would push ten blobs a second of audio
	// through the UI thread and make the sound hostage to a repaint.
	player atomic.Pointer[playerRef]
	// newSpeaker builds that player. It is a field only so the tests can drive
	// the start/stop/reopen rules without opening a device — malgo opens real
	// hardware, and a test that needs headphones plugged in is not a test.
	newSpeaker func(deviceName string, sourceRate int) speaker
	// speechMuted is the m key. It is a runtime override on the gain and is
	// deliberately *not* persisted: Settings.SpeakTranslation is the durable
	// switch, and a second durable switch for the same thing gives a silent
	// launch no way to explain itself. A mute that outlived a restart is
	// indistinguishable from the feature being broken.
	speechMuted bool

	// musicGate filters the send path for the running session; nil when the
	// feature is off or the model could not be had. yamnet is the loaded
	// model, kept across sessions — loading dequantizes 4 M weights, and a
	// stop/start should not pay that twice.
	// voice speaks the translation locally when the user has chosen a voice.
	// voiceActive is read from the translator's receive goroutine, which is
	// why it is atomic and the pointer is not: everything that touches the
	// pointer runs on the UI goroutine.
	voice       *tts.Speaker
	voiceActive atomic.Bool

	musicGate *classify.Gate
	yamnet    *classify.Model
	// loadModel reads the model from disk. A field for the same reason
	// newSpeaker is: the tests must not read the real app-support directory,
	// and a test that needs a 4 MB download is not a unit test.
	loadModel func() (*classify.Model, error)

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
		newSpeaker: func(deviceName string, sourceRate int) speaker {
			return audio.NewPlayer(deviceName, sourceRate)
		},
	}
	w.buffer = newBuffer(settings)
	w.view = NewCaptionView(settings)

	// The version is in the title because a screenshot of a bug report is
	// often all there is to say which build produced it, and the About box is
	// two clicks away from a window that may already be misbehaving.
	w.win = app.NewWindow(WindowTitle())
	// Explicitly, on the app rather than the window, so dialogs carry it too.
	// See icon.go for why leaving it unset does not fall back to the icon
	// compiled into the executable.
	app.SetIcon(AppIcon())
	w.win.SetIcon(AppIcon())
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
		w.applyFullScreen(true)
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
			w.ToggleCompletePhrases()
		case 'm', 'M':
			w.ToggleSpeechMute()
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
	// The level changes in the running process. It used to take a relaunch,
	// with nothing on screen saying so — and the one thing debug logging is
	// currently for is measuring a live session.
	if next.DebugLogging != old.DebugLogging {
		logging.SetDebug(next.DebugLogging)
		slog.Info("debug logging changed", "on", next.DebugLogging, "log", logging.Path())
	}
	w.applyBufferSettings()
	w.view.ApplySettings(next)
	w.pushLines()
	w.syncActions()
	if next.Fullscreen != old.Fullscreen {
		w.applyFullScreen(next.Fullscreen)
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

// ToggleCompletePhrases flips the mode and says so under the caption box, in
// the same style as the direction hint. The 9 key gives no other feedback, and
// a mode that silently changes what the captions withhold looks like a caption
// bug when the key is hit by accident.
//
// Unlike the direction hint this is not gated on ShowDirectionHint: that
// setting quiets an announcement that repeats what was just clicked in a menu,
// while this one is the only confirmation a bare keypress gets.
func (w *Window) ToggleCompletePhrases() {
	w.update(func(s *config.Settings) { s.CompletePhrasesOnly = !s.CompletePhrasesOnly })
	if w.settings.CompletePhrasesOnly {
		w.view.ShowHint("Complete phrases only: on", 0)
	} else {
		w.view.ShowHint("Complete phrases only: off", 0)
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
	w.applySpeechChanges(old)
	// Tuning moves without a restart — the gate reads it per frame. Only the
	// on/off switch rebuilds the translator, below.
	if w.musicGate != nil && next.MusicGate &&
		(old.MusicGateThreshold != next.MusicGateThreshold ||
			old.MusicGateHold != next.MusicGateHold ||
			old.MusicGateRelease != next.MusicGateRelease) {
		w.musicGate.SetTuning(classify.GateTuning{
			Threshold:      next.MusicGateThreshold,
			HoldSeconds:    next.MusicGateHold,
			ReleaseSeconds: next.MusicGateRelease,
		})
	}
	if old.TargetLanguageCode != next.TargetLanguageCode ||
		old.SilenceGate != next.SilenceGate ||
		old.MusicGate != next.MusicGate ||
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

// applySpeechChanges brings the player into line with the settings, doing the
// least that will get there.
//
// The last case is the one that matters and the one that gets broken by
// accident: with the output settings unchanged this must do *nothing at all*.
// Reopening the device on every Apply would punch a hole in the sound every
// time any setting on any tab was changed.
func (w *Window) applySpeechChanges(old config.Settings) {
	next := w.settings
	player := w.player.Load()

	switch {
	case !next.SpeakTranslation:
		// Turning the feature off in Preferences is the durable decision, so
		// the transient override has nothing left to override. Leaving it set
		// would make the next Apply that turns speech back on produce silence.
		w.speechMuted = false
		w.view.SetSpeechMuted(false)
		w.stopPlayer()
	case player == nil:
		// Off→on, or a device that failed to open last time. Retrying on Apply
		// is how a user who plugged the headphones back in gets sound.
		w.startPlayer()
	case old.OutputDeviceName != next.OutputDeviceName:
		slog.Info("output device changed while running — reopening",
			"from", old.OutputDeviceName, "to", next.OutputDeviceName)
		w.stopPlayer()
		w.startPlayer()
	case old.VoiceFor(old.TargetLanguageCode) != next.VoiceFor(next.TargetLanguageCode):
		// Switching between two local voices is not worth a device restart —
		// the next line simply comes out in the new voice. Switching to or
		// from the model's voice changes which source feeds the player, so
		// the speaker has to come or go.
		switch chosen := next.VoiceFor(next.TargetLanguageCode); {
		case chosen == "":
			w.stopVoice()
		case w.voice == nil:
			w.startVoice()
		default:
			w.voice.SetVoice(chosen)
		}
	case old.SpeechVolume != next.SpeechVolume:
		// Never reopen for this. The gain is read per block, so the slider
		// takes effect on the next one with no gap.
		player.SetVolume(w.speechGain())
	}
}

// restartTranslator reopens only the translator session, keeping audio and the
// transcript.
func (w *Window) restartTranslator() {
	// The old gate's Filter is captured in the old translator's options; a
	// fresh translator needs a fresh gate or the toggle-off case would keep
	// filtering for ever.
	w.stopMusicGate()
	if w.capture == nil {
		return
	}
	apiKey := config.LoadAPIKey()
	if apiKey == "" {
		// The running session keeps translating in the old direction, so this
		// is a notice rather than an error state — but it has to be said. It
		// used to return in silence, and pressing 2 with the key forgotten
		// looked exactly like the key binding being broken.
		slog.Warn("settings change needs a session restart, but the API key is gone")
		w.showNotice("API key required — the change will apply next time you start")
		return
	}
	if w.translator != nil {
		w.translator.Stop()
		w.translator = nil
	}
	tr, err := w.newTranslator(w.capture.Ring, apiKey)
	if err != nil {
		w.stopMusicGate()
		w.failToStart(err.Error())
		return
	}
	w.translator = tr
	if err := tr.Start(); err != nil {
		w.stopMusicGate()
		w.failToStart(err.Error())
		return
	}
	w.state = translator.StateConnecting
	w.syncActions()
	w.refreshOverlay()
}

// loadYamnet loads (once) the classifier model from the app-support copy.
func (w *Window) loadYamnet() (*classify.Model, error) {
	if w.yamnet != nil {
		return w.yamnet, nil
	}
	load := w.loadModel
	if load == nil {
		load = func() (*classify.Model, error) {
			return classify.LoadModel(classify.ModelPath(platform.AppSupportDir()))
		}
	}
	model, err := load()
	if err != nil {
		return nil, err
	}
	w.yamnet = model
	return model, nil
}

// musicFilter builds the session's music gate, or nil when the feature is off.
//
// A model that is missing or corrupt must not stop the session — captions are
// the product, the gate is a refinement. It logs, says so in the caption
// area, and the session runs ungated.
func (w *Window) musicFilter() func([]byte) [][]byte {
	if !w.settings.MusicGate {
		return nil
	}
	model, err := w.loadYamnet()
	if err != nil {
		slog.Error("music gate unavailable", "err", err)
		w.showNotice("Music filter unavailable — translating everything. Re-download the model in Preferences.")
		return nil
	}
	w.musicGate = classify.NewGate(model, classify.GateTuning{
		Threshold:      w.settings.MusicGateThreshold,
		HoldSeconds:    w.settings.MusicGateHold,
		ReleaseSeconds: w.settings.MusicGateRelease,
	})
	return w.musicGate.Filter
}

// stopMusicGate retires the session's gate and writes its verdict to the log.
func (w *Window) stopMusicGate() {
	if w.musicGate == nil {
		return
	}
	gated, flushed := w.musicGate.GatedMS()
	// gated−flushed is what was never sent — audio the session did not pay
	// for. Flushed audio was sent late, by a reopening.
	slog.Info("music gate closed out", "suppressed_ms", gated-flushed, "flushed_ms", flushed)
	w.musicGate.Close()
	w.musicGate = nil
	w.view.SetMusicGated(false)
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
		Filter:      w.musicFilter(),
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
			// The exception to the rule above, deliberately — see speakAudio.
			OnAudio: w.speakAudio,
		},
	})
}

// speakAudio hands one blob of translated speech to the player.
//
// This is the one callback that does not hop to the UI goroutine, and that is
// the point: it touches no widget, and it must not queue behind a repaint.
// Ten blobs a second through fyne.Do would make the sound hostage to the
// drawing. The player has its own jitter buffer and its own device thread, so
// the fastest thing to do here is hand the bytes over and return.
//
// With no player — speech switched off, or a device that would not open — the
// bytes go where they went before 1.2.0, which is nowhere.
func (w *Window) speakAudio(pcm []byte) {
	// With a chosen voice the model's audio is dropped here rather than never
	// requested: it is generated whether or not anyone listens, and failing to
	// *read* it stalls the stream. Reading and discarding is the contract.
	if w.voiceActive.Load() {
		return
	}
	if p := w.player.Load(); p != nil {
		p.Write(pcm)
	}
}

// startVoice attaches the local synthesizer to the open player, when the user
// has chosen a voice instead of the model's.
//
// Failure is never fatal, and it is never silent either: falling back to the
// model's voice without saying so would look like the setting did nothing.
func (w *Window) startVoice() {
	want := w.settings.VoiceFor(w.settings.TargetLanguageCode)
	if w.voice != nil || want == "" {
		return
	}
	if !tts.Available() {
		slog.Warn("local speech synthesis unavailable — using the model's voice")
		w.showNotice("This machine has no local speech synthesis — using the model's voice")
		return
	}
	w.voice = tts.NewSpeaker(want, func(pcm []byte) {
		if p := w.player.Load(); p != nil {
			p.Write(pcm)
		}
	})
	w.voiceActive.Store(true)
	slog.Info("speaking the translation locally", "voice", want,
		"target", w.settings.TargetLanguageCode)
}

// stopVoice detaches it, and hands the output path back to the model's audio.
func (w *Window) stopVoice() {
	w.voiceActive.Store(false)
	if w.voice != nil {
		guard("stopping local speech", w.voice.Stop)
		w.voice = nil
	}
}

// speakLines sends newly committed caption lines to the local voice. Called
// with every drain, so the queue's own bound is what keeps it from growing.
func (w *Window) speakLines(lines []string) {
	if w.voice == nil {
		return
	}
	for _, line := range lines {
		w.voice.Say(line)
	}
}

// speaker is the output player as the window uses it.
type speaker interface {
	Start() (audio.OutputDevice, error)
	Stop()
	Write(pcm []byte)
	SetVolume(v float64)
}

// playerRef boxes the interface so it can live in an atomic.Pointer.
type playerRef struct{ speaker }

// startPlayer opens the output device, when speech is switched on and a session
// is running.
//
// Failure is never fatal. Captions are the product; sound is an extra, and an
// output device that will not open must not take the session down with it.
func (w *Window) startPlayer() {
	if !w.settings.SpeakTranslation || w.capture == nil {
		return
	}
	if w.player.Load() != nil {
		return
	}
	player := w.newSpeaker(w.settings.OutputDeviceName, audio.TranslatedRate)
	// Muted before the session started stays muted, or m would silently undo
	// itself at every reconnect-shaped restart.
	player.SetVolume(w.speechGain())
	dev, err := player.Start()
	if err != nil {
		slog.Error("could not open the output device", "err", err)
		w.showNotice("Cannot speak the translation: " + err.Error())
		return
	}
	if w.settings.OutputDeviceName != "" && dev.Name != w.settings.OutputDeviceName {
		slog.Warn("requested output device unavailable", "using", dev.Name)
		w.showNotice(w.settings.OutputDeviceName + " unavailable — playing through " + dev.Name)
	}
	w.player.Store(&playerRef{player})
	w.startVoice()
}

// speechGain is the volume the player should actually run at. Every place that
// sets a gain goes through here, so muting cannot be undone by an unrelated
// Apply and unmuting cannot resurrect a stale volume.
func (w *Window) speechGain() float64 {
	if w.speechMuted {
		return 0
	}
	return w.settings.SpeechVolume
}

// ToggleSpeechMute silences the spoken translation without closing the device.
//
// Gain rather than Stop: reopening an output device takes long enough to clip
// the first word, and mute is pressed when someone walks in — it has to be
// instant in both directions. Captions are unaffected, which is the point.
func (w *Window) ToggleSpeechMute() {
	if !w.settings.SpeakTranslation {
		// Saying nothing here looks like the key is dead. It is not — there is
		// simply nothing to mute.
		w.showNotice("Speaking the translation is off — turn it on in Preferences → Audio")
		return
	}
	w.speechMuted = !w.speechMuted
	if player := w.player.Load(); player != nil {
		player.SetVolume(w.speechGain())
	}
	w.view.SetSpeechMuted(w.speechMuted)
	slog.Info("speech mute toggled", "muted", w.speechMuted)
	if w.speechMuted {
		w.showNotice("Speech muted — press m to unmute")
	} else {
		w.showNotice("Speech unmuted")
	}
}

// stopPlayer releases the output device. Safe when nothing is playing.
func (w *Window) stopPlayer() {
	// The voice first: it writes into the player, and stopping the device out
	// from under a goroutine that is mid-utterance is the shape of bug M3.
	w.stopVoice()
	if player := w.player.Swap(nil); player != nil {
		guard("stopping audio playback", player.Stop)
	}
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
		// newTranslator may already have built the gate; its worker must not
		// outlive the session that never was.
		w.stopMusicGate()
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

	// Before tr.Start, so the first blob of translated speech already has
	// somewhere to go.
	w.startPlayer()

	if err := tr.Start(); err != nil {
		w.stopMusicGate()
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
	// After the translator, so nothing is still calling Filter.
	guard("stopping the music gate", w.stopMusicGate)
	// After the translator, so nothing is still writing into it.
	w.stopPlayer()
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
	// Spoken before the writer check: draining is unconditional, and so is
	// speaking. Returning early on a missing writer is what once let
	// _new_commits grow for as long as the application ran.
	w.speakLines(lines)
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
	w.view.SetMusicGated(w.musicGate != nil && w.musicGate.Closed())
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
	// applySettings switches the window when the setting changed. When it did
	// not — the setting and the window state have somehow diverged — force the
	// window to match anyway, or the key would do nothing.
	if w.win.FullScreen() != on {
		w.applyFullScreen(on)
	}
}

// applyFullScreen switches the window and keeps the in-window menu in step.
// Every full-screen entry point funnels through here: the `f` and Escape keys,
// the menu item, Preferences Apply, and start-up all reached SetFullScreen
// separately once, and the path that skipped the menu sync shipped the strip.
//
// The order is the second Windows finding (see spikes/RESULTS.md). On the way
// in the menu comes off *before* the mode switch, so the full-screen layout is
// computed without it; on the way out the menu goes back *after*, and Fyne's
// non-nil menu path re-lays the content out itself.
func (w *Window) applyFullScreen(on bool) {
	if on {
		w.syncMenuVisibility(true)
		w.win.SetFullScreen(true)
		return
	}
	w.win.SetFullScreen(false)
	w.syncMenuVisibility(false)
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
		// Fyne's setMenuOverlay(nil) clears the menu but skips the content
		// re-layout — the re-layout sits behind an `if c.menu != nil` guard
		// (glfw/canvas.go). Left alone, the content stays shifted down by the
		// old menu height and the canvas background shows as a strip across
		// the top: the second white bar in the Windows photographs. Reclaim
		// the space explicitly; a later resize recomputes from scratch and
		// agrees, since the menu really is gone.
		c := w.win.Canvas()
		c.Content().Resize(c.Size())
		c.Content().Move(fyne.NewPos(0, 0))
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
		// Last, so everything above is on disk before the file goes.
		guard("closing the log file", logging.Close)
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

// processStart anchors nowSeconds, so it runs on the monotonic clock.
var processStart = time.Now()

// nowSeconds is the caption buffer's clock. Every decision it drives is a
// difference — the idle commit, the duplicate window, the line hold, the clear
// countdown — so only the rate matters, never the epoch.
//
// It used to read time.Now().UnixNano(), which is wall time: an NTP step
// mid-session moved every one of those deadlines at once, settling or holding
// text for a reason nothing on screen could explain. time.Since carries the
// monotonic reading, which no clock adjustment touches.
func nowSeconds() float64 { return time.Since(processStart).Seconds() }

// deviceLabel is the menu text for one input device.
func deviceLabel(d audio.InputDevice) string {
	return fmt.Sprintf("%s  —  %dch / %.0f Hz", strings.TrimSpace(d.Name),
		d.Channels, d.DefaultSampleRate)
}
