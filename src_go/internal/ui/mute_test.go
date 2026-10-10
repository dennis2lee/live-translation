package ui

import (
	"strings"
	"testing"

	"github.com/dennis2lee/live-translation/src_go/internal/config"
)

// lastVolume is the gain the player is actually running at.
func lastVolume(t *testing.T, f *fakeSpeaker) float64 {
	t.Helper()
	history := f.volumeHistory()
	if len(history) == 0 {
		t.Fatal("the player was never given a volume")
	}
	return history[len(history)-1]
}

func TestMuteSilencesThePlayer(t *testing.T) {
	w, factory := speakingWindow(t)
	w.startPlayer()
	player := factory.last()

	w.ToggleMute()

	if !w.muted {
		t.Error("m did not mute")
	}
	if got := lastVolume(t, player); got != 0 {
		t.Errorf("gain after mute = %v, want 0", got)
	}
}

// Mute is a gain change, not a device change. Reopening an output device takes
// long enough to clip the first word back.
func TestMuteDoesNotCloseTheDevice(t *testing.T) {
	w, factory := speakingWindow(t)
	w.startPlayer()
	player := factory.last()

	w.ToggleMute()

	if _, stops := player.counts(); stops != 0 {
		t.Errorf("Stop called %d times on mute, want 0", stops)
	}
	if w.player.Load() == nil {
		t.Error("mute dropped the player")
	}
	if factory.count() != 1 {
		t.Errorf("built %d players, want 1 — mute must not reopen", factory.count())
	}
}

func TestPressingMTwiceRestoresTheVolume(t *testing.T) {
	w, factory := speakingWindow(t)
	w.settings.SpeechVolume = 0.6
	w.startPlayer()
	player := factory.last()

	w.ToggleMute()
	w.ToggleMute()

	if w.muted {
		t.Error("the second m did not unmute")
	}
	if got := lastVolume(t, player); got != 0.6 {
		t.Errorf("gain after unmute = %v, want the settings volume 0.6", got)
	}
}

// A session that starts while muted stays muted, or m would quietly undo itself
// at every stop and start.
func TestAPlayerStartedWhileMutedIsSilent(t *testing.T) {
	w, factory := speakingWindow(t)
	w.startPlayer()
	w.ToggleMute()

	w.stopPlayer()
	w.startPlayer()

	if got := lastVolume(t, factory.last()); got != 0 {
		t.Errorf("a player opened while muted runs at %v, want 0", got)
	}
}

// The slider moves the volume that unmuting will restore, and nothing else.
// Reading Settings.SpeechVolume directly here is what would break it.
func TestMovingTheVolumeWhileMutedStaysSilent(t *testing.T) {
	w, factory := speakingWindow(t)
	w.startPlayer()
	player := factory.last()
	w.ToggleMute()

	next := w.settings
	next.SpeechVolume = 0.3
	w.applySettings(next)

	if got := lastVolume(t, player); got != 0 {
		t.Errorf("gain = %v after moving the slider while muted, want 0", got)
	}

	w.ToggleMute()
	if got := lastVolume(t, player); got != 0.3 {
		t.Errorf("gain after unmute = %v, want the new volume 0.3", got)
	}
}

// Muting before anything is playing must not panic, and must still be in force
// when a player does appear.
func TestMuteWithNoPlayerRunning(t *testing.T) {
	w, factory := speakingWindow(t)

	w.ToggleMute()
	w.startPlayer()

	if got := lastVolume(t, factory.last()); got != 0 {
		t.Errorf("gain = %v, want 0 — the mute was set before the player existed", got)
	}
}

// --- the dot's mute half ---------------------------------------------------

// Unmuted, the dot is what it has always been: one colour, decided by the
// session state alone.
func TestAnUnmutedDotIsOneColour(t *testing.T) {
	for _, i := range []Indicator{IndicatorIdle, IndicatorBusy, IndicatorLive, IndicatorError} {
		left, right := dotHalves(i, false)
		if left != right {
			t.Errorf("%s unmuted: halves differ (%v vs %v), want one colour", i, left, right)
		}
		if want := dotColour(i); right != want {
			t.Errorf("%s unmuted: dot = %v, want %v", i, right, want)
		}
	}
}

// Muting is a second fact about the session, so it takes the half the status
// is not using. The status half must go on saying exactly what it said before.
func TestMutingColoursOnlyTheLeftHalf(t *testing.T) {
	for _, i := range []Indicator{IndicatorIdle, IndicatorBusy, IndicatorLive, IndicatorError} {
		left, right := dotHalves(i, true)
		if left != colourMuted {
			t.Errorf("%s muted: left half = %v, want the mute red %v", i, left, colourMuted)
		}
		if want := dotColour(i); right != want {
			t.Errorf("%s muted: right half = %v, want the unchanged status %v", i, right, want)
		}
	}
}

// A muted live session and a broken one must not be the same red on the same
// dot, or muting would look like a failure.
func TestTheMuteRedIsNotTheErrorRed(t *testing.T) {
	if colourMuted == colourError {
		t.Fatal("the mute half and the error dot are the same colour")
	}
	mutedLive, _ := dotHalves(IndicatorLive, true)
	if mutedLive == dotColour(IndicatorError) {
		t.Error("a muted live session paints the error colour")
	}
}

// m has to leave something on screen. The notice it prints scrolls away; a
// session muted ten minutes ago must still be readable from the box.
func TestMutingShowsOnTheDot(t *testing.T) {
	w, _ := speakingWindow(t)
	w.startPlayer()

	if w.view.Muted() {
		t.Fatal("setup: the dot started muted")
	}
	w.ToggleMute()
	if !w.view.Muted() {
		t.Error("m muted the sound but not the dot")
	}
	w.ToggleMute()
	if w.view.Muted() {
		t.Error("m unmuted the sound but left the dot red")
	}
}

// The renderer draws the split. Centre-left and centre-right are the two
// halves; a corner is outside the disc and must stay transparent, or the dot
// would be a square.
func TestTheDotIsDrawnAsASplitDisc(t *testing.T) {
	v := NewCaptionView(config.Default())
	v.indicator = IndicatorLive
	r := &captionRenderer{view: v}

	const n = 32
	leftPix := r.dotPixel(n/4, n/2, n, n)
	rightPix := r.dotPixel(3*n/4, n/2, n, n)
	corner := r.dotPixel(0, 0, n, n)

	if corner.A != 0 {
		t.Errorf("the dot's corner is painted (alpha %d) — it is a square, not a disc", corner.A)
	}
	if leftPix.A == 0 || rightPix.A == 0 {
		t.Fatalf("the dot's middle is transparent: left %v right %v", leftPix, rightPix)
	}
	if leftPix != rightPix {
		t.Errorf("unmuted halves differ: %v vs %v", leftPix, rightPix)
	}

	v.muted = true
	leftPix = r.dotPixel(n/4, n/2, n, n)
	rightPix = r.dotPixel(3*n/4, n/2, n, n)
	if leftPix == rightPix {
		t.Fatal("muted, the dot is still one colour")
	}
	if leftPix.R != colourMuted.R || leftPix.G != colourMuted.G || leftPix.B != colourMuted.B {
		t.Errorf("left half = %v, want the mute red", leftPix)
	}
	live := dotColour(IndicatorLive)
	if rightPix.R != live.R || rightPix.G != live.G || rightPix.B != live.B {
		t.Errorf("right half = %v, want the live green", rightPix)
	}
}

// The pulse is the dot's, not one half's. A red half holding still against a
// breathing green one reads as two indicators that happen to touch.
func TestThePulseDimsBothHalves(t *testing.T) {
	v := NewCaptionView(config.Default())
	v.indicator = IndicatorBusy
	v.muted = true
	v.dotAlpha = 0.5
	r := &captionRenderer{view: v}

	const n = 32
	left := r.dotPixel(n/4, n/2, n, n)
	right := r.dotPixel(3*n/4, n/2, n, n)
	if left.A == 0xFF || right.A == 0xFF {
		t.Fatalf("the pulse did not reach the dot: left %v right %v", left, right)
	}
	if left.A != right.A {
		t.Errorf("halves pulse apart: left alpha %d, right alpha %d", left.A, right.A)
	}
}

// The key itself, not the method: every other test here calls
// ToggleMute directly, so a binding that stopped reaching it would pass
// them all. m, M and ㅡ (what the m key types under the Korean input source)
// must each mute the sound and split the dot, and press again to undo.
func TestTheMKeyMutesAndSplitsTheDot(t *testing.T) {
	for _, key := range []rune{'m', 'M', 'ㅡ'} {
		w, _ := speakingWindow(t)
		w.startPlayer()
		typed := w.win.Canvas().OnTypedRune()
		if typed == nil {
			t.Fatal("no key handler is bound")
		}
		typed(key)
		if !w.muted || !w.view.Muted() {
			t.Errorf("%q: muted %v, dot split %v", key, w.muted, w.view.Muted())
		}
		typed(key)
		if w.muted || w.view.Muted() {
			t.Errorf("%q pressed again: muted %v, dot split %v", key, w.muted, w.view.Muted())
		}
	}
}

// m mutes the translation whether or not speech is on: the room sees nothing
// and the dot says so.
func TestMuteWorksWithSpeechOff(t *testing.T) {
	w := newTestWindow(t)
	if w.settings.SpeakTranslation {
		t.Fatal("setup: speech is on")
	}
	w.ToggleMute()
	if !w.muted || !w.view.Muted() {
		t.Errorf("muted %v, dot %v", w.muted, w.view.Muted())
	}
	w.ToggleMute()
	if w.muted || w.view.Muted() {
		t.Errorf("unmuted: muted %v, dot %v", w.muted, w.view.Muted())
	}
}

// While muted no caption reaches the screen, though the buffer — which also
// feeds the transcript — goes on taking them. Unmuting shows what is said
// from then on, not what piled up while muted.
func TestMuteHidesTheCaptions(t *testing.T) {
	w := newTestWindow(t)
	w.onTargetText(" Good morning everyone.")
	w.pushLines()
	if len(w.view.lines) == 0 {
		t.Fatal("setup: nothing on screen")
	}
	w.ToggleMute()
	if len(w.view.lines) != 0 {
		t.Error("captions stayed on screen when muted")
	}
	w.onTargetText(" Said while muted.")
	w.pushLines()
	if len(w.view.lines) != 0 {
		t.Error("a caption reached the screen while muted")
	}
	if len(w.buffer.DisplayLines()) == 0 {
		t.Error("the buffer stopped taking captions, so the transcript would have a hole")
	}
	w.ToggleMute()
	if len(w.view.lines) != 0 {
		t.Error("what was said while muted arrived all at once on unmuting")
	}
	w.onTargetText(" And now we continue.")
	w.pushLines()
	got := ""
	for _, l := range w.view.lines {
		got += l.Text
	}
	if !strings.Contains(got, "continue") || strings.Contains(got, "muted") {
		t.Errorf("after unmuting the screen shows %q", got)
	}
}

// The source line is captions too.
func TestMuteHidesTheSourceText(t *testing.T) {
	w := newTestWindow(t)
	w.ToggleMute()
	cb := w.translatorCallbacks()
	cb.OnSource("안녕하세요")
	if w.view.source != "" {
		t.Errorf("source line %q while muted", w.view.source)
	}
}

// Turning speech off does not lift the mute — it mutes the captions as well —
// and turning speech back on while muted stays silent.
func TestTurningSpeechOffKeepsTheMute(t *testing.T) {
	w, factory := speakingWindow(t)
	w.startPlayer()
	w.ToggleMute()

	off := w.settings
	off.SpeakTranslation = false
	w.applySettings(off)
	if !w.muted || !w.view.Muted() {
		t.Fatal("turning speech off lifted the translation mute")
	}

	on := w.settings
	on.SpeakTranslation = true
	w.applySettings(on)
	if got := lastVolume(t, factory.last()); got != 0 {
		t.Errorf("speech turned back on while muted plays at %v", got)
	}
}
