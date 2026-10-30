# AppImage COLRv1 release check

`verify-appimage.sh <AppImage>` extends the existing Arch/Xvfb release gate.
Do not run `verify-in-container.sh` on a workstation. It installs packages and
mutates its extracted AppImage. The normal gate runs the repaired artifact
first, then removes FreeType and each helper/path repair in fresh containers.
An optional list after the artifact selects mutations for diagnosis.
`verify-appimage.sh original-v0.8.0.AppImage unbundled-baseline` asserts that
no FreeType ELF exists anywhere in the original artifact, then applies the
same fixture/probe and exact assertion requirement as `no-freetype`. It skips
repaired-package provenance and is deliberately not a default mutation.

The `none` case checks the exact FreeType package provenance shipped in the
image, modern-host relocations (`ldd -r`), and main/WebKitWebProcess memory
maps. Every repaired process must map the image's `usr/lib/libfreetype.so.6`.
Host FreeType must be >=2.13. The host has no GTK or WebKit package.

`colrv1-fixture.py` downloads, rather than vendors, the following test font:

- `googlefonts/color-fonts` commit
  `503f3d4950a60b30bd9c646d1b64a9d1237eafbb`,
  `fonts/noto-glyf_colr_1.ttf`.
- SHA256 `fc1c3b581d01fcaed3283b9a12a04f784db0a22710e1c196dfb3a52696063fdc`.
- The repository's Apache-2.0 LICENSE is downloaded and hash checked.
- Its Noto source submodule is pinned at
  `150e8ba1f68036bff1020c17e3f6b09530fd9d8a`. That source's `fonts/LICENSE`
  (SIL OFL 1.1) is also downloaded and hash checked. Both notices stay beside
  the downloaded fixture. No modified font is distributed.

FontTools traverses each candidate glyph's complete COLRv1 paint graph.
It reports each of U+26A0+FE0F, U+1F308 and U+1F600, including solid-only
graphs, and requires a gradient with at least two ColorStops in the set. It
prints code points, glyph names, font family, path, hash and each gradient
format/stop count. A COLRv1 table alone
or a cmap entry is not enough.

`colrv1-probe.py` loads only the image's GTK/WebKit libraries and helpers.
It first reports real loader maps while the renderer is alive, then loads the
font with `@font-face`, paints all three glyphs in DOM and canvas, waits for two
animation frames, and takes a WebKit/Cairo snapshot. DOM glyph bounds and canvas
bounds come from their own `getBoundingClientRect`. DOM glyphs, canvas rainbow
and canvas grinning face must contain colour pixels. U+26A0 is allowed text
presentation on canvas only; its full FE0F DOM sequence still requires colour.
The grinning-face canvas graph contains a verified radial gradient, so a solid
rainbow alone cannot satisfy the canvas check. A second snapshot replaces DOM
glyphs with monospace U+10FFFF missing-glyph controls. Each colour glyph must
exceed its control's chromatic pixel count, hue buckets and mean saturation.
Each U+10FFFF control crop must independently have exactly zero chromatic
pixels, hue buckets and chromatic saturation.
Cairo pixels are inspected directly, without host image-library dependencies. A new JS message after the snapshot
proves the renderer still responds. The `no-freetype` mutation must instead
produce a nonzero result AND the exact `colrv1_configure_skpaint` / `ColorStop`
vector `__n < this->size()` assertion. Generic crashes or timeouts fail the
gate rather than passing the negative control.

The same font is installed in isolated fontconfig fallback for the actual
AppImage. The gate checks `fc-match` for both sans and mono. The actual PTY
shell prints the glyph in an OSC title and output. The gate captures the
actual window and sends a subsequent command through X11 keyboard events.
The command must produce a response file, and the renderer must still map the
bundled FreeType afterwards. This checks survival and input response, not
semantic screenshot recognition of the actual app's tab and grid: inspect the
two AppImage PNGs to confirm those glyphs visibly appear. The standalone
shipped-engine DOM/canvas snapshots do have automated colour-pixel checks.

Evidence stays in `<artifact directory>/appimage-verification/<mutation>/`.
Set `NOCX_APPIMAGE_VERIFY_EVIDENCE` to choose another directory. Logs print
in the gate output. The font itself is not copied to retained evidence;
`fixture.json` identifies the exact bytes and selected glyph.

## Optional host comparison

The default foreign host stays Arch. `NOCX_APPIMAGE_VERIFY_IMAGE=ubuntu:24.04`
uses the same harness with apt dependencies and explicit no-host-GTK/WebKit
assertions. Printouts identify the actual FreeType version. A successful paint
is not accepted as a negative-control reproduction.

## Diagnostic trial limits

During diagnosis an optional richer fixture trial selected a 13-stop linear
gradient glyph, a 101-layer glyph and shaped ZWJ sequences. The original image
also produced the exact ColorStop assertion for those static scenarios. A
separate variable-font trial reached fixture preparation, but its default
coordinate snapshot did not prove colour rendering; the maximum coordinate
scenario did not run. Variable rendering remains unproven. The experimental
helper and wiring were removed rather than retained as an unvalidated release
check. The final bounded gate uses the three verified default glyphs only.

## Recorded ABI regression rehearsal

[ADR-8c9vy815](../../docs/decisions/8c9vy815-appimage-bundles-webkits-matched-freetype-abi.md)
owns this packaging decision. On 2026-10-29 the controlled rehearsal used these
artifacts, not a newly built nightly:

- Released `nocx-0.8.0-linux-amd64.AppImage`, SHA256
  `99054eaf8e8eb93fa3d61f89aa77c7c13d1b365a76209ef4d438117fb1f2370f`.
- A fresh extraction of that image, repacked with matched FreeType, its notices
  and package provenance; released executable, WebKit and helper bytes unchanged.
  SHA256 `96a54550a1adaf1b974310446f90471365a3ce82e88280f8ffccb4ed14a8adf1`.
- Jammy `libfreetype6` version `2.11.1+dfsg-1ubuntu0.4`, archive SHA256
  `3728210ef44a7a51b3824d5c499f6fbd428584a42a65aee15794a4264c8c173a`;
  copied ELF SHA256
  `c338538f8d90eb85217e8525ab363e68e79b46d3c8acc0b6824190e032687e5f`.

The actual pinned font graphs are:

| Text          | Glyph     | Gradient paints                      |
| ------------- | --------- | ------------------------------------ |
| U+26A0 + FE0F | `uni26A0` | two linear gradients, two stops each |
| U+1F308       | `u1F308`  | none; solid-colour companion         |
| U+1F600       | `u1F600`  | one radial gradient, three stops     |

The original and deletion cases reproduced the exact ColorStop assertion on
Arch FreeType **2.15.0** and Ubuntu 24.04 FreeType **2.13.2**. Their WebProcess
maps respectively named `/usr/lib/libfreetype.so.6.20.7` and
`/usr/lib/x86_64-linux-gnu/libfreetype.so.6.20.1`. The probe exited **1** and
reported `WEB_PROCESS_TERMINATED`; the negative-control harness exited **0**
after requiring all of `colrv1_configure_skpaint`, `ColorStop` and
`Assertion '__n < this->size()' failed` on one assertion line. A single
U+1F600 probe had survived, so it is not an equivalent regression fixture.

With the matched bundle, main and WebProcess mapped the image's regular
`usr/lib/libfreetype.so.6`, version **2.11.1**, before and after painting.
Modern host fontconfig/HarfBuzz passed `ldd -r` with that pairing. An actual
terminal child reported empty `LD_LIBRARY_PATH`; no global AppRun loader-path
export is needed. The window accepted a subsequent command through keyboard
input after title/output. Explicit user loader overrides remain unsupported.

The Ubuntu DOM snapshot measured each span's own bounds:

| Text     | Crop x/y/w/h  | Chromatic pixels | Hue buckets | Mean saturation | U+10FFFF control pixels/hues/saturation |
| -------- | ------------- | ---------------- | ----------- | --------------- | --------------------------------------- |
| warning  | 0/0/200/160   | 4118             | 2           | 0.80659         | 0/0/0                                   |
| rainbow  | 200/0/200/160 | 4914             | 17          | 0.85527         | 0/0/0                                   |
| grinning | 400/0/200/160 | 5438             | 4           | 0.80562         | 0/0/0                                   |

Repeat the product gate with a freshly assembled image:

```sh
scripts/appimage/verify-appimage.sh /path/to/repaired.AppImage
NOCX_APPIMAGE_VERIFY_IMAGE=ubuntu:24.04 \
  scripts/appimage/verify-appimage.sh /path/to/repaired.AppImage none no-freetype
scripts/appimage/verify-appimage.sh /path/to/released-0.8.0.AppImage unbundled-baseline
```

The full five-case Arch command exited **0**. The Ubuntu positive/deletion pair
exited **0**. The original baseline check on each host exited **0**, meaning the
expected exact product failure was observed, not that the original renderer
passed. After adding explicit zero-control assertions, the final-source Arch
`none no-freetype` pair again exited **0**. Commit hooks and present-document
checks passed. Go/vitest/Playwright product suites were left to integration/CI.

This establishes a controlled shipped-engine red/green/deletion result, not
next-nightly promotion or a reporter-host retest. The reporter's exact font,
glyph, loaded path and overwritten local field remain unknown. Actual-window
screenshots have not been semantically inspected; its response proves survival,
while the controlled shipped-engine snapshots prove glyph colour painting.

## Staging-script failure checks

Run `python3 scripts/appimage/test-stage-freetype.py` for the bounded shell-level
contract tests. They execute the real staging script with local PATH doubles
for fetch/package boundaries, never a network download. The checksum-negative
case uses real `sha256sum`; deeper cases cross a controlled archive-checksum
boundary and still run the real copied-library ctypes ABI check against tiny
compiled C API fixtures. They require Bash, Python, coreutils and a C compiler.

The suite rejects download failure, a wrong archive hash, wrong package name,
version or architecture, extraction failure, missing library or copyright,
a non-ELF copied file, and a runtime reporting FreeType 2.13 rather than 2.11.
Every failure must name the failed check, return nonzero and omit provenance.
A paired successful 2.11 fixture must copy a regular SONAME, notice and provenance.
These are staging contract tests, not substitutes for the renderer artifact gate.
