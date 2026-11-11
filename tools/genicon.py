"""Generates the WoWSync icons (run: python3 tools/genicon.py).

- cmd/wowsync-gui/Icon.png   1024x1024 app icon (used by `fyne package`)
- internal/gui/tray.svg      monochrome tray glyph (recoloured by Fyne)
"""
import math
from PIL import Image, ImageDraw, ImageFilter

S = 4096  # supersampled canvas
OUT = 1024


def lerp(a, b, t):
    return tuple(int(a[i] + (b[i] - a[i]) * t) for i in range(len(a)))


def arrow_geometry(size, cx, cy, r, start, end, head_len, head_w):
    th = math.radians(end)
    px, py = cx + r * math.cos(th), cy + r * math.sin(th)
    dx, dy = -math.sin(th), math.cos(th)  # clockwise tangent (y down)
    nx, ny = math.cos(th), math.sin(th)
    tip = (px + dx * head_len, py + dy * head_len)
    b1 = (px + nx * head_w, py + ny * head_w)
    b2 = (px - nx * head_w, py - ny * head_w)
    return (px, py), [tip, b1, b2]


def app_icon(path):
    img = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    # Background: rounded square with a vertical gradient.
    grad = Image.new("RGBA", (S, S))
    top, bottom = (40, 62, 110, 255), (12, 18, 36, 255)
    gd = ImageDraw.Draw(grad)
    for y in range(S):
        gd.line([(0, y), (S, y)], fill=lerp(top, bottom, y / S))
    mask = Image.new("L", (S, S), 0)
    m = int(S * 0.06)
    ImageDraw.Draw(mask).rounded_rectangle([m, m, S - m, S - m], radius=int(S * 0.2), fill=255)
    img.paste(grad, (0, 0), mask)

    d = ImageDraw.Draw(img)
    cx = cy = S / 2
    r = S * 0.27
    w = int(S * 0.085)
    gold = (244, 197, 66, 255)
    for start, end in ((205, 335), (25, 155)):
        bbox = [cx - r, cy - r, cx + r, cy + r]
        d.arc(bbox, start, end, fill=gold, width=w)
        # PIL draws the arc width inwards, so the stroke centre is at r - w/2.
        _, tri = arrow_geometry(S, cx, cy, r - w / 2, start, end, S * 0.11, S * 0.095)
        d.polygon(tri, fill=gold)
    # Small centre gem.
    g = S * 0.06
    d.polygon([(cx, cy - g), (cx + g, cy), (cx, cy + g), (cx - g, cy)], fill=(120, 190, 255, 255))

    img = img.resize((OUT, OUT), Image.LANCZOS)
    img.save(path)


def tray_svg(path):
    n = 64
    cx = cy = n / 2
    r = 19
    parts = []
    for start, end in ((205, 335), (25, 155)):
        a0, a1 = math.radians(start), math.radians(end)
        x0, y0 = cx + r * math.cos(a0), cy + r * math.sin(a0)
        x1, y1 = cx + r * math.cos(a1), cy + r * math.sin(a1)
        parts.append(f'<path d="M {x0:.2f} {y0:.2f} A {r} {r} 0 0 1 {x1:.2f} {y1:.2f}" '
                     f'fill="none" stroke="#000" stroke-width="6.5" stroke-linecap="butt"/>')
        _, tri = arrow_geometry(n, cx, cy, r, start, end, 10, 8.5)
        pts = " ".join(f"{x:.2f},{y:.2f}" for x, y in tri)
        parts.append(f'<polygon points="{pts}" fill="#000"/>')
    svg = ('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64">\n  '
           + "\n  ".join(parts) + "\n</svg>\n")
    open(path, "w").write(svg)


if __name__ == "__main__":
    app_icon("cmd/wowsync-gui/Icon.png")
    tray_svg("internal/gui/tray.svg")
