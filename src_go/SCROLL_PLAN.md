# Smooth caption scrolling — design

Status: shipped in 1.14.0 after two design and two implementation red-team rounds (Fable).

## The request

When new translated text no longer fits the box, the rows already on screen
move up by one and the new text is written on the freed row. Today that move
is a jump: one repaint shows the old rows, the next shows them a row higher.
Make the move an animation. It must not slow the captions down — the
user's words: too slow and it gets in the way of the translation.

## What "a row moves up" is, precisely

The renderer (`captionRenderer.rebuild`) wraps the buffer's lines into rows
(`Face.reflowAnchored`) and keeps the newest `maxLines`. Rows on screen move
in three situations, all of which this design treats the same way:

1. **Overflow** (the request): the box is full and new text needs another
   row. Every visible row moves up one `lineH`; the top row leaves.
2. **Bottom-aligned growth**: a fixed box (the default) is bottom-aligned,
   so while it is not yet full a new row also pushes the rows above it up by
   one `lineH`. Nothing leaves.
3. **Several rows at once**: a long fragment (OpenAI sends 200 ms of speech
   per delta, Gemini ~14 characters a second apart) can add two rows in one
   repaint. Everything moves up `2·lineH`.

The rows that *stay* are unchanged in text — `wrapAnchor` (1.13.0) keeps row
breaks steady, and a fragment is only ever appended to — except that the last
old row may have grown. That is what makes the movement detectable from the
rows alone.

Situations that must **not** animate, because no row moved, or a row's
content changed rather than its place:

- the last row grows within itself (most repaints);
- the box is cleared (clear-on-silence, `m`, Stop, direction change);
- font, size, width, line count, box position, fixed-box or the window size
  changes — the paragraph is wrapped afresh;
- the anchor fails and the paragraph re-wraps differently (rare since 1.13.0);
- DimDraft's dimmed draft rows are folded into the paragraph when a sentence
  settles (rows rewritten, not moved);
- a downward move (a non-fixed box shrinking): snap, as today.

## Detection

Detection runs **only in a rebuild that follows a change of the lines**
(`SetLines`, `Clear` bump a version counter). Every other rebuild — the
source line, the status, the hint fade, the dot pulse, the scroll's own
frames — draws the cached wrap and leaves a running move alone. A
"no move" result never cancels a move already running; only the cancel list
below does.

It is done with the anchor's **byte offsets**, not by comparing row strings
(red team, design round): `keptRows` re-wraps the old last row, and when that
row ended in an unfinished piece of a word ("…the quick trans" + "lation"),
the piece moves down and the row gets *shorter*, so no string rule can find it.
The row's **start** survives exactly, because `keptRows` keeps the start of
every row it carries over, row *m* included.

- `wrapAnchored` reports whether it carried rows over (`keptRows` succeeded)
  and the cut `k` it used: new offset = old offset − `k`.
- The tracked row is the **last settled row** of the previous repaint. Its
  start offset is mapped into the new paragraph and looked up in the new
  starts; an exact hit gives its new index, anything else gives no move.
  Settled rows come first in the full row list, so in DimDraft mode the draft
  rows below do not disturb it — and when a draft grows onto a new row and
  pushes the settled rows up, that is a real move and animates.
- The movement in **pixels** is that row's `y` in the previous repaint minus
  its `y` (real, or virtual above the box) in this one — right for every box
  mode: `rows·lineH` in a fixed bottom-aligned box, `lineH/2` per row in a
  growing middle box, `0` in a growing top box.

- **No settled row** (DimDraft, the first sentence after a clear, or
  complete-phrases' draft in an empty box): the last *draft* row is tracked
  instead, by its offset in the draft text, provided the new draft has the old
  one as a prefix — greedy wrapping from a fixed start keeps those starts as
  steady as the anchor's (design round two).
- **A paragraph shorter than `alignProbe`** (32 bytes — ten Hangul, or a large
  font in a narrow window) cannot be aligned, so `keptRows` gives up. When the
  new text simply continues the old, the wrap is reported as carried with a cut
  of 0: a fresh greedy wrap reproduces the old starts, and where it does not
  the lookup misses and the move snaps (design round two).

Only a strictly upward move animates. No hit, no move, a downward move, a box
whose left edge moved (a non-fixed box hugs its widest row, so it also moves
sideways) — all are drawn at once, exactly as today.

`reflowAnchored` gains the full row list before trimming to `maxLines`; it
already computes it.

## Drawing the move

- **Offset.** All rows are drawn at their final `y` plus an offset that starts
  at the measured movement and eases to `0`. It is set in the detecting
  rebuild itself, so not one frame is painted at the final position first;
  the animation is started after that rebuild returns (Fyne's test driver
  ticks an animation to its end inside `Start`, which would otherwise
  re-enter `rebuild`).
- **Leaving rows** ("ghosts"): the previous repaint's rows that are above the
  new top row are kept as text with their virtual `y`, drawn with the same
  offset, and **fade** with the fraction of their height still inside the
  text area, so a row dissolves through the top padding instead of being
  sliced by the clip.
- **Entering rows are drawn at full alpha** and only clipped (red team: they
  carry the newest words, and in a one-row box they are everything on
  screen — fading them in would delay exactly what the reader is waiting
  for). 24 px of padding is visible box, so the top of an entering row shows
  in the first frame.
- **Clipping.** Text is drawn inside a `container.Clip` (Fyne 2.7+) the size
  and place of the box, between the box and the source line in the object
  order, with rows positioned relative to it. Nothing moving is drawn over
  the background, the source line, the hint or the dot. Corner radius (≤ 48)
  against `PadH` (32) leaks a few pixels of a fading ghost at most.
- **Interrupted.** If the rows move again while a move is running, the new
  move starts from where the text *is*: start offset = what is left of the
  running offset + the new movement, with **no cap** — a cap made a one-row
  box jump at every interruption. Ghosts carry over, moved by the new
  movement; any that can no longer reach the box are dropped. Repeated
  interruptions are self-limiting: 100 ms into a move only a quarter of it
  is left.
- **Cancelled** — offset 0, ghosts dropped — by an empty wrap (the real clear
  paths: clear-on-silence and a muted `pushLines` both arrive as
  `SetLines(empty)`, not `Clear`), a geometry change (font, size, width,
  lines, position, margin, fixed box, DimDraft, window size), SmoothScroll
  switched off, and `StopAnimations`.
- **Few textures** (implementation red team, measured in the real GL driver).
  Fyne's GL painter caches a text texture by its *content* — text, size,
  style, font and colour — for a minute. So a row that only moves costs no
  rasterising at all, and the design's "refresh only on change" rule saved
  nothing and was dropped. But a leaving row faded in continuous alpha was a
  new texture every frame: graphics memory went from 62 MB to 303 MB at
  1280x720 over 84 fragments. The fade is therefore quantised to
  `ghostFadeSteps` (3) visible levels, three textures per leaving row, and a
  test pins how many alphas a leaving row takes.
- **A change that is not a push ends a running move** (implementation red
  team): when new lines arrive and the tracked row cannot be found, moved
  down, or the non-fixed box moved sideways, the move is cancelled. Left
  running, its leaving row was drawn beside the same text back on screen —
  60 times in 5059 fuzzed DimDraft moves.

## Timing

`ScrollDuration` = **150 ms**, `fyne.AnimationEaseOut` (fast start, slow
finish).

- **The text is never delayed.** The new words are in the layout, at full
  alpha, in the repaint they arrive in; only their position eases. Half the
  distance is covered in under 45 ms.
- **It is short against the rate rows turn over.** Fragments arrive on a
  one-second server clock (93 gaps, median 1000 ms — `spikes/RESULTS.md`); a
  row of ~50 characters at ~15 characters a second fills in about three
  seconds. A scroll is a twentieth of that.
- 150 ms is the fast end of the usual range for small movements (150–250 ms):
  long enough for the eye to follow the row it was reading, short enough not
  to trail the voice, which 1.13.0 spent a release making the text keep up
  with. It is a judgement about reading, not a measurement — the one
  constant here that is — and the release notes say so.

## Setting

`Settings.SmoothScroll` (default **on**, JSON `smooth_scroll`), Preferences
→ Captions, a checkbox "Smooth scrolling" sharing a row with "Show the source
text" — one more row would make the tab taller than the dialog, which
`TestNoTabOverflowsTheDialog` measures. Off draws every move at once, exactly
as 1.13.0. Applies to a running session without a restart (it is not in
`sessionNeedsRestart`). A missing key in an existing `settings.json` decodes
to the default, so upgraders get it on.

Off exists because motion is a matter of taste and of eyesight (macOS's own
Reduce Motion setting); LiveCap does not read the system setting — Fyne does
not expose it, and guessing at it from `defaults` is the kind of platform
branching CLAUDE.md keeps in one place.

## Cost

The wrap is cached: `reflowAnchored` runs only when the lines version or the
wrap key changed, which takes it out of every animation frame and every frame
of the connecting dot's pulse. A frame is a microsecond or two of CPU; its GPU
cost is bounded by the fade steps above.

## Tests

- Detection through the anchor: growth within a row → none; one row of
  overflow → one row; two rows in one fragment → two; a fragment that
  completes a word across the row end (the row gets shorter) → one row; a
  refrain; a one-row box; history trimmed at the front with nothing visible
  moving → none; DimDraft draft growing onto a new row → one row.
- View, with the animation driven by hand (an injectable starter) **and** one
  test with the real `fyne.Animation` (duration, curve, stopped by
  `StopAnimations` and `Destroy`):
  - at progress 0 every surviving row is where it was last repaint, to the
    pixel; at 1 every row is where a SmoothScroll-off view puts it;
  - mid-move the leaving row is above the top row with alpha in (0,1), the
    entering row below at full alpha;
  - a second move mid-move at **MaxLines 1 and 2** (where a cap would bite):
    no surviving row's drawn `y` jumps between the frames either side;
  - `SetSourceText`, `SetStatus`, `SetMusicGated`, a hint and a pulse frame
    mid-move leave the offset untouched;
  - cancelled by `SetLines(nil)`, an empty wrap, a resize, a font change,
    DimDraft, SmoothScroll off — and **through `Window`**: `ToggleMute` and a
    clear-on-silence tick mid-move leave nothing drawn;
  - every text and ghost is a descendant of the clip, and the clip is the
    box's rectangle;
  - across the recorded `spoken` fixture fed fragment by fragment, at rest
    every row's text, colour and position equals SmoothScroll off's, **and the
    number of moves detected is above zero and equals an independent count**
    (a string oracle, exact for that fixture's whole words) — otherwise the
    equality passes with detection dead;
  - a leaving row takes at most `ghostFadeSteps`+1 alphas, keeps a draft's
    dimming and the box's fade;
  - DimDraft and in-place sessions with sentences settling mid-move never
    draw a word twice.
- Settings: default on, absent key → on, Preferences round trip
  (`prefsync`), `TestNoTabOverflowsTheDialog`.
- Mutation check of the detection and the offset arithmetic.

## Not measured

How it reads on a projector at a distance, and on Windows, where Fyne's GL
frame pacing has not been looked at. Neither is checkable from here; both go
in the release notes.
