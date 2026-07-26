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

    def take_peak(self) -> float:
        return self._peak


def _run_with_capture(window, monkeypatch, tmp_path, capture):
    monkeypatch.setattr(mw.config, "load_api_key", lambda: "test-key")
    monkeypatch.setattr(mw, "AudioCapture", lambda *a, **k: capture)
    monkeypatch.setattr(mw, "LiveTranslator", _StubTranslator)
    window._update(transcript_dir=str(tmp_path))
    window.start_translation()


def test_a_device_delivering_no_signal_is_reported(window, monkeypatch, tmp_path):
    """A misrouted input looks exactly like a quiet room without this."""
    _run_with_capture(window, monkeypatch, tmp_path, _SilentCapture(peak=0.0))
    window._loud_at = time.monotonic() - mw.SILENT_INPUT_SECONDS - 1
    window._check_input_level()
    assert "No audio from Sound Blaster X3" in window._view._status


def test_a_working_device_is_never_reported_as_silent(window, monkeypatch, tmp_path):
    _run_with_capture(window, monkeypatch, tmp_path, _SilentCapture(peak=0.02))
    window._loud_at = time.monotonic() - mw.SILENT_INPUT_SECONDS - 1
    window._check_input_level()
    assert window._view._status == ""


def test_a_brief_pause_is_not_reported_as_silence(window, monkeypatch, tmp_path):
    """Nobody speaking for a second or two is normal, not a fault."""
    _run_with_capture(window, monkeypatch, tmp_path, _SilentCapture(peak=0.0))
    window._loud_at = time.monotonic() - 2.0
    window._check_input_level()
    assert window._view._status == ""


def test_the_silence_warning_is_not_repeated_every_tick(window, monkeypatch, tmp_path):
    """The tick runs ten times a second; the warning must be raised once."""
    capture = _SilentCapture(peak=0.0)
    _run_with_capture(window, monkeypatch, tmp_path, capture)

    notices: list[str] = []
    monkeypatch.setattr(
        mw.CaptionWindow, "_show_notice", lambda _self, text: notices.append(text)
    )
    window._loud_at = time.monotonic() - mw.SILENT_INPUT_SECONDS - 1
    for _ in range(20):
        window._check_input_level()
    assert len(notices) == 1


def test_signal_returning_rearms_the_warning(window, monkeypatch, tmp_path):
    capture = _SilentCapture(peak=0.0)
    _run_with_capture(window, monkeypatch, tmp_path, capture)
    window._loud_at = time.monotonic() - mw.SILENT_INPUT_SECONDS - 1
    window._check_input_level()
    assert window._warned_silent is True

    capture._peak = 0.05  # someone speaks
    window._check_input_level()
    assert window._warned_silent is False


def test_no_silence_check_while_idle(window):
    window._check_input_level()  # no capture at all — must not raise
    assert window._view._status == ""


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
