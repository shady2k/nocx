# Distribution and updates — design

Date: 2026-07-22
Epic: `nocx-a75`
Status: revised after three adversarial review rounds; approved for planning

Revision note: this spec has been through three adversarial reviews. Round 1 found seven
defects that would have shipped broken code. Round 2 found that several of the *fixes* were
themselves wrong — most importantly that the rollback protocol inferred transaction state
from a filename, and that the health signal it depended on is not observable in this
codebase. Round 3 attacked §7 as a crash-consistency protocol and broke the phase machine in
five places, which is why §7 no longer has one: the filesystem is now the source of truth and
the journal only records intent. Scope reductions (Linux, Developer ID signing,
`--sequesterRsrc`, a beta channel, a durable `healthy` phase) are deliberate, not omissions.

Revision (2026-07-23) — **cross-platform, per [ADR-0006](../../decisions/0006-cross-platform-auto-update.md).**
D1 is reversed: the updater is no longer macOS-only. It becomes one platform-agnostic core
plus a thin `Platform` seam; §7 below is the **darwin** implementation of that seam, and Linux
ships as an **AppImage** with its own implementation (extract + `chmod +x`,
`renameat2(RENAME_EXCHANGE)`, an `APPIMAGE`-env refusal mirroring §7.7). We stay on **Wails v2**
— v3's built-in updater was evaluated and rejected (bare-binary-only, alpha). ADR-0006 is the
authority for this reversal; where §3 (D1), §9, §10 and §12 below still read "macOS only",
ADR-0006 supersedes them.

## 1. Problem

nocx has no way to reach another machine. CI (`.github/workflows/ci.yml`) verifies but
publishes nothing, the binary carries no version, and nothing checks whether a newer build
exists. This design covers getting an installable macOS artefact out of CI and keeping an
installed copy current.

## 2. The constraint that shapes everything

**There is no Apple Developer ID and there will not be one.**

This is not a preference to route around; it removes options that are usually assumed:

- Notarization requires a Developer ID, which requires enrolment in the Apple Developer
  Program. An unenrolled team is refused with `Team is not enrolled in the Apple Developer
  Program`.
- No third-party service can sign in its own name. SignPath — free for open source, used by
  Tabby for Windows — *stores and applies your* certificate; it does not issue one.
- Homebrew has closed the workaround. `--no-quarantine` has been removed from `brew`
  (Homebrew/brew#20755, shipped in #20973), and from **1 September 2026** every cask must
  pass Gatekeeper checks. The official tap already forbids casks that "require System
  Integrity Protection or Gatekeeper to be disabled or bypassed". A private tap inherits the
  same problem: the bypass mechanism no longer exists anywhere in `brew`.
- A free Apple ID yields `Apple Development` certificates for local work only — neither
  Developer ID nor notarization.

**What is absent is publisher identity, not every signature.** Wails v2.13 unconditionally
runs `codesign --force --deep --sign -` on a macOS production bundle, so the shipped `.app`
carries an ad-hoc signature. It proves no publisher and satisfies no Gatekeeper policy, but
it is real, macOS can validate its integrity, and packaging must not damage it (§5, §9).

Consequences accepted:

- The **first** install requires the user to strip the quarantine attribute by hand. There is
  no way to avoid this without a certificate.
- **Updates are not affected.** The quarantine attribute is set by quarantine-aware
  downloading applications (browsers), not by the OS on any HTTP transfer. A Go updater that
  fetches and installs the bundle itself does not set it. This is standard macOS behaviour
  rather than a guarantee — endpoint-security tooling can differ — so the updater treats an
  unexpected quarantine attribute as a detected condition with a remediation message, and §13
  verifies the claim on real hardware.

## 3. Decisions

**D1 — Cross-platform (macOS + Linux now, Windows later), behind a `Platform` seam.**
*Reversed 2026-07-23 ([ADR-0006](../../decisions/0006-cross-platform-auto-update.md)); the
round-1 deferral of Linux is undone now that the maintainer runs nocx on Linux and needs
updates there.* The updater is one platform-agnostic core (manifest fetch + ed25519 verify,
semver, download, and the §7 crash-consistency transaction — journal by device+inode, `flock`,
reconcile, health, auto-rollback) plus a thin `Platform` seam with per-OS implementations:
**darwin** exactly as specified throughout §7 (`.app`, `ditto`, `codesign`, `lipo`,
`RENAME_SWAP`), and **linux** shipping an **AppImage** (extract + `chmod +x`, atomic
`renameat2(RENAME_EXCHANGE)`, and an `APPIMAGE`-env refusal mirroring §7.7). The Wails Linux
build's dynamic linkage against GTK/WebKitGTK/glibc — the original reason to defer — is exactly
why the format is AppImage (linuxdeploy + its GTK plugin bundle the stack into one
self-replaceable file), shipped against a stated support envelope rather than as a bare
tarball. `nocx-mbu` is reopened. Windows is left as an unimplemented seam: `internal/pty` still
has no conpty branch, so it is missing code rather than a priority call.

Because Linux is no longer deferred, `docs/architecture.md:179` ("MVP is macOS-only;
Windows/Linux are Phase 3") **is** amended for Linux (Windows stays Phase 3) — see §10.

**D2 — Artefacts: `.dmg` + `.zip`.**
The `.zip` is the updater payload. The `.dmg` is the human install path: it lands the app at a
stable `/Applications/nocx.app` and is the familiar gesture.

*Revised after round 1: the first draft justified the DMG by App Translocation and overstated
it.* Accurate version: a quarantined app that was not moved by Finder runs from a randomised
read-only path and cannot replace itself. But since this app is unsigned, the user must run
`xattr -dr com.apple.quarantine` before it will launch at all, and that removes both the
Gatekeeper refusal and translocation. So the DMG is the clearer path, not a technical
prerequisite. The actual prerequisites are: quarantine removed, and the app in a stable
writable location.

Built with plain `hdiutil` over a staging folder containing the `.app` and an `/Applications`
symlink. No `create-dmg`, no background artwork.

**D3 — Developer ID signing and notarization are out of scope.**
*Revised after round 1: the first draft kept a dormant "sign if secrets exist" branch,
modelled on Tabby's workflow.* That was speculative work the governing principle forbids, and
a half-truth besides: real Developer ID distribution needs bundle signing order, hardened
runtime, entitlements, notarytool submission and stapling — not a secrets toggle. *Sharpened
after round 2:* what is out of scope is **distribution signing**, not all signing. Wails'
ad-hoc signature (§2) is existing packaging behaviour that this work preserves and verifies.

**D4 — Update integrity is ours, not Apple's.**
No Gatekeeper check will ever validate these builds against a publisher, so the updater is the
only integrity gate. CI signs the release manifest with an **ed25519** key held in a repository
secret; the binary compiles in a **keyring** (§6).

Threat model, stated rather than implied. This protects against a tampered artefact and
against anyone who can serve bytes but cannot sign. It does **not** protect against:

- a compromised repository or CI — write access means signing whatever you like;
- **update freezing** — serving an older, validly-signed manifest indefinitely, which the
  updater reads as "already current". Defending against that needs signed freshness metadata
  and a persisted release watermark; that is TUF in miniature and is not built here.

Both gaps are recorded in ADR-0003 rather than papered over.

**D5 — The updater never restarts the app.**
A terminal holds live PTYs and people leave it open for days. The updater installs the new
version and offers a restart; the user chooses when.

**D6 — Check cadence lives in the frontend, mechanism and state live in Go.**
Go exposes bound methods matching the one app-level seam already in the codebase (`GetWSPort`
in `main.go` → `frontend/src/main.ts`). No Wails event channel is introduced. The frontend
decides when to ask — on start and every 24 h, because a window that lives for weeks makes "on
start only" mean "never".

*Revised after round 1: the first draft had `ApplyUpdate` take the `Release` returned by
`CheckForUpdate`*, which would have round-tripped security-critical data (URL, digest, version)
through JavaScript for the backend to trust on the way back. The verified release now lives in
backend state; `ApplyUpdate()` takes no arguments.

## 4. Versioning

New package `internal/version` holding `Version`, `Commit`, `Date` as `var`s set at link time.
Wails v2.13 passes `-ldflags` through to the Go compiler for both slices of a universal build:

```
-X github.com/shady2k/nocx/internal/version.Version=${VERSION}
-X github.com/shady2k/nocx/internal/version.Commit=${GITHUB_SHA}
-X github.com/shady2k/nocx/internal/version.Date=${BUILD_DATE}
```

Defaults are `dev` / `none` / `unknown`. `Version` holds the bare number (`0.2.0`), matching the
manifest's `version` field so the two never need translating.

**The bundle plist is a second version and must not be forgotten.** Wails writes
`info.productVersion` from `wails.json` into *both* `CFBundleShortVersionString` and
`CFBundleVersion`. There is no build flag for it, so patching the file before the build is the
available mechanism.

*Corrected after round 2:* `wails.json` currently has **no `info` object at all** — `1.0.0` is
Wails' default, not a value sitting in the file. The patch step must therefore create the
object, not edit a key:

```json
"info": {
  "productName": "nocx",
  "productVersion": "0.2.0"
}
```

**Releases are restricted to stable `vMAJOR.MINOR.PATCH`.** `CFBundleVersion` has a narrower
numeric grammar than semver, so a tag like `v0.3.0-beta.1` would produce an invalid plist;
GitHub's `latest` excludes prereleases anyway. Restricting the tag grammar removes the problem
without post-processing the plist.

Comparison uses `golang.org/x/mod/semver` — a new direct dependency, chosen over 30 lines of
hand-rolled version parsing. It requires a `v` prefix, so one helper normalises both sides.

## 5. Release pipeline

*Revised after round 1: the first draft had `release.yml` depend on `ci.yml` via `needs`, which
only addresses jobs within one workflow — the two would have run independently and a release
could publish while the quality gates were failing.*

`ci.yml` gains a `workflow_call` trigger and **loses its `push: tags: ['v*']` trigger**.
*Added after round 2:* keeping both would run the whole suite twice for every release — once
standalone, once through the caller — for no added protection. Release branches and
`workflow_dispatch` keep their existing triggers.

`release.yml` triggers on `push: tags: ['v*']`, calls `ci.yml`, and gates every publishing job
behind it, so a release cannot exist without green backend, frontend and e2e runs on that exact
commit. The caller grants the reusable job `contents: read`; only the publish job gets
`contents: write`.

**Validation before anything is built.** The tag must parse as stable semver (§4), must point
at a commit reachable from `main`, and must resolve to exactly the commit being built. Its
version must match what lands in `wails.json` and in the manifest.

**`workflow_dispatch` is dry-run only.** A manual run has no tag, so it cannot produce the
`/releases/download/v<version>/…` URLs the manifest asserts. It performs every build, sign and
verify step, uploads the results as workflow artefacts, and creates no release. Real releases
come from tags, and only from tags. *The first draft proposed a draft release for this, which
also does not work — drafts are excluded from `releases/latest`.*

**Build (`macos-15` — an explicit image, not the moving `macos-latest` alias)**
1. patch `wails.json` per §4
2. `wails build -platform darwin/universal -ldflags "<version flags>"` — verified to work in
   v2.13.0, which builds both slices and joins them with `lipo`
3. smoke checks: `lipo -archs` shows both architectures, the plist version matches the linked
   Go version, and the built binary reports the expected version. A universal Mach-O is not
   proof that both slices launch, and the checks say so honestly rather than implying more.
4. `codesign --verify --deep --strict` on the built bundle, establishing the baseline for the
   round-trip check in §9
5. `.dmg` via staging folder + `hdiutil create -format UDZO`
6. `.zip` via `ditto -c -k --keepParent --noqtn` — `ditto`, not `zip`, because it preserves
   bundle symlinks and metadata; `--noqtn` so no quarantine attribute can ride along.
   *`--sequesterRsrc` was dropped after round 2:* it is valid and round-trips correctly, but
   nothing in this bundle is shown to need it.

**Publish (separate job)**
Generates `manifest.json`, signs it with `RELEASE_SIGNING_KEY`, and attaches every artefact plus
`manifest.json` and `manifest.json.sig` to the GitHub Release.

Separate from the build jobs so the signing key is never present in a job that runs build
tooling. Manual approval gates and SHA-pinned actions were considered and declined: this is a
single-owner repository using first-party `actions/*`, and the ceremony would buy nothing.

Artefact naming: `nocx-<version>-darwin-universal.<ext>`.

## 6. Manifest

Fetched from `https://github.com/shady2k/nocx/releases/latest/download/manifest.json`, with the
detached signature at the same path plus `.sig`. GitHub's `latest` redirect serves it, so the
updater needs no API call and hits no rate limit.

```json
{
  "version": "0.2.0",
  "released": "2026-07-22T10:00:00Z",
  "notesUrl": "https://github.com/shady2k/nocx/releases/tag/v0.2.0",
  "artifacts": [
    {
      "os": "darwin",
      "arch": "universal",
      "format": "zip",
      "url": "https://github.com/shady2k/nocx/releases/download/v0.2.0/nocx-0.2.0-darwin-universal.zip",
      "sha256": "…",
      "size": 41234567
    }
  ]
}
```

**Artefact matching.** *Revised after round 1: this was a real bug.* The runtime reports
`darwin/arm64` or `darwin/amd64`; the manifest declares `darwin/universal`. Exact matching would
never find an artefact. Matching is therefore explicit: for `os == "darwin"`, an artefact with
`arch == "universal"` matches any architecture; an exact architecture match wins if both are
present. Both runtime architectures are tested.

`manifest.json.sig` is a base64 ed25519 signature over the exact bytes of `manifest.json`.
Verification happens before the JSON is parsed.

**Keyring, not a key.** `internal/update` compiles in a slice of accepted public keys and
verifies against any of them, so a client that has upgraded past a key-introducing release keeps
working across a rotation.

*Round 2 correctly showed this does not make rotation lossless, and the limit is stated rather
than engineered around:* a binary that only ever knew key A cannot authenticate a manifest
signed solely by key B, and with a `latest`-only endpoint it can never be handed an intermediate
release. Retiring a key therefore means **clients older than the release that introduced its
successor must reinstall by hand**. That is acceptable here and belongs in ADR-0003. Losing the
private key with no successor in any shipped keyring means existing installs can never be
updated in place again, so the key lives in a backup outside GitHub.

There is no beta channel. Excluding prereleases yields only the stable channel, and a second
channel needs a second URL or an API lookup. Not built, no user for it.

## 7. Updater

New package `internal/update`, interface-first like the rest of the tree:

```go
type Updater interface {
    Check(ctx context.Context) (*Release, error) // nil, nil when already current
    Apply(ctx context.Context) error             // applies the release Check verified
    Reconcile(ctx context.Context) error         // startup: settle any transaction in flight
    ReportHealthy(ctx context.Context) error     // frontend readiness (§7.5)
}
```

Concrete implementation `GitHubUpdater{manifestURL, keyring, current version, runtime os/arch,
install path, http client, logger}`, wired at the composition root in `internal/app` and exposed
through bound methods on `WailsApp`.

**Install path is derived, not assumed.** The bundle path comes from the running executable, so
`/Applications`, `~/Applications` and anywhere else all work. `/Applications` is normally
`root:admin` and group-writable, which lets an admin account replace an entry without a prompt —
but that is a default, not a guarantee, and managed Macs, standard user accounts and unusual
ACLs all break it. The updater performs the operation and reports the real error; it never
assumes "admin" is sufficient and never attempts privilege escalation.

### 7.1 The filesystem is the truth; the journal records intent

*This is the round-3 redesign and the most important thing in §7.* The previous revision had a
`prepared` / `swapped` / `healthy` phase machine, and round 3 broke it in five places — every
one of them the same bug: a crash between a filesystem mutation and the journal write that was
supposed to describe it left the record lying. Ordering the two more carefully only moves the
window; it cannot close it.

So the record no longer claims what happened. It records **what this transaction intends and
which objects it involves**, identified by device+inode rather than by name or version:

| Field | Purpose |
|---|---|
| `txID` | opaque, names the extraction directory |
| `installPath` | where the canonical bundle lives |
| `oldBundleID`, `newBundleID` | device+inode of the previous and replacement bundles |
| `fromVersion`, `toVersion` | reporting and UI only — never used to decide state |
| `artifactSHA256` | what was verified |
| `launchAttempts` | see §7.5 |

Reconciliation **observes** which identity currently sits at which path and derives the state
from that. Versions cannot do this job: an equal-version re-release, a reinstall, and a
downgrade all produce identical version strings for different bundles.

Recorded identities also replace string path comparison everywhere. *Round 3 was right that
comparing paths as strings is unsound*: symlinks and the `/Applications` versus
`/System/Volumes/Data/Applications` alias give different strings for the same directory, while a
copy the user made gives the same version at a different inode.

### 7.2 Paths

All inside the install directory, so every rename is same-volume:

| Path | Role |
|---|---|
| `<install-dir>/.nocx-update-<txID>/` | download + extraction directory |
| `<install-dir>/.nocx-update-<txID>/nocx.app` | staged bundle as `--keepParent` produces it |
| `<install-dir>/.nocx-swap.app` | peer of the atomic exchange; holds the *previous* bundle after it |
| `<install-dir>/.nocx-backup.app` | retained known-good bundle |

*Round 3 found version-derived names collide across a reinstall, a downgrade, or a same-version
rebuild.* Fixed names plus recorded identities avoid that: there is at most one transaction at a
time (§7.6), and nothing is deleted or reused without checking its identity against the record.

### 7.3 Reconciliation

`Reconcile` runs at startup and at the head of `Apply`, always under the lock. It observes the
identities at `installPath`, `.nocx-swap.app` and `.nocx-backup.app`, and acts:

- **No record, no managed debris** — nothing in flight.
- **No record, but managed debris exists** — refuse with an actionable message naming what was
  found. *Round 3: deleting the config directory after an exchange produces exactly this, and
  `.nocx-swap.app` may then hold the only rollback copy. Automatic interpretation would be
  guessing.*
- **Record present, `installPath` holds `oldBundleID`** — the exchange did not happen, whether or
  not `newBundleID` was ever filled in. Everything else this transaction created is therefore
  debris: remove `.nocx-swap.app` and the extraction directory if present, clear the record. Both
  being absent is a valid state, not an error — the record is written before either exists
  (§7.4), so a crash in between is normal.
- **Record present, `installPath` holds `newBundleID`** — the exchange happened, health is
  unconfirmed. This is the `pendingRestart` state (§7.5). Do not undo it.
- **Record present, `installPath` holds neither** — refuse, naming both observed identities. The
  user replaced the bundle by hand, or something else did.
- **Record unreadable, truncated, or of an unknown schema version** — refuse with recovery
  instructions. Atomic writes make truncation unlikely, but deliberate corruption still needs a
  documented escape rather than a guess.

Reconciliation is idempotent: running it twice changes nothing the first run did not.

### 7.4 Apply

1. acquire the lock (§7.6)
2. reconcile; refuse if a transaction is already in flight
3. re-read the installed version; abort if it no longer matches what `Check` saw
4. write the record, including `oldBundleID` observed from the currently installed bundle —
   before anything touches the disk, so every file this transaction creates is explained by it
   and the "did the exchange happen?" question is answerable from the very first moment.
   `newBundleID` is the only field that cannot be known yet; it is filled in at step 11. *An earlier draft wrote it after extraction, which meant a crash during
   the download left an extraction directory no reconciliation could classify, blocking every
   future update as unexplained debris.* `fsync` the record and then its containing directory:
   a rename is not durable until the parent directory is synced.
5. create the extraction directory
6. download the artefact into it, with bounded timeouts and response-size limits
7. verify SHA-256 and the declared size **before** extracting
8. **preflight the archive before invoking `ditto`**: read the zip central directory with
   `archive/zip` and reject absolute paths, `..` traversal, link entries pointing outside the
   tree, more than one `.app` root, or an implausible expanded size. *Round 2 was right that
   "extract, then assert containment" is useless — writes performed during extraction cannot be
   un-performed.* `archive/zip` cannot *restore* a bundle, but inspecting names and types is
   exactly what it is good at.
9. extract with `/usr/bin/ditto -x -k --noqtn`. *The first draft said `archive/zip`, which
   recreates nothing on its own and would have produced regular non-executable files with no
   symlinks.*
10. verify the extracted tree: exactly one `.app` root, inside the extraction directory, and
    `codesign --verify --deep --strict` passes
11. rename the staged bundle to `.nocx-swap.app`; record `newBundleID` from it, and sync
12. **re-verify identity immediately before the exchange** — the installed bundle is still
    `oldBundleID`. *Round 2 caught that step 3 sits before a download that may take minutes, so
    calling it "immediately before" was internally false. The lock covers other nocx processes;
    this covers everything that does not honour it.*
13. exchange atomically with `unix.RenameatxNp(…, unix.RENAME_SWAP)` (`golang.org/x/sys`, already
    an indirect dependency; this promotes it to direct). Verified: both paths must exist, APFS
    supports atomic exchange of non-empty directories so an `.app` is valid, and the running
    process keeps its mapped image and open descriptors. *The first draft moved the old bundle
    aside and then moved the new one in, leaving a window with nothing at the install path — a
    crash there left the user with no application and no code able to recover.*
14. report success. The frontend offers a restart (D5).

No journal write follows the exchange, and none is needed: after step 13 the identities on disk
say unambiguously that it happened. *That is precisely the gap round 3 identified in the previous
revision, where the record still said `prepared` until a subsequent write that a crash could
lose.*

### 7.5 Health check and finalisation

*Round 3 confirmed by reading the code that the previous signal does not work.* `TabManager`'s
constructor calls `newTab()` without awaiting it (`frontend/src/tabs.ts:443`), and `Tab.start()`
catches its own failure and renders a `.pane-error` notice into the pane
(`frontend/src/tabs.ts:356`). A constructed `TabManager` is therefore fully compatible with a
terminal that never started, and health would have blessed a broken release.

The frontend gains a real readiness signal: `Tab` exposes whether its renderer mounted and its
PTY session opened, `TabManager` exposes a promise that resolves when the initial tab reaches
that state, and only then does `main.ts` call `ReportHealthy()`. The existing swallow-and-show-a-
notice behaviour stays for the UI; it simply stops counting as success.

Finalisation requires **all** of:

- a record exists and `installPath` holds `newBundleID`;
- the running executable's bundle is `newBundleID` — by identity, not by path (§7.1), which also
  means a bundle the user moved after the exchange still finalises instead of sticking forever;
- the running binary reports `toVersion`;
- backend `Start` returned without error — recorded explicitly, because `main.go:75` logs a
  `Start` failure and continues into Wails;
- `ReportHealthy()` arrived.

Then, idempotently: delete `.nocx-backup.app` if it exists and is not `newBundleID`, rename
`.nocx-swap.app` to `.nocx-backup.app`, and delete the record. Each step checks whether it has
already been done, so a crash anywhere in this sequence is resolved by simply running it again.
*Round 3 found the previous version could crash between the rename and a `healthy` write and
then retry the rename forever; there is no `healthy` phase now, and the record's absence is what
marks completion.*

`ReportHealthy` takes the lock, is idempotent, and is harmless when no record exists.

**Escape from an unconfirmed update.** *Round 3's most user-visible finding: a persistent JS
exception, a broken asset bundle, or a user who quits immediately would leave the app in
`pendingRestart` forever, with checks suppressed and the documented rollback naming a file that
does not exist yet.* `Reconcile` increments `launchAttempts` early in startup, before anything
else can fail. On the third launch that reaches Go and never confirms health, the updater rolls
back automatically: exchange `.nocx-swap.app` back, delete the record, and log loudly. The user
gets their working version back without touching a terminal.

This cannot help if the new build fails before Go runs at all — a dyld failure leaves nothing to
count. That case needs the manual route, so it is documented rather than pretended away.

**Manual rollback.** *Round 3 was right that the previous instructions named
`.nocx-<fromVersion>.app`, which does not exist until finalisation succeeds* — exactly the state
a user recovering from a bad update is not in. The README documents both:

```
# after a successful update, to go back to the retained known-good version
osascript -e 'quit app "nocx"'
rm -rf /Applications/nocx.app
mv /Applications/.nocx-backup.app /Applications/nocx.app

# during a failed update, before health was ever confirmed
osascript -e 'quit app "nocx"'
rm -rf /Applications/nocx.app
mv /Applications/.nocx-swap.app /Applications/nocx.app
rm -f ~/Library/Application\ Support/nocx/update-state.json
```

### 7.6 Locking

*Round 3 rejected the previous "a lock file", correctly: an `O_EXCL` sentinel survives SIGKILL
and blocks updates forever, while deleting a held sentinel lets a second process take a lock on
a new inode.*

The lock is an advisory `flock` held on an open descriptor for the lifetime of the operation, so
the kernel releases it when the process dies however it dies. There is no state to clean up and
no file for the user to find.

`Reconcile`, `Apply` and `ReportHealthy` are public wrappers that acquire the lock around a
private `reconcileLocked` and friends — *round 3 caught that a public `Reconcile()` taking the
lock would deadlock against `Apply()` calling it while already holding it.*

Startup uses a **bounded try-lock**: if another process is mid-download, startup skips
reconciliation for this launch rather than blocking the terminal behind somebody else's network
transfer. `Apply` reports a legible "an update is already in progress" instead of hanging.

### 7.7 Refusals that must be legible rather than mysterious

- `Version == "dev"` → no check at all, so `wails dev` never offers an update.
- Running from a translocated path (`/private/var/folders/…/AppTranslocation/…`) → the updater
  cannot replace itself; it says "move nocx to /Applications".
- Install directory not writable → say so plainly, with the path.
- Managed debris with no record, an unreadable record, or an unrecognised identity at the install
  path → name what was found and what to do (§7.3).

**Replacing a running bundle.** The process holds its own mapped image, so the exchange does not
disturb live PTYs. This is asserted as a tested property of *this* app, not a general macOS
guarantee: nothing prevents Wails from lazily reading a bundle resource by path after the swap,
so §9 tests continued terminal use across an exchange. If that ever fails, the fallback is a
post-exit helper.

All update failures are non-fatal, logged through the existing `log.Logger`, and never modal — a
modal stealing focus from a terminal is worse than a missed update.

## 8. Frontend

`frontend/src/main.ts` calls `CheckForUpdate()` after mount and every 24 h, with the last check
time persisted so a restart does not re-check immediately, and calls `ReportHealthy()` once the
initial tab's renderer has mounted and its PTY session has opened (§7.5) — not merely once
`TabManager` exists.

**Automatic-check failures are silent and logged.** *The first draft surfaced any error in the
UI, which turns airplane mode, a DNS hiccup or a GitHub outage into a visible error on every
start.* Errors are shown only for a check or an apply the user initiated.

When a release is found, a small notice appears in the tab-bar row: version, a link to the notes,
and an action. Choosing it calls `ApplyUpdate()` and shows a **busy state** — not progress: a
synchronous bound call cannot report bytes, and adding an event subsystem for a progress bar is
not worth it (§12). On success the notice becomes "Restart to apply", which is also what it shows
on any start in the `pendingRestart` state (§7.3). The manual download link stays available on
failure.

Bound methods change the generated `frontend/wailsjs` bindings, which are committed; they are
regenerated and verified as part of this work.

## 9. Testing

TDD, as the repo requires. No unit test touches the network.

`internal/version` — link-time defaults.

`internal/update`, against `httptest` and `t.TempDir()`:
- semver comparison; artefact matching (`universal` from both runtimes; no-match)
- manifest verification: valid; tampered body; key outside the keyring; valid signature from a
  *second* key in the keyring; malformed base64. Fixture keys generated in the test.
- checksum and size mismatch abort before anything is touched
- oversized manifest and oversized artefact are refused
- archive preflight rejects absolute paths, `..`, escaping links, two `.app` roots
- `dev` version short-circuits `Check`; translocated path detection

**Crash consistency is the load-bearing test surface**, so it is tested by fault injection rather
than by asserting on phases: *round 3 broke the previous protocol precisely where "test each
phase" would not have looked.* A fault is injected after **every** numbered step of §7.4, and
reconciliation must reach a correct, idempotent outcome from each — with the identity-based
assertions being the point:

- crash between the record write and the extraction directory (record with no files)
- crash mid-download and mid-extraction
- crash between the staged rename and the exchange (`.nocx-swap.app` holds `newBundleID`)
- crash immediately after the exchange, with no further write
- crash during finalisation, at each of its three steps
- record deleted, truncated, corrupt, or of an unknown schema
- same version re-released; a downgrade; a reinstall over an existing backup
- the bundle moved between the exchange and the restart
- `ReportHealthy` called twice, called with no record, called after backend `Start` failed
- SIGKILL releases the `flock`; a second process then proceeds
- startup try-lock times out against a running `Apply` and does not block

**Two tests that cannot be faked**, both macOS integration tests rather than unit tests:

1. **Payload round trip** — a genuinely built `.app` through `ditto -c -k` → `ditto -x -k`,
   asserting the executable bit, symlink targets, `lipo -archs` still showing both slices,
   `codesign --verify --deep --strict` still passing, and that the extracted bundle launches.
2. **Exchange under load** — copy a built `.app` into a fresh temporary parent, launch *that
   copy*, open a PTY, perform the `RENAME_SWAP`, and verify the session keeps working. *Round 2
   was right that this cannot run through the existing Playwright harness*:
   `playwright.config.ts` starts `wails dev`, so swapping that bundle would be destructive to the
   checkout and would not exercise a release artefact anyway.

**e2e**: an offline update check must not delay terminal startup and must not produce a notice.

Frontend: the readiness promise resolves only on a genuinely started tab and not on a failed one;
the update notice renders from state, including `pendingRestart`; bound calls are stubbed.

## 10. Documentation to amend

- `docs/vision.md:89` — "no app-store or packaged distribution" → GitHub Releases, unsigned
  builds, a self-hosted update chain, and the manual quarantine step.
- `docs/architecture.md` CI paragraphs — record `release.yml`, and `ci.yml` gaining
  `workflow_call` while losing its tag trigger.
- **ADR-0003 "Distribution without a Developer ID"**, following `docs/decisions/0001` and `0002`:
  why there is no publisher signature, that Homebrew closed this route on 1 September 2026, the
  D4 threat model *including update freezing and CI compromise*, and the key-rotation limit and
  key-loss consequence from §6.
- `README.md` — install instructions, the quarantine one-liner against `/Applications/nocx.app`,
  and both rollback procedures from §7.5. Plus the Linux (AppImage) install + update notes.
- **ADR-0006 "Cross-platform auto-update via a platform abstraction"** — the D1 reversal, the
  `Platform` seam, AppImage as the Linux format and its support envelope, and why Wails v3's
  built-in updater was evaluated and rejected.

`docs/architecture.md:179` **is** amended for Linux (D1 reversed, ADR-0006): "MVP is macOS-only;
Windows/Linux are Phase 3" becomes "macOS + Linux now; Windows is Phase 3".

## 11. Work breakdown

Staged deliberately. An updater defect can remove the application itself, so the artefact path
ships and gets used by hand before anything replaces a bundle automatically.

| # | Bead | Work |
|---|------|------|
| 1 | `nocx-vu9` | Bump actions to Node 24 majors. Formally blocks a75.1; ten minutes. |
| 2 | `nocx-hj1` | Resolve the half-tracked `frontend/dist` before it churns in release builds. |
| 3 | `nocx-a75.1` | `internal/version` incl. the plist version, `ci.yml` → `workflow_call`, `release.yml`, dmg + zip, signed manifest, dry run. Ends with a downloadable build installed by hand on another Mac. |
| 4 | new | Payload round trip (§9.1) and manifest verification against the keyring — the two load-bearing mechanisms, landed before anything swaps a bundle. |
| 5 | new | Frontend readiness signal: `Tab` reports started/failed, `TabManager` exposes the promise. Independently useful, and a prerequisite for health. |
| 6 | `nocx-a75.3` | `internal/update`: record, flock, preflight, exchange, reconciliation, finalisation, auto-rollback, refusals, bound methods, UI notice, README. |
| 7 | `nocx-a75.2` | Close as out of scope per D3, with the reason recorded. |
| 8 | `nocx-mbu` | Linux distribution — now **in scope** (D1 reversed, ADR-0006): AppImage build in CI (linuxdeploy + GTK plugin), the `linux` `Platform` impl, and the Linux entries in the manifest. Items 4 and 6 gain their Linux half behind the seam. |

Doc and ADR changes ride inside items 3 and 6.

## 12. Out of scope

Windows (unimplemented `Platform` seam — also needs a conpty branch in `internal/pty`);
Developer ID signing and notarization (D3 — Wails' ad-hoc signature is preserved, not added);
`.deb`/`.rpm` (AppImage is now **in** scope as the Linux format per D1/ADR-0006, but native
packages update via `apt`/`dnf`, not this updater); a styled DMG; `--sequesterRsrc`; artefact
signatures beyond the hash in the signed manifest; signed freshness metadata against update
freezing (D4 records the gap); a beta channel; a Wails event subsystem for download progress;
`nocx-q36` (restoring the pull-request CI trigger — about gates on main, not distribution).

## 13. To verify empirically during implementation

- That a Go-fetched, `ditto`-extracted bundle carries no `com.apple.quarantine` attribute, on
  real hardware. The "updates are friction-free" claim rests on it.
- That both slices of the universal build actually launch, not merely that `lipo` reports two
  architectures.
- That a live terminal session survives a `RENAME_SWAP` of the running bundle (§9.2).
- Durability beyond namespace atomicity is **not** claimed. `RENAME_SWAP` guarantees no observer
  sees a missing install path; the record plus the `fsync` barriers in §7.4 make a crash
  recoverable; neither is a claim about arbitrary power-loss states, and §9's fault injection
  tests process crashes, not power cuts.
