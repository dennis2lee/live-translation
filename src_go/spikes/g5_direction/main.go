// Spike G5 — a mid-session direction switch, live.
//
// The gate line: EN→KO as well as KO→EN, with a direction switch mid-session
// that keeps the audio and the transcript — same file, switch noted.
//
// This mimics exactly what Window.restartTranslator does on a direction
// change: the ring buffer and the transcript writer stay, the translator is
// stopped and a new one opened with the new target, and the switch is noted in
// the transcript. Korean audio is fed to the KO→EN half and English audio to
// the EN→KO half, so both directions have something real to translate.
//
//	go run ./spikes/g5_direction
//
// About four minutes of session time, so roughly $0.15.
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/transcript"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
)

type half struct {
	target    string
	fragments []string
}

type collector struct {
	mu     sync.Mutex
	halves []*half
	writer *transcript.Writer
	start  time.Time
}

func (c *collector) onTarget(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.halves[len(c.halves)-1]
	h.fragments = append(h.fragments, text)
	c.writer.AppendLine(strings.TrimSpace(text), time.Since(c.start).Seconds())
}

func (c *collector) begin(target string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.halves = append(c.halves, &half{target: target})
}

func readWAV(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" {
		return nil, errors.New("not a WAV")
	}
	pos := 12
	for pos+8 <= len(raw) {
		id := string(raw[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		if id == "data" {
			end := min(pos+8+size, len(raw))
			return raw[pos+8 : end], nil
		}
		pos += 8 + size + size%2
	}
	return nil, errors.New("no data chunk")
}

// feed pushes seconds of PCM into the ring at real time.
func feed(ring *audio.Ring, pcm []byte, seconds float64) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	i, sent, want := 0, 0, int(seconds*audio.TargetRate*audio.BytesPerSample)
	for sent < want {
		<-ticker.C
		end := i + audio.ChunkBytes
		if end > len(pcm) {
			i, end = 0, audio.ChunkBytes
		}
		ring.Write(pcm[i:end])
		i, sent = end, sent+audio.ChunkBytes
	}
}

func hasHangul(s string) bool {
	for _, r := range s {
		if r >= 0xAC00 && r <= 0xD7A3 {
			return true
		}
	}
	return false
}

func main() {
	seconds := flag.Float64("seconds", 90, "how long each half runs")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	apiKey := config.LoadAPIKey()
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "no API key found")
		os.Exit(1)
	}
	koPCM, err := readWAV("../spikes/fixtures/ko_fixture.wav")
	if err != nil {
		panic(err)
	}
	enPCM, err := readWAV("../spikes/fixtures/en_fixture.wav")
	if err != nil {
		panic(err)
	}

	dir, err := os.MkdirTemp("", "livecap-direction-")
	if err != nil {
		panic(err)
	}

	// The pieces that survive the switch, exactly as in the window: one ring,
	// one transcript writer.
	ring := audio.NewRing(audio.TargetRate * audio.BytesPerSample * audio.RingSeconds)
	c := &collector{
		writer: transcript.New(transcript.Options{Dir: dir, TargetLanguage: "en", Model: translator.Model}),
		start:  time.Now(),
	}
	path := c.writer.Open(time.Now())
	fmt.Printf("direction-switch run: %.0f s per half\n  transcript %s\n\n", *seconds, path)

	open := func(target string) *translator.Translator {
		c.begin(target)
		tr, err := translator.New(translator.Options{
			Ring:    ring,
			APIKey:  apiKey,
			Session: translator.SessionConfig{TargetLanguageCode: target},
			Callbacks: translator.Callbacks{
				OnTarget: c.onTarget,
			},
		})
		if err != nil {
			panic(err)
		}
		if err := tr.Start(); err != nil {
			panic(err)
		}
		return tr
	}

	// --- first half: KO→EN ------------------------------------------------
	fmt.Println("KO→EN, feeding Korean…")
	tr := open("en")
	feed(ring, koPCM, *seconds)
	time.Sleep(5 * time.Second) // let the tail arrive

	// --- the switch, as restartTranslator does it -------------------------
	fmt.Println("switching direction: translator only; ring and transcript stay")
	tr.Stop()
	c.writer.NoteDirection("EN→KO", time.Since(c.start).Seconds())
	tr = open("ko")

	// --- second half: EN→KO -----------------------------------------------
	fmt.Println("EN→KO, feeding English…")
	feed(ring, enPCM, *seconds)
	time.Sleep(5 * time.Second)
	tr.Stop()
	c.writer.Close(time.Since(c.start).Seconds(), 0)

	// --- verdict -----------------------------------------------------------
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Printf("\n--- G5 direction-switch result ---\n")
	pass := true
	for _, h := range c.halves {
		korean := 0
		for _, f := range h.fragments {
			if hasHangul(f) {
				korean++
			}
		}
		fmt.Printf("  target=%s  fragments=%d  with-hangul=%d\n", h.target, len(h.fragments), korean)
		sample := ""
		if len(h.fragments) > 2 {
			sample = strings.TrimSpace(strings.Join(h.fragments[:3], ""))
		}
		fmt.Printf("    first: %.80s\n", sample)
		switch {
		case len(h.fragments) == 0:
			pass = false
			fmt.Printf("    FAIL: no fragments in this half\n")
		case h.target == "en" && korean > len(h.fragments)/4:
			pass = false
			fmt.Printf("    FAIL: the KO→EN half is full of Hangul\n")
		case h.target == "ko" && korean == 0:
			pass = false
			fmt.Printf("    FAIL: the EN→KO half produced no Hangul at all\n")
		}
	}
	fmt.Printf("  transcript %s\n", path)
	if pass {
		fmt.Println("  PASS — one transcript, both directions, switch noted")
	} else {
		os.Exit(1)
	}
}
