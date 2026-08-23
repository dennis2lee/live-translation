package translator

import (
	"errors"
	"testing"
	"time"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
)

// swapped drives a translator through one go_away reconnect and hands back both
// sessions. Every test below needs the same three steps.
func swapped(t *testing.T, second *fakeSession) (*Translator, *fakeSession, *collector) {
	t.Helper()
	fastTiming(t)
	c := &collector{}
	first := newFakeSession(nil)

	opener := &recordingOpener{build: func(attempt int) (liveSession, error) {
		if attempt == 0 {
			return first, nil
		}
		return second, nil
	}}
	tr, err := New(Options{Ring: audio.NewRing(audio.ChunkBytes * 4), open: opener.open, Callbacks: c.callbacks()})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.Stop)

	waitFor(t, "the first session to run", func() bool { return tr.State() == StateRunning })
	first.push(Message{GoAway: true, GoAwayLeft: 50 * time.Second})
	waitFor(t, "the replacement session", func() bool { return opener.attempts() >= 2 })
	waitFor(t, "the reconnect callback", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.reconnects == 1
	})
	return tr, first, c
}

func isClosed(f *fakeSession) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// The measured reason this exists: closing at the swap left 2.8–4.2 s of
// silence, because the replacement needs 3.3–5.2 s before it says anything.
func TestTheReplacedSessionOutlivesTheSwap(t *testing.T) {
	tr, first, _ := swapped(t, newFakeSession(nil))

	if isClosed(first) {
		t.Fatal("the replaced session was closed at the swap — its tail is what covers the gap")
	}
	tr.mu.Lock()
	retiring := tr.retiring
	tr.mu.Unlock()
	if retiring == nil {
		t.Error("nothing is being held back")
	}
}

// It has covered the hole the moment the replacement speaks, and holding a
// second connection open past that point is waste.
func TestTheReplacedSessionClosesWhenTheReplacementSpeaks(t *testing.T) {
	second := newFakeSession(nil)
	tr, first, _ := swapped(t, second)

	second.push(Message{Audio: make([]byte, 480)})

	waitFor(t, "the replaced session to close", func() bool { return isClosed(first) })
	waitFor(t, "the hold to be released", func() bool {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		return tr.retiring == nil
	})
}

// Text alone is not cover. Only audio can fill a silence, so only audio ends the
// overlap — a replacement that has produced a caption and no sound has not yet
// taken over from the session it replaced.
func TestTextFromTheReplacementDoesNotEndTheOverlap(t *testing.T) {
	second := newFakeSession(nil)
	_, first, _ := swapped(t, second)

	second.push(Message{TargetText: "hello"})
	time.Sleep(20 * time.Millisecond)

	if isClosed(first) {
		t.Error("a caption with no audio closed the session that was covering the gap")
	}
}

// If nobody is speaking the replacement may never produce audio, and the hold
// must not last for ever.
func TestTheReplacedSessionClosesAfterTheGracePeriod(t *testing.T) {
	_, first, _ := swapped(t, newFakeSession(nil))

	waitFor(t, "the grace period to close the replaced session", func() bool { return isClosed(first) })
}

// A retired session goes on receiving, resumption updates included. Its handle
// points at a session on its way out, and the next reconnect must not be given
// it.
func TestARetiredSessionCannotWriteTheResumptionHandle(t *testing.T) {
	second := newFakeSession(nil)
	tr, first, _ := swapped(t, second)

	second.push(Message{ResumeHandle: "from-the-live-session"})
	waitFor(t, "the live handle", func() bool {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		return tr.handle == "from-the-live-session"
	})

	first.push(Message{ResumeHandle: "from-the-retired-session"})
	time.Sleep(20 * time.Millisecond)

	tr.mu.Lock()
	got := tr.handle
	tr.mu.Unlock()
	if got != "from-the-live-session" {
		t.Errorf("handle = %q — a replaced session overwrote it", got)
	}
}

// Stop must not leave a held-back session open.
func TestStoppingClosesTheHeldBackSession(t *testing.T) {
	tr, first, _ := swapped(t, newFakeSession(nil))

	if isClosed(first) {
		t.Fatal("closed before Stop, so this proves nothing")
	}
	tr.Stop()

	waitFor(t, "the held-back session to close", func() bool { return isClosed(first) })
}

// A stop and start must not inherit the previous run's held-back session.
func TestRestartingForgetsTheHeldBackSession(t *testing.T) {
	tr, _, _ := swapped(t, newFakeSession(nil))
	tr.Stop()

	tr.mu.Lock()
	retiring := tr.retiring
	tr.mu.Unlock()
	if retiring != nil {
		t.Error("a held-back session survived Stop")
	}
}

// The supervisor can also return without the context being cancelled — a fatal
// error gives up rather than retrying. That path is why the defer closes the
// held-back session explicitly instead of leaving it to the watchdog: waiting
// out RetireGrace would hold a connection open for twelve seconds after the
// translator had already stopped and shown an error.
func TestAFatalErrorClosesTheHeldBackSessionAtOnce(t *testing.T) {
	fastTiming(t)
	// Long enough that the watchdog cannot be the thing that closes it.
	RetireGrace = 30 * time.Second

	c := &collector{}
	first := newFakeSession(nil)
	second := newFakeSession(nil)

	opener := &recordingOpener{build: func(attempt int) (liveSession, error) {
		switch attempt {
		case 0:
			return first, nil
		case 1:
			return second, nil
		default:
			return nil, errors.New("API key not valid. Please pass a valid API key.")
		}
	}}
	tr, err := New(Options{Ring: audio.NewRing(audio.ChunkBytes * 4), open: opener.open, Callbacks: c.callbacks()})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.Stop)

	waitFor(t, "the first session to run", func() bool { return tr.State() == StateRunning })
	first.push(Message{GoAway: true, GoAwayLeft: 50 * time.Second})
	waitFor(t, "the replacement", func() bool { return opener.attempts() >= 2 })
	if isClosed(first) {
		t.Fatal("the replaced session closed at the swap, so this proves nothing")
	}

	// Now push the replacement into a reconnect that fails fatally.
	second.push(Message{GoAway: true, GoAwayLeft: 50 * time.Second})
	waitFor(t, "the error state", func() bool { return tr.State() == StateError })

	waitFor(t, "the held-back session to close", func() bool { return isClosed(first) })
}

// The same defect the test above uncovered, stated directly: giving up must
// close the live session, not leave it open being fed audio it is charged for.
func TestGivingUpClosesTheLiveSession(t *testing.T) {
	fastTiming(t)
	MaxConsecutiveFailures = 1
	t.Cleanup(func() { MaxConsecutiveFailures = 5 })

	live := newFakeSession(nil)
	opener := &recordingOpener{build: func(attempt int) (liveSession, error) {
		if attempt == 0 {
			return live, nil
		}
		return nil, errors.New("dial tcp: connection refused")
	}}
	tr, err := New(Options{Ring: audio.NewRing(audio.ChunkBytes * 4), open: opener.open})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.Stop)

	waitFor(t, "the session to run", func() bool { return tr.State() == StateRunning })
	live.push(Message{GoAway: true, GoAwayLeft: 50 * time.Second})
	waitFor(t, "the error state", func() bool { return tr.State() == StateError })

	waitFor(t, "the live session to close", func() bool { return isClosed(live) })
}
