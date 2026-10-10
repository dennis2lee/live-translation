package audio

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestFreezeGateOnRecordedAudio runs the gate over a real recording of the
// model's audio, the sessions back to back with each one's start marked, as
// the player receives them, at ten alignments of the 10 ms grid. It skips
// without a recording; see TestReplayRecordedRun for how to make one.
//
// It checks what the gate is for: it finds openings, the same ones at every
// alignment, and it never cuts anywhere else.
func TestFreezeGateOnRecordedAudio(t *testing.T) {
	dir := os.Getenv("REPLAY_DIR")
	if dir == "" {
		t.Skip("REPLAY_DIR not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "s0*_t01.wav"))
	sort.Strings(files)
	var sessions [][]float32
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		pcm := wavData(raw)
		s := make([]float32, len(pcm)/2)
		for i := range s {
			s[i] = float32(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
		}
		sessions = append(sessions, s)
	}
	counts := map[int]int{}
	for off := 0; off < 10; off++ {
		g := newFreezeGate(TranslatedRate)
		in := make([]float32, TranslatedRate*off/1000)
		out := append([]float32(nil), g.Process(in)...)
		blob := TranslatedRate / 4
		var starts []int64
		for _, s := range sessions {
			g.MarkSessionStart()
			starts = append(starts, g.total)
			for i := 0; i < len(s); i += blob {
				out = append(out, g.Process(s[i:min(i+blob, len(s))])...)
			}
			in = append(in, s...)
		}
		tail := make([]float32, 2*TranslatedRate)
		out = append(out, g.Process(tail)...)
		in = append(in, tail...)
		counts[len(g.cutLog)]++

		if len(out) != len(in) {
			t.Fatalf("offset %d ms: %d of %d samples came out", off, len(out), len(in))
		}
		touched := make([]bool, len(in))
		for _, c := range g.cutLog {
			for i := max(0, c.from-int64(c.fade)); i < min(int64(len(in)), c.to+int64(g.fadeIn)); i++ {
				touched[i] = true
			}
		}
		for i := range in {
			if !touched[i] && out[i] != in[i] {
				t.Fatalf("offset %d ms: sample %d changed outside every cut", off, i)
			}
		}
		for i, c := range g.cutLog {
			onset := g.runStarts[i]
			opening := false
			for _, st := range starts {
				if onset >= st && onset-st <= int64(TranslatedRate*freezeStartMS/1000) {
					opening = true
				}
			}
			if !opening {
				t.Errorf("offset %d ms: cut at %.2f s, which is not a session's opening",
					off, float64(onset)/TranslatedRate)
				continue
			}
			from := frozenOnset(in, c)
			if left := energy(out[from:c.to]) / energy(in[from:c.to]); left > 0.01 {
				t.Errorf("offset %d ms: %.1f%% of the freeze at %.2f s still plays",
					off, 100*left, float64(from)/TranslatedRate)
			}
		}
	}
	t.Logf("cuts per alignment: %v", counts)
	if len(counts) != 1 {
		t.Errorf("the number of openings found depends on the alignment: %v", counts)
	}
	if counts[0] > 0 {
		t.Error("no opening freeze found in the recording at all")
	}
}

// frozenOnset finds where a cut's freeze really begins, by a method that owes
// nothing to the gate: take the pitch period from the middle of the cut, then
// walk back one period at a time while each period is a near copy of the one
// after it (normalised cross-correlation of at least 0.99).
func frozenOnset(x []float32, c cut) int64 {
	mid := int((c.from + c.to) / 2)
	lo, hi := TranslatedRate/500, TranslatedRate/70
	best, period := -1.0, lo
	for p := lo; p < hi; p++ {
		if v := ncc(x, mid-p, mid, p); v > best {
			best, period = v, p
		}
	}
	i := mid
	for i-2*period >= 0 && ncc(x, i-2*period, i-period, period) >= 0.99 {
		i -= period
	}
	return int64(i - period)
}

func ncc(x []float32, a, b, n int) float64 {
	if a < 0 || b+n > len(x) {
		return 0
	}
	var ab, aa, bb float64
	for k := 0; k < n; k++ {
		u, v := float64(x[a+k]), float64(x[b+k])
		ab += u * v
		aa += u * u
		bb += v * v
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return ab / math.Sqrt(aa*bb)
}
