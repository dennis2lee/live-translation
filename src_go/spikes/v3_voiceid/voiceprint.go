package main

import (
	"math"
	"sort"
)

// A voice fingerprint, and why median F0 alone was not enough.
//
// The 1.2.0 study measured SpeechConfig by median F0 and concluded the voice
// could not be chosen. That conclusion still stands, but the *reason* it looked
// noisy was read wrongly: the control runs varied as much as the treatment runs
// did, and variance in the control was filed as measurement error. If the model
// picks a voice per session, a control that varies is not noise — it is the
// finding. Telling those two apart needs a fingerprint that is stable inside
// one voice, so this adds two features that move together for a person and
// independently for a microphone:
//
//   - f0Median / f0IQR — how high the voice sits and how much it moves.
//   - centroid — where the energy sits in the spectrum. Timbre, roughly. Two
//     speakers can share a pitch and still differ here.
//
// None of this is a speaker-identification system. It is enough to say "these
// two clips are not the same voice" with the WAVs available to confirm by ear.
type voicePrint struct {
	seconds  float64
	voiced   float64 // fraction of frames with a usable pitch
	f0Median float64
	f0IQR    float64
	centroid float64
}

const (
	frameSize = 1024 // 42.7 ms at 24 kHz
	hopSize   = 512
	minF0     = 60.0
	maxF0     = 400.0
	// voicingFloor is the autocorrelation peak ratio below which a frame is
	// called unvoiced. Consonants and silence land under it.
	voicingFloor = 0.3
)

// fingerprint reduces mono PCM16 to the features above.
func fingerprint(pcm []byte, rate int) voicePrint {
	samples := toFloat(pcm)
	vp := voicePrint{seconds: float64(len(samples)) / float64(rate)}
	if len(samples) < frameSize {
		return vp
	}

	// The energy floor is relative, not absolute: the model's output level is
	// not guaranteed and a fixed threshold would call a quiet clip silent.
	var sumSq float64
	for _, s := range samples {
		sumSq += s * s
	}
	rmsAll := math.Sqrt(sumSq / float64(len(samples)))
	floor := rmsAll * 0.2

	window := hann(frameSize)
	var f0s, centroids []float64
	frames := 0
	for start := 0; start+frameSize <= len(samples); start += hopSize {
		frame := samples[start : start+frameSize]
		frames++
		if rms(frame) < floor {
			continue
		}
		f0, clarity := pitch(frame, rate)
		if clarity < voicingFloor || f0 <= 0 {
			continue
		}
		f0s = append(f0s, f0)
		centroids = append(centroids, spectralCentroid(frame, window, rate))
	}
	if frames > 0 {
		vp.voiced = float64(len(f0s)) / float64(frames)
	}
	if len(f0s) == 0 {
		return vp
	}
	sort.Float64s(f0s)
	vp.f0Median = quantile(f0s, 0.5)
	vp.f0IQR = quantile(f0s, 0.75) - quantile(f0s, 0.25)
	sort.Float64s(centroids)
	vp.centroid = quantile(centroids, 0.5)
	return vp
}

// voiceChangeThreshold is where "the same voice, said differently" stops and
// "a different voice" starts. It is calibrated, not guessed.
//
// macOS `say` produced 39 s from one voice, and a second file that switched
// Fred -> Kathy -> Fred at known times. Distances between neighbouring 5 s
// windows:
//
//	within one voice          0.14 .. 1.16   (8 windows)
//	at a real voice change    7.76, 10.69    (both boundaries, no others)
//
// There is more than a factor of six of empty space between those, so the
// threshold sits in the middle of it. Anything under this is how one voice
// varies across a sentence; anything over it was a different speaker.
const voiceChangeThreshold = 3.0

// distance is a same-or-different score. Each feature is divided by a spread
// that one voice stays inside, so the two features contribute comparably
// instead of pitch drowning out timbre.
func distance(a, b voicePrint) float64 {
	d := func(x, y, spread float64) float64 { return math.Abs(x-y) / spread }
	return d(a.f0Median, b.f0Median, 12) + // Hz
		d(a.centroid, b.centroid, 250) // Hz
}

func toFloat(pcm []byte) []float64 {
	out := make([]float64, len(pcm)/2)
	for i := range out {
		v := int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8)
		out[i] = float64(v) / 32768.0
	}
	return out
}

func rms(x []float64) float64 {
	var s float64
	for _, v := range x {
		s += v * v
	}
	return math.Sqrt(s / float64(len(x)))
}

func hann(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
	}
	return w
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

// pitch estimates F0 by autocorrelation, returning the frequency and the peak
// ratio that says how periodic the frame actually was.
func pitch(frame []float64, rate int) (f0, clarity float64) {
	x := make([]float64, len(frame))
	var mean float64
	for _, v := range frame {
		mean += v
	}
	mean /= float64(len(frame))
	for i, v := range frame {
		x[i] = v - mean
	}

	minLag := rate / int(maxF0)
	maxLag := rate / int(minF0)
	if maxLag >= len(x) {
		maxLag = len(x) - 1
	}
	if minLag < 1 || minLag >= maxLag {
		return 0, 0
	}

	var r0 float64
	for _, v := range x {
		r0 += v * v
	}
	if r0 == 0 {
		return 0, 0
	}

	bestLag, best := 0, 0.0
	for lag := minLag; lag <= maxLag; lag++ {
		var sum float64
		for i := 0; i+lag < len(x); i++ {
			sum += x[i] * x[i+lag]
		}
		if sum > best {
			best, bestLag = sum, lag
		}
	}
	if bestLag == 0 {
		return 0, 0
	}
	return float64(rate) / float64(bestLag), best / r0
}

// spectralCentroid is the energy-weighted mean frequency of one frame.
func spectralCentroid(frame, window []float64, rate int) float64 {
	re := make([]float64, frameSize)
	im := make([]float64, frameSize)
	for i := range frame {
		re[i] = frame[i] * window[i]
	}
	fft(re, im)

	var num, den float64
	for k := 1; k < frameSize/2; k++ {
		mag := math.Hypot(re[k], im[k])
		freq := float64(k) * float64(rate) / float64(frameSize)
		num += freq * mag
		den += mag
	}
	if den == 0 {
		return 0
	}
	return num / den
}

// fft is an in-place iterative radix-2 transform. frameSize is a power of two.
func fft(re, im []float64) {
	n := len(re)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j |= bit
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}
	for length := 2; length <= n; length <<= 1 {
		angle := -2 * math.Pi / float64(length)
		wr, wi := math.Cos(angle), math.Sin(angle)
		for i := 0; i < n; i += length {
			cr, ci := 1.0, 0.0
			for j := 0; j < length/2; j++ {
				ur, ui := re[i+j], im[i+j]
				vr := re[i+j+length/2]*cr - im[i+j+length/2]*ci
				vi := re[i+j+length/2]*ci + im[i+j+length/2]*cr
				re[i+j], im[i+j] = ur+vr, ui+vi
				re[i+j+length/2], im[i+j+length/2] = ur-vr, ui-vi
				cr, ci = cr*wr-ci*wi, cr*wi+ci*wr
			}
		}
	}
}
