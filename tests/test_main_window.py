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


# --- 번역 방향 프리셋 ------------------------------------------------------


def test_default_direction_is_korean_to_english(window):
    assert window._settings.target_language_code == "en"
    assert mw.direction_for_target("en") is mw.Direction.KO_TO_EN
    assert mw.direction_for_target("ko") is mw.Direction.EN_TO_KO


def test_direction_menu_items_use_keys_one_and_two(window):
    shortcuts = {
        a.text(): a.shortcut().toString() for a in window._direction_actions.values()
    }
    assert shortcuts["한국어 → 영어"] == "1"
    assert shortcuts["영어 → 한국어"] == "2"


def test_setting_direction_changes_the_target_language(window):
    window.set_direction(mw.Direction.EN_TO_KO)
    assert window._settings.target_language_code == "ko"
    window.set_direction(mw.Direction.KO_TO_EN)
    assert window._settings.target_language_code == "en"


def test_direction_menu_reflects_the_current_setting(window):
    window.set_direction(mw.Direction.EN_TO_KO)
    checked = [d for d, a in window._direction_actions.items() if a.isChecked()]
    assert checked == [mw.Direction.EN_TO_KO]


def test_switching_direction_shows_the_hint(window):
    window.set_direction(mw.Direction.EN_TO_KO)
    assert window._view._hint_text == "EN → KO"
    assert window._view._hint_alpha == 1.0
    window.set_direction(mw.Direction.KO_TO_EN)
    assert window._view._hint_text == "KO → EN"


def test_pressing_the_same_direction_still_shows_the_hint(window):
    window._view.hide_hint()
    window.set_direction(mw.Direction.KO_TO_EN)  # 이미 한→영
    assert window._view._hint_text == "KO → EN"


def test_hint_is_suppressed_when_the_menu_option_is_off(window):
    window._update(show_direction_hint=False)
    window._view.hide_hint()
    window.set_direction(mw.Direction.EN_TO_KO)
    assert window._view._hint_text == ""
    assert window._view._hint_alpha == 0.0


def test_custom_target_language_gets_a_generic_label(window):
    window._update(target_language_code="ja")
    assert mw.direction_label("ja") == "→ JA"
    assert not any(a.isChecked() for a in window._direction_actions.values())


# --- 0번 키 토글 -----------------------------------------------------------


def test_zero_key_is_bound_to_the_toggle(window):
    assert window._act_toggle.shortcut().toString() == "0"


def test_toggle_starts_when_idle_and_stops_when_running(window, monkeypatch):
    calls: list[str] = []
    monkeypatch.setattr(mw.CaptionWindow, "start_translation", lambda self: calls.append("start"))
    monkeypatch.setattr(mw.CaptionWindow, "stop_translation", lambda self: calls.append("stop"))

    window._state = State.IDLE
    window.toggle_translation()
    window._state = State.RUNNING
    window.toggle_translation()
    assert calls == ["start", "stop"]


def test_start_and_stop_remain_separate_menu_items(window):
    """실수로 중지를 누르는 사고를 줄이기 위해 메뉴는 둘로 유지한다."""
    labels = [a.text() for a in find_menu(window, "통역").actions() if not a.isSeparator()]
    assert "통역 시작" in labels
    assert "통역 중지" in labels


def test_starting_translation_announces_the_direction(window, monkeypatch):
    monkeypatch.setattr(mw.config, "load_api_key", lambda: "test-key")
    monkeypatch.setattr(mw, "AudioCapture", _StubCapture)
    monkeypatch.setattr(mw, "LiveTranslator", _StubTranslator)
    window._view.hide_hint()
    window.start_translation()
    assert window._view._hint_text == "KO → EN"


class _StubCapture:
    def __init__(self, *a, **k):
        self.ring = object()

    def start(self):
        return InputDevice(0, "Stub Mic", 1, 48000.0)

    def stop(self):
        pass


class _StubTranslator:
    def __init__(self, *a, **k):
        self.stats = type("S", (), {"reconnects": 0, "gaps_ms": []})()

    def start(self):
        pass

    def stop(self):
        pass


def test_switching_direction_while_running_keeps_audio_and_transcript(
    window, monkeypatch, tmp_path
):
    """오디오 캡처와 링 버퍼, 기록 파일은 유지하고 번역 세션만 다시 연다."""
    made: list[str] = []
    stopped: list[str] = []

    class _Tr(_StubTranslator):
        def __init__(self, ring, key, **kw):
            super().__init__()
            made.append(kw["target_language_code"])

        def stop(self):
            stopped.append("stop")

    capture = _StubCapture()
    monkeypatch.setattr(mw.config, "load_api_key", lambda: "test-key")
    monkeypatch.setattr(mw, "AudioCapture", lambda *a, **k: capture)
    monkeypatch.setattr(mw, "LiveTranslator", _Tr)

    window._update(transcript_dir=str(tmp_path))
    window.start_translation()
    assert made == ["en"]
    writer_before = window._writer
    assert writer_before is not None and writer_before.path is not None
    window._on_target_text("Before the switch.")

    window.set_direction(mw.Direction.EN_TO_KO)

    assert made == ["en", "ko"]      # 새 목적지로 다시 연결
    assert stopped == ["stop"]       # 구 번역기만 정리
    assert window._capture is capture  # 캡처는 그대로 — 오디오가 끊기지 않는다
    assert window._writer is writer_before  # 기록 파일도 그대로

    # 닫기 전에도 디스크에 반영되어 있어야 한다.
    text = writer_before.path.read_text(encoding="utf-8")
    assert "Before the switch." in text
    assert "번역 방향 — EN → KO" in text


def test_switching_direction_while_idle_does_not_start_a_session(window, monkeypatch):
    made: list[str] = []
    monkeypatch.setattr(mw, "LiveTranslator", lambda *a, **k: made.append(1))
    window.set_direction(mw.Direction.EN_TO_KO)
    assert made == []
    assert window._state is State.IDLE


def test_reconnect_signal_continues_the_previous_line(window):
    window._on_target_text("the quarterly numbers.")
    window._bridge.reconnected.emit()
    window._on_target_text("were better than expected.")
    assert window._buffer.committed_lines == [
        "the quarterly numbers. were better than expected."
    ]
