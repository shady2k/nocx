#!/usr/bin/env python3
"""Fetch a pinned, licensed COLRv1 gradient fixture for the AppImage gate.

Run only in verify-in-container.sh's disposable Arch host. No font is vendored.
The download hash is checked before fontTools inspects its paint graph.
"""
import hashlib
import json
from pathlib import Path
import sys
import urllib.request
from xml.sax.saxutils import escape

from fontTools.ttLib import TTFont

COMMIT = "503f3d4950a60b30bd9c646d1b64a9d1237eafbb"
NOTO_COMMIT = "150e8ba1f68036bff1020c17e3f6b09530fd9d8a"
BASE = f"https://raw.githubusercontent.com/googlefonts/color-fonts/{COMMIT}"
FONT_SHA256 = "fc1c3b581d01fcaed3283b9a12a04f784db0a22710e1c196dfb3a52696063fdc"
# Repository Apache-2.0 notice plus the font source's own OFL-1.1.
DOWNLOADS = (
    (f"{BASE}/fonts/noto-glyf_colr_1.ttf", "noto-colrv1.ttf", FONT_SHA256),
    (f"{BASE}/LICENSE", "color-fonts-LICENSE.txt",
     "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30"),
    (f"https://raw.githubusercontent.com/googlefonts/noto-emoji/{NOTO_COMMIT}/fonts/LICENSE",
     "noto-emoji-OFL.txt", "6a73f9541c2de74158c0e7cf6b0a58ef774f5a780bf191f2d7ec9cc53efe2bf2"),
)


def main():
    work = Path(sys.argv[1]).resolve()
    fonts = work / "fixture-fonts"
    fonts.mkdir(parents=True, exist_ok=True)
    for url, name, expected in DOWNLOADS:
        destination = fonts / name
        with urllib.request.urlopen(url, timeout=60) as response:
            content = response.read()
        actual = hashlib.sha256(content).hexdigest()
        if actual != expected:
            raise RuntimeError(f"fixture hash mismatch: {url}: {actual} != {expected}")
        destination.write_bytes(content)
        print(f"FIXTURE_DOWNLOAD url={url} sha256={actual}", flush=True)

    font_path = fonts / "noto-colrv1.ttf"
    font = TTFont(font_path)
    assert font["COLR"].version == 1, "fixture is not COLRv1"
    colr = font["COLR"].table
    records = {r.BaseGlyph: r.Paint for r in colr.BaseGlyphList.BaseGlyphPaintRecord}
    cmap = font.getBestCmap()
    # A cmap entry or COLRv1 table alone does not prove that this glyph reaches
    # the gradient ColorStop code that aborted in the released engine.
    candidates = []
    for codepoint in (0x26A0, 0x1F308, 0x1F600):
        glyph = cmap.get(codepoint)
        if glyph not in records:
            continue
        gradients = []

        def visit(paint):
            # Paint{Linear,Radial,Sweep}Gradient and their variable forms.
            if paint.Format in (4, 5, 6, 7, 8, 9):
                stops = paint.ColorLine.ColorStop
                assert len(stops) >= 2, "gradient has fewer than two ColorStops"
                gradients.append({"format": int(paint.Format), "stops": len(stops)})

        # traverse follows PaintColrLayers and PaintColrGlyph references too.
        records[glyph].traverse(colr, visit)
        candidate = {"codepoint": codepoint, "glyph": glyph, "gradients": gradients,
                     "text": chr(codepoint) + ("\ufe0f" if codepoint == 0x26A0 else "")}
        candidates.append(candidate)
        print("COLRV1_CANDIDATE " + json.dumps(candidate), flush=True)
    assert len(candidates) == 3, "fixture must map all three requested glyphs"
    assert any(c["gradients"] for c in candidates), "no candidate reaches a COLRv1 gradient with >=2 stops"
    # Some emoji are solid-paint graphs. Report each one's graph honestly,
    # while requiring the rendered set to include a verified gradient glyph.
    chosen = next(c.copy() for c in candidates if c["gradients"])
    chosen["candidates"] = candidates
    chosen["text"] = " ".join(c["text"] for c in candidates)
    family = font["name"].getDebugName(1)
    assert family, "font has no family name"
    chosen.update({"family": family, "font": str(font_path), "font_sha256": FONT_SHA256,
                   "commit": COMMIT, "noto_commit": NOTO_COMMIT})
    (work / "fixture.json").write_text(json.dumps(chosen, indent=2) + "\n")
    (work / "glyph.txt").write_text(chosen["text"])
    print("COLRV1_GRADIENT_FIXTURE " + json.dumps(chosen), flush=True)

    # Keep desktop fonts; prefer this fixture for the chosen Unicode scalar.
    config = f"""<?xml version="1.0"?>
<!DOCTYPE fontconfig SYSTEM "urn:fontconfig:fonts.dtd">
<fontconfig>
  <include ignore_missing="no">/etc/fonts/fonts.conf</include>
  <dir>{escape(str(fonts))}</dir>
  <cachedir>{escape(str(work / "font-cache"))}</cachedir>
  <match target="pattern">

    <edit name="family" mode="prepend" binding="strong"><string>{escape(family)}</string></edit>
  </match>
</fontconfig>
"""
    (work / "fonts.conf").write_text(config)
    home = work / "home"
    home.mkdir(exist_ok=True)
    # Real PTY output, not JS injected into a lookalike test window.
    (home / ".bashrc").write_text(
        "export PS1='COLRV1> '\n"
        "printf '\\033]0;COLRV1 " + chosen["text"] + "\\007'\n"
        "printf '\\nCOLRV1 OUTPUT " + chosen["text"] + "\\n'\n"
    )


if __name__ == "__main__":
    main()
