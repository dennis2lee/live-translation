// Package translator is the Gemini Live API integration.
//
// An audio session lasts about 15 minutes and the WebSocket about 10, so
// surviving 30 minutes needs all three of:
//
//  1. ContextWindowCompression with a SlidingWindow — extend the session itself
//  2. SessionResumption — keep and reuse the newest handle
//  3. open a replacement session when go_away arrives, *before* closing the old
//     one
//
// The ring buffer keeps filling during a reconnect and whatever accumulated is
// pushed to the new session, so no sentence is lost.
//
// Three goroutines, with the same crossing rules the Python build's three
// threads had: a supervisor that owns session lifetime, a sender that drains
// the ring, and one receiver per session. Callbacks run on those goroutines —
// never touch a widget from them; cross to the UI with its dispatcher.
package translator

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
)

var (
	// BackoffSeconds is the retry ladder after a failed connect.
	BackoffSeconds = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	// MaxConsecutiveFailures gives up rather than hammering a broken endpoint.
	MaxConsecutiveFailures = 5
)

// These are variables rather than constants so the tests can shorten them.
// Nothing outside a test changes them.
var (
	// MinSessionSeconds: a session that dies sooner than this counts as a
	// failure even though the connection itself succeeded. Without it, a server
	// that accepts and then drops every connection would be reconnected to in a
	// tight loop. Real sessions last about nine minutes.
	MinSessionSeconds = 5 * time.Second

	// OpenTimeout bounds how long a connect may hang.
	OpenTimeout = 30 * time.Second

	idlePoll     = 20 * time.Millisecond
	silenceHold  = 3 * time.Second
	backlogGrace = 15 * time.Second
)

const (
	silenceRMSThreshold = 0.003

	// bytesPerMS is 32, at 16 kHz 16-bit mono.
	bytesPerMS = float64(audio.TargetRate*audio.BytesPerSample) / 1000.0

	// BacklogWarnMS: unsent audio in the ring means the captions trail the
	// speaker by at least that much, and that whatever the model answers
	// arrives all at once. A backlog is normal while connecting, so it is only
	// worth reporting once a session has settled — hence the grace period.
	BacklogWarnMS   = 1500.0
	stopJoinTimeout = 6 * time.Second
)

// State is what the status indicator shows.
type State string

const (
	StateIdle         State = "idle"
	StateConnecting   State = "connecting"
	StateRunning      State = "running"
	StateReconnecting State = "reconnecting"
	StateError        State = "error"
)

// fatalErrors are failures retrying cannot fix. Report immediately instead of
// backing off.
var fatalErrors = []struct{ marker, message string }{
	{"credits are depleted", "Billing credits exhausted — set up billing in AI Studio"},
	{"billing", "Billing required — this model is not on the free tier"},
	{"api key not valid", "API key is not valid"},
	{"api_key_invalid", "API key is not valid"},
	{"unauthenticated", "API key authentication failed"},
	{"permission_denied", "No permission to access this model"},
	{"was not found", "Model not found — check the model name"},
}

// FatalReason returns the message to show for a non-retryable error, or "".
func FatalReason(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(err.Error())
	for _, f := range fatalErrors {
		if strings.Contains(text, f.marker) {
			return f.message
		}
	}
	return ""
}

// Stats is what the session did, for the transcript footer and the logs.
type Stats struct {
	Reconnects   int
	GapsMS       []float64
	ChunksSent   int
	StartedAt    time.Time
	MaxBacklogMS float64
}

// Summary is the one-line form written to the log at stop.
func (s Stats) Summary() string {
	gaps := "-"
	if len(s.GapsMS) > 0 {
		parts := make([]string, len(s.GapsMS))
		for i, g := range s.GapsMS {
			parts[i] = formatMS(g)
		}
		gaps = strings.Join(parts, ", ")
	}
	elapsed := 0.0
	if !s.StartedAt.IsZero() {
		elapsed = time.Since(s.StartedAt).Minutes()
	}
	return "elapsed=" + formatMinutes(elapsed) + "min reconnects=" + itoa(s.Reconnects) +
		" gaps_ms=[" + gaps + "] chunks_sent=" + itoa(s.ChunksSent) +
		" max_backlog=" + formatMS(s.MaxBacklogMS) + "ms"
}

// Callbacks are delivered on the translator's own goroutines.
type Callbacks struct {
	OnSource       func(text string)
	OnTarget       func(text string)
	OnState        func(state State, detail string)
	OnReconnect    func()
	OnTurnComplete func()
}

func (c Callbacks) sourceText(t string) {
	if c.OnSource != nil {
		c.OnSource(t)
	}
}
func (c Callbacks) targetText(t string) {
	if c.OnTarget != nil {
		c.OnTarget(t)
	}
}
func (c Callbacks) state(s State, detail string) {
	if c.OnState != nil {
		c.OnState(s, detail)
	}
}
func (c Callbacks) reconnected() {
	if c.OnReconnect != nil {
		c.OnReconnect()
	}
}
func (c Callbacks) turnComplete() {
	if c.OnTurnComplete != nil {
		c.OnTurnComplete()
	}
}

// Options configures a Translator.
type Options struct {
	Ring        *audio.Ring
	APIKey      string
	Session     SessionConfig
	SilenceGate bool
	Callbacks   Callbacks

	// open is the session opener. Left nil, a genai-backed one is built from
	// APIKey; the tests set it to a fake.
	open sessionOpener
}

// Translator pushes PCM from the ring buffer to the Live API and emits
// transcriptions.
type Translator struct {
	ring        *audio.Ring
	apiKey      string
	sessionCfg  SessionConfig
	silenceGate bool
	cb          Callbacks
	open        sessionOpener

	mu     sync.Mutex
	state  State
	stats  Stats
	handle string
	active *sessionHolder
	// lastError is the most recent open failure, kept so the supervisor can
	// decide whether it is worth retrying.
	lastError error

	cancel  context.CancelFunc
	done    chan struct{}
	running bool
}

// sessionHolder is one Live session bundled with its receive goroutine.
type sessionHolder struct {
	session liveSession

	ready  chan struct{}
	closed chan struct{}
	goAway chan struct{}

	closeOnce  sync.Once
	goAwayOnce sync.Once
	readyOnce  sync.Once

	mu  sync.Mutex
	err error
}

func newSessionHolder() *sessionHolder {
	return &sessionHolder{
		ready:  make(chan struct{}),
		closed: make(chan struct{}),
		goAway: make(chan struct{}),
	}
}

func (h *sessionHolder) markReady()  { h.readyOnce.Do(func() { close(h.ready) }) }
func (h *sessionHolder) markClosed() { h.closeOnce.Do(func() { close(h.closed) }) }
func (h *sessionHolder) markGoAway() { h.goAwayOnce.Do(func() { close(h.goAway) }) }

func (h *sessionHolder) setErr(err error) {
	h.mu.Lock()
	h.err = err
	h.mu.Unlock()
}

func (h *sessionHolder) Err() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}

func (h *sessionHolder) isClosed() bool {
	select {
	case <-h.closed:
		return true
	default:
		return false
	}
}

func (h *sessionHolder) sawGoAway() bool {
	select {
	case <-h.goAway:
		return true
	default:
		return false
	}
}

// New builds a Translator. It fails only if the API client cannot be created.
func New(opts Options) (*Translator, error) {
	if opts.Ring == nil {
		return nil, errors.New("translator: a ring buffer is required")
	}
	open := opts.open
	if open == nil {
		if strings.TrimSpace(opts.APIKey) == "" {
			return nil, errors.New("translator: no API key")
		}
		var err error
		open, err = newGenaiOpener(opts.APIKey, opts.Session)
		if err != nil {
			return nil, err
		}
	}
	return &Translator{
		ring:        opts.Ring,
		apiKey:      opts.APIKey,
		sessionCfg:  opts.Session,
		silenceGate: opts.SilenceGate,
		cb:          opts.Callbacks,
		open:        open,
		state:       StateIdle,
	}, nil
}

// State is the current connection state.
func (t *Translator) State() State {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// Stats is a snapshot of what the session has done.
func (t *Translator) Stats() Stats {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.stats
	out.GapsMS = append([]float64(nil), t.stats.GapsMS...)
	return out
}

// Start brings the supervisor up. Calling it twice is an error.
func (t *Translator) Start() error {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return errors.New("translator already started")
	}
	// A resumption handle only refers to a session of this run. Reusing one
	// across a stop/start would ask the server to resume a dead session.
	t.handle = ""
	t.lastError = nil
	t.stats = Stats{StartedAt: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.done = make(chan struct{})
	t.running = true
	done := t.done
	t.mu.Unlock()

	go func() {
		defer close(done)
		t.supervise(ctx)
	}()
	return nil
}

// Stop tears everything down and waits for it. Idempotent.
func (t *Translator) Stop() {
	t.mu.Lock()
	if !t.running {
		t.mu.Unlock()
		return
	}
	cancel, done := t.cancel, t.done
	t.running = false
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	select {
	case <-done:
	case <-time.After(stopJoinTimeout):
		slog.Error("translator did not exit in time", "timeout", stopJoinTimeout)
	}
	t.setState(StateIdle, "")
	slog.Info("translator stopped", "stats", t.Stats().Summary())
}

func (t *Translator) setState(state State, detail string) {
	t.mu.Lock()
	changed := state != t.state || detail != ""
	t.state = state
	t.mu.Unlock()
	if changed {
		t.cb.state(state, detail)
	}
}

// --- supervisor -----------------------------------------------------------

// supervise opens sessions, replaces them on go_away or drop, and keeps the
// sender alive.
func (t *Translator) supervise(ctx context.Context) {
	var senderDone sync.WaitGroup
	senderDone.Add(1)
	go func() {
		defer senderDone.Done()
		t.sendLoop(ctx)
	}()

	defer func() {
		senderDone.Wait()
		t.mu.Lock()
		active := t.active
		t.active = nil
		t.mu.Unlock()
		if active != nil {
			t.closeSession(active)
		}
	}()

	failures := 0
	for ctx.Err() == nil {
		// Having a session to replace means this is a reconnect. Using
		// Stats.Reconnects would mislabel the first one, since it is still 0.
		t.mu.Lock()
		reconnecting := t.active != nil
		t.mu.Unlock()
		if reconnecting {
			t.setState(StateReconnecting, "")
		} else {
			t.setState(StateConnecting, "")
		}

		gapStarted := time.Now()
		holder := t.openSession(ctx)
		if ctx.Err() != nil {
			return
		}

		if holder == nil {
			t.mu.Lock()
			lastErr := t.lastError
			t.mu.Unlock()
			if reason := FatalReason(lastErr); reason != "" {
				// Credit, auth and permission problems do not heal with time.
				slog.Error("fatal", "err", lastErr)
				t.setState(StateError, reason)
				return
			}
			failures++
			if failures >= MaxConsecutiveFailures {
				t.setState(StateError, "Repeated connection failures")
				return
			}
			delay := backoffFor(failures)
			slog.Warn("connect failed", "attempt", failures, "of", MaxConsecutiveFailures, "retry_in", delay)
			if !sleepCtx(ctx, delay) {
				return
			}
			continue
		}

		openedAt := time.Now()
		t.mu.Lock()
		previous := t.active
		t.active = holder // atomically swap the send target
		t.mu.Unlock()
		gapMS := float64(time.Since(gapStarted).Microseconds()) / 1000.0

		if previous != nil {
			t.mu.Lock()
			t.stats.Reconnects++
			t.stats.GapsMS = append(t.stats.GapsMS, gapMS)
			count := t.stats.Reconnects
			t.mu.Unlock()
			slog.Info("session swapped", "gap_ms", gapMS, "reconnect", count)
			t.cb.reconnected()
			t.closeSession(previous)
		}

		t.setState(StateRunning, "")

		// Keep translating on this session until it warns or drops. The old
		// session stays alive past go_away, until the replacement is ready.
		select {
		case <-holder.goAway:
		case <-holder.closed:
		case <-ctx.Done():
			return
		}

		// A session that opened and died immediately is a failure too, or a
		// server dropping every connection becomes a hot loop.
		lifetime := time.Since(openedAt)
		if lifetime < MinSessionSeconds && !holder.sawGoAway() {
			if reason := FatalReason(holder.Err()); reason != "" {
				slog.Error("fatal", "err", holder.Err())
				t.setState(StateError, reason)
				return
			}
			failures++
			if failures >= MaxConsecutiveFailures {
				t.setState(StateError, "Session keeps dropping")
				return
			}
			delay := backoffFor(failures)
			slog.Warn("session dropped early", "lifetime", lifetime,
				"attempt", failures, "of", MaxConsecutiveFailures, "retry_in", delay)
			if !sleepCtx(ctx, delay) {
				return
			}
		} else {
			failures = 0
		}
	}
}

// openSession opens a session and returns its holder once it is ready.
func (t *Translator) openSession(ctx context.Context) *sessionHolder {
	holder := newSessionHolder()

	t.mu.Lock()
	handle := t.handle
	t.mu.Unlock()

	go t.sessionMain(ctx, holder, handle)

	select {
	case <-holder.ready:
		t.mu.Lock()
		t.lastError = nil
		t.mu.Unlock()
		return holder
	case <-holder.closed:
	case <-ctx.Done():
		return nil
	case <-time.After(OpenTimeout):
		slog.Warn("session open timed out", "timeout", OpenTimeout)
	}

	err := holder.Err()
	t.mu.Lock()
	t.lastError = err
	t.mu.Unlock()
	if err != nil {
		slog.Warn("session open failed", "err", err)
	}
	return nil
}

// sessionMain runs one session's receive loop, closing `closed` when it ends.
func (t *Translator) sessionMain(ctx context.Context, holder *sessionHolder, handle string) {
	defer holder.markClosed()

	openCtx, cancel := context.WithTimeout(ctx, OpenTimeout)
	session, err := t.open(openCtx, handle)
	cancel()
	if err != nil {
		holder.setErr(err)
		return
	}
	holder.session = session
	holder.markReady()
	slog.Info("live session open", "resumed", handle != "")

	defer func() {
		if err := session.Close(); err != nil {
			slog.Debug("error closing session", "err", err)
		}
	}()

	// Fragment inter-arrival, for the debug log below. Per session, which is
	// what matters: the gap across a reconnect is the swap, not the speaker.
	var lastFragmentAt time.Time

	for {
		if ctx.Err() != nil {
			return
		}
		msg, err := session.Receive()
		if err != nil {
			if ctx.Err() == nil {
				holder.setErr(err)
				slog.Warn("session ended", "err", err)
			}
			return
		}

		// 1) Shutdown warning — triggers the pre-emptive reconnect. Only a flag
		// is set: this session must keep receiving until the swap completes.
		if msg.GoAway && !holder.sawGoAway() {
			slog.Info("go_away received — opening replacement", "time_left", msg.GoAwayLeft)
			holder.markGoAway()
		}

		// 2) Resumption handle — always keep the newest.
		if msg.ResumeHandle != "" {
			t.mu.Lock()
			t.handle = msg.ResumeHandle
			t.mu.Unlock()
		}

		// 3) Source-language transcription (hidden by default).
		if msg.SourceText != "" {
			t.cb.sourceText(msg.SourceText)
		}
		// 4) The translation — this is what goes on screen.
		if msg.TargetText != "" {
			// The gap since the previous fragment is what decides
			// captions.IdleCommitSeconds, and until now nothing recorded it:
			// the constant was 1.2 s against a fragment interval measured at
			// "about one per second", which leaves 0.2 s of slack before a
			// pause splits a sentence. A 67-minute session settled 30% of its
			// lines on that timer in plain speech and 53% over singing. Run
			// with debug logging on and this gives the real distribution, so
			// the constant can be set from a measurement like every other one.
			if slog.Default().Enabled(ctx, slog.LevelDebug) {
				now := time.Now()
				gap := time.Duration(0)
				if !lastFragmentAt.IsZero() {
					gap = now.Sub(lastFragmentAt)
				}
				lastFragmentAt = now
				slog.Debug("target fragment",
					"gap_ms", gap.Milliseconds(),
					"runes", len([]rune(msg.TargetText)))
			}
			t.cb.targetText(msg.TargetText)
		}
		// 5) End of turn, the one boundary the API reports reliably. This
		// message carries no transcription payload of its own, hence the
		// separate check.
		if msg.TurnComplete {
			t.cb.turnComplete()
		}
		// 6) msg.AudioBytes was already consumed and discarded by the adapter.
		// Failing to read the audio stalls the stream.
	}
}

func (t *Translator) closeSession(holder *sessionHolder) {
	if holder.session != nil {
		_ = holder.session.Close()
	}
	holder.markClosed()
}

// --- sender ---------------------------------------------------------------

// sendLoop drains the ring buffer into the active session. With no session,
// audio just accumulates — that is what survives a reconnect.
func (t *Translator) sendLoop(ctx context.Context) {
	var silentSince time.Time
	warnedBacklog := false

	for ctx.Err() == nil {
		t.mu.Lock()
		holder := t.active
		started := t.stats.StartedAt
		t.mu.Unlock()

		if holder == nil || holder.session == nil || holder.isClosed() {
			// Reconnecting. The buffer keeps filling; it drains once a session
			// opens.
			if !sleepCtx(ctx, idlePoll) {
				return
			}
			continue
		}

		backlogMS := float64(t.ring.Available()) / bytesPerMS
		t.mu.Lock()
		if backlogMS > t.stats.MaxBacklogMS {
			t.stats.MaxBacklogMS = backlogMS
		}
		t.mu.Unlock()
		if !warnedBacklog && backlogMS >= BacklogWarnMS && time.Since(started) >= backlogGrace {
			warnedBacklog = true
			slog.Warn("audio is queueing up — captions will trail and arrive in bursts",
				"backlog_ms", backlogMS)
		}

		chunk, err := t.ring.Read(audio.ChunkBytes)
		if err != nil {
			slog.Error("ring read failed", "err", err)
			return
		}
		if chunk == nil {
			if !sleepCtx(ctx, idlePoll) {
				return
			}
			continue
		}

		if t.silenceGate {
			now := time.Now()
			if rms(chunk) < silenceRMSThreshold {
				if silentSince.IsZero() {
					silentSince = now
				}
				if now.Sub(silentSince) >= silenceHold {
					continue // skip the send to save cost
				}
			} else {
				silentSince = time.Time{}
			}
		}

		if err := holder.session.SendAudio(chunk); err != nil {
			slog.Warn("send failed, will retry on the next session", "err", err)
			holder.markClosed()
			if !sleepCtx(ctx, idlePoll) {
				return
			}
			continue
		}
		t.mu.Lock()
		t.stats.ChunksSent++
		t.mu.Unlock()
	}
}

// --- helpers --------------------------------------------------------------

func backoffFor(failures int) time.Duration {
	i := failures - 1
	if i < 0 {
		i = 0
	}
	if i >= len(BackoffSeconds) {
		i = len(BackoffSeconds) - 1
	}
	return BackoffSeconds[i]
}

// sleepCtx sleeps unless the context is cancelled first. It reports whether the
// sleep completed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func rms(pcm []byte) float64 {
	n := len(pcm) / 2
	if n == 0 {
		return 0
	}
	var acc float64
	for i := 0; i+1 < len(pcm); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[i:i+2]))) / 32768.0
		acc += v * v
	}
	return math.Sqrt(acc / float64(n))
}
