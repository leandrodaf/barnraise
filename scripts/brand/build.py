#!/usr/bin/env python3
"""Builds every Barnraise brand file from one geometry.

The mark is a barn drawn in outline (walls, a barn door with its cross brace,
the hayloft window) under a gambrel roof that floats a little above it: the
roof being raised. Everything here is computed, so the logo, the panel's
icons and the desktop app's icons always agree.

    python3 scripts/brand/build.py      # or: make brand

Needs Inkscape (SVG to PNG, text to outlines), the Inter font installed (the
wordmark is converted to outlines, so the outputs do not need it) and Pillow
(ICO and ICNS). The outputs are committed: building the panel or the desktop
app never runs this.
"""

import io
import math
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

from PIL import Image

ROOT = Path(__file__).resolve().parents[2]
BRAND = ROOT / "assets" / "brand"
PUBLIC = ROOT / "web" / "public"
DESKTOP = ROOT / "desktop" / "build"
APP_ID = "io.github.leandrodaf.barnraise"

# Colours.
INK = "#13141C"        # plate of the app icon
ROOF_ON_DARK = "#FFC857"
BODY_ON_DARK = "#F5A524"
ROOF_ON_LIGHT = "#E8930C"
BODY_ON_LIGHT = "#1C1D29"
TEXT_ON_DARK = "#EDEBE6"
TEXT_ON_LIGHT = "#1C1D29"

# Line weights in mark units (the mark is 176 x 176). Small renders need
# heavier lines: a 1-unit line at 16 px is a tenth of a pixel.
WEIGHTS = {
    "regular": dict(roof=8, gap=8, line=5),    # 128 px and up, the logo
    "medium": dict(roof=12, gap=9, line=7.5),  # 48 to 96 px
    "heavy": dict(roof=16, gap=10, line=9),    # 32 to 40 px, the panel
    # 24 px and below: no hayloft and no brace, only what a few pixels can hold.
    "tiny": dict(roof=26, gap=16, line=16, simple=True),
}
# Outer edge of the gambrel roof: steep lower slopes, shallow upper ones.
ROOF_EDGE = [(40, 150), (66, 90), (128, 50), (190, 90), (216, 150)]
WALL_X, GROUND = 62, 226
BOX = (40, 50, 176, 176)  # x, y, width, height of the mark


def _offset(p, q, d):
    (x1, y1), (x2, y2) = p, q
    dx, dy = x2 - x1, y2 - y1
    n = math.hypot(dx, dy)
    nx, ny = -dy / n, dx / n
    if ny < 0:
        nx, ny = -nx, -ny
    return (x1 + nx * d, y1 + ny * d), (x2 + nx * d, y2 + ny * d)


def _cross(a, b):
    (x1, y1), (x2, y2) = a
    (x3, y3), (x4, y4) = b
    den = (x1 - x2) * (y3 - y4) - (y1 - y2) * (x3 - x4)
    t = ((x1 - x3) * (y3 - y4) - (y1 - y3) * (x3 - x4)) / den
    return x1 + t * (x2 - x1), y1 + t * (y2 - y1)


def _parallel(points, d):
    segs = [_offset(points[i], points[i + 1], d) for i in range(len(points) - 1)]
    return segs, [_cross(segs[i], segs[i + 1]) for i in range(len(segs) - 1)]


def _pts(points):
    return " ".join(f"{x:.2f},{y:.2f}" for x, y in points)


def mark(weight="regular", roof=ROOF_ON_DARK, body=BODY_ON_DARK, extra=""):
    """The mark's shapes in its own coordinates (see BOX)."""
    w = WEIGHTS[weight]
    segs, inner = _parallel(ROOF_EDGE, w["roof"])
    eave_y = ROOF_EDGE[0][1]

    def at_y(seg, y):
        (x1, y1), (x2, y2) = seg
        return x1 + (x2 - x1) * (y - y1) / (y2 - y1), y

    band = ROOF_EDGE + [at_y(segs[-1], eave_y)] + inner[::-1] + [at_y(segs[0], eave_y)]
    # The walls follow the roof at a constant gap, measured to the line's centre.
    wsegs, top = _parallel(ROOF_EDGE, w["roof"] + w["gap"] + w["line"] / 2)

    def at_x(seg, x):
        (x1, y1), (x2, y2) = seg
        return y1 + (y2 - y1) * (x - x1) / (x2 - x1)

    right = 256 - WALL_X
    walls = [(WALL_X, GROUND), (WALL_X, at_x(wsegs[0], WALL_X))] + top + [(right, at_x(wsegs[-1], right)), (right, GROUND)]
    l = w["line"]
    ground = GROUND - l / 2
    d0, d1, dy = 106, 150, 172
    hay = top[1][1] + 24 + (l - 5)
    hs = 12 + (l - 5) * 0.6
    if w.get("simple"):
        door = ""
        window = f'<rect x="106" y="172" width="44" height="{GROUND - 172}" fill="{body}" stroke="none"/>'
    else:
        door = f"M{d0} {ground} V{dy} H{d1} V{ground} M{d0} {dy} L{d1} {ground} M{d1} {dy} L{d0} {ground}"
        window = f'<rect x="{128 - hs / 2:.2f}" y="{hay:.2f}" width="{hs:.2f}" height="{hs:.2f}" rx="1"/>'
    return (
        f'<g fill="none" stroke="{body}" stroke-width="{l}" stroke-linejoin="miter"{extra}>'
        f'<polyline points="{_pts(walls)}"/>'
        f'<path d="M{WALL_X - 10} {ground} H{right + 10} {door}"/>{window}</g>'
        f'<polygon points="{_pts(band)}" fill="{roof}" stroke="{roof}" stroke-width="1.5" stroke-linejoin="round"/>'
    )


def place(inner, size, x, y):
    """Puts the mark at (x, y) with the given height."""
    s = size / BOX[3]
    return f'<g transform="translate({x - BOX[0] * s:.3f},{y - BOX[1] * s:.3f}) scale({s:.5f})">{inner}</g>'


def svg(w, h, body, title="Barnraise"):
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w} {h}" width="{w}" height="{h}" role="img" aria-label="{title}">'
        f"<title>{title}</title>{body}</svg>\n"
    )


def squircle(x, y, size, n=5.0, steps=96):
    """A superellipse: the continuous-corner shape of macOS icons."""
    r = size / 2
    cx, cy = x + r, y + r
    pts = []
    for i in range(steps * 4):
        t = 2 * math.pi * i / (steps * 4)
        c, s = math.cos(t), math.sin(t)
        pts.append((cx + r * math.copysign(abs(c) ** (2 / n), c), cy + r * math.copysign(abs(s) ** (2 / n), s)))
    return "M" + " L".join(f"{px:.2f} {py:.2f}" for px, py in pts) + " Z"


def plate(size, weight, radius=0.225, scale=None, rim=True):
    """The app icon: the mark on a dark rounded square that fills the canvas.

    The faint rim keeps the edge visible on dark taskbars and docks.
    """
    rx = size * radius
    # Small icons give the mark more of the plate.
    m = size * (scale or {"tiny": 0.78, "heavy": 0.64}.get(weight, 0.56))
    rim_svg = (
        f'<rect x=".5" y=".5" width="{size - 1}" height="{size - 1}" rx="{rx - .5:.2f}" fill="none" stroke="#fff" stroke-opacity=".09"/>'
        if rim else ""
    )
    return svg(size, size, f'<rect width="{size}" height="{size}" rx="{rx:.2f}" fill="{INK}"/>{rim_svg}'
               + place(mark(weight), m, (size - m) / 2, (size - m) / 2 + size * 0.01))


def full_bleed(size, weight, scale):
    """Square, no transparency: iOS and Android mask it themselves."""
    m = size * scale
    return svg(size, size, f'<rect width="{size}" height="{size}" fill="{INK}"/>' + place(mark(weight), m, (size - m) / 2, (size - m) / 2))


def macos(size=1024, weight="regular"):
    """macOS (Big Sur and later): an 824 px squircle on a 1024 canvas, with
    room for the system's shadow, per Apple's icon grid."""
    k = size / 1024
    body, off = 824 * k, 100 * k
    m = body * {"tiny": 0.74, "heavy": 0.64}.get(weight, 0.54)
    defs = (
        f'<defs><filter id="s" x="-20%" y="-20%" width="140%" height="140%">'
        f'<feGaussianBlur in="SourceAlpha" stdDeviation="{12 * k:.2f}"/><feOffset dy="{10 * k:.2f}"/>'
        f'<feComponentTransfer><feFuncA type="linear" slope=".32"/></feComponentTransfer>'
        f'<feMerge><feMergeNode/><feMergeNode in="SourceGraphic"/></feMerge></filter>'
        f'<linearGradient id="g" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#1D1E2A"/><stop offset="1" stop-color="{INK}"/></linearGradient></defs>'
    )
    shape = squircle(off, off, body)
    return svg(size, size, defs + f'<path d="{shape}" fill="url(#g)" filter="url(#s)"/>'
               f'<path d="{shape}" fill="none" stroke="#fff" stroke-opacity=".08" stroke-width="{2 * k:.2f}"/>'
               + place(mark(weight), m, (size - m) / 2, (size - m) / 2 + 8 * k))


def wordmark(x, y, size, fill, anchor="start"):
    return (f'<text x="{x:.2f}" y="{y:.2f}" font-family="Inter" font-weight="500" font-size="{size}" '
            f'letter-spacing="{-size * 0.03:.2f}" fill="{fill}" text-anchor="{anchor}">barnraise</text>')


CAP = 0.727  # Inter's cap height, in em
TEXT_W = 4.24  # width of "barnraise" at that tracking, in em (measured)


def lockup(on_dark, bg=None):
    """The mark beside the name: the mark is 1.9 cap heights tall and sits on the baseline."""
    fs = 72
    h = fs * CAP * 1.9
    pad = 32 if bg else 4
    gap = 26
    w = pad + h + gap + fs * TEXT_W + pad
    roof, body, text = (ROOF_ON_DARK, BODY_ON_DARK, TEXT_ON_DARK) if on_dark else (ROOF_ON_LIGHT, BODY_ON_LIGHT, TEXT_ON_LIGHT)
    back = f'<rect width="{w:.0f}" height="{h + 2 * pad:.0f}" fill="{bg}"/>' if bg else ""
    return svg(round(w), round(h + 2 * pad), back + place(mark("regular", roof, body), h, pad, pad) + wordmark(pad + h + gap, pad + h, fs, text))


def stacked(on_dark):
    roof, body, text = (ROOF_ON_DARK, BODY_ON_DARK, TEXT_ON_DARK) if on_dark else (ROOF_ON_LIGHT, BODY_ON_LIGHT, TEXT_ON_LIGHT)
    w, m = 300, 150
    return svg(w, 236, place(mark("regular", roof, body), m, (w - m) / 2, 4) + wordmark(w / 2, 226, 50, text, "middle"))


def social():
    """GitHub's social preview: 1280 x 640, the name centred, room for cropping."""
    w, h, fs = 1280, 640, 120
    mh = fs * CAP * 1.9
    tw = fs * TEXT_W
    total = mh + 44 + tw
    x = (w - total) / 2
    y = (h - mh) / 2 - 24
    tag = ('<text x="640" y="470" font-family="Inter" font-weight="400" font-size="30" fill="#9A9CA8" '
           'text-anchor="middle">AI coding agents, from the queue to the commit on main</text>')
    return svg(w, h, f'<rect width="{w}" height="{h}" fill="{INK}"/>' + place(mark("regular"), mh, x, y)
               + wordmark(x + mh + 44, y + mh, fs, TEXT_ON_DARK) + tag)


# ---------------------------------------------------------------- tooling


def inkscape(*args):
    exe = shutil.which("inkscape")
    if not exe:
        sys.exit("inkscape is needed: apt install inkscape / brew install --cask inkscape")
    subprocess.run([exe, *args], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def outline(src: str) -> str:
    """Converts text to paths, so the file does not depend on Inter."""
    with tempfile.TemporaryDirectory() as d:
        i, o = Path(d, "in.svg"), Path(d, "out.svg")
        i.write_text(src)
        inkscape(str(i), "--export-text-to-path", "--export-plain-svg", "-o", str(o))
        return o.read_text()


def png(src: str, size: int) -> Image.Image:
    with tempfile.TemporaryDirectory() as d:
        i, o = Path(d, "in.svg"), Path(d, "out.png")
        i.write_text(src)
        inkscape(str(i), "-w", str(size), "-h", str(size), "-o", str(o))
        return Image.open(o).convert("RGBA").copy()


def save_png(img: Image.Image, path: Path, opaque=False):
    path.parent.mkdir(parents=True, exist_ok=True)
    if opaque:
        img = img.convert("RGB")
    img.save(path, optimize=True)


def write(path: Path, text: str):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)


def icns(path: Path):
    """Writes an .icns with a PNG for every slot macOS looks up, each drawn at
    its own pixel size (Pillow leaves out 16 and 32 at 1x)."""
    slots = [("icp4", 16), ("icp5", 32), ("icp6", 64), ("ic07", 128), ("ic08", 256), ("ic09", 512),
             ("ic10", 1024), ("ic11", 32), ("ic12", 64), ("ic13", 256), ("ic14", 512)]
    cache = {}
    chunks = b""
    for kind, px in slots:
        if px not in cache:
            buf = io.BytesIO()
            png(macos(1024, weight_for(px)), px).save(buf, format="PNG", optimize=True)
            cache[px] = buf.getvalue()
        data = cache[px]
        chunks += kind.encode() + (8 + len(data)).to_bytes(4, "big") + data
    path.write_bytes(b"icns" + (8 + len(chunks)).to_bytes(4, "big") + chunks)


def weight_for(px):
    return "tiny" if px <= 24 else "heavy" if px <= 40 else "medium" if px <= 96 else "regular"


def icon_at(px):
    """The app icon at a pixel size, with the line weight that size needs."""
    return png(plate(256, weight_for(px)), px)


# ---------------------------------------------------------------- outputs


def main():
    if subprocess.run(["fc-list", "Inter"], capture_output=True, text=True).stdout.strip() == "":
        sys.exit("the Inter font is needed to outline the wordmark: https://rsms.me/inter/")

    for d in (BRAND / "svg", BRAND / "png", PUBLIC, DESKTOP / "windows", DESKTOP / "darwin"):
        d.mkdir(parents=True, exist_ok=True)

    # assets/brand: the sources people use.
    svgs = {
        "mark.svg": svg(176, 176, place(mark(), 176, 0, 0)),
        "mark-on-light.svg": svg(176, 176, place(mark("regular", ROOF_ON_LIGHT, BODY_ON_LIGHT), 176, 0, 0)),
        "mark-white.svg": svg(176, 176, place(mark("regular", "#FFFFFF", "#FFFFFF"), 176, 0, 0)),
        "mark-black.svg": svg(176, 176, place(mark("regular", "#000000", "#000000"), 176, 0, 0)),
        "icon.svg": plate(256, "regular"),
        "icon-small.svg": plate(256, "heavy"),
        "logo-on-dark.svg": outline(lockup(True)),
        "logo-on-light.svg": outline(lockup(False)),
        "logo-stacked-on-dark.svg": outline(stacked(True)),
        "logo-stacked-on-light.svg": outline(stacked(False)),
    }
    for name, text in svgs.items():
        write(BRAND / "svg" / name, text)
    soc = outline(social())
    with tempfile.TemporaryDirectory() as d:
        p = Path(d, "s.svg")
        p.write_text(soc)
        inkscape(str(p), "-w", "1280", "-h", "640", "-o", str(BRAND / "png" / "social-preview.png"))
    save_png(png(plate(256, "regular"), 1024), BRAND / "png" / "icon-1024.png")
    save_png(png(macos(), 1024), BRAND / "png" / "icon-macos-1024.png")

    # web/public: the panel, served by the daemon.
    write(PUBLIC / "favicon.svg", plate(256, "heavy"))  # tabs draw it at 16 to 32 px
    ico = [icon_at(s) for s in (16, 32, 48)]
    ico[-1].save(PUBLIC / "favicon.ico", format="ICO", sizes=[(16, 16), (32, 32), (48, 48)], append_images=ico[:-1])
    save_png(png(full_bleed(180, "regular", 0.6), 180), PUBLIC / "apple-touch-icon.png", opaque=True)
    save_png(icon_at(192), PUBLIC / "icon-192.png")
    save_png(icon_at(512), PUBLIC / "icon-512.png")
    # Maskable: everything inside the central 80 % circle.
    save_png(png(full_bleed(512, "regular", 0.46), 512), PUBLIC / "icon-maskable-512.png", opaque=True)

    # desktop/build: Wails. appicon.png becomes the macOS .icns at build time,
    # so it has the macOS shape; Windows gets its own .ico below.
    save_png(png(macos(), 1024), DESKTOP / "appicon.png")
    win = [16, 20, 24, 32, 40, 48, 64, 96, 128, 256]
    imgs = [icon_at(s) for s in win]
    imgs[-1].save(DESKTOP / "windows" / "icon.ico", format="ICO", sizes=[(s, s) for s in win], append_images=imgs[:-1])
    # Wails makes the bundle's icon from appicon.png alone, downscaling it for
    # every size; `make desktop` puts this one in its place on macOS.
    icns(DESKTOP / "darwin" / "iconfile.icns")
    for s in (16, 22, 24, 32, 48, 64, 128, 256, 512):
        save_png(icon_at(s), DESKTOP / "linux" / "icons" / "hicolor" / f"{s}x{s}" / "apps" / f"{APP_ID}.png")
    write(DESKTOP / "linux" / "icons" / "hicolor" / "scalable" / "apps" / f"{APP_ID}.svg", plate(256, "medium"))

    # The panel's inline mark: heavy lines, the walls in the text colour.
    s = place(mark("heavy", "#F5A524", "currentColor"), 176, 0, 0)
    write(ROOT / "web" / "src" / "lib" / "app" / "BrandMark.svelte",
          "<!-- Generated by scripts/brand/build.py: edit the script, not this file. -->\n"
          "<script lang=\"ts\">\n\tlet { class: cls = '' }: { class?: string } = $props();\n</script>\n\n"
          f'<svg viewBox="0 0 176 176" class={{cls}} aria-hidden="true" focusable="false">{s}</svg>\n')
    print("brand files written")


if __name__ == "__main__":
    main()
