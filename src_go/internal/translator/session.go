package translator

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"sync"
	"time"

	"google.golang.org/genai"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
)

// --- Preview model contract. Change these here and nowhere else. -----------

const (
	// Model is the only model this application talks to.
	Model = "gemini-3.5-live-translate-preview"
	// AudioMIME describes the chunks sent upstream.
	AudioMIME = "audio/pcm;rate=16000"
)

// ResponseModalities is audio-only. This model has no text output; the captions
// come from OutputAudioTranscription. Setting TEXT fails the session.
var ResponseModalities = []genai.Modality{genai.ModalityAudio}

// Message is one server message, reduced to the parts this application acts on.
// Normalising here is what lets the tests drive a fake session without building
// SDK types.
type Message struct {
	// GoAway is the shutdown warning that triggers a pre-emptive reconnect.
	GoAway     bool
	GoAwayLeft time.Duration

	// ResumeHandle is a new resumption handle, when the server offered one.
	ResumeHandle string

	// SourceText is the transcription of what was heard.
	SourceText string
	// TargetText is the translation. This is what goes on screen.
	TargetText string
	// TurnComplete is the end-of-turn boundary — the only one the API reports
	// reliably. Transcription.Finished exists in the SDK but does not arrive in
	// practice, so never wait on it.
	TurnComplete bool
	// Audio is the translated speech, 24 kHz mono PCM16 (audio.TranslatedRate).
	//
	// It must be read whether or not anyone is listening — failing to consume
	// it stalls the stream — so this is nil only when the message carried none.
	// Until 1.2.0 it was an int counting bytes that went straight in the bin.
	Audio []byte
}

// liveSession is one open connection. The genai implementation is below; the
// tests supply a fake.
type liveSession interface {
	// SendAudio pushes one PCM chunk upstream.
	SendAudio(chunk []byte) error
	// Receive blocks until the next message, or fails when the session ends.
	Receive() (Message, error)
	// Close ends the session. Safe to call more than once.
	Close() error
}

// sessionOpener opens a session. handle is the resumption handle to offer, or
// "" for a fresh session.
type sessionOpener func(ctx context.Context, handle string) (liveSession, error)

// genaiSession adapts the SDK's session to liveSession.
type genaiSession struct {
	session *genai.Session
}

func (g *genaiSession) SendAudio(chunk []byte) error {
	return g.session.SendRealtimeInput(genai.LiveRealtimeInput{
		Audio: &genai.Blob{Data: chunk, MIMEType: AudioMIME},
	})
}

func (g *genaiSession) Receive() (Message, error) {
	raw, err := g.session.Receive()
	if err != nil {
		return Message{}, err
	}
	return newMessage(raw), nil
}

// newMessage reduces one server message to the parts this application acts on.
//
// Pure, and separate from Receive, so the normalisation can be tested against
// hand-built SDK values — there is no way to fake a *genai.Session, and the
// copying below is worth a test of its own.
func newMessage(raw *genai.LiveServerMessage) Message {
	var msg Message
	if raw == nil {
		return msg
	}
	if raw.GoAway != nil {
		msg.GoAway = true
		msg.GoAwayLeft = raw.GoAway.TimeLeft
	}
	if upd := raw.SessionResumptionUpdate; upd != nil && upd.Resumable && upd.NewHandle != "" {
		msg.ResumeHandle = upd.NewHandle
	}
	if sc := raw.ServerContent; sc != nil {
		if sc.InputTranscription != nil {
			msg.SourceText = sc.InputTranscription.Text
		}
		if sc.OutputTranscription != nil {
			msg.TargetText = sc.OutputTranscription.Text
		}
		msg.TurnComplete = sc.TurnComplete
		if sc.ModelTurn != nil {
			for _, part := range sc.ModelTurn.Parts {
				if part.InlineData == nil {
					continue
				}
				checkAudioRate(part.InlineData.MIMEType)
				// Copied, not referenced. The buffer belongs to the SDK, and
				// whether it is reused between messages is not documented —
				// the failure that would cause is audio that is subtly wrong
				// only under load, which is the worst kind to go looking for.
				msg.Audio = append(msg.Audio, part.InlineData.Data...)
			}
		}
	}
	return msg
}

func (g *genaiSession) Close() error { return g.session.Close() }

// audioRateRE reads the rate out of "audio/pcm;rate=24000".
var audioRateRE = regexp.MustCompile(`rate=(\d+)`)

// warnedRates keeps the complaint below to one line per distinct rate. Blobs
// arrive ten times a second; a plain warning would be ten thousand lines an
// hour.
var warnedRates sync.Map

// checkAudioRate says something when the server stops sending what the player
// was built for.
//
// The rate is a measured constant, not a negotiated one: every blob of every
// run carried rate=24000. If a model revision changes it, the audio does not
// fail — it plays at the wrong pitch, which sounds like the model has developed
// a speech impediment. This turns that into a log line naming the real cause.
func checkAudioRate(mime string) {
	m := audioRateRE.FindStringSubmatch(mime)
	if m == nil {
		return
	}
	rate, err := strconv.Atoi(m[1])
	if err != nil || rate == audio.TranslatedRate {
		return
	}
	if _, seen := warnedRates.LoadOrStore(rate, true); seen {
		return
	}
	slog.Warn("the server is sending audio at an unexpected rate — it will play at the wrong pitch",
		"got", rate, "expected", audio.TranslatedRate, "mime", mime)
}

// newGenaiOpener builds the opener the application runs with.
func newGenaiOpener(apiKey string, cfg SessionConfig) (sessionOpener, error) {
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, handle string) (liveSession, error) {
		session, err := client.Live.Connect(ctx, Model, cfg.build(handle))
		if err != nil {
			return nil, err
		}
		return &genaiSession{session: session}, nil
	}, nil
}

// SessionConfig is what every session is opened with. Surviving 30 minutes
// needs all three of the endurance settings below; see the package comment.
type SessionConfig struct {
	// TargetLanguageCode must be a bare code — "en", not "en-US". The API
	// rejects the region-qualified form, and rejects it a second after
	// setupComplete so it looks like an SDK fault (spikes/RESULTS.md).
	TargetLanguageCode string
	EchoTargetLanguage bool

	// SourceTerms bias recognition of the language being spoken;
	// CaptionTerms bias how names come out in the captions. They must stay
	// separate — a Korean name hint must never reach the output side.
	SourceTerms  []string
	CaptionTerms []string
}

func (c SessionConfig) build(handle string) *genai.LiveConnectConfig {
	echo := c.EchoTargetLanguage
	target := c.TargetLanguageCode
	if target == "" {
		target = "en"
	}
	return &genai.LiveConnectConfig{
		ResponseModalities:       ResponseModalities,
		InputAudioTranscription:  transcription(c.SourceTerms),
		OutputAudioTranscription: transcription(c.CaptionTerms),
		TranslationConfig: &genai.TranslationConfig{
			TargetLanguageCode: target,
			EchoTargetLanguage: &echo,
		},
		// 1) extend the session itself
		ContextWindowCompression: &genai.ContextWindowCompressionConfig{
			SlidingWindow: &genai.SlidingWindow{},
		},
		// 2) keep and reuse the newest handle
		SessionResumption: &genai.SessionResumptionConfig{Handle: handle},
	}
}

// transcription builds the transcription config, with vocabulary hints when
// there are any.
func transcription(terms []string) *genai.AudioTranscriptionConfig {
	if len(terms) == 0 {
		return &genai.AudioTranscriptionConfig{}
	}
	// AdaptationPhrases is not used: the SDK marks it deprecated and describes
	// it identically to CustomVocabulary, so there is nothing it would add.
	return &genai.AudioTranscriptionConfig{CustomVocabulary: terms}
}
