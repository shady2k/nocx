# Prove the macOS installed nightly update

`nocx-1ebrs.35.5` owns the macOS evidence. The shared manifest/download code
and the executable Linux proof belong to `nocx-1ebrs.35.6` in
`scripts/update-proof/common.py` and `prove-linux.py`. Integrate that change
before running `prove-macos.py`; this branch intentionally does not copy its
code. Both PRs must name this dependency. Neither proof replaces updater calls.

## Acceptance is not a mocked browser test

The macOS driver installs the previous **published** nightly ZIP, starts that
installed app, chooses nightly through the real Settings control, waits for the
real producer to publish the next nightly, then presses **Check for updates**,
**Install now**, and **Restart now** through System Events Accessibility.
It never injects JavaScript, replaces a Wails binding, calls an updater double,
changes the app's keyring, or changes the manifest base URL.

The publication contract was read from `docs/agents/nightly.md` on producer PR
[#549](https://github.com/shady2k/nocx/pull/549), revision
`9adc42ef49815515db1fccaab8bf45da1dba3e34`. The harness does not require that
file to exist on its branch. It consumes manifest paths and detached signatures.
The manifest has no channel, tag or commit field. Its Darwin entry is
`darwin/universal/zip`; the URL names an immutable dated nightly, and its
`notesUrl` names that same tag. `version` is the next stable semver, not a
nightly suffix. Promotion republishes these bytes, not a rebuild (ADR-0097).

The shared preparation command verifies both manifests with the **compiled**
`cmd/manifest-sign` release keyring before parsing and verifies the ZIP size and
SHA-256 against that signed document. On the Mac the driver also verifies
`codesign --verify --deep --strict` and both universal slices. It obtains the
version **and commit** from each authenticated ZIP's actual executable with
`--version`. The app independently verifies the live channel signature with its
own compiled keyring. The input candidate must byte-match the live moving
`nightly/manifest.json` and signature; no locally signed fixture is accepted as
publication evidence.

A successful run requires two same-semver nightlies with different ZIP bytes
and different linked commits. After install, the driver checks the transaction
journal's target, version and artifact digest, installed executable bytes,
rollback peer, replacement window PID/path, and **About → Copy diagnostics**
version **and commit** from the answering coordinator. Only then may the journal
disappear and the retained backup replace the swap peer. Journal absence alone
is not proof: health certification currently compares version only, so an older
same-version coordinator can otherwise appear healthy.

## What Linux can and cannot prove

In the Linux e2e image, through `nocx-sky-test` at its default size:

| Check                                   | What it establishes                                                                                                                                                                  |
| --------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Shared preparation of the Darwin ZIP    | The real signed manifest authenticates, its universal ZIP entry is selected, and downloaded ZIP bytes match the signed size/digest. **Not** a Mac installation.                      |
| Real Linux AppImage install and startup | A published AppImage starts from its real install target, not a rebuilt headless backend.                                                                                            |
| Settings and real update check          | Stable is the unset default, explicit nightly selects its channel, signature failures surface, and the real updater makes the update decision.                                       |
| Real AppImage exchange/restart/health   | Possible after merged `nocx-azxe.8`; `.35.6` owns execution and evidence. The old install-only warning is stale, tracked by `.66.1.9`. This is **not** evidence for Darwin exchange. |
| `test_macos.py`                         | Offline assertions for this driver's evidence checks only. No installed app, keyring or platform update is exercised by those unit tests.                                            |

Only the owner's Mac can prove `ditto` installation, the project certificate and
universal executable after extraction on macOS, WKWebView Accessibility actions,
`renameatx_np(RENAME_SWAP)`, Launch Services relaunch, the running window and
coordinator pair, and real macOS `ReportHealthy` finalisation. No Linux pass may
be reported as the macOS DONE WHEN. The owner records that pass under
`nocx-1ebrs.4` and attaches `macos-evidence.json` to this task's acceptance.

## Run on the owner's Mac, after the producer is on main

Do not publish a nightly by hand from an unmerged workflow. Publication remains
the producer's operation, not a side effect of running this driver.

Use a **disposable graphical macOS login** with no existing `nocx` profile,
window, coordinator, app, swap or backup. The script refuses an existing install
in either Applications directory and never removes it. The updater itself picks
`/Applications/nocx.app` when writable, otherwise `~/Applications/nocx.app`;
the driver uses that same order. A disposable `$HOME` alone does not isolate the
macOS keychain, launch services or a running coordinator. Do not substitute it
for a disposable login. Grant Accessibility/Automation to the terminal running
`osascript`. Keep the desktop unlocked. The test leaves the app, profile, backup
and evidence in that login for inspection; it does not clean up user data.

Obtain the existing release tool `manifest-sign`, compiled from the reviewed
release tree using the project's build environment (tests/build checks here
run only through `nocx-sky-test`). Do not supply a verifier with a test keyring.
Provide the previous immutable nightly's `manifest.json` and signature. Ensure
the moving channel still serves it before starting. The candidate paths may
not exist yet; copy the real producer outputs to them atomically after publish.
Use absolute paths for all inputs and a new work directory:

```bash
python3 scripts/update-proof/prove-macos.py \
  --previous-manifest /tmp/nightly-N/manifest.json \
  --previous-signature /tmp/nightly-N/manifest.json.sig \
  --manifest /tmp/nightly-N1/manifest.json \
  --signature /tmp/nightly-N1/manifest.json.sig \
  --verifier /path/to/compiled/manifest-sign \
  --work-dir /tmp/nocx-macos-nightly-proof-001 \
  --consent-disposable-login
```

Wait for `READY_FOR_PUBLICATION`. The old installed build is then running on
nightly. Let the workflow on **main** publish N+1 after a real product change;
transfer its manifest and detached signature to the candidate paths. The driver
waits up to one hour for those files and for the production alias to match,
then drives the real update. Waits observe state, not an elapsed success timer.
A temporary 404 while the moving alias is replaced means publication is not
ready; authentication/TLS/server errors fail the run. A UI state that never
appears fails with a bounded timeout, not a retry around the test.

The `--preset unavailable` default is deliberate: `.66.1.7` is still the
cask/installation preset prerequisite. It asserts the plain artifact begins on
**Stable**, records the preset as **SKIPPED**, and opts into nightly in the UI.
This is partial channel evidence, **not** stable-cask or nightly-cask acceptance.
`--preset nightly` asserts an installed artifact's first-launch nightly preset;
it does not fabricate a cask postflight or claim to test it. If the final seam
is cask-owned rather than artifact-owned, add a separate real cask install pass
before using that mode. A skipped preset never satisfies the complete task.

The stable-cask default and promoted stable's byte identity remain separate
acceptance evidence owned by the cask/promotion PRs. This driver does not infer
either from `--version`, a Ruby string, or a signed nightly alone. Record the
real cask first-launch channel and compare promoted asset sizes/digests to the
signed nightly provenance (`nightly.json`) before accepting those clauses.

## Known prerequisites and reporting

- PR #549 must be merged and publish real nightlies. The harness does not
  dispatch it or weaken its main-only publication policy.
- `.66.1.8` must implement the same-version/different-bytes decision and ordering.
  Current `transaction.Check` returns no update for equal semver. That must
  fail this real-app proof; the driver must not manufacture a higher version.
- `.66.1.7` owns the cask preset, and `.66.1.9` owns the stale Linux warning.
- `.35.6` supplies the shared code; `.35.3` owns promotion byte-identity evidence,
  and `.35.4` owns cask evidence. None is silently counted as a pass here.

Keep the command's own exit status. `macos-evidence.json` records each child
command's exit, facts observed, and a failure reason; a failed or interrupted
run is not `updated`. If SkyPilot reports HTTP 507 and exit **3** with no
`NOCX_RUN_EXIT`, **nothing ran**. Do not fall back to a local test or Docker.
