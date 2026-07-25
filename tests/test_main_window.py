"""Smoke tests for the menu bar wiring.

The window must build and its menus populate without an audio device or API key.
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
    # Never touch the real QSettings store.
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
    assert menu_titles(window) == ["LiveCap", "Translate", "Settings"]


def test_start_is_enabled_and_stop_disabled_while_idle(window):
    """Two separate items, with exactly one enabled at a time."""
    assert window._act_start.isEnabled() is True
    assert window._act_stop.isEnabled() is False


def test_start_and_stop_swap_when_running(window):
    window._on_state_changed(State.RUNNING.value, "")
    assert window._act_start.isEnabled() is False
    assert window._act_stop.isEnabled() is True


def test_status_item_is_display_only(window):
    assert window._act_status.isEnabled() is False
    assert window._act_status.text() == "Status: Idle"


def test_status_item_tracks_state(window):
    window._on_state_changed(State.RECONNECTING.value, "")
    assert "Reconnecting" in window._act_status.text()


def test_translation_menu_has_the_specified_shortcuts(window):
    shortcuts = {
        a.text(): a.shortcut().toString()
        for a in find_menu(window, "Translate").actions()
        if not a.isSeparator()
    }
    assert shortcuts["Start Translating"] == "Ctrl+R"
    assert shortcuts["Stop Translating"] == "Ctrl+."
    assert shortcuts["Clear Captions"] == "Ctrl+K"


def test_device_menu_is_repopulated_on_open(window, monkeypatch):
    """A USB interface can appear or vanish, so rebuild the list on every open."""
    monkeypatch.setattr(
        mw, "list_input_devices", lambda: [InputDevice(0, "Scarlett 2i2", 2, 48000.0)]
    )
    window._populate_device_menu()
    labels = [a.text() for a in window._device_menu.actions() if not a.isSeparator()]
    assert labels[0] == "System Default Input"
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
    assert "(no input device found)" in labels


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
    assert "API key" in window._view._status


def test_missing_audio_device_shows_status_not_a_modal(window, monkeypatch):
    monkeypatch.setattr(mw.config, "load_api_key", lambda: "test-key")

    class _Failing:
        def __init__(self, *a, **k):
            pass

        def start(self):
            raise RuntimeError("No audio input device found")

    monkeypatch.setattr(mw, "AudioCapture", _Failing)
    window.start_translation()
    assert window._state is State.ERROR
    assert "No audio input device" in window._view._status


def test_target_text_flows_into_the_caption_view(window):
    window._on_target_text("Hello there.")
    assert window._view._lines == [("Hello there.", True)]


def test_clear_captions_empties_the_view(window):
    window._on_target_text("Hello there.")
    window._clear_captions()
    assert window._view._lines == []
    assert window._buffer.is_empty


def test_silence_clear_setting_reaches_the_buffer(window):
    assert window._buffer.clear_after_seconds == 3.0
    window._update(clear_after_seconds=5.0)
    assert window._buffer.clear_after_seconds == 5.0


def test_tick_clears_the_view_after_silence(window, monkeypatch):
    import time as _time

    base = _time.monotonic()
    monkeypatch.setattr(mw.time, "monotonic", lambda: base)
    window._on_target_text("Hello there.")
    assert window._view._lines == [("Hello there.", True)]

    monkeypatch.setattr(mw.time, "monotonic", lambda: base + 3.5)
    window._on_tick()
    assert window._view._lines == []
    assert window._buffer.is_empty


def test_opening_the_transcript_folder_uses_the_platform_command(window, monkeypatch, tmp_path):
    """macOS uses `open`; Windows and Linux need their own command."""
    calls: list[list[str]] = []
    monkeypatch.setattr(mw.subprocess, "run", lambda cmd, **kw: calls.append(cmd))
    monkeypatch.setattr(mw.platform_support, "reveal_command", lambda p: ["fake-open", str(p)])

    window._update(transcript_dir=str(tmp_path))
    window._open_transcript_dir()

    assert calls == [["fake-open", str(tmp_path)]]


def test_default_box_width_leaves_five_percent_margins(window):
    assert window._settings.box_max_width_pct == 90


# --- direction presets ----------------------------------------------------


def test_default_direction_is_korean_to_english(window):
    assert window._settings.target_language_code == "en"
    assert mw.direction_for_target("en") is mw.Direction.KO_TO_EN
    assert mw.direction_for_target("ko") is mw.Direction.EN_TO_KO


def test_direction_menu_items_use_keys_one_and_two(window):
    shortcuts = {
        a.text(): a.shortcut().toString() for a in window._direction_actions.values()
    }
    assert shortcuts["Korean → English"] == "1"
    assert shortcuts["English → Korean"] == "2"


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
    window.set_direction(mw.Direction.KO_TO_EN)  # already KO→EN
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


# --- the 0 key ------------------------------------------------------------


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
    """The menu keeps both items so a mis-click cannot stop a live session."""
    labels = [a.text() for a in find_menu(window, "Translate").actions() if not a.isSeparator()]
    assert "Start Translating" in labels
    assert "Stop Translating" in labels


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
    """Only the translator session reopens; capture, buffer and transcript persist."""
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

    assert made == ["en", "ko"]             # reconnected with the new target
    assert stopped == ["stop"]              # only the old translator torn down
    assert window._capture is capture       # capture continues, audio unbroken
    assert window._writer is writer_before  # same transcript file

    # Content must be on disk before close().
    text = writer_before.path.read_text(encoding="utf-8")
    assert "Before the switch." in text
    assert "direction: EN → KO" in text


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
