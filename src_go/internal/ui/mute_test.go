package ui

import "testing"

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
