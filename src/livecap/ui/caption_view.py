"""캡션 페인팅 — PLAN.md §6.2.

QLabel + 스타일시트로는 라운드 박스 자동 크기 조절과 N줄 클리핑을 깔끔하게
처리하기 어려우므로 QWidget.paintEvent에서 직접 그린다.
"""

from __future__ import annotations

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

# 번역 방향 표시 (KO → EN)
HINT_HOLD_MS = 2000
HINT_FADE_MS = 400
HINT_GAP = 12.0
HINT_FONT_RATIO = 0.42
STATUS_COLOR = QColor("#6E6E6E")
# 한글 출력(영어→한국어)을 쓰므로 한글 글꼴을 명시한다. 빠뜨리면 Qt 폴백에
# 맡겨지면서 굵기·자간이 라틴 문자와 어긋난다.
FONT_FAMILIES = [
    "SF Pro Display",
    "Apple SD Gothic Neo",
    "Helvetica Neue",
    "Helvetica",
]
_CENTER = int(Qt.AlignmentFlag.AlignHCenter | Qt.AlignmentFlag.AlignVCenter)


class CaptionView(QWidget):
    """검은 배경 + 하단 라운드 캡션 박스."""

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

        # 방향 표시 — 2초 유지한 뒤 서서히 사라진다.
        self._hint_text = ""
        self._hint_alpha = 0.0
        self._hint_fade = QPropertyAnimation(self, b"hintAlpha", self)
        self._hint_fade.setDuration(HINT_FADE_MS)
        self._hint_fade.setEasingCurve(QEasingCurve.Type.InCubic)
        self._hint_timer = QTimer(self)
        self._hint_timer.setSingleShot(True)
        self._hint_timer.timeout.connect(self._start_hint_fade)

        self.setAutoFillBackground(False)
        self.setAttribute(Qt.WidgetAttribute.WA_OpaquePaintEvent, True)

    # --- 애니메이션용 프로퍼티 --------------------------------------------

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

    # --- 방향 표시 --------------------------------------------------------

    def show_hint(self, text: str, hold_ms: int = HINT_HOLD_MS) -> None:
        """캡션 박스 아래에 회색으로 잠깐 띄웠다가 페이드아웃한다."""
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

    def _start_hint_fade(self) -> None:
        self._hint_fade.stop()
        self._hint_fade.setStartValue(self._hint_alpha)
        self._hint_fade.setEndValue(0.0)
        self._hint_fade.start()

    # --- 입력 -------------------------------------------------------------

    def apply_settings(self, settings: Settings) -> None:
        """설정 변경을 즉시 반영한다 (앱 재시작 불필요)."""
        self._settings = settings
        self._refresh_visibility()

    def set_lines(self, lines: list[tuple[str, bool]]) -> None:
        self._lines = lines
        self._refresh_visibility()

    def set_source_text(self, text: str) -> None:
        self._source_text = text
        self.update()

    def set_status(self, status: str) -> None:
        """연결 중·재연결 중 등. 모달을 띄우지 않고 캡션 자리에 표시한다."""
        self._status = status
        self._refresh_visibility()

    def clear(self) -> None:
        self._lines = []
        self._source_text = ""
        self._refresh_visibility()

    def _refresh_visibility(self) -> None:
        # 고정 박스 모드에서는 비어 있어도 박스를 유지한다.
        # 끄면 표시할 텍스트가 없을 때 박스 자체를 숨긴다 (PLAN.md §6.2).
        target = 1.0 if (self._lines or self._settings.fixed_box) else 0.0
        if self._fade.state() == QPropertyAnimation.State.Running:
            self._fade.stop()
        if self._box_alpha != target:
            self._fade.setStartValue(self._box_alpha)
            self._fade.setEndValue(target)
            self._fade.start()
        else:
            self.update()

    # --- 페인팅 -----------------------------------------------------------

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
        self._paint_hint(p)  # 캡션 유무와 무관하게 그린다
        p.end()

    def _paint_body(self, p: QPainter) -> None:
        s = self._settings
        if self._status and not self._lines and not s.fixed_box:
            self._paint_status(p)
            return
        if self._box_alpha <= 0.001 or (not self._lines and not s.fixed_box):
            return
        self._paint_caption(p)

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

        top = self._box_bottom() + HINT_GAP
        # 하단 여백을 좁게 잡아 자리가 모자라면 화면 안으로 밀어 넣는다.
        top = min(top, self.height() - fm.height())
        p.drawText(QRectF(0, top, self.width(), fm.height()), _CENTER, self._hint_text)

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

        # 논리 줄을 실제 폭에 맞춰 접고, 최대 줄 수를 넘으면 앞부분을 버린다.
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
            # 폭·높이를 고정한다. 내용에 따라 박스가 흔들리지 않는다.
            box_w = max_box_w
            box_h = line_h * max_lines + 2 * PAD_V
        else:
            text_w = max(fm.horizontalAdvance(t) for t, _ in wrapped)
            box_w = min(max_box_w, text_w + 2 * PAD_H)
            box_h = line_h * len(wrapped) + 2 * PAD_V  # 실제 줄 수에 맞춰 자동 조절

        bottom = self._box_bottom()
        box = QRectF((self.width() - box_w) / 2.0, bottom - box_h, box_w, box_h)

        # 박스
        box_color = QColor(s.box_color)
        box_color.setAlphaF(max(0.0, min(1.0, s.box_opacity)) * self._box_alpha)
        path = QPainterPath()
        path.addRoundedRect(box, float(s.corner_radius), float(s.corner_radius))
        p.fillPath(path, box_color)

        # 고정 박스가 비어 있으면 상태 문구를 박스 안에 넣는다.
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

        # 텍스트
        confirmed_color = QColor(s.text_color)
        pending_color = QColor(s.text_color)
        pending_color.setAlphaF(0.55)  # 미확정은 확정색의 55% 알파
        # 고정 박스에서는 아래쪽 정렬 — 줄 수가 변해도 최신 줄이 같은 자리에 있다.
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

        # 원문 한국어 — 박스 밖, 배경색 위에 작은 회색 1줄
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
    """QTextLayout으로 줄바꿈 위치를 계산해 실제 문자열 조각으로 돌려준다."""
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
