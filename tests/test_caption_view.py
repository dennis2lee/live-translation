"""CaptionView painting, checked by inspecting rendered pixels offscreen.

The window splits into two regions:
  - the caption box, above the bottom margin
  - the strip just below it, holding the direction hint and status indicator

`box_bbox` and `strip_bbox` keep measurements of the two from mixing.
"""

from __future__ import annotations

import os

os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")

import pytest  # noqa: E402
from PyQt6.QtCore import QSize  # noqa: E402
from PyQt6.QtGui import QColor, QImage, QPainter  # noqa: E402
from PyQt6.QtWidgets import QApplication  # noqa: E402

from livecap.config import BoxPosition, Settings  # noqa: E402
from livecap.ui.caption_view import CaptionView, Indicator  # noqa: E402


@pytest.fixture(scope="session")
def qapp():
    app = QApplication.instance() or QApplication([])
    yield app


def make_view(settings: Settings, lines=(), size=(1280, 800), *, source="", status=""):
    view = CaptionView(settings)
    view.resize(QSize(*size))
    view._box_alpha = 1.0  # skip the fade animation, take its end state
    view._lines = list(lines)
    view._source_text = source
    view._status = status
    return view


def render_view(view, size=(1280, 800)) -> QImage:
    img = QImage(QSize(*size), QImage.Format.Format_RGB32)
    painter = QPainter(img)
    view.render(painter)
    painter.end()
    return img


def render(settings: Settings, lines, size=(1280, 800), *, source="", status="") -> QImage:
    return render_view(make_view(settings, lines, size, source=source, status=status), size)


def content_bbox(
    img: QImage, bg: str, *, top_row: int = 0, max_row: int | None = None
) -> tuple[int, int, int, int] | None:
    """Bounding box of pixels differing from the background: left, top, right, bottom."""
    bg_rgb = QColor(bg).rgb()
    last = img.height() - 1 if max_row is None else min(max_row, img.height() - 1)
    left, top, right, bottom = img.width(), img.height(), -1, -1
    for y in range(top_row, last + 1):
        for x in range(img.width()):
            if img.pixel(x, y) != bg_rgb:
                left, right = min(left, x), max(right, x)
                top, bottom = min(top, y), max(bottom, y)
    return None if right < 0 else (left, top, right, bottom)


def _split_row(img: QImage, s: Settings) -> int:
    return int(img.height() * (1 - s.bottom_margin_pct / 100.0))


def box_bbox(img: QImage, s: Settings):
    """The caption box only, excluding the strip below it."""
    return content_bbox(img, s.bg_color, max_row=_split_row(img, s))


def strip_bbox(img: QImage, s: Settings):
    """The strip below the box only."""
    return content_bbox(img, s.bg_color, top_row=_split_row(img, s) + 3)


def text_rows(img: QImage, text_color: str) -> list[int]:
    rgb = QColor(text_color).rgb()
    return [
        y for y in range(img.height())
        if any(img.pixel(x, y) == rgb for x in range(img.width()))
    ]


ONE = [("Good morning everyone.", True)]
TWO = [("We're going to look at the Q3 numbers before moving on to the roadmap.", True)]
FIXED = Settings(fixed_box=True)


# --- background and box ---------------------------------------------------


def test_empty_content_paints_no_box_when_box_is_not_fixed(qapp):
    """With the fixed box off, an empty caption hides the box entirely."""
    s = Settings(fixed_box=False)
    assert box_bbox(render(s, []), s) is None


def test_background_fills_the_whole_window(qapp):
    s = Settings(bg_color="#101820")
    img = render(s, [])
    assert img.pixel(0, 0) == QColor("#101820").rgb()
    assert img.pixel(5, 799) == QColor("#101820").rgb()


def test_one_line_box_is_shorter_than_two_line_box(qapp):
    """With the fixed box off, height follows the real line count."""
    s = Settings(fixed_box=False)
    one = box_bbox(render(s, ONE), s)
    two = box_bbox(render(s, TWO), s)
    assert one is not None and two is not None
    assert (two[3] - two[1]) > (one[3] - one[1]) * 1.5


def test_box_never_exceeds_max_width_setting(qapp):
    s = Settings(box_max_width_pct=60)
    bbox = box_bbox(render(s, TWO), s)
    assert bbox is not None
    assert (bbox[2] - bbox[0]) <= 1280 * 0.60 + 2


def test_box_is_horizontally_centred(qapp):
    s = Settings()
    bbox = box_bbox(render(s, ONE), s)
    assert bbox is not None
    assert abs(bbox[0] - (1280 - bbox[2])) <= 2


def test_bottom_margin_ratio_holds_across_window_sizes(qapp):
    s = Settings(bottom_margin_pct=8)
    for w, h in ((1280, 800), (2560, 1440), (1024, 640)):
        bbox = box_bbox(render(s, ONE, size=(w, h)), s)
        assert bbox is not None
        margin_ratio = (h - bbox[3]) / h
        assert abs(margin_ratio - 0.08) < 0.02, f"{w}x{h} margin was {margin_ratio:.3f}"


def test_max_width_ratio_holds_across_window_sizes(qapp):
    """The font is fixed in px, so the box shrinks to fit. Only the cap holds."""
    s = Settings(box_max_width_pct=80, max_lines=4)
    saturating = [("word " * 400, True)]
    ratios = []
    for w, h in ((1280, 800), (2560, 1600)):
        bbox = box_bbox(render(s, saturating, size=(w, h)), s)
        assert bbox is not None
        ratios.append((bbox[2] - bbox[0]) / w)
    for r in ratios:
        assert r <= 0.80 + 0.005
    assert abs(ratios[0] - ratios[1]) < 0.06


def test_box_hugs_short_text_instead_of_filling_max_width(qapp):
    s = Settings(box_max_width_pct=80, fixed_box=False)
    bbox = box_bbox(render(s, [("Hi.", True)]), s)
    assert bbox is not None
    assert (bbox[2] - bbox[0]) < 1280 * 0.40


def test_box_colour_is_applied(qapp):
    s = Settings(box_color="#2E6F95")
    img = render(s, ONE)
    bbox = box_bbox(img, s)
    assert bbox is not None
    px = QColor(img.pixel(bbox[0] + 8, (bbox[1] + bbox[3]) // 2))
    assert (px.red(), px.green(), px.blue()) == (0x2E, 0x6F, 0x95)


def test_long_text_is_clipped_to_max_lines(qapp):
    long_line = [("word " * 200, True)]
    s2, s4 = Settings(fixed_box=True, max_lines=2), Settings(fixed_box=True, max_lines=4)
    two = box_bbox(render(s2, long_line), s2)
    four = box_bbox(render(s4, long_line), s4)
    assert two is not None and four is not None
    assert (four[3] - four[1]) > (two[3] - two[1])


# --- status text ----------------------------------------------------------


def test_status_text_is_painted_when_there_are_no_captions(qapp):
    s = Settings(fixed_box=False)
    assert box_bbox(render(s, [], status="API key required"), s) is not None


def test_captions_win_over_status_text(qapp):
    s = Settings()
    with_status = render(s, ONE, status="Error")
    without = render(s, ONE)
    assert box_bbox(with_status, s) == box_bbox(without, s)


# --- source text ----------------------------------------------------------


def test_source_text_is_drawn_above_the_box(qapp):
    s = Settings(show_source_text=True)
    without = box_bbox(render(s, ONE), s)
    with_src = box_bbox(render(s, ONE, source="곧 시작하겠습니다."), s)
    assert without is not None and with_src is not None
    assert with_src[1] < without[1]


def test_source_text_hidden_when_setting_is_off(qapp):
    s = Settings(show_source_text=False)
    without = box_bbox(render(s, ONE), s)
    with_src = box_bbox(render(s, ONE, source="곧 시작하겠습니다."), s)
    assert with_src == without


# --- fixed box ------------------------------------------------------------


def test_fixed_box_stays_visible_when_empty(qapp):
    assert box_bbox(render(FIXED, []), FIXED) is not None


def test_fixed_box_height_is_the_same_for_one_and_two_lines(qapp):
    one = box_bbox(render(FIXED, ONE), FIXED)
    two = box_bbox(render(FIXED, TWO), FIXED)
    empty = box_bbox(render(FIXED, []), FIXED)
    assert one is not None and two is not None and empty is not None
    assert one[3] - one[1] == two[3] - two[1] == empty[3] - empty[1]


def test_fixed_box_geometry_never_moves(qapp):
    boxes = [
        box_bbox(render(FIXED, lines), FIXED)
        for lines in ([], ONE, TWO, [("Hi.", True)])
    ]
    assert all(b is not None for b in boxes)
    assert len(set(boxes)) == 1


def test_fixed_box_fills_the_configured_max_width(qapp):
    s = Settings(fixed_box=True, box_max_width_pct=70)
    bbox = box_bbox(render(s, ONE), s)
    assert bbox is not None
    assert abs((bbox[2] - bbox[0]) - 1280 * 0.70) <= 2


def test_fixed_box_grows_by_one_line_height_per_extra_line(qapp):
    heights = []
    for n in (1, 2, 3):
        s = Settings(fixed_box=True, max_lines=n)
        bbox = box_bbox(render(s, []), s)
        assert bbox is not None
        heights.append(bbox[3] - bbox[1])
    assert abs((heights[2] - heights[1]) - (heights[1] - heights[0])) <= 2


def test_fixed_box_bottom_anchors_text_so_newest_line_stays_put(qapp):
    """Descenders change the ink extent, so compare with an identical last line."""
    last = ("Second line here.", True)
    one_rows = text_rows(render(FIXED, [last]), FIXED.text_color)
    two_rows = text_rows(render(FIXED, [("Good morning everyone.", True), last]), FIXED.text_color)
    assert one_rows and two_rows
    assert abs(max(one_rows) - max(two_rows)) <= 2
    assert min(two_rows) < min(one_rows)


def test_fixed_box_shows_status_inside_the_box(qapp):
    with_status = render(FIXED, [], status="API key required")
    without = render(FIXED, [])
    assert box_bbox(without, FIXED) == box_bbox(with_status, FIXED)
    assert with_status != without


# --- status indicator -----------------------------------------------------


def test_indicator_starts_grey(qapp):
    """The app starts with a grey dot."""
    view = make_view(Settings(), [])
    assert view.indicator is Indicator.IDLE


def test_indicator_is_drawn_below_the_box_on_the_right(qapp):
    s = Settings()
    bbox = strip_bbox(render(s, ONE), s)
    assert bbox is not None
    box_right = (1280 + 1280 * s.box_max_width_pct / 100) / 2
    assert abs(bbox[2] - box_right) <= 3  # aligned to the box right edge


def test_indicator_colour_tracks_state(qapp):
    s = Settings()
    seen = {}
    for state, expected in (
        (Indicator.IDLE, (0x6E, 0x6E, 0x6E)),
        (Indicator.LIVE, (0x34, 0xC7, 0x59)),
        (Indicator.ERROR, (0xFF, 0x3B, 0x30)),
    ):
        view = make_view(s, ONE)
        view.set_indicator(state)
        img = render_view(view)
        bbox = strip_bbox(img, s)
        assert bbox is not None
        centre = ((bbox[0] + bbox[2]) // 2, (bbox[1] + bbox[3]) // 2)
        px = QColor(img.pixel(*centre))
        seen[state] = (px.red(), px.green(), px.blue())
        assert seen[state] == expected
    assert seen[Indicator.IDLE] != seen[Indicator.LIVE]


def test_busy_state_pulses_and_others_do_not(qapp):
    view = make_view(Settings(), ONE)
    view.set_indicator(Indicator.BUSY)
    assert view._dot_pulse.state() == view._dot_pulse.State.Running

    view.set_indicator(Indicator.LIVE)
    assert view._dot_pulse.state() != view._dot_pulse.State.Running
    assert view._dot_alpha == 1.0


def test_indicator_position_is_stable_while_captions_change(qapp):
    s = Settings(fixed_box=False)  # the box hugs its content in this mode
    short = strip_bbox(render(s, [("Hi.", True)]), s)
    long_ = strip_bbox(render(s, TWO), s)
    assert short is not None and short == long_


def test_indicator_stays_on_screen_with_a_tiny_bottom_margin(qapp):
    s = Settings(bottom_margin_pct=2)
    bbox = content_bbox(render(s, ONE), s.bg_color)
    assert bbox is not None
    assert bbox[3] <= 799


# --- direction hint -------------------------------------------------------


def test_hint_is_drawn_in_the_strip_left_of_the_indicator(qapp):
    s = Settings()
    without = strip_bbox(render_view(make_view(s, ONE)), s)
    view = make_view(s, ONE)
    view.show_hint("KO → EN")
    with_hint = strip_bbox(render_view(view), s)
    assert without is not None and with_hint is not None
    assert with_hint[0] < without[0]  # extends left of the indicator
    assert with_hint[2] == without[2]  # the indicator has not moved


def test_hint_does_not_overlap_the_indicator(qapp):
    s = Settings()
    view = make_view(s, ONE)
    view.show_hint("KO → EN")
    img = render_view(view)
    dot_left = (1280 + 1280 * s.box_max_width_pct / 100) / 2 - view._dot_radius() * 2
    hint = content_bbox(
        img, s.bg_color, top_row=_split_row(img, s) + 3, max_row=799
    )
    assert hint is not None
    gap_col = int(dot_left - 4)
    column = [img.pixel(gap_col, y) for y in range(_split_row(img, s) + 3, 800)]
    assert all(px == QColor(s.bg_color).rgb() for px in column)  # the gap is empty


def test_hint_is_drawn_even_with_no_captions(qapp):
    s = Settings(fixed_box=False)
    view = make_view(s, [])
    view.show_hint("EN → KO")
    before = strip_bbox(render_view(make_view(s, [])), s)
    after = strip_bbox(render_view(view), s)
    assert before is not None and after is not None
    assert after[0] < before[0]


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
    opaque = strip_bbox(render_view(view), s)
    view.hintAlpha = 0.0
    faded = strip_bbox(render_view(view), s)
    assert opaque is not None and faded is not None
    assert faded[0] > opaque[0]  # once the hint fades only the dot remains


# --- vertical position ----------------------------------------------------


def full_bbox(img: QImage, s: Settings):
    """Whole-window bounding box — the box can now be anywhere vertically."""
    return content_bbox(img, s.bg_color)


def test_default_position_is_bottom():
    assert Settings().position is BoxPosition.BOTTOM


def test_top_position_puts_the_box_in_the_upper_half(qapp):
    s = Settings(box_position="top")
    bbox = full_bbox(render(s, ONE), s)
    assert bbox is not None
    assert bbox[3] < 800 / 2, "box should end above the midpoint"


def test_bottom_position_puts_the_box_in_the_lower_half(qapp):
    s = Settings(box_position="bottom")
    bbox = full_bbox(render(s, ONE), s)
    assert bbox is not None
    assert bbox[1] > 800 / 2, "box should start below the midpoint"


def test_middle_position_straddles_the_centre(qapp):
    s = Settings(box_position="center")
    bbox = full_bbox(render(s, ONE), s)
    assert bbox is not None
    centre = (bbox[1] + bbox[3]) / 2
    # The strip below the box drags the measured centre down a little.
    assert abs(centre - 400) < 90, f"box centred at {centre}, expected near 400"


def test_the_three_positions_are_ordered_down_the_screen(qapp):
    tops = []
    for position in ("top", "center", "bottom"):
        s = Settings(box_position=position)
        bbox = full_bbox(render(s, ONE), s)
        assert bbox is not None
        tops.append(bbox[1])
    assert tops[0] < tops[1] < tops[2]


def test_top_margin_follows_the_margin_setting(qapp):
    """bottom_margin_pct becomes the top margin when anchored to the top."""
    near = full_bbox(render(Settings(box_position="top", bottom_margin_pct=2), ONE),
                     Settings(box_position="top", bottom_margin_pct=2))
    far = full_bbox(render(Settings(box_position="top", bottom_margin_pct=20), ONE),
                    Settings(box_position="top", bottom_margin_pct=20))
    assert near is not None and far is not None
    assert far[1] > near[1], "a bigger margin should push the box further down"


def test_a_top_box_stays_fully_on_screen(qapp):
    s = Settings(box_position="top", max_lines=4, font_size=96)
    bbox = full_bbox(render(s, ONE), s)
    assert bbox is not None
    assert bbox[1] >= 0 and bbox[3] < 800


def test_position_does_not_change_the_box_width(qapp):
    widths = []
    for position in ("top", "center", "bottom"):
        s = Settings(box_position=position)
        bbox = full_bbox(render(s, ONE), s)
        widths.append(bbox[2] - bbox[0])
    assert max(widths) - min(widths) <= 2  # only the strip differs


def test_the_indicator_follows_the_box_to_the_top(qapp):
    """The dot sits just below the box, wherever the box is."""
    s = Settings(box_position="top", fixed_box=True)
    view = make_view(s, [])
    view.set_indicator(Indicator.LIVE)
    bbox = full_bbox(render_view(view), s)
    assert bbox is not None
    assert bbox[3] < 800 / 2, "box and its indicator should both be in the top half"
