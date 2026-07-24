"""Preferences dialog.

The API key goes to the macOS Keychain, never into Settings.
"""

from __future__ import annotations

from dataclasses import replace

from PyQt6.QtCore import Qt
from PyQt6.QtWidgets import (
    QCheckBox,
    QComboBox,
    QDialog,
    QDialogButtonBox,
    QFileDialog,
    QFormLayout,
    QHBoxLayout,
    QLabel,
    QLineEdit,
    QPushButton,
    QSlider,
    QSpinBox,
    QVBoxLayout,
    QWidget,
)

from .. import config
from ..audio import list_input_devices
from ..caption_buffer import Policy
from ..config import Settings
from ..transcript import DEFAULT_DIR, resolve_dir

POLICY_LABELS = {
    Policy.AUTO: "Detect automatically",
    Policy.INCREMENTAL: "A — incremental",
    Policy.UTTERANCE: "B — per utterance",
}


class SettingsDialog(QDialog):
    def __init__(self, settings: Settings, parent: QWidget | None = None) -> None:
        super().__init__(parent)
        self.setWindowTitle("LiveCap Preferences")
        self.setModal(True)
        self._settings = settings

        form = QFormLayout()

        # --- API key ---
        self._key_edit = QLineEdit()
        self._key_edit.setEchoMode(QLineEdit.EchoMode.Password)
        self._key_edit.setMinimumWidth(320)
        existing = config.load_api_key()
        self._key_edit.setPlaceholderText(
            "Stored in the Keychain — type a new key to replace it"
            if existing
            else "AIza…"
        )
        key_row = QHBoxLayout()
        key_row.addWidget(self._key_edit)
        clear_btn = QPushButton("Delete")
        clear_btn.setToolTip("Remove the stored key from the Keychain")
        clear_btn.clicked.connect(self._clear_key)
        key_row.addWidget(clear_btn)
        key_holder = QWidget()
        key_holder.setLayout(key_row)
        form.addRow("Gemini API key", key_holder)

        hint = QLabel("A GEMINI_API_KEY environment variable takes precedence.")
        hint.setStyleSheet("color: gray;")
        form.addRow("", hint)

        # --- audio ---
        self._device = QComboBox()
        self._device.addItem("System default input", "")
        for dev in list_input_devices():
            self._device.addItem(
                f"{dev.name} — {dev.channels}ch / {dev.default_samplerate:.0f} Hz", dev.name
            )
        idx = self._device.findData(settings.audio_device_name)
        self._device.setCurrentIndex(idx if idx >= 0 else 0)
        form.addRow("Audio input", self._device)

        # --- layout ---
        self._font_size = self._spin(settings.font_size, 24, 96, " px")
        form.addRow("Font size", self._font_size)

        self._max_lines = self._spin(settings.max_lines, 1, 4, " lines")
        form.addRow("Caption lines", self._max_lines)

        self._radius = self._spin(settings.corner_radius, 0, 48, " px")
        form.addRow("Corner radius", self._radius)

        self._box_width, box_width_row = self._slider(settings.box_max_width_pct, 60, 100, "%")
        form.addRow("Box max width", box_width_row)

        self._clear_after, clear_after_row = self._slider(
            int(settings.clear_after_seconds * 10), 0, 100, ""
        )
        self._clear_after.setToolTip("0 keeps captions on screen indefinitely.")
        form.addRow("Clear after silence (0.1 s steps)", clear_after_row)

        self._bottom_margin, margin_row = self._slider(settings.bottom_margin_pct, 2, 20, "%")
        form.addRow("Bottom margin", margin_row)

        self._opacity, opacity_row = self._slider(int(settings.box_opacity * 100), 60, 100, "%")
        form.addRow("Box opacity", opacity_row)

        # --- behaviour ---
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

        buttons = QDialogButtonBox(
            QDialogButtonBox.StandardButton.Ok | QDialogButtonBox.StandardButton.Cancel
        )
        buttons.accepted.connect(self._accept)
        buttons.rejected.connect(self.reject)

        root = QVBoxLayout(self)
        root.addLayout(form)
        root.addWidget(buttons)

    # --- widget helpers ---------------------------------------------------

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

    def _clear_key(self) -> None:
        config.clear_api_key()
        self._key_edit.clear()
        self._key_edit.setPlaceholderText("AIza…")

    def _accept(self) -> None:
        typed = self._key_edit.text().strip()
        if typed:
            config.save_api_key(typed)
        self.accept()

    # --- result -----------------------------------------------------------

    def result_settings(self) -> Settings:
        return replace(
            self._settings,
            audio_device_name=self._device.currentData() or "",
            font_size=self._font_size.value(),
            max_lines=self._max_lines.value(),
            corner_radius=self._radius.value(),
            box_max_width_pct=self._box_width.value(),
            bottom_margin_pct=self._bottom_margin.value(),
            box_opacity=self._opacity.value() / 100.0,
            clear_after_seconds=self._clear_after.value() / 10.0,
            save_transcript=self._save_transcript.isChecked(),
            show_direction_hint=self._direction_hint.isChecked(),
            transcript_dir=self._transcript_dir.text().strip() or DEFAULT_DIR,
            fixed_box=self._fixed_box.isChecked(),
            show_source_text=self._show_source.isChecked(),
            fullscreen=self._fullscreen.isChecked(),
            silence_gate=self._silence_gate.isChecked(),
            caption_policy=self._policy.currentData(),
            target_language_code=self._target_lang.text().strip() or "en",
        ).clamped()
