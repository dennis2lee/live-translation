"""Caption painting.

A QLabel with a stylesheet cannot cleanly auto-size a rounded box or clip to N
lines, so this draws everything itself in QWidget.paintEvent.
"""

from __future__ import annotations

from enum import StrEnum

from PyQt6.QtCore import (
    QEasingCurve,
    QPointF,
    QPropertyAnimation,
    QRectF,
    Qt,
    QTimer,
    pyqtProperty,
)
from PyQt6.QtGui import (
    QColor,
    QFont,
    QFontMetricsF,
    QPainter,
    QPainterPath,
    QTextLayout,
)
from PyQt6.QtWidgets import QWidget

from ..config import Settings

PAD_H = 32.0
PAD_V = 24.0
FADE_MS = 150
SOURCE_GAP = 14.0
SOURCE_FONT_RATIO = 0.42

# Direction hint (KO → EN), at the bottom-right of the caption box
HINT_HOLD_MS = 2000
HINT_FADE_MS = 400
HINT_GAP = 12.0
HINT_FONT_RATIO = 0.42

# Status indicator, at the bottom-right of the caption box
DOT_RADIUS_RATIO = 0.13
DOT_MIN_RADIUS = 5.0
DOT_GAP = 14.0
DOT_PULSE_MS = 1200
DOT_DIM_ALPHA = 0.15
COLOR_LIVE = QColor("#34C759")  # connected
COLOR_IDLE = QColor("#6E6E6E")  # idle
COLOR_ERROR = QColor("#FF3B30")  # error


class Indicator(StrEnum):
    IDLE = "idle"
    """Grey, steady — at launch and after stopping."""

    BUSY = "busy"
    """Green, blinking — connecting or reconnecting."""

    LIVE = "live"
    """Green, steady — connected."""

    ERROR = "error"
    """Red, steady — error."""


DOT_COLOR = {
    Indicator.IDLE: COLOR_IDLE,
    Indicator.BUSY: COLOR_LIVE,
    Indicator.LIVE: COLOR_LIVE,
    Indicator.ERROR: COLOR_ERROR,
}
STATUS_COLOR = QColor("#6E6E6E")
# Korean output needs an explicit Hangul family. Without it Qt falls back per
# glyph and the weight and spacing drift away from the Latin text.
FONT_FAMILIES = [
    "SF Pro Display",
    "Apple SD Gothic Neo",
    "Helvetica Neue",
    "Helvetica",
]
_CENTER = int(Qt.AlignmentFlag.AlignHCenter | Qt.AlignmentFlag.AlignVCenter)


class CaptionView(QWidget):
    """Solid background with a rounded caption box near the bottom."""

    def __init__(self, settings: Settings, parent: QWidget | None = None) -> None:
        super().__init__(parent)
        self._settings = settings
        self._lines: list[tuple[str, bool]] = []
        self._source_text = ""
        self._status = ""
        self._box_alpha = 0.0

        self._fade = QPropertyAnimation(self, b"boxAlpha", self)
        self._fade.setDuration(FADE_MS)
        self._fade.setEasingCurve(QEasingCurve.Type.InOutQuad)

        # Direction hint — held for two seconds, then faded out.
        self._hint_text = ""
        self._hint_alpha = 0.0
        self._hint_fade = QPropertyAnimation(self, b"hintAlpha", self)
        self._hint_fade.setDuration(HINT_FADE_MS)
        self._hint_fade.setEasingCurve(QEasingCurve.Type.InCubic)
        self._hint_timer = QTimer(self)
        self._hint_timer.setSingleShot(True)
        self._hint_timer.timeout.connect(self._start_hint_fade)

        # Status indicator — starts grey.
        self._indicator = Indicator.IDLE
        self._dot_alpha = 1.0
        self._dot_pulse = QPropertyAnimation(self, b"dotAlpha", self)
        self._dot_pulse.setDuration(DOT_PULSE_MS)
        self._dot_pulse.setStartValue(1.0)
        self._dot_pulse.setKeyValueAt(0.5, DOT_DIM_ALPHA)
        self._dot_pulse.setEndValue(1.0)
        self._dot_pulse.setLoopCount(-1)
        self._dot_pulse.setEasingCurve(QEasingCurve.Type.InOutSine)

        self.setAutoFillBackground(False)
        self.setAttribute(Qt.WidgetAttribute.WA_OpaquePaintEvent, True)

    # --- animated properties ----------------------------------------------

    def _get_box_alpha(self) -> float:
        return self._box_alpha

    def _set_box_alpha(self, v: float) -> None:
        self._box_alpha = v
        self.update()

    boxAlpha = pyqtProperty(float, fget=_get_box_alpha, fset=_set_box_alpha)

    def _get_hint_alpha(self) -> float:
        return self._hint_alpha

    def _set_hint_alpha(self, v: float) -> None:
        self._hint_alpha = v
        self.update()

    hintAlpha = pyqtProperty(float, fget=_get_hint_alpha, fset=_set_hint_alpha)

    # --- direction hint ---------------------------------------------------

    def show_hint(self, text: str, hold_ms: int = HINT_HOLD_MS) -> None:
        """Show grey text below the caption box briefly, then fade it out."""
        self._hint_text = text
        self._hint_fade.stop()
        self._hint_timer.stop()
        self._hint_alpha = 1.0
        self._hint_timer.start(hold_ms)
        self.update()

    def hide_hint(self) -> None:
        self._hint_fade.stop()
        self._hint_timer.stop()
        self._hint_alpha = 0.0
        self._hint_text = ""
        self.update()

    # --- status indicator -------------------------------------------------

    def _get_dot_alpha(self) -> float:
        return self._dot_alpha

    def _set_dot_alpha(self, v: float) -> None:
        self._dot_alpha = v
        self.update()

    dotAlpha = pyqtProperty(float, fget=_get_dot_alpha, fset=_set_dot_alpha)

    @property
    def indicator(self) -> Indicator:
        return self._indicator

    def set_indicator(self, state: Indicator) -> None:
        """Blink while connecting; hold steady otherwise."""
        if state is self._indicator:
            return
        self._indicator = state
        if state is Indicator.BUSY:
            self._dot_pulse.start()
        else:
            self._dot_pulse.stop()
            self._dot_alpha = 1.0
        self.update()

    def _start_hint_fade(self) -> None:
        self._hint_fade.stop()
        self._hint_fade.setStartValue(self._hint_alpha)
        self._hint_fade.setEndValue(0.0)
        self._hint_fade.start()

    # --- inputs -----------------------------------------------------------

    def apply_settings(self, settings: Settings) -> None:
        """Apply a settings change immediately, without restarting the app."""
        self._settings = settings
        self._refresh_visibility()

    def set_lines(self, lines: list[tuple[str, bool]]) -> None:
        self._lines = lines
        self._refresh_visibility()

    def set_source_text(self, text: str) -> None:
        self._source_text = text
        self.update()

    def set_status(self, status: str) -> None:
        """Error text, shown in the caption area rather than in a modal dialog."""
        self._status = status
        self._refresh_visibility()

    def clear(self) -> None:
        self._lines = []
        self._source_text = ""
        self._refresh_visibility()

    def _refresh_visibility(self) -> None:
        # In fixed mode the box stays even when empty; otherwise it disappears
        # as soon as there is nothing to show.
        target = 1.0 if (self._lines or self._settings.fixed_box) else 0.0
        if self._fade.state() == QPropertyAnimation.State.Running:
            self._fade.stop()
        if self._box_alpha != target:
            self._fade.setStartValue(self._box_alpha)
            self._fade.setEndValue(target)
            self._fade.start()
        else:
            self.update()

    # --- painting ---------------------------------------------------------

    def _caption_font(self) -> QFont:
        f = QFont()
        f.setFamilies(FONT_FAMILIES)
        f.setPixelSize(self._settings.font_size)
        f.setWeight(QFont.Weight.Medium)
        return f

    def paintEvent(self, event) -> None:  # noqa: N802 (Qt naming)
        p = QPainter(self)
        p.setRenderHint(QPainter.RenderHint.Antialiasing, True)
        p.setRenderHint(QPainter.RenderHint.TextAntialiasing, True)

        s = self._settings
        p.fillRect(self.rect(), QColor(s.bg_color))
        self._paint_body(p)
        # Drawn whether or not there are captions.
        self._paint_hint(p)
        self._paint_indicator(p)
        p.end()

    def _paint_body(self, p: QPainter) -> None:
        s = self._settings
        if self._status and not self._lines and not s.fixed_box:
            self._paint_status(p)
            return
        if self._box_alpha <= 0.001 or (not self._lines and not s.fixed_box):
            return
        self._paint_caption(p)

    def _anchor_right(self) -> float:
        """The right edge of the caption box.

        Fixed even when the box hugs its content, so the indicator does not
        drift sideways as the text length changes.
        """
        max_box_w = self.width() * self._settings.box_max_width_pct / 100.0
        return (self.width() + max_box_w) / 2.0

    def _dot_radius(self) -> float:
        return max(DOT_MIN_RADIUS, self._settings.font_size * DOT_RADIUS_RATIO)

    def _strip_rect(self, height: float) -> QRectF:
        """The strip just below the caption box, nudged on-screen if space is tight."""
        top = min(self._box_bottom() + HINT_GAP, self.height() - height)
        return QRectF(0, top, self.width(), height)

    def _paint_indicator(self, p: QPainter) -> None:
        radius = self._dot_radius()
        strip = self._strip_rect(radius * 2)
        color = QColor(DOT_COLOR[self._indicator])
        color.setAlphaF(max(0.0, min(1.0, self._dot_alpha)))
        p.setPen(Qt.PenStyle.NoPen)
        p.setBrush(color)
        centre = QPointF(self._anchor_right() - radius, strip.center().y())
        p.drawEllipse(centre, radius, radius)
        p.setBrush(Qt.BrushStyle.NoBrush)

    def _paint_hint(self, p: QPainter) -> None:
        if self._hint_alpha <= 0.001 or not self._hint_text:
            return
        s = self._settings
        f = self._caption_font()
        f.setPixelSize(max(16, int(s.font_size * HINT_FONT_RATIO)))
        f.setWeight(QFont.Weight.Normal)
        p.setFont(f)
        fm = QFontMetricsF(f)

        color = QColor(STATUS_COLOR)
        color.setAlphaF(max(0.0, min(1.0, self._hint_alpha)))
        p.setPen(color)

        # Right-aligned to the left of the indicator, so the two never overlap.
        radius = self._dot_radius()
        strip = self._strip_rect(max(fm.height(), radius * 2))
        right = self._anchor_right() - radius * 2 - DOT_GAP
        rect = QRectF(0, strip.top(), right, strip.height())
        p.drawText(
            rect,
            int(Qt.AlignmentFlag.AlignRight | Qt.AlignmentFlag.AlignVCenter),
            self._hint_text,
        )

    def _paint_status(self, p: QPainter) -> None:
        f = self._caption_font()
        f.setPixelSize(max(18, int(self._settings.font_size * 0.55)))
        p.setFont(f)
        p.setPen(STATUS_COLOR)
        fm = QFontMetricsF(f)
        y = self._box_bottom() - fm.height() / 2
        rect = QRectF(0, y - fm.height(), self.width(), fm.height() * 2)
        p.drawText(rect, int(Qt.AlignmentFlag.AlignHCenter | Qt.AlignmentFlag.AlignVCenter),
                   self._status)

    def _box_bottom(self) -> float:
        return self.height() * (1.0 - self._settings.bottom_margin_pct / 100.0)

    def _paint_caption(self, p: QPainter) -> None:
        s = self._settings
        font = self._caption_font()
        fm = QFontMetricsF(font)
        p.setFont(font)

        max_box_w = self.width() * s.box_max_width_pct / 100.0
        max_text_w = max(40.0, max_box_w - 2 * PAD_H)

        # Wrap to the real width, then drop from the front if it exceeds max_lines.
        wrapped: list[tuple[str, bool]] = []
        for text, confirmed in self._lines:
            for piece in _wrap(text, font, max_text_w):
                wrapped.append((piece, confirmed))
        max_lines = max(1, s.max_lines)
        wrapped = wrapped[-max_lines:]
        if not wrapped and not s.fixed_box:
            return

        line_h = fm.height()
        if s.fixed_box:
            # Fixed width and height, so the box never jitters with content.
            box_w = max_box_w
            box_h = line_h * max_lines + 2 * PAD_V
        else:
            text_w = max(fm.horizontalAdvance(t) for t, _ in wrapped)
            box_w = min(max_box_w, text_w + 2 * PAD_H)
            box_h = line_h * len(wrapped) + 2 * PAD_V  # sized to the real line count

        bottom = self._box_bottom()
        box = QRectF((self.width() - box_w) / 2.0, bottom - box_h, box_w, box_h)

        # the box
        box_color = QColor(s.box_color)
        box_color.setAlphaF(max(0.0, min(1.0, s.box_opacity)) * self._box_alpha)
        path = QPainterPath()
        path.addRoundedRect(box, float(s.corner_radius), float(s.corner_radius))
        p.fillPath(path, box_color)

        # An empty fixed box carries the status text inside it.
        if not wrapped:
            if self._status:
                sf = self._caption_font()
                sf.setPixelSize(max(18, int(s.font_size * 0.55)))
                p.setFont(sf)
                status_color = QColor(STATUS_COLOR)
                status_color.setAlphaF(self._box_alpha)
                p.setPen(status_color)
                p.drawText(box, _CENTER, self._status)
            return

        # the text
        confirmed_color = QColor(s.text_color)
        pending_color = QColor(s.text_color)
        pending_color.setAlphaF(0.55)  # unsettled text at 55% of the settled colour
        # Bottom-aligned in fixed mode, so the newest line never moves.
        y = (
            box.bottom() - PAD_V - line_h * len(wrapped)
            if s.fixed_box
            else box.top() + PAD_V
        )
        for text, confirmed in wrapped:
            color = QColor(confirmed_color if confirmed else pending_color)
            color.setAlphaF(color.alphaF() * self._box_alpha)
            p.setPen(color)
            line_rect = QRectF(box.left() + PAD_H, y, box.width() - 2 * PAD_H, line_h)
            p.drawText(line_rect, _CENTER, text)
            y += line_h

        # Source text — one small grey line outside the box, on the background
        if s.show_source_text and self._source_text:
            sf = self._caption_font()
            sf.setPixelSize(max(14, int(s.font_size * SOURCE_FONT_RATIO)))
            sf.setWeight(QFont.Weight.Normal)
            p.setFont(sf)
            sfm = QFontMetricsF(sf)
            src = sfm.elidedText(self._source_text, Qt.TextElideMode.ElideLeft, max_box_w)
            color = QColor("#9A9A9A")
            color.setAlphaF(self._box_alpha)
            p.setPen(color)
            rect = QRectF(
                (self.width() - max_box_w) / 2.0,
                box.top() - SOURCE_GAP - sfm.height(),
                max_box_w,
                sfm.height(),
            )
            p.drawText(rect, _CENTER, src)


def _wrap(text: str, font: QFont, max_width: float) -> list[str]:
    """Use QTextLayout to find the break positions, and return the pieces."""
    text = text.strip()
    if not text:
        return []
    layout = QTextLayout(text, font)
    layout.beginLayout()
    pieces: list[str] = []
    while True:
        line = layout.createLine()
        if not line.isValid():
            break
        line.setLineWidth(max_width)
        line.setPosition(QPointF(0, 0))
        start = line.textStart()
        pieces.append(text[start : start + line.textLength()].rstrip())
    layout.endLayout()
    return [p for p in pieces if p]
