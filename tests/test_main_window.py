"""Smoke tests for the menu bar wiring.

The window must build and its menus populate without an audio device or API key.
"""

from __future__ import annotations

import os
import time

os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")

from dataclasses import replace  # noqa: E402

import pytest  # noqa: E402
from PyQt6.QtWidgets import QApplication  # noqa: E402

from livecap import config  # noqa: E402
from livecap.audio import InputDevice  # noqa: E402
from livecap.caption_buffer import Policy  # noqa: E402
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
    window._translator = _StubTranslator()
    window._on_state_changed(State.RUNNING.value, "")
    assert window._act_start.isEnabled() is False
    assert window._act_stop.isEnabled() is True


def test_a_failed_start_leaves_start_usable(window, monkeypatch):
    """An error with nothing running must not lock the user out of Start."""
    monkeypatch.setattr(mw.config, "load_api_key", lambda: None)
    window.start_translation()
    assert window._state is State.ERROR
    assert window._act_start.isEnabled() is True
    assert "API key" in window._view._status


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
    assert window._buffer.clear_after_seconds == 2.0
    window._update(clear_after_seconds=5.0)
    assert window._buffer.clear_after_seconds == 5.0


def test_line_hold_setting_reaches_the_buffer(window):
    assert window._buffer.line_hold_seconds == 0.7
    window._update(line_hold_seconds=1.5)
    assert window._buffer.line_hold_seconds == 1.5


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


# --- complete phrases only ------------------------------------------------


def test_the_nine_key_is_bound_to_complete_phrases(window):
    assert window._act_complete_phrases.shortcut().toString() == "9"


def test_it_is_off_by_default(window):
    assert window._settings.complete_phrases_only is False
    assert window._act_complete_phrases.isChecked() is False
    assert window._buffer.complete_phrases_only is False


def test_toggling_it_reaches_the_buffer(window):
    window._update(complete_phrases_only=True)
    assert window._buffer.complete_phrases_only is True
    assert window._act_complete_phrases.isChecked() is True


def test_it_withholds_unsettled_text_from_the_view(window):
    """The reported symptom: captions rewriting themselves as the model works."""
    window._update(complete_phrases_only=True)
    window._on_target_text("The quarterly numbers")
    assert window._view._lines == []

    window._on_target_text(" are ahead of plan.")
    assert window._view._lines == [("The quarterly numbers are ahead of plan.", True)]


def test_turn_complete_flushes_the_buffer_to_the_view(window):
    window._update(complete_phrases_only=True)
    window._on_target_text("A phrase with no full stop")
    assert window._view._lines == []

    window._bridge.turn_complete.emit()
    assert window._view._lines == [("A phrase with no full stop", True)]


def test_turn_complete_writes_the_line_to_the_transcript(
    window, monkeypatch, tmp_path
):
    _running_window(window, monkeypatch, tmp_path)
    path = window._writer.path
    window._update(complete_phrases_only=True)
    window._on_target_text("Settled by the end of the turn")
    window._bridge.turn_complete.emit()
    assert "Settled by the end of the turn" in path.read_text(encoding="utf-8")


def test_a_saved_setting_reaches_the_buffer_at_startup(qapp, monkeypatch):
    """It used to take a Preferences visit before the buffer picked this up."""
    monkeypatch.setattr(mw.config, "save", lambda _s: None)
    w = mw.CaptionWindow(replace(Settings(), complete_phrases_only=True, line_hold_seconds=1.5))
    try:
        assert w._buffer.complete_phrases_only is True
        assert w._buffer.line_hold_seconds == 1.5
    finally:
        w._tick.stop()
        w.close()


# --- the 0 key ------------------------------------------------------------


def test_zero_key_is_bound_to_the_toggle(window):
    assert window._act_toggle.shortcut().toString() == "0"


def test_toggle_starts_when_idle_and_stops_when_running(window, monkeypatch):
    calls: list[str] = []
    monkeypatch.setattr(mw.CaptionWindow, "start_translation", lambda self: calls.append("start"))
    monkeypatch.setattr(mw.CaptionWindow, "stop_translation", lambda self: calls.append("stop"))

    window.toggle_translation()
    window._translator = _StubTranslator()  # a session is now live
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
        self.device = InputDevice(0, "Stub Mic", 1, 48000.0)

    def start(self):
        return self.device

    def stop(self):
        pass

    def take_peak(self) -> float:
        return 0.0


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


# --- preferences applied to a live session --------------------------------


def _running_window(window, monkeypatch, tmp_path):
    """Start a session and return (made_targets, stopped, restarted_capture)."""
    made: list[dict] = []
    events: list[str] = []

    class _Capture(_StubCapture):
        def start(self):
            events.append("capture-start")
            return InputDevice(0, "Stub Mic", 1, 48000.0)

        def stop(self):
            events.append("capture-stop")

    class _Tr(_StubTranslator):
        def __init__(self, ring, key, **kw):
            super().__init__()
            made.append(kw)

        def stop(self):
            events.append("translator-stop")

    monkeypatch.setattr(mw.config, "load_api_key", lambda: "test-key")
    monkeypatch.setattr(mw, "AudioCapture", lambda *a, **k: _Capture())
    monkeypatch.setattr(mw, "LiveTranslator", _Tr)
    window._update(transcript_dir=str(tmp_path))
    window.start_translation()
    return made, events


def test_changing_the_target_language_in_preferences_reconnects(window, monkeypatch, tmp_path):
    """The target is fixed at connect time, so it cannot apply silently."""
    made, events = _running_window(window, monkeypatch, tmp_path)
    assert made[0]["target_language_code"] == "en"

    window._apply_new_settings(replace(window._settings, target_language_code="ko"))

    assert [m["target_language_code"] for m in made] == ["en", "ko"]
    assert events.count("translator-stop") == 1
    assert "capture-stop" not in events  # audio keeps running, nothing is lost


def test_changing_the_silence_gate_in_preferences_reconnects(window, monkeypatch, tmp_path):
    made, _events = _running_window(window, monkeypatch, tmp_path)
    window._apply_new_settings(replace(window._settings, silence_gate=True))
    assert [m["silence_gate"] for m in made] == [False, True]


def test_changing_the_vocabulary_in_preferences_reconnects(window, monkeypatch, tmp_path):
    """The terms are fixed when the session connects, so they cannot apply silently."""
    made, _events = _running_window(window, monkeypatch, tmp_path)
    window._apply_new_settings(
        replace(window._settings, vocabulary_source="영업이익률\n평우")
    )
    assert [m["source_terms"] for m in made] == [[], ["영업이익률", "평우"]]


def test_changing_the_audio_device_in_preferences_restarts_capture(
    window, monkeypatch, tmp_path
):
    _made, events = _running_window(window, monkeypatch, tmp_path)
    window._apply_new_settings(replace(window._settings, audio_device_name="Other Mic"))
    assert events.count("capture-stop") == 1
    assert events.count("capture-start") == 2


def test_a_cosmetic_preference_change_does_not_touch_the_session(
    window, monkeypatch, tmp_path
):
    made, events = _running_window(window, monkeypatch, tmp_path)
    window._apply_new_settings(replace(window._settings, font_size=72))
    assert len(made) == 1
    assert "translator-stop" not in events


def test_preferences_apply_the_clear_delay_and_policy_immediately(window):
    """These live in the buffer, which used to keep the old values until restart."""
    window._apply_new_settings(
        replace(window._settings, clear_after_seconds=9.0, caption_policy="utterance")
    )
    assert window._buffer.clear_after_seconds == 9.0
    assert window._buffer.effective_policy is Policy.UTTERANCE


# --- Apply, OK and Cancel in Preferences ----------------------------------


class _FakeDialog:
    """Stands in for SettingsDialog: scripted button presses, no widgets."""

    def __init__(self, script, result):
        self._script = script  # callables run inside exec()
        self._result = result  # what exec() returns: 1 = OK, 0 = Cancel
        self._settings = None
        self.applied = _Signal()

    def exec(self):
        for step in self._script:
            step(self)
        return self._result

    def result_settings(self):
        return self._settings


class _Signal:
    def __init__(self):
        self._slots = []

    def connect(self, slot):
        self._slots.append(slot)

    def emit(self, value):
        for slot in self._slots:
            slot(value)


def _open_with(window, monkeypatch, script, result, base=None):
    """Run _open_settings against a scripted dialog."""
    base = base if base is not None else window._settings
    made = {}

    def factory(settings, parent=None):
        dlg = _FakeDialog(script, result)
        dlg._settings = base
        made["dlg"] = dlg
        return dlg

    monkeypatch.setattr(mw, "SettingsDialog", factory)
    window._open_settings()
    return made["dlg"]


def test_apply_takes_effect_while_the_dialog_is_still_open(window, monkeypatch):
    target = replace(window._settings, font_size=72)
    seen: list[int] = []

    def press_apply(dlg):
        dlg.applied.emit(target)
        seen.append(window._settings.font_size)  # read from inside exec()
        seen.append(window._view._settings.font_size)

    _open_with(window, monkeypatch, script=[press_apply], result=1, base=target)
    assert seen == [72, 72], "Apply must reach the window and the view at once"


def test_apply_then_ok_keeps_the_change(window, monkeypatch):
    target = replace(window._settings, font_size=72)
    _open_with(
        window, monkeypatch, script=[lambda d: d.applied.emit(target)], result=1, base=target
    )
    assert window._settings.font_size == 72


def test_apply_then_cancel_restores_the_original_settings(window, monkeypatch):
    """Cancel means "as it was", even when Apply already changed things."""
    original = window._settings
    applied = replace(original, font_size=96, bg_color="#123456", max_lines=4)
    _open_with(
        window, monkeypatch, script=[lambda d: d.applied.emit(applied)], result=0, base=applied
    )
    assert window._settings == original
    assert window._view._settings.font_size == original.font_size
    assert window._buffer.max_lines == original.max_lines


def test_cancel_without_apply_changes_nothing(window, monkeypatch):
    original = window._settings
    _open_with(window, monkeypatch, script=[], result=0)
    assert window._settings == original


def test_ok_applies_the_dialog_result(window, monkeypatch):
    target = replace(window._settings, font_size=32)
    _open_with(window, monkeypatch, script=[], result=1, base=target)
    assert window._settings.font_size == 32


def test_apply_reaches_a_live_session_and_cancel_takes_it_back(
    window, monkeypatch, tmp_path
):
    """Apply must restart what a live session needs, and Cancel must undo that."""
    made, _events = _running_window(window, monkeypatch, tmp_path)
    assert [m["target_language_code"] for m in made] == ["en"]

    applied = replace(window._settings, target_language_code="ko")
    _open_with(
        window, monkeypatch, script=[lambda d: d.applied.emit(applied)], result=0, base=applied
    )
    # Applied, then reverted: the translator reconnected twice, ending on "en".
    assert [m["target_language_code"] for m in made] == ["en", "ko", "en"]
    assert window._settings.target_language_code == "en"


def test_resetting_settings_keeps_undrained_lines_in_the_transcript(
    window, monkeypatch, tmp_path
):
    """Reset replaces the buffer; anything unwritten must be flushed first."""
    _running_window(window, monkeypatch, tmp_path)
    path = window._writer.path
    window._on_target_text("Said before the reset")
    monkeypatch.setattr(mw.config, "reset", lambda: Settings())
    window._reset_settings()
    assert "Said before the reset" in path.read_text(encoding="utf-8")


# --- start-up warnings ----------------------------------------------------


def test_a_device_fallback_warning_survives_the_next_overlay_refresh(
    window, monkeypatch, tmp_path
):
    """The message used to be wiped before the user could read it."""
    monkeypatch.setattr(mw.config, "load_api_key", lambda: "test-key")
    monkeypatch.setattr(mw, "AudioCapture", lambda *a, **k: _StubCapture())
    monkeypatch.setattr(mw, "LiveTranslator", _StubTranslator)
    window._update(transcript_dir=str(tmp_path), audio_device_name="Missing Interface")

    window.start_translation()
    window._on_state_changed(State.RUNNING.value, "")  # refreshes the overlay

    assert "Missing Interface" in window._view._status
    assert "Stub Mic" in window._view._status


def test_a_notice_expires_on_its_own(window, monkeypatch):
    window._show_notice("temporary")
    assert window._view._status == "temporary"
    window._notice_until = 0.0  # pretend the hold elapsed
    window._refresh_overlay()
    assert window._view._status == ""


def test_an_error_detail_outranks_a_notice(window):
    window._show_notice("just a warning")
    window._on_state_changed(State.ERROR.value, "Billing credits exhausted")
    assert window._view._status == "Billing credits exhausted"


# --- keeping the display awake --------------------------------------------


class _FakeBlocker:
    def __init__(self):
        self.events: list[str] = []
        self.held = False

    def acquire(self, reason: str = "") -> bool:
        self.events.append("acquire")
        self.held = True
        return True

    def release(self) -> None:
        self.events.append("release")
        self.held = False


def test_translating_keeps_the_display_awake(window, monkeypatch, tmp_path):
    """Captions are watched, not typed at, so the idle timer would blank them."""
    blocker = _FakeBlocker()
    window._display_lock = blocker
    _running_window(window, monkeypatch, tmp_path)
    assert blocker.held is True

    window.stop_translation()
    assert blocker.held is False
    assert blocker.events == ["acquire", "release"]


def test_quitting_releases_the_display(window, monkeypatch, tmp_path):
    blocker = _FakeBlocker()
    window._display_lock = blocker
    _running_window(window, monkeypatch, tmp_path)
    window.shutdown()
    assert blocker.held is False


def test_the_display_is_released_even_if_capture_fails_to_stop(
    window, monkeypatch, tmp_path
):
    """A stuck device must not leave the screen pinned awake forever."""
    blocker = _FakeBlocker()
    window._display_lock = blocker

    class _Stuck(_StubCapture):
        def stop(self):
            raise RuntimeError("device wedged")

    monkeypatch.setattr(mw.config, "load_api_key", lambda: "test-key")
    monkeypatch.setattr(mw, "AudioCapture", lambda *a, **k: _Stuck())
    monkeypatch.setattr(mw, "LiveTranslator", _StubTranslator)
    window._update(transcript_dir=str(tmp_path))
    window.start_translation()
    window.shutdown()
    assert blocker.held is False


def test_idle_does_not_hold_the_display(window):
    assert window._display_lock.held is False


# --- a device that delivers silence ---------------------------------------


class _SilentCapture(_StubCapture):
    """Opens and streams happily, like a misrouted input, but carries no signal."""

    def __init__(self, peak: float = 0.0):
        super().__init__()
        self._peak = peak
        self.device = InputDevice(1, "Sound Blaster X3", 2, 96000.0)

    def start(self):
        return self.device

    def take_peak(self) -> float:
        return self._peak


def _run_with_capture(window, monkeypatch, tmp_path, capture):
    monkeypatch.setattr(mw.config, "load_api_key", lambda: "test-key")
    monkeypatch.setattr(mw, "AudioCapture", lambda *a, **k: capture)
    monkeypatch.setattr(mw, "LiveTranslator", _StubTranslator)
    window._update(transcript_dir=str(tmp_path))
    window.start_translation()


def test_a_device_delivering_no_signal_is_logged_not_shown(
    window, monkeypatch, tmp_path, caplog
):
    """A misrouted input goes to the log; the caption area stays clean.

    A silent input is often intentional, so nothing may cover the captions.
    """
    _run_with_capture(window, monkeypatch, tmp_path, _SilentCapture(peak=0.0))
    window._loud_at = time.monotonic() - mw.SILENT_INPUT_SECONDS - 1
    with caplog.at_level("WARNING", logger="livecap.ui.main_window"):
        window._check_input_level()
    assert any("no signal from 'Sound Blaster X3'" in r.message for r in caplog.records)
    assert window._view._status == ""  # nothing on screen
    assert window._notice == ""


def test_a_working_device_is_never_reported_as_silent(window, monkeypatch, tmp_path, caplog):
    _run_with_capture(window, monkeypatch, tmp_path, _SilentCapture(peak=0.02))
    window._loud_at = time.monotonic() - mw.SILENT_INPUT_SECONDS - 1
    with caplog.at_level("WARNING", logger="livecap.ui.main_window"):
        window._check_input_level()
    assert not caplog.records
    assert window._view._status == ""


def test_a_brief_pause_is_not_reported_as_silence(window, monkeypatch, tmp_path, caplog):
    """Nobody speaking for a second or two is normal, not a fault."""
    _run_with_capture(window, monkeypatch, tmp_path, _SilentCapture(peak=0.0))
    window._loud_at = time.monotonic() - 2.0
    with caplog.at_level("WARNING", logger="livecap.ui.main_window"):
        window._check_input_level()
    assert not caplog.records
    assert window._view._status == ""


def test_the_silence_warning_is_not_repeated_every_tick(
    window, monkeypatch, tmp_path, caplog
):
    """The tick runs ten times a second; the log line must appear once."""
    capture = _SilentCapture(peak=0.0)
    _run_with_capture(window, monkeypatch, tmp_path, capture)
    window._loud_at = time.monotonic() - mw.SILENT_INPUT_SECONDS - 1
    with caplog.at_level("WARNING", logger="livecap.ui.main_window"):
        for _ in range(20):
            window._check_input_level()
    assert sum("no signal" in r.message for r in caplog.records) == 1


def test_signal_returning_rearms_the_warning(window, monkeypatch, tmp_path):
    capture = _SilentCapture(peak=0.0)
    _run_with_capture(window, monkeypatch, tmp_path, capture)
    window._loud_at = time.monotonic() - mw.SILENT_INPUT_SECONDS - 1
    window._check_input_level()
    assert window._warned_silent is True

    capture._peak = 0.05  # someone speaks
    window._check_input_level()
    assert window._warned_silent is False


def test_no_silence_check_while_idle(window, caplog):
    with caplog.at_level("WARNING", logger="livecap.ui.main_window"):
        window._check_input_level()  # no capture at all — must not raise
    assert not caplog.records
    assert window._view._status == ""


def test_the_tick_reads_the_peak_only_once(window, monkeypatch, tmp_path):
    """take_peak() resets the meter, so two readers would each see half of it."""
    reads = []

    class _Counting(_SilentCapture):
        def take_peak(self) -> float:
            reads.append(1)
            return self._peak

    _run_with_capture(window, monkeypatch, tmp_path, _Counting(peak=0.1))
    reads.clear()
    window._on_tick()
    assert len(reads) == 1


def test_a_loud_input_tells_the_buffer_somebody_is_speaking(
    window, monkeypatch, tmp_path
):
    _run_with_capture(window, monkeypatch, tmp_path, _SilentCapture(peak=0.2))
    window._on_tick()
    assert window._buffer._quiet_since is None
    assert window._buffer._audio_known is True


def test_a_quiet_input_starts_the_clear_countdown(window, monkeypatch, tmp_path):
    _run_with_capture(window, monkeypatch, tmp_path, _SilentCapture(peak=0.0))
    window._on_tick()
    assert window._buffer._quiet_since is not None


def test_no_capture_leaves_the_level_unknown(window):
    window._on_tick()
    assert window._buffer._audio_known is False


def test_speech_keeps_the_captions_up_when_fragments_stop(
    window, monkeypatch, tmp_path
):
    """The reported symptom: captions vanished while the speaker was mid-sentence."""
    base = time.monotonic()
    monkeypatch.setattr(mw.time, "monotonic", lambda: base)
    _run_with_capture(window, monkeypatch, tmp_path, _SilentCapture(peak=0.2))
    window._on_target_text("Half of a sentence.")
    assert window._view._lines == [("Half of a sentence.", True)]

    # Six seconds pass with the speaker still talking and no fragments arriving.
    for i in range(1, 61):
        monkeypatch.setattr(mw.time, "monotonic", lambda t=base + i * 0.1: t)
        window._on_tick()
    assert window._view._lines == [("Half of a sentence.", True)]


# --- quitting with a session open -----------------------------------------


def _run_session(window, monkeypatch, tmp_path, direction):
    """Start a session in the given direction, backed by stubs."""
    stopped: list[str] = []

    class _Capture(_StubCapture):
        def stop(self):
            stopped.append("capture")

    class _Tr(_StubTranslator):
        def stop(self):
            stopped.append("translator")

    monkeypatch.setattr(mw.config, "load_api_key", lambda: "test-key")
    monkeypatch.setattr(mw, "AudioCapture", lambda *a, **k: _Capture())
    monkeypatch.setattr(mw, "LiveTranslator", lambda *a, **k: _Tr())
    window._update(transcript_dir=str(tmp_path))
    window.set_direction(direction)
    window.start_translation()
    return stopped


@pytest.mark.parametrize("direction", [mw.Direction.KO_TO_EN, mw.Direction.EN_TO_KO])
def test_quitting_mid_session_closes_everything_in_both_directions(
    window, monkeypatch, tmp_path, direction
):
    """Quitting with a live session must release the device and finish the file."""
    stopped = _run_session(window, monkeypatch, tmp_path, direction)
    window._on_target_text("A sentence with no terminator")
    path = window._writer.path

    window.shutdown()

    assert sorted(stopped) == ["capture", "translator"]
    assert window._translator is None and window._capture is None
    assert window._state is State.IDLE
    # The unterminated sentence is settled on the way out, and the footer written.
    text = path.read_text(encoding="utf-8")
    assert "A sentence with no terminator" in text
    assert "**Ended**" in text and "**Lines** 1" in text


def test_shutdown_is_idempotent(window, monkeypatch, tmp_path):
    """closeEvent and aboutToQuit can both fire; the second must do nothing."""
    stopped = _run_session(window, monkeypatch, tmp_path, mw.Direction.KO_TO_EN)
    window._on_target_text("Recorded once.")
    path = window._writer.path

    window.shutdown()
    window.shutdown()

    assert sorted(stopped) == ["capture", "translator"]  # not stopped twice
    assert path.read_text(encoding="utf-8").count("**Ended**") == 1


def test_shutdown_finishes_the_transcript_even_if_the_translator_hangs(
    window, monkeypatch, tmp_path
):
    """A translator that throws on stop must not strand the device or the file."""
    monkeypatch.setattr(mw.config, "load_api_key", lambda: "test-key")
    closed: list[str] = []

    class _Capture(_StubCapture):
        def stop(self):
            closed.append("capture")

    class _Broken(_StubTranslator):
        def stop(self):
            raise RuntimeError("thread will not join")

    monkeypatch.setattr(mw, "AudioCapture", lambda *a, **k: _Capture())
    monkeypatch.setattr(mw, "LiveTranslator", lambda *a, **k: _Broken())
    window._update(transcript_dir=str(tmp_path))
    window.start_translation()
    window._on_target_text("Survives the broken translator.")
    path = window._writer.path

    window.shutdown()  # must not raise

    assert closed == ["capture"]
    text = path.read_text(encoding="utf-8")
    assert "Survives the broken translator." in text
    assert "**Ended**" in text


def test_close_event_shuts_down(window, monkeypatch, tmp_path):
    stopped = _run_session(window, monkeypatch, tmp_path, mw.Direction.EN_TO_KO)
    window.close()
    assert sorted(stopped) == ["capture", "translator"]


# --- caption position -----------------------------------------------------


def test_caption_position_menu_exists_with_all_three(window):
    labels = [a.text() for a in find_menu(window, "Settings").actions() if a.menu()]
    assert "Caption Position" in labels
    items = [a.text() for a in window._position_actions.values()]
    assert items == ["Top", "Middle", "Bottom"]


def test_choosing_a_position_updates_the_setting(window):
    window._position_actions[mw.BoxPosition.TOP].trigger()
    assert window._settings.box_position == "top"
    window._position_actions[mw.BoxPosition.CENTER].trigger()
    assert window._settings.box_position == "center"


def test_the_position_menu_reflects_the_current_setting(window):
    window._update(box_position="top")
    checked = [p for p, a in window._position_actions.items() if a.isChecked()]
    assert checked == [mw.BoxPosition.TOP]


def test_bottom_is_checked_by_default(window):
    checked = [p for p, a in window._position_actions.items() if a.isChecked()]
    assert checked == [mw.BoxPosition.BOTTOM]


def test_changing_position_reaches_the_view(window):
    window._update(box_position="top")
    assert window._view._settings.box_position == "top"


def test_changing_position_does_not_restart_a_session(window, monkeypatch, tmp_path):
    """It is a cosmetic change; a live session must not be interrupted."""
    made, events = _running_window(window, monkeypatch, tmp_path)
    window._update(box_position="top")
    window._apply_session_changes(replace(window._settings, box_position="bottom"))
    assert len(made) == 1
    assert "translator-stop" not in events


# --- about ----------------------------------------------------------------


def test_about_names_the_author(window, monkeypatch):
    shown = {}
    monkeypatch.setattr(
        mw.QMessageBox, "about", lambda _p, title, text: shown.update(title=title, text=text)
    )
    window._show_about()
    assert "Pyeongwoo Lee" in shown["text"]
    assert "dennis2.lee@gmail.com" in shown["text"]


def test_about_reports_the_package_version_and_model(window, monkeypatch):
    from livecap import __version__

    shown = {}
    monkeypatch.setattr(
        mw.QMessageBox, "about", lambda _p, title, text: shown.update(title=title, text=text)
    )
    window._show_about()
    assert __version__ in shown["text"]
    assert mw.MODEL in shown["text"]


def test_about_action_is_in_the_app_menu(window):
    labels = [a.text() for a in find_menu(window, "LiveCap").actions()]
    assert "About LiveCap" in labels
