package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/dennis2lee/live-translation/src_go/internal/config"
)

// openLivePrefs is openPrefs registered as the window's open dialog, the way
// OpenPreferences registers it — which is what lets a key or a menu item find
// it.
func openLivePrefs(t *testing.T, w *Window) *prefsDialog {
	t.Helper()
	p := openPrefs(t, w)
	w.prefs = p
	return p
}

// The reported bug: 9 pressed while Preferences is open left the dialog's
// check on the old value, and the next Apply put complete-phrases mode back.
func TestNineWhilePreferencesIsOpenIsKeptByApply(t *testing.T) {
	w := newTestWindow(t)
	p := openLivePrefs(t, w)
	before := w.settings.CompletePhrasesOnly

	press(t, w, '9')
	if w.settings.CompletePhrasesOnly == before {
		t.Fatal("setup: 9 did not toggle complete-phrases mode")
	}
	if p.completeOnly.Checked != w.settings.CompletePhrasesOnly {
		t.Error("the dialog's check did not follow the 9 key")
	}
	if p.showDraft.Disabled() == w.settings.CompletePhrasesOnly {
		t.Error("the draft option's greyed state did not follow the 9 key")
	}

	p.apply()
	if w.settings.CompletePhrasesOnly == before {
		t.Error("Apply undid the 9 key")
	}
}

// Cancel undoes what the dialog did, not what the key did.
func TestCancelKeepsAChangeMadeOutsideTheDialog(t *testing.T) {
	w := newTestWindow(t)
	p := openLivePrefs(t, w)
	p.fontSize.SetValue(72)
	p.apply()

	press(t, w, '9')
	toggled := w.settings.CompletePhrasesOnly
	p.cancel()

	if w.settings.FontSize == 72 {
		t.Error("Cancel did not undo the dialog's own Apply")
	}
	if w.settings.CompletePhrasesOnly != toggled {
		t.Error("Cancel undid the 9 key")
	}
}

// Only what the outside change touched is taken in. An edit the user has
// made in the dialog and not yet applied survives, and goes out on Apply.
func TestAnOutsideChangeLeavesUnappliedEditsAlone(t *testing.T) {
	w := newTestWindow(t)
	p := openLivePrefs(t, w)
	p.fontSize.SetValue(72) // not applied

	press(t, w, '9')
	if p.fontSize.Value != 72 {
		t.Errorf("the unapplied font size was reset to %.0f", p.fontSize.Value)
	}
	p.apply()
	if w.settings.FontSize != 72 {
		t.Errorf("FontSize = %d after Apply, want the edit kept", w.settings.FontSize)
	}
}

// The direction keys change the target language, which the dialog does not
// read from a widget at all: it used to carry the old one in working and
// write it back on Apply. The voice list must follow too, or a voice chosen
// from the old language's list is stored under the new one.
func TestADirectionKeyWhilePreferencesIsOpenIsKeptByApply(t *testing.T) {
	w := newTestWindow(t)
	p := openLivePrefs(t, w)
	from := w.settings.TargetLanguageCode
	key := '2'
	if from != "en" {
		key = '1'
	}
	press(t, w, key)
	to := w.settings.TargetLanguageCode
	if to == from {
		t.Fatal("setup: the direction key did not change the target")
	}
	if p.voiceTarget != to {
		t.Errorf("the voice list is for %q, want %q", p.voiceTarget, to)
	}
	p.apply()
	if w.settings.TargetLanguageCode != to {
		t.Errorf("Apply put the target back to %q", w.settings.TargetLanguageCode)
	}
}

// Every menu toggle goes through the same path; one of each shape is enough
// to catch a field left out of showFields.
func TestMenuChangesWhilePreferencesIsOpenAreShown(t *testing.T) {
	w := newTestWindow(t)
	p := openLivePrefs(t, w)

	w.update(func(s *config.Settings) {
		s.FixedBox = !s.FixedBox
		s.FontSize = 60
		s.MaxLines = 3
		s.BoxPosition = string(config.BoxTop)
		s.SaveTranscript = !s.SaveTranscript
		s.Fullscreen = !s.Fullscreen
	})
	want := w.settings
	if p.fixedBox.Checked != want.FixedBox || int(p.fontSize.Value) != 60 ||
		p.maxLines.Selected != "3" || p.boxPosition.Selected != config.BoxPositionMenuText[config.BoxTop] ||
		p.saveScript.Checked != want.SaveTranscript {
		t.Errorf("the dialog does not show the menu changes")
	}
	p.apply()
	got := w.settings
	if got.FixedBox != want.FixedBox || got.FontSize != 60 || got.MaxLines != 3 ||
		got.BoxPosition != string(config.BoxTop) || got.SaveTranscript != want.SaveTranscript ||
		got.Fullscreen != want.Fullscreen {
		t.Errorf("Apply reverted a menu change: got %+v", got)
	}
}

// A device chosen from the menu that the dialog never listed — plugged in
// after it opened — is added to the dropdown rather than lost on Apply.
func TestAMenuDeviceTheDialogDidNotListIsKept(t *testing.T) {
	w := newTestWindow(t)
	p := openLivePrefs(t, w)
	w.update(func(s *config.Settings) { s.AudioDeviceName = "A Device Plugged In Later" })
	p.apply()
	if w.settings.AudioDeviceName != "A Device Plugged In Later" {
		t.Errorf("AudioDeviceName = %q after Apply", w.settings.AudioDeviceName)
	}
}

// Reset while the dialog is open shows the defaults in it.
func TestResetWhilePreferencesIsOpenIsShown(t *testing.T) {
	w := newTestWindow(t)
	w.update(func(s *config.Settings) { s.FontSize = 80; s.CompletePhrasesOnly = true })
	p := openLivePrefs(t, w)

	w.ResetSettings()
	d := config.Default()
	if int(p.fontSize.Value) != d.FontSize || p.completeOnly.Checked != d.CompletePhrasesOnly {
		t.Errorf("after Reset the dialog shows font %.0f, complete %v", p.fontSize.Value, p.completeOnly.Checked)
	}
	p.apply()
	if w.settings.FontSize != d.FontSize {
		t.Errorf("Apply after Reset wrote back FontSize %d", w.settings.FontSize)
	}
}

// With no dialog open nothing changes: the sync is only for an open one.
func TestNoDialogNoSync(t *testing.T) {
	w := newTestWindow(t)
	press(t, w, '9')
	if w.prefs != nil {
		t.Error("a key opened or registered a dialog")
	}
}

// A voice picked in the dialog and not applied belongs to the language it was
// picked for. A direction key used to reload the list from working and drop
// it (the red team's first finding).
func TestAnUnappliedVoiceSurvivesADirectionKey(t *testing.T) {
	w := newTestWindow(t)
	p := openLivePrefs(t, w)
	if len(p.voiceNames) < 2 {
		t.Skip("no local voices installed for the target language")
	}
	from := p.voiceTarget
	p.voiceSelect.SetSelectedIndex(1)
	chosen := p.voiceNames[1]

	back, away := '1', '2'
	if from != "en" {
		back, away = '2', '1'
	}
	press(t, w, away)
	press(t, w, back)
	if got, _ := p.selectedVoice(); got != chosen {
		t.Errorf("back on %q the picker shows %q, want the unapplied %q", from, got, chosen)
	}
	p.apply()
	if got := w.settings.VoiceFor(from); got != chosen {
		t.Errorf("VoiceFor(%q) = %q after Apply, want %q", from, got, chosen)
	}
}

// Preview stays disabled while it is playing, whatever reloads the list.
func TestADirectionKeyDoesNotReenablePreviewMidPreview(t *testing.T) {
	w := newTestWindow(t)
	p := openLivePrefs(t, w)
	p.previewing = true
	p.voiceButton.Disable()
	press(t, w, '2')
	press(t, w, '1')
	if !p.voiceButton.Disabled() {
		t.Error("Preview was re-enabled while a preview was playing")
	}
}

// The voice note is always in the layout and shown only with text, so a
// note that appears after a direction change is visible.
func TestTheVoiceNoteFollowsTheList(t *testing.T) {
	w := newTestWindow(t)
	p := openLivePrefs(t, w)
	// On a canvas and laid out, the way the dialog shows it: Visible() alone
	// passed while the note was still 0x0 (the red team's second round).
	win := test.NewWindow(p.outputBox)
	defer win.Close()
	win.Resize(fyne.NewSize(prefsWidth, prefsContentHeight))
	p.loadVoices("zz") // a language nothing speaks
	if p.voiceNote.Text == "" || !p.voiceNote.Visible() {
		t.Errorf("note %q visible %v, want a visible note", p.voiceNote.Text, p.voiceNote.Visible())
	}
	if h := p.voiceNote.Size().Height; h <= 0 {
		t.Errorf("the note is shown but laid out %.0f px tall", h)
	}
	p.voiceNote.SetText("")
	p.loadVoices(w.settings.TargetLanguageCode)
	if p.voiceNote.Text == "" && p.voiceNote.Visible() {
		t.Error("an empty note is still taking a row")
	}
}

// The audio device menu, through its own handler rather than update.
func TestTheDeviceMenuWhilePreferencesIsOpenIsKeptByApply(t *testing.T) {
	w := newTestWindow(t)
	p := openLivePrefs(t, w)
	w.chooseDevice("A Device From The Menu")
	p.apply()
	if w.settings.AudioDeviceName != "A Device From The Menu" {
		t.Errorf("AudioDeviceName = %q after Apply", w.settings.AudioDeviceName)
	}
}
