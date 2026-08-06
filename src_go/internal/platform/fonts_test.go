package platform

import (
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// The Windows and Linux branches are pinned from a Mac, the same job
// tests/test_platform_support.py does for the Python build.
func TestCaptionFontStacksPerPlatform(t *testing.T) {
	cases := map[string][]string{
		"darwin":  {"SF Pro Display", "Apple SD Gothic Neo", "Helvetica Neue", "Helvetica"},
		"windows": {"Segoe UI", "Malgun Gothic", "Arial"},
		"linux":   {"Noto Sans", "Noto Sans CJK KR", "DejaVu Sans"},
	}
	for goos, want := range cases {
		if got := captionFontFamiliesFor(goos); !reflect.DeepEqual(got, want) {
			t.Errorf("%s stack = %q, want %q", goos, got, want)
		}
	}
}

// Every stack must name a Hangul face, or Korean falls back per glyph.
func TestEveryFontStackHasAKoreanFace(t *testing.T) {
	korean := map[string]string{
		"darwin":  "Apple SD Gothic Neo",
		"windows": "Malgun Gothic",
		"linux":   "Noto Sans CJK KR",
	}
	for goos, face := range korean {
		stack := captionFontFamiliesFor(goos)
		found := false
		for _, f := range stack {
			if f == face {
				found = true
			}
		}
		if !found {
			t.Errorf("%s stack %q has no Korean face", goos, stack)
		}
	}
}

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
