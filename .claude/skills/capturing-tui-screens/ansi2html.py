#!/usr/bin/env python3
"""ansi2html.py: stdin ANSI (SGR colours, bold/faint/italic/underline/reverse) -> an HTML page
that mimics a dark terminal, for a headless-browser screenshot."""
import html, re, sys

BG, FG = "#0f1115", "#e6e8ec"
BASE = ["#000000", "#cd3131", "#0dbc79", "#e5e510", "#2472c8", "#bc3fbc", "#11a8cd", "#e5e5e5",
        "#666666", "#f14c4c", "#23d18b", "#f5f543", "#3b8eea", "#d670d6", "#29b8db", "#ffffff"]

def c256(n):
    if n < 16:
        return BASE[n]
    if n < 232:
        n -= 16
        v = [0, 95, 135, 175, 215, 255]
        return "#%02x%02x%02x" % (v[n // 36], v[n // 6 % 6], v[n % 6])
    g = 8 + (n - 232) * 10
    return "#%02x%02x%02x" % (g, g, g)

def sgr(params, st):
    p = [int(x) if x else 0 for x in params.split(";")] if params else [0]
    i = 0
    while i < len(p):
        c = p[i]
        if c == 0: st.clear()
        elif c == 1: st["b"] = 1
        elif c == 2: st["f"] = 1
        elif c == 3: st["i"] = 1
        elif c == 4: st["u"] = 1
        elif c == 7: st["r"] = 1
        elif c == 22: st.pop("b", None); st.pop("f", None)
        elif c == 23: st.pop("i", None)
        elif c == 24: st.pop("u", None)
        elif c == 27: st.pop("r", None)
        elif 30 <= c <= 37: st["fg"] = BASE[c - 30]
        elif 90 <= c <= 97: st["fg"] = BASE[c - 82]
        elif 40 <= c <= 47: st["bg"] = BASE[c - 40]
        elif 100 <= c <= 107: st["bg"] = BASE[c - 92]
        elif c == 39: st.pop("fg", None)
        elif c == 49: st.pop("bg", None)
        elif c in (38, 48):
            key = "fg" if c == 38 else "bg"
            if i + 1 < len(p) and p[i + 1] == 5 and i + 2 < len(p):
                st[key] = c256(p[i + 2]); i += 2
            elif i + 1 < len(p) and p[i + 1] == 2 and i + 4 < len(p):
                st[key] = "#%02x%02x%02x" % tuple(p[i + 2:i + 5]); i += 4
        i += 1

def style(st):
    fg, bg = st.get("fg", FG), st.get("bg")
    if st.get("r"):
        fg, bg = (bg or BG), fg
    css = ["color:" + fg]
    if bg: css.append("background:" + bg)
    if st.get("b"): css.append("font-weight:700")
    if st.get("f"): css.append("opacity:.6")
    if st.get("i"): css.append("font-style:italic")
    if st.get("u"): css.append("text-decoration:underline")
    return ";".join(css)

out, st = [], {}
for token in re.split(r"(\x1b\[[0-9;]*[A-Za-z])", sys.stdin.read()):
    m = re.fullmatch(r"\x1b\[([0-9;]*)([A-Za-z])", token)
    if m:
        if m.group(2) == "m":
            sgr(m.group(1), st)
        continue
    if token:
        out.append('<span style="%s">%s</span>' % (style(st), html.escape(token)))
print('<!doctype html><meta charset="utf-8"><body style="margin:16px;background:%s">'
      '<pre style="margin:0;font:15px/19px \'DejaVu Sans Mono\',\'Fira Code\',monospace;'
      'font-variant-ligatures:none;color:%s">%s</pre>' % (BG, FG, "".join(out)))
