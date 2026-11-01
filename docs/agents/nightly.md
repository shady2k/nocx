# Nightly publication and the artifact refusal gate

`nocx-1ebrs.66.1.4` owns nightly publication and this manifest contract.
`nocx-ngw57` owns the packaged-artifact inspection. Promotion (`nocx-1ebrs.35.3`)
and the one stable cask consume these bytes, not a second build — there is no nightly cask: the channel is the app's own
setting, decided in [ADR-2syp2fpx](../decisions/2syp2fpx-one-cask-and-the-channel-setting-not-two-casks.md).

## Rehearse, then publish

1. Dispatch `nightly.yml` on the working branch with `dry-run=true` and
   `linux-only=true` (both defaults). It builds that branch's Linux artifact,
   runs the foreign-distro verification and the packaged-artifact check, and
   publishes nothing. No macOS secrets are needed for this iteration.
2. After Linux is green, dispatch with `dry-run=true`, `linux-only=false` once.
   This builds both platforms, checks the project-certificate signature and
   designated requirement on macOS, inspects the actual zip and AppImage,
   and signs/verifies the manifest and provenance. It still publishes nothing.
3. After the nightly and promotion PRs are merged, dispatch `nightly.yml` on
   **main** with `dry-run=false`, `linux-only=false`. It publishes only if
   product changes exist since the last successful signed nightly. The daily
   schedule does the same. A branch publication or Linux-only publication is
   refused by `authorize()` before building. Use the resulting immutable dated
   tag for promotion; do not run the transitional tag-based release path.

No notarization is performed: ADR-0003/0052 deliberately use the project's
certificate, not an Apple Developer ID. The checks verify the code signature,
committed designated requirement and bundle seal, not Apple notarization.

`release-build.yml` also admits direct branch dispatch for Linux-first build
rehearsal. It has no publish job. Its `dry-run` input documents that boundary;
changing it cannot authorize publication.

A failed publication is recovered by a **fresh dispatch**, not "rerun failed
jobs". The run attempt and immutable dated tag are part of signed provenance.
The script restores the old signed alias after a failed replacement. If GitHub
also rejects restoration, the job fails loudly; recover from the immutable dated
release and its signed provenance. GitHub cannot atomically swap release assets,
so the moving endpoint can be briefly unavailable during replacement.

## Manifest and cask contract

The cask here is the **stable** one (`Casks/nocx.rb`). `nocx-1ebrs.35.4`'s nightly cask and `nocx-1ebrs.66.1.7`'s
install-time channel preset are cancelled by [ADR-2syp2fpx](../decisions/2syp2fpx-one-cask-and-the-channel-setting-not-two-casks.md):
one app, one cask, and `update.channel` decides which line an install follows.

The version is **not** stored in a file on main. A nightly reads the stable
tags its commit can reach (`git tag --list --merged HEAD`) and embeds the next
patch after the highest one: after `v0.8.0` it stamps `0.8.1`, and after
`v0.8.1` exists it stamps `0.8.2`. Nightly tags are excluded, `git describe` is
not used, and a history with no readable stable tag fails the nightly instead of
guessing. One function owns the rule (`nextVersion` in
`scripts/publish-nightly.mjs`); the workflows pass it the tag list and never
derive a version themselves. The stable tag a promotion creates is therefore
the next nightly's input, with no version PR between them. A nightly stamps that
version into both executables and both plist version fields. Nightly identity
is the channel/tag, not a prerelease version suffix.

`manifest.json` retains the updater's existing strict schema:

- `version`: the next stable MAJOR.MINOR.PATCH.
- `released`: UTC RFC3339 timestamp.
- `notesUrl`: `https://github.com/shady2k/nocx/releases/tag/<dated-tag>`.
- `artifacts`: `os`, `arch`, `format`, `url`, `sha256`, `size` for each platform.
  Darwin: `darwin`, `universal`, `zip`, `nocx-<version>-darwin-universal.zip`.
  Linux: `linux`, `amd64`, `AppImage`, `nocx-<version>-linux-amd64.AppImage`.
- `helpers`: `os`, `arch`, `url`, `sha256`, `size` for linux/amd64,
  linux/arm64, darwin/amd64 and darwin/arm64. Names are
  `nocx-helper-<version>-<os>-<arch>`.

There are **no channel or tag fields** in the updater manifest: adding them would
change its strict wire schema. Channel is the signed document's serving URL
(`/releases/download/nightly/manifest.json`); all artifact and helper URLs use
`/releases/download/nightly-YYYYMMDD-RUNID/<name>`, the immutable dated tag.
The moving tag and dated tag carry the same manifest bytes.

The cask generator receives the Darwin zip entry's **sha256** verbatim. This is
the SHA-256 of the actual signed, packaged zip; not the DMG hash, not the
manifest hash, not a rebuilt zip. Its URL is that entry's immutable URL.

`nightly.json` is separate signed provenance:
`{schema:1, sha, version, tag, runId, runAttempt, artifacts:[{name,sha256,size}]}`.
It binds every public asset except itself and its signature: the two updater
payloads, Darwin DMG, four helper binaries, `manifest.json`, `manifest.json.sig`,
`verified-darwin.json`, and `verified-linux.json`. Promotion must independently
verify the successful outer `nightly.yml` run at that SHA/runId/attempt and the
dated tag's SHA; provenance does not claim a run is successful before it ends.
The last seven dated releases are kept, plus the moving alias.

Both signatures are detached Ed25519 over the **exact JSON bytes**: raw 64-byte
signature, standard base64 plus optional trailing newline, in `<name>.sig`.
`RELEASE_SIGNING_KEY` is the seed secret; the public keys are the compiled
`internal/update/keyring.go` keyring. No signing key is committed.

## Acceptance still needed on GitHub

The pod tests execute decision logic, signed fixtures, deliberate field mutations,
rollback failures and the real desktop updater's production signed-manifest fetch.
They do not establish GitHub permissions, artifact transport, macOS certificate
import/signature preservation, the foreign-distro container on a hosted runner,
secret/keyring pairing, or release/tag mutation in the real repository.

The owner must see the Linux-first rehearsal, then the full rehearsal, then one
main publication succeed. Inspect `verified-*.json`, signatures, both prereleases,
the matching SHA and immutable URLs. An unchanged-main run must skip build/publish.
Then promote that exact dated copy. In-app previous-nightly update proof remains
with `.35.5`/`.35.6`; the owner's Mac pass remains required. Live agent-token
propagation is not covered by the metadata diagnostic, so `nocx-ngw57`'s broad
six-regression falsification criterion remains pending that seam's acceptance.
