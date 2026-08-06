// Spike R1 — libsoxr via cgo, or a pure-Go windowed-sinc FIR?
//
// Both are fed the same tones through the same 100 ms blocking the real
// capture path uses (PLAN §4), then measured on the two things that matter for
// speech: passband flatness below 8 kHz, and how much of a tone above the
// 16 kHz Nyquist folds back into the band as an audible alias.
//
//	go run ./spikes/r1_resample
package main

import (
	"flag"
	"fmt"
	"math"
	"sort"
	"time"
)

const (
	outRate    = 16000.0
	toneAmp    = 0.5
	seconds    = 2.0
	blockMS    = 100
	warmupOuts = 2000 // samples dropped before measuring, for filter warm-up
)

var inRates = []float64{48000, 44100, 96000}

// Tones below 8 kHz must survive; tones above it must not fold back.
var passband = []float64{200, 1000, 2000, 3000, 4000, 6000, 7000, 7600}
var stopband = []float64{8400, 9000, 10000, 12000, 15000, 20000}

type resampler interface {
	Run(in []int16) []int16
	Name() string
	Close()
}

type soxrRunner struct {
	s *SoxrStream
}

func (r *soxrRunner) Name() string { return "libsoxr " + soxrVersion() }
func (r *soxrRunner) Run(in []int16) []int16 {
	out, err := r.s.Process(in)
	if err != nil {
		panic(err)
	}
	return out
}
func (r *soxrRunner) Close() { r.s.Close() }

type firRunner struct {
	f *FIRStream
}

func (r *firRunner) Name() string           { return "pure-Go Kaiser sinc" }
func (r *firRunner) Run(in []int16) []int16 { return r.f.Process(in) }
func (r *firRunner) Close()                 {}

// tone builds a sine at f, quantised to int16 the way the capture path would.
func tone(f, rate float64, n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(toneAmp * 32767 * math.Sin(2*math.Pi*f*float64(i)/rate))
	}
	return out
}

// goertzelPower returns the power of x at frequency f, normalised so that a
// full-scale sine reads 0.5 (amplitude²/2).
func goertzelPower(x []float64, f, rate float64) float64 {
	if len(x) == 0 {
		return 0
	}
	w := 2 * math.Pi * f / rate
	coeff := 2 * math.Cos(w)
	var s0, s1, s2 float64
	for _, v := range x {
		s0 = v + coeff*s1 - s2
		s2, s1 = s1, s0
	}
	re := s1 - s2*math.Cos(w)
	im := s2 * math.Sin(w)
	n := float64(len(x))
	return 2 * (re*re + im*im) / (n * n)
}

func totalPower(x []float64) float64 {
	var acc float64
	for _, v := range x {
		acc += v * v
	}
	if len(x) == 0 {
		return 0
	}
	return acc / float64(len(x))
}

func dB(x float64) float64 {
	if x <= 0 {
		return math.Inf(-1)
	}
	return 10 * math.Log10(x)
}

// measure runs one tone through a resampler and returns the output samples,
// minus the warm-up, as floats in [-1, 1].
func measure(r resampler, f, inRate float64) ([]float64, time.Duration) {
	n := int(inRate * seconds)
	src := tone(f, inRate, n)
	block := int(inRate * blockMS / 1000)

	var out []int16
	start := time.Now()
	for i := 0; i < len(src); i += block {
		end := i + block
		if end > len(src) {
			end = len(src)
		}
		out = append(out, r.Run(src[i:end])...)
	}
	elapsed := time.Since(start)

	if len(out) > warmupOuts {
		out = out[warmupOuts:]
	}
	fl := make([]float64, len(out))
	for i, v := range out {
		fl[i] = float64(v) / 32768.0
	}
	return fl, elapsed
}

func newRunners(inRate float64) []resampler {
	s, err := NewSoxrStream(inRate, outRate, SoxrHQ)
	if err != nil {
		panic(err)
	}
	return []resampler{
		&soxrRunner{s: s},
		&firRunner{f: NewFIRStream(inRate, outRate)},
	}
}

// probe locates spurious energy: it scans the output band and prints the
// strongest components apart from the tone itself, so a "spur -41 dB" line in
// the table can be attributed instead of guessed at.
func probe(inRate, f float64) {
	fmt.Printf("=== probe: %.0f Hz tone, %.0f -> %.0f Hz ===\n", f, inRate, outRate)
	for _, r := range newRunners(inRate) {
		out, _ := measure(r, f, inRate)
		type peak struct {
			hz, dB float64
		}
		var peaks []peak
		for hz := 50.0; hz < outRate/2; hz += 25 {
			if math.Abs(hz-f) < 100 {
				continue
			}
			p := goertzelPower(out, hz, outRate)
			peaks = append(peaks, peak{hz, dB(p / (toneAmp * toneAmp / 2))})
		}
		sort.Slice(peaks, func(i, j int) bool { return peaks[i].dB > peaks[j].dB })
		fmt.Printf("  %-24s ", r.Name())
		for _, p := range peaks[:5] {
			fmt.Printf("%.0f Hz %.1f dB | ", p.hz, p.dB)
		}
		fmt.Println()
		r.Close()
	}
	fmt.Println()
}

func main() {
	probeAt := flag.Float64("probe", 0, "locate spurious energy for this tone instead of running the table")
	probeRate := flag.Float64("probe-rate", 48000, "input rate for -probe")
	flag.Parse()
	if *probeAt > 0 {
		probe(*probeRate, *probeAt)
		return
	}

	fmt.Printf("libsoxr %s   |   %d ms blocks, %.0f s per tone, %.1f amplitude\n\n",
		soxrVersion(), blockMS, seconds, toneAmp)

	// Reference power of the source tone, for a 0 dB passband reading.
	ref := toneAmp * toneAmp / 2

	for _, inRate := range inRates {
		fmt.Printf("=== %.0f Hz -> %.0f Hz (ratio %.4f) ===\n", inRate, outRate, outRate/inRate)

		type row struct {
			label string
			vals  []string
		}
		runners := newRunners(inRate)
		names := make([]string, len(runners))
		for i, r := range runners {
			names[i] = r.Name()
		}
		fmt.Printf("%-28s %22s %22s\n", "", names[0], names[1])

		fmt.Println("  passband (should read 0.0 dB, spurious well below -60):")
		for _, f := range passband {
			cells := make([]string, len(runners))
			for i, r := range runners {
				out, _ := measure(r, f, inRate)
				p := goertzelPower(out, f, outRate)
				spur := totalPower(out) - p
				cells[i] = fmt.Sprintf("%+6.2f dB, spur %6.1f", dB(p/ref), dB(math.Max(spur, 0)/math.Max(p, 1e-30)))
			}
			fmt.Printf("  %8.0f Hz %14s %22s %22s\n", f, "", cells[0], cells[1])
		}

		fmt.Println("  stopband (whole output should be far below the source):")
		for _, f := range stopband {
			cells := make([]string, len(runners))
			for i, r := range runners {
				out, _ := measure(r, f, inRate)
				cells[i] = fmt.Sprintf("%8.1f dB", dB(totalPower(out)/ref))
			}
			fmt.Printf("  %8.0f Hz %14s %22s %22s\n", f, "", cells[0], cells[1])
		}

		// Cost, measured on a plain 1 kHz tone.
		cells := make([]string, len(runners))
		for i, r := range runners {
			_, took := measure(r, 1000, inRate)
			realtime := took.Seconds() / seconds * 100
			cells[i] = fmt.Sprintf("%.1f ms (%.3f%% RT)", float64(took.Microseconds())/1000, realtime)
		}
		fmt.Printf("  %-26s %22s %22s\n", "cost for 2 s of audio", cells[0], cells[1])

		for _, r := range runners {
			r.Close()
		}
		fmt.Println()
	}
}
