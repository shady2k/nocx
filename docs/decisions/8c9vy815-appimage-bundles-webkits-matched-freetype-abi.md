# ADR-8c9vy815 — The AppImage bundles WebKit's matched FreeType ABI

- **Status:** Accepted (owner-delegated packaging decision on `nocx-2inid`;
  renderer evidence recorded below; release promotion remains pending)
- **Date:** 2026-10-29
- **Bead:** `nocx-2inid`
- **Touches:** [ADR-0035](0035-appimage-carries-webkits-helper-processes.md)'s
  AppImage dependency closure. Its helper lookup policy and rejection of owning
  WebKit security builds stand. Host fonts remain intentional; FreeType can no
  longer be borrowed from an arbitrary host.

## Context and evidence limits

The released v0.8.0 AppImage carries Ubuntu jammy's WebKitGTK
`2.50.4-0ubuntu0.22.04.1` and its helpers, but no FreeType shared library.
WebKit's own `$ORIGIN` RUNPATH cannot find a library that is absent, so its
`libfreetype.so.6` dependency resolves outside the image. The Ubuntu buildinfo
records FreeType `2.11.1+dfsg-1ubuntu0.3` headers and runtime at WebKit build time
in its [Ubuntu amd64 buildinfo](https://launchpad.net/~ubuntu-security-proposed/+archive/ubuntu/ppa/+build/32114182/+files/webkit2gtk_2.50.4-0ubuntu0.22.04.1_amd64.buildinfo).
The validated WebKit ELF build ID is
`5453dc26598bc698bee8376a0266f979a653dac7`.

FreeType 2.13 changed experimental COLRv1 structures without changing that
SONAME. `FT_ColorStop.stop_offset` changed from a short to a C long, which is
64-bit storage on Linux amd64, and `FT_ColorStopIterator` gained a field.
Old-header local objects passed to the new runtime can therefore corrupt
adjacent data. The bundled Skia path uses these objects without a runtime ABI
guard. This explains the reported `ColorStop` vector assertion and matches an
[independent AppImage repair](https://github.com/home-assistant/installer/pull/366).
The reporter's loaded FreeType, font, glyph and corrupted field remain
unknown. The controlled reproduction described below proves the same assertion,
not the reporter's exact triggering input.

The layout changes are documented by FreeType's commits
[`117df36b`](https://github.com/freetype/freetype/commit/117df36b4c3a5f7850a3789e92ce54d9341673ff)
(`stop_offset`) and
[`3414fef7`](https://github.com/freetype/freetype/commit/3414fef74f9dd1858aca521f93ebadca9ab36f96)
(`read_variable`). WebKit's
[2.50.4 Skia paint path](https://github.com/WebKit/WebKit/blob/webkitgtk-2.50.4/Source/ThirdParty/skia/src/ports/SkFontHost_FreeType_common.cpp#L607-L640)
passes compile-time-layout objects to those runtime symbols without an ABI guard.

No built-in 45-second emoji insertion was found. Pets render PNG sprites;
arbitrary tab titles, terminal output and assistant text can supply Unicode.
Filtering one UI element would not close the unsafe path.

## Decision

**Bundle jammy's security-current FreeType
`2.11.1+dfsg-1ubuntu0.4`, which retains the experimental COLRv1 ABI used to build
this WebKit.** Match the ABI generation, not the superseded `.3` patch version.
Copy the regular library into `AppDir/usr/lib`, provide
`libfreetype.so.6`, ship its licence, and record the exact package version and
hash in build provenance. Packaging must reject an unexpected ABI generation.
The initial package is Ubuntu's
[`libfreetype6_2.11.1+dfsg-1ubuntu0.4_amd64.deb`](https://archive.ubuntu.com/ubuntu/pool/main/f/freetype/libfreetype6_2.11.1+dfsg-1ubuntu0.4_amd64.deb).

The normal loader path must reach the bundle in nocx and its WebProcess,
not merely place a file beside WebKit. WebKit's `$ORIGIN` RUNPATH resolves the
library in `AppDir/usr/lib`; nocx independently has `$ORIGIN/../lib`. Verify
actual process maps per process and reject outside-image FreeType bindings in
the repaired artifact. Keep host fonts and fontconfig configuration; a
compatible ABI preserves colour rendering without banning the user's emoji
fonts.

Do not prepend a bundle-wide `LD_LIBRARY_PATH` in AppRun. That environment would
reach the detached coordinator and every terminal child, handing jammy
libraries to modern host applications. The gate asserts that an actual terminal
child inherits no AppDir path in that variable. Explicit user
`LD_LIBRARY_PATH` or `LD_PRELOAD` overrides are unsupported and can defeat the
pairing; this repair does not sanitise deliberate loader injection. The process
map check still rejects an outside-image FreeType in the measured release run.

## Alternatives and trade-offs

### A newer distro WebKit is not a proven drop-in fix

Ubuntu noble's WebKitGTK 2.52.6
[buildinfo](https://launchpad.net/~ubuntu-security-proposed/+archive/ubuntu/ppa/+build/33529899/+files/webkit2gtk_2.52.6-0ubuntu0.24.04.1_amd64.buildinfo)
lists FreeType 2.13.2, libc6 2.39,
GLib 2.80 and ICU74. Our current jammy build floor is glibc 2.35. Switching to
that prebuilt changes dependency generations and requires a separate support
floor decision. **Buildinfo proves its build environment, not its ELF minimum
GLIBC symbol requirement.** The noble ELF was not inspected; this record does
not claim every symbol requires glibc 2.39.

No confirmed WebKitGTK fixed release was found. Checked tags 2.50.6, 2.52.6 and
2.54.2 still contain the unguarded path. A concrete
[rust-skia fork guard](https://github.com/rust-skia/skia/commit/fb635bc3c03e1cd085dfedbd8b11ab19e4d8d4e5)
compares header/runtime sides of the 2.13 boundary and declines COLRv1 on a
mismatch, but it is not evidence of a WebKit fix. Backporting it through our own
WebKit build buys the security-build obligation ADR-0035 rejected. Waiting for
an unconfirmed upstream release does not protect the current artifact.

### Removing assertions is not a repair

Dropping `_GLIBCXX_ASSERTIONS` cannot change the struct layout or prevent the
out-of-bounds access. It replaces a deterministic abort with undefined
behaviour, including corruption or a later crash. Those assertions are already
compiled into the Ubuntu shared library; nocx compiler flags cannot remove
them. Rebuilding that library only to hide the assertion is rejected.

### A fontconfig exclusion is only an emergency stopgap

Hiding a named Noto family misses other COLRv1 fonts, and downloaded
`@font-face` fonts can bypass system font selection. Broadly denying colour
fonts costs colour emoji throughout nocx and still leaves the incompatible ABI
available to another caller. Such an exclusion would require an explicit,
temporary exception with that user-visible cost; it is not the chosen repair.
App-side emoji replacement has the same coverage problem.

## Consequences and acceptance boundary

This adds a small distro library, licence notices, provenance and loader checks,
not a WebKit build. Security updates within the matched ABI generation must
continue; a future WebKit or FreeType ABI-generation change must revisit the
pairing rather than silently taking the host library.

Packaging pins the validated WebKit package and ELF build ID as well as
FreeType. This makes drift visible during assembly; it is not a substitute for
the renderer gate. **An Ubuntu WebKit security update cannot ship until someone
bumps those pins, records the new buildinfo URL and FreeType header provenance,
and reruns the COLRv1 artifact gate.** That conscious repin is the intended cost:
silent ABI mismatch is worse than an explicit blocked package build. The paint
probe and mismatch mutation protect a future repin from being wrong; the pin
only makes the change require attention.

One loaded FreeType SONAME serves other libraries too. Modern host fontconfig
and HarfBuzz, as well as bundled GTK/Pango/Cairo, must be checked for compatible
FT imports and relocations (`ldd -r` and imported-symbol checks). FreeType itself
does not depend on fontconfig or HarfBuzz. Its PNG and Brotli dependencies are
already bundled; zlib remains host-provided. Bundling unrelated shaping
libraries is not justified by FreeType's own dependency closure.

The acceptance gate belongs to the artifact verification before release upload,
not to Playwright's separate WebKit. On a pinned host with FreeType >=2.13 and
no host WebKit, it must use the shipped engine/helpers, a disposable profile,
and a pinned font whose selected glyph has a verified COLRv1 gradient graph.
Exercise the real AppImage with that font as fallback in a tab title and
terminal text. Report the font path, glyph code points, FreeType path/version,
WebKit build ID and artifact hash; capture WebProcess maps and a screenshot
that contains the glyph rather than tofu. Await paint/snapshot acknowledgement
and a subsequent UI response, not a liveness timer.

Use the same font/glyph for unrepaired and repaired artifacts. The unrepaired
v0.8.0 must show the exact `ColorStop` assertion, not a generic timeout; the
repaired artifact must render successfully with bundled FreeType. Deleting the
bundled library must turn the gate red.

The final controlled rehearsal on Ubuntu 24.04's FreeType 2.13.2 reproduced
the exact original `ColorStop` assertion with the released artifact and with
the repaired artifact's bundled FreeType deleted. The repaired artifact painted
the same fixture and survived, including a subsequent actual-window command
response. Its main and WebProcess mapped the bundled FreeType 2.11.1, while an
actual terminal child inherited an empty `LD_LIBRARY_PATH`. The gate command
exited 0. The same all-three fixture also reproduced the exact original failure
on Arch FreeType 2.15.0; the earlier single grinning-face probe did not. This is
why the gate keeps the warning's linear gradients as well as the grinning
face's radial gradient, rather than treating any COLRv1 glyph as equivalent.

The snapshot's glyph-element bounds come from `getBoundingClientRect`. Its
chromatic pixel counts, hue buckets and mean saturation exceed a same-page
missing-glyph control in those same rectangles. This provides measurable glyph
paint evidence without pretending that an unreviewed screenshot was inspected
by a person. The actual AppImage screenshots and subsequent input response are
separate evidence of the real window path.

These measurements use a controlled repack of the released binaries with only
FreeType, notices and provenance added, not a newly built nightly. They do not
identify the reporter's exact glyph or prove next-nightly promotion. The
[verification guide](../../scripts/appimage/COLRV1-VERIFY.md) records repeatable
commands, fixture and artifact hashes, measured outcomes and their limits. `nocx-2inid` stays open until its
integration/release acceptance boundary.
