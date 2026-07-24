"""메뉴바 배선 스모크 테스트 — PLAN.md §6.3.

실제 오디오 장치나 API 키 없이도 창이 뜨고 메뉴가 구성되어야 한다.
"""

from __future__ import annotations

import os

os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")

import pytest  # noqa: E402
from PyQt6.QtWidgets import QApplication  # noqa: E402

from livecap import config  # noqa: E402
from livecap.audio import InputDevice  # noqa: E402
from livecap.config import Settings  # noqa: E402
from livecap.translator import State  # noqa: E402
from livecap.ui import main_window as mw  # noqa: E402


@pytest.fixture(scope="session")
def qapp():
    yield QApplication.instance() or QApplication([])


@pytest.fixture
def window(qapp, monkeypatch):
    # QSettings에 실제로 쓰지 않도록 막는다.
    monkeypatch.setattr(config, "save", lambda _s: None)
    monkeypatch.setattr(mw.config, "save", lambda _s: None)
    w = mw.CaptionWindow(Settings())
    yield w
    w._tick.stop()
    w.close()


def menu_titles(window) -> list[str]:
    return [a.menu().title() for a in window.menuBar().actions() if a.menu() is not None]


def find_menu(window, title):
    for a in window.menuBar().actions():
        if a.menu() is not None and a.menu().title() == title:
            return a.menu()
    raise AssertionError(f"menu {title!r} not found")


def test_all_three_menus_exist(window):
    assert menu_titles(window) == ["LiveCap", "통역", "설정"]


def test_start_is_enabled_and_stop_disabled_while_idle(window):
    """토글이 아니라 별도 항목 두 개, 상태에 따라 하나씩만 활성."""
    assert window._act_start.isEnabled() is True
    assert window._act_stop.isEnabled() is False


def test_start_and_stop_swap_when_running(window):
    window._on_state_changed(State.RUNNING.value, "")
    assert window._act_start.isEnabled() is False
    assert window._act_stop.isEnabled() is True


def test_status_item_is_display_only(window):
    assert window._act_status.isEnabled() is False
    assert window._act_status.text() == "상태: 대기 중"


def test_status_item_tracks_state(window):
    window._on_state_changed(State.RECONNECTING.value, "")
    assert "재연결 중" in window._act_status.text()


def test_translation_menu_has_the_specified_shortcuts(window):
    shortcuts = {
        a.text(): a.shortcut().toString()
        for a in find_menu(window, "통역").actions()
        if not a.isSeparator()
    }
    assert shortcuts["통역 시작"] == "Ctrl+R"
    assert shortcuts["통역 중지"] == "Ctrl+."
    assert shortcuts["캡션 지우기"] == "Ctrl+K"


def test_device_menu_is_repopulated_on_open(window, monkeypatch):
    """USB 인터페이스는 도중에 연결·해제될 수 있으므로 열 때마다 갱신한다."""
    monkeypatch.setattr(
        mw, "list_input_devices", lambda: [InputDevice(0, "Scarlett 2i2", 2, 48000.0)]
    )
    window._populate_device_menu()
    labels = [a.text() for a in window._device_menu.actions() if not a.isSeparator()]
    assert labels[0] == "시스템 기본 입력"
    assert any("Scarlett 2i2" in label for label in labels)

    monkeypatch.setattr(
        mw, "list_input_devices", lambda: [InputDevice(0, "MacBook Air Microphone", 1, 48000.0)]
    )
    window._populate_device_menu()
    labels = [a.text() for a in window._device_menu.actions() if not a.isSeparator()]
    assert not any("Scarlett" in label for label in labels)
    assert any("MacBook Air Microphone" in label for label in labels)


def test_device_menu_handles_no_devices(window, monkeypatch):
    monkeypatch.setattr(mw, "list_input_devices", lambda: [])
    window._populate_device_menu()
    labels = [a.text() for a in window._device_menu.actions() if not a.isSeparator()]
    assert "(입력 장치를 찾을 수 없습니다)" in labels


def test_choosing_a_device_stores_the_name_not_the_index(window, monkeypatch):
    monkeypatch.setattr(
        mw, "list_input_devices", lambda: [InputDevice(7, "Scarlett 2i2", 2, 48000.0)]
    )
    window._choose_device("Scarlett 2i2")
    assert window._settings.audio_device_name == "Scarlett 2i2"


def test_current_device_is_checked_in_the_menu(window, monkeypatch):
    monkeypatch.setattr(
        mw, "list_input_devices", lambda: [InputDevice(0, "Scarlett 2i2", 2, 48000.0)]
    )
    window._update(audio_device_name="Scarlett 2i2")
    window._populate_device_menu()
    checked = [a.text() for a in window._device_menu.actions() if a.isCheckable() and a.isChecked()]
    assert len(checked) == 1
    assert "Scarlett 2i2" in checked[0]


def test_font_size_menu_reflects_current_setting(window):
    window._update(font_size=72)
    checked = [a.text() for a in window._font_group.actions() if a.isChecked()]
    assert checked == ["72"]


def test_line_count_menu_reflects_current_setting(window):
    window._update(max_lines=3)
    checked = [a.text() for a in window._lines_group.actions() if a.isChecked()]
    assert checked == ["3"]
    assert window._buffer.max_lines == 3


def test_settings_change_does_not_stop_a_running_session(window):
    window._on_state_changed(State.RUNNING.value, "")
    window._update(bg_color="#123456")
    assert window._state is State.RUNNING
    assert window._settings.bg_color == "#123456"


def test_start_without_api_key_shows_status_not_a_modal(window, monkeypatch):
    monkeypatch.setattr(mw.config, "load_api_key", lambda: None)
    window.start_translation()
    assert window._state is State.ERROR
    assert "API 키" in window._view._status


def test_missing_audio_device_shows_status_not_a_modal(window, monkeypatch):
    monkeypatch.setattr(mw.config, "load_api_key", lambda: "test-key")

    class _Failing:
        def __init__(self, *a, **k):
            pass

        def start(self):
            raise RuntimeError("오디오 입력 장치를 찾을 수 없습니다")

    monkeypatch.setattr(mw, "AudioCapture", _Failing)
    window.start_translation()
    assert window._state is State.ERROR
    assert "오디오 입력 장치" in window._view._status


def test_target_text_flows_into_the_caption_view(window):
    window._on_target_text("Hello there.")
    assert window._view._lines == [("Hello there.", True)]


def test_clear_captions_empties_the_view(window):
    window._on_target_text("Hello there.")
    window._clear_captions()
    assert window._view._lines == []
    assert window._buffer.is_empty


def test_silence_clear_setting_reaches_the_buffer(window):
    assert window._buffer.clear_after_seconds == 2.0
    window._update(clear_after_seconds=5.0)
    assert window._buffer.clear_after_seconds == 5.0


def test_tick_clears_the_view_after_silence(window, monkeypatch):
    import time as _time

    base = _time.monotonic()
    monkeypatch.setattr(mw.time, "monotonic", lambda: base)
    window._on_target_text("Hello there.")
    assert window._view._lines == [("Hello there.", True)]

    monkeypatch.setattr(mw.time, "monotonic", lambda: base + 2.5)
    window._on_tick()
    assert window._view._lines == []
    assert window._buffer.is_empty


def test_default_box_width_leaves_five_percent_margins(window):
    assert window._settings.box_max_width_pct == 90


def test_reconnect_signal_continues_the_previous_line(window):
    window._on_target_text("the quarterly numbers.")
    window._bridge.reconnected.emit()
    window._on_target_text("were better than expected.")
    assert window._buffer.committed_lines == [
        "the quarterly numbers. were better than expected."
    ]
