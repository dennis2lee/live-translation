"""CaptionView 페인팅 검증 — PLAN.md §6.2 수용 기준.

오프스크린 플랫폼에서 렌더한 픽셀을 직접 검사한다.
"""

from __future__ import annotations

import os

os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")

import pytest  # noqa: E402
from PyQt6.QtCore import QSize  # noqa: E402
from PyQt6.QtGui import QColor, QImage, QPainter  # noqa: E402
from PyQt6.QtWidgets import QApplication  # noqa: E402

from livecap.config import Settings  # noqa: E402
from livecap.ui.caption_view import CaptionView  # noqa: E402


@pytest.fixture(scope="session")
def qapp():
    app = QApplication.instance() or QApplication([])
    yield app


def render(settings: Settings, lines, size=(1280, 800), *, source="", status="") -> QImage:
    view = CaptionView(settings)
    view.resize(QSize(*size))
    view._box_alpha = 1.0  # 페이드 애니메이션을 건너뛴 최종 상태
    view._lines = lines
    view._source_text = source
    view._status = status
    img = QImage(QSize(*size), QImage.Format.Format_RGB32)
    painter = QPainter(img)
    view.render(painter)
    painter.end()
    return img


def content_bbox(img: QImage, bg: str) -> tuple[int, int, int, int] | None:
    """배경색과 다른 픽셀의 경계 상자 (left, top, right, bottom)."""
    bg_rgb = QColor(bg).rgb()
    left, top, right, bottom = img.width(), img.height(), -1, -1
    for y in range(img.height()):
        for x in range(img.width()):
            if img.pixel(x, y) != bg_rgb:
                left, right = min(left, x), max(right, x)
                top, bottom = min(top, y), max(bottom, y)
    return None if right < 0 else (left, top, right, bottom)


ONE = [("Good morning everyone.", True)]
TWO = [("We're going to look at the Q3 numbers before moving on to the roadmap.", True)]


def test_empty_content_paints_nothing_but_background(qapp):
    """표시할 텍스트가 없으면 박스 자체를 숨긴다."""
    s = Settings()
    img = render(s, [])
    assert content_bbox(img, s.bg_color) is None


def test_background_fills_the_whole_window(qapp):
    s = Settings(bg_color="#101820")
    img = render(s, [])
    assert img.pixel(0, 0) == QColor("#101820").rgb()
    assert img.pixel(1279, 799) == QColor("#101820").rgb()


def test_one_line_box_is_shorter_than_two_line_box(qapp):
    """박스 높이는 실제 줄 수에 맞춰 자동 조절된다."""
    s = Settings()
    one = content_bbox(render(s, ONE), s.bg_color)
    two = content_bbox(render(s, TWO), s.bg_color)
    assert one is not None and two is not None
    h1 = one[3] - one[1]
    h2 = two[3] - two[1]
    # 두 줄이면 한 줄보다 대략 한 줄 높이만큼 더 크다.
    assert h2 > h1 * 1.5


def test_box_never_exceeds_max_width_setting(qapp):
    s = Settings(box_max_width_pct=60)
    bbox = content_bbox(render(s, TWO), s.bg_color)
    assert bbox is not None
    width = bbox[2] - bbox[0]
    assert width <= 1280 * 0.60 + 2  # 안티에일리어싱 여유


def test_box_is_horizontally_centred(qapp):
    s = Settings()
    bbox = content_bbox(render(s, ONE), s.bg_color)
    assert bbox is not None
    left_gap = bbox[0]
    right_gap = 1280 - bbox[2]
    assert abs(left_gap - right_gap) <= 2


def test_bottom_margin_ratio_holds_across_window_sizes(qapp):
    """창 크기를 바꿔도 여백 비율이 유지된다."""
    s = Settings(bottom_margin_pct=8)
    for w, h in ((1280, 800), (2560, 1440), (1024, 640)):
        bbox = content_bbox(render(s, ONE, size=(w, h)), s.bg_color)
        assert bbox is not None
        margin_ratio = (h - bbox[3]) / h
        assert abs(margin_ratio - 0.08) < 0.02, f"{w}x{h} margin was {margin_ratio:.3f}"


def test_max_width_ratio_holds_across_window_sizes(qapp):
    """폰트는 px 고정이라 박스는 텍스트에 맞춰 줄어든다. 유지되는 것은 '상한' 비율이다."""
    s = Settings(box_max_width_pct=80, max_lines=4)
    saturating = [("word " * 400, True)]  # 어느 창 크기에서도 상한까지 차오르는 길이
    ratios = []
    for w, h in ((1280, 800), (2560, 1600)):
        bbox = content_bbox(render(s, saturating, size=(w, h)), s.bg_color)
        assert bbox is not None
        ratios.append((bbox[2] - bbox[0]) / w)
    for r in ratios:
        assert r <= 0.80 + 0.005
    assert abs(ratios[0] - ratios[1]) < 0.06


def test_box_hugs_short_text_instead_of_filling_max_width(qapp):
    s = Settings(box_max_width_pct=80)
    bbox = content_bbox(render(s, [("Hi.", True)]), s.bg_color)
    assert bbox is not None
    assert (bbox[2] - bbox[0]) < 1280 * 0.40


def test_status_text_is_painted_when_there_are_no_captions(qapp):
    """통역 중 모달 대신 캡션 자리에 상태 문구만 표시한다."""
    s = Settings()
    img = render(s, [], status="재연결 중…")
    assert content_bbox(img, s.bg_color) is not None


def test_captions_win_over_status_text(qapp):
    """캡션이 있으면 상태 문구가 화면을 가리지 않는다."""
    s = Settings()
    with_status = render(s, ONE, status="재연결 중…")
    without = render(s, ONE)
    assert content_bbox(with_status, s.bg_color) == content_bbox(without, s.bg_color)


def test_box_colour_is_applied(qapp):
    s = Settings(box_color="#2E6F95")
    img = render(s, ONE)
    bbox = content_bbox(img, s.bg_color)
    assert bbox is not None
    # 박스 좌측 안쪽(텍스트가 없는 영역)의 픽셀은 박스 색이어야 한다.
    px = QColor(img.pixel(bbox[0] + 8, (bbox[1] + bbox[3]) // 2))
    assert (px.red(), px.green(), px.blue()) == (0x2E, 0x6F, 0x95)


def test_source_text_is_drawn_above_the_box(qapp):
    s = Settings(show_source_text=True)
    without = content_bbox(render(s, ONE), s.bg_color)
    with_src = content_bbox(render(s, ONE, source="곧 시작하겠습니다."), s.bg_color)
    assert without is not None and with_src is not None
    assert with_src[1] < without[1]  # 위쪽으로 더 뻗는다


def test_source_text_hidden_when_setting_is_off(qapp):
    s = Settings(show_source_text=False)
    without = content_bbox(render(s, ONE), s.bg_color)
    with_src = content_bbox(render(s, ONE, source="곧 시작하겠습니다."), s.bg_color)
    assert with_src == without


def test_long_text_is_clipped_to_max_lines(qapp):
    long_line = [("word " * 200, True)]
    two = content_bbox(render(Settings(max_lines=2), long_line), "#000000")
    four = content_bbox(render(Settings(max_lines=4), long_line), "#000000")
    assert two is not None and four is not None
    assert (four[3] - four[1]) > (two[3] - two[1])
