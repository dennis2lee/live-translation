package ui

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"

	"github.com/dennis2lee/live-translation/src_go/internal/captions"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/fonts"
)

// anInstalledFont is a family other than the default, or a skip.
func anInstalledFont(t *testing.T, want ...string) string {
	t.Helper()
	for _, name := range want {
		if fonts.Has(name) {
			return name
		}
	}
	if len(want) > 0 {
		t.Skipf("none of %q installed", want)
	}
	all := fonts.Families()
	if len(all) == 0 {
		t.Skip("no system fonts could be read here")
	}
	return all[0]
}

// showing puts a caption view on a test window with one settled line.
func showing(t *testing.T, s config.Settings, text string) (*CaptionView, *captionRenderer) {
	t.Helper()
	v := NewCaptionView(s)
	v.boxAlpha = 1
	v.SetLines([]captions.Line{{Text: text, Settled: true}})
	win := test.NewWindow(v)
	t.Cleanup(win.Close)
	win.SetPadded(false)
	win.Resize(fyne.NewSize(900, 400))
	return v, test.WidgetRenderer(v).(*captionRenderer)
}

func shownTexts(r *captionRenderer) []*canvas.Text {
	var out []*canvas.Text
	for _, tx := range r.texts {
		if tx.Visible() && tx.Text != "" {
			out = append(out, tx)
		}
	}
	return out
}

// The chosen family is what the caption text is drawn in.
func TestTheCaptionFontReachesTheText(t *testing.T) {
	name := anInstalledFont(t)
	s := config.Default()
	s.CaptionFont = name
	v, r := showing(t, s, "A caption in the chosen font.")
	if v.Font() == nil {
		t.Fatalf("%q did not load", name)
	}
	texts := shownTexts(r)
	if len(texts) == 0 {
		t.Fatal("nothing drawn")
	}
	for _, tx := range texts {
		if tx.FontSource != v.Font() {
			t.Errorf("row %q drawn in %v, want the caption font", tx.Text, tx.FontSource)
		}
	}
}

// The default draws in the theme's font, as before.
func TestTheDefaultFontIsTheThemes(t *testing.T) {
	v, r := showing(t, config.Default(), "A caption.")
	if v.Font() != nil {
		t.Error("the default font is not the theme's")
	}
	for _, tx := range shownTexts(r) {
		if tx.FontSource != nil {
			t.Errorf("row %q has a font source with the default chosen", tx.Text)
		}
	}
}

// A family that is no longer installed is drawn in the default, and the
// setting is kept so it comes back with the font.
func TestAnUninstalledFontFallsBackToTheDefault(t *testing.T) {
	s := config.Default()
	s.CaptionFont = "No Such Font Family"
	v, r := showing(t, s, "Still readable.")
	if v.Font() != nil {
		t.Error("an uninstalled font produced a font source")
	}
	if len(shownTexts(r)) == 0 {
		t.Error("nothing drawn with an uninstalled font")
	}
	if v.settings.CaptionFont != "No Such Font Family" {
		t.Error("the setting was dropped")
	}
}

// Changing the font applies at once, and the same family is one resource
// however often it is applied — Fyne caches a face per resource.
func TestTheFontChangesInPlace(t *testing.T) {
	name := anInstalledFont(t)
	s := config.Default()
	v, r := showing(t, s, "A caption.")
	s.CaptionFont = name
	v.ApplySettings(s)
	first := v.Font()
	if first == nil {
		t.Fatalf("%q did not load", name)
	}
	v.Refresh()
	for _, tx := range shownTexts(r) {
		if tx.FontSource != first {
			t.Errorf("row %q not redrawn in the new font", tx.Text)
		}
	}
	s.CaptionFont = ""
	v.ApplySettings(s)
	s.CaptionFont = name
	v.ApplySettings(s)
	if v.Font() != first {
		t.Error("applying the same family again built a new resource")
	}
}

// Lines are broken by measuring in the caption font, not the default. A
// wide monospaced face and the default proportional one break the same
// text differently; measured in the wrong one, the rows would overflow.
func TestLinesAreWrappedInTheCaptionFont(t *testing.T) {
	name := anInstalledFont(t, "Courier New", "Courier", "Menlo", "Consolas")
	res := captionFont(name)
	if res == nil {
		t.Fatalf("%q did not load", name)
	}
	text := "The quick brown fox jumps over the lazy dog, and then it does it again, slowly."
	const size, width = 40, 600
	face := Face{Source: res}
	for _, piece := range face.Wrap(text, size, width) {
		if got := face.measure(piece, size); got > width {
			t.Errorf("row %q is %.0f px in %s, past %d", piece, got, name, width)
		}
	}
	if face.measure(text, size) == (Face{}).measure(text, size) {
		t.Errorf("%s measured the same as the default — the source is not being used", name)
	}
}

// Preferences lists the families, applies the choice, and keeps a family it
// cannot find rather than quietly resetting it.
func TestPreferencesChoosesTheCaptionFont(t *testing.T) {
	name := anInstalledFont(t)
	w := newTestWindow(t)
	p := openPrefs(t, w)
	if p.captionFont.Selected != defaultFontLabel {
		t.Errorf("a fresh install shows %q, want the default", p.captionFont.Selected)
	}
	p.captionFont.SetSelected(name)
	p.apply()
	if w.settings.CaptionFont != name {
		t.Errorf("CaptionFont = %q after Apply, want %q", w.settings.CaptionFont, name)
	}
	if w.view.Font() == nil {
		t.Error("the window's captions did not take the font")
	}
}

func TestPreferencesKeepsAnUninstalledFont(t *testing.T) {
	w := newTestWindow(t)
	w.update(func(s *config.Settings) { s.CaptionFont = "No Such Font Family" })
	p := openPrefs(t, w)
	if p.captionFont.Selected != "No Such Font Family (not installed)" {
		t.Errorf("the dialog shows %q", p.captionFont.Selected)
	}
	p.apply()
	if w.settings.CaptionFont != "No Such Font Family" {
		t.Errorf("opening and applying the dialog changed CaptionFont to %q", w.settings.CaptionFont)
	}
}

// An older settings file has no caption_font; it reads as the default.
func TestAnOlderSettingsFileUsesTheDefaultFont(t *testing.T) {
	if config.Default().CaptionFont != "" {
		t.Error("the default caption font is not the built-in one")
	}
}

// spacedFamily is an installed family with a space in its name, or a skip.
func spacedFamily(t *testing.T) string {
	t.Helper()
	for _, n := range fonts.Families() {
		if strings.Contains(n, " ") {
			return n
		}
	}
	t.Skip("no installed family has a space in its name")
	return ""
}

// A stored name that differs only in spacing or case is the same family.
// Preferences used to show it as the default, and Apply then dropped it (the
// red team's first finding).
func TestPreferencesFindsAFamilyStoredWithOtherSpacing(t *testing.T) {
	name := spacedFamily(t)
	squashed := strings.ToLower(strings.ReplaceAll(name, " ", ""))
	w := newTestWindow(t)
	w.update(func(s *config.Settings) { s.CaptionFont = squashed })
	p := openPrefs(t, w)
	if p.captionFont.Selected != name {
		t.Errorf("stored %q shows as %q, want %q", squashed, p.captionFont.Selected, name)
	}
	p.apply()
	if !fonts.SameFamily(w.settings.CaptionFont, name) {
		t.Errorf("Apply changed CaptionFont to %q", w.settings.CaptionFont)
	}
}

// An outside change to a differently cased name selects the listed family
// rather than adding a second entry for it.
func TestAnOutsideFontChangeAddsNoDuplicate(t *testing.T) {
	name := anInstalledFont(t)
	w := newTestWindow(t)
	p := openLivePrefs(t, w)
	before := len(p.captionFont.Options)
	w.update(func(s *config.Settings) { s.CaptionFont = strings.ToUpper(name) })
	if len(p.captionFont.Options) != before || p.captionFont.Selected != name {
		t.Errorf("options %d -> %d, selected %q", before, len(p.captionFont.Options), p.captionFont.Selected)
	}
	w.update(func(s *config.Settings) { s.CaptionFont = "Gone Font" })
	if p.captionFont.Selected != "Gone Font (not installed)" {
		t.Errorf("an uninstalled family from outside shows as %q", p.captionFont.Selected)
	}
}

// One loaded copy per family, whatever case or spacing names it.
func TestOneResourcePerFamily(t *testing.T) {
	name := anInstalledFont(t)
	a, b := captionFont(name), captionFont(strings.ToUpper(name))
	if a == nil || a != b {
		t.Errorf("%q and its upper case gave %p and %p", name, a, b)
	}
}
