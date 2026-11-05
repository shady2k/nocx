# ADR-1q13mmgw — A nightly's tag carries its build number, and three dated copies are kept

- **Status:** Accepted (owner decision, 2026-11-02, recorded on `nocx-vh2hr.1.17`)
- **Date:** 2026-11-02
- **Supersedes:** [ADR-0097](0097-trunk-based-releases-with-a-nightly-channel.md)'s
  decision 2 in full ("How many nightlies are kept: the last 7") and the tag spelling
  named in its decision 1 (`nightly-YYYYMMDD-RUNID` dated copies). Everything else in
  ADR-0097 — trunk-based releases, the nightly channel, the byte-identical promotion, the
  moving `nightly` alias, `latest` never serving a prerelease — stands unchanged.
- **Related:** [ADR-79b6cy61](79b6cy61-nightly-version-from-the-last-stable-tag.md) (the
  version source, which this record extends by one more nightly tag spelling),
  `nocx-vh2hr.1.5` (the build number itself), `nocx-vh2hr.1.17` (this record)

## Context

The releases page had become unreadable, and every symptom was measurable on it. Each dated
nightly's tag was `nightly-<date>-<CI run id>` and each title was that tag, so the list
read as a wall of run ids that said nothing about which build a person had installed.
Nothing ever pruned: the page grew by one a day and pushed `v0.8.0` out of sight. And the
notes on every release were the same fixed sentence, so opening one did not answer the only
question anyone has — what changed since the build I am running.

The run id was specifically the wrong number. `nocx-vh2hr.1.5` had already decided that the
number a build is known by is the UTC minute of its signed build instant: it is what
`internal/version.Build` stamps, what macOS carries in `CFBundleVersion`, and what the update
notice names. A CI run id is not in the signed manifest at all, and a rerun of an older
revision takes a higher run number for an older build — the opposite of a build number.

## Decision

**A dated nightly is tagged `v<version>-nightly.<build number>` and titled
`nocx <version> nightly · build <build number>`**, where the build number is
`YYYYMMDDHHMM` UTC of the signed `build.builtAt` — `v0.8.1-nightly.202611021101`. The
suffix is therefore the same number About and the update notice show, and two nightlies of
one version are distinguishable from the releases list alone.

- **The tag is derived where the instant is chosen, never passed in.** `release-build.yml`'s
  `validate` job reads `date -u` once, derives the build date, the build number and the tag
  from that one instant, and publishes the tag as an output. The nightly's `prepare` job
  decides only `moved`, the version and the commits; publication and the proofs read the
  build's tag. `decide()` in `scripts/publish-nightly.mjs` refuses a tag whose number is not
  the signed build time, so a tag and a manifest cannot name different builds.
- **The stable-tag derivation ignores both nightly spellings by name.** `nextVersion` skips
  `nightly`, the old `nightly-<date>-<run>`, and the new `v<version>-nightly.<build>`.
  The new spelling is the trap this closes: as semver it sorts above the stable tags of a
  lower minor, so any rule that took "the highest tag" without excluding it would name a
  version no stable release will ever carry. Every other tag must still be a stable
  `vX.Y.Z` and fails the nightly closed otherwise.
- **The newest three dated nightlies are kept**: this build, the one before it — which is
  the build the update proof updates FROM — and one spare. Older dated copies are deleted
  with their tags after a successful publication, in both spellings, so the first run under
  this scheme also retires the `nightly-YYYYMMDD-RUNID` copies already published. A stable
  release and the moving `nightly` alias are never candidates: one predicate decides what a
  dated nightly is, and the pruner, the proof's pair selection and the promotion all use it
  (or its own independent copy, in the proof).
- **Publication is never `latest`.** Both the dated copy and the moving alias stay
  prereleases created with `--latest=false`; GitHub's `releases/latest` therefore keeps
  resolving to the newest stable, which the promotion and the downgrade probe rely on.
- **The notes are the change, not a sentence.** Each release lists the first-parent commits
  since the previous nightly, newest first, each linked to its commit at its full SHA. The
  first dated build on a channel says so rather than pretending to have a predecessor.
- **The moving `Nightly` release keeps its role and title**, and its notes are the notes of
  the build it serves at the moment.

## Consequences

- Promotion accepts both dated spellings, because the old copies stay published until the
  pruner retires them and because the owner may be asked to promote one of them in the
  window before the next nightly.
- The update proof reads the pair with the publication module's own predicate
  (`publish-nightly.mjs dated-tags`), and the Python harness holds an independently written
  copy of it. Two copies are deliberate here: a proof that reuses the publisher's parser
  proves nothing about the publisher.
- A tag is no longer meaningful before the build exists, so a dry-run dispatch produces a
  rehearsal decision without a tag and a real run names one from the build that stamped it.
- The build number can be recomputed from a signed manifest alone, so anyone holding an
  archive can tell which build it is without GitHub.
