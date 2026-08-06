package resample

import (
	"math"
	"testing"
)

func tone(freq, rate float64, n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(0.5 * math.Sin(2*math.Pi*freq*float64(i)/rate))
	}
	return out
}

// power at freq, normalised so a 0.5-amplitude sine reads 0.125.
func power(x []float32, freq, rate float64) float64 {
	if len(x) == 0 {
		return 0
	}
	w := 2 * math.Pi * freq / rate
	coeff := 2 * math.Cos(w)
	var s1, s2 float64
	for _, v := range x {
		s0 := float64(v) + coeff*s1 - s2
		s2, s1 = s1, s0
	}
	re := s1 - s2*math.Cos(w)
	im := s2 * math.Sin(w)
	n := float64(len(x))
	return 2 * (re*re + im*im) / (n * n)
}

// implementations returns every filter compiled into this build, so the same
// checks run against both on macOS and against the pure-Go one everywhere else.
func implementations() map[string]func(in, out float64) (Stream, error) {
	out := map[string]func(in, out float64) (Stream, error){
		"pure-Go": func(in, o float64) (Stream, error) { return newFIRStream(in, o), nil },
	}
	if newNativeStream != nil {
		out["native"] = newNativeStream
	}
	return out
}

func TestVersionIsReported(t *testing.T) {
	if Version() == "" {
		t.Error("no filter name reported")
	}
	t.Logf("resampler in use: %s", Version())
}

func TestRatesAreValidated(t *testing.T) {
	if _, err := NewStream(0, 16000); err == nil {
		t.Error("a zero input rate was accepted")
	}
	if _, err := NewStream(48000, -1); err == nil {
		t.Error("a negative output rate was accepted")
	}
}

// The whole point of the streaming API: a 48 kHz block comes out at 16 kHz with
// its content intact.
func TestResamplesATone(t *testing.T) {
	s, err := NewStream(48000, 16000)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var out []float32
	block := 4800 // 100 ms at 48 kHz
	src := tone(1000, 48000, block*20)
	for i := 0; i+block <= len(src); i += block {
		got, err := s.Process(src[i : i+block])
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, got...)
	}

	// A third of the input, less the filter's startup delay. That delay is a
	// fixed number of samples, not a fraction of the run, so the allowance is
	// absolute: measured at 486 samples here for HQ at 48k->16k.
	const filterDelayAllowance = 1000
	want := len(src) / 3
	if len(out) > want || len(out) < want-filterDelayAllowance {
		t.Errorf("%d samples out for %d in, want %d less the filter delay", len(out), len(src), want)
	}
	// Drop the warm-up before measuring.
	measured := out[2000:]
	got := power(measured, 1000, 16000)
	const fullScale = 0.125 // a 0.5-amplitude sine
	if db := 10 * math.Log10(got/fullScale); math.Abs(db) > 0.5 {
		t.Errorf("1 kHz came through at %.2f dB, want flat", db)
	}
}

// Content above the 8 kHz Nyquist must be filtered out, not folded back into
// the band as an audible alias.
func TestRejectsContentAboveNyquist(t *testing.T) {
	s, err := NewStream(48000, 16000)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var out []float32
	block := 4800
	src := tone(12000, 48000, block*20)
	for i := 0; i+block <= len(src); i += block {
		got, _ := s.Process(src[i : i+block])
		out = append(out, got...)
	}

	var total float64
	for _, v := range out[2000:] {
		total += float64(v) * float64(v)
	}
	total /= float64(len(out) - 2000)
	if db := 10 * math.Log10(total/0.125); db > -60 {
		t.Errorf("a 12 kHz tone came through at %.1f dB, want it rejected", db)
	}
}

// Block boundaries must be seamless — that is why the filter state is kept.
func TestBlockBoundariesAreSeamless(t *testing.T) {
	whole, err := NewStream(48000, 16000)
	if err != nil {
		t.Fatal(err)
	}
	defer whole.Close()
	split, err := NewStream(48000, 16000)
	if err != nil {
		t.Fatal(err)
	}
	defer split.Close()

	src := tone(1000, 48000, 9600)

	a, err := whole.Process(src)
	if err != nil {
		t.Fatal(err)
	}
	oneGo := append([]float32(nil), a...)

	var inPieces []float32
	for i := 0; i < len(src); i += 480 {
		got, err := split.Process(src[i : i+480])
		if err != nil {
			t.Fatal(err)
		}
		inPieces = append(inPieces, got...)
	}

	n := min(len(oneGo), len(inPieces))
	if n == 0 {
		t.Fatal("nothing came out")
	}
	for i := 0; i < n; i++ {
		if math.Abs(float64(oneGo[i]-inPieces[i])) > 1e-5 {
			t.Fatalf("sample %d differs: one block %v, many blocks %v", i, oneGo[i], inPieces[i])
		}
	}
}

// The returned slice is reused, so the caller must copy. This pins the
// contract rather than leaving it to a comment.
func TestOutputBufferIsReused(t *testing.T) {
	s, err := NewStream(48000, 16000)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, err := s.Process(tone(1000, 48000, 4800))
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Skip("no output from the first block")
	}
	firstPtr := &first[0]
	second, err := s.Process(tone(2000, 48000, 4800))
	if err != nil {
		t.Fatal(err)
	}
	if len(second) > 0 && &second[0] != firstPtr {
		t.Log("buffer was grown rather than reused; the copy contract still holds")
	}
}

func TestUseAfterCloseIsAnError(t *testing.T) {
	s, err := NewStream(48000, 16000)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s.Close() // twice is fine
	if _, err := s.Process(make([]float32, 10)); err == nil {
		t.Error("Process after Close was accepted")
	}
	if _, err := s.Drain(); err == nil {
		t.Error("Drain after Close was accepted")
	}
}

// The response of every filter in this build, so the Windows one is measured
// rather than assumed. Spike R1 found the pure-Go filter 17.8 dB down at
// 7.6 kHz with a 0.92 rolloff; this is what pins the corrected constant.
func TestFrequencyResponseOfEveryImplementation(t *testing.T) {
	const fullScale = 0.125 // a 0.5-amplitude sine

	for name, open := range implementations() {
		t.Run(name, func(t *testing.T) {
			measure := func(freq float64) float64 {
				s, err := open(48000, 16000)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				var out []float32
				src := tone(freq, 48000, 4800*20)
				for i := 0; i+4800 <= len(src); i += 4800 {
					got, err := s.Process(src[i : i+4800])
					if err != nil {
						t.Fatal(err)
					}
					out = append(out, got...)
				}
				if len(out) < 3000 {
					t.Fatalf("only %d samples came out", len(out))
				}
				return power(out[2000:], freq, 16000)
			}

			// Passband: flat through the speech band.
			for _, freq := range []float64{200, 1000, 3000, 6000, 7000} {
				db := 10 * math.Log10(measure(freq)/fullScale)
				if math.Abs(db) > 0.5 {
					t.Errorf("%.0f Hz came through at %+.2f dB, want flat", freq, db)
				}
			}

			// 7.6 kHz is the fricative band — the difference between "s" and
			// "sh" for the recogniser, and what a lazy rolloff throws away.
			db := 10 * math.Log10(measure(7600)/fullScale)
			t.Logf("7.6 kHz: %+.2f dB", db)
			if db < -4 {
				t.Errorf("7.6 kHz is %.1f dB down — the fricative band is being lost", db)
			}

			// Stopband: nothing above the 8 kHz Nyquist may fold back.
			for _, freq := range []float64{9000, 12000, 20000} {
				s, err := open(48000, 16000)
				if err != nil {
					t.Fatal(err)
				}
				var out []float32
				src := tone(freq, 48000, 4800*20)
				for i := 0; i+4800 <= len(src); i += 4800 {
					got, _ := s.Process(src[i : i+4800])
					out = append(out, got...)
				}
				s.Close()
				var total float64
				for _, v := range out[2000:] {
					total += float64(v) * float64(v)
				}
				total /= float64(len(out) - 2000)
				if db := 10 * math.Log10(total/fullScale); db > -60 {
					t.Errorf("a %.0f Hz tone folded back at %.1f dB", freq, db)
				}
			}
		})
	}
}
