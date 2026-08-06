// Spike L1 — does the Go SDK's Live surface actually work end to end?
//
// The shortest program that proves it: a 16 kHz mono WAV of Korean speech
// streamed in 100 ms chunks exactly the way the real sender will, with the
// English transcription fragments printed as they arrive. A fixture rather
// than the microphone, so the run is deterministic and repeatable.
//
// This costs money — about $0.037 a minute. Keep -seconds short.
//
//	go run ./spikes/l1_live -seconds 30
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
	"google.golang.org/genai"

	"github.com/dennis2lee/live-translation/src_go/internal/credentials"
)

const (
	model     = "gemini-3.5-live-translate-preview"
	chunkMS   = 100
	sendRate  = 16000
	chunkSize = sendRate * 2 * chunkMS / 1000 // 3200 bytes

	keyringService = "LiveCap"
	keyringUser    = "gemini-api-key"
)

var apiKeyVars = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}

// loadAPIKey mirrors config.load_api_key: environment, then the encrypted
// store, then the Keychain. The value is never printed.
func loadAPIKey() (key, source string) {
	for _, name := range apiKeyVars {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v, "environment (" + name + ")"
		}
	}
	if v := credentials.Get(apiKeyVars[0]); v != "" {
		return v, "encrypted store"
	}
	if v, err := keyring.Get(keyringService, keyringUser); err == nil {
		if v = strings.TrimSpace(v); v != "" {
			return v, "macOS Keychain"
		}
	}
	return "", ""
}

// readWAV returns the PCM payload of a 16 kHz mono 16-bit WAV, skipping the
// header chunks rather than assuming a fixed 44-byte offset.
func readWAV(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF/WAVE file")
	}
	pos := 12
	var channels, bits int
	var rate int
	for pos+8 <= len(raw) {
		id := string(raw[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		body := pos + 8
		switch id {
		case "fmt ":
			channels = int(binary.LittleEndian.Uint16(raw[body+2 : body+4]))
			rate = int(binary.LittleEndian.Uint32(raw[body+4 : body+8]))
			bits = int(binary.LittleEndian.Uint16(raw[body+14 : body+16]))
		case "data":
			if channels != 1 || rate != sendRate || bits != 16 {
				return nil, fmt.Errorf("fixture is %d Hz/%dch/%d-bit, need %d/1/16",
					rate, channels, bits, sendRate)
			}
			end := body + size
			if end > len(raw) {
				end = len(raw)
			}
			return raw[body:end], nil
		}
		pos = body + size
		if size%2 == 1 {
			pos++ // RIFF chunks are word-aligned
		}
	}
	return nil, errors.New("no data chunk")
}

func main() {
	fixture := flag.String("fixture", "../spikes/fixtures/ko_fixture.wav", "16 kHz mono WAV to stream")
	seconds := flag.Float64("seconds", 30, "how much of the fixture to send")
	target := flag.String("target", "en-US", "target language code")
	bisect := flag.Bool("bisect", false, "find which config field the server rejects")
	flag.Parse()

	key, source := loadAPIKey()
	if key == "" {
		fmt.Fprintln(os.Stderr, "no API key: set GEMINI_API_KEY, or store one in Preferences")
		os.Exit(1)
	}
	fmt.Printf("api key from %s\n", source)

	pcm, err := readWAV(*fixture)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture: %v\n", err)
		os.Exit(1)
	}
	want := int(*seconds) * sendRate * 2
	if want > 0 && want < len(pcm) {
		pcm = pcm[:want]
	}
	fmt.Printf("fixture %s: %.1f s of 16 kHz mono\n\n", *fixture, float64(len(pcm))/(sendRate*2))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  key,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "client: %v\n", err)
		os.Exit(1)
	}

	if *bisect {
		bisectConfig(ctx, client, pcm)
		return
	}

	echo := false
	cfg := &genai.LiveConnectConfig{
		// The model has no text output. The English on screen comes only
		// from OutputAudioTranscription.
		ResponseModalities:       []genai.Modality{genai.ModalityAudio},
		InputAudioTranscription:  &genai.AudioTranscriptionConfig{},
		OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
		TranslationConfig: &genai.TranslationConfig{
			TargetLanguageCode: *target,
			EchoTargetLanguage: &echo,
		},
		ContextWindowCompression: &genai.ContextWindowCompressionConfig{
			SlidingWindow: &genai.SlidingWindow{},
		},
		SessionResumption: &genai.SessionResumptionConfig{},
	}

	connected := time.Now()
	session, err := client.Live.Connect(ctx, model, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	defer session.Close()
	fmt.Printf("connected in %v\n\n", time.Since(connected).Round(time.Millisecond))

	type counters struct {
		fragments, audioChunks, audioBytes, turnCompletes, resumptionUpdates int
		firstFragment                                                        time.Duration
		goAway                                                               string
	}
	var c counters
	done := make(chan struct{})
	started := time.Now()

	go func() {
		defer close(done)
		for {
			msg, err := session.Receive()
			if err != nil {
				if !errors.Is(err, io.EOF) && ctx.Err() == nil {
					fmt.Printf("\n[receive ended: %v]\n", err)
				}
				return
			}
			if msg.SessionResumptionUpdate != nil {
				c.resumptionUpdates++
			}
			if msg.GoAway != nil {
				c.goAway = fmt.Sprintf("go_away after %v, time left %v",
					time.Since(started).Round(time.Second), msg.GoAway.TimeLeft)
			}
			sc := msg.ServerContent
			if sc == nil {
				continue
			}
			if sc.OutputTranscription != nil && sc.OutputTranscription.Text != "" {
				if c.fragments == 0 {
					c.firstFragment = time.Since(started)
				}
				c.fragments++
				fmt.Print(sc.OutputTranscription.Text)
			}
			if sc.TurnComplete {
				c.turnCompletes++
				fmt.Print("  ⏎\n")
			}
			// Received audio must be consumed but not played. Failing to
			// read it stalls the stream.
			if sc.ModelTurn != nil {
				for _, part := range sc.ModelTurn.Parts {
					if part.InlineData != nil {
						c.audioChunks++
						c.audioBytes += len(part.InlineData.Data)
					}
				}
			}
		}
	}()

	// Send in real time, the way the capture path will.
	ticker := time.NewTicker(chunkMS * time.Millisecond)
	defer ticker.Stop()
	var maxBacklog time.Duration
	sendStart := time.Now()
	for i := 0; i < len(pcm); i += chunkSize {
		end := i + chunkSize
		if end > len(pcm) {
			end = len(pcm)
		}
		<-ticker.C
		if backlog := time.Since(sendStart) - time.Duration(i/2)*time.Second/sendRate; backlog > maxBacklog {
			maxBacklog = backlog
		}
		err := session.SendRealtimeInput(genai.LiveRealtimeInput{
			Audio: &genai.Blob{
				Data:     pcm[i:end],
				MIMEType: fmt.Sprintf("audio/pcm;rate=%d", sendRate),
			},
		})
		if err != nil {
			fmt.Printf("\n[send failed after %.1f s: %v]\n", time.Since(started).Seconds(), err)
			break
		}
	}

	// Let the tail of the translation come back before closing.
	fmt.Printf("\n\n[audio sent, waiting 5 s for the tail]\n")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	session.Close()
	<-done

	fmt.Printf("\n--- L1 result ---\n")
	fmt.Printf("  fragments:            %d (first at %v)\n", c.fragments, c.firstFragment.Round(time.Millisecond))
	fmt.Printf("  turn_complete:        %d\n", c.turnCompletes)
	fmt.Printf("  audio chunks in:      %d (%d bytes, consumed and discarded)\n", c.audioChunks, c.audioBytes)
	fmt.Printf("  resumption updates:   %d\n", c.resumptionUpdates)
	fmt.Printf("  max send backlog:     %v\n", maxBacklog.Round(time.Millisecond))
	if c.goAway != "" {
		fmt.Printf("  %s\n", c.goAway)
	}
	if c.fragments == 0 {
		fmt.Printf("  FAIL: no transcription fragments arrived\n")
		os.Exit(1)
	}
	fmt.Printf("  PASS\n")
}
