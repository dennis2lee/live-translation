package ui

import (
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"fyne.io/fyne/v2"
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
	netResult    *widget.Label
	netDetail    *widget.Label
	runTest      *widget.Button
	vocabSource  *widget.Entry
	vocabCaption *widget.Entry

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

	levelStop chan struct{}
	netBusy   bool
}

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
// does not leave a goroutine writing into freed widgets.
func (p *prefsDialog) close() {
	select {
	case <-p.levelStop:
	default:
		close(p.levelStop)
	}
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

	// The meter tells "nobody is speaking" from "this device delivers nothing".
	p.level = widget.NewProgressBar()
	p.level.TextFormatter = func() string { return "Input level" }

	p.silenceGate = widget.NewCheck("Skip sending silence (saves cost)", nil)
	p.silenceGate.SetChecked(p.working.SilenceGate)

	return container.NewVBox(
		widget.NewLabel("Input device"),
		p.deviceSelect,
		widget.NewLabel("Level (only while translating)"),
		p.level,
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

func (p *prefsDialog) appearanceTab() fyne.CanvasObject {
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

// startLevelMeter polls the capture peak while the dialog is open.
func (p *prefsDialog) startLevelMeter() {
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				fyne.Do(func() {
					capture := p.win.capture
					if capture == nil {
						p.level.SetValue(0)
						return
					}
					p.level.SetValue(capture.TakePeak())
				})
			case <-p.levelStop:
				return
			}
		}
	}()
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
