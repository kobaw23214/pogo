#!/usr/bin/env python3
"""Regenerates the vision-probe images (color.png, digit.png, count.png).

The probe needs pictures whose content is known by construction, not by
annotation: a solid colour, a printed digit, a count of shapes. They are drawn
here with no dependencies beyond the standard library so anyone can reproduce
the bytes, and the answers in web/visionprobe.go stay in lockstep with what
the pixels say.

Run from this directory:  python gen.py
"""

import struct
import zlib

SIZE = 256
BLACK = (17, 17, 17)
WHITE = (247, 247, 247)
VIOLET = (138, 43, 226)  # blueviolet — uncommon enough not to be a lucky guess


def write_png(path, pixels):
    raw = b"".join(
        b"\x00" + bytes(v for px in row for v in px) for row in pixels
    )

    def chunk(tag, data):
        body = tag + data
        return (
            struct.pack(">I", len(data))
            + body
            + struct.pack(">I", zlib.crc32(body) & 0xFFFFFFFF)
        )

    ihdr = struct.pack(">IIBBBBB", SIZE, SIZE, 8, 2, 0, 0, 0)
    with open(path, "wb") as f:
        f.write(
            b"\x89PNG\r\n\x1a\n"
            + chunk(b"IHDR", ihdr)
            + chunk(b"IDAT", zlib.compress(raw, 9))
            + chunk(b"IEND", b"")
        )


def solid(color):
    return [[color] * SIZE for _ in range(SIZE)]


def rect(px, x0, y0, x1, y1, color):
    for y in range(y0, y1):
        for x in range(x0, x1):
            px[y][x] = color


def circle(px, cx, cy, r, color):
    for y in range(cy - r, cy + r):
        for x in range(cx - r, cx + r):
            if (x - cx) ** 2 + (y - cy) ** 2 <= r * r:
                px[y][x] = color


# digit.png — a seven-segment "7": top bar plus the right-hand uprights.
px = solid(WHITE)
t = 34  # stroke thickness
rect(px, 48, 48, 208, 48 + t, BLACK)            # top
rect(px, 208 - t, 48, 208, 208, BLACK)          # right upright, full height
write_png("digit.png", px)

# count.png — exactly three filled circles.
px = solid(WHITE)
circle(px, 78, 128, 44, BLACK)
circle(px, 178, 88, 44, BLACK)
circle(px, 178, 168, 44, BLACK)
write_png("count.png", px)

# color.png — one full-bleed swatch.
write_png("color.png", solid(VIOLET))
