package platform

import (
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// The caption font stack was removed rather than tested. It named font
// families, which is a Qt API shape with no Fyne counterpart — nothing could
// consume the list, and nothing called it. See the note in fonts.go.

func TestRevealCommandPerPlatform(t *testing.T) {
	cases := map[string][]string{
		"darwin":  {"open", "/tmp/x"},
		"windows": {"explorer", "/tmp/x"},
		"linux":   {"xdg-open", "/tmp/x"},
	}
	for goos, want := range cases {
		if got := revealCommandFor(goos, "/tmp/x"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s command = %q, want %q", goos, got, want)
		}
	}
}

// Acquiring is best-effort and idempotent, and releasing what was never taken
// must be harmless — the shutdown path calls Release unconditionally.
func TestDisplaySleepBlockerIsSafeToMisuse(t *testing.T) {
	var b DisplaySleepBlocker
	b.Release() // nothing held
	if b.Held() {
		t.Error("Held is true before anything was acquired")
	}
	first := b.Acquire("LiveCap test")
	if b.Acquire("LiveCap test") != first {
		t.Error("a second Acquire disagreed with the first")
	}
	if runtime.GOOS == "darwin" && !first {
		t.Skip("assertion refused on this machine; nothing further to check")
	}
	if runtime.GOOS != "darwin" && first {
		t.Error("a non-macOS build claimed to have blocked display sleep")
	}
	if first && !b.Held() {
		t.Error("Acquire returned true but Held is false")
	}
	b.Release()
	if b.Held() {
		t.Error("still held after Release")
	}
	b.Release() // twice is fine
}

// The verification the Python build uses: the assertion shows up in pmset.
func TestAssertionIsVisibleToPmset(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	var b DisplaySleepBlocker
	if !b.Acquire("LiveCap pmset probe") {
		t.Skip("assertion refused on this machine")
	}
	defer b.Release()

	out, err := exec.Command("/usr/bin/pmset", "-g", "assertions").Output()
	if err != nil {
		t.Skipf("pmset unavailable: %v", err)
	}
	if !strings.Contains(string(out), "LiveCap pmset probe") {
		t.Error("the assertion is held but pmset does not list it")
	}
}
