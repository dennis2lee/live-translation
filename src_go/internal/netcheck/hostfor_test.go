package netcheck

import (
	"strings"
	"testing"
	"time"
)

// The round trip that matters is to the server the chosen provider's
// sessions connect to.
func TestHostForFollowsTheProvider(t *testing.T) {
	if HostFor("openai") != OpenAIHost || HostFor("gemini") != LiveAPIHost || HostFor("") != LiveAPIHost {
		t.Errorf("openai %q, gemini %q, empty %q", HostFor("openai"), HostFor("gemini"), HostFor(""))
	}
}

// OpenAI's audio is 24 kHz base64 in JSON: twice Gemini's upload. A link
// that carries Gemini comfortably can be too slow for it.
func TestTheUploadVerdictFollowsTheProvider(t *testing.T) {
	up := Throughput{BytesMoved: 400 * 1000 / 8, Seconds: 1} // 400 kbit/s
	lat := Latency{Host: "x", SamplesMS: []float64{20, 20, 20}}
	gemini := Report{Latency: lat, Upload: up, Download: up, NeedKbps: KbpsFor("gemini")}
	openai := Report{Latency: lat, Upload: up, Download: up, NeedKbps: KbpsFor("openai")}
	if strings.HasPrefix(gemini.Verdict(), "Too slow") {
		t.Errorf("400 kbit/s judged too slow for Gemini: %q", gemini.Verdict())
	}
	if !strings.HasPrefix(openai.Verdict(), "Too slow") {
		t.Errorf("400 kbit/s judged enough for OpenAI: %q", openai.Verdict())
	}
}

// RunFor measures the round trip to the chosen provider's server and judges
// the upload by that provider's rate. Either half going back to Gemini's
// silently undoes the other provider's check.
func TestRunForWiresTheProvider(t *testing.T) {
	var host string
	ml, mu, md := measureLatency, measureUpload, measureDownload
	t.Cleanup(func() { measureLatency, measureUpload, measureDownload = ml, mu, md })
	measureLatency = func(h string, _, _ int, _ time.Duration) Latency {
		host = h
		return Latency{Host: h, SamplesMS: []float64{20, 20, 20}}
	}
	// 1.5 Mbit/s: comfortable for Gemini (4 × 256), only enough for OpenAI
	// (4 × 512).
	tp := func(string, int, time.Duration) Throughput {
		return Throughput{BytesMoved: 1500 * 1000 / 8, Seconds: 1}
	}
	measureUpload, measureDownload = tp, tp

	g := RunFor("gemini")
	if host != LiveAPIHost || g.NeedKbps != 256 || strings.HasPrefix(g.Verdict(), "Enough") {
		t.Errorf("gemini: host %q, need %.0f, verdict %q", host, g.NeedKbps, g.Verdict())
	}
	o := RunFor("openai")
	if host != OpenAIHost || o.NeedKbps != 512 || !strings.HasPrefix(o.Verdict(), "Enough for one session") {
		t.Errorf("openai: host %q, need %.0f, verdict %q", host, o.NeedKbps, o.Verdict())
	}
}
