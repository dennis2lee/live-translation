"""CaptionWindow and the native macOS menu bar.

Thread boundaries are crossed with pyqtSignal only. Widgets are never updated
from the asyncio thread.
"""

from __future__ import annotations

import subprocess
import time
from dataclasses import replace
from datetime import datetime

from PyQt6.QtCore import QObject, Qt, QTimer, pyqtSignal
from PyQt6.QtGui import QAction, QActionGroup, QColor, QCursor, QKeySequence
from PyQt6.QtWidgets import QColorDialog, QMainWindow, QMessageBox

from .. import config
from ..audio import AudioCapture, list_input_devices
from ..caption_buffer import CaptionBuffer
from ..config import (
    DIRECTION_MENU_TEXT,
    DIRECTION_TARGET,
    Direction,
    Settings,
    direction_for_target,
    direction_label,
)
from ..transcript import TranscriptWriter, resolve_dir
from ..translator import MODEL, LiveTranslator, State
from ..util import logging as log
from .caption_view import CaptionView, Indicator
from .settings_dialog import SettingsDialog

_log = log.get(__name__)

TICK_MS = 100
CURSOR_HIDE_MS = 3000
FONT_SIZE_CHOICES = (24, 32, 44, 56, 72, 96)
LINE_COUNT_CHOICES = (1, 2, 3, 4)

STATUS_TEXT = {
    State.IDLE: "Idle",
    State.CONNECTING: "Connecting",
    State.RUNNING: "Translating",
    State.RECONNECTING: "Reconnecting",
    State.ERROR: "Error",
}

# Connecting and reconnecting are reported by the indicator dot, not by text.
INDICATOR_FOR_STATE = {
    State.IDLE: Indicator.IDLE,
    State.CONNECTING: Indicator.BUSY,
    State.RECONNECTING: Indicator.BUSY,
    State.RUNNING: Indicator.LIVE,
    State.ERROR: Indicator.ERROR,
}


class _Bridge(QObject):
    """Signal bridge from the asyncio thread to the Qt main thread."""

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
        self._writer: TranscriptWriter | None = None
        self._started_at = 0.0

        self._buffer = CaptionBuffer(
            max_lines=settings.max_lines,
            policy=settings.policy,
            clear_after_seconds=settings.clear_after_seconds,
        )
        self._view = CaptionView(settings, self)
        self.setCentralWidget(self._view)
        self.setWindowTitle("LiveCap")

        self._bridge = _Bridge()
        self._bridge.source_text.connect(self._on_source_text)
        self._bridge.target_text.connect(self._on_target_text)
        self._bridge.state_changed.connect(self._on_state_changed)
        self._bridge.reconnected.connect(self._on_reconnected)

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

        # True full screen hides the macOS menu bar, which makes starting and
        # stopping awkward. Default to a maximised window with the bar visible.
        if settings.fullscreen:
            self.showFullScreen()
        else:
            self.showMaximized()

    # --- menus ------------------------------------------------------------

    def _build_menus(self) -> None:
        bar = self.menuBar()
        bar.setNativeMenuBar(True)

        app_menu = bar.addMenu("LiveCap")
        about = QAction("About LiveCap", self)
        about.setMenuRole(QAction.MenuRole.AboutRole)
        about.triggered.connect(self._show_about)
        app_menu.addAction(about)

        prefs = QAction("Preferences…", self)
        prefs.setMenuRole(QAction.MenuRole.PreferencesRole)
        prefs.setShortcut(QKeySequence("Ctrl+,"))
        prefs.triggered.connect(self._open_settings)
        app_menu.addAction(prefs)

        quit_act = QAction("Quit LiveCap", self)
        quit_act.setMenuRole(QAction.MenuRole.QuitRole)
        quit_act.setShortcut(QKeySequence("Ctrl+Q"))
        quit_act.triggered.connect(self.close)
        app_menu.addAction(quit_act)

        # --- Translate ---
        run_menu = bar.addMenu("Translate")
        self._act_start = QAction("Start Translating", self)
        self._act_start.setShortcut(QKeySequence("Ctrl+R"))
        self._act_start.triggered.connect(self.start_translation)
        run_menu.addAction(self._act_start)

        # Two separate items rather than one toggle, so a mis-click cannot stop
        # a live session.
        self._act_stop = QAction("Stop Translating", self)
        self._act_stop.setShortcut(QKeySequence("Ctrl+."))
        self._act_stop.triggered.connect(self.stop_translation)
        run_menu.addAction(self._act_stop)

        run_menu.addSeparator()
        clear = QAction("Clear Captions", self)
        clear.setShortcut(QKeySequence("Ctrl+K"))
        clear.triggered.connect(self._clear_captions)
        run_menu.addAction(clear)

        # The 0 key toggles. The menu keeps two items to avoid accidental stops.
        self._act_toggle = QAction("Toggle Translation", self)
        self._act_toggle.setShortcut(QKeySequence("0"))
        self._act_toggle.triggered.connect(self.toggle_translation)
        self.addAction(self._act_toggle)

        run_menu.addSeparator()
        self._act_save_transcript = QAction("Save Transcript", self, checkable=True)
        self._act_save_transcript.setToolTip(
            "Write the translation to a .md file named after the start time.\n"
            "Takes effect from the next session."
        )
        self._act_save_transcript.triggered.connect(lambda c: self._update(save_transcript=c))
        run_menu.addAction(self._act_save_transcript)

        open_dir = QAction("Open Transcript Folder", self)
        open_dir.triggered.connect(self._open_transcript_dir)
        run_menu.addAction(open_dir)

        run_menu.addSeparator()
        self._act_status = QAction("Status: Idle", self)
        self._act_status.setEnabled(False)
        run_menu.addAction(self._act_status)

        # --- Settings ---
        cfg = bar.addMenu("Settings")

        dir_menu = cfg.addMenu("Direction")
        self._direction_group = QActionGroup(self)
        self._direction_group.setExclusive(True)
        self._direction_actions: dict[Direction, QAction] = {}
        for i, direction in enumerate(Direction, start=1):
            act = QAction(DIRECTION_MENU_TEXT[direction], self, checkable=True)
            act.setShortcut(QKeySequence(str(i)))  # 1 = KO→EN, 2 = EN→KO
            act.triggered.connect(lambda _c, d=direction: self.set_direction(d))
            self._direction_group.addAction(act)
            dir_menu.addAction(act)
            self._direction_actions[direction] = act

        self._act_direction_hint = QAction("Show Direction", self, checkable=True)
        self._act_direction_hint.setToolTip(
            "On start and on a direction change, show KO → EN below the\n"
            "caption box for two seconds, then fade it out."
        )
        self._act_direction_hint.triggered.connect(
            lambda c: self._update(show_direction_hint=c)
        )
        cfg.addAction(self._act_direction_hint)
        cfg.addSeparator()
        cfg.addAction(self._color_action("Background Colour…", "bg_color", alpha=False))
        cfg.addAction(self._color_action("Box Colour…", "box_color", alpha=True))
        cfg.addAction(self._color_action("Text Colour…", "text_color", alpha=False))
        cfg.addSeparator()

        size_menu = cfg.addMenu("Font Size")
        self._font_group = QActionGroup(self)
        self._font_group.setExclusive(True)
        for size in FONT_SIZE_CHOICES:
            a = QAction(str(size), self, checkable=True)
            a.triggered.connect(lambda _c, s=size: self._update(font_size=s))
            self._font_group.addAction(a)
            size_menu.addAction(a)

        lines_menu = cfg.addMenu("Caption Lines")
        self._lines_group = QActionGroup(self)
        self._lines_group.setExclusive(True)
        for n in LINE_COUNT_CHOICES:
            a = QAction(str(n), self, checkable=True)
            a.triggered.connect(lambda _c, v=n: self._update(max_lines=v))
            self._lines_group.addAction(a)
            lines_menu.addAction(a)

        # A USB interface can be plugged or unplugged at any time, so rebuild
        # the device list every time the menu opens.
        self._device_menu = cfg.addMenu("Audio Input")
        self._device_menu.aboutToShow.connect(self._populate_device_menu)
        self._device_group: QActionGroup | None = None

        cfg.addSeparator()
        self._act_fixed_box = QAction("Always Show Caption Box", self, checkable=True)
        self._act_fixed_box.setToolTip(
            "Keep the box at a fixed size and position.\n"
            "Turn off to let it hug its content and disappear when empty."
        )
        self._act_fixed_box.triggered.connect(lambda c: self._update(fixed_box=c))
        cfg.addAction(self._act_fixed_box)

        self._act_source = QAction("Show Source Text", self, checkable=True)
        self._act_source.triggered.connect(lambda c: self._update(show_source_text=c))
        cfg.addAction(self._act_source)

        self._act_fullscreen = QAction("Full Screen", self, checkable=True)
        self._act_fullscreen.triggered.connect(self._toggle_fullscreen)
        cfg.addAction(self._act_fullscreen)

        cfg.addSeparator()
        reset = QAction("Reset to Defaults", self)
        reset.triggered.connect(self._reset_settings)
        cfg.addAction(reset)

    def _color_action(self, label: str, field: str, *, alpha: bool) -> QAction:
        act = QAction(label, self)
        act.triggered.connect(lambda: self._pick_color(label, field, alpha))
        return act

    def _populate_device_menu(self) -> None:
        """Fill the submenu with the detected input devices as radio items."""
        self._device_menu.clear()
        group = QActionGroup(self)
        group.setExclusive(True)
        self._device_group = group

        default = QAction("System Default Input", self, checkable=True)
        default.setChecked(not self._settings.audio_device_name)
        default.triggered.connect(lambda: self._choose_device(""))
        group.addAction(default)
        self._device_menu.addAction(default)
        self._device_menu.addSeparator()

        devices = list_input_devices()
        if not devices:
            none_act = QAction("(no input device found)", self)
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
        """Store devices by name, not index — indexes change on USB reconnect."""
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
        self._act_status.setText(f"Status: {STATUS_TEXT.get(self._state, 'Idle')}{detail}")
        for a in self._font_group.actions():
            a.setChecked(int(a.text()) == self._settings.font_size)
        for a in self._lines_group.actions():
            a.setChecked(int(a.text()) == self._settings.max_lines)
        self._act_save_transcript.setChecked(self._settings.save_transcript)
        self._act_direction_hint.setChecked(self._settings.show_direction_hint)
        current = direction_for_target(self._settings.target_language_code)
        for direction, act in self._direction_actions.items():
            act.setChecked(direction is current)
        self._act_fixed_box.setChecked(self._settings.fixed_box)
        self._act_source.setChecked(self._settings.show_source_text)
        self._act_fullscreen.setChecked(self._settings.fullscreen)

    # --- settings changes -------------------------------------------------

    def _apply_buffer_settings(self) -> None:
        self._buffer.set_max_lines(self._settings.max_lines)
        self._buffer.clear_after_seconds = self._settings.clear_after_seconds

    def _update(self, **changes: object) -> None:
        """Apply and persist a settings change without dropping the session."""
        self._settings = replace(self._settings, **changes).clamped()  # type: ignore[arg-type]
        config.save(self._settings)
        self._apply_buffer_settings()
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
            max_lines=self._settings.max_lines,
            policy=self._settings.policy,
            clear_after_seconds=self._settings.clear_after_seconds,
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

    def _open_transcript_dir(self) -> None:
        path = resolve_dir(self._settings.transcript_dir)
        try:
            path.mkdir(parents=True, exist_ok=True)
            subprocess.run(["open", str(path)], check=False)
        except OSError as exc:
            _log.error("could not open transcript directory: %s", exc)
            self._view.set_status(f"Cannot open transcript folder: {path}")

    def _show_about(self) -> None:
        from .. import __version__

        QMessageBox.about(
            self,
            "LiveCap",
            f"<b>LiveCap {__version__}</b><br><br>"
            "Real-time speech translation captions.<br>"
            "Engine: Google Gemini Live API",
        )

    # --- translation control ----------------------------------------------

    def toggle_translation(self) -> None:
        """The 0 key: start when idle, stop otherwise."""
        if self._state is State.IDLE:
            self.start_translation()
        else:
            self.stop_translation()

    def set_direction(self, direction: Direction) -> None:
        """Change the direction preset (1 = KO→EN, 2 = EN→KO).

        The target language is fixed when the session connects, so a live
        session reconnects the translator only. Audio capture and the ring
        buffer keep running, so nothing is lost in the gap.
        """
        target = DIRECTION_TARGET[direction]
        if target == self._settings.target_language_code:
            self._announce_direction()  # still show it when re-selecting the same direction
            return

        self._update(target_language_code=target)
        self._announce_direction()

        if self._state is not State.IDLE:
            if self._writer is not None:
                self._writer.note_direction(
                    direction_label(target), time.monotonic() - self._started_at
                )
            self._restart_translator()

    def _announce_direction(self) -> None:
        if self._settings.show_direction_hint:
            self._view.show_hint(direction_label(self._settings.target_language_code))

    def _restart_translator(self) -> None:
        """Reopen only the translator session, keeping audio and the transcript."""
        if self._capture is None:
            return
        api_key = config.load_api_key()
        if not api_key:
            return
        if self._translator is not None:
            self._translator.stop()
            self._translator = None
        self._translator = self._make_translator(self._capture.ring, api_key)
        self._translator.start()
        self._state = State.CONNECTING
        self._sync_actions()
        self._refresh_overlay()

    def _make_translator(self, ring: object, api_key: str) -> LiveTranslator:
        return LiveTranslator(
            ring,  # type: ignore[arg-type]
            api_key,
            target_language_code=self._settings.target_language_code,
            echo_target_language=self._settings.echo_target_language,
            silence_gate=self._settings.silence_gate,
            on_source=self._bridge.source_text.emit,
            on_target=self._bridge.target_text.emit,
            on_state=lambda s, d: self._bridge.state_changed.emit(s.value, d),
            on_reconnect=self._bridge.reconnected.emit,
        )

    def start_translation(self) -> None:
        if self._state is not State.IDLE:
            return
        api_key = config.load_api_key()
        if not api_key:
            self._state = State.ERROR
            self._error_detail = ""
            self._view.set_status("API key required — enter it in Preferences")
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
            self._view.set_status(
                f"{self._settings.audio_device_name} unavailable — using {dev.name}"
            )

        self._translator = self._make_translator(self._capture.ring, api_key)
        self._started_at = time.monotonic()
        self._writer = None
        if self._settings.save_transcript:
            writer = TranscriptWriter(
                self._settings.transcript_dir,
                target_language=self._settings.target_language_code,
                model=MODEL,
            )
            if writer.open(datetime.now()) is not None:
                self._writer = writer
            else:
                self._view.set_status("Cannot write the transcript — check folder permissions")

        self._translator.start()
        self._state = State.CONNECTING
        self._error_detail = ""
        self._sync_actions()
        self._refresh_overlay()
        self._reset_cursor_timer()
        self._announce_direction()  # report the current direction on start

    def stop_translation(self) -> None:
        reconnects = self._translator.stats.reconnects if self._translator is not None else 0
        if self._translator is not None:
            self._translator.stop()
            self._translator = None
        if self._capture is not None:
            self._capture.stop()
            self._capture = None
        # Make sure the final unsettled sentence reaches the transcript.
        self._buffer.flush()
        self._record_new_lines()
        if self._writer is not None:
            self._writer.close(
                elapsed_seconds=time.monotonic() - self._started_at, reconnects=reconnects
            )
            self._writer = None
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

    # --- signal handlers, on the main thread ------------------------------

    def _on_source_text(self, text: str) -> None:
        self._view.set_source_text(text)

    def _on_target_text(self, text: str) -> None:
        if self._buffer.feed(text, time.monotonic()):
            self._push_lines()
        self._record_new_lines()

    def _record_new_lines(self) -> None:
        """Feed settled lines to the transcript, independent of what is displayed."""
        if self._writer is None:
            return
        elapsed = time.monotonic() - self._started_at
        for line in self._buffer.drain_new_commits():
            self._writer.append_line(line, elapsed)

    def _on_reconnected(self) -> None:
        self._buffer.note_reconnect()
        if self._writer is not None and self._translator is not None:
            gaps = self._translator.stats.gaps_ms
            self._writer.note_reconnect(
                time.monotonic() - self._started_at, gaps[-1] if gaps else 0.0
            )

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
        self._record_new_lines()

    def _push_lines(self) -> None:
        self._view.set_lines(self._buffer.display_lines())
        self._refresh_overlay()

    def _refresh_overlay(self) -> None:
        """Update the indicator. Never opens a modal dialog.

        Connecting and reconnecting are conveyed by the dot alone. Errors also
        print their cause, because a red dot is not actionable on its own.
        """
        self._view.set_indicator(INDICATOR_FOR_STATE.get(self._state, Indicator.IDLE))
        if self._state is State.ERROR:
            self._view.set_status(self._error_detail or "Connection failed")
        else:
            self._view.set_status("")

    # --- cursor hiding ----------------------------------------------------

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

    # --- shutdown ---------------------------------------------------------

    def closeEvent(self, event) -> None:  # noqa: N802 (Qt naming)
        self.stop_translation()
        config.save(self._settings)
        super().closeEvent(event)
