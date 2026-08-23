// Package classify answers one question about a stretch of audio: is this
// speech, or is it music?
//
// The classifier is YAMNet (AudioSet, 521 classes, Apache 2.0), run entirely
// locally by a pure-Go port of its pinned TFLite export — no shared library,
// no cgo, no network at inference time, for the same reason the Windows build
// ships a pure-Go resampler instead of libsoxr. The port is verified against
// golden outputs from the reference interpreter; see model_test.go for the
// measured agreement.
//
// Like caption_buffer and netcheck, this package must stay free of Fyne and
// genai dependencies, and covered by unit tests.
package classify

import (
	_ "embed"
	"strings"
)

// classmap.csv is the AudioSet class list (yamnet_class_map.csv, Apache 2.0),
// embedded because 14 KB of text is not worth a second download.
//
//go:embed classmap.csv
var classMapCSV string

// classNames is the display name per class index.
var classNames = func() []string {
	names := make([]string, 0, NumClasses)
	for i, line := range strings.Split(strings.TrimSpace(classMapCSV), "\n") {
		if i == 0 {
			continue // header
		}
		// index,mid,display_name — CRLF endings, and the name may be quoted
		// because it holds commas.
		parts := strings.SplitN(strings.TrimRight(line, "\r"), ",", 3)
		if len(parts) != 3 {
			continue
		}
		names = append(names, strings.Trim(parts[2], `"`))
	}
	return names
}()

// ClassName returns the display name for a class index.
func ClassName(i int) string {
	if i < 0 || i >= len(classNames) {
		return ""
	}
	return classNames[i]
}

// The two families the gate compares. Ranges, not hand-picked lists, because
// the AudioSet ontology is arranged in blocks and blocks are checkable against
// the embedded CSV — TestFamiliesMatchTheClassMap does exactly that.
//
//   - Speech: classes 0–3 (Speech, Child speech, Conversation, Narration)
//     plus 5 (Speech synthesizer — a TTS voice in the room is still speech
//     for translation purposes). 4 is Babbling, which is not.
//   - Music: 24–31, the singing block (Singing … Rapping), and 132–276, the
//     ontology's music block (Music … Scary music). Humming (32) and
//     Whistling (35) are deliberately outside: gating a speaker who hums a
//     bar mid-sentence is the failure mode, not the feature.
var (
	speechClasses = []int{0, 1, 2, 3, 5}
	musicRanges   = [][2]int{{24, 31}, {132, 276}}
)

// SpeechScore is the strongest speech-family score in one frame.
func SpeechScore(scores []float32) float32 {
	var best float32
	for _, i := range speechClasses {
		if scores[i] > best {
			best = scores[i]
		}
	}
	return best
}

// MusicScore is the strongest music-family score in one frame.
func MusicScore(scores []float32) float32 {
	var best float32
	for _, r := range musicRanges {
		for i := r[0]; i <= r[1]; i++ {
			if scores[i] > best {
				best = scores[i]
			}
		}
	}
	return best
}

// Top returns the n highest-scoring class indices, best first — the settings
// window's live test shows these so tuning is done against evidence.
func Top(scores []float32, n int) []int {
	idx := make([]int, 0, n)
	for i := range scores {
		pos := len(idx)
		for pos > 0 && scores[i] > scores[idx[pos-1]] {
			pos--
		}
		if pos < n {
			if len(idx) < n {
				idx = append(idx, 0)
			}
			copy(idx[pos+1:], idx[pos:])
			idx[pos] = i
		}
	}
	return idx
}
