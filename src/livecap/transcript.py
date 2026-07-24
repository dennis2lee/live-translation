"""번역 기록을 Markdown 파일로 저장한다.

파일명은 통역이 시작된 날짜·시간을 따른다: `2026-07-24_143052.md`.

Qt·네트워크 의존성이 없어야 하며 단위 테스트로 덮는다.
기록 실패가 통역을 중단시키면 안 되므로, 입출력 오류는 삼키고 로그만 남긴다.
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
    """확정된 번역 줄을 Markdown으로 이어 쓴다.

    각 줄을 쓸 때마다 flush 하므로 앱이 갑자기 죽어도 그때까지 내용이 남는다.
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
        """기록 파일을 만든다. 실패하면 None (통역은 계속 진행한다)."""
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
            f"# LiveCap 번역 기록\n\n"
            f"- **시작** {started_at:%Y-%m-%d %H:%M:%S}\n"
            f"- **대상 언어** {self._target_language}\n"
            + (f"- **모델** {self._model}\n" if self._model else "")
            + "\n---\n\n"
        )
        _log.info("recording transcript to %s", self.path)
        return self.path

    def _unique_path(self, started_at: datetime) -> Path:
        base = started_at.strftime(FILENAME_FORMAT)
        candidate = self._dir / f"{base}.md"
        n = 2
        while candidate.exists():  # 같은 초에 두 번 시작한 경우
            candidate = self._dir / f"{base}-{n}.md"
            n += 1
        return candidate

    def append_line(self, text: str, elapsed_seconds: float) -> None:
        """확정된 번역 한 줄을 추가한다."""
        text = text.strip()
        if not text or self._file is None:
            return
        self._write(f"`{_format_elapsed(elapsed_seconds)}` {text}\n\n")
        self._lines_written += 1

    def note_reconnect(self, elapsed_seconds: float, gap_ms: float) -> None:
        """재연결 지점을 기록에 남긴다 — 나중에 유실 구간을 찾을 때 쓴다."""
        if self._file is None:
            return
        self._write(
            f"> _재연결 — {_format_elapsed(elapsed_seconds)} 지점, "
            f"공백 {gap_ms:.0f}ms_\n\n"
        )

    def close(self, *, elapsed_seconds: float = 0.0, reconnects: int = 0) -> Path | None:
        if self._file is None:
            return None
        ended = datetime.now()
        self._write(
            "---\n\n"
            f"- **종료** {ended:%Y-%m-%d %H:%M:%S}\n"
            f"- **길이** {_format_elapsed(elapsed_seconds)}\n"
            f"- **줄 수** {self._lines_written}\n"
            f"- **재연결** {reconnects}회\n"
        )
        try:
            self._file.close()
        except OSError as exc:
            _log.warning("error closing transcript: %s", exc)
        self._file = None
        path = self.path
        if path is not None and self._lines_written == 0:
            # 한 줄도 못 받은 기록은 남겨봐야 쓸모가 없다.
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
            self._file.flush()  # 갑자기 죽어도 여기까지는 남는다
        except OSError as exc:
            _log.error("transcript write failed, recording stopped: %s", exc)
            with contextlib.suppress(OSError):
                self._file.close()
            self._file = None
