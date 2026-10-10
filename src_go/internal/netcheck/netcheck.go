// Package netcheck is an internet speed test, sized against what a Live
// session actually needs.
//
// A translation session is a continuous upstream of 16 kHz / 16-bit mono PCM —
// 32 kB/s, or 256 kbit/s — with transcriptions coming back a fragment at a
// time. Upstream capacity and round-trip latency therefore matter far more than
// download speed, which is the opposite of what a general speed test
// emphasises.
//
// Three things are measured:
//
//   - latency and jitter to the Live API host itself, by timing TCP handshakes.
//     Nothing is sent, so this costs nothing and needs no API key.
//   - upload throughput, which is what carries the audio.
//   - download throughput, for completeness.
//
// Throughput uses Cloudflare's public speed-test endpoints. The payload is
// zero-filled, so no audio, transcript or key material leaves the machine.
//
// Stdlib only here — no UI and no genai — so it stays unit-testable.
package netcheck

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// LiveAPIHost is what a session connects to. Latency to anywhere else is a
	// guess.
	LiveAPIHost = "generativelanguage.googleapis.com"
	LiveAPIPort = 443
	// OpenAIHost is where an OpenAI translation session connects.
	OpenAIHost = "api.openai.com"

	SpeedHost   = "speed.cloudflare.com"
	downloadURL = "https://" + SpeedHost + "/__down?bytes=%d"
	uploadURL   = "https://" + SpeedHost + "/__up"

	// UserAgent identifies the application. Cloudflare's endpoints answer the
	// default Go/Python client string with 403, so this is load-bearing — do
	// not remove it.
	UserAgent = "LiveCap/network-check"

	LatencySamples = 5
	DownloadBytes  = 4_000_000
	UploadBytes    = 2_000_000
	Timeout        = 10 * time.Second
	// A TCP handshake needs nothing like this long; the cap only bounds how
	// long the whole check can take when the network is dead.
	LatencyTimeout = 3 * time.Second

	// AudioKbps is 16000 samples/s × 2 bytes × 8 bits, one way — Gemini's.
	AudioKbps = 256.0

	// OpenAIAudioKbps is what an OpenAI session sends: 24000 samples/s × 2
	// bytes × 8 bits is 384, and the audio goes as base64 inside JSON, a
	// third more again.
	OpenAIAudioKbps = 512.0

	// UploadComfortableKbps is the headroom over the bare audio rate. Below
	// this, a session works until anything else on the network wants bandwidth.
	UploadComfortableKbps = AudioKbps * 4

	// LatencyPoorMS is where the captions trail the speaker enough to notice.
	LatencyOKMS   = 150.0
	LatencyPoorMS = 300.0

	// Jitter is what actually produces stalls: a steady 200 ms is easier to
	// watch than an average of 80 ms that spikes to 400.
	JitterOKMS   = 40.0
	JitterPoorMS = 100.0
)

// Latency holds round-trip times to a host, from TCP handshakes.
type Latency struct {
	Host      string
	SamplesMS []float64
	Failures  int
	Err       string
}

// OK reports whether any handshake succeeded.
func (l Latency) OK() bool { return len(l.SamplesMS) > 0 }

// BestMS is the fastest handshake.
func (l Latency) BestMS() float64 {
	if !l.OK() {
		return 0
	}
	best := l.SamplesMS[0]
	for _, v := range l.SamplesMS[1:] {
		if v < best {
			best = v
		}
	}
	return best
}

// MedianMS is the typical handshake.
func (l Latency) MedianMS() float64 {
	if !l.OK() {
		return 0
	}
	sorted := append([]float64(nil), l.SamplesMS...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// JitterMS is the spread of the samples — the part that causes stalls.
func (l Latency) JitterMS() float64 {
	if len(l.SamplesMS) < 2 {
		return 0
	}
	lo, hi := l.SamplesMS[0], l.SamplesMS[0]
	for _, v := range l.SamplesMS[1:] {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	return hi - lo
}

// Throughput is one direction of a transfer.
type Throughput struct {
	Direction  string
	BytesMoved int
	Seconds    float64
	Err        string
}

// OK reports whether anything actually moved.
func (t Throughput) OK() bool { return t.BytesMoved > 0 && t.Seconds > 0 }

// Kbps is kilobits per second.
func (t Throughput) Kbps() float64 {
	if !t.OK() {
		return 0
	}
	return float64(t.BytesMoved) * 8.0 / t.Seconds / 1000.0
}

// Mbps is megabits per second.
func (t Throughput) Mbps() float64 { return t.Kbps() / 1000.0 }

// Report is the whole check.
type Report struct {
	Latency  Latency
	Upload   Throughput
	Download Throughput
	// NeedKbps is the upload the chosen provider's audio takes; zero means
	// Gemini's AudioKbps.
	NeedKbps float64
}

// need is the upload this report judges against.
func (r Report) need() float64 {
	if r.NeedKbps > 0 {
		return r.NeedKbps
	}
	return AudioKbps
}

// Verdict is one line saying whether this connection can carry a session.
func (r Report) Verdict() string {
	switch {
	case !r.Latency.OK() && !r.Upload.OK():
		return "No connection — the network is unreachable"
	case !r.Latency.OK():
		return fmt.Sprintf("Cannot reach %s — a session will not connect", r.Latency.Host)
	case !r.Upload.OK():
		// Latency worked, so the network is up; only the speed test failed.
		return fmt.Sprintf("Latency is %.0f ms, but the speed test could not run — %s",
			r.Latency.MedianMS(), orDefault(r.Upload.Err, "no data"))
	case r.Upload.Kbps() < r.need():
		return fmt.Sprintf("Too slow — %.0f kbit/s up cannot carry the %.0f kbit/s of audio a session needs",
			r.Upload.Kbps(), r.need())
	case r.Latency.MedianMS() > LatencyPoorMS:
		return fmt.Sprintf("Usable, but %.0f ms to the API will leave the captions noticeably behind the speaker",
			r.Latency.MedianMS())
	case r.Latency.JitterMS() > JitterPoorMS:
		return fmt.Sprintf("Unsteady — latency swings by %.0f ms, so expect captions to stall and then catch up",
			r.Latency.JitterMS())
	case r.Upload.Kbps() < r.need()*UploadComfortableKbps/AudioKbps:
		return fmt.Sprintf("Enough for one session (%.0f kbit/s up), but with little to spare for anything else on this network",
			r.Upload.Kbps())
	default:
		return "Good — plenty of headroom for a translation session"
	}
}

// SummaryLines is human-readable detail, one measurement per line.
func (r Report) SummaryLines() []string {
	var out []string
	lat := r.Latency
	if lat.OK() {
		line := fmt.Sprintf("Latency to %s: %.0f ms median, %.0f ms best, %.0f ms jitter",
			lat.Host, lat.MedianMS(), lat.BestMS(), lat.JitterMS())
		if lat.Failures > 0 {
			line += fmt.Sprintf(" (%d failed)", lat.Failures)
		}
		out = append(out, line)
	} else {
		out = append(out, fmt.Sprintf("Latency to %s: unreachable — %s",
			lat.Host, orDefault(lat.Err, "no response")))
	}
	for _, flow := range []Throughput{r.Upload, r.Download} {
		name := capitalise(flow.Direction)
		if flow.OK() {
			out = append(out, fmt.Sprintf("%s: %.1f Mbit/s (%.1f MB in %.1f s)",
				name, flow.Mbps(), float64(flow.BytesMoved)/1_000_000, flow.Seconds))
		} else {
			out = append(out, fmt.Sprintf("%s: failed — %s", name, orDefault(flow.Err, "no data")))
		}
	}
	return out
}

// --- measurement ----------------------------------------------------------

// MeasureLatency times TCP handshakes to host. One handshake is one round trip.
//
// ICMP ping is not usable here — it needs a raw socket, and Google's front end
// does not answer it consistently anyway.
func MeasureLatency(host string, port, samples int, timeout time.Duration) Latency {
	if host == "" {
		host = LiveAPIHost
	}
	if port == 0 {
		port = LiveAPIPort
	}
	if samples < 1 {
		samples = 1
	}
	if timeout == 0 {
		timeout = LatencyTimeout
	}
	result := Latency{Host: host}
	address := net.JoinHostPort(host, fmt.Sprint(port))
	for i := 0; i < samples; i++ {
		started := time.Now()
		conn, err := net.DialTimeout("tcp", address, timeout)
		if err != nil {
			result.Failures++
			result.Err = truncate(err.Error(), 120)
			continue
		}
		elapsed := time.Since(started)
		_ = conn.Close()
		result.SamplesMS = append(result.SamplesMS, float64(elapsed.Microseconds())/1000.0)
	}
	return result
}

// MeasureDownload fetches nbytes of zeros and times it.
func MeasureDownload(baseURL string, nbytes int, timeout time.Duration) Throughput {
	flow := Throughput{Direction: "download"}
	if nbytes <= 0 {
		nbytes = DownloadBytes
	}
	url := baseURL
	if url == "" {
		url = fmt.Sprintf(downloadURL, nbytes)
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		flow.Err = truncate(err.Error(), 120)
		return flow
	}
	req.Header.Set("User-Agent", UserAgent)

	started := time.Now()
	resp, err := client(timeout).Do(req)
	if err != nil {
		flow.Err = truncate(err.Error(), 120)
		return flow
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		flow.Err = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return flow
	}
	read, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		flow.Err = truncate(err.Error(), 120)
		return flow
	}
	flow.Seconds = time.Since(started).Seconds()
	flow.BytesMoved = int(read)
	return flow
}

// MeasureUpload posts nbytes of zeros and times it. This is the direction the
// audio travels, and the only one that decides whether a session works.
func MeasureUpload(url string, nbytes int, timeout time.Duration) Throughput {
	flow := Throughput{Direction: "upload"}
	if nbytes <= 0 {
		nbytes = UploadBytes
	}
	if url == "" {
		url = uploadURL
	}

	// Zero-filled deliberately: no audio, transcript or key material leaves
	// the machine. bytes.NewReader, not strings.NewReader(string(payload)) —
	// that conversion copied the whole two megabytes for nothing.
	payload := make([]byte, nbytes)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		flow.Err = truncate(err.Error(), 120)
		return flow
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", UserAgent)
	req.ContentLength = int64(nbytes)

	started := time.Now()
	resp, err := client(timeout).Do(req)
	if err != nil {
		flow.Err = truncate(err.Error(), 120)
		return flow
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		flow.Err = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return flow
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	flow.Seconds = time.Since(started).Seconds()
	flow.BytesMoved = nbytes
	return flow
}

// KbpsFor is the upload a provider's sessions take: "openai", or anything
// else for Gemini.
func KbpsFor(provider string) float64 {
	if provider == "openai" {
		return OpenAIAudioKbps
	}
	return AudioKbps
}

// HostFor is the server a provider's sessions connect to: "openai", or
// anything else for Gemini.
func HostFor(provider string) string {
	if provider == "openai" {
		return OpenAIHost
	}
	return LiveAPIHost
}

// Run runs the whole check for Gemini. Blocking — call it off the UI
// goroutine.
func Run() Report { return RunFor("gemini") }

// RunFor runs the whole check for a provider: the round trip to the server
// its sessions connect to, and the upload judged against what its audio
// takes.
func RunFor(provider string) Report {
	report := Report{
		Latency:  MeasureLatency(HostFor(provider), LiveAPIPort, LatencySamples, LatencyTimeout),
		NeedKbps: KbpsFor(provider),
	}
	report.Upload = MeasureUpload("", UploadBytes, Timeout)
	report.Download = MeasureDownload("", DownloadBytes, Timeout)
	slog.Info("network check", "verdict", report.Verdict())
	for _, line := range report.SummaryLines() {
		slog.Info("  " + line)
	}
	return report
}

func client(timeout time.Duration) *http.Client {
	if timeout == 0 {
		timeout = Timeout
	}
	return &http.Client{Timeout: timeout}
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// truncate caps an error message for display, on a rune boundary. Cutting at a
// byte offset would split a multi-byte character and put a replacement glyph on
// the end of the message.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
