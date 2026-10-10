package translator

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
	"github.com/dennis2lee/live-translation/src_go/internal/resample"
)

// --- OpenAI's contract. Change these here and nowhere else. ---------------

const (
	// ProviderOpenAI selects this file's session in Options.Provider; it
	// matches config.ProviderOpenAI.
	ProviderOpenAI = "openai"

	// OpenAIModel is OpenAI's dedicated speech-to-speech translation model.
	OpenAIModel = "gpt-realtime-translate"
	// OpenAITranscriptionModel transcribes the source side, for the source
	// captions. The translation side's transcript comes with the model.
	OpenAITranscriptionModel = "gpt-realtime-whisper"
	// openAISendRate is what the translations endpoint takes: 24 kHz mono
	// PCM16. The capture path produces 16 kHz for Gemini, so each session
	// converts.
	openAISendRate = 24000
	// openAIReceiveRate is what it sends back, which the documentation does
	// not state. Measured 2026-10-10: every delta was 19 200 bytes, the
	// "200 ms chunks" the guide mentions, and 146 of them arrived over 31 s
	// of a 28 s input — 48 kHz mono. The session converts to the 24 kHz the
	// player and the freeze gate are built for.
	openAIReceiveRate = 48000
)

// These are variables so the tests can point them at a local server and
// shorten the session's life. Nothing outside a test changes them.
var (
	OpenAIEndpoint = "wss://api.openai.com/v1/realtime/translations?model=" + OpenAIModel

	// openAISessionLife is when a session announces its own end. The
	// Realtime API documents a hard 60-minute limit that arrives without
	// warning, so the session raises the warning itself, leaving the
	// supervisor openAIGoAwayLeft to swap — the same path Gemini's go_away
	// takes. Not measured on the translations endpoint, which documents no
	// limit of its own.
	openAISessionLife = 50 * time.Minute
	openAIGoAwayLeft  = 10 * time.Minute
)

// openAISession adapts one translations WebSocket to liveSession.
//
// One goroutine reads the socket and hands messages to Receive over a
// channel, because the session must also be able to deliver its own warning
// while Receive is blocked waiting for the server.
type openAISession struct {
	conn *websocket.Conn

	wmu       sync.Mutex // gorilla allows one writer at a time
	resampler resample.Stream
	mono      []float32
	pcm       []byte

	// down converts what arrives to audio.TranslatedRate. Only the read
	// goroutine touches it, until Close.
	down resample.Stream

	msgs    chan openAIResult
	goAway  chan struct{}
	timer   *time.Timer
	closeMu sync.Once
	done    chan struct{}
}

type openAIResult struct {
	msg Message
	err error
}

// newOpenAIOpener builds the opener for OpenAI. The handle is ignored: the
// translations endpoint has no session resumption, so every session after a
// swap starts fresh — which costs nothing here, since a translation session
// carries no conversation to resume.
func newOpenAIOpener(apiKey string, cfg SessionConfig) (sessionOpener, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("translator: no API key")
	}
	if len(cfg.SourceTerms) > 0 || len(cfg.CaptionTerms) > 0 {
		slog.Info("vocabulary hints are not used: OpenAI's translation sessions take none")
	}
	return func(ctx context.Context, _ string) (liveSession, error) {
		return openOpenAI(ctx, apiKey, cfg)
	}, nil
}

func openOpenAI(ctx context.Context, apiKey string, cfg SessionConfig) (*openAISession, error) {
	hdr := http.Header{}
	// The key goes in a header, never the URL — the same rule as TestAPIKey,
	// for the same reason: a URL ends up in error text.
	hdr.Set("Authorization", "Bearer "+apiKey)
	dialer := websocket.Dialer{HandshakeTimeout: OpenTimeout}
	conn, resp, err := dialer.DialContext(ctx, OpenAIEndpoint, hdr)
	if err != nil {
		return nil, handshakeError(err, resp)
	}
	rs, err := resample.NewStream(audio.TargetRate, openAISendRate)
	if err != nil {
		conn.Close()
		return nil, err
	}
	down, err := resample.NewStream(openAIReceiveRate, audio.TranslatedRate)
	if err != nil {
		rs.Close()
		conn.Close()
		return nil, err
	}
	s := &openAISession{
		conn:      conn,
		resampler: rs,
		down:      down,
		msgs:      make(chan openAIResult, 64),
		goAway:    make(chan struct{}),
		done:      make(chan struct{}),
	}
	// The reader starts first, so a failed write below still leaves Close a
	// reader to end: its drain waits for msgs to close, and the reader is
	// what closes it and the down-sampler.
	go s.readLoop()
	if err := s.write(openAIConfig(cfg)); err != nil {
		s.Close()
		return nil, err
	}
	s.timer = time.AfterFunc(openAISessionLife, func() { close(s.goAway) })
	return s, nil
}

// handshakeError keeps what the server said when it refused the upgrade —
// gorilla's own error is only "bad handshake", which names no cause.
func handshakeError(err error, resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
	return fmt.Errorf("openai: handshake failed: %s: %s", resp.Status, redactKeys(strings.TrimSpace(string(body))))
}

// keyRE matches anything shaped like an OpenAI key, masked or not.
var keyRE = regexp.MustCompile(`sk-[A-Za-z0-9_*\-]+`)

// redactKeys strips keys out of text the server wrote. OpenAI's own errors
// repeat the key that was sent — "Incorrect API key provided: sk-…",
// measured — and these strings reach the log and the screen, neither of which
// may ever hold a key.
func redactKeys(text string) string { return keyRE.ReplaceAllString(text, "sk-[redacted]") }

// openAIConfig is the session.update every session opens with.
func openAIConfig(cfg SessionConfig) map[string]any {
	target := cfg.TargetLanguageCode
	if target == "" {
		target = "en"
	}
	return map[string]any{
		"type": "session.update",
		"session": map[string]any{
			"audio": map[string]any{
				"output": map[string]any{"language": target},
				"input": map[string]any{
					"transcription": map[string]any{"model": OpenAITranscriptionModel},
				},
			},
		},
	}
}

func (s *openAISession) write(v any) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.conn.WriteJSON(v)
}

// SendAudio converts one 16 kHz chunk to 24 kHz and sends it.
func (s *openAISession) SendAudio(chunk []byte) error {
	s.wmu.Lock()
	s.mono = s.mono[:0]
	for i := 0; i+1 < len(chunk); i += 2 {
		s.mono = append(s.mono, float32(int16(binary.LittleEndian.Uint16(chunk[i:])))/32768)
	}
	up, err := s.resampler.Process(s.mono)
	if err != nil {
		s.wmu.Unlock()
		return err
	}
	s.pcm = s.pcm[:0]
	for _, v := range up {
		x := math.Round(float64(v) * 32767)
		s.pcm = binary.LittleEndian.AppendUint16(s.pcm, uint16(int16(max(-32768, min(32767, x)))))
	}
	payload := base64.StdEncoding.EncodeToString(s.pcm)
	s.wmu.Unlock()
	if len(payload) == 0 {
		return nil
	}
	return s.write(map[string]string{"type": "session.input_audio_buffer.append", "audio": payload})
}

// Receive returns the next message the application acts on, or the session's
// own end-of-life warning.
func (s *openAISession) Receive() (Message, error) {
	select {
	case r, ok := <-s.msgs:
		if !ok {
			return Message{}, errors.New("openai: session closed")
		}
		return r.msg, r.err
	case <-s.goAway:
		// Delivered once: the nil channel blocks for ever after.
		s.goAway = nil
		return Message{GoAway: true, GoAwayLeft: openAIGoAwayLeft}, nil
	}
}

// readLoop hands every event worth acting on to Receive.
//
// An error event does not end the session. Measured: a new account's first
// session drew two rate_limit_exceeded errors in its first 0.6 s and then
// translated 28 s without fault. What does end it is the server closing the
// socket, and when an error event preceded that — insufficient_quota, say —
// its text is what names the cause, so it rides along with the read error
// for FatalReason to find.
func (s *openAISession) readLoop() {
	defer close(s.msgs)
	defer s.down.Close()
	var lastErr error
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			select {
			case <-s.done:
			default:
				text := err.Error()
				if lastErr != nil {
					text += " (after " + lastErr.Error() + ")"
				}
				// The close reason is the server's text too, and can carry
				// the key: "1008 … Incorrect API key provided: sk-…".
				s.msgs <- openAIResult{err: errors.New("openai: " + redactKeys(text))}
			}
			return
		}
		msg, ok, err := parseOpenAIEvent(data)
		if err != nil {
			lastErr = err
			slog.Warn("the translation service reported an error", "err", err)
			continue
		}
		if !ok {
			continue
		}
		if len(msg.Audio) > 0 {
			if msg.Audio, err = s.downsample(msg.Audio); err != nil {
				slog.Warn("could not convert the translated audio", "err", err)
				continue
			}
			if len(msg.Audio) == 0 {
				continue
			}
		}
		s.msgs <- openAIResult{msg: msg}
	}
}

// downsample converts one 48 kHz delta to 24 kHz PCM16.
func (s *openAISession) downsample(pcm []byte) ([]byte, error) {
	in := make([]float32, len(pcm)/2)
	for i := range in {
		in[i] = float32(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
	}
	out, err := s.down.Process(in)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 0, 2*len(out))
	for _, v := range out {
		x := math.Round(float64(v) * 32767)
		b = binary.LittleEndian.AppendUint16(b, uint16(int16(max(-32768, min(32767, x)))))
	}
	return b, nil
}

// Close ends the session. Safe to call more than once.
func (s *openAISession) Close() error {
	var err error
	s.closeMu.Do(func() {
		close(s.done)
		if s.timer != nil {
			s.timer.Stop()
		}
		s.wmu.Lock()
		_ = s.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		// Under the write lock, never outside it. SendAudio runs the
		// resampler holding this lock, and on macOS that is libsoxr: closing
		// it while a send was converting freed the handle under the C call,
		// a SIGSEGV that took the whole application down (found by the red
		// team, 100 runs). The supervisor closes sessions that sendLoop may
		// still be sending to — a warm switch, a server hang-up — so this
		// is reachable in ordinary use.
		s.resampler.Close()
		s.wmu.Unlock()
		err = s.conn.Close()
		// Drain, so a reader blocked on a full channel can see the closed
		// socket and leave.
		go func() {
			for range s.msgs {
			}
		}()
	})
	return err
}

// openAIEvent is the part of a server event this application reads.
type openAIEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
	Error *struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// parseOpenAIEvent reduces one server event to a Message. ok is false for
// events that carry nothing to act on. An error event comes back as err, with
// the server's type and code in the text so FatalReason can recognise them;
// whether it ends the session is the server's to decide, by closing.
//
// Pure, so the mapping is tested without a socket.
func parseOpenAIEvent(data []byte) (msg Message, ok bool, err error) {
	var ev openAIEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return Message{}, false, nil
	}
	switch ev.Type {
	case "session.output_audio.delta":
		b, err := base64.StdEncoding.DecodeString(ev.Delta)
		if err != nil || len(b) == 0 {
			return Message{}, false, nil
		}
		return Message{Audio: b}, true, nil
	case "session.output_transcript.delta":
		return Message{TargetText: ev.Delta}, ev.Delta != "", nil
	case "session.input_transcript.delta":
		return Message{SourceText: ev.Delta}, ev.Delta != "", nil
	case "error":
		if ev.Error == nil {
			return Message{}, false, errors.New("openai: error event with no detail")
		}
		return Message{}, false, fmt.Errorf("openai: %s: %s: %s",
			ev.Error.Type, ev.Error.Code, redactKeys(ev.Error.Message))
	case "session.closed":
		return Message{}, false, errors.New("openai: session closed by the server")
	}
	return Message{}, false, nil
}
