# Built-artifact publication gate

`nocx-ngw57` owns this check. Run it on the target OS after packaging and before
uploading the artifact. It needs Python 3, the checkout declarations, and network
access to the real stable release manifest. It needs no signing secret.

```sh
python3 scripts/verify-built-artifact.py \
  --artifact "dist/nocx-${VERSION}-darwin-universal.zip" \
  --version "$VERSION" --commit "$GITHUB_SHA" --report dist/verified-darwin.json
python3 scripts/verify-built-artifact.py \
  --artifact "dist/nocx-${VERSION}-linux-amd64.AppImage" \
  --version "$VERSION" --commit "$GITHUB_SHA" --report dist/verified-linux.json
```

Use the existing release's actual filename if it differs. Do not pass an unpacked
build directory. The gate extracts the archive itself. macOS uses `ditto` to
preserve bundle symlinks and signatures. Linux uses the AppImage's extract mode
and runs its extracted `AppRun`, so its bundled library setup is used.

The gate runs `--build-info` on the packaged desktop and coordinator executables.
They return the `version.Info()` descriptor that supplies the About page. The
gate checks every field and compares both descriptors. It reads the assembled
macOS `Info.plist`, not the build-assets arguments. Expected values come from the
committed plist identifier, LICENSE owner, main's description, go.mod Wails pin,
and the workflow's expected version and SHA. A missing, stale, or template field
fails with its name. Read and parse failures name the archive and the exact
input path (or executable command), byte count, first 64 bytes, and parser
reason. Missing inputs explicitly say that byte evidence is unavailable.

Wails beta.9 emits valid XML plists starting with `<!DOCTYPE plist`, without
an XML declaration. The gate selects XML parsing for that prefix because
`plistlib` auto-detection does not recognize it. It parses the original bytes;
it does not normalize the package or bypass any identity check. Binary plists
remain supported. Malformed XML and binary plists still fail publication.

`--verify-release-manifest` runs before any desktop, profile, daemon, or keystore
starts. It calls the **same updater construction** as `ServiceStartup`, then calls
`Updater.Check`. It neither reconciles nor applies an update. The production HTTP
client, stable endpoint, and compiled release keyring remain unchanged. It returns
the exact fetched manifest and signature only after verification. Even an older
or equal manifest must verify. Network failure, invalid signature, a nil HTTP
client panic, or a development build fails the gate. The stable endpoint is used
because an initial nightly does not yet exist before publication.

## Evidence

On success the report is JSON with schema version 1:

- `archive.name` and `archive.sha256` identify the exact published archive.
- `version` and `commit` record workflow expectations.
- `declaredIdentity` and `identity` record expected and actual bundle fields.
  Linux has no Info.plist and its actual identity object is empty.
- `executables` records SHA-256 and actual About descriptors for both programs.
- `updater` records the fetched verified manifest, its SHA-256, detached signature,
  and production source URL.
- `limitations` records what this gate did not establish.

A failure removes any prior report. Publication must hash its input archives and
compare those hashes with the reports. Passing reports for a different archive
must not authorize publication. The workflow must not continue on a gate failure.

## Deliberate limits

This is not a GUI smoke test. It does not prove the transport's `app.about`
construction, only its shared build descriptor in each actual packaged executable.
The owner's platform pass remains required. A universal macOS archive executes the
runner's native slice; it does not execute its other architecture's slice.

The local agent-token regression requires a live coordinator, helper, and shell
launch. A metadata diagnostic cannot establish token propagation. No success
boolean is reported for it. This remains an acceptance limitation of
`nocx-ngw57`.

Landing pages are separate deployment artifacts and are not in the desktop
archive. Their marker gate is `scripts/check-site.sh`, called by the site
pipeline. This desktop gate does not claim to inspect the deployed site.

## Unit checks

Use the worktree's normal `nocx-sky-test` route, with its default resources:

```sh
flock /tmp/nocx-sky.lock -c 'nocx-sky-test "$PWD" "python3 scripts/verify_built_artifact_test.py"'
flock /tmp/nocx-sky.lock -c 'nocx-sky-test "$PWD" "go test -tags gtk3 -run TestReleaseDiagnostics . && go test -tags gtk3 -run TestWindowUpdater ."'
```

The Python checks package executable fixtures into actual zip archives, mutate
About fields, identity, and manifest failures, and assert publication refusal.
`scripts/testdata/wails-beta.9-Info.plist` is the unmodified output of the pinned
`wails3 generate build-assets` command and identity flags in `release-build.yml`
for version `0.8.0` (1097 bytes, SHA-256
`ae274dd09516a36c85e8131679738ac988d8b228cd85d1622ff05d027644e3a1`). It exercises
the real declaration-free XML rather than a plistlib-generated approximation.
They establish the checker, not a real signed application build. The real macOS
zip and AppImage checks must run once on GitHub before acceptance.
