// Pure-Go alternative to libsoxr: bandlimited interpolation with a
// Kaiser-windowed sinc. Arbitrary ratio, streaming, no cgo — the fallback the
// plan keeps in case bundling the dylib into the .app proves painful.
package main

import "math"

const (
	// Half-width in sinc zero crossings. The filter's support in *input*
	// samples is this divided by the cutoff, so decimating by 3 stretches
	// the same 32 crossings over ~104 input samples. Scaling the support
	// with the ratio is the whole difference between a usable stopband and
	// a useless one.
	firZeroCrossings = 32
	// Kaiser beta. 8.6 gives roughly -90 dB sidelobes.
	firBeta = 8.6
	// Sub-sample resolution of the precomputed filter table.
	firPhases = 512
	// Cutoff safety margin, so the transition band ends before Nyquist.
	firRolloff = 0.92
)

// FIRStream resamples mono int16 with a fixed rate ratio, keeping the tail of
// the previous block so block boundaries are seamless.
type FIRStream struct {
	step     float64 // input samples advanced per output sample
	cutoff   float64 // normalised to the input rate
	halfSpan int     // filter support either side, in input samples
	scale    float64 // filter table steps per input sample
	table    []float64

	history []float64 // the last 2*halfSpan input samples
	pos     float64   // fractional read position inside history+incoming
}

func besselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 40; k++ {
		term *= (x / 2) * (x / 2) / float64(k*k)
		sum += term
		if term < 1e-16*sum {
			break
		}
	}
	return sum
}

func NewFIRStream(inRate, outRate float64) *FIRStream {
	step := inRate / outRate
	cutoff := 0.5 * firRolloff
	if outRate < inRate {
		cutoff = 0.5 * firRolloff * outRate / inRate
	}
	// Support in input samples: 32 zero crossings of a sinc whose crossings
	// are 1/(2*cutoff) input samples apart.
	halfSpan := int(math.Ceil(firZeroCrossings / (2 * cutoff)))

	// The windowed sinc, sampled at firPhases points per input sample.
	n := halfSpan * firPhases
	table := make([]float64, n+2)
	denom := besselI0(firBeta)
	for i := 0; i <= n; i++ {
		t := float64(i) / firPhases // distance in input samples
		var s float64
		if t == 0 {
			s = 2 * cutoff
		} else {
			s = math.Sin(2*math.Pi*cutoff*t) / (math.Pi * t)
		}
		r := t / float64(halfSpan)
		w := 0.0
		if r <= 1 {
			w = besselI0(firBeta*math.Sqrt(1-r*r)) / denom
		}
		table[i] = s * w
	}
	return &FIRStream{
		step:     step,
		cutoff:   cutoff,
		halfSpan: halfSpan,
		scale:    firPhases,
		table:    table,
		history:  make([]float64, 2*halfSpan),
		pos:      float64(halfSpan),
	}
}

func (f *FIRStream) tap(t float64) float64 {
	if t < 0 {
		t = -t
	}
	idx := t * f.scale
	i := int(idx)
	if i >= len(f.table)-1 {
		return 0
	}
	frac := idx - float64(i)
	return f.table[i]*(1-frac) + f.table[i+1]*frac
}

// Process converts one block, carrying state across calls.
func (f *FIRStream) Process(in []int16) []int16 {
	// buf = history ++ in, so taps either side of pos are always available.
	buf := make([]float64, 0, len(f.history)+len(in))
	buf = append(buf, f.history...)
	for _, v := range in {
		buf = append(buf, float64(v)/32768.0)
	}

	// The last halfSpan samples cannot be centred on yet.
	limit := float64(len(buf) - f.halfSpan)
	out := make([]int16, 0, int(float64(len(in))/f.step)+2)
	for f.pos < limit {
		centre := int(f.pos)
		frac := f.pos - float64(centre)
		var acc float64
		for k := -f.halfSpan + 1; k <= f.halfSpan; k++ {
			j := centre + k
			if j < 0 || j >= len(buf) {
				continue
			}
			acc += buf[j] * f.tap(float64(k)-frac)
		}
		// h(t) = 2*cutoff*sinc(2*cutoff*t) already sums to unity at DC,
		// so there is nothing further to normalise.
		out = append(out, clampInt16(acc*32768.0))
		f.pos += f.step
	}

	// Keep the tail as the next call's history and rebase pos onto it.
	keep := 2 * f.halfSpan
	if keep > len(buf) {
		keep = len(buf)
	}
	f.history = append(f.history[:0], buf[len(buf)-keep:]...)
	f.pos -= float64(len(buf) - keep)
	return out
}

func clampInt16(v float64) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(math.Round(v))
}
