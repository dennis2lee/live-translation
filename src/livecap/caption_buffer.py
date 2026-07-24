"""전사 조각 → 화면 줄 조립 — PLAN.md §7.

`output_transcription.text`는 partial/final 구분이 없고 순서 보장도 없다.
조각을 모아 표시 줄로 만드는 책임은 전적으로 클라이언트에 있다.

이 모듈은 Qt·네트워크 의존성이 없어야 한다 (단위 테스트로 덮는다).
"""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass, field
from enum import StrEnum

SENTENCE_ENDERS = ".?!"
IDLE_COMMIT_SECONDS = 1.2
CLEAR_AFTER_SECONDS = 2.0
_AUTO_SAMPLE_SIZE = 8
_AUTO_UTTERANCE_CHARS = 25


class Policy(StrEnum):
    """PLAN.md §7의 커밋 정책."""

    AUTO = "auto"
    """첫 조각들을 보고 A/B를 자동 판별. Phase 0 결과가 없을 때의 기본값."""

    INCREMENTAL = "incremental"
    """정책 A — 조각이 글자/단어 단위로 자주 도착."""

    UTTERANCE = "utterance"
    """정책 B — 발화 단위로 뭉텅이로 도착."""


@dataclass
class CaptionBuffer:
    """도착한 전사 조각을 확정 줄과 미확정 텍스트로 나눠 보관한다.

    시간은 인자로 주입받는다(`now`). 내부에서 시계를 읽지 않으므로 테스트가 결정적이다.
    """

    max_lines: int = 2
    policy: Policy = Policy.AUTO
    idle_commit_seconds: float = IDLE_COMMIT_SECONDS

    clear_after_seconds: float = CLEAR_AFTER_SECONDS
    """마지막 조각 이후 이만큼 지나면 캡션을 비운다. 0이면 비활성."""

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

    # --- 조회 -------------------------------------------------------------

    @property
    def committed_lines(self) -> list[str]:
        """확정된 줄. 최신이 마지막."""
        return list(self._committed)

    @property
    def pending_text(self) -> str:
        """아직 확정되지 않은 텍스트 (흐린 색으로 표시)."""
        return self._pending

    @property
    def effective_policy(self) -> Policy:
        """AUTO가 아직 판별 전이면 INCREMENTAL로 간주한다."""
        return self._resolved or Policy.INCREMENTAL

    @property
    def is_empty(self) -> bool:
        """표시할 것이 없으면 True — 이때 캡션 박스 자체를 숨긴다."""
        return not self._committed and not self._pending

    def display_lines(self) -> list[tuple[str, bool]]:
        """(텍스트, 확정여부) 목록. 최대 max_lines개, 오래된 앞부분을 버린다."""
        lines: list[tuple[str, bool]] = [(t, True) for t in self._committed]
        if self._pending:
            lines.append((self._pending, False))
        return lines[-self.max_lines :]

    # --- 갱신 -------------------------------------------------------------

    def set_max_lines(self, n: int) -> None:
        n = max(1, n)
        if n == self.max_lines:
            return
        self.max_lines = n
        self._committed = deque(self._committed, maxlen=n)

    def feed(self, text: str, now: float) -> bool:
        """전사 조각을 넣는다. 화면이 바뀌어야 하면 True."""
        if not text:
            return False
        # 순서 미보장 대비 — 같은 텍스트가 연속 중복 도착하면 무시.
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

        # 재연결 직후 첫 조각은 이전 문장의 이어짐일 수 있다. 줄을 새로 시작하지 않고
        # 마지막 확정 줄을 미확정 상태로 되돌려 이어 붙인다.
        if self._continue_next:
            self._continue_next = False
            if not self._pending and self._committed:
                resumed = self._committed.pop()
                self._pending = resumed
                if self._new_commits and self._new_commits[-1] == resumed:
                    # 아직 기록으로 넘어가지 않았다 — 합본을 통째로 내보내면 된다.
                    self._new_commits.pop()
                else:
                    # 이미 기록에 쓰였다 — 이어진 부분만 내보내 중복을 막는다.
                    self._resumed_prefix = resumed

        self._pending = _join(self._pending, text)

        if self.effective_policy is Policy.UTTERANCE or self._pending.rstrip().endswith(
            tuple(SENTENCE_ENDERS)
        ):
            self._commit()
        return True

    def tick(self, now: float) -> bool:
        """주기 호출. 유휴 시간에 따라 미확정분을 확정하고, 더 지나면 비운다."""
        changed = False
        idle = now - self._last_feed_at

        # 정책 A — 종결 부호 없이 끊긴 미확정분을 확정한다.
        if (
            self.effective_policy is Policy.INCREMENTAL
            and self._pending
            and idle >= self.idle_commit_seconds
        ):
            self._commit()
            changed = True

        # 말이 멎으면 캡션을 비운다 (정책 A/B 공통).
        if self.clear_after_seconds and not self.is_empty and idle >= self.clear_after_seconds:
            # 지우기 전에 미확정분을 확정한다 — 안 그러면 기록에서 사라진다.
            # (clear_after_seconds를 idle_commit_seconds보다 짧게 잡은 경우)
            self._commit()
            self.clear()
            changed = True

        return changed

    def drain_new_commits(self) -> list[str]:
        """마지막 호출 이후 확정된 줄을 꺼내 간다 (기록 저장용).

        화면 표시와 무관하게 한 번씩만 나온다. `clear()`로 화면을 비워도
        아직 꺼내 가지 않은 줄은 사라지지 않는다.
        """
        out = self._new_commits
        self._new_commits = []
        return out

    def flush(self) -> bool:
        """미확정분을 강제로 확정한다 (통역 중지 시 마지막 문장을 남기기 위해)."""
        if not self._pending:
            return False
        self._commit()
        return True

    def note_reconnect(self) -> None:
        """재연결이 일어났음을 알린다. 다음 조각을 이전 줄에 이어 붙인다."""
        self._continue_next = True
        self._last_text = None

    def clear(self) -> None:
        """캡션 지우기 (⌘K)."""
        self._committed.clear()
        self._pending = ""
        self._last_text = None
        self._continue_next = False
        self._resumed_prefix = ""

    # --- 내부 -------------------------------------------------------------

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


def _join(left: str, right: str) -> str:
    """조각을 이어 붙인다. 양쪽이 공백을 안 주면 한 칸 넣는다."""
    if not left:
        return right.lstrip()
    if left.endswith((" ", "\n")) or right.startswith((" ", "\n")):
        return left + right
    if right[0] in ",.?!;:%)]}'\"" or left.endswith(("(", "[", "{", "-", "'")):
        return left + right
    return left + " " + right
