package resample

import "math"

// The pure-Go filter: bandlimited interpolation with a Kaiser-windowed sinc.
//
// Spike R1 measured its first version as unusable and the cause as a mistake
// rather than a limit of the method: the filter's support has to be scaled by
// the decimation ratio, or decimating by three leaves the filter only a third
// as long as it needs to be. With that fixed it reaches a −91 dB stopband at
// well under 1% of realtime.
const (
	// firZeroCrossings is the half-width in sinc zero crossings. The support in
	// *input* samples is this divided by the cutoff, so decimating 48 k → 16 k
	// stretches the same crossings over about 104 input samples.
	firZeroCrossings = 32
	// firBeta is the Kaiser parameter. 8.6 gives roughly −90 dB sidelobes.
	firBeta = 8.6
	// firPhases is the sub-sample resolution of the precomputed table.
	firPhases = 512
	// firRolloff sets where the transition band starts, as a fraction of the
	// output Nyquist. R1 measured 0.92 as costing 17.8 dB at 7.6 kHz — the
	// fricative band, which decides "s" from "sh" for the recogniser. 0.97
	// moves the transition up to where libsoxr puts it; the cost is a slightly
	// steeper filter, which the same 32 crossings absorb.
	firRolloff = 0.97

	firName = "pure-Go Kaiser-windowed sinc"
)

// firStream resamples mono float32 with a fixed rate ratio, keeping the tail of
// the previous block so block boundaries are seamless.
type firStream struct {
	step     float64 // input samples advanced per output sample
	cutoff   float64 // normalised to the input rate
	halfSpan int     // filter support either side, in input samples
	table    []float64

	history []float64 // the last 2*halfSpan input samples
	pos     float64   // fractional read position inside history+incoming
	buf     []float64 // history ++ this block; reused, see Process
	out     []float32 // reused across calls; the callback must not allocate
	closed  bool
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

func newFIRStream(inRate, outRate float64) *firStream {
	cutoff := 0.5 * firRolloff
	if outRate < inRate {
		cutoff = 0.5 * firRolloff * outRate / inRate
	}
	halfSpan := int(math.Ceil(firZeroCrossings / (2 * cutoff)))

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
		w := 0.0
		if r := t / float64(halfSpan); r <= 1 {
			w = besselI0(firBeta*math.Sqrt(1-r*r)) / denom
		}
		table[i] = s * w
	}
	return &firStream{
		step:     inRate / outRate,
		cutoff:   cutoff,
		halfSpan: halfSpan,
		table:    table,
		history:  make([]float64, 2*halfSpan),
		pos:      float64(halfSpan),
	}
}

func (f *firStream) tap(t float64) float64 {
	if t < 0 {
		t = -t
	}
	idx := t * firPhases
	i := int(idx)
	if i >= len(f.table)-1 {
		return 0
	}
	frac := idx - float64(i)
	return f.table[i]*(1-frac) + f.table[i+1]*frac
}

// Process converts one block, carrying state across calls.
func (f *firStream) Process(in []float32) ([]float32, error) {
	if f.closed {
		return nil, errClosed
	}
	// buf = history ++ in, so taps either side of pos are always available.
	//
	// Reused rather than freshly allocated: this runs on the device callback
	// thread about fifty times a second, and the package's own rule is that the
	// callback allocates nothing. It used to `make` here every call. The slice
	// settles at its working size after the first block and never grows again,
	// since every block is the same length.
	buf := append(f.buf[:0], f.history...)
	for _, v := range in {
		buf = append(buf, float64(v))
	}
	f.buf = buf

	// The last halfSpan samples cannot be centred on yet.
	limit := float64(len(buf) - f.halfSpan)
	want := int(float64(len(in))/f.step) + 2
	if cap(f.out) < want {
		f.out = make([]float32, 0, want)
	}
	f.out = f.out[:0]

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
		// h(t) = 2*cutoff*sinc(2*cutoff*t) already sums to unity at DC, so
		// there is nothing further to normalise.
		f.out = append(f.out, float32(acc))
		f.pos += f.step
	}

	// Keep the tail as the next call's history and rebase pos onto it.
	keep := min(2*f.halfSpan, len(buf))
	f.history = append(f.history[:0], buf[len(buf)-keep:]...)
	f.pos -= float64(len(buf) - keep)
	return f.out, nil
}

// Drain flushes the filter's remaining delay by feeding it silence.
func (f *firStream) Drain() ([]float32, error) {
	if f.closed {
		return nil, errClosed
	}
	out, err := f.Process(make([]float32, f.halfSpan))
	if err != nil {
		return nil, err
	}
	return append([]float32(nil), out...), nil
}

func (f *firStream) Close() { f.closed = true }
