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
