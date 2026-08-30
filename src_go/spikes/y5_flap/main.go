// Spike Y5 — why the gate flaps, and what stops it.
//
// The 1.3.0 gate opened on the first non-musical frame. RESULTS.md predicted
// the consequence and called it the safe direction ("quiet, sparse music will
// flap the gate open ... it leaks translation, it never eats speech"). In use
// it was not safe: one misread frame in the middle of a song reopened the gate
// AND flushed the 1.5 s pre-roll, so a single bad frame sent ~3.5 s of music
// to be translated, and it repeated every time the music thinned out. This is
// the spike that measured it and chose the release hold that fixed it in
// 1.4.0; it is kept so the sweep can be re-run against new material.
//
// This measures, on real material:
//
//  1. the run-length distribution of sub-threshold frames *inside* music —
//     how long a spurious opening actually is, which is what sizes a release
//     hold;
//  2. how far the music score dips during those runs, which sizes a
//     hysteresis band;
//  3. leaked and clipped milliseconds for the current policy against
//     candidate ones, swept, on a labelled speech/music/speech timeline.
//
// Ground truth is the timeline construction, so "leaked" and "clipped" are
// counted, not judged.
//
//	go run ./spikes/y5_flap [extra-music.wav ...]
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/dennis2lee/live-translation/src_go/internal/classify"
)

const (
	modelPath   = "spikes/fixtures/yamnet.tflite"
	chunkSample = 1600 // 100 ms at 16 kHz
)

// frame is one classification, tagged with the chunk it lands on.
type frame struct {
	chunk         int
	speech, music float64
	top           []int
	truth         string // "music", "speech" or "gap"
}

func main() {
	model, err := classify.LoadModel(modelPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "model: %v (run from src_go/)\n", err)
		os.Exit(1)
	}

	// --- part 1: per-material score sequences ------------------------------
	fmt.Println("=== per-material frame scores (threshold 0.5) ===")
	fmt.Printf("%-30s %6s %7s %7s %7s  %-28s %s\n",
		"material", "frames", "musicP50", "musicP10", "musicMin", "clear-runs inside", "top classes")
	for _, path := range materials() {
		wave, err := readWAV16k(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			continue
		}
		fr := classifyStream(model, wave, "music")
		reportMaterial(filepath.Base(path), fr)
	}

	// --- part 2: the labelled timeline and the policy sweep ----------------
	speech, err := readWAV16k("spikes/fixtures/ko_fixture.wav")
	if err != nil {
		fmt.Fprintf(os.Stderr, "speech fixture: %v\n", err)
		os.Exit(1)
	}
	music, err := readWAV16k(musicForTimeline())
	if err != nil {
		fmt.Fprintf(os.Stderr, "timeline music: %v\n", err)
		os.Exit(1)
	}

	// The gap between a song and the speech after it is what makes a release
	// hold free: the countdown runs during the silence. 0 s is the adversarial
	// case the user's description says does not happen, measured anyway.
	for _, gap := range []float64{0, 0.5, 1.0} {
		frames, chunks := timeline(model, speech, music, gap)
		fmt.Printf("\n=== timeline: speech 20s / gap %.1fs / music 30s / gap %.1fs / speech 20s ===\n", gap, gap)
		fmt.Printf("%d chunks, %d frames\n", chunks, len(frames))
		fmt.Printf("%-46s %9s %9s %6s\n", "policy", "leaked", "clipped", "opens")

		run(frames, chunks, policy{name: "1.3.0: open on one clear frame",
			threshold: 0.5, hysteresis: 0, hold: 2.0, release: 0, preroll: 15})
		for _, h := range []float64{0, 0.15} {
			for _, r := range []float64{0.5, 1.0, 1.5, 2.0, 3.0} {
				run(frames, chunks, policy{
					name:      fmt.Sprintf("hysteresis %.2f + release %.1fs", h, r),
					threshold: 0.5, hysteresis: h, hold: 2.0, release: r, preroll: 40})
			}
		}
		// Pre-roll sizing at the chosen release: how much held-back audio is
		// needed before the first words after a song start being eaten.
		fmt.Printf("%-46s %9s %9s %6s\n", "  -- preroll sweep at release 1.5s --", "", "", "")
		for _, pr := range []int{10, 15, 20, 25, 30, 40} {
			run(frames, chunks, policy{name: fmt.Sprintf("  preroll %d chunks (%.1fs)", pr, float64(pr)/10),
				threshold: 0.5, hysteresis: 0, hold: 2.0, release: 1.5, preroll: pr})
		}
	}
}

// --- material ---------------------------------------------------------------

func materials() []string {
	if len(os.Args) > 1 {
		return os.Args[1:]
	}
	return nil
}

func musicForTimeline() string {
	if len(os.Args) > 1 {
		return os.Args[1]
	}
	return ""
}

// --- classification ---------------------------------------------------------

// classifyStream runs the real gate geometry over a waveform: a 15600-sample
// sliding window, a verdict every 7680 samples, chunks of 1600.
func classifyStream(model *classify.Model, wave []float32, truth string) []frame {
	var out []frame
	window := make([]float32, 0, classify.PatchSamples)
	fresh := 0
	for c := 0; (c+1)*chunkSample <= len(wave); c++ {
		window = append(window, wave[c*chunkSample:(c+1)*chunkSample]...)
		if excess := len(window) - classify.PatchSamples; excess > 0 {
			copy(window, window[excess:])
			window = window[:classify.PatchSamples]
		}
		fresh += chunkSample
		if len(window) < classify.PatchSamples || fresh < 7680 {
			continue
		}
		fresh = 0
		s := model.Scores(window)
		out = append(out, frame{
			chunk:  c,
			speech: float64(classify.SpeechScore(s)),
			music:  float64(classify.MusicScore(s)),
			top:    classify.Top(s, 2),
			truth:  truth,
		})
	}
	return out
}

func reportMaterial(name string, fr []frame) {
	if len(fr) == 0 {
		return
	}
	ms := make([]float64, len(fr))
	for i, f := range fr {
		ms[i] = f.music
	}
	sort.Float64s(ms)
	p := func(q float64) float64 { return ms[int(q*float64(len(ms)-1))] }

	// Clear-runs: consecutive frames whose music score sits under 0.5 — each
	// one is a spurious reopening under the shipped policy.
	runs := []int{}
	cur := 0
	for _, f := range fr {
		if f.music < 0.5 || f.music < f.speech+classify.Margin {
			cur++
		} else if cur > 0 {
			runs = append(runs, cur)
			cur = 0
		}
	}
	if cur > 0 {
		runs = append(runs, cur)
	}
	hist := map[int]int{}
	for _, r := range runs {
		hist[r]++
	}
	keys := []int{}
	for k := range hist {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	desc := ""
	for _, k := range keys {
		desc += fmt.Sprintf("%dx%d ", hist[k], k)
	}
	if desc == "" {
		desc = "none"
	}
	fmt.Printf("%-30s %6d %7.3f %7.3f %7.3f  %-28s %s\n",
		name, len(fr), p(0.5), p(0.1), ms[0], desc, topClasses(fr))
}

// topClasses names the classes that won most often — the answer to "if this is
// not scoring as music, what does the model think it is?"
func topClasses(fr []frame) string {
	count := map[int]int{}
	for _, f := range fr {
		for _, i := range f.top {
			count[i]++
		}
	}
	type kv struct {
		i, n int
	}
	all := []kv{}
	for i, n := range count {
		all = append(all, kv{i, n})
	}
	sort.Slice(all, func(a, b int) bool { return all[a].n > all[b].n })
	out := ""
	for i, e := range all {
		if i == 3 {
			break
		}
		out += fmt.Sprintf("%s(%d) ", classify.ClassName(e.i), e.n)
	}
	return out
}

// timeline builds speech/gap/music/gap/speech and classifies it, tagging each
// frame with the truth of the chunk it lands on.
func timeline(model *classify.Model, speech, music []float32, gapSeconds float64) ([]frame, int) {
	const sr = classify.SampleRate
	seg := func(src []float32, seconds int, from int) []float32 {
		n := seconds * sr
		out := make([]float32, n)
		for i := 0; i < n; i++ {
			out[i] = src[(from+i)%len(src)]
		}
		return out
	}
	gap := make([]float32, int(gapSeconds*sr)) // the pause the user described between sections

	var wave []float32
	var truth []string
	add := func(w []float32, label string) {
		wave = append(wave, w...)
		for i := 0; i+chunkSample <= len(w); i += chunkSample {
			truth = append(truth, label)
		}
	}
	add(seg(speech, 20, sr), "speech")
	add(gap, "gap")
	add(seg(music, 30, 0), "music")
	add(gap, "gap")
	add(seg(speech, 20, 21*sr), "speech")

	fr := classifyStream(model, wave, "")
	for i := range fr {
		if fr[i].chunk < len(truth) {
			fr[i].truth = truth[fr[i].chunk]
		}
	}
	// Chunk truth is needed by the simulator too; stash it globally-ish.
	chunkTruth = truth
	return fr, len(truth)
}

var chunkTruth []string

// --- the policy simulator ---------------------------------------------------

type policy struct {
	name       string
	threshold  float64
	hysteresis float64 // reopen only under threshold-hysteresis
	hold       float64 // seconds of musical frames to close
	release    float64 // seconds of clear frames to open (0 = open instantly)
	preroll    int     // chunks
}

func framesFor(seconds float64) int {
	n := int(seconds/(7680.0/classify.SampleRate) + 0.999)
	if n < 1 {
		n = 1
	}
	return n
}

// run replays the timeline through one policy and counts what the listener
// would have paid for and what they would have lost.
func run(frames []frame, chunks int, p policy) {
	holdFrames, releaseFrames := framesFor(p.hold), 1
	if p.release > 0 {
		releaseFrames = framesFor(p.release)
	}

	byChunk := map[int]frame{}
	for _, f := range frames {
		byChunk[f.chunk] = f
	}

	closed := false
	musical := false // the sticky per-frame verdict
	musicalRun, clearRun := 0, 0
	preroll := []int{} // chunk indices held back
	opens := 0

	sent := make([]bool, chunks)
	for c := 0; c < chunks; c++ {
		if f, ok := byChunk[c]; ok {
			// Schmitt trigger on the music score.
			switch {
			case f.music >= p.threshold && f.music >= f.speech+classify.Margin:
				musical = true
			case f.music <= p.threshold-p.hysteresis || f.speech > f.music:
				musical = false
			}
			if musical {
				musicalRun++
				clearRun = 0
				if musicalRun >= holdFrames {
					closed = true
				}
			} else {
				clearRun++
				musicalRun = 0
				if clearRun >= releaseFrames {
					if closed {
						opens++
					}
					closed = false
				}
			}
		}
		if closed {
			preroll = append(preroll, c)
			if len(preroll) > p.preroll {
				preroll = preroll[1:]
			}
			continue
		}
		for _, held := range preroll {
			sent[held] = true
		}
		preroll = preroll[:0]
		sent[c] = true
	}

	var leaked, clipped int
	for c := 0; c < chunks; c++ {
		switch chunkTruth[c] {
		case "music":
			if sent[c] {
				leaked++
			}
		case "speech":
			if !sent[c] {
				clipped++
			}
		}
	}
	fmt.Printf("%-52s %6d ms %6d ms %6d\n", p.name, leaked*100, clipped*100, opens)
}

// --- wav --------------------------------------------------------------------

func readWAV16k(path string) ([]float32, error) {
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
