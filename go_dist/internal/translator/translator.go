// Package translator drives the Gemini Live API session.
//
// Port of the Python src/livecap/translator.go. An audio session lasts about
// 15 minutes and the WebSocket about 10, so surviving 30 minutes needs all
// three of:
//
//  1. ContextWindowCompression with a sliding window — extend the session itself
//  2. SessionResumption — keep and reuse the handle
//  3. open a replacement session when GoAway arrives, before closing the old one
//
// The ring buffer keeps filling during a reconnect and whatever accumulated is
// pushed to the new session, so no sentence is lost.
package translator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"

	"github.com/dennis2lee/live-translation/go_dist/internal/audio"
)

// --- Preview model contract. Change these here and nowhere else. -----------

const (
	Model     = "gemini-3.5-live-translate-preview"
	AudioMIME = "audio/pcm;rate=16000"
)

// ResponseModalities must stay AUDIO. This model has no text output; the text
// on screen comes only from OutputAudioTranscription.
var ResponseModalities = []genai.Modality{genai.ModalityAudio}

var (
	backoff                = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	maxConsecutiveFailures = 5

	idlePoll             = 20 * time.Millisecond
	silenceRMSThreshold  = 0.003
	silenceHoldSeconds   = 3.0
	sessionOpenTimeout   = 30 * time.Second
	shutdownGraceTimeout = 6 * time.Second
)

// fatalErrors are failures that retrying cannot fix. Report them immediately
// instead of backing off.
var fatalErrors = []struct{ marker, message string }{
	{"credits are depleted", "Billing credits exhausted — set up billing in AI Studio"},
	{"billing", "Billing required — this model is not on the free tier"},
	{"api key not valid", "API key is not valid"},
	{"api_key_invalid", "API key is not valid"},
	{"unauthenticated", "API key authentication failed"},
	{"permission_denied", "No permission to access this model"},
	{"was not found", "Model not found — check the model name"},
}

// FatalReason returns the message to show for a non-retryable error, else "".
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

// State is the connection state reported to the UI.
type State string

const (
	StateIdle         State = "idle"
	StateConnecting   State = "connecting"
	StateRunning      State = "running"
	StateReconnecting State = "reconnecting"
	StateError        State = "error"
)

// Stats records what happened during a run.
type Stats struct {
	Reconnects int
	GapsMS     []float64
	ChunksSent int
	StartedAt  time.Time
}

// Summary renders the stats for a log line.
func (s Stats) Summary() string {
	gaps := make([]string, 0, len(s.GapsMS))
	for _, g := range s.GapsMS {
		gaps = append(gaps, fmt.Sprintf("%.0f", g))
	}
	var elapsed time.Duration
	if !s.StartedAt.IsZero() {
		elapsed = time.Since(s.StartedAt)
	}
	joined := strings.Join(gaps, ", ")
	if joined == "" {
		joined = "-"
	}
	return fmt.Sprintf("elapsed=%.1fmin reconnects=%d gaps_ms=[%s] chunks_sent=%d",
		elapsed.Minutes(), s.Reconnects, joined, s.ChunksSent)
}

// Callbacks receive events on the translator's own goroutines. The UI layer
// must marshal them onto its own thread rather than touching widgets here.
type Callbacks struct {
	OnSource    func(string)
	OnTarget    func(string)
	OnState     func(State, string)
	OnReconnect func()
}

// Options configure a Translator.
type Options struct {
	APIKey             string
	TargetLanguageCode string
	EchoTargetLanguage bool
	SilenceGate        bool
	Callbacks          Callbacks
}

// sessionHolder is one Live session bundled with its receive goroutine.
type sessionHolder struct {
	session *genai.Session

	ready   chan struct{}
	closed  chan struct{}
	goAway  chan struct{}
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
	once    sync.Once
	awayOne sync.Once
}

func newHolder() *sessionHolder {
	return &sessionHolder{
		ready:  make(chan struct{}),
		closed: make(chan struct{}),
		goAway: make(chan struct{}),
		done:   make(chan struct{}),
	}
}

func (h *sessionHolder) markClosed() { h.once.Do(func() { close(h.closed) }) }
func (h *sessionHolder) markGoAway() { h.awayOne.Do(func() { close(h.goAway) }) }

// Translator pushes PCM from the ring buffer to the Live API and emits
// transcriptions through its callbacks.
type Translator struct {
	ring *audio.RingBuffer
	opts Options

	mu      sync.Mutex
	active  *sessionHolder
	handle  string
	lastErr error
	state   State
	stats   Stats
	cancel  context.CancelFunc
	stopped chan struct{}
	running bool
}

// New returns a translator that is not yet started.
func New(ring *audio.RingBuffer, opts Options) *Translator {
	if opts.TargetLanguageCode == "" {
		opts.TargetLanguageCode = "en"
	}
	return &Translator{ring: ring, opts: opts, state: StateIdle}
}

// State reports the current connection state.
func (t *Translator) State() State {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// Stats returns a copy of the run statistics.
func (t *Translator) Stats() Stats {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.stats
	s.GapsMS = append([]float64(nil), t.stats.GapsMS...)
	return s
}

// Start launches the supervisor. It returns immediately.
func (t *Translator) Start() error {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return errors.New("translator already started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.stopped = make(chan struct{})
	t.running = true
	t.stats = Stats{StartedAt: time.Now()}
	stopped := t.stopped
	t.mu.Unlock()

	go func() {
		defer close(stopped)
		t.supervise(ctx)
	}()
	return nil
}

// Stop cancels the supervisor and waits for it to unwind.
func (t *Translator) Stop() {
	t.mu.Lock()
	if !t.running {
		t.mu.Unlock()
		return
	}
	cancel, stopped := t.cancel, t.stopped
	t.running = false
	t.mu.Unlock()

	cancel()
	select {
	case <-stopped:
	case <-time.After(shutdownGraceTimeout):
		slog.Error("translator did not exit within the grace period")
	}
	t.setState(StateIdle, "")
	slog.Info("translator stopped", "stats", t.Stats().Summary())
}

func (t *Translator) setState(s State, detail string) {
	t.mu.Lock()
	changed := s != t.state || detail != ""
	t.state = s
	cb := t.opts.Callbacks.OnState
	t.mu.Unlock()
	if changed && cb != nil {
		cb(s, detail)
	}
}

func (t *Translator) config() *genai.LiveConnectConfig {
	t.mu.Lock()
	handle := t.handle
	t.mu.Unlock()
	echo := t.opts.EchoTargetLanguage
	return &genai.LiveConnectConfig{
		ResponseModalities:       ResponseModalities,
		InputAudioTranscription:  &genai.AudioTranscriptionConfig{},
		OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
		TranslationConfig: &genai.TranslationConfig{
			TargetLanguageCode: t.opts.TargetLanguageCode,
			EchoTargetLanguage: &echo,
		},
		ContextWindowCompression: &genai.ContextWindowCompressionConfig{
			SlidingWindow: &genai.SlidingWindow{},
		},
		SessionResumption: &genai.SessionResumptionConfig{Handle: handle},
	}
}

// supervise opens sessions, replaces them on GoAway or drop, and keeps the
// sender alive across the swaps.
func (t *Translator) supervise(ctx context.Context) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  t.opts.APIKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		t.setState(StateError, "Could not create the API client")
		slog.Error("client creation failed", "err", err)
		return
	}

	senderCtx, stopSender := context.WithCancel(ctx)
	go t.sendLoop(senderCtx)
	defer stopSender()

	failures := 0
	defer func() {
		if h := t.swapActive(nil); h != nil {
			t.closeSession(h)
		}
	}()

	for ctx.Err() == nil {
		// Having a session to replace means this is a reconnect. Using
		// stats.Reconnects would mislabel the first one, since it is still 0.
		previous := t.currentActive()
		if previous != nil {
			t.setState(StateReconnecting, "")
		} else {
			t.setState(StateConnecting, "")
		}

		gapStart := time.Now()
		holder := t.openSession(ctx, client)
		if holder == nil {
			if reason := FatalReason(t.takeLastErr()); reason != "" {
				// Credit, auth and permission problems do not heal with time.
				t.setState(StateError, reason)
				return
			}
			failures++
			if failures >= maxConsecutiveFailures {
				t.setState(StateError, "Repeated connection failures")
				return
			}
			d := backoff[min(failures-1, len(backoff)-1)]
			slog.Warn("connect failed", "attempt", failures, "retryIn", d)
			select {
			case <-ctx.Done():
				return
			case <-time.After(d):
			}
			continue
		}
		failures = 0

		t.swapActive(holder) // atomically swap the send target
		if previous != nil {
			gap := float64(time.Since(gapStart).Microseconds()) / 1000.0
			t.mu.Lock()
			t.stats.Reconnects++
			t.stats.GapsMS = append(t.stats.GapsMS, gap)
			n := t.stats.Reconnects
			cb := t.opts.Callbacks.OnReconnect
			t.mu.Unlock()
			slog.Info("session swapped", "gapMS", gap, "reconnect", n)
			if cb != nil {
				cb()
			}
			t.closeSession(previous)
		}
		t.setState(StateRunning, "")

		// Keep translating on this session until it warns or drops. The old
		// session stays alive past GoAway, until the replacement is ready.
		select {
		case <-ctx.Done():
			return
		case <-holder.goAway:
		case <-holder.closed:
		}
	}
}

func (t *Translator) currentActive() *sessionHolder {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active
}

func (t *Translator) swapActive(h *sessionHolder) *sessionHolder {
	t.mu.Lock()
	defer t.mu.Unlock()
	prev := t.active
	t.active = h
	return prev
}

func (t *Translator) takeLastErr() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastErr
}

// openSession opens a session and returns its holder once it is ready.
func (t *Translator) openSession(ctx context.Context, client *genai.Client) *sessionHolder {
	h := newHolder()
	sessCtx, cancel := context.WithCancel(ctx)
	h.cancel = cancel

	go func() {
		defer close(h.done)
		defer h.markClosed()
		session, err := client.Live.Connect(sessCtx, Model, t.config())
		if err != nil {
			h.err = err
			slog.Warn("session open failed", "err", err)
			return
		}
		defer session.Close()
		h.session = session
		close(h.ready)
		slog.Info("live session open", "resumed", t.hasHandle())
		t.receiveLoop(sessCtx, session, h)
	}()

	select {
	case <-h.ready:
		t.mu.Lock()
		t.lastErr = nil
		t.mu.Unlock()
		return h
	case <-h.done:
	case <-ctx.Done():
	case <-time.After(sessionOpenTimeout):
		slog.Warn("session open timed out")
	}
	cancel()
	t.mu.Lock()
	t.lastErr = h.err
	t.mu.Unlock()
	return nil
}

func (t *Translator) hasHandle() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.handle != ""
}

func (t *Translator) receiveLoop(ctx context.Context, session *genai.Session, h *sessionHolder) {
	for ctx.Err() == nil {
		msg, err := session.Receive()
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("session ended", "err", err)
				h.err = err
			}
			return
		}
		if msg == nil {
			return
		}

		// 1) Shutdown warning — triggers the pre-emptive reconnect.
		// Returning here would close this session at once; we must keep
		// receiving until the swap completes, so only raise a flag.
		if msg.GoAway != nil {
			slog.Info("go_away received", "timeLeft", msg.GoAway.TimeLeft)
			h.markGoAway()
		}

		// 2) Resumption handle — always keep the newest.
		if u := msg.SessionResumptionUpdate; u != nil && u.Resumable && u.NewHandle != "" {
			t.mu.Lock()
			t.handle = u.NewHandle
			t.mu.Unlock()
		}

		sc := msg.ServerContent
		if sc == nil {
			continue
		}
		// 3) Source-language transcription (hidden by default).
		if sc.InputTranscription != nil && sc.InputTranscription.Text != "" {
			if cb := t.opts.Callbacks.OnSource; cb != nil {
				cb(sc.InputTranscription.Text)
			}
		}
		// 4) The translation — this is what goes on screen.
		if sc.OutputTranscription != nil && sc.OutputTranscription.Text != "" {
			if cb := t.opts.Callbacks.OnTarget; cb != nil {
				cb(sc.OutputTranscription.Text)
			}
		}
		// 5) Audio must be consumed and discarded, or the stream stalls.
		// Receive() already drained it; nothing further to do.
	}
}

func (t *Translator) closeSession(h *sessionHolder) {
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
		slog.Warn("session did not close promptly")
	}
	h.markClosed()
}

// sendLoop moves audio from the ring buffer to the active session. With no
// session, audio just accumulates in the buffer.
func (t *Translator) sendLoop(ctx context.Context) {
	var silentSince time.Time
	for ctx.Err() == nil {
		h := t.currentActive()
		if h == nil || h.session == nil {
			// Reconnecting. The buffer keeps filling; we drain it once a session opens.
			sleep(ctx, idlePoll)
			continue
		}
		select {
		case <-h.closed:
			sleep(ctx, idlePoll)
			continue
		default:
		}

		chunk, err := t.ring.Read(audio.ChunkBytes)
		if err != nil || chunk == nil {
			sleep(ctx, idlePoll)
			continue
		}

		if t.opts.SilenceGate {
			if rms(chunk) < silenceRMSThreshold {
				if silentSince.IsZero() {
					silentSince = time.Now()
				}
				if time.Since(silentSince).Seconds() >= silenceHoldSeconds {
					continue // skip the send to save cost
				}
			} else {
				silentSince = time.Time{}
			}
		}

		if err := h.session.SendRealtimeInput(genai.LiveRealtimeInput{
			Media: &genai.Blob{Data: chunk, MIMEType: AudioMIME},
		}); err != nil {
			slog.Warn("send failed, will retry on the next session", "err", err)
			h.markClosed()
			sleep(ctx, idlePoll)
			continue
		}
		t.mu.Lock()
		t.stats.ChunksSent++
		t.mu.Unlock()
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func rms(pcm []byte) float64 {
	n := len(pcm) / 2
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		v := float64(int16(uint16(pcm[i*2])|uint16(pcm[i*2+1])<<8)) / 32768.0
		sum += v * v
	}
	return sqrt(sum / float64(n))
}

func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 12; i++ {
		z = 0.5 * (z + x/z)
	}
	return z
}
