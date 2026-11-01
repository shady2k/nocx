# ADR-79b6cy61 — A nightly's version is the next patch after the last stable tag

- **Status:** Accepted (owner decision, 2026-10-31)
- **Date:** 2026-10-31
- **Supersedes:** [ADR-0097](0097-trunk-based-releases-with-a-nightly-channel.md)'s
  version source and its bump requirement: the sentence "read from a version source on
  `main`, bumped by the promotion itself", the `VERSION` bump PR it prescribes, and the
  consequence that a nightly may reuse a released version until that PR merges. The rest
  of ADR-0097 — trunk-based releases, the nightly channel, the byte-identical promotion,
  the updater's equal-version rule — stands unchanged.
- **Related:** [ADR-0101](0101-a-protocol-compatible-coordinator-is-kept-across-an-update.md)
  (nightlies of one version differ by commit), [ADR-2syp2fpx](2syp2fpx-one-cask-and-the-channel-setting-not-two-casks.md)
  (one cask, channel setting), task `nocx-vh2hr.1.2`

## Context

ADR-0097 chose the right shape: a nightly stamps **the next stable version**, and
_nightly-ness_ lives in the channel, so the bytes a nightly verified can be published as
stable without a rebuild. It then sourced that version from a `VERSION` file on `main`
that the promotion bumps through a pull request. That source has three measured costs:

- **The release gate is a merge, not the tag.** The promotion creates `vX.Y.Z` and asks a
  PR to move `VERSION` to the next patch. Until that PR merges, every nightly built from
  `main` stamps `X.Y.Z` again: a new nightly of a version stable already published, which
  the promotion then refuses ("not newer than stable"). The channel has a dead window
  whose length is a human review cycle, and the failure appears at the next promotion
  rather than where it was caused.
- **The bump PR cannot test itself.** `GITHUB_TOKEN` cannot trigger `ci.yml` on the PR it
  creates, so the owner must close and reopen it before merging. A one-line release chore
  becomes a manual, order-dependent step in the release path, and forgetting it is silent
  until the next promotion is refused.
- **Two sources for one number.** The nightly read `VERSION`; the promotion wrote it; the
  README and release notes had to agree with it; and `VERSION` had to be listed as a
  pipeline file so changing it did not count as a product change. The tag was already the
  immutable fact — the file was a second answer to a question the tag answers.

## Decision

**A nightly's version is the next patch after the highest stable tag reachable from its
commit.** After `v0.8.0` a nightly embeds `0.8.1`; after promotion creates `v0.8.1`, the
next nightly embeds `0.8.2`. No file on `main` names the version, and nothing advances it
between a release and the next nightly.

- **One owner for the rule.** `nextVersion(tags)` in `scripts/publish-nightly.mjs` is the
  only place that chooses among tags. The nightly workflow passes it the output of
  `git tag --list --merged <sha>` — every tag the build commit can reach — and
  `prepare()` calls it, so a version cannot be handed in beside the tags. `git describe`
  is not used: "nearest tag" is a different policy and it walks toward a commit instead of
  answering which release is the newest.
- **Nightly tags are excluded by name.** The moving `nightly` alias and the immutable
  `nightly-YYYYMMDD-RUNID` tags are not releases. Every other tag must be a stable
  `vMAJOR.MINOR.PATCH`; a tag that does not parse fails the nightly closed rather than
  being skipped, because skipping an unreadable release would silently reuse its version.
  A history with no stable tag fails closed for the same reason.
- **Promotion is unchanged byte for byte.** It still verifies the tried nightly's signed
  provenance and payload digests, copies the exact bytes, rewrites only the manifest's
  URLs and signs it as stable, then creates the stable tag. It changes no source file and
  no version file — there is no version file to move. The stable tag it creates is the
  input to the next nightly's version, so the release advances the number by existing.
- **A released version cannot be promoted twice.** `verifyCandidate` still requires the
  candidate's version to be strictly newer than the latest stable release, so a nightly
  built before the new tag, or a stale candidate, is refused before any tag, upload or
  cask write. The updater keeps its numeric semver rule and its equal-version,
  different-commit rule (ADR-0101); neither changes.
- **Nightly and promotion serialize.** Both workflows join one `nocx-release` concurrency
  group that queues rather than cancels, so a nightly cannot publish while a promotion is
  reading its tags and publishing its bytes.
- **README and release notes stay the promotion's precondition.** The promoted commit must
  carry the unique `**Status:**` line naming that version and link
  `docs/release-notes/vX.Y.Z.md`. Since the version is tag-derived, the owner merges the
  next release's README/notes **before** the nightly that will be promoted; that nightly
  then already carries the right version, and no second step is needed afterwards.

### Correction recorded with this record

ADR-0097's version-scheme section says `semver.Canonical` "drops the prerelease". It does
not: `semver.Canonical` drops **build metadata** and keeps the prerelease. The rejection
of a `0.7.0-nightly.N` suffix in that section therefore rests on a wrong mechanism, not a
wrong conclusion: the actual traps are `CFBundleVersion`'s grammar, the tag validator's
`vMAJOR.MINOR.PATCH` requirement, and a promoted install comparing its prerelease version
against the stable version on every check. This record supersedes the sentence, not the
choice of a numeric version.

## Consequences

- The nightly's version is deterministic from the commit and its tags: the same commit,
  with the same reachable tags, always yields the same number, and two nightlies of one
  version differ only by commit (the existing nightly identity rule).
- The next nightly after a promotion needs no PR and no owner action; the released tag
  is enough. There is no dead window in which nightlies reuse a published version.
- A release's notes PR now also decides the version its nightly will carry. That is
  earlier than before — before, the number came from a file the promotion wrote later — and
  the promotion still refuses a candidate whose commit does not carry the matching status
  line and notes.
- The `VERSION` file is deleted; `scripts/product-change.mjs` no longer lists it. A local
  release build still passes its version explicitly (`make build-release VERSION=...`).
- Nightly and promotion runs now share a concurrency group; a promotion dispatches behind
  a running nightly (including its Linux proof), which is the ordering the tag dependency
  requires.
