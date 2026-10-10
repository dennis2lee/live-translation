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

// Every single-key binding, each in its English and Korean-layout form where
// they differ, checked by its effect.
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
	for _, key := range []rune{'f', 'F', 'ㄹ'} {
		t.Run(string(key)+" toggles full screen", func(t *testing.T) {
			w := newTestWindow(t)
			press(t, w, key)
			if !w.win.FullScreen() || !w.settings.Fullscreen {
				t.Errorf("window %v, setting %v", w.win.FullScreen(), w.settings.Fullscreen)
			}
			press(t, w, key)
			if w.win.FullScreen() || w.settings.Fullscreen {
				t.Errorf("pressed again: window %v, setting %v", w.win.FullScreen(), w.settings.Fullscreen)
			}
		})
	}
	for _, key := range []rune{'m', 'M', 'ㅡ'} {
		t.Run(string(key)+" mutes and splits the dot", func(t *testing.T) {
			w, _ := speakingWindow(t)
			w.startPlayer()
			press(t, w, key)
			if !w.muted || !w.view.Muted() {
				t.Errorf("muted %v, dot %v", w.muted, w.view.Muted())
			}
		})
	}
	t.Run("Escape leaves full screen", func(t *testing.T) {
		w := newTestWindow(t)
		press(t, w, 'f')
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
	for _, r := range []rune{'3', '8', 'a', 'z', 'q', 'ㅂ', ' ', 'ㅁ'} {
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
	press(t, w, 'm')
	if !w.view.Muted() {
		t.Error("m did nothing after Preferences")
	}
}
