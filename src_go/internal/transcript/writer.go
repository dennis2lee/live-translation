// Package transcript writes the translation to a Markdown file.
//
// The filename is the time translation started: 2026-07-24_143052.md.
//
// This package must stay free of UI and network dependencies, and covered by
// unit tests. A failure to record must never interrupt translation, so I/O
// errors are swallowed and only logged.
package transcript

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultDir is where transcripts land unless the settings say otherwise.
	DefaultDir = "~/Documents/LiveCap"
	// FilenameFormat is the session start time, as a filename.
	FilenameFormat = "2006-01-02_150405"

	stampFormat = "2006-01-02 15:04:05"
)

// ResolveDir expands ~ and falls back to the default.
func ResolveDir(raw string) string {
	if strings.TrimSpace(raw) == "" {
		raw = DefaultDir
	}
	if strings.HasPrefix(raw, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(raw, "~"))
		}
	}
	return raw
}

// FormatElapsed renders a duration as mm:ss, or hh:mm:ss past an hour.
func FormatElapsed(seconds float64) string {
	total := int(seconds)
	if total < 0 {
		total = 0
	}
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// Writer appends settled translation lines as Markdown.
//
// Every line is flushed, so a crash cannot lose what was already written.
// A Writer is used from one goroutine only — the UI dispatcher, which is where
// drained lines arrive.
type Writer struct {
	dir            string
	targetLanguage string
	model          string

	file         *os.File
	path         string
	startedAt    time.Time
	linesWritten int
}

// Options configures a Writer.
type Options struct {
	Dir            string
	TargetLanguage string
	Model          string
}

// New builds a Writer. Nothing is created on disk until Open.
func New(opts Options) *Writer {
	lang := opts.TargetLanguage
	if lang == "" {
		lang = "en"
	}
	return &Writer{
		dir:            ResolveDir(opts.Dir),
		targetLanguage: lang,
		model:          opts.Model,
	}
}

// IsOpen reports whether a file is being recorded to.
func (w *Writer) IsOpen() bool { return w.file != nil }

// LinesWritten is how many translation lines have been recorded.
func (w *Writer) LinesWritten() int { return w.linesWritten }

// Path is the file being written, or "" when there is none.
func (w *Writer) Path() string { return w.path }

// Open creates the file and writes the header. It returns "" on failure;
// translation continues regardless.
func (w *Writer) Open(startedAt time.Time) string {
	if w.file != nil {
		return w.path
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	w.startedAt = startedAt

	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		slog.Error("could not create the transcript directory", "err", err)
		return ""
	}
	path := w.uniquePath(startedAt)
	file, err := os.Create(path)
	if err != nil {
		slog.Error("could not open transcript file", "err", err)
		return ""
	}
	w.file = file
	w.path = path

	header := "# LiveCap transcript\n\n" +
		"- **Started** " + startedAt.Format(stampFormat) + "\n" +
		"- **Target language** " + w.targetLanguage + "\n"
	if w.model != "" {
		header += "- **Model** " + w.model + "\n"
	}
	header += "\n---\n\n"
	w.write(header)

	slog.Info("recording transcript", "path", w.path)
	return w.path
}

// uniquePath avoids colliding with a session that started in the same second.
func (w *Writer) uniquePath(startedAt time.Time) string {
	base := startedAt.Format(FilenameFormat)
	candidate := filepath.Join(w.dir, base+".md")
	for n := 2; ; n++ {
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
		candidate = filepath.Join(w.dir, fmt.Sprintf("%s-%d.md", base, n))
	}
}

// AppendLine records one settled translation line.
func (w *Writer) AppendLine(text string, elapsedSeconds float64) {
	text = strings.TrimSpace(text)
	if text == "" || w.file == nil {
		return
	}
	w.write("`" + FormatElapsed(elapsedSeconds) + "` " + text + "\n\n")
	w.linesWritten++
}

// NoteReconnect marks where a reconnect happened, for locating gaps afterwards.
func (w *Writer) NoteReconnect(elapsedSeconds, gapMS float64) {
	if w.file == nil {
		return
	}
	w.write(fmt.Sprintf("> _reconnected at %s, gap %.0f ms_\n\n",
		FormatElapsed(elapsedSeconds), gapMS))
}

// NoteDirection marks where the translation direction changed.
func (w *Writer) NoteDirection(label string, elapsedSeconds float64) {
	if w.file == nil {
		return
	}
	w.write(fmt.Sprintf("> _direction: %s at %s_\n\n", label, FormatElapsed(elapsedSeconds)))
}

// Close writes the footer and closes the file. A transcript that captured
// nothing is removed rather than left behind. It returns the saved path, or ""
// when nothing was kept.
func (w *Writer) Close(elapsedSeconds float64, reconnects int) string {
	if w.file == nil {
		return ""
	}
	w.write("---\n\n" +
		"- **Ended** " + time.Now().Format(stampFormat) + "\n" +
		"- **Duration** " + FormatElapsed(elapsedSeconds) + "\n" +
		fmt.Sprintf("- **Lines** %d\n", w.linesWritten) +
		fmt.Sprintf("- **Reconnects** %d\n", reconnects))

	// A failed footer write already closed and cleared the file.
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			slog.Warn("error closing transcript", "err", err)
		}
		w.file = nil
	}

	path := w.path
	if path != "" && w.linesWritten == 0 {
		if err := os.Remove(path); err == nil {
			slog.Info("removed empty transcript", "path", path)
			w.path = ""
			return ""
		}
	}
	slog.Info("transcript saved", "path", path, "lines", w.linesWritten)
	return path
}

func (w *Writer) write(text string) {
	if w.file == nil {
		return
	}
	if _, err := w.file.WriteString(text); err != nil {
		slog.Error("transcript write failed, recording stopped", "err", err)
		_ = w.file.Close()
		w.file = nil
		return
	}
	// Survive a crash.
	if err := w.file.Sync(); err != nil {
		slog.Error("transcript flush failed, recording stopped", "err", err)
		_ = w.file.Close()
		w.file = nil
	}
}
