package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// restore puts the global logger back, so one test cannot silence another.
func withTempLog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(SetDirForTests(dir))
	previous := slog.Default()
	t.Cleanup(func() {
		Close()
		slog.SetDefault(previous)
	})
	return dir
}

func readLog(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, Filename))
	if err != nil {
		t.Fatalf("could not read the log: %v", err)
	}
	return string(data)
}

// An installed .app has no terminal, so stderr alone loses everything it
// writes. The log has to reach a file.
func TestSetupWritesToAFile(t *testing.T) {
	dir := withTempLog(t)

	path := Setup(false)
	if path == "" {
		t.Fatal("Setup reported no log file")
	}
	if want := filepath.Join(dir, Filename); path != want {
		t.Errorf("log path = %q, want %q", path, want)
	}
	slog.Info("hello from the test")

	if got := readLog(t, dir); !strings.Contains(got, "hello from the test") {
		t.Errorf("the line did not reach the file: %q", got)
	}
}

// The Preferences checkbox has to take effect in the running process. It did
// not: the level was baked into the handler at startup, so the setting was
// saved and silently ignored until the next launch.
func TestDebugLevelChangesWithoutARestart(t *testing.T) {
	dir := withTempLog(t)
	Setup(false)

	slog.Debug("before the toggle")
	if got := readLog(t, dir); strings.Contains(got, "before the toggle") {
		t.Error("a debug line was written while debug logging was off")
	}

	SetDebug(true)
	if !Debug() {
		t.Error("Debug() = false after SetDebug(true)")
	}
	slog.Debug("after the toggle")
	if got := readLog(t, dir); !strings.Contains(got, "after the toggle") {
		t.Errorf("the debug line did not appear after the toggle: %q", got)
	}

	// And back off again, which is the half a one-way switch would miss.
	SetDebug(false)
	slog.Debug("after turning it off")
	if got := readLog(t, dir); strings.Contains(got, "after turning it off") {
		t.Error("debug lines continued after the toggle was turned off")
	}
}

// An oversized log is rolled aside rather than growing without limit.
func TestLogRollsOverWhenLarge(t *testing.T) {
	dir := withTempLog(t)
	path := filepath.Join(dir, Filename)

	if err := os.WriteFile(path, make([]byte, MaxBytes+1), 0o600); err != nil {
		t.Fatalf("could not seed the log: %v", err)
	}
	Setup(false)
	slog.Info("first line of the new log")

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("the old log was not rolled aside: %v", err)
	}
	got := readLog(t, dir)
	if len(got) > 4096 {
		t.Errorf("the new log did not start empty: %d bytes", len(got))
	}
	if !strings.Contains(got, "first line of the new log") {
		t.Error("the new log is missing its first line")
	}
}

// A log that cannot be written must never stop the application starting.
func TestSetupSurvivesAnUnwritableDirectory(t *testing.T) {
	dir := withTempLog(t)

	// A file where the directory should be, so MkdirAll cannot succeed.
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("could not seed: %v", err)
	}
	t.Cleanup(SetDirForTests(blocked))

	if path := Setup(true); path != "" {
		t.Errorf("Setup reported a path it could not have opened: %q", path)
	}
	slog.Info("this must not panic")
	if !Debug() {
		t.Error("the level was not applied when the file could not be opened")
	}
}
