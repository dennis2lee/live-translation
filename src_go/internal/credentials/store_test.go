package credentials

import (
	"os"
	"path/filepath"
	"testing"
)

// isolate redirects the store at a temporary directory, the way
// tests/conftest.py does for the Python build. Every test that writes must use
// it — a test that touches the real store overwrites the key actually in use.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := dirOverride
	dirOverride = dir
	t.Cleanup(func() { dirOverride = prev })
	return dir
}

// The port's whole reason for existing: a store the Python application wrote
// must still open. The fixture was produced by credentials.py's own cipher.
func TestDecryptsAPythonWrittenStore(t *testing.T) {
	dir := isolate(t)
	blob, err := os.ReadFile(filepath.Join("testdata", "python_written.enc"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, Filename), blob, 0o600); err != nil {
		t.Fatal(err)
	}

	got := LoadAll()
	want := map[string]string{"GEMINI_API_KEY": "test-key-not-a-real-one", "OTHER": "x"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	isolate(t)
	if err := Put("GEMINI_API_KEY", "  spaced  "); err != nil {
		t.Fatal(err)
	}
	if got := Get("GEMINI_API_KEY"); got != "spaced" {
		t.Errorf("Get = %q, want the trimmed value", got)
	}
}

// A corrupt store must never stop the application starting.
func TestCorruptStoreReadsAsEmpty(t *testing.T) {
	dir := isolate(t)
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte("not a fernet token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadAll(); len(got) != 0 {
		t.Errorf("LoadAll = %v, want empty", got)
	}
}

func TestMissingStoreReadsAsEmpty(t *testing.T) {
	isolate(t)
	if got := LoadAll(); len(got) != 0 {
		t.Errorf("LoadAll = %v, want empty", got)
	}
}

// Removing the last entry leaves no file behind, not an empty one.
func TestRemoveLastEntryDeletesTheFile(t *testing.T) {
	dir := isolate(t)
	if err := Put("ONLY", "value"); err != nil {
		t.Fatal(err)
	}
	if err := Remove("ONLY"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, Filename)); !os.IsNotExist(err) {
		t.Errorf("store still present after removing the last entry")
	}
}

func TestRemoveKeepsOtherEntries(t *testing.T) {
	isolate(t)
	if err := Put("A", "1"); err != nil {
		t.Fatal(err)
	}
	if err := Put("B", "2"); err != nil {
		t.Fatal(err)
	}
	if err := Remove("A"); err != nil {
		t.Fatal(err)
	}
	if got := Get("B"); got != "2" {
		t.Errorf("B = %q after removing A, want 2", got)
	}
	if got := Get("A"); got != "" {
		t.Errorf("A = %q after removal, want empty", got)
	}
}

// The store holds a key; it must not be world-readable.
func TestStoreIsWrittenPrivate(t *testing.T) {
	dir := isolate(t)
	if err := Put("GEMINI_API_KEY", "value"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, Filename))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode %o, want 600", perm)
	}
}
