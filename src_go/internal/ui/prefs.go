package ui

import (
	"context"
	"fmt"
	"image/color"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
	"github.com/dennis2lee/live-translation/src_go/internal/classify"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/logging"
	"github.com/dennis2lee/live-translation/src_go/internal/netcheck"
	"github.com/dennis2lee/live-translation/src_go/internal/platform"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
	"github.com/dennis2lee/live-translation/src_go/internal/tts"
)

// prefsDialog is the eight-tab Preferences window.
//
// Three buttons, three meanings — the contract the Python build settled on:
//
//   - Apply applies and keeps the window open;
//   - OK applies and closes;
//   - Cancel puts back the settings that were in force when the dialog opened,
//     undoing any Apply, and restores the encrypted key store with them.
//
// A Keychain copy is not undone: there is no way to tell a fresh copy from one
// that was already there.
type prefsDialog struct {
	win    *Window
	dialog dialog.Dialog

	// original is what was in force when the dialog opened, for Cancel.
	original config.Settings
	// storedKeys is the encrypted store's contents at the same moment, one
	// entry per provider: Cancel puts back every key the dialog touched.
	storedKeys map[config.Provider]string
	// dirty records that the dialog has already changed something outside
	// itself, so Cancel knows there is something to undo.
	//
	// Apply is not the only thing that sets it. Forget Key acts the moment it
	// is pressed, and while it only checked Apply, Forget→Cancel left the key
	// deleted while Forget→Apply→Cancel brought it back — the same button
	// meaning two different things depending on an unrelated press.
	dirty bool

	working config.Settings

	// Widgets whose values are read back on Apply.
	provider     *widget.Select
	keyHeading   *widget.Label
	apiKey       *widget.Entry
	toKeychain   *widget.Check
	keyStatus    *widget.Label
	deviceSelect *widget.Select
	deviceNames  []string
	level        *widget.ProgressBar
	levelStatus  *widget.Label
	levelButton  *widget.Button

	speakAloud   *widget.Check
	outputSelect *widget.Select
	outputNames  []string
	speechVolume *widget.Slider
	captionsForm *widget.Form
	coloursForm  *widget.Form
	toneButton   *widget.Button
	toneStatus   *widget.Label

	netResult    *widget.Label
	netDetail    *widget.Label
	runTest      *widget.Button
	vocabSource  *widget.Entry
	vocabCaption *widget.Entry

	bgColour   *colourPick
	boxColour  *colourPick
	textColour *colourPick

	fontSize      *widget.Slider
	maxLines      *widget.Select
	boxOpacity    *widget.Slider
	cornerRadius  *widget.Slider
	boxWidth      *widget.Slider
	bottomMargin  *widget.Slider
	boxPosition   *widget.Select
	clearAfter    *widget.Slider
	lineHold      *widget.Slider
	fixedBox      *widget.Check
	completeOnly  *widget.Check
	directionHint *widget.Check
	sourceText    *widget.Check
	silenceGate   *widget.Check
	saveScript    *widget.Check
	transcriptDir *widget.Entry
	debugLogging  *widget.Check
	adaptivePrep  *widget.Check
	warmHandover  *widget.Check

	// meter is the dialog's own capture, opened only when there is no session
	// to observe; observing says the meter is watching the session's capture
	// instead. Exactly one of them is set while the test runs — a bool rather
	// than the button's label, so the state is not stored in a UI string.
	meter     *audio.Capture
	observing bool
	lastPeak  float64
	levelStop chan struct{}
	netBusy   bool

	// tone is the dialog's own player for the output test, and toneStop ends
	// the goroutine feeding it.
	//
	// Unlike the level meter, this never borrows the session's device. Two
	// captures on one microphone is a genuine conflict, which is why the meter
	// observes instead; two streams on an output device simply mix, so opening
	// our own is free — and it is the only way to test the device the dropdown
	// is *pointing at* rather than the one already in use.
	tone     *audio.Player
	toneStop chan struct{}

	// The music filter tab.
	musicGate      *widget.Check
	musicThreshold *widget.Slider
	musicHold      *widget.Slider
	musicRelease   *widget.Slider

	voiceSelect  *widget.Select
	voiceNames   []string // parallel to voiceSelect.Options; "" is the model
	voiceSamples []string
	voiceButton  *widget.Button
	voiceNote    *widget.Label
	modelStatus  *widget.Label
	modelButton  *widget.Button
	clsButton    *widget.Button
	clsStatus    *widget.Label
	// clsCapture is the classifier test's own microphone, opened only when no
	// session is running — the same conflict rule as the level meter. clsStop
	// ends its goroutine; when a gated session is live the test observes the
	// session's own gate instead and opens nothing.
	clsCapture  *audio.Capture
	clsStop     chan struct{}
	downloading bool
}

// running reports whether the level test is on, either way round.
func (p *prefsDialog) meterRunning() bool { return p.meter != nil || p.observing }

// OpenPreferences shows the dialog, or brings the open one forward.
//
// One at a time. ⌘, works while the dialog is up, and a second dialog was a
// second level meter opening the same device, and a second Cancel whose idea of
// "the settings in force when it opened" was whatever the first one had just
// applied.
func (w *Window) OpenPreferences() {
	if w.prefs != nil {
		w.prefs.dialog.Show()
		return
	}
	p := &prefsDialog{
		win:        w,
		original:   w.settings,
		storedKeys: storedKeys(),
		working:    w.settings,
		levelStop:  make(chan struct{}),
	}
	w.prefs = p
	p.show()
}

// The dialog's size, and the room a tab actually gets inside it.
//
// Measured rather than chosen: TestNoTabOverflowsTheDialog prints what every
// tab asks for and fails if one does not fit, so these two numbers and the tab
// layout have to be changed together. prefsChrome is the tab bar plus the
// Apply/OK/Cancel row.
const (
	// 600, not 560: the eighth tab (Filter) pushed the measured tab bar to
	// 573 px. TestTheAssembledTabsFitTheDialog holds the proof.
	prefsWidth  = 600
	prefsHeight = 480

	// prefsChrome is the tab bar plus the Apply/OK/Cancel row. The bar half is
	// measured: the assembled tabs come to 386 px against a tallest tab of
	// 346, so the bar costs 40. TestTheAssembledTabsFitTheDialog checks the
	// whole thing rather than trusting this number.
	prefsChrome        = 100
	prefsContentHeight = prefsHeight - prefsChrome
)

func (p *prefsDialog) show() {
	// Eight narrow tabs rather than four wide ones. "API" and not
	// "Connection" purely for the tab bar's width: at 600 px the longer word
	// pushed Behaviour into Fyne's "…" overflow menu, which is where settings
	// go to not be found. The dialog is sized by its
	// tallest and widest tab, and with Audio at 668 px tall and Vocabulary at
	// 884 px wide, two of the six overflowed a 640x520 window — the settings
	// that did fit were made big by the ones that did not. Splitting the two
	// tallest and wrapping the labels that forced the width lets the whole
	// dialog be smaller than any of its old tabs. TestNoTabOverflowsTheDialog
	// keeps it that way.
	tabs := container.NewAppTabs(
		container.NewTabItem("API", p.connectionTab()),
		container.NewTabItem("Input", p.inputTab()),
		container.NewTabItem("Output", p.outputTab()),
		container.NewTabItem("Captions", p.captionsTab()),
		container.NewTabItem("Colours", p.coloursTab()),
		container.NewTabItem("Filter", p.filterTab()),
		container.NewTabItem("Words", p.vocabularyTab()),
		container.NewTabItem("Behaviour", p.behaviourTab()),
	)

	buttons := container.NewHBox(
		widget.NewButton("Cancel", func() {
			p.cancel()
			p.dialog.Hide()
		}),
		widget.NewButton("Apply", func() { p.apply() }),
		widget.NewButton("OK", func() {
			p.apply()
			p.dialog.Hide()
		}),
	)

	content := container.NewBorder(nil, container.NewCenter(buttons), nil, nil, tabs)
	p.dialog = dialog.NewCustomWithoutButtons("Preferences", content, p.win.win)
	p.dialog.Resize(fyne.NewSize(prefsWidth, prefsHeight))
	p.dialog.SetOnClosed(p.close)
	p.dialog.Show()
	p.startLevelMeter()
}

// close settles the background workers, so a dialog that is dismissed mid-test
// does not leave a goroutine writing into freed widgets — and releases the
// meter's device, which would otherwise stay open for the life of the process.
func (p *prefsDialog) close() {
	p.stopClassifierTest()
	select {
	case <-p.levelStop:
	default:
		close(p.levelStop)
	}
	p.stopMeter()
	p.stopTone()
	if p.win.prefs == p {
		p.win.prefs = nil
	}
}

// --- tabs -----------------------------------------------------------------

// storedKeys reads every provider's entry in the encrypted store.
func storedKeys() map[config.Provider]string {
	keys := map[config.Provider]string{}
	for _, pr := range config.Providers {
		keys[pr] = config.StoredKey(pr)
	}
	return keys
}

func (p *prefsDialog) apiTab() fyne.CanvasObject {
	options := make([]string, len(config.Providers))
	for i, pr := range config.Providers {
		options[i] = config.ProviderMenuText[pr]
	}
	p.keyHeading = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	p.apiKey = widget.NewPasswordEntry()
	p.toKeychain = widget.NewCheck("Also store in the macOS Keychain", nil)
	p.keyStatus = widget.NewLabel("")
	p.keyStatus.Wrapping = fyne.TextWrapWord
	p.provider = widget.NewSelect(options, func(string) {
		// A key typed for one provider is never saved as the other's: it
		// would be a rejected session and a key sent to the wrong company.
		p.apiKey.SetText("")
		p.showProviderKey()
	})
	p.provider.SetSelected(config.ProviderMenuText[p.working.Provider])
	p.showProviderKey()

	test := widget.NewButton("Test Key", func() { p.testKey() })
	forget := widget.NewButton("Forget Key", func() { p.forgetKey() })

	// The key is never logged and never written to the settings file — only to
	// the encrypted store, and to the Keychain when asked.
	return container.NewVBox(
		widget.NewLabelWithStyle("Provider", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.provider,
		p.keyHeading,
		p.apiKey,
		p.toKeychain,
		container.NewHBox(test, forget),
		p.keyStatus,
	)
}

// connectionTab is everything about reaching the service: the key that
// authorises it and the link that carries it. Both were their own tab and both
// were short; together they are still shorter than any other tab.
func (p *prefsDialog) connectionTab() fyne.CanvasObject {
	// Side by side rather than stacked. Both groups are narrow — 255 px each
	// against a 560 px dialog — and stacking them made this the tallest tab in
	// the dialog at 394 px, which is the one thing the split was for.
	return container.NewGridWithColumns(2, p.apiTab(), p.networkTab())
}

func (p *prefsDialog) inputTab() fyne.CanvasObject {
	devices := audio.ListInputDevices()
	p.deviceNames = []string{""}
	labels := []string{"System Default Input"}
	for _, d := range devices {
		p.deviceNames = append(p.deviceNames, d.Name)
		labels = append(labels, deviceLabel(d))
	}
	p.deviceSelect = widget.NewSelect(labels, nil)
	p.deviceSelect.SetSelectedIndex(0)
	for i, name := range p.deviceNames {
		if name == p.working.AudioDeviceName {
			p.deviceSelect.SetSelectedIndex(i)
		}
	}

	// Restarting on a device change is the whole point of the control: the
	// reason to run it is to find out which device actually carries the sound.
	p.deviceSelect.OnChanged = func(string) {
		if p.meter != nil {
			p.stopMeter()
			p.startMeter()
		}
	}

	// The meter tells "nobody is speaking" from "this device delivers nothing".
	p.level = widget.NewProgressBar()
	p.level.TextFormatter = func() string { return peakVerdict(p.lastPeak) }

	p.levelStatus = widget.NewLabel("Not running.")
	p.levelStatus.Wrapping = fyne.TextWrapWord
	p.levelButton = widget.NewButton("Start Level Test", func() {
		if p.meterRunning() {
			p.stopMeter()
		} else {
			p.startMeter()
		}
	})

	// The numbers are measured, not guessed — see spikes/RESULTS.md.
	hint := widget.NewLabel(
		"Aim for peaks around −12 dBFS. A device with nothing routed into it " +
			"sits near −88 dBFS, while a live microphone in a quiet room reads " +
			"about −36.")
	hint.Wrapping = fyne.TextWrapWord

	p.silenceGate = widget.NewCheck("Skip sending silence (saves cost)", nil)
	p.silenceGate.SetChecked(p.working.SilenceGate)

	return container.NewVBox(
		widget.NewLabel("Input device"),
		p.deviceSelect,
		widget.NewLabel("Input level"),
		p.level,
		p.levelStatus,
		container.NewHBox(p.levelButton),
		hint,
		p.silenceGate,
	)
}

// outputTab is the spoken translation. It was the lower half of the Audio tab,
// which made that tab 668 px tall on its own.
func (p *prefsDialog) outputTab() fyne.CanvasObject {
	p.speakAloud = widget.NewCheck("Speak the translation aloud", nil)
	p.speakAloud.SetChecked(p.working.SpeakTranslation)

	// The one thing a first-time user of this has to know, and it is measured,
	// not cautious: with speakers the translation is heard by the microphone
	// and translated again, and echo_target_language does not stop it.
	warning := widget.NewLabel(
		"Use headphones. Through speakers the translation returns to the " +
			"microphone and is translated again.")
	warning.Wrapping = fyne.TextWrapWord

	devices := audio.ListOutputDevices()
	p.outputNames = []string{""}
	labels := []string{"System Default Output"}
	for _, d := range devices {
		p.outputNames = append(p.outputNames, d.Name)
		labels = append(labels, outputDeviceLabel(d))
	}
	p.outputSelect = widget.NewSelect(labels, nil)
	p.outputSelect.SetSelectedIndex(0)
	for i, name := range p.outputNames {
		if name == p.working.OutputDeviceName {
			p.outputSelect.SetSelectedIndex(i)
		}
	}
	// Restarting on a device change is the point of the test: what is being
	// checked is which box the sound comes out of.
	p.outputSelect.OnChanged = func(string) {
		if p.tone != nil {
			p.stopTone()
			p.startTone()
		}
	}

	p.voiceRow()

	p.speechVolume = widget.NewSlider(0, 1)
	p.speechVolume.Step = 0.05
	p.speechVolume.Value = p.working.SpeechVolume
	volumeLabel := widget.NewLabel("")
	setVolumeLabel := func(v float64) { volumeLabel.SetText(fmt.Sprintf("%.0f%%", v*100)) }
	setVolumeLabel(p.speechVolume.Value)
	// Live only on the dialog's own test player. The session's volume moves on
	// Apply like every other setting, so Cancel has one thing to put back.
	p.speechVolume.OnChanged = func(v float64) {
		setVolumeLabel(v)
		if p.tone != nil {
			p.tone.SetVolume(v)
		}
	}

	// The key is written down here for the same reason "(9)" is written into
	// the complete-phrases check: a bare-digit or bare-letter shortcut cannot
	// be shown as a menu accelerator, so the only place a user can find out
	// about it is the label.
	muteHint := widget.NewLabel("Press m while translating to mute and unmute.")
	muteHint.Wrapping = fyne.TextWrapWord

	p.toneStatus = widget.NewLabel("Not running.")
	p.toneStatus.Wrapping = fyne.TextWrapWord
	p.toneButton = widget.NewButton("Play Test Sound", func() {
		if p.tone != nil {
			p.stopTone()
		} else {
			p.startTone()
		}
	})

	// A form rather than stacked label/widget pairs: three labelled rows cost
	// three rows here and six there, and the tab is measured against
	// prefsContentHeight by TestNoTabOverflowsTheDialog.
	form := widget.NewForm(
		widget.NewFormItem("Output device", p.outputSelect),
		widget.NewFormItem("Voice",
			container.NewBorder(nil, nil, nil, p.voiceButton, p.voiceSelect)),
		widget.NewFormItem("Volume",
			container.NewBorder(nil, nil, nil, volumeLabel, p.speechVolume)),
	)

	rows := []fyne.CanvasObject{p.speakAloud, warning, form}
	// The note is only worth a row when it has something unusual to say; in
	// the ordinary case the dropdown's own first entry says it.
	if p.voiceNote.Text != "" {
		rows = append(rows, p.voiceNote)
	}
	rows = append(rows, muteHint, container.NewHBox(p.toneButton), p.toneStatus)
	return container.NewVBox(rows...)
}

// voiceRow builds the voice picker and its Preview button.
//
// The list is filtered to the target language: the direction keys switch the
// target while the app runs, the setting is stored per language, and an
// English voice reading Korean is not a choice anyone makes on purpose.
func (p *prefsDialog) voiceRow() {
	target := p.working.TargetLanguageCode

	labels := []string{modelVoiceLabel}
	p.voiceNames = []string{""}
	p.voiceSamples = []string{""}

	note := ""
	if !tts.Available() {
		note = "This machine has no local speech synthesis, so only the model's " +
			"voice is available."
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		voices, err := tts.Voices(ctx)
		cancel()
		if err != nil {
			note = "Could not list the installed voices: " + err.Error()
		}
		for _, v := range voices {
			if !v.Matches(target) {
				continue
			}
			labels = append(labels, fmt.Sprintf("%s (%s)", v.Name, v.Language))
			p.voiceNames = append(p.voiceNames, v.Name)
			p.voiceSamples = append(p.voiceSamples, v.Sample)
		}
		if len(p.voiceNames) == 1 {
			note = "No voices are installed for " + strings.ToUpper(target) +
				". " + platform.VoiceInstallHint()
		}
	}

	p.voiceNote = widget.NewLabel(note)
	p.voiceNote.Wrapping = fyne.TextWrapWord

	p.voiceSelect = widget.NewSelect(labels, nil)
	p.voiceSelect.SetSelectedIndex(0)
	chosen := p.working.VoiceFor(target)
	for i, name := range p.voiceNames {
		if name != "" && name == chosen {
			p.voiceSelect.SetSelectedIndex(i)
		}
	}
	p.voiceSelect.OnChanged = func(string) {
		if p.voiceButton != nil {
			p.voiceButton.Enable()
		}
	}

	p.voiceButton = widget.NewButton("Preview", p.previewVoice)
	if len(p.voiceNames) == 1 {
		p.voiceButton.Disable()
	}
}

const modelVoiceLabel = "Model voice — changes at every reconnect"

// selectedVoice is the voice name the dropdown is on, "" for the model's.
func (p *prefsDialog) selectedVoice() (name, sample string) {
	i := p.voiceSelect.SelectedIndex()
	if i < 0 || i >= len(p.voiceNames) {
		return "", ""
	}
	return p.voiceNames[i], p.voiceSamples[i]
}

// previewVoice speaks the voice's own sample sentence through the output
// device the dialog has selected — so it answers "what does it sound like"
// and "does it come out of the right box" in one press.
func (p *prefsDialog) previewVoice() {
	name, sample := p.selectedVoice()
	if name == "" {
		p.toneStatus.SetText("The model's voice cannot be previewed — it is chosen per session.")
		return
	}
	if sample == "" {
		sample = "This is the voice that will speak the translation."
	}
	p.stopTone()

	device := ""
	if i := p.outputSelect.SelectedIndex(); i >= 0 && i < len(p.outputNames) {
		device = p.outputNames[i]
	}
	player := audio.NewPlayer(device, tts.Rate)
	player.SetVolume(p.speechVolume.Value)
	dev, err := player.Start()
	if err != nil {
		p.toneStatus.SetText("Could not open the device: " + err.Error())
		return
	}

	p.voiceButton.Disable()
	p.toneStatus.SetText("Previewing " + name + " through " + dev.Name + "…")

	go func() {
		defer player.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		pcm, err := tts.Synthesize(ctx, name, sample)
		done := func(text string) {
			fyne.Do(func() {
				if p.toneStatus != nil {
					p.toneStatus.SetText(text)
				}
				if p.voiceButton != nil {
					p.voiceButton.Enable()
				}
			})
		}
		if err != nil {
			done("Could not speak with " + name + ": " + err.Error())
			return
		}
		// Paced into the player the same way the session paces it, because the
		// ring is the same size here as it is there.
		speaker := tts.NewSpeaker(name, player.Write)
		speaker.Say(sample)
		_ = pcm
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if spoken, _, failed := speaker.Stats(); spoken+failed > 0 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		// Let the ring drain before the device closes, or the tail is clipped.
		time.Sleep(time.Duration(audio.PlayPrebufferMS+300) * time.Millisecond)
		speaker.Stop()
		done("Not running.")
	}()
}

func outputDeviceLabel(d audio.OutputDevice) string {
	label := fmt.Sprintf("%s (%.0f Hz, %dch)", d.Name, d.DefaultSampleRate, d.Channels)
	if d.IsDefault {
		label += " — default"
	}
	return label
}

func (p *prefsDialog) networkTab() fyne.CanvasObject {
	// The result wraps to two lines at this width, and the label is given room
	// for both — a clipped verdict is worse than no verdict.
	p.netResult = widget.NewLabel("Not tested")
	p.netResult.Wrapping = fyne.TextWrapWord
	p.netDetail = widget.NewLabel("")
	p.netDetail.Wrapping = fyne.TextWrapWord

	p.runTest = widget.NewButton("Run Test", func() { p.runNetworkTest() })

	// Wrapping, like the two labels above. Without it this one sentence set the
	// minimum width of the whole dialog to 720 px.
	intro := widget.NewLabel(fmt.Sprintf(
		"A session needs about %.0f kbit/s upstream and a steady round trip to the provider's server.",
		netcheck.AudioKbps))
	intro.Wrapping = fyne.TextWrapWord

	return container.NewVBox(
		intro,
		p.runTest,
		widget.NewLabelWithStyle("Result", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.netResult,
		p.netDetail,
	)
}

func (p *prefsDialog) vocabularyTab() fyne.CanvasObject {
	p.vocabSource = widget.NewMultiLineEntry()
	p.vocabSource.SetText(p.working.VocabularySource)
	p.vocabCaption = widget.NewMultiLineEntry()
	p.vocabCaption.SetText(p.working.VocabularyCaptions)

	// This was the widest thing in the dialog at 884 px, purely because a label
	// with no wrapping asks for its whole text on one line.
	note := widget.NewLabel(fmt.Sprintf(
		"Split on line breaks only, never commas, so a term can be a whole clause. "+
			"At most %d terms — they are resent with every reconnect. "+
			"Google Gemini only: OpenAI takes no vocabulary hints.",
		config.MaxVocabularyTerms))
	note.Wrapping = fyne.TextWrapWord

	sourceHint := widget.NewLabel("Names and jargon the model should expect to hear.")
	sourceHint.Wrapping = fyne.TextWrapWord
	captionHint := widget.NewLabel("How names should be spelled on screen.")
	captionHint.Wrapping = fyne.TextWrapWord

	// Two lists, and they must stay separate: one biases recognition of the
	// language being spoken, the other how names come out in the captions.
	return container.NewVBox(
		widget.NewLabelWithStyle("Spoken language — one term per line",
			fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		sourceHint,
		p.vocabSource,
		widget.NewLabelWithStyle("Captions — one term per line",
			fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		captionHint,
		p.vocabCaption,
		note,
	)
}

// Named colour presets, so choosing a caption colour is picking from a row of
// swatches rather than driving RGB sliders. The full picker is still one click
// away for anyone who wants an exact value.
//
// The palettes are per role because the useful choices differ: a background is
// dark or it competes with the captions, and caption text has to stay legible
// on the box.
type preset struct {
	name string
	hex  string
}

var (
	bgPresets = []preset{
		{"Black", "#000000"}, {"Charcoal", "#1C1C1E"}, {"Navy", "#0B1D33"},
		{"Deep green", "#0C2018"}, {"Deep plum", "#1A0F1F"}, {"White", "#FFFFFF"},
	}
	boxPresets = []preset{
		{"Ocean", "#1B4965"}, {"Black", "#000000"}, {"Slate", "#2C3440"},
		{"Forest", "#1E3A2F"}, {"Wine", "#4A1220"}, {"Indigo", "#2A2A5A"},
	}
	textPresets = []preset{
		{"White", "#FFFFFF"}, {"Soft white", "#EDEDED"}, {"Warm", "#FFE9C7"},
		{"Yellow", "#FFD34D"}, {"Mint", "#B9F5D0"}, {"Black", "#000000"},
	}
)

// swatch is a tappable block of colour. Fyne has no colour-well widget and a
// Button paints its own background over anything stacked behind it, so this is
// the smallest thing that can show a colour and be clicked.
type swatch struct {
	widget.BaseWidget
	fill     color.NRGBA
	selected bool
	tooltip  string
	onTap    func()
}

func newSwatch(hex, tooltip string, onTap func()) *swatch {
	s := &swatch{
		fill:    parseHex(hex, color.NRGBA{A: 0xFF}),
		tooltip: tooltip,
		onTap:   onTap,
	}
	s.ExtendBaseWidget(s)
	return s
}

func (s *swatch) Tapped(*fyne.PointEvent) {
	if s.onTap != nil {
		s.onTap()
	}
}

func (s *swatch) CreateRenderer() fyne.WidgetRenderer {
	block := canvas.NewRectangle(s.fill)
	block.CornerRadius = 5
	block.StrokeWidth = 3
	return &swatchRenderer{sw: s, block: block}
}

type swatchRenderer struct {
	sw    *swatch
	block *canvas.Rectangle
}

func (r *swatchRenderer) Layout(size fyne.Size)        { r.block.Resize(size) }
func (r *swatchRenderer) MinSize() fyne.Size           { return fyne.NewSize(40, 28) }
func (r *swatchRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.block} }
func (r *swatchRenderer) Destroy()                     {}

func (r *swatchRenderer) Refresh() {
	r.block.FillColor = r.sw.fill
	// The tick a checkbox would use is not readable at this size, so selection
	// is a ring — visible against a swatch of any colour.
	if r.sw.selected {
		r.block.StrokeColor = color.NRGBA{R: 0x4D, G: 0x9E, B: 0xFF, A: 0xFF}
	} else {
		r.block.StrokeColor = color.NRGBA{R: 0x80, G: 0x80, B: 0x80, A: 0x60}
	}
	r.block.Refresh()
}

// colourPick is a row of presets, a live preview of the value in force, and a
// Custom… button for the full picker.
type colourPick struct {
	value    string // #RRGGBB, uppercase
	presets  []preset
	swatches []*swatch
	current  *canvas.Rectangle
	label    *widget.Label
	root     fyne.CanvasObject
}

func (p *prefsDialog) newColourPick(title, initial string, presets []preset) *colourPick {
	c := &colourPick{value: normaliseHex(initial), presets: presets}

	row := container.NewHBox()
	for _, pre := range presets {
		hex := pre.hex
		sw := newSwatch(hex, pre.name, func() { c.setHex(hex) })
		c.swatches = append(c.swatches, sw)
		row.Add(sw)
	}

	c.current = canvas.NewRectangle(parseHex(c.value, color.NRGBA{A: 0xFF}))
	c.current.SetMinSize(fyne.NewSize(28, 28))
	c.current.CornerRadius = 5
	c.label = widget.NewLabel(c.value)

	custom := widget.NewButton("Custom…", func() {
		d := dialog.NewColorPicker(title, "", func(chosen color.Color) {
			c.set(chosen)
		}, p.win.win)
		d.Advanced = true
		d.SetColor(parseHex(c.value, color.NRGBA{A: 0xFF}))
		d.Show()
	})

	// Two rows, not one. All of it on a single line made this the widest thing
	// in the dialog at 608 px — the swatches, the hex, the preview and the
	// button end to end — and the dialog is only ever as narrow as its widest
	// control. Stacked, it costs about 35 px of height per colour and the
	// Colours tab has room for it.
	//
	// The hex label still sits between the presets and the preview: with the
	// preview butted straight against the row it read as a seventh preset.
	c.root = container.NewVBox(row, container.NewHBox(c.label, c.current, custom))
	c.refresh()
	return c
}

func (c *colourPick) set(chosen color.Color) {
	nrgba := color.NRGBAModel.Convert(chosen).(color.NRGBA)
	c.setHex(fmt.Sprintf("#%02X%02X%02X", nrgba.R, nrgba.G, nrgba.B))
}

func (c *colourPick) setHex(hex string) {
	c.value = normaliseHex(hex)
	c.refresh()
}

// refresh repaints the preview and rings whichever preset is in force. A
// custom value simply rings nothing, which is the honest thing to show.
func (c *colourPick) refresh() {
	c.current.FillColor = parseHex(c.value, color.NRGBA{A: 0xFF})
	c.current.Refresh()
	c.label.SetText(c.value)
	for i, sw := range c.swatches {
		sw.selected = c.presets[i].hex == c.value
		sw.Refresh()
	}
}

func normaliseHex(v string) string { return strings.ToUpper(strings.TrimSpace(v)) }

// Appearance was one tab of ten form rows and three checks, 511 px tall. It is
// two now, split where the question changes: Captions is about size and
// placement, Colours is about what it looks like. Both halves are built here
// because the widgets are read back together by collect().
func (p *prefsDialog) appearanceWidgets() {
	p.bgColour = p.newColourPick("Background colour", p.working.BgColor, bgPresets)
	p.boxColour = p.newColourPick("Caption box colour", p.working.BoxColor, boxPresets)
	p.textColour = p.newColourPick("Text colour", p.working.TextColor, textPresets)

	p.fontSize = widget.NewSlider(24, 96)
	p.fontSize.Step = 2
	p.fontSize.SetValue(float64(p.working.FontSize))
	fontLabel := widget.NewLabel("")
	p.fontSize.OnChanged = func(v float64) { fontLabel.SetText(strconv.Itoa(int(v)) + " px") }
	p.fontSize.OnChanged(p.fontSize.Value)

	p.maxLines = widget.NewSelect([]string{"1", "2", "3", "4"}, nil)
	p.maxLines.SetSelected(strconv.Itoa(p.working.MaxLines))

	p.boxOpacity = widget.NewSlider(0.6, 1.0)
	p.boxOpacity.Step = 0.05
	p.boxOpacity.SetValue(p.working.BoxOpacity)

	p.cornerRadius = widget.NewSlider(0, 48)
	p.cornerRadius.SetValue(float64(p.working.CornerRadius))

	p.boxWidth = widget.NewSlider(60, 100)
	p.boxWidth.SetValue(float64(p.working.BoxMaxWidthPct))

	p.bottomMargin = widget.NewSlider(2, 20)
	p.bottomMargin.SetValue(float64(p.working.BottomMarginPct))

	p.boxPosition = widget.NewSelect([]string{"Top", "Middle", "Bottom"}, nil)
	p.boxPosition.SetSelected(config.BoxPositionMenuText[p.working.Position()])

	p.fixedBox = widget.NewCheck("Always show the caption box", nil)
	p.fixedBox.SetChecked(p.working.FixedBox)
	p.sourceText = widget.NewCheck("Show the source text", nil)
	p.sourceText.SetChecked(p.working.ShowSourceText)
	p.directionHint = widget.NewCheck("Show the direction briefly on start", nil)
	p.directionHint.SetChecked(p.working.ShowDirectionHint)

	p.captionsForm = widget.NewForm(
		widget.NewFormItem("Font size", container.NewBorder(nil, nil, nil, fontLabel, p.fontSize)),
		widget.NewFormItem("Caption lines", p.maxLines),
		widget.NewFormItem("Box width %", p.boxWidth),
		widget.NewFormItem("Edge margin %", p.bottomMargin),
		widget.NewFormItem("Box position", p.boxPosition),
	)
	p.coloursForm = widget.NewForm(
		widget.NewFormItem("Background colour", p.bgColour.root),
		widget.NewFormItem("Caption box colour", p.boxColour.root),
		widget.NewFormItem("Text colour", p.textColour.root),
		widget.NewFormItem("Box opacity", p.boxOpacity),
		widget.NewFormItem("Corner radius", p.cornerRadius),
	)
}

// captionsTab is size and placement.
func (p *prefsDialog) captionsTab() fyne.CanvasObject {
	if p.captionsForm == nil {
		p.appearanceWidgets()
	}
	return container.NewVBox(p.captionsForm, p.fixedBox, p.sourceText, p.directionHint)
}

// coloursTab is what it looks like. It shares appearanceWidgets with
// captionsTab, so whichever is built first builds both — show() calls each
// once, and the tab that is built second reuses the widgets rather than
// replacing the ones collect() will read.
func (p *prefsDialog) coloursTab() fyne.CanvasObject {
	if p.coloursForm == nil {
		p.appearanceWidgets()
	}
	return container.NewVBox(p.coloursForm)
}

func (p *prefsDialog) behaviourTab() fyne.CanvasObject {
	p.clearAfter = widget.NewSlider(0, 30)
	p.clearAfter.Step = 0.5
	p.clearAfter.SetValue(p.working.ClearAfterSeconds)
	clearLabel := widget.NewLabel("")
	p.clearAfter.OnChanged = func(v float64) {
		if v == 0 {
			clearLabel.SetText("never")
			return
		}
		clearLabel.SetText(fmt.Sprintf("%.1f s", v))
	}
	p.clearAfter.OnChanged(p.clearAfter.Value)

	p.lineHold = widget.NewSlider(0, 3)
	p.lineHold.Step = 0.1
	p.lineHold.SetValue(p.working.LineHoldSeconds)

	p.completeOnly = widget.NewCheck("Complete phrases only (9)", nil)
	p.completeOnly.SetChecked(p.working.CompletePhrasesOnly)

	p.saveScript = widget.NewCheck("Save a transcript for each session", nil)
	p.saveScript.SetChecked(p.working.SaveTranscript)
	p.transcriptDir = widget.NewEntry()
	p.transcriptDir.SetText(p.working.TranscriptDir)

	p.debugLogging = widget.NewCheck("Debug logging", nil)
	p.debugLogging.SetChecked(p.working.DebugLogging)

	p.adaptivePrep = widget.NewCheck("Adaptive session prep", nil)
	p.adaptivePrep.SetChecked(p.working.AdaptiveSessionPrep)
	p.warmHandover = widget.NewCheck("Warm session handover", nil)
	p.warmHandover.SetChecked(p.working.WarmHandover)
	prepHint := widget.NewLabel(
		"Hide the nine-minute session change: in a pause, with the new " +
			"session already running, or while the music filter is closed.")
	prepHint.Wrapping = fyne.TextWrapWord

	// Where the log is, because it is not obvious and there is no terminal
	// behind an installed .app. The level takes effect on Apply, in this
	// process — no relaunch.
	logHint := widget.NewLabel("")
	logHint.Wrapping = fyne.TextWrapWord
	if path := logging.Path(); path != "" {
		logHint.SetText("Log file: " + path)
	} else {
		logHint.SetText("No log file could be opened — logging to the terminal only.")
	}

	form := widget.NewForm(
		widget.NewFormItem("Clear after silence",
			container.NewBorder(nil, nil, nil, clearLabel, p.clearAfter)),
		widget.NewFormItem("Line hold", p.lineHold),
		widget.NewFormItem("Transcript folder", p.transcriptDir),
	)
	return container.NewVBox(form, p.completeOnly, p.saveScript,
		// Side by side: one more row made this the one tab taller than the
		// dialog.
		container.NewHBox(p.warmHandover, p.adaptivePrep), prepHint, p.debugLogging, logHint)
}

// --- buttons --------------------------------------------------------------

// collect reads every widget back into a Settings.
func (p *prefsDialog) collect() config.Settings {
	s := p.working
	s.Provider = p.selectedProvider()
	if i := p.deviceSelect.SelectedIndex(); i >= 0 && i < len(p.deviceNames) {
		s.AudioDeviceName = p.deviceNames[i]
	}
	if i := p.outputSelect.SelectedIndex(); i >= 0 && i < len(p.outputNames) {
		s.OutputDeviceName = p.outputNames[i]
	}
	s.SpeakTranslation = p.speakAloud.Checked
	s.SpeechVolume = p.speechVolume.Value
	s.SilenceGate = p.silenceGate.Checked
	s.MusicGate = p.musicGate.Checked
	s.MusicGateThreshold = p.musicThreshold.Value
	s.MusicGateHold = p.musicHold.Value
	s.MusicGateRelease = p.musicRelease.Value
	if p.adaptivePrep != nil {
		s.AdaptiveSessionPrep = p.adaptivePrep.Checked
	}
	if p.warmHandover != nil {
		s.WarmHandover = p.warmHandover.Checked
	}
	if p.voiceSelect != nil {
		name, _ := p.selectedVoice()
		s = s.WithVoiceFor(s.TargetLanguageCode, name)
	}
	s.VocabularySource = p.vocabSource.Text
	s.VocabularyCaptions = p.vocabCaption.Text
	s.FontSize = int(p.fontSize.Value)
	if n, err := strconv.Atoi(p.maxLines.Selected); err == nil {
		s.MaxLines = n
	}
	s.BgColor = p.bgColour.value
	s.BoxColor = p.boxColour.value
	s.TextColor = p.textColour.value
	s.BoxOpacity = p.boxOpacity.Value
	s.CornerRadius = int(p.cornerRadius.Value)
	s.BoxMaxWidthPct = int(p.boxWidth.Value)
	s.BottomMarginPct = int(p.bottomMargin.Value)
	for position, label := range config.BoxPositionMenuText {
		if label == p.boxPosition.Selected {
			s.BoxPosition = string(position)
		}
	}
	s.FixedBox = p.fixedBox.Checked
	s.ShowSourceText = p.sourceText.Checked
	s.ShowDirectionHint = p.directionHint.Checked
	s.ClearAfterSeconds = p.clearAfter.Value
	s.LineHoldSeconds = p.lineHold.Value
	s.CompletePhrasesOnly = p.completeOnly.Checked
	s.SaveTranscript = p.saveScript.Checked
	s.TranscriptDir = p.transcriptDir.Text
	s.DebugLogging = p.debugLogging.Checked
	return s.Clamped()
}

// selectedProvider is the provider the API tab is showing.
func (p *prefsDialog) selectedProvider() config.Provider {
	if p.provider != nil {
		for pr, label := range config.ProviderMenuText {
			if label == p.provider.Selected {
				return pr
			}
		}
	}
	return p.working.Provider
}

// showProviderKey points the key widgets at the selected provider.
func (p *prefsDialog) showProviderKey() {
	pr := p.selectedProvider()
	p.keyHeading.SetText(config.ProviderMenuText[pr] + " API key")
	if pr == config.ProviderOpenAI {
		p.apiKey.SetPlaceHolder("sk-…")
	} else {
		p.apiKey.SetPlaceHolder("AIza…")
	}
	p.refreshKeyStatus()
}

// apply pushes the current widget values into the running application.
func (p *prefsDialog) apply() {
	if key := p.apiKey.Text; key != "" {
		if err := config.SaveKey(p.selectedProvider(), key, p.toKeychain.Checked); err != nil {
			slog.Error("could not save the API key", "err", err)
		}
		p.apiKey.SetText("")
		p.refreshKeyStatus()
	}
	next := p.collect()
	p.working = next
	p.dirty = true
	p.win.applySettings(next)
}

// forgetKey drops the key from everywhere this application put it.
//
// It takes effect at once rather than waiting for Apply, which is why it marks
// the dialog dirty: Cancel restores the encrypted store, and until this counted
// as a change, Forget→Cancel left the key deleted while Forget→Apply→Cancel
// brought it back. The Keychain copy is gone either way — there is no way to
// tell a copy this dialog made from one that was already there.
func (p *prefsDialog) forgetKey() {
	config.ClearKey(p.selectedProvider())
	p.dirty = true
	p.apiKey.SetText("")
	p.refreshKeyStatus()
}

// cancel puts back the settings that were in force when the dialog opened, and
// the credential store with them.
func (p *prefsDialog) cancel() {
	if !p.dirty {
		return
	}
	slog.Info("preferences cancelled — reverting the applied changes")
	for pr, key := range p.storedKeys {
		if err := config.RestoreStoredKey(pr, key); err != nil {
			slog.Error("could not restore the stored key", "provider", pr, "err", err)
		}
	}
	p.win.applySettings(p.original)
}

// --- workers --------------------------------------------------------------

func (p *prefsDialog) refreshKeyStatus() {
	source := config.KeySource(p.selectedProvider())
	if source == "" {
		p.keyStatus.SetText("No key found. Enter one above.")
		return
	}
	p.keyStatus.SetText("Key in use, from: " + source)
}

// testKey does one cheap authenticated call, off the UI goroutine.
func (p *prefsDialog) testKey() {
	pr := p.selectedProvider()
	key := p.apiKey.Text
	if key == "" {
		key = config.LoadKey(pr)
	}
	if key == "" {
		p.keyStatus.SetText("No key to test.")
		return
	}
	p.keyStatus.SetText("Testing…")
	go func() {
		err := translator.TestKeyFor(string(pr), key)
		fyne.Do(func() {
			if err != nil {
				p.keyStatus.SetText("Key rejected: " + err.Error())
				return
			}
			if pr == config.ProviderOpenAI {
				// The free check cannot see the balance; saying only
				// "accepted" invites a surprise at the first session.
				p.keyStatus.SetText("Key accepted. Credit balance is not checked.")
				return
			}
			p.keyStatus.SetText("Key accepted.")
		})
	}()
}

// runNetworkTest measures the connection off the UI goroutine.
func (p *prefsDialog) runNetworkTest() {
	if p.netBusy {
		return
	}
	p.netBusy = true
	p.runTest.Disable()
	p.netResult.SetText("Testing…")
	p.netDetail.SetText("")
	// Read here, on the UI goroutine: the select belongs to it.
	provider := p.selectedProvider()

	go func() {
		host := netcheck.LiveAPIHost
		if provider == config.ProviderOpenAI {
			host = netcheck.OpenAIHost
		}
		report := netcheck.RunFor(host)
		fyne.Do(func() {
			p.netBusy = false
			p.runTest.Enable()
			p.netResult.SetText(report.Verdict())
			detail := ""
			for _, line := range report.SummaryLines() {
				detail += line + "\n"
			}
			p.netDetail.SetText(detail)
		})
	}()
}

// Meter scale. A progress bar is linear and hearing is not: a normal speaking
// peak is −36 dBFS, which is 0.016 of full scale, so a linear bar sits at 1.6%
// while somebody talks into the microphone and looks identical to a dead input
// at 0.004%. Mapping dBFS across this span puts the same voice at 40% and the
// dead input at 0.
const (
	meterFloorDB   = -60.0
	meterCeilingDB = 0.0
	meterGoodDB    = -24.0 // comfortably above the noise floor of a live mic
	meterLoudDB    = -3.0  // about to clip
)

// peakToFraction maps a 0–1 sample peak onto the bar's 0–1 range in dBFS.
func peakToFraction(peak float64) float64 {
	if peak <= 0 {
		return 0
	}
	db := 20 * math.Log10(peak)
	return math.Max(0, math.Min(1, (db-meterFloorDB)/(meterCeilingDB-meterFloorDB)))
}

// peakVerdict is the plain-language reading printed on the bar. A number alone
// does not tell somebody whether to turn the source up.
func peakVerdict(peak float64) string {
	if peak <= 0 {
		return "no signal"
	}
	db := 20 * math.Log10(peak)
	switch {
	case db < meterFloorDB:
		// Both readings land here and they mean opposite things: a working
		// microphone in a quiet room measured −53 to −73 dBFS on this machine,
		// and an input with nothing routed into it sits at −88. "Check the
		// input" alone accuses a working device, so the prompt comes first.
		return fmt.Sprintf("%.0f dBFS — silent; speak, or check the input", db)
	case db > meterLoudDB:
		return fmt.Sprintf("%.0f dBFS — too loud, turn the source down", db)
	case db >= meterGoodDB:
		return fmt.Sprintf("%.0f dBFS — good", db)
	default:
		return fmt.Sprintf("%.0f dBFS — quiet, turn the source up", db)
	}
}

// startMeter opens the selected device so the level can be tested without
// starting a translation — which is the point, since the reason to look at the
// meter is to find out whether this device carries the sound at all.
//
// While a session is running the meter observes that capture instead of opening
// the device a second time.
func (p *prefsDialog) startMeter() {
	if live := p.win.capture; live != nil && live.IsRunning() {
		p.observing = true
		p.levelButton.SetText("Stop Level Test")
		p.levelStatus.SetText("Listening to the running session — " +
			live.Device().Name)
		return
	}

	name := ""
	if i := p.deviceSelect.SelectedIndex(); i >= 0 && i < len(p.deviceNames) {
		name = p.deviceNames[i]
	}
	capture := audio.NewMeterCapture(name)
	dev, err := capture.Start()
	if err != nil {
		p.levelStatus.SetText("Could not open the device: " + err.Error())
		// The handover paths reach here with the button reading "Stop", so a
		// failure has to put the label back where the state is.
		p.levelButton.SetText("Start Level Test")
		return
	}
	p.meter = capture
	p.levelButton.SetText("Stop Level Test")
	p.levelStatus.SetText("Listening to " + dev.Name + "…")
}

// startTone opens the selected output device and plays the chime.
//
// A sound of our own rather than a speech sample: it needs no bundled asset, it
// cannot be mistaken for a caption, and what is being tested is routing —
// whether sound leaves the right box — not quality. It runs through the real
// Player, so it exercises the resampler and the jitter buffer the translation
// will use. See chime.go for what it plays and why it stopped being a beep.
func (p *prefsDialog) startTone() {
	name := ""
	if i := p.outputSelect.SelectedIndex(); i >= 0 && i < len(p.outputNames) {
		name = p.outputNames[i]
	}

	player := audio.NewPlayer(name, audio.TranslatedRate)
	player.SetVolume(p.speechVolume.Value)
	dev, err := player.Start()
	if err != nil {
		p.toneStatus.SetText("Could not open the device: " + err.Error())
		p.toneButton.SetText("Play Test Sound")
		return
	}
	p.tone = player
	p.toneStop = make(chan struct{})
	p.toneButton.SetText("Stop Test Sound")
	p.toneStatus.SetText("Playing a test chime through " + dev.Name + "…")

	stop := p.toneStop
	go func() {
		// 100 ms blobs, the shape the network delivers.
		const blobMS = 100
		samples := audio.TranslatedRate * blobMS / 1000
		pcm := make([]byte, samples*2)
		var pos int64
		ticker := time.NewTicker(blobMS * time.Millisecond)
		defer ticker.Stop()
		for {
			pos = fillChime(pcm, audio.TranslatedRate, pos)
			player.Write(pcm)
			select {
			case <-stop:
				return
			case <-p.levelStop:
				return
			case <-ticker.C:
			}
		}
	}()
}

// stopTone releases the device. Safe when nothing is running, because close()
// calls it on every dismissal.
func (p *prefsDialog) stopTone() {
	if p.toneStop != nil {
		select {
		case <-p.toneStop:
		default:
			close(p.toneStop)
		}
		p.toneStop = nil
	}
	if p.tone != nil {
		p.tone.Stop()
		p.tone = nil
	}
	if p.toneButton != nil {
		p.toneButton.SetText("Play Test Sound")
	}
	if p.toneStatus != nil {
		p.toneStatus.SetText("Not running.")
	}
}

// fillTone writes one blob of sine into pcm and returns the phase to continue
// from. Carrying the phase is what stops a click at every blob boundary.

// stopMeter releases the device. It is safe to call when nothing is running,
// because close() calls it on every dismissal.
func (p *prefsDialog) stopMeter() {
	if p.meter != nil {
		p.meter.Stop()
		p.meter = nil
	}
	p.observing = false
	p.lastPeak = 0
	if p.levelButton != nil {
		p.levelButton.SetText("Start Level Test")
		p.levelStatus.SetText("Not running.")
		p.level.SetValue(0)
	}
}

// startLevelMeter polls whichever capture is being observed, while the dialog
// is open.
func (p *prefsDialog) startLevelMeter() {
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				fyne.Do(p.pollMeter)
			case <-p.levelStop:
				return
			}
		}
	}()
}

// meterAction is what a poll tick has to do about the session before it reads
// a level. Factored out because the session can start or stop underneath a
// running test at any tick, and both directions were missable: the first held
// two streams open on one device, and the second left the dialog claiming to
// listen to a session that had ended.
type meterAction int

const (
	meterKeep meterAction = iota
	// The session started under our own meter: release the device to it and
	// observe its capture instead.
	meterHandToSession
	// The session ended while we were observing it: it released the device,
	// so open it ourselves and carry on.
	meterReclaimFromSession
)

func meterTransition(ownMeter, observing, sessionRunning bool) meterAction {
	switch {
	case ownMeter && sessionRunning:
		return meterHandToSession
	case !ownMeter && observing && !sessionRunning:
		// The !ownMeter guard is load-bearing: startMeter does not check for
		// an existing capture, so reclaiming while one is open would open a
		// second stream on the device and leak the first.
		return meterReclaimFromSession
	default:
		return meterKeep
	}
}

func (p *prefsDialog) pollMeter() {
	live := p.win.capture
	sessionRunning := live != nil && live.IsRunning()

	switch meterTransition(p.meter != nil, p.observing, sessionRunning) {
	case meterHandToSession:
		p.meter.Stop()
		p.meter = nil
		p.observing = true
		p.levelStatus.SetText("Listening to the running session — " +
			live.Device().Name)
	case meterReclaimFromSession:
		// observing drops before the reopen, so a failed reopen cannot retry
		// on every tick.
		p.observing = false
		p.startMeter()
	}

	switch {
	case p.meter != nil:
		// Our own device: we are the only reader, so take and reset.
		p.lastPeak = p.meter.TakePeak()
	case p.observing && live != nil && live.IsRunning():
		// The session's device: read without resetting. TakePeak here would
		// steal the peaks the window needs for speech detection.
		p.lastPeak = live.Peak()
	default:
		p.lastPeak = 0
	}
	// SetValue refreshes unconditionally, and the refresh re-runs the
	// TextFormatter, so the verdict follows lastPeak with no extra call.
	p.level.SetValue(peakToFraction(p.lastPeak))
}

// --- other window commands -----------------------------------------------

// ShowAbout is the About box.
func (w *Window) ShowAbout() {
	engine, model := "Google Gemini Live API", translator.Model
	if w.settings.Provider == config.ProviderOpenAI {
		engine, model = "OpenAI Realtime API", translator.OpenAIModel
	}
	body := fmt.Sprintf(
		"%s %s\n\nReal-time speech translation captions.\n"+
			"Engine: %s\nModel: %s\n\n"+
			"Created by %s\n%s\n\nCopyright © 2026 %s. All rights reserved.",
		AppName, Version, engine, model, Author, AuthorEmail, Author)
	dialog.ShowInformation("About "+AppName, body, w.win)
}

// ResetSettings forgets every preference.
func (w *Window) ResetSettings() {
	// Hand any settled-but-undrained lines to the transcript before the buffer
	// is thrown away.
	w.buffer.Flush()
	w.recordNewLines()
	w.applySettings(config.Reset())
	w.buffer.Clear()
	w.view.Clear()
	w.syncActions()
}

// --- the music filter tab --------------------------------------------------

// filterTab is the music gate: the switch, the model, the tuning, and a live
// classifier test, because the only tuning that means anything is done
// against the user's own music playing into their own microphone.
func (p *prefsDialog) filterTab() fyne.CanvasObject {
	p.musicGate = widget.NewCheck("Translate speech only — skip singing and music", nil)
	p.musicGate.SetChecked(p.working.MusicGate)

	about := widget.NewLabel(
		"Songs are classified locally by YAMNet (Apache 2.0) — no audio " +
			"leaves the machine for this, and gated minutes are not billed.")
	about.Wrapping = fyne.TextWrapWord

	p.modelStatus = widget.NewLabel("")
	p.modelStatus.Wrapping = fyne.TextWrapWord
	p.modelButton = widget.NewButton("Download Model (4 MB)", p.downloadModel)
	p.refreshModelStatus()

	// A slider with a readout, and a Step small enough that the arrow keys
	// reach any value: Fyne's Slider is focusable and steps by Step, so fine
	// control is a matter of choosing one, not of adding a second widget.
	slider := func(minV, maxV, step float64, format string, value float64) (*widget.Slider, fyne.CanvasObject) {
		sl := widget.NewSlider(minV, maxV)
		sl.Step = step
		sl.SetValue(value)
		read := widget.NewLabel("")
		sl.OnChanged = func(v float64) { read.SetText(fmt.Sprintf(format, v)) }
		sl.OnChanged(sl.Value)
		// A fixed-width readout, or the slider resizes as the digits change.
		read.Alignment = fyne.TextAlignTrailing
		return sl, container.NewBorder(nil, nil, nil,
			container.NewGridWrap(fyne.NewSize(52, read.MinSize().Height), read), sl)
	}

	// 0.01 steps over the full usable range. The material this has to
	// separate spans 0.109 (a marching band recorded outdoors) to 0.891 (a
	// solo bugle), so 0.05 steps were too coarse to sit between two pieces of
	// the user's own music — see spikes/RESULTS.md.
	thresholdRow := fyne.CanvasObject(nil)
	p.musicThreshold, thresholdRow = slider(0.05, 0.95, 0.01, "%.2f", p.working.MusicGateThreshold)
	holdRow := fyne.CanvasObject(nil)
	p.musicHold, holdRow = slider(0.5, 10, 0.1, "%.1f s", p.working.MusicGateHold)
	releaseRow := fyne.CanvasObject(nil)
	p.musicRelease, releaseRow = slider(0.5, 5, 0.1, "%.1f s", p.working.MusicGateRelease)

	form := widget.NewForm(
		widget.NewFormItem("Music threshold", thresholdRow),
		widget.NewFormItem("Gate after", holdRow),
		widget.NewFormItem("Reopen after", releaseRow),
	)

	tuningNote := widget.NewLabel(
		"Waiting to reopen costs nothing — the audio from just before is kept " +
			"and sent — and it is what stops a momentary misread leaking a song.")
	tuningNote.Wrapping = fyne.TextWrapWord
	tuningNote.TextStyle = fyne.TextStyle{Italic: true}

	p.clsStatus = widget.NewLabel("Not running.")
	p.clsStatus.Wrapping = fyne.TextWrapWord
	p.clsButton = widget.NewButton("Start Classifier Test", func() {
		if p.clsRunning() {
			p.stopClassifierTest()
		} else {
			p.startClassifierTest()
		}
	})

	return container.NewVBox(
		p.musicGate,
		about,
		container.NewHBox(p.modelButton),
		p.modelStatus,
		form,
		tuningNote,
		container.NewHBox(p.clsButton),
		p.clsStatus,
	)
}

// refreshModelStatus reads what is on disk and sets the label and button.
func (p *prefsDialog) refreshModelStatus() {
	if p.downloading {
		return
	}
	path := classify.ModelPath(platform.AppSupportDir())
	if _, err := classify.LoadModel(path); err == nil {
		p.modelStatus.SetText("Model installed.")
		p.modelButton.Hide()
		return
	}
	p.modelStatus.SetText("The classifier model is not installed yet. " +
		"It is downloaded once and runs entirely on this machine.")
	p.modelButton.Show()
}

// downloadModel fetches the model in the background, reporting into the
// status label. Failure is stated, never fatal — the dialog stays usable.
func (p *prefsDialog) downloadModel() {
	if p.downloading {
		return
	}
	p.downloading = true
	p.modelButton.Disable()
	p.modelStatus.SetText("Downloading…")
	dst := classify.ModelPath(platform.AppSupportDir())

	go func() {
		err := classify.DownloadModel(context.Background(), dst, func(done, total int64) {
			mb := float64(done) / (1 << 20)
			fyne.Do(func() { p.modelStatus.SetText(fmt.Sprintf("Downloading… %.1f MB", mb)) })
		})
		fyne.Do(func() {
			p.downloading = false
			p.modelButton.Enable()
			if err != nil {
				p.modelStatus.SetText("Download failed: " + err.Error())
				return
			}
			p.refreshModelStatus()
		})
	}()
}

func (p *prefsDialog) clsRunning() bool { return p.clsStop != nil }

// startClassifierTest shows live verdicts. With a gated session running it
// observes the session's own gate; otherwise it opens the selected input
// device itself — never both, because two captures on one microphone
// conflict, the same rule the level meter follows.
func (p *prefsDialog) startClassifierTest() {
	if gate := p.win.musicGate; gate != nil {
		p.clsStop = make(chan struct{})
		p.clsButton.SetText("Stop Classifier Test")
		p.clsStatus.SetText("Observing the running session's gate…")
		go p.pollGateVerdicts(gate, p.clsStop)
		return
	}
	if live := p.win.capture; live != nil && live.IsRunning() {
		p.clsStatus.SetText("A session is running without the filter — stop it first, or enable the filter and Apply.")
		return
	}

	model, err := p.win.loadYamnet()
	if err != nil {
		p.clsStatus.SetText("Model not available: " + err.Error())
		return
	}

	name := ""
	if i := p.deviceSelect.SelectedIndex(); i >= 0 && i < len(p.deviceNames) {
		name = p.deviceNames[i]
	}
	capture := audio.NewCapture(name, 1)
	dev, err := capture.Start()
	if err != nil {
		p.clsStatus.SetText("Could not open the device: " + err.Error())
		return
	}
	p.clsCapture = capture
	p.clsStop = make(chan struct{})
	p.clsButton.SetText("Stop Classifier Test")
	p.clsStatus.SetText("Listening on " + dev.Name + "…")
	go p.classifyLoop(model, capture, p.clsStop)
}

// pollGateVerdicts mirrors the live gate's verdicts into the label.
func (p *prefsDialog) pollGateVerdicts(gate *classify.Gate, stop chan struct{}) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			v, ok := gate.LastVerdict()
			if !ok {
				continue
			}
			text := verdictText(v, gate.Closed())
			fyne.Do(func() { p.clsStatus.SetText(text) })
		}
	}
}

// classifyLoop drains the test capture into a sliding window and classifies
// at the model's own cadence. All off the UI goroutine — one verdict costs
// ~52 ms of inference.
func (p *prefsDialog) classifyLoop(model *classify.Model, capture *audio.Capture, stop chan struct{}) {
	window := make([]float32, 0, classify.PatchSamples)
	fresh := 0
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		for {
			chunk, err := capture.Ring.Read(audio.ChunkBytes)
			if err != nil || chunk == nil {
				break
			}
			for i := 0; i+1 < len(chunk); i += 2 {
				v := int16(uint16(chunk[i]) | uint16(chunk[i+1])<<8)
				window = append(window, float32(v)/32768)
			}
			fresh += len(chunk) / 2
			if excess := len(window) - classify.PatchSamples; excess > 0 {
				copy(window, window[excess:])
				window = window[:classify.PatchSamples]
			}
		}
		if len(window) < classify.PatchSamples || fresh < classify.PatchSamples/2 {
			continue
		}
		fresh = 0
		scores := model.Scores(window)
		v := classify.Verdict{
			Speech: classify.SpeechScore(scores),
			Music:  classify.MusicScore(scores),
			Top:    classify.Top(scores, 3),
		}
		v.Musical = float64(v.Music) >= p.thresholdValue() &&
			float64(v.Music) >= float64(v.Speech)+classify.Margin
		text := verdictText(v, false)
		fyne.Do(func() { p.clsStatus.SetText(text) })
	}
}

// thresholdValue reads the slider from the classify goroutine — sliders are
// not thread-safe to *write*, but Value is a plain read of a float the user
// moves; worst case is a verdict computed against the previous position.
func (p *prefsDialog) thresholdValue() float64 { return p.musicThreshold.Value }

// verdictText renders one verdict for the status label.
func verdictText(v classify.Verdict, gateClosed bool) string {
	kind := "SPEECH"
	if v.Musical {
		kind = "MUSIC"
	}
	if v.Speech < 0.1 && v.Music < 0.1 {
		kind = "quiet"
	}
	state := ""
	if gateClosed {
		state = " — gate closed, not translating"
	}
	names := make([]string, 0, len(v.Top))
	for _, i := range v.Top {
		names = append(names, classify.ClassName(i))
	}
	return fmt.Sprintf("%s%s   (speech %.2f, music %.2f)\n%s",
		kind, state, v.Speech, v.Music, strings.Join(names, " / "))
}

// stopClassifierTest releases whichever source the test was using.
func (p *prefsDialog) stopClassifierTest() {
	if p.clsStop != nil {
		close(p.clsStop)
		p.clsStop = nil
	}
	if p.clsCapture != nil {
		p.clsCapture.Stop()
		p.clsCapture = nil
	}
	if p.clsButton != nil {
		p.clsButton.SetText("Start Classifier Test")
	}
	if p.clsStatus != nil {
		p.clsStatus.SetText("Not running.")
	}
}
