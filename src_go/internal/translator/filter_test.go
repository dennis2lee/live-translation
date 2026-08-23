package translator

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
)

// filterRecorder is a controllable Options.Filter.
type filterRecorder struct {
	mu   sync.Mutex
	fed  [][]byte
	mode string // "pass", "drop", "flush"
	held [][]byte
}

func (f *filterRecorder) filter(chunk []byte) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fed = append(f.fed, append([]byte(nil), chunk...))
	switch f.mode {
	case "drop":
		f.held = append(f.held, append([]byte(nil), chunk...))
		return nil
	case "flush":
		out := append(f.held, chunk)
		f.held = nil
		f.mode = "pass"
		return out
	default:
		return [][]byte{chunk}
	}
}

func (f *filterRecorder) setMode(m string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mode = m
}

func (f *filterRecorder) fedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fed)
}

func filteredTranslator(t *testing.T, f *filterRecorder) (*Translator, *fakeSession, *audio.Ring) {
	t.Helper()
	fastTiming(t)
	session := newFakeSession(nil)
	opener := &recordingOpener{build: func(int) (liveSession, error) { return session, nil }}
	ring := audio.NewRing(audio.ChunkBytes * 64)
	tr, err := New(Options{Ring: ring, open: opener.open, Filter: f.filter})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.Stop)
	waitFor(t, "the session", func() bool { return tr.State() == StateRunning })
	return tr, session, ring
}

func TestFilterSeesEveryChunk(t *testing.T) {
	f := &filterRecorder{mode: "drop"}
	_, session, ring := filteredTranslator(t, f)

	ring.Write(bytes.Repeat([]byte{1}, audio.ChunkBytes*3))
	waitFor(t, "the filter to be fed", func() bool { return f.fedCount() >= 3 })

	// Dropped means dropped: nothing reached the session.
	time.Sleep(10 * time.Millisecond)
	if got := session.chunksSent(); got != 0 {
		t.Errorf("%d chunks were sent while the filter dropped everything", got)
	}
}

func TestFilterPassThroughSends(t *testing.T) {
	f := &filterRecorder{mode: "pass"}
	_, session, ring := filteredTranslator(t, f)

	ring.Write(bytes.Repeat([]byte{1}, audio.ChunkBytes*3))
	waitFor(t, "the chunks to be sent", func() bool { return session.chunksSent() >= 3 })
}

// A flush delivers the withheld backlog before the current chunk — the gate's
// reopening must not reorder audio.
func TestFilterFlushSendsTheBacklogInOrder(t *testing.T) {
	f := &filterRecorder{mode: "drop"}
	tr, session, ring := filteredTranslator(t, f)

	chunk := func(b byte) []byte { return bytes.Repeat([]byte{b}, audio.ChunkBytes) }
	ring.Write(chunk(1))
	ring.Write(chunk(2))
	waitFor(t, "the drops", func() bool { return f.fedCount() >= 2 })

	f.setMode("flush")
	ring.Write(chunk(3))
	waitFor(t, "the flush", func() bool { return session.chunksSent() >= 3 })

	sent := session.sentChunks()
	for i, want := range []byte{1, 2, 3} {
		if sent[i][0] != want {
			t.Fatalf("chunk %d starts with %d, want %d — the flush reordered audio", i, sent[i][0], want)
		}
	}
	if got := tr.Stats().ChunksSent; got != 3 {
		t.Errorf("Stats.ChunksSent = %d, want 3", got)
	}
}

func TestNoFilterMeansTodaysBehaviour(t *testing.T) {
	fastTiming(t)
	session := newFakeSession(nil)
	opener := &recordingOpener{build: func(int) (liveSession, error) { return session, nil }}
	ring := audio.NewRing(audio.ChunkBytes * 8)
	tr, err := New(Options{Ring: ring, open: opener.open})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.Stop)
	waitFor(t, "the session", func() bool { return tr.State() == StateRunning })

	ring.Write(bytes.Repeat([]byte{1}, audio.ChunkBytes*2))
	waitFor(t, "the chunks", func() bool { return session.chunksSent() >= 2 })
}

// sentChunks copies what the fake session received, for order assertions.
func (f *fakeSession) sentChunks() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.sent))
	for i, c := range f.sent {
		out[i] = append([]byte(nil), c...)
	}
	return out
}
