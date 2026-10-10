package translator

import (
	"sync"
	"testing"
	"time"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
)

// events records the order of session starts and audio, as the player sees it.
type events struct {
	mu  sync.Mutex
	seq []string
}

func (e *events) add(s string) { e.mu.Lock(); e.seq = append(e.seq, s); e.mu.Unlock() }
func (e *events) get() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.seq...)
}

// The player removes a session's opening buzz whole and only trims one
// elsewhere, so it has to know where each session's audio begins — exactly,
// and before the audio, not after it.
func TestEachSessionAnnouncesItsFirstAudio(t *testing.T) {
	fastTiming(t)
	ev := &events{}
	first, second := newFakeSession(nil), newFakeSession(nil)
	opener := &recordingOpener{build: func(attempt int) (liveSession, error) {
		if attempt == 0 {
			return first, nil
		}
		return second, nil
	}}
	tr, err := New(Options{Ring: audio.NewRing(audio.ChunkBytes * 4), open: opener.open,
		Callbacks: Callbacks{
			OnSessionStart: func() { ev.add("start") },
			OnAudio: func(pcm []byte) {
				if len(pcm) > 0 {
					ev.add("audio")
				}
			},
		}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.Stop)
	waitFor(t, "the first session", func() bool { return tr.State() == StateRunning })

	blob := Message{Audio: make([]byte, 480)}
	first.push(blob)
	first.push(blob)
	waitFor(t, "two blobs", func() bool { return len(ev.get()) >= 3 })
	first.push(Message{GoAway: true, GoAwayLeft: 50 * time.Second})
	waitFor(t, "the replacement", func() bool { return opener.attempts() >= 2 })
	second.push(blob)
	second.push(blob)
	waitFor(t, "the replacement's audio", func() bool { return len(ev.get()) >= 6 })

	want := []string{"start", "audio", "audio", "start", "audio", "audio"}
	got := ev.get()
	if len(got) != len(want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events %v, want %v", got, want)
		}
	}
}

// Messages without audio — transcription, handles — do not open anything.
func TestOnlyAudioOpensASession(t *testing.T) {
	fastTiming(t)
	starts := 0
	var mu sync.Mutex
	s := newFakeSession(nil)
	opener := &recordingOpener{build: func(int) (liveSession, error) { return s, nil }}
	tr, err := New(Options{Ring: audio.NewRing(audio.ChunkBytes * 4), open: opener.open,
		Callbacks: Callbacks{OnSessionStart: func() { mu.Lock(); starts++; mu.Unlock() }}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.Stop)
	waitFor(t, "running", func() bool { return tr.State() == StateRunning })
	s.push(Message{TargetText: "hello"})
	s.push(Message{ResumeHandle: "h"})
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if starts != 0 {
		t.Errorf("OnSessionStart fired %d times without any audio", starts)
	}
}
