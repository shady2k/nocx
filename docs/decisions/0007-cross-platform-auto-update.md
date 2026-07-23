# ADR-0007 — Cross-platform auto-update via a platform abstraction

- **Status:** Accepted
- **Date:** 2026-07-23
- **Amends:** ADR-0003 consequence "with Linux deferred (D1), MVP is macOS-only";
  reverses design decision **D1** (macOS only) in
  `docs/superpowers/specs/2026-07-22-distribution-and-updates-design.md`
- **Related:** epic `nocx-a75` (Distribution and updates), `nocx-3dk` (payload
  round-trip + manifest verify), `nocx-a75.3` (auto-update mechanism), `nocx-mbu`
  (Linux distribution — reopened by this ADR); ADR-0003 (integrity model, unchanged)

## Context

The auto-update feature (§7–§8 of the design) was scoped **macOS-only** in D1: a
Wails Linux build is dynamically linked against GTK/WebKitGTK/libsoup/glibc, so an
artefact built on one distribution runs only where those happen to match, and
shipping it means choosing a support envelope for a platform with **no identified
user**. `nocx-mbu` recorded the deferral and its reopening condition: *both* a
person who will actually run it *and* a decision about which distributions are
supported.

One of those conditions is now met. The maintainer runs nocx on Linux and needs
in-app updates there, not only on macOS. The other condition — the support
envelope — is decided here.

The requirement, stated plainly: **one update mechanism, per-platform
implementations behind an abstraction, with room to add Windows later.** This is
not a new pattern for this codebase; it is the `AGENTS.md` engineering rule
(interface-first + DI, modules trivially replaceable) applied to the updater.

**How a comparable app does it.** Tabby (an Electron terminal, already cited in
ADR-0003 for its Windows signing) self-updates through `electron-updater`. On
Linux that path updates **AppImage only** — not `.deb`/`.rpm`, which are left to
the system package manager. The mechanism replaces the *running AppImage file in
place* and relaunches, and it only engages when the app is actually running as an
AppImage (the runtime sets the `APPIMAGE` environment variable; without it the
self-update is a no-op). That is the same shape as our macOS refusals for `dev`
and translocated builds (§7.7).

**Wails v3's built-in updater — evaluated and rejected.** Wails v3 (still
`v3.0.0-alpha` as of 2026-07) ships a native updater that checks GitHub Releases
and swaps-and-relaunches, which looked like it would remove most of this work. It
does not fit. The updater replaces the **running binary file in place**, so it
ships a *bare binary* on every platform: on macOS a bare binary rather than a
`.app`, abandoning the bundle model ADR-0003 and §7 rest on; on Linux a bare
binary that still needs the host `libgtk-3`/`libwebkit2gtk-4.1` and therefore does
**not** solve the dependency-envelope problem AppImage exists to solve. Adopting
it would trade a proper cross-platform distribution for a fragile bare-binary one,
on an alpha framework. We stay on Wails v2 and hand-roll the mechanism below.
Migrating to v3 is a separate decision on its own merits (multi-window, the newer
API), not forced by updates.

## Decision

**The updater is one platform-agnostic transaction core plus a thin `Platform`
seam with per-OS implementations. Linux ships as an AppImage. Windows is left as
an unimplemented seam.**

- **Core (one implementation, all OSes), unchanged from the design:** manifest
  fetch from GitHub `latest`, ed25519 signature verification over the exact bytes
  before parsing (§6), semver comparison, artefact matching, download with
  sha256/size verification, the crash-consistency transaction (journal keyed by
  device+inode, advisory `flock`, `Reconcile`, health check, auto-rollback — §7).
  None of this is OS-specific; device+inode identity and `flock` work on Linux as
  they do on macOS.

- **Seam (`Platform` interface):** `Preflight`, `Extract`, `VerifyExtracted`,
  `Exchange`, `ArtifactID` — the only OS-specific surface.
  - **darwin:** `.app` directory, `ditto` pack/extract, `codesign --verify --deep
    --strict`, `lipo` slice check, atomic exchange via `RENAME_SWAP`
    (`RenameatxNp`). Exactly the design as written.
  - **linux:** a single AppImage file. `Extract` is a fetch + `chmod +x`;
    `VerifyExtracted` checks the executable bit and the ed25519-signed manifest
    hash (there is no OS-level signature to lean on — see Consequences); `Exchange`
    is an atomic same-filesystem `renameat2(RENAME_EXCHANGE)` of the running
    AppImage file (the single-file analogue of the macOS directory swap); a Linux
    refusal mirrors §7.7 — **no `APPIMAGE` env var → not a distributed AppImage
    (dev, unpacked, or a deb/rpm install) → no self-update, with a legible
    message.**
  - **windows:** the seam exists; there is no implementation, and none is built
    now. (Windows also needs a conpty branch in `internal/pty` — a separate gap.)

- **Linux format is AppImage, and only AppImage, for self-update.** A
  self-updating app needs a **self-contained, self-replaceable single file**.
  - `.deb`/`.rpm` update through `apt`/`dnf`, not through an in-app GitHub updater
    — shipping them as the self-update target is a category error, and Tabby /
    `electron-updater` refuse exactly that combination.
  - A bare tarball installs no dependencies and runs only on a matching host; the
    design already rejected it.
  - AppImage (built with `linuxdeploy` + its GTK plugin) bundles the GTK/WebKitGTK
    stack into one file the updater can replace in place. Its cost is a real
    WebKitGTK-versus-host breakage class, accepted below.

- **Support envelope:** the AppImage targets a stated glibc floor and the
  WebKitGTK the GTK plugin bundles, tested on the maintainer's distribution. It is
  "runs on distributions at or above this baseline", **not** "runs everywhere" —
  the honest scope AppImage can deliver.

## Consequences

- **D1 is reversed and `nocx-mbu` reopens.** `docs/architecture.md:179` ("MVP is
  macOS-only; Windows/Linux are Phase 3") is no longer accurate for Linux and is
  amended; Windows stays Phase 3. The design's D1, §7, §9 and §12 are revised to
  describe the `Platform` seam rather than a macOS-only mechanism.

- **ADR-0003's integrity model carries over cleanly — and is now the *whole*
  story on Linux.** D4 already made the ed25519-signed manifest the real integrity
  gate ("integrity is ours, not Apple's"). macOS additionally carries Wails'
  ad-hoc signature; **AppImage has no equivalent OS-level signature**, so on Linux
  integrity rests entirely on the signed manifest plus the artefact sha256. This
  is consistent with D4, not a regression — there was never a publisher signature
  to lose. If a stronger Linux signature chain is ever wanted (a detached artefact
  signature beyond the manifest), that is a future decision.

- **The abstraction solves code structure, not distribution.** The hard,
  load-bearing part is the AppImage build and its support envelope, not where the
  interface boundary sits. WebKitGTK-versus-host is a genuine breakage class the
  seam cannot abstract away.

- **CI grows a Linux build job** (`linuxdeploy` + GTK plugin producing the
  AppImage), and the manifest gains a `linux`/`<arch>`/`AppImage` artefact entry.
  The manifest schema (§6, `artifacts[]` with os/arch/format) already accommodates
  this — no format change, just another entry.

- **`nocx-3dk` re-scopes.** The payload round-trip becomes per-platform behind the
  seam: the macOS `.app` ditto/codesign/lipo round-trip as designed, plus a much
  lighter Linux AppImage check (executable bit and integrity survive a
  download-and-replace; there is no bundle tree or symlink set to preserve). The
  cross-platform manifest verification is unchanged.

- **Windows is cheap to add later and expensive to fake now.** Leaving it as a
  seam with no implementation is deliberate: adding it is a new `Platform` impl
  plus the conpty work, not a core rewrite. Building it speculatively now is the
  YAGNI the design forbids.

## Revisit when

- **WebKitGTK-versus-host breakage forces the envelope to move** — e.g. a class of
  target distributions the bundled WebKitGTK cannot satisfy. That is a
  support-envelope decision, tracked against `nocx-mbu`.
- **Windows becomes a priority.** Then the seam gets its third implementation, and
  the conpty gap in `internal/pty` is addressed alongside it.
- **A Linux signing story becomes warranted** (a detached artefact signature or a
  distro-level trust chain beyond the signed manifest). Today the signed manifest
  is the whole integrity gate on Linux, by the same logic as D4.
- **Wails v3 reaches a stable release and its updater grows a bundle-aware mode**
  (updating a macOS `.app` and a Linux AppImage rather than only a bare binary).
  Then replacing this hand-rolled updater with the framework's is worth
  reconsidering — see the "evaluated and rejected" note in Context for why it was
  not adopted now.
