# ADR-0003 — Distribution without a Developer ID

- **Status:** Accepted
- **Date:** 2026-07-23
- **Related:** epic `nocx-a75` (Distribution and updates), `nocx-a75.1` (release
  builds), `nocx-a75.3` (auto-update); design
  `docs/superpowers/specs/2026-07-22-distribution-and-updates-design.md` (D3, D4,
  §6)

## Context

nocx has to reach other machines and stay current, and it must do so with **no
Apple Developer ID** — there is none, and there will not be one. That single
constraint removes the options a macOS distribution story normally assumes:

- **Notarization requires a Developer ID**, which requires enrolment in the
  Apple Developer Program. An unenrolled team is refused outright.
- **No third-party service signs in its own name.** SignPath (free for open
  source, used by Tabby on Windows) *stores and applies your* certificate; it
  does not issue one.
- **Homebrew has closed the workaround.** `--no-quarantine` has been removed from
  `brew`, and from **2026-09-01** every cask must pass Gatekeeper. The official
  tap already forbids casks that require SIP or Gatekeeper to be disabled, and a
  private tap inherits the same rule — the bypass mechanism no longer exists
  anywhere in `brew`.
- **A free Apple ID** yields only `Apple Development` certificates for local work
  — neither Developer ID nor notarization.

What is absent is **publisher identity, not every signature**. Wails v2.13
unconditionally runs `codesign --force --deep --sign -` on a production bundle,
so the shipped `.app` carries an **ad-hoc** signature: it proves no publisher and
satisfies no Gatekeeper policy, but it is real, macOS can validate its integrity,
and the packaging pipeline preserves and verifies it (`ditto`, not `zip`).

## Decision

**Ship unsigned builds through GitHub Releases, and own update integrity
ourselves.**

- Developer ID signing and notarization are **out of scope** (D3). What is out of
  scope is *distribution* signing; Wails' ad-hoc signature is existing behaviour
  that the build preserves, not something added.
- The **first install requires a manual `xattr -dr com.apple.quarantine`**. There
  is no way to avoid this without a certificate; the README documents it against
  `/Applications/nocx.app`.
- **Updates are not gated by Apple.** The updater fetches the `.zip` itself, and
  a Go HTTP transfer does not set the quarantine attribute (browsers do), so a
  fetched-and-installed build launches without a repeat of the manual step. This
  is standard macOS behaviour rather than a guarantee — the updater treats an
  unexpected quarantine attribute as a detected condition with a remediation
  message, and it is verified on real hardware (design §13).
- **Integrity is a signed manifest, not Gatekeeper** (D4). CI signs `manifest.json`
  with an **ed25519** key held in the `RELEASE_SIGNING_KEY` secret; the binary
  compiles in a **keyring** of accepted public keys and verifies the detached
  signature over the exact manifest bytes before parsing any JSON.

## Consequences

- **The build is Homebrew-ineligible by design.** Given the 2026-09-01 rule, a
  cask is not a route we can take without a certificate, so the `.dmg`/`.zip` on
  the Releases page is the distribution channel, full stop.
- **The threat model is explicit** (D4). The signed manifest protects against a
  tampered artefact and against anyone who can serve bytes but cannot sign. It
  does **not** protect against:
  - **a compromised repository or CI** — write access means signing whatever you
    like; this is accepted, not defended.
  - **update freezing** — serving an older, validly-signed manifest indefinitely,
    which the updater reads as "already current". Defending against that needs
    signed freshness metadata and a persisted release watermark (TUF in
    miniature), which is **not built**. The gap is recorded here rather than
    papered over.
- **Key rotation is not lossless** (§6). The keyring lets a client that has
  upgraded past a key-introducing release survive a rotation, but a binary that
  only ever knew key A cannot authenticate a manifest signed solely by key B, and
  a `latest`-only endpoint can never hand it an intermediate release. Retiring a
  key therefore means **clients older than the release that introduced its
  successor must reinstall by hand**. Acceptable here.
- **Key loss is terminal for in-place updates.** Losing the private key with no
  successor already shipped in a keyring means existing installs can never be
  updated in place again. The private key therefore lives in a **backup outside
  GitHub**, and a new key is introduced into the keyring *before* it is ever
  needed to sign.
- **`vision.md` is amended, `architecture.md:179` is not.** "No app-store or
  packaged distribution" becomes GitHub Releases + unsigned builds + a
  self-hosted update chain + the manual quarantine step. With Linux deferred
  (D1), "MVP is macOS-only" stays accurate.

## Revisit when

- **An Apple Developer ID becomes available.** Then notarization is back on the
  table and the manual quarantine step and the whole manual-integrity argument
  can be retired — but that is a real programme (bundle signing order, hardened
  runtime, entitlements, `notarytool`, stapling), not a secrets toggle, so it is
  its own decision, not an incremental tweak.
- **Update freezing becomes a real risk** (a hostile network position against a
  target that matters). That is the point to add signed freshness metadata and a
  persisted watermark, closing the D4 gap this ADR records.
