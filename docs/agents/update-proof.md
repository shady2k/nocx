# A published release replaces the installed Linux AppImage

`nocx-1ebrs.35.6` owns this acceptance harness. It does not build an updater or
add a diagnostic to the product. It runs the downloaded previous release's real
GTK/WebKit window, selects the channel through Settings, checks, installs and
presses **Restart now**. It then hashes the installed AppImage **after restart**.

The same driver proves both channels, chosen by `--channel` (default
`nightly`): the nightly pair, and since `nocx-86e2f` the previous stable
AppImage updating to the stable release the release workflow just promoted.
Only the channel's pair rules, the settings row it selects and the relay's
manifest location differ; the assertions and the code path are one.

## Inputs and trust

Read ADR-0097, ADR-0104 and ADR-0106 first. The producer contract was read from
PR #549 at `9adc42ef49815515db1fccaab8bf45da1dba3e34`; this harness does not need
that branch's `docs/agents/nightly.md` to exist in its checkout.

Supply these downloaded, unmodified release assets as file paths:

- Previous dated nightly: `manifest.json` and `manifest.json.sig`.
- Candidate dated nightly: the same two assets. The live moving `nightly` alias
  must serve exactly these bytes for both files. No publication is performed by
  this harness. A missing producer or a different alias fails with an explicit
  error, not an update timeout.
- A published lower-version stable manifest and signature. These are real signed
  downgrade probes, not a new manifest signed by a test key.
- `cmd/manifest-sign`, built from the checkout through the pod. Its verify mode
  uses the compiled production keyring. The shipped previous AppImage independently
  verifies every update check with its own compiled keyring.

Both nightlies must stamp plain next-stable versions, name different immutable
`nightly-YYYYMMDD-RUNID` tags, have increasing RFC3339 `released` instants, and
name different AppImage SHA-256 values. A higher-version successor is expected
after a stable promotion: a nightly installed at 0.8.0 must update to 0.8.1.
A lower version is always refused. At equal versions, the candidate must have a
different full commit and a strictly later stamped `build.builtAt`, matching
`newerNightly` in `internal/update/manifest.go:137`. A later publication alone
cannot turn an older build into an update. The equal-version pair still proves
the daily-channel case that a semver-only updater cannot pass.

The stable channel has none of that: `newerNightly` returns false for it, so the
candidate must be **strictly newer** and no build stamp is read. Its two
manifests must name `https://github.com/shady2k/nocx/releases/download/vX.Y.Z/`
artifacts and `releases/tag/vX.Y.Z` notes, each with the manifest's own version;
the promotion copies the tried nightly's bytes and rewrites only those URLs
(ADR-0097), so the tag is what proves these are the promoted bytes rather than
the nightly they came from.

## Run through the allowed test route

All commands below belong **inside** `nocx-sky-test --e2e`. Do not run them on the
shared host or through local Docker. Keep the default pod size. The e2e image has
Xvfb; the real desktop driver also needs distro Python's AT-SPI bindings. Install
`python3-pyatspi` and `at-spi2-core` in that pod if the recipe does not carry them.
Use `/usr/bin/python3`, not an unrelated Python virtual environment. The
disposable runner in the job described below is the one other place these
commands run.

Publication readiness can be queried separately. It prints `result: blocked`
with `no nightly published yet` (exit 2), distinct from a failed update assertion
(exit 1). This is never an acceptance pass:

```sh
flock /tmp/nocx-sky.lock -c 'nocx-sky-test --e2e "$PWD" /usr/bin/python3 scripts/update-proof/prove-linux.py --publication-status'
```

First run the harness's unit checks (not product acceptance):

```sh
flock /tmp/nocx-sky.lock -c 'nocx-sky-test --e2e "$PWD" /usr/bin/python3 -m unittest discover -s scripts/update-proof -p "test_*.py" -v'
```

Prepare the verifier on the pod:

```sh
go build -o /tmp/nocx-manifest-sign ./cmd/manifest-sign
```

Then, in the same pod command, with input files staged under `proof-inputs/`:

```sh
dbus-run-session -- xvfb-run -a /usr/bin/python3 scripts/update-proof/prove-linux.py \
  --previous-manifest proof-inputs/previous/manifest.json \
  --previous-signature proof-inputs/previous/manifest.json.sig \
  --candidate-manifest proof-inputs/candidate/manifest.json \
  --candidate-signature proof-inputs/candidate/manifest.json.sig \
  --lower-manifest proof-inputs/lower/manifest.json \
  --lower-signature proof-inputs/lower/manifest.json.sig \
  --verifier /tmp/nocx-manifest-sign \
  --work /tmp/nocx-nightly-proof \
  --appimage /tmp/nocx-nightly-proof/nocx.AppImage \
  --evidence proof-results/linux.json
```

The work directory and AppImage path must not already exist. The harness creates
its own HOME and all XDG directories there. It will never overwrite an existing
app or use the developer's profile. Fetch `proof-results/` through
`nocx-sky-test --fetch`; keep the printed command statuses with the result.
A failed upload without `NOCX_RUN_EXIT` means no test ran, not a failed assertion.

## What establishes a pass

1. The previous downloaded AppImage's digest and size match its signed manifest.
   Its runtime launches that file with `APPIMAGE_EXTRACT_AND_RUN=1` for pods
   without FUSE. **`$APPIMAGE` is the install target and the file hashed after
   restart, never the extracted binary or mount.** The extracted candidate is
   only an identity reference, never a writable substitute for the installation.
2. The lock lives under the disposable profile's data directory, not beside a
   mounted executable. Settings selects the proof's channel through the actual
   accessible controls. A lower-semver signed manifest and an unsigned signature
   produce no mutation; the unsigned check visibly fails signature
   verification.
3. A positive check uses the production location of the channel it proves —
   the moving nightly tag, or `releases/latest` for stable. The relay forwards
   real upstream bytes and refuses a candidate/location mismatch. The real
   Install control applies it. Before restart, the journal names the actual installed
   AppImage and candidate digest; the install holds candidate bytes and the swap
   peer holds previous bytes.
4. After the real Restart control, a different window PID executes the candidate
   embedded window bytes. The installed AppImage still hashes to the candidate.
   The journal disappears and the backup hashes to the previous AppImage, proving
   health finalization rather than only a completed download or a reported version.
   Coordinator identity is recorded separately: ADR-0104 explicitly permits a
   compatible same-Version/different-Commit coordinator to survive a nightly.
   A version increase must instead run the candidate coordinator.
5. An already-current check does not offer the same artifact again. Replaying
   the previous authentic published release must not offer a downgrade. This is
   an assertion against the product's ordering policy, not a harness-only
   `released` comparison.

The loopback HTTPS relay in `scripts/update-proof/transport.py` is **test tooling
only**, never shipped code. It can replace only the proved channel's manifest
and its signature for negative probes — the moving `nightly` tag for a nightly
proof, `releases/latest/download` for a stable one — so a check that used the
other channel asks a URL the relay does not intercept and the proof fails
rather than passing on the wrong channel. The real application, production
keyring, artifact URL and desktop controls stay unchanged. Positive checks use the actual published
channel. Temporary transport trust does not introduce a release signing key.

## The stable channel, and the release workflow runs it

`nocx-86e2f` needs no second driver: `--channel stable` is the same `run()`.
The pair is chosen by version, never by publication order — `stable_releases()`
in `prove-linux.py` sorts every non-prerelease, non-draft release tag by semver,
takes the newest as the candidate, the one below it as the previous, and the
one below that as the downgrade probe. Every such tag must be
`vMAJOR.MINOR.PATCH`; one that does not parse fails the proof closed, for the
reason ADR-79b6cy61 gives. Three releases are required, which is why this mode
cannot run before v0.8.1 is promoted: v0.8.0 is the first stable whose updater
the harness covers, so it has to be the _previous_ stable, v0.8.1 the
candidate, and a real stable below v0.8.0 the downgrade probe. Blocked is exit
2 and never a pass. To run it by hand, add `--channel stable --promoted
vX.Y.Z` to the command above, with the previous stable, the promoted stable and
a stable below the previous as the three staged pairs.

`.github/workflows/release.yml` carries it as the `prove-linux-stable` job with
`needs: promote`, so it starts only after the promotion that created the tag,
published the release and updated the cask has finished. It reads the version
this run promoted from `promotion/plan.json` in the promotion's own evidence
artifact, asks readiness for the three tags with
`--publication-status --channel stable --promoted vX.Y.Z`, refuses a newest
stable that is not the promoted tag, downloads the three signed pairs, builds
`cmd/manifest-sign` from main — the tree the promotion signed with — and runs
the driver under `dbus-run-session`/`xvfb-run`. Its evidence is uploaded as
`linux-stable-update-proof`. The host packages it needs are the
`linux-update-proof-runtime` composite action, shared with the nightly job so
the two cannot prove on different desktops.

Two consequences of it living in the `Release` workflow: a failed proof fails
the release run (the release stays published; the job's evidence names what
failed), and the run holds the `nocx-release` concurrency group until the proof
ends, so a nightly that fires meanwhile queues behind it rather than publishing
during it.

## The nightly workflow runs it

`.github/workflows/nightly.yml` carries the same recipe as a `prove-linux` job
that needs `publish`, so the pair it proves is the nightly this run published
and the one before it. It reads, and writes, only this:

- **The two dated nightlies** — `gh api repos/{owner}/{repo}/releases`, newest
  first by `published_at`, `nightly-` tags only, `prerelease` and not `draft`.
- **Their `manifest.json` and `.sig`** — `gh release download <dated tag>`.
- **The lower probe** — `releases/latest`, the newest non-prerelease release,
  refused unless its `version` is strictly below the nightly's.
- **The verifier** — `go build ./cmd/manifest-sign` in the checkout that
  published.
- **Both AppImages** — the signed manifest's own `linux/amd64` URL, fetched by
  the harness.
- **Evidence** — `proof-results/{linux.json,app.log,inputs.txt}`, uploaded as
  `linux-update-proof`.

The job fails when `--publication-status` reports blocked (fewer than two dated
nightlies exist): blocked is not a pass, and the annotation says so. It also
refuses to run when the newest dated nightly is not the tag this run published,
so a pass cannot belong to another run's bytes. `workflow_dispatch` with
`proof-only: true` runs this job alone against whatever is published now and
skips `prepare`, `build` and `publish`.

The driver reads the accessibility tree, so a control it must find has to be
announced. The settings rows bind each control to the row's `<label>` through
`Field for` and the control's own `id` (`frontend/src/settings.tsx`); a select
with neither an `id` nor an `aria-label` is announced unnamed, and
`named("Update channel", "combo box")` then times out — which is how this proof
found that the Update-channel row's label pointed at the row instead of at the
control.

Announced is not the same as reachable, and listed is not the same as chosen.
The rail holds more rows than the default 1024x768 window shows, so its last
rows are in the tree, `FOCUSABLE`, and not `SHOWING`; `rail_click` asks the
engine to focus the row first, which is what Tab does and which scrolls the
rail to it (measured on WebKitGTK 2.52), and refuses with the rail's own
reading if that does not put the row on screen. A `<select>` exposes every
option as a menu item at all times, so a row named Nightly proves nothing; the
channel is the row the combo marks `SELECTED`, which is what
`channel_settled` waits for. Both rules live in `controls.py`, with unit
checks that need no display, so a future change to them fails somewhere other
than a 20-minute nightly job.

## Shared macOS evidence collector

`common.py` is consumed by the macOS proof. It contains no UI driver or updater:

```sh
python3 scripts/update-proof/common.py prepare \
  --manifest manifest.json --signature manifest.json.sig \
  --verifier /path/to/manifest-sign --platform darwin --arch universal \
  --download /path/to/nightly.zip --evidence prepared.json
```

It writes and prints `{manifest, artifact, manifestSHA256, artifactPath}`. `manifest`
is the verified signed object; `artifact` is its selected `{os,arch,format,url,
sha256,size}` entry; `artifactPath` is absolute. Signature verification happens
before JSON parsing. A corrupt/short download never replaces an existing file.

## Acceptance blockers are not skipped tests

There were no published nightlies when this harness was written; PR #549 was
still open. Two real publications, including the read-only AppImage placement fix,
are required. `nocx-1ebrs.66.1.8` owns same-version comparison and monotonic ordering;
the baseline `Check` is semver-only, so this proof must fail until it lands and is
published. `nocx-1ebrs.66.1.9` owns the stale install-only text on Linux. The health
policy question for same-version builds is recorded on `nocx-1ebrs.66.1.10`;
ADR-0104's accepted coordinator reuse rule is not silently changed by this test.
A green unit test suite is not a green published-AppImage update proof.
