# A published nightly replaces the installed Linux AppImage

`nocx-1ebrs.35.6` owns this acceptance harness. It does not build an updater or
add a diagnostic to the product. It runs the downloaded previous nightly's real
GTK/WebKit window, selects nightly through Settings, checks, installs and presses
**Restart now**. It then hashes the installed AppImage **after restart**.

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

Both nightlies must stamp the same plain next-stable version, name different
immutable `nightly-YYYYMMDD-RUNID` tags, have increasing RFC3339 `released` instants,
and name different AppImage SHA-256 values. This forces the daily-channel case;
a semver-only updater cannot pass by testing a version bump instead.

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
   mounted executable. Settings selects nightly through the actual accessible
   controls. A lower-semver signed manifest and an unsigned signature produce no
   mutation; the unsigned check visibly fails signature verification.
3. A positive check uses the production moving nightly URL. The relay forwards
   real upstream bytes and refuses a candidate/alias mismatch. The real Install
   control applies it. Before restart, the journal names the actual installed
   AppImage and candidate digest; the install holds candidate bytes and the swap
   peer holds previous bytes.
4. After the real Restart control, a different window PID executes the candidate
   embedded window bytes. The installed AppImage still hashes to the candidate.
   The journal disappears and the backup hashes to the previous AppImage, proving
   health finalization rather than only a completed download or a reported version.
   Coordinator identity is recorded separately: ADR-0104 explicitly permits a
   compatible same-Version/different-Commit coordinator to survive a nightly.
5. An already-current check does not offer the same artifact again. Replaying the
   previous authentic nightly must not offer a downgrade. This is an assertion
   against the product's ordering policy, not a harness-only `released` comparison.

The loopback HTTPS relay in `scripts/update-proof/transport.py` is **test tooling
only**, never shipped code. It can replace only the nightly manifest and its
signature for negative probes. The real application, production keyring, artifact
URL and desktop controls stay unchanged. Positive checks use the actual published
channel. Temporary transport trust does not introduce a release signing key.

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
