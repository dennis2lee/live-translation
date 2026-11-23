package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/wordpreset"
)

// presetFile writes a preset into the (redirected) presets folder, as a text
// editor would.
func presetFile(t *testing.T, name, body string) string {
	t.Helper()
	dir := wordpreset.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".txt")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// withLists is a window whose settings already hold two lists — non-empty, and
// different from any preset below, so a revert that does nothing is caught.
func withLists(t *testing.T) *Window {
	t.Helper()
	return newTestWindowWith(t, func(s *config.Settings) {
		s.VocabularySource = "기존 용어"
		s.VocabularyCaptions = "Old Name"
	})
}

func choose(t *testing.T, wp *wordPresets, name string) {
	t.Helper()
	wp.relist()
	if !hasOption(wp.sel, name) {
		t.Fatalf("%q is not listed: %q", name, wp.sel.Options)
	}
	wp.sel.SetSelected(name)
}

// The presets folder of every UI test is a temporary one: a test must never
// read or write the real presets.
func TestPresetsFolderIsRedirectedInTests(t *testing.T) {
	newTestWindow(t)
	if strings.Contains(wordpreset.Dir(), "Application Support") || strings.Contains(wordpreset.Dir(), "AppData") {
		t.Fatalf("tests write into the real presets folder: %s", wordpreset.Dir())
	}
}

// Load fills both boxes, each with its own list, and applies nothing.
func TestLoadFillsTheBoxesAndAppliesNothing(t *testing.T) {
	w := withLists(t)
	presetFile(t, "Church", "[spoken]\n영업이익률\n평우\n[captions]\nPyeongwoo Lee\n")
	p := openLivePrefs(t, w)
	choose(t, p.words, "Church")
	p.words.doLoad()
	if p.vocabSource.Text != "영업이익률\n평우" {
		t.Errorf("spoken box = %q", p.vocabSource.Text)
	}
	if p.vocabCaption.Text != "Pyeongwoo Lee" {
		t.Errorf("captions box = %q", p.vocabCaption.Text)
	}
	if w.settings.VocabularySource != "기존 용어" || w.settings.VocabularyCaptions != "Old Name" {
		t.Error("Load applied the lists")
	}
	if !strings.Contains(p.words.status.Text, "Loaded 2 spoken, 1 captions") {
		t.Errorf("status = %q", p.words.status.Text)
	}
}

// Apply puts the loaded lists into force and records the preset's name;
// Cancel puts the old lists back, and the label with them; a file saved in
// between stays on disk.
func TestApplyAndCancelAfterLoad(t *testing.T) {
	w := withLists(t)
	presetFile(t, "Church", "[spoken]\n영업이익률\n[captions]\nPyeongwoo Lee\n")
	p := openLivePrefs(t, w)
	choose(t, p.words, "Church")
	p.words.doLoad()
	p.apply()
	if w.settings.VocabularySource != "영업이익률" || w.settings.VocabularyCaptions != "Pyeongwoo Lee" {
		t.Fatalf("applied %q / %q", w.settings.VocabularySource, w.settings.VocabularyCaptions)
	}
	if w.settings.WordPreset != "Church" {
		t.Errorf("WordPreset = %q, want Church", w.settings.WordPreset)
	}
	p.words.name.SetText("Kept")
	p.words.doSaveAs()
	p.cancel()
	if w.settings.VocabularySource != "기존 용어" || w.settings.VocabularyCaptions != "Old Name" {
		t.Errorf("Cancel left %q / %q", w.settings.VocabularySource, w.settings.VocabularyCaptions)
	}
	if w.settings.WordPreset != "" {
		t.Errorf("Cancel left WordPreset = %q", w.settings.WordPreset)
	}
	if _, err := os.Stat(filepath.Join(wordpreset.Dir(), "Kept.txt")); err != nil {
		t.Error("Cancel removed a saved preset — saving is not part of the dialog's working copy")
	}
}

// Lists edited after loading are not that preset any more.
func TestAnEditedPresetIsNotLabelled(t *testing.T) {
	w := withLists(t)
	presetFile(t, "Church", "[spoken]\nA\n[captions]\nB\n")
	p := openLivePrefs(t, w)
	choose(t, p.words, "Church")
	p.words.doLoad()
	p.vocabCaption.SetText("B\nC")
	p.apply()
	if w.settings.WordPreset != "" {
		t.Errorf("WordPreset = %q for edited lists", w.settings.WordPreset)
	}
}

// Save writes the boxes to the preset that was loaded — never to whatever the
// Select happens to show.
func TestSaveWritesToTheLoadedPreset(t *testing.T) {
	w := withLists(t)
	presetFile(t, "Church", "[spoken]\nA\n[captions]\nB\n")
	earnings := presetFile(t, "Earnings", "[spoken]\nEBITDA\n[captions]\nQ3\n")
	p := openLivePrefs(t, w)
	choose(t, p.words, "Church")
	p.words.doLoad()
	p.vocabSource.SetText("A\nA2")
	choose(t, p.words, "Earnings") // chosen, not loaded
	p.words.doSave()
	r, _, err := wordpreset.Load(wordpreset.Dir(), "Church")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.Spoken, "|") != "A|A2" || strings.Join(r.Captions, "|") != "B" {
		t.Errorf("Church holds %q / %q", r.Spoken, r.Captions)
	}
	if got, _ := os.ReadFile(earnings); string(got) != "[spoken]\nEBITDA\n[captions]\nQ3\n" {
		t.Errorf("Earnings was overwritten: %q", got)
	}
}

// Save with nothing loaded is disabled.
func TestSaveNeedsALoadedPreset(t *testing.T) {
	w := withLists(t)
	p := openLivePrefs(t, w)
	if !p.words.save.Disabled() {
		t.Error("Save is enabled with nothing loaded")
	}
}

// A file changed in a text editor since it was loaded is not overwritten by
// the first press of Save; two Saves of our own in a row do not ask.
func TestSaveAsksBeforeOverwritingAnEditorsChange(t *testing.T) {
	w := withLists(t)
	path := presetFile(t, "Church", "[spoken]\nA\n[captions]\nB\n")
	p := openLivePrefs(t, w)
	choose(t, p.words, "Church")
	p.words.doLoad()

	p.words.doSave()
	if p.words.saveArmed {
		t.Fatal("the first Save of an unchanged file asked to overwrite")
	}
	p.words.doSave()
	if p.words.saveArmed {
		t.Fatal("a second Save of our own asked to overwrite — the stamp was not refreshed")
	}

	edited := "[spoken]\nA\nFROM THE EDITOR\n[captions]\nB\n"
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	p.words.doSave()
	if !p.words.saveArmed || p.words.save.Text != "Overwrite" {
		t.Fatal("an editor's change was about to be overwritten without asking")
	}
	if got, _ := os.ReadFile(path); string(got) != edited {
		t.Fatal("the first press wrote")
	}
	p.words.doSave()
	if got, _ := os.ReadFile(path); string(got) == edited {
		t.Error("the second press did not write")
	}
}

// Save as over an existing name asks first — including a name that differs
// only in case, which is the same file on this volume.
func TestSaveAsAsksBeforeOverwriting(t *testing.T) {
	w := withLists(t)
	path := presetFile(t, "Church", "[spoken]\nA\n")
	p := openLivePrefs(t, w)
	caseInsensitive := false
	if _, err := os.Stat(filepath.Join(wordpreset.Dir(), "church.txt")); err == nil {
		caseInsensitive = true
	}
	for _, typed := range []string{"Church", "church"} {
		if typed == "church" && !caseInsensitive {
			continue
		}
		p.words.disarm()
		p.words.name.SetText(typed)
		p.words.doSaveAs()
		if !p.words.saveAsArmed {
			t.Errorf("Save as %q over Church did not ask", typed)
		}
		if got, _ := os.ReadFile(path); string(got) != "[spoken]\nA\n" {
			t.Fatalf("Save as %q wrote on the first press", typed)
		}
	}
	// Editing the name puts the button back.
	p.words.name.SetText("Church2")
	if p.words.saveAsArmed {
		t.Error("editing the name kept the Overwrite armed")
	}
	if caseInsensitive {
		p.words.name.SetText("church")
		p.words.doSaveAs()
		p.words.doSaveAs()
		if p.words.loaded != "Church" || p.words.sel.Selected != "Church" {
			t.Errorf("after saving as church over Church: loaded %q, selected %q — want the listed spelling",
				p.words.loaded, p.words.sel.Selected)
		}
	}
}

// A file that cannot be read changes nothing in the boxes and says where.
func TestABadFileChangesNothing(t *testing.T) {
	w := withLists(t)
	presetFile(t, "Broken", "[spoken]\nA\n[Music]\nB\n")
	p := openLivePrefs(t, w)
	choose(t, p.words, "Broken")
	p.words.doLoad()
	if p.vocabSource.Text != "기존 용어" || p.vocabCaption.Text != "Old Name" {
		t.Error("a bad file changed the boxes")
	}
	if !strings.Contains(p.words.status.Text, "line 3") {
		t.Errorf("status = %q, want the line", p.words.status.Text)
	}
	if p.words.loaded != "" {
		t.Error("a bad file became the loaded preset")
	}
}

// A file written behind the dialog's back appears when the Words tab is
// shown again — through the real tab container — and when the application
// comes back to the front.
func TestAFileWrittenMeanwhileIsListed(t *testing.T) {
	w := withLists(t)
	w.OpenPreferences()
	p := w.prefs
	t.Cleanup(func() { p.dialog.Hide() })
	words := -1
	for i, item := range p.tabs.Items {
		if item.Text == "Words" {
			words = i
		}
	}
	if words < 0 {
		t.Fatal("no Words tab")
	}
	p.tabs.SelectIndex(0)
	presetFile(t, "Written in TextEdit", "[spoken]\nA\n")
	if hasOption(p.words.sel, "Written in TextEdit") {
		t.Fatal("setup: listed before the tab was shown")
	}
	p.tabs.SelectIndex(words)
	if !hasOption(p.words.sel, "Written in TextEdit") {
		t.Error("not listed after the Words tab was shown")
	}

	presetFile(t, "Another", "[spoken]\nB\n")
	w.onForeground()
	if !hasOption(p.words.sel, "Another") {
		t.Error("not listed after coming back to the front")
	}
}

// Typing a name goes to the name entry, not to the caption window's keys. The
// test canvas does not route keys by focus, so this follows the desktop
// driver's rule (processCharInput: the focused object, else the canvas
// handler) — what it pins is that the name is typed into an inline entry
// that can hold the focus, where a form dialog held none.
func TestTypingANameDoesNotDriveTheCaptions(t *testing.T) {
	w := withLists(t)
	w.OpenPreferences()
	p := w.prefs
	t.Cleanup(func() { p.dialog.Hide() })
	for i, item := range p.tabs.Items {
		if item.Text == "Words" {
			p.tabs.SelectIndex(i)
		}
	}
	c := w.win.Canvas()
	c.Focus(p.words.name)
	if c.Focused() != p.words.name {
		t.Fatal("the name entry cannot take the focus")
	}
	target := w.settings.TargetLanguageCode
	for _, r := range "2026 morning 0 m f" {
		if f := c.Focused(); f != nil {
			f.TypedRune(r)
		} else {
			c.OnTypedRune()(r)
		}
	}
	if p.words.name.Text != "2026 morning 0 m f" {
		t.Errorf("entry = %q", p.words.name.Text)
	}
	if w.settings.TargetLanguageCode != target || w.view.Muted() {
		t.Error("typing a name drove the caption window")
	}
}

// Loading lists equal to the current ones, written differently — CRLF, a
// duplicate — restarts nothing.
func TestTheSameListsWrittenDifferentlyRestartNothing(t *testing.T) {
	w := newTestWindowWith(t, func(s *config.Settings) {
		s.VocabularySource = "a\nb"
		s.VocabularyCaptions = "C"
	})
	presetFile(t, "Same", "[spoken]\r\na\r\nb\r\nb\r\n[captions]\r\nC\r\n")
	old := w.settings
	p := openLivePrefs(t, w)
	choose(t, p.words, "Same")
	p.words.doLoad()
	if p.vocabSource.Text != "a\nb\nb" {
		t.Fatalf("setup: the preset did not reach the boxes: %q", p.vocabSource.Text)
	}
	p.apply()
	if sessionNeedsRestart(old, w.settings) {
		t.Errorf("the same lists asked for a restart: %q / %q", w.settings.VocabularySource, w.settings.VocabularyCaptions)
	}
}

// The over-the-cap count is of distinct terms, as ParseTerms keeps them.
func TestTheIgnoredCountIsOfDistinctTerms(t *testing.T) {
	w := withLists(t)
	var b strings.Builder
	b.WriteString("[spoken]\n")
	for i := 0; i < 220; i++ {
		b.WriteString("term" + string(rune('A'+i%26)) + string(rune('a'+i/26)) + "\n")
	}
	for i := 0; i < 30; i++ { // duplicates: 250 lines, 220 distinct
		b.WriteString("termAa\n")
	}
	presetFile(t, "Long", b.String())
	p := openLivePrefs(t, w)
	choose(t, p.words, "Long")
	p.words.doLoad()
	if !strings.Contains(p.words.status.Text, "20 spoken terms beyond 200") {
		t.Errorf("status = %q, want 20 (250 lines, 220 distinct)", p.words.status.Text)
	}
}

// OpenAI takes no word lists: changing them there restarts nothing; a switch
// to Gemini restarts and picks them up; on Gemini a change restarts.
func TestVocabularyRestartsOnlyWhereItIsUsed(t *testing.T) {
	gemini := config.Default()
	openai := gemini
	openai.Provider = config.ProviderOpenAI
	edit := func(s config.Settings) config.Settings { s.VocabularyCaptions = "New Name"; return s }
	if sessionNeedsRestart(openai, edit(openai)) {
		t.Error("OpenAI→OpenAI with new lists restarted")
	}
	if !sessionNeedsRestart(openai, edit(gemini)) {
		t.Error("OpenAI→Gemini with new lists did not restart")
	}
	if !sessionNeedsRestart(gemini, edit(gemini)) {
		t.Error("Gemini→Gemini with new lists did not restart")
	}
	label := gemini
	label.WordPreset = "Church"
	if sessionNeedsRestart(gemini, label) {
		t.Error("the preset's label restarted the session")
	}
}

// A Reset made with the dialog open reaches the preset row, so the next
// Apply does not write the old label back.
func TestAResetWithTheDialogOpenClearsTheLabel(t *testing.T) {
	w := withLists(t)
	presetFile(t, "Church", "[spoken]\nA\n[captions]\nB\n")
	p := openLivePrefs(t, w)
	choose(t, p.words, "Church")
	p.words.doLoad()
	p.apply()
	if w.settings.WordPreset != "Church" {
		t.Fatal("setup: not applied")
	}
	w.ResetSettings()
	p.apply()
	if w.settings.WordPreset != "" || w.settings.VocabularySource != "" {
		t.Errorf("after Reset and Apply: %q, %q", w.settings.WordPreset, w.settings.VocabularySource)
	}
}

// The same with a preset whose lists are empty, where Reset leaves the boxes
// as they were: only the preset row hearing about the Reset clears the label.
func TestAResetClearsTheLabelOfAnEmptyPreset(t *testing.T) {
	w := newTestWindow(t)
	presetFile(t, "Blank", "[spoken]\n[captions]\n")
	p := openLivePrefs(t, w)
	choose(t, p.words, "Blank")
	p.words.doLoad()
	p.apply()
	if w.settings.WordPreset != "Blank" {
		t.Fatal("setup: not applied")
	}
	w.ResetSettings()
	p.apply()
	if w.settings.WordPreset != "" {
		t.Errorf("Reset then Apply wrote the label back: %q", w.settings.WordPreset)
	}
}

// Reopening Preferences shows the applied preset as loaded, so Save works —
// unless the file changed since, when it is not adopted and Save stays off.
func TestReopeningAdoptsTheAppliedPreset(t *testing.T) {
	w := withLists(t)
	path := presetFile(t, "Church", "[spoken]\nA\n[captions]\nB\n")
	p := openLivePrefs(t, w)
	choose(t, p.words, "Church")
	p.words.doLoad()
	p.apply()
	p.close()

	q := openLivePrefs(t, w)
	if q.words.loaded != "Church" || q.words.save.Disabled() {
		t.Errorf("reopened: loaded %q, Save disabled %v", q.words.loaded, q.words.save.Disabled())
	}
	q.close()

	if err := os.WriteFile(path, []byte("[spoken]\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := openLivePrefs(t, w)
	if r.words.loaded != "" || !r.words.save.Disabled() {
		t.Error("a preset changed on disk was adopted as loaded")
	}
}

// Folder creates the folder and reveals it.
func TestFolderRevealsThePresetsFolder(t *testing.T) {
	var got string
	old := revealFolder
	revealFolder = func(p string) error { got = p; return nil }
	t.Cleanup(func() { revealFolder = old })
	w := withLists(t)
	p := openLivePrefs(t, w)
	p.words.doFolder()
	if got != wordpreset.Dir() {
		t.Errorf("revealed %q, want %q", got, wordpreset.Dir())
	}
	if info, err := os.Stat(got); err != nil || !info.IsDir() {
		t.Error("the folder was not created first")
	}
}

// Every tab still fits with the longest status message the row can show.
func TestTheWordsTabFitsWithALongStatus(t *testing.T) {
	w := withLists(t)
	p := openPrefs(t, w)
	tab := p.vocabularyTab()
	p.words.status.SetText(strings.Repeat("A preset called Quarterly earnings review already exists — press Overwrite to replace it ", 3))
	m := tab.MinSize()
	if m.Width > float32(prefsWidth) || m.Height > float32(prefsContentHeight) {
		t.Errorf("Words with a long status needs %v, dialog content is %dx%d", m, prefsWidth, prefsContentHeight)
	}
	if h, one := p.words.status.MinSize().Height, widget.NewLabel("x").MinSize().Height; h > one {
		t.Errorf("the status line is %v tall, one line is %v — it must stay one line", h, one)
	}
}

// A same-size edit whose modification time did not move — a FAT, exFAT or
// SMB volume — is still an edit, and Save asks before replacing it.
func TestASameSizeEditWithTheSameTimeIsNoticed(t *testing.T) {
	w := withLists(t)
	path := presetFile(t, "Church", "[spoken]\nAAAA\n")
	info, _ := os.Stat(path)
	p := openLivePrefs(t, w)
	choose(t, p.words, "Church")
	p.words.doLoad()
	if err := os.WriteFile(path, []byte("[spoken]\nBBBB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(path, info.ModTime(), info.ModTime())
	p.words.doSave()
	if !p.words.saveArmed {
		t.Error("a same-size, same-time edit was overwritten without asking")
	}
}

// A preset deleted since it was loaded is not silently recreated either.
func TestSaveAsksWhenThePresetIsGone(t *testing.T) {
	w := withLists(t)
	path := presetFile(t, "Church", "[spoken]\nA\n")
	p := openLivePrefs(t, w)
	choose(t, p.words, "Church")
	p.words.doLoad()
	_ = os.Remove(path)
	p.words.doSave()
	if !p.words.saveArmed {
		t.Error("a deleted preset was written again without asking")
	}
}

// A confirmed Overwrite covers that one write: the next change on disk is
// asked about again. And a pending Overwrite is dropped by an edit to either
// box, by Load, by leaving the tab, and by a preset adopted from outside.
func TestAnOverwriteIsConfirmedOnceAndForgotten(t *testing.T) {
	w := withLists(t)
	w.OpenPreferences()
	p := w.prefs
	t.Cleanup(func() { p.dialog.Hide() })
	path := presetFile(t, "Church", "[spoken]\nA\n")
	arm := func() {
		t.Helper()
		if err := os.WriteFile(path, []byte("[spoken]\nEDITED "+time.Now().String()+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(path)
		p.words.doSave()
		if !p.words.saveArmed {
			t.Fatal("setup: not armed")
		}
		if after, _ := os.ReadFile(path); string(after) != string(before) {
			t.Fatal("the first press wrote")
		}
	}
	choose(t, p.words, "Church")
	p.words.doLoad()

	arm()
	p.words.doSave() // confirmed: writes
	if p.words.saveArmed || p.words.save.Text != "Save" {
		t.Fatal("the confirmation outlived the write it confirmed")
	}
	arm() // a fresh change must be asked about again

	p.vocabSource.SetText("edited box")
	if p.words.saveArmed {
		t.Error("an edit to the spoken box kept the Overwrite")
	}
	p.words.doLoad()
	arm()
	p.vocabCaption.SetText("edited captions")
	if p.words.saveArmed {
		t.Error("an edit to the captions box kept the Overwrite")
	}
	p.words.doLoad()
	arm()
	p.words.doLoad()
	if p.words.saveArmed {
		t.Error("Load kept the Overwrite")
	}
	for i, item := range p.tabs.Items {
		if item.Text == "Words" {
			p.tabs.SelectIndex(i)
		}
	}
	arm()
	p.tabs.SelectIndex(0)
	if p.words.saveArmed || p.words.save.Text != "Save" {
		t.Error("leaving the tab kept the Overwrite")
	}
	p.words.doLoad()
	arm()
	p.words.adopt("")
	if p.words.saveArmed {
		t.Error("an adopt from outside inherited the Overwrite")
	}
}

// Save refuses lists that could not be loaded back, and the preset it would
// have replaced is untouched.
func TestSaveRefusesUnloadableLists(t *testing.T) {
	w := withLists(t)
	path := presetFile(t, "Good", "[spoken]\ngood\n")
	p := openLivePrefs(t, w)
	choose(t, p.words, "Good")
	p.words.doLoad()
	p.vocabSource.SetText(strings.Repeat("x", wordpreset.MaxLineRunes+1))
	p.words.doSave()
	if got, _ := os.ReadFile(path); string(got) != "[spoken]\ngood\n" {
		t.Error("an unloadable list replaced a good preset")
	}
	if !strings.HasPrefix(p.words.status.Text, "Not saved") || strings.Contains(p.words.status.Text, "Saved") {
		t.Errorf("status = %q", p.words.status.Text)
	}
}

// Reopening adopts a preset whose file has duplicates: applying collapsed
// them, which is not a change.
func TestReopeningAdoptsAPresetWithDuplicates(t *testing.T) {
	w := withLists(t)
	presetFile(t, "Dup", "[spoken]\nA\nB\nB\n[captions]\nC\n")
	p := openLivePrefs(t, w)
	choose(t, p.words, "Dup")
	p.words.doLoad()
	p.apply()
	p.close()
	q := openLivePrefs(t, w)
	if q.words.loaded != "Dup" || q.words.save.Disabled() {
		t.Errorf("not adopted: loaded %q, status %q", q.words.loaded, q.words.status.Text)
	}
}

// Reopening does not adopt a preset whose spoken list alone, or captions list
// alone, changed on disk.
func TestReopeningNoticesEitherListChanging(t *testing.T) {
	for _, edited := range []string{"[spoken]\nA2\n[captions]\nB\n", "[spoken]\nA\n[captions]\nB2\n"} {
		w := withLists(t)
		path := presetFile(t, "Church", "[spoken]\nA\n[captions]\nB\n")
		p := openLivePrefs(t, w)
		choose(t, p.words, "Church")
		p.words.doLoad()
		p.apply()
		p.close()
		if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
			t.Fatal(err)
		}
		q := openLivePrefs(t, w)
		if q.words.loaded != "" {
			t.Errorf("%q: adopted a changed preset", edited)
		}
		if !strings.Contains(q.words.status.Text, "Changed since it was applied") {
			t.Errorf("%q: status = %q", edited, q.words.status.Text)
		}
		q.close()
	}
}

// An edit to the spoken list alone also makes the lists not the preset.
func TestAnEditToTheSpokenListIsNotLabelled(t *testing.T) {
	w := withLists(t)
	presetFile(t, "Church", "[spoken]\nA\n[captions]\nB\n")
	p := openLivePrefs(t, w)
	choose(t, p.words, "Church")
	p.words.doLoad()
	p.vocabSource.SetText("A\nA2")
	p.apply()
	if w.settings.WordPreset != "" {
		t.Errorf("WordPreset = %q for an edited spoken list", w.settings.WordPreset)
	}
}

// The warnings that matter reach the status line, after the result.
func TestTheStatusCarriesItsWarnings(t *testing.T) {
	t.Run("comments", func(t *testing.T) {
		w := withLists(t)
		presetFile(t, "C", "# note\n[spoken]\nA\n")
		p := openLivePrefs(t, w)
		choose(t, p.words, "C")
		p.words.doLoad()
		p.words.doSave()
		if !strings.HasPrefix(p.words.status.Text, "Saved") || !strings.Contains(p.words.status.Text, "comments") {
			t.Errorf("status = %q", p.words.status.Text)
		}
	})
	t.Run("openai", func(t *testing.T) {
		w := newTestWindowWith(t, func(s *config.Settings) { s.Provider = config.ProviderOpenAI })
		presetFile(t, "O", "[spoken]\nA\n")
		p := openLivePrefs(t, w)
		choose(t, p.words, "O")
		p.words.doLoad()
		if !strings.HasPrefix(p.words.status.Text, "Loaded") || !strings.Contains(p.words.status.Text, "OpenAI ignores") {
			t.Errorf("status = %q — the result must come first", p.words.status.Text)
		}
	})
	t.Run("replace", func(t *testing.T) {
		w := withLists(t)
		presetFile(t, "R", "[spoken]\nA\n[replace]\nx=y\n")
		p := openLivePrefs(t, w)
		choose(t, p.words, "R")
		p.words.doLoad()
		if !strings.Contains(p.words.status.Text, "line 3: [replace]") {
			t.Errorf("status = %q", p.words.status.Text)
		}
	})
	t.Run("skipped", func(t *testing.T) {
		w := withLists(t)
		presetFile(t, "S", "[spoken]\nA\n")
		presetFile(t, "CON", "[spoken]\nA\n")
		p := openLivePrefs(t, w)
		choose(t, p.words, "S")
		p.words.doLoad()
		if !strings.Contains(p.words.status.Text, "not listed") {
			t.Errorf("status = %q", p.words.status.Text)
		}
	})
}

// The line is cut at the dialog's width, so what the user must do comes
// before anything that may be cut — measured, with a long preset name.
func TestTheStatusPutsTheActionWhereItIsSeen(t *testing.T) {
	w := withLists(t)
	p := openPrefs(t, w)
	p.vocabularyTab()
	size := theme.TextSize()
	room := float32(prefsWidth) - 4*theme.Padding() - 2*theme.InnerPadding()
	long := strings.Repeat("Quarterly earnings review ", 2)
	for _, msg := range []struct{ text, mustShow string }{
		{"Already exists — press Overwrite to replace " + long, "press Overwrite"},
		{"Changed on disk since it was loaded — press Overwrite to replace it", "press Overwrite"},
		{"Loaded 120 spoken, 90 captions — press Apply to use them (the session restarts)", "press Apply"},
		{"Not loaded: line 3: unknown section — only [spoken] and [captions]", "[captions]"},
	} {
		end := strings.Index(msg.text, msg.mustShow) + len(msg.mustShow)
		if wdt := fyne.MeasureText(msg.text[:end], size, fyne.TextStyle{}).Width; wdt > room {
			t.Errorf("%q is cut before %q: needs %.0f px, has %.0f", msg.text, msg.mustShow, wdt, room)
		}
	}
}

// A preset over the 200-term cap, applied — the boxes come back with 200 —
// and reopened: one press of Save must not cut the file to 200.
func TestSaveAfterReopenDoesNotCutAnOverCapPreset(t *testing.T) {
	w := withLists(t)
	var b strings.Builder
	b.WriteString("# hand-written\n[spoken]\n")
	for i := 0; i < 250; i++ {
		b.WriteString(fmt.Sprintf("term%03d\n", i))
	}
	b.WriteString("[captions]\nC\n")
	path := presetFile(t, "Big", b.String())
	p := openLivePrefs(t, w)
	choose(t, p.words, "Big")
	p.words.doLoad()
	p.apply()
	p.close()

	q := openLivePrefs(t, w)
	if q.words.loaded != "Big" {
		t.Fatalf("setup: not adopted (%q)", q.words.status.Text)
	}
	if n := len(wordpreset.Lines(q.vocabSource.Text)); n != 200 {
		t.Fatalf("setup: the box holds %d terms, want the 200 that were applied", n)
	}
	if !strings.Contains(q.words.status.Text, "fewer terms") {
		t.Errorf("reopening said nothing about the dropped terms: %q", q.words.status.Text)
	}
	q.words.doSave()
	if got, _ := os.ReadFile(path); string(got) != b.String() {
		t.Fatal("one press of Save rewrote an over-cap preset")
	}
	if !q.words.saveArmed || !strings.Contains(q.words.status.Text, "will be lost") {
		t.Errorf("status = %q, want the loss explained before Overwrite", q.words.status.Text)
	}
	q.words.doSave()
	r, _, err := wordpreset.Load(wordpreset.Dir(), "Big")
	if err != nil || len(r.Spoken) != 200 {
		t.Errorf("the confirmed Overwrite: %d terms, %v", len(r.Spoken), err)
	}
}
