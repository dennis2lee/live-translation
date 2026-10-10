package netcheck

import (
	"strings"
	"testing"
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
