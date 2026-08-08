package ui

import (
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
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/netcheck"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
)

// prefsDialog is the six-tab Preferences window.
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
	// storedKey is the encrypted store's contents at the same moment.
	storedKey string
	// applied records whether Apply was pressed, so Cancel knows there is
	// something to undo.
	applied bool

	working config.Settings

	// Widgets whose values are read back on Apply.
	apiKey       *widget.Entry
	toKeychain   *widget.Check
	keyStatus    *widget.Label
	deviceSelect *widget.Select
	deviceNames  []string
	level        *widget.ProgressBar
	levelStatus  *widget.Label
	levelButton  *widget.Button
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

	// meter is the dialog's own capture, opened only when there is no session
	// to observe; observing says the meter is watching the session's capture
	// instead. Exactly one of them is set while the test runs — a bool rather
	// than the button's label, so the state is not stored in a UI string.
	meter     *audio.Capture
	observing bool
	lastPeak  float64
	levelStop chan struct{}
	netBusy   bool
}

// running reports whether the level test is on, either way round.
func (p *prefsDialog) meterRunning() bool { return p.meter != nil || p.observing }

// OpenPreferences shows the dialog.
func (w *Window) OpenPreferences() {
	p := &prefsDialog{
		win:       w,
		original:  w.settings,
		storedKey: config.StoredAPIKey(),
		working:   w.settings,
		levelStop: make(chan struct{}),
	}
	p.show()
}

func (p *prefsDialog) show() {
	tabs := container.NewAppTabs(
		container.NewTabItem("API Key", p.apiTab()),
		container.NewTabItem("Audio", p.audioTab()),
		container.NewTabItem("Network", p.networkTab()),
		container.NewTabItem("Vocabulary", p.vocabularyTab()),
		container.NewTabItem("Appearance", p.appearanceTab()),
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
	p.dialog.Resize(fyne.NewSize(640, 520))
	p.dialog.SetOnClosed(p.close)
	p.dialog.Show()
	p.startLevelMeter()
}

// close settles the background workers, so a dialog that is dismissed mid-test
// does not leave a goroutine writing into freed widgets — and releases the
// meter's device, which would otherwise stay open for the life of the process.
func (p *prefsDialog) close() {
	select {
	case <-p.levelStop:
	default:
		close(p.levelStop)
	}
	p.stopMeter()
}

// --- tabs -----------------------------------------------------------------

func (p *prefsDialog) apiTab() fyne.CanvasObject {
	p.apiKey = widget.NewPasswordEntry()
	p.apiKey.SetPlaceHolder("AIza…")
	p.toKeychain = widget.NewCheck("Also store in the macOS Keychain", nil)
	p.keyStatus = widget.NewLabel("")
	p.keyStatus.Wrapping = fyne.TextWrapWord
	p.refreshKeyStatus()

	test := widget.NewButton("Test Key", func() { p.testKey() })
	forget := widget.NewButton("Forget Key", func() {
		config.ClearAPIKey()
		p.apiKey.SetText("")
		p.refreshKeyStatus()
	})

	// The key is never logged and never written to the settings file — only to
	// the encrypted store, and to the Keychain when asked.
	return container.NewVBox(
		widget.NewLabelWithStyle("Gemini API key", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.apiKey,
		p.toKeychain,
		container.NewHBox(test, forget),
		p.keyStatus,
	)
}

func (p *prefsDialog) audioTab() fyne.CanvasObject {
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

func (p *prefsDialog) networkTab() fyne.CanvasObject {
	// The result wraps to two lines at this width, and the label is given room
	// for both — a clipped verdict is worse than no verdict.
	p.netResult = widget.NewLabel("Not tested")
	p.netResult.Wrapping = fyne.TextWrapWord
	p.netDetail = widget.NewLabel("")
	p.netDetail.Wrapping = fyne.TextWrapWord

	p.runTest = widget.NewButton("Run Test", func() { p.runNetworkTest() })

	return container.NewVBox(
		widget.NewLabel(fmt.Sprintf(
			"A session needs about %.0f kbit/s upstream and a steady round trip to %s.",
			netcheck.AudioKbps, netcheck.LiveAPIHost)),
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

	// Two lists, and they must stay separate: one biases recognition of the
	// language being spoken, the other how names come out in the captions.
	return container.NewVBox(
		widget.NewLabelWithStyle("Spoken language — one term per line",
			fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel("Names and jargon the model should expect to hear."),
		p.vocabSource,
		widget.NewLabelWithStyle("Captions — one term per line",
			fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel("How names should be spelled on screen."),
		p.vocabCaption,
		widget.NewLabel(fmt.Sprintf(
			"Split on line breaks only, never commas, so a term can be a whole clause. "+
				"At most %d terms — they are resent with every reconnect.",
			config.MaxVocabularyTerms)),
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

	// The hex label sits between the presets and the preview: with the preview
	// butted straight against the row it read as a seventh preset.
	c.root = container.NewHBox(row, c.label, c.current, custom)
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

func (p *prefsDialog) appearanceTab() fyne.CanvasObject {
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

	form := widget.NewForm(
		widget.NewFormItem("Font size", container.NewBorder(nil, nil, nil, fontLabel, p.fontSize)),
		widget.NewFormItem("Caption lines", p.maxLines),
		widget.NewFormItem("Background colour", p.bgColour.root),
		widget.NewFormItem("Caption box colour", p.boxColour.root),
		widget.NewFormItem("Text colour", p.textColour.root),
		widget.NewFormItem("Box opacity", p.boxOpacity),
		widget.NewFormItem("Corner radius", p.cornerRadius),
		widget.NewFormItem("Box width %", p.boxWidth),
		widget.NewFormItem("Edge margin %", p.bottomMargin),
		widget.NewFormItem("Box position", p.boxPosition),
	)
	return container.NewVBox(form, p.fixedBox, p.sourceText, p.directionHint)
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

	form := widget.NewForm(
		widget.NewFormItem("Clear after silence",
			container.NewBorder(nil, nil, nil, clearLabel, p.clearAfter)),
		widget.NewFormItem("Line hold", p.lineHold),
		widget.NewFormItem("Transcript folder", p.transcriptDir),
	)
	return container.NewVBox(form, p.completeOnly, p.saveScript, p.debugLogging)
}

// --- buttons --------------------------------------------------------------

// collect reads every widget back into a Settings.
func (p *prefsDialog) collect() config.Settings {
	s := p.working
	if i := p.deviceSelect.SelectedIndex(); i >= 0 && i < len(p.deviceNames) {
		s.AudioDeviceName = p.deviceNames[i]
	}
	s.SilenceGate = p.silenceGate.Checked
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

// apply pushes the current widget values into the running application.
func (p *prefsDialog) apply() {
	if key := p.apiKey.Text; key != "" {
		if err := config.SaveAPIKey(key, p.toKeychain.Checked); err != nil {
			slog.Error("could not save the API key", "err", err)
		}
		p.apiKey.SetText("")
		p.refreshKeyStatus()
	}
	next := p.collect()
	p.working = next
	p.applied = true
	p.win.applySettings(next)
}

// cancel puts back the settings that were in force when the dialog opened, and
// the credential store with them.
func (p *prefsDialog) cancel() {
	if !p.applied {
		return
	}
	slog.Info("preferences cancelled — reverting the applied changes")
	if err := config.RestoreStoredAPIKey(p.storedKey); err != nil {
		slog.Error("could not restore the stored key", "err", err)
	}
	p.win.applySettings(p.original)
}

// --- workers --------------------------------------------------------------

func (p *prefsDialog) refreshKeyStatus() {
	source := config.APIKeySource()
	if source == "" {
		p.keyStatus.SetText("No key found. Enter one above.")
		return
	}
	p.keyStatus.SetText("Key in use, from: " + source)
}

// testKey does one cheap authenticated call, off the UI goroutine.
func (p *prefsDialog) testKey() {
	key := p.apiKey.Text
	if key == "" {
		key = config.LoadAPIKey()
	}
	if key == "" {
		p.keyStatus.SetText("No key to test.")
		return
	}
	p.keyStatus.SetText("Testing…")
	go func() {
		err := translator.TestAPIKey(key)
		fyne.Do(func() {
			if err != nil {
				p.keyStatus.SetText("Key rejected: " + err.Error())
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

	go func() {
		report := netcheck.Run()
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
	body := fmt.Sprintf(
		"%s %s\n\nReal-time speech translation captions.\n"+
			"Engine: Google Gemini Live API\nModel: %s\n\n"+
			"Created by %s\n%s\n\nCopyright © 2026 %s. All rights reserved.",
		AppName, Version, translator.Model, Author, AuthorEmail, Author)
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
