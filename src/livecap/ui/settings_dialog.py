"""Preferences dialog — the single place everything is configured.

Apply applies without closing, OK applies and closes, and Cancel puts back the
settings that were in force when the dialog opened — including undoing a key
Apply had already written.

The API key is written to the encrypted credential store, never into Settings
and never to a `.env` file.
"""

from __future__ import annotations

from dataclasses import replace

from PyQt6.QtCore import QObject, Qt, QThread, QTimer, pyqtSignal
from PyQt6.QtGui import QColor, QFontMetrics
from PyQt6.QtWidgets import (
    QCheckBox,
    QColorDialog,
    QComboBox,
    QDialog,
    QDialogButtonBox,
    QFileDialog,
    QFormLayout,
    QHBoxLayout,
    QLabel,
    QLineEdit,
    QPlainTextEdit,
    QProgressBar,
    QPushButton,
    QSlider,
    QSpinBox,
    QTabWidget,
    QVBoxLayout,
    QWidget,
)

from .. import config, netcheck
from ..audio import AudioCapture, list_input_devices
from ..caption_buffer import Policy
from ..config import BOX_POSITION_MENU_TEXT, BoxPosition, Settings
from ..transcript import DEFAULT_DIR, resolve_dir
from ..util import logging as log

_log = log.get(__name__)

POLICY_LABELS = {
    Policy.AUTO: "Detect automatically",
    Policy.INCREMENTAL: "A — incremental",
    Policy.UTTERANCE: "B — per utterance",
}

# How long closing Preferences waits for a worker to finish. Neither the key
# test nor the speed test can be cancelled — both are blocking calls — so a
# worker that overruns is detached instead, because destroying a QThread while
# it is still running crashes.
WORKER_WAIT_MS = 3000
_DETACHED_WORKERS: set[QThread] = set()

# Width reserved for the Network tab's result labels, wide enough that most
# verdicts fit on one line without forcing the dialog uncomfortably wide.
NET_LABEL_WIDTH = 460

METER_MS = 100
# Peak in dBFS mapped onto the meter. Below the floor there is nothing to see;
# above the ceiling the signal is about to clip.
METER_FLOOR_DB = -60.0
METER_CEILING_DB = 0.0
METER_GOOD_DB = -24.0  # comfortably above the noise floor of a live mic


def peak_to_percent(peak: float) -> int:
    """Map a 0-1 peak onto the meter's dBFS scale."""
    if peak <= 0.0:
        return 0
    import math

    db = 20.0 * math.log10(peak)
    span = METER_CEILING_DB - METER_FLOOR_DB
    return max(0, min(100, int((db - METER_FLOOR_DB) / span * 100)))


def peak_verdict(peak: float) -> str:
    """Plain-language reading of a level, for the label under the meter."""
    if peak <= 0.0:
        return "no signal"
    import math

    db = 20.0 * math.log10(peak)
    if db < METER_FLOOR_DB:
        return f"{db:.0f} dBFS — silent, check the input"
    if db > -3.0:
        return f"{db:.0f} dBFS — too loud, turn the source down"
    if db >= METER_GOOD_DB:
        return f"{db:.0f} dBFS — good"
    return f"{db:.0f} dBFS — quiet, turn the source up"


class _KeyTest(QObject):
    """Checks the key off the UI thread. Listing models is free — nothing is billed."""

    done = pyqtSignal(bool, str)

    def __init__(self, api_key: str) -> None:
        super().__init__()
        self._api_key = api_key

    def run(self) -> None:
        try:
            from google import genai

            from ..translator import MODEL

            # Bind the client to a name: models.list() is a lazy pager, and an
            # inline client closes itself mid-iteration.
            client = genai.Client(api_key=self._api_key)
            names = [m.name for m in client.models.list()]
        except Exception as exc:  # noqa: BLE001 — every failure is a message
            self.done.emit(False, f"{type(exc).__name__}: {exc}"[:200])
            return
        if any(MODEL in name for name in names):
            self.done.emit(
                True,
                f"Key works — {len(names)} models visible, including the translation model.",
            )
        else:
            self.done.emit(
                False,
                f"Key is valid ({len(names)} models) but {MODEL} is not available to it. "
                "That model needs a paid-tier project.",
            )


class _NetTest(QObject):
    """Runs the speed test off the UI thread. Takes several seconds."""

    done = pyqtSignal(object)  # a netcheck.Report

    def run(self) -> None:
        try:
            report = netcheck.run_check()
        except Exception as exc:  # noqa: BLE001 — every failure is a message
            report = netcheck.Report()
            report.latency.error = f"{type(exc).__name__}: {exc}"[:120]
        self.done.emit(report)


class _ColorButton(QPushButton):
    """A button showing a colour, which opens the picker when clicked."""

    def __init__(self, value: str, title: str, *, alpha: bool = False,
                 parent: QWidget | None = None) -> None:
        super().__init__(parent)
        self._title = title
        self._alpha = alpha
        self._color = QColor(value)
        self._opacity = 1.0
        self.setFixedWidth(120)
        self.clicked.connect(self._pick)
        self._refresh()

    def _refresh(self) -> None:
        name = self._color.name(QColor.NameFormat.HexRgb).upper()
        # Keep the label readable whichever colour was chosen.
        ink = "#000000" if self._color.lightness() > 127 else "#FFFFFF"
        self.setStyleSheet(
            f"background-color: {name}; color: {ink}; padding: 6px; border: 1px solid gray;"
        )
        self.setText(name)

    def _pick(self) -> None:
        opts = (
            QColorDialog.ColorDialogOption.ShowAlphaChannel
            if self._alpha
            else QColorDialog.ColorDialogOption(0)
        )
        start = QColor(self._color)
        if self._alpha:
            start.setAlphaF(self._opacity)
        chosen = QColorDialog.getColor(start, self, self._title, opts)
        if not chosen.isValid():
            return
        self._color = chosen
        if self._alpha:
            self._opacity = chosen.alphaF()
        self._refresh()

    def value(self) -> str:
        return self._color.name(QColor.NameFormat.HexRgb).upper()

    def opacity(self) -> float:
        return self._opacity

    def set_opacity(self, value: float) -> None:
        self._opacity = value


class SettingsDialog(QDialog):
    """Preferences. Emits `applied` whenever Apply or OK is pressed."""

    applied = pyqtSignal(object)  # a Settings

    def __init__(self, settings: Settings, parent: QWidget | None = None) -> None:
        super().__init__(parent)
        self.setWindowTitle("LiveCap Preferences")
        self.setModal(True)
        self._settings = settings
        self._capture: AudioCapture | None = None
        self._key_thread: QThread | None = None
        self._net_thread: QThread | None = None
        # Taken before anything is written, so Cancel can undo an Apply.
        self._key_at_open = config.stored_api_key()
        self._key_written = False

        tabs = QTabWidget()
        tabs.addTab(self._api_tab(), "API Key")
        tabs.addTab(self._audio_tab(settings), "Audio")
        tabs.addTab(self._network_tab(), "Network")
        tabs.addTab(self._vocabulary_tab(settings), "Vocabulary")
        tabs.addTab(self._appearance_tab(settings), "Appearance")
        tabs.addTab(self._behaviour_tab(settings), "Behaviour")

        buttons = QDialogButtonBox(
            QDialogButtonBox.StandardButton.Ok
            | QDialogButtonBox.StandardButton.Apply
            | QDialogButtonBox.StandardButton.Cancel
        )
        buttons.accepted.connect(self._accept)
        buttons.rejected.connect(self.reject)
        self._apply_btn = buttons.button(QDialogButtonBox.StandardButton.Apply)
        self._apply_btn.setToolTip("Apply the changes and keep this window open")
        self._apply_btn.clicked.connect(self.apply_now)

        root = QVBoxLayout(self)
        root.addWidget(tabs)
        root.addWidget(buttons)

    # --- API key ----------------------------------------------------------

    def _api_tab(self) -> QWidget:
        form = QFormLayout()

        self._key_edit = QLineEdit()
        self._key_edit.setEchoMode(QLineEdit.EchoMode.Password)
        self._key_edit.setMinimumWidth(360)
        source = config.api_key_source()
        self._key_edit.setPlaceholderText(
            f"Stored in the {source} — type a new key to replace it"
            if source
            else "AIza…"
        )
        form.addRow("Gemini API key", self._key_edit)

        self._show_key = QCheckBox("Show what I type")
        self._show_key.toggled.connect(
            lambda on: self._key_edit.setEchoMode(
                QLineEdit.EchoMode.Normal if on else QLineEdit.EchoMode.Password
            )
        )
        form.addRow("", self._show_key)

        row = QHBoxLayout()
        self._test_btn = QPushButton("Test Key")
        self._test_btn.setToolTip(
            "Lists the models the key can reach. Free — it does not open a session."
        )
        self._test_btn.clicked.connect(self._test_key)
        row.addWidget(self._test_btn)

        clear_btn = QPushButton("Delete Stored Key")
        clear_btn.setToolTip("Remove the key from the encrypted store and the Keychain")
        clear_btn.clicked.connect(self._clear_key)
        row.addWidget(clear_btn)
        row.addStretch(1)
        holder = QWidget()
        holder.setLayout(row)
        form.addRow("", holder)

        self._key_status = QLabel(
            f"Currently reading the key from the {source}." if source else "No key stored yet."
        )
        self._key_status.setWordWrap(True)
        form.addRow("", self._key_status)

        self._also_keychain = QCheckBox("Also store in the macOS Keychain")
        self._also_keychain.setToolTip(
            "The Keychain is protected by your login password, so it is the stronger\n"
            "of the two. The encrypted file is written either way."
        )
        form.addRow("", self._also_keychain)

        note = QLabel(
            "The key is encrypted before it is written to disk. The passphrase is part of "
            "the application, so treat this as keeping the key out of plain sight rather "
            "than as real secrecy — the Keychain is stronger. A GEMINI_API_KEY environment "
            "variable still takes precedence over both. Cancel puts the encrypted store back "
            "as it was, but a copy already made in the Keychain stays there."
        )
        note.setWordWrap(True)
        note.setStyleSheet("color: gray;")
        form.addRow("", note)

        page = QWidget()
        page.setLayout(form)
        return page

    def _test_key(self) -> None:
        typed = self._key_edit.text().strip()
        api_key = typed or config.load_api_key()
        if not api_key:
            self._key_status.setText("No key to test — type one above first.")
            return
        if self._key_thread is not None:
            return  # a test is already running

        self._test_btn.setEnabled(False)
        self._key_status.setText("Testing…")

        thread = QThread(self)
        worker = _KeyTest(api_key)
        worker.moveToThread(thread)
        thread.started.connect(worker.run)
        worker.done.connect(self._key_tested)
        worker.done.connect(thread.quit)
        thread.finished.connect(worker.deleteLater)
        thread.finished.connect(self._key_test_finished)
        self._key_thread = thread
        self._key_worker = worker  # keep a reference, or it is collected mid-flight
        thread.start()

    def _key_tested(self, ok: bool, message: str) -> None:
        self._key_status.setText(("✓ " if ok else "✗ ") + message)
        self._key_status.setStyleSheet("color: green;" if ok else "color: #C0392B;")

    def _key_test_finished(self) -> None:
        self._test_btn.setEnabled(True)
        self._key_thread = None

    def _clear_key(self) -> None:
        config.clear_api_key()
        self._key_edit.clear()
        self._key_edit.setPlaceholderText("AIza…")
        self._key_status.setText("Stored key deleted.")
        self._key_status.setStyleSheet("color: gray;")

    # --- audio ------------------------------------------------------------

    def _audio_tab(self, settings: Settings) -> QWidget:
        form = QFormLayout()

        self._device = QComboBox()
        self._device.addItem("System default input", "")
        for dev in list_input_devices():
            self._device.addItem(
                f"{dev.name} — {dev.channels}ch / {dev.default_samplerate:.0f} Hz", dev.name
            )
        idx = self._device.findData(settings.audio_device_name)
        self._device.setCurrentIndex(idx if idx >= 0 else 0)
        self._device.currentIndexChanged.connect(self._device_changed)
        form.addRow("Audio input", self._device)

        self._meter = QProgressBar()
        self._meter.setRange(0, 100)
        self._meter.setTextVisible(False)
        self._meter.setMinimumWidth(300)
        form.addRow("Input level", self._meter)

        self._meter_label = QLabel("Not running.")
        form.addRow("", self._meter_label)

        self._meter_btn = QPushButton("Start Level Test")
        self._meter_btn.setToolTip(
            "Open the selected device and show its level. Speak, or play the source,\n"
            "and aim for the green band."
        )
        self._meter_btn.clicked.connect(self._toggle_meter)
        form.addRow("", self._meter_btn)

        hint = QLabel(
            "Aim for peaks around -12 dBFS. A device with nothing routed into it sits near "
            "-88 dBFS, while a live microphone in a quiet room reads about -36."
        )
        hint.setWordWrap(True)
        hint.setStyleSheet("color: gray;")
        form.addRow("", hint)

        self._meter_timer = QTimer(self)
        self._meter_timer.timeout.connect(self._poll_meter)

        page = QWidget()
        page.setLayout(form)
        return page

    def _device_changed(self) -> None:
        if self._capture is not None:
            self._stop_meter()
            self._start_meter()

    def _toggle_meter(self) -> None:
        if self._capture is None:
            self._start_meter()
        else:
            self._stop_meter()

    def _start_meter(self) -> None:
        name = self._device.currentData() or ""
        capture = AudioCapture(device_name=name)
        try:
            dev = capture.start()
        except (RuntimeError, OSError, ValueError) as exc:
            self._meter_label.setText(f"Could not open the device: {exc}")
            self._meter_label.setStyleSheet("color: #C0392B;")
            return
        self._capture = capture
        self._meter_btn.setText("Stop Level Test")
        self._meter_label.setStyleSheet("")
        self._meter_label.setText(f"Listening to {dev.name}…")
        self._meter_timer.start(METER_MS)

    def _stop_meter(self) -> None:
        self._meter_timer.stop()
        capture, self._capture = self._capture, None
        if capture is not None:
            try:
                capture.stop()
            except Exception:  # noqa: BLE001 — closing a meter must not raise
                _log.exception("error stopping the level meter")
        self._meter.setValue(0)
        self._meter_btn.setText("Start Level Test")
        self._meter_label.setText("Not running.")
        self._meter_label.setStyleSheet("")

    def _poll_meter(self) -> None:
        if self._capture is None:
            return
        peak = self._capture.take_peak()
        self._meter.setValue(peak_to_percent(peak))
        self._meter_label.setText(peak_verdict(peak))

    # --- network ----------------------------------------------------------

    def _network_tab(self) -> QWidget:
        form = QFormLayout()

        self._net_btn = QPushButton("Run Speed Test")
        self._net_btn.setToolTip(
            "Times TCP handshakes to the Live API host, then transfers a few\n"
            "megabytes of zeros to measure upload and download speed."
        )
        self._net_btn.clicked.connect(self._run_speed_test)
        form.addRow("", self._net_btn)

        self._net_verdict = QLabel("Not run yet.")
        self._net_verdict.setWordWrap(True)
        # QFormLayout fixes a wrapped label's row height to whatever its text
        # needs at construction time — "Not run yet." is one line. The real
        # verdict can run to two, and without reserving room for that up front,
        # the second line is clipped rather than the row growing to fit it.
        self._size_for_wrapped_lines(self._net_verdict, lines=2, width=NET_LABEL_WIDTH)
        form.addRow("Result", self._net_verdict)

        self._net_detail = QLabel("")
        self._net_detail.setWordWrap(True)
        self._net_detail.setTextInteractionFlags(Qt.TextInteractionFlag.TextSelectableByMouse)
        # Three measurements, and the latency line alone can wrap on its own —
        # same reservation, sized for the worst case rather than the empty start.
        self._size_for_wrapped_lines(self._net_detail, lines=5, width=NET_LABEL_WIDTH)
        form.addRow("Detail", self._net_detail)

        note = QLabel(
            f"A session sends a steady {netcheck.AUDIO_KBPS:.0f} kbit/s of audio upstream, so "
            "upload speed and latency matter far more than download speed. Latency is measured "
            f"against {netcheck.LIVE_API_HOST} itself and costs nothing — no session is opened "
            f"and no key is needed. Throughput uses the public {netcheck.SPEED_HOST} endpoints; "
            "the payload is zeros, so no audio, transcript or key material leaves this machine."
        )
        note.setWordWrap(True)
        note.setStyleSheet("color: gray;")
        form.addRow("", note)

        page = QWidget()
        page.setLayout(form)
        return page

    def _run_speed_test(self) -> None:
        if self._net_thread is not None:
            return  # already running
        self._net_btn.setEnabled(False)
        self._net_verdict.setText("Measuring… this takes a few seconds.")
        self._net_verdict.setStyleSheet("")
        self._net_detail.setText("")

        thread = QThread(self)
        worker = _NetTest()
        worker.moveToThread(thread)
        thread.started.connect(worker.run)
        worker.done.connect(self._speed_tested)
        worker.done.connect(thread.quit)
        thread.finished.connect(worker.deleteLater)
        thread.finished.connect(self._speed_test_finished)
        self._net_thread = thread
        self._net_worker = worker  # keep a reference, or it is collected mid-flight
        thread.start()

    def _speed_tested(self, report: netcheck.Report) -> None:
        verdict = report.verdict()
        self._net_verdict.setText(verdict)
        good = verdict.startswith("Good")
        usable = good or verdict.startswith(("Enough", "Usable"))
        self._net_verdict.setStyleSheet(
            "color: green;" if good else ("color: #B7791F;" if usable else "color: #C0392B;")
        )
        self._net_detail.setText("\n".join(report.summary_lines()))

    def _speed_test_finished(self) -> None:
        self._net_btn.setEnabled(True)
        self._net_thread = None

    # --- vocabulary -------------------------------------------------------

    def _vocabulary_tab(self, settings: Settings) -> QWidget:
        form = QFormLayout()

        self._vocab_source = self._terms_box(
            settings.vocabulary_source,
            placeholder="영업이익률\n평우\nLiveCap",
            tip=(
                "Names, jargon and product names as they are SPOKEN, one per line.\n"
                "\n"
                "These bias speech recognition towards hearing them correctly — use\n"
                "it for a Korean name or a technical term the model keeps mangling.\n"
                "Because the translation is made from what the model heard, getting\n"
                "this right is what improves the captions.\n"
                "\n"
                "Sent as the API's custom_vocabulary on the source transcription."
            ),
        )
        form.addRow("Source terms", self._vocab_source)

        self._vocab_captions = self._terms_box(
            settings.vocabulary_captions,
            placeholder="operating margin\nPyeongwoo Lee\nLiveCap",
            tip=(
                "The same idea for the other side: how names and terms should come\n"
                "out IN THE CAPTIONS, one per line.\n"
                "\n"
                "Use this when a name is heard correctly but written inconsistently —\n"
                "spelled differently from one sentence to the next, for instance.\n"
                "\n"
                "Sent as the API's custom_vocabulary on the caption transcription."
            ),
        )
        form.addRow("Caption terms", self._vocab_captions)

        note = QLabel(
            "One term per line; blank lines are ignored and duplicates are dropped. "
            f"At most {config.MAX_VOCABULARY_TERMS} terms per list — they are re-sent on "
            "every reconnect, so a long list slows each one. The terms are fixed when a "
            "session connects, so changing them here reopens the translator; audio and "
            "the transcript carry on. "
            "Whether the model actually honours them has not been measured — the API "
            "accepts them, which is not the same thing."
        )
        note.setWordWrap(True)
        note.setStyleSheet("color: gray;")
        form.addRow("", note)

        page = QWidget()
        page.setLayout(form)
        return page

    def _terms_box(self, value: str, *, placeholder: str, tip: str) -> QPlainTextEdit:
        box = QPlainTextEdit(value)
        box.setPlaceholderText(placeholder)
        box.setToolTip(tip)
        box.setMinimumWidth(320)
        box.setMaximumHeight(110)
        box.setLineWrapMode(QPlainTextEdit.LineWrapMode.NoWrap)
        return box

    # --- appearance -------------------------------------------------------

    def _appearance_tab(self, settings: Settings) -> QWidget:
        form = QFormLayout()

        self._bg_color = _ColorButton(settings.bg_color, "Background colour")
        form.addRow("Background colour", self._bg_color)

        self._box_color = _ColorButton(settings.box_color, "Caption box colour", alpha=True)
        self._box_color.set_opacity(settings.box_opacity)
        form.addRow("Caption box colour", self._box_color)

        self._text_color = _ColorButton(settings.text_color, "Text colour")
        form.addRow("Text colour", self._text_color)

        self._position = QComboBox()
        for position in BoxPosition:
            self._position.addItem(BOX_POSITION_MENU_TEXT[position], position.value)
        pos_idx = self._position.findData(settings.box_position)
        self._position.setCurrentIndex(pos_idx if pos_idx >= 0 else 0)
        form.addRow("Caption position", self._position)

        self._font_size = self._spin(settings.font_size, 24, 96, " px")
        form.addRow("Font size", self._font_size)

        self._max_lines = self._spin(settings.max_lines, 1, 4, " lines")
        form.addRow("Caption lines", self._max_lines)

        self._radius = self._spin(settings.corner_radius, 0, 48, " px")
        form.addRow("Corner radius", self._radius)

        self._box_width, box_width_row = self._slider(settings.box_max_width_pct, 60, 100, "%")
        form.addRow("Box max width", box_width_row)

        self._bottom_margin, margin_row = self._slider(settings.bottom_margin_pct, 2, 20, "%")
        form.addRow("Edge margin", margin_row)

        self._opacity, opacity_row = self._slider(int(settings.box_opacity * 100), 60, 100, "%")
        form.addRow("Box opacity", opacity_row)

        self._fixed_box = QCheckBox("Always show the caption box (fixed size)")
        self._fixed_box.setToolTip(
            "Turn off to let the box hug its content and disappear when empty."
        )
        self._fixed_box.setChecked(settings.fixed_box)
        form.addRow("", self._fixed_box)

        self._show_source = QCheckBox("Show the source text as well")
        self._show_source.setChecked(settings.show_source_text)
        form.addRow("", self._show_source)

        self._fullscreen = QCheckBox("Start in full screen")
        self._fullscreen.setChecked(settings.fullscreen)
        form.addRow("", self._fullscreen)

        page = QWidget()
        page.setLayout(form)
        return page

    # --- behaviour --------------------------------------------------------

    def _behaviour_tab(self, settings: Settings) -> QWidget:
        form = QFormLayout()

        self._target_lang = QLineEdit(settings.target_language_code)
        self._target_lang.setMaximumWidth(80)
        self._target_lang.setToolTip(
            "en = Korean→English (key 1), ko = English→Korean (key 2).\n"
            "The source language is detected by the model, never specified."
        )
        form.addRow("Target language", self._target_lang)

        self._direction_hint = QCheckBox("Briefly show the direction below the captions")
        self._direction_hint.setChecked(settings.show_direction_hint)
        form.addRow("", self._direction_hint)

        self._clear_after, clear_after_row = self._slider(
            int(settings.clear_after_seconds * 10), 0, 100, ""
        )
        self._clear_after.setToolTip(
            "Counted from when the last buffered line reached the screen and the\n"
            "input went quiet — not from the last fragment. 0 keeps captions up."
        )
        form.addRow("Clear after silence (0.1 s steps)", clear_after_row)

        self._line_hold, line_hold_row = self._slider(
            int(settings.line_hold_seconds * 10), 0, 30, ""
        )
        self._line_hold.setToolTip(
            "The model translates a whole utterance at a time, so several\n"
            "sentences can settle at once. Holding each line this long stops the\n"
            "burst scrolling past unread. 0 shows everything immediately."
        )
        form.addRow("Hold each line (0.1 s steps)", line_hold_row)

        self._save_transcript = QCheckBox("Save the transcript as a .md file")
        self._save_transcript.setToolTip("The filename is the session start timestamp.")
        self._save_transcript.setChecked(settings.save_transcript)
        form.addRow("", self._save_transcript)

        self._transcript_dir = QLineEdit(settings.transcript_dir)
        self._transcript_dir.setMinimumWidth(260)
        browse = QPushButton("Browse…")
        browse.clicked.connect(self._pick_transcript_dir)
        dir_row = QHBoxLayout()
        dir_row.addWidget(self._transcript_dir)
        dir_row.addWidget(browse)
        dir_holder = QWidget()
        dir_holder.setLayout(dir_row)
        form.addRow("Transcript folder", dir_holder)

        self._silence_gate = QCheckBox("Stop sending during silence (saves cost)")
        self._silence_gate.setToolTip(
            "Pause sending after 3 seconds below the threshold.\n"
            "Off by default: Korean pauses mid-sentence and context would break."
        )
        self._silence_gate.setChecked(settings.silence_gate)
        form.addRow("", self._silence_gate)

        self._policy = QComboBox()
        for policy, label in POLICY_LABELS.items():
            self._policy.addItem(label, policy.value)
        pidx = self._policy.findData(settings.caption_policy)
        self._policy.setCurrentIndex(pidx if pidx >= 0 else 0)
        self._policy.setToolTip("Fix this to match the measurements in spikes/RESULTS.md.")
        form.addRow("Caption commit policy", self._policy)

        self._debug_logging = QCheckBox("Verbose logging to the terminal")
        self._debug_logging.setToolTip(
            "Was LIVECAP_DEBUG in .env. Takes effect on the next launch.\n"
            "Key values are never logged."
        )
        self._debug_logging.setChecked(settings.debug_logging)
        form.addRow("", self._debug_logging)

        page = QWidget()
        page.setLayout(form)
        return page

    # --- widget helpers ---------------------------------------------------

    def _size_for_wrapped_lines(self, label: QLabel, *, lines: int, width: int) -> None:
        """Reserve room for `lines` of wrapped text, at construction time.

        A QFormLayout row built from a short placeholder fixes its height to
        that placeholder — later text that needs more lines just gets clipped,
        because nothing forces the row to grow again. Setting the height up
        front, before any real content arrives, sidesteps that entirely.
        """
        label.setMinimumWidth(width)
        fm = QFontMetrics(label.font())
        label.setMinimumHeight(fm.lineSpacing() * lines)

    def _spin(self, value: int, lo: int, hi: int, suffix: str) -> QSpinBox:
        s = QSpinBox()
        s.setRange(lo, hi)
        s.setValue(value)
        s.setSuffix(suffix)
        return s

    def _slider(self, value: int, lo: int, hi: int, suffix: str) -> tuple[QSlider, QWidget]:
        s = QSlider(Qt.Orientation.Horizontal)
        s.setRange(lo, hi)
        s.setValue(value)
        s.setMinimumWidth(200)
        label = QLabel(f"{value}{suffix}")
        label.setMinimumWidth(44)
        s.valueChanged.connect(lambda v: label.setText(f"{v}{suffix}"))
        row = QHBoxLayout()
        row.addWidget(s)
        row.addWidget(label)
        holder = QWidget()
        holder.setLayout(row)
        return s, holder

    def _pick_transcript_dir(self) -> None:
        current = str(resolve_dir(self._transcript_dir.text()))
        chosen = QFileDialog.getExistingDirectory(self, "Choose transcript folder", current)
        if chosen:
            self._transcript_dir.setText(chosen)

    # --- lifetime ---------------------------------------------------------

    def _save_typed_key(self) -> None:
        typed = self._key_edit.text().strip()
        if not typed:
            return
        config.save_api_key(typed, to_keychain=self._also_keychain.isChecked())
        self._key_written = True

    def apply_now(self) -> None:
        """Apply the current values without closing. Bound to Apply."""
        self._save_typed_key()
        settings = self.result_settings()
        # Keep it as the new baseline, so the next Apply diffs against this one.
        self._settings = settings
        self.applied.emit(settings)

    def _accept(self) -> None:
        self._save_typed_key()
        self._stop_meter()
        self.accept()

    def reject(self) -> None:  # noqa: D102 — Qt override
        # Cancel means "as it was": undo a key Apply already wrote. The window
        # puts the settings themselves back, since it owns them.
        if self._key_written:
            config.restore_stored_api_key(self._key_at_open)
            self._key_written = False
        self._stop_meter()
        super().reject()

    def closeEvent(self, event) -> None:  # noqa: N802 (Qt naming)
        # The meter holds the microphone open; never leave it running.
        self._stop_meter()
        self._settle_workers()
        super().closeEvent(event)

    def _settle_workers(self) -> None:
        """Let running workers finish before this dialog can be destroyed.

        The threads are children of the dialog, and a QThread destroyed while it
        is still running crashes. A speed test can take several seconds, so one
        that overruns the wait is detached rather than waited on for ever.
        """
        for attr in ("_key_thread", "_net_thread"):
            thread = getattr(self, attr, None)
            if thread is None:
                continue
            thread.quit()
            if thread.wait(WORKER_WAIT_MS):
                continue
            _log.warning("a preferences worker is still running — detaching it")
            thread.setParent(None)
            _DETACHED_WORKERS.add(thread)
            thread.finished.connect(lambda t=thread: _DETACHED_WORKERS.discard(t))
            setattr(self, attr, None)

    # --- result -----------------------------------------------------------

    def result_settings(self) -> Settings:
        return replace(
            self._settings,
            bg_color=self._bg_color.value(),
            box_color=self._box_color.value(),
            text_color=self._text_color.value(),
            audio_device_name=self._device.currentData() or "",
            box_position=self._position.currentData(),
            font_size=self._font_size.value(),
            max_lines=self._max_lines.value(),
            corner_radius=self._radius.value(),
            box_max_width_pct=self._box_width.value(),
            bottom_margin_pct=self._bottom_margin.value(),
            box_opacity=self._opacity.value() / 100.0,
            clear_after_seconds=self._clear_after.value() / 10.0,
            line_hold_seconds=self._line_hold.value() / 10.0,
            save_transcript=self._save_transcript.isChecked(),
            show_direction_hint=self._direction_hint.isChecked(),
            transcript_dir=self._transcript_dir.text().strip() or DEFAULT_DIR,
            fixed_box=self._fixed_box.isChecked(),
            show_source_text=self._show_source.isChecked(),
            fullscreen=self._fullscreen.isChecked(),
            silence_gate=self._silence_gate.isChecked(),
            caption_policy=self._policy.currentData(),
            target_language_code=self._target_lang.text().strip() or "en",
            debug_logging=self._debug_logging.isChecked(),
            vocabulary_source=self._vocab_source.toPlainText(),
            vocabulary_captions=self._vocab_captions.toPlainText(),
        ).clamped()
