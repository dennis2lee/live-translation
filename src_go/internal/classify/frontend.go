package classify

// The log-mel frontend: waveform to the 96×64 patch the tower consumes.
//
// Everything data-like — the Hann window, the mel matrix, the 0.001 offset,
// the quantization grid — comes out of the model file itself (model.go), so
// the only things implemented here are framing, an FFT, a matmul and a log.
// The golden test compares the result against the reference interpreter's
// feature_patch tensor for the same synthetic inputs.

import "math"

// logMelPatch computes the [numFrames][melBands] patch for one 15600-sample
// window, snapped to the model's int8 grid the way the QUANTIZE op snaps it.
// dst must hold numFrames*melBands values; scratch buffers live in c so a
// streaming caller allocates nothing per patch.
func (m *Model) logMelPatch(wave []float32, dst []float64) {
	var re, im [fftLen]float64
	var mag [numBins]float64

	for f := 0; f < numFrames; f++ {
		start := f * frameHop
		// Window and zero-pad. 400 real samples into a 512 FFT.
		for i := 0; i < frameLen; i++ {
			re[i] = float64(wave[start+i]) * float64(m.hann[i])
			im[i] = 0
		}
		for i := frameLen; i < fftLen; i++ {
			re[i], im[i] = 0, 0
		}
		fft512(&re, &im)
		for k := 0; k < numBins; k++ {
			mag[k] = math.Hypot(re[k], im[k])
		}

		// Mel projection plus the 0.001 floor, then the natural log — the
		// graph's FULLY_CONNECTED + LOG pair.
		row := dst[f*melBands:]
		for b := 0; b < melBands; b++ {
			w := m.mel[b*numBins:]
			sum := float64(m.melOff[b])
			for k := 0; k < numBins; k++ {
				sum += mag[k] * float64(w[k])
			}
			logMel := math.Log(sum)
			// Snap to the patch's int8 grid, replicating the reference's
			// QUANTIZE op. Kept for fidelity, not for effect — both facts
			// measured: the clamps never bind on audio (log-mel spans about
			// −6.91..+3.5 against a grid of −6.92..+5.33, full-scale input
			// included), and removing the rounding moves no golden score past
			// tolerance. It is the one step that makes the tower's input
			// bit-identical to the interpreter's, and it costs a multiply.
			q := math.Round(logMel/m.patchScale) + float64(m.patchZero)
			if q < -128 {
				q = -128
			} else if q > 127 {
				q = 127
			}
			row[b] = (q - float64(m.patchZero)) * m.patchScale
		}
	}
}

// rawLogMel is logMelPatch without the grid snap, for the frontend golden
// test, which compares against the pre-quantization feature_patch tensor.
func (m *Model) rawLogMel(wave []float32, dst []float64) {
	var re, im [fftLen]float64
	var mag [numBins]float64
	for f := 0; f < numFrames; f++ {
		start := f * frameHop
		for i := 0; i < frameLen; i++ {
			re[i] = float64(wave[start+i]) * float64(m.hann[i])
			im[i] = 0
		}
		for i := frameLen; i < fftLen; i++ {
			re[i], im[i] = 0, 0
		}
		fft512(&re, &im)
		for k := 0; k < numBins; k++ {
			mag[k] = math.Hypot(re[k], im[k])
		}
		row := dst[f*melBands:]
		for b := 0; b < melBands; b++ {
			w := m.mel[b*numBins:]
			sum := float64(m.melOff[b])
			for k := 0; k < numBins; k++ {
				sum += mag[k] * float64(w[k])
			}
			row[b] = math.Log(sum)
		}
	}
}

// fft512 is an in-place iterative radix-2 FFT, fixed at 512 points. The same
// shape as the renderer's chime FFT, but sized by constant so the compiler
// can keep the loop bounds concrete.
func fft512(re, im *[fftLen]float64) {
	const n = fftLen
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
			half := length / 2
			for j := 0; j < half; j++ {
				ur, ui := re[i+j], im[i+j]
				vr := re[i+j+half]*cr - im[i+j+half]*ci
				vi := re[i+j+half]*ci + im[i+j+half]*cr
				re[i+j], im[i+j] = ur+vr, ui+vi
				re[i+j+half], im[i+j+half] = ur-vr, ui-vi
				cr, ci = cr*wr-ci*wi, cr*wi+ci*wr
			}
		}
	}
}
