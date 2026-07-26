"""Platform differences, kept in one place.

Only macOS is shipped today, but keeping these behind functions means the rest
of the code carries no per-platform branches, and the Windows behaviour can be
tested here without a Windows machine.

Named platform_support rather than platform so it cannot shadow the standard
library module.
"""

from __future__ import annotations

import ctypes
import ctypes.util
import os
import sys
from pathlib import Path

from .util import logging as log

_log = log.get(__name__)

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


# --- keeping the display awake --------------------------------------------

# kIOPMAssertionTypeNoDisplaySleep. Stops the idle timer that blanks the
# screen, on battery as well as on mains — the same assertion a video player
# holds. It does not stop the lid switch or a manual lock.
_NO_DISPLAY_SLEEP = b"NoDisplaySleepAssertion"
_ASSERTION_LEVEL_ON = 255
_CF_STRING_ENCODING_UTF8 = 0x08000100


class DisplaySleepBlocker:
    """Holds a power assertion so the screen stays lit while captions are live.

    Captions are useless if the display blanks mid-session, and the user is
    watching rather than typing, so the idle timer fires. Acquiring is
    best-effort: if the assertion cannot be taken the app carries on and only
    logs it, because translating matters more than the screen staying on.
    """

    def __init__(self) -> None:
        self._assertion_id: ctypes.c_uint32 | None = None

    @property
    def held(self) -> bool:
        return self._assertion_id is not None

    def acquire(self, reason: str = "LiveCap is showing live captions") -> bool:
        """Take the assertion. Returns True if the display is now pinned awake."""
        if self._assertion_id is not None:
            return True
        if not is_macos():
            # Windows would use SetThreadExecutionState and Linux an inhibit
            # call over D-Bus. Neither ships, so neither is guessed at here.
            return False
        try:
            iokit, cf = _power_libraries()
            assertion_type = cf.CFStringCreateWithCString(
                None, _NO_DISPLAY_SLEEP, _CF_STRING_ENCODING_UTF8
            )
            name = cf.CFStringCreateWithCString(
                None, reason.encode("utf-8"), _CF_STRING_ENCODING_UTF8
            )
            assertion_id = ctypes.c_uint32(0)
            rc = iokit.IOPMAssertionCreateWithName(
                assertion_type, _ASSERTION_LEVEL_ON, name, ctypes.byref(assertion_id)
            )
        except Exception as exc:  # noqa: BLE001 — never block translation
            _log.warning("could not keep the display awake: %s", exc)
            return False
        if rc != 0:
            _log.warning("display sleep assertion refused (status %d)", rc)
            return False
        self._assertion_id = assertion_id
        _log.info("display sleep blocked while translating")
        return True

    def release(self) -> None:
        """Give the assertion back. Safe to call when nothing is held."""
        assertion_id, self._assertion_id = self._assertion_id, None
        if assertion_id is None:
            return
        try:
            iokit, _cf = _power_libraries()
            iokit.IOPMAssertionRelease(assertion_id)
        except Exception as exc:  # noqa: BLE001
            _log.warning("could not release the display sleep assertion: %s", exc)
        else:
            _log.info("display sleep no longer blocked")


def _power_libraries() -> tuple[ctypes.CDLL, ctypes.CDLL]:
    """Load IOKit and CoreFoundation with the signatures we call declared."""
    iokit = ctypes.cdll.LoadLibrary(ctypes.util.find_library("IOKit"))
    cf = ctypes.cdll.LoadLibrary(ctypes.util.find_library("CoreFoundation"))
    cf.CFStringCreateWithCString.restype = ctypes.c_void_p
    cf.CFStringCreateWithCString.argtypes = [ctypes.c_void_p, ctypes.c_char_p, ctypes.c_uint32]
    iokit.IOPMAssertionCreateWithName.restype = ctypes.c_int
    iokit.IOPMAssertionCreateWithName.argtypes = [
        ctypes.c_void_p,
        ctypes.c_uint32,
        ctypes.c_void_p,
        ctypes.POINTER(ctypes.c_uint32),
    ]
    iokit.IOPMAssertionRelease.restype = ctypes.c_int
    iokit.IOPMAssertionRelease.argtypes = [ctypes.c_uint32]
    return iokit, cf
