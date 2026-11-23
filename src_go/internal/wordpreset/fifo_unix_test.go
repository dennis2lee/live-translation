//go:build unix

package wordpreset

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO named like a preset: opening it blocks until something writes to
// it, so only the regular-file check keeps Load off it. (A link to /dev/zero
// is also stopped by the size limit, so it cannot prove the check is there.)
func TestLoadRefusesAFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.txt"), 0o644); err != nil {
		t.Skip("no FIFOs here:", err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := Load(dir, "pipe"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a FIFO was loaded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Load blocked on a FIFO")
	}
}
