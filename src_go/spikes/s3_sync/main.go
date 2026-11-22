// Spike S3 — when does the caption text arrive, against the voice it belongs to?
//
// The report from live use is that the captions always trail the translated
// speech. This records the two streams the translator hands the application —
// translated audio and caption fragments — with the time each arrived, so the
// lag can be read off a recording instead of argued about.
//
// It drives translator.Translator, the shipping code, for either service, and
// feeds it the way capture does: 100 ms at a time, in real time. It writes
// events.jsonl (one line per callback) and out.wav (the translated speech)
// into -out and nothing else; analyze.py reads those, so a run never has to
// be paid for twice.
//
// This costs money — a few cents a minute. Keep -seconds short.
//
//	go run ./spikes/s3_sync -out /tmp/sync -seconds 95
//	go run ./spikes/s3_sync -out /tmp/sync-openai -provider openai
//	python3 spikes/s3_sync/analyze.py /tmp/sync
package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
)

func readWAV(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF/WAVE file")
	}
	pos := 12
	for pos+8 <= len(raw) {
		id := string(raw[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		body := pos + 8
		if id == "data" {
			return raw[body:min(body+size, len(raw))], nil
		}
		pos = body + size + size%2
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

// event is one callback. T is milliseconds since the first chunk went into
// the ring, so it lines up with a position in the fixture.
type event struct {
	T      int64  `json:"t"`
	Audio  int    `json:"audio,omitempty"` // bytes of 24 kHz PCM16
	Target string `json:"target,omitempty"`
	Source string `json:"source,omitempty"`
	Turn   bool   `json:"turn,omitempty"`
}

func main() {
	fixture := flag.String("fixture", "../spikes/fixtures/ko_fixture.wav", "16 kHz mono WAV to stream")
	seconds := flag.Float64("seconds", 95, "how much of the fixture to send")
	target := flag.String("target", "en", "target language code, bare")
	provider := flag.String("provider", "gemini", "gemini or openai")
	out := flag.String("out", "", "directory for events.jsonl and out.wav")
	tail := flag.Float64("tail", 12, "seconds of silence to send after the fixture")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "pass -out")
		os.Exit(1)
	}
	// Wherever the application finds it; never printed.
	key := config.LoadKey(config.Provider(*provider))
	if key == "" {
		fmt.Fprintln(os.Stderr, "no API key stored for", *provider)
		os.Exit(1)
	}
	pcm, err := readWAV(*fixture)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture: %v\n", err)
		os.Exit(1)
	}
	if want := int(*seconds*audio.TargetRate) * 2; want > 0 && want < len(pcm) {
		pcm = pcm[:want]
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var (
		mu      sync.Mutex
		events  []event
		speech  []byte
		started time.Time
	)
	note := func(e event) {
		mu.Lock()
		defer mu.Unlock()
		if started.IsZero() {
			return // before the first chunk: nothing to line it up against
		}
		e.T = time.Since(started).Milliseconds()
		events = append(events, e)
	}

	ring := audio.NewRing(audio.TargetRate * 2 * 10)
	tr, err := translator.New(translator.Options{
		Ring:     ring,
		APIKey:   key,
		Provider: *provider,
		Session:  translator.SessionConfig{TargetLanguageCode: *target},
		Callbacks: translator.Callbacks{
			OnTarget: func(text string) {
				fmt.Print(text)
				note(event{Target: text})
			},
			OnSource:       func(text string) { note(event{Source: text}) },
			OnTurnComplete: func() { note(event{Turn: true}) },
			OnAudio: func(b []byte) {
				note(event{Audio: len(b)})
				mu.Lock()
				speech = append(speech, b...)
				mu.Unlock()
			},
			OnState: func(s translator.State, detail string) {
				fmt.Fprintf(os.Stderr, "[state %s %s]\n", s, detail)
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

	// Silence after the fixture, not a stopped stream: the live application
	// keeps sending whatever the room holds, and the model finishes its
	// sentence against that.
	stream := append(append([]byte(nil), pcm...), make([]byte, int(*tail*audio.TargetRate)*2)...)
	const chunk = audio.TargetRate * 2 / 10
	mu.Lock()
	started = time.Now()
	mu.Unlock()
	next := started
	for i := 0; i < len(stream); i += chunk {
		ring.Write(stream[i:min(i+chunk, len(stream))])
		next = next.Add(100 * time.Millisecond)
		time.Sleep(time.Until(next))
	}
	tr.Stop()

	mu.Lock()
	defer mu.Unlock()
	f, err := os.Create(filepath.Join(*out, "events.jsonl"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	enc := json.NewEncoder(f)
	for _, e := range events {
		_ = enc.Encode(e)
	}
	f.Close()
	if err := writeWAV(filepath.Join(*out, "out.wav"), speech, audio.TranslatedRate); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("\n\n%s: %d events, %.1f s of translated speech, written to %s\n",
		*provider, len(events), float64(len(speech))/(audio.TranslatedRate*2), *out)
}
