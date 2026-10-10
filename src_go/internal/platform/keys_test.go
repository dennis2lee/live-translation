package platform

import "testing"

// The scan codes of the M and F keys, pinned per platform. A wrong one makes
// the key do nothing under a Korean input source, where the scan code is the
// only reliable way to recognise it.
func TestScanCodesForMAndF(t *testing.T) {
	for _, c := range []struct {
		goos   string
		letter rune
		want   int
	}{
		{"darwin", 'M', 46}, {"darwin", 'F', 3},
		{"windows", 'M', 0x32}, {"windows", 'F', 0x21},
		{"linux", 'M', 58}, {"linux", 'F', 41},
		{"darwin", 'Q', -1}, {"plan9", 'M', -1},
	} {
		if got := scanCodeFor(c.goos, c.letter); got != c.want {
			t.Errorf("%s %c: %d, want %d", c.goos, c.letter, got, c.want)
		}
	}
}
