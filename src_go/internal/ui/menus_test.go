package ui

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"

	"github.com/dennis2lee/live-translation/src_go/internal/config"
)

// walk visits every item in a menu, submenu parents included — a parent is a
// visible row too, and missing one is exactly the kind of gap this checks for.
func walk(menu *fyne.Menu, visit func(*fyne.MenuItem)) {
	for _, item := range menu.Items {
		if item.IsSeparator {
			continue
		}
		visit(item)
		if item.ChildMenu != nil {
			walk(item.ChildMenu, visit)
		}
	}
}

func allItems(w *Window) []*fyne.MenuItem {
	var out []*fyne.MenuItem
	for _, menu := range w.menus.menu.Items {
		walk(menu, func(item *fyne.MenuItem) { out = append(out, item) })
	}
	return out
}

// Every menu item must either do something or say why it cannot. A nil action
// on an enabled item is a dead menu entry — except on a submenu parent, whose
// job is to open its submenu, and on the deliberately disabled status line.
func TestNoDeadMenuItems(t *testing.T) {
	w := newTestWindow(t)
	for _, item := range allItems(w) {
		if item.ChildMenu != nil || item.Disabled {
			continue
		}
		if item.Action == nil {
			t.Errorf("menu item %q is enabled but does nothing", item.Label)
		}
	}
}

// The menu bar's shape, so an item cannot quietly go missing.
func TestMenuBarContents(t *testing.T) {
	w := newTestWindow(t)
	var labels []string
	for _, item := range allItems(w) {
		labels = append(labels, item.Label)
	}
	joined := strings.Join(labels, "\n")

	for _, want := range []string{
		"Start Translating", "Stop Translating", "Clear Captions",
		"Toggle Translation (0)", "Save Transcript", "Open Transcript Folder",
		"Preferences…", "Show Direction", "Complete Phrases Only (9)",
		"Always Show Caption Box", "Show Source Text", "Full Screen",
		"Reset to Defaults", "Korean→English (1)", "English→Korean (2)",
		"Top", "Middle", "Bottom", "Audio Input", "Refresh Device List",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("menu is missing %q\ngot:\n%s", want, joined)
		}
	}
	// The Python build's font sizes and line counts, as menu items.
	for _, size := range FontSizeChoices {
		if !strings.Contains(joined, itoa(size)) {
			t.Errorf("font size %d is not in the menu", size)
		}
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

// find returns the item with this label, failing the test if it is absent.
func find(t *testing.T, w *Window, label string) *fyne.MenuItem {
	t.Helper()
	for _, item := range allItems(w) {
		if item.Label == label {
			return item
		}
	}
	t.Fatalf("no menu item labelled %q", label)
	return nil
}

// Every checkable item must drive its setting *and* follow it back — the tick
// and the state cannot disagree.
func TestCheckableItemsDriveAndFollowTheirSetting(t *testing.T) {
	cases := []struct {
		label string
		get   func(config.Settings) bool
	}{
		{"Save Transcript", func(s config.Settings) bool { return s.SaveTranscript }},
		{"Show Direction", func(s config.Settings) bool { return s.ShowDirectionHint }},
		{"Complete Phrases Only (9)", func(s config.Settings) bool { return s.CompletePhrasesOnly }},
		{"Always Show Caption Box", func(s config.Settings) bool { return s.FixedBox }},
		{"Show Source Text", func(s config.Settings) bool { return s.ShowSourceText }},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			w := newTestWindow(t)
			item := find(t, w, c.label)
			before := c.get(w.settings)
			if item.Checked != before {
				t.Fatalf("tick starts at %v but the setting is %v", item.Checked, before)
			}
			item.Action()
			if c.get(w.settings) == before {
				t.Fatalf("choosing %q did not change the setting", c.label)
			}
			if item.Checked != c.get(w.settings) {
				t.Errorf("tick is %v but the setting is %v", item.Checked, c.get(w.settings))
			}
			item.Action() // and back
			if c.get(w.settings) != before {
				t.Errorf("choosing it twice did not return to %v", before)
			}
		})
	}
}

// The direction items are a radio group: exactly one tick, and it follows the
// target language.
func TestDirectionItemsAreExclusive(t *testing.T) {
	w := newTestWindow(t)
	ko := find(t, w, "Korean→English (1)")
	en := find(t, w, "English→Korean (2)")

	en.Action()
	if w.settings.TargetLanguageCode != "ko" {
		t.Fatalf("English→Korean set the target to %q", w.settings.TargetLanguageCode)
	}
	if !en.Checked || ko.Checked {
		t.Errorf("ticks after EN→KO: ko=%v en=%v, want exactly one", ko.Checked, en.Checked)
	}
	ko.Action()
	if w.settings.TargetLanguageCode != "en" {
		t.Fatalf("Korean→English set the target to %q", w.settings.TargetLanguageCode)
	}
	if !ko.Checked || en.Checked {
		t.Errorf("ticks after KO→EN: ko=%v en=%v, want exactly one", ko.Checked, en.Checked)
	}
}

// Font size, line count and box position are radio groups over values.
func TestValueGroupsFollowTheSetting(t *testing.T) {
	w := newTestWindow(t)

	find(t, w, "72").Action()
	if w.settings.FontSize != 72 {
		t.Errorf("FontSize = %d", w.settings.FontSize)
	}
	if !find(t, w, "72").Checked || find(t, w, "44").Checked {
		t.Error("the font-size tick did not move")
	}

	find(t, w, "Top").Action()
	if w.settings.Position() != config.BoxTop {
		t.Errorf("position = %v", w.settings.Position())
	}
	if !find(t, w, "Top").Checked || find(t, w, "Bottom").Checked {
		t.Error("the position tick did not move")
	}
}

// Start and Stop are separate items so a mis-click cannot stop a live session,
// and they enable and disable off the session objects rather than the state —
// a start that failed leaves ERROR behind with nothing running, and Start must
// stay usable.
func TestStartAndStopEnablement(t *testing.T) {
	w := newTestWindow(t)
	start, stop := find(t, w, "Start Translating"), find(t, w, "Stop Translating")

	if start.Disabled {
		t.Error("Start is disabled with nothing running")
	}
	if !stop.Disabled {
		t.Error("Stop is enabled with nothing running")
	}

	w.failToStart("API key required — enter it in Preferences")
	if start.Disabled {
		t.Error("Start is disabled after a start that never started anything")
	}
	if stop.Disabled {
		t.Error("Stop should stay reachable in the error state, to clear it")
	}
}

// The status line reports the state and the cause, because a red dot alone is
// not actionable.
func TestStatusItemReportsStateAndCause(t *testing.T) {
	w := newTestWindow(t)
	status := find(t, w, "Status: Idle")
	if !status.Disabled {
		t.Error("the status line should not be clickable")
	}
	w.failToStart("API key is not valid")
	if !strings.Contains(status.Label, "Error") || !strings.Contains(status.Label, "API key is not valid") {
		t.Errorf("status label = %q", status.Label)
	}
}

// Clear Captions must not truncate the transcript — the same invariant the
// buffer holds, reached through the menu.
func TestClearCaptionsMenuItemKeepsWhatWasSaid(t *testing.T) {
	w := newTestWindow(t)
	w.buffer.Feed("A settled sentence.", nowSeconds())
	find(t, w, "Clear Captions").Action()
	if len(w.view.lines) != 0 {
		t.Error("the display was not cleared")
	}
	// Drained into the (absent) transcript rather than stranded on the buffer.
	if got := len(w.buffer.DrainNewCommits()); got != 0 {
		t.Errorf("%d lines were left on the buffer", got)
	}
}

// Reset restores every default, and the menu follows.
func TestResetToDefaults(t *testing.T) {
	w := newTestWindow(t)
	w.update(func(s *config.Settings) { s.FontSize = 96; s.MaxLines = 1 })
	find(t, w, "Reset to Defaults").Action()

	if w.settings.FontSize != config.Default().FontSize {
		t.Errorf("FontSize = %d after reset", w.settings.FontSize)
	}
	if !find(t, w, itoa(config.Default().FontSize)).Checked {
		t.Error("the menu tick did not follow the reset")
	}
}
