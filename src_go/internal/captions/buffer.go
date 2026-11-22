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
	//
	// Measured, 2026-08-08: fragments do not arrive at the pace of speech, they
	// arrive on a **one-second server clock**. 73 of 93 gaps in a debug session
	// fell between 900 and 1200 ms, median 1000. The tail is not the speaker
	// pausing — it is a beat arriving late followed by one landing back on the
	// clock (1252 then 743, 1239 then 753), each pair summing to about two
	// seconds.
	//
	// At 1.2 s the timer sat 240 ms above the normal interval, which is inside
	// that jitter: 11 of 93 gaps crossed it, and every one of those split a
	// sentence the speaker had not finished. That is the source of the short
	// settled lines counted in the 67-minute transcript — 56 of them six runes
	// or fewer ("Love", "My", "arise").
	//
	// 1.6 s clears a late beat with room to spare and still fires well inside
	// the two-second missed beat. It cannot usefully go higher at the default
	// settings: Tick force-commits pending text at ClearAfterSeconds (2.0)
	// as well, so anything above that is unreachable.
	IdleCommitSeconds = 1.6
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

	// MinSentenceChars is what a full stop used to need behind it before it
	// ended a sentence, in complete-phrases mode. Nothing reads it now — it is
	// kept as the record of a rule that was removed, because the number is
	// referenced from spikes/RESULTS.md and from the Python reference, which
	// still applies it.
	//
	// It made "Yes, I agree." wait out the 1.2 s idle timer while a long
	// sentence appeared at once: a full stop that worked only sometimes, and
	// the difference was a length the reader cannot see. The length was a proxy
	// for "is this an abbreviation", which endsASentence now asks directly.
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

	// DisplayHistoryFactor is how many settled sentences are kept per display
	// row, so the box can fill all of its rows.
	//
	// The Python build painted one sentence per row, so keeping max_lines
	// sentences was exactly one boxful. The Go renderer reflows settled
	// sentences into one paragraph — rows pack full — which means several
	// sentences share a row, and max_lines sentences often filled a single
	// row while the rows above it sat blank. Reported from live use with three
	// lines configured: the bottom row carried the only text.
	//
	// It was eight per row, from the 67-minute transcript: 56 settled lines
	// of six runes or fewer ("Love", "My", "arise") against a row of about 50
	// runes. That is the default window. In a wide one a row takes twice as
	// many, the history no longer filled the box, and — the box being drawn
	// as one paragraph — each sentence dropped from a history that is all on
	// screen pulled the top row to the left under the reader (the red team:
	// 157 of 200 short sentences at 1920 px and four rows). Thirty-two a row
	// covers that with room over; it is not a proof — the smallest font on a
	// 3840 px window still outruns it with nothing but two-word sentences.
	// The renderer trims to max_lines *rows* after wrapping, so history
	// costs nothing on screen (a cold wrap of 128 sentences is 2–5 ms, a
	// repaint after it tens of microseconds); only too little shows.
	DisplayHistoryFactor = 32

	// WordHoldSeconds is how long the last word of text in progress is kept
	// off the screen when fragments are pieces of words (WordPieces), waiting
	// for the piece that finishes it. Measured on a recorded OpenAI session:
	// a piece that continued a word arrived a median 0.37 s after the one
	// before it, 0.76 s at most. A word is normally released sooner than
	// this, by the next word's first piece arriving.
	WordHoldSeconds = 1.0

	// DraftFallbackSeconds is how long HideDraftWhenEmpty keeps an empty box
	// waiting for a sentence to finish before showing it in progress anyway.
	//
	// A sentence settles at .?!, at the server's turn_complete, or after
	// IdleCommitSeconds without a fragment. Speech that keeps coming with
	// none of those — no punctuation, a CJK full stop, an ellipsis, a closing
	// quote after the stop — never settles while it lasts, and without this
	// the box stayed blank for as long as the speaker talked (the red team,
	// 600 of 600 ticks over a minute). A blank box there looks like a dead
	// app, which is worse than a sentence drawn twice.
	//
	// 10 s is past nine in ten sentences: across 11,357 transcript lines the
	// gap between consecutive settled lines was 10 s or more 10.3% of the
	// time, and that gap includes pauses, so fewer sentences than that ever
	// reach it.
	DraftFallbackSeconds = 10.0

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

	// CompletePhrasesOnly shows only settled text, never a sentence still
	// arriving. The translation comes a fragment at a time — a word or three
	// a second — and with this on a sentence is withheld until it settles: at
	// a sentence end, at the server's turn_complete, or after
	// IdleCommitSeconds of quiet. In exchange for captions that arrive at
	// once, each one lands when its sentence ends: a median 2.3 s after the
	// translated voice began it, three to four for a five-second sentence,
	// longer for a longer one (s3_sync).
	CompletePhrasesOnly bool

	// HideDraftWhenEmpty, in complete-phrases mode, withholds unsettled text
	// even when no settled line is on screen. Left off, an empty box shows
	// the sentence in progress, dimmed, and redraws it settled when it ends —
	// which reads as the line being written twice. On, the box stays empty
	// until the sentence settles. No effect outside complete-phrases mode,
	// where text in progress is always shown.
	HideDraftWhenEmpty bool

	// DraftInPlace says the display draws text in progress where it will
	// stay — in the paragraph, in the settled colour — rather than dimmed on
	// a row of its own. A sentence that settles is then one the reader has
	// been reading all along, so it is never held back to wait its turn:
	// that would take it off the screen and put it back.
	//
	// This is what keeps the captions in step with the translated voice. The
	// fragments arrive about a second ahead of the speech they belong to
	// (spikes/RESULTS.md, s3_sync), so text shown as it arrives is on screen
	// just before it is spoken; text withheld until its sentence ends lands
	// seconds after the voice began it, about as many as the sentence is long.
	DraftInPlace bool

	// WordPieces says the fragments are pieces of words rather than words:
	// OpenAI sends "gre", "ets", " me", "." where Gemini sends " greets me.".
	// A piece carries its own spacing — one with no space in front continues
	// the word before it — so pieces are joined as they come, and the last
	// word of text in progress is not shown until it is finished (the next
	// piece begins with a space, the sentence settles, or WordHoldSeconds
	// pass). Without that a word on screen grows after it was drawn, and the
	// row it ends can lose it to the next one.
	WordPieces bool
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
	hideDraftWhenEmpty  bool
	draftInPlace        bool
	wordPieces          bool

	// tailOpen is set while the last word of pending may still be a piece of
	// a word (WordPieces), and keeps that word off the screen. tailShown is
	// set once the hold on it has run out and it has been drawn: a piece
	// that continues it then makes it grow where it is — hiding it again
	// would take a word off the screen and put a longer one back.
	tailOpen  bool
	tailShown bool

	// pendingSince is when the current pending text began. draftOverdue is
	// set once it has waited DraftFallbackSeconds, and pendingResumed when it
	// is a line pulled back off the screen by a reconnect. Either lets
	// HideDraftWhenEmpty show the draft after all.
	pendingSince   float64
	draftOverdue   bool
	pendingResumed bool

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

	// lastCommitFinished is how the newest settled line came to settle: a
	// whole utterance, or interrupted by a timer. Only the second kind should
	// be resumed after a reconnect.
	//
	// It has to be recorded rather than inferred, which is what the first
	// version of the resume gate got wrong. Punctuation answers the question
	// for most lines, but not for all of them: the server's turn_complete
	// settles whatever is buffered whether or not it ends in a full stop, and
	// "Thank you" is then a complete turn that looks exactly like a phrase cut
	// off mid-sentence. Every commit says which it was.
	//
	// One flag is enough for the whole buffer: commit appends to the queue,
	// and the line a reconnect resumes is always the newest settled one.
	lastCommitFinished bool
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
		hideDraftWhenEmpty:  opts.HideDraftWhenEmpty,
		draftInPlace:        opts.DraftInPlace,
		wordPieces:          opts.WordPieces,
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

// historyBound is how many settled sentences are kept for the display. More
// than maxLines, because the renderer reflows sentences into shared rows —
// see DisplayHistoryFactor. The transcript is unaffected either way; it is
// fed by newCommits, not by this history.
func (b *Buffer) historyBound() int { return b.maxLines * DisplayHistoryFactor }

// CompletePhrasesOnly reports whether unsettled text is withheld.
func (b *Buffer) CompletePhrasesOnly() bool { return b.completePhrasesOnly }

// SetCompletePhrasesOnly turns the mode on or off.
func (b *Buffer) SetCompletePhrasesOnly(on bool) { b.completePhrasesOnly = on }

// HideDraftWhenEmpty reports whether an empty box also withholds unsettled
// text in complete-phrases mode.
func (b *Buffer) HideDraftWhenEmpty() bool { return b.hideDraftWhenEmpty }

// SetHideDraftWhenEmpty turns that on or off.
func (b *Buffer) SetHideDraftWhenEmpty(on bool) { b.hideDraftWhenEmpty = on }

// DraftInPlace reports whether text in progress is drawn where it will stay.
func (b *Buffer) DraftInPlace() bool { return b.draftInPlace }

// SetDraftInPlace turns that on or off.
func (b *Buffer) SetDraftInPlace(on bool) { b.draftInPlace = on }

// WordPieces reports whether fragments are joined as pieces of words.
func (b *Buffer) WordPieces() bool { return b.wordPieces }

// SetWordPieces turns that on or off — the translation service changed.
func (b *Buffer) SetWordPieces(on bool) {
	b.wordPieces = on
	if !on {
		b.tailOpen, b.tailShown = false, false
	}
}

// followsSpeech is true when every word is drawn the moment it arrives and
// stays where it was drawn: text in progress is on screen, in place, and the
// fragments come at the pace of speech. Nothing is paced then — see
// releaseDue.
func (b *Buffer) followsSpeech() bool {
	return b.draftInPlace && !b.completePhrasesOnly && b.EffectivePolicy() == PolicyIncremental
}

// CommittedLines returns the settled lines, newest last.
func (b *Buffer) CommittedLines() []string { return append([]string(nil), b.committed...) }

// PendingText is the text not yet settled. Drawn dimmed.
func (b *Buffer) PendingText() string { return b.pending }

// shownPending is the text in progress as it may be drawn: all of it, or —
// while its last word may still be a piece of one — all but that word.
func (b *Buffer) shownPending() string {
	if !b.tailOpen {
		return b.pending
	}
	if i := strings.LastIndexFunc(b.pending, unicode.IsSpace); i >= 0 {
		return b.pending[:i]
	}
	return ""
}

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

// DisplayLines returns the settled history, newest last, with the unsettled
// text after it when it may be shown.
//
// This is material for the renderer, not the finished screen: it holds up to
// historyBound sentences — more than MaxLines — because the renderer reflows
// settled sentences into shared rows and trims to MaxLines *rows* after
// wrapping. Handing it exactly MaxLines sentences was the Python rule, where
// one sentence was one row; under reflow it left a three-row box showing one
// packed row and two blank ones.
//
// Unsettled text is withheld while the queue drains: appending live text under
// lines that are still catching up would show them out of order. In
// complete-phrases mode it is withheld whenever a settled line is on screen —
// that is the whole point of the mode, so the reader never watches a phrase
// rewrite itself — and, with HideDraftWhenEmpty, in an empty box too, unless
// it has waited DraftFallbackSeconds or is a line a reconnect pulled back.
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
	//
	// That draft is then redrawn settled when the sentence ends, and readers
	// following along reported it as the caption being written a second time.
	// HideDraftWhenEmpty is the choice between the two: a box that waits, or
	// a sentence drawn twice. Two exceptions keep the waiting box from being
	// a broken one. A draft that has waited DraftFallbackSeconds is shown, or
	// speech that never settles leaves the box blank for as long as it lasts.
	// And a line a reconnect pulled back off the screen to continue is
	// shown, or the one line the reader was reading vanishes until the
	// continuation settles (both the red team's).
	if b.pending != "" && len(b.queue) == 0 &&
		(!b.completePhrasesOnly || (len(b.committed) == 0 &&
			(!b.hideDraftWhenEmpty || b.draftOverdue || b.pendingResumed))) {
		if text := b.shownPending(); strings.TrimSpace(text) != "" {
			lines = append(lines, Line{Text: text, Settled: false})
		}
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
	if bound := b.historyBound(); len(b.committed) > bound {
		b.committed = append([]string(nil), b.committed[len(b.committed)-bound:]...)
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
	//
	// Not for pieces of words: they come over an ordered stream, once each,
	// a quarter of a second apart — and the same piece twice running is what
	// "very very" and "ha ha" look like.
	if !b.wordPieces && text == b.lastText && now-b.lastFeedAt < DuplicateWindowSeconds {
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
	afterReconnect := b.continueNext
	if b.continueNext {
		b.continueNext = false
		if b.pending == "" && !b.lastCommitFinished {
			resumed := ""
			if n := len(b.queue); n > 0 {
				resumed = b.queue[n-1]
				b.queue = b.queue[:n-1]
			} else if n := len(b.committed); n > 0 {
				resumed = b.committed[n-1]
				b.committed = b.committed[:n-1]
				// It was on screen: the reader has already seen it.
				b.pendingResumed = true
			}
			if resumed != "" {
				b.pending = resumed
				b.pendingSince = now
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

	if b.pending == "" {
		b.pendingSince = now
	}
	if b.wordPieces && !afterReconnect && b.pending != "" {
		// A piece says for itself whether it starts a word. Only the first
		// one of a session cannot — it never has a space in front — and that
		// one is joined by the rule below.
		b.pending += text
	} else {
		b.pending = join(b.pending, text)
	}
	if b.wordPieces {
		if first, _ := utf8.DecodeRuneInString(text); unicode.IsSpace(first) {
			b.tailShown = false // a new word; the one before it is finished
		}
		last, _ := utf8.DecodeLastRuneInString(b.pending)
		b.tailOpen = !unicode.IsSpace(last) && !b.tailShown
	}
	b.noteDraftAge(now)

	if b.EffectivePolicy() == PolicyUtterance || b.endsASentence() {
		b.commit(true)
		// Show it straight away when there is room, so a single sentence never
		// waits for the next tick.
		b.drain(now)
	}
	return true
}

// endsASentence reports whether the pending text has reached a sentence end.
//
// The same question looksFinished asks of a settled line, so it is the same
// code: the two were written separately and were identical line for line, which
// meant the next change to what counts as a sentence end could land in one and
// miss the other.
func (b *Buffer) endsASentence() bool { return looksFinished(b.pending) }

// abbreviations are the words whose trailing full stop does not end a sentence.
//
// Only entries whose abbreviation reading dominates belong here, because every
// one of them costs a real sentence its immediate display. The first draft of
// this list included "no", "etc", "inc" and "ltd" and each was wrong for the
// same reason: they end sentences far more often than they abbreviate.
// "The answer is no." would not settle on its full stop, and waited out the
// idle timer — which is precisely the defect the list was written to remove.
var abbreviations = map[string]bool{
	"dr": true, "mr": true, "mrs": true, "ms": true, "prof": true,
	"rev": true, "fr": true, "st": true, "jr": true, "sr": true, "vs": true,
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
	b.commit(true)
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

	// A last word held back for the piece that would finish it has waited
	// long enough: it was the whole word.
	if b.tailOpen && idle >= WordHoldSeconds {
		b.tailOpen, b.tailShown = false, true
		changed = true
	}

	// A draft withheld long enough is shown after all; say so, or the screen
	// would not repaint until the next fragment.
	if b.noteDraftAge(now) {
		changed = true
	}

	// Settle a phrase that stopped without sentence-final punctuation.
	if b.EffectivePolicy() == PolicyIncremental && b.pending != "" && idle >= b.idleCommitSeconds {
		b.commit(false)
		changed = true
	}

	// A clear window shorter than the commit window would otherwise strand
	// pending text: the display would never report itself complete, so it would
	// never clear and the text would never reach the transcript.
	if b.clearAfterSeconds != 0 && b.pending != "" && idle >= b.clearAfterSeconds {
		b.commit(false)
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
	b.commit(false)
	return true
}

// NoteReconnect tells the buffer a reconnect happened, so the next fragment
// may continue the line the old session was part-way through.
//
// "May", not "does": see looksFinished. The server ends a session every nine
// minutes, so this runs seven times in an hour-long talk, and resuming a
// sentence that was already complete glued two unrelated sentences into one
// line every time.
func (b *Buffer) NoteReconnect() {
	b.continueNext = true
	b.lastText = ""
}

// looksFinished reports whether a settled line was a whole sentence.
//
// A line settles two ways: it reached sentence-final punctuation, or the idle
// timer gave up waiting. Only the second kind was cut off, and only that kind
// should be resumed after a reconnect. In complete-phrases mode every line is
// the first kind, which is why the merge was so visible there:
//
//	"The reading is finished here. and then the speaker carried on."
func looksFinished(line string) bool {
	trimmed := strings.TrimRightFunc(line, unicode.IsSpace)
	if trimmed == "" {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(trimmed)
	if !strings.ContainsRune(SentenceEnders, last) {
		return false
	}
	return last != '.' || !endsWithAbbreviation(trimmed)
}

// Clear wipes the display. What was already said still reaches the transcript.
//
// Half-finished text is settled first rather than dropped: recording is
// deliberately independent of what is on screen, and a sentence the speaker
// finished saying should not vanish because the captions were cleared
// underneath it. commit files it under newCommits, and emptying the queue below
// keeps it off the screen.
func (b *Buffer) Clear() {
	b.commit(false)
	b.committed = b.committed[:0]
	b.queue = b.queue[:0]
	b.pending = ""
	b.lastText = ""
	b.continueNext = false
	b.resumedPrefix = ""
	b.draftOverdue = false
	b.pendingResumed = false
}

// --- internals -----------------------------------------------------------

// noteDraftAge marks the pending text overdue once it has waited
// DraftFallbackSeconds, and reports whether that just happened while it is
// being withheld from an empty box — the one case where it changes the
// screen.
func (b *Buffer) noteDraftAge(now float64) bool {
	if b.draftOverdue || b.pending == "" || now-b.pendingSince < DraftFallbackSeconds {
		return false
	}
	b.draftOverdue = true
	// A resumed line is already on screen, so it becoming overdue changes
	// nothing there.
	return b.completePhrasesOnly && b.hideDraftWhenEmpty && len(b.committed) == 0 && !b.pendingResumed
}

// drain releases as many queued lines as their hold times allow.
func (b *Buffer) drain(now float64) bool {
	changed := false
	for b.releaseDue(now) {
		b.committed = append(b.committed, b.queue[0])
		b.queue = b.queue[1:]
		if bound := b.historyBound(); len(b.committed) > bound {
			b.committed = b.committed[len(b.committed)-bound:]
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
	// The line was already on screen, word by word, as it arrived. Holding it
	// now would wipe a sentence the reader is in the middle of and draw it
	// again up to a hold later — and withhold the next sentence's first words
	// behind it, which is the lag this mode exists to remove. There is no
	// burst to pace either: the words came at the pace of speech.
	if b.followsSpeech() {
		return true
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

// commit settles the pending text. `finished` says whether it was a whole
// utterance — a sentence end, the server's turn_complete, or a whole-utterance
// policy — as opposed to a timer giving up on it. Punctuation is honoured on
// top, so a caller that does not know can pass false safely.
func (b *Buffer) commit(finished bool) {
	line := strings.TrimSpace(b.pending)
	b.pending = ""
	b.tailOpen, b.tailShown = false, false
	b.draftOverdue = false
	b.pendingResumed = false
	prefix := b.resumedPrefix
	b.resumedPrefix = ""
	if line == "" {
		return
	}
	b.lastCommitFinished = finished || looksFinished(line)
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
