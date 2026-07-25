"""Generate the application icon as LiveCap.icns.

Draws the app's own visual language — a dark rounded tile carrying a caption
box and the green status dot — at every size macOS asks for, then hands the
iconset to iconutil.

    python packaging/make_icon.py
"""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
from pathlib import Path

os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")

from PyQt6.QtCore import QRectF, Qt  # noqa: E402
from PyQt6.QtGui import QColor, QImage, QPainter, QPainterPath  # noqa: E402
from PyQt6.QtWidgets import QApplication  # noqa: E402

HERE = Path(__file__).resolve().parent
BG = QColor("#101418")
BOX = QColor("#1B4965")
TEXT = QColor("#FFFFFF")
DOT = QColor("#34C759")

# macOS wants each size at 1x and 2x.
SIZES = [16, 32, 64, 128, 256, 512, 1024]


def render(size: int) -> QImage:
    img = QImage(size, size, QImage.Format.Format_ARGB32_Premultiplied)
    img.fill(Qt.GlobalColor.transparent)
    p = QPainter(img)
    p.setRenderHint(QPainter.RenderHint.Antialiasing, True)
    u = size / 1024.0  # design at 1024 and scale

    # Rounded tile, following the macOS icon grid rather than filling the square.
    tile = QRectF(84 * u, 84 * u, 856 * u, 856 * u)
    path = QPainterPath()
    path.addRoundedRect(tile, 190 * u, 190 * u)
    p.fillPath(path, BG)

    # Caption box, sitting low like it does on screen.
    box = QRectF(170 * u, 545 * u, 684 * u, 250 * u)
    box_path = QPainterPath()
    box_path.addRoundedRect(box, 56 * u, 56 * u)
    p.fillPath(box_path, BOX)

    # Two text bars: one settled, one still arriving.
    p.setPen(Qt.PenStyle.NoPen)
    for i, (width, alpha) in enumerate(((520, 1.0), (360, 0.55))):
        bar = QColor(TEXT)
        bar.setAlphaF(alpha)
        y = 618 * u + i * 92 * u
        r = QRectF(box.center().x() - width * u / 2, y, width * u, 44 * u)
        bar_path = QPainterPath()
        bar_path.addRoundedRect(r, 22 * u, 22 * u)
        p.fillPath(bar_path, bar)

    # Status dot, below the box on the right, as in the running app. Kept clear
    # of the tile's rounded corner, whose arc centre sits at (750, 750).
    p.setBrush(DOT)
    p.drawEllipse(QRectF(746 * u, 811 * u, 88 * u, 88 * u))

    p.end()
    return img


def main() -> int:
    if sys.platform != "darwin":
        print("macOS only — iconutil is required", file=sys.stderr)
        return 1
    app = QApplication([])  # noqa: F841  (Qt needs an instance to paint)

    iconset = HERE / "LiveCap.iconset"
    if iconset.exists():
        shutil.rmtree(iconset)
    iconset.mkdir(parents=True)

    for size in SIZES:
        if size <= 512:
            render(size).save(str(iconset / f"icon_{size}x{size}.png"))
        if size >= 32:
            half = size // 2
            render(size).save(str(iconset / f"icon_{half}x{half}@2x.png"))

    out = HERE / "LiveCap.icns"
    subprocess.run(["iconutil", "-c", "icns", str(iconset), "-o", str(out)], check=True)
    shutil.rmtree(iconset)
    print(f"wrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
