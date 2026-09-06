package main

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/genai"
)

// bisectConfig finds which LiveConnectConfig field the server rejects, by
// connecting with progressively richer configs and reading the setup reply.
// The Python build sends all of these happily, so a failure here is a
// difference in the Go SDK's converters, not in what the model supports.
func bisectConfig(ctx context.Context, client *genai.Client, audio []byte) {
	echo := false

	variants := []struct {
		name string
		cfg  *genai.LiveConnectConfig
	}{
		{"modalities only", &genai.LiveConnectConfig{
			ResponseModalities: []genai.Modality{genai.ModalityAudio},
		}},
		{"+ output transcription", &genai.LiveConnectConfig{
			ResponseModalities:       []genai.Modality{genai.ModalityAudio},
			OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
		}},
		{"+ input transcription", &genai.LiveConnectConfig{
			ResponseModalities:       []genai.Modality{genai.ModalityAudio},
			OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
			InputAudioTranscription:  &genai.AudioTranscriptionConfig{},
		}},
		{"+ translation", &genai.LiveConnectConfig{
			ResponseModalities:       []genai.Modality{genai.ModalityAudio},
			OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
			InputAudioTranscription:  &genai.AudioTranscriptionConfig{},
			TranslationConfig: &genai.TranslationConfig{
				TargetLanguageCode: bisectTarget,
				EchoTargetLanguage: &echo,
			},
		}},
		{"+ context compression", &genai.LiveConnectConfig{
			ResponseModalities:       []genai.Modality{genai.ModalityAudio},
			OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
			InputAudioTranscription:  &genai.AudioTranscriptionConfig{},
			TranslationConfig: &genai.TranslationConfig{
				TargetLanguageCode: bisectTarget,
				EchoTargetLanguage: &echo,
			},
			ContextWindowCompression: &genai.ContextWindowCompressionConfig{
				SlidingWindow: &genai.SlidingWindow{},
			},
		}},
		{"+ session resumption", &genai.LiveConnectConfig{
			ResponseModalities:       []genai.Modality{genai.ModalityAudio},
			OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
			InputAudioTranscription:  &genai.AudioTranscriptionConfig{},
			TranslationConfig: &genai.TranslationConfig{
				TargetLanguageCode: bisectTarget,
				EchoTargetLanguage: &echo,
			},
			ContextWindowCompression: &genai.ContextWindowCompressionConfig{
				SlidingWindow: &genai.SlidingWindow{},
			},
			SessionResumption: &genai.SessionResumptionConfig{},
		}},
	}

	for _, v := range variants {
		fmt.Printf("  %-24s %s\n", v.name, probeSetup(ctx, client, v.cfg, audio))
	}
}

// probeSetup opens a session, sends two seconds of real audio and then keeps
// reading until the server either answers or hangs up. Reading only the first
// message is not enough: setupComplete arrives before the server has looked at
// any audio, so a rejected audio message would go unseen.
func probeSetup(ctx context.Context, client *genai.Client, cfg *genai.LiveConnectConfig, audio []byte) string {
	session, err := client.Live.Connect(ctx, model, cfg)
	if err != nil {
		return "CONNECT FAILED: " + err.Error()
	}
	defer session.Close()

	messages := make(chan error, 1)
	count := 0
	go func() {
		for {
			msg, err := session.Receive()
			if err != nil {
				messages <- err
				return
			}
			count++
			if msg.ServerContent != nil && msg.ServerContent.OutputTranscription != nil {
				messages <- nil
				return
			}
		}
	}()

	for i := 0; i+chunkSize <= len(audio) && i < 20*chunkSize; i += chunkSize {
		if err := session.SendRealtimeInput(genai.LiveRealtimeInput{
			Audio: &genai.Blob{Data: audio[i : i+chunkSize], MIMEType: "audio/pcm;rate=16000"},
		}); err != nil {
			return "SEND FAILED: " + err.Error()
		}
		time.Sleep(chunkMS * time.Millisecond)
	}

	select {
	case err := <-messages:
		if err != nil {
			return fmt.Sprintf("REJECTED after %d messages: %v", count, err)
		}
		return fmt.Sprintf("ok — transcription after %d messages", count)
	case <-time.After(6 * time.Second):
		return fmt.Sprintf("ok — %d messages, no transcription yet", count)
	}
}

// bisectTarget is a bare language code on purpose. With "en-US" here the
// server rejects every variant from "+ translation" onwards, so the bisector
// blames the translation config whatever the real cause is — the one thing a
// bisector must never do.
const bisectTarget = "en"
