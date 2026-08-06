// Spike G3 — the 30-minute endurance run, headless.
//
// The parity gate asks for three things that only a long session shows: at
// least three seamless reconnects, no sentence lost across one, and a backlog
// that stays near zero. A microphone is not needed to prove any of them, so
// this feeds the fixture straight into the ring buffer at real time and runs
// the *real* translator, caption buffer and transcript writer above it — the
// same objects the window drives, minus the painting.
//
//	go run ./spikes/g3_endurance                 # the full 30 minutes
//	go run ./spikes/g3_endurance -minutes 2      # a shorter smoke run
//
// This costs money: about $0.037 a minute, so a full run is roughly $1.10.
//
// "No sentence lost" is not judged by eye and not judged on the English. The
// Python build scored it by collecting the *Korean input transcription* and
// measuring how much of every script sentence survives in it
// (spikes/s2_headless_run.py, SequenceMatcher coverage; its 30-minute run
// scored 360/360, worst 0.78, mean 0.96). This harness saves the collected
// source transcription in that scorer's format, so the *same* Python code
// scores both builds:
//
//	.venv/bin/python spikes/s2_headless_run.py --compare-only <out>/source.txt \
//	    --script spikes/fixtures/ko_script.txt --repeat 20
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
	"github.com/dennis2lee/live-translation/src_go/internal/captions"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/transcript"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
)

// headless is the window's state machine without the window: the same caption
// buffer, the same tick, the same transcript writer, driven by the same
// callbacks. Everything here runs on the dispatch goroutine, which stands in
// for the UI goroutine, so the buffer keeps its single-owner contract.
type headless struct {
	buffer *captions.Buffer
	writer *transcript.Writer
	start  time.Time

	mu            sync.Mutex
	source        []string
	fragments     int
	turnCompletes int
	reconnects    int
	firstFragment time.Duration
	// events are the reconnects, with the transcript line index at the moment
	// they happened, so continuity across a swap can be checked afterwards.
	events []string
	// lineAt records how many lines had been written when each reconnect
	// landed.
	lineAt []int

	dispatch chan func()
}

func (h *headless) run(done <-chan struct{}) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case fn := <-h.dispatch:
			fn()
		case <-ticker.C:
			// speaking is unknown: this run has no level meter, which is the
			// same state the window is in before any audio device is open.
			h.buffer.Tick(nowSeconds(), captions.SpeechUnknown)
			h.record()
		case <-done:
			return
		}
	}
}

func (h *headless) record() {
	lines := h.buffer.DrainNewCommits()
	if h.writer == nil {
		return
	}
	elapsed := time.Since(h.start).Seconds()
	for _, line := range lines {
		h.writer.AppendLine(line, elapsed)
	}
}

func (h *headless) onSource(text string) {
	h.mu.Lock()
	h.source = append(h.source, text)
	h.mu.Unlock()
}

func (h *headless) onTarget(text string) {
	h.dispatch <- func() {
		h.mu.Lock()
		if h.fragments == 0 {
			h.firstFragment = time.Since(h.start)
		}
		h.fragments++
		h.mu.Unlock()
		h.buffer.Feed(text, nowSeconds())
		h.record()
	}
}

func (h *headless) onTurnComplete() {
	h.dispatch <- func() {
		h.mu.Lock()
		h.turnCompletes++
		h.mu.Unlock()
		h.buffer.NoteTurnComplete(nowSeconds())
		h.record()
	}
}

func (h *headless) onReconnect(gaps []float64) {
	h.dispatch <- func() {
		// The buffer merges the sentence a reconnect interrupted; this is the
		// call that makes it do so.
		h.buffer.NoteReconnect()
		gap := 0.0
		if len(gaps) > 0 {
			gap = gaps[len(gaps)-1]
		}
		h.mu.Lock()
		h.reconnects++
		h.events = append(h.events, fmt.Sprintf("reconnect #%d at %s, gap %.0f ms",
			h.reconnects, time.Since(h.start).Round(time.Second), gap))
		if h.writer != nil {
			h.lineAt = append(h.lineAt, h.writer.LinesWritten())
			h.writer.NoteReconnect(time.Since(h.start).Seconds(), gap)
		}
		h.mu.Unlock()
	}
}

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// readWAV returns the PCM payload of a 16 kHz mono 16-bit WAV.
func readWAV(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF/WAVE file")
	}
	pos := 12
	var channels, bits, rate int
	for pos+8 <= len(raw) {
		id := string(raw[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		body := pos + 8
		switch id {
		case "fmt ":
			channels = int(binary.LittleEndian.Uint16(raw[body+2 : body+4]))
			rate = int(binary.LittleEndian.Uint32(raw[body+4 : body+8]))
			bits = int(binary.LittleEndian.Uint16(raw[body+14 : body+16]))
		case "data":
			if channels != 1 || rate != audio.TargetRate || bits != 16 {
				return nil, fmt.Errorf("fixture is %d Hz/%dch/%d-bit, need 16000/1/16",
					rate, channels, bits)
			}
			end := min(body+size, len(raw))
			return raw[body:end], nil
		}
		pos = body + size
		if size%2 == 1 {
			pos++
		}
	}
	return nil, errors.New("no data chunk")
}

func main() {
	fixture := flag.String("fixture", "../spikes/fixtures/ko_30min.wav", "16 kHz mono WAV to stream")
	minutes := flag.Float64("minutes", 30, "how long to run")
	target := flag.String("target", "en", "target language code")
	out := flag.String("out", "", "transcript directory (default: a temporary one)")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	apiKey := config.LoadAPIKey()
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "no API key found")
		os.Exit(1)
	}
	pcm, err := readWAV(*fixture)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture: %v\n", err)
		os.Exit(1)
	}
	wanted := int(*minutes * 60 * audio.TargetRate * audio.BytesPerSample)
	looped := *minutes*60 > float64(len(pcm))/(audio.TargetRate*audio.BytesPerSample)

	dir := *out
	if dir == "" {
		dir, err = os.MkdirTemp("", "livecap-endurance-")
		if err != nil {
			panic(err)
		}
	}

	ring := audio.NewRing(audio.TargetRate * audio.BytesPerSample * audio.RingSeconds)
	settings := config.Default()
	h := &headless{
		buffer: captions.New(captions.Options{
			MaxLines:          settings.MaxLines,
			Policy:            settings.Policy(),
			ClearAfterSeconds: settings.ClearAfterSeconds,
			LineHoldSeconds:   settings.LineHoldSeconds,
		}),
		writer:   transcript.New(transcript.Options{Dir: dir, TargetLanguage: *target, Model: translator.Model}),
		start:    time.Now(),
		dispatch: make(chan func(), 256),
	}
	path := h.writer.Open(time.Now())

	var tr *translator.Translator
	tr, err = translator.New(translator.Options{
		Ring:    ring,
		APIKey:  apiKey,
		Session: translator.SessionConfig{TargetLanguageCode: *target},
		Callbacks: translator.Callbacks{
			OnSource:       h.onSource,
			OnTarget:       h.onTarget,
			OnTurnComplete: h.onTurnComplete,
			OnReconnect:    func() { h.onReconnect(tr.Stats().GapsMS) },
			OnState: func(s translator.State, detail string) {
				if detail != "" {
					slog.Info("state", "state", s, "detail", detail)
				}
			},
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "translator: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("endurance run: %.0f minutes, target %s\n", *minutes, *target)
	fmt.Printf("  fixture   %s (%.1f min%s)\n", *fixture,
		float64(len(pcm))/(audio.TargetRate*audio.BytesPerSample)/60,
		map[bool]string{true: ", looped"}[looped])
	fmt.Printf("  transcript %s\n\n", path)

	done := make(chan struct{})
	go h.run(done)
	if err := tr.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "start: %v\n", err)
		os.Exit(1)
	}

	// Ctrl-C stops early but still writes the footer and the summary, so a run
	// that has to be cut short is not wasted.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Feed the ring at real time, exactly as the capture callback would.
	ticker := time.NewTicker(100 * time.Millisecond)
	sent, i := 0, 0
	lastReport := time.Now()
feed:
	for sent < wanted {
		select {
		case <-ticker.C:
		case <-stop:
			fmt.Println("\ninterrupted — finishing up")
			break feed
		}
		end := i + audio.ChunkBytes
		if end > len(pcm) {
			i, end = 0, audio.ChunkBytes // loop the fixture
		}
		ring.Write(pcm[i:end])
		i, sent = end, sent+audio.ChunkBytes

		if time.Since(lastReport) >= time.Minute {
			lastReport = time.Now()
			st := tr.Stats()
			h.mu.Lock()
			frags, recon := h.fragments, h.reconnects
			h.mu.Unlock()
			fmt.Printf("  %5s  fragments=%-5d lines=%-4d reconnects=%d backlog_max=%.0fms chunks=%d\n",
				time.Since(h.start).Round(time.Second), frags, h.writer.LinesWritten(),
				recon, st.MaxBacklogMS, st.ChunksSent)
		}
	}
	ticker.Stop()

	fmt.Println("\naudio finished — waiting 10 s for the tail")
	time.Sleep(10 * time.Second)

	tr.Stop()
	h.buffer.Flush()
	h.dispatch <- h.record
	time.Sleep(200 * time.Millisecond)
	close(done)

	stats := tr.Stats()
	elapsed := time.Since(h.start)
	h.writer.Close(elapsed.Seconds(), stats.Reconnects)

	// --- verdict ------------------------------------------------------------
	h.mu.Lock()
	defer h.mu.Unlock()

	fmt.Printf("\n--- G3 endurance result ---\n")
	fmt.Printf("  elapsed              %s\n", elapsed.Round(time.Second))
	fmt.Printf("  fragments            %d (first at %s)\n", h.fragments, h.firstFragment.Round(time.Millisecond))
	fmt.Printf("  turn_complete        %d\n", h.turnCompletes)
	fmt.Printf("  transcript lines     %d\n", h.writer.LinesWritten())
	fmt.Printf("  chunks sent          %d\n", stats.ChunksSent)
	fmt.Printf("  max send backlog     %.0f ms\n", stats.MaxBacklogMS)
	fmt.Printf("  reconnects           %d\n", stats.Reconnects)
	for _, e := range h.events {
		fmt.Printf("    %s\n", e)
	}
	fmt.Printf("  transcript           %s\n", path)

	// The source transcription, in the format the Python scorer reads.
	sourcePath := dir + "/source.txt"
	src := "# Source transcription\n" + strings.Join(h.source, "") + "\n# Translation\n"
	if err := os.WriteFile(sourcePath, []byte(src), 0o644); err != nil {
		fmt.Printf("  could not save the source transcription: %v\n", err)
	} else {
		fmt.Printf("  source transcription %s (%d fragments)\n", sourcePath, len(h.source))
		fmt.Printf("\n  score it with the Python build's own scorer:\n")
		fmt.Printf("    .venv/bin/python spikes/s2_headless_run.py --compare-only %s \\\n", sourcePath)
		fmt.Printf("        --script spikes/fixtures/ko_script.txt --repeat 20\n")
	}

	pass := true
	fail := func(why string) {
		pass = false
		fmt.Printf("  FAIL: %s\n", why)
	}

	// The gate's bar, from the Python build's measured run.
	if elapsed >= 25*time.Minute && stats.Reconnects < 3 {
		fail(fmt.Sprintf("%d reconnects in %s — the gate asks for at least 3 over 30 minutes",
			stats.Reconnects, elapsed.Round(time.Minute)))
	}
	for _, gap := range stats.GapsMS {
		if gap > 1000 {
			fail(fmt.Sprintf("a reconnect gap of %.0f ms is over the 1 s bar", gap))
		}
	}
	if stats.MaxBacklogMS > translator.BacklogWarnMS {
		fail(fmt.Sprintf("backlog reached %.0f ms — the audio was not going out as it arrived",
			stats.MaxBacklogMS))
	}
	if h.fragments == 0 {
		fail("no fragments arrived at all")
	}
	if h.writer.LinesWritten() == 0 {
		fail("nothing reached the transcript")
	}
	if pass {
		fmt.Printf("  PASS\n")
	}
	fmt.Printf("\nRead the transcript around each reconnect marker to confirm no\n")
	fmt.Printf("sentence was truncated across a swap — that is the one thing a\n")
	fmt.Printf("counter cannot check.\n")
	if !pass {
		os.Exit(1)
	}
}
