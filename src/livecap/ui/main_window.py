"""CaptionWindow + 시스템 메뉴바 — PLAN.md §6.1, §6.3.

스레드 경계는 반드시 pyqtSignal을 경유한다. asyncio 스레드에서 위젯을 직접
갱신하지 않는다.
"""

from __future__ import annotations

import time
from dataclasses import replace

from PyQt6.QtCore import QObject, Qt, QTimer, pyqtSignal
from PyQt6.QtGui import QAction, QActionGroup, QColor, QCursor, QKeySequence
from PyQt6.QtWidgets import QColorDialog, QMainWindow, QMessageBox

from .. import config
from ..audio import AudioCapture, list_input_devices
from ..caption_buffer import CaptionBuffer
from ..config import Settings
from ..translator import LiveTranslator, State
from ..util import logging as log
from .caption_view import CaptionView
from .settings_dialog import SettingsDialog

_log = log.get(__name__)

TICK_MS = 100
CURSOR_HIDE_MS = 3000
FONT_SIZE_CHOICES = (24, 32, 44, 56, 72, 96)
LINE_COUNT_CHOICES = (1, 2, 3, 4)

STATUS_TEXT = {
    State.IDLE: "대기 중",
    State.CONNECTING: "연결 중",
    State.RUNNING: "통역 중",
    State.RECONNECTING: "재연결 중",
    State.ERROR: "오류",
}

OVERLAY_TEXT = {
    State.CONNECTING: "연결 중…",
    State.RECONNECTING: "재연결 중…",
}


class _Bridge(QObject):
    """asyncio 스레드 → Qt 메인 스레드 시그널 브리지."""

    source_text = pyqtSignal(str)
    target_text = pyqtSignal(str)
    state_changed = pyqtSignal(str, str)
    reconnected = pyqtSignal()


class CaptionWindow(QMainWindow):
    def __init__(self, settings: Settings) -> None:
        super().__init__()
        self._settings = settings
        self._capture: AudioCapture | None = None
        self._translator: LiveTranslator | None = None
        self._state = State.IDLE
        self._error_detail = ""

        self._buffer = CaptionBuffer(max_lines=settings.max_lines, policy=settings.policy)
        self._view = CaptionView(settings, self)
        self.setCentralWidget(self._view)
        self.setWindowTitle("LiveCap")

        self._bridge = _Bridge()
        self._bridge.source_text.connect(self._on_source_text)
        self._bridge.target_text.connect(self._on_target_text)
        self._bridge.state_changed.connect(self._on_state_changed)
        self._bridge.reconnected.connect(self._buffer.note_reconnect)

        self._tick = QTimer(self)
        self._tick.timeout.connect(self._on_tick)
        self._tick.start(TICK_MS)

        self._cursor_timer = QTimer(self)
        self._cursor_timer.setSingleShot(True)
        self._cursor_timer.timeout.connect(self._hide_cursor)
        self._cursor_hidden = False
        self.setMouseTracking(True)
        self._view.setMouseTracking(True)

        self._build_menus()
        self._sync_actions()
        self._refresh_overlay()

        # 진짜 전체화면이면 macOS가 시스템 메뉴바를 숨겨 통역 시작/중지가 불편하다.
        # 기본은 메뉴바가 항상 보이는 최대화 창.
        if settings.fullscreen:
            self.showFullScreen()
        else:
            self.showMaximized()

    # --- 메뉴 -------------------------------------------------------------

    def _build_menus(self) -> None:
        bar = self.menuBar()
        bar.setNativeMenuBar(True)

        app_menu = bar.addMenu("LiveCap")
        about = QAction("LiveCap 정보", self)
        about.setMenuRole(QAction.MenuRole.AboutRole)
        about.triggered.connect(self._show_about)
        app_menu.addAction(about)

        prefs = QAction("환경설정…", self)
        prefs.setMenuRole(QAction.MenuRole.PreferencesRole)
        prefs.setShortcut(QKeySequence("Ctrl+,"))
        prefs.triggered.connect(self._open_settings)
        app_menu.addAction(prefs)

        quit_act = QAction("LiveCap 종료", self)
        quit_act.setMenuRole(QAction.MenuRole.QuitRole)
        quit_act.setShortcut(QKeySequence("Ctrl+Q"))
        quit_act.triggered.connect(self.close)
        app_menu.addAction(quit_act)

        # --- 통역 ---
        run_menu = bar.addMenu("통역")
        self._act_start = QAction("통역 시작", self)
        self._act_start.setShortcut(QKeySequence("Ctrl+R"))
        self._act_start.triggered.connect(self.start_translation)
        run_menu.addAction(self._act_start)

        # 토글이 아니라 별도 항목 두 개 — 실수로 중지를 누르는 사고를 줄인다.
        self._act_stop = QAction("통역 중지", self)
        self._act_stop.setShortcut(QKeySequence("Ctrl+."))
        self._act_stop.triggered.connect(self.stop_translation)
        run_menu.addAction(self._act_stop)

        run_menu.addSeparator()
        clear = QAction("캡션 지우기", self)
        clear.setShortcut(QKeySequence("Ctrl+K"))
        clear.triggered.connect(self._clear_captions)
        run_menu.addAction(clear)

        run_menu.addSeparator()
        self._act_status = QAction("상태: 대기 중", self)
        self._act_status.setEnabled(False)
        run_menu.addAction(self._act_status)

        # --- 설정 ---
        cfg = bar.addMenu("설정")
        cfg.addAction(self._color_action("배경색…", "bg_color", alpha=False))
        cfg.addAction(self._color_action("캡션 박스 색…", "box_color", alpha=True))
        cfg.addAction(self._color_action("글자색…", "text_color", alpha=False))
        cfg.addSeparator()

        size_menu = cfg.addMenu("글자 크기")
        self._font_group = QActionGroup(self)
        self._font_group.setExclusive(True)
        for size in FONT_SIZE_CHOICES:
            a = QAction(str(size), self, checkable=True)
            a.triggered.connect(lambda _c, s=size: self._update(font_size=s))
            self._font_group.addAction(a)
            size_menu.addAction(a)

        lines_menu = cfg.addMenu("캡션 줄 수")
        self._lines_group = QActionGroup(self)
        self._lines_group.setExclusive(True)
        for n in LINE_COUNT_CHOICES:
            a = QAction(str(n), self, checkable=True)
            a.triggered.connect(lambda _c, v=n: self._update(max_lines=v))
            self._lines_group.addAction(a)
            lines_menu.addAction(a)

        # 오디오 입력 장치 — USB 인터페이스는 도중에 연결·해제될 수 있으므로
        # 메뉴를 열 때마다 목록을 새로 만든다.
        self._device_menu = cfg.addMenu("오디오 입력 장치")
        self._device_menu.aboutToShow.connect(self._populate_device_menu)
        self._device_group: QActionGroup | None = None

        cfg.addSeparator()
        self._act_fixed_box = QAction("캡션 박스 항상 표시", self, checkable=True)
        self._act_fixed_box.setToolTip(
            "박스를 항상 같은 크기·위치로 유지합니다.\n"
            "끄면 내용에 맞춰 크기가 변하고, 캡션이 없을 때 사라집니다."
        )
        self._act_fixed_box.triggered.connect(lambda c: self._update(fixed_box=c))
        cfg.addAction(self._act_fixed_box)

        self._act_source = QAction("원문(한국어) 함께 표시", self, checkable=True)
        self._act_source.triggered.connect(lambda c: self._update(show_source_text=c))
        cfg.addAction(self._act_source)

        self._act_fullscreen = QAction("전체화면", self, checkable=True)
        self._act_fullscreen.triggered.connect(self._toggle_fullscreen)
        cfg.addAction(self._act_fullscreen)

        cfg.addSeparator()
        reset = QAction("기본값으로 되돌리기", self)
        reset.triggered.connect(self._reset_settings)
        cfg.addAction(reset)

    def _color_action(self, label: str, field: str, *, alpha: bool) -> QAction:
        act = QAction(label, self)
        act.triggered.connect(lambda: self._pick_color(label, field, alpha))
        return act

    def _populate_device_menu(self) -> None:
        """감지된 입력 장치를 라디오 선택으로 채운다."""
        self._device_menu.clear()
        group = QActionGroup(self)
        group.setExclusive(True)
        self._device_group = group

        default = QAction("시스템 기본 입력", self, checkable=True)
        default.setChecked(not self._settings.audio_device_name)
        default.triggered.connect(lambda: self._choose_device(""))
        group.addAction(default)
        self._device_menu.addAction(default)
        self._device_menu.addSeparator()

        devices = list_input_devices()
        if not devices:
            none_act = QAction("(입력 장치를 찾을 수 없습니다)", self)
            none_act.setEnabled(False)
            self._device_menu.addAction(none_act)
            return

        for dev in devices:
            label = f"{dev.name}  —  {dev.channels}ch / {dev.default_samplerate:.0f} Hz"
            a = QAction(label, self, checkable=True)
            a.setChecked(dev.name == self._settings.audio_device_name)
            a.triggered.connect(lambda _c, n=dev.name: self._choose_device(n))
            group.addAction(a)
            self._device_menu.addAction(a)

    def _choose_device(self, name: str) -> None:
        """장치는 인덱스가 아니라 이름으로 저장한다 (USB 재연결 시 인덱스가 바뀜)."""
        if name == self._settings.audio_device_name:
            return
        self._update(audio_device_name=name)
        if self._state is not State.IDLE:
            _log.info("audio device changed while running — restarting capture")
            self.stop_translation()
            self.start_translation()

    def _sync_actions(self) -> None:
        running = self._state is not State.IDLE
        self._act_start.setEnabled(not running)
        self._act_stop.setEnabled(running)
        detail = f" ({self._error_detail})" if self._error_detail else ""
        self._act_status.setText(f"상태: {STATUS_TEXT.get(self._state, '대기 중')}{detail}")
        for a in self._font_group.actions():
            a.setChecked(int(a.text()) == self._settings.font_size)
        for a in self._lines_group.actions():
            a.setChecked(int(a.text()) == self._settings.max_lines)
        self._act_fixed_box.setChecked(self._settings.fixed_box)
        self._act_source.setChecked(self._settings.show_source_text)
        self._act_fullscreen.setChecked(self._settings.fullscreen)

    # --- 설정 변경 --------------------------------------------------------

    def _update(self, **changes: object) -> None:
        """설정을 바꾸고 즉시 반영·저장한다. 통역 세션은 끊지 않는다."""
        self._settings = replace(self._settings, **changes).clamped()  # type: ignore[arg-type]
        config.save(self._settings)
        self._buffer.set_max_lines(self._settings.max_lines)
        self._view.apply_settings(self._settings)
        self._push_lines()
        self._sync_actions()

    def _pick_color(self, title: str, field: str, alpha: bool) -> None:
        current = QColor(getattr(self._settings, field))
        opts = (
            QColorDialog.ColorDialogOption.ShowAlphaChannel
            if alpha
            else QColorDialog.ColorDialogOption(0)
        )
        chosen = QColorDialog.getColor(current, self, title, opts)
        if not chosen.isValid():
            return
        changes: dict[str, object] = {field: chosen.name(QColor.NameFormat.HexRgb).upper()}
        if alpha:
            changes["box_opacity"] = chosen.alphaF()
        self._update(**changes)

    def _toggle_fullscreen(self, checked: bool) -> None:
        self._update(fullscreen=checked)
        if checked:
            self.showFullScreen()
        else:
            self.showMaximized()

    def _reset_settings(self) -> None:
        self._settings = config.reset()
        self._buffer = CaptionBuffer(
            max_lines=self._settings.max_lines, policy=self._settings.policy
        )
        self._view.apply_settings(self._settings)
        self._view.clear()
        self._sync_actions()
        if self.isFullScreen():
            self.showMaximized()

    def _open_settings(self) -> None:
        dlg = SettingsDialog(self._settings, self)
        if dlg.exec():
            self._settings = dlg.result_settings()
            config.save(self._settings)
            self._buffer.set_max_lines(self._settings.max_lines)
            self._view.apply_settings(self._settings)
            self._push_lines()
            self._sync_actions()

    def _show_about(self) -> None:
        from .. import __version__

        QMessageBox.about(
            self,
            "LiveCap",
            f"<b>LiveCap {__version__}</b><br><br>"
            "한국어 발화를 영어로 실시간 통역해 캡션으로 띄웁니다.<br>"
            "엔진: Google Gemini Live API",
        )

    # --- 통역 제어 --------------------------------------------------------

    def start_translation(self) -> None:
        if self._state is not State.IDLE:
            return
        api_key = config.load_api_key()
        if not api_key:
            self._state = State.ERROR
            self._error_detail = ""
            self._view.set_status("API 키가 필요합니다 (환경설정에서 입력)")
            self._sync_actions()
            return

        self._capture = AudioCapture(device_name=self._settings.audio_device_name)
        try:
            dev = self._capture.start()
        except RuntimeError as exc:
            self._capture = None
            self._state = State.ERROR
            self._error_detail = ""
            self._view.set_status(str(exc))
            self._sync_actions()
            return

        if self._settings.audio_device_name and dev.name != self._settings.audio_device_name:
            _log.warning("requested device unavailable — using %r", dev.name)
            self._view.set_status(f"{self._settings.audio_device_name} 없음 — {dev.name} 사용")

        self._translator = LiveTranslator(
            self._capture.ring,
            api_key,
            target_language_code=self._settings.target_language_code,
            echo_target_language=self._settings.echo_target_language,
            silence_gate=self._settings.silence_gate,
            on_source=self._bridge.source_text.emit,
            on_target=self._bridge.target_text.emit,
            on_state=lambda s, d: self._bridge.state_changed.emit(s.value, d),
            on_reconnect=self._bridge.reconnected.emit,
        )
        self._translator.start()
        self._state = State.CONNECTING
        self._error_detail = ""
        self._sync_actions()
        self._refresh_overlay()
        self._reset_cursor_timer()

    def stop_translation(self) -> None:
        if self._translator is not None:
            self._translator.stop()
            self._translator = None
        if self._capture is not None:
            self._capture.stop()
            self._capture = None
        self._state = State.IDLE
        self._error_detail = ""
        self._show_cursor()
        self._cursor_timer.stop()
        self._sync_actions()
        self._refresh_overlay()

    def _clear_captions(self) -> None:
        self._buffer.clear()
        self._view.clear()
        self._refresh_overlay()

    # --- 시그널 수신 (메인 스레드) ----------------------------------------

    def _on_source_text(self, text: str) -> None:
        self._view.set_source_text(text)

    def _on_target_text(self, text: str) -> None:
        if self._buffer.feed(text, time.monotonic()):
            self._push_lines()

    def _on_state_changed(self, state_value: str, detail: str) -> None:
        try:
            self._state = State(state_value)
        except ValueError:
            return
        self._error_detail = detail
        self._sync_actions()
        self._refresh_overlay()

    def _on_tick(self) -> None:
        if self._buffer.tick(time.monotonic()):
            self._push_lines()

    def _push_lines(self) -> None:
        self._view.set_lines(self._buffer.display_lines())
        self._refresh_overlay()

    def _refresh_overlay(self) -> None:
        """캡션이 없을 때만 상태 문구를 보여준다. 모달은 띄우지 않는다."""
        if self._state is State.ERROR:
            self._view.set_status(self._error_detail or "연결에 실패했습니다")
        else:
            self._view.set_status(OVERLAY_TEXT.get(self._state, ""))

    # --- 커서 숨김 --------------------------------------------------------

    def mouseMoveEvent(self, event) -> None:  # noqa: N802 (Qt naming)
        self._show_cursor()
        if self._state is not State.IDLE:
            self._reset_cursor_timer()
        super().mouseMoveEvent(event)

    def _reset_cursor_timer(self) -> None:
        self._cursor_timer.start(CURSOR_HIDE_MS)

    def _hide_cursor(self) -> None:
        if not self._cursor_hidden and self._state is not State.IDLE:
            self.setCursor(QCursor(Qt.CursorShape.BlankCursor))
            self._cursor_hidden = True

    def _show_cursor(self) -> None:
        if self._cursor_hidden:
            self.unsetCursor()
            self._cursor_hidden = False

    # --- 종료 -------------------------------------------------------------

    def closeEvent(self, event) -> None:  # noqa: N802 (Qt naming)
        self.stop_translation()
        config.save(self._settings)
        super().closeEvent(event)
