# ADR-0026: the control plane runs off the read loop under a bound

- **Status:** accepted (2026-08-08)
- **Taken from** `nocx-sfv6` — the executor contract (`internal/transport/control`),
  the capability layer (`internal/capability`), the saturation wire surface
  (`internal/transport/ws_saturation.go`)
- **Related:** [ADR-0001](0001-xterm-js-as-vt-frontend.md) (the WebSocket framing),
  [ADR-0004](0004-input-ownership-and-editor-abstraction.md) (input ownership), and
  AD-9 (the replay ring) as documented in `docs/architecture.md` and
  `internal/transport/ring.go`

## Context

The control plane always had an implicit FIFO: every JSON-RPC method ran in turn on
the WebSocket read loop, the one goroutine per connection that also forwards every
keystroke to every session's PTY on that socket. A slow method — a 30-second SSH
probe, a native file dialog that stays open for minutes — froze every tab on the
connection for its duration. The guarantee was real and unstated: everything
eventually ran, in arrival order, one at a time. Nothing named the resource the FIFO
was consuming, and nothing bounded it.

This epic removes the FIFO. The read loop now performs bounded validation and a
non-blocking submission attempt; the work runs on worker goroutines under explicit
bounds, or is refused with a stable wire shape. A guarantee that disappears without a
document is one the next person reinstates by accident, so this ADR is the record of
the scheduling contract as it now is — and because a written rule nothing enforces
decays, the enforcement at the bottom ships with it.

## Decision

### Scope

This ADR governs **JSON-RPC control-plane execution only**. AD-1 framing (raw PTY
bytes ride the binary data plane; typed ledger facts may cross the control plane
after the fact) and AD-6 byte ownership (the renderer owns the VT state derived from
the byte stream; the backend stays byte-blind) are untouched. Nothing here changes
what bytes cross the wire, only who runs the work that reacts to them.

### 1. The read-loop invariant

Between the moment `ReadMessage` returns and the moment the next `ReadMessage`
begins, `handleControlFrame` performs exactly four things, in order
(`internal/transport/ws.go`):

1. **a bounded envelope parse** — `decodeEnvelope` walks the frame's top-level
   members with a stdlib tokenizer capped at `envelopeScanCap` (4 KiB) and stops the
   moment `method` is decoded. Params and everything after are never tokenized, so a
   huge frame's size cannot make this step expensive — it just exhausts the cap and
   is refused;
2. **validation** — protocol checks (`jsonrpc == "2.0"`, non-empty method), the
   per-method params budget (refusal by length _before_ any full decode), the
   envelope/full-decode method agreement check (a repeated top-level member could
   otherwise take one method's budget and dispatch as another), and the full
   `json.Unmarshal`, now cheap because the frame is within budget;
3. **ingress-critical execution** — the closed set below runs inline via
   `control.ImmediateSubmission`;
4. **a non-blocking submission attempt** — `TrySubmit` on the method's registered
   submission. `TrySubmit` returns a `*Rejection` value or hands the work off; it
   never waits for a slot, because the read loop is the caller.

The invariant has two ends: from before `ReadMessage` returns until the next
`ReadMessage` begins, the loop spends only bounded, small work, and it never blocks
on admission. The budgets exist so the loop cannot be made to spend real work on a
hostile frame, and the loop survives every refusal to read the next frame
immediately.

### 2. The closed ingress-critical set

Three methods run inline on the read loop, and the set is validated at server
construction in both directions (`internal/transport/registration.go`): `ack`,
`vault.unlockResolved`, `connections.passwordResolved`. `buildMethodSpecs` rejects a
registration that pairs an immediate disposition with any other method, and one that
pairs a non-immediate disposition with a member — a wrong claim fails the server
build, never a socket at runtime. The set is closed deliberately: a handler that
wrongly claims immediate recreates the original bug (a blocking handler on the read
loop freezes every tab).

The most work a member may do:

- `ack` is ring trimming (AD-9 credit): bounded bookkeeping whose delay would close
  the credit window;
- the two resolvers unmarshal a `budgetTiny` (1 KiB) frame, look up a pending ask by
  server-assigned request id, signal its channel, and answer.

**Why `RequestUnlock`/`RequestConnectionPassword` can never queue.** The asks block
until their resolution arrives over the same socket the read loop consumes. A
resolution queued behind a full lane would deadlock the ask: the asking task
goroutine waits on a channel that only a frame the read loop has stopped reading can
fill. The resolver methods are therefore ingress-critical by construction, and the
composition root registers them with `ImmediateSubmission` — the frame that answers
an ask is the one frame the loop must never defer.

### 3. The resolver ordering invariant

A resolution reaches its waiter before any potentially blocking write.
`handleUnlockResolved` consumes the pending ask, sends the resolution into the
waiter's channel — buffered at registration, so the send never blocks — and only
then enqueues the JSON-RPC response. The waiter therefore unblocks, and the asking
task can proceed, before any write that could stall is attempted.

### 4. Two kinds of admission

There are two admission classes, and the difference between them is the subtlest
thing in this design, learned the hard way:

- **The ordinary lane refuses instantly.** `control.NewSemaphore`'s `TryAcquire` is a
  non-blocking channel send; a full lane returns a `*Rejection` immediately. This is
  correct because it is _real saturation_ — the lane is the execution bound, and the
  read loop is the caller — and because the caller must never block.
- **A domain conflict waits, bounded.** `control.NewWaitingSemaphore` (behind
  `capability.Gate`) queues a conflicting request up to a wait timeout and a
  queue-depth bound. A conflict is a serialisation point, not an overload: two
  operations on the config domain are a queue of length two, and the second may
  proceed once the first releases.

What happened when the distinction was missing: the first revision used the same
instant-refusal semaphore for the domain gates. A handler enqueues its response and
releases the gate a moment later, so a sequential client's very next request —
arriving while the gate is still held — was told "Control plane busy" for doing
nothing wrong. Invisible on a fast host, where the response beat the next request,
and reproducible under load. The waiting gate exists to bridge exactly that
response/release tail window; only exhausting a bound is a refusal.

The same window later reappeared on the **native picker**, which had been left in
the instant-refusal class because "a second picker must never stack over the first"
reads like an overload bound. It is not one: a picker is a serialisation point, and
one at a time is a queue of length two. Held instantly-refusing, it refused its own
tail — `dialog.openFile` sorts immediately before `dialog.openFileForUpload`, so a
sweep that calls every method in order was told "Control plane busy" and the picker
never opened (`nocx-9le.8.2`). The gate is now a waiting gate acquired inside the
task, exactly like a domain gate; refusal is what a picker a human left open still
produces, once the wait bound runs out.

### 5. The bounds

All numbers are named at the composition root or its defaults — the same way the D14
bounds are named there, so a reviewer looks in one place
(`internal/app/app.go`, `internal/transport/ws.go`):

| bound                         | value                                                                               | where                                     |
| ----------------------------- | ----------------------------------------------------------------------------------- | ----------------------------------------- |
| per-connection outbound queue | `outbound.DefaultQueueDepth` = 256 frames                                           | `internal/transport/outbound/outbound.go` |
| reserved response queue       | `outbound.DefaultResponseQueueDepth` = 64 frames                                    | same                                      |
| per-write deadline            | `outbound.DefaultWriteDeadline` = 10 s                                              | same                                      |
| process-wide outbound         | `outboundBudgetBytes` = 32 MiB queued bytes                                         | `internal/transport/ws.go`                |
| ordinary lane capacity        | `DefaultControlLaneCapacity` = 8 concurrent tasks                                   | `ws.go`, named at the composition root    |
| conflict wait timeout         | `DefaultDomainConflictWaitTimeout` = 1 s per waiter                                 | `ws.go`, named at the composition root    |
| conflict queue bound          | `DefaultDomainMaxQueue` = 8 registered waiters per gate; beyond, refusal is instant | `ws.go`                                   |
| per-operation in-flight bound | `DefaultDomainQueueDepth` = 8 tasks (waiting or running), refused at submit time    | `ws.go`                                   |
| probe / agent-probe admission | capacity one, composed with the lane, acquired at submit (instant refusal)          | `ws.go` `buildControlPlane`               |
| native picker (dialog) gate   | capacity one, WAITING, composed with the lane, acquired inside the task             | `ws.go` `buildControlPlane`               |
| frame ceiling                 | `wsReadLimit` = 16 MiB (protocol-layer, close code 1009)                            | `ws.go`                                   |
| envelope scan cap             | `envelopeScanCap` = 4 KiB                                                           | `ws.go`                                   |
| params budgets                | `budgetTiny` 1 KiB / `budgetDefault` 64 KiB / `budgetDocument` 8 MiB                | `ws.go`                                   |
| shutdown drain                | `defaultControlDrainTimeout` = 5 s                                                  | `ws_control.go`                           |

### 6. Saturation

Refusal is a new outcome for the user — before this epic every operation eventually
ran, because everything waited. It has a stable wire shape
(`internal/transport/ws_saturation.go`, `contracts/control.saturated.schema.json`):

- a refused **request** (has an id) answers JSON-RPC error **-32004** (in the
  server-error range, deliberately not -32001 which already means vault-sealed),
  message "Control plane busy", with a fixed `data` payload: `reason` = the literal
  `control-saturated` (the renderer's discriminator — never the rejection's free
  text), `scope` = the saturated admission's name (closed server vocabulary, set at
  the composition root), `retryable` = true, `retryAfterMs` = the server's suggested
  wait (0 = no hint). The payload builder takes _only_ the `*control.Rejection` —
  no request parameter can reach the wire, because any control frame may carry a
  secret;
- a refused **notification** (no id) has no response to carry the error, so the
  server emits the `control.saturated` server-to-client notification instead,
  rate-limited to one per (method class, scope) per second so a burst of refused
  notifications cannot flood the wire, carrying only the coarse method class and the
  scope;
- the renderer surfaces both centrally: the dispatcher matches the fixed `reason`
  and raises a deduplicated "terminal is busy" toast (one per 10 s episode, danger
  level, sticky), with **no calling surface opting in** — a refused action must be
  visible in the product, not only in a log.

Retry is client-driven: the payload says the failure is transient and suggests a
wait; individual surfaces may offer retry or disable an action, and the toast is
what stops silence.

### 7. There is no response-completion FIFO

Say it plainly: **responses are not guaranteed to leave in request order.** Requests
arrive in order and are submitted in order, but admitted work runs concurrently on
the lane, so a fast method's response can overtake a slow one's, and the per-
connection outbound queue serialises responses in _completion_ order. Ordering
guarantees that do exist are narrower and named: same-socket arrival order is
preserved only where it is load-bearing — the resize/close lane's coalescing with a
terminal close gate, and the ordered submission's single worker. Nothing else may
rely on response ordering, and the renderer must not.

### 8. Conflicts and atomicity

Domain operations acquire their conflict gates in one canonical order — `config,
vault, content, session, git, filesystem` (`capability.CanonicalOrder`) — enforced
by the operation constructors, which take the gates as separate parameters and
compose them in that order, so overlapping operations cannot acquire in opposite
orders. The gates are capacity-1 whole-domain exclusions today (conservative grain;
per-id grain is a deliberate, deferred refinement inside the capability package).

The gates live on the `WSServer`, not on a connection, so **conflict enforcement is
cross-connection**: two connections touching the config domain exclude each other
exactly as two requests on one connection do.

Where an operation spans several stores, the operation owns the sequencing — never
the handler. `backup.restore` runs through `BackupOperation`, which has a documented
prepared/committed journal boundary. Before it, cancellation changes nothing on disk.
From it on, the domain owns completion or rollback, and the closing event of the
interval is the error return: when restore returns an error, every store is at a
recoverable generation.

### 9. Namespace FIFO was considered and rejected

A per-domain FIFO is an attractive false guarantee, because the invariants cross
domains. `backup.restore` touches profiles, groups and settings inside one commit
interval; `open` crosses session, profile, vault and sshConfig. Serialising per
domain would order work that has no ordering relationship, and the work that genuinely
needs ordering — conflicts — is already ordered by the domain gates. A namespace FIFO
would also reintroduce head-of-line blocking one level down: a slow `git` operation
would stall unrelated `files` work that shares no resource. Rejected.

### 10. Context owners

Every context used by control work has a named owner and a named closing event:

- **request-scoped** — a task's context derives from the connection context (the
  read loop's), so a disconnect cancels running probe/dialog work. The closing event
  is the task's return, cancelled or not, which is also what releases the admission
  permit;
- **server/session-owned** — the resize lane's context derives from `Background`: a
  session is resized by whichever connection is attached (AD-9), so the lane must
  not die with any one connection. Its closing event is `closeLane`'s admission, not
  a disconnect. The PTY pump and the replay ring likewise outlive any single
  WebSocket;
- **domain-owned commit interval** — from `RestoreImport`'s commit point to its
  return, the operation owns completion; `rollbackConfig` deliberately never
  consults `ctx`, because a cancellation must not be able to strand the operation
  between generations.

### 11. Disconnect behaviour

- **Queued** work (resize in the session lane) is not cancelled by a disconnect: the
  lane is per-session, its context is session-owned, and the session survives the
  connection. Close is terminal for the lane and preempts a stuck resize — the one
  operation that can tear down a dead channel never queues behind a blocked resize.
- **Running** admission-backed work (probes, dialogs) is cancelled: the task context
  derives from the connection, and the permit is released when the task returns,
  cancelled or not — that is what frees the capacity-one slot for the next
  connection.
- **Native-dialog work** is the one asymmetric case: a cancel-aware adapter may
  dismiss its dialog on `ctx.Done`, but a non-cooperative adapter (the real Wails
  runtime cannot cancel a shown picker) keeps the admission permit until it actually
  returns, and that held permit is what refuses a second dialog from any connection.
  The transport never assumes a prompt return from a cancelled context, and an
  adapter never assumes its context will be cancelled at all.

### 12. Two connections

Two connections have no transport ordering relationship: each has its own read loop
and its own outbound queue, and nothing orders their submissions. What they do share
is domain conflict enforcement (item 8), the process-wide outbound budget, and the
ask broadcast (item 16).

### 13. Outbound is bounded separately from admission

Admission bounds how much work runs; it does not solve a blocked socket write. The
outbound side is a bounded queue per connection plus one pump goroutine per
connection that is the only writer to the socket. The raw `*websocket.Conn` lives in
package `outbound` and never leaves it, so reaching past the queue is not expressible
from the transport — not merely discouraged. A handler's only way to respond is a
non-blocking enqueue (`TryResult`/`TryError`/`TryNotify` on its `Responder`), so a
stuck renderer can delay the read loop by no more than one channel send, and a
connection whose queue overflows is marked outbound-stalled and told through a
reserved overload slot (`outbound.stalled`). The two bounds are independent, and
both are needed.

Responses and notifications are not the same class of frame, and the overflow
consequence differs deliberately. A notification is refreshable state: dropping it
is safe, so the stall notice — "reconnect and resync" — is the honest answer to a
saturated refreshable queue. A response is the other half of a promise: the caller
correlates by request id and waits forever if it is dropped, so a stall notice
announcing that the answer was thrown away is the same silence. Responses therefore
get reserved capacity (`outbound.DefaultResponseQueueDepth` = 64 frames, sized
against the lane's 8 concurrent tasks) that the data plane cannot consume; the pump
drains them ahead of data; and a response that cannot be queued even there closes
the connection, so the caller's promise settles on disconnect instead of hanging.
Silence is the one outcome that must not survive (nocx-sfv6).

### 14. Observability

Admission names exist "for metrics only" (`control.Admission.Name`) and no
implementation may branch on them (AD-8). Refusals are observable on the wire per
request (-32004), and the `control.saturated` notification carries the method class
and scope — both server vocabulary, never request data, because any control frame
may carry a secret, and the payload builder's signature admits no request.

The gap, stated plainly: the four signals the executor design names — active work,
admission rejections as counters, duration by non-secret method name, cancellations
— are **not yet instrumented** as metrics or logs anywhere in this tree. The wire
surface above is the observability that exists today, plus the shutdown abandonment
warning. The `Name()` plumbing is in place for the counters; the instrumentation is
a deliberate follow-up, and this paragraph is what the next person finds instead of
a claim that it exists. No parameters may ever be logged, whatever is added.

### 15. Shutdown

`Stop` stops accepting new connections, then cancels every in-flight admission-
backed task and waits for it to drain, bounded at `controlDrainTimeout` (5 s). Work
that ignores cancellation is abandoned at that bound — the forced-abandonment policy
for work outside a commit interval: a dependency that ignores cancellation must not
hold shutdown hostage past the documented maximum. Domain commit intervals restore
their invariants first, because the drain happens after cancel and the operations
own their rollback. `Stop` therefore terminates within the documented maximum
against any dependency, cooperative or not.

### 16. Late resolver replies

A resolution for an unknown request id answers "Unknown request id" (-32602) and
cannot affect a later ask. The broker's `consume` removes the pending ask on first
resolution, so the second resolution of an already-answered ask _is_ the unknown-id
case; a dropped ask (no client connected, context done) is removed so a late
resolution cannot wake a waiter nobody is listening to.

Asks broadcast to every connected client, and **first-consumer-wins is deliberate**:
whichever connected renderer answers `vault.unlockRequest` /
`connections.passwordRequest` resolves the ask. What the broker enforces is that
there IS only one answer: `consume` removes the pending ask on first resolution, so
the second resolution is the unknown-id case above, never a second wake. What is
NOT enforced anywhere is which renderer should answer — no priority, no routing, no
ownership — and the property has no test pinning it; it is the shape of the broker,
written here for the first time. The product relies on it: the window the user
unlocks in answers.

## Rationale

- **Why admission is a value, not a wait.** `TrySubmit`/`TryAcquire` never block
  because the read loop calls them; refusal is a value the caller answers with. Bounded
  waiting exists only where the caller is a task goroutine — the operation's `Run` —
  which is exactly why the two admission classes of item 4 differ.
- **Why the numbers are what they are.** Lane capacity 8 lets non-conflicting long
  operations genuinely overlap (a tabby import beside a git status) while a burst of
  control requests cannot spawn unbounded goroutines. The conflict wait of 1 s
  bridges the microseconds-wide response/release tail with generous headroom while
  staying perceptually short when a gate is held by a genuinely long operation.
  Queue 8 and depth 8 refuse floods at the bound instead of piling them up. The
  process-wide 32 MiB outbound budget tolerates roughly a dozen saturated
  connections before the shared budget itself trips the stall policy.
- **Why conflict gates are acquired before the execution permit.** The composite
  order (conflict, then lane) means refused or waiting conflict work never occupies a
  worker permit; otherwise conflicting requests would fill every worker and
  unrelated work would stall behind them.
- **Why immediate work is a closed, validated set rather than a convention.** The
  original bug was one blocking handler on the read loop; the regression that
  reintroduces it is one method claiming immediate for the wrong reason. Validation
  at construction makes that a build failure, not a frozen socket.

## Consequences

- Every method now declares, at server construction, whether it runs inline, on the
  lane, or on its own resource admission — and the ingress-critical set is enforced
  in both directions.
- Refusal is a user-visible outcome with a schema, a renderer toast, and a
  rate-limited notification — no calling surface opts in, and none can silence it.
- A handler that spawns its own goroutine escapes context, admission, conflict
  ownership and shutdown accounting; the enforcement below forbids it.
- AD-1 framing and AD-6 byte ownership are untouched: this contract is about who
  runs control work and under what bound, never about what crosses the wire.

## Enforcement

Prose is not a gate. Three checks ship with this document:

1. **Package-boundary proof** (`internal/transport/ws_boundary_test.go`, run in
   `package transport_test`). The compiler enforces the first half: `wsConn` is
   unexported and cannot be named outside the package — the test's own package is
   the evidence. The test makes the second half checkable rather than inspected: it
   scans the package's export surface and asserts no exported symbol references
   `wsConn` or `*websocket.Conn` (no getter, no exported factory can hand the
   connection out), and asserts by reflection that the one write seam handlers do
   receive — `Responder` — exposes exactly the non-blocking `TryResult`/`TryError`/
   `TryNotify` trio. A domain service not given to a handler is unreachable for the
   same structural reason at the capability layer, proven externally in
   `internal/capability/capability_test.go` (`TestConfigOperationCannotReachVault`,
   `TestServiceCannotEscapeCallback`, and the reflection method-set test).
2. **No `go` statements in control-handler code**
   (`.githooks/check-control-goroutines.mjs`, wired into the pre-commit hook beside
   the deadcode ratchet). Go cannot remove the keyword, so the available structural
   check is a ratchet: the checker scans the non-test Go files of the control-handler
   packages (`internal/transport`, `internal/transport/control`,
   `internal/capability`) and fails on any `go` statement outside the committed
   baseline. The baseline names the thirteen legitimate infrastructure spawn sites
   (pumps, the read loop, ring workers, the admission runner, the stored-forward
   replay) with their justifications; a new spawn — from a handler or anywhere else —
   is a review decision that shows up in the diff, and a baseline entry that stops
   matching any spawn fails too, so the list cannot rot into a permission ledger. An
   update script rewrites the baseline, preserving justifications and stamping new
   entries as un-justified until a human fills them in.
3. **A waiting admission cannot reach a submission.** `control.NewWaitingSemaphore`
   may block, and the read loop must never call one. Rather than a test that
   enumerates today's wiring, the control package now distinguishes the classes by
   type: `control.NonblockingAdmission` — sealed with an unexported marker method,
   satisfied only by `NewSemaphore` and `NewCompositeNonblocking` — is the only type
   `NewBoundedSubmission` accepts. A waiting admission, or a composite containing
   one, does not satisfy it, so the miswiring does not compile. The type distinction
   was chosen over a reachability test because it is strictly stronger: a test
   proves a snapshot of the wiring; the type system forbids the class. The cost is
   deliberate and named: the submission-path admission vocabulary is closed to the
   package, so the AD-8 third-implementation proof (control_test.go →
   third_admission_test.go, in the package's own test namespace, where the
   marker is implementable) moved with it — production source stays untouched
   and the executor still has no switch on admission kinds.
