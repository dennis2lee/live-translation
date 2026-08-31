package classify

import "testing"

// The tuning that reaches a Gate comes from a settings file, so it can be a
// tuning nobody chose. These cover the two ways that happens: an upgrade, and
// a slider moved while a session is running.

// A settings file written before 1.4.0 has no music_gate_release key at all,
// so ReleaseSeconds arrives here as zero. Zero is not a choice — the slider
// stops at 0.5 — and a zero release is precisely the 1.3.0 policy the release
// hold replaced. The upgrade path has to fill it in.
func TestAnOlderTuningGetsTheMeasuredDefaults(t *testing.T) {
	if got, want := withDefaults(GateTuning{}), DefaultTuning(); got != want {
		t.Fatalf("withDefaults(zero) = %+v, want the measured %+v", got, want)
	}
}

func TestWithDefaultsLeavesAChosenTuningAlone(t *testing.T) {
	chosen := GateTuning{Threshold: 0.31, HoldSeconds: 4, ReleaseSeconds: 2.5}
	if got := withDefaults(chosen); got != chosen {
		t.Fatalf("withDefaults rewrote a chosen tuning: %+v, want %+v", got, chosen)
	}
}

// The upgrade path end to end. A 1.3.x install has a threshold and a hold and
// no release; if that reached the machine as written, one misread would reopen
// the gate and 1.4.0's fix would be undone for exactly the users who had the
// bug. Same assertion as TestOneClearFrameDoesNotReopenTheGate, reached the
// way an upgraded install reaches it.
func TestAnUpgradedGateStillHoldsThroughAMisread(t *testing.T) {
	var m gateMachine
	m.retune(withDefaults(GateTuning{Threshold: 0.5, HoldSeconds: 2.0}))

	for i := 0; i < m.holdFrames; i++ {
		m.onFrame(true)
	}
	if !m.closed {
		t.Fatal("setup: the gate should be closed after the hold")
	}
	m.onFrame(false) // the single sub-threshold frame inside a song
	if !m.closed {
		t.Fatal("an upgraded settings file reopened the gate on one clear frame")
	}
	if out := m.onChunk(chunkOf(1)); out != nil {
		t.Fatalf("music leaked after an upgrade: %d pieces", len(out))
	}
}

// "Tuning applies to a running gate without a restart" is a claim the
// Preferences sliders make. Nothing checked it.
func TestRetuningTakesEffectWithoutARestart(t *testing.T) {
	g := &Gate{
		tuning: DefaultTuning(),
		window: make([]float32, 0, PatchSamples),
		kick:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	g.machine.retune(g.tuning)
	before := g.machine.releaseFrames

	next := GateTuning{Threshold: 0.7, HoldSeconds: 5, ReleaseSeconds: 3}
	g.SetTuning(next)

	if got := g.Tuning(); got != next {
		t.Errorf("Tuning() = %+v after SetTuning(%+v)", got, next)
	}
	if g.machine.releaseFrames == before {
		t.Errorf("the machine still counts %d release frames — the slider did not reach it", before)
	}
	if want := framesFor(next.ReleaseSeconds); g.machine.releaseFrames != want {
		t.Errorf("releaseFrames = %d, want %d", g.machine.releaseFrames, want)
	}
	if want := framesFor(next.HoldSeconds); g.machine.holdFrames != want {
		t.Errorf("holdFrames = %d, want %d", g.machine.holdFrames, want)
	}
	if want := prerollFor(g.machine.releaseFrames); g.machine.prerollCap != want {
		t.Errorf("prerollCap = %d, want %d", g.machine.prerollCap, want)
	}
}

// SetTuning goes through withDefaults too, so a zero cannot reach the machine
// by way of a dialog that left a field unset.
func TestRetuningWithAZeroKeepsTheDefault(t *testing.T) {
	g := &Gate{tuning: DefaultTuning(), kick: make(chan struct{}, 1), done: make(chan struct{})}
	g.machine.retune(g.tuning)

	g.SetTuning(GateTuning{Threshold: 0.7})
	if got := g.Tuning().ReleaseSeconds; got != DefaultTuning().ReleaseSeconds {
		t.Errorf("ReleaseSeconds = %v after a partial retune, want the default", got)
	}
	if g.machine.releaseFrames <= 1 {
		t.Errorf("releaseFrames = %d — a partial retune restored the one-frame release",
			g.machine.releaseFrames)
	}
}

// retune's comment promises that shrinking the pre-roll does not discard what
// is already held: the trim happens on the next chunk, so a slider moved in
// the middle of a song cannot drop audio on the spot.
func TestShrinkingTheReleaseKeepsTheAudioAlreadyHeld(t *testing.T) {
	m := machineWith(1, 4)
	m.onFrame(true) // closes
	for i := 0; i < m.prerollCap; i++ {
		m.onChunk(chunkOf(byte(i)))
	}
	held := len(m.preroll)

	m.retune(GateTuning{Threshold: 0.5, HoldSeconds: 1, ReleaseSeconds: 0.5})
	if len(m.preroll) != held {
		t.Fatalf("retune discarded %d held chunks on the spot", held-len(m.preroll))
	}

	// The new cap applies from the next chunk, and the flush keeps the newest.
	m.onChunk(chunkOf(99))
	if len(m.preroll) > m.prerollCap {
		t.Errorf("pre-roll holds %d chunks, cap is %d", len(m.preroll), m.prerollCap)
	}
	for i := 0; i < m.releaseFrames; i++ {
		m.onFrame(false)
	}
	out := m.onChunk(chunkOf(100))
	if len(out) == 0 || out[len(out)-1][0] != 100 {
		t.Error("the reopening chunk did not come last")
	}
	if out[len(out)-2][0] != 99 {
		t.Error("the newest held chunk was not the one kept")
	}
}

// A hold is a promise of at least that much evidence, so seconds round up to
// whole frames, and no setting can ever mean "act on nothing".
func TestFramesForRoundsUpAndNeverReachesZero(t *testing.T) {
	const frameMS = hopSamples * 1000 / SampleRate // 480
	for _, tc := range []struct {
		seconds float64
		want    int
	}{
		{0.5, 2},   // 1.04 frames
		{0.96, 2},  // exactly 2
		{1.5, 4},   // 3.125 frames
		{2.0, 5},   // 4.17 frames
		{5.0, 11},  // 10.4 frames
		{0.001, 1}, // below one frame
		{0, 1},     // and a zero that slipped past withDefaults
		{-3, 1},
	} {
		if got := framesFor(tc.seconds); got != tc.want {
			t.Errorf("framesFor(%v) = %d, want %d (frame is %d ms)",
				tc.seconds, got, tc.want, frameMS)
		}
	}
}
