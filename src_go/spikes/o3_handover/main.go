// Spike O3 — what a listener hears across a reconnect, cold and warm.
//
// Drives the application's translator with the Korean fixture, looped at
// real-time pace, and forces a session swap every -swap seconds instead of
// waiting nine minutes for the server's. Everything the listener would get is
// recorded with its arrival time: the translated audio as one WAV in play
// order, the captions, and where each swap landed. Printed per swap: the
// longest stretch with no voiced audio around it, and the captions either
// side, so a duplicated or lost phrase is visible.
//
//	go run ./spikes/o3_handover -provider gemini -warm=true  -seconds 150 -swap 45 -out /tmp/o3w
//	go run ./spikes/o3_handover -provider gemini -warm=false -seconds 150 -swap 45 -out /tmp/o3c
//
// Costs about $0.04 a minute, more while two sessions overlap.
package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
)

func openAIKey(envFile string) string {
	if v := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); v != "" {
		return v
	}
	f, err := os.Open(envFile)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "OPENAI_API_KEY="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

func readWAV(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pos := 12
	for pos+8 <= len(raw) {
		id := string(raw[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		if id == "data" {
			return raw[pos+8 : min(pos+8+size, len(raw))], nil
		}
		pos += 8 + size + size%2
	}
	return nil, errors.New("no data chunk")
}

func writeWAV(path string, pcm []byte, rate int) error {
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(pcm)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(pcm)))
	return os.WriteFile(path, append(h, pcm...), 0o644)
}

func rms(pcm []byte) float64 {
	var s float64
	n := len(pcm) / 2
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
		s += v * v
	}
	return math.Sqrt(s / float64(max(n, 1)))
}

type blob struct {
	at     float64
	offset int // byte offset in the play-order stream
	voiced bool
}

type caption struct {
	at   float64
	text string
}

func main() {
	provider := flag.String("provider", "gemini", "gemini or openai")
	warm := flag.Bool("warm", true, "warm handover")
	seconds := flag.Float64("seconds", 150, "how long to run")
	swap := flag.Float64("swap", 45, "force a swap this many seconds into each session")
	fixture := flag.String("fixture", "spikes/fixtures/ko_fixture.wav", "16 kHz mono WAV, looped")
	envFile := flag.String("env", "../.env", ".env to read OPENAI_API_KEY from")
	out := flag.String("out", "", "directory for the recording")
	debug := flag.Bool("debug", false, "log the handover's decisions")
	flag.Parse()
	if *debug {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	var key string
	if *provider == "openai" {
		key = openAIKey(*envFile)
	} else {
		key = config.LoadAPIKey()
	}
	if key == "" {
		fmt.Fprintln(os.Stderr, "no key for", *provider)
		os.Exit(1)
	}
	pcm, err := readWAV(*fixture)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var (
		mu       sync.Mutex
		started  = time.Now()
		stream   []byte
		blobs    []blob
		captions []caption
		swaps    []float64
	)
	now := func() float64 { return time.Since(started).Seconds() }
	ring := audio.NewRing(audio.TargetRate * 2 * 10)
	tr, err := translator.New(translator.Options{
		Ring:           ring,
		APIKey:         key,
		Provider:       *provider,
		Session:        translator.SessionConfig{TargetLanguageCode: "en"},
		WarmHandover:   *warm,
		ForceSwapAfter: time.Duration(*swap * float64(time.Second)),
		Callbacks: translator.Callbacks{
			OnTarget: func(text string) {
				mu.Lock()
				captions = append(captions, caption{now(), text})
				mu.Unlock()
			},
			OnReconnect: func() {
				mu.Lock()
				swaps = append(swaps, now())
				mu.Unlock()
				fmt.Printf("[%6.1f] swap\n", now())
			},
			OnAudio: func(b []byte) {
				mu.Lock()
				blobs = append(blobs, blob{now(), len(stream), rms(b) >= 0.003})
				stream = append(stream, b...)
				mu.Unlock()
			},
			OnState: func(s translator.State, d string) {
				if s == translator.StateError {
					fmt.Printf("[%6.1f] ERROR %s\n", now(), d)
				}
			},
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := tr.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	next := time.Now()
	end := time.Now().Add(time.Duration(*seconds * float64(time.Second)))
	for pos := 0; time.Now().Before(end); pos = (pos + 3200) % (len(pcm) - len(pcm)%3200) {
		ring.Write(pcm[pos : pos+3200])
		next = next.Add(100 * time.Millisecond)
		time.Sleep(time.Until(next))
	}
	// Let the last words come back before stopping.
	time.Sleep(4 * time.Second)
	tr.Stop()

	mu.Lock()
	defer mu.Unlock()
	st := tr.Stats()
	fmt.Printf("\n%s warm=%v: %s\n", *provider, *warm, st.Summary())
	for i, s := range swaps {
		// The longest stretch without voiced audio within 10 s either side.
		var last, worst, worstAt float64
		last = s - 10
		for _, b := range blobs {
			if b.at < s-10 || b.at > s+10 || !b.voiced {
				continue
			}
			if gap := b.at - last; gap > worst {
				worst, worstAt = gap, b.at
			}
			last = b.at
		}
		fmt.Printf("\nswap %d at %.1f s: longest silence nearby %.2f s (ending %.1f s)\n", i+1, s, worst, worstAt)
		var before, after strings.Builder
		for _, c := range captions {
			switch {
			case c.at >= s-6 && c.at < s:
				before.WriteString(c.text)
			case c.at >= s && c.at < s+6:
				after.WriteString(c.text)
			}
		}
		fmt.Printf("  before: …%s\n  after:  %s…\n", before.String(), after.String())
	}
	// The same measure away from any swap, for comparison.
	var last, worst float64
	for _, b := range blobs {
		near := false
		for _, s := range swaps {
			if math.Abs(b.at-s) < 12 {
				near = true
			}
		}
		if !b.voiced {
			continue
		}
		if !near && last > 0 && b.at-last > worst {
			worst = b.at - last
		}
		last = b.at
	}
	fmt.Printf("\nlongest silence away from swaps: %.2f s\n", worst)

	if *out != "" {
		_ = os.MkdirAll(*out, 0o755)
		_ = writeWAV(filepath.Join(*out, "heard_24k.wav"), stream, audio.TranslatedRate)
		var sb strings.Builder
		sb.WriteString("swap_at_s,stream_offset_s\n")
		for _, s := range swaps {
			off := 0
			for _, b := range blobs {
				if b.at >= s {
					off = b.offset
					break
				}
			}
			fmt.Fprintf(&sb, "%.3f,%.3f\n", s, float64(off)/(audio.TranslatedRate*2))
		}
		_ = os.WriteFile(filepath.Join(*out, "swaps.csv"), []byte(sb.String()), 0o644)
		var cb strings.Builder
		for _, c := range captions {
			fmt.Fprintf(&cb, "%.3f\t%q\n", c.at, c.text)
		}
		_ = os.WriteFile(filepath.Join(*out, "captions.tsv"), []byte(cb.String()), 0o644)
		fmt.Println("saved to", *out)
	}
}
