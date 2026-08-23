// Spike Y3 — where do the gate's defaults come from?
//
// The gate needs a threshold (music score that counts as musical) and the
// margin is fixed in code; both have to come from score tables on real
// material, not from taste. This runs the shipped classifier over:
//
//   - the Korean TTS fixture (speech — the gate must never close on this)
//   - synthesized band music: chords, bass, hats (music — it must close)
//   - speech mixed over that music at several levels (the ambiguous case that
//     decides the margin)
//   - any WAV/CAF passed as arguments (real material, e.g. converted Apple
//     sound-design loops, or the user's own songs)
//
// For each it prints per-hop speech/music scores and the distribution, which
// is what DefaultTuning cites.
//
//	go run ./spikes/y3_gatecal [more.wav ...]
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"

	"github.com/dennis2lee/live-translation/src_go/internal/classify"
)

const modelPath = "spikes/fixtures/yamnet.tflite"

func main() {
	model, err := classify.LoadModel(modelPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "model: %v (run from src_go/, fetch first)\n", err)
		os.Exit(1)
	}

	speech, err := readWAV16k("spikes/fixtures/ko_fixture.wav")
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture: %v\n", err)
		os.Exit(1)
	}
	music := synthBand(30 * classify.SampleRate)

	run(model, "speech (Korean TTS)", speech)
	run(model, "music (synth band)", music)
	// The gain sweep decides what "talking over background music" does: at
	// which speech-to-music ratio the verdict flips.
	for _, level := range []float64{0.5, 1.0, 2.0, 4.0} {
		run(model, fmt.Sprintf("speech over music (speech x%.1f)", level),
			mix(speech, music, level))
	}
	for _, path := range os.Args[1:] {
		wave, err := readWAV16k(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			continue
		}
		run(model, path, wave)
	}
}

// run classifies every 0.48 s hop and prints the score table.
func run(model *classify.Model, label string, wave []float32) {
	const hop = 7680
	var speeches, musics []float64
	fmt.Printf("\n=== %s (%.1f s) ===\n", label, float64(len(wave))/classify.SampleRate)
	for off := 0; off+classify.PatchSamples <= len(wave); off += hop {
		scores := model.Scores(wave[off : off+classify.PatchSamples])
		sp := float64(classify.SpeechScore(scores))
		mu := float64(classify.MusicScore(scores))
		speeches = append(speeches, sp)
		musics = append(musics, mu)
		top := classify.Top(scores, 3)
		fmt.Printf("  %5.1fs  speech %.3f  music %.3f   %s / %s / %s\n",
			float64(off)/classify.SampleRate, sp, mu,
			classify.ClassName(top[0]), classify.ClassName(top[1]), classify.ClassName(top[2]))
	}
	if len(speeches) == 0 {
		return
	}
	fmt.Printf("  speech: %s\n  music:  %s\n", dist(speeches), dist(musics))
}

func dist(xs []float64) string {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	q := func(p float64) float64 { return s[int(p*float64(len(s)-1))] }
	return fmt.Sprintf("min %.3f  p25 %.3f  median %.3f  p75 %.3f  max %.3f",
		s[0], q(0.25), q(0.5), q(0.75), s[len(s)-1])
}

// synthBand makes n samples of something a listener would call music: an
// eight-chord loop of decaying piano-ish tones, a bass line, and noise hats.
func synthBand(n int) []float32 {
	const sr = classify.SampleRate
	out := make([]float64, n)
	chords := [][]float64{
		{261.63, 329.63, 392.00}, // C
		{220.00, 261.63, 329.63}, // Am
		{174.61, 220.00, 261.63}, // F
		{196.00, 246.94, 293.66}, // G
	}
	beat := sr / 2 // 120 bpm eighth notes
	x := int64(7)
	for i := range out {
		t := float64(i) / sr
		bar := (i / (beat * 4)) % len(chords)
		strike := i % (beat * 4)
		dt := float64(strike) / sr
		env := math.Exp(-dt / 0.8)
		for _, f := range chords[bar] {
			w := 2 * math.Pi * f * dt
			out[i] += 0.22 * env * (math.Sin(w) + 0.3*math.Sin(2*w))
		}
		// Bass on the beat.
		bdt := float64(i%beat) / sr
		out[i] += 0.25 * math.Exp(-bdt/0.3) * math.Sin(2*math.Pi*chords[bar][0]/2*bdt)
		// A noise hat on every eighth.
		if i%(beat/2) < sr/50 {
			x = (1103515245*x + 12345) % (1 << 31)
			out[i] += 0.08 * (float64(x)/float64(1<<30) - 1) * math.Exp(-float64(i%(beat/2))/(sr/200))
		}
		_ = t
	}
	f32 := make([]float32, n)
	for i, v := range out {
		f32[i] = float32(v)
	}
	return f32
}

func mix(a, b []float32, aGain float64) []float32 {
	n := min(len(a), len(b))
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = float32(aGain)*a[i] + b[i]
	}
	return out
}

// readWAV16k reads a 16 kHz mono 16-bit WAV into floats.
func readWAV16k(path string) ([]float32, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < 44 || string(raw[0:4]) != "RIFF" {
		return nil, errors.New("not a WAV")
	}
	pos := 12
	rate := 0
	for pos+8 <= len(raw) {
		id := string(raw[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		body := pos + 8
		switch id {
		case "fmt ":
			rate = int(binary.LittleEndian.Uint32(raw[body+4 : body+8]))
		case "data":
			if rate != classify.SampleRate {
				return nil, fmt.Errorf("%d Hz, need %d (convert: afconvert -f WAVE -d LEI16@16000 -c 1 in out.wav)", rate, classify.SampleRate)
			}
			end := min(body+size, len(raw))
			out := make([]float32, (end-body)/2)
			for i := range out {
				v := int16(binary.LittleEndian.Uint16(raw[body+2*i:]))
				out[i] = float32(v) / 32768
			}
			return out, nil
		}
		pos = body + size + size%2
	}
	return nil, errors.New("no data chunk")
}
