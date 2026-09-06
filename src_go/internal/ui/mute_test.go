package ui

import (
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

	w.ToggleSpeechMute()

	if !w.speechMuted {
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

	w.ToggleSpeechMute()

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

	w.ToggleSpeechMute()
	w.ToggleSpeechMute()

	if w.speechMuted {
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
	w.ToggleSpeechMute()

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
	w.ToggleSpeechMute()

	next := w.settings
	next.SpeechVolume = 0.3
	w.applySettings(next)

	if got := lastVolume(t, player); got != 0 {
		t.Errorf("gain = %v after moving the slider while muted, want 0", got)
	}

	w.ToggleSpeechMute()
	if got := lastVolume(t, player); got != 0.3 {
		t.Errorf("gain after unmute = %v, want the new volume 0.3", got)
	}
}

// Preferences is the durable switch. Turning speech off there and on again must
// speak, or the transient override outlives the thing it was overriding.
func TestTurningSpeechOffClearsTheMute(t *testing.T) {
	w, factory := speakingWindow(t)
	w.startPlayer()
	w.ToggleSpeechMute()

	off := w.settings
	off.SpeakTranslation = false
	w.applySettings(off)

	if w.speechMuted {
		t.Fatal("mute survived turning speech off in Preferences")
	}

	on := w.settings
	on.SpeakTranslation = true
	w.applySettings(on)

	if got := lastVolume(t, factory.last()); got == 0 {
		t.Error("speech turned back on is still silent")
	}
}

// With the feature off there is nothing to mute, and a key that does nothing
// silently reads as a key that is broken.
func TestMuteWithSpeechOffSaysSo(t *testing.T) {
	w := newTestWindow(t)
	factory := &speakerFactory{}
	w.newSpeaker = factory.build

	w.ToggleSpeechMute()

	if w.speechMuted {
		t.Error("m muted a feature that is switched off")
	}
	if w.notice == "" {
		t.Error("m with speech off said nothing")
	}
}

// Muting before anything is playing must not panic, and must still be in force
// when a player does appear.
func TestMuteWithNoPlayerRunning(t *testing.T) {
	w, factory := speakingWindow(t)

	w.ToggleSpeechMute()
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

	if w.view.SpeechMuted() {
		t.Fatal("setup: the dot started muted")
	}
	w.ToggleSpeechMute()
	if !w.view.SpeechMuted() {
		t.Error("m muted the sound but not the dot")
	}
	w.ToggleSpeechMute()
	if w.view.SpeechMuted() {
		t.Error("m unmuted the sound but left the dot red")
	}
}

// Turning the feature off in Preferences clears the override on the gain. The
// dot has to follow, or it would claim a mute that no longer exists — and no
// key would clear it, because m refuses to act when speech is off.
func TestTurningSpeechOffClearsTheMuteHalf(t *testing.T) {
	w, _ := speakingWindow(t)
	w.startPlayer()
	w.ToggleSpeechMute()
	if !w.view.SpeechMuted() {
		t.Fatal("setup: the dot is not showing the mute")
	}

	old := w.settings
	w.settings.SpeakTranslation = false
	w.applySpeechChanges(old)

	if w.speechMuted {
		t.Error("the gain override survived speech being turned off")
	}
	if w.view.SpeechMuted() {
		t.Error("the dot still claims a mute that no key can now clear")
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

	v.speechMuted = true
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
	v.speechMuted = true
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
