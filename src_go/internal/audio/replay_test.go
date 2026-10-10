package audio

import (
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestReplayRecordedRun plays a real recorded session — the model's actual
// audio, at its actual arrival times — through onPlayback, and reports what a
// listener would have heard at several trim thresholds.
//
// It needs a recording, so it skips without one:
//
//	go run ./spikes/v3_voiceid -fixture ../spikes/fixtures/ko_6min.wav \
//	    -reconnect 60 -overlap -outdir /tmp/run -timeline /tmp/run/timeline.csv
//	REPLAY_DIR=/tmp/run go test ./internal/audio -run TestReplayRecordedRun -v
//
// This is where PlayTrimAboveMS came from. Re-run it whenever the player's
// buffering changes; the unit tests pin the mechanism, this measures it.
func TestReplayRecordedRun(t *testing.T) {
	dir := os.Getenv("REPLAY_DIR")
	if dir == "" {
		t.Skip("REPLAY_DIR not set — see the comment for how to record one")
	}
	arrivals, audio := loadRecording(t, dir)

	for _, trimMS := range []int{0, 400, 800, 1200, PlayTrimAboveMS} {
		r := replay(arrivals, audio, trimMS)
		t.Logf("trim %4d ms | cuts %2d  lost %5.2fs | skipped pause %5.2fs | starved %5.2fs | queue avg %.2fs max %.2fs",
			trimMS, r.cuts, r.lost, r.trimmed, r.starved, r.avgQueue, r.maxQueue)
		if trimMS == PlayTrimAboveMS && r.cuts != 0 {
			t.Errorf("at the shipped threshold the player still cut %d times (%.2f s of speech)",
				r.cuts, r.lost)
		}
	}
}

type arrival struct {
	at      float64
	session int
	bytes   int
}

type replayResult struct {
	cuts                                       int
	lost, trimmed, starved, avgQueue, maxQueue float64
}

// replay drives a mono 24 kHz player, so the bytes recorded are the bytes the
// ring holds and no resampler stands between the recording and the result.
func replay(arrivals []arrival, audio map[int][]byte, trimMS int) replayResult {
	const bps = TranslatedRate * BytesPerSample
	p := NewPlayer("", TranslatedRate)
	p.devRate, p.devChan = TranslatedRate, 1
	st := &playState{
		ring:        newPlayRing(bps * PlayRingMS / 1000),
		prebuffer:   bps * PlayPrebufferMS / 1000,
		blockBytes:  BytesPerSample,
		trimAbove:   bps * trimMS / 1000,
		silencePeak: 32768 * math.Pow(10, PlaySilenceDBFS/20),
	}
	p.active.Store(st)

	out := make([]byte, bps*PlayCallbackMS/1000)
	frames := uint32(len(out) / BytesPerSample)
	step := float64(PlayCallbackMS) / 1000
	offset := map[int]int{}
	var res replayResult
	var lastDropped int64
	ticks, i := 0, 0
	for now := arrivals[0].at; now < arrivals[len(arrivals)-1].at+5; now += step {
		for ; i < len(arrivals) && arrivals[i].at <= now; i++ {
			a := arrivals[i]
			src := audio[a.session]
			end := min(offset[a.session]+a.bytes, len(src))
			st.ring.Write(src[offset[a.session]:end])
			offset[a.session] = end
			if _, _, d := st.ring.Stats(); d > lastDropped {
				res.cuts++
				lastDropped = d
			}
		}
		p.onPlayback(out, nil, frames)
		q := float64(st.ring.Available()) / bps
		res.avgQueue += q
		res.maxQueue = max(res.maxQueue, q)
		ticks++
	}
	_, starved, dropped := st.ring.Stats()
	res.lost = float64(dropped) / bps
	res.trimmed = float64(st.ring.Trimmed()) / bps
	res.starved = float64(starved) / bps
	res.avgQueue /= float64(ticks)
	return res
}

// loadRecording reads the spike's timeline and per-session WAVs.
func loadRecording(t *testing.T, dir string) ([]arrival, map[int][]byte) {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "timeline.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var arrivals []arrival
	for _, r := range rows[1:] {
		at, _ := strconv.ParseFloat(r[0], 64)
		s, _ := strconv.Atoi(r[1])
		n, _ := strconv.Atoi(r[2])
		arrivals = append(arrivals, arrival{at, s, n})
	}
	audio := map[int][]byte{}
	for _, a := range arrivals {
		if _, ok := audio[a.session]; ok {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("s%02d_t01.wav", a.session)))
		if err != nil {
			t.Fatal(err)
		}
		audio[a.session] = wavData(raw)
	}
	return arrivals, audio
}

// wavData returns the data chunk of a RIFF/WAVE file.
func wavData(raw []byte) []byte {
	for pos := 12; pos+8 <= len(raw); {
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		body := pos + 8
		if string(raw[pos:pos+4]) == "data" {
			return raw[body:min(body+size, len(raw))]
		}
		pos = body + size + size%2
	}
	return nil
}
