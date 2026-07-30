"""Preferences dialog — now the single place everything is configured."""

from __future__ import annotations

import os

os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")

import pytest  # noqa: E402
from PyQt6.QtWidgets import QApplication  # noqa: E402

from livecap import config, credentials  # noqa: E402
from livecap.config import Settings  # noqa: E402
from livecap.ui import settings_dialog as sd  # noqa: E402


@pytest.fixture(scope="session")
def qapp():
    yield QApplication.instance() or QApplication([])


@pytest.fixture
def dialog(qapp, monkeypatch):
    monkeypatch.setattr(sd, "list_input_devices", lambda: [])
    dlg = sd.SettingsDialog(Settings())
    yield dlg
    dlg._stop_meter()
    dlg.close()


# --- the meter scale ------------------------------------------------------


def test_silence_reads_as_nothing_on_the_meter():
    assert sd.peak_to_percent(0.0) == 0
    assert "no signal" in sd.peak_verdict(0.0)


def test_a_dead_input_is_called_silent():
    # -88 dBFS, the level measured from an input with nothing routed into it.
    assert "silent" in sd.peak_verdict(10 ** (-88 / 20))


def test_a_quiet_source_is_called_quiet():
    assert "quiet" in sd.peak_verdict(10 ** (-40 / 20))


def test_a_healthy_level_is_called_good():
    assert "good" in sd.peak_verdict(10 ** (-12 / 20))


def test_a_clipping_level_is_called_too_loud():
    assert "too loud" in sd.peak_verdict(10 ** (-1 / 20))


def test_the_meter_scale_rises_with_the_level():
    values = [sd.peak_to_percent(10 ** (db / 20)) for db in (-60, -40, -20, -6, 0)]
    assert values == sorted(values)
    assert values[0] == 0 and values[-1] == 100


def test_the_meter_never_leaves_its_range():
    for peak in (0.0, 1e-9, 0.5, 1.0, 4.0):
        assert 0 <= sd.peak_to_percent(peak) <= 100


# --- the level test -------------------------------------------------------


class _FakeCapture:
    def __init__(self, *a, peak=0.5, **k):
        self._peak = peak
        self.stopped = False
        from livecap.audio import InputDevice

        self.device = InputDevice(0, "Fake Mic", 1, 48000.0)

    def start(self):
        return self.device

    def stop(self):
        self.stopped = True

    def take_peak(self):
        return self._peak


def test_starting_the_level_test_opens_the_device(dialog, monkeypatch):
    made = []

    def factory(*a, **k):
        cap = _FakeCapture()
        made.append(cap)
        return cap

    monkeypatch.setattr(sd, "AudioCapture", factory)
    dialog._start_meter()
    assert dialog._capture is not None
    assert "Fake Mic" in dialog._meter_label.text()
    assert dialog._meter_btn.text() == "Stop Level Test"


def test_the_meter_follows_the_level(dialog, monkeypatch):
    monkeypatch.setattr(sd, "AudioCapture", lambda *a, **k: _FakeCapture(peak=10 ** (-12 / 20)))
    dialog._start_meter()
    dialog._poll_meter()
    assert dialog._meter.value() > 50
    assert "good" in dialog._meter_label.text()


def test_stopping_the_level_test_releases_the_device(dialog, monkeypatch):
    cap = _FakeCapture()
    monkeypatch.setattr(sd, "AudioCapture", lambda *a, **k: cap)
    dialog._start_meter()
    dialog._stop_meter()
    assert cap.stopped is True
    assert dialog._capture is None
    assert dialog._meter.value() == 0


def test_closing_the_dialog_never_leaves_the_microphone_open(dialog, monkeypatch):
    cap = _FakeCapture()
    monkeypatch.setattr(sd, "AudioCapture", lambda *a, **k: cap)
    dialog._start_meter()
    dialog.reject()
    assert cap.stopped is True


def test_a_device_that_will_not_open_is_reported(dialog, monkeypatch):
    class _Broken(_FakeCapture):
        def start(self):
            raise RuntimeError("No audio input device found")

    monkeypatch.setattr(sd, "AudioCapture", lambda *a, **k: _Broken())
    dialog._start_meter()
    assert dialog._capture is None
    assert "No audio input device" in dialog._meter_label.text()


# --- the API key ----------------------------------------------------------


def test_typing_a_key_writes_it_encrypted(dialog):
    dialog._key_edit.setText("AIzaTypedIntoTheDialog")
    dialog._accept()
    assert config.load_api_key() == "AIzaTypedIntoTheDialog"
    assert b"AIzaTypedIntoTheDialog" not in credentials.store_path().read_bytes()


def test_leaving_the_field_blank_keeps_the_stored_key(dialog):
    credentials.put("GEMINI_API_KEY", "AIzaAlreadyStored")
    dialog._accept()
    assert config.load_api_key() == "AIzaAlreadyStored"


def test_deleting_clears_the_stored_key(dialog):
    credentials.put("GEMINI_API_KEY", "AIzaAlreadyStored")
    dialog._clear_key()
    assert config.load_api_key() is None
    assert "deleted" in dialog._key_status.text()


def test_testing_without_a_key_says_so(dialog):
    dialog._test_key()
    assert "No key to test" in dialog._key_status.text()


def test_a_failed_key_test_is_reported(dialog):
    dialog._key_tested(False, "API key not valid")
    assert "not valid" in dialog._key_status.text()
    assert dialog._key_status.text().startswith("✗")


def test_a_successful_key_test_is_reported(dialog):
    dialog._key_tested(True, "Key works")
    assert dialog._key_status.text().startswith("✓")


def test_the_key_field_is_masked_until_asked(dialog):
    from PyQt6.QtWidgets import QLineEdit

    assert dialog._key_edit.echoMode() == QLineEdit.EchoMode.Password
    dialog._show_key.setChecked(True)
    assert dialog._key_edit.echoMode() == QLineEdit.EchoMode.Normal


# --- settings round-trip --------------------------------------------------


def test_colours_reach_the_result(dialog):
    dialog._bg_color._color.setNamedColor("#123456")
    dialog._text_color._color.setNamedColor("#ABCDEF")
    result = dialog.result_settings()
    assert result.bg_color == "#123456"
    assert result.text_color == "#ABCDEF"


def test_caption_position_reaches_the_result(dialog):
    dialog._position.setCurrentIndex(dialog._position.findData("top"))
    assert dialog.result_settings().box_position == "top"


def test_debug_logging_reaches_the_result(dialog):
    dialog._debug_logging.setChecked(True)
    assert dialog.result_settings().debug_logging is True


def test_everything_the_old_dotenv_carried_is_now_in_the_dialog(dialog):
    """.env held the API key and the debug flag; both must be settable here."""
    assert dialog._key_edit is not None
    assert dialog._debug_logging is not None


def test_unchanged_settings_survive_a_round_trip(dialog):
    original = Settings()
    result = dialog.result_settings()
    assert result == original


def test_the_line_hold_reaches_the_result(dialog):
    dialog._line_hold.setValue(15)  # 0.1 s steps
    assert dialog.result_settings().line_hold_seconds == 1.5


def test_the_clear_delay_reaches_the_result(dialog):
    dialog._clear_after.setValue(25)
    assert dialog.result_settings().clear_after_seconds == 2.5


# --- vocabulary hints -----------------------------------------------------


def test_vocabulary_terms_reach_the_result(dialog):
    dialog._vocab_source.setPlainText("영업이익률\n평우")
    dialog._vocab_captions.setPlainText("operating margin\nPyeongwoo Lee")
    result = dialog.result_settings()
    assert result.source_terms == ["영업이익률", "평우"]
    assert result.caption_terms == ["operating margin", "Pyeongwoo Lee"]


def test_blank_lines_and_duplicates_are_dropped(dialog):
    dialog._vocab_source.setPlainText("  first  \n\n  \nsecond\nfirst\n")
    assert dialog.result_settings().source_terms == ["first", "second"]


def test_the_two_lists_are_independent(dialog):
    """They are different languages, so one must not leak into the other."""
    dialog._vocab_source.setPlainText("한국어 용어")
    assert dialog.result_settings().caption_terms == []


def test_every_vocabulary_control_has_a_tooltip(dialog):
    """These knobs are meaningless without an explanation of what they do."""
    for widget in (dialog._vocab_source, dialog._vocab_captions):
        tip = widget.toolTip()
        assert len(tip) > 60, f"{widget} has no useful tooltip"


def test_the_tooltips_name_the_api_fields_they_set(dialog):
    assert "custom_vocabulary" in dialog._vocab_source.toolTip()
    assert "custom_vocabulary" in dialog._vocab_captions.toolTip()


def test_the_dialog_has_no_deprecated_adaptation_phrases_control(dialog):
    """Removed: the SDK describes it identically to custom_vocabulary."""
    assert not hasattr(dialog, "_adaptation_phrases")


# --- Apply, OK and Cancel -------------------------------------------------


def test_the_dialog_offers_apply_ok_and_cancel(dialog):
    """Clicking the real button, not calling the slot, so the wiring is covered."""
    from PyQt6.QtWidgets import QDialogButtonBox

    box = dialog.findChild(QDialogButtonBox)
    standard = {
        box.standardButton(b) for b in box.buttons()
    }
    assert standard == {
        QDialogButtonBox.StandardButton.Ok,
        QDialogButtonBox.StandardButton.Apply,
        QDialogButtonBox.StandardButton.Cancel,
    }

    seen: list[Settings] = []
    dialog.applied.connect(seen.append)
    dialog._font_size.setValue(56)
    box.button(QDialogButtonBox.StandardButton.Apply).click()
    assert [s.font_size for s in seen] == [56]


def test_apply_emits_the_settings_without_closing(dialog):
    seen: list[Settings] = []
    dialog.applied.connect(seen.append)
    dialog._font_size.setValue(72)
    dialog.apply_now()
    assert [s.font_size for s in seen] == [72]
    assert dialog.isVisible() is False  # never shown, but it was not accepted
    assert dialog.result() == 0


def test_ok_does_not_emit_applied_twice(dialog):
    """The window applies OK's result itself, so the signal must not double up."""
    seen: list[Settings] = []
    dialog.applied.connect(seen.append)
    dialog._font_size.setValue(56)
    dialog._accept()
    assert seen == []


def test_apply_then_a_further_change_only_emits_the_newer_value(dialog):
    seen: list[Settings] = []
    dialog.applied.connect(seen.append)
    dialog._font_size.setValue(32)
    dialog.apply_now()
    dialog._font_size.setValue(96)
    dialog.apply_now()
    assert [s.font_size for s in seen] == [32, 96]


def test_apply_writes_the_typed_key(dialog):
    dialog._key_edit.setText("AIzaAppliedNotAccepted")
    dialog.apply_now()
    assert config.load_api_key() == "AIzaAppliedNotAccepted"


def test_cancel_puts_back_the_key_that_apply_replaced(dialog):
    credentials.put("GEMINI_API_KEY", "AIzaTheOriginalKey")
    # Reopened, so it takes the stored key as its baseline.
    reopened = sd.SettingsDialog(Settings())
    try:
        reopened._key_edit.setText("AIzaTypedByMistake")
        reopened.apply_now()
        assert config.load_api_key() == "AIzaTypedByMistake"

        reopened.reject()
        assert config.load_api_key() == "AIzaTheOriginalKey"
    finally:
        reopened.close()


def test_cancel_removes_a_key_that_had_none_before(dialog):
    assert config.stored_api_key() is None
    dialog._key_edit.setText("AIzaTypedByMistake")
    dialog.apply_now()
    assert config.load_api_key() == "AIzaTypedByMistake"

    dialog.reject()
    assert config.load_api_key() is None


def test_cancel_without_apply_leaves_the_key_alone(dialog):
    credentials.put("GEMINI_API_KEY", "AIzaUntouched")
    dialog.reject()
    assert config.load_api_key() == "AIzaUntouched"


def test_ok_after_apply_keeps_the_key(dialog):
    dialog._key_edit.setText("AIzaMeantIt")
    dialog.apply_now()
    dialog._accept()
    assert config.load_api_key() == "AIzaMeantIt"


def test_cancel_still_releases_the_microphone_after_an_apply(dialog, monkeypatch):
    cap = _FakeCapture()
    monkeypatch.setattr(sd, "AudioCapture", lambda *a, **k: cap)
    dialog._start_meter()
    dialog.apply_now()
    dialog.reject()
    assert cap.stopped is True


# --- the speed test -------------------------------------------------------


def test_the_network_tab_exists(dialog):
    from PyQt6.QtWidgets import QTabWidget

    tabs = dialog.findChild(QTabWidget)
    titles = [tabs.tabText(i) for i in range(tabs.count())]
    assert titles == [
        "API Key",
        "Audio",
        "Network",
        "Vocabulary",
        "Appearance",
        "Behaviour",
    ]


def _report(up_kbps: float, latency_ms: list[float]):
    from livecap import netcheck

    report = netcheck.Report()
    report.latency = netcheck.Latency(samples_ms=list(latency_ms))
    for flow, kbps in ((report.upload, up_kbps), (report.download, 50_000.0)):
        flow.seconds = 1.0
        flow.bytes_moved = int(kbps * 1000.0 / 8.0)
    return report


def test_a_good_result_is_shown_in_green(dialog):
    dialog._speed_tested(_report(20_000.0, [20.0]))
    assert dialog._net_verdict.text().startswith("Good")
    assert "green" in dialog._net_verdict.styleSheet()
    assert "Mbit/s" in dialog._net_detail.text()


def test_a_connection_too_slow_for_audio_is_shown_in_red(dialog):
    dialog._speed_tested(_report(120.0, [20.0]))
    assert dialog._net_verdict.text().startswith("Too slow")
    assert "C0392B" in dialog._net_verdict.styleSheet()


def test_the_detail_lists_every_measurement(dialog):
    dialog._speed_tested(_report(20_000.0, [20.0, 30.0]))
    lines = dialog._net_detail.text().splitlines()
    assert len(lines) == 3
    assert "Latency" in lines[0]
    assert lines[1].startswith("Upload")
    assert lines[2].startswith("Download")


def test_the_speed_test_button_is_re_enabled_afterwards(dialog):
    dialog._net_btn.setEnabled(False)
    dialog._speed_test_finished()
    assert dialog._net_btn.isEnabled() is True
    assert dialog._net_thread is None


def test_closing_waits_for_a_running_worker(dialog, monkeypatch):
    """A QThread destroyed while still running crashes, so closing must settle it."""
    from PyQt6.QtCore import QThread

    class _SlowThread(QThread):
        def __init__(self):
            super().__init__(dialog)
            self.quit_called = False
            self.waited = 0

        def quit(self):
            self.quit_called = True

        def wait(self, ms=None):
            self.waited = ms
            return True  # finished in time

    thread = _SlowThread()
    dialog._net_thread = thread
    dialog._settle_workers()
    assert thread.quit_called is True
    assert thread.waited == sd.WORKER_WAIT_MS


def test_a_worker_that_overruns_is_detached_rather_than_destroyed(dialog):
    from PyQt6.QtCore import QThread

    class _Stuck(QThread):
        def quit(self):
            pass

        def wait(self, ms=None):
            return False  # never finishes in time

    thread = _Stuck(dialog)
    dialog._net_thread = thread
    dialog._settle_workers()
    assert dialog._net_thread is None
    assert thread.parent() is None
    assert thread in sd._DETACHED_WORKERS
    sd._DETACHED_WORKERS.discard(thread)


def test_a_speed_test_failure_is_a_message_not_a_crash(dialog, monkeypatch):
    from livecap import netcheck

    monkeypatch.setattr(netcheck, "run_check", lambda: (_ for _ in ()).throw(OSError("down")))
    worker = sd._NetTest()
    seen: list[object] = []
    worker.done.connect(seen.append)
    worker.run()
    assert len(seen) == 1
    dialog._speed_tested(seen[0])
    assert "No connection" in dialog._net_verdict.text()
