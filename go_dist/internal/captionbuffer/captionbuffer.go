// Package captionbuffer assembles transcription fragments into display lines.
//
// output_transcription text has no partial/final marker and no ordering
// guarantee, so turning fragments into lines is entirely the client's job.
//
// This package has no UI or network dependency and is covered by unit tests.
// It is a direct port of the Python src/livecap/caption_buffer.py.
package captionbuffer

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Policy decides when pending text is promoted to a committed line.
type Policy string

const (
	// PolicyAuto decides from the first few fragments. Only useful before measuring.
	PolicyAuto Policy = "auto"
	// PolicyIncremental is what the API actually does: fragments per word or phrase.
	PolicyIncremental Policy = "incremental"
	// PolicyUtterance is one whole utterance at a time.
	PolicyUtterance Policy = "utterance"
)

const (
	sentenceEnders    = ".?!"
	IdleCommitSeconds = 1.2
	ClearAfterSeconds = 3.0

	autoSampleSize     = 8
	autoUtteranceChars = 25
)

// Line is one display line and whether it has settled.
type Line struct {
	Text    string
	Settled bool
}

// Buffer holds arriving fragments as committed lines plus pending text.
//
// Time is injected via the now argument rather than read from a clock, which
// keeps the tests deterministic. Not safe for concurrent use; call it from one
// goroutine (in the app, the UI goroutine).
type Buffer struct {
	maxLines          int
	policy            Policy
	IdleCommitSeconds float64

	// ClearAfterSeconds clears captions this long after the last fragment.
	// Zero disables clearing.
	ClearAfterSeconds float64

	committed     []string
	pending       string
	lastText      string
	hasLastText   bool
	lastFeedAt    float64
	continueNext  bool
	resolved      Policy
	samples       []int
	newCommits    []string
	resumedPrefix string
}

// New returns a buffer with the measured defaults.
func New(maxLines int, policy Policy, clearAfter float64) *Buffer {
	if maxLines < 1 {
		maxLines = 1
	}
	b := &Buffer{
		maxLines:          maxLines,
		policy:            policy,
		IdleCommitSeconds: IdleCommitSeconds,
		ClearAfterSeconds: clearAfter,
	}
	if policy != PolicyAuto {
		b.resolved = policy
	}
	return b
}

// MaxLines reports the display line cap.
func (b *Buffer) MaxLines() int { return b.maxLines }

// SetMaxLines changes the cap, keeping the most recent lines.
func (b *Buffer) SetMaxLines(n int) {
	if n < 1 {
		n = 1
	}
	if n == b.maxLines {
		return
	}
	b.maxLines = n
	if len(b.committed) > n {
		b.committed = append([]string(nil), b.committed[len(b.committed)-n:]...)
	}
}

// CommittedLines returns the settled lines, newest last.
func (b *Buffer) CommittedLines() []string {
	return append([]string(nil), b.committed...)
}

// PendingText returns text that has not settled. Drawn dimmed.
func (b *Buffer) PendingText() string { return b.pending }

// EffectivePolicy treats AUTO as incremental until enough fragments are seen.
func (b *Buffer) EffectivePolicy() Policy {
	if b.resolved == "" {
		return PolicyIncremental
	}
	return b.resolved
}

// IsEmpty reports whether there is nothing to show.
func (b *Buffer) IsEmpty() bool { return len(b.committed) == 0 && b.pending == "" }

// DisplayLines returns at most MaxLines lines, dropping the oldest.
func (b *Buffer) DisplayLines() []Line {
	lines := make([]Line, 0, len(b.committed)+1)
	for _, t := range b.committed {
		lines = append(lines, Line{Text: t, Settled: true})
	}
	if b.pending != "" {
		lines = append(lines, Line{Text: b.pending, Settled: false})
	}
	if len(lines) > b.maxLines {
		lines = lines[len(lines)-b.maxLines:]
	}
	return lines
}

// Feed takes one transcription fragment. It reports whether the display should change.
func (b *Buffer) Feed(text string, now float64) bool {
	if text == "" {
		return false
	}
	// Ordering is not guaranteed, so ignore an immediately repeated fragment.
	if b.hasLastText && text == b.lastText {
		return false
	}
	b.lastText, b.hasLastText = text, true
	b.lastFeedAt = now

	if b.resolved == "" {
		b.samples = append(b.samples, utf8.RuneCountInString(strings.TrimSpace(text)))
		if len(b.samples) >= autoSampleSize {
			ordered := append([]int(nil), b.samples...)
			sort.Ints(ordered)
			if ordered[len(ordered)/2] >= autoUtteranceChars {
				b.resolved = PolicyUtterance
			} else {
				b.resolved = PolicyIncremental
			}
		}
	}

	// The first fragment after a reconnect may continue the previous sentence,
	// so pull the last committed line back into pending rather than starting new.
	if b.continueNext {
		b.continueNext = false
		if b.pending == "" && len(b.committed) > 0 {
			resumed := b.committed[len(b.committed)-1]
			b.committed = b.committed[:len(b.committed)-1]
			b.pending = resumed
			if n := len(b.newCommits); n > 0 && b.newCommits[n-1] == resumed {
				// Not handed to the transcript yet — emit the merged line whole.
				b.newCommits = b.newCommits[:n-1]
			} else {
				// Already written out — emit only the continuation, not a duplicate.
				b.resumedPrefix = resumed
			}
		}
	}

	b.pending = join(b.pending, text)

	if b.EffectivePolicy() == PolicyUtterance || endsSentence(b.pending) {
		b.commit()
	}
	return true
}

// Tick settles idle text, then clears after longer silence.
func (b *Buffer) Tick(now float64) bool {
	changed := false
	idle := now - b.lastFeedAt

	// Settle a phrase that stopped without sentence-final punctuation.
	if b.EffectivePolicy() == PolicyIncremental && b.pending != "" && idle >= b.IdleCommitSeconds {
		b.commit()
		changed = true
	}

	// Clear once speech has stopped. Applies to both policies.
	if b.ClearAfterSeconds > 0 && !b.IsEmpty() && idle >= b.ClearAfterSeconds {
		// Settle first, or pending text vanishes from the transcript when
		// ClearAfterSeconds is set shorter than IdleCommitSeconds.
		b.commit()
		b.Clear()
		changed = true
	}
	return changed
}

// DrainNewCommits takes the lines settled since the last call, for the transcript.
//
// Independent of what is on screen: each line comes out exactly once, and
// clearing the display does not discard lines that were never drained.
func (b *Buffer) DrainNewCommits() []string {
	out := b.newCommits
	b.newCommits = nil
	if out == nil {
		return []string{}
	}
	return out
}

// Flush force-settles pending text, so stopping keeps the last sentence.
func (b *Buffer) Flush() bool {
	if b.pending == "" {
		return false
	}
	b.commit()
	return true
}

// NoteReconnect tells the buffer the next fragment continues the previous line.
func (b *Buffer) NoteReconnect() {
	b.continueNext = true
	b.hasLastText = false
	b.lastText = ""
}

// Clear empties the display.
func (b *Buffer) Clear() {
	b.committed = nil
	b.pending = ""
	b.hasLastText = false
	b.lastText = ""
	b.continueNext = false
	b.resumedPrefix = ""
}

func (b *Buffer) commit() {
	line := strings.TrimSpace(b.pending)
	b.pending = ""
	prefix := b.resumedPrefix
	b.resumedPrefix = ""
	if line == "" {
		return
	}
	b.committed = append(b.committed, line)
	if len(b.committed) > b.maxLines {
		b.committed = append([]string(nil), b.committed[len(b.committed)-b.maxLines:]...)
	}
	if prefix != "" && strings.HasPrefix(line, prefix) {
		if rest := strings.TrimSpace(line[len(prefix):]); rest != "" {
			b.newCommits = append(b.newCommits, rest)
		}
		return
	}
	b.newCommits = append(b.newCommits, line)
}

func endsSentence(s string) bool {
	s = strings.TrimRight(s, " \t\n")
	if s == "" {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(s)
	return strings.ContainsRune(sentenceEnders, last)
}

// isCJK reports Hangul, kana or han. Never insert a space between these.
func isCJK(r rune) bool {
	switch {
	case r >= 0xAC00 && r <= 0xD7A3: // Hangul syllables
		return true
	case r >= 0x1100 && r <= 0x11FF: // Hangul jamo
		return true
	case r >= 0x3040 && r <= 0x30FF: // hiragana and katakana
		return true
	case r >= 0x4E00 && r <= 0x9FFF: // han
		return true
	}
	return false
}

const (
	noSpaceBefore = ",.?!;:%)]}'\""
	noSpaceAfter  = "([{-'"
)

// join concatenates two fragments, inserting a space only when neither supplies one.
//
// Measured behaviour (see spikes/RESULTS.md): fragments arrive with their own
// leading space in both English and Korean, so they normally concatenate
// directly. In the rare case a space is missing, CJK text is joined without one
// — a fragment that split mid-word would otherwise be broken apart. English
// needs the space, or two words run together.
func join(left, right string) string {
	if left == "" {
		return strings.TrimLeftFunc(right, unicode.IsSpace)
	}
	lastLeft, _ := utf8.DecodeLastRuneInString(left)
	firstRight, _ := utf8.DecodeRuneInString(right)
	if lastLeft == ' ' || lastLeft == '\n' || firstRight == ' ' || firstRight == '\n' {
		return left + right
	}
	if strings.ContainsRune(noSpaceBefore, firstRight) || strings.ContainsRune(noSpaceAfter, lastLeft) {
		return left + right
	}
	if isCJK(lastLeft) && isCJK(firstRight) {
		return left + right
	}
	return left + " " + right
}
