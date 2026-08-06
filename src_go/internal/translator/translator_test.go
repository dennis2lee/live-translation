package translator

import (
	"context"
	"errors"
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
	}
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
	BackoffSeconds = []time.Duration{5 * time.Millisecond}
	MinSessionSeconds = 20 * time.Millisecond
	OpenTimeout = 500 * time.Millisecond
	idlePoll = time.Millisecond
	backlogGrace = 10 * time.Millisecond
	t.Cleanup(func() {
		BackoffSeconds, MinSessionSeconds, OpenTimeout, idlePoll, backlogGrace = backoff, minSession, open, poll, grace
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
			{AudioBytes: 4096},
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
	// The old session is closed only after the new one is ready.
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
