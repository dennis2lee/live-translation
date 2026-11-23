package ui

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// wrapAnchor remembers how the caption paragraph was last wrapped, so the
// rows the reader is on keep their breaks when the paragraph changes
// somewhere else.
//
// The paragraph is the buffer's settled history joined end to end, and the
// buffer drops its oldest sentence once the history is full — every sentence,
// in speech that goes on for a couple of minutes without a pause long enough
// to clear the box. Wrapped afresh from its new first word, the paragraph
// breaks in different places all the way down: every row on screen changed at
// once, at the end of a sentence, although nothing the reader could see had
// been added or removed. Measured on a recorded session replayed until the
// history was full: 24 such redraws in 656 fragments with four rows, 74 with
// two (spikes/RESULTS.md, s3_sync).
//
// Greedy wrapping from a row's first word reproduces that row and every row
// after it, so it is enough to start again from a row boundary that survived
// rather than from the top.
type wrapAnchor struct {
	// key is everything a break depends on besides the text: the font, its
	// size and the width. Any change there wraps afresh.
	key string
	// text is the paragraph as last wrapped, and starts the byte offset in
	// it of each row's first character.
	text   string
	starts []int

	// carried is whether the last wrap carried rows over from the one before
	// it, and cut how many bytes went from the front of the old paragraph
	// when it did: a row that began at old offset o begins at o-cut now.
	// That is what the scroll animation follows a row by — see scrollMove.
	carried bool
	cut     int
}

// alignProbe is how much of the new paragraph is looked for in the old one to
// find where the two line up. Long enough that a repeated "Amen. Amen." does
// not line them up in the wrong place often; and when it does, the rows are
// still a correct wrap of the text, only not the steadiest one — see below.
const alignProbe = 32

// wrapAnchored wraps text like Wrap, keeping every break it can from the last
// time. With a nil anchor it is Wrap. boxRows is how many rows the box shows.
//
// Whatever it keeps is checked byte for byte against the old paragraph, so
// the rows are always the text, in order, each within the width — the anchor
// only chooses between wraps that are all correct.
func (f Face) wrapAnchored(a *wrapAnchor, key, text string, fontSize, maxWidth float32, boxRows int) []string {
	text = strings.TrimSpace(text)
	if a == nil {
		return f.Wrap(text, fontSize, maxWidth)
	}
	rows, cut := f.keptRows(a, key, text, fontSize, maxWidth, boxRows)
	a.carried, a.cut = rows != nil, cut
	if rows == nil {
		// A paragraph shorter than alignProbe cannot be aligned at all, so
		// keptRows gives up on it. When the new text only continues it, the
		// fresh greedy wrap below starts every old row where it started; and
		// where it does not, the scroll's lookup simply misses.
		a.carried = a.key == key && a.text != "" && strings.HasPrefix(text, a.text)
		rows = f.Wrap(text, fontSize, maxWidth)
	}
	a.key, a.text, a.starts = key, text, rowStarts(text, rows)
	if a.starts == nil {
		a.carried = false
	}
	return rows
}

// keptRows is the wrap that keeps the old breaks, or nil when there is nothing
// to keep: another font or width, or text that does not continue the old.
// cut is how many bytes of the old paragraph went from the front.
//
// Every row it carries over keeps its first character's offset, less cut:
// rows j to m-1 are sliced at their old starts, row m is wrapped again from
// its old start, and the fresh-row rule below re-emits row m from that same
// start. The scroll animation depends on that.
func (f Face) keptRows(a *wrapAnchor, key, text string, fontSize, maxWidth float32, boxRows int) (rows []string, cut int) {
	if a.key != key || a.text == "" || text == "" || len(a.starts) == 0 {
		return nil, 0
	}
	// k: how much of the old paragraph went from the front. d: the first old
	// offset that is not carried over unchanged. Text added at the end
	// leaves d at the old length; a sentence pulled back off the end, or
	// anything rewritten, puts it earlier.
	k, shared := align(a.text, text)
	if k < 0 {
		return nil, 0
	}
	d := k + shared

	// Rows j up to m keep their breaks: j is the first row that begins at or
	// after the cut, m the last that begins before the change. Row m itself
	// is wrapped again, since what follows its first word may differ.
	j := sort.SearchInts(a.starts, k)
	m := sort.SearchInts(a.starts, d) - 1
	if j >= len(a.starts) || m < j {
		return nil, 0
	}

	// What is left of the row the cut went through. It lost its first words,
	// so it is short — and above the rows on screen unless the whole
	// paragraph fits in the box.
	rows = f.Wrap(text[:a.starts[j]-k], fontSize, maxWidth)
	for i := j; i < m; i++ {
		rows = append(rows, strings.TrimSpace(text[a.starts[i]-k:a.starts[i+1]-k]))
	}
	kept := len(rows)
	rows = append(rows, f.Wrap(text[a.starts[m]-k:], fontSize, maxWidth)...)

	// Words that finish a row which leaves the box in the same repaint are
	// never seen. With one row that is every row: "…earnings meeting" on
	// screen, " will now begin." arrives, the box shows "begin." and "will
	// now" was never drawn (the red team: 10% of a session's characters at
	// one row). So when the text is only added to and the old last row would
	// scroll out at once, the old row is left as it was read and the new text
	// starts a row of its own — provided the new text then fits the box. When
	// it does not, its beginning goes by unseen either way, and running it on
	// from the old row is what shows the most of it.
	oldEnd := len(a.text) - k
	if d == len(a.text) && oldEnd < len(text) && len(rows)-(kept+1) >= boxRows {
		if added := text[oldEnd:]; startsWithSpace(added) {
			if fresh := f.Wrap(added, fontSize, maxWidth); len(fresh) <= boxRows {
				rows = append(rows[:kept], strings.TrimSpace(text[a.starts[m]-k:oldEnd]))
				rows = append(rows, fresh...)
			}
		}
	}
	return rows, k
}

// startsWithSpace reports whether s begins a new word rather than continuing
// the one before it.
func startsWithSpace(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsSpace(r)
}

// align finds where now continues old: old[k:] and now begin alike, for
// `shared` bytes. k is 0 when nothing went from the front of old, and -1 when
// now does not continue old at all.
//
// Where the same words come round again — a refrain, a repeated "Amen." —
// more than one place fits the opening of now; the one that fits furthest is
// the cut. A wrong guess costs steadiness, never correctness: keptRows only
// carries over rows that lie inside the stretch found to be the same.
func align(old, now string) (k, shared int) {
	// Bytes, not characters: the probe only finds places to try, and each is
	// then compared in full.
	probe := now
	if len(probe) > alignProbe {
		probe = probe[:alignProbe]
	}
	k, shared = -1, 0
	try := func(at int) {
		if n := commonPrefix(old[at:], now); n > shared {
			k, shared = at, n
		}
	}
	for from := 0; from < len(old); {
		i := strings.Index(old[from:], probe)
		if i < 0 {
			break
		}
		try(from + i)
		from += i + 1
	}
	return k, shared
}

// commonPrefix is how many bytes a and b share from the start. It may stop
// inside a character; nothing is ever cut there — rows are cut at row starts.
func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// rowStarts finds where each row begins in text. Rows are stretches of text
// in order, trimmed, so each is found from where the last one ended; nil if
// one is not, which would mean they are not a wrap of this text.
func rowStarts(text string, rows []string) []int {
	starts := make([]int, 0, len(rows))
	pos := 0
	for _, row := range rows {
		i := strings.Index(text[pos:], row)
		if i < 0 || row == "" {
			return nil
		}
		starts = append(starts, pos+i)
		pos += i + len(row)
	}
	return starts
}
