package ui

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"

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

// pressKey delivers a physical key the way the driver does: the key-down
// handler, then the key-up.
func pressKey(t *testing.T, w *Window, k fyne.KeyName) {
	t.Helper()
	w.win.Canvas().OnTypedKey()(&fyne.KeyEvent{Name: k})
	w.keyUp(&fyne.KeyEvent{Name: k})
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
	if !w.settings.Equal(before) || w.state != translator.StateIdle || w.win.FullScreen() {
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

// On Windows an input method can swallow the key-down entirely; the key-up
// alone must then do the job — and only then, never as a second toggle.
func TestAKeyUpActsOnlyWhenTheKeyDownWasSwallowed(t *testing.T) {
	w := newTestWindow(t)
	up := w.keyUp
	up(&fyne.KeyEvent{Name: fyne.KeyM}) // no key-down: an IME took it
	if !w.muted {
		t.Fatal("a lone key-up did not mute")
	}
	pressKey(t, w, fyne.KeyM) // down and up: one toggle, not two
	if w.muted {
		t.Error("a full press toggled twice, or not at all")
	}
	up(&fyne.KeyEvent{Name: fyne.KeyF})
	if !w.win.FullScreen() {
		t.Error("a lone f key-up did not toggle full screen")
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
