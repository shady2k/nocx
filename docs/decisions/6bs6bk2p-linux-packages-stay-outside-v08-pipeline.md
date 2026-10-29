# ADR-6bs6bk2p — Linux packages stay outside the v0.8 release pipeline

- **Status:** Accepted (owner decision, 2026-10-27, with the amendments recorded below)
- **Date:** 2026-10-27
- **Related:** [ADR-0007](0007-cross-platform-auto-update.md) (AppImage-only
  self-update), [ADR-0035](0035-appimage-carries-webkits-helper-processes.md)
  (host WebKitGTK versus bundled helper path),
  [ADR-0087](0087-linux-user-installs-use-the-signed-release-manifest.md)
  (signed-manifest AppImage installer),
  [ADR-0097](0097-trunk-based-releases-with-a-nightly-channel.md) (nightly and
  promotion pipeline), task `nocx-1ebrs.66.1.2`, feature `nocx-j3prm`

## Context

The v0.8 outcome is a daily changed-main build and a byte-identical promotion,
not a catalogue of install formats. ADR-0097 removes a measured 40-plus minute
CI queue from release iteration, and that queue is a property of re-running CI,
not of how many formats are wrapped: turning an already-built binary into a deb
or an rpm is minutes of work, and it can run in a job decoupled from promotion,
so an extra format does not lengthen the promotion path. What an extra format
does add is maintenance that outlives the build - repository hosting and key
custody, package revisions that must increase monotonically with every nightly,
per-package signatures and metadata signatures for rpm, install and upgrade
proofs against host WebKitGTK, and a failure mode that can stop a publication
this milestone exists to make routine. The cost is ownership, not queue time,
and this record treats it as ownership.

The AppImage remains the one Linux artifact in that outcome and the only one
updated in-app. Its signed release manifest authenticates the exact bytes
(ADR-0003, ADR-0007, ADR-0087); it is self-contained because host WebKitGTK
versions and helper paths are not a stable runtime contract. ADR-0035's byte
patch is specific to the bundled library and must never touch a build linked to
the host's WebKitGTK.

A native deb, rpm, or Arch package takes the opposite dependency model: it
requires the host GTK/WebKit libraries and lets their own packages provide their
matching helpers. That avoids the AppImage helper-path failure, but it does not
make the artifact self-updating. The owning package manager or store must deliver
updates. A standalone release asset is not a maintained package repository, and
a signed nocx manifest does not itself establish a package manager's trust
chain.

There is no Flatpak packaging, repository, signing, or sandbox policy in the
current tree. Flatpak adds a sandbox permission and runtime-maintenance surface
as well as another build. It is not merely another package extension.

## Decision

**Keep the v0.8 nightly and promotion artifact set unchanged: Linux AppImage only.
Do not add Flatpak, deb, rpm, or AUR builds to the v0.8 nightly or promotion
pipeline.** The format-by-format recommendation and intended later boundary are:

| Format      | Recommendation                                                                                                                                                                                                                                           | Hosting and signing                                                                                                                                                                                                                                                                                                                                 | Update owner                                                                        | Pipeline                                                                                                                                                                                                                                                                               |
| ----------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Flatpak** | **Deferred beyond v0.8** - not refused. It becomes live work when one of the revisit criteria below fires, and it needs a reviewed permission model, a maintained runtime and a store route before it can be built.                                      | No repository or signer is adopted. If reconsidered, target Flathub; Flathub's build/review and repository signing would be the distribution trust path, not the nocx AppImage updater.                                                                                                                                                             | Flatpak/Flathub update mechanism. No in-app update.                                 | Not v0.8; a separate later decision and implementation, only after the conditions above.                                                                                                                                                                                               |
| **deb**     | Recommend a later release, not v0.8. A Debian package is a useful native path, but publishing a lone `.deb` does not provide unattended package-manager updates or a maintained security channel.                                                        | If admitted later, publish through a nocx-owned apt repository with repository metadata signed by a dedicated apt signing key; do not imply that the AppImage manifest signs apt metadata. Host and key custody must be approved before implementation.                                                                                             | `apt`/the configured system package manager. No in-app update.                      | Later, after an owner-approved repository, key custody/rotation, supported Debian/Ubuntu baseline, and package install/upgrade tests exist. **Every nightly's package revision must increase monotonically**, or a manager sees no update.                                             |
| **rpm**     | Recommend a later release, not v0.8, for the same reason as deb; adding a second repository ecosystem now multiplies maintenance and release failure paths without advancing the AppImage update outcome.                                                | If admitted later, publish through a nocx-owned RPM repository with signed repository metadata and a dedicated signing key, separate from the AppImage manifest key. Host and key custody must be approved first.                                                                                                                                   | `dnf`/the configured system package manager. No in-app update.                      | Later, after repository/key ownership, supported distribution baseline, and install/upgrade tests are agreed. **The packages need their own signatures as well as signed repository metadata**, and monotonically increasing revisions per nightly. Do not block v0.8 promotion on it. |
| **AUR**     | Recommend a later, bounded first native-package experiment, ahead of expanding to deb and rpm. It directly serves the Arch use case already recorded in `nocx-j3prm`, while avoiding a project-operated binary repository. It is not a v0.8 deliverable. | A project-maintained PKGBUILD in the AUR points to a versioned GitHub Release source artifact and checks its pinned SHA-256. AUR Git commits are not a nocx publisher signature, and the PKGBUILD is not a signed binary package; the checksum protects the fetched release bytes only if the PKGBUILD itself is trusted. Do not claim AUR signing. | `makepkg`/`pacman` via AUR helper or the user's package workflow. No in-app update. | **First of the later formats** (`-bin`, wrapping the existing release artefact): after a review/update cadence is assigned and build/install/upgrade checks prove host WebKitGTK use and exclusion of the AppImage patch.                                                              |

The recommendations are intentionally not equivalent to admission. The ADR
chooses no additional v0.8 artifact. Separate implementation work must be filed
from this decision, and no later format enters the nightly or promotion artifact
set merely because this record recommends reconsidering it.

### Updater refusal for packaged installs

A non-AppImage Linux install must never fetch or apply an AppImage update. The
current Linux updater refuses when `APPIMAGE` is unset; its refusal text already
says package-manager updates are responsible for deb/rpm, but the implementation
does not establish reliable identification for every packaged form. The product
must present a visible, actionable message for recognized Flatpak, deb, rpm, and
AUR installs: updates come from the named store/package manager, and in-app
updates are unavailable. It must not silently log and continue offering an
in-app update. An unrecognized unpacked or development binary must retain a
separate refusal, not be mislabeled as a package. Detection must not rely on
`APPIMAGE` absence alone to claim a specific package type.

These refusals preserve the boundary: the in-app updater remains AppImage-only;
package managers and stores own native-package updates; and no native build may
run the AppImage helper-path patch or bundle WebKitGTK as though it were an
AppImage.

### What comparable terminals and desktop apps actually do

Recorded because the question "is AppImage-only defensible?" is answered by the
field rather than by preference, and the evidence was gathered for this decision
(a consultation kept at `codex-linux-consult.md` beside the coordinator's run; its
sources are linked here):

- **AppImage plus native packages, both maintained**: Warp (deb/rpm with vendor
  repositories and a signing key, plus AppImage and an Arch archive), WezTerm
  (AppImage, deb/rpm, tarball and Flathub, with a signed apt repository, a nightly
  COPR and distro builds), Cursor (AppImage plus GPG-protected apt/yum
  repositories). Sources: <https://docs.warp.dev/support-and-community/troubleshooting-and-support/updating-warp>,
  <https://wezterm.org/install/linux.html>, <https://cursor.com/docs/get-started/quickstart>.
- **A signed repository _and_ a self-updating AppImage, by one vendor**: Signal,
  which is the closest analogue to nocx's combination.
  <https://signal.org/download/linux/>
- **No upstream Linux binaries at all**: Ghostty, which leaves packaging,
  signing and updates to distributions and community packagers.
  <https://ghostty.org/docs/install/binary>
- **The AppImage risks that survive bundling**: WebKitGTK still meets the host's
  EGL/Mesa/NVIDIA and Wayland/XWayland, so a container or Xvfb gate cannot prove
  physical-GPU compatibility (<https://v2.tauri.app/develop/debug/linux-graphics/>),
  and an AppImage is not a sandbox: Ubuntu 24.04's AppArmor user-namespace
  restriction is a real-desktop test, not a CI one
  (<https://documentation.ubuntu.com/release-notes/24.04/>). Bundled-engine
  security becomes the vendor's duty to track, advisory by advisory
  (<https://webkitgtk.org/security.html>).

## Consequences

- v0.8 has no new Linux publication destination, package signing secret, key
  rotation duty, repository metadata, or package-specific promotion transaction.
  Its nightly and promotion continue to build, verify, sign, and publish only the
  AppImage on Linux.
- This keeps the pipeline's critical path and release failure surface small. The
  cost is that users who require distro-native installation do not get it in
  v0.8, and they must continue using the signed AppImage installer or manual
  AppImage path.
- Native formats remain technically plausible and useful, but each brings
  ongoing package compatibility, install/upgrade testing, and maintenance. AUR
  has no project-controlled signing boundary; apt and rpm require repository
  operations and key custody; Flatpak requires sandbox/runtime ownership.
- The updater's recognized-package messages are a product requirement for any
  future package implementation, not evidence that current code recognizes all
  four formats. The package implementation task must include the user-visible
  refusal and prove the AppImage patch is unreachable from native packaging.
- No build task is created by this document. Once the owner accepts the
  recommendations, the admitted format's build and updater-recognition work is
  filed separately; no format is assumed admitted before that decision.

## Revisit when - criteria that fire without anybody's permission

The owner amended these on 2026-10-27: a criterion of "when a maintainer accepts
the work" never fires, because accepting ongoing work is exactly what nobody
volunteers for. These are measurable instead.

- **Installation failure on a real Linux desktop becomes a pattern**: the
  AppImage fails to start, or to update, for a measurable share of users - failed
  start-ups reported, or affected users counted - rather than a single report.
- **The bundled WebKitGTK falls behind on security**: a security advisory applies
  to the WebKitGTK nocx bundles and the fix is available only in a distribution
  build (or in a newer runtime a package manager would deliver), so staying
  AppImage-only would ship a known-vulnerable engine.
- **The measured packaging and publication time stops being a rounding error**:
  if producing and publishing an extra format costs more than the measured
  minutes it takes today to wrap an already-built binary, the reason not to do it
  has changed from ownership to time, and the record must say which.

Each of these can fire without anyone volunteering: the first two are counts and
advisories, the third is a measurement this record already asks for.

## Later order, if they fire

AUR `-bin` first (it wraps the existing artefact and needs no project-operated
repository), then signed apt, then dnf, and Flathub last - Flathub's permission
model and runtime maintenance are the largest piece of ownership on the list.
Cloudsmith's OSS hosting is the preferred home for the apt and dnf repositories
if that order is ever taken up.
