package translator

import (
	"testing"
	"time"
)

// noteSpeech is the whole attribution mechanism, and it is driven by a clock,
// so the tests set the clock rather than sleeping.
func gapAfter(t *testing.T, tr *Translator, since time.Duration) {
	t.Helper()
	tr.mu.Lock()
	tr.lastAudioAt = time.Now().Add(-since)
	tr.mu.Unlock()
	tr.noteSpeech()
}

func TestTheFirstAudioOfARunHasNoGapInFrontOfIt(t *testing.T) {
	tr := &Translator{}
	tr.noteSpeech()

	if tr.stats.SpeechGaps != 0 {
		t.Errorf("SpeechGaps = %d before any audio had ever arrived, want 0", tr.stats.SpeechGaps)
	}
	if tr.stats.MaxSpeechGapMS != 0 {
		t.Errorf("MaxSpeechGapMS = %v, want 0 — the wait before the first word is not a gap",
			tr.stats.MaxSpeechGapMS)
	}
}

// Blobs arrive about ten times a second. Recording that cadence would bury the
// gaps that matter.
func TestOrdinaryCadenceIsNotAGap(t *testing.T) {
	tr := &Translator{}
	tr.noteSpeech()
	gapAfter(t, tr, 400*time.Millisecond)

	if tr.stats.SpeechGaps != 0 {
		t.Errorf("SpeechGaps = %d after a 0.4 s gap, want 0 (the measured in-session maximum is 0.38 s)",
			tr.stats.SpeechGaps)
	}
}

func TestALongSilenceIsRecorded(t *testing.T) {
	tr := &Translator{}
	tr.noteSpeech()
	gapAfter(t, tr, 4*time.Second)

	if tr.stats.SpeechGaps != 1 {
		t.Fatalf("SpeechGaps = %d after a 4 s silence, want 1", tr.stats.SpeechGaps)
	}
	if tr.stats.MaxSpeechGapMS < 3900 || tr.stats.MaxSpeechGapMS > 4200 {
		t.Errorf("MaxSpeechGapMS = %v, want about 4000", tr.stats.MaxSpeechGapMS)
	}
	if tr.stats.SwapGaps != 0 {
		t.Errorf("SwapGaps = %d without a reconnect, want 0", tr.stats.SwapGaps)
	}
}

// The finding the user actually needs: this silence belongs to the swap, not to
// their network.
func TestASilenceAcrossAReconnectIsAttributedToIt(t *testing.T) {
	tr := &Translator{}
	tr.noteSpeech()

	// A reconnect happens while nothing is arriving.
	tr.mu.Lock()
	tr.stats.Reconnects = 1
	tr.mu.Unlock()

	gapAfter(t, tr, 4*time.Second)

	if tr.stats.SwapGaps != 1 {
		t.Errorf("SwapGaps = %d, want 1", tr.stats.SwapGaps)
	}
	if tr.stats.SwapSilenceMS < 3900 {
		t.Errorf("SwapSilenceMS = %v, want about 4000", tr.stats.SwapSilenceMS)
	}
	if tr.stats.SpeechGaps != 1 {
		t.Errorf("SpeechGaps = %d, want 1 — a swap gap is still a gap", tr.stats.SpeechGaps)
	}
}

// Once attributed, the next gap in the same session must not be blamed on the
// same reconnect all over again.
func TestOnlyTheFirstGapAfterAReconnectIsChargedToIt(t *testing.T) {
	tr := &Translator{}
	tr.noteSpeech()
	tr.mu.Lock()
	tr.stats.Reconnects = 1
	tr.mu.Unlock()

	gapAfter(t, tr, 4*time.Second)
	gapAfter(t, tr, 4*time.Second)

	if tr.stats.SwapGaps != 1 {
		t.Errorf("SwapGaps = %d, want 1 — the second gap had no swap in front of it", tr.stats.SwapGaps)
	}
	if tr.stats.SpeechGaps != 2 {
		t.Errorf("SpeechGaps = %d, want 2", tr.stats.SpeechGaps)
	}
}

// Messages carrying only a transcription or a handle must not count as speech,
// or the gap would appear to close without a sound being made.
func TestOnlyAudioCountsAsSpeech(t *testing.T) {
	tr := &Translator{}
	tr.noteSpeech()
	before := tr.lastAudioAt

	// This is what the receive loop does for a message with no audio: nothing.
	if before.IsZero() {
		t.Fatal("the first noteSpeech did not start the clock")
	}
	time.Sleep(time.Millisecond)
	if !tr.lastAudioAt.Equal(before) {
		t.Error("the clock moved without audio arriving")
	}
}

func TestSummaryReportsTheSilence(t *testing.T) {
	s := Stats{Reconnects: 2, SpeechGaps: 3, MaxSpeechGapMS: 4210, SwapSilenceMS: 8400, SwapGaps: 2}
	got := s.Summary()
	for _, want := range []string{"speech_gaps=3", "max_speech_gap=", "swap_silence="} {
		if !contains(got, want) {
			t.Errorf("Summary() = %q, missing %q", got, want)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
