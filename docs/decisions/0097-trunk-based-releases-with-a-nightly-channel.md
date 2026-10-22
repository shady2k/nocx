# ADR-0097 — Trunk-based releases with a nightly channel

- **Status:** Accepted (owner decision, 2026-10-21: the five-point shape approved
  2026-10-20 on `nocx-1ebrs.35`, and all eight choices it opened, recorded as
  decided in "The eight decisions" below)
- **Date:** 2026-10-21
- **Decided by:** the owner, 2026-10-21 — the five-point shape approved 2026-10-20
  and all eight choices it opened. Drafted for that decision by the worker on
  `nocx-1ebrs.35`, from the measured cost of the two v0.6.1 release runs, the
  2026-10-19 dry run, and the records named below.
- **Related:** [ADR-0003](0003-distribution-without-a-developer-id.md) (integrity
  model), [ADR-0007](0007-cross-platform-auto-update.md) (the updater this record
  gives a channel to), [ADR-0052](0052-a-code-identity-without-a-publisher-identity.md)
  (the identity a nightly must keep), [ADR-0086](0086-a-third-party-homebrew-tap-may-clear-quarantine-for-the-unsigned-cask.md)
  (the cask), [ADR-0087](0087-linux-user-installs-use-the-signed-release-manifest.md)
  (the installer), [ADR-0095](0095-the-launch-path-raises-a-window-wails-has-already-built.md)
  (the defect the first v0.6.1 attempt died on), `nocx-1ebrs.36` (the updater panic
  that makes every channel unobservable until it lands), `nocx-1ebrs.4` (the owner's
  Mac pass), `nocx-azxe.8` (Linux self-update never applicable in a shipped
  AppImage), `.github/workflows/ci.yml`, `.github/workflows/release.yml`
- **Supersedes in part:** ADR-0007's single-channel manifest fetch and version
  comparison; and `release.yml`'s own decision to re-run the whole `ci.yml` on the
  tag. It supersedes nothing it does not name: ADR-0003, ADR-0052, ADR-0086 and
  ADR-0087 stand, each with what changes for it spelled out below.

## Context

The owner cannot build a release after every pull request, and does not want to:
per-PR CI is twelve jobs (`changes`, `ci-backlog`, `ci-mac`, two `ci-backend`, two
`ci-linux`, `ci-frontend`, four `ci-e2e` shards), the release pipeline is heavier
again, and the macOS runner is the scarcest resource in both. The owner's frame, as
given: roughly 15,000 billable minutes burned in two days in early October. One
honest reconciliation belongs here: the repository is public, and `ci.yml`'s own
header says so — standard GitHub-hosted runners, macOS included, do not bill on a
public repository. What is finite either way is the macOS runner's **availability
and wall clock**, and that part is measured below; the 15k figure is what the same
behaviour costs the day the repository is private again or the runners are larger
ones. The decision this record takes is worth taking under the expensive reading,
because it is the honest one.

A release today re-runs everything. `release.yml` is triggered by a `v*` tag, and
its first job calls the whole of `ci.yml` again on the tagged commit before
anything is built. The two v0.6.1 attempts of 2026-10-20 measured what that costs:

- **First attempt, run 39463655845.** Tag pushed 17:09:52Z. `ci / ci-mac` did not
  start until 17:49:19Z — a ~40-minute queue for the macOS runner — and finished
  18:15:59Z. `build-linux` then failed at 18:24 on the AppImage startup crash
  (ADR-0095), so `sign` and `publish` never ran. Seventy-five minutes, one dead
  release.
- **Second attempt, run 39495985064** (after PR #430 fixed the crash). 20:54:33Z →
  21:47:29Z, 53 minutes, all green. Of those, the `ci / *` block consumed 46m54s
  (20:54:35 → 21:41:29); helpers, both builds, `sign` and `publish` together took
  5m56s. The re-run of already-green CI was 89% of the release's wall clock.
- **The dry run, run 39283960973** (2026-10-19, `workflow_dispatch`). 71 minutes
  to build, sign and verify everything and publish nothing — and it re-runs the CI
  block too.

The re-run buys almost nothing: the tagged commit is a merge of pull requests whose
CI ran green on the exact head revisions that were merged, and `main` is always
releasable by that argument. What the re-run does buy — detection of a defect that
exists in no single pull request, only in the merge — is exactly what a nightly
build buys as well, at a fraction of the cost, every day.

The owner approved the feature and its shape on 2026-10-20 (`nocx-1ebrs.35`):

1. Merges to `main` stay per-PR with the current PR CI; `main` is always
   releasable.
2. A nightly workflow builds from `main` once a day **only if `main` moved**:
   macOS + Linux AppImage + the foreign-distro AppImage verify + signing,
   published to a `nightly` channel of the signed update manifest; the in-app
   updater gains a channel setting (stable default, nightly opt-in).
3. A release is a **promotion** of an already-built nightly: tag the same commit,
   publish the same artefacts as stable, update the cask — **no rebuild**.
4. `release.yml` stops re-running the whole `ci.yml` on the tag; it checks via the
   API that CI was green on that sha.
5. Unfinished work goes behind a settings flag, not a long branch.

This record evaluated that shape rather than assuming it; the eight choices it
opened were taken by the owner on 2026-10-21 and are recorded below as decided.

## Decision

**Adopt the shape as approved, with the version scheme and proof below. The eight choices the shape opened were decided by the owner on 2026-10-21 and are recorded here as decided, not as recommendations.**

- **Per-PR CI is unchanged.** `ci.yml` keeps its triggers and its twelve jobs. A
  pull request is where quality is bought; nothing in this record moves that.
- **A nightly workflow runs once a day at a fixed hour and does nothing unless
  `main` moved since the last nightly.** "Moved" means product code moved: the
  diff since the last nightly's sha is tested against the same case list the
  `changes` job already owns (`.beads/`, `docs/`, `.internal/`, the backlog-gate
  hooks and root `*.md` are not product) — one owner for that predicate, not a
  second list. When it runs, it builds exactly what a release builds — helpers,
  the universal macOS bundle, the Linux AppImage, the foreign-distro AppImage
  verify, the codesign requirement gate, the signed manifest — and publishes the
  artefacts to the nightly channel as a prerelease. It does **not** re-run `ci.yml`:
  every pull request in the range already ran it. The nightly build itself is the
  merged-tree gate — a merge that broke what no single PR broke is a red nightly
  and no nightly that day, never a broken stable.
- **A stable release is a promotion.** The owner names a nightly they have tried;
  the promotion tags that nightly's commit with the stable `vMAJOR.MINOR.PATCH`,
  publishes the **same artefact bytes** as the stable release, re-signs the
  manifest to say the stable version, and updates the tap cask. Nothing is
  rebuilt, recompiled or re-verified: the bytes were verified the night they were
  built.
- **`release.yml` stops re-running `ci.yml` on the tag.** The promotion verifies
  greenness through the GitHub API instead — and this has a measured trap: commits
  on `main` carry **no** check-runs of their own. The merge commit of PR #446
  (69be67414) has 0 check-runs; its PR head sha (fd7de0256) has all 12. The check
  must therefore resolve the pull requests merged into the promoted range and read
  the check-runs of their **head** revisions; `commits/{sha}/check-runs` on the
  merge commit answers nothing and must not be written as the green check.
- **The updater gains a channel setting**, stable by default, nightly opt-in. The
  channel selects the manifest's base URL; both channels' manifests are signed by
  the same key and verified by the same compiled keyring. On the stable channel
  the update rule is today's semver comparison. On the nightly channel it is
  "newer semver **or same version with different artefact bytes**" — see the
  version scheme for why.
- **The tap carries two casks, and each follows its own channel.** `Casks/nocx.rb`
  follows stable and `Casks/nocx@nightly.rb` follows nightly, each declared
  `conflicts_with` the other so neither can shadow its sibling. `auto_updates
true` stands on both (ADR-0086): each install self-updates on its own channel
  in place, and `brew upgrade` must not fight either. The promotion updates the
  stable cask's version and sha256; the nightly workflow updates the nightly
  cask's on **every published nightly** — one tap commit per nightly, a real
  pinned sha256, never `version :latest` or `sha256 :no_check`: brew must verify
  the bytes it installs, the same verification ADR-0086 built the tap on. The
  nightly cask installs the app preset to the nightly update channel, so a
  `brew install nocx@nightly` keeps self-updating on nightly from its first
  launch. Leaving nightly is a reinstall — `brew uninstall nocx@nightly`, then
  `brew install nocx` — never a downgrade, which is the same no-downgrade rule
  the channel setting carries. `scripts/install/install.sh` (ADR-0087) keeps
  fetching the stable latest release only: the Linux installer gains no channel
  of its own.

### The version scheme, and why promotion forces it

Promotion publishes **the same bytes** as stable, so the version a nightly stamps
into the binary, `Info.plist` and manifest must already be the version those bytes
will be promoted as. The two obvious schemes fail measurably:

- **A prerelease suffix (`0.7.0-nightly.20261022`)** makes a promoted install
  disagree with its own manifest forever. `internal/update.IsNewer` normalises
  both sides with `semver.Canonical` (manifest.go:81), which drops the prerelease
  — so nightly-to-nightly updates would compare equal and never fire, and fixing
  that by comparing full semver instead makes a promoted install see the stable
  `0.7.0` as newer than its own `0.7.0-nightly.N` on every check, re-downloading
  the same bytes in a loop. The suffix also violates the `CFBundleVersion` grammar
  `release.yml` asserts, and `validate` rejects any non-`vMAJOR.MINOR.PATCH` tag
  outright.
- **A version the stable release will never use (`0.0.0`, the dry-run scheme)**
  cannot be promoted without a rebuild, which is the thing promotion exists to
  avoid.

**The nightly stamps the next stable version** — read from a version source on
`main`, bumped by the promotion itself — and the _nightly-ness_ lives in the
channel, not in the version. A nightly of `0.7.0` and the promoted stable `0.7.0`
are the same version with (at promotion time) the same bytes, so no install ever
disagrees with its manifest and no comparison rule can loop. The cost is honest
and accepted: two nightlies of one version differ in bytes, so the nightly
channel's update rule must compare artefact identity, not version alone. A stable
user who switches to the nightly channel updates once; a nightly user who
switches back to stable **never downgrades** — they wait for the next stable, or
reinstall by hand. That is the correct default for an updater whose transaction
core has no downgrade path, and the channel-switch wording must say it.

### The end-to-end proof

Testing rule 2, one sentence: **a person who opted into the nightly channel is
offered, and installs, tonight's build from the app they ran yesterday, through
the real signed channel.** The automated check that watches it, on Linux, lives in
the nightly workflow itself: after publishing nightly N+1, a proof step takes the
**real** previous nightly AppImage N (not a fixture), runs the **real** updater
from inside it — `APPIMAGE` set, real manifest, real signature, production
keyring — and asserts the manifest verified, the AppImage was swapped, and
`ReportHealthy` finalised with the new build reported. The check proves the
channel end to end because it _is_ the channel. On darwin there is no runner
(ADR-0007's seam knows this); the owner's recorded Mac pass remains the platform's
acceptance, already the root epic's DONE WHEN (`nocx-1ebrs.4`).

Prerequisites, in order, all already tracked:

- `nocx-1ebrs.36` — the updater panics on every check today (`main.go:400` passes
  a nil `http.Client`). Until it lands, no channel behaviour is observable at all.
- `nocx-azxe.8` — the shipped AppImage derives its install path from the read-only
  squashfs mount, so Linux self-update has never been able to apply. The proof
  step above cannot run until this is fixed; until then a Linux nightly is
  install-only, and the setting must say so rather than offer a control that
  cannot work (AGENTS.md: a soft degrade is visible in the product).
- The owner's Mac pass (`nocx-1ebrs.4`) — the darwin half of the proof.

### The eight decisions, owner-decided 2026-10-21

Each was opened by this record in recommendation form; the owner accepted all
eight as recommended, and they are recorded here as decided.

1. **Where nightlies live.** The same repository, as prereleases under a moving
   `nightly` tag plus dated copies (owner-decided 2026-10-21). Consequence: `latest` never
   serves them (prereleases are excluded), so the stable channel's URL is
   untouched by construction; URLs stay under `releases/download/` where
   ADR-0087's pin expects them; one signing secret and one threat surface. The
   alternative — a separate repository — doubles the secrets, widens ADR-0003's
   "compromised repository" boundary to a second repo, and buys isolation this
   record cannot find a use for.
2. **How many nightlies are kept.** The last 7 (owner-decided 2026-10-21).
   Consequence: a week
   of bisect points at 488 MB each (~3.4 GB), the oldest deleted by the workflow;
   retention is a setting, not a promise, and the moving `nightly` tag always
   points at today's.
3. **Mac-only at first, or both platforms.** Both from day one
   (owner-decided 2026-10-21).
   Consequence: the pipeline already builds both and the Linux runner cost is
   negligible; but until `nocx-azxe.8` is fixed the Linux nightly is install-only
   — its in-app self-update cannot apply — and the channel setting must say so
   rather than offer a dead control.
4. **Channel-switch UX.** Settings → Updates, stable default, with the nightly
   opt-in naming its nature in one sentence ("built from main every night it
   moved; not tried by anyone before you") and the return path stated
   ("switching back never downgrades; you wait for the next stable or reinstall")
   (owner-decided 2026-10-21). Consequence: no silent channel changes, and the one irreversible edge (leaving
   nightly) is consented to at the switch, not discovered after it.
5. **Minutes budget per day.** The nightly runs build, sign and publish only —
   no CI re-run — so a day costs the measured ~6-minute pipeline plus the macOS
   build's runner minutes, and nothing when `main` did not move
   (owner-decided 2026-10-21).
   Consequence: the queue the v0.6.1 tag run measured (~40 minutes waiting for
   `ci-mac`) is never paid by a scheduled job; under the public-repo reading the
   dollars are zero and the constraint is concurrency, under the private-repo
   reading the cap is what keeps the 15k frame from returning.
6. **The version scheme.** The one above — nightlies stamp the next stable
   version, nightly-ness lives in the channel (owner-decided 2026-10-21). Consequence: promotion is
   byte-identical and no comparison rule can loop; the cost is the channel-aware
   update rule and a version source on `main` the promotion bumps. The
   prerelease-suffix alternative keeps versions self-describing but, measured
   against `IsNewer`'s canonicalisation and the `CFBundleVersion` grammar, either
   never updates nightly-to-nightly or loops after promotion — both are worse.
7. **Who may promote.** The owner only, by naming a nightly they have tried
   (owner-decided 2026-10-21). Consequence: promotion is a manual dispatch, never automatic; a
   bad nightly is caught by the person the release is for, which is the shape the
   owner approved.
8. **The nightly channel's brew surface.** The tap carries a second cask,
   `Casks/nocx@nightly.rb` beside `nocx.rb`, each `conflicts_with` the other
   (owner-decided 2026-10-21). The nightly workflow updates the nightly cask's
   version and sha256 on every published nightly — one tap commit per nightly, a
   real pinned sha256, never `version :latest` or `sha256 :no_check`: brew must
   verify the bytes it installs, the same verification ADR-0086 built the tap on.
   The nightly cask installs the app preset to the nightly update channel, so a
   `brew install nocx@nightly` keeps self-updating on nightly from its first
   launch; leaving nightly is `brew uninstall nocx@nightly` then `brew install
nocx` — a reinstall, never a downgrade, matching the channel's own
   no-downgrade rule.

## What stands and what changes, record by record

- **[ADR-0003] — stands.** No Developer ID, no notarization, the manual quarantine
  step on first install, integrity is the ed25519-signed manifest: all unchanged,
  and the nightly channel is signed by the same key and verified by the same
  compiled keyring, so the threat model carries over byte for byte. What changes
  is scope, not decision: "the Releases page is the distribution channel, full
  stop" now describes one page carrying two channels — stable releases and
  prerelease nightlies — instead of one.
- **[ADR-0007] — stands, superseded in part.** The platform seam, the transaction
  core, the AppImage-as-self-update-target, verify-before-parse: all stand. What
  changes is the half this record names in its supersedes line: the manifest fetch
  grows a channel (the `releases/latest/download` BaseURL becomes the stable
  channel's URL, not the only one), and the comparison grows the nightly rule
  above. The Linux refusal ("no `APPIMAGE` → no self-update") stands and becomes
  load-bearing for the proof step.
- **[ADR-0052] — stands, and becomes more important.** The nightly macOS build
  signs with the same project certificate and passes the same committed
  `requirement.txt` gate, so a keychain identity written by one nightly is
  recognised by the next. A nightly channel without a stable identity would churn
  the keychain on every update; with ADR-0052 it does not. Nothing changes.
- **[ADR-0086] — stands, and grows a second cask.** The tap, the postflight
  quarantine step, `auto_updates true`: unchanged, on both casks. What moves is
  the trigger and the surface: the stable cask's version and sha256 are updated
  by the **promotion**, not by a tag-triggered from-scratch release, and the tap
  gains `Casks/nocx@nightly.rb` beside `nocx.rb` — each `conflicts_with` the
  other, the nightly cask's version and sha256 updated by every published
  nightly (owner-decided 2026-10-21). Both casks pin a real sha256; neither uses
  `version :latest` or `sha256 :no_check`.
- **[ADR-0087] — stands.** The installer keeps fetching the stable latest release
  and keeps its URL pin. Nightlies do not change what the installer accepts,
  because the installer never sees them.
- **`release.yml`'s tag-triggered `ci.yml` re-run — superseded** by the API green
  check above. This is a workflow decision this record names so it stops being
  folklore: the 89%-of-wall-clock measurement is the reason.

## Consequences

- **A release stops costing a re-run.** Promotion is helpers-free: it downloads
  artefacts that exist, re-signs a manifest, creates a release and updates a cask
  — minutes, against the measured 53. The nightly adds one build+sign+publish
  (~6 minutes of the measured pipeline, plus the macOS build's runner time) per
  day, only when `main` moved.
- **The nightly is the merged-tree releaseability gate.** Two green pull requests
  can still merge red; today that is discovered by a release attempt (or the
  e2e suite's merged-tree runs), tomorrow morning it is discovered by the nightly,
  before any release is attempted.
- **`main`'s version source becomes release-critical.** The version a nightly
  stamps comes from `main`, so the promotion must bump it on `main` in the same
  act, or the next nightly stamps the version just released and looks stale. The
  bump is a commit like any other — CI runs on its PR like everything else.
- **The first release this pipeline builds is the next one.** v0.6.1 shipped on
  2026-10-20 through the existing tag pipeline (run 39495985064), with the owner's
  tag question settled by leaving the tag where it was. Whatever follows v0.6.1 is
  the first promoted nightly; nothing under v0.6.1 exists in the field, so nothing
  needs migrating.
- **Nightly storage is real and bounded.** One nightly's asset set is 488 MB
  (measured on v0.6.1). How many are kept is an owner decision below, not an
  accident of the workflow.
- **No code or workflow changes ride with this record.** It is the design the
  implementation beads will hang off; `nocx-1ebrs.35`'s own prerequisite holds —
  no implementation starts before this record is accepted and the end-to-end
  update proof exists.

## Revisit when

- A Developer ID or notarization arrives (ADR-0003's own revisit): the quarantine
  and tap reasoning changes under it, and so does the cask's role.
- Wails v3's updater grows a bundle-aware mode (ADR-0007's revisit): the hand-rolled
  transaction core this channel rides may be replaceable then.
- GitHub changes `latest`/prerelease semantics, or the artefact retention rules.
- The macOS runner queue stops being the constraint it measured as, or the
  repository's visibility changes and every "free minutes" assumption flips — that
  is the day the 15k frame becomes the live price again.
