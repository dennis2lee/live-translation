"""The caption buffer's deliberate departures from the frozen Python reference.

`src/` is the executable specification and nothing lands in it, so the four
2026-08-08 caption fixes would make every bufdiff run diverge and the harness
would stop saying anything. Instead the deviations are applied to the reference
*at runtime*, here, in one place. bufdiff then still holds: any divergence it
reports is an accident, because every intended one is written below.

Each function mirrors the Go code it was ported to. Reported by Dennis from a
67-minute complete-phrases session; measured with spikes/capscenario.
"""

import re

ABBREVIATIONS = {
    "dr", "mr", "mrs", "ms", "prof", "rev", "fr", "st", "jr", "sr",
    "vs", "etc", "no", "vol", "approx", "inc", "ltd",
}
CATCH_UP_FACTOR = 2


def _ends_with_abbreviation(settled: str) -> bool:
    trimmed = settled.removesuffix(".")
    if not trimmed:
        return True
    fields = trimmed.split()
    if not fields:
        return True
    word = re.sub(r"^[^\w]+|[^\w]+$", "", fields[-1], flags=re.UNICODE)
    if not word:
        return False
    if len(word) == 1:
        return word.isalpha()
    return word.lower() in ABBREVIATIONS


def apply(cb) -> None:
    """Patch the frozen CaptionBuffer with the Go build's caption fixes."""

    # 1. A full stop settles whatever the sentence's length; an abbreviation
    #    still does not. Replaces the blanket MIN_SENTENCE_CHARS rule, which
    #    made a short sentence wait out the idle timer.
    def ends_a_sentence(self) -> bool:
        settled = self._pending.rstrip()
        if not settled.endswith(tuple(cb.SENTENCE_ENDERS)):
            return False
        if settled[-1] != ".":
            return True
        return not _ends_with_abbreviation(settled)

    # 2. Falling behind halves the hold before it abolishes it, so a burst is
    #    drawn line by line instead of being dumped in one repaint.
    def release_due(self, now: float) -> bool:
        if not self._queue:
            return False
        if len(self._committed) < self.max_lines:
            return True
        hold = self.line_hold_seconds
        if len(self._queue) > self.max_lines * CATCH_UP_FACTOR:
            return True
        if len(self._queue) > self.max_lines:
            hold /= 2
        return now - self._released_at >= hold

    # 3. Complete-phrases mode shows the text in progress when withholding it
    #    would leave the box empty — there is no settled line to protect, and a
    #    blank box during a long sentence tells the reader nothing.
    def display_lines(self):
        lines = [(t, True) for t in self._committed]
        if self._pending and not self._queue and (
            not self.complete_phrases_only or not self._committed
        ):
            lines.append((self._pending, False))
        return lines[-self.max_lines:]

    cb.CaptionBuffer._ends_a_sentence = ends_a_sentence
    cb.CaptionBuffer._release_due = release_due
    cb.CaptionBuffer.display_lines = display_lines
