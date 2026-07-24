"""설정 스키마와 영속화 — PLAN.md §8.

`QSettings("LiveCap", "LiveCap")` → ~/Library/Preferences/ 에 plist 생성.
API 키는 Settings에 넣지 않는다. 우선순위는
`GEMINI_API_KEY` 환경변수 → macOS 키체인 → 환경설정 다이얼로그 입력.
"""

from __future__ import annotations

import os
from dataclasses import asdict, dataclass, fields

from PyQt6.QtCore import QSettings

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
    box_max_width_pct: int = 80  # 60 ~ 95
    bottom_margin_pct: int = 8  # 2 ~ 20
    font_size: int = 44  # 24 ~ 96
    max_lines: int = 2  # 1 ~ 4

    # 동작
    audio_device_name: str = ""  # 빈 문자열이면 시스템 기본 입력
    show_source_text: bool = False
    fullscreen: bool = False
    silence_gate: bool = False

    # API
    target_language_code: str = "en"
    echo_target_language: bool = False

    # 캡션 커밋 정책 — Phase 0 결과(spikes/RESULTS.md)에 따라 고정할 수 있다.
    caption_policy: str = Policy.AUTO.value

    def clamped(self) -> Settings:
        """범위를 벗어난 값을 명세 범위로 되돌린 사본."""
        return Settings(
            bg_color=_hex(self.bg_color, "#000000"),
            box_color=_hex(self.box_color, "#1B4965"),
            text_color=_hex(self.text_color, "#FFFFFF"),
            box_opacity=_clamp(self.box_opacity, 0.6, 1.0),
            corner_radius=int(_clamp(self.corner_radius, 0, 48)),
            box_max_width_pct=int(_clamp(self.box_max_width_pct, 60, 95)),
            bottom_margin_pct=int(_clamp(self.bottom_margin_pct, 2, 20)),
            font_size=int(_clamp(self.font_size, 24, 96)),
            max_lines=int(_clamp(self.max_lines, 1, 4)),
            audio_device_name=self.audio_device_name,
            show_source_text=bool(self.show_source_text),
            fullscreen=bool(self.fullscreen),
            silence_gate=bool(self.silence_gate),
            target_language_code=self.target_language_code or "en",
            echo_target_language=bool(self.echo_target_language),
            caption_policy=(
                self.caption_policy
                if self.caption_policy in {p.value for p in Policy}
                else Policy.AUTO.value
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


def load_api_key() -> str | None:
    """환경변수 → 키체인 순으로 조회. 소스나 plist에는 절대 저장하지 않는다."""
    for env in ("GEMINI_API_KEY", "GOOGLE_API_KEY"):
        v = os.environ.get(env)
        if v:
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
