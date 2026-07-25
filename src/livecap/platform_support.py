"""Platform differences, kept in one place.

Only macOS is shipped today, but keeping these behind functions means the rest
of the code carries no per-platform branches, and the Windows behaviour can be
tested here without a Windows machine.

Named platform_support rather than platform so it cannot shadow the standard
library module.
"""

from __future__ import annotations

import os
import sys
from pathlib import Path

MACOS = "darwin"
WINDOWS = "win32"


def _plat() -> str:
    return sys.platform


def is_macos() -> bool:
    return _plat() == MACOS


def is_windows() -> bool:
    return _plat() == WINDOWS


def app_support_dir() -> Path:
    """Per-user directory for application data.

    This is where a packaged build looks for `.env`, since the repository root
    does not exist inside a bundle.
    """
    if is_macos():
        return Path("~/Library/Application Support/LiveCap").expanduser()
    if is_windows():
        if appdata := os.environ.get("APPDATA"):
            return Path(appdata) / "LiveCap"
        return Path("~/AppData/Roaming/LiveCap").expanduser()
    if xdg := os.environ.get("XDG_CONFIG_HOME"):
        return Path(xdg) / "LiveCap"
    return Path("~/.config/LiveCap").expanduser()


def caption_font_families() -> list[str]:
    """Font stack for the captions, Latin first then a CJK face.

    Korean output needs an explicit Hangul family. Without one Qt falls back per
    glyph and the weight and spacing drift away from the Latin text.
    """
    if is_macos():
        return ["SF Pro Display", "Apple SD Gothic Neo", "Helvetica Neue", "Helvetica"]
    if is_windows():
        return ["Segoe UI", "Malgun Gothic", "Arial"]
    return ["Noto Sans", "Noto Sans CJK KR", "DejaVu Sans"]


def reveal_command(path: Path) -> list[str]:
    """Command that opens a directory in the system file manager."""
    if is_macos():
        return ["open", str(path)]
    if is_windows():
        return ["explorer", str(path)]
    return ["xdg-open", str(path)]
