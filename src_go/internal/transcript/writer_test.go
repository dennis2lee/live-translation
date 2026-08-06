package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "00:00"},
		{-5, "00:00"},
		{9.9, "00:09"},
		{75, "01:15"},
		{3600, "01:00:00"},
		{3725, "01:02:05"},
	}
	for _, c := range cases {
		if got := FormatElapsed(c.in); got != c.want {
			t.Errorf("FormatElapsed(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFilenameIsTheSessionStart(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 7, 24, 14, 30, 52, 0, time.Local)
	w := New(Options{Dir: dir})
	path := w.Open(started)
	if filepath.Base(path) != "2026-07-24_143052.md" {
		t.Errorf("filename = %q", filepath.Base(path))
	}
}

// Two sessions can start inside the same second.
func TestSecondSessionInTheSameSecondGetsItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 7, 24, 14, 30, 52, 0, time.Local)

	first := New(Options{Dir: dir})
	firstPath := first.Open(started)
	first.AppendLine("Something.", 1)

	second := New(Options{Dir: dir})
	secondPath := second.Open(started)
	if secondPath == firstPath {
		t.Fatal("second session overwrote the first")
	}
	if filepath.Base(secondPath) != "2026-07-24_143052-2.md" {
		t.Errorf("second filename = %q", filepath.Base(secondPath))
	}
}

func TestHeaderLinesAndFooter(t *testing.T) {
	dir := t.TempDir()
	w := New(Options{Dir: dir, TargetLanguage: "en", Model: "gemini-3.5-live-translate-preview"})
	path := w.Open(time.Now())

	w.AppendLine("Revenue is up fourteen percent.", 75)
	w.NoteReconnect(80, 412)
	w.NoteDirection("EN→KO", 90)
	w.AppendLine("  Another line.  ", 95)
	w.Close(120, 1)

	body := read(t, path)
	for _, want := range []string{
		"# LiveCap transcript",
		"- **Target language** en",
		"- **Model** gemini-3.5-live-translate-preview",
		"`01:15` Revenue is up fourteen percent.",
		"> _reconnected at 01:20, gap 412 ms_",
		"> _direction: EN→KO at 01:30_",
		"`01:35` Another line.",
		"- **Duration** 02:00",
		"- **Lines** 2",
		"- **Reconnects** 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("transcript is missing %q\n---\n%s", want, body)
		}
	}
}

// A transcript that captured nothing is not worth keeping.
func TestEmptyTranscriptIsRemoved(t *testing.T) {
	dir := t.TempDir()
	w := New(Options{Dir: dir})
	path := w.Open(time.Now())
	if got := w.Close(10, 0); got != "" {
		t.Errorf("Close returned %q for an empty transcript", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("empty transcript was left on disk")
	}
}

// Recording must never interrupt translation.
func TestWritingBeforeOpenIsHarmless(t *testing.T) {
	w := New(Options{Dir: t.TempDir()})
	w.AppendLine("dropped", 1)
	w.NoteReconnect(1, 1)
	w.NoteDirection("KO→EN", 1)
	if got := w.Close(1, 0); got != "" {
		t.Errorf("Close on an unopened writer returned %q", got)
	}
	if w.LinesWritten() != 0 {
		t.Errorf("LinesWritten = %d", w.LinesWritten())
	}
}

func TestBlankLinesAreNotRecorded(t *testing.T) {
	dir := t.TempDir()
	w := New(Options{Dir: dir})
	w.Open(time.Now())
	w.AppendLine("   ", 1)
	w.AppendLine("", 2)
	if w.LinesWritten() != 0 {
		t.Errorf("LinesWritten = %d, want 0", w.LinesWritten())
	}
}

func TestResolveDirExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got := ResolveDir(""); got != filepath.Join(home, "Documents", "LiveCap") {
		t.Errorf("ResolveDir(\"\") = %q", got)
	}
	if got := ResolveDir("/tmp/x"); got != "/tmp/x" {
		t.Errorf("ResolveDir absolute = %q", got)
	}
}

// Opening twice must not create a second file or lose the first.
func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	w := New(Options{Dir: dir})
	first := w.Open(time.Now())
	second := w.Open(time.Now())
	if first != second {
		t.Errorf("second Open returned %q, want %q", second, first)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("%d files in the directory, want 1", len(entries))
	}
}
