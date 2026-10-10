package translator

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
)

// --- a fake session, so none of this touches the network ------------------

type fakeSession struct {
	mu       sync.Mutex
	messages []Message
	sent     [][]byte
	closed   bool
	sendErr  error
	// endErr is returned by Receive once the script runs out. Left nil, Receive
	// blocks until Close, which is what a healthy session does.
	endErr error

	next    chan Message
	done    chan struct{}
	closeCh sync.Once
	// closeGate, when set, holds Close until it is closed — a session that
	// takes a moment to shut, receiving all the while.
	closeGate chan struct{}
}

func newFakeSession(messages []Message) *fakeSession {
	f := &fakeSession{
		next: make(chan Message, len(messages)+1),
		done: make(chan struct{}),
	}
	for _, m := range messages {
		f.next <- m
	}
	return f
}

func (f *fakeSession) SendAudio(chunk []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, append([]byte(nil), chunk...))
	return nil
}

func (f *fakeSession) Receive() (Message, error) {
	select {
	case m := <-f.next:
		return m, nil
	case <-f.done:
		return Message{}, errors.New("session closed")
	}
}

func (f *fakeSession) Close() error {
	if f.closeGate != nil {
		<-f.closeGate
	}
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	f.closeCh.Do(func() { close(f.done) })
	return nil
}

func (f *fakeSession) push(m Message) { f.next <- m }

func (f *fakeSession) chunksSent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// opener records the handles it was offered, so resumption can be checked.
type recordingOpener struct {
	mu       sync.Mutex
	handles  []string
	sessions []*fakeSession
	build    func(attempt int) (liveSession, error)
}

func (o *recordingOpener) open(_ context.Context, handle string) (liveSession, error) {
	o.mu.Lock()
	attempt := len(o.handles)
	o.handles = append(o.handles, handle)
	o.mu.Unlock()

	s, err := o.build(attempt)
	if err != nil {
		return nil, err
	}
	if fake, ok := s.(*fakeSession); ok {
		o.mu.Lock()
		o.sessions = append(o.sessions, fake)
		o.mu.Unlock()
	}
	return s, nil
}

func (o *recordingOpener) handleAt(i int) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if i >= len(o.handles) {
		return ""
	}
	return o.handles[i]
}

func (o *recordingOpener) attempts() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.handles)
}

func (o *recordingOpener) session(i int) *fakeSession {
	o.mu.Lock()
	defer o.mu.Unlock()
	if i >= len(o.sessions) {
		return nil
	}
	return o.sessions[i]
}

// collector gathers callbacks from the translator's goroutines.
type collector struct {
	mu            sync.Mutex
	target        []string
	source        []string
	states        []State
	details       []string
	audio         [][]byte
	reconnects    int
	turnCompletes int
}

func (c *collector) callbacks() Callbacks {
	return Callbacks{
		OnTarget: func(t string) { c.mu.Lock(); c.target = append(c.target, t); c.mu.Unlock() },
		OnSource: func(t string) { c.mu.Lock(); c.source = append(c.source, t); c.mu.Unlock() },
		OnState: func(s State, d string) {
			c.mu.Lock()
			c.states = append(c.states, s)
			if d != "" {
				c.details = append(c.details, d)
			}
			c.mu.Unlock()
		},
		OnReconnect:    func() { c.mu.Lock(); c.reconnects++; c.mu.Unlock() },
		OnTurnComplete: func() { c.mu.Lock(); c.turnCompletes++; c.mu.Unlock() },
		// Kept by reference on purpose: whether the bytes handed over stay
		// valid is exactly what TestReceivedAudioIsCopied checks.
		OnAudio: func(pcm []byte) { c.mu.Lock(); c.audio = append(c.audio, pcm); c.mu.Unlock() },
	}
}

func (c *collector) audioBlobs() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.audio...)
}

func (c *collector) targets() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.target...)
}

func (c *collector) sawState(s State) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, got := range c.states {
		if got == s {
			return true
		}
	}
	return false
}

func (c *collector) detailContains(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, d := range c.details {
		if strings.Contains(d, sub) {
			return true
		}
	}
	return false
}

// fastTiming shortens the real-world delays so a test is not a coffee break.
func fastTiming(t *testing.T) {
	t.Helper()
	backoff, minSession, open, poll, grace := BackoffSeconds, MinSessionSeconds, OpenTimeout, idlePoll, backlogGrace
	retire := RetireGrace
	reserve, prep := PrepReserve, prepPoll
	PrepReserve = 10 * time.Millisecond
	prepPoll = time.Millisecond
	BackoffSeconds = []time.Duration{5 * time.Millisecond}
	MinSessionSeconds = 20 * time.Millisecond
	OpenTimeout = 500 * time.Millisecond
	idlePoll = time.Millisecond
	backlogGrace = 10 * time.Millisecond
	RetireGrace = 50 * time.Millisecond
	t.Cleanup(func() {
		BackoffSeconds, MinSessionSeconds, OpenTimeout, idlePoll, backlogGrace = backoff, minSession, open, poll, grace
		RetireGrace = retire
		PrepReserve, prepPoll = reserve, prep
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// --- tests ----------------------------------------------------------------

func TestFatalReason(t *testing.T) {
	cases := []struct{ err, want string }{
		{"Your credits are depleted", "Billing credits exhausted"},
		{"API key not valid, please pass a valid key", "API key is not valid"},
		{"PERMISSION_DENIED: no access", "No permission to access this model"},
		{"models/nonsense was not found", "Model not found"},
		{"connection reset by peer", ""},
	}
	for _, c := range cases {
		got := FatalReason(errors.New(c.err))
		if c.want == "" && got != "" {
			t.Errorf("FatalReason(%q) = %q, want it retryable", c.err, got)
		}
		if c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("FatalReason(%q) = %q, want it to contain %q", c.err, got, c.want)
		}
	}
	if FatalReason(nil) != "" {
		t.Error("nil is not fatal")
	}
}

// The translation is what goes on screen, and it must reach the callback.
func TestFragmentsReachTheCallbacks(t *testing.T) {
	fastTiming(t)
	c := &collector{}
	opener := &recordingOpener{build: func(int) (liveSession, error) {
		return newFakeSession([]Message{
			{SourceText: "안녕하세요"},
			{TargetText: "Hello."},
			{TurnComplete: true},
			{Audio: make([]byte, 4096)},
		}), nil
	}}

	tr, err := New(Options{Ring: audio.NewRing(audio.ChunkBytes * 4), open: opener.open, Callbacks: c.callbacks()})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	defer tr.Stop()

	waitFor(t, "a translation fragment", func() bool { return len(c.targets()) > 0 })
	if got := c.targets()[0]; got != "Hello." {
		t.Errorf("target = %q", got)
	}
	waitFor(t, "turn_complete", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.turnCompletes == 1
	})
	if !c.sawState(StateRunning) {
		t.Error("never reached the running state")
	}
}

// The translated speech has to reach whoever is listening, and carry its bytes
// rather than a count. Until 1.1.0 it was counted and dropped.
func TestTranslatedAudioReachesTheCallback(t *testing.T) {
	fastTiming(t)
	c := &collector{}
	want := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	opener := &recordingOpener{build: func(int) (liveSession, error) {
		return newFakeSession([]Message{{Audio: want}}), nil
	}}

	tr, err := New(Options{Ring: audio.NewRing(audio.ChunkBytes * 4), open: opener.open, Callbacks: c.callbacks()})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	defer tr.Stop()

	waitFor(t, "a blob of translated audio", func() bool { return len(c.audioBlobs()) > 0 })
	if got := c.audioBlobs()[0]; !bytes.Equal(got, want) {
		t.Errorf("audio = %v, want %v", got, want)
	}
}

// A session that carries no audio must not call the callback at all, or a
// player would be woken ten times a second to do nothing.
func TestNoAudioMeansNoAudioCallback(t *testing.T) {
	fastTiming(t)
	c := &collector{}
	opener := &recordingOpener{build: func(int) (liveSession, error) {
		return newFakeSession([]Message{{TargetText: "Hello."}, {TurnComplete: true}}), nil
	}}

	tr, err := New(Options{Ring: audio.NewRing(audio.ChunkBytes * 4), open: opener.open, Callbacks: c.callbacks()})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	defer tr.Stop()

	waitFor(t, "turn_complete", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.turnCompletes == 1
	})
	if got := c.audioBlobs(); len(got) != 0 {
		t.Errorf("OnAudio fired %d times for messages carrying no audio", len(got))
	}
}

// Audio in the ring must reach the session.
func TestAudioIsSentFromTheRing(t *testing.T) {
	fastTiming(t)
	ring := audio.NewRing(audio.ChunkBytes * 8)
	opener := &recordingOpener{build: func(int) (liveSession, error) {
		return newFakeSession(nil), nil
	}}
	tr, err := New(Options{Ring: ring, open: opener.open})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	defer tr.Stop()

	waitFor(t, "the session to open", func() bool { return opener.session(0) != nil })
	ring.Write(make([]byte, audio.ChunkBytes*3))

	waitFor(t, "three chunks to be sent", func() bool { return opener.session(0).chunksSent() >= 3 })
	if got := tr.Stats().ChunksSent; got < 3 {
		t.Errorf("Stats.ChunksSent = %d", got)
	}
}

// go_away must open a replacement *before* the old session closes, and the
// resumption handle must be carried across.
func TestGoAwaySwapsTheSessionAndResumes(t *testing.T) {
	fastTiming(t)
	c := &collector{}
	first := newFakeSession([]Message{{ResumeHandle: "handle-1"}})
	second := newFakeSession(nil)

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
	defer tr.Stop()

	waitFor(t, "the first session to run", func() bool { return tr.State() == StateRunning })
	// Let the handle land before the shutdown warning.
	waitFor(t, "the resumption handle", func() bool {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		return tr.handle == "handle-1"
	})

	first.push(Message{GoAway: true, GoAwayLeft: 50 * time.Second})

	waitFor(t, "the replacement session", func() bool { return opener.attempts() >= 2 })
	waitFor(t, "the reconnect callback", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.reconnects == 1
	})

	if got := opener.handleAt(1); got != "handle-1" {
		t.Errorf("replacement was offered handle %q, want the newest one", got)
	}
	if got := tr.Stats().Reconnects; got != 1 {
		t.Errorf("Stats.Reconnects = %d", got)
	}
	if len(tr.Stats().GapsMS) != 1 {
		t.Errorf("no gap was recorded: %v", tr.Stats().GapsMS)
	}
	if !c.sawState(StateReconnecting) {
		t.Error("never reported reconnecting")
	}
	// The old session outlives the swap on purpose — it covers the replacement's
	// warm-up — and is closed once the replacement speaks, or after RetireGrace
	// if it never does. This replacement has nothing to say, so it is the grace
	// period that closes it here.
	waitFor(t, "the old session to close", func() bool {
		first.mu.Lock()
		defer first.mu.Unlock()
		return first.closed
	})
}

// A handle only refers to a session of this run; a stop/start must not offer a
// stale one.
func TestHandleIsForgottenAcrossRestart(t *testing.T) {
	fastTiming(t)
	opener := &recordingOpener{build: func(int) (liveSession, error) {
		return newFakeSession([]Message{{ResumeHandle: "stale"}}), nil
	}}
	tr, err := New(Options{Ring: audio.NewRing(audio.ChunkBytes * 4), open: opener.open})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the handle", func() bool {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		return tr.handle == "stale"
	})
	tr.Stop()

	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	defer tr.Stop()
	waitFor(t, "the second run to connect", func() bool { return opener.attempts() >= 2 })
	if got := opener.handleAt(opener.attempts() - 1); got != "" {
		t.Errorf("a stale handle %q was offered after a restart", got)
	}
}

// Credit, auth and permission problems do not heal with time: report at once
// rather than backing off.
func TestFatalErrorStopsImmediately(t *testing.T) {
	fastTiming(t)
	c := &collector{}
	opener := &recordingOpener{build: func(int) (liveSession, error) {
		return nil, errors.New("API key not valid, please pass a valid API key")
	}}
	tr, err := New(Options{Ring: audio.NewRing(audio.ChunkBytes), open: opener.open, Callbacks: c.callbacks()})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	defer tr.Stop()

	waitFor(t, "the error state", func() bool { return tr.State() == StateError })
	if !c.detailContains("API key is not valid") {
		t.Error("the cause was not reported — a red dot alone is not actionable")
	}
	// One attempt, not a ladder of retries.
	time.Sleep(30 * time.Millisecond)
	if got := opener.attempts(); got != 1 {
		t.Errorf("%d connect attempts for a fatal error, want 1", got)
	}
}

// A retryable failure backs off and gives up after the ladder runs out.
func TestRetryableFailuresGiveUpEventually(t *testing.T) {
	fastTiming(t)
	c := &collector{}
	opener := &recordingOpener{build: func(int) (liveSession, error) {
		return nil, errors.New("connection reset by peer")
	}}
	tr, err := New(Options{Ring: audio.NewRing(audio.ChunkBytes), open: opener.open, Callbacks: c.callbacks()})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	defer tr.Stop()

	waitFor(t, "the error state", func() bool { return tr.State() == StateError })
	if got := opener.attempts(); got != MaxConsecutiveFailures {
		t.Errorf("%d attempts, want %d", got, MaxConsecutiveFailures)
	}
	if !c.detailContains("Repeated connection failures") {
		t.Error("gave up without saying why")
	}
}

// A server that accepts and immediately drops every connection must not become
// a hot loop.
func TestSessionsThatDieImmediatelyCountAsFailures(t *testing.T) {
	fastTiming(t)
	c := &collector{}
	opener := &recordingOpener{build: func(int) (liveSession, error) {
		f := newFakeSession(nil)
		// Alive long enough to be "ready", then gone.
		go func() {
			time.Sleep(2 * time.Millisecond)
			f.Close()
		}()
		return f, nil
	}}
	tr, err := New(Options{Ring: audio.NewRing(audio.ChunkBytes), open: opener.open, Callbacks: c.callbacks()})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	defer tr.Stop()

	waitFor(t, "the error state", func() bool { return tr.State() == StateError })
	if !c.detailContains("Session keeps dropping") {
		t.Error("a session dying instantly was not reported as such")
	}
}

// A send failure must retire the session rather than being retried for ever on
// a dead connection.
func TestSendFailureRetiresTheSession(t *testing.T) {
	fastTiming(t)
	ring := audio.NewRing(audio.ChunkBytes * 8)
	broken := newFakeSession(nil)
	broken.sendErr = errors.New("websocket: close sent")
	healthy := newFakeSession(nil)

	opener := &recordingOpener{build: func(attempt int) (liveSession, error) {
		if attempt == 0 {
			return broken, nil
		}
		return healthy, nil
	}}
	tr, err := New(Options{Ring: ring, open: opener.open})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	defer tr.Stop()

	waitFor(t, "the first session", func() bool { return opener.attempts() >= 1 })
	ring.Write(make([]byte, audio.ChunkBytes))
	waitFor(t, "a replacement session", func() bool { return opener.attempts() >= 2 })
}

// The chunk a failed send was carrying must reach the replacement session.
//
// It had already left the ring, so dropping it lost 100 ms of speech every time
// a session ended uncleanly — while the log said the send would be retried.
func TestAFailedSendKeepsItsChunkForTheNextSession(t *testing.T) {
	fastTiming(t)
	ring := audio.NewRing(audio.ChunkBytes * 8)
	broken := newFakeSession(nil)
	broken.sendErr = errors.New("websocket: close sent")
	healthy := newFakeSession(nil)

	opener := &recordingOpener{build: func(attempt int) (liveSession, error) {
		if attempt == 0 {
			return broken, nil
		}
		return healthy, nil
	}}
	tr, err := New(Options{Ring: ring, open: opener.open})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	defer tr.Stop()

	waitFor(t, "the first session", func() bool { return opener.attempts() >= 1 })

	// Exactly one chunk, with a recognisable payload, and nothing behind it.
	chunk := make([]byte, audio.ChunkBytes)
	for i := range chunk {
		chunk[i] = 0x5A
	}
	ring.Write(chunk)

	waitFor(t, "the chunk to reach the replacement", func() bool {
		return healthy.chunksSent() >= 1
	})
	healthy.mu.Lock()
	got := append([]byte(nil), healthy.sent[0]...)
	healthy.mu.Unlock()
	if !bytes.Equal(got, chunk) {
		t.Error("the replacement session did not receive the chunk the failed send was holding")
	}
}

// A session that opens after the supervisor gave up waiting has no owner. It
// must close itself rather than run on, receiving and overwriting the live
// session's resumption handle with its own stale one.
func TestASessionThatOpensAfterTheTimeoutIsClosed(t *testing.T) {
	fastTiming(t)
	ring := audio.NewRing(audio.ChunkBytes * 4)

	late := newFakeSession([]Message{{ResumeHandle: "stale-handle-from-the-orphan"}})
	release := make(chan struct{})

	opener := &recordingOpener{build: func(attempt int) (liveSession, error) {
		if attempt == 0 {
			// Outlast the supervisor's OpenTimeout, then succeed anyway —
			// the window this test exists for.
			<-release
			return late, nil
		}
		return newFakeSession(nil), nil
	}}
	tr, err := New(Options{Ring: ring, open: opener.open})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	defer tr.Stop()

	// Let the supervisor time out and move on to a second attempt.
	waitFor(t, "the supervisor to give up and retry", func() bool { return opener.attempts() >= 2 })
	close(release)

	waitFor(t, "the abandoned session to be closed", func() bool {
		late.mu.Lock()
		defer late.mu.Unlock()
		return late.closed
	})

	// And its handle must never have been kept.
	tr.mu.Lock()
	handle := tr.handle
	tr.mu.Unlock()
	if handle == "stale-handle-from-the-orphan" {
		t.Error("an abandoned session overwrote the resumption handle")
	}
}

// The gate exists to save money on a silent room, and must not swallow speech.
func TestSilenceGateSkipsSilenceButNotSpeech(t *testing.T) {
	fastTiming(t)
	silenceHold = 0 // gate from the first silent chunk
	ring := audio.NewRing(audio.ChunkBytes * 8)
	opener := &recordingOpener{build: func(int) (liveSession, error) { return newFakeSession(nil), nil }}
	tr, err := New(Options{Ring: ring, open: opener.open, SilenceGate: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	defer tr.Stop()

	waitFor(t, "the session", func() bool { return opener.session(0) != nil })

	ring.Write(make([]byte, audio.ChunkBytes)) // silence
	time.Sleep(30 * time.Millisecond)
	if got := opener.session(0).chunksSent(); got != 0 {
		t.Errorf("%d silent chunks were sent", got)
	}

	loud := make([]byte, audio.ChunkBytes)
	for i := 0; i+1 < len(loud); i += 2 {
		loud[i], loud[i+1] = 0x00, 0x40 // ~0.5 full scale
	}
	ring.Write(loud)
	waitFor(t, "speech to be sent", func() bool { return opener.session(0).chunksSent() >= 1 })
}

func TestRMS(t *testing.T) {
	if got := rms(nil); got != 0 {
		t.Errorf("rms(nil) = %v", got)
	}
	if got := rms(make([]byte, 320)); got != 0 {
		t.Errorf("rms(silence) = %v", got)
	}
	loud := make([]byte, 320)
	for i := 0; i+1 < len(loud); i += 2 {
		loud[i], loud[i+1] = 0x00, 0x40
	}
	if got := rms(loud); got < 0.4 || got > 0.6 {
		t.Errorf("rms(half scale) = %v, want about 0.5", got)
	}
}

// Stop must be safe from any state, and never hang.
func TestStopIsIdempotent(t *testing.T) {
	fastTiming(t)
	opener := &recordingOpener{build: func(int) (liveSession, error) { return newFakeSession(nil), nil }}
	tr, err := New(Options{Ring: audio.NewRing(audio.ChunkBytes), open: opener.open})
	if err != nil {
		t.Fatal(err)
	}
	tr.Stop() // never started
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err == nil {
		t.Error("a second Start was accepted")
	}
	tr.Stop()
	tr.Stop()
	if tr.State() != StateIdle {
		t.Errorf("state after Stop = %v", tr.State())
	}
}

func TestNewRejectsMissingPieces(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Error("a translator with no ring buffer was accepted")
	}
	if _, err := New(Options{Ring: audio.NewRing(16)}); err == nil {
		t.Error("a translator with no API key and no opener was accepted")
	}
}

func TestStatsSummary(t *testing.T) {
	s := Stats{Reconnects: 2, GapsMS: []float64{412, 388}, ChunksSent: 900, MaxBacklogMS: 12.4}
	got := s.Summary()
	for _, want := range []string{"reconnects=2", "gaps_ms=[412, 388]", "chunks_sent=900", "max_backlog=12ms"} {
		if !strings.Contains(got, want) {
			t.Errorf("Summary = %q, want it to contain %q", got, want)
		}
	}
}

// A failed key test must never put the key in the error it reports.
//
// Preferences prints that error verbatim in a label, so this is what stops the
// key being shown in clear on screen. It used to travel in the query string,
// and net/http's *url.Error prints the whole URL — every DNS failure and every
// timeout displayed the key.
func TestKeyTestErrorsNeverCarryTheKey(t *testing.T) {
	const key = "AIzaSyFAKEKEYFAKEKEYFAKEKEYFAKEKEY123"

	// A server that accepts the connection and closes it without answering, so
	// the failure is at transport level — the case that used to leak.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not listen: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	original := KeyTestEndpoint
	KeyTestEndpoint = "http://" + listener.Addr().String() + "/v1beta/models?pageSize=1"
	defer func() { KeyTestEndpoint = original }()

	err = TestAPIKey(key)
	if err == nil {
		t.Fatal("expected the request to fail")
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("the API key appears in the error shown to the user: %v", err)
	}
}

// The key rides in the header, not the URL. Checked directly, because that is
// the property the test above depends on.
func TestKeyTestSendsTheKeyInAHeader(t *testing.T) {
	const key = "AIzaSyFAKEKEYFAKEKEYFAKEKEYFAKEKEY123"

	var gotHeader, gotURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("x-goog-api-key")
		gotURL = r.URL.String()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	original := KeyTestEndpoint
	KeyTestEndpoint = server.URL + "/v1beta/models?pageSize=1"
	defer func() { KeyTestEndpoint = original }()

	if err := TestAPIKey(key); err != nil {
		t.Fatalf("TestAPIKey = %v, want success", err)
	}
	if gotHeader != key {
		t.Errorf("x-goog-api-key = %q, want the key", gotHeader)
	}
	if strings.Contains(gotURL, key) {
		t.Errorf("the key is still in the URL: %q", gotURL)
	}
}

// The session config is the preview model's contract; getting it wrong is what
// spike L1 spent its afternoon on.
func TestSessionConfigMatchesTheModelContract(t *testing.T) {
	cfg := SessionConfig{
		TargetLanguageCode: "en",
		SourceTerms:        []string{"영업이익률"},
		CaptionTerms:       []string{"Pyeongwoo Lee"},
	}
	built := cfg.build("handle-9")

	if len(built.ResponseModalities) != 1 || built.ResponseModalities[0] != "AUDIO" {
		t.Errorf("ResponseModalities = %v, want AUDIO only", built.ResponseModalities)
	}
	if built.TranslationConfig.TargetLanguageCode != "en" {
		t.Errorf("target = %q", built.TranslationConfig.TargetLanguageCode)
	}
	if strings.Contains(built.TranslationConfig.TargetLanguageCode, "-") {
		t.Error("a region-qualified language code would be rejected by the API")
	}
	if built.ContextWindowCompression == nil || built.ContextWindowCompression.SlidingWindow == nil {
		t.Error("no sliding window — the session would not survive 30 minutes")
	}
	if built.SessionResumption == nil || built.SessionResumption.Handle != "handle-9" {
		t.Error("the resumption handle was not offered")
	}
	// The two vocabularies must never cross.
	if got := built.InputAudioTranscription.CustomVocabulary; len(got) != 1 || got[0] != "영업이익률" {
		t.Errorf("input vocabulary = %v", got)
	}
	if got := built.OutputAudioTranscription.CustomVocabulary; len(got) != 1 || got[0] != "Pyeongwoo Lee" {
		t.Errorf("output vocabulary = %v", got)
	}
	// No terms means no vocabulary field, not an empty one.
	plain := SessionConfig{TargetLanguageCode: "ko"}.build("")
	if len(plain.InputAudioTranscription.CustomVocabulary) != 0 {
		t.Error("an empty vocabulary was sent")
	}
}
