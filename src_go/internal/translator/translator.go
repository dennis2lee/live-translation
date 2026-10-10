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
	"sync/atomic"
	"time"
	"unicode/utf8"

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
	// OpenAI's first, because its quota message links to a "billing" page
	// and would otherwise be reported as Gemini's free-tier problem.
	{"insufficient_quota", "OpenAI credit exhausted — add credit at platform.openai.com"},
	{"invalid_api_key", "API key is not valid"},
	{"401 unauthorized", "API key is not valid"},
	{"model_not_found", "Model not found — check the model name"},
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

// RetireGrace bounds how long a replaced session is held open when its
// replacement never speaks. A var so the tests can shorten it, like OpenTimeout.
//
// The replacement needs 3.3–5.2 s of input before it produces any audio, and if
// nobody is talking it may produce none at all — so this cannot wait forever on
// a sound that is not coming. 12 s is past twice the measured worst case, and
// well inside the 50 s of grace the server gives after go_away, so the retiring
// session is always closed by us rather than dropped by the far end.
var RetireGrace = 12 * time.Second

// PrepReserve is how much of the server's go_away grace adaptive session prep
// refuses to spend. Whatever is left over is how long it may wait for a quiet
// moment.
//
// The swap needs the connect (243–391 ms measured) plus the replacement's
// warm-up before it speaks (3.3–5.2 s), and RetireGrace (12 s) bounds the case
// where it never speaks at all — so 20 s covers the worst measured path with
// the whole retirement window inside it. Against the 50 s the server gives,
// that leaves about 30 s of window-shopping. Erring large is right: waiting
// too long means the far end hangs up mid-swap, which is the 2.8–4.2 s hole
// that 1.3.0 was built to remove, while waiting too little only means the swap
// happens where it used to.
var PrepReserve = 20 * time.Second

// prepPoll is how often the supervisor asks whether the moment has come. The
// window it is waiting for is a song, which lasts tens of seconds, so this
// costs nothing and does not need to be fine.
var prepPoll = 250 * time.Millisecond

// SpeechGapSeconds is how long the translated speech has to stop before the gap
// is worth recording.
//
// Measured, not guessed. Over an uninterrupted 103 s session the longest gap
// between audio blobs was 0.38 s; a reconnect costs 3.6–4.2 s, because the
// replacement session needs 3.3–5.2 s of input before it says anything and the
// jitter buffer only ever holds about 0.57 s (spike v3_voiceid). This sits in
// the empty space between those, so ordinary cadence records nothing and a real
// hole records every time.
const SpeechGapSeconds = 1.5

// Stats is what the session did, for the transcript footer and the logs.
type Stats struct {
	Reconnects int
	GapsMS     []float64
	// PreppedSwaps is how many reconnects adaptive session prep placed in a
	// quiet window, PrepWaitsMS how long each waited, and UnpreppedSwaps how
	// many gave up and swapped on the server's schedule. Together they answer
	// "did this feature do anything" from the transcript footer alone.
	PreppedSwaps   int
	PrepWaitsMS    []float64
	UnpreppedSwaps int
	// QuietHandovers is how many warm handovers switched in a pause (or under
	// the music gate), ForcedHandovers how many ran out of time or lost the
	// old session first, and HandoverWaitsMS how long each waited.
	QuietHandovers  int
	ForcedHandovers int
	HandoverWaitsMS []float64
	ChunksSent      int
	StartedAt       time.Time
	MaxBacklogMS    float64

	// SpeechGaps counts stretches with no translated audio at all, and
	// SwapSilenceMS totals the ones that straddled a reconnect.
	//
	// The distinction is the whole point of measuring this. A gap while nobody
	// is speaking is the system working; a gap on either side of a session swap
	// is the cost of the swap, and it is the one a listener complains about
	// because the voice is different afterwards. Separating them is what lets a
	// user answer "is this my network?" from the log instead of by guessing.
	SpeechGaps     int
	MaxSpeechGapMS float64
	SwapSilenceMS  float64
	SwapGaps       int
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
		" max_backlog=" + formatMS(s.MaxBacklogMS) + "ms" +
		" speech_gaps=" + itoa(s.SpeechGaps) +
		" max_speech_gap=" + formatMS(s.MaxSpeechGapMS) + "ms" +
		" swap_silence=" + formatMS(s.SwapSilenceMS) + "ms/" + itoa(s.SwapGaps) +
		" handovers=" + itoa(s.QuietHandovers) + "/" + itoa(s.QuietHandovers+s.ForcedHandovers) +
		" prepped_swaps=" + itoa(s.PreppedSwaps) + "/" +
		itoa(s.PreppedSwaps+s.UnpreppedSwaps)
}

// Callbacks are delivered on the translator's own goroutines.
type Callbacks struct {
	OnSource       func(text string)
	OnTarget       func(text string)
	OnState        func(state State, detail string)
	OnReconnect    func()
	OnTurnComplete func()
	// OnAudio receives the translated speech, 24 kHz mono PCM16. It is called
	// from the receive loop, so it must not block: anything slow here stalls
	// the stream for the captions too.
	OnAudio func(pcm []byte)
	// OnSessionStart fires once per session, on the receive goroutine,
	// immediately before that session's first audio reaches OnAudio. The
	// player uses it to know a session's opening from the rest of its speech
	// — the far end's synthesis buzzes there, with no word in front of it.
	// Same rule as OnAudio: called synchronously, so it is ordered with the
	// audio, and it must not block.
	OnSessionStart func()
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
func (c Callbacks) sessionStart() {
	if c.OnSessionStart != nil {
		c.OnSessionStart()
	}
}

func (c Callbacks) audio(pcm []byte) {
	if c.OnAudio != nil && len(pcm) > 0 {
		c.OnAudio(pcm)
	}
}

// Options configures a Translator.
type Options struct {
	Ring   *audio.Ring
	APIKey string
	// Provider picks the service: ProviderOpenAI, or anything else for
	// Gemini. A string rather than config.Provider so this package stays
	// free of the settings package.
	Provider    string
	Session     SessionConfig
	SilenceGate bool
	Callbacks   Callbacks

	// Filter, when set, decides per chunk what actually goes to the API: the
	// chunk, nothing, or several chunks (a gate reopening flushes what it had
	// withheld). It sees every chunk regardless of the silence gate, which is
	// applied after it — a classifier that goes deaf during silence cannot
	// notice the music ending. Must not block: the send loop paces real-time
	// audio.
	Filter func(chunk []byte) [][]byte

	// AdaptiveSessionPrep holds a reconnect's session swap back until
	// QuietMoment says nobody is listening. See waitForSwapWindow.
	AdaptiveSessionPrep bool

	// QuietMoment reports whether audio is being withheld right now, so a
	// swap would be inaudible — in the application, the music gate being
	// closed. Nil means there is no such window to wait for and adaptive prep
	// does nothing, which is what makes it safe to leave on by default.
	//
	// Called from the supervisor on a poll, never from the send loop.
	QuietMoment func() bool

	// WarmHandover opens a reconnect's replacement as soon as the warning
	// arrives, feeds it unheard alongside the old session, and switches in a
	// pause. See handOver.
	WarmHandover bool

	// ForceSwapAfter, when set, has every session warned as if by the server
	// this long after it opens, with 50 s of grace — the measured go_away.
	// For measuring handovers without waiting nine minutes; the application
	// never sets it.
	ForceSwapAfter time.Duration

	// open is the session opener. Left nil, a genai-backed one is built from
	// APIKey; the tests set it to a fake.
	open sessionOpener
}

// Translator pushes PCM from the ring buffer to the Live API and emits
// transcriptions.
type Translator struct {
	ring         *audio.Ring
	apiKey       string
	sessionCfg   SessionConfig
	silenceGate  bool
	filter       func(chunk []byte) [][]byte
	adaptivePrep bool
	quietMoment  func() bool
	warmHandover bool
	forceSwap    time.Duration
	cb           Callbacks
	open         sessionOpener

	mu     sync.Mutex
	state  State
	stats  Stats
	handle string

	// retiring is a session that has been replaced but is deliberately still
	// open. See watchRetirement() for why.
	retiring *sessionHolder
	// warming is a replacement being fed alongside the active session, not
	// yet heard. See handOver.
	warming *sessionHolder
	// retireWG tracks the watchdogs so a run does not outlive itself. Without
	// it a watchdog kept running after Stop returned — harmless in production
	// but a real loose end, and the race detector called it what it was.
	retireWG sync.WaitGroup

	// lastAudioAt and audioReconnects are how a silence is attributed. They
	// belong to the run rather than to a session, because the gap that matters
	// is exactly the one that spans two of them.
	lastAudioAt     time.Time
	audioReconnects int
	active          *sessionHolder
	// lastError is the most recent open failure, kept so the supervisor can
	// decide whether it is worth retrying.
	lastError error

	cancel  context.CancelFunc
	done    chan struct{}
	running bool
}

// sessionHolder is one Live session bundled with its receive goroutine.
type sessionHolder struct {
	ready  chan struct{}
	closed chan struct{}
	goAway chan struct{}

	closeOnce  sync.Once
	goAwayOnce sync.Once
	readyOnce  sync.Once

	mu      sync.Mutex
	err     error
	session liveSession
	// goAwayAt and goAwayLeft record the shutdown warning: when it arrived
	// and how long the server said was left. Adaptive session prep spends
	// that grace looking for a quiet moment, so it has to know how much
	// there is rather than assume the measured 50 s.
	goAwayAt   time.Time
	goAwayLeft time.Duration

	// spoke is set by this session's own receive loop when its first audio
	// arrives, and read nowhere else.
	spoke bool

	// warming is set while this session is a replacement being fed but not
	// heard; its receive loop then delivers nothing. Cleared by the
	// supervisor in the same breath as making it active.
	warming atomic.Bool
	// openedAt, firstAudio and lastVoice are unix nanoseconds, for deciding
	// when a handover can be made unheard. See noteOutput.
	openedAt   atomic.Int64
	firstAudio atomic.Int64
	lastVoice  atomic.Int64
	// sentenceEnded is whether this session's latest caption fragment
	// closed a sentence.
	sentenceEnded atomic.Bool
	// recent is the tail of this session's captions, under mu. See noteText.
	recent string
	// abandoned is set when the supervisor gives up waiting for this holder.
	// A session that opens afterwards has no owner and must not run — see
	// adopt.
	abandoned bool
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
func (h *sessionHolder) markGoAway(left time.Duration) {
	h.goAwayOnce.Do(func() {
		h.mu.Lock()
		h.goAwayAt, h.goAwayLeft = time.Now(), left
		h.mu.Unlock()
		close(h.goAway)
	})
}

// graceLeft is how much of the server's warning remains. Zero when no warning
// has arrived, or when the server did not say how long it was giving us.
func (h *sessionHolder) graceLeft() time.Duration {
	h.mu.Lock()
	at, left := h.goAwayAt, h.goAwayLeft
	h.mu.Unlock()
	if at.IsZero() || left <= 0 {
		return 0
	}
	return left - time.Since(at)
}

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

// live is the open session, or nil.
func (h *sessionHolder) live() liveSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.session
}

// adopt stores a freshly opened session and reports whether it may run.
//
// It returns false when the supervisor already gave up on this holder — the
// open timed out, or the context was cancelled — in which case the caller owns
// the session and must close it. Without this, a connect that completed in the
// moment after the supervisor's timer fired produced a session belonging to
// nobody: still receiving, still writing t.handle, and alive until the server
// dropped it up to ten minutes later. Its stale resumption handle could then
// overwrite the live session's newer one.
func (h *sessionHolder) adopt(session liveSession) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.abandoned {
		return false
	}
	h.session = session
	return true
}

// abandon marks the holder unwanted and hands back any session already stored,
// so exactly one side closes it: whichever of adopt and abandon ran second.
func (h *sessionHolder) abandon() liveSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.abandoned = true
	session := h.session
	h.session = nil
	return session
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
		if opts.Provider == ProviderOpenAI {
			open, err = newOpenAIOpener(opts.APIKey, opts.Session)
		} else {
			open, err = newGenaiOpener(opts.APIKey, opts.Session)
		}
		if err != nil {
			return nil, err
		}
	}
	return &Translator{
		ring:         opts.Ring,
		apiKey:       opts.APIKey,
		sessionCfg:   opts.Session,
		silenceGate:  opts.SilenceGate,
		filter:       opts.Filter,
		adaptivePrep: opts.AdaptiveSessionPrep,
		quietMoment:  opts.QuietMoment,
		warmHandover: opts.WarmHandover,
		forceSwap:    opts.ForceSwapAfter,
		cb:           opts.Callbacks,
		open:         open,
		state:        StateIdle,
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
	// Likewise the silence clock: the time between stopping and starting again
	// is not a gap in anything.
	t.lastAudioAt = time.Time{}
	t.audioReconnects = 0
	t.retiring = nil
	t.warming = nil
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
		// Whatever brought the supervisor back — Stop, a fatal error, or
		// repeated failures — this run is over, so cancel it.
		//
		// Stop cancels before waiting, but the give-up paths do not: they set
		// StateError and return with the context still live. sendLoop runs
		// `for ctx.Err() == nil`, so senderDone.Wait() below then blocked for
		// ever, and the session underneath it stayed open and kept being fed
		// audio it was still being charged for, behind a window showing an
		// error. Found by TestAFatalErrorClosesTheHeldBackSessionAtOnce, which
		// was written for something else entirely.
		t.mu.Lock()
		cancel := t.cancel
		t.mu.Unlock()
		if cancel != nil {
			cancel()
		}

		senderDone.Wait()
		t.mu.Lock()
		active := t.active
		t.active = nil
		t.mu.Unlock()
		if active != nil {
			t.closeSession(active)
		}
		// So is one being warmed up to replace it.
		t.mu.Lock()
		warming := t.warming
		t.warming = nil
		t.mu.Unlock()
		if warming != nil {
			t.closeSession(warming)
		}
		// A session kept open across a swap is still ours to close.
		t.retireAny("the translator is stopping")
		// The watchdogs are already unblocked by the cancel above; this is
		// what makes a stopped run leave nothing of itself behind.
		t.retireWG.Wait()
	}()

	failures := 0
	// warmNext is a replacement handOver has already opened and warmed; the
	// next turn of the loop switches to it instead of opening one.
	var warmNext *sessionHolder
	for ctx.Err() == nil {
		holder, warm := warmNext, warmNext != nil
		warmNext = nil
		gapStarted := time.Now()
		if !warm {
			// Having a session to replace means this is a reconnect. Using
			// Stats.Reconnects would mislabel the first one, since it is
			// still 0. A warm switch is neither: nothing is connecting and
			// nothing is missing, so the indicator is left alone.
			t.mu.Lock()
			reconnecting := t.active != nil
			t.mu.Unlock()
			if reconnecting {
				t.setState(StateReconnecting, "")
			} else {
				t.setState(StateConnecting, "")
			}
			holder = t.openSession(ctx, false)
		}
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
		// Heard from this instant: its receive loop delivers from the next
		// message on. Cleared under the same lock as the swap, so no message
		// falls between being suppressed as a replacement's and delivered as
		// the active session's.
		holder.warming.Store(false)
		if t.warming == holder {
			t.warming = nil
		}
		// Marked retired in the same breath as the swap, not later. Between
		// those two points the replaced session is still receiving, and the
		// gate in noteHandle is what stops it writing a resumption handle for
		// a session that is on its way out.
		stale := t.retiring
		if previous != nil {
			t.retiring = previous
		}
		t.mu.Unlock()
		if stale != nil {
			// Two swaps inside one warm-up. Only one session can be held
			// back, and the older of the two has nothing left to say.
			slog.Info("closing a replaced session, replaced again before it finished")
			t.closeSession(stale)
		}
		gapMS := float64(time.Since(gapStarted).Microseconds()) / 1000.0

		if previous != nil {
			t.mu.Lock()
			t.stats.Reconnects++
			t.stats.GapsMS = append(t.stats.GapsMS, gapMS)
			count := t.stats.Reconnects
			t.mu.Unlock()
			slog.Info("session swapped", "gap_ms", gapMS, "reconnect", count, "warm", warm)
			t.cb.reconnected()
			if warm {
				// The replacement has been hearing everything and has been
				// speaking for a while; the old session has nothing left to
				// cover, and whatever it would say next the replacement
				// will say too.
				t.retireIf(previous, "the warm replacement has taken over")
			} else {
				t.watchRetirement(ctx, previous)
			}
		}

		t.setState(StateRunning, "")

		// Keep translating on this session until it warns or drops. The old
		// session stays alive past go_away, until the replacement is ready.
		var forced <-chan time.Time
		var forceTimer *time.Timer
		if t.forceSwap > 0 {
			forceTimer = time.NewTimer(t.forceSwap)
			forced = forceTimer.C
		}
		select {
		case <-holder.goAway:
		case <-forced:
			slog.Info("forcing a swap, as asked", "after", t.forceSwap)
			holder.markGoAway(50 * time.Second)
		case <-holder.closed:
		case <-ctx.Done():
		}
		if forceTimer != nil {
			forceTimer.Stop()
		}
		if ctx.Err() != nil {
			return
		}

		// Spend what the server gave us looking for a moment when the swap
		// cannot be heard. A warm handover opens the replacement now and
		// waits for a pause; otherwise adaptive prep may wait for a song.
		// Either returns at once unless everything lines up.
		if t.warmHandover && holder.sawGoAway() && !holder.isClosed() {
			warmNext = t.handOver(ctx, holder)
		}
		if warmNext == nil {
			t.waitForSwapWindow(ctx, holder)
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
func (t *Translator) openSession(ctx context.Context, warming bool) *sessionHolder {
	holder := newSessionHolder()
	// Before the receive loop exists, so not one message of a replacement's
	// opening can be delivered.
	holder.warming.Store(warming)

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
		t.discard(holder)
		return nil
	case <-time.After(OpenTimeout):
		slog.Warn("session open timed out", "timeout", OpenTimeout)
	}

	// Nothing owns this holder now. Mark it, so a session that opens a moment
	// later closes itself instead of running unattended.
	t.discard(holder)

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
	if !holder.adopt(session) {
		// The supervisor gave up while this was connecting and has moved on.
		slog.Warn("session opened after the supervisor gave up — closing it")
		if err := session.Close(); err != nil {
			slog.Debug("error closing an abandoned session", "err", err)
		}
		return
	}
	holder.openedAt.Store(time.Now().UnixNano())
	holder.markReady()
	slog.Info("live session open", "resumed", handle != "", "warming", holder.warming.Load())

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
			holder.markGoAway(msg.GoAwayLeft)
		}

		holder.noteOutput(msg)
		if holder.warming.Load() {
			// A replacement being warmed is heard from nothing: its captions
			// would repeat the active session's, and its opening is where
			// the buzz is. Its first audio still counts as the session
			// having spoken, so the freeze gate is never told a session
			// opens at the switch — what it plays from then on is the
			// middle of a session, not the start of one.
			if len(msg.Audio) > 0 {
				holder.spoke = true
			}
			continue
		}

		// 2) Resumption handle — always keep the newest the *active* session
		// offered. See noteHandle.
		if msg.ResumeHandle != "" {
			t.noteHandle(holder, msg.ResumeHandle)
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
				// The first fragment of a session has no gap to report. It used
				// to log 0, which is indistinguishable from an instant
				// redelivery — about eight false zeros an hour, in the very
				// distribution the constant is to be read off.
				if !lastFragmentAt.IsZero() {
					slog.Debug("target fragment",
						"gap_ms", now.Sub(lastFragmentAt).Milliseconds(),
						"runes", utf8.RuneCountInString(msg.TargetText))
				} else {
					slog.Debug("target fragment, first of the session",
						"runes", utf8.RuneCountInString(msg.TargetText))
				}
				lastFragmentAt = now
			}
			t.cb.targetText(msg.TargetText)
		}
		// 5) End of turn, the one boundary the API reports reliably. This
		// message carries no transcription payload of its own, hence the
		// separate check.
		if msg.TurnComplete {
			t.cb.turnComplete()
		}
		// 6) The translated speech. Consuming it is not optional — failing to
		// read the audio stalls the stream — but playing it is: with nobody
		// listening the callback is nil and the bytes go where they always
		// went.
		if len(msg.Audio) > 0 {
			if !holder.spoke {
				// Only this receive loop touches spoke.
				holder.spoke = true
				t.cb.sessionStart()
			}
			t.noteSpeech()
			// The replacement is speaking, so the session it replaced has
			// finished covering for it and can go.
			if t.isActive(holder) {
				t.retireAny("the replacement is speaking")
			}
		}
		t.cb.audio(msg.Audio)
	}
}

// noteSpeech records the arrival of translated audio and the silence before it.
//
// Called only when a message actually carried audio: the stream also delivers
// transcription and handle updates, and counting those as speech would mean the
// gap never appeared to open.
func (t *Translator) noteSpeech() {
	now := time.Now()
	t.mu.Lock()
	previous := t.lastAudioAt
	acrossSwap := t.stats.Reconnects != t.audioReconnects
	t.lastAudioAt = now
	t.audioReconnects = t.stats.Reconnects

	// The first blob of a run has no gap in front of it. Reporting one would
	// count the time before anyone spoke as a fault.
	if previous.IsZero() {
		t.mu.Unlock()
		return
	}
	gap := now.Sub(previous).Seconds()
	if gap < SpeechGapSeconds {
		t.mu.Unlock()
		return
	}
	t.stats.SpeechGaps++
	if ms := gap * 1000; ms > t.stats.MaxSpeechGapMS {
		t.stats.MaxSpeechGapMS = ms
	}
	if acrossSwap {
		t.stats.SwapGaps++
		t.stats.SwapSilenceMS += gap * 1000
	}
	t.mu.Unlock()

	// A gap around a swap is a finding; a gap in the middle of a session is
	// usually just nobody talking, so it goes to debug and stays out of the way.
	if acrossSwap {
		slog.Info("the translation went silent across a reconnect",
			"seconds", gap, "reconnects", t.audioReconnects)
	} else {
		slog.Debug("no translated speech for a while", "seconds", gap)
	}
}

// watchRetirement closes the replaced session once it is no longer useful.
//
// The session is marked retired by the supervisor at the moment of the swap;
// this only decides when it is closed.
//
// Closing it at the swap is what made a reconnect audible. Connecting takes
// 250–390 ms, but the replacement then needs 3.3–5.2 s of input before it says
// anything, and the jitter buffer holds 0.57 s — so the listener got 2.8–4.2 s
// of silence at every reconnect, roughly every nine minutes. Measured in
// spikes/v3_voiceid.
//
// A session that stops being fed does not stop working: it finishes translating
// what it already heard. That tail is about as long as the replacement's
// warm-up, so holding the old session open until the new one speaks covers the
// hole exactly. With this in place the same spike measured 0.00 s of silence
// across three swaps, and no gap at any swap point at all.
//
// The cost is one extra open connection for a few seconds per reconnect, which
// is under 1% of a session, and the audio it produces was going to be paid for
// either way — it was already generated before the swap.
func (t *Translator) watchRetirement(ctx context.Context, previous *sessionHolder) {
	grace := RetireGrace
	t.retireWG.Add(1)
	go func() {
		defer t.retireWG.Done()
		select {
		case <-time.After(grace):
			t.retireIf(previous, "the replacement never spoke")
		case <-previous.closed:
			// Already closed, by the audio arriving or by shutdown.
		case <-ctx.Done():
			t.retireIf(previous, "the translator is stopping")
		}
	}()
}

// retireAny closes whatever session is being held back, if any.
func (t *Translator) retireAny(reason string) {
	t.mu.Lock()
	holder := t.retiring
	t.retiring = nil
	t.mu.Unlock()
	if holder != nil {
		slog.Info("closing the replaced session", "reason", reason)
		t.closeSession(holder)
	}
}

// retireIf closes the held-back session only if it is still the one expected.
//
// The watchdog fires on a timer, and by then the session it was watching may
// already have been closed and a different one held back in its place. Closing
// blind would take out the wrong one.
func (t *Translator) retireIf(expected *sessionHolder, reason string) {
	t.mu.Lock()
	if t.retiring != expected {
		t.mu.Unlock()
		return
	}
	t.retiring = nil
	t.mu.Unlock()
	slog.Info("closing the replaced session", "reason", reason)
	t.closeSession(expected)
}

// isActive reports whether this holder is the one being sent audio.
func (t *Translator) isActive(holder *sessionHolder) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active == holder
}

// noteHandle keeps the newest resumption handle, unless it came from a session
// that has already been replaced.
//
// The gate is the whole point of the method. A retired session goes on
// receiving for as long as it is held open, resumption updates included, and
// letting it write here would hand the next reconnect a handle for a session
// that is being closed.
//
// The test is "has this been replaced", not "is this the active one". They are
// not the same: a session's receive loop starts before the supervisor adopts
// it as active, and its first resumption update can arrive in that window. That
// version of the gate dropped the first handle of every run — harmless in
// practice, since updates arrive about once a second, but wrong, and
// TestGoAwaySwapsTheSessionAndResumes caught it immediately.
func (t *Translator) noteHandle(holder *sessionHolder, handle string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.retiring == holder {
		return
	}
	t.handle = handle
}

func (t *Translator) closeSession(holder *sessionHolder) {
	if session := holder.live(); session != nil {
		_ = session.Close()
	}
	holder.markClosed()
}

// discard retires a holder the supervisor never adopted. A session that opens
// after this closes itself; one that opened just before is closed here.
func (t *Translator) discard(holder *sessionHolder) {
	if session := holder.abandon(); session != nil {
		slog.Warn("closing a session the supervisor had already given up on")
		_ = session.Close()
	}
	holder.markClosed()
}

// --- sender ---------------------------------------------------------------

// sendLoop drains the ring buffer into the active session. With no session,
// audio just accumulates — that is what survives a reconnect.
func (t *Translator) sendLoop(ctx context.Context) {
	var silentSince time.Time
	warnedBacklog := false
	// carried is the chunk a failed send was holding. It is kept rather than
	// dropped: it had already left the ring, so letting it go lost 100 ms of
	// speech every time a session ended uncleanly, while the log said the send
	// would be retried.
	var carried []byte
	// oneChunk is the no-filter fast path's reusable one-element slice.
	oneChunk := make([][]byte, 1)

	for ctx.Err() == nil {
		t.mu.Lock()
		holder := t.active
		warming := t.warming
		started := t.stats.StartedAt
		t.mu.Unlock()
		var warmSession liveSession
		if warming != nil && !warming.isClosed() {
			warmSession = warming.live()
		}

		session := liveSession(nil)
		if holder != nil {
			session = holder.live()
		}
		if holder == nil || session == nil || holder.isClosed() {
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

		chunk := carried
		carried = nil
		if chunk == nil {
			var err error
			chunk, err = t.ring.Read(audio.ChunkBytes)
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
		}

		// The music gate runs before the silence gate: it must hear every
		// chunk (silence included, or it never notices a song ending), and it
		// may return several chunks when a reopening flushes its pre-roll.
		released := oneChunk
		released[0] = chunk
		if t.filter != nil {
			released = t.filter(chunk)
		}

	sendReleased:
		for i, out := range released {
			if t.silenceGate {
				now := time.Now()
				if rms(out) < silenceRMSThreshold {
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

			if err := session.SendAudio(out); err != nil {
				slog.Warn("send failed, will retry on the next session", "err", err)
				carried = out // the retry the message promises
				if rest := len(released) - i - 1; rest > 0 {
					// A flush burst died mid-way. Only one chunk can be
					// carried; the rest of the pre-roll is gone, and saying
					// so beats a transcript hole with no explanation.
					slog.Warn("dropping the rest of a flushed backlog", "chunks", rest)
				}
				holder.markClosed()
				if !sleepCtx(ctx, idlePoll) {
					return
				}
				break sendReleased
			}
			t.mu.Lock()
			t.stats.ChunksSent++
			t.mu.Unlock()

			// The replacement hears exactly what the active session hears,
			// after the same gates. A failure here costs only the warm-up:
			// handOver sees it closed and falls back to a cold swap.
			if warmSession != nil {
				if err := warmSession.SendAudio(out); err != nil {
					slog.Warn("send to the warming replacement failed", "err", err)
					warming.markClosed()
					warmSession = nil
				}
			}
		}
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

// waitForSwapWindow holds a reconnect back until the swap would be inaudible.
//
// The server ends a session about every nine minutes and warns 50 s ahead. The
// swap itself needs a fraction of that, so the remainder is time the server
// gave us to choose a moment — and until now it was thrown away: the
// replacement opened within 300 ms of the warning, every time, whatever was
// being said.
//
// A swap made while the music gate is closed costs the listener nothing. No
// audio is going out, so none is coming back, and the crossover that would
// otherwise interrupt a sentence interrupts a silence instead.
//
// Four things end the wait, and three of them are refusals:
//
//   - the quiet moment arrives — the point of the exercise;
//   - the reserve runs out, so the swap happens where it always did;
//   - the session dies early, which the supervisor must handle immediately;
//   - the run is stopping.
//
// It can only choose *when* a swap happens, never whether. A session is
// replaced on the server's schedule either way.
func (t *Translator) waitForSwapWindow(ctx context.Context, holder *sessionHolder) {
	if !t.adaptivePrep || t.quietMoment == nil || !holder.sawGoAway() {
		return
	}
	// Already quiet: take it without burning a poll interval.
	if t.quietMoment() {
		t.notePreppedSwap(0)
		return
	}
	budget := holder.graceLeft() - PrepReserve
	if budget <= 0 {
		// Either the server did not say how long it was giving us, or it gave
		// less than the swap needs. Neither is a case for gambling.
		return
	}
	slog.Info("waiting for a quiet moment to swap sessions", "budget", budget)

	started := time.Now()
	deadline := time.After(budget)
	ticker := time.NewTicker(prepPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-holder.closed:
			slog.Info("the session ended before a quiet moment came")
			return
		case <-deadline:
			slog.Info("no quiet moment came — swapping anyway",
				"waited", time.Since(started).Round(time.Millisecond))
			t.noteUnpreppedSwap()
			return
		case <-ticker.C:
			if t.quietMoment() {
				waited := time.Since(started)
				slog.Info("quiet moment — swapping now",
					"waited", waited.Round(time.Millisecond))
				t.notePreppedSwap(waited)
				return
			}
		}
	}
}

// notePreppedSwap records a swap that landed in a quiet window, and how long
// it waited for one. Counted because "is this feature doing anything" is
// otherwise unanswerable from a log.
func (t *Translator) notePreppedSwap(waited time.Duration) {
	t.mu.Lock()
	t.stats.PreppedSwaps++
	t.stats.PrepWaitsMS = append(t.stats.PrepWaitsMS,
		float64(waited.Microseconds())/1000.0)
	t.mu.Unlock()
}

func (t *Translator) noteUnpreppedSwap() {
	t.mu.Lock()
	t.stats.UnpreppedSwaps++
	t.mu.Unlock()
}
