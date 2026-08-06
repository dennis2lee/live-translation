"""Settings schema and persistence.

`QSettings("LiveCap", "LiveCap")` writes a plist under ~/Library/Preferences/.

The API key is deliberately not part of Settings. It is looked up in order:
environment variable, `.env` at the repository root, macOS Keychain, then
whatever is typed into the preferences dialog.
"""

from __future__ import annotations

import os
import sys
from dataclasses import asdict, dataclass, fields
from enum import StrEnum
from pathlib import Path

from PyQt6.QtCore import QSettings

from . import credentials, platform_support, transcript
from .caption_buffer import Policy
from .util import logging as log

_log = log.get(__name__)

ORG = "LiveCap"
APP = "LiveCap"


class Direction(StrEnum):
    """Translation direction preset.

    The Live API's `TranslationConfig` has no source-language field — the model
    detects it. A "direction" therefore reduces to a target language.
    """

    KO_TO_EN = "ko2en"
    EN_TO_KO = "en2ko"


class BoxPosition(StrEnum):
    """Where the caption box sits vertically."""

    TOP = "top"
    CENTER = "center"
    BOTTOM = "bottom"


BOX_POSITION_MENU_TEXT: dict[BoxPosition, str] = {
    BoxPosition.TOP: "Top",
    BoxPosition.CENTER: "Middle",
    BoxPosition.BOTTOM: "Bottom",
}


DIRECTION_TARGET: dict[Direction, str] = {
    Direction.KO_TO_EN: "en",
    Direction.EN_TO_KO: "ko",
}

DIRECTION_LABEL: dict[Direction, str] = {
    Direction.KO_TO_EN: "KO → EN",
    Direction.EN_TO_KO: "EN → KO",
}

DIRECTION_MENU_TEXT: dict[Direction, str] = {
    Direction.KO_TO_EN: "Korean → English",
    Direction.EN_TO_KO: "English → Korean",
}


def direction_for_target(target_language_code: str) -> Direction | None:
    """The preset for a target language, or None if it is not one of them."""
    code = (target_language_code or "").strip().lower()
    for direction, target in DIRECTION_TARGET.items():
        if target == code:
            return direction
    return None


def direction_label(target_language_code: str) -> str:
    """Label for the overlay. Falls back to just the target for other languages."""
    direction = direction_for_target(target_language_code)
    if direction is not None:
        return DIRECTION_LABEL[direction]
    return f"→ {(target_language_code or '?').strip().upper()}"


KEYRING_SERVICE = "LiveCap"
KEYRING_USER = "gemini-api-key"


@dataclass
class Settings:
    # colours (hex)
    bg_color: str = "#000000"
    box_color: str = "#1B4965"
    text_color: str = "#FFFFFF"
    box_opacity: float = 1.0  # 0.6 ~ 1.0

    # layout
    corner_radius: int = 24  # 0 ~ 48
    box_max_width_pct: int = 90  # 60-100, leaving a 5% margin each side

    # Distance from the edge the box is anchored to, as a percentage of the
    # window height. Named for the original bottom-only layout; it is the top
    # margin when box_position is "top", and ignored when centred.
    bottom_margin_pct: int = 8  # 2 ~ 20

    # Where the box sits vertically. Top is useful when the lower part of the
    # screen is covered by something else.
    box_position: str = BoxPosition.BOTTOM.value

    font_size: int = 44  # 24 ~ 96
    max_lines: int = 2  # 1 ~ 4

    # Keep the caption box at a fixed size and position, even when empty.
    # Turning this off makes it hug its content and disappear when empty.
    fixed_box: bool = True

    # Clear captions this long after the caption backlog has drained and the
    # input has gone quiet. 0 keeps them on screen.
    clear_after_seconds: float = 2.0  # 0 ~ 30

    # Minimum time a settled line holds the screen before the next replaces it.
    # Stops a burst of sentences scrolling past inside a single repaint.
    line_hold_seconds: float = 0.7  # 0 ~ 3

    # Show only settled text, never a phrase the model is still revising.
    # Trades a little latency for captions that stop moving under the reader.
    complete_phrases_only: bool = False

    # Transcript recording. The filename is the session start timestamp.
    save_transcript: bool = True
    transcript_dir: str = transcript.DEFAULT_DIR

    # Briefly show `KO → EN` below the caption box on start and on direction change.
    show_direction_hint: bool = True

    # behaviour
    audio_device_name: str = ""  # empty means the system default input
    # Used to come from LIVECAP_DEBUG in .env; now a preference.
    debug_logging: bool = False
    show_source_text: bool = False
    fullscreen: bool = False
    silence_gate: bool = False

    # Terms to bias speech recognition towards, one per line. Two lists, because
    # they are genuinely different: what the model should hear in the source
    # language, and how names should come out in the captions.
    vocabulary_source: str = ""
    vocabulary_captions: str = ""

    # API
    target_language_code: str = "en"
    echo_target_language: bool = False

    # Caption commit policy, fixed to A by measurement (spikes/RESULTS.md):
    # fragments arrive incrementally, median 14 chars, about once per second.
    caption_policy: str = Policy.INCREMENTAL.value

    def clamped(self) -> Settings:
        """A copy with every value clamped back into its documented range."""
        return Settings(
            bg_color=_hex(self.bg_color, "#000000"),
            box_color=_hex(self.box_color, "#1B4965"),
            text_color=_hex(self.text_color, "#FFFFFF"),
            box_opacity=_clamp(self.box_opacity, 0.6, 1.0),
            corner_radius=int(_clamp(self.corner_radius, 0, 48)),
            box_max_width_pct=int(_clamp(self.box_max_width_pct, 60, 100)),
            bottom_margin_pct=int(_clamp(self.bottom_margin_pct, 2, 20)),
            box_position=(
                self.box_position
                if self.box_position in {p.value for p in BoxPosition}
                else BoxPosition.BOTTOM.value
            ),
            font_size=int(_clamp(self.font_size, 24, 96)),
            max_lines=int(_clamp(self.max_lines, 1, 4)),
            fixed_box=bool(self.fixed_box),
            clear_after_seconds=_clamp(self.clear_after_seconds, 0.0, 30.0),
            line_hold_seconds=_clamp(self.line_hold_seconds, 0.0, 3.0),
            complete_phrases_only=bool(self.complete_phrases_only),
            save_transcript=bool(self.save_transcript),
            show_direction_hint=bool(self.show_direction_hint),
            transcript_dir=self.transcript_dir or transcript.DEFAULT_DIR,
            audio_device_name=self.audio_device_name,
            debug_logging=bool(self.debug_logging),
            show_source_text=bool(self.show_source_text),
            fullscreen=bool(self.fullscreen),
            silence_gate=bool(self.silence_gate),
            vocabulary_source=_normalise_terms(self.vocabulary_source),
            vocabulary_captions=_normalise_terms(self.vocabulary_captions),
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

    @property
    def position(self) -> BoxPosition:
        return BoxPosition(self.box_position)

    @property
    def source_terms(self) -> list[str]:
        """Vocabulary hints for the language being spoken."""
        return parse_terms(self.vocabulary_source)

    @property
    def caption_terms(self) -> list[str]:
        """Vocabulary hints for the language on screen."""
        return parse_terms(self.vocabulary_captions)


# Our own ceiling, not the API's: the terms ride in the setup message of every
# session, including each reconnect, so an unbounded list would slow every swap.
MAX_VOCABULARY_TERMS = 200


def parse_terms(text: str) -> list[str]:
    """One term per line. Blank lines and surrounding spaces are dropped.

    Split on newlines only, never commas — an adaptation phrase can be a whole
    clause, and clauses contain commas.
    """
    seen: dict[str, None] = {}
    for line in (text or "").splitlines():
        term = line.strip()
        if term:
            seen.setdefault(term, None)
    return list(seen)[:MAX_VOCABULARY_TERMS]


def _normalise_terms(text: str) -> str:
    """Store exactly what will be sent: stripped, de-duplicated, capped."""
    return "\n".join(parse_terms(text))


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


# --- persistence ----------------------------------------------------------


def _qsettings() -> QSettings:
    return QSettings(ORG, APP)


def load() -> Settings:
    """Read from QSettings. Missing or malformed values fall back to defaults."""
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
    """Reset everything to defaults."""
    qs = _qsettings()
    qs.clear()
    qs.sync()
    return Settings()


# --- API key --------------------------------------------------------------


API_KEY_VARS = ("GEMINI_API_KEY", "GOOGLE_API_KEY")
_PLACEHOLDER_MARKERS = ("your", "xxx", "<", "here")


# Kept as a module attribute so callers and tests can refer to it directly.
APP_SUPPORT_DIR = platform_support.app_support_dir()


def dotenv_candidates() -> list[Path]:
    """Every place `.env` may live, in order of preference.

    A packaged .app cannot use the repository root — that path lands inside the
    bundle — so it falls back to Application Support and to the folder holding
    the .app itself.
    """
    override = os.environ.get("LIVECAP_ENV_FILE")
    if override:
        return [Path(override).expanduser()]

    paths = [Path.cwd() / ".env"]
    if getattr(sys, "frozen", False):
        # .../LiveCap.app/Contents/MacOS/LiveCap -> the folder holding the .app
        paths += [
            platform_support.app_support_dir() / ".env",
            Path(sys.executable).resolve().parents[3] / ".env",
        ]
    else:
        # src/livecap/config.py -> repository root
        paths.append(Path(__file__).resolve().parents[2] / ".env")
    return paths


def dotenv_path() -> Path:
    """The `.env` that will be read. The first existing candidate wins."""
    candidates = dotenv_candidates()
    for path in candidates:
        if path.is_file():
            return path
    return candidates[-1]


def read_dotenv(path: Path | None = None) -> dict[str, str]:
    """Minimal KEY=VALUE parser: comments, blank lines, quotes, `export ` prefix."""
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
        value = value.strip()
        if value[:1] in ('"', "'"):
            # A quoted value may contain ` #` — take everything up to the
            # closing quote and only then discard the rest of the line.
            quote = value[0]
            closing = value.find(quote, 1)
            value = value[1:closing] if closing != -1 else value[1:]
        else:
            value = value.split(" #", 1)[0].strip()
        values[key] = value
    return values


def migrate_dotenv_once() -> str | None:
    """Import a key from a leftover `.env` into the encrypted store, once.

    `.env` is no longer read at run time. This exists only so an install that
    predates the encrypted store keeps working; it runs when the store holds no
    key, and never overwrites one. Returns the variable name imported, if any —
    never the value.
    """
    if credentials.get(API_KEY_VARS[0]):
        return None
    dotenv = read_dotenv()
    for name in API_KEY_VARS:
        value = dotenv.get(name, "").strip()
        if value and not _is_placeholder(value):
            if credentials.put(API_KEY_VARS[0], value):
                _log.info("imported %s from .env into the encrypted store", name)
                return name
            return None
    return None


def _is_placeholder(value: str) -> bool:
    # Real Google API keys start with "AIza"; a random key body could contain
    # a marker like "here" by chance, so never reject one of those.
    if value.startswith("AIza"):
        return False
    low = value.lower()
    return any(marker in low for marker in _PLACEHOLDER_MARKERS)


def load_api_key() -> str | None:
    """Look up the key: environment variable, encrypted store, then Keychain.

    `.env` is no longer consulted — the key is entered in Preferences. Never
    stored in source or in the QSettings plist.
    """
    for env in API_KEY_VARS:
        v = os.environ.get(env)
        if v and v.strip() and not _is_placeholder(v.strip()):
            return v.strip()

    stored = credentials.get(API_KEY_VARS[0])
    if stored and not _is_placeholder(stored):
        return stored

    try:
        import keyring

        v = keyring.get_password(KEYRING_SERVICE, KEYRING_USER)
        return v.strip() if v else None
    except Exception as exc:
        _log.warning("keychain unavailable: %s", exc)
        return None


def api_key_source() -> str:
    """Where the key in force comes from, for the Preferences dialog to show."""
    for env in API_KEY_VARS:
        v = (os.environ.get(env) or "").strip()
        if v and not _is_placeholder(v):
            return f"{env} environment variable"
    stored = credentials.get(API_KEY_VARS[0])
    if stored and not _is_placeholder(stored):
        return "encrypted store"
    try:
        import keyring

        if keyring.get_password(KEYRING_SERVICE, KEYRING_USER):
            return "macOS Keychain"
    except Exception:  # noqa: BLE001 — only used for a label
        pass
    return ""


def save_api_key(key: str, *, to_keychain: bool = False) -> bool:
    """Store the key encrypted, and optionally in the Keychain as well."""
    ok = credentials.put(API_KEY_VARS[0], key.strip())
    if to_keychain:
        try:
            import keyring

            keyring.set_password(KEYRING_SERVICE, KEYRING_USER, key.strip())
        except Exception as exc:  # noqa: BLE001
            _log.error("could not write key to keychain: %s", exc)
            return False
    return ok


def stored_api_key() -> str | None:
    """The key held in the encrypted store, ignoring the env var and Keychain.

    Preferences takes this before it writes anything, so cancelling can put the
    store back exactly as it found it.
    """
    return credentials.get(API_KEY_VARS[0])


def restore_stored_api_key(previous: str | None) -> None:
    """Undo a write to the encrypted store, back to `previous`.

    Only the store is restored. A key that was also copied into the Keychain
    stays there — there is no way to tell a fresh copy from a pre-existing one.
    """
    if previous:
        credentials.put(API_KEY_VARS[0], previous)
    else:
        credentials.remove(API_KEY_VARS[0])


def clear_api_key() -> None:
    """Forget the key everywhere this application put it."""
    credentials.remove(API_KEY_VARS[0])
    try:
        import keyring

        keyring.delete_password(KEYRING_SERVICE, KEYRING_USER)
    except Exception:  # noqa: BLE001 — nothing stored is the normal case
        pass
