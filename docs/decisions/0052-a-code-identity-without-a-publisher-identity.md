# ADR-0052 — A code identity without a publisher identity

- **Status:** Accepted
- **Date:** 2026-08-30
- **Related:** [ADR-0003](0003-distribution-without-a-developer-id.md) (distribution
  without a Developer ID — amended by this record),
  [ADR-0050](0050-the-keystore-holds-a-key-only-a-person-can-take.md) step 3 (the
  keystore is reached under our own code identity),
  [ADR-0007](0007-cross-platform-auto-update.md) (the platform abstraction the
  updater's signature check lives behind), plan
  `.internal/plans/2026-08-30-a-stable-code-identity-and-what-the-release-publishes.md`,
  beads `nocx-p7ftq`, `nocx-hid2u`, `nocx-rsdik`, `nocx-rybbm`.
- **Amends:** ADR-0003's statement that the shipped `.app` carries an ad-hoc
  signature. Everything ADR-0003 decided about PUBLISHER identity stands unchanged.

## Context

The backend became a per-user daemon (`cmd/nocx-server`), so the vault now lives in a
process that outlives the window. ADR-0050 answered what that process may keep in the
OS keystore, and its step 3 says how it must reach it: not through `/usr/bin/security`,
which anything on the machine can invoke, but directly, under nocx's own code identity —
"signed by a project certificate whose designated requirement is stable across
releases".

The release did not produce such an identity. Wails signs a production bundle ad-hoc
(`codesign --force --deep --sign -`), and an ad-hoc signature's designated requirement
**is** the code hash: `cdhash H"…"`. A keychain item bound to it recognises exactly one
build. The next release, differing by a single linked version string, is a different
program as far as the requirement is concerned. That is the precise opposite of what
step 3 needs — an identity that is stable across releases — and no amount of care in the
provider could have fixed it, because the property is a property of the signature, not
of the code that reads the item.

ADR-0050 recorded the alternative as a measurement taken on one laptop: a self-signed
code-signing certificate yields `designated => identifier X and certificate leaf =
H"…"`, anchored to the certificate rather than to the code. Nothing in the release used
it.

## Decision

**A self-signed project certificate is the release's signing identity on macOS.** It is
minted by `scripts/darwin/create-signing-certificate.sh` (self-signed, `codeSigning`
EKU, 7300 days), exported as a `.p12`, and reaches CI as `MACOS_SIGNING_P12` and
`MACOS_SIGNING_P12_PASSWORD`. Nothing in the repository holds either.

`build-macos` signs **inside-out**: the coordinator first, the bundle last, because the
bundle's signature seals `Info.plist`, the icon and both executables, so anything
touching the tree afterwards invalidates it. `--deep` is not used to sign — it would
sign the coordinator as a by-product of signing the app rather than as its own decision
— and survives only in `--verify --deep --strict`, where it means "check nested code
too". The coordinator is signed with an explicit `-i com.nocx.server`, because a bare
Mach-O takes its identifier from its file name and the identifier is half of the
designated requirement; a defaulted identifier is a requirement that can change without
anyone deciding to change it. The bundle needs no `-i`: it takes `CFBundleIdentifier`
from `Info.plist`.

**The requirement is committed and every build is compared against it.**
`build/darwin/requirement.txt` holds the string `codesign -d -r-` prints for the
coordinator, and the release job fails if the built one differs — before packaging, and
again on the extracted bundle after packaging. The same job rebuilds the coordinator
twice with different version stamps and asserts that the requirement is identical while
the cdhash is not, so the property this whole design rests on is measured on the
artefact rather than remembered from a laptop.

**The signature buys identity and nothing else.** It is not a step towards
notarization, it does not change what a stranger can conclude about the download, and it
is macOS-only.

## Rationale

### What this does not buy, stated plainly

There is no Apple Developer ID and there will not be one (ADR-0003). Everything that
follows from that is unchanged by this record:

- **No notarization.** It requires enrolment in the Apple Developer Program, and a
  self-signed certificate is refused.
- **No Gatekeeper policy.** Gatekeeper asks who published this; a certificate that
  vouches only for itself answers nothing.
- **No hardened runtime, no entitlements.** Those are notarization's companions, and
  turning them on without it would buy restrictions and no acceptance.
- **The manual `xattr -dr com.apple.quarantine` on first install stands**, exactly as
  the README documents it.
- **Homebrew stays closed.** Every cask must pass Gatekeeper from 2026-09-01, and the
  bypass no longer exists anywhere in `brew`.

**Integrity of what was downloaded is still the ed25519-signed `manifest.json`, on both
platforms.** A self-signed certificate proves nothing to a stranger — anybody can mint
one naming anything — so it is not evidence that the bytes came from us, and we do not
present it as though it were. The updater's `codesign --verify` is, and always was, a
check that the seal on the extracted bundle is intact after a pack/extract round trip;
it says nothing about who signed it. The two mechanisms answer different questions and
neither is being retired in favour of the other.

### Why self-signed rather than nothing

The requirement is matched by the local keychain, which does not need to trust the
issuer — only to recognise the same leaf certificate again. Publisher identity requires
a party the user's machine already trusts; code identity requires only that the signer
stay the same. The second is available to us; the first is not. That is the whole
argument, and it is also the reason the certificate cannot be quietly upgraded into a
distribution story later: a different problem needs a different issuer.

### Why Linux gets nothing here

No OS mechanism on Linux reads an ELF signature at run time, so a signed AppImage would
change no decision any component makes. And the Secret Service authenticates the caller
by uid, not by code identity, so the property ADR-0050 step 3 buys on macOS has no
counterpart to buy there: the same change would cost a key and a CI job and alter
nothing about who can read a stored item. Linux integrity remains the signed manifest,
which is what it already was.

### Why the identifier is pinned rather than defaulted

`com.nocx.server` is asserted at the codesign call because the alternative is a
requirement that follows the output file's name. A rename of the build artefact, or a
build that writes it under a temporary name, would then silently produce a different
identity — the failure mode this ADR exists to prevent, arriving through the back door.

## Consequences

**A certificate change is an identity change.** The requirement is anchored to the
certificate's leaf hash, so replacing, rotating, re-minting or mis-importing it produces
a different requirement, and every install holding keychain items under the old one
stops being recognised as their owner. Their items are not deleted; they are simply no
longer reachable by us. Today this costs nothing, because there are no installs. When it
stops costing nothing the answer is a project CA with the requirement anchored to the CA
rather than to the leaf, which lets a leaf be reissued without moving the identity —
that is a decision to take before the first install exists, not after. The certificate is
issued for 7300 days so that expiry does not force it.

**Losing the private key is losing the identity.** It is not the manifest key's failure
mode, which strands in-place updates (ADR-0003); it is that no future build can be
recognised as the same program by any keychain item already written. Treat it as a
release secret, held offline beside `RELEASE_SIGNING_KEY`, and never regenerated as a
convenience.

**One honest deviation from how the manifest key is handled.** `release.yml` keeps the
ed25519 manifest key in a `sign` job that runs no build tooling. The code-signing key
cannot have that: `codesign` operates on the assembled bundle, on macOS, in the job that
assembles it, so the key is present in a job that also runs `npm ci`, `go build` and the
Wails CLI. The two keys stay in different secrets and different jobs, so the manifest
key's isolation is exactly as strong as it was — but the code-signing key's is weaker,
by construction, and pretending otherwise would be the kind of claim this ADR is written
to avoid.

**The gate is only as good as the committed file.** `build/darwin/requirement.txt` is
not in the repository at the time of writing: its content is what the real certificate
produces, so it can only be read out of a run that has the secret. The release job
therefore prints the built requirement and fails when the file is absent or empty,
rather than comparing against `""` and passing. Until that file is committed, no release
can complete — which is the intended shape: an unfinished setup is a red build, not a
quiet one.

**What is still not done.** ADR-0050 step 3 needs the darwin provider to call
Security.framework directly, which needs `CGO_ENABLED=1` for the coordinator; the
release builds it with `CGO_ENABLED=0`. Step 4, user presence on the item, is not
started. This ADR removes the obstacle that was in front of both — there is now an
identity to bind to — and closes none of the rest (`nocx-rybbm`).
