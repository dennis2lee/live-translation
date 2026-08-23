// Spike V3 — is it the same voice, and who is making the silence?
//
// Two complaints from real use, and one suspicion that they are the same bug:
//
//  1. The spoken translation changes voice partway through a session.
//  2. It drops into silence for a stretch, and the voice tends to be different
//     on the other side of the silence.
//
// If (2) always accompanies (1), the shared cause is almost certainly the
// reconnect: the server sends go_away about every nine minutes, the application
// swaps in a new session, and a new session is free to pick a new voice. The
// 1.2.0 study could not have seen this — it measured one short session at a
// time, so "the voice changed between runs" and "the voice changes within a
// run" produce the same numbers.
//
// This spike separates them. It streams a fixed TTS fixture, so the input is
// byte-identical every run and nothing about the *speaker* varies; it cuts the
// received audio at every turn boundary and at every session swap; and it
// fingerprints each piece. It also keeps an inventory of arrival gaps with the
// audio in hand at the moment each gap began, which is what decides whether a
// gap is audible or is absorbed by the jitter buffer.
//
// That last number is the answer to "is the silence my fault?". Reserve is
// computed from arrival times alone — no player, no audio device, no ring
// buffer. If reserve goes negative here, the silence is in what the server
// sent, and no amount of client buffering would have covered it.
//
// The fixture is macOS text-to-speech, not a recording, so it is identical to
// the byte on every run and nothing about the speaker can drift between them.
// `*.wav` is gitignored — never commit captured audio — so rebuild it with:
//
//	say -v Yuna -f spikes/fixtures/ko_script.txt -o /tmp/ko.aiff
//	afconvert -f WAVE -d LEI16@16000 -c 1 /tmp/ko.aiff spikes/fixtures/ko_fixture.wav
//
// This costs money — about $0.037 a minute.
//
//	go run ./spikes/v3_voiceid -outdir /tmp/v3a                    # one session
//	go run ./spikes/v3_voiceid -reconnect 30 -resume -outdir /tmp/v3b
//	go run ./spikes/v3_voiceid -reconnect 30 -outdir /tmp/v3c      # fresh sessions
//	go run ./spikes/v3_voiceid -voice Kore -outdir /tmp/v3d        # re-test SpeechConfig
//	go run ./spikes/v3_voiceid -reconnect 30 -overlap              # does overlapping hide the gap?
//	go run ./spikes/v3_voiceid -analyze out.wav                    # offline, costs nothing
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
	"google.golang.org/genai"
)

const (
	model     = "gemini-3.5-live-translate-preview"
	chunkMS   = 100
	sendRate  = 16000
	chunkSize = sendRate * 2 * chunkMS / 1000
	outRate   = 24000

	// gapFloor is the arrival gap worth recording. Blobs arrive about ten times
	// a second, so anything under this is the normal cadence.
	gapFloor = 0.30

	keyringService = "LiveCap"
	keyringUser    = "gemini-api-key"
)

var apiKeyVars = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}

func loadAPIKey() (key, source string) {
	for _, name := range apiKeyVars {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v, "environment (" + name + ")"
		}
	}
	if v, err := keyring.Get(keyringService, keyringUser); err == nil {
		if v = strings.TrimSpace(v); v != "" {
			return v, "macOS Keychain"
		}
	}
	return "", ""
}

// --- fixture io -------------------------------------------------------------

// readWAVAny returns the PCM payload and rate of any mono 16-bit WAV.
func readWAVAny(path string) (pcm []byte, rate int, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, 0, errors.New("not a RIFF/WAVE file")
	}
	pos := 12
	var channels, bits int
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
			if channels != 1 || bits != 16 {
				return nil, 0, fmt.Errorf("file is %dch/%d-bit, need mono 16-bit", channels, bits)
			}
			return raw[body:min(body+size, len(raw))], rate, nil
		}
		pos = body + size
		if size%2 == 1 {
			pos++
		}
	}
	return nil, 0, errors.New("no data chunk")
}

func readWAV(path string) ([]byte, error) {
	pcm, rate, err := readWAVAny(path)
	if err != nil {
		return nil, err
	}
	if rate != sendRate {
		return nil, fmt.Errorf("fixture is %d Hz, need %d", rate, sendRate)
	}
	return pcm, nil
}

// analyse fingerprints a saved WAV in windows. Offline, so a recording can be
// re-read as many times as the question changes without paying for it again.
func analyse(path string, windowSeconds float64) {
	pcm, rate, err := readWAVAny(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "analyze: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s: %.1f s at %d Hz\n\n", path, float64(len(pcm))/float64(rate*2), rate)
	fmt.Printf("  %-8s %7s %6s %9s %8s %8s %7s\n",
		"window", "at", "voiced", "f0_median", "f0_iqr", "centroid", "dist")
	step := int(windowSeconds * float64(rate) * 2)
	var prev voicePrint
	var f0s []float64
	for off, n := 0, 0; off < len(pcm); off, n = off+step, n+1 {
		chunk := pcm[off:min(off+step, len(pcm))]
		if float64(len(chunk))/float64(rate*2) < windowSeconds/3 {
			continue
		}
		vp := fingerprint(chunk, rate)
		dist, mark := 0.0, ""
		if n > 0 {
			dist = distance(prev, vp)
			if dist > voiceChangeThreshold {
				mark = "  <-- CHANGED"
			}
		}
		fmt.Printf("  w%-7d %6.1fs %5.0f%% %8.1f %7.1f %8.0f %6.2f%s\n",
			n+1, float64(n)*windowSeconds, vp.voiced*100,
			vp.f0Median, vp.f0IQR, vp.centroid, dist, mark)
		if vp.f0Median > 0 {
			f0s = append(f0s, vp.f0Median)
		}
		prev = vp
	}
	if len(f0s) > 1 {
		sort.Float64s(f0s)
		fmt.Printf("\n  f0 across windows: %.1f .. %.1f Hz (spread %.1f)\n",
			f0s[0], f0s[len(f0s)-1], f0s[len(f0s)-1]-f0s[0])
	}
}

func writeWAV(path string, pcm []byte, rate int) error {
	var h []byte
	put32 := func(v int) { h = binary.LittleEndian.AppendUint32(h, uint32(v)) }
	put16 := func(v int) { h = binary.LittleEndian.AppendUint16(h, uint16(v)) }
	h = append(h, "RIFF"...)
	put32(36 + len(pcm))
	h = append(h, "WAVEfmt "...)
	put32(16)
	put16(1)
	put16(1)
	put32(rate)
	put32(rate * 2)
	put16(2)
	put16(16)
	h = append(h, "data"...)
	put32(len(pcm))
	return os.WriteFile(path, append(h, pcm...), 0o644)
}

var rateRE = regexp.MustCompile(`rate=(\d+)`)

func rateFromMIME(mime string) int {
	m := rateRE.FindStringSubmatch(mime)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// --- collection -------------------------------------------------------------

// segment is one turn's worth of speech from one session.
type segment struct {
	session, turn int
	firstAt       time.Duration
	pcm           []byte
}

func (s *segment) name() string { return fmt.Sprintf("s%02d_t%02d", s.session, s.turn) }

// gapEvent is a hole in the arrival stream, with the audio in hand when it
// began. reserve below zero is silence the listener hears.
type gapEvent struct {
	at           time.Duration
	gap          float64
	reserve      float64
	silence      float64
	session      int
	crossSession bool
}

type collector struct {
	mu      sync.Mutex
	started time.Time

	segs   []*segment
	cur    *segment
	turnNo map[int]int

	totalBytes int
	// availableUntil is the wall-clock instant the queued audio runs out. It
	// is the whole model of the player: each blob extends it by its own
	// duration, and a blob that arrives after it has already passed means the
	// player had nothing to play in between.
	//
	// The first cut of this tracked a running reserve instead, which went
	// permanently negative the moment anything starved and then reported the
	// same backlog as fresh silence at every later gap. A player does not
	// accumulate a debt — it goes quiet and starts again from the next blob.
	availableUntil time.Time
	haveFirst      bool
	lastBlob       time.Time
	lastSess       int

	gaps  []gapEvent
	mimes map[string]int
	text  strings.Builder
}

func newCollector(started time.Time) *collector {
	return &collector{started: started, turnNo: map[int]int{}, mimes: map[string]int{}}
}

func (c *collector) onAudio(session int, mime string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.mimes[mime]++

	if !c.haveFirst {
		c.availableUntil, c.haveFirst = now, true
	} else if gap := now.Sub(c.lastBlob).Seconds(); gap >= gapFloor {
		c.gaps = append(c.gaps, gapEvent{
			at:      c.lastBlob.Sub(c.started),
			gap:     gap,
			reserve: c.availableUntil.Sub(c.lastBlob).Seconds(),
			// Silence is the stretch between the audio running out and the
			// next blob. Never negative: a player that still had audio in
			// hand made no silence at all.
			silence:      max(0, now.Sub(c.availableUntil).Seconds()),
			session:      session,
			crossSession: session != c.lastSess,
		})
	}
	if c.availableUntil.Before(now) {
		// Starved. Playback restarts from this blob rather than from a debt.
		c.availableUntil = now
	}
	c.availableUntil = c.availableUntil.Add(
		time.Duration(float64(len(data)) / float64(outRate*2) * float64(time.Second)))
	c.lastBlob, c.lastSess = now, session
	c.totalBytes += len(data)

	if c.cur == nil || c.cur.session != session {
		c.closeTurnLocked()
		c.turnNo[session]++
		c.cur = &segment{session: session, turn: c.turnNo[session], firstAt: now.Sub(c.started)}
	}
	c.cur.pcm = append(c.cur.pcm, data...)
}

func (c *collector) onTurnComplete(session int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != nil && c.cur.session == session {
		c.closeTurnLocked()
	}
}

func (c *collector) closeTurnLocked() {
	if c.cur != nil && len(c.cur.pcm) > 0 {
		c.segs = append(c.segs, c.cur)
	}
	c.cur = nil
}

func (c *collector) onText(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.text.WriteString(s)
}

func (c *collector) finish() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeTurnLocked()
}

// --- main -------------------------------------------------------------------

func main() {
	fixture := flag.String("fixture", "spikes/fixtures/ko_fixture.wav", "16 kHz mono WAV to stream")
	seconds := flag.Float64("seconds", 0, "how much of the fixture to send; 0 sends all of it")
	target := flag.String("target", "en", "target language code")
	voice := flag.String("voice", "", "prebuilt voice name; empty sends no SpeechConfig")
	reconnect := flag.Float64("reconnect", 0, "swap in a new session every N seconds; 0 never does")
	resume := flag.Bool("resume", false, "offer the newest resumption handle when reconnecting")
	overlap := flag.Bool("overlap", false, "keep the old session open until the new one speaks")
	outdir := flag.String("outdir", "", "write per-session WAVs here")
	window := flag.Float64("window", 10, "fingerprint the audio in windows this many seconds long")
	analyze := flag.String("analyze", "", "fingerprint a saved WAV and exit, without touching the API")
	flag.Parse()

	if *analyze != "" {
		analyse(*analyze, *window)
		return
	}

	key, source := loadAPIKey()
	if key == "" {
		fmt.Fprintln(os.Stderr, "no API key: set GEMINI_API_KEY, or store one in the Keychain")
		os.Exit(1)
	}
	fmt.Printf("api key from %s\n", source)

	pcm, err := readWAV(*fixture)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture: %v\n", err)
		os.Exit(1)
	}
	if want := int(*seconds) * sendRate * 2; want > 0 && want < len(pcm) {
		pcm = pcm[:want]
	}
	fmt.Printf("sending %.1f s of 16 kHz mono from %s\n",
		float64(len(pcm))/(sendRate*2), *fixture)
	if *reconnect > 0 {
		fmt.Printf("reconnecting every %.0f s, resume handle %v\n", *reconnect, *resume)
	}
	if *voice != "" {
		fmt.Printf("speech_config: prebuilt voice %q\n", *voice)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: key, Backend: genai.BackendGeminiAPI})
	if err != nil {
		fmt.Fprintf(os.Stderr, "client: %v\n", err)
		os.Exit(1)
	}

	buildCfg := func(handle string) *genai.LiveConnectConfig {
		echo := false
		cfg := &genai.LiveConnectConfig{
			ResponseModalities:       []genai.Modality{genai.ModalityAudio},
			InputAudioTranscription:  &genai.AudioTranscriptionConfig{},
			OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
			TranslationConfig: &genai.TranslationConfig{
				TargetLanguageCode: *target,
				EchoTargetLanguage: &echo,
			},
			ContextWindowCompression: &genai.ContextWindowCompressionConfig{
				SlidingWindow: &genai.SlidingWindow{},
			},
			SessionResumption: &genai.SessionResumptionConfig{Handle: handle},
		}
		if *voice != "" {
			cfg.SpeechConfig = &genai.SpeechConfig{
				VoiceConfig: &genai.VoiceConfig{
					PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: *voice},
				},
			}
		}
		return cfg
	}

	started := time.Now()
	col := newCollector(started)

	var (
		mu      sync.Mutex
		handle  string
		current *genai.Session
		sessN   int
		wg      sync.WaitGroup
		swaps   []float64

		// retiring is the session that has been replaced but not yet closed.
		//
		// The hypothesis this flag exists to test: a session that stops being
		// fed still finishes translating what it already heard, and that tail
		// is about as long as the replacement takes to warm up. If so, holding
		// the old one open until the new one speaks costs one overlapping
		// connection for a few seconds and removes the gap entirely.
		retiring    *genai.Session
		retiringIdx int
	)

	// retire closes the replaced session once the replacement has spoken.
	retire := func(idx int) {
		mu.Lock()
		s := retiring
		old := retiringIdx
		if s != nil && idx == sessN {
			retiring, retiringIdx = nil, 0
		} else {
			s = nil
		}
		mu.Unlock()
		if s != nil {
			fmt.Printf("[session %d retired — session %d has started speaking]\n", old, idx)
			s.Close()
		}
	}

	receive := func(s *genai.Session, idx int) {
		defer wg.Done()
		for {
			msg, err := s.Receive()
			if err != nil {
				if !errors.Is(err, io.EOF) && ctx.Err() == nil {
					fmt.Printf("[session %d ended: %v]\n", idx, err)
				}
				return
			}
			if upd := msg.SessionResumptionUpdate; upd != nil && upd.Resumable && upd.NewHandle != "" {
				mu.Lock()
				handle = upd.NewHandle
				mu.Unlock()
			}
			if msg.GoAway != nil {
				fmt.Printf("[session %d go_away, %v left]\n", idx, msg.GoAway.TimeLeft)
			}
			sc := msg.ServerContent
			if sc == nil {
				continue
			}
			if sc.OutputTranscription != nil {
				col.onText(sc.OutputTranscription.Text)
			}
			if sc.ModelTurn != nil {
				for _, part := range sc.ModelTurn.Parts {
					if part.InlineData != nil {
						col.onAudio(idx, part.InlineData.MIMEType, part.InlineData.Data)
						if *overlap {
							retire(idx)
						}
					}
				}
			}
			if sc.TurnComplete {
				col.onTurnComplete(idx)
			}
		}
	}

	open := func(h string) error {
		s, err := client.Live.Connect(ctx, model, buildCfg(h))
		if err != nil {
			return err
		}
		mu.Lock()
		sessN++
		idx := sessN
		previous := current
		current = s
		mu.Unlock()
		wg.Add(1)
		go receive(s, idx)
		if previous != nil {
			if *overlap {
				// Left open on purpose. It has no more audio coming in, so it
				// will translate its tail and fall quiet by itself; retire()
				// closes it the moment the replacement speaks.
				mu.Lock()
				stale := retiring
				retiring, retiringIdx = previous, idx-1
				mu.Unlock()
				if stale != nil {
					stale.Close() // two swaps inside one warm-up
				}
			} else {
				previous.Close()
			}
		}
		fmt.Printf("[session %d open at %v, resumed=%v]\n",
			idx, time.Since(started).Round(time.Millisecond), h != "")
		return nil
	}

	if err := open(""); err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}

	ticker := time.NewTicker(chunkMS * time.Millisecond)
	defer ticker.Stop()
	nextSwap := *reconnect
	for i := 0; i < len(pcm); i += chunkSize {
		<-ticker.C
		elapsed := time.Since(started).Seconds()
		if *reconnect > 0 && elapsed >= nextSwap {
			nextSwap += *reconnect
			mu.Lock()
			h := handle
			mu.Unlock()
			if !*resume {
				h = ""
			}
			swapStart := time.Now()
			if err := open(h); err != nil {
				fmt.Printf("[reconnect failed: %v]\n", err)
			} else {
				swaps = append(swaps, time.Since(swapStart).Seconds())
			}
		}
		mu.Lock()
		s := current
		mu.Unlock()
		if err := s.SendRealtimeInput(genai.LiveRealtimeInput{
			Audio: &genai.Blob{
				Data:     pcm[i:min(i+chunkSize, len(pcm))],
				MIMEType: fmt.Sprintf("audio/pcm;rate=%d", sendRate),
			},
		}); err != nil {
			fmt.Printf("[send failed: %v]\n", err)
		}
	}

	// Let the tail arrive before pulling the connection down.
	time.Sleep(8 * time.Second)
	mu.Lock()
	if current != nil {
		current.Close()
	}
	if retiring != nil {
		retiring.Close()
	}
	mu.Unlock()
	cancel()
	wg.Wait()
	col.finish()

	report(col, *outdir, swaps, *window)
}

func report(col *collector, outdir string, swaps []float64, windowSeconds float64) {
	fmt.Printf("\n--- V3 result ---\n")
	for mime, n := range col.mimes {
		fmt.Printf("  mime: %q (%d blobs, rate=%d)\n", mime, n, rateFromMIME(mime))
	}
	fmt.Printf("  transcription: %q\n", strings.TrimSpace(col.text.String()))
	if len(swaps) > 0 {
		fmt.Printf("  session swaps: %d, connect took", len(swaps))
		for _, s := range swaps {
			fmt.Printf(" %.0fms", s*1000)
		}
		fmt.Println()
	}

	// Windows, not turns. turn_complete never arrives on this model during a
	// continuous stream — the 103 s control run produced exactly one turn — so
	// a per-turn fingerprint would average a whole session into one number and
	// hide the very drift this spike exists to find.
	fmt.Printf("\n  === voice per %.0fs window ===\n", windowSeconds)
	fmt.Printf("  %-12s %7s %6s %9s %8s %8s %7s\n",
		"window", "at", "voiced", "f0_median", "f0_iqr", "centroid", "dist")

	type row struct {
		label   string
		session int
		vp      voicePrint
		first   bool // first window of a session
	}
	var rows []row
	for _, seg := range col.segs {
		if outdir != "" {
			_ = os.MkdirAll(outdir, 0o755)
			if err := writeWAV(filepath.Join(outdir, seg.name()+".wav"), seg.pcm, outRate); err != nil {
				fmt.Printf("  wav: %v\n", err)
			}
		}
		step := int(windowSeconds * outRate * 2)
		if step <= 0 {
			step = len(seg.pcm)
		}
		for off, n := 0, 0; off < len(seg.pcm); off, n = off+step, n+1 {
			chunk := seg.pcm[off:min(off+step, len(seg.pcm))]
			// A stub at the end of a session says nothing reliable.
			if float64(len(chunk))/(outRate*2) < windowSeconds/3 {
				continue
			}
			rows = append(rows, row{
				label:   fmt.Sprintf("%s_w%02d", seg.name(), n+1),
				session: seg.session,
				vp:      fingerprint(chunk, outRate),
				first:   n == 0,
			})
		}
	}

	var f0s []float64
	for i, r := range rows {
		dist, mark := 0.0, ""
		if i > 0 {
			dist = distance(rows[i-1].vp, r.vp)
			if r.session != rows[i-1].session {
				mark = "  <-- session swap"
			} else if dist > voiceChangeThreshold {
				mark = "  <-- CHANGED mid-session"
			}
		}
		fmt.Printf("  %-12s %6.1fs %5.0f%% %8.1f %7.1f %8.0f %6.2f%s\n",
			r.label, float64(i)*windowSeconds, r.vp.voiced*100,
			r.vp.f0Median, r.vp.f0IQR, r.vp.centroid, dist, mark)
		if r.vp.f0Median > 0 {
			f0s = append(f0s, r.vp.f0Median)
		}
	}
	if len(f0s) > 1 {
		sort.Float64s(f0s)
		fmt.Printf("  f0 across windows: %.1f .. %.1f Hz (spread %.1f)\n",
			f0s[0], f0s[len(f0s)-1], f0s[len(f0s)-1]-f0s[0])
	}

	fmt.Printf("\n  === arrival gaps over %.2fs, and what a player would do ===\n", gapFloor)
	if len(col.gaps) == 0 {
		fmt.Printf("  none — the stream never paused for that long\n")
	}
	var audible float64
	for _, g := range col.gaps {
		verdict := "covered by the buffer"
		if g.silence > 0 {
			verdict = fmt.Sprintf("SILENT for %.2fs", g.silence)
			audible += g.silence
		}
		tag := ""
		if g.crossSession {
			tag = " [across a session swap]"
		}
		fmt.Printf("  at %6.1fs  gap %.2fs  reserve %+.2fs  -> %s%s\n",
			g.at.Seconds(), g.gap, g.reserve, verdict, tag)
	}
	fmt.Printf("  total audible silence attributable to arrival: %.2fs\n", audible)
}
