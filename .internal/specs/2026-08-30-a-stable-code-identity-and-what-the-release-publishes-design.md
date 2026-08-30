# A stable code identity, and what the release publishes

- **Date:** 2026-08-30
- **Beads:** `nocx-2s0zc` (this brainstorming session), `nocx-mchgh` (the missing
  helper build, found while writing this)
- **Status:** design, approved by the owner 2026-08-30 section by section
- **Related:** ADR-0003 (distribution without a Developer ID), ADR-0050 (the OS
  keystore holds a key only a person can take), ADR-0007 (cross-platform
  auto-update), `.internal/specs/2026-08-28-the-nocx-server-design.md` (the
  coordinator, and where the second executable lives),
  `.internal/specs/2026-08-13-remote-helper-design.md` D20 (the helper build
  matrix, amended here), epic `nocx-rybbm` (the keychain work this unblocks)

## 1. In one sentence

The macOS build stops signing itself ad-hoc and signs with a project certificate
whose **designated requirement is the same in every release**, because that string
is what the keychain uses to tell our process from anyone else's (ADR-0050 step 3)
— and the release starts building and publishing the remote helper, which today it
does neither.

The signature buys **identity, nothing else**. Gatekeeper, notarization and the
hardened runtime stay out (ADR-0003 is unchanged on publisher identity). What a
person downloaded is still proven the way it is proven on Linux: by the ed25519
signature over `manifest.json`.

## 2. What this crosses, and what those documents already decided

| Boundary                                                                                                                         | What it already decides                                                                                                                                                                                                                                                                                                  | What this design does about it                                                                                                                                                                                                                                                                         |
| -------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **ADR-0003** — distribution without a Developer ID                                                                               | No Developer ID, no notarization; the ad-hoc signature Wails applied is "existing behaviour the build preserves"; integrity is the signed manifest                                                                                                                                                                       | Amended in one direction only: publisher identity is still absent, **code identity is not**. A self-signed certificate is not a distribution signature and buys no Gatekeeper policy. The manual `xattr -dr com.apple.quarantine` on first install stays, and so does Homebrew ineligibility           |
| **ADR-0050** — the keystore holds a key only a person can take                                                                   | Step 3: nocx reaches the keychain under its own code identity, "signed by a project certificate whose designated requirement is stable across releases". It also records the measurement that a self-signed certificate yields `identifier X and certificate leaf = H"…"` — anchored to the certificate, not to a cdhash | This design delivers exactly step 3's build half and nothing more. The provider (Security.framework instead of `/usr/bin/security`), the presence requirement of step 4, and `CGO_ENABLED=1` remain `nocx-rybbm`'s                                                                                     |
| **ADR-0007** — auto-update: swap in place, relaunch, health, rollback                                                            | `internal/update/platform_darwin.go:81` runs `codesign --verify --deep --strict` on the extracted bundle before a swap                                                                                                                                                                                                   | The check is unchanged and still correct: it verifies integrity, not identity. Its comment and error text name "the ad-hoc signature Wails applied" and become false — they are rewritten in the same commit                                                                                           |
| **The nocx-server design §4** — where the binary lives                                                                           | `nocx-server` is a second executable inside `nocx.app/Contents/MacOS`, installed by installing the app                                                                                                                                                                                                                   | Unchanged. It is the binary that most needs the identity, because it is the process that holds the vault after the split                                                                                                                                                                               |
| **Remote-helper design D20** — the artifact ships inside the app for three targets, `darwin/amd64` answers `unsupportedPlatform` | Three cross-compiled helpers are embedded; nothing is downloaded at runtime                                                                                                                                                                                                                                              | **Amended, deliberately.** The matrix becomes 2×2 — `linux/{amd64,arm64}`, `darwin/{amd64,arm64}` — because the app itself is universal and an Intel Mac is a host we support. D20's own reasoning (no runtime download, no second delivery channel) is untouched; only the target list changes        |
| **AD-2** — one Go codebase, multiple build targets                                                                               | Three roles, three targets                                                                                                                                                                                                                                                                                               | Unchanged, and it is why publishing the helper is publishing an artifact we already build rather than a new one                                                                                                                                                                                        |
| **release.yml's own §5 principle** — "the signing key lives only in the publish job, which runs no build tooling"                | The manifest key is isolated from build tooling                                                                                                                                                                                                                                                                          | **Deviated from, for the code-signing key only, and it cannot be otherwise:** `codesign` must run on the assembled bundle, on macOS, in the job that assembles it. The two keys stay distinct, so the manifest key's isolation is exactly as strong as before. Named here rather than discovered later |

## 3. Decisions

- **D1 — One self-signed code-signing certificate, long-lived.** `CN=nocx Project
Code Signing`, extended key usage `codeSigning`, 20 years. No project CA and no
  intermediate: an anchor-on-the-CA arrangement would let a leaf be renewed without
  changing the requirement, and it buys nothing today — there are no installs whose
  grants a renewal could break, and YAGNI decides the rest. The cost is recorded in
  D8 so that a future renewal is a decision rather than a surprise.
- **D2 — The requirement is derived, recorded, and then enforced.** We do not write
  a custom requirement with `--requirements`. We take what `codesign` derives, commit
  it to `build/darwin/requirement.txt`, and make CI fail when a build's requirement
  differs from that file. The file is the single source of truth the keychain
  provider will read when `nocx-rybbm` lands, so there is never a second copy of the
  string to drift.
- **D3 — Signing is inside-out, and `--deep` is not used to sign.** `nocx-server`
  first with an explicit `-i com.nocx.server`, then the bundle, which takes its
  identifier from `Info.plist`. `--deep` is deprecated by Apple for signing (it
  applies the outer options to nested code, which is almost never right); it stays
  only in `--verify --deep --strict`, where it means "check nested code too". The
  bundle signature remains the last thing that touches the tree.
- **D4 — A bare binary gets its identifier stated, never defaulted.** `codesign`
  derives a bare Mach-O's identifier from its file name, so `nocx-server` would carry
  whatever the file happened to be called. The requirement contains the identifier;
  a defaulted identifier is a requirement that can change without anyone deciding to
  change it.
- **D5 — The certificate lives in two secrets and one offline backup.**
  `MACOS_SIGNING_P12` (base64 of the `.p12`) and `MACOS_SIGNING_P12_PASSWORD`. The
  job imports them into a **temporary keychain**, runs `security
set-key-partition-list` (without it `codesign` blocks on an interactive password
  prompt and the job hangs rather than fails), and deletes the keychain in an
  `if: always()` step. The private key is backed up outside the repository, beside
  `RELEASE_SIGNING_KEY`, under the same discipline ADR-0003 already states for it.
- **D6 — The dry run signs.** `workflow_dispatch` builds, signs and verifies, and
  publishes nothing. A signing path whose first execution is a release is the defect
  `nocx-94kl` already bought once, when the keyring check lived inside the tag-gated
  publish job and had therefore never run.
- **D7 — `make helpers` runs in both build jobs, and its output is published
  uncompressed.** Building it is the fix for `nocx-mchgh`. Publishing the
  **decompressed** binaries rather than the embedded `.gz` is not a convenience:
  `gzip` records the source file's mtime in its header, so the `.gz` bytes differ
  between the macOS job and the Linux job even when the binary inside is identical.
  The decompressed bytes are what `install.go` hashes to key the install directory,
  so publishing them makes "the same bytes you would have been sent" a checkable
  claim — a hand-installed helper lands in the same directory name the ssh path
  would have created, and no reinstall-on-hash-mismatch follows.
- **D8 — A certificate change is an identity change, and is stated as one.**
  Renewing, rotating or losing the certificate produces a different requirement;
  every install that already holds keychain items under the old one stops being
  recognised as their owner. Today that costs nothing (no installs). When it stops
  costing nothing, the answer is a project CA with the requirement anchored to it —
  written down here so that the option is remembered rather than rediscovered.

## 4. What the workflow does

**`build-macos`** (`macos-15`), in order:

1. `make helpers` — four targets, before the app is built, because the app embeds them.
2. Build the frontend, then the two app slices and the two `nocx-server` slices
   exactly as today; `lipo` both into the bundle; copy `Info.plist` and the icon.
3. Import the certificate into a temporary keychain (D5).
4. `codesign --force --sign "$ID" -i com.nocx.server` on `Contents/MacOS/nocx-server`,
   then `codesign --force --sign "$ID"` on `nocx.app` (D3).
5. Smoke checks (§5).
6. Package `.zip` with `ditto --noqtn` and the `.dmg` with `hdiutil`, unchanged.
7. Delete the temporary keychain, `if: always()`.

**`build-linux`** (`ubuntu-22.04`): unchanged except that `make helpers` runs before
the binary is built, and the four helper binaries are uploaded as artifacts along
with the AppImage. Nothing on Linux is code-signed; there is no OS mechanism that
would read such a signature.

**`sign`**: unchanged in what it does with the manifest key, and gains one check —
the helper content hashes reported by the two build jobs must be equal (§5). The
helper hashes join the manifest before it is signed.

**`publish`**: unchanged in shape; the release now carries four helper binaries
beside the `.dmg`, `.zip` and `.AppImage`.

## 5. The checks, and what each would catch

Two survive unchanged: `codesign --verify --deep --strict` on the bundle and
`--verify --strict` on `nocx-server`. Four are new, and each exists because it can
fail:

1. **The requirement matches the committed one.** `codesign -d -r- "$SRV"` compared
   with `build/darwin/requirement.txt`. Catches: the certificate was replaced,
   rotated or imported from a different secret, and the identity changed silently.
2. **The requirement survives a rebuild.** Rebuild `nocx-server` with a different
   `Date` in `-ldflags`, sign the copy, assert its `cdhash` differs and its
   requirement does not. Catches: the property this whole design rests on, asserted
   on the artefact rather than assumed from ADR-0050's one-off measurement.
3. **The signature survives packaging.** Extract `dist/*.zip` into a temporary
   directory and verify there. Catches: a packaging change that damages the
   signature — which is precisely what the updater checks at install time and what
   assembling the bundle by hand once already broke.
4. **The helper is embedded, and the two jobs agree.** On the built artefact,
   resolve an artifact for every matrix platform and fail on
   `ErrArtifactsNotBuilt`; and compare the helper content hashes computed in the
   macOS job with those from the Linux job. Catches: `nocx-mchgh` returning, and a
   macOS-built app and a Linux-built app deploying different bytes to one host under
   one version — a silent source of endless reinstalls.

## 6. Failure paths

- **Secret absent or malformed.** The import step fails and the job fails. There is
  no "sign if secrets exist" branch: ADR-0003's review already rejected that shape
  as a half-truth, and here it would mean a release that silently ships an identity
  nothing can recognise.
- **`set-key-partition-list` not run.** `codesign` waits for a password on a runner
  where nobody can type one. The step is written so that this is a failure, not a
  six-hour hang: the keychain is created with a known password and unlocked
  explicitly.
- **Requirement drift.** Check 1 turns it red. This is the whole point of committing
  the string.
- **A helper hash mismatch between jobs.** Check 4 turns it red before publish. The
  release does not go out with two different helpers under one version.
- **The certificate expires.** Signing fails at build time; existing signatures are
  unaffected because nothing here verifies against a trust chain. The 20-year
  validity makes this a successor's problem, and D8 says what the successor does.

## 7. Testing

The gates live in the workflow, on the built artefact, and are listed in §5 — this
is packaging, so a Go test cannot reach it. Two things are testable in Go and are:
the `deploy` package's matrix (the 2×2 change means
`TestArtifactDarwinAMD64IsUnsupported` is rewritten, not deleted: `darwin/amd64`
becomes a supported target, so the assertion that separates "outside the matrix"
from "not built" moves to a platform the matrix still does not contain), and the
update path's darwin verification, whose text changes but whose behaviour does not.

The one thing this design cannot verify from a Linux host is the two macOS facts it
rests on: the exact requirement string a self-signed certificate produces, and the
`set-key-partition-list` incantation the runner needs. Both are measured on the
first `workflow_dispatch` dry run, and `build/darwin/requirement.txt` is written
from that run's output rather than guessed. That is the first work item, and no
other item can be finished before it.

## 8. Work order

1. Generate the certificate, load the two secrets, back the key up offline.
2. `workflow_dispatch` dry run with signing wired but no committed requirement file;
   read the actual requirement string out of the run; commit it.
3. Turn on checks 1-3.
4. `make helpers` in both jobs, the 2×2 matrix, the rewritten test, D20 amended
   (`nocx-mchgh`).
5. Publish the helper binaries and their hashes; check 4.
6. The new ADR, and the corrected comment in `internal/update/platform_darwin.go`.

## 9. Deliberately out of scope

- **Notarization, Gatekeeper, hardened runtime, entitlements.** No Developer ID;
  ADR-0003's position on distribution stands.
- **`CGO_ENABLED=1` for darwin `nocx-server`, the Security.framework provider, and
  the presence requirement.** `nocx-rybbm`, which is blocked on measurements that
  need a Mac.
- **Signing the AppImage.** Nothing on Linux reads such a signature at run time; the
  manifest is the verification path, and it already exists.
- **Signing `nocx-helper` with our identity.** No one on the far host asks for it,
  and a `darwin/arm64` helper is executable because the Go linker ad-hoc signs it
  itself.
- **Linux keystore behaviour.** The Secret Service authenticates by uid, not by code
  identity; ADR-0050 and `nocx-rybbm` both file it separately and this design does
  not touch it.

## 10. Open questions

None blocking. The two measurements in §7 are work items, not questions: the dry run
answers both, and the design says what to do with either answer.
