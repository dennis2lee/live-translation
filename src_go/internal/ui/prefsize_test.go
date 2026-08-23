package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
)

// tabsOf builds every tab the dialog shows, in the order show() builds them.
func tabsOf(t *testing.T) []struct {
	name string
	obj  fyne.CanvasObject
} {
	t.Helper()
	w := newTestWindow(t)
	p := openPrefs(t, w)
	return []struct {
		name string
		obj  fyne.CanvasObject
	}{
		{"API", p.connectionTab()},
		{"Input", p.inputTab()},
		{"Output", p.outputTab()},
		{"Captions", p.captionsTab()},
		{"Colours", p.coloursTab()},
		{"Filter", p.filterTab()},
		{"Words", p.vocabularyTab()},
		{"Behaviour", p.behaviourTab()},
	}
}

// The dialog is exactly as large as its largest tab, so a single overgrown tab
// makes every other one sit in a window far bigger than it needs.
//
// Before the split, Audio wanted 668 px of height and Vocabulary 884 px of
// width against a 640x520 dialog: two of the six tabs did not fit, and the
// other four were shown in a window sized for tabs they had nothing to do with.
// This is the check that stops that happening again quietly.
func TestNoTabOverflowsTheDialog(t *testing.T) {
	for _, c := range tabsOf(t) {
		m := c.obj.MinSize()
		t.Logf("%-11s %4.0f x %4.0f", c.name, m.Width, m.Height)
		if m.Width > float32(prefsWidth) {
			t.Errorf("%s needs %.0f px of width, dialog is %d", c.name, m.Width, prefsWidth)
		}
		if m.Height > float32(prefsContentHeight) {
			t.Errorf("%s needs %.0f px of height, content area is %d",
				c.name, m.Height, prefsContentHeight)
		}
	}
}

// A label that is not told to wrap asks for its whole text on one line, and one
// such label set the width of the entire dialog. Both offenders are named here
// because the failure is silent: the dialog simply grows.
func TestLongLabelsWrap(t *testing.T) {
	w := newTestWindow(t)
	p := openPrefs(t, w)

	for _, c := range []struct {
		name string
		obj  fyne.CanvasObject
	}{
		{"Words", p.vocabularyTab()},
		{"API", p.connectionTab()},
	} {
		if got := c.obj.MinSize().Width; got > float32(prefsWidth) {
			t.Errorf("%s is %.0f px wide — a label is missing Wrapping", c.name, got)
		}
	}
}

// The tab bar has to fit too. Seven labels in a row is the trade the split
// makes, and a bar wider than the dialog is how that trade goes wrong — Fyne
// does not shrink labels, it overflows them into a menu the user has to find.
//
// This also checks prefsChrome, which is otherwise a guess: the assembled tabs
// minus the tallest tab is what the bar actually costs.
func TestTheAssembledTabsFitTheDialog(t *testing.T) {
	w := newTestWindow(t)
	p := openPrefs(t, w)
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
	m := tabs.MinSize()
	t.Logf("assembled tabs %.0f x %.0f (dialog %d x %d)", m.Width, m.Height, prefsWidth, prefsHeight)

	if m.Width > float32(prefsWidth) {
		t.Errorf("the tab bar and its widest tab need %.0f px, dialog is %d", m.Width, prefsWidth)
	}
	if m.Height > float32(prefsHeight) {
		t.Errorf("the tabs need %.0f px of height, dialog is %d", m.Height, prefsHeight)
	}
}
