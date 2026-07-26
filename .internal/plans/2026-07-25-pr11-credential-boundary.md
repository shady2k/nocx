# PR #11 Credential Boundary Implementation Plan (v3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** A _stored_ secret never crosses out of the credential/SSH boundary — not to the renderer, not into a log, not onto the wire — and the renderer's known injection paths are closed.

> **Wording matters here, and an earlier draft overstated it twice.** "The store returns
> nothing outward" was false: `CredentialStore.LookupPassword` returns a `string`
> (`internal/credential/credential.go:22-25`) and `internal/ssh/ssh_auth.go:117` receives
> one, because SSH genuinely needs the plaintext to authenticate. The accurate claim is
> the one above: plaintext exists only inside `internal/credential` and `internal/ssh`.
> Likewise "the renderer stops being privileged" was false — Task 2 closes known injection
> paths, but the renderer still holds the WebSocket capability and still handles freshly
> typed passwords. An overstated guarantee is how people stop checking.

**Architecture:** The SSH package already resolves stored secrets itself via `ssh.WithCredentials(store, identity)` (`internal/ssh/ssh.go:139-146`), late-binding at auth time (`internal/ssh/ssh_auth.go:117-118`). This plan does not build a new resolution path — it _uses the one the PR author already built_ and removes everything that routes around it. The control plane carries a profile ID; the backend maps it to a `credential.Identity` and hands the store plus the identity to the SSH layer. No component between the keychain and `x/crypto/ssh` ever holds the plaintext.

**Tech Stack:** Go (`internal/{credential,profile,ssh,session,transport,app}`), TypeScript (`frontend/src/{connections,profiles,ipc,tabs,main}.ts`), `go test -race`, vitest, Playwright.

## Scope of the guarantee — stated honestly

The goal above says **stored**. That word is load-bearing and v1 of this plan overreached without it.

- **In scope:** a secret already in the keychain is never returned, never travels on the connection-open path, and never appears in a log.
- **Out of scope, deliberately:** _initial entry_. The user types a password into our own password field and it travels once, one way, to the backend (`frontend/src/connections.ts:876-879` → `frontend/src/profiles.ts:269-270` → `credentials.savePassword`). A native secure prompt was argued for and **rejected:** for a local-first desktop app whose renderer is our own bundled HTML, a one-way, user-initiated, transient write is a categorically smaller risk than a standing oracle that hands stored secrets back on every connect. Building native secure ingress is disproportionate here. **State the consequence plainly rather than hiding it: the confidentiality of initial entry trusts the renderer.** Task 2 shrinks that trust by closing injection paths; it does not eliminate it. A future XSS, a malicious dependency, or an attached debugger all defeat it.
- **Out of scope:** _encrypted private keys_. They do not work today at all — `loadKey` (`internal/ssh/ssh_auth.go:180-194`) calls only `gossh.ParsePrivateKey` and returns `ErrEncryptedKey`; `ParsePrivateKeyWithPassphrase` appears nowhere in production, and the `lookupKeyPassphrase` helper at `:155` has no production caller. So removing the lookup RPC in Task 4 breaks nothing — but this plan must not claim to preserve a capability that never existed. Backend late-binding (read key → derive the storage hash → fetch the passphrase inside `internal/ssh` → `ParsePrivateKeyWithPassphrase`) is real work; it is filed separately, not smuggled in here.
- **Documented limitation, not a work item:** secrets live in process memory as Go values and can appear in a core dump; Go cannot reliably erase a `string` copy, and `x/crypto/ssh` keeps its own. Recorded in ADR-0011 §Consequences (added 2026-07-25 — an earlier draft of this plan claimed it was there when it was not).

## Global Constraints

- **Branch:** `pr-11-boundary`, from `pr-11` (`refs/pull/11/head`, `557e87d`). Do not rewrite the author's commit.
- **Binding:** ADR-0011, AD-1 (control plane JSON-RPC; PTY bytes never in JSON), AD-6 (backend never parses the byte stream), AD-7, AD-8.
- **Not in this plan:** connection pool wiring, client/jump leaks, dial cancellation (`nocx-ea6` items 3/4/9); vault AES-CBC→AEAD (`nocx-ea6` item 5); DocumentStore, path resolution, settings registry (`nocx-6ek`); the secret-transition journal (ADR-0011 §4, belongs with `nocx-6ek`).
- **Gate per commit:** `gofumpt -l .` clean, `golangci-lint run`, `go test -race ./...`; for frontend commits also `npx prettier --check .`, `npx eslint .`, `npx tsc --noEmit`, `npx vitest run`.
- **Read before writing any test.** The harnesses exist: Go uses `connectWS(t, ws)` + `jsonrpcCall(t, conn, method, map[string]any{})` with `keyring.MockInit()` and the `WithProfileStore` / `WithCredentialStore` options (`internal/transport/ws_profiles_test.go:15-40`). The frontend uses `new WSClient()` with `socket().serverAccepts()` (`frontend/src/ipc.test.ts:42-45, 85-101`). Do not invent helper names — v1 of this plan invented three and every one was wrong.

---

### Task 1: Stop the two active disclosures

Both are live today and neither depends on anything else.

**Files:**

- Modify: `frontend/src/tabs.ts:560` (delete the log line)
- Modify: `internal/transport/ws.go:417-429` (raw frame logging)
- Test: `internal/transport/ws_logging_test.go` (create)

**Acceptance Criteria:**

- `frontend/src/tabs.ts` no longer logs `sshOpts`.
- A malformed or unparsable control frame is logged by **size, parse-error category and request id only** — never its bytes.
- A truncated `credentials.savePassword` frame produces a log line containing no substring of the password.

- [ ] **Step 1: Write the failing test**

Read `internal/transport/ws.go:410-435` first to see the current log call and its logger. Then create `internal/transport/ws_logging_test.go` asserting that feeding a truncated `credentials.savePassword` frame produces no log record containing the password. Capture logs with a `slog` handler writing to a `bytes.Buffer`, the way `log.NewSlogAdapter` is constructed in the existing suite.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/transport/ -run TestMalformedFrame -race`
Expected: FAIL — the raw frame, password included, is in the buffer.

- [ ] **Step 3: Fix both sites**

Delete `frontend/src/tabs.ts:560`. Do not replace it with a redacted variant — Task 4 removes the secret from that object entirely, and a redacted log would encode the assumption that secrets belong there.

In `ws.go`, replace the raw-frame log with size + category + id. This is a transport-wide rule, not a patch for one method: any control frame may carry a secret, so none of them are logged verbatim.

- [ ] **Step 4: Verify**

Run: `go test -race ./internal/transport/` and `cd frontend && npx vitest run`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -am "fix(security): stop logging SSH passwords and raw control frames (nocx-ea6)"
```

---

### Task 2: Harden the renderer against injection (`nocx-2ay`)

**This is the highest-severity task in the plan and it comes before the socket token, deliberately.** A token handed to an injectable renderer protects nothing: injected same-origin code calls the binding and gets it. Codex's own closing verdict was that the renderer remains a privileged injectable authority, and it is right.

The chain is verified end to end: `internal/importer/tabby.go` parses an untrusted YAML the user imports → credential names from that file are interpolated into HTML at `frontend/src/connections.ts:472-475` with no escaping → `frontend/index.html` has no CSP → injected code can call `open` and get a PTY.

**Files:**

- Modify: `frontend/src/connections.ts:472-475` and every other `innerHTML` site (`main.ts` ×2, `sidebar.ts`, `tabs.ts` ×2 — audit each; some may be static and fine)
- Modify: `frontend/index.html` (add CSP)
- Modify: Wails config / `main.go` (inspector off in production, block untrusted navigation)
- Test: `frontend/src/connections.test.ts`

**Acceptance Criteria:**

- A credential named `<img src=x onerror="...">` renders as literal text; no element is created from it.
- `index.html` carries a CSP that forbids inline script and foreign origins, and the app still runs under both Vite dev and a Wails production build.
- The devtools inspector is off in production builds.

- [ ] **Step 1: Write the failing test**

Render the credential info panel with `name` set to `<img src=x onerror="window.__pwned=1">` and assert `container.querySelector('img')` is null and the text content contains the literal tag.

- [ ] **Step 2: Run it and watch it fail**

Expected: FAIL — an `img` element exists.

- [ ] **Step 3: Replace interpolation with construction**

Build the panel with `document.createElement` and `textContent`. Audit the other `innerHTML` sites; leave genuinely static markup alone and convert anything touching stored or imported data.

- [ ] **Step 4: Add the CSP and verify both build modes**

Run: `cd frontend && npx vitest run && npx tsc --noEmit`, then a Wails production build. A CSP that breaks the app is worse than none — it will be removed by the next person in a hurry.

- [ ] **Step 5: Commit**

```bash
git commit -am "fix(security): escape stored data in the UI, add CSP, disable prod inspector (nocx-2ay)"
```

---

### Task 3: Authenticate the local WebSocket (`nocx-x4u`)

`internal/transport/ws.go:72` listens on `127.0.0.1:0` and `:62` accepts every origin, with no authentication at any layer. Behind it, `open` creates a PTY. The random port is friction, not authorization.

**Design decisions, with reasons:**

- **Transport: `Sec-WebSocket-Protocol`.** Browser `WebSocket` cannot set `Authorization` or custom headers. A query parameter is the worst option — it lands in URLs, proxy logs, devtools and crash diagnostics. A first-frame handshake authenticates too late: the socket is already upgraded and every subsequent path needs its own gate. The subprotocol keeps the capability out of the URL and lets us reject **before** upgrade.
- **Token: 32 random bytes from `crypto/rand`, encoded unpadded base64url.** Not raw bytes and not padded base64 — a subprotocol token must be a valid HTTP token, and `=` and `/` are not. Validate the request's _parsed_ protocol list, compare in constant time (`crypto/subtle.ConstantTimeCompare`), and echo the selected protocol back on upgrade or the browser will reject the connection. Fail closed if generation errors — the rule `ActivationEnv` already follows for the session id.
- **Origin and Host both checked — but the production Origin must be captured, not guessed.** An HTTP `Origin` is scheme/host/port with no trailing path, so the Wails production value is most likely `wails://wails` rather than `wails://wails/`; hard-coding the wrong one rejects the legitimate client and the failure looks like a bug in the token. **Step one of the implementation is to log the actual `Origin` from a production Wails build on each supported platform and pin those values.** Dev is an HTTP origin with a dynamic port, so the policy is injected per runtime mode. Reject missing, malformed and foreign origins in production. Also require the expected loopback `Host` with the real port — that is what closes DNS rebinding. Both are defence in depth; a native local process forges either trivially, so the token stays mandatory.
- **A Go test with a synthetic Origin cannot establish the Wails value.** Add either a production-shell handshake test or an explicit manual verification gate before this task is closed.
- **Rejected: Unix socket / `SO_PEERCRED` / a 0600 token file.** All are stronger primitives and none is reachable from a WebView `WebSocket`, which AD-1 requires. A token file additionally needs a second bridge to be readable by JS and enters disk and backup scope.
- **Stated limitation:** this stops foreign pages and ordinary local processes. It does not stop a compromised renderer or a same-user debugger reading memory. That is what Task 2 is for, and why Task 2 comes first.

**Files:**

- Modify: `internal/transport/ws.go:62` (origin policy), `:72` (listener), the upgrade path
- Modify: `main.go:150` area (`GetWSToken` binding beside `GetWSPort`)
- Modify: `frontend/wailsjs/go/main/WailsApp.{js,d.ts}` — **regenerate or hand-update, or `main.ts` will not type-check**
- Modify: `frontend/src/main.ts:2`, `frontend/src/ipc.ts` (pass the token as subprotocol)
- Modify: `e2e/harness.ts:13-26` (currently stubs only `GetWSPort`)
- Test: `internal/transport/ws_auth_test.go` (create)

**Acceptance Criteria:**

- No token, wrong token, hostile Origin, and wrong Host each fail **before** upgrade; `open` is unreachable in every case.
- Correct token + correct origin + correct host succeeds and existing behaviour is unchanged.
- Token is fresh per launch, never logged, never persisted.
- Entropy failure fails closed — injectable so it can be tested.
- e2e authenticates for real. **Do not add a bypass to the harness**: pass a test-scoped token through the launch protocol and add a case asserting an unauthenticated connection fails.

- [ ] **Step 1: Write the failing tests**

`connectWS` at `internal/transport/ws_test.go:42-52` sends neither token nor Origin. Add a `connectWSRaw(t, ws, token, origin, host)` beside it and give `connectWS` the valid values so the rest of the suite keeps passing. Cover: missing token, wrong token, foreign origin, wrong host, entropy failure, and the happy path.

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/transport/ -run TestHandshake -race`

- [ ] **Step 3: Implement**

`NewWSServer` returns only `*WSServer`, so generate the token in `Start` before `Listen` rather than changing every construction site — and inject the entropy source so failure is testable.

- [ ] **Step 4: Verify everything, including e2e**

Run: `go test -race ./...`, `cd frontend && npx vitest run && npx tsc --noEmit`, then the Playwright suite. Every e2e case failing at connect is the expected blast radius, not a surprise.

- [ ] **Step 5: Commit**

```bash
git commit -am "fix(security): require a per-launch capability on the local WebSocket (nocx-x4u)"
```

---

### Task 4: Move the wire to profile IDs — one atomic commit

**v1 of this plan split backend and frontend across two tasks; that leaves an intermediate commit where SSH cannot connect at all.** Backend contract, frontend migration and removal of the plaintext-lookup RPCs land together.

**Use the seam that already exists.** `ssh.WithCredentials(store, identity)` (`internal/ssh/ssh.go:139-146`) hands the SSH package the _store and an identity_; it resolves the password itself at `internal/ssh/ssh_auth.go:117-118`, and `session.go:274` already forwards it. So the resolver's job is **only** to map a profile ID to a host, a `credential.Identity`, and non-secret options. Nothing in `transport`, `session` or `connection` ever holds a plaintext password — plaintext exists only inside `internal/credential` and `internal/ssh`, which is the boundary this plan defends. This is stronger than making `CredentialStore` return a wrapped `Secret`, because it keeps the value out of three packages instead of dressing it up as it passes through them.

**Get the model right — v1 got this wrong.** `SSHProfile` is `Base` + `Options` (`internal/profile/profile.go:71-74`); host, port, user, auth and jump all live under `Options`, not at top level. `Options.CredentialID` references a reusable `Credential`, and when set, **username, auth mode and key path come from the credential, not the profile** (`profile.go:49-56`, `:95-108`). Secrets saved by credential ID are keyed `Identity{User: credentialID}` (`internal/transport/ws.go:1030-1035`); inline-mode secrets are keyed by `{user, host, port}`. The resolver must handle both.

**Files:**

- Create: `internal/connection/resolver.go`, `resolver_test.go`
- Modify: `internal/transport/ws.go` — the `open` params (`:316-326`), remove `credentials.lookupPassword` and `credentials.lookupKeyPassphrase` (`:458-459`, `:1077-1095`, `:1123-1136`)
- Modify: `internal/app/app.go` (wire the resolver at the composition root)
- Modify: `frontend/src/ipc.ts`, `frontend/src/connections.ts:273-319` **and `:565-611`** (password resolution happens twice), `frontend/src/profiles.ts:278-280` (remove `lookupPassword`), `frontend/src/tabs.ts` (SSH option declarations and callers at ~129-142, 909-916, 944-958, 1009-1021)
- Delete: `TestCredentialsRPC_SaveLookup` (`internal/transport/ws_profiles_test.go:144`) — it asserts the behaviour being removed

**Interfaces:**

- Produces: `ResolveSSH(ctx, profileID) (host string, opts []ssh.ConnectOption, err error)`, where `opts` includes `ssh.WithCredentials(store, identity)` and never a `WithPassword`.
- Wire: `open` params for `kind:"ssh"` become `{profileId, cols, rows, xpixel, ypixel}`. Removed: `host, user, port, keyFile, password, authMode, jumpHost, jumpPort, jumpUser, jumpPassword, jumpAuthMode` — note `jumpAuthMode`, which v1 forgot.

**The jump hop needs an SSH-layer change — v2 promised something the API cannot express.**
`ConnectConfig` carries exactly one `Credentials`/`CredIdentity` pair, for the target
(`internal/ssh/ssh.go:66-70`). `WithJumpHost` takes a plaintext password, and
`connectToJumpHost` (`internal/ssh/ssh_dial.go:109-116`) builds a fresh `jumpCfg` with
`Password: cfg.JumpPassword` and **never forwards `Credentials`**. So "the resolver maps
the jump profile's own credential" is unimplementable as written. Add a second capability —
`JumpCredentials credential.CredentialStore` + `JumpCredIdentity credential.Identity` —
forward it in `connectToJumpHost`, and surface it through `session.sshOptionsFromConfig`
(`internal/session/session.go:246-278`), which currently maps only `WithJumpHost(...)` with
a password. **Additional files for this task:** `internal/ssh/ssh.go`,
`internal/ssh/ssh_dial.go`, `internal/session/session.go`.

**Acceptance Criteria:**

- Password auth, public-key auth (`Credential.KeyPath` → `WithKeyFile`), and jump-host connections all still work — jump resolution loads the jump profile by name or ID and passes _its_ store and identity, never a password.
- A test exercises the real auth-chain construction for both hops, not only the options the resolver returned. Options that look right and a chain that never receives them is exactly the failure mode here.
- Unknown profile ID → error naming the ID, wrapping `ErrProfileNotFound`.
- `credentials.lookupPassword` / `lookupKeyPassphrase` return `-32601`; `hasPassword` still works.
- No `frontend/src/` type that crosses the WebSocket has a `password` or `jumpPassword` field; `tsc --noEmit` clean.
- **Quick-connect decision recorded:** if the wire accepts stored profile IDs only, ad-hoc "connect to host now" either creates an ephemeral profile or is explicitly dropped. Decide and write it down; do not discover it in review.

- [ ] **Step 1: Write the failing resolver tests**

Cover all four paths: inline-mode password, credential-ID mode (username/auth/keyPath from the `Credential`), public-key mode (asserts `WithKeyFile`, and that no password lookup happens), and jump. Assert the resolver never returns a password: apply the options to a `ssh.ConnectConfig` and check `cfg.Password == ""` while `cfg.Credentials != nil`.

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/connection/ -race`

- [ ] **Step 3: Implement the resolver, then the wire, then the frontend, then delete the RPCs**

Four edits, one commit. The tree is broken in between; that is why they are one commit.

- [ ] **Step 4: Verify**

Run: `go test -race ./...`, `cd frontend && npx vitest run && npx tsc --noEmit`, then Playwright.

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(ssh): carry a profile id on the wire, resolve credentials backend-side (nocx-ea6, ADR-0011)"
```

---

### Task 5: Bind a credential to its target

`Credential.Host` is optional and empty means "works for any host" (`internal/profile/profile.go:105-107`). The same authenticated renderer can create profiles _and_ call `open`. So even with Task 4 done — the password never returned — injected or hostile renderer code can point a victim credential at a host it controls and have the backend submit that password. Strict `known_hosts` limits _which_ hosts answer; it is not credential authorization.

**The policy is decided here, not by the implementer.** An earlier draft offered "refuse, or
require recorded approval — pick one". That delegates the security decision, and the
approval variant is worse than it looks: the approval UI would live in the renderer, which
is precisely the actor Task 5 exists to constrain. So:

> **A stored password credential must carry a bound host and effective port. An unbound
> (empty-`Host`) credential is refused at connection time.**

Matching is on the **resolved hostname and effective port**, not the profile alias and not
the configured `Host` string — those three differ whenever `~/.ssh/config` is involved, and
matching the wrong one either blocks legitimate use or fails open.

**Enforce it inside `internal/ssh`, immediately after `resolveConfig` — not in the
resolver.** `resolveConfig` is an unexported method on `RealClient`
(`internal/ssh/ssh_config.go:26-28`) that merges `~/.ssh/config` with the explicit options,
precedence _explicit > config file > default_. The authoritative host and port therefore
only exist inside `internal/ssh`. `internal/connection` cannot know them without
reimplementing SSH config resolution, and a second implementation that disagrees with the
first is precisely how a credential gets bound to the wrong target. So the resolver passes
the **allowed** host and port alongside the credential capability, and `internal/ssh`
checks them against the resolved values before any authentication attempt.

Existing unbound credentials need a migration path: bind on first use with the user's
confirmation, or make the user pick a host when editing. Choose during implementation and
record it; the _rule_ above is fixed.

**Files:**

- Modify: `internal/ssh/ssh_config.go` / the call site right after `resolveConfig` (enforcement point)
- Modify: `internal/ssh/ssh.go` (carry the allowed host/port alongside the credential capability)
- Modify: `internal/connection/resolver.go` (pass the allowed host/port; it does **not** decide)
- Test: `internal/ssh/credential_binding_test.go` (create)

**Acceptance Criteria:**

- A credential bound to one host is refused for any other target, **checked inside `internal/ssh` after `resolveConfig` and before any authentication attempt**, with a distinct error.
- An unbound password credential is refused at connection time.
- Matching uses the resolved hostname and effective port — never the profile alias, never the configured `Host` string.
- Tested against `~/.ssh/config` resolution, not just literal hosts: **an alias whose `HostName` differs from the alias, and an alias whose `Port` overrides the default.** A credential bound to the alias must not authorize a different resolved `HostName`, and vice versa.
- Covered by a test that attempts exactly the misuse: a credential bound to `a.example.com` offered to a profile pointing at `b.attacker.example`.

- [ ] **Step 1: Write the failing tests** in `internal/ssh` — (a) credential bound to `a.example.com` used against `b.attacker.example`, expect refusal; (b) an alias `prod` with `HostName b.attacker.example` in a temp `~/.ssh/config`, credential bound to `prod`, expect refusal because the _resolved_ host differs; (c) an alias with a `Port` override, credential bound to the default port, expect refusal; (d) unbound credential, expect refusal.
- [ ] **Step 2: Run them, watch them pass wrongly** — today nothing checks, so the connection proceeds.
- [ ] **Step 3: Enforce inside `internal/ssh`, immediately after `resolveConfig`.** Not in the resolver: `internal/connection` cannot see the resolved values without reimplementing SSH config resolution, and two implementations that disagree are how a credential gets bound to the wrong target.
- [ ] **Step 4:** `go test -race ./...`
- [ ] **Step 5: Commit** — `fix(security): bind stored credentials to their resolved target host (nocx-ea6)`

---

### Task 6: Deleting a credential deletes its secret

`internal/transport/ws.go:998-1010` deletes metadata only; the keychain entry is orphaned permanently.

**This needs stable secret references to be correct**, which is why it comes after Task 4: today, password saves by credential ID key on `Identity{User: credentialID}` while key passphrases key on `KeyHash` — and metadata stores only `KeyPath`, so once the metadata is gone there is no hash left to delete from. Load the metadata **before** deleting it, derive both keys, then delete metadata first and the secrets after (ADR-0011 §4: a brief unreachable orphan beats metadata pointing at a deleted secret).

**Acceptance Criteria:**

- Deleting a credential removes the metadata, the password entry, and the key-passphrase entry.
- Secret-deletion failure is reported; metadata deletion still stands.
- Test uses `keyring.MockInit()` and the real ID assigned by the create RPC — never a hardcoded ID format.

- [ ] Steps follow the same shape: failing test → run → implement → `go test -race ./...` → commit.

---

### Task 7: `VaultSecret` stops being serializable

`internal/credential/credential.go:59-64` defines `Value string \`json:"value"\``— a secret designed to marshal. Replace with the`Secret` type.

**Note the honest scope:** this changes encryption and decryption DTOs, their adapters, and existing credential tests — more than the three files v1 listed. It does **not** fix the vault's unauthenticated AES-CBC or its length-only padding check (`vault.go:97-114`, `:274-295`); that is `nocx-ea6` item 5 and stays deferred. Do not mistake this task for the crypto fix.

Add `internal/credential/secret.go` with a `Secret` that refuses `MarshalJSON`/`MarshalText`, renders `[REDACTED]` through `String`/`GoString`/`LogValue`, and exposes plaintext only via `Use(func([]byte) error)`. Its value is defence in depth _inside_ the credential and ssh packages — after Task 4 the boundary is that no API returns a secret outward, and the type is what keeps an accident inside from undoing that.

- [ ] Steps: failing test (`json.Marshal` of a struct containing a `Secret` must error) → run → implement → migrate `VaultSecret` and the vault DTOs → `go test -race ./internal/credential/` → commit.

---

## Where I disagree with the review that produced this plan

Recorded so the reasoning survives, not to be contrarian:

- **Native secure prompt for initial password entry — rejected.** Disproportionate for a local-first app with a bundled renderer. Task 2 addresses the actual risk; a one-way user-initiated write is not a standing oracle.
- **"Cannot reach a crash report" must be provable — declined as a work item.** True of any process holding credentials. Documented as a limitation in ADR-0011 instead of chased.
- **WSL reachability testing — noted, not planned.** Windows is Phase 3 (`docs/vision.md:75`) and nocx does not support WSL today. If it ever does, retest loopback reachability from the Windows host; the token requirement already holds regardless.
- **`CredentialStore` returning `Secret` — superseded by something stronger.** The author's `WithCredentials` seam means the store need not return a secret outward at all. Task 7 keeps the type for internal defence in depth.

## Out of scope, tracked

`nocx-ea6` items 3/4/9 (pool dead code, client and jump leaks, dial cancellation) · item 5 (vault AEAD) · item 6 (hygiene: `frontend/src/tabs.ts.orig`, `gofumpt` on `internal/profile/profile.go`, trailing whitespace) · `nocx-6ek` (DocumentStore, path resolution, settings registry, ContentDB seam) · `nocx-p7g` (profile store split by aggregate) · `nocx-de7` (backend output capture) · Tabby import policy for the inline `Password` in the YAML (`internal/importer/tabby.go:31-44`) — the converter drops it, but the renderer, transport buffers and process memory all see it; folded into `nocx-2ay`.
