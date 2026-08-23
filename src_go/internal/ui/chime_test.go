package ui

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
)

// peakAndRMS walks a whole pattern at the real output rate.
func peakAndRMS() (peak, rms float64) {
	n := int(chimePeriod * audio.TranslatedRate)
	var sumSq float64
	for i := 0; i < n; i++ {
		v := chimeAt(float64(i)/audio.TranslatedRate) * chimeAmplitude
		if a := math.Abs(v); a > peak {
			peak = a
		}
		sumSq += v * v
	}
	return peak, math.Sqrt(sumSq / float64(n))
}

// Four notes ring together, so the level is not one note's amplitude. If this
// ever fails the clamp in fillChime starts doing the work, and a clipped chime
// is the harsh sound this was meant to get away from.
func TestChimeDoesNotClip(t *testing.T) {
	peak, _ := peakAndRMS()
	if peak >= 1.0 {
		t.Errorf("peak %.3f reaches full scale — chimeAmplitude is too high", peak)
	}
	if peak > 0.9 {
		t.Errorf("peak %.3f leaves no headroom for a device already near clipping", peak)
	}
	t.Logf("measured peak %.3f", peak)
}

func TestChimeIsAudible(t *testing.T) {
	peak, rms := peakAndRMS()
	if peak < 0.05 {
		t.Errorf("peak %.3f — too quiet to confirm a device works", peak)
	}
	// Most of the pattern is deliberately silence, so the RMS is low by
	// design; this only catches a chime that is silent everywhere.
	if rms < 0.005 {
		t.Errorf("rms %.4f — the pattern is effectively silent", rms)
	}
	t.Logf("measured peak %.3f, rms %.4f", peak, rms)
}

// A loop that restarts on a non-zero sample clicks once per period, which is
// exactly the appliance noise the chime replaced.
func TestChimeLoopsWithoutAClick(t *testing.T) {
	if v := math.Abs(chimeAt(0)); v > 0.001 {
		t.Errorf("pattern starts at %.4f, not silence — the attack is not rounded", v)
	}
	last := math.Abs(chimeAt(chimePeriod - 1.0/audio.TranslatedRate))
	if last > 0.01 {
		t.Errorf("pattern ends at %.4f — the tail is cut off and will click", last)
	}
	t.Logf("ends at %.5f", last)
}

// The position is threaded through fillChime by hand, so the join between two
// blocks is where it would break. Two blocks must equal one block of twice the
// length, sample for sample.
func TestFillChimeIsContinuousAcrossBlocks(t *testing.T) {
	const samples = 2400 // 100 ms, the shape the network delivers

	whole := make([]byte, samples*2*2)
	fillChime(whole, audio.TranslatedRate, 0)

	first := make([]byte, samples*2)
	second := make([]byte, samples*2)
	pos := fillChime(first, audio.TranslatedRate, 0)
	fillChime(second, audio.TranslatedRate, pos)

	if pos != samples {
		t.Fatalf("fillChime advanced to %d after %d samples", pos, samples)
	}
	joined := append(append([]byte(nil), first...), second...)
	for i := range whole {
		if whole[i] != joined[i] {
			t.Fatalf("block join differs at byte %d (sample %d)", i, i/2)
		}
	}
}

// One period later is the same sound, or the pattern drifts instead of looping.
func TestFillChimeRepeatsExactly(t *testing.T) {
	period := int64(chimePeriod * audio.TranslatedRate)
	const samples = 480

	a := make([]byte, samples*2)
	b := make([]byte, samples*2)
	fillChime(a, audio.TranslatedRate, 0)
	fillChime(b, audio.TranslatedRate, period)

	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("the pattern does not repeat: byte %d differs", i)
		}
	}
}

// Every note must actually be struck. A frequency table that is never read is
// the kind of thing that survives a refactor unnoticed.
func TestEveryNoteSounds(t *testing.T) {
	for _, n := range chimeNotes {
		// Just after the attack has risen, this note dominates the sum.
		before := math.Abs(chimeAt(n.at - 0.001))
		after := math.Abs(chimeAt(n.at + 0.02))
		if after <= before {
			t.Errorf("note at %.2fs (%.0f Hz) did not raise the level: %.4f -> %.4f",
				n.at, n.freq, before, after)
		}
	}
}

// The buffer really receives the waveform, not zeroes.
func TestFillChimeWritesTheWaveform(t *testing.T) {
	pcm := make([]byte, 4800)
	fillChime(pcm, audio.TranslatedRate, 0)
	var peak int16
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int16(binary.LittleEndian.Uint16(pcm[i : i+2]))
		if v > peak {
			peak = v
		}
	}
	if peak < 1000 {
		t.Errorf("peak sample %d — the buffer is essentially silent", peak)
	}
}
