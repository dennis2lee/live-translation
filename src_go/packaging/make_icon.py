#!/usr/bin/env python3
"""Draw the LiveCap icon and emit .png, .icns and .ico.

Minecraft theme, so the icon is genuinely pixel art: a 32x32 grid scaled with
nearest-neighbour sampling, never a smooth vector shrunk down.

The subject is Minecraft's **filled map** — a parchment sheet with the world
drawn on it. It is an item you can hold, and it is literally a picture of the
world, which is what translation reaches across.

Six attempts got here. The first four never shipped:

  * two small speech bubbles facing each other — at icon size they read as a
    pair of eyes;
  * one bubble with a thick arrow — legible, but an arrow says "convert" and
    equally says next, export or play;
  * one bubble with 가 and A — the right *meaning*, but the glyphs were
    hand-plotted bitmaps and looked it;
  * a Minecraft sign with caption bars — clean, but it said "text", not
    "across languages".

The fifth, a blocky planet, shipped in 1.0.0 and was replaced by this one.

Every one of those died the same death: fine at 1024 px, gone at 16. So the map
is drawn to survive small, and the two decisions that make it are both about
area:

  * the border is two rings, not three. The draft's third ring ate the field.
  * four landmasses, not seven, each built from overlapping rectangles. Seven
    thin ones vanish when the 16 px pass samples every other pixel; four tidy
    rectangles survive but read as a window pane. Overlapping them buys a
    ragged coastline at full size without costing area at small size.

    python3 packaging/make_icon.py

Pure standard library — the PNG and ICO writers are below, so the build has no
image dependency to install.
"""

from __future__ import annotations

import pathlib
import struct
import subprocess
import sys
import zlib

N = 32  # the art grid; every coordinate below is in these units

# Minecraft palette: parchment, ocean, grass block, and the light from top-left.
PARCH = (0xE2, 0xD1, 0xA8)
PARCH_SH = (0xC3, 0xAE, 0x7C)
PARCH_EDGE = (0x8A, 0x74, 0x4C)
SEA = (0x3A, 0x7C, 0xBC)
SEA_DEEP = (0x2B, 0x5E, 0x94)
GRASS_LIT = (0x8B, 0xC3, 0x4A)
GRASS = (0x6A, 0xA8, 0x30)
GRASS_DARK = (0x4F, 0x81, 0x25)
DIRT = (0x87, 0x5A, 0x38)
DIRT_DARK = (0x6B, 0x44, 0x23)

BORDER = (1, 1, 30, 30)   # the parchment sheet, filling the frame
FIELD = (3, 3, 28, 28)    # the drawn part of it

# Four continents, each from overlapping rectangles so the outline is irregular
# rather than a bar. Rectangular at the pixel level on purpose — this is a
# Minecraft map, so the coastlines are made of blocks.
LANDMASSES = (
    (4, 6, 14, 9), (4, 10, 11, 13), (8, 10, 13, 12),
    (18, 5, 27, 9), (20, 10, 27, 13),
    (4, 17, 12, 21), (7, 22, 15, 25),
    (19, 18, 27, 22), (22, 23, 27, 25),
)


def blank():
    return [[None] * N for _ in range(N)]


def rect(px, x0, y0, x1, y1, colour) -> None:
    """Inclusive rectangle, clipped to the grid."""
    for y in range(max(0, y0), min(N - 1, y1) + 1):
        for x in range(max(0, x0), min(N - 1, x1) + 1):
            px[y][x] = colour


def draw_sheet(px) -> None:
    """The parchment and the ocean drawn on it, shaded away from the light."""
    bx0, by0, bx1, by1 = BORDER
    rect(px, bx0, by0, bx1, by1, PARCH_EDGE)
    rect(px, bx0 + 1, by0 + 1, bx1 - 1, by1 - 1, PARCH)

    fx0, fy0, fx1, fy1 = FIELD
    rect(px, fx0, fy0, fx1, fy1, PARCH_SH)   # a hairline where the ink starts
    rect(px, fx0, fy0 + 1, fx1, fy1, SEA)
    for x in range(fx0, fx1 + 1):            # deep water along the far edges
        px[fy0 + 1][x] = SEA_DEEP
        px[fy1][x] = SEA_DEEP
    for y in range(fy0 + 1, fy1 + 1):
        px[y][fx1] = SEA_DEEP


def draw_land(px) -> None:
    """Grass-block continents, clipped to the drawn field.

    The colour of a land pixel comes from its neighbours, not from which
    rectangle drew it: exposed at the top means grass, exposed at the bottom
    means the dirt underside. That is what makes overlapping rectangles read as
    one continent with a coastline instead of a stack of bars.
    """
    fx0, fy0, fx1, fy1 = FIELD
    land = [[False] * N for _ in range(N)]
    for x0, y0, x1, y1 in LANDMASSES:
        for y in range(max(y0, fy0 + 2), min(y1, fy1 - 1) + 1):
            for x in range(max(x0, fx0 + 1), min(x1, fx1 - 1) + 1):
                land[y][x] = True

    def is_land(x: int, y: int) -> bool:
        return 0 <= x < N and 0 <= y < N and land[y][x]

    for y in range(N):
        for x in range(N):
            if not land[y][x]:
                continue
            shaded = not is_land(x + 1, y)   # the sea side away from the light
            if not is_land(x, y - 1):
                px[y][x] = GRASS_DARK if shaded else GRASS_LIT
            elif not is_land(x, y + 1):
                px[y][x] = DIRT_DARK if shaded else DIRT
            else:
                px[y][x] = GRASS_DARK if shaded else GRASS


def grid():
    px = blank()
    draw_sheet(px)
    draw_land(px)
    return px


def scale(rows, size: int) -> bytes:
    """Nearest-neighbour to any size, so the pixels stay square.

    Sampling rather than replicating means the 16 px slot works too: it drops
    every other pixel, which is what pixel art wants at that size and what a
    smooth resampler would ruin.
    """
    out = bytearray()
    for y in range(size):
        row = rows[y * N // size]
        line = bytearray()
        for x in range(size):
            p = row[x * N // size]
            line += bytes((0, 0, 0, 0)) if p is None else bytes((*p, 255))
        out += b"\x00" + line  # filter byte 0 per scanline
    return bytes(out)


def png(rows, size: int) -> bytes:
    def chunk(tag: bytes, data: bytes) -> bytes:
        return (struct.pack(">I", len(data)) + tag + data
                + struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF))

    return (b"\x89PNG\r\n\x1a\n"
            + chunk(b"IHDR", struct.pack(">IIBBBBB", size, size, 8, 6, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(scale(rows, size), 9))
            + chunk(b"IEND", b""))


def ico(pngs: dict[int, bytes]) -> bytes:
    """ICO carrying PNG entries — supported by Windows Vista and later."""
    sizes = sorted(pngs)
    out = struct.pack("<HHH", 0, 1, len(sizes))
    offset = 6 + 16 * len(sizes)
    entries, blobs = b"", b""
    for size in sizes:
        blob = pngs[size]
        entries += struct.pack("<BBBBHHII",
                               0 if size >= 256 else size,
                               0 if size >= 256 else size,
                               0, 0, 1, 32, len(blob), offset)
        offset += len(blob)
        blobs += blob
    return out + entries + blobs


def main() -> None:
    here = pathlib.Path(__file__).parent
    rows = grid()

    (here / "AppIcon.png").write_bytes(png(rows, 1024))

    # The window icon, embedded in the binary by internal/ui/icon.go.
    #
    # It has to be set explicitly or GLFW never sets one: GLFW looks the
    # executable up for a resource *named* GLFW_ICON, and rsrc writes the .ico
    # under a numeric id, so GLFW falls through to Windows' generic default
    # while Explorer reads the real icon straight out of the binary. That is
    # the mismatch — the desktop was right and the title bar was not.
    #
    # 128 because the art is a 32x32 grid: 128 is 4x it exactly, and the sizes
    # Windows actually asks for (16, 32, 64) are exact halvings of that, so
    # nothing the scaler does can soften a pixel that was drawn deliberately.
    (here.parent / "internal" / "ui" / "appicon.png").write_bytes(png(rows, 128))
    (here / "AppIcon.ico").write_bytes(
        ico({s: png(rows, s) for s in (16, 32, 48, 64, 128, 256)}))
    print(f"wrote {here/'AppIcon.png'}, {here/'AppIcon.ico'} "
          f"and {here.parent/'internal'/'ui'/'appicon.png'}")

    iconset = here / "AppIcon.iconset"
    subprocess.run(["rm", "-rf", str(iconset)], check=True)
    iconset.mkdir()
    for size in (16, 32, 128, 256, 512):
        (iconset / f"icon_{size}x{size}.png").write_bytes(png(rows, size))
        (iconset / f"icon_{size}x{size}@2x.png").write_bytes(png(rows, size * 2))
    if subprocess.run(["iconutil", "-c", "icns", str(iconset),
                       "-o", str(here / "AppIcon.icns")]).returncode != 0:
        sys.exit("iconutil failed")
    subprocess.run(["rm", "-rf", str(iconset)], check=True)
    print(f"wrote {here/'AppIcon.icns'}")


if __name__ == "__main__":
    main()
