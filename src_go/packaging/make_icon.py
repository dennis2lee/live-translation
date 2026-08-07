#!/usr/bin/env python3
"""Draw the LiveCap icon and emit .png, .icns and .ico.

Minecraft theme, so the icon is genuinely pixel art: a 32x32 grid scaled with
nearest-neighbour sampling, never a smooth vector shrunk down.

The subject is a **world** — which is both what translation reaches across and
what Minecraft calls a save. It is drawn as a blocky planet: ocean, grass-block
coastlines with their dirt underside, lit from the top left. One shape, filling
the frame, which is what survives at 16 px.

Four attempts got here, and the first three are worth remembering:

  * two small speech bubbles facing each other — at icon size they read as a
    pair of eyes;
  * one bubble with a thick arrow — legible, but an arrow says "convert" and
    equally says next, export or play;
  * one bubble with 가 and A — the right *meaning*, but the glyphs were
    hand-plotted bitmaps and looked it;
  * a Minecraft sign with caption bars — clean, but it said "text", not
    "across languages".

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

# Minecraft palette: ocean, grass block, and the light that falls top-left.
SEA_LIT = (0x4E, 0x9B, 0xD9)
SEA = (0x3A, 0x7C, 0xBC)
SEA_DEEP = (0x2B, 0x5E, 0x94)
GRASS_LIT = (0x8B, 0xC3, 0x4A)
GRASS = (0x6A, 0xA8, 0x30)
GRASS_DARK = (0x4F, 0x81, 0x25)
DIRT = (0x87, 0x5A, 0x38)
DIRT_DARK = (0x6B, 0x44, 0x23)

# Three continents, each built from overlapping rectangles so its outline is
# irregular rather than a bar. Deliberately rectangular at the pixel level —
# this is a Minecraft world, so the coastlines are made of blocks.
LANDMASSES = (
    (7, 6, 14, 8), (5, 9, 13, 11), (9, 12, 15, 13),
    (18, 8, 25, 10), (20, 11, 26, 14), (17, 15, 22, 16),
    (6, 18, 12, 20), (8, 21, 14, 23),
)


def blank():
    return [[None] * N for _ in range(N)]


def rect(px, x0, y0, x1, y1, colour) -> None:
    """Inclusive rectangle, clipped to the grid."""
    for y in range(max(0, y0), min(N - 1, y1) + 1):
        for x in range(max(0, x0), min(N - 1, x1) + 1):
            px[y][x] = colour


def inside(x: int, y: int, radius: float) -> bool:
    """Circle test on pixel centres, so the edge steps like Minecraft."""
    dx, dy = x - 15.5, y - 15.5
    return dx * dx + dy * dy <= radius * radius


def draw_planet(px) -> None:
    """The ocean sphere, lit from the top left and darker at the rim."""
    for y in range(N):
        for x in range(N):
            if not inside(x, y, 15.4):
                continue
            if not inside(x, y, 13.6):
                px[y][x] = SEA_DEEP          # the rim, turning away from us
                continue
            # A round highlight where the light strikes, rather than a
            # half-plane: a diagonal cut reads as a wedge, not as a sphere.
            hx, hy = x - 10.0, y - 10.5
            px[y][x] = SEA_LIT if hx * hx + hy * hy <= 30 else SEA


def draw_land(px) -> None:
    """Grass-block continents, clipped to the sphere.

    The colour of a land pixel comes from its neighbours, not from which
    rectangle drew it: exposed at the top means grass, exposed at the bottom
    means the dirt underside. That is what makes overlapping rectangles read as
    one continent with a coastline instead of a stack of bars.
    """
    land = [[False] * N for _ in range(N)]
    for x0, y0, x1, y1 in LANDMASSES:
        for y in range(y0, y1 + 1):
            for x in range(x0, x1 + 1):
                if inside(x, y, 14.2):
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
    draw_planet(px)
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
    (here / "AppIcon.ico").write_bytes(
        ico({s: png(rows, s) for s in (16, 32, 48, 64, 128, 256)}))
    print(f"wrote {here/'AppIcon.png'} and {here/'AppIcon.ico'}")

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
