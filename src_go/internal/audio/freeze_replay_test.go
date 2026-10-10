package audio

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestFreezeGateOnRecordedAudio runs the gate over a real recording of the
// model's audio, the sessions back to back with each one's start marked, as
// the player receives them. It skips without a recording; see
// TestReplayRecordedRun for how to make one.
//
// Every freeze within freezeStartMS of a session's first audio must be
// removed whole, and every other one must keep its first freezeKeepMS — at
// every frame alignment, since in the application the 10 ms grid lines up
// with the start of playback rather than with anything in the audio.
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
		g.Process(make([]float32, TranslatedRate*off/1000))
		blob := TranslatedRate / 4
		var starts []int64
		for _, s := range sessions {
			g.MarkSessionStart()
			starts = append(starts, g.total)
			for i := 0; i < len(s); i += blob {
				g.Process(s[i:min(i+blob, len(s))])
			}
		}
		g.Process(make([]float32, TranslatedRate))
		counts[len(g.cutLog)]++
		for i, c := range g.cutLog {
			onset := g.runStarts[i]
			opening := false
			for _, st := range starts {
				if onset >= st && onset-st <= int64(TranslatedRate*freezeStartMS/1000) {
					opening = true
				}
			}
			whole := c.from < onset
			if opening != whole {
				t.Errorf("offset %d ms: freeze at %.2f s (opening=%v) was cut whole=%v",
					off, float64(onset)/TranslatedRate, opening, whole)
			}
		}
	}
	t.Logf("freezes found per alignment: %v", counts)
}
