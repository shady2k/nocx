# ADR-8eej3e66 — Every operation is catalogued with its lifetime, and busy is not an answer

- **Status:** proposed
- **Date:** 2026-11-05
- **Taken from** `nocx-3an1s.1.1`, the owner's decisions of 2026-11-05
- **Supersedes parts of ADR-0026**, mapped under "What happens to ADR-0026" below. ADR-0032's
  unlock fence stands, and this record says how it survives.

## Context and problem

A person working in two local tabs, with the Files panel never expanded, is told "The terminal
is busy — a file operation is still running. Try again in a moment." The owner's decisions: this
sentence must not exist in principle; no operation may exist without a timeout, and that is fixed
at the level of the contract; every operation is registered in one catalogue, the way settings
are; and what is waiting is shown in the existing Operations surface.

The sentence exists because of a shape, not a bug. The backend is asynchronous end to end, and yet
seven domain gates (config, vault, content, session, git, filesystem, api) each admit one operation
for the whole server, make the next wait one second and refuse it. The gate is held across the
whole provider call: a directory walk, a far SFTP round trip, `git.commit` with its hooks. The
second bound, `operationQueue`, refuses at eight tasks per group, and the `control` lane refuses
instantly at eight. The one-second wait was chosen for a microsecond window, the tail of a task
that has enqueued its response (`DefaultDomainConflictWaitTimeout`).

Read at `511157d49`:

- No method needs whole-domain exclusion. Reads have no order to keep. Writes need order on the
  resource they change: `files.watch` per binding, five git mutators per checkout, session
  operations per session, collection writes per collection.
- `backup.restore` and `vault.reset`, with the coherent read of `backup.create`, have a
  reader/writer shape. `PatchProfile` is load-modify-write, so config parallelism waits for an
  audit of each store.
- The gates carry an undocumented effect. Capacity one limits how many lane permits a hung domain
  holds. A blocking `readdir` on a hung mount cannot be interrupted, so removing the gates alone
  lets eight hung calls take the whole lane.
- There is no mandatory, complete operation-level lifetime contract. Some work has bounds: assistant
  probes a 30 s context deadline, local listings a 10 s elapsed bound that cannot interrupt
  `ReadDir`. Others have none: `git.commit` and its hooks, `files.close`'s drain, the renderer's
  ordinary call.
- The Files store runs with its panel collapsed, and a background call's refusal raises the same
  danger toast as a person's.

## Decision drivers

1. A person's action is never refused because other work is running.
2. Background work does not compete with a person and does not interrupt them.
3. A slow or hung dependency affects only the work that depends on it.
4. No operation exists without a stated, finite lifetime, by contract.
5. What was truly guaranteed stays guaranteed: the read loop never blocks, resources are bounded,
   no two writers race in a store, order holds within a session, unlock does not self-deadlock.
6. Everything waiting or expired is visible in the log and in Copy diagnostics.

## Considered options

**A. Keep the domain gates; waits have no timeout.** Rejected: it breaks driver 4 and keeps
head-of-line blocking, so one hung mount stalls every file call silently.

**B. Remove the gates; keep only the lane.** Rejected: it loses the protection of
read-modify-write stores, and one failed dependency takes the whole lane.

**C. Raise the capacities.** Rejected, on the owner's question. A request behind a capacity-one
gate is not helped by a larger lane, and a larger gate weakens exclusion where it is real. A permit
is cheap (a token and a goroutine), so this is not a resource question. What costs is the work
behind it: a git process, an SSH channel.

**D. A catalogue of operations, ordered per resource, isolated per failure domain.** Chosen. The
named patterns are a single-site registry, keyed serial execution, shared/exclusive barriers,
bulkheads with circuit breakers, and credit-based flow control.

## Decision

### 1. One catalogue, declared at one site

Settings have one declaration site, and registration follows from it. Operations do the same.
`internal/operation/catalog` is a transport-independent Go table, and it is the only place an
operation's lifetime is written. It imports nothing from `internal/transport`, so startup recovery
and other work that precedes `buildMethodSpecs` can use it.

Everything else is a projection and is never written by hand:

- `methodSpec` holds a typed handle from the table and takes its submission from it. A JSON-RPC
  method with no entry does not compile. An entry whose handle nobody references is refused by a
  static check, because construction cannot prove an arbitrary function is a live caller.
- `contracts/operations.json` is generated from the table, validated by a schema with
  `additionalProperties: false` and every field required. `gen-openrpc.mjs` reads it to add
  `x-nocx-operation` to each method, and the renderer's deadlines, origins and sentences are
  generated from it. A test compares the complete metadata of table and manifest, not only the ids.

**Why not a timeout per call site.** A rule stated at each call site is a rule somebody forgets at
the next one. A consumer that reads the declaration cannot disagree with it.

### 2. What is declared, and what is not

A **declaration** describes an operation, and holds no optional field and no zero value meaning
"unlimited":

| Field          | Says                                                                                                                                 |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------ |
| `id`           | The JSON-RPC method, or an internal operation's name                                                                                 |
| `phases`       | Ordered phases, each with its own `resources` and `dependencies` sets. A phase may hold nothing                                      |
| `resources`    | Keys to order on: `session`, `binding`, `checkout`, `store:<name>`, `collection`, or a named barrier as `shared` or `exclusive`      |
| `dependencies` | The failure domains it draws on                                                                                                      |
| `turn`         | A finite deadline on waiting for resources and capacity                                                                              |
| `max`          | A finite, non-renewable absolute bound on its work                                                                                   |
| `silence`      | A progress bound within `max`, or `none`                                                                                             |
| `answer`       | A finite deadline on a person's answer, or `none` when it asks nothing                                                               |
| `recovery`     | The finite budget to finish or roll back a committed mutation, or `none`                                                             |
| `merge`        | `none`, `latest-value` for state a later value replaces (resize), or `same-key` for a read. An effectful mutation may only be `none` |
| `preempts`     | Queued operations on its key it may overtake (close over resize), or `none`                                                          |
| `waiting`      | The sentence shown while it waits, in the person's terms                                                                             |
| `expired`      | The outcome at the deadline: `not-started`, `outcome-unknown` or `retry-read`                                                        |

An **invocation** carries what a declaration cannot: its `origin` (`person`, `background` or
`system`) and its owner. One read method serves a person's refresh and a background poll, so origin
travels with the call, on the wire, and the renderer sets it. Absent, the backend treats it as
`person`, the side on which an error cannot silence somebody.

An **owner** is what an invocation dies with: a request, a session, a binding or the server.

A **lifetime loop is not an operation.** The watch poll, the pane observer and the recorder run as
long as their owner, so a total deadline would kill a healthy service. Each iteration of such a loop
is an operation, and the loop itself is registered with its owner and a bounded stop.

### 3. Lifetime is one calculation

- `turn` starts when the invocation is made, in the renderer for a call that waits there for credit,
  and is one budget across every phase: each later phase's wait consumes what remains of it. If it
  expires before anything took effect, the outcome is `not-started`. After a phase with effects, it
  is the entry's `expired` outcome, and a mutation's is `outcome-unknown` until its record says
  otherwise.
- `max` starts when work starts and is never renewed. `silence` is renewed by declared progress and
  ends the work if it elapses, within `max`. A commit hook gets a generous `max` and `silence: none`,
  since a legitimate hook can be silent and a broken one can print for ever. A transfer gets a
  `silence` on bytes moved and a `max` proportional to its declared size, set at invocation and
  never above the declaration's.
- `answer` pauses `max` while a person is being asked, and has its own finite bound. Replaying after
  an unlock does not restart `max`.
- A child operation's deadline is the minimum of its own and its parent's remainder.
- `recovery` runs after settlement and does not extend the response. It is about the store, not the
  caller.
- The backend alone decides a deadline. The renderer's timer exists to detect a lost backend, and
  is the projection of `turn + max` plus a margin, paused while the backend reports the `answer`
  phase.

### 4. Nothing runs outside a scope, and one owner settles

The catalogue's handle hands out an opaque scope that carries the deadlines and cancellation. Provider
adapters take that scope. A static check refuses direct provider calls outside the adapters,
`context.Background()` in request work and unchecked context detachment, in the manner of
`.githooks/check-control-goroutines.mjs`. Go cannot prove a syscall returns, so behaviour is held by
the tests below.

A scope has exclusive settlement authority. Handlers complete through it, and the first of
completion, cancellation and expiry wins; the other is dropped and logged. Settling on disconnect
promises nothing about delivery to a client that is gone.

A mutation's identity is minted by the renderer and travels with the call. The backend retains its
outcome for a bounded time, in memory, and `operation.outcome` answers for it. So
"checking what happened" has an end: it asks, and on no answer after the bound, or after a restart,
it says the outcome is unknown and names what to look at. A retry after `outcome-unknown` asks first.
Nothing replays a mutation automatically, including ADR-0032's sealed-error replay when the first
attempt's outcome is not known to be "did not start".

### 5. Order is per resource, and there is no domain gate

Independent reads run concurrently. A mutation chains on the resources of its phase, on the keyed
FIFO (`control/keyed_ordered.go`), extended as follows. A queued operation that is cancelled or
expires is removed before it starts, and start against cancel is decided in one step, because the
present worker runs every queued task with its original context. `preempts` lets close overtake the
resize it must rescue. `latest-value` replaces a queued resize. A key identifies the real resource
across connections: a checkout, not a binding, since the branch source and the panel hold separate
bindings on one.

A phase acquires its complete set together, so holding part of a set while waiting is gone, and with
it `CanonicalOrder` as a convention. Acquiring is sound only under this rule: **no acquisition is
nested inside a held scope**, which a test build enforces by panicking. Work that discovers a need
for more ends its phase, releases what it holds and revalidates after the next acquisition.

**The unlock fence survives by phasing.** A phase that may ask for a credential holds nothing that
`vault.unseal` needs. The fence stays derived from the declaration, as it is derived from
`UnlockAnsweringGate` today, and a credential read under a set that includes `store:vault` fails
loudly as now. `vault.unseal`'s own path has capacity no ordinary work can hold.

Restore and reset take a barrier exclusively, and every writer and coherent reader of those stores
takes it shared. A waiting exclusive holder admits no new shared ones, so continuous reads cannot
starve restore. Config and vault stay ordered per store until the audit of each store shows its
writes are atomic or ordered.

**A resource is held until the work really stops.** A late result is discarded, but its effects are
not, so an expired mutation keeps its resource while its worker runs. If `recovery` expires on a
barrier, the record trades availability for consistency: those stores enter a quarantined state, are
unavailable with a named failure, and stay so until restart, which runs journal recovery before
reopening access. Mixed generations are never exposed.

The native picker is one holder, and an open picker holds no execution capacity. Another request
does not share its result, because methods differ in what they select and who owns the answer: it
takes its turn, under its `turn` deadline, and the open picker is raised where the adapter can. A
shown picker that cannot be cancelled holds the resource until the native call returns, and a late
result for an expired request is discarded.

### 6. Execution is bounded per failure domain

A failure domain is what fails together: a mount (by device id) for local filesystem work, a resolved
host for far work, a checkout's mount for git processes, a store. Each has its own bulkhead, so a hung
mount holds its own permits and a healthy mount's listings go on.

- **Class.** Each bulkhead reserves part of its capacity for `person` origin, and `background` has a
  ceiling below the whole. Background cannot take every permit before a person arrives.
- **Abandoned work.** At a deadline, the request settles and the worker keeps its permit until it
  really returns. Abandoned workers are bounded per domain and globally.
- **Circuit breaker.** A domain at its abandoned bound is suspect. Calls on it settle at once with the
  entry's `expired` outcome, naming the domain, and no new worker starts. Other domains are unaffected
  unless the global abandoned bound is reached, in which case only suspect domains are refused new
  workers. Healthy domains keep their reserved capacity, a part of the global bound that suspect
  domains cannot take.
- **Reclaiming.** An empty domain's state is dropped when idle, so changing host ids cannot grow it.

Global bounds remain on bytes queued and outbound (ADR-0026 §13). The `control` lane goes once the
bulkheads bound execution together and that has been shown. Cancellation, unlock answers, heartbeat,
diagnostics and teardown never share a pool with the work they rescue, and are the read loop's
ingress-critical set or their own capacity.

Sizes are measured in the stage that adopts them. Eight is inherited from ADR-0026 as the migration's
starting point and measures nothing about a bulkhead.

### 7. Ordinary load is not refused; flow control is

Under ordinary load the bound is on what is **sent**, not what is accepted. The backend advertises a
credit window to the renderer, per connection, and the renderer holds calls beyond it in its own
queue: a person's action stays pending there, and background calls merge on their key. So a person's
action is never answered with a capacity error, however much is going on, because it has not been
sent yet.

That queue is bounded and has a lifetime. A waiting call is under its `turn` deadline from the moment
it is made, can be cancelled, and appears in Operations past two seconds like any other wait. Its size
is bounded by what produces it: background calls merge to one per key, and a person's actions come at
a person's rate and are bounded by their `turn`. A surface that would enqueue a further identical
action instead joins the one pending.

Rescue traffic needs no credit: cancel, ack, resolvers (`vault.unlockResolved` and its peers), ping
and diagnostics are exempt, and `vault.unseal` has its own reserved credit, so calls waiting on an
unlock cannot hold up the answer that releases them.

Stopping reading the socket is not the backpressure, since that traffic travels on the same socket and
stays on the read loop. A peer that exceeds its advertised window is
violating the protocol. It is logged, recorded in diagnostics and disconnected, and the renderer's
reconnection path handles it. That is the only remaining capacity outcome, and no well-behaved client
reaches it.

### 8. What a person sees

| Situation                               | They see                                                                                                                                                                                                                                       |
| --------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Waiting under two seconds               | The action's own pending state                                                                                                                                                                                                                 |
| Waiting longer                          | A row in Operations (`frontend/src/operations/`), with the entry's `waiting` sentence and Cancel, and the count in the activity bar's indicator, which is always on screen. A first open's helper install appears there for its whole duration |
| Background work stuck past its deadline | A row in Operations, with no toast                                                                                                                                                                                                             |
| A read expired                          | That the action timed out, named, where it was asked for, with Retry                                                                                                                                                                           |
| A mutation expired                      | One of "did not start", "done", or "checking what happened". A timeout never implies a rollback                                                                                                                                                |
| Background waits or merges              | Nothing. It is in the log and in diagnostics                                                                                                                                                                                                   |
| The sender has gone                     | Queued work never starts; running work is cancelled unless committed; session-owned work survives                                                                                                                                              |
| A failure domain is suspect             | That the disk or host is not answering, named, and other work goes on                                                                                                                                                                          |

`control.saturated`, its schemas, the holder sentences in `dispatcher.ts`, the toast and
`isSaturationRefusal` are removed in the last stage. Two seconds is the agent's recommendation, put to
the owner and not yet answered.

Operations exists for this, by the owner's pointer: one list across every tab, one count, and a `cancel`
each item owns. A waiting operation is a new source and a new `OperationKind`, which that module's
comment says is how a kind joins. The scheduler stays in the backend and does not grow in that surface.

Every call a person makes can be cancelled, which needs `rpc.cancel` beyond its present three methods.

### 9. Doing nothing is the cheapest merge

With its panel collapsed, the Files store opens, lists and watches nothing; its work starts when the
panel is first shown. A binding survives a tab switch instead of being rebuilt on each one.

## Constraints

Each is a requirement with its check.

| Constraint                                                                   | Check                                                                                                                                                                                                                                                                                                                                                         |
| ---------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Every operation has one complete declaration, and every declaration is live  | A method with no handle does not compile; a static check refuses an unreferenced handle; full-metadata comparison of table and manifest; schema validation; planted violations                                                                                                                                                                                |
| No zero or unlimited lifetime can be declared                                | Schema and constructor tests, with planted zero values                                                                                                                                                                                                                                                                                                        |
| `max` cannot be renewed; `answer` pauses it; a child stays within its parent | Clock-driven tests of renewal, unlock replay and child budgets                                                                                                                                                                                                                                                                                                |
| No provider call outside a scope; no nested acquisition                      | Static check with planted violations; test-build panic                                                                                                                                                                                                                                                                                                        |
| Unrelated work completes beside a blocked domain                             | One mount blocked for ever and another healthy; listings on the healthy mount, git and session operations complete within declared deadlines, including with background saturating the blocked domain's ceiling                                                                                                                                               |
| A person never sees busy under load                                          | e2e: 50,000 entries churning, a far host at 50 ms RTT, tabs switched continually, legitimate traffic beyond the credit window. No toast or refusal; healthy actions **succeed** within declared deadlines; the blocked domain's actions expire as declared; diagnostics hold the matching records. Passing by promptly timing everything out fails this check |
| Every accepted request settles exactly once                                  | Completion against timeout and cancel races, disconnect, under `-race`                                                                                                                                                                                                                                                                                        |
| Repeated timeouts cannot grow resources                                      | 10,000 calls on a hung domain across changing host ids; goroutines, descriptors, subprocesses and queued bytes stay under declared bounds                                                                                                                                                                                                                     |
| A cancelled queued mutation never starts                                     | Against a blocked chain, in both start-against-cancel orders                                                                                                                                                                                                                                                                                                  |
| Restore never exposes mixed generations                                      | Failure injected at every restore boundary, a hung rollback, shutdown during recovery; the quarantined state and restart recovery                                                                                                                                                                                                                             |
| Sealed vault work does not deadlock                                          | Sealed-vault work while restore is pending and capacity is exhausted                                                                                                                                                                                                                                                                                          |
| A mutation is never replayed after a timeout                                 | Client test at the renderer's seam, including the sealed-error replay                                                                                                                                                                                                                                                                                         |
| Two pickers do not cross                                                     | Two different requests, cancellation, disconnect and a late native result                                                                                                                                                                                                                                                                                     |
| Order within a session holds, and close still preempts a blocked resize      | The existing `session-ops` tests stay green, unedited                                                                                                                                                                                                                                                                                                         |

## Consequences

The oldest shortcut goes, and the cost is real: a scheduler with queue ownership, fairness and resource
identity, a credit protocol, and a pending state wherever a person can wait. Dozens of tests assert
refusal as behaviour, and they are rewritten to the guarantee each was standing for, not deleted.
Fairness between background and person is now the scheduler's to get right, where the gate's crudeness
hid it.

What gets easier: a new operation cannot be added without saying how long it may take and what a person
sees, and Copy diagnostics names every wait by id. A hung dependency becomes a named outcome, not a
stalled application.

What would invalidate this: a measured case where per-resource order loses to a domain lock on
correctness that no key can express, or bulkheads whose necessary size the machine cannot sustain.

## Migration

Each stage is green alone. **No gate goes until every conflicting caller participates in what replaces
it**, and a migrated consumer keeps taking the old gate through an adapter until then. Deadlines are
enforced per domain as it migrates, never before settlement and resource ownership exist.

| Stage | What lands                                                                                                                                                                                                                                                                                                                                                                                                          | What it must hold                                                                                                                           |
| ----- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| 1     | The toast goes; the wire discriminator stays for now. Calls carry `origin`. A background refusal is quiet and recorded. Every surface a person uses gets its own refused state: pending cleared, input kept, a named local failure, an explicit retry, with no word "busy". `backup-restore-section.tsx` stops suppressing its error on the assumption that a toast was shown. A collapsed Files panel does no work | Stops the message and fixes nothing underneath, and is said to. An enumeration test of the surfaces; no refusal path leaves a pending state |
| 2     | The catalogue, its contract and every declaration, with deadlines **measured from present behaviour and not enforced**: expiry is logged. Scopes and settlement. The static checks                                                                                                                                                                                                                                  | Full-metadata parity; settled-exactly-once. Nothing behaves differently except the records                                                  |
| 3     | Operations rows, `rpc.cancel` widened, mutation identity and `operation.outcome`, credit window with rescue exemption. Renderer timers exist but are armed per domain, as it migrates: an unarmed call has none, because backend work is still allowed to run past its declared deadline                                                                                                                            | Cancel and no-replay checks; the full-window unlock and cancel case                                                                         |
| 4     | Filesystem: enforced deadlines including the poll and baseline, renderer timers armed for it, failure domains, abandoned-worker bounds and breaker, `files.close` and shutdown repaired, per-binding order; then the gate goes                                                                                                                                                                                      | Unrelated-work and repeated-timeout checks on filesystem                                                                                    |
| 5     | Git: per-checkout order, its bulkhead, hook deadlines; then the gate goes                                                                                                                                                                                                                                                                                                                                           | The same, on git; mutation checks                                                                                                           |
| 6     | Session, content, API                                                                                                                                                                                                                                                                                                                                                                                               | Session-ops tests unedited                                                                                                                  |
| 7     | Config and vault after their audit: barrier, per-store order, quarantine                                                                                                                                                                                                                                                                                                                                            | Restore and sealed-vault checks                                                                                                             |
| 8     | Remaining refusals, `control.saturated` and the lane go; the load check becomes required                                                                                                                                                                                                                                                                                                                            | The whole constraint table                                                                                                                  |

## What happens to ADR-0026

An accepted record is not edited; this says what moves.

| Section                                       | Fate                                                                                                                                                                                                                                                            |
| --------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| §1 read-loop invariant, §3, §7, §12, §13, §16 | Stand. §1's ingress invariant stands; its historical list of methods is qualified by the ten registered today                                                                                                                                                   |
| §2 closed ingress-critical set                | Stands, and is where rescue traffic lives                                                                                                                                                                                                                       |
| §4 two kinds of admission                     | Superseded, for gates and for the lane's instant refusal                                                                                                                                                                                                        |
| §5 the bounds                                 | Superseded: the one-second wait, eight waiters, depth eight and the lane. §13's reserve is re-derived against the bulkheads                                                                                                                                     |
| §6 saturation                                 | Superseded. Refusal does not surface centrally, because there is none                                                                                                                                                                                           |
| §8 conflicts and atomicity                    | Superseded: conflict moves to per-phase resource sets. Its order of conflict before execution stands                                                                                                                                                            |
| §9 namespace FIFO rejected                    | Stands. Per-resource order is a refinement of it: it escapes head-of-line blocking because a key is one resource, not a namespace                                                                                                                               |
| §10 context owners, §11 disconnect            | Stand, with owners now declared. §11's sentence that a held picker refuses another dialog is superseded by section 5                                                                                                                                            |
| §14 observability                             | Stands for the holders; its saturation records become wait and expiry records                                                                                                                                                                                   |
| §15 shutdown                                  | Its drain stands for request work. Recovery may outlast it, and restart recovery covers that. Its universal claim of a bounded `Stop` is qualified: the recorder join at `ws.go:2499` and binding cleanup are unbounded today, and they are repaired by stage 4 |
| Rationale, Consequences                       | Superseded where they state saturation policy                                                                                                                                                                                                                   |
| Enforcement 1, 2, 3                           | Stand                                                                                                                                                                                                                                                           |

## Review

Codex reviewed the options before this record was written, then this record in ATAM's manner, reading
the code at `511157d49` without running it. Its findings in two rounds changed the catalogue's
declaration site, the lifetime calculation, settlement, failure domains, flow control and this
migration. The second round's four remaining items are fixed in this text and were not reviewed a
third time. The findings are on the task.
