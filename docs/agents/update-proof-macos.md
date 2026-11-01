# A published nightly replaces the installed macOS app

`nocx-1ebrs.35.5` owns this acceptance harness; `nocx-vh2hr.1.6` is the defect
it exists to name. It installs the previous published nightly's `.app`, lets
**that bundle's own updater** reach the newest published nightly, relaunches the
installed app, and asserts the four things the Linux proof asserts — verify,
swap, relaunch, healthy — through macOS's own platform code.

Where the Linux proof clicks: the window's own updater, driven by the bundle's
packaged diagnostic (`--apply-release-update`, `release_diagnostics.go`). A
hosted macOS runner grants no scripted Accessibility permission, and no TCC
entry can be added without a person at the machine, so the trigger a person uses
(Install in Settings) cannot be clicked by a driver. The diagnostic calls the
same `Check`/`Apply` the window's binding calls, from inside the installed
bundle: same placement, same compiled keyring, same transaction. Nothing is
mocked. The **relaunch** is the real one — the driver starts the installed app
afterwards, its renderer reports healthy, and finalisation certifies the pair
(`internal/update/pair.go`) exactly as after a person's restart.

## What it asserts, and what each assertion reads

1. **Install.** The previous nightly's signed zip, extracted with `ditto -x -k
--noqtn` (the product's own extraction), `codesign --verify --deep --strict`
   and `lipo -archs` (both slices) — the two checks
   `internal/update/platform_darwin.go`'s `VerifyExtracted` makes — then
   `--version` agreeing with the signed manifest.
2. **Verify.** The installed app's `--apply-release-update --channel nightly`
   fetches and verifies the candidate manifest with the bundle's own compiled
   keyring. An unsigned or wrong-key manifest fails here.
3. **Swap.** After the diagnostic returns, `Contents/MacOS/nocx` in the installed
   bundle hashes to the candidate zip's binary, `.nocx-backup.app` holds the
   previous nightly's bytes, `.nocx-swap.app` is gone, and the journal names the
   candidate.
4. **Relaunch and healthy.** The driver starts the installed bundle. The app's
   renderer reports healthy, the transaction's pair check certifies the window
   against the coordinator that answered it, and the journal is removed while the
   backup is retained. The driver waits for the journal's disappearance — the
   observable finalisation produces, never a duration.

## When it fails

The driver prints `FAILED STEP: <step>` and the updater's own log lines, and the
job uploads `macos.json` (the evidence), `app.log` (the relaunch) and the
application's `nocx.log`. The steps are named from the updater's vocabulary in
`internal/update`:

download the release manifest · signature verification (manifest) · artifact
selection · preflight (Applications not writable) · journal · download ·
extraction (ditto) · bundle seal (codesign/lipo) · bundle swap (renameatx_np) ·
pair check (mixed pair / probe / version) · pair check.

`scripts/update-proof/test_macos.py` holds that table against
`internal/update`'s own messages, so a reworded log line breaks a test instead
of quietly turning a named failure back into "the update failed".

## How it runs, and how it is triggered after merge

The job is `prove-macos` in `.github/workflows/nightly.yml`, after `publish`,
beside `prove-linux`. It never builds anything a promotion route needs: it is
the nightly workflow only, and `scripts/release`'s tests pin that promotion
builds nothing.

After merge, two ways to run it:

- **automatically**, on the next scheduled nightly that publishes (03:17 UTC)
  — it proves the pair that run published against the one before it;
- **on demand**, `workflow_dispatch` on the Nightly workflow with
  `proof-only: true`, which skips prepare/build/publish and proves the two
  newest published nightlies right now (the same input `prove-linux` uses).

`linux-only: true` (the dispatch default for iteration) skips the macOS build,
so it also skips this job — a nightly that never built the bundle cannot prove
one.

## What this cannot prove

A hosted runner is not the owner's Mac. It proves the packaged bundle, the
signed channel, and every step of the transaction on the shipping platform
(arm64 macOS), but not Gatekeeper's first-run treatment of a downloaded app and
not a person's own login session. Those stay with `nocx-1ebrs.4`'s real-use
pass, and `nocx-vh2hr.1.6`'s "the owner sees an installed nightly update itself"
is its own acceptance, not this job's.
