"""Assembles transcription fragments into display lines.

`output_transcription.text` has no partial/final marker and no ordering
guarantee, so turning fragments into lines is entirely the client's job.

This module must stay free of Qt and network dependencies, and covered by unit
tests.
"""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass, field
from enum import StrEnum

SENTENCE_ENDERS = ".?!"
IDLE_COMMIT_SECONDS = 1.2
CLEAR_AFTER_SECONDS = 2.0

# A settled line has to stay on screen this long before the next one may push
# it off. The model translates a whole utterance at a time, so several sentences
# can settle inside a single 100 ms display tick; Qt coalesces the repaints and
# the reader only ever sees the last pair. Releasing the backlog one line at a
# time is what stops a burst flashing past unread.
LINE_HOLD_SECONDS = 0.7

# Ceiling on how long captions may sit on screen after the last fragment,
# whatever the input is doing. Without it a noisy room would keep resetting the
# clear countdown for ever.
MAX_CLEAR_WAIT_SECONDS = 15.0

# A full stop only ends a sentence once there is this much text behind it.
# Abbreviations — "Dr.", "vs.", "Mr." — otherwise settle a two-word fragment as
# though it were a whole sentence. Only consulted in complete-phrases mode,
# where a premature commit is what the reader actually sees.
MIN_SENTENCE_CHARS = 20

# How soon a byte-identical fragment counts as the API redelivering the same
# one rather than the speaker genuinely repeating a word. Fragments arrive about
# a second apart (spikes/RESULTS.md), so this sits far below any real repeat —
# " the" twice in one sentence is ordinary English and must not be swallowed.
DUPLICATE_WINDOW_SECONDS = 0.25

_AUTO_SAMPLE_SIZE = 8
_AUTO_UTTERANCE_CHARS = 25


class Policy(StrEnum):
    """When to promote pending text into a committed line."""

    AUTO = "auto"
    """Decide from the first few fragments. Only useful before measuring."""

    INCREMENTAL = "incremental"
    """Fragments arrive per word or phrase. This is what the API actually does."""

    UTTERANCE = "utterance"
    """Fragments arrive one whole utterance at a time."""


@dataclass
class CaptionBuffer:
    """Holds arriving fragments as committed lines plus pending text.

    A settled line does not go straight on screen. It joins a queue that is
    released one line at a time, so a burst of sentences is read out at a
    readable pace rather than scrolling past inside one repaint. Pacing never
    accumulates: once the display has caught up, no more than `max_lines` lines
    are held back, so it trails the audio by at most
    `max_lines * line_hold_seconds`. (Shrinking `max_lines` leaves the queue
    briefly over that bound; the next `tick` drains it.) A burst longer than
    that loses its oldest line from the screen — the transcript still gets every
    one of them, since `drain_new_commits` is independent of what is displayed.

    Time is injected via `now` rather than read from a clock, which keeps the
    tests deterministic.
    """

    max_lines: int = 2
    policy: Policy = Policy.AUTO
    idle_commit_seconds: float = IDLE_COMMIT_SECONDS

    clear_after_seconds: float = CLEAR_AFTER_SECONDS
    """Clear this long after the backlog has drained and the input went quiet.

    0 disables clearing.
    """

    line_hold_seconds: float = LINE_HOLD_SECONDS
    """Minimum time a line stays on screen before the next may replace it."""

    complete_phrases_only: bool = False
    """Show only settled text, never a phrase still being revised.

    The model streams its translation a fragment at a time and rewrites as it
    goes, so drawing every fragment makes the captions visibly churn. With this
    on, unsettled text is withheld until it settles — at a sentence end, at the
    server's `turn_complete`, or after `idle_commit_seconds` of quiet. Costs a
    little latency, and in exchange the captions stop moving under the reader.
    """

    _committed: deque[str] = field(default_factory=lambda: deque(maxlen=2), init=False)
    _queue: deque[str] = field(default_factory=deque, init=False)
    _pending: str = field(default="", init=False)
    _last_text: str | None = field(default=None, init=False)
    _last_feed_at: float = field(default=0.0, init=False)
    _released_at: float = field(default=0.0, init=False)
    _audio_known: bool = field(default=False, init=False)
    _quiet_since: float | None = field(default=None, init=False)
    _continue_next: bool = field(default=False, init=False)
    _resolved: Policy | None = field(default=None, init=False)
    _samples: list[int] = field(default_factory=list, init=False)
    _new_commits: list[str] = field(default_factory=list, init=False)
    _resumed_prefix: str = field(default="", init=False)

    def __post_init__(self) -> None:
        self._committed = deque(maxlen=max(1, self.max_lines))
        if self.policy is not Policy.AUTO:
            self._resolved = self.policy

    # --- queries ----------------------------------------------------------

    @property
    def committed_lines(self) -> list[str]:
        """Settled lines, newest last."""
        return list(self._committed)

    @property
    def pending_text(self) -> str:
        """Text not yet settled. Drawn dimmed."""
        return self._pending

    @property
    def queued_lines(self) -> list[str]:
        """Settled lines still waiting for their turn on screen, oldest first."""
        return list(self._queue)

    @property
    def output_complete(self) -> bool:
        """True when everything received has been shown.

        Nothing is queued and nothing is half-settled, so the display has caught
        up with the audio. The clear countdown only runs once this holds.
        """
        return not self._queue and not self._pending

    @property
    def effective_policy(self) -> Policy:
        """AUTO counts as INCREMENTAL until enough fragments have been seen."""
        return self._resolved or Policy.INCREMENTAL

    @property
    def is_empty(self) -> bool:
        """True when there is nothing to show and nothing waiting to be shown."""
        return not self._committed and not self._pending and not self._queue

    def display_lines(self) -> list[tuple[str, bool]]:
        """(text, settled) pairs, at most max_lines, dropping the oldest.

        Unsettled text is withheld while the queue drains: appending live text
        under lines that are still catching up would show them out of order.
        In complete-phrases mode it is withheld always — that is the whole point
        of the mode, so the reader never watches a phrase rewrite itself.
        """
        lines: list[tuple[str, bool]] = [(t, True) for t in self._committed]
        if self._pending and not self._queue and not self.complete_phrases_only:
            lines.append((self._pending, False))
        return lines[-self.max_lines :]

    # --- updates ----------------------------------------------------------

    def set_max_lines(self, n: int) -> None:
        n = max(1, n)
        if n == self.max_lines:
            return
        self.max_lines = n
        self._committed = deque(self._committed, maxlen=n)

    def set_policy(self, policy: Policy) -> None:
        """Switch the commit policy; AUTO starts detection over."""
        if policy is self.policy:
            return
        self.policy = policy
        self._resolved = None if policy is Policy.AUTO else policy
        self._samples.clear()

    def feed(self, text: str, now: float) -> bool:
        """Take one transcription fragment. True when the display should change."""
        if not text:
            return False
        # Ordering is not guaranteed, so ignore a fragment the API redelivered.
        # Only within a short window: a second later the same text is the
        # speaker repeating a word, and dropping it loses what they said.
        if text == self._last_text and now - self._last_feed_at < DUPLICATE_WINDOW_SECONDS:
            return False
        self._last_text = text
        self._last_feed_at = now

        if self._resolved is None:
            self._samples.append(len(text.strip()))
            if len(self._samples) >= _AUTO_SAMPLE_SIZE:
                ordered = sorted(self._samples)
                median = ordered[len(ordered) // 2]
                self._resolved = (
                    Policy.UTTERANCE if median >= _AUTO_UTTERANCE_CHARS else Policy.INCREMENTAL
                )

        # The first fragment after a reconnect may continue the previous sentence,
        # so pull the last settled line back into pending rather than starting new.
        # It may still be queued rather than on screen, so check the queue first.
        if self._continue_next:
            self._continue_next = False
            if not self._pending:
                resumed = ""
                if self._queue:
                    resumed = self._queue.pop()
                elif self._committed:
                    resumed = self._committed.pop()
                if resumed:
                    self._pending = resumed
                    if self._new_commits and self._new_commits[-1] == resumed:
                        # Not handed to the transcript yet — emit the merged line whole.
                        self._new_commits.pop()
                    else:
                        # Already written out — emit only the continuation.
                        self._resumed_prefix = resumed

        self._pending = _join(self._pending, text)

        if self.effective_policy is Policy.UTTERANCE or self._ends_a_sentence():
            self._commit()
            # Show it straight away when there is room, so a single sentence
            # never waits for the next tick.
            self._drain(now)
        return True

    def _ends_a_sentence(self) -> bool:
        """Whether the pending text has reached a sentence end.

        In complete-phrases mode a full stop also has to have enough text behind
        it: "Dr." and "vs." would otherwise settle a two-word fragment as a
        sentence, and there is no dimmed pending line left on screen to show the
        reader that more is coming.
        """
        settled = self._pending.rstrip()
        if not settled.endswith(tuple(SENTENCE_ENDERS)):
            return False
        return not (self.complete_phrases_only and len(settled) < MIN_SENTENCE_CHARS)

    def note_turn_complete(self, now: float) -> bool:
        """Settle whatever is buffered — the server says the turn ended.

        `server_content.turn_complete` is the only boundary the API reports
        reliably; `Transcription.finished` exists in the SDK but does not arrive
        in practice. Outside complete-phrases mode this changes nothing visible,
        since unsettled text is already on screen.
        """
        if not self._pending:
            return False
        self._commit()
        self._drain(now)
        return True

    def tick(self, now: float, *, speaking: bool | None = None) -> bool:
        """Call periodically. Settles idle text, releases the backlog, then clears.

        `speaking` reports whether the input carried signal since the last call.
        `None` means the level is unknown — with no audio to go on, silence is
        judged from fragment arrival alone, as it was before.
        """
        changed = False
        idle = now - self._last_feed_at

        # Three states, not two: somebody is speaking, the input is quiet, or
        # there is no level to go on at all.
        self._audio_known = speaking is not None
        if speaking is True:
            self._quiet_since = None
        elif speaking is False and self._quiet_since is None:
            self._quiet_since = now

        # Settle a phrase that stopped without sentence-final punctuation.
        if (
            self.effective_policy is Policy.INCREMENTAL
            and self._pending
            and idle >= self.idle_commit_seconds
        ):
            self._commit()
            changed = True

        # A clear window shorter than the commit window would otherwise strand
        # pending text: the display would never report itself complete, so it
        # would never clear and the text would never reach the transcript.
        if self.clear_after_seconds and self._pending and idle >= self.clear_after_seconds:
            self._commit()
            changed = True

        if self._drain(now):
            changed = True

        # Clear once the backlog has drained and the room has gone quiet.
        if self._clear_due(now):
            self.clear()
            changed = True

        return changed

    def drain_new_commits(self) -> list[str]:
        """Take the lines settled since the last call, for the transcript.

        Independent of what is on screen: each line comes out exactly once, and
        clearing the display does not discard lines that were never drained.
        """
        out = self._new_commits
        self._new_commits = []
        return out

    def flush(self) -> bool:
        """Force-settle pending text, so stopping keeps the last sentence."""
        if not self._pending:
            return False
        self._commit()
        return True

    def note_reconnect(self) -> None:
        """Tell the buffer a reconnect happened, so the next fragment continues the line."""
        self._continue_next = True
        self._last_text = None

    def clear(self) -> None:
        """Wipe the display. What was already said still reaches the transcript.

        Half-finished text is settled first rather than dropped: recording is
        deliberately independent of what is on screen, and a sentence the
        speaker finished saying should not vanish because the captions were
        cleared underneath it. `_commit` files it under `_new_commits`, and
        emptying the queue below keeps it off the screen.
        """
        self._commit()
        self._committed.clear()
        self._queue.clear()
        self._pending = ""
        self._last_text = None
        self._continue_next = False
        self._resumed_prefix = ""

    # --- internals --------------------------------------------------------

    def _drain(self, now: float) -> bool:
        """Release as many queued lines as their hold times allow."""
        changed = False
        while self._release_due(now):
            self._committed.append(self._queue.popleft())
            self._released_at = now
            changed = True
        return changed

    def _release_due(self, now: float) -> bool:
        """Whether the next queued line may go on screen yet."""
        if not self._queue:
            return False
        if len(self._committed) < self.max_lines:
            return True  # room to spare — nothing readable is pushed off
        if len(self._queue) > self.max_lines:
            return True  # too far behind to keep pacing; catch up in one go
        return now - self._released_at >= self.line_hold_seconds

    def _clear_due(self, now: float) -> bool:
        if not self.clear_after_seconds or self.is_empty:
            return False
        # The ceiling comes first: captions must not sit on screen for ever
        # because the room is noisy or the backlog stopped draining.
        if now - self._last_feed_at >= MAX_CLEAR_WAIT_SECONDS:
            return True
        if not self.output_complete:
            return False  # still pouring out — the countdown has not started
        if self._audio_known and self._quiet_since is None:
            # Somebody is talking. The model can go quiet mid-sentence, and
            # wiping the captions underneath them is the reported complaint.
            return False
        # The countdown runs from the later of the last fragment, the last line
        # reaching the screen, and the moment the input went quiet.
        quiet_since = max(self._last_feed_at, self._released_at)
        if self._quiet_since is not None:
            quiet_since = max(quiet_since, self._quiet_since)
        return now - quiet_since >= self.clear_after_seconds

    def _commit(self) -> None:
        line = self._pending.strip()
        self._pending = ""
        prefix, self._resumed_prefix = self._resumed_prefix, ""
        if not line:
            return
        self._queue.append(line)
        if prefix and line.startswith(prefix):
            rest = line[len(prefix) :].strip()
            if rest:
                self._new_commits.append(rest)
        else:
            self._new_commits.append(line)


def _is_cjk(ch: str) -> bool:
    """Hangul, kana or han. Never insert a space between these."""
    code = ord(ch)
    return (
        0xAC00 <= code <= 0xD7A3  # Hangul syllables
        or 0x1100 <= code <= 0x11FF  # Hangul jamo
        or 0x3040 <= code <= 0x30FF  # hiragana and katakana
        or 0x4E00 <= code <= 0x9FFF  # han
    )


def _join(left: str, right: str) -> str:
    """Concatenate two fragments, inserting a space only when neither supplies one.

    Measured behaviour (see spikes/RESULTS.md): fragments arrive with their own
    leading space in both English and Korean, so they normally concatenate
    directly. In the rare case a space is missing, CJK text is joined **without**
    one — a fragment that split mid-word (`계획` + `입니다.`) would otherwise be
    broken apart. English needs the space, or two words run together.
    """
    if not left:
        return right.lstrip()
    if left.endswith((" ", "\n")) or right.startswith((" ", "\n")):
        return left + right
    if right[0] in ",.?!;:%)]}'\"" or left.endswith(("(", "[", "{", "-", "'")):
        return left + right
    if _is_cjk(left[-1]) and _is_cjk(right[0]):
        return left + right
    return left + " " + right
