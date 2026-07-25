"""Platform branches, exercised without needing the other platforms.

Only macOS ships today. These tests pin the Windows and Linux behaviour so the
branches cannot rot before anyone builds for them.
"""

from __future__ import annotations

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


# --- wiring ---------------------------------------------------------------


def test_caption_view_takes_its_fonts_from_here():
    from livecap.ui import caption_view

    assert ps.caption_font_families() == caption_view.FONT_FAMILIES


def test_config_takes_its_app_dir_from_here():
    from livecap import config

    assert ps.app_support_dir() == config.APP_SUPPORT_DIR


# The window's use of reveal_command is covered in test_main_window.py, where
# the shared QApplication fixture lives.
