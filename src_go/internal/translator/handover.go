package translator

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Warm handover: the replacement is opened the moment the warning arrives and
// fed the same audio as the session it replaces, unheard, until both have
// fallen quiet at the same time. Then the replacement is heard from and the
// old session closed in one step.
//
// What it replaces is a cold swap. There the replacement started listening at
// the swap and needed 3.3–5.2 s of input before it said anything, which the
// retiring session's tail covered with about 2 s of silence left over at every
// handover. The replacement's first audio is also where the far end's
// frozen-and-fading buzz lives (8 of 15 measured), and the retiring session,
// starved of input mid-sentence, ended its last word on another (5 of 10
// handovers). A warm replacement has done its warm-up and its opening before
// anyone hears it, and the old session is only cut off once it has finished
// what it was saying.
//
// These are variables so the tests can shorten them. Nothing outside a test
// changes them.
var (
	// HandoverQuiet is how long both sessions' output must have been silent,
	// with both their latest captions closing a sentence, for the switch.
	// Measured on Gemini (spike o3_handover, the Korean fixture): output
	// pauses are short, and two sessions translating the same speech rarely
	// pause together — 0.5 s of shared silence never came in 45 s of
	// warm-up, and the switch was forced. 200 ms of silence in the audio
	// itself, at a sentence end in both, came for 1.0–1.2 s in every
	// handover.
	HandoverQuiet = 200 * time.Millisecond

	// HandoverMatch is how alike the two sessions' last sentences must be.
	// A shared pause is not enough on its own: a fresh OpenAI session runs
	// ahead of the one it replaces (measured, spike o3_handover), so both
	// fell quiet at sentence ends a whole sentence apart, and the switch lost
	// "There are no sentence breaks." Two translations of the same source
	// sentence scored a median 0.81 and a 10th percentile 0.65 on character
	// bigrams; unrelated sentences a median 0.16 and a 90th percentile 0.31.
	HandoverMatch = 0.5

	// HandoverWarm is how long the replacement must have been producing audio
	// before it may be switched to: past its opening, where the latest
	// measured freeze ended 0.84 s in, with margin.
	HandoverWarm = 1500 * time.Millisecond

	// HandoverMaxWait caps the wait however much grace there is. Gemini
	// warns 50 s ahead, but OpenAI's session warns itself ten minutes ahead,
	// and waiting most of that for a pause would be ten minutes of two
	// billed sessions. Measured waits for a pause: 6.5–23.8 s.
	HandoverMaxWait = 45 * time.Second

	// HandoverReserve is the part of the server's grace the wait never
	// spends. The replacement is already open and warm, so the switch itself
	// is instant; this only keeps it clear of the far end hanging up.
	HandoverReserve = 5 * time.Second

	// handoverPoll is how often the supervisor looks for the moment. A pause
	// lasts a second or two, so this has to be fine-grained, unlike prepPoll.
	handoverPoll = 20 * time.Millisecond
)

// voiceRMS is the level above which a blob of translated audio counts as
// speech. Both models send digital zeros between utterances; -50 dBFS is far
// above that and far below any word.
const voiceRMS = 0.003

// handOver opens a warm replacement for old and waits for the moment to
// switch to it. It returns the replacement, ready to be made active, or nil
// to fall back to a cold swap: no stated grace, a replacement that would not
// open or died warming, or the run stopping.
//
// It chooses only *when*: whatever happens, the switch is made before the
// server's deadline.
func (t *Translator) handOver(ctx context.Context, old *sessionHolder) *sessionHolder {
	if handoverBudget(old.graceLeft()) <= 0 {
		return nil
	}
	next := t.openSession(ctx, true)
	if next == nil {
		return nil
	}
	t.mu.Lock()
	t.warming = next
	t.mu.Unlock()
	// Taken now, not before the open: connecting can take up to
	// OpenTimeout, and that time comes out of the server's grace too.
	budget := max(handoverBudget(old.graceLeft()), 0)
	slog.Info("warming a replacement session", "budget", budget.Round(time.Second))

	// switchTo hands the replacement over, unless it died in the meantime:
	// a select with several ready cases picks one at random, so the pause
	// test can win against the replacement closing. Switching to a dead
	// session would close the healthy old one for nothing.
	started := time.Now()
	switchTo := func(quiet bool) *sessionHolder { return t.claim(next, quiet, time.Since(started)) }

	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	ticker := time.NewTicker(handoverPoll)
	defer ticker.Stop()
	polls := 0
	for {
		select {
		case <-ctx.Done():
			t.dropWarming(next)
			return nil
		case <-next.closed:
			slog.Warn("the warming replacement ended before the switch — swapping cold")
			t.dropWarming(next)
			return nil
		case <-old.closed:
			slog.Info("the session ended while its replacement warmed — switching now",
				"warm", next.warmFor() > 0)
			return switchTo(false)
		case <-deadline.C:
			slog.Info("no pause came — switching anyway",
				"waited", time.Since(started).Round(time.Millisecond))
			return switchTo(false)
		case <-ticker.C:
			// What the decision below sees, for tuning its thresholds from a
			// real run rather than by guessing. Debug only, and thinned to
			// ten a second.
			if polls++; polls%5 == 0 && slog.Default().Enabled(ctx, slog.LevelDebug) {
				slog.Debug("handover poll",
					"old_quiet_ms", old.quietFor().Milliseconds(),
					"new_quiet_ms", next.quietFor().Milliseconds(),
					"new_warm_ms", next.warmFor().Milliseconds(),
					"old_end", old.sentenceEnded.Load(),
					"new_end", next.sentenceEnded.Load(),
					"match", similarity(lastSentence(old.recentText()), lastSentence(next.recentText())))
			}
			// A song is better than a pause: nobody is listening at all. But
			// the gate withholds input, not output — the old session is still
			// translating what was said just before the song, and switching
			// then muted it mid-sentence (the red team's third round). So the
			// gate also waits for both to fall quiet, which with no input
			// going out they do within seconds; the replacement need not be
			// warm, since nothing is being said for it to warm up on.
			if t.adaptivePrep && t.quietMoment != nil && t.quietMoment() &&
				old.quietFor() >= HandoverQuiet && next.quietFor() >= HandoverQuiet {
				slog.Info("switching sessions under the music gate",
					"waited", time.Since(started).Round(time.Millisecond))
				t.notePreppedSwap(time.Since(started))
				return switchTo(true)
			}
			if next.warmFor() >= HandoverWarm && pauseShared(old, next) {
				slog.Info("switching sessions in a pause",
					"waited", time.Since(started).Round(time.Millisecond))
				return switchTo(true)
			}
		}
	}
}

// claim hands the replacement over for the switch, unless it has closed in
// the meantime — see switchTo in handOver — in which case it is dropped and
// the swap falls back to a cold one.
func (t *Translator) claim(next *sessionHolder, quiet bool, waited time.Duration) *sessionHolder {
	if next.isClosed() {
		slog.Warn("the warming replacement ended at the switch — swapping cold")
		t.dropWarming(next)
		return nil
	}
	t.noteHandover(quiet, waited)
	return next
}

// handoverBudget is how long a warm handover may wait for a pause, given the
// grace the server stated.
func handoverBudget(grace time.Duration) time.Duration {
	return min(grace-HandoverReserve, HandoverMaxWait)
}

// dropWarming closes a replacement that will not be switched to.
func (t *Translator) dropWarming(next *sessionHolder) {
	t.mu.Lock()
	if t.warming == next {
		t.warming = nil
	}
	t.mu.Unlock()
	t.closeSession(next)
}

// noteHandover records whether a warm switch found its moment, and how long
// it waited.
func (t *Translator) noteHandover(quiet bool, waited time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if quiet {
		t.stats.QuietHandovers++
	} else {
		t.stats.ForcedHandovers++
	}
	t.stats.HandoverWaitsMS = append(t.stats.HandoverWaitsMS, float64(waited.Microseconds())/1000.0)
}

// noteOutput records what one message says about whether this session is
// speaking. Called by the session's own receive loop for every message.
func (h *sessionHolder) noteOutput(msg Message) {
	now := time.Now().UnixNano()
	if len(msg.Audio) > 0 {
		h.firstAudio.CompareAndSwap(0, now)
		// The blob arrives as it ends, so the voice in it stopped as long
		// before now as the silence at its tail lasts. Measuring by arrival
		// alone could not see a pause shorter than the ~250 ms between blobs.
		if tail, voiced := trailingSilence(msg.Audio); voiced {
			h.lastVoice.Store(now - tail.Nanoseconds())
		}
	}
	if msg.TargetText != "" {
		h.lastVoice.Store(now)
		h.sentenceEnded.Store(endsSentence(msg.TargetText))
		h.noteText(msg.TargetText)
		if h.warming.Load() && slog.Default().Enabled(context.Background(), slog.LevelDebug) {
			slog.Debug("warming caption", "text", msg.TargetText)
		}
	}
}

// endsSentence reports whether a caption fragment closes a sentence.
func endsSentence(text string) bool {
	t := strings.TrimRight(text, " \t\n\"'”’)")
	if t == "" {
		return false
	}
	switch t[len(t)-1] {
	case '.', '?', '!':
		return true
	}
	return strings.HasSuffix(t, "。") || strings.HasSuffix(t, "？") || strings.HasSuffix(t, "！")
}

// quietFor is how long this session has said nothing. A session that has
// never spoken has been quiet since it opened.
func (h *sessionHolder) quietFor() time.Duration {
	last := h.lastVoice.Load()
	if last == 0 {
		last = h.openedAt.Load()
	}
	if last == 0 {
		return 0
	}
	return time.Duration(time.Now().UnixNano() - last)
}

// warmFor is how long this session has been producing audio; zero until its
// first blob.
func (h *sessionHolder) warmFor() time.Duration {
	first := h.firstAudio.Load()
	if first == 0 {
		return 0
	}
	return time.Duration(time.Now().UnixNano() - first)
}

// trailingSilence measures the silence at the end of a blob of 24 kHz PCM16,
// in 10 ms windows, and reports whether there was any voice before it.
func trailingSilence(pcm []byte) (time.Duration, bool) {
	const window = 240 * 2 // 10 ms at 24 kHz, in bytes
	n := len(pcm) / window
	for i := n - 1; i >= 0; i-- {
		if rms(pcm[i*window:(i+1)*window]) >= voiceRMS {
			return time.Duration(n-1-i) * 10 * time.Millisecond, true
		}
	}
	return 0, false
}

// pauseShared reports whether both sessions are in the same pause: silent
// for HandoverQuiet, both at a sentence end, and that sentence the same one.
//
// A looser test was tried for late in the wait — longer silence, no sentence
// end asked — and it lost a clause, "This figure is a bit higher than
// expected", that the replacement had already said unheard. Nothing loose is
// safe while the two can be at different places in the speech, so the only
// fallback is the deadline.
func pauseShared(old, next *sessionHolder) bool {
	if old.quietFor() < HandoverQuiet || next.quietFor() < HandoverQuiet ||
		!old.sentenceEnded.Load() || !next.sentenceEnded.Load() {
		return false
	}
	a, b := lastSentence(old.recentText()), lastSentence(next.recentText())
	return digits(a) == digits(b) && similarity(a, b) >= HandoverMatch
}

// digits is every digit in s, in order. Two sentences built the same way —
// "Sales rose 5% in the first quarter", "… 7% in the second" — score well
// above HandoverMatch on their shared words, and the numbers are what tell
// them apart. A translation keeps a number as a number, so two sessions
// translating one sentence agree on its digits.
func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// recentTextBytes is how much of a session's captions noteText keeps: a few
// sentences, enough to find the last one.
const recentTextBytes = 600

// noteText keeps the tail of this session's captions.
func (h *sessionHolder) noteText(text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recent += text
	if over := len(h.recent) - recentTextBytes; over > 0 {
		cut := over
		for cut < len(h.recent) && !utf8.RuneStart(h.recent[cut]) {
			cut++
		}
		h.recent = h.recent[cut:]
	}
}

func (h *sessionHolder) recentText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.recent
}

// lastSentence is the final complete sentence of text, without the text
// before it.
//
// A full stop between two digits is a decimal point, not a sentence end:
// cutting " The average delay was 3.3 seconds." at it left "3 seconds", and
// two different numeric sentences shrank to tails that matched (the red
// team's second round).
func lastSentence(text string) string {
	t := strings.TrimRight(text, " \t\n\"'”’)")
	t = strings.TrimRightFunc(t, isTerminator)
	for i := len(t); i > 0; {
		r, size := utf8.DecodeLastRuneInString(t[:i])
		i -= size
		if isTerminator(r) && !decimalPoint(t, i, size) {
			return strings.TrimSpace(t[i+size:])
		}
	}
	return strings.TrimSpace(t)
}

// decimalPoint reports whether the terminator at t[i:i+size] is a full stop
// with a digit on each side.
func decimalPoint(t string, i, size int) bool {
	if t[i] != '.' || i == 0 || i+size >= len(t) {
		return false
	}
	before, _ := utf8.DecodeLastRuneInString(t[:i])
	after, _ := utf8.DecodeRuneInString(t[i+size:])
	return unicode.IsDigit(before) && unicode.IsDigit(after)
}

func isTerminator(r rune) bool {
	switch r {
	case '.', '?', '!', '。', '？', '！':
		return true
	}
	return false
}

// similarity is the Dice coefficient of two strings' character bigrams,
// letters and digits only, case folded. Language-agnostic: it works on
// Korean syllables as it does on English letters, and it forgives the
// different wording two sessions give the same sentence.
func similarity(a, b string) float64 {
	ga, gb := bigrams(a), bigrams(b)
	total := 0
	for _, n := range ga {
		total += n
	}
	for _, n := range gb {
		total += n
	}
	if total == 0 {
		return 0
	}
	shared := 0
	for g, n := range ga {
		shared += min(n, gb[g])
	}
	return 2 * float64(shared) / float64(total)
}

func bigrams(s string) map[[2]rune]int {
	var rs []rune
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			rs = append(rs, r)
		}
	}
	out := map[[2]rune]int{}
	for i := 0; i+1 < len(rs); i++ {
		out[[2]rune{rs[i], rs[i+1]}]++
	}
	return out
}
