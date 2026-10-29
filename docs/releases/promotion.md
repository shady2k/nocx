# Promote a tried nightly to stable

[ADR-0097](../decisions/0097-trunk-based-releases-with-a-nightly-channel.md)
is the contract. `.github/workflows/release.yml` is named **Release** so the
landing page's `workflow_run` still observes a successful stable publication.
It has no tag trigger, CI call, compiler, code signing, packaging or AppImage
verification. Those belong to the nightly producer, `release-build.yml`.

## Rehearse without secrets

Dispatch **Release** on the implementation branch with `dry-run=true` (the
default). It runs `node --test --test-reporter=tap
scripts/release/promotion*.test.mjs`. Fixtures use a generated Ed25519 key and
small stand-in files. This proves the decision logic, original-byte signature
verification, unchanged payload digests, refusal cases, GitHub boundary error
handling and publication ordering. It does **not** publish a release or access
signing/tap secrets. Do not push another PR revision merely to iterate a run.

The local equivalent must run on the test pod:

```sh
nocx-sky-test "$PWD" 'node --test --test-reporter=tap scripts/release/promotion*.test.mjs'
```

## Promote

1. Merge the nightly producer and this promotion implementation. Prepare the
   next stable's README and release notes **before** its nightly: the unique
   `**Status:**` line must name `` `vX.Y.Z` `` and link
   `docs/release-notes/vX.Y.Z.md` on that same line. `VERSION` must be `X.Y.Z`.
   Dispatch **Nightly** on `main` rather than waiting for its schedule. Wait for
   its verified publication to finish successfully.
2. Install and try that dated nightly. As the repository owner, dispatch
   **Release** on `main`, set `dry-run=false`, name its immutable
   `nightly-YYYYMMDD-RUNID` tag, and confirm `tried=true`. Never use the moving
   `nightly` alias. Promotion derives the version from the authenticated nightly;
   there is no arbitrary stable-version input and no rebuilding to change it.
3. Observe **Release** finish: provenance and PR-head CI green; original digests
   verified; stable manifest signed and checked against the candidate's keyring;
   draft upload downloaded and compared; stable published and downloaded again;
   stable cask updated from that exact zip hash. The evidence artifact contains
   `evidence.json` and `plan.json`. Review the generated next-`VERSION` PR, start
   CI on it (close/reopen as the owner, because `GITHUB_TOKEN` cannot trigger CI),
   then merge it before the next nightly. The workflow never merges it.

### What the CI gate accepts

Every first-parent commit since the last stable is checked:

- **A merged PR** needs its head commit's `ci.yml` run. Each required job is green, a
  supporting-only skip, or — under [ADR-xf4p9fzd](../decisions/xf4p9fzd-a-flaky-test-is-quarantined-by-its-open-bead.md)
  — red only on tests `docs/testing/quarantine.json` holds under an open bead (or a
  nameless failure that passed in another attempt, or a `job:<name>#<PR>` entry). A run
  is the PR's by `pull_requests`, or, once the branch is deleted and GitHub empties that
  list, by its head commit, branch and repository.
- **A direct push to `main`** (the owner may push directly) has no run of its own. It
  passes if it touches no product file, or if a later PR in the range ran the product
  suites fully green — nothing quarantined — on a head that contains it.

`.beads/issues.jsonl` is read at the candidate (about 14 MB), so the promotion's `git`
reads carry a 256 MiB buffer.

### GitHub and real-secret acceptance

Fixtures cannot establish Actions permissions or GitHub's live behavior. The
owner must see one complete real run after merging both pipeline PRs. In
particular:

- GitHub read APIs must resolve the dated tag, successful `nightly.yml` run,
  each first-parent commit since the latest stable to its merged `main` PR,
  that PR's **head** revision, its authoritative `ci.yml` run and complete jobs.
  List calls are paginated. Empty, failed, ambiguous and unavailable evidence
  refuses rather than silently approving a merge commit with zero checks.
- **Copy exact payload bytes and sign only the stable manifest** needs
  `RELEASE_SIGNING_KEY`: the existing base64 32-byte Ed25519 seed. No macOS P12
  or notarization secret is used by promotion. The derived signature must verify
  with `internal/update/keyring.go` read from the candidate SHA.
- **Upload draft…then publish stable** needs `github.token` contents write.
  Watch both fresh download checks compare every zip/dmg/AppImage/helper digest
  before and after publication. The only rewritten bytes are `manifest.json`
  and `manifest.json.sig`: URLs/notes now name stable, version and payload hashes
  do not change.
- **Update the stable Homebrew cask** needs `HOMEBREW_TAP_TOKEN`, permitted to
  push `Casks/nocx.rb` in `shady2k/homebrew-nocx`. Check the cask version and
  sha256 against `plan.json`, not against a fresh hash chosen after publication.
- **Propose the next nightly version** needs Actions to be allowed to create PRs
  with `github.token`. Enable that repository setting before the run. The owner
  must start CI and merge the normal bump PR; no direct push to `main` occurs.

### Refusals and partial failures

An untried/non-owner live dispatch fails the read-only authorization job. An
unverified nightly, mismatched SHA/run attempt, unsigned manifest, stale README
or changed input digest fails before any tag/release/tap write. A stale README
error names the required `vX.Y.Z` and notes path. A substituted signing key
fails before any write. A changed upload fails before making the draft stable.

There is no automatic retry that overwrites a tag or an asset. A fresh promotion
refuses an existing stable tag or release. This also makes a repeat click safe.
Promotions serialize under `stable-promotion`; the nightly tag remains unchanged.

If a tag/draft was created and a later upload/check failed, inspect it and the
saved plan. Delete only that **unpublished** draft and tag after checking that no
stable release was exposed, then dispatch again. Never retarget a published
stable tag or replace its assets to make a retry pass.

If publication succeeded but the tap push failed, stable is already public and
the job says so. The independent version-PR step still runs. Recover the cask
without rebuilding or republishing: download the saved evidence artifact and
all `plan.json` stable assets into `promotion/published/`, hash them against
`plan.assets` using `verifyFiles`, then run `node
scripts/release/promotion-github.mjs cask promotion tap/Casks/nocx.rb` and review
and push that cask delta with the tap token. If the version PR failed, verify
that same release and run the `bump` command with PR permissions (or prepare
and review the one-line `VERSION` patch by hand). Do not rerun live promotion
to repair a tap/PR: it deliberately refuses the existing stable tag.

## Producer contract

The nightly publishes `manifest.json`, its raw detached base64
`manifest.json.sig`, `nightly.json` and `nightly.json.sig` under the dated tag.
`nightly.json` is signed with the same release key and has:

```json
{
  "schema": 1,
  "sha": "full forty-character commit SHA",
  "version": "0.8.0",
  "tag": "nightly-20261027-42",
  "runId": 42,
  "runAttempt": 1,
  "artifacts": [{ "name": "asset basename", "sha256": "64 lower-case hex", "size": 123 }]
}
```

The inventory covers both manifest files, all seven public payloads (universal
zip and dmg, Linux amd64 AppImage, four helpers), and the producer's verification
reports. Promotion verifies **all** inventory files before copying the seven
payloads. Reports/provenance are retained on nightly rather than presented as
stable payloads. A successful independently queried `nightly.yml` run must match
that exact SHA, main branch, run id and run attempt. The candidate's own unsigned
claim of success never substitutes for that API query.

The existing CI classifier (`scripts/product-change.mjs`) owns product versus
supporting changes. Promotion imports `touchesProduct`; it does not keep a second
file-pattern list. Required job names/matrix values are read from `ci.yml` at
each PR head. Unsupported matrix syntax refuses until this reader is extended.
