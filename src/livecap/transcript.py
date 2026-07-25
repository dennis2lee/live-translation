"""Writes the translation to a Markdown file.

The filename is the time translation started: `2026-07-24_143052.md`.

This module must stay free of Qt and network dependencies, and covered by unit
tests. A failure to record must never interrupt translation, so I/O errors are
swallowed and only logged.
"""

from __future__ import annotations

import contextlib
from datetime import datetime
from pathlib import Path
from typing import TextIO

from .util import logging as log

_log = log.get(__name__)

DEFAULT_DIR = "~/Documents/LiveCap"
FILENAME_FORMAT = "%Y-%m-%d_%H%M%S"


def resolve_dir(raw: str) -> Path:
    return Path(raw or DEFAULT_DIR).expanduser()


def _format_elapsed(seconds: float) -> str:
    seconds = max(0, int(seconds))
    h, rem = divmod(seconds, 3600)
    m, s = divmod(rem, 60)
    return f"{h:02d}:{m:02d}:{s:02d}" if h else f"{m:02d}:{s:02d}"


class TranscriptWriter:
    """Appends settled translation lines as Markdown.

    Every line is flushed, so a crash cannot lose what was already written.
    """

    def __init__(
        self,
        directory: str | Path = DEFAULT_DIR,
        *,
        target_language: str = "en",
        model: str = "",
    ) -> None:
        self._dir = resolve_dir(str(directory))
        self._target_language = target_language
        self._model = model
        self._file: TextIO | None = None
        self._started_at: datetime | None = None
        self._lines_written = 0
        self.path: Path | None = None

    @property
    def is_open(self) -> bool:
        return self._file is not None

    @property
    def lines_written(self) -> int:
        return self._lines_written

    def open(self, started_at: datetime | None = None) -> Path | None:
        """Create the file. Returns None on failure; translation continues regardless."""
        if self._file is not None:
            return self.path
        started_at = started_at or datetime.now()
        self._started_at = started_at
        try:
            self._dir.mkdir(parents=True, exist_ok=True)
            self.path = self._unique_path(started_at)
            self._file = self.path.open("w", encoding="utf-8")
        except OSError as exc:
            _log.error("could not open transcript file: %s", exc)
            self._file = None
            self.path = None
            return None

        self._write(
            f"# LiveCap transcript\n\n"
            f"- **Started** {started_at:%Y-%m-%d %H:%M:%S}\n"
            f"- **Target language** {self._target_language}\n"
            + (f"- **Model** {self._model}\n" if self._model else "")
            + "\n---\n\n"
        )
        _log.info("recording transcript to %s", self.path)
        return self.path

    def _unique_path(self, started_at: datetime) -> Path:
        base = started_at.strftime(FILENAME_FORMAT)
        candidate = self._dir / f"{base}.md"
        n = 2
        while candidate.exists():  # two sessions started in the same second
            candidate = self._dir / f"{base}-{n}.md"
            n += 1
        return candidate

    def append_line(self, text: str, elapsed_seconds: float) -> None:
        """Record one settled translation line."""
        text = text.strip()
        if not text or self._file is None:
            return
        self._write(f"`{_format_elapsed(elapsed_seconds)}` {text}\n\n")
        self._lines_written += 1

    def note_reconnect(self, elapsed_seconds: float, gap_ms: float) -> None:
        """Mark where a reconnect happened, for locating gaps afterwards."""
        if self._file is None:
            return
        self._write(
            f"> _reconnected at {_format_elapsed(elapsed_seconds)}, "
            f"gap {gap_ms:.0f} ms_\n\n"
        )

    def note_direction(self, label: str, elapsed_seconds: float) -> None:
        """Mark where the translation direction changed."""
        if self._file is None:
            return
        self._write(f"> _direction: {label} at {_format_elapsed(elapsed_seconds)}_\n\n")

    def close(self, *, elapsed_seconds: float = 0.0, reconnects: int = 0) -> Path | None:
        if self._file is None:
            return None
        ended = datetime.now()
        self._write(
            "---\n\n"
            f"- **Ended** {ended:%Y-%m-%d %H:%M:%S}\n"
            f"- **Duration** {_format_elapsed(elapsed_seconds)}\n"
            f"- **Lines** {self._lines_written}\n"
            f"- **Reconnects** {reconnects}\n"
        )
        # A failed footer write already closed and cleared the file.
        if self._file is not None:
            try:
                self._file.close()
            except OSError as exc:
                _log.warning("error closing transcript: %s", exc)
            self._file = None
        path = self.path
        if path is not None and self._lines_written == 0:
            # A transcript that captured nothing is not worth keeping.
            try:
                path.unlink()
                _log.info("removed empty transcript %s", path)
                self.path = None
                return None
            except OSError:
                pass
        _log.info("transcript saved: %s (%d lines)", path, self._lines_written)
        return path

    def _write(self, text: str) -> None:
        if self._file is None:
            return
        try:
            self._file.write(text)
            self._file.flush()  # survive a crash
        except OSError as exc:
            _log.error("transcript write failed, recording stopped: %s", exc)
            with contextlib.suppress(OSError):
                self._file.close()
            self._file = None
