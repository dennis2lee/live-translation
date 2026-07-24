"""설정 스키마와 영속화 — PLAN.md §8.

`QSettings("LiveCap", "LiveCap")` → ~/Library/Preferences/ 에 plist 생성.
API 키는 Settings에 넣지 않는다. 우선순위는
환경변수 → 리포지토리 루트의 `.env` → macOS 키체인 → 환경설정 다이얼로그 입력.
"""

from __future__ import annotations

import os
from dataclasses import asdict, dataclass, fields
from pathlib import Path

from PyQt6.QtCore import QSettings

from . import transcript
from .caption_buffer import Policy
from .util import logging as log

_log = log.get(__name__)

ORG = "LiveCap"
APP = "LiveCap"
KEYRING_SERVICE = "LiveCap"
KEYRING_USER = "gemini-api-key"


@dataclass
class Settings:
    # 색상 (hex)
    bg_color: str = "#000000"
    box_color: str = "#1B4965"
    text_color: str = "#FFFFFF"
    box_opacity: float = 1.0  # 0.6 ~ 1.0

    # 레이아웃
    corner_radius: int = 24  # 0 ~ 48
    box_max_width_pct: int = 90  # 60 ~ 100 (좌우 여백 5%씩)
    bottom_margin_pct: int = 8  # 2 ~ 20
    font_size: int = 44  # 24 ~ 96
    max_lines: int = 2  # 1 ~ 4

    # 캡션 박스를 항상 같은 크기·위치로 띄운다 (텍스트가 없어도 유지).
    # 끄면 PLAN.md §6.2의 원래 동작 — 내용에 맞춰 크기가 변하고, 비면 사라진다.
    fixed_box: bool = True

    # 말이 멎고 이만큼 지나면 캡션을 비운다. 0이면 계속 남겨둔다.
    clear_after_seconds: float = 2.0  # 0 ~ 30

    # 번역 기록 저장 (파일명은 통역 시작 날짜·시간)
    save_transcript: bool = True
    transcript_dir: str = transcript.DEFAULT_DIR

    # 동작
    audio_device_name: str = ""  # 빈 문자열이면 시스템 기본 입력
    show_source_text: bool = False
    fullscreen: bool = False
    silence_gate: bool = False

    # API
    target_language_code: str = "en"
    echo_target_language: bool = False

    # 캡션 커밋 정책. Phase 0 측정 결과(spikes/RESULTS.md, 2026-07-24)에 따라
    # 정책 A로 고정한다 — 조각이 중간값 14자로 약 1초마다 점진 도착한다.
    caption_policy: str = Policy.INCREMENTAL.value

    def clamped(self) -> Settings:
        """범위를 벗어난 값을 명세 범위로 되돌린 사본."""
        return Settings(
            bg_color=_hex(self.bg_color, "#000000"),
            box_color=_hex(self.box_color, "#1B4965"),
            text_color=_hex(self.text_color, "#FFFFFF"),
            box_opacity=_clamp(self.box_opacity, 0.6, 1.0),
            corner_radius=int(_clamp(self.corner_radius, 0, 48)),
            box_max_width_pct=int(_clamp(self.box_max_width_pct, 60, 100)),
            bottom_margin_pct=int(_clamp(self.bottom_margin_pct, 2, 20)),
            font_size=int(_clamp(self.font_size, 24, 96)),
            max_lines=int(_clamp(self.max_lines, 1, 4)),
            fixed_box=bool(self.fixed_box),
            clear_after_seconds=_clamp(self.clear_after_seconds, 0.0, 30.0),
            save_transcript=bool(self.save_transcript),
            transcript_dir=self.transcript_dir or transcript.DEFAULT_DIR,
            audio_device_name=self.audio_device_name,
            show_source_text=bool(self.show_source_text),
            fullscreen=bool(self.fullscreen),
            silence_gate=bool(self.silence_gate),
            target_language_code=self.target_language_code or "en",
            echo_target_language=bool(self.echo_target_language),
            caption_policy=(
                self.caption_policy
                if self.caption_policy in {p.value for p in Policy}
                else Policy.INCREMENTAL.value
            ),
        )

    @property
    def policy(self) -> Policy:
        return Policy(self.caption_policy)


def _clamp(v: float, lo: float, hi: float) -> float:
    try:
        return max(lo, min(hi, float(v)))
    except (TypeError, ValueError):
        return lo


def _hex(v: str, fallback: str) -> str:
    v = (v or "").strip()
    if len(v) == 7 and v.startswith("#"):
        try:
            int(v[1:], 16)
            return v.upper()
        except ValueError:
            pass
    return fallback


# --- 영속화 ---------------------------------------------------------------


def _qsettings() -> QSettings:
    return QSettings(ORG, APP)


def load() -> Settings:
    """QSettings에서 읽는다. 없거나 깨진 값은 기본값으로 되돌린다."""
    qs = _qsettings()
    defaults = Settings()
    kwargs: dict[str, object] = {}
    for f in fields(Settings):
        raw = qs.value(f.name, None)
        if raw is None:
            kwargs[f.name] = getattr(defaults, f.name)
            continue
        try:
            if f.type is bool or f.type == "bool":
                kwargs[f.name] = str(raw).lower() in ("true", "1", "yes")
            elif f.type is int or f.type == "int":
                kwargs[f.name] = int(raw)
            elif f.type is float or f.type == "float":
                kwargs[f.name] = float(raw)
            else:
                kwargs[f.name] = str(raw)
        except (TypeError, ValueError):
            kwargs[f.name] = getattr(defaults, f.name)
    return Settings(**kwargs).clamped()  # type: ignore[arg-type]


def save(settings: Settings) -> None:
    qs = _qsettings()
    for k, v in asdict(settings.clamped()).items():
        qs.setValue(k, v)
    qs.sync()


def reset() -> Settings:
    """기본값으로 되돌리기."""
    qs = _qsettings()
    qs.clear()
    qs.sync()
    return Settings()


# --- API 키 ---------------------------------------------------------------


API_KEY_VARS = ("GEMINI_API_KEY", "GOOGLE_API_KEY")
_PLACEHOLDER_MARKERS = ("여기에", "your", "xxx", "<", "붙여넣")


def dotenv_path() -> Path:
    """`.env`의 위치. LIVECAP_ENV_FILE로 덮어쓸 수 있다."""
    override = os.environ.get("LIVECAP_ENV_FILE")
    if override:
        return Path(override).expanduser()
    # src/livecap/config.py → 리포지토리 루트
    root = Path(__file__).resolve().parents[2]
    local = Path.cwd() / ".env"
    return local if local.is_file() else root / ".env"


def read_dotenv(path: Path | None = None) -> dict[str, str]:
    """아주 단순한 KEY=VALUE 파서. 주석·빈 줄·따옴표·`export ` 접두를 처리한다."""
    path = path or dotenv_path()
    values: dict[str, str] = {}
    try:
        text = path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return values
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        line = line.removeprefix("export ").lstrip()
        key, _, value = line.partition("=")
        key = key.strip()
        if not key:
            continue
        value = value.strip().split(" #", 1)[0].strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
        values[key] = value
    return values


def apply_dotenv_to_environ() -> list[str]:
    """`.env`의 값을 os.environ에 채운다. 이미 있는 환경변수는 덮어쓰지 않는다.

    앱 시작 시 한 번 호출한다. 돌려주는 것은 채워 넣은 키 이름 목록이며,
    값은 로그에 남기지 않는다.
    """
    applied: list[str] = []
    for key, value in read_dotenv().items():
        if key not in os.environ and value:
            os.environ[key] = value
            applied.append(key)
    return applied


def _is_placeholder(value: str) -> bool:
    low = value.lower()
    return any(marker in low for marker in _PLACEHOLDER_MARKERS)


def load_api_key() -> str | None:
    """환경변수 → .env → 키체인 순으로 조회.

    소스나 QSettings plist에는 절대 저장하지 않는다.
    .env에 예시 문구가 그대로 남아 있으면 키가 없는 것으로 취급한다.
    """
    for env in API_KEY_VARS:
        v = os.environ.get(env)
        if v and v.strip() and not _is_placeholder(v.strip()):
            return v.strip()

    dotenv = read_dotenv()
    for env in API_KEY_VARS:
        v = dotenv.get(env, "")
        if v.strip() and not _is_placeholder(v.strip()):
            return v.strip()

    try:
        import keyring

        v = keyring.get_password(KEYRING_SERVICE, KEYRING_USER)
        return v.strip() if v else None
    except Exception as exc:
        _log.warning("keychain unavailable: %s", exc)
        return None


def save_api_key(key: str) -> bool:
    """macOS 키체인에 저장."""
    try:
        import keyring

        keyring.set_password(KEYRING_SERVICE, KEYRING_USER, key.strip())
        return True
    except Exception as exc:
        _log.error("could not write key to keychain: %s", exc)
        return False


def clear_api_key() -> None:
    try:
        import keyring

        keyring.delete_password(KEYRING_SERVICE, KEYRING_USER)
    except Exception:
        pass
