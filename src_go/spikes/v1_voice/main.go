// Spike V1 — can the translation be *heard*, and can the voice be chosen?
//
// Playing the translation needs two facts the design cannot guess at:
//
//  1. What comes back. The application counts the model's audio and throws it
//     away, so nothing here has ever looked at its format. A player needs the
//     sample rate, and the only authority on it is the MIME type the server
//     stamps on each blob.
//  2. Whether the voice is selectable. LiveConnectConfig has SpeechConfig, but
//     this is a preview *translation* model, and it already refuses batch mode,
//     caching, tools and structured output. A field existing in the SDK says
//     nothing about this model honouring it.
//
// Point 2 has a trap: a server that ignores SpeechConfig looks exactly like one
// that honours it, because both connect and both return speech. So this also
// sends a voice name that cannot exist. If nonsense is accepted, the field is
// being dropped on the floor and voice selection is not a feature we have.
//
// Each run writes the received audio to a WAV, so the answer can be listened to
// rather than inferred from byte counts.
//
// This costs money — about $0.037 a minute. Keep -seconds short.
//
//	go run ./spikes/v1_voice -voice ""      # control: whatever the default is
//	go run ./spikes/v1_voice -voice Kore    # a documented prebuilt voice
//	go run ./spikes/v1_voice -voice NoSuchVoiceZZ9  # must be rejected
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
	"google.golang.org/genai"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
	"github.com/dennis2lee/live-translation/src_go/internal/credentials"
)

const (
	model     = "gemini-3.5-live-translate-preview"
	chunkMS   = 100
	sendRate  = 16000
	chunkSize = sendRate * 2 * chunkMS / 1000

	keyringService = "LiveCap"
	keyringUser    = "gemini-api-key"
)

var apiKeyVars = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}

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

// readWAV returns the PCM payload of a 16 kHz mono 16-bit WAV.
func readWAV(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF/WAVE file")
	}
	pos := 12
	var channels, bits, rate int
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
			end := min(body+size, len(raw))
			return raw[body:end], nil
		}
		pos = body + size
		if size%2 == 1 {
			pos++
		}
	}
	return nil, errors.New("no data chunk")
}

// writeWAV wraps raw PCM16 mono so the result can be played.
func writeWAV(path string, pcm []byte, rate int) error {
	var h []byte
	put32 := func(v int) { h = binary.LittleEndian.AppendUint32(h, uint32(v)) }
	put16 := func(v int) { h = binary.LittleEndian.AppendUint16(h, uint16(v)) }
	h = append(h, "RIFF"...)
	put32(36 + len(pcm))
	h = append(h, "WAVEfmt "...)
	put32(16)
	put16(1) // PCM
	put16(1) // mono
	put32(rate)
	put32(rate * 2) // byte rate
	put16(2)        // block align
	put16(16)       // bits
	h = append(h, "data"...)
	put32(len(pcm))
	return os.WriteFile(path, append(h, pcm...), 0o644)
}

// rateFromMIME reads the rate out of "audio/pcm;rate=24000". The server is the
// only authority on this; assuming a rate and being wrong plays the translation
// at the wrong pitch, which sounds like a bug in the model rather than in us.
var rateRE = regexp.MustCompile(`rate=(\d+)`)

func rateFromMIME(mime string) int {
	m := rateRE.FindStringSubmatch(mime)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func main() {
	fixture := flag.String("fixture", "../spikes/fixtures/ko_fixture.wav", "16 kHz mono WAV to stream")
	seconds := flag.Float64("seconds", 12, "how much of the fixture to send")
	target := flag.String("target", "en", "target language code")
	voice := flag.String("voice", "", "prebuilt voice name; empty sends no SpeechConfig")
	echoFlag := flag.Bool("echo", false, "echo_target_language")
	out := flag.String("out", "", "write the received audio here as a WAV")
	speak := flag.Bool("speak", false, "play the translation as it arrives, through audio.Player")
	flag.Parse()

	// The WAV test in v2_play feeds the player on a perfect 100 ms tick. Live
	// audio does not arrive that neatly, and whether the jitter buffer copes
	// with the real arrival pattern is a different question from whether it
	// copes with a metronome.
	var player *audio.Player
	if *speak {
		player = audio.NewPlayer("", audio.TranslatedRate)
		if dev, err := player.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "output device: %v\n", err)
			os.Exit(1)
		} else {
			fmt.Printf("speaking through %s at %.0f Hz\n", dev.Name, dev.DefaultSampleRate)
		}
		defer func() {
			// Let the tail finish rather than cutting the last words off.
			for player.Buffered() > 0 {
				time.Sleep(50 * time.Millisecond)
			}
			time.Sleep(300 * time.Millisecond)
			player.Stop()
			audio.ReleaseContext()
		}()
	}

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
	if want := int(*seconds) * sendRate * 2; want > 0 && want < len(pcm) {
		pcm = pcm[:want]
	}
	sentSeconds := float64(len(pcm)) / (sendRate * 2)
	fmt.Printf("sending %.1f s of 16 kHz mono\n", sentSeconds)

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

	echo := *echoFlag
	cfg := &genai.LiveConnectConfig{
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
	if *voice != "" {
		cfg.SpeechConfig = &genai.SpeechConfig{
			VoiceConfig: &genai.VoiceConfig{
				PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: *voice},
			},
		}
		fmt.Printf("speech_config: prebuilt voice %q\n", *voice)
	} else {
		fmt.Printf("speech_config: none (control)\n")
	}

	session, err := client.Live.Connect(ctx, model, cfg)
	if err != nil {
		// A rejected voice is a *result*, not a crash. The application has to
		// tell "this model has no voice selection" from "the network is down",
		// and this is where that distinction is first visible.
		fmt.Printf("\n--- V1 result ---\n  connect REJECTED: %v\n", err)
		return
	}
	defer session.Close()

	var (
		mimes      = map[string]int{}
		audioBytes int
		chunks     int
		text       strings.Builder
		received   []byte
		firstAudio time.Duration
		setupErr   error
		// backlog is the question a player has to answer: if every blob is
		// played the instant it arrives, how far behind the speaker does the
		// listener end up? Each sample is (audio held) minus (wall clock since
		// the first blob). Growing means the translation is generated faster
		// than it can be spoken, and the lag is permanent.
		maxBacklog float64
		backlogAt  time.Duration
	)
	done := make(chan struct{})
	started := time.Now()

	go func() {
		defer close(done)
		for {
			msg, err := session.Receive()
			if err != nil {
				// The model rejects a bad config about a second *after*
				// setupComplete, so a config fault shows up here rather than at
				// Connect. That is the trap TargetLanguageCode fell into.
				if !errors.Is(err, io.EOF) && ctx.Err() == nil {
					setupErr = err
				}
				return
			}
			sc := msg.ServerContent
			if sc == nil {
				continue
			}
			if sc.OutputTranscription != nil {
				text.WriteString(sc.OutputTranscription.Text)
			}
			if sc.ModelTurn != nil {
				for _, part := range sc.ModelTurn.Parts {
					if part.InlineData == nil {
						continue
					}
					now := time.Since(started)
					if chunks == 0 {
						firstAudio = now
					}
					chunks++
					audioBytes += len(part.InlineData.Data)
					mimes[part.InlineData.MIMEType]++
					received = append(received, part.InlineData.Data...)
					if player != nil {
						player.Write(part.InlineData.Data)
					}

					if rate := rateFromMIME(part.InlineData.MIMEType); rate > 0 {
						held := float64(audioBytes) / float64(rate*2)
						if b := held - (now - firstAudio).Seconds(); b > maxBacklog {
							maxBacklog, backlogAt = b, now
						}
					}
				}
			}
		}
	}()

	ticker := time.NewTicker(chunkMS * time.Millisecond)
	defer ticker.Stop()
	for i := 0; i < len(pcm); i += chunkSize {
		<-ticker.C
		err := session.SendRealtimeInput(genai.LiveRealtimeInput{
			Audio: &genai.Blob{
				Data:     pcm[i:min(i+chunkSize, len(pcm))],
				MIMEType: fmt.Sprintf("audio/pcm;rate=%d", sendRate),
			},
		})
		if err != nil {
			fmt.Printf("[send failed: %v]\n", err)
			break
		}
	}
	select {
	case <-done:
	case <-time.After(6 * time.Second):
	}
	session.Close()
	<-done

	fmt.Printf("\n--- V1 result ---\n")
	if setupErr != nil {
		fmt.Printf("  session ENDED: %v\n", setupErr)
	}
	fmt.Printf("  transcription:  %q\n", strings.TrimSpace(text.String()))
	fmt.Printf("  audio chunks:   %d, first at %v\n", chunks, firstAudio.Round(time.Millisecond))
	fmt.Printf("  audio bytes:    %d\n", audioBytes)
	for mime, n := range mimes {
		rate := rateFromMIME(mime)
		fmt.Printf("  mime:           %q  (%d chunks, rate=%d)\n", mime, n, rate)
		if rate > 0 {
			fmt.Printf("  playback:       %.2f s of audio for %.1f s of speech\n",
				float64(audioBytes)/float64(rate*2), sentSeconds)
		}
	}
	if len(mimes) == 0 {
		fmt.Printf("  mime:           no audio arrived at all\n")
	}
	fmt.Printf("  max backlog:    %.2f s (at %v) — how far behind a player would fall\n",
		maxBacklog, backlogAt.Round(time.Second))

	if *out != "" && len(received) > 0 {
		rate := 0
		for mime := range mimes {
			if r := rateFromMIME(mime); r > 0 {
				rate = r
			}
		}
		if rate == 0 {
			rate = 24000
			fmt.Printf("  (no rate in the MIME type — guessing %d for the WAV)\n", rate)
		}
		if err := writeWAV(*out, received, rate); err != nil {
			fmt.Printf("  wav: %v\n", err)
		} else {
			fmt.Printf("  wrote %s (%d bytes at %d Hz)\n", *out, len(received), rate)
		}
	}
}
