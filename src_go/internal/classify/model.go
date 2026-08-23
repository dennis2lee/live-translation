package classify

// Extraction of the YAMNet graph from the pinned .tflite file.
//
// This is not a general model loader. The file is pinned by SHA-256, its
// architecture is written down here as a fixed table, and only the weight and
// constant tensors are pulled from the flatbuffer — by exact name, with dtype
// and presence-of-data as the check. If Google ever publishes a different
// export, the hash pin fails first, loudly, before any name lookup can go
// quietly wrong.
//
// What the graph is (dumped from the file itself, spike Y1, 2026-08-23):
//
//	waveform [15600] float32
//	  framing 400/160 → hann → pad 512 → rfft → |·|      (frontend, float)
//	  → mel matmul [64×257] + 0.001 → ln → patch [96×64]
//	  → quantize (scale 0.0480206, zp 16)                 (int8 from here)
//	  → MobileNet v1: conv s2 + 13 × (dwconv, pwconv)     (per-channel int8)
//	  → mean over H,W → fc [521×1024] → sigmoid → dequant
//
// The tower is run here in float32, with weights dequantized at load and every
// activation snapped back to its stored int8 grid (scale, zero point) after
// ReLU6. That reproduces the reference interpreter's arithmetic to within one
// activation grid step without implementing fixed-point requantization; the
// golden tests in model_test.go measure the residual and pin it.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
)

// ModelSHA256 pins the one file this package can load: 1.tflite from the
// yamnet/tflite/classification-tflite/1 bundle, 4 126 810 bytes. Measured
// twice from the canonical Kaggle download (spike Y1); the bundle itself is
// byte-identical across fetches.
const ModelSHA256 = "10c95ea3eb9a7bb4cb8bddf6feb023250381008177ac162ce169694d05c317de"

// Input geometry, fixed by the model.
const (
	SampleRate = 16000
	// PatchSamples is one classifier input: 0.975 s.
	PatchSamples = 15600

	frameLen  = 400 // 25 ms
	frameHop  = 160 // 10 ms
	fftLen    = 512
	numFrames = 96 // (15600-400)/160 + 1
	melBands  = 64
	numBins   = fftLen/2 + 1 // 257

	// NumClasses is the AudioSet class count.
	NumClasses = 521
)

// layerSpec is one row of the fixed architecture table. Strides are written
// down rather than parsed out of operator options — the hash pin makes the
// table checkable once and true forever.
type layerSpec struct {
	n      int // layer number in the tensor names
	dw     bool
	stride int
}

var towerSpec = []layerSpec{
	{1, false, 2},
	{2, true, 1}, {3, false, 1},
	{4, true, 2}, {5, false, 1},
	{6, true, 1}, {7, false, 1},
	{8, true, 2}, {9, false, 1},
	{10, true, 1}, {11, false, 1},
	{12, true, 2}, {13, false, 1},
	{14, true, 1}, {15, false, 1},
	{16, true, 1}, {17, false, 1},
	{18, true, 1}, {19, false, 1},
	{20, true, 1}, {21, false, 1},
	{22, true, 1}, {23, false, 1},
	{24, true, 2}, {25, false, 1},
	{26, true, 1}, {27, false, 1},
}

// layer is one convolution with everything inference needs, dequantized.
type layer struct {
	dw        bool
	stride    int
	kh, kw    int
	cin, cout int
	// w layout follows the file: conv [cout][kh][kw][cin], dw [kh][kw][c].
	w []float32
	b []float32
	// outScale/outZero: the int8 grid of this layer's ReLU6 output, for
	// snapping.
	outScale float64
	outZero  int
}

// Model is a loaded YAMNet, ready for Scores.
type Model struct {
	hann   []float32 // [400]
	mel    []float32 // [64*257], row per band
	melOff []float32 // [64], the 0.001 offset

	patchScale float64 // the int8 grid of the log-mel patch
	patchZero  int

	tower []layer

	fcW        []float32 // [521*1024]
	fcB        []float32 // [521]
	poolScale  float64   // the int8 grid of the pooled embedding
	poolZero   int
	logitScale float64
	logitZero  int
}

// LoadModel reads and verifies the pinned model file.
func LoadModel(path string) (*Model, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != ModelSHA256 {
		return nil, fmt.Errorf("model file %s is not the pinned YAMNet export (sha256 %s)", path, got)
	}
	tensors, err := readTensors(raw)
	if err != nil {
		return nil, err
	}
	return extract(tensors)
}

// byName indexes the tensor list for the exact-name lookups below.
type byName map[string][]*tensorInfo

func indexTensors(tensors []tensorInfo) byName {
	m := make(byName, len(tensors))
	for i := range tensors {
		t := &tensors[i]
		m[t.name] = append(m[t.name], t)
	}
	return m
}

// one returns the single tensor with this exact name, requiring the given
// dtype and that it does (or does not) carry constant data.
func (m byName) one(name string, dtype byte, wantData bool) (*tensorInfo, error) {
	list := m[name]
	var found *tensorInfo
	for _, t := range list {
		if t.dtype == dtype && (len(t.data) > 0) == wantData {
			if found != nil {
				return nil, fmt.Errorf("tensor %q: ambiguous", name)
			}
			found = t
		}
	}
	if found == nil {
		return nil, fmt.Errorf("tensor %q (dtype %d, data=%v): not found", name, dtype, wantData)
	}
	return found, nil
}

// prefixed returns the single tensor whose name starts with prefix, with the
// given dtype/rank, carrying data. Needed where the exporter fused names: the
// depthwise weight tensor's name is the bias tensor's name plus a ";…" tail.
func (m byName) prefixed(all []tensorInfo, prefix string, dtype byte, rank int) (*tensorInfo, error) {
	var found *tensorInfo
	for i := range all {
		t := &all[i]
		if t.dtype != dtype || len(t.data) == 0 || len(t.shape) != rank {
			continue
		}
		if len(t.name) < len(prefix) || t.name[:len(prefix)] != prefix {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("prefix %q: ambiguous", prefix)
		}
		found = t
	}
	if found == nil {
		return nil, fmt.Errorf("prefix %q (dtype %d, rank %d): not found", prefix, dtype, rank)
	}
	return found, nil
}

const (
	dtF32  = 0
	dtI32  = 2
	dtInt8 = 9
)

func extract(tensors []tensorInfo) (*Model, error) {
	names := indexTensors(tensors)
	model := &Model{}

	// --- frontend constants, straight from the graph ----------------------
	// Taking the mel matrix and window from the file rather than re-deriving
	// them removes the entire "does my mel formula match theirs" class of bug.
	hann, err := names.one("stft/hann_window/sub_2", dtF32, true)
	if err != nil {
		return nil, err
	}
	if model.hann, err = hann.f32(); err != nil {
		return nil, err
	}
	mel, err := names.one("mel_spectrogram", dtF32, true)
	if err != nil {
		return nil, err
	}
	if model.mel, err = mel.f32(); err != nil {
		return nil, err
	}
	off, err := names.one("add", dtF32, true)
	if err != nil {
		return nil, err
	}
	if model.melOff, err = off.f32(); err != nil {
		return nil, err
	}
	if len(model.hann) != frameLen || len(model.mel) != melBands*numBins || len(model.melOff) != melBands {
		return nil, fmt.Errorf("frontend constants have unexpected shapes: %d %d %d",
			len(model.hann), len(model.mel), len(model.melOff))
	}

	patchQ, err := names.one("tfl.quantize", dtInt8, false)
	if err != nil {
		return nil, err
	}
	if len(patchQ.scales) != 1 {
		return nil, fmt.Errorf("patch quantize: %d scales", len(patchQ.scales))
	}
	model.patchScale = float64(patchQ.scales[0])
	model.patchZero = int(patchQ.zeros[0])

	// --- the tower --------------------------------------------------------
	for _, spec := range towerSpec {
		l := layer{dw: spec.dw, stride: spec.stride}
		kind := "conv"
		if spec.dw {
			kind = "sepconv"
		}
		base := fmt.Sprintf("tower0/network/layer%d/%s/", spec.n, kind)

		var wT *tensorInfo
		if spec.dw {
			// The fused export gave the weight tensor the bias tensor's name
			// plus a tail; prefix matching with rank 4 picks the weight.
			wT, err = names.prefixed(tensors, base+"BatchNorm/FusedBatchNormV3;", dtInt8, 4)
		} else {
			wT, err = names.one(base+"Conv2D", dtInt8, true)
		}
		if err != nil {
			return nil, err
		}
		bT, err := names.one(base+"BatchNorm/FusedBatchNormV3", dtI32, true)
		if err != nil {
			return nil, err
		}
		actT, err := activationByPrefix(tensors, base+"Relu6")
		if err != nil {
			return nil, err
		}

		if err := l.fill(wT, bT, actT); err != nil {
			return nil, fmt.Errorf("layer%d: %w", spec.n, err)
		}
		model.tower = append(model.tower, l)
	}

	// --- classifier head --------------------------------------------------
	fcW, err := names.one("tower0/network/layer29/fc/MatMul", dtInt8, true)
	if err != nil {
		return nil, err
	}
	fcB, err := names.one("network/layer29/fc/biases", dtI32, true)
	if err != nil {
		return nil, err
	}
	if len(fcW.shape) != 2 || fcW.shape[0] != NumClasses {
		return nil, fmt.Errorf("fc weights shape %v", fcW.shape)
	}
	model.fcW = dequantPerTensor(fcW)
	model.fcB = dequantBias(fcB)

	pooled, err := activationByPrefix(tensors, "tower0/network/layer28/reduce_mean")
	if err != nil {
		return nil, err
	}
	model.poolScale = float64(pooled.scales[0])
	model.poolZero = int(pooled.zeros[0])

	logits, err := activationByPrefix(tensors, "tower0/network/layer29/fc/MatMul;")
	if err != nil {
		return nil, err
	}
	model.logitScale = float64(logits.scales[0])
	model.logitZero = int(logits.zeros[0])

	return model, nil
}

// activationByPrefix finds the single data-less int8 tensor with this prefix.
func activationByPrefix(tensors []tensorInfo, prefix string) (*tensorInfo, error) {
	var found *tensorInfo
	for i := range tensors {
		t := &tensors[i]
		if t.dtype != dtInt8 || len(t.data) > 0 {
			continue
		}
		if len(t.name) < len(prefix) || t.name[:len(prefix)] != prefix {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("activation prefix %q: ambiguous", prefix)
		}
		found = t
	}
	if found == nil || len(found.scales) != 1 {
		return nil, fmt.Errorf("activation prefix %q: not found", prefix)
	}
	return found, nil
}

// fill dequantizes one layer's tensors.
func (l *layer) fill(wT, bT, actT *tensorInfo) error {
	if len(wT.shape) != 4 {
		return fmt.Errorf("weight rank %d", len(wT.shape))
	}
	if l.dw {
		// [1, kh, kw, c], per-channel scales on axis 3.
		l.kh, l.kw = int(wT.shape[1]), int(wT.shape[2])
		l.cin = int(wT.shape[3])
		l.cout = l.cin
		if int(wT.quantDim) != 3 || len(wT.scales) != l.cout {
			return fmt.Errorf("dw quant: dim %d, %d scales for %d channels", wT.quantDim, len(wT.scales), l.cout)
		}
		n := l.kh * l.kw * l.cin
		l.w = make([]float32, n)
		for i := 0; i < n; i++ {
			c := i % l.cin
			l.w[i] = wT.scales[c] * float32(int8(wT.data[i]))
		}
	} else {
		// [cout, kh, kw, cin], per-channel scales on axis 0.
		l.cout = int(wT.shape[0])
		l.kh, l.kw = int(wT.shape[1]), int(wT.shape[2])
		l.cin = int(wT.shape[3])
		if int(wT.quantDim) != 0 || len(wT.scales) != l.cout {
			return fmt.Errorf("conv quant: dim %d, %d scales for %d filters", wT.quantDim, len(wT.scales), l.cout)
		}
		per := l.kh * l.kw * l.cin
		l.w = make([]float32, l.cout*per)
		for co := 0; co < l.cout; co++ {
			s := wT.scales[co]
			for i := 0; i < per; i++ {
				l.w[co*per+i] = s * float32(int8(wT.data[co*per+i]))
			}
		}
	}
	l.b = dequantBias(bT)
	if len(l.b) != l.cout {
		return fmt.Errorf("%d biases for %d channels", len(l.b), l.cout)
	}
	l.outScale = float64(actT.scales[0])
	l.outZero = int(actT.zeros[0])
	return nil
}

// dequantBias decodes an int32 bias tensor with per-channel scales. The stored
// scale already is inputScale × weightScale, which is what makes this a plain
// multiply.
func dequantBias(t *tensorInfo) []float32 {
	n := t.elems()
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		v := int32(uint32(t.data[4*i]) | uint32(t.data[4*i+1])<<8 |
			uint32(t.data[4*i+2])<<16 | uint32(t.data[4*i+3])<<24)
		s := t.scales[0]
		if len(t.scales) == n {
			s = t.scales[i]
		}
		out[i] = s * float32(v)
	}
	return out
}

// dequantPerTensor decodes an int8 tensor with a single scale.
func dequantPerTensor(t *tensorInfo) []float32 {
	out := make([]float32, len(t.data))
	s := t.scales[0]
	for i, b := range t.data {
		out[i] = s * float32(int8(b))
	}
	return out
}
