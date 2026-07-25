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
CLEAR_AFTER_SECONDS = 3.0
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

    Time is injected via `now` rather than read from a clock, which keeps the
    tests deterministic.
    """

    max_lines: int = 2
    policy: Policy = Policy.AUTO
    idle_commit_seconds: float = IDLE_COMMIT_SECONDS

    clear_after_seconds: float = CLEAR_AFTER_SECONDS
    """Clear captions this long after the last fragment. 0 disables clearing."""

    _committed: deque[str] = field(default_factory=lambda: deque(maxlen=2), init=False)
    _pending: str = field(default="", init=False)
    _last_text: str | None = field(default=None, init=False)
    _last_feed_at: float = field(default=0.0, init=False)
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
    def effective_policy(self) -> Policy:
        """AUTO counts as INCREMENTAL until enough fragments have been seen."""
        return self._resolved or Policy.INCREMENTAL

    @property
    def is_empty(self) -> bool:
        """True when there is nothing to show."""
        return not self._committed and not self._pending

    def display_lines(self) -> list[tuple[str, bool]]:
        """(text, settled) pairs, at most max_lines, dropping the oldest."""
        lines: list[tuple[str, bool]] = [(t, True) for t in self._committed]
        if self._pending:
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
        # Ordering is not guaranteed, so ignore an immediately repeated fragment.
        if text == self._last_text:
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
        # so pull the last committed line back into pending rather than starting new.
        if self._continue_next:
            self._continue_next = False
            if not self._pending and self._committed:
                resumed = self._committed.pop()
                self._pending = resumed
                if self._new_commits and self._new_commits[-1] == resumed:
                    # Not handed to the transcript yet — emit the merged line whole.
                    self._new_commits.pop()
                else:
                    # Already written out — emit only the continuation, not a duplicate.
                    self._resumed_prefix = resumed

        self._pending = _join(self._pending, text)

        if self.effective_policy is Policy.UTTERANCE or self._pending.rstrip().endswith(
            tuple(SENTENCE_ENDERS)
        ):
            self._commit()
        return True

    def tick(self, now: float) -> bool:
        """Call periodically. Settles idle text, then clears after longer silence."""
        changed = False
        idle = now - self._last_feed_at

        # Settle a phrase that stopped without sentence-final punctuation.
        if (
            self.effective_policy is Policy.INCREMENTAL
            and self._pending
            and idle >= self.idle_commit_seconds
        ):
            self._commit()
            changed = True

        # Clear once speech has stopped. Applies to both policies.
        if self.clear_after_seconds and not self.is_empty and idle >= self.clear_after_seconds:
            # Settle first, or pending text vanishes from the transcript when
            # clear_after_seconds is set shorter than idle_commit_seconds.
            self._commit()
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
        self._committed.clear()
        self._pending = ""
        self._last_text = None
        self._continue_next = False
        self._resumed_prefix = ""

    # --- internals --------------------------------------------------------

    def _commit(self) -> None:
        line = self._pending.strip()
        self._pending = ""
        prefix, self._resumed_prefix = self._resumed_prefix, ""
        if not line:
            return
        self._committed.append(line)
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
