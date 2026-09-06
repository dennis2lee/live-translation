// Spike Y4 — the music gate against the real API, end to end.
//
// The unit tests prove the gate machine and the classifier separately, and
// the filter tests prove the send loop obeys the filter. This is the piece
// none of them can prove: a real translator session, with the real gate wired
// in, fed music and then speech at real-time pace, must
//
//  1. stop sending chunks a few seconds into the music (hold + patch fill),
//  2. resume on speech and flush the pre-roll,
//  3. still produce captions for the speech on both sides of the song.
//
// Costs money like every live spike — about $0.037/min, and this runs ~75 s.
//
//	go run ./spikes/y4_gate_live
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
	"github.com/dennis2lee/live-translation/src_go/internal/classify"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
)

func main() {
	// The application's chain, encrypted store included — see v3_voiceid.
	key := config.LoadAPIKey()
	if key == "" {
		fmt.Fprintln(os.Stderr, "no API key")
		os.Exit(1)
	}
	model, err := classify.LoadModel("spikes/fixtures/yamnet.tflite")
	if err != nil {
		fmt.Fprintln(os.Stderr, "model:", err)
		os.Exit(1)
	}
	speech, err := readWAV("spikes/fixtures/ko_fixture.wav")
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixture:", err)
		os.Exit(1)
	}

	gate := classify.NewGate(model, classify.GateTuning{Threshold: 0.5, HoldSeconds: 2.0})
	defer gate.Close()

	var mu sync.Mutex
	var captions strings.Builder
	var phase string
	perPhase := map[string]int{}

	ring := audio.NewRing(audio.ChunkBytes * 64)
	tr, err := translator.New(translator.Options{
		Ring:   ring,
		APIKey: key,
		Session: translator.SessionConfig{
			TargetLanguageCode: "en",
		},
		Filter: gate.Filter,
		Callbacks: translator.Callbacks{
			OnTarget: func(text string) {
				mu.Lock()
				captions.WriteString(text)
				perPhase[phase] += len(text)
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
	defer tr.Stop()

	setPhase := func(p string) {
		mu.Lock()
		phase = p
		mu.Unlock()
		fmt.Printf("\n--- %s (sent so far: %d chunks, gate closed: %v)\n",
			p, tr.Stats().ChunksSent, gate.Closed())
	}

	// The script: speech, then music, then speech again — the shape of a
	// meeting with a song in the middle.
	music := synthBand(30 * classify.SampleRate)
	feed := func(wave []float32, seconds float64) {
		n := int(seconds * classify.SampleRate)
		if n > len(wave) {
			n = len(wave)
		}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		chunk := make([]byte, audio.ChunkBytes)
		for off := 0; off+1600 <= n; off += 1600 {
			<-ticker.C
			for i := 0; i < 1600; i++ {
				v := int16(wave[off+i] * 32767)
				binary.LittleEndian.PutUint16(chunk[2*i:], uint16(v))
			}
			ring.Write(chunk)
		}
	}

	setPhase("speech #1")
	feed(speech[16000:], 20) // skip the leading second of quiet
	sentAfterSpeech1 := tr.Stats().ChunksSent

	setPhase("music")
	feed(music, 25)
	sentAfterMusic := tr.Stats().ChunksSent
	closedDuringMusic := gate.Closed()

	setPhase("speech #2")
	feed(speech[16000+20*classify.SampleRate:], 20)
	time.Sleep(4 * time.Second) // let the tail of the captions arrive
	sentAfterSpeech2 := tr.Stats().ChunksSent

	gated, flushed := gate.GatedMS()
	mu.Lock()
	text := captions.String()
	p1, pm, p2 := perPhase["speech #1"], perPhase["music"], perPhase["speech #2"]
	mu.Unlock()

	fmt.Printf("\n--- Y4 result ---\n")
	fmt.Printf("  chunks sent: speech#1 %d, music +%d, speech#2 +%d (fed 200/250/200)\n",
		sentAfterSpeech1, sentAfterMusic-sentAfterSpeech1, sentAfterSpeech2-sentAfterMusic)
	fmt.Printf("  gate closed during music: %v\n", closedDuringMusic)
	fmt.Printf("  gated %d ms, flushed back %d ms\n", gated, flushed)
	fmt.Printf("  caption bytes per phase: speech#1 %d, music %d, speech#2 %d\n", p1, pm, p2)
	fmt.Printf("  captions: %q\n", strings.TrimSpace(text))

	ok := closedDuringMusic || gated > 5000
	musicSent := sentAfterMusic - sentAfterSpeech1
	fmt.Printf("\n  verdict: gate engaged=%v, music chunks sent=%d of 250 fed\n", ok, musicSent)
}

// synthBand is y3_gatecal's music, unchanged.
func synthBand(n int) []float32 {
	const sr = classify.SampleRate
	out := make([]float64, n)
	chords := [][]float64{
		{261.63, 329.63, 392.00},
		{220.00, 261.63, 329.63},
		{174.61, 220.00, 261.63},
		{196.00, 246.94, 293.66},
	}
	beat := sr / 2
	x := int64(7)
	for i := range out {
		bar := (i / (beat * 4)) % len(chords)
		dt := float64(i%(beat*4)) / sr
		env := math.Exp(-dt / 0.8)
		for _, f := range chords[bar] {
			w := 2 * math.Pi * f * dt
			out[i] += 0.22 * env * (math.Sin(w) + 0.3*math.Sin(2*w))
		}
		bdt := float64(i%beat) / sr
		out[i] += 0.25 * math.Exp(-bdt/0.3) * math.Sin(2*math.Pi*chords[bar][0]/2*bdt)
		if i%(beat/2) < sr/50 {
			x = (1103515245*x + 12345) % (1 << 31)
			out[i] += 0.08 * (float64(x)/float64(1<<30) - 1) * math.Exp(-float64(i%(beat/2))/(sr/200))
		}
	}
	f32 := make([]float32, n)
	for i, v := range out {
		f32[i] = float32(v)
	}
	return f32
}

func readWAV(path string) ([]float32, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < 44 || string(raw[0:4]) != "RIFF" {
		return nil, errors.New("not a WAV")
	}
	pos := 12
	for pos+8 <= len(raw) {
		id := string(raw[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		body := pos + 8
		if id == "data" {
			end := min(body+size, len(raw))
			out := make([]float32, (end-body)/2)
			for i := range out {
				out[i] = float32(int16(binary.LittleEndian.Uint16(raw[body+2*i:]))) / 32768
			}
			return out, nil
		}
		pos = body + size + size%2
	}
	return nil, errors.New("no data chunk")
}
