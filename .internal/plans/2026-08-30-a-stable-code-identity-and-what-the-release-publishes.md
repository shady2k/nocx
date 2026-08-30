# A stable code identity, and what the release publishes — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** The macOS release signs with a project certificate whose designated requirement is identical in every build, and the release builds and publishes the remote helper it currently ships without.

**Architecture:** Everything lives in `.github/workflows/release.yml`, plus one committed requirement string (`build/darwin/requirement.txt`), one certificate-generation script, a four-target helper matrix, and the documentation that records the amendment. No product code changes except one test-gate helper and one comment whose text became false.

**Tech Stack:** GitHub Actions (`macos-15`, `ubuntu-22.04`, `ubuntu-latest`), `codesign(1)`, `security(1)`, `openssl`, Go 1.26, `jq`, `gh`.

**Spec:** `.internal/specs/2026-08-30-a-stable-code-identity-and-what-the-release-publishes-design.md`

## Global Constraints

- **No Developer ID, no notarization, no hardened runtime.** ADR-0003 stands on publisher identity; this work adds code identity only.
- **The certificate is self-signed, `CN=nocx Project Code Signing`, valid 7300 days**, EKU `codeSigning` (spec D1).
- **`--deep` is never used to SIGN.** It stays only in `--verify --deep --strict` (spec D3).
- **The bundle signature is the last thing that touches the tree.** Any copy after it invalidates it.
- **The dry run signs.** `workflow_dispatch` must exercise every signing and checking step and publish nothing (spec D6).
- **Two secrets, and they are distinct from `RELEASE_SIGNING_KEY`:** `MACOS_SIGNING_P12`, `MACOS_SIGNING_P12_PASSWORD`.
- **Helper matrix is 2x2:** `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`.
- **Every commit names its bead** and follows AGENTS.md's message shape.
- A worker on this plan runs the unit tests for what it changed and stops there. The containerized suites and `make ci-full` belong to whoever integrates.

---

### Task 1: The certificate, and a script that can make another one

**Files:**

- Create: `scripts/darwin/create-signing-certificate.sh`
- Create: `docs/decisions/` — nothing yet (Task 9)

**Interfaces:**

- Produces: a `.p12` whose base64 goes into `MACOS_SIGNING_P12`, its password into `MACOS_SIGNING_P12_PASSWORD`. Nothing in the repository ever holds either.

**Acceptance Criteria:**

- The script runs on macOS and Linux, needs only `openssl`, and prints the base64 of the `.p12` on stdout and nothing else on stdout.
- It refuses to overwrite an existing key file.
- It never writes the key or the `.p12` anywhere but the directory it is told to use, and it prints where.

- [ ] **Step 1: Write the script**

```bash
#!/usr/bin/env bash
# Generate the project's code-signing certificate (spec D1).
#
# The signature exists to give nocx-server a designated requirement the
# keychain recognises across releases (ADR-0050 step 3) — NOT to satisfy
# Gatekeeper, which needs a Developer ID we do not have (ADR-0003). The
# certificate is therefore self-signed and long-lived: a renewal is an
# IDENTITY CHANGE, so it is a decision, not a chore (spec D8).
#
# Output: base64 of the .p12 on stdout, for the MACOS_SIGNING_P12 secret.
# The private key and the .p12 stay in $OUT_DIR; back them up offline,
# beside RELEASE_SIGNING_KEY, and delete them from the machine afterwards.
set -euo pipefail

OUT_DIR="${1:-./signing}"
DAYS=7300 # 20 years

mkdir -p "$OUT_DIR"
key="$OUT_DIR/nocx-signing.key"
crt="$OUT_DIR/nocx-signing.crt"
p12="$OUT_DIR/nocx-signing.p12"

if [ -e "$key" ]; then
  echo "refusing to overwrite $key — an existing key is an existing identity" >&2
  exit 1
fi

pass="$(openssl rand -base64 24)"

# codeSigning EKU is what makes `security find-identity -p codesigning`
# list it; without it the certificate imports and codesign never sees it.
openssl req -x509 -newkey rsa:4096 -sha256 -days "$DAYS" -nodes \
  -keyout "$key" -out "$crt" \
  -subj "/CN=nocx Project Code Signing/O=nocx" \
  -addext "basicConstraints=critical,CA:FALSE" \
  -addext "keyUsage=critical,digitalSignature" \
  -addext "extendedKeyUsage=critical,codeSigning" >/dev/null 2>&1

openssl pkcs12 -export -inkey "$key" -in "$crt" -out "$p12" \
  -name "nocx Project Code Signing" -passout "pass:$pass"

chmod 600 "$key" "$p12"

echo "certificate: $crt" >&2
echo "private key: $key  (back this up offline; losing it is losing the identity)" >&2
echo "password for MACOS_SIGNING_P12_PASSWORD: $pass" >&2
echo "--- base64 of the .p12 for MACOS_SIGNING_P12 ---" >&2
base64 < "$p12"
```

- [ ] **Step 2: Run it and check the certificate is what codesign will accept**

Run:

```bash
chmod +x scripts/darwin/create-signing-certificate.sh
./scripts/darwin/create-signing-certificate.sh /tmp/nocx-signing >/tmp/p12.b64
openssl x509 -in /tmp/nocx-signing/nocx-signing.crt -noout -text | grep -A1 'Extended Key Usage'
```

Expected: `Code Signing` under Extended Key Usage. `/tmp/p12.b64` is non-empty base64.

- [ ] **Step 3: Verify the refusal path**

Run: `./scripts/darwin/create-signing-certificate.sh /tmp/nocx-signing; echo "exit=$?"`
Expected: `refusing to overwrite …` on stderr and `exit=1`.

- [ ] **Step 4: Clean the temporary material off the machine**

Run: `rm -rf /tmp/nocx-signing /tmp/p12.b64`

- [ ] **Step 5: Commit**

```bash
git add scripts/darwin/create-signing-certificate.sh
git commit -m "build(release): a script that mints the project code-signing certificate (<bead>)"
```

- [ ] **Step 6: Hand the secrets to the owner**

The owner runs the script once, stores the key offline, and sets `MACOS_SIGNING_P12` and `MACOS_SIGNING_P12_PASSWORD` as repository secrets. **This task is not done until both secrets exist** — Task 2 fails without them, deliberately.

---

### Task 2: build-macos signs with the certificate

**Files:**

- Modify: `.github/workflows/release.yml:83-236` (the `build-macos` job: a new import step before assembly, the signing lines at the end of assembly, a cleanup step)

**Interfaces:**

- Consumes: `MACOS_SIGNING_P12`, `MACOS_SIGNING_P12_PASSWORD` (Task 1).
- Produces: `$SIGN_IDENTITY` in `GITHUB_ENV` — the SHA-1 of the imported identity, which later steps pass to `codesign --sign`.

**Acceptance Criteria:**

- A `workflow_dispatch` run signs the bundle and `nocx-server` with the certificate and succeeds.
- `codesign -dv nocx.app` reports `Authority=nocx Project Code Signing`, not `Signature=adhoc`.
- The temporary keychain is deleted even when the job fails.
- An absent or malformed secret fails the job at the import step with a named error, never a silent fall back to ad-hoc.

- [ ] **Step 1: Add the import step, immediately before "Build and assemble the .app"**

```yaml
# The code-signing key is present in a job that runs build tooling, and
# unlike the manifest key (§5, the `sign` job) it cannot be anywhere
# else: codesign works on the assembled bundle, on macOS, in the job
# that assembles it. The two keys stay distinct, so the manifest key's
# isolation is exactly as strong as it was.
- name: Import the signing certificate
  env:
    MACOS_SIGNING_P12: ${{ secrets.MACOS_SIGNING_P12 }}
    MACOS_SIGNING_P12_PASSWORD: ${{ secrets.MACOS_SIGNING_P12_PASSWORD }}
  run: |
    set -euo pipefail
    [ -n "$MACOS_SIGNING_P12" ] || { echo "::error::MACOS_SIGNING_P12 is empty — the release cannot sign, and it must not fall back to ad-hoc"; exit 1; }
    [ -n "$MACOS_SIGNING_P12_PASSWORD" ] || { echo "::error::MACOS_SIGNING_P12_PASSWORD is empty"; exit 1; }
    kc_pass="$(openssl rand -hex 32)"
    echo "::add-mask::$kc_pass"
    security create-keychain -p "$kc_pass" build.keychain
    # -lut: no auto-lock during the job. A keychain that relocks
    # mid-run makes codesign prompt, and a prompt on a runner is a
    # six-hour hang rather than a failure.
    security set-keychain-settings -lut 21600 build.keychain
    security unlock-keychain -p "$kc_pass" build.keychain
    printf '%s' "$MACOS_SIGNING_P12" | base64 --decode > "$RUNNER_TEMP/signing.p12"
    security import "$RUNNER_TEMP/signing.p12" -k build.keychain \
      -P "$MACOS_SIGNING_P12_PASSWORD" -T /usr/bin/codesign -f pkcs12
    rm -f "$RUNNER_TEMP/signing.p12"
    # WITHOUT THIS LINE codesign asks for the keychain password
    # interactively and the job hangs instead of failing. -T on the
    # import grants codesign the ACL entry; the partition list is the
    # second half of the same permission on modern macOS.
    security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$kc_pass" build.keychain >/dev/null
    security list-keychains -d user -s build.keychain login.keychain
    id="$(security find-identity -v -p codesigning build.keychain | awk 'NR==1 {print $2}')"
    [ -n "$id" ] || { echo "::error::no code-signing identity in the imported keychain"; exit 1; }
    echo "SIGN_IDENTITY=$id" >> "$GITHUB_ENV"
    security find-identity -v -p codesigning build.keychain
```

- [ ] **Step 2: Replace the ad-hoc signing line at the end of the assembly step**

Replace `codesign --force --deep --sign - build/bin/nocx.app` (release.yml:235) and the paragraph above it about `--deep` with:

```bash
          # Sign inside-out. --deep is NOT used to sign: Apple deprecated it
          # for signing because it applies the outer options to nested code,
          # and here it would sign the coordinator as a by-product of signing
          # the app rather than as its own decision. It survives only in
          # --verify --deep --strict below, where it means "check nested code
          # too".
          #
          # -i on the coordinator because a bare Mach-O takes its identifier
          # from its FILE NAME, and the identifier is part of the designated
          # requirement the keychain matches on (ADR-0050 step 3). A
          # defaulted identifier is a requirement that can change without
          # anyone deciding to change it. The bundle needs no -i: it takes
          # CFBundleIdentifier from Info.plist.
          #
          # Order matters and the bundle is last: its signature seals
          # Info.plist, the icon and both executables, so anything that
          # touches the tree afterwards invalidates it.
          codesign --force --sign "$SIGN_IDENTITY" -i com.nocx.server \
            build/bin/nocx.app/Contents/MacOS/nocx-server
          codesign --force --sign "$SIGN_IDENTITY" build/bin/nocx.app
```

- [ ] **Step 3: Add the cleanup step at the end of the job**

```yaml
# The keychain outlives the job's success or failure, so its removal
# must too. A hosted runner is discarded anyway; a self-hosted one is
# where this line earns its keep.
- name: Remove the signing keychain
  if: always()
  run: security delete-keychain build.keychain || true
```

- [ ] **Step 4: Update the "Smoke checks" comment that names the ad-hoc signature**

In the smoke-check step, check 4's comment currently reads "The ad-hoc signature must be intact … Applied by the signing command at the end of the assembly step above; under v2 the wails CLI applied it". Replace with:

```bash
          # 4. The signature must be intact — the baseline the payload round
          #    trip (§9) asserts is preserved through packaging. It is the
          #    project certificate's, not the ad-hoc one Wails used to apply:
          #    what changed is the signer, not the check.
```

- [ ] **Step 5: Run the dry run**

Run: `gh workflow run release.yml --ref <branch>` then `gh run watch`
Expected: green. In the import step's log, one identity listed. In the smoke checks, `codesign --verify --deep --strict` passes.

- [ ] **Step 6: Read the actual authority out of the run**

Add temporarily to the smoke-check step, or read from the run log: `codesign -dv build/bin/nocx.app 2>&1 | grep Authority`
Expected: `Authority=nocx Project Code Signing`. If it says `Signature=adhoc`, the identity was empty and Step 1's guard failed to catch it — fix the guard, not the symptom.

- [ ] **Step 7: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "build(release): sign the macOS bundle with the project certificate, not ad-hoc (<bead>)"
```

---

### Task 3: The requirement is committed, and every build is compared against it

**Files:**

- Create: `build/darwin/requirement.txt`
- Modify: `.github/workflows/release.yml` (smoke checks in `build-macos`)

**Interfaces:**

- Consumes: `$SIGN_IDENTITY` and the signed bundle (Task 2).
- Produces: `build/darwin/requirement.txt` — the single source of truth for the requirement string. `nocx-rybbm`'s keychain provider reads this file rather than minting a second copy.

**Acceptance Criteria:**

- The file holds the exact `designated =>` string `codesign` derives for `nocx-server`, taken from a real run, not written by hand.
- The macOS job fails when a build's requirement differs from the file, and the failure message names both strings.

- [ ] **Step 1: Get the real string out of the Task 2 dry run**

Add this step after the smoke checks, temporarily, and run the workflow:

```yaml
- name: Print the designated requirement
  run: codesign -d -r- "build/bin/nocx.app/Contents/MacOS/nocx-server" 2>/dev/null
```

Expected output shape (the hash will differ):
`designated => identifier "com.nocx.server" and certificate leaf = H"a1b2…"`

- [ ] **Step 2: Commit the string exactly as printed**

```bash
printf 'designated => identifier "com.nocx.server" and certificate leaf = H"<hash from the run>"\n' > build/darwin/requirement.txt
```

- [ ] **Step 3: Replace the temporary print step with the check**

```yaml
- name: The designated requirement is the one we committed
  run: |
    set -euo pipefail
    # This string is what the keychain matches a process against
    # (ADR-0050 step 3). Committing it and comparing every build
    # against it is what turns "stable across releases" from a claim
    # into a gate: replace, rotate or mis-import the certificate and
    # this step is red, instead of the release quietly shipping an
    # identity no existing install recognises.
    want="$(cat build/darwin/requirement.txt)"
    got="$(codesign -d -r- "build/bin/nocx.app/Contents/MacOS/nocx-server" 2>/dev/null)"
    if [ "$got" != "$want" ]; then
      echo "::error::designated requirement changed"
      echo "committed: $want"
      echo "built:     $got"
      exit 1
    fi
    echo "requirement matches: $got"
```

- [ ] **Step 4: Run the dry run again**

Run: `gh workflow run release.yml --ref <branch>` then `gh run watch`
Expected: the step prints `requirement matches: designated => identifier "com.nocx.server" …`.

- [ ] **Step 5: Prove the check can fail**

Locally (or by a one-off run), change one character in `build/darwin/requirement.txt`, push, and confirm the job goes red naming both strings. Revert.

- [ ] **Step 6: Commit**

```bash
git add build/darwin/requirement.txt .github/workflows/release.yml
git commit -m "build(release): commit the designated requirement and compare every build against it (<bead>)"
```

---

### Task 4: The requirement survives a rebuild

**Files:**

- Modify: `.github/workflows/release.yml` (a step after Task 3's check)

**Interfaces:**

- Consumes: `$SIGN_IDENTITY`, `$LDFLAGS` (rebuilt locally in the step).

**Acceptance Criteria:**

- The step builds `cmd/nocx-server` twice with different `-ldflags`, signs both, and asserts their `cdhash` differs while their requirement is identical.
- A failure names which of the two properties broke.

- [ ] **Step 1: Add the step**

```yaml
- name: The requirement survives a rebuild, and the code hash does not
  run: |
    set -euo pipefail
    # ADR-0050 recorded this property from one measurement on a
    # laptop. It is the property the entire design rests on, so it is
    # asserted on the artefact, every build: an ad-hoc signature's
    # requirement IS the code hash, and the whole point of the
    # certificate is that ours is not.
    LD="-X github.com/shady2k/nocx/internal/version.Version=${VERSION}"
    for stamp in A B; do
      CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -tags release \
        -ldflags "$LD -X github.com/shady2k/nocx/internal/version.Date=stamp-$stamp" \
        -o "$RUNNER_TEMP/srv-$stamp" ./cmd/nocx-server
      codesign --force --sign "$SIGN_IDENTITY" -i com.nocx.server "$RUNNER_TEMP/srv-$stamp"
    done
    req_a="$(codesign -d -r- "$RUNNER_TEMP/srv-A" 2>/dev/null)"
    req_b="$(codesign -d -r- "$RUNNER_TEMP/srv-B" 2>/dev/null)"
    cd_a="$(codesign -d --verbose=4 "$RUNNER_TEMP/srv-A" 2>&1 | sed -n 's/^CDHash=//p')"
    cd_b="$(codesign -d --verbose=4 "$RUNNER_TEMP/srv-B" 2>&1 | sed -n 's/^CDHash=//p')"
    [ "$req_a" = "$req_b" ] || { echo "::error::requirement changed between two builds:"; echo "$req_a"; echo "$req_b"; exit 1; }
    [ "$cd_a" != "$cd_b" ] || { echo "::error::the two builds have the same cdhash ($cd_a) — the check proves nothing as written"; exit 1; }
    echo "requirement stable across a rebuild; cdhash moved from $cd_a to $cd_b"
```

- [ ] **Step 2: Run the dry run**

Run: `gh workflow run release.yml --ref <branch>` then `gh run watch`
Expected: `requirement stable across a rebuild; cdhash moved from … to …`.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "build(release): assert the requirement outlives a rebuild while the code hash does not (<bead>)"
```

---

### Task 5: The signature survives packaging

**Files:**

- Modify: `.github/workflows/release.yml` (the "Package .dmg and .zip" step in `build-macos`)

**Acceptance Criteria:**

- After `ditto`, the `.zip` is extracted to a temporary directory and `codesign --verify --deep --strict` passes there.
- The check runs on the file that is uploaded, not on a copy made before packaging.

- [ ] **Step 1: Append to the packaging step, after `shasum -a 256 dist/*`**

```bash
          # The updater verifies the EXTRACTED bundle, not the one we
          # assembled (internal/update/platform_darwin.go:81), and the
          # signature is the thing packaging is most likely to damage —
          # assembling the bundle by hand is what dropped the signature in
          # the first place. So verify what we are about to upload, the way
          # the updater will.
          probe="$(mktemp -d)"
          ditto -x -k "dist/${base}.zip" "$probe"
          codesign --verify --deep --strict "$probe/nocx.app"
          got="$(codesign -d -r- "$probe/nocx.app/Contents/MacOS/nocx-server" 2>/dev/null)"
          [ "$got" = "$(cat build/darwin/requirement.txt)" ] || {
            echo "::error::the packaged coordinator's requirement is not the committed one: $got"; exit 1; }
          rm -rf "$probe"
```

- [ ] **Step 2: Run the dry run**

Run: `gh workflow run release.yml --ref <branch>` then `gh run watch`
Expected: green, with no output from the verify (silence is success for `codesign --verify`).

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "build(release): verify the signature on the extracted zip, the way the updater will (<bead>)"
```

---

### Task 6: The release builds the helper it ships (nocx-mchgh)

**Files:**

- Modify: `.github/workflows/release.yml` (`build-macos` and `build-linux`: a `make helpers` step, and a gate step)
- Modify: `internal/helper/deploy/platform_test.go:21-32` (`requireArtifacts`), and add one test

**Interfaces:**

- Produces: `internal/helper/deploy/artifacts/nocx-helper-<goos>-<goarch>.gz` in both build jobs, before the Go build that embeds them.
- Produces: the environment variable contract `NOCX_REQUIRE_HELPER_ARTIFACTS=1` — set, the artifact-gated tests FAIL instead of skipping.

**Acceptance Criteria:**

- Both build jobs run `make helpers` before building the binary that embeds the artifacts.
- A build with no artifacts fails the release, and does so with a message naming `make helpers`.
- On a fresh checkout with no artifacts, `go test ./internal/helper/deploy/` still passes (the skip stays the default).

- [ ] **Step 1: Write the failing test**

Add to `internal/helper/deploy/platform_test.go`:

```go
// TestEveryMatrixPlatformResolves is the release's gate expressed as a
// test: a build that embedded nothing answers ErrArtifactsNotBuilt for
// every platform, which is exactly what every published release did until
// nocx-mchgh — release.yml never ran `make helpers`, and the refusal was
// invisible because a checkout-built binary has them (Makefile:104).
//
// It skips where the artifacts are genuinely absent (a fresh checkout) and
// FAILS where NOCX_REQUIRE_HELPER_ARTIFACTS says a build should have them.
func TestEveryMatrixPlatformResolves(t *testing.T) {
	requireArtifacts(t)
	for _, p := range []deploy.Platform{
		{GOOS: "linux", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "arm64"},
		{GOOS: "darwin", GOARCH: "arm64"},
	} {
		if _, _, err := deploy.DefaultSource.Artifact(p); err != nil {
			t.Fatalf("Artifact(%+v): %v", p, err)
		}
	}
}
```

(`darwin/amd64` joins this list in Task 7, not here — one change per task.)

- [ ] **Step 2: Make `requireArtifacts` able to fail**

Replace `internal/helper/deploy/platform_test.go:27-32` with:

```go
func requireArtifacts(t *testing.T) {
	t.Helper()
	if _, _, err := deploy.DefaultSource.Artifact(deploy.Platform{GOOS: "linux", GOARCH: "amd64"}); errors.Is(err, deploy.ErrArtifactsNotBuilt) {
		if os.Getenv("NOCX_REQUIRE_HELPER_ARTIFACTS") != "" {
			t.Fatal("embedded helper artifacts absent while NOCX_REQUIRE_HELPER_ARTIFACTS is set: this build was supposed to run `make helpers` and did not")
		}
		t.Skip("embedded helper artifacts absent — run `make helpers` first")
	}
}
```

Add `"os"` to the file's imports.

- [ ] **Step 3: Run the tests both ways**

Run:

```bash
go test ./internal/helper/deploy/ -run TestEveryMatrixPlatformResolves -v 2>&1 | tail -3
NOCX_REQUIRE_HELPER_ARTIFACTS=1 go test ./internal/helper/deploy/ -run TestEveryMatrixPlatformResolves 2>&1 | tail -3
```

Expected: the first SKIPs (fresh checkout), the second FAILs naming `make helpers`. Then:

```bash
make helpers && go test ./internal/helper/deploy/ -run TestEveryMatrixPlatformResolves
```

Expected: PASS.

- [ ] **Step 4: Add `make helpers` to `build-macos`, before "Build and assemble the .app"**

```yaml
# The helpers are EMBEDDED in the app (//go:embed all:artifacts), so
# they must exist before the Go build that embeds them. Until
# nocx-mchgh this step did not exist and every published release
# answered ErrArtifactsNotBuilt for every platform: remote git could
# not work in any release, and worked from a checkout, which is why
# nothing reported it.
- name: Build the remote helpers
  run: make helpers
```

- [ ] **Step 5: Add the same step to `build-linux`, before "Build Linux binary"**

Identical step, identical comment.

- [ ] **Step 6: Add the gate to both jobs' smoke checks**

```yaml
- name: The built binary embeds a helper for every matrix platform
  run: NOCX_REQUIRE_HELPER_ARTIFACTS=1 go test ./internal/helper/deploy/ -run 'TestEveryMatrixPlatformResolves|TestArtifactReturnsCompressedBytesAndDecompressedHash' -count=1
```

- [ ] **Step 7: Run the dry run**

Run: `gh workflow run release.yml --ref <branch>` then `gh run watch`
Expected: both build jobs green, the gate step reporting `ok` for the package.

- [ ] **Step 8: Commit**

```bash
git add internal/helper/deploy/platform_test.go .github/workflows/release.yml
git commit -m "fix(release): build the helpers the release embeds, and fail when it has not (nocx-mchgh)"
```

---

### Task 7: The helper matrix becomes 2x2

**Files:**

- Modify: `Makefile:73` (`HELPER_TARGETS`)
- Modify: `internal/helper/deploy/build.go:87-93` (`supportedTargets`) and the comment at `:42-45`
- Modify: `internal/helper/deploy/platform_test.go:87-96` (delete `TestArtifactDarwinAMD64IsUnsupported`), and `TestEveryMatrixPlatformResolves` from Task 6
- Modify: `.internal/specs/2026-08-13-remote-helper-design.md` (D20)

**Interfaces:**

- Produces: `darwin/amd64` becomes a supported target everywhere the matrix is named.

**Acceptance Criteria:**

- `make helpers` produces four `.gz` files.
- `DefaultSource.Artifact(darwin/amd64)` returns bytes, not `ErrUnsupportedPlatform`.
- A platform genuinely outside the matrix still returns `ErrUnsupportedPlatform` — the distinction between "outside the matrix" and "not built" survives.
- D20 in the remote-helper design says four targets and records that it was amended, by whom and why.

- [ ] **Step 1: Write the failing test** — extend Task 6's list

```go
	for _, p := range []deploy.Platform{
		{GOOS: "linux", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "arm64"},
		{GOOS: "darwin", GOARCH: "amd64"},
		{GOOS: "darwin", GOARCH: "arm64"},
	} {
```

- [ ] **Step 2: Run it to verify it fails**

Run: `make helpers && go test ./internal/helper/deploy/ -run TestEveryMatrixPlatformResolves`
Expected: FAIL — `Artifact({GOOS:darwin GOARCH:amd64}): deploy: no helper artifact for this platform`.

- [ ] **Step 3: Add the target to the build matrix**

`Makefile:73`:

```make
HELPER_TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
```

`internal/helper/deploy/build.go`, `supportedTargets`:

```go
var supportedTargets = []Platform{
	{GOOS: "linux", GOARCH: "amd64"},
	{GOOS: "linux", GOARCH: "arm64"},
	{GOOS: "darwin", GOARCH: "amd64"},
	{GOOS: "darwin", GOARCH: "arm64"},
}
```

And the `ErrUnsupportedPlatform` comment at `:42-45`, which names `darwin/amd64` as the example of a deliberately unshipped platform:

```go
// ErrUnsupportedPlatform is returned by the embedded source for a platform
// the build matrix deliberately does not ship a helper for (anything a
// probe cannot map onto one of the four targets: windows, 32-bit, the
// BSDs). darwin/amd64 was such a platform until 2026-08-30; it is shipped
// now, because the app itself is universal and an Intel Mac is a host we
// support.
```

- [ ] **Step 4: Delete the test that asserted the old behaviour**

Remove `TestArtifactDarwinAMD64IsUnsupported` (`platform_test.go:87-96`) entirely. It asserted a decision that no longer holds; `TestArtifactUnknownPlatformIsUnsupported` already covers `windows/amd64`, `linux/386` and `freebsd/arm64`, so the "outside the matrix" half stays asserted. Add one line to that test's doc comment:

```go
// (darwin/amd64 was in this list until the matrix went 2x2 on 2026-08-30 —
// D20 amended. The distinction it protected lives on in the platforms
// below, which the matrix genuinely does not contain.)
```

- [ ] **Step 5: Run the tests**

Run: `make helpers && go test ./internal/helper/deploy/ -count=1`
Expected: PASS, and `ls internal/helper/deploy/artifacts/*.gz | wc -l` prints `4`.

- [ ] **Step 6: Amend D20 in the remote-helper design**

In `.internal/specs/2026-08-13-remote-helper-design.md`, D20's decision column: change "for three targets — `linux/amd64`, `linux/arm64`, `darwin/arm64`" to "for four targets — `linux/{amd64,arm64}`, `darwin/{amd64,arm64}`", delete "`darwin/amd64` answers `unsupportedPlatform`", and append to the row:

```
**Amended 2026-08-30** (`.internal/specs/2026-08-30-a-stable-code-identity-and-what-the-release-publishes-design.md`): the matrix is 2x2. The app is built universal and an Intel Mac is a supported host, so excluding `darwin/amd64` meant the git panel refused the platform the bundle was carrying a slice for. The reasoning of this decision — nothing downloaded at runtime, no second delivery channel, decompressed locally — is untouched.
```

- [ ] **Step 7: Commit**

```bash
git add Makefile internal/helper/deploy/build.go internal/helper/deploy/platform_test.go .internal/specs/2026-08-13-remote-helper-design.md
git commit -m "feat(helper): ship a helper for Intel Macs too, and amend D20 to say so (<bead>)"
```

---

### Task 8: The release publishes the helper, and the two jobs must agree about it

**Files:**

- Modify: `.github/workflows/release.yml` — `build-macos` and `build-linux` (emit the binaries and a hash file), `sign` (compare, and put them in the manifest), `publish` (upload them)

**Interfaces:**

- Consumes: `internal/helper/deploy/artifacts/*.gz` from Task 6.
- Produces: release assets `nocx-helper-${VERSION}-<goos>-<goarch>`, a `helpers` array in `manifest.json` (`{os, arch, url, sha256, size}`), and `helper-hashes-<platform>.json` as a build artifact for the cross-job comparison.

**Acceptance Criteria:**

- Four uncompressed helper binaries are published, and each one's sha256 equals the content hash `internal/helper/deploy/install.go` uses to key the install directory.
- `manifest.json` carries them and is signed with the same key as before; the shipped updater ignores the new field (it decodes into `internal/update/manifest.go`'s struct, which does not reject unknown fields).
- If the macOS job and the Linux job produce different helper bytes, `sign` fails before anything is published.

- [ ] **Step 1: Emit the binaries and their hashes, in BOTH build jobs**

Add after each job's `make helpers` gate:

```yaml
- name: Stage the helper binaries for publication
  run: |
    set -euo pipefail
    # PUBLISHED UNCOMPRESSED, deliberately. `make helpers` gzips, and
    # gzip stamps the source file's mtime into its header, so the .gz
    # bytes differ between this job and the other one even when the
    # binary inside is identical. The DECOMPRESSED bytes are what
    # install.go hashes to key the install directory, so publishing
    # those makes "the same bytes the ssh path would have sent" a
    # checkable claim: a hand-installed helper lands in the same
    # directory name, and no reinstall-on-hash-mismatch follows.
    mkdir -p dist
    : > helper-hashes.txt
    for gz in internal/helper/deploy/artifacts/nocx-helper-*.gz; do
      name="$(basename "$gz" .gz)"          # nocx-helper-<goos>-<goarch>
      plat="${name#nocx-helper-}"
      out="dist/nocx-helper-${VERSION}-${plat}"
      gunzip -c "$gz" > "$out"
      chmod +x "$out"
      printf '%s %s\n' "$plat" "$(shasum -a 256 "$out" | awk '{print $1}')" >> helper-hashes.txt
    done
    sort -o helper-hashes.txt helper-hashes.txt
    cat helper-hashes.txt
```

Note for the macOS job: `shasum -a 256` (there is no `sha256sum`); on Linux either works, so use `shasum` in both for one line that reads the same.

- [ ] **Step 2: Upload the hash file from each job**

In `build-macos`, add to the existing `upload-artifact` step's path, or add a second upload:

```yaml
- uses: actions/upload-artifact@v7
  with:
    name: helper-hashes-macos
    path: helper-hashes.txt
    retention-days: 7
```

And in `build-linux`, the same with `name: helper-hashes-linux`. The Linux job's `dist/*` upload already carries the helper binaries themselves; **the Linux job's copies are the ones published** — one origin, so there is no question later about which four files went out.

- [ ] **Step 3: Compare them in `sign`, before the manifest is written**

```yaml
- uses: actions/download-artifact@v8
  with:
    name: helper-hashes-macos
    path: hashes/macos
- uses: actions/download-artifact@v8
  with:
    name: helper-hashes-linux
    path: hashes/linux

- name: The two build jobs produced the same helpers
  run: |
    set -euo pipefail
    # Two jobs cross-compile the same four helpers from the same
    # commit with -trimpath, so they must be byte-identical. If they
    # are not, a macOS-built app and a Linux-built app would deploy
    # DIFFERENT binaries to one host under one version, and D6's
    # "exactly one automatic reinstall" would fire on every
    # connection, forever, silently.
    if ! diff -u hashes/macos/helper-hashes.txt hashes/linux/helper-hashes.txt; then
      echo "::error::the macOS and Linux jobs built different helper binaries"
      exit 1
    fi
    echo "helper content hashes agree across both build jobs"
```

- [ ] **Step 4: Put the helpers in the manifest**

In the "Generate manifest.json" step, before the `jq -n` call:

```bash
          # ── helpers ──────────────────────────────────────────────────
          # sha256 here is the content hash install.go keys the install
          # directory by (D7), so a hand-installed helper and a
          # ssh-delivered one resolve to the same directory.
          helpers_json="$(
            while read -r plat sha; do
              os="${plat%-*}"; arch="${plat#*-}"
              f="dist/nocx-helper-${VERSION}-${plat}"
              jq -n --arg os "$os" --arg arch "$arch" \
                --arg url "https://github.com/shady2k/nocx/releases/download/v${VERSION}/nocx-helper-${VERSION}-${plat}" \
                --arg sha "$sha" --argjson size "$(stat -c%s "$f")" \
                '{os: $os, arch: $arch, url: $url, sha256: $sha, size: $size}'
            done < hashes/linux/helper-hashes.txt | jq -s .
          )"
```

and add `--argjson helpers "$helpers_json"` to the `jq -n` invocation plus `helpers: $helpers` to the object it builds.

- [ ] **Step 5: Upload the helper binaries in `publish`**

Append to `gh release create`:

```bash
            "dist/nocx-helper-${VERSION}-linux-amd64" \
            "dist/nocx-helper-${VERSION}-linux-arm64" \
            "dist/nocx-helper-${VERSION}-darwin-amd64" \
            "dist/nocx-helper-${VERSION}-darwin-arm64" \
```

- [ ] **Step 6: Prove the shipped updater tolerates the new field**

Run:

```bash
cat > /tmp/m.json <<'EOF'
{"version":"9.9.9","released":"2026-08-30T00:00:00Z","notesUrl":"x","artifacts":[],"helpers":[{"os":"linux","arch":"amd64","url":"u","sha256":"s","size":1}]}
EOF
go test ./internal/update/ -run Manifest -count=1
```

Expected: PASS. If a test decodes with `DisallowUnknownFields` anywhere, the manifest gains a schema change and this step becomes its own task — check before assuming.

- [ ] **Step 7: Run the dry run**

Run: `gh workflow run release.yml --ref <branch>` then `gh run watch`
Expected: `helper content hashes agree across both build jobs`, and the printed `manifest.json` carries four helper entries.

- [ ] **Step 8: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "feat(release): publish the helper binaries and put their hashes in the signed manifest (<bead>)"
```

---

### Task 9: The documents say what is now true

**Files:**

- Create: `docs/decisions/0052-a-code-identity-without-a-publisher-identity.md`
- Modify: `docs/decisions/INDEX.md`
- Modify: `docs/decisions/0003-distribution-without-a-developer-id.md` (a "Superseded in part" line)
- Modify: `internal/update/platform_darwin.go:75-90`
- Modify: bead `nocx-rybbm` (its description cites `release.yml:235`, which no longer exists)

**Acceptance Criteria:**

- The ADR states what the certificate buys, what it does not, and that a certificate change is an identity change.
- No comment in the codebase still claims the shipped bundle carries an ad-hoc signature applied by Wails.
- `nocx-rybbm` names what is now done and what is still blocked.

- [ ] **Step 1: Write the ADR**

`docs/decisions/0052-a-code-identity-without-a-publisher-identity.md`, sections Context / Decision / Rationale / Consequences. Context: the split moved the vault into `nocx-server`, ADR-0050 step 3 needs a requirement stable across releases, ad-hoc anchors it to the code hash. Decision: a self-signed project certificate signs the bundle and the coordinator; the requirement is committed to `build/darwin/requirement.txt` and gated in CI; Gatekeeper, notarization and hardened runtime stay out; integrity remains the signed manifest on both platforms. Consequences: the manual quarantine step and Homebrew ineligibility are unchanged; a certificate change is an identity change and existing installs lose their keychain grants; the code-signing key lives in a job that runs build tooling and cannot be anywhere else, while the manifest key's isolation is untouched.

- [ ] **Step 2: Add the row to `docs/decisions/INDEX.md`**

```
| 0052 | [A code identity without a publisher identity](0052-a-code-identity-without-a-publisher-identity.md) | Accepted 2026-08-30 |
```

- [ ] **Step 3: Amend ADR-0003 in one line, under its Decision**

```markdown
> **Amended by ADR-0052 (2026-08-30):** publisher identity is still absent —
> no Developer ID, no notarization, no Gatekeeper policy, and the manual
> quarantine step stands. What changed is that the bundle no longer signs
> itself ad-hoc: it carries a project certificate, because the keychain
> needs a designated requirement that survives a release (ADR-0050 step 3).
```

- [ ] **Step 4: Correct the updater's comment and error text**

`internal/update/platform_darwin.go`, in `VerifyExtracted`:

```go
	// codesign integrity check. The shipped bundle carries the project
	// code-signing certificate (ADR-0052); it used to carry the ad-hoc
	// signature Wails v2.13 applied, and this check long predates the
	// change. What it verifies is unchanged: that the extracted bundle's
	// seal is intact, not who signed it.
```

and the error:

```go
			return fmt.Errorf("codesign verification of %s failed (the bundle's signature may have been damaged in transit or extraction): %w\n%s", bundlePath, err, string(out))
```

- [ ] **Step 5: Run the affected tests**

Run: `go test ./internal/update/ -count=1`
Expected: PASS. If a test asserts the old error string, update the test — the string is the product of this task, not evidence against it.

- [ ] **Step 6: Update the bead**

```bash
bd show nocx-rybbm   # read the notes FIRST: --notes replaces, it does not append
bd update nocx-rybbm --notes "<the existing text, with the release.yml:235 obstacle marked resolved by <this epic>, and CGO_ENABLED=0 at release.yml:199-203 left as the one remaining build obstacle>"
```

- [ ] **Step 7: Commit**

```bash
git add docs/decisions/ internal/update/platform_darwin.go
git commit -m "docs(decisions): record the code identity, and stop calling the shipped signature ad-hoc (<bead>)"
```

---

## Order and dependencies

```
1 ──▶ 2 ──▶ 3 ──▶ 4
              └──▶ 5
6 ──▶ 7 ──▶ 8
(1..5) ∪ (6..8) ──▶ 9
```

Tasks 6-8 are independent of the signing chain and can run in parallel with it — different files, different jobs, one shared workflow file whose hunks do not overlap. Task 9 is last because it describes what the others did.

## What this plan does not do

- `CGO_ENABLED=1` for the darwin coordinator, the Security.framework provider, and the presence prompt: `nocx-rybbm`, blocked on measurements that need a Mac.
- The release-notes defect found while writing this: `nocx-woh3s` (publish has no checkout, so `docs/release-notes/v<version>.md` is never found and every release ships the fallback text). Separate bug, separate fix.
