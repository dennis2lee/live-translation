package translator

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
)

const fakeOpenAIKey = "sk-proj-test-key-not-real"

// fakeOpenAI is a translations endpoint on localhost. It records what the
// client sent and lets a test script what it sends back.
type fakeOpenAI struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	auth   string
	query  string
	events []map[string]any
	audio  []byte // decoded input audio
	conn   *websocket.Conn
	ready  chan struct{}
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	f := &fakeOpenAI{t: t, ready: make(chan struct{})}
	up := websocket.Upgrader{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = r.Header.Get("Authorization")
		f.query = r.URL.RawQuery
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+fakeOpenAIKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: ` +
				strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") +
				`","type":"invalid_request_error","code":"invalid_api_key"}}`))
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conn = c
		f.mu.Unlock()
		close(f.ready)
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			var ev map[string]any
			_ = json.Unmarshal(data, &ev)
			f.mu.Lock()
			f.events = append(f.events, ev)
			if ev["type"] == "session.input_audio_buffer.append" {
				b, _ := base64.StdEncoding.DecodeString(ev["audio"].(string))
				f.audio = append(f.audio, b...)
			}
			f.mu.Unlock()
		}
	}))
	t.Cleanup(f.srv.Close)
	old := OpenAIEndpoint
	OpenAIEndpoint = "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/v1/realtime/translations?model=" + OpenAIModel
	t.Cleanup(func() { OpenAIEndpoint = old })
	return f
}

func (f *fakeOpenAI) send(v any) {
	f.t.Helper()
	<-f.ready
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.conn.WriteJSON(v); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeOpenAI) hangUp(code int, reason string) {
	<-f.ready
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = f.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason))
	_ = f.conn.Close()
}

func openFake(t *testing.T, cfg SessionConfig) liveSession {
	t.Helper()
	open, err := newOpenAIOpener(fakeOpenAIKey, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s, err := open(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func tone(rate, n int, hz float64) []byte {
	b := make([]byte, 0, 2*n)
	for i := range n {
		v := 0.5 * math.Sin(2*math.Pi*hz*float64(i)/float64(rate))
		b = binary.LittleEndian.AppendUint16(b, uint16(int16(v*32767)))
	}
	return b
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The key goes in the Authorization header and nowhere else: never the URL,
// which ends up in error text.
func TestTheOpenAIKeyTravelsOnlyInTheHeader(t *testing.T) {
	f := newFakeOpenAI(t)
	openFake(t, SessionConfig{TargetLanguageCode: "en"})
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth != "Bearer "+fakeOpenAIKey {
		t.Errorf("Authorization = %q", f.auth)
	}
	if strings.Contains(f.query, "sk-") || f.query != "model="+OpenAIModel {
		t.Errorf("query = %q", f.query)
	}
}

// The session opens with the target language and asks for the source
// transcript, which is where the source captions come from.
func TestTheOpenAISessionAsksForItsLanguage(t *testing.T) {
	for _, target := range []string{"en", "ko"} {
		f := newFakeOpenAI(t)
		openFake(t, SessionConfig{TargetLanguageCode: target})
		waitFor(t, "session.update", func() bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			return len(f.events) > 0
		})
		f.mu.Lock()
		ev := f.events[0]
		f.mu.Unlock()
		raw, _ := json.Marshal(ev)
		if ev["type"] != "session.update" ||
			!strings.Contains(string(raw), `"output":{"language":"`+target+`"}`) ||
			!strings.Contains(string(raw), `"model":"`+OpenAITranscriptionModel+`"`) {
			t.Errorf("first event = %s", raw)
		}
	}
}

// Capture produces 16 kHz for Gemini; this endpoint takes 24 kHz. A second
// of audio must arrive as a second, at the new rate, at the same pitch.
func TestOpenAIAudioGoesUpAt24kHz(t *testing.T) {
	f := newFakeOpenAI(t)
	s := openFake(t, SessionConfig{})
	in := tone(audio.TargetRate, audio.TargetRate, 440)
	for i := 0; i < len(in); i += 3200 {
		if err := s.SendAudio(in[i:min(i+3200, len(in))]); err != nil {
			t.Fatal(err)
		}
	}
	// The converter keeps a little in its filter; most of a second is plenty
	// to tell 24 kHz from 16.
	waitFor(t, "the audio", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.audio) >= 2*openAISendRate*9/10
	})
	f.mu.Lock()
	got := append([]byte(nil), f.audio...)
	f.mu.Unlock()
	if n := len(got) / 2; n > openAISendRate {
		t.Errorf("%d samples for one second, want at most %d", n, openAISendRate)
	}
	if hz := zeroCrossingHz(got, openAISendRate); math.Abs(hz-440) > 5 {
		t.Errorf("the tone arrived at %.1f Hz, want 440", hz)
	}
}

func zeroCrossingHz(pcm []byte, rate int) float64 {
	n, crossings := len(pcm)/2, 0
	prev := int16(binary.LittleEndian.Uint16(pcm))
	start := n / 4 // past the converter's warm-up
	for i := start; i < n; i++ {
		v := int16(binary.LittleEndian.Uint16(pcm[2*i:]))
		if (prev < 0) != (v < 0) {
			crossings++
		}
		prev = v
	}
	return float64(crossings) / 2 / (float64(n-start) / float64(rate))
}

// The model sends 48 kHz (measured; the documentation does not say). The
// player is built for 24 kHz, so audio arriving at the wrong rate would play
// an octave low and twice as long.
func TestOpenAIAudioComesBackAt24kHz(t *testing.T) {
	f := newFakeOpenAI(t)
	s := openFake(t, SessionConfig{})
	in := tone(openAIReceiveRate, openAIReceiveRate, 300) // one second
	for i := 0; i < len(in); i += 19200 {
		f.send(map[string]any{"type": "session.output_audio.delta",
			"delta": base64.StdEncoding.EncodeToString(in[i:min(i+19200, len(in))])})
	}
	var got []byte
	for len(got) < 2*audio.TranslatedRate*9/10 {
		msg, err := s.Receive()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, msg.Audio...)
	}
	if n := len(got) / 2; n > audio.TranslatedRate {
		t.Errorf("%d samples for one second, want at most %d", n, audio.TranslatedRate)
	}
	if hz := zeroCrossingHz(got, audio.TranslatedRate); math.Abs(hz-300) > 5 {
		t.Errorf("the tone came back at %.1f Hz, want 300", hz)
	}
}

func TestOpenAITranscriptsReachTheirSides(t *testing.T) {
	f := newFakeOpenAI(t)
	s := openFake(t, SessionConfig{})
	f.send(map[string]any{"type": "session.input_transcript.delta", "delta": " 안녕하세요"})
	f.send(map[string]any{"type": "session.created"})
	f.send(map[string]any{"type": "session.output_transcript.delta", "delta": " Hello"})
	m1, err := s.Receive()
	if err != nil || m1.SourceText != " 안녕하세요" || m1.TargetText != "" {
		t.Errorf("first = %+v, %v", m1, err)
	}
	m2, err := s.Receive() // session.created carries nothing and is skipped
	if err != nil || m2.TargetText != " Hello" || m2.SourceText != "" {
		t.Errorf("second = %+v, %v", m2, err)
	}
}

// Measured: a new account's first session drew two rate_limit_exceeded errors
// and then translated without fault. An error event is not the end.
func TestAnOpenAIErrorEventDoesNotEndTheSession(t *testing.T) {
	f := newFakeOpenAI(t)
	s := openFake(t, SessionConfig{})
	f.send(map[string]any{"type": "error", "error": map[string]any{
		"type": "tokens", "code": "rate_limit_exceeded", "message": "Request too large"}})
	f.send(map[string]any{"type": "session.output_transcript.delta", "delta": " still here"})
	msg, err := s.Receive()
	if err != nil || msg.TargetText != " still here" {
		t.Errorf("after an error event: %+v, %v", msg, err)
	}
}

// When the server does hang up, the error event before it is what names the
// cause — and it must be one the supervisor gives up on, not retries.
func TestAnOpenAIQuotaHangUpIsFatal(t *testing.T) {
	f := newFakeOpenAI(t)
	s := openFake(t, SessionConfig{})
	f.send(map[string]any{"type": "error", "error": map[string]any{
		"type": "insufficient_quota", "code": "credit_balance_exhausted",
		"message": "You have no credits remaining. Add credits at https://platform.openai.com/settings/organization/billing/."}})
	f.hangUp(1013, "insufficient_quota.credit_balance_exhausted")
	_, err := s.Receive()
	if err == nil {
		t.Fatal("no error after the server hung up")
	}
	if got := FatalReason(err); !strings.Contains(got, "OpenAI credit") {
		t.Errorf("FatalReason = %q for %v", got, err)
	}
}

// OpenAI's error text repeats the key that was sent. None of it may reach an
// error, which reaches the log and the screen.
func TestARejectedOpenAIKeyIsNotRepeated(t *testing.T) {
	newFakeOpenAI(t)
	const wrong = "sk-proj-wrong-key-123"
	open, err := newOpenAIOpener(wrong, SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = open(context.Background(), "")
	if err == nil {
		t.Fatal("a wrong key opened a session")
	}
	if strings.Contains(err.Error(), "wrong-key") {
		t.Errorf("the error repeats the key: %v", err)
	}
	if got := FatalReason(err); got != "API key is not valid" {
		t.Errorf("FatalReason = %q for %v", got, err)
	}
}

func TestOpenAIErrorEventsAreRedacted(t *testing.T) {
	_, _, err := parseOpenAIEvent([]byte(`{"type":"error","error":{"type":"invalid_request_error",` +
		`"code":"invalid_api_key","message":"Incorrect API key provided: sk-proj-abc*****xyz."}}`))
	if err == nil || strings.Contains(err.Error(), "abc") || strings.Contains(err.Error(), "xyz") {
		t.Errorf("err = %v", err)
	}
}

// The Realtime API ends a session at 60 minutes without warning. The session
// warns itself, through the same path Gemini's go_away takes, so the
// supervisor swaps before the far end hangs up — once.
func TestAnOpenAISessionWarnsBeforeItsLimit(t *testing.T) {
	old := openAISessionLife
	openAISessionLife = 50 * time.Millisecond
	t.Cleanup(func() { openAISessionLife = old })
	f := newFakeOpenAI(t)
	s := openFake(t, SessionConfig{})
	msg, err := s.Receive()
	if err != nil || !msg.GoAway || msg.GoAwayLeft != openAIGoAwayLeft {
		t.Fatalf("first message = %+v, %v; want the session's own warning", msg, err)
	}
	f.send(map[string]any{"type": "session.output_transcript.delta", "delta": " after"})
	msg, err = s.Receive()
	if err != nil || msg.GoAway || msg.TargetText != " after" {
		t.Errorf("the warning repeated, or swallowed what followed: %+v, %v", msg, err)
	}
}

// Closing is safe twice, and a closed session's Receive returns rather than
// blocking the receive loop for ever.
func TestAClosedOpenAISessionStopsReceiving(t *testing.T) {
	newFakeOpenAI(t)
	s := openFake(t, SessionConfig{})
	if err := s.Close(); err != nil {
		t.Logf("first Close: %v", err)
	}
	_ = s.Close()
	done := make(chan struct{})
	go func() {
		_, _ = s.Receive()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Receive blocked after Close")
	}
}

func TestOpenAINeedsAKey(t *testing.T) {
	if _, err := newOpenAIOpener("  ", SessionConfig{}); err == nil {
		t.Error("an empty key built an opener")
	}
}

// New picks the opener from Provider.
func TestNewBuildsTheProvidersSession(t *testing.T) {
	f := newFakeOpenAI(t)
	tr, err := New(Options{Ring: audio.NewRing(4096), APIKey: fakeOpenAIKey, Provider: ProviderOpenAI})
	if err != nil {
		t.Fatal(err)
	}
	s, err := tr.open(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok := s.(*openAISession); !ok {
		t.Errorf("Provider %q built a %T", ProviderOpenAI, s)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth == "" {
		t.Error("the OpenAI endpoint was never reached")
	}
}

// The test above passes on the close reason alone, since the server repeats
// the code there. A hang-up that names nothing must still be explained by the
// error event before it.
func TestTheErrorBeforeAPlainHangUpNamesTheCause(t *testing.T) {
	f := newFakeOpenAI(t)
	s := openFake(t, SessionConfig{})
	f.send(map[string]any{"type": "error", "error": map[string]any{
		"type": "insufficient_quota", "code": "credit_balance_exhausted", "message": "No credit."}})
	f.hangUp(websocket.CloseInternalServerErr, "")
	_, err := s.Receive()
	if got := FatalReason(err); !strings.Contains(got, "OpenAI credit") {
		t.Errorf("FatalReason = %q for %v", got, err)
	}
}

// Closing a session while audio is being converted for it freed libsoxr's
// handle under the C call: a SIGSEGV, found by the red team in 100 runs. The
// supervisor does this in ordinary use — a warm switch, a server hang-up —
// because sendLoop may still hold the session it is closing. Run with -race,
// which reported the race every time.
func TestClosingWhileSendingIsSafe(t *testing.T) {
	chunk := tone(audio.TargetRate, audio.TargetRate/10, 440)
	for range 30 {
		newFakeOpenAI(t)
		s := openFake(t, SessionConfig{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s.SendAudio(chunk) == nil {
			}
		}()
		time.Sleep(2 * time.Millisecond)
		_ = s.Close()
		wg.Wait()
	}
}

// OpenAI's rate-limit message ends with a link to its billing page. Carried
// into the error of a session that then drops, it read as Gemini's
// free-tier refusal and stopped translating — the red team's reproduction,
// with the text recorded from a real session.
func TestARateLimitIsNotABillingProblem(t *testing.T) {
	const rateLimit = "openai: tokens: rate_limit_exceeded: Request too large for gpt-realtime-translate " +
		"in organization org-x on tokens per min (TPM): Limit 2, Requested 50. The input or output tokens " +
		"must be reduced in order to run successfully. Visit https://platform.openai.com/account/rate-limits " +
		"to learn more. You can increase your rate limit by adding a payment method to your account at " +
		"https://platform.openai.com/account/billing."
	drop := errors.New("openai: websocket: close 1006 (abnormal closure): unexpected EOF (after " + rateLimit + ")")
	if r := FatalReason(drop); r != "" {
		t.Errorf("a drop after a rate limit is reported fatal: %q", r)
	}
	// But a real quota failure arriving with one is still fatal.
	quota := errors.New("openai: websocket: close 1013: insufficient_quota.credit_balance_exhausted (after " + rateLimit + ")")
	if r := FatalReason(quota); !strings.Contains(r, "OpenAI credit") {
		t.Errorf("insufficient_quota beside a rate limit: %q", r)
	}
	// And Gemini's billing refusal is untouched.
	if r := FatalReason(errors.New("Billing account not enabled")); r == "" {
		t.Error("Gemini's billing error is no longer fatal")
	}
}

// The server's close reason is its text too, and can carry the key.
func TestACloseReasonIsRedacted(t *testing.T) {
	f := newFakeOpenAI(t)
	s := openFake(t, SessionConfig{})
	f.hangUp(websocket.ClosePolicyViolation, "Incorrect API key provided: sk-proj-abcdef123456.")
	_, err := s.Receive()
	if err == nil || strings.Contains(err.Error(), "abcdef") {
		t.Errorf("err = %v", err)
	}
}

// The key test speaks each provider's dialect: OpenAI's in a bearer header.
func TestTheOpenAIKeyTestUsesABearerHeader(t *testing.T) {
	var auth, goog string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, goog = r.Header.Get("Authorization"), r.Header.Get("x-goog-api-key")
	}))
	defer srv.Close()
	old := OpenAIKeyTestEndpoint
	OpenAIKeyTestEndpoint = srv.URL
	defer func() { OpenAIKeyTestEndpoint = old }()
	if err := TestKeyFor(ProviderOpenAI, "sk-test"); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer sk-test" || goog != "" {
		t.Errorf("Authorization %q, x-goog-api-key %q", auth, goog)
	}
}
