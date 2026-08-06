package translator

import (
	"context"
	"time"

	"google.golang.org/genai"
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
	// AudioBytes is how much audio arrived. It is counted and discarded:
	// failing to read it stalls the stream, and this application never plays it.
	AudioBytes int
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
	var msg Message
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
				if part.InlineData != nil {
					msg.AudioBytes += len(part.InlineData.Data)
				}
			}
		}
	}
	return msg, nil
}

func (g *genaiSession) Close() error { return g.session.Close() }

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
