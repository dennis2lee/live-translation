// Package captions assembles transcription fragments into display lines.
//
// `OutputTranscription.Text` has no partial/final marker and no ordering
// guarantee, so turning fragments into lines is entirely the client's job.
//
// This package must stay free of UI and network dependencies, and covered by
// unit tests. It is a straight port of caption_buffer.py, invariants included.
//
// A Buffer is owned by one goroutine — the UI dispatcher — and holds no lock,
// exactly as the Python original is owned by the Qt main thread.
package captions

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// SentenceEnders are the characters that end a sentence.
const SentenceEnders = ".?!"

const (
	// IdleCommitSeconds settles a phrase that stopped without punctuation.
	IdleCommitSeconds = 1.2
	// ClearAfterSeconds is the default silence before the display is wiped.
	ClearAfterSeconds = 2.0

	// LineHoldSeconds is how long a settled line stays on screen before the
	// next one may push it off. The model translates a whole utterance at a
	// time, so several sentences can settle inside a single 100 ms display
	// tick; the toolkit coalesces the repaints and the reader only ever sees
	// the last pair. Releasing the backlog one line at a time is what stops a
	// burst flashing past unread.
	LineHoldSeconds = 0.7

	// MaxClearWaitSeconds caps how long captions may sit on screen after the
	// last fragment, whatever the input is doing. Without it a noisy room
	// would keep resetting the clear countdown for ever.
	MaxClearWaitSeconds = 15.0

	// MinSentenceChars is retained for the transcript's own use and for the
	// Python reference's behaviour; it no longer gates a full stop.
	//
	// It used to: in complete-phrases mode a full stop only ended a sentence
	// with 20 runes behind it, so "Yes, I agree." could not settle on its full
	// stop and waited out the 1.2 s idle timer instead. That is the reported
	// "sometimes the full stop shows, sometimes it does not" — the difference
	// was the length of the sentence, which is not something a reader can see.
	// endsASentence now looks at the word before the stop instead.
	MinSentenceChars = 20

	// DuplicateWindowSeconds is how soon an identical fragment counts as the
	// API redelivering the same one rather than the speaker genuinely
	// repeating a word. Fragments arrive about a second apart
	// (spikes/RESULTS.md), so this sits far below any real repeat — " the"
	// twice in one sentence is ordinary English and must not be swallowed.
	DuplicateWindowSeconds = 0.25

	// CatchUpFactor bounds the backlog. Up to max_lines behind, settled lines
	// are paced at the full hold; up to CatchUpFactor*max_lines behind, at half
	// the hold, so each is still drawn; beyond that the backlog is released in
	// one pass and the oldest lines are lost from the screen — never from the
	// transcript, which DrainNewCommits keeps independent of the display.
	CatchUpFactor = 2

	autoSampleSize     = 8
	autoUtteranceChars = 25
)

// Policy decides when pending text is promoted into a committed line.
type Policy string

const (
	// PolicyAuto decides from the first few fragments. Only useful before
	// measuring.
	PolicyAuto Policy = "auto"
	// PolicyIncremental is what the API actually does: fragments arrive per
	// word or phrase.
	PolicyIncremental Policy = "incremental"
	// PolicyUtterance is one whole utterance per fragment.
	PolicyUtterance Policy = "utterance"
)

// Speech reports what the input level said since the last tick. Three states,
// not two: somebody is speaking, the input is quiet, or there is no level to go
// on at all.
type Speech int

const (
	// SpeechUnknown means no level was available, so silence is judged from
	// fragment arrival alone, as it was before metering existed.
	SpeechUnknown Speech = iota
	// SpeechQuiet means the input carried no signal.
	SpeechQuiet
	// SpeechActive means somebody is talking.
	SpeechActive
)

// Line is one row of the display.
type Line struct {
	Text string
	// Settled is false for text still being revised, which is drawn dimmed.
	Settled bool
}

// Options configures a Buffer. The zero value is not useful — use New.
type Options struct {
	MaxLines          int
	Policy            Policy
	IdleCommitSeconds float64

	// ClearAfterSeconds clears this long after the backlog has drained and the
	// input went quiet. 0 disables clearing.
	ClearAfterSeconds float64

	// LineHoldSeconds is the minimum time a line stays on screen before the
	// next may replace it.
	LineHoldSeconds float64

	// CompletePhrasesOnly shows only settled text, never a phrase still being
	// revised. The model streams its translation a fragment at a time and
	// rewrites as it goes, so drawing every fragment makes the captions
	// visibly churn. With this on, unsettled text is withheld until it settles
	// — at a sentence end, at the server's turn_complete, or after
	// IdleCommitSeconds of quiet. Costs a little latency, and in exchange the
	// captions stop moving under the reader.
	CompletePhrasesOnly bool
}

// Buffer holds arriving fragments as committed lines plus pending text.
//
// A settled line does not go straight on screen. It joins a queue that is
// released one line at a time, so a burst of sentences is read out at a
// readable pace rather than scrolling past inside one repaint. Pacing never
// accumulates: the queue stays within CatchUpFactor*MaxLines once the display
// has caught up, so it trails the audio by at most MaxLines*LineHoldSeconds.
// (Shrinking MaxLines leaves the queue briefly over that bound; the next Tick
// drains it.) A burst longer than that loses its oldest line from the screen —
// the transcript still gets every one of them, since DrainNewCommits is
// independent of what is displayed.
//
// Time is injected via `now` rather than read from a clock, which keeps the
// tests deterministic.
type Buffer struct {
	maxLines            int
	policy              Policy
	idleCommitSeconds   float64
	clearAfterSeconds   float64
	lineHoldSeconds     float64
	completePhrasesOnly bool

	committed     []string
	queue         []string
	pending       string
	lastText      string
	lastFeedAt    float64
	releasedAt    float64
	audioKnown    bool
	quietSince    float64
	quietSinceSet bool
	continueNext  bool
	resolved      Policy
	samples       []int
	newCommits    []string
	resumedPrefix string
}

// New builds a Buffer, filling in the defaults for anything left at zero.
func New(opts Options) *Buffer {
	b := &Buffer{
		maxLines:            max(1, opts.MaxLines),
		policy:              opts.Policy,
		idleCommitSeconds:   opts.IdleCommitSeconds,
		clearAfterSeconds:   opts.ClearAfterSeconds,
		lineHoldSeconds:     opts.LineHoldSeconds,
		completePhrasesOnly: opts.CompletePhrasesOnly,
	}
	if b.policy == "" {
		b.policy = PolicyAuto
	}
	if opts.MaxLines == 0 {
		b.maxLines = 2
	}
	if b.idleCommitSeconds == 0 {
		b.idleCommitSeconds = IdleCommitSeconds
	}
	if b.lineHoldSeconds == 0 {
		b.lineHoldSeconds = LineHoldSeconds
	}
	if b.policy != PolicyAuto {
		b.resolved = b.policy
	}
	return b
}

// --- queries -------------------------------------------------------------

// MaxLines is the most lines the display will hold.
func (b *Buffer) MaxLines() int { return b.maxLines }

// CompletePhrasesOnly reports whether unsettled text is withheld.
func (b *Buffer) CompletePhrasesOnly() bool { return b.completePhrasesOnly }

// SetCompletePhrasesOnly turns the mode on or off.
func (b *Buffer) SetCompletePhrasesOnly(on bool) { b.completePhrasesOnly = on }

// CommittedLines returns the settled lines, newest last.
func (b *Buffer) CommittedLines() []string { return append([]string(nil), b.committed...) }

// PendingText is the text not yet settled. Drawn dimmed.
func (b *Buffer) PendingText() string { return b.pending }

// QueuedLines are settled lines still waiting for their turn on screen,
// oldest first.
func (b *Buffer) QueuedLines() []string { return append([]string(nil), b.queue...) }

// OutputComplete is true when everything received has been shown: nothing is
// queued and nothing is half-settled, so the display has caught up with the
// audio. The clear countdown only runs once this holds.
func (b *Buffer) OutputComplete() bool { return len(b.queue) == 0 && b.pending == "" }

// EffectivePolicy treats AUTO as INCREMENTAL until enough fragments have been
// seen.
func (b *Buffer) EffectivePolicy() Policy {
	if b.resolved == "" {
		return PolicyIncremental
	}
	return b.resolved
}

// IsEmpty is true when there is nothing to show and nothing waiting to be
// shown.
func (b *Buffer) IsEmpty() bool {
	return len(b.committed) == 0 && b.pending == "" && len(b.queue) == 0
}

// DisplayLines returns at most MaxLines rows, dropping the oldest.
//
// Unsettled text is withheld while the queue drains: appending live text under
// lines that are still catching up would show them out of order. In
// complete-phrases mode it is withheld always — that is the whole point of the
// mode, so the reader never watches a phrase rewrite itself.
func (b *Buffer) DisplayLines() []Line {
	lines := make([]Line, 0, len(b.committed)+1)
	for _, t := range b.committed {
		lines = append(lines, Line{Text: t, Settled: true})
	}
	// Complete-phrases mode withholds unsettled text so a line never rewrites
	// itself under the reader — but when there is no settled line on screen
	// there is nothing to rewrite, and an empty box tells the reader nothing
	// at all. A long sentence takes seconds to translate, and the reported
	// symptom was staring at a blank box for the whole of it. Show the text in
	// progress in that case; withhold it again the moment a settled line
	// exists to protect.
	if b.pending != "" && len(b.queue) == 0 &&
		(!b.completePhrasesOnly || len(b.committed) == 0) {
		lines = append(lines, Line{Text: b.pending, Settled: false})
	}
	if len(lines) > b.maxLines {
		lines = lines[len(lines)-b.maxLines:]
	}
	return lines
}

// --- updates -------------------------------------------------------------

// SetMaxLines changes the display height, keeping the newest lines.
func (b *Buffer) SetMaxLines(n int) {
	n = max(1, n)
	if n == b.maxLines {
		return
	}
	b.maxLines = n
	if len(b.committed) > n {
		b.committed = append([]string(nil), b.committed[len(b.committed)-n:]...)
	}
}

// SetPolicy switches the commit policy; AUTO starts detection over.
func (b *Buffer) SetPolicy(p Policy) {
	if p == b.policy {
		return
	}
	b.policy = p
	if p == PolicyAuto {
		b.resolved = ""
	} else {
		b.resolved = p
	}
	b.samples = b.samples[:0]
}

// Feed takes one transcription fragment. It reports whether the display should
// change.
func (b *Buffer) Feed(text string, now float64) bool {
	if text == "" {
		return false
	}
	// Ordering is not guaranteed, so ignore a fragment the API redelivered.
	// Only within a short window: a second later the same text is the speaker
	// repeating a word, and dropping it loses what they said.
	if text == b.lastText && now-b.lastFeedAt < DuplicateWindowSeconds {
		return false
	}
	b.lastText = text
	b.lastFeedAt = now

	if b.resolved == "" {
		b.samples = append(b.samples, utf8.RuneCountInString(strings.TrimSpace(text)))
		if len(b.samples) >= autoSampleSize {
			if median(b.samples) >= autoUtteranceChars {
				b.resolved = PolicyUtterance
			} else {
				b.resolved = PolicyIncremental
			}
		}
	}

	// The first fragment after a reconnect may continue the previous sentence,
	// so pull the last settled line back into pending rather than starting new.
	// It may still be queued rather than on screen, so check the queue first.
	if b.continueNext {
		b.continueNext = false
		if b.pending == "" {
			resumed := ""
			if n := len(b.queue); n > 0 {
				resumed = b.queue[n-1]
				b.queue = b.queue[:n-1]
			} else if n := len(b.committed); n > 0 {
				resumed = b.committed[n-1]
				b.committed = b.committed[:n-1]
			}
			if resumed != "" {
				b.pending = resumed
				if n := len(b.newCommits); n > 0 && b.newCommits[n-1] == resumed {
					// Not handed to the transcript yet — emit the merged line whole.
					b.newCommits = b.newCommits[:n-1]
				} else {
					// Already written out — emit only the continuation.
					b.resumedPrefix = resumed
				}
			}
		}
	}

	b.pending = join(b.pending, text)

	if b.EffectivePolicy() == PolicyUtterance || b.endsASentence() {
		b.commit()
		// Show it straight away when there is room, so a single sentence never
		// waits for the next tick.
		b.drain(now)
	}
	return true
}

// endsASentence reports whether the pending text has reached a sentence end.
//
// In complete-phrases mode a full stop also has to have enough text behind it:
// "Dr." and "vs." would otherwise settle a two-word fragment as a sentence, and
// there is no dimmed pending line left on screen to show the reader that more
// is coming.
func (b *Buffer) endsASentence() bool {
	settled := strings.TrimRightFunc(b.pending, unicode.IsSpace)
	if settled == "" {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(settled)
	if !strings.ContainsRune(SentenceEnders, last) {
		return false
	}
	// "?" and "!" never end an abbreviation, so they always settle.
	if last != '.' {
		return true
	}
	return !endsWithAbbreviation(settled)
}

// abbreviations are the words whose trailing full stop does not end a sentence.
// Kept deliberately short: every entry costs a real sentence that now waits for
// the idle timer, and the only ones that matter are those the translation
// actually produces.
var abbreviations = map[string]bool{
	"dr": true, "mr": true, "mrs": true, "ms": true, "prof": true, "rev": true,
	"fr": true, "st": true, "jr": true, "sr": true, "vs": true, "etc": true,
	"no": true, "vol": true, "approx": true, "inc": true, "ltd": true,
}

// endsWithAbbreviation reports whether the full stop at the end belongs to an
// abbreviation rather than to a sentence.
//
// This replaced a blanket minimum length. The length rule made a short sentence
// indistinguishable from an abbreviation, so "Yes, I agree." was held back for
// the full idle timer while a long sentence appeared at once — from the
// reader's side, a full stop that sometimes works.
func endsWithAbbreviation(settled string) bool {
	trimmed := strings.TrimSuffix(settled, ".")
	if trimmed == "" {
		return true // a lone "."
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return true
	}
	word := fields[len(fields)-1]
	// Strip anything that is not part of the word itself, so ("Dr. and (Dr.
	// are recognised too.
	word = strings.TrimFunc(word, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if word == "" {
		return false
	}
	// A single letter is an initial — "J. Smith", not the end of a sentence.
	if utf8.RuneCountInString(word) == 1 {
		r, _ := utf8.DecodeRuneInString(word)
		return unicode.IsLetter(r)
	}
	return abbreviations[strings.ToLower(word)]
}

// NoteTurnComplete settles whatever is buffered — the server says the turn
// ended.
//
// server_content.turn_complete is the only boundary the API reports reliably;
// Transcription.Finished exists in the SDK but does not arrive in practice.
// Outside complete-phrases mode this changes nothing visible, since unsettled
// text is already on screen.
func (b *Buffer) NoteTurnComplete(now float64) bool {
	if b.pending == "" {
		return false
	}
	b.commit()
	b.drain(now)
	return true
}

// Tick is called periodically. It settles idle text, releases the backlog, then
// clears. `speech` reports whether the input carried signal since the last call.
func (b *Buffer) Tick(now float64, speech Speech) bool {
	changed := false
	idle := now - b.lastFeedAt

	b.audioKnown = speech != SpeechUnknown
	switch speech {
	case SpeechActive:
		b.quietSinceSet = false
	case SpeechQuiet:
		if !b.quietSinceSet {
			b.quietSince = now
			b.quietSinceSet = true
		}
	}

	// Settle a phrase that stopped without sentence-final punctuation.
	if b.EffectivePolicy() == PolicyIncremental && b.pending != "" && idle >= b.idleCommitSeconds {
		b.commit()
		changed = true
	}

	// A clear window shorter than the commit window would otherwise strand
	// pending text: the display would never report itself complete, so it would
	// never clear and the text would never reach the transcript.
	if b.clearAfterSeconds != 0 && b.pending != "" && idle >= b.clearAfterSeconds {
		b.commit()
		changed = true
	}

	if b.drain(now) {
		changed = true
	}

	// Clear once the backlog has drained and the room has gone quiet.
	if b.clearDue(now) {
		b.Clear()
		changed = true
	}

	return changed
}

// DrainNewCommits takes the lines settled since the last call, for the
// transcript.
//
// Independent of what is on screen: each line comes out exactly once, and
// clearing the display does not discard lines that were never drained.
func (b *Buffer) DrainNewCommits() []string {
	out := b.newCommits
	b.newCommits = nil
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

// NoteReconnect tells the buffer a reconnect happened, so the next fragment
// continues the line.
func (b *Buffer) NoteReconnect() {
	b.continueNext = true
	b.lastText = ""
}

// Clear wipes the display. What was already said still reaches the transcript.
//
// Half-finished text is settled first rather than dropped: recording is
// deliberately independent of what is on screen, and a sentence the speaker
// finished saying should not vanish because the captions were cleared
// underneath it. commit files it under newCommits, and emptying the queue below
// keeps it off the screen.
func (b *Buffer) Clear() {
	b.commit()
	b.committed = b.committed[:0]
	b.queue = b.queue[:0]
	b.pending = ""
	b.lastText = ""
	b.continueNext = false
	b.resumedPrefix = ""
}

// --- internals -----------------------------------------------------------

// drain releases as many queued lines as their hold times allow.
func (b *Buffer) drain(now float64) bool {
	changed := false
	for b.releaseDue(now) {
		b.committed = append(b.committed, b.queue[0])
		b.queue = b.queue[1:]
		if len(b.committed) > b.maxLines {
			b.committed = b.committed[len(b.committed)-b.maxLines:]
		}
		b.releasedAt = now
		changed = true
	}
	return changed
}

// releaseDue reports whether the next queued line may go on screen yet.
func (b *Buffer) releaseDue(now float64) bool {
	if len(b.queue) == 0 {
		return false
	}
	if len(b.committed) < b.maxLines {
		return true // room to spare — nothing readable is pushed off
	}
	// Falling behind shortens the hold before it abolishes it.
	//
	// This used to release the whole backlog the moment it passed max_lines,
	// which put every intermediate line into `committed` and straight back out
	// inside one repaint: a burst of five sentences showed three and silently
	// skipped two. They still reached the transcript, which is what made it
	// hard to notice.
	//
	// Pacing a backlog without limit is not the answer either — the display
	// would fall arbitrarily far behind the speaker. So there are two tiers,
	// and the queue stays bounded by CatchUpFactor*max_lines after any tick,
	// which is the same latency ceiling as before, reached differently.
	hold := b.lineHoldSeconds
	switch {
	case len(b.queue) > b.maxLines*CatchUpFactor:
		return true // beyond recovery by pacing; take the loss and catch up
	case len(b.queue) > b.maxLines:
		hold /= 2 // behind, but every line still gets drawn
	}
	return now-b.releasedAt >= hold
}

func (b *Buffer) clearDue(now float64) bool {
	if b.clearAfterSeconds == 0 || b.IsEmpty() {
		return false
	}
	// The ceiling comes first: captions must not sit on screen for ever
	// because the room is noisy or the backlog stopped draining.
	if now-b.lastFeedAt >= MaxClearWaitSeconds {
		return true
	}
	if !b.OutputComplete() {
		return false // still pouring out — the countdown has not started
	}
	if b.audioKnown && !b.quietSinceSet {
		// Somebody is talking. The model can go quiet mid-sentence, and wiping
		// the captions underneath them is the reported complaint.
		return false
	}
	// The countdown runs from the later of the last fragment, the last line
	// reaching the screen, and the moment the input went quiet.
	quietSince := max(b.lastFeedAt, b.releasedAt)
	if b.quietSinceSet {
		quietSince = max(quietSince, b.quietSince)
	}
	return now-quietSince >= b.clearAfterSeconds
}

func (b *Buffer) commit() {
	line := strings.TrimSpace(b.pending)
	b.pending = ""
	prefix := b.resumedPrefix
	b.resumedPrefix = ""
	if line == "" {
		return
	}
	b.queue = append(b.queue, line)
	if prefix != "" && strings.HasPrefix(line, prefix) {
		rest := strings.TrimSpace(line[len(prefix):])
		if rest != "" {
			b.newCommits = append(b.newCommits, rest)
		}
	} else {
		b.newCommits = append(b.newCommits, line)
	}
}

// isCJK reports Hangul, kana or han. Never insert a space between these.
func isCJK(r rune) bool {
	return (r >= 0xAC00 && r <= 0xD7A3) || // Hangul syllables
		(r >= 0x1100 && r <= 0x11FF) || // Hangul jamo
		(r >= 0x3040 && r <= 0x30FF) || // hiragana and katakana
		(r >= 0x4E00 && r <= 0x9FFF) // han
}

// join concatenates two fragments, inserting a space only when neither
// supplies one.
//
// Measured behaviour (see spikes/RESULTS.md): fragments arrive with their own
// leading space in both English and Korean, so they normally concatenate
// directly. In the rare case a space is missing, CJK text is joined *without*
// one — a fragment that split mid-word (계획 + 입니다.) would otherwise be
// broken apart. English needs the space, or two words run together.
func join(left, right string) string {
	if left == "" {
		return strings.TrimLeftFunc(right, unicode.IsSpace)
	}
	if strings.HasSuffix(left, " ") || strings.HasSuffix(left, "\n") ||
		strings.HasPrefix(right, " ") || strings.HasPrefix(right, "\n") {
		return left + right
	}
	first, _ := utf8.DecodeRuneInString(right)
	last, _ := utf8.DecodeLastRuneInString(left)
	if strings.ContainsRune(",.?!;:%)]}'\"", first) || strings.ContainsRune("([{-'", last) {
		return left + right
	}
	if isCJK(last) && isCJK(first) {
		return left + right
	}
	return left + " " + right
}

func median(xs []int) int {
	ordered := append([]int(nil), xs...)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j] < ordered[j-1]; j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	return ordered[len(ordered)/2]
}

// SetClearAfterSeconds changes the silence window. 0 disables clearing.
func (b *Buffer) SetClearAfterSeconds(v float64) { b.clearAfterSeconds = v }

// SetLineHoldSeconds changes how long a settled line holds the screen.
func (b *Buffer) SetLineHoldSeconds(v float64) { b.lineHoldSeconds = v }
