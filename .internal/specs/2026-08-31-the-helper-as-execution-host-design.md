# The helper as execution host: a session outlives its carrier and its coordinator

## 0. What a user can do that they could not before

Start a long job on a remote host or locally, let nocx update itself, and come back to a tab
still driving the same running process.

The check that watches it: start a program in a tab on a remote host that writes a marker
file every second **and** produces more output than the window can hold; replace the
coordinator with a build carrying a different helper version; the marker timestamps span the
update **with no gap in the file** — the process never stopped — the tab reads and writes
that same process afterwards, and the product **states that output was missed** rather than
implying continuity. A NEW tab on that host comes up on the new generation. Close the old
tab; the old generation's directory is reclaimed.

**What is deliberately not promised here**: that a full-screen program's current screen is
restored on attach. D9 makes a best-effort attempt and says why it cannot be a guarantee.

## 1. In one sentence

`nocx-helper` becomes the **execution host** — one per machine where work runs, per
generation: it spawns processes, owns their PTYs, applies OS policy and holds one bounded
output window, on the local machine and on remote hosts alike, over any carrier — while the
coordinator keeps every product decision and every interpretation of the stream; and because
a generation is an immutable content-addressed directory, generations coexist, so updating
nocx never kills work.

## 2. What this crosses, and what those documents already decided

**AD-1** (`architecture.md:104`) — one WebSocket, raw binary data plane, JSON-RPC control
plane, PTY bytes never inside JSON-RPC. It does **not** license an extra process hop: it
governs framing, never copies, wakeups or head-of-line boundaries. D11 answers the hop on
its own terms. It also binds the helper wire: PTY bytes travel as raw frames **in both
directions** — `write` is input and may not become JSON.

**AD-5** (`architecture.md:135`) — "Tier B ... **augments** (never replaces) the remote
shell." **Amended by D2**: where the helper spawns the shell it replaces the _delivery_ of
the hooks; the hooks keep their role as the source of prompt and block metadata.
`nocx-k6p18` already requires this amendment to land in `docs/architecture.md`.

**AD-6** (`architecture.md:150`, `:151`) — the backend does not derive render state from the
byte stream, with **two** carve-outs: ADR-0024's bootstrap window (a framing we wrote,
interpreting nothing) and ADR-0041's **live backend VT grid for an enrolled pane, which does
read the stream's meaning** — permitted because ADR-0002's revisit trigger fired on "a
session that survives the client process entirely". Both are coordinator-side. D7 keeps them
there and widens neither; the helper never interprets a byte.

**AD-7** (`architecture.md:166`) — the backend session registry is authoritative;
`session.go:484` mints the id. **Amended by D10**: the execution host mints session identity
and the durable handle names its generation, because a session now outlives the coordinator
that started it.

**AD-8** — one owner per behaviour. Decisive twice: it forbids two owners of the replay
window (D7 deletes the coordinator's), and it says one owner per _behaviour_, not one process
per role.

**AD-9 / AD-10** (`architecture.md:181`, `:188`) — AD-9 puts the bounded per-session output
ring **in the backend**, keyed by monotonic byte offset; AD-10 requires bounded credit,
throttling at the bound, and "never drop, never grow unbounded; bytes are lossless and
ordered". **Both are amended here**: AD-9 by D7, which moves the ring across a process
boundary and deletes the backend's, and AD-10 by D8, which permits one lossy case and names
it. `nocx-8mllr` posed exactly D8's choice (D6-a lossless-and-stop versus D6-b
continue-with-a-gap) and closed as _superseded, not decided_. **This design answers it: D6-b.**

**The content store's startup sweep** (`internal/content/sqlite.go:410`) — `dropDeadSessions`
executes `DELETE FROM sessions` and `DELETE FROM session_output` on every store open, and its
comment states the premises: "a session is server-authoritative (AD-7), lives inside one
backend process and **cannot outlive it**", and "a recording is the bytes ONE pipe produced,
**the pipe cannot outlive the process**". This design makes both premises false, so as it
stands a fresh coordinator would **delete the recordings of live sessions** at exactly the
moment D1a matters most, and the same sweep would terminalize their still-open ledger entries
as `unknown`. Reconciling it is a blocking prerequisite (§9).

**`session_output`'s contiguity guarantee** (`internal/content/session_output.go:70`) — a gap
"is a caller defect and not a fact about the stream — accepting it would put a hole in a
recording whose whole value is that its offsets line up with what the client received";
`session_output_sqlite.go:141` rejects every discontinuity and `ws_session_record.go:106`
stops recording permanently once it loses its place. D8 makes gaps a fact about the stream, so
this guarantee is amended deliberately rather than violated by accident.

**ADR-0041** — `x/vt` as the backend emulator, fed on the backend's own read path
(`ws.go:2931`), deliberately not on the subscriber path, so it survives the client. It stays
coordinator-side and is fed from the forwarded stream (D7).

**ADR-0034** — consent keyed to the machine. Kept; D14 changes only what uninstall does to
the grant.

**ADR-0043** — one connection to the encrypted store, and explicitly not a cross-process
safety claim. Untouched: a helper generation never opens the vault or `content.db`, and
coordinator replacement stays sequential.

**The nocx-server design** (`.internal/specs/2026-08-28-the-nocx-server-design.md`):

- **D1** — two build targets, separate composition roots. Unchanged; no third target.
- **D2** — "the coordinator owns the durable-session contract; the helper preserves it."
  **Amended by D7**, for a physical reason rather than a preference.
- **D4** — a version mismatch may kill the old coordinator and lose its sessions, saying so
  out loud. Unchanged **for the coordinator process**; D1a makes its consequence false for
  sessions, which is the point of this document.
- **D8** — "one active client per session, and the loser is told" (`ws.go:64`,
  `session.displaced`, shipped as `nocx-oevq4`). **Unchanged here**; the multi-coordinator
  case is document 2 (§7).

**The nocxify delivery-modes design**: §3.4 named `relay` as the Tier-B carrier and deferred
it "so the seam it lands in is decided now rather than forked into later" — this is that
seam. §3.5's three axes are kept whole; which carrier delivered integration is the
observed-delivery axis's answer. §5.2's additive rule is kept.

**The remote-helper design**: D7's install layout; D15's reserved `session` name —
`host.Register` panics on `session` and **only** on it (`host.go:85`), while `files` and
`ports` are merely unregistered names answering `ErrCodeUnknownService`; D21's content hash;
D25's prune and uninstall, whose prune rule **D4 replaces** and whose uninstall **D14 keeps
whole-tree and adds to**.

**`contracts/`** — 378 top-level schemas (398 counting `contracts/files` and
`contracts/tools`), and they describe the **frontend** wire, not the helper's: frontend
`git.open` requires `sessionId` (`contracts/git.open.params.schema.json:8`) while helper
`git.open` carries only `cwd` (`internal/git/hostsvc/params.go:9`). Two surfaces, two shapes.
D12 extends the regime to the helper surface rather than claiming it is already covered.

## 3. Decisions

### D1 — The promise

**D1a, binding.** Once an execution host acknowledges creation of a session, no nocx update
action — installation, activation, coordinator replacement, compatibility negotiation or
pruning — may terminate or signal its process, close its PTY, delete its live generation, or
**destroy the information and rendezvous a fresh compatible coordinator needs to reattach**.
It ends only on natural process exit, explicit session close, user-confirmed uninstall, host
failure, or an explicitly authorised security revocation.

Two consequences, stated so they are not discovered:

- An update incompatible with a **non-core service** refuses that service and touches
  neither the survival ABI nor the session (D3).
- A **security** update does not silently override D1a. Terminating a vulnerable generation
  is an explicit, user-authorised revocation that lists the live work first, as uninstall
  does (D14).

_Destroy the ability to reattach_, not _make unattachable_: sequential coordinator
replacement necessarily has a window with no coordinator, and that temporary refusal is not
destruction.

**D1b, decided here, not deferred.** A program **keeps running** through an arbitrarily long
coordinator absence. It does not stop at the ring's bound. The price is that output produced
beyond the window is **gone for everyone, permanently** — see D8, which amends AD-10 to
permit it and requires the product to say so.

### D2 — The execution host: fat infrastructure, thin product

The helper owns, on the machine where the user's work runs: process creation and any policy
that depends on being the process's ancestor (including the future Landlock/Seatbelt
sandbox, which only the spawner can apply); PTY master lifetime, resize, signal delivery,
foreground-group questions, exit status; execution-domain operations — login-shell
resolution, native port enumeration, git, filesystem; and **one bounded output window per
session** with 64-bit offsets and raw forwarding.

The helper does **not** own: any interpretation of the byte stream — no VT, no OSC, no block
boundaries; blocks, the ledger, retention, the vault, `content.db`, the assistant, UI state;
or **policy decisions**. The coordinator decides which sandbox a tab gets, which mode a
destination is in, how big the window is and what a resize should be; the helper applies
them.

**Rejected: a helper that maintains a terminal grid.** herdr does exactly this — its server
links Ghostty's terminal library and renders virtual frames with no client attached
(`src/ghostty/bindings.rs`, `src/server/headless.rs:4375`) — and it is right for herdr,
whose server is the whole application. For nocx it fails twice: the helper would interpret
the stream (AD-6, whose two carve-outs are both coordinator-side), and it would stop being a
2.8 MB binary, which is the measurement D11 and §6 rest on. D9 gets the same outcome without
either cost.

**This amends AD-5** as recorded in §2.

### D3 — A small frozen survival ABI; everything else versions freely

**Frozen for the lifetime of the product**, and small enough that freezing it is a promise
rather than a hope:

`attach`, `detach`, `write` (raw), `data` (raw, session-keyed, offset-carrying), `resize`,
`signal`, `exit`, `close`, `sessions` — **and the retirement surface**: the lifetime lock's
contract and `probe-prunable` (D5). A future coordinator must be able to probe a generation
several versions old safely; if that surface can drift, D1a can be broken by an upgrade that
merely reasoned wrongly about what it was allowed to delete.

`attach` carries an explicit **`fresh`** flag — whether the caller holds render state — and
never infers it from the offset. A fresh renderer can attach at a non-zero offset, and a
renderer can retain an offset after losing its screen; the cursor cannot decide which.

**`nudge` is deliberately NOT here.** An earlier draft froze it. `resize` already expresses
it, and a presentation heuristic whose reliability D9 cannot vouch for does not belong in a
contract that can never change.

`spawn` is deliberately **outside** it: an old generation never starts new work. New starts
go to the current generation; an old one only continues sessions it already owns and accepts
attaches to them. _Survival and reattachment ABI_ is the honest name.

**Everything else versions freely**: `git`, `files`, `ports`, sandbox policy shape,
capability reporting, `spawn`. A coordinator that cannot speak an old helper's `git` makes
the git panel say why (D13); the session is untouched.

This is the only one of three compatibility answers that keeps D1a under AGENTS.md's "no
backward-compatibility shims". The others are recorded so they are not re-proposed: a
payload-blind router fronting versioned helpers needs a third process, and "break the
contract, kill incompatible sessions" is D1a negated. **Keeping several old protocol clients
inside one coordinator is a shim under another name and is refused.** Freezing one path
forever is not a shim, but it **is** a compatibility obligation, and this document says so
rather than implying compatibility went away.

**What is frozen is more than ten verbs**: framing and maximum sizes; the core handshake
version; session and generation identity; raw data routing with **64-bit** offsets; ordering,
reset and gap semantics; error and refusal envelopes; authentication; exit/close race
semantics; and service-version negotiation for everything outside the core.

**The header cannot carry this contract.** Type byte 8 is numerically free (`frame.go:28`)
and `seq`/`ack` were reserved "so a later PTY-owning service can resume without a wire break"
(`frame.go:15`) — but `valid()` is a closed set, so adding a type **is** a vocabulary change;
and `EncodeFrame(t FrameType, seq, ack uint32, …)` is 32-bit and connection-wide while
offsets are `uint64` and the header has no session id. **The raw payload carries session id
and a 64-bit offset itself**; the header's `seq`/`ack` stay connection-level sequencing and
are not the cursor. v1 has no live PTY sessions, so the baseline is bumped now and the freeze
begins after.

### D4 — Generations coexist, and held generations are never pruned

`~/.nocx/helper/<version>-<goos>-<goarch>-<hash>/` is content-addressed: a new version
installs into a **new** directory and does not touch the old.

Prune's rule changes from "not current" to:

> Remove an install directory that is neither `keep` nor **held** by a live generation.

**There is no cap on held generations.** Storage is bounded by live sessions, not by a count;
a cap would have to break D1a to honour itself. Disk used by held generations is reported
(D13), never resolved by deleting held code.

**Two corrections about the existing install**, so later work does not rely on them: the
directory is **not** atomically published — `install()` calls `mkdirAll` on the final
directory, writes a temp file inside, renames the _file_, then writes the marker, so
completeness is marker-gated only; and "immutable" is convention, since `dirMode` and
`binaryMode` are `0700`.

**Prune is not the only blocker.** The larger one is that the helper exits when its lane
reaches EOF (`host.go:154` → `client.go:188` → `launch.go:211`). That is the lifecycle
document's subject (§9).

### D5 — Liveness is a kernel fact; deletion is a rename

The bundle publisher's lock **cannot** be extended here, and an earlier draft that said it
could was reusing a name rather than a solution. Verified: `acquireLock` probes five times
over 1.55 s and then **breaks the lock unconditionally** (`publisher.go:1164`). That is right
for a _bounded_ publish, where elapsed time is a valid liveness proxy and breaking costs
duplicate work. A generation hold is unbounded: 1.55 seconds would declare a healthy
three-hour job stale.

An unbounded hold needs a **kernel-lifetime primitive**, released by the kernel on process
death, trusting neither PID nor clock. SFTP cannot test a remote advisory lock, so
determination runs on the host while deletion stays with the coordinator — and **deletion is
an atomic rename, not a recursive delete under a lease**:

1. The generation daemon holds a live lock for its lifetime.
2. `nocx-helper probe-prunable <generation>` attempts the same lock **exclusively,
   non-blocking**.
3. Failure ⇒ held ⇒ report live and exit.
4. Success ⇒ unheld ⇒ report a nonce and **retain the lock**.
5. The coordinator **renames the generation directory** to a nonce-bearing tombstone under
   the helper root. After the rename the canonical path does not exist, so resurrection
   through it is impossible.
6. The probe releases and exits.
7. Deleting the tombstone is ordinary cleanup; an interruption leaves a recognisable
   tombstone the next pass reconciles.

**Why not a cancellable recursive delete.** `removeTree` recurses with no context check
inside it — `ctx` reaches only `Prune` and `Uninstall` (`prune.go:30`, `:50`, `:65`) — so
"carrier loss stops the deletion" is not implementable there. And even a cancellable one
cannot undo files already removed: a probe dying mid-delete leaves a half-deleted directory
with no lock and no owner. The rename moves the whole decision to one atomic step.

**Why not liveness by socket.** A successful connect is good positive evidence of life; a
failed connect is **not** safe negative evidence of death — accept backlog, descriptor
exhaustion, a broken listener loop on a daemon that still owns PTYs, a forwarding failure —
and it cannot hold its conclusion until the later delete. The two mechanisms answer different
questions and neither replaces the other:

| mechanism           | question                                                             |
| ------------------- | -------------------------------------------------------------------- |
| rendezvous socket   | can I communicate with this generation? (discovery, attach, health)  |
| lifetime/prune lock | can I prove retirement is mutually exclusive with a live generation? |

**Shared homes get a namespace, not a refusal.** `installDir`'s comment already anticipates
one account across two machines — "(NFS, or the same login on both) resolves to two
directories" — but that holds only for _different platforms_: two **same-platform** machines
sharing a home resolve to **one** directory, and a host-local advisory lock on one cannot bind
the other.

An earlier draft answered this by disabling automatic prune on detected shared storage, on the
argument that a machine namespace "would double the footprint for every user". **That argument
is false**: a machine namespace produces one namespace on an ordinary single-machine home and
duplicates only where homes are genuinely shared, which is precisely the colliding case. And
the refusal was incomplete anyway — a shared tree makes `Ensure`'s repair path
(`install.go:130`, which removes an incomplete directory), tombstone reconciliation, and
whole-tree **uninstall** unsafe in the same way, not just automatic prune. One machine cannot
enumerate another's live sessions, yet D14 proposes deleting their common `~/.nocx/helper`.

So: **mutable generation state — the lock, the daemon's runtime endpoint, tombstones — is
namespaced by machine identity.** Immutable artifact bytes may still be shared, since they are
content-addressed and identical by construction. Detection of shared storage is not relied on
as a gate: portable, reliable detection is itself hard, and treating a negative result as proof
would put D1a on an inference.

### D6 — The coordinator retires generations; the helper only tells the truth about itself

Retirement stays where installation is: the coordinator, over the SFTP lease it holds anyway,
at connect time.

**Rejected: a new generation cleaning up an old one.** It would grant a helper write authority
over a sibling's directory, ask the process with the least context to judge another's
liveness, and still not cover the case pruning exists for — a generation killed without
running its own cleanup.

**`probe-prunable` is a lease, not a read.** It deletes nothing, but it acquires an exclusive
mutation lease and is a party to a destructive transaction. Calling it a read would understate
what it is authorised to do.

**And the severity survives a correction to its mechanism.** Deleting a running executable's
directory does **not** kill the process — the inode outlives the unlink. It destroys the
generation's durable identity and rendezvous, permanently orphaning its live PTYs: the program
and its descendants go on consuming CPU, memory and external resources while nocx can no
longer read output, send input, resize, signal or deliberately stop it, and reinstalling
identical bytes does not reconstruct the lost attachment state. That violates D1a more
directly than termination would, because nocx loses both control **and** recovery.

### D7 — One output window, and three independent readers of it

**This amends AD-9 and D2 of the nocx-server design.** AD-9 puts the ring in the backend; it
moves across a process boundary, for a physical reason: **only the process holding the fd can
say what was produced while nobody was listening.**

`pumpToRing` is today the junction of three connection-independent consumers, and their
co-location is the design: the enrolled VT grid (`ws.go:2931` — fed "on the backend's own read
path, and deliberately not on the subscriber path"), the durable recorder (`ws_session_record.go:84`,
"started HERE, beside the pump and for the same reason") and the replay ring (`ws.go:2945`).

**`internal/transport.outputRing` is deleted**, not shared: a second buffer in series would be
a second owner of replay, which is AD-8's whole point. But **"the coordinator becomes a
pass-through" was wrong, and a single serial fan-out would be a defect**, because the three
consumers have different pacing: WebSocket credit would stall the recorder, and a SQLite commit
would stall the frontend.

The topology is **independent pulls from one stateless window**:

```
PTY master → helper: sole reader, no interpretation
           → helper: one bounded window per session, capacity-reclaimed, readers stateless

coordinator, each pulling on its own cursor, none blocking another:
   ├─ frontend delivery  — from the window's acked offset, stops pulling at CreditLimit
   ├─ recorder           — from its persistence cursor (content.db)
   └─ enrolled VT grid   — from its own cursor (ADR-0041 stays coordinator-side)

each reader independently receives reset-to-base when its cursor falls behind
```

**D8 removes reader state from the helper; it does not remove reader state from the system.**
A cursor is a position, not a buffer, and holding three of them is what keeps the consumers
uncoupled.

**Correcting an earlier draft: "flow control is not buffering" is false as written.**
`internal/transport/outbound/outbound.go:196` owns a bounded outbound queue, which is real
buffering on the delivery path. `CreditLimit` and `FairChunk` stay, and so does that queue;
what goes is the _replay_ buffer.

Four obligations, each owed an assertion, and two of them are existing couplings this design
must move rather than assume away:

1. **The run lease's output observation must move to the delivery path.** `run_lease.go:426`
   takes `l.ring = rx.ring` and `:188` hangs `setWriteObserver(func(n int) { l.onOutput(n) })`
   on it, and the field is declared `ring *outputRing // nil → no output observation
(wall-clock only)`. Delete the ring without rehoming that observer and the agent's run lease
   silently degrades to a wall clock — the soft degrade AGENTS.md forbids. Observing arriving
   bytes is in fact more correct than observing buffered ones.
2. **`waitForCredit` is not separable today.** `ws.go:3000` calls `ring.waitForCredit(ctx,
startOffset, pos, CreditLimit)` and `ring.go:370` computes it from that ring's `acked`. The
   credit accounting is extracted onto the delivery reader; this is work the design owes, not a
   decomposition that already exists. `setAttached`, `close` and `wake` (`ws.go:98`, `:133`,
   `:1720`, `:2589`) move with it.
3. **Grid enrolment** requiring byte-zero observation is committed **before spawn**, or the
   grid is initialised by replay from the window's base and **stays `unknown`** until ADR-0041's
   full-repaint criterion is met (D9). It does not become trusted by being fed.
4. **Coordinator replacement must not double-consume** into grid or recorder.

### D8 — AD-10 is amended: the window is capacity-bounded and a straggler is reset

**The amendment**, landing in `docs/architecture.md` in the same commit as its implementation:

> AD-10 permits exactly one lossy case. A session's output window is bounded; when it is full
> the **oldest bytes are discarded** rather than the source throttled, and a reader asking for
> a discarded offset is told the window's base instead. Every other path stays lossless and
> ordered. **The loss is stated in the product**, never only in a log.

This is `nocx-8mllr`'s D6-b, and it is what keeps D1a honest: a three-hour build continues
rather than stopping because nobody is watching.

**What it buys is simplicity, not only continuity.** A capacity-reclaimed window needs to know
**nothing about its readers**: no subscriber map, no lease, no expiry, no minimum-cursor rule,
no recorder authority, no arbitration. A reader asks "from offset X"; if `X ≥ base` it is
served, otherwise it is told `base` and resets. Everything an earlier draft carried under those
names existed only to serve losslessness.

**The lossy case is wider than "no coordinator attached", and the earlier draft understated
it.** It fires whenever _any_ reader is slower than the source: a stalled WebSocket, a
congested carrier, a paused SQLite recorder, a scheduling stall. `CreditLimit` bounds
coordinator-to-frontend bytes in flight; it does **not** stop the helper's window advancing
while the frontend is credit-blocked. Two consequences follow:

- **A live reset path is required, not only an attach-time one.** Today the pump treats losing
  its position as nominally unreachable and simply stops the stream (`ws.go:3004`). Under this
  rule an attached, slow reader must receive one explicit gap-and-reset and resume at the base
  — never go quietly silent.
- **The recorder needs a gap operation.** `session_output` today rejects every discontinuity
  by design, because "its whole value is that its offsets line up with what the client
  received". D8 makes a gap a fact about the stream, so recording gains an explicit
  `Skip(expected, resumeAt, reason)` — atomically advancing the produced cursor and permitting
  later appends — and **recording resumes after it**. Showing a gap in the frontend is not
  sufficient; a recorder that stops at the first gap turns one lost second into a lost session.

**Correcting an earlier draft: `session_output` is not the ledger.** The holes are in the
_recording_ of a session's output. Ledger entries — blocks — are frontend-derived and cross
the control plane, so a block whose bytes were partly missed is still an entry; what it loses
is its output. The product says which recording has a gap and where.

**The bound is 256 KiB today, and it is no longer deferred** — see §6. It is the number that
decides how much an ordinary coordinator restart destroys, and `ring.go:10`'s own illustrative
256 KiB/s means the whole window can roll in about a second.

### D9 — Reconnect replays; a fresh attach may ask for a repaint, and may not get one

Two entry paths, distinguished by `attach`'s explicit `fresh` flag (D3) and never by the
cursor's value:

- **Reconnect after a carrier drop** → replay from the last acked offset, and **do not
  repaint**: a network blip must deliver the bytes that were missed, never erase what
  scrolled by.
- **Fresh attach, no render state** → replay the retained window, and **may additionally
  request a best-effort repaint** via `resize` — a one-shot winsize change that a TUI
  typically answers by redrawing.

**It is best-effort, and the document will not pretend otherwise.** `SIGWINCH` is advisory; an
application may ignore it or repaint partially; a resize can change a program's layout or its
state; and there is **no observable signal saying a complete repaint has occurred**. A shell at
a prompt, a finished build's output and `tail -f` reconstruct nothing at all — for them the
window is the only answer there is.

**Therefore the backend grid does not become trusted by being nudged.** ADR-0041 keeps an
adopted grid `unknown` until a full repaint has been observed, and `internal/panegrid`'s store
has no trust state and no repaint-completeness detector — it is an emulator fed bytes
(`panegrid.go:244`). This is safety-relevant, because the grid is what decides whether nocx may
inject input. An earlier draft claimed the grid "becomes exact after a nudge"; **that was
false**, and the criterion that would make it true is not designed here.

**Correcting this draft's own reading of herdr.** The nudge is real
(`src/pty/actor/unix.rs:758`), but `rows > 2` is **not a safety guard** — it selects a _row_
nudge, and the `else` branch does a _column_ nudge (`cols.saturating_sub(1).max(4)`); there is
also a 30 ms sleep between the two size changes (`:787`). "App-safe" is a source comment, not a
demonstrated property. And herdr does not use it as a substitute for terminal state: it applies
one nudge to _imported_ panes after the first client attaches (`headless.rs:336`) on top of a
server that already owns authoritative Ghostty state and renders frames with no client attached
(`headless.rs:4358`). Taking the technique is fine; citing it as a guarantee was not.

The two mechanisms remain complements, with honest reach:

| mechanism       | recovers       | reliable for                                     |
| --------------- | -------------- | ------------------------------------------------ |
| window          | recent bytes   | everything, up to its bound                      |
| repaint request | current screen | TUIs that fully repaint on `SIGWINCH` — untested |

**What would make the repaint promotable from best-effort to promised**: a declared, bounded
set of supported programs shown to fully repaint under the exact sequence, plus a criterion
that prevents the grid being trusted after a partial one. Neither exists; until they do, §0
promises the process, not the screen.

### D10 — Discovery is a directory listing; no router, and the AD-7 amendment

A coordinator finds what is on a host by **listing `~/.nocx/helper/`**. That directory _is_
the index: each entry is a generation, each live one holds D5's lock, and each is asked for
its own sessions. There are one to three of them in practice. No router process, no durable
index to keep consistent, no admission protocol.

**And no lookup service, because the durable handle addresses its generation.** A session's
handle carries generation identity, the generation's endpoint is derived from it, and the
mapping is committed durably **before** the open acknowledgement — a mapping held only in
coordinator memory would recreate the problem at the next restart. The trigger that would
justify a stable registry role, recorded so it is recognised rather than rediscovered:

> A registry role is needed when session ownership or endpoint selection requires **lookup**
> rather than **derivation** — which is where a machine that has never held the handle must
> discover sessions it did not create. That is document 2's problem (§7), not this one's.

**This is the AD-7 amendment**, made explicitly rather than by redefining "server": the
execution host mints session identity, the durable handle names generation and session, and
the coordinator's registry stays authoritative over panes and tabs.

**The endpoint is derived, not spelled, and this is not a theoretical limit.**
`sockaddr_un.sun_path` is ~108 bytes on Linux and ~104 on macOS, and the coordinator already
refuses past it — measured on 2026-08-31 with a deep home, `nocx-server` exits with
"coordinator: socket path exceeds the platform's unix-socket limit: 161 bytes". A generation
directory adds `<version>-<goos>-<goarch>-<64-hex>` on top of that, so the helper's endpoint
**cannot** live under its install directory. The runtime endpoint is a short encoding of the generation hash under a private
runtime directory; the full identity participates in the handshake.

**The install/prune race is benign, and ordering is what makes it so.** A coordinator takes a
generation's lock **before** spawning its daemon. A concurrent prune then either sees the lock
and skips, or wins and the losing coordinator finds its directory retired and reinstalls —
`Ensure` is content-addressed and idempotent, so the cost is a 2.8 MB reinstall and never a
session. The dangerous case is retiring a generation with live sessions, and the lock covers
exactly that.

### D11 — One shape locally and remotely; the helper reads everywhere

The local case is **not deferred**, and the argument is AD-8's rather than convenience: a
helper-side window remotely and a coordinator-side one locally would be two owners of replay
and backpressure — D2's delay fuse from the other direction.

- **The helper is the sole reader everywhere; the coordinator fans out everywhere.** This
  **rejects local fd passing**, the earlier preference: handing the master to the coordinator
  would make it the local reader and the helper the remote one, reintroducing the split this
  decision removes.
- **D1a is delivered locally too.** The nocx-server design's D4 may kill an old coordinator
  and lose its sessions; with the local helper owning the PTY that stops being necessary.
- **Local install reuses `deploy`** with a local FS adapter, into the same
  `~/.nocx/helper/<version>-<goos>-<goarch>-<hash>/`. **Not** a sibling binary in the bundle:
  updating the bundle replaces that binary in place, which is what generational coexistence
  forbids.
- **`make build` gains one native helper build.** The 2×2 cross matrix stays a release
  prerequisite for remote upload only. This is also what stops a dev stand having no local
  helper.

**The carrier is not settled by AD-8, and this document does not pretend otherwise.** AD-8
constrains semantic ownership, not transport. The local carrier **begins** as a framed unix
socket and must meet a budget; if it does not, the carrier is optimised — shared memory plus
event notification would preserve identical `SessionHost` semantics — **without moving reader
or window ownership**. The budget and its thresholds are §6's second item, and without a
threshold "uniformity is worth more" would be unfalsifiable.

### D12 — The wire stays what it is, and `contracts/` is extended to it

Framing stays: JSON payloads for control, **raw frames for data in both directions**. `write`
carries input PTY bytes and may not become JSON — AD-1 binds this leg as it binds the
WebSocket. Correcting an earlier draft: the frozen verbs are **not** all attach/detach
frequency — `write` fires per input burst and `resize` interactively — which is precisely why
they are raw.

**gRPC/protobuf is rejected**: a heavy dependency in a binary whose 2.8 MB decided D2 and
§6; HTTP/2 does not fit an stdin/stdout exec lane; and it would be a second control-plane
vocabulary in one product, against AD-8 and AD-1's existing choice.

**OpenRPC is rejected as redundant, not as bad.** It describes JSON-RPC, and `contracts/`
already does that job here — 378 schemas, generated renderer types, Go validation and
over-the-wire conformance. Correcting an earlier draft: `contracts/` does **not** already
cover the helper wire — frontend `git.open` requires `sessionId` while helper `git.open`
carries only `cwd`; they are separate surfaces. The regime is **extended** to the helper
surface, which is what `contracts/README.md` already asks of any method being touched.

**The limitation is stated rather than papered over: JSON Schema describes shapes, not
sequences.** The hard part of the frozen ABI is ordering and state — attach, offsets, reset,
repaint requests, exit/close races. Neither JSON Schema nor OpenRPC expresses that. The ABI's contract is
**schema plus assertions**, and the load-bearing half is the assertions.

### D13 — What the product says, and what it stays quiet about

**A tab is not marked for running on an older generation.** After an update most remote tabs
would sprout a badge meaning "everything is fine", which is how people learn to stop reading
badges. AGENTS.md requires a _degrade_ to be visible; an older generation is not one.

1. **Nothing degraded** — no tab marker. The facts live where a person asks what nocx put on a
   host: `shell.footprint.status`, already read-only, already never connecting, already honest
   about "last seen". It gains generation rows and the disk they use. `FactList` is the kit
   component, and its `note` field is documented as "the honest half. A value the product
   cannot fully vouch for" — exactly a fact observed at last contact. **It is labelled and
   tested as last-observed inventory**, never as current truth.
2. **A helper-served feature is genuinely unavailable** — the affected surface explains itself
   **at the point of use**, not a generic badge whose meaning must be hunted.
3. **The session is durable and currently detached** — this earns a tab marker, because it is
   new information about the session, and `nocx-k6p18` names the consequence: "'the channel is
   closed' stops meaning 'no helper is running'". `Badge`, `tone="info"`, `title` carrying the
   reason.
4. **Output was missed** (D8) — a gap is shown **in the stream, where it happened**, not in a
   log and not only in a status panel. This is the amendment's price being paid in public.
5. **Automatic prune is off because the helper root is on shared storage** (D5) — stated in
   the footprint surface, since the footprint will otherwise grow without explanation.

**The script bundle.** §5.2's additive rule holds; what changes is the _carrier_. Where the
helper spawns the shell, integration arrives at spawn instead of through a published bundle,
and which carrier ran is the observed-delivery axis's answer. The bundle is not additionally
published for a helper-spawned session and remains published for every other session on that
host.

### D14 — Uninstall is whole, lists what it kills, and revokes consent first

All-or-nothing over `~/.nocx/helper`: those directories are content-addressed and nobody
hand-edits them. This is the helper tree only — the **script bundle** keeps its opposite and
deliberate rule (manifest-owned unmodified files only; a modified file is a reported conflict
and stays).

1. **The confirmation lists the live work it will end**, across every generation — which the
   D10 listing provides. `deploy.Uninstall`'s precondition (no helper running out of a
   directory being deleted) stops being free: satisfying it _is_ ending the user's work, so it
   is raised into the product as a consent moment rather than left as a caller's obligation.
   `Dialog` plus a `FactList` row per session.
2. **The list is bound to a snapshot token.** Same-connection listing does not remove the
   TOCTOU: a session can start or exit between listing and confirmation. New starts are
   quiesced and the confirmation names the epoch it saw; a session started after the snapshot
   invalidates the confirmation rather than being silently killed.
3. **Consent is revoked first, then files are removed.** The failure modes are asymmetric:
   revoking first and failing to delete leaves inert files, while deleting first and failing
   to revoke **silently reinstalls on the next connection**. ADR-0034 is untouched — consent
   stays keyed to the machine; only the stored answer is cleared, so the next connection asks.

### D15 — What is reserved for document 2, and why it is reserved now

Document 2 (§7) makes one workspace reachable from two machines at once. It needs no wire
break, because the frozen ABI carries its room from the first day — the repository's own
established move, used twice already: AD-1 allocated a version byte and a reserved
`metadata` msg-type up front, and `proto` reserved `seq`/`ack` "so a later PTY-owning service
can resume without a wire break".

Reserved here, unused here, and **required to stay** so a later optimisation does not quietly
remove them:

- **A subscriber identity** in `attach` and in every `data`/`write`, though there is exactly
  one subscriber today.
- **An opaque `WorkspaceID`** in `sessions` and `spawn`, though the workspace is coordinator-
  owned today and `workspace.Default` is a coordinator-side constant. Opaque, never a display
  name: human names bring rename, collision, normalisation, case and guessability into
  execution-host policy.
- **Session-keyed, 64-bit offsets** in `data` — already required by D3, and the thing that
  makes N readers an implementation question rather than a wire question.

### D16 — A coordinator drains the window before it can record

Reading the helper's window needs **no vault and no `content.db`**: those are the recorder's
dependencies, not the reader's. A replacing coordinator therefore **attaches and drains
immediately on start**, before the vault is open, holding what it reads in memory for
delivery and starting the recorder only when the store is available.

This is what keeps conclusion 3 from being fatal. Without it the read gap is bounded by
however long a person takes to unlock; with it the gap is the machine floor plus carrier
setup, which the measured table shows 256 KiB covering for everything but a bulk dump.

The recorder's own gap remains real and is what `Skip` (D8) exists to record: bytes that
flowed while the store was closed are delivered to the frontend and **absent from the
recording**, and the product says so.

## 4. What the user sees

**An update.** The coordinator installs vN into a new directory; vN−1 is untouched and its
process runs. New tabs open on vN; existing tabs reattach to vN−1, because their handle names
their generation, over the frozen ABI. If a non-core service changed its wire, that panel —
on that tab — says why. When vN−1's last session ends its daemon exits, its lock releases,
and the next connect retires it. Nothing is interrupted.

**A long absence.** The build ran the whole time. The tab comes back and the stream shows —
in place — that output was missed while nobody was attached. If the pane held a full-screen
program that repaints on a resize, attaching asks it to, and it usually comes back looking like
itself; if it held a shell or a scrolling build, what rolled out of the window is gone, and the
gap says so rather than the tab pretending otherwise.

## 5. Assertions

Written as assertions rather than prose (AGENTS.md rule 4); the ones that matter are written
from this spec by someone who did not implement it. Where an earlier draft's assertion could
pass while the product was broken, the reason is recorded beside its replacement.

**Continuity (D1a, D4, D8)**

1. A process spawned by vN−1 is still running — proven by **the same remote PID and process
   identity, from a fresh coordinator** — after replacement by a coordinator carrying vN,
   having produced **more than the window's capacity** while detached, and the marker file it
   wrote every second has **no gap**.
2. A session opened after that replacement is served by vN — asserted by **distinct helper PID
   and content hash**, never by a version label.
3. Installing vN removes no file inside vN−1's directory **while vN−1 is held**, through the
   production install-and-prune path; and an **unheld** vN−1 is retired by that same path.
4. With vN−1 held, prune retires nothing; after the hold releases, it does — driven through a
   **real generation daemon**, not a synthetic hold.
5. A generation killed without cleanup is retired on a later connect, with staleness decided by
   D5's lock: no remote PID read, no wall clock compared.
6. With **three** held old generations plus current, none is retired and the footprint is
   reported.
7. The frozen ABI — including the retirement surface — is exercised by a coordinator against
   **an archived released helper binary**, not the same source with a changed version constant.
8. Retirement is a rename: interrupting the coordinator between rename and tombstone deletion
   leaves a recognisable tombstone, the canonical path absent, and the next pass reconciles it.
9. **Two same-platform hosts sharing one home cannot damage each other**: with a live
   generation on host A, host B's install-repair, prune and whole-tree uninstall each leave A's
   generation and its live sessions intact. (An earlier assertion tested only a local
   filesystem and would have passed while this was broken.)

**The frozen ABI (D3, D12)**

10. A non-core service whose wire changed answers a version refusal naming the service, **over
    the real wire**, while the same session continues reading and writing.
11. A raw data frame round-trips a payload containing invalid UTF-8 and bytes resembling a frame
    header, across split reads and adjacent frames, with multiple sessions multiplexed and
    offsets above 2³², and the gap callback reports nothing.
12. Input travels as raw frames: a `write` carrying arbitrary bytes, including a NUL and a valid
    JSON document, arrives at the PTY byte-identical.
13. **`fresh` is explicit**: an attach with `fresh=false` at offset zero does not request a
    repaint, and an attach with `fresh=true` at a non-zero offset does. Neither is inferred from
    the cursor.

**The window and its readers (D7, D8, D9)**

14. **The coordinator constructs no output window**: the helper's production composition root
    constructs the one concrete implementation, proven reachable by `deadcode -whylive`, and the
    coordinator binary contains no replay implementation or instance. This proves _placement_
    only — 15 and 16 are what prove the replacement is correct.
15. **The three readers do not couple.** With the frontend held credit-stalled, the recorder and
    the grid keep advancing; with the recorder blocked on a slow commit, frontend delivery keeps
    advancing. Asserted concurrently, because a sequential trace passes while a serial fan-out
    deadlocks.
16. **An attached slow reader is reset, never silenced.** Hold an attached frontend
    credit-stalled, produce more than capacity, and it receives exactly **one** explicit
    gap-and-reset and resumes at the helper's base — it does not stop receiving. (`ws.go:3004`
    stops the stream today; this is the behaviour that replaces it.)
17. **The recorder records a gap and resumes.** Starve the recorder past the window's bound: it
    writes an explicit gap through `Skip`, its produced cursor advances, and **subsequent
    appends succeed** — the recording is not abandoned at the first discontinuity.
18. Producing more than capacity with nothing attached **does not block the producer**, and the
    reset a later reader gets carries the base, not the written offset. (`ring.go:316` returns
    the written offset today; the change is expected and this is what catches it.)
19. One shared trace suite drives the real helper window through the real coordinator adapters
    and the real wire — write, attach, read-from-offset, overflow, reset, detach, close —
    observing identical offsets, output and reset points across all three readers, with memory
    bounded throughout. A boundary ratchet forbids offset and reset arithmetic outside that
    package.
20. **A partial or absent repaint leaves the grid `unknown`.** A fixture that ignores `SIGWINCH`
    and one that repaints only its top half both leave the backend grid untrusted, and input
    injection is refused on it. (An earlier assertion claimed the grid "becomes exact after a
    nudge" and could be made green by a cooperative fixture alone.)
21. A reconnect at the last acked offset replays exactly and **does not request a repaint**.
22. **The run lease still observes output** with no ring in the coordinator: a command producing
    output does not terminalize on the wall clock, asserted against the lease's own behaviour
    rather than against the presence of an observer.

**Restart reconciliation (§9)**

23. **A coordinator restart preserves the recordings and the open ledger state of sessions the
    helper proves live**, and sweeps only those it proves absent. Asserted against the real
    startup path, because `dropDeadSessions` deletes both today.
24. A command spanning a coordinator replacement is **not** terminalized as `unknown` while its
    process is still running.

**Failure paths (AGENTS.md rule 3)**

25. Install fails at each enumerated write boundary in turn; vN−1 keeps serving, and the next
    connect converges through the automatic path with no manual cleanup.
26. The lock cannot be taken because the directory is read-only: the product refuses visibly,
    names the reason, **spawns no child** and retires no generation.
27. The carrier drops **for long enough to roll the window**: a fresh coordinator reattaches,
    proves the same PID, receives a reset carrying the base rather than a false continuation,
    and the product shows the gap. (An earlier assertion produced less than capacity and would
    have passed without ever exercising loss.)
28. **And the paired positive**: on an ordinary host with an ordinary home, using the shipped
    coordinator and helper binaries over a real carrier, a generation is installed, held,
    served, reattached and retired end to end.

**Product surfaces (D13, D14)**

29. After an update with an old generation serving, no tab acquires a badge, and
    `shell.footprint.status` lists both generations **without connecting**, labelled
    last-observed.
30. A detached durable session's tab carries the info badge, and it clears on reattach **by a
    fresh coordinator**.
31. Uninstall's confirmation lists every session it will end **across every generation**, bound
    to a snapshot token; a session started after the snapshot invalidates the confirmation
    rather than being silently killed; declining ends and removes nothing.
32. Consent is revoked before any file is renamed or deleted, **and the first failure is
    asserted too**: a revocation that fails begins no removal.

## 6. Measurements this design rests on, and the one it still owes

**Measured 2026-08-31, commit `b0983332`, go1.26.7 linux/amd64, no build tags, with no
embedded helper artifacts present (they are gitignored, and a fresh checkout embeds only
the committed `.gitignore`):**

```
CGO_ENABLED=0 go build -ldflags "-s -w" -o srv ./cmd/nocx-server            → 42,143,906 B (40.19 MiB)
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o hlp ./cmd/nocx-helper  →  2,949,282 B ( 2.81 MiB)
```

This decides **one** thing and is not asked to decide more: do not deploy the full
coordinator as the execution host. Four cross-compiled coordinators would embed at ~160 MB
raw against ~11 MB for helpers — a raw extrapolation, not a shipped download size, since the
artifacts are gzipped.

**Owed before the local execution host ships: the local carrier budget** (D11), with
thresholds and a consequence rather than a number alone: p50/p99 keystroke-to-echo against
today's direct PTY; sustained output throughput; coordinator and helper CPU; wakeups and
context switches; fairness of an interactive tab beside a flooding one; and backpressure onset
with its memory bound. Failing the budget optimises the carrier, never the ownership.

**Measured, and no longer owed: the window's bound** (D8). An earlier draft carried 256 KiB
forward as a named deferral, which satisfied `nocx-8mllr` procedurally and evaded it in
substance. Measured 2026-08-31 on this machine, each command run on a **real pty** — so
colour, progress and repaints are present, which piping suppresses — with peak bytes in any
rolling one-second window, because an average tells a bounded buffer nothing:

| command                   | peak/1 s | 256 KiB survives |
| ------------------------- | -------: | ---------------: |
| `go build ./...` (cold)   |  1,412 B |            186 s |
| `go vet ./...`            |  1,920 B |            137 s |
| `npm run build` (vite)    |  4,480 B |             58 s |
| `npm ls --all`            | 47,861 B |            5.5 s |
| `go test -v` (2 packages) | 57,965 B |            4.5 s |
| `cat` a 238 KB file       |   238 KB |            1.1 s |
| `find / -xdev -type f`    |  10.1 MB |           0.03 s |

**Coordinator start to `nocx-server ready`: 17 ms, 0 ms, 16 ms** over three runs on an empty
profile with the OS keystore out of play. That is a **floor**, not a replacement: it excludes
the bundle swap, a populated `content.db`, and the vault.

Three conclusions, and the third is the one that changes a decision:

1. **For the common case 256 KiB is generous.** Compiles and builds peak in the low kilobytes
   per second; the window holds one to three minutes of them.
2. **A bigger window does not buy its way out.** The distribution spans four orders of
   magnitude. Going 256 KiB → 8 MiB is 32×, which turns `go test -v`'s 4.5 s into two minutes
   and `find /`'s 0.03 s into one second. Nothing in memory covers a bulk dump, and sizing for
   one would cost every idle session the memory.
3. **The bound is not what decides the loss — the length of the read gap is.** Against a 17 ms
   machine floor even `find /` loses ~170 KB. But a replacement can wait on a **person**:
   nocx-server D9 seals the vault after a departure window, and a session needing a secret
   suspends until someone returns. Human time inside the read gap makes any fixed bound
   irrelevant.

So the bound stays **256 KiB**, and it is now a measured choice rather than an inherited one.
What the measurement buys instead is **D16**, which removes human time from the gap.

## 7. Deliberately out of scope — and document 2

**One workspace from two machines at once** is its own design, because it is not an extension
of durable sessions: it introduces discovery of sessions a machine never created, admission
between coordinators at different versions, recorder authority, geometry arbitration between
two focused viewers, global commit ordering and key distribution. Specifically deferred there:

- **A workspace as a namespace on the execution host** — including whether naming is
  organisation or authorisation, which differs entirely between two Unix accounts and one.
- **N coordinators per session.** The nocx-server design's D8 stands here unchanged; note that
  the current ring is not "two generic cursors" — `acked` is a scalar, `attached` and
  `recording` are bools, and `ws.go:61` has one subscriber slot — so this is a state-machine
  rewrite, not a generalisation. D8 above removes most of its motivation by making readers
  stateless, which is why it is worth doing in that order.
- **A host-side authoritative ledger.** `ingest_seq` is assigned from a one-row counter in the
  same transaction as the insert, with idempotency and payload-conflict detection
  (`ledger_sqlite.go:114`, `:156`); two coordinators cannot independently assign that order.
  And the key is load-bearing rather than polish: without shared key material neither machine
  reads the other's blobs, so nothing can reconstruct the ledger as a whole — and ADR-0018 §5
  deliberately does not migrate the content key. Per-coordinator ledgers stand until that
  design exists.

**Also out of scope here:** read-only observers; autonomous work with no coordinator attached
(`ws_agent.go:756` stands — the helper keeps the process alive, it does not advance the
agent); sandbox implementation (D2 places it helper-side, and it is greenfield: no sandbox
package exists in this tree, `grep -l 'landlock|seatbelt|sandbox-exec' --include=*.go` is
empty and ADR-0035 is now about AppImage); the `files` and `ports` services; Windows remote
hosts; and graceful coordinator handover (A2 — replacement stays sequential, which _avoids_
ADR-0043 rather than solving multi-process encrypted SQLite).

## 8. Open questions

1. **The local carrier budget** (§6) — owed as numbers with thresholds before the local
   execution host ships. Not blocking acceptance: it decides a carrier, not an ownership.
2. **`nocx-22k1c`** is answered for the case this document creates (D8 chooses D6-b). What
   remains with that bead is narrower: what the _recorder_ does when it is slower than the
   source while a coordinator **is** attached — which D8's `Skip` gives it a way to express
   but does not set a policy for.
3. **A full-repaint criterion** for ADR-0041's grid (D9). Without it the grid stays `unknown`
   after an attach, which is the safe answer; with it, §0 could promise the screen as well as
   the process. Not blocking, because the safe answer is available today.
4. **Bulk output is not covered by any in-memory bound** (§6, conclusion 2). A session
   dumping megabytes per second loses output across any read gap longer than a moment. This
   document accepts that and states it; whether some sessions deserve a durable spool is a
   later question, and one D11's opaque store could answer without changing this design.

## 9. What must exist before this design is accepted

Two things, and neither is deferred implementation detail: each is a place where several
plausible implementations satisfy every word above and make D1a **false**. (The window's
bound was a third until it was measured — see §6.)

**1. Content-store restart reconciliation, as its own document.** `dropDeadSessions`
(`sqlite.go:410`) deletes every session row and every `session_output` recording at store open,
because a session "cannot outlive" its backend — the premise this design removes. As it stands a
fresh coordinator destroys the recordings of live sessions and terminalizes their open ledger
entries as `unknown`. It must define: how live sessions are discovered from the helper before
the sweep runs; which rows are preserved and which are proven absent; how an open entry is
reconciled without declaring a running process finished; and how all of that happens without a
second connection to the encrypted store (ADR-0043).

**2. The durable lifecycle protocol, as its own document.** The clearest way to satisfy every
word above and still break D1a is a daemon that treats bridge EOF as shutdown — which is what
the code does today (`host.go:154`). It must define daemonization and parent-death behaviour; deterministic endpoint derivation and
its short-path encoding; atomic socket establishment and stale-path recovery; peer
authentication and attach authorisation; durable generation-bearing handles; the machine
namespace for mutable generation state (D5); reader ownership and the single-writer rule; the
SSH streamlocal and exec-bridge behaviour; reconnect after carrier loss and after coordinator
replacement; the lifetime and prune lock with its tombstone reconciliation; and shutdown rules
distinguishing bridge EOF, last detach, process exit, explicit close and uninstall.
