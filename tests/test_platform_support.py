"""Platform branches, exercised without needing the other platforms.

Only macOS ships today. These tests pin the Windows and Linux behaviour so the
branches cannot rot before anyone builds for them.
"""

from __future__ import annotations

import sys
from pathlib import Path

import pytest

from livecap import platform_support as ps


@pytest.fixture
def on(monkeypatch):
    def _set(plat: str):
        monkeypatch.setattr(ps.sys, "platform", plat)

    return _set


# --- detection ------------------------------------------------------------


def test_detection_is_mutually_exclusive(on):
    on("darwin")
    assert ps.is_macos() and not ps.is_windows()
    on("win32")
    assert ps.is_windows() and not ps.is_macos()
    on("linux")
    assert not ps.is_macos() and not ps.is_windows()


# --- application data directory -------------------------------------------


def test_macos_uses_application_support(on):
    on("darwin")
    assert ps.app_support_dir() == Path("~/Library/Application Support/LiveCap").expanduser()


def test_windows_uses_appdata(on, monkeypatch):
    on("win32")
    monkeypatch.setenv("APPDATA", "/fake/Roaming")
    assert ps.app_support_dir() == Path("/fake/Roaming/LiveCap")


def test_windows_falls_back_when_appdata_is_unset(on, monkeypatch):
    on("win32")
    monkeypatch.delenv("APPDATA", raising=False)
    assert ps.app_support_dir() == Path("~/AppData/Roaming/LiveCap").expanduser()


def test_linux_honours_xdg_config_home(on, monkeypatch):
    on("linux")
    monkeypatch.setenv("XDG_CONFIG_HOME", "/fake/xdg")
    assert ps.app_support_dir() == Path("/fake/xdg/LiveCap")


def test_linux_falls_back_to_dot_config(on, monkeypatch):
    on("linux")
    monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
    assert ps.app_support_dir() == Path("~/.config/LiveCap").expanduser()


def test_no_platform_returns_a_path_under_home(on, monkeypatch):
    monkeypatch.delenv("APPDATA", raising=False)
    monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
    for plat in ("darwin", "win32", "linux", "freebsd"):
        on(plat)
        path = ps.app_support_dir()
        assert path.is_absolute(), plat
        assert path.name == "LiveCap", plat


# --- fonts ----------------------------------------------------------------


def test_every_platform_offers_a_latin_and_a_cjk_face(on):
    # Korean output needs an explicit Hangul family on every platform.
    expected_cjk = {
        "darwin": "Apple SD Gothic Neo",
        "win32": "Malgun Gothic",
        "linux": "Noto Sans CJK KR",
    }
    for plat, cjk in expected_cjk.items():
        on(plat)
        families = ps.caption_font_families()
        assert len(families) >= 2, plat
        assert cjk in families, plat


def test_font_stack_is_never_empty(on):
    for plat in ("darwin", "win32", "linux", "freebsd"):
        on(plat)
        assert ps.caption_font_families(), plat


# --- revealing a folder ---------------------------------------------------


def test_reveal_command_per_platform(on):
    path = Path("/tmp/transcripts")
    on("darwin")
    assert ps.reveal_command(path) == ["open", str(path)]
    on("win32")
    assert ps.reveal_command(path) == ["explorer", str(path)]
    on("linux")
    assert ps.reveal_command(path) == ["xdg-open", str(path)]


def test_reveal_command_passes_the_path_as_a_string(on):
    on("darwin")
    cmd = ps.reveal_command(Path("~/Documents/LiveCap").expanduser())
    assert all(isinstance(part, str) for part in cmd)
    assert "~" not in cmd[1]


# --- the name macOS shows in the menu bar ---------------------------------


def test_setting_the_app_name_is_declined_off_macos(on):
    for plat in ("win32", "linux"):
        on(plat)
        assert ps.set_app_name("LiveCap") is False


@pytest.mark.skipif(sys.platform != "darwin", reason="macOS bundle info")
def test_macos_really_renames_the_bundle():
    """AppKit takes the application menu title from here."""
    import ctypes
    import ctypes.util

    assert ps.set_app_name("LiveCapTest") is True

    objc = ctypes.cdll.LoadLibrary(ctypes.util.find_library("objc"))
    cf = ctypes.cdll.LoadLibrary(ctypes.util.find_library("CoreFoundation"))
    objc.objc_getClass.restype = ctypes.c_void_p
    objc.objc_getClass.argtypes = [ctypes.c_char_p]
    objc.sel_registerName.restype = ctypes.c_void_p
    objc.sel_registerName.argtypes = [ctypes.c_char_p]
    cf.CFStringCreateWithCString.restype = ctypes.c_void_p
    cf.CFStringCreateWithCString.argtypes = [ctypes.c_void_p, ctypes.c_char_p, ctypes.c_uint32]
    cf.CFStringGetCString.argtypes = [ctypes.c_void_p, ctypes.c_char_p, ctypes.c_long,
                                      ctypes.c_uint32]
    send = ctypes.cast(
        objc.objc_msgSend,
        ctypes.CFUNCTYPE(ctypes.c_void_p, ctypes.c_void_p, ctypes.c_void_p),
    )
    send_key = ctypes.cast(
        objc.objc_msgSend,
        ctypes.CFUNCTYPE(ctypes.c_void_p, ctypes.c_void_p, ctypes.c_void_p, ctypes.c_void_p),
    )
    bundle = send(objc.objc_getClass(b"NSBundle"), objc.sel_registerName(b"mainBundle"))
    info = send(bundle, objc.sel_registerName(b"infoDictionary"))
    key = cf.CFStringCreateWithCString(None, b"CFBundleName", 0x08000100)
    value = send_key(info, objc.sel_registerName(b"objectForKey:"), key)
    buf = ctypes.create_string_buffer(256)
    cf.CFStringGetCString(value, buf, 256, 0x08000100)
    try:
        assert buf.value.decode() == "LiveCapTest"
    finally:
        ps.set_app_name("LiveCap")


# --- keeping the display awake --------------------------------------------


def test_blocker_starts_holding_nothing():
    assert ps.DisplaySleepBlocker().held is False


def test_non_macos_declines_rather_than_guessing(on):
    """Windows and Linux need different APIs; neither is shipped, so neither is faked."""
    for plat in ("win32", "linux"):
        on(plat)
        blocker = ps.DisplaySleepBlocker()
        assert blocker.acquire() is False
        assert blocker.held is False
        blocker.release()  # must not raise


def test_release_without_acquire_is_a_noop():
    ps.DisplaySleepBlocker().release()  # must not raise


def test_a_refused_assertion_does_not_raise(on, monkeypatch):
    """Translating matters more than the screen; a failure is logged, not fatal."""
    on("darwin")

    def boom():
        raise OSError("IOKit unavailable")

    monkeypatch.setattr(ps, "_power_libraries", boom)
    blocker = ps.DisplaySleepBlocker()
    assert blocker.acquire() is False
    assert blocker.held is False


def test_a_nonzero_status_is_treated_as_failure(on, monkeypatch):
    on("darwin")

    class _FakeIOKit:
        def IOPMAssertionCreateWithName(self, *_a):  # noqa: N802 (mirrors the C name)
            return 1  # kIOReturnError

    class _FakeCF:
        def CFStringCreateWithCString(self, *_a):  # noqa: N802
            return 0

    monkeypatch.setattr(ps, "_power_libraries", lambda: (_FakeIOKit(), _FakeCF()))
    assert ps.DisplaySleepBlocker().acquire() is False


@pytest.mark.skipif(sys.platform != "darwin", reason="macOS power assertions")
def test_macos_really_takes_and_releases_the_assertion():
    """The real IOKit call, checked against pmset."""
    import subprocess

    blocker = ps.DisplaySleepBlocker()
    assert blocker.acquire("LiveCap test assertion") is True
    assert blocker.held is True
    try:
        listed = subprocess.run(
            ["pmset", "-g", "assertions"], capture_output=True, text=True
        ).stdout
        assert "LiveCap test assertion" in listed
    finally:
        blocker.release()
    assert blocker.held is False
    listed = subprocess.run(
        ["pmset", "-g", "assertions"], capture_output=True, text=True
    ).stdout
    assert "LiveCap test assertion" not in listed


@pytest.mark.skipif(sys.platform != "darwin", reason="macOS power assertions")
def test_acquiring_twice_holds_one_assertion():
    blocker = ps.DisplaySleepBlocker()
    assert blocker.acquire() is True
    first = blocker._assertion_id
    assert blocker.acquire() is True
    assert blocker._assertion_id is first  # not a second, leaked assertion
    blocker.release()


# --- wiring ---------------------------------------------------------------


def test_caption_view_takes_its_fonts_from_here():
    from livecap.ui import caption_view

    assert ps.caption_font_families() == caption_view.FONT_FAMILIES


def test_config_takes_its_app_dir_from_here():
    from livecap import config

    assert ps.app_support_dir() == config.APP_SUPPORT_DIR


# The window's use of reveal_command is covered in test_main_window.py, where
# the shared QApplication fixture lives.
