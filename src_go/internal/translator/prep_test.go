package translator

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
)

// prepRun drives a translator through one go_away with adaptive session prep
// configured, and reports when the replacement was opened.
type prepRun struct {
	tr      *Translator
	first   *fakeSession
	opener  *recordingOpener
	quiet   atomic.Bool
	asked   atomic.Int64
	collect *collector
}

func startPrep(t *testing.T, enabled bool, withPredicate bool) *prepRun {
	t.Helper()
	fastTiming(t)
	r := &prepRun{collect: &collector{}, first: newFakeSession(nil)}
	second := newFakeSession(nil)
	r.opener = &recordingOpener{build: func(attempt int) (liveSession, error) {
		if attempt == 0 {
			return r.first, nil
		}
		return second, nil
	}}
	opts := Options{
		Ring:                audio.NewRing(audio.ChunkBytes * 4),
		open:                r.opener.open,
		Callbacks:           r.collect.callbacks(),
		AdaptiveSessionPrep: enabled,
	}
	if withPredicate {
		opts.QuietMoment = func() bool {
			r.asked.Add(1)
			return r.quiet.Load()
		}
	}
	tr, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	r.tr = tr
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.Stop)
	waitFor(t, "the first session to run", func() bool { return tr.State() == StateRunning })
	return r
}

// The point of the feature: a swap held back until the gate closes.
func TestTheSwapWaitsForAQuietMoment(t *testing.T) {
	r := startPrep(t, true, true)
	PrepReserve = 0 // the whole grace is available to wait in
	r.first.push(Message{GoAway: true, GoAwayLeft: 2 * time.Second})

	// Nothing may open while the gate is open. The predicate being asked is
	// what proves the supervisor is waiting rather than merely slow.
	waitFor(t, "the supervisor to start polling", func() bool { return r.asked.Load() > 2 })
	if got := r.opener.attempts(); got != 1 {
		t.Fatalf("the replacement opened during speech: %d attempts, want 1", got)
	}

	r.quiet.Store(true)
	waitFor(t, "the replacement to open once it went quiet", func() bool {
		return r.opener.attempts() >= 2
	})

	stats := r.tr.Stats()
	if stats.PreppedSwaps != 1 {
		t.Errorf("PreppedSwaps = %d, want 1", stats.PreppedSwaps)
	}
	if stats.UnpreppedSwaps != 0 {
		t.Errorf("UnpreppedSwaps = %d, want 0 — it did not give up", stats.UnpreppedSwaps)
	}
}

// It can only choose *when*, never whether. A quiet moment that never comes
// must not cost the session.
func TestAQuietMomentThatNeverComesStillSwaps(t *testing.T) {
	r := startPrep(t, true, true)
	r.first.push(Message{GoAway: true, GoAwayLeft: 50 * time.Millisecond})

	waitFor(t, "the swap to happen anyway", func() bool { return r.opener.attempts() >= 2 })
	if got := r.tr.Stats().UnpreppedSwaps; got != 1 {
		t.Errorf("UnpreppedSwaps = %d, want 1", got)
	}
	if got := r.tr.Stats().PreppedSwaps; got != 0 {
		t.Errorf("PreppedSwaps = %d, want 0 — no window ever opened", got)
	}
}

// Already quiet when the warning lands: take it, without burning a poll.
func TestAnAlreadyQuietMomentIsTakenAtOnce(t *testing.T) {
	r := startPrep(t, true, true)
	r.quiet.Store(true)
	r.first.push(Message{GoAway: true, GoAwayLeft: 50 * time.Second})

	waitFor(t, "the replacement", func() bool { return r.opener.attempts() >= 2 })
	if got := r.tr.Stats().PreppedSwaps; got != 1 {
		t.Errorf("PreppedSwaps = %d, want 1", got)
	}
	waits := r.tr.Stats().PrepWaitsMS
	if len(waits) != 1 || waits[0] != 0 {
		t.Errorf("PrepWaitsMS = %v, want a single zero — it did not wait", waits)
	}
}

// With the music gate off there is no window, so the feature must be inert
// rather than waiting for something that can never happen. This is what makes
// it safe to ship switched on.
func TestWithNoQuietWindowTheSwapIsImmediate(t *testing.T) {
	r := startPrep(t, true, false)
	r.first.push(Message{GoAway: true, GoAwayLeft: 50 * time.Second})

	waitFor(t, "an immediate swap", func() bool { return r.opener.attempts() >= 2 })
	s := r.tr.Stats()
	if s.PreppedSwaps != 0 || s.UnpreppedSwaps != 0 {
		t.Errorf("prep counted a swap it had no part in: %+v", s)
	}
}

// Switched off, the predicate must not even be consulted.
func TestSwitchedOffTheGateIsNeverAsked(t *testing.T) {
	r := startPrep(t, false, true)
	r.first.push(Message{GoAway: true, GoAwayLeft: 50 * time.Second})

	waitFor(t, "an immediate swap", func() bool { return r.opener.attempts() >= 2 })
	if got := r.asked.Load(); got != 0 {
		t.Errorf("the quiet predicate was asked %d times with the feature off", got)
	}
}

// A server that warns without saying how long is not a reason to gamble: the
// reserve cannot be computed, so the swap happens as it always did.
func TestAWarningWithNoTimeLeftDoesNotWait(t *testing.T) {
	r := startPrep(t, true, true)
	r.first.push(Message{GoAway: true}) // no GoAwayLeft

	waitFor(t, "an immediate swap", func() bool { return r.opener.attempts() >= 2 })
	if got := r.tr.Stats().PreppedSwaps; got != 0 {
		t.Errorf("PreppedSwaps = %d, want 0", got)
	}
}

// A session that dies while the supervisor is holding out for quiet has to be
// noticed at once — waiting on a dead session is the 2.8 s hole coming back.
func TestADeadSessionEndsTheWaitImmediately(t *testing.T) {
	r := startPrep(t, true, true)
	PrepReserve = 0
	r.first.push(Message{GoAway: true, GoAwayLeft: 10 * time.Second})
	waitFor(t, "the supervisor to start polling", func() bool { return r.asked.Load() > 2 })

	_ = r.first.Close() // the far end hangs up

	waitFor(t, "the replacement to open without waiting out the budget",
		func() bool { return r.opener.attempts() >= 2 })
}

// graceLeft counts down from when the warning arrived, not from when it is
// asked — otherwise a supervisor that took a moment to get there would think
// it had the full 50 s.
func TestTheGraceCountsDownFromTheWarning(t *testing.T) {
	h := newSessionHolder()
	if got := h.graceLeft(); got != 0 {
		t.Errorf("graceLeft with no warning = %v, want 0", got)
	}
	h.markGoAway(50 * time.Second)
	time.Sleep(20 * time.Millisecond)
	got := h.graceLeft()
	if got >= 50*time.Second {
		t.Errorf("graceLeft = %v, want it to have counted down", got)
	}
	if got < 49*time.Second {
		t.Errorf("graceLeft = %v, want it near 50 s", got)
	}
	// A server that says nothing about the remaining time reports no grace,
	// which is what stops the wait from running on a guess.
	h2 := newSessionHolder()
	h2.markGoAway(0)
	if got := h2.graceLeft(); got != 0 {
		t.Errorf("graceLeft with no stated time = %v, want 0", got)
	}
}

// markGoAway is once-only; a second warning must not restart the clock and
// hand the supervisor grace the server never gave.
func TestASecondWarningDoesNotExtendTheGrace(t *testing.T) {
	h := newSessionHolder()
	h.markGoAway(time.Second)
	time.Sleep(10 * time.Millisecond)
	before := h.graceLeft()
	h.markGoAway(50 * time.Second)
	if after := h.graceLeft(); after > before {
		t.Errorf("grace grew from %v to %v on a second warning", before, after)
	}
}

// The summary line is where a session's measurements land, and it is the only
// place a user can answer "did adaptive session prep do anything" after the
// fact — a log full of individual decisions is not an answer.
func TestTheSummaryReportsWhatPrepAchieved(t *testing.T) {
	s := Stats{PreppedSwaps: 2, UnpreppedSwaps: 1}
	got := s.Summary()
	if !strings.Contains(got, "prepped_swaps=2/3") {
		t.Errorf("summary = %q, want it to carry prepped_swaps=2/3", got)
	}
	// A run where the feature was off or never fired still has to say so
	// rather than leave the reader guessing.
	if got := (Stats{}).Summary(); !strings.Contains(got, "prepped_swaps=0/0") {
		t.Errorf("an untouched run's summary = %q, want prepped_swaps=0/0", got)
	}
}
