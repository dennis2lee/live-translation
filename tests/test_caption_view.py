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


def test_empty_content_paints_nothing_when_box_is_not_fixed(qapp):
    """고정 박스를 끄면 표시할 텍스트가 없을 때 박스를 숨긴다 (PLAN.md §6.2)."""
    s = Settings(fixed_box=False)
    img = render(s, [])
    assert content_bbox(img, s.bg_color) is None


def test_background_fills_the_whole_window(qapp):
    s = Settings(bg_color="#101820")
    img = render(s, [])
    assert img.pixel(0, 0) == QColor("#101820").rgb()
    assert img.pixel(1279, 799) == QColor("#101820").rgb()


def test_one_line_box_is_shorter_than_two_line_box(qapp):
    """고정 박스를 끄면 박스 높이가 실제 줄 수에 맞춰 자동 조절된다."""
    s = Settings(fixed_box=False)
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
    s = Settings(box_max_width_pct=80, fixed_box=False)
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


# --- 고정 박스 --------------------------------------------------------------


def text_rows(img: QImage, text_color: str) -> list[int]:
    """글자색 픽셀이 있는 행 번호. 박스와 글자를 구분하기 위해 씀."""
    rgb = QColor(text_color).rgb()
    return [
        y for y in range(img.height())
        if any(img.pixel(x, y) == rgb for x in range(img.width()))
    ]


FIXED = Settings(fixed_box=True)


def test_fixed_box_stays_visible_when_empty(qapp):
    """캡션이 없어도 박스가 유지된다."""
    img = render(FIXED, [])
    assert content_bbox(img, FIXED.bg_color) is not None


def test_fixed_box_height_is_the_same_for_one_and_two_lines(qapp):
    one = content_bbox(render(FIXED, ONE), FIXED.bg_color)
    two = content_bbox(render(FIXED, TWO), FIXED.bg_color)
    empty = content_bbox(render(FIXED, []), FIXED.bg_color)
    assert one is not None and two is not None and empty is not None
    assert one[3] - one[1] == two[3] - two[1] == empty[3] - empty[1]


def test_fixed_box_width_does_not_change_with_text_length(qapp):
    short = content_bbox(render(FIXED, [("Hi.", True)]), FIXED.bg_color)
    long_ = content_bbox(render(FIXED, TWO), FIXED.bg_color)
    empty = content_bbox(render(FIXED, []), FIXED.bg_color)
    assert short is not None and long_ is not None and empty is not None
    assert short[0] == long_[0] == empty[0]
    assert short[2] == long_[2] == empty[2]


def test_fixed_box_position_does_not_move(qapp):
    boxes = [
        content_bbox(render(FIXED, lines), FIXED.bg_color)
        for lines in ([], ONE, TWO, [("Hi.", True)])
    ]
    assert all(b is not None for b in boxes)
    assert len(set(boxes)) == 1


def test_fixed_box_fills_the_configured_max_width(qapp):
    s = Settings(fixed_box=True, box_max_width_pct=70)
    bbox = content_bbox(render(s, ONE), s.bg_color)
    assert bbox is not None
    assert abs((bbox[2] - bbox[0]) - 1280 * 0.70) <= 2


def test_fixed_box_sized_for_two_lines_by_default(qapp):
    """기본 2줄 높이. 3줄 설정이면 그만큼 커진다."""
    two = content_bbox(render(Settings(fixed_box=True, max_lines=2), []), "#000000")
    three = content_bbox(render(Settings(fixed_box=True, max_lines=3), []), "#000000")
    assert two is not None and three is not None
    h2, h3 = two[3] - two[1], three[3] - three[1]
    assert h3 > h2
    # 정확히 한 줄 높이만큼 차이나야 한다 (±2px).
    one = content_bbox(render(Settings(fixed_box=True, max_lines=1), []), "#000000")
    assert one is not None
    assert abs((h3 - h2) - (h2 - (one[3] - one[1]))) <= 2


def test_fixed_box_bottom_anchors_text_so_newest_line_stays_put(qapp):
    """줄 수가 변해도 최신(마지막) 줄의 위치가 그대로여야 한다.

    디센더 유무로 잉크 범위가 달라지므로 마지막 줄은 같은 문구로 맞춰 비교한다.
    """
    last = ("Second line here.", True)
    one_rows = text_rows(render(FIXED, [last]), FIXED.text_color)
    two_rows = text_rows(render(FIXED, [("Good morning everyone.", True), last]), FIXED.text_color)
    assert one_rows and two_rows
    assert abs(max(one_rows) - max(two_rows)) <= 2  # 마지막 줄 하단이 같은 자리
    # 두 줄일 때만 위쪽으로 더 뻗는다.
    assert min(two_rows) < min(one_rows)


def test_fixed_box_shows_status_inside_the_box(qapp):
    with_status = render(FIXED, [], status="재연결 중…")
    without = render(FIXED, [])
    box = content_bbox(without, FIXED.bg_color)
    assert box is not None
    # 상태 문구가 박스 밖으로 삐져나가지 않는다.
    with_bbox = content_bbox(with_status, FIXED.bg_color)
    assert with_bbox == box
    # 그리고 실제로 무언가 더 그려졌다.
    assert with_status != without


# --- 번역 방향 표시 --------------------------------------------------------


def render_view(view, size=(1280, 800)) -> QImage:
    img = QImage(QSize(*size), QImage.Format.Format_RGB32)
    painter = QPainter(img)
    view.render(painter)
    painter.end()
    return img


def make_view(settings: Settings, lines=(), size=(1280, 800)) -> CaptionView:
    view = CaptionView(settings)
    view.resize(QSize(*size))
    view._box_alpha = 1.0
    view._lines = list(lines)
    return view


def test_hint_is_drawn_below_the_caption_box(qapp):
    s = Settings()
    without = make_view(s, ONE)
    with_hint = make_view(s, ONE)
    with_hint.show_hint("KO → EN")

    box = content_bbox(render_view(without), s.bg_color)
    both = content_bbox(render_view(with_hint), s.bg_color)
    assert box is not None and both is not None
    assert both[3] > box[3]  # 아래쪽으로 더 뻗는다


def test_hint_is_drawn_even_with_no_captions(qapp):
    s = Settings(fixed_box=False)
    view = make_view(s, [])
    view.show_hint("EN → KO")
    assert content_bbox(render_view(view), s.bg_color) is not None


def test_hint_starts_fully_opaque_and_hides_cleanly(qapp):
    view = make_view(Settings(), ONE)
    view.show_hint("KO → EN")
    assert view._hint_alpha == 1.0
    view.hide_hint()
    assert view._hint_alpha == 0.0
    assert view._hint_text == ""


def test_hidden_hint_paints_nothing_extra(qapp):
    s = Settings()
    plain = render_view(make_view(s, ONE))
    view = make_view(s, ONE)
    view.show_hint("KO → EN")
    view.hide_hint()
    assert render_view(view) == plain


def test_hint_alpha_property_drives_repaint(qapp):
    s = Settings()
    view = make_view(s, ONE)
    view.show_hint("KO → EN")
    opaque = content_bbox(render_view(view), s.bg_color)
    view.hintAlpha = 0.0
    faded = content_bbox(render_view(view), s.bg_color)
    assert opaque is not None and faded is not None
    assert faded[3] < opaque[3]  # 사라지면 박스 아래 잉크가 없어진다


def test_hint_stays_on_screen_with_a_tiny_bottom_margin(qapp):
    """하단 여백을 좁게 잡아도 화면 밖으로 나가지 않는다."""
    s = Settings(bottom_margin_pct=2)
    view = make_view(s, ONE)
    view.show_hint("KO → EN")
    bbox = content_bbox(render_view(view), s.bg_color)
    assert bbox is not None
    assert bbox[3] <= 799


def test_long_text_is_clipped_to_max_lines(qapp):
    long_line = [("word " * 200, True)]
    two = content_bbox(render(Settings(max_lines=2), long_line), "#000000")
    four = content_bbox(render(Settings(max_lines=4), long_line), "#000000")
    assert two is not None and four is not None
    assert (four[3] - four[1]) > (two[3] - two[1])
