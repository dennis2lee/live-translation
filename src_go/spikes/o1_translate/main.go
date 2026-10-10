// Spike O1 — what does OpenAI's gpt-realtime-translate actually send?
//
// The documentation names the endpoint and a handful of events but not the
// output sample rate, the error shape, or what arrives besides the deltas.
// This streams the Korean fixture at real-time pace, logs every event type
// with its arrival time (audio payloads reduced to byte counts), and saves the
// translated audio so it can be measured offline like the Gemini recordings.
//
// This costs money — $0.034 a minute of audio. Keep -seconds short.
//
//	go run ./spikes/o1_translate -seconds 30 -out /tmp/o1
//
// The key comes from OPENAI_API_KEY, or from that one line of ../.env. It is
// sent only in the Authorization header and never printed.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/dennis2lee/live-translation/src_go/internal/resample"
)

const (
	endpoint = "wss://api.openai.com/v1/realtime/translations?model=gpt-realtime-translate"
	inRate   = 16000
	sendRate = 24000
	chunkMS  = 100
)

func loadKey(envFile string) string {
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
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "OPENAI_API_KEY="); ok {
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
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF/WAVE file")
	}
	pos := 12
	for pos+8 <= len(raw) {
		id := string(raw[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		body := pos + 8
		if id == "fmt " {
			if r := binary.LittleEndian.Uint32(raw[body+4 : body+8]); r != inRate {
				return nil, fmt.Errorf("fixture is %d Hz, need %d", r, inRate)
			}
		}
		if id == "data" {
			return raw[body:min(body+size, len(raw))], nil
		}
		pos = body + size + size%2
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

// upsample converts 16 kHz PCM16 to 24 kHz PCM16 in one pass.
func upsample(pcm []byte) ([]byte, error) {
	rs, err := resample.NewStream(inRate, sendRate)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	in := make([]float32, len(pcm)/2)
	for i := range in {
		in[i] = float32(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
	}
	out, err := rs.Process(in)
	if err != nil {
		return nil, err
	}
	tail, err := rs.Drain()
	if err != nil {
		return nil, err
	}
	out = append(out, tail...)
	b := make([]byte, 2*len(out))
	for i, v := range out {
		s := math.Round(float64(v) * 32767)
		s = max(-32768, min(32767, s))
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(s)))
	}
	return b, nil
}

func main() {
	fixture := flag.String("fixture", "spikes/fixtures/ko_fixture.wav", "16 kHz mono WAV to stream")
	seconds := flag.Float64("seconds", 30, "how much of the fixture to send")
	target := flag.String("target", "en", "output language, a bare code")
	transcribe := flag.Bool("transcribe", true, "ask for the source transcript")
	envFile := flag.String("env", "../.env", ".env to read OPENAI_API_KEY from")
	out := flag.String("out", "", "directory to save translated audio and the event log")
	flag.Parse()

	key := loadKey(*envFile)
	if key == "" {
		fmt.Fprintln(os.Stderr, "no OPENAI_API_KEY in the environment or", *envFile)
		os.Exit(1)
	}
	pcm16, err := readWAV(*fixture)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixture:", err)
		os.Exit(1)
	}
	if n := int(*seconds * inRate * 2); n < len(pcm16) {
		pcm16 = pcm16[:n]
	}
	pcm, err := upsample(pcm16)
	if err != nil {
		fmt.Fprintln(os.Stderr, "resample:", err)
		os.Exit(1)
	}

	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+key)
	t0 := time.Now()
	conn, resp, err := websocket.DefaultDialer.Dial(endpoint, hdr)
	if err != nil {
		status := ""
		if resp != nil {
			status = resp.Status
		}
		fmt.Fprintln(os.Stderr, "dial:", err, status)
		os.Exit(1)
	}
	defer conn.Close()
	fmt.Printf("connected in %d ms\n", time.Since(t0).Milliseconds())

	var wmu sync.Mutex
	send := func(v any) error {
		wmu.Lock()
		defer wmu.Unlock()
		return conn.WriteJSON(v)
	}

	audioCfg := map[string]any{"output": map[string]any{"language": *target}}
	if *transcribe {
		audioCfg["input"] = map[string]any{"transcription": map[string]any{"model": "gpt-realtime-whisper"}}
	}
	if err := send(map[string]any{"type": "session.update", "session": map[string]any{"audio": audioCfg}}); err != nil {
		fmt.Fprintln(os.Stderr, "session.update:", err)
		os.Exit(1)
	}

	var (
		mu         sync.Mutex
		outAudio   []byte
		deltaSizes = map[int]int{}
		counts     = map[string]int{}
		firstAudio time.Duration
		firstText  time.Duration
		src, tgt   strings.Builder
		sendStart  time.Time
		log        strings.Builder
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				fmt.Printf("[%6.2f] read ended: %v\n", time.Since(t0).Seconds(), err)
				return
			}
			var ev map[string]any
			if json.Unmarshal(data, &ev) != nil {
				continue
			}
			typ, _ := ev["type"].(string)
			at := time.Since(t0)
			mu.Lock()
			counts[typ]++
			fmt.Fprintf(&log, "%.3f\t%s", at.Seconds(), typ)
			switch typ {
			case "session.output_audio.delta":
				b, _ := base64.StdEncoding.DecodeString(fmt.Sprint(ev["delta"]))
				outAudio = append(outAudio, b...)
				deltaSizes[len(b)]++
				fmt.Fprintf(&log, "\t%d", len(b))
				if firstAudio == 0 && !sendStart.IsZero() {
					firstAudio = time.Since(sendStart)
				}
			case "session.output_transcript.delta":
				d := fmt.Sprint(ev["delta"])
				tgt.WriteString(d)
				fmt.Fprintf(&log, "\t%q", d)
				if firstText == 0 && !sendStart.IsZero() {
					firstText = time.Since(sendStart)
				}
				fmt.Printf("[%6.2f] EN %q\n", at.Seconds(), d)
			case "session.input_transcript.delta":
				d := fmt.Sprint(ev["delta"])
				src.WriteString(d)
				fmt.Fprintf(&log, "\t%q", d)
				fmt.Printf("[%6.2f] KO %q\n", at.Seconds(), d)
			default:
				// Everything else is printed whole: these are the events the
				// documentation does not describe.
				s := string(data)
				if len(s) > 600 {
					s = s[:600] + "…"
				}
				fmt.Printf("[%6.2f] %s\n", at.Seconds(), s)
				fmt.Fprintf(&log, "\t%s", s)
			}
			log.WriteString("\n")
			closed := typ == "session.closed"
			mu.Unlock()
			if closed {
				return
			}
		}
	}()

	time.Sleep(300 * time.Millisecond)
	chunk := sendRate * 2 * chunkMS / 1000
	mu.Lock()
	sendStart = time.Now()
	mu.Unlock()
	silence := make([]byte, sendRate*2*3) // 3 s so the tail is translated
	stream := append(append([]byte(nil), pcm...), silence...)
	next := time.Now()
	for i := 0; i < len(stream); i += chunk {
		c := stream[i:min(i+chunk, len(stream))]
		if err := send(map[string]any{"type": "session.input_audio_buffer.append",
			"audio": base64.StdEncoding.EncodeToString(c)}); err != nil {
			fmt.Println("send:", err)
			break
		}
		next = next.Add(chunkMS * time.Millisecond)
		time.Sleep(time.Until(next))
	}
	sentFor := time.Since(sendStart)
	_ = send(map[string]any{"type": "session.close"})
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		fmt.Println("no session.closed within 20 s")
	}

	mu.Lock()
	defer mu.Unlock()
	fmt.Printf("\n--- summary ---\n")
	fmt.Printf("sent %.1f s of audio in %.1f s\n", float64(len(stream))/(sendRate*2), sentFor.Seconds())
	fmt.Printf("first translated audio %.2f s after sending began, first text %.2f s\n",
		firstAudio.Seconds(), firstText.Seconds())
	types := make([]string, 0, len(counts))
	for k := range counts {
		types = append(types, k)
	}
	sort.Strings(types)
	for _, k := range types {
		fmt.Printf("  %-40s %d\n", k, counts[k])
	}
	sizes := make([]int, 0, len(deltaSizes))
	for k := range deltaSizes {
		sizes = append(sizes, k)
	}
	sort.Ints(sizes)
	fmt.Printf("audio delta sizes (bytes: count): ")
	for _, s := range sizes {
		fmt.Printf("%d:%d ", s, deltaSizes[s])
	}
	fmt.Printf("\nreceived %d bytes of audio = %.1f s at 24 kHz, %.1f s at 16 kHz\n",
		len(outAudio), float64(len(outAudio))/48000, float64(len(outAudio))/32000)
	fmt.Printf("\nsource: %s\ntarget: %s\n", src.String(), tgt.String())
	if *out != "" {
		_ = os.MkdirAll(*out, 0o755)
		_ = writeWAV(filepath.Join(*out, "translated_24k.wav"), outAudio, 24000)
		_ = os.WriteFile(filepath.Join(*out, "events.tsv"), []byte(log.String()), 0o644)
		fmt.Println("saved to", *out)
	}
}
