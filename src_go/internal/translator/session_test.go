package translator

import (
	"bytes"
	"testing"

	"google.golang.org/genai"
)

func audioMessage(blobs ...*genai.Blob) *genai.LiveServerMessage {
	parts := make([]*genai.Part, 0, len(blobs))
	for _, b := range blobs {
		parts = append(parts, &genai.Part{InlineData: b})
	}
	return &genai.LiveServerMessage{
		ServerContent: &genai.LiveServerContent{
			ModelTurn: &genai.Content{Parts: parts},
		},
	}
}

func TestNewMessageCarriesTheAudio(t *testing.T) {
	want := []byte{1, 2, 3, 4}
	msg := newMessage(audioMessage(&genai.Blob{
		Data:     want,
		MIMEType: "audio/pcm;rate=24000",
	}))
	if !bytes.Equal(msg.Audio, want) {
		t.Errorf("Audio = %v, want %v", msg.Audio, want)
	}
}

// One message can carry several blobs, and they are one stretch of speech.
func TestNewMessageConcatenatesEveryBlob(t *testing.T) {
	msg := newMessage(audioMessage(
		&genai.Blob{Data: []byte{1, 2}, MIMEType: "audio/pcm;rate=24000"},
		&genai.Blob{Data: []byte{3, 4}, MIMEType: "audio/pcm;rate=24000"},
	))
	if want := []byte{1, 2, 3, 4}; !bytes.Equal(msg.Audio, want) {
		t.Errorf("Audio = %v, want %v", msg.Audio, want)
	}
}

// The SDK owns the buffer it hands over, and whether it reuses it between
// messages is undocumented. Holding a reference would produce audio that is
// wrong only under load — so the bytes are copied, and this is what says so.
func TestReceivedAudioIsCopied(t *testing.T) {
	source := []byte{1, 2, 3, 4}
	msg := newMessage(audioMessage(&genai.Blob{
		Data:     source,
		MIMEType: "audio/pcm;rate=24000",
	}))

	// The SDK reuses its buffer for the next message.
	for i := range source {
		source[i] = 0xFF
	}

	if want := []byte{1, 2, 3, 4}; !bytes.Equal(msg.Audio, want) {
		t.Errorf("Audio = %v after the source buffer was reused, want %v", msg.Audio, want)
	}
}

func TestNewMessageWithoutAudioCarriesNone(t *testing.T) {
	msg := newMessage(&genai.LiveServerMessage{
		ServerContent: &genai.LiveServerContent{
			OutputTranscription: &genai.Transcription{Text: "Hello."},
			TurnComplete:        true,
		},
	})
	if msg.Audio != nil {
		t.Errorf("Audio = %v, want nil", msg.Audio)
	}
	if msg.TargetText != "Hello." || !msg.TurnComplete {
		t.Errorf("the rest of the message did not survive: %+v", msg)
	}
}

// checkAudioRate must not fire for what the server actually sends, or the log
// fills with a warning about the normal case.
func TestCheckAudioRateIsQuietForTheMeasuredRate(t *testing.T) {
	// A rate this application cannot play would be a real warning; 24000 is
	// what every blob of every measured run carried.
	warnedRates.Range(func(k, _ any) bool { warnedRates.Delete(k); return true })

	checkAudioRate("audio/pcm;rate=24000")
	if _, warned := warnedRates.Load(24000); warned {
		t.Error("warned about the expected rate")
	}

	checkAudioRate("audio/pcm;rate=16000")
	if _, warned := warnedRates.Load(16000); !warned {
		t.Error("did not warn about an unexpected rate")
	}
}
