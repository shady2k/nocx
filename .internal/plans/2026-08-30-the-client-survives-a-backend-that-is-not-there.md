# The client survives a backend that is not there — implementation plan

> **For agentic workers:** each Task below is one bead and one worker. Your brief is the
> task section plus `.internal/specs/2026-08-30-the-client-survives-a-backend-that-is-not-there-design.md`.
> Do not run repo-wide gates; the coordinator runs those at the end of the epic.

**Goal:** the app opens whether or not a backend exists, states one condition in one place
when it cannot reach one, recovers by re-discovering the coordinator rather than by
retrying a dead port, and notices a socket that is open but dead.

**Architecture:** the Dispatcher keeps ownership of the socket, the schedule and the name of
the condition, and gains a fourth state (`blocked`). What it connects _to_ stops being a
remembered port and becomes the result of an `EndpointProvider` — an interface with one
implementation today (the Wails binding that re-runs `Launcher.Launch`) and a second one
reserved for the web client. The launcher stops printing wrapped Go errors in a modal that
quits, and returns typed failure kinds the overlay can render with a remedy. A heartbeat on
the JSON-RPC control plane makes a half-open socket indistinguishable from a closed one.

**Tech stack:** Go 1.x (`internal/coordinator`, `internal/transport`, `main.go`),
TypeScript + Solid (`frontend/src`), `gorilla/websocket`, vitest, Playwright.

## Global constraints

- **AD-1**: the heartbeat is a JSON-RPC control-plane method. Never a binary msg-type,
  never a second plane. Its result shape gets a JSON Schema in `contracts/`.
- **AD-3**: the Wails shell offers a _capability_, never a _policy_. The binding performs
  one attempt and reports. The backoff, the single-flight guard and the decision to retry
  live in the renderer.
- **AD-8**: variation is expressed by the interface. No `if (isWeb)`, no mode strings.
- **`frontend/src/ui/README.md` is binding**: a new control goes in `ui/`, with one CSS
  file in `styles/components/`, a stable identity class, a test and a README row. A surface
  may place a kit component and may never repaint it.
- **No repo-wide gates in a worker.** Targeted tests for your own files, plus the
  type-checker with the exact binary named in your task. The coordinator runs
  `make ci-full` and the e2e suite once, on the merged tree.
- **No commits to `main`, no pushes, no `bd` writes.** Commit on your own branch only.
- **Exact frontend type-check** (both projects, from `frontend/`):
  `./node_modules/.bin/tsc --noEmit -p tsconfig.json && ./node_modules/.bin/tsc --noEmit -p tsconfig.test.json`
- **Exact frontend targeted test** (from `frontend/`):
  `./node_modules/.bin/vitest run src/<your-file>.test.ts`
- **Exact Go targeted test:** `go test ./internal/<your-package>/...`
- **Go commands that reach `main.go` need `-tags gtk3` on Linux.** Without it cgo fails on
  the GTK/WebKitGTK pkg-config before it reaches our code, and `go vet ./...` reports an
  environment problem that reads like a broken build. `go build -tags gtk3 .` and
  `go vet -tags gtk3 .` are the working forms. `internal/notify/wailsadapter` is the package
  that fails first; three `internal/shellintegration` tests separately need `dash` installed.
- A test asserts what a user can do, not what the code currently does. For every
  "returns an error when…" there is a paired "and on an ordinary machine it succeeds".

## File structure

| File                                                          | Owner task  | Responsibility                                         |
| ------------------------------------------------------------- | ----------- | ------------------------------------------------------ |
| `internal/coordinator/launchfailure.go` (new)                 | T1          | the typed failure kind, its message and its remedy     |
| `internal/coordinator/launcher.go`                            | T1          | classify, instead of wrapping into a string            |
| `internal/transport/heartbeat.go` (new)                       | T2          | the `transport.ping` handler and the read deadline     |
| `internal/transport/ws.go`                                    | T2          | register the method; set the deadline on the read loop |
| `contracts/transport.ping.schema.json` (new)                  | T2          | the result shape                                       |
| `frontend/src/ui/connection-overlay.tsx` (new)                | T3          | the kit component, unwired                             |
| `frontend/src/styles/components/connection-overlay.css` (new) | T3          | its appearance                                         |
| `frontend/src/ui/README.md`                                   | T3          | its row                                                |
| `frontend/src/endpoint.ts` (new)                              | T4          | `EndpointProvider`, `Endpoint`, `EndpointFailure`      |
| `frontend/src/dispatcher.ts`                                  | T4, then T6 | the socket, the schedule, the four states              |
| `frontend/src/endpoint-desktop.ts` (new)                      | T5          | the provider backed by the Wails binding               |
| `main.go`                                                     | T5          | the binding; the launcher path stops calling `fatal()` |
| `frontend/vite.dev-view.config.ts`                            | T5          | the dev stand's shim, kept working                     |
| `frontend/src/main.tsx`                                       | T7          | the composition root, split by lifetime                |
| `e2e/connection-overlay.spec.ts` (new)                        | T8          | the happy path, watched end to end                     |

## Task ordering

```
wave 1 (parallel, disjoint):   T1   T2   T3   T4
wave 2 (parallel, disjoint):        T5 (needs T1,T4)     T6 (needs T2,T4)
wave 3 (sequential):                     T7 (needs T3,T4,T5)  →  T8
```

---

### Task 1: the launcher classifies its own failure

**Files:**

- Create: `internal/coordinator/launchfailure.go`
- Create: `internal/coordinator/launchfailure_test.go`
- Modify: `internal/coordinator/launcher.go` (`Launch`, `raise`, `replace`)

**Interfaces — Produces:**

```go
// FailureKind names why a launch cannot succeed by being repeated as-is.
type FailureKind string

const (
    FailureProfileUnusable       FailureKind = "profile-unusable"
    FailureServerBinaryUnusable  FailureKind = "server-binary-unusable"
    FailureIncompatible          FailureKind = "incompatible-coordinator"
    FailureNotReady              FailureKind = "not-ready"
)

// LaunchFailure is what a person is told, not what a log records.
type LaunchFailure struct {
    Kind    FailureKind // one of the four above
    Message string      // one sentence stating what could not be done
    Remedy  string      // what to do about it
    Cause   error       // the wrapped error, for the log only — never rendered
}

func (f *LaunchFailure) Error() string { /* Message */ }
func (f *LaunchFailure) Unwrap() error { /* Cause */ }

// AsLaunchFailure reports whether err carries a classified failure.
func AsLaunchFailure(err error) (*LaunchFailure, bool)
```

`Launch` returns a `*LaunchFailure` for every failure path it owns. Callers that only log
keep working, because it is an `error`.

**Acceptance criteria:**

- Every failure return from `Launcher.Launch` carries a `FailureKind`, a `Message` and a
  `Remedy`. None returns a bare `fmt.Errorf`.
- `Message` and `Remedy` are sentences a person reads: no Go type names, no `%w` chains,
  no package paths. The wrapped cause stays in `Cause` for the log.
- `FailureIncompatible` names the socket path; `FailureNotReady` names the daemon's log
  path; `FailureServerBinaryUnusable` differs by GOOS — reinstall on darwin, the
  `~/.local/share/nocx/bin` path on linux.
- `AsLaunchFailure` recovers the classification through `errors.As`.
- Paired per AGENTS.md rule 3: on an ordinary machine, a successful `Launch` returns no
  failure and the existing three outcomes (found / raised / replaced) are unchanged — the
  existing `launcher_test.go` cases still pass untouched.

**Steps:**

1. Write `launchfailure_test.go` first: one case per kind asserting `Kind`, that `Message`
   and `Remedy` are non-empty, that `Message` contains no `:` chain from `%w`, and that
   `errors.As` recovers it through one `fmt.Errorf("%w")` wrap.
2. Run `go test ./internal/coordinator/...` — expect failures naming undefined symbols.
3. Write `launchfailure.go`.
4. Change each failure return in `launcher.go` (and `main.go:410`'s two pre-`Launch`
   failures are **not** yours — T5 owns `main.go`; expose what T5 needs by exporting the
   constructor).
5. Run `go test ./internal/coordinator/...` — all green, including the pre-existing cases.
6. Commit.

**You do not own:** `main.go`. Anything you need there, state in your completion report.

---

### Task 2: the transport notices that it is dead

**Files:**

- Create: `internal/transport/heartbeat.go`
- Create: `internal/transport/heartbeat_test.go`
- Create: `contracts/transport.ping.schema.json`
- Modify: `internal/transport/ws.go` (method registration; the read loop's deadline)

**Interfaces — Produces:**

JSON-RPC method `transport.ping`, no params, result:

```json
{ "serverTimeMs": 1756500000000 }
```

`additionalProperties: false`, `required: ["serverTimeMs"]`.

**Acceptance criteria:**

- `transport.ping` is registered on the control plane and answers over the real socket.
- `contracts/transport.ping.schema.json` exists with `additionalProperties: false` and an
  explicit `required`, and `npm run contracts:check` passes.
- A `…_OverTheWireConformsToContract` test validates **the real result off the real
  socket**, not a payload the test built.
- The server sets a read deadline on the connection and refreshes it on every frame it
  reads, so a client that has vanished stops holding its half of the session. Name the
  window as a constant with the reasoning beside it.
- Paired: a client that pings inside the window is never disconnected, across at least
  three windows' worth of idle time.
- **AD-1 holds:** no binary msg-type is allocated and the data plane is untouched.

**Steps:**

1. Write the failing over-the-wire test in `heartbeat_test.go`.
2. `go test ./internal/transport/... -run Ping` — expect method-not-found.
3. Write `heartbeat.go` and register it in `ws.go`.
4. Write the schema; run `npm run contracts:check` from `frontend/`.
5. `go test ./internal/transport/... -run Ping` — green.
6. Add the read-deadline test and its paired healthy-idle case; implement; green.
7. Commit.

**You do not own:** `frontend/src/dispatcher.ts` — the client half is T6. Report the exact
method name and result shape in your completion report so T6 does not guess.

---

### Task 3: the overlay, as a kit component

**Files:**

- Create: `frontend/src/ui/connection-overlay.tsx`
- Create: `frontend/src/styles/components/connection-overlay.css`
- Create: `frontend/src/ui/connection-overlay.test.tsx`
- Modify: `frontend/src/ui/README.md` (one row)
- Modify: `frontend/src/styles/style.css` (the `@import` for the new component CSS)

**Read first:** `frontend/src/ui/README.md` in full, and list `frontend/src/ui/`. If the kit
already answers part of this, extend that rather than forking it.

**Interfaces — Produces:**

```ts
export type ConnectionOverlayState =
  | { kind: 'connecting' }
  | { kind: 'waiting'; nextAttemptInMs: number }
  | { kind: 'blocked'; message: string; remedy: string }
  | { kind: 'online' }

export interface ConnectionOverlayProps {
  state: Accessor<ConnectionOverlayState>
  /** Invoked by Retry. The component decides nothing about when to retry. */
  onRetry: () => void
  /** Minimum visible time from first mount, in ms. */
  minimumVisibleMs?: number
}

export function mountConnectionOverlay(
  host: HTMLElement,
  props: ConnectionOverlayProps,
): { destroy(): void }
```

**Acceptance criteria:**

- Identity class `.ui-connection-overlay`; variance as `data-state`, never as a second
  class name.
- Rendered as a native `<dialog>` with `showModal()`, so it is genuinely top-layer and
  genuinely inert behind — the mechanism `ui/dialog.tsx` already uses. Read that file and
  follow it rather than inventing a second way.
- Composed from kit primitives. Nothing repainted: no `background`, `border`, `color`,
  `font-*`, `padding` or `box-shadow` applied to a borrowed component.
- The logo pulses **only** in `connecting`. In `waiting` and `blocked` it is still. This is
  asserted.
- Retry is present in `waiting` and `blocked`, absent in `connecting`. Asserted for all
  three.
- In `blocked` the component renders the `message` and the `remedy` it was given, verbatim,
  and invents no sentence of its own.
- `minimumVisibleMs` (default 1000) holds the overlay from **first mount** only. A
  transition into `connecting` after it has already been hidden shows immediately, with no
  minimum. Both halves asserted.
- On `online` it hides — after the minimum if that is still running, immediately otherwise.
- A row in `ui/README.md` matching the table's existing grammar.

**Steps:**

1. Write `connection-overlay.test.tsx` first, one case per acceptance bullet.
2. `cd frontend && ./node_modules/.bin/vitest run src/ui/connection-overlay.test.tsx` —
   expect failure.
3. Write the component and its CSS.
4. Re-run the targeted test — green.
5. Type-check with the exact binaries in Global constraints.
6. Add the README row. Commit.

**You do not own:** `frontend/src/main.tsx` (T7 wires it), `frontend/src/dispatcher.ts`.
Ship this component unwired; that is correct and expected.

---

### Task 4: the dispatcher's four states, and the endpoint seam

**Files:**

- Create: `frontend/src/endpoint.ts`
- Modify: `frontend/src/dispatcher.ts`
- Modify: `frontend/src/dispatcher.test.ts`

**Interfaces — Produces:**

```ts
// endpoint.ts
export interface Endpoint {
  host: string
  port: number
  token: string
}

export type EndpointFailureKind =
  | 'profile-unusable'
  | 'server-binary-unusable'
  | 'incompatible-coordinator'
  | 'not-ready'
  | 'no-server'
  | 'token-refused'

export interface EndpointFailure {
  kind: EndpointFailureKind
  message: string
  remedy: string
}

export type EndpointResult =
  { ok: true; endpoint: Endpoint } | { ok: false; failure: EndpointFailure }

/** One attempt to learn where the backend is. Never throws. */
export interface EndpointProvider {
  resolve(): Promise<EndpointResult>
}
```

```ts
// dispatcher.ts
export type ConnectionState =
  | { kind: 'connecting' }
  | { kind: 'online' }
  | { kind: 'waiting'; backoffMs: number }
  | { kind: 'blocked'; failure: EndpointFailure }

class Dispatcher {
  constructor(provider: EndpointProvider)
  get connectionState(): ConnectionState
  onConnectionStateChange(cb: (s: ConnectionState) => void): () => void
  /** Cancel the scheduled wait, reset backoff, attempt now. Single-flight. */
  retryNow(): void
  start(): void // replaces connect(port, host, token)
}
```

**Acceptance criteria:**

- `connect(port, host, token)` is gone. The dispatcher asks its provider on **every**
  attempt, including the first, and connects to what it returns.
- The four states are published, and every transition fires the change event exactly once.
  Including the one no live event produced before: `waiting → connecting` when the timer
  fires.
- A provider returning `{ok: false}` puts the dispatcher in `blocked` carrying that
  failure. A `blocked` dispatcher still schedules nothing on its own; `retryNow()` is what
  moves it.
- **`retryNow()` is single-flight**: called twice while an attempt is in flight it opens
  exactly one socket. Asserted.
- **`ws.onopen` gains the identity guard `if (this.ws !== ws) return`** that the close
  listener already has (bead `nocx-ubszh`). Asserted: a superseded socket's late `open`
  moves no state and fires no `onConnect`.
- The backoff policy is unchanged — 250 ms, ×2, cap 5 s, 50 % jitter. Do not retune it.
- `visibilitychange` and `navigator.onLine` trigger an immediate attempt when the state is
  `waiting`, and do nothing in the other three states. Asserted.
- Paired per rule 3: a provider that succeeds on an ordinary machine reaches `online` and
  stays there.

**Steps:**

1. Write the new `dispatcher.test.ts` cases first — one per acceptance bullet, driving a
   fake `EndpointProvider`.
2. `cd frontend && ./node_modules/.bin/vitest run src/dispatcher.test.ts` — expect failure.
3. Write `endpoint.ts`, then change `dispatcher.ts`.
4. Re-run the targeted test — green.
5. Type-check with the exact binaries. **`main.tsx` will not compile yet** — it still calls
   `connect()`. That is expected and is T7's; report it, do not fix it.
6. Commit.

**You do not own:** `main.tsx`, `main.go`, `endpoint-desktop.ts`, the heartbeat.

---

### Task 5: the binding that re-runs the launcher (needs T1, T4)

**Files:**

- Create: `frontend/src/endpoint-desktop.ts`
- Create: `frontend/src/endpoint-desktop.test.ts`
- Modify: `main.go` (new binding; `ServiceStartup`'s launcher path; `GetWSPort`/`GetWSToken`
  removed)
- Modify: `frontend/vite.dev-view.config.ts` (the dev stand's shim)
- Modify: `bindings/` generated Wails bindings (regenerate, do not hand-edit)

**Interfaces — Consumes:** T1's `coordinator.LaunchFailure` / `FailureKind`;
T4's `EndpointProvider`, `EndpointResult`, `EndpointFailure`.

**Interfaces — Produces:**

```go
// ResolveBackend runs one discovery attempt and reports the outcome.
// It may find a running coordinator, raise one, or replace an incompatible one.
// It decides nothing about WHEN to try again — that is the renderer's (AD-3).
func (w *WailsApp) ResolveBackend() BackendResolution

type BackendResolution struct {
    OK      bool   `json:"ok"`
    Host    string `json:"host"`
    Port    int    `json:"port"`
    Token   string `json:"token"`
    Kind    string `json:"kind"`    // "" when OK
    Message string `json:"message"` // "" when OK
    Remedy  string `json:"remedy"`  // "" when OK
}
```

```ts
// endpoint-desktop.ts
export function createDesktopEndpointProvider(): EndpointProvider
```

**Acceptance criteria:**

- `ResolveBackend` runs the same `launchCoordinator` path `ServiceStartup` runs, so it may
  find, raise or replace. Called twice it does not raise two daemons.
- `ServiceStartup`'s launcher failure **no longer calls `fatal()` and no longer quits.**
  The window opens; the failure is held and returned by the next `ResolveBackend`.
- `GetWSPort` and `GetWSToken` are removed. Nothing reads `w.ws` as a cached endpoint.
- The token still leaves the process only through this binding — never a log line, never
  argv, never the spawned daemon's environment. Asserted by reading, and stated in the
  completion report.
- `frontend/vite.dev-view.config.ts` shims `ResolveBackend` instead of the two getters, so
  `make dev-web` still reaches a backend. **This is not optional** — the dev stand is the
  web client today.
- `createDesktopEndpointProvider().resolve()` never throws: a missing Wails runtime
  resolves to `{ok: false, failure: {kind: 'no-server', …}}`.
- Paired: on an ordinary machine `ResolveBackend` returns `OK` with a non-zero port and a
  non-empty token.

**Steps:**

1. Write the Go test for `ResolveBackend`'s two outcomes and the "does not quit on failure"
   case; write `endpoint-desktop.test.ts` for the mapping and the no-runtime case.
2. Run both targeted suites — expect failure.
3. Implement; regenerate the Wails bindings with the project's own command (do not
   hand-edit `bindings/`).
4. Re-run both targeted suites — green. Type-check the frontend.
5. Update the dev-view shim; state in your report that you did.
6. Commit.

**You do not own:** `frontend/src/dispatcher.ts`, `frontend/src/main.tsx`,
`internal/coordinator/*`.

---

### Task 6: the heartbeat's client half (needs T2, T4)

**Files:**

- Modify: `frontend/src/dispatcher.ts`
- Modify: `frontend/src/dispatcher.test.ts`

**Interfaces — Consumes:** T2's `transport.ping` and its result shape; T4's state machine.

**Acceptance criteria:**

- The dispatcher pings when the socket has been idle for the window, and closes the socket
  itself when no answer arrives inside the answer window. Both windows are named constants
  with the reasoning beside them.
- After that self-close, everything downstream takes the **existing** `close` path — the
  same rejections, the same state transition, the same scheduled retry. There is no second
  disconnect path. Asserted.
- **A busy socket is never pinged.** Traffic in either direction resets the idle timer.
- Paired per rule 3: an idle but healthy socket survives at least three windows.
- **No per-call RPC timeout is added.** A long legitimate call (an upload, a streamed
  answer) must not be cancelled by this work. Stated in the report.

**Steps:**

1. Write the failing cases: half-open detection, healthy idle, busy-not-pinged,
   "self-close reaches the same close path".
2. `cd frontend && ./node_modules/.bin/vitest run src/dispatcher.test.ts` — expect failure.
3. Implement.
4. Re-run — green, **including every case T4 added**. Type-check.
5. Commit.

**You do not own:** anything in Go.

---

### Task 7: the composition root, split by lifetime (needs T3, T4, T5)

**Files:**

- Modify: `frontend/src/main.tsx`
- Delete: `frontend/src/connection-notice.tsx`, `frontend/src/connection-notice.test.tsx`,
  `frontend/src/styles/surfaces/connection-notice.css`
- Modify: `frontend/src/styles/style.css` (remove the `@import`)

**Acceptance criteria:**

- **Nothing is awaited before the application exists.** `main()` builds theme, platform,
  the `App` shell, the `Dispatcher`, every client, the global key handlers, the observers,
  the update notice and the connection overlay **before** any socket attempt. With no
  backend the window is populated and the overlay is on it.
- The composition root is **not re-entered** on reconnect. Built on `online` and torn down
  on disconnect: the sidebar and any open backend-driven dialog. Never torn down: the tab
  bar, the tab strip, the panes and their xterm instances.
- **The disposal contract is asserted as an interval**, not a moment: from before the first
  teardown until after the Nth remount, the Dispatcher's subscriber counts and the
  observers' registrations return to their baseline. N ≥ 3.
- `vault.unlockRequest`, `connections.passwordRequest` and the host-key queue close on
  disconnect.
- The activity bar's selected view is restored after a remount.
- `connection-notice.*` is gone, its `@import` is gone, and **no inline `ws closed` or
  `not connected` text is reachable in the document during an outage.**
- Paired: with a backend present, startup reaches a usable app and the overlay leaves.

**Steps:**

1. Write the disposal-contract test first — it is the one that guards `nocx-bwzg`.
2. Run it — expect failure.
3. Split `main()` by lifetime; wire the overlay to the dispatcher's state and `retryNow`.
4. Delete the notice and its import.
5. Targeted tests green; type-check; `cd frontend && npm run lint:dead-exports` (this one
   is cheap and catches the deletion's leftovers).
6. Commit.

---

### Task 8: the happy path, watched end to end (needs T7)

**Files:**

- Create: `e2e/connection-overlay.spec.ts`

**Acceptance criteria** — one spec per line, each asserting what a person can do:

1. Start with **no backend**, then start one without reloading: the overlay is present from
   first paint and the application becomes usable. (Fails on `main` today.)
2. Kill the backend with a live terminal tab: the overlay appears, and no `ws closed` /
   `not connected` text is reachable anywhere in the document during the outage.
3. Kill the daemon and let a new one be raised: the client recovers without a restart,
   reaching a **different** port than it started with.
4. Restart the same daemon: the overlay leaves, the sidebar answers, and the terminal's
   scrollback is byte-identical to before the drop.
5. Retry acts in `waiting`; it is absent during `connecting`.
6. A launch failing with each `FailureKind`: the window opens, the overlay names the kind's
   message and remedy, and the process is still alive.
7. Disconnect with a backend-driven dialog open: it closes, and the overlay is the
   interactive surface.

**A test may not depend on timing.** Wait on an observable state change — a DOM state, a
record, a frame — never on a duration.

**Steps:**

1. Write the specs.
2. Run **only your own file**: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/connection-overlay.spec.ts`.
3. Commit. The coordinator runs the full suite.

---

## Amendment, 2026-08-30: two planning errors, both the same shape

Both were found by workers blocking rather than by review, both cost a round trip, and both
are the lesson `AGENTS.md` already records as `nocx-z7s6` — **a task lands together with
the wiring that makes its output reachable, or its commit cannot pass the gate at all. The
gate is the hook, not the brief, so a worker cannot be briefed out of it.** Written down
here because the plan above is what produced them.

**T4's blast radius was measured wrong.** The task said removing `Dispatcher.connect` would
break `main.tsx` and that this was expected and T7's. It breaks **25 files**, because
`WSClient.connect(port, host, token)` (`frontend/src/ipc.ts:759`) is a second public layer
over the dispatcher and is what every test actually calls. And `pre-commit` type-checks the
whole frontend, so "my files are green, someone else's are red" is not a state anything can
be committed from. The worker blocked, correctly, rather than reaching for `--no-verify`.

Corrected: **an API change owns its call sites.** T4's scope now includes `ipc.ts`, every
test that constructs a `Dispatcher`, and a `fixedEndpoint()` helper exported from
`endpoint.ts` that makes each of those 25 sites a one-line change. It also makes the one
minimal edit in `main.tsx` needed to compile — a temporary bridge, clearly commented, with
the real lifetime split still T7's.

**T2 and T6 cannot be separate tasks.** The plan split the `transport.ping` contract from
its only consumer. That makes `frontend/src/generated/transport.ping.ts` dead on arrival,
and the dead-export ratchet fails the commit. There is no deferral: `check-dead-exports.mjs`
states the baseline may only **shrink**, and `update-dead-exports-baseline.mjs` refuses to
write one that grows. Deleting the generated file is not an escape either — `contracts:check`
requires the schema and its type as a pair.

Corrected: **T6 is folded into T2** (bead `nocx-kgfe7` closed into `nocx-x6ggy`), which now
delivers both halves in one commit and waits on T4 for the dispatcher it must attach to.

**The instrument, which is cheaper than estimating better.** Both errors above were
predictions where a count was available. So: **when a brief removes or renames a symbol, the
brief states the call-site count and how it was counted.** Two seconds, mechanical, and it
would have said 106 rather than 2:

```bash
grep -rn 'Dispatcher.connect\|\.connect(' --include=*.ts --include=*.tsx frontend/src | wc -l
```

The point is not the number. It is that without it a worker discovers mid-flight that no
commit can pass the hook, which costs a round trip and cannot be briefed away.

**The general rule for this plan, and for the next one:** a contract lands with its
consumer, a package lands with its caller, and an API change lands with its call sites. If
a task's output has no consumer inside the task, the task boundary is wrong — check it
against the ratchets before dispatching, not after.

### Amendment, 2026-08-31: the third one, and it was in an acceptance criterion

T8's criterion 4 read "Restart the same daemon: ... the terminal's scrollback is
byte-identical to before the drop." The worker blocked on it rather than weakening it,
which is the right outcome and is what the brief asked for. The criterion is
unsatisfiable as written, and checking the code says so in one read:
`internal/transport/ws_reclaim.go:238` and `internal/session/lineage.go:59` refuse a
claim that names another backend instance, because "a record out of a previous backend
can never resolve to a current session". `VaultBackend.restart()` IS another instance —
new `InstanceID`, new port, new token, and the previous daemon's PTYs died with it. So
"restart the same daemon" contradicts itself: byte-identical scrollback across a backend
restart would require sessions to survive one, which nocx deliberately does not do.

What the criterion is worth is the REPLAY RING (AD-9, `internal/transport/ring.go`),
and the ring's condition is the opposite one — **the socket dies while the daemon
lives**. Corrected: criterion 4 is now a fault-proxy `cut()` → `pass()` against a live
daemon, asserting the scrollback carries both what preceded the outage AND what the
daemon wrote during it. The stronger half is the point: the ring proves nothing was
lost, not merely that nothing was erased. The daemon restart keeps criterion 3, which
gains the assertion that the refused claim never reaches the person as raw text
(`Invalid params`, `different backend instance`) — the exact place an RPC error would
leak into the UI against the owner's ban on inline errors.

**Same shape as the two errors above, one layer earlier.** T4 and T2 were task
boundaries written from what sounded right; this was an ACCEPTANCE CRITERION written the
same way, in the spec, and it survived the plan, the bead and the brief because each
step copied it forward. The instrument generalises: **a criterion that names a mechanism
states where that mechanism is implemented, and the plan checks it there before the
criterion is written down.** Two minutes in `ws_reclaim.go` would have caught this
before a worker spent a container run on it.

### Two more, found by the gate rather than by a review

**A task that registers a wire method names and counts BOTH halves of the contract.**
The merged gate came back red on `transport.ping has no valid params probes`. T2 had
shipped `contracts/transport.ping.params.schema.json` and no entry in the probe table
in `internal/transport/params_contract_test.go` — a file its task never named, so a
worker running only the tests for the files it changed could not see the failure, and
did not. Rule 5 in AGENTS.md says the wire is a party to the contract; what this adds is
that the contract has two artefacts and a brief that names one of them silently descopes
the other. The probe table is the enforcement, and it is not co-located with the schema
on purpose — so the brief has to carry the second path.

**Adopting a kit component adopts its whole behaviour, including what it does to the
rest of the page.** The overlay was built on `Dialog`, which is correct — the kit had
the component and rule 1 of the UI kit says import it. What the plan never asked was
what `showModal()` does to everything underneath: the background goes inert, so a
`focus()` call on a pane beneath it is discarded, and on close `dialog.tsx:259-260`
restores focus to whatever was focused BEFORE the dialog opened, which at startup is
`body`. Composed with the deliberate one-second minimum visibility and a first pane
that opens inside `onConnect`, that is a terminal which accepts no keystrokes after
launch until the person clicks (`nocx-0c7qz.1`). Every unit was right and the
composition was not — the same shape rule 2 exists for, and the reason the epic's
end-to-end check is what found it, at the cost of a full container run.

The instrument: **when a surface adopts a kit component that takes over the page —
modality, inertness, focus capture, scroll locking — the plan states what that surface
hands back, and to whom, when the component goes away.** For a modal the answer is
almost never "nothing", because the component's own restore targets the element that
opened it, and a surface raised by the application rather than by a click has no such
element.

### Revised ordering

```
wave 1:  T1 ✅ merged 929ebb1d   T3 ✅ merged 8cc22fca   T4 (in flight, critical path)
wave 2:  T5 (needs T1 ✅, T4)    T2+T6 (needs T4, work preserved, waiting)
wave 3:  T7 (needs T3 ✅, T4, T5)  →  T8
```
