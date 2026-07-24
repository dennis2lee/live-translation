"""환경설정 다이얼로그 — PLAN.md §8.

API 키는 Settings가 아니라 macOS 키체인에 저장한다.
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
    Policy.AUTO: "자동 판별 (권장)",
    Policy.INCREMENTAL: "정책 A — 점진 표시",
    Policy.UTTERANCE: "정책 B — 발화 단위",
}


class SettingsDialog(QDialog):
    def __init__(self, settings: Settings, parent: QWidget | None = None) -> None:
        super().__init__(parent)
        self.setWindowTitle("LiveCap 환경설정")
        self.setModal(True)
        self._settings = settings

        form = QFormLayout()

        # --- API 키 ---
        self._key_edit = QLineEdit()
        self._key_edit.setEchoMode(QLineEdit.EchoMode.Password)
        self._key_edit.setMinimumWidth(320)
        existing = config.load_api_key()
        self._key_edit.setPlaceholderText(
            "키체인에 저장됨 (변경하려면 새 키 입력)" if existing else "AIza…"
        )
        key_row = QHBoxLayout()
        key_row.addWidget(self._key_edit)
        clear_btn = QPushButton("삭제")
        clear_btn.setToolTip("키체인에서 저장된 키를 지웁니다")
        clear_btn.clicked.connect(self._clear_key)
        key_row.addWidget(clear_btn)
        key_holder = QWidget()
        key_holder.setLayout(key_row)
        form.addRow("Gemini API 키", key_holder)

        hint = QLabel("GEMINI_API_KEY 환경변수가 있으면 그쪽이 우선합니다.")
        hint.setStyleSheet("color: gray;")
        form.addRow("", hint)

        # --- 오디오 ---
        self._device = QComboBox()
        self._device.addItem("시스템 기본 입력", "")
        for dev in list_input_devices():
            self._device.addItem(
                f"{dev.name} — {dev.channels}ch / {dev.default_samplerate:.0f} Hz", dev.name
            )
        idx = self._device.findData(settings.audio_device_name)
        self._device.setCurrentIndex(idx if idx >= 0 else 0)
        form.addRow("오디오 입력 장치", self._device)

        # --- 레이아웃 ---
        self._font_size = self._spin(settings.font_size, 24, 96, " px")
        form.addRow("글자 크기", self._font_size)

        self._max_lines = self._spin(settings.max_lines, 1, 4, " 줄")
        form.addRow("캡션 줄 수", self._max_lines)

        self._radius = self._spin(settings.corner_radius, 0, 48, " px")
        form.addRow("모서리 반경", self._radius)

        self._box_width, box_width_row = self._slider(settings.box_max_width_pct, 60, 100, "%")
        form.addRow("박스 최대 폭", box_width_row)

        self._clear_after, clear_after_row = self._slider(
            int(settings.clear_after_seconds * 10), 0, 100, ""
        )
        self._clear_after.setToolTip("0으로 두면 캡션을 지우지 않고 계속 남겨둡니다.")
        form.addRow("무음 후 캡션 지우기 (0.1초 단위)", clear_after_row)

        self._bottom_margin, margin_row = self._slider(settings.bottom_margin_pct, 2, 20, "%")
        form.addRow("하단 여백", margin_row)

        self._opacity, opacity_row = self._slider(int(settings.box_opacity * 100), 60, 100, "%")
        form.addRow("박스 불투명도", opacity_row)

        # --- 동작 ---
        self._save_transcript = QCheckBox("번역 기록을 .md 파일로 저장")
        self._save_transcript.setToolTip("파일명은 통역을 시작한 날짜·시간을 따릅니다.")
        self._save_transcript.setChecked(settings.save_transcript)
        form.addRow("", self._save_transcript)

        self._transcript_dir = QLineEdit(settings.transcript_dir)
        self._transcript_dir.setMinimumWidth(260)
        browse = QPushButton("찾아보기…")
        browse.clicked.connect(self._pick_transcript_dir)
        dir_row = QHBoxLayout()
        dir_row.addWidget(self._transcript_dir)
        dir_row.addWidget(browse)
        dir_holder = QWidget()
        dir_holder.setLayout(dir_row)
        form.addRow("기록 저장 폴더", dir_holder)

        self._fixed_box = QCheckBox("캡션 박스 항상 표시 (크기·위치 고정)")
        self._fixed_box.setToolTip(
            "끄면 내용에 맞춰 박스 크기가 변하고, 캡션이 없을 때 사라집니다."
        )
        self._fixed_box.setChecked(settings.fixed_box)
        form.addRow("", self._fixed_box)

        self._show_source = QCheckBox("원문(한국어) 함께 표시")
        self._show_source.setChecked(settings.show_source_text)
        form.addRow("", self._show_source)

        self._fullscreen = QCheckBox("전체화면으로 시작")
        self._fullscreen.setChecked(settings.fullscreen)
        form.addRow("", self._fullscreen)

        self._silence_gate = QCheckBox("무음 구간 전송 중단 (비용 절감)")
        self._silence_gate.setToolTip(
            "임계 이하가 3초 이상 지속되면 전송을 멈춥니다.\n"
            "한국어는 문장 중간 휴지가 길어 문맥이 끊길 수 있으므로 기본은 꺼짐입니다."
        )
        self._silence_gate.setChecked(settings.silence_gate)
        form.addRow("", self._silence_gate)

        self._policy = QComboBox()
        for policy, label in POLICY_LABELS.items():
            self._policy.addItem(label, policy.value)
        pidx = self._policy.findData(settings.caption_policy)
        self._policy.setCurrentIndex(pidx if pidx >= 0 else 0)
        self._policy.setToolTip("Phase 0 측정 결과(spikes/RESULTS.md)에 맞춰 고정할 수 있습니다.")
        form.addRow("캡션 커밋 정책", self._policy)

        self._target_lang = QLineEdit(settings.target_language_code)
        self._target_lang.setMaximumWidth(80)
        self._target_lang.setToolTip(
            "en = 한국어→영어 (단축키 1), ko = 영어→한국어 (단축키 2).\n"
            "출발 언어는 모델이 자동 감지하므로 지정하지 않습니다."
        )
        form.addRow("번역 대상 언어", self._target_lang)

        self._direction_hint = QCheckBox("번역 방향을 캡션 아래에 잠깐 표시")
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

    # --- 위젯 헬퍼 --------------------------------------------------------

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
        chosen = QFileDialog.getExistingDirectory(self, "기록 저장 폴더 선택", current)
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

    # --- 결과 -------------------------------------------------------------

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
