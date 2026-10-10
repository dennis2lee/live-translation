// Spike O2 — the whole translator, on OpenAI, end to end.
//
// O1 talked to the endpoint directly. This drives the application's own
// translator — ring buffer, send loop, supervisor, the OpenAI session behind
// liveSession — with the Korean fixture written into the ring at real-time
// pace, the way capture fills it. What comes back is printed and the audio
// saved, so the session's conversion to 24 kHz can be checked by ear and by
// measurement.
//
// Costs $0.034 a minute.
//
//	go run ./spikes/o2_pipeline -seconds 30 -out /tmp/o2
package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
)

func loadKey(envFile string) string {
	if v := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); v != "" {
		return v
	}
	f, err := os.Open(envFile)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "OPENAI_API_KEY="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

func readWAV(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pos := 12
	for pos+8 <= len(raw) {
		id := string(raw[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		if id == "data" {
			return raw[pos+8 : min(pos+8+size, len(raw))], nil
		}
		pos += 8 + size + size%2
	}
	return nil, errors.New("no data chunk")
}

func writeWAV(path string, pcm []byte, rate int) error {
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(pcm)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(pcm)))
	return os.WriteFile(path, append(h, pcm...), 0o644)
}

func main() {
	fixture := flag.String("fixture", "spikes/fixtures/ko_fixture.wav", "16 kHz mono WAV")
	seconds := flag.Float64("seconds", 30, "how much of the fixture to send")
	target := flag.String("target", "en", "bare language code")
	envFile := flag.String("env", "../.env", ".env to read OPENAI_API_KEY from")
	out := flag.String("out", "", "directory for the translated audio")
	flag.Parse()

	key := loadKey(*envFile)
	if key == "" {
		fmt.Fprintln(os.Stderr, "no OPENAI_API_KEY")
		os.Exit(1)
	}
	pcm, err := readWAV(*fixture)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if n := int(*seconds * audio.TargetRate * 2); n < len(pcm) {
		pcm = pcm[:n]
	}

	var (
		mu       sync.Mutex
		speech   []byte
		started  = time.Now()
		firstAt  time.Duration
		target_  strings.Builder
		sessions int
	)
	ring := audio.NewRing(audio.TargetRate * 2 * 10)
	tr, err := translator.New(translator.Options{
		Ring:     ring,
		APIKey:   key,
		Provider: translator.ProviderOpenAI,
		Session:  translator.SessionConfig{TargetLanguageCode: *target},
		Callbacks: translator.Callbacks{
			OnTarget: func(text string) {
				mu.Lock()
				target_.WriteString(text)
				mu.Unlock()
			},
			OnState: func(s translator.State, detail string) {
				fmt.Printf("[%5.2f] state %s %s\n", time.Since(started).Seconds(), s, detail)
			},
			OnSessionStart: func() {
				mu.Lock()
				sessions++
				mu.Unlock()
			},
			OnAudio: func(b []byte) {
				mu.Lock()
				if firstAt == 0 {
					firstAt = time.Since(started)
				}
				speech = append(speech, b...)
				mu.Unlock()
			},
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "translator:", err)
		os.Exit(1)
	}
	if err := tr.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start:", err)
		os.Exit(1)
	}

	// Capture's pace: 100 ms at a time, in real time, then 4 s of silence so
	// the last sentence is translated.
	stream := append(append([]byte(nil), pcm...), make([]byte, audio.TargetRate*2*4)...)
	next := time.Now()
	for i := 0; i < len(stream); i += 3200 {
		ring.Write(stream[i:min(i+3200, len(stream))])
		next = next.Add(100 * time.Millisecond)
		time.Sleep(time.Until(next))
	}
	tr.Stop()

	mu.Lock()
	defer mu.Unlock()
	st := tr.Stats()
	fmt.Printf("\nsent %.1f s; received %.1f s of speech at 24 kHz; first audio at %.2f s; %d session start(s); reconnects %d\n",
		float64(len(stream))/(audio.TargetRate*2), float64(len(speech))/(audio.TranslatedRate*2),
		firstAt.Seconds(), sessions, st.Reconnects)
	fmt.Printf("captions: %s\n", target_.String())
	if *out != "" {
		_ = os.MkdirAll(*out, 0o755)
		_ = writeWAV(filepath.Join(*out, "speech_24k.wav"), speech, audio.TranslatedRate)
		fmt.Println("saved", filepath.Join(*out, "speech_24k.wav"))
	}
}
