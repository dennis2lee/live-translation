package netcheck

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLatencyStatistics(t *testing.T) {
	l := Latency{Host: "h", SamplesMS: []float64{10, 30, 20, 50}}
	if got := l.BestMS(); got != 10 {
		t.Errorf("BestMS = %v", got)
	}
	if got := l.MedianMS(); got != 25 {
		t.Errorf("MedianMS = %v, want the mean of the middle pair", got)
	}
	if got := l.JitterMS(); got != 40 {
		t.Errorf("JitterMS = %v", got)
	}

	odd := Latency{SamplesMS: []float64{10, 30, 20}}
	if got := odd.MedianMS(); got != 20 {
		t.Errorf("odd MedianMS = %v", got)
	}
	var empty Latency
	if empty.OK() || empty.BestMS() != 0 || empty.MedianMS() != 0 || empty.JitterMS() != 0 {
		t.Error("an empty Latency should read as zero throughout")
	}
	single := Latency{SamplesMS: []float64{12}}
	if single.JitterMS() != 0 {
		t.Error("one sample has no jitter")
	}
}

func TestThroughputRates(t *testing.T) {
	// 1 MB in 1 s = 8000 kbit/s.
	flow := Throughput{Direction: "upload", BytesMoved: 1_000_000, Seconds: 1}
	if got := flow.Kbps(); got != 8000 {
		t.Errorf("Kbps = %v", got)
	}
	if got := flow.Mbps(); got != 8 {
		t.Errorf("Mbps = %v", got)
	}
	if (Throughput{}).OK() {
		t.Error("a zero Throughput must not read as OK")
	}
}

// The verdict has to name the actual problem, not shrug.
func TestVerdicts(t *testing.T) {
	fast := Throughput{Direction: "upload", BytesMoved: 2_000_000, Seconds: 1} // 16000 kbit/s
	good := Latency{Host: "h", SamplesMS: []float64{20, 22, 24}}

	cases := []struct {
		name   string
		report Report
		want   string
	}{
		{
			"nothing works",
			Report{},
			"No connection",
		},
		{
			"speed test fine, host unreachable",
			Report{Latency: Latency{Host: "h", Err: "timeout"}, Upload: fast},
			"Cannot reach h",
		},
		{
			// The bug this replaced: both transfers failing used to read "Good".
			"latency fine, transfers failed",
			Report{Latency: good, Upload: Throughput{Direction: "upload", Err: "HTTP 403"}},
			"the speed test could not run — HTTP 403",
		},
		{
			"too slow for the audio",
			Report{Latency: good, Upload: Throughput{Direction: "upload", BytesMoved: 20_000, Seconds: 1}},
			"Too slow",
		},
		{
			"high latency",
			Report{
				Latency: Latency{Host: "h", SamplesMS: []float64{400, 400, 400}},
				Upload:  fast,
			},
			"noticeably behind the speaker",
		},
		{
			"jittery",
			Report{
				Latency: Latency{Host: "h", SamplesMS: []float64{20, 200, 20}},
				Upload:  fast,
			},
			"Unsteady",
		},
		{
			"workable but tight",
			Report{
				Latency: good,
				Upload:  Throughput{Direction: "upload", BytesMoved: 50_000, Seconds: 1}, // 400 kbit/s
			},
			"little to spare",
		},
		{
			"plenty",
			Report{Latency: good, Upload: fast},
			"Good",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.report.Verdict(); !strings.Contains(got, c.want) {
				t.Errorf("Verdict = %q, want it to contain %q", got, c.want)
			}
		})
	}
}

func TestSummaryLinesReportFailuresPlainly(t *testing.T) {
	r := Report{
		Latency:  Latency{Host: "h", Err: "no route"},
		Upload:   Throughput{Direction: "upload", Err: "HTTP 403"},
		Download: Throughput{Direction: "download", BytesMoved: 4_000_000, Seconds: 2},
	}
	lines := r.SummaryLines()
	if len(lines) != 3 {
		t.Fatalf("%d lines, want 3", len(lines))
	}
	if !strings.Contains(lines[0], "unreachable — no route") {
		t.Errorf("latency line = %q", lines[0])
	}
	if !strings.Contains(lines[1], "Upload: failed — HTTP 403") {
		t.Errorf("upload line = %q", lines[1])
	}
	if !strings.Contains(lines[2], "Download: 16.0 Mbit/s") {
		t.Errorf("download line = %q", lines[2])
	}
}

// The User-Agent is load-bearing: Cloudflare answers the default client string
// with 403.
func TestRequestsCarryTheUserAgent(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("User-Agent"))
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			// The payload must be zeros — nothing from this machine.
			for _, b := range body {
				if b != 0 {
					t.Error("upload payload was not zero-filled")
					break
				}
			}
		}
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer srv.Close()

	down := MeasureDownload(srv.URL, 1024, time.Second*5)
	if !down.OK() {
		t.Errorf("download failed: %s", down.Err)
	}
	up := MeasureUpload(srv.URL, 4096, time.Second*5)
	if !up.OK() {
		t.Errorf("upload failed: %s", up.Err)
	}
	for _, ua := range seen {
		if ua != UserAgent {
			t.Errorf("User-Agent = %q, want %q", ua, UserAgent)
		}
	}
}

// A 403 must be reported as a failure, not counted as throughput.
func TestHTTPErrorIsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	if up := MeasureUpload(srv.URL, 1024, time.Second); up.OK() || !strings.Contains(up.Err, "403") {
		t.Errorf("upload = %+v, want a 403 failure", up)
	}
	if down := MeasureDownload(srv.URL, 1024, time.Second); down.OK() || !strings.Contains(down.Err, "403") {
		t.Errorf("download = %+v, want a 403 failure", down)
	}
}

func TestLatencyAgainstALocalListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot listen locally")
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}

	lat := MeasureLatency(host, port, 3, time.Second)
	if !lat.OK() || len(lat.SamplesMS) != 3 {
		t.Fatalf("latency = %+v", lat)
	}
	if lat.Failures != 0 {
		t.Errorf("%d handshakes failed against a local listener", lat.Failures)
	}
}

// An unreachable port must be counted, not silently ignored.
func TestUnreachablePortIsCounted(t *testing.T) {
	lat := MeasureLatency("127.0.0.1", 1, 2, 200*time.Millisecond)
	if lat.OK() {
		t.Error("port 1 answered, which is not expected here")
	}
	if lat.Failures != 2 {
		t.Errorf("Failures = %d, want 2", lat.Failures)
	}
	if lat.Err == "" {
		t.Error("no error recorded for an unreachable host")
	}
}
