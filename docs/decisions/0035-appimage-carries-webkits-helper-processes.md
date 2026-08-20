# ADR-0035: the AppImage carries WebKitGTK's helper processes, and is patched to look for them inside itself

- **Status:** accepted
- **Date:** 2026-08-20
- **Bead:** `nocx-azxe.7`
- **Touches:** [ADR-0007](0007-cross-platform-auto-update.md) (the Linux support
  envelope it revises), `.github/workflows/release.yml`, `scripts/appimage/`

## Context

v0.2.0's AppImage could not start on Arch. It died before the window existed:

```
ERROR: Unable to spawn a new child process: Failed to spawn child process
"/usr/lib/x86_64-linux-gnu/webkit2gtk-4.1/WebKitNetworkProcess"
(No such file or directory)
SIGTRAP: trace trap … signal arrived during cgo execution
```

WebKitGTK does not render in one process. `libwebkit2gtk` spawns
`WebKitWebProcess` and `WebKitNetworkProcess`, and it finds them at a directory
compiled into the library when the distribution built it — for ubuntu-22.04,
`/usr/lib/x86_64-linux-gnu/webkit2gtk-4.1`. That is an ordinary data string, not
loader metadata: no `RPATH`, `patchelf` or `LD_LIBRARY_PATH` reaches it.

`linuxdeploy-plugin-gtk` bundles the library because `ldd` lists it. The helpers
are separate executables that nothing links, so `ldd` never names them and
nothing copied them. We shipped the library without its helpers.

The consequence is worse than "broken on Arch". On Debian and Ubuntu the missing
files existed on the host, so the bundled library silently borrowed the **host's**
helper binaries, of whatever version the host had. It appeared to work, by
accident, on the two distributions anybody tested — and only there. The README
claimed the opposite in as many words: "does not depend on the host's `libgtk-3`
or `libwebkit2gtk-4.1`".

Upstream has no answer: [wails#4313 / discussions#4320](https://github.com/wailsapp/wails/discussions/4320)
is this crash verbatim, unanswered since May 2025, while the wails documentation
says an AppImage "runs on any Linux distribution".

## What was measured, before deciding

On a container stand, against the real ubuntu-22.04 WebKitGTK 2.50.4 we bundle,
and then against the released artefact on Arch with no GTK and no WebKitGTK
installed:

1. **Copying the helpers into the AppDir at the mirrored absolute path** — what
   wails v3's own `internal/commands/appimage.go` does — **fails identically**.
   The lookup is absolute; the copy is never consulted.
2. **`WEBKIT_EXEC_PATH` does not exist** in a distribution build. The string is
   absent from the shipped library; upstream gates it behind
   `ENABLE(DEVELOPER_MODE)`. `WEBKIT_INJECTED_BUNDLE_PATH` and
   `WEBKIT_DISABLE_SANDBOX_*` are present and redirect neither.
3. The library knows nothing of AppImage: no `APPDIR`, no `APPIMAGE`.
4. **Patching the baked string works**, and is the only thing that does.

## Decision

**Ship the helpers, and patch the bundled library's baked path to an
equal-length relative one, resolved by a chdir in the AppRun.**

```
/usr/lib/x86_64-linux-gnu/webkit2gtk-4.1   (40 bytes, absolute)
usr//lib/x86_64-linux-gnu/webkit2gtk-4.1   (40 bytes, relative)
```

Three properties, each load-bearing:

- **Equal length.** No ELF offset moves, and the library's _other_ baked string —
  this one plus `/injected-bundle/` — keeps its suffix, which a shorter
  replacement would truncate with NUL padding. An upstream report tried
  `sed s|/usr|./usr|g`; that _lengthens_ the string, shifts every byte after it
  and corrupts the file, which is why theirs "did nothing".
- **The doubled slash is padding.** A path containing one addresses the same
  place.
- **Relative, not absolute.** The rejected alternative was an absolute path into
  a fixed `/tmp` directory that the AppRun symlinks into the mount. It works, and
  it was measured working, but it puts a predictable name — derivable from a
  public artefact — into a shared namespace: squattable into a permanent denial
  of service under a sticky `/tmp`, guarded only by a check-then-use race, and
  contested between two copies of one build running from different paths. The
  relative form needs no shared namespace at all.

The chdir lives in an AppRun hook (`zz-nocx-webkit-cwd.sh`, sourced last). It is
invisible to the user: `internal/pty/pty_local.go` `resolveCwd()` already falls
back to `$HOME` when a session names no directory — a GUI app's working directory
is a useless place to start a shell — and `os.Getwd()` appears nowhere in
non-test code. The release gate asserts this as an observable property rather
than trusting this paragraph.

## Consequences

- **Packaging inputs are pinned** — `linuxdeploy` by tag, its GTK plugin by
  commit, `appimagetool` by release, the type-2 runtime by tag, each with a
  SHA-256. The patch asserts an exact byte pattern in a deployed library; on
  `continuous` and `master` that contract could change with no commit here.
- **Stripping is off** (`NO_STRIP=1`) for the whole packaging run. Nothing may
  rewrite an ELF file after the patch. (It also avoids linuxdeploy's bundled
  `strip`, too old for the `.relr.dyn` sections modern toolchains emit.)
- **linuxdeploy deploys; `appimagetool` packages.** A second linuxdeploy
  invocation is not a packaging-only step — it may rescan, redeploy, strip or
  replace the patched inode.
- **The release gate runs the artefact on Arch** under Xvfb with no GTK and no
  WebKitGTK, and asserts a terminal session opens, that both helpers were
  `execve`'d from _inside_ the AppDir, and that no WebKit helper was reached
  outside it. Three mutations must each turn it red. A `--version` check cannot
  replace this: it never creates a webview, which is exactly how v0.2.0 shipped.
- **The injected bundle is shipped but has no red test.** Measured: the app
  reaches a working terminal without it, because our renderer talks over our own
  websocket (AD-1), not through a web extension. It is carried for runtime
  completeness on configurations we do not exercise, and that is the reason —
  not that it happened to be in the source directory.
- **The support envelope is narrower than "self-contained" implied**, and the
  README now says so: fonts and text shaping come from the host on purpose.
- **A native package (AUR/deb/rpm) does not replace this.** There the system's
  own WebKit is a dependency and the baked path is correct by construction — but
  ADR-0007 makes AppImage the only self-updating format, and a native package
  cannot repair an AppImage already on someone's disk. The patch stays
  AppImage-specific and must not be shared with a native-package build.

## Alternatives rejected

- **Do not bundle WebKitGTK; depend on the host's.** The host's newer WebKit
  would be loaded against our bundled ubuntu-22.04 GLib/GTK/libsoup — the classic
  version-skew failure, in the direction that crashes at startup. Making it
  robust means excluding the whole desktop stack, at which point the AppImage is
  a tarball with a launcher and ADR-0007's self-update premise is gone.
- **Bind-mount the AppDir's helper directory over the baked path** in a private
  user namespace. Architecturally cleaner than mutating a library, operationally
  worse: unprivileged user namespaces are disabled or AppArmor-restricted on
  exactly the distributions this is meant to fix.
- **`LD_PRELOAD` shim over the spawn.** More code, more surface, and brittle
  across GLib versions — the interposition point moves between `g_spawn_*`,
  `posix_spawn` and `execve` depending on how GLib was built.
- **Build WebKitGTK ourselves with our own `libexecdir`.** Removes the patch and
  buys a standing obligation to ship timely WebKit security builds. Not a
  commitment a terminal emulator should make.
