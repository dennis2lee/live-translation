package classify

// The MobileNet tower, in float with grid snapping.
//
// The reference interpreter runs this in int8. Running it here in float32
// with the weights dequantized would drift a little further at every layer;
// snapping each activation back to its stored int8 grid after ReLU6 collapses
// that drift to at most one grid step per layer, without implementing
// fixed-point requantization. Accumulation is float64, so the only remaining
// disagreement with exact integer arithmetic is a sum landing within rounding
// distance of a grid boundary — the golden test measures what that costs on
// real inputs and pins it.
//
// Padding is TensorFlow SAME: total = max((out-1)·stride + k − in, 0), split
// with the smaller half first. For stride 2 on even sizes that is asymmetric
// (0 before, 1 after) — the detail most hand-rolled convolutions get wrong.

import "math"

// Scores runs one 15600-sample window through the whole model.
// The returned slice has NumClasses entries in [0, 1].
func (m *Model) Scores(wave []float32) []float32 {
	if len(wave) != PatchSamples {
		panic("classify: Scores needs exactly PatchSamples samples")
	}
	patch := make([]float64, numFrames*melBands)
	m.logMelPatch(wave, patch)

	// Activations ping-pong between two buffers sized for the largest layer.
	h, w := numFrames, melBands
	cur := patch // [h*w*c] with c=1
	c := 1
	buf := make([]float64, 48*32*64) // large enough for every intermediate

	for i := range m.tower {
		l := &m.tower[i]
		oh := (h + l.stride - 1) / l.stride
		ow := (w + l.stride - 1) / l.stride
		var out []float64
		if l.dw {
			out = ensure(&buf, oh*ow*c)
			depthwise(l, cur, h, w, c, out, oh, ow)
		} else {
			out = ensure(&buf, oh*ow*l.cout)
			if l.kh == 1 && l.kw == 1 {
				pointwise(l, cur, h*w, c, out)
			} else {
				conv2d(l, cur, h, w, c, out, oh, ow)
			}
		}
		snap(out, l.outScale, l.outZero)
		cur, buf = out, remainderBuffer(cur, patch)
		h, w, c = oh, ow, l.cout
	}

	// Global average pool over what is left of H×W.
	pooled := make([]float64, c)
	area := float64(h * w)
	for i := 0; i < h*w; i++ {
		row := cur[i*c:]
		for ch := 0; ch < c; ch++ {
			pooled[ch] += row[ch]
		}
	}
	for ch := range pooled {
		pooled[ch] /= area
	}
	// The reference MEAN op emits int8 too.
	snap(pooled, m.poolScale, m.poolZero)

	// Classifier head: fc → snap logits to their coarse grid → sigmoid →
	// snap to the 1/256 output grid the DEQUANTIZE op produces.
	scores := make([]float32, NumClasses)
	for cls := 0; cls < NumClasses; cls++ {
		w := m.fcW[cls*len(pooled):]
		sum := float64(m.fcB[cls])
		for i, v := range pooled {
			sum += v * float64(w[i])
		}
		q := math.Round(sum/m.logitScale) + float64(m.logitZero)
		if q < -128 {
			q = -128
		} else if q > 127 {
			q = 127
		}
		logit := (q - float64(m.logitZero)) * m.logitScale
		sig := 1 / (1 + math.Exp(-logit))
		scores[cls] = float32(math.Round(sig*256)) / 256
		if scores[cls] > 255.0/256 {
			scores[cls] = 255.0 / 256
		}
	}
	return scores
}

// ensure returns a slice of exactly n values backed by *buf, growing it once
// if the first layers need more than the initial guess.
func ensure(buf *[]float64, n int) []float64 {
	if cap(*buf) < n {
		*buf = make([]float64, n)
	}
	return (*buf)[:n]
}

// remainderBuffer picks whichever previous buffer is free for reuse. The
// first iteration hands back the patch slice, which is never needed again.
func remainderBuffer(prev, patch []float64) []float64 {
	if &prev[0] == &patch[0] {
		return make([]float64, 0, 48*32*64)
	}
	return prev
}

// snap rounds every activation to its int8 grid, ReLU6 range included — the
// grid's own clamp at [-128, 127] is exactly [0, 6] once the zero point is
// applied, so no separate ReLU6 is needed.
func snap(x []float64, scale float64, zero int) {
	z := float64(zero)
	for i, v := range x {
		q := math.Round(v/scale) + z
		if q < -128 {
			q = -128
		} else if q > 127 {
			q = 127
		}
		x[i] = (q - z) * scale
	}
}

// samePad returns the leading pad for TensorFlow SAME padding.
func samePad(in, k, stride, out int) int {
	total := (out-1)*stride + k - in
	if total < 0 {
		total = 0
	}
	return total / 2
}

// conv2d is the full 3×3 convolution — only layer 1 runs it, on a 1-channel
// input, so clarity beats cleverness here.
func conv2d(l *layer, in []float64, h, w, cin int, out []float64, oh, ow int) {
	padH := samePad(h, l.kh, l.stride, oh)
	padW := samePad(w, l.kw, l.stride, ow)
	per := l.kh * l.kw * cin
	for oy := 0; oy < oh; oy++ {
		for ox := 0; ox < ow; ox++ {
			baseY := oy*l.stride - padH
			baseX := ox*l.stride - padW
			dst := out[(oy*ow+ox)*l.cout:]
			for co := 0; co < l.cout; co++ {
				k := l.w[co*per:]
				sum := float64(l.b[co])
				for ky := 0; ky < l.kh; ky++ {
					y := baseY + ky
					if y < 0 || y >= h {
						continue
					}
					for kx := 0; kx < l.kw; kx++ {
						x := baseX + kx
						if x < 0 || x >= w {
							continue
						}
						src := in[(y*w+x)*cin:]
						kk := k[(ky*l.kw+kx)*cin:]
						for ci := 0; ci < cin; ci++ {
							sum += src[ci] * float64(kk[ci])
						}
					}
				}
				dst[co] = sum
			}
		}
	}
}

// depthwise is the 3×3 per-channel convolution.
func depthwise(l *layer, in []float64, h, w, c int, out []float64, oh, ow int) {
	padH := samePad(h, l.kh, l.stride, oh)
	padW := samePad(w, l.kw, l.stride, ow)
	for oy := 0; oy < oh; oy++ {
		for ox := 0; ox < ow; ox++ {
			baseY := oy*l.stride - padH
			baseX := ox*l.stride - padW
			dst := out[(oy*ow+ox)*c:]
			for ch := 0; ch < c; ch++ {
				dst[ch] = float64(l.b[ch])
			}
			for ky := 0; ky < l.kh; ky++ {
				y := baseY + ky
				if y < 0 || y >= h {
					continue
				}
				for kx := 0; kx < l.kw; kx++ {
					x := baseX + kx
					if x < 0 || x >= w {
						continue
					}
					src := in[(y*w+x)*c:]
					k := l.w[(ky*l.kw+kx)*c:]
					for ch := 0; ch < c; ch++ {
						dst[ch] += src[ch] * float64(k[ch])
					}
				}
			}
		}
	}
}

// pointwise is the 1×1 convolution — a matmul over positions, and where
// nearly all the model's arithmetic happens.
func pointwise(l *layer, in []float64, positions, cin int, out []float64) {
	for p := 0; p < positions; p++ {
		src := in[p*cin:]
		dst := out[p*l.cout:]
		for co := 0; co < l.cout; co++ {
			k := l.w[co*cin:]
			sum := float64(l.b[co])
			for ci := 0; ci < cin; ci++ {
				sum += src[ci] * float64(k[ci])
			}
			dst[co] = sum
		}
	}
}
