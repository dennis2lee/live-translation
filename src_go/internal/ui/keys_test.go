package ui

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"

	"github.com/dennis2lee/live-translation/src_go/internal/platform"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
)

// press types one rune into the window the way the driver does when nothing
// has focus: through the canvas's own handler, which is what bindKeys sets.
// Every other test calls the action methods directly, so a binding that
// stopped reaching them would pass them all.
func press(t *testing.T, w *Window, r rune) {
	t.Helper()
	typed := w.win.Canvas().OnTypedRune()
	if typed == nil {
		t.Fatal("no key handler is bound")
	}
	typed(r)
}

// desktopCanvas is a test canvas that, like the desktop driver's, also
// reports key-downs — the test package's own does not, so without this no
// test would run the key-down wiring m and f depend on.
type desktopCanvas struct {
	fyne.Canvas
	down, up func(*fyne.KeyEvent)
}

func (c *desktopCanvas) OnKeyDown() func(*fyne.KeyEvent)     { return c.down }
func (c *desktopCanvas) SetOnKeyDown(f func(*fyne.KeyEvent)) { c.down = f }
func (c *desktopCanvas) OnKeyUp() func(*fyne.KeyEvent)       { return c.up }
func (c *desktopCanvas) SetOnKeyUp(f func(*fyne.KeyEvent))   { c.up = f }

// keyed binds w's keys on a desktop-like canvas and returns it.
func keyed(t *testing.T, w *Window) *desktopCanvas {
	t.Helper()
	c := &desktopCanvas{Canvas: test.NewCanvas()}
	w.bindKeysOn(c)
	if c.OnKeyDown() == nil {
		t.Fatal("no key-down handler is bound")
	}
	return c
}

// pressKey delivers one press the way the desktop driver does, through the
// canvas's own handlers: the key-down, the typed key, then the character the
// key types.
func pressKey(t *testing.T, w *Window, k fyne.KeyName) {
	t.Helper()
	c := keyed(t, w)
	ev := &fyne.KeyEvent{Name: k}
	c.OnKeyDown()(ev)
	c.OnTypedKey()(ev)
	if len(k) == 1 {
		c.OnTypedRune()(rune(strings.ToLower(string(k))[0]))
	}
}

// Every single-key binding, checked by its effect.
func TestEveryKeyDoesItsJob(t *testing.T) {
	t.Run("0 starts translating (and says why it cannot, with no key)", func(t *testing.T) {
		w := newTestWindow(t)
		press(t, w, '0')
		if w.state != translator.StateError || !strings.Contains(w.errorDetail, "API key") {
			t.Errorf("state %v, detail %q", w.state, w.errorDetail)
		}
	})
	t.Run("0 stops a running session", func(t *testing.T) {
		w := newTestWindow(t)
		fakeSession(t, w)
		press(t, w, '0')
		if w.capture != nil {
			t.Error("0 left the capture running")
		}
	})
	for _, c := range []struct {
		key  rune
		from string
		want string
	}{{'1', "ko", "en"}, {'2', "en", "ko"}} {
		t.Run(string(c.key)+" sets the direction", func(t *testing.T) {
			w := newTestWindow(t)
			w.settings.TargetLanguageCode = c.from
			press(t, w, c.key)
			if w.settings.TargetLanguageCode != c.want {
				t.Errorf("target %q, want %q", w.settings.TargetLanguageCode, c.want)
			}
		})
	}
	t.Run("9 toggles complete phrases", func(t *testing.T) {
		w := newTestWindow(t)
		before := w.settings.CompletePhrasesOnly
		press(t, w, '9')
		if w.settings.CompletePhrasesOnly == before {
			t.Error("9 did not toggle")
		}
		press(t, w, '9')
		if w.settings.CompletePhrasesOnly != before {
			t.Error("9 twice did not toggle back")
		}
	})
	t.Run("f toggles full screen", func(t *testing.T) {
		w := newTestWindow(t)
		pressKey(t, w, fyne.KeyF)
		if !w.win.FullScreen() || !w.settings.Fullscreen {
			t.Errorf("window %v, setting %v", w.win.FullScreen(), w.settings.Fullscreen)
		}
		pressKey(t, w, fyne.KeyF)
		if w.win.FullScreen() || w.settings.Fullscreen {
			t.Errorf("pressed again: window %v, setting %v", w.win.FullScreen(), w.settings.Fullscreen)
		}
	})
	t.Run("m mutes and splits the dot", func(t *testing.T) {
		w, _ := speakingWindow(t)
		w.startPlayer()
		pressKey(t, w, fyne.KeyM)
		if !w.muted || !w.view.Muted() {
			t.Errorf("muted %v, dot %v", w.muted, w.view.Muted())
		}
	})
	t.Run("Escape leaves full screen", func(t *testing.T) {
		w := newTestWindow(t)
		pressKey(t, w, fyne.KeyF)
		w.win.Canvas().OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyEscape})
		if w.win.FullScreen() {
			t.Error("Escape did not leave full screen")
		}
	})
}

// Keys with no binding change nothing — including the digits next to the
// bound ones, and the jamo of unbound keys.
func TestUnboundKeysDoNothing(t *testing.T) {
	w := newTestWindow(t)
	before := w.settings
	// m, f and their jamo arrive as runes too, and must not act a second time:
	// those two keys are bound by physical key.
	for _, r := range []rune{'3', '8', 'a', 'z', 'q', 'ㅂ', ' ', 'ㅁ', 'm', 'M', 'ㅡ', 'f', 'F', 'ㄹ'} {
		press(t, w, r)
	}
	if !w.settings.Equal(before) || w.state != translator.StateIdle || w.win.FullScreen() ||
		w.muted || w.view.Muted() {
		t.Error("an unbound key changed something")
	}
}

// The two menu shortcuts are ⌘ on macOS and Ctrl on Windows: Fyne's
// "shortcut default" modifier is both, and the driver finds them by name on
// every platform. Each must be on the item that does the job.
func TestTheMenuShortcutsAreOnTheirItems(t *testing.T) {
	w := newTestWindow(t)
	want := map[string]fyne.KeyName{"Start Translating": fyne.KeyR, "Preferences…": fyne.KeyComma}
	found := map[string]bool{}
	for _, m := range w.win.MainMenu().Items {
		for _, item := range m.Items {
			key, ok := want[item.Label]
			if !ok {
				continue
			}
			s, ok := item.Shortcut.(*desktop.CustomShortcut)
			if !ok || s.KeyName != key || s.Modifier != fyne.KeyModifierShortcutDefault {
				t.Errorf("%s: shortcut %#v", item.Label, item.Shortcut)
			}
			found[item.Label] = true
		}
	}
	for label := range want {
		if !found[label] {
			t.Errorf("no %q item", label)
		}
	}
}

// Keys keep working after Preferences has been open and closed — the path a
// user takes to switch speech on before pressing m.
func TestKeysStillWorkAfterPreferences(t *testing.T) {
	w, _ := speakingWindow(t)
	w.startPlayer()
	w.OpenPreferences()
	p := w.prefs
	w.win.Canvas().Focus(p.speakAloud)
	p.dialog.Hide()
	if f := w.win.Canvas().Focused(); f != nil {
		t.Fatalf("%T still has focus after Preferences closed — every key goes to it", f)
	}
	pressKey(t, w, fyne.KeyM)
	if !w.view.Muted() {
		t.Error("m did nothing after Preferences")
	}
}

// Under a Korean input source the m key's character is a jamo the input
// method holds until the next key, so the binding is by physical key: one
// press acts at once, whatever the input source.
func TestMAndFAreBoundByPhysicalKey(t *testing.T) {
	w := newTestWindow(t)
	w.win.Canvas().OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyM})
	if !w.muted {
		t.Error("the m key-down did not mute")
	}
}

// A full press — key-down, key, character — toggles once. Binding the m
// character as well as the key would toggle twice, and look like a dead key.
func TestOnePressTogglesOnce(t *testing.T) {
	w := newTestWindow(t)
	pressKey(t, w, fyne.KeyM)
	if !w.muted {
		t.Error("one press of m did not mute, or muted twice")
	}
	pressKey(t, w, fyne.KeyF)
	if !w.win.FullScreen() {
		t.Error("one press of f did not toggle full screen, or toggled twice")
	}
}

// Holding m repeats the typed key but not the key-down: one toggle, not one
// per repeat landing on whatever state the release catches.
func TestHoldingMTogglesOnce(t *testing.T) {
	w := newTestWindow(t)
	c := keyed(t, w)
	ev := &fyne.KeyEvent{Name: fyne.KeyM}
	c.OnKeyDown()(ev)
	// An even count: unfiltered, the repeats would land back on unmuted.
	for range 4 {
		c.OnTypedKey()(ev)
	}
	if !w.muted {
		t.Error("holding m left the translation unmuted")
	}
}

// Under a Korean input source the layout may not name the key "M"; the scan
// code still identifies it. A Latin name is trusted as it is.
func TestTheMKeyIsKnownByItsScanCode(t *testing.T) {
	for _, c := range []struct {
		letter rune
		check  func(*Window) bool
	}{
		{'M', func(w *Window) bool { return w.muted }},
		{'F', func(w *Window) bool { return w.win.FullScreen() }},
	} {
		w := newTestWindow(t)
		cv := keyed(t, w)
		// Fyne v2.8.0 gives a key the layout names with a multi-byte
		// character no name at all; the scan code is what is left.
		ev := &fyne.KeyEvent{Physical: fyne.HardwareKey{ScanCode: platform.ScanCode(c.letter)}}
		cv.OnKeyDown()(ev)
		cv.OnTypedKey()(ev)
		if !c.check(w) {
			t.Errorf("the unnamed %c key did not act", c.letter)
		}
	}
	other := &fyne.KeyEvent{Name: fyne.KeyComma, Physical: fyne.HardwareKey{ScanCode: platform.ScanCode('M')}}
	if isKey(other, fyne.KeyM, 'M') {
		t.Error("a key the layout names was taken for M by position")
	}
}

// Modifier combinations are shortcuts: Fyne sends the key-down but never the
// typed key, so Ctrl+M or Cmd+F must not toggle.
func TestAModifiedPressDoesNotToggle(t *testing.T) {
	w := newTestWindow(t)
	c := keyed(t, w)
	held := fyne.KeyModifier(0)
	old := heldModifiers
	heldModifiers = func() fyne.KeyModifier { return held }
	t.Cleanup(func() { heldModifiers = old })

	// Ctrl+M: the key-down arrives, the typed key does not (a shortcut).
	held = fyne.KeyModifierControl
	c.OnKeyDown()(&fyne.KeyEvent{Name: fyne.KeyM})
	// Ctrl comes up with M still held; the next repeat reaches the typed key.
	held = 0
	c.OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyM})
	if w.muted {
		t.Error("a repeat after Ctrl+M toggled the mute")
	}
	// Shift is not a shortcut modifier: Shift+M is still m.
	held = fyne.KeyModifierShift
	c.OnKeyDown()(&fyne.KeyEvent{Name: fyne.KeyM})
	c.OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyM})
	if !w.muted {
		t.Error("Shift+M did not mute")
	}
}

// The menu item is the key's twin, ticked while muted.
func TestTheMuteMenuItem(t *testing.T) {
	w := newTestWindow(t)
	item := find(t, w, "Mute Translation (m)")
	item.Action()
	if !w.muted || !item.Checked {
		t.Errorf("muted %v, ticked %v", w.muted, item.Checked)
	}
	item.Action()
	if w.muted || item.Checked {
		t.Errorf("unmuted: muted %v, ticked %v", w.muted, item.Checked)
	}
}
