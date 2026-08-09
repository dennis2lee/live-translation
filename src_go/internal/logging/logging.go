// Package logging is the application's one log destination and level.
//
// Two things live here that used to live in main, and each was a defect.
//
// The level is held in a slog.LevelVar rather than baked into the handler at
// startup, so Preferences can turn debug logging on in a running process.
// It could not before: setupLogging ran once, and the checkbox saved a setting
// that nothing read until the next launch — silently, with nothing on screen
// to say so.
//
// The log goes to a file as well as stderr. It used to go to stderr alone,
// which an .app launched from Finder does not have, so an installed build
// discarded every line it wrote. That made the fragment-gap measurement behind
// captions.IdleCommitSeconds impossible to collect from the builds people
// actually run.
//
// No secrets are logged anywhere in this application — the API key never
// reaches slog — so a plain file at 0600 is the whole of the protection needed.
package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/dennis2lee/live-translation/src_go/internal/platform"
)

// Filename of the log inside the application-support directory.
const Filename = "livecap.log"

// MaxBytes is when the log is rolled over to Filename+".1", replacing whatever
// was there. One generation is enough: the log exists to be read now, not to be
// an archive, and debug logging writes roughly one line per second of speech.
const MaxBytes = 8 << 20 // 8 MB

// level is the live level. Every handler built here reads it, so changing it
// changes the running process.
var level = new(slog.LevelVar)

// file is the open log file, kept so Close can release it.
var file *os.File

// Path is the log file. Empty when the file could not be opened.
func Path() string {
	if file == nil {
		return ""
	}
	return file.Name()
}

// Setup installs the handler. It reports the log file's path, or "" when only
// stderr is available — a log that cannot be written must never stop the
// application starting.
func Setup(debug bool) string {
	SetDebug(debug)

	var writer io.Writer = os.Stderr
	if f, err := openLogFile(); err == nil {
		file = f
		writer = io.MultiWriter(os.Stderr, f)
	} else {
		// Reported through the handler installed below, so it is not lost.
		defer slog.Warn("could not open the log file — logging to stderr only", "err", err)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: level})))
	return Path()
}

// SetDebug changes the level of the running process. This is what the
// Preferences checkbox drives.
func SetDebug(debug bool) {
	if debug {
		level.Set(slog.LevelDebug)
		return
	}
	level.Set(slog.LevelInfo)
}

// Debug reports whether debug logging is on.
func Debug() bool { return level.Level() == slog.LevelDebug }

// Close releases the file. Called from shutdown; safe when nothing is open.
func Close() {
	if file == nil {
		return
	}
	_ = file.Close()
	file = nil
}

// dirOverride lets tests redirect the log. Never set outside tests.
var dirOverride string

func logDir() string {
	if dirOverride != "" {
		return dirOverride
	}
	return platform.AppSupportDir()
}

func openLogFile() (*os.File, error) {
	dir := logDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, Filename)
	rollIfLarge(path)
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// rollIfLarge moves an oversized log aside so the new one starts empty.
// Failure is not reported: an unrolled log is better than no log.
func rollIfLarge(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < MaxBytes {
		return
	}
	_ = os.Rename(path, path+".1")
}
